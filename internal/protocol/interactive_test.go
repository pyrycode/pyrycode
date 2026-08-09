package protocol

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// roundTripEnvelope re-marshals payload back into env and asserts the
// envelope bytes are canonically byte-equal to raw. Re-marshalling the
// decoded payload struct (rather than passing the original RawMessage
// through) is what pins the struct → wire shape: a missing or reordered
// json tag would diverge the bytes here.
func roundTripEnvelope(t *testing.T, env Envelope, payload any, raw []byte) {
	t.Helper()
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	env.Payload = payloadBytes
	out, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	if !bytes.Equal(canonical(t, out), canonical(t, raw)) {
		t.Errorf("round-trip bytes differ:\n got: %s\nwant: %s", out, raw)
	}
}

func TestTurnStatePayload_RoundTrip(t *testing.T) {
	raw := readFixture(t, "turn_state.json")

	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if env.Type != TypeTurnState {
		t.Errorf("Type: got %q, want %q", env.Type, TypeTurnState)
	}

	var payload TurnStatePayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload.ConversationID != "c1" {
		t.Errorf("ConversationID: got %q, want %q", payload.ConversationID, "c1")
	}
	if payload.State != "thinking" {
		t.Errorf("State: got %q, want %q", payload.State, "thinking")
	}

	roundTripEnvelope(t, env, payload, raw)
}

func TestAssistantDeltaPayload_RoundTrip(t *testing.T) {
	raw := readFixture(t, "assistant_delta.json")

	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if env.Type != TypeAssistantDelta {
		t.Errorf("Type: got %q, want %q", env.Type, TypeAssistantDelta)
	}

	var payload AssistantDeltaPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload.ConversationID != "c1" {
		t.Errorf("ConversationID: got %q, want %q", payload.ConversationID, "c1")
	}
	if payload.TurnID != "t7" {
		t.Errorf("TurnID: got %q, want %q", payload.TurnID, "t7")
	}
	// Seq==0 is the boundary value: the fixture pins that a zero seq stays
	// on the wire (no omitempty would silently drop it).
	if payload.Seq != 0 {
		t.Errorf("Seq: got %d, want 0", payload.Seq)
	}
	if payload.Text != "Let me check the weather for you." {
		t.Errorf("Text: got %q, want %q", payload.Text, "Let me check the weather for you.")
	}

	roundTripEnvelope(t, env, payload, raw)
}

func TestToolUsePayload_RoundTrip(t *testing.T) {
	raw := readFixture(t, "tool_use.json")

	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if env.Type != TypeToolUse {
		t.Errorf("Type: got %q, want %q", env.Type, TypeToolUse)
	}

	var payload ToolUsePayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload.ConversationID != "c1" {
		t.Errorf("ConversationID: got %q, want %q", payload.ConversationID, "c1")
	}
	if payload.TurnID != "t7" {
		t.Errorf("TurnID: got %q, want %q", payload.TurnID, "t7")
	}
	if payload.ToolUseID != "tu1" {
		t.Errorf("ToolUseID: got %q, want %q", payload.ToolUseID, "tu1")
	}
	if payload.Name != "WebSearch" {
		t.Errorf("Name: got %q, want %q", payload.Name, "WebSearch")
	}
	if payload.InputSummary != "weather in Helsinki tomorrow" {
		t.Errorf("InputSummary: got %q, want %q", payload.InputSummary, "weather in Helsinki tomorrow")
	}

	roundTripEnvelope(t, env, payload, raw)
}

