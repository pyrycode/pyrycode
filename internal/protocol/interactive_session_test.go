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
	// identity (session_id), the MCP server status that belongs to #2373's frame, and
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

func TestMCPStatusPayload_RoundTrip(t *testing.T) {
	raw := readFixture(t, "mcp_status.json")

	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if env.Type != TypeMCPStatus {
		t.Errorf("Type: got %q, want %q", env.Type, TypeMCPStatus)
	}
	assertWireKeys(t, env.Payload, "conversation_id", "servers", "dropped_servers")

	var wire struct {
		Servers []json.RawMessage `json:"servers"`
	}
	if err := json.Unmarshal(env.Payload, &wire); err != nil {
		t.Fatalf("unmarshal payload key view: %v", err)
	}
	if len(wire.Servers) != 2 {
		t.Fatalf("wire Servers: got %d entries, want 2", len(wire.Servers))
	}
	for i, entry := range wire.Servers {
		t.Run(fmt.Sprintf("wire-keys-%d", i), func(t *testing.T) {
			assertWireKeys(t, entry, "name", "status", "error", "scope", "version")
		})
	}

	var payload MCPStatusPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload.ConversationID != "conversation-mcp" {
		t.Errorf("ConversationID: got %q, want %q", payload.ConversationID, "conversation-mcp")
	}
	if len(payload.Servers) != 2 {
		t.Fatalf("Servers: got %d entries, want 2", len(payload.Servers))
	}
	wantServers := []MCPServerStatus{
		{Name: "filesystem", Status: "connected", Error: "", Scope: "local", Version: "1.4.2"},
		{Name: "remote<&>", Status: "failed", Error: "dial refused\nretry?", Scope: "project", Version: "2.0-beta"},
	}
	for i, want := range wantServers {
		got := payload.Servers[i]
		t.Run(want.Name, func(t *testing.T) {
			for _, field := range []struct {
				name string
				got  string
				want string
			}{
				{"Name", got.Name, want.Name},
				{"Status", got.Status, want.Status},
				{"Error", got.Error, want.Error},
				{"Scope", got.Scope, want.Scope},
				{"Version", got.Version, want.Version},
			} {
				if field.got != field.want {
					t.Errorf("%s: got %q, want %q", field.name, field.got, field.want)
				}
			}
		})
	}
	if payload.DroppedServers != 3 {
		t.Errorf("DroppedServers: got %d, want 3", payload.DroppedServers)
	}

	roundTripEnvelope(t, env, payload, raw)
}

func TestMCPStatusPayload_ZeroValueEncoding(t *testing.T) {
	payload := MCPStatusPayload{}
	if payload.Servers != nil {
		t.Fatalf("precondition: Servers must be nil, got %v", payload.Servers)
	}

	for _, tc := range []struct {
		name string
		in   any
	}{
		{"value", payload},
		{"pointer", &payload},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := json.Marshal(tc.in)
			if err != nil {
				t.Fatalf("marshal payload: %v", err)
			}
			assertWireKeys(t, out, "conversation_id", "servers", "dropped_servers")

			var fields map[string]json.RawMessage
			if err := json.Unmarshal(out, &fields); err != nil {
				t.Fatalf("unmarshal payload key values: %v", err)
			}
			for key, want := range map[string]string{
				"conversation_id": `""`,
				"servers":         `[]`,
				"dropped_servers": `0`,
			} {
				if got := string(fields[key]); got != want {
					t.Errorf("%s: got JSON %s, want %s", key, got, want)
				}
			}
		})
	}
	if payload.Servers != nil {
		t.Errorf("MarshalJSON mutated the receiver: Servers is now %v", payload.Servers)
	}

	entry, err := json.Marshal(MCPServerStatus{})
	if err != nil {
		t.Fatalf("marshal zero server: %v", err)
	}
	assertWireKeys(t, entry, "name", "status", "error", "scope", "version")
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(entry, &fields); err != nil {
		t.Fatalf("unmarshal server key values: %v", err)
	}
	for _, key := range []string{"name", "status", "error", "scope", "version"} {
		if got, want := string(fields[key]), `""`; got != want {
			t.Errorf("%s: got JSON %s, want %s", key, got, want)
		}
	}
}

