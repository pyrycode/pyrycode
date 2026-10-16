package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/relay"
	"github.com/pyrycode/pyrycode/internal/thread"
)

type daemonThreadStore interface {
	Status(conversations.ConversationID) thread.Snapshot
	Observe(conversations.ConversationID) thread.Observation
	Changes(context.Context, conversations.ConversationID, string, uint64) (thread.Observation, error)
}

// Publication is owned by the existing conversation coordinator, never a writer.
type daemonThreadPublisher struct {
	store   daemonThreadStore
	active  func(context.Context, string) []relay.ActiveConn
	push    func(context.Context, string, protocol.Envelope) error
	retire  func(context.Context, string) error
	log     *slog.Logger
	ctx     context.Context
	nextID  atomic.Uint64
	mu      sync.Mutex
	stopped bool
	wg      sync.WaitGroup
}

func installDaemonThreadRelay(cfg *relay.V2SessionConfig, s *threadShadow) {
	cfg.ThreadReady = nil
	cfg.ThreadLastShownVersion = nil
	cfg.ThreadCatchUp = nil
	cfg.ThreadPage = nil
	if s == nil || s.registry == nil {
		return
	}
	store, ok := s.store.(*thread.Store)
	if !ok || store == nil {
		return
	}
	cfg.ThreadLastShownVersion = func(id string) (uint64, bool) {
		if _, ok := s.registry.Get(conversations.ConversationID(id)); !ok {
			return 0, false
		}
		o := store.Status(conversations.ConversationID(id))
		if o.State == thread.StateNotLoaded {
			return store.UnloadedLastShownVersion(conversations.ConversationID(id))
		}
		return o.LastShownVersion, o.State == thread.StateUsable
	}
	cfg.ThreadCatchUp = func(ctx context.Context, r protocol.ThreadCatchUpPayload) (thread.QueryResult, error) {
		return s.queryThread(ctx, conversations.ConversationID(r.ConversationID), store, func(ctx context.Context) (thread.QueryResult, error) {
			return store.CatchUp(ctx, conversations.ConversationID(r.ConversationID), r.Epoch, r.Version, r.Limit)
		})
	}
	cfg.ThreadPage = func(ctx context.Context, r protocol.ThreadPagePayload) (thread.QueryResult, error) {
		return s.queryThread(ctx, conversations.ConversationID(r.ConversationID), store, func(ctx context.Context) (thread.QueryResult, error) {
			return store.HistoryPage(ctx, conversations.ConversationID(r.ConversationID), r.BeforeOrder, r.Limit)
		})
	}
	cfg.ThreadReady = func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		p := s.publisher
		if cfg.ThreadCatchUp == nil || cfg.ThreadPage == nil || cfg.ThreadLiveState == nil || cfg.LiveStateCurrent == nil || cfg.ThreadLastShownVersion == nil || p == nil || p.store != store {
			return false
		}
		p.mu.Lock()
		defer p.mu.Unlock()
		return !p.stopped && p.ctx.Err() == nil
	}
}

func startDaemonThreadPublisher(base context.Context, s *threadShadow, m *relay.V2SessionManager) func() {
	if s == nil {
		return func() {}
	}
	store, ok := s.store.(*thread.Store)
	if !ok || store == nil {
		return func() {}
	}
	ctx, cancel := context.WithCancel(base)
	p := &daemonThreadPublisher{store: store, active: m.ActiveThreadConns, push: m.Push, retire: m.RetireLiveState, log: s.log, ctx: ctx}
	s.mu.Lock()
	if s.publisher != nil {
		s.mu.Unlock()
		cancel()
		return func() {}
	}
	s.publisher = p
	s.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			p.mu.Lock()
			p.stopped = true
			cancel()
			p.mu.Unlock()
			p.wg.Wait()
			s.mu.Lock()
			if s.publisher == p {
				s.publisher = nil
			}
			s.mu.Unlock()
		})
	}
}

func (s *threadShadow) publish(ctx context.Context, id conversations.ConversationID, w *shadowConversation) {
	s.mu.Lock()
	p := s.publisher
	if p != nil {
		p.mu.Lock()
		if p.stopped {
			p = nil
		} else {
			p.wg.Add(1)
		}
		s.publisher.mu.Unlock()
	}
	s.mu.Unlock()
	if p == nil {
		return
	}
	defer p.wg.Done()
	operation, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(p.ctx, cancel)
	defer stop()
	defer cancel()
	if w.publisher != p {
		w.publisher = p
		w.threadEpoch = ""
		w.threadPeers = nil
	}
	p.publish(operation, id, w)
}

