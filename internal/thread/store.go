package thread

import (
	"context"
	"errors"
	"sync"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/history"
)

// SnapshotState describes whether a conversation has a complete consumed view.
type SnapshotState uint8

const (
	StateNotLoaded SnapshotState = iota
	StateRebuilding
	StateUsable
	StateUnavailable
)

var (
	ErrClosed         = errors.New("thread: store is shut down")
	ErrUnloading      = errors.New("thread: conversation is unloading")
	ErrNotLoaded      = errors.New("thread: conversation is not loaded")
	ErrNotUnavailable = errors.New("thread: conversation is not unavailable")
	ErrUnavailable    = errors.New("thread: conversation history is unavailable")
)

// Snapshot is a detached view. Only StateUsable carries Items and Version;
// ErrUnavailable describes recoverable failure without exposing source errors.
type Snapshot struct {
	State   SnapshotState
	Items   []Item
	Version uint64
	Err     error
}

type storeReader interface {
	Walk(context.Context, uint64, func([]history.Entry) error) error
	Tail(context.Context, func([]history.Entry) error) error
}

type conversationWorker struct {
	cancel   context.CancelFunc
	done     chan struct{}
	retiring bool
	snapshot Snapshot
}

// Store follows raw history for caller-authorized conversations. Construct it
// with NewStore; all public methods are safe for concurrent use.
type Store struct {
	mu      sync.Mutex
	history *history.Store
	forward func(conversations.ConversationID) (storeReader, error)
	workers map[conversations.ConversationID]*conversationWorker
	closed  bool
}

// NewStore creates an in-memory owner; history remains the only durable record.
// Tail notifications cover commits through the supplied history Store only.
func NewStore(h *history.Store) *Store {
	return &Store{history: h, workers: make(map[conversations.ConversationID]*conversationWorker), forward: func(id conversations.ConversationID) (storeReader, error) { return h.Forward(id, 0) }}
}

// Load starts background replay without waiting for history I/O. Repeated loads
// keep the existing worker, including an unavailable view requiring Retry.
// ctx governs the loaded worker's lifetime, rather than just the Load call.
// The caller must already be authorized for id.
func (s *Store) Load(ctx context.Context, id conversations.ConversationID) error {
	return s.start(ctx, id, false)
}

// Retry replaces an unavailable view with fresh replay, discarding all private
// reader/fold progress. It never replaces a rebuilding or usable worker.
func (s *Store) Retry(ctx context.Context, id conversations.ConversationID) error {
	return s.start(ctx, id, true)
}

func (s *Store) start(ctx context.Context, id conversations.ConversationID, retry bool) error {
	if !conversations.ValidID(string(id)) {
		return history.ErrInvalidID
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	w := s.workers[id]
	if w != nil && w.retiring {
		return ErrUnloading
	}
	if retry {
		if w == nil {
			return ErrNotLoaded
		}
		if w.snapshot.State != StateUnavailable {
			return ErrNotUnavailable
		}
	} else if w != nil {
		return nil
	}
	workerCtx, cancel := context.WithCancel(ctx)
	w = &conversationWorker{cancel: cancel, done: make(chan struct{}), snapshot: Snapshot{State: StateRebuilding}}
	s.workers[id] = w
	go s.run(workerCtx, id, w)
	return nil
}

func (s *Store) run(ctx context.Context, id conversations.ConversationID, w *conversationWorker) {
	defer func() {
		w.cancel()
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.workers[id] == w && !w.retiring && !s.closed {
			w.snapshot = Snapshot{State: StateUnavailable, Err: ErrUnavailable}
		}
		close(w.done)
	}()
	bound, err := s.history.LatestEntryID(id)
	if err != nil {
		return
	}
	reader, err := s.forward(id)
	if err != nil {
		return
	}
	fold := New(string(id))
	if err := reader.Walk(ctx, bound, fold.Feed); err != nil || fold.Version() != bound {
		return
	}
	publish := func() {
		s.publish(ctx, id, w, Snapshot{State: StateUsable, Items: fold.Items(), Version: fold.Version()})
	}
	publish()
	// Any tail error ends this worker. Partial Feed advancement is never retried
	// into the same fold; the deferred completion withdraws the usable view.
	_ = reader.Tail(ctx, func(entries []history.Entry) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := fold.Feed(entries); err != nil {
			return err
		}
		publish()
		return nil
	})
}

func (s *Store) publish(ctx context.Context, id conversations.ConversationID, w *conversationWorker, snapshot Snapshot) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.workers[id] == w && !w.retiring && !s.closed && ctx.Err() == nil {
		w.snapshot = snapshot
	}
}

// Snapshot does no history I/O. Items/content are copied from an immutable
// publication, keeping their revisions and consumed Version consistent.
func (s *Store) Snapshot(id conversations.ConversationID) Snapshot {
	s.mu.Lock()
	var snapshot Snapshot
	if w := s.workers[id]; w != nil && !w.retiring {
		snapshot = w.snapshot
	}
	s.mu.Unlock()
	if snapshot.Items != nil {
		items := make([]Item, len(snapshot.Items))
		copy(items, snapshot.Items)
		for i := range items {
			items[i].Content = append([]byte(nil), items[i].Content...)
		}
		snapshot.Items = items
	}
	return snapshot
}

// Unload cancels and joins one worker, releasing its view and continuation
// state. After return the conversation is not loaded unless explicitly reopened
// concurrently. Repeated unloads are safe.
func (s *Store) Unload(id conversations.ConversationID) {
	s.mu.Lock()
	w := s.workers[id]
	if w == nil {
		s.mu.Unlock()
		return
	}
	w.retiring = true
	w.snapshot = Snapshot{}
	w.cancel()
	s.mu.Unlock()
	<-w.done
	s.mu.Lock()
	if s.workers[id] == w {
		delete(s.workers, id)
	}
	s.mu.Unlock()
}

// Shutdown cancels and joins every worker, releases loaded state, and rejects
// all subsequent Load/Retry calls. Repeated or concurrent shutdowns are safe.
func (s *Store) Shutdown() {
	s.mu.Lock()
	s.closed = true
	workers := make([]*conversationWorker, 0, len(s.workers))
	for _, w := range s.workers {
		w.retiring = true
		w.snapshot = Snapshot{}
		w.cancel()
		workers = append(workers, w)
	}
	s.mu.Unlock()
	for _, w := range workers {
		<-w.done
	}
	s.mu.Lock()
	clear(s.workers)
	s.mu.Unlock()
}