func TestMCPStatusRequestPayload_RoundTrip(t *testing.T) {
	raw := readFixture(t, "mcp_status_request.json")

	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if env.Type != TypeMCPStatusRequest {
		t.Errorf("Type: got %q, want %q", env.Type, TypeMCPStatusRequest)
	}
	if env.InReplyTo != nil {
		t.Errorf("InReplyTo: got %v, want nil for a request", env.InReplyTo)
	}

	var payload MCPStatusRequestPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload.ConversationID != "conversation-mcp-request" {
		t.Errorf("ConversationID: got %q, want %q", payload.ConversationID, "conversation-mcp-request")
	}

	roundTripEnvelope(t, env, payload, raw)
}

func TestMCPStatusRequestPayload_WireShape(t *testing.T) {
	b, err := json.Marshal(MCPStatusRequestPayload{ConversationID: "conversation-mcp-request"})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(b, &fields); err != nil {
		t.Fatalf("unmarshal payload key set: %v", err)
	}
	if len(fields) != 1 {
		t.Fatalf("wire keys: got %v, want exactly [conversation_id]", fields)
	}
	raw, ok := fields["conversation_id"]
	if !ok {
		t.Fatalf("wire keys: got %v, want conversation_id", fields)
	}
	var conversationID string
	if err := json.Unmarshal(raw, &conversationID); err != nil {
		t.Fatalf("conversation_id JSON value is not a string: %v", err)
	}
	if conversationID != "conversation-mcp-request" {
		t.Errorf("conversation_id: got %q, want %q", conversationID, "conversation-mcp-request")
	}

	zero, err := json.Marshal(MCPStatusRequestPayload{})
	if err != nil {
		t.Fatalf("marshal zero payload: %v", err)
	}
	if got, want := string(zero), `{"conversation_id":""}`; got != want {
		t.Errorf("zero payload: got %s, want %s", got, want)
	}
}

func TestMCPReconnectPayload_RoundTrip(t *testing.T) {
	raw := readFixture(t, "mcp_reconnect.json")

	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if env.Type != TypeMCPReconnect {
		t.Errorf("Type: got %q, want %q", env.Type, TypeMCPReconnect)
	}
	if env.InReplyTo != nil {
		t.Errorf("InReplyTo: got %v, want nil for a request", env.InReplyTo)
	}

	var payload MCPReconnectPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload.ConversationID != "conversation-mcp-actuate" {
		t.Errorf("ConversationID: got %q, want %q", payload.ConversationID, "conversation-mcp-actuate")
	}
	if payload.ServerName != "context7" {
		t.Errorf("ServerName: got %q, want %q", payload.ServerName, "context7")
	}

	roundTripEnvelope(t, env, payload, raw)
}

