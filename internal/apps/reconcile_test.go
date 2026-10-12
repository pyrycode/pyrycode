package apps

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func recoveryGeneration(o *Owner, id string) *generation {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.generations[id]
}
func recoveryAvailable(t *testing.T, r *Registry, id string) {
	t.Helper()
	v := ownerRecord(t, r, id)
	if _, e := r.UpdateLifecycle(testHost, id, Lifecycle{Title: v.Title, Desired: "available", State: "starting", ActiveRelease: v.ActiveRelease}); e != nil {
		t.Fatal(e)
	}
}
func TestOwnerReconcileSnapshot(t *testing.T) {
	r, o := healthFixture(t, healthReply{Body: healthyBody()})
	recoveryAvailable(t, r, testApp)
	healthTarget(t, o)
	g := recoveryGeneration(o, testApp)
	if e := o.Start(context.Background(), testApp); e != nil {
		t.Fatal(e)
	}
	if recoveryGeneration(o, testApp) != g {
		t.Fatal("duplicate start")
	}
	v := ownerRecord(t, r, testApp)
	if _, e := r.UpdateLifecycle(testHost, testApp, Lifecycle{Title: v.Title, Desired: "stopped", State: "running", ActiveRelease: v.ActiveRelease}); e != nil {
		t.Fatal(e)
	}
	select {
	case <-g.done:
	case <-time.After(time.Second):
		t.Fatal("stop notification did not cancel")
	}
	waitGone(t, g.cmd.Process.Pid)
}
func TestOwnerRecovery(t *testing.T) {
	r, o := healthFixture(t, healthReply{Body: healthyBody()})
	g := healthStart(t, r, o)
	old := healthTarget(t, o)
	exit := filepath.Join(r.root, testApp, "data/exit")
	if e := os.WriteFile(exit, nil, 0600); e != nil {
		t.Fatal(e)
	}
	<-g.done
	if e := os.Remove(exit); e != nil {
		t.Fatal(e)
	}
	healthWait(t, func() bool { return recoveryGeneration(o, testApp) != g })
	fresh := healthTarget(t, o)
	next := recoveryGeneration(o, testApp)
	if next.port == g.port || next.cmd.Process.Pid == g.cmd.Process.Pid {
		t.Fatal("identity reused")
	}
	if e := healthUse(old); e == nil {
		t.Fatal("retired target survived")
	}
	if e := healthUse(fresh); e != nil {
		t.Fatal(e)
	}
}

