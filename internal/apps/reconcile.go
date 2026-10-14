package apps

import (
	"context"
	"errors"
	"time"
)

const healthyReset = 60 * time.Second

type retryState struct {
	delay      time.Duration
	due        time.Time
	suppressed bool
}

func nextRetry(previous time.Duration) time.Duration {
	if previous == 0 {
		return time.Second
	}
	return min(previous*2, 30*time.Second)
}
func (o *Owner) signal() {
	select {
	case o.wake <- struct{}{}:
	default:
	}
}
func generationDone(g *generation) bool {
	select {
	case <-g.done:
		return true
	default:
		return false
	}
}

// currentLocked requires the registry lock; the returned record is detached.
func (o *Owner) currentLocked(id string) Record {
	for _, r := range o.registry.snapshot.Records {
		if r.AppID == id {
			return cloneRecord(r)
		}
	}
	return Record{}
}
func (o *Owner) retryLocked(id string) *retryState {
	if o.retries[id] == nil {
		o.retries[id] = &retryState{}
	}
	return o.retries[id]
}

// admitLocked requires registry then owner locks. The worker owns preparation,
// conditional starting observation, spawn, health and complete group retirement.
func (o *Owner) admitLocked(ctx context.Context, r Record) *generation {
	work, cancel := context.WithCancel(ctx)
	g := &generation{ctx: work, cancel: cancel, record: r, exited: make(chan struct{}), ready: make(chan struct{}), done: make(chan struct{})}
	o.generations[r.AppID] = g
	o.retryLocked(r.AppID).due = time.Time{}
	go o.launch(work, r.AppID, g)
	return g
}
func (o *Owner) completed(id string, g *generation) {
	o.registry.mu.Lock()
	o.mu.Lock()
	r := o.currentLocked(id)
	if o.generations[id] == g && (g.publication == nil || g.publication.activated) && r.AppID != "" && (r.Desired == "stopped" || r.ActiveRelease == "") {
		_, _, err := o.registry.commitObservationLocked(r.Manifest.ServerID(), id, r.Revision, Observation{State: "stopped"})
		if err != nil {
			o.failures = errors.Join(o.failures, err)
		}
	}
	if o.generations[id] == g && !o.closed && !g.retired {
		retry := o.retryLocked(id)
		if !g.healthySince.IsZero() && o.now().Sub(g.healthySince) >= healthyReset {
			retry.delay = 0
		}
		retry.delay = nextRetry(retry.delay)
		retry.due = o.now().Add(retry.delay)
	}
	close(g.done)
	o.mu.Unlock()
	o.registry.mu.Unlock()
	o.signal()
}
func (o *Owner) reconcile() {
	defer close(o.reconciled)
	timer := time.NewTimer(time.Hour)
	defer timer.Stop()
	for {
		select {
		case <-o.reconcileCtx.Done():
			return
		case <-o.wake:
		case <-timer.C:
		}
		delay := o.reconcileSnapshot()
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(delay)
	}
}
func (o *Owner) reconcileSnapshot() time.Duration {
	rows, revision := o.registry.List()
	return o.reconcileRecords(rows, revision)
}
func (o *Owner) reconcileRecords(rows []Record, revision uint64) time.Duration {
	o.registry.mu.Lock()
	defer o.registry.mu.Unlock()
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed {
		return time.Hour
	}
	if revision != o.registry.snapshot.Revision {
		o.signal()
		return time.Hour
	}
	available := make(map[string]Record, len(rows))
	for _, r := range rows {
		if r.Desired == "available" && r.ActiveRelease != "" {
			available[r.AppID] = r
		}
	}
	for id, g := range o.generations {
		if o.publishing[id] != nil {
			continue
		}
		r, ok := available[id]
		if !ok || r.ActiveRelease != g.record.ActiveRelease {
			if !g.retired && !generationDone(g) {
				g.retired = true
				g.cancel()
			}
			if !ok {
				delete(o.retries, id)
			}
		}
	}
	delay := time.Hour
	now := o.now()
	for id, r := range available {
		if o.publishing[id] != nil || o.removing[id] || r.PublishJournal != nil {
			continue
		}
		g := o.generations[id]
		if g != nil && !generationDone(g) {
			continue
		}
		retry := o.retryLocked(id)
		if retry.suppressed {
			continue
		}
		if remaining := retry.due.Sub(now); remaining > 0 {
			delay = min(delay, remaining)
			continue
		}
		o.admitLocked(o.reconcileCtx, r)
	}
	return delay
}
func (o *Owner) rememberObservation(err error) {
	if !errors.Is(err, ErrStale) && !errors.Is(err, ErrRemoved) && !errors.Is(err, context.Canceled) {
		o.remember(err)
	}
}

// Failure observations preserve newer metadata but cannot alter another release,
// stopped intent or replacement generation.
func (o *Owner) observeFailure(id string, g *generation, message string) (Record, bool, error) {
	o.registry.mu.Lock()
	defer o.registry.mu.Unlock()
	o.mu.Lock()
	defer o.mu.Unlock()
	r := o.currentLocked(id)
	if o.generations[id] != g || r.Desired != "available" || r.ActiveRelease != g.record.ActiveRelease {
		return Record{}, false, ErrStale
	}
	return o.registry.commitObservationLocked(r.Manifest.ServerID(), id, r.Revision, Observation{State: "failed", LastError: &LastError{Code: "app.unavailable", Message: message}})
}
