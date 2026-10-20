package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"sync/atomic"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/relay"
	"github.com/pyrycode/pyrycode/internal/sessions"
)

func daemonRelayReading(r daemonLiveReading) relay.LiveState {
	r = detachLiveReading(r)
	return relay.LiveState{Envelope: r.Envelope, ConversationID: r.ConversationID, SessionGeneration: r.SessionGeneration, ReadingID: r.ReadingID, Revision: r.Revision, Answerable: r.Answerable, ModelListUnprojected: r.Envelope.Type == protocol.TypeModelList}
}

func daemonLiveEligible(r daemonLiveReading) bool {
	return r.Envelope.SessionStateCleared || (r.Envelope.Type != protocol.TypeModalShown && r.Envelope.Type != protocol.TypeQuestionShown) || (r.Answerable != nil && *r.Answerable)
}

func daemonRelaySnapshot(o *daemonLiveState) func() func() (relay.LiveState, bool) {
	return func() func() (relay.LiveState, bool) {
		cursor := &daemonLiveCursor{}
		if o != nil {
			o.mu.Lock()
			for _, c := range o.conversations {
				for _, r := range c.readings {
					if daemonLiveEligible(*r) {
						cursor.records = append(cursor.records, r)
					}
				}
			}
			o.mu.Unlock()
		}
		return func() (relay.LiveState, bool) { r, ok := cursor.Next(); return daemonRelayReading(r), ok }
	}
}

// Catalogs are resolved once per captured target, independently of retained
// session readings. The finite cursor never follows newly registered targets.
func daemonRelayCatalogSnapshot(o *daemonLiveState, reg *conversations.Registry, resolve func(string, bool) (daemonLiveReading, bool)) func() func() (relay.LiveState, bool) {
	retained := daemonRelaySnapshot(o)
	return func() func() (relay.LiveState, bool) {
		next := retained()
		targets := reg.List()
		index := 0
		return func() (relay.LiveState, bool) {
			for {
				r, ok := next()
				if !ok {
					break
				}
				if r.Envelope.Type != protocol.TypeModelList {
					return r, true
				}
			}
			for index < len(targets) {
				id := string(targets[index].ID)
				index++
				// Capture canonical inventory; relay projects for each recipient.
				if r, ok := resolve(id, true); ok {
					r.Envelope.InReplyTo = nil
					return daemonRelayReading(r), true
				}
			}
			return relay.LiveState{}, false
		}
	}
}

const daemonLivePublicationCap = 256

type daemonLivePublisher struct {
	pending  chan *daemonLiveReading
	wake     chan struct{}
	overflow atomic.Bool
}

// enqueue runs under the owner lock. Immutable records cross a bounded handoff;
// recipient lookup and delivery belong exclusively to the publisher worker.
func (p *daemonLivePublisher) enqueue(r *daemonLiveReading) {
	if p == nil || !daemonLiveEligible(*r) {
		return
	}
	select {
	case p.pending <- r:
	default:
		p.overflow.Store(true)
	}
	select {
	case p.wake <- struct{}{}:
	default:
	}
}

func startDaemonLivePublisher(base context.Context, o *daemonLiveState, active func(context.Context) []relay.ActiveConn, push func(context.Context, string, relay.LiveState) error, retire func(context.Context, string), log *slog.Logger) func() {
	if o == nil {
		return func() {}
	}
	ctx, cancel := context.WithCancel(base)
	p := &daemonLivePublisher{pending: make(chan *daemonLiveReading, daemonLivePublicationCap), wake: make(chan struct{}, 1)}
	o.mu.Lock()
	if o.publisher != nil {
		o.mu.Unlock()
		cancel()
		return func() {}
	}
	o.publisher = p
	o.mu.Unlock()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-ctx.Done():
				return
			case <-p.wake:
			}

			for {
				var r *daemonLiveReading
				select {
				case r = <-p.pending:
				default:
					r = nil
				}
				if r == nil && !p.overflow.Load() {
					break
				}
				// Select the reading before enumerating. A connection admitted after
				// this enumeration snapshots it or a subsequent queued replacement.
				conns := active(ctx)
				if ctx.Err() != nil {
					return
				}
				if p.overflow.Swap(false) {
					// A gap requires a new handshake and snapshot, matching relay queue overflow.
					for _, c := range conns {
						if c.Thread {
							retire(ctx, c.ConnID)
						}
					}
					log.Warn("relay: live publication handoff overflow", "event", "daemon.live.ceiling")
				}
				if r == nil {
					continue
				}
				reading := daemonRelayReading(*r)
				reading.Envelope.InReplyTo = nil
				if !daemonLiveCurrent(o, reading) {
					continue
				}
				for _, c := range conns {
					if !c.Interactive || !c.Thread {
						continue
					}
					if err := push(ctx, c.ConnID, reading); err != nil && ctx.Err() == nil {
						log.Debug("relay: live publication dropped", "event", "daemon.live.push_err", "type", reading.Envelope.Type)
					}
				}
				if ctx.Err() != nil {
					return
				}
			}
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() {
			cancel()
			<-done
			o.mu.Lock()
			if o.publisher == p {
				o.publisher = nil
			}
			o.mu.Unlock()
			for {
				select {
				case <-p.pending:
				default:
					return
				}
			}
		})
	}
}

