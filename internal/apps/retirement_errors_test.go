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

func TestGenerationRetirementErrors(t *testing.T) {
	for _, origin := range []string{"ordinary", "published"} {
		for _, event := range []string{"shutdown", "stop", "remove", "completed", "replaced", "removed"} {
			t.Run(origin+"/"+event, func(t *testing.T) {
				var r *Registry
				var o *Owner
				var tools Toolchain
				if origin == "published" {
					r, o, tools, _, _ = publishFixture(t, healthReply{Body: healthyBody(), Descendant: true}, "ok")
				} else {
					r, o = healthFixture(t, healthReply{Body: healthyBody(), Descendant: true})
					o.startup = startupDeadline
				}
				denied := errors.New("controlled retirement denial")
				var group atomic.Int64
				o.kill = func(pid int, sig syscall.Signal) error {
					err := syscall.Kill(pid, sig)
					if int64(pid) == group.Load() && sig == syscall.SIGTERM {
						return errors.Join(denied, syscall.EPERM, err)
					}
					return err
				}
				if origin == "published" {
					if _, err := o.Publish(context.Background(), testHost, testApp, tools); err != nil {
						t.Fatal(err)
					}
				} else {
					if err := o.Start(context.Background(), testApp); err != nil {
						t.Fatal(err)
					}
				}
				target := healthTarget(t, o)
				o.mu.Lock()
				g := o.generations[testApp]
				o.mu.Unlock()
				data := filepath.Join(r.root, testApp, "data")
				desc, err := strconv.Atoi(string(waitFile(t, filepath.Join(data, "descendant"))))
				if err != nil || desc < 1 {
					t.Fatal("invalid descendant", desc, err)
				}
				// The injection applies only to this group, including after replacement.
				// Deliver the signal so failure assertions cannot conceal live writers.
				group.Store(int64(-g.cmd.Process.Pid))
				t.Cleanup(func() {
					_ = o.Shutdown()
					o.mu.Lock()
					o.failures = nil
					o.mu.Unlock()
				})
				switch event {
				case "shutdown":
					err = o.Shutdown()
				case "stop", "replaced", "removed":
					err = o.Stop(testApp)
				case "remove":
					var removed bool
					removed, err = r.Remove(testApp)
					if removed {
						t.Fatal("removal discarded retirement failure")
					}
				case "completed":
					if err = os.WriteFile(filepath.Join(data, "exit"), nil, 0600); err != nil {
						t.Fatal(err)
					}
					<-g.done
					err = o.Shutdown()
				}
				if !errors.Is(err, denied) || !errors.Is(err, syscall.EPERM) {
					t.Errorf("retirement error lost: %v", err)
				}
				<-g.done
				waitGone(t, g.cmd.Process.Pid)
				waitGone(t, desc)
				if err := healthUse(target); err == nil {
					t.Error("retired target remained usable")
				}
				if event == "remove" {
					s := publishDisk(t, r)
					if len(s.Records) != 1 || len(s.Tombstones) != 0 || s.Records[0].Desired != "stopped" {
						t.Fatal("removal lost registration", s)
					}
				}
				if event == "replaced" || event == "removed" {
					if err := o.Start(context.Background(), testApp); err != nil {
						t.Fatal(err)
					}
					fresh := healthTarget(t, o)
					o.mu.Lock()
					next := o.generations[testApp]
					o.mu.Unlock()
					if next == g || healthUse(fresh) != nil || healthUse(target) == nil {
						t.Fatal("replacement did not retire target identity")
					}
					if event == "removed" {
						var nextDesc int
						healthWait(t, func() bool {
							b, err := os.ReadFile(filepath.Join(data, "descendant"))
							nextDesc, _ = strconv.Atoi(string(b))
							return err == nil && nextDesc > 0 && nextDesc != desc
						})
						if removed, err := r.Remove(testApp); !removed || err != nil {
							t.Fatal("healthy replacement removal failed", removed, err)
						}
						s := publishDisk(t, r)
						if len(s.Records) != 0 || len(s.Tombstones) != 1 || healthUse(fresh) == nil {
							t.Fatal("removed generation remained usable", s)
						}
						waitGone(t, next.cmd.Process.Pid)
						waitGone(t, nextDesc)
					}
				}
				for i := 0; i < 2; i++ {
					if err := o.Shutdown(); !errors.Is(err, denied) || !errors.Is(err, syscall.EPERM) {
						t.Errorf("shutdown %d lost completed generation error: %v", i, err)
					}
					o.mu.Lock()
					count := 0
					if o.failures != nil {
						count = strings.Count(o.failures.Error(), denied.Error())
					}
					o.mu.Unlock()
					if count != 1 {
						t.Errorf("retirement error aggregated %d times, want once", count)
					}
				}
			})
		}
	}
}

func TestPublishServiceRetirementUncertain(t *testing.T) {
	for _, event := range []string{"stop", "remove", "shutdown"} {
		t.Run(event, func(t *testing.T) {
			r, o, tools, _, _ := publishFixture(t, healthReply{Body: `{}`, Descendant: true}, "ok")
			var group atomic.Int64
			o.kill = func(pid int, sig syscall.Signal) error {
				err := syscall.Kill(pid, sig)
				if int64(pid) == group.Load() && sig == syscall.SIGTERM {
					return errors.Join(syscall.EPERM, err)
				}
				return err
			}
			testArtifactWrite(t, r.root, testApp+"/data/sentinel", "before")
			done := make(chan error, 1)
			go func() { _, err := o.Publish(context.Background(), testHost, testApp, tools); done <- err }()
			data := filepath.Join(r.root, testApp, "data")
			waitFile(t, filepath.Join(data, "ready"))
			o.mu.Lock()
			g := o.generations[testApp]
			o.mu.Unlock()
			desc, err := strconv.Atoi(string(waitFile(t, filepath.Join(data, "descendant"))))
			if err != nil || desc < 1 {
				t.Fatal(desc, err)
			}
			group.Store(int64(-g.cmd.Process.Pid))
			switch event {
			case "stop":
				err = o.Stop(testApp)
			case "remove":
				_, err = r.Remove(testApp)
			case "shutdown":
				err = o.Shutdown()
			}
			if !errors.Is(err, syscall.EPERM) {
				t.Errorf("retirement failure lost: %v", err)
			}
			var failure *PublishFailure
			if err := <-done; !errors.As(err, &failure) || failure.Recovery != "Recovery is pending; retry is barred." {
				t.Fatal("unsafe publication outcome", err)
			}
			<-g.done
			waitGone(t, g.cmd.Process.Pid)
			waitGone(t, desc)
			s := publishDisk(t, r)
			if len(s.Records) != 1 {
				t.Fatal("registration lost during failed retirement", s)
			}
			v := s.Records[0]
			if len(s.Tombstones) != 0 || v.ActiveRelease != "" || v.PublishJournal == nil || v.PublishJournal.ActivationCommitted || v.LastError == nil || v.LastError.Code != "app.io_failed" {
				t.Fatal("recovery evidence lost", s)
			}
			if _, err := os.Stat(filepath.Join(data, "migration")); err != nil {
				t.Fatal("restored despite uncertain retirement", err)
			}
			if _, err := o.Lookup(testHost, testApp); err == nil {
				t.Fatal("private candidate exposed")
			}
			if err := o.Shutdown(); !errors.Is(err, syscall.EPERM) {
				t.Error("shutdown lost preactivation retirement failure", err)
			}
		})
	}
}
