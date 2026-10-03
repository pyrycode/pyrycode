package relay

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/dispatch"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// gateAgentSeam is the ConversationAgent seam the tagging tests wire: the Codex
// and Claude test conversations resolve to their agents, every other id misses.
func gateAgentSeam(id string) (string, bool) {
	switch id {
	case gateCodexConv:
		return protocol.AgentCodex, true
	case gateClaudeConv:
		return protocol.AgentClaude, true
	}
	return "", false
}

// gateUpdatedPayload is a conversation_updated record for id as a producer
// marshals it: no agent key.
func gateUpdatedPayload(t *testing.T, id string) json.RawMessage {
	t.Helper()
	name := "renamed"
	raw, err := json.Marshal(protocol.ConversationUpdatedPayload{
		ID:         id,
		IsPromoted: true,
		Name:       &name,
		Cwd:        "/w",
		LastUsedAt: time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("marshal conversation_updated: %v", err)
	}
	return raw
}

// assertAgentTagged checks that got is sent's record with Agent set to agent and
// every other field kept.
func assertAgentTagged(t *testing.T, got json.RawMessage, sent json.RawMessage, agent string) {
	t.Helper()
	var want protocol.ConversationUpdatedPayload
	if err := json.Unmarshal(sent, &want); err != nil {
		t.Fatalf("decode sent: %v", err)
	}
	want.Agent = agent
	wantJSON, _ := json.Marshal(want)
	if string(got) != string(wantJSON) {
		t.Errorf("capable conn's conversation_updated = %s, want %s", got, wantJSON)
	}
}

// TestV2Session_PushedConversationUpdated_AgentForMultiAgentConn pins #2669's
// push side on one daemon with a capable and an old conn open: a pushed
// conversation_updated for a Claude and a Codex conversation reaches the capable
// conn carrying its agent and the old conn byte-identical to the pushed bytes, so
// the capable copy never wrote through the shared payload. A conversation the
// seam does not know, and every frame when the seam is unwired, reach the capable
// conn unchanged.
func TestV2Session_PushedConversationUpdated_AgentForMultiAgentConn(t *testing.T) {
	t.Parallel()
	for _, wired := range []bool{true, false} {
		name := "wired"
		if !wired {
			name = "unwired"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			respPriv, respPub := genV2Keypair(t)
			initPrivA, initPrivB := v2TestInstallPriv, v2TestInstallPriv
			const capable, old = "c-tag-capable", "c-tag-old"

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
				cfg.ConversationAgent = gateAgentSeam
			}
			mgr, stop := startManager(t, cfg)
			t.Cleanup(stop)

			recvA := openGateConn(t, frames, rec, capable, respPub, initPrivA, gateCapableCaps, nil)
			recvB := openGateConn(t, frames, rec, old, respPub, initPrivB, gateOldCaps, nil)

			convs := []string{gateClaudeConv, gateCodexConv, "conv-unknown"}
			agents := []string{protocol.AgentClaude, protocol.AgentCodex, ""}
			ts := time.Now().UTC()
			var envs []protocol.Envelope
			for i, id := range convs {
				envs = append(envs, protocol.Envelope{ID: uint64(i + 1), Type: protocol.TypeConversationUpdated, TS: ts, Payload: gateUpdatedPayload(t, id)})
			}
			envs = append(envs, protocol.Envelope{ID: 9, Type: protocol.TypeWorkspaceUpdated, TS: ts, Payload: json.RawMessage(`{"path":"/w","label":"w"}`)})
			for _, conn := range []string{capable, old} {
				for _, env := range envs {
					if err := mgr.Push(t.Context(), conn, env); err != nil {
						t.Fatalf("Push(%s, %s): %v", conn, env.Type, err)
					}
				}
			}

			want := []string{
				"conversation_updated/" + gateClaudeConv,
				"conversation_updated/" + gateCodexConv,
				"conversation_updated/conv-unknown",
				"workspace_updated/",
			}
			gotA := gateFramesUntil(t, rec, capable, recvA, protocol.TypeWorkspaceUpdated)
			gotB := gateFramesUntil(t, rec, old, recvB, protocol.TypeWorkspaceUpdated)
			gateEqual(t, capable, gotA, want)
			gateEqual(t, old, gotB, want)
			for i := range convs {
				if wired && agents[i] != "" {
					assertAgentTagged(t, gotA[i].Payload, envs[i].Payload, agents[i])
				} else if string(gotA[i].Payload) != string(envs[i].Payload) {
					t.Errorf("capable conn's %s = %s, want the pushed %s", convs[i], gotA[i].Payload, envs[i].Payload)
				}
				if string(gotB[i].Payload) != string(envs[i].Payload) {
					t.Errorf("old conn's %s = %s, want the pushed %s", convs[i], gotB[i].Payload, envs[i].Payload)
				}
			}
		})
	}
}

