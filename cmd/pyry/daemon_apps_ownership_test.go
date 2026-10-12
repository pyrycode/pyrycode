package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/apps"
	"github.com/pyrycode/pyrycode/internal/control"
	"github.com/pyrycode/pyrycode/internal/conversations"
)

func daemonAppSnapshot(t *testing.T, root string) ([]apps.Record, []byte) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, "registry.json"))
	var snapshot struct {
		Records []apps.Record `json:"records"`
	}
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &snapshot); err != nil {
		t.Fatal(err)
	}
	return snapshot.Records, b
}

func daemonAppSupervisorArgs(t *testing.T, socket string) []string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(resolveConfigPath()), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(resolveConfigPath(), []byte(`{"relay_url":""}`), 0600); err != nil {
		t.Fatal(err)
	}
	bin := stubBinary(t)
	return []string{"-pyry-name=test", "-pyry-workdir=" + os.Getenv("HOME"), "-pyry-socket=" + socket, "-pyry-codex=" + bin, "-pyry-claude=" + bin}
}

func TestDaemonAppsRejectedDuplicateStartup(t *testing.T) {
	r, node, root := daemonOwnerFixture(t)
	t.Setenv("PATH", filepath.Dir(node)+":"+os.Getenv("PATH"))
	t.Setenv("PYRY_RELAY_URL", "")
	socket := filepath.Join(shortTempDir(t), "daemon.sock")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	incumbent := newDaemonAppsOwner(r, node, discardLogger())
	if incumbent == nil {
		t.Fatal("owner unavailable")
	}
	defer func() {
		if err := incumbent.Shutdown(); err != nil {
			t.Error(err)
		}
	}()
	daemonAwait(t, func() bool {
		rows, _ := daemonAppSnapshot(t, root)
		return rows[0].State == "running" && rows[1].State == "running"
	})
	original := daemonProcesses(t, root, daemonAppID)
	_, before := daemonAppSnapshot(t, root)
	if err := runSupervisor(daemonAppSupervisorArgs(t, socket)); !errors.Is(err, control.ErrInstanceRunning) {
		t.Fatalf("duplicate startup: %v", err)
	}
	_, after := daemonAppSnapshot(t, root)
	if !bytes.Equal(before, after) {
		t.Error("rejected startup changed incumbent registry observations or revisions")
	}
	if proof := daemonProcesses(t, root, daemonAppID); proof["pid"] != original["pid"] {
		t.Error("rejected startup launched another generation")
	}
	rows, _ := r.List()
	if _, err := incumbent.Lookup(rows[0].Manifest.ServerID(), rows[0].AppID); err != nil {
		t.Fatal("incumbent target unavailable", err)
	}
	// Rejection must also leave a previously absent app root and identity absent.
	t.Setenv("HOME", testRealTempDir(t))
	if err := runSupervisor(daemonAppSupervisorArgs(t, socket)); !errors.Is(err, control.ErrInstanceRunning) {
		t.Fatal(err)
	}
	for _, path := range []string{resolveServerIDPath("test"), filepath.Join(resolveInstanceDirPath("test"), "apps")} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("rejected startup created app storage: %s: %v", path, err)
		}
	}
}

func TestDaemonAppsShutdownRetainsOwnership(t *testing.T) {
	for _, path := range []string{"normal", "startup-error"} {
		t.Run(path, func(t *testing.T) {
			_, node, root := daemonOwnerFixture(t)
			t.Setenv("PATH", filepath.Dir(node)+":"+os.Getenv("PATH"))
			t.Setenv("PYRY_RELAY_URL", "")
			data := filepath.Join(root, daemonAppID, "data")
			ids := []string{daemonAppID, "33333333-3333-4333-8333-333333333333"}
			for _, id := range ids {
				dir := filepath.Join(root, id, "data")
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "hold-shutdown"), nil, 0600); err != nil {
					t.Fatal(err)
				}
			}
			unblockChildren := func() {
				for _, id := range ids {
					if err := os.WriteFile(filepath.Join(root, id, "data/release-shutdown"), nil, 0600); err != nil {
						t.Error(err)
					}
				}
			}
			socket := filepath.Join(shortTempDir(t), "daemon.sock")
			args := daemonAppSupervisorArgs(t, socket)
			expected := errors.New("delivery startup failure")
			entered, release := make(chan struct{}), make(chan struct{})
			done := make(chan error, 1)
			go func() {
				done <- runSupervisor(args, func(p string, h channelDeliveryHistory, carry func(conversations.ConversationID, string), log *slog.Logger) (*channelDelivery, error) {
					close(entered)
					<-release
					if path == "startup-error" {
						return nil, expected
					}
					return newChannelDelivery(p, h, carry, log)
				})
			}()
			joined, released := false, false
			t.Cleanup(func() {
				unblockChildren()
				if !released {
					close(release)
				}
				if !joined {
					ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
					defer cancel()
					_ = control.Stop(ctx, socket)
					select {
					case <-done:
					case <-ctx.Done():
						t.Error("daemon cleanup did not join")
					}
				}
			})
			select {
			case <-entered:
			case <-time.After(8 * time.Second):
				t.Fatal("startup did not reach delivery composition")
			}
			daemonAwait(t, func() bool {
				rows, _ := daemonAppSnapshot(t, root)
				return rows[0].State == "running" && rows[1].State == "running"
			})
			proof := daemonProcesses(t, root, daemonAppID)
			sibling := daemonProcesses(t, root, "33333333-3333-4333-8333-333333333333")
			close(release)
			released = true
			if path == "normal" {
				ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
				defer cancel()
				if err := control.Stop(ctx, socket); err != nil {
					t.Fatal(err)
				}
			}
			daemonAwait(t, func() bool { _, err := os.Stat(filepath.Join(data, "shutdown-started")); return err == nil })
			for _, pid := range proof {
				if err := syscall.Kill(pid, 0); err != nil {
					t.Fatal("cleanup barrier did not retain owned children", err)
				}
			}
			_, before := daemonAppSnapshot(t, root)
			if err := runSupervisor(args, func(string, channelDeliveryHistory, func(conversations.ConversationID, string), *slog.Logger) (*channelDelivery, error) {
				return nil, errors.New("replacement admitted during shutdown")
			}); !errors.Is(err, control.ErrInstanceRunning) {
				t.Errorf("ownership released before app shutdown: %v", err)
			}
			_, after := daemonAppSnapshot(t, root)
			if !bytes.Equal(before, after) {
				t.Error("replacement startup changed storage during app shutdown")
			}
			unblockChildren()
			select {
			case err := <-done:
				joined = true
				if (path == "normal" && err != nil) || (path == "startup-error" && !errors.Is(err, expected)) {
					t.Fatal(err)
				}
			case <-time.After(8 * time.Second):
				t.Fatal("shutdown did not join app workers")
			}
			daemonAssertGone(t, proof)
			daemonAssertGone(t, sibling)
			rows, _ := daemonAppSnapshot(t, root)
			for _, row := range rows {
				if row.State != "stopped" || row.Desired != "available" {
					t.Fatal("final observation or available intent missing", rows)
				}
			}
			replacement := control.NewServer(socket, poolResolver{}, nil, nil, discardLogger(), nil)
			if err := replacement.Listen(); err != nil {
				t.Fatal("claim not released after final observations", err)
			}
			defer replacement.Close()
		})
	}
}
