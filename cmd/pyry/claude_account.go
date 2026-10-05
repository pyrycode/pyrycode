package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/pyrycode/pyrycode/internal/streamsup"
)

// claudeAccountSourceEnv and claudeAccountFileName are the two lower-priority
// places an instance's Claude account source can come from, after the
// -pyry-claude-account-source flag (#2824).
const (
	claudeAccountSourceEnv = "PYRY_CLAUDE_ACCOUNT_SOURCE"
	claudeAccountFileName  = "claude-account.json"
	claudeAccountFlagName  = "pyry-claude-account-source"
)

// The 1Password CLI an op:// source runs is chosen by its own flag, variable
// and claude-account.json key (#2825), in that order, defaulting to op.
const (
	opReferencePrefix          = "op://"
	claudeAccountOpCLIEnv      = "PYRY_CLAUDE_ACCOUNT_OP_CLI"
	claudeAccountOpCLIFlagName = "pyry-claude-account-op-cli"
	claudeAccountOpCLIDefault  = "op"
)

// maxAccountTokenBytes caps one token file. A subscription OAuth token is far
// shorter; anything larger is refused rather than read in full.
const maxAccountTokenBytes = 4096

// claudeAccountStartupRead bounds the one read runSupervisor makes before any
// session exists, matching the per-attempt deadline streamsup applies.
const claudeAccountStartupRead = 10 * time.Second

// Accessor states. not-configured means no provider is set and launches keep
// whatever credentials they inherit.
const (
	claudeAccountNotConfigured = "not-configured"
	claudeAccountReady         = "ready"
	claudeAccountFailed        = "failed"
)

// claudeAccountStatus is what the accessor reports about itself. It never
// carries the token or the source path, so it is safe to log or surface.
type claudeAccountStatus struct {
	Kind   string
	State  string
	Reason string
}

// accountReadError is a refusal whose text is fixed by the daemon. It never
// wraps an OS or exec error, because those carry the path, the CLI or the
// 1Password reference.
type accountReadError struct {
	reason string
}

func (e *accountReadError) Error() string { return e.reason }

// claudeAccount is the instance-scoped accessor: one per daemon, shared by
// every Claude runner it creates. Its read is the streamsup provider, so each
// launch attempt re-reads the source and a failed read never reuses an older
// token. The token itself is never held here.
type claudeAccount struct {
	kind   string
	reader streamsup.AccountTokenProvider
	logger *slog.Logger

	mu     sync.Mutex
	state  string
	reason string
}

// newClaudeAccount selects this instance's source and builds its accessor. An
// unset source returns a not-configured accessor. A source this build cannot
// read, or an unusable claude-account.json, is a startup error naming only
// where the setting came from: the value could be a token pasted by mistake.
// The 1Password CLI setting is resolved only for an op:// source, so a file
// source never depends on it.
func newClaudeAccount(flagValue, envValue, opCLIFlag, opCLIEnv, instanceDir string, logger *slog.Logger) (*claudeAccount, error) {
	source, origin, err := resolveClaudeAccountSource(flagValue, envValue, instanceDir)
	if err != nil {
		return nil, err
	}
	a := &claudeAccount{logger: logger, state: claudeAccountNotConfigured}
	switch {
	case source == "":
		return a, nil
	case strings.HasPrefix(source, opReferencePrefix):
		if len(source) == len(opReferencePrefix) || hasControlByte(source) {
			return nil, fmt.Errorf("claude account source from %s: malformed 1Password reference", origin)
		}
		cli, cliOrigin, err := resolveClaudeAccountOpCLI(opCLIFlag, opCLIEnv, instanceDir)
		if err != nil {
			return nil, err
		}
		if !validOpCLI(cli) {
			return nil, fmt.Errorf("claude account 1Password CLI from %s: want one executable name or one absolute path", cliOrigin)
		}
		a.kind = "1password"
		a.reader = func(ctx context.Context) (string, streamsup.AccountTokenFailure, error) {
			return readOpReference(ctx, cli, source)
		}
	case filepath.IsAbs(source):
		a.kind = "file"
		a.reader = func(ctx context.Context) (string, streamsup.AccountTokenFailure, error) {
			return readTokenFile(ctx, source)
		}
	default:
		return nil, fmt.Errorf("claude account source from %s: unsupported source (want an absolute file path or an op:// reference)", origin)
	}
	return a, nil
}

