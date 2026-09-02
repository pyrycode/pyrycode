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
// (internal/protocol/testdata/ is #1705's). The producer (#1848) builds its outer
// slice by appending into a nil one, so an empty or absent claude models array
// hands this type a nil slice and, without normalisation, would ship
// "models":null to a phone.
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
	// Value is opus[1m] deliberately — the bracketed variant form, whose bytes
	// are the ones an encoding probe is most likely to mangle, and the shape
	// internal/relay's validModel was widened for at #1838.
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
//     which is a turn announcement rather than an initialize reply. The initialize
//     reply's own per-entry keys have not been measured, and no shipped slice owns
//     doing it — the four sentinels still stand in the fixture, where Go's encoder
//     stores their angle brackets \u-escaped, so grep them as unmeasured rather
//     than as <unmeasured>.
//   - truncated_fields. Populated on row 3 and null on the other four —
//     background_task_roster.json's two-entry pattern. No measured value is
//     anywhere near a producer cap, so no real frame carries this row with this
//     report; the flag is chosen to discriminate a struct that drops or mis-wires
//     the field, and a cut `value` is the sharpest pairing available, being doubly
//     un-sendable.
//   - dropped_models. 2, non-zero so this fixture pins the value rather than the
//     zero encoding (background_task_roster.json's dropped_tasks: 3). The decode
//     counts it since #1812 (turnevent.ModelList.DroppedModels), and #1848 is where
//     the field and that counter meet — MapEvent's arm carries the count through
//     verbatim rather than recomputing it.
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
	// text cut. #1848 fills this field from what the decode counted and #1849 puts
	// the frame on the wire, so len(models) + dropped_models IS the menu's true
	// size on a real frame today.
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

// TestSlashCommandListPayload_NilCommandsNormalises covers the case no fixture
// could — and here that is the ONLY path there is. Unmarshalling "commands":[]
// always yields a non-nil empty slice, so the nil branch is reachable only by
// constructing the value directly: slash_command_list_empty.json carries the key
// as [] and decodes to an empty non-nil slice, never to nil. A producer (#1720)
// mapping an empty
// or absent claude commands array would hand this type a nil slice and, without
// normalisation, would ship "commands":null to a phone.
//
// Both the value and pointer forms are checked because a pointer-receiver
// marshaller would silently miss the value path roundTripEnvelope takes. This is
// TestModelListPayload_NilModelsNormalises's shape.
func TestSlashCommandListPayload_NilCommandsNormalises(t *testing.T) {
	p := SlashCommandListPayload{ConversationID: "c1"}
	if p.Commands != nil {
		t.Fatalf("precondition: Commands must be nil, got %v", p.Commands)
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
			if bytes.Contains(out, []byte(`"commands":null`)) {
				t.Errorf("nil Commands marshalled to null: %s", out)
			}
			if !bytes.Contains(out, []byte(`"commands":[]`)) {
				t.Errorf("nil Commands did not normalise to []: %s", out)
			}
		})
	}

	// The normalisation must not mutate the receiver's copy back into the caller.
	if p.Commands != nil {
		t.Errorf("MarshalJSON mutated the receiver: Commands is now %v", p.Commands)
	}
}

