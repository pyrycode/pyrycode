package protocol

import (
	"encoding/json"
	"time"
)

// RequestHistoryPayload is the body of an Envelope whose Type ==
// TypeRequestHistory (docs/protocol-mobile.md § Conversation history, published
// by #2113). The frame a client sends to ask for older entries of a conversation
// it already has open. Wire vocabulary only — nothing constructs, decodes or
// validates it here; #2116 answers it and owns every reject below.
//
// ONE DIRECTION ONLY, phone → binary, so there is no provenance to disambiguate:
// EVERY FIELD IS AN UNVERIFIED CLAIM, ALWAYS. That is the same posture
// RequestAttachmentPayload ships with and for the same reason — the frame it is
// answered with rides the other way and shares no type with it.
//
// PAGED, NEWEST-FIRST, WALKING BACKWARDS. A client opens a conversation at its
// most recent entries and asks for older ones as the operator scrolls up; it
// never reads forward from the beginning. The shape mirrors history.Store.Page,
// which is what serves it.
//
// THE CURSOR, NOT AN OFFSET AND NOT A PAGE NUMBER. Both of those are invalidated
// by appends landing while the operator scrolls; a cursor names a position in an
// append-only file that nothing ever rewrites. It is minted by the daemon's log,
// OPAQUE on this wire, and a client only ever echoes back what it was handed.
// history.parseCursor is the ONLY validator anywhere — nothing above
// internal/history may parse one.
//
// OPACITY IS A CONVENTION, NOT A SECURITY PRIMITIVE, and the distinction has to
// be stated because base64 invites the opposite reading. A cursor is NOT A SECRET
// AND NOT A CAPABILITY: it is trivially reversible and it carries the
// conversation id the client already knows. It is bound to that conversation by
// parseCursor, and it is deliberately unsigned — a MAC would imply an
// authorization it does not carry, where authorization is pairing, enforced
// structurally at the Noise IK handshake.
//
// IT NAMES A CONVERSATION, because history.Store.Page's precondition is a bound
// conversation and this wire is multi-conversation — TypeRequestSessionSettings
// and TypeRequestAttachment both name one. THE ID IS A LOOKUP KEY, NEVER A VALUE
// TRUSTED AS SENT: it is validated against the daemon's own registry BEFORE IT
// REACHES A PATH JOIN, and NAMING A CONVERSATION IS NOT AUTHORIZATION.
// RequestAttachmentPayload's block carries that rule in full, and it is the same
// rule here.
//
// CORRELATION RIDES THE ENVELOPE'S InReplyTo, so the payload carries NO
// REQUEST-ID KEY — TypeAttachmentStored's block has that decision and it
// transfers unchanged. TestRequestHistoryPayload_WireKeys pins the key set so
// this is checked rather than reviewed, and the committed
// testdata/history_page.json rides in_reply_to 140 answering the envelope 140 of
// testdata/request_history.json, so a request-id key added later would leave a
// landed fixture describing a different scheme.
//
// A HOSTILE OR TRUNCATED PAYLOAD DECODES TO THE ZERO VALUE, NOT TO AN ERROR,
// since every key is optional to encoding/json. A DECODE FAILURE IS A REJECTED
// FRAME, NEVER AN EMPTY-BUT-SUCCESSFUL REQUEST — QuestionAnswerPayload's
// published obligation, and discharging it is #2116's. The specific silent
// failure this warns about: the empty conversation id names nothing, and joining
// it into a path resolves to the log ROOT rather than erroring.
//
// NO omitempty AND NO MarshalJSON. All three keys are always present, so a
// decoder may rely on all three, and Limit's zero is MEANINGFUL rather than
// absent (see its own comment). TestRequestHistoryPayload_WireKeys marshals a
// zero value, so an omitempty added later for tidiness reddens there.
//
// SECURITY: SENDING THIS FRAME IS NOT A CAPABILITY, and neither is receiving an
// answer. There is no per-verb gate and none is invented here; what bounds a
// paired but hostile client is confinement to the conversation it named plus the
// daemon's validation of that name. Both strings are client-supplied and are
// LOGGABLE ONLY AFTER THEIR SHAPE IS VALIDATED — raw, either is the
// log-injection shape § Attachments already forbids for a filename, and
// history.cursorRefusal already declines to echo a cursor for exactly that
// reason. Any bound on concurrent or repeated history requests is
// receiver-configured and unpublished, learned by being rejected.
type RequestHistoryPayload struct {
	// ConversationID names the conversation whose history is wanted. A lookup key
	// validated against the daemon's registry before it resolves anything, and not
	// authorization; the empty string names nothing and resolves nothing.
	ConversationID string `json:"conversation_id"`

	// Cursor is the position handed back by the previous page, echoed verbatim.
	// EMPTY MEANS "START AT THE NEWEST" — the first ask of a walk has nothing to
	// echo yet, so empty is the normal opening value and not a missing one.
	Cursor string `json:"cursor"`

	// Limit is how many entries the client wants, and its ZERO IS MEANINGFUL: 0
	// (whether sent as 0 or omitted entirely) asks the DAEMON TO CHOOSE, and must
	// never be read as a request for zero entries. history.Store.Page refuses a
	// limit below 1, so an absent key reaching it as a literal 0 would turn every
	// client that omits the field into an error — which is why the zero is
	// assigned a meaning here rather than left to the handler.
	//
	// A positive value is honoured up to history.MaxPageEntries, where it is
	// CLAMPED rather than refused. A negative value is a reject condition (#2116
	// owns its code).
	//
	// NEVER ALLOCATE FROM THIS CLAIM. It is the one count in this vocabulary and
	// AttachmentChunkPayload's NEVER ALLOCATE FROM A CLAIM rule applies verbatim:
	// hand it to history.Store.Page and let the clamp decide, rather than sizing
	// any buffer from an attacker-chosen integer.
	Limit int `json:"limit"`
}

