package protocol

import "encoding/json"

// Question-batch v2 wire payloads (#1963, dismissal #1974, the inbound answer and
// refusal #1983). These describe the clarifying-question batch claude's
// AskUserQuestion tool call carries, surfaced to a client as one frame whose Type
// is TypeQuestionShown, the frame that retires it (TypeQuestionDismissed), and the
// two a client sends back to resolve it (TypeQuestionAnswer, TypeQuestionRefused).
// Wire vocabulary only: pure structs and their serialization.
//
// NOTHING IN THIS PACKAGE CONSTRUCTS OR DECODES THEM — every producer and
// consumer is elsewhere. #1965 owns the parse that fills the batch from claude's
// tool input, #1973 the producer that emits both outbound frames, #1975 the nonce
// mint, #1985 the resolution of an answer against the daemon's parked batch, and
// pyrycode-desktop#849 / pyrycode-desktop#853 the clients that decode and send
// them. The two INBOUND shapes are decoded as of #1984, by the relay handlers
// behind dispatchAppFrame's cases for them.
// Declared ahead of all of those so that client can be written against the shape —
// the sequencing #1405 used ahead of #1410, #1616 ahead of #1638, #1704 ahead of
// #1848 and #1726 ahead of #1727.
//
// The testdata fixtures and the docs/protocol-mobile.md section HAVE LANDED
// (#1964): that section is where the field-by-field contract, the bounds and
// their enforcement are published for a client author, and it is the copy to
// keep true when this shape changes.
//
// Why this is its own frame family rather than a grown modal_shown, and why the
// options nest under each question, are argued in TypeQuestionShown's doc block
// (internal/protocol/codes.go, #1962), which also records the naming trap
// question_shown avoids. Read them there; they are not restated here.
//
// The batch is modelled WHOLE, in one frame. The desktop consumer steps one
// question at a time with header tabs that jump between questions and a Previous
// button, so every question has to be in hand at once — a sequence of
// single-question frames cannot serve that.
//
// The BOUNDS are documented here and enforced nowhere in this package. claude's
// contract (https://code.claude.com/docs/en/agent-sdk/user-input) states one to
// four questions per batch and two to four options per question; the fail-closed
// bounded parse is #1965's.
//
// The header cap is DOCUMENTED 12, OBSERVED 14. The vendor page says "Short label
// for the question (max 12 characters)", while the only header in the committed
// capture internal/e2e/realclaude/testdata/ask_user_question_v2.1.239.json is
// "Write strategy" — 14 runes and 14 bytes. The cap is therefore a
// generation-side guideline claude does not itself hold to, not a wire invariant,
// and #1965 MUST NOT enforce 12 fail-closed: its own acceptance criteria pin it
// against that same capture, so a 12-rune reject branch would make the two
// unsatisfiable together. Whatever bound it does land on has to NAME ITS UNIT — a
// rune count and a byte count diverge on the first non-ASCII header claude emits,
// and the observed header is pure ASCII, so nothing in the tree distinguishes
// them yet. The question and option COUNTS are not contradicted: the capture's
// one question and two options sit inside the documented 1-4 and 2-4.
//
// No field carries omitempty, § Modal's discipline unchanged: every field is
// always present on the wire, so an empty header or an unset multi_select is a
// real answer rather than a vanished one.

// QuestionShownPayload is the body of an Envelope whose Type ==
// TypeQuestionShown. Binary → phone direction; the wire form of one
// clarifying-question batch. The name is the mechanical <Type>Payload the family
// uses (TypeModalShown → ModalShownPayload, TypeSlashCommandList →
// SlashCommandListPayload).
//
// ConversationID is ModalShownPayload's #1065 outbound routing/scoping key,
// unchanged and for its reasons: daemon-asserted, never filled from claude's tool
// input, so interactive clients filter display by conversation. Present and
// unfilled by this ticket — the producer supplies it at mapping time.
//
// QuestionBatchID plays ModalID's role exactly, and all four of that paragraph's
// properties carry across: a one-time, opaque, UNGUESSABLE nonce minted per
// surfaced batch, which an inbound answer is resolved against server-side rather
// than trusting a phone-asserted conversation. "Unguessable" is what obliges
// #1975's minting to crypto/rand, mirroring #703's for ModalID. Adding this
// outbound key loosens no inbound guarantee.
//
// It is QuestionBatchID rather than QuestionID because this payload also declares
// a nested Question type: a question_id key sitting beside a questions array
// would read as that type's key, and Question carries no id at all, so the
// misreading is not idle. The _batch_ is what makes the field self-describing to
// #1973 and to pyrycode-desktop#849. QuestionDismissedPayload below carries the
// same key, which is what lets a client match a dismissal to the batch it clears.
//
// Questions is in claude's own order — the JSON-array order is the canonical
// display order. The key is always present on the wire and never null; see
// MarshalJSON.
type QuestionShownPayload struct {
	ConversationID  string     `json:"conversation_id"`
	QuestionBatchID string     `json:"question_batch_id"`
	Questions       []Question `json:"questions"`
}

