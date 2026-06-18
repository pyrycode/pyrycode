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
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/pyrycode/pyrycode/internal/config"
	"github.com/pyrycode/pyrycode/internal/control"
	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/install"
	"github.com/pyrycode/pyrycode/internal/relay/handlers"
	"github.com/pyrycode/pyrycode/internal/sessions"
	"github.com/pyrycode/pyrycode/internal/supervisor"
	"golang.org/x/term"
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
		case "attach":
			return runAttach(os.Args[2:])
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

// extractSessionID scans claudeArgs for the value of claude's --session-id
// flag. Accepts the four shapes claude itself accepts: `--session-id <v>`,
// `--session-id=<v>`, `-session-id <v>`, `-session-id=<v>`. Returns "" when
// the flag is absent or appears as the last arg with no value. The first
// occurrence wins.
//
// Pure function — no environment, no syscalls. The returned string is
// opaque to extractSessionID; UUID validation is the daemon's job
// (sessions.has-id rejects malformed input server-side).
func extractSessionID(args []string) string {
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--session-id" || a == "-session-id":
			if i+1 < len(args) {
				return args[i+1]
			}
			return ""
		case strings.HasPrefix(a, "--session-id="):
			return strings.TrimPrefix(a, "--session-id=")
		case strings.HasPrefix(a, "-session-id="):
			return strings.TrimPrefix(a, "-session-id=")
		}
	}
	return ""
}

// tryAutoAttach is the foreground-binary auto-attach gate. Called from
// runSupervisor after pyry-flag parsing but before any supervisor-mode
// side effect (logger, ring buffer, Bridge, Pool init).
//
// Returns (false, nil) on every fall-through path: no --session-id in
// claudeArgs, PYRY_NO_AUTO_ATTACH=1 in the env, socket file absent, any
// stat error, transport failure, malformed UUID, or has-id returning
// false. The caller carries on with the existing supervised-spawn flow.
//
// Returns (true, err) when the daemon hosts the requested UUID and we
// dispatched to control.AttachStdio. err is the AttachStdio result —
// nil on a clean EOF detach, transport / handshake error otherwise.
//
// AC#3 (<50ms in the no-daemon case) is satisfied structurally: when
// the socket is absent, os.Stat's ENOENT branch returns before any
// dial / context allocation / goroutine.
func tryAutoAttach(socketPath string, claudeArgs []string) (handled bool, err error) {
	if os.Getenv("PYRY_NO_AUTO_ATTACH") == "1" {
		return false, nil
	}
	id := extractSessionID(claudeArgs)
	if id == "" {
		return false, nil
	}
	if _, statErr := os.Stat(socketPath); statErr != nil {
		// ENOENT is the common no-daemon path; any other stat error
		// (EACCES, EPERM, …) also falls through — auto-attach is the
		// exception, never the default.
		return false, nil
	}

	probeCtx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	has, hasErr := control.SessionsHasID(probeCtx, socketPath, id)
	if hasErr != nil || !has {
		return false, nil
	}

	if attachErr := control.AttachStdio(context.Background(), socketPath, id, os.Stdin, os.Stdout, false); attachErr != nil {
		return true, fmt.Errorf("attach: %w", attachErr)
	}
	return true, nil
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

	// Phase 1.3c-2: foreground binary auto-attaches when the daemon
	// hosts the requested --session-id. Conservative — falls through on
	// every failure mode except "definitely registered".
	if handled, err := tryAutoAttach(socketPath, claudeArgs); handled {
		return err
	}

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

	// Service vs foreground mode is detected from stdin: if there's no
	// controlling terminal (e.g. launchd / systemd / nohup), the supervisor
	// runs detached and PTY I/O routes through a Bridge so a `pyry attach`
	// client can take over interactively.
	var bridge *supervisor.Bridge
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		bridge = supervisor.NewBridge(logger)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	cfg, err := config.Load(resolveConfigPath())
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
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
		Bootstrap: sessions.SessionConfig{
			ClaudeBin:  *claudeBin,
			WorkDir:    trustedWorkdir,
			ResumeLast: *resume,
			ClaudeArgs: claudeArgs,
			Bridge:     bridge,
		},
	})
	if err != nil {
		return fmt.Errorf("pool init: %w", err)
	}

	relayURL := resolveRelayURL(*relayFlag, os.Getenv("PYRY_RELAY_URL"), cfg)
	allowInsecure := os.Getenv("PYRY_ALLOW_INSECURE_RELAY") == "1"
	// PYRY_MOBILE_V2=1 flips the daemon's relay leg from the v1 dispatch path
	// to the Mobile Protocol v2 (Noise_IK E2E) manager. Operator-set switch,
	// mirroring PYRY_ALLOW_INSECURE_RELAY (env-only, no config/flag); run
	// `pyry pair preflight` first to confirm no v1 pairings will break.
	v2Enabled := os.Getenv("PYRY_MOBILE_V2") == "1"
	bootstrap := pool.Default()
	relayCleanup, err := startRelay(ctx, logger, *name, relayURL, Version, allowInsecure, v2Enabled, cancel, convReg, sessionMinter{pool}, sessionRouter{pool: pool, convReg: convReg}, bootstrap.Supervisor(), bootstrap.Bridge(), claudeSessionsDir, defaultCwd, pool)
	if err != nil {
		return fmt.Errorf("relay start: %w", err)
	}
	defer relayCleanup()

	// Pool satisfies control.Sessioner directly — Pool.Create returns
	// sessions.SessionID and Pool.Remove returns plain error, matching
	// Sessioner.Create / Sessioner.Remove (via embedded Remover) signatures
	// with no adapter (contrast with poolResolver for the read-side Lookup).
	ctrl := control.NewServer(socketPath, poolResolver{pool}, logRing, cancel, logger, pool)
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

	if runErr != nil && !errors.Is(runErr, context.Canceled) {
		return fmt.Errorf("supervisor: %w", runErr)
	}
	logger.Info("pyrycode stopped")
	return nil
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

