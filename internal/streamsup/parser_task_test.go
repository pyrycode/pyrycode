package streamsup

import (
	"encoding/json"
	"log/slog"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// taskStartedCapCheat records the cap values as LITERALS, deliberately not as
// maxTaskFieldID / maxTaskDescription / maxTaskPatch. Same rule as
// harnessNudgeFixture: a fixture built from the constant it validates asserts
// nothing about the number — halve the constant and every row below would follow
// it green. These literals are what make such an edit go RED.
//
// taskPatchCapFixture is a THIRD literal even though it currently equals
// taskDescriptionCapFixture, mirroring the production split: the two bound
// different fields for different reasons, and sharing a fixture would let a
// change to one silently retarget the other's proof. taskSummaryCapFixture is a
// FOURTH on the identical grounds (#2245).
//
// taskRosterEntriesCapFixture is the first fixture pinning a CARDINALITY rather
// than a byte length (#1381), and the same reasoning carries over unchanged: a
// count fixture written as maxTaskRosterEntries would follow the constant green
// if someone halved it, which is exactly the edit worth catching.
// taskProgressTasksCapFixture is the SECOND cardinality fixture (#2246), on the
// identical grounds and a separate literal for taskPatchCapFixture's reason — it
// currently equals the roster's 8 and the two bound different things, one a list
// inside an event and the other a map the parser retains.
//
// taskToolCallsPerEventFixture is the first fixture pinning a FREQUENCY (#2246),
// and it is the one this file needs most. minTaskToolCallsPerEvent is derived from
// a two-line capture — a ceiling of 2 from the observed task's whole advance, a
// floor of 2 because 1 is not a bound — so the value is pinned by an argument that
// only holds while the number does. A rate table written as the constant would
// follow it green in either direction, which is precisely the edit that would spend
// the argument without noticing.
const (
	taskFieldIDCapFixture           = 256
	taskDescriptionCapFixture       = 4096
	taskPatchCapFixture             = 4096
	taskRosterEntriesCapFixture     = 8
	taskRosterDescriptionCapFixture = 512
	taskSummaryCapFixture           = 4096
	taskProgressTasksCapFixture     = 8
	taskToolCallsPerEventFixture    = 2
)

// taskStartedLineFixture builds a system/task_started line from the four mapped
// keys. It invents NO field structure — the keys are exactly the capture's — and
// exists only to vary the VALUES, which is what the cap proof needs and what the
// capture cannot supply: every captured line is small (235 bytes for
// task_started, with a 9-byte description), so nothing in it approaches a cap.
func taskStartedLineFixture(t *testing.T, taskID, toolUseID, description, taskType string) string {
	t.Helper()
	b, err := json.Marshal(map[string]string{
		"type":        "system",
		"subtype":     "task_started",
		"task_id":     taskID,
		"tool_use_id": toolUseID,
		"description": description,
		"task_type":   taskType,
	})
	if err != nil {
		t.Fatalf("marshalling task_started fixture: %v", err)
	}
	return string(b)
}

// taskStartedEvent drives one line through the shipped parser and returns the
// single BackgroundTaskStarted it must emit.
func taskStartedEvent(t *testing.T, line string) turnevent.BackgroundTaskStarted {
	t.Helper()
	got := collectEvents(line)
	if len(got) != 1 {
		t.Fatalf("event count: got %d, want 1 (%#v)", len(got), got)
	}
	ev, ok := got[0].(turnevent.BackgroundTaskStarted)
	if !ok {
		t.Fatalf("event type: got %T, want turnevent.BackgroundTaskStarted", got[0])
	}
	return ev
}

// TestParser_TaskStartedMapsFromCapture is #1380's central assertion: the
// CAPTURED system/task_started line becomes one turnevent.BackgroundTaskStarted
// carrying the fields the capture shows.
//
// The field assertions are DERIVED from the capture's own payload rather than
// pinned to literals. The capture is redacted (session_id reads $SESSION_ID and
// description reads `cat $FIFO`), so a pinned expectation would be pinning the
// placeholder rather than a real value; a derived one still catches a field
// swap, which is the failure worth catching. task_type carries the one pinned
// literal, as a canary that the reader picked the right record at all.
func TestParser_TaskStartedMapsFromCapture(t *testing.T) {
	t.Parallel()
	line := capturedSystemLine(t, "task_started")

	var payload map[string]any
	if err := json.Unmarshal(line, &payload); err != nil {
		t.Fatalf("decoding the captured payload: %v", err)
	}

	ev := taskStartedEvent(t, string(line))

	routes := []struct {
		field     string
		claudeKey string
		got       string
	}{
		{"TaskID", "task_id", ev.TaskID},
		{"ToolCallID", "tool_use_id", ev.ToolCallID},
		{"Description", "description", ev.Description},
		{"TaskType", "task_type", ev.TaskType},
	}
	for _, r := range routes {
		want, _ := payload[r.claudeKey].(string)
		if want == "" {
			t.Fatalf("the capture carries no string %q, so the routing of %s cannot be proven against it", r.claudeKey, r.field)
		}
		if r.got != want {
			t.Errorf("%s: got %q, want the capture's %s = %q", r.field, r.got, r.claudeKey, want)
		}
	}

	// The canary: proves the reader selected the task_started record rather than
	// some other system line that happens to decode into the same shape.
	if ev.TaskType != "local_bash" {
		t.Errorf("TaskType: got %q, want %q (the capture's one observed value)", ev.TaskType, "local_bash")
	}
	if ev.TruncatedFields != nil {
		t.Errorf("TruncatedFields: got %v, want nil — the captured line is far under every cap", ev.TruncatedFields)
	}

	// The two deliberate drops (see BackgroundTaskStarted's doc): claude's
	// session_id is NOT the daemon's conversation identity, and uuid has no
	// reader in the daemon.
	//
	// Swept by REFLECTION over every string field on the event rather than over
	// the four routes above, so a field added later to this event is covered
	// without anyone remembering to extend a list.
	// That is what makes the drop an enforced property rather than a comment. Both
	// values are read from the decoded payload rather than pinned, so the
	// assertion stays honest if the capture is ever re-taken unredacted.
	rv := reflect.ValueOf(ev)
	var swept int
	for _, key := range []string{"session_id", "uuid"} {
		v, _ := payload[key].(string)
		if v == "" {
			t.Fatalf("the capture carries no string %q, so the drop assertion would be vacuous", key)
		}
		for i := 0; i < rv.NumField(); i++ {
			if rv.Field(i).Kind() != reflect.String {
				continue
			}
			swept++
			if rv.Field(i).String() == v {
				t.Errorf("field %s carries claude's %s (%q); it is deliberately NOT on this event",
					rv.Type().Field(i).Name, key, v)
			}
		}
	}
	// The sweep visiting nothing would pass silently, which is the one way this
	// assertion could rot into decoration.
	if swept < len(routes)*2 {
		t.Errorf("the drop sweep visited %d string fields, want at least %d", swept, len(routes)*2)
	}
}

// TestParser_TaskStartedFieldCaps pins the construction-time bound. It is
// applied before the event reaches the sink, so an oversized payload never
// enters the event stream, a queue, or a log — the same ordering
// maxUnrecognizedRaw's cap has, and the only thing that makes the bound real
// rather than cosmetic.
//
// The capture proves the mapping and cannot prove the bound: every captured line
// is far under any cap worth setting. So these lines are SYNTHESIZED, which
// invents no field structure — the keys are the capture's, only the values are
// oversized.
//
// Exact cut lengths are asserted on ASCII fixtures only. The scrub uses an empty
// replacement (truncateRaw's precedent), so a mid-rune cut DELETES the partial
// rune and a multi-byte result is legitimately 1-3 bytes short of the cap; that
// row asserts validity and the ceiling instead.
func TestParser_TaskStartedFieldCaps(t *testing.T) {
	t.Parallel()

	const (
		smallID   = "bybi8g8i8"
		smallTool = "toolu_01ENMNP5P4d3pqjTg9LgCxgZ"
		smallDesc = "cat /tmp/fifo"
		smallType = "local_bash"
	)
	overID := strings.Repeat("i", taskFieldIDCapFixture+1)
	overTool := strings.Repeat("u", taskFieldIDCapFixture*2)
	overDesc := strings.Repeat("d", taskDescriptionCapFixture+1)
	overType := strings.Repeat("k", taskFieldIDCapFixture+64)

	tests := []struct {
		name                                     string
		taskID, toolUseID, description, taskType string
		wantCut                                  []string
		wantLenTaskID                            int
		wantLenToolCallID                        int
		wantLenDescription                       int
		wantLenTaskType                          int
	}{
		{
			name: "description over cap is cut and reported",
			// The report names the field that was cut, and the other three are both
			// intact and unnamed — a cut must not smear across the event.
			taskID: smallID, toolUseID: smallTool, description: overDesc, taskType: smallType,
			wantCut:            []string{"description"},
			wantLenTaskID:      len(smallID),
			wantLenToolCallID:  len(smallTool),
			wantLenDescription: taskDescriptionCapFixture,
			wantLenTaskType:    len(smallType),
		},
		{
			name:   "task_id over cap is cut and reported",
			taskID: overID, toolUseID: smallTool, description: smallDesc, taskType: smallType,
			wantCut:            []string{"task_id"},
			wantLenTaskID:      taskFieldIDCapFixture,
			wantLenToolCallID:  len(smallTool),
			wantLenDescription: len(smallDesc),
			wantLenTaskType:    len(smallType),
		},
		{
			// The row that pins the TRANSLATION: claude's key is tool_use_id, and the
			// report names the daemon's field, tool_call_id.
			name:   "tool_use_id over cap is reported as tool_call_id",
			taskID: smallID, toolUseID: overTool, description: smallDesc, taskType: smallType,
			wantCut:            []string{"tool_call_id"},
			wantLenTaskID:      len(smallID),
			wantLenToolCallID:  taskFieldIDCapFixture,
			wantLenDescription: len(smallDesc),
			wantLenTaskType:    len(smallType),
		},
		{
			name:   "task_type over cap is cut and reported",
			taskID: smallID, toolUseID: smallTool, description: smallDesc, taskType: overType,
			wantCut:            []string{"task_type"},
			wantLenTaskID:      len(smallID),
			wantLenToolCallID:  len(smallTool),
			wantLenDescription: len(smallDesc),
			wantLenTaskType:    taskFieldIDCapFixture,
		},
		{
			// Two at once: both named, in DECLARATION order (task_id before
			// description), which is what makes the value deterministic and pinnable.
			name:   "two fields over cap are both named in declaration order",
			taskID: overID, toolUseID: smallTool, description: overDesc, taskType: smallType,
			wantCut:            []string{"task_id", "description"},
			wantLenTaskID:      taskFieldIDCapFixture,
			wantLenToolCallID:  len(smallTool),
			wantLenDescription: taskDescriptionCapFixture,
			wantLenTaskType:    len(smallType),
		},
		{
			// The <= boundary: a field of exactly its cap is not truncated, and
			// nothing is reported.
			name:               "every field exactly at its cap is not truncated",
			taskID:             strings.Repeat("i", taskFieldIDCapFixture),
			toolUseID:          strings.Repeat("u", taskFieldIDCapFixture),
			description:        strings.Repeat("d", taskDescriptionCapFixture),
			taskType:           strings.Repeat("k", taskFieldIDCapFixture),
			wantCut:            nil,
			wantLenTaskID:      taskFieldIDCapFixture,
			wantLenToolCallID:  taskFieldIDCapFixture,
			wantLenDescription: taskDescriptionCapFixture,
			wantLenTaskType:    taskFieldIDCapFixture,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			line := taskStartedLineFixture(t, tc.taskID, tc.toolUseID, tc.description, tc.taskType)
			ev := taskStartedEvent(t, line)

			if !reflect.DeepEqual(ev.TruncatedFields, tc.wantCut) {
				t.Errorf("TruncatedFields: got %#v, want %#v", ev.TruncatedFields, tc.wantCut)
			}
			lens := []struct {
				field string
				got   int
				want  int
			}{
				{"TaskID", len(ev.TaskID), tc.wantLenTaskID},
				{"ToolCallID", len(ev.ToolCallID), tc.wantLenToolCallID},
				{"Description", len(ev.Description), tc.wantLenDescription},
				{"TaskType", len(ev.TaskType), tc.wantLenTaskType},
			}
			for _, l := range lens {
				if l.got != l.want {
					t.Errorf("len(%s): got %d, want %d", l.field, l.got, l.want)
				}
			}
		})
	}

	t.Run("a cut mid-rune yields valid UTF-8", func(t *testing.T) {
		t.Parallel()
		// The cap is a BYTE slice, so it can land inside a multi-byte rune. The
		// value rides a JSON string field downstream, where an invalid sequence
		// would be silently replaced, so the producer scrubs it here instead.
		pad := strings.Repeat("d", taskDescriptionCapFixture-1)
		desc := pad + strings.Repeat("€", 8)
		line := taskStartedLineFixture(t, smallID, smallTool, desc, smallType)
		ev := taskStartedEvent(t, line)

		if !reflect.DeepEqual(ev.TruncatedFields, []string{"description"}) {
			t.Fatalf("TruncatedFields: got %#v, want [description]", ev.TruncatedFields)
		}
		if !utf8.ValidString(ev.Description) {
			t.Errorf("Description is not valid UTF-8 after a mid-rune cut")
		}
		// Short of the cap, not at it: the empty replacement DELETES the partial
		// rune rather than replacing it.
		if len(ev.Description) > taskDescriptionCapFixture {
			t.Errorf("len(Description): got %d, want <= %d", len(ev.Description), taskDescriptionCapFixture)
		}
	})
}

