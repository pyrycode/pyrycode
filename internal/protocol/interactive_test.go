package protocol

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"strconv"
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
	if payload.ParentToolUseID != "toolu_delta_parent" {
		t.Errorf("ParentToolUseID: got %q, want %q", payload.ParentToolUseID, "toolu_delta_parent")
	}
	if payload.Text != "Let me check the weather for you." {
		t.Errorf("Text: got %q, want %q", payload.Text, "Let me check the weather for you.")
	}

	roundTripEnvelope(t, env, payload, raw)
}

func TestAssistantDeltaPayload_ZeroValueParentIDIsEmitted(t *testing.T) {
	b, err := json.Marshal(AssistantDeltaPayload{})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(b, &fields); err != nil {
		t.Fatalf("unmarshal payload fields: %v", err)
	}
	got, ok := fields["parent_tool_use_id"]
	if !ok {
		t.Fatalf("parent_tool_use_id missing from zero-value payload: %s", b)
	}
	if string(got) != `""` {
		t.Errorf("parent_tool_use_id: got %s, want empty string", got)
	}
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
	// EMPTY IS THE BOUNDARY VALUE HERE (#2191), and the fixture is a MAIN-THREAD
	// call on purpose: this is the frame every existing client already receives, so
	// pinning it empty is what says "unchanged apart from the new key". The
	// round-trip below pins the KEY's presence byte-for-byte — without it a future
	// omitempty would drop the key and a client could not tell "main thread" from
	// "a daemon too old to send it".
	if payload.ParentToolUseID != "" {
		t.Errorf("ParentToolUseID: got %q, want %q (main thread)", payload.ParentToolUseID, "")
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
	// Empty for the reason the tool_use fixture states, and it matters twice here:
	// this frame is never-droppable control class, so its field set is the one a
	// client cannot afford to guess at.
	if payload.ParentToolUseID != "" {
		t.Errorf("ParentToolUseID: got %q, want %q (main thread)", payload.ParentToolUseID, "")
	}
	// IsError==false is the boundary value: the fixture pins that a false
	// bool stays on the wire (no omitempty would silently drop it).
	if payload.IsError {
		t.Errorf("IsError: got true, want false")
	}
	if payload.ResultSummary != "4°C, light snow showers in the afternoon." {
		t.Errorf("ResultSummary: got %q, want %q", payload.ResultSummary, "4°C, light snow showers in the afternoon.")
	}
	// The fixture pins result_detail present-and-empty, which is this file's rule
	// (no field carries omitempty) rather than an oversight: empty means "no
	// count", which is the answer for most tools, and always emitting the key
	// keeps the fixture a full-shape reference for a client author (#2024).
	if payload.ResultDetail != "" {
		t.Errorf("ResultDetail: got %q, want empty", payload.ResultDetail)
	}

	roundTripEnvelope(t, env, payload, raw)
}

// TestToolResultPayload_ResultDetailIsOptional covers #2024's wire criterion: the
// new field's ABSENCE is a value, not an error. A frame minted by a daemon built
// before this landed carries no result_detail key at all, and must still decode —
// to "", which means exactly what an explicit "" means, no count.
//
// The reverse direction needs no test to be safe and cannot get one here: a
// client built before this landed decodes into a struct without the field, and
// encoding/json ignores unknown keys. This asserts the half that lives in this
// repo.
func TestToolResultPayload_ResultDetailIsOptional(t *testing.T) {
	t.Parallel()
	// The pre-#2024 payload shape, byte for byte.
	const legacy = `{"conversation_id":"c1","turn_id":"t7","tool_use_id":"tu1","is_error":false,"result_summary":"done"}`

	var payload ToolResultPayload
	if err := json.Unmarshal([]byte(legacy), &payload); err != nil {
		t.Fatalf("a payload without result_detail must decode, got: %v", err)
	}
	if payload.ResultDetail != "" {
		t.Errorf("ResultDetail: got %q, want empty", payload.ResultDetail)
	}
	// The rest of the payload must survive unchanged — a decode that "worked" but
	// dropped a sibling field would pass the check above.
	if payload.ResultSummary != "done" || payload.ToolUseID != "tu1" {
		t.Errorf("sibling fields: got %+v", payload)
	}
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
	// The #2223 stop shape, asserted rather than merely present in the fixture.
	// The fixture is deliberately a BUDGET-STOPPED turn whose stop_reason is still
	// "end_turn": that combination is the whole point of the ticket, and a fixture
	// where every field agreed would pin nothing — a decoder that read outcome off
	// stop_reason, or is_error off the subtype, would pass it.
	if payload.Outcome != "error_max_turns" {
		t.Errorf("Outcome: got %q, want %q", payload.Outcome, "error_max_turns")
	}
	if !payload.IsError {
		t.Errorf("IsError: got false, want true")
	}
	if payload.TerminalReason != "max_turns" {
		t.Errorf("TerminalReason: got %q, want %q", payload.TerminalReason, "max_turns")
	}
	// #2224's category, and the fixture value is deliberately one NOTHING else on
	// the frame implies: a turn that stopped on --max-turns whose API was ALSO rate
	// limited. A decoder that read this off the subtype, off terminal_reason or off
	// is_error would pass a fixture where the four agreed; it cannot pass this one.
	if payload.ErrorCategory != "rate_limit" {
		t.Errorf("ErrorCategory: got %q, want %q", payload.ErrorCategory, "rate_limit")
	}
	// #2260's four numbers, and the fixture's values are claude's own from the
	// error_max_turns line of the committed permission_mode_switch_v2.1.239_plan
	// capture rather than round figures. Two properties make the fixture pin
	// something a tidier one would not. duration_api_ms EXCEEDS duration_ms, which is
	// the reading the ticket exists to foreclose and the norm across the corpus; and
	// no value is derivable from another, so a decoder that crossed two keys or read
	// one off a neighbour cannot pass.
	if payload.DurationMS != 24594 {
		t.Errorf("DurationMS: got %d, want %d", payload.DurationMS, 24594)
	}
	if payload.DurationAPIMS != 27064 {
		t.Errorf("DurationAPIMS: got %d, want %d", payload.DurationAPIMS, 27064)
	}
	if payload.NumTurns != 5 {
		t.Errorf("NumTurns: got %d, want %d", payload.NumTurns, 5)
	}
	if payload.CostUSDTotal != 0.1608898 {
		t.Errorf("CostUSDTotal: got %v, want %v", payload.CostUSDTotal, 0.1608898)
	}
	if payload.InputTokens != 8 || payload.OutputTokens != 1715 ||
		payload.CacheReadTokens != 196771 || payload.CacheCreationTokens != 12211 {
		t.Errorf("token counts: got {%d %d %d %d}, want {8 1715 196771 12211}",
			payload.InputTokens, payload.OutputTokens, payload.CacheReadTokens, payload.CacheCreationTokens)
	}

	roundTripEnvelope(t, env, payload, raw)
}

// TestTurnEndPayload_TurnTotalsAreOptional is the "optional means no omitempty, not a
// pointer" contract measured from the client's side: a frame minted before #2260
// and #2261 landed must decode without error, with both four-number groups at zero
// and every field beside them intact.
//
// The sibling-survival check is the half a decode-succeeded assertion would miss — a
// decode that "worked" but dropped error_category would pass the first check alone.
func TestTurnEndPayload_TurnTotalsAreOptional(t *testing.T) {
	t.Parallel()
	// The pre-#2260 payload shape, byte for byte.
	const legacy = `{"conversation_id":"c1","turn_id":"t7","stop_reason":"end_turn",` +
		`"outcome":"error_max_turns","is_error":true,"terminal_reason":"max_turns",` +
		`"error_category":"rate_limit"}`

	var payload TurnEndPayload
	if err := json.Unmarshal([]byte(legacy), &payload); err != nil {
		t.Fatalf("a payload without the turn totals must decode, got: %v", err)
	}
	if payload.DurationMS != 0 || payload.DurationAPIMS != 0 || payload.NumTurns != 0 || payload.CostUSDTotal != 0 {
		t.Errorf("turn totals: got {%d %d %d %v}, want all zero",
			payload.DurationMS, payload.DurationAPIMS, payload.NumTurns, payload.CostUSDTotal)
	}
	if payload.InputTokens != 0 || payload.OutputTokens != 0 ||
		payload.CacheReadTokens != 0 || payload.CacheCreationTokens != 0 {
		t.Errorf("token counts: got {%d %d %d %d}, want all zero",
			payload.InputTokens, payload.OutputTokens, payload.CacheReadTokens, payload.CacheCreationTokens)
	}
	if payload.Outcome != "error_max_turns" || payload.TerminalReason != "max_turns" ||
		payload.ErrorCategory != "rate_limit" || !payload.IsError {
		t.Errorf("sibling fields: got %+v", payload)
	}
}

func TestTurnEndPayload_ZeroTokenCountsAreEmitted(t *testing.T) {
	t.Parallel()
	raw, err := json.Marshal(TurnEndPayload{})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, key := range []string{"input_tokens", "output_tokens", "cache_read_tokens", "cache_creation_tokens"} {
		if !bytes.Contains(raw, []byte(`"`+key+`":0`)) {
			t.Errorf("missing zero-valued %q in %s", key, raw)
		}
	}
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
	// #2236's AC 2 at the wire: the rising edge names no outcome, because the
	// compaction it announces has not finished. Both keys are still PRESENT and
	// empty — this file emits no omitempty, so the fixture pins the full shape.
	if payload.Result != "" || payload.ErrorText != "" {
		t.Errorf("rising edge carries Result=%q ErrorText=%q, want both empty",
			payload.Result, payload.ErrorText)
	}

	roundTripEnvelope(t, env, payload, raw)
}

// TestCompactingPayload_FallingEdgeRoundTrip is #2236's AC 1 at the wire. The
// committed compacting.json is a RISING edge and cannot carry the two fields at
// all, so the falling edge needs a fixture of its own — the shape a client reads
// when a compaction FAILED, which before #2236 was indistinguishable from a
// successful one everywhere outside a daemon log.
func TestCompactingPayload_FallingEdgeRoundTrip(t *testing.T) {
	raw := readFixture(t, "compacting_ended.json")

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
	if payload.Active {
		t.Errorf("Active: got %v, want false", payload.Active)
	}
	if payload.Result != "failed" {
		t.Errorf("Result: got %q, want %q", payload.Result, "failed")
	}
	if payload.ErrorText != "context window still over budget" {
		t.Errorf("ErrorText: got %q, want %q", payload.ErrorText,
			"context window still over budget")
	}

	roundTripEnvelope(t, env, payload, raw)
}

// TestCompactionBoundaryPayload_RoundTrip is #2237 at the wire: the frame carrying
// claude's trigger and both counts, with the observed capture's own values.
func TestCompactionBoundaryPayload_RoundTrip(t *testing.T) {
	raw := readFixture(t, "compaction_boundary.json")

	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if env.Type != TypeCompactionBoundary {
		t.Errorf("Type: got %q, want %q", env.Type, TypeCompactionBoundary)
	}

	var payload CompactionBoundaryPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload.ConversationID != "c1" {
		t.Errorf("ConversationID: got %q, want %q", payload.ConversationID, "c1")
	}
	if payload.Trigger != "manual" {
		t.Errorf("Trigger: got %q, want %q", payload.Trigger, "manual")
	}
	if payload.PreTokens == nil || *payload.PreTokens != 23600 {
		t.Errorf("PreTokens: got %s, want 23600", countOrNull(payload.PreTokens))
	}
	if payload.PostTokens == nil || *payload.PostTokens != 2612 {
		t.Errorf("PostTokens: got %s, want 2612", countOrNull(payload.PostTokens))
	}

	roundTripEnvelope(t, env, payload, raw)
}

// TestCompactionBoundaryPayload_AbsentCountIsNullNotZero is the wire half of #2237's
// AC 1, and the reason it is a fixture of its own rather than a column on the test
// above: one payload cannot carry both a present and an absent post_tokens.
//
// The round-trip is what makes it non-vacuous in the direction that matters. Decoding
// `null` to a nil pointer is not the claim — the claim is that a nil pointer ENCODES
// back to `null` rather than to `0` or to a dropped key, which is the only thing that
// makes absence survive to a client. roundTripEnvelope compares bytes, so an
// omitempty added here later for tidiness reddens rather than silently erasing the
// distinction.
func TestCompactionBoundaryPayload_AbsentCountIsNullNotZero(t *testing.T) {
	raw := readFixture(t, "compaction_boundary_no_post.json")

	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	var payload CompactionBoundaryPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload.PreTokens == nil || *payload.PreTokens != 23600 {
		t.Errorf("PreTokens: got %s, want 23600 — an absent post_tokens must not cost the "+
			"count claude DID state", countOrNull(payload.PreTokens))
	}
	if payload.PostTokens != nil {
		t.Errorf("PostTokens: got %s, want null. A client rendering \"24k → 0 tokens\" for a "+
			"boundary claude reported without a post count is the failure this shape exists to "+
			"prevent, and a plain int field passes every other assertion in this file",
			countOrNull(payload.PostTokens))
	}

	roundTripEnvelope(t, env, payload, raw)
}

// countOrNull renders a count pointer for a failure message, keeping nil and zero
// visibly apart — every assertion above turns on that distinction, so a message
// printing both as "0" would misdescribe the failure it reports.
func countOrNull(p *int) string {
	if p == nil {
		return "null"
	}
	return strconv.Itoa(*p)
}

// TestCompactingPayload_OldDecoderReadsTheExtendedFrame is #2236's AC 4, and it is
// asserted rather than assumed on purpose. That JSON decoding ignores unknown keys
// is exactly the kind of "obviously true" wire-compatibility claim that goes false
// unwatched — a later omitempty, a rename, or a payload growing a MarshalJSON of
// its own would break it silently, and every one of those is a plausible edit here.
//
// oldCompactingPayload is the struct as it shipped at #1074: the two fields a
// client built before #2236 declares. Deliberately a local type rather than a
// commented-out copy, so it is compiled and decoded rather than read.
func TestCompactingPayload_OldDecoderReadsTheExtendedFrame(t *testing.T) {
	type oldCompactingPayload struct {
		ConversationID string `json:"conversation_id"`
		Active         bool   `json:"active"`
	}

	tests := []struct {
		fixture    string
		wantActive bool
	}{
		{"compacting.json", true},
		{"compacting_ended.json", false},
	}
	for _, tc := range tests {
		t.Run(tc.fixture, func(t *testing.T) {
			var env Envelope
			if err := json.Unmarshal(readFixture(t, tc.fixture), &env); err != nil {
				t.Fatalf("unmarshal envelope: %v", err)
			}
			var old oldCompactingPayload
			if err := json.Unmarshal(env.Payload, &old); err != nil {
				t.Fatalf("an old decoder failed on the extended frame: %v — the two added keys "+
					"must be invisible to a client that does not declare them", err)
			}
			if old.ConversationID != "c1" {
				t.Errorf("ConversationID: got %q, want %q", old.ConversationID, "c1")
			}
			if old.Active != tc.wantActive {
				t.Errorf("Active: got %v, want %v", old.Active, tc.wantActive)
			}
		})
	}
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
	// The fixture's status is deliberately NOT a value any capture carries, and the
	// reason was RESTATED at #2249 rather than removed. It used to read "every capture
	// on record reads allowed", which is no longer true: one record carries
	// allowed_warning, the weekly warning band. What still holds is the part the choice
	// rests on — "<unmeasured>" is a value no capture carries, so it cannot be copied
	// out of this fixture as if it were observed, and the measured non-benign value is
	// already pinned where it was measured (internal/streamsup's replay of the
	// committed capture) rather than duplicated here. The angle
	// brackets double as the escaping pin: encoding/json emits '<' and '>' in
	// their six-byte \uXXXX form, so a claude-authored string survives the trip
	// byte-exactly — which is why this sentinel stays even though a realistic value
	// now exists.
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
	// 0.94 is MEASURED, unlike the status one field up: it is the reading the committed
	// allowed_warning capture carries, so the number a client author copies out of this
	// fixture is one claude actually sent. The non-nil guard is load-bearing — a nil
	// pointer dereferenced in the comparison would panic rather than report, and a
	// field whose tag stopped matching decodes to nil rather than to a wrong number.
	if payload.Utilization == nil {
		t.Errorf("Utilization: got null, want 0.94")
	} else if *payload.Utilization != 0.94 {
		t.Errorf("Utilization: got %v, want 0.94", *payload.Utilization)
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
	// The THIRD such guard, and the one whose two wrong answers are both plausible.
	// The zero value of the reading is a nil pointer, which must reach the wire as
	// null — "utilization":0 would say claude reported a fresh window, and a dropped
	// key (an omitempty away) would say nothing at all. Pinning the fixture to null is
	// what makes the round trip below assert the TYPE's behaviour rather than agree
	// with whatever the fixture happens to carry.
	if !bytes.Contains(canonical(t, raw), []byte(`"utilization":null`)) {
		t.Errorf("fixture must carry the unreported reading as null, got: %s", raw)
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
	// Explicitly nil, not a dereferenced comparison against 0: those are the two
	// different facts this field exists to keep apart, and the failure message renders
	// them apart too.
	if payload.Utilization != nil {
		t.Errorf("Utilization: got %s, want null", utilOrNull(payload.Utilization))
	}
	// Explicitly nil, not len() == 0: len is 0 for both nil and [], and [] is the
	// value this payload must never produce.
	if payload.TruncatedFields != nil {
		t.Errorf("TruncatedFields: got %v, want nil", payload.TruncatedFields)
	}

	roundTripEnvelope(t, env, payload, raw)
}

// utilOrNull renders a reading pointer for a failure message, keeping nil and zero
// visibly apart — countOrNull's reason, applied to the field whose whole contract is
// that distinction. A message printing both as "0" would misdescribe the failure it
// reports, and here that is the failure most likely to be reported.
func utilOrNull(p *float64) string {
	if p == nil {
		return "null"
	}
	return strconv.FormatFloat(*p, 'g', -1, 64)
}

// TestRateLimitedPayload_AbsentUtilizationIsNullNotZero is #2249's AC 3 made
// executable, and it takes TestCompactionBoundaryPayload_AbsentCountIsNullNotZero's
// shape for the same reason: one payload cannot carry both an absent and a present
// reading, so the claim needs two encodings compared against each other rather than
// one round trip.
//
// What it asserts is the ENCODING direction, which is the only one that makes absence
// survive to a client. Decoding null to a nil pointer is not the claim — the claim is
// that nil encodes back to null rather than to 0 or to a dropped key, and that a
// pointer to 0 encodes to 0. The final comparison is the criterion's own wording: the
// two must not produce the same bytes. A plain float64 field makes them identical and
// every other assertion in this file still passes, which is why this test exists.
//
// Deliberately not a third golden fixture. The two committed ones pin the populated
// and the zero-value round trips, and what remains is a property of the TYPE's
// marshalling rather than of any file's bytes.
func TestRateLimitedPayload_AbsentUtilizationIsNullNotZero(t *testing.T) {
	zero := 0.0
	base := RateLimitedPayload{ConversationID: "c1", Status: "<unmeasured>", LimitType: "five_hour"}

	absent := base
	reported := base
	reported.Utilization = &zero

	absentBytes, err := json.Marshal(absent)
	if err != nil {
		t.Fatalf("marshal the absent reading: %v", err)
	}
	reportedBytes, err := json.Marshal(reported)
	if err != nil {
		t.Fatalf("marshal the reported reading: %v", err)
	}

	if !bytes.Contains(absentBytes, []byte(`"utilization":null`)) {
		t.Errorf("an absent reading must encode as null, got: %s", absentBytes)
	}
	if !bytes.Contains(reportedBytes, []byte(`"utilization":0`)) {
		t.Errorf("a reading claude reported as 0 must encode as 0, got: %s", reportedBytes)
	}
	if bytes.Equal(absentBytes, reportedBytes) {
		t.Fatalf("a reading claude never sent encodes identically to one claude reported as 0, "+
			"so a client cannot tell an unknown window from a fresh one: %s", absentBytes)
	}

	// Both directions, because a client reads before it writes: the bytes each side
	// produced must decode back to the pointer state they came from. Without this a
	// MarshalJSON that wrote the right bytes from the wrong state would pass above.
	var backAbsent, backReported RateLimitedPayload
	if err := json.Unmarshal(absentBytes, &backAbsent); err != nil {
		t.Fatalf("unmarshal the absent reading: %v", err)
	}
	if err := json.Unmarshal(reportedBytes, &backReported); err != nil {
		t.Fatalf("unmarshal the reported reading: %v", err)
	}
	if backAbsent.Utilization != nil {
		t.Errorf("null decoded to %s, want nil", utilOrNull(backAbsent.Utilization))
	}
	if backReported.Utilization == nil || *backReported.Utilization != 0 {
		t.Errorf("0 decoded to %s, want a pointer to 0", utilOrNull(backReported.Utilization))
	}
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

func TestSessionFactsPayload_RoundTrip(t *testing.T) {
	raw := readFixture(t, "session_facts.json")

	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if env.Type != TypeSessionFacts {
		t.Errorf("Type: got %q, want %q", env.Type, TypeSessionFacts)
	}

	var payload SessionFactsPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	// BOTH claude-derived values are MEASURED, per TestModelAnnouncedPayload_RoundTrip's
	// register: a client author copies a fixture as if it were observed traffic, so it
	// has to be. 2.1.259 is claude's own build on all three init lines of #2251's
	// effort capture; default is the posture those same lines report.
	// bypassPermissions is the other measured posture, from the capture
	// internal/e2e/realclaude/testdata/dropped_lines_v2.1.220.json.
	//
	// The two differ from each other deliberately. With one string in both fields, a
	// struct wiring claude_code_version and permission_mode to each other's wire key
	// would decode and re-encode identically and this test would stay green.
	for _, f := range []struct {
		name string
		got  string
		want string
	}{
		{"ConversationID", payload.ConversationID, "c1"},
		{"ClaudeCodeVersion", payload.ClaudeCodeVersion, "2.1.259"},
		{"PermissionMode", payload.PermissionMode, "default"},
	} {
		if f.got != f.want {
			t.Errorf("%s: got %q, want %q", f.name, f.got, f.want)
		}
	}
	// One entry, naming the SECOND field. That is what discriminates this []string
	// from ModelAnnounced's Truncated bool: a list permanently indexed to the first
	// field carries no more than the bool does. It also pins the DAEMON's name for
	// the posture — claude's key is permissionMode, and an entry spelling it claude's
	// way is the realistic bug (turnevent.SessionFacts.TruncatedFields' own rule).
	//
	// The PAIRING is not a capture, as model_announced.json's is not: 2.1.259 is 7
	// bytes against a 256-byte cap, so no real frame reports this version as cut. The
	// two values are measured; the truncation report is chosen to discriminate.
	if got, want := strings.Join(payload.TruncatedFields, ","), "permission_mode"; got != want {
		t.Errorf("TruncatedFields: got %q, want %q", got, want)
	}

	roundTripEnvelope(t, env, payload, raw)
}

// TestSessionFactsPayload_ZeroValue_RoundTrip pins the encoding of every field's
// zero value, which is what this stream's no-omitempty rule
// (docs/protocol-mobile.md § Interactive events) actually asserts: adding omitempty
// to any one of the four fields turns this round trip red, and no realistic fixture
// can make that claim for all four.
//
// The frame itself is one the bridge will never emit — the producer gates on at
// least one of the two facts being present (turnevent.SessionFacts' own gate note)
// and the bridge always supplies a conversation id. It exists for the encoding, not
// for the scenario, exactly as TestModelAnnouncedPayload_ZeroValue_RoundTrip's does.
func TestSessionFactsPayload_ZeroValue_RoundTrip(t *testing.T) {
	raw := readFixture(t, "session_facts_zero.json")

	// The four guards are load-bearing, and WHICH failure they catch is worth stating
	// precisely rather than copying the sibling's sentence — measured by overlay
	// mutant rather than assumed. Against the COMMITTED fixture the round trip below
	// already reddens on an omitempty: the key vanishes from the re-marshalled bytes
	// while the fixture still carries it, so the byte comparison fails. What these
	// guards add is survival of a fixture REGENERATION — the moment someone
	// regenerates session_facts_zero.json under an omitempty, the key is gone from
	// both sides and the round trip goes green again while these stay red. That is
	// the property the drift-detector overview records for key-set pins generally.
	//
	// The truncated_fields guard differs from the sibling's three: nil serialises as
	// null and not as [], the form every truncated_fields row in
	// docs/protocol-mobile.md already describes.
	for _, want := range []string{
		`"conversation_id":""`,
		`"claude_code_version":""`,
		`"permission_mode":""`,
		`"truncated_fields":null`,
	} {
		if !bytes.Contains(canonical(t, raw), []byte(want)) {
			t.Errorf("fixture must carry %s explicitly, got: %s", want, raw)
		}
	}

	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if env.Type != TypeSessionFacts {
		t.Errorf("Type: got %q, want %q", env.Type, TypeSessionFacts)
	}

	var payload SessionFactsPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	for _, f := range []struct {
		name string
		got  string
	}{
		{"ConversationID", payload.ConversationID},
		{"ClaudeCodeVersion", payload.ClaudeCodeVersion},
		{"PermissionMode", payload.PermissionMode},
	} {
		if f.got != "" {
			t.Errorf("%s: got %q, want empty", f.name, f.got)
		}
	}
	if payload.TruncatedFields != nil {
		t.Errorf("TruncatedFields: got %v, want nil", payload.TruncatedFields)
	}

	roundTripEnvelope(t, env, payload, raw)
}

// TestSessionFactsType_IsNotClaudesVocabulary pins the translation layer this frame
// exists to preserve, as its model_announced, model_list and slash_command_list
// siblings above do. The daemon is the ONE place a claude rename lands.
//
// THE DISCRIMINATING WORDS INVERT THE OBVIOUS CHOICE, and the trap cuts closer here
// than on any sibling: `session` is a substring of the correct name, so a
// strings.Contains check on it would be RED against session_facts — the same trap
// TypeModelAnnounced's block records for the singular `model` and
// TypeSlashCommandList's records three words wide for command, slash_command and
// slash. `session` is claude's word as well, via the session_id key on the very
// system/init line these two facts come from, which is what makes the collision
// worth stating rather than leaving for the next reader to rediscover.
//
// The checkable words are therefore claude's SUBTYPE (init) and claude's two KEYS
// for the values (claude_code_version, permissionMode). session_facts contains none
// of the three, which is what makes these pins satisfiable at all.
//
// Separating this constant from its shipped session_-prefixed neighbours has to be
// an EQUALITY rather than a containment, for the same reason: three constants
// already begin session_, so the shared word cannot tell them apart. That is the
// shape TypeQuestionDismissed's pin needed against TypeModalDismissed.
func TestSessionFactsType_IsNotClaudesVocabulary(t *testing.T) {
	if TypeSessionFacts == "init" {
		t.Errorf("wire type is claude's subtype %q; it must be the daemon's own name", TypeSessionFacts)
	}
	for _, word := range []string{"init", "claude_code_version", "permissionMode"} {
		if strings.Contains(TypeSessionFacts, word) {
			t.Errorf("wire type %q is derived from claude's vocabulary (contains %q)", TypeSessionFacts, word)
		}
	}
	// The exact pin, matching internal/turnevent's variant name (SessionFacts) in
	// snake_case rather than anything of claude's. cmd/pyry's eventKind already
	// returns this spelling for the variant (#2252), so the wire name and the daemon's
	// own log agree by construction rather than by coincidence.
	if TypeSessionFacts != "session_facts" {
		t.Errorf("wire type: got %q, want %q", TypeSessionFacts, "session_facts")
	}
	for _, sibling := range []struct {
		name  string
		value string
	}{
		{"TypeSessionTransition", TypeSessionTransition},
		{"TypeSessionSettings", TypeSessionSettings},
		{"TypeSessionError", TypeSessionError},
	} {
		if TypeSessionFacts == sibling.value {
			t.Errorf("wire type collides with %s (%q)", sibling.name, sibling.value)
		}
	}

	// The payload's own bytes, not the envelope's — the envelope carries its own
	// id/ts and would dilute the check. This is where "what this frame does NOT
	// carry" stops being prose and becomes machine-checked: the operator's local
	// filesystem (cwd, memory_paths, messaging_socket_path), claude's own session
	// identity (session_id), the MCP server status that belongs to #2275's frame, and
	// the effort key #2251 measured claude does not publish at all. Non-discriminating
	// today by construction; the job is to go red the day someone "helpfully" adds
	// claude's init-line keys back.
	body, err := json.Marshal(SessionFactsPayload{
		ConversationID:    "c1",
		ClaudeCodeVersion: "2.1.259",
		PermissionMode:    "bypassPermissions",
		TruncatedFields:   []string{"claude_code_version", "permission_mode"},
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	for _, key := range []string{
		"cwd", "session_id", "memory_paths", "messaging_socket_path",
		"mcp_servers", "effort", "tools", "slash_commands",
	} {
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
//     turnevent.SlashCommandList.DroppedCommands carries the cut, and since #2001
//     turnbridge.MapEvent's arm MAPS that onto this field verbatim. The 2 here is
//     nevertheless still a fixture's number rather than a producer's, because this
//     is a decode fixture and no live frame carries either count yet — #2003 is
//     what emits one.
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
	// maxSlashCommandListEntries) and turnbridge.MapEvent's arm now maps that count
	// onto this payload verbatim (#2001), but no frame of this type is produced at
	// all, so a client still cannot read len(commands) + dropped_commands as the
	// menu's true size off any live frame — #2003 is what makes that reading true.
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

// TestRequestModelListPayload_RoundTrip pins the on-demand model-list REQUEST
// (#2125) against its committed fixture.
//
// It is the ONLY test in this package that pins the wire STRING
// "request_model_list". None of the three registries in compat_test.go can — all
// three key on the Go symbol, so renaming the constant's value moves through them
// consistently and reddens none (measured by mutant on #1895's sibling, recorded
// in the drift-detectors overview).
//
// THE CONVERSATION ID IS THE TIE, not an arbitrary string. It is "c1", exactly the
// conversation_id the committed model_list.json carries, so the two files describe
// ONE exchange — the ask, and the frame shape that answers it. The correlation
// itself is NOT pinned here and cannot be: model_list.json is the unsolicited form
// (live lane and connect-time reconcile), which carries no in_reply_to at all. The
// answer's envelope — same type, same payload source, in_reply_to set, event_id
// absent — is pinned against bytes the daemon actually emits, in
// internal/relay's handler test, which is stronger evidence than a hand-written
// file for a shape this package never constructs.
//
// InReplyTo is asserted NIL, the structural half of the classification
// cmd/pyry/relay_guard_test.go's inboundTypes records: this frame is a request,
// and the correlation runs FROM it rather than to it.
func TestRequestModelListPayload_RoundTrip(t *testing.T) {
	raw := readFixture(t, "request_model_list.json")

	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if env.Type != TypeRequestModelList {
		t.Errorf("Type: got %q, want %q", env.Type, TypeRequestModelList)
	}
	if env.InReplyTo != nil {
		t.Errorf("InReplyTo: got pointer to %d, want nil — this frame is a request, not a reply", *env.InReplyTo)
	}

	var payload RequestModelListPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if want := "c1"; payload.ConversationID != want {
		t.Errorf("ConversationID: got %q, want %q (model_list.json's conversation — the two fixtures describe one exchange)", payload.ConversationID, want)
	}

	roundTripEnvelope(t, env, payload, raw)
}

// TestRequestModelListPayload_WireKeys pins the payload's COMPLETE set of wire
// keys, so a later field cannot be added without this failing. It is the
// machine-checked form of the frame's central omission — NO REQUEST-ID KEY,
// because correlation rides the envelope's InReplyTo.
//
// It marshals a freshly populated struct rather than reading the fixture, and that
// is the whole point: adding an undeclared field reddens both this and the round
// trip, but regenerating the fixture under that change turns the round trip green
// again while this stays red. It is also what exercises the struct tags at all —
// the round trip above re-emits env.Payload's own json.RawMessage bytes, so an
// omitempty added here for tidiness is invisible to it.
func TestRequestModelListPayload_WireKeys(t *testing.T) {
	b, err := json.Marshal(RequestModelListPayload{ConversationID: "c1"})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal payload into key set: %v", err)
	}

	want := map[string]bool{"conversation_id": true}
	for k := range got {
		if !want[k] {
			t.Errorf("unexpected wire key %q: the payload's key set is fixed at %v — correlation rides the envelope's in_reply_to, so this frame carries no request-id key", k, want)
		}
	}
	for k := range want {
		if _, ok := got[k]; !ok {
			t.Errorf("missing wire key %q, got: %s — the key is always present (no omitempty), so absent and empty stay the same case", k, b)
		}
	}
}

// TestRequestModelListPayload_ZeroValue_KeyPresent pins the no-omitempty decision
// directly: a zero-valued payload still carries the key.
//
// Separate from the key-set test above because the two fail on different mutants.
// That one marshals a POPULATED struct, so an omitempty added to ConversationID
// leaves it green; only a zero value forces the tag to speak.
func TestRequestModelListPayload_ZeroValue_KeyPresent(t *testing.T) {
	b, err := json.Marshal(RequestModelListPayload{})
	if err != nil {
		t.Fatalf("marshal zero payload: %v", err)
	}
	if want := `{"conversation_id":""}`; string(b) != want {
		t.Errorf("zero payload: got %s, want %s — the key is unconditional, so a client that names no conversation still sends a well-formed frame", b, want)
	}
}

func TestToolDeniedPayload_RoundTrip(t *testing.T) {
	raw := readFixture(t, "tool_denied.json")

	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if env.Type != TypeToolDenied {
		t.Errorf("Type: got %q, want %q", env.Type, TypeToolDenied)
	}

	var payload ToolDeniedPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload.ConversationID != "c1" {
		t.Errorf("ConversationID: got %q, want %q", payload.ConversationID, "c1")
	}
	if payload.TurnID != "t7" {
		t.Errorf("TurnID: got %q, want %q", payload.TurnID, "t7")
	}
	// The join key. A client correlates this row with the tool_use and tool_result
	// carrying the same value, so it is asserted for its exact bytes rather than for
	// being non-empty.
	if payload.ToolUseID != "toolu_01A9F" {
		t.Errorf("ToolUseID: got %q, want %q", payload.ToolUseID, "toolu_01A9F")
	}
	// Empty AND named in dropped_fields: the daemon emptied an over-cap name. The
	// pairing is the whole reason the second report slice exists, so both halves are
	// asserted together.
	if payload.ToolName != "" {
		t.Errorf("ToolName: got %q, want %q — the fixture names it dropped", payload.ToolName, "")
	}
	if payload.DecisionReasonType != "rule" {
		t.Errorf("DecisionReasonType: got %q, want %q", payload.DecisionReasonType, "rule")
	}
	if want := "Command matches a deny rule for this session…"; payload.DecisionReason != want {
		t.Errorf("DecisionReason: got %q, want %q", payload.DecisionReason, want)
	}
	if want := "Claude requested permissions to use Bash, but you haven't granted it yet…"; payload.Message != want {
		t.Errorf("Message: got %q, want %q", payload.Message, want)
	}
	if want := []string{"message", "decision_reason"}; !reflect.DeepEqual(payload.TruncatedFields, want) {
		t.Errorf("TruncatedFields: got %v, want %v", payload.TruncatedFields, want)
	}
	// tool_name, not the daemon-internal spelling: every token in either slice names
	// a key THIS frame carries, so a client can look it up.
	if want := []string{"tool_name"}; !reflect.DeepEqual(payload.DroppedFields, want) {
		t.Errorf("DroppedFields: got %v, want %v", payload.DroppedFields, want)
	}

	roundTripEnvelope(t, env, payload, raw)
}

// TestToolDeniedPayload_EmptyReasons_RoundTrip pins the shape every committed capture
// actually produces: both reason fields empty and neither report slice populated. It is
// the row that fails if a MarshalJSON or an omitempty is ever added — the populated row
// above passes either way, so on its own it proves nothing about absence.
//
// Empty and named in NEITHER slice is the reading "claude sent nothing", which is what
// makes the two nulls load-bearing rather than incidental.
func TestToolDeniedPayload_EmptyReasons_RoundTrip(t *testing.T) {
	raw := readFixture(t, "tool_denied_empty_reasons.json")

	// The four keys whose value here is a zero an omitempty would elide, asserted on
	// the bytes rather than on the decoded struct: a dropped key decodes to the same
	// zero value, so only the wire form can tell the two apart.
	for _, want := range []string{
		`"decision_reason_type":""`,
		`"decision_reason":""`,
		`"truncated_fields":null`,
		`"dropped_fields":null`,
	} {
		if !bytes.Contains(canonical(t, raw), []byte(want)) {
			t.Errorf("fixture must carry %s explicitly, got: %s", want, raw)
		}
	}

	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if env.Type != TypeToolDenied {
		t.Errorf("Type: got %q, want %q", env.Type, TypeToolDenied)
	}

	var payload ToolDeniedPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload.ToolName != "Bash" {
		t.Errorf("ToolName: got %q, want %q", payload.ToolName, "Bash")
	}
	if payload.DecisionReasonType != "" || payload.DecisionReason != "" {
		t.Errorf("both reason fields must decode empty, got %q / %q",
			payload.DecisionReasonType, payload.DecisionReason)
	}
	if payload.TruncatedFields != nil {
		t.Errorf("TruncatedFields: got %v, want nil", payload.TruncatedFields)
	}
	if payload.DroppedFields != nil {
		t.Errorf("DroppedFields: got %v, want nil", payload.DroppedFields)
	}

	// The claim that binds: a nil slice must ENCODE back to a literal null. An
	// allocated [] would tell a phone that claude's cut text is complete, which is the
	// opposite of what nothing-was-cut means.
	roundTripEnvelope(t, env, payload, raw)
}

// TestToolDeniedType_IsNotClaudesVocabulary pins the wire type as the DAEMON's word.
// claude's line is system/permission_denied; the frame follows internal/turnevent's
// variant instead, so a claude rename lands in the parser rather than breaking every
// client at once.
func TestToolDeniedType_IsNotClaudesVocabulary(t *testing.T) {
	if TypeToolDenied != "tool_denied" {
		t.Errorf("TypeToolDenied: got %q, want %q", TypeToolDenied, "tool_denied")
	}
	if TypeToolDenied == "permission_denied" {
		t.Error("TypeToolDenied must not adopt claude's own subtype spelling")
	}
}

func TestModelRefusalFallbackPayload_RoundTrip(t *testing.T) {
	raw := readFixture(t, "model_refusal_fallback.json")

	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if env.Type != TypeModelRefusalFallback {
		t.Errorf("Type: got %q, want %q", env.Type, TypeModelRefusalFallback)
	}

	var payload ModelRefusalFallbackPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload.ConversationID != "c1" {
		t.Errorf("ConversationID: got %q, want %q", payload.ConversationID, "c1")
	}
	if payload.OriginalModel != "claude-opus-4-1" {
		t.Errorf("OriginalModel: got %q, want %q", payload.OriginalModel, "claude-opus-4-1")
	}
	if payload.FallbackModel != "claude-sonnet-4-5" {
		t.Errorf("FallbackModel: got %q, want %q", payload.FallbackModel, "claude-sonnet-4-5")
	}
	if payload.Scope != "session" || payload.RefusalCategory != "cyber" {
		t.Errorf("open-string fields: got scope=%q refusal_category=%q", payload.Scope, payload.RefusalCategory)
	}
	if payload.Banner != "Retrying this turn with a fallback model…" {
		t.Errorf("Banner: got %q", payload.Banner)
	}
	if want := []string{"banner"}; !reflect.DeepEqual(payload.TruncatedFields, want) {
		t.Errorf("TruncatedFields: got %v, want %v", payload.TruncatedFields, want)
	}
	if want := []string{"scope", "original_model", "fallback_model", "refusal_category"}; !reflect.DeepEqual(payload.DroppedFields, want) {
		t.Errorf("DroppedFields: got %v, want %v", payload.DroppedFields, want)
	}

	var keys map[string]json.RawMessage
	if err := json.Unmarshal(env.Payload, &keys); err != nil {
		t.Fatalf("re-decode payload keys: %v", err)
	}
	wantKeys := map[string]bool{
		"conversation_id": true, "original_model": true, "fallback_model": true,
		"scope": true, "refusal_category": true, "banner": true,
		"truncated_fields": true, "dropped_fields": true,
	}
	if len(keys) != len(wantKeys) {
		t.Fatalf("payload key count: got %d (%v), want %d (%v)", len(keys), keys, len(wantKeys), wantKeys)
	}
	for key := range keys {
		if !wantKeys[key] {
			t.Errorf("unexpected payload key %q", key)
		}
	}

	roundTripEnvelope(t, env, payload, raw)
}

func TestModelRefusalFallbackPayload_EmptyReports_RoundTrip(t *testing.T) {
	raw := readFixture(t, "model_refusal_fallback_empty_reports.json")
	canonicalRaw := canonical(t, raw)
	for _, want := range []string{`"refusal_category":""`, `"truncated_fields":null`, `"dropped_fields":null`} {
		if !bytes.Contains(canonicalRaw, []byte(want)) {
			t.Errorf("fixture must carry %s explicitly, got: %s", want, raw)
		}
	}

	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	var payload ModelRefusalFallbackPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload.TruncatedFields != nil || payload.DroppedFields != nil {
		t.Fatalf("empty reports: got truncated=%v dropped=%v, want both nil", payload.TruncatedFields, payload.DroppedFields)
	}

	roundTripEnvelope(t, env, payload, raw)
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

// TestBannerPayload_RoundTrip is #2256 at the wire: the frame carrying claude's
// operator-facing text, its level, the truncation report and the stops-turn flag.
//
// The fixture's level and stops_turn are the OBSERVED capture's own values (claude
// 2.1.259 — warning and true, on the single informational line
// internal/e2e/realclaude/testdata/operator_system_lines_v2.1.259.json carries). Its
// text is DELIBERATELY NOT the captured prose, which is the one place this fixture
// departs from the capture and is a decision rather than convenience: that prose embeds
// a host filesystem path and echoes the operator's own prompt back, and neither is
// needed to pin a shape. A short synthetic string proves everything a long real one
// would.
//
// truncated is false here because the fixture's text is short. The interesting half of
// that field — a long text a bridge did NOT flag, and a short one it did — cannot live
// on a fixture at all, since the bridge is what could get it wrong; it is
// TestMapEvent_BannerCrossesVerbatim's row in internal/turnbridge.
func TestBannerPayload_RoundTrip(t *testing.T) {
	raw := readFixture(t, "banner.json")

	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if env.Type != TypeBanner {
		t.Errorf("Type: got %q, want %q", env.Type, TypeBanner)
	}

	var payload BannerPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload.ConversationID != "c1" {
		t.Errorf("ConversationID: got %q, want %q", payload.ConversationID, "c1")
	}
	if payload.Level != "warning" {
		t.Errorf("Level: got %q, want %q", payload.Level, "warning")
	}
	if want := "UserPromptSubmit operation blocked by hook"; payload.Text != want {
		t.Errorf("Text: got %q, want %q", payload.Text, want)
	}
	if payload.Truncated {
		t.Errorf("Truncated: got %v, want false", payload.Truncated)
	}
	if !payload.StopsTurn {
		t.Errorf("StopsTurn: got %v, want true", payload.StopsTurn)
	}

	roundTripEnvelope(t, env, payload, raw)
}

// TestBannerType_IsNotClaudesVocabulary pins the translation layer this frame exists to
// preserve, as its rate_limited, model_announced, model_list, slash_command_list and
// question_shown siblings do. The daemon is the ONE place a claude rename lands; naming
// the wire type after claude's own vocabulary would undo that.
//
// claude has FIVE words on this path: the system subtypes informational (#2257) and
// notification (#2258), and the payload keys content, level and prevent_continuation.
// Four of them are checked below and the fifth may not be, which is the whole
// difference between this pin and its siblings.
//
// level IS ADOPTED VERBATIM, SO IT MUST NOT BE A CHECK — in either half. In the name
// half it would assert the frame is not derived from a word the frame DELIBERATELY
// keeps, which is backwards even though it happens to be green against `banner` today.
// In the payload half it would be RED against the correct struct, because level is this
// payload's own wire key: exactly the trap
// TestSlashCommandListType_IsNotClaudesVocabulary records for `commands` and
// TestModelListType_IsNotClaudesVocabulary for `models`. Do not copy either sibling's
// check list here.
//
// THE CONTAINMENT LATTICE IS FLAT, unlike every sibling's, and that is worth stating so
// a future rename knows the siblings' subtlety does not apply. Those pins have to
// reason about singular subject nouns that are substrings of the correct name — command
// and slash for slash_command_list, question for question_shown, model for
// model_announced. None of claude's five words here is a substring of `banner`, and
// `banner` is a substring of none of them, so every check below is independently
// discriminating and none is kept for redundancy. A rename that reintroduces a
// containment relation must re-derive this paragraph rather than inherit it.
//
// The exact-equality pin is the half that fails a WRONG name rather than merely a
// claude-derived one: the negative checks alone leave every other wrong name green.
// Naming is half this ticket's deliverable and no producer supplies the string, so the
// pin is load-bearing.
func TestBannerType_IsNotClaudesVocabulary(t *testing.T) {
	for _, word := range []string{"informational", "notification", "content", "prevent_continuation"} {
		if TypeBanner == word {
			t.Errorf("wire type is claude's own word %q; it must be the daemon's own name", TypeBanner)
		}
		if strings.Contains(TypeBanner, word) {
			t.Errorf("wire type %q is derived from claude's vocabulary (contains %q)", TypeBanner, word)
		}
	}
	// The exact pin, naming what the frame IS to a client rather than anything of
	// claude's.
	if TypeBanner != "banner" {
		t.Errorf("wire type: got %q, want %q", TypeBanner, "banner")
	}

	// The payload's own bytes, not the envelope's — the envelope carries its own id/ts
	// and would dilute the check.
	//
	// The list is exactly claude's two RENAMED keys, and both omissions from it are
	// deliberate. level is omitted because the shape adopts it (above). informational
	// and notification are omitted because they are LINE-level subtypes that were never
	// payload keys, so checking them would be non-discriminating by construction — and
	// worse than merely redundant here, because text carries claude's arbitrary prose
	// and a banner legitimately saying the word "notification" would turn this pin red
	// on correct data. That hazard is why this half checks keys claude used and this
	// shape rejected, never words claude merely said.
	//
	// The row's values are chosen to carry neither spelling for the same reason.
	body, err := json.Marshal(BannerPayload{
		ConversationID: "c1",
		Level:          "warning",
		Text:           "UserPromptSubmit operation blocked by hook",
		Truncated:      false,
		StopsTurn:      true,
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	for _, key := range []string{"content", "prevent_continuation"} {
		if bytes.Contains(body, []byte(key)) {
			t.Errorf("payload carries claude's spelling %q: %s", key, body)
		}
	}
}

func TestToolProgressPayload_RoundTrip(t *testing.T) {
	raw := readFixture(t, "tool_progress.json")

	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if env.Type != TypeToolProgress {
		t.Errorf("Type: got %q, want %q", env.Type, TypeToolProgress)
	}

	var payload ToolProgressPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload.ConversationID != "conversation-11" {
		t.Errorf("ConversationID: got %q, want %q", payload.ConversationID, "conversation-11")
	}
	if payload.TurnID != "turn-22" {
		t.Errorf("TurnID: got %q, want %q", payload.TurnID, "turn-22")
	}
	if payload.ToolUseID != "tool-progress-33" {
		t.Errorf("ToolUseID: got %q, want %q", payload.ToolUseID, "tool-progress-33")
	}
	if payload.ElapsedSeconds != -44 {
		t.Errorf("ElapsedSeconds: got %d, want %d", payload.ElapsedSeconds, -44)
	}

	roundTripEnvelope(t, env, payload, raw)
}

func TestToolProgressPayload_ZeroValue_RoundTrip(t *testing.T) {
	raw := readFixture(t, "tool_progress_zero.json")

	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if env.Type != TypeToolProgress {
		t.Errorf("Type: got %q, want %q", env.Type, TypeToolProgress)
	}

	var keys map[string]json.RawMessage
	if err := json.Unmarshal(env.Payload, &keys); err != nil {
		t.Fatalf("unmarshal payload keys: %v", err)
	}
	wantRaw := map[string]string{
		"conversation_id": `""`,
		"turn_id":         `""`,
		"tool_use_id":     `""`,
		"elapsed_seconds": "0",
	}
	if len(keys) != len(wantRaw) {
		t.Errorf("payload key count: got %d, want %d; keys=%v", len(keys), len(wantRaw), keys)
	}
	for key, want := range wantRaw {
		got, present := keys[key]
		if !present {
			t.Errorf("zero payload is missing key %q", key)
			continue
		}
		if string(got) != want {
			t.Errorf("zero payload %s: got %s, want %s", key, got, want)
		}
	}

	var payload ToolProgressPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	roundTripEnvelope(t, env, payload, raw)
}
