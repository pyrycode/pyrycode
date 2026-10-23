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
	started   time.Time
	commandAt time.Time
	opened    map[string]time.Time
	queued    bool
	liveIdle  *protocol.Envelope
}

func (s *sourceTurn) feed(t *testing.T, conv string, entries []history.Entry) {
	t.Helper()
	for _, e := range entries {
		if e.ID <= s.watermark {
			continue
		}
		s.watermark = e.ID
		var p struct {
			ConversationID string    `json:"conversation_id"`
			TurnID         string    `json:"turn_id"`
			Parent         string    `json:"parent_tool_use_id"`
			Text           string    `json:"text"`
			Reason         string    `json:"stop_reason"`
			OccurredAt     time.Time `json:"occurred_at"`
		}
		if json.Unmarshal(e.Payload, &p) != nil {
			t.Fatal("invalid source payload")
		}
		if p.ConversationID == conv && (e.Type == "send_accepted" || e.Type == protocol.TypeMessage) {
			if !s.queued && e.TS.After(s.commandAt) {
				s.commandAt = e.TS
			}
			s.queued = true
		}
		if p.ConversationID != conv || p.Parent != "" || p.TurnID == "" {
			continue
		}
		if e.Type == "main_turn_opened" && e.Session != nil && !p.OccurredAt.IsZero() {
			if s.opened == nil {
				s.opened = make(map[string]time.Time)
			}
			s.opened[e.Session.Kind+"\x00"+e.Session.SessionID+"\x00"+p.TurnID] = p.OccurredAt
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
			opened, executing := s.opened[e.Session.Kind+"\x00"+e.Session.SessionID+"\x00"+p.TurnID]
			// Append order cannot establish execution order: a predecessor's opening
			// can be retained after the watched command was accepted.
			if (s.queued && !executing) || (executing && opened.Before(s.commandAt)) {
				continue
			}
			s.turn, s.session = p.TurnID, *e.Session
			s.started = e.TS
			if executing {
				s.started = opened
			}
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

// QuestionBoundary separates pre-answer prose from continuation while retaining
// the parked turn's producing identity. A predecessor cannot answer a successor.
func QuestionBoundary(t *testing.T, source SourceFacts, conv string) (SourceFacts, string) {
	t.Helper()
	if source == nil {
		t.Fatal("question requires source observation")
	}
	entries := source(conv)
	var before sourceTurn
	before.feed(t, conv, entries)
	if before.turn == "" {
		for _, e := range entries {
			var p struct {
				Conversation string `json:"conversation_id"`
				Turn         string `json:"turn_id"`
				Parent       string `json:"parent_tool_use_id"`
			}
			if json.Unmarshal(e.Payload, &p) == nil && p.Conversation == conv && p.Parent == "" && p.Turn != "" && e.Session != nil && e.Type == protocol.TypeToolUse {
				before.turn, before.session = p.Turn, *e.Session
			}
		}
	}
	mark := before.watermark
	var anchors []history.Entry
	for _, e := range entries {
		if e.Type == "main_turn_opened" {
			var p struct {
				ConversationID string `json:"conversation_id"`
			}
			if json.Unmarshal(e.Payload, &p) != nil || p.ConversationID != conv {
				continue
			}
			anchors = append(anchors, e)
		}
	}
	return func(id string) []history.Entry {
		if id != conv {
			t.Fatal("question boundary crossed conversations")
		}
		after := append([]history.Entry(nil), anchors...)
		for _, e := range source(conv) {
			if e.ID <= mark {
				continue
			}
			var p struct {
				Conversation string `json:"conversation_id"`
				Turn         string `json:"turn_id"`
				Parent       string `json:"parent_tool_use_id"`
			}
			if json.Unmarshal(e.Payload, &p) != nil {
				t.Fatal("invalid question source")
			}
			if p.Conversation == conv && p.Parent == "" && (before.turn == "" || (p.Turn == before.turn && e.Session != nil && *e.Session == before.session)) {
				after = append(after, e)
			}
		}
		return after
	}, before.text.String()
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
	if env.SessionStateCleared {
		var id string
		if env.Type == protocol.TypeTurnState && json.Unmarshal(env.ConversationID, &id) == nil && id == conv {
			s.liveIdle = nil
		}
		return false
	}
	if env.Type == protocol.TypeTurnState {
		var p protocol.TurnStatePayload
		if json.Unmarshal(env.Payload, &p) != nil {
			t.Fatal("invalid live turn state")
		}
		if p.ConversationID == conv {
			if env.SessionStateCleared || p.State != "idle" {
				s.liveIdle = nil
			} else {
				copy := env
				s.liveIdle = &copy
			}
		}
	}
	// App delivery and source append run independently. Keep the actual received
	// terminal until its producing source is available; never manufacture one.
	if s.liveIdle == nil || !s.ended {
		return false
	}
	env = *s.liveIdle
	if len(env.SessionID) != 0 {
		var session string
		if json.Unmarshal(env.SessionID, &session) != nil || session != s.session.SessionID {
			return false
		}
		// Execution belongs to the exact source turn, independently of when its
		// history content is appended. Queue acceptance cannot establish this bound.
		return !env.TS.Before(s.started)
	}
	return !env.TS.Before(s.end.TS)
}

func pollDeadline(deadline time.Time) time.Time {
	poll := time.Now().Add(20 * time.Millisecond)
	if deadline.Before(poll) {
		return deadline
	}
	return poll
}
