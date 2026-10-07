package protocol

import (
	"bytes"
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestBackgroundTaskStartedPayload_RoundTrip(t *testing.T) {
	raw := readFixture(t, "background_task_started.json")

	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if env.Type != TypeBackgroundTaskStarted {
		t.Errorf("Type: got %q, want %q", env.Type, TypeBackgroundTaskStarted)
	}

	var payload BackgroundTaskStartedPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload.ConversationID != "c1" {
		t.Errorf("ConversationID: got %q, want %q", payload.ConversationID, "c1")
	}
	if payload.TaskID != "task_01ABC" {
		t.Errorf("TaskID: got %q, want %q", payload.TaskID, "task_01ABC")
	}
	// tool_call_id, NOT claude's tool_use_id: the wire name matches the name
	// TruncatedFields would report for the field, and the name ToolUsePayload /
	// ToolResultPayload already carry for the same identifier.
	if payload.ToolCallID != "toolu_01XYZ" {
		t.Errorf("ToolCallID: got %q, want %q", payload.ToolCallID, "toolu_01XYZ")
	}
	// A local_bash Description is a literal command line, so the fixture's
	// shell metacharacters are part of the pinned shape: encoding/json emits
	// '<', '>' and '&' in their six-byte \uXXXX form, which is the escaping the
	// envelope-cap test below measures against.
	wantDesc := `grep -rn 'a<b&c' . > /tmp/out.txt &`
	if payload.Description != wantDesc {
		t.Errorf("Description: got %q, want %q", payload.Description, wantDesc)
	}
	if payload.TaskType != "local_bash" {
		t.Errorf("TaskType: got %q, want %q", payload.TaskType, "local_bash")
	}
	if got, want := strings.Join(payload.TruncatedFields, ","), "description"; got != want {
		t.Errorf("TruncatedFields: got %q, want %q", got, want)
	}

	roundTripEnvelope(t, env, payload, raw)
}

func TestBackgroundTaskUpdatedPayload_RoundTrip(t *testing.T) {
	raw := readFixture(t, "background_task_updated.json")

	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if env.Type != TypeBackgroundTaskUpdated {
		t.Errorf("Type: got %q, want %q", env.Type, TypeBackgroundTaskUpdated)
	}

	var payload BackgroundTaskUpdatedPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload.ConversationID != "c1" {
		t.Errorf("ConversationID: got %q, want %q", payload.ConversationID, "c1")
	}
	if payload.TaskID != "task_01ABC" {
		t.Errorf("TaskID: got %q, want %q", payload.TaskID, "task_01ABC")
	}
	// The fixture's patch is deliberately UNPARSEABLE — the producer truncates
	// mid-object, so a truncated patch is not valid JSON. This assertion is what
	// pins Patch as text rather than json.RawMessage: a raw-JSON field could not
	// carry these bytes at all.
	wantPatch := `{"is_backgrounded":tr`
	if payload.Patch != wantPatch {
		t.Errorf("Patch: got %q, want %q", payload.Patch, wantPatch)
	}
	if json.Valid([]byte(payload.Patch)) {
		t.Errorf("Patch: fixture must carry an unparseable fragment, got valid JSON %q", payload.Patch)
	}
	if got, want := strings.Join(payload.TruncatedFields, ","), "patch"; got != want {
		t.Errorf("TruncatedFields: got %q, want %q", got, want)
	}

	roundTripEnvelope(t, env, payload, raw)
}

// TestBackgroundTaskUpdatedPayload_TerminalRoundTrip pins the SECOND shape this
// one frame carries (#2245): the terminal state claude reports on its
// system/task_notification line.
//
// A second fixture rather than fields added to the first, and the pair is the
// assertion. The two producing subtypes fill DISJOINT fields, so one fixture
// carrying both patch and status would be a shape the daemon cannot emit, and it
// would let a wire regression that merged the two sets pass. Keeping them apart is
// what makes each one's empty half load-bearing.
func TestBackgroundTaskUpdatedPayload_TerminalRoundTrip(t *testing.T) {
	raw := readFixture(t, "background_task_updated_terminal.json")

	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if env.Type != TypeBackgroundTaskUpdated {
		t.Errorf("Type: got %q, want %q", env.Type, TypeBackgroundTaskUpdated)
	}

	var payload BackgroundTaskUpdatedPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	// The join key AC2 names: the same task_id the sibling fixture carries, which
	// is what a client uses to reach the background_task_started that opened the
	// row this frame closes.
	if payload.TaskID != "task_01ABC" {
		t.Errorf("TaskID: got %q, want %q", payload.TaskID, "task_01ABC")
	}
	if payload.Status != "completed" {
		t.Errorf("Status: got %q, want %q — the one terminal token ever captured", payload.Status, "completed")
	}
	if payload.Summary != "cat /tmp/pyry-fifo" {
		t.Errorf("Summary: got %q, want the fixture's summary", payload.Summary)
	}
	// The load-bearing empty: a line of this subtype carries no patch key and the
	// daemon synthesizes none, so a producer that manufactured one to report the
	// terminal state would falsify this type's own SECURITY block.
	if payload.Patch != "" {
		t.Errorf("Patch: got %q, want empty on a frame produced by task_notification", payload.Patch)
	}
	if payload.TruncatedFields != nil {
		t.Errorf("TruncatedFields: got %v, want nil — nothing in this fixture approaches a cap", payload.TruncatedFields)
	}

	roundTripEnvelope(t, env, payload, raw)
}

func TestBackgroundTaskRosterPayload_RoundTrip(t *testing.T) {
	raw := readFixture(t, "background_task_roster.json")

	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if env.Type != TypeBackgroundTaskRoster {
		t.Errorf("Type: got %q, want %q", env.Type, TypeBackgroundTaskRoster)
	}

	var payload BackgroundTaskRosterPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload.ConversationID != "c1" {
		t.Errorf("ConversationID: got %q, want %q", payload.ConversationID, "c1")
	}
	if len(payload.Tasks) != 2 {
		t.Fatalf("Tasks: got %d entries, want 2", len(payload.Tasks))
	}
	if payload.Tasks[0].TaskID != "task_01ABC" {
		t.Errorf("Tasks[0].TaskID: got %q, want %q", payload.Tasks[0].TaskID, "task_01ABC")
	}
	if payload.Tasks[0].TaskType != "local_bash" {
		t.Errorf("Tasks[0].TaskType: got %q, want %q", payload.Tasks[0].TaskType, "local_bash")
	}
	if want := `grep -rn 'a<b&c' .`; payload.Tasks[0].Description != want {
		t.Errorf("Tasks[0].Description: got %q, want %q", payload.Tasks[0].Description, want)
	}
	// The two entries pin both forms of the per-entry truncation report:
	// populated on one, null on the other. truncated_fields is deliberately NOT
	// normalised (unlike tasks) — nil and [] say the identical thing here.
	if got, want := strings.Join(payload.Tasks[0].TruncatedFields, ","), "description,tool_call_id"; got != want {
		t.Errorf("Tasks[0].TruncatedFields: got %q, want %q", got, want)
	}
	if payload.Tasks[1].TruncatedFields != nil {
		t.Errorf("Tasks[1].TruncatedFields: got %v, want nil", payload.Tasks[1].TruncatedFields)
	}
	// dropped_tasks is the count dimension, decided at the roster level and
	// distinct from any entry's text cut: the roster's true size is
	// len(Tasks) + DroppedTasks.
	if payload.DroppedTasks != 3 {
		t.Errorf("DroppedTasks: got %d, want 3", payload.DroppedTasks)
	}

	for i, want := range []string{"tool_01START", ""} {
		if payload.Tasks[i].ToolCallID != want {
			t.Errorf("Tasks[%d].ToolCallID = %q, want %q", i, payload.Tasks[i].ToolCallID, want)
		}
	}
	// Golden round-trip pins the key's presence even when its value is empty.
	roundTripEnvelope(t, env, payload, raw)
}

// TestBackgroundTaskRosterPayload_Empty_RoundTrip pins the empty roster, which
// is a MEANINGFUL frame — it says nothing is alive, exactly the signal #1240's
// symptom needs — so the tasks key must be present in the serialised bytes and
// still present after the trip, rather than elided.
func TestBackgroundTaskRosterPayload_Empty_RoundTrip(t *testing.T) {
	raw := readFixture(t, "background_task_roster_empty.json")

	if !bytes.Contains(canonical(t, raw), []byte(`"tasks":[]`)) {
		t.Errorf("fixture must carry the tasks key as an empty array, got: %s", raw)
	}

	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if env.Type != TypeBackgroundTaskRoster {
		t.Errorf("Type: got %q, want %q", env.Type, TypeBackgroundTaskRoster)
	}

	var payload BackgroundTaskRosterPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload.ConversationID != "c1" {
		t.Errorf("ConversationID: got %q, want %q", payload.ConversationID, "c1")
	}
	if len(payload.Tasks) != 0 {
		t.Errorf("Tasks: got %d entries, want 0", len(payload.Tasks))
	}
	if payload.DroppedTasks != 0 {
		t.Errorf("DroppedTasks: got %d, want 0", payload.DroppedTasks)
	}

	roundTripEnvelope(t, env, payload, raw)
}

// TestBackgroundTaskRosterPayload_NilTasksNormalises covers the case the empty
// fixture cannot: unmarshalling "tasks":[] yields a non-nil empty slice, so the
// fixture never exercises the nil path. A bridge mapping turnevent's nil Tasks
// (nil for an empty roster AND when claude omits the key) would take exactly
// that path, and without normalisation would ship "tasks":null to a phone while
// the fixture kept asserting []. Both the value and pointer forms are checked
// because a pointer-receiver marshaller would silently miss the value path
// roundTripEnvelope takes.
func TestBackgroundTaskRosterPayload_NilTasksNormalises(t *testing.T) {
	p := BackgroundTaskRosterPayload{ConversationID: "c1"}
	if p.Tasks != nil {
		t.Fatalf("precondition: Tasks must be nil, got %v", p.Tasks)
	}

	for _, tc := range []struct {
		name string
		in   any
	}{
		{"value", p},
		{"pointer", &p},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := json.Marshal(tc.in)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if bytes.Contains(out, []byte(`"tasks":null`)) {
				t.Errorf("nil Tasks marshalled to null: %s", out)
			}
			if !bytes.Contains(out, []byte(`"tasks":[]`)) {
				t.Errorf("nil Tasks did not normalise to []: %s", out)
			}
		})
	}

	// The normalisation must not mutate the receiver's copy back into the caller.
	if p.Tasks != nil {
		t.Errorf("MarshalJSON mutated the receiver: Tasks is now %v", p.Tasks)
	}
}

