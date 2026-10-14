package apps

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
)

func TestUpdatePredecessorRetirementUncertain(t *testing.T) {
	for _, origin := range []string{"ordinary", "published"} {
		t.Run(origin, func(t *testing.T) {
			r, o, tools, c, _ := publishFixture(t, healthReply{Body: healthyBody(), Descendant: true}, "ok")
			var group atomic.Int64
			var durable atomic.Bool
			o.kill = func(pid int, sig syscall.Signal) error {
				if int64(pid) == group.Load() && sig != 0 {
					b, err := os.ReadFile(filepath.Join(r.root, "registry.json"))
					if err == nil && strings.Contains(string(b), `"retirement_pending":true`) {
						durable.Store(true)
					}
					// Join the leader while deliberately leaving its writer alive.
					_ = syscall.Kill(-pid, syscall.SIGKILL)
					return syscall.EPERM
				}
				return syscall.Kill(pid, sig)
			}
			if origin == "published" {
				if _, err := o.Publish(context.Background(), testHost, testApp, tools); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err := r.PrepareCandidate(context.Background(), testHost, testApp, tools); err != nil {
					t.Fatal(err)
				}
				if _, err := r.UpdateLifecycle(testHost, testApp, Lifecycle{Title: "App", ActiveRelease: "1.0.0", Desired: "available", State: "starting"}); err != nil {
					t.Fatal(err)
				}
				if err := o.Start(context.Background(), testApp); err != nil {
					t.Fatal(err)
				}
			}
			old := healthTarget(t, o)
			g := old.generation
			data := filepath.Join(r.root, testApp, "data")
			desc, err := strconv.Atoi(string(waitFile(t, filepath.Join(data, "descendant"))))
			if err != nil || desc < 1 {
				t.Fatal("invalid descendant", desc, err)
			}
			defer func() {
				_ = syscall.Kill(-g.cmd.Process.Pid, syscall.SIGKILL)
				waitGone(t, desc)
			}()
			testArtifactWrite(t, r.root, testApp+"/data/preference", "saved")
			group.Store(int64(-g.cmd.Process.Pid))
			updateCandidate(t, r, c, "2.0.0", "ok", strings.Replace(healthyBody(), "1.0.0", "2.0.0", 1))
			result, err := o.Publish(context.Background(), testHost, testApp, tools)
			if !durable.Load() {
				t.Error("predecessor signalled without durable retirement evidence")
			}
			if err == nil || !result.RecoveryPending || result.Healthy || result.ActivationCommitted {
				t.Errorf("uncertain retirement outcome: %+v, %v", result, err)
			}
			<-g.done
			waitGone(t, g.cmd.Process.Pid)
			if err := syscall.Kill(desc, 0); origin == "ordinary" && err != nil {
				t.Fatal("live descendant uncertainty not exercised", err)
			}
			v := publishDisk(t, r).Records[0]
			if v.PublishJournal != nil || v.State != "failed" || v.LastError == nil || v.LastError.Code != "app.io_failed" || v.ActiveRelease != "1.0.0" || v.PendingRelease != "2.0.0" {
				t.Errorf("pre-snapshot evidence lost: %+v", v)
			}
			if !strings.Contains(string(testBytes(t, filepath.Join(r.root, "registry.json"))), `"retirement_pending":true`) {
				t.Error("missing durable retirement barrier")
			}
			if healthUse(old) == nil {
				t.Error("old target survived retirement")
			}
			log := string(testBytes(t, c.Log))
			if _, err := o.Publish(context.Background(), testHost, testApp, tools); err == nil || string(testBytes(t, c.Log)) != log {
				t.Error("publication checked another candidate while writer remained live")
			}
			if err := o.Start(context.Background(), testApp); !errors.Is(err, ErrConflict) {
				t.Error("start bypassed uncertain retirement", err)
			}
			o.reconcileSnapshot()
			if recoveryGeneration(o, testApp) != g {
				t.Error("uncertain predecessor was replaced")
			}
			if _, err := o.Lookup(testHost, testApp); err == nil {
				t.Error("target exposed beneath live writer")
			}
			if removed, err := r.Remove(testApp); removed || err == nil {
				t.Error("removal discarded retirement evidence", removed, err)
			}
			_ = o.Shutdown() // The injected retirement failure is retained by shutdown.
			reopened, err := Open(r.root, testHost)
			if err != nil {
				t.Fatal(err)
			}
			if removed, err := reopened.Remove(testApp); removed || !errors.Is(err, ErrConflict) {
				t.Error("ownerless removal discarded evidence", removed, err)
			}
			next, err := NewOwner(reopened, tools.Node)
			if err != nil {
				t.Fatal(err)
			}
			defer next.Shutdown()
			next.reconcileSnapshot()
			if err := next.Start(context.Background(), testApp); !errors.Is(err, ErrConflict) {
				t.Error("reopened owner bypassed uncertain retirement", err)
			}
			if _, err := next.Publish(context.Background(), testHost, testApp, tools); err == nil || string(testBytes(t, c.Log)) != log {
				t.Error("reopened publication bypassed recovery")
			}
			if string(testBytes(t, filepath.Join(data, "preference"))) != "saved" {
				t.Error("uncertain retirement mutated data")
			}
		})
	}
}

func TestUpdateRetirementStorage(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value any
		valid bool
	}{
		{"pending", true, true}, {"clear", false, true},
		{"null", nil, false}, {"string", "true", false}, {"number", 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			document := testStorageDocument(t)
			record := document["records"].([]any)[0].(map[string]any)
			record["retirement_pending"] = tc.value
			testStorageOpen(t, testJSON(t, document), testHost, tc.valid)
			record["RETIREMENT_PENDING"] = false
			testStorageOpen(t, testJSON(t, document), testHost, false)
		})
	}
}

func TestUpdateRecoveredCompletion(t *testing.T) {
	for _, event := range []string{"shutdown", "stopped intent"} {
		t.Run(event, func(t *testing.T) {
			r, o, tools, c, _ := updateFixture(t)
			updateCandidate(t, r, c, "2.0.0", "fail", `{}`)
			result, err := o.Publish(context.Background(), testHost, testApp, tools)
			if err == nil || result.RecoveryPending || result.Lifecycle.State != "running" {
				t.Fatal("recovery did not complete", result, err)
			}
			target := healthTarget(t, o)
			before := ownerRecord(t, r, testApp)
			if event == "shutdown" {
				if err := o.Shutdown(); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, _, err := r.CommitObservation(testHost, testApp, before.Revision, Observation{Desired: "stopped"}); err != nil {
					t.Fatal(err)
				}
				<-target.generation.done
			}
			v := publishDisk(t, r).Records[0]
			if v.State != "stopped" || v.ActiveRelease != "1.0.0" || v.PublishJournal != nil || v.LastError == nil || *v.LastError != *before.LastError {
				t.Fatalf("recovered completion lost state/error: %+v", v)
			}
			if event == "shutdown" && v.Desired != "available" || event == "stopped intent" && v.Desired != "stopped" {
				t.Fatal("completion changed intent", v)
			}
			waitGone(t, target.generation.cmd.Process.Pid)
			if healthUse(target) == nil {
				t.Fatal("completed recovery target remained usable")
			}
		})
	}
}
