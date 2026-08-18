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
//	pyry attach           Attach local terminal to a service-mode daemon
//	pyry sessions <verb>  Multi-session management (verbs: new, rm, rename, list)
//	pyry pair             Mint a device token and print the QR / paste payload
//	pyry install-service  Write a systemd / launchd unit file for pyry
//	pyry agent-run        Drive a single supervised claude turn headlessly
//	                       (replaces `claude -p` in the dispatcher)
//	pyry acp              Serve the ACP JSON-RPC transport over stdio
//	                       (spawned by an ACP host)
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
	"github.com/pyrycode/pyrycode/internal/install"
	"github.com/pyrycode/pyrycode/internal/msgqueue"
	"github.com/pyrycode/pyrycode/internal/permbridge"
	"github.com/pyrycode/pyrycode/internal/relay"
	"github.com/pyrycode/pyrycode/internal/relay/handlers"
	"github.com/pyrycode/pyrycode/internal/sessions"
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
// _. Empty input becomes "_". Defends the on-disk socket filename against
// path-traversal and other filesystem-unsafe input (e.g. PYRY_NAME from a
// careless shell setup).
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
	return b.String()
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "pyry:", err)
		os.Exit(1)
	}
}

func run() error {
	if len(os.Args) >= 2 {
		switch os.Args[1] {
		case "version", "-v", "--version":
			fmt.Println("pyry", Version)
			return nil
		case "status":
			return runStatus(os.Args[2:])
		case "stop":
			return runStop(os.Args[2:])
		case "logs":
			return runLogs(os.Args[2:])
		case "sessions":
			return runSessions(os.Args[2:])
		case "pair":
			return runPair(os.Args[2:])
		case "rekey":
			return runRekey(os.Args[2:])
		case "install-service":
			return runInstallService(os.Args[2:])
		case "update":
			return runUpdate(os.Args[2:])
		case "agent-run":
			return runAgentRun(os.Stdout, os.Args[2:])
		case "mcp-approve":
			return runMCPApprove(os.Args[2:])
		case "help", "-h", "--help":
			printHelp()
			return nil
		}
	}

	return runSupervisor(os.Args[1:])
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
func selectInteractiveRunner(cfg config.Config, logger *slog.Logger, mcpApprovePath string) (sessions.RunnerFactory, *streamTurnSink, error) {
	switch cfg.InteractiveRunner {
	case "", "stream-json":
		sink := newStreamTurnSink(0, logger)
		return newStreamRunnerFactory(sink, mcpApprovePath), sink, nil
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
	relayFlag := fs.String("pyry-relay", "", "relay URL override (default: $PYRY_RELAY_URL or ~/.pyry/config.json)")
	if err := fs.Parse(pyryArgs); err != nil {
		return err
	}
	socketPath := resolveSocketPath(*socketFlag, *name)
	registryPath := resolveRegistryPath(*name)
	convRegistryPath := resolveConversationsRegistryPath(*name)
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
	// mcpApprovePath stays "".
	var mcpApprovePath string
	if selectsStreamRunner(cfg) {
		mcpApprovePath, err = writeMCPApproveConfig(resolveExecutable(), socketPath)
		if err != nil {
			return fmt.Errorf("write mcp-approve config: %w", err)
		}
		defer func() { _ = os.Remove(mcpApprovePath) }()
	}
	// Interactive-runner selection (#1081): pick the runner factory + its shared
	// turn-event sink from config BEFORE the pool is built, so an invalid value
	// fails fast (AC4, no silent PTY fallback). Both are nil on the "" / "pty"
	// rollback path, leaving the sessions.Config and relayWiring literals below
	// byte-identical to today. On "stream-json" the same sink instance is threaded
	// two ways: RunnerFactory (below) and relayWiring.streamSink (the drain); the
	// factory also carries mcpApprovePath to inject the approval-tool flags (#1168).
	runnerFactory, streamSink, err := selectInteractiveRunner(cfg, logger, mcpApprovePath)
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
	var turnBusy *turnBusyTracker
	if streamSink != nil {
		turnBusy = newTurnBusyTracker(
			func(sid string) (string, bool) { return conversationForSession(convReg, sid) }, logger)
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
	queue, err := msgqueue.New(msgqueue.Config{
		Deliver:  newInboundDeliver(router.resolve, turnBusy, streamTurnHoldTimeout),
		OnChange: queueStateNotify(queueChanges, logger),
		OnGiveUp: blocked,
		// Pending exempted a turn held behind claude's startup trust modal from the
		// give-up bound (#1014 AC-1). That modal only ever appeared on the terminal
		// surface, which #1348 removed, so nothing produces the sentinel any more
		// and the exemption is permanently false. Left unset rather than wired to a
		// never-true predicate: the stream surface has no equivalent hold today,
		// and inventing one here would be guessing at a failure nobody has seen.
		Logger: logger,
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

	// One shared pending-approval registry for the whole daemon. Created at
	// the composition root — the only scope that sees both the control
	// server (which services VerbMCPApprove against it below) and, once
	// #1080 lands, the v2 modal-resolve consumer that will Resolve/Lookup
	// against this exact instance. No relayWiring field is threaded here:
	// with no reader until #1080, a set-but-unread field would trip
	// staticcheck U1000; #1080 is the thin change that adds the reader.
	approvals := permbridge.New()

	relayCleanup, approvalSurface, err := startRelay(ctx, logger, relayWiring{
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
				return resolveBoundRunner(convReg, pool, convID)
			},
			log: logger,
		},
		activeSessionStarter: activeSessionStarter{
			currentConv: active.CurrentConversation,
			resolveBound: func(convID string) (sessions.Runner, sessions.SessionID, bool) {
				sess, id, ok := resolveBoundSession(convReg, pool, convID)
				if !ok {
					return nil, "", false
				}
				return sess.Runner(), id, true
			},
			rotate: pool.RotateForNewSession,
		},
		claudeSessionsDir: claudeSessionsDir,
		bootstrapIDFn:     func() string { return string(pool.BootstrapID()) },
		defaultCwd:        defaultCwd,
		transitions:       pool,
		qse:               qse,
		sessionErr:        see,
		blockedNotify:     blocked,
		debugBundler:      debugBundler,
		settings:          settingsUpdaterAdapter{pool},
		snapshotSettings:  snapshotSettings,
		approvals:         approvals,
		streamSink:        streamSink,
		busy:              turnBusy,
	})
	if err != nil {
		return fmt.Errorf("relay start: %w", err)
	}
	defer relayCleanup()

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
	ctrl.SetApprovalRegistry(approvals, approvalTimeout())
	// Install the stream-approval surfacer (#1080) so a parked mcp.approve raises
	// the SAME permission modal_shown clients already answer and a client's
	// modal_answer resolves claude's blocked tool. nil when the relay leg is
	// disabled (no URL) — SetApprovalSurfacer(nil) leaves mcp.approve modal-less,
	// the pre-#1080 behaviour.
	ctrl.SetApprovalSurfacer(approvalSurface)
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

	// Stop the control server (already wired to ctx but Close is idempotent
	// and ensures the socket file is gone before we return).
	_ = ctrl.Close()
	<-ctrlDone

	// Join the inbound-queue lifecycle: pool.Run returned because ctx was
	// cancelled, so queue.Run has observed ctx.Done and is winding down its
	// drains. Waiting here keeps the daemon from exiting while a drain goroutine
	// is still in flight.
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
// Pool.CreateIn, which uses the resolved realpath verbatim (#685). A rejected
// spawnDir wraps handlers.ErrSpawnDirRejected and short-circuits before any
// mint. The precedent for this type-narrowing seam is poolResolver above.
type sessionMinter struct{ p *sessions.Pool }

func (m sessionMinter) Create(ctx context.Context, label, spawnDir string) (string, error) {
	resolved, err := resolveSpawnDir(spawnDir)
	if err != nil {
		return "", err
	}
	id, err := m.p.CreateIn(ctx, label, resolved)
	return string(id), err
}

// settingsUpdaterAdapter adapts *sessions.Pool to relay.SettingsUpdater (#845).
// It narrows the type (relay speaks relay.SettingsUpdate / relay.ErrSessionUnknown
// so internal/relay imports neither internal/sessions nor cmd/pyry) and owns the
// single sessions.ErrSessionNotFound → relay.ErrSessionUnknown mapping — the
// project convention that sentinel-to-wire mapping lives at the consumer call
// site, not in the primitive. The three presence pointers pass straight through:
// relay.SettingsUpdate mirrors sessions.SettingsUpdate 1:1, so a nil field still
// means "leave unchanged" and a nil YOLO can never enable bypass. The precedent
// for this type-narrowing seam is sessionMinter / poolResolver above.
type settingsUpdaterAdapter struct{ p *sessions.Pool }

func (a settingsUpdaterAdapter) UpdateSettings(id string, u relay.SettingsUpdate) error {
	err := a.p.UpdateSettings(sessions.SessionID(id), sessions.SettingsUpdate{
		Model:  u.Model,
		Effort: u.Effort,
		YOLO:   u.YOLO,
	})
	if errors.Is(err, sessions.ErrSessionNotFound) {
		return relay.ErrSessionUnknown
	}
	return err
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
// VALUES are operator-facing: SendEsc logs them verbatim as a record's arm field
// (#1193), so they are part of the record contract, not an internal detail.
type interruptArm string

const (
	armInterrupt interruptArm = "interrupt" // streamRunner.Interrupt()
	armSendEsc   interruptArm = "send_esc"  // *supervisor.Supervisor.SendEsc()
	armNone      interruptArm = "none"      // neither method — inert
)

// interruptRunner actuates a runner's interrupt through whichever concrete method
// its runner type exposes — the SendEsc-vs-Interrupt dispatch #1121 places in
// cmd/pyry, the only package that sees both concrete runner types (the streamRunner
// adapter lives here, so internal/sessions cannot reach it). The two arms are
// mutually exclusive: *supervisor.Supervisor has SendEsc but no Interrupt, and
// streamRunner has Interrupt but no SendEsc, so the switch is unambiguous. Interrupt
// is matched first so any future runner that grows both prefers the stream-json
// control_request over a PTY Esc. An unknown runner is inert (nil) — no actuation
// beats wrong actuation.
//
// It returns the arm it dispatched to alongside the chosen method's error, so the
// caller — the only scope holding the conversation id — can record which arm ran
// (#1193); armNone always pairs with a nil error. The dispatcher itself stays pure:
// no logger, no ambient state. The arm is an OBSERVABILITY value and nothing may
// branch on it beyond selecting a record — in particular armNone must NOT trigger a
// fallback actuation, since the only other runner to try is the bootstrap
// supervisor, the #678 cross-conversation isolation break resolveBoundRunner's
// guard exists to prevent.
func interruptRunner(r sessions.Runner) (interruptArm, error) {
	switch v := r.(type) {
	case interface{ Interrupt() error }:
		return armInterrupt, v.Interrupt()
	case interface{ SendEsc() error }:
		return armSendEsc, v.SendEsc()
	default:
		return armNone, nil
	}
}

// resolveBoundRunner resolves the active conversation's bound runner, mirroring
// boundHost's lookup shape and sessionRouter.resolve's load-bearing guard:
// convID → CurrentSessionID → Pool.Lookup → the bound session's runner. The
// conv.CurrentSessionID == "" guard is the cross-conversation isolation
// enforcement point — without it Pool.Lookup("") returns the BOOTSTRAP session
// (see errNoBoundSession / sessionRouter.resolve), so an unbound conversation's
// interrupt would actuate the shared bootstrap claude (the #678 isolation break).
// Every non-resolvable state returns (nil, false) so the caller stays inert; this
// NEVER falls through to bootstrap.
func resolveBoundRunner(convReg *conversations.Registry, pool *sessions.Pool, convID string) (sessions.Runner, bool) {
	conv, ok := convReg.Get(conversations.ConversationID(convID))
	if !ok || conv.CurrentSessionID == "" {
		return nil, false
	}
	sess, err := pool.Lookup(sessions.SessionID(conv.CurrentSessionID))
	if err != nil {
		return nil, false
	}
	return sess.Runner(), true
}

// activeInterrupter satisfies relay.Interrupter by routing an inbound interrupt to
// the runner bound to the ACTIVE conversation — replacing the former
// Interrupter: w.sup wiring that mis-delivered every interrupt to the bootstrap
// supervisor regardless of which conversation's turn was running (#1121). The two
// seams are injected (not raw *Pool/*Registry) so the AC2 test can drive the
// composition with fakes; production wires currentConv: active.CurrentConversation
// and resolveRunner over resolveBoundRunner(convReg, pool, …).
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

// SendEsc interrupts the active conversation's bound runner. It keeps the relay
// seam's method name (relay.Interrupter.SendEsc) even though the actuation is a
// per-runner interrupt, not literally an Esc — the seam doc already abstracts
// SendEsc as "claude's own interrupt" (#1121 seam decision), so the whole
// internal/relay package (including its interrupt tests) stays untouched. Every
// ambiguous state (no active conversation, unbound/dangling binding) is inert
// (nil), never actuating the wrong child; a live runner's no-child error
// propagates for handleInterrupt to Warn-log and tolerate (best-effort contract).
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
func (a activeInterrupter) SendEsc() error {
	convID := a.currentConv()
	if convID == "" {
		// No conversation id to carry — the record's information is its existence:
		// the frame reached SendEsc and nothing was active.
		a.logger().Info("relay: v2 interrupt inert; no active conversation",
			"event", "v2.interrupt.no_active_conv")
		return nil
	}
	r, ok := a.resolveRunner(convID)
	if !ok {
		a.logger().Info("relay: v2 interrupt inert; active conversation has no bound runner",
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
func resolveBoundSession(convReg *conversations.Registry, pool *sessions.Pool, convID string) (*sessions.Session, sessions.SessionID, bool) {
	conv, ok := convReg.Get(conversations.ConversationID(convID))
	if !ok || conv.CurrentSessionID == "" {
		return nil, "", false
	}
	sess, err := pool.Lookup(sessions.SessionID(conv.CurrentSessionID))
	if err != nil {
		return nil, "", false
	}
	return sess, sessions.SessionID(conv.CurrentSessionID), true
}

// startFreshRunner is the new_session twin of interruptRunner: it dispatches a
// fresh-session start to the active conversation's bound runner by concrete type.
// The streamRunner arm (*streamsup.Runner, exposing RestartFresh) is the DIRECT
// path — rotate the pool-side id then RestartFresh so the next spawn uses
// --session-id <newID>, with NO /clear keystroke. The *supervisor.Supervisor arm
// keeps today's PTY behavior: type /clear and let the fsnotify watcher drive the
// pool-side rotation (we do NOT pre-mint an id there — /clear makes claude pick
// its own, so a pre-minted id would mismatch). RestartFresh is matched first so
// any future runner that grows both prefers the direct stream-json path; an
// unknown runner is inert (nil) — no actuation beats wrong actuation (#1121).
//
// Ordering is load-bearing: rotate() completes — including the allocated-skip-set
// register published under Pool.mu — BEFORE RestartFresh spawns <newID>.jsonl, so
// the watcher's CREATE observation is guaranteed to see the registration and skip
// the id. Reversing it reopens the double-rotation race (spec §Concurrency).
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
func startFreshRunner(r sessions.Runner, oldID sessions.SessionID,
	rotate func(sessions.SessionID) (sessions.SessionID, error)) error {
	switch v := r.(type) {
	case interface{ RestartFresh(string) }:
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
		v.RestartFresh(string(newID))
		return nil
	case interface{ StartNewSession() error }:
		return v.StartNewSession()
	default:
		return nil
	}
}

// beginRotationOrNoop arms r's rotation gate when the runner has one (#1330) and
// returns the disarm; a runner without the gate returns an inert disarm, leaving
// the dispatch shape unchanged.
//
// An OPTIONAL assertion, deliberately, rather than widening startFreshRunner's
// case to interface{ RestartFresh(string); BeginRotation() func() }. Widening it
// would silently re-route restartFreshStub and bothMethodsStub
// (new_session_routing_test.go) to the inert default arm, turning existing
// subtests from assertions into vacuities without a single failure, and would
// change the documented "RestartFresh is matched first" dispatch contract. Treating
// the gate as a CAPABILITY is also how Interrupt and RestartFresh are already
// treated one layer up.
func beginRotationOrNoop(r sessions.Runner) (abort func()) {
	if g, ok := r.(interface{ BeginRotation() func() }); ok {
		return g.BeginRotation()
	}
	return func() {}
}

// activeSessionStarter satisfies relay.SessionStarter by routing an inbound
// new_session frame to the runner bound to the ACTIVE conversation — replacing
// the former SessionStarter: w.sup wiring that mis-delivered every new_session to
// the bootstrap supervisor regardless of which conversation the remote client was
// in (the #1121 interrupt shape, applied to new_session). The seams are injected
// (not raw *Pool/*Registry) so the AC2 test can drive the composition with fakes;
// production wires currentConv: active.CurrentConversation, resolveBound over
// resolveBoundSession, and rotate: pool.RotateForNewSession.
type activeSessionStarter struct {
	currentConv  func() string
	resolveBound func(convID string) (runner sessions.Runner, oldID sessions.SessionID, ok bool)
	rotate       func(oldID sessions.SessionID) (sessions.SessionID, error)
}

// StartNewSession starts a fresh session in the active conversation's bound
// runner. Order mirrors activeInterrupter.SendEsc and is load-bearing: no active
// conversation → inert; unbound/dangling binding → inert (resolveBound's
// CurrentSessionID == "" guard is the #678 isolation enforcement — an unbound
// conversation NEVER resolves to the bootstrap session Pool.Lookup("") returns).
// Otherwise dispatch by runner type. Every ambiguous state is inert (nil); a live
// runner or rotate error propagates for handleNewSession to Warn-log and tolerate
// (best-effort contract). No /clear keystroke is sent on the stream arm.
func (a activeSessionStarter) StartNewSession() error {
	convID := a.currentConv()
	if convID == "" {
		return nil
	}
	runner, oldID, ok := a.resolveBound(convID)
	if !ok {
		return nil
	}
	return startFreshRunner(runner, oldID, a.rotate)
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
// the half hour. There is deliberately NO Pending analogue (contrast
// supervisor.ErrTrustModalPending at the msgqueue.Config literal): that exemption
// resets the give-up streak forever, which a HUMAN decision may legitimately need
// and a running turn should not — it would make the bound unsatisfiable. The
// better discriminator is staleness (no turn event for N minutes) rather than
// duration, but that needs a per-conversation timestamp the tracker deliberately
// does not hold (#1201); it is a separate ticket if production ever surfaces a
// session_error for a turn that was legitimately progressing. A tuning knob, not a
// contract.
const streamTurnHoldTimeout = 15 * time.Minute

// mcpApprovalTimeout is the human-approval window handed to the
// pending-approval registry (internal/permbridge) for every VerbMCPApprove
// request: after it elapses with no resolver decision, the registry's
// own timer denies the request. Until #1080 wires the modal-resolve
// consumer there is no resolver, so every production approval times out to
// deny after this window — inert for now because nothing invokes the verb
// until the `pyry mcp-approve` sibling wires --permission-prompt-tool. A
// tuning knob, not a contract; make it configurable when the full chain
// lands.
const mcpApprovalTimeout = 2 * time.Minute

// envApprovalTimeout overrides the human-approval window (mcpApprovalTimeout). A
// plausibly-operational knob for tuning the window — the e2e (#1139) shrinks it to
// ~2s to prove the daemon's fail-closed timer denies a no-answer approval within a
// bounded deadline.
const envApprovalTimeout = "PYRY_APPROVAL_TIMEOUT"

// approvalTimeout is the approval window handed to the pending-approval registry:
// mcpApprovalTimeout by default, overridable via PYRY_APPROVAL_TIMEOUT. An unset or
// unparseable value falls back to the default, so production behaviour is
// byte-identical when the env is absent. Only the timer's DURATION is tunable —
// permbridge's deterministic deny-on-deadline logic (the fail-closed core) is
// untouched.
func approvalTimeout() time.Duration {
	if v := os.Getenv(envApprovalTimeout); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return mcpApprovalTimeout
}

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
//     between Activate and the write — see the placement note below;
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
		if err := busy.waitIdleForDelivery(ctx, convID, hold); err != nil {
			return fmt.Errorf("stream turn hold: %w", err)
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
	fmt.Print(`pyry — Pyrycode daemon, a supervisor for Claude Code

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
  pyry attach [flags] [--stdio] [<id>]           attach local terminal to daemon
                                                  (Ctrl-B d to detach; <id>
                                                  selects a session — full
                                                  UUID or unique prefix; omit
                                                  for the bootstrap session;
                                                  --stdio: no-PTY raw byte
                                                  forwarding for SDK consumers)
  pyry sessions <verb> [flags]                   manage sessions on a running
                                                  daemon (verbs: new, rm, rename, list)
  pyry pair [flags] [--name <label>] [--relay <url>]
                                                 mint a device token, persist it
                                                  in ~/.pyry/<name>/devices.json,
                                                  print QR + paste-fallback payload
  pyry pair list [flags]                         list paired devices
  pyry pair revoke <name> [flags]                revoke a paired device by Name
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
  pyry acp                                       serve the ACP JSON-RPC transport
                                                  over stdio (spawned by an ACP
                                                  host; takes no flags or args)
  pyry mcp-approve [flags]                       serve the MCP approve tool over
                                                  stdio, forwarding each tool-use
                                                  approval to the daemon
                                                  (spawned by claude via
                                                  --permission-prompt-tool)
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
  -pyry-relay string    relay URL (default: $PYRY_RELAY_URL or ~/.pyry/config.json)

Examples:
  pyry                                  # supervised claude (default instance)
  pyry "summarize foo.md"               # initial prompt forwarded to claude
  pyry --model sonnet -p "..."          # any claude flag passes through
  pyry -pyry-name elli                  # second instance, socket ~/.pyry/elli.sock
  PYRY_NAME=elli pyry status            # status of the elli instance via env
  pyry status                           # check on the running daemon
  pyry stop                             # graceful shutdown via control socket
  pyry logs                             # last 200 lines of supervisor logs
  pyry attach                           # interactive bridge to a service-mode daemon
  pyry install-service                  # write a systemd/launchd unit (template)
  pyry install-service -- --dangerously-skip-permissions \
        --channels plugin:discord@claude-plugins-official  # bake flags into ExecStart

See https://github.com/pyrycode/pyrycode for documentation.
`)
}
