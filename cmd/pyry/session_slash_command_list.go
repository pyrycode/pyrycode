package main

import (
	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/sessions"
	"github.com/pyrycode/pyrycode/internal/turnbridge"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// resolveBoundSlashCommandList answers the slash-command inventory the named
// conversation's bound session currently holds (#2004), already shaped as a
// marshal-ready protocol.SlashCommandListPayload, so a delivery path (#2007) can
// send it without reaching into session internals or re-deriving the untrusted-id
// rules for itself. It is COMPOSITION AND REFUSAL — every part it joins already
// exists.
//
// It is the FIFTH member of the conversation-keyed resolver family, after
// resolveBoundRunner, resolveBoundSession, resolveBoundRunSettings and
// resolveBoundModelList, and it is resolveBoundModelList's TWIN: read that
// function first, because everything below either cites it or states one of the
// THREE ways this one differs. The refusal is INHERITED VERBATIM rather than
// re-derived: an unknown conversation or an empty CurrentSessionID returns (zero,
// false) BEFORE the pool is touched. That second guard is the #678 isolation
// enforcement point resolveBoundRunner documents — Pool.Lookup("") returns the
// BOOTSTRAP session rather than an error, so an unbound conversation that reached
// a lookup would be handed the shared bootstrap child's inventory stamped with its
// own conversation id. There is deliberately NO third convID == "" pre-check: an
// empty id lands in the first guard. The Get → guard → Lookup duplication with the
// four siblings is accepted for resolveBoundSession's stated reason — keeping each
// twin byte-stable beats folding them.
//
// Keyed on CONVERSATION id, not session id: protocol.SlashCommandListPayload's
// field is conversation_id and #2007 addresses a conn's conversation. The bound
// session id is an internal hop and is never surfaced to the caller.
//
// The reported id comes from the RESOLVED RECORD (conv.ID), not reflected from the
// convID parameter. conversations.Registry.Get compares byte-exactly today, so the
// two spellings are identical and no test can separate them; it is a deliberate
// choice rather than an accident, so the provenance stays correct if the lookup
// ever loosens (prefix match, case folding, normalisation).
//
// The retention is reached by TYPE ASSERTION off Session.Runner, the way
// interruptRunner reaches Interrupt. internal/sessions' Runner doc states the
// rule: the interface carries a method when its consumer sits INSIDE
// internal/sessions, where a structural assertion would fail open. This consumer
// is in cmd/pyry, so widening would buy no compile-time guarantee and would drag
// every fake runner in two packages into the diff. A runner without the method is
// a REFUSAL, not a panic — and a nil Runner fails the assertion cleanly, so no nil
// check is needed.
//
// The mapping is turnbridge.MapEvent's, unforked: #2001 owns that translation,
// #2002 owns the frame-level byte bound inside it, and neither may be re-derived
// here. TurnID and Seq stay zero because the SlashCommandList arm ignores both —
// one initialize exchange per child is not even per-turn, so there is no turn id
// to invent.
//
// FIRST DIVERGENCE FROM THE TWIN. The bool is still the only spelling of "nothing
// to send", but the guarantee behind it has a DIFFERENT SOURCE and the twin's
// sentence about it is FALSE here. resolveBoundModelList leans on
// turnevent.ModelList.Models being documented "Never empty";
// turnevent.SlashCommandList.Commands carries no such type-level guarantee — it is
// documented nil for a zero-length list and defers the question to its producer.
// The conclusion survives for the PRODUCER's reason, which is
// sessionSlashCommandHold.SlashCommandList's own wording rather than the twin's:
// streamsup's emitSlashCommandList returns early on a zero-length entry list
// (#1877), so nothing with zero entries ever reaches the retention and an empty
// inventory is not a value any reader can be handed. No arm returns true with an
// empty Commands and none returns a partially-filled payload. Unknown
// conversation, unbound conversation, session gone, runner without the method and
// nothing reported all answer (zero, false).
//
// THIRD DIVERGENCE. DroppedCommands is a SUM OF TWO SOURCES where the twin's
// DroppedModels carries one: the mapping computes the decode's own drops plus
// whatever the #2002 serialised bound cut from the tail. It rides through
// UNTOUCHED — never recomputed from len(Commands), never zeroed — so
// len(Commands) + DroppedCommands is the inventory's true size after both cuts,
// and shipping 0 would tell a client that a capped menu is the whole menu.
//
// Value ownership: sessionSlashCommandHold.SlashCommandList hands back a DEEP COPY
// this function solely owns — three levels, since each entry's Aliases and
// TruncatedFields are cloned too — and MapEvent allocates a fresh outer slice
// while each row's two []string fields cross by reference from that copy. The copy
// is per-call and never retained, so the payload owns its slices outright and two
// callers never share a backing array — do not add a second clone.
//
// SECURITY: convID is untrusted network input and never leaves this function — a
// lookup key into the daemon's own registry and nothing else, never returned on a
// refusal, never joined into a path, never wrapped into an error. Pool.Lookup's
// error is DISCARDED rather than wrapped, resolveBoundRunSettings' stated reason:
// returning it bare is what keeps a hostile or malformed id from being reflected
// into a log line or a wire frame a caller builds from it. This resolver takes no
// logger and MUST NOT grow one — the only thing a "why did it not resolve" line
// could add is the caller's id or the command strings themselves, and those are
// the WORKSPACE's rather than claude's, so whoever controls a repository controls
// them. That is the #833 posture with the threat one step sharper, and
// sessionSlashCommandHold enforces the same property by construction, having no
// logger field. The strings are bounded but NOT sanitized and the render boundary
// owing that is the CLIENT's, as both payload types' SECURITY paragraphs assign;
// this slice adds no sink of its own.
//
// Concurrency: a synchronous read on the caller's goroutine that spawns nothing,
// mints nothing and mutates nothing. Its three locks — the registry's, the pool's
// RLock and the hold's leaf mutex — are acquired SEQUENTIALLY and never nested, so
// this adds no edge to the daemon's lock order; do not restructure the body so one
// lookup happens inside another's scope. The Get → Lookup window is the benign
// TOCTOU all four siblings share: a rotation landing in it leaves the id either
// resolvable (we answer the inventory of the session bound a moment ago) or not
// (we refuse), and both are correct because a slash-command list is a property of
// one child's initialize reply and a rotation's fresh child reports its own. No
// re-read, no retry.
func resolveBoundSlashCommandList(convReg *conversations.Registry, pool *sessions.Pool, convID string) (protocol.SlashCommandListPayload, bool) {
	conv, ok := convReg.Get(conversations.ConversationID(convID))
	if !ok || conv.CurrentSessionID == "" {
		return protocol.SlashCommandListPayload{}, false
	}
	sess, err := pool.Lookup(sessions.SessionID(conv.CurrentSessionID))
	if err != nil {
		return protocol.SlashCommandListPayload{}, false
	}
	lister, ok := sess.Runner().(interface {
		SlashCommandList() (turnevent.SlashCommandList, bool)
	})
	if !ok {
		return protocol.SlashCommandListPayload{}, false
	}
	list, ok := lister.SlashCommandList()
	if !ok {
		return protocol.SlashCommandListPayload{}, false
	}
	// MapEvent returns `payload any` (the payloads share no marker interface), so
	// reuse costs one assertion back to protocol.SlashCommandListPayload — and that
	// assertion is the ONLY discriminant. typ is discarded because comparing it to
	// protocol.TypeSlashCommandList would be a strictly weaker second spelling of
	// the same check; #2007 names the type itself when it builds the envelope.
	_, payload, ok := turnbridge.MapEvent(list, turnbridge.TurnContext{ConversationID: string(conv.ID)})
	if !ok {
		return protocol.SlashCommandListPayload{}, false
	}
	out, ok := payload.(protocol.SlashCommandListPayload)
	if !ok {
		return protocol.SlashCommandListPayload{}, false
	}
	// SECOND DIVERGENCE FROM THE TWIN, and it makes these two arms WEAKER than the
	// twin's rather than merely analogous. They are the TYPE SYSTEM's, not states
	// this daemon can reach — but where the ModelList arm at least suppresses
	// nothing observable, MapEvent's SlashCommandList arm has NO SUPPRESSION BRANCH
	// AT ALL and maps even a zero-value list, returning true by construction. The
	// gate deciding whether such an event exists is the producer's alone. They still
	// need an answer, and a refusal is the only one that keeps the contract above —
	// not a panic, and no log, since a log here would carry exactly the strings the
	// SECURITY paragraph keeps off that path. No fixture reaches them, deliberately.
	return out, true
}
