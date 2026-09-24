package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/dispatch"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

const (
	muteConvConnID    = "c-mute-conv"
	muteConvRequestID = uint64(47)
	// muteConvFirstID is the id the handler's first reply must carry: on a fresh
	// conn NextID starts at 1.
	muteConvFirstID    = uint64(1)
	muteConvTargetID   = "conv-mute-target"
	muteConvName       = "noisy-channel"
	muteConvCwd        = "/work/mute-target"
	muteConvPayloadTag = "INJECTED_MUTE_MARKER_7c1"
)

var muteConvSeedTime = time.Date(2026, 9, 1, 8, 30, 0, 0, time.UTC)

// recordingAnnouncer collects every record the handler pushes through its
// ConversationAnnouncer, standing in for #2156's conversationUpdateEmitterV2.
type recordingAnnouncer struct {
	mu  sync.Mutex
	got []protocol.ConversationUpdatedPayload
}

func (a *recordingAnnouncer) announce(p protocol.ConversationUpdatedPayload) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.got = append(a.got, p)
}

func (a *recordingAnnouncer) records() []protocol.ConversationUpdatedPayload {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]protocol.ConversationUpdatedPayload(nil), a.got...)
}

func newMuteConvConn(t *testing.T) (*dispatch.Conn, func() protocol.RoutingEnvelope) {
	t.Helper()
	out := make(chan protocol.RoutingEnvelope, 4)
	c := dispatch.NewTestConn(muteConvConnID, out, nil)
	recv := func() protocol.RoutingEnvelope {
		t.Helper()
		select {
		case env := <-out:
			return env
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for outbound envelope")
			return protocol.RoutingEnvelope{}
		}
	}
	return c, recv
}

// newMuteConvReg returns a registry whose path sits in a temp dir and has never
// been written, so the presence of that file afterwards is exactly "the handler
// saved". The one seeded row carries the supplied IsMuted.
func newMuteConvReg(t *testing.T, muted bool) (*conversations.Registry, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "conversations.json")
	reg, err := conversations.Load(path)
	if err != nil {
		t.Fatalf("load registry: %v", err)
	}
	name := muteConvName
	reg.Create(conversations.Conversation{
		ID:         conversations.ConversationID(muteConvTargetID),
		Name:       &name,
		Cwd:        muteConvCwd,
		IsPromoted: true,
		IsMuted:    muted,
		LastUsedAt: muteConvSeedTime,
	})
	return reg, path
}

func muteConvRequest(payload string) protocol.Envelope {
	return protocol.Envelope{
		ID:      muteConvRequestID,
		Type:    protocol.TypeSetConversationMuted,
		TS:      time.Now().UTC(),
		Payload: []byte(payload),
	}
}

func assertMuteConvEnvelopeShape(t *testing.T, resp protocol.RoutingEnvelope, wantType string) protocol.Envelope {
	t.Helper()
	if resp.ConnID != muteConvConnID {
		t.Errorf("Response.ConnID = %q, want %q", resp.ConnID, muteConvConnID)
	}
	var env protocol.Envelope
	if err := json.Unmarshal(resp.Frame, &env); err != nil {
		t.Fatalf("unmarshal response envelope: %v", err)
	}
	if env.Type != wantType {
		t.Errorf("Type = %q, want %q", env.Type, wantType)
	}
	if env.ID != muteConvFirstID {
		t.Errorf("ID = %d, want %d (first reply on a fresh conn)", env.ID, muteConvFirstID)
	}
	if env.InReplyTo == nil || *env.InReplyTo != muteConvRequestID {
		t.Errorf("InReplyTo = %v, want pointer to %d", env.InReplyTo, muteConvRequestID)
	}
	return env
}

