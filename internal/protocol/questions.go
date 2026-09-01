package protocol

import "encoding/json"

// Question-batch v2 wire payloads (#1963). These describe the clarifying-question
// batch claude's AskUserQuestion tool call carries, surfaced to a client as one
// frame whose Type is TypeQuestionShown. Wire vocabulary only: pure structs and
// their serialization.
//
// NOTHING CONSTRUCTS THEM. #1965 owns the parse that fills them from claude's
// tool input, #1927 the producer that emits the frame and mints the nonce, and
// pyrycode-desktop#849 the client that decodes them. Declared ahead of all three
// so that client can be written against the shape — the sequencing #1405 used
// ahead of #1410, #1616 ahead of #1638, #1704 ahead of #1848 and #1726 ahead of
// #1727.
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
// #1927's minting to crypto/rand, mirroring #703's for ModalID. Adding this
// outbound key loosens no inbound guarantee.
//
// It is QuestionBatchID rather than QuestionID because this payload also declares
// a nested Question type: a question_id key sitting beside a questions array
// would read as that type's key, and Question carries no id at all, so the
// misreading is not idle. The _batch_ is what makes the field self-describing to
// #1927 and to pyrycode-desktop#849.
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
// the producer's (#1927): this struct re-decides no maximum and declares no
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
// selects an option by its LABEL. So whatever inbound answer #1927 designs will
// identify an option by a claude-authored string, and publishing that string does
// not make it trusted when it comes back — the amendment ModelOption.Value's
// report-only convention sentence carries. This slice declares no inbound verb
// and grants nothing; TypeQuestionShown's doc block has that reasoning.
//
// SECURITY: both strings are covered by Question's paragraph, which credits all
// four of the batch's claude-authored strings at once.
type QuestionOption struct {
	Label       string `json:"label"`
	Description string `json:"description"`
}