// MarshalJSON normalises a nil Questions to an empty array, so a batch always
// serialises as "questions":[] and never as "questions":null.
//
// The reason is genuinely SHARED with Question.MarshalJSON's Options, so it is
// stated once here for both rather than split into two manufactured halves. Both
// empty arrays are OUT OF CONTRACT: the documented bounds are 1-4 questions and
// 2-4 options, so neither "questions":[] nor "options":[] is reachable in a
// well-formed batch, and a nil slice is only reachable from a constructed zero
// value or a producer bug. [] is the encoding that leaves such a frame decodable
// by a client whose array type is non-optional, where null fails that decode
// outright.
//
// That is one half of BackgroundTaskRosterPayload.MarshalJSON's rationale — the
// client-decode half — and the OTHER half deliberately does not transfer here.
// "An empty list is a positive statement" is true of an empty roster, which says
// the daemon has no background tasks, and false of an empty question array, which
// states nothing legal at all.
//
// Value receiver, so the substitution lands on a copy rather than on the caller's
// slice, and the type alias keeps json.Marshal from recursing back into this
// method — SlashCommandListPayload.MarshalJSON's reasons.
func (p QuestionShownPayload) MarshalJSON() ([]byte, error) {
	if p.Questions == nil {
		p.Questions = []Question{}
	}
	type alias QuestionShownPayload
	return json.Marshal(alias(p))
}

// Question is one question of a QuestionShownPayload batch, carrying claude's own
// per-question keys: the question text, its header, its ordered options and its
// multiSelect flag.
//
// Text's wire key is question, claude's own key, as SlashCommand keeps all four
// of claude's. The Go name diverges only because Question.Question stutters at
// every call site.
//
// MultiSelect is a plain bool on the wire, key multi_select — claude's camelCase
// multiSelect snake-cased, exactly as SlashCommand.ArgumentHint snake-cases
// argumentHint. Under the no-omitempty discipline the wire always states a
// position, so an absent key is not representable outbound and there is no
// tri-state to mint here; the absent-versus-well-formed-false question is real
// but it is #1965's, on the decode side.
//
// Options is ordered, always present on the wire and never null; see MarshalJSON.
//
// This shape carries NO TRUNCATION REPORT, unlike SlashCommand.TruncatedFields
// and ModelOption.TruncatedFields, and the omission is a decision rather than an
// oversight. Its consequence is #1965's: a producer that cuts a long question or
// description has nowhere to report the cut, so an over-long field must be
// rejected fail-closed rather than silently truncated — or this type comes back
// and grows the field. Cutting silently would present claude's truncated text to
// a client as complete.
//
// SECURITY: Text, Header, and every QuestionOption's Label and Description are
// CLAUDE-authored strings that crossed the subprocess trust boundary to a client
// render surface — ModelOption's trust level, not SlashCommand's lower
// workspace-authored one. They are safe to RENDER as inert text and must never be
// fed to an HTML sink, an attribute, or a URL; the daemon does not sanitize them
// — no control-character or terminal-escape stripping happens on this path — so
// they stay untrusted text all the way to the client, and the render boundary
// owing the sanitization is the CLIENT's. Their bound is the parse's (#1965) and
// the producer's (#1973): this struct re-decides no maximum and declares no
// charset check, because a second cap here would be a second place the limit is
// decided and the two could disagree silently.
type Question struct {
	Text        string           `json:"question"`
	Header      string           `json:"header"`
	Options     []QuestionOption `json:"options"`
	MultiSelect bool             `json:"multi_select"`
}

