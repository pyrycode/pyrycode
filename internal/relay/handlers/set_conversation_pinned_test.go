package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/dispatch"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

type testPinRegistry struct {
	*conversations.Registry
	saves       int
	vanish      bool
	mutationErr error
}

func (r *testPinRegistry) Save(path string) error {
	r.saves++
	return r.Registry.Save(path)
}

func (r *testPinRegistry) SetPinned(id conversations.ConversationID, pinned bool, now time.Time) error {
	if r.mutationErr != nil {
		return r.mutationErr
	}
	return r.Registry.SetPinned(id, pinned, now)
}

func (r *testPinRegistry) Get(id conversations.ConversationID) (conversations.Conversation, bool) {
	if r.vanish {
		return conversations.Conversation{}, false
	}
	return r.Registry.Get(id)
}

func testPinRequest(payload string) protocol.Envelope {
	return protocol.Envelope{ID: 71, Type: protocol.TypeSetConversationPinned, Payload: []byte(payload)}
}

func testPinReply(t *testing.T, out <-chan protocol.RoutingEnvelope, request protocol.Envelope, wantType string) protocol.Envelope {
	t.Helper()
	if len(out) != 1 {
		t.Fatalf("replies = %d, want 1", len(out))
	}
	var env protocol.Envelope
	if err := json.Unmarshal((<-out).Frame, &env); err != nil {
		t.Fatal(err)
	}
	if env.Type != wantType || env.InReplyTo == nil || *env.InReplyTo != request.ID {
		t.Fatalf("reply = %#v, want correlated %s", env, wantType)
	}
	return env
}

func TestSetConversationPinned_Transitions(t *testing.T) {
	t.Parallel()
	reg, path := newMuteConvReg(t, true)
	reg.Update(muteConvTargetID, func(cv *conversations.Conversation) {
		cv.ReadUpTo = 19
		cv.CurrentSessionID = "stored-session"
		cv.SessionHistory = []string{"old-session"}
	})
	workspace := "workspace"
	reg.SetWorkspaceLabel(muteConvCwd, &workspace)
	r := &testPinRegistry{Registry: reg}
	var first *time.Time
	for i, pinned := range []bool{true, true, false, false, true} {
		before, _ := reg.Get(muteConvTargetID)
		out := make(chan protocol.RoutingEnvelope, 1)
		c := dispatch.NewTestConn("pin", out, nil)
		var pushed []protocol.ConversationUpdatedPayload
		announce := func(p protocol.ConversationUpdatedPayload) {
			if len(out) != 1 {
				t.Error("announcement preceded reply")
			}
			pushed = append(pushed, p)
		}
		req := testPinRequest(fmt.Sprintf(`{"conversation_id":%q,"pinned":%t}`, muteConvTargetID, pinned))
		start := time.Now().UTC()
		if err := SetConversationPinned(r, path, announce, testLogger(t))(context.Background(), c, req); err != nil {
			t.Fatal(err)
		}
		after, _ := reg.Get(muteConvTargetID)
		if pinned {
			if after.PinnedAt == nil || after.PinnedAt.Location() != time.UTC {
				t.Fatalf("pin is not UTC: %v", after.PinnedAt)
			}
			if before.PinnedAt == nil && (after.PinnedAt.Before(start) || after.PinnedAt.After(time.Now())) {
				t.Fatal("pin outside host current time")
			}
			if i == 0 {
				first = after.PinnedAt
			}
			if i == 1 && !after.PinnedAt.Equal(*first) {
				t.Fatal("repeat pin changed timestamp")
			}
			if i == 4 && !after.PinnedAt.After(*first) {
				t.Fatal("re-pin reused old timestamp")
			}
		} else if after.PinnedAt != nil {
			t.Fatal("unpin retained timestamp")
		}
		unchanged := after
		unchanged.PinnedAt = before.PinnedAt
		if !reflect.DeepEqual(before, unchanged) {
			t.Fatalf("pin changed other fields: %#v", after)
		}
		reloaded, err := conversations.Load(path)
		if err != nil {
			t.Fatal(err)
		}
		disk, ok := reloaded.Get(muteConvTargetID)
		if !ok || !reflect.DeepEqual(after, disk) {
			t.Fatalf("disk = %#v, memory = %#v", disk, after)
		}
		env := testPinReply(t, out, req, protocol.TypeConversationUpdated)
		var reply protocol.ConversationUpdatedPayload
		if err := json.Unmarshal(env.Payload, &reply); err != nil {
			t.Fatal(err)
		}
		label := "workspace"
		want := protocol.ConversationUpdatedPayload{ID: muteConvTargetID, IsPromoted: true, IsMuted: true, PinnedAt: after.PinnedAt, ReadUpTo: 19, Name: after.Name, Cwd: muteConvCwd, WorkspaceLabel: &label, LastUsedAt: muteConvSeedTime}
		if !reflect.DeepEqual(reply, want) || len(pushed) != 1 || !reflect.DeepEqual(pushed[0], want) {
			t.Fatalf("reply/push = %#v / %#v, want %#v", reply, pushed, want)
		}
		if !pinned && !bytes.Contains(env.Payload, []byte(`"pinned_at":null`)) {
			t.Fatalf("missing explicit null: %s", env.Payload)
		}
		if r.saves != i+1 {
			t.Fatalf("Save calls = %d, want %d", r.saves, i+1)
		}
	}
}