// resolveClaudeAccountSource returns the first nonempty source among the flag,
// the environment variable and the "source" string in the instance's
// claude-account.json, plus a label for where it came from. The file is read
// only when the flag and the variable are both empty, and an absent file means
// no source.
func resolveClaudeAccountSource(flagValue, envValue, instanceDir string) (source, origin string, err error) {
	if flagValue != "" {
		return flagValue, "flag -" + claudeAccountFlagName, nil
	}
	if envValue != "" {
		return envValue, "env " + claudeAccountSourceEnv, nil
	}
	return readClaudeAccountField(instanceDir, "source")
}

// resolveClaudeAccountOpCLI picks the 1Password CLI from the flag, the
// environment variable, then the "op_cli" string in claude-account.json,
// defaulting to op. It is independent of where the source came from.
func resolveClaudeAccountOpCLI(flagValue, envValue, instanceDir string) (cli, origin string, err error) {
	if flagValue != "" {
		return flagValue, "flag -" + claudeAccountOpCLIFlagName, nil
	}
	if envValue != "" {
		return envValue, "env " + claudeAccountOpCLIEnv, nil
	}
	cli, origin, err = readClaudeAccountField(instanceDir, "op_cli")
	if err != nil || cli != "" {
		return cli, origin, err
	}
	return claudeAccountOpCLIDefault, "default", nil
}

// readClaudeAccountField returns the string under key in the instance's
// claude-account.json and the file as its origin. An absent file or key is an
// empty value; other keys are ignored.
func readClaudeAccountField(instanceDir, key string) (value, origin string, err error) {
	path := filepath.Join(instanceDir, claudeAccountFileName)
	origin = "file " + path
	// O_NONBLOCK keeps a FIFO at this path from blocking startup before the
	// type check below refuses it.
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if errors.Is(err, fs.ErrNotExist) {
		return "", "", nil
	}
	if err != nil {
		return "", "", fmt.Errorf("claude account source from %s: unreadable", origin)
	}
	defer func() { _ = f.Close() }()
	// The file chooses which owner-only file is read, or which executable is
	// run, to produce the token handed to Claude, so a file another user could
	// write would let them redirect that choice.
	info, err := f.Stat()
	if err != nil {
		return "", "", fmt.Errorf("claude account source from %s: unreadable", origin)
	}
	if !info.Mode().IsRegular() || !ownedByEUID(info) || info.Mode().Perm()&0o022 != 0 {
		return "", "", fmt.Errorf("claude account source from %s: must be a regular file owned by this user and not group or other writable", origin)
	}
	data, err := io.ReadAll(io.LimitReader(f, 1<<16))
	if err != nil {
		return "", "", fmt.Errorf("claude account source from %s: unreadable", origin)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return "", "", fmt.Errorf("claude account source from %s: invalid JSON", origin)
	}
	raw, ok := fields[key]
	if !ok {
		return "", "", nil
	}
	if err := json.Unmarshal(raw, &value); err != nil || string(raw) == "null" {
		return "", "", fmt.Errorf("claude account source from %s: %q must be a string", origin, key)
	}
	return value, origin, nil
}

// validOpCLI admits one bare executable name, looked up on PATH, or one
// absolute path, which may contain spaces. Neither is ever given to a shell.
func validOpCLI(cli string) bool {
	if filepath.IsAbs(cli) {
		return !hasControlByte(cli)
	}
	if cli == "" || cli == "." || cli == ".." {
		return false
	}
	for _, r := range cli {
		ok := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("._+-", r)
		if !ok {
			return false
		}
	}
	return true
}

func hasControlByte(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] == 0x7f {
			return true
		}
	}
	return false
}

