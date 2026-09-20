// Command pyry is the Pyrycode daemon — a process supervisor for Claude Code.
//
// pyry is designed to be a near-drop-in replacement for the `claude` CLI:
// arguments and flags it doesn't recognize are forwarded to claude verbatim.
// pyry's own configuration uses an explicit -pyry-* prefix so it never
// collides with claude's namespace, no matter how claude evolves.
//
//	pyry                              # supervised claude, no extra args
//	pyry "summarize foo.md"           # forwards as claude's initial prompt
//	pyry --model sonnet -p "..."      # any claude flags pass through
//	pyry -pyry-verbose -- --resume    # pyry flags first, then claude flags
//
// Reserved control verbs (pyry's own, no -pyry- prefix needed since they
// don't collide with anything claude does today):
//
//	pyry version          Print version and exit
//	pyry status           Query the running daemon via its control socket
//	pyry stop             Graceful shutdown via the control socket
//	pyry logs             Recent supervisor log lines
//	pyry sessions <verb>  Multi-session management (verbs: new, rm, rename, list)
//	pyry pair             Mint a device token and print the QR / paste payload
//	pyry install-service  Write a systemd / launchd unit file for pyry
//	pyry agent-run        Drive a single supervised claude turn headlessly
//	                       (replaces `claude -p` in the dispatcher)
//	pyry help             Show help
//
// See https://github.com/pyrycode/pyrycode for documentation.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	iofs "io/fs"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/pyrycode/pyrycode/internal/config"
	"github.com/pyrycode/pyrycode/internal/control"
	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/debugbundle"
	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/install"
	"github.com/pyrycode/pyrycode/internal/msgqueue"
	"github.com/pyrycode/pyrycode/internal/permbridge"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/relay"
	"github.com/pyrycode/pyrycode/internal/relay/handlers"
	"github.com/pyrycode/pyrycode/internal/sessions"
	"github.com/pyrycode/pyrycode/internal/streamsup"
	"github.com/pyrycode/pyrycode/internal/turnbridge"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// Version is set at build time via -ldflags "-X main.Version=...".
var Version = "dev"

// DefaultName is the instance name used when -pyry-name is unset and the
// PYRY_NAME environment variable is empty. It produces socket
// ~/.pyry/pyry.sock — the right thing for a single-pyry-per-user setup.
const DefaultName = "pyry"

// defaultName returns the instance name to use when -pyry-name was not given
// on the command line. The PYRY_NAME environment variable wins over
// DefaultName, so shell aliasing (`alias pyry-elli='PYRY_NAME=elli pyry'`)
// works for both supervisor mode and the control verbs.
func defaultName() string {
	if n := os.Getenv("PYRY_NAME"); n != "" {
		return n
	}
	return DefaultName
}

// resolveSocketPath returns the socket path to use given the parsed flags.
// If -pyry-socket was set explicitly, it wins. Otherwise the path is
// derived from the (sanitized) instance name as ~/.pyry/<name>.sock. Falls
// back to a CWD-relative path if $HOME can't be resolved.
func resolveSocketPath(socketFlag, name string) string {
	if socketFlag != "" {
		return socketFlag
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return sanitizeName(name) + ".sock"
	}
	return filepath.Join(home, ".pyry", sanitizeName(name)+".sock")
}

// resolveRegistryPath returns ~/.pyry/<sanitized-name>/sessions.json. Falls
// back to a CWD-relative path if $HOME can't be resolved (matches
// resolveSocketPath's contract).
func resolveRegistryPath(name string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return filepath.Join(sanitizeName(name), "sessions.json")
	}
	return filepath.Join(home, ".pyry", sanitizeName(name), "sessions.json")
}

// resolveInstanceDirPath returns ~/.pyry/<sanitized-name> — the per-instance
// directory the two registry paths above are built under, and the anchor
// attachments.EnsureDir files uploads beneath as
// conversations/<conversation-id>/attachments/<attachment-id> (#1897). Falls back
// to a CWD-relative path if $HOME can't be resolved (matches
// resolveRegistryPath's contract).
//
// The name is the operator's own -name flag, routed through the same sanitizeName
// as its siblings; nothing remote reaches this path. Containment for everything
// beneath it is EnsureDir's, which resolves this directory as its anchor and
// compares the destination against it.
func resolveInstanceDirPath(name string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return sanitizeName(name)
	}
	return filepath.Join(home, ".pyry", sanitizeName(name))
}

// resolveConversationsRegistryPath returns
// ~/.pyry/<sanitized-name>/conversations.json. Falls back to a CWD-relative
// path if $HOME can't be resolved (matches resolveRegistryPath's contract).
func resolveConversationsRegistryPath(name string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return filepath.Join(sanitizeName(name), "conversations.json")
	}
	return filepath.Join(home, ".pyry", sanitizeName(name), "conversations.json")
}

// resolveModelVocabularyPath returns ~/.pyry/<sanitized-name>/model_list.json —
// the daemon-wide model vocabulary this instance last saw (#2450), a sibling of
// the two registry files above. Falls back to a CWD-relative path if $HOME can't
// be resolved (matches resolveRegistryPath's contract).
//
// ONE FILE PER DAEMON INSTANCE is the grain because the vocabulary is a property
// of the machine and account rather than of a conversation — the same fact that
// licenses #2124's cross-conversation read — so it is keyed exactly as the
// instance's other daemon-wide state is and carries no conversation id.
//
// The name is the operator's own -name flag through the same sanitizeName as its
// siblings; nothing remote reaches this path.
func resolveModelVocabularyPath(name string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return filepath.Join(sanitizeName(name), "model_list.json")
	}
	return filepath.Join(home, ".pyry", sanitizeName(name), "model_list.json")
}

// resolveClaudeSessionsDir returns the directory where claude writes
// <uuid>.jsonl files for the given workdir. An empty workdir is resolved to
// the process cwd (matching claude's behaviour). Returns "" when the path
// cannot be resolved — startup proceeds without on-disk reconciliation.
func resolveClaudeSessionsDir(workdir string) string {
	if workdir == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return ""
		}
		workdir = cwd
	}
	abs, err := filepath.Abs(workdir)
	if err != nil {
		return ""
	}
	return sessions.DefaultClaudeSessionsDir(abs)
}

// resolveRecordingsDir returns the fixed directory where debug_capture writes
// the daemon's interactive-session .cast recordings: ~/.local/share/pyry-recordings
// — the non-synced, non-backed-up location the ptyrunner SECURITY: comment
// designates (sibling of ~/.local/share/pyry-artifacts/). Returns "" when $HOME
// cannot be resolved, in which case capture silently no-ops — the same
// degrade-to-empty contract as resolveClaudeSessionsDir. The directory is a
// compile-time-fixed location under $HOME; no untrusted input influences it.
func resolveRecordingsDir() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".local", "share", "pyry-recordings")
}

// resolveDefaultCwd returns the absolute working directory recorded on a
// conversation created (via the create_conversation handler) with a null cwd.
// It mirrors the bootstrap session's WorkDir resolution: the absolute form of
// workdir, or the process cwd when workdir is empty, so a created conversation's
// recorded cwd matches where the bootstrap session actually runs. Falls back to
// the raw value when the path cannot be made absolute (Getwd/Abs failure) so the
// call site always receives a usable string.
func resolveDefaultCwd(workdir string) string {
	if workdir == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return ""
		}
		workdir = cwd
	}
	abs, err := filepath.Abs(workdir)
	if err != nil {
		return workdir
	}
	return abs
}

// sanitizeName keeps a-z, A-Z, 0-9, _, ., - and replaces anything else with
// _. Empty input becomes "_", and a result of "." or ".." gains a trailing _
// ("._", ".._") — the same shape the transform already gives "./x" and
// "../x", where the separator becomes _. Defends the on-disk socket filename
// and the per-instance state directory against path-traversal and other
// filesystem-unsafe input (e.g. PYRY_NAME from a careless shell setup).
//
// Postcondition: the result is non-empty, holds no path separator, and is
// never "." or "..", so filepath.Join(dir, sanitizeName(name), file) always
// yields dir/<one-component>/file. Pinned by TestSanitizeName and by
// assertInsideInstanceDir's callers. A "." or ".." reaching filepath.Join as
// a live component put per-instance state in ~/.pyry or in $HOME itself; the
// keystore's own validator rejects both rather than transforming them, and
// deliberately stays separate — see internal/keys.validDaemonName.
func sanitizeName(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == '_', r == '.', r == '-':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	if b.Len() == 0 {
		return "_"
	}
	// Checked on the built result rather than the input, so the
	// postcondition keeps holding if the allowlist above ever changes.
	switch out := b.String(); out {
	case ".", "..":
		return out + "_"
	default:
		return out
	}
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "pyry:", err)
		os.Exit(1)
	}
}

// errAttachRemoved and errACPRemoved are what the `attach` and `acp` verbs
// return now that #1348 has deleted both implementations. They exist because
// the fall-through for an unrecognised first argument is a full daemon start:
// without an arm, `pyry attach` trust-marks the cwd in ~/.claude.json, binds
// the control socket, contacts the relay, and hands claude the verb string as
// its initial prompt. Same posture as the "pty" arm in selectInteractiveRunner
// — name the removal, and never render the dead thing as something to run.
//
// Sentinels rather than fmt.Errorf at the call site so callers and tests match
// on identity instead of prose.
var (
	errAttachRemoved = errors.New("attach was removed in #1348: the terminal path it bridged no longer exists, so there is nothing to attach to. Watch a live session from the desktop or mobile client instead")
	errACPRemoved    = errors.New("acp was removed in #1348: pyry no longer serves the ACP JSON-RPC transport over stdio. Remove the verb from the ACP host's launch configuration — there is no replacement")
)

func run() error { return runArgs(os.Args) }

// runArgs dispatches on args[1], where args is the full argv (args[0] is the
// program name). Split out of run so a test can drive the router without going
// through the process's real os.Args.
//
// The switch deliberately has no default: an unrecognised first argument
// forwards to claude verbatim, which is the near-drop-in design stated in this
// package's doc comment. Removed verbs therefore need explicit constant cases.
func runArgs(args []string) error {
	if len(args) >= 2 {
		switch args[1] {
		case "version", "-v", "--version":
			fmt.Println("pyry", Version)
			return nil
		case "status":
			return runStatus(args[2:])
		case "stop":
			return runStop(args[2:])
		case "logs":
			return runLogs(args[2:])
		case "sessions":
			return runSessions(args[2:])
		case "channel":
			return runChannel(args[2:])
		case "pair":
			return runPair(args[2:])
		case "rekey":
			return runRekey(args[2:])
		case "install-service":
			return runInstallService(args[2:])
		case "update":
			return runUpdate(args[2:])
		case "agent-run":
			return runAgentRun(os.Stdout, args[2:])
		case "mcp-approve":
			return runMCPApprove(args[2:])
		case "mcp-files":
			return runMCPFiles(args[2:])
		// Verbs #1348 deleted. Duplicate constant cases are a compile error, so
		// a future edit that tries to revive either one as a live verb fails the
		// build rather than silently shadowing a working route.
		case "attach":
			return errAttachRemoved
		case "acp":
			return errACPRemoved
		case "help", "-h", "--help":
			printHelp()
			return nil
		}
	}

	return runSupervisor(args[1:])
}

// pyryFlagBools are pyry-specific boolean flags. Recognised by their exact
// name (with or without a leading -- and with or without =value).
var pyryFlagBools = map[string]bool{
	"pyry-resume":  true,
	"pyry-verbose": true,
}

// pyryFlagValues are pyry-specific flags that take a value. The value can
// be glued (`-pyry-claude=/path`) or in the next arg (`-pyry-claude /path`).
var pyryFlagValues = map[string]bool{
	"pyry-claude":              true,
	"pyry-workdir":             true,
	"pyry-socket":              true,
	"pyry-name":                true,
	"pyry-idle-timeout":        true,
	"pyry-active-cap":          true,
	"pyry-conv-sweep-interval": true,
	"pyry-wrapup-deadline":     true,
	"pyry-relay":               true,
}

// splitArgs walks args left-to-right and partitions them into pyry's own
// flags and the rest (forwarded to claude). The split rules are:
//
//   - "--" is an explicit separator: everything before it is pyry's, every-
//     thing after is claude's.
//   - Args matching a known pyry-* flag pattern (with or without a value) are
//     pyry's. Boolean flags consume only themselves; value flags also consume
//     the next arg if no `=value` was glued on.
//   - The first arg that isn't a recognised pyry flag (and isn't "--") tips
//     into claude territory: it and everything after go to claude.
//
// This means pyry-* flags must come BEFORE any claude arguments — same
// convention as `sudo`, `time`, `xargs`. Use "--" if you need to mix.
func splitArgs(args []string) (pyryArgs, claudeArgs []string) {
	i := 0
	for i < len(args) {
		a := args[i]

		if a == "--" {
			claudeArgs = append(claudeArgs, args[i+1:]...)
			return
		}

		name, _, hasVal := parseFlagSyntax(a)

		if pyryFlagBools[name] {
			pyryArgs = append(pyryArgs, a)
			i++
			continue
		}
		if pyryFlagValues[name] {
			pyryArgs = append(pyryArgs, a)
			if !hasVal && i+1 < len(args) {
				pyryArgs = append(pyryArgs, args[i+1])
				i += 2
				continue
			}
			i++
			continue
		}

		// Not a pyry flag — everything from here goes to claude.
		claudeArgs = append(claudeArgs, args[i:]...)
		return
	}
	return
}

// clientPyryValueFlags lists the -pyry-* flags every control client accepts.
// Both take a value (string). Walk-based extraction needs this map so it can
// decide whether to consume the next token as the value (for the
// space-separated form: `-pyry-name elli`).
var clientPyryValueFlags = map[string]bool{
	"pyry-name":   true,
	"pyry-socket": true,
}

// splitClientFlags peels recognised -pyry-name / -pyry-socket tokens off the
// front of args and returns them as pyryArgs, leaving everything else in
// rest verbatim. Stops at the first non-pyry-* token: subsequent -pyry-*
// tokens are not extracted. Mirrors splitArgs's shape; differs only in the
// recognised flag set.
//
// Both `-pyry-name=elli` and `-pyry-name elli` forms are supported, as are
// the `-` and `--` dash prefixes (parseFlagSyntax normalises both).
//
// `--` is treated as a verb-side token: it and everything after go into
// rest. The verb's own FlagSet is the one that should interpret `--`.
func splitClientFlags(args []string) (pyryArgs, rest []string) {
	i := 0
	for i < len(args) {
		a := args[i]
		if a == "--" {
			rest = append(rest, args[i:]...)
			return
		}
		name, _, hasVal := parseFlagSyntax(a)
		if !clientPyryValueFlags[name] {
			rest = append(rest, args[i:]...)
			return
		}
		pyryArgs = append(pyryArgs, a)
		if !hasVal && i+1 < len(args) {
			pyryArgs = append(pyryArgs, args[i+1])
			i += 2
			continue
		}
		i++
	}
	return
}

// parseFlagSyntax extracts the flag name from a "-foo", "--foo", "-foo=bar",
// or "--foo=bar" arg. Returns (name, value, hasValue). For non-flag args
// (e.g. "summarize this") returns ("", "", false).
func parseFlagSyntax(a string) (name, value string, hasValue bool) {
	if !strings.HasPrefix(a, "-") {
		return "", "", false
	}
	a = strings.TrimLeft(a, "-")
	if eq := strings.IndexByte(a, '='); eq >= 0 {
		return a[:eq], a[eq+1:], true
	}
	return a, "", false
}

// confineWorkdirToHome resolves workdir to its canonical realpath and verifies
// it lies within the operator's home directory, returning the realpath on
// success. The supervised claude's workdir is auto-trusted in ~/.claude.json so
// claude never wedges on the workspace-trust modal; the $HOME bound keeps that
// machine-level auto-accept from extending to system paths or other users'
// spaces. An empty workdir resolves to the process working directory, matching
// how the supervisor launches claude when -pyry-workdir is unset.
//
// Both sides are canonicalised with EvalSymlinks (macOS /var→/private/var,
// case-folding) before the containment test, so a symlinked home does not yield
// a false reject and a sibling like /home/userfoo is not treated as inside
// /home/user (the #118/#221 two-path-sources gotcha). The rejection error names
// the offending path and the $HOME boundary only — never any file contents.
func confineWorkdirToHome(workdir string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	homeReal, err := filepath.EvalSymlinks(home)
	if err != nil {
		return "", fmt.Errorf("resolve home directory %q: %w", home, err)
	}
	absWork, err := filepath.Abs(workdir)
	if err != nil {
		return "", fmt.Errorf("resolve workdir %q: %w", workdir, err)
	}
	workReal, err := filepath.EvalSymlinks(absWork)
	if err != nil {
		return "", fmt.Errorf("resolve workdir %q: %w", workdir, err)
	}
	if !withinDir(homeReal, workReal) {
		return "", fmt.Errorf("workdir %q resolves outside the home directory %q: the supervised claude's workdir must be within $HOME", workReal, homeReal)
	}
	return workReal, nil
}

