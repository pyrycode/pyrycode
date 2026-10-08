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
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/pyrycode/pyrycode/internal/config"
	"github.com/pyrycode/pyrycode/internal/control"
	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/debugbundle"
	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/msgqueue"
	"github.com/pyrycode/pyrycode/internal/permbridge"
	"github.com/pyrycode/pyrycode/internal/relay"
	"github.com/pyrycode/pyrycode/internal/sessions"
)

// Version is set at build time via -ldflags "-X main.Version=...".
var Version = "dev"

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
		case "conversation":
			return runConversation(args[2:])
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
//
// codex carries the Codex binary and home; its sink is set here to the same
// fan-in the Claude factory feeds (#2620), and its approval to the same
// registry, window and modal surface (#2587).
func selectInteractiveRunner(cfg config.Config, logger *slog.Logger, mcpServersPath string, vocab *modelVocabularyStore, approval streamApprovalConfig, codex codexHarness) (sessions.RunnerFactory, *streamTurnSink, error) {
	switch cfg.InteractiveRunner {
	case "", "stream-json":
		sink := newStreamTurnSink(0, logger)
		codex.sink = sink
		codex.approval = approval
		codex.vocab = vocab
		return harnessRunnerFactory(newStreamRunnerFactory(sink, mcpServersPath, vocab, approval), newCodexRunnerFactory(codex)), sink, nil
	case "pty":
		return nil, nil, fmt.Errorf(`interactive_runner "pty" was removed in #1348: the terminal-driving interactive runner no longer exists. Remove the key or set it to "stream-json"`)
	default:
		return nil, nil, fmt.Errorf("interactive_runner %q not recognized (accepted: \"\", \"stream-json\")", cfg.InteractiveRunner)
	}
}

// harnessRunnerFactory is the factory the daemon wires into the pool: it selects
// each session's runner by RunnerConfig.Harness (#2593). claude, and the empty
// value a RunnerConfig built outside the pool carries, get the claude factory;
// codex gets the codex factory (#2620). Any other harness is refused as a
// construction error without calling either, so no runner is ever built for a
// session recorded as an agent it cannot drive: the pool leaves that session
// dormant and the daemon keeps running.
func harnessRunnerFactory(claude, codex sessions.RunnerFactory) sessions.RunnerFactory {
	return func(cfg sessions.RunnerConfig) (sessions.Runner, error) {
		switch cfg.Harness {
		case "", sessions.HarnessClaude:
			return claude(cfg)
		case harnessCodex:
			return codex(cfg)
		default:
			return nil, fmt.Errorf("no runner for harness %q", cfg.Harness)
		}
	}
}

// checkDebugCapture rejects debug_capture: true. The flag switched on a .cast
// recorder attached to the terminal spawn path, and #1348 deleted that path with
// the recorder in it, so an accepted opt-in would record nothing and the
// operator would learn it only from an empty debug bundle. Like the "pty" arm
// of selectInteractiveRunner, the error names the removal and the edit to make.
// false and an absent key return nil and log nothing. config.Load stays
// parse-only: this runs at the composition root, right after it.
func checkDebugCapture(cfg config.Config) error {
	if !cfg.DebugCapture {
		return nil
	}
	return fmt.Errorf(`debug_capture was removed in #1348: the terminal session recorder no longer exists, so nothing would be captured. Remove the key or set it to false`)
}

