package main

import (
	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/sessions"
	"github.com/pyrycode/pyrycode/internal/turnbridge"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// resolveBoundBackgroundTaskRoster answers the background-task roster the named
// conversation's bound session currently holds (#2077), already shaped as a
// marshal-ready protocol.BackgroundTaskRosterPayload, so the enumeration below can
// hand it to the relay's connect-time reconcile seam without reaching into session
// internals or re-deriving the untrusted-id rules for itself. It is COMPOSITION AND
// REFUSAL — every part it joins already exists.
//
// It is the SIXTH member of the conversation-keyed resolver family, after
// resolveBoundRunner, resolveBoundSession, resolveBoundRunSettings, resolveBoundModelList
// and resolveBoundSlashCommandList, and it is the last of those reproduced with the
// payload type substituted — with ONE contract inverted, stated under THE EMPTY CASE
// below. The refusal is INHERITED VERBATIM rather than re-derived: an unknown
// conversation or an empty CurrentSessionID returns (zero, false) BEFORE the pool is
// touched. That second guard is the #678 isolation enforcement point resolveBoundRunner
// documents — Pool.Lookup("") returns the BOOTSTRAP session rather than an error, so an
// unbound conversation that reached a lookup would be handed the shared bootstrap
// child's roster stamped with its own conversation id. There is deliberately NO third
// convID == "" pre-check: an empty id lands in the first guard. The Get → guard → Lookup
// duplication with the five siblings is accepted for resolveBoundSession's stated reason
// — keeping each twin byte-stable beats folding them.
//
// Keyed on CONVERSATION id, not session id: protocol.BackgroundTaskRosterPayload's field
// is conversation_id, and the bound session id is an internal hop never surfaced to the
// caller.
//
// The reported id comes from the RESOLVED RECORD (conv.ID), not reflected from the convID
// parameter. conversations.Registry.Get compares byte-exactly today, so the two spellings
// are identical and no test can separate them; it is a deliberate choice rather than an
// accident, so the provenance stays correct if the lookup ever loosens.
//
// The retention is reached by TYPE ASSERTION off Session.Runner, the way interruptRunner
// reaches Interrupt. internal/sessions' Runner doc states the rule: the interface carries
// a method when its consumer sits INSIDE internal/sessions, where a structural assertion
// would fail open. This consumer is in cmd/pyry, so widening would buy no compile-time
// guarantee and would drag every fake runner in two packages into the diff. A runner
// without the method is a REFUSAL, not a panic — and a nil Runner fails the assertion
// cleanly, so no nil check is needed.
//
// The mapping is turnbridge.MapEvent's, unforked. TurnID and Seq stay zero because the
// BackgroundTaskRoster arm ignores both: a roster is conversation-scoped rather than
// turn-scoped and opens no turn, so there is no turn id to invent.
//
// THE EMPTY CASE, and it is the OPPOSITE of what both twins do. There is deliberately no
// len(Tasks) == 0 check at any step, and copying either neighbour's sentence about one
// would state something false here. resolveBoundModelList leans on turnevent.ModelList.
// Models being documented "never empty" and resolveBoundSlashCommandList on streamsup's
// emitSlashCommandList returning early for a zero-length list (#1877) — so for both of
// them an empty aggregate is a value no reader can be handed. emitBackgroundTaskRoster
// emits an event for an absent or empty tasks array ON PURPOSE, because an empty roster
// positively says NOTHING IS ALIVE, which is exactly the reassurance a consumer of
// #1240's symptom needs. So a session that reported an empty roster contributes a payload
// carrying an empty task list, and silence is reserved for three states only: unbound,
// session gone, and nothing ever reported. sessionBackgroundTaskHold.BackgroundTaskRoster's
// bool is what tells the last of those apart from an empty report, and collapsing the two
// reconciles a live session as a silent one.
//
// The consequence rides all the way to the wire and is worth stating because it looks
// like an omission: MapEvent's arm FORWARDS a nil Tasks rather than pre-allocating, so an
// empty roster leaves here with Tasks == nil and protocol.BackgroundTaskRosterPayload.
// MarshalJSON renders "tasks":[]. Do NOT pre-allocate to get the same bytes — that hides
// the normalisation the payload type owns and which the mapping deliberately declines to
// duplicate.
//
// DroppedTasks rides through UNTOUCHED — never recomputed from len(Tasks), never zeroed.
// It is this variant's ONLY truncation report, since the payload has no top-level
// truncated_fields, so shipping 0 would tell a client that a capped roster is the whole
// roster.
//
// Value ownership: sessionBackgroundTaskHold.BackgroundTaskRoster hands back a DEEP COPY
// this function solely owns — two levels, the Tasks slice and each entry's
// TruncatedFields — and MapEvent allocates a fresh outer slice while each row's
// TruncatedFields crosses by reference from that copy. The copy is per-call and never
// retained, so the payload owns its slices outright and two callers never share a backing
// array — do not add a second clone.
//
// SECURITY: convID is untrusted network input and never leaves this function — a lookup
// key into the daemon's own registry and nothing else, never returned on a refusal, never
// joined into a path, never wrapped into an error, and not even echoed on success, since
// the reported id comes from the resolved record. Pool.Lookup's error is DISCARDED rather
// than wrapped, resolveBoundRunSettings' stated reason: returning it bare is what keeps a
// hostile or malformed id from being reflected into a log line or a wire frame a caller
// builds from it. This resolver takes no logger and MUST NOT grow one. That is the #833
// posture with the threat at this family's sharpest: all four strings a task row carries
// are claude-authored untrusted text, and for the local_bash task type a Description IS
// the literal command line — safe to RENDER as inert text, never to execute, re-shell, or
// feed to a structured sink. emitBackgroundTaskRoster's own drop path refuses to log even
// the entry COUNT, on the ground that "just the length" is the leak a content-free rule is
// most often bent for. The strings are bounded at construction but NOT sanitized, and the
// render boundary owing that is the CLIENT's, as turnevent.BackgroundTask assigns; this
// slice adds no sink of its own.
//
// Concurrency: a synchronous read on the caller's goroutine that spawns nothing, mints
// nothing and mutates nothing. Its three locks — the registry's, the pool's RLock and the
// hold's leaf mutex — are acquired SEQUENTIALLY and never nested, so this adds no edge to
// the daemon's lock order; do not restructure the body so one lookup happens inside
// another's scope. The hold's mutex doc names this read as the one its lock exists for.
// The Get → Lookup window is the benign TOCTOU all five siblings share, and it is closed
// where it matters: the guard and the lookup key are read off the SAME Get result, so a
// rotation landing in the window leaves the id either resolvable (we answer the roster of
// the session bound a moment ago) or not (we refuse), and both are correct because a
// rotation's fresh child reports its own roster. No re-read, no retry.
func resolveBoundBackgroundTaskRoster(convReg *conversations.Registry, pool *sessions.Pool, convID string) (protocol.BackgroundTaskRosterPayload, bool) {
	conv, ok := convReg.Get(conversations.ConversationID(convID))
	if !ok || conv.CurrentSessionID == "" {
		return protocol.BackgroundTaskRosterPayload{}, false
	}
	sess, err := pool.Lookup(sessions.SessionID(conv.CurrentSessionID))
	if err != nil {
		return protocol.BackgroundTaskRosterPayload{}, false
	}
	reader, ok := sess.Runner().(interface {
		BackgroundTaskRoster() (turnevent.BackgroundTaskRoster, bool)
	})
	if !ok {
		return protocol.BackgroundTaskRosterPayload{}, false
	}
	roster, ok := reader.BackgroundTaskRoster()
	if !ok {
		return protocol.BackgroundTaskRosterPayload{}, false
	}
	// MapEvent returns `payload any` (the payloads share no marker interface), so reuse
	// costs one assertion back to protocol.BackgroundTaskRosterPayload — and that
	// assertion is the ONLY discriminant. typ is discarded because comparing it to
	// protocol.TypeBackgroundTaskRoster would be a strictly weaker second spelling of the
	// same check; reconcileBackgroundTaskRosters names the type itself when it builds the
	// envelope.
	_, payload, ok := turnbridge.MapEvent(roster, turnbridge.TurnContext{ConversationID: string(conv.ID)})
	if !ok {
		return protocol.BackgroundTaskRosterPayload{}, false
	}
	out, ok := payload.(protocol.BackgroundTaskRosterPayload)
	if !ok {
		return protocol.BackgroundTaskRosterPayload{}, false
	}
	// These two arms are the TYPE SYSTEM's, not states this daemon can reach: MapEvent's
	// BackgroundTaskRoster arm has NO SUPPRESSION BRANCH AT ALL — it maps even a
	// zero-value roster and returns true by construction, which is precisely what makes
	// the empty case above reachable without forking the mapping. They still need an
	// answer, and a refusal is the only one that keeps the contract — not a panic, and no
	// log, since a log here would carry exactly the strings the SECURITY paragraph keeps
	// off this path. No fixture reaches them, deliberately.
	return out, true
}

// retainedBackgroundTaskRosters adapts the conversation-keyed resolver above to the
// relay's connect-time reconcile seam (#2079 fills #2078's
// V2SessionConfig.RetainedBackgroundTaskRosters, which shipped wired to nothing): one
// marshal-ready protocol.BackgroundTaskRosterPayload per conversation whose bound session
// holds a roster. The shape is outstandingQueues' — dependencies in, a closure out, one
// payload per contributing entity, a pure read — and it is retainedSlashCommandLists'
// TWIN, so read that function alongside this one.
//
// This slice builds NO ENVELOPE. reconcileBackgroundTaskRosters already owns the type
// stamp, the shared batch timestamp, the nil EventID and the Push; this half ends at the
// payload slice.
//
// The ENUMERATION is forced rather than chosen: a relay V2Session carries no conversation
// id — it holds connID, state, resp, send, recv, device, interactive and peerStatic — so
// there is nothing to key the resolver on at connect time and the only way to fill a
// conversation-scoped seam is to walk the registry daemon-side. The seam says so from its
// own side, and names the conversation-keyed resolver as the obvious wrong shape to reach
// for here.
//
// The comma-ok is the ONLY filter. This function reads no field of Conversation but ID,
// and inspects no payload it is about to append. Every refusal rule stays inside
// resolveBoundBackgroundTaskRoster; a second spelling here would be a second place the
// rule is decided. In particular there is deliberately NO len(Tasks) > 0 check, and unlike
// both twins the reason is not that the case is unreachable — it is that the case is the
// POINT. An empty roster says nothing is alive, and a filter here would delete the payoff
// of the whole feature while looking exactly like the neighbours' correct code. Both
// twins' enumerations carry a sentence justifying the same absent filter on the opposite
// ground; do not copy either one into this function.
//
// The DOUBLE LOOKUP is deliberate, and it is a SECURITY CONTROL rather than redundancy.
// Registry.List already hands back each Conversation, CurrentSessionID included, and this
// loop throws that away so the resolver can Get the row again. Reading CurrentSessionID
// off the listed row and calling Pool.Lookup directly would fork the empty-CurrentSessionID
// guard, which is the #678 isolation enforcement point: Pool.Lookup("") returns the
// BOOTSTRAP session with a nil error, so a fork that drifted would hand an unbound
// conversation the shared bootstrap child's roster stamped with its own conversation id —
// a cross-conversation disclosure, and the exact failure the isolation test pins. The
// bootstrap session contributes nothing for the same reason it needs no special case: it
// has no conversation record, so it never appears in List at all, and the payload is
// conversation-scoped with no id to stamp for it.
//
// The cost of that choice is O(rows²) comparisons: Registry.Get is a linear scan, so N rows
// cost N scans, plus one deep copy per contributing session's roster. This runs once per
// interactive handshake and never per turn, so at the tens to hundreds of rows this daemon
// carries it is microseconds on the relay manager's Run goroutine. It is stated because the
// registry only GROWS — auto-archive sets a flag rather than deleting, and archived rows are
// deliberately enumerated below — so whoever hits it can see the cost. The fix, if one is
// ever needed, is an id-keyed index inside internal/conversations, NOT reading the binding
// off the listed row.
//
// Enumerate-all, unfiltered, so ARCHIVED conversations CONTRIBUTE. List with no filter
// returns them, and Registry.SetArchived writes exactly one field without unbinding
// CurrentSessionID, so an archived conversation whose bound session still holds a roster
// produces a payload. That is intended: the reconcile asserts current control truth and the
// client decides what to show. Neither adding nor omitting a conversations.ListFilter here
// is a free edit.
//
// Order is List's (registry insertion order) and is NOT a contract.
// reconcileBackgroundTaskRosters sends one envelope per payload under a single fixed,
// non-load-bearing envelope id, and the seam's own doc block binds callers to correlate by
// conversation_id — so callers and tests index by ConversationID, never by position.
//
// SECURITY: a pure read — it mints nothing, retires nothing and mutates no daemon state,
// including nothing it read: each resolver call takes its own deep copy out of the hold, so
// two conversations bound to the SAME session get payloads with independent backing arrays
// and no caller can write through a producer's slice. Connecting twice with no roster change
// between delivers the same payloads both times. It takes NO logger and MUST NOT grow one,
// resolveBoundBackgroundTaskRoster's #833 rule inherited: the only thing a "why did this row
// not contribute" line could carry is a conversation id or the task strings themselves, and
// a Description is a literal command line for claude's local_bash type. Every id it hands the
// resolver came out of this daemon's own registry and is server-minted
// (handlers.CreateConversation builds each row's ID from conversations.NewID), so the
// resolver's untrusted-id arm is unreachable from this entry point and needs no second guard
// here. This path applies no bound of its own: the payloads arrive already bounded at
// construction (DroppedTasks on the aggregate, TruncatedFields per entry, over streamsup's
// maxTaskRosterEntries and maxTaskRosterDescription), and the count is deliberately uncapped
// — the shape outstandingQueues, retainedModelLists and retainedSlashCommandLists already
// ship, with pushQueue's byte ceiling as the relay-side backstop. A cap here would be a
// second place the limit is decided. Not closed here, and named because turning this path on
// is what gives it effect: a paired interactive conn is unicast EVERY retained roster,
// including conversations bound to other workspaces. Per-device confinement belongs to the
// Mode B umbrella (#829) rather than to one of its six instances.
//
// Concurrency: synchronous on the caller's goroutine; nothing is spawned, so there is nothing
// to leak or join. The seam requires BOUNDED TIME because this closure runs on the manager's
// Run goroutine, and it holds: the per-row Get runs OUTSIDE List's lock scope because the loop
// iterates the copy List returns, and Registry.Save takes its snapshot under the registry mutex
// and releases it BEFORE any disk write, so no Get here can ever queue behind an fsync. Do not
// restructure through Registry.Update or any registry-held callback, which would both add a
// registry→pool lock edge the daemon does not have today and deadlock against Update's own
// no-re-entry rule. The List → Get window is the benign TOCTOU the resolver documents: a row
// created, deleted, rebound or rotated inside it either resolves to the roster of the session
// bound a moment ago or refuses, and both are correct. No re-read, no retry, no re-list.
func retainedBackgroundTaskRosters(convReg *conversations.Registry, pool *sessions.Pool) func() []protocol.BackgroundTaskRosterPayload {
	return func() []protocol.BackgroundTaskRosterPayload {
		convs := convReg.List()
		out := make([]protocol.BackgroundTaskRosterPayload, 0, len(convs))
		for _, c := range convs {
			payload, ok := resolveBoundBackgroundTaskRoster(convReg, pool, string(c.ID))
			if !ok {
				continue
			}
			out = append(out, payload)
		}
		return out
	}
}