// MarshalJSON normalises a nil Options to an empty array, so a question always
// serialises as "options":[] and never as "options":null.
//
// The out-of-contract reason is QuestionShownPayload.MarshalJSON's, stated there
// once for both arrays because it is genuinely the same one — read it there
// rather than expecting a distinction this method does not have. ModelOption's
// normaliser and its payload's are the opposite case, two in one family whose
// reasons really do differ, each stating its own.
//
// The one reason that is this method's alone: it CANNOT BE FOLDED into the
// payload's. A payload marshaller normalising entries in place would reach
// through p.Questions[i] into the caller's backing array, which for a payload
// shared between an emitter goroutine and a per-connection fan-out is a data race
// as well as a correctness bug; and it would not fire at all when a Question is
// marshalled on its own.
//
// Value receiver and the type alias, for QuestionShownPayload.MarshalJSON's
// reasons.
func (q Question) MarshalJSON() ([]byte, error) {
	if q.Options == nil {
		q.Options = []QuestionOption{}
	}
	type alias Question
	return json.Marshal(alias(q))
}

// QuestionOption is one choice offered by a Question: Label is the short display
// text, Description the longer line under it. It is QuestionOption rather than a
// bare Option because ModalOption already exists in this package, and a bare
// Option beside it would read as the generic one.
//
// label and description are the COMPLETE per-option key set, and that is measured
// rather than assumed. claude's contract gives each option an optional preview
// carrying an HTML fragment, emitted only when toolConfig.askUserQuestion's
// previewFormat is set; pyry never sets it — neither previewFormat nor toolConfig
// appears anywhere under cmd/ or internal/ (verified 2026-09-01) — so preview is
// absent BY CONSTRUCTION rather than dropped. Recorded the way
// SlashCommandListPayload records its own measured key set, so that if pyry ever
// sets previewFormat the first reader finds the sentence saying why the field is
// not here.
//
// There is no id, unlike ModalOption's {id, label}: claude's answer protocol
// selects an option by its LABEL. The inbound answer (#1983) therefore does NOT
// identify an option by echoing that claude-authored string back — a selection
// names its question by INDEX and carries client-authored values, so no label
// makes the return trip; see QuestionAnswerEntry. Publishing a label here would
// not have made it trusted on the way back either way — the amendment
// ModelOption.Value's report-only convention sentence carries — and the daemon
// resolves an answer against its own parked copy regardless.
//
// The same label is why QuestionDismissedPayload.Outcome is forbidden from
// carrying one: see that field's paragraph.
//
// SECURITY: both strings are covered by Question's paragraph, which credits all
// four of the batch's claude-authored strings at once.
type QuestionOption struct {
	Label       string `json:"label"`
	Description string `json:"description"`
}