// withinDir reports whether path is dir itself or lies beneath it, using a
// boundary-aware comparison (filepath.Rel) so /home/userfoo is not treated as
// inside /home/user. Both arguments must be cleaned, canonical absolute paths.
func withinDir(dir, path string) bool {
	rel, err := filepath.Rel(dir, path)
	if err != nil {
		return false
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	return true
}

// expandTilde performs minimal, leading-only home expansion on a phone-supplied
// path: a bare "~" and a "~/"-prefix expand to the daemon's $HOME; everything
// else (an absolute path, a relative path, or a "~user" form) is returned
// verbatim. A "~user" is intentionally NOT resolved to another user's home — it
// passes through as a literal segment and fails the later confinement/existence
// check as a deterministic reject. No $VAR / arbitrary env expansion (out of
// scope, larger attack surface). The phone cannot know the daemon's absolute
// home, so it sends the default scratch Cwd as "~/.pyrycode/scratch" meaning
// "the daemon's home"; this anchors that at the real $HOME before path
// resolution, never under the process working directory (#696).
func expandTilde(p string) (string, error) {
	if p != "~" && !strings.HasPrefix(p, "~/") {
		return p, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	if p == "~" {
		return home, nil
	}
	return filepath.Join(home, p[2:]), nil
}

// confineWorkdirToHomeCreating is the create-aware variant of
// confineWorkdirToHome: same return contract (the canonical realpath on
// success, confined to $HOME), but it tolerates a not-yet-existing leaf/parents
// by canonicalising the longest existing ancestor and creating the rest only
// after the $HOME check passes (#696, the default-scratch case #685 previously
// rejected). When the whole path already exists this reduces exactly to
// confineWorkdirToHome (rest == "" → no MkdirAll, identical realpath).
//
// Order is security-load-bearing. A naive "containment-check the filepath.Abs
// (un-symlink-resolved) path, then MkdirAll" lets a symlinked ancestor (e.g.
// ~/link -> /tmp/evil) pass a textual check and create outside $HOME. So the
// candidate is built from the symlink-RESOLVED longest existing ancestor: the
// ancestor walk probes with os.Lstat (not os.Stat) so a symlink counts as
// existing and is resolved by EvalSymlinks, never stepped over; containment
// check #1 runs on that resolved candidate BEFORE any directory is created; and
// after creation the full path is re-EvalSymlinks'd and re-confined (check #2),
// catching a path that became escaping during creation and yielding the realpath
// to return. The residual confine→chdir TOCTOU window is the same one #685
// accepts; MkdirAll does not widen it.
func confineWorkdirToHomeCreating(workdir string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	homeReal, err := filepath.EvalSymlinks(home)
	if err != nil {
		return "", fmt.Errorf("resolve home directory %q: %w", home, err)
	}
	absWork, err := filepath.Abs(workdir)
	if err != nil {
		return "", fmt.Errorf("resolve workdir %q: %w", workdir, err)
	}

	// Split absWork into the longest leading ancestor that exists on disk
	// (probed with Lstat, so a symlink is "existing" and gets resolved, not
	// stepped over) and the not-yet-existing suffix `rest`.
	existing := absWork
	var rest string
	for {
		if _, err := os.Lstat(existing); err == nil {
			break
		}
		parent := filepath.Dir(existing)
		if parent == existing {
			break // reached the filesystem root; guard against looping
		}
		rest = filepath.Join(filepath.Base(existing), rest)
		existing = parent
	}

	existingReal, err := filepath.EvalSymlinks(existing)
	if err != nil {
		return "", fmt.Errorf("resolve workdir %q: %w", workdir, err)
	}
	candidate := existingReal
	if rest != "" {
		candidate = filepath.Join(existingReal, rest)
	}

	// Containment check #1 (pre-creation): the candidate is built from the
	// symlink-resolved existing ancestor, so a symlinked ancestor escaping
	// $HOME is rejected here, before any directory is created. The error names
	// the resolved path and the $HOME boundary only — never file contents.
	if !withinDir(homeReal, candidate) {
		return "", fmt.Errorf("workdir %q resolves outside the home directory %q: the supervised claude's workdir must be within $HOME", candidate, homeReal)
	}

	if rest != "" {
		if err := os.MkdirAll(candidate, 0o700); err != nil {
			return "", fmt.Errorf("create workdir %q: %w", candidate, err)
		}
	}

	// Containment check #2 (post-creation re-confine): the path now exists, so
	// EvalSymlinks fully canonicalises it; re-check the $HOME bound to catch a
	// path that became escaping during creation and to yield the realpath.
	final, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return "", fmt.Errorf("resolve workdir %q: %w", workdir, err)
	}
	if !withinDir(homeReal, final) {
		return "", fmt.Errorf("workdir %q resolves outside the home directory %q: the supervised claude's workdir must be within $HOME", final, homeReal)
	}
	return final, nil
}

// resolveSpawnDir validates a phone-requested per-conversation spawn workdir for
// create_conversation, mirroring the daemon bootstrap's confine→trust sequence
// (runSupervisor below). It returns the directory to hand sessions.Pool.CreateIn:
//
//	requested == "" → ("", nil): the pool spawns in the shared trusted template
//	                  workdir (today's behaviour for a conversation with no Cwd);
//	                  trustMark is NOT called.
//	requested set   → expandTilde (leading "~"/"~/" → $HOME) then
//	                  confineWorkdirToHomeCreating (canonicalise, confine to
//	                  $HOME, create the dir if missing) then trustMark the
//	                  realpath; returns trustMark's realpath so claude's cwd and
//	                  the trust-marked path are byte-identical (AC#4).
//
// Order is load-bearing: confine gates the $HOME bound BEFORE trust — trustMark
// has no $HOME bound, so trust-marking first could auto-trust a path outside
// $HOME. The phone-default scratch Cwd ("~/.pyrycode/scratch") must resolve under
// $HOME and be created before spawn (#696): expandTilde anchors a leading "~" at
// the daemon's $HOME, and confineWorkdirToHomeCreating creates the dir only after
// the $HOME check passes (a symlinked-ancestor escape is rejected and never
// created). A requested dir that escapes $HOME (including via a symlink resolving
// outside $HOME) or is unresolvable fails confinement and is returned wrapping
// handlers.ErrSpawnDirRejected — a deterministic, non-retryable rejection (the
// confine detail is wrapped via %v for logs; errors.Is matches the sentinel). A
// trustMark failure (a transient ~/.claude.json write error) is returned plain so
// the handler classifies it retryable.
func resolveSpawnDir(requested string) (string, error) {
	if requested == "" {
		return "", nil
	}
	expanded, err := expandTilde(requested)
	if err != nil {
		return "", fmt.Errorf("%w: %v", handlers.ErrSpawnDirRejected, err)
	}
	realpath, err := confineWorkdirToHomeCreating(expanded)
	if err != nil {
		return "", fmt.Errorf("%w: %v", handlers.ErrSpawnDirRejected, err)
	}
	trusted, err := trustMark(realpath)
	if err != nil {
		return "", fmt.Errorf("mark conversation workdir trusted in ~/.claude.json: %w", err)
	}
	return trusted, nil
}

// selectsStreamRunner reports whether this config selects the stream-json
// runner, which since #1348 is every valid value including the empty one. It
// exists so the composition root can prepare the approval-tool config BEFORE
// calling selectInteractiveRunner, which needs that path as an argument. An
// invalid value answers true here and then fails loudly one line later, which
// is harmless: the only effect is a temp file written and removed at shutdown.
func selectsStreamRunner(cfg config.Config) bool {
	return cfg.InteractiveRunner != "pty"
}

// selectInteractiveRunner maps cfg.InteractiveRunner to the runner factory and
// its turn-event sink at the composition root (#1081, AC4's loud-error path):
//
//   - "" / "stream-json" → (factory, sink, nil): ONE newStreamTurnSink backs both
//     the factory (which binds sinkFor per runner) and the single relay-leg drain,
//     so the two ends of the wire share that one instance (#1098 late-bound
//     singleton). The caller threads the returned sink into relayWiring.streamSink.
//   - "pty" → (nil, nil, error) naming the removal. It gets its own arm rather
//     than falling into the default, because an operator with that key in a config
//     file has something to edit and deserves to be told the path was deleted
//     rather than that the value is unrecognised.
//   - any other value → (nil, nil, error) naming the offending value and the
//     accepted set. No silent fallback (AC4).
//
// The empty default moved from the terminal runner to the stream runner here.
// It used to select the terminal path, which meant an absent or reset config
// file came up on the path four live specs failed against. Nothing now selects a
// terminal runner, because there is no longer one to select.
//
// Validation lives here, not in config.Load, because the accepted set is defined
// by the factory mapping — which the leaf config package cannot import
// (streamsup). config.Load stays parse-only, matching DebugCapture.
func selectInteractiveRunner(cfg config.Config, logger *slog.Logger, mcpServersPath string, vocab *modelVocabularyStore, approval streamApprovalConfig) (sessions.RunnerFactory, *streamTurnSink, error) {
	switch cfg.InteractiveRunner {
	case "", "stream-json":
		sink := newStreamTurnSink(0, logger)
		return newStreamRunnerFactory(sink, mcpServersPath, vocab, approval), sink, nil
	case "pty":
		return nil, nil, fmt.Errorf(`interactive_runner "pty" was removed in #1348: the terminal-driving interactive runner no longer exists. Remove the key or set it to "stream-json"`)
	default:
		return nil, nil, fmt.Errorf("interactive_runner %q not recognized (accepted: \"\", \"stream-json\")", cfg.InteractiveRunner)
	}
}

// runSupervisor starts the supervisor and the control server together, blocks
// until the context is cancelled by SIGINT/SIGTERM, then drains both.
func runSupervisor(args []string) error {
	pyryArgs, claudeArgs := splitArgs(args)

	fs := flag.NewFlagSet("pyry", flag.ContinueOnError)
	claudeBin := fs.String("pyry-claude", "claude", "path to the claude binary")
	workdir := fs.String("pyry-workdir", "", "working directory for claude (default: current)")
	resume := fs.Bool("pyry-resume", true, "resume the most recent session on restart")
	verbose := fs.Bool("pyry-verbose", false, "verbose pyry logging")
	name := fs.String("pyry-name", defaultName(), "instance name (socket: ~/.pyry/<name>.sock)")
	socketFlag := fs.String("pyry-socket", "", "explicit socket path (overrides -pyry-name)")
	idleTimeout := fs.Duration("pyry-idle-timeout", 0, "evict idle claudes after this duration (0 disables; pass e.g. 15m to enable)")
	activeCap := fs.Int("pyry-active-cap", 0, "max concurrently active claudes (0 = uncapped)")
	convSweepInterval := fs.Duration("pyry-conv-sweep-interval", 0, "override conversations sweep tick interval (testing; 0 = production default)")
	// SHORTEN-ONLY, and conversationReset.bound is where that is enforced (#2486): a
	// value at or above wrapUpDeadline means the production ninety seconds, exactly as
	// zero does. The live suite needs a bound it can arm from a spawned daemon's argv;
	// wrapUpDeadline's own doc refuses an operator a LONGER one, and both hold.
	wrapUpDeadlineFlag := fs.Duration("pyry-wrapup-deadline", 0, "shorten the conversation reset's wrap-up bound (testing; 0 or >= the 90s default = production default)")
	relayFlag := fs.String("pyry-relay", "", "relay URL override (default: $PYRY_RELAY_URL or ~/.pyry/config.json)")
	if err := fs.Parse(pyryArgs); err != nil {
		return err
	}
	socketPath := resolveSocketPath(*socketFlag, *name)
	registryPath := resolveRegistryPath(*name)
	convRegistryPath := resolveConversationsRegistryPath(*name)
	// The daemon-wide model vocabulary (#2450), read ONCE here and never on a
	// request path. It is built and loaded before the runner factory and the pool
	// because both consume it: the factory's persist decorator writes into it, and
	// the three read seams below answer from it when no hold holds anything —
	// which, since #2085 removed the spawn at daemon start, is the whole of a
	// restarted daemon's life until a turn runs in the bootstrap-bound
	// conversation. Load never fails loudly: an absent, unreadable or undecodable
	// file leaves the store empty and the daemon starts exactly as it does today.
	// Close joins the writer goroutine at shutdown.
	modelVocabulary := newModelVocabularyStore(resolveModelVocabularyPath(*name))
	modelVocabulary.Load()
	defer modelVocabulary.Close()
	claudeSessionsDir := resolveClaudeSessionsDir(*workdir)
	defaultCwd := resolveDefaultCwd(*workdir)

	// Pre-mark the supervised claude's workdir trusted in ~/.claude.json so it
	// never wedges on claude's workspace-trust modal — the #421 clean-exit
	// restart loop the bridge can't surface to the phone. Confine the auto-
	// trust to $HOME: running the daemon here already implies the operator
	// trusts this folder, but the auto-accept must never reach system paths or
	// other users' spaces, so a workdir resolving outside $HOME is rejected as a
	// loud startup failure rather than a silent loop. Claude is launched in the
	// returned realpath (threaded into Bootstrap.WorkDir below), so the marked
	// path and the child's cwd stay byte-identical — a symlinked or wrong-case
	// workdir cannot re-render the modal (#470/#473).
	workdirReal, err := confineWorkdirToHome(*workdir)
	if err != nil {
		return err
	}
	trustedWorkdir, err := trustMark(workdirReal)
	if err != nil {
		return fmt.Errorf("mark workdir trusted in ~/.claude.json: %w", err)
	}

	convReg, err := conversations.Load(convRegistryPath)
	if err != nil {
		return fmt.Errorf("loading conversations: %w", err)
	}

	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	// Tee the supervisor's logger to a ring buffer so `pyry logs` can replay
	// recent lifecycle events from another shell. 200 entries is enough for
	// several minutes of normal activity at debug level.
	logRing := control.NewRingBuffer(200)
	logger := slog.New(control.SlogTee(
		slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}),
		logRing,
	))

	// Two-layer shutdown context so a shutdown's ORIGIN survives to the exit
	// classification below. The signal layer handles SIGTERM/SIGINT (operator
	// stop → nil cause → exit 0). The cause layer lets a self-initiated fatal
	// path (a persistent 4409 server-id conflict) cancel WITH an error, which
	// fatalCause turns into a non-zero exit so launchd restarts the daemon
	// (the 2026-07-16 outage's second half). `pyry stop` cancels with a nil
	// cause via the control server, so it stays down like a signal.
	sigCtx, sigCancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer sigCancel()
	ctx, cancelCause := context.WithCancelCause(sigCtx)
	defer cancelCause(nil)

	cfg, err := config.Load(resolveConfigPath())
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}
	// Debug-capture gate (#802): resolve the recordings dir ONLY when the
	// persisted flag is ON, so the bootstrap supervisor sees a non-empty
	// RecordDir strictly when the operator opted in. OFF (or unset) leaves it
	// "" → the interactive spawn is byte-identical to today.
	var recordDir string
	if cfg.DebugCapture {
		recordDir = resolveRecordingsDir()
	}
	// Approval-tool wiring (#1168): on the stream-json interactive path, write the
	// per-daemon --mcp-config file that points claude's approval-prompt tool at
	// THIS daemon's control socket, so a non-yolo permission-bearing turn surfaces
	// an answerable modal (the #1154 gate). Its content — (pyry binary, socketPath)
	// — is daemon-global and identical across every session and respawn, so it is
	// written ONCE here and removed at shutdown (the file lives the daemon's whole
	// lifetime; per-spawn removal is out of scope). Fail-closed: a write error
	// aborts startup rather than spawning a non-yolo session with an empty
	// --mcp-config. Written unconditionally in stream mode (not gated on
	// bootstrap-yolo): a yolo daemon can still mint non-yolo per-conversation
	// sessions, so the config must exist; it is harmless and unreferenced when
	// every spawn is yolo. The "" / "pty" path never builds the factory, so
	// mcpServersPath stays "".
	var mcpServersPath string
	if selectsStreamRunner(cfg) {
		mcpServersPath, err = writeMCPServersConfig(resolveExecutable(), socketPath)
		if err != nil {
			return fmt.Errorf("write mcp-approve config: %w", err)
		}
		defer func() { _ = os.Remove(mcpServersPath) }()
	}
	// Interactive-runner selection (#1081): pick the runner factory + its shared
	// turn-event sink from config BEFORE the pool is built, so an invalid value
	// fails fast (AC4, no silent PTY fallback). Both are nil on the "" / "pty"
	// rollback path, leaving the sessions.Config and relayWiring literals below
	// byte-identical to today. On "stream-json" the same sink instance is threaded
	// two ways: RunnerFactory (below) and relayWiring.streamSink (the drain); the
	// factory also carries mcpServersPath to inject the approval-tool flags (#1168).
	// One registry and one window serve both Claude-facing transports. The stdio
	// handler needs them when the runner factory is built; the MCP control server
	// receives the same values after the relay leg has been composed.
	approvals := permbridge.New()
	approvalWindow := approvalTimeout()
	approvalSurfaces := &approvalSurfaceReport{}
	runnerFactory, streamSink, err := selectInteractiveRunner(cfg, logger, mcpServersPath, modelVocabulary, streamApprovalConfig{
		stdio:    cfg.StdioPermissionPrompt,
		registry: approvals,
		timeout:  approvalWindow,
		surface:  approvalSurfaces,
	})
	if err != nil {
		return fmt.Errorf("interactive runner: %w", err)
	}
	// The #1201 per-conversation turn-busy tracker, constructed HERE — at the
	// composition root — because it now has two consumers in different subtrees: the
	// inbound-delivery seam below (#1199, via msgqueue.Config.Deliver) and the relay
	// leg's drain + teardown clear (relayWiring.busy). Its only input is convReg
	// (loaded above), so it can be minted between the runner selection and
	// msgqueue.New without hoisting or late-binding anything: the runner factory's
	// parameter list is untouched.
	//
	// The gate is streamSink != nil, NOT cfg.InteractiveRunner == "stream-json":
	// that is selectInteractiveRunner's own post-validation answer, so an
	// unrecognised config value has already failed fast one line above, and this
	// stays byte-identical to the discriminant relayWiring.streamSink documents. In
	// PTY mode the tracker is nil and every method reached from either consumer is a
	// nil-receiver no-op.
	//
	// withExitEpoch is what ARMS the stale-exit guard (#1483): it binds the very
	// fan-in whose exit lane the tracker's clear is ordered against, which is in
	// scope here and nowhere else the tracker is built. Dropping it compiles, passes
	// every test and silently restores the pre-#1483 behaviour — a stale exit
	// clearing a mark opened on the respawned child — so it is not an optional knob
	// on this call, only on the ~36 test constructions that want the incumbent
	// semantics.
	var turnBusy *turnBusyTracker
	if streamSink != nil {
		turnBusy = newTurnBusyTracker(
			func(sid string) (string, bool) { return conversationForSession(convReg, sid) }, logger,
			withExitEpoch(streamSink.exitEpoch),
			withLifecycleClose(streamSink.requestLifecycleClose))
	}
	pool, err := sessions.New(sessions.Config{
		Logger:                    logger,
		RegistryPath:              registryPath,
		ClaudeSessionsDir:         claudeSessionsDir,
		IdleTimeout:               *idleTimeout,
		ActiveCap:                 *activeCap,
		ConversationsRegistry:     convReg,
		ConversationsRegistryPath: convRegistryPath,
		SweepInterval:             *convSweepInterval,
		RunnerFactory:             runnerFactory,
		Bootstrap: sessions.SessionConfig{
			ClaudeBin:  *claudeBin,
			WorkDir:    trustedWorkdir,
			ResumeLast: *resume,
			ClaudeArgs: claudeArgs,
			RecordDir:  recordDir,
		},
	})
	if err != nil {
		return fmt.Errorf("pool init: %w", err)
	}

	relayURL := resolveRelayURL(*relayFlag, os.Getenv("PYRY_RELAY_URL"), cfg)
	// PYRY_ALLOW_INSECURE_RELAY is a dev/test-only flag (set only by the e2e harness,
	// never by production code) that lets the relay client accept an insecure ws://
	// URL; production leaves it unset and requires wss://.
	allowInsecure := os.Getenv("PYRY_ALLOW_INSECURE_RELAY") == "1"
	// One activeConversation holder, shared two ways: the sessionRouter writes it
	// on each successful route, and startRelay threads it (read-side) to the
	// structured turn stream as its cursor (#687) and its follow-active switch
	// signal (#679).
	active := &activeConversation{}
	router := sessionRouter{pool: pool, convReg: convReg, active: active}

	// The inbound message queue (#704/#721): send_message enqueues here instead
	// of delivering synchronously, and one serial drain goroutine per conversation
	// delivers the backlog through the reliable WriteUserTurn path, paced by claude
	// reaching idle. The delivery seam re-resolves the bound session per attempt
	// via router.resolve (the stamp-free core — the drain must NOT move the
	// active-conversation cursor). It runs under the daemon ctx; on shutdown Run
	// stops spawning drains, joins the in-flight ones, and returns. The in-memory
	// backlog survives a claude child respawn but not a daemon-process restart
	// (resync covers it) — the same loss boundary as the event ring.
	// queueChanges is the hand-off channel from msgqueue's OnChange seam to the
	// queue_state producer (#722). It is created BEFORE msgqueue.New so OnChange
	// can send to it, and shared with newQueueStateEmitterV2 below — this breaks
	// the chicken-and-egg (OnChange is a Config field set at New, before startRelay
	// builds the broadcaster) without any late-bound field.
	queueChanges := make(chan string, queueStateQueueSize)
	// giveUps is the parallel hand-off channel from msgqueue's OnGiveUp seam
	// (#1000) to the session_error producer (#1008), created BEFORE msgqueue.New
	// for the same chicken-and-egg reason as queueChanges and shared with
	// newSessionErrorEmitterV2 below. Setting OnGiveUp here flips #1000's seam from
	// nil (disabled) to live: a persistent-delivery give-up now surfaces as a
	// typed, client-visible session_error frame instead of a silently dropped head.
	giveUps := make(chan giveUpNotice, sessionErrorQueueSize)
	// blocked is the shared session_error notify closure (a non-blocking,
	// drop-on-full send into giveUps): the msgqueue give-up path uses it as
	// OnGiveUp, and the modal resolver uses it (via relayWiring.blockedNotify) to
	// surface a folder-not-trusted session_error on a trust deny/timeout (#1014).
	// One closure, two senders into the same #1008 frame path.
	blocked := sessionErrorNotify(giveUps, logger)
	// approvalParked is the third value in this block built BEFORE msgqueue.New for
	// the same chicken-and-egg reason as queueChanges and giveUps: it carries #1919's
	// ApprovalParked report to the Pending gate below, and the bridge that answers
	// the report is constructed inside startRelayV2, well after this queue. Unlike
	// the two channels it cannot be a channel — the gate needs an answer, not a
	// notification — so it is the one late-bound field here, threaded to its single
	// setter through relayWiring.approvalParked. Left unset (PTY mode, or before the
	// relay leg wires it) it reports negative for every conversation, which is the
	// pre-#1911 behaviour exactly.
	approvalParked := &approvalParkedReport{}
	// The daemon's ONE durable conversation log (#2112, first written by #2114).
	// Exactly one Store may exist per instance directory: it caches each
	// conversation's next id after recovering it from disk once, so two stores
	// mint duplicate ids for the same conversation, and each undoes a failed write
	// by truncating to a size it stat'd itself — which can drop an entry the other
	// had just appended. So this is the single mint, shared by every producer.
	//
	// It is the fourth value in this block built BEFORE msgqueue.New, for the same
	// chicken-and-egg reason as queueChanges, giveUps and approvalParked: #2115's
	// producer hangs off the queue's OnDelivered seam, a Config field set at New,
	// and the store used to be minted a frame further down where that literal
	// could not see it. Moving it costs nothing and touches no filesystem — the
	// instance directory is resolved lazily, per Append. It is read again below by
	// relayWiring.hist, which the #2114 producers reach it through.
	conversationHistory := history.New(resolveInstanceDirPath(*name))
	// #2499's carry-forward, the fifth value in this block built BEFORE
	// msgqueue.New: it wraps the delivery seam AND hangs off OnDelivered, so both
	// of its queue-facing halves are set in the literal below. Its third half is
	// wired into the channel poster further down, which is the whole point — ONE
	// value, so the post that records pending text and the delivery that carries it
	// cannot come from two stores holding different records of one conversation.
	//
	// It takes the SAME registry and registry path every other conversation-keyed
	// seam resolves against, both already in scope here.
	postCarry := &channelCarry{reg: convReg, path: convRegistryPath, logger: logger}
	queue, err := msgqueue.New(msgqueue.Config{
		// Carry OUTERMOST, so the pending posted text is composed onto the payload
		// once, at the boundary with the queue, and markApprovalHolds stays adjacent to
		// the seam that produces the hold error it marks. The composed value goes no
		// further than newInboundDeliver's WriteUserTurn — see channelCarry on why the
		// "clients see no change" property is structural here rather than a filter.
		Deliver:  postCarry.carryPending(approvalParked.markApprovalHolds(newInboundDeliver(router.resolve, turnBusy, streamTurnHoldTimeout))),
		OnChange: queueStateNotify(queueChanges, logger),
		OnGiveUp: blocked,
		// #2115: the operator's own message reaches the durable log HERE and
		// nowhere else. It cannot be written from the Deliver seam above, which
		// receives the composed payload that may name an on-host path; this one
		// carries the client-readable text. Fires only on a confirmed write, so a
		// dequeued or abandoned message is never recorded as said.
		//
		// #2499 shares the seam through deliveredFuncs. History FIRST — its doc block
		// states that its record is written as close to the commit as possible — then
		// the carry's clear, which drops exactly the pending text this delivery
		// carried. The clear cannot be done from the Deliver seam either: that seam
		// runs per ATTEMPT, and a head cleared on an attempt that then fails would
		// lose the text the retry was meant to carry.
		OnDelivered: deliveredFuncs(
			newOperatorMessageHistory(conversationHistory, logger),
			postCarry.clearDelivered,
		),
		// Pending exempts a head held behind an approval parked on a PERSON from the
		// give-up bound (#1911). #1014 wired this seam to claude's startup trust modal;
		// that modal only ever appeared on the terminal surface, which #1348 removed,
		// so supervisor.ErrTrustModalPending has had no producer since and this is the
		// exemption's second and only current one. The delivery wrap above answers the
		// conversation-scoped half — Pending is func(error) bool and never sees a
		// conversation — and this predicate classifies its mark.
		//
		// THE GATE IS WHAT KEEPS THE BOUND SATISFIABLE, and is why the exemption could
		// not simply be granted to every hold (streamTurnHoldTimeout's doc records the
		// refusal). Held to a person actually being asked, a turn making no progress
		// with nothing parked still reaches give-up on today's schedule, and the
		// exemption ends the moment the approval does — retire deletes the correlation
		// on every terminal path an approval has.
		Pending: approvalHoldPending,
		Logger:  logger,
	})
	if err != nil {
		return fmt.Errorf("msgqueue init: %w", err)
	}
	qDone := make(chan error, 1)
	go func() { qDone <- queue.Run(ctx) }()

	// The queue_state producer (#722): on each backlog change it snapshots the
	// changed conversation and fans a queue_state envelope to interactive phones.
	// Built here (so queue.Snapshot is bound) but its Run goroutine starts inside
	// startRelayV2, where the broadcaster exists.
	qse := newQueueStateEmitterV2(queueChanges, queue.Snapshot, logger)

	// The session_error producer (#1008): on each msgqueue give-up it fans a typed
	// session_error envelope (terminal CodeSessionBlocked) to interactive phones,
	// so a wedged session surfaces instead of leaving a queued turn that silently
	// never runs. Built here (channel shared with the OnGiveUp seam) but its Run
	// goroutine starts inside startRelayV2, where the broadcaster exists — same
	// shape as qse. It holds no queue reference, so it cannot reach queued text.
	see := newSessionErrorEmitterV2(giveUps, logger)

	// The resetting producer (#2478): as a conversation reset runs, it fans the
	// wrap-up and restart phases and the falling edge that closes them to interactive
	// phones, so the pause an operator is watching has a name and says whether a
	// handoff note was made. Built here because the activeSessionStarter literal below
	// is its caller; it holds NO broadcaster yet, and startRelayV2 attaches the relay
	// leg's one. Unlike qse and see it starts no Run goroutine — its caller is the
	// reset tail, which already runs off the dispatch goroutine, so it emits
	// synchronously and cannot drop or reorder an edge.
	resetting := newResettingEmitterV2(ctx, logger)

	// The debug-bundle producer (#813): a paired `request_debug_bundle` frame
	// assembles the daemon-global bundle — the recent log ring plus the newest
	// terminal recording — as one in-memory archive and streams it back over the
	// encrypted v2 channel. The recordings dir is resolved independent of the
	// DebugCapture flag: recordings written while capture was on persist and stay
	// readable after it is turned off, and Assemble marks the recording absent
	// when the dir is empty. Assemble makes zero log calls and the archive bytes
	// travel only over the sealed push path — no bundle content reaches any log.
	bundleRecordingsDir := resolveRecordingsDir()
	debugBundler := func() ([]byte, error) {
		archive, _, err := debugbundle.Assemble(bundleRecordingsDir, logRing.Snapshot())
		return archive, err
	}
	// Test-only override: an env-gated fake bundler, inert unless insecure-relay
	// is set (see fakeDebugBundler). Lets an out-of-process e2e round-trip a known
	// archive and inject a path-quoting assemble error; production leaves this
	// untouched and streams the real Assemble output.
	if fake, ok := fakeDebugBundler(); ok {
		debugBundler = fake
	}

	// The screen-snapshot settings reader (#848): reports the bootstrap session's
	// persisted model / effort / YOLO so the screen_snapshot reply the phone
	// already receives can render the current model / reasoning-effort /
	// permissions posture before offering to change it (desktop#156). Built here,
	// not in relay.go, so the internal/sessions dependency stays at the
	// composition root — the closure decodes SessionSettings into three
	// primitives, so the value crossing into relay.go is a bare
	// func() (string, string, bool) (same discipline as debugBundler and
	// settingsUpdaterAdapter). No bootstrap ⇒ defaults, which collapse to the same
	// wire output as the all-defaults case (empty model/effort, yolo:false).
	snapshotSettings := func() (model, effort string, yolo bool) {
		s, ok := pool.DefaultSettings()
		if !ok {
			return "", "", false
		}
		return s.Model, s.Effort, s.YOLO
	}

	relayCleanup, approvalSurface, announceAttachment, announceConversation, announcePost, pairingProvider, err := startRelay(ctx, logger, relayWiring{
		instanceName:  *name,
		relayURL:      relayURL,
		version:       Version,
		allowInsecure: allowInsecure,
		shutdown:      cancelCause,
		convReg:       convReg,
		creator:       sessionMinter{pool},
		router:        router,
		queue:         queue,
		active:        active,
		activeInterrupter: activeInterrupter{
			currentConv: active.CurrentConversation,
			resolveRunner: func(convID string) (sessions.Runner, bool) {
				runner, _, ok := resolveBoundRunner(convReg, pool, convID)
				return runner, ok
			},
			log: logger,
		},
		activeSessionStarter: activeSessionStarter{
			currentConv: active.CurrentConversation,
			resolveBound: func(convID string) (sessions.Runner, sessions.SessionID, string, bool) {
				sess, id, cwd, ok := resolveBoundSession(convReg, pool, convID)
				if !ok {
					return nil, "", "", false
				}
				return sess.Runner(), id, cwd, true
			},
			rotate: pool.RotateForNewSession,
			// #1475: the same validator the mint and revive paths use, re-run at
			// rotation time on the recorded workspace rather than trusting it.
			spawnDirFor: resolveSpawnDir,
			// #2521: the three seams that let a DORMANT conversation be reset without
			// a preliminary message. everRan is the durable discriminator that keeps
			// the #2085 never-used refusal intact while releasing the three states it
			// was over-reaching into; resolveDormant reads the persisted binding the
			// pool has not materialised; reviveBound is sessionRouter.revive's body —
			// the same re-validated spawn dir and the same non-spawning Pool.Revive —
			// so the reset path recovers a dropped session exactly as the message
			// route already does.
			everRan: pool.EverActivated,
			resolveDormant: func(convID string) (sessions.SessionID, string, bool) {
				return resolveDormantSession(convReg, convID)
			},
			reviveBound: func(convID string, oldID sessions.SessionID, recordedCwd string) (sessions.Runner, error) {
				// The label is the conversation id, matching what create_conversation
				// originally minted the session with — sessionRouter.revive's posture.
				spawnDir, err := resolveSpawnDir(recordedCwd)
				if err != nil {
					return nil, err
				}
				sess, err := pool.Revive(oldID, convID, spawnDir)
				if err != nil {
					return nil, err
				}
				return sess.Runner(), nil
			},
			// #2477: the wrap-up turn that writes the outgoing session's handoff note
			// before the rotation. Built over the SAME registry, pool, tracker and
			// queue the rest of this literal's seams are built over, and under the
			// daemon ctx — never a frame's — because the reset outlives the dispatch
			// that started it. nil on a daemon with no registry or pool, which leaves
			// the rotation exactly as it was.
			reset: newConversationReset(ctx, convReg, pool, turnBusy, queue, *wrapUpDeadlineFlag, logger),
			// #2478: the same emitter the relay leg attaches its broadcaster to, so the
			// tail that orders the three edges and the producer that puts them on the
			// wire are one object rather than two that could disagree.
			resetting: resetting,
			log:       logger,
		},
		claudeSessionsDir: claudeSessionsDir,
		bootstrapIDFn:     func() string { return string(pool.BootstrapID()) },
		defaultCwd:        defaultCwd,
		transitions:       pool,
		// #2148: the relay leg hands back its open-conn enumerator, and this closure
		// — the only place that names both packages — maps it onto the pool's
		// resolver. The two ActiveConn fields cross as untrusted text and are judged
		// nowhere on this path; sessions.admitClient is the single door.
		setClientIdentity: func(enum func(context.Context) []relay.ActiveConn) {
			pool.SetClientIdentityResolver(func(ctx context.Context) []sessions.ClientIdentity {
				conns := enum(ctx)
				out := make([]sessions.ClientIdentity, 0, len(conns))
				for _, c := range conns {
					out = append(out, sessions.ClientIdentity{Name: c.DeviceName, Version: c.ClientVersion})
				}
				return out
			})
		},
		qse:              qse,
		sessionErr:       see,
		resetting:        resetting,
		blockedNotify:    blocked,
		debugBundler:     debugBundler,
		settings:         settingsUpdaterAdapter{pool, modelVocabulary},
		snapshotSettings: snapshotSettings,
		runSettings: func(convID string) (boundRunSettings, bool) {
			return resolveBoundRunSettings(convReg, runSettingsPool{Pool: pool}, convID)
		},
		// The registry half and the live-session half of one conversation's
		// system-prompt picture (#2152), resolved together over the same registry and
		// pool. An inline closure like runSettings above rather than a named adapter
		// like modelListFor below, and for the reason that pair differs: this value is
		// cmd/pyry-typed, so no internal/protocol annotation is needed here and none
		// would compile — this file does not import that package.
		promptState: func(convID string) (conversationPromptState, bool) {
			return resolveConversationPrompt(convReg, pool, convID)
		},
		modelWindows: sessionModelWindows(pool),
		// The folder half of the same reading (#2423), built beside the windows half
		// over the same pool and gated on the daemon's own sessions directory: with
		// none, the conversation-keyed usage seam stays unwired exactly as before.
		sessionTranscriptDir: sessionTranscriptDir(pool, claudeSessionsDir),
		// The conversation-keyed half of the model-list pair (#2125), built beside its
		// enumerating twin below over the same registry and pool.
		modelListFor:       modelListFor(convReg, pool, modelVocabulary),
		mcpStatusFor:       mcpStatusFor(convReg, pool),
		effectiveEffortFor: effectiveEffortFor(convReg, pool),
		// The resolution half of the on-demand context-usage read (#2431), built
		// beside its MCP twin over the same registry and pool. The collapsing and
		// mid-turn-deferral half is composed in startRelayV2, which holds the turn
		// tracker and the daemon context.
		contextUsageResolve:           contextUsageResolve(convReg, pool),
		mcpActuatorFor:                boundMCPChildActuator(convReg, pool),
		retainedModelLists:            retainedModelLists(convReg, pool, modelVocabulary),
		retainedSlashCommandLists:     retainedSlashCommandLists(convReg, pool),
		retainedBackgroundTaskRosters: retainedBackgroundTaskRosters(convReg, pool),
		approvals:                     approvals,
		streamSink:                    streamSink,
		busy:                          turnBusy,
		hist:                          conversationHistory,
		approvalParked:                approvalParked,
	})
	if err != nil {
		return fmt.Errorf("relay start: %w", err)
	}
	approvalSurfaces.set(approvalSurface)
	// Cancel-then-join: relayCleanup joins producer drains whose Run loops
	// return only on ctx.Done, so the daemon ctx must already be cancelled when
	// it runs. Defers are LIFO, so the `defer cancelCause(nil)` registered at the
	// top of runSupervisor runs AFTER this one — too late to unblock them. Cancel
	// here instead, so an error return between startRelay and the end of
	// runSupervisor (today: ctrl.Listen's ErrInstanceRunning) takes the same
	// ordering the normal shutdown path already takes, rather than wedging with
	// the unblocking cancel queued behind the block (#1492). Cause contexts are
	// first-cause-wins, so a 4409 already recorded by startRelay's conn.Wait
	// classifier survives this nil and fatalCause still reports it.
	defer func() {
		cancelCause(nil)
		relayCleanup()
	}()

	// Pool satisfies control.Sessioner directly — Pool.Create returns
	// sessions.SessionID and Pool.Remove returns plain error, matching
	// Sessioner.Create / Sessioner.Remove (via embedded Remover) signatures
	// with no adapter (contrast with poolResolver for the read-side Lookup).
	// `pyry stop` is an OPERATOR-initiated shutdown: cancel with a nil cause
	// so the daemon exits 0 and launchd leaves it down (unlike the relay's
	// self-initiated fatal path, which passes an error cause).
	ctrl := control.NewServer(socketPath, poolResolver{pool}, logRing, func() { cancelCause(nil) }, logger, pool)
	// Install the shared approval registry between NewServer and Serve so the
	// mcp.approve verb reaches the same instance #1080's modal wiring will
	// resolve against (AC-4). Nil until here — v1/foreground never calls this.
	ctrl.SetApprovalRegistry(approvals, approvalWindow)
	// Install the stream-approval surfacer (#1080) so a parked mcp.approve raises
	// the SAME permission modal_shown clients already answer and a client's
	// modal_answer resolves claude's blocked tool. nil when the relay leg is
	// disabled (no URL) — SetApprovalSurfacer(nil) leaves mcp.approve modal-less,
	// the pre-#1080 behaviour.
	ctrl.SetApprovalSurfacer(approvalSurface)
	// The relay leg constructs this provider only after it has loaded the exact
	// identity, static key, URL, and registry used by the running daemon. A nil
	// provider when relay is disabled preserves pairing.mint's fixed
	// not-configured response.
	ctrl.SetPairingProvider(pairingProvider)
	// Install the attachment.file destination (#2164) in the same
	// between-NewServer-and-Serve window, over the SAME registry and pool every
	// other conversation-keyed seam above resolves against. Wired here rather
	// than left nil because this slice OWNS this dependency: #1104's precedent is
	// that a verb installs the dependency it owns (SetApprovalRegistry) and
	// leaves its sibling's nil (the surfacer, until #1080). What ships inert is
	// the verb's CALLER — the MCP tool claude invokes is #2165 — not the verb.
	//
	// The liveness adapter is deliberately one line and deliberately discards
	// the *sessions.Session: whether the named session is live is the entire
	// question, and handing the attacher a session it has no use for would widen
	// the seam for nothing.
	//
	// announceAttachment (#2166) is the relay leg's attachment-offer fan-out, so
	// a stored file reaches paired clients as an attachment_offered rather than
	// as nothing at all. It arrives nil from startRelay's no-URL early return —
	// the SetApprovalSurfacer(nil) shape one line up — and the attacher stores
	// and mints exactly as before when it is.
	ctrl.SetFileAttacher(fileAttacher(convReg, func(id sessions.SessionID) error {
		_, err := pool.Lookup(id)
		return err
	}, resolveInstanceDirPath(*name), announceAttachment, logger))
	// Install the channel.new creator (#2155) in the same window, over the SAME
	// registry, path and pool every other conversation-keyed seam above resolves
	// against. The mint closure is the narrowing sessionMinter.Create performs
	// for the wire path, minus the ctx it discards and minus resolveSpawnDir —
	// the creator calls that itself, because unlike the wire handler it needs
	// the RESOLVED path back (to record as Cwd and to derive the name from) and
	// Mint answers only with a session id. Resolving once here rather than
	// widening handlers.SessionCreator also avoids a second trustMark write for
	// a path already marked.
	//
	// announceConversation (#2156) is the relay leg's conversation fan-out, so a
	// channel created from the host's shell reaches every open client without a
	// reconnect rather than waiting for its next list. It arrives nil from
	// startRelay's no-URL early return — announceAttachment's shape one wiring
	// up — and the creator creates exactly as before when it is.
	//
	// Hoisted to a local because #2497's poster needs the SAME creator: a post
	// whose label matches nothing creates the channel through it rather than
	// minting a second create path, so the confinement order, the eager persist,
	// the bound session, the announcement and the channel_new.created log line
	// have one home for both entry points.
	createChannel := channelCreator(convReg, func(label, spawnDir string) (string, error) {
		id, err := pool.Mint(label, spawnDir)
		return string(id), err
	}, convRegistryPath, announceConversation, logger)
	ctrl.SetChannelCreator(createChannel)
	// Install the channel.post poster (#2497) over that creator and the SAME
	// conversation registry and durable log every other conversation-keyed seam
	// above resolves against, so a posted message and a served history page
	// cannot come from two stores that mint duplicate ids for one conversation.
	//
	// defaultCwd is the workspace a label that matches nothing creates under —
	// resolved once at the top of this function, where create_conversation's
	// null-cwd path already takes it, rather than a second time here. The verb
	// carries no cwd on its wire, so this is the only path value it can have.
	//
	// conversationHistory.Append rather than appendConversationHistory: the poster
	// must be able to FAIL when the write fails, which that seam's contract
	// deliberately does not allow. See channelPoster.
	//
	// announcePost (#2498) is the relay leg's posted-message fan-out, so a message
	// a cron posts reaches every open client without a reconnect rather than
	// waiting for its next connect. It arrives nil from startRelay's no-URL early
	// return — announceConversation's shape one wiring up — and the poster records
	// exactly as before when it is.
	//
	// postCarry.record (#2499) is the third half of the carry built above the queue:
	// this is where a post becomes pending state, and the queue's two seams are where
	// it is carried to claude and cleared.
	ctrl.SetChannelPoster(channelPoster(convReg, createChannel, defaultCwd, conversationHistory.Append, announcePost, postCarry.record, logger))
	if err := ctrl.Listen(); err != nil {
		return fmt.Errorf("control listen: %w", err)
	}
	defer func() { _ = ctrl.Close() }()

	ctrlDone := make(chan error, 1)
	go func() { ctrlDone <- ctrl.Serve(ctx) }()

	logger.Info("pyrycode starting",
		"version", Version,
		"name", *name,
		"claude", *claudeBin,
		"socket", socketPath,
	)
	runErr := pool.Run(ctx)
	// Pool.Run can return with the daemon ctx still LIVE: any early non-ctx error
	// out of its errgroup returns from a ctx DERIVED from ours, so cancelling that
	// one does not cancel this one. Queue.Run blocks on ctx.Done before joining
	// its drains, so the <-qDone join below would never complete (#1492). Cancel
	// first — on the shutdown paths that reach here today (signal, `pyry stop`, a
	// fatal 4409) ctx is already cancelled and this is a no-op that cannot
	// displace the recorded cause.
	cancelCause(nil)

	// Stop the control server (already wired to ctx but Close is idempotent
	// and ensures the socket file is gone before we return).
	_ = ctrl.Close()
	<-ctrlDone

	// Join the inbound-queue lifecycle: ctx is cancelled by the time we get here
	// (either before pool.Run returned or by the cancelCause above), so queue.Run
	// has observed ctx.Done and is winding down its drains. Waiting here keeps the
	// daemon from exiting while a drain goroutine is still in flight.
	<-qDone

	if runErr != nil && !errors.Is(runErr, context.Canceled) {
		return fmt.Errorf("supervisor: %w", runErr)
	}
	// A clean ctx-cancel got us here. Distinguish WHY: a self-initiated fatal
	// shutdown (persistent 4409) cancelled with an error cause, so exit
	// non-zero and let launchd restart the daemon. An operator stop (SIGTERM,
	// `pyry stop`) cancelled with a nil cause and stays down at exit 0.
	if cause := fatalCause(ctx); cause != nil {
		logger.Error("pyrycode fatal shutdown", "cause", cause)
		return cause
	}
	logger.Info("pyrycode stopped")
	return nil
}

