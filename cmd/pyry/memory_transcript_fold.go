package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/thread"
)

type memoryTranscriptReader struct {
	reader  *history.ForwardReader
	fold    *thread.Fold
	stamps  map[uint64]time.Time
	replies *memoryReplyEvidence
}

func newMemoryTranscriptReader(h *history.Store, id conversations.ConversationID) *memoryTranscriptReader {
	var r *history.ForwardReader
	if h != nil {
		r, _ = h.Forward(id, 0)
	} // callers establish registry-owned valid IDs
	return &memoryTranscriptReader{reader: r, fold: thread.New(string(id)), stamps: make(map[uint64]time.Time), replies: newMemoryReplyEvidence(string(id))}
}
func (w *memoryTranscriptReader) feed(entries []history.Entry) error {
	if err := w.fold.Feed(entries); err != nil {
		return err
	}
	for _, e := range entries {
		if e.Type == "message" || e.Type == "assistant_delta" {
			w.stamps[e.ID] = e.TS
		}
	}
	w.replies.feed(entries, w.fold.Items())
	return nil
}

type memoryTranscriptGroup struct {
	session                   string
	unknown                   bool
	start, closed, last, text uint64
	messages                  []thread.Item
}

func (w *memoryTranscriptReader) files(c conversations.Conversation) map[string]string {
	items := w.fold.Items()
	replyUsers, replyText := w.replies.rows(items)
	userGroups := make(map[uint64]*memoryTranscriptGroup)
	sort.SliceStable(items, func(i, j int) bool { return items[i].Order < items[j].Order })
	groups := []*memoryTranscriptGroup{}
	current := make(map[string]*memoryTranscriptGroup)
	group := func(session string, start uint64, unknown bool) *memoryTranscriptGroup {
		key := fmt.Sprintf("%t/%s", unknown, session)
		if g := current[key]; g != nil {
			return g
		}
		g := &memoryTranscriptGroup{session: session, start: start, unknown: unknown}
		groups = append(groups, g)
		current[key] = g
		return g
	}
	unknownScope := uint64(0)
	for _, item := range items {
		if item.Kind == "session_divider" {
			var p struct {
				Cause, Reason string
				Previous      string `json:"previous_session_id"`
				Next          string `json:"new_session_id"`
			}
			if json.Unmarshal(item.Content, &p) != nil {
				continue
			}
			if p.Cause == "idle_sleep" || p.Cause == "capacity_eviction" || p.Reason == "idle_evict" || p.Previous == "" || p.Next == "" || p.Previous == p.Next {
				continue
			}
			predecessor := group(p.Previous, 0, false)
			if predecessor.closed == 0 {
				predecessor.closed = item.ID
			}
			// A recorded replacement creates a new logical generation even when a
			// routing ID seen earlier is reused; late text retains its predecessor group.
			delete(current, "false/"+p.Next)
			group(p.Next, item.ID, false)
			unknownScope = item.ID
			continue
		}
		if item.Kind != "user_message" || item.Status != "delivered" || item.Order == 0 || item.NoChild {
			continue
		}
		session, start, unknown := item.Session, uint64(0), item.Session == ""
		if unknown {
			session, start = fmt.Sprintf("unknown/%d", unknownScope), unknownScope
		}
		g := group(session, start, unknown)
		g.messages = append(g.messages, item)
		g.last = max(g.last, item.Order)
		g.text = max(g.text, item.Order)
		userGroups[item.Order] = g
	}
	for _, item := range items {
		if user := replyUsers[item.ID]; user != 0 {
			if g := userGroups[user]; g != nil && replyText[item.ID] != 0 {
				g.messages = append(g.messages, item)
				g.text = max(g.text, replyText[item.ID])
			}
		}
	}
	files := make(map[string]string)
	quote := func(s string) string { raw, _ := json.Marshal(s); return string(raw) }
	for _, g := range groups {
		identity, _ := json.Marshal([]any{string(c.ID), g.session, g.start, g.unknown})
		name := fmt.Sprintf("%x.md", sha256.Sum256(identity))
		var b strings.Builder
		title, _ := json.Marshal(c.Name)
		fmt.Fprintf(&b, "# Recent conversation transcript\n\nConversation: %s\nTitle: %s\nTranscript: %s\n", quote(string(c.ID)), title, strings.TrimSuffix(name, ".md"))
		if g.unknown {
			b.WriteString("Session: unknown\n")
		} else {
			fmt.Fprintf(&b, "Session: %s\n", quote(g.session))
		}
		if g.closed == 0 {
			b.WriteString("State: open\n")
		} else {
			fmt.Fprintf(&b, "State: closed\nClosing entry: %d\n", g.closed)
		}
		fmt.Fprintf(&b, "Last delivered entry: %d\nLast text entry: %d\n", g.last, g.text)
		sort.SliceStable(g.messages, func(i, j int) bool { return g.messages[i].Order < g.messages[j].Order })
		for _, item := range g.messages {
			var p struct {
				Text string `json:"text"`
			}
			if json.Unmarshal(item.Content, &p) != nil {
				continue
			}
			agent := item.Agent
			if agent == "" {
				agent = "unknown"
			}
			speaker, title := "user", "User"
			if item.Kind == "assistant_message" {
				speaker, title = "assistant", "Assistant"
			}
			fmt.Fprintf(&b, "\n## %s\nMessage: %s/%d\nTimestamp: %s\nSpeaker: %s\nAgent: %s\n\n", title, c.ID, item.Order, w.stamps[item.Order].UTC().Format(time.RFC3339Nano), speaker, agent)
			if speaker == "assistant" {
				session := "unknown"
				if item.Session != "" {
					session = quote(item.Session)
				}
				fmt.Fprintf(&b, "Session: %s\n\n", session)
			}
			fence := "```"
			for strings.Contains(p.Text, fence) {
				fence += "`"
			}
			b.WriteString(fence + "text\n" + p.Text + "\n" + fence + "\n")
		}
		files[name] = b.String()
	}
	return files
}
