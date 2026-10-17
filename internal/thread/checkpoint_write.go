package thread

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"hash/crc32"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"

	"github.com/pyrycode/pyrycode/internal/conversations"
)

// checkpointWriter is owned and serialized by one conversation's worker outside
// Store.mu. Inputs are detached exports; changed includes private/recipe changes
// since the last acknowledgement. Disk state, never ack, governs reconciliation.
type checkpointWriter struct {
	s          *Store
	id         conversations.ConversationID
	source     HistorySource
	ack        checkpointFileState
	step       func(string, func() error) error
	write      func(*os.File, []byte) (int, error)
	fullRefold bool
	generation string
}

func (w *checkpointWriter) run(stage string, action func() error) error {
	var err error
	if w.step != nil {
		err = w.step(stage, action)
	} else {
		err = action()
	}
	if err != nil {
		return ErrPersistence
	}
	return nil
}

func (w *checkpointWriter) put(stage string, f *os.File, raw []byte) error {
	return w.run(stage, func() error {
		write := w.write
		if write == nil {
			write = (*os.File).Write
		}
		n, err := write(f, raw)
		if err == nil && n != len(raw) {
			err = io.ErrShortWrite
		}
		return err
	})
}

// check admits an absent or regular package leaf in the exact history directory.
func (w *checkpointWriter) check(dir, name string) error {
	again, err := w.s.history.LogDir(w.id)
	if err != nil || again != dir || !cacheLeaf(name) {
		return ErrPersistence
	}
	info, err := os.Lstat(filepath.Join(dir, name))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil || !info.Mode().IsRegular() {
		return ErrPersistence
	}
	return nil
}

