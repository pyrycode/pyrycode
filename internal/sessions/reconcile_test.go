package sessions

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestEncodeWorkdir(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in, want string
	}{
		{"", ""},
		{"/", "-"},
		{"/foo/bar", "-foo-bar"},
		{"/foo/.bar", "-foo--bar"},
		{"/Users/x/Workspace/Projects/.pyrycode-worktrees/architect-38",
			"-Users-x-Workspace-Projects--pyrycode-worktrees-architect-38"},
		{"foo.bar", "foo-bar"},
		{"a..b", "a--b"},
		{"a//b", "a--b"},
	}
	for _, c := range cases {
		if got := encodeWorkdir(c.in); got != c.want {
			t.Errorf("encodeWorkdir(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// touchJSONL creates dir/<id>.jsonl and stamps it with mtime.
func touchJSONL(t *testing.T, dir string, id string, mtime time.Time) {
	t.Helper()
	path := filepath.Join(dir, id+".jsonl")
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatalf("chtimes %s: %v", path, err)
	}
}

func TestMostRecentJSONL_PicksLatestMtime(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	older := SessionID("00000000-0000-4000-8000-000000000001")
	middle := SessionID("00000000-0000-4000-8000-000000000002")
	newest := SessionID("00000000-0000-4000-8000-000000000003")

	base := time.Now().Add(-1 * time.Hour)
	touchJSONL(t, dir, string(older), base)
	touchJSONL(t, dir, string(middle), base.Add(10*time.Minute))
	touchJSONL(t, dir, string(newest), base.Add(20*time.Minute))

	got, err := mostRecentJSONL(dir)
	if err != nil {
		t.Fatalf("mostRecentJSONL: %v", err)
	}
	if got != newest {
		t.Errorf("got %q, want %q", got, newest)
	}
}

func TestMostRecentJSONL_IgnoresNonJSONL(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	valid := SessionID("11111111-1111-4111-8111-111111111111")
	now := time.Now()
	touchJSONL(t, dir, string(valid), now.Add(-time.Hour))

	// noise: non-jsonl extensions, malformed UUID stems, wrong-length stems,
	// uppercase (non-canonical), backup files, and a subdirectory.
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "session.jsonl.bak"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "not-a-uuid.jsonl"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "AAAAAAAA-AAAA-4AAA-8AAA-AAAAAAAAAAAA.jsonl"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Stamp the noise files with a much-newer mtime so they would win if
	// they were not filtered out.
	for _, name := range []string{"notes.txt", "session.jsonl.bak", "not-a-uuid.jsonl", "AAAAAAAA-AAAA-4AAA-8AAA-AAAAAAAAAAAA.jsonl"} {
		if err := os.Chtimes(filepath.Join(dir, name), now, now); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "subdir"), 0o700); err != nil {
		t.Fatal(err)
	}

	got, err := mostRecentJSONL(dir)
	if err != nil {
		t.Fatalf("mostRecentJSONL: %v", err)
	}
	if got != valid {
		t.Errorf("got %q, want %q (noise files should be ignored)", got, valid)
	}
}

func TestMostRecentJSONL_EmptyDir(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	got, err := mostRecentJSONL(dir)
	if err != nil {
		t.Fatalf("mostRecentJSONL: %v", err)
	}
	if got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

func TestMostRecentJSONL_SingleEntry(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	id := SessionID("22222222-2222-4222-8222-222222222222")
	touchJSONL(t, dir, string(id), time.Now())
	got, err := mostRecentJSONL(dir)
	if err != nil {
		t.Fatalf("mostRecentJSONL: %v", err)
	}
	if got != id {
		t.Errorf("got %q, want %q", got, id)
	}
}

func TestMostRecentJSONL_TieBreakDeterministic(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// Same mtime; lex-larger ID should win deterministically.
	a := SessionID("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa")
	b := SessionID("bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb")
	when := time.Now()
	touchJSONL(t, dir, string(a), when)
	touchJSONL(t, dir, string(b), when)
	for i := 0; i < 5; i++ {
		got, err := mostRecentJSONL(dir)
		if err != nil {
			t.Fatalf("mostRecentJSONL: %v", err)
		}
		if got != b {
			t.Errorf("iter %d: got %q, want %q (lex-larger on tie)", i, got, b)
		}
	}
}

func TestMostRecentJSONL_MissingDir(t *testing.T) {
	t.Parallel()
	got, err := mostRecentJSONL(filepath.Join(t.TempDir(), "does-not-exist"))
	if err == nil {
		t.Errorf("got %q nil err, want a read error", got)
	}
}
