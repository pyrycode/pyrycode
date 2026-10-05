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
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/pyrycode/pyrycode/internal/protocol"
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

// keychainPrefix marks an OS secret store source (#2815): the macOS Keychain
// on darwin, the Secret Service on Linux. The rest is the item's service name.
const keychainPrefix = "keychain:"

// claudeAccountGOOS picks the keychain tool. Tests set it to exercise both
// platforms' read paths on one host.
var claudeAccountGOOS = runtime.GOOS

// maxAccountTokenBytes caps one token file. A subscription OAuth token is far
// shorter; anything larger is refused rather than read in full.
const maxAccountTokenBytes = 4096

// maxAccountLabelBytes caps the operator label paired clients are shown.
const maxAccountLabelBytes = 64

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
	Label  string
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
	label  string
	reader streamsup.AccountTokenProvider
	logger *slog.Logger

	mu     sync.Mutex
	state  string
	reason string
	// started numbers each read as it begins; recorded is the number of the
	// read whose outcome state and reason hold. Reads overlap, so an outcome
	// is recorded only when no later-started read has recorded one already.
	started  uint64
	recorded uint64
}

// newClaudeAccount selects this instance's source and builds its accessor. An
// unset source returns a not-configured accessor. A source this build cannot
// read, or an unusable claude-account.json, is a startup error naming only
// where the setting came from: the value could be a token pasted by mistake.
// The 1Password CLI setting is resolved only for an op:// source, so a file
// source never depends on it. The label is read only when claude-account.json
// supplies the source, so it always describes the source it sits beside.
func newClaudeAccount(flagValue, envValue, opCLIFlag, opCLIEnv, instanceDir string, logger *slog.Logger) (*claudeAccount, error) {
	source, origin, err := resolveClaudeAccountSource(flagValue, envValue, instanceDir)
	if err != nil {
		return nil, err
	}
	a := &claudeAccount{logger: logger, state: claudeAccountNotConfigured}
	if flagValue == "" && envValue == "" && source != "" {
		label, labelOrigin, err := readClaudeAccountField(instanceDir, "label")
		if err != nil {
			return nil, err
		}
		if !validAccountLabel(label) {
			return nil, fmt.Errorf("claude account label from %s: must be at most %d bytes of printable UTF-8", labelOrigin, maxAccountLabelBytes)
		}
		a.label = label
	}
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
	case strings.HasPrefix(source, keychainPrefix):
		name := strings.TrimPrefix(source, keychainPrefix)
		if name == "" || hasControlByte(name) || strings.HasPrefix(name, "-") {
			return nil, fmt.Errorf("claude account source from %s: malformed keychain item name", origin)
		}
		tool, args, ok := keychainCommand(claudeAccountGOOS, name)
		if !ok {
			return nil, fmt.Errorf("claude account source from %s: keychain sources are supported only on macOS and Linux", origin)
		}
		a.kind = "os_keychain"
		a.reader = func(ctx context.Context) (string, streamsup.AccountTokenFailure, error) {
			return runTokenCommand(ctx, tool, args, keychainReasons)
		}
	case filepath.IsAbs(source):
		a.kind = "file"
		a.reader = func(ctx context.Context) (string, streamsup.AccountTokenFailure, error) {
			return readTokenFile(ctx, source)
		}
	default:
		return nil, fmt.Errorf("claude account source from %s: unsupported source (want an absolute file path, an op:// reference or keychain:<name>)", origin)
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
	// Unmarshal would replace invalid UTF-8 with U+FFFD, so it is refused on
	// the raw bytes first.
	if err := json.Unmarshal(raw, &value); err != nil || string(raw) == "null" || !utf8.Valid(raw) {
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

// validAccountLabel admits up to maxAccountLabelBytes of valid UTF-8 with no
// control character, C1 included, since clients render it.
func validAccountLabel(label string) bool {
	if len(label) > maxAccountLabelBytes || !utf8.ValidString(label) {
		return false
	}
	for _, r := range label {
		if unicode.IsControl(r) {
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

// opWaitDelay bounds how long the background Wait on a killed token command
// may wait for a descendant that escaped its process group and still holds
// stdout. The launch itself never waits for it.
const opWaitDelay = time.Second

// commandReasons are the fixed status reasons one token command reports. None
// carries the command, its arguments or its output.
type commandReasons struct {
	unavailable, failed, empty, invalid, timedOut, cancelled string
}

var opReasons = commandReasons{
	unavailable: "1Password CLI unavailable",
	failed:      "1Password read failed",
	empty:       "1Password output empty",
	invalid:     "1Password output invalid",
	timedOut:    "1Password read timed out",
	cancelled:   "1Password read cancelled",
}

var keychainReasons = commandReasons{
	unavailable: "keychain tool unavailable",
	failed:      "keychain read failed",
	empty:       "keychain output empty",
	invalid:     "keychain output invalid",
	timedOut:    "keychain read timed out",
	cancelled:   "keychain read cancelled",
}

// readOpReference runs "<cli> read <ref>" and takes its stdout as the token.
func readOpReference(ctx context.Context, cli, ref string) (string, streamsup.AccountTokenFailure, error) {
	return runTokenCommand(ctx, cli, []string{"read", ref}, opReasons)
}

// keychainCommand is the tool and argument vector that print the password of
// the item whose service is name, on the platforms that have one.
func keychainCommand(goos, name string) (tool string, args []string, ok bool) {
	switch goos {
	case "darwin":
		return "security", []string{"find-generic-password", "-s", name, "-w"}, true
	case "linux":
		return "secret-tool", []string{"lookup", "service", name}, true
	}
	return "", nil, false
}

// runTokenCommand runs tool with args, no shell, no stdin and stderr
// discarded, and takes its capped stdout as the token. The tool runs in its
// own process group, killed as a group when ctx ends, and the read returns as
// soon as ctx ends whether or not the tool has exited.
func runTokenCommand(ctx context.Context, tool string, args []string, reasons commandReasons) (string, streamsup.AccountTokenFailure, error) {
	if err := ctx.Err(); err != nil {
		return commandContextFailure(err, reasons)
	}
	var stdout cappedBuffer
	cmd := exec.CommandContext(ctx, tool, args...)
	cmd.Stdout = &stdout
	cmd.Env = withoutAccountToken(os.Environ())
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = opWaitDelay
	if err := cmd.Start(); err != nil {
		return accountReadFailure(streamsup.AccountTokenReadFailure, reasons.unavailable)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	var waitErr error
	select {
	case waitErr = <-done:
	case <-ctx.Done():
		return commandContextFailure(ctx.Err(), reasons)
	}
	if err := ctx.Err(); err != nil {
		return commandContextFailure(err, reasons)
	}
	if waitErr != nil {
		return accountReadFailure(streamsup.AccountTokenReadFailure, reasons.failed)
	}
	data := stdout.Bytes()
	if len(data) > maxAccountTokenBytes {
		return accountReadFailure(streamsup.AccountTokenInvalidOutput, reasons.invalid)
	}
	token, failure := parseTokenBytes(data)
	switch failure {
	case streamsup.AccountTokenEmptyOutput:
		return accountReadFailure(failure, reasons.empty)
	case streamsup.AccountTokenInvalidOutput:
		return accountReadFailure(failure, reasons.invalid)
	}
	return token, "", nil
}

func commandContextFailure(err error, reasons commandReasons) (string, streamsup.AccountTokenFailure, error) {
	if errors.Is(err, context.DeadlineExceeded) {
		return accountReadFailure(streamsup.AccountTokenTimeout, reasons.timedOut)
	}
	return accountReadFailure(streamsup.AccountTokenCancellation, reasons.cancelled)
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

// read is the provider: it reads the source afresh and records the outcome
// unless a read that started after it has recorded one first. The mutex covers
// only the state and the read numbers, never the read itself. The caller always
// gets its own read's result.
func (a *claudeAccount) read(ctx context.Context) (string, streamsup.AccountTokenFailure, error) {
	a.mu.Lock()
	a.started++
	seq := a.started
	a.mu.Unlock()
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
		if seq > a.recorded {
			a.recorded = seq
			a.state, a.reason = claudeAccountFailed, reason
		}
		a.mu.Unlock()
		a.logger.Warn("claude account: token read failed; claude launch refused", "kind", a.kind, "reason", reason)
		return "", failure, err
	}
	a.mu.Lock()
	recovered := false
	if seq > a.recorded {
		a.recorded = seq
		recovered = a.state == claudeAccountFailed
		a.state, a.reason = claudeAccountReady, ""
	}
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
	return claudeAccountStatus{Kind: a.kind, Label: a.label, State: a.state, Reason: a.reason}
}

// ClaudeAccount is the relay's view of status: the accessor's kind and state
// mapped to the wire vocabulary. It carries no token, path or reference.
func (a *claudeAccount) ClaudeAccount() protocol.ClaudeAccountPayload {
	return claudeAccountPayload(a.status())
}

func claudeAccountPayload(s claudeAccountStatus) protocol.ClaudeAccountPayload {
	kind := s.Kind
	if kind == "" {
		kind = protocol.ClaudeAccountKindMachineLogin
	}
	state := s.State
	if state == claudeAccountNotConfigured {
		state = protocol.ClaudeAccountStateNotConfigured
	}
	return protocol.ClaudeAccountPayload{Kind: kind, Label: s.Label, State: state, Reason: s.Reason}
}