// The producer's caps, mirrored from internal/streamsup/parser.go. This package
// is a stdlib-only leaf and must not import internal/streamsup, so these are
// copies; each names its source constant, which is what a future cap change
// greps for. Raising a producer cap without updating these leaves the
// measurement below silently stale.
const (
	capTaskFieldID           = 256  // internal/streamsup.maxTaskFieldID
	capTaskDescription       = 4096 // internal/streamsup.maxTaskDescription
	capTaskPatch             = 4096 // internal/streamsup.maxTaskPatch
	capTaskRosterEntries     = 8    // internal/streamsup.maxTaskRosterEntries
	capTaskRosterDescription = 512  // internal/streamsup.maxTaskRosterDescription
	capTaskSummary           = 4096 // internal/streamsup.maxTaskSummary
)

// TestBackgroundTaskPayloads_FitV2EnvelopeCap constructs each payload with every
// string field full to its producer cap — and the roster at its full entry cap —
// and proves the serialised envelope fits under the v2 application-envelope cap.
// Per-field caps do not compose into an envelope guarantee on their own, so this
// is measured rather than argued.
//
// The fill is '<', not 'a'. encoding/json escapes '<', '>' and '&' (SetEscapeHTML
// is on by default) and every control byte to a six-byte \uXXXX form, so one
// input byte costs six on the wire; an 'a' fill under-reports by over 5x and
// would prove nothing about the constraint. A NUL fill measures identically —
// "a character that costs six bytes to escape" admits either. Multi-byte runes
// are NOT the worst case: Go emits them raw, so a 4-byte emoji stays 4 bytes.
// The producer's caps are BYTE caps (internal/streamsup/parser.go's
// truncateField slices s[:limit]), which is what makes 6-bytes-out-per-input-byte
// the true ceiling.
//
// '<' is also the realistic case rather than a contrived one: Description is the
// literal command line for claude's local_bash task type, and '<', '>' and '&'
// are exactly what a shell command line carries.
func TestBackgroundTaskPayloads_FitV2EnvelopeCap(t *testing.T) {
	fill := func(n int) string { return strings.Repeat("<", n) }

	// A hostile conversation identity too: the bridge supplies this field, and
	// nothing in the payload bounds it.
	convID := fill(64)

	roster := BackgroundTaskRosterPayload{
		ConversationID: convID,
		DroppedTasks:   1 << 31,
	}
	for i := 0; i < capTaskRosterEntries; i++ {
		roster.Tasks = append(roster.Tasks, BackgroundTask{
			TaskID:          fill(capTaskFieldID),
			ToolCallID:      fill(capTaskFieldID),
			TaskType:        fill(capTaskFieldID),
			Description:     fill(capTaskRosterDescription),
			TruncatedFields: []string{"task_id", "task_type", "description", "tool_call_id"},
		})
	}

	cases := []struct {
		name    string
		typ     string
		payload any
	}{
		{
			name: "started",
			typ:  TypeBackgroundTaskStarted,
			payload: BackgroundTaskStartedPayload{
				ConversationID:  convID,
				TaskID:          fill(capTaskFieldID),
				ToolCallID:      fill(capTaskFieldID),
				Description:     fill(capTaskDescription),
				TaskType:        fill(capTaskFieldID),
				TruncatedFields: []string{"task_id", "tool_call_id", "description", "task_type"},
			},
		},
		{
			name: "updated",
			typ:  TypeBackgroundTaskUpdated,
			payload: BackgroundTaskUpdatedPayload{
				ConversationID: convID,
				TaskID:         fill(capTaskFieldID),
				// All four claude-derived fields at once, which is MORE than any
				// single frame can carry: the two producing subtypes fill disjoint
				// sets (#2245), so patch is never populated alongside status and
				// summary. The row is deliberately the impossible worst case anyway
				// — it measures the TYPE's ceiling rather than an arm's, which is
				// the number a third producing subtype would have to fit inside.
				Patch:           fill(capTaskPatch),
				Status:          fill(capTaskFieldID),
				Summary:         fill(capTaskSummary),
				TruncatedFields: []string{"task_id", "patch", "status", "summary"},
			},
		},
		{
			name: "progress",
			typ:  TypeBackgroundTaskProgress,
			payload: BackgroundTaskProgressPayload{
				ConversationID: convID,
				TaskID:         fill(capTaskFieldID),
				Description:    fill(capTaskDescription),
				SubagentType:   fill(capTaskFieldID),
				LastToolName:   fill(capTaskFieldID),
				// The three integers are at their type's widest so the row measures the
				// envelope rather than a comfortable reading. They contribute no
				// claude-derived TEXT term, which is why this frame's text worst case —
				// 256 + 4096 + 256 + 256 = 4864 bytes — is background_task_started's to
				// the byte: three identifier-shaped fields plus one description, the
				// same shape. The two rows' SERIALISED sizes differ only by key names
				// and the integers, which is what the log line below shows.
				TotalTokens:     math.MaxInt64,
				ToolUses:        math.MaxInt64,
				DurationMS:      math.MaxInt64,
				TruncatedFields: []string{"task_id", "description", "subagent_type", "last_tool_name"},
			},
		},
		{name: "roster", typ: TypeBackgroundTaskRoster, payload: roster},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body, err := json.Marshal(tc.payload)
			if err != nil {
				t.Fatalf("marshal payload: %v", err)
			}
			// Worst-case envelope too: max-uint64 ids and a populated EventID,
			// so the outer frame costs as much as it ever can.
			eventID := ^uint64(0)
			env := Envelope{
				ID:      ^uint64(0),
				Type:    tc.typ,
				TS:      time.Date(2026, 5, 8, 10, 33, 18, 0, time.UTC),
				Payload: body,
				EventID: &eventID,
			}
			out, err := json.Marshal(env)
			if err != nil {
				t.Fatalf("marshal envelope: %v", err)
			}
			t.Logf("%s at full caps: %d B, %.1f%% of the %d-byte v2 application-envelope cap",
				tc.name, len(out), float64(len(out))/float64(maxV2AppEnvelope)*100, maxV2AppEnvelope)
			if len(out) >= maxV2AppEnvelope {
				t.Errorf("serialised envelope: got %d B, want < %d B", len(out), maxV2AppEnvelope)
			}
		})
	}
}

