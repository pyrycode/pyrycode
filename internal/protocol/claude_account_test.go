package protocol

import (
	"encoding/json"
	"reflect"
	"testing"
)

var claudeAccountKeys = []string{"kind", "label", "state", "reason"}

func TestClaudeAccountPayloads_RoundTrip(t *testing.T) {
	t.Parallel()
	cases := []struct {
		fixture string
		typ     string
		replyID uint64
		want    any
		keys    []string
	}{
		{"request_claude_account.json", TypeRequestClaudeAccount, 0, &RequestClaudeAccountPayload{}, nil},
		{"claude_account_machine_login.json", TypeClaudeAccount, 300, &ClaudeAccountPayload{
			Kind: ClaudeAccountKindMachineLogin, State: ClaudeAccountStateNotConfigured,
		}, claudeAccountKeys},
		{"claude_account_file_ready.json", TypeClaudeAccount, 300, &ClaudeAccountPayload{
			Kind: ClaudeAccountKindFile, Label: "Work account", State: ClaudeAccountStateReady,
		}, claudeAccountKeys},
		{"claude_account_file_failed.json", TypeClaudeAccount, 300, &ClaudeAccountPayload{
			Kind: ClaudeAccountKindFile, State: ClaudeAccountStateFailed, Reason: "token file not readable",
		}, claudeAccountKeys},
	}
	for _, tc := range cases {
		t.Run(tc.fixture, func(t *testing.T) {
			t.Parallel()
			raw := readFixture(t, tc.fixture)
			var env Envelope
			if err := json.Unmarshal(raw, &env); err != nil {
				t.Fatalf("decode envelope: %v", err)
			}
			if env.Type != tc.typ {
				t.Fatalf("type = %q, want %q", env.Type, tc.typ)
			}
			if tc.replyID == 0 {
				if env.InReplyTo != nil {
					t.Fatalf("request in_reply_to = %d, want absent", *env.InReplyTo)
				}
			} else if env.InReplyTo == nil || *env.InReplyTo != tc.replyID {
				t.Fatalf("in_reply_to = %v, want %d", env.InReplyTo, tc.replyID)
			}
			// The committed example itself carries every key, empty ones included.
			var wire map[string]any
			if err := json.Unmarshal(env.Payload, &wire); err != nil {
				t.Fatalf("decode payload keys: %v", err)
			}
			if len(wire) != len(tc.keys) {
				t.Fatalf("fixture payload = %v, want exactly keys %v", wire, tc.keys)
			}
			for _, k := range tc.keys {
				if _, ok := wire[k].(string); !ok {
					t.Fatalf("fixture key %q = %#v, want a present string", k, wire[k])
				}
			}
			got := reflect.New(reflect.TypeOf(tc.want).Elem()).Interface()
			if err := json.Unmarshal(env.Payload, got); err != nil {
				t.Fatalf("decode payload: %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("payload = %#v, want %#v", got, tc.want)
			}
			roundTripEnvelope(t, env, got, raw)
		})
	}
}

// Constructed values pin the entire key set independently of fixture updates.
func TestClaudeAccountPayloads_WireKeys(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		payload any
		want    map[string]any
	}{
		{"request", RequestClaudeAccountPayload{}, map[string]any{}},
		{"zero reply", ClaudeAccountPayload{}, map[string]any{"kind": "", "label": "", "state": "", "reason": ""}},
		{"machine login", ClaudeAccountPayload{Kind: ClaudeAccountKindMachineLogin, State: ClaudeAccountStateNotConfigured},
			map[string]any{"kind": "machine_login", "label": "", "state": "not_configured", "reason": ""}},
		{"1password failed", ClaudeAccountPayload{Kind: ClaudeAccountKindOnePassword, Label: "Team", State: ClaudeAccountStateFailed, Reason: "vault locked"},
			map[string]any{"kind": "1password", "label": "Team", "state": "failed", "reason": "vault locked"}},
		{"os keychain ready", ClaudeAccountPayload{Kind: ClaudeAccountKindOSKeychain, State: ClaudeAccountStateReady},
			map[string]any{"kind": "os_keychain", "label": "", "state": "ready", "reason": ""}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			raw, err := json.Marshal(tc.payload)
			if err != nil {
				t.Fatalf("marshal payload: %v", err)
			}
			var got map[string]any
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatalf("decode keys: %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("wire fields = %#v, want %#v", got, tc.want)
			}
		})
	}
}

// A client must accept a kind it does not know; the DTO carries it verbatim.
func TestClaudeAccountPayload_UnknownKindDecodes(t *testing.T) {
	t.Parallel()
	var got ClaudeAccountPayload
	raw := `{"kind":"hardware_token","label":"","state":"ready","reason":""}`
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("decode unknown kind: %v", err)
	}
	if got.Kind != "hardware_token" || got.State != ClaudeAccountStateReady {
		t.Errorf("payload = %#v, want unknown kind preserved", got)
	}
}