// TestParser_TaskStartedDropIsLoggedContentFree is the package's standing rule
// applied to the new subtype: nothing derived from claude's output reaches a
// log. The content crosses the wire, not the log — emitUnrecognized's precedent,
// which records site, type and byte count only.
//
// The sweep over EVERY captured record is the load-bearing part. It catches the
// realistic way this rule gets broken later: someone appending "description",
// d.Description to a Debug line, on either the success path or the
// decode-failure path. Both are driven here, because the malformed line is the
// one whose handler is most tempted to explain itself.
func TestParser_TaskStartedDropIsLoggedContentFree(t *testing.T) {
	t.Parallel()

	captured := capturedSystemLine(t, "task_started")
	var payload map[string]any
	if err := json.Unmarshal(captured, &payload); err != nil {
		t.Fatalf("decoding the captured payload: %v", err)
	}
	capturedDesc, _ := payload["description"].(string)
	if capturedDesc == "" {
		t.Fatalf("the capture carries no description, so the leak sweep would be vacuous")
	}

	// A task_started line the new shape cannot decode (task_id is a number), so
	// the decode-failure path runs. Its description is a distinctive literal, and
	// the whole point is that it must appear in no log record.
	const malformedDesc = "rm -rf /tmp/never-log-this-command"
	malformed := `{"type":"system","subtype":"task_started","task_id":42,` +
		`"tool_use_id":"toolu_x","description":"` + malformedDesc + `","task_type":"local_bash"}`

	rec := &logRecorder{}
	var events []turnevent.Event
	p := NewParser(func(ev turnevent.Event) { events = append(events, ev) }, slog.New(rec))
	if _, err := p.Write(append(append([]byte(nil), captured...), '\n')); err != nil {
		t.Fatalf("Write err = %v, want nil", err)
	}
	if _, err := p.Write([]byte(malformed + "\n")); err != nil {
		t.Fatalf("Write err = %v, want nil", err)
	}

	// The malformed line is dropped, and NOT as an Unrecognized: keeping `system`
	// whole on ignoredLineTypes is what makes "no system line reaches the
	// unrecognized lane" structural, and that guarantee outranks surfacing a
	// malformed line of a subtype we already know.
	if len(events) != 1 {
		t.Fatalf("event count: got %d, want 1 (the captured line only) — %#v", len(events), events)
	}
	if _, ok := events[0].(turnevent.BackgroundTaskStarted); !ok {
		t.Fatalf("event type: got %T, want turnevent.BackgroundTaskStarted", events[0])
	}

	for _, r := range rec.all() {
		for _, leak := range []string{capturedDesc, malformedDesc} {
			if strings.Contains(r.msg, leak) {
				t.Errorf("record message carries a claude-derived description: %q", r.msg)
			}
			for k, v := range r.attrs {
				if strings.Contains(v, leak) {
					t.Errorf("record %q attr %q carries a claude-derived description; this path logs the subtype only", r.msg, k)
				}
			}
		}
	}
}

