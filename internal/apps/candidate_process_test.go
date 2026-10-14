package apps

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func TestCandidateDescendants(t *testing.T) {
	for _, phase := range []string{"ci", "run typecheck", "test", "run build"} {
		t.Run(phase, func(t *testing.T) {
			r, tools, c, config := testCandidate(t)
			c.Descendant = phase
			testCandidateConfig(t, config, c)
			_, e := r.PrepareCandidate(context.Background(), testHost, testApp, tools)
			// The build descendant produces required artifacts only on cleanup TERM;
			// success proves artifact validation began after descendant cleanup.
			if e != nil {
				t.Fatal(e)
			}
			pid, e := strconv.Atoi(string(waitFile(t, c.Ready+".child")))
			if e != nil {
				t.Fatal(e)
			}
			if !childGone(pid) {
				t.Fatal("returned with descendant running", pid)
			}
			if _, e = os.Stat(c.Ready + ".escaped"); !os.IsNotExist(e) {
				t.Fatal("descendant executed late work")
			}
		})
	}
}
func TestCandidateCancellationDescendants(t *testing.T) {
	r, tools, c, config := testCandidate(t)
	c.Descendant = "test"
	c.Hold = true
	testCandidateConfig(t, config, c)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, e := r.PrepareCandidate(ctx, testHost, testApp, tools); done <- e }()
	pid, e := strconv.Atoi(string(waitFile(t, c.Ready+".child")))
	if e != nil {
		t.Fatal(e)
	}
	cancel()
	testCandidateFailure(t, <-done, "test")
	if !childGone(pid) {
		t.Fatal("cancelled with descendant running")
	}
	if strings.Contains(string(testBytes(t, c.Log)), "run build") {
		t.Fatal("ran later phase")
	}
}

func TestCandidateInstallSelection(t *testing.T) {
	r, tools, _, _ := testCandidate(t)
	release, e := r.PrepareCandidate(context.Background(), testHost, testApp, tools)
	if e != nil {
		t.Fatal(e)
	}
	record := ownerRecord(t, r, testApp)
	record.ActiveRelease = release.ReleaseVersion
	parent := filepath.Join(r.root, testApp, "builds")
	// An installer has allocated its private entry but has not finished scanning.
	if e = os.Mkdir(filepath.Join(parent, ".install-private"), 0700); e != nil {
		t.Fatal(e)
	}
	dir, name, e := selectBuild(context.Background(), r.root, record)
	if e != nil {
		t.Fatal(e)
	}
	dir.Close()
	if name != release.BuildID {
		t.Fatal(name)
	}
	partial := filepath.Join(parent, strings.Repeat("a", 64))
	if e = os.Mkdir(partial, 0700); e != nil {
		t.Fatal(e)
	}
	if dir, _, e = selectBuild(context.Background(), r.root, record); e == nil {
		dir.Close()
		t.Fatal("accepted partial digest build")
	}
}
func TestCandidateExistingService(t *testing.T) {
	// Reuse the controlled HTTP service so failed preparation is checked against
	// actual loopback availability, the process generation, and durable app data.
	r, o := healthFixture(t, healthReply{Body: healthyBody()})
	if e := o.Start(context.Background(), testApp); e != nil {
		t.Fatal(e)
	}
	target := healthTarget(t, o)
	before := ownerRecord(t, r, testApp)
	data := filepath.Join(r.root, testApp, "data")
	testArtifactWrite(t, data, "saved", "durable preference")
	// Build-tool fixture belongs to a different registry; only its selected tools
	// and fixture configuration are reused for this registration's source.
	_, tools, c, config := testCandidate(t)
	c.Fail = "test"
	testCandidateConfig(t, config, c)
	files, e := templateFiles(before.Manifest)
	if e != nil {
		t.Fatal(e)
	}
	source := filepath.Join(r.root, testApp, "source")
	if e = os.Mkdir(source, 0700); e != nil {
		t.Fatal(e)
	}
	dir, e := privateDir(source)
	if e != nil {
		t.Fatal(e)
	}
	for _, file := range files {
		if e = writeSourceFile(dir, file); e != nil {
			t.Fatal(e)
		}
	}
	dir.Close()
	testCandidateVersion(t, r, "1.10.0")
	_, e = r.PrepareCandidate(context.Background(), testHost, testApp, tools)
	testCandidateFailure(t, e, "test")
	after := ownerRecord(t, r, testApp)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("committed state changed", before, after)
	}
	current, e := o.Lookup(testHost, testApp)
	if e != nil || current.generation != target.generation {
		t.Fatal("active process changed", e)
	}
	req, e := http.NewRequest("GET", "http://caller.invalid/resource", nil)
	if e != nil {
		t.Fatal(e)
	}
	resp, e := target.Do(req)
	if e != nil {
		t.Fatal(e)
	}
	b, e := io.ReadAll(resp.Body)
	resp.Body.Close()
	if e != nil || string(b) != "resource" {
		t.Fatal("service unavailable", string(b), e)
	}
	if string(testBytes(t, filepath.Join(data, "saved"))) != "durable preference" {
		t.Fatal("data changed")
	}
}
