package main

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/apps"
	"github.com/pyrycode/pyrycode/internal/identity"
)

func TestDaemonAppsRuntimeReadiness(t *testing.T) {
	for _, tc := range []struct {
		version   string
		supported bool
	}{
		{"v24.15.0", true}, {"v24.16.1", true}, {"v24.14.9", false},
		{"v25.0.0", false}, {"v24.15.0-rc.1", false}, {"garbage", false},
		{"v24.15.0" + strings.Repeat(" ", 64), false},
	} {
		t.Run(tc.version, func(t *testing.T) {
			t.Parallel()
			r, err := apps.Open(filepath.Join(testRealTempDir(t), "apps"), string(identity.NewServerID()))
			if err != nil {
				t.Fatal(err)
			}
			node := filepath.Join(t.TempDir(), "node")
			testWriteDaemonAppsRuntime(t, node, "#!/bin/sh\nprintf '%s\\n' '"+tc.version+"'\n")
			o := newDaemonAppsOwner(r, node, discardLogger())
			if o == nil {
				t.Fatal("owner missing")
			}
			defer o.Shutdown()
			probeErr := o.CheckRuntime(context.Background())
			if got := probeErr == nil; got != tc.supported {
				t.Fatalf("supported = %v, want %v; probe error: %v", got, tc.supported, probeErr)
			}
			// Replacing the selected executable exercises the same absolute path.
			testWriteDaemonAppsRuntime(t, node, "#!/bin/sh\nexec /bin/sleep 20\n")
			ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
			defer cancel()
			start := time.Now()
			if err := o.CheckRuntime(ctx); err == nil || !errors.Is(ctx.Err(), context.DeadlineExceeded) || time.Since(start) > time.Second {
				t.Fatalf("runtime probe: error = %v, context error = %v, elapsed = %v", err, ctx.Err(), time.Since(start))
			}
		})
	}
}

// A joined child writes and closes the executable so concurrent test-process
// forks cannot inherit a writable descriptor and make exec fail with ETXTBSY.
func testWriteDaemonAppsRuntime(t *testing.T, path, script string) {
	t.Helper()
	cmd := exec.Command("/bin/sh", "-c", `printf '%s' "$1" > "$2" && chmod 700 "$2"`, "write-node", script, path)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("write runtime fixture: %v: %s", err, out)
	}
}
