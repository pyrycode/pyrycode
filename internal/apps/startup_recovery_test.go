package apps

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

const otherApp = "33333333-3333-4333-8333-333333333333"

// Interrupt the durable publication path without permitting same-owner rollback.
func interruptedPublication(t *testing.T, first bool, phase string) (*Registry, Toolchain) {
	t.Helper()
	return interruptedPublicationReply(t, first, phase, healthReply{Body: healthyBody(), API: true})
}

func interruptedPublicationReply(t *testing.T, first bool, phase string, reply healthReply) (*Registry, Toolchain) {
	t.Helper()
	r, o, tools, c, _ := publishFixture(t, reply, "ok")
	if reply.HoldHealth {
		testArtifactWrite(t, r.root, testApp+"/data/health-release", "")
	}
	if !first {
		if _, err := o.Publish(context.Background(), testHost, testApp, tools); err != nil {
			t.Fatal(err)
		}
		testArtifactWrite(t, r.root, testApp+"/data/preference", "saved")
		updateCandidate(t, r, c, "2.0.0", "ok", healthyBody())
	}
	r.mu.Lock()
	r.persist = func(root string, s snapshot) error {
		v := s.Records[0]
		if v.PublishJournal != nil && !v.PublishJournal.ActivationCommitted && phase == "journal" {
			if err := writeSnapshot(root, s); err != nil {
				return err
			}
			return errSnapshotUncertain
		}
		if v.PublishJournal != nil && v.PublishJournal.ActivationCommitted && phase == "migration" {
			return errSnapshotUncertain
		}
		return writeSnapshot(root, s)
	}
	r.mu.Unlock()
	// Candidate matching health is needed to reach the activation write.
	if !first && phase == "migration" {
		updateCandidate(t, r, c, "2.0.0", "ok", `{"ready":true,"app_id":"`+testApp+`","release_version":"2.0.0"}`)
	}
	if _, err := o.Publish(context.Background(), testHost, testApp, tools); err == nil {
		t.Fatal("interruption absent")
	}
	_ = o.Shutdown()
	if v := publishDisk(t, r).Records[0]; v.PublishJournal == nil || v.PublishJournal.ActivationCommitted {
		t.Fatal(v)
	}
	return r, tools
}

