// Package questionbridge recognises claude's clarifying-question tool call,
// parses its batch into the outbound wire payload, and records the surfaced
// batch under a one-time nonce it mints (Registry). It is named after its two
// neighbours, internal/permbridge and internal/modalbridge, and sits beside them
// rather than inside internal/protocol, which enforces no bound by design, or
// inside modalbridge, whose whole argument (TypeQuestionShown's doc block) is
// that a question is a distinct frame family rather than a grown modal.
//
// The input is a tool name and the opaque tool input claude blocks on across the
// mcp-approve bridge — permbridge.Request's ToolName and Input. The parse takes
// the PAIR rather than the Request, so it never holds a ToolUseID it has no
// business reading and depends on internal/protocol and the standard library
// alone.
//
// FAIL-CLOSED, AND REJECT RATHER THAN TRUNCATE. The result becomes a control
// frame, and control frames are never dropped by the push queue (soft-overflow
// admit), so an un-droppable frame must not be inflatable — modalbridge's
// maxPromptBytes posture. Its MECHANISM does not carry over: boundPrompt
// silently truncates, and this family has no truncated_fields to report a cut
// in, so a cut would present claude's clipped text to a client as complete.
// docs/protocol-mobile.md's Question (v2) section forbids exactly that. The cap
// is therefore taken once, over the whole raw input, and rejects it whole.
//
// Fail-closed means BOUNDS, NOT UNKNOWN KEYS. The decode is a plain
// json.Unmarshal: a claude release adding a field — the vendor docs already
// describe an optional per-option preview, emitted only when
// toolConfig.askUserQuestion's previewFormat is set, which pyry never sets —
// must not turn every question into a dropped frame.
//
// LOG-FREE, deliberately and structurally: this package does not import
// log/slog, so no question text, header, option label or description, and no
// other byte of claude's tool input, can reach a log from here. permbridge
// carries the same property for the same reason. Content-free decision logging
// belongs at the call site, where a conversation id is in hand.
//
// NOTHING CALLS THIS YET. #1973 is the consumer: it wires the discriminant into
// the stream approval surfacer, then hands the parsed batch and its conversation
// id to Registry.Record, which mints QuestionBatchID from crypto/rand and stamps
// both ids on. Parse leaves the pair deliberately zero because they are
// daemon-asserted and never come from claude's tool input.
package questionbridge

import (
	"encoding/json"

	"github.com/pyrycode/pyrycode/internal/protocol"
)

// ToolName is claude's clarifying-question tool, and the discriminant that tells
// this call apart from a permission prompt on the shared approval bridge. The
// name is the agent SDK's; the committed capture at
// internal/e2e/realclaude/testdata/ask_user_question_v2.1.239.json is the
// authority on what claude actually sends, and agrees.
const ToolName = "AskUserQuestion"

// maxInputBytes bounds the raw tool input. THE UNIT IS BYTES, counted over
// Request.Input before decode — stated here because a rune count and a byte
// count diverge on the first non-ASCII string claude emits, and nothing
// committed anywhere separates them for this family yet.
//
// Sized by arithmetic rather than by measurement, since one capture is the whole
// population: a worst-case in-contract batch — four questions, each with prose
// text and four options carrying a prose description — comes to roughly 8 KB, so
// 16 KiB clears legitimate output by about 2× while still capping a pathological
// input. The committed capture's tool input is well under 1 KB. If a real batch
// is ever seen near this figure, the constant moves and this comment records the
// new evidence.
//
// This is the ONLY length bound in the package. The four claude-authored strings
// carry no individual cap, and the header carries none in particular: the vendor
// documents 12 characters, the one header anybody has captured is 14, and
// internal/protocol's questions.go records that contradiction along with the
// instruction not to enforce 12 fail-closed. Minting a different header number
// would be a defence for a failure mode nobody has observed.
const maxInputBytes = 16384

