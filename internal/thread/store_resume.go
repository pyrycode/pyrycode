package thread

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"syscall"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/history"
)

func (s *Store) initialPublicationIndex(items []*Item, fold *Fold, headers map[uint64]childHeader) *queryIndex {
	index := newPublicationIndex(items, s.queryWork)
	index.prepareCatchUp(fold)
	for _, item := range items {
		if item == nil {
			continue
		}
		row, _ := index.lookup(item.ID, true)
		if header, ok := headers[item.ID]; ok {
			row.header = header
		}
		index.refreshHeader(row, fold.version)
	}
	index.compactFrom = fold.version
	return index
}

// resumeCheckpoint validates history before permitting generation mutation.
// Only checkpoint state authorizes epoch reuse or continuation restoration.
func (s *Store) resumeCheckpoint(ctx context.Context, id conversations.ConversationID, bound uint64, source HistorySource) (*workerCheckpoint, cacheRecord, error) {
	fold, saved, err := s.readCheckpoint(id, source)
	if errors.Is(err, ErrPersistence) {
		return nil, cacheRecord{}, err
	}
	valid := err == nil && saved.Certificate.Version <= bound
	if valid && saved.Certificate.Version != 0 {
		entries, err := s.lookup(ctx, id, []uint64{saved.Certificate.Version})
		var missing *history.MissingEntriesError
		if err != nil && !errors.As(err, &missing) {
			return nil, cacheRecord{}, ErrUnavailable
		}
		valid = err == nil && len(entries) == 1 && entryChecksum(entries[0]) == saved.Certificate.Checksum
	}
	c := cacheRecord{}
	w := &workerCheckpoint{writer: checkpointWriter{s: s, id: id, source: source, fullRefold: !valid, step: s.checkpointStep}, pending: make(map[uint64]bool)}
	if !valid {
		fold = newReferenceFold(string(id), source)
	} else {
		c = saved.Certificate
		if !c.Clean {
			c.Epoch = ""
		}
		w.writer.ack, w.version = saved, saved.Certificate.Version
		_, rows, err := fold.ExportCheckpoint()
		if err != nil {
			return nil, c, ErrPersistence
		}
		w.ack = make(map[uint64][]byte, len(rows))
		for _, row := range rows {
			raw, err := json.Marshal(row)
			if err != nil {
				return nil, c, ErrPersistence
			}
			w.ack[row.Metadata.ID] = raw
		}
	}
	w.fold = fold
	return w, c, nil
}

func (w *checkpointWriter) reconcileResume() error {
	dir, err := w.s.history.LogDir(w.id)
	if err != nil {
		return ErrPersistence
	}
	saved := w.ack
	if err := w.check(dir, saved.Generation); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(dir, saved.Generation), os.O_RDWR|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return ErrPersistence
	}
	defer f.Close() // failed mutation still closes its descriptor
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() < saved.ValidLength {
		return ErrPersistence
	}
	if err := w.run("resume-truncate", func() error {
		if err := w.check(dir, saved.Generation); err != nil {
			return err
		}
		return f.Truncate(saved.ValidLength)
	}); err != nil {
		return err
	}
	if err := w.run("resume-sync", f.Sync); err != nil {
		return err
	}
	if err := w.run("resume-close", f.Close); err != nil {
		return err
	}
	return w.cleanGenerations(dir, saved.Generation)
}

// cleanGenerations preserves the surviving state's named generation. It never
// traverses directories or accepts redirects as disposable package leaves.
func (w *checkpointWriter) cleanGenerations(dir, generation string) error {
	var named checkpointFileState
	if err := w.s.readCacheFile(w.id, checkpointStateName, &named); err != nil || named.Generation != generation {
		return ErrPersistence
	}
	leaves, err := os.ReadDir(dir)
	if err != nil {
		return ErrPersistence
	}
	removed := false
	for _, leaf := range leaves {
		name := leaf.Name()
		if name == generation || !checkpointIndexLeaf(name) {
			continue
		}
		if err := w.check(dir, name); err != nil {
			return err
		}
		if err := os.Remove(filepath.Join(dir, name)); err != nil {
			return ErrPersistence
		}
		removed = true
	}
	if removed {
		return w.s.syncDir(dir)
	}
	return nil
}