// QuestionDismissedPayload is the body of an Envelope whose Type ==
// TypeQuestionDismissed. Binary → phone direction; the frame that retires a batch
// a client is rendering, so no panel is left up for an ask that is already dead.
// Declared by #1974, emitted by #1973 (the no-answer terminal paths) and #1985
// (the answered one); nothing constructs it here.
//
// Field for field with ModalDismissedPayload, including the ABSENCES.
// QuestionBatchID plays ModalID's role — the nonce QuestionShownPayload carries,
// which is what matches a dismissal to the batch it clears.
//
// There is NO conversation_id, though question_shown carries one, and the modal
// frame's reason transfers whole: the batch id is the sole correlation key, and a
// shape carrying both would admit a disagreeing pair someone has to adjudicate. A
// client holding the batch already knows its conversation.
//
// No omitempty on any field, § Modal's and QuestionShownPayload's discipline
// unchanged, and NO MarshalJSON: there is no slice field here, so there is no
// nil→[] normalisation to perform. QuestionShownPayload.MarshalJSON's
// backing-array data-race argument is specific to a normaliser reaching through a
// shared slice and does not transfer — do not add one here by analogy to the
// sibling.
//
// SECURITY: this frame carries NO CLAUDE-AUTHORED BYTE, which is the property
// that separates it from QuestionShownPayload's trust tier, and it holds only
// because Outcome does what its paragraph says. All three fields are
// daemon-asserted. Consequences worth having in one place: § Security model's
// threat 1 (prompt injection reaching a remote render surface) does NOT land on
// this frame; no field can carry a byte of the parked tool input into a log; and
// the frame's length is daemon-determined rather than subprocess-influenced,
// which matters in a family that ships no truncated_fields and so could not
// report a cut.
//
// The nonce is DEAD once this frame lands, and receiving it is not a capability.
// A retired batch resolves nothing server-side, the way a stale modal_id resolves
// nothing under first-answer-wins (#703/#706). It is echoed to exactly the
// interactive-capability-gated audience that received question_shown, so
// disclosure widens nothing.
type QuestionDismissedPayload struct {
	// QuestionBatchID is the batch being cleared: QuestionShownPayload's own
	// nonce, echoed back. A client matches on it and clears nothing when it does
	// not recognise the value.
	QuestionBatchID string `json:"question_batch_id"`

	// Outcome is how the batch ended — a PRODUCER-DEFINED SENTINEL, plain string,
	// vocabulary owned by #1973/#1985 and documented rather than enforced, exactly
	// as ModalDismissedPayload.Outcome is #703's.
	//
	// IT MUST NEVER CARRY A CLAUDE-AUTHORED OPTION LABEL. The rule is stated
	// positively because the natural implementation violates it: QuestionOption
	// carries no id and claude's answer protocol selects by label, so an answer
	// path reporting which option was chosen reaches for that string first — and
	// it crossed the subprocess trust boundary. Putting it here would silently
	// move this frame to question_shown's trust tier while its published
	// provenance still said daemon-asserted, and a client would render it as
	// trusted chrome. A client that needs the label reads it from the batch it
	// already holds, keyed on QuestionBatchID.
	Outcome string `json:"outcome"`

	// Source is what resolved the batch. Plain string, NOT modal_dismissed's
	// closed {remote, local, timeout} — that set is provably short here, because
	// two of the producer's terminal paths (a caller disconnect, a daemon
	// shutdown) have no member in it and neither is a timeout nor an answer.
	// TypeQuestionDismissed's doc block carries the per-value carry-over reading;
	// docs/protocol-mobile.md § Question publishes it.
	//
	// A client must read an UNRECOGNISED value as "resolved, cause unknown" and
	// never as an answer. Getting that backwards renders a daemon safe-deny as the
	// operator's own choice, and the values a client written today will not
	// recognise are precisely the two the producer has yet to name.
	Source string `json:"source"`
}

// QuestionAnswerPayload is the body of an Envelope whose Type ==
// TypeQuestionAnswer (#1983). PHONE → BINARY direction — the first inbound frame
// in this family; everything above it is outbound. It carries the operator's
// selections for a batch question_shown surfaced.
//
// Like ModalAnswerPayload this is an inbound v2 CONTROL envelope, structurally
// like RequestSnapshotPayload / TypeRekeyRequest: it is intercepted at
// internal/relay/v2session.go's dispatchAppFrame BEFORE dispatch.Route, and there
// is no dispatch.Route handler for it. #1984 added that case and the handler
// behind it, which decodes this shape and hands it to a resolver seam; #1985
// resolves an answer against the daemon's parked batch. The guard classification
// that follows from being switch-intercepted is argued in TypeQuestionAnswer's doc
// block (codes.go); read it there.
//
// There is NO conversation_id, though question_shown carries one, and
// QuestionDismissedPayload's reason transfers whole: the batch id is the sole
// correlation key, and a shape carrying both would admit a disagreeing pair
// someone has to adjudicate. Here that absence is also the SECURITY property —
// ModalAnswerPayload's, unchanged: the daemon resolves QuestionBatchID against
// its own outstanding-batch state and never trusts a phone-asserted conversation.
//
// AnswerToken is ModalAnswerPayload's field verbatim, including its reasons: a
// CLIENT-MINTED IDEMPOTENCY KEY whose uniqueness and stability matter and whose
// secrecy does not, and which is NOT the authorization. The daemon's actual dedup
// is the one-shot consume of QuestionBatchID, exactly as modal_answer's is of
// ModalID. Neither field here is a secret and both are safe to log — stated so
// that #1984's handler neither invents a redaction rule nor assumes one exists.
//
// Answers is ordered, always present on the wire and never null; see MarshalJSON.
// ARRAY ORDER IS NOT THE CORRELATION: a client should emit entries in batch
// order, but QuestionAnswerEntry.QuestionIndex is what selects, so a decoder must
// never infer the question from an entry's array position.
//
// No field carries omitempty, § Modal's and QuestionShownPayload's discipline
// unchanged.
//
// THIS PACKAGE ENFORCES NO BOUNDS ON THIS SHAPE, by the same design that leaves
// the outbound batch's bounds to questionbridge.Parse. Answers may be arbitrarily
// long, an entry may repeat or omit an index, and a value may be arbitrarily
// large; the only operative limit today is the transport's AEAD frame cap, which
// bounds total bytes and not entry count. Those are obligations on whoever
// decodes (#1984) and resolves (#1985), and they are enumerated on
// QuestionAnswerEntry rather than implied here. One belongs to the decode itself
// and is DISCHARGED by #1984's handler: a decode failure MUST be a rejected frame,
// never an empty-but-successful answer, and its error must not embed the raw
// payload — those bytes are remote-authored and nothing on this path strips
// terminal escape sequences. The rule stays stated as a rule because it binds
// every future decoder of this shape, not only the first; note that it is a rule
// about a decode ERROR, so a payload of `null` — which decodes cleanly into the
// zero value — is not a rejected frame but an unknown batch, and judging it is the
// resolver's, not the decoder's.
type QuestionAnswerPayload struct {
	QuestionBatchID string                `json:"question_batch_id"`
	AnswerToken     string                `json:"answer_token"`
	Answers         []QuestionAnswerEntry `json:"answers"`
}

