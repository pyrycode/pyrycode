package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"slices"

	"github.com/pyrycode/pyrycode/internal/config"
	"github.com/pyrycode/pyrycode/internal/contextwindow"
	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/devices"
	"github.com/pyrycode/pyrycode/internal/dispatch"
	"github.com/pyrycode/pyrycode/internal/identity"
	"github.com/pyrycode/pyrycode/internal/keys"
	"github.com/pyrycode/pyrycode/internal/modalbridge"
	"github.com/pyrycode/pyrycode/internal/msgqueue"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/relay"
	"github.com/pyrycode/pyrycode/internal/relay/handlers"
	"github.com/pyrycode/pyrycode/internal/supervisor"
)

// resolveRelayURL returns the first non-empty value among:
//  1. flagValue (from -pyry-relay)
//  2. envValue  (from PYRY_RELAY_URL)
//  3. cfg.RelayURL (from ~/.pyry/config.json — config.Load already
//     overlays DefaultConfig, so this leg covers both the operator
//     file and the built-in default)
//
// Returns "" only if all three are empty (config.Load's overlay makes
// that effectively unreachable in production).
func resolveRelayURL(flagValue, envValue string, cfg config.Config) string {
	if flagValue != "" {
		return flagValue
	}
	if envValue != "" {
		return envValue
	}
	return cfg.RelayURL
}

// resolveWorkspaceDir validates a paired client's requested workspace path for
// change_workspace (#823): expandTilde (leading "~"/"~/" → the daemon's $HOME,
// since a client cannot know the daemon's absolute home) then the STRICT
// confineWorkdirToHome (canonical realpath, confined to $HOME). It returns the
// realpath, or an error wrapping handlers.ErrWorkspaceRejected on any failure
// (escape after symlink resolution / unresolvable). It is injected as the
// change_workspace handler's WorkspaceResolver at both wiring sites below.
//
// Two deliberate differences from resolveSpawnDir, both load-bearing:
//
//   - Uses the STRICT confineWorkdirToHome, NOT confineWorkdirToHomeCreating.
//     change_workspace does not spawn and must not create a directory as a side
//     effect of a metadata edit; a non-existent target is "unresolvable"
//     (EvalSymlinks fails) → rejected, satisfying AC #3's "or is unresolvable"
//     branch for free. The desktop picker offers only existing folders; if the
//     folder is later needed, the next fresh spawn's confineWorkdirToHomeCreating
//     creates it.
//   - Does NOT call trustMark. Trust-marking auto-accepts claude's workspace-trust
//     modal and is a SPAWN concern; marking here would prematurely auto-trust a
//     dir that may never be spawned into. The next fresh spawn's resolveSpawnDir
//     trust-marks then. (Same reasoning #686 used to reuse confineWorkdirToHome,
//     not resolveSpawnDir.)
//
// Every failure — from expandTilde or confineWorkdirToHome — is wrapped with the
// sentinel so the handler sees it on every rejection and maps it uniformly to a
// non-retryable reply: there is no transient failure mode here, so every confine
// failure is deterministic and non-retryable. The confine detail is wrapped via
// %v for the wrapped-error chain; the handler never logs or echoes it (it names
// the offending path).
func resolveWorkspaceDir(requested string) (string, error) {
	expanded, err := expandTilde(requested)
	if err != nil {
		return "", fmt.Errorf("%w: %v", handlers.ErrWorkspaceRejected, err)
	}
	realpath, err := confineWorkdirToHome(expanded)
	if err != nil {
		return "", fmt.Errorf("%w: %v", handlers.ErrWorkspaceRejected, err)
	}
	return realpath, nil
}

