package relay

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/eventring"
	"github.com/pyrycode/pyrycode/internal/noise"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

const (
	gateCodexConv  = "conv-codex"
	gateClaudeConv = "conv-claude"
)

var (
	gateCapableCaps = []string{protocol.CapabilityInteractive, protocol.CapabilityMultiAgent}
	gateOldCaps     = []string{protocol.CapabilityInteractive}
)

// gateCodexSeam is the CodexConversation seam the gate tests wire: exactly
// gateCodexConv runs Codex.
func gateCodexSeam(id string) bool { return id == gateCodexConv }

func TestPushedConversationID(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		typ     string
		payload string
		want    string
	}{
		{"conversation_updated reads id", protocol.TypeConversationUpdated, `{"id":"c1","name":"n"}`, "c1"},
		{"turn event reads conversation_id", protocol.TypeAssistantDelta, `{"conversation_id":"c1","text":"hi"}`, "c1"},
		{"conversation_updated ignores conversation_id", protocol.TypeConversationUpdated, `{"conversation_id":"c1"}`, ""},
		{"other type ignores id", protocol.TypeModalDismissed, `{"id":"c1","modal_id":"m"}`, ""},
		{"missing key", protocol.TypeWorkspaceUpdated, `{"path":"/w"}`, ""},
		{"null payload", protocol.TypeAssistantDelta, `null`, ""},
		{"non-object payload", protocol.TypeAssistantDelta, `["c1"]`, ""},
		{"malformed payload", protocol.TypeAssistantDelta, `{"conversation_id":`, ""},
		{"empty payload", protocol.TypeAssistantDelta, ``, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			env := protocol.Envelope{Type: tc.typ, Payload: json.RawMessage(tc.payload)}
			if got := pushedConversationID(env); got != tc.want {
				t.Errorf("pushedConversationID = %q, want %q", got, tc.want)
			}
		})
	}
}

