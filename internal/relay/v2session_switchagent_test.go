package relay

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/protocol"
)

type testAgentSwitchCall struct {
	ctx     context.Context
	payload protocol.SwitchAgentPayload
	release chan AgentSwitchOutcome
}
type testAgentSwitcher struct{ calls chan testAgentSwitchCall }

func (f *testAgentSwitcher) SwitchAgent(ctx context.Context, p protocol.SwitchAgentPayload) AgentSwitchOutcome {
	call := testAgentSwitchCall{ctx, p, make(chan AgentSwitchOutcome, 1)}
	select {
	case f.calls <- call:
	case <-ctx.Done():
		return AgentSwitchOutcome{}
	}
	select {
	case result := <-call.release:
		return result
	case <-ctx.Done():
		return AgentSwitchOutcome{}
	}
}
func testAwaitSwitch(t *testing.T, f *testAgentSwitcher) testAgentSwitchCall {
	t.Helper()
	select {
	case c := <-f.calls:
		return c
	case <-time.After(3 * time.Second):
		t.Fatal("switch seam not called")
		return testAgentSwitchCall{}
	}
}
func testSwitchManager(t *testing.T, f AgentSwitcher, connected ...func() bool) (*V2SessionManager, chan protocol.RoutingEnvelope, *v2Recorder, []byte, *syncLogBuffer, func()) {
	t.Helper()
	priv, pub := genV2Keypair(t)
	frames := make(chan protocol.RoutingEnvelope, 16)
	rec := &v2Recorder{}
	logger, logs := bufferLogger()
	var probe func() bool
	if len(connected) > 0 {
		probe = connected[0]
	}
	mgr, stop := startManager(t, V2SessionConfig{Connected: probe, Frames: frames, Outbound: rec.outbound, StaticPriv: priv, Devices: v2PairedRegistry(t, v2TestToken), ServerID: v2TestServerID, Logger: logger, AgentSwitcher: f})
	t.Cleanup(stop)
	return mgr, frames, rec, pub, logs, stop
}
func testSwitchError(t *testing.T, env protocol.Envelope, code, message string, retry bool) {
	t.Helper()
	var p protocol.ErrorPayload
	if err := json.Unmarshal(env.Payload, &p); err != nil {
		t.Fatal(err)
	}
	if env.Type != protocol.TypeError || env.InReplyTo == nil || *env.InReplyTo != 2870 || !reflect.DeepEqual(p, protocol.ErrorPayload{Code: code, Message: message, Retryable: retry}) {
		t.Fatalf("unexpected correlated error: %#v / %#v", env, p)
	}
}
func testSwitchPrivacy(t *testing.T, logs *syncLogBuffer) {
	t.Helper()
	for _, secret := range []string{"private-conversation-marker", "private-model-marker", "private-effort-marker", "private-decode-marker"} {
		if strings.Contains(logs.String(), secret) {
			t.Fatalf("diagnostic leaked %s", secret)
		}
	}
	for _, line := range strings.Split(logs.String(), "\n") {
		if strings.Contains(line, "event=v2.switch_agent.") {
			for _, forbidden := range []string{" model=", " effort=", " err=", " payload=", " conversation_id=", " in_reply_to=", " request_id="} {
				if strings.Contains(line, forbidden) {
					t.Fatalf("unexpected diagnostic field: %s", line)
				}
			}
		}
	}
}