func TestSetConversationPinned_Eligibility(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name                                string
		promoted, archived, seedPin, pinned bool
	}{
		{"archived-pin", true, true, false, true},
		{"unpromoted-unpin", false, false, true, false},
		{"archived-unpromoted-unpin", false, true, true, false},
		{"already-unpinned-unpromoted", false, false, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reg, path := newMuteConvReg(t, false)
			reg.Update(muteConvTargetID, func(cv *conversations.Conversation) {
				cv.IsPromoted, cv.IsArchived = tc.promoted, tc.archived
				if tc.seedPin {
					at := muteConvSeedTime
					cv.PinnedAt = &at
				}
			})
			r := &testPinRegistry{Registry: reg}
			out := make(chan protocol.RoutingEnvelope, 1)
			req := testPinRequest(fmt.Sprintf(`{"conversation_id":%q,"pinned":%t}`, muteConvTargetID, tc.pinned))
			if err := SetConversationPinned(r, path, nil, testLogger(t))(context.Background(), dispatch.NewTestConn("pin", out, nil), req); err != nil {
				t.Fatal(err)
			}
			testPinReply(t, out, req, protocol.TypeConversationUpdated)
			cv, _ := reg.Get(muteConvTargetID)
			if (cv.PinnedAt != nil) != tc.pinned || cv.IsPromoted != tc.promoted || cv.IsArchived != tc.archived || r.saves != 1 {
				t.Fatalf("unexpected stored state: %#v, saves %d", cv, r.saves)
			}
		})
	}
}

func TestSetConversationPinned_Rejects(t *testing.T) {
	t.Parallel()
	const marker = "PRIVATE_PIN_INPUT"
	target := `"conversation_id":"` + muteConvTargetID + `"`
	for _, tc := range []struct {
		name, payload, code, message string
		unpromoted                   bool
	}{
		{"syntax", `{` + target + `,"pinned":true`, protocol.CodeProtocolMalformed, "malformed set_conversation_pinned payload", false},
		{"wrong-bool", `{` + target + `,"pinned":"` + marker + `"}`, protocol.CodeProtocolMalformed, "malformed set_conversation_pinned payload", false},
		{"partial-valid-id", `{` + target + `,"pinned":true,"conversation_id":17}`, protocol.CodeProtocolMalformed, "malformed set_conversation_pinned payload", false},
		{"missing", `{` + target + `}`, protocol.CodeProtocolMalformed, "malformed set_conversation_pinned payload", false},
		{"null-bool", `{` + target + `,"pinned":null}`, protocol.CodeProtocolMalformed, "malformed set_conversation_pinned payload", false},
		{"array", `[]`, protocol.CodeProtocolMalformed, "malformed set_conversation_pinned payload", false},
		{"null", `null`, protocol.CodeProtocolMalformed, "malformed set_conversation_pinned payload", false},
		{"unknown", `{"conversation_id":"` + marker + `","pinned":true}`, protocol.CodeConversationNotFound, "conversation not found", false},
		{"empty", `{"conversation_id":"","pinned":true}`, protocol.CodeConversationNotFound, "conversation not found", false},
		{"unpromoted", `{` + target + `,"pinned":true}`, protocol.CodeConversationNotPromoted, "conversation not promoted", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reg, path := newMuteConvReg(t, true)
			reg.Update(muteConvTargetID, func(cv *conversations.Conversation) { cv.IsPromoted = !tc.unpromoted })
			before := reg.List()
			r := &testPinRegistry{Registry: reg}
			var logs bytes.Buffer
			out := make(chan protocol.RoutingEnvelope, 1)
			ann := &recordingAnnouncer{}
			req := testPinRequest(tc.payload)
			if err := SetConversationPinned(r, path, ann.announce, slog.New(slog.NewJSONHandler(&logs, nil)))(context.Background(), dispatch.NewTestConn("pin", out, nil), req); err != nil {
				t.Fatal(err)
			}
			env := testPinReply(t, out, req, protocol.TypeError)
			p := assertErrorPayload(t, env, tc.code, tc.message)
			if p.Retryable || r.saves != 0 || len(ann.records()) != 0 || !reflect.DeepEqual(before, reg.List()) {
				t.Fatal("reject mutated, saved, announced or was retryable")
			}
			if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("reject wrote registry: %v", err)
			}
			for _, secret := range []string{marker, muteConvTargetID, "cannot unmarshal", "unexpected end"} {
				if bytes.Contains(env.Payload, []byte(secret)) || strings.Contains(logs.String(), secret) {
					t.Fatalf("reject leaked %q", secret)
				}
			}
		})
	}
}

