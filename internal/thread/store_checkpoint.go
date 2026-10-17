package thread

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/history"
)

const checkpointInterval = 30 * time.Second

type checkpointTimer struct {
	c     <-chan time.Time
	reset func()
	stop  func()
}

func newCheckpointTimer() checkpointTimer {
	t := time.NewTimer(checkpointInterval)
	return checkpointTimer{t.C, func() { t.Reset(checkpointInterval) }, func() { t.Stop() }}
}

type workerCheckpoint struct {
	writer  checkpointWriter
	parent  context.Context
	fold    *Fold
	ack     map[uint64][]byte
	pending map[uint64]bool
	version uint64
}

func (s *Store) dirtyCheckpoint(id conversations.ConversationID) error {
	var saved checkpointFileState
	if err := s.readCacheFile(id, checkpointStateName, &saved); err != nil {
		if errors.Is(err, errCacheAbsent) {
			return nil
		}
		return err
	}
	saved.Certificate.Clean = false
	dir, err := s.history.LogDir(id)
	if err != nil {
		return ErrPersistence
	}
	w := checkpointWriter{s: s, id: id, step: s.checkpointStep}
	return w.replaceState(dir, saved)
}

// invalidate removes only contained regular evidence leaves. A failed final
// transaction must not leave either surface authorizing clean recovery.
func (w *workerCheckpoint) invalidate() {
	dir, err := w.writer.s.history.LogDir(w.writer.id)
	if err != nil {
		return
	}
	for _, name := range []string{checkpointStateName, cacheName} {
		if w.writer.check(dir, name) == nil {
			_ = os.Remove(filepath.Join(dir, name)) // best-effort invalidation
		}
	}
	_ = w.writer.s.syncDir(dir) // best-effort durability after failed certification
}

func (w *workerCheckpoint) commit(snapshot Snapshot, checksum string, clean bool) (result error) {
	defer func() {
		if result != nil && clean {
			w.invalidate()
		}
	}()
	if clean && w.parent != nil && w.parent.Err() != nil {
		return ErrUnavailable
	}
	d, all, err := w.fold.ExportCheckpoint()
	if err != nil || d.Version != snapshot.Version {
		return ErrPersistence
	}
	latest := make(map[uint64][]byte, len(all))
	var changed []CheckpointItem
	var liveBytes int64
	for _, row := range all {
		raw, err := json.Marshal(row)
		if err != nil {
			return ErrPersistence
		}
		id := row.Metadata.ID
		latest[id] = raw
		liveBytes += int64(len(raw) + checkpointFrameHeader)
		if !bytes.Equal(w.ack[id], raw) {
			w.pending[id] = true
		}
		if w.pending[id] {
			changed = append(changed, row)
		}
	}
	c := cacheRecord{Schema: cacheSchema, Rules: foldingRules, Epoch: snapshot.Epoch, Version: snapshot.Version, LastShownVersion: snapshot.LastShownVersion, Checksum: checksum, Clean: clean}
	if err := w.writer.commit(d, all, changed, c); err != nil {
		return ErrPersistence
	}
	if clean {
		if w.parent != nil && w.parent.Err() != nil {
			return ErrUnavailable
		}
		if err := w.writer.s.writeCacheFile(w.writer.id, cacheName, c); err != nil {
			return ErrPersistence
		}
		if w.parent != nil && w.parent.Err() != nil {
			return ErrUnavailable
		}
	}
	w.ack, w.version = latest, snapshot.Version
	clear(w.pending)
	raw, err := json.Marshal(w.writer.ack)
	if err != nil {
		return ErrPersistence
	}
	w.writer.s.log.Info("thread checkpoint committed", "live_record_bytes", liveBytes, "committed_record_bytes", w.writer.ack.ValidLength, "state_rewrite_bytes", len(raw))
	return nil
}

// tailCheckpoints keeps Fold access on the worker. The forwarder borrows one
// bounded chunk until its callback is acknowledged and is always joined.
func (s *Store) tailCheckpoints(ctx context.Context, reader storeReader, checkpoint *workerCheckpoint, progress func() (Snapshot, string), feed func([]history.Entry) error) error {
	tailCtx, cancel := context.WithCancel(ctx)
	batches := make(chan []history.Entry)
	acks := make(chan error)
	done := make(chan error, 1)
	go func() {
		done <- reader.Tail(tailCtx, func(entries []history.Entry) error {
			select {
			case batches <- entries:
			case <-tailCtx.Done():
				return tailCtx.Err()
			}
			select {
			case err := <-acks:
				return err
			case <-tailCtx.Done():
				return tailCtx.Err()
			}
		})
	}()
	timer := s.newTimer()
	defer timer.stop()
	joined := false
	defer func() {
		cancel()
		if !joined {
			<-done
		}
	}()
	tick := func() {
		snapshot, checksum := progress()
		if ctx.Err() == nil && snapshot.Version != checkpoint.version {
			if err := checkpoint.commit(snapshot, checksum, false); err != nil {
				s.log.Warn("thread checkpoint failed")
			}
		}
		timer.reset()
	}
	for {
		// Prefer an elapsed deadline to another ready batch during busy tails.
		select {
		case <-timer.c:
			tick()
		default:
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-done:
			joined = true
			return err
		case <-timer.c:
			tick()
		case entries := <-batches:
			err := feed(entries)
			select {
			case acks <- err:
			case <-tailCtx.Done():
			}
			if err != nil {
				return err
			}
		}
	}
}