// MarshalJSON normalises a nil Answers to an empty array, so an answer always
// serialises as "answers":[] and never as "answers":null.
//
// The reason is genuinely SHARED with QuestionAnswerEntry.MarshalJSON's Values,
// so it is stated once here for both. Both empty arrays are OUT OF CONTRACT: an
// answer with no entries says nothing a refusal does not say better, which is why
// question_refused is its own type, and an entry with no values selects nothing.
// [] is the encoding that leaves such a frame decodable by a client whose array
// type is non-optional, where null fails that decode outright — the client-decode
// half of BackgroundTaskRosterPayload.MarshalJSON's rationale, and the other half
// ("an empty list is a positive statement") deliberately does not transfer, for
// QuestionShownPayload.MarshalJSON's reason.
//
// Value receiver, so the substitution lands on a copy rather than on the caller's
// slice, and the type alias keeps json.Marshal from recursing back into this
// method — SlashCommandListPayload.MarshalJSON's reasons.
func (p QuestionAnswerPayload) MarshalJSON() ([]byte, error) {
	if p.Answers == nil {
		p.Answers = []QuestionAnswerEntry{}
	}
	type alias QuestionAnswerPayload
	return json.Marshal(alias(p))
}

// QuestionAnswerEntry is one question's answer inside a QuestionAnswerPayload:
// which question, and the values chosen for it.
//
// It is QuestionAnswerEntry rather than QuestionAnswer because QuestionAnswer
// beside QuestionAnswerPayload differs only by the family's frame-body suffix, so
// the pair reads as "the payload of a QuestionAnswer" rather than "the body of a
// question_answer frame". The nested types under question_shown did not need the
// disambiguation — Question is not QuestionShown — so this suffix is bought
// rather than inherited.
//
// QuestionIndex is the entry's index into the batch's questions array, the
// canonical display order QuestionShownPayload publishes. The wire key is
// question_index and not index because an entry quoted or logged on its own must
// not read as "the index of this answer". The daemon reads the question text from
// its own parked copy keyed on the payload's QuestionBatchID; nothing about the
// question travels back.
//
// THE INDEX IS CARRIED, NEVER RANGE-CHECKED HERE, and the consequence has to be
// loud because it is a panic rather than a wrong answer: subscripting the parked
// batch with a negative or over-large index panics, so #1985 owes an EXPLICIT
// range check before indexing. Two more obligations fall on the same reader and
// are named here rather than left to be discovered — a duplicate or missing index
// across entries must not resolve to a silently partial or last-write-wins
// answer, and an Answers array far longer than the parked batch must not become
// unbounded work per inbound frame. internal/protocol enforcing no bound is the
// same design that leaves the outbound batch's bounds to questionbridge.Parse
// (#1965); it is not a guarantee this type makes.
//
// It is a plain int rather than an unsigned type on purpose. A uint would reject
// -1 at decode and still accept 1<<62 — half-closing the door while turning a
// range problem into a decode-error surprise — and it would smuggle a bounds
// decision into a package that makes none.
//
// Values are the strings chosen for this question, ordered, always present and
// never null; see MarshalJSON. They are CLIENT-AUTHORED AND OPAQUE, and they are
// NEVER CHECKED against the batch's offered labels: claude's contract permits
// free text anywhere and requires no value to be one of the labels, so a
// validator rejecting an unlisted value would reject a legal answer. More than
// one value is the multi_select case; one is the ordinary one.
//
// SECURITY: these strings are REMOTE-AUTHORED — they crossed the network from a
// paired but not trusted client, the opposite direction from Question's
// claude-authored ones, so § Security model's threat 1 does not describe them.
// What they do reach is claude's context, via #1985 feeding them back as the
// blocked tool call's result. That grants nothing new: a paired client can
// already put arbitrary text into the conversation with send_message, so an
// answer sits at exactly that trust tier — recorded because the shape invites the
// opposite reading, that an answer is somehow more constrained than a message.
type QuestionAnswerEntry struct {
	QuestionIndex int      `json:"question_index"`
	Values        []string `json:"values"`
}

