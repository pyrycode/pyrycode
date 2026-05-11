package protocol

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestRegisterPushTokenPayload_RoundTrip(t *testing.T) {
	raw := readFixture(t, "register_push_token.json")

	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if env.Type != TypeRegisterPushToken {
		t.Errorf("Type: got %q, want %q", env.Type, TypeRegisterPushToken)
	}

	var p RegisterPushTokenPayload
	if err := json.Unmarshal(env.Payload, &p); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if p.Platform != "fcm" {
		t.Errorf("Platform: got %q, want %q", p.Platform, "fcm")
	}
	if p.Token != "f0r..." {
		t.Errorf("Token: got %q, want %q", p.Token, "f0r...")
	}
	if p.DeviceName != "Juhana's Pixel 8" {
		t.Errorf("DeviceName: got %q, want %q", p.DeviceName, "Juhana's Pixel 8")
	}

	out, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !bytes.Equal(canonical(t, out), canonical(t, raw)) {
		t.Errorf("round-trip bytes differ:\n got: %s\nwant: %s", out, raw)
	}
}