// TestV2Session_ConversationUpdatedReply_AgentForMultiAgentConn pins #2669's reply
// side: a rename handler's conversation_updated reply, for a Claude and a Codex
// conversation, reaches a capable conn carrying the agent and an old conn as the
// exact bytes the handler sent. A reply of another type to the capable conn is
// untouched.
func TestV2Session_ConversationUpdatedReply_AgentForMultiAgentConn(t *testing.T) {
	t.Parallel()
	respPriv, respPub := genV2Keypair(t)
	const capable, old = "c-tag-reply-capable", "c-tag-reply-old"

	const otherPayload = `{"conversations":[]}`
	// Built here, not in the handler: the handler runs on the conn's worker
	// goroutine, where t.Fatalf must not be called.
	replies := map[string]json.RawMessage{
		gateClaudeConv: gateUpdatedPayload(t, gateClaudeConv),
		gateCodexConv:  gateUpdatedPayload(t, gateCodexConv),
	}
	handlers := map[string]dispatch.Handler{
		protocol.TypeRenameConversation: func(ctx context.Context, c *dispatch.Conn, env protocol.Envelope) error {
			var req struct {
				ID string `json:"id"`
			}
			if err := json.Unmarshal(env.Payload, &req); err != nil {
				return err
			}
			return c.Reply(ctx, env, protocol.TypeConversationUpdated, replies[req.ID])
		},
		protocol.TypeListConversations: func(ctx context.Context, c *dispatch.Conn, env protocol.Envelope) error {
			return c.Reply(ctx, env, protocol.TypeConversations, json.RawMessage(otherPayload))
		},
	}

	frames := make(chan protocol.RoutingEnvelope, 4)
	rec := &v2Recorder{}
	mgr, stop := startManager(t, V2SessionConfig{
		Frames:            frames,
		Outbound:          rec.outbound,
		StaticPriv:        respPriv,
		Devices:           v2PairedRegistry(t, v2TestToken),
		ServerID:          v2TestServerID,
		Logger:            silentLogger(),
		Handlers:          handlers,
		ConversationAgent: gateAgentSeam,
	})
	t.Cleanup(stop)

	sendA, recvA := openModalConn(t, mgr, frames, rec, respPub, capable, gateCapableCaps)
	sendB, recvB := openModalConn(t, mgr, frames, rec, respPub, old, gateOldCaps)

	convs := []string{gateClaudeConv, gateCodexConv}
	agents := []string{protocol.AgentClaude, protocol.AgentCodex}
	for i, id := range convs {
		reqID := uint64(i + 1)
		payload, _ := json.Marshal(map[string]string{"id": id})
		frames <- sealAppFrameConn(t, sendA, capable, protocol.Envelope{ID: reqID, Type: protocol.TypeRenameConversation, TS: time.Now().UTC(), Payload: payload})
		frames <- sealAppFrameConn(t, sendB, old, protocol.Envelope{ID: reqID, Type: protocol.TypeRenameConversation, TS: time.Now().UTC(), Payload: payload})

		gotA := decryptAppFrame(t, waitForConnNoiseMsg(t, rec, capable, i+1)[i], recvA)
		gotB := decryptAppFrame(t, waitForConnNoiseMsg(t, rec, old, i+1)[i], recvB)
		for _, got := range []protocol.Envelope{gotA, gotB} {
			if got.Type != protocol.TypeConversationUpdated || got.InReplyTo == nil || *got.InReplyTo != reqID {
				t.Fatalf("reply = %s in_reply_to %v, want conversation_updated to %d", got.Type, got.InReplyTo, reqID)
			}
		}
		assertAgentTagged(t, gotA.Payload, replies[id], agents[i])
		if want := replies[id]; string(gotB.Payload) != string(want) {
			t.Errorf("old conn's %s reply = %s, want the handler's %s", id, gotB.Payload, want)
		}
	}

	frames <- sealAppFrameConn(t, sendA, capable, protocol.Envelope{ID: 9, Type: protocol.TypeListConversations, TS: time.Now().UTC(), Payload: json.RawMessage(`{}`)})
	got := decryptAppFrame(t, waitForConnNoiseMsg(t, rec, capable, len(convs)+1)[len(convs)], recvA)
	if got.Type != protocol.TypeConversations || string(got.Payload) != otherPayload {
		t.Errorf("capable conn's conversations reply = %s %s, want %s unchanged", got.Type, got.Payload, otherPayload)
	}
}
