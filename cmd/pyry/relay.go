package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strconv"

	"github.com/pyrycode/pyrycode/internal/attachments"
	"github.com/pyrycode/pyrycode/internal/config"
	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/devices"
	"github.com/pyrycode/pyrycode/internal/dispatch"
	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/identity"
	"github.com/pyrycode/pyrycode/internal/keys"
	"github.com/pyrycode/pyrycode/internal/modalbridge"
	"github.com/pyrycode/pyrycode/internal/msgqueue"
	"github.com/pyrycode/pyrycode/internal/permbridge"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/questionbridge"
	"github.com/pyrycode/pyrycode/internal/relay"
	"github.com/pyrycode/pyrycode/internal/relay/handlers"
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
	// shutdown unwinds the daemon; called on a PERSISTENT 4409 server-id
	// conflict (the transport retries a transient conflict through its
	// backoff ladder first, #1072) so the relay leg does not
	// reconnect-loop forever against a genuine duplicate. It carries a
	// cause: this self-initiated fatal path passes the conflict error so
	// runSupervisor exits non-zero and launchd restarts the daemon, unlike
	// an operator stop which cancels with a nil cause and stays down.
	shutdown context.CancelCauseFunc
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
	// activeInterrupter routes an inbound interrupt frame to the runner bound to
	// the ACTIVE conversation, not the bootstrap supervisor — the #1121 isolation
	// fix. Wired to V2SessionConfig.Interrupter below.
	activeInterrupter relay.Interrupter
	// activeSessionStarter routes an inbound new_session frame to the runner bound
	// to the ACTIVE conversation, not the bootstrap supervisor — the #1125
	// isolation fix (the new_session twin of activeInterrupter). Wired to
	// V2SessionConfig.SessionStarter below.
	activeSessionStarter relay.SessionStarter
	// claudeSessionsDir is the directory the rotation-following JSONL resolver
	// scans to tail the daemon's own claude child's transcript (turn stream #633,
	// snapshot-usage reader #857). Empty disables reconcile and the interactive
	// turn/modal streams.
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
	// blockedNotify routes a folder-not-trusted session_error (a trust deny /
	// deny-on-timeout) into the same give-up → session_error frame path the
	// msgqueue OnGiveUp seam uses (#1014). It is main.go's shared `blocked` closure
	// (a non-blocking send into the giveUps channel). Set on the resolver's #1014
	// emit seam below; nil in foreground/v1 leaves the resolver's emit inert.
	blockedNotify func(convID, reason string)
	// debugBundler assembles the daemon-global debug bundle for the
	// request_debug_bundle verb (#813). nil in foreground/v1 replies "unavailable".
	debugBundler func() ([]byte, error)
	// settings persists a per-session model/effort change for the
	// set_session_settings verb (#845). nil in foreground/v1 replies "unavailable".
	settings relay.SettingsUpdater
	// snapshotSettings reports the bootstrap session's persisted model/effort/YOLO
	// for the screen_snapshot reply (#848). nil reports defaults.
	snapshotSettings func() (model, effort string, yolo bool)
	// runSettings is the settings half of the conversation-keyed run-configuration
	// seam (#1609): it resolves a NAMED conversation to its bound session id plus
	// that session's model/effort/YOLO, under one pool acquisition
	// (resolveBoundRunSettings). Built at main.go over the conversations registry
	// and *sessions.Pool.SettingsFor so the internal/sessions dependency stays at
	// the composition root. runConfigFor below composes it with the by-id
	// context-window reader into the primitive-typed seam that crosses into
	// internal/relay; this cmd/pyry-typed value never does. nil in foreground/v1 ⇒
	// no seam is built at all.
	runSettings func(convID string) (boundRunSettings, bool)
	// promptState is the resolution half of the conversation system-prompt read
	// seam (#2152): it resolves a NAMED conversation to the prompt the registry
	// stores and to what the session it is bound to was actually spawned with
	// (main.go's resolveConversationPrompt). Built at main.go over the conversations
	// registry and *sessions.Pool.SystemPromptFor so the internal/sessions
	// dependency stays at the composition root, exactly as runSettings above is.
	//
	// systemPromptFor below composes it into the primitive-typed seam that crosses
	// into internal/relay, where the stored/spawned-with COMPARISON is computed;
	// this cmd/pyry-typed value never crosses. nil in foreground/v1 ⇒ no seam is
	// built at all, and the verb answers its constant no-session reply to every
	// request.
	promptState func(convID string) (conversationPromptState, bool)
	// modelListFor resolves a NAMED conversation to the model menu it should be
	// offered, already shaped as a marshal-ready protocol.ModelListPayload, for the
	// relay's on-demand request seam (#2125 fills V2SessionConfig.ModelListFor).
	// Built at main.go over the conversations registry and *sessions.Pool for the
	// SAME reason runSettings above is: the internal/sessions dependency stays at
	// the composition root, and startRelayV2 holds no pool reference at all.
	//
	// It is retainedModelLists' CONVERSATION-KEYED TWIN and reaches the same
	// resolver — including the #2124 fallback that answers a conversation with no
	// bound session from the daemon-wide vocabulary, which is the case the request
	// verb exists to serve. The enumerator below is the connect-time shape and is
	// forced to enumerate because a relay V2Session carries no conversation id;
	// this path has one in the request, so it must not walk the registry.
	//
	// The value is already primitive to internal/relay (protocol is imported both
	// sides), so it crosses into V2SessionConfig unwrapped. nil in foreground/v1 ⇒
	// the verb refuses every request as model_list.unavailable.
	modelListFor func(convID string) (protocol.ModelListPayload, bool)
	// modelWindows answers the context windows a named SESSION's child has
	// reported, keyed by claude's own model id, for the context-window half of
	// both usage seams below (#2107). Built at main.go over *sessions.Pool for the
	// SAME reason runSettings above is: the internal/sessions dependency stays at
	// the composition root, and startRelayV2 holds no pool reference at all. It is
	// keyed on the session id rather than the conversation id because both seams
	// that consume it already hold one — snapshotUsageFor resolves a transcript by
	// exactly that id, so the two halves are two readings of one child.
	//
	// nil in foreground/v1, which is NOT the either-half-unwired shape
	// bootstrapIDFn has: a nil value leaves both usage seams working and reporting
	// the default window, the pre-#2107 reading. See snapshotUsageFor.
	modelWindows func(sessionID string) map[string]int
	// retainedModelLists enumerates the daemon's currently-retained model lists as
	// marshal-ready model_list payloads — one per conversation whose bound session
	// holds a list — for the relay's connect-time reconcile seam (#1867 fills
	// #1863's V2SessionConfig.RetainedModelLists). Built at main.go over the
	// conversations registry and *sessions.Pool for the SAME reason runSettings
	// above is: the internal/sessions dependency stays at the composition root.
	// OutstandingQueues below is built inline in startRelayV2 only because this
	// file imports internal/msgqueue; it deliberately does not import
	// internal/sessions, so the queue precedent does not apply here. The value is
	// already primitive to internal/relay (protocol is imported both sides), so it
	// crosses into V2SessionConfig unwrapped. nil in foreground/v1 ⇒ no reconcile.
	retainedModelLists func() []protocol.ModelListPayload
	// retainedSlashCommandLists enumerates the daemon's currently-retained
	// slash-command inventories as marshal-ready slash_command_list payloads — one
	// per conversation whose bound session holds one — for the relay's connect-time
	// reconcile seam (#2007 fills #2006's
	// V2SessionConfig.RetainedSlashCommandLists). retainedModelLists above is the
	// twin in every respect including this placement: built at main.go over the
	// conversations registry and *sessions.Pool so the internal/sessions dependency
	// stays at the composition root, since this file deliberately does not import
	// it. The value is already primitive to internal/relay (protocol is imported
	// both sides), so it crosses into V2SessionConfig unwrapped. nil in
	// foreground/v1 ⇒ no reconcile.
	retainedSlashCommandLists func() []protocol.SlashCommandListPayload
	// retainedBackgroundTaskRosters enumerates the background-task rosters the
	// daemon's sessions currently hold as marshal-ready background_task_roster
	// payloads — one per conversation whose bound session has reported a roster —
	// for the relay's connect-time reconcile seam (#2079 fills #2078's
	// V2SessionConfig.RetainedBackgroundTaskRosters). retainedSlashCommandLists
	// above is the twin in every respect including this placement: built at main.go
	// over the conversations registry and *sessions.Pool so the internal/sessions
	// dependency stays at the composition root, since this file deliberately does
	// not import it. The value is already primitive to internal/relay (protocol is
	// imported both sides), so it crosses into V2SessionConfig unwrapped. nil in
	// foreground/v1 ⇒ no reconcile.
	//
	// Where it is NOT the twin: a session that reported an EMPTY roster contributes
	// a payload carrying an empty task list rather than nothing, because an empty
	// roster positively says nothing is alive. The producer states the rule; it is
	// noted here so a reader wiring a seventh seam beside this one does not read the
	// three retained* fields as interchangeable.
	retainedBackgroundTaskRosters func() []protocol.BackgroundTaskRosterPayload
	// approvals is the daemon-singleton pending-approval registry (#1103). The
	// stream-approval bridge (#1080) constructed in startRelayV2 Lookups/Resolves
	// parked completers against this SAME instance the control server parks into,
	// and startRelayV2 returns the bridge's Surface for the control server to call.
	// nil (foreground/v1) leaves the resolver's stream arm unwired ⇒ keystroke-only.
	approvals *permbridge.Registry
	// streamSink is the stream-json runner's turn-event fan-in (#1081). Non-nil
	// selects stream mode — main.go sets it iff interactive_runner == "stream-json",
	// over the SAME newStreamTurnSink instance the runner factory feeds; nil keeps
	// the PTY interactive path. Its presence IS the relay leg's stream-mode
	// discriminant (equivalent to w.sup == nil, since bootstrap.Supervisor() returns
	// a genuine nil *supervisor.Supervisor for a stream runner, #1077): it gates the
	// three raw w.sup.State() readers off and feeds the stream turn drain in their
	// place. See the branch at the interactive-streams gate below.
	streamSink *streamTurnSink
	// busy is the #1201 per-conversation turn-busy tracker. Non-nil exactly when
	// streamSink is non-nil — both are minted at the composition root (main.go) from
	// the same streamSink != nil condition — so PTY mode leaves it nil and the #1202
	// teardown clear composed onto the UNCONDITIONAL session-transition observer
	// below stays a nil-receiver no-op.
	//
	// It is a field rather than a local because it now has a consumer outside this
	// file: the inbound-delivery seam (#1199, newInboundDeliver) holds a mid-turn
	// send against it. This leg's two consumers are the stream turn drain (which
	// FEEDS it) and that teardown clear.
	busy *turnBusyTracker

	// hist is the daemon's ONE durable conversation log (#2112), minted at the
	// composition root and threaded here because both v2 stream producers write
	// to it: the interactive chokepoint's emitter, built inside the streamSink
	// branch, and the session-transition stream, started unconditionally. nil is
	// a daemon with no durable log; every write site guards for it.
	hist *history.Store
	// approvalParked is the late-bound seam carrying #1919's ApprovalParked report
	// back to the msgqueue delivery seam (#1911), which main.go builds FIRST:
	// msgqueue.New runs well before this function constructs the bridge that answers
	// the report, and this struct's own literal is built after the queue. It travels
	// in the wiring rather than being handed to the queue at construction for that
	// reason alone. Set inside the approvals branch below, beside
	// bridge.toolCallInFlight. nil, or never set, answers negative for every
	// conversation and the give-up bound runs exactly as it did before the exemption
	// existed.
	approvalParked *approvalParkedReport
}

