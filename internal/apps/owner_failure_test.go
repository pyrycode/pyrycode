package apps

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestOwnerBuildRejection(t *testing.T) {
	for _, name := range []string{"missing", "ambiguous", "foreign", "wrong app", "manifest symlink", "manifest directory", "entry symlink", "entry directory", "build symlink", "data symlink", "database symlink", "database directory", "unsupported runtime"} {
		t.Run(name, func(t *testing.T) {
			r, o := ownerFixture(t)
			dir := ownerBuild(t, r, testApp, "a", "1.0.0", ownerService)
			outside := t.TempDir()
			must := func(e error) {
				t.Helper()
				if e != nil {
					t.Fatal(e)
				}
			}
			switch name {
			case "missing":
				must(os.Remove(filepath.Join(dir, "app.json")))
			case "ambiguous":
				ownerBuild(t, r, testApp, "b", "1.0.0", ownerService)
			case "foreign", "wrong app":
				m := testMap(t)
				if name == "foreign" {
					m["server_id"] = testID(9)
				} else {
					m["app_id"] = testID(9)
				}
				must(os.WriteFile(filepath.Join(dir, "app.json"), testJSON(t, m), 0600))
			case "manifest symlink", "manifest directory":
				p := filepath.Join(dir, "app.json")
				must(os.Remove(p))
				if name == "manifest symlink" {
					must(os.Symlink(filepath.Join(outside, "app.json"), p))
				} else {
					must(os.Mkdir(p, 0700))
				}
			case "entry symlink", "entry directory":
				p := filepath.Join(dir, "dist/service/main.js")
				must(os.Remove(p))
				if name == "entry symlink" {
					must(os.Symlink(filepath.Join(outside, "main.js"), p))
				} else {
					must(os.Mkdir(p, 0700))
				}
			case "build symlink":
				must(os.Rename(dir, filepath.Join(outside, "build")))
				must(os.Symlink(filepath.Join(outside, "build"), dir))
			case "data symlink":
				must(os.Symlink(outside, filepath.Join(r.root, testApp, "data")))
			case "database symlink", "database directory":
				p := filepath.Join(r.root, testApp, "data")
				must(os.Mkdir(p, 0700))
				if name == "database symlink" {
					must(os.WriteFile(filepath.Join(outside, "db"), []byte("keep"), 0600))
					must(os.Symlink(filepath.Join(outside, "db"), filepath.Join(p, "app.sqlite")))
				} else {
					must(os.Mkdir(filepath.Join(p, "app.sqlite"), 0700))
				}
			case "unsupported runtime":
				o.node = writeNodeDouble(t, "v25.0.0", false)
			}
			if e := o.Start(context.Background(), testApp); e == nil {
				t.Fatal("accepted unsafe release")
			}
			<-o.generations[testApp].done
			v := ownerRecord(t, r, testApp)
			if v.State != "failed" || v.LastError == nil || v.LastError.Message != "Service could not start" || strings.Contains(v.LastError.Message, r.root) {
				t.Fatal(v)
			}
			if _, e := os.Stat(filepath.Join(r.root, testApp, "data/ready")); e == nil {
				t.Fatal("executed service")
			}
		})
	}
	for _, v := range []string{"v24.14.9", "v25.0.0", "v24.15.0-rc.1", "24.15.0", "v024.15.0"} {
		if supportedNode(v) {
			t.Fatal(v)
		}
	}
	for _, v := range []string{"v24.15.0", "v24.16.1"} {
		if !supportedNode(v) {
			t.Fatal(v)
		}
	}
}
func TestOwnerUnpublished(t *testing.T) {
	r := testOpen(t)
	testRegister(t, r, []byte(testManifest))
	o, e := NewOwner(r, writeNodeDouble(t, "v24.15.0", false))
	if e != nil {
		t.Fatal(e)
	}
	if e = o.Start(context.Background(), testApp); e != nil {
		t.Fatal(e)
	}
	v := ownerRecord(t, r, testApp)
	if v.State != "stopped" || v.ActiveRelease != "" {
		t.Fatal(v)
	}
	if len(o.ports) != 0 {
		t.Fatal("reserved unpublished port")
	}
	if e = o.Shutdown(); e != nil {
		t.Fatal(e)
	}
}
func TestOwnerReservations(t *testing.T) {
	r, o := ownerFixture(t)
	ownerBuild(t, r, testApp, "a", "1.0.0", ownerService)
	o.node = writeNodeDouble(t, "v24.15.0", true)
	if e := o.Start(context.Background(), testApp); e == nil {
		t.Fatal("spawn must fail")
	}
	g := o.generations[testApp]
	<-g.done
	port := g.port
	if port == 0 || len(o.ports) != 0 {
		t.Fatal(port, o.ports)
	}
	o.node = writeNodeDouble(t, "v24.15.0", false)
	if e := o.Start(context.Background(), testApp); e != nil {
		t.Fatal(e)
	}
	next := o.generations[testApp]
	if next.port == port || next == g {
		t.Fatal("reused retired generation/endpoint")
	}
	waitFile(t, filepath.Join(r.root, testApp, "data/ready"))
}
func TestOwnerStaleAndPersistence(t *testing.T) {
	for _, phase := range []string{"starting", "failed observation", "stop", "newer commit", "removed"} {
		t.Run(phase, func(t *testing.T) {
			r, o := ownerFixture(t)
			ownerBuild(t, r, testApp, "a", "1.0.0", ownerService)
			o.node = writeNodeDouble(t, "v24.15.0", false)
			failure := errors.New("injected persistence failure")
			if phase == "starting" {
				r.persist = func(string, snapshot) error { return failure }
				if e := o.Start(context.Background(), testApp); !errors.Is(e, failure) {
					t.Fatal(e)
				}
				<-o.generations[testApp].done
				if o.generations[testApp].cmd != nil {
					t.Fatal("spawned after write failure")
				}
				r.persist = writeSnapshot
				return
			}
			if phase == "failed observation" {
				r.persist = func(root string, s snapshot) error {
					if s.Records[0].State == "failed" {
						return failure
					}
					return writeSnapshot(root, s)
				}
				if e := os.Remove(filepath.Join(r.root, testApp, "builds", strings.Repeat("a", 64), "app.json")); e != nil {
					t.Fatal(e)
				}
				if e := o.Start(context.Background(), testApp); !errors.Is(e, failure) {
					t.Fatal(e)
				}
				<-o.generations[testApp].done
				o.mu.Lock()
				o.failures = nil
				o.mu.Unlock()
				r.persist = writeSnapshot
				return
			}
			if e := o.Start(context.Background(), testApp); e != nil {
				t.Fatal(e)
			}
			waitFile(t, filepath.Join(r.root, testApp, "data/ready"))
			g := o.generations[testApp]
			if phase == "stop" {
				r.mu.Lock()
				r.persist = func(string, snapshot) error { return failure }
				r.mu.Unlock()
				if e := o.Stop(testApp); !errors.Is(e, failure) {
					t.Fatal(e)
				}
				select {
				case <-g.done:
				default:
					t.Fatal("leaked child")
				}
				v := ownerRecord(t, r, testApp)
				if v.Desired != "available" {
					t.Fatal("claimed stopped intent")
				}
				r.mu.Lock()
				r.persist = writeSnapshot
				r.mu.Unlock()
				return
			}
			if phase == "removed" {
				if _, e := r.Remove(testApp); e != nil {
					t.Fatal(e)
				}
			} else {
				v := ownerRecord(t, r, testApp)
				_, e := r.UpdateLifecycle(testHost, testApp, Lifecycle{Title: "New title", Desired: "stopped", State: "stopped", ActiveRelease: "2.0.0", PendingRelease: "3.0.0"})
				if e != nil {
					t.Fatal(e)
				}
				if v.Revision == ownerRecord(t, r, testApp).Revision {
					t.Fatal("no update")
				}
			}
			before, rev := r.List()
			if e := os.WriteFile(filepath.Join(r.root, testApp, "data/exit"), nil, 0600); e != nil {
				t.Fatal(e)
			}
			<-g.done
			after, next := r.List()
			if rev != next || !reflect.DeepEqual(before, after) {
				t.Fatal("late observation clobbered commit")
			}
		})
	}
}
func TestOwnerStartStopRaces(t *testing.T) {
	r, o := ownerFixture(t)
	ownerBuild(t, r, testApp, "a", "1.0.0", ownerService)
	o.node = writeNodeDouble(t, "v24.15.0", false)
	if stopGrace != 5*time.Second {
		t.Fatal("production grace", stopGrace)
	}
	for i := 0; i < 12; i++ {
		done := make(chan error, 1)
		go func() { done <- o.Start(context.Background(), testApp) }()
		if e := o.Stop(testApp); e != nil && !errors.Is(e, ErrStale) {
			t.Fatal(e)
		}
		<-done
		if e := o.Stop(testApp); e != nil {
			t.Fatal(e)
		}
	}
	done := make(chan error, 1)
	go func() { done <- o.Start(context.Background(), testApp) }()
	if e := o.Shutdown(); e != nil {
		t.Fatal(e)
	}
	<-done
	if e := o.Start(context.Background(), testApp); !errors.Is(e, ErrClosed) {
		t.Fatal(e)
	}
	if e := o.Shutdown(); e != nil {
		t.Fatal(e)
	}
}

