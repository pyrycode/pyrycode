package scenario

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// SourceObserver observes retained facts separately from a single live reader.
// Main completion is reported only after its actual terminal live idle.
type SourceObserver struct {
	t        *testing.T
	snapshot SourceFacts
	watches  map[string]*sourceWatch
	OnEntry  func(history.Entry)
}

type sourceWatch struct {
	mark    uint64
	turn    sourceTurn
	pending *history.Entry
	idle    bool
}

func (o *SourceObserver) Text(conv string) string {
	if w := o.watches[conv]; w != nil {
		return w.turn.text.String()
	}
	return ""
}

func (o *SourceObserver) Ended(conv string) bool {
	w := o.watches[conv]
	return w != nil && w.turn.ended && w.idle
}

func (o *SourceObserver) Terminal(conv string) (history.Entry, bool) {
	w := o.watches[conv]
	if w == nil || !w.turn.ended {
		return history.Entry{}, false
	}
	return w.turn.end, true
}

func NewSourceObserver(t *testing.T, home string) *SourceObserver {
	t.Helper()
	return &SourceObserver{t: t, snapshot: SourceSnapshot(t, home), watches: map[string]*sourceWatch{}}
}

// Watch captures the source boundary before submitting a command.
func (o *SourceObserver) Watch(conv string) {
	o.t.Helper()
	w := &sourceWatch{}
	for _, e := range o.snapshot(conv) {
		w.mark = e.ID
	}
	w.turn.watermark = w.mark
	o.watches[conv] = w
}

func (o *SourceObserver) Before(env protocol.Envelope) {
	if env.Type != protocol.TypeSendMessage {
		return
	}
	var p protocol.SendMessagePayload
	if json.Unmarshal(env.Payload, &p) != nil {
		o.t.Fatal("invalid observed send")
	}
	o.Watch(p.ConversationID)
}

// Next returns only actual app envelopes; source polling may return an empty
// envelope with true while the caller's collection window remains open.
func (o *SourceObserver) Next(next Next, deadline time.Time) (protocol.Envelope, bool) {
	o.t.Helper()
	if !time.Now().Before(deadline) {
		return protocol.Envelope{}, false
	}
	env := next(pollDeadline(deadline))
	switch env.Type {
	case protocol.TypeAssistantDelta, protocol.TypeToolUse, protocol.TypeToolResult, protocol.TypeToolDenied, protocol.TypeTurnEnd:
		o.t.Fatal("retired interactive content reached app transport: ", env.Type)
	}
	for conv, w := range o.watches {
		entries := o.snapshot(conv)
		w.turn.feed(o.t, conv, entries)
		if w.turn.idle(o.t, env, conv) {
			w.idle = true
		}
		if o.OnEntry == nil {
			continue
		}
		for _, e := range entries {
			if e.ID <= w.mark {
				continue
			}
			w.mark = e.ID
			var p struct {
				Conversation string `json:"conversation_id"`
				Parent       string `json:"parent_tool_use_id"`
			}
			if json.Unmarshal(e.Payload, &p) != nil {
				o.t.Fatal("invalid observed source")
			}
			if p.Conversation != conv {
				continue
			}
			switch e.Type {
			case protocol.TypeAssistantDelta, protocol.TypeToolUse, protocol.TypeToolResult, protocol.TypeToolDenied, protocol.TypeTurnEnd:
				if e.Session == nil || e.Session.SessionID == "" {
					o.t.Fatal("observed content lacks producing session")
				}
			default:
				continue
			}
			if e.Type == protocol.TypeTurnEnd && p.Parent == "" {
				if w.turn.ended && e.ID == w.turn.end.ID {
					copy := e
					w.pending = &copy
				}
				continue
			}
			if o.OnEntry != nil {
				o.OnEntry(e)
			}
		}
		if w.pending != nil && w.idle {
			if o.OnEntry != nil {
				o.OnEntry(*w.pending)
			}
			w.pending = nil
		}
	}
	return env, true
}