// TestBackgroundTaskProgressPayload_RoundTrip pins #2246's wire shape.
//
// The fixture's values are the committed capture's own second frame plus a
// conversation id the bridge injects, so the golden bytes and the producer's
// measurement cannot drift apart silently. The three integers carry DIFFERENT
// values on purpose, following ThinkingProgressPayload's fixture: equal ones would
// let a struct that wired two wire keys to one field pass.
func TestBackgroundTaskProgressPayload_RoundTrip(t *testing.T) {
	raw := readFixture(t, "background_task_progress.json")

	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if env.Type != TypeBackgroundTaskProgress {
		t.Errorf("Type: got %q, want %q", env.Type, TypeBackgroundTaskProgress)
	}

	var payload BackgroundTaskProgressPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	for _, f := range []struct {
		name string
		got  string
		want string
	}{
		{"ConversationID", payload.ConversationID, "c1"},
		{"TaskID", payload.TaskID, "a8eec1cd5e109aa38"},
		{"Description", payload.Description, "Reading beta.txt"},
		{"SubagentType", payload.SubagentType, "general-purpose"},
		{"LastToolName", payload.LastToolName, "Read"},
	} {
		if f.got != f.want {
			t.Errorf("%s: got %q, want %q", f.name, f.got, f.want)
		}
	}
	for _, f := range []struct {
		name string
		got  int
		want int
	}{
		{"TotalTokens", payload.TotalTokens, 16246},
		{"ToolUses", payload.ToolUses, 2},
		{"DurationMS", payload.DurationMS, 4546},
	} {
		if f.got != f.want {
			t.Errorf("%s: got %d, want %d", f.name, f.got, f.want)
		}
	}
	if payload.TruncatedFields != nil {
		t.Errorf("TruncatedFields: got %v, want nil", payload.TruncatedFields)
	}

	roundTripEnvelope(t, env, payload, raw)
}

