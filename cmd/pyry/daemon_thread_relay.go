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
	Changes(context.Context, conversations.ConversationID, string, uint64) (thread.Observation, error)
	CompactChanges(context.Context, conversations.ConversationID, string, uint64) (thread.CompactQueryResult, error)
}

// Publication is serialized by the conversation coordinator, never a writer.
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
	cfg.ThreadToolBody = nil
	cfg.ThreadChildren = nil
	cfg.ThreadCompactCatchUp = nil
	cfg.ThreadCompactPage = nil
	cfg.ThreadCompactReady = nil
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
	cfg.ThreadToolBody = func(ctx context.Context, r protocol.ThreadExpansionRequest) (thread.ToolBodyResult, error) {
		return queryDaemonThread(ctx, s, conversations.ConversationID(r.ConversationID), store, func(ctx context.Context) (thread.ToolBodyResult, error) {
			return store.ToolBody(ctx, conversations.ConversationID(r.ConversationID), r.Epoch, r.ItemID, r.ExpectedRev)
		})
	}
	cfg.ThreadChildren = func(ctx context.Context, r protocol.ThreadExpansionRequest) (thread.ChildPageResult, error) {
		var cursor *thread.ChildCursor
		if c := r.Cursor; c != nil {
			cursor = &thread.ChildCursor{ConversationID: conversations.ConversationID(c.ConversationID), Epoch: c.Epoch, Parent: c.ItemID, ParentRev: c.Rev, Version: c.Version, Order: c.Order, ID: c.ID}
		}
		return queryDaemonThread(ctx, s, conversations.ConversationID(r.ConversationID), store, func(ctx context.Context) (thread.ChildPageResult, error) {
			return store.ChildPage(ctx, conversations.ConversationID(r.ConversationID), r.Epoch, r.ItemID, r.ExpectedRev, r.Limit, cursor)
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
	cfg.ThreadCompactCatchUp = func(ctx context.Context, r protocol.ThreadCatchUpPayload) (thread.CompactQueryResult, error) {
		return queryDaemonThread(ctx, s, conversations.ConversationID(r.ConversationID), store, func(ctx context.Context) (thread.CompactQueryResult, error) {
			return store.CompactCatchUp(ctx, conversations.ConversationID(r.ConversationID), r.Epoch, r.Version, r.Limit)
		})
	}
	cfg.ThreadCompactPage = func(ctx context.Context, r protocol.ThreadPagePayload) (thread.CompactQueryResult, error) {
		return queryDaemonThread(ctx, s, conversations.ConversationID(r.ConversationID), store, func(ctx context.Context) (thread.CompactQueryResult, error) {
			return store.CompactHistoryPage(ctx, conversations.ConversationID(r.ConversationID), r.BeforeOrder, r.Limit)
		})
	}
	// Compact needs the live publisher and both expansion reads: a header is only
	// useful when the client can open it.
	cfg.ThreadCompactReady = func() bool {
		return cfg.ThreadReady != nil && cfg.ThreadToolBody != nil && cfg.ThreadChildren != nil && cfg.ThreadCompactCatchUp != nil && cfg.ThreadCompactPage != nil && cfg.ThreadReady()
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

func (s *threadShadow) publish(ctx context.Context, id conversations.ConversationID, w *shadowConversation) bool {
	return s.publishThread(ctx, id, w, false)
}

func (s *threadShadow) publishThread(ctx context.Context, id conversations.ConversationID, w *shadowConversation, initialOnly bool) bool {
	if !w.lockPublication(ctx) {
		return false
	}
	defer w.unlockPublication()
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
		return true
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
	if initialOnly && w.threadEpoch != "" {
		return true
	}
	p.publish(operation, id, w)
	return w.threadEpoch != ""
}

func (p *daemonThreadPublisher) gap(ctx context.Context, id conversations.ConversationID, w *shadowConversation) {
	// Queries can initialize a consumer without any successful live push.
	peers := w.threadPeers
	if peers == nil {
		peers = make(map[string]bool)
	}
	for _, peer := range p.active(ctx, string(id)) {
		if peer.Interactive && peer.Thread {
			peers[peer.ConnID] = true
		}
	}
	for peer := range peers {
		if err := p.retire(ctx, peer); err != nil && ctx.Err() == nil {
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
			p.gap(ctx, id, w)
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
			p.gap(ctx, id, w)
			o, err = p.store.Changes(ctx, id, "", 0)
			if err != nil || o.State != thread.StateUsable {
				return
			}
		}
	} else {
		var err error
		o, err = p.store.Changes(ctx, id, "", 0)
		if err != nil || o.State != thread.StateUsable {
			return
		}
	}
	if ctx.Err() != nil {
		return
	}
	// Recipient selection follows observation capture. Reconnect history belongs
	// to catch-up; this publisher does not initialize peers admitted later.
	// Both the completed and pending projections need their cancellation frames.
	// Admission after this snapshot captures only after acquiring this publication
	// gate, so its result includes this pass. Delivery rechecks the current join.
	var completePeers, compactPeers []string
	for _, peer := range p.active(ctx, string(id)) {
		if !peer.Interactive || !peer.Thread {
			continue
		}
		if peer.View != relay.ThreadViewCompact || peer.PendingView == relay.ThreadViewComplete {
			completePeers = append(completePeers, peer.ConnID)
		}
		if peer.View == relay.ThreadViewCompact || peer.PendingView == relay.ThreadViewCompact || peer.View == relay.ThreadViewUnjoined && peer.ThreadCompact {
			compactPeers = append(compactPeers, peer.ConnID)
		}
	}
	previousPeers := w.threadPeers
	w.threadPeers = make(map[string]bool)
	for _, peer := range append(completePeers, compactPeers...) {
		if previousPeers[peer] {
			w.threadPeers[peer] = true
		}
	}
	var updates []any
	if w.threadEpoch != "" {
		if len(completePeers) > 0 {
			for _, change := range o.Changes {
				updates = append(updates, daemonThreadUpdates(string(id), o.Epoch, change)...)
			}
		}
		if !p.deliver(ctx, id, w, updates, completePeers) {
			return
		}
		updates = nil
		if len(compactPeers) > 0 && o.Epoch == w.threadEpoch && o.Version > w.threadVersion {
			// The Store coalesces every hidden event since the cursor into one
			// final row per affected root, so this pass is bounded by visible roots.
			r, err := p.store.CompactChanges(ctx, id, w.threadEpoch, w.threadVersion)
			if err != nil || ctx.Err() != nil {
				return
			}
			if r.State != thread.StateUsable || r.ResetRequired {
				p.gap(ctx, id, w)
				return
			}
			updates = daemonThreadCompactUpdates(string(id), w.threadVersion, r)
		}
		if !p.deliver(ctx, id, w, updates, compactPeers) {
			return
		}
	}
	if ctx.Err() == nil {
		w.threadEpoch = o.Epoch
		w.threadVersion = o.Version
	}
}

// deliver encodes each update once and pushes it to every selected peer. A
// failed push skips only that peer; false means the pass must stop.
func (p *daemonThreadPublisher) deliver(ctx context.Context, id conversations.ConversationID, w *shadowConversation, updates []any, peers []string) bool {
	for _, update := range updates {
		frames, err := protocol.EncodeThreadUpdate(protocol.Envelope{TS: time.Now().UTC()}, update)
		if err != nil {
			p.log.Warn("relay: thread encoding failed", "event", "daemon.thread.encode_err")
			p.gap(ctx, id, w)
			return false
		}
		for _, raw := range frames {
			var e protocol.Envelope
			if err := json.Unmarshal(raw, &e); err != nil {
				p.gap(ctx, id, w)
				return false
			}
			e.ID = p.nextID.Add(1)
			for _, peer := range peers {
				if ctx.Err() != nil {
					return false
				}
				if err := p.push(ctx, peer, e); err == nil {
					w.threadPeers[peer] = true
				} else if ctx.Err() == nil {
					p.log.Debug("relay: thread push failed", "event", "daemon.thread.push_err")
				}
			}
		}
	}
	return ctx.Err() == nil
}

// daemonThreadCompactUpdates turns one coalesced interval into one frame per
// affected root, or one progress frame when no root changed.
func daemonThreadCompactUpdates(conv string, from uint64, r thread.CompactQueryResult) []any {
	if from >= r.Version {
		return nil
	}
	scope := protocol.ThreadCompactUpdatePayload{Projection: protocol.ThreadProjectionCompact, ConversationID: conv, Epoch: r.Epoch, FromVersion: from, Version: r.Version}
	var updates []any
	for _, i := range r.Items {
		u, item := scope, daemonThreadItem(i)
		u.Item = &item
		updates = append(updates, u)
	}
	for _, h := range r.Headers {
		u, header := scope, daemonThreadSummary(h)
		u.Header = &header
		updates = append(updates, u)
	}
	if len(updates) == 0 {
		scope.Progress = true
		updates = append(updates, scope)
	}
	return updates
}

func daemonThreadItem(i thread.Item) protocol.ThreadItem {
	return protocol.ThreadItem{ID: i.ID, Kind: i.Kind, Order: i.Order, Rev: i.Rev, EndedOrder: i.EndedOrder, Session: i.Session, Agent: i.Agent, NoChild: i.NoChild, Turn: i.Turn, Parent: i.Parent, Status: i.Status, Active: i.Active, Shown: i.Shown, Summary: i.Summary, Subtype: i.Subtype, Content: i.Content}
}

// daemonThreadSummary copies every header fact, including unknown (nil) ones.
func daemonThreadSummary(h thread.ChildSummary) protocol.ThreadSummary {
	return protocol.ThreadSummary{ID: h.ID, Kind: h.Kind, Order: h.Order, Rev: h.Rev, EndedOrder: h.EndedOrder, Session: h.Session, Agent: h.Agent, NoChild: h.NoChild, Turn: h.Turn, Parent: h.Parent, Status: h.Status, Active: h.Active, Shown: h.Shown, Headline: h.Headline, Subtype: h.Subtype, DetailOmitted: true,
		ErrorSummary: h.ErrorSummary, PublicChildCount: h.PublicChildCount, HasPublicChildren: h.HasPublicChildren, SubtreeVersion: h.SubtreeVersion}
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
	if len(fields) > 0 && c.TextAppend != "" {
		fields["content"] = c.ResultContent
	}
	if len(fields) > 0 || c.TextAppend == "" {
		return []any{protocol.ThreadItemChangedPayload{ConversationID: conv, Epoch: epoch, Version: c.Version, ItemID: c.ID, BaseRev: c.PreviousRev, Rev: c.Rev, Changes: fields}}
	}
	return []any{protocol.ThreadTextAppendPayload{ConversationID: conv, Epoch: epoch, Version: c.Version, ItemID: c.ID, BaseRev: c.PreviousRev, Rev: c.Rev, Text: c.TextAppend}}
}
