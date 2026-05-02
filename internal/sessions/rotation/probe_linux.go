//go:build linux

package rotation

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// linuxProbe walks /proc/<pid>/fd/* and returns the first fd target that
// looks like a regular file ending in .jsonl.
type linuxProbe struct{}

// DefaultProbe returns the Linux probe.
func DefaultProbe(_ *slog.Logger) Probe { return linuxProbe{} }

func (linuxProbe) OpenJSONL(pid int) (string, error) {
	fdDir := fmt.Sprintf("/proc/%d/fd", pid)
	entries, err := os.ReadDir(fdDir)
	if err != nil {
		// PID gone or unreadable — caller skips this PID.
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, fs.ErrPermission) {
			return "", nil
		}
		return "", err
	}
	for _, e := range entries {
		target, err := os.Readlink(filepath.Join(fdDir, e.Name()))
		if err != nil {
			continue
		}
		path := parseProcFD(target)
		if path == "" {
			continue
		}
		if strings.HasSuffix(path, ".jsonl") {
			return path, nil
		}
	}
	return "", nil
}