// The clock changes only under the owner lock, as do production clock reads.
func recoveryAdvance(o *Owner, d time.Duration) {
	o.mu.Lock()
	next := o.now().Add(d)
	o.now = func() time.Time { return next }
	o.mu.Unlock()
	o.signal()
}
func TestOwnerRetryTimings(t *testing.T) {
	r, o := ownerFixture(t)
	if healthyReset != 60*time.Second {
		t.Fatal("reset changed")
	}
	o.mu.Lock()
	now := time.Now()
	o.now = func() time.Time { return now }
	o.mu.Unlock()
	if e := o.Start(context.Background(), testApp); e == nil {
		t.Fatal("missing release accepted")
	}
	g := recoveryGeneration(o, testApp)
	for _, delay := range []time.Duration{1, 2, 4, 8, 16, 30, 30} {
		<-g.done
		o.mu.Lock()
		retry := *o.retryLocked(testApp)
		got := retry.due.Sub(o.now())
		o.mu.Unlock()
		if got != delay*time.Second || retry.delay != got {
			t.Fatal("retry", got, delay, retry)
		}
		if e := o.Start(context.Background(), testApp); e == nil {
			t.Fatal("duplicate start bypassed failure")
		}
		v := ownerRecord(t, r, testApp)
		if _, e := r.UpdateLifecycle(testHost, testApp, Lifecycle{Title: "Metadata", Desired: v.Desired, State: v.State, ActiveRelease: v.ActiveRelease, PendingRelease: "2.0.0", LastError: v.LastError}); e != nil {
			t.Fatal(e)
		}
		recoveryAdvance(o, delay*time.Second-time.Nanosecond)
		o.reconcileSnapshot()
		if recoveryGeneration(o, testApp) != g {
			t.Fatal("early retry")
		}
		recoveryAdvance(o, time.Nanosecond)
		healthWait(t, func() bool { return recoveryGeneration(o, testApp) != g })
		g = recoveryGeneration(o, testApp)
	}
	if e := o.Stop(testApp); e != nil {
		t.Fatal(e)
	}
	recoveryAdvance(o, time.Hour)
	o.reconcileSnapshot()
	if recoveryGeneration(o, testApp) != g {
		t.Fatal("stopped retry")
	}
}
func TestOwnerReconcileMetadata(t *testing.T) {
	r, o := healthFixture(t, healthReply{Body: healthyBody()})
	g := healthStart(t, r, o)
	target := healthTarget(t, o)
	o.mu.Lock()
	o.retryLocked(testApp).delay = 16 * time.Second
	o.mu.Unlock()
	recoveryAdvance(o, 59*time.Second)
	o.reconcileSnapshot()
	o.mu.Lock()
	if o.retryLocked(testApp).delay != 16*time.Second {
		t.Error("early reset")
	}
	o.mu.Unlock()
	v := ownerRecord(t, r, testApp)
	r.mu.Lock()
	o.mu.Lock()
	// Observation callbacks execute while both production locks are already held.
	_, err := r.changeLocked("lifecycle", testApp, nil, &lifecycleUpdate{serverID: testHost, fields: Lifecycle{Title: "Latest", Desired: v.Desired, State: v.State, ActiveRelease: v.ActiveRelease, PendingRelease: "2.0.0"}})
	o.mu.Unlock()
	r.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}

	m := testMap(t)
	m["title"] = "Candidate"
	m["release_version"] = "3.0.0"
	if _, e := r.UpdateManifest(testApp, testJSON(t, m)); e != nil {
		t.Fatal(e)
	}
	o.reconcileSnapshot()
	if recoveryGeneration(o, testApp) != g {
		t.Fatal("metadata replaced healthy process")
	}
	if e := healthUse(target); e != nil {
		t.Fatal(e)
	}
	recoveryAdvance(o, 2*time.Second)
	healthWait(t, func() bool { o.mu.Lock(); defer o.mu.Unlock(); return o.retryLocked(testApp).delay == 0 })
	healthWrite(t, r, healthReply{Body: `{}`})
	<-g.done
	o.mu.Lock()
	delay := o.retryLocked(testApp).delay
	o.mu.Unlock()
	if delay != time.Second {
		t.Fatal("reset not applied", delay)
	}
	latest := ownerRecord(t, r, testApp)
	if latest.Title != "Latest" || latest.PendingRelease != "2.0.0" || latest.ActiveRelease != "1.0.0" || latest.State != "failed" {
		t.Fatal(latest)
	}
}
func TestOwnerReconcileCancellation(t *testing.T) {
	for _, event := range []string{"startup stop", "startup remove", "backoff stop", "backoff remove"} {
		t.Run(event, func(t *testing.T) {
			r, o := healthFixture(t, healthReply{Body: `{}`, BodyDelay: time.Second})
			g := healthStart(t, r, o)
			if event == "backoff stop" || event == "backoff remove" {
				<-g.done
			}
			rows, rev := r.List()
			started := time.Now()
			if event == "startup remove" || event == "backoff remove" {
				if _, e := r.Remove(testApp); e != nil {
					t.Fatal(e)
				}
			} else {
				if e := o.Stop(testApp); e != nil {
					t.Fatal(e)
				}
			}
			select {
			case <-g.done:
			case <-time.After(time.Second):
				t.Fatal("cancellation waited for probe/retry")
			}
			if time.Since(started) > 500*time.Millisecond {
				t.Fatal("slow cancellation")
			}
			waitGone(t, g.cmd.Process.Pid)
			recoveryAdvance(o, time.Hour)
			o.reconcileRecords(rows, rev)
			o.reconcileSnapshot()
			if recoveryGeneration(o, testApp) != g {
				t.Fatal("stale snapshot resurrected retired identity")
			}
		})
	}
}

