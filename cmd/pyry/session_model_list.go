package main

import (
	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/sessions"
	"github.com/pyrycode/pyrycode/internal/turnbridge"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// resolveBoundModelList answers the model menu the named conversation's bound
// session currently holds (#1840), already shaped as a marshal-ready
// protocol.ModelListPayload, so a delivery path (#1858) can send it without
// reaching into session internals or re-deriving the untrusted-id rules for
// itself. It is COMPOSITION AND REFUSAL — every part it joins already exists.
//
// It is the FOURTH member of the conversation-keyed resolver family, after
// resolveBoundRunner, resolveBoundSession and resolveBoundRunSettings, and since
// #2124 it is the ONE MEMBER THAT DIVERGES FROM THE FAMILY'S REFUSAL. The other
// five inherit it verbatim; this one keeps exactly ONE hard refusal — the
// registry lookup — and answers every arm below it from the daemon-wide
// vocabulary instead. Read retainedModelVocabulary for that fallback and its
// ordering; read the paragraph below for why the isolation rule it relaxes is not
// the isolation rule the family enforces. The Get → Lookup duplication with the
// siblings is still accepted for resolveBoundSession's stated reason — keeping
// each twin byte-stable beats folding them.
//
// WHY THE #678 GUARD IS RELAXED HERE AND NOWHERE ELSE, stated at length because
// this file would otherwise carry two rules that read as contradictory. The
// family's empty-CurrentSessionID check is the #678 isolation enforcement point
// resolveBoundRunner documents: Pool.Lookup("") returns the BOOTSTRAP session
// rather than an error, so an unbound conversation that reached a lookup would be
// handed the shared bootstrap child's state. That rule is about ROUTING TURNS — a
// message must never reach another conversation's child, and a run setting, a
// runner or a session read off the bootstrap on an unbound conversation's behalf
// is exactly that leak. A MODEL VOCABULARY IS NOT TURN STATE. It varies by
// machine and account, not by conversation: the committed capture
// initialize_control_v2.1.239.json was taken with an argv carrying
// --model claude-haiku-4-5 and its reply still reports six models with
// default → claude-sonnet-5. So reading the bootstrap child's MODEL LIST for a
// conversation that has none of its own is allowed, and reading anything else off
// it still is not. THE EXCEPTION IS VOCABULARY-ONLY: resolveBoundRunner,
// resolveBoundSession, resolveBoundRunSettings, resolveBoundSlashCommandList,
// resolveBoundBackgroundTaskRoster and the twins in cmd/pyry's main.go keep the
// guard unchanged, and a future reader must not generalise from this one.
// (resolveBoundSlashCommandList in particular looks like it should follow and
// must not: its inventory is WORKSPACE-scoped — the same initialize reply's
// commands array is 51 entries for this repository — so the bootstrap's copy
// would be a WRONG answer there rather than a merely borrowed one.)
//
// The registry lookup staying hard is the WHOLE SECURITY BOUNDARY, and it is the
// only reason the relaxation above is safe: the fallback is reachable only
// through a RESOLVED registry record, so no caller-supplied id is ever answered
// from the bootstrap's menu. There is deliberately NO separate convID == ""
// pre-check — an empty id lands in that same lookup, because no conversation
// carries an empty id (handlers.CreateConversation mints every one from
// conversations.NewID) — and a second spelling of a check in only one of six
// twins would close nothing.
//
// Keyed on CONVERSATION id, not session id: protocol.ModelListPayload's field is
// conversation_id and #1858 addresses a conn's conversation. The bound session
// id is an internal hop and is never surfaced to the caller.
//
// The reported id comes from the RESOLVED RECORD (conv.ID), not reflected from
// the convID parameter. conversations.Registry.Get compares byte-exactly today,
// so the two spellings are identical and no test can separate them; it is a
// deliberate choice rather than an accident, so the provenance stays correct if
// the lookup ever loosens (prefix match, case folding, normalisation). That is
// internal/relay's RunConfigFor posture: the reported id comes out of the
// daemon's own registry record.
//
// The retention is reached by TYPE ASSERTION off Session.Runner — see
// sessionRetainedModelList, which owns that read and the one nil check on this
// path.
//
// The mapping is turnbridge.MapEvent's, unforked: #1848 owns that translation
// and it must not be re-derived here. TurnID and Seq stay zero because the
// ModelList arm ignores both — one initialize exchange per child is not even
// per-turn, so there is no turn id to invent.
//
// The bool is the only spelling of "nothing to send". turnevent.ModelList.Models
// is documented "Never empty", so an empty retained list cannot stand in for the
// unreported state: no arm returns true with an empty Models and none returns a
// partially-filled payload. Since #2124 only TWO states answer (zero, false): an
// id the registry does not carry, and NOTHING RETAINED ANYWHERE — no bootstrap in
// the pool, a bootstrap constructed evicted (sessions.Config.BootstrapEvicted,
// set by pyry acp, which spawns no claude), or its initialize reply not yet
// arrived. #2450 NARROWED the second state without adding a third: it now also
// requires that no vocabulary was persisted by a previous process — an absent,
// unreadable or undecodable model_list.json, or a daemon built with no store at
// all. An unbound conversation, a binding the pool cannot resolve, a runner
// without the method and an empty hold no longer refuse; they fall back. Absence
// of the frame stays the only "no list" signal a client gets — reconcileModelLists
// sends nothing for an empty enumeration — so do NOT invent an empty Models array
// to mean "unknown".
//
// Value ownership: sessionModelHold.ModelList hands back a DEEP COPY this
// function solely owns, and MapEvent allocates a fresh outer slice while each
// row's two []string fields cross by reference from that copy. The copy is
// per-call and never retained, so the payload owns its slices outright and two
// callers never share a backing array — do not add a second clone.
// DroppedModels rides through from the decode; never recompute it from
// len(Models) and never zero it.
//
// SECURITY: convID is untrusted network input and never leaves this function —
// a lookup key into the daemon's own registry and nothing else, never returned
// on a refusal, never joined into a path, never wrapped into an error.
// Pool.Lookup's error is DISCARDED rather than wrapped, resolveBoundRunSettings'
// stated reason: returning it bare is what keeps a hostile or malformed id from
// being reflected into a log line or a wire frame a caller builds from it. This
// resolver takes no logger and MUST NOT grow one — the only thing a "why did it
// not resolve" line could add is the caller's id or the model values themselves,
// which is exactly the channel the #833 posture closes (sessionModelHold
// enforces the same property by construction, having no logger field). #2124 adds
// a SECOND tempting line with the same content, and it is forbidden for the same
// reason: do not log that a conversation fell back to the daemon-wide vocabulary,
// at any level. Nothing about which source answered is diagnosable from outside
// without naming a conversation or a model.
//
// The bootstrap session itself never escapes: sessionRetainedModelList narrows it
// to a turnevent.ModelList through a one-method assertion, so nothing else about
// that child — its settings, its runner, its transcript, its session id — is
// reachable from here even though this function can now read it.
//
// Concurrency: a synchronous read on the caller's goroutine that spawns nothing,
// mints nothing and mutates nothing. Its locks — the registry's, Pool.Lookup's
// RLock, the bound hold's leaf mutex, and since #2124 Pool.Default's RLock and
// the bootstrap hold's leaf mutex, at most five — are acquired SEQUENTIALLY and
// never nested, so this adds no edge to the daemon's lock order; do not
// restructure the body so one read happens inside another's scope. The Get →
// Lookup window is the benign TOCTOU all the siblings share, and the Lookup →
// Default window #2124 adds is a second instance of it: a rotation landing in
// either leaves the answer either the menu of the session bound a moment ago or
// the daemon-wide copy, and both are correct because the vocabulary is a property
// of the machine and account rather than of one child. No re-read, no retry.
func resolveBoundModelList(convReg *conversations.Registry, pool *sessions.Pool, saved savedModelVocabulary, convID string) (protocol.ModelListPayload, bool) {
	conv, ok := convReg.Get(conversations.ConversationID(convID))
	if !ok {
		return protocol.ModelListPayload{}, false
	}
	list, ok := retainedModelVocabulary(pool, saved, conv.CurrentSessionID)
	if !ok {
		return protocol.ModelListPayload{}, false
	}
	// MapEvent returns `payload any` (the payloads share no marker interface), so
	// reuse costs one assertion back to protocol.ModelListPayload — and that
	// assertion is the ONLY discriminant. typ is discarded because comparing it to
	// protocol.TypeModelList would be a strictly weaker second spelling of the same
	// check; #1858 names the type itself when it builds the envelope, exactly as
	// outstandingQueues' payloads carry no type field.
	_, payload, ok := turnbridge.MapEvent(list, turnbridge.TurnContext{ConversationID: string(conv.ID)})
	if !ok {
		return protocol.ModelListPayload{}, false
	}
	out, ok := payload.(protocol.ModelListPayload)
	if !ok {
		return protocol.ModelListPayload{}, false
	}
	// The two arms above are the TYPE SYSTEM's, not states this daemon can reach:
	// MapEvent maps every turnevent.ModelList and returns that concrete payload
	// type. They still need an answer, and "no list" is the only one that keeps
	// the contract above — a caller must never be handed a payload with an empty
	// Models. Hence a refusal rather than a panic, and no log.
	return out, true
}

// savedModelVocabulary is the file-backed THIRD source: the last vocabulary this
// daemon saw in any previous process, restored once at start (#2450).
// *modelVocabularyStore is the only production implementation.
//
// It is the SAME ONE-METHOD SHAPE sessionRetainedModelList asserts for off a
// Runner, and deliberately so: the third source then answers the same comma-ok
// contract as the first two, the bool stays the only spelling of "no list", and no
// arm of retainedModelVocabulary has to learn a second vocabulary-shaped protocol.
// Defined at the CONSUMER, per CODING-STYLE, which is also what keeps this file
// free of any dependency on how the store persists anything.
type savedModelVocabulary interface {
	ModelList() (turnevent.ModelList, bool)
}

// retainedModelVocabulary answers the model vocabulary to stamp with a
// conversation's id: the bound session's OWN retained list when it has one, else
// the daemon-wide copy a bootstrap child retains (#2124), else the copy this
// daemon persisted in a previous process (#2450).
//
// ORDER IS THE CONTRACT, and it is now THREE DEEP. Each source is reached only
// when the one above it yields nothing, so a live child's own report is never
// overridden — a child that has answered initialize is the authority on what IT
// will accept, and every fallback below it is a stand-in for silence rather than a
// second opinion. The file is LAST for that rule applied once more: a hold is this
// process's observation and the file is a previous process's. Do not reorder these
// reads, and do not "simplify" by reading a lower source unconditionally and
// preferring the bound list afterwards: that spends a lock and a deep copy on every
// call for the common case where the binding already answered.
//
// WHY THE THIRD SOURCE EXISTS, stated because this doc previously asserted the
// premise that removed the need for it. It used to say the daemon-wide copy is
// always there "because RequestInitializeOnSpawn is true for every child this
// daemon spawns, so the bootstrap child asks at daemon start" — and #2085 had
// already deleted the spawn at daemon start. Nothing spawns at start, so a
// restarted daemon holds no vocabulary in EITHER hold until a turn runs in the
// conversation bound to the bootstrap session; every minted conversation gets its
// own session, so on a daemon whose bootstrap-bound conversation is idle that is
// never, for the whole process lifetime. The file closes exactly that window and
// nothing else: it changes which answers are available on a COLD daemon, and
// changes no answer a warm one gives.
//
// The bootstrap is read through Pool.Default rather than through Pool.Lookup(""),
// and that choice is load-bearing TWICE. It names what is being read, which is what
// keeps this a documented vocabulary exception rather than the #678 hazard reopened
// by accident. And Pool.Lookup("") returns p.sessions[p.bootstrap] with a NIL ERROR
// rather than an error, so a version of this function that dropped the
// boundSessionID != "" check and let an unbound conversation fall through to the
// lookup would get back a possibly-nil session with no signal that anything was
// wrong — which sessionRetainedModelList's nil guard would then have to catch
// anyway, one layer further from the decision.
//
// A NIL saved IS A DAEMON WITH TWO SOURCES, not an error and not a special case: a
// host that never built a store (every test pool, and pyry acp) falls through the
// same arms and refuses at the same place it did before #2450. savedModelList owns
// that guard so neither this function nor its callers repeat it.
//
// SECURITY: the boundSessionID is the daemon's own — it comes off a resolved
// conversations.Conversation, never from a caller — and the untrusted id was
// already spent on the registry lookup one frame up. Nothing here logs, and
// Pool.Lookup's error is discarded rather than wrapped, for the reason
// resolveBoundModelList states. The file's strings are claude-authored and were
// bounded by streamsup at construction before this daemon wrote them; the store
// treats the file as daemon-written and applies one aggregate size bound of its own
// — see maxModelVocabularyFile, which carries that decision and its residual risk.
func retainedModelVocabulary(pool *sessions.Pool, saved savedModelVocabulary, boundSessionID string) (turnevent.ModelList, bool) {
	if boundSessionID != "" {
		if sess, err := pool.Lookup(sessions.SessionID(boundSessionID)); err == nil {
			if list, ok := sessionRetainedModelList(sess); ok {
				return list, true
			}
		}
	}
	if list, ok := sessionRetainedModelList(pool.Default()); ok {
		return list, true
	}
	return savedModelList(saved)
}

// savedModelList reads the third source, or reports that there is none to read. It
// is the single place a nil store is handled, which is why retainedModelVocabulary
// has no guard of its own — sessionRetainedModelList's argument for owning the nil
// session and the type assertion, applied to the one absence THIS source can have.
//
// The nil check is on the INTERFACE, and it catches the daemon that never built a
// store. A store that exists but has seen nothing is a different state and answers
// through its own comma-ok, and *modelVocabularyStore.ModelList is additionally
// nil-receiver-safe, so a typed nil inside a non-nil interface also lands on the
// unreported state rather than panicking.
//
// Value ownership: the store hands back a DEEP COPY the caller solely owns, exactly
// as sessionModelHold.ModelList does. This function forwards it untouched — no
// clone, no normalisation, no recomputation of DroppedModels.
func savedModelList(saved savedModelVocabulary) (turnevent.ModelList, bool) {
	if saved == nil {
		return turnevent.ModelList{}, false
	}
	return saved.ModelList()
}

// sessionRetainedModelList reads one session's retained model list, or reports
// that it has none. It is the single place this file reaches a hold, so the type
// assertion and the nil handling are decided once rather than at each of
// retainedModelVocabulary's two call sites.
//
// The retention is reached by TYPE ASSERTION off Session.Runner, the way
// interruptRunner reaches Interrupt and sessionModelWindows reaches its own.
// internal/sessions' Runner doc states the rule: the interface carries a method
// when its consumer sits INSIDE internal/sessions, where a structural assertion
// would fail open. This consumer is in cmd/pyry, so widening would buy no
// compile-time guarantee and would drag every fake runner in two packages into
// the diff. A runner without the method is a REFUSAL, not a panic — and a nil
// Runner fails the assertion cleanly.
//
// THE NIL SESSION IS A DIFFERENT CASE and needs the explicit check, which is why
// the family's "a nil Runner fails the assertion cleanly, so no nil check is
// needed" sentence does NOT carry over here. Session.Runner has a POINTER
// RECEIVER that dereferences its receiver, so a nil *Session panics before any
// assertion runs. Pool.Default returns p.sessions[p.bootstrap] and is nil on a
// map miss — the case Pool.DefaultSettings documents and answers with a comma-ok
// — so this is reachable rather than defensive: a zero-value pool, and any host
// whose bootstrap is absent from the map. It answers "no list", which is
// resolveBoundModelList's nothing-retained-anywhere state.
//
// Value ownership: sessionModelHold.ModelList hands back a DEEP COPY the caller
// solely owns. This function forwards it untouched — no clone, no normalisation,
// no recomputation of DroppedModels.
func sessionRetainedModelList(sess *sessions.Session) (turnevent.ModelList, bool) {
	if sess == nil {
		return turnevent.ModelList{}, false
	}
	lister, ok := sess.Runner().(interface {
		ModelList() (turnevent.ModelList, bool)
	})
	if !ok {
		return turnevent.ModelList{}, false
	}
	return lister.ModelList()
}

// modelListFor adapts the conversation-keyed resolver above to the relay's
// on-demand request seam (#2125 fills V2SessionConfig.ModelListFor): the menu for
// the ONE conversation an inbound request_model_list names.
//
// It is retainedModelLists' twin below in construction — dependencies in, a closure
// out, a pure read — and its OPPOSITE in shape, deliberately. That one enumerates
// because a relay V2Session carries no conversation id, so a connect-time reconcile
// has nothing to key on; this one is handed an id by the request, so it must not
// walk the registry to answer about a single row. The two exist together because
// the two triggers differ, not because the answer does.
//
// IT ADDS NOTHING TO THE RESOLVER AND MUST NOT. No filter, no second lookup, no
// re-derivation of which vocabulary answers — retainedModelLists' rule, and the
// same reason: the resolver decides WHICH SOURCE answers (the bound session's own
// retained list, else the daemon-wide copy since #2124), and a second decision here
// would be a second place that rule could be stated and disagree. The comma-ok
// crosses untouched, which is what lets the relay handler turn "no menu" into a
// coded error frame rather than an empty models array — the shape
// turnevent.ModelList.Models' never-empty contract forbids.
//
// A NAMED FUNCTION RATHER THAN AN INLINE CLOSURE AT THE CALL SITE, unlike its
// neighbour runSettings and like retainedModelLists below. Two reasons, and the
// second is the deciding one: it matches the twin it will always be read beside,
// and cmd/pyry's composition root does not import internal/protocol, so an inline
// closure would drag that import in for one type annotation.
//
// SECURITY: convID is untrusted network input that has ALREADY passed the relay's
// KnownConversation membership gate before it reaches here, and this function spends
// it on nothing but resolveBoundModelList's registry lookup. It takes NO logger and
// MUST NOT grow one — resolveBoundModelList's #833 rule inherited for its reason:
// the only thing a "why did this not resolve" line could carry is a conversation id
// or the model values themselves.
//
// Concurrency: synchronous on the caller's goroutine — the relay manager's Run
// dispatch goroutine, which is new for this resolver and is why the body must stay
// what it is. It spawns nothing, mints nothing and mutates nothing, and the locks it
// takes are resolveBoundModelList's, acquired sequentially and never nested.
func modelListFor(convReg *conversations.Registry, pool *sessions.Pool, saved savedModelVocabulary) func(convID string) (protocol.ModelListPayload, bool) {
	return func(convID string) (protocol.ModelListPayload, bool) {
		return resolveBoundModelList(convReg, pool, saved, convID)
	}
}

// retainedModelLists adapts the conversation-keyed resolver above to the relay's
// connect-time reconcile seam (#1863): one marshal-ready
// protocol.ModelListPayload per conversation the registry carries, once ANY
// vocabulary is retained. The shape is outstandingQueues' — dependencies in, a
// closure out, one payload per contributing entity, a pure read — and the
// ENUMERATION is forced rather than chosen: a relay V2Session carries no
// conversation id, so there is nothing to key the resolver on at connect time and
// the only way to fill a conversation-scoped seam is to walk the registry.
//
// WHAT #2124 CHANGED HERE IS THE POPULATION, NOT A LINE OF THIS BODY. It was "one
// payload per conversation whose bound session currently holds a list"; the
// resolver's fallback makes it "one per conversation the registry carries, once
// any vocabulary is retained" — and still NONE when none is, because every row
// then refuses and reconcileModelLists sends nothing for an empty slice. Nothing
// in this function decides that, which is the point: the rule moved inside
// resolveBoundModelList, where it can be stated once.
//
// The comma-ok is the ONLY filter. This function reads no field of Conversation
// but ID, and inspects no payload it is about to append. Every refusal rule stays
// inside resolveBoundModelList, where #1857 put them; a second spelling here
// would be a second place the rule is decided. In particular there is
// deliberately NO len(Models) > 0 check — the resolver's bool is the only
// spelling of "nothing to send" and no arm of it answers true with an empty
// Models, so such a check would be a strictly weaker restatement no test could
// redden.
//
// The DOUBLE LOOKUP is deliberate, and #2124 REPLACED THE ARGUMENT FOR IT rather
// than weakening it. Registry.List already hands back each Conversation,
// CurrentSessionID included, and this loop throws that away so the resolver can
// Get the row again. The old reason was that reading CurrentSessionID off the
// listed row and calling Pool.Lookup directly would fork the
// empty-CurrentSessionID guard and hand an unbound conversation the bootstrap's
// menu — which is now exactly what the resolver does ON PURPOSE, so that sentence
// would argue against a shipped feature. The reason that survives is stronger and
// simpler: the resolver decides WHICH SOURCE answers, and reading the binding
// here would fork that decision into a second place. Whether a row falls back
// belongs to retainedModelVocabulary; this loop must not be able to disagree
// with it.
//
// The pool's bootstrap session still needs no special case, but the sentence has
// NARROWED. It contributes no payload OF ITS OWN — it has no conversation record,
// so it never appears in List at all, and ModelListPayload is conversation-scoped
// with no id to stamp for it. What changed is that its RETAINED LIST is now
// readable on another conversation's behalf, so "the bootstrap contributes
// nothing" is true of payloads and false of vocabulary. Both halves matter to a
// reader: no frame is ever addressed to the bootstrap, and most frames may now
// carry its models.
//
// The cost of that choice is O(rows²) comparisons: Registry.Get is a linear scan,
// so N rows cost N scans. #2124 made the SECOND term the dominant one — where a
// deep copy was paid once per contributing session, it is now paid once per ROW,
// because every row contributes while a vocabulary is retained. The same is true
// of what reaches the wire: a handshake that used to push few or zero frames now
// pushes one per row. No cap is added and none should be — the count stays
// deliberately uncapped, the shape outstandingQueues already ships, and capping
// here would silently truncate a client's menus. The exposure is bounded by
// registry size, is reachable only post-handshake and post-token-validation, is
// unicast to the conn that asked, and each payload arrives already bounded at
// construction (DroppedModels, TruncatedFields). This runs once per interactive
// handshake and never per turn, so at the tens to hundreds of rows this daemon
// carries it is still microseconds on the relay manager's Run goroutine. It is
// stated because the registry only GROWS — auto-archive sets a flag rather than
// deleting, and archived rows are deliberately enumerated below — so whoever hits
// it can see the cost. The fix, if one is ever needed, is an id-keyed index
// inside internal/conversations, NOT reading the binding off the listed row.
//
// Enumerate-all, unfiltered, so ARCHIVED conversations CONTRIBUTE. List with no
// filter returns them, and Registry.SetArchived writes exactly one field without
// unbinding CurrentSessionID, so an archived conversation produces a payload —
// from its own bound session's list, or since #2124 from the daemon-wide
// vocabulary like any other row. That is intended: the reconcile asserts current
// control truth and the client decides what to show. Neither adding nor omitting
// a conversations.ListFilter here is a free edit.
//
// Order is List's (registry insertion order) and is NOT a contract.
// reconcileModelLists sends one envelope per payload and the client correlates on
// conversation_id, so callers and tests index by ConversationID, never by
// position.
//
// SECURITY: a pure read — it mints nothing, retires nothing and mutates no daemon
// state. It takes NO logger and MUST NOT grow one, resolveBoundModelList's #833
// rule inherited for its reason: the only thing a "why did this row not
// contribute" line could carry is a conversation id or the model values
// themselves. Every id it hands the resolver came out of this daemon's own
// registry and is server-minted (handlers.CreateConversation builds each row's ID
// from conversations.NewID), so the resolver's untrusted-id arm is unreachable
// from this entry point and needs no second guard here. This path applies no
// bound of its own: the payloads arrive already bounded at construction
// (DroppedModels, TruncatedFields) and the count is deliberately uncapped, the
// shape outstandingQueues already ships.
//
// Concurrency: synchronous on the caller's goroutine; nothing is spawned, so
// there is nothing to leak or join. The per-row Get runs OUTSIDE List's lock
// scope because the loop iterates the copy List returns — do not restructure
// through Registry.Update or any registry-held callback, which would both add a
// registry→pool lock edge the daemon does not have today and deadlock against
// Update's own no-re-entry rule. The List → Get window is the benign TOCTOU the
// resolver documents: a row created, deleted, rebound or rotated inside it either
// resolves to the menu of the session bound a moment ago or refuses, and both are
// correct. No re-read, no retry, no re-list.
func retainedModelLists(convReg *conversations.Registry, pool *sessions.Pool, saved savedModelVocabulary) func() []protocol.ModelListPayload {
	return func() []protocol.ModelListPayload {
		convs := convReg.List()
		out := make([]protocol.ModelListPayload, 0, len(convs))
		for _, c := range convs {
			payload, ok := resolveBoundModelList(convReg, pool, saved, string(c.ID))
			if !ok {
				continue
			}
			out = append(out, payload)
		}
		return out
	}
}
