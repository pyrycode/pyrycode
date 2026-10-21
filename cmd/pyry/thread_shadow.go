package main

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/msgqueue"
	"github.com/pyrycode/pyrycode/internal/thread"
)

type shadowStore interface {
	Load(context.Context, conversations.ConversationID) error
	Retry(context.Context, conversations.ConversationID) error
	Status(conversations.ConversationID) thread.Snapshot
	Unload(conversations.ConversationID) error
	Shutdown() error
}
type shadowConversation struct {
	publicationOnce sync.Once
	publicationGate chan struct{} // serializes following with query boundary acquisition
	cancel          context.CancelFunc
	done            chan struct{}
	version         uint64
	complete        bool
	idleUnloaded    bool // successful coordinator-owned retirement; not a publication gap
	publisher       *daemonThreadPublisher
	threadEpoch     string
	threadVersion   uint64
	threadPeers     map[string]bool
}

func (w *shadowConversation) lockPublication(ctx context.Context) bool {
	w.publicationOnce.Do(func() { w.publicationGate = make(chan struct{}, 1) })
	select {
	case w.publicationGate <- struct{}{}:
		return true
	case <-ctx.Done():
		return false
	}
}

func (w *shadowConversation) unlockPublication() { <-w.publicationGate }

// threadShadow coordinates optional publication. Discovery includes all history
// writers without adding replay or cache I/O to their commit paths.
type threadShadow struct {
	history       *history.Store
	registry      *conversations.Registry
	store         shadowStore
	publisher     *daemonThreadPublisher
	log           *slog.Logger
	mu            sync.Mutex
	workers       map[conversations.ConversationID]*shadowConversation
	removed       map[conversations.ConversationID]bool
	quietMu       sync.Mutex
	quiet         func(conversations.ConversationID) bool
	queryDemands  map[conversations.ConversationID]*threadQueryDemand // guarded by quietMu
	queryRetiring map[conversations.ConversationID]chan struct{}      // guarded by quietMu
	queryDone     sync.WaitGroup
	queryStopping bool // guarded by quietMu
	ctx           context.Context
	cancel        context.CancelFunc
	done          chan struct{}
	finish        chan struct{}
	once          sync.Once
	shutdownOnce  sync.Once
	shutdownDone  chan struct{}
	err           error
}