// Batch bounds from claude's contract (https://code.claude.com/docs/en/agent-sdk/user-input).
// The capture's one question and two options sit inside both, so neither is
// contradicted by observation the way the header cap is.
const (
	minQuestions = 1
	maxQuestions = 4
	minOptions   = 2
	maxOptions   = 4
)

// toolInput mirrors claude's own keys for the question tool. Unexported: it is a
// decode target, not vocabulary — the wire vocabulary is protocol's.
type toolInput struct {
	Questions []inputQuestion `json:"questions"`
}

// inputQuestion is one question as claude sends it, in claude's camelCase.
//
// MultiSelect is a *bool, so an ABSENT key stays distinguishable from a
// well-formed false. That distinction is the point: protocol.Question's
// MultiSelect is a plain bool under a no-omitempty discipline, so an absent key
// silently becoming false would put a stated position on the wire that claude
// never sent. JSON null leaves the pointer nil and so rejects too, which reads
// correctly — null states no position either.
//
// It is a pointer where ask_user_question_shape_test.go's decode target uses a
// json.RawMessage for the same distinction, and the divergence is deliberate:
// that helper must survive a wrong-typed value in order to REPORT it as a
// finding, whereas here a non-bool multiSelect should reject anyway, so the
// pointer folds that case into the malformed-JSON branch instead of paying for a
// second unmarshal and an eighth reject branch.
type inputQuestion struct {
	Question    string        `json:"question"`
	Header      string        `json:"header"`
	Options     []inputOption `json:"options"`
	MultiSelect *bool         `json:"multiSelect"`
}

// inputOption is one offered choice. label and description are the complete
// per-option key set claude sends pyry; see the package doc on preview.
type inputOption struct {
	Label       string `json:"label"`
	Description string `json:"description"`
}

// Parse reports whether toolName is claude's question tool AND input is a
// well-formed, in-bounds batch, returning that batch as the outbound payload
// with its questions and their options in claude's own array order — which is
// the canonical display order.
//
// Every rejection yields the ZERO payload and false, never a partial or a
// truncated one: nothing is built until every check has passed.
//
// The two negative outcomes — not the question tool, and the question tool
// rejected — are deliberately NOT distinguished. A caller that needs to tell
// them apart compares against ToolName itself, which is exported for exactly
// that; minting an error or a sentinel here would be API nothing consumes.
//
// ConversationID and QuestionBatchID are left unset: both are daemon-asserted,
// and a parse that filled either would be asserting daemon state out of claude's
// tool input.
func Parse(toolName string, input json.RawMessage) (protocol.QuestionShownPayload, bool) {
	if toolName != ToolName {
		return protocol.QuestionShownPayload{}, false
	}
	// Before the decode, so an oversized input is never parsed.
	if len(input) > maxInputBytes {
		return protocol.QuestionShownPayload{}, false
	}
	var in toolInput
	if err := json.Unmarshal(input, &in); err != nil {
		return protocol.QuestionShownPayload{}, false
	}
	if len(in.Questions) < minQuestions || len(in.Questions) > maxQuestions {
		return protocol.QuestionShownPayload{}, false
	}

	questions := make([]protocol.Question, 0, len(in.Questions))
	for _, q := range in.Questions {
		if len(q.Options) < minOptions || len(q.Options) > maxOptions {
			return protocol.QuestionShownPayload{}, false
		}
		if q.MultiSelect == nil {
			return protocol.QuestionShownPayload{}, false
		}
		options := make([]protocol.QuestionOption, len(q.Options))
		for i, o := range q.Options {
			options[i] = protocol.QuestionOption{Label: o.Label, Description: o.Description}
		}
		questions = append(questions, protocol.Question{
			Text:        q.Question,
			Header:      q.Header,
			Options:     options,
			MultiSelect: *q.MultiSelect,
		})
	}
	return protocol.QuestionShownPayload{Questions: questions}, true
}
