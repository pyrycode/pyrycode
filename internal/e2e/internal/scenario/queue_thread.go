package scenario

import (
	"encoding/json"
	"maps"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// queuePlacementReader keeps canonical items across catch-up and live patches.
// Foreign conversations never alter its revision or identity state.
type queuePlacementReader struct {
	t               *testing.T
	w               Wire
	conv            string
	items           map[uint64]protocol.ThreadItem
	received        map[uint64]protocol.ThreadItem
	pending         []protocol.ThreadItem
	version         uint64
	epoch           string
	checkpoint      uint64
	request         uint64
	requested       uint64
	retryAfter      time.Time
	sourceID        uint64
	ends            []history.Entry
	since           time.Time
	running         map[string]bool
	idles           []string
	completed       map[string]bool
	completionBatch int
}

func newQueuePlacementReader(t *testing.T, w Wire, conv string) *queuePlacementReader {
	t.Helper()
	// One queue observation spans its running predecessor and queued successors.
	// Keep that execution window while ordinary sends establish their own window.
	if w.sourceWindow != nil {
		w.Source = w.sourceWindow(conv)
	}
	request := w.Send(protocol.TypeThreadCatchUp, protocol.ThreadCatchUpPayload{ConversationID: conv, Limit: 100})
	r := &queuePlacementReader{t: t, w: w, conv: conv, items: map[uint64]protocol.ThreadItem{}}
	deadline := time.Now().Add(30 * time.Second)
	for {
		env := w.Next(deadline)
		if env.Type == "" || env.Type == protocol.TypeError || env.Type == "queue_state" {
			t.Fatal("initial thread catch-up failed")
		}
		if env.Type != protocol.TypeThreadItems || env.InReplyTo == nil || *env.InReplyTo != request {
			continue
		}
		var p protocol.ThreadItemsPayload
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			t.Fatal(err)
		}
		if p.ConversationID != conv {
			t.Fatal("foreign initial catch-up")
		}
		for _, item := range p.Items {
			r.items[item.ID] = item
		}
		if p.Final {
			r.epoch, r.version, r.checkpoint = p.Epoch, p.Version, p.Version
			r.since = time.Now().UTC()
			return r
		}
	}
}

// QueuePlacementFrames observes explicit catch-up followed by canonical updates.
func QueuePlacementFrames(t *testing.T, w Wire, conv string) func(time.Time) (QueuePlacementFrame, bool) {
	return newQueuePlacementReader(t, w, conv).next
}