func TestV2Session_SwitchAgentValidation(t *testing.T) {
	t.Parallel()
	const valid = `{"conversation_id":"private-conversation-marker","agent":"codex","model":"private-model-marker","effort":"private-effort-marker"}`
	cases := []struct {
		name, payload, code   string
		caps                  []string
		inert, valid, unwired bool
	}{
		{name: "capability precedes decode", payload: `"private-decode-marker"`, code: protocol.CodeProtocolUnsupported, caps: []string{protocol.CapabilityInteractive}},
		{name: "capability precedes interactive", payload: `[]`, code: protocol.CodeProtocolUnsupported},
		{name: "capable noninteractive inert", payload: `"private-decode-marker"`, caps: []string{protocol.CapabilityMultiAgent}, inert: true},
		{name: "absent payload", code: protocol.CodeProtocolMalformed},
		{name: "null payload", payload: `null`, code: protocol.CodeProtocolMalformed},
		{name: "array payload", payload: `[]`, code: protocol.CodeProtocolMalformed},
		{name: "scalar payload", payload: `"private-decode-marker"`, code: protocol.CodeProtocolMalformed},
		{name: "agent missing", payload: `{"conversation_id":"c","model":""}`, code: protocol.CodeProtocolUnsupported},
		{name: "agent null", payload: `{"conversation_id":"c","agent":null,"model":""}`, code: protocol.CodeProtocolUnsupported},
		{name: "agent empty", payload: `{"conversation_id":"c","agent":"","model":""}`, code: protocol.CodeProtocolUnsupported},
		{name: "agent unsupported", payload: `{"conversation_id":"c","agent":"other","model":""}`, code: protocol.CodeProtocolUnsupported},
		{name: "agent wrong type", payload: `{"conversation_id":"c","agent":7,"model":""}`, code: protocol.CodeProtocolMalformed},
		{name: "conversation missing", payload: `{"agent":"claude","model":""}`, code: protocol.CodeProtocolMalformed},
		{name: "conversation null", payload: `{"conversation_id":null,"agent":"claude","model":""}`, code: protocol.CodeProtocolMalformed},
		{name: "conversation empty", payload: `{"conversation_id":"","agent":"claude","model":""}`, code: protocol.CodeProtocolMalformed},
		{name: "conversation wrong type", payload: `{"conversation_id":[],"agent":"claude","model":""}`, code: protocol.CodeProtocolMalformed},
		{name: "model missing", payload: `{"conversation_id":"c","agent":"claude"}`, code: protocol.CodeProtocolMalformed},
		{name: "model null", payload: `{"conversation_id":"c","agent":"claude","model":null}`, code: protocol.CodeProtocolMalformed},
		{name: "model wrong type", payload: `{"conversation_id":"c","agent":"claude","model":false}`, code: protocol.CodeProtocolMalformed},
		{name: "model grammar", payload: `{"conversation_id":"c","agent":"claude","model":"private-model-marker;bad"}`, code: protocol.CodeProtocolMalformed},
		{name: "effort wrong type", payload: `{"conversation_id":"c","agent":"claude","model":"","effort":{}}`, code: protocol.CodeProtocolMalformed},
		{name: "effort grammar", payload: `{"conversation_id":"c","agent":"claude","model":"","effort":"private-effort-marker;bad"}`, code: protocol.CodeProtocolMalformed},
		{name: "claude template unspecified", payload: `{"conversation_id":"c","agent":"claude","model":""}`, valid: true},
		{name: "codex populated", payload: valid, valid: true},
		{name: "null effort unspecified", payload: `{"conversation_id":"c","agent":"codex","model":"","effort":null}`, valid: true},
		{name: "empty effort clears", payload: `{"conversation_id":"c","agent":"claude","model":"","effort":""}`, valid: true},
		{name: "unwired", payload: valid, unwired: true, code: protocol.CodeServerBinaryOffline},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &testAgentSwitcher{calls: make(chan testAgentSwitchCall, 1)}
			var seam AgentSwitcher = f
			if tc.unwired {
				seam = nil
			}
			mgr, frames, rec, pub, logs, _ := testSwitchManager(t, seam)
			caps := tc.caps
			if caps == nil && tc.name != "capability precedes interactive" {
				caps = []string{protocol.CapabilityInteractive, protocol.CapabilityMultiAgent}
			}
			send, recv := openModalConn(t, mgr, frames, rec, pub, "requester", caps)
			var payload json.RawMessage
			if tc.payload != "" {
				payload = json.RawMessage(tc.payload)
			}
			frames <- sealAppFrameConn(t, send, "requester", protocol.Envelope{ID: 2870, Type: protocol.TypeSwitchAgent, TS: time.Now().UTC(), Payload: payload})
			if tc.valid {
				call := testAwaitSwitch(t, f)
				var want protocol.SwitchAgentPayload
				if err := json.Unmarshal([]byte(tc.payload), &want); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(call.payload, want) {
					t.Fatalf("seam payload %#v, want %#v", call.payload, want)
				}
				call.release <- AgentSwitchOutcome{State: AgentSwitchCommitted}
				waitForLogContains(t, logs, "v2.switch_agent.completed")
			} else if !tc.inert {
				msgs := awaitReplyForConn(t, rec, "requester")
				message := msgSwitchMalformed
				if tc.code == protocol.CodeProtocolUnsupported {
					message = msgSwitchUnsupported
				}
				if tc.unwired {
					message = msgSwitchUnavailable
				}
				testSwitchError(t, decryptAppFrame(t, msgs[0], recv), tc.code, message, tc.unwired)
			}
			openModalConn(t, mgr, frames, rec, pub, "barrier", nil)
			select {
			case <-f.calls:
				t.Fatal("unexpected seam call")
			default:
			}
			wantCount := 0
			if !tc.valid && !tc.inert {
				wantCount = 1
			}
			if n := len(noiseMsgsForConn(t, rec, "requester")); n != wantCount {
				t.Fatalf("replies=%d, want %d", n, wantCount)
			}
			testSwitchPrivacy(t, logs)
		})
	}
}