// resolveWorkspaceFolder validates and creates a paired client's requested
// workspace folder for create_workspace_folder (#887): expandTilde(parent)
// (leading "~"/"~/" → the daemon's $HOME, since a client cannot know the daemon's
// absolute home) then filepath.Join(expandedParent, name) then the CREATING
// confineWorkdirToHomeCreating (canonical realpath, confined to $HOME, created if
// missing). It returns the created folder's realpath, or an error wrapping
// handlers.ErrWorkspaceFolderRejected on any failure. It is referenced directly as
// the create_workspace_folder handler's WorkspaceFolderResolver at both wiring
// sites below (no startRelay parameter threaded — like resolveWorkspaceDir, it has
// no external state). The caller (handler) has already validated `name` is a
// single clean path element, so the join lands the folder directly under parent.
//
// A HYBRID of resolveSpawnDir and resolveWorkspaceDir, both choices load-bearing:
//
//   - Uses confineWorkdirToHomeCreating (CREATING), like resolveSpawnDir and unlike
//     resolveWorkspaceDir's strict confiner — this verb's whole purpose is to create
//     the folder. The creating confiner's containment check runs BEFORE MkdirAll, so
//     an escaping target is rejected with nothing created (AC #2); MkdirAll is
//     idempotent (AC #5); it returns the post-creation EvalSymlinks realpath (AC #4).
//   - Does NOT call trustMark, like resolveWorkspaceDir and UNLIKE resolveSpawnDir.
//     Creating a folder is not spawning into it; trust-marking auto-accepts claude's
//     workspace-trust modal and is a SPAWN concern. Marking a folder that may never
//     host a session would prematurely auto-trust it. The eventual create_conversation
//     into this folder re-runs resolveSpawnDir → confineWorkdirToHomeCreating
//     (idempotent on the now-existing realpath) + trustMark then.
//
// Every failure — from expandTilde or confineWorkdirToHomeCreating — is wrapped with
// the sentinel so the handler maps it uniformly to a non-retryable reply. This folds
// a rare in-$HOME MkdirAll error (EACCES/ENOSPC) into the same non-retryable reject
// as an escape, exactly as resolveSpawnDir already does for its create path; the
// dominant, security-relevant failure is the escape, and a retry won't fix a full
// disk. The confine detail is wrapped via %v for the chain; the handler never logs
// or echoes it (it names the offending path).
func resolveWorkspaceFolder(parent, name string) (string, error) {
	expandedParent, err := expandTilde(parent)
	if err != nil {
		return "", fmt.Errorf("%w: %v", handlers.ErrWorkspaceFolderRejected, err)
	}
	created, err := confineWorkdirToHomeCreating(filepath.Join(expandedParent, name))
	if err != nil {
		return "", fmt.Errorf("%w: %v", handlers.ErrWorkspaceFolderRejected, err)
	}
	return created, nil
}