// fatalCause reports the self-initiated fatal shutdown reason carried by the
// daemon's cause context, or nil for an operator-initiated stop. A shutdown
// via context.CancelCauseFunc records a cause: the operator paths (SIGTERM /
// SIGINT / `pyry stop`) cancel with nil, which context.Cause reports as
// context.Canceled, while a self-initiated fatal path (the relay's persistent
// 4409 handler) cancels with a real error. Returning that error makes
// runSupervisor exit non-zero so launchd (KeepAlive SuccessfulExit:false)
// restarts the daemon; the operator paths return nil and stay down. Pure so it
// can be unit-tested directly (nil cause / context.Canceled / real cause).
func fatalCause(ctx context.Context) error {
	cause := context.Cause(ctx)
	if cause == nil || errors.Is(cause, context.Canceled) {
		return nil
	}
	return cause
}

// poolResolver adapts *sessions.Pool to control.SessionResolver. The shapes
// differ only in the return type: Pool.Lookup returns *sessions.Session,
// SessionResolver.Lookup returns control.Session (an interface satisfied
// structurally by *sessions.Session). Go's lack of covariant return types on
// interface satisfaction is the only reason this adapter exists.
type poolResolver struct{ p *sessions.Pool }

func (r poolResolver) Lookup(id sessions.SessionID) (control.Session, error) {
	return r.p.Lookup(id)
}

func (r poolResolver) ResolveID(arg string) (sessions.SessionID, error) {
	return r.p.ResolveID(arg)
}

// sessionMinter adapts *sessions.Pool to handlers.SessionCreator. It narrows the
// type (Pool returns sessions.SessionID; the handler interface speaks plain
// string, keeping internal/relay/handlers free of an internal/sessions import)
// and owns the cmd-layer validation of the phone-requested spawn workdir:
// resolveSpawnDir confines + trust-marks a set spawnDir before it reaches
// Pool.Mint, which uses the resolved realpath verbatim (#685). A rejected
// spawnDir wraps handlers.ErrSpawnDirRejected and short-circuits before any
// mint. The precedent for this type-narrowing seam is poolResolver above.
//
// It mints WITHOUT spawning (#2085). The conversation's session is still bound
// and persisted at create_conversation, exactly as before; only the child is
// deferred, to the first message on that conversation — where the drain's
// boundSession.Activate brings it up on the same lazy path an idle-evicted
// conversation already takes. That is what lets a model, an effort or a
// permission mode chosen before the first message be simply what the child
// launches with, including one cleared back to claude's own default, which
// cannot be expressed in-band at all.
//
// SECURITY: the deferral widens the validated-then-spawn window from
// milliseconds to "whenever the operator sends". The decision, recorded in this
// ticket's spec, is to ACCEPT it rather than re-validate at the spawn site.
// resolveSpawnDir returns trustMark's own realpath and that value is frozen onto
// the session at build time — no phone-influenced state is re-read between the
// check and the chdir, so the phone gains nothing from the wait. Winning the
// window means replacing an ancestor of an already-resolved realpath under the
// operator's $HOME, which needs the $HOME write access the confinement exists to
// protect and claude itself already holds. This is a widening of the residual
// TOCTOU that conversation-session-binding.md already records as accepted, not a
// new class of exposure. It differs from the #1487 revive path — which DOES
// re-run resolveSpawnDir at its own spawn site — because that path re-reads a
// raw, persisted conv.Cwd from a mutable file across a daemon restart, so its
// stored value is unvalidated bytes from a previous process lifetime.
type sessionMinter struct{ p *sessions.Pool }

// Create satisfies handlers.SessionCreator. The ctx is discarded because neither
// half of this can observe one: resolveSpawnDir takes no context.Context, and
// Pool.Mint is ctx-free by contract because it cannot spawn. The parameter stays
// for the seam's shape, which the handler shares with its cancellable siblings.
//
// The honest consequence is that the handler's mint budget cannot interrupt this
// — a wedged filesystem blocks in a syscall regardless of any deadline. See
// handlers.createConversationMintTimeout, which records the same thing rather
// than claiming a protection it no longer provides.
func (m sessionMinter) Create(_ context.Context, label, spawnDir string) (string, error) {
	resolved, err := resolveSpawnDir(spawnDir)
	if err != nil {
		return "", err
	}
	id, err := m.p.Mint(label, resolved)
	return string(id), err
}

// settingsUpdaterAdapter adapts *sessions.Pool to relay.SettingsUpdater (#845,
// model-vocabulary validation #2281).
// It narrows the type (relay speaks relay.SettingsUpdate / relay.ErrSessionUnknown
// so internal/relay imports neither internal/sessions nor cmd/pyry) and owns the
// sessions.ErrSessionNotFound → relay.ErrSessionUnknown mapping and the retained
// model-vocabulary decision — the
// project convention that sentinel-to-wire mapping lives at the consumer call
// site, not in the primitive. The four presence pointers pass straight through:
// relay.SettingsUpdate mirrors sessions.SettingsUpdate 1:1, so a nil field still
// means "leave unchanged" and a nil YOLO can never enable bypass. The precedent
// for this type-narrowing seam is sessionMinter / poolResolver above.
//
// A non-empty model is checked before Pool.UpdateSettings against the same
// retainedModelVocabulary used for client publication. This adapter is the one
// layer that can see both cmd-side retention and the sessions primitive without
// inverting either package dependency. Empty retains its restart-to-default
// meaning and skips membership.
//
// The mirror is maintained BY HAND, so a field added on one side and forgotten
// here compiles and ships as a silent no-op. That is what
// TestSettingsUpdaterAdapter_CarriesPermissionMode exists to catch, and why it
// asserts on a REJECTED mode: the pool validates a posture only when the update
// names one, so a dropped field returns nil rather than an error.
type settingsUpdaterAdapter struct {
	p *sessions.Pool
	// saved is the daemon's persisted model vocabulary (#2450), the third source
	// retainedModelVocabulary reads when neither hold holds anything. It is carried
	// here rather than re-derived so this gate and the two client-facing seams in
	// relayWiring answer from the SAME three sources — a membership check that saw
	// fewer sources than the menu the client was offered would refuse a model that
	// menu had just advertised. nil is a daemon built without a store and is two
	// sources, not an error.
	saved savedModelVocabulary
}

func (a settingsUpdaterAdapter) UpdateSettings(id string, u relay.SettingsUpdate) error {
	sessionID := sessions.SessionID(id)
	if u.Model != nil && *u.Model != "" {
		// Check membership only for a session the daemon actually has a record of.
		// Apart from preserving session.not_found precedence, this prevents an
		// unknown id from probing whether the bootstrap vocabulary is complete —
		// which is why requireKnownSession must stay AHEAD of the vocabulary read
		// below rather than being folded into the write.
		// Pool.Lookup("") deliberately resolves the bootstrap session for legacy
		// internal callers, while neither pool write accepts anything but an exact
		// map key. Preserve the update seam's unknown-session behavior before
		// consulting the vocabulary.
		if sessionID == "" {
			return relay.ErrSessionUnknown
		}
		if err := a.requireKnownSession(sessionID); err != nil {
			return err
		}
		list, have := retainedModelVocabulary(a.p, a.saved, id)
		if err := validateModelVocabulary(list, have, *u.Model); err != nil {
			return err
		}
	}

	update := sessions.SettingsUpdate{
		Model:          u.Model,
		Effort:         u.Effort,
		YOLO:           u.YOLO,
		PermissionMode: u.PermissionMode,
	}

	// TWO WRITES, LIVE FIRST, EACH WITH ITS OWN MISS (#2463) — resolveBoundRunSettings'
	// composition for the read half (#2449), applied to the write. A session the pool
	// holds is written through Pool.UpdateSettings; one the daemon has only a persisted
	// record of is merged into that entry by Pool.UpdateDormantSettings, which is what
	// the first message will then revive it under. Only an id in neither half is
	// unknown. Before this the dormant case fell through to the refusal, and since
	// Pool.New materialises just the bootstrap, that was EVERY conversation after a
	// daemon restart — a model or effort picked before the channel's first message
	// simply did not land.
	//
	// The order is not interchangeable, for the reason the read's twin records: live
	// first means a session the pool holds is never written into a stale persisted
	// entry, and that is a guarantee of this function rather than merely of the pool's
	// bookkeeping (Pool.materialise retires the dormant entry it takes over, so the
	// halves partition — but a composition that asked in the other order would depend
	// on that staying true forever).
	//
	// ONLY ErrSessionNotFound falls through. Every other error from the live write —
	// an unsupported mode, a contradicting posture pair, a failed save — is that
	// session's answer and is returned as it is today; retrying such a frame against
	// the dormant half would be asking a second writer to re-judge a verdict already
	// reached.
	//
	// A revive can land between the two writes and retire the entry. p.dormant only
	// ever shrinks, so the id can only move that way and the dormant write finds a
	// clean miss rather than a torn entry; session.not_found is then the correct
	// answer, since the settings reached nothing. No retry — the operator's next pick
	// goes through the live half.
	if err := a.p.UpdateSettings(sessionID, update); !errors.Is(err, sessions.ErrSessionNotFound) {
		return err
	}
	err := a.p.UpdateDormantSettings(sessionID, update)
	// Both sentinels become session.not_found, which is a deliberate decision and not
	// a lost distinction: a dormant session cannot store a posture (a revive does not
	// restore one, #1487), and the user-visible outcome is identical either way — the
	// client's menu snaps back. A distinguishable code is wire vocabulary plus client
	// work, and no client has been observed mis-reading this one. The two stay
	// separate sentinels INSIDE internal/sessions for the reason
	// ErrDormantPostureUnsupported records; it is this seam that collapses them.
	if errors.Is(err, sessions.ErrSessionNotFound) || errors.Is(err, sessions.ErrDormantPostureUnsupported) {
		return relay.ErrSessionUnknown
	}
	return err
}