func TestV2Session_SwitchAgentLateOutcomes(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name          string
		state         AgentSwitchState
		failure       AgentSwitchFailure
		code, message string
		retry         bool
	}{
		{"unknown", AgentSwitchRefused, AgentSwitchConversationNotFound, protocol.CodeConversationNotFound, msgSwitchNotFound, false},
		{"invalid or same agent", AgentSwitchRefused, AgentSwitchInvalidRequest, protocol.CodeProtocolMalformed, msgSwitchMalformed, false},
		{"model not offered", AgentSwitchRefused, AgentSwitchModelNotOffered, protocol.CodeProtocolMalformed, MsgSettingsModelNotOffered, false},
		{"effort not offered", AgentSwitchRefused, AgentSwitchEffortNotOffered, protocol.CodeProtocolMalformed, msgSettingsMalformed, false},
		{"vocabulary unavailable", AgentSwitchRefused, AgentSwitchVocabularyUnavailable, protocol.CodeModelListUnavailable, MsgModelListUnavailable, true},
		{"busy", AgentSwitchRefused, AgentSwitchBusy, protocol.CodeServerBinaryBusy, msgSwitchBusy, true},
		{"workspace confinement", AgentSwitchRefused, AgentSwitchWorkspaceRejected, protocol.CodeProtocolUnsupported, msgSwitchUnsupported, false},
		{"uncommitted after wrapup", AgentSwitchFailed, AgentSwitchOtherFailure, protocol.CodeServerBinaryOffline, msgSwitchUnavailable, true},
		{"zero outcome", 0, 0, protocol.CodeServerBinaryOffline, msgSwitchUnavailable, true},
		{"clean committed", AgentSwitchCommitted, 0, "", "", false},
		{"committed cleanup failure", AgentSwitchCommitted, AgentSwitchOtherFailure, "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &testAgentSwitcher{calls: make(chan testAgentSwitchCall, 1)}
			mgr, frames, rec, pub, logs, _ := testSwitchManager(t, f)
			send, recv := openModalConn(t, mgr, frames, rec, pub, "requester", []string{protocol.CapabilityInteractive, protocol.CapabilityMultiAgent})
			frames <- sealAppFrameConn(t, send, "requester", protocol.Envelope{ID: 2870, Type: protocol.TypeSwitchAgent, TS: time.Now().UTC(), Payload: json.RawMessage(`{"conversation_id":"private-conversation-marker","agent":"codex","model":"private-model-marker","effort":"private-effort-marker"}`)})
			call := testAwaitSwitch(t, f)
			// A second connection receives an actual encrypted reply while switch is held.
			otherSend, otherRecv := openModalConn(t, mgr, frames, rec, pub, "other", []string{protocol.CapabilityInteractive})
			frames <- sealAppFrameConn(t, otherSend, "other", protocol.Envelope{ID: 2870, Type: protocol.TypeSwitchAgent, TS: time.Now().UTC(), Payload: json.RawMessage(`[]`)})
			testSwitchError(t, decryptAppFrame(t, awaitReplyForConn(t, rec, "other")[0], otherRecv), protocol.CodeProtocolUnsupported, msgSwitchUnsupported, false)
			if n := len(noiseMsgsForConn(t, rec, "requester")); n != 0 {
				t.Fatal("switch replied before completion")
			}
			call.release <- AgentSwitchOutcome{State: tc.state, Failure: tc.failure}
			if tc.code != "" {
				testSwitchError(t, decryptAppFrame(t, awaitReplyForConn(t, rec, "requester")[0], recv), tc.code, tc.message, tc.retry)
			}
			waitForLogContains(t, logs, "v2.switch_agent.completed")
			openModalConn(t, mgr, frames, rec, pub, "barrier", nil)
			want := 0
			if tc.code != "" {
				want = 1
			}
			if n := len(noiseMsgsForConn(t, rec, "requester")); n != want {
				t.Fatalf("replies=%d want=%d", n, want)
			}
			testSwitchPrivacy(t, logs)
		})
	}
}

