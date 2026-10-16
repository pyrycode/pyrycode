package apps

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type testResolutionContext struct {
	context.Context
	cancel context.CancelFunc
	checks int
}

func (c *testResolutionContext) Err() error {
	c.checks--
	if c.checks == 0 {
		c.cancel()
	}
	return c.Context.Err()
}

func TestRetainedResolutionCancellation(t *testing.T) {
	t.Parallel()
	s := artifactScan{nodes: map[string]artifactEntry{
		"node_modules":     {kind: "directory"},
		"node_modules/pkg": {kind: "directory"},
	}}
	for i := 0; i < 100; i++ {
		target := fmt.Sprintf("link%d", i+1)
		if i == 99 {
			target = "pkg"
		}
		s.nodes[fmt.Sprintf("node_modules/link%d", i)] = artifactEntry{kind: "symlink", target: target}
	}
	base, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx := &testResolutionContext{Context: base, cancel: cancel, checks: 5}
	if err := s.resolve(ctx, "node_modules/link0"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation during resolution: %v", err)
	}
	// A canceled traversal must not poison a later resolution in the same scan.
	if err := s.resolve(context.Background(), "node_modules/link0"); err != nil {
		t.Fatal(err)
	}
	if err := s.resolve(base, "node_modules/link0"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled cached resolution: %v", err)
	}
}

func TestRetainedRepeatedExpansion(t *testing.T) {
	t.Parallel()
	stage, root := testArtifactTree(t), testArtifactTemp(t)
	testArtifactLink(t, stage, "node_modules/amplify00", "pkg")
	for i := 1; i <= 20; i++ {
		previous := fmt.Sprintf("amplify%02d", i-1)
		testArtifactLink(t, stage, fmt.Sprintf("node_modules/amplify%02d", i), previous+"/../"+previous)
	}
	testArtifactLink(t, stage, "node_modules/use", "amplify20/../amplify20/index.js")
	// The integration bound covers filesystem work and scheduling. Resolution
	// complexity has its own checkpoint budget after the fixture is read.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	dir, err := privateDir(stage)
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Close()
	scan := artifactScan{nodes: map[string]artifactEntry{}}
	started := time.Now()
	err = scan.walk(ctx, dir, nil, "")
	t.Logf("fixture filesystem scan: %s", time.Since(started))
	if err != nil {
		t.Fatal(err)
	}
	base, stop := context.WithCancel(ctx)
	defer stop()
	const workLimit = 512
	work := &testResolutionContext{Context: base, cancel: stop, checks: workLimit}
	started = time.Now()
	// Resolve the deepest consumer with a cold cache before visiting other links.
	err = scan.resolve(work, "node_modules/use")
	for _, entry := range scan.entries {
		if err == nil && entry.kind == "symlink" {
			err = scan.resolve(work, entry.path)
		}
	}
	t.Logf("in-memory resolution: %s, checkpoints=%d/%d", time.Since(started), workLimit-work.checks, workLimit)
	if err != nil || base.Err() != nil {
		t.Fatalf("bounded repeated expansion: error=%v context=%v", err, base.Err())
	}
	if got := strings.Join(scan.resolved["node_modules/use"], "/"); got != "node_modules/pkg/index.js" {
		t.Fatalf("resolved consumer: %q", got)
	}
	started = time.Now()
	validated, err := ValidateArtifacts(ctx, stage, testHost, testApp)
	t.Logf("validation: %s", time.Since(started))
	if err != nil || ctx.Err() != nil {
		t.Fatalf("contained repeated expansion: error=%v context=%v", err, ctx.Err())
	}
	started = time.Now()
	installed, err := InstallBuild(ctx, root, stage, testHost, testApp)
	t.Logf("installation: %s", time.Since(started))
	if err != nil || ctx.Err() != nil || installed.BuildID != validated.BuildID {
		t.Fatalf("install repeated expansion: error=%v context=%v", err, ctx.Err())
	}
	t.Cleanup(func() { testWritableBuild(t, filepath.Join(root, testApp, "builds", installed.BuildID)) })
	started = time.Now()
	verified, err := VerifyBuild(ctx, root, testHost, testApp, installed.BuildID)
	t.Logf("verification: %s", time.Since(started))
	if err != nil || ctx.Err() != nil || verified.BuildID != installed.BuildID {
		t.Fatalf("verify repeated expansion: error=%v context=%v", err, ctx.Err())
	}
}