// MarshalJSON normalises a nil Values to an empty array, so an entry always
// serialises as "values":[] and never as "values":null.
//
// The out-of-contract reason is QuestionAnswerPayload.MarshalJSON's, stated there
// once for both arrays because it is genuinely the same one — read it there
// rather than expecting a distinction this method does not have.
//
// The one reason that is this method's alone is Question.MarshalJSON's, and it
// transfers verbatim: it CANNOT BE FOLDED into the payload's. A payload
// marshaller normalising entries in place would reach through p.Answers[i] into
// the caller's backing array, which for a payload shared between goroutines is a
// data race as well as a correctness bug; and it would not fire at all when an
// entry is marshalled on its own.
//
// Value receiver and the type alias, for QuestionAnswerPayload.MarshalJSON's
// reasons.
func (e QuestionAnswerEntry) MarshalJSON() ([]byte, error) {
	if e.Values == nil {
		e.Values = []string{}
	}
	type alias QuestionAnswerEntry
	return json.Marshal(alias(e))
}

// QuestionRefusedPayload is the body of an Envelope whose Type ==
// TypeQuestionRefused (#1983). Phone → binary direction; the operator declined to
// choose, so the batch is resolved without any selection.
//
// It is its OWN TYPE rather than a QuestionAnswerPayload with an empty Answers
// array or a nullable flag, mirroring TypeModalCancel beside TypeModalAnswer and
// following the same precedent question_dismissed set: a distinct meaning gets a
// distinct type, so a reader routes on the frame's name rather than on a value's
// shape. A refusal carries no answers, and a shape able to express "answered with
// nothing" would need someone to adjudicate it against a genuine refusal.
//
// Field for field with QuestionAnswerPayload minus Answers, INCLUDING THE
// ABSENCE: no conversation_id, for that type's reasons. AnswerToken is carried —
// a refusal is as replayable as an answer, and the daemon's dedup is the same
// one-shot consume of QuestionBatchID. No omitempty on either field.
//
// NO MarshalJSON: there is no slice field here, so there is no nil→[]
// normalisation to perform. QuestionAnswerPayload.MarshalJSON's backing-array
// argument is specific to a normaliser reaching through a shared slice and does
// not transfer — do not add one here by analogy to the sibling, the note
// QuestionDismissedPayload carries for the same reason.
//
// SECURITY: this frame carries NO FREE TEXT AT ALL, which makes it the narrowest
// surface in the family — both fields are ids a client echoes back. Like its
// sibling it is switch-intercepted as of #1984, and being intercepted grants no
// inbound capability: that handler applies no authorization, and what keeps it
// fail-safe is a resolver seam left nil at every construction site. The interactive
// gate and the per-device answer gate (#702) stay the resolver's to apply, default
// deny, with #1986 installing the latter before anything is wired.
type QuestionRefusedPayload struct {
	QuestionBatchID string `json:"question_batch_id"`
	AnswerToken     string `json:"answer_token"`
}
