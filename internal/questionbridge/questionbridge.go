// Package questionbridge parses clarifying-question tool calls into bounded
// outbound payloads and records surfaced batches under daemon-minted identities.
// Parse takes a tool name and input rather than an approval request, so it holds
// no approval correlation and depends only on protocol and the standard library.
//
// Batches are rejected whole rather than truncated: this control-frame family
// has no truncated_fields marker, so clipped text would appear complete. Raw
// input is bounded before decode; encoded payloads reserve space for the full
// envelope and daemon metadata. Unknown input keys remain tolerated.
//
// The package is log-free. Question content stays inert text, and Parse leaves
// both daemon-asserted IDs unset for Registry.Record to assign.
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

// maxInputBytes bounds raw tool input before decode, independently of the
// encoded-envelope bound. Individual fields have no separate length cap; the
// captured 14-rune header remains supported.
const maxInputBytes = 16384

// maxPayloadBytes bounds the JSON payload with unset daemon IDs. The 4096-byte
// reserve covers sixfold escaping of two 36-byte IDs and a 256-byte producing
// session ID, envelope keys, maximum-width uint64 stamps, the longest supported
// RFC3339Nano timestamp and boolean flags, including a session-state clear.
const maxPayloadBytes = protocol.MaxThreadEnvelopeBytes - 4096

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
// truncated one: the batch is accepted or rejected as a whole.
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
	payload := protocol.QuestionShownPayload{Questions: questions}
	encoded, err := json.Marshal(payload)
	if err != nil || len(encoded) > maxPayloadBytes {
		return protocol.QuestionShownPayload{}, false
	}
	return payload, true
}