// requireKnownSession reports whether this daemon has any record of id — live or
// dormant — mapping an absence to relay.ErrSessionUnknown. It is the gate's
// existence probe, hoisted out of UpdateSettings because it now asks two
// questions and the ordering constraint above is easier to see with one call.
//
// Pool.DormantSettingsFor is reused as the dormant probe and its value discarded:
// it already answers exactly "does this pool hold a dormant entry for id", with a
// miss of its own, so no new pool surface is needed for a question the read half
// already exposes.
//
// Both probes are advisory by the time the write runs — a revive can retire an
// entry in between — and that is sound rather than tolerated: the id can only move
// dormant→live, the live write is attempted first, and an id that moved is written
// by the half that now holds it. The probe's job is to refuse an id the daemon has
// NO record of before the vocabulary is consulted, and an unknown id stays unknown.
func (a settingsUpdaterAdapter) requireKnownSession(id sessions.SessionID) error {
	_, err := a.p.Lookup(id)
	if err == nil {
		return nil
	}
	if !errors.Is(err, sessions.ErrSessionNotFound) {
		return err
	}
	if _, err := a.p.DormantSettingsFor(id); err != nil {
		return relay.ErrSessionUnknown
	}
	return nil
}

// validateModelVocabulary classifies one non-empty client model against the same
// retained menu used for publication. A complete untruncated row set can prove
// absence; a missing list, a dropped row, or a cut Value cannot. Exact match is
// checked first because a present full row proves availability even when a
// different row was lost or truncated.
//
// Only ModelOption.Value participates. Neither the requested value nor any menu
// value is included in the returned sentinels, so callers can log the outcome
// without disclosing model vocabulary.
func validateModelVocabulary(list turnevent.ModelList, have bool, model string) error {
	if model == "" {
		return nil
	}
	if !have || len(list.Models) == 0 {
		return relay.ErrModelVocabularyUnavailable
	}
	for _, option := range list.Models {
		if option.Value == model && !slices.Contains(option.TruncatedFields, "value") {
			return nil
		}
	}
	if list.DroppedModels > 0 {
		return relay.ErrModelVocabularyUnavailable
	}
	for _, option := range list.Models {
		if slices.Contains(option.TruncatedFields, "value") {
			return relay.ErrModelVocabularyUnavailable
		}
	}
	return relay.ErrModelNotOffered
}

// errNoBoundSession is the sentinel sessionRouter.Route returns when a
// conversation exists but has no live bound session — an empty
// CurrentSessionID. It has no wire surface; the send_message handler maps any
// non-ErrConversationNotFound Route error to a retryable server.binary_offline
// reply. Rejecting the empty binding here, before any Pool.Lookup, is
// load-bearing: Pool.Lookup("") returns the bootstrap session, so without this
// guard an unbound conversation would silently route the phone's turn into the
// shared bootstrap claude (the isolation break #678 AC#4 forbids).
var errNoBoundSession = errors.New("conversation has no bound session")

// sessionRouter adapts *sessions.Pool + *conversations.Registry to
// handlers.SessionRouter (#678). cmd/pyry is the only package importing both,
// so the conversation→session resolution that bridges them lives here, beside
// sessionMinter and poolResolver. Route maps a send_message frame's
// ConversationID to the write surface for that conversation's bound session.
type sessionRouter struct {
	pool    *sessions.Pool
	convReg *conversations.Registry
	// active records the conversation each successful Route resolves — the signal
	// the structured turn stream reads as its cursor (#687). Route has a value
	// receiver behind the handlers.SessionRouter interface, so this is a pointer:
	// the copy must write the one holder the emitter reads.
	active *activeConversation
}

// resolve maps conversationID to its bound session's write surface WITHOUT
// stamping the active-conversation cursor. It is the single resolution
// authority: the order is load-bearing — the empty-CurrentSessionID guard fires
// before any Lookup so an unbound conversation never resolves to the bootstrap
// session that Pool.Lookup("") returns (#678 AC#4), and therefore also before
// the revive branch below, which must never be reachable for an unbound
// conversation. Both Route (handler-side validation, which layers the cursor
// stamp on top) and newInboundDeliver (the drain seam, which must NOT stamp) go
// through resolve, so neither path can bypass the guard. The drain re-resolves
// per attempt because the binding may change between enqueue and delivery
// (#721).
func (r sessionRouter) resolve(conversationID string) (handlers.TurnWriter, error) {
	conv, ok := r.convReg.Get(conversations.ConversationID(conversationID))
	if !ok {
		return nil, conversations.ErrConversationNotFound
	}
	if conv.CurrentSessionID == "" {
		return nil, errNoBoundSession
	}
	id := sessions.SessionID(conv.CurrentSessionID)
	sess, err := r.pool.Lookup(id)
	if errors.Is(err, sessions.ErrSessionNotFound) {
		// A healthy binding pointing at an id the pool lacks is the daemon-
		// restart case: sessions.New materialises only the bootstrap, so every
		// per-conversation minted session is dropped and its thread would be
		// permanently dead. Re-materialise it lazily, on first touch (#1487).
		sess, err = r.revive(id, conversationID, conv.Cwd)
	}
	if err != nil {
		return nil, err
	}
	return boundSession{pool: r.pool, sess: sess, id: id}, nil
}

// revive re-materialises a conversation's dropped session so the caller gets the
// same write surface a live binding yields. It does NOT spawn claude: Pool.Revive
// registers the session in the evicted state, and the child comes up on the
// Activate that boundSession already performs — so resolve stays free of the
// blocking spawn wait the send_message path removed in #721.
//
// resolveSpawnDir is the SAME validator the mint path uses (sessionMinter.Create),
// re-run here rather than trusting the recorded value: conv.Cwd was confined to
// $HOME when the conversation was minted, but a path valid then can be turned
// into an escape before the restart, and this is the spawn site that would
// otherwise believe the stale check (#685/#696, #1487 AC#4). A rejected Cwd
// returns wrapping handlers.ErrSpawnDirRejected before any pool state is touched,
// leaving the conversation rejected exactly as it is today — never spawning
// outside the boundary.
//
// The label is the conversation id, matching what create_conversation originally
// minted the session with.
func (r sessionRouter) revive(id sessions.SessionID, label, cwd string) (*sessions.Session, error) {
	spawnDir, err := resolveSpawnDir(cwd)
	if err != nil {
		return nil, err
	}
	return r.pool.Revive(id, label, spawnDir)
}

// Route resolves conversationID to its bound session's write surface and stamps
// the active-conversation cursor on success. It is a thin wrapper over resolve:
// the only thing it adds is the cursor stamp, fired on the successful-route path
// only, so a rejected route (unknown / unbound / dangling) never moves it
// (#687). The drain path must not move the cursor, so it calls resolve directly.
func (r sessionRouter) Route(conversationID string) (handlers.TurnWriter, error) {
	w, err := r.resolve(conversationID)
	if err != nil {
		return nil, err
	}
	r.active.set(conversationID)
	return w, nil
}

// boundSession is the per-conversation write surface sessionRouter.Route
// returns. *sessions.Session already satisfies handlers.TurnWriter directly;
// this wrapper exists only to redirect Activate through Pool.Activate — the
// single cap-enforcing spawn-path entry — instead of Session.Activate, which
// would bypass ActiveCap (the invariant the idle-evict follow-up #680 relies
// on). WriteUserTurn passes straight through to the resolved session.
type boundSession struct {
	pool *sessions.Pool
	sess *sessions.Session
	id   sessions.SessionID
}

func (b boundSession) Activate(ctx context.Context) error {
	return b.pool.Activate(ctx, b.id)
}

func (b boundSession) WriteUserTurn(ctx context.Context, conversationID string, payload []byte) error {
	return b.sess.WriteUserTurn(ctx, conversationID, payload)
}

// interruptArm names which actuation interruptRunner dispatched to. The constant
// VALUES are operator-facing: activeInterrupter.SendEsc logs them verbatim as a
// record's arm field (#1193), so they are part of the record contract, not an
// internal detail.
type interruptArm string

const (
	armInterrupt interruptArm = "interrupt" // streamRunner.Interrupt()
	armNone      interruptArm = "none"      // no interrupt method — inert
)

// interruptRunner actuates a runner's interrupt through the one concrete method a
// runner can expose. The dispatch lives in cmd/pyry (#1121) because Interrupt is
// OFF the sessions.Runner interface (un-widened, #1077) and the streamRunner
// adapter carrying it lives here, so internal/sessions cannot reach the method at
// all. It is an OPTIONAL capability, asserted for: a runner without Interrupt is
// inert (nil) — no actuation beats wrong actuation.
//
// It returns the arm it dispatched to alongside the chosen method's error, so the
// caller — the only scope holding the conversation id — can record which arm ran
// (#1193); armNone always pairs with a nil error. The dispatcher itself stays pure:
// no logger, no ambient state. The arm is an OBSERVABILITY value and nothing may
// branch on it beyond selecting a record — in particular armNone must NOT trigger a
// fallback actuation, since the only other runner to try is the bootstrap session's
// runner, the #678 cross-conversation isolation break resolveBoundRunner's
// guard exists to prevent.
func interruptRunner(r sessions.Runner) (interruptArm, error) {
	if v, ok := r.(interface{ Interrupt() error }); ok {
		return armInterrupt, v.Interrupt()
	}
	return armNone, nil
}

// resolveBoundRunner resolves the active conversation's bound runner, mirroring
// boundHost's lookup shape and sessionRouter.resolve's load-bearing guard:
// convID → CurrentSessionID → Pool.Lookup → the bound session's runner. The
// conv.CurrentSessionID == "" guard is the cross-conversation isolation
// enforcement point — without it Pool.Lookup("") returns the BOOTSTRAP session
// (see errNoBoundSession / sessionRouter.resolve), so an unbound conversation's
// interrupt would actuate the shared bootstrap claude (the #678 isolation break).
// Every non-resolvable state returns (nil, "", false) so the caller stays inert;
// this NEVER falls through to bootstrap. The returned conversation id comes from
// the matched registry record so callers that stamp output never need to reflect
// the untrusted lookup key.
func resolveBoundRunner(
	convReg *conversations.Registry,
	pool *sessions.Pool,
	convID string,
) (sessions.Runner, conversations.ConversationID, bool) {
	conv, ok := convReg.Get(conversations.ConversationID(convID))
	if !ok || conv.CurrentSessionID == "" {
		return nil, "", false
	}
	sess, err := pool.Lookup(sessions.SessionID(conv.CurrentSessionID))
	if err != nil {
		return nil, "", false
	}
	return sess.Runner(), conv.ID, true
}

type mcpStatusQuerier interface {
	QueryMCPStatus(context.Context) (turnevent.MCPStatus, bool)
}

// resolveBoundMCPStatus queries only the runner currently bound to convID and
// shapes its bounded event through the same mapper as the automatic live path.
// The request id remains lookup-only; the mapped payload is stamped with the
// registry-owned conversation id returned alongside the runner. Every refusal
// returns the zero payload; there is no retained-status fallback.
func resolveBoundMCPStatus(
	ctx context.Context,
	convReg *conversations.Registry,
	pool *sessions.Pool,
	convID string,
) (protocol.MCPStatusPayload, bool) {
	runner, canonicalID, ok := resolveBoundRunner(convReg, pool, convID)
	if !ok {
		return protocol.MCPStatusPayload{}, false
	}
	querier, ok := runner.(mcpStatusQuerier)
	if !ok {
		return protocol.MCPStatusPayload{}, false
	}
	status, ok := querier.QueryMCPStatus(ctx)
	if !ok {
		return protocol.MCPStatusPayload{}, false
	}
	typ, mapped, ok := turnbridge.MapEvent(status, turnbridge.TurnContext{ConversationID: string(canonicalID)})
	if !ok || typ != protocol.TypeMCPStatus {
		return protocol.MCPStatusPayload{}, false
	}
	payload, ok := mapped.(protocol.MCPStatusPayload)
	if !ok {
		return protocol.MCPStatusPayload{}, false
	}
	return payload, true
}

func mcpStatusFor(
	convReg *conversations.Registry,
	pool *sessions.Pool,
) func(context.Context, string) (protocol.MCPStatusPayload, bool) {
	if convReg == nil || pool == nil {
		return nil
	}
	return func(ctx context.Context, convID string) (protocol.MCPStatusPayload, bool) {
		return resolveBoundMCPStatus(ctx, convReg, pool, convID)
	}
}

// effectiveEffortQueryTimeout bounds one child round trip. A live child can stay
// silent forever, and this provider runs on the requesting connection's worker.
const effectiveEffortQueryTimeout = 30 * time.Second

type effectiveEffortQuerier interface {
	QueryAppliedSettings(context.Context) (streamsup.AppliedSettings, bool)
}

// resolveBoundEffectiveEffort asks only the exact live child currently reached by
// convID's registry binding. resolveBoundRunner owns the load-bearing empty-binding
// guard: without it Pool.Lookup("") selects bootstrap, which would let an unbound
// conversation read another conversation's applied effort.
//
// The child result stays narrow at this boundary. Model is deliberately discarded;
// Effort alone crosses into the relay provider, where a nil pointer with true means
// confirmed JSON null and false means unavailable. Every refusal is content-free,
// and this function never starts a session, mutates settings, or falls back to a
// retained reading.
func resolveBoundEffectiveEffort(
	ctx context.Context,
	convReg *conversations.Registry,
	pool *sessions.Pool,
	convID string,
) (*string, bool) {
	if ctx.Err() != nil {
		return nil, false
	}
	runner, _, ok := resolveBoundRunner(convReg, pool, convID)
	if !ok {
		return nil, false
	}
	querier, ok := runner.(effectiveEffortQuerier)
	if !ok {
		return nil, false
	}

	queryCtx, cancel := context.WithTimeout(ctx, effectiveEffortQueryTimeout)
	defer cancel()
	settings, ok := querier.QueryAppliedSettings(queryCtx)
	if !ok {
		return nil, false
	}
	return settings.Effort, true
}

// effectiveEffortFor preserves the relay seam's nil-unwired contract. A closure is
// built only when both halves of exact-child resolution exist; foreground and
// isolated constructions therefore continue to return saved settings while omitting
// effective_effort. The closure retains the registry and pool, not a resolved runner,
// so every call observes a fresh binding.
func effectiveEffortFor(
	convReg *conversations.Registry,
	pool *sessions.Pool,
) func(context.Context, string) (*string, bool) {
	if convReg == nil || pool == nil {
		return nil
	}
	return func(ctx context.Context, convID string) (*string, bool) {
		return resolveBoundEffectiveEffort(ctx, convReg, pool, convID)
	}
}

// activeInterrupter satisfies relay.Interrupter by routing an inbound interrupt to
// the runner bound to the conversation the frame NAMES (#2103), falling back to the
// ACTIVE conversation when it names none — replacing the former Interrupter: w.sup
// wiring that mis-delivered every interrupt to the bootstrap supervisor regardless
// of which conversation's turn was running (#1121). The two seams are injected (not
// raw *Pool/*Registry) so active_interrupter_test.go can drive the composition with
// fakes; production wires currentConv: active.CurrentConversation and resolveRunner
// over resolveBoundRunner(convReg, pool, …), and #2103 changed neither field, only
// how SendEsc picks the id it passes.
type activeInterrupter struct {
	currentConv   func() string
	resolveRunner func(convID string) (sessions.Runner, bool)

	// log records which arm an inbound interrupt took (#1192). Optional: nil
	// falls back to slog.Default() via logger().
	log *slog.Logger
}

// logger returns a's logger, falling back to slog.Default() when unset.
// activeInterrupter is a constructor-less bag of injected seams built as a
// named-field literal, so an omitted field is a reachable state — and a nil
// *slog.Logger panics on first use, which on this remotely-driven relay path
// would be a latent crash on a rarely-hit inert arm. Falling back to the default
// logger (not a discard handler) keeps an unwired literal's record visible.
func (a activeInterrupter) logger() *slog.Logger {
	if a.log == nil {
		return slog.Default()
	}
	return a.log
}

// SendEsc interrupts the runner bound to the conversation the frame NAMES, or —
// when it names none — the one the daemon's cursor points at (#1121, widened by
// #2103). It keeps the relay seam's method name (relay.Interrupter.SendEsc) even
// though the actuation is a per-runner interrupt, not literally an Esc — the seam
// doc already abstracts SendEsc as "claude's own interrupt" (#1121 seam decision),
// so renaming would churn the whole internal/relay package for no behavioural gain.
//
// conversationID is UNTRUSTED: it is the string a paired client put on the wire,
// and internal/relay forwards it unjudged because it can neither shape-check nor
// resolve it (relay.Interrupter's own doc block states that division). This method
// is the trust boundary, and the order below is what discharges it.
//
// The empty-string branch runs BEFORE conversations.ValidID, not after:
// ValidID("") is false by that function's own doc, so reversing the two would
// refuse every un-upgraded client's bare frame — the only shape interrupt had from
// #707 until #2103 — and silently break backward compatibility. From there every
// ambiguous state (no active conversation, a non-canonical named id, an
// unknown/unbound binding) is inert (nil), never actuating the wrong child; the
// actuation's own error propagates for handleInterrupt to Warn-log and tolerate
// (best-effort contract).
//
// THERE IS DELIBERATELY NO LIVENESS PROBE, and this is where #2103 parts from its
// twin rather than by oversight. activeSessionStarter.StartNewSession needs
// `named && State().ChildPID == 0` because RestartFresh on a childless runner
// rekeys the pool, persists sessions.json, rebinds the conversation and broadcasts
// a session_transition — observable damage with no session to show for it. This
// actuator has no such hazard: streamsup.WriteInterrupt checks its writer for nil
// FIRST and returns ErrNoLiveChild having written nothing, so a conversation with
// no live child is inert by construction on the named and cursor paths alike.
// Adding the probe would introduce a refusal the bare path does not have today,
// which is the pre-#2103 behaviour AC-2 exists to preserve. Generalises: a
// blocker's late fix is not automatically the twin's requirement — trace the twin's
// actuation to its own write site before copying a guard across.
// EVERY arm records which one it took, at Info so the records are visible at the
// daemon's default level (#1192, #1193) — the inert ones, the actuation, and a
// bound runner exposing no interrupt method at all. Combined with handleInterrupt's
// own records that closes the route: an interrupt reaching it always leaves at
// least one v2.interrupt.* record, so on a wired daemon an empty log means the
// frame never arrived. The dispatched record is written when the arm RETURNS —
// under #1193's direction choice the arm identity IS interruptRunner's return
// value — so an actuation that blocked forever would leave none; both actuations
// are single small writes and no such hang has been observed. The records identify
// the CONVERSATION: resolveBoundRunner never surfaces the bound session id to this
// caller.
//
// The cursor is never WRITTEN here, and on a named frame it is not even READ; only
// sessionRouter.Route stamps it. Interrupting a named conversation therefore leaves
// the active conversation exactly where it was.
func (a activeInterrupter) SendEsc(conversationID string) error {
	convID := conversationID
	switch {
	case convID == "":
		// Nothing named: the pre-#2103 path, verbatim. The cursor's own id is
		// daemon-authored and is deliberately NOT shape-checked — doing so would
		// change behaviour on the path this branch exists to preserve.
		convID = a.currentConv()
		if convID == "" {
			// No conversation id to carry — the record's information is its existence:
			// the frame reached SendEsc and nothing was active.
			a.logger().Info("relay: v2 interrupt inert; no active conversation",
				"event", "v2.interrupt.no_active_conv")
			return nil
		}
	case !conversations.ValidID(convID):
		// The ONE arm where an arbitrary client-chosen string reaches a log call, so
		// the ONE that needs boundedConvID: every arm below logs an id that has
		// passed ValidID and is provably 36 bytes. Refused before the registry is
		// touched, so a non-canonical string never becomes a lookup key.
		a.logger().Info("relay: v2 interrupt inert; named conversation id is not canonical",
			"event", "v2.interrupt.invalid_conv_id",
			"conversation_id", boundedConvID(convID))
		return nil
	}
	r, ok := a.resolveRunner(convID)
	if !ok {
		// One record for the unknown id and the known-but-unbound one alike:
		// resolveBoundRunner refuses both identically, so this caller structurally
		// cannot distinguish them — which is also what keeps the frame from
		// answering "does this conversation exist?" to a client that gets no reply.
		a.logger().Info("relay: v2 interrupt inert; conversation has no bound runner",
			"event", "v2.interrupt.no_bound_runner",
			"conversation_id", convID)
		return nil
	}
	arm, err := interruptRunner(r)
	if arm == armNone {
		a.logger().Info("relay: v2 interrupt inert; bound runner exposes no interrupt method",
			"event", "v2.interrupt.no_actuator",
			"conversation_id", convID)
		return err
	}
	// Emitted even when the arm returned an error: this records WHICH arm was
	// dispatched to, not that the child quiesced (hence dispatched, not actuated).
	// handleInterrupt's v2.interrupt.keystroke_err carries the error but not the
	// arm, so on a failed actuation the PAIR is what names the failing actuation —
	// suppressing this record on error would delete that. The error itself is NOT
	// repeated here: keystroke_err already carries it, and logging a wrapped
	// supervisor/streamsup sentinel twice under two correlation keys widens the
	// record surface for no diagnostic gain. arm is logged as a plain string, never
	// the named
	// type: slog renders a named string type through the Any path, and relay's
	// TextHandler and cmd/pyry's JSONHandler do not agree on how that renders.
	a.logger().Info("relay: v2 interrupt dispatched",
		"event", "v2.interrupt.dispatched",
		"conversation_id", convID,
		"arm", string(arm))
	return err
}

// resolveBoundSession is the new_session twin of resolveBoundRunner: it resolves
// the active conversation's bound *Session AND its bound id, so the caller can
// both reach the runner (sess.Runner()) and hand the id to the pool's rotation
// (Pool.RotateForNewSession). Same convID → CurrentSessionID == "" guard →
// Pool.Lookup body; the CurrentSessionID == "" guard is the same #678 isolation
// enforcement point resolveBoundRunner documents — Pool.Lookup("") returns the
// BOOTSTRAP session, so an unbound conversation must be rejected BEFORE Lookup or
// its new_session would rotate the shared bootstrap child. Every non-resolvable
// state returns (nil, "", false) so the caller stays inert; this NEVER falls
// through to bootstrap. The small Get→guard→Lookup duplication with
// resolveBoundRunner is accepted: keeping interrupt's resolveBoundRunner
// byte-stable is worth more than folding the two (the same tolerance #1121 was
// granted for its isolation-guard duplication).
// Since #1475 it also returns the conversation's RECORDED WORKSPACE, taken off
// the same row the binding came from so the two can never describe different
// conversations. It is returned RAW — unvalidated, exactly as change_workspace
// stored it — because re-confining it belongs at the spawn site (the posture
// sessionRouter.revive's doc argues for a persisted Cwd), not at a resolver that
// also serves callers with no spawn to perform. The empty string is the ordinary
// answer for a conversation whose workspace was never set.
func resolveBoundSession(convReg *conversations.Registry, pool *sessions.Pool, convID string) (*sessions.Session, sessions.SessionID, string, bool) {
	conv, ok := convReg.Get(conversations.ConversationID(convID))
	if !ok || conv.CurrentSessionID == "" {
		return nil, "", "", false
	}
	sess, err := pool.Lookup(sessions.SessionID(conv.CurrentSessionID))
	if err != nil {
		return nil, "", "", false
	}
	return sess, sessions.SessionID(conv.CurrentSessionID), conv.Cwd, true
}