// HistoryEntry is one entry of a HistoryPagePayload: one past wire frame, as it
// was stored. It MIRRORS history.Entry key for key, which is the whole point —
// a client re-reduces a loaded page oldest-first through the timeline reducer it
// already has for the live stream, and that only works if an entry is shaped like
// the frame it replays.
//
// ID IS THE DURABLE, PER-CONVERSATION ENTRY ID FROM THE LOG, and it is
// deliberately NOT Envelope.EventID. Ring ids are per-process and do not survive
// a daemon restart; this one is on disk and does. A client must not join the two
// namespaces — they are different sequences that both look like small integers.
//
// TRUST CLASS, AND IT IS NOT THE PAGE'S. Type and Payload are REPLAYED CONTENT:
// the bytes of a frame that was appended to the log, operator-authored for a
// stored send_message and CLAUDE-AUTHORED for a stored assistant frame. An entry
// therefore carries EXACTLY THE TRUST CLASS OF THE LIVE FRAME IT MIRRORS, and a
// client applies the same sanitisation it applies on the live lane.
// docs/protocol-mobile.md § Security model's threat 1 LANDS on this frame — the
// opposite of RequestAttachmentPayload, which carries no content at all, so that
// type's "threat 1 does not land here" does not transfer.
//
// Type is a STORED STRING that nothing re-validates against this package's Type*
// vocabulary, so a client must tolerate an entry type it does not know rather
// than treating one as a protocol violation. Payload is carried opaquely and
// nothing in this package decodes it.
type HistoryEntry struct {
	// ID is the durable per-conversation entry id (history.Entry.ID), monotonic
	// within one conversation's log and stable across daemon restarts.
	ID uint64 `json:"id"`

	// Type is the wire type the stored frame carried. Unvalidated; tolerate an
	// unknown one.
	Type string `json:"type"`

	// Payload is the stored frame's body, verbatim. Replayed content — see the
	// type's trust-class block.
	Payload json.RawMessage `json:"payload"`

	// TS is when the entry was appended.
	TS time.Time `json:"ts"`
}

// HistoryPagePayload is the body of an Envelope whose Type == TypeHistoryPage
// (docs/protocol-mobile.md § Conversation history, published by #2113). The
// daemon's answer to a TypeRequestHistory: one backward step of a walk. It
// mirrors history.Page. Wire vocabulary only — #2116 emits it.
//
// A WALK TERMINATES ON AtStart, NEVER ON AN EMPTY Entries. A page that fills
// EXACTLY at the log's first entry reports AtStart false with a usable Cursor;
// the call after it returns no entries with AtStart true. That is history.Page's
// own contract, restated here because the tempting client implementation ("stop
// when a page comes back empty") is wrong against it and the tempting producer
// implementation (report AtStart only on an empty page) is wrong the other way.
// Cursor is empty whenever AtStart is set, so the two are never both meaningful.
//
// THE CLAMP BOUNDS ENTRIES, NOT BYTES, and the consequence is a client-visible
// contract rather than an implementation detail. history.MaxPageEntries caps the
// entry COUNT; nothing bounds a stored entry's payload, so a full page can exceed
// the v2 application-envelope cap and be undeliverable as the daemon's OWN
// outbound frame. A producer may therefore return FEWER ENTRIES THAN ASKED to fit
// that cap. This is precisely why termination is AtStart and not an entry count:
// byte-driven truncation is safe only for a client that never infers "short page
// ⇒ start of log". The byte budgeting itself is #2116's.
//
// NO conversation_id, and that is a decision rather than an omission.
// Correlation rides the envelope's InReplyTo, the shape SessionSettingsPayload
// already takes — it answers a request that named a conversation and carries none
// itself. The cost is stated rather than hidden: a client keeps its outstanding
// asks keyed by envelope id. The gain is that NOTHING IN A PAGE IS ECHOED BACK
// FROM THE REQUEST, which is not the same as every field being daemon-authored —
// each entry's Type and Payload are replayed content, per HistoryEntry's block.
// TestHistoryPagePayload_WireKeys pins the absence.
type HistoryPagePayload struct {
	// Entries are this page's entries, NEWEST-FIRST. A client re-reduces them
	// oldest-first through its existing timeline reducer.
	Entries []HistoryEntry `json:"entries"`

	// Cursor is the position to ask with next, opaque to the client. Empty
	// whenever AtStart is set.
	Cursor string `json:"cursor"`

	// AtStart reports that the start of the log was reached while filling this
	// page. It is the ONLY termination signal; an empty Entries is not one.
	AtStart bool `json:"at_start"`
}

// MarshalJSON normalises a nil Entries to an empty array, so a page always
// serialises as "entries":[] and never as "entries":null.
//
// The reachable case is a real one rather than a constructed-zero-value
// curiosity: history.Store.Page returns Page{AtStart: true} with no entries for a
// conversation that was never written to, which is the terminal-empty page a walk
// ends on. null fails the decode of a client whose array type is non-optional —
// QuestionShownPayload.MarshalJSON's client-decode reason, and here the OTHER
// half of that block's rationale transfers too: an empty list is a POSITIVE
// STATEMENT, saying this conversation has nothing older, where an empty question
// array states nothing legal at all.
//
// Value receiver, so the substitution lands on a copy rather than on the caller's
// slice, and the type alias keeps json.Marshal from recursing back into this
// method.
func (p HistoryPagePayload) MarshalJSON() ([]byte, error) {
	if p.Entries == nil {
		p.Entries = []HistoryEntry{}
	}
	type alias HistoryPagePayload
	return json.Marshal(alias(p))
}