// openGateConn drives a handshake for connID on a running manager, advertising
// caps and (when non-nil) a last_event_id, and returns the initiator's recv
// CipherState once the conn's noise_resp is recorded.
func openGateConn(t *testing.T, frames chan protocol.RoutingEnvelope, rec *v2Recorder, connID string, respPub, initPriv []byte, caps []string, lastEventID *uint64) *noise.CipherState {
	t.Helper()
	payload, err := json.Marshal(protocol.HelloClientPayload{
		Role:             "client",
		DeviceName:       v2TestDevName,
		ClientVersion:    "v2-test",
		ProtocolVersions: []string{"v2"},
		Token:            v2TestToken,
		Capabilities:     caps,
		LastEventID:      lastEventID,
	})
	if err != nil {
		t.Fatalf("marshal hello payload: %v", err)
	}
	hello, err := json.Marshal(protocol.Envelope{ID: 1, Type: protocol.TypeHello, TS: time.Now().UTC(), Payload: payload})
	if err != nil {
		t.Fatalf("marshal hello envelope: %v", err)
	}
	initiator, err := noise.NewInitiator(initPriv, respPub)
	if err != nil {
		t.Fatalf("NewInitiator(%s): %v", connID, err)
	}
	initMsg, err := initiator.WriteInit(hello)
	if err != nil {
		t.Fatalf("WriteInit(%s): %v", connID, err)
	}
	frames <- wrapInnerFrame(t, connID, protocol.TypeNoiseInit, initMsg)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		for _, e := range rec.snapshot() {
			if e.ConnID == connID && e.Frame != nil {
				_, _, recv, err := initiator.ReadResp(decodeRespFrame(t, e))
				if err != nil {
					t.Fatalf("ReadResp(%s): %v", connID, err)
				}
				return recv
			}
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("openGateConn(%s): no noise_resp recorded within deadline", connID)
	return nil
}

// gateFramesUntil waits until connID's decrypted frames end in a frame of type
// last, then returns every frame after its noise_resp. Per-conn delivery is FIFO,
// so a sentinel pushed last marks the end of everything pushed before it.
func gateFramesUntil(t *testing.T, rec *v2Recorder, connID string, recv *noise.CipherState, last string) []protocol.Envelope {
	t.Helper()
	var out []protocol.Envelope
	seen := 1 // skip the noise_resp
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		var mine []protocol.RoutingEnvelope
		for _, e := range rec.snapshot() {
			if e.ConnID == connID {
				mine = append(mine, e)
			}
		}
		for ; seen < len(mine); seen++ {
			out = append(out, decryptAppFrame(t, mine[seen], recv))
		}
		if len(out) > 0 && out[len(out)-1].Type == last {
			return out
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("%s: no %s frame within deadline; got %d frames", connID, last, len(out))
	return nil
}

// gateDescribe renders a frame as "type/conversation[/reply]" for comparison.
func gateDescribe(env protocol.Envelope) string {
	d := env.Type + "/" + pushedConversationID(env)
	if env.InReplyTo != nil {
		d += "/reply"
	}
	return d
}

func gateEqual(t *testing.T, connID string, got []protocol.Envelope, want []string) {
	t.Helper()
	gotD := make([]string, len(got))
	for i, e := range got {
		gotD[i] = gateDescribe(e)
	}
	if len(gotD) != len(want) {
		t.Fatalf("%s frames = %v, want %v", connID, gotD, want)
	}
	for i := range want {
		if gotD[i] != want[i] {
			t.Fatalf("%s frames = %v, want %v", connID, gotD, want)
		}
	}
}

// TestV2Session_CodexFrames_ReachOnlyMultiAgentConns pins #2644 on one daemon
// with a capable and an old conn open: a Codex conversation's turn event and
// conversation_updated reach only the conn that negotiated multi_agent; a Claude
// conversation's, a reply, and a frame about no conversation reach both.
func TestV2Session_CodexFrames_ReachOnlyMultiAgentConns(t *testing.T) {
	t.Parallel()
	respPriv, respPub := genV2Keypair(t)
	initPrivA, _ := genV2Keypair(t)
	initPrivB, _ := genV2Keypair(t)
	const capable, old = "c-gate-capable", "c-gate-old"

	frames := make(chan protocol.RoutingEnvelope, 2)
	rec := &v2Recorder{}
	mgr, stop := startManager(t, V2SessionConfig{
		Frames:            frames,
		Outbound:          rec.outbound,
		StaticPriv:        respPriv,
		Devices:           v2PairedRegistry(t, v2TestToken),
		ServerID:          v2TestServerID,
		Logger:            silentLogger(),
		CodexConversation: gateCodexSeam,
	})
	t.Cleanup(stop)

	recvA := openGateConn(t, frames, rec, capable, respPub, initPrivA, gateCapableCaps, nil)
	recvB := openGateConn(t, frames, rec, old, respPub, initPrivB, gateOldCaps, nil)

	ts := time.Now().UTC()
	reply := uint64(7)
	envs := []protocol.Envelope{
		{ID: 1, Type: protocol.TypeAssistantDelta, TS: ts, Payload: json.RawMessage(`{"conversation_id":"conv-codex","text":"x"}`)},
		{ID: 2, Type: protocol.TypeConversationUpdated, TS: ts, Payload: json.RawMessage(`{"id":"conv-codex","name":"n"}`)},
		{ID: 3, Type: protocol.TypeAssistantDelta, TS: ts, Payload: json.RawMessage(`{"conversation_id":"conv-claude","text":"x"}`)},
		{ID: 4, Type: protocol.TypeConversationUpdated, TS: ts, Payload: json.RawMessage(`{"id":"conv-claude","name":"n"}`)},
		{ID: 5, Type: protocol.TypeAssistantDelta, TS: ts, Payload: json.RawMessage(`{"conversation_id":"conv-codex","text":"x"}`), InReplyTo: &reply},
		{ID: 6, Type: protocol.TypeWorkspaceUpdated, TS: ts, Payload: json.RawMessage(`{"path":"/w","label":"w"}`)},
	}
	for _, conn := range []string{capable, old} {
		for _, env := range envs {
			if err := mgr.Push(t.Context(), conn, env); err != nil {
				t.Fatalf("Push(%s, %s): %v", conn, env.Type, err)
			}
		}
	}

	gateEqual(t, capable, gateFramesUntil(t, rec, capable, recvA, protocol.TypeWorkspaceUpdated), []string{
		"assistant_delta/conv-codex",
		"conversation_updated/conv-codex",
		"assistant_delta/conv-claude",
		"conversation_updated/conv-claude",
		"assistant_delta/conv-codex/reply",
		"workspace_updated/",
	})
	gateEqual(t, old, gateFramesUntil(t, rec, old, recvB, protocol.TypeWorkspaceUpdated), []string{
		"assistant_delta/conv-claude",
		"conversation_updated/conv-claude",
		"assistant_delta/conv-codex/reply",
		"workspace_updated/",
	})
}

// TestV2Session_CodexReplay_WithheldFromOldConn pins the replay path: a
// reconnecting old conn is replayed none of a Codex conversation's ring events,
// and its replay still completes (the live sentinel, held until the replay
// drains, arrives), while a capable conn is replayed them.
func TestV2Session_CodexReplay_WithheldFromOldConn(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		caps []string
		want []string
	}{
		{"capable", gateCapableCaps, []string{"turn_state/conv-codex", "turn_state/conv-codex", "workspace_updated/"}},
		{"old", gateOldCaps, []string{"workspace_updated/"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			respPriv, respPub := genV2Keypair(t)
			initPriv, _ := genV2Keypair(t)
			const connID = "c-gate-replay"

			ring := eventring.New(eventring.MaxEventsPerConversation)
			for range 3 {
				ring.Append(gateCodexConv, protocol.TypeTurnState, json.RawMessage(`{"conversation_id":"conv-codex"}`), time.Now().UTC())
			}

			frames := make(chan protocol.RoutingEnvelope, 1)
			rec := &v2Recorder{}
			mgr, stop := startManager(t, V2SessionConfig{
				Frames:            frames,
				Outbound:          rec.outbound,
				StaticPriv:        respPriv,
				Devices:           v2PairedRegistry(t, v2TestToken),
				ServerID:          v2TestServerID,
				Logger:            silentLogger(),
				CodexConversation: gateCodexSeam,
			})
			t.Cleanup(stop)
			mgr.SetReplaySource(ring, func() string { return gateCodexConv })

			last := uint64(1)
			recv := openGateConn(t, frames, rec, connID, respPub, initPriv, tc.caps, &last)
			sentinel := protocol.Envelope{ID: 9, Type: protocol.TypeWorkspaceUpdated, TS: time.Now().UTC(), Payload: json.RawMessage(`{"path":"/w","label":"w"}`)}
			if err := mgr.Push(t.Context(), connID, sentinel); err != nil {
				t.Fatalf("Push sentinel: %v", err)
			}
			gateEqual(t, connID, gateFramesUntil(t, rec, connID, recv, protocol.TypeWorkspaceUpdated), tc.want)
		})
	}
}

// gatePushedModelList is a pushed model_list as turnbridge encodes a Claude
// child's initialize reply: untagged Claude entries, a conversation, a cut count.
func gatePushedModelList(t *testing.T) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(protocol.ModelListPayload{
		ConversationID: gateClaudeConv,
		Models: []protocol.ModelOption{
			{Value: "default", ResolvedModel: "claude-sonnet-5", DisplayName: "Default", EffortLevels: []string{"low"}},
			{Value: "haiku", ResolvedModel: "claude-haiku-4-5", DisplayName: "Haiku"},
		},
		DroppedModels: 2,
	})
	if err != nil {
		t.Fatalf("marshal model_list: %v", err)
	}
	return raw
}