// taskUpdatedLineFixture builds a system/task_updated line from the two mapped
// keys, taking the patch as RAW JSON so a test can hand it a shape or a size the
// capture does not contain. A nil patch omits the key entirely.
//
// It invents NO field structure: the LINE-level keys are exactly the capture's.
// It claims nothing about the patch's INTERNAL keys either, and that is the
// point rather than a loophole — the mapping carries the patch unparsed, so a
// synthesized multi-key patch asserts the opposite of an invented mapping: that
// keys the daemon knows nothing about survive.
func taskUpdatedLineFixture(t *testing.T, taskID string, patch json.RawMessage) string {
	t.Helper()
	line := map[string]any{
		"type":    "system",
		"subtype": "task_updated",
		"task_id": taskID,
	}
	if patch != nil {
		line["patch"] = patch
	}
	b, err := json.Marshal(line)
	if err != nil {
		t.Fatalf("marshalling task_updated fixture: %v", err)
	}
	return string(b)
}

// taskUpdatedEvent drives one line through the shipped parser and returns the
// single BackgroundTaskUpdated it must emit.
func taskUpdatedEvent(t *testing.T, line string) turnevent.BackgroundTaskUpdated {
	t.Helper()
	got := collectEvents(line)
	if len(got) != 1 {
		t.Fatalf("event count: got %d, want 1 (%#v)", len(got), got)
	}
	ev, ok := got[0].(turnevent.BackgroundTaskUpdated)
	if !ok {
		t.Fatalf("event type: got %T, want turnevent.BackgroundTaskUpdated", got[0])
	}
	return ev
}

