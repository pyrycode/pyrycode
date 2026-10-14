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

func TestSnapshotDurabilityBoundary(t *testing.T) {
	for _, fail := range []string{"", "file sync", "rename", "directory sync"} {
		t.Run(fail, func(t *testing.T) {
			r := testOpen(t)
			testRegister(t, r, []byte(testManifest))
			before := testBytes(t, filepath.Join(r.root, "registry.json"))
			next := cloneSnapshot(r.snapshot)
			next.Revision++
			next.Records[0].Revision = next.Revision
			next.Records[0].Title = "Next"
			var order []string
			injected := errors.New("SECRET registry boundary")
			ops := snapshotOps{sync: func(f *os.File) error {
				phase := "file sync"
				if info, e := f.Stat(); e == nil && info.IsDir() {
					phase = "directory sync"
					if publishDisk(t, r).Records[0].Title != "Next" {
						t.Error("directory sync preceded rename")
					}
				}
				order = append(order, phase)
				if phase == fail {
					return injected
				}
				return f.Sync()
			}, rename: func(from, to string) error {
				order = append(order, "rename")
				if fail == "rename" {
					return injected
				}
				return os.Rename(from, to)
			}}
			err := ops.write(r.root, next)
			want := []string{"file sync", "rename", "directory sync"}
			if fail == "file sync" {
				want = want[:1]
			} else if fail == "rename" {
				want = want[:2]
			}
			if !reflect.DeepEqual(order, want) || (err == nil) != (fail == "") || errors.Is(err, errSnapshotUncertain) != (fail == "directory sync") {
				t.Fatal(order, want, err)
			}
			if fail != "" && !errors.Is(err, injected) {
				t.Fatal("lost storage cause", err)
			}
			if fail == "file sync" || fail == "rename" {
				if !reflect.DeepEqual(before, testBytes(t, filepath.Join(r.root, "registry.json"))) {
					t.Fatal("pre-rename failure changed registry")
				}
			}
		})
	}
}

func TestPublishJournalDirectorySyncOrdering(t *testing.T) {
	r, o, tools, _, _ := publishFixture(t, healthReply{Body: healthyBody()}, "ok")
	entered, release := make(chan struct{}), make(chan struct{})
	r.mu.Lock()
	r.persist = func(root string, s snapshot) error {
		journal := s.Records[0].PublishJournal
		return (snapshotOps{sync: func(f *os.File) error {
			info, e := f.Stat()
			if e != nil {
				return e
			}
			if info.IsDir() && journal != nil && !journal.ActivationCommitted {
				close(entered)
				<-release
			}
			return f.Sync()
		}}).write(root, s)
	}
	r.mu.Unlock()
	done := make(chan error, 1)
	go func() { _, e := o.Publish(context.Background(), testHost, testApp, tools); done <- e }()
	select {
	case <-entered:
		if _, e := os.Stat(filepath.Join(r.root, testApp, "data")); !os.IsNotExist(e) {
			t.Error("data preparation preceded directory sync", e)
		}
		if j := publishDisk(t, r).Records[0].PublishJournal; j == nil || j.ActivationCommitted {
			t.Error("journal absent at directory sync", j)
		}
	case e := <-done:
		close(release)
		t.Fatal("publication passed directory durability boundary without sync", e)
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("journal sync never reached")
	}
	close(release)
	if e := <-done; e != nil {
		t.Fatal(e)
	}
}

func TestPublishDurabilityUncertain(t *testing.T) {
	for _, phase := range []string{"journal", "activation", "finalization", "failure finalization"} {
		t.Run(phase, func(t *testing.T) {
			migration := "ok"
			if phase == "failure finalization" {
				migration = "fail"
			}
			r, o, tools, c, _ := publishFixture(t, healthReply{Body: healthyBody(), Descendant: true}, migration)
			r.mu.Lock()
			r.persist = func(root string, s snapshot) error {
				v := s.Records[0]
				fail := phase == "journal" && v.PublishJournal != nil && !v.PublishJournal.ActivationCommitted || phase == "activation" && v.ActiveRelease != "" || phase == "finalization" && v.ActiveRelease != "" && v.PublishJournal == nil || phase == "failure finalization" && v.State == "failed" && v.PublishJournal == nil
				return (snapshotOps{sync: func(f *os.File) error {
					info, e := f.Stat()
					if e == nil && info.IsDir() && fail {
						return errors.New("SECRET directory sync " + root)
					}
					return f.Sync()
				}}).write(root, s)
			}
			r.mu.Unlock()
			result, err := o.Publish(context.Background(), testHost, testApp, tools)
			if err == nil || !result.RecoveryPending || result.Healthy || strings.Contains(err.Error(), "SECRET") || strings.Contains(err.Error(), r.root) {
				t.Fatal(result, err)
			}
			v := publishDisk(t, r).Records[0]
			if phase == "activation" || phase == "finalization" {
				if v.ActiveRelease != "1.0.0" || result.ActivationCommitted != (phase == "finalization") {
					t.Fatal("ambiguous activation decision lost", result, v)
				}
				if _, e := os.Stat(filepath.Join(r.root, testApp, "data/migration")); e != nil {
					t.Fatal("activation uncertainty restored old data", e)
				}
			} else if _, e := os.Stat(filepath.Join(r.root, testApp, "data")); !os.IsNotExist(e) {
				t.Fatal("journal refusal mutated data / failure recovery incomplete", e)
			}
			if phase == "journal" && (v.PublishJournal == nil || v.PublishJournal.ActivationCommitted) {
				t.Fatal("uncommitted evidence absent", v)
			}
			if _, e := o.Lookup(testHost, testApp); e == nil {
				t.Fatal("uncertain target exposed")
			}
			if g := recoveryGeneration(o, testApp); g != nil && phase == "activation" {
				waitGone(t, g.cmd.Process.Pid)
				pid, e := strconv.Atoi(string(testBytes(t, filepath.Join(r.root, testApp, "data/descendant"))))
				if e != nil {
					t.Fatal(e)
				}
				waitGone(t, pid)
			}
			before, log := testBytes(t, filepath.Join(r.root, "registry.json")), testBytes(t, c.Log)
			if _, e := o.Publish(context.Background(), testHost, testApp, tools); e == nil || !reflect.DeepEqual(log, testBytes(t, c.Log)) {
				t.Fatal("uncertain registry admitted another build", e)
			}
			if _, e := r.UpdatePublish(testHost, testApp, nil, nil); !errors.Is(e, errSnapshotUncertain) {
				t.Fatal("uncertain evidence overwritten", e)
			}
			if !reflect.DeepEqual(before, testBytes(t, filepath.Join(r.root, "registry.json"))) {
				t.Fatal("uncertain on-disk decision changed")
			}
		})
	}
}