// gateMergeSeam stands in for the daemon's MergedModelOptions: the Claude entries
// tagged into a fresh slice, then one Codex entry.
func gateMergeSeam(claude []protocol.ModelOption) []protocol.ModelOption {
	out := make([]protocol.ModelOption, 0, len(claude)+1)
	for _, m := range claude {
		m.Agent, m.Family = protocol.AgentClaude, m.Value
		out = append(out, m)
	}
	return append(out, protocol.ModelOption{Value: "gpt-5-codex", ResolvedModel: "gpt-5-codex", DisplayName: "gpt-5-codex", Agent: protocol.AgentCodex, Family: "gpt-5-codex"})
}

// assertMergedModelList checks that got is pushed's model_list with its Claude
// entries replaced by gateMergeSeam's merge, conversation_id and dropped_models
// kept.
func assertMergedModelList(t *testing.T, got protocol.Envelope, pushed json.RawMessage) {
	t.Helper()
	var in, out protocol.ModelListPayload
	if err := json.Unmarshal(pushed, &in); err != nil {
		t.Fatalf("decode pushed: %v", err)
	}
	if err := json.Unmarshal(got.Payload, &out); err != nil {
		t.Fatalf("decode delivered: %v", err)
	}
	want := in
	want.Models = gateMergeSeam(in.Models)
	wantJSON, _ := json.Marshal(want)
	gotJSON, _ := json.Marshal(out)
	if string(gotJSON) != string(wantJSON) {
		t.Errorf("capable conn's model_list = %s, want %s", gotJSON, wantJSON)
	}
}