// boundRunSettings is the settings half of one conversation's run configuration:
// the pool session it is bound to, plus that session's persisted model / effort
// and current-child permission confirmation, decoded into primitives HERE so the
// value crossing into relay.go carries no internal/sessions type — the same composition-root discipline
// snapshotSettings, settingsUpdaterAdapter and debugBundler keep.
//
// A struct rather than a four-value return: three adjacent same-typed strings in
// a return list transpose silently, while a named-field construction makes the
// swap a visible edit. That is the reasoning relayWiring's own doc records for
// named-field wiring (#917).
type boundRunSettings struct {
	sessionID string
	model     string
	effort    string
	yolo      bool
	// permissionMode is the last posture the exact current child confirmed. Empty
	// means there is no current-child confirmation, including for a dormant session.
	// yolo is derived from this value rather than from stored launch intent.
	permissionMode string
	// live says model and effort came from a session the pool HOLDS, rather
	// than from the persisted entry of one it has not materialised (#2449). It
	// carries no run configuration and never reaches the wire; its one consumer is
	// runConfigFor, which reads a dormant session's context occupancy as zero
	// because there is no transcript to stat until that session is revived.
	//
	// A separate field because the id cannot carry the distinction — both cases
	// report a real, addressable session id — and runConfigFor cannot re-derive it
	// without a pool of its own, which is the dependency that seam exists to avoid.
	//
	// The polarity is the fail-closed direction and not an accident of phrasing:
	// the zero boundRunSettings is NOT live, so "do not stat a transcript for a
	// session nobody confirmed the pool holds" falls out of the zero value rather
	// than out of a branch someone has to keep correct.
	live bool
}

// sessionSettingsReader is the three pool reads resolveBoundRunSettings needs,
// declared at the consumer per CODING-STYLE. runSettingsPool supplies the
// current-child read while promoting the two *sessions.Pool settings methods. It
// exists for a stated testing need rather than pre-emptively: "no
// pool read is performed for an unresolvable conversation" is a claim about
// CALLS, and the returned values cannot carry it — a conversation bound to an
// all-defaults session reports exactly the zeros a refusal reports — so the
// double has to be able to count.
//
// It was one method until #2449, whose whole subject is the dormant one: after a
// daemon restart the pool has materialised only the bootstrap, so SettingsFor
// alone answers "not addressable" for every other conversation the daemon holds a
// persisted record of. Widening it here is that ticket's intended change, and the
// counting need widened with it rather than being outgrown — the sequence a double
// records now also carries "a session the pool HOLDS is never read from the
// dormant half", which no single-method double could state.
//
// The third read is deliberately narrow: it returns only the confirmed permission
// pair for one exact live id, not a Session or Runner a resolver could actuate.
type sessionSettingsReader interface {
	SettingsFor(id sessions.SessionID) (sessions.SessionSettings, error)
	DormantSettingsFor(id sessions.SessionID) (sessions.SessionSettings, error)
	ConfirmedPermissionModeFor(id sessions.SessionID) (string, bool)
}

// confirmedPermissionModeReader is the optional runner capability #2511 shipped.
// It stays off sessions.Runner because its consumer lives in this package.
type confirmedPermissionModeReader interface {
	ConfirmedPermissionMode() (string, bool)
}

// runSettingsPool adds the exact-current-runner read to *sessions.Pool's stored
// settings reads. It refuses the empty id before Pool.Lookup, whose empty-id
// convention resolves to the bootstrap session.
type runSettingsPool struct {
	*sessions.Pool
}

func (p runSettingsPool) ConfirmedPermissionModeFor(id sessions.SessionID) (string, bool) {
	if id == "" {
		return "", false
	}
	sess, err := p.Lookup(id)
	if err != nil {
		return "", false
	}
	reader, ok := sess.Runner().(confirmedPermissionModeReader)
	if !ok {
		return "", false
	}
	return reader.ConfirmedPermissionMode()
}

// resolveBoundRunSettings is the run-configuration twin of resolveBoundSession:
// it resolves a named conversation to its bound session id AND that session's
// persisted settings, for the conversation-keyed run-configuration seam (#1609,
// composed with the context-window half at runConfigFor).
//
// The refusal is inherited verbatim rather than re-derived: an unknown
// conversation or an empty CurrentSessionID returns (zero, false) BEFORE the pool
// is touched. That second guard is the #678 isolation enforcement point
// resolveBoundSession documents — Pool.Lookup("") returns the BOOTSTRAP session,
// so an unbound conversation that reached a lookup would read the shared
// bootstrap child's run configuration. An empty convID lands in the first guard:
// no conversation carries an empty id, so the pool is never touched for it.
//
// TWO READS, LIVE FIRST, EACH WITH ITS OWN MISS (#2449). A conversation bound to
// a session the pool holds is answered from Pool.SettingsFor; one bound to a
// session the daemon has only a persisted record of is answered from
// Pool.DormantSettingsFor, which reports that entry's model and effort. Only an id
// in neither half is unresolvable. Before this the dormant case fell through to
// the refusal, and since Pool.New materialises just the bootstrap, that was EVERY
// conversation after a daemon restart — a reply whose every field sat at its zero,
// which a client renders as inert menus and a model label the channel is not on.
//
// The order is not interchangeable. Live first means a session the pool holds is
// never reported from a stale persisted entry, which is a guarantee of this
// function and not merely of the pool's bookkeeping (Pool.materialise retires the
// dormant entry it takes over, so the halves partition — but a resolver that
// asked in the other order would depend on that to stay true forever).
//
// The refusal is reached only after BOTH reads miss, and that is the whole of the
// last acceptance criterion: an id the daemon has no record of at all still gets
// the all-zero reply, because the dormant read has a miss of its own rather than
// collapsing an unknown id into empty settings (the reason Pool.revivedSettings,
// which does collapse it, could not be reused).
//
// Stored posture is never copied here. A live answer asks only the exact session's
// runner for Claude's last current-child confirmation; a dormant answer has no
// runner to ask and therefore keeps the permission pair at its zero value.
//
// Pool.SettingsFor, not Lookup-then-read, for two reasons load-bearing enough to
// state so a later reader does not "simplify" them away:
//
//   - ONE acquisition FOR STORED SETTINGS. SettingsFor answers whether the pool
//     held the id and what model/effort it stored under a single RLock;
//     DormantSettingsFor does the same for its half. The separate current-runner
//     lookup is informational and may race a lifecycle transition only into an
//     unavailable permission pair — never into another id's settings or runner.
//   - No ""-is-bootstrap convention. SettingsFor deliberately does not
//     special-case the empty id (its doc says why: read and write must agree), so
//     "" is an ordinary map miss here. Building on it means fall-through-to-
//     bootstrap has no expression in this code path at all — a second, structural
//     guarantee stacked on the guard above, never a replacement for it.
//
// Pool.DefaultSettings is untouched and must stay so: its doc forbids rewriting
// it as SettingsFor(BootstrapID()), which is two acquisitions with a rotation
// window between them. This adds a caller of SettingsFor and changes nothing
// about how the bootstrap's own settings are read.
//
// SECURITY: convID is untrusted network input and never leaves this function — it
// is a lookup key into the daemon's own registry and nothing else. SettingsFor's
// error is discarded rather than wrapped: it is returned bare precisely so a
// hostile or malformed id cannot be reflected into a log line or wire frame a
// caller builds from it. This resolver takes no logger and must not grow one —
// there is no operational event here to record, and the only thing a "why did it
// not resolve" line could add is the caller's id.
func resolveBoundRunSettings(convReg *conversations.Registry, pool sessionSettingsReader, convID string) (boundRunSettings, bool) {
	conv, ok := convReg.Get(conversations.ConversationID(convID))
	if !ok || conv.CurrentSessionID == "" {
		return boundRunSettings{}, false
	}
	id := sessions.SessionID(conv.CurrentSessionID)
	if s, err := pool.SettingsFor(id); err == nil {
		bound := settingsOf(conv.CurrentSessionID, s, true)
		if mode, confirmed := pool.ConfirmedPermissionModeFor(id); confirmed {
			bound.permissionMode = mode
			bound.yolo = mode == sessions.PermissionModeBypass
		}
		return bound, true
	}
	s, err := pool.DormantSettingsFor(id)
	if err != nil {
		return boundRunSettings{}, false
	}
	return settingsOf(conv.CurrentSessionID, s, false), true
}

// settingsOf decodes the stored half of one pool answer into the primitive-typed
// value that crosses into relay.go, tagged with which half of the pool answered.
// The permission pair is intentionally absent: only the live runner read above
// may populate it.
//
// It exists so the two return sites above cannot drift: a field added to
// sessions.SessionSettings and wired into only one of two hand-written literals
// compiles, ships, and reports that field for a live session while silently
// dropping it for a dormant one — the failure mode boundRunSettings' own doc
// records for transposed same-typed returns, in its other form.
func settingsOf(sessionID string, s sessions.SessionSettings, live bool) boundRunSettings {
	return boundRunSettings{
		sessionID: sessionID,
		model:     s.Model,
		effort:    s.Effort,
		live:      live,
	}
}

// spawnedPromptReader is the single pool method resolveConversationPrompt needs,
// declared at the consumer per CODING-STYLE; *sessions.Pool satisfies it with no
// adapter. It exists for the same stated testing need sessionSettingsReader above
// records: "no pool lookup is performed for an unbound conversation" is a claim
// about CALLS, and the returned values cannot carry it — a conversation whose
// session was spawned with no operator text reports exactly the "" a refusal
// reports — so the double has to be able to count. Do not widen it past
// SystemPromptFor.
type spawnedPromptReader interface {
	SystemPromptFor(id sessions.SessionID) (string, error)
}

// conversationPromptState is one conversation's system-prompt picture: what the
// registry stores today, and what the session it is currently bound to was
// actually spawned with. The two are read from different places and fail
// differently, which is the whole reason the type has two fields rather than one
// verdict — see resolveConversationPrompt.
//
// A struct rather than a two-value return, boundRunSettings' stated reason: two
// adjacent same-typed values in a return list transpose silently, and here the
// transposition would invert every verdict while type-checking perfectly.
type conversationPromptState struct {
	// stored is the registry's tri-state, COPIED rather than aliased: nil is "no
	// prompt", a non-nil pointer to "" is the explicitly-empty state, otherwise
	// the operator's text.
	stored *string
	// spawnedWith is the operator text the conversation's CURRENT session was
	// spawned with, or nil when there is no running session to compare against —
	// the conversation is bound to none, or bound to one the pool no longer holds.
	// A non-nil pointer to "" is a real answer meaning "spawned with no operator
	// text", and Pool.SystemPromptFor returns it for BOTH of the registry's
	// no-bytes states, which is why the comparison must collapse before it
	// compares (systemPromptFor, in relay.go, is where that happens).
	spawnedWith *string
}

// resolveConversationPrompt is the system-prompt twin of resolveBoundRunSettings:
// it resolves a named conversation to its stored prompt AND to what its running
// session was spawned with, for the conversation-keyed read seam #2152 puts on the
// wire (shaped into a payload at relay.go's systemPromptFor).
//
// THE TWO HALVES FAIL DIFFERENTLY AND THE RESULT KEEPS THEM APART. The comma-ok
// means only "this daemon does not host the named conversation" — an unknown id,
// returned BEFORE the pool is touched. A hosted conversation with no running
// session is a successful resolution whose spawnedWith is nil, because "nothing is
// running" is precisely when an operator most needs to see what is stored, and
// collapsing it into a refusal would suppress the stored value.
//
// The empty-CurrentSessionID guard is the #678 isolation enforcement point
// resolveBoundSession documents. Pool.SystemPromptFor is a plain map read and no
// session is keyed under "", so it would miss rather than return the bootstrap —
// but the guard stays anyway, so fall-through-to-a-shared-session has no
// expression in this code path at all rather than depending on a fact about
// another package's map. An empty convID lands in the first guard: no conversation
// carries an empty id, so the pool is never touched for it either.
//
// stored is a COPY OF THE POINTEE, never conv.SystemPrompt itself. Registry.Get
// copies the record shallowly under the registry mutex, so the pointer it returns
// aliases registry-held memory — the aliasing hazard SetSystemPrompt's own block
// flags for anything that projects the field. The read is race-free as it stands
// (the pointer is taken under the lock, and Go strings are immutable), so the copy
// is forward defence against a later change that retains or mutates it, not a fix
// for a live race.
//
// SECURITY: convID is untrusted network input and never leaves this function — it
// is a lookup key into the daemon's own registry and nothing else. The SESSION id
// handed to the pool is daemon-authored, read off the resolved record, so no
// caller can reach another conversation's session through here.
// Pool.SystemPromptFor's error is discarded rather than wrapped: it is dropped bare
// precisely so a hostile or malformed id cannot be reflected into a log line or
// wire frame a caller builds from it, and an evicted session is not an operational
// event — it is the ordinary "nothing is running" answer. This resolver takes no
// logger and MUST NOT grow one: the only things a "why did it not resolve" line
// could carry are the conversation id and the operator's prompt.
//
// Concurrency: the registry lock and the pool lock are taken SEQUENTIALLY and
// never nested, so this adds no edge to the daemon's lock order
// (Pool.SystemPromptFor requires p.mu unheld, and nothing is held here when it is
// called). The two acquisitions leave a window in which a rotation or an idle
// eviction lands between them, so a verdict can be one rotation stale; the client
// repairs that by asking again. Closing it would need a combined registry+pool
// acquisition no existing path takes — resolveBoundRunSettings' single-acquisition
// argument does not transfer, because there the values were fields of ONE session.
func resolveConversationPrompt(convReg *conversations.Registry, pool spawnedPromptReader, convID string) (conversationPromptState, bool) {
	conv, ok := convReg.Get(conversations.ConversationID(convID))
	if !ok {
		return conversationPromptState{}, false
	}
	var st conversationPromptState
	if conv.SystemPrompt != nil {
		stored := *conv.SystemPrompt
		st.stored = &stored
	}
	if conv.CurrentSessionID != "" {
		if spawned, err := pool.SystemPromptFor(sessions.SessionID(conv.CurrentSessionID)); err == nil {
			st.spawnedWith = &spawned
		}
	}
	return st, true
}

// startFreshRunner is the new_session twin of interruptRunner: it dispatches a
// fresh-session start to the active conversation's bound runner through the one
// concrete method a runner can expose. The streamRunner path (*streamsup.Runner,
// exposing RestartFresh) is DIRECT — rotate the pool-side id then RestartFresh so
// the next spawn uses --session-id <newID>, with NO /clear keystroke. Like
// Interrupt it is an OPTIONAL capability, asserted for: a runner without
// RestartFresh is inert (nil) and rotates nothing — no actuation beats wrong
// actuation (#1121).
//
// Ordering is load-bearing: rotate() completes — the pool-side re-key published
// under Pool.mu — BEFORE RestartFresh spawns <newID>.jsonl. Its original reason was
// the rotation watcher, which had to observe the id registered as freshly allocated
// before it saw the CREATE, or it double-rotated; #2137 retired the watcher and the
// skip-set, and the reason that survives is #1330's, below.
//
// That order is PRESERVED at #1330; the rotation gate is armed AHEAD of both, not
// substituted for either. rotate() also fires the ReasonClear transition fan-out,
// so without the arm the clear reaches clients — and releases whatever the turn
// tracker held — while the outgoing child is still alive and the fresh one does
// not yet exist. A turn accepted in that ~4 ms window writes into the doomed
// child, msgqueue reads the successful write as a commit and drops the head, and
// the turn is gone. Arming first makes every WriteUserTurn in the window return
// the retryable ErrNoLiveChild instead, so msgqueue re-attempts the same head
// until the fresh child binds.
// spawnDir, when non-empty, is the directory the successor must come up in —
// already re-confined by resolveSpawnDir at the caller (#1475). "" means "leave
// the runner where it is", which is both the no-recorded-workspace case and the
// refused case; either way the rotation still completes and the child still comes
// up. Installing it is an OPTIONAL capability on the same terms
// beginRotationOrNoop treats the gate: a runner that can RestartFresh but cannot
// move still rotates, rather than being sent to the inert path where it would
// rotate nothing.
//
// THE INSTALL SITS BETWEEN rotate AND RestartFresh, the window
// refreshSystemPromptForRotation occupies, and both edges are load-bearing.
// Below rotate, because a FAILED rotation must leave the runner untouched:
// installed above it, a rotate error would leave the directory swapped and the
// next crash-respawn would silently move a child no rotation ever replaced —
// which is exactly the "change_workspace alone does not move a running child"
// promise, broken from a remotely-driven frame that merely lost a race. Above
// RestartFresh, because that call cancels the live child at once and the Run loop
// answers on its own goroutine, so an install landing after it races the
// successor's own beginSpawn and the loser comes up in the pre-move directory.
//
// The resolve itself is deliberately NOT here. It does filesystem I/O, and the
// #1330 gate is armed across this whole function: every WriteUserTurn on the
// conversation returns ErrNoLiveChild while it is, so a blocking syscall inside
// the armed window would widen a ~4 ms refusal into a filesystem's worth.
// StartNewSession resolves before it calls, below all of its inert arms.
// log records the install's own failure and must be the daemon's, not
// slog.Default(): cmd/pyry never calls slog.SetDefault, so a default-logger record
// would leave the daemon's log entirely. Nil is tolerated and discards, which is
// what keeps the two direct test callers (dispatch_arms_test.go,
// inbound_deliver_rotation_test.go) free of a logger they have no assertion for.
func startFreshRunner(r sessions.Runner, oldID sessions.SessionID, spawnDir string,
	rotate func(sessions.SessionID) (sessions.SessionID, error), log *slog.Logger) error {
	v, ok := r.(interface{ RestartFresh(string) })
	if !ok {
		return nil
	}
	// The arming stays BELOW the inert return, not hoisted to the top of the
	// function: an unrecognised runner rotates nothing, and its return skips the
	// abort() disarm below, so a gate armed for it would outlive the rotation that
	// never happened — every subsequent WriteUserTurn on the conversation failing
	// with ErrNoLiveChild until the next respawn, from a remotely-driven frame.
	abort := beginRotationOrNoop(r)
	newID, err := rotate(oldID)
	if err != nil {
		// The rotation never happened, so the gate must not outlive it: left
		// armed it would refuse every turn on this conversation until the next
		// respawn — a wedge the failed rotation never earned. Losing a race with
		// a concurrent new_session frame is the ORDINARY way to land here
		// (RotateForNewSession's ErrSessionNotFound), which is exactly why the
		// disarm is generation-stamped runner-side: it must not clear the winner's
		// arm.
		abort()
		return err
	}
	installSpawnDir(r, spawnDir, log)
	v.RestartFresh(string(newID))
	return nil
}

// installSpawnDir moves the runner's next spawn into spawnDir when the runner can
// be moved and there is somewhere to move it to (#1475); anything else is inert.
//
// An OPTIONAL assertion, deliberately, for beginRotationOrNoop's stated reason
// rather than by resemblance to it: folding the method into startFreshRunner's
// RestartFresh assertion would send a runner that can restart but cannot move to
// the inert path, where it would rotate NOTHING — where today it rotates, just
// without moving. Both existing capability probes (startFreshRunner's and
// StartNewSession's) therefore keep asserting RestartFresh alone, so the
// inert-arm log keeps matching the dispatch.
//
// The install's own failure is Warned and SWALLOWED. It is reachable only when
// the directory disappears between the caller's confinement and this call, and by
// then the rotation is already committed — the pool is re-keyed, the conversation
// rebound, the transition broadcast — so refusing to respawn would leave the
// conversation with no child at all. Fail-closed means keeping the old directory,
// not withholding the successor. It is a DISTINCT event from the caller's
// rejection record: "refused by confinement" and "vanished before the install"
// are different failures and one event name for both would be unreadable.
//
// SECURITY: spawnDir is resolveSpawnDir's confined output or "". No path reaches
// the log — the record names the conversation and nothing else, the posture
// Pool.Revive's contract and sessionTranscriptDir's SECURITY paragraph both hold
// for phone-influenced workspace paths.
func installSpawnDir(r sessions.Runner, spawnDir string, log *slog.Logger) {
	if spawnDir == "" {
		return
	}
	m, ok := r.(interface{ SetSpawnWorkDir(string) error })
	if !ok {
		return
	}
	if err := m.SetSpawnWorkDir(spawnDir); err != nil && log != nil {
		log.Warn("relay: v2 new_session could not install the recorded workspace",
			"event", "v2.new_session.spawn_dir_install_failed")
	}
}

// beginRotationOrNoop arms r's rotation gate when the runner has one (#1330) and
// returns the disarm; a runner without the gate returns an inert disarm, leaving
// the dispatch shape unchanged.
//
// An OPTIONAL assertion, deliberately, rather than widening startFreshRunner's
// assertion to interface{ RestartFresh(string); BeginRotation() func() }. Widening
// it would send a runner that exposes RestartFresh without a gate to the inert
// path, where it would rotate NOTHING — where today it rotates, just ungated. That
// is the contract streamRunner.BeginRotation's doc already states. Treating the
// gate as a CAPABILITY is also how Interrupt and RestartFresh are already treated
// one layer up.
//
// rotatingRunner is what makes the optionality load-bearing to the existing
// coverage rather than academic: it offers the gate unconditionally and leaves the
// ARMING to the real startFreshRunner, which is what keeps
// TestInboundDeliver_RotationInProductionOrder_DeliversToFreshChild a question
// about the dispatch instead of one true by construction.
func beginRotationOrNoop(r sessions.Runner) (abort func()) {
	if g, ok := r.(interface{ BeginRotation() func() }); ok {
		return g.BeginRotation()
	}
	return func() {}
}

