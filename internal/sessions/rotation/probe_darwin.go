//go:build darwin

package rotation

import (
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// darwinProbe shells out to `lsof -nP -p <pid> -F fn` and parses the file
// records.
type darwinProbe struct{}

// noopProbe is the fallback when lsof is missing. Returns ("", nil) so the
// watcher silently skips rotation detection rather than failing pyry startup.
type noopProbe struct{}

func (noopProbe) OpenJSONL(int) (string, error) { return "", nil }

// DefaultProbe returns the Darwin probe, or a noopProbe if lsof is not on
// PATH. Logging at construction time means a missing-lsof shows up at
// startup, not on the first event.
func DefaultProbe(log *slog.Logger) Probe {
	if _, err := exec.LookPath("lsof"); err != nil {
		if log != nil {
			log.Warn("lsof not found; rotation probe disabled", "err", err)
		}
		return noopProbe{}
	}
	return darwinProbe{}
}

// lsofProbeTimeout bounds the single lsof invocation so a hung lsof cannot wedge
// the v2 dispatch goroutine, a producer resolver, or the rotation watcher.
// Mirrors ptyrunner.reapPSTimeout. A var (not const) so probe tests can shrink
// the bound for a fast, deterministic timeout assertion.
var lsofProbeTimeout = 2 * time.Second

// newLsofCmd builds the lsof command under ctx so a context timeout kills it.
// A package-var seam (mirroring ptyrunner.reapDescendantGroupsFn) so tests can
// point OpenJSONL at a fake slow or exit-1 binary without a real lsof or PATH
// manipulation. Production leaves it pointed here; tests swap it non-parallel
// and restore via t.Cleanup.
var newLsofCmd = func(ctx context.Context, pid int) *exec.Cmd {
	return exec.CommandContext(ctx, "lsof", "-nP", "-p", strconv.Itoa(pid), "-F", "fn")
}

func (darwinProbe) OpenJSONL(pid int) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), lsofProbeTimeout)
	defer cancel()

	out, err := newLsofCmd(ctx, pid).Output()
	if err != nil {
		// A context timeout SIGKILLs lsof (ExitCode() == -1, so it would not be
		// misclassified as exit-1 below); checking ctx.Err() first yields a
		// clear, greppable timeout error and a deterministic guard against any
		// future platform quirk in the killed-process exit code.
		if ctx.Err() != nil {
			return "", fmt.Errorf("lsof probe timed out after %s: %w", lsofProbeTimeout, ctx.Err())
		}
		// Exit code 1 from lsof means "no matching files" or "process gone";
		// neither is a probe failure.
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return "", nil
		}
		return "", fmt.Errorf("lsof: %w", err)
	}
	for _, f := range parseLsofOutput(string(out)) {
		if strings.HasSuffix(f.Name, ".jsonl") {
			return f.Name, nil
		}
	}
	return "", nil
}
