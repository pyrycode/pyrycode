package claudetape

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestRunnerPrivateReadReplacement(t *testing.T) {
	for _, kind := range []string{"fifo", "symlink", "symlink-original", "regular", "directory", "public", "special-mode", "oversize"} {
		t.Run(kind, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestRunnerPrivateReadHelperProcess$")
			cmd.Env = append(os.Environ(), "PYRY_PRIVATE_READ_CASE="+kind, "PYRY_PRIVATE_READ_ROOT="+t.TempDir())
			output, err := cmd.CombinedOutput()
			if ctx.Err() != nil {
				t.Fatalf("replacement blocked: %v\n%s", ctx.Err(), output)
			}
			if err != nil {
				t.Fatalf("replacement control: %v\n%s", err, output)
			}
		})
	}
}

func TestRunnerPrivateReadHelperProcess(t *testing.T) {
	kind := os.Getenv("PYRY_PRIVATE_READ_CASE")
	if kind == "" {
		return
	}
	path := filepath.Join(os.Getenv("PYRY_PRIVATE_READ_ROOT"), "artifact")
	if err := os.WriteFile(path, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	var opened, witness *os.File
	var openErr error
	raw, err := runnerPrivateReadWithOpen(path, 8, func(name string, flags int, mode os.FileMode) (*os.File, error) {
		if kind == "public" || kind == "special-mode" {
			changedMode := os.FileMode(0644)
			if kind == "special-mode" {
				changedMode = 0600 | os.ModeSetuid
			}
			if err := os.Chmod(name, changedMode); err != nil {
				t.Fatal(err)
			}
		} else if kind == "oversize" {
			if err := os.WriteFile(name, []byte("too large"), 0600); err != nil {
				t.Fatal(err)
			}
		} else {
			if err := os.Rename(name, name+".original"); err != nil {
				t.Fatal(err)
			}
			var err error
			switch kind {
			case "fifo":
				err = syscall.Mkfifo(name, 0600)
			case "directory":
				err = os.Mkdir(name, 0600)
			case "regular":
				err = os.WriteFile(name, []byte("foreign"), 0600)
			case "symlink-original":
				err = os.Symlink(name+".original", name)
			case "symlink":
				if err = os.WriteFile(name+".target", []byte("private"), 0600); err == nil {
					err = os.Symlink(name+".target", name)
				}
			default:
				t.Fatalf("unknown replacement %q", kind)
			}
			if err != nil {
				t.Fatal(err)
			}
		}
		opened, openErr = os.OpenFile(name, flags, mode)
		if openErr == nil {
			fd, err := syscall.Dup(int(opened.Fd()))
			if err != nil {
				t.Fatal(err)
			}
			witness = os.NewFile(uintptr(fd), "read-offset-witness")
			t.Cleanup(func() {
				if err := witness.Close(); err != nil {
					t.Error(err)
				}
			})
		}
		return opened, openErr
	})
	if err == nil || raw != nil {
		t.Errorf("replacement returned bytes or success: %q, %v", raw, err)
	}
	if strings.HasPrefix(kind, "symlink") {
		if !errors.Is(openErr, syscall.ELOOP) || opened != nil {
			t.Errorf("symlink was followed: opened=%v err=%v", opened, openErr)
		}
	} else if openErr != nil {
		t.Fatalf("replacement did not reach descriptor validation: %v", openErr)
	}
	if opened != nil {
		if _, err := opened.Stat(); !errors.Is(err, os.ErrClosed) {
			t.Errorf("rejected descriptor still open: %v", err)
		}
		if kind != "fifo" && kind != "directory" {
			if offset, err := witness.Seek(0, io.SeekCurrent); err != nil || offset != 0 {
				t.Errorf("rejected descriptor read bytes: offset=%d err=%v", offset, err)
			}
		}
	}
}

func TestRunnerPrivateReadBoundaries(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		data  string
		mode  os.FileMode
		limit int
		valid bool
	}{
		{"empty", "", 0600, 0, true},
		{"private", "private", 0600, 8, true},
		{"exact-limit", "private", 0600, 7, true},
		{"oversize", "private", 0600, 6, false},
		{"public", "private", 0644, 8, false},
		{"special-mode", "private", 0600 | os.ModeSetuid, 8, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "artifact")
			if err := os.WriteFile(path, []byte(tc.data), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(path, tc.mode); err != nil {
				t.Fatal(err)
			}
			for range 2 {
				raw, err := runnerPrivateRead(path, tc.limit)
				if tc.valid {
					if err != nil || string(raw) != tc.data {
						t.Fatalf("private read: %q, %v", raw, err)
					}
				} else if err == nil || raw != nil {
					t.Fatalf("invalid artifact accepted: %q, %v", raw, err)
				}
			}
		})
	}
	if raw, err := runnerPrivateRead(filepath.Join(t.TempDir(), "missing"), 8); err == nil || raw != nil {
		t.Fatal("missing artifact accepted")
	}
}

func TestRunnerPrivateReadClosedControl(t *testing.T) {
	t.Parallel()
	for _, data := range []string{`{"pid":42}`, `{"pid":42,"pid":43}`, `{"pid":42,"unknown":true}`, `{"pid":42} {}`, `{"pid":`} {
		t.Run(data, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "control")
			if err := os.WriteFile(path, []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			var target struct {
				PID int `json:"pid"`
			}
			err := ReadLifecycleControl(path, 128, &target)
			if data == `{"pid":42}` {
				if err != nil || target.PID != 42 {
					t.Fatal("valid control rejected", err)
				}
			} else if err == nil {
				t.Fatal("non-closed JSON accepted")
			}
		})
	}
}