// TestParser_TaskUpdatedMapsFromCapture is #1382's central assertion: the
// CAPTURED system/task_updated line becomes one turnevent.BackgroundTaskUpdated
// carrying task_id and patch.
//
// This line used to be a row in TestParser_IgnoredLineTypesStaySilent, asserting
// the opposite. Moving it here is the change; #1380 put it there deliberately
// because the subtype was genuinely still dropped between the two tickets.
//
// TaskID is DERIVED from the capture's own payload rather than pinned, for
// TestParser_TaskStartedMapsFromCapture's reason: the capture is redacted
// (session_id reads $SESSION_ID), so a pinned expectation risks pinning a
// placeholder, while a derived one still catches a field swap. Patch carries the
// one pinned literal, as a canary that the reader picked the right record at all.
func TestParser_TaskUpdatedMapsFromCapture(t *testing.T) {
	t.Parallel()
	line := capturedSystemLine(t, "task_updated")

	var payload map[string]any
	if err := json.Unmarshal(line, &payload); err != nil {
		t.Fatalf("decoding the captured payload: %v", err)
	}

	ev := taskUpdatedEvent(t, string(line))

	routes := []struct {
		field     string
		claudeKey string
		got       string
	}{
		{"TaskID", "task_id", ev.TaskID},
	}
	for _, r := range routes {
		want, _ := payload[r.claudeKey].(string)
		if want == "" {
			t.Fatalf("the capture carries no string %q, so the routing of %s cannot be proven against it", r.claudeKey, r.field)
		}
		if r.got != want {
			t.Errorf("%s: got %q, want the capture's %s = %q", r.field, r.got, r.claudeKey, want)
		}
	}

	// Patch is asserted SEMANTICALLY — re-decoded and compared against the
	// payload's own patch — so the carriage claim does not rest on key ordering,
	// which is not ours to control.
	wantPatch, ok := payload["patch"]
	if !ok {
		t.Fatalf("the capture carries no patch, so the carriage assertion would be vacuous")
	}
	var gotPatch any
	if err := json.Unmarshal([]byte(ev.Patch), &gotPatch); err != nil {
		t.Fatalf("Patch %q does not re-decode as JSON: %v", ev.Patch, err)
	}
	if !reflect.DeepEqual(gotPatch, wantPatch) {
		t.Errorf("Patch: got %#v, want the capture's patch %#v", gotPatch, wantPatch)
	}

	// The canary: proves the reader selected the task_updated record rather than
	// some other system line that happens to decode into the same two-field shape.
	const wantPatchLiteral = `{"is_backgrounded":true}`
	if ev.Patch != wantPatchLiteral {
		t.Errorf("Patch: got %q, want %q (the capture's one observed patch)", ev.Patch, wantPatchLiteral)
	}
	if ev.TruncatedFields != nil {
		t.Errorf("TruncatedFields: got %v, want nil — the captured line is 170 bytes on the wire, far under every cap", ev.TruncatedFields)
	}

	// The two deliberate drops (see BackgroundTaskUpdated's doc): claude's
	// session_id is NOT the daemon's conversation identity, and uuid has no reader
	// in the daemon. Neither is even declared on systemTaskUpdatedLine, so this
	// sweep guards the event against a LATER field, which is the failure a fixed
	// list of field names stops catching the moment someone adds one.
	//
	// Swept by REFLECTION over every string field, inherited from
	// TestParser_TaskStartedMapsFromCapture. Both values are read from the decoded
	// payload rather than pinned, so the assertion stays honest if the capture is
	// ever re-taken unredacted.
	rv := reflect.ValueOf(ev)
	var swept int
	for _, key := range []string{"session_id", "uuid"} {
		v, _ := payload[key].(string)
		if v == "" {
			t.Fatalf("the capture carries no string %q, so the drop assertion would be vacuous", key)
		}
		for i := 0; i < rv.NumField(); i++ {
			if rv.Field(i).Kind() != reflect.String {
				continue
			}
			swept++
			if rv.Field(i).String() == v {
				t.Errorf("field %s carries claude's %s (%q); it is deliberately NOT on this event",
					rv.Type().Field(i).Name, key, v)
			}
		}
	}
	// The sweep visiting nothing would pass silently, which is the one way this
	// assertion could rot into decoration.
	//
	// The floor is a LITERAL, not #1380's len(routes)*2. That expression happened
	// to be exact there because BackgroundTaskStarted has as many routes as string
	// fields (4 and 4); here there is one route and two string fields, so
	// len(routes)*2 would be a floor of 2 against an actual 4 — it would pass with
	// half the sweep missing. 2 string fields x 2 dropped keys = 4. A later ticket
	// adding a field only raises the count; a field LEAVING string kind drops it
	// below the floor and goes red, which is exactly the weakening worth catching.
	const wantSwept = 2 * 2
	if swept < wantSwept {
		t.Errorf("the drop sweep visited %d string fields, want at least %d", swept, wantSwept)
	}
}

