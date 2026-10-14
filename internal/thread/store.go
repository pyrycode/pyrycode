package thread

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"sync"
	"time"

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
	ErrUnavailable    = errors.New("thread: conversation view is unavailable")
)

// Snapshot is a detached view. Only StateUsable carries items, versions and epoch;
// ErrUnavailable describes recoverable failure without exposing source errors.
type Snapshot struct {
	Active           bool // includes private unresolved work omitted from Items
	State            SnapshotState
	Items            []Item
	Version          uint64
	LastShownVersion uint64 // latest first shown publication or nonempty shown text append
	Epoch            string
	Err              error
}

type storeReader interface {
	Walk(context.Context, uint64, func([]history.Entry) error) error
	Tail(context.Context, func([]history.Entry) error) error
}

type conversationWorker struct {
	cancel     context.CancelFunc
	done       chan struct{}
	retiring   bool
	snapshot   Snapshot
	query      *queryIndex
	err        error // read only after done closes
	changed    chan struct{}
	ranges     []changeBatch
	rangeBytes int
}

// Store follows raw history for caller-authorized conversations. Construct it
// with NewStore; all public methods are safe for concurrent use.
type Store struct {
	mu           sync.Mutex
	history      *history.Store
	forward      func(conversations.ConversationID) (storeReader, error)
	beforeFold   func(context.Context, conversations.ConversationID) error
	queryWork    func(bool) // optional work counter: true for preparation, false for requests
	workers      map[conversations.ConversationID]*conversationWorker
	closed       bool
	initMu       sync.Mutex
	runID        string
	coordinator  conversations.ConversationID
	records      map[conversations.ConversationID]progressRecord
	shutdownDone chan struct{}
	shutdownErr  error
	replace      func(string, string) error
	now          func() time.Time
	newTimer     func(time.Duration) (<-chan time.Time, func())
}

// NewStore creates a background cache owner; history is the durable source.
// Tail notifications cover commits through the supplied history Store only.
// An optional fold barrier runs before each replay/tail chunk. It must return
// promptly or honor cancellation; failure withdraws the conversation view.
func NewStore(h *history.Store, beforeFold ...func(context.Context, conversations.ConversationID) error) *Store {
	var barrier func(context.Context, conversations.ConversationID) error
	if len(beforeFold) > 0 {
		barrier = beforeFold[0]
	}
	return &Store{
		history: h, beforeFold: barrier,
		records: make(map[conversations.ConversationID]progressRecord),
		replace: os.Rename,
		now:     time.Now,
		newTimer: func(d time.Duration) (<-chan time.Time, func()) {
			timer := time.NewTimer(d)
			return timer.C, func() { timer.Stop() }
		},
		workers: make(map[conversations.ConversationID]*conversationWorker),
		forward: func(id conversations.ConversationID) (storeReader, error) { return h.Forward(id, 0) },
	}
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
	w = &conversationWorker{cancel: cancel, done: make(chan struct{}), changed: make(chan struct{}), snapshot: Snapshot{State: StateRebuilding}}
	s.workers[id] = w
	s.records[id] = progressRecord{Err: ErrUnavailable}
	go s.run(workerCtx, id, w)
	return nil
}

