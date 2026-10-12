package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/apps"
	"github.com/pyrycode/pyrycode/internal/conversations"
)

func TestDaemonAppProcess(t *testing.T) {
	if len(os.Args) < 2 || os.Args[len(os.Args)-1] != "daemon-app-child" {
		return
	}
	os.Exit(daemonAppProcess())
}

func daemonAppProcess() (exitCode int) {
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM)
	defer signal.Stop(sig)
	child := exec.Command("/bin/sleep", "99999")
	if err := child.Start(); err != nil {
		return 2
	}
	data := os.Getenv("PYRY_APP_DATA_DIR")
	defer func() {
		_ = child.Process.Kill() // Best effort if group termination already killed it.
		_ = child.Wait()         // A signal exit still reaps the descendant.
		if err := os.WriteFile(filepath.Join(data, "descendant-reaped"), nil, 0600); err != nil {
			exitCode = 7
		}
	}()
	server := &http.Server{Addr: "127.0.0.1:" + os.Getenv("PYRY_APP_PORT"), ReadHeaderTimeout: time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"ready": true, "app_id": os.Getenv("PYRY_APP_ID"), "release_version": os.Getenv("PYRY_APP_RELEASE")})
	})}
	stop, stopped := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(stopped)
		select {
		case <-sig:
		case <-stop:
			return
		}
		if _, err := os.Stat(filepath.Join(data, "hold-shutdown")); err == nil {
			if err := os.WriteFile(filepath.Join(data, "shutdown-started"), nil, 0600); err != nil {
				os.Exit(5)
			}
			for {
				if _, err := os.Stat(filepath.Join(data, "release-shutdown")); err == nil {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
		}
		_ = server.Close()
	}()
	defer func() { close(stop); <-stopped }()
	proof, _ := json.Marshal(map[string]int{"pid": os.Getpid(), "descendant": child.Process.Pid})
	// Publish complete PID readiness only after termination and reaping are armed.
	path := filepath.Join(data, "process.json")
	if err := os.WriteFile(path+".tmp", proof, 0600); err != nil {
		return 3
	}
	if err := os.Rename(path+".tmp", path); err != nil {
		return 3
	}
	if err := daemonAppStartupPause(data, stopped); err != nil {
		return 6
	}
	_ = server.ListenAndServe()
	return 0
}

// The bounded gate lets the parent terminate a fixture immediately after its
// PID publication, while service startup has not completed.
func daemonAppStartupPause(data string, stopped <-chan struct{}) error {
	if _, err := os.Stat(filepath.Join(data, "hold-startup")); err != nil {
		return nil
	}
	if err := os.WriteFile(filepath.Join(data, "startup-paused"), nil, 0600); err != nil {
		return err
	}
	select {
	case <-stopped:
		return os.WriteFile(filepath.Join(data, "startup-interrupted"), nil, 0600)
	case <-time.After(5 * time.Second):
	}
	return nil
}

