package apps

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
)

func TestCandidateDamagedRetry(t *testing.T) {
	for _, kind := range []string{"missing", "partial", "changed"} {
		t.Run(kind, func(t *testing.T) {
			r, tools, _, _ := testCandidate(t)
			release, e := r.PrepareCandidate(context.Background(), testHost, testApp, tools)
			if e != nil {
				t.Fatal(e)
			}
			testWritableBuild(t, release.BuildRoot)
			switch kind {
			case "missing":
				if e = os.RemoveAll(release.BuildRoot); e != nil {
					t.Fatal(e)
				}
			case "partial":
				if e = os.Remove(filepath.Join(release.BuildRoot, "dist/service/main.js")); e != nil {
					t.Fatal(e)
				}
			case "changed":
				testArtifactWrite(t, release.BuildRoot, "dist/service/main.js", "different")
			}
			if kind != "missing" {
				if e = filepath.Walk(release.BuildRoot, func(p string, i os.FileInfo, e error) error {
					if e != nil {
						return e
					}
					mode := os.FileMode(0400)
					if i.IsDir() {
						mode = 0500
					}
					return os.Chmod(p, mode)
				}); e != nil {
					t.Fatal(e)
				}
			}
			before := testArtifactSnapshot(t, release.BuildRoot)
			registry := testBytes(t, filepath.Join(r.root, "registry.json"))
			_, e = r.PrepareCandidate(context.Background(), testHost, testApp, tools)
			testCandidateFailure(t, e, "record")
			if !reflect.DeepEqual(before, testArtifactSnapshot(t, release.BuildRoot)) || !reflect.DeepEqual(registry, testBytes(t, filepath.Join(r.root, "registry.json"))) {
				t.Fatal("retry repaired build or changed history")
			}
		})
	}
}
func TestCandidatePersistence(t *testing.T) {
	r, tools, _, _ := testCandidate(t)
	before := testBytes(t, filepath.Join(r.root, "registry.json"))
	rows, revision := r.List()
	persist := r.persist
	r.persist = func(string, snapshot) error { return errors.New("SECRET /private/path") }
	_, e := r.PrepareCandidate(context.Background(), testHost, testApp, tools)
	testCandidateFailure(t, e, "record")
	got, rev := r.List()
	if !reflect.DeepEqual(rows, got) || rev != revision || !reflect.DeepEqual(before, testBytes(t, filepath.Join(r.root, "registry.json"))) {
		t.Fatal("failed persistence changed registry")
	}
	r.persist = persist
	if _, e = r.PrepareCandidate(context.Background(), testHost, testApp, tools); e != nil {
		t.Fatal(e)
	}
	reopened, e := Open(r.root, testHost)
	if e != nil {
		t.Fatal(e)
	}
	got, _ = reopened.List()
	if len(got[0].ReleaseHistory) != 1 {
		t.Fatal(got)
	}
}
func TestCandidateConcurrent(t *testing.T) {
	for _, different := range []bool{false, true} {
		t.Run(map[bool]string{false: "identical", true: "different"}[different], func(t *testing.T) {
			r, tools, c, _ := testCandidate(t)
			first, e := r.PrepareCandidate(context.Background(), testHost, testApp, tools)
			if e != nil {
				t.Fatal(e)
			}
			// Two completed private candidates race at the atomic recording boundary.
			stage := c.Fixture
			testArtifactWrite(t, stage, "app.json", string(testBytes(t, filepath.Join(first.BuildRoot, "app.json"))))
			testArtifactWrite(t, stage, "package.json", string(testBytes(t, filepath.Join(first.BuildRoot, "package.json"))))
			testArtifactWrite(t, stage, "package-lock.json", string(testBytes(t, filepath.Join(first.BuildRoot, "package-lock.json"))))
			if different {
				testArtifactWrite(t, stage, "dist/service/main.js", "other bytes")
			}
			if !different {
				stage = first.BuildRoot
			}
			build, e := ValidateArtifacts(context.Background(), stage, testHost, testApp)
			if e != nil {
				t.Fatal(e)
			}
			before := testBytes(t, filepath.Join(r.root, "registry.json"))
			var wg sync.WaitGroup
			for range 8 {
				wg.Go(func() {
					got, e := r.recordCandidate(context.Background(), testHost, testApp, stage, build, "24.16.0", "12.0.0")
					if different {
						if e == nil {
							t.Error("accepted replacement")
						}
					} else if e != nil || got != first {
						t.Error(got, e)
					}
				})
			}
			wg.Wait()
			if !reflect.DeepEqual(before, testBytes(t, filepath.Join(r.root, "registry.json"))) {
				t.Fatal("concurrent retry changed registry")
			}
		})
	}
	// Concurrent first preparations must record exactly one metadata entry.
	r, tools, _, _ := testCandidate(t)
	var wg sync.WaitGroup
	results := make(chan Release, 4)
	for range 4 {
		wg.Go(func() {
			release, e := r.PrepareCandidate(context.Background(), testHost, testApp, tools)
			if e != nil {
				t.Error(e)
			}
			results <- release
		})
	}
	wg.Wait()
	close(results)
	first := <-results
	for release := range results {
		if release != first {
			t.Fatal("metadata differs")
		}
	}
	if len(ownerRecord(t, r, testApp).ReleaseHistory) != 1 {
		t.Fatal("duplicate history")
	}
}
func TestCandidateLegacy(t *testing.T) {
	r, tools, _, _ := testCandidate(t)
	record := ownerRecord(t, r, testApp)
	_, e := r.UpdateLifecycle(testHost, testApp, Lifecycle{Title: record.Title, Desired: "stopped", State: "stopped", ActiveRelease: "1.9.0"})
	if e != nil {
		t.Fatal(e)
	}
	testCandidateVersion(t, r, "1.9.0")
	_, e = r.PrepareCandidate(context.Background(), testHost, testApp, tools)
	testCandidateFailure(t, e, "record")
	testCandidateVersion(t, r, "1.10.0")
	if _, e = r.PrepareCandidate(context.Background(), testHost, testApp, tools); e != nil {
		t.Fatal(e)
	}
	if ownerRecord(t, r, testApp).ActiveRelease != "1.9.0" {
		t.Fatal("activated candidate")
	}
}
func TestCandidateCurrentRegistration(t *testing.T) {
	for _, remove := range []bool{false, true} {
		t.Run(map[bool]string{false: "changed", true: "removed"}[remove], func(t *testing.T) {
			r, tools, c, config := testCandidate(t)
			c.Gate = "run build"
			testCandidateConfig(t, config, c)
			done := make(chan error, 1)
			go func() { _, e := r.PrepareCandidate(context.Background(), testHost, testApp, tools); done <- e }()
			waitFile(t, c.Ready)
			if remove {
				if _, e := r.Remove(testApp); e != nil {
					t.Fatal(e)
				}
			} else {
				m := testMap(t)
				m["title"] = "Current title"
				if _, e := r.UpdateManifest(testApp, testJSON(t, m)); e != nil {
					t.Fatal(e)
				}
			}
			if e := os.WriteFile(c.Ready+".continue", nil, 0600); e != nil {
				t.Fatal(e)
			}
			e := <-done
			if remove {
				testCandidateFailure(t, e, "record")
			} else if e != nil || ownerRecord(t, r, testApp).Manifest.Title() != "Current title" {
				t.Fatal(e)
			}
		})
	}
}

