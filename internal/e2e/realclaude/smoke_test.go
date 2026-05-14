//go:build e2e_realclaude

package realclaude

import (
	"context"
	"os/exec"
	"testing"
	"time"
)

func TestClaudeBinaryAvailable(t *testing.T) {
	if _, err := exec.LookPath("claude"); err != nil {
		t.Fatalf("claude binary not on PATH: %v\nthis suite requires the real claude CLI; install it or adjust PATH before running `make e2e-realclaude`", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	out, err := exec.CommandContext(ctx, "claude", "--version").CombinedOutput()
	if err != nil {
		t.Fatalf("claude --version failed: %v\noutput:\n%s", err, out)
	}
}
