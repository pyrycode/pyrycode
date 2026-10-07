package protocol

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
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
