package agentrun

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/pyrycode/tui-driver/pkg/tuidriver"
)

func TestResolveWorkdir_DarwinRealpath(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("darwin-only: /var symlink")
	}
	wd := t.TempDir()
	got, err := ResolveWorkdir(wd)
	if err != nil {
		t.Fatalf("ResolveWorkdir(%q): %v", wd, err)
	}
	const want = "/private/var/"
	if len(got) < len(want) || got[:len(want)] != want {
		t.Fatalf("ResolveWorkdir(%q) = %q, want prefix %q", wd, got, want)
	}
}

func TestResolveWorkdir_AlreadyResolved(t *testing.T) {
	t.Parallel()
	wd := t.TempDir()
	resolved, err := filepath.EvalSymlinks(wd)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	got, err := ResolveWorkdir(resolved)
	if err != nil {
		t.Fatalf("ResolveWorkdir: %v", err)
	}
	if got != resolved {
		t.Fatalf("ResolveWorkdir(%q) = %q, want %q", resolved, got, resolved)
	}
}

func TestResolveWorkdir_RelativePath(t *testing.T) {
	t.Parallel()
	got, err := ResolveWorkdir(".")
	if err != nil {
		t.Fatalf("ResolveWorkdir(\".\"): %v", err)
	}
	if !filepath.IsAbs(got) {
		t.Fatalf("ResolveWorkdir(\".\") = %q, want absolute", got)
	}
}

func TestResolveWorkdir_MissingPath(t *testing.T) {
	t.Parallel()
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	_, err := ResolveWorkdir(missing)
	if err == nil {
		t.Fatalf("ResolveWorkdir(%q) = nil error; want non-nil", missing)
	}
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("ResolveWorkdir(%q): error %v, want fs.ErrNotExist", missing, err)
	}
}

func TestEncodeProjectDir_DarwinRealpath(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("darwin-only: /var symlink")
	}
	wd := t.TempDir()
	got, err := EncodeProjectDir(wd)
	if err != nil {
		t.Fatalf("EncodeProjectDir(%q): %v", wd, err)
	}
	const want = "-private-var-folders-"
	if !strings.HasPrefix(got, want) {
		t.Fatalf("EncodeProjectDir(%q) = %q, want prefix %q", wd, got, want)
	}
}

func TestEncodeProjectDir_LiteralSubstitution(t *testing.T) {
	t.Parallel()
	wd := t.TempDir()
	resolved, err := filepath.EvalSymlinks(wd)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	got, err := EncodeProjectDir(resolved)
	if err != nil {
		t.Fatalf("EncodeProjectDir(%q): %v", resolved, err)
	}
	want := tuidriver.EncodeCwd(resolved)
	if got != want {
		t.Fatalf("EncodeProjectDir(%q) = %q, want %q", resolved, got, want)
	}
}

func TestEncodeProjectDir_NonAlnumBytes(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	cases := []struct {
		name    string
		segment string
		suffix  string
	}{
		{name: "underscores", segment: "Test_With_Underscores", suffix: "-Test-With-Underscores"},
		{name: "space", segment: "with space", suffix: "-with-space"},
		{name: "pre-existing dash idempotent", segment: "already-dashed", suffix: "-already-dashed"},
		{name: "mixed specials", segment: "a_b-c.d e", suffix: "-a-b-c-d-e"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			wd := filepath.Join(base, tc.segment)
			if err := os.MkdirAll(wd, 0o755); err != nil {
				t.Fatalf("MkdirAll(%q): %v", wd, err)
			}
			got, err := EncodeProjectDir(wd)
			if err != nil {
				t.Fatalf("EncodeProjectDir(%q): %v", wd, err)
			}
			if !strings.HasSuffix(got, tc.suffix) {
				t.Fatalf("EncodeProjectDir(%q) = %q, want suffix %q", wd, got, tc.suffix)
			}
		})
	}
}

func TestEncodeProjectDir_DotInPathSegment(t *testing.T) {
	t.Parallel()
	hidden := filepath.Join(t.TempDir(), ".hidden")
	if err := os.MkdirAll(hidden, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	got, err := EncodeProjectDir(hidden)
	if err != nil {
		t.Fatalf("EncodeProjectDir(%q): %v", hidden, err)
	}
	if !strings.HasSuffix(got, "--hidden") {
		t.Fatalf("EncodeProjectDir(%q) = %q, want suffix %q", hidden, got, "--hidden")
	}
}

func TestEncodeProjectDir_MissingPath(t *testing.T) {
	t.Parallel()
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	_, err := EncodeProjectDir(missing)
	if err == nil {
		t.Fatalf("EncodeProjectDir(%q) = nil error; want non-nil", missing)
	}
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("EncodeProjectDir(%q): error %v, want fs.ErrNotExist", missing, err)
	}
}
