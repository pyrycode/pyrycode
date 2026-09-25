package relay

import (
	"bytes"
	"encoding/json"
	"reflect"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/protocol"
)

// fixtureAgentCaps is what the capability seam double reports for the fixture
// session: one agent's half of the list (#2646).
var fixtureAgentCaps = AgentCapabilities{
	Interrupt:          true,
	MidTurnInput:       false,
	SlashCommands:      true,
	MCPServers:         true,
	ContextUsageDetail: true,
	EffortLevels:       []string{"low", "high"},
	Models:             []string{"opus", "sonnet"},
}

// capsCall is one consultation of the capability seam.
type capsCall struct{ sessionID, model string }

// capsSeams wires the fixture run configuration plus a capability seam that
// records every call and answers with agent/ok.
func capsSeams(agent AgentCapabilities, ok bool) (readSeams, func() []capsCall) {
	var mu sync.Mutex
	var calls []capsCall
	seams := allReadSeams()
	seams.capabilities = func(sessionID, model string) (AgentCapabilities, bool) {
		mu.Lock()
		calls = append(calls, capsCall{sessionID, model})
		mu.Unlock()
		return agent, ok
	}
	return seams, func() []capsCall {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(calls)
	}
}

// requestSettingsAs sends one request_session_settings naming convID from a conn
// that negotiated caps, and returns the reply's raw payload.
func requestSettingsAs(t *testing.T, seams readSeams, caps []string, convID string) json.RawMessage {
	t.Helper()
	mgr, frames, rec, respPub := readManagerFor(t, seams, silentLogger())
	send, recv := openModalConn(t, mgr, frames, rec, respPub, "c-caps", caps)
	frames <- sealAppFrameConn(t, send, "c-caps", protocol.Envelope{
		ID:      91,
		Type:    protocol.TypeRequestSessionSettings,
		TS:      time.Now().UTC(),
		Payload: json.RawMessage(`{"conversation_id":"` + convID + `"}`),
	})
	waitForConnNoiseMsg(t, rec, "c-caps", 1)
	msgs := noiseMsgsForConn(t, rec, "c-caps")
	if len(msgs) != 1 {
		t.Fatalf("got %d app frame(s), want exactly 1 reply", len(msgs))
	}
	reply := decryptAppFrame(t, msgs[0], recv)
	if reply.Type != protocol.TypeSessionSettings {
		t.Fatalf("reply Type = %q, want %q", reply.Type, protocol.TypeSessionSettings)
	}
	return reply.Payload
}

var (
	oldClientCaps     = []string{protocol.CapabilityInteractive}
	multiAgentCaps    = []string{protocol.CapabilityInteractive, protocol.CapabilityMultiAgent}
	wantFixtureCapObj = protocol.SessionCapabilities{
		Interrupt:          true,
		MidTurnInput:       false,
		SlashCommands:      true,
		MCPServers:         true,
		ContextUsageDetail: true,
		EffortLevels:       []string{"low", "high"},
		PermissionModes:    []string{"default", "acceptEdits", "plan", "auto", "dontAsk"},
		AttachmentTypes:    []string{"*/*"},
		Models:             []string{"opus", "sonnet"},
	}
)

// #2646: a multi_agent conn's reply about a resolved session carries the
// capability object, composed from the seam's agent half (asked with the
// RESOLVED session id and model, not the conversation id) and the relay's own
// permission modes and attachment types. An old client's reply has no such key,
// is byte-identical to the reply with the seam unwired, and never consults it.
func TestV2Session_RequestSessionSettings_CapabilitiesForMultiAgentOnly(t *testing.T) {
	t.Parallel()

	t.Run("multi_agent", func(t *testing.T) {
		t.Parallel()
		seams, calls := capsSeams(fixtureAgentCaps, true)
		raw := requestSettingsAs(t, seams, multiAgentCaps, readKnownConvID)

		var got protocol.SessionSettingsPayload
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if got.Capabilities == nil {
			t.Fatalf("capabilities absent from a multi_agent reply: %s", raw)
		}
		if !reflect.DeepEqual(*got.Capabilities, wantFixtureCapObj) {
			t.Errorf("capabilities = %+v, want %+v", *got.Capabilities, wantFixtureCapObj)
		}
		caps := got.Capabilities
		got.Capabilities = nil
		assertSessionSettingsPayload(t, got, fixtureReport)
		got.Capabilities = caps

		want := []capsCall{{readSessionID, readModel}}
		if c := calls(); !reflect.DeepEqual(c, want) {
			t.Errorf("CapabilitiesFor calls = %+v, want %+v", c, want)
		}
	})

	t.Run("old client", func(t *testing.T) {
		t.Parallel()
		seams, calls := capsSeams(fixtureAgentCaps, true)
		raw := requestSettingsAs(t, seams, oldClientCaps, readKnownConvID)
		unwired := requestSettingsAs(t, allReadSeams(), oldClientCaps, readKnownConvID)

		if bytes.Contains(raw, []byte(`"capabilities"`)) {
			t.Errorf("old client's reply carries capabilities: %s", raw)
		}
		if !bytes.Equal(raw, unwired) {
			t.Errorf("old client's reply = %s, want byte-identical to the unwired reply %s", raw, unwired)
		}
		if c := calls(); len(c) != 0 {
			t.Errorf("CapabilitiesFor consulted %d time(s) for an old client, want 0", len(c))
		}
	})
}