func TestToolResultPayload_RoundTrip(t *testing.T) {
	raw := readFixture(t, "tool_result.json")

	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if env.Type != TypeToolResult {
		t.Errorf("Type: got %q, want %q", env.Type, TypeToolResult)
	}

	var payload ToolResultPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload.ConversationID != "c1" {
		t.Errorf("ConversationID: got %q, want %q", payload.ConversationID, "c1")
	}
	if payload.TurnID != "t7" {
		t.Errorf("TurnID: got %q, want %q", payload.TurnID, "t7")
	}
	if payload.ToolUseID != "tu1" {
		t.Errorf("ToolUseID: got %q, want %q", payload.ToolUseID, "tu1")
	}
	// IsError==false is the boundary value: the fixture pins that a false
	// bool stays on the wire (no omitempty would silently drop it).
	if payload.IsError {
		t.Errorf("IsError: got true, want false")
	}
	if payload.ResultSummary != "4°C, light snow showers in the afternoon." {
		t.Errorf("ResultSummary: got %q, want %q", payload.ResultSummary, "4°C, light snow showers in the afternoon.")
	}

	roundTripEnvelope(t, env, payload, raw)
}

func TestTurnEndPayload_RoundTrip(t *testing.T) {
	raw := readFixture(t, "turn_end.json")

	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if env.Type != TypeTurnEnd {
		t.Errorf("Type: got %q, want %q", env.Type, TypeTurnEnd)
	}

	var payload TurnEndPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload.ConversationID != "c1" {
		t.Errorf("ConversationID: got %q, want %q", payload.ConversationID, "c1")
	}
	if payload.TurnID != "t7" {
		t.Errorf("TurnID: got %q, want %q", payload.TurnID, "t7")
	}
	// stop_reason carries the turnevent.TurnEndReason string values
	// verbatim; "end_turn" is a real taxonomy value.
	if payload.StopReason != "end_turn" {
		t.Errorf("StopReason: got %q, want %q", payload.StopReason, "end_turn")
	}

	roundTripEnvelope(t, env, payload, raw)
}

func TestStallPayload_RoundTrip(t *testing.T) {
	raw := readFixture(t, "stall.json")

	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if env.Type != TypeStall {
		t.Errorf("Type: got %q, want %q", env.Type, TypeStall)
	}

	var payload StallPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload.ConversationID != "c1" {
		t.Errorf("ConversationID: got %q, want %q", payload.ConversationID, "c1")
	}

	roundTripEnvelope(t, env, payload, raw)
}

func TestApiRetryPayload_RoundTrip(t *testing.T) {
	raw := readFixture(t, "api_retry.json")

	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if env.Type != TypeApiRetry {
		t.Errorf("Type: got %q, want %q", env.Type, TypeApiRetry)
	}

	var payload ApiRetryPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload.ConversationID != "c1" {
		t.Errorf("ConversationID: got %q, want %q", payload.ConversationID, "c1")
	}
	if !payload.Active {
		t.Errorf("Active: got %v, want true", payload.Active)
	}
	if payload.Current != 3 {
		t.Errorf("Current: got %d, want %d", payload.Current, 3)
	}
	if payload.Total != 10 {
		t.Errorf("Total: got %d, want %d", payload.Total, 10)
	}

	roundTripEnvelope(t, env, payload, raw)
}

func TestCompactingPayload_RoundTrip(t *testing.T) {
	raw := readFixture(t, "compacting.json")

	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if env.Type != TypeCompacting {
		t.Errorf("Type: got %q, want %q", env.Type, TypeCompacting)
	}

	var payload CompactingPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload.ConversationID != "c1" {
		t.Errorf("ConversationID: got %q, want %q", payload.ConversationID, "c1")
	}
	if !payload.Active {
		t.Errorf("Active: got %v, want true", payload.Active)
	}

	roundTripEnvelope(t, env, payload, raw)
}