func TestOwnerInflightCurrentness(t *testing.T) {
	for _, event := range []string{"stop", "shutdown", "newer lifecycle"} {
		t.Run(event, func(t *testing.T) {
			r, o := ownerFixture(t)
			ownerBuild(t, r, testApp, "a", "1.0.0", ownerService)
			executable, e := os.Executable()
			if e != nil {
				t.Fatal(e)
			}
			base := filepath.Join(t.TempDir(), "version")
			node := filepath.Join(t.TempDir(), "node")
			quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
			script := "#!/bin/sh\nif [ \"$1\" = '--version' ]; then\nexec " + quote(executable) + " -test.run '^TestOwnerHelperProcess$' -- version " + quote(base) + "\nfi\nexec " + quote(executable) + " -test.run '^TestOwnerHelperProcess$' -- \"$@\"\n"
			if e = os.WriteFile(node, []byte(script), 0700); e != nil {
				t.Fatal(e)
			}
			o.node = node
			started := make(chan error, 1)
			go func() { started <- o.Start(context.Background(), testApp) }()
			waitFile(t, base+".entered")
			switch event {
			case "stop":
				if e = o.Stop(testApp); e != nil {
					t.Fatal(e)
				}
			case "shutdown":
				if e = o.Shutdown(); e != nil {
					t.Fatal(e)
				}
			case "newer lifecycle":
				_, e = r.UpdateLifecycle(testHost, testApp, Lifecycle{Title: "Newer", Desired: "stopped", State: "stopped", ActiveRelease: "2.0.0", PendingRelease: "3.0.0"})
				if e != nil {
					t.Fatal(e)
				}
				if e = os.WriteFile(base+".release", nil, 0600); e != nil {
					t.Fatal(e)
				}
			}
			if e = <-started; e == nil {
				t.Fatal("obsolete/cancelled launch succeeded")
			}
			<-o.generations[testApp].done
			if _, e = os.Stat(filepath.Join(r.root, testApp, "data/ready")); e == nil {
				t.Fatal("stale service ran")
			}
			v := ownerRecord(t, r, testApp)
			if event == "newer lifecycle" && (v.Title != "Newer" || v.ActiveRelease != "2.0.0" || v.PendingRelease != "3.0.0" || v.Desired != "stopped") {
				t.Fatal(v)
			}
			if event == "shutdown" && v.Desired != "available" {
				t.Fatal("shutdown changed intent", v)
			}
		})
	}
}
func TestOwnerReplacement(t *testing.T) {
	r, o := ownerFixture(t)
	o.node = writeNodeDouble(t, "v24.15.0", false)
	ownerBuild(t, r, testApp, "a", "1.0.0", ownerService)
	if e := o.Start(context.Background(), testApp); e != nil {
		t.Fatal(e)
	}
	waitFile(t, filepath.Join(r.root, testApp, "data/ready"))
	old := o.generations[testApp]
	ownerBuild(t, r, testApp, "b", "2.0.0", ownerService)
	_, e := r.UpdateLifecycle(testHost, testApp, Lifecycle{Title: "Replacement", Desired: "available", State: "starting", ActiveRelease: "2.0.0"})
	if e != nil {
		t.Fatal(e)
	}
	if e = o.Start(context.Background(), testApp); e != nil {
		t.Fatal(e)
	}
	replacement := o.generations[testApp]
	if replacement == old || replacement.port == old.port {
		t.Fatal("replacement reused ownership")
	}
	select {
	case <-old.done:
	default:
		t.Fatal("old child not retired")
	}
	v := ownerRecord(t, r, testApp)
	if v.Title != "Replacement" || v.ActiveRelease != "2.0.0" || v.State != "starting" {
		t.Fatal(v)
	}
}