// TestParser_TaskUpdatedCarriesPatchWhole proves the clause the capture cannot:
// that patch is carried WHOLE rather than key-enumerated.
//
// The captured patch has exactly one key, so an implementation that declared
// `struct{ IsBackgrounded bool }` and re-marshalled would satisfy every
// capture-derived assertion above. Whole-ness needs a second input — the same
// shape as the ticket's "the capture proves the mapping and cannot prove the
// bound" argument, one level down. These lines are SYNTHESIZED and invent no
// field structure: the line-level keys are the capture's, and the mapping claims
// nothing about the patch's internal keys, which is precisely what is being
// asserted here.
func TestParser_TaskUpdatedCarriesPatchWhole(t *testing.T) {
	t.Parallel()

	const taskID = "bybi8g8i8"

	t.Run("keys claude has never shown survive un-mapped", func(t *testing.T) {
		t.Parallel()
		// A string, a nested object and an array beside the one observed key. A
		// key-enumerating mapping drops all three and goes red here.
		raw := json.RawMessage(`{"is_backgrounded":true,"status":"running",` +
			`"detail":{"exit_code":0,"note":"still going"},"tags":["a","b"]}`)
		ev := taskUpdatedEvent(t, taskUpdatedLineFixture(t, taskID, raw))

		var got, want any
		if err := json.Unmarshal([]byte(ev.Patch), &got); err != nil {
			t.Fatalf("Patch %q does not re-decode as JSON: %v", ev.Patch, err)
		}
		if err := json.Unmarshal(raw, &want); err != nil {
			t.Fatalf("decoding the fixture patch: %v", err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("Patch: got %#v, want %#v — every key must survive, including the ones the daemon knows nothing about", got, want)
		}
		if ev.TruncatedFields != nil {
			t.Errorf("TruncatedFields: got %v, want nil", ev.TruncatedFields)
		}
	})

	t.Run("a large integer keeps its digits verbatim", func(t *testing.T) {
		t.Parallel()
		// The row that falsifies a map[string]any decode-and-re-marshal: that
		// implementation rounds every number through float64 and this id loses its
		// low digits. Byte-level, deliberately not DeepEqual, which would compare two
		// equally-rounded float64s and pass.
		const big = "12345678901234567890"
		raw := json.RawMessage(`{"revision":` + big + `}`)
		ev := taskUpdatedEvent(t, taskUpdatedLineFixture(t, taskID, raw))

		if !strings.Contains(ev.Patch, big) {
			t.Errorf("Patch: got %q, want it to carry %s verbatim", ev.Patch, big)
		}
	})

	t.Run("an absent patch lands empty and is not an error", func(t *testing.T) {
		t.Parallel()
		// Absence is claude's to choose; there is no captured negative case, so the
		// field lands empty rather than inventing a validation rule.
		ev := taskUpdatedEvent(t, taskUpdatedLineFixture(t, taskID, nil))

		if ev.Patch != "" {
			t.Errorf("Patch: got %q, want the empty string", ev.Patch)
		}
		if ev.TaskID != taskID {
			t.Errorf("TaskID: got %q, want %q — an absent patch must not disturb the rest", ev.TaskID, taskID)
		}
		if ev.TruncatedFields != nil {
			t.Errorf("TruncatedFields: got %v, want nil", ev.TruncatedFields)
		}
	})

	t.Run("a non-object patch is carried as its serialized text", func(t *testing.T) {
		t.Parallel()
		// Deliberate permissiveness: json.RawMessage takes any valid JSON value, and
		// the cap is what makes that safe rather than a shape check. One observation
		// of an object does not earn a validation rule.
		raw := json.RawMessage(`"backgrounded"`)
		ev := taskUpdatedEvent(t, taskUpdatedLineFixture(t, taskID, raw))

		if ev.Patch != string(raw) {
			t.Errorf("Patch: got %q, want %q", ev.Patch, string(raw))
		}
	})
}

// TestParser_TaskUpdatedFieldCaps pins the construction-time bound, mirroring
// TestParser_TaskStartedFieldCaps. It is applied before the event reaches the
// sink, so an oversized payload never enters the event stream, a queue, or a log.
//
// The capture proves the mapping and cannot prove the bound: the captured line
// is 170 bytes on the wire with a 24-byte patch, against caps of 256 and 4096.
// So these lines are SYNTHESIZED, which invents no field structure — the keys are
// the capture's, only the values are oversized.
func TestParser_TaskUpdatedFieldCaps(t *testing.T) {
	t.Parallel()

	const smallID = "bybi8g8i8"
	// A one-key patch object, split so the byte arithmetic below is visible: the
	// cut has to land INSIDE the string value for the typing proof to mean
	// anything.
	const patchOpen = `{"note":"`
	const patchClose = `"}`
	patchOf := func(value string) json.RawMessage {
		return json.RawMessage(patchOpen + value + patchClose)
	}
	const patchOverhead = len(patchOpen) + len(patchClose)

	smallPatch := patchOf("ok")
	// Comfortably past the cap, and long enough that the cut at taskPatchCapFixture
	// lands inside the string value rather than in the trailing `"}`.
	overPatch := patchOf(strings.Repeat("p", taskPatchCapFixture))
	atPatch := patchOf(strings.Repeat("p", taskPatchCapFixture-patchOverhead))
	overID := strings.Repeat("i", taskFieldIDCapFixture+1)

	tests := []struct {
		name          string
		taskID        string
		patch         json.RawMessage
		wantCut       []string
		wantLenTaskID int
		wantLenPatch  int
		// wantPatchValidJSON is the typing proof, asserted in BOTH directions: an
		// intact patch is valid JSON, and a CUT one is not — which is why Patch is a
		// string and not json.RawMessage. Typing a truncated object as raw JSON
		// would be a lie (turnevent.Unrecognized.Raw's precedent).
		wantPatchValidJSON bool
	}{
		{
			name: "patch over cap is cut and reported",
			// The report names the field that was cut, and task_id is both intact and
			// unnamed — a cut must not smear across the event.
			taskID: smallID, patch: overPatch,
			wantCut:            []string{"patch"},
			wantLenTaskID:      len(smallID),
			wantLenPatch:       taskPatchCapFixture,
			wantPatchValidJSON: false,
		},
		{
			name:   "task_id over cap is cut and reported",
			taskID: overID, patch: smallPatch,
			wantCut:            []string{"task_id"},
			wantLenTaskID:      taskFieldIDCapFixture,
			wantLenPatch:       len(smallPatch),
			wantPatchValidJSON: true,
		},
		{
			// Two at once: both named, in DECLARATION order (task_id before patch),
			// which is what makes the value deterministic and pinnable.
			name:   "two fields over cap are both named in declaration order",
			taskID: overID, patch: overPatch,
			wantCut:            []string{"task_id", "patch"},
			wantLenTaskID:      taskFieldIDCapFixture,
			wantLenPatch:       taskPatchCapFixture,
			wantPatchValidJSON: false,
		},
		{
			// The <= boundary: a field of exactly its cap is not truncated, and
			// nothing is reported.
			name:   "every field exactly at its cap is not truncated",
			taskID: strings.Repeat("i", taskFieldIDCapFixture), patch: atPatch,
			wantCut:            nil,
			wantLenTaskID:      taskFieldIDCapFixture,
			wantLenPatch:       taskPatchCapFixture,
			wantPatchValidJSON: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ev := taskUpdatedEvent(t, taskUpdatedLineFixture(t, tc.taskID, tc.patch))

			if !reflect.DeepEqual(ev.TruncatedFields, tc.wantCut) {
				t.Errorf("TruncatedFields: got %#v, want %#v", ev.TruncatedFields, tc.wantCut)
			}
			if len(ev.TaskID) != tc.wantLenTaskID {
				t.Errorf("len(TaskID): got %d, want %d", len(ev.TaskID), tc.wantLenTaskID)
			}
			if len(ev.Patch) != tc.wantLenPatch {
				t.Errorf("len(Patch): got %d, want %d", len(ev.Patch), tc.wantLenPatch)
			}
			if got := json.Valid([]byte(ev.Patch)); got != tc.wantPatchValidJSON {
				t.Errorf("json.Valid(Patch): got %v, want %v — a truncated patch is a STRING that is no longer valid JSON, which is why the field is not typed json.RawMessage",
					got, tc.wantPatchValidJSON)
			}
		})
	}

	t.Run("a cut mid-rune yields valid UTF-8", func(t *testing.T) {
		t.Parallel()
		// The cap is a BYTE slice, so it can land inside a multi-byte rune. The value
		// rides a JSON string field downstream, where an invalid sequence would be
		// silently replaced, so the producer scrubs it here instead. The padding puts
		// the first multi-byte rune so its bytes straddle the cut.
		pad := strings.Repeat("d", taskPatchCapFixture-len(patchOpen)-1)
		ev := taskUpdatedEvent(t, taskUpdatedLineFixture(t, smallID, patchOf(pad+strings.Repeat("€", 8))))

		if !reflect.DeepEqual(ev.TruncatedFields, []string{"patch"}) {
			t.Fatalf("TruncatedFields: got %#v, want [patch]", ev.TruncatedFields)
		}
		if !utf8.ValidString(ev.Patch) {
			t.Errorf("Patch is not valid UTF-8 after a mid-rune cut")
		}
		// Short of the cap, not at it: the empty replacement DELETES the partial rune
		// rather than replacing it.
		if len(ev.Patch) > taskPatchCapFixture {
			t.Errorf("len(Patch): got %d, want <= %d", len(ev.Patch), taskPatchCapFixture)
		}
	})
}