func daemonModelListReadingFor(reg *conversations.Registry, pool *sessions.Pool, saved savedModelVocabulary, live *daemonLiveBindings) func(string, bool) (daemonLiveReading, bool) {
	result := modelListResultFor(reg, pool, saved, live)
	return func(conv string, multi bool) (daemonLiveReading, bool) { _, ok, r := result(conv, multi); return r, ok }
}
func daemonMCPStatusReadingFor(reg *conversations.Registry, pool *sessions.Pool, live *daemonLiveBindings) func(context.Context, string) (daemonLiveReading, bool) {
	if reg == nil || pool == nil {
		return nil
	}
	return func(ctx context.Context, conv string) (daemonLiveReading, bool) {
		_, ok, r := resolveBoundMCPStatusReading(ctx, reg, pool, conv, protocol.Envelope{}, live)
		return r, ok
	}
}

func installDaemonLiveRelay(cfg *relay.V2SessionConfig, w relayWiring, usage *contextUsageResolver) {
	if w.streamSink == nil || w.streamSink.live == nil {
		return
	}
	cfg.ThreadLiveState = daemonRelaySnapshot(w.streamSink.live)
	if w.convReg != nil && w.modelListReading != nil {
		cfg.ThreadLiveState = daemonRelayCatalogSnapshot(w.streamSink.live, w.convReg, w.modelListReading)
	}
	cfg.LiveStateCurrent = func(r relay.LiveState) bool { return daemonLiveCurrent(w.streamSink.live, r) }
	if usage != nil {
		cfg.ContextUsageReadingFor = func(ctx context.Context, conv string) (relay.LiveState, bool) {
			r, ok := usage.GetLive(ctx, conv, protocol.Envelope{})
			return daemonRelayReading(r), ok
		}
	}
	if w.modelListReading != nil {
		cfg.ModelListReadingFor = func(conv string, multi bool) (relay.LiveState, bool) {
			r, ok := w.modelListReading(conv, multi)
			return daemonRelayReading(r), ok
		}
	}
	if w.mcpStatusReading != nil {
		cfg.MCPStatusReadingFor = func(ctx context.Context, conv string) (relay.LiveState, bool) {
			r, ok := w.mcpStatusReading(ctx, conv)
			return daemonRelayReading(r), ok
		}
	}
	if result := runConfigResultFor(w.runSettings, snapshotUsageFor(w.sessionTranscriptDir, w.modelWindows)); result != nil {
		cfg.SessionSettingsReadingFor = func(conv string) (relay.LiveState, bool) {
			_, ok, r := result(conv)
			return daemonRelayReading(r), ok && r.Revision > 0
		}
	}
	if updater, ok := w.settings.(interface {
		UpdateLive(string, relay.SettingsUpdate, protocol.Envelope) (daemonLiveReading, error)
	}); ok {
		cfg.UpdateSettingsReading = func(id string, u relay.SettingsUpdate) (relay.LiveState, error) {
			r, err := updater.UpdateLive(id, u, protocol.Envelope{})
			return daemonRelayReading(r), err
		}
	}
}

func daemonLiveCurrent(o *daemonLiveState, r relay.LiveState) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	c := o.conversations[r.ConversationID]
	if c == nil || c.generation != r.SessionGeneration {
		return false
	}
	if r.Envelope.SessionStateCleared {
		return true
	}
	key := liveReadingKey{liveFamily(r.Envelope.Type), r.ReadingID}
	if liveDismissal(r.Envelope.Type) {
		return c.revisions[liveRevisionKey{r.SessionGeneration, key}] == r.Revision
	}
	retained := c.readings[key]
	if r.Envelope.Type == protocol.TypeModelList {
		revision := c.catalogReplyRevision
		if retained != nil {
			revision = max(revision, retained.Revision)
		}
		return r.Revision == revision
	}
	// Queued turn progress is an edge consumers need even after a later idle
	// replaces retention. Generation fencing above and relay revision watermarks
	// still reject retired sources and delivery that would roll a consumer back.
	if r.Envelope.Type == protocol.TypeTurnState && retained != nil && r.Revision > 0 && r.Revision <= retained.Revision {
		var phase protocol.TurnStatePayload
		if json.Unmarshal(r.Envelope.Payload, &phase) == nil && (phase.State == "thinking" || phase.State == "responding") {
			return true
		}
	}
	return retained != nil && retained.Revision == r.Revision
}

func runConfigResultFor(
	resolve func(convID string) (boundRunSettings, bool),
	usage func(sessionID string) (usedTokens, windowTokens int),
) func(convID string) (relay.RunConfig, bool, daemonLiveReading) {
	if resolve == nil {
		return nil
	}
	return func(convID string) (relay.RunConfig, bool, daemonLiveReading) {
		b, ok := resolve(convID)
		if !ok {
			return relay.RunConfig{}, false, daemonLiveReading{}
		}
		cfg := relay.RunConfig{
			SessionID:      b.sessionID,
			Model:          b.model,
			Effort:         b.effort,
			YOLO:           b.yolo,
			PermissionMode: b.permissionMode,
		}
		if usage != nil && b.live {
			cfg.UsedTokens, cfg.WindowTokens = usage(b.sessionID)
		}
		reading, _ := runSettingsReading(b, cfg, protocol.Envelope{})
		return cfg, true, reading
	}
}
