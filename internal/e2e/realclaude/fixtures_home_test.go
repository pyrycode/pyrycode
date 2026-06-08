//go:build e2e_realclaude

package realclaude

import (
	"strings"
	"testing"
)

// TestBuildEnvWithRealHomeIgnoresRedirectedHOME proves the pyry build env
// uses the real HOME captured at package load, not a test-redirected one.
// This is the fix for the private-module fetch failure: WithWorktree pins
// HOME to a throwaway dir, which cold-starts Go's module cache and hides git
// auth; the build env must escape that redirect so a private dependency
// resolves from the warm cache instead of a credential-less git fetch.
func TestBuildEnvWithRealHomeIgnoresRedirectedHOME(t *testing.T) {
	const redirected = "/tmp/throwaway-home-for-test"
	t.Setenv("HOME", redirected)

	env := buildEnvWithRealHome()

	var homes []string
	for _, kv := range env {
		if strings.HasPrefix(kv, "HOME=") {
			homes = append(homes, strings.TrimPrefix(kv, "HOME="))
		}
	}
	if len(homes) != 1 {
		t.Fatalf("env has %d HOME entries, want exactly 1: %v", len(homes), homes)
	}
	if homes[0] == redirected {
		t.Fatal("build env used the test-redirected HOME; the module cache would be cold")
	}
	if homes[0] != realHome {
		t.Fatalf("build HOME = %q, want realHome %q", homes[0], realHome)
	}
}
