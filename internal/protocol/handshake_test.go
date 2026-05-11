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