// sessionMinter adapts *sessions.Pool to handlers.SessionCreator. Pool.Create
// returns sessions.SessionID; the handler-side interface speaks plain string so
// internal/relay/handlers stays free of an internal/sessions import. The wrapper
// is the single type-narrowing seam — the precedent is poolResolver above.
type sessionMinter struct{ p *sessions.Pool }

func (m sessionMinter) Create(ctx context.Context, label string) (string, error) {
	id, err := m.p.Create(ctx, label)
	return string(id), err
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
}

// Route resolves conversationID to its bound session's write surface. The order
// is load-bearing: the empty-CurrentSessionID guard fires before any Lookup so
// an unbound conversation never resolves to the bootstrap session that
// Pool.Lookup("") returns (#678 AC#4).
func (r sessionRouter) Route(conversationID string) (handlers.TurnWriter, error) {
	conv, ok := r.convReg.Get(conversations.ConversationID(conversationID))
	if !ok {
		return nil, conversations.ErrConversationNotFound
	}
	if conv.CurrentSessionID == "" {
		return nil, errNoBoundSession
	}
	id := sessions.SessionID(conv.CurrentSessionID)
	sess, err := r.pool.Lookup(id)
	if err != nil {
		return nil, err
	}
	return boundSession{pool: r.pool, sess: sess, id: id}, nil
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

// parseAttachArgs peels the attach-specific sub-flags (--stdio,
// --create-if-missing) out of the post-`-pyry-*` remainder and returns the
// selector positional. Extracted from runAttach so the parsing rules can be
// unit-tested without dialling the control socket. Mirrors
// parseSessionsNewArgs's split — flag-set sub-parser first,
// attachSelectorFromArgs on the post-flag positionals.
//
// --create-if-missing with no positional does NOT error at parse time — the
// empty SessionID flows through to the server, where GetOrCreate's empty-id
// rejection produces the canonical ErrInvalidSessionID. Parse-time vs.
// semantic boundary: parsing is purely about shape; semantic rejection
// lives at the Pool.
func parseAttachArgs(args []string) (sessionID string, stdio bool, createIfMissing bool, err error) {
	fs := flag.NewFlagSet("pyry attach", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	stdioFlag := fs.Bool("stdio", false, "no-PTY byte forwarding for SDK consumers")
	cimFlag := fs.Bool("create-if-missing", false, "create the session if the supplied UUID is not registered")
	if err := fs.Parse(args); err != nil {
		return "", false, false, err
	}
	sel, err := attachSelectorFromArgs(fs.Args())
	if err != nil {
		return "", false, false, err
	}
	return sel, *stdioFlag, *cimFlag, nil
}

// errTooManyAttachArgs is returned by attachSelectorFromArgs when more than
// one positional follows the recognised -pyry-* flags. runAttach turns this
// into a usage line + os.Exit(2); the helper exists separately so the
// argument-shape rule is unit-testable without intercepting os.Exit.
var errTooManyAttachArgs = errors.New("too many arguments")

// attachSelectorFromArgs returns the session selector string from the
// post-flag remainder. Empty rest → "" (bootstrap). One arg → that arg
// passed through verbatim — no trimming, no UUID parsing, no prefix logic.
// More than one → errTooManyAttachArgs.
func attachSelectorFromArgs(rest []string) (string, error) {
	switch len(rest) {
	case 0:
		return "", nil
	case 1:
		return rest[0], nil
	default:
		return "", errTooManyAttachArgs
	}
}

// runAttach implements `pyry attach [--stdio] [<id>]`: connect to a running
// daemon's control socket, hand stdin/stdout over to a supervised claude
// session. The optional <id> selects the session — full UUID or unique
// prefix; omitted means the bootstrap session.
//
// Default mode allocates a PTY on the client side and prints
// "pyry: attached…" / "pyry: detached." human-affordance lines; press
// Ctrl-B d to detach (leaves pyry running).
//
// `--stdio` is the no-PTY mode for SDK consumers (stream-json, tooling):
// stdin/stdout are bridged as raw bytes through the same wire protocol,
// no raw mode, no SIGWINCH, no escape detection, no stderr noise. EOF on
// stdin ends the attach cleanly; the session stays alive.
func runAttach(args []string) error {
	socketPath, rest, err := parseClientFlags("pyry attach", args)
	if err != nil {
		return err
	}

	sessionID, stdio, createIfMissing, err := parseAttachArgs(rest)
	if err != nil {
		fmt.Fprintln(os.Stderr, "pyry attach: "+err.Error())
		fmt.Fprintln(os.Stderr, "usage: pyry attach [flags] [--stdio] [--create-if-missing] [<id>]")
		os.Exit(2)
	}

	if stdio {
		if err := control.AttachStdio(context.Background(), socketPath, sessionID, os.Stdin, os.Stdout, createIfMissing); err != nil {
			return fmt.Errorf("attach: %w", err)
		}
		return nil
	}

	// Read local terminal geometry so the supervised claude knows the
	// initial window size.
	cols, rows := 0, 0
	if term.IsTerminal(int(os.Stdout.Fd())) {
		w, h, err := term.GetSize(int(os.Stdout.Fd()))
		if err == nil {
			cols, rows = w, h
		}
	}

	fmt.Fprintln(os.Stderr, "pyry: attached. Press Ctrl-B d to detach.")
	if err := control.Attach(context.Background(), socketPath, cols, rows, sessionID, createIfMissing); err != nil {
		return fmt.Errorf("attach: %w", err)
	}
	fmt.Fprintln(os.Stderr, "\npyry: detached.")
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