func daemonOwnerFixture(t *testing.T) (*apps.Registry, string, string) {
	t.Helper()
	t.Setenv("HOME", testRealTempDir(t))
	r := openDaemonApps("test", discardLogger())
	if r == nil {
		t.Fatal("registry unavailable")
	}
	root := filepath.Join(resolveInstanceDirPath("test"), "apps")
	host, err := os.ReadFile(resolveServerIDPath("test"))
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{daemonAppID, "33333333-3333-4333-8333-333333333333"} {
		manifest := bytes.ReplaceAll(daemonAppManifest(strings.TrimSpace(string(host))), []byte(daemonAppID), []byte(id))
		if _, err := r.Register(manifest); err != nil {
			t.Fatal(err)
		}
		build := filepath.Join(root, id, "builds", strings.Repeat("a", 64))
		if err := os.MkdirAll(filepath.Join(build, "dist/service"), 0700); err != nil {
			t.Fatal(err)
		}
		for path, data := range map[string][]byte{"app.json": manifest, "dist/service/main.js": []byte("fixture")} {
			if err := os.WriteFile(filepath.Join(build, path), data, 0600); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := r.UpdateLifecycle(strings.TrimSpace(string(host)), id, apps.Lifecycle{Title: "Committed", Desired: "available", State: "starting", ActiveRelease: "1.0.0"}); err != nil {
			t.Fatal(err)
		}
	}
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	node := filepath.Join(t.TempDir(), "node")
	script := fmt.Sprintf("#!/bin/sh\nif [ \"$1\" = --version ]; then echo v24.15.0; else exec '%s' -test.run='^TestDaemonAppProcess$' -- daemon-app-child; fi\n", strings.ReplaceAll(bin, "'", "'\\''"))
	if err := os.WriteFile(node, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	return r, node, root
}

func daemonAwait(t *testing.T, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("app lifecycle deadline")
}
func daemonProcesses(t *testing.T, root, id string) map[string]int {
	t.Helper()
	var proof map[string]int
	daemonAwait(t, func() bool {
		b, err := os.ReadFile(filepath.Join(root, id, "data/process.json"))
		return err == nil && json.Unmarshal(b, &proof) == nil
	})
	return proof
}
func daemonAssertGone(t *testing.T, proof map[string]int) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		gone := true
		for _, pid := range proof {
			if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
				gone = false
			}
		}
		if gone {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	for role, pid := range proof {
		if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
			state, stateErr := exec.Command("ps", "-o", "stat=", "-p", fmt.Sprint(pid)).Output()
			t.Errorf("app cleanup: role=%s pid=%d state=%q probe=%v ps=%v", role, pid, strings.TrimSpace(string(state)), err, stateErr)
		}
	}
}
func TestDaemonAppsOwnerLifetime(t *testing.T) {
	r, node, root := daemonOwnerFixture(t)
	events := make(chan apps.Change, 32)
	detach := r.Subscribe(func(c apps.Change) { events <- c })
	defer detach()
	o := newDaemonAppsOwner(r, node, discardLogger())
	if o == nil {
		t.Fatal("owner unavailable")
	}
	t.Cleanup(func() {
		if err := o.Shutdown(); err != nil {
			t.Error(err)
		}
	})
	daemonAwait(t, func() bool { rows, _ := r.List(); return rows[0].State == "running" && rows[1].State == "running" })
	proof := daemonProcesses(t, root, daemonAppID)
	select {
	case c := <-events:
		if c.Record == nil || c.Record.State != "running" {
			t.Fatal(c)
		}
	default:
		t.Fatal("owner observations missing")
	}
	if err := o.Stop(daemonAppID); err != nil {
		t.Fatal(err)
	}
	if err := o.Stop(daemonAppID); err != nil {
		t.Fatal(err)
	}
	daemonAssertGone(t, proof)
	rows, _ := r.List()
	if rows[0].Desired != "stopped" || rows[0].State != "stopped" || rows[1].State != "running" {
		t.Fatal(rows)
	}
	target, err := o.Lookup(rows[1].Manifest.ServerID(), rows[1].AppID)
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://unused/_pyry/health", nil)
	res, err := target.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	sibling := daemonProcesses(t, root, rows[1].AppID)
	if err := o.Shutdown(); err != nil {
		t.Fatal(err)
	}
	daemonAssertGone(t, sibling)
	rows, _ = r.List()
	if rows[1].Desired != "available" {
		t.Fatal(rows)
	}
	if newDaemonAppsOwner(nil, node, discardLogger()) != nil {
		t.Fatal("nil storage accepted")
	}
	if newDaemonAppsOwner(r, filepath.Join(root, "missing"), discardLogger()) != nil {
		t.Fatal("missing runtime accepted")
	}
}
func TestHostedAppsStartupFailureCleanup(t *testing.T) {
	for _, mode := range []string{"normal", "publication-pause"} {
		t.Run(mode, func(t *testing.T) {
			_, node, root := daemonOwnerFixture(t)
			t.Setenv("PATH", filepath.Dir(node)+":"+os.Getenv("PATH"))
			if mode == "publication-pause" {
				for _, id := range []string{daemonAppID, "33333333-3333-4333-8333-333333333333"} {
					data := filepath.Join(root, id, "data")
					if err := os.MkdirAll(data, 0700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(data, "hold-startup"), nil, 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			expected := errors.New("delivery failure")
			var proofs []map[string]int
			err := runSupervisor([]string{"-pyry-name=test", "-pyry-workdir=" + os.Getenv("HOME"), "-pyry-socket=" + filepath.Join(shortTempDir(t), "control.sock"), "-pyry-relay=", "-pyry-claude=/bin/sleep"}, func(_ string, _ channelDeliveryHistory, _ func(conversations.ConversationID, string), _ *slog.Logger) (*channelDelivery, error) {
				for _, id := range []string{daemonAppID, "33333333-3333-4333-8333-333333333333"} {
					proofs = append(proofs, daemonProcesses(t, root, id))
					if mode == "publication-pause" {
						daemonAwait(t, func() bool { _, err := os.Stat(filepath.Join(root, id, "data/startup-paused")); return err == nil })
					}
				}
				return nil, expected
			})
			if !errors.Is(err, expected) {
				t.Fatal(err)
			}
			for _, proof := range proofs {
				daemonAssertGone(t, proof)
			}
			for i, id := range []string{daemonAppID, "33333333-3333-4333-8333-333333333333"} {
				if mode == "publication-pause" {
					if _, err := os.Stat(filepath.Join(root, id, "data/startup-interrupted")); err != nil {
						t.Errorf("app %s shutdown did not interrupt its startup pause: %v", id, err)
					}
				}
				if _, err := os.Stat(filepath.Join(root, id, "data/descendant-reaped")); err != nil {
					t.Errorf("app %s did not acknowledge descendant reaping: %v", id, err)
					for role, pid := range proofs[i] {
						state, _ := exec.Command("ps", "-o", "stat=", "-p", fmt.Sprint(pid)).Output()
						t.Logf("role=%s pid=%d state=%q probe=%v", role, pid, strings.TrimSpace(string(state)), syscall.Kill(pid, 0))
					}
				}
			}
		})
	}
}
