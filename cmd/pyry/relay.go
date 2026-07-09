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

// authGate builds the dispatcher's FirstFrame closure that bridges
// dispatch.FirstFrameGate and relay.AuthenticateFirstFrame. The token
// is read from env.Token (relay-prepended on the first phone→binary
// frame); the gate never logs the token, never wraps it into an error,
// and never echoes it.
func authGate(registry *devices.Registry, serverID string, logger *slog.Logger) dispatch.FirstFrameGate {
	return func(ctx context.Context, env protocol.RoutingEnvelope) dispatch.FirstFrameOutcome {
		outcome, err := relay.AuthenticateFirstFrame(env, env.Token, registry, serverID, logger)
		if err != nil {
			// Today only ErrMalformedHelloFrame is reachable. Surface to
			// the dispatcher's malformed-frame fall-through.
			return dispatch.FirstFrameOutcome{Err: err}
		}
		out := dispatch.FirstFrameOutcome{
			Response: outcome.Response,
			Device:   outcome.Device, // nil on reject; populated on accept
		}
		if outcome.CloseConn {
			out.CloseConn = true
			out.Code = uint16(relay.StatusUnauthorized) // 4401
		}
		return out
	}
}

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

// startRelay opens the binary↔relay leg in a supervisor-owned goroutine.
// Returns a no-op cleanup and nil err when relayURL is empty (relay
// disabled — see operator note below). Otherwise loads the server-id,
// calls relay.Connect, and spawns one goroutine that:
//
//   - drains conn.Frames() (the dispatcher slice consumes them later)
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
	instanceName, relayURL, version string,
	allowInsecure, v2Enabled bool,
	shutdown context.CancelFunc,
	convReg *conversations.Registry,
	creator handlers.SessionCreator,
	router handlers.SessionRouter,
	queue *msgqueue.Queue,
	active *activeConversation,
	boundHost boundHostFunc,
	sup *supervisor.Supervisor,
	bridge *supervisor.Bridge,
	claudeSessionsDir string,
	defaultCwd string,
	transitions transitionObserverSink,
	qse *queueStateEmitterV2,
	debugBundler func() ([]byte, error),
	settings relay.SettingsUpdater,
	snapshotSettings func() (model, effort string, yolo bool),
) (cleanup func(), err error) {
	if relayURL == "" {
		logger.Info("relay: disabled (no URL configured)")
		return func() {}, nil
	}

	serverID, err := identity.LoadOrCreate(resolveServerIDPath(instanceName))
	if err != nil {
		return nil, fmt.Errorf("load server-id: %w", err)
	}

	// Load the device registry once at daemon startup. A missing file
	// (ENOENT) yields an empty registry — every phone rejects until
	// `pyry pair` runs. Malformed JSON fails fast.
	registry, err := devices.Load(resolveDevicesPath(instanceName))
	if err != nil {
		return nil, fmt.Errorf("load device registry: %w", err)
	}

	if allowInsecure {
		logger.Info("relay: PYRY_ALLOW_INSECURE_RELAY=1 — accepting ws:// scheme")
	}
	logger.Info("relay: connecting", "url", relayURL, "server_id", string(serverID))

	conn, err := relay.Connect(ctx, relay.Config{
		ServerID:            serverID,
		RelayURL:            relayURL,
		BinaryVersion:       version,
		Logger:              logger,
		AllowInsecureScheme: allowInsecure,
	})
	if err != nil {
		return nil, fmt.Errorf("relay connect: %w", err)
	}

	// legCleanup tears down the protocol-specific consumers of conn.Frames()
	// (the v1 dispatcher path or the v2 Noise manager); the shared waitDone
	// classifier below is appended to it. Exactly one leg consumes the frame
	// stream — there is no mixed-mode path (ADR 024: v2 is a hard cutover).
	var legCleanup func()

	if v2Enabled {
		logger.Info("relay: Mobile Protocol v2 (Noise_IK) enabled — default; set PYRY_MOBILE_V2=0 to force legacy v1")
		drain, err := startRelayV2(ctx, logger, instanceName, conn, registry, serverID, convReg, creator, router, queue, active, boundHost, sup, bridge, claudeSessionsDir, defaultCwd, transitions, qse, debugBundler, settings, snapshotSettings)
		if err != nil {
			_ = conn.Close()
			return nil, err
		}
		legCleanup = func() {
			// Close the connection first so Connection.run closes Frames,
			// which unblocks the manager's Run; drain then waits for it.
			_ = conn.Close()
			drain()
		}
	} else {
		logger.Warn("relay: PYRY_MOBILE_V2=0 — legacy v1 dispatch path (DEPRECATED; no shipping client speaks v1, mobile/desktop require v2)")
		d := dispatch.New(dispatch.Config{
			Frames:     conn.Frames(),
			Logger:     logger,
			FirstFrame: authGate(registry, string(serverID), logger),
		})
		d.Register(protocol.TypeListConversations, handlers.ListConversations(convReg))
		d.Register(protocol.TypeCreateConversation, handlers.CreateConversation(convReg, creator, resolveConversationsRegistryPath(instanceName), defaultCwd, logger))
		d.Register(protocol.TypeRenameConversation, handlers.RenameConversation(convReg, resolveConversationsRegistryPath(instanceName), logger))
		d.Register(protocol.TypeDeleteConversation, handlers.DeleteConversation(convReg, resolveConversationsRegistryPath(instanceName), logger))
		d.Register(protocol.TypeArchiveConversation, handlers.ArchiveConversation(convReg, resolveConversationsRegistryPath(instanceName), logger, true))
		d.Register(protocol.TypeUnarchiveConversation, handlers.ArchiveConversation(convReg, resolveConversationsRegistryPath(instanceName), logger, false))
		d.Register(protocol.TypeChangeWorkspace, handlers.ChangeWorkspace(convReg, resolveWorkspaceDir, resolveConversationsRegistryPath(instanceName), logger))
		d.Register(protocol.TypeCreateWorkspaceFolder, handlers.CreateWorkspaceFolder(resolveWorkspaceFolder, logger))
		d.Register(protocol.TypeRecentWorkspaces, handlers.RecentWorkspaces(convReg))
		d.Register(protocol.TypeRegisterPushToken, handlers.RegisterPushToken(registry, resolveDevicesPath(instanceName), logger))
		d.Register(protocol.TypeSendMessage, handlers.SendMessage(router, queue, logger))

		// The assistant-turn bridge taps Bridge.Write so PTY chunks fan out
		// to every active phone conn as a `message` envelope (#311). Skip in
		// foreground mode (bridge == nil) — there is no PTY-output observer
		// surface in that path; inbound `send_message` still works.
		var bridgeCleanup func()
		if bridge != nil {
			bridgeCleanup = startAssistantTurnBridge(ctx, sup, bridge, d, logger)
		}

		dispatcherDone := make(chan struct{})
		go func() {
			defer close(dispatcherDone)
			if err := d.Run(ctx); err != nil {
				logger.Debug("relay: dispatcher run returned", "err", err)
			}
		}()

		forwarderDone := make(chan struct{})
		go func() {
			defer close(forwarderDone)
			for env := range d.Outbound() {
				if err := conn.Send(env); err != nil {
					// Transport-internal reconnect handles transient drops;
					// a Send error here means the conn is currently dropped
					// or closed. We log and continue draining so the
					// dispatcher's Outbound close still unblocks Run.
					logger.Debug("relay: outbound forward dropped",
						"conn_id", env.ConnID, "err", err)
				}
			}
		}()

		legCleanup = func() {
			// Stop the assistant-turn observer first so no new PTY chunks
			// queue while the dispatcher is winding down. The cleanup waits
			// for the emitter goroutine on ctx-cancel.
			if bridgeCleanup != nil {
				bridgeCleanup()
			}
			_ = conn.Close()
			// Order: Connection.run defers close(frames) → dispatcher.Run
			// returns (Frames closed) → dispatcher closes Outbound → forwarder
			// exits. Wait returns once Connection.run completes.
			<-dispatcherDone
			<-forwarderDone
		}
	}

	// The conn.Wait() classifier is identical for both legs — a 4409
	// server-id conflict unwinds the daemon (no reconnect loop); ctx-cancel
	// is the clean-shutdown path; any other terminal error is logged at warn.
	// Shared after the branch so the v2 leg inherits the contract unchanged.
	waitDone := make(chan struct{})
	go func() {
		defer close(waitDone)
		err := conn.Wait()
		switch {
		case errors.Is(err, relay.ErrServerIDConflict):
			logger.Error("relay: server-id conflict; shutting down daemon",
				"server_id", string(serverID), "err", err)
			shutdown()
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
	instanceName string,
	conn *relay.Connection,
	registry *devices.Registry,
	serverID identity.ServerID,
	convReg *conversations.Registry,
	creator handlers.SessionCreator,
	router handlers.SessionRouter,
	queue *msgqueue.Queue,
	active *activeConversation,
	boundHost boundHostFunc,
	sup *supervisor.Supervisor,
	bridge *supervisor.Bridge,
	claudeSessionsDir string,
	defaultCwd string,
	transitions transitionObserverSink,
	qse *queueStateEmitterV2,
	debugBundler func() ([]byte, error),
	settings relay.SettingsUpdater,
	snapshotSettings func() (model, effort string, yolo bool),
) (drain func(), err error) {
	staticKey, err := keys.LoadOrCreate(resolveStaticKeyBaseDir(), sanitizeName(instanceName))
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
	if claudeSessionsDir != "" {
		usageResolve := resolveOwnBootstrapJSONL(claudeSessionsDir, newBootstrapProbe(logger), func() int { return sup.State().ChildPID })
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
		StaticPriv:  priv[:],
		Devices:     registry,
		DevicesPath: resolveDevicesPath(instanceName),
		ServerID:    string(serverID),
		Logger:      logger,
		Handlers: map[string]dispatch.Handler{
			protocol.TypeListConversations:     handlers.ListConversations(convReg),
			protocol.TypeCreateConversation:    handlers.CreateConversation(convReg, creator, resolveConversationsRegistryPath(instanceName), defaultCwd, logger),
			protocol.TypeRenameConversation:    handlers.RenameConversation(convReg, resolveConversationsRegistryPath(instanceName), logger),
			protocol.TypeDeleteConversation:    handlers.DeleteConversation(convReg, resolveConversationsRegistryPath(instanceName), logger),
			protocol.TypeArchiveConversation:   handlers.ArchiveConversation(convReg, resolveConversationsRegistryPath(instanceName), logger, true),
			protocol.TypeUnarchiveConversation: handlers.ArchiveConversation(convReg, resolveConversationsRegistryPath(instanceName), logger, false),
			protocol.TypeChangeWorkspace:       handlers.ChangeWorkspace(convReg, resolveWorkspaceDir, resolveConversationsRegistryPath(instanceName), logger),
			protocol.TypeCreateWorkspaceFolder: handlers.CreateWorkspaceFolder(resolveWorkspaceFolder, logger),
			protocol.TypeRecentWorkspaces:      handlers.RecentWorkspaces(convReg),
			protocol.TypeRegisterPushToken:     handlers.RegisterPushToken(registry, resolveDevicesPath(instanceName), logger),
			protocol.TypeSendMessage:           handlers.SendMessage(router, queue, logger),
		},
		// Screen-snapshot seam (#618): the supervisor renders the live screen
		// inside the tui-driver seal; KnownConversation gates request_snapshot
		// on registry membership (AC #4), mirroring the established
		// conversations-registry validation pattern but returning a bool so the
		// relay needs no conversations import or errors.Is coupling.
		Snapshotter: sup,
		KnownConversation: func(id string) bool {
			_, ok := convReg.Get(conversations.ConversationID(id))
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
		SnapshotSettings: snapshotSettings,
		// Screen-snapshot usage reader (#857): populates the screen_snapshot
		// reply's used_tokens / window_tokens from the bootstrap session's current
		// context-window occupancy so the phone can render an "N% used (X of Y)"
		// gauge (desktop#182). The closure (built above over
		// resolveOwnBootstrapJSONL + contextwindow.Read) returns two primitives, so
		// internal/relay imports neither internal/contextwindow nor internal/sessions.
		// Read-only reflection — no secret, no authz. nil in foreground / unresolved
		// sessions dir makes the handler report zeros (used_tokens:0, window_tokens:0).
		SnapshotUsage: snapshotUsage,
		// Inbound modal-control resolver (#727): consumes the outstanding-modal
		// registry, routes the resolving keystroke via the supervisor safe-answer
		// seam, and audits. sup (*supervisor.Supervisor) satisfies modalKeystroker
		// (it has SendEsc). modal_cancel resolves here; modal_answer is a deferred
		// no-op until #717 fills the gated arm.
		ModalResolver: newModalResolverV2(modalReg, sup, logger),
		// Inbound interrupt seam (#707): an interactive `interrupt` frame routes
		// one Esc through the sealed supervisor keystroke surface. sup
		// (*supervisor.Supervisor) satisfies Interrupter via SendEsc (#726).
		Interrupter: sup,
		// Inbound new_session seam (#831): an interactive `new_session` frame
		// routes a /clear through the sealed supervisor keystroke surface. sup
		// (*supervisor.Supervisor) satisfies SessionStarter via StartNewSession
		// (#830).
		SessionStarter: sup,
		// Inbound dequeue_message seam (#723): an interactive `dequeue_message`
		// frame removes a not-yet-drained queued message by id from the live
		// daemon queue; the OnChange seam Remove fires drives the #722 producer to
		// push an updated queue_state. The concrete *msgqueue.Queue (built at
		// main.go) satisfies QueueRemover via Remove(string, uint64) bool.
		QueueRemover: queue,
		// Inbound debug-bundle seam (#813): a paired `request_debug_bundle` frame
		// assembles the daemon-global bundle (recent log ring + newest recording)
		// and streams it back over the encrypted channel. The closure (built at
		// main.go over debugbundle.Assemble + logRing.Snapshot) returns only
		// (archive, err), so internal/relay never imports internal/debugbundle. nil
		// in foreground / v1 makes the verb reply "unavailable" deterministically.
		DebugBundler: debugBundler,
		// Inbound set_session_settings seam (#845): a paired interactive
		// `set_session_settings` frame validates the untrusted model/effort and
		// persists the per-session change via *sessions.Pool.UpdateSettings (#840),
		// applied on the session's next spawn (#833). settingsUpdaterAdapter (built
		// at main.go over the pool) maps sessions.ErrSessionNotFound → the relay
		// sentinel, so internal/relay imports neither internal/sessions nor cmd/pyry.
		// nil in foreground / v1 makes the verb reply "unavailable" deterministically.
		SettingsUpdater: settings,
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
	if bridge != nil && claudeSessionsDir != "" {
		// The bootstrap-branch resolver tails the transcript the daemon's OWN
		// claude child has open (probe over its PID) rather than the newest file by
		// mtime, so a second claude in the shared sessions dir can't redirect the
		// tail. pidFn re-reads State each resolve — the child respawns with a new
		// PID and State() is mutex-guarded.
		probe := newBootstrapProbe(logger)
		pidFn := func() int { return sup.State().ChildPID }
		streamCleanup = startInteractiveTurnStreamV2(ctx, sup, active, boundHost, mgr, claudeSessionsDir, probe, pidFn, logger)
		modalStreamCleanup = startInteractiveModalStreamV2(ctx, sup, active, boundHost, mgr, modalReg, claudeSessionsDir, probe, pidFn, logger)
	} else if bridge != nil {
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
	// emitted envelope. It captures convReg (in scope here, already a startRelayV2
	// parameter) so session_transition_v2.go never imports internal/conversations
	// — the same purity discipline that keeps toWirePayload registry-free.
	streamTransitionsCleanup := startSessionTransitionStreamV2(ctx, transitions, mgr,
		func(sid string) (string, bool) { return conversationForSession(convReg, sid) }, logger)

	// Wire the queue_state producer (#722): start the pre-built emitter's Run
	// goroutine over mgr, fanning a queue_state envelope to capability-gated
	// interactive phones whenever a conversation's inbound backlog changes
	// (enqueue, drain-advance, or remove). Like the session-transition producer it
	// has NO PTY dependency — it consumes msgqueue changes — so it is wired
	// unconditionally whenever the v2 manager exists. The capability filter
	// (ActiveConns → Interactive) is the delivery gate: with no interactive phone
	// connected, the fan-out reaches nobody.
	streamQueueStateCleanup := startQueueStateStreamV2(ctx, qse, mgr)

	return func() {
		// Stop the producers — the structured turn stream, the modal stream, the
		// session-transition producer, and the queue_state producer — before waiting
		// on the manager so no fan-out races a winding-down manager. Each cleanup
		// waits for its goroutine on ctx-cancel (already cancelled by the time drain
		// runs). Then wait for the manager's Run to exit on the closed Frames channel.
		if streamCleanup != nil {
			streamCleanup()
		}
		if modalStreamCleanup != nil {
			modalStreamCleanup()
		}
		streamTransitionsCleanup()
		streamQueueStateCleanup()
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