func TestMCPReconnectPayload_WireShape(t *testing.T) {
	b, err := json.Marshal(MCPReconnectPayload{
		ConversationID: "conversation-mcp-actuate",
		ServerName:     "context7",
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(b, &fields); err != nil {
		t.Fatalf("unmarshal payload key set: %v", err)
	}
	if len(fields) != 2 {
		t.Fatalf("wire keys: got %v, want exactly [conversation_id server_name]", fields)
	}
	for key, want := range map[string]string{
		"conversation_id": "conversation-mcp-actuate",
		"server_name":     "context7",
	} {
		raw, ok := fields[key]
		if !ok {
			t.Fatalf("wire keys: got %v, want %s", fields, key)
		}
		var got string
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatalf("%s JSON value is not a string: %v", key, err)
		}
		if got != want {
			t.Errorf("%s: got %q, want %q", key, got, want)
		}
	}

	zero, err := json.Marshal(MCPReconnectPayload{})
	if err != nil {
		t.Fatalf("marshal zero payload: %v", err)
	}
	if got, want := string(zero), `{"conversation_id":"","server_name":""}`; got != want {
		t.Errorf("zero payload: got %s, want %s", got, want)
	}
}

func TestMCPTogglePayload_RoundTrip(t *testing.T) {
	raw := readFixture(t, "mcp_toggle.json")

	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if env.Type != TypeMCPToggle {
		t.Errorf("Type: got %q, want %q", env.Type, TypeMCPToggle)
	}
	if env.InReplyTo != nil {
		t.Errorf("InReplyTo: got %v, want nil for a request", env.InReplyTo)
	}

	var payload MCPTogglePayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload.ConversationID != "conversation-mcp-actuate" {
		t.Errorf("ConversationID: got %q, want %q", payload.ConversationID, "conversation-mcp-actuate")
	}
	if payload.ServerName != "context7" {
		t.Errorf("ServerName: got %q, want %q", payload.ServerName, "context7")
	}
	if !payload.Enabled {
		t.Error("Enabled: got false, want true from the fixture")
	}

	roundTripEnvelope(t, env, payload, raw)
}

func TestMCPTogglePayload_WireShape(t *testing.T) {
	b, err := json.Marshal(MCPTogglePayload{
		ConversationID: "conversation-mcp-actuate",
		ServerName:     "context7",
		Enabled:        true,
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(b, &fields); err != nil {
		t.Fatalf("unmarshal payload key set: %v", err)
	}
	if len(fields) != 3 {
		t.Fatalf("wire keys: got %v, want exactly [conversation_id server_name enabled]", fields)
	}
	for key, want := range map[string]string{
		"conversation_id": "conversation-mcp-actuate",
		"server_name":     "context7",
	} {
		raw, ok := fields[key]
		if !ok {
			t.Fatalf("wire keys: got %v, want %s", fields, key)
		}
		var got string
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatalf("%s JSON value is not a string: %v", key, err)
		}
		if got != want {
			t.Errorf("%s: got %q, want %q", key, got, want)
		}
	}
	rawEnabled, ok := fields["enabled"]
	if !ok {
		t.Fatalf("wire keys: got %v, want enabled", fields)
	}
	var enabled bool
	if err := json.Unmarshal(rawEnabled, &enabled); err != nil {
		t.Fatalf("enabled JSON value is not a bool: %v", err)
	}
	if !enabled {
		t.Errorf("enabled: got %v, want true", enabled)
	}

	// The zero value pins the two properties the plain-bool choice buys: the key is
	// always present (so the fixture pins a complete shape), and its absent reading
	// is false — "disable", the non-escalating direction.
	zero, err := json.Marshal(MCPTogglePayload{})
	if err != nil {
		t.Fatalf("marshal zero payload: %v", err)
	}
	if got, want := string(zero), `{"conversation_id":"","server_name":"","enabled":false}`; got != want {
		t.Errorf("zero payload: got %s, want %s", got, want)
	}

	var omitted MCPTogglePayload
	if err := json.Unmarshal([]byte(`{"conversation_id":"c","server_name":"s"}`), &omitted); err != nil {
		t.Fatalf("unmarshal payload with enabled omitted: %v", err)
	}
	if omitted.Enabled {
		t.Error("an omitted enabled decoded as true; it must read as false (disable)")
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

func TestModelRefusalNoFallbackPayload_RoundTrip(t *testing.T) {
	raw := readFixture(t, "model_refusal_no_fallback.json")

	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if env.Type != TypeModelRefusalNoFallback {
		t.Errorf("Type: got %q, want %q", env.Type, TypeModelRefusalNoFallback)
	}

	var payload ModelRefusalNoFallbackPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload.ConversationID != "c1" || payload.OriginalModel != "claude-opus-4-1" {
		t.Errorf("identity fields: got conversation_id=%q original_model=%q", payload.ConversationID, payload.OriginalModel)
	}
	if payload.RefusalCategory != "cyber" || payload.Banner != "Claude refused this request…" {
		t.Errorf("refusal fields: got category=%q banner=%q", payload.RefusalCategory, payload.Banner)
	}
	if want := []string{"banner"}; !reflect.DeepEqual(payload.TruncatedFields, want) {
		t.Errorf("TruncatedFields: got %v, want %v", payload.TruncatedFields, want)
	}
	if want := []string{"original_model", "refusal_category"}; !reflect.DeepEqual(payload.DroppedFields, want) {
		t.Errorf("DroppedFields: got %v, want %v", payload.DroppedFields, want)
	}

	var keys map[string]json.RawMessage
	if err := json.Unmarshal(env.Payload, &keys); err != nil {
		t.Fatalf("re-decode payload keys: %v", err)
	}
	wantKeys := map[string]bool{
		"conversation_id": true, "original_model": true, "refusal_category": true,
		"banner": true, "truncated_fields": true, "dropped_fields": true,
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

func TestModelRefusalNoFallbackPayload_EmptyReports_RoundTrip(t *testing.T) {
	raw := readFixture(t, "model_refusal_no_fallback_empty_reports.json")
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
	var payload ModelRefusalNoFallbackPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload.TruncatedFields != nil || payload.DroppedFields != nil {
		t.Fatalf("empty reports: got truncated=%v dropped=%v, want both nil", payload.TruncatedFields, payload.DroppedFields)
	}

	roundTripEnvelope(t, env, payload, raw)
}

// contextUsageWireKeys is the published top-level key set of a context_usage
// payload. It is spelled once and shared by the populated, empty-fixture and
// zero-value tests so all three assert the same contract: assertWireKeys rejects
// an unexpected key as well as a missing one, so this list is what fails a
// silently added or renamed field.
var contextUsageWireKeys = []string{
	"conversation_id", "model", "total_tokens", "max_tokens", "percentage",
	"categories", "dropped_categories",
	"mcp_tools", "dropped_mcp_tools",
	"memory_files", "dropped_memory_files",
}

func TestContextUsagePayload_RoundTrip(t *testing.T) {
	raw := readFixture(t, "context_usage.json")

	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if env.Type != TypeContextUsage {
		t.Errorf("Type: got %q, want %q", env.Type, TypeContextUsage)
	}
	assertWireKeys(t, env.Payload, contextUsageWireKeys...)

	// Row-level key sets, asserted against the raw wire rather than the decoded
	// struct: a row type that grew a field would still decode fine here.
	var wire struct {
		Categories  []json.RawMessage `json:"categories"`
		MCPTools    []json.RawMessage `json:"mcp_tools"`
		MemoryFiles []json.RawMessage `json:"memory_files"`
	}
	if err := json.Unmarshal(env.Payload, &wire); err != nil {
		t.Fatalf("unmarshal payload key view: %v", err)
	}
	for _, list := range []struct {
		name string
		rows []json.RawMessage
		keys []string
	}{
		{"categories", wire.Categories, []string{"name", "tokens"}},
		{"mcp_tools", wire.MCPTools, []string{"name", "server_name", "tokens"}},
		{"memory_files", wire.MemoryFiles, []string{"path", "type", "tokens"}},
	} {
		if len(list.rows) != 2 {
			t.Fatalf("wire %s: got %d entries, want 2", list.name, len(list.rows))
		}
		for i, row := range list.rows {
			t.Run(fmt.Sprintf("wire-keys-%s-%d", list.name, i), func(t *testing.T) {
				assertWireKeys(t, row, list.keys...)
			})
		}
	}

	var payload ContextUsagePayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}

	want := ContextUsagePayload{
		ConversationID: "conversation-context",
		Model:          "claude-opus-5",
		TotalTokens:    128400,
		MaxTokens:      200000,
		Percentage:     64,
		Categories: []ContextUsageCategory{
			{Name: "System prompt", Tokens: 41200},
			// Markup metacharacters survive verbatim: this layer neither
			// validates nor sanitizes claude-authored descriptive text.
			{Name: "Messages <&>", Tokens: 9800},
		},
		DroppedCategories: 3,
		MCPTools: []ContextUsageMCPTool{
			{Name: "read_file", ServerName: "filesystem", Tokens: 1450},
			{Name: "query\ndocs", ServerName: "remote<mcp>", Tokens: 620},
		},
		DroppedMCPTools: 5,
		MemoryFiles: []ContextUsageMemoryFile{
			{Path: "/Users/dev/project/CLAUDE.md", Type: "project", Tokens: 3100},
			// A traversal-shaped path crosses unchanged. Nothing on this path
			// joins, cleans, resolves or opens it — see the doc comment on
			// ContextUsageMemoryFile.
			{Path: "../../../etc/passwd", Type: "user", Tokens: 240},
		},
		DroppedMemoryFiles: 7,
	}
	if !reflect.DeepEqual(payload, want) {
		t.Errorf("payload:\n got %+v\nwant %+v", payload, want)
	}

	// The three dropped counts are asserted separately and given mutually
	// distinct fixture values, so a mapper that cross-wires two of them fails
	// here rather than passing on a coincidence.
	for _, tc := range []struct {
		name string
		got  int
		want int
	}{
		{"DroppedCategories", payload.DroppedCategories, 3},
		{"DroppedMCPTools", payload.DroppedMCPTools, 5},
		{"DroppedMemoryFiles", payload.DroppedMemoryFiles, 7},
	} {
		if tc.got != tc.want {
			t.Errorf("%s: got %d, want %d", tc.name, tc.got, tc.want)
		}
	}
	// Each dropped count is independent of its list's length: the retained rows
	// say nothing about how many were cut.
	if len(payload.Categories) == payload.DroppedCategories {
		t.Errorf("fixture is too weak: len(Categories) must differ from DroppedCategories")
	}

	roundTripEnvelope(t, env, payload, raw)
}

func TestContextUsagePayload_EmptyFixtureRoundTrip(t *testing.T) {
	raw := readFixture(t, "context_usage_empty.json")

	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if env.Type != TypeContextUsage {
		t.Errorf("Type: got %q, want %q", env.Type, TypeContextUsage)
	}
	assertWireKeys(t, env.Payload, contextUsageWireKeys...)

	var payload ContextUsagePayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	for _, tc := range []struct {
		name string
		got  int
	}{
		{"Categories", len(payload.Categories)},
		{"MCPTools", len(payload.MCPTools)},
		{"MemoryFiles", len(payload.MemoryFiles)},
	} {
		if tc.got != 0 {
			t.Errorf("%s: got %d entries, want 0", tc.name, tc.got)
		}
	}
	if payload.Categories == nil || payload.MCPTools == nil || payload.MemoryFiles == nil {
		t.Errorf("an empty wire array must decode to a non-nil empty slice: %+v", payload)
	}
	if want := (ContextUsagePayload{
		Categories:  []ContextUsageCategory{},
		MCPTools:    []ContextUsageMCPTool{},
		MemoryFiles: []ContextUsageMemoryFile{},
	}); !reflect.DeepEqual(payload, want) {
		t.Errorf("payload:\n got %+v\nwant %+v", payload, want)
	}

	roundTripEnvelope(t, env, payload, raw)
}

func TestContextUsagePayload_ZeroValueEncoding(t *testing.T) {
	payload := ContextUsagePayload{}
	if payload.Categories != nil || payload.MCPTools != nil || payload.MemoryFiles != nil {
		t.Fatalf("precondition: all three slices must be nil, got %+v", payload)
	}

	for _, tc := range []struct {
		name string
		in   any
	}{
		{"value", payload},
		{"pointer", &payload},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := json.Marshal(tc.in)
			if err != nil {
				t.Fatalf("marshal payload: %v", err)
			}
			assertWireKeys(t, out, contextUsageWireKeys...)

			var fields map[string]json.RawMessage
			if err := json.Unmarshal(out, &fields); err != nil {
				t.Fatalf("unmarshal payload key values: %v", err)
			}
			for key, want := range map[string]string{
				"conversation_id":      `""`,
				"model":                `""`,
				"total_tokens":         `0`,
				"max_tokens":           `0`,
				"percentage":           `0`,
				"categories":           `[]`,
				"dropped_categories":   `0`,
				"mcp_tools":            `[]`,
				"dropped_mcp_tools":    `0`,
				"memory_files":         `[]`,
				"dropped_memory_files": `0`,
			} {
				if got := string(fields[key]); got != want {
					t.Errorf("%s: got JSON %s, want %s", key, got, want)
				}
			}
		})
	}
	if payload.Categories != nil || payload.MCPTools != nil || payload.MemoryFiles != nil {
		t.Errorf("MarshalJSON mutated the receiver: %+v", payload)
	}

	for _, tc := range []struct {
		name string
		in   any
		keys []string
	}{
		{"category", ContextUsageCategory{}, []string{"name", "tokens"}},
		{"mcp-tool", ContextUsageMCPTool{}, []string{"name", "server_name", "tokens"}},
		{"memory-file", ContextUsageMemoryFile{}, []string{"path", "type", "tokens"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			row, err := json.Marshal(tc.in)
			if err != nil {
				t.Fatalf("marshal zero row: %v", err)
			}
			assertWireKeys(t, row, tc.keys...)

			var fields map[string]json.RawMessage
			if err := json.Unmarshal(row, &fields); err != nil {
				t.Fatalf("unmarshal row key values: %v", err)
			}
			for _, key := range tc.keys {
				want := `""`
				if key == "tokens" {
					want = `0`
				}
				if got := string(fields[key]); got != want {
					t.Errorf("%s: got JSON %s, want %s", key, got, want)
				}
			}
		})
	}
}

// TestContextUsagePayload_AsOf pins the REMEMBERED-answer key (#2461) from both
// sides, and the two sides are one contract rather than two tests that happen to be
// adjacent.
//
// The absent side is already asserted three times over — contextUsageWireKeys is
// shared by the populated, empty-fixture and zero-value tests, and assertWireKeys
// rejects an unexpected key as well as a missing one, so a field that lost its
// omitempty reddens all three. What this test adds is the PRESENT side: exactly one
// extra key, spelled as_of, carrying the RFC 3339 Z form, and decoding back to an
// equal instant.
//
// THE Z FORM IS NOT PRODUCED HERE. time.Time marshals with whatever offset it
// carries, and the normalisation lives at the one door that stores a reading,
// conversations.Registry.SetLastContextUsage. This test feeds a non-UTC instant
// through the payload to state that boundary out loud: this layer preserves the
// offset it is handed rather than forcing one, so a producer that skipped the door
// would emit a local offset and no assertion here would catch it — which is why the
// door is where it is.
func TestContextUsagePayload_AsOf(t *testing.T) {
	stored := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	payload := ContextUsagePayload{
		ConversationID: "conversation-remembered",
		Model:          "claude-opus-5",
		TotalTokens:    31337,
		MaxTokens:      200000,
		Percentage:     16,
		AsOf:           &stored,
	}

	out, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	assertWireKeys(t, out, append(append([]string{}, contextUsageWireKeys...), "as_of")...)

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(out, &fields); err != nil {
		t.Fatalf("unmarshal payload key values: %v", err)
	}
	if got, want := string(fields["as_of"]), `"2026-09-14T12:00:00Z"`; got != want {
		t.Errorf("as_of: got JSON %s, want %s", got, want)
	}
	// A remembered answer stores no inventories, and the three keys must still be
	// present and empty rather than null — the property that makes as_of necessary
	// in the first place, since an empty inventory is otherwise a positive reading.
	for _, key := range []string{"categories", "mcp_tools", "memory_files"} {
		if got := string(fields[key]); got != `[]` {
			t.Errorf("%s: got JSON %s, want []", key, got)
		}
	}

	var back ContextUsagePayload
	if err := json.Unmarshal(out, &back); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if back.AsOf == nil {
		t.Fatal("as_of decoded to nil")
	}
	if !back.AsOf.Equal(stored) {
		t.Errorf("as_of round trip: got %v, want %v", *back.AsOf, stored)
	}

	// The offset is carried, not normalised: a caller handing this layer a non-UTC
	// instant gets one back on the wire. The one door that stores a reading is what
	// makes the daemon's own frames Z-formed.
	local := stored.In(time.FixedZone("UTC+3", 3*60*60))
	payload.AsOf = &local
	out, err = json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal non-UTC payload: %v", err)
	}
	if err := json.Unmarshal(out, &fields); err != nil {
		t.Fatalf("unmarshal non-UTC payload: %v", err)
	}
	if got, want := string(fields["as_of"]), `"2026-09-14T15:00:00+03:00"`; got != want {
		t.Errorf("non-UTC as_of: got JSON %s, want %s", got, want)
	}
}

// TestRequestContextUsagePayload_WireKeys pins the payload's COMPLETE set of wire
// keys (#2431). Its reason is TestRequestModelListPayload_WireKeys': correlation
// rides the envelope's InReplyTo, so this frame carries NO request-id key, and
// marshalling a freshly populated struct is what exercises the struct tags at all.
//
// It also pins the absence of a detail key. The daemon asks claude at
// detail:"full" and that choice is the DAEMON'S — a client cannot select the cheap
// reading through this verb, because a request that could would let one client
// downgrade what another is shown for the same conversation once the asks collapse.
func TestRequestContextUsagePayload_WireKeys(t *testing.T) {
	b, err := json.Marshal(RequestContextUsagePayload{ConversationID: "c1"})
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
			t.Errorf("unexpected wire key %q: the payload's key set is fixed at %v — correlation rides the envelope's in_reply_to, and the reading's detail is the daemon's choice, not the client's", k, want)
		}
	}
	for k := range want {
		if _, ok := got[k]; !ok {
			t.Errorf("missing wire key %q, got: %s — the key is always present (no omitempty), so absent and empty stay the same case", k, b)
		}
	}
}

