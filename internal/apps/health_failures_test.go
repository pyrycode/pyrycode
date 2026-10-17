package apps

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestOwnerHealthFailuresDelayedObserver(t *testing.T) {
	testOwnerHealthFailures(t, true)
}

func healthFailureState(o *Owner, g *generation) (int, string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return g.healthFailures, fmt.Sprintf("healthy=%t retired=%t done=%t current=%t state=%s context=%v", g.healthy, g.retired, generationDone(g), o.generations[testApp] == g, g.record.State, g.ctx.Err())
}

func healthFailureWait(t *testing.T, o *Owner, g *generation, expected int) {
	t.Helper()
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		observed, state := healthFailureState(o, g)
		if observed == expected {
			return
		}
		if generationDone(g) {
			t.Fatalf("expected failures=%d, observed=%d %s", expected, observed, state)
		}
		time.Sleep(5 * time.Millisecond)
	}
	observed, state := healthFailureState(o, g)
	t.Fatalf("health result timed out: expected failures=%d, observed=%d %s", expected, observed, state)
}

func testOwnerHealthFailures(t *testing.T, delayed bool) {
	t.Helper()
	r, o := healthLifecycleFixture(t, healthReply{Body: healthyBody(), StartDelay: 900 * time.Millisecond, BodyDelay: 200 * time.Millisecond})
	ticks := make(chan time.Time)
	o.healthTicks = ticks
	g := healthStart(t, r, o)
	target := healthTarget(t, o)
	probes := filepath.Join(r.root, testApp, "data/probes")
	probeCount := func() int {
		t.Helper()
		b, err := os.ReadFile(probes)
		if err != nil {
			t.Fatal(err)
		}
		return strings.Count(string(b), "\n")
	}
	baseline := probeCount()
	for step, expected := range []int{1, 2, 0, 1, 2, 3} {
		body := `{}`
		if expected == 0 {
			body = healthyBody()
		}
		healthWrite(t, r, healthReply{Body: body, BodyDelay: 200 * time.Millisecond})
		select {
		case ticks <- time.Now():
		case <-g.done:
			observed, state := healthFailureState(o, g)
			t.Fatalf("before probe: expected failures=%d, observed=%d %s", expected, observed, state)
		case <-time.After(4 * time.Second):
			observed, state := healthFailureState(o, g)
			t.Fatalf("probe advancement timed out: expected failures=%d, observed=%d %s", expected, observed, state)
		}
		if delayed && step == 0 {
			// Pause after completion so response delay cannot consume the idle window.
			healthFailureWait(t, o, g, expected)
			time.Sleep(4 * o.runtimeInterval)
		}
		healthFailureWait(t, o, g, expected)
		if got := probeCount(); got != baseline+step+1 {
			observed, state := healthFailureState(o, g)
			t.Fatalf("expected probes=%d, observed=%d; expected failures=%d, observed=%d %s", baseline+step+1, got, expected, observed, state)
		}
		t.Logf("released probe=%d completed failures=%d", step+1, expected)
		if expected < 3 {
			if err := healthUse(target); err != nil {
				observed, state := healthFailureState(o, g)
				t.Fatalf("target unavailable: expected failures=%d, observed=%d %s: %v", expected, observed, state, err)
			}
		}
	}
	select {
	case <-g.done:
	case <-time.After(4 * time.Second):
		observed, state := healthFailureState(o, g)
		t.Fatalf("retirement timed out: expected failures=3, observed=%d %s", observed, state)
	}
	if err := healthUse(target); err == nil {
		t.Fatal("third failure target survived")
	}
	if v := ownerRecord(t, r, testApp); v.State != "failed" {
		t.Fatal(v)
	}
	waitGone(t, g.cmd.Process.Pid)
}
