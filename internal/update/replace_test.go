package update

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestAtomicReplace_CreatesNewFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	target := filepath.Join(dir, "pyry")
	want := []byte("new binary contents")

	if err := AtomicReplace(target, want, 0o755); err != nil {
		t.Fatalf("AtomicReplace: %v", err)
	}

	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("contents = %q, want %q", got, want)
	}
}

func TestAtomicReplace_OverwritesExistingFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	target := filepath.Join(dir, "pyry")
	if err := os.WriteFile(target, []byte("OLD"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	want := []byte("NEW")

	if err := AtomicReplace(target, want, 0o755); err != nil {
		t.Fatalf("AtomicReplace: %v", err)
	}

	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("contents = %q, want %q", got, want)
	}
}

func TestAtomicReplace_PreservesMode(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	target := filepath.Join(dir, "pyry")

	if err := AtomicReplace(target, []byte("x"), 0o755); err != nil {
		t.Fatalf("AtomicReplace: %v", err)
	}

	info, err := os.Stat(target)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o755 {
		t.Errorf("mode = %o, want %o", got, 0o755)
	}
}

func TestAtomicReplace_ParentDirMissing(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	target := filepath.Join(dir, "nope", "pyry")

	err := AtomicReplace(target, []byte("x"), 0o755)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	if len(entries) != 0 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("expected empty tempdir, got entries: %v", names)
	}
}
