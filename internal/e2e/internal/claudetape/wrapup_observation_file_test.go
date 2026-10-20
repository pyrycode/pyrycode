package claudetape

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestWrapUpObservationFileProcess(t *testing.T) {
	if os.Getenv("PYRY_WRAPUP_OBSERVATION_CONTROL") != "1" {
		return
	}
	path := os.Args[len(os.Args)-1]
	expected, err := os.Lstat(path + ".original")
	if err != nil {
		os.Exit(2)
	}
	f, err := openWrapUpObservation(path, expected, 16384)
	if f != nil {
		_ = f.Close()
	}
	if err == nil {
		os.Exit(2)
	}
	os.Exit(0)
}

func TestWrapUpObservationFileReplacement(t *testing.T) {
	for _, kind := range []string{"regular", "symlink", "fifo", "directory"} {
		t.Run(kind, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "observation.json")
			if err := os.WriteFile(path, []byte(`{}`), 0600); err != nil {
				t.Fatal(err)
			}
			// Preserve the inspected inode while replacing its directory entry.
			if err := os.Rename(path, path+".original"); err != nil {
				t.Fatal(err)
			}
			var err error
			switch kind {
			case "regular":
				err = os.WriteFile(path, []byte(`{}`), 0600)
			case "symlink":
				err = os.Symlink(path+".original", path)
			case "fifo":
				err = syscall.Mkfifo(path, 0600)
			case "directory":
				err = os.Mkdir(path, 0700)
			}
			if err != nil {
				t.Fatal(err)
			}
			exe, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, exe, "-test.run=^TestWrapUpObservationFileProcess$", "--", path)
			cmd.Env = append(os.Environ(), "PYRY_WRAPUP_OBSERVATION_CONTROL=1")
			cmd.WaitDelay = time.Second
			if err := cmd.Run(); err != nil {
				t.Fatalf("replacement was accepted or blocked: %v (deadline=%t)", err, ctx.Err() != nil)
			}
		})
	}
}