func (p *daemonThreadPublisher) gap(ctx context.Context, w *shadowConversation) {
	for id := range w.threadPeers {
		if err := p.retire(ctx, id); err != nil && ctx.Err() == nil {
			p.log.Debug("relay: thread retirement failed", "event", "daemon.thread.retire_err")
		}
	}
	w.threadEpoch = ""
	w.threadVersion = 0
	w.threadPeers = nil
}

func (p *daemonThreadPublisher) publish(ctx context.Context, id conversations.ConversationID, w *shadowConversation) {
	status := p.store.Status(id)
	if status.State != thread.StateUsable {
		if w.threadEpoch != "" {
			p.gap(ctx, w)
		}
		return
	}
	var o thread.Observation
	if w.threadEpoch != "" {
		if status.Epoch == w.threadEpoch && status.Version == w.threadVersion {
			return
		}
		var err error
		o, err = p.store.Changes(ctx, id, w.threadEpoch, w.threadVersion)
		if err != nil {
			return
		}
		if o.State != thread.StateUsable || o.BaselineRequired {
			p.gap(ctx, w)
			o = p.store.Observe(id)
			if o.State != thread.StateUsable {
				return
			}
		}
	} else {
		o = p.store.Observe(id)
		if o.State != thread.StateUsable {
			return
		}
	}
	if ctx.Err() != nil {
		return
	}
	// Recipient selection follows observation capture. Reconnect history belongs
	// to catch-up; this publisher does not initialize peers admitted later.
	peers := p.active(ctx, string(id))
	previousPeers := w.threadPeers
	w.threadPeers = make(map[string]bool)
	for _, peer := range peers {
		if previousPeers[peer.ConnID] {
			w.threadPeers[peer.ConnID] = true
		}
	}
	var changes []thread.Change
	if w.threadEpoch == "" {
		for i := range o.Items {
			item := o.Items[i]
			changes = append(changes, thread.Change{Addition: &item, Version: o.Version})
		}
	} else {
		changes = o.Changes
	}
	for _, change := range changes {
		for _, update := range daemonThreadUpdates(string(id), o.Epoch, change) {
			frames, err := protocol.EncodeThreadUpdate(protocol.Envelope{TS: time.Now().UTC()}, update)
			if err != nil {
				p.log.Warn("relay: thread encoding failed", "event", "daemon.thread.encode_err")
				p.gap(ctx, w)
				return
			}
			for _, raw := range frames {
				var e protocol.Envelope
				if err := json.Unmarshal(raw, &e); err != nil {
					p.gap(ctx, w)
					return
				}
				e.ID = p.nextID.Add(1)
				for _, peer := range peers {
					if ctx.Err() != nil {
						return
					}
					if !peer.Interactive || !peer.Thread {
						continue
					}
					if err := p.push(ctx, peer.ConnID, e); err == nil {
						w.threadPeers[peer.ConnID] = true
					} else if ctx.Err() == nil {
						p.log.Debug("relay: thread push failed", "event", "daemon.thread.push_err")
					}
				}
			}
		}
	}
	if ctx.Err() == nil {
		w.threadEpoch = o.Epoch
		w.threadVersion = o.Version
	}
}

func daemonThreadItem(i thread.Item) protocol.ThreadItem {
	return protocol.ThreadItem{ID: i.ID, Kind: i.Kind, Order: i.Order, Rev: i.Rev, EndedOrder: i.EndedOrder, Session: i.Session, Agent: i.Agent, NoChild: i.NoChild, Turn: i.Turn, Parent: i.Parent, Status: i.Status, Active: i.Active, Shown: i.Shown, Summary: i.Summary, Subtype: i.Subtype, Content: i.Content}
}

func daemonThreadUpdates(conv, epoch string, c thread.Change) []any {
	if c.Addition != nil {
		return []any{protocol.ThreadItemAddedPayload{ConversationID: conv, Epoch: epoch, Version: c.Version, Item: daemonThreadItem(*c.Addition)}}
	}
	fields := make(map[string]json.RawMessage, len(c.Fields))
	dto := reflect.TypeOf(protocol.ThreadItem{})
	for name, value := range c.Fields {
		if f, ok := dto.FieldByName(name); ok {
			fields[strings.Split(f.Tag.Get("json"), ",")[0]] = value
		}
	}
	var updates []any
	base := c.PreviousRev
	if len(fields) > 0 || c.TextAppend == "" {
		updates = append(updates, protocol.ThreadItemChangedPayload{ConversationID: conv, Epoch: epoch, Version: c.Version, ItemID: c.ID, BaseRev: base, Rev: c.Rev, Changes: fields})
		base = c.Rev
	}
	if c.TextAppend != "" {
		updates = append(updates, protocol.ThreadTextAppendPayload{ConversationID: conv, Epoch: epoch, Version: c.Version, ItemID: c.ID, BaseRev: base, Rev: c.Rev, Text: c.TextAppend})
	}
	return updates
}