func TestOwnerConcurrentStop(t *testing.T) {
	r, o := ownerFixture(t)
	o.node = writeNodeDouble(t, "v24.15.0", false)
	ownerBuild(t, r, testApp, "a", "1.0.0", ownerService)
	if e := o.Start(context.Background(), testApp); e != nil {
		t.Fatal(e)
	}
	waitFile(t, filepath.Join(r.root, testApp, "data/ready"))
	done := make(chan error, 12)
	for i := 0; i < 12; i++ {
		go func() { done <- o.Stop(testApp) }()
	}
	for i := 0; i < 12; i++ {
		if e := <-done; e != nil {
			t.Error(e)
		}
	}
	v := ownerRecord(t, r, testApp)
	if v.Desired != "stopped" || v.State != "stopped" {
		t.Fatal(v)
	}
}

func TestOwnerPostSpawnPersistenceFailure(t *testing.T) {
	for _, event := range []string{"stopped observation", "unexpected exit"} {
		t.Run(event, func(t *testing.T) {
			r, o := ownerFixture(t)
			o.node = writeNodeDouble(t, "v24.15.0", false)
			ownerBuild(t, r, testApp, "a", "1.0.0", ownerService)
			if e := o.Start(context.Background(), testApp); e != nil {
				t.Fatal(e)
			}
			data := filepath.Join(r.root, testApp, "data")
			pid, e := strconv.Atoi(string(waitFile(t, filepath.Join(data, "ready"))))
			if e != nil {
				t.Fatal(e)
			}
			failure := errors.New("post-spawn persistence failure")
			r.mu.Lock()
			r.persist = func(root string, s snapshot) error {
				state := s.Records[0].State
				if (event == "stopped observation" && state == "stopped") || (event == "unexpected exit" && state == "failed") {
					return failure
				}
				return writeSnapshot(root, s)
			}
			r.mu.Unlock()
			if event == "stopped observation" {
				if e = o.Stop(testApp); !errors.Is(e, failure) {
					t.Fatal("stop hid persistence failure", e)
				}
				v := ownerRecord(t, r, testApp)
				if v.Desired != "stopped" || v.State != "starting" {
					t.Fatal("claimed uncommitted observation", v)
				}
			} else {
				if e = os.WriteFile(filepath.Join(data, "exit"), nil, 0600); e != nil {
					t.Fatal(e)
				}
				<-o.generations[testApp].done
				if e = o.Shutdown(); !errors.Is(e, failure) {
					t.Fatal("shutdown hid asynchronous persistence failure", e)
				}
			}
			waitGone(t, pid)
			r.mu.Lock()
			r.persist = writeSnapshot
			r.mu.Unlock()
			o.mu.Lock()
			o.failures = nil
			o.mu.Unlock()
		})
	}
}