// activeSessionStarter satisfies relay.SessionStarter by routing an inbound
// new_session frame to the runner bound to the conversation the FRAME NAMES, or —
// when it names none — to the ACTIVE conversation's. It replaced the former
// SessionStarter: w.sup wiring that mis-delivered every new_session to the
// bootstrap supervisor regardless of which conversation the remote client was in
// (the #1121 interrupt shape, applied to new_session), and #2099 closed the
// remaining half of that same defect: the cursor is stamped only by a successful
// route, so pressing New session on a chat before sending anything to it restarted
// whichever chat was last routed. The seams are injected (not raw *Pool/*Registry)
// so the composition test can drive the arms with fakes; production wires
// currentConv: active.CurrentConversation, resolveBound over resolveBoundSession,
// and rotate: pool.RotateForNewSession.
//
// This type is the TRUST BOUNDARY for the client-named id: internal/relay imports
// neither internal/conversations nor internal/sessions, so it hands the string over
// unvalidated and every check lives here.
type activeSessionStarter struct {
	currentConv func() string
	// resolveBound also hands back the conversation's RECORDED workspace — the raw
	// Conversation.Cwd, unvalidated, exactly as ChangeWorkspace stored it (#1475).
	// It is raw on purpose: re-confining it is spawnDirFor's job and must happen at
	// the spawn site, not at the resolve.
	resolveBound func(convID string) (runner sessions.Runner, oldID sessions.SessionID, recordedCwd string, ok bool)
	rotate       func(oldID sessions.SessionID) (sessions.SessionID, error)

	// spawnDirFor re-confines a recorded workspace to $HOME at rotation time,
	// answering the directory the successor must spawn in — production wires
	// resolveSpawnDir, the same validator the mint and revive paths use. ("", nil)
	// means no recorded workspace; an error means refused.
	//
	// Optional: nil leaves every successor in the directory its runner already has,
	// which is pre-#1475 behaviour. That tolerance exists for this struct's shape —
	// a constructor-less bag of injected seams built as a named-field literal, where
	// an omitted field is a reachable state — and matches how logger() treats its
	// own nil, degrading rather than panicking on a remotely-driven path.
	spawnDirFor func(recordedCwd string) (string, error)

	// everRan reports whether the conversation's bound session has ever been
	// activated — the durable answer to "has this conversation ever run?" that lets
	// this type tell a conversation created but never messaged, which must stay
	// inert, from a previously-used session that merely has no child right now,
	// which must be resettable (#2521). Production wires Pool.EverActivated, whose
	// doc carries why the reading is exact and where it is blind.
	//
	// Optional: nil answers false for every conversation, which is pre-#2521
	// behaviour — every dormant reset inert. That is the fail-closed direction and
	// it is what keeps every pre-#2521 literal in this package unchanged.
	//
	// IT MUST BE WIRED WHEREVER reviveBound IS. A literal offering the revive
	// without this gate would materialise a never-used conversation's session on an
	// inbound frame, which is the registry mutation AC-3 forbids. The nil default
	// fails closed in the other direction (nothing revives), so the pairing is a
	// wiring discipline rather than an exploitable state.
	everRan func(oldID sessions.SessionID) bool

	// resolveDormant answers a named conversation's PERSISTED binding when
	// resolveBound could not (#2521). It reads the conversation registry and
	// deliberately does not consult the pool: the state it exists for is the one
	// where the pool's answer is "miss", because after a daemon restart
	// sessions.New materialises only the bootstrap and every per-conversation
	// session is a persisted entry with no *Session behind it (#1487).
	//
	// It repeats resolveBound's unknown/unbound refusal rather than inheriting it
	// by proximity — that is the #678 isolation point, and without it the empty
	// CurrentSessionID would reach a revive of the BOOTSTRAP session.
	//
	// Optional: nil leaves the after-restart case exactly as inert as it is today.
	resolveDormant func(convID string) (oldID sessions.SessionID, recordedCwd string, ok bool)

	// reviveBound re-materialises a dormant session into the pool and answers its
	// runner, so the rotation below can proceed against a session Pool.Lookup was
	// missing (#2521). Production wires resolveSpawnDir + Pool.Revive — the same
	// pair sessionRouter.revive uses, so the reset path re-validates the recorded
	// workspace at the spawn site on the same terms the message route already does,
	// rather than trusting a confinement that was checked before the restart.
	//
	// It does NOT spawn claude: Pool.Revive registers the session evicted and the
	// child comes up on the first message's Activate, which is precisely the
	// identity the rotation below is about to install.
	//
	// Optional: nil leaves the after-restart case inert.
	reviveBound func(convID string, oldID sessions.SessionID, recordedCwd string) (sessions.Runner, error)

	// reset runs the outgoing session's wrap-up turn and writes its reply as the
	// conversation's handoff note before the rotation (#2477). Optional: nil keeps
	// the synchronous pre-#2477 rotation, which is the PTY posture and the shape
	// every test literal in this package still gets for free.
	reset *conversationReset

	// resetting reports the reset's two phases and its falling edge to interactive
	// clients (#2478). Optional on the same terms as reset and every other field in
	// this literal: a nil emitter emits nothing, which is the PTY posture and what
	// keeps every pre-#2478 test literal in this package compiling unchanged.
	//
	// It is deliberately SEPARATE from reset rather than a field on it. The
	// coordinator owns one phase of the two and would have to be told when the other
	// began; the tail below is the one place all three edges sit in sequence.
	resetting *resettingEmitterV2

	// log records which arm an inbound new_session took (#2099). Optional: nil
	// falls back to slog.Default() via logger(), mirroring activeInterrupter.
	log *slog.Logger
}

// activeSessionStarter MUST satisfy the LATE seam, not merely the plain one. The
// config field it is assigned to is typed relay.SessionStarter, so only that half
// is compile-checked at the wiring site, and handleNewSession picks the late form
// by a runtime type assertion — meaning a signature that drifted would not fail to
// build, it would silently fall back to the synchronous path and drop #2443's
// reply on every wrap-up. That is precisely how this ticket shipped red once, so
// the assertion is here rather than left to a test to notice (#2477).
var _ relay.LateSessionStarter = activeSessionStarter{}

// resetThenRotate is the reset's tail, run on its own goroutine: the wrap-up turn,
// then the rotation StartNewSession would otherwise have performed inline.
//
// IT IS OFF THE CALLER'S GOROUTINE BECAUSE THE CALLER IS relay's Run LOOP.
// handleNewSession calls StartNewSession inline on the V2SessionManager's single
// Run dispatch goroutine, and a wrap-up is bounded at ninety seconds; blocking
// there would freeze frame dispatch for every connection and every conversation
// the daemon hosts while one background chat wrote a paragraph.
//
// It terminates unconditionally: the wrap-up is bounded by its own deadline and by
// the daemon context, and the rotation below is two map operations and a restart.
//
// THE OUTCOME IS REPORTED WHEN IT BECOMES TRUE, which is the whole reason this
// tail may run at all. #2443's reply is not a status line but a claim with a
// tense: RotatedWithoutWorkspaceError's own doc fixes the pool as re-keyed and the
// session_transition as already broadcast BY THE TIME THE VALUE EXISTS, and then
// forbids it outright for a rotation that failed — "a lie the client cannot
// check". At dispatch time, ninety seconds above this line, that precondition is
// not merely unproven but routinely false: startFreshRunner's own doc calls losing
// the race to a concurrent new_session "the ORDINARY way to land here", and the
// arm below logs it. So the value is minted HERE, under the rotation it describes,
// and handed to outcome — relay.LateSessionStarter's callback, which carries it to
// the Run goroutine where the reply is sealed. A rotation that failed reports the
// plain error and makes no workspace claim, exactly as the synchronous path does.
//
// outcome MAY BE NIL, and that is a reachable state rather than a defensive check:
// a caller holding only the plain SessionStarter method has nowhere to put a late
// answer. The rotation still happens and both outcomes are still recorded; only
// the client reply is absent. The records are written on BOTH paths, not as a
// fallback — they carry conversation_id, which handleNewSession deliberately never
// logs, so they are the daemon-side half of a pair rather than a substitute.
//
// The runner and oldID are the ones resolved at DISPATCH time, which is a wider
// window than the synchronous path's. That is handled where it already was: if a
// rotation won in between, Pool.RotateForNewSession answers ErrSessionNotFound and
// startFreshRunner disarms the #1330 gate and returns it.
//
// IT IS ALSO THE ONE PLACE THE THREE resetting EDGES SIT IN SEQUENCE (#2478), which
// is why they are emitted here rather than inside the coordinator: wrapUp owns one
// phase of the two and knows nothing of the rotation that is the other.
//
// THE FALLING EDGE IS DEFERRED, AND ITS POSITION RELATIVE TO release IS
// LOAD-BEARING. Registered below `defer release()`, LIFO runs it FIRST — so the
// conversation is still claimed when active:false goes out. Reversed, release would
// admit a second reset whose own wrapping_up could reach a client AHEAD of this
// one's falling edge, and a client that cleared its indicator on the late arrival
// would strand the second reset's — the one ordering error the "every rising
// sequence ends in a falling edge" guarantee cannot recover from. Deferring also
// puts it on all three exits at once, which is the argument reportNewSessionOutcome
// records for itself: the fourth edit would forget one.
func (a activeSessionStarter) resetThenRotate(release func(), outcome func(error),
	runner sessions.Runner, oldID sessions.SessionID, convID, spawnDir string, refused bool) {
	defer release()
	a.resetting.wrappingUp(convID)
	// The bool is an OUTCOME, not an error: a wrap-up that produced no note does not
	// fail the reset, and nothing below branches on it. It exists so the phase change
	// can say whether the successor starts with a note.
	wroteNote := a.reset.wrapUp(convID)
	a.resetting.restarting(convID, wroteNote)
	defer a.resetting.done(convID)
	if err := startFreshRunner(runner, oldID, spawnDir, a.rotate, a.log); err != nil {
		a.logger().Warn("relay: v2 new_session could not rotate after the wrap-up turn",
			"event", "v2.new_session.rotate_failed",
			"conversation_id", convID,
			"err", err)
		reportNewSessionOutcome(outcome, err)
		return
	}
	if refused {
		// Reached only with the rotation returned nil, which is the tense the reply
		// claims. No path: resolveSpawnDir's confinement error is not carried here,
		// matching the posture that function keeps for its own record.
		a.logger().Warn("relay: v2 new_session rotated without the conversation's recorded workspace",
			"event", "v2.new_session.workspace_refused",
			"conversation_id", convID)
		reportNewSessionOutcome(outcome, &relay.RotatedWithoutWorkspaceError{ConversationID: convID})
		return
	}
	reportNewSessionOutcome(outcome, nil)
}

// reportNewSessionOutcome delivers one new_session outcome to a
// relay.LateSessionStarter callback, tolerating the nil callback a plain
// SessionStarter caller leaves behind (#2477). A free function rather than a
// method because it reads nothing from the starter, and one place rather than a
// nil check at each of resetThenRotate's three exits, where the fourth edit would
// forget it.
func reportNewSessionOutcome(outcome func(error), err error) {
	if outcome == nil {
		return
	}
	outcome(err)
}

// logger returns a's logger, falling back to slog.Default() when unset.
// activeSessionStarter is a constructor-less bag of injected seams built as a
// named-field literal, so an omitted field is a reachable state — and a nil
// *slog.Logger panics on first use, which on this remotely-driven relay path
// would be a latent crash on a rarely-hit inert arm. Falling back to the default
// logger (not a discard handler) keeps an unwired literal's record visible.
func (a activeSessionStarter) logger() *slog.Logger {
	if a.log == nil {
		return slog.Default()
	}
	return a.log
}

// StartNewSession starts a fresh session in conversationID's bound runner, or in
// the active conversation's when conversationID is empty.
//
// THE ORDER OF THE FIRST TWO BRANCHES IS THE DESIGN. The empty-string check comes
// BEFORE the shape check because conversations.ValidID returns false for the empty
// string (its own doc says so): reversed, every un-upgraded client's bare frame —
// the shape new_session had from #831 until #2099 — would fail validation and go
// inert, silently breaking the compatibility this ticket promises.
//
// After that, order mirrors activeInterrupter.SendEsc and every ambiguous state is
// inert (nil, no rotation, no respawn, no reply, and never a fall-through to the
// bootstrap session): no conversation to act on → inert; a named id that is not a
// canonical conversation id → inert without ever reaching the registry;
// unbound/dangling binding → inert, resolveBound's CurrentSessionID == "" guard
// being the #678 isolation enforcement; a runner that cannot restart → inert; a
// NAMED conversation with no live child → inert. Only a rotate error propagates,
// for handleNewSession to Warn-log and tolerate (best-effort contract).
//
// THAT LAST ARM IS NAMED-ONLY, and the asymmetry is deliberate rather than an
// oversight. AC-4 requires a named conversation with no live child to be inert, and
// AC-3 requires the bare frame to behave EXACTLY as before #2099 — where an evicted
// cursor conversation rotates and comes back up under the fresh id. Applying the
// liveness guard to both would satisfy one at the cost of the other, so it is
// applied to the path this ticket introduces and withheld from the path it promises
// not to disturb.
//
// Every arm records the id it refused, at DEBUG because the id is client-supplied —
// the posture handleDequeueMessage already takes for its own client-named
// conversation_id, and deliberately NOT activeInterrupter's Info, which records a
// daemon-authored arm identity rather than a remote string. The two registry
// refusals share ONE record: resolveBoundSession refuses the unknown and the
// unbound identically, so this caller structurally cannot distinguish them, which
// is also what keeps the frame from answering "does this conversation exist?".
//
// The cursor is never WRITTEN here; only sessionRouter.Route stamps it. Rotating a
// named conversation therefore leaves the active conversation where it was.
//
// It is relay.SessionStarter's method and answers only what it can prove: the
// wrap-up arm's rotation outlives this call, so this form returns nil there. The
// answer is not lost — StartNewSessionLate is the form that receives it (#2477).
func (a activeSessionStarter) StartNewSession(conversationID string) error {
	err, _ := a.start(conversationID, nil)
	return err
}

// StartNewSessionLate is relay.LateSessionStarter's method: the same arms, with
// the wrap-up arm's outcome delivered through outcome once its rotation has
// actually happened (#2477). handleNewSession asserts this method on the seam and
// prefers it, so in the daemon it is the form that runs.
//
// outcome IS CALLED EXACTLY ONCE on every arm. Synchronously here for every arm
// that resolves inline — including the inert ones, which report nil and elicit no
// reply — and from resetThenRotate's goroutine for the one that defers. The split
// is `deferred`, answered by start itself, so neither this function nor a future
// arm has to infer "did that one defer?" from the error being nil.
func (a activeSessionStarter) StartNewSessionLate(conversationID string, outcome func(error)) {
	if err, deferred := a.start(conversationID, outcome); !deferred {
		reportNewSessionOutcome(outcome, err)
	}
}

// start holds every arm of both seam forms, so the two cannot drift (#2477).
//
// It answers (err, deferred): err is what the synchronous forms return, and
// deferred says the outcome has been handed to the wrap-up tail and will arrive
// through outcome instead. The pair is exhaustive — deferred == true always comes
// with a nil err, and a caller that ignores deferred gets exactly the pre-#2477
// synchronous contract, which is what StartNewSession relies on.
//
// outcome is carried rather than consumed: only the wrap-up arm uses it, and a nil
// one there is the PTY / unwired-caller posture, in which the rotation still runs
// asynchronously and only the client reply is absent.
func (a activeSessionStarter) start(conversationID string, outcome func(error)) (err error, deferred bool) {
	convID := conversationID
	named := conversationID != ""
	switch {
	case convID == "":
		// Nothing named: the pre-#2099 path, verbatim. The cursor's own id is
		// daemon-authored and is deliberately NOT shape-checked — doing so would
		// change behaviour on the path this branch exists to preserve.
		convID = a.currentConv()
		if convID == "" {
			// No id to carry — the record's information is its existence: the frame
			// reached here and nothing was active.
			a.logger().Debug("relay: v2 new_session inert; no active conversation",
				"event", "v2.new_session.no_active_conv")
			return nil, false
		}
	case !conversations.ValidID(convID):
		a.logger().Debug("relay: v2 new_session inert; named conversation id is not canonical",
			"event", "v2.new_session.invalid_conv_id",
			"conversation_id", boundedConvID(convID))
		return nil, false
	}

	runner, oldID, recordedCwd, ok := a.resolveBound(convID)
	// used carries the dormant arm's PROOF forward rather than re-deriving it: a
	// revived session reached this line only because everRan already answered true
	// for it, and the liveness guard below would otherwise ask the same question a
	// second time for an answer it cannot have changed.
	used := false
	if !ok {
		// #2521: "the pool has no session for it" is not the same as "there is
		// nothing to reset". After a daemon restart sessions.New materialises only
		// the bootstrap, so a previously-used conversation's session is a persisted
		// entry Pool.Lookup misses — the state the message route already recovers
		// from through sessionRouter.revive and this verb did not. reviveDormantBound
		// writes its own record on every refusal, including the unknown/unbound one
		// this arm used to write here.
		runner, oldID, recordedCwd, ok = a.reviveDormantBound(convID)
		if !ok {
			return nil, false
		}
		used = true
	}
	// The rotation capability is probed HERE as well as inside startFreshRunner,
	// which is deliberate rather than an oversight: this is the arm AC-4 asks to be
	// recorded, and startFreshRunner cannot report it — it returns nil both for "no
	// arm" and for a clean rotation. Its own assertion stays where it is because
	// that assertion is what keeps the #1330 gate arming below the inert return, and
	// because dispatch_arms_test.go and inbound_deliver_rotation_test.go drive it
	// directly. This mirrors activeInterrupter, which records its armNone arm the
	// same way.
	if _, canRestart := runner.(interface{ RestartFresh(string) }); !canRestart {
		a.logger().Debug("relay: v2 new_session inert; bound runner cannot restart",
			"event", "v2.new_session.no_restart",
			"conversation_id", convID)
		return nil, false
	}
	// AC-4's fourth row, and the ONE arm the capability probe above does not already
	// cover — which is exactly how it was first shipped broken. The probe asks what
	// the runner CAN do; production's answer is always yes, because the sole
	// implementation is streamRunner, which exposes RestartFresh unconditionally and
	// is assigned at mint time. A conversation created but never messaged therefore
	// reaches this line with a real runner whose child has never spawned (#2085
	// defers the spawn to the first message), and rotating it would rekey the pool,
	// persist sessions.json, rebind the conversation and broadcast a
	// session_transition telling every client to render a delimiter for a chat that
	// has never had a turn. RestartFresh itself would spawn nothing, so the rotation
	// would be pure observable damage with no session to show for it.
	//
	// State().ChildPID is the liveness signal rather than Phase because it is the one
	// the field's own doc defines that way ("PID of the running child, or 0 when
	// none") — an unstarted, backing-off, evicted or stopped runner all report 0, and
	// all four are states where there is nothing to restart fresh. Racing a spawn
	// that has started but not yet published its pid reads 0 too and refuses; that is
	// the same fail-safe direction the whole reject set takes, and the frame is
	// re-sendable.
	//
	// SINCE #2521 THE ZERO PID IS NO LONGER THE WHOLE ANSWER. It covers an unstarted
	// runner AND one whose child has stopped, backed off or been evicted, and only
	// the first of those has nothing to reset — the rest are the channel an operator
	// comes back to after lunch, whose Reset did nothing at all. everRan splits the
	// two on the durable signal (Pool.EverActivated), so the refusal keeps its
	// original subject — a conversation created but never messaged — and loses the
	// three states it was over-reaching into. The guard is still fail-safe in the
	// same direction: a nil seam, an unknown id, or a spawn racing its own pid all
	// read "never ran" and refuse, and the frame is re-sendable.
	//
	// ONE CONSEQUENCE IS KEPT RATHER THAN CORRECTED: a REPEATED Reset now rotates
	// every time. Pool.rekeyLocked stamps lastActiveAt while createdAt is immutable,
	// so the successor of a rotation reads as previously used and a second frame
	// arriving before any message re-keys again. Refusing it would need a signal
	// these timestamps cannot carry — "has a child ever spawned under the CURRENT
	// id" — because the Session survives the re-key; only the runner's rotatePending
	// latch knows, and exposing it is a new API for a state no AC names. The cost is
	// one extra separator for a session that has had no turn, it is operator-driven
	// rather than client-replayable, and it is arguably what pressing the control
	// twice asks for.
	live := runner.State().ChildPID != 0
	if named && !live && !used && !a.hasEverRun(oldID) {
		a.logger().Debug("relay: v2 new_session inert; named conversation has no live child",
			"event", "v2.new_session.no_live_child",
			"conversation_id", convID)
		return nil, false
	}
	// #2477: a LIVE child is asked to write a handoff note for its successor before
	// it is replaced, and that turn moves the whole tail off this goroutine. See
	// resetThenRotate for why, and conversationReset's file header for the bound.
	//
	// The gate is liveness, not namedness, and the asymmetry above is not repeated
	// here. A bare frame on a childless conversation keeps rotating exactly as #2099
	// promised, because it takes the synchronous path below; a bare frame on a live
	// one gets the same wrap-up a named one gets, because the operator pressed the
	// same control and there is the same session's worth of context to lose.
	if a.reset != nil && live {
		release, armed := a.reset.begin(convID)
		if !armed {
			// AC-4: DROPPED, not queued. A second reset would run another wrap-up turn
			// against a child the first one is about to replace — real tokens spent to
			// write a note over the one being written. Recorded at Debug beside the
			// other client-driven refusals on this verb.
			a.logger().Debug("relay: v2 new_session inert; a reset is already in progress",
				"event", "v2.new_session.reset_in_progress",
				"conversation_id", convID)
			return nil, false
		}
		// Resolved SYNCHRONOUSLY so it stays BELOW every inert arm AND below the guard
		// above, which is what keeps a repeated frame from driving MkdirAll — the
		// containment resolveSpawnDir's own doc requires. The refusal it answers
		// travels WITH the tail rather than being answered from here; resetThenRotate
		// mints #2443's value under the rotation that makes it true.
		spawnDir, refused := a.resolveSpawnDir(convID, recordedCwd)
		go a.resetThenRotate(release, outcome, runner, oldID, convID, spawnDir, refused)
		// DEFERRED, AND THE ONLY ARM THAT IS: this frame owes the client a reply it
		// cannot yet make true. The only reply this verb has is #2443's, and that value
		// asserts a COMPLETED rotation — one that is ninety seconds away here and may
		// not happen at all. Answering now would be the "lie the client cannot check"
		// its own doc forbids, so the answer travels with the tail and arrives when it
		// is true. A caller with no late channel (outcome == nil) simply does not get
		// it; the rotation is unaffected.
		return nil, true
	}
	spawnDir, refused := a.resolveSpawnDir(convID, recordedCwd)
	if err := startFreshRunner(runner, oldID, spawnDir, a.rotate, a.log); err != nil {
		// The rotation did not happen, so the refusal is not reportable: "rotated
		// without the workspace" would describe a rotation the client's own
		// session_transition never announced. The rotate error is the pre-#2443
		// answer and stays the answer; resolveSpawnDir's record still stands.
		return err, false
	}
	if refused {
		// The rotation COMPLETED and only the move did not (#2443). Not a failure —
		// the type's own doc block says so — and the only outcome handleNewSession
		// answers the client about. convID is resolved: the frame's id after
		// conversations.ValidID, or the cursor's own, so it is bounded and canonical
		// on both paths and is never the raw client string.
		return &relay.RotatedWithoutWorkspaceError{ConversationID: convID}, false
	}
	return nil, false
}

// resolveSpawnDir re-confines the conversation's recorded workspace to $HOME and
// answers the directory the successor must spawn in, or "" to leave the runner
// where it is (#1475).
//
// IT RE-VALIDATES RATHER THAN TRUSTING, taking sessionRouter.revive's posture and
// explicitly not sessionMinter.Create's. Create may defer without re-validating
// because resolveSpawnDir's realpath is frozen onto the session at build time, so
// no phone-influenced state is re-read between the check and the chdir. Nothing is
// frozen here: recordedCwd is raw persisted bytes in a mutable file, written by
// change_workspace at an arbitrary earlier moment and possibly across a daemon
// restart, which is revive's situation exactly — "a path valid then can be turned
// into an escape before the restart, and this is the spawn site that would
// otherwise believe the stale check". So the validator runs again, here, on every
// rotation.
//
// FAIL-CLOSED MEANS KEEPING THE OLD DIRECTORY, never spawning in an unconfined
// one: both an empty recording and a refusal answer "", and the rotation still
// completes with the child still coming up where it was. The two differ in the
// SECOND return and in whether a record is written — an unset workspace is not a
// refusal.
//
// THE BOOL IS THE SAME CONDITION THE RECORD REPORTS, deliberately so the wire and
// the log cannot disagree: refused is true on exactly the arm that writes
// spawn_dir_rejected, and on no other. It carries no text at all, which is what
// keeps the confinement error's path out of everything downstream — the caller
// turns it into a relay.RotatedWithoutWorkspaceError naming the conversation and
// nothing else (#2443). installSpawnDir's later failure is a DIFFERENT condition
// with its own record and is deliberately not reported here: by then the
// directory was accepted and the rotation is already committed.
//
// IT IS CALLED BELOW EVERY INERT ARM, which is load-bearing rather than tidy:
// resolveSpawnDir creates a directory and writes ~/.claude.json, so hoisting it
// would let a frame naming a conversation with no live child drive MkdirAll on the
// daemon's behalf. It also blocks in syscalls on V2SessionManager's single Run
// dispatch goroutine — the honest consequence handlers.createConversationMintTimeout
// already records for the neighbouring mint path, and deliberately NOT defended
// with a deadline, which cannot interrupt a syscall and would claim a protection it
// does not provide. Only a frame that will actually rotate pays it.
//
// SECURITY: the wrapped error names the resolved path and the $HOME boundary and
// MUST NOT be logged. Pool.Revive's contract is that a phone-influenced workspace
// path must not reach a log, and sessionTranscriptDir's SECURITY paragraph closes
// the same channel (#833). The record carries the event and the conversation id —
// which is already logged unbounded on the resolvable arms above — and nothing
// else. At Warn rather than the arms' Debug: this one is about the daemon's own
// stored state failing its own validator, not about a string a client just sent.
func (a activeSessionStarter) resolveSpawnDir(convID, recordedCwd string) (dir string, refused bool) {
	if a.spawnDirFor == nil || recordedCwd == "" {
		return "", false
	}
	dir, err := a.spawnDirFor(recordedCwd)
	if err != nil {
		a.logger().Warn("relay: v2 new_session rejected the recorded workspace; keeping the current one",
			"event", "v2.new_session.spawn_dir_rejected",
			"conversation_id", convID)
		return "", true
	}
	return dir, false
}