func (s *Store) run(ctx context.Context, id conversations.ConversationID, w *conversationWorker) {
	result := ErrUnavailable
	var progress Snapshot
	var checksum, progressChecksum string
	var recovery recoveryRecord
	var savedVersion uint64
	var lastCheckpoint time.Time
	var deadline <-chan time.Time
	var stopTimer func()
	clearTimer := func() {
		if stopTimer != nil {
			stopTimer()
		}
		deadline, stopTimer = nil, nil
	}
	flush := func() error {
		if err := s.checkpoint(id, recovery, progress, progressChecksum); err != nil {
			return err
		}
		savedVersion, lastCheckpoint = progress.Version, s.now()
		clearTimer()
		return nil
	}
	defer func() {
		clearTimer()
		s.mu.Lock()
		retiring := w.retiring
		s.mu.Unlock()
		if retiring && progress.State == StateUsable && errors.Is(result, context.Canceled) {
			result = nil
			if progress.Version != savedVersion {
				result = flush()
			}
		}
		w.cancel()
		s.mu.Lock()
		defer s.mu.Unlock()
		if result != nil && result != ErrPersistence {
			result = ErrUnavailable
		}
		w.err = result
		if s.workers[id] == w {
			s.records[id] = progressRecord{Epoch: progress.Epoch, Version: progress.Version, LastShownVersion: progress.LastShownVersion, Checksum: progressChecksum, Err: result}
			if !w.retiring && !s.closed {
				w.snapshot = Snapshot{State: StateUnavailable, Err: ErrUnavailable}
				w.query = nil
				w.ranges, w.rangeBytes = nil, 0
				w.notify()
			}
		}
		close(w.done)
	}()
	if s.history == nil {
		return
	}
	// Page reads the surviving log; LatestEntryID can retain an append cursor
	// ahead of history after truncation. Replay must still reach this read bound.
	page, err := s.history.Page(id, "", 1)
	if err != nil {
		return
	}
	var bound uint64
	if len(page.Entries) > 0 {
		bound = page.Entries[0].ID
	}
	if _, err := s.history.EnsureLogDir(id); err != nil {
		result = ErrPersistence
		return
	}
	candidate, err := s.recoveryCandidate(id, bound)
	if err != nil {
		result = err
		return
	}
	recovery, err = s.beginRun(id)
	if err != nil {
		result = err
		return
	}
	if err := s.writeCacheFile(id, recoveryName, recovery); err != nil {
		result = err
		return
	}
	reader, err := s.forward(id)
	if err != nil {
		return
	}
	fold := New(string(id))
	feed := func(entries []history.Entry) error {
		if s.beforeFold != nil {
			if err := s.beforeFold(ctx, id); err != nil {
				return err
			}
		}
		if err := fold.Feed(entries); err != nil {
			return err
		}
		if len(entries) > 0 {
			checksum = entryChecksum(entries[len(entries)-1])
		}
		return nil
	}
	epoch := ""
	if candidate.Epoch != "" {
		if err := reader.Walk(ctx, candidate.Version, feed); err != nil {
			return
		}
		if fold.Version() == candidate.Version && checksum == candidate.Checksum && fold.lastShown == candidate.LastShownVersion {
			epoch = candidate.Epoch
		}
	}
	if err := reader.Walk(ctx, bound, feed); err != nil || fold.Version() != bound || ctx.Err() != nil {
		return
	}
	if epoch == "" {
		epoch, err = randomToken()
		if err != nil {
			result = err
			return
		}
	}
	publish := func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		snapshot := Snapshot{State: StateUsable, Items: fold.Items(), Version: fold.Version(), LastShownVersion: fold.lastShown, Epoch: epoch}
		for _, item := range fold.items {
			snapshot.Active = snapshot.Active || item.Active
		}
		initial := progress.State != StateUsable
		becameIdle := progress.Active && !snapshot.Active
		progress, progressChecksum = snapshot, checksum
		if initial || progress.Version != savedVersion {
			remaining := lastCheckpoint.Add(30 * time.Second).Sub(s.now())
			if initial || becameIdle || remaining <= 0 {
				if err := flush(); err != nil {
					return err
				}
			} else if deadline == nil {
				deadline, stopTimer = s.newTimer(remaining)
			}
		}
		s.publish(ctx, id, w, snapshot, fold)
		return nil
	}
	if err := publish(); err != nil {
		result = err
		return
	}
	// Tail waits for commits independently; only this worker accesses the fold
	// and checkpoints. Acknowledgement keeps at most one chunk in flight.
	tailCtx, cancelTail := context.WithCancel(ctx)
	chunks := make(chan []history.Entry)
	ack := make(chan error)
	tailResult := make(chan error, 1)
	tailDone := make(chan struct{})
	go func() {
		defer close(tailDone)
		tailResult <- reader.Tail(tailCtx, func(entries []history.Entry) error {
			select {
			case chunks <- entries:
			case <-tailCtx.Done():
				return tailCtx.Err()
			}
			select {
			case err := <-ack:
				return err
			case <-tailCtx.Done():
				return tailCtx.Err()
			}
		})
	}()
	defer func() { cancelTail(); <-tailDone }()
	for {
		select {
		case <-ctx.Done():
			result = ctx.Err()
			return
		case result = <-tailResult:
			if result == nil {
				result = ErrUnavailable
			}
			return
		case <-deadline:
			if result = flush(); result != nil {
				return
			}
		case entries := <-chunks:
			if result = ctx.Err(); result == nil {
				// Partial Feed failures never become complete progress. Retry
				// constructs a fresh reader and fold after a failed chunk.
				if err := feed(entries); err != nil {
					result = ErrUnavailable
					if errors.Is(err, context.Canceled) {
						result = err
					}
				} else {
					result = publish()
				}
			}
			if result != nil {
				return
			}
			select {
			case ack <- nil:
			case <-ctx.Done():
				result = ctx.Err()
				return
			}
		}
	}
}

