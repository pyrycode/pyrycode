//go:build darwin

package canonicalpath

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestResolveAncestorFanout(t *testing.T) {
	root := testRealpath(t, t.TempDir())
	leaf := filepath.Join(root, "Workspace", "Child")
	if err := os.MkdirAll(leaf, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, wide := range []bool{false, true} {
		if wide {
			for i := range 35000 {
				if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("sibling-%05d", i)), nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}
		}
		calls, entries := 0, 0
		readDir := func(path string) ([]os.DirEntry, error) {
			result, err := os.ReadDir(path)
			if path == root {
				calls++
				entries += len(result)
			}
			return result, err
		}
		start := time.Now()
		for range 16 {
			got, err := resolve(leaf, readDir)
			if err != nil || got != leaf {
				t.Fatalf("resolve = %q, %v; want %q", got, err, leaf)
			}
			testResolve(t, leaf, leaf)
		}
		t.Logf("wide=%t resolutions=16 ancestor_scans=%d ancestor_entries=%d elapsed=%s", wide, calls, entries, time.Since(start))
		if calls > 1 {
			t.Errorf("repeated full ancestor enumeration: %d scans, %d entries", calls, entries)
		}
	}
}

func TestResolveFreshIdentity(t *testing.T) {
	t.Parallel()
	root := testRealpath(t, t.TempDir())
	original := filepath.Join(root, "Workspace")
	child := filepath.Join(original, "Child")
	if err := os.MkdirAll(child, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(child, link); err != nil {
		t.Fatal(err)
	}
	testResolve(t, link, child)
	renamed := filepath.Join(root, "WORKSPACE")
	if err := os.Rename(original, renamed); err != nil {
		t.Fatal(err)
	}
	testResolve(t, filepath.Join(renamed, "Child"), filepath.Join(renamed, "Child"))
	if _, err := os.Stat(original); err == nil {
		testResolve(t, child, filepath.Join(renamed, "Child"))
	}
	if err := os.Rename(renamed, filepath.Join(root, "old")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(child, 0o700); err != nil {
		t.Fatal(err)
	}
	testResolve(t, child, child)
	testResolve(t, link, child)
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "old", "Child"), link); err != nil {
		t.Fatal(err)
	}
	testResolve(t, link, filepath.Join(root, "old", "Child"))
	if err := os.Remove(filepath.Join(root, "old", "Child")); err != nil {
		t.Fatal(err)
	}
	if got, err := Resolve(link); got != "" || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing retargeted leaf = %q, %v", got, err)
	}
}

func TestResolveSpecialLeaves(t *testing.T) {
	t.Parallel()
	root := testRealpath(t, t.TempDir())
	fifo := filepath.Join(root, "FIFO")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(root, "Socket")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	for _, path := range []string{fifo, socket} {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestResolveSpecialHelper$")
		cmd.Env = append(os.Environ(), "PYRY_RESOLVE_SPECIAL="+path)
		out, err := cmd.CombinedOutput()
		cancel()
		if err != nil {
			t.Fatalf("special leaf resolution: %v: %s", err, out)
		}
	}
}

func TestResolveSpecialHelper(t *testing.T) {
	if path := os.Getenv("PYRY_RESOLVE_SPECIAL"); path != "" {
		testResolve(t, path, path)
	}
}

func TestNativePathDescriptors(t *testing.T) {
	if _, err := getDescriptorPath(-1); !errors.Is(err, syscall.EBADF) {
		t.Fatalf("invalid descriptor query: %v", err)
	}
	root := testRealpath(t, t.TempDir())
	count := func() int {
		t.Helper()
		dir, err := os.Open("/dev/fd")
		if err != nil {
			t.Fatal(err)
		}
		names, readErr := dir.Readdirnames(-1)
		closeErr := dir.Close()
		if readErr != nil || closeErr != nil {
			t.Fatalf("descriptor inventory: read=%v close=%v", readErr, closeErr)
		}
		return len(names)
	}
	before := count()
	for range 256 {
		if got, ok := nativePath(root); !ok || got != root {
			t.Fatalf("native path = %q, %t", got, ok)
		}
		if _, ok := nativePath(filepath.Join(root, "missing")); ok {
			t.Fatal("missing path accepted")
		}
	}
	if after := count(); after != before {
		t.Fatalf("descriptor count: before=%d after=%d", before, after)
	}
	for _, phase := range []string{"success", "open", "query", "close", "empty", "relative"} {
		t.Run(phase, func(t *testing.T) {
			closed := 0
			open := func(_ string, flags int, _ uint32) (int, error) {
				if flags != syscall.O_EVTONLY|syscall.O_CLOEXEC|syscall.O_NONBLOCK {
					t.Fatalf("unsafe open flags: %x", flags)
				}
				if phase == "open" {
					return -1, syscall.EACCES
				}
				return 123, nil
			}
			query := func(fd int) (string, error) {
				if fd != 123 {
					t.Fatal("wrong descriptor")
				}
				switch phase {
				case "query":
					return "", syscall.EBADF
				case "empty":
					return "", nil
				case "relative":
					return "relative", nil
				}
				return root, nil
			}
			close := func(fd int) error {
				closed++
				if fd != 123 {
					t.Fatal("closing wrong descriptor")
				}
				if phase == "close" {
					return syscall.EIO
				}
				return nil
			}
			got, ok := descriptorPath(root, open, query, close)
			wantClosed := 1
			if phase == "open" {
				wantClosed = 0
			}
			if closed != wantClosed || ok != (phase == "success") || (ok && got != root) {
				t.Fatalf("path=%q ok=%t closed=%d", got, ok, closed)
			}
		})
	}
}