func TestOwnerReopenRecovery(t *testing.T) {
	r, o := healthFixture(t, healthReply{Body: healthyBody()})
	g := healthStart(t, r, o)
	old := healthTarget(t, o)
	stopped, unpublished := testID(8), testID(9)
	for _, id := range []string{stopped, unpublished} {
		testRegister(t, r, testInput(id))
	}
	v := ownerRecord(t, r, stopped)
	if _, e := r.UpdateLifecycle(testHost, stopped, Lifecycle{Title: v.Title, Desired: "stopped", State: "stopped", ActiveRelease: "1.0.0"}); e != nil {
		t.Fatal(e)
	}
	db := filepath.Join(r.root, testApp, "data/app.sqlite")
	if e := os.WriteFile(db, []byte("surviving data"), 0600); e != nil {
		t.Fatal(e)
	}
	if e := o.Shutdown(); e != nil {
		t.Fatal(e)
	}
	waitGone(t, g.cmd.Process.Pid)
	if ownerRecord(t, r, testApp).Desired != "available" {
		t.Fatal("shutdown changed intent")
	}
	r.mu.Lock()
	count := len(r.consumers)
	r.mu.Unlock()
	if count != 0 {
		t.Fatal("subscription retained", count)
	}
	reopened, e := Open(r.root, testHost)
	if e != nil {
		t.Fatal(e)
	}
	// A large reply delay proves construction and spawn do not wait for readiness.
	build := filepath.Join(r.root, testApp, "builds", strings.Repeat("a", 64), "dist/service/main.js")
	b, _ := json.Marshal(healthReply{Body: healthyBody(), BodyDelay: 300 * time.Millisecond})
	if e = os.WriteFile(build, b, 0600); e != nil {
		t.Fatal(e)
	}
	before := time.Now()
	next, e := NewOwner(reopened, o.node)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		if e := next.Shutdown(); e != nil {
			t.Error(e)
		}
	})
	if time.Since(before) > 100*time.Millisecond {
		t.Fatal("constructor waited for service")
	}
	fresh := healthTarget(t, next)
	replacement := recoveryGeneration(next, testApp)
	if replacement.cmd.Process.Pid == g.cmd.Process.Pid || replacement == g {
		t.Fatal("old ownership resumed")
	}
	if e := healthUse(old); e == nil {
		t.Fatal("old target survived transfer")
	}
	if e := healthUse(fresh); e != nil {
		t.Fatal(e)
	}
	if string(testBytes(t, db)) != "surviving data" {
		t.Fatal("data lost")
	}
	if recoveryGeneration(next, stopped) != nil || recoveryGeneration(next, unpublished) != nil {
		t.Fatal("stopped/unpublished launched")
	}
}
func TestOwnerRecoveryIsolation(t *testing.T) {
	r, o := healthFixture(t, healthReply{Body: healthyBody()})
	sibling := testID(8)
	testRegister(t, r, testInput(sibling))
	body := strings.Replace(healthyBody(), testApp, sibling, 1)
	b, _ := json.Marshal(healthReply{Body: body})
	ownerBuild(t, r, sibling, "a", "1.0.0", string(b))
	v := ownerRecord(t, r, sibling)
	if _, e := r.UpdateLifecycle(testHost, sibling, Lifecycle{Title: v.Title, Desired: "available", State: "starting", ActiveRelease: "1.0.0"}); e != nil {
		t.Fatal(e)
	}
	var target *Target
	healthWait(t, func() bool { var e error; target, e = o.Lookup(testHost, sibling); return e == nil })
	g := healthStart(t, r, o)
	healthTarget(t, o)
	healthWrite(t, r, healthReply{Body: `{}`})
	<-g.done
	// Repair the helper's next-generation service response without modifying registry intent.
	b, _ = json.Marshal(healthReply{Body: healthyBody()})
	if e := os.WriteFile(filepath.Join(r.root, testApp, "builds", strings.Repeat("a", 64), "dist/service/main.js"), b, 0600); e != nil {
		t.Fatal(e)
	}
	recoveryAdvance(o, time.Second)
	healthWait(t, func() bool { return recoveryGeneration(o, testApp) != g })
	healthTarget(t, o)
	if e := healthUse(target); e != nil {
		t.Fatal("sibling interrupted", e)
	}
	if e := o.Stop(testApp); e != nil {
		t.Fatal(e)
	}
	if e := healthUse(target); e != nil {
		t.Fatal("sibling stopped", e)
	}
}
func TestOwnerRecoveryPersistence(t *testing.T) {
	r, o := healthFixture(t, healthReply{Body: healthyBody()})
	failure := errors.New("running persistence unavailable")
	r.mu.Lock()
	r.persist = func(root string, s snapshot) error {
		if s.Records[0].State == "running" {
			return failure
		}
		return writeSnapshot(root, s)
	}
	r.mu.Unlock()
	g := healthStart(t, r, o)
	<-g.done
	waitGone(t, g.cmd.Process.Pid)
	if _, e := o.Lookup(testHost, testApp); e == nil {
		t.Fatal("failed write exposed target")
	}
	r.mu.Lock()
	r.persist = writeSnapshot
	r.mu.Unlock()
	recoveryAdvance(o, time.Second)
	healthWait(t, func() bool { return recoveryGeneration(o, testApp) != g })
	healthTarget(t, o)
	if e := o.Shutdown(); !errors.Is(e, failure) {
		t.Fatal("lost asynchronous failure", e)
	}
	o.mu.Lock()
	o.failures = nil
	o.mu.Unlock()
}