// TestSlashCommand_NilSliceEncodings pins the asymmetry between the entry's two
// list fields at the one value no fixture can reach: decoding [] always yields a
// non-nil slice, so only a constructed value is nil here. Aliases normalises nil
// to [], TruncatedFields does not and stays null.
//
// The fixtures (#1718) now pin "truncated_fields":null in the wire bytes, and
// this test survives that because it reaches what they cannot: the value form,
// the pointer form, the nested-in-payload form, and the caller's-backing-array
// assertion below. An unpinned nil is one that a later omitempty, or a third
// normaliser copied from the field above it, silently turns into something else.
//
// The nested-in-payload subtest proves the entry marshaller fires through the
// payload's, and its trailing assertion is the only thing that would catch a
// payload marshaller normalising entries IN PLACE — that would reach through
// p.Commands[i] into the caller's backing array, and its JSON is byte-identical
// to the correct implementation's, so no byte check could tell the two apart.
//
// This is TestModelOption_NilSliceEncodings's shape, shared assertEncodings
// helper included, so the three assertions are stated once.
func TestSlashCommand_NilSliceEncodings(t *testing.T) {
	c := SlashCommand{Name: "clear"}
	if c.Aliases != nil || c.TruncatedFields != nil {
		t.Fatalf("precondition: both slices must be nil, got %v / %v", c.Aliases, c.TruncatedFields)
	}

	assertEncodings := func(t *testing.T, out []byte) {
		t.Helper()
		if bytes.Contains(out, []byte(`"aliases":null`)) {
			t.Errorf("nil Aliases marshalled to null: %s", out)
		}
		if !bytes.Contains(out, []byte(`"aliases":[]`)) {
			t.Errorf("nil Aliases did not normalise to []: %s", out)
		}
		if !bytes.Contains(out, []byte(`"truncated_fields":null`)) {
			t.Errorf("nil TruncatedFields must stay null, not normalise: %s", out)
		}
	}

	for _, tc := range []struct {
		name string
		in   any
	}{
		{"value", c},
		{"pointer", &c},
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
		p := SlashCommandListPayload{ConversationID: "c1", Commands: []SlashCommand{c}}
		out, err := json.Marshal(p)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		assertEncodings(t, out)
		if p.Commands[0].Aliases != nil {
			t.Errorf("payload marshalling mutated the caller's backing array: Aliases is now %v", p.Commands[0].Aliases)
		}
	})

	// The entry's own normalisation must not write back through the receiver.
	if c.Aliases != nil {
		t.Errorf("MarshalJSON mutated the receiver: Aliases is now %v", c.Aliases)
	}
}

