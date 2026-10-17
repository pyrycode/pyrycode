package apps

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Lifecycle assertions need healthy services independent of accelerated expiry tests.
func healthLifecycleFixture(t *testing.T, reply healthReply) (*Registry, *Owner) {
	t.Helper()
	r, o := healthFixture(t, reply)
	o.startup = startupDeadline
	o.requestTimeout = healthRequestTimeout
	return r, o
}

func TestOwnerLifecycleFixtureDelays(t *testing.T) {
	for _, phase := range []string{"startup", "request"} {
		t.Run(phase, func(t *testing.T) {
			reply := healthReply{Body: healthyBody()}
			if phase == "startup" {
				reply.StartDelay = 900 * time.Millisecond
			} else {
				reply.BodyDelay = 200 * time.Millisecond
			}
			r, o := healthLifecycleFixture(t, reply)
			g := healthStart(t, r, o)
			target := healthTarget(t, o)
			if target.generation != g || g.ctx.Err() != nil {
				t.Fatal("fixture replaced the delayed healthy generation")
			}
			if err := healthUse(target); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestPublishFixturePreparationDelay(t *testing.T) {
	r, o, tools, _, _ := publishFixture(t, healthReply{Body: `{}`, Descendant: true}, "ok")
	// Delay installation, before migration or service readiness can exist.
	script := strings.Replace(string(testBytes(t, tools.Node)), "#!/bin/sh\n", "#!/bin/sh\nif [ \"$2\" = ci ]; then sleep 5.2; fi\n", 1)
	if err := os.WriteFile(tools.Node, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	done := publishFixtureStart(t, o, tools)
	leader, err := strconv.Atoi(string(publishFixtureReady(t, r, done)))
	if err != nil || leader < 1 {
		t.Fatal("invalid ready leader", leader, err)
	}
	desc, err := strconv.Atoi(string(waitFile(t, filepath.Join(r.root, testApp, "data/descendant"))))
	if err != nil || desc < 1 || syscall.Kill(desc, 0) != nil {
		t.Fatal("fixture did not establish a live descendant", desc, err)
	}
	if err := o.Stop(testApp); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err == nil {
		t.Fatal("retired private candidate activated")
	}
	waitGone(t, leader)
	waitGone(t, desc)
	if v := publishDisk(t, r).Records[0]; v.PublishJournal != nil || v.RetirementPending {
		t.Fatal("confirmed retirement retained uncertainty", v)
	}
}

func publishFixtureStart(t *testing.T, o *Owner, tools Toolchain) <-chan error {
	t.Helper()
	ctx, cancel := context.WithCancel(testCandidateDiagnostics(t))
	done := make(chan error, 1)
	go func() {
		_, err := o.Publish(ctx, testHost, testApp, tools)
		done <- err
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		_ = o.Shutdown()
		for range done {
		}
	})
	return done
}

// Readiness includes checked preparation and migration, not only child startup.
// An early failure must retain its phase instead of becoming a missing-file timeout.
func publishFixtureReady(t *testing.T, r *Registry, done <-chan error) []byte {
	t.Helper()
	deadline := time.NewTimer(time.Minute)
	defer deadline.Stop()
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case err := <-done:
			t.Fatalf("publication completed before fixture readiness: %v", err)
		case <-deadline.C:
			t.Fatal("publication fixture readiness timed out")
		case <-tick.C:
			b, err := os.ReadFile(filepath.Join(r.root, testApp, "data/ready"))
			if err == nil {
				return b
			}
			if !os.IsNotExist(err) {
				t.Fatal(err)
			}
		}
	}
}
