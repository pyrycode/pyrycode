package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/pyrycode/pyrycode/internal/control"
	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/msgqueue"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// channelDeliveryHistory must be the composition root's existing history store.
type channelDeliveryHistory interface {
	Append(conversations.ConversationID, string, json.RawMessage, time.Time) (uint64, error)
	Page(conversations.ConversationID, string, int) (history.Page, error)
}
type channelDeliveryPost struct {
	ConversationID conversations.ConversationID `json:"conversation_id"`
	TurnID         string                       `json:"turn_id"`
	Text           string                       `json:"text"`
	TS             time.Time                    `json:"ts"`
	delivered      bool                         // cleanup can fail after delivery; never repeat its announcement
}

// channelDelivery is private client-delivery state, independent of channelCarry.
// mu serializes snapshot renames with acceptance and the sole consumer. No user
// turn is written while holding it; beforeInbound only checks pending state.
type channelDelivery struct {
	mu       sync.Mutex
	path     string
	posts    []channelDeliveryPost
	hist     channelDeliveryHistory
	carry    func(conversations.ConversationID, string)
	announce func(protocol.AssistantDeltaPayload)
	log      *slog.Logger
	save     func([]channelDeliveryPost) error
	wake     chan struct{}
}

func newChannelDelivery(path string, hist channelDeliveryHistory, carry func(conversations.ConversationID, string), log *slog.Logger) (*channelDelivery, error) {
	d := &channelDelivery{path: path, hist: hist, carry: carry, log: log, wake: make(chan struct{}, 1)}
	d.save = d.persist
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if errors.Is(err, os.ErrNotExist) {
		return d, nil
	}
	if err != nil {
		return nil, errors.New("channel delivery load failed")
	}
	defer f.Close()
	if err := json.NewDecoder(f).Decode(&d.posts); err != nil {
		return nil, errors.New("channel delivery load failed")
	}
	for _, p := range d.posts {
		if !conversations.ValidID(string(p.ConversationID)) || !conversations.ValidID(p.TurnID) || len(p.Text) == 0 || len(p.Text) > control.MaxChannelPostBytes || p.TS.IsZero() {
			return nil, errors.New("channel delivery state invalid")
		}
	}
	return d, nil
}

func (d *channelDelivery) persist(posts []channelDeliveryPost) error {
	if err := os.MkdirAll(filepath.Dir(d.path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(d.path), ".channel-delivery-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name()) // best-effort removal of uncommitted temporary data
	if err := json.NewEncoder(f).Encode(posts); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), d.path)
}

func (d *channelDelivery) accept(id conversations.ConversationID, turnID, text string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	candidate := append(append([]channelDeliveryPost(nil), d.posts...), channelDeliveryPost{ConversationID: id, TurnID: turnID, Text: text, TS: time.Now().UTC()})
	if err := d.save(candidate); err != nil {
		d.log.Warn("control: channel.post acceptance failed", "event", "channel_post.accept_err", "conversation_id", string(id))
		return errors.New(msgChannelPostRecordFailed)
	}
	d.posts = candidate
	// Only new acceptance records carry. Reload and client retries never do.
	if d.carry != nil {
		d.carry(id, text)
	}
	select {
	case d.wake <- struct{}{}:
	default:
	}
	return nil
}

func (d *channelDelivery) run(ctx context.Context) {
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		d.drain()
		select {
		case <-ctx.Done():
			return
		case <-d.wake:
		case <-tick.C:
		}
	}
}

func (d *channelDelivery) drain() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.posts) == 0 {
		return
	}
	failed := make(map[conversations.ConversationID]bool)
	pending := make([]channelDeliveryPost, 0, len(d.posts))
	for i := range d.posts {
		p := &d.posts[i]
		if !p.delivered && !failed[p.ConversationID] {
			if err := d.deliver(*p); err != nil {
				failed[p.ConversationID] = true
				d.log.Warn("control: channel.post delivery pending", "event", "channel_post.delivery_retry", "conversation_id", string(p.ConversationID))
			} else {
				p.delivered = true
			}
		}
		if !p.delivered {
			pending = append(pending, *p)
		}
	}
	if len(pending) == len(d.posts) {
		return
	}
	if err := d.save(pending); err != nil {
		d.log.Warn("control: channel.post cleanup pending", "event", "channel_post.cleanup_retry")
		return
	}
	d.posts = pending
}

// deliver reconciles every history page: unrelated newer entries may hide a
// recorded prefix. A complete record on entry means cleanup only, even when the
// process died before its first live push. History is the recovery guarantee.
func (d *channelDelivery) deliver(post channelDeliveryPost) error {
	chunks := splitDeltaText(post.Text, maxDeltaTextBytes)
	payloads := make([]protocol.AssistantDeltaPayload, len(chunks))
	for i, text := range chunks {
		payloads[i] = protocol.AssistantDeltaPayload{ConversationID: string(post.ConversationID), TurnID: post.TurnID, Seq: i, Text: text}
	}
	recorded := make([]bool, len(chunks))
	cursor := ""
	for {
		page, err := d.hist.Page(post.ConversationID, cursor, 128)
		if err != nil {
			return err
		}
		for _, e := range page.Entries {
			if e.Type != protocol.TypeAssistantDelta {
				continue
			}
			var p protocol.AssistantDeltaPayload
			if json.Unmarshal(e.Payload, &p) == nil && p.Seq >= 0 && p.Seq < len(payloads) && p == payloads[p.Seq] {
				recorded[p.Seq] = true
			}
		}
		if page.AtStart {
			break
		}
		cursor = page.Cursor
	}
	appended := false
	for i, p := range payloads {
		if recorded[i] {
			continue
		}
		raw, err := json.Marshal(p)
		if err != nil {
			return err
		}
		if _, err := d.hist.Append(post.ConversationID, protocol.TypeAssistantDelta, raw, post.TS); err != nil {
			return err
		}
		appended = true
	}
	if appended && d.announce != nil {
		for _, p := range payloads {
			d.announce(p)
		}
	}
	return nil
}

// beforeInbound establishes recovered-post precedence before carry composition
// and user-turn writing. It never waits on work for a different conversation.
func (d *channelDelivery) beforeInbound(deliver msgqueue.DeliverFunc) msgqueue.DeliverFunc {
	return func(ctx context.Context, id string, payload []byte) error {
		tick := time.NewTicker(20 * time.Millisecond)
		defer tick.Stop()
		for {
			if err := ctx.Err(); err != nil {
				return err
			}
			d.mu.Lock()
			pending := false
			for _, p := range d.posts {
				if string(p.ConversationID) == id && !p.delivered {
					pending = true
					break
				}
			}
			d.mu.Unlock()
			if !pending {
				return deliver(ctx, id, payload)
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-tick.C:
			}
		}
	}
}