func TestStartupRecoveryLateHealth(t *testing.T) {
	r, tools := interruptedPublicationReply(t, false, "migration", healthReply{Body: healthyBody(), HoldHealth: true})
	fresh, err := Open(r.root, testHost)
	if err != nil {
		t.Fatal(err)
	}
	o, err := newOwner(fresh, tools.Node, backupOps{hook: func(step string) error {
		if step == "restored" {
			for _, name := range []string{"health-release", "health-blocked"} {
				if e := os.Remove(filepath.Join(r.root, testApp, "data", name)); e != nil && !os.IsNotExist(e) {
					return e
				}
			}
		}
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer o.Shutdown()
	waitFile(t, filepath.Join(r.root, testApp, "data/health-blocked"))
	assertRecoveryBarred(t, o, "1.0.0")
	done := make(chan error, 1)
	go func() { done <- o.Stop(testApp) }()
	select {
	case err = <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("stop did not cancel private health")
	}
	v := ownerRecord(t, fresh, testApp)
	if v.Desired != "stopped" || v.State != "stopped" || v.PublishJournal == nil {
		t.Fatal(v)
	}
	assertRecoveryBarred(t, o, "1.0.0")
	if e := o.Shutdown(); e != nil {
		t.Fatal(e)
	}
}

func TestStartupRecoveryExhaustion(t *testing.T) {
	r, tools := interruptedPublication(t, false, "journal")
	s := publishDisk(t, r)
	s.Revision = maxRevision
	s.Records[0].Revision = maxRevision
	if e := writeSnapshot(r.root, s); e != nil {
		t.Fatal(e)
	}
	fresh, o := reopenPublication(t, r, tools.Node)
	waitRecoveryWorker(t, o)
	assertRecoveryBarred(t, o, "1.0.0")
	_, revision := fresh.List()
	if v := publishDisk(t, fresh); revision != maxRevision || v.Revision != maxRevision || v.Records[0].PublishJournal == nil {
		t.Fatal(v)
	}
}

func TestStartupRecoveryValidatorRetirement(t *testing.T) {
	r, tools := interruptedPublication(t, false, "journal")
	fresh, err := Open(r.root, testHost)
	if err != nil {
		t.Fatal(err)
	}
	v := ownerRecord(t, fresh, testApp)
	if _, err = fresh.UpdateLifecycle(testHost, testApp, Lifecycle{Title: v.Title, Desired: "stopped", State: "stopped", ActiveRelease: v.ActiveRelease, PendingRelease: v.PendingRelease}); err != nil {
		t.Fatal(err)
	}
	script := strings.Replace(string(testBytes(t, tools.Node)), "#!/bin/sh\n", "#!/bin/sh\nif [ \"$1\" = --input-type=module ]; then : > \"$PYRY_APP_DATA_DIR/validating\"; /bin/sleep 20 & wait; fi\n", 1)
	if err = os.WriteFile(tools.Node, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unpark := func() { once.Do(func() { close(release) }) }
	o, err := newOwner(fresh, tools.Node, backupOps{hook: func(step string) error {
		if step == "restored" {
			close(entered)
			<-release
		}
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer o.Shutdown()
	defer unpark()
	<-entered
	o.kill = func(pid int, sig syscall.Signal) error {
		if sig != 0 {
			return syscall.EPERM
		}
		return syscall.Kill(pid, sig)
	}
	unpark()
	waitFile(t, filepath.Join(r.root, testApp, "data/validating"))
	if err = o.Stop(testApp); !errors.Is(err, syscall.EPERM) {
		t.Fatal("lost validator retirement failure", err)
	}
	v = ownerRecord(t, fresh, testApp)
	if !v.RetirementPending || v.PublishJournal == nil || v.Desired != "stopped" {
		t.Fatal(v)
	}
	if err = o.Shutdown(); !errors.Is(err, syscall.EPERM) {
		t.Fatal(err)
	}
	reopened, next := reopenPublication(t, fresh, tools.Node)
	assertRecoveryBarred(t, next, "1.0.0")
	if got := ownerRecord(t, reopened, testApp); !got.RetirementPending || got.PublishJournal == nil {
		t.Fatal(got)
	}
}

func waitRecoveryWorker(t *testing.T, o *Owner) {
	t.Helper()
	healthWait(t, func() bool { o.mu.Lock(); defer o.mu.Unlock(); return o.publishing[testApp] == nil })
}

func assertRecoveryBarred(t *testing.T, o *Owner, release string) {
	t.Helper()
	if _, err := o.Lookup(testHost, testApp); err == nil {
		t.Fatal("API target exposed")
	}
	if _, err := o.PrepareAsset(context.Background(), testHost, testApp, release, AssetRequest{}); err == nil {
		t.Fatal("asset target exposed")
	}
	if err := o.Start(context.Background(), testApp); !errors.Is(err, ErrConflict) {
		t.Fatal("start admitted", err)
	}
	if _, err := o.Publish(context.Background(), testHost, testApp, Toolchain{}); err == nil {
		t.Fatal("publish admitted")
	}
}

func TestStartupRecoveryRetry(t *testing.T) {
	for _, boundary := range []string{"replace", "restored", "finalization"} {
		t.Run(boundary, func(t *testing.T) {
			r, tools := interruptedPublication(t, false, "migration")
			last := publishDisk(t, r).Revision
			for attempt := 0; attempt < 2; attempt++ {
				fresh, err := Open(r.root, testHost)
				if err != nil {
					t.Fatal(err)
				}
				if boundary == "finalization" {
					fresh.persist = func(root string, s snapshot) error {
						if s.Records[0].PublishJournal == nil {
							return errSnapshotUncertain
						}
						return writeSnapshot(root, s)
					}
				}
				hits := 0
				o, err := newOwner(fresh, tools.Node, backupOps{hook: func(step string) error {
					if step == boundary {
						hits++
						if boundary != "replace" || hits == 2 {
							return errors.New("injected interruption")
						}
					}
					return nil
				}})
				if err != nil {
					t.Fatal(err)
				}
				waitRecoveryWorker(t, o)
				assertRecoveryBarred(t, o, "1.0.0")
				v := ownerRecord(t, fresh, testApp)
				if v.PublishJournal == nil || v.Revision < last || boundary != "finalization" && (hits == 0 || v.LastError == nil || v.LastError.Code != "app.io_failed") {
					t.Fatal(v, hits)
				}
				last = v.Revision
				_ = o.Shutdown()
			}
			fresh, o := reopenPublication(t, r, tools.Node)
			v := waitRecovery(t, fresh)
			if v.Revision <= last || v.ActiveRelease != "1.0.0" || string(testBytes(t, filepath.Join(r.root, testApp, "data/preference"))) != "saved" {
				t.Fatal(v)
			}
			if healthUse(healthTarget(t, o)) != nil {
				t.Fatal("unhealthy retry")
			}
		})
	}
}

func TestStartupRecoveryInvalidation(t *testing.T) {
	for _, event := range []string{"stop", "intent reuse", "remove", "shutdown", "metadata"} {
		t.Run(event, func(t *testing.T) {
			r, tools := interruptedPublication(t, false, "migration")
			fresh, err := Open(r.root, testHost)
			if err != nil {
				t.Fatal(err)
			}
			entered, release := make(chan struct{}), make(chan struct{})
			var releaseOnce sync.Once
			unpark := func() { releaseOnce.Do(func() { close(release) }) }
			o, err := newOwner(fresh, tools.Node, backupOps{hook: func(step string) error {
				if step == "restored" {
					close(entered)
					<-release
				}
				return nil
			}})
			if err != nil {
				t.Fatal(err)
			}
			defer o.Shutdown()
			defer unpark()
			<-entered
			assertRecoveryBarred(t, o, "1.0.0")
			// An unrelated committed release remains usable while restoration is parked.
			m := testMap(t)
			m["app_id"] = otherApp
			testRegister(t, fresh, testJSON(t, m))
			b, _ := json.Marshal(healthReply{Body: strings.ReplaceAll(healthyBody(), testApp, otherApp), API: true})
			ownerBuild(t, fresh, otherApp, "a", "1.0.0", string(b))
			if _, err = fresh.UpdateLifecycle(testHost, otherApp, Lifecycle{Title: "Sibling", Desired: "available", State: "starting", ActiveRelease: "1.0.0"}); err != nil {
				t.Fatal(err)
			}
			healthWait(t, func() bool { target, e := o.Lookup(testHost, otherApp); return e == nil && healthUse(target) == nil })
			done := make(chan error, 1)
			go func() {
				switch event {
				case "stop":
					done <- o.Stop(testApp)
				case "remove":
					_, e := fresh.Remove(testApp)
					done <- e
				case "shutdown":
					done <- o.Shutdown()
				default:
					v := ownerRecord(t, fresh, testApp)
					f := Lifecycle{Title: "Newer title", Desired: v.Desired, State: v.State, ActiveRelease: v.ActiveRelease, PendingRelease: v.PendingRelease}
					if event == "intent reuse" {
						f.Desired = "stopped"
					}
					_, e := fresh.UpdateLifecycle(testHost, testApp, f)
					if event == "intent reuse" && e == nil {
						f.Desired = "available"
						_, e = fresh.UpdateLifecycle(testHost, testApp, f)
					}
					done <- e
				}
			}()
			if event == "stop" || event == "remove" || event == "shutdown" {
				select {
				case e := <-done:
					t.Fatal("did not join parked restoration", e)
				case <-time.After(30 * time.Millisecond):
				}
				unpark()
				err = <-done
			} else {
				err = <-done
				unpark()
			}
			if err != nil && !(event == "remove" && errors.Is(err, ErrConflict)) {
				t.Fatal(err)
			}
			waitRecoveryWorker(t, o)
			v := ownerRecord(t, fresh, testApp)
			if v.PublishJournal == nil || v.State == "running" || event == "stop" && v.Desired != "stopped" || (event == "metadata" || event == "intent reuse") && v.Title != "Newer title" {
				t.Fatal(v)
			}
			if _, e := o.Lookup(testHost, testApp); e == nil {
				t.Fatal("obsolete target")
			}
		})
	}
}

func TestStartupRecoveryFailures(t *testing.T) {
	for _, failure := range []string{"missing backup", "changed backup", "previous build", "previous health", "committed", "retirement"} {
		t.Run(failure, func(t *testing.T) {
			r, tools := interruptedPublication(t, false, "migration")
			v := publishDisk(t, r).Records[0]
			switch failure {
			case "missing backup":
				if err := os.Rename(v.PublishJournal.VerifiedBackup.Root, v.PublishJournal.VerifiedBackup.Root+"-missing"); err != nil {
					t.Fatal(err)
				}
			case "changed backup":
				testArtifactWrite(t, v.PublishJournal.VerifiedBackup.Root, "data/preference", "changed")
			case "previous build":
				p := filepath.Join(v.ReleaseHistory[0].BuildRoot, "dist/service/main.js")
				if err := os.Chmod(p, 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(p, []byte("changed"), 0600); err != nil {
					t.Fatal(err)
				}
			case "previous health":
				script := strings.Replace(string(testBytes(t, tools.Node)), "#!/bin/sh\n", "#!/bin/sh\nif [ \"$1\" = dist/service/main.js ]; then exit 8; fi\n", 1)
				if err := os.WriteFile(tools.Node, []byte(script), 0700); err != nil {
					t.Fatal(err)
				}
			case "committed", "retirement":
				fresh, e := Open(r.root, testHost)
				if e != nil {
					t.Fatal(e)
				}
				fresh.mu.Lock()
				u := &lifecycleUpdate{serverID: testHost, expected: fresh.snapshot.Records[0].Revision, metadata: func(r *Record) error {
					if failure == "retirement" {
						r.RetirementPending = true
					} else {
						r.PublishJournal.ActivationCommitted = true
						r.ActiveRelease = r.PublishJournal.Candidate.ReleaseVersion
					}
					return nil
				}}
				_, e = fresh.changeLocked("lifecycle", testApp, nil, u)
				fresh.mu.Unlock()
				if e != nil {
					t.Fatal(e)
				}
			}
			fresh, o := reopenPublication(t, r, tools.Node)
			waitRecoveryWorker(t, o)
			assertRecoveryBarred(t, o, ownerRecord(t, fresh, testApp).ActiveRelease)
			if v := ownerRecord(t, fresh, testApp); v.PublishJournal == nil || failure != "committed" && failure != "retirement" && v.LastError == nil {
				t.Fatal(v)
			}
		})
	}
}

func reopenPublication(t *testing.T, r *Registry, node string) (*Registry, *Owner) {
	t.Helper()
	fresh, err := Open(r.root, testHost)
	if err != nil {
		t.Fatal(err)
	}
	o, err := NewOwner(fresh, node)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = o.Shutdown() })
	return fresh, o
}

func waitRecovery(t *testing.T, r *Registry) Record {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		v := ownerRecord(t, r, testApp)
		if v.PublishJournal == nil {
			return v
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("recovery did not complete", ownerRecord(t, r, testApp))
	return Record{}
}

func TestStartupPublicationRecovery(t *testing.T) {
	for _, first := range []bool{false, true} {
		for _, phase := range []string{"journal", "migration"} {
			t.Run(map[bool]string{false: "update", true: "first"}[first]+"/"+phase, func(t *testing.T) {
				r, tools := interruptedPublication(t, first, phase)
				before := publishDisk(t, r)
				testArtifactWrite(t, r.root, testApp+"/data/candidate-only", "pending")
				fresh, o := reopenPublication(t, r, tools.Node)
				v := waitRecovery(t, fresh)
				if v.Title != before.Records[0].Title || v.ActiveRelease != before.Records[0].ActiveRelease || v.PendingRelease != "" || v.LastError == nil || v.Revision <= before.Revision {
					t.Fatal(v)
				}
				if _, err := os.Stat(filepath.Join(r.root, testApp, "data/candidate-only")); !os.IsNotExist(err) {
					t.Fatal(err)
				}
				if first {
					if v.State != "failed" {
						t.Fatal(v)
					}
					if _, err := o.Lookup(testHost, testApp); !errors.Is(err, ErrUnavailable) {
						t.Fatal(err)
					}
				} else {
					if v.State != "running" || string(testBytes(t, filepath.Join(r.root, testApp, "data/preference"))) != "saved" {
						t.Fatal(v)
					}
					if err := healthUse(healthTarget(t, o)); err != nil {
						t.Fatal(err)
					}
				}
			})
		}
	}
}
