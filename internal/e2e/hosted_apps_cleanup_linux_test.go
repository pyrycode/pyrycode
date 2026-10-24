//go:build e2e && linux

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/apps"
)

func TestHostedAppsFixtureCleanup(t *testing.T) {
	if root := os.Getenv("PYRY_HOSTED_CLEANUP_OWNER"); root != "" {
		home := shortHome(t)
		var owned hostedProcess
		t.Cleanup(func() {
			if _, err := os.Stat(home); err != nil {
				t.Error("home removed before child retirement", err)
			}
			if !hostedRetiredForCleanup(owned.PID) || !hostedRetiredForCleanup(owned.Descendant) {
				t.Error("children remain before fixture files disappear")
				return
			}
			if err := os.WriteFile(filepath.Join(root, "retired-before-removal"), []byte("retired"), 0600); err != nil {
				t.Error(err)
			}
		})
		hostedDisableRelay(t, home)
		seed := hostedSeed(t, home, "cleanup generation")
		registry, err := apps.Open(hostedRoot(home), seed.ServerID)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := registry.UpdateLifecycle(seed.ServerID, seed.Records[3].AppID, apps.Lifecycle{Title: "cleanup sibling", Desired: "stopped", State: "stopped", ActiveRelease: "1.0.0", PendingRelease: "2.0.0"}); err != nil {
			t.Fatal(err)
		}
		env := hostedNodeEnv(t)
		node := strings.TrimPrefix(strings.Split(env[0], ":")[0], "PATH=") + "/node"
		script, err := os.ReadFile(node)
		if err != nil {
			t.Fatal(err)
		}
		wrapper, err := filepath.Abs("../../scripts/test-admission.py")
		if err != nil {
			t.Fatal(err)
		}
		prefix := fmt.Sprintf("else exec env PYRY_HOSTED_LOCK_PROOF=1 PYRY_TEST_ADMISSION_FD=-1 PYRY_TEST_ADMISSION_LOCK='%s/hold' python3 '%s' --", root, wrapper)
		if os.Getenv("PYRY_HOSTED_CLEANUP_FATAL") == "1" {
			script = []byte(strings.Replace(string(script), "else exec", prefix, 1))
		}
		if err := os.WriteFile(node, script, 0700); err != nil {
			t.Fatal(err)
		}
		h := StartInWithEnv(t, home, env, "-pyry-relay=")
		t.Cleanup(func() {
			if t.Failed() {
				t.Log(h.Stderr.String())
				_, b := hostedRead(t, home)
				t.Log(string(b))
			}
		})
		p := hostedProof(t, home, hostedAppID)
		owned = p
		hostedWait(t, func() bool { s, _ := hostedRead(t, home); return s.Records[0].State == "running" })
		hostedHealthy(t, p)
		pg, err := syscall.Getpgid(p.PID)
		if err != nil || pg == h.PID {
			t.Fatalf("hosted group=%d daemon=%d: %v", pg, h.PID, err)
		}
		t.Logf("generation=%s/1.0.0 daemon=%d app=%+v app-group=%d home=%s", hostedAppID, h.PID, p, pg, home)
		b, _ := json.Marshal(struct {
			Process hostedProcess
			Home    string
		}{p, home})
		if err := os.WriteFile(filepath.Join(root, "receipt"), b, 0600); err != nil {
			t.Fatal(err)
		}
		// Freeze the owner before teardown: SIGTERM cannot run Owner.Shutdown.
		if err := h.cmd.Process.Signal(syscall.SIGSTOP); err != nil {
			t.Fatal(err)
		}
		if os.Getenv("PYRY_HOSTED_CLEANUP_FATAL") == "1" {
			t.Fatal("controlled owner failure")
		}
		return
	}
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	pyry := ensurePyryBuilt(t)
	sibling := Start(t)
	for _, fatal := range []string{"0", "1"} {
		t.Run("fatal="+fatal, func(t *testing.T) {
			root := t.TempDir()
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, bin, "-test.v", "-test.run=^TestHostedAppsFixtureCleanup$")
			cmd.Env = append(os.Environ(), "PYRY_HOSTED_CLEANUP_OWNER="+root, "PYRY_HOSTED_CLEANUP_FATAL="+fatal, "GORACE=atexit_sleep_ms=0", "PYRY_E2E_BIN="+pyry)
			log := &safeBuffer{}
			cmd.Stdout, cmd.Stderr = log, log
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			done := make(chan struct{})
			var ownerErr error
			go func() { ownerErr = cmd.Wait(); close(done) }()
			t.Cleanup(func() { cancel(); <-done; t.Log(log.String()) })
			var receipt struct {
				Process hostedProcess
				Home    string
			}
			for {
				select {
				case <-done:
					t.Fatalf("owner exited before receipt: %v\n%s", ownerErr, log.String())
				default:
				}
				b, e := os.ReadFile(filepath.Join(root, "receipt"))
				if e == nil && json.Unmarshal(b, &receipt) == nil {
					break
				}
				select {
				case <-ctx.Done():
					t.Fatal("owner receipt deadline", ctx.Err())
				case <-time.After(10 * time.Millisecond):
				}
			}
			// Handles are captured while the nested owner is alive; emergency cleanup
			// cannot target a later process that reuses one of these PIDs.
			var handles []*os.Process
			for _, pid := range []int{receipt.Process.PID, receipt.Process.Descendant} {
				p, e := os.FindProcess(pid)
				if e != nil {
					t.Fatal(e)
				}
				handles = append(handles, p)
			}
			defer func() {
				for _, p := range handles {
					_ = p.Kill()
					_ = p.Release()
				}
			}()
			var lock os.FileInfo
			if fatal == "1" {
				lock, err = os.Stat(filepath.Join(root, "hold"))
				if err != nil {
					t.Fatal(err)
				}
				for _, pid := range []int{receipt.Process.PID, receipt.Process.Descendant} {
					fds, e := os.ReadDir(fmt.Sprintf("/proc/%d/fd", pid))
					if e != nil {
						t.Fatal(e)
					}
					var held []string
					for _, fd := range fds {
						info, e := os.Stat(fmt.Sprintf("/proc/%d/fd/%s", pid, fd.Name()))
						if e == nil && os.SameFile(lock, info) {
							held = append(held, fd.Name())
						}
					}
					if len(held) == 0 {
						t.Fatalf("pid=%d did not retain isolated lock", pid)
					}
					t.Logf("pid=%d isolated lock descriptors=%v inode=%d", pid, held, lock.Sys().(*syscall.Stat_t).Ino)
				}
			}
			<-done
			err = ownerErr
			t.Log(log.String())
			if ctx.Err() != nil || (fatal == "0" && err != nil) || (fatal == "1" && (err == nil || !strings.Contains(log.String(), "controlled owner failure"))) {
				t.Fatalf("owner outcome: %v %v", err, ctx.Err())
			}
			if _, e := os.Stat(receipt.Home); !os.IsNotExist(e) {
				t.Fatalf("fixture home remains: %v", e)
			}
			if _, e := os.Stat(filepath.Join(root, "retired-before-removal")); e != nil {
				t.Error("missing retirement proof before fixture removal", e)
			}
			if !processAlive(sibling.PID) {
				t.Error("unrelated fixture stopped")
			}
			for _, pid := range []int{receipt.Process.PID, receipt.Process.Descendant} {
				if !hostedRetiredForCleanup(pid) {
					t.Errorf("owned pid=%d survived fixture cleanup", pid)
				}
			}
			if fatal == "1" {
				fresh, stop := context.WithTimeout(context.Background(), 3*time.Second)
				defer stop()
				probe := exec.CommandContext(fresh, "python3", "../../scripts/test-admission.py", "--", "true")
				probe.Env = append(os.Environ(), "PYRY_TEST_ADMISSION_LOCK="+filepath.Join(root, "hold"), "PYRY_TEST_ADMISSION_FD=-1")
				if out, e := probe.CombinedOutput(); e != nil {
					t.Errorf("fresh independent admission: %v: %s", e, out)
				}
				after, e := os.Stat(filepath.Join(root, "hold"))
				if e != nil || !os.SameFile(lock, after) {
					t.Fatal("admission lock inode changed", e)
				}
				t.Logf("original lock inode=%d; process receipt=%+v", lock.Sys().(*syscall.Stat_t).Ino, receipt)
			}
		})
	}
}