func TestUnrecognizedMessagePayload_RoundTrip(t *testing.T) {
	raw := readFixture(t, "unrecognized_message.json")

	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if env.Type != TypeUnrecognizedMessage {
		t.Errorf("Type: got %q, want %q", env.Type, TypeUnrecognizedMessage)
	}

	var payload UnrecognizedMessagePayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload.ConversationID != "c1" {
		t.Errorf("ConversationID: got %q, want %q", payload.ConversationID, "c1")
	}
	if payload.Site != "line_type" {
		t.Errorf("Site: got %q, want %q", payload.Site, "line_type")
	}
	if payload.MessageType != "mystery_event" {
		t.Errorf("MessageType: got %q, want %q", payload.MessageType, "mystery_event")
	}
	// Raw is carried as an opaque JSON *string*, so the fixture's escaping is
	// part of the pinned shape: the inner braces and quotes must survive the
	// trip without being re-interpreted as structure.
	wantRaw := `{"type":"mystery_event","detail":"something new"}`
	if payload.Raw != wantRaw {
		t.Errorf("Raw: got %q, want %q", payload.Raw, wantRaw)
	}
	if payload.Truncated {
		t.Errorf("Truncated: got %v, want false", payload.Truncated)
	}

	roundTripEnvelope(t, env, payload, raw)
}

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
	if got, want := strings.Join(payload.Tasks[0].TruncatedFields, ","), "description"; got != want {
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

func TestThinkingProgressPayload_RoundTrip(t *testing.T) {
	raw := readFixture(t, "thinking_progress.json")

	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if env.Type != TypeThinkingProgress {
		t.Errorf("Type: got %q, want %q", env.Type, TypeThinkingProgress)
	}

	var payload ThinkingProgressPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload.ConversationID != "c1" {
		t.Errorf("ConversationID: got %q, want %q", payload.ConversationID, "c1")
	}
	// The two readings carry DIFFERENT fixture values on purpose: equal ones
	// would let a struct that wired both wire keys to the same field pass.
	if payload.EstimatedTokens != 184 {
		t.Errorf("EstimatedTokens: got %d, want %d", payload.EstimatedTokens, 184)
	}
	if payload.EstimatedTokensDelta != 37 {
		t.Errorf("EstimatedTokensDelta: got %d, want %d", payload.EstimatedTokensDelta, 37)
	}

	roundTripEnvelope(t, env, payload, raw)
}

// TestThinkingProgressType_IsNotClaudesSubtype pins the translation layer this
// frame exists to preserve. The daemon is the ONE place a claude rename lands;
// naming the wire type after claude's own `system/thinking_tokens` subtype would
// undo that, letting a single claude release break every client at once with
// nothing in between to absorb it. So the constant is the daemon's variant name
// (turnevent.ThinkingProgress), and the discriminating word is "progress" — what
// the daemon reports — not "tokens", which is claude's.
//
// The substring check is what makes this non-vacuous rather than a restatement
// of the constant: it goes red for "thinking_tokens", for "thinking_tokens_
// progress", and for any other name derived from claude's subtype, not just for
// the exact literal. It is scoped to the TYPE constant only — the payload's
// FIELD names legitimately contain "tokens", because those are readings of
// tokens and no client dispatches on them.
func TestThinkingProgressType_IsNotClaudesSubtype(t *testing.T) {
	if TypeThinkingProgress == "thinking_tokens" {
		t.Errorf("wire type is claude's subtype %q; it must be the daemon's own name", TypeThinkingProgress)
	}
	if strings.Contains(TypeThinkingProgress, "tokens") {
		t.Errorf("wire type %q is derived from claude's subtype (contains %q)", TypeThinkingProgress, "tokens")
	}
	if TypeThinkingProgress != "thinking_progress" {
		t.Errorf("wire type: got %q, want %q", TypeThinkingProgress, "thinking_progress")
	}
}

// maxV2AppEnvelope is the Mobile Protocol v2 application-envelope size cap
// (docs/protocol-mobile.md § Application-envelope size cap). Test-local on
// purpose: nothing in internal/protocol enforces the cap — the transport does —
// so an exported constant here would imply an enforcement this package does not
// perform.
const maxV2AppEnvelope = 65519

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
			TaskType:        fill(capTaskFieldID),
			Description:     fill(capTaskRosterDescription),
			TruncatedFields: []string{"task_id", "task_type", "description"},
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
				ConversationID:  convID,
				TaskID:          fill(capTaskFieldID),
				Patch:           fill(capTaskPatch),
				TruncatedFields: []string{"task_id", "patch"},
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
