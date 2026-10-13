package apps

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
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
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	validated, err := ValidateArtifacts(ctx, stage, testHost, testApp)
	if err != nil || ctx.Err() != nil {
		t.Fatalf("contained repeated expansion: error=%v context=%v", err, ctx.Err())
	}
	ctx, cancel = context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	installed, err := InstallBuild(ctx, root, stage, testHost, testApp)
	if err != nil || ctx.Err() != nil || installed.BuildID != validated.BuildID {
		t.Fatalf("install repeated expansion: error=%v context=%v", err, ctx.Err())
	}
	t.Cleanup(func() { testWritableBuild(t, filepath.Join(root, testApp, "builds", installed.BuildID)) })
	ctx, cancel = context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	verified, err := VerifyBuild(ctx, root, testHost, testApp, installed.BuildID)
	if err != nil || ctx.Err() != nil || verified.BuildID != installed.BuildID {
		t.Fatalf("verify repeated expansion: error=%v context=%v", err, ctx.Err())
	}
}
