package protocol

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
)

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

// The four resetting tests below are #2453 at the wire, and they are four fixtures
// rather than one table because a single payload carries one edge and one phase.
// Together they pin the whole sequence a client decodes for ONE reset: two rising
// edges, then a falling one.
//
// Each asserts the closed-set fields against the exported constants rather than
// against a repeated literal. That is what makes them non-vacuous on the property
// nothing else in this package can catch — every registry entry in both structural
// guards keys on the Go SYMBOL, so a mutated constant VALUE moves consistently
// through all of them and reddens none (measured on #1895). Here the constant is
// compared against the committed fixture's bytes, so a typo in either one reddens.

// TestResettingPayload_RoundTrip is the first of a reset's two rising edges: the
// wrap-up turn is running and the handoff note does not exist yet.
func TestResettingPayload_RoundTrip(t *testing.T) {
	raw := readFixture(t, "resetting.json")

	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if env.Type != TypeResetting {
		t.Errorf("Type: got %q, want %q", env.Type, TypeResetting)
	}

	var payload ResettingPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload.ConversationID != "c1" {
		t.Errorf("ConversationID: got %q, want %q", payload.ConversationID, "c1")
	}
	if !payload.Active {
		t.Errorf("Active: got %v, want true", payload.Active)
	}
	if payload.Phase != ResetPhaseWrappingUp {
		t.Errorf("Phase: got %q, want %q", payload.Phase, ResetPhaseWrappingUp)
	}
	if payload.Handoff != ResetHandoffPending {
		t.Errorf("Handoff: got %q, want %q — the note cannot be resolved while the "+
			"turn that writes it is still running", payload.Handoff, ResetHandoffPending)
	}

	roundTripEnvelope(t, env, payload, raw)
}

// TestResettingPayload_RestartingWrittenRoundTrip is the SECOND rising edge, and the
// one a client gets wrong: a repeated active:true carrying a new phase is a phase
// change, not a second reset. This is where the frame departs from compacting's
// strict edge pair.
func TestResettingPayload_RestartingWrittenRoundTrip(t *testing.T) {
	raw := readFixture(t, "resetting_restarting_written.json")

	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if env.Type != TypeResetting {
		t.Errorf("Type: got %q, want %q", env.Type, TypeResetting)
	}

	var payload ResettingPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload.ConversationID != "c1" {
		t.Errorf("ConversationID: got %q, want %q", payload.ConversationID, "c1")
	}
	if !payload.Active {
		t.Errorf("Active: got %v, want true — the second rising edge stays active", payload.Active)
	}
	if payload.Phase != ResetPhaseRestarting {
		t.Errorf("Phase: got %q, want %q", payload.Phase, ResetPhaseRestarting)
	}
	if payload.Handoff != ResetHandoffWritten {
		t.Errorf("Handoff: got %q, want %q", payload.Handoff, ResetHandoffWritten)
	}

	roundTripEnvelope(t, env, payload, raw)
}

// TestResettingPayload_RestartingSkippedRoundTrip is the same edge reporting the
// other outcome. It is a fixture of its own rather than a column on the test above
// because the pair is the frame's whole point: a client can say whether a note was
// made, and both answers have to be on the wire for that claim to be checkable.
func TestResettingPayload_RestartingSkippedRoundTrip(t *testing.T) {
	raw := readFixture(t, "resetting_restarting_skipped.json")

	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if env.Type != TypeResetting {
		t.Errorf("Type: got %q, want %q", env.Type, TypeResetting)
	}

	var payload ResettingPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if !payload.Active {
		t.Errorf("Active: got %v, want true", payload.Active)
	}
	if payload.Phase != ResetPhaseRestarting {
		t.Errorf("Phase: got %q, want %q", payload.Phase, ResetPhaseRestarting)
	}
	if payload.Handoff != ResetHandoffSkipped {
		t.Errorf("Handoff: got %q, want %q — a skipped note is a reported outcome, not "+
			"a missing value", payload.Handoff, ResetHandoffSkipped)
	}

	roundTripEnvelope(t, env, payload, raw)
}

// TestResettingPayload_FallingEdgeRoundTrip pins the shape that closes every reset.
// Both closed-set fields carry the EMPTY STRING, which this wire states rather than
// omits: no omitempty means the keys are present and their zero value is the
// statement that no phase is in progress. The round trip is what makes that
// non-vacuous — an omitempty added here later for tidiness drops both keys from the
// re-marshalled bytes and reddens against the committed fixture.
func TestResettingPayload_FallingEdgeRoundTrip(t *testing.T) {
	raw := readFixture(t, "resetting_ended.json")

	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if env.Type != TypeResetting {
		t.Errorf("Type: got %q, want %q", env.Type, TypeResetting)
	}

	var payload ResettingPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload.ConversationID != "c1" {
		t.Errorf("ConversationID: got %q, want %q", payload.ConversationID, "c1")
	}
	if payload.Active {
		t.Errorf("Active: got %v, want false", payload.Active)
	}
	if payload.Phase != "" || payload.Handoff != "" {
		t.Errorf("falling edge carries Phase=%q Handoff=%q, want both empty — neither "+
			"field is meaningful once the reset is over", payload.Phase, payload.Handoff)
	}

	roundTripEnvelope(t, env, payload, raw)
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
