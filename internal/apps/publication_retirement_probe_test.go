package apps

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func TestPublishRetirementProbeSequence(t *testing.T) {
	for _, mode := range []string{"native observation", "permission until kill", "permission after kill", "persistent permission"} {
		t.Run(mode, func(t *testing.T) {
			r, o, tools, _, _ := publishFixture(t, healthReply{Body: `{}`, Descendant: true}, "ok")
			var group atomic.Int64
			var killed atomic.Bool
			var probes atomic.Int64
			var descendant atomic.Int64
			o.kill = func(pid int, sig syscall.Signal) error {
				err := syscall.Kill(pid, sig)
				if int64(pid) != group.Load() {
					return err
				}
				if sig == syscall.SIGKILL {
					killed.Store(true)
					if mode == "native observation" {
						// Deliberately sample the post-kill window before the next helper.
						last := ""
						deadline := time.Now().Add(time.Second)
						for time.Now().Before(deadline) {
							probe := syscall.Kill(pid, 0)
							value := "live"
							if probe != nil {
								value = probe.Error()
							}
							if value != last {
								t.Logf("post-kill group probe: %s; leader=%v descendant=%v", value, syscall.Kill(-pid, 0), syscall.Kill(int(descendant.Load()), 0))
								if errors.Is(probe, syscall.EPERM) {
									state, stateErr := exec.Command("ps", "-o", "pid=,pgid=,stat=", "-p", strconv.FormatInt(descendant.Load(), 10)).CombinedOutput()
									t.Logf("post-EPERM descendant ps (later sample): %q error=%v", state, stateErr)
								}
								last = value
							}
							if errors.Is(probe, syscall.ESRCH) {
								break
							}
							time.Sleep(50 * time.Microsecond)
						}
					}
				}
				if sig == 0 && (mode == "persistent permission" || mode == "permission until kill" && !killed.Load()) {
					probes.Add(1)
					return syscall.EPERM
				}
				if sig == 0 && mode == "permission after kill" && probes.Add(1) <= 5 {
					return syscall.EPERM
				}
				return err
			}
			done := make(chan error, 1)
			go func() { _, err := o.Publish(context.Background(), testHost, testApp, tools); done <- err }()
			data := filepath.Join(r.root, testApp, "data")
			leader, err := strconv.Atoi(string(waitFile(t, filepath.Join(data, "ready"))))
			if err != nil || leader < 1 {
				t.Fatal(leader, err)
			}
			desc, err := strconv.Atoi(string(waitFile(t, filepath.Join(data, "descendant"))))
			if err != nil || desc < 1 {
				t.Fatal(desc, err)
			}
			if syscall.Kill(desc, 0) != nil {
				t.Fatal("fixture did not establish a live descendant")
			}
			descendant.Store(int64(desc))
			group.Store(int64(-leader))
			err = o.Stop(testApp)
			if mode == "persistent permission" {
				if !errors.Is(err, syscall.EPERM) {
					t.Errorf("persistent uncertainty lost: %v", err)
				}
			} else if err != nil {
				t.Errorf("transient inspection survived positive retirement: %v", err)
			}
			if err := <-done; err == nil {
				t.Fatal("retired publication succeeded")
			}
			waitGone(t, leader)
			waitGone(t, desc)
			v := publishDisk(t, r).Records[0]
			pending := mode == "persistent permission"
			if v.RetirementPending != pending || (v.PublishJournal != nil) != pending {
				t.Errorf("wrong retirement evidence: pending=%v journal=%v", v.RetirementPending, v.PublishJournal != nil)
			}
			if pending {
				if v.LastError == nil || v.LastError.Code != "app.io_failed" {
					t.Error("unsafe failure metadata", v.LastError)
				}
				if _, err := o.Publish(context.Background(), testHost, testApp, tools); err == nil {
					t.Error("uncertain retirement admitted retry")
				}
				if _, err := r.Remove(testApp); err == nil {
					t.Error("uncertain retirement admitted removal")
				}
			}
			if _, err := o.Lookup(testHost, testApp); err == nil {
				t.Error("retired target available")
			}
			if !killed.Load() || mode == "permission after kill" && probes.Load() < 6 {
				t.Error("did not exercise forced retirement and post-kill uncertainty")
			}
			t.Logf("sequence=%s injected-probes=%d forced-kill=%v", mode, probes.Load(), killed.Load())
		})
	}
}