// TestBackgroundTaskProgressType_IsNotClaudesSubtype pins the translation layer,
// for TestThinkingProgressType_IsNotClaudesSubtype's reason: the daemon is the ONE
// place a claude rename lands, and naming the wire type after claude's own
// `system/task_progress` subtype would undo that.
//
// The near-miss it must refuse is SPECIFIC and is why this test is not a
// restatement of the constant. claude's own vocabulary carries a DIFFERENT
// top-level type spelled tool_progress, already consumed elsewhere in the daemon,
// and the two are documented as the easy mistake on this ticket. A wire name that
// dropped the "background_task_" prefix would be indistinguishable from a frame
// about that other thing.
func TestBackgroundTaskProgressType_IsNotClaudesSubtype(t *testing.T) {
	if TypeBackgroundTaskProgress == "task_progress" {
		t.Errorf("wire type is claude's subtype %q; it must be the daemon's own name", TypeBackgroundTaskProgress)
	}
	if TypeBackgroundTaskProgress == "tool_progress" {
		t.Errorf("wire type is claude's UNRELATED tool_progress type; the two are documented as the " +
			"easy conflation on this frame and mean different things")
	}
	if !strings.HasPrefix(TypeBackgroundTaskProgress, "background_task_") {
		t.Errorf("wire type %q does not carry the family prefix, so a client cannot tell it from a "+
			"frame about claude's separate tool_progress type", TypeBackgroundTaskProgress)
	}
	if TypeBackgroundTaskProgress != "background_task_progress" {
		t.Errorf("wire type: got %q, want %q", TypeBackgroundTaskProgress, "background_task_progress")
	}
}

// TestBackgroundTaskProgressPayload_DeclaresNoneOfClaudesOmittedKeys is the wire
// half of the family's standing-omission rule, asserted STRUCTURALLY over the
// struct's json tags rather than over one marshalled value.
//
// A value sweep would prove nothing here: every omitted key's absence looks
// identical to a field that happens to be empty, and it would pass unchanged if
// somebody declared the field and left it unset. Absence from the TYPE is the
// guarantee, and it is the only form of it that also covers `summary`, which no
// captured line carries a value for.
func TestBackgroundTaskProgressPayload_DeclaresNoneOfClaudesOmittedKeys(t *testing.T) {
	declared := map[string]bool{}
	pt := reflect.TypeOf(BackgroundTaskProgressPayload{})
	for i := range pt.NumField() {
		declared[strings.Split(pt.Field(i).Tag.Get("json"), ",")[0]] = true
	}
	for _, key := range []string{"session_id", "uuid", "tool_use_id", "summary", "output_file", "patch"} {
		if declared[key] {
			t.Errorf("BackgroundTaskProgressPayload declares %q — see turnevent.BackgroundTaskProgress "+
				"for the omission set and why each is absent from the DECODE TARGET upstream too", key)
		}
	}
}