// relayWiring carries every call-site-supplied wiring value for the relay leg.
// It is populated with named fields at the single runSupervisor call site
// (cmd/pyry/main.go) and threaded unchanged from startRelay into startRelayV2, so
// each wiring argument is bound by field name rather than list position: a
// transposition of two same-typed fields (the two directory-path strings, the
// three bare-string identifiers, the two adjacent bools) becomes a visible
// named-field edit in the diff rather than a clean-compiling positional swap
// (#917). ctx/logger stay leading positional args on both functions (idiomatic
// Go), and the startRelayV2-only values startRelay produces internally (conn,
// registry, serverID) stay trailing positional args — they are not call-site
// wiring. startRelayV2 reads the subset of fields it needs; the config values it
// does not read (relayURL, version, allowInsecure, shutdown) still
// travel in the struct so they too are bound by name at the call site.
type relayWiring struct {
	// instanceName is the daemon instance name; it derives the per-instance
	// server-id, device-registry, conversations-registry, and static-key paths.
	instanceName string
	// relayURL is the resolved binary↔relay WebSocket URL. Empty disables the
	// relay leg entirely (startRelay returns a no-op cleanup).
	relayURL string
	// version is the binary version advertised to the relay on connect.
	version string
	// allowInsecure permits a ws:// (non-TLS) relay scheme
	// (PYRY_ALLOW_INSECURE_RELAY=1).
	allowInsecure bool
	// shutdown unwinds the daemon; called on a 4409 server-id conflict so the
	// relay leg does not reconnect-loop.
	shutdown context.CancelFunc
	// convReg is the conversations registry backing the list/create/rename/
	// delete/archive/change-workspace/recent handlers.
	convReg *conversations.Registry
	// creator mints a new session for the create_conversation handler.
	creator handlers.SessionCreator
	// router resolves the bound session for the send_message handler.
	router handlers.SessionRouter
	// queue is the live inbound message queue: send_message enqueues, and the
	// dequeue handler / queue_state reconcile read and mutate it.
	queue *msgqueue.Queue
	// active tracks the active-conversation cursor the interactive turn and modal
	// streams follow.
	active *activeConversation
	// boundHost resolves the host bound to the active conversation for the
	// interactive turn/modal streams.
	boundHost boundHostFunc
	// sup is the bootstrap session's supervisor — the keystroke/interrupt/
	// new-session/snapshot surface and the source of the daemon's own claude
	// child PID.
	sup *supervisor.Supervisor
	// bridge is the bootstrap session's PTY-output bridge. nil in foreground mode
	// disables the PTY-dependent streams (assistant-turn bridge, structured turn
	// stream, modal stream).
	bridge *supervisor.Bridge
	// claudeSessionsDir is the directory the rotation-following JSONL resolver
	// scans to tail the daemon's own claude child's transcript (turn stream #633,
	// snapshot-usage reader #857). Empty disables reconcile, the rotation watcher,
	// and the interactive turn/modal streams.
	claudeSessionsDir string
	// bootstrapIDFn returns the bootstrap session's pinned claude session id —
	// the SAME id source the bootstrap spawn's --session-id uses (#839) — so the
	// turn/modal stream resolvers tail the deterministic <id>.jsonl path instead
	// of the fd probe real claude defeats (#989). Nil or empty-returning falls
	// back to the probe (legacy unpinned spawns).
	bootstrapIDFn func() string
	// defaultCwd is the default workspace directory stamped onto conversations
	// created without an explicit cwd (the CreateConversation handler).
	defaultCwd string
	// transitions is the pool-side session-transition observer sink the
	// session_transition producer (#657) installs on.
	transitions transitionObserverSink
	// qse is the pre-built queue_state emitter (#722) whose Run goroutine
	// startRelayV2 starts over the v2 manager.
	qse *queueStateEmitterV2
	// sessionErr is the pre-built session_error emitter (#1008) whose Run goroutine
	// startRelayV2 starts over the v2 manager. Built at main.go (channel shared with
	// the msgqueue OnGiveUp seam) for the same chicken-and-egg reason as qse.
	sessionErr *sessionErrorEmitterV2
	// debugBundler assembles the daemon-global debug bundle for the
	// request_debug_bundle verb (#813). nil in foreground/v1 replies "unavailable".
	debugBundler func() ([]byte, error)
	// settings persists a per-session model/effort change for the
	// set_session_settings verb (#845). nil in foreground/v1 replies "unavailable".
	settings relay.SettingsUpdater
	// snapshotSettings reports the bootstrap session's persisted model/effort/YOLO
	// for the screen_snapshot reply (#848). nil reports defaults.
	snapshotSettings func() (model, effort string, yolo bool)
}