// TestParser_TaskUpdatedDropIsLoggedContentFree is the package's standing rule
// applied to the second subtype: nothing derived from claude's output reaches a
// log. The content crosses the wire, not the log.
//
// The sweep over EVERY captured record is the load-bearing part, and patch is
// the most attractive thing to log while debugging a malformed line — which is
// why both the success and the decode-failure path are driven here.
func TestParser_TaskUpdatedDropIsLoggedContentFree(t *testing.T) {
	t.Parallel()

	captured := capturedSystemLine(t, "task_updated")
	var payload map[string]any
	if err := json.Unmarshal(captured, &payload); err != nil {
		t.Fatalf("decoding the captured payload: %v", err)
	}
	capturedSession, _ := payload["session_id"].(string)
	if capturedSession == "" {
		t.Fatalf("the capture carries no session_id, so the leak sweep would be vacuous")
	}

	// A task_updated line the shape cannot decode (task_id is a number), so the
	// decode-failure path runs. Its patch carries a distinctive literal, and the
	// whole point is that it must appear in no log record.
	const malformedPatchValue = "rm -rf /tmp/never-log-this-patch"
	malformed := `{"type":"system","subtype":"task_updated","task_id":42,` +
		`"patch":{"command":"` + malformedPatchValue + `"}}`

	rec := &logRecorder{}
	var events []turnevent.Event
	p := NewParser(func(ev turnevent.Event) { events = append(events, ev) }, slog.New(rec))
	if _, err := p.Write(append(append([]byte(nil), captured...), '\n')); err != nil {
		t.Fatalf("Write err = %v, want nil", err)
	}
	if _, err := p.Write([]byte(malformed + "\n")); err != nil {
		t.Fatalf("Write err = %v, want nil", err)
	}

	// The malformed line is dropped, and NOT as an Unrecognized: keeping `system`
	// whole on ignoredLineTypes is what makes "no system line reaches the
	// unrecognized lane" structural, and that guarantee outranks surfacing a
	// malformed line of a subtype we already know.
	if len(events) != 1 {
		t.Fatalf("event count: got %d, want 1 (the captured line only) — %#v", len(events), events)
	}
	ev, ok := events[0].(turnevent.BackgroundTaskUpdated)
	if !ok {
		t.Fatalf("event type: got %T, want turnevent.BackgroundTaskUpdated", events[0])
	}
	// The captured patch as the parser itself carried it, so the leak candidate is
	// derived from the shipped mapping rather than transcribed.
	if ev.Patch == "" {
		t.Fatalf("the mapped Patch is empty, so the leak sweep over it would be vacuous")
	}

	// The decode-failure path must actually have run, or the half of this sweep
	// that covers it asserts nothing.
	const dropMsg = "streamsup: dropping undecodable system line"
	var sawDrop bool
	for _, r := range rec.all() {
		if r.msg != dropMsg {
			continue
		}
		sawDrop = true
		if !reflect.DeepEqual(r.attrs, map[string]string{"subtype": "task_updated"}) {
			t.Errorf("the undecodable-drop record carries %#v, want the subtype keyword only", r.attrs)
		}
	}
	if !sawDrop {
		t.Fatalf("no %q record: the decode-failure path never ran, so its leak sweep is vacuous", dropMsg)
	}

	for _, r := range rec.all() {
		for _, leak := range []string{ev.Patch, malformedPatchValue, capturedSession} {
			if strings.Contains(r.msg, leak) {
				t.Errorf("record message carries claude-derived content: %q", r.msg)
			}
			for k, v := range r.attrs {
				if strings.Contains(v, leak) {
					t.Errorf("record %q attr %q carries claude-derived content; this path logs the subtype only", r.msg, k)
				}
			}
		}
	}
}

// taskNotificationEvent drives one line through the shipped parser and returns
// the single BackgroundTaskUpdated it must emit. Peer of taskUpdatedEvent: the
// two subtypes produce the SAME event, which is the whole shape of #2245.
func taskNotificationEvent(t *testing.T, line string) turnevent.BackgroundTaskUpdated {
	t.Helper()
	got := collectEvents(line)
	if len(got) != 1 {
		t.Fatalf("event count: got %d, want 1 (%#v)", len(got), got)
	}
	ev, ok := got[0].(turnevent.BackgroundTaskUpdated)
	if !ok {
		t.Fatalf("event type: got %T, want turnevent.BackgroundTaskUpdated", got[0])
	}
	return ev
}

// TestParser_TaskNotificationMapsFromCapture is #2245's central assertion: the
// CAPTURED system/task_notification line becomes one turnevent.BackgroundTaskUpdated
// carrying the task id and the terminal state claude reported.
//
// This line used to be a row in TestParser_IgnoredLineTypesStaySilent asserting
// the opposite, exactly as task_updated's did before #1382. That row was not an
// oversight: the subtype was genuinely dropped until #2247 committed a payload to
// declare from, and a synthesized row was the only honest option until then.
//
// The field assertions are DERIVED from the capture's own payload rather than
// pinned, for TestParser_TaskUpdatedMapsFromCapture's reason: the capture is
// redacted, so a pinned expectation risks pinning a placeholder while a derived
// one still catches a field swap. status carries the one pinned literal, as a
// canary that the reader picked the right record at all.
func TestParser_TaskNotificationMapsFromCapture(t *testing.T) {
	t.Parallel()
	line := capturedTaskNotificationLine(t)

	var payload map[string]any
	if err := json.Unmarshal(line, &payload); err != nil {
		t.Fatalf("decoding the captured payload: %v", err)
	}

	ev := taskNotificationEvent(t, string(line))

	routes := []struct {
		field     string
		claudeKey string
		got       string
	}{
		{"TaskID", "task_id", ev.TaskID},
		{"Status", "status", ev.Status},
		{"Summary", "summary", ev.Summary},
	}
	for _, r := range routes {
		want, _ := payload[r.claudeKey].(string)
		if want == "" {
			t.Fatalf("the capture carries no string %q, so the routing of %s cannot be proven against it", r.claudeKey, r.field)
		}
		if r.got != want {
			t.Errorf("%s: got %q, want the capture's %s = %q", r.field, r.got, r.claudeKey, want)
		}
	}

	// The canary: proves the reader selected the task_notification record rather
	// than some other system line that happens to decode into the same shape. It
	// is also the only value here worth pinning — the record's limitations state
	// that `failed` and `stopped` were never staged, so this is the one terminal
	// token this repo has ever seen.
	if ev.Status != "completed" {
		t.Errorf("Status: got %q, want %q (the capture's one observed terminal state)", ev.Status, "completed")
	}
	// AC3, at the point it is decided: the line carries no `patch` key, and the
	// daemon synthesizes none from the terminal state. A Patch assembled here
	// would falsify both turnevent.BackgroundTaskUpdated.Patch's contract and
	// protocol.BackgroundTaskUpdatedPayload's, which promise a consumer that the
	// field holds claude's own bytes and nothing else.
	if _, ok := payload["patch"]; ok {
		t.Fatalf("the captured line carries a patch key, so the no-synthesis assertion below would not be proving what it claims")
	}
	if ev.Patch != "" {
		t.Errorf("Patch: got %q, want empty — this subtype's line carries no patch and the daemon synthesizes none", ev.Patch)
	}
	if ev.TruncatedFields != nil {
		t.Errorf("TruncatedFields: got %v, want nil — the captured line is 249 bytes on the wire, far under every cap", ev.TruncatedFields)
	}
}

