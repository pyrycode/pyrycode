package thread

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
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
	parent       context.Context
	cancel       context.CancelFunc
	done         chan struct{}
	retiring     bool
	renderFailed bool
	snapshot     Snapshot
	items        []*Item
	query        *queryIndex
	err          error // read only after done closes
	changed      chan struct{}
	ranges       []changeBatch
	rangeBytes   int
}

// Store follows raw history for caller-authorized conversations. Construct it
// with NewStore; all public methods are safe for concurrent use.
type Store struct {
	mu             sync.Mutex
	history        *history.Store
	forward        func(conversations.ConversationID) (storeReader, error)
	lookup         func(context.Context, conversations.ConversationID, []uint64) ([]history.Entry, error)
	beforeFold     func(context.Context, conversations.ConversationID) error
	queryWork      func(bool) // optional work counter: true for preparation, false for requests
	queryCopies    func(int)  // optional pointer-copy counter for index preparation
	workers        map[conversations.ConversationID]*conversationWorker
	closed         bool
	records        map[conversations.ConversationID]progressRecord
	shutdownDone   chan struct{}
	shutdownErr    error
	replace        func(string, string) error
	syncDir        func(string) error
	checkpointStep func(string, func() error) error
	newTimer       func() checkpointTimer
	log            *slog.Logger
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
		records:  make(map[conversations.ConversationID]progressRecord),
		replace:  os.Rename,
		syncDir:  syncCacheDir,
		newTimer: newCheckpointTimer,
		log:      slog.Default(),
		workers:  make(map[conversations.ConversationID]*conversationWorker),
		forward:  func(id conversations.ConversationID) (storeReader, error) { return h.Forward(id, 0) },
		lookup: func(ctx context.Context, id conversations.ConversationID, ids []uint64) ([]history.Entry, error) {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			return h.Lookup(id, ids)
		},
	}
}

// Load starts background replay without waiting for history I/O. Repeated loads
// keep the existing worker, including an unavailable view requiring Retry.
// ctx governs the loaded worker's lifetime, rather than just the Load call.
// The caller must already be authorized for id.
func (s *Store) Load(ctx context.Context, id conversations.ConversationID) error {
	return s.start(ctx, id, false)
}

// Retry replaces an unavailable view using validated disk state, discarding
// private reader/fold progress. It never replaces a rebuilding or usable worker.
func (s *Store) Retry(ctx context.Context, id conversations.ConversationID) error {
	return s.start(ctx, id, true)
}

func (s *Store) start(ctx context.Context, id conversations.ConversationID, retry bool) error {
	if !conversations.ValidID(string(id)) {
		return history.ErrInvalidID
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for {
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
			// Withdrawal can precede producer exit. Replacement requires the
			// current worker to be joined under the same identity check.
			select {
			case <-w.done:
			default:
				done := w.done
				s.mu.Unlock()
				select {
				case <-done:
				case <-ctx.Done():
				}
				s.mu.Lock()
				continue
			}
		} else if w != nil {
			return nil
		}
		workerCtx, cancel := context.WithCancel(ctx)
		w = &conversationWorker{parent: ctx, cancel: cancel, done: make(chan struct{}), changed: make(chan struct{}), snapshot: Snapshot{State: StateRebuilding}}
		s.workers[id] = w
		s.records[id] = progressRecord{Err: ErrUnavailable}
		go s.run(workerCtx, id, w)
		return nil
	}
}

