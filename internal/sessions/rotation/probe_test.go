package rotation

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseProcFD(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		target string
		want   string
	}{
		{"regular file", "/Users/jane/foo.jsonl", "/Users/jane/foo.jsonl"},
		{"socket", "socket:[12345]", ""},
		{"pipe", "pipe:[6789]", ""},
		{"anon_inode", "anon_inode:[bpf-prog]", ""},
		{"eventfd", "[eventfd]", ""},
		{"empty", "", ""},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := parseProcFD(tc.target); got != tc.want {
				t.Errorf("parseProcFD(%q) = %q, want %q", tc.target, got, tc.want)
			}
		})
	}
}

func TestParseLsofOutput_FilesAndSockets(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile(filepath.Join("testdata", "lsof_basic.txt"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	got := parseLsofOutput(string(raw))
	wantNames := []string{
		"/Users/jane/Workspace",
		"/usr/local/bin/claude",
		"/dev/ttys003",
		"/dev/ttys003",
		"/dev/ttys003",
		"/Users/jane/.claude/projects/-Users-jane-Workspace/8a4cf9b2-7e5d-4d3a-9fb2-12c4f8a1de91.jsonl",
	}
	if len(got) != len(wantNames) {
		t.Fatalf("len(got) = %d, want %d (got = %+v)", len(got), len(wantNames), got)
	}
	for i, name := range wantNames {
		if got[i].Name != name {
			t.Errorf("got[%d].Name = %q, want %q", i, got[i].Name, name)
		}
	}
}

func TestParseLsofOutput_EmptyAfterPID(t *testing.T) {
	t.Parallel()
	raw := "p4711\n"
	got := parseLsofOutput(raw)
	if len(got) != 0 {
		t.Errorf("len(got) = %d, want 0 (got = %+v)", len(got), got)
	}
}

func TestParseLsofOutput_OrphanFRecord(t *testing.T) {
	t.Parallel()
	// f12u with no following 'n' line, then a fresh f/n pair. The orphan
	// must be dropped and the valid pair returned without panic.
	raw := "p4711\nf12u\nf3\n/n/Users/jane/foo.jsonl\n"
	// Note: a stray '/' line is malformed; the real data has 'n' prefix.
	// Reconstruct properly:
	raw = "p4711\nf12u\nf3\nn/Users/jane/foo.jsonl\n"
	got := parseLsofOutput(raw)
	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1 (got = %+v)", len(got), got)
	}
	if got[0].FD != "3" || got[0].Name != "/Users/jane/foo.jsonl" {
		t.Errorf("got[0] = %+v", got[0])
	}
}

func TestParseLsofOutput_PathWithSpaces(t *testing.T) {
	t.Parallel()
	raw := "p4711\nf3\nn/Users/jane/with space/foo.jsonl\n"
	got := parseLsofOutput(raw)
	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1", len(got))
	}
	if got[0].Name != "/Users/jane/with space/foo.jsonl" {
		t.Errorf("got[0].Name = %q", got[0].Name)
	}
}