// startRelay opens the binary↔relay leg in a supervisor-owned goroutine.
// Returns a no-op cleanup and nil err when relayURL is empty (relay
// disabled — see operator note below). Otherwise loads the server-id,
// calls relay.Connect, and spawns one goroutine that:
//
//   - drains conn.Frames() (the v2 Noise manager consumes them)
//   - blocks on conn.Wait()
//   - on relay.ErrServerIDConflict: logs the conflict and calls shutdown()
//     to unwind the daemon (AC#3: no endless reconnect-loop on 4409; the
//     transport has already retried a bounded window per #1072, so the
//     conflict is persistent — a genuine duplicate binary)
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
) (cleanup func(), surface func(permbridge.Request) func(), announce func(conversationID, attachmentID, filename string), announceConversation func(protocol.ConversationUpdatedPayload), err error) {
	if w.relayURL == "" {
		logger.Info("relay: disabled (no URL configured)")
		// No relay leg ⇒ no stream-approval bridge and neither fan-out emitter;
		// all three stay nil. SetApprovalSurfacer(nil) leaves mcp.approve
		// modal-less, a nil announce hook leaves attachment.file storing and
		// minting with nobody to tell (#2166), and a nil conversation hook leaves
		// channel.new creating with nobody to tell (#2156) — this early return is
		// exactly the daemon both hooks' nil-tolerance is written for.
		return func() {}, nil, nil, nil, nil
	}

	serverID, err := identity.LoadOrCreate(resolveServerIDPath(w.instanceName))
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("load server-id: %w", err)
	}

	// Load the device registry once at daemon startup. A missing file
	// (ENOENT) yields an empty registry — every phone rejects until
	// `pyry pair` runs. Malformed JSON fails fast.
	registry, err := devices.Load(resolveDevicesPath(w.instanceName))
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("load device registry: %w", err)
	}

	if w.allowInsecure {
		logger.Info("relay: PYRY_ALLOW_INSECURE_RELAY=1 — accepting ws:// scheme")
	}
	logger.Info("relay: connecting", "url", w.relayURL, "server_id", string(serverID))

	conn, err := relay.Connect(ctx, relay.Config{
		ServerID:                  serverID,
		RelayURL:                  w.relayURL,
		BinaryVersion:             w.version,
		Logger:                    logger,
		AllowInsecureScheme:       w.allowInsecure,
		ServerIDConflictThreshold: relay4409Threshold(logger),
	})
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("relay connect: %w", err)
	}

	// legCleanup tears down the v2 Noise manager — the sole consumer of
	// conn.Frames() (ADR 024: v2 is a hard cutover, no mixed-mode path). The
	// shared waitDone classifier below is appended to it in the returned cleanup.
	logger.Info("relay: Mobile Protocol v2 (Noise_IK)")
	drain, surface, announce, announceConversation, err := startRelayV2(ctx, logger, w, conn, registry, serverID)
	if err != nil {
		_ = conn.Close()
		return nil, nil, nil, nil, err
	}
	legCleanup := func() {
		// Close the connection first so Connection.run closes Frames,
		// which unblocks the manager's Run; drain then waits for it.
		_ = conn.Close()
		drain()
	}

	// The conn.Wait() classifier — a PERSISTENT 4409 server-id conflict
	// (post-#1072: the transport has already retried the bounded backoff
	// window, so this is a genuine duplicate) unwinds the daemon; ctx-cancel
	// is the clean-shutdown path; any other terminal error is logged at warn.
	waitDone := make(chan struct{})
	go func() {
		defer close(waitDone)
		err := conn.Wait()
		switch {
		case errors.Is(err, relay.ErrServerIDConflict):
			logger.Error("relay: server-id conflict; shutting down daemon",
				"server_id", string(serverID), "err", err)
			// Cancel WITH the conflict as cause: a self-initiated fatal
			// shutdown must exit non-zero so launchd (KeepAlive
			// SuccessfulExit:false) restarts the daemon into a fresh
			// window, rather than reading a clean exit 0 as intentional
			// and stranding it (the 2026-07-16 outage's second half).
			w.shutdown(err)
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
	return cleanup, surface, announce, announceConversation, nil
}

// relay4409Threshold reads the PYRY_RELAY_4409_THRESHOLD test-only seam: a
// positive integer overrides the production count of consecutive 4409 closes
// required before the daemon treats a server-id conflict as fatal (see
// relay.Config.ServerIDConflictThreshold). Unset, empty, zero, or unparseable
// returns 0, which leaves the production default (8). The e2e suite sets it
// small so the persistent-conflict → non-zero-exit path is reachable in a few
// seconds of backoff. Mirrors the PYRY_ALLOW_INSECURE_RELAY seam: dev/test
// only, never set by production.
func relay4409Threshold(logger *slog.Logger) int {
	raw := os.Getenv("PYRY_RELAY_4409_THRESHOLD")
	if raw == "" {
		return 0
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		logger.Warn("relay: ignoring invalid PYRY_RELAY_4409_THRESHOLD", "value", raw)
		return 0
	}
	logger.Info("relay: PYRY_RELAY_4409_THRESHOLD override", "threshold", n)
	return n
}

// boundSessionIDForActive resolves the pool session id bound to the ACTIVE
// conversation, for the stream turn drain's AC2 scoping gate (#1081). It mirrors
// the follow-active cursor the PTY emitter reads (active.CurrentConversation) and
// the conv.CurrentSessionID == "" isolation guard resolveBoundRunner /
// resolveBoundSession enforce (#678): Pool.Lookup("") would return the BOOTSTRAP
// session, so an unbound conversation must be rejected here, not fall through to a
// bootstrap default.
//
// Returns ("", false) when there is no active conversation, it is unknown, or it
// is unbound — the drain then drops every event (fail-closed), exactly as the
// emitter drops on an empty cursor. It deliberately SKIPS pool.Lookup (unlike
// boundHost): the drain compares this id against the producing runner's
// construction-time tag, so a stale id simply matches no live producer and
// forwarding nothing is the fail-closed outcome — no cross-session leak, and no
// *sessions.Pool dependency dragged into this resolver.
func boundSessionIDForActive(active *activeConversation, convReg *conversations.Registry) (sessionID string, ok bool) {
	convID := active.CurrentConversation()
	if convID == "" {
		return "", false
	}
	conv, found := convReg.Get(conversations.ConversationID(convID))
	if !found || conv.CurrentSessionID == "" {
		return "", false
	}
	return conv.CurrentSessionID, true
}

// runConfigFor composes the two halves of the conversation-keyed
// run-configuration seam (#1609) into the primitive-typed value that crosses into
// internal/relay as V2SessionConfig.RunConfigFor: the settings half (resolve —
// main.go's resolveBoundRunSettings: registry → bound session id → that session's
// model/effort/YOLO) and the context-window half (usage — the by-id reader
// snapshotUsageFor returns). Same shape as boundSessionIDForActive and
// bootstrapSnapshotUsage: a named, unit-testable resolver pulled out of otherwise
// untestable wiring.
//
// The order is the security property, not a convenience. usage is consulted ONLY
// after resolve says yes, and only with the session id resolve returned — never
// with the caller's conversation id and never with "". So the id that reaches
// transcript.StatByID is always one the pool holds, taken from the daemon's own
// registry record, never a string a caller supplied.
//
// resolve == nil ⇒ nil, decided at BUILD time before any closure exists, so "no
// path can invoke a nil resolver" is structural rather than a promise (the
// bootstrapSnapshotUsage pattern). Foreground / v1.
//
// usage == nil is deliberately NOT the same, and this is the spot the neighbouring
// shape is close enough to be pattern-matched wrong: bootstrapSnapshotUsage
// collapses to nil when EITHER half is unwired, because both are required to
// report anything at all, whereas here a nil usage half yields a WORKING seam
// whose resolved answers carry UsedTokens/WindowTokens at zero. A daemon with no
// sessions directory still hosts conversations bound to real sessions with real
// settings; an unwired usage half degrades two integers and must not make a
// resolved conversation unresolvable.
func runConfigFor(
	resolve func(convID string) (boundRunSettings, bool),
	usage func(sessionID string) (usedTokens, windowTokens int),
) func(convID string) (relay.RunConfig, bool) {
	if resolve == nil {
		return nil
	}
	return func(convID string) (relay.RunConfig, bool) {
		b, ok := resolve(convID)
		if !ok {
			return relay.RunConfig{}, false
		}
		cfg := relay.RunConfig{
			SessionID:      b.sessionID,
			Model:          b.model,
			Effort:         b.effort,
			YOLO:           b.yolo,
			PermissionMode: b.permissionMode,
		}
		if usage != nil {
			cfg.UsedTokens, cfg.WindowTokens = usage(b.sessionID)
		}
		return cfg, true
	}
}

// systemPromptFor composes the conversation system-prompt read seam (#2152) into
// the primitive-typed value that crosses into internal/relay as
// V2SessionConfig.SystemPromptFor: it takes the resolution half (main.go's
// resolveConversationPrompt — registry → stored prompt, plus bound session → the
// text that session was spawned with) and shapes it into a marshal-ready
// protocol.SystemPromptPayload. Same shape as runConfigFor above: a named,
// unit-testable adapter pulled out of otherwise untestable wiring.
//
// resolve == nil ⇒ nil, decided at BUILD time before any closure exists, so "no
// path can invoke a nil resolver" is structural rather than a promise —
// runConfigFor's rule, and the reason a wrapper must never be assigned
// unconditionally into the config literal (a wrapper is non-nil even when the
// field it closes over is nil, which would silently defeat the seam's nil
// contract).
//
// The comma-ok crosses UNTOUCHED. false means the daemon does not host the named
// conversation, and internal/relay turns that into its constant no-session reply;
// this adapter must not pre-empt that by inventing a payload for it, and it must
// not fold "hosted but running nothing" into it either — that case is a true
// resolution whose verdict is no_session and whose stored value still travels.
func systemPromptFor(resolve func(convID string) (conversationPromptState, bool)) func(convID string) (protocol.SystemPromptPayload, bool) {
	if resolve == nil {
		return nil
	}
	return func(convID string) (protocol.SystemPromptPayload, bool) {
		st, ok := resolve(convID)
		if !ok {
			return protocol.SystemPromptPayload{}, false
		}
		return protocol.SystemPromptPayload{
			SystemPrompt:        st.stored,
			SessionPromptStatus: systemPromptStatus(st),
		}, true
	}
}

// systemPromptStatus computes the three-value verdict a system_prompt reply
// carries: whether the running session was spawned with the stored value, with a
// different one, or whether there is no running session to compare against.
//
// THE COLLAPSE IS THE WHOLE FUNCTION, and skipping it is the single most likely
// way this verb ships wrong. The registry stores a TRI-state — nil is "no prompt",
// a non-nil pointer to "" is the explicitly-empty state a client can mint through
// set_system_prompt, and otherwise text — while Pool.SystemPromptFor returns "" for
// BOTH no-bytes states by design, because composing the daemon's own prompt with
// the operator's is the sessions package's business and a caller comparing the
// composed text would have to strip a constant it does not own. So the comparison
// runs on the COLLAPSED stored value: a conversation storing an explicitly empty
// prompt whose session spawned with no operator text MATCHES. Comparing the
// pointer's presence against the spawned-with string instead — `st.stored != nil`
// as a proxy for "has bytes" — reports that pair as differing, and an operator
// would be told a session is stale that is running exactly what they stored.
//
// spawnedWith nil is the ONLY spelling of "nothing to compare against". A non-nil
// pointer to "" is a real answer meaning the session was spawned with no operator
// text, and it participates in the comparison like any other value; conflating the
// two would report every bare session as unresolvable.
func systemPromptStatus(st conversationPromptState) string {
	if st.spawnedWith == nil {
		return protocol.SystemPromptStatusNoSession
	}
	stored := ""
	if st.stored != nil {
		stored = *st.stored
	}
	if stored == *st.spawnedWith {
		return protocol.SystemPromptStatusMatches
	}
	return protocol.SystemPromptStatusDiffers
}

// startRelayV2 wires the Mobile Protocol v2 (Noise_IK E2E) dispatch leg: it
// loads the binary's persistent static keypair, builds a V2SessionManager
// against conn.Frames() registering the conversation / messaging / workspace /
// push-token handler set that dispatch.Route consults, and runs the manager in
// one goroutine. The returned drain func blocks
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
// already disables reconcile, so disabling the producer too is coherent).
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
) (drain func(), surface func(permbridge.Request) func(), announce func(conversationID, attachmentID, filename string), announceConversation func(protocol.ConversationUpdatedPayload), err error) {
	staticKey, err := keys.LoadOrCreate(resolveStaticKeyBaseDir(), sanitizeName(w.instanceName))
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("load static key: %w", err)
	}
	priv := staticKey.PrivateKey()

	// Daemon-singleton outstanding-modal registry. Its sole live producer is the
	// stream-json approval bridge's Surface (#1080, newStreamApprovalBridge below),
	// which Records a raised approval here; the consumers are the inbound resolver
	// newModalResolverV2 builds (ModalResolver seam, including deny-on-timeout) and
	// the connect-time replay source (the OutstandingModals field below, which is
	// this registry's Snapshot). All three sit on this same instance.
	modalReg := modalbridge.New()

	// Daemon-singleton store of surfaced-but-unretired clarifying-question batches
	// (#1975). Minted HERE beside modalReg rather than inside the bridge, for the
	// reason modalReg is: #1928's connect-time reconcile reads its Snapshot from
	// the manager config assembled below, which is built before the bridge exists.
	// Its only live producer today is that bridge's Surface, wired after
	// construction below; the answer path (#1907) is the second consumer.
	questionReg := questionbridge.New()

	// Inbound modal-control resolver (#727). Constructed here (not inline in the
	// config literal below) so its #1014 emit seams can be set: a trust deny /
	// deny-on-timeout surfaces a folder-not-trusted session_error via the shared
	// blockedNotify closure, stamped with the active conversation (the same
	// follow-active cursor the modal producer resolves its target from). Both
	// seams are nil in foreground/v1, leaving the pre-#1014 behaviour intact.
	//
	// The keystroker is nil-safe-wrapped (#1131): PTY mode passes w.sup straight
	// through, but on the stream-json bootstrap path w.sup is a typed-nil
	// The terminal keystroker argument is gone with #1348: ResolveCancel and
	// ResolveTimeout used it to press escape at a terminal modal, and there is no
	// terminal. A stream approval denies fail-closed through the permission
	// bridge's deny-on-timeout (#1103), which is the only path left.
	modalResolver := newModalResolverV2(modalReg, noopKeystroker{}, logger)
	modalResolver.activeConv = w.active.CurrentConversation
	modalResolver.notifyBlocked = w.blockedNotify

	// Inbound question-control resolver (#1986): the per-device authorization gate
	// for an inbound question_answer / question_refused, and the audit record of
	// every decision it makes. Constructed here over the batch registry minted
	// above — which exists before the manager, where its actuator cannot — and its
	// bridge field is assigned once that bridge exists, the split modalResolver's
	// streamApprovals uses for the same ordering reason.
	//
	// CONSTRUCTED AND ASSIGNED UNCONDITIONALLY, deliberately. Building it only
	// under a non-nil w.approvals and assigning the (typed-nil) pointer into the
	// interface field below would make relay's `QuestionResolver == nil` guard read
	// FALSE — the handler would call methods on a nil receiver instead of treating
	// the frame as inert. That is the typed-nil hazard bridge.toolCallInFlight's
	// guard documents, arriving through an interface rather than a method value.
	// Inertness therefore lives inside the resolver, where a nil actuator is a plain
	// field compare: with no stream-approval bridge (foreground / PTY) both arms
	// resolve nothing and nothing panics.
	questionResolver := newQuestionResolverV2(questionReg, logger)

	// Inbound attachment-upload service (#1897): the one thing the v2 session
	// manager calls for an attachment_chunk, and internal/attachments' first
	// caller from outside its own package.
	//
	// EXACTLY ONE PER DAEMON, which is what makes the registry's in-flight
	// ceiling a daemon-wide bound rather than a per-something one. Minted here
	// beside modalReg / questionReg for the same reason those are: the manager
	// config below needs it, and it must exist before the manager does.
	//
	// IT TAKES NO CONVERSATION RESOLVER since #2143. It held one over the
	// follow-active cursor — the same one modalResolver.activeConv reads — and
	// that cursor is stamped only by a successful send_message route, so an
	// attachment added before a conversation's first message could never be
	// stored and one added after a switch was filed under the wrong conversation.
	// The destination arrives per transfer off attachment_chunk instead, gated by
	// the KnownConversation membership check this same config already wires
	// below, which is what discharges Receive's caller-side precondition.
	attachmentIntake := attachments.NewIntake(resolveInstanceDirPath(w.instanceName))

	// Attachment resolver seam for send_message (#2038): the READ half of the
	// upload leg above, over the same instance directory the intake writes into.
	//
	// It adapts attachments.ResolvePath to handlers.AttachmentResolver's comma-ok
	// shape, and THIS IS THE ONLY SCOPE THAT EVER HOLDS THE RESOLVER'S ERROR. The
	// adaptation is a discard rather than a loss: ResolvePath answers exactly one
	// sentinel for an unknown id, a non-canonical id and an id stored under
	// another conversation alike, so nothing downstream could branch on it — while
	// its shape-invalid refusal formats the RAW client-supplied attachment id into
	// its message, which docs/protocol-mobile.md § Attachments forbids logging
	// (raw, an element is an arbitrary client string in a line-oriented log). The
	// error dies here, unlogged, so the handler cannot disclose what it never
	// receives. Nothing is logged on the success path either: the resolved path's
	// leaf is a client filename, which the same section bans logging for a privacy
	// reason sanitising does not lift.
	//
	// conversationID arrives from the handler, which has already validated it
	// against the registry binding via SessionRouter.Route — ResolvePath's stated
	// precondition, and the confinement property that keeps a documented
	// non-capability from becoming one.
	attachmentResolve := func(conversationID, attachmentID string) (string, bool) {
		path, err := attachments.ResolvePath(
			resolveInstanceDirPath(w.instanceName),
			conversations.ConversationID(conversationID),
			attachmentID,
		)
		if err != nil {
			return "", false
		}
		return path, true
	}

	// Context-window usage reader (#857, rebuilt by #1214): reports the bootstrap
	// session's current occupancy (used tokens + window size) for the
	// screen_snapshot reply. session_settings read it too between #491 and #1610,
	// and now sources all six of its fields from the conversation-keyed seam below.
	snapshotUsage := bootstrapSnapshotUsage(w.claudeSessionsDir, w.bootstrapIDFn, w.modelWindows)

	// Conversation-keyed run-configuration seam (#1609): composes the settings half
	// (main.go's resolveBoundRunSettings, over the conversations registry and the
	// pool) with the by-id context-window reader, so ONE call reports a named
	// conversation's own session id, model/effort/YOLO and occupancy together.
	// snapshotUsageFor is called a second time over the same directory rather than
	// reusing the binding above: bootstrapSnapshotUsage supplies the BOOTSTRAP id,
	// so a seam composed through it would report the bootstrap's occupancy for
	// every conversation. The closure it builds is stateless, so a second one costs
	// nothing and leaves the three existing seams byte-identical. BOTH readers are
	// handed the same w.modelWindows, so the two surfaces report one window for
	// one session; wiring only one would make them disagree (#2107 AC 1).
	runConfig := runConfigFor(w.runSettings, snapshotUsageFor(w.claudeSessionsDir, w.modelWindows))
	// The system-prompt read seam (#2152), composed the same way and for the same
	// reason: the resolution half is cmd/pyry-typed and the seam is not, so the
	// shaping happens here rather than at the composition root, which does not
	// import internal/protocol.
	systemPrompt := systemPromptFor(w.promptState)

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
			protocol.TypeListConversations:  handlers.ListConversations(w.convReg),
			protocol.TypeCreateConversation: handlers.CreateConversation(w.convReg, w.creator, resolveConversationsRegistryPath(w.instanceName), w.defaultCwd, logger),
			protocol.TypeRenameConversation: handlers.RenameConversation(w.convReg, resolveConversationsRegistryPath(w.instanceName), logger),
			// rename_workspace is the workspace-keyed sibling of the line above
			// (#2207). It takes no session surface for the same reason
			// set_system_prompt does not: naming a folder must not disturb anything
			// running in it, and a handler holding no pool or runner seam cannot.
			protocol.TypeRenameWorkspace:       handlers.RenameWorkspace(w.convReg, resolveConversationsRegistryPath(w.instanceName), logger),
			protocol.TypePromoteConversation:   handlers.PromoteConversation(w.convReg, resolveConversationsRegistryPath(w.instanceName), logger),
			protocol.TypeDeleteConversation:    handlers.DeleteConversation(w.convReg, resolveConversationsRegistryPath(w.instanceName), logger),
			protocol.TypeArchiveConversation:   handlers.ArchiveConversation(w.convReg, resolveConversationsRegistryPath(w.instanceName), logger, true),
			protocol.TypeUnarchiveConversation: handlers.ArchiveConversation(w.convReg, resolveConversationsRegistryPath(w.instanceName), logger, false),
			protocol.TypeChangeWorkspace:       handlers.ChangeWorkspace(w.convReg, resolveWorkspaceDir, resolveConversationsRegistryPath(w.instanceName), logger),
			// set_system_prompt takes no session surface (#2151): the value's route
			// to a running child is the registry, re-read at the pool's own spawn
			// funnel by #2150's refreshSystemPrompt. Wiring a pool or runner in here
			// would build the restart this verb is specified NOT to do.
			protocol.TypeSetSystemPrompt:       handlers.SetSystemPrompt(w.convReg, resolveConversationsRegistryPath(w.instanceName), logger),
			protocol.TypeCreateWorkspaceFolder: handlers.CreateWorkspaceFolder(resolveWorkspaceFolder, logger),
			protocol.TypeRecentWorkspaces:      handlers.RecentWorkspaces(w.convReg),
			protocol.TypeRegisterPushToken:     handlers.RegisterPushToken(registry, resolveDevicesPath(w.instanceName), logger),
			protocol.TypeSendMessage:           handlers.SendMessage(w.router, w.queue, attachmentResolve, logger),
		},
		// Screen-snapshot seam (#618): the supervisor renders the live screen
		// inside the tui-driver seal; KnownConversation gates request_snapshot
		// on registry membership (AC #4), mirroring the established
		// conversations-registry validation pattern but returning a bool so the
		// relay needs no conversations import or errors.Is coupling.
		// Screen snapshot answered by photographing claude's terminal, so it goes
		// with the terminal (#1348). A nil Snapshotter lands the request in the
		// handler's existing offline arm, AFTER the KnownConversation gate, so a
		// foreign conversation id still returns not-found rather than leaking an
		// existence oracle (#1101).
		Snapshotter: nil,
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
		// Context-window usage reader (#857, rebuilt by #1214): populates the
		// used_tokens / window_tokens on the screen_snapshot reply from the
		// bootstrap session's current occupancy, so a client can render an
		// "N% used (X of Y)" gauge (desktop#182). The
		// closure (bootstrapSnapshotUsage, built above: the by-id reader
		// snapshotUsageFor returns — transcript.StatByID + contextwindow.Read —
		// bound to the bootstrap id source) returns two primitives, so
		// internal/relay imports neither internal/contextwindow nor
		// internal/sessions. Read-only reflection — no secret, no authz. nil in
		// foreground / unresolved sessions dir / no id source makes the handlers
		// report zeros (used_tokens:0, window_tokens:0).
		SnapshotUsage: snapshotUsage,
		// Conversation-keyed run configuration (#1609, consulted since #1610): one
		// seam reporting a NAMED conversation's own bound session id, model / effort
		// / YOLO and context-window figures together, composed at runConfigFor. It
		// is handleRequestSessionSettings' only run-configuration source, so the
		// session id a client is handed — and writes its set_session_settings back
		// to — is the one bound to the conversation it named, never the shared
		// bootstrap session. It replaced a bootstrap-scoped session-id seam that
		// named the session the two seams above describe. An unresolvable
		// conversation gets ok=false and addresses nothing. nil in foreground/v1 (no
		// settings resolver wired).
		RunConfigFor: runConfig,
		// On-demand model-list source (#2125): resolves the NAMED conversation's model
		// menu for an inbound request_model_list, so a client can ask at any time
		// instead of waiting for the live turn lane (which drops every event whose
		// producing session is not the active conversation's) or for the next connect
		// (whose reconcile cannot include a conversation that did not exist at
		// handshake time). That window is what leaves a freshly created chat's model
		// and effort menus blank (pyrycode-desktop#1054), and #2085 makes it universal.
		//
		// It reaches the same resolver RetainedModelLists below enumerates over, so the
		// answer is the same one the connect-time reconcile would send for that
		// conversation — same SOURCE, not the same fields copied. Assigned straight
		// through rather than wrapped in a closure, for RetainedModelLists' stated
		// reason: a wrapper would be non-nil even when the field is nil and would
		// silently defeat the seam's nil ⇒ refuse contract. A pure read: it mints no
		// id and mutates no daemon state.
		ModelListFor: w.modelListFor,
		// The conversation system-prompt read seam (#2152): what the registry stores
		// for the NAMED conversation, plus a verdict on whether the session it is
		// bound to was spawned with that same value — the gap #2151's store-and-
		// apply-at-next-start design opens, and which an operator editing a prompt
		// while typing at a live child cannot otherwise see. Composed above rather
		// than assigned from w directly, exactly as RunConfigFor is, because the
		// resolution half is cmd/pyry-typed; systemPromptFor collapses to nil when
		// that half is unwired, so no wrapper can defeat the seam's nil contract. A
		// pure read: it holds nothing that can start, restart, rotate or interrupt a
		// session, which is what keeps reading a prompt from touching a running one.
		SystemPromptFor: systemPrompt,
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
		// Connect-time model-list reconcile source (#1867): enumerates the model menu
		// each conversation's bound session retained from its child's initialize reply
		// (#1839/#1840) as marshal-ready model_list payloads, so a client that attaches
		// AFTER that exchange is unicast the current menu on open instead of having to
		// send a message first to discover which models exist. The live turn lane
		// (#1849) reaches only a client that was already connected — three independent
		// loss points sit in front of it (reconcileModelLists names them). Assigned
		// straight through rather than wrapped in a closure: a wrapper would be non-nil
		// even when the field is nil and would silently defeat the seam's
		// nil ⇒ no-reconcile contract, which every foreground/v1 and test wiring relies
		// on. A pure read: it mints no id and mutates no daemon state.
		RetainedModelLists: w.retainedModelLists,
		// Connect-time question reconcile source (#1980): enumerates the daemon's
		// surfaced-but-unretired clarifying-question batches as marshal-ready
		// question_shown payloads, so a client that connects or reconnects while claude
		// is waiting on an AskUserQuestion is unicast the outstanding batch on open
		// instead of never learning it exists. The raise-time broadcast (#1973) reaches
		// only whoever was connected at that instant and the frame carries no event id,
		// so it is not in the #647 replay ring either — this is the only path to a late
		// client. questionReg is the same daemon-singleton minted above that the stream
		// approval bridge's surfacer Records into and the answer path (#1907) resolves
		// against, so enumerate-current-truth reflects live control state.
		//
		// Snapshot and not Resolve: the read retires nothing, so a batch outstanding
		// across a reconnect is re-sent and stays answerable exactly once — the one-shot
		// consume that governs answerability is Resolve's, and this path never calls it —
		// while a batch resolved or dismissed meanwhile is simply absent from the read.
		// Assigned straight through rather than wrapped in a closure, matching
		// OutstandingModals above; RetainedModelLists' stated reason for the same choice
		// (a wrapper stays non-nil when the underlying field is nil, defeating the seam's
		// nil ⇒ no-reconcile contract) does NOT apply here, because questionReg is minted
		// unconditionally and no nil is reachable at this site.
		OutstandingQuestions: questionReg.Snapshot,
		// Connect-time slash-command-list reconcile source (#2007): enumerates the
		// command menu each conversation's bound session retained from its child's
		// initialize reply (#2004/#2005) as marshal-ready slash_command_list payloads,
		// so a client that attaches AFTER that exchange is unicast the current menu on
		// open instead of showing an empty command list until a turn that may never
		// come. The live turn lane reaches only a client that was already connected —
		// three independent loss points sit in front of it (reconcileSlashCommandLists
		// names them). Assigned straight through rather than wrapped in a closure, for
		// RetainedModelLists' stated reason: a wrapper would be non-nil even when the
		// field is nil and would silently defeat the seam's nil ⇒ no-reconcile
		// contract, which every foreground/v1 and test wiring relies on. A pure read:
		// it mints no id and mutates no daemon state.
		RetainedSlashCommandLists: w.retainedSlashCommandLists,
		// Connect-time background-task-roster reconcile source (#2079): enumerates the
		// roster each conversation's bound session retained from its child's last
		// background_tasks_changed report (#2077), so a client that attaches to a
		// long-running daemon sees what is still running instead of an empty panel that
		// only fills when claude next CHANGES the roster — which on a quiet session may
		// never happen. The live turn lane reaches only a client that was already
		// connected (reconcileBackgroundTaskRosters names the loss points). Assigned
		// straight through rather than wrapped in a closure, for RetainedModelLists'
		// stated reason: a wrapper would be non-nil even when the field is nil and would
		// silently defeat the seam's nil ⇒ no-reconcile contract, which every
		// foreground/v1 and test wiring relies on. A pure read: it mints no id and
		// mutates no daemon state, so connecting twice delivers the same payloads.
		//
		// Unlike its five predecessors an EMPTY payload is meaningful on this seam and is
		// sent rather than filtered: an empty roster says nothing is alive, which is the
		// signal a client needs. Both halves of that contract are the producer's and the
		// reconcile's; nothing is decided at this assignment.
		RetainedBackgroundTaskRosters: w.retainedBackgroundTaskRosters,
		// Inbound modal-control resolver (#727): consumes the outstanding-modal
		// registry, routes the resolving keystroke via the supervisor safe-answer
		// seam, and audits. The keystroker is nil-safe-wrapped (#1131): PTY mode
		// passes w.sup (*supervisor.Supervisor, satisfies modalKeystroker) straight
		// through; the stream-json bootstrap path (typed-nil w.sup, #1077) gets a
		// no-op keystroker whose ESC is moot — a stream-json approval has no PTY
		// modal to dismiss and denies fail-closed via the permbridge timeout (#1103).
		// Constructed above so its #1014 folder-not-trusted emit seams are set first.
		ModalResolver: modalResolver,
		// Inbound question-control resolver (#1986), discharging the seam's
		// written ordering obligation: nothing may be wired here until the
		// per-device answer gate exists, because the relay handler applies no
		// authorization at all and what kept the #1984 interception fail-safe was
		// this field being nil at every construction site. The resolver
		// constructed above IS that gate — it looks the batch up, denies an
		// ineligible device before anything is consumed, records the decision, and
		// only then hands the batch to the daemon-side primitives.
		QuestionResolver: questionResolver,
		// Inbound attachment-upload seam (#1897), the intake constructed above.
		// Wired unconditionally: the seam's whole security surface is inside
		// internal/attachments — the declaration cross-check, both resource
		// bounds, the id-shape validation and the containment check — and the
		// relay handler adds no authorization of its own, deliberately, so there
		// is no gate this must wait behind. The conversation a chunk lands under
		// comes from this construction-time resolver and never from the frame,
		// which is what makes "a client cannot steer bytes into another
		// conversation" structural rather than checked.
		AttachmentIntake: attachmentIntake,
		// Inbound attachment-RETRIEVAL seam (#2054): the read half of the leg
		// above, wired to the SAME closure handlers.SendMessage already uses, over
		// the same instance directory the intake writes into. Reuse rather than a
		// second adapter — the closure's shape is already this seam's — and the
		// closure now serves two callers whose registry validation differs: this
		// one validates through KnownConversation above (membership alone, which
		// reads a known but UNBOUND conversation as addressable — the correct
		// reading for retrieval, and precisely the reopened conversation a
		// SessionRouter.Route check would refuse), send_message through
		// SessionRouter.Route. The closure itself discharges NEITHER; it wraps
		// attachments.ResolvePath, whose stated precondition is that the caller
		// already validated the conversation id.
		AttachmentResolve: attachmentResolve,
		// Inbound conversation-HISTORY seam (#2116): the read half of the durable
		// log #2114 and #2115 append to, over w.hist — the daemon's ONE store,
		// minted at the composition root, so a served page and a just-appended
		// entry cannot disagree about ids. Wired unconditionally: newHistoryPager
		// answers a nil store as unavailable rather than leaving the seam nil, and
		// the difference is load-bearing — a nil SEAM makes the verb inert and
		// parses nothing, which is the posture for a build with no history at all,
		// not for a daemon whose store failed to open.
		//
		// It validates through KnownConversation above, membership alone, and that
		// is the correct reading here for the same reason it is for attachment
		// retrieval and the opposite reason it would be for send_message: a
		// SessionRouter.Route check refuses a known conversation with NO BOUND
		// SESSION, which is precisely the reopened conversation this verb exists to
		// serve, and would answer it the retryable server.binary_offline.
		HistoryPage: newHistoryPager(w.hist, logger),
		// Inbound interrupt seam (#707): an interactive `interrupt` frame routes to
		// the runner bound to the ACTIVE conversation (#1121) — not the bootstrap
		// supervisor. The activeInterrupter adapter (main.go) resolves active →
		// CurrentSessionID → Pool.Lookup → runner, then actuates Interrupt
		// (stream-json, #1120) when the runner exposes it and stays inert when it
		// does not; it satisfies Interrupter via activeInterrupter.SendEsc, the
		// seam's abstract "claude's own interrupt" name.
		Interrupter: w.activeInterrupter,
		// Inbound new_session seam (#831): an interactive `new_session` frame
		// routes to the runner bound to the ACTIVE conversation (#1125) — not the
		// bootstrap supervisor. The activeSessionStarter adapter (main.go) resolves
		// active → CurrentSessionID → Pool.Lookup → runner, then for a
		// *streamsup.Runner rotates the pool-side id (Pool.RotateForNewSession) and
		// RestartFreshes into --session-id <newID> with NO /clear.
		SessionStarter: w.activeSessionStarter,
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
		// then live-applies it to a running session — in-band as a /model or /effort
		// command, or a set_permission_mode control request for ANY of the six
		// storable postures, the bypass enable included since #2066 (#1581, #1604);
		// by live restart only for a model or effort cleared back to claude's own
		// default (#842) — and installs the recomposed argv for the next spawn
		// (#833).
		// settingsUpdaterAdapter (built
		// at main.go over the pool) maps sessions.ErrSessionNotFound → the relay
		// sentinel, so internal/relay imports neither internal/sessions nor cmd/pyry.
		// nil in foreground / v1 makes the verb reply "unavailable" deterministically.
		SettingsUpdater: w.settings,
	})
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("build v2 session manager: %w", err)
	}

	// Attachment-offer announcer (#2166): the producer half of a frame #2082
	// published with nothing emitting it. Once attachment.file stores a file
	// claude named, this tells every interactive client the file exists, so a
	// client can draw it and fetch it with request_attachment — the id used to
	// exist only in the control-socket reply that went back to claude.
	//
	// OUTSIDE the `if w.approvals != nil` block below, deliberately. It needs mgr
	// and nothing else; putting it in there would tie announcing a stored file to
	// the presence of a permission bridge, two capabilities with no relationship.
	// Its route out is the same as that block's surface: a bare func returned to
	// the composition root, which hands it to fileAttacher.
	//
	// Built AFTER mgr and BEFORE mgr.Run's goroutine starts below, the window
	// streamApprovals and the bridge's post-construction assignments use — so
	// nothing the Run goroutine reads is written concurrently.
	announce = newAttachmentOfferEmitterV2(mgr, ctx, logger).announce

	// Host-side conversation announcer (#2156): the first unsolicited producer of
	// conversation_updated, a frame whose four existing producers all answer their
	// requester. Once `pyry channel new` writes the row, this tells every
	// interactive client, which re-lists on it and draws the channel — before,
	// nothing prompted a re-list and the row stayed invisible until the client's
	// next connect.
	//
	// Built beside the offer emitter and outside the `if w.approvals != nil` block
	// for that emitter's reason: it needs mgr and nothing else, and tying a
	// created channel's announcement to the presence of a permission bridge would
	// join two capabilities with no relationship. Same window too — after mgr,
	// before mgr.Run's goroutine starts below — so nothing that goroutine reads is
	// written concurrently. Its route out is the same: a bare func returned to the
	// composition root, which hands it to channelCreator.
	announceConversation = newConversationUpdateEmitterV2(mgr, ctx, logger).announce

	// Stream-json approval bridge (#1080): joins the daemon-singleton permbridge
	// parked-approval store (claude-facing completers, keyed by tool_use_id) to
	// modalReg (client-facing modal_shown, keyed by modal_id), owning the
	// modal_id ⇄ tool_use_id correlation. Surface (returned to the control server)
	// raises a parked approval as the SAME permission modal_shown clients already
	// answer; the resolver's stream arm resolves the parked completer on a
	// modal_answer. Constructed AFTER mgr (its interactive broadcaster) and BEFORE
	// mgr.Run starts below, so streamApprovals is set before any modal_answer can
	// dispatch on the Run goroutine — no data race on the resolver field. nil
	// approvals (foreground/v1) leaves streamApprovals nil ⇒ keystroke-only.
	if w.approvals != nil {
		bridge := newStreamApprovalBridge(w.approvals, modalReg, mgr, w.active.CurrentConversation, ctx, logger)
		// The question arm (#1973), assigned after construction rather than passed
		// in — the same shape toolCallInFlight and modalResolver.activeConv /
		// notifyBlocked use, and the only way to keep the constructor's fifteen call
		// sites untouched. NO NIL GUARD is needed or wanted: questionReg is minted
		// unconditionally above. Leaving it unset is what every other construction in
		// the tree does, and there a question surfaces as the permission modal it did
		// before this slice. Set before mgr.Run's goroutine starts below, like
		// streamApprovals — no data race on the field.
		bridge.questions = questionReg
		// The #1919 report's membership half, assigned after construction rather
		// than passed in — the same shape modalResolver.activeConv/notifyBlocked use
		// above, and the only way to keep the constructor's 14 call sites untouched.
		//
		// THE GUARD IS MANDATORY, not defensive padding. A method value on a nil
		// *turnBusyTracker is a NON-NIL func that panics on its first call:
		// ToolCallInFlight takes the tracker's mutex immediately and carries no
		// receiver guard, by #1917's explicit decision. Unguarded, this assignment
		// would defeat ApprovalParked's nil short-circuit entirely and turn PTY mode
		// — where w.busy is nil, the composition root minting it only alongside
		// streamSink — from "reports negative" into "panics on the first consumer
		// read". It is the typed-nil-in-an-interface hazard the tracker's observe
		// documents, arriving through a method value instead of an interface.
		//
		// Set before mgr.Run's goroutine starts below, like streamApprovals — no
		// data race on the field.
		if w.busy != nil {
			bridge.toolCallInFlight = w.busy.ToolCallInFlight
			// The report's consumer side (#1911): the delivery seam's give-up
			// exemption. Published under the SAME guard rather than beside the
			// constructor because with no tracker the bridge answers negative for
			// every conversation anyway — setting it there would be exactly as
			// informative — and keeping the pair adjacent keeps the two halves of
			// this knot readable as one. set is written before mgr.Run's goroutine
			// starts below, which is the first link of the happens-before chain
			// approvalParkedReport's doc records for its unguarded field.
			w.approvalParked.set(bridge.ApprovalParked)
		}
		// The approval-liveness report (#1932): permbridge's fail-closed window
		// stops being a hard deadline and becomes a re-check interval — expire asks
		// this on every expiry and re-arms the SAME window while somebody can still
		// answer, so a prompt keeps waiting for the human walking to their desk and
		// denies within one window of the last answerer going away.
		//
		// OUTER BRANCH, deliberately NOT beside the two assignments under the
		// w.busy guard above. Those are guarded because a method value on a nil
		// *turnBusyTracker is a non-nil func that panics on first call.
		// ApprovalAnswerable reads only the bridge's own modal correlation and the
		// broadcaster — never the turn-busy tracker — so gating it on w.busy would
		// silently disable the extension in PTY mode for no reason at all.
		//
		// NO NIL GUARD AND NO WRAPPER CLOSURE. AnswerableFunc assigns the
		// no-panic duty to its injection site, and it is discharged STRUCTURALLY
		// here: bridge was constructed on the line above and is non-nil, and its
		// bcast is mgr, also non-nil — the same reasoning that leaves
		// ApprovalAnswerable carrying no b.bcast guard. A defensive
		// `func(id string) bool { if bridge == nil { … } }` would be unreachable
		// code that turns a future genuine nil into "nobody can answer", i.e. every
		// approval silently denying, which is precisely the failure mode the seam's
		// doc forbids. A moved assignment must stay after newStreamApprovalBridge.
		//
		// The registry outliving this leg is safe in both directions: startRelay
		// returns early when relayURL is empty, so SetAnswerable is never called and
		// the window stays the hard deadline it has always been; and at teardown
		// ActiveConns returns nil once the daemon ctx is cancelled or Run has exited,
		// so the report reads "nobody can answer" and every parked approval denies
		// within one window. The installed method value is safe to call for the whole
		// life of the registry, which is what the seam demands.
		w.approvals.SetAnswerable(bridge.ApprovalAnswerable)
		modalResolver.streamApprovals = bridge
		// The question resolver's actuator (#1986): the same bridge, whose
		// AnswerQuestion / RefuseQuestion own the batch consume, claude's verdict
		// and the single question_dismissed. Assigned here rather than passed to
		// newQuestionResolverV2 because the bridge is built after the manager and
		// the manager already holds the resolver. Set before mgr.Run's goroutine
		// starts below — the only reader — so there is no data race on the field,
		// the edge modalResolver.streamApprovals relies on one line up.
		questionResolver.bridge = bridge
		surface = bridge.Surface
	}

	mgrDone := make(chan struct{})
	go func() {
		defer close(mgrDone)
		if err := mgr.Run(ctx); err != nil {
			logger.Debug("relay: v2 manager run returned", "err", err)
		}
	}()

	// Wire the structured interactive turn stream (#633). There is one producer:
	// the stream-json turn drain, which feeds the #632 capability-gated emitter so
	// turn_state / assistant_delta / tool / turn_end envelopes reach interactive
	// phones. It is gated on w.streamSink != nil, the relay leg's stream-mode
	// discriminant — since #1348 deleted the terminal arm, stream mode is the only
	// mode with a turn producer at all.
	//
	// The cleanup is declared out here because it is assigned inside that branch
	// and called from the returned drain closure. Every other mode leaves it nil,
	// which is what the nil-guard at the call site is for.
	var streamDrainCleanup func()
	if w.streamSink != nil {
		// STREAM MODE (#1081): the turn stream is fed from the stream-json runner's
		// turnevent drain, startStreamTurnDrainV2, whose cleanup blocks until its
		// goroutine exits. The stream-mode counterpart of the modal stream #1348
		// deleted — the #1080 approval bridge — is already wired unconditionally
		// above (modalResolver.streamApprovals), so the turn stream is the only
		// producer this branch builds. It does NOT gate on claudeSessionsDir: the
		// drain consumes parsed turnevent.Events from the sink, not an on-disk
		// transcript, so it runs whenever stream mode is selected.
		//
		// The emitter is constructed here rather than inside the drain so this leg
		// owns the reconnect wiring: the active-conversation cursor it stamps with
		// and the session-scoped replay source it registers. SetReplaySource runs at
		// most once, because this branch does.
		emitter := newInteractiveTurnEmitterV2(w.active, mgr, logger)
		mgr.SetReplaySource(emitter.ring, w.active.CurrentConversation)
		// The durable conversation log (#2114), assigned the same way the ring is
		// reached one line up: newInteractiveTurnEmitterV2 has 86 call sites and a
		// positional parameter is not separable from them in Go. w.hist is minted
		// at the composition root, not here, because #2115's delivery-path
		// producer must consume THIS store rather than a second one over the same
		// instance directory — two stores mint duplicate ids for a conversation
		// and each can truncate away the other's just-appended entry.
		emitter.hist = w.hist
		// The drain's AC2 scoping gate follows the ACTIVE conversation's bound
		// session — the same follow-active cursor the PTY emitter reads, with the
		// #678 conv.CurrentSessionID == "" isolation guard resolveBoundSession
		// enforces. An unmatched/empty id forwards nothing (fail-closed).
		activeSession := func() (string, bool) { return boundSessionIDForActive(w.active, w.convReg) }
		// The #1201 per-conversation turn-busy tracker (w.busy), fed from the same
		// fan-in BEFORE the gate above, so a turn on a non-active conversation still
		// reports busy. It is minted at the composition root, not here, because the
		// inbound-delivery seam consumes it too (#1199) and that seam is built for
		// msgqueue.New — outside this leg entirely. Its resolve closure there is the
		// same session→conversation closure the session_transition producer uses below,
		// which inherits conversationForSession's SessionHistory match (a just-rotated
		// session still resolves) and keeps internal/conversations out of the tracker's
		// file.
		//
		// Every clear that must land does: TurnEnd on the fan-in, the #1202 teardown
		// transition below, the #1210 child-exit lane through the drain, and — for a
		// write that fails after its mark — the delivery seam's own undo (see
		// stream_turn_busy.go's feeds note).
		streamDrainCleanup = startStreamTurnDrainV2(ctx, w.streamSink, emitter, activeSession, w.busy, logger)
	}
	// The terminal-mode arm that stood here is gone with #1348. It read claude's
	// screen to produce the same turn and modal events the drain above produces
	// from claude's own structured output, and it was the only reason this leg
	// had a branch at all.

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
	// The pool's observer slot is single-valued, so #1202's turn-busy clear is
	// COMPOSED onto the emitter in there rather than installed separately. w.busy is
	// nil in PTY mode (the composition root mints it only alongside streamSink) and
	// the clear is nil-safe.
	streamTransitionsCleanup := startSessionTransitionStreamV2(ctx, w.transitions, mgr,
		func(sid string) (string, bool) { return conversationForSession(w.convReg, sid) }, w.busy, w.hist, logger)

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
		// Stop the producers — the structured turn stream's drain, the
		// session-transition producer, the queue_state producer, and the session_error
		// producer — before waiting on the manager so no fan-out races a winding-down
		// manager. Each cleanup waits for its goroutine on ctx-cancel (already
		// cancelled by the time drain runs). Then wait for the manager's Run to exit
		// on the closed Frames channel.
		if streamDrainCleanup != nil {
			streamDrainCleanup()
		}
		streamTransitionsCleanup()
		streamQueueStateCleanup()
		streamSessionErrCleanup()
		<-mgrDone
	}, surface, announce, announceConversation, nil
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
