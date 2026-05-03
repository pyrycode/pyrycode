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
//	pyry install-service  Write a systemd / launchd unit file for pyry
//	pyry help             Show help
//
// See https://github.com/pyrycode/pyrycode for documentation.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/pyrycode/pyrycode/internal/control"
	"github.com/pyrycode/pyrycode/internal/install"
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
		case "install-service":
			return runInstallService(os.Args[2:])
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
	"pyry-claude":       true,
	"pyry-workdir":      true,
	"pyry-socket":       true,
	"pyry-name":         true,
	"pyry-idle-timeout": true,
	"pyry-active-cap":   true,
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
	idleTimeout := fs.Duration("pyry-idle-timeout", 15*time.Minute, "evict idle claudes after this duration (0 disables)")
	activeCap := fs.Int("pyry-active-cap", 0, "max concurrently active claudes (0 = uncapped)")
	if err := fs.Parse(pyryArgs); err != nil {
		return err
	}
	socketPath := resolveSocketPath(*socketFlag, *name)
	registryPath := resolveRegistryPath(*name)
	claudeSessionsDir := resolveClaudeSessionsDir(*workdir)

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

	pool, err := sessions.New(sessions.Config{
		Logger:            logger,
		RegistryPath:      registryPath,
		ClaudeSessionsDir: claudeSessionsDir,
		IdleTimeout:       *idleTimeout,
		ActiveCap:         *activeCap,
		Bootstrap: sessions.SessionConfig{
			ClaudeBin:  *claudeBin,
			WorkDir:    *workdir,
			ResumeLast: *resume,
			ClaudeArgs: claudeArgs,
			Bridge:     bridge,
		},
	})
	if err != nil {
		return fmt.Errorf("pool init: %w", err)
	}

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

// parseClientFlags handles the shared flags every control verb accepts:
// -pyry-name (instance name → ~/.pyry/<name>.sock) and -pyry-socket (explicit
// path that overrides the name). Returns the resolved socket path and any
// positionals after the recognised flags. Verbs that don't take positionals
// can bind rest to _ — same silent-ignore behaviour as before.
func parseClientFlags(name string, args []string) (socketPath string, rest []string, err error) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	nameFlag := fs.String("pyry-name", defaultName(), "instance name (socket: ~/.pyry/<name>.sock)")
	socketFlag := fs.String("pyry-socket", "", "explicit socket path (overrides -pyry-name)")
	if err := fs.Parse(args); err != nil {
		return "", nil, err
	}
	return resolveSocketPath(*socketFlag, *nameFlag), fs.Args(), nil
}

// runStatus implements the `pyry status` subcommand: dial the control socket,
// fetch a status snapshot, pretty-print it.
func runStatus(args []string) error {
	socketPath, _, err := parseClientFlags("pyry status", args)
	if err != nil {
		return err
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
	socketPath, _, err := parseClientFlags("pyry logs", args)
	if err != nil {
		return err
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

// runAttach implements `pyry attach [<id>]`: connect to a running daemon's
// control socket, hand the local terminal over to a supervised claude
// session. The optional <id> selects the session — full UUID or unique
// prefix; omitted means the bootstrap session. Press Ctrl-B d to detach
// (leaves pyry running).
func runAttach(args []string) error {
	socketPath, rest, err := parseClientFlags("pyry attach", args)
	if err != nil {
		return err
	}

	sessionID, err := attachSelectorFromArgs(rest)
	if err != nil {
		fmt.Fprintln(os.Stderr, "pyry attach: too many arguments")
		fmt.Fprintln(os.Stderr, "usage: pyry attach [flags] [<id>]")
		os.Exit(2)
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
	if err := control.Attach(context.Background(), socketPath, cols, rows, sessionID); err != nil {
		return fmt.Errorf("attach: %w", err)
	}
	fmt.Fprintln(os.Stderr, "\npyry: detached.")
	return nil
}

// runStop implements `pyry stop`: dial the control socket and ask the daemon
// to shut down. Returns when the server has acknowledged — the daemon may
// still be unwinding its child.
func runStop(args []string) error {
	socketPath, _, err := parseClientFlags("pyry stop", args)
	if err != nil {
		return err
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
	workdir := fs.String("workdir", "", "WorkingDirectory baked into the unit (default: ~/pyry-workspace)")
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

	path, resolved, err := install.Install(install.Options{
		Platform:   plat,
		Name:       *name,
		WorkDir:    *workdir,
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
  pyry attach [flags] [<id>]                     attach local terminal to daemon
                                                  (Ctrl-B d to detach; <id>
                                                  selects a session — full
                                                  UUID or unique prefix; omit
                                                  for the bootstrap session)
  pyry install-service [flags] [-- claude-args]  write a systemd or launchd
                                                  unit file for pyry
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
  -pyry-idle-timeout    evict idle claudes after this duration (default 15m;
                        0 disables; respawn latency 2-15s on next attach)

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
