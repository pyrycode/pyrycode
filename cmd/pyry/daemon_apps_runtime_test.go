package main

import (
	"context"
	"os"
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
			if err := os.WriteFile(node, []byte("#!/bin/sh\nprintf '%s\\n' '"+tc.version+"'\n"), 0700); err != nil {
				t.Fatal(err)
			}
			o := newDaemonAppsOwner(r, node, discardLogger())
			if o == nil {
				t.Fatal("owner missing")
			}
			defer o.Shutdown()
			if got := o.CheckRuntime(context.Background()) == nil; got != tc.supported {
				t.Fatalf("supported = %v", got)
			}
			// The executable selected at construction is independent of PATH changes.
			if err := os.WriteFile(node, []byte("#!/bin/sh\nexec /bin/sleep 20\n"), 0700); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
			defer cancel()
			start := time.Now()
			if err := o.CheckRuntime(ctx); err == nil || time.Since(start) > time.Second {
				t.Fatalf("unbounded runtime probe: %v", err)
			}
		})
	}
}
