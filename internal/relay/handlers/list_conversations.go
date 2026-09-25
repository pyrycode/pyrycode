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
//
// WorkspaceLabel (#2208) is the per-key read the row projection needs. Its
// presence here is not in tension with WorkspaceLabeler in rename_workspace.go
// deliberately lacking one: that interface omits the door because its reply
// echoes the requester's own validated value, so having no way to fetch a
// stored label is a disclosure barrier. This handler's job IS to disclose the
// stored label, to every client already permitted to read the row's cwd.
type ConversationLister interface {
	List(filter ...conversations.ListFilter) []conversations.Conversation
	WorkspaceLabel(cwd string) (string, bool)
}

// workspaceLabelReader is the one-method read every conversation-frame producer
// needs (#2210). Each of the six narrow handler interfaces that gained
// WorkspaceLabel satisfies it by method-set inclusion, so a handler passes its
// existing registry value straight through.
type workspaceLabelReader interface {
	WorkspaceLabel(cwd string) (string, bool)
}

// workspaceLabelFor projects the label stored for cwd onto the nullable wire
// field the conversation payloads carry (#2210), returning nil when no label is
// stored. It exists so the one correctness rule this projection has lives in a
// single place rather than being restated at each of the seven producers:
// presence comes ONLY from the accessor's second return, never from a
// label != "" comparison, which would collapse the registry's deliberately
// distinct stored-empty and absent states.
//
// cwd must be the same value the caller writes to its payload's Cwd field.
// Labels are keyed byte-exactly and the registry normalizes nothing, so a
// caller that looks up a request's raw path while sending a resolved one would
// report a labelled workspace as unlabelled.
//
// cmd/pyry's channel-new announce cannot reach this helper across the package
// boundary and repeats the three lines inline.
func workspaceLabelFor(r workspaceLabelReader, cwd string) *string {
	if label, ok := r.WorkspaceLabel(cwd); ok {
		return &label
	}
	return nil
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
			// Resolved from this row's own Cwd, passed verbatim: the registry
			// matches the key byte-exactly and normalizes nothing, so
			// canonicalizing here would look up a key rename_workspace never
			// stored and report a labelled workspace as unlabelled. Presence
			// comes from the second return, never from label != "": the registry
			// keeps a stored empty label distinct from an absent one, and the
			// comparison would collapse them.
			var workspaceLabel *string
			if label, ok := reg.WorkspaceLabel(conv.Cwd); ok {
				workspaceLabel = &label
			}
			out = append(out, protocol.ConversationSummary{
				ID:             string(conv.ID),
				Name:           conv.Name,
				IsPromoted:     conv.IsPromoted,
				IsArchived:     conv.IsArchived,
				IsMuted:        conv.IsMuted,
				Cwd:            conv.Cwd,
				WorkspaceLabel: workspaceLabel,
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
