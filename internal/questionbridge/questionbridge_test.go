package questionbridge

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/pyrycode/pyrycode/internal/protocol"
)

// capturePath is the committed AskUserQuestion capture, read by RELATIVE PATH
// rather than copied inline so this pin cannot drift from the bytes the live
// capture family writes. The Go files in internal/e2e/realclaude sit behind the
// e2e_realclaude build tag and so never compile under make check; this JSON file
// carries no tag, which is what lets the pin run in the hermetic gate.
var capturePath = filepath.Join("..", "e2e", "realclaude", "testdata", "ask_user_question_v2.1.239.json")

// captureRecord is the capture's wrapper: the two fields this parse consumes.
// The version fields are deliberately not decoded — nothing here asserts on
// which claude produced the bytes.
type captureRecord struct {
	ToolName  string          `json:"tool_name"`
	ToolInput json.RawMessage `json:"tool_input"`
}

func readCapture(t *testing.T) captureRecord {
	t.Helper()
	b, err := os.ReadFile(capturePath)
	if err != nil {
		t.Fatalf("read capture %s: %v", capturePath, err)
	}
	var rec captureRecord
	if err := json.Unmarshal(b, &rec); err != nil {
		t.Fatalf("decode capture %s: %v", capturePath, err)
	}
	return rec
}

// twoOptions is the minimum in-contract option array, reused verbatim by every
// row whose subject is something OTHER than the option count, so those rows
// cannot trip the option bounds by accident. inBounds is the whole question
// built on it, for rows whose subject is the question count.
const (
	twoOptions = `[{"label":"A","description":"a"},{"label":"B","description":"b"}]`
	inBounds   = `{"question":"q","header":"h","options":` + twoOptions + `,"multiSelect":false}`
)

// batch wraps question objects into a tool input.
func batch(questions ...string) json.RawMessage {
	return json.RawMessage(`{"questions":[` + strings.Join(questions, ",") + `]}`)
}

// padded builds a well-formed single-question input whose first option's
// description is n bytes long. The over-cap reject row and the large-but-legal
// accept row differ ONLY in n, which is what makes the size branch sole-tripping
// rather than merely untested: at n under the cap the same shape parses.
func padded(n int) json.RawMessage {
	return batch(`{"question":"q","header":"h","options":[{"label":"A","description":"` +
		strings.Repeat("x", n) + `"},{"label":"B","description":"b"}],"multiSelect":false}`)
}

// opts renders n options with distinct labels, for the option-count rows.
func opts(n int) string {
	parts := make([]string, n)
	for i := range parts {
		parts[i] = `{"label":"L` + string(rune('A'+i)) + `","description":"d"}`
	}
	return `[` + strings.Join(parts, ",") + `]`
}

// wantOpts mirrors opts on the payload side.
func wantOpts(n int) []protocol.QuestionOption {
	out := make([]protocol.QuestionOption, n)
	for i := range out {
		out[i] = protocol.QuestionOption{Label: "L" + string(rune('A'+i)), Description: "d"}
	}
	return out
}

type parseCase struct {
	name  string
	tool  string
	input json.RawMessage
	want  protocol.QuestionShownPayload
	ok    bool
}

