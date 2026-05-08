package identity

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadOrCreate_FirstRunGeneratesAndPersists(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	parent := filepath.Join(dir, "subdir")
	path := filepath.Join(parent, "server-id")

	id1, err := LoadOrCreate(path)
	if err != nil {
		t.Fatalf("first LoadOrCreate: %v", err)
	}
	if id1 == "" {
		t.Fatalf("first LoadOrCreate returned empty id")
	}
	if _, err := ParseServerID(string(id1)); err != nil {
		t.Fatalf("returned id %q does not parse: %v", id1, err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read persisted file: %v", err)
	}
	if want := string(id1) + "\n"; string(raw) != want {
		t.Errorf("file contents = %q, want %q", raw, want)
	}

	parentInfo, err := os.Stat(parent)
	if err != nil {
		t.Fatalf("stat parent: %v", err)
	}
	if mode := parentInfo.Mode().Perm(); mode != 0o700 {
		t.Errorf("parent dir mode = %v, want 0700", mode)
	}

	fileInfo, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat file: %v", err)
	}
	if mode := fileInfo.Mode().Perm(); mode != 0o600 {
		t.Errorf("file mode = %v, want 0600", mode)
	}

	id2, err := LoadOrCreate(path)
	if err != nil {
		t.Fatalf("second LoadOrCreate: %v", err)
	}
	if id2 != id1 {
		t.Errorf("second LoadOrCreate returned %q, want %q", id2, id1)
	}
}

func TestLoadOrCreate_ExistingFileRoundTripsWithoutRewrite(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "server-id")
	const fixture = "550e8400-e29b-41d4-a716-446655440000"
	contents := []byte(fixture + "\n")
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	preInfo, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat seed: %v", err)
	}
	preMtime := preInfo.ModTime()

	id, err := LoadOrCreate(path)
	if err != nil {
		t.Fatalf("LoadOrCreate: %v", err)
	}
	if string(id) != fixture {
		t.Errorf("got id %q, want %q", id, fixture)
	}

	postBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("re-read: %v", err)
	}
	if string(postBytes) != string(contents) {
		t.Errorf("file rewritten: got %q, want %q", postBytes, contents)
	}
	postInfo, err := os.Stat(path)
	if err != nil {
		t.Fatalf("re-stat: %v", err)
	}
	if !postInfo.ModTime().Equal(preMtime) {
		t.Errorf("mtime changed: pre=%v post=%v", preMtime, postInfo.ModTime())
	}
}

func TestLoadOrCreate_ToleratesNoTrailingNewline(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "server-id")
	const fixture = "550e8400-e29b-41d4-a716-446655440000"
	contents := []byte(fixture)
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	preInfo, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat seed: %v", err)
	}
	preMtime := preInfo.ModTime()

	id, err := LoadOrCreate(path)
	if err != nil {
		t.Fatalf("LoadOrCreate: %v", err)
	}
	if string(id) != fixture {
		t.Errorf("got id %q, want %q", id, fixture)
	}

	postBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("re-read: %v", err)
	}
	if string(postBytes) != string(contents) {
		t.Errorf("file rewritten: got %q, want %q", postBytes, contents)
	}
	postInfo, err := os.Stat(path)
	if err != nil {
		t.Fatalf("re-stat: %v", err)
	}
	if !postInfo.ModTime().Equal(preMtime) {
		t.Errorf("mtime changed: pre=%v post=%v", preMtime, postInfo.ModTime())
	}
}

func TestLoadOrCreate_CorruptFileReturnsErrInvalidServerID(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		contents string
	}{
		{"not a uuid", "not-a-uuid\n"},
		{"empty", ""},
		{"uppercase", "550E8400-E29B-41D4-A716-446655440000\n"},
		{"leading whitespace", "  550e8400-e29b-41d4-a716-446655440000\n"},
		{"crlf", "550e8400-e29b-41d4-a716-446655440000\r\n"},
		{"double newline", "550e8400-e29b-41d4-a716-446655440000\n\n"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			path := filepath.Join(dir, "server-id")
			pre := []byte(tt.contents)
			if err := os.WriteFile(path, pre, 0o600); err != nil {
				t.Fatalf("seed file: %v", err)
			}

			id, err := LoadOrCreate(path)
			if !errors.Is(err, ErrInvalidServerID) {
				t.Errorf("err = %v, want errors.Is ErrInvalidServerID", err)
			}
			if id != "" {
				t.Errorf("id = %q, want empty on error", id)
			}

			post, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("re-read: %v", err)
			}
			if string(post) != string(pre) {
				t.Errorf("file mutated on corrupt-read path: got %q, want %q", post, pre)
			}
		})
	}
}

func TestLoadOrCreate_ReadFileError(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "server-id")
	// Make path itself a directory so ReadFile returns a non-ENOENT error.
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatalf("mkdir trap: %v", err)
	}

	id, err := LoadOrCreate(path)
	if err == nil {
		t.Fatalf("LoadOrCreate returned nil error for directory-as-path")
	}
	if errors.Is(err, ErrInvalidServerID) {
		t.Errorf("I/O error misclassified as ErrInvalidServerID: %v", err)
	}
	if id != "" {
		t.Errorf("id = %q, want empty on error", id)
	}
}