// maxLoggedConvID bounds how much of a REFUSED conversation id reaches a log
// record. Only the invalid-shape arm needs it: every other arm logs a string that
// has already passed conversations.ValidID, so it is provably 36 bytes. That arm is
// the one place an arbitrary client-chosen string reaches a log call, and it is
// bounded upstream only by the application-envelope cap — kilobytes per frame, and
// a paired client may send frames freely. 64 is generously above a canonical id's
// 36 so a near-miss (a stray character, a wrong-cased id) still prints whole and
// stays diagnosable.
const maxLoggedConvID = 64

// boundedConvID renders an untrusted conversation id for a log record: unchanged
// when it is already short, and otherwise a COPIED prefix carrying an elision
// marker. The copy is load-bearing — a bare s[:n] would share the decoded frame's
// backing array, so a buffered log record would pin the whole payload allocation.
// The marker matters too: without it a truncated id reads as a complete one, and a
// reader would chase a conversation that was never named.
func boundedConvID(s string) string {
	if len(s) <= maxLoggedConvID {
		return s
	}
	return strings.Clone(s[:maxLoggedConvID]) + "…(truncated)"
}

// inboundActivateTimeout caps the drain's per-attempt wait for an idle-evicted
// session to respawn its supervisor and bind its PTY. It matches the CLI attach
// budget (#396): a wedged respawn surfaces as a delivery error so the drain
// retries the FIFO head rather than blocking forever inside Activate. Unlike the
// removed #594 deliver timeout, it does NOT bound the WriteUserTurn that follows
// — that block is the drain's turn-end pacing and may run for a whole claude
// turn. That remains exactly right on the PTY path, where the pacing block lives
// INSIDE WriteUserTurn (supervisor's idle gate). On the stream path the pacing
// block sits in FRONT of WriteUserTurn instead — the write there returns as soon
// as the envelope is in the child's stdin pipe — and it is bounded, by
// streamTurnHoldTimeout (#1199). A tuning knob, not a contract.
const inboundActivateTimeout = 30 * time.Second

// streamTurnHoldTimeout bounds ONE delivery attempt's wait for the conversation's
// running turn to end on the stream-json path (#1199). Unused when the turn-busy
// tracker is nil (PTY mode), where the pacing block is inside WriteUserTurn and
// unbounded, as inboundActivateTimeout's doc above records.
//
// The arithmetic, against msgqueue's drain: a turn that never ends fails attempt 1
// after this window, which starts the give-up streak (elapsed ≈ 0 <
// defaultGiveUpAfter, 2m), sleeps defaultRetryInterval (1s) and retries; attempt 2
// fails with elapsed ≈ this window ≥ the bound, so the drain gives up, fires
// OnGiveUp, and the head surfaces as a typed session_error / CodeSessionBlocked
// (#1000/#1008) instead of holding the conversation forever. Worst case ≈ 2× this
// value, so a client-visible bound of about half an hour.
//
// The trade-off the value encodes: give-up ABANDONS the head, so any finite bound
// trades "a wedged turn is reported late" against "a message queued behind a
// genuinely long agentic turn is thrown away". 15 minutes sits above any
// interactive turn observed to date while keeping the client-visible bound inside
// the half hour.
//
// There IS now a Pending analogue, and it is GATED (#1911): approvalHoldPending
// exempts an attempt from the give-up bound only when this hold timed out while an
// approval was parked on a person for that conversation. What it exempts is the
// PERSON'S deciding time, not the turn. The unconditional exemption this doc used
// to refuse resets the give-up streak forever, which a human decision may
// legitimately need and a running turn must not — that is what would make the bound
// unsatisfiable; per-conversation gating is what keeps it satisfiable, and the
// arithmetic above stands unchanged for a turn making no progress with nobody being
// asked. For THAT case the better discriminator is still staleness (no turn event
// for N minutes) rather than duration, but that needs a per-conversation timestamp
// the tracker deliberately does not hold (#1201); it is a separate ticket if
// production ever surfaces a session_error for a turn that was legitimately
// progressing. A tuning knob, not a contract.
const streamTurnHoldTimeout = 15 * time.Minute

// mcpApprovalTimeout is the DEFAULT human-approval window handed to the
// pending-approval registry (permbridge.Register) for every VerbMCPApprove
// request. Since #1932 wired the daemon's liveness report into that registry —
// which startRelayV2 does whenever a relay is configured, and startRelay skips
// along with the whole leg when one is not — the window is a re-check interval
// rather than a hard deadline: the registry's own
// timer asks the report at every expiry and re-arms this SAME window while the
// approval is still answerable, denying the request and deleting the entry only
// once the answer comes back no. The bound is therefore one window from the daemon
// OBSERVING the last answerer go away, which is not the moment it went — a vanished
// phone stays in the daemon's active set until internal/relay's idle sweep notices
// it, and docs/knowledge/features/permbridge-package.md carries what that costs.
// It is a default, not the value — envApprovalTimeout overrides it, and
// approvalTimeout is the accessor every consumer actually calls.
//
// Why ten and not two: waiting is not the unsafe state. The tool does not run
// while the approval is outstanding, so denying early prevents nothing that
// remaining pending was not already preventing. The number is about how long a
// person may reasonably take to reach a phone, not about safety.
//
// Why ten and not more: this constant no longer answers that. It used to be held
// deliberately clear of streamTurnHoldTimeout, which bounds the delivery hold,
// because a give-up there ABANDONED whatever message was queued behind the waiting
// turn — so raising this window past that hold traded a prompt that gives up too
// early for a message that silently disappears. #1911 removed the abandonment:
// approvalHoldPending exempts a hold from the give-up bound for exactly as long as
// an approval is parked on a person, so a queued message now waits out the decision
// instead of vanishing. Ten stays because it is what a person reaching a phone
// plausibly needs, not because the hold pins it. A tuning knob, not a contract.
const mcpApprovalTimeout = 10 * time.Minute

// envApprovalTimeout overrides the human-approval window (mcpApprovalTimeout). A
// plausibly-operational knob for tuning the window — the e2e (#1139, rebuilt under
// #1932) shrinks it to ~2s so both halves of the conditional bound are cheap to
// exercise. One arm keeps the answering phone connected and answers past more than
// one window, and nothing may deny in the meantime; the other ENDS that phone's
// session first, so nobody is left able to answer, and only then does the window
// deny. What the suite pins is a deny once nobody can answer it, and explicitly no
// deny while somebody still can.
const envApprovalTimeout = "PYRY_APPROVAL_TIMEOUT"

// approvalTimeout is the approval window handed to the pending-approval registry:
// mcpApprovalTimeout by default, overridable via PYRY_APPROVAL_TIMEOUT. An unset or
// unparseable value falls back to the default, so production behaviour is
// byte-identical when the env is absent. The DURATION stays the only knob, and it
// is the duration every arming uses, not just the first: permbridge.Register
// re-arms the value it was handed and never a different one. What that value feeds
// is the re-check interval the fail-closed core now applies while somebody can
// still answer — not a deadline it applies unconditionally.
func approvalTimeout() time.Duration {
	if v := os.Getenv(envApprovalTimeout); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return mcpApprovalTimeout
}

// approvalParkedReport is the late-bound holder for #1919's ApprovalParked report,
// carrying it from the relay wiring back to the delivery seam (#1911). The
// composition root builds msgqueue.New — and therefore the seam — well before
// startRelayV2 constructs the bridge that answers the report, and the relayWiring
// literal is built after the queue, so the report cannot be handed to the queue at
// construction. This is the same shape, for the same reason, as
// streamApprovalBridge.toolCallInFlight: one holder, set at one wiring site.
//
// ask is written exactly once, at wiring time, and read from each conversation's
// drain goroutine. It carries no mutex because goroutine creation supplies the
// happens-before edge on every link of the chain: set runs inside startRelayV2
// before the v2 manager's Run goroutine starts, that goroutine spawns the
// per-connection goroutines, the send_message handler on one of them calls
// msgqueue.Enqueue, and Enqueue spawns the drain that reads it. The queue's own Run
// goroutine cannot beat that: it holds no conversation until an Enqueue lands.
type approvalParkedReport struct {
	// ask is streamApprovalBridge.ApprovalParked; nil until the relay wiring sets it.
	ask func(conversationID string) bool
}

// set publishes the report. A nil receiver is a no-op, matching the nil-safety
// idiom of the seam this feeds (waitIdleForDelivery, openForDelivery).
func (r *approvalParkedReport) set(ask func(conversationID string) bool) {
	if r == nil {
		return
	}
	r.ask = ask
}

// parked reports whether a person is currently being asked about conversationID.
// A nil receiver or an unset ask answers false for every conversation — which is
// the behaviour before this exemption existed, and the right answer in PTY mode,
// where ApprovalParked is negative anyway because no tracker is wired.
func (r *approvalParkedReport) parked(conversationID string) bool {
	if r == nil || r.ask == nil {
		return false
	}
	return r.ask(conversationID)
}

// errStreamTurnHold marks a delivery attempt that ended in the stream-path
// mid-turn hold, having written nothing. newInboundDeliver's hold branch is its
// only producer and markApprovalHolds its only consumer. Its text keeps the
// rendered hold error byte-identical to the pre-#1911 wrap.
var errStreamTurnHold = errors.New("stream turn hold")

// errHeldForApproval marks a hold that happened while a person was being asked
// about the conversation (#1911). markApprovalHolds is its only producer and
// approvalHoldPending its only consumer. Like errStreamTurnHold it is a fixed
// daemon-authored string with no interpolation, so a wrapped hold error carries
// neither queued text nor approval content by construction.
var errHeldForApproval = errors.New("delivery held for a parked approval")

// markApprovalHolds decorates the delivery seam so msgqueue's Pending classifier
// can tell a person's deciding time apart from a wedged conversation. It exists
// because msgqueue.PendingFunc is func(error) bool: it classifies the delivery
// error alone and never sees a conversation, so the conversation-scoped question
// has to be answered here, before the error leaves the delivery seam.
//
// The two-step test order is load-bearing, for the reason ApprovalParked's own doc
// gives about its conjunction: the sentinel test is a local comparison while parked
// crosses two leaf locks, so the common case — an ordinary delivery failure with
// nobody being asked — must not pay the second.
//
// PRECISION IS THE POINT. Only a hold error is ever re-marked. A resolve or
// Activate failure is a genuinely wedged conversation and stays on the give-up
// clock even while an approval is parked, which is what keeps that clock
// satisfiable once an approval may outlive the hold. There is deliberately no
// context.Canceled guard here: both cancellation sources — a removed head and
// daemon shutdown — are decided by branches upstream of Pending in msgqueue's
// drain, so a guard would be dead code defending a failure mode nobody has seen.
func (r *approvalParkedReport) markApprovalHolds(deliver msgqueue.DeliverFunc) msgqueue.DeliverFunc {
	return func(ctx context.Context, convID string, payload []byte) error {
		err := deliver(ctx, convID, payload)
		if err == nil || !errors.Is(err, errStreamTurnHold) {
			return err
		}
		if !r.parked(convID) {
			return err
		}
		return fmt.Errorf("%w: %w", errHeldForApproval, err)
	}
}

// approvalHoldPending is the msgqueue.PendingFunc the composition root wires: it
// classifies a delivery held behind an approval parked on a person as a legitimate
// hold rather than a failure, so the give-up streak resets instead of counting.
//
// A free function rather than a method on the report: PendingFunc must be pure and
// non-blocking (msgqueue calls it on the drain path), and everything that could
// block has already run on the delivery path, which blocks for whole turns anyway.
func approvalHoldPending(err error) bool { return errors.Is(err, errHeldForApproval) }

// newInboundDeliver builds the msgqueue.DeliverFunc seam over the stamp-free
// resolve core. The engine (#704) calls it on a per-conversation drain
// goroutine, one delivery at a time. It:
//
//   - re-resolves the bound session per attempt (the binding may change between
//     enqueue and drain), returning any resolve error so the drain retries the
//     head (a conversation that becomes transiently unbound post-ack is held,
//     not dropped);
//   - Activates under a bounded budget so a wedged respawn becomes a retryable
//     error instead of a permanent block;
//   - HOLDS the delivery while the conversation's turn is running on the
//     stream-json path (#1199), then marks the conversation mid-turn, both
//     between Activate and the write — see the placement note below. A hold that
//     ends in an error is the sole producer of errStreamTurnHold, which is what
//     lets markApprovalHolds exempt a person's deciding time from the give-up
//     bound (#1911) without exempting a wedged conversation with it;
//   - writes the turn with the RAW lifecycle ctx — no deliver timeout, because
//     that blocking IS the drain's turn-end pacing (DeliverFunc must return nil
//     only on a confirmed commit, `defaultRetryInterval`).
//
// PLACEMENT of the hold, all three constraints load-bearing. It sits BEFORE
// WriteUserTurn, therefore before the turncommit claim streamsup.WriteTurn makes
// inside it: that is what keeps the queued head draining && !committing for the
// whole wait, which is the only window in which msgqueue.Remove drops it — so the
// drop-before-drain control is delivered by placement, not by new code (commitGate
// itself documents the seam as calling it "after the idle-gate wait and before the
// write"). It sits AFTER Activate, which is already bounded and idempotent, so a
// wedged respawn still surfaces as a prompt retryable error rather than being
// masked behind a long hold. And the MARK precedes the write rather than following
// it, because the tracker's ordinary opener feed is asynchronous and a fast child's
// TurnEnd could otherwise clear before the mark landed; openForDelivery's doc
// carries that argument, and returns the undo this body runs on a write error.
//
// A nil tracker (PTY mode) makes both calls no-ops, leaving this body semantically
// identical to the pre-#1199 sequence.
//
// It is built over router.resolve, NOT router.Route, so it never stamps the
// active-conversation cursor: the cursor stays single-writer (the routing-path
// goroutine via Route), preserving the #679/#687 follow-active invariant against
// a drain-time re-stamp. Taking resolve as a func value (not the struct) keeps
// the seam unit-testable with a fake resolve; busy and hold are taken the same way
// so the hold is exercisable with a short bound.
func newInboundDeliver(resolve func(string) (handlers.TurnWriter, error), busy *turnBusyTracker, hold time.Duration) msgqueue.DeliverFunc {
	return func(ctx context.Context, convID string, payload []byte) error {
		w, err := resolve(convID)
		if err != nil {
			return err
		}
		activateCtx, cancelActivate := context.WithTimeout(ctx, inboundActivateTimeout)
		err = w.Activate(activateCtx)
		cancelActivate()
		if err != nil {
			return err
		}
		// Wrapped, so a hold failure is legible in msgqueue's retry Warn; the wrap
		// keeps errors.Is(err, context.DeadlineExceeded) and context.Canceled true for
		// the drain's own classification. Nothing has been written at this point.
		// errStreamTurnHold rides the same wrap and this statement is its only
		// producer, so markApprovalHolds can tell a hold apart from every other
		// delivery failure without widening this seam; the rendered text is unchanged.
		if err := busy.waitIdleForDelivery(ctx, convID, hold); err != nil {
			return fmt.Errorf("%w: %w", errStreamTurnHold, err)
		}
		undo := busy.openForDelivery(convID)
		if err := w.WriteUserTurn(ctx, convID, payload); err != nil {
			// Returned VERBATIM (unwrapped): msgqueue classifies ErrNoLiveSession,
			// ErrTrustModalPending and turncommit.ErrDropped by errors.Is, and the undo
			// leaves the tracker exactly as it was before this attempt.
			undo()
			return err
		}
		return nil
	}
}

// activeConversation holds the id of the conversation the operator is currently
// interacting with — the one most recently resolved by sessionRouter.Route. It
// is the structured turn stream's cursor source (#687): the emitter
// (`flushC`) and the #647 reconnect-replay source
// (`startInteractiveTurnStreamV2`) read it instead of the bootstrap
// supervisor's CurrentConversation(), which #678 emptied for routed turns —
// those commit on bound-session supervisors and never touch the bootstrap cursor
// (docs/knowledge/codebase/678.md). Before any route the zero value is "", the
// well-defined "no conversation routed yet" state the emitter drops on.
//
// It is written on the routing-path goroutine (set, from Route) and read on the
// producer's single Run goroutine (CurrentConversation — live emit + replay;
// watch — follow-active subscription). The mutex makes that cross-goroutine
// hand-off race-free; it is a leaf lock, never held across a call-out. This is
// the one piece of new synchronisation — it absorbs the hand-off so the
// emitter's other counters stay unguarded-single-goroutine
// (`interactiveTurnEmitterV2`). Mirrors the supervisor's own
// convMu+currentConvID cursor.
//
// changed is the follow-active switch signal (#679): it is closed-and-replaced
// whenever set records a DIFFERENT id, so a watcher snapshotted via watch sees
// its captured channel close and re-subscribes onto the now-active bound
// session. Consecutive messages to the same conversation do NOT fire it (the
// tail stays open and catches each turn continuously, as the bootstrap did).
// It is lazy-initialised under mu so the zero-value &activeConversation{}
// literal (main.go + session_router_test.go) stays valid with no constructor.
type activeConversation struct {
	mu      sync.Mutex
	id      string
	changed chan struct{}
}

// set stamps id as the current conversation. Called from sessionRouter.Route on
// the successful-route path only. When id differs from the current value it
// fires the change signal (close + replace changed) so a follow-active watcher
// re-subscribes; the same id is a no-op on the signal.
func (a *activeConversation) set(id string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.changed == nil {
		a.changed = make(chan struct{})
	}
	if id != a.id {
		a.id = id
		close(a.changed)
		a.changed = make(chan struct{})
	}
}

// CurrentConversation returns the stamped conversation id, "" before any route.
// It satisfies the cursorReader interface (interactive_turn_v2.go) verbatim, and
// its method value satisfies SetReplaySource's func() string.
func (a *activeConversation) CurrentConversation() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.id
}

// parseClientFlags handles the shared flags every control verb accepts:
// -pyry-name (instance name → ~/.pyry/<name>.sock) and -pyry-socket (explicit
// path that overrides the name). Returns the resolved socket path and any
// positionals after the recognised flags. Verbs that don't take positionals
// can bind rest to _ — same silent-ignore behaviour as before.
func parseClientFlags(name string, args []string) (socketPath string, rest []string, err error) {
	pyryArgs, rest := splitClientFlags(args)
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	nameFlag := fs.String("pyry-name", defaultName(), "instance name (socket: ~/.pyry/<name>.sock)")
	socketFlag := fs.String("pyry-socket", "", "explicit socket path (overrides -pyry-name)")
	if err := fs.Parse(pyryArgs); err != nil {
		return "", nil, err
	}
	return resolveSocketPath(*socketFlag, *nameFlag), rest, nil
}