func newThreadShadow(h *history.Store, reg *conversations.Registry, store shadowStore, log *slog.Logger) *threadShadow {
	ctx, cancel := context.WithCancel(context.Background())
	return &threadShadow{history: h, registry: reg, store: store, log: log, workers: make(map[conversations.ConversationID]*shadowConversation), removed: make(map[conversations.ConversationID]bool), ctx: ctx, cancel: cancel, done: make(chan struct{}), finish: make(chan struct{}), shutdownDone: make(chan struct{}), quiet: func(conversations.ConversationID) bool { return true }}
}
func (s *threadShadow) start() {
	if s == nil {
		return
	}
	s.once.Do(func() {
		go func() {
			defer close(s.done)
			tick := time.NewTicker(50 * time.Millisecond)
			defer tick.Stop()
			for {
				s.discover()
				select {
				case <-s.finish:
					return
				case <-tick.C:
				}
			}
		}()
	})
}
func (s *threadShadow) discover() bool {
	if s.store == nil || s.history == nil || s.registry == nil {
		return true
	}
	complete := true
	for _, c := range s.registry.List() {
		if !conversations.ValidID(string(c.ID)) {
			continue
		}
		v, err := s.history.LatestEntryID(c.ID)
		if err != nil {
			s.failure(c.ID, "history")
			if _, exists := s.registry.Get(c.ID); exists {
				complete = false
			}
			continue
		}
		if v == 0 {
			continue
		}
		s.mu.Lock()
		_, exists := s.registry.Get(c.ID)
		select {
		case <-s.finish:
			exists = false
		default:
		}
		if exists && !s.removed[c.ID] && s.workers[c.ID] == nil {
			ctx, cancel := context.WithCancel(s.ctx)
			w := &shadowConversation{cancel: cancel, done: make(chan struct{})}
			s.workers[c.ID] = w
			go s.follow(ctx, c.ID, w)
		}
		s.mu.Unlock()
	}
	return complete
}
func (s *threadShadow) consumed(id conversations.ConversationID) uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	if w := s.workers[id]; w != nil {
		return w.version
	}
	return 0
}
func (s *threadShadow) follow(ctx context.Context, id conversations.ConversationID, w *shadowConversation) {
	defer close(w.done)
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		final := false
		select {
		case <-s.finish:
			final = true
		default:
		}
		version, err := s.history.LatestEntryID(id)
		if err != nil {
			s.failure(id, "history")
			if final {
				return
			}
		} else {
			snapshot := s.store.Status(id)
			// A deliberate clean unload keeps the already-published cursor. Reading
			// that same checkpoint must not reconnect every consumer on each idle
			// cycle. A changed usable view still has to prove retained continuity;
			// failure and unplanned withdrawal retain the normal gap handling.
			if !w.idleUnloaded || snapshot.State == thread.StateUsable || snapshot.State == thread.StateUnavailable {
				s.publish(ctx, id, w)
				w.idleUnloaded = false
			}
			switch snapshot.State {
			case thread.StateNotLoaded:
				if version > s.consumed(id) {
					if err := s.store.Load(s.ctx, id); err != nil {
						s.failure(id, "load")
						if final {
							return
						}
					}
				} else if final {
					w.complete = true
					return
				}
			case thread.StateUnavailable:
				s.failure(id, "fold")
				if final {
					return
				}
				if err := s.store.Retry(s.ctx, id); err != nil {
					s.failure(id, "retry")
				}
			case thread.StateUsable:
				if snapshot.Version >= version {
					if final {
						w.complete = true
						return
					}
					s.quietMu.Lock()
					quiet := !snapshot.Active && s.quiet(id) && s.queryDemands[id] == nil
					var retired chan struct{}
					if quiet {
						retired = make(chan struct{})
						if s.queryRetiring == nil {
							s.queryRetiring = make(map[conversations.ConversationID]chan struct{})
						}
						s.queryRetiring[id] = retired
					}
					s.quietMu.Unlock()
					if quiet {
						// Save the consumed boundary before retirement. A racing append exceeds
						// it and reloads on the next pass, even if it committed during Unload.
						s.mu.Lock()
						w.version = snapshot.Version
						s.mu.Unlock()
						if err := s.store.Unload(id); err != nil {
							s.failure(id, "unload")
						} else {
							w.idleUnloaded = true
						}
						s.quietMu.Lock()
						delete(s.queryRetiring, id)
						close(retired)
						s.quietMu.Unlock()
						if w.idleUnloaded {
							s.refreshThreadSummary(ctx, id)
						}
					}
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}
func (s *threadShadow) remove(id conversations.ConversationID) {
	if s == nil || !conversations.ValidID(string(id)) {
		return
	}
	s.mu.Lock()
	s.removed[id] = true
	w := s.workers[id]
	if w != nil {
		w.cancel()
	}
	s.mu.Unlock()
	s.cancelThreadDemand(id)
	if w != nil {
		<-w.done
	}
	if s.store != nil {
		if err := s.store.Unload(id); err != nil {
			s.failure(id, "remove")
		}
	}
	s.mu.Lock()
	delete(s.workers, id)
	s.mu.Unlock()
}

// shutdown is called only after producer admission is sealed and all successful
// history writers have joined. Discovery stops before final committed draining.
func (s *threadShadow) shutdown() error {
	if s == nil {
		return nil
	}
	s.shutdownOnce.Do(func() {
		s.stopThreadDemands()
		s.start()
		// Discover final new conversations before sealing background admission.
		complete := s.discover()
		close(s.finish)
		<-s.done
		s.mu.Lock()
		workers := make([]*shadowConversation, 0, len(s.workers))
		for _, w := range s.workers {
			workers = append(workers, w)
		}
		s.mu.Unlock()
		for _, w := range workers {
			<-w.done
			complete = complete && w.complete
		}
		if s.store != nil {
			s.err = s.store.Shutdown()
		}
		if s.err == nil && !complete {
			s.err = thread.ErrUnavailable
		}
		s.cancel()
		if s.err != nil {
			s.failure("", "shutdown")
		} else if s.store != nil {
			s.log.Info("thread shadow cache stopped", "event", "thread_shadow.clean_exit")
		}
		close(s.shutdownDone)
	})
	<-s.shutdownDone
	return s.err
}
func (s *threadShadow) failure(id conversations.ConversationID, reason string) {
	fields := []any{"event", "thread_shadow.failure", "reason", reason}
	if conversations.ValidID(string(id)) {
		fields = append(fields, "conversation_id", string(id))
	}
	s.log.Warn("thread shadow unavailable", fields...)
}

// shadowQuiescent shares the publication gate with stream handling and channel
// writes. It never performs cache/history I/O or waits for shadow workers.
func shadowQuiescent(d *channelDelivery, q *msgqueue.Queue, sink *streamTurnSink, id conversations.ConversationID) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.held(string(id)) || d.pendingFor(string(id)) || len(q.Snapshot(string(id))) != 0 {
		return false
	}
	sink.offerMu.Lock()
	defer sink.offerMu.Unlock()
	return sink.queued <= sink.shadowProcessed.Load() && len(sink.placementCommands) == 0
}
