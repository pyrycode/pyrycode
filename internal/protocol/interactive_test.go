package protocol

import (
	"bytes"
	"encoding/json"
	"fmt"
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
	// The fixture's input is the WebSearch call's one real field, so it pins a
	// shape a client actually receives rather than a synthetic key: the same
	// text input_summary carries, but addressable by name (#1678).
	if len(payload.Input) != 1 || payload.Input["query"] != "weather in Helsinki tomorrow" {
		t.Errorf("Input: got %#v, want map[query:weather in Helsinki tomorrow]", payload.Input)
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

func TestRateLimitedPayload_RoundTrip(t *testing.T) {
	raw := readFixture(t, "rate_limited.json")

	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if env.Type != TypeRateLimited {
		t.Errorf("Type: got %q, want %q", env.Type, TypeRateLimited)
	}

	var payload RateLimitedPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload.ConversationID != "c1" {
		t.Errorf("ConversationID: got %q, want %q", payload.ConversationID, "c1")
	}
	// The fixture's status is deliberately NOT a value any capture carries.
	// Every capture on record reads "allowed", which is the one value the
	// producer's gate silences, so a realistic-looking alternative here would be
	// an invention a client author could copy as if it were measured. The angle
	// brackets double as the escaping pin: encoding/json emits '<' and '>' in
	// their six-byte \uXXXX form, so a claude-authored string survives the trip
	// byte-exactly.
	if payload.Status != "<unmeasured>" {
		t.Errorf("Status: got %q, want %q", payload.Status, "<unmeasured>")
	}
	// status and limit_type carry DIFFERENT fixture values on purpose: equal ones
	// would let a struct that wired both wire keys to the same field pass.
	if payload.LimitType != "five_hour" {
		t.Errorf("LimitType: got %q, want %q", payload.LimitType, "five_hour")
	}
	if payload.ResetsAt != 1786012405 {
		t.Errorf("ResetsAt: got %d, want %d", payload.ResetsAt, 1786012405)
	}
	// Joined rather than compared as a set: the producer appends these names in
	// declaration order (internal/streamsup/parser.go's two bound() calls), and a
	// swapped order must go red.
	if got, want := strings.Join(payload.TruncatedFields, ","), "status,limit_type"; got != want {
		t.Errorf("TruncatedFields: got %q, want %q", got, want)
	}

	roundTripEnvelope(t, env, payload, raw)
}

// TestRateLimitedPayload_ZeroValue_RoundTrip pins the encoding of every field's
// zero value, which is what this stream's no-omitempty rule
// (docs/protocol-mobile.md § Interactive events) actually asserts: adding
// omitempty to any one of the five fields turns this round trip red, and no
// realistic fixture can make that claim for all five.
//
// The frame itself is one the bridge will never emit — Status is never empty
// (the producer's gate does not emit on an empty status) and the bridge always
// supplies a conversation id. It exists for the encoding, not for the scenario.
//
// Unlike the empty roster's fixture, this one DOES reach the path that matters:
// unmarshalling "truncated_fields":null yields nil, marshalling nil yields null,
// so a MarshalJSON normalising nil→[] here — the guard BackgroundTaskRosterPayload
// needs and this payload must not have — would diverge the round-trip bytes.
// That makes this test the enforcement mechanism for the no-guard decision,
// which is why no separate construct-and-marshal test is owed.
func TestRateLimitedPayload_ZeroValue_RoundTrip(t *testing.T) {
	raw := readFixture(t, "rate_limited_zero.json")

	// Both guards are load-bearing rather than decoration. Edited to
	// "truncated_fields":[] the round trip alone would still pass — unmarshalling
	// [] yields a non-nil empty slice that marshals back to [] — so the round trip
	// pins the TYPE's behaviour only once the fixture is pinned to null. The
	// resets_at guard is what makes "explicit 0, not elided" checkable at all.
	if !bytes.Contains(canonical(t, raw), []byte(`"truncated_fields":null`)) {
		t.Errorf("fixture must carry the truncation report as null, got: %s", raw)
	}
	if !bytes.Contains(canonical(t, raw), []byte(`"resets_at":0`)) {
		t.Errorf("fixture must carry the reset timestamp explicitly as 0, got: %s", raw)
	}

	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if env.Type != TypeRateLimited {
		t.Errorf("Type: got %q, want %q", env.Type, TypeRateLimited)
	}

	var payload RateLimitedPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload.ConversationID != "" {
		t.Errorf("ConversationID: got %q, want %q", payload.ConversationID, "")
	}
	if payload.Status != "" {
		t.Errorf("Status: got %q, want %q", payload.Status, "")
	}
	if payload.LimitType != "" {
		t.Errorf("LimitType: got %q, want %q", payload.LimitType, "")
	}
	if payload.ResetsAt != 0 {
		t.Errorf("ResetsAt: got %d, want 0", payload.ResetsAt)
	}
	// Explicitly nil, not len() == 0: len is 0 for both nil and [], and [] is the
	// value this payload must never produce.
	if payload.TruncatedFields != nil {
		t.Errorf("TruncatedFields: got %v, want nil", payload.TruncatedFields)
	}

	roundTripEnvelope(t, env, payload, raw)
}

// TestRateLimitedType_IsNotClaudesVocabulary pins the translation layer this
// frame exists to preserve, as its thinking_progress sibling above does. The
// daemon is the ONE place a claude rename lands; naming the wire type after
// claude's own `rate_limit_event` line type would undo that.
//
// The sibling's exact form does not transfer: strings.Contains("rate_limited",
// "rate_limit") is TRUE, so a check on "rate_limit" would be red against the
// correct name. The discriminating word here is "event" — claude's, describing
// its line — where ours names the CONDITION the daemon reports.
func TestRateLimitedType_IsNotClaudesVocabulary(t *testing.T) {
	if TypeRateLimited == "rate_limit_event" {
		t.Errorf("wire type is claude's line type %q; it must be the daemon's own name", TypeRateLimited)
	}
	if strings.Contains(TypeRateLimited, "event") {
		t.Errorf("wire type %q is derived from claude's line type (contains %q)", TypeRateLimited, "event")
	}
	// The exact pin, and the string cmd/pyry/interactive_turn_v2.go's eventKind
	// already returns for this variant — internal/protocol cannot import cmd/pyry,
	// so the agreement between the two is pinned here rather than by a test that
	// reads both.
	if TypeRateLimited != "rate_limited" {
		t.Errorf("wire type: got %q, want %q", TypeRateLimited, "rate_limited")
	}

	// The payload's own bytes, not the envelope's — the envelope carries its own
	// id/ts and would dilute the check. These are regression pins: non-discriminating
	// today by construction, their job is to go red the day someone "helpfully"
	// adds claude's keys back. conversation_id is "c1" rather than a uuid-shaped
	// value so the uuid pin cannot pass by accident on the field's content.
	body, err := json.Marshal(RateLimitedPayload{
		ConversationID:  "c1",
		Status:          "<unmeasured>",
		LimitType:       "five_hour",
		ResetsAt:        1786012405,
		TruncatedFields: []string{"status", "limit_type"},
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	for _, key := range []string{"rateLimitType", "resetsAt", "rate_limit_info", "session_id", "uuid"} {
		if bytes.Contains(body, []byte(key)) {
			t.Errorf("payload carries claude's key or an excluded identity field %q: %s", key, body)
		}
	}
}

func TestModelAnnouncedPayload_RoundTrip(t *testing.T) {
	raw := readFixture(t, "model_announced.json")

	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if env.Type != TypeModelAnnounced {
		t.Errorf("Type: got %q, want %q", env.Type, TypeModelAnnounced)
	}

	var payload ModelAnnouncedPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload.ConversationID != "c1" {
		t.Errorf("ConversationID: got %q, want %q", payload.ConversationID, "c1")
	}
	// A MEASURED identifier, not an invented one: claude echoed exactly this for a
	// bare `haiku` alias, in the committed capture
	// internal/e2e/realclaude/testdata/dropped_lines_v2.1.220.json. A fixture is
	// something a client author copies as if it were observed, so it has to be.
	// (Contrast rate_limited.json's deliberately unmeasurable "<unmeasured>", which
	// exists because no capture of its field's non-benign value set exists at all.)
	if payload.Model != "claude-haiku-4-5-20251001" {
		t.Errorf("Model: got %q, want %q", payload.Model, "claude-haiku-4-5-20251001")
	}
	// true here and false in the zero fixture on purpose: with both false a struct
	// wiring "truncated" to the wrong field, or dropping it, would still pass. The
	// PAIRING is not a capture — a 25-byte identifier is nowhere near the producer's
	// 256-byte cap, so no real frame carries this model with this bool. The model
	// value is measured; the bool is chosen to discriminate.
	if !payload.Truncated {
		t.Errorf("Truncated: got %v, want true", payload.Truncated)
	}

	roundTripEnvelope(t, env, payload, raw)
}

// TestModelAnnouncedPayload_ZeroValue_RoundTrip pins the encoding of every
// field's zero value, which is what this stream's no-omitempty rule
// (docs/protocol-mobile.md § Interactive events) actually asserts: adding
// omitempty to any one of the three fields turns this round trip red, and no
// realistic fixture can make that claim for all three.
//
// The frame itself is one the bridge will never emit — Model is never empty (the
// producer's gate does not emit on an empty model) and the bridge always supplies
// a conversation id. It exists for the encoding, not for the scenario.
func TestModelAnnouncedPayload_ZeroValue_RoundTrip(t *testing.T) {
	raw := readFixture(t, "model_announced_zero.json")

	// Both guards are load-bearing rather than decoration: they are what makes
	// "explicit zero, not elided" checkable at all. Without them an omitempty on
	// either field would elide the key from BOTH the fixture and the re-marshalled
	// bytes, and the round trip alone would go on passing.
	if !bytes.Contains(canonical(t, raw), []byte(`"model":""`)) {
		t.Errorf("fixture must carry the announced model explicitly as the empty string, got: %s", raw)
	}
	if !bytes.Contains(canonical(t, raw), []byte(`"truncated":false`)) {
		t.Errorf("fixture must carry the truncation report explicitly as false, got: %s", raw)
	}

	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if env.Type != TypeModelAnnounced {
		t.Errorf("Type: got %q, want %q", env.Type, TypeModelAnnounced)
	}

	var payload ModelAnnouncedPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload.ConversationID != "" {
		t.Errorf("ConversationID: got %q, want %q", payload.ConversationID, "")
	}
	if payload.Model != "" {
		t.Errorf("Model: got %q, want %q", payload.Model, "")
	}
	if payload.Truncated {
		t.Errorf("Truncated: got %v, want false", payload.Truncated)
	}

	roundTripEnvelope(t, env, payload, raw)
}

// TestModelAnnouncedType_IsNotClaudesSubtype pins the translation layer this
// frame exists to preserve, as its thinking_progress and rate_limited siblings
// above do. The daemon is the ONE place a claude rename lands; naming the wire
// type after claude's own `init` subtype would undo that.
//
// Neither sibling's exact form transfers. claude's KEY for the value is `model`,
// so a strings.Contains(TypeModelAnnounced, "model") check would be red against
// the correct name — `model` here is the subject noun and the daemon's own field
// name. The discriminating word is claude's subtype `init`, which names claude's
// LINE where ours names what the daemon reports.
func TestModelAnnouncedType_IsNotClaudesSubtype(t *testing.T) {
	if TypeModelAnnounced == "init" {
		t.Errorf("wire type is claude's subtype %q; it must be the daemon's own name", TypeModelAnnounced)
	}
	if strings.Contains(TypeModelAnnounced, "init") {
		t.Errorf("wire type %q is derived from claude's subtype (contains %q)", TypeModelAnnounced, "init")
	}
	// The exact pin, matching internal/turnevent's variant name (ModelAnnounced)
	// in snake_case rather than anything of claude's.
	if TypeModelAnnounced != "model_announced" {
		t.Errorf("wire type: got %q, want %q", TypeModelAnnounced, "model_announced")
	}

	// The payload's own bytes, not the envelope's — the envelope carries its own
	// id/ts and would dilute the check. These are regression pins: non-discriminating
	// today by construction, their job is to go red the day someone "helpfully"
	// adds claude's init-line keys back. The captured init line carries 22 keys and
	// this payload carries the substance of one.
	body, err := json.Marshal(ModelAnnouncedPayload{
		ConversationID: "c1",
		Model:          "claude-haiku-4-5-20251001",
		Truncated:      true,
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	for _, key := range []string{"cwd", "session_id", "tools", "mcp_servers", "permissionMode", "slash_commands"} {
		if bytes.Contains(body, []byte(key)) {
			t.Errorf("payload carries claude's excluded init-line key %q: %s", key, body)
		}
	}
}

// TestModelListPayload_NilModelsNormalises covers the case no fixture could —
// and here that is the ONLY path there is. Unmarshalling "models":[] always
// yields a non-nil empty slice, so the nil branch is reachable only by
// constructing the value directly, and this slice (#1704) ships no fixture at all
// (internal/protocol/testdata/ is #1705's). A producer (#1693) mapping an empty
// or absent claude models array would hand this type a nil slice and, without
// normalisation, would ship "models":null to a phone.
//
// Both the value and pointer forms are checked because a pointer-receiver
// marshaller would silently miss the value path roundTripEnvelope takes. This is
// TestBackgroundTaskRosterPayload_NilTasksNormalises's shape.
func TestModelListPayload_NilModelsNormalises(t *testing.T) {
	p := ModelListPayload{ConversationID: "c1"}
	if p.Models != nil {
		t.Fatalf("precondition: Models must be nil, got %v", p.Models)
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
			if bytes.Contains(out, []byte(`"models":null`)) {
				t.Errorf("nil Models marshalled to null: %s", out)
			}
			if !bytes.Contains(out, []byte(`"models":[]`)) {
				t.Errorf("nil Models did not normalise to []: %s", out)
			}
		})
	}

	// The normalisation must not mutate the receiver's copy back into the caller.
	if p.Models != nil {
		t.Errorf("MarshalJSON mutated the receiver: Models is now %v", p.Models)
	}
}

// TestModelOption_NilSliceEncodings pins the asymmetry between the entry's two
// list fields, which no fixture could pin either: decoding [] always yields a
// non-nil slice, and this slice ships no fixture at all. EffortLevels normalises
// nil to [], TruncatedFields does not and stays null.
//
// truncated_fields is pinned at all precisely BECAUSE it ships no fixture — the
// route BackgroundTaskRosterPayload's second roster entry takes is not open here.
// An unpinned null is one that a later omitempty, or a third normaliser copied
// from the field above it, silently turns into something else.
//
// The nested-in-payload subtest proves the entry marshaller fires through the
// payload's, and its trailing assertion is the only thing that would catch a
// payload marshaller normalising entries IN PLACE — that would reach through
// p.Models[i] into the caller's backing array.
func TestModelOption_NilSliceEncodings(t *testing.T) {
	o := ModelOption{Value: "sonnet"}
	if o.EffortLevels != nil || o.TruncatedFields != nil {
		t.Fatalf("precondition: both slices must be nil, got %v / %v", o.EffortLevels, o.TruncatedFields)
	}

	assertEncodings := func(t *testing.T, out []byte) {
		t.Helper()
		if bytes.Contains(out, []byte(`"effort_levels":null`)) {
			t.Errorf("nil EffortLevels marshalled to null: %s", out)
		}
		if !bytes.Contains(out, []byte(`"effort_levels":[]`)) {
			t.Errorf("nil EffortLevels did not normalise to []: %s", out)
		}
		if !bytes.Contains(out, []byte(`"truncated_fields":null`)) {
			t.Errorf("nil TruncatedFields must stay null, not normalise: %s", out)
		}
	}

	for _, tc := range []struct {
		name string
		in   any
	}{
		{"value", o},
		{"pointer", &o},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := json.Marshal(tc.in)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			assertEncodings(t, out)
		})
	}

	t.Run("nested in payload", func(t *testing.T) {
		p := ModelListPayload{ConversationID: "c1", Models: []ModelOption{o}}
		out, err := json.Marshal(p)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		assertEncodings(t, out)
		if p.Models[0].EffortLevels != nil {
			t.Errorf("payload marshalling mutated the caller's backing array: EffortLevels is now %v", p.Models[0].EffortLevels)
		}
	})

	// The entry's own normalisation must not write back through the receiver.
	if o.EffortLevels != nil {
		t.Errorf("MarshalJSON mutated the receiver: EffortLevels is now %v", o.EffortLevels)
	}
}

// TestModelListType_IsNotClaudesVocabulary pins the translation layer this frame
// exists to preserve, as its thinking_progress, rate_limited and model_announced
// siblings above do. The daemon is the ONE place a claude rename lands; naming
// the wire type after claude's own `initialize` subtype, or after its `models`
// array key, would undo that.
//
// The model_announced sibling's trap is this frame's too, and sharper here: a
// strings.Contains(TypeModelList, "model") check would be RED against the correct
// name — "model" is this frame's subject noun and the daemon's own word. The
// discriminating words are claude's subtype "init" and its array key "models",
// and "model_list" contains neither.
//
// The exact-equality pin is the half that fails a WRONG name rather than merely a
// claude-derived one: the negative checks alone leave every other wrong name
// green.
func TestModelListType_IsNotClaudesVocabulary(t *testing.T) {
	if TypeModelList == "initialize" {
		t.Errorf("wire type is claude's control_request subtype %q; it must be the daemon's own name", TypeModelList)
	}
	if strings.Contains(TypeModelList, "init") {
		t.Errorf("wire type %q is derived from claude's subtype (contains %q)", TypeModelList, "init")
	}
	if strings.Contains(TypeModelList, "models") {
		t.Errorf("wire type %q is derived from claude's array key (contains %q)", TypeModelList, "models")
	}
	// The exact pin, naming what the frame IS to a client rather than anything of
	// claude's.
	if TypeModelList != "model_list" {
		t.Errorf("wire type: got %q, want %q", TypeModelList, "model_list")
	}

	// The payload's own bytes, not the envelope's — the envelope carries its own
	// id/ts and would dilute the check. These are regression pins: non-discriminating
	// today by construction, their job is to go red the day someone wires claude's
	// camelCase entry keys back in or adds a field this shape deliberately drops.
	// "models" is NOT in the list and must not be: it is this payload's own wire
	// key, so checking for it would be red against the correct shape.
	//
	// Value is opus[1m] deliberately — one of the two measured values
	// internal/relay's validModel rejects — so this fixture-free test carries the
	// hazard ModelOption's Value paragraph describes.
	body, err := json.Marshal(ModelListPayload{
		ConversationID: "c1",
		Models: []ModelOption{{
			ResolvedModel:    "claude-opus-4-5-20251101",
			Value:            "opus[1m]",
			DisplayName:      "Opus (1M context)",
			EffortLevels:     []string{"low", "medium", "high", "xhigh", "max"},
			SupportsAutoMode: true,
		}},
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	for _, key := range []string{
		"resolvedModel", "displayName", "supportsEffort",
		"supportedEffortLevels", "supportsAutoMode", "supportsFastMode",
		"description",
	} {
		if bytes.Contains(body, []byte(key)) {
			t.Errorf("payload carries claude's entry key or a deliberately-dropped field %q: %s", key, body)
		}
	}
}

// TestModelListPayload_RoundTrip pins the populated menu's bytes: the five rows
// claude 2.1.220 returned from a control_request with subtype initialize,
// measured 2026-08-21, in claude's own order.
//
// Haiku's row is the load-bearing one. claude's reply OMITS supportsAutoMode and
// supportedEffortLevels on that entry, and this wire states ONE position for
// absent and empty (ModelOption.MarshalJSON's rationale), so the row carries
// false and [] rather than an elided key. An empty effort list is a real shape a
// client meets on the first frame it ever decodes.
//
// Three values here are CHOSEN rather than captured, in
// TestModelAnnouncedPayload_RoundTrip's register — a fixture is something a client
// author copies as if it were observed, so the parts that were not observed have
// to say so:
//
//   - resolved_model. The 2026-08-21 measurement recorded displayName, value,
//     supportsAutoMode and supportedEffortLevels and no resolvedModel-shaped key,
//     so rows 1-4 carry rate_limited.json's "<unmeasured>" sentinel — whose angle
//     brackets double as the pin on Go's HTML escaping. Row 5 carries the
//     resolution of the `haiku` alias measured on the SAME claude version in the
//     committed capture internal/e2e/realclaude/testdata/dropped_lines_v2.1.220.json,
//     which is a turn announcement rather than an initialize reply. #1693 measures
//     the initialize reply's own per-entry keys and replaces the four sentinels.
//   - truncated_fields. Populated on row 3 and null on the other four —
//     background_task_roster.json's two-entry pattern. No measured value is
//     anywhere near a producer cap, so no real frame carries this row with this
//     report; the flag is chosen to discriminate a struct that drops or mis-wires
//     the field, and a cut `value` is the sharpest pairing available, being doubly
//     un-sendable.
//   - dropped_models. 2, non-zero so this fixture pins the value rather than the
//     zero encoding (background_task_roster.json's dropped_tasks: 3). Nothing
//     counts it yet: #1690 owns making the decode record it, #1693 is where the
//     field and a counter meet.
func TestModelListPayload_RoundTrip(t *testing.T) {
	raw := readFixture(t, "model_list.json")

	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if env.Type != TypeModelList {
		t.Errorf("Type: got %q, want %q", env.Type, TypeModelList)
	}

	var payload ModelListPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload.ConversationID != "c1" {
		t.Errorf("ConversationID: got %q, want %q", payload.ConversationID, "c1")
	}
	if len(payload.Models) != 5 {
		t.Fatalf("Models: got %d entries, want 5", len(payload.Models))
	}

	const allLevels = "low,medium,high,xhigh,max"
	rows := []struct {
		resolvedModel    string
		value            string
		displayName      string
		effortLevels     string // joined, the idiom the roster test uses
		supportsAutoMode bool
		truncatedFields  []string // nil where the row reports no cut
	}{
		{"<unmeasured>", "default", "Default (recommended)", allLevels, true, nil},
		{"<unmeasured>", "opus[1m]", "Opus (1M context)", allLevels, true, nil},
		{"<unmeasured>", "claude-fable-5[1m]", "Fable", allLevels, true, []string{"value"}},
		{"<unmeasured>", "sonnet", "Sonnet", allLevels, true, nil},
		{"claude-haiku-4-5-20251001", "haiku", "Haiku", "", false, nil},
	}
	for i, want := range rows {
		got := payload.Models[i]
		t.Run(want.value, func(t *testing.T) {
			if got.ResolvedModel != want.resolvedModel {
				t.Errorf("ResolvedModel: got %q, want %q", got.ResolvedModel, want.resolvedModel)
			}
			if got.Value != want.value {
				t.Errorf("Value: got %q, want %q", got.Value, want.value)
			}
			if got.DisplayName != want.displayName {
				t.Errorf("DisplayName: got %q, want %q", got.DisplayName, want.displayName)
			}
			if joined := strings.Join(got.EffortLevels, ","); joined != want.effortLevels {
				t.Errorf("EffortLevels: got %q, want %q", joined, want.effortLevels)
			}
			if got.SupportsAutoMode != want.supportsAutoMode {
				t.Errorf("SupportsAutoMode: got %v, want %v", got.SupportsAutoMode, want.supportsAutoMode)
			}
			// Both forms of the per-entry truncation report are pinned across the
			// five rows: populated on one, null on the rest. truncated_fields is
			// deliberately NOT normalised the way effort_levels is — nil and []
			// say the identical thing here.
			if want.truncatedFields == nil && got.TruncatedFields != nil {
				t.Errorf("TruncatedFields: got %v, want nil", got.TruncatedFields)
			}
			if joined, wantJoined := strings.Join(got.TruncatedFields, ","), strings.Join(want.truncatedFields, ","); joined != wantJoined {
				t.Errorf("TruncatedFields: got %q, want %q", joined, wantJoined)
			}
		})
	}

	// The count dimension, decided at the menu level and distinct from any row's
	// text cut. Nothing counts it yet, so a client must not read
	// len(models) + dropped_models as the menu's true size today.
	if payload.DroppedModels != 2 {
		t.Errorf("DroppedModels: got %d, want 2", payload.DroppedModels)
	}

	roundTripEnvelope(t, env, payload, raw)
}

// TestModelListPayload_Empty_RoundTrip pins the frame carrying no models at all,
// on background_task_roster_empty.json's shape.
//
// The opening byte guard is that sibling's own device and is load-bearing rather
// than decoration: without it an omitempty on Models would elide the key from
// BOTH the fixture and the re-marshalled bytes, and the round trip alone would go
// on passing. An empty menu is a positive statement rather than an absence, which
// is why the key is present as [] — see ModelListPayload.MarshalJSON.
func TestModelListPayload_Empty_RoundTrip(t *testing.T) {
	raw := readFixture(t, "model_list_empty.json")

	if !bytes.Contains(canonical(t, raw), []byte(`"models":[]`)) {
		t.Errorf("fixture must carry the models key as an empty array, got: %s", raw)
	}

	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if env.Type != TypeModelList {
		t.Errorf("Type: got %q, want %q", env.Type, TypeModelList)
	}

	var payload ModelListPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload.ConversationID != "c1" {
		t.Errorf("ConversationID: got %q, want %q", payload.ConversationID, "c1")
	}
	if len(payload.Models) != 0 {
		t.Errorf("Models: got %d entries, want 0", len(payload.Models))
	}
	if payload.DroppedModels != 0 {
		t.Errorf("DroppedModels: got %d, want 0", payload.DroppedModels)
	}

	roundTripEnvelope(t, env, payload, raw)
}

// TestModelListPayload_ZeroValue_RoundTrip pins the encoding of every field's zero
// value across BOTH types, which is what this stream's no-omitempty rule
// (docs/protocol-mobile.md § Interactive events) actually asserts.
//
// It is the fixture that carries most of the wire: five of the nine keys are
// reachable only here, and models carries exactly one entry precisely because an
// empty list cannot reach ModelOption's keys at all. The asymmetry inside that
// entry — effort_levels [] beside truncated_fields null — is the two marshallers'
// decided positions rather than a typo; ModelOption.MarshalJSON says why the two
// list fields differ.
//
// The six byte guards are what make "explicit zero, not elided" checkable at all,
// exactly as TestModelAnnouncedPayload_ZeroValue_RoundTrip's two are: an
// omitempty on any of those keys would elide it from both sides and the round
// trip would go on passing.
//
// The frame is one no producer will ever emit — a real menu names a conversation
// and its rows carry identifiers. It exists for the encoding, not the scenario.
func TestModelListPayload_ZeroValue_RoundTrip(t *testing.T) {
	raw := readFixture(t, "model_list_zero.json")

	// Two payload keys and four entry keys: the six whose zero value an omitempty
	// would elide. The three list keys are covered by the constructed-value tests
	// above, which is the only route that reaches a nil slice at all.
	for _, want := range []string{
		`"conversation_id":""`,
		`"dropped_models":0`,
		`"resolved_model":""`,
		`"value":""`,
		`"display_name":""`,
		`"supports_auto_mode":false`,
	} {
		if !bytes.Contains(canonical(t, raw), []byte(want)) {
			t.Errorf("fixture must carry %s explicitly at its zero value, got: %s", want, raw)
		}
	}

	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if env.Type != TypeModelList {
		t.Errorf("Type: got %q, want %q", env.Type, TypeModelList)
	}

	var payload ModelListPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload.ConversationID != "" {
		t.Errorf("ConversationID: got %q, want %q", payload.ConversationID, "")
	}
	if payload.DroppedModels != 0 {
		t.Errorf("DroppedModels: got %d, want 0", payload.DroppedModels)
	}
	if len(payload.Models) != 1 {
		t.Fatalf("Models: got %d entries, want 1", len(payload.Models))
	}

	entry := payload.Models[0]
	if entry.ResolvedModel != "" {
		t.Errorf("ResolvedModel: got %q, want %q", entry.ResolvedModel, "")
	}
	if entry.Value != "" {
		t.Errorf("Value: got %q, want %q", entry.Value, "")
	}
	if entry.DisplayName != "" {
		t.Errorf("DisplayName: got %q, want %q", entry.DisplayName, "")
	}
	if entry.SupportsAutoMode {
		t.Errorf("SupportsAutoMode: got %v, want false", entry.SupportsAutoMode)
	}
	// Decoding [] yields a non-nil empty slice while null yields nil, so this pair
	// pins the fixture's asymmetry on the decode side too.
	if entry.EffortLevels == nil || len(entry.EffortLevels) != 0 {
		t.Errorf("EffortLevels: got %v, want an empty non-nil slice", entry.EffortLevels)
	}
	if entry.TruncatedFields != nil {
		t.Errorf("TruncatedFields: got %v, want nil", entry.TruncatedFields)
	}

	roundTripEnvelope(t, env, payload, raw)
}

// maxV2AppEnvelope is the Mobile Protocol v2 application-envelope size cap
// (docs/protocol-mobile.md § Application-envelope size cap). Test-local on
// purpose: nothing in internal/protocol enforces the cap — the transport does —
// so an exported constant here would imply an enforcement this package does not
// perform.
const maxV2AppEnvelope = 65519

// TestToolUsePayload_NilInputNormalises covers the case the fixture cannot:
// unmarshalling "input":{} yields a non-nil empty map, so the round-trip never
// exercises the nil path. The bridge (#1678) returns nil for all three
// no-fields cases — an absent input, an empty object, and an input that is not
// a JSON object — and would take exactly that path; without normalisation it
// would ship "input":null to a client while the fixture kept asserting {}.
// Both the value and pointer forms are checked because a pointer-receiver
// marshaller would silently miss the value path roundTripEnvelope takes. This
// is TestBackgroundTaskRosterPayload_NilTasksNormalises's shape, for a map.
func TestToolUsePayload_NilInputNormalises(t *testing.T) {
	p := ToolUsePayload{ConversationID: "c1", Name: "Bash"}
	if p.Input != nil {
		t.Fatalf("precondition: Input must be nil, got %v", p.Input)
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
			if bytes.Contains(out, []byte(`"input":null`)) {
				t.Errorf("nil Input marshalled to null: %s", out)
			}
			if !bytes.Contains(out, []byte(`"input":{}`)) {
				t.Errorf("nil Input did not normalise to {}: %s", out)
			}
		})
	}

	// The normalisation must not mutate the receiver's copy back into the caller.
	if p.Input != nil {
		t.Errorf("MarshalJSON mutated the receiver: Input is now %v", p.Input)
	}
}

// The bridge's tool-input caps, mirrored from internal/turnbridge/outbound.go.
// Copies for the same reason the streamsup block below carries copies — this
// package is a stdlib-only leaf and must not import internal/turnbridge — and
// each names its source constant, which is what a future cap change greps for.
// Raising a bridge cap without updating these leaves the measurement silently
// stale.
const (
	capInputValueRunes = 4000 // internal/turnbridge.maxInputValueRunes
	capInputKeyRunes   = 128  // internal/turnbridge.maxInputKeyRunes
	capInputFields     = 16   // internal/turnbridge.maxInputFields
	capInputTotalRunes = 8500 // internal/turnbridge.maxInputTotalRunes
	capSummaryLen      = 200  // internal/turnbridge.maxSummaryLen
)

// TestToolUsePayload_FitV2EnvelopeCap constructs a tool_use with its input map
// filled to the bridge's total rune budget across its full entry count, and
// proves the serialised envelope fits under the v2 application-envelope cap.
// Per-field caps do not compose into an envelope guarantee on their own, so
// this is measured rather than argued — the same statement
// TestBackgroundTaskPayloads_FitV2EnvelopeCap below makes, and this test is
// that one's shape.
//
// The fill is '<', not 'a', for the reason stated at length on that test:
// SetEscapeHTML is on by default so one such input byte costs six on the wire,
// and an 'a' fill under-reports by over 5x. Here the producer's cut is a RUNE
// cut rather than a byte cut, and six bytes per rune is still the ceiling — a
// 1-byte rune escapes to at most 6, a multi-byte rune is emitted RAW at 4 bytes
// or fewer. '<' is the realistic case too: a Bash command value IS a shell
// command line, and the blocked consumer is a desktop client whose own source
// is '<'-dense TSX.
//
// Every value also carries the trailing "…" the bridge appends to a shortened
// value, because the rune budget bounds PRE-ellipsis content and the ellipsis
// rides on top of it.
//
// The identity fields are filled hostilely because nothing bounds them, which
// makes this an assumption rather than an enforced cap and is worth saying out
// loud: streamsup's emitAssistant passes claude's tool Name and ID through
// VERBATIM with no cap, and the bridge supplies conversation_id / turn_id.
// name gets 512 runes — four times the others' paranoia — because a fat MCP
// tool name is the plausible way that field grows, and budgeting for it is the
// difference between a measured guarantee and a tacit assumption. Capping name
// upstream is a separate ticket (#1678 § Open questions).
func TestToolUsePayload_FitV2EnvelopeCap(t *testing.T) {
	fill := func(n int) string { return strings.Repeat("<", n) }

	// Keys are filled to their own cap and made distinct by a numeric suffix
	// (digits do not escape, so this is the hostile shape a map of maximal keys
	// takes). Keys count against the same budget as values, so what remains is
	// spread across the values.
	keyRunes := capInputFields * capInputKeyRunes
	valueRunes := capInputTotalRunes - keyRunes
	if valueRunes <= 0 {
		t.Fatalf("precondition: keys alone (%d runes) exhaust the %d-rune budget", keyRunes, capInputTotalRunes)
	}
	input := make(map[string]string, capInputFields)
	for i := 0; i < capInputFields; i++ {
		per := valueRunes / capInputFields
		if i == 0 {
			per += valueRunes % capInputFields // the remainder rides on one entry
		}
		if per > capInputValueRunes {
			t.Fatalf("precondition: per-entry fill %d exceeds the %d-rune value cap", per, capInputValueRunes)
		}
		// The suffix keeps the key distinct; it is inside the key cap, not on top.
		key := fill(capInputKeyRunes-2) + fmt.Sprintf("%02d", i)
		input[key] = fill(per) + "…"
	}

	payload := ToolUsePayload{
		ConversationID: fill(64),
		TurnID:         fill(64),
		ToolUseID:      fill(64),
		Name:           fill(512),
		InputSummary:   fill(capSummaryLen) + "…",
		Input:          input,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	// Worst-case envelope too: max-uint64 ids and a populated EventID, so the
	// outer frame costs as much as it ever can.
	eventID := ^uint64(0)
	env := Envelope{
		ID:      ^uint64(0),
		Type:    TypeToolUse,
		TS:      time.Date(2026, 5, 8, 10, 33, 18, 0, time.UTC),
		Payload: body,
		EventID: &eventID,
	}
	out, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	t.Logf("tool_use at full caps: %d B, %.1f%% of the %d-byte v2 application-envelope cap",
		len(out), float64(len(out))/float64(maxV2AppEnvelope)*100, maxV2AppEnvelope)
	if len(out) >= maxV2AppEnvelope {
		t.Errorf("serialised envelope: got %d B, want < %d B", len(out), maxV2AppEnvelope)
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