// runSupervisor starts the supervisor and the control server together, blocks
// until the context is cancelled by SIGINT/SIGTERM, then drains both.
func runSupervisor(args []string, deliveryFactory ...channelDeliveryFactory) error {
	// Tests can pause delivery I/O without replacing the composition root.
	makeDelivery := newChannelDelivery
	if len(deliveryFactory) != 0 {
		makeDelivery = deliveryFactory[0]
	}
	pyryArgs, claudeArgs := splitArgs(args)

	fs := flag.NewFlagSet("pyry", flag.ContinueOnError)
	claudeBin := fs.String("pyry-claude", "claude", "path to the claude binary")
	codexBin := fs.String("pyry-codex", "codex", "path to the codex binary")
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
	autoUpdate := fs.Bool("pyry-auto-update", false, "install a new release by itself once the daemon is idle, then restart the managed unit")
	accountSource := fs.String(claudeAccountFlagName, "", "Claude account token source for this instance: an absolute path to an owner-only token file, or an op:// 1Password reference (default: $"+claudeAccountSourceEnv+" or ~/.pyry/<name>/"+claudeAccountFileName+")")
	accountOpCLI := fs.String(claudeAccountOpCLIFlagName, "", "1Password CLI an op:// account source runs: one executable name on PATH or one absolute path (default: $"+claudeAccountOpCLIEnv+", the op_cli key of ~/.pyry/<name>/"+claudeAccountFileName+", or op)")
	var readFolderEntries folderList
	fs.Var(&readFolderEntries, "pyry-read-folder", "absolute folder the file reader may also open; repeatable")
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
	workspaceBase := resolveStartupWorkspaceBase(os.Getwd, logger)
	// Rows recorded before #2568 carry the client's spelling of their folder —
	// relative or "~"-prefixed — beside rows holding its realpath, so one folder
	// shows as several workspaces. Rewrite them once, before anything else reads
	// the registry, with the strict resolver: it neither creates a folder nor
	// trust-marks one, and a row it cannot resolve is left as it was.
	normaliseLegacyCwds(convReg, convRegistryPath, resolveWorkspaceDir, logger)
	// The operator-named folders the file reader may open besides a
	// conversation's workspace (#2710), resolved ONCE here so every reader request
	// checks against the same canonical roots. A bad entry is skipped with a
	// warning and the daemon still starts.
	readFolders := resolveReadFolders(readFolderEntries, logger)
	// The daemon's own working folder is always readable too (#2720), unless it
	// is the home folder or the root, which withWorkdirReadFolder refuses with
	// one startup line.
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("resolve home directory: %w", err)
	}
	readFolders = withWorkdirReadFolder(readFolders, workdirReal, home, logger)

	// Two-layer shutdown context so a shutdown's ORIGIN survives to the exit
	// classification below. The signal layer handles SIGTERM/SIGINT (operator
	// stop → inherited signal cause → exit 0). The cause layer lets a
	// self-initiated fatal path (a persistent 4409 server-id conflict) cancel
	// WITH an error, which fatalCause turns into a non-zero exit so launchd
	// restarts the daemon
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
	if err := checkDebugCapture(cfg); err != nil {
		return err
	}
	// The instance's Claude account source (#2824) is fixed until restart; the
	// token bytes are re-read on every Claude launch attempt. An unusable source
	// stops startup like interactive_runner and debug_capture do, while a failed
	// read only refuses Claude launches and leaves the daemon running.
	account, err := newClaudeAccount(*accountSource, os.Getenv(claudeAccountSourceEnv), *accountOpCLI, os.Getenv(claudeAccountOpCLIEnv), resolveInstanceDirPath(*name), logger)
	if err != nil {
		return err
	}
	account.prime(ctx)
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
	// every spawn is yolo. Only a "pty" value skips the write, and that value fails
	// startup in selectInteractiveRunner just below, so no factory is ever built
	// over an empty mcpServersPath.
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
	// fails fast (AC4, no silent fallback). Every accepted value ("" and
	// "stream-json") returns both, so past the error check neither is nil. The same
	// sink instance is threaded two ways: RunnerFactory (below) and
	// relayWiring.streamSink (the drain); the factory also carries mcpServersPath
	// to inject the approval-tool flags (#1168).
	// One registry and one window serve both Claude-facing transports. The stdio
	// handler needs them when the runner factory is built; the MCP control server
	// receives the same values after the relay leg has been composed.
	approvals := permbridge.New()
	approvalWindow := approvalTimeout()
	approvalSurfaces := &approvalSurfaceReport{}
	codex := codexHarness{bin: *codexBin, home: codexHomePath(resolveInstanceDirPath(*name))}
	runnerFactory, streamSink, err := selectInteractiveRunner(cfg, logger, mcpServersPath, modelVocabulary, streamApprovalConfig{
		stdio:    cfg.StdioPermissionPrompt,
		registry: approvals,
		timeout:  approvalWindow,
		surface:  approvalSurfaces,
		account:  account.provider(),
	}, codex)
	if err != nil {
		return fmt.Errorf("interactive runner: %w", err)
	}
	// Codex's model families are read once here, off the startup path (#2664), so
	// the model menu offers Codex before any Codex session has spawned. ctx is
	// sigCtx's child that `pyry stop` also cancels. The deferred wait runs before
	// modelVocabulary.Close, so no retention lands on a closed store.
	codex.vocab = modelVocabulary
	defer startCodexModelRead(ctx, codex, trustedWorkdir, logger)()
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
	// stays byte-identical to the condition relayWiring.streamSink documents. Since
	// #1348 streamSink is always non-nil here, so the tracker is always built; the
	// nil arm is unreachable from this composition root. The guard stays, and a nil
	// tracker (a test wiring) makes every method reached from either consumer a
	// nil-receiver no-op.
	//
	// withExitEpoch is what ARMS the stale-exit guard (#1483): it binds the very
	// fan-in whose exit lane the tracker's clear is ordered against, which is in
	// scope here and nowhere else the tracker is built. Dropping it compiles, passes
	// every test and silently restores the pre-#1483 behaviour — a stale exit
	// clearing a mark opened on the respawned child — so it is not an optional knob
	// on this call, only on the ~36 test constructions that want the incumbent
	// semantics.
	//
	// sessionTurnBusy is the same tracker seen from the pool (#1486): an idle-timer
	// fire on a session whose conversation has a turn open re-arms instead of
	// killing the turn. It hands the pool one bool per session and keeps the
	// conversation id on this side of the seam. Built under the same guard: had the
	// tracker been nil it would stay a nil func rather than a closure over the nil
	// tracker, so the pool would see no signal at all.
	var turnBusy *turnBusyTracker
	var sessionTurnBusy func(sessions.SessionID) bool
	var sessionRunnerStopped func(sessions.SessionID)
	if streamSink != nil {
		turnBusy = newTurnBusyTracker(
			func(sid string) (string, bool) { return conversationForSession(convReg, sid) }, logger,
			withExitEpoch(streamSink.exitEpoch),
			withLifecycleClose(streamSink.requestLifecycleClose))
		sessionTurnBusy = func(id sessions.SessionID) bool {
			convID, ok := conversationForSession(convReg, string(id))
			return ok && turnBusy.Busy(convID)
		}
		// A whole runner can stop after its last child exit was already offered.
		// Stamp a confirmed stop before the pool permits reactivation, so an early
		// eviction hold always has a retained boundary after its queued producer tail.
		sessionRunnerStopped = func(id sessions.SessionID) { streamSink.runnerStopped(string(id)) }
	}
	pool, err := sessions.New(sessions.Config{
		Logger:                    logger,
		RegistryPath:              registryPath,
		ClaudeSessionsDir:         claudeSessionsDir,
		IdleTimeout:               *idleTimeout,
		TurnBusy:                  sessionTurnBusy,
		OnRunnerStopped:           sessionRunnerStopped,
		ActiveCap:                 *activeCap,
		ConversationsRegistry:     convReg,
		ConversationsRegistryPath: convRegistryPath,
		ReadFolders:               readFolders,
		DefaultModel:              sessions.ClaudeSettingsModel,
		SweepInterval:             *convSweepInterval,
		RunnerFactory:             runnerFactory,
		Bootstrap: sessions.SessionConfig{
			ClaudeBin:  *claudeBin,
			WorkDir:    trustedWorkdir,
			ResumeLast: *resume,
			ClaudeArgs: claudeArgs,
		},
	})
	if err != nil {
		return fmt.Errorf("pool init: %w", err)
	}

	// Claim instance ownership before loading pending posts or starting relay
	// inbound delivery. Listen only binds; Serve waits until all hooks are wired.
	ctrl := control.NewServer(socketPath, poolResolver{pool}, logRing, func() { cancelCause(nil) }, logger, pool)
	if err := ctrl.Listen(); err != nil {
		return fmt.Errorf("control listen: %w", err)
	}
	// Serve's cancellation watcher releases ownership, so its context must outlive
	// the daemon's writers. This defer runs after their cancel/seal/join defers.
	controlCtx, controlCancel := context.WithCancel(context.WithoutCancel(ctx))
	var ctrlDone chan error
	controlJoined := false
	defer func() {
		cancelCause(nil)
		controlCancel()
		_ = ctrl.Close()
		if ctrlDone != nil && !controlJoined {
			<-ctrlDone
		}
	}()

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
	// blocked is the session_error notify closure (a non-blocking, drop-on-full
	// send into giveUps), and msgqueue's give-up path is its only caller, as
	// OnGiveUp. #1014 gave it a second one, the modal resolver surfacing a
	// folder-not-trusted session_error on a trust deny; that sender was removed
	// deliberately (#1545), because trust is settled by trustMark before every
	// spawn and no trust modal can reach the resolver since #1348.
	blocked := sessionErrorNotify(giveUps, logger)
	// The second sender into giveUps (#2724): a stream runner whose claude keeps
	// exiting at startup reports its crash episode as a non-terminal
	// session.child_crashing. Installed on the sink after the pool exists because the
	// factory captured the sink before this channel did; it is set before pool.Run,
	// and the sink loads it atomically at fire time.
	streamSink.setCrashLoopNotify(childCrashingNotify(giveUps, func(sid string) (string, bool) {
		return conversationForSession(convReg, sid)
	}, logger))
	// approvalParked is the third value in this block built BEFORE msgqueue.New for
	// the same chicken-and-egg reason as queueChanges and giveUps: it carries #1919's
	// ApprovalParked report to the Pending gate below, and the bridge that answers
	// the report is constructed inside startRelayV2, well after this queue. Unlike
	// the two channels it cannot be a channel — the gate needs an answer, not a
	// notification — so it is the one late-bound field here, threaded to its single
	// setter through relayWiring.approvalParked. Left unset (before the relay leg
	// wires it, or when it never does) it reports negative for every conversation,
	// which is the pre-#1911 behaviour exactly.
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
	reconcileStartupHistory(conversationHistory, convReg, logger, time.Now().UTC())
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
	// Read a fresh pending snapshot after ownership and before inbound delivery.
	postDelivery, err := makeDelivery(filepath.Join(resolveInstanceDirPath(*name), "channel-delivery.json"), conversationHistory, postCarry.record, logger)
	if err != nil {
		return err
	}
	defer postDelivery.stopAccepting()
	postDelivery.sessionFor = channelPostSession(convReg, pool.HarnessFor)
	postDelivery.bind(turnBusy)
	// operatorMessages is the hand-off from the history producer below to the
	// live push of the operator's own message (#2699), built BEFORE msgqueue.New
	// for queueChanges' reason. The ring that push appends to is born in the relay
	// leg, which hands it to the emitter's Run at start.
	operatorMessages := make(chan operatorMessage, operatorMessageQueueSize)
	// Queued Claude history entries and pushes wait for their echoes, which the stream drain hands over through the sink. Built only on the
	// stream path, beside the tracker whose idle is its fallback; nil elsewhere,
	// which commits at the write as before.
	var sendNowPlace *sendNowPlacement
	if turnBusy != nil {
		sendNowPlace = newSendNowPlacement(ctx,
			func(sid string) (string, bool) { return conversationForSession(convReg, sid) },
			turnBusy.WaitIdle)
		sendNowPlace.bindQueued(ctx, streamSink, conversationHistory, router.isClaude, logger)
		streamSink.setEchoObserver(sendNowPlace.echo)
	}
	// Native suggested-reply state (#2831), stream path only. Minted here because
	// its delivered-text hook is fixed in the msgqueue literal below; the relay leg
	// binds its broadcaster and starts its Run.
	var replySugg *replySuggestions
	var replySuggDelivered msgqueue.DeliveredFunc
	if streamSink != nil {
		replySugg = newReplySuggestions(logger)
		replySugg.parent = ctx
		defer replySugg.stopFallbacks()
		replySugg.fallback = (replyFallback{binary: *claudeBin, account: account.provider(), logger: logger}).run
		replySuggDelivered = replySugg.noteDelivered
	}
	// Zero keeps msgqueue's default; only an e2e_realclaude build can set it.
	giveUpAfter, err := queueGiveUpAfter()
	if err != nil {
		return err
	}
	queue, err := msgqueue.New(msgqueue.Config{
		// Recovery precedes carry, so the pending posted text is composed onto the payload
		// once, at the boundary with the queue, and markApprovalHolds stays adjacent to
		// the seam that produces the hold error it marks. The composed value goes no
		// further than newInboundDeliver's WriteUserTurn — see channelCarry on why the
		// "clients see no change" property is structural here rather than a filter.
		Deliver:  postDelivery.beforeInbound(postCarry.carryPending(approvalParked.markApprovalHolds(replySugg.trackDelivery(newInboundDeliver(router.resolve, turnBusy, streamTurnHoldTimeout, sendNowPlace))))),
		OnChange: queueStateNotify(queueChanges, logger),
		OnGiveUp: blocked,
		// History uses only the safe queued projection. Stream Claude writes
		// prepare it at the final write boundary and OnDelivered acknowledges
		// placement; other deliveries commit here. The channel carry is cleared
		// only on confirmation, never on a failed attempt or send-now delivery.
		OnDelivered: deliveredFuncs(
			newOperatorMessageHistory(conversationHistory, operatorMessageNotify(operatorMessages, logger), sendNowPlace, logger),
			postCarry.clearDelivered,
			replySuggDelivered,
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
		// #2729: send_queued_now writes a queued message into the running claude
		// turn. Deliberately NOT through carryPending or markApprovalHolds — see
		// newSendNowDeliver — and its OnDelivered carries SentNow so the carry's
		// clear leaves the waiting head's composed posts alone.
		SendNow:     newSendNowDeliver(router.resolve, router.isClaude, turnBusy, sendNowPlace),
		GiveUpAfter: giveUpAfter,
		Logger:      logger,
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

	// The live push of the operator's own delivered message (#2699): fed by the
	// history producer on OnDelivered, started inside startRelayV2 where the
	// broadcaster and the replay ring exist — same shape as qse.
	ome := newOperatorMessageEmitterV2(operatorMessages, logger)

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
	reset := newConversationReset(ctx, convReg, pool, turnBusy, queue, *wrapUpDeadlineFlag, logger)

	// The debug-bundle producer (#813): a paired `request_debug_bundle` frame
	// assembles the daemon-global bundle — the recent log ring plus the newest
	// terminal recording — as one in-memory archive and streams it back over the
	// encrypted v2 channel. The recordings dir is resolved independent of the
	// DebugCapture flag, which is now rejected at startup (#1514): no recorder has
	// written there since #1348, but a recording left from before then stays
	// readable, and Assemble marks the recording absent when the dir is empty or
	// holds none. Assemble makes zero log calls and the archive bytes
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

	relayCleanup, approvalSurface, announceAttachment, announceConversation, announcePost, pairingProvider, err := startRelay(ctx, logger, relayWiring{
		instanceName:     *name,
		relayURL:         relayURL,
		version:          Version,
		allowInsecure:    allowInsecure,
		shutdown:         cancelCause,
		convReg:          convReg,
		hostSystemPrompt: pool,
		claudeAccount:    account,
		workspaceBase:    workspaceBase,
		readFolders:      readFolders,
		creator:          sessionMinter{pool, modelVocabulary},
		router:           router,
		queue:            queue,
		active:           active,
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
			rotate:            pool.RotateForNewSession,
			rotateWithHandoff: pool.RotateForNewSessionWithHandoff,
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
			reset: reset,
			// #2478: the same emitter the relay leg attaches its broadcaster to, so the
			// tail that orders the three edges and the producer that puts them on the
			// wire are one object rather than two that could disagree.
			resetting: resetting,
			log:       logger,
		},
		agentSwitcher: relayAgentSwitcher{switcher: conversationAgentSwitcher{pool: pool, conversations: convReg, registryPath: convRegistryPath, reset: reset, history: conversationHistory, resetting: resetting, saved: modelVocabulary}},
		defaultCwd:    defaultCwd,
		transitions:   pool,
		// #2148: the relay leg hands back its open-conn enumerator, and this closure
		// — the only place that names both packages — maps it onto the pool's
		// resolver. The three ActiveConn fields cross as untrusted text and are judged
		// nowhere on this path; sessions.admitClient is the single door.
		setClientIdentity: func(enum func(context.Context) []relay.ActiveConn) {
			pool.SetClientIdentityResolver(func(ctx context.Context) []sessions.ClientIdentity {
				conns := enum(ctx)
				out := make([]sessions.ClientIdentity, 0, len(conns))
				for _, c := range conns {
					out = append(out, sessions.ClientIdentity{Name: c.DeviceName, Version: c.ClientVersion, Features: c.ClientFeatures})
				}
				return out
			})
		},
		qse:          qse,
		sessionErr:   see,
		resetting:    resetting,
		debugBundler: debugBundler,
		settings:     settingsUpdaterAdapter{pool, modelVocabulary},
		// #2699: pushes each delivered operator message; Run starts in startRelayV2.
		operatorMessages: ome,
		// #2646: the capability list is read off an adapter over the same pool and
		// store as settings above, so it reports exactly what that one checks.
		capabilities: settingsUpdaterAdapter{pool, modelVocabulary}.Capabilities,
		runSettings:  runSettingsFor(convReg, pool, modelVocabulary),
		// The registry half and the live-session half of one conversation's
		// system-prompt picture (#2152), resolved together over the same registry and
		// pool. The closure returns cmd/pyry's own primitive-typed state.
		promptState: func(convID string) (conversationPromptState, bool) {
			return resolveConversationPrompt(convReg, pool, convID)
		},
		modelWindows: sessionModelWindows(pool),
		// The folder half of the same reading (#2423), built beside the windows half
		// over the same pool and gated on the daemon's own sessions directory: with
		// none, the conversation-keyed usage seam stays unwired exactly as before.
		sessionTranscriptDir: sessionTranscriptDir(pool, claudeSessionsDir),
		sessionHarness:       sessionHarness(pool),
		// The conversation-keyed half of the model-list pair (#2125), built beside its
		// enumerating twin below over the same registry and pool.
		modelListFor: modelListFor(convReg, pool, modelVocabulary),
		// The pushed-frame half of the merged list (#2652), over the same store.
		pushedModelOptions: pushedModelOptions(modelVocabulary),
		mcpStatusFor:       mcpStatusFor(convReg, pool),
		effectiveEffortFor: effectiveEffortFor(convReg, pool),
		memorySearchFor:    memorySearchFor(convReg, pool, resolveConfigPath()),
		// The resolution half of the on-demand context-usage read (#2431), built
		// beside its MCP twin over the same registry and pool. The collapsing and
		// mid-turn-deferral half is composed in startRelayV2, which holds the turn
		// tracker and the daemon context.
		contextUsageResolve:           contextUsageResolve(convReg, pool),
		mcpActuatorFor:                boundMCPChildActuator(convReg, pool),
		backgroundTaskStopper:         boundBackgroundTaskStopper(convReg, pool),
		retainedModelLists:            retainedModelLists(convReg, pool, modelVocabulary),
		retainedSlashCommandLists:     retainedSlashCommandLists(convReg, pool),
		retainedBackgroundTaskRosters: retainedBackgroundTaskRosters(convReg, pool),
		approvals:                     approvals,
		streamSink:                    streamSink,
		suggestions:                   replySugg,
		busy:                          turnBusy,
		hist:                          conversationHistory,
		postDelivery:                  postDelivery,
		approvalParked:                approvalParked,
	})
	if err != nil {
		return fmt.Errorf("relay start: %w", err)
	}
	approvalSurfaces.set(approvalSurface)
	// Delivery runs even when startRelay returned without a relay URL.
	postDelivery.announce = announcePost
	postDeliveryDone := make(chan struct{})
	go func() { postDelivery.run(ctx); close(postDeliveryDone) }()
	defer func() {
		cancelCause(nil)
		postDelivery.stopAccepting()
		<-postDeliveryDone
	}()
	// Cancel-then-join: relayCleanup joins producer drains whose Run loops
	// return only on ctx.Done, so the daemon ctx must already be cancelled when
	// it runs. Defers are LIFO, so the `defer cancelCause(nil)` registered at the
	// top of runSupervisor runs AFTER this one — too late to unblock them. Cancel
	// here instead, so an error return between startRelay and the end of
	// runSupervisor takes the same
	// ordering the normal shutdown path already takes, rather than wedging with
	// the unblocking cancel queued behind the block (#1492). Cause contexts are
	// first-cause-wins, so a 4409 already recorded by startRelay's conn.Wait
	// classifier survives this nil and fatalCause still reports it.
	defer func() {
		cancelCause(nil)
		relayCleanup()
	}()

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
	// Local creation is available even when startRelay installed no announcement hook.
	ctrl.SetConversationCreator(conversationCreator(convReg, sessionMinter{pool, modelVocabulary}, convRegistryPath, announceConversation, logger))
	ctrl.SetConversationSubmitter(conversationSubmitter(convReg, router.resolve, queue.Enqueue, convRegistryPath, logger))
	// Give a new host its starting point (#2569) through that same creator, so
	// the General channel is confined, trust-marked and bound exactly as
	// `pyry channel new` would make it. It waits for pool.Ready: pool.Run blocks
	// below, and a Mint before it runs persists a session and then fails, which
	// would orphan one session per start. Joined after pool.Run returns.
	seedDone := seedWhenReady(ctx, pool.Ready(), func() {
		seedDefaultWorkspace(convReg, convRegistryPath, workspaceBase, createChannel, logger)
	})
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
	ctrl.SetChannelPoster(postDelivery.guardPoster(channelPoster(convReg, createChannel, defaultCwd, postDelivery.accept, logger)))
	// Explicit requests always have a provider; the flag controls scheduling only.
	// A nil tracker fails closed rather than inferring idle from missing signals.
	au, err := newAutoUpdater(func() bool {
		infos := pool.List()
		lastActive := make([]time.Time, len(infos))
		for i, s := range infos {
			lastActive[i] = s.LastActiveAt
		}
		return daemonIdle(turnBusy == nil || turnBusy.AnyBusy(), lastActive, time.Now(), autoUpdateQuietWindow)
	}, logger)
	if err != nil {
		return err
	}
	ctrl.SetUpdateWhenIdleProvider(func() (control.UpdateWhenIdleResult, error) { return au.request(ctx) })
	ctrlDone = make(chan error, 1)
	go func() { ctrlDone <- serveControlWhenReady(ctx, pool.Ready(), controlCtx, ctrl) }()

	logger.Info("pyrycode starting",
		"version", Version,
		"name", *name,
		"claude", *claudeBin,
		"socket", socketPath,
	)
	auDone := make(chan error, 1)
	if *autoUpdate {
		go func() { auDone <- au.Run(ctx) }()
	} else {
		auDone <- nil
	}

	runErr := pool.Run(ctx)
	// Pool.Run can return with the daemon ctx still LIVE: any early non-ctx error
	// out of its errgroup returns from a ctx DERIVED from ours, so cancelling that
	// one does not cancel this one. Queue.Run blocks on ctx.Done before joining
	// its drains, so the <-qDone join below would never complete (#1492). Cancel
	// first — on the shutdown paths that reach here today (signal, `pyry stop`, a
	// fatal 4409) ctx is already cancelled and this is a no-op that cannot
	// displace the recorded cause.
	cancelCause(nil)

	// Keep ownership through post callback quiescence and consumer completion.
	// Then stop control handlers before joining their auto-update workers.
	postDelivery.stopAccepting()
	<-postDeliveryDone
	controlCancel()
	_ = ctrl.Close()
	<-ctrlDone
	controlJoined = true

	// Join the inbound-queue lifecycle: ctx is cancelled by the time we get here
	// (either before pool.Run returned or by the cancelCause above), so queue.Run
	// has observed ctx.Done and is winding down its drains. Waiting here keeps the
	// daemon from exiting while a drain goroutine is still in flight.
	<-qDone
	<-seedDone
	<-auDone
	au.join()

	if runErr != nil && !errors.Is(runErr, context.Canceled) {
		return fmt.Errorf("supervisor: %w", runErr)
	}
	// A clean ctx-cancel got us here. Distinguish WHY: a self-initiated fatal
	// shutdown (persistent 4409) cancelled with an error cause, so exit
	// non-zero and let launchd restart the daemon. An operator stop (SIGTERM,
	// `pyry stop`) carries the signal parent's cause or context.Canceled and
	// stays down at exit 0.
	if cause := fatalCause(ctx, sigCtx); cause != nil {
		logger.Error("pyrycode fatal shutdown", "cause", cause)
		return cause
	}
	logger.Info("pyrycode stopped")
	return nil
}

// serveControlWhenReady prevents requests from minting sessions before the
// pool's supervisor handle is wired. Startup cancellation ends the wait, while
// serving uses the detached control context to retain ownership until writers stop.
func serveControlWhenReady(startupCtx context.Context, ready <-chan struct{}, controlCtx context.Context, ctrl *control.Server) error {
	select {
	case <-ready:
		return ctrl.Serve(controlCtx)
	case <-startupCtx.Done():
		return startupCtx.Err()
	}
}

// fatalCause reports the self-initiated fatal shutdown reason carried by the
// daemon's cause context, or nil for an operator-initiated stop. A shutdown
// via `pyry stop` records context.Canceled; SIGTERM/SIGINT inherit sigCtx's
// signal cause. A self-initiated fatal path (the relay's persistent 4409
// handler) records a distinct error. Compare causes rather than sigCtx.Err:
// a fatal cause recorded before a later signal must survive. Returning it makes
// runSupervisor exit non-zero so launchd (KeepAlive SuccessfulExit:false)
// restarts the daemon; the operator paths return nil and stay down. Pure so it
// can be unit-tested directly (nil / cancelled / signal / fatal cause).
func fatalCause(ctx, sigCtx context.Context) error {
	cause := context.Cause(ctx)
	if cause == nil || errors.Is(cause, context.Canceled) || errors.Is(cause, context.Cause(sigCtx)) {
		return nil
	}
	return cause
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
  pyry conversation new [--type chat|channel] [--name <label>]
                        [--model MODEL] [--effort EFFORT]
                                                create in the current directory
                                                  (default: unnamed chat) and print id
  pyry conversation post --id ID (--text TEXT | --file PATH)
                                                submit a user turn to an existing
                                                  conversation (queue acceptance)
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
  pyry update [daemon flags] --when-idle        request the daemon's next idle update
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
  -pyry-codex string    path to the codex binary (default "codex")
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
  -pyry-read-folder path  an absolute folder the in-app file reader may
                        also open besides the conversation's workspace;
                        repeat for several (a bad entry is skipped with a warning)

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