// readTokenFile reads one raw token from an owner-only regular file, opened
// afresh on every call. Type, owner and mode are checked on the descriptor the
// bytes are read from, so a swapped path cannot slip a different file past the
// checks, and O_NONBLOCK keeps a FIFO from blocking the open.
func readTokenFile(ctx context.Context, path string) (string, streamsup.AccountTokenFailure, error) {
	if err := ctx.Err(); err != nil {
		return "", streamsup.AccountTokenCancellation, err
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return accountReadFailure(streamsup.AccountTokenReadFailure, "token file missing")
		}
		return accountReadFailure(streamsup.AccountTokenReadFailure, "token file unreadable")
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return accountReadFailure(streamsup.AccountTokenReadFailure, "token file unreadable")
	}
	switch {
	case !info.Mode().IsRegular():
		return accountReadFailure(streamsup.AccountTokenReadFailure, "token file is not a regular file")
	case !ownedByEUID(info):
		return accountReadFailure(streamsup.AccountTokenReadFailure, "token file owned by another user")
	case info.Mode().Perm()&0o077 != 0:
		return accountReadFailure(streamsup.AccountTokenReadFailure, "token file allows group or other access")
	}
	data, err := io.ReadAll(io.LimitReader(f, maxAccountTokenBytes+1))
	if err != nil {
		return accountReadFailure(streamsup.AccountTokenReadFailure, "token file unreadable")
	}
	if len(data) > maxAccountTokenBytes {
		return accountReadFailure(streamsup.AccountTokenInvalidOutput, "token file too large")
	}
	token, failure := parseTokenBytes(data)
	switch failure {
	case streamsup.AccountTokenEmptyOutput:
		return accountReadFailure(failure, "token file empty")
	case streamsup.AccountTokenInvalidOutput:
		return accountReadFailure(failure, "token file content invalid")
	}
	if err := ctx.Err(); err != nil {
		return "", streamsup.AccountTokenCancellation, err
	}
	return token, "", nil
}

// parseTokenBytes strips one trailing LF or CRLF and accepts only a nonempty
// run of printable non-space ASCII.
func parseTokenBytes(data []byte) (string, streamsup.AccountTokenFailure) {
	switch {
	case len(data) >= 2 && data[len(data)-2] == '\r' && data[len(data)-1] == '\n':
		data = data[:len(data)-2]
	case len(data) >= 1 && data[len(data)-1] == '\n':
		data = data[:len(data)-1]
	}
	if len(data) == 0 {
		return "", streamsup.AccountTokenEmptyOutput
	}
	for _, b := range data {
		if b < 0x21 || b > 0x7e {
			return "", streamsup.AccountTokenInvalidOutput
		}
	}
	return string(data), ""
}

// opWaitDelay bounds how long the background Wait on a killed 1Password CLI
// may wait for a descendant that escaped its process group and still holds
// stdout. The launch itself never waits for it.
const opWaitDelay = time.Second