// #2646: no capability object when there is no session to describe — an
// unresolved conversation, a seam that refuses, or no seam — even for a
// multi_agent conn; the rest of the reply is unchanged.
func TestV2Session_RequestSessionSettings_CapabilitiesAbsentWithoutSession(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name      string
		convID    string
		seamOK    bool
		unwired   bool
		wantCalls int
		want      protocol.SessionSettingsPayload
	}{
		{name: "unresolved conversation", convID: "conv-unknown", seamOK: true, wantCalls: 0},
		{name: "seam refuses", convID: readKnownConvID, seamOK: false, wantCalls: 1, want: fixtureReport},
		{name: "seam unwired", convID: readKnownConvID, unwired: true, want: fixtureReport},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			seams, calls := capsSeams(fixtureAgentCaps, tc.seamOK)
			if tc.unwired {
				seams.capabilities = nil
			}
			raw := requestSettingsAs(t, seams, multiAgentCaps, tc.convID)
			if bytes.Contains(raw, []byte(`"capabilities"`)) {
				t.Errorf("reply carries capabilities: %s", raw)
			}
			var got protocol.SessionSettingsPayload
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatalf("decode: %v", err)
			}
			assertSessionSettingsPayload(t, got, tc.want)
			if c := calls(); len(c) != tc.wantCalls {
				t.Errorf("CapabilitiesFor consulted %d time(s), want %d", len(c), tc.wantCalls)
			}
		})
	}
}

// #2646: every listed option passes the wire's own shape checks, so a value the
// producer offers that set_session_settings would refuse at the boundary is
// dropped rather than advertised. Lists stay [] rather than null.
func TestSessionCapabilities_DropsValuesTheShapeChecksRefuse(t *testing.T) {
	t.Parallel()

	got := sessionCapabilities(AgentCapabilities{
		Interrupt:    true,
		EffortLevels: []string{"high", "", "High", "-x", "a b"},
		Models:       []string{"opus", "", "--flag", "opus[1m]", "x y"},
	})
	if want := []string{"high"}; !reflect.DeepEqual(got.EffortLevels, want) {
		t.Errorf("EffortLevels = %q, want %q", got.EffortLevels, want)
	}
	if want := []string{"opus", "opus[1m]"}; !reflect.DeepEqual(got.Models, want) {
		t.Errorf("Models = %q, want %q", got.Models, want)
	}

	empty, err := json.Marshal(sessionCapabilities(AgentCapabilities{}))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, key := range []string{`"effort_levels":[]`, `"models":[]`, `"interrupt":false`, `"mid_turn_input":false`, `"slash_commands":false`, `"mcp_servers":false`, `"context_usage_detail":false`} {
		if !bytes.Contains(empty, []byte(key)) {
			t.Errorf("empty capabilities %s lacks %s", empty, key)
		}
	}
}

// #2646: the listed permission modes and validPermissionMode agree, with the
// switch as the authority: every listed mode passes, and over claude's six modes
// plus near misses a value is accepted exactly when it is listed. Bypass is not
// listed. The list is a fresh slice per call, so no caller can widen the next.
func TestPermissionModeOptions_AgreeWithValidPermissionMode(t *testing.T) {
	t.Parallel()

	listed := permissionModeOptions()
	for _, m := range listed {
		if !validPermissionMode(m) {
			t.Errorf("listed mode %q refused by validPermissionMode", m)
		}
	}
	candidates := []string{"default", "acceptEdits", "plan", "auto", "dontAsk", "bypassPermissions",
		"", "Default", "acceptedits", "PLAN", "yolo", "bypass", "default ", "dontask"}
	for _, c := range candidates {
		if validPermissionMode(c) != slices.Contains(listed, c) {
			t.Errorf("mode %q: validPermissionMode = %v, listed = %v", c, validPermissionMode(c), slices.Contains(listed, c))
		}
	}
	if slices.Contains(listed, "bypassPermissions") {
		t.Error("bypassPermissions listed; bypass stays on yolo")
	}

	listed[0] = "bypassPermissions"
	if slices.Contains(permissionModeOptions(), "bypassPermissions") {
		t.Error("mutating a returned list changed the next one")
	}
}