// TestRequestContextUsagePayload_ZeroValue_KeyPresent pins the no-omitempty
// decision directly: a zero-valued payload still carries the key. Separate from
// the key-set test above because the two fail on different mutants — that one
// marshals a POPULATED struct, so an omitempty added later leaves it green.
//
// The zero value is a REACHABLE state on this path, not a hypothetical: the relay
// handler tolerates a payload decode failure rather than minting a third reject
// code, and what it is left holding is exactly this.
func TestRequestContextUsagePayload_ZeroValue_KeyPresent(t *testing.T) {
	b, err := json.Marshal(RequestContextUsagePayload{})
	if err != nil {
		t.Fatalf("marshal zero payload: %v", err)
	}
	if got, want := string(b), `{"conversation_id":""}`; got != want {
		t.Errorf("zero payload: got %s, want %s — an omitempty here would make absent and empty distinguishable on a frame whose two cases are deliberately the same", got, want)
	}
}

// TestRequestContextUsageWireConstants pins the three vocabulary strings #2431
// mints. The VALUES are the contract — a rename is a wire break for every client —
// so they are asserted literally rather than against each other.
func TestRequestContextUsageWireConstants(t *testing.T) {
	for name, pair := range map[string][2]string{
		"TypeRequestContextUsage":     {TypeRequestContextUsage, "request_context_usage"},
		"CodeContextUsageUnavailable": {CodeContextUsageUnavailable, "context_usage.unavailable"},
		"CapabilityContextUsage":      {CapabilityContextUsage, "context_usage"},
	} {
		if pair[0] != pair[1] {
			t.Errorf("%s: got %q, want %q", name, pair[0], pair[1])
		}
	}

	// The answer is the EXISTING frame, not a second outbound shape. A verb that
	// minted its own reply type would leave two shapes for one reading, which is
	// exactly what TypeContextUsage's declaration forbids.
	if TypeRequestContextUsage == TypeContextUsage {
		t.Error("the request verb and the reply type must be distinct strings")
	}
}
