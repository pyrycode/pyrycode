package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/dispatch"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// ConversationLister is the minimal surface this handler consumes from
// the conversations registry. *conversations.Registry satisfies it
// structurally; no adapter required. The variadic shape mirrors
// conversations.Registry.List exactly so structural matching holds.
type ConversationLister interface {
	List(filter ...conversations.ListFilter) []conversations.Conversation
}

// ListConversations returns a dispatch.Handler that answers a
// list_conversations request with a conversations envelope. The handler
// reads the registry, projects each row to a protocol.ConversationSummary,
// sorts by LastUsedAt asc / ID asc (mirroring Registry.Save), and replies
// via Conn.Reply (which stamps id, ts, and in_reply_to).
func ListConversations(reg ConversationLister) dispatch.Handler {
	return func(ctx context.Context, c *dispatch.Conn, env protocol.Envelope) error {
		list := reg.List()
		sort.SliceStable(list, func(i, j int) bool {
			if !list[i].LastUsedAt.Equal(list[j].LastUsedAt) {
				return list[i].LastUsedAt.Before(list[j].LastUsedAt)
			}
			return list[i].ID < list[j].ID
		})

		out := make([]protocol.ConversationSummary, 0, len(list))
		for _, conv := range list {
			out = append(out, protocol.ConversationSummary{
				ID:         string(conv.ID),
				Name:       conv.Name,
				IsPromoted: conv.IsPromoted,
				Cwd:        conv.Cwd,
				// LastMessageTS collapses onto LastUsedAt: Conversation
				// does not carry a distinct last-message timestamp today.
				// When a real LastMessageTS lands on the registry, update
				// this projection only.
				LastMessageTS: conv.LastUsedAt,
				LastUsedAt:    conv.LastUsedAt,
			})
		}

		payloadJSON, err := json.Marshal(protocol.ConversationsPayload{Conversations: out})
		if err != nil {
			return fmt.Errorf("marshal conversations payload: %w", err)
		}
		return c.Reply(ctx, env, protocol.TypeConversations, payloadJSON)
	}
}