func TestOwnerRecoveryCleanupBackoff(t *testing.T) {
	r, o := healthFixture(t, healthReply{Body: healthyBody()})
	g := healthStart(t, r, o)
	healthTarget(t, o)
	entered, release := make(chan struct{}), make(chan struct{})
	o.mu.Lock()
	kill := o.kill
	o.kill = func(pid int, sig syscall.Signal) error {
		if sig == syscall.SIGTERM {
			close(entered)
			<-release
		}
		return kill(pid, sig)
	}
	o.mu.Unlock()
	healthWrite(t, r, healthReply{Body: `{}`})
	<-entered
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	if e := o.Start(ctx, testApp); !errors.Is(e, context.DeadlineExceeded) {
		t.Error("duplicate start during retirement", e)
	}
	cancel()
	o.mu.Lock()
	if !o.retryLocked(testApp).due.IsZero() {
		t.Error("retry scheduled before cleanup")
	}
	o.mu.Unlock()
	close(release)
	<-g.done
	o.mu.Lock()
	o.kill = kill
	due := o.retryLocked(testApp).due
	delay := o.retryLocked(testApp).delay
	o.mu.Unlock()
	if time.Until(due) < 900*time.Millisecond || delay != time.Second {
		t.Fatal("cleanup backoff bypassed", due, delay)
	}
}
func TestOwnerRecoveryStartingWrite(t *testing.T) {
	r, o := healthFixture(t, healthReply{Body: healthyBody()})
	failure := errors.New("starting persistence unavailable")
	r.mu.Lock()
	o.mu.Lock()
	r.persist = func(root string, s snapshot) error {
		if s.Records[0].State == "starting" {
			return failure
		}
		return writeSnapshot(root, s)
	}
	_, e := r.changeLocked("lifecycle", testApp, nil, &lifecycleUpdate{serverID: testHost, fields: Lifecycle{Title: "App", Desired: "available", State: "failed", ActiveRelease: "1.0.0"}})
	o.mu.Unlock()
	r.mu.Unlock()
	if e != nil {
		t.Fatal(e)
	}
	healthWait(t, func() bool { return recoveryGeneration(o, testApp) != nil })
	g := recoveryGeneration(o, testApp)
	<-g.done
	if !errors.Is(g.err, failure) || g.cmd != nil {
		t.Fatal("starting failure spawned or disappeared", g.err)
	}
	o.mu.Lock()
	delay := o.retryLocked(testApp).delay
	o.mu.Unlock()
	if delay != time.Second {
		t.Fatal("starting write spun", delay)
	}
	r.mu.Lock()
	r.persist = writeSnapshot
	r.mu.Unlock()
	recoveryAdvance(o, time.Second)
	healthWait(t, func() bool { return recoveryGeneration(o, testApp) != g })
	healthTarget(t, o)
	if e := o.Shutdown(); !errors.Is(e, failure) {
		t.Fatal(e)
	}
	o.mu.Lock()
	o.failures = nil
	o.mu.Unlock()
}

func TestOwnerRetryResetOnExit(t *testing.T) {
	for _, healthyFor := range []time.Duration{59 * time.Second, 60 * time.Second} {
		t.Run(healthyFor.String(), func(t *testing.T) {
			r, o := healthFixture(t, healthReply{Body: healthyBody()})
			// No later successful probe can provide the reset: retirement must account
			// for continuous confirmed running when an exit precedes the next probe.
			o.runtimeInterval = time.Hour
			g := healthStart(t, r, o)
			healthTarget(t, o)
			o.mu.Lock()
			o.retryLocked(testApp).delay = 16 * time.Second
			o.mu.Unlock()
			recoveryAdvance(o, healthyFor)
			if e := os.WriteFile(filepath.Join(r.root, testApp, "data/exit"), nil, 0600); e != nil {
				t.Fatal(e)
			}
			<-g.done
			o.mu.Lock()
			delay := o.retryLocked(testApp).delay
			o.mu.Unlock()
			want := 30 * time.Second
			if healthyFor == 60*time.Second {
				want = time.Second
			}
			if delay != want {
				t.Fatal("exit reset", delay, want)
			}
		})
	}
}
