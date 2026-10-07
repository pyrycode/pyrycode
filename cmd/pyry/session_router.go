package main

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/relay/handlers"
	"github.com/pyrycode/pyrycode/internal/sessions"
	"github.com/pyrycode/pyrycode/internal/streamsup"
	"github.com/pyrycode/pyrycode/internal/turnbridge"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

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

// mcpStatusQueryTimeout bounds one on-demand MCP status round trip, for
// effectiveEffortQueryTimeout's reason: a live child can stay silent forever.
const mcpStatusQueryTimeout = 30 * time.Second

type mcpStatusQuerier interface {
	QueryMCPStatus(context.Context) (turnevent.MCPStatus, bool)
}

// resolveBoundMCPStatus queries only the runner currently bound to convID and
// shapes its bounded event through the same mapper as the automatic live path.
// The request id remains lookup-only; the mapped payload is stamped with the
// registry-owned conversation id returned alongside the runner. Every refusal
// returns the zero payload; there is no retained-status fallback.
//
// The child round trip runs under mcpStatusQueryTimeout. QueryMCPStatus leaves the
// bound to its caller (actuateMCP's doc says why), and the relay passes the conn's
// ctx, which has no deadline — so without this a live child that never answers
// would hold the ask, and its per-conn slot, until the connection closed (#2702).
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
	queryCtx, cancel := context.WithTimeout(ctx, mcpStatusQueryTimeout)
	defer cancel()
	status, ok := querier.QueryMCPStatus(queryCtx)
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
// value crossing into relay.go carries no internal/sessions type — the same
// composition-root discipline settingsUpdaterAdapter and debugBundler keep.
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

// activeConversation holds the id of the conversation the operator is currently
// interacting with — the one most recently resolved by sessionRouter.Route. It
// is the structured turn stream's cursor source (#687): the emitter
// (`flushC`) and the #647 reconnect-replay source (registered in
// `startRelayV2` via SetReplaySource) read it instead of the bootstrap
// supervisor's CurrentConversation(), which #678 emptied for routed turns —
// those commit on bound-session supervisors and never touch the bootstrap cursor
// (docs/knowledge/codebase/678.md). Before any route the zero value is "", the
// well-defined "no conversation routed yet" state the emitter drops on.
//
// It is written on the routing-path goroutine (set, from Route) and read on the
// turn-stream drain goroutine (CurrentConversation — live emit + replay;
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
