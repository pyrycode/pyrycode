package apps

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

func TestUpdatePreparation(t *testing.T) {
	r, o, tools, c, config := updateFixture(t)
	old := healthTarget(t, o)
	updateCandidate(t, r, c, "2.0.0", "ok", strings.Replace(healthyBody(), "1.0.0", "2.0.0", 1))
	c.Gate, c.Fail = "run build", "run build"
	testCandidateConfig(t, config, c)
	done := make(chan error, 1)
	go func() { _, e := o.Publish(context.Background(), testHost, testApp, tools); done <- e }()
	defer func() { _ = os.WriteFile(c.Ready+".continue", nil, 0600) }()
	waitFile(t, c.Ready)
	v := ownerRecord(t, r, testApp)
	if v.State != "running" || v.Title != "App" || v.ActiveRelease != "1.0.0" || healthUse(old) != nil {
		t.Fatal("checks retired old target", v)
	}
	log := string(testBytes(t, c.Log))
	if _, e := o.Publish(context.Background(), testHost, testApp, tools); e == nil || string(testBytes(t, c.Log)) != log {
		t.Fatal("competing publication built")
	}
	if e := o.Start(context.Background(), testApp); !errors.Is(e, ErrConflict) {
		t.Fatal(e)
	}
	sibling, e := r.Create(context.Background(), "Sibling")
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		for _, release := range ownerRecord(t, r, sibling.AppID).ReleaseHistory {
			testWritableBuild(t, release.BuildRoot)
		}
	})
	other := c
	other.Gate, other.Fail = "", ""
	testCandidateConfig(t, config, other)
	b, _ := json.Marshal(healthReply{Body: strings.Replace(healthyBody(), testApp, sibling.AppID, 1)})
	testArtifactWrite(t, c.Fixture, "dist/service/main.js", string(b))
	if _, e = o.Publish(context.Background(), testHost, sibling.AppID, tools); e != nil {
		t.Fatal("sibling isolated", e)
	}
	if e = os.WriteFile(c.Ready+".continue", nil, 0600); e != nil {
		t.Fatal(e)
	}
	if e = <-done; e == nil {
		t.Fatal("build failure succeeded")
	}
	if healthUse(old) != nil || ownerRecord(t, r, testApp).LastError == nil {
		t.Fatal("failed build lost healthy old service")
	}
}

func TestUpdateExclusiveRetirement(t *testing.T) {
	r, o, tools, c, _ := updateFixture(t)
	old := healthTarget(t, o)
	apiWrite(t, r, apiReply{Status: 200, Stall: "body"})
	req, _ := http.NewRequest("GET", "http://caller.invalid/api/stall", nil)
	response, e := old.Do(req)
	if e != nil {
		t.Fatal(e)
	}
	defer response.Body.Close()
	read := make(chan error, 1)
	go func() { _, e := io.ReadAll(response.Body); read <- e }()
	waitFile(t, filepath.Join(r.root, testApp, "data/stalled"))
	apiWrite(t, r, apiReply{Status: 200, Stall: "headers"})
	headers := make(chan error, 1)
	go func() {
		req, _ := http.NewRequest("GET", "http://caller.invalid/api/headers", nil)
		response, e := old.Do(req)
		if response != nil {
			response.Body.Close()
		}
		headers <- e
	}()
	healthWait(t, func() bool { return apiCount(r) == 2 })
	updateCandidate(t, r, c, "2.0.0", "ok", strings.Replace(healthyBody(), "1.0.0", "2.0.0", 1))
	var violation string
	o.backupOps.hook = func(step string) error {
		if step == "copy" {
			if !generationDone(old.generation) {
				violation = "snapshot before old group joined"
			}
			select {
			case e := <-read:
				if e == nil {
					violation = "body not cancelled"
				}
				read <- e
			default:
				violation = "snapshot before body read retired"
			}
			if healthUse(old) == nil {
				violation = "old target survived exclusive cutover"
			}
			select {
			case e := <-headers:
				if e == nil {
					violation = "pending request was not cancelled"
				}
				headers <- e
			default:
				violation = "snapshot preceded pending request retirement"
			}
		}
		return nil
	}
	if _, e = o.Publish(context.Background(), testHost, testApp, tools); e != nil {
		t.Fatal(e)
	}
	if violation != "" {
		t.Fatal(violation)
	}
	if e = <-read; e == nil {
		t.Fatal("retired body succeeded")
	}
	if e = <-headers; e == nil {
		t.Fatal("retired headers succeeded")
	}
	waitGone(t, old.generation.cmd.Process.Pid)
}

