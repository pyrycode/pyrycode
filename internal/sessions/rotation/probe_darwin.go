//go:build darwin

package rotation

import (
	"fmt"
	"log/slog"
	"os/exec"
	"strconv"
	"strings"
)

// darwinProbe shells out to `lsof -nP -p <pid> -F fn` and parses the file
// records.
type darwinProbe struct{}

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

func (darwinProbe) OpenJSONL(pid int) (string, error) {
	cmd := exec.Command("lsof", "-nP", "-p", strconv.Itoa(pid), "-F", "fn")
	out, err := cmd.Output()
	if err != nil {
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
