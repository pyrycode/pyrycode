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
// resolveBoundRunner, resolveBoundSession and resolveBoundRunSettings, and the
// refusal is INHERITED VERBATIM rather than re-derived: an unknown conversation
// or an empty CurrentSessionID returns (zero, false) BEFORE the pool is touched.
// That second guard is the #678 isolation enforcement point resolveBoundRunner
// documents — Pool.Lookup("") returns the BOOTSTRAP session rather than an
// error, so an unbound conversation that reached a lookup would be handed the
// shared bootstrap child's menu. There is deliberately NO third convID == ""
// pre-check: an empty id lands in the first guard, and a fourth spelling of a
// check in only one of four twins would close nothing. The Get → guard → Lookup
// duplication with the siblings is accepted for resolveBoundSession's stated
// reason — keeping each twin byte-stable beats folding them.
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
// The retention is reached by TYPE ASSERTION off Session.Runner, the way
// interruptRunner reaches Interrupt. internal/sessions' Runner doc states the
// rule: the interface carries a method when its consumer sits INSIDE
// internal/sessions, where a structural assertion would fail open. This consumer
// is in cmd/pyry, so widening would buy no compile-time guarantee and would drag
// every fake runner in two packages into the diff. A runner without the method
// is a REFUSAL, not a panic — and a nil Runner fails the assertion cleanly, so
// no nil check is needed.
//
// The mapping is turnbridge.MapEvent's, unforked: #1848 owns that translation
// and it must not be re-derived here. TurnID and Seq stay zero because the
// ModelList arm ignores both — one initialize exchange per child is not even
// per-turn, so there is no turn id to invent.
//
// The bool is the only spelling of "nothing to send". turnevent.ModelList.Models
// is documented "Never empty", so an empty retained list cannot stand in for the
// unreported state: no arm returns true with an empty Models and none returns a
// partially-filled payload. Unknown conversation, unbound conversation, session
// gone, runner without the method and nothing reported all answer (zero, false).
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
// enforces the same property by construction, having no logger field).
//
// Concurrency: a synchronous read on the caller's goroutine that spawns nothing,
// mints nothing and mutates nothing. Its three locks — the registry's, the
// pool's RLock and the hold's leaf mutex — are acquired SEQUENTIALLY and never
// nested, so this adds no edge to the daemon's lock order; do not restructure
// the body so one lookup happens inside another's scope. The Get → Lookup window
// is the benign TOCTOU all three siblings share: a rotation landing in it leaves
// the id either resolvable (we answer the menu of the session bound a moment
// ago) or not (we refuse), and both are correct because a model list is a
// property of one child's initialize reply and a rotation's fresh child reports
// its own. No re-read, no retry.
func resolveBoundModelList(convReg *conversations.Registry, pool *sessions.Pool, convID string) (protocol.ModelListPayload, bool) {
	conv, ok := convReg.Get(conversations.ConversationID(convID))
	if !ok || conv.CurrentSessionID == "" {
		return protocol.ModelListPayload{}, false
	}
	sess, err := pool.Lookup(sessions.SessionID(conv.CurrentSessionID))
	if err != nil {
		return protocol.ModelListPayload{}, false
	}
	lister, ok := sess.Runner().(interface {
		ModelList() (turnevent.ModelList, bool)
	})
	if !ok {
		return protocol.ModelListPayload{}, false
	}
	list, ok := lister.ModelList()
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

// retainedModelLists adapts the conversation-keyed resolver above to the relay's
// connect-time reconcile seam (#1863): one marshal-ready
// protocol.ModelListPayload per conversation whose bound session currently holds
// a list. The shape is outstandingQueues' — dependencies in, a closure out, one
// payload per contributing entity, a pure read — and the ENUMERATION is forced
// rather than chosen: a relay V2Session carries no conversation id, so there is
// nothing to key the resolver on at connect time and the only way to fill a
// conversation-scoped seam is to walk the registry.
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
// The DOUBLE LOOKUP is deliberate. Registry.List already hands back each
// Conversation, CurrentSessionID included, and this loop throws that away so the
// resolver can Get the row again. Reading CurrentSessionID off the listed row and
// calling Pool.Lookup directly would fork the empty-CurrentSessionID guard, which
// is the #678 isolation enforcement point: Pool.Lookup("") returns the BOOTSTRAP
// session, so a fork that drifted would hand an unbound conversation the shared
// bootstrap child's menu stamped with its own conversation id. The pool's
// bootstrap session contributes nothing for the same reason it needs no special
// case — it has no conversation record, so it never appears in List at all, and
// ModelListPayload is conversation-scoped with no id to stamp for it.
//
// The cost of that choice is O(rows²) comparisons: Registry.Get is a linear scan,
// so N rows cost N scans, plus one deep copy per contributing session's list.
// This runs once per interactive handshake and never per turn, so at the tens to
// hundreds of rows this daemon carries it is microseconds on the relay manager's
// Run goroutine. It is stated because the registry only GROWS — auto-archive sets
// a flag rather than deleting, and archived rows are deliberately enumerated
// below — so whoever hits it can see the cost. The fix, if one is ever needed, is
// an id-keyed index inside internal/conversations, NOT reading the binding off
// the listed row.
//
// Enumerate-all, unfiltered, so ARCHIVED conversations CONTRIBUTE. List with no
// filter returns them, and Registry.SetArchived writes exactly one field without
// unbinding CurrentSessionID, so an archived conversation whose bound session
// still holds a list produces a payload. That is intended: the reconcile asserts
// current control truth and the client decides what to show. Neither adding nor
// omitting a conversations.ListFilter here is a free edit.
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
func retainedModelLists(convReg *conversations.Registry, pool *sessions.Pool) func() []protocol.ModelListPayload {
	return func() []protocol.ModelListPayload {
		convs := convReg.List()
		out := make([]protocol.ModelListPayload, 0, len(convs))
		for _, c := range convs {
			payload, ok := resolveBoundModelList(convReg, pool, string(c.ID))
			if !ok {
				continue
			}
			out = append(out, payload)
		}
		return out
	}
}