func (r *queuePlacementReader) next(deadline time.Time) (QueuePlacementFrame, bool) {
	r.t.Helper()
	if r.received == nil {
		r.received = maps.Clone(r.items)
	}
	for {
		if r.w.Source != nil {
			if r.completed == nil {
				r.completed = map[string]bool{}
			}
			for _, e := range r.w.Source(r.conv) {
				if e.ID <= r.sourceID {
					continue
				}
				r.sourceID = e.ID
				if e.Type != protocol.TypeTurnEnd {
					continue
				}
				var p struct {
					ConversationID  string `json:"conversation_id"`
					TurnID          string `json:"turn_id"`
					ParentToolUseID string `json:"parent_tool_use_id"`
				}
				if json.Unmarshal(e.Payload, &p) != nil {
					r.t.Fatal("invalid queued terminal source")
				}
				if p.ConversationID != r.conv || p.ParentToolUseID != "" {
					continue
				}
				if e.Session == nil || p.TurnID == "" {
					r.t.Fatal("queued terminal source lost identity")
				}
				key := e.Session.Kind + "/" + e.Session.SessionID + "/" + p.TurnID
				if !r.completed[key] {
					r.ends = append(r.ends, e)
					r.completed[key] = true
				}
			}
		}
		if r.checkpointReady() && r.checkpoint < r.ends[0].ID && r.request == 0 && !time.Now().Before(r.retryAfter) {
			r.requested = r.version
			r.request = r.w.Send(protocol.TypeThreadCatchUp, protocol.ThreadCatchUpPayload{ConversationID: r.conv, Epoch: r.epoch, Version: r.requested, Limit: 100})
		}
		boundary := len(r.ends) > 0 && (len(r.pending) == 0 || (r.pending[0].Order != 0 && r.pending[0].Order > r.ends[0].ID))
		for len(r.ends) > 0 && len(r.idles) > 0 && r.idles[0] != "" && r.idles[0] != r.ends[0].Session.SessionID {
			r.idles = r.idles[1:]
		}
		if boundary && r.checkpoint >= r.ends[0].ID && len(r.idles) > 0 {
			r.ends = r.ends[1:]
			r.idles = r.idles[1:]
			r.completionBatch = max(1, r.completionBatch-1)
			return QueuePlacementFrame{Kind: "turn_end"}, true
		}
		if len(r.pending) > 0 && !boundary {
			item := r.pending[0]
			r.pending = r.pending[1:]
			old := r.items[item.ID]
			if old.Rev >= item.Rev {
				continue
			}
			r.items[item.ID] = item
			var content struct {
				Text      string `json:"text"`
				Name      string `json:"name"`
				ToolUseID string `json:"tool_use_id"`
				QueuedID  uint64 `json:"queued_msg_id"`
				SentNow   bool   `json:"sent_now"`
				MessageID string `json:"message_id"`
			}
			if err := json.Unmarshal(item.Content, &content); err != nil {
				r.t.Fatalf("decode queued item content: kind=%s item=%d rev=%d: %v", item.Kind, item.ID, item.Rev, err)
			}
			f := QueuePlacementFrame{Text: content.Text, ToolName: content.Name, ToolUseID: content.ToolUseID, QueuedID: content.QueuedID, ItemID: item.ID, Status: item.Status, SentNow: content.SentNow, MessageID: content.MessageID}
			f.Active, f.Shown, f.Order = item.Active, item.Shown, item.Order
			if item.Parent != 0 {
				continue
			}
			switch item.Kind {
			case "user_message":
				if item.Status == "queued" && item.Active && item.Order == 0 && content.QueuedID != 0 {
					f.Kind = "queued_item"
				}
				if item.Status == "delivered" && old.Status != "delivered" && !item.Active && item.Order != 0 {
					f.Kind = "user_message"
				}
			case "tool_call":
				if old.ID == 0 {
					f.Kind = "tool_use"
				} else if old.Active && !item.Active {
					f.Kind = "tool_result"
				}
			case "assistant_message":
				var before struct {
					Text string `json:"text"`
				}
				_ = json.Unmarshal(old.Content, &before)
				f.Text = strings.TrimPrefix(content.Text, before.Text)
				if strings.TrimSpace(f.Text) != "" {
					f.Kind = "delta"
				}
			case "turn_end":
				if old.ID == 0 && r.w.Source == nil {
					f.Kind = "turn_end"
				}
			}
			if f.Kind != "" || item.Kind == "user_message" {
				return f, true
			}
			continue
		}
		readDeadline := deadline
		if r.w.Source != nil {
			readDeadline = pollDeadline(deadline)
		}
		env := r.w.Next(readDeadline)
		if env.Type == "" {
			if r.w.Source != nil && time.Now().Before(deadline) {
				continue
			}
			if len(r.ends) > 0 {
				r.t.Logf("queue completion wait: version=%d checkpoint=%d request=%d terminal=%d pending=%d idles=%d", r.version, r.checkpoint, r.request, r.ends[0].ID, len(r.pending), len(r.idles))
			}
			return QueuePlacementFrame{}, false
		}
		decode := func(v any) {
			if err := json.Unmarshal(env.Payload, v); err != nil {
				r.t.Fatalf("decode queue envelope: type=%s: %v", env.Type, err)
			}
		}
		switch env.Type {
		case protocol.TypeError:
			var p protocol.ErrorPayload
			decode(&p)
			if r.request != 0 && env.InReplyTo != nil && *env.InReplyTo == r.request && p.Code == "thread.query_failed" && p.Retryable {
				r.request = 0
				r.retryAfter = time.Now().Add(20 * time.Millisecond)
				continue
			}
			r.t.Fatalf("queue observation received error: %s", p.Code)
		case "queue_state":
			r.t.Fatal("retired queue snapshot reached the app")
		case protocol.TypeThreadItems:
			var p protocol.ThreadItemsPayload
			decode(&p)
			if p.ConversationID == r.conv {
				if p.Epoch != r.epoch {
					r.t.Fatal("thread checkpoint changed epoch")
				}
				r.version = max(r.version, p.Version)
				for _, item := range p.Items {
					r.receive(item)
				}
				if p.Final && env.InReplyTo != nil && *env.InReplyTo == r.request {
					if p.FromVersion != nil && *p.FromVersion != r.requested {
						r.t.Fatal("thread checkpoint changed requested version")
					}
					r.checkpoint = max(r.checkpoint, p.Version)
					r.request = 0
				}
			}
		case protocol.TypeThreadItemAdded:
			var p protocol.ThreadItemAddedPayload
			decode(&p)
			if p.ConversationID == r.conv {
				r.version = max(r.version, p.Version)
				r.receive(p.Item)
			}
		case protocol.TypeThreadItemChanged:
			var p protocol.ThreadItemChangedPayload
			decode(&p)
			if p.ConversationID != r.conv {
				continue
			}
			r.version = max(r.version, p.Version)
			item := r.received[p.ItemID]
			if item.Rev >= p.Rev {
				continue // a full catch-up may already include this revision
			}
			if item.Rev != p.BaseRev {
				r.t.Fatalf("thread patch revision gap: item=%d have=%d base=%d rev=%d", p.ItemID, item.Rev, p.BaseRev, p.Rev)
			}
			raw, err := json.Marshal(item)
			if err != nil {
				r.t.Fatal(err)
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(raw, &fields); err != nil {
				r.t.Fatal(err)
			}
			for k, v := range p.Changes {
				fields[k] = v
			}
			raw, err = json.Marshal(fields)
			if err != nil {
				r.t.Fatal(err)
			}
			var updated protocol.ThreadItem
			if err := json.Unmarshal(raw, &updated); err != nil {
				r.t.Fatal(err)
			}
			updated.Rev = p.Rev
			r.receive(updated)
		case protocol.TypeThreadTextAppend:
			var p protocol.ThreadTextAppendPayload
			decode(&p)
			if p.ConversationID != r.conv {
				continue
			}
			r.version = max(r.version, p.Version)
			item := r.received[p.ItemID]
			if item.Rev >= p.Rev {
				continue // a full catch-up may already include this revision
			}
			if item.Rev != p.BaseRev {
				r.t.Fatalf("thread append revision gap: item=%d have=%d base=%d rev=%d", p.ItemID, item.Rev, p.BaseRev, p.Rev)
			}
			var content map[string]json.RawMessage
			if err := json.Unmarshal(item.Content, &content); err != nil {
				r.t.Fatalf("decode append content: kind=%s item=%d rev=%d: %v", item.Kind, item.ID, item.Rev, err)
			}
			var text string
			if err := json.Unmarshal(content["text"], &text); err != nil {
				r.t.Fatalf("decode append text: kind=%s item=%d rev=%d: %v", item.Kind, item.ID, item.Rev, err)
			}
			content["text"], _ = json.Marshal(text + p.Text)
			item.Content, _ = json.Marshal(content)
			item.Rev = p.Rev
			r.receive(item)
		default:
			f := ClassifyQueuePlacementFrame(r.t, env, r.conv)
			if env.Type == protocol.TypeAck && env.InReplyTo != nil {
				f.Kind, f.RequestID = "ack", *env.InReplyTo
			}
			if env.Type == protocol.TypeTurnState {
				var p protocol.TurnStatePayload
				decode(&p)
				if p.ConversationID == r.conv && !env.SessionStateCleared && !env.TS.Before(r.since) {
					var session string
					if len(env.SessionID) > 0 && json.Unmarshal(env.SessionID, &session) != nil {
						r.t.Fatal("invalid queue live-state session")
					}
					if r.running == nil {
						r.running = map[string]bool{}
					}
					if p.State != "idle" {
						r.running[session] = true
						f.Kind = "reply_state"
					} else {
						// Supplied state is captured before the history append; its
						// receive sequence, not the append timestamp, closes a turn.
						if r.running[session] {
							r.idles = append(r.idles, session)
							delete(r.running, session)
						}
						f.Kind = "idle"
					}
				}
			}
			return f, true
		}
	}
}

// receive advances canonical state immediately. Reporting placement may wait for
// an earlier turn's certified completion without delaying patch application.
func (r *queuePlacementReader) receive(item protocol.ThreadItem) {
	if r.received[item.ID].Rev >= item.Rev {
		return
	}
	r.received[item.ID] = item
	r.pending = append(r.pending, item)
}

// checkpointReady keeps completion queries out of a known successor's live
// publication. Each source end needs its own observed running-to-idle boundary.
func (r *queuePlacementReader) checkpointReady() bool {
	batch := max(1, r.completionBatch)
	if len(r.ends) < batch || len(r.running) != 0 {
		return false
	}
	idle := 0
	for _, end := range r.ends[:batch] {
		for idle < len(r.idles) && r.idles[idle] != "" && r.idles[idle] != end.Session.SessionID {
			idle++
		}
		if idle == len(r.idles) {
			return false
		}
		idle++
	}
	return true
}
