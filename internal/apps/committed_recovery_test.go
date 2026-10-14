package apps

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func committedPublication(t *testing.T, first bool) (*Registry, Toolchain) {
	t.Helper()
	r, o, tools, c, _ := publishFixture(t, healthReply{Body: healthyBody(), API: true}, "ok")
	if !first {
		if _, err := o.Publish(context.Background(), testHost, testApp, tools); err != nil {
			t.Fatal(err)
		}
		updateCandidate(t, r, c, "2.0.0", "ok", strings.ReplaceAll(healthyBody(), "1.0.0", "2.0.0"))
	}
	r.mu.Lock()
	activated := false
	r.persist = func(root string, s snapshot) error {
		if s.Records[0].PublishJournal != nil && s.Records[0].PublishJournal.ActivationCommitted {
			activated = true
		}
		if activated && s.Records[0].PublishJournal == nil {
			return errors.New("injected finalization failure")
		}
		return writeSnapshot(root, s)
	}
	r.mu.Unlock()
	if _, err := o.Publish(context.Background(), testHost, testApp, tools); err == nil {
		t.Fatal("finalization interruption absent")
	}
	if healthUse(healthTarget(t, o)) != nil {
		t.Fatal("live committed publication lost target")
	}
	_ = o.Shutdown()
	v := publishDisk(t, r).Records[0]
	if v.PublishJournal == nil || !v.PublishJournal.ActivationCommitted {
		t.Fatal(v)
	}
	testArtifactWrite(t, r.root, testApp+"/data/candidate-only", "committed data")
	script := strings.Replace(string(testBytes(t, tools.Node)), "#!/bin/sh\n", "#!/bin/sh\nif [ \"$1\" = --input-type=module ]; then exit 0; fi\n", 1)
	if err := os.WriteFile(tools.Node, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	return r, tools
}

func TestCommittedStartupRecovery(t *testing.T) {
	for _, first := range []bool{false, true} {
		for _, stopped := range []bool{false, true} {
			t.Run(map[bool]string{false: "update", true: "first"}[first]+"/"+map[bool]string{false: "available", true: "stopped"}[stopped], func(t *testing.T) {
				r, tools := committedPublication(t, first)
				fresh, err := Open(r.root, testHost)
				if err != nil {
					t.Fatal(err)
				}
				v := ownerRecord(t, fresh, testApp)
				if stopped {
					if _, err = fresh.UpdateLifecycle(testHost, testApp, Lifecycle{Title: v.Title, Desired: "stopped", State: "stopped", ActiveRelease: v.ActiveRelease, LastError: &LastError{"app.unavailable", "Interrupted"}}); err != nil {
						t.Fatal(err)
					}
				}
				before := publishDisk(t, fresh)
				o, err := NewOwner(fresh, tools.Node)
				if err != nil {
					t.Fatal(err)
				}
				defer o.Shutdown()
				v = waitRecovery(t, fresh)
				want := before.Records[0]
				if v.ActiveRelease != want.ActiveRelease || v.Title != want.Title || v.Desired != want.Desired || v.LastError != nil || v.PendingRelease != "" || v.Revision <= before.Revision {
					t.Fatal(v)
				}
				if string(testBytes(t, filepath.Join(r.root, testApp, "data/candidate-only"))) != "committed data" {
					t.Fatal("candidate data replaced")
				}
				if stopped {
					if v.State != "stopped" || recoveryGeneration(o, testApp) != nil {
						t.Fatal(v)
					}
					if _, err = o.Lookup(testHost, testApp); err == nil {
						t.Fatal("stopped target")
					}
				} else if v.State != "running" || healthUse(healthTarget(t, o)) != nil {
					t.Fatal(v)
				}
			})
		}
	}
}

func TestCommittedRecoveryRetry(t *testing.T) {
	for _, failure := range []string{"validation", "write", "uncertain", "exhaustion"} {
		t.Run(failure, func(t *testing.T) {
			r, tools := committedPublication(t, false)
			last := publishDisk(t, r).Revision
			if failure == "exhaustion" {
				s := publishDisk(t, r)
				s.Revision = maxRevision
				s.Records[0].Revision = maxRevision
				s.Records[0].State = "starting"
				if err := writeSnapshot(r.root, s); err != nil {
					t.Fatal(err)
				}
				last = maxRevision
			}
			for attempt := 0; attempt < 2; attempt++ {
				fresh, err := Open(r.root, testHost)
				if err != nil {
					t.Fatal(err)
				}
				if failure == "write" || failure == "uncertain" {
					fresh.persist = func(root string, s snapshot) error {
						if s.Records[0].PublishJournal == nil {
							if failure == "uncertain" {
								return errSnapshotUncertain
							}
							return errors.New("injected pre-commit failure")
						}
						return writeSnapshot(root, s)
					}
				}
				o, err := newOwner(fresh, tools.Node, backupOps{hook: func(step string) error {
					if step == "recovery-validated" && failure == "validation" {
						return errors.New("interrupted after health")
					}
					return nil
				}})
				if err != nil {
					t.Fatal(err)
				}
				waitRecoveryWorker(t, o)
				assertRecoveryBarred(t, o, "2.0.0")
				v := ownerRecord(t, fresh, testApp)
				if v.PublishJournal == nil || !v.PublishJournal.ActivationCommitted || v.ActiveRelease != "2.0.0" || v.Revision < last {
					t.Fatal(v)
				}
				last = v.Revision
				if err = VerifyBackup(context.Background(), r.root, testHost, testApp, v.PublishJournal.VerifiedBackup, tools.Node); err != nil {
					t.Fatal(err)
				}
				if string(testBytes(t, filepath.Join(r.root, testApp, "data/candidate-only"))) != "committed data" {
					t.Fatal("data replaced")
				}
				_ = o.Shutdown()
			}
			if failure == "exhaustion" {
				return
			}
			fresh, o := reopenPublication(t, r, tools.Node)
			if v := waitRecovery(t, fresh); v.Revision <= last || v.LastError != nil || healthUse(healthTarget(t, o)) != nil {
				t.Fatal(v)
			}
		})
	}
}

func TestCommittedRecoverySelection(t *testing.T) {
	r, tools := committedPublication(t, false)
	fresh, err := Open(r.root, testHost)
	if err != nil {
		t.Fatal(err)
	}
	v := ownerRecord(t, fresh, testApp)
	if _, err = fresh.UpdateLifecycle(testHost, testApp, Lifecycle{Title: v.Title, Desired: "available", State: "starting", ActiveRelease: "1.0.0"}); err != nil {
		t.Fatal(err)
	}
	o, err := NewOwner(fresh, tools.Node)
	if err != nil {
		t.Fatal(err)
	}
	defer o.Shutdown()
	v = waitRecovery(t, fresh)
	if v.ActiveRelease != "2.0.0" || v.Title != "Updated" || v.LastError != nil || healthUse(healthTarget(t, o)) != nil {
		t.Fatal(v)
	}
}

func TestCommittedRecoveryFailures(t *testing.T) {
	for _, failure := range []string{"missing build", "changed build", "health", "stopped validation", "symlink database", "retirement"} {
		t.Run(failure, func(t *testing.T) {
			r, tools := committedPublication(t, false)
			v := publishDisk(t, r).Records[0]
			build := v.ReleaseHistory[len(v.ReleaseHistory)-1].BuildRoot
			switch failure {
			case "missing build":
				testWritableBuild(t, build)
				if err := os.Chmod(filepath.Dir(build), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(build, filepath.Join(t.TempDir(), "missing")); err != nil {
					t.Fatal(err)
				}
			case "changed build":
				testWritableBuild(t, build)
				testArtifactWrite(t, build, "dist/service/main.js", "damaged")
			case "health", "stopped validation":
				script := strings.Replace(string(testBytes(t, tools.Node)), "#!/bin/sh\n", "#!/bin/sh\nif [ \"$1\" = dist/service/main.js ] || [ \"$1\" = --input-type=module ]; then exit 8; fi\n", 1)
				if err := os.WriteFile(tools.Node, []byte(script), 0700); err != nil {
					t.Fatal(err)
				}
			case "symlink database":
				db := filepath.Join(r.root, testApp, "data/app.sqlite")
				if err := os.Rename(db, db+"-linked"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(db+"-linked", db); err != nil {
					t.Fatal(err)
				}
			}
			fresh, err := Open(r.root, testHost)
			if err != nil {
				t.Fatal(err)
			}
			if failure == "stopped validation" || failure == "symlink database" {
				if _, err = fresh.UpdateLifecycle(testHost, testApp, Lifecycle{Title: v.Title, Desired: "stopped", State: "stopped", ActiveRelease: v.ActiveRelease}); err != nil {
					t.Fatal(err)
				}
			}
			if failure == "retirement" {
				fresh.mu.Lock()
				_, err = fresh.changeLocked("lifecycle", testApp, nil, &lifecycleUpdate{serverID: testHost, metadata: func(r *Record) error { r.RetirementPending = true; return nil }})
				fresh.mu.Unlock()
				if err != nil {
					t.Fatal(err)
				}
			}
			o, err := NewOwner(fresh, tools.Node)
			if err != nil {
				t.Fatal(err)
			}
			defer o.Shutdown()
			waitRecoveryWorker(t, o)
			assertRecoveryBarred(t, o, "2.0.0")
			v = ownerRecord(t, fresh, testApp)
			if v.PublishJournal == nil || v.ActiveRelease != "2.0.0" || failure != "retirement" && v.LastError == nil {
				t.Fatal(v)
			}
			if string(testBytes(t, filepath.Join(r.root, testApp, "data/candidate-only"))) != "committed data" {
				t.Fatal("fallback data")
			}
		})
	}
}

func TestCommittedRecoveryInvalidation(t *testing.T) {
	for _, event := range []string{"stop", "intent reuse", "stopped intent reuse", "remove", "shutdown", "metadata"} {
		t.Run(event, func(t *testing.T) {
			r, tools := committedPublication(t, false)
			fresh, err := Open(r.root, testHost)
			if err != nil {
				t.Fatal(err)
			}
			if event == "stopped intent reuse" {
				v := ownerRecord(t, fresh, testApp)
				if _, err = fresh.UpdateLifecycle(testHost, testApp, Lifecycle{Title: v.Title, Desired: "stopped", State: "stopped", ActiveRelease: v.ActiveRelease}); err != nil {
					t.Fatal(err)
				}
			}
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			unpark := func() { once.Do(func() { close(release) }) }
			o, err := newOwner(fresh, tools.Node, backupOps{hook: func(step string) error {
				if step == "recovery-validated" {
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
			assertRecoveryBarred(t, o, "2.0.0")
			m := testMap(t)
			m["app_id"] = otherApp
			testRegister(t, fresh, testJSON(t, m))
			ownerBuild(t, fresh, otherApp, "a", "1.0.0", `{"Body":"{\"ready\":true,\"app_id\":\"`+otherApp+`\",\"release_version\":\"1.0.0\"}","API":true}`)
			if _, err = fresh.UpdateLifecycle(testHost, otherApp, Lifecycle{Title: "Sibling", Desired: "available", State: "starting", ActiveRelease: "1.0.0"}); err != nil {
				t.Fatal(err)
			}
			healthWait(t, func() bool { target, e := o.Lookup(testHost, otherApp); return e == nil && healthUse(target) == nil })
			v := ownerRecord(t, fresh, testApp)
			switch event {
			case "metadata", "intent reuse", "stopped intent reuse":
				f := Lifecycle{Title: "Newer title", Desired: v.Desired, State: v.State, ActiveRelease: v.ActiveRelease}
				if event == "intent reuse" {
					f.Desired = "stopped"
				}
				if event == "stopped intent reuse" {
					f.Desired = "available"
				}
				if _, err = fresh.UpdateLifecycle(testHost, testApp, f); err != nil {
					t.Fatal(err)
				}
				if event == "intent reuse" {
					f.Desired = "available"
					if _, err = fresh.UpdateLifecycle(testHost, testApp, f); err != nil {
						t.Fatal(err)
					}
				}
				if event == "stopped intent reuse" {
					f.Desired = "stopped"
					if _, err = fresh.UpdateLifecycle(testHost, testApp, f); err != nil {
						t.Fatal(err)
					}
				}
				unpark()
			default:
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
					}
				}()
				healthWait(t, func() bool {
					o.mu.Lock()
					defer o.mu.Unlock()
					return o.closed || o.publishing[testApp].ctx.Err() != nil
				})
				select {
				case e := <-done:
					t.Fatal("did not join parked work", e)
				default:
				}
				unpark()
				err = <-done
				if err != nil && !(event == "remove" && errors.Is(err, ErrConflict)) {
					t.Fatal(err)
				}
			}
			waitRecoveryWorker(t, o)
			v = ownerRecord(t, fresh, testApp)
			if v.PublishJournal == nil || v.State == "running" || v.ActiveRelease != "2.0.0" || (event == "stop" || event == "remove" || event == "stopped intent reuse") && v.Desired != "stopped" || (event == "metadata" || strings.Contains(event, "intent reuse")) && v.Title != "Newer title" {
				t.Fatal(v)
			}
			if _, e := o.Lookup(testHost, testApp); e == nil {
				t.Fatal("stale target")
			}
			if string(testBytes(t, filepath.Join(r.root, testApp, "data/candidate-only"))) != "committed data" {
				t.Fatal("data replaced")
			}
		})
	}
}