// readOpReference runs "<cli> read <ref>" with no shell, no stdin and stderr
// discarded, and takes its capped stdout as the token. The CLI runs in its own
// process group, killed as a group when ctx ends, and the read returns as
// soon as ctx ends whether or not the CLI has exited.
func readOpReference(ctx context.Context, cli, ref string) (string, streamsup.AccountTokenFailure, error) {
	if err := ctx.Err(); err != nil {
		return opContextFailure(err)
	}
	var stdout cappedBuffer
	cmd := exec.CommandContext(ctx, cli, "read", ref)
	cmd.Stdout = &stdout
	cmd.Env = withoutAccountToken(os.Environ())
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = opWaitDelay
	if err := cmd.Start(); err != nil {
		return accountReadFailure(streamsup.AccountTokenReadFailure, "1Password CLI unavailable")
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	var waitErr error
	select {
	case waitErr = <-done:
	case <-ctx.Done():
		return opContextFailure(ctx.Err())
	}
	if err := ctx.Err(); err != nil {
		return opContextFailure(err)
	}
	if waitErr != nil {
		return accountReadFailure(streamsup.AccountTokenReadFailure, "1Password read failed")
	}
	data := stdout.Bytes()
	if len(data) > maxAccountTokenBytes {
		return accountReadFailure(streamsup.AccountTokenInvalidOutput, "1Password output invalid")
	}
	token, failure := parseTokenBytes(data)
	switch failure {
	case streamsup.AccountTokenEmptyOutput:
		return accountReadFailure(failure, "1Password output empty")
	case streamsup.AccountTokenInvalidOutput:
		return accountReadFailure(failure, "1Password output invalid")
	}
	return token, "", nil
}

func opContextFailure(err error) (string, streamsup.AccountTokenFailure, error) {
	if errors.Is(err, context.DeadlineExceeded) {
		return accountReadFailure(streamsup.AccountTokenTimeout, "1Password read timed out")
	}
	return accountReadFailure(streamsup.AccountTokenCancellation, "1Password read cancelled")
}

// withoutAccountToken drops any inherited Claude token from the CLI's
// environment; the CLI has no use for it.
func withoutAccountToken(env []string) []string {
	out := make([]string, 0, len(env))
	for _, entry := range env {
		if !strings.HasPrefix(entry, "CLAUDE_CODE_OAUTH_TOKEN=") {
			out = append(out, entry)
		}
	}
	return out
}

// cappedBuffer keeps the first maxAccountTokenBytes+1 bytes written to it and
// discards the rest, so an oversized output is detectable without being held.
// exec's copying goroutine is its only writer, and Bytes is read only after
// Wait has returned.
type cappedBuffer struct {
	buf []byte
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	if room := maxAccountTokenBytes + 1 - len(b.buf); room > 0 {
		b.buf = append(b.buf, p[:min(room, len(p))]...)
	}
	return len(p), nil
}

func (b *cappedBuffer) Bytes() []byte { return b.buf }

func accountReadFailure(failure streamsup.AccountTokenFailure, reason string) (string, streamsup.AccountTokenFailure, error) {
	return "", failure, &accountReadError{reason: reason}
}

func ownedByEUID(info fs.FileInfo) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(st.Uid) == os.Geteuid()
}

// provider is what the Claude runner factory installs: nil when no source is
// configured, so launches keep their inherited credentials.
func (a *claudeAccount) provider() streamsup.AccountTokenProvider {
	if a == nil || a.reader == nil {
		return nil
	}
	return a.read
}

// prime makes the one bounded startup read. The bootstrap session stays
// dormant until a turn needs it, so this is what reports a boot-time failure.
// A failure is logged and recorded, never fatal.
func (a *claudeAccount) prime(ctx context.Context) {
	if a.reader == nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, claudeAccountStartupRead)
	defer cancel()
	_, _, _ = a.read(ctx)
}

// read is the provider: it reads the source afresh and records the outcome.
// The mutex covers only the state, never the read itself.
func (a *claudeAccount) read(ctx context.Context) (string, streamsup.AccountTokenFailure, error) {
	token, failure, err := a.reader(ctx)
	if err != nil || failure != "" {
		reason := "token read failed"
		var tfe *accountReadError
		switch {
		case errors.As(err, &tfe):
			reason = tfe.reason
		case failure == streamsup.AccountTokenCancellation || errors.Is(err, context.Canceled):
			reason = "token read cancelled"
		case failure == streamsup.AccountTokenTimeout || errors.Is(err, context.DeadlineExceeded):
			reason = "token read timed out"
		}
		a.mu.Lock()
		a.state, a.reason = claudeAccountFailed, reason
		a.mu.Unlock()
		a.logger.Warn("claude account: token read failed; claude launch refused", "kind", a.kind, "reason", reason)
		return "", failure, err
	}
	a.mu.Lock()
	recovered := a.state == claudeAccountFailed
	a.state, a.reason = claudeAccountReady, ""
	a.mu.Unlock()
	if recovered {
		a.logger.Info("claude account: token read recovered", "kind", a.kind)
	}
	return token, "", nil
}

// status reports the source kind and the outcome of the latest read.
func (a *claudeAccount) status() claudeAccountStatus {
	a.mu.Lock()
	defer a.mu.Unlock()
	return claudeAccountStatus{Kind: a.kind, State: a.state, Reason: a.reason}
}
