package history

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"

	"github.com/pyrycode/pyrycode/internal/conversations"
)

// syncIO is a store-local syscall seam. Callers hold Store.mu.
type syncIO struct {
	open  func(string, int, fs.FileMode) (*os.File, error)
	sync  func(*os.File) error
	close func(*os.File) error
}

// Sync makes preceding successful appends to convID durable using ordinary file
// and directory fsync. It shares Append's authenticated-conversation precondition:
// the ID must come from the authenticated session, never from a client's claim.
// Appends ordered after the captured mutex boundary remain pending. The first
// sync includes all existing segments; subsequent syncs cover this Store's writes.
// Missing/empty logs create nothing unless pending writes require an error.
// Any I/O failure leaves the whole boundary retryable. Sync changes no log bytes,
// entry IDs, read counters or follower notifications; Append itself does no fsync.
func (s *Store) Sync(convID conversations.ConversationID) error {
	if !conversations.ValidID(string(convID)) {
		return fmt.Errorf("%w: conversation id %q", ErrInvalidID, string(convID))
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.convs[convID]
	if c == nil {
		c = &convLog{}
		s.convs[convID] = c
	}
	dir, err := s.resolveDir(convID, false)
	if errors.Is(err, fs.ErrNotExist) && len(c.syncPending) == 0 && !c.syncDirPending {
		return nil
	}
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("history: list sync directory: %w", err)
	}
	present := make(map[uint64]bool)
	for _, e := range entries {
		n, ok := parseSegmentName(e.Name())
		if !ok {
			continue
		}
		if !c.syncDone {
			if c.syncPending == nil {
				c.syncPending = make(map[uint64]bool)
			}
			c.syncPending[n] = true
		}
		// Unlike the reader's listing, certification cannot hide unsafe leaves.
		if !e.Type().IsRegular() {
			return fmt.Errorf("%w: sync segment is not regular", ErrCorruptSegment)
		}
		present[n] = true
	}
	for n := range c.syncPending {
		if !present[n] {
			return fmt.Errorf("history: pending sync segment %q: %w", segmentName(n), fs.ErrNotExist)
		}
	}
	if len(c.syncPending) == 0 {
		return nil
	}
	// ReadDir's fixed-width names give ascending segment order. Keep every target
	// pending until files, directory and all closes have succeeded.
	for _, e := range entries {
		n, ok := parseSegmentName(e.Name())
		if ok && c.syncPending[n] {
			if err := s.syncPath(filepath.Join(dir, e.Name()), false); err != nil {
				return err
			}
		}
	}
	if !c.syncDone || c.syncDirPending {
		if err := s.syncPath(dir, true); err != nil {
			return err
		}
	}
	c.syncDone, c.syncDirPending, c.syncPending = true, false, nil
	return nil
}

func (s *Store) syncPath(path string, directory bool) error {
	flags := os.O_RDONLY | syscall.O_NOFOLLOW | syscall.O_NONBLOCK
	if directory {
		flags |= syscall.O_DIRECTORY
	}
	f, err := s.syncIO.open(path, flags, 0)
	if err != nil {
		return fmt.Errorf("history: open sync target %q: %w", path, err)
	}
	info, err := f.Stat()
	if err == nil && ((!directory && !info.Mode().IsRegular()) || (directory && !info.IsDir())) {
		err = fmt.Errorf("%w: unsafe sync target", ErrCorruptSegment)
	}
	if err != nil {
		return fmt.Errorf("history: stat sync target %q: %w", path, errors.Join(err, s.syncIO.close(f)))
	}
	syncErr := s.syncIO.sync(f)
	closeErr := s.syncIO.close(f)
	if err := errors.Join(syncErr, closeErr); err != nil {
		return fmt.Errorf("history: sync/close target %q: %w", path, err)
	}
	return nil
}