// parseCases is the single source of rows for both TestParse and
// TestParse_EmitsNoLog, so the no-log pin provably covers every branch the
// behaviour table covers rather than a hand-copied subset of it.
//
// Every reject row wants the ZERO payload: AC 2's "no payload at all rather
// than a partial or a truncated one" is asserted by the same reflect.DeepEqual
// that checks the accepted rows, and the zero payload's empty ConversationID
// and QuestionBatchID also pin that this parse never fills those two
// daemon-asserted ids from claude's tool input (#1927 mints them).
func parseCases(t *testing.T) []parseCase {
	t.Helper()
	rec := readCapture(t)
	twoWant := []protocol.QuestionOption{{Label: "A", Description: "a"}, {Label: "B", Description: "b"}}

	return []parseCase{
		{
			name:  "committed capture",
			tool:  rec.ToolName,
			input: rec.ToolInput,
			ok:    true,
			want: protocol.QuestionShownPayload{Questions: []protocol.Question{{
				Text:   "For the in-memory key-value cache, which write strategy should it use?",
				Header: "Write strategy", // 14 runes: past the vendor's documented 12, and accepted
				Options: []protocol.QuestionOption{
					{Label: "Write-through", Description: "Writes go to the cache and the backing store synchronously, together."},
					{Label: "Write-behind", Description: "Writes go to the cache immediately and are persisted to the backing store asynchronously."},
				},
				MultiSelect: false,
			}}},
		},
		{
			name: "batch order is claude's own",
			tool: ToolName,
			input: batch(
				`{"question":"first","header":"h1","options":`+twoOptions+`,"multiSelect":false}`,
				`{"question":"second","header":"h2","options":`+twoOptions+`,"multiSelect":true}`,
			),
			ok: true,
			want: protocol.QuestionShownPayload{Questions: []protocol.Question{
				{Text: "first", Header: "h1", Options: twoWant, MultiSelect: false},
				{Text: "second", Header: "h2", Options: twoWant, MultiSelect: true},
			}},
		},
		{
			name:  "multiSelect true",
			tool:  ToolName,
			input: batch(`{"question":"q","header":"h","options":` + twoOptions + `,"multiSelect":true}`),
			ok:    true,
			want: protocol.QuestionShownPayload{Questions: []protocol.Question{
				{Text: "q", Header: "h", Options: twoWant, MultiSelect: true},
			}},
		},
		{
			name:  "three options",
			tool:  ToolName,
			input: batch(`{"question":"q","header":"h","options":` + opts(3) + `,"multiSelect":false}`),
			ok:    true,
			want: protocol.QuestionShownPayload{Questions: []protocol.Question{
				{Text: "q", Header: "h", Options: wantOpts(3)},
			}},
		},
		{
			name:  "four options",
			tool:  ToolName,
			input: batch(`{"question":"q","header":"h","options":` + opts(4) + `,"multiSelect":false}`),
			ok:    true,
			want: protocol.QuestionShownPayload{Questions: []protocol.Question{
				{Text: "q", Header: "h", Options: wantOpts(4)},
			}},
		},
		{
			// Fail-closed means BOUNDS, not unknown keys: a claude release adding
			// a field (the docs describe a per-option preview under
			// previewFormat, which pyry never sets) must not drop every question.
			name: "unknown keys are tolerated",
			tool: ToolName,
			input: batch(`{"question":"q","header":"h","newKey":{"a":1},"multiSelect":false,` +
				`"options":[{"label":"A","description":"a","preview":"<b>x</b>"},{"label":"B","description":"b"}]}`),
			ok: true,
			want: protocol.QuestionShownPayload{Questions: []protocol.Question{
				{Text: "q", Header: "h", Options: twoWant},
			}},
		},
		{
			// The accept side of the size branch: same shape as "input over the
			// byte cap", differing only in the pad length.
			name:  "large but under the byte cap",
			tool:  ToolName,
			input: padded(maxInputBytes / 2),
			ok:    true,
			want: protocol.QuestionShownPayload{Questions: []protocol.Question{{
				Text: "q", Header: "h", Options: []protocol.QuestionOption{
					{Label: "A", Description: strings.Repeat("x", maxInputBytes/2)},
					{Label: "B", Description: "b"},
				},
			}}},
		},

		// Discriminant: not the question tool, so no batch — over input that is
		// otherwise well formed, so only the name decides.
		{name: "another tool name", tool: "Bash", input: rec.ToolInput},
		{name: "empty tool name", tool: "", input: rec.ToolInput},

		// The seven reject branches. Each fixture trips its own branch and no
		// other; the malformed-JSON row sits far under the byte cap, because the
		// cap is taken first and would otherwise be what rejects it.
		{name: "reject malformed json", tool: ToolName, input: json.RawMessage(`{"questions":[`)},
		{
			// The syntactically-malformed row above is caught twice over — swallow
			// the decode error and a zero toolInput still fails the question-count
			// bound — so it cannot show the decode guard is load-bearing. This row
			// can, and it is the same branch rather than an eighth: encoding/json
			// PARTIALLY POPULATES on a type error, so a wrong-typed question text
			// leaves a structurally valid batch whose text is silently empty, and
			// only the decode guard keeps that off the wire. Measured, not
			// predicted — the guard's mutant survives without this row.
			name:  "reject wrong-typed question text",
			tool:  ToolName,
			input: batch(`{"question":123,"header":"h","options":` + twoOptions + `,"multiSelect":false}`),
		},
		{name: "reject over the byte cap", tool: ToolName, input: padded(maxInputBytes)},
		{name: "reject zero questions", tool: ToolName, input: batch()},
		{
			name:  "reject more than four questions",
			tool:  ToolName,
			input: batch(inBounds, inBounds, inBounds, inBounds, inBounds),
		},
		{
			name:  "reject fewer than two options",
			tool:  ToolName,
			input: batch(`{"question":"q","header":"h","options":` + opts(1) + `,"multiSelect":false}`),
		},
		{
			name:  "reject more than four options",
			tool:  ToolName,
			input: batch(`{"question":"q","header":"h","options":` + opts(5) + `,"multiSelect":false}`),
		},
		{
			// Absent, not false: protocol.Question.MultiSelect is a plain bool
			// under a no-omitempty discipline, so letting Go's zero value stand
			// would put a position on the wire claude never stated.
			name:  "reject absent multiSelect",
			tool:  ToolName,
			input: batch(`{"question":"q","header":"h","options":` + twoOptions + `}`),
		},
	}
}

func TestParse(t *testing.T) {
	t.Parallel()
	for _, tc := range parseCases(t) {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, ok := Parse(tc.tool, tc.input)
			if ok != tc.ok {
				t.Fatalf("Parse ok = %v, want %v", ok, tc.ok)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Parse payload = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// TestParse_EmitsNoLog pins AC 4 over every branch of parseCases. The handler is
// configured at slog.LevelDebug ON PURPOSE: at the default level the buffer
// would stay empty without the assertion ever having observed a Debug line, so
// the pin would pass while proving nothing.
//
// slog.SetDefault also redirects the standard log package, so one buffer covers
// both routes. It mutates a process-wide global, which is why this test does not
// call t.Parallel.
func TestParse_EmitsNoLog(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))

	for _, tc := range parseCases(t) {
		Parse(tc.tool, tc.input)
	}

	if buf.Len() != 0 {
		t.Errorf("Parse wrote %d bytes to the default logger, want none: %s", buf.Len(), buf.String())
	}
}