// TestParser_TaskNotificationDropsClaudesUnmappedKeys is AC4 plus the family's
// standing omissions, and it asserts them at TWO levels because one level alone
// would be weaker than it looks.
//
// The STRUCTURAL half is the one AC4 actually needs. output_file's captured value
// is the EMPTY STRING, so sweeping the emitted event for it would match every
// unset field and prove nothing; worse, it would pass identically if the field
// were declared and carried. So the assertion is made where the guarantee lives:
// systemTaskNotificationLine declares no field whose json tag is output_file, and
// therefore no code path in the daemon ever holds a value for it. A field that is
// never declared cannot leak.
//
// The VALUE half covers the three keys whose captured values are non-empty, swept
// by reflection over every string field of the event so a field added later is
// covered without anyone remembering to extend a list.
func TestParser_TaskNotificationDropsClaudesUnmappedKeys(t *testing.T) {
	t.Parallel()
	line := capturedTaskNotificationLine(t)

	var payload map[string]any
	if err := json.Unmarshal(line, &payload); err != nil {
		t.Fatalf("decoding the captured payload: %v", err)
	}

	// Structural: the decode target's declared json tags, against the keys the
	// capture proves claude sends.
	declared := map[string]bool{}
	lt := reflect.TypeOf(systemTaskNotificationLine{})
	for i := 0; i < lt.NumField(); i++ {
		tag, _, _ := strings.Cut(lt.Field(i).Tag.Get("json"), ",")
		declared[tag] = true
	}
	for _, key := range []string{"output_file", "tool_use_id", "uuid", "session_id"} {
		if _, ok := payload[key]; !ok {
			t.Fatalf("the capture carries no %q key, so asserting that the decode target omits it would be vacuous", key)
		}
		if declared[key] {
			t.Errorf("systemTaskNotificationLine declares a field tagged %q; it is deliberately NOT decoded — "+
				"output_file is a path on the operator's host, tool_use_id has no reader, and uuid and "+
				"session_id are the family's standing omissions", key)
		}
	}
	if len(declared) != 3 {
		t.Errorf("systemTaskNotificationLine declares %d json fields, want 3 (task_id, status, summary) — "+
			"the capture pins nine top-level keys and this target's whole discipline is that it declares "+
			"three of them", len(declared))
	}

	// Value: the three dropped keys whose captured values are non-empty, swept
	// over the emitted event. output_file is absent from this list on purpose —
	// its captured value is "" and a sweep for it would match every unset field.
	ev := taskNotificationEvent(t, string(line))
	rv := reflect.ValueOf(ev)
	var swept int
	for _, key := range []string{"tool_use_id", "uuid", "session_id"} {
		v, _ := payload[key].(string)
		if v == "" {
			t.Fatalf("the capture carries no string %q, so the drop assertion would be vacuous", key)
		}
		for i := 0; i < rv.NumField(); i++ {
			if rv.Field(i).Kind() != reflect.String {
				continue
			}
			swept++
			if rv.Field(i).String() == v {
				t.Errorf("field %s carries claude's %s (%q); it is deliberately NOT on this event",
					rv.Type().Field(i).Name, key, v)
			}
		}
	}
	// The sweep visiting nothing would pass silently, which is the one way this
	// assertion could rot into decoration. A LITERAL floor, not an expression
	// derived from the event's own shape, for TestParser_TaskUpdatedMapsFromCapture's
	// reason: three keys over four string fields (TaskID, Patch, Status, Summary).
	const wantSwept = 12
	if swept != wantSwept {
		t.Errorf("the sweep visited %d field/key pairs, want %d — a field was added to or removed from "+
			"BackgroundTaskUpdated and this floor is what says so", swept, wantSwept)
	}
}

// taskNotificationLineFixture builds a system/task_notification line from the
// three mapped keys. It invents NO field structure — the keys are exactly the
// capture's — and exists only to vary the VALUES, which is what the cap proof
// needs and what the capture cannot supply: the captured line is 249 bytes with a
// 9-byte summary, far under every cap.
func taskNotificationLineFixture(t *testing.T, taskID, status, summary string) string {
	t.Helper()
	b, err := json.Marshal(map[string]string{
		"type":    "system",
		"subtype": "task_notification",
		"task_id": taskID,
		"status":  status,
		"summary": summary,
	})
	if err != nil {
		t.Fatalf("marshalling task_notification fixture: %v", err)
	}
	return string(b)
}

