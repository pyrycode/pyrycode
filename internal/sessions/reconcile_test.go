package sessions

import (
	"os"
	"path/filepath"
	"testing"
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
		// Spaces (and any other non-alphanumeric) must map to '-' too, matching
		// claude. The '/'-and-'.'-only encoder left spaces intact and could not
		// find the transcript for a workdir like the vault's "Second Brain".
		{"a b c", "a-b-c"},
		{"/foo/Second Brain", "-foo-Second-Brain"},
		{"/Users/juhanailmoniemi/obsidian-vault/Second Brain",
			"-Users-juhanailmoniemi-obsidian-vault-Second-Brain"},
	}
	for _, c := range cases {
		if got := encodeWorkdir(c.in); got != c.want {
			t.Errorf("encodeWorkdir(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestDefaultClaudeSessionsDir_ResolvesSymlinks (#989): claude encodes its
// SYMLINK-RESOLVED cwd into the projects folder name (macOS: a workdir under
// /var/folders/... writes transcripts under -private-var-folders-...), so the
// daemon must encode the resolved form too or every by-id resolver stats a
// folder claude never writes. Caught live: the pinned-id resolver looped
// "no such file" on -var-folders-... while the transcript grew under
// -private-var-folders-....
func TestDefaultClaudeSessionsDir_ResolvesSymlinks(t *testing.T) {
	t.Parallel()

	real := t.TempDir()
	realResolved, err := filepath.EvalSymlinks(real)
	if err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(realResolved, link); err != nil {
		t.Fatal(err)
	}

	viaLink := DefaultClaudeSessionsDir(link)
	viaReal := DefaultClaudeSessionsDir(realResolved)
	if viaLink != viaReal {
		t.Errorf("DefaultClaudeSessionsDir(symlink) = %q, want %q (the resolved form claude encodes)", viaLink, viaReal)
	}
}