// runStatus implements the `pyry status` subcommand: dial the control socket,
// fetch a status snapshot, pretty-print it.
func runStatus(args []string) error {
	socketPath, rest, err := parseClientFlags("pyry status", args)
	if err != nil {
		return err
	}
	if len(rest) > 0 {
		return fmt.Errorf("status: unexpected arguments: %s", strings.Join(rest, " "))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	resp, err := control.Status(ctx, socketPath)
	if err != nil {
		return fmt.Errorf("status: %w", err)
	}

	fmt.Printf("Phase:         %s\n", resp.Phase)
	if resp.ChildPID > 0 {
		fmt.Printf("Child PID:     %d\n", resp.ChildPID)
	}
	fmt.Printf("Restart count: %d\n", resp.RestartCount)
	if resp.LastUptime != "" {
		fmt.Printf("Last uptime:   %s\n", resp.LastUptime)
	}
	if resp.NextBackoff != "" {
		fmt.Printf("Next backoff:  %s\n", resp.NextBackoff)
	}
	fmt.Printf("Started at:    %s\n", resp.StartedAt)
	fmt.Printf("Uptime:        %s\n", resp.Uptime)
	return nil
}

// runLogs implements `pyry logs`: fetch the recent supervisor log lines from
// the daemon's in-memory ring buffer and print them.
func runLogs(args []string) error {
	socketPath, rest, err := parseClientFlags("pyry logs", args)
	if err != nil {
		return err
	}
	if len(rest) > 0 {
		return fmt.Errorf("logs: unexpected arguments: %s", strings.Join(rest, " "))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	resp, err := control.Logs(ctx, socketPath)
	if err != nil {
		return fmt.Errorf("logs: %w", err)
	}
	for _, line := range resp.Lines {
		fmt.Println(line)
	}
	return nil
}

// sessionsVerbList is the displayed verb list in `pyry sessions` usage
// errors. Update in lockstep with the switch in runSessions — Phase
// 1.1b/c/d/e each append one verb here in the same edit that adds the
// case.
const sessionsVerbList = "new, rm, rename, list"

// errSessionsUsage formats a help-style error listing the implemented
// `pyry sessions` verbs. Mapped to a non-zero exit by main's top-level
// error printer.
func errSessionsUsage(detail string) error {
	return fmt.Errorf("sessions: %s\nverbs: %s", detail, sessionsVerbList)
}

// runSessions implements `pyry sessions <verb>`: peel the global pyry
// flags via parseClientFlags, then dispatch on the first positional.
//
// Convention (matches the top-level CLI: "pyry flags must come before
// claude args"): -pyry-socket / -pyry-name must precede the sub-verb.
// Sub-verb flags (e.g. --name on `new`) come after.
//
// New verbs in this family (1.1b list, 1.1c rename, 1.1d rm, 1.1e
// attach refactor) each add one switch case + one runSessions<Verb>
// helper.
func runSessions(args []string) error {
	socketPath, rest, err := parseClientFlags("pyry sessions", args)
	if err != nil {
		return err
	}
	if len(rest) == 0 {
		return errSessionsUsage("missing subcommand")
	}
	sub, subArgs := rest[0], rest[1:]
	switch sub {
	case "new":
		return runSessionsNew(socketPath, subArgs)
	case "rm":
		return runSessionsRm(socketPath, subArgs)
	case "rename":
		return runSessionsRename(socketPath, subArgs)
	case "list":
		return runSessionsList(socketPath, subArgs)
	default:
		return errSessionsUsage(fmt.Sprintf("unknown verb %q", sub))
	}
}

// parseSessionsNewArgs is the flag-parse + arity check for
// `pyry sessions new [--name LABEL]`. Extracted from runSessionsNew so
// the parsing rules can be unit-tested without dialling the control
// socket. Mirrors attachSelectorFromArgs's split.
func parseSessionsNewArgs(args []string) (label string, err error) {
	fs := flag.NewFlagSet("pyry sessions new", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	labelFlag := fs.String("name", "", "human-friendly label for the new session")
	if err := fs.Parse(args); err != nil {
		return "", err
	}
	if fs.NArg() > 0 {
		return "", fmt.Errorf("unexpected positional %q", fs.Arg(0))
	}
	return *labelFlag, nil
}

// runSessionsNew implements `pyry sessions new [--name LABEL]`: dial
// the daemon's control socket, ask it to mint a session, print the
// UUID. Empty label maps to a no-label session per AC#1.
func runSessionsNew(socketPath string, args []string) error {
	label, err := parseSessionsNewArgs(args)
	if err != nil {
		return fmt.Errorf("sessions new: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	id, err := control.SessionsNew(ctx, socketPath, label)
	if err != nil {
		return fmt.Errorf("sessions new: %w", err)
	}
	fmt.Println(id)
	return nil
}

// errSessionsRmUsage marks every parse-time failure of `pyry sessions rm`
// as a usage error. runSessionsRm matches via errors.Is and exits 2 with
// the wrapped message printed verbatim (no `pyry:` prefix). One sentinel
// covers arity, mutually-exclusive flags, and any other handler-side
// usage rule — the wire-call path is reached only on parse-success, so
// runSessionsRm doesn't need to discriminate further.
var errSessionsRmUsage = errors.New("usage")

// errAmbiguousPrefix carries the formatted multi-line "ambiguous prefix"
// message produced by resolveSessionIDViaList. The unexported sentinel
// exists so runSessionsRm can branch with errors.Is rather than
// string-matching the message text. Mirrors sessions.ErrAmbiguousSessionID
// in spirit — Pool.ResolveID's server-side equivalent — but lives at the
// CLI layer because prefix resolution here is client-side via
// control.SessionsList.
var errAmbiguousPrefix = errors.New("ambiguous session id prefix")

// parseSessionsRmArgs parses `[--archive|--purge] <id>`. Returns
// (id, policy, err); policy is the wire enum (control.JSONLPolicy) —
// empty when neither --archive nor --purge was set, which the server
// treats as JSONLPolicyLeave.
//
// Mirrors parseSessionsNewArgs's shape: extracted from runSessionsRm
// so flag-parsing rules are unit-testable without dialling the
// control socket. Every error returned wraps errSessionsRmUsage so
// runSessionsRm can map the whole class to exit 2 with a single
// errors.Is check.
func parseSessionsRmArgs(args []string) (id string, policy control.JSONLPolicy, err error) {
	fs := flag.NewFlagSet("pyry sessions rm", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	archive := fs.Bool("archive", false, "archive the on-disk JSONL transcript")
	purge := fs.Bool("purge", false, "delete the on-disk JSONL transcript (default: leave)")
	if err := fs.Parse(args); err != nil {
		return "", "", fmt.Errorf("%w: %v", errSessionsRmUsage, err)
	}
	if *archive && *purge {
		return "", "", fmt.Errorf("%w: --archive and --purge are mutually exclusive", errSessionsRmUsage)
	}
	if fs.NArg() != 1 {
		return "", "", fmt.Errorf("%w: expected <id>, got %d positional args", errSessionsRmUsage, fs.NArg())
	}
	switch {
	case *archive:
		policy = control.JSONLPolicyArchive
	case *purge:
		policy = control.JSONLPolicyPurge
	default:
		// Empty policy — wire layer normalises to JSONLPolicyLeave.
		policy = ""
	}
	return fs.Arg(0), policy, nil
}

// resolveSessionIDViaList resolves a user-supplied UUID-or-prefix to a
// canonical SessionID by listing every session via the wire and
// filtering client-side. Mirrors Pool.ResolveID's resolution order:
// exact match wins outright; otherwise scan with strings.HasPrefix —
// one match returns its ID; zero returns sessions.ErrSessionNotFound;
// multiple returns errAmbiguousPrefix wrapping a sorted "<uuid> <label>"
// list (one per line, matching AC#3's space-separated form).
//
// Empty arg is rejected at parse time; callers may assume arg != "".
func resolveSessionIDViaList(ctx context.Context, socketPath, arg string) (string, error) {
	list, err := control.SessionsList(ctx, socketPath)
	if err != nil {
		return "", err
	}
	for _, s := range list {
		if s.ID == arg {
			return s.ID, nil
		}
	}
	var matches []control.SessionInfo
	for _, s := range list {
		if strings.HasPrefix(s.ID, arg) {
			matches = append(matches, s)
		}
	}
	switch len(matches) {
	case 0:
		return "", sessions.ErrSessionNotFound
	case 1:
		return matches[0].ID, nil
	default:
		sort.Slice(matches, func(i, j int) bool { return matches[i].ID < matches[j].ID })
		var b strings.Builder
		for i, m := range matches {
			label := m.Label
			if m.Bootstrap && label == "" {
				label = "bootstrap"
			}
			if i > 0 {
				b.WriteByte('\n')
			}
			fmt.Fprintf(&b, "%s %s", m.ID, label)
		}
		return "", fmt.Errorf("%w:\n%s", errAmbiguousPrefix, b.String())
	}
}

// runSessionsRm implements `pyry sessions rm [--archive|--purge] <id>`:
// resolve the (possibly-prefix) <id> via sessions.list, dial the
// daemon's control socket, ask it to terminate the named session,
// remove its registry entry, and apply the JSONL disposition policy.
//
// Exit codes match the rest of cmd/pyry:
//
//	0 — removal succeeded.
//	1 — runtime error (ambiguous prefix, unknown id, bootstrap
//	    rejection, server-side error, or no-daemon dial failure).
//	2 — usage error (parse failure, mutually-exclusive flags, or
//	    wrong arity). Mirrors runAttach's exit-2 policy.
//
// The three AC-prescribed messages (ambiguous, unknown, bootstrap) are
// printed to stderr without the `pyry:` outer-error prefix; other
// errors flow through `fmt.Errorf("sessions rm: %w", err)`, which
// main's top-level error printer prepends with `pyry: `.
func runSessionsRm(socketPath string, args []string) error {
	id, policy, err := parseSessionsRmArgs(args)
	if err != nil {
		if errors.Is(err, errSessionsRmUsage) {
			fmt.Fprintln(os.Stderr, "pyry sessions rm:", err)
		}
		os.Exit(2)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	canonical, err := resolveSessionIDViaList(ctx, socketPath, id)
	if err != nil {
		switch {
		case errors.Is(err, errAmbiguousPrefix):
			fmt.Fprintln(os.Stderr, err.Error())
			os.Exit(1)
		case errors.Is(err, sessions.ErrSessionNotFound):
			fmt.Fprintf(os.Stderr, "no session with id %q\n", id)
			os.Exit(1)
		}
		return fmt.Errorf("sessions rm: %w", err)
	}

	if err := control.SessionsRm(ctx, socketPath, canonical, policy); err != nil {
		switch {
		case errors.Is(err, sessions.ErrCannotRemoveBootstrap):
			fmt.Fprintln(os.Stderr, "cannot remove bootstrap session")
			os.Exit(1)
		case errors.Is(err, sessions.ErrSessionNotFound):
			// Race window: list returned the canonical UUID, then
			// another caller removed it before our SessionsRm landed.
			// Surface the original (typed) <id> — that's the string
			// the operator typed.
			fmt.Fprintf(os.Stderr, "no session with id %q\n", id)
			os.Exit(1)
		}
		return fmt.Errorf("sessions rm: %w", err)
	}
	return nil
}

// errSessionsRenameUsage marks every parse-time failure of
// `pyry sessions rename` as a usage error. runSessionsRename matches via
// errors.Is and exits 2 with the wrapped message printed verbatim (no
// `pyry:` prefix). One sentinel covers arity and any future handler-side
// usage rule. Mirrors errSessionsRmUsage's shape.
var errSessionsRenameUsage = errors.New("usage")

// parseSessionsRenameArgs parses `<id> <new-label>`. Returns
// (id, newLabel, err). Both positionals are required; the empty string is
// a valid value for <new-label> (Pool.Rename treats it as "clear the
// on-disk label" per #62), so the arity check counts positionals (must
// be exactly 2) rather than testing for non-empty strings.
//
// No flags today — the FlagSet exists for symmetry with `new` and `rm`
// and so a future flag slots in mechanically.
func parseSessionsRenameArgs(args []string) (id, newLabel string, err error) {
	fs := flag.NewFlagSet("pyry sessions rename", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return "", "", fmt.Errorf("%w: %v", errSessionsRenameUsage, err)
	}
	if fs.NArg() != 2 {
		return "", "", fmt.Errorf("%w: expected <id> <new-label>, got %d positional args", errSessionsRenameUsage, fs.NArg())
	}
	return fs.Arg(0), fs.Arg(1), nil
}

// runSessionsRename implements `pyry sessions rename <id> <new-label>`:
// resolve the (possibly-prefix) <id> via sessions.list, dial the daemon's
// control socket, ask it to update the named session's human-friendly
// label.
//
// Exit codes match the rest of cmd/pyry:
//
//	0 — rename succeeded.
//	1 — runtime error (ambiguous prefix, unknown id, server-side
//	    error, or no-daemon dial failure).
//	2 — usage error (parse failure or wrong arity).
//
// The AC-prescribed messages (ambiguous, unknown) are printed to stderr
// without the `pyry:` outer-error prefix; other errors flow through
// `fmt.Errorf("sessions rename: %w", err)`, which main's top-level error
// printer prepends with `pyry: `.
func runSessionsRename(socketPath string, args []string) error {
	id, newLabel, err := parseSessionsRenameArgs(args)
	if err != nil {
		if errors.Is(err, errSessionsRenameUsage) {
			fmt.Fprintln(os.Stderr, "pyry sessions rename:", err)
		}
		os.Exit(2)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	canonical, err := resolveSessionIDViaList(ctx, socketPath, id)
	if err != nil {
		switch {
		case errors.Is(err, errAmbiguousPrefix):
			fmt.Fprintln(os.Stderr, err.Error())
			os.Exit(1)
		case errors.Is(err, sessions.ErrSessionNotFound):
			fmt.Fprintf(os.Stderr, "no session with id %q\n", id)
			os.Exit(1)
		}
		return fmt.Errorf("sessions rename: %w", err)
	}

	if err := control.SessionsRename(ctx, socketPath, canonical, newLabel); err != nil {
		if errors.Is(err, sessions.ErrSessionNotFound) {
			// Race window: resolver returned the canonical UUID, then
			// another caller removed it before our wire call landed.
			// Surface the operator's original <id> — the string they typed.
			fmt.Fprintf(os.Stderr, "no session with id %q\n", id)
			os.Exit(1)
		}
		return fmt.Errorf("sessions rename: %w", err)
	}
	return nil
}

// parseSessionsListArgs parses `[--json]`. Returns (jsonOut, err). No
// positional arguments accepted — `pyry sessions list` lists every session
// in one shot. Mirrors parseSessionsNewArgs's shape: extracted so flag
// rules are unit-testable without dialling the control socket. Errors are
// returned verbatim (no usage sentinel) — runSessionsList wraps via
// fmt.Errorf("sessions list: %w", err) and exits 1, matching
// runSessionsNew's exit-1-on-parse-error precedent.
func parseSessionsListArgs(args []string) (jsonOut bool, err error) {
	fs := flag.NewFlagSet("pyry sessions list", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	jsonFlag := fs.Bool("json", false, "emit JSON instead of a human table")
	if err := fs.Parse(args); err != nil {
		return false, err
	}
	if fs.NArg() > 0 {
		return false, fmt.Errorf("unexpected positional %q", fs.Arg(0))
	}
	return *jsonFlag, nil
}

// sortSessionsForDisplay applies the renderer's deterministic order in
// place: LastActive descending (most recent first), ID ascending as the
// tiebreak. Pool.List already returns this order today, but the AC says
// the renderer enforces it — defence against future wire changes that
// would otherwise reshuffle every operator's table. time.Time.Equal (not
// ==) handles JSON-roundtripped values that have lost their monotonic
// component (see lessons.md § "JSON roundtrip strips monotonic-clock
// state").
func sortSessionsForDisplay(list []control.SessionInfo) {
	sort.SliceStable(list, func(i, j int) bool {
		if !list[i].LastActive.Equal(list[j].LastActive) {
			return list[i].LastActive.After(list[j].LastActive)
		}
		return list[i].ID < list[j].ID
	})
}

// writeSessionsTable renders the snapshot as a tabwriter-aligned table to
// w. Columns: UUID, LABEL, STATE, LAST-ACTIVE. UUIDs render in their full
// 36-character canonical form (no truncation — operators copy/paste them).
// LAST-ACTIVE is rendered as RFC3339; jq consumers wanting nanos use
// --json. Empty Label renders as the empty cell — the wire substitutes
// the bootstrap entry's empty on-disk label with "bootstrap" before
// returning, so this layer renders verbatim.
func writeSessionsTable(w io.Writer, list []control.SessionInfo) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(tw, "UUID\tLABEL\tSTATE\tLAST-ACTIVE"); err != nil {
		return err
	}
	for _, s := range list {
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n",
			s.ID, s.Label, s.State, s.LastActive.Format(time.RFC3339)); err != nil {
			return err
		}
	}
	return tw.Flush()
}

// writeSessionsJSON encodes the snapshot as a single JSON object with a
// top-level "sessions" array. Envelope is intentionally NOT a bare array —
// leaves room for future top-level fields (e.g. "generated_at") without a
// breaking change. Per-element shape is whatever encoding/json produces
// from control.SessionInfo (id, label, state, last_active, optional
// bootstrap). Encoder.Encode appends a single \n — what jq pipelines
// expect.
func writeSessionsJSON(w io.Writer, list []control.SessionInfo) error {
	payload := struct {
		Sessions []control.SessionInfo `json:"sessions"`
	}{Sessions: list}
	enc := json.NewEncoder(w)
	return enc.Encode(payload)
}

// runSessionsList implements `pyry sessions list [--json]`: dial the
// daemon's control socket, fetch the session snapshot, render it as
// either a human-readable table or a single JSON object. Empty pool
// (would only ever contain bootstrap) renders a one-row table or a
// one-element sessions array.
func runSessionsList(socketPath string, args []string) error {
	jsonOut, err := parseSessionsListArgs(args)
	if err != nil {
		return fmt.Errorf("sessions list: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	list, err := control.SessionsList(ctx, socketPath)
	if err != nil {
		return fmt.Errorf("sessions list: %w", err)
	}

	sortSessionsForDisplay(list)

	if jsonOut {
		if err := writeSessionsJSON(os.Stdout, list); err != nil {
			return fmt.Errorf("sessions list: %w", err)
		}
		return nil
	}
	if err := writeSessionsTable(os.Stdout, list); err != nil {
		return fmt.Errorf("sessions list: %w", err)
	}
	return nil
}

// runStop implements `pyry stop`: dial the control socket and ask the daemon
// to shut down. Returns when the server has acknowledged — the daemon may
// still be unwinding its child.
func runStop(args []string) error {
	socketPath, rest, err := parseClientFlags("pyry stop", args)
	if err != nil {
		return err
	}
	if len(rest) > 0 {
		return fmt.Errorf("stop: unexpected arguments: %s", strings.Join(rest, " "))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := control.Stop(ctx, socketPath); err != nil {
		return fmt.Errorf("stop: %w", err)
	}
	fmt.Println("pyry: stop requested")
	return nil
}

// runInstallService implements `pyry install-service`: write a systemd unit
// (Linux) or launchd plist (macOS) for pyry, ready to enable. The user's
// claude flags are split off after `--` and baked into ExecStart; without
// them, the unit is written as a documented template the user edits before
// enabling.
func runInstallService(args []string) error {
	// Split pyry-side flags from claude flags at the first "--".
	var pyrySide, claudeSide []string
	for i, a := range args {
		if a == "--" {
			pyrySide = args[:i]
			claudeSide = args[i+1:]
			break
		}
	}
	if pyrySide == nil {
		pyrySide = args
	}

	fs := flag.NewFlagSet("pyry install-service", flag.ContinueOnError)
	name := fs.String("pyry-name", defaultName(), "instance name (filename + ExecStart suffix)")
	systemdFlag := fs.Bool("systemd", false, "force systemd output (default: detect from OS)")
	launchdFlag := fs.Bool("launchd", false, "force launchd output (default: detect from OS)")
	workdir := fs.String("workdir", "", "WorkingDirectory baked into the unit (default: current directory)")
	pathEnv := fs.String("path", "", "PATH baked into the unit (default: inherit your current shell's PATH)")
	force := fs.Bool("force", false, "overwrite an existing unit file")
	if err := fs.Parse(pyrySide); err != nil {
		return err
	}
	if *systemdFlag && *launchdFlag {
		return fmt.Errorf("--systemd and --launchd are mutually exclusive")
	}
	plat := install.PlatformAuto
	switch {
	case *systemdFlag:
		plat = install.PlatformSystemd
	case *launchdFlag:
		plat = install.PlatformLaunchd
	}

	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("install-service: get cwd: %w", err)
	}
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("install-service: home dir: %w", err)
	}
	resolvedWorkDir, err := install.ResolveWorkDir(*workdir, cwd, homeDir)
	if err != nil {
		return fmt.Errorf("install-service: resolve workdir: %w", err)
	}

	fmt.Printf("WorkingDirectory: %s\n", resolvedWorkDir)
	if _, err := os.Stat(resolvedWorkDir); errors.Is(err, iofs.ErrNotExist) {
		fmt.Printf("warning: %s does not exist; create it with: mkdir -p %s\n",
			resolvedWorkDir, resolvedWorkDir)
	}

	path, resolved, err := install.Install(install.Options{
		Platform:   plat,
		Name:       *name,
		WorkDir:    resolvedWorkDir,
		PathEnv:    *pathEnv,
		ClaudeArgs: claudeSide,
		Force:      *force,
	})
	if err != nil {
		if errors.Is(err, install.ErrFileExists) {
			return fmt.Errorf("%w: %s", err, path)
		}
		return fmt.Errorf("install-service: %w", err)
	}

	fmt.Printf("Wrote %s (%s)\n\n", path, resolved)
	if *pathEnv == "" {
		// Tell the user what we inherited so surprises surface here, not
		// after a service starts and a hook silently fails.
		fmt.Printf("Inherited PATH from current shell (review with: systemctl --user cat %s):\n", *name)
		for _, entry := range strings.Split(os.Getenv("PATH"), ":") {
			if entry == "" {
				continue
			}
			fmt.Printf("    %s\n", entry)
		}
		fmt.Println()
	}
	switch resolved {
	case install.PlatformSystemd:
		if len(claudeSide) == 0 {
			fmt.Printf("Edit ExecStart for your claude flags, then:\n")
		} else {
			fmt.Printf("Next:\n")
		}
		fmt.Printf("    systemctl --user daemon-reload\n")
		fmt.Printf("    systemctl --user enable --now %s\n\n", *name)
		fmt.Printf("Verify with:\n")
		fmt.Printf("    pyry status\n")
		fmt.Printf("    pyry logs\n")
		fmt.Printf("    journalctl --user -u %s -f\n\n", *name)
		fmt.Printf("For boot-before-login persistence: sudo loginctl enable-linger $USER\n")
	case install.PlatformLaunchd:
		if len(claudeSide) == 0 {
			fmt.Printf("Edit ProgramArguments for your claude flags, then:\n")
		} else {
			fmt.Printf("Next:\n")
		}
		fmt.Printf("    launchctl load %s\n\n", path)
		fmt.Printf("Verify with:\n")
		fmt.Printf("    pyry status\n")
		fmt.Printf("    pyry logs\n")
		fmt.Printf("    tail -f /tmp/pyry.%s.{out,err}.log\n", *name)
	}
	return nil
}

func printHelp() {
	fmt.Print(helpText)
}

// helpText is what printHelp prints. It is a package-level constant rather than
// a literal inlined in printHelp so TestHelpTextDropsRemovedVerbs can read the
// advertised verb list without capturing os.Stdout.
const helpText = `pyry — Pyrycode daemon, a supervisor for Claude Code

pyry is a near-drop-in replacement for ` + "`claude`" + `: anything it doesn't
recognize is forwarded to claude verbatim. pyry's own configuration uses an
explicit -pyry-* prefix so it never collides with claude's namespace.

Usage:
  pyry [pyry-flags] [claude-flags-and-args...]   supervised claude session
  pyry [pyry-flags] -- [claude-args-with-dashes] (use -- if claude args begin
                                                  with -pyry-* by accident)
  pyry status [flags]                            query the running daemon
  pyry stop [flags]                              ask the daemon to shut down
  pyry logs [flags]                              print recent supervisor logs
  pyry sessions <verb> [flags]                   manage sessions on a running
                                                  daemon (verbs: new, rm, rename, list)
  pyry channel new [--name <label>]              create a channel whose workspace
                                                  is the current directory, and
                                                  print its conversation id
  pyry pair [flags] [--name <label>]             ask a running service to mint a
                                                  pairing; selects the sole service,
                                                  or requires -pyry-name when several
                                                  run (PYRY_NAME is not a selector;
                                                  --relay is rejected because the
                                                  service owns its relay destination)
  pyry pair list [flags]                         list saved paired devices offline
  pyry pair revoke <name> [flags]                revoke a saved device offline by Name
  pyry pair preflight [flags]                    check saved device state offline
  pyry rekey <conn_id> [flags]                   trigger an immediate Noise re-key
                                                  on the named v2 conn (operator
                                                  rotation; control-socket only)
  pyry install-service [flags] [-- claude-args]  write a systemd or launchd
                                                  unit file for pyry
  pyry update [--check] [--version <v>]          download and install the latest
                                                  release (--check: print versions
                                                  only; --version <v>: pin a tag)
  pyry agent-run [flags]                         drive a single supervised claude
                                                  turn headlessly; replaces
                                                  ` + "`claude -p`" + ` in the dispatcher
                                                  (see --help on the verb for the
                                                  full flag list)
  pyry mcp-approve [flags]                       serve the MCP approve tool over
                                                  stdio, forwarding each tool-use
                                                  approval to the daemon
                                                  (spawned by claude via
                                                  --permission-prompt-tool)
  pyry mcp-files [flags]                         serve the MCP send_file tool over
                                                  stdio, filing a workspace file
                                                  under the calling session's
                                                  conversation (spawned by claude;
                                                  reads PYRY_SESSION_ID)
  pyry version                                   print version
  pyry help                                      show this help

Pyry flags (must come before claude args, or after a -- separator):
  -pyry-claude string   path to the claude binary (default "claude")
  -pyry-workdir string  working directory for claude (default: current)
  -pyry-resume          --continue most recent session on restart (default true)
  -pyry-verbose         verbose pyry logging
  -pyry-name string     instance name; socket is ~/.pyry/<name>.sock
                        (default "pyry"; PYRY_NAME env var overrides default)
  -pyry-socket string   explicit socket path (overrides -pyry-name)
  -pyry-idle-timeout    evict idle claudes after this duration
                        (default 0 / disabled; pass e.g. 15m to enable)
  -pyry-conv-sweep-interval duration  override conversations sweep tick interval
                        (testing; 0 = production default of 1h)
  -pyry-wrapup-deadline duration  shorten the conversation reset's wrap-up bound
                        (testing; 0 or >= the 90s default = production default.
                        It can only shorten: the ceiling is not operator-raisable)
  -pyry-relay string    relay URL (default: $PYRY_RELAY_URL or ~/.pyry/config.json)

Examples:
  pyry                                  # supervised claude (default instance)
  pyry "summarize foo.md"               # initial prompt forwarded to claude
  pyry --model sonnet -p "..."          # any claude flag passes through
  pyry -pyry-name elli                  # second instance, socket ~/.pyry/elli.sock
  PYRY_NAME=elli pyry status            # status of the elli instance via env
  pyry status                           # check on the running daemon
  pyry pair                             # pair through the sole running service
  pyry pair -pyry-name elli             # select elli when several services run
  pyry stop                             # graceful shutdown via control socket
  pyry logs                             # last 200 lines of supervisor logs
  pyry install-service                  # write a systemd/launchd unit (template)
  pyry install-service -- --dangerously-skip-permissions \
        --channels plugin:discord@claude-plugins-official  # bake flags into ExecStart

See https://github.com/pyrycode/pyrycode for documentation.
`
