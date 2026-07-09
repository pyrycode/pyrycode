package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/dispatch"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// RecentWorkspacesReader is the minimal surface this handler consumes from
// the conversations registry. *conversations.Registry satisfies it
// structurally; no adapter required. The variadic shape mirrors
// conversations.Registry.List exactly so structural matching holds.
//
// A fresh interface rather than a reuse of ConversationLister: the per-handler
// consumer-named narrow interface is the convention in this package, and it
// keeps the two read handlers decoupled. The one-line duplication is intentional.
type RecentWorkspacesReader interface {
	List(filter ...conversations.ListFilter) []conversations.Conversation
}

// RecentWorkspaces returns a dispatch.Handler that answers a recent_workspaces
// request with a recent_workspaces_list reply. It derives the distinct set of
// workspace folders from the conversations registry's Cwd values: each distinct
// Cwd appears exactly once, carrying the most-recent LastUsedAt across the
// conversations that share it, ordered most-recent-first (tie broken by Path
// ascending so ordering is deterministic).
//
// Archived conversations are included by design (#888): a workspace is a folder,
// not a conversation, and archiving a conversation does not "un-use" its folder.
// A folder whose conversations are all archived simply carries an older
// LastUsedAt and sinks to the bottom of the recency-ordered list. Conversations
// with an empty Cwd contribute no entry (an empty string is not a workspace
// path). An empty registry yields "workspaces":[], not an error.
//
// The request payload is empty by spec and ignored, mirroring ListConversations.
// The handler replies via Conn.Reply (which stamps id, ts, and in_reply_to).
func RecentWorkspaces(reg RecentWorkspacesReader) dispatch.Handler {
	return func(ctx context.Context, c *dispatch.Conn, env protocol.Envelope) error {
		// reg.List() returns a copy (registry mutex is taken internally), so the
		// fold/sort/marshal below run lock-free on data this handler owns.
		list := reg.List()

		latest := make(map[string]time.Time, len(list))
		for _, conv := range list {
			if strings.TrimSpace(conv.Cwd) == "" {
				continue
			}
			if prev, ok := latest[conv.Cwd]; !ok || conv.LastUsedAt.After(prev) {
				latest[conv.Cwd] = conv.LastUsedAt
			}
		}

		out := make([]protocol.RecentWorkspace, 0, len(latest))
		for path, ts := range latest {
			out = append(out, protocol.RecentWorkspace{Path: path, LastUsedAt: ts})
		}
		sort.SliceStable(out, func(i, j int) bool {
			if !out[i].LastUsedAt.Equal(out[j].LastUsedAt) {
				return out[i].LastUsedAt.After(out[j].LastUsedAt)
			}
			return out[i].Path < out[j].Path
		})

		payloadJSON, err := json.Marshal(protocol.RecentWorkspacesListPayload{Workspaces: out})
		if err != nil {
			return fmt.Errorf("marshal recent workspaces payload: %w", err)
		}
		return c.Reply(ctx, env, protocol.TypeRecentWorkspacesList, payloadJSON)
	}
}
