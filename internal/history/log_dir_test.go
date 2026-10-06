package history

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func TestLogDir(t *testing.T) {
	root := t.TempDir()
	s := New(root)
	if _, err := s.LogDir("../escape"); !errors.Is(err, ErrInvalidID) {
		t.Fatalf("invalid ID: %v", err)
	}
	if _, err := s.LogDir(convA); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing log: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "conversations")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("lookup created directories")
	}
	appendN(t, s, convA, 0, 1)
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	want := historyDir(realRoot, convA)
	if got, err := s.LogDir(convA); err != nil || got != want || !filepath.IsAbs(got) {
		t.Fatalf("LogDir = %q, %v; want %q", got, err, want)
	}
	alias := filepath.Join(t.TempDir(), "root")
	if err := os.Symlink(root, alias); err != nil {
		t.Fatal(err)
	}
	if got, err := New(alias).LogDir(convA); err != nil || got != want {
		t.Fatalf("root alias = %q, %v", got, err)
	}
	for _, target := range []string{historyDir(root, convB), t.TempDir()} {
		if err := os.MkdirAll(target, 0o700); err != nil {
			t.Fatal(err)
		}
		moved := want + "-saved"
		if err := os.Rename(want, moved); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, want); err != nil {
			t.Fatal(err)
		}
		if _, err := s.LogDir(convA); !errors.Is(err, ErrNotContained) {
			t.Fatalf("redirected dir: %v", err)
		}
		if err := os.Remove(want); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(moved, want); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Rename(want, want+"-saved"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(want, []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LogDir(convA); err == nil {
		t.Fatal("file presented as log directory")
	}

}
