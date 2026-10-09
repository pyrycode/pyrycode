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
	Snapshot(conversations.ConversationID) thread.Snapshot
	Unload(conversations.ConversationID) error
	Shutdown() error
}
type shadowConversation struct {
	cancel   context.CancelFunc
	done     chan struct{}
	version  uint64
	complete bool
}

// threadShadow has no publication capability. Discovery includes all history
// writers without adding replay or cache I/O to their commit paths.
type threadShadow struct {
	history      *history.Store
	registry     *conversations.Registry
	store        shadowStore
	log          *slog.Logger
	mu           sync.Mutex
	workers      map[conversations.ConversationID]*shadowConversation
	removed      map[conversations.ConversationID]bool
	quietMu      sync.Mutex
	quiet        func(conversations.ConversationID) bool
	ctx          context.Context
	cancel       context.CancelFunc
	done         chan struct{}
	finish       chan struct{}
	once         sync.Once
	shutdownOnce sync.Once
	shutdownDone chan struct{}
	err          error
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
			tick := time.NewTicker(250 * time.Millisecond)
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
func (s *threadShadow) discover() {
	if s.store == nil || s.history == nil || s.registry == nil {
		return
	}
	for _, c := range s.registry.List() {
		if !conversations.ValidID(string(c.ID)) {
			continue
		}
		v, err := s.history.LatestEntryID(c.ID)
		if err != nil {
			s.failure(c.ID, "history")
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
	tick := time.NewTicker(100 * time.Millisecond)
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
			snapshot := s.store.Snapshot(id)
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
					quiet := !snapshot.Active && s.quiet(id)
					s.quietMu.Unlock()
					if quiet {
						// Save the consumed boundary before retirement. A racing append exceeds
						// it and reloads on the next pass, even if it committed during Unload.
						s.mu.Lock()
						w.version = snapshot.Version
						s.mu.Unlock()
						if err := s.store.Unload(id); err != nil {
							s.failure(id, "unload")
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
		s.start()
		// Discover final new conversations before sealing background admission.
		s.discover()
		close(s.finish)
		<-s.done
		s.mu.Lock()
		workers := make([]*shadowConversation, 0, len(s.workers))
		for _, w := range s.workers {
			workers = append(workers, w)
		}
		s.mu.Unlock()
		complete := true
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
