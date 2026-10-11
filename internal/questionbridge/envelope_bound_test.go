package questionbridge

import (
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/protocol"
)

// testEscapingQuestionInput keeps HTML-sensitive bytes literal in the tool input
// so encoding the mapped payload must account for their sixfold expansion.
func testEscapingQuestionInput(t *testing.T, char, field string, n int) (json.RawMessage, protocol.QuestionShownPayload) {
	t.Helper()
	questionCount, optionCount := 1, 2
	if field == "four by four" {
		questionCount, optionCount = 4, 4
	}
	want := protocol.QuestionShownPayload{Questions: make([]protocol.Question, questionCount)}
	var fields []*string
	for i := range want.Questions {
		q := &want.Questions[i]
		q.Text, q.Header, q.MultiSelect = fmt.Sprintf("question%d", i), fmt.Sprintf("header%d", i), i%2 == 1
		q.Options = make([]protocol.QuestionOption, optionCount)
		fields = append(fields, &q.Text, &q.Header)
		for j := range q.Options {
			o := &q.Options[j]
			o.Label, o.Description = fmt.Sprintf("label%d", j), fmt.Sprintf("description%d", j)
			fields = append(fields, &o.Label, &o.Description)
		}
	}
	if field != "four by four" {
		index := map[string]int{"question": 0, "header": 1, "label": 2, "description": 3}[field]
		fields = fields[index : index+1]
	}
	for i, f := range fields {
		count := n / len(fields)
		if i < n%len(fields) {
			count++
		}
		*f += strings.Repeat(char, count)
	}
	raw, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	input := strings.NewReplacer(`\u003c`, "<", `\u003e`, ">", `\u0026`, "&", `"multi_select":`, `"multiSelect":`).Replace(string(raw))
	return json.RawMessage(input), want
}

func testQuestionAcceptedBoundary(t *testing.T, char, field string) int {
	t.Helper()
	low, high := 0, maxInputBytes+1
	for low+1 < high {
		middle := (low + high) / 2
		input, _ := testEscapingQuestionInput(t, char, field, middle)
		if _, ok := Parse(ToolName, input); ok {
			low = middle
		} else {
			high = middle
		}
	}
	return low
}

func testQuestionFullEnvelope(t *testing.T, p protocol.QuestionShownPayload, char string) protocol.Envelope {
	t.Helper()
	p.ConversationID, p.QuestionBatchID = strings.Repeat(char, 36), strings.Repeat(char, 36)
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	sid, err := json.Marshal(strings.Repeat(char, 256))
	if err != nil {
		t.Fatal(err)
	}
	n := uint64(math.MaxUint64)
	return protocol.Envelope{
		ID: n, Type: protocol.TypeQuestionShown,
		TS:      time.Date(9999, 12, 31, 23, 59, 59, 999999999, time.FixedZone("", -86340)),
		Payload: raw, InReplyTo: &n, EventID: &n, HistoryEntryID: &n,
		SessionID: sid, PayloadEncrypted: true,
	}
}

func TestParseEnvelopeBound(t *testing.T) {
	t.Parallel()
	for _, char := range []string{"<", ">", "&"} {
		for _, field := range []string{"question", "header", "label", "description", "four by four"} {
			t.Run(char+"/"+field, func(t *testing.T) {
				t.Parallel()
				n := testQuestionAcceptedBoundary(t, char, field)
				input, want := testEscapingQuestionInput(t, char, field, n)
				got, ok := Parse(ToolName, input)
				if !ok || !reflect.DeepEqual(got, want) {
					t.Fatal("largest accepted batch changed content, order, selection or daemon IDs")
				}
				env := testQuestionFullEnvelope(t, got, char)
				for _, clear := range []bool{false, true} {
					if clear {
						env.Payload, env.SessionStateCleared = json.RawMessage(`{}`), true
					}
					raw, err := json.Marshal(env)
					if err != nil || len(raw) > protocol.MaxThreadEnvelopeBytes {
						t.Fatalf("clear=%v envelope=%d cap=%d error=%v", clear, len(raw), protocol.MaxThreadEnvelopeBytes, err)
					}
					t.Logf("literal bytes=%d raw input=%d clear=%v envelope=%d", n, len(input), clear, len(raw))
				}
				rejected, _ := testEscapingQuestionInput(t, char, field, n+1)
				if len(rejected) > maxInputBytes {
					t.Fatal("raw guard masked the encoded-envelope guard")
				}
				if p, ok := Parse(ToolName, rejected); ok || !reflect.DeepEqual(p, protocol.QuestionShownPayload{}) {
					t.Fatal("first oversized batch did not return zero payload and false")
				}
			})
		}
	}
}

func TestParseEnvelopeBoundaryRegistryIdentity(t *testing.T) {
	t.Parallel()
	n := testQuestionAcceptedBoundary(t, "<", "four by four")
	input, _ := testEscapingQuestionInput(t, "<", "four by four", n)
	batch, ok := Parse(ToolName, input)
	if !ok {
		t.Fatal("boundary input rejected")
	}
	r := New()
	stamped, err := r.Record(batch, strings.Repeat("c", 36))
	if err != nil || len(stamped.QuestionBatchID) != 36 {
		t.Fatalf("record identity: %v", err)
	}
	if !reflect.DeepEqual(stamped.Questions, batch.Questions) {
		t.Fatal("record changed boundary content")
	}
	for range 2 {
		found, ok := r.Lookup(stamped.QuestionBatchID)
		if !ok || !reflect.DeepEqual(found, stamped) || !reflect.DeepEqual(r.Snapshot(), []protocol.QuestionShownPayload{stamped}) {
			t.Fatal("lookup or snapshot changed original answer identity/content")
		}
	}
	resolved, ok := r.Resolve(stamped.QuestionBatchID)
	if !ok || !reflect.DeepEqual(resolved, stamped) {
		t.Fatal("resolution lost original answer identity/content")
	}
	if _, ok := r.Resolve(stamped.QuestionBatchID); ok || len(r.Snapshot()) != 0 {
		t.Fatal("resolved boundary batch remains answerable")
	}
}

func TestParseRawInputBoundary(t *testing.T) {
	t.Parallel()
	n := maxInputBytes - len(padded(0))
	if input := padded(n); len(input) != maxInputBytes {
		t.Fatal("fixture missed the raw boundary")
	} else if p, ok := Parse(ToolName, input); !ok || p.Questions[0].Options[0].Description != strings.Repeat("x", n) {
		t.Fatal("ordinary text at the raw limit was refused or changed")
	}
	if p, ok := Parse(ToolName, padded(n+1)); ok || !reflect.DeepEqual(p, protocol.QuestionShownPayload{}) {
		t.Fatal("raw limit no longer rejects whole input")
	}
}
