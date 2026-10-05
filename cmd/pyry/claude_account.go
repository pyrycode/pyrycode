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
	"path/filepath"
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

// tokenFileError is a refusal whose text is fixed by the daemon. It never wraps
// an OS error, because those carry the path.
type tokenFileError struct {
	reason string
}

func (e *tokenFileError) Error() string { return e.reason }

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
func newClaudeAccount(flagValue, envValue, instanceDir string, logger *slog.Logger) (*claudeAccount, error) {
	source, origin, err := resolveClaudeAccountSource(flagValue, envValue, instanceDir)
	if err != nil {
		return nil, err
	}
	a := &claudeAccount{logger: logger, state: claudeAccountNotConfigured}
	if source == "" {
		return a, nil
	}
	if !filepath.IsAbs(source) {
		return nil, fmt.Errorf("claude account source from %s: unsupported source (want an absolute file path)", origin)
	}
	a.kind = "file"
	a.reader = func(ctx context.Context) (string, streamsup.AccountTokenFailure, error) {
		return readTokenFile(ctx, source)
	}
	return a, nil
}

// resolveClaudeAccountSource returns the first nonempty source among the flag,
// the environment variable and the "source" string in the instance's
// claude-account.json, plus a label for where it came from. The file is read
// only when the flag and the variable are both empty, and an absent file means
// no source. Keys other than "source" are ignored.
func resolveClaudeAccountSource(flagValue, envValue, instanceDir string) (source, origin string, err error) {
	if flagValue != "" {
		return flagValue, "flag -" + claudeAccountFlagName, nil
	}
	if envValue != "" {
		return envValue, "env " + claudeAccountSourceEnv, nil
	}
	path := filepath.Join(instanceDir, claudeAccountFileName)
	origin = "file " + path
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", "", nil
	}
	if err != nil {
		return "", "", fmt.Errorf("claude account source from %s: unreadable", origin)
	}
	defer func() { _ = f.Close() }()
	// The file chooses which owner-only file is read and handed to Claude, so a
	// file another user could write would let them redirect that choice.
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
	raw, ok := fields["source"]
	if !ok {
		return "", "", nil
	}
	if err := json.Unmarshal(raw, &source); err != nil || string(raw) == "null" {
		return "", "", fmt.Errorf("claude account source from %s: \"source\" must be a string", origin)
	}
	return source, origin, nil
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
			return tokenFileFailure(streamsup.AccountTokenReadFailure, "token file missing")
		}
		return tokenFileFailure(streamsup.AccountTokenReadFailure, "token file unreadable")
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return tokenFileFailure(streamsup.AccountTokenReadFailure, "token file unreadable")
	}
	switch {
	case !info.Mode().IsRegular():
		return tokenFileFailure(streamsup.AccountTokenReadFailure, "token file is not a regular file")
	case !ownedByEUID(info):
		return tokenFileFailure(streamsup.AccountTokenReadFailure, "token file owned by another user")
	case info.Mode().Perm()&0o077 != 0:
		return tokenFileFailure(streamsup.AccountTokenReadFailure, "token file allows group or other access")
	}
	data, err := io.ReadAll(io.LimitReader(f, maxAccountTokenBytes+1))
	if err != nil {
		return tokenFileFailure(streamsup.AccountTokenReadFailure, "token file unreadable")
	}
	if len(data) > maxAccountTokenBytes {
		return tokenFileFailure(streamsup.AccountTokenInvalidOutput, "token file too large")
	}
	switch {
	case len(data) >= 2 && data[len(data)-2] == '\r' && data[len(data)-1] == '\n':
		data = data[:len(data)-2]
	case len(data) >= 1 && data[len(data)-1] == '\n':
		data = data[:len(data)-1]
	}
	if len(data) == 0 {
		return tokenFileFailure(streamsup.AccountTokenEmptyOutput, "token file empty")
	}
	for _, b := range data {
		if b < 0x21 || b > 0x7e {
			return tokenFileFailure(streamsup.AccountTokenInvalidOutput, "token file content invalid")
		}
	}
	if err := ctx.Err(); err != nil {
		return "", streamsup.AccountTokenCancellation, err
	}
	return string(data), "", nil
}

func tokenFileFailure(failure streamsup.AccountTokenFailure, reason string) (string, streamsup.AccountTokenFailure, error) {
	return "", failure, &tokenFileError{reason: reason}
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
		var tfe *tokenFileError
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