func (s *Store) checkpoint(id conversations.ConversationID, recovery recoveryRecord, snapshot Snapshot, checksum string) error {
	return s.writeCacheFile(id, cacheName, cacheRecord{Schema: cacheSchema, Rules: foldingRules, Epoch: snapshot.Epoch, Version: snapshot.Version, LastShownVersion: snapshot.LastShownVersion, Checksum: checksum, Complete: true, Recovery: recovery})
}

func (s *Store) publish(ctx context.Context, id conversations.ConversationID, w *conversationWorker, snapshot Snapshot, fold *Fold) {
	index := newQueryIndex(snapshot.Items, s.queryWork)
	index.prepareCatchUp(fold)
	s.mu.Lock()
	before := w.snapshot
	s.mu.Unlock()
	var batch changeBatch
	if before.State == StateUsable {
		batch.observation = difference(before, snapshot)
		encoded, _ := json.Marshal(batch.observation.Changes)
		batch.bytes = len(encoded)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.workers[id] == w && !w.retiring && !s.closed && ctx.Err() == nil {
		if before.State == StateUsable {
			w.retain(batch)
		}
		w.snapshot = snapshot
		w.query = index
		w.notify()
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
	snapshot.Items = copyItems(snapshot.Items)
	return snapshot
}

// Unload checkpoints dirty complete progress, cancels and joins one worker, and
// releases all items/private fold state. It does not certify a clean lifetime.
// Persistence or incomplete recovery failures are returned without source data.
func (s *Store) Unload(id conversations.ConversationID) error {
	if !conversations.ValidID(string(id)) {
		return history.ErrInvalidID
	}
	s.mu.Lock()
	w := s.workers[id]
	if w == nil {
		err := s.records[id].Err
		s.mu.Unlock()
		return err
	}
	w.retiring = true
	w.snapshot = Snapshot{}
	w.query = nil
	w.ranges, w.rangeBytes = nil, 0
	w.notify()
	w.cancel()
	s.mu.Unlock()
	<-w.done
	s.mu.Lock()
	if s.workers[id] == w {
		delete(s.workers, id)
	}
	s.mu.Unlock()
	return w.err
}

// Shutdown closes admission, joins workers and checkpoints every conversation
// opened in this lifetime, including unloaded ones. Only successful completion
// certifies epoch reuse on reopen. Concurrent callers share the same result.
func (s *Store) Shutdown() error {
	s.mu.Lock()
	if s.shutdownDone != nil {
		done := s.shutdownDone
		s.mu.Unlock()
		<-done
		return s.shutdownErr
	}
	s.shutdownDone = make(chan struct{})
	s.closed = true
	workers := make([]*conversationWorker, 0, len(s.workers))
	for _, w := range s.workers {
		w.retiring = true
		w.snapshot = Snapshot{}
		w.query = nil
		w.ranges, w.rangeBytes = nil, 0
		w.notify()
		w.cancel()
		workers = append(workers, w)
	}
	s.mu.Unlock()
	for _, w := range workers {
		<-w.done
	}
	s.mu.Lock()
	records := s.records
	clear(s.workers)
	s.mu.Unlock()
	err := s.finishRun(records)
	s.mu.Lock()
	s.shutdownErr = err
	close(s.shutdownDone)
	s.mu.Unlock()
	return err
}