// TestSlashCommandListType_IsNotClaudesVocabulary pins the translation layer
// this frame exists to preserve, as its rate_limited, model_announced and
// model_list siblings do. The daemon is the ONE place a claude rename lands;
// naming the wire type after claude's own vocabulary would undo that.
//
// claude has FOUR words on this path, not the two model_list had: the
// control_request subtype `initialize`, the control reply's array key
// `commands`, the system/init stdout line's `slash_commands`, and that same
// line's `terminal_slash_commands`. Only two of them appear in code below, and
// the reason is the containment lattice:
//
// The SINGULAR subject nouns cannot be checks at all. `command`,
// `slash_command` and `slash` are each a substring of the correct name, so a
// strings.Contains on any of the three would be RED against it — the trap
// TestModelAnnouncedType_IsNotClaudesSubtype records for `model` and
// TestModelListType_IsNotClaudesVocabulary for `models`, three words wide here
// instead of one. claude's keys differ from this frame's subject noun by a
// trailing s, so the discriminating checks are the PLURALS.
//
// One plural check covers all three. `commands` is a substring of
// `slash_commands`, which is a substring of `terminal_slash_commands`, so a name
// derived from either longer key necessarily contains the shorter one. Neither
// longer check could be sole-red for anything; adding them would be
// documentation rather than coverage, and this comment is the documentation.
//
// The `initialize` equality check is likewise subsumed — any name equal to it
// also contains `init` — and is kept anyway, as all three sibling pins keep
// theirs: it is the named statement of the one wrong name a reader would most
// plausibly reach for, so its redundancy is deliberate rather than an oversight.
//
// The exact-equality pin is the half that fails a WRONG name rather than merely
// a claude-derived one: the negative checks alone leave every other wrong name
// green. Naming is this ticket's (#1726) whole deliverable and nothing
// downstream supplies the string, so the pin is load-bearing.
//
// The payload-bytes half below arrived with the shape (#1727), which is what
// every sibling pin ends with. It checks that a populated payload's bytes carry
// none of claude's spellings this shape does NOT adopt, and it is
// non-discriminating today by construction: its job is to go red the day someone
// wires claude's own spellings, or the names-only twin, back in.
func TestSlashCommandListType_IsNotClaudesVocabulary(t *testing.T) {
	if TypeSlashCommandList == "initialize" {
		t.Errorf("wire type is claude's control_request subtype %q; it must be the daemon's own name", TypeSlashCommandList)
	}
	if strings.Contains(TypeSlashCommandList, "init") {
		t.Errorf("wire type %q is derived from claude's subtype (contains %q)", TypeSlashCommandList, "init")
	}
	if strings.Contains(TypeSlashCommandList, "commands") {
		t.Errorf("wire type %q is derived from claude's array key (contains %q)", TypeSlashCommandList, "commands")
	}
	// The exact pin, naming what the frame IS to a client rather than anything of
	// claude's.
	if TypeSlashCommandList != "slash_command_list" {
		t.Errorf("wire type: got %q, want %q", TypeSlashCommandList, "slash_command_list")
	}

	// The payload's own bytes, not the envelope's — the envelope carries its own
	// id/ts and would dilute the check.
	//
	// The check list is DERIVED, not copied, and two derivations are why it is so
	// short. First, this shape adopts all FOUR of claude's per-entry keys, so
	// "name", "description" and "aliases" would be red against the correct struct
	// and must not be checked; only argumentHint differs from what this wire
	// spells (argument_hint), so it is the one per-entry spelling a check can
	// name. Do NOT copy TestModelListType_IsNotClaudesVocabulary's list — it
	// contains "description", which this shape adopts, so carrying it over
	// verbatim would ship a check that fails against the correct implementation.
	// For the same reason there is no deliberately-dropped-field half here, unlike
	// that sibling's: this shape drops nothing.
	//
	// Second, "commands" is absent because it is this payload's own wire key — the
	// same trap the sibling records for "models". Of claude's two array keys on
	// this path, slash_commands is the load-bearing check: terminal_slash_commands
	// is subsumed by it in the byte-containment direction, since any bytes
	// containing the longer contain the shorter. The longer one is kept as the
	// named statement of claude's fourth word, the same deliberate redundancy the
	// `initialize` equality check above is.
	//
	// The row is clear's deliberately: an empty argument hint (the ordinary case,
	// 33 of the capture's 51 entries) and the aliases whose first member, reset,
	// is the desktop Actions menu's own entry.
	body, err := json.Marshal(SlashCommandListPayload{
		ConversationID: "c1",
		Commands: []SlashCommand{{
			Name:         "clear",
			ArgumentHint: "",
			Description:  "Clear conversation history and free up context",
			Aliases:      []string{"reset", "new"},
		}},
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	for _, key := range []string{
		"argumentHint", "slash_commands", "terminal_slash_commands",
	} {
		if bytes.Contains(body, []byte(key)) {
			t.Errorf("payload carries claude's spelling %q: %s", key, body)
		}
	}
}

// TestSlashCommandListPayload_RoundTrip pins the populated menu's bytes: five
// rows drawn from the 51-entry array claude 2.1.239 returned from a
// control_request with subtype initialize (the committed capture
// internal/e2e/realclaude/testdata/initialize_control_v2.1.239.json,
// re-measured 2026-08-24), in claude's own capture order.
//
// The five are a SAMPLE, not the whole array and not evidence of any cap. They
// are chosen to cover both of the capture's key sets — 42 of 51 carry name,
// description and argumentHint, the other 9 add aliases, and there is no third
// — and the four properties a client otherwise gets wrong: clear carries the
// aliases whose reset is the desktop Actions menu's own entry (a name-only
// matcher greys out a command that works), config carries a single-element
// alias array beside clear's two, model's <model> hint is the pin on Go's HTML
// escaping, usage pairs aliases with an empty hint, and claude-api carries the
// only sub-0x20 byte on this path — 0x0a — beside raw non-ASCII em dashes that
// the same encoder passes through unescaped.
//
// Two values are CHOSEN rather than captured, in TestModelListPayload_RoundTrip's
// register — a fixture is something a client author copies as if it were
// observed, so the parts that were not observed have to say so:
//
//   - "aliases":[] on rows 1 and 4. claude never sends an empty alias array:
//     zero of the capture's 51 entries carry one and 42 omit the key. [] is the
//     position SlashCommand.MarshalJSON decided for both, so it is what the wire
//     states and what a fixture has to show.
//   - truncated_fields on row 1. Nothing cut that description; the fixture
//     carries it at its full measured length. The flag is chosen to discriminate
//     a struct that drops or mis-wires the field, and claude-api is the sharpest
//     pairing available — 1078 bytes against the capture's 69-byte median, in the
//     10-of-51 population a per-field bound cuts first. Populated on one row and
//     null on the other four is model_list.json's own two-form pattern.
//   - dropped_commands. 2, non-zero so this fixture pins the value rather than
//     the zero encoding. Something counts it since #1826 — streamsup's
//     maxSlashCommandListEntries bounds the entry count and
//     turnevent.SlashCommandList.DroppedCommands carries the cut — but nothing
//     MAPS that onto this field, so the 2 here is still a fixture's number and
//     not a producer's. #1720 is where the field and that counter meet.
//
// Row 1's description is asserted by measured property rather than as an exact
// string — it is the only row too long for the table, and byte length, rune
// length, the newline count and a prefix pin it more legibly than 1078 bytes of
// literal would.
//
// An omitempty on name, argument_hint or description is red here as well as on
// the zero-value fixture: canonical is json.Compact only, so the round trip
// re-encodes through the struct and the elided key diverges the bytes.
func TestSlashCommandListPayload_RoundTrip(t *testing.T) {
	raw := readFixture(t, "slash_command_list.json")

	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if env.Type != TypeSlashCommandList {
		t.Errorf("Type: got %q, want %q", env.Type, TypeSlashCommandList)
	}

	var payload SlashCommandListPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload.ConversationID != "c1" {
		t.Errorf("ConversationID: got %q, want %q", payload.ConversationID, "c1")
	}
	if len(payload.Commands) != 5 {
		t.Fatalf("Commands: got %d entries, want 5", len(payload.Commands))
	}

	rows := []struct {
		name            string
		argumentHint    string
		description     string // empty where the row asserts by property below
		aliases         string // joined, the idiom the roster and model-list tests use
		truncatedFields []string
	}{
		{"claude-api", "", "", "", []string{"description"}},
		{"clear", "[name]", "Start a new session with empty context; previous session stays on disk (resumable with /resume)", "reset,new", nil},
		{"config", "key=value", "Set a setting by key", "settings", nil},
		{"model", "<model>", "Set the AI model for Claude Code", "", nil},
		{"usage", "", "Show session cost, plan usage, and what's contributing to your limits", "cost,stats", nil},
	}
	for i, want := range rows {
		got := payload.Commands[i]
		t.Run(want.name, func(t *testing.T) {
			if got.Name != want.name {
				t.Errorf("Name: got %q, want %q", got.Name, want.name)
			}
			if got.ArgumentHint != want.argumentHint {
				t.Errorf("ArgumentHint: got %q, want %q", got.ArgumentHint, want.argumentHint)
			}
			if want.description != "" && got.Description != want.description {
				t.Errorf("Description: got %q, want %q", got.Description, want.description)
			}
			// Absent and empty are the same [] position (SlashCommand.MarshalJSON's
			// COLLAPSE), so rows 1 and 4 decode to an empty slice and join to "".
			if joined := strings.Join(got.Aliases, ","); joined != want.aliases {
				t.Errorf("Aliases: got %q, want %q", joined, want.aliases)
			}
			// Both forms of the per-entry truncation report are pinned across the
			// five rows: populated on one, null on the rest. truncated_fields is
			// deliberately NOT normalised the way aliases is — nil and [] say the
			// identical thing here.
			if want.truncatedFields == nil && got.TruncatedFields != nil {
				t.Errorf("TruncatedFields: got %v, want nil", got.TruncatedFields)
			}
			if joined, wantJoined := strings.Join(got.TruncatedFields, ","), strings.Join(want.truncatedFields, ","); joined != wantJoined {
				t.Errorf("TruncatedFields: got %q, want %q", joined, wantJoined)
			}
		})
	}

	// claude-api's description by measured property. The newline count is the
	// load-bearing one: 0x0a is the ONLY sub-0x20 byte anywhere across the 51
	// entries' four string fields, so a type-ahead row assuming one line per
	// description meets its counterexample on this frame. The byte/rune split is
	// the em dashes, which Go's encoder passes through as raw UTF-8.
	t.Run("claude-api description", func(t *testing.T) {
		d := payload.Commands[0].Description
		if len(d) != 1078 {
			t.Errorf("byte length: got %d, want 1078", len(d))
		}
		if runes := len([]rune(d)); runes != 1068 {
			t.Errorf("rune length: got %d, want 1068", runes)
		}
		if n := strings.Count(d, "\n"); n != 2 {
			t.Errorf("newline count: got %d, want 2", n)
		}
		const prefix = "Reference for the Claude API / Anthropic SDK — model ids, pricing,"
		if !strings.HasPrefix(d, prefix) {
			t.Errorf("prefix: got %q, want it to start with %q", d, prefix)
		}
	})

	// The count dimension, decided at the menu level and distinct from any row's
	// text cut. A producer counts it since #1826 (streamsup's
	// maxSlashCommandListEntries), but nothing maps that count onto this payload and
	// no frame of this type is produced at all, so a client still cannot read
	// len(commands) + dropped_commands as the menu's true size off any live frame —
	// #1720 is what makes that reading true.
	if payload.DroppedCommands != 2 {
		t.Errorf("DroppedCommands: got %d, want 2", payload.DroppedCommands)
	}

	roundTripEnvelope(t, env, payload, raw)
}

// TestSlashCommandListPayload_Empty_RoundTrip pins the frame carrying no
// commands at all, on TestModelListPayload_Empty_RoundTrip's shape.
//
// The opening byte guard is that sibling's own device and is load-bearing rather
// than decoration: without it an omitempty on Commands would elide the key from
// BOTH the fixture and the re-marshalled bytes, and the round trip alone would go
// on passing. An empty menu is a positive statement — claude offered nothing —
// rather than an absence, which is why the key is present as [] — see
// SlashCommandListPayload.MarshalJSON.
func TestSlashCommandListPayload_Empty_RoundTrip(t *testing.T) {
	raw := readFixture(t, "slash_command_list_empty.json")

	if !bytes.Contains(canonical(t, raw), []byte(`"commands":[]`)) {
		t.Errorf("fixture must carry the commands key as an empty array, got: %s", raw)
	}

	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if env.Type != TypeSlashCommandList {
		t.Errorf("Type: got %q, want %q", env.Type, TypeSlashCommandList)
	}

	var payload SlashCommandListPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload.ConversationID != "c1" {
		t.Errorf("ConversationID: got %q, want %q", payload.ConversationID, "c1")
	}
	if len(payload.Commands) != 0 {
		t.Errorf("Commands: got %d entries, want 0", len(payload.Commands))
	}
	if payload.DroppedCommands != 0 {
		t.Errorf("DroppedCommands: got %d, want 0", payload.DroppedCommands)
	}

	roundTripEnvelope(t, env, payload, raw)
}

// TestSlashCommandListPayload_ZeroValue_RoundTrip pins the encoding of every
// field's zero value across BOTH types, which is what this stream's no-omitempty
// rule (docs/protocol-mobile.md § Interactive events) actually asserts.
//
// It is the fixture that carries most of the wire: five of the eight keys are
// reachable only here, and commands carries exactly one entry precisely because
// an empty list cannot reach SlashCommand's keys at all. The asymmetry inside
// that entry — aliases [] beside truncated_fields null — is the two marshallers'
// decided positions rather than a typo; SlashCommand.MarshalJSON says why the
// two list fields differ.
//
// The five byte guards are what make "explicit zero, not elided" checkable at
// all, exactly as TestModelListPayload_ZeroValue_RoundTrip's six are. Measured by
// running each of the eight keys as an omitempty mutant over a scratch overlay:
// conversation_id, dropped_commands, name, argument_hint and description reach no
// other assertion in the tree, so this fixture is the only thing that reddens
// them; commands, aliases and truncated_fields are red here too and additionally
// under the constructed-value tests above, the only route that reaches a nil
// slice at all.
//
// The frame is one no producer will ever emit — a real menu names a conversation
// and its rows carry command names. It exists for the encoding, not the scenario.
func TestSlashCommandListPayload_ZeroValue_RoundTrip(t *testing.T) {
	raw := readFixture(t, "slash_command_list_zero.json")

	// Two payload keys and three entry keys: the five whose zero value an
	// omitempty would elide and which no other test reaches.
	for _, want := range []string{
		`"conversation_id":""`,
		`"dropped_commands":0`,
		`"name":""`,
		`"argument_hint":""`,
		`"description":""`,
	} {
		if !bytes.Contains(canonical(t, raw), []byte(want)) {
			t.Errorf("fixture must carry %s explicitly at its zero value, got: %s", want, raw)
		}
	}

	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if env.Type != TypeSlashCommandList {
		t.Errorf("Type: got %q, want %q", env.Type, TypeSlashCommandList)
	}

	var payload SlashCommandListPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload.ConversationID != "" {
		t.Errorf("ConversationID: got %q, want %q", payload.ConversationID, "")
	}
	if payload.DroppedCommands != 0 {
		t.Errorf("DroppedCommands: got %d, want 0", payload.DroppedCommands)
	}
	if len(payload.Commands) != 1 {
		t.Fatalf("Commands: got %d entries, want 1", len(payload.Commands))
	}

	entry := payload.Commands[0]
	if entry.Name != "" {
		t.Errorf("Name: got %q, want %q", entry.Name, "")
	}
	if entry.ArgumentHint != "" {
		t.Errorf("ArgumentHint: got %q, want %q", entry.ArgumentHint, "")
	}
	if entry.Description != "" {
		t.Errorf("Description: got %q, want %q", entry.Description, "")
	}
	// Decoding [] yields a non-nil empty slice while null yields nil, so this pair
	// pins the fixture's asymmetry on the decode side too.
	if entry.Aliases == nil || len(entry.Aliases) != 0 {
		t.Errorf("Aliases: got %v, want an empty non-nil slice", entry.Aliases)
	}
	if entry.TruncatedFields != nil {
		t.Errorf("TruncatedFields: got %v, want nil", entry.TruncatedFields)
	}

	roundTripEnvelope(t, env, payload, raw)
}

// TestQuestionShownType_IsNotClaudesVocabulary pins the translation layer this
// frame exists to preserve, as its rate_limited, model_announced, model_list and
// slash_command_list siblings do. The daemon is the ONE place a claude rename
// lands; naming the wire type after claude's own tool would undo that.
//
// claude's words on this path are the tool name AskUserQuestion and the tool_input
// keys questions, question, header, options and multiSelect (the committed capture
// internal/e2e/realclaude/testdata/ask_user_question_v2.1.239.json). Only two of
// them appear in code below, and the containment lattice is why.
//
// The SINGULAR subject noun cannot be a check at all. question is a substring of
// the correct name, so a strings.Contains on it would be RED against it — the trap
// TestSlashCommandListType_IsNotClaudesVocabulary records three words wide for
// command, slash_command and slash, and the model siblings record one word wide
// for the singular model. claude's array key differs from this frame's subject
// noun by a trailing s, so the discriminating check is the PLURAL: questions is
// not a substring of question_shown, which carries question_. That is also why the
// name is not questions_shown — that spelling would make the plural check red by
// construction.
//
// The ask check is NOT subsumed by the AskUserQuestion equality above it, unlike
// the redundancy the sibling pins keep deliberately. strings.Contains is
// case-sensitive, so the equality is GREEN against ask_user_question_shown — the
// most plausible wrong name — while the containment check is red. ask is the root
// every snake-cased derivation carries. One subtlety a future rename must
// re-check: ask is a substring of task, so this check is safe only because the
// correct name carries no task word.
//
// There is NO payload-bytes half here, which every sibling pin ends with. header,
// options and multiSelect are per-entry keys and belong to that half, and the
// shape they would be checked against does not exist until #1963; this ticket
// (#1962) is names-only. TestSlashCommandListType_IsNotClaudesVocabulary's own
// comment records that its half arrived with the shape (#1727), and this pin gains
// one the same way.
//
// The exact-equality pin is the half that fails a WRONG name rather than merely a
// claude-derived one: the negative checks alone leave every other wrong name
// green. Naming is this ticket's whole deliverable and nothing downstream supplies
// the string, so the pin is load-bearing.
func TestQuestionShownType_IsNotClaudesVocabulary(t *testing.T) {
	if TypeQuestionShown == "AskUserQuestion" {
		t.Errorf("wire type is claude's tool name %q; it must be the daemon's own name", TypeQuestionShown)
	}
	if strings.Contains(TypeQuestionShown, "ask") {
		t.Errorf("wire type %q is derived from claude's tool name (contains %q)", TypeQuestionShown, "ask")
	}
	if strings.Contains(TypeQuestionShown, "questions") {
		t.Errorf("wire type %q is derived from claude's array key (contains %q)", TypeQuestionShown, "questions")
	}
	// The exact pin, naming what the frame IS to a client rather than anything of
	// claude's.
	if TypeQuestionShown != "question_shown" {
		t.Errorf("wire type: got %q, want %q", TypeQuestionShown, "question_shown")
	}
}

// TestQuestionDismissedType_IsNotClaudesVocabulary pins the dismissal frame's
// name the way TestQuestionShownType_IsNotClaudesVocabulary pins its sibling's,
// and inherits that pin's containment lattice wholesale: the SINGULAR question is
// a substring of the correct name, so it cannot be a check, while the PLURAL
// questions (claude's array key) and ask (the root every snake-cased derivation
// of AskUserQuestion carries) both discriminate. ask is safe here for the reason
// recorded there and re-checked here rather than assumed — ask is a substring of
// task, and this name carries no task word.
//
// What is NEW is the modal check, and it is an EQUALITY rather than a
// containment. dismissed is shared with modal_dismissed, so a
// strings.Contains(…, "dismissed") would be RED against the correct name — the
// same shape of trap the singular subject noun poses one step over, arriving from
// the sibling frame instead of from claude.
//
// That equality is subsumed by the exact pin below it as a PREDICATE: anything
// equal to question_dismissed is already unequal to modal_dismissed. It is not
// subsumed as a standing bar, which is why it stays. A rename edits the exact
// pin's literal — that is what renaming means — while a check written against the
// other CONSTANT survives the edit and still refuses the one wrong answer that
// silently clears the wrong client panel. It also names that failure in its own
// message rather than reporting it as a generic got/want.
//
// Unlike the sibling pin, there is no claude-derived risk in the dismissal half
// of the name: claude's AskUserQuestion has no dismissal concept and contributes
// no vocabulary here. The two negative checks guard the question half, which this
// frame inherits from the family.
//
// There is no payload-bytes half. The shape lands in the same slice as the name
// (#1974), and its keys are pinned by TestQuestionDismissedPayload_RoundTrip and
// TestQuestionDismissedPayload_ZeroValue_KeysPresent rather than restated here.
func TestQuestionDismissedType_IsNotClaudesVocabulary(t *testing.T) {
	if TypeQuestionDismissed == "AskUserQuestion" {
		t.Errorf("wire type is claude's tool name %q; it must be the daemon's own name", TypeQuestionDismissed)
	}
	if strings.Contains(TypeQuestionDismissed, "ask") {
		t.Errorf("wire type %q is derived from claude's tool name (contains %q)", TypeQuestionDismissed, "ask")
	}
	if strings.Contains(TypeQuestionDismissed, "questions") {
		t.Errorf("wire type %q is derived from claude's array key (contains %q)", TypeQuestionDismissed, "questions")
	}
	// The dismissal must be its OWN type, not the modal frame's. A client routes
	// modal_dismissed to the modal panel, so a question batch arriving under that
	// name clears the wrong panel or none.
	if TypeQuestionDismissed == TypeModalDismissed {
		t.Errorf("wire type reuses the modal frame's name %q; the question dismissal is its own type", TypeModalDismissed)
	}
	// The exact pin, naming what the frame IS to a client.
	if TypeQuestionDismissed != "question_dismissed" {
		t.Errorf("wire type: got %q, want %q", TypeQuestionDismissed, "question_dismissed")
	}
}
