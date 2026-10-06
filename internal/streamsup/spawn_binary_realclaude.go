//go:build e2e_realclaude

package streamsup

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// spawnClaudeBin snapshots a driver-owned selection for this spawn only. The
// driver atomically replaces the file; no watcher, restart hint or Runner lock
// is involved. Activated input fails closed rather than releasing a crash loop
// accidentally. This bridge exists only in explicitly tagged test daemons.
func (r *Runner) spawnClaudeBin() (string, error) {
	file := os.Getenv("PYRY_E2E_CLAUDE_BIN_FILE")
	if file == "" {
		return r.cfg.ClaudeBin, nil
	}
	// Nonblocking open plus the regular-file check rejects a mistaken FIFO without
	// hanging the supervise loop. The trusted isolated driver owns this path.
	f, err := os.OpenFile(file, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return "", fmt.Errorf("open test executable selection: %w", err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", fmt.Errorf("stat test executable selection: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("test executable selection must be a regular file")
	}
	const maxSelectionBytes = 4096
	data, err := io.ReadAll(io.LimitReader(f, maxSelectionBytes+1))
	if err != nil {
		return "", fmt.Errorf("read test executable selection: %w", err)
	}
	if len(data) > maxSelectionBytes {
		return "", fmt.Errorf("test executable selection exceeds size limit")
	}
	bin := strings.TrimSpace(string(data))
	if !filepath.IsAbs(bin) || strings.ContainsAny(bin, "\x00\n\r") {
		return "", fmt.Errorf("test executable selection must contain one absolute path")
	}
	return bin, nil
}
