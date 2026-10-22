package scenario

import (
	"encoding/json"
	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// SourceFacts reads retained history, independently of encrypted app delivery.
type SourceFacts func(string) []history.Entry

// SourceSnapshot reads current source entries in append order. A fresh store
// avoids retaining an append cursor while another process writes the history.
func SourceSnapshot(t *testing.T, home string) SourceFacts {
	t.Helper()
	return func(conv string) []history.Entry {
		t.Helper()
		store := history.New(filepath.Join(home, ".pyry", "test"))
		var entries []history.Entry
		cursor := ""
		for {
			page, err := store.Page(conversations.ConversationID(conv), cursor, history.MaxPageEntries)
			if err != nil {
				t.Fatal("read source history: ", err)
			}
			entries = append(entries, page.Entries...)
			if page.AtStart {
				break
			}
			cursor = page.Cursor
		}
		slices.Reverse(entries)
		return entries
	}
}

// SourceSince captures a conversation watermark before submitting a message.
func SourceSince(t *testing.T, home, conv string) SourceFacts {
	t.Helper()
	snapshot := SourceSnapshot(t, home)
	entries := snapshot(conv)
	var mark uint64
	if len(entries) != 0 {
		mark = entries[len(entries)-1].ID
	}
	return func(id string) []history.Entry {
		if id != conv {
			t.Fatal("source cursor used for another conversation")
		}
		return slices.DeleteFunc(snapshot(conv), func(e history.Entry) bool { return e.ID <= mark })
	}
}

// ObserveSource keeps one wire reader and marks source history before each
// submitted turn. It never projects source facts into app envelopes.
func ObserveSource(t *testing.T, home string, w Wire) Wire {
	t.Helper()
	snapshot := SourceSnapshot(t, home)
	marks := map[string]uint64{}
	send := w.Send
	w.Send = func(kind string, payload any) uint64 {
		if kind == protocol.TypeSendMessage {
			p, ok := payload.(protocol.SendMessagePayload)
			if !ok {
				t.Fatal("source observer requires a typed send_message")
			}
			entries := snapshot(p.ConversationID)
			marks[p.ConversationID] = 0
			if len(entries) != 0 {
				marks[p.ConversationID] = entries[len(entries)-1].ID
			}
		}
		return send(kind, payload)
	}
	w.Source = func(conv string) []history.Entry {
		entries := snapshot(conv)
		return slices.DeleteFunc(entries, func(e history.Entry) bool { return e.ID <= marks[conv] })
	}
	return w
}

type sourceTurn struct {
	watermark uint64
	session   history.SessionProvenance
	turn      string
	text      strings.Builder
	ended     bool
	end       history.Entry
	reason    string
}

func (s *sourceTurn) feed(t *testing.T, conv string, entries []history.Entry) {
	t.Helper()
	for _, e := range entries {
		if e.ID <= s.watermark {
			continue
		}
		s.watermark = e.ID
		var p struct {
			ConversationID string `json:"conversation_id"`
			TurnID         string `json:"turn_id"`
			Parent         string `json:"parent_tool_use_id"`
			Text           string `json:"text"`
			Reason         string `json:"stop_reason"`
		}
		if json.Unmarshal(e.Payload, &p) != nil {
			t.Fatal("invalid source payload")
		}
		if p.ConversationID != conv || p.Parent != "" || p.TurnID == "" {
			continue
		}
		switch e.Type {
		case protocol.TypeAssistantDelta, protocol.TypeToolUse, protocol.TypeToolResult, protocol.TypeToolDenied, protocol.TypeTurnEnd:
		default:
			continue
		}
		if e.Session == nil || e.Session.SessionID == "" {
			t.Fatal("source content lacks producing session")
		}
		if s.turn == "" && (e.Type == protocol.TypeAssistantDelta || e.Type == protocol.TypeTurnEnd) {
			s.turn, s.session = p.TurnID, *e.Session
		}
		if s.turn != p.TurnID || s.session != *e.Session {
			continue
		}
		if e.Type == protocol.TypeAssistantDelta && !s.ended {
			s.text.WriteString(p.Text)
		}
		if e.Type == protocol.TypeTurnEnd && !s.ended {
			s.ended, s.end, s.reason = true, e, p.Reason
		}
	}
}

func sourceFacts(t *testing.T, sources []SourceFacts, conv string) []history.Entry {
	t.Helper()
	if len(sources) != 1 || sources[0] == nil {
		t.Fatal("source observation is required")
	}
	return sources[0](conv)
}

func (s *sourceTurn) idle(t *testing.T, env protocol.Envelope, conv string) bool {
	t.Helper()
	if env.Type != protocol.TypeTurnState || !s.ended {
		return false
	}
	var p protocol.TurnStatePayload
	if json.Unmarshal(env.Payload, &p) != nil {
		t.Fatal("invalid live turn state")
	}
	return p.ConversationID == conv && p.State == "idle" && !env.TS.Before(s.end.TS)
}

func pollDeadline(deadline time.Time) time.Time {
	poll := time.Now().Add(20 * time.Millisecond)
	if deadline.Before(poll) {
		return deadline
	}
	return poll
}