func (w *checkpointWriter) commit(d CheckpointState, all, changed []CheckpointItem, c cacheRecord) (result error) {
	if !conversations.ValidID(string(w.id)) || c.Schema != cacheSchema || c.Rules != foldingRules || !validToken(c.Epoch) ||
		!validEvidence(c.Version, c.LastShownVersion, c.Checksum) || c.Version != d.Version || c.LastShownVersion != d.LastShown {
		return ErrPersistence
	}
	if _, err := ImportCheckpoint(string(w.id), w.source, d, all); err != nil {
		return err
	}
	dir, err := w.s.history.EnsureLogDir(w.id)
	if err != nil {
		return ErrPersistence
	}
	defer func() {
		if result != nil && c.Clean && w.check(dir, checkpointStateName) == nil {
			// Failed clean certification cannot leave a trusted clean claim. If
			// invalidation itself fails, the owner must still refuse certification.
			_ = os.Remove(filepath.Join(dir, checkpointStateName)) // best-effort invalidation
			_ = w.s.syncDir(dir)                                   // best-effort durability of invalidation
		}
	}()
	if err := w.run("history-sync", func() error { return w.s.history.Sync(w.id) }); err != nil {
		return err
	}
	bound, err := w.s.history.LatestEntryID(w.id)
	if err != nil || bound < d.Version {
		return ErrPersistence
	}
	var prior *Fold
	var saved checkpointFileState
	if err := w.run("reconcile", func() error {
		var err error
		prior, saved, err = w.s.readCheckpoint(w.id, w.source)
		if errors.Is(err, ErrPersistence) {
			return err
		}
		return nil // missing, incompatible or invalid cache requires a full generation
	}); err != nil {
		return err
	}
	fresh := prior == nil || w.fullRefold && saved.Generation != w.generation
	rows := changed
	if fresh {
		token, err := randomToken()
		if err != nil {
			return err
		}
		saved = checkpointFileState{Generation: "thread-index-" + token + ".bin"}
		w.generation = saved.Generation
		rows = all
	} else {
		if saved.Fold.Version > d.Version {
			return ErrPersistence
		}
		// Validate delta completeness before mutating the surviving generation.
		_, old, err := prior.ExportCheckpoint()
		if err != nil {
			return err
		}
		latest := make(map[uint64]CheckpointItem, len(all))
		for _, row := range old {
			latest[row.Metadata.ID] = row
		}
		for _, row := range changed {
			latest[row.Metadata.ID] = row
		}
		if len(latest) != len(all) {
			return ErrPersistence
		}
		for _, row := range all {
			a, errA := json.Marshal(row)
			b, errB := json.Marshal(latest[row.Metadata.ID])
			if errA != nil || errB != nil || !bytes.Equal(a, b) {
				return ErrPersistence
			}
		}
	}
	if err := w.check(dir, saved.Generation); err != nil {
		return err
	}
	flags := os.O_RDWR | syscall.O_NOFOLLOW | syscall.O_NONBLOCK
	if fresh {
		flags |= os.O_CREATE | os.O_EXCL
	}
	f, err := os.OpenFile(filepath.Join(dir, saved.Generation), flags, 0600)
	if err != nil {
		return ErrPersistence
	}
	defer f.Close() // also close on a failed write/sync; successful close is checked below
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() < saved.ValidLength {
		return ErrPersistence
	}
	if err := w.run("index-truncate", func() error {
		if err := w.check(dir, saved.Generation); err != nil {
			return err
		}
		if info.Size() != saved.ValidLength {
			return f.Truncate(saved.ValidLength)
		}
		return nil
	}); err != nil {
		return err
	}
	if _, err := f.Seek(saved.ValidLength, io.SeekStart); err != nil {
		return ErrPersistence
	}
	for _, row := range rows {
		raw, err := json.Marshal(row)
		if err != nil || uint64(len(raw)) > uint64(^uint32(0)) {
			return ErrPersistence
		}
		frame := make([]byte, checkpointFrameHeader, checkpointFrameHeader+len(raw))
		binary.BigEndian.PutUint32(frame, uint32(len(raw)))
		binary.BigEndian.PutUint32(frame[4:], crc32.Checksum(raw, checkpointCRC))
		frame = append(frame, raw...)
		if err := w.check(dir, saved.Generation); err != nil {
			return err
		}
		if err := w.put("index-append", f, frame); err != nil {
			return err
		}
		saved.ValidLength += int64(len(frame))
	}
	if err := w.run("index-sync", f.Sync); err != nil {
		return err
	}
	if err := w.run("index-close", f.Close); err != nil {
		return err
	}
	// Repeat directory sync even for an existing generation: a prior attempt may
	// have created it without establishing directory-entry durability.
	if err := w.run("index-directory", func() error {
		if err := w.check(dir, saved.Generation); err != nil {
			return err
		}
		return w.s.syncDir(dir)
	}); err != nil {
		return err
	}
	saved.Schema, saved.Fold, saved.Certificate = checkpointFileSchema, d, c
	if err := w.replaceState(dir, saved); err != nil {
		return err
	}
	if err := w.cleanGenerations(dir, saved.Generation); err != nil {
		return err
	}
	w.ack = saved
	return nil
}

func (w *checkpointWriter) replaceState(dir string, saved checkpointFileState) error {
	if err := w.check(dir, checkpointStateName); err != nil {
		return err
	}
	raw, err := json.Marshal(saved)
	if err != nil {
		return ErrPersistence
	}
	f, err := os.CreateTemp(dir, ".thread-*")
	if err != nil {
		return ErrPersistence
	}
	defer func() {
		if again, err := w.s.history.LogDir(w.id); err == nil && again == dir {
			_ = os.Remove(f.Name()) // best-effort cleanup inside the original directory
		}
	}()
	defer f.Close() // also close on a failed write/sync
	if err := f.Chmod(0600); err != nil {
		return ErrPersistence
	}
	if err := w.put("state-write", f, raw); err != nil {
		return err
	}
	if err := w.run("state-sync", f.Sync); err != nil {
		return err
	}
	if err := w.run("state-close", f.Close); err != nil {
		return err
	}
	if err := w.run("state-rename", func() error {
		if err := w.check(dir, checkpointStateName); err != nil {
			return err
		}
		return w.s.replace(f.Name(), filepath.Join(dir, checkpointStateName))
	}); err != nil {
		return err
	}
	return w.run("state-directory", func() error {
		if err := w.check(dir, checkpointStateName); err != nil {
			return err
		}
		return w.s.syncDir(dir)
	})
}