// TestParser_TaskNotificationBoundsClaudeText is AC5: every claude-authored string
// this subtype puts on the wire is cut to its cap at CONSTRUCTION and the cut is
// reported the way the family already reports one.
//
// Cap values come from the LITERAL fixtures, never from the constants they
// validate, per taskStartedCapCheat's rule: a fixture built from its own constant
// follows that constant green when someone halves it.
func TestParser_TaskNotificationBoundsClaudeText(t *testing.T) {
	t.Parallel()

	const smallID = "task-1"
	overID := strings.Repeat("i", taskFieldIDCapFixture+1)
	overStatus := strings.Repeat("s", taskFieldIDCapFixture+1)
	overSummary := strings.Repeat("m", taskSummaryCapFixture+1)

	tests := []struct {
		name                                     string
		taskID, status, summary                  string
		wantCut                                  []string
		wantLenTaskID, wantLenStatus, wantLenSum int
	}{
		{
			name: "nothing over cap is reported", taskID: smallID, status: "completed", summary: "done",
			wantCut: nil, wantLenTaskID: len(smallID), wantLenStatus: len("completed"), wantLenSum: len("done"),
		},
		{
			name: "an oversized summary is cut and named", taskID: smallID, status: "completed", summary: overSummary,
			wantCut: []string{"summary"}, wantLenTaskID: len(smallID), wantLenStatus: len("completed"),
			wantLenSum: taskSummaryCapFixture,
		},
		{
			name: "an oversized status is cut and named", taskID: smallID, status: overStatus, summary: "done",
			wantCut: []string{"status"}, wantLenTaskID: len(smallID), wantLenStatus: taskFieldIDCapFixture,
			wantLenSum: len("done"),
		},
		{
			// All three at once, named in DECLARATION order (task_id, status,
			// summary), which is what makes the value deterministic and pinnable.
			// patch never appears: this subtype's arm does not fill it.
			name:   "three fields over cap are named in declaration order",
			taskID: overID, status: overStatus, summary: overSummary,
			wantCut:       []string{"task_id", "status", "summary"},
			wantLenTaskID: taskFieldIDCapFixture, wantLenStatus: taskFieldIDCapFixture,
			wantLenSum: taskSummaryCapFixture,
		},
		{
			// The <= boundary: a field of exactly its cap is not truncated.
			name:    "every field exactly at its cap is not truncated",
			taskID:  strings.Repeat("i", taskFieldIDCapFixture),
			status:  strings.Repeat("s", taskFieldIDCapFixture),
			summary: strings.Repeat("m", taskSummaryCapFixture),
			wantCut: nil, wantLenTaskID: taskFieldIDCapFixture, wantLenStatus: taskFieldIDCapFixture,
			wantLenSum: taskSummaryCapFixture,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ev := taskNotificationEvent(t, taskNotificationLineFixture(t, tc.taskID, tc.status, tc.summary))

			if !reflect.DeepEqual(ev.TruncatedFields, tc.wantCut) {
				t.Errorf("TruncatedFields: got %#v, want %#v", ev.TruncatedFields, tc.wantCut)
			}
			if len(ev.TaskID) != tc.wantLenTaskID {
				t.Errorf("len(TaskID): got %d, want %d", len(ev.TaskID), tc.wantLenTaskID)
			}
			if len(ev.Status) != tc.wantLenStatus {
				t.Errorf("len(Status): got %d, want %d", len(ev.Status), tc.wantLenStatus)
			}
			if len(ev.Summary) != tc.wantLenSum {
				t.Errorf("len(Summary): got %d, want %d", len(ev.Summary), tc.wantLenSum)
			}
			// AC3 on every row: no input to this subtype's arm can produce a patch,
			// because the daemon has none to carry and synthesizes nothing.
			if ev.Patch != "" {
				t.Errorf("Patch: got %q, want empty on every task_notification event", ev.Patch)
			}
		})
	}

	t.Run("a cut mid-rune yields valid UTF-8", func(t *testing.T) {
		t.Parallel()
		// The cap is a BYTE count, so it can land inside a multi-byte rune. The value
		// rides a JSON string field downstream, where an invalid sequence would be
		// silently replaced, so the producer scrubs it here instead. The padding puts
		// the first multi-byte rune so its bytes straddle the cut.
		pad := strings.Repeat("m", taskSummaryCapFixture-1)
		ev := taskNotificationEvent(t, taskNotificationLineFixture(t, smallID, "completed", pad+strings.Repeat("€", 8)))

		if !reflect.DeepEqual(ev.TruncatedFields, []string{"summary"}) {
			t.Fatalf("TruncatedFields: got %#v, want [summary]", ev.TruncatedFields)
		}
		if !utf8.ValidString(ev.Summary) {
			t.Errorf("Summary is not valid UTF-8 after a mid-rune cut")
		}
		// Short of the cap, not at it: the empty replacement DELETES the partial rune
		// rather than replacing it.
		if len(ev.Summary) > taskSummaryCapFixture {
			t.Errorf("len(Summary): got %d, want <= %d", len(ev.Summary), taskSummaryCapFixture)
		}
	})
}

// TestParser_TaskNotificationDropIsLoggedContentFree applies the package's standing
// rule to the fourth subtype: nothing derived from claude's output reaches a log.
// The content crosses the wire, not the log.
//
// summary is this subtype's most attractive thing to log while debugging a
// malformed line — it is the field most likely to hold a readable account of what
// went wrong — which is why the decode-failure path is driven here with a
// distinctive one.
func TestParser_TaskNotificationDropIsLoggedContentFree(t *testing.T) {
	t.Parallel()

	captured := capturedTaskNotificationLine(t)
	var payload map[string]any
	if err := json.Unmarshal(captured, &payload); err != nil {
		t.Fatalf("decoding the captured payload: %v", err)
	}
	capturedSession, _ := payload["session_id"].(string)
	if capturedSession == "" {
		t.Fatalf("the capture carries no session_id, so the leak sweep would be vacuous")
	}

	// A task_notification line the shape cannot decode (task_id is a number), so
	// the decode-failure path runs. Its summary carries a distinctive literal, and
	// the whole point is that it must appear in no log record.
	const malformedSummaryValue = "rm -rf /tmp/never-log-this-summary"
	malformed := `{"type":"system","subtype":"task_notification","task_id":42,` +
		`"status":"failed","summary":"` + malformedSummaryValue + `"}`

	rec := &logRecorder{}
	var events []turnevent.Event
	p := NewParser(func(ev turnevent.Event) { events = append(events, ev) }, slog.New(rec))
	if _, err := p.Write(append(append([]byte(nil), captured...), '\n')); err != nil {
		t.Fatalf("Write err = %v, want nil", err)
	}
	if _, err := p.Write([]byte(malformed + "\n")); err != nil {
		t.Fatalf("Write err = %v, want nil", err)
	}

	// The malformed line is dropped, and NOT as an Unrecognized: keeping `system`
	// whole on ignoredLineTypes is what makes "no system line reaches the
	// unrecognized lane" structural, and it is what AC1's no-unrecognized half
	// rests on.
	if len(events) != 1 {
		t.Fatalf("event count: got %d, want 1 (the captured line only) — %#v", len(events), events)
	}
	ev, ok := events[0].(turnevent.BackgroundTaskUpdated)
	if !ok {
		t.Fatalf("event type: got %T, want turnevent.BackgroundTaskUpdated", events[0])
	}
	// The captured summary as the parser itself carried it, so the leak candidate
	// is derived from the shipped mapping rather than transcribed.
	if ev.Summary == "" {
		t.Fatalf("the mapped Summary is empty, so the leak sweep over it would be vacuous")
	}

	const dropMsg = "streamsup: dropping undecodable system line"
	var sawDrop bool
	for _, r := range rec.all() {
		if r.msg != dropMsg {
			continue
		}
		if !reflect.DeepEqual(r.attrs, map[string]string{"subtype": "task_notification"}) {
			// The task_updated sweep runs in its own test, so any other subtype here
			// means this line took a path it should not have.
			continue
		}
		sawDrop = true
	}
	if !sawDrop {
		t.Fatalf("no %q record naming task_notification: the decode-failure path never ran, so its leak sweep is vacuous", dropMsg)
	}

	for _, r := range rec.all() {
		for _, leak := range []string{ev.Summary, ev.Status, malformedSummaryValue, capturedSession} {
			if leak == "" {
				continue
			}
			if strings.Contains(r.msg, leak) {
				t.Errorf("record message carries claude-derived content: %q", r.msg)
			}
			for k, v := range r.attrs {
				if strings.Contains(v, leak) {
					t.Errorf("record %q attr %q carries claude-derived content; this path logs the subtype only", r.msg, k)
				}
			}
		}
	}
}
