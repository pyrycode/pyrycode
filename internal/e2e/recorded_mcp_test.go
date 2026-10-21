//go:build e2e

package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
)

var recordedMCPDaemon struct {
	sync.Once
	path string
	err  error
}

// The cached private build remains available until all tests finish. Ordinary
// suites keep their ordinary daemon; only the MCP recording profile enables this tag.
func buildRecordedMCPDaemon(t *testing.T) string {
	t.Helper()
	recordedMCPDaemon.Do(func() {
		dir, err := os.MkdirTemp("", "pyry-recorded-mcp-")
		if err != nil {
			recordedMCPDaemon.err = err
			return
		}
		recordedMCPDaemon.path = filepath.Join(dir, "pyry")
		cmd := exec.Command("go", "build", "-tags=e2e_mcp_recording", "-o", recordedMCPDaemon.path, "github.com/pyrycode/pyrycode/cmd/pyry")
		cmd.Env = recordedPoolOfflineEnv()
		recordedMCPDaemon.err = cmd.Run()
	})
	if recordedMCPDaemon.err != nil {
		t.Fatal("build current MCP recording daemon", recordedMCPDaemon.err)
	}
	return recordedMCPDaemon.path
}
