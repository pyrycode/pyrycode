package apps

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func TestOwnerStopAdmission(t *testing.T) {
	r, o := ownerFixture(t)
	o.node = writeNodeDouble(t, "v24.15.0", false)
	ownerBuild(t, r, testApp, "a", "1.0.0", ownerService)
	if err := o.Start(context.Background(), testApp); err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(string(waitFile(t, filepath.Join(r.root, testApp, "data/ready"))))
	if err != nil {
		t.Fatal(err)
	}
	// The worker captured its deferred cancel before publishing ready. Explicit
	// cancellations use the owner lock, so this wrapper has no concurrent reader.
	o.mu.Lock()
	g := o.generations[testApp]
	cancel := g.cancel
	locked := make(chan bool, 1)
	g.cancel = func() {
		registryLocked := !r.mu.TryLock()
		if !registryLocked {
			r.mu.Unlock()
		}
		locked <- registryLocked
		cancel()
	}
	o.mu.Unlock()
	t.Cleanup(func() {
		o.mu.Lock()
		g.cancel = cancel
		o.mu.Unlock()
	})
	stopped := false
	unsubscribe := r.Subscribe(func(change Change) {
		if change.Record != nil && change.Record.Desired == "stopped" && change.Record.State == "stopped" {
			stopped = true
			if !childGone(pid) {
				t.Error("stopped observation committed while owned child survived")
			}
		}
	})
	defer unsubscribe()
	if err = o.Stop(testApp); err != nil {
		t.Fatal(err)
	}
	if !<-locked {
		t.Fatal("stop captured/cancelled ownership outside the durable intent lock")
	}
	if !stopped || !childGone(pid) {
		t.Fatal("successful stop left a live child or omitted stopped observation")
	}
}

func TestOwnerStopCommitAdmission(t *testing.T) {
	for _, ownership := range []string{"empty", "retired"} {
		t.Run(ownership, func(t *testing.T) {
			r, o := ownerFixture(t)
			if ownership == "retired" {
				o.node = writeNodeDouble(t, "v24.15.0", false)
				ownerBuild(t, r, testApp, "a", "1.0.0", ownerService)
				if err := o.Start(context.Background(), testApp); err != nil {
					t.Fatal(err)
				}
				data := filepath.Join(r.root, testApp, "data")
				pid, err := strconv.Atoi(string(waitFile(t, filepath.Join(data, "ready"))))
				if err != nil {
					t.Fatal(err)
				}
				if err = os.WriteFile(filepath.Join(data, "exit"), nil, 0600); err != nil {
					t.Fatal(err)
				}
				<-o.generations[testApp].done
				waitGone(t, pid)
			} else {
				v := ownerRecord(t, r, testApp)
				if _, err := r.UpdateLifecycle(testHost, testApp, Lifecycle{Title: v.Title, Desired: "available", State: "starting", ActiveRelease: v.ActiveRelease}); err != nil {
					t.Fatal(err)
				}
			}
			committed := false
			r.persist = func(root string, next snapshot) error {
				if next.Records[0].Desired == "stopped" && !committed {
					committed = true
					if o.mu.TryLock() {
						o.mu.Unlock()
						t.Error("new generation admission remained open during stopped-intent commit")
					}
				}
				return writeSnapshot(root, next)
			}
			if err := o.Stop(testApp); err != nil {
				t.Fatal(err)
			}
			if !committed || ownerRecord(t, r, testApp).State != "stopped" {
				t.Fatal("stop omitted durable stopped state")
			}
		})
	}
}
