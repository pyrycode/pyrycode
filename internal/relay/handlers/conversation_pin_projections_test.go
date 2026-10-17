package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/dispatch"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

func TestConversationPinProjections(t *testing.T) {
	t.Parallel()
	for _, producer := range []string{"rename", "autoname", "promote", "archive", "unarchive", "workspace", "prompt", "mute", "read", "list"} {
		for _, pinned := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/pinned=%t", producer, pinned), func(t *testing.T) {
				t.Parallel()
				reg, path := newMuteConvReg(t, true)
				at := time.Date(2026, 10, 12, 14, 15, 16, 123, time.UTC)
				reg.Update(muteConvTargetID, func(cv *conversations.Conversation) {
					if pinned {
						cv.PinnedAt = &at
					}
					if producer == "promote" {
						cv.IsPromoted = false
					}
					if producer == "autoname" {
						cv.Name = nil
					}
					if producer == "unarchive" {
						cv.IsArchived = true
						cv.ArchivedAt = &at
					}
				})
				out := make(chan protocol.RoutingEnvelope, 1)
				c := dispatch.NewTestConn("projection", out, nil)
				logger := testLogger(t)
				req := protocol.Envelope{ID: 93}
				var h dispatch.Handler
				var payload []byte
				suffix := ""
				switch producer {
				case "rename":
					h = RenameConversation(reg, path, logger)
					suffix = `,"name":"renamed"`
				case "autoname":
					calls := 0
					autoNameConversation(reg, path, func(p protocol.ConversationUpdatedPayload) {
						calls++
						var err error
						payload, err = json.Marshal(p)
						if err != nil {
							t.Fatal(err)
						}
					}, logger, "projection", muteConvTargetID, "first message")
					if calls != 1 {
						t.Fatalf("autoname announcements = %d", calls)
					}
				case "promote":
					h = PromoteConversation(reg, path, logger)
					suffix = `,"name":"channel"`
				case "archive", "unarchive":
					req.Type = protocol.TypeArchiveConversation
					if producer == "unarchive" {
						req.Type = protocol.TypeUnarchiveConversation
					}
					h = ArchiveConversation(reg, path, logger, producer == "archive")
				case "workspace":
					h = ChangeWorkspace(reg, func(string) (string, error) { return "/work/new", nil }, path, logger)
					suffix = `,"cwd":"/work/new"`
				case "prompt":
					h = SetSystemPrompt(reg, path, logger)
					suffix = `,"system_prompt":"instructions"`
				case "mute":
					h = SetConversationMuted(reg, path, nil, logger)
					suffix = `,"muted":false`
				case "read":
					h = MarkConversationRead(reg, &fakeLatestHistory{latest: 7}, path, func(p protocol.ConversationUpdatedPayload) {
						if pinned && (p.PinnedAt == nil || !p.PinnedAt.Equal(at)) {
							t.Error("read announcement lost pin")
						}
					}, logger)
					suffix = `,"up_to":7`
				case "list":
					h = ListConversationsWithAgents(reg, nil, &fakeLatestHistory{})
				}
				if h != nil {
					req.Payload = []byte(`{"conversation_id":"` + muteConvTargetID + `"` + suffix + `}`)
					if err := h(context.Background(), c, req); err != nil {
						t.Fatal(err)
					}
					wantType := protocol.TypeConversationUpdated
					if producer == "list" {
						wantType = protocol.TypeConversations
					}
					env := testPinReply(t, out, req, wantType)
					payload = env.Payload
					if producer == "list" {
						var list struct {
							Conversations []json.RawMessage `json:"conversations"`
						}
						if err := json.Unmarshal(payload, &list); err != nil {
							t.Fatal(err)
						}
						if len(list.Conversations) != 1 {
							t.Fatalf("list = %s", payload)
						}
						payload = list.Conversations[0]
					}
				}
				var raw map[string]json.RawMessage
				if err := json.Unmarshal(payload, &raw); err != nil {
					t.Fatal(err)
				}
				pin, ok := raw["pinned_at"]
				if !ok {
					t.Fatalf("%s omitted pinned_at", payload)
				}
				if pinned {
					var got time.Time
					if err := json.Unmarshal(pin, &got); err != nil {
						t.Fatal(err)
					}
					if !got.Equal(at) {
						t.Fatalf("pin = %s, want %s", pin, at)
					}
				} else if string(pin) != "null" {
					t.Fatalf("unpinned = %s, want null", pin)
				}
			})
		}
	}
}