func TestV2Session_SwitchAgentTeardown(t *testing.T) {
	t.Parallel()
	for _, shutdown := range []bool{false, true} {
		t.Run(map[bool]string{false: "requester replaced", true: "manager shutdown"}[shutdown], func(t *testing.T) {
			f := &testAgentSwitcher{calls: make(chan testAgentSwitchCall, 1)}
			mgr, frames, rec, pub, _, stop := testSwitchManager(t, f)
			send, _ := openModalConn(t, mgr, frames, rec, pub, "requester", []string{protocol.CapabilityInteractive, protocol.CapabilityMultiAgent})
			frames <- sealAppFrameConn(t, send, "requester", protocol.Envelope{ID: 2870, Type: protocol.TypeSwitchAgent, TS: time.Now().UTC(), Payload: json.RawMessage(`{"conversation_id":"c","agent":"claude","model":""}`)})
			call := testAwaitSwitch(t, f)
			if shutdown {
				stop()
				select {
				case <-call.ctx.Done():
				case <-time.After(time.Second):
					t.Fatal("seam not cancelled on shutdown")
				}
				return
			}
			frames <- protocol.RoutingEnvelope{ConnID: "requester", CloseCode: 1000}
			openModalConn(t, mgr, frames, rec, pub, "barrier", nil)
			select {
			case <-call.ctx.Done():
				t.Fatal("disconnect cancelled accepted switch")
			default:
			}
			rec.mu.Lock()
			rec.env = nil
			rec.mu.Unlock()
			replacementSend, replacementRecv := openModalConn(t, mgr, frames, rec, pub, "requester", []string{protocol.CapabilityInteractive})
			call.release <- AgentSwitchOutcome{State: AgentSwitchFailed, Failure: AgentSwitchOtherFailure}
			// A correlated round-trip on the replacement must decrypt with its first nonce.
			frames <- sealAppFrameConn(t, replacementSend, "requester", protocol.Envelope{ID: 2870, Type: protocol.TypeSwitchAgent, TS: time.Now().UTC()})
			testSwitchError(t, decryptAppFrame(t, awaitReplyForConn(t, rec, "requester")[0], replacementRecv), protocol.CodeProtocolUnsupported, msgSwitchUnsupported, false)
		})
	}
}

func TestV2Session_SwitchAgentHandoffEscapes(t *testing.T) {
	t.Parallel()
	for _, shutdown := range []bool{false, true} {
		t.Run(map[bool]string{false: "requester done", true: "manager done"}[shutdown], func(t *testing.T) {
			mgr := &V2SessionManager{switchAgentDone: make(chan switchAgentResult, 1)}
			mgr.switchAgentDone <- switchAgentResult{} // force a blocked handoff
			s := &V2Session{done: make(chan struct{})}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if shutdown {
				cancel()
			} else {
				close(s.done)
			}
			returned := make(chan struct{})
			go func() { mgr.deferSwitchAgentOutcome(ctx, switchAgentResult{s: s}); close(returned) }()
			select {
			case <-returned:
			case <-time.After(time.Second):
				t.Fatal("completion handoff blocked after teardown")
			}
		})
	}
}

func TestV2Session_SwitchAgentClosedOutcome(t *testing.T) {
	t.Parallel()
	// Force the Run completion guard even when handoff's teardown escape could win.
	rec := &v2Recorder{}
	mgr := &V2SessionManager{cfg: V2SessionConfig{Outbound: rec.outbound}, sessions: map[string]*V2Session{"same-id": {state: V2StateOpen}}}
	old := &V2Session{connID: "same-id", state: V2StateClosed}
	mgr.handleSwitchAgentDone(context.Background(), switchAgentResult{s: old, outcome: AgentSwitchOutcome{State: AgentSwitchFailed}})
	if len(rec.snapshot()) != 0 {
		t.Fatal("stale outcome sent to replacement")
	}
}

func TestV2Session_SwitchAgentTransportDown(t *testing.T) {
	t.Parallel()
	var down atomic.Bool
	f := &testAgentSwitcher{calls: make(chan testAgentSwitchCall, 1)}
	mgr, frames, rec, pub, logs, _ := testSwitchManager(t, f, func() bool { return !down.Load() })
	send, recv := openModalConn(t, mgr, frames, rec, pub, "requester", []string{protocol.CapabilityInteractive, protocol.CapabilityMultiAgent})
	frames <- sealAppFrameConn(t, send, "requester", protocol.Envelope{ID: 2870, Type: protocol.TypeSwitchAgent, TS: time.Now().UTC(), Payload: json.RawMessage(`{"conversation_id":"c","agent":"codex","model":""}`)})
	call := testAwaitSwitch(t, f)
	down.Store(true)
	call.release <- AgentSwitchOutcome{State: AgentSwitchFailed}
	waitForLogContains(t, logs, "v2.switch_agent.completed")
	waitForLogContains(t, logs, "v2.switch_agent.reply_dropped_transport_down")
	if len(noiseMsgsForConn(t, rec, "requester")) != 0 {
		t.Fatal("sealed while transport down")
	}
	down.Store(false)
	frames <- sealAppFrameConn(t, send, "requester", protocol.Envelope{ID: 2870, Type: protocol.TypeSwitchAgent, TS: time.Now().UTC(), Payload: json.RawMessage(`[]`)})
	testSwitchError(t, decryptAppFrame(t, awaitReplyForConn(t, rec, "requester")[0], recv), protocol.CodeProtocolMalformed, msgSwitchMalformed, false)
	testSwitchPrivacy(t, logs)
}