// TestV2Session_PushedModelList_MergedForMultiAgentConn pins #2652 on one daemon
// with a capable and an old conn open: one pushed model_list reaches the capable
// conn with Codex's entries merged in and the old conn byte-identical; a
// model_list reply reaches both unchanged; with the seam unwired the capable conn
// gets the pushed bytes too.
func TestV2Session_PushedModelList_MergedForMultiAgentConn(t *testing.T) {
	t.Parallel()
	for _, wired := range []bool{true, false} {
		name := "wired"
		if !wired {
			name = "unwired"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			respPriv, respPub := genV2Keypair(t)
			initPrivA, _ := genV2Keypair(t)
			initPrivB, _ := genV2Keypair(t)
			const capable, old = "c-merge-capable", "c-merge-old"

			frames := make(chan protocol.RoutingEnvelope, 2)
			rec := &v2Recorder{}
			cfg := V2SessionConfig{
				Frames:     frames,
				Outbound:   rec.outbound,
				StaticPriv: respPriv,
				Devices:    v2PairedRegistry(t, v2TestToken),
				ServerID:   v2TestServerID,
				Logger:     silentLogger(),
			}
			if wired {
				cfg.MergedModelOptions = gateMergeSeam
			}
			mgr, stop := startManager(t, cfg)
			t.Cleanup(stop)

			recvA := openGateConn(t, frames, rec, capable, respPub, initPrivA, gateCapableCaps, nil)
			recvB := openGateConn(t, frames, rec, old, respPub, initPrivB, gateOldCaps, nil)

			pushed := gatePushedModelList(t)
			ts := time.Now().UTC()
			reply := uint64(3)
			envs := []protocol.Envelope{
				{ID: 1, Type: protocol.TypeModelList, TS: ts, Payload: pushed},
				{ID: 2, Type: protocol.TypeModelList, TS: ts, Payload: pushed, InReplyTo: &reply},
				{ID: 3, Type: protocol.TypeWorkspaceUpdated, TS: ts, Payload: json.RawMessage(`{"path":"/w","label":"w"}`)},
			}
			for _, conn := range []string{capable, old} {
				for _, env := range envs {
					if err := mgr.Push(t.Context(), conn, env); err != nil {
						t.Fatalf("Push(%s, %s): %v", conn, env.Type, err)
					}
				}
			}

			gotA := gateFramesUntil(t, rec, capable, recvA, protocol.TypeWorkspaceUpdated)
			gotB := gateFramesUntil(t, rec, old, recvB, protocol.TypeWorkspaceUpdated)
			want := []string{"model_list/" + gateClaudeConv, "model_list/" + gateClaudeConv + "/reply", "workspace_updated/"}
			gateEqual(t, capable, gotA, want)
			gateEqual(t, old, gotB, want)

			if wired {
				assertMergedModelList(t, gotA[0], pushed)
			} else if string(gotA[0].Payload) != string(pushed) {
				t.Errorf("unwired capable conn's model_list = %s, want the pushed %s", gotA[0].Payload, pushed)
			}
			for _, got := range []protocol.Envelope{gotA[1], gotB[0], gotB[1]} {
				if string(got.Payload) != string(pushed) {
					t.Errorf("model_list = %s, want the pushed bytes %s", got.Payload, pushed)
				}
			}
		})
	}
}