func TestUpdateRetirement(t *testing.T) {
	for _, event := range []string{"stop", "remove", "shutdown"} {
		for _, point := range []string{"build", "migration", "health"} {
			t.Run(event+"/"+point, func(t *testing.T) {
				r, o, tools, c, config := updateFixture(t)
				old := recoveryGeneration(o, testApp)
				migration := "ok"
				if point == "migration" {
					migration = "hold"
				}
				updateCandidate(t, r, c, "2.0.0", migration, `{}`)
				if point == "build" {
					c.Gate = "run build"
					testCandidateConfig(t, config, c)
				}
				path := filepath.Join(r.root, testApp, "data/ready")
				if point == "migration" {
					path = filepath.Join(r.root, testApp, "data/migration")
				}
				if point == "build" {
					path = c.Ready
				} else if e := os.Remove(path); e != nil {
					t.Fatal(e)
				}
				done := make(chan error, 1)
				go func() { _, e := o.Publish(context.Background(), testHost, testApp, tools); done <- e }()
				waitFile(t, path)
				var e error
				switch event {
				case "stop":
					e = o.Stop(testApp)
				case "remove":
					_, e = r.Remove(testApp)
				case "shutdown":
					e = o.Shutdown()
				}
				if e != nil {
					t.Fatal(e)
				}
				if e = <-done; e == nil {
					t.Fatal("late success")
				}
				if _, e = o.Lookup(testHost, testApp); e == nil {
					t.Fatal("late target")
				}
				if e = o.confirmRunning(context.Background(), testApp, old); !errors.Is(e, ErrStale) {
					t.Fatal("late health", e)
				}
				if string(testBytes(t, filepath.Join(r.root, testApp, "data/preference"))) != "saved" {
					t.Fatal("stop discarded retained data")
				}
				s := publishDisk(t, r)
				if event == "remove" {
					if len(s.Records) != 0 || len(s.Tombstones) != 1 {
						t.Fatal(s)
					}
				} else {
					v := s.Records[0]
					if v.ActiveRelease != "1.0.0" || v.PublishJournal != nil || v.State == "running" || event == "stop" && v.Desired != "stopped" {
						t.Fatal(v)
					}
				}
			})
		}
	}
}

func TestUpdatePersistence(t *testing.T) {
	for _, phase := range []string{"activation", "finalization", "recovery"} {
		t.Run(phase, func(t *testing.T) {
			r, o, tools, c, _ := updateFixture(t)
			migration := "ok"
			if phase == "recovery" {
				migration = "fail"
			}
			updateCandidate(t, r, c, "2.0.0", migration, strings.Replace(healthyBody(), "1.0.0", "2.0.0", 1))
			injected := false
			r.mu.Lock()
			r.persist = func(root string, s snapshot) error {
				v := s.Records[0]
				fail := phase == "activation" && v.ActiveRelease == "2.0.0" && v.PublishJournal != nil || phase == "finalization" && v.ActiveRelease == "2.0.0" && v.PublishJournal == nil || phase == "recovery" && v.LastError != nil && v.State == "running" && v.PublishJournal == nil
				return (snapshotOps{sync: func(f *os.File) error {
					info, e := f.Stat()
					if e == nil && info.IsDir() && fail {
						injected = true
						return errors.New("SECRET")
					}
					return f.Sync()
				}}).write(root, s)
			}
			r.mu.Unlock()
			result, e := o.Publish(context.Background(), testHost, testApp, tools)
			if !injected || e == nil || !result.RecoveryPending || result.Healthy || result.ActivationCommitted != (phase == "finalization") {
				t.Fatal(result, e)
			}
			if _, e = o.Lookup(testHost, testApp); e == nil {
				t.Fatal("uncertain target")
			}
			v := ownerRecord(t, r, testApp)
			if v.PublishJournal == nil || v.PublishJournal.ActivationCommitted != (phase == "finalization") {
				t.Fatal("last durable evidence lost", v)
			}
			onDisk := publishDisk(t, r).Records[0]
			if phase == "activation" && (!onDisk.PublishJournal.ActivationCommitted || onDisk.ActiveRelease != "2.0.0") {
				t.Fatal("ambiguous disk decision not retained", onDisk)
			}
			if phase == "activation" || phase == "finalization" {
				if _, e = os.Stat(filepath.Join(r.root, testApp, "data/migration")); e != nil {
					t.Fatal("candidate data restored beneath activation decision", e)
				}
			}
			before := string(testBytes(t, filepath.Join(r.root, "registry.json")))
			if _, e = o.Publish(context.Background(), testHost, testApp, tools); e == nil || string(testBytes(t, filepath.Join(r.root, "registry.json"))) != before {
				t.Fatal("uncertainty admitted another write")
			}
		})
	}
}