// TestSetConversationMuted_SetsClearsPersistsAndAnnounces covers AC 1 and AC 2:
// the reply is correlated and carries the read-back is_muted with every other
// field unchanged, the stored row and a fresh Load from disk carry the new value,
// and the same record is pushed once through the announcer. The idempotent rows
// send the value the conversation already has.
func TestSetConversationMuted_SetsClearsPersistsAndAnnounces(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		seedMuted bool
		payload   string
		want      bool
	}{
		{"mute", false, `{"conversation_id":"` + muteConvTargetID + `","muted":true}`, true},
		{"unmute", true, `{"conversation_id":"` + muteConvTargetID + `","muted":false}`, false},
		{"mute-already-muted", true, `{"conversation_id":"` + muteConvTargetID + `","muted":true}`, true},
		{"unmute-already-unmuted", false, `{"conversation_id":"` + muteConvTargetID + `","muted":false}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			reg, regPath := newMuteConvReg(t, tc.seedMuted)
			c, recv := newMuteConvConn(t)
			ann := &recordingAnnouncer{}

			h := SetConversationMuted(reg, regPath, ann.announce, testLogger(t))
			if err := h(context.Background(), c, muteConvRequest(tc.payload)); err != nil {
				t.Fatalf("handler: %v", err)
			}

			env := assertMuteConvEnvelopeShape(t, recv(), protocol.TypeConversationUpdated)
			var reply protocol.ConversationUpdatedPayload
			if err := json.Unmarshal(env.Payload, &reply); err != nil {
				t.Fatalf("unmarshal conversation_updated payload: %v", err)
			}
			if reply.IsMuted != tc.want {
				t.Errorf("reply IsMuted = %v, want %v", reply.IsMuted, tc.want)
			}
			if reply.ID != muteConvTargetID {
				t.Errorf("reply ID = %q, want %q", reply.ID, muteConvTargetID)
			}
			if reply.Name == nil || *reply.Name != muteConvName {
				t.Errorf("reply Name = %v, want pointer to %q (unchanged)", reply.Name, muteConvName)
			}
			if reply.Cwd != muteConvCwd || !reply.IsPromoted || reply.IsArchived {
				t.Errorf("reply Cwd/IsPromoted/IsArchived = %q/%v/%v, want %q/true/false (unchanged)",
					reply.Cwd, reply.IsPromoted, reply.IsArchived, muteConvCwd)
			}
			if !reply.LastUsedAt.Equal(muteConvSeedTime) {
				t.Errorf("reply LastUsedAt = %v, want %v (unchanged)", reply.LastUsedAt, muteConvSeedTime)
			}

			stored, ok := reg.Get(conversations.ConversationID(muteConvTargetID))
			if !ok || stored.IsMuted != tc.want {
				t.Errorf("stored IsMuted = %v (found %v), want %v", stored.IsMuted, ok, tc.want)
			}

			reloaded, err := conversations.Load(regPath)
			if err != nil {
				t.Fatalf("reload registry from disk: %v", err)
			}
			got, ok := reloaded.Get(conversations.ConversationID(muteConvTargetID))
			if !ok {
				t.Fatalf("conversation not found after reload from disk")
			}
			if got.IsMuted != tc.want {
				t.Errorf("reloaded IsMuted = %v, want %v (must survive a restart)", got.IsMuted, tc.want)
			}

			pushed := ann.records()
			if len(pushed) != 1 {
				t.Fatalf("announcer called %d times, want 1", len(pushed))
			}
			if pushed[0].ID != reply.ID || pushed[0].IsMuted != reply.IsMuted || !pushed[0].LastUsedAt.Equal(reply.LastUsedAt) {
				t.Errorf("pushed record = %+v, want the reply record %+v", pushed[0], reply)
			}
		})
	}
}

// TestSetConversationMuted_Rejects covers AC 3: a malformed payload, a missing
// (or null) muted key, and an unknown conversation_id each get a non-retryable
// error with a fixed static message; nothing is saved (the registry file is
// never written), nothing is pushed, the stored row is untouched, and the
// payload marker reaches neither the reply nor the log.
func TestSetConversationMuted_Rejects(t *testing.T) {
	t.Parallel()
	const malformedMsg = "malformed set_conversation_muted payload"
	cases := []struct {
		name     string
		payload  string
		wantCode string
		wantMsg  string
	}{
		{"malformed-json", `{"conversation_id":"` + muteConvPayloadTag + `"`, protocol.CodeProtocolMalformed, malformedMsg},
		{"muted-wrong-type", `{"conversation_id":"` + muteConvTargetID + `","muted":"` + muteConvPayloadTag + `"}`, protocol.CodeProtocolMalformed, malformedMsg},
		{"muted-missing", `{"conversation_id":"` + muteConvTargetID + `","x":"` + muteConvPayloadTag + `"}`, protocol.CodeProtocolMalformed, malformedMsg},
		{"muted-null", `{"conversation_id":"` + muteConvTargetID + `","muted":null,"x":"` + muteConvPayloadTag + `"}`, protocol.CodeProtocolMalformed, malformedMsg},
		{"unknown-id", `{"conversation_id":"` + muteConvPayloadTag + `","muted":true}`, protocol.CodeConversationNotFound, msgMuteConversationNotFound},
		{"empty-id", `{"conversation_id":"","muted":true,"x":"` + muteConvPayloadTag + `"}`, protocol.CodeConversationNotFound, msgMuteConversationNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var logBuf bytes.Buffer
			logger := slog.New(slog.NewJSONHandler(&logBuf, nil))
			reg, regPath := newMuteConvReg(t, false)
			c, recv := newMuteConvConn(t)
			ann := &recordingAnnouncer{}

			h := SetConversationMuted(reg, regPath, ann.announce, logger)
			if err := h(context.Background(), c, muteConvRequest(tc.payload)); err != nil {
				t.Fatalf("handler: %v", err)
			}

			resp := recv()
			env := assertMuteConvEnvelopeShape(t, resp, protocol.TypeError)
			payload := assertErrorPayload(t, env, tc.wantCode, tc.wantMsg)
			if payload.Retryable {
				t.Errorf("Retryable = true, want false")
			}
			if bytes.Contains(resp.Frame, []byte(muteConvPayloadTag)) {
				t.Errorf("reply frame echoed payload bytes: %s", resp.Frame)
			}
			if strings.Contains(logBuf.String(), muteConvPayloadTag) {
				t.Errorf("log leaked payload bytes: %s", logBuf.String())
			}

			if _, err := os.Stat(regPath); !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("registry file stat err = %v, want not-exist (a reject must not save)", err)
			}
			if n := len(ann.records()); n != 0 {
				t.Errorf("announcer called %d times, want 0", n)
			}
			stored, ok := reg.Get(conversations.ConversationID(muteConvTargetID))
			if !ok || stored.IsMuted {
				t.Errorf("seeded row IsMuted = %v (found %v), want false (a reject must not mutate)", stored.IsMuted, ok)
			}
		})
	}
}

// TestSetConversationMuted_NilAnnouncer pins the ConversationAnnouncer contract
// that nil means "no fan-out": the write and the reply still happen.
func TestSetConversationMuted_NilAnnouncer(t *testing.T) {
	t.Parallel()
	reg, regPath := newMuteConvReg(t, false)
	c, recv := newMuteConvConn(t)

	h := SetConversationMuted(reg, regPath, nil, testLogger(t))
	req := muteConvRequest(`{"conversation_id":"` + muteConvTargetID + `","muted":true}`)
	if err := h(context.Background(), c, req); err != nil {
		t.Fatalf("handler: %v", err)
	}
	assertMuteConvEnvelopeShape(t, recv(), protocol.TypeConversationUpdated)
	if stored, _ := reg.Get(conversations.ConversationID(muteConvTargetID)); !stored.IsMuted {
		t.Errorf("stored IsMuted = false, want true")
	}
}