func TestSetConversationPinned_Vanished(t *testing.T) {
	t.Parallel()
	reg, path := newMuteConvReg(t, false)
	r := &testPinRegistry{Registry: reg, vanish: true}
	out := make(chan protocol.RoutingEnvelope, 1)
	ann := &recordingAnnouncer{}
	req := testPinRequest(`{"conversation_id":"` + muteConvTargetID + `","pinned":true}`)
	if err := SetConversationPinned(r, path, ann.announce, testLogger(t))(context.Background(), dispatch.NewTestConn("pin", out, nil), req); err != nil {
		t.Fatal(err)
	}
	env := testPinReply(t, out, req, protocol.TypeError)
	p := assertErrorPayload(t, env, protocol.CodeConversationNotFound, "conversation not found")
	if p.Retryable || r.saves != 0 || len(ann.records()) != 0 {
		t.Fatal("vanished row saved or announced")
	}
}

func TestSetConversationPinned_Failures(t *testing.T) {
	t.Parallel()
	for _, failure := range []string{"save", "reply", "mutation"} {
		t.Run(failure, func(t *testing.T) {
			reg, path := newMuteConvReg(t, false)
			r := &testPinRegistry{Registry: reg}
			ctx := context.Background()
			if failure == "save" {
				path = t.TempDir()
			}
			if failure == "mutation" {
				r.mutationErr = errors.New("PRIVATE_MUTATION_ERROR")
			}
			out := make(chan protocol.RoutingEnvelope, 1)
			if failure == "reply" {
				cancelCtx, cancel := context.WithCancel(ctx)
				cancel()
				ctx = cancelCtx
				out = make(chan protocol.RoutingEnvelope)
			}
			ann := &recordingAnnouncer{}
			req := testPinRequest(`{"conversation_id":"` + muteConvTargetID + `","pinned":true}`)
			err := SetConversationPinned(r, path, ann.announce, testLogger(t))(ctx, dispatch.NewTestConn("pin", out, nil), req)
			if failure == "mutation" {
				cv, _ := reg.Get(muteConvTargetID)
				if err == nil || strings.Contains(err.Error(), "PRIVATE_MUTATION_ERROR") || r.saves != 0 || len(ann.records()) != 0 || cv.PinnedAt != nil || len(out) != 0 {
					t.Fatalf("unexpected mutation error handling: %v", err)
				}
				return
			}
			if (err != nil) != (failure == "reply") {
				t.Fatalf("handler error: %v", err)
			}
			cv, _ := reg.Get(muteConvTargetID)
			if cv.PinnedAt == nil || r.saves != 1 || len(ann.records()) != 1 || !ann.records()[0].PinnedAt.Equal(*cv.PinnedAt) {
				t.Fatal("failure lost write or announcement")
			}
			if failure == "save" {
				testPinReply(t, out, req, protocol.TypeConversationUpdated)
			}
		})
	}
}
