package history

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/pyrycode/pyrycode/internal/conversations"
)

func TestEnsureLogDir(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	s := New(root)
	id := conversations.ConversationID("11111111-1111-4111-8111-111111111111")
	if _, err := s.EnsureLogDir("../../private"); !errors.Is(err, ErrInvalidID) {
		t.Fatal("invalid ID accepted", err)
	}
	dir, err := s.EnsureLogDir(id)
	if err != nil {
		t.Fatal(err)
	}
	files, err := os.ReadDir(dir)
	if err != nil || len(files) != 0 {
		t.Fatal("history fabricated", err)
	}
	if got, err := s.LatestEntryID(id); err != nil || got != 0 {
		t.Fatal("empty history cursor", got, err)
	}
	if err := os.Rename(dir, dir+"-saved"); err != nil {
		t.Fatal(err)
	}
	other := t.TempDir()
	if err := os.Symlink(other, dir); err != nil {
		t.Fatal(err)
	}
	if _, err := s.EnsureLogDir(id); !errors.Is(err, ErrNotContained) {
		t.Fatal("warm containment accepted", err)
	}
	if entries, err := os.ReadDir(other); err != nil || len(entries) != 0 {
		t.Fatal("redirected write", err)
	}
	if _, err := os.Stat(filepath.Join(root, "private")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("invalid ID touched storage")
	}
}