func TestCandidatePreservesCommittedFields(t *testing.T) {
	r, tools, _, _ := testCandidate(t)
	fields := testLifecycle()
	fields.PendingRelease = "2.0.0"
	if _, e := r.UpdatePublish(testHost, testApp, testJournal(r, testApp, true), &fields); e != nil {
		t.Fatal(e)
	}
	before := ownerRecord(t, r, testApp)
	testCandidateVersion(t, r, "1.10.0")
	release, e := r.PrepareCandidate(context.Background(), testHost, testApp, tools)
	if e != nil {
		t.Fatal(e)
	}
	after := ownerRecord(t, r, testApp)
	before.Revision = after.Revision
	before.ReleaseHistory = []Release{release}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("changed committed fields", before, after)
	}
}
func TestCandidateConcurrentDifferentBytes(t *testing.T) {
	r, tools, _, _ := testCandidate(t)
	_, other, c, _ := testCandidate(t)
	testArtifactWrite(t, c.Fixture, "dist/service/main.js", "second candidate")
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for _, tool := range []Toolchain{tools, other} {
		wg.Go(func() {
			<-start
			_, e := r.PrepareCandidate(context.Background(), testHost, testApp, tool)
			results <- e
		})
	}
	close(start)
	wg.Wait()
	close(results)
	success := 0
	for e := range results {
		if e == nil {
			success++
		} else {
			testCandidateFailure(t, e, "record")
		}
	}
	if success != 1 || len(ownerRecord(t, r, testApp).ReleaseHistory) != 1 {
		t.Fatal("conflicting preparations did not choose one winner", success)
	}
}
func TestCandidateConcurrentVersions(t *testing.T) {
	r, _, _, _ := testCandidate(t)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for _, version := range []string{"1.9.0", "1.10.0"} {
		stage := testArtifactTree(t)
		m := testMap(t)
		m["release_version"] = version
		testArtifactWrite(t, stage, "app.json", string(testJSON(t, m)))
		build, e := ValidateArtifacts(context.Background(), stage, testHost, testApp)
		if e != nil {
			t.Fatal(e)
		}
		wg.Go(func() {
			<-start
			_, e := r.recordCandidate(context.Background(), testHost, testApp, stage, build, "24.15.0", "11.12.0")
			if version == "1.10.0" && e != nil {
				t.Error(e)
			}
			if e != nil && !errors.Is(e, ErrConflict) {
				t.Error(e)
			}
		})
	}
	close(start)
	wg.Wait()
	history := ownerRecord(t, r, testApp).ReleaseHistory
	if len(history) == 0 || history[len(history)-1].ReleaseVersion != "1.10.0" {
		t.Fatal(history)
	}
	for i := 1; i < len(history); i++ {
		if !newerCandidate(history[i].ReleaseVersion, history[i-1].ReleaseVersion) {
			t.Fatal("unordered version commits", history)
		}
	}
}