// startRelay opens the binary↔relay leg in a supervisor-owned goroutine.
// Returns a no-op cleanup and nil err when relayURL is empty (relay
// disabled — see operator note below). Otherwise loads the server-id,
// calls relay.Connect, and spawns one goroutine that:
//
//   - drains conn.Frames() (the v2 Noise manager consumes them)
//   - blocks on conn.Wait()
//   - on relay.ErrServerIDConflict: logs the conflict and calls shutdown()
//     to unwind the daemon (AC#3: no reconnect-loop on 4409)
//   - on any other terminal error: logs at warn (transport-internal
//     reconnect already handled non-fatal closes; reaching this path
//     means a genuinely unrecoverable transport error surfaced)
//   - on ctx.Err: logs at debug; returns
//
// The returned cleanup func is idempotent: it Close()s the connection
// and waits for the goroutine to drain so the daemon process does not
// exit while a WS handle is still in flight.
//
// startRelay does NOT swallow relay.Connect's synchronous errors
// (invalid scheme, missing identity). Those are programmer/config errors
// that should surface as a daemon startup failure — return wrapped, let
// runSupervisor fail fast. Lifecycle errors (post-Connect) flow through
// the goroutine.
func startRelay(
	ctx context.Context,
	logger *slog.Logger,
	w relayWiring,
) (cleanup func(), err error) {
	if w.relayURL == "" {
		logger.Info("relay: disabled (no URL configured)")
		return func() {}, nil
	}

	serverID, err := identity.LoadOrCreate(resolveServerIDPath(w.instanceName))
	if err != nil {
		return nil, fmt.Errorf("load server-id: %w", err)
	}

	// Load the device registry once at daemon startup. A missing file
	// (ENOENT) yields an empty registry — every phone rejects until
	// `pyry pair` runs. Malformed JSON fails fast.
	registry, err := devices.Load(resolveDevicesPath(w.instanceName))
	if err != nil {
		return nil, fmt.Errorf("load device registry: %w", err)
	}

	if w.allowInsecure {
		logger.Info("relay: PYRY_ALLOW_INSECURE_RELAY=1 — accepting ws:// scheme")
	}
	logger.Info("relay: connecting", "url", w.relayURL, "server_id", string(serverID))

	conn, err := relay.Connect(ctx, relay.Config{
		ServerID:            serverID,
		RelayURL:            w.relayURL,
		BinaryVersion:       w.version,
		Logger:              logger,
		AllowInsecureScheme: w.allowInsecure,
	})
	if err != nil {
		return nil, fmt.Errorf("relay connect: %w", err)
	}

	// legCleanup tears down the v2 Noise manager — the sole consumer of
	// conn.Frames() (ADR 024: v2 is a hard cutover, no mixed-mode path). The
	// shared waitDone classifier below is appended to it in the returned cleanup.
	logger.Info("relay: Mobile Protocol v2 (Noise_IK)")
	drain, err := startRelayV2(ctx, logger, w, conn, registry, serverID)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	legCleanup := func() {
		// Close the connection first so Connection.run closes Frames,
		// which unblocks the manager's Run; drain then waits for it.
		_ = conn.Close()
		drain()
	}

	// The conn.Wait() classifier — a 4409 server-id conflict unwinds the daemon
	// (no reconnect loop); ctx-cancel is the clean-shutdown path; any other
	// terminal error is logged at warn.
	waitDone := make(chan struct{})
	go func() {
		defer close(waitDone)
		err := conn.Wait()
		switch {
		case errors.Is(err, relay.ErrServerIDConflict):
			logger.Error("relay: server-id conflict; shutting down daemon",
				"server_id", string(serverID), "err", err)
			w.shutdown()
		case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			logger.Debug("relay: lifecycle ended via ctx cancel", "err", err)
		case err != nil:
			logger.Warn("relay: lifecycle ended with terminal error", "err", err)
		default:
			logger.Debug("relay: lifecycle ended cleanly")
		}
	}()

	cleanup = func() {
		legCleanup()
		<-waitDone
	}
	return cleanup, nil
}

