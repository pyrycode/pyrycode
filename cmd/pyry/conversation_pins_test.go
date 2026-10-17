package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/relay/handlers"
)

type pinCreationLog struct {
	slog.Handler
	beforeAnnounce func()
}

func (h pinCreationLog) Handle(ctx context.Context, r slog.Record) error {
	if r.Message == "control: channel.new created" || r.Message == "control: conversation.new created" {
		h.beforeAnnounce()
	}
	return h.Handler.Handle(ctx, r)
}

func TestCommandConversationPins(t *testing.T) {
	for _, producer := range []string{"channelCreator", "conversationCreator", "announceSwitchedConversation"} {
		for _, pinned := range []bool{false, true} {
			state := "unpinned"
			if pinned {
				state = "pinned"
			}
			t.Run(producer+"/"+state, func(t *testing.T) {
				home := t.TempDir()
				t.Setenv("HOME", home)
				installIdentityTrustMark(t)
				reg, path := newChannelTestRegistry(t, home)
				stamp := time.Date(2026, 10, 17, 10, 11, 12, 123, time.UTC)
				pinStoredRow := func() {
					rows := reg.List()
					if len(rows) != 1 {
						t.Fatalf("stored rows = %d", len(rows))
					}
					if err := reg.SetPinned(rows[0].ID, pinned, stamp); err != nil {
						t.Fatal(err)
					}
				}
				log := slog.New(pinCreationLog{discardLogger().Handler(), pinStoredRow})
				var updates []protocol.ConversationUpdatedPayload
				announce := func(p protocol.ConversationUpdatedPayload) { updates = append(updates, p) }
				switch producer {
				case "channelCreator":
					_, err := channelCreator(reg, func(string, string) (string, error) { return "session", nil }, path, announce, log)(home, "channel")
					if err != nil {
						t.Fatal(err)
					}
				case "conversationCreator":
					mint := conversationMinterFunc(func(context.Context, string, string, string, handlers.CreateSettings) (string, string, error) {
						return "session", home, nil
					})
					_, err := conversationCreator(reg, mint, path, announce, log)(home, "channel", "channel", nil, nil)
					if err != nil {
						t.Fatal(err)
					}
				case "announceSwitchedConversation":
					reg.Create(conversations.Conversation{ID: testConvID, Cwd: home, IsPromoted: true})
					pinStoredRow()
					announceSwitchedConversation(reg, testConvID, announce)
				}
				if len(updates) != 1 {
					t.Fatalf("updates = %d, want 1", len(updates))
				}
				got := updates[0]
				if pinned && (got.PinnedAt == nil || !got.PinnedAt.Equal(stamp)) || !pinned && got.PinnedAt != nil {
					t.Fatalf("pin = %v, pinned=%v", got.PinnedAt, pinned)
				}
				raw, err := json.Marshal(got)
				if err != nil {
					t.Fatal(err)
				}
				var fields map[string]json.RawMessage
				if err := json.Unmarshal(raw, &fields); err != nil {
					t.Fatal(err)
				}
				if !pinned && string(fields["pinned_at"]) != "null" {
					t.Fatalf("unpinned wire field = %s, want explicit null", fields["pinned_at"])
				}
			})
		}
	}
}
