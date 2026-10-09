package history

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/pyrycode/pyrycode/internal/conversations"
)

// ForwardReader consumes raw committed entries chronologically. Its methods must
// be called serially. It retains only scalar progress, never decoded entries.
type ForwardReader struct {
	store   *Store
	convID  conversations.ConversationID
	afterID uint64
	segment uint64
	sealed  bool

	// Last validated stored ID preceding the first segment the next walk reads.
	// An unsealed segment is reread, so its own IDs are excluded from this bound.
	boundaryID uint64
}

// Forward constructs an exclusive resume position; zero starts at the beginning.
// It shares Append's authenticated-conversation precondition. Capture a snapshot
// bound with LatestEntryID, then pass it to Walk before continuing with Tail.
func (s *Store) Forward(convID conversations.ConversationID, afterID uint64) (*ForwardReader, error) {
	if !conversations.ValidID(string(convID)) {
		return nil, fmt.Errorf("%w: conversation id %q", ErrInvalidID, string(convID))
	}
	if s == nil {
		return nil, fmt.Errorf("history: store is unavailable")
	}
	return &ForwardReader{store: s, convID: convID, afterID: afterID}, nil
}

// LastEntryID returns the exclusive resume ID after successful delivery.
func (r *ForwardReader) LastEntryID() uint64 { return r.afterID }

// Walk delivers entries after LastEntryID through throughID, inclusive, in
// chunks of at most MaxPageEntries. Nil return explicitly completes the range,
// including an empty range. Callback success advances progress; callback failure
// leaves its chunk retryable. Delivery runs without the Store mutex held.
// Callbacks must return promptly or honor ctx when blocking.
func (r *ForwardReader) Walk(ctx context.Context, throughID uint64, consume func([]Entry) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if r.afterID >= throughID {
		return nil
	}
	segs, err := r.store.forwardSegments(r.convID)
	if errors.Is(err, fs.ErrNotExist) && r.segment == 0 {
		return nil
	}
	if err != nil {
		return err
	}
	previous := r.boundaryID
	for _, seg := range segs {
		if seg.num < r.segment || (seg.num == r.segment && r.sealed) {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		entries, sealed, err := r.store.forwardSegment(r.convID, seg.name)
		if err != nil {
			return err
		}
		boundaryID := previous
		// Check the stored order before using IDs as resume boundaries.
		for _, e := range entries {
			if e.entry.ID <= previous {
				return fmt.Errorf("%w: non-increasing entry IDs", ErrCorruptSegment)
			}
			previous = e.entry.ID
		}
		r.segment = seg.num
		r.sealed = false
		r.boundaryID = boundaryID
		var chunk []Entry
		for _, e := range entries {
			if e.entry.ID <= r.afterID {
				continue
			}
			if e.entry.ID > throughID {
				break
			}
			chunk = append(chunk, e.entry)
			if len(chunk) == MaxPageEntries {
				if err := r.deliver(ctx, chunk, consume); err != nil {
					return err
				}
				chunk = nil
			}
		}
		if len(chunk) > 0 {
			if err := r.deliver(ctx, chunk, consume); err != nil {
				return err
			}
		}
		r.sealed = sealed && (len(entries) == 0 || entries[len(entries)-1].entry.ID <= r.afterID)
		if r.sealed {
			r.boundaryID = previous
		}
		if previous >= throughID {
			return nil
		}
	}
	return ctx.Err()
}

func (r *ForwardReader) deliver(ctx context.Context, entries []Entry, consume func([]Entry) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	last := entries[len(entries)-1].ID
	if err := consume(entries); err != nil {
		return err
	}
	r.afterID = last
	return nil
}

// Tail catches up from the reader's position, then follows successful appends
// through the same Store until cancellation or a read/callback error. The log
// supplies entries; wakeups may coalesce. External writers do not wake a tail.
// Registration precedes catch-up so replay and read-to-wait appends cannot be
// lost. No decoded buffers are retained while waiting.
func (r *ForwardReader) Tail(ctx context.Context, consume func([]Entry) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	wake, unregister := r.store.follow(r.convID)
	defer unregister()
	for {
		if err := r.Walk(ctx, ^uint64(0), consume); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-wake:
		}
	}
}

func (s *Store) forwardSegments(convID conversations.ConversationID) ([]segmentRef, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	dir, err := s.resolveDir(convID, false)
	if err != nil {
		return nil, err
	}
	return listSegments(dir)
}

func (s *Store) forwardSegment(convID conversations.ConversationID, name string) ([]segEntry, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	dir, err := s.resolveDir(convID, false)
	if err != nil {
		return nil, false, err
	}
	path := filepath.Join(dir, name)
	info, err := os.Lstat(path)
	if err != nil {
		return nil, false, fmt.Errorf("history: stat segment: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, false, fmt.Errorf("%w: segment is not regular", ErrCorruptSegment)
	}
	entries, size, complete, err := s.readSegment(path)
	return entries, !complete || size >= s.maxSegmentBytes, err
}

func (s *Store) follow(convID conversations.ConversationID) (<-chan struct{}, func()) {
	wake := make(chan struct{}, 1)
	s.mu.Lock()
	if s.followers == nil {
		s.followers = make(map[conversations.ConversationID]map[chan struct{}]struct{})
	}
	if s.followers[convID] == nil {
		s.followers[convID] = make(map[chan struct{}]struct{})
	}
	s.followers[convID][wake] = struct{}{}
	s.mu.Unlock()
	return wake, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		delete(s.followers[convID], wake)
		if len(s.followers[convID]) == 0 {
			delete(s.followers, convID)
		}
	}
}

// notifyCommitted runs under Store.mu after a successful append. Slow consumers
// retain one wakeup while further commits coalesce without blocking the writer.
func (s *Store) notifyCommitted(convID conversations.ConversationID) {
	for wake := range s.followers[convID] {
		select {
		case wake <- struct{}{}:
		default:
		}
	}
}