func (s *Store) run(ctx context.Context, id conversations.ConversationID, w *conversationWorker) {
	result := ErrUnavailable
	var progress Snapshot
	var checksum, progressChecksum string
	var capture publicationCapture
	var checkpoint *workerCheckpoint
	complete := false
	capture.copied = s.queryCopies
	defer func() {
		s.mu.Lock()
		retiring := w.retiring
		renderFailed := w.renderFailed
		s.mu.Unlock()
		if retiring && !renderFailed && complete && w.parent.Err() == nil && progress.State == StateUsable && errors.Is(result, context.Canceled) {
			result = checkpoint.commit(progress, progressChecksum, true)
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
				w.items = nil
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
	source := newRecentSource(func(ids []uint64) ([]history.Entry, error) { return s.lookup(ctx, id, ids) })
	checkpoint, dirty, err := s.resumeCheckpoint(ctx, id, bound, source)
	if err != nil {
		result = err
		return
	}
	checkpoint.parent = w.parent
	if dirty.Epoch == "" {
		dirty.Epoch, err = randomToken()
		if err != nil {
			result = err
			return
		}
	}
	dirty.Schema, dirty.Rules, dirty.Clean = cacheSchema, foldingRules, false
	if err := s.writeCacheFile(id, cacheName, dirty); err != nil {
		result = err
		return
	}
	if err := s.dirtyCheckpoint(id); err != nil {
		result = err
		return
	}
	if !checkpoint.writer.fullRefold {
		if err := checkpoint.writer.reconcileResume(); err != nil {
			result = err
			return
		}
	}
	if err := s.removeLegacy(id); err != nil {
		result = err
		return
	}
	reader, err := s.forward(id)
	if err != nil {
		return
	}
	fold := checkpoint.fold
	if _, production := reader.(*history.ForwardReader); production {
		reader, err = s.history.Forward(id, fold.Version())
		if err != nil {
			return
		}
	}
	fold.captureSet = make(map[int]struct{})
	if !checkpoint.writer.fullRefold {
		capture.restore(fold)
	}
	checksum = dirty.Checksum
	resumeVersion := fold.Version()
	feed := func(entries []history.Entry) error {
		// Wrapped fixture readers retain the original hook signature. Concrete
		// production readers are positioned before reading any suffix segments.
		for len(entries) > 0 && entries[0].ID <= resumeVersion {
			entries = entries[1:]
		}
		if len(entries) == 0 {
			return nil
		}
		if s.beforeFold != nil {
			if err := s.beforeFold(ctx, id); err != nil {
				return err
			}
		}
		complete = false
		source.chunk = entries
		err := fold.Feed(entries)
		source.remember(entries)
		if err != nil {
			return err
		}
		if len(entries) > 0 {
			checksum = entryChecksum(entries[len(entries)-1])
		}
		return nil
	}
	epoch := dirty.Epoch
	if err := reader.Walk(ctx, bound, feed); err != nil || fold.Version() != bound || ctx.Err() != nil {
		return
	}
	publish := func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		items, changes, err := capture.capture(fold)
		source.chunk = nil
		if err != nil {
			return err
		}
		snapshot := changes.Snapshot
		snapshot.Epoch = epoch
		changes.Snapshot = snapshot
		progress, progressChecksum = snapshot, checksum
		complete = true
		s.publish(ctx, id, w, items, changes, fold, &capture)
		return nil
	}
	if err := publish(); err != nil {
		result = err
		return
	}
	result = s.tailCheckpoints(ctx, reader, checkpoint, func() (Snapshot, string) { return progress, progressChecksum }, func(entries []history.Entry) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		// Partial Feed failures never become complete retirement progress.
		if err := feed(entries); err != nil {
			return err
		}
		return publish()
	})
	if result == nil {
		result = ErrUnavailable
	}
}

func (s *Store) publish(ctx context.Context, id conversations.ConversationID, w *conversationWorker, items []*Item, changes Observation, fold *Fold, capture *publicationCapture) {
	s.mu.Lock()
	before := w.snapshot
	index := w.query
	s.mu.Unlock()
	if index == nil {
		index = newPublicationIndex(items, s.queryWork)
		index.prepareCatchUp(fold)
		for _, item := range items {
			if item == nil {
				continue
			}
			row, _ := index.lookup(item.ID, true)
			if header, ok := capture.headers[item.ID]; ok {
				row.header = header
			}
			index.refreshHeader(row, fold.version)
		}
	} else {
		index = index.advance(items, capture.affected, fold, s.queryWork, s.queryCopies, capture.headers)
	}
	index.worker = w
	var batch changeBatch
	if before.State == StateUsable {
		batch.observation = changes
		encoded, _ := json.Marshal(batch.observation.Changes)
		batch.bytes = len(encoded)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.workers[id] == w && !w.retiring && !s.closed && ctx.Err() == nil {
		if before.State == StateUsable {
			w.retain(batch)
		}
		w.snapshot = changes.Snapshot
		w.items = items
		w.query = index
		w.notify()
	}
}

// Snapshot renders one immutable publication outside the store mutex. Source
// failure returns only unavailable State/Err and withdraws the captured worker.
func (s *Store) Snapshot(id conversations.ConversationID) Snapshot {
	s.mu.Lock()
	var snapshot Snapshot
	var items []*Item
	var index *queryIndex
	if w := s.workers[id]; w != nil && !w.retiring {
		snapshot = w.snapshot
		items = w.items
		index = w.query
	}
	s.mu.Unlock()
	if items != nil {
		snapshot.Items = detachPublication(items)
	} else {
		snapshot.Items = copyItems(snapshot.Items)
	}
	if snapshot.State == StateUsable {
		var err error
		snapshot.Items, err = s.renderItems(context.Background(), id, index, snapshot.Items)
		if err != nil {
			return Snapshot{State: StateUnavailable, Err: ErrUnavailable}
		}
	}
	return snapshot
}

// Status returns Snapshot's metadata and readiness from one publication, with
// nil Items. It performs no I/O, worker load, item traversal or allocation.
func (s *Store) Status(id conversations.ConversationID) Snapshot {
	s.mu.Lock()
	var snapshot Snapshot
	if w := s.workers[id]; w != nil && !w.retiring {
		snapshot = w.snapshot
	}
	s.mu.Unlock()
	snapshot.Items = nil
	return snapshot
}

// Unload cancels and joins one worker, certifies its complete progress clean,
// and releases all items/private fold state.
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
	w.items = nil
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

// Shutdown closes admission and joins workers, certifying each independently.
// Already unloaded certificates are preserved. Concurrent callers share the result.
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
		w.items = nil
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
	var err error
	for _, p := range s.records {
		if p.Err != nil {
			err = ErrPersistence
		}
	}
	clear(s.workers)
	s.mu.Unlock()
	s.mu.Lock()
	s.shutdownErr = err
	close(s.shutdownDone)
	s.mu.Unlock()
	return err
}