// TestV2Session_ReplayedModelList_MergedForMultiAgentConn pins the replay path: a
// ring model_list replayed to a reconnecting capable conn arrives merged, and the
// same ring entry replayed to an old conn afterwards arrives byte-identical, so
// the capable conn's copy never wrote through the stored one. The replay dedup
// still applies: the event at the advertised last_event_id is not replayed.
func TestV2Session_ReplayedModelList_MergedForMultiAgentConn(t *testing.T) {
	t.Parallel()
	respPriv, respPub := genV2Keypair(t)
	initPrivA, _ := genV2Keypair(t)
	initPrivB, _ := genV2Keypair(t)
	const capable, old = "c-merge-replay-capable", "c-merge-replay-old"

	pushed := gatePushedModelList(t)
	ring := eventring.New(eventring.MaxEventsPerConversation)
	for range 2 {
		ring.Append(gateClaudeConv, protocol.TypeModelList, pushed, time.Now().UTC())
	}

	frames := make(chan protocol.RoutingEnvelope, 1)
	rec := &v2Recorder{}
	mgr, stop := startManager(t, V2SessionConfig{
		Frames:             frames,
		Outbound:           rec.outbound,
		StaticPriv:         respPriv,
		Devices:            v2PairedRegistry(t, v2TestToken),
		ServerID:           v2TestServerID,
		Logger:             silentLogger(),
		MergedModelOptions: gateMergeSeam,
	})
	t.Cleanup(stop)
	mgr.SetReplaySource(ring, func() string { return gateClaudeConv })

	sentinel := protocol.Envelope{ID: 9, Type: protocol.TypeWorkspaceUpdated, TS: time.Now().UTC(), Payload: json.RawMessage(`{"path":"/w","label":"w"}`)}
	last := uint64(1)
	want := []string{"model_list/" + gateClaudeConv, "workspace_updated/"}

	recvA := openGateConn(t, frames, rec, capable, respPub, initPrivA, gateCapableCaps, &last)
	if err := mgr.Push(t.Context(), capable, sentinel); err != nil {
		t.Fatalf("Push sentinel: %v", err)
	}
	gotA := gateFramesUntil(t, rec, capable, recvA, protocol.TypeWorkspaceUpdated)
	gateEqual(t, capable, gotA, want)
	assertMergedModelList(t, gotA[0], pushed)

	recvB := openGateConn(t, frames, rec, old, respPub, initPrivB, gateOldCaps, &last)
	if err := mgr.Push(t.Context(), old, sentinel); err != nil {
		t.Fatalf("Push sentinel: %v", err)
	}
	gotB := gateFramesUntil(t, rec, old, recvB, protocol.TypeWorkspaceUpdated)
	gateEqual(t, old, gotB, want)
	if string(gotB[0].Payload) != string(pushed) {
		t.Errorf("old conn's replayed model_list = %s, want the stored bytes %s", gotB[0].Payload, pushed)
	}
}