// startRelayV2 wires the Mobile Protocol v2 (Noise_IK E2E) dispatch leg: it
// loads the binary's persistent static keypair, builds a V2SessionManager
// against conn.Frames() registering the same three relay handlers as the v1
// path, and runs the manager in one goroutine. The returned drain func blocks
// until that goroutine has exited; the caller Close()s conn before calling
// drain so the manager's Run unblocks on the closed Frames channel.
//
// The static key is loaded with the same (baseDir, sanitizeName(name)) pair
// `pyry pair` uses, so the loaded private key derives the public key the phone
// pinned at pairing. On error the leg fails fast at startup, mirroring the
// identity.LoadOrCreate / devices.Load posture in startRelay's prologue.
//
// The structured interactive turn stream (#633) wires the #615 producer to the
// #632 capability-gated emitter, fanning turn_state / assistant_delta / tool /
// turn_end envelopes to interactive phones. It is gated on bridge != nil
// (foreground has no PTY-output observer surface) plus a non-empty
// claudeSessionsDir (the dir the rotation-following JSONL resolver scans; ""
// already disables reconcile + the rotation watcher, so disabling the producer
// too is coherent).
//
// SECURITY: StaticPriv is the binary's 32-byte X25519 static secret. It is
// passed to the manager as an opaque slice and is never logged, wrapped into
// an error, or emitted on any wire surface here — the same contract
// internal/keys and internal/noise enforce for these bytes.
func startRelayV2(
	ctx context.Context,
	logger *slog.Logger,
	w relayWiring,
	conn *relay.Connection,
	registry *devices.Registry,
	serverID identity.ServerID,
) (drain func(), err error) {
	staticKey, err := keys.LoadOrCreate(resolveStaticKeyBaseDir(), sanitizeName(w.instanceName))
	if err != nil {
		return nil, fmt.Errorf("load static key: %w", err)
	}
	priv := staticKey.PrivateKey()

	// Daemon-singleton outstanding-modal registry. The interactive modal stream
	// (#798, startInteractiveModalStreamV2 below) constructs the surfacer over this
	// same instance, so a live permission/trust prompt Records here and the inbound
	// resolver (ModalResolver seam) / deny-on-timeout consume the same entries.
	modalReg := modalbridge.New()

	// Screen-snapshot usage reader (#857): reports the bootstrap session's
	// current context-window occupancy (used tokens + window size) for the
	// screen_snapshot reply. A dedicated probe-preferred resolver follows the
	// daemon's OWN claude child's open transcript (same construction as the
	// turn-stream gate below), so usage-source == snapshot-source == bootstrap
	// even when a second claude shares the sessions dir. Built here — not
	// threaded from main.go like snapshotSettings — because it needs no
	// internal/sessions type: resolveOwnBootstrapJSONL is cmd/pyry-local and
	// internal/contextwindow imports only internal/agentrun/jsonl. nil when there
	// is no sessions dir to resolve a transcript from (foreground / unwired),
	// which makes the handler report zeros. Read-only reflection of two non-secret
	// integers — no authz, no mutation.
	var snapshotUsage func() (usedTokens, windowTokens int)
	if w.claudeSessionsDir != "" {
		usageResolve := resolveOwnBootstrapJSONL(w.claudeSessionsDir, newBootstrapProbe(logger), func() int { return w.sup.State().ChildPID })
		snapshotUsage = func() (int, int) {
			// resolveOwnBootstrapJSONL returns an error (not "",0,nil) for the
			// fresh / backoff / raced / confined-out cases; collapse it to the
			// empty path, which contextwindow.Read maps to the zero/window-default
			// report. The offset return is unused here.
			path, _, err := usageResolve(ctx)
			if err != nil {
				path = ""
			}
			u, rerr := contextwindow.Read(path)
			if rerr != nil {
				// A genuine open failure on a resolved path (e.g. raced away
				// between resolve and read). Read("") never errors and yields the
				// same deterministic fresh-session report, so the error is dead —
				// never surfaced, never logged with path content.
				u, _ = contextwindow.Read("")
			}
			return u.UsedTokens, u.WindowTokens
		}
	}

	mgr, err := relay.NewV2SessionManager(relay.V2SessionConfig{
		Frames:      conn.Frames(),
		Outbound:    conn.Send,
		Connected:   conn.Connected,
		Reconnect:   conn.Reconnected(),
		StaticPriv:  priv[:],
		Devices:     registry,
		DevicesPath: resolveDevicesPath(w.instanceName),
		ServerID:    string(serverID),
		Logger:      logger,
		Handlers: map[string]dispatch.Handler{
			protocol.TypeListConversations:     handlers.ListConversations(w.convReg),
			protocol.TypeCreateConversation:    handlers.CreateConversation(w.convReg, w.creator, resolveConversationsRegistryPath(w.instanceName), w.defaultCwd, logger),
			protocol.TypeRenameConversation:    handlers.RenameConversation(w.convReg, resolveConversationsRegistryPath(w.instanceName), logger),
			protocol.TypePromoteConversation:   handlers.PromoteConversation(w.convReg, resolveConversationsRegistryPath(w.instanceName), logger),
			protocol.TypeDeleteConversation:    handlers.DeleteConversation(w.convReg, resolveConversationsRegistryPath(w.instanceName), logger),
			protocol.TypeArchiveConversation:   handlers.ArchiveConversation(w.convReg, resolveConversationsRegistryPath(w.instanceName), logger, true),
			protocol.TypeUnarchiveConversation: handlers.ArchiveConversation(w.convReg, resolveConversationsRegistryPath(w.instanceName), logger, false),
			protocol.TypeChangeWorkspace:       handlers.ChangeWorkspace(w.convReg, resolveWorkspaceDir, resolveConversationsRegistryPath(w.instanceName), logger),
			protocol.TypeCreateWorkspaceFolder: handlers.CreateWorkspaceFolder(resolveWorkspaceFolder, logger),
			protocol.TypeRecentWorkspaces:      handlers.RecentWorkspaces(w.convReg),
			protocol.TypeRegisterPushToken:     handlers.RegisterPushToken(registry, resolveDevicesPath(w.instanceName), logger),
			protocol.TypeSendMessage:           handlers.SendMessage(w.router, w.queue, logger),
		},
		// Screen-snapshot seam (#618): the supervisor renders the live screen
		// inside the tui-driver seal; KnownConversation gates request_snapshot
		// on registry membership (AC #4), mirroring the established
		// conversations-registry validation pattern but returning a bool so the
		// relay needs no conversations import or errors.Is coupling.
		Snapshotter: w.sup,
		KnownConversation: func(id string) bool {
			_, ok := w.convReg.Get(conversations.ConversationID(id))
			return ok
		},
		// Screen-snapshot settings reader (#848): populates the screen_snapshot
		// reply's model / effort / YOLO fields from the bootstrap session's
		// persisted settings so the phone can render the current model /
		// reasoning-effort / permissions posture before offering to change it
		// (desktop#156). The closure (built at main.go over
		// *sessions.Pool.DefaultSettings) returns three primitives, so
		// internal/relay imports neither internal/sessions nor its SessionSettings
		// type. Read-only reflection — no secret, no authz (contrast
		// SettingsUpdater below, the write path). nil in foreground / v1 makes the
		// handler report defaults (empty model/effort, yolo:false).
		SnapshotSettings: w.snapshotSettings,
		// Screen-snapshot usage reader (#857): populates the screen_snapshot
		// reply's used_tokens / window_tokens from the bootstrap session's current
		// context-window occupancy so the phone can render an "N% used (X of Y)"
		// gauge (desktop#182). The closure (built above over
		// resolveOwnBootstrapJSONL + contextwindow.Read) returns two primitives, so
		// internal/relay imports neither internal/contextwindow nor internal/sessions.
		// Read-only reflection — no secret, no authz. nil in foreground / unresolved
		// sessions dir makes the handler report zeros (used_tokens:0, window_tokens:0).
		SnapshotUsage: snapshotUsage,
		// Connect-time modal reconcile source (#877): enumerates the outstanding-
		// modal registry as marshal-ready modal_shown payloads so a phone that
		// connects/reconnects while a permission prompt is pending is unicast the
		// still-outstanding modal_shown on open. modalReg is the same daemon-
		// singleton the raise-time producer Records into and the ModalResolver
		// below consumes, so enumerate-current-truth reflects live control state.
		// A pure read: it mints no nonce and re-arms no timer.
		OutstandingModals: modalReg.Snapshot,
		// Connect-time queue reconcile source (#878): enumerates the daemon's
		// per-conversation inbound backlogs as marshal-ready queue_state payloads so
		// a phone that connects/reconnects between backlog changes is unicast the
		// current queue_state for each non-empty conversation on open. queue is the
		// same live daemon queue the #722 on-change producer snapshots and the
		// dequeue handler (QueueRemover below) mutates, so enumerate-current-truth
		// reflects live backlog state. A pure read: it mints no id and dequeues nothing.
		OutstandingQueues: outstandingQueues(w.queue),
		// Inbound modal-control resolver (#727): consumes the outstanding-modal
		// registry, routes the resolving keystroke via the supervisor safe-answer
		// seam, and audits. sup (*supervisor.Supervisor) satisfies modalKeystroker
		// (it has SendEsc). modal_cancel resolves here; modal_answer is a deferred
		// no-op until #717 fills the gated arm.
		ModalResolver: newModalResolverV2(modalReg, w.sup, logger),
		// Inbound interrupt seam (#707): an interactive `interrupt` frame routes
		// one Esc through the sealed supervisor keystroke surface. sup
		// (*supervisor.Supervisor) satisfies Interrupter via SendEsc (#726).
		Interrupter: w.sup,
		// Inbound new_session seam (#831): an interactive `new_session` frame
		// routes a /clear through the sealed supervisor keystroke surface. sup
		// (*supervisor.Supervisor) satisfies SessionStarter via StartNewSession
		// (#830).
		SessionStarter: w.sup,
		// Inbound dequeue_message seam (#723): an interactive `dequeue_message`
		// frame removes a not-yet-drained queued message by id from the live
		// daemon queue; the OnChange seam Remove fires drives the #722 producer to
		// push an updated queue_state. The concrete *msgqueue.Queue (built at
		// main.go) satisfies QueueRemover via Remove(string, uint64) bool.
		QueueRemover: w.queue,
		// Inbound debug-bundle seam (#813): a paired `request_debug_bundle` frame
		// assembles the daemon-global bundle (recent log ring + newest recording)
		// and streams it back over the encrypted channel. The closure (built at
		// main.go over debugbundle.Assemble + logRing.Snapshot) returns only
		// (archive, err), so internal/relay never imports internal/debugbundle. nil
		// in foreground / v1 makes the verb reply "unavailable" deterministically.
		DebugBundler: w.debugBundler,
		// Inbound set_session_settings seam (#845): a paired interactive
		// `set_session_settings` frame validates the untrusted model/effort and
		// persists the per-session change via *sessions.Pool.UpdateSettings (#840),
		// applied on the session's next spawn (#833). settingsUpdaterAdapter (built
		// at main.go over the pool) maps sessions.ErrSessionNotFound → the relay
		// sentinel, so internal/relay imports neither internal/sessions nor cmd/pyry.
		// nil in foreground / v1 makes the verb reply "unavailable" deterministically.
		SettingsUpdater: w.settings,
	})
	if err != nil {
		return nil, fmt.Errorf("build v2 session manager: %w", err)
	}

	mgrDone := make(chan struct{})
	go func() {
		defer close(mgrDone)
		if err := mgr.Run(ctx); err != nil {
			logger.Debug("relay: v2 manager run returned", "err", err)
		}
	}()

	// Wire the structured interactive turn stream (#633) and the interactive modal
	// stream (#798) inside one shared PTY gate. Both follow the active conversation
	// over resolveTarget + NewTargetSubscriber: the turn stream bridges the #615
	// producer's mapped events to the #632 emitter; the modal stream drains the RAW
	// event stream (a second, independent Session.Events() subscription) to the
	// frozen #716/#717/#725/#706 surfacer, so a live permission/trust prompt emits
	// modal_shown, arms the deny-on-timeout, and resolves modal_dismissed{local}.
	// Gated on bridge != nil (foreground has no phone-mirroring surface) plus a
	// resolvable sessions dir (an empty dir would make the resolver perpetually
	// error and Warn-spam every retry; "" already disables reconcile + the rotation
	// watcher).
	var (
		streamCleanup      func()
		modalStreamCleanup func()
	)
	if w.bridge != nil && w.claudeSessionsDir != "" {
		// The bootstrap-branch resolver tails the transcript the daemon's OWN
		// claude child has open (probe over its PID) rather than the newest file by
		// mtime, so a second claude in the shared sessions dir can't redirect the
		// tail. pidFn re-reads State each resolve — the child respawns with a new
		// PID and State() is mutex-guarded.
		probe := newBootstrapProbe(logger)
		pidFn := func() int { return w.sup.State().ChildPID }
		streamCleanup = startInteractiveTurnStreamV2(ctx, w.sup, w.active, w.boundHost, mgr, w.claudeSessionsDir, probe, pidFn, w.bootstrapIDFn, logger)
		modalStreamCleanup = startInteractiveModalStreamV2(ctx, w.sup, w.active, w.boundHost, mgr, modalReg, w.claudeSessionsDir, probe, pidFn, w.bootstrapIDFn, logger)
	} else if w.bridge != nil {
		logger.Info("relay: interactive turn + modal streams disabled; claude sessions dir unresolved",
			"event", "interactive_turn_stream.no_sessions_dir")
	}

	// Wire the session-transition producer (#657): install #659's pool-side
	// observer and fan a session_transition envelope to capability-gated
	// interactive phones on each /clear rotation or idle/cap eviction. Unlike the
	// coarse bridge and the structured turn stream above, this has NO PTY
	// dependency — it consumes pool transitions, which fire in any mode — so it is
	// wired unconditionally whenever the v2 manager exists (no bridge != nil
	// gate). The capability filter (ActiveConns → Interactive) is the real
	// delivery gate: with no interactive phone connected, the fan-out reaches
	// nobody.
	// The resolver closure (#741) stamps the owning conversation_id onto each
	// emitted envelope. It captures w.convReg (a relayWiring field in scope here)
	// so session_transition_v2.go never imports internal/conversations — the same
	// purity discipline that keeps toWirePayload registry-free.
	streamTransitionsCleanup := startSessionTransitionStreamV2(ctx, w.transitions, mgr,
		func(sid string) (string, bool) { return conversationForSession(w.convReg, sid) }, logger)

	// Wire the queue_state producer (#722): start the pre-built emitter's Run
	// goroutine over mgr, fanning a queue_state envelope to capability-gated
	// interactive phones whenever a conversation's inbound backlog changes
	// (enqueue, drain-advance, or remove). Like the session-transition producer it
	// has NO PTY dependency — it consumes msgqueue changes — so it is wired
	// unconditionally whenever the v2 manager exists. The capability filter
	// (ActiveConns → Interactive) is the delivery gate: with no interactive phone
	// connected, the fan-out reaches nobody.
	streamQueueStateCleanup := startQueueStateStreamV2(ctx, w.qse, mgr)

	// Wire the session_error producer (#1008): start the pre-built emitter's Run
	// goroutine over mgr, fanning a typed session_error frame to capability-gated
	// interactive phones whenever the message queue gives up delivering a
	// conversation's head (#1000's OnGiveUp seam). Like the queue_state producer it
	// has NO PTY dependency — it consumes msgqueue give-ups — so it is wired
	// unconditionally whenever the v2 manager exists.
	streamSessionErrCleanup := startSessionErrorStreamV2(ctx, w.sessionErr, mgr)

	return func() {
		// Stop the producers — the structured turn stream, the modal stream, the
		// session-transition producer, the queue_state producer, and the session_error
		// producer — before waiting on the manager so no fan-out races a winding-down
		// manager. Each cleanup waits for its goroutine on ctx-cancel (already
		// cancelled by the time drain runs). Then wait for the manager's Run to exit
		// on the closed Frames channel.
		if streamCleanup != nil {
			streamCleanup()
		}
		if modalStreamCleanup != nil {
			modalStreamCleanup()
		}
		streamTransitionsCleanup()
		streamQueueStateCleanup()
		streamSessionErrCleanup()
		<-mgrDone
	}, nil
}

// conversationForSession resolves a claude session id to the id of the
// conversation that owns it, scanning the maintained registry binding
// (CurrentSessionID + the append-only SessionHistory). It is the #741 read
// counterpart to #739's RebindSession write maintenance; the registry exposes
// no by-session-id read method, so the scan is duplicated here cmd-side (its
// only consumer — PROJECT-MEMORY "Resist over-DRY on duplicated registry
// primitives").
//
// An empty sid returns ("", false) immediately, mirroring RebindSession's
// empty-oldID guard: an unbound conversation carries CurrentSessionID == ""
// and must never be matched by a stray empty-id lookup. First match wins —
// the single-owner invariant (a session id binds exactly one conversation for
// life) makes the first match the only match.
//
// Race-safe against a concurrent RebindSession: List() copies the slice header
// under the registry mutex, and a concurrent append writes at/above len (or
// reallocates), never overwriting an index the captured [0,len) read touches.
func conversationForSession(convReg *conversations.Registry, sid string) (string, bool) {
	if sid == "" {
		return "", false
	}
	for _, c := range convReg.List() {
		if c.CurrentSessionID == sid || slices.Contains(c.SessionHistory, sid) {
			return string(c.ID), true
		}
	}
	return "", false
}