func TestUpdateWriterRetirementUncertain(t *testing.T) {
	r, o, tools, c, _ := updateFixture(t)
	old := recoveryGeneration(o, testApp)
	updateCandidate(t, r, c, "2.0.0", "fork", strings.Replace(healthyBody(), "1.0.0", "2.0.0", 1))
	o.kill = func(pid int, sig syscall.Signal) error {
		if pid != -old.cmd.Process.Pid && pid < 0 && sig != 0 {
			return syscall.EPERM
		}
		return syscall.Kill(pid, sig)
	}
	defer func() {
		b, e := os.ReadFile(filepath.Join(r.root, testApp, "data/descendant"))
		if e == nil {
			pid, _ := strconv.Atoi(string(b))
			if pid > 0 {
				_ = syscall.Kill(pid, syscall.SIGKILL)
				waitGone(t, pid)
			}
		}
	}()
	result, e := o.Publish(context.Background(), testHost, testApp, tools)
	if e == nil || !result.RecoveryPending || result.ActivationCommitted || result.Healthy {
		t.Fatal(result, e)
	}
	v := publishDisk(t, r).Records[0]
	if v.State != "failed" || v.PublishJournal == nil || v.PublishJournal.Previous == nil || v.PublishJournal.Previous.ReleaseVersion != "1.0.0" || v.PublishJournal.ActivationCommitted {
		t.Fatal(v)
	}
	pid, _ := strconv.Atoi(string(testBytes(t, filepath.Join(r.root, testApp, "data/descendant"))))
	if syscall.Kill(pid, 0) != nil {
		t.Fatal("live-writer uncertainty not exercised")
	}
	if _, e = os.Stat(filepath.Join(r.root, testApp, "data/migration")); e != nil {
		t.Fatal("restored beneath live writer", e)
	}
	if _, e = o.Lookup(testHost, testApp); e == nil {
		t.Fatal("uncertain target")
	}
	if removed, e := r.Remove(testApp); removed || !errors.Is(e, ErrConflict) {
		t.Fatal("removed unresolved evidence", removed, e)
	}
}

func TestUpdateCancellationRecovery(t *testing.T) {
	for _, event := range []string{"cancel", "intent reuse"} {
		t.Run(event, func(t *testing.T) {
			r, o, tools, c, _ := updateFixture(t)
			updateCandidate(t, r, c, "2.0.0", "hold", `{}`)
			path := filepath.Join(r.root, testApp, "data/migration")
			if e := os.Remove(path); e != nil {
				t.Fatal(e)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan struct {
				result PublishResult
				err    error
			}, 1)
			go func() {
				result, e := o.Publish(ctx, testHost, testApp, tools)
				done <- struct {
					result PublishResult
					err    error
				}{result, e}
			}()
			waitFile(t, path)
			o.mu.Lock()
			p := o.publishing[testApp]
			o.mu.Unlock()
			if event == "cancel" {
				cancel()
			} else {
				v := ownerRecord(t, r, testApp)
				for _, desired := range []string{"stopped", "available"} {
					_, e := r.UpdateLifecycle(testHost, testApp, Lifecycle{Title: v.Title, Desired: desired, State: "stopped", ActiveRelease: v.ActiveRelease, PendingRelease: v.PendingRelease})
					if e != nil {
						t.Fatal(e)
					}
				}
			}
			out := <-done
			if out.err == nil || out.result.ActivationCommitted || out.result.Healthy || out.result.RecoveryPending {
				t.Fatal(out)
			}
			if event == "cancel" {
				if out.result.Lifecycle.State != "running" || healthUse(healthTarget(t, o)) != nil {
					t.Fatal("cancel did not join safe recovery", out)
				}
			} else {
				o.mu.Lock()
				recovered := p.recovered
				o.mu.Unlock()
				if recovered {
					t.Fatal("obsolete intent exposed recovery")
				}
			}
			if string(testBytes(t, filepath.Join(r.root, testApp, "data/preference"))) != "saved" {
				t.Fatal("cancellation lost data")
			}
		})
	}
}
