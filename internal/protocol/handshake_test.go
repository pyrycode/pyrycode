package protocol

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"
)

func TestHelloServerPayload_RoundTrip(t *testing.T) {
	raw := readFixture(t, "hello_server.json")

	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if env.Type != TypeHello {
		t.Errorf("Type: got %q, want %q", env.Type, TypeHello)
	}

	var payload HelloServerPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload.Role != "server" {
		t.Errorf("Role: got %q, want %q", payload.Role, "server")
	}
	if payload.ServerID != "8f7e" {
		t.Errorf("ServerID: got %q, want %q", payload.ServerID, "8f7e")
	}
	if payload.BinaryVersion != "0.10.0" {
		t.Errorf("BinaryVersion: got %q, want %q", payload.BinaryVersion, "0.10.0")
	}
	if len(payload.ProtocolVersions) != 1 || payload.ProtocolVersions[0] != "v1" {
		t.Errorf("ProtocolVersions: got %v, want [v1]", payload.ProtocolVersions)
	}

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

func TestHelloClientPayload_RoundTrip(t *testing.T) {
	raw := readFixture(t, "hello_client.json")

	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if env.Type != TypeHello {
		t.Errorf("Type: got %q, want %q", env.Type, TypeHello)
	}

	var payload HelloClientPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload.Role != "client" {
		t.Errorf("Role: got %q, want %q", payload.Role, "client")
	}
	if payload.DeviceName != "Juhana's Pixel 8" {
		t.Errorf("DeviceName: got %q, want %q", payload.DeviceName, "Juhana's Pixel 8")
	}
	if payload.ClientVersion != "pyrycode-mobile 0.1.0" {
		t.Errorf("ClientVersion: got %q, want %q", payload.ClientVersion, "pyrycode-mobile 0.1.0")
	}
	wantTS, err := time.Parse(time.RFC3339Nano, "2026-05-08T08:14:02Z")
	if err != nil {
		t.Fatalf("parse expected last_seen_ts: %v", err)
	}
	if payload.LastSeenTS == nil {
		t.Fatalf("LastSeenTS: got nil, want pointer to %v", wantTS)
	}
	if !payload.LastSeenTS.Equal(wantTS) {
		t.Errorf("LastSeenTS: got %v, want %v", *payload.LastSeenTS, wantTS)
	}

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

func TestHelloAckPayload_RoundTrip(t *testing.T) {
	raw := readFixture(t, "hello_ack.json")

	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if env.Type != TypeHelloAck {
		t.Errorf("Type: got %q, want %q", env.Type, TypeHelloAck)
	}
	if env.InReplyTo == nil || *env.InReplyTo != 1 {
		t.Errorf("InReplyTo: got %v, want pointer to 1", env.InReplyTo)
	}

	var payload HelloAckPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload.ProtocolVersion != "v1" {
		t.Errorf("ProtocolVersion: got %q, want %q", payload.ProtocolVersion, "v1")
	}
	if payload.ServerID != "8f7e" {
		t.Errorf("ServerID: got %q, want %q", payload.ServerID, "8f7e")
	}
	if payload.ConnID != "c-7f3a" {
		t.Errorf("ConnID: got %q, want %q", payload.ConnID, "c-7f3a")
	}

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

// TestHelloClientPayload_CapabilitiesRoundTrip pins that an advertised
// capability set survives marshal→unmarshal, and that an empty/nil set is
// omitted from the wire (the omitempty byte-stability lever — the
// "capabilities" key must be absent, not null, so the existing
// hello_client.json fixture round-trips byte-identically).
func TestHelloClientPayload_CapabilitiesRoundTrip(t *testing.T) {
	empty := HelloClientPayload{Role: "client", DeviceName: "phone", ClientVersion: "v"}
	out, err := json.Marshal(empty)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if bytes.Contains(out, []byte(`"capabilities"`)) {
		t.Errorf("empty Capabilities should be omitted; got %s", out)
	}

	adv := HelloClientPayload{
		Role:          "client",
		DeviceName:    "phone",
		ClientVersion: "v",
		Capabilities:  []string{CapabilityInteractive},
	}
	out, err = json.Marshal(adv)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back HelloClientPayload
	if err := json.Unmarshal(out, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(back.Capabilities) != 1 || back.Capabilities[0] != CapabilityInteractive {
		t.Errorf("Capabilities round-trip: got %v, want [%q]", back.Capabilities, CapabilityInteractive)
	}
}

// TestHelloClientPayload_LastEventIDRoundTrip pins the #647 inbound replay
// cursor: an absent last_event_id is omitted from the wire (key absent, not
// null — the byte-stability lever so today's hello round-trips identically),
// and a set value round-trips to a pointer to the same id.
func TestHelloClientPayload_LastEventIDRoundTrip(t *testing.T) {
	absent := HelloClientPayload{Role: "client", DeviceName: "phone", ClientVersion: "v"}
	out, err := json.Marshal(absent)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if bytes.Contains(out, []byte(`"last_event_id"`)) {
		t.Errorf("absent LastEventID should be omitted; got %s", out)
	}

	id := uint64(42)
	adv := HelloClientPayload{
		Role:          "client",
		DeviceName:    "phone",
		ClientVersion: "v",
		LastEventID:   &id,
	}
	out, err = json.Marshal(adv)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !bytes.Contains(out, []byte(`"last_event_id":42`)) {
		t.Errorf("set LastEventID should encode as last_event_id:42; got %s", out)
	}
	var back HelloClientPayload
	if err := json.Unmarshal(out, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.LastEventID == nil || *back.LastEventID != id {
		t.Errorf("LastEventID round-trip: got %v, want pointer to %d", back.LastEventID, id)
	}
}

// TestHelloAckPayload_CapabilitiesRoundTrip pins the same for the daemon's
// supported-set echo on hello_ack.
func TestHelloAckPayload_CapabilitiesRoundTrip(t *testing.T) {
	empty := HelloAckPayload{ProtocolVersion: "v2", ServerID: "8f7e", ConnID: "c-1"}
	out, err := json.Marshal(empty)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if bytes.Contains(out, []byte(`"capabilities"`)) {
		t.Errorf("empty Capabilities should be omitted; got %s", out)
	}

	ack := HelloAckPayload{
		ProtocolVersion: "v2",
		ServerID:        "8f7e",
		ConnID:          "c-1",
		Capabilities:    []string{CapabilityInteractive},
	}
	out, err = json.Marshal(ack)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back HelloAckPayload
	if err := json.Unmarshal(out, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(back.Capabilities) != 1 || back.Capabilities[0] != CapabilityInteractive {
		t.Errorf("Capabilities round-trip: got %v, want [%q]", back.Capabilities, CapabilityInteractive)
	}
}

func TestHelloAckPayload_WorkspaceRootRoundTrip(t *testing.T) {
	empty := HelloAckPayload{ProtocolVersion: "v2", ServerID: "8f7e", ConnID: "c-1"}
	out, err := json.Marshal(empty)
	if err != nil {
		t.Fatalf("marshal empty payload: %v", err)
	}
	if bytes.Contains(out, []byte(`"workspace_root"`)) {
		t.Errorf("empty WorkspaceRoot should be omitted; got %s", out)
	}

	want := "/Users/tester/pyry-workspace"
	ack := HelloAckPayload{
		ProtocolVersion: "v2",
		ServerID:        "8f7e",
		ConnID:          "c-1",
		WorkspaceRoot:   want,
	}
	out, err = json.Marshal(ack)
	if err != nil {
		t.Fatalf("marshal populated payload: %v", err)
	}
	var back HelloAckPayload
	if err := json.Unmarshal(out, &back); err != nil {
		t.Fatalf("unmarshal populated payload: %v", err)
	}
	if back.WorkspaceRoot != want {
		t.Errorf("WorkspaceRoot round-trip: got %q, want %q", back.WorkspaceRoot, want)
	}
}

// TestCapability_Constants_MatchSpec pins each capability constant to its exact
// wire string, the same shape TestErrorCode_Constants_MatchSpec uses for the
// Code* block and for the same reason: every other test in this repo passes
// these constants symbolically on both the advertise and the expect side, so a
// fat-fingered value is self-consistent and stays green everywhere. That is
// worse than a typo in most constants, because the capability set is a
// cross-repo contract — a client outside this module compares against the
// literal string, and the whole point of `question` (#2020) is letting it detect
// which daemon build it is talking to. Add a line here with every new capability.
func TestCapability_Constants_MatchSpec(t *testing.T) {
	got := map[string]string{
		"CapabilityInteractive":  CapabilityInteractive,
		"CapabilityQuestion":     CapabilityQuestion,
		"CapabilityModelList":    CapabilityModelList,
		"CapabilityContextUsage": CapabilityContextUsage,
	}
	want := map[string]string{
		"CapabilityInteractive":  "interactive",
		"CapabilityQuestion":     "question",
		"CapabilityModelList":    "model_list",
		"CapabilityContextUsage": "context_usage",
	}
	if len(got) != len(want) {
		t.Fatalf("capability constant count: got %d, want %d", len(got), len(want))
	}
	for name, spec := range want {
		if got[name] != spec {
			t.Errorf("%s = %q, want %q", name, got[name], spec)
		}
	}
}

func TestErrorPayload_RoundTrip(t *testing.T) {
	raw := readFixture(t, "error.json")

	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if env.Type != TypeError {
		t.Errorf("Type: got %q, want %q", env.Type, TypeError)
	}

	var payload ErrorPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload.Code != CodeAuthInvalidToken {
		t.Errorf("Code: got %q, want %q", payload.Code, CodeAuthInvalidToken)
	}
	if payload.Retryable {
		t.Errorf("Retryable: got true, want false")
	}
	if payload.RetryAfterS != nil {
		t.Errorf("RetryAfterS: got %v, want nil (omitempty)", payload.RetryAfterS)
	}

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

// TestErrorPayload_ConversationIDIsOptional pins both halves of #2443's addition
// to this shared payload. Shared is the operative word: every error reply on every
// v2 verb marshals through this struct, so an unset field that emitted a key would
// change the wire shape of frames that have nothing to do with new_session.
//
// The round-trip test above already proves the fixture is byte-stable; what is
// pinned here is that the KEY is absent rather than empty, which is what a client
// distinguishing "no subject named" from "subject named as nothing" depends on.
func TestErrorPayload_ConversationIDIsOptional(t *testing.T) {
	t.Parallel()

	unset, err := json.Marshal(ErrorPayload{Code: CodeProtocolMalformed, Message: "nope"})
	if err != nil {
		t.Fatalf("marshal unset: %v", err)
	}
	if bytes.Contains(unset, []byte("conversation_id")) {
		t.Errorf("an unset ConversationID emitted the key: %s — every pre-#2443 error reply must stay "+
			"byte-identical", unset)
	}

	const conv = "44444444-4444-4444-8444-444444444444"
	set, err := json.Marshal(ErrorPayload{
		Code:           CodeNewSessionWorkspaceRefused,
		Message:        "nope",
		ConversationID: conv,
	})
	if err != nil {
		t.Fatalf("marshal set: %v", err)
	}
	var back ErrorPayload
	if err := json.Unmarshal(set, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.ConversationID != conv {
		t.Errorf("ConversationID round-tripped as %q, want %q", back.ConversationID, conv)
	}
	if !bytes.Contains(set, []byte(`"conversation_id":"`+conv+`"`)) {
		t.Errorf("wire key is not conversation_id: %s", set)
	}
}

// TestErrorPayload_MinClientVersionIsOptional pins #2576's addition to the shared
// error payload, for the reason the ConversationID test above gives: every error
// reply marshals through this struct, so an unset field must emit no key and leave
// every existing error frame byte-identical. Set, it carries the three-part minimum
// the app-too-old rejection names.
func TestErrorPayload_MinClientVersionIsOptional(t *testing.T) {
	t.Parallel()

	unset, err := json.Marshal(ErrorPayload{Code: CodeProtocolMalformed, Message: "nope"})
	if err != nil {
		t.Fatalf("marshal unset: %v", err)
	}
	if bytes.Contains(unset, []byte("min_client_version")) {
		t.Errorf("an unset MinClientVersion emitted the key: %s — every pre-#2576 error reply must stay "+
			"byte-identical", unset)
	}

	const minVersion = "1.4.0"
	set, err := json.Marshal(ErrorPayload{
		Code:             CodeClientUpdateRequired,
		Message:          "nope",
		MinClientVersion: minVersion,
	})
	if err != nil {
		t.Fatalf("marshal set: %v", err)
	}
	var back ErrorPayload
	if err := json.Unmarshal(set, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.MinClientVersion != minVersion {
		t.Errorf("MinClientVersion round-tripped as %q, want %q", back.MinClientVersion, minVersion)
	}
	if !bytes.Contains(set, []byte(`"min_client_version":"`+minVersion+`"`)) {
		t.Errorf("wire key is not min_client_version: %s", set)
	}
}

func TestAckPayload_RoundTrip(t *testing.T) {
	raw := readFixture(t, "ack.json")

	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if env.Type != TypeAck {
		t.Errorf("Type: got %q, want %q", env.Type, TypeAck)
	}

	var payload AckPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}

	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	if string(payloadBytes) != "{}" {
		t.Errorf("marshalled AckPayload: got %s, want {}", payloadBytes)
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
