package canonicalpath

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

func testRealpath(t *testing.T, path string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatalf("EvalSymlinks(%q): %v", path, err)
	}
	return resolved
}

func testResolve(t *testing.T, input, want string) {
	t.Helper()
	got, err := Resolve(input)
	if err != nil || got != want || !filepath.IsAbs(got) {
		t.Fatalf("Resolve(%q) = (%q, %v), want (%q, nil), absolute", input, got, err, want)
	}
}

func TestResolve_DarwinRealpath(t *testing.T) {
	t.Parallel()
	if runtime.GOOS != "darwin" {
		t.Skip("darwin-only: /var or /tmp realpath alias")
	}
	path := t.TempDir()
	want := testRealpath(t, path)
	if want == path {
		t.Skip("temporary directory does not cross a macOS symlink alias")
	}
	testResolve(t, path, want)
}

func TestResolve_AlreadyResolved(t *testing.T) {
	t.Parallel()
	path := testRealpath(t, t.TempDir())
	testResolve(t, path, path)
}

func TestResolve_RelativeAndEmpty(t *testing.T) {
	t.Parallel()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	want := testRealpath(t, cwd)
	for _, tc := range []struct {
		name  string
		input string
	}{
		{"empty", ""},
		{"dot", "."},
		{"relative child", filepath.Join("..", filepath.Base(cwd))},
	} {
		t.Run(tc.name, func(t *testing.T) { testResolve(t, tc.input, want) })
	}
}

func TestResolve_FilesAndSymlinks(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	file := filepath.Join(dir, "MixedCaseFile")
	if err := os.WriteFile(file, []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		target string
	}{
		{"directory", dir},
		{"file", file},
	} {
		t.Run(tc.name, func(t *testing.T) {
			want := testRealpath(t, tc.target)
			testResolve(t, tc.target, want)
			link := filepath.Join(dir, tc.name+"-link")
			// Relative targets also exercise symlink traversal from its parent.
			target, err := filepath.Rel(dir, tc.target)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, link); err != nil {
				t.Fatal(err)
			}
			testResolve(t, link, want)
		})
	}
}

func TestResolve_MissingPath(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	missing := filepath.Join(dir, "missing")
	broken := filepath.Join(dir, "broken-link")
	if err := os.Symlink("missing", broken); err != nil {
		t.Fatal(err)
	}
	for _, input := range []string{missing, broken} {
		t.Run(filepath.Base(input), func(t *testing.T) {
			got, err := Resolve(input)
			if got != "" || !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("Resolve(%q) = (%q, %v), want empty path and fs.ErrNotExist", input, got, err)
			}
		})
	}
}

func TestResolve_CaseMismatchFoldsToOnDiskCase(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	workspace := filepath.Join(dir, "Workspace")
	if err := os.Mkdir(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "WORKSPACE")); errors.Is(err, fs.ErrNotExist) {
		t.Skip("case-sensitive filesystem: wrong-case path cannot resolve")
	} else if err != nil {
		t.Fatal(err)
	}
	testResolve(t, filepath.Join(dir, "WorkSpace"), filepath.Join(testRealpath(t, dir), "Workspace"))
}

func TestResolve_CorrectlyCasedMixedCaseUnchanged(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "MixedCaseDir")
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
	testResolve(t, path, filepath.Join(testRealpath(t, dir), "MixedCaseDir"))
}

func TestResolve_SiblingSafetyCaseSensitive(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	for _, name := range []string{"Workspace", "workspace"} {
		if err := os.Mkdir(filepath.Join(dir, name), 0o755); errors.Is(err, fs.ErrExist) {
			t.Skip("case-insensitive filesystem: cannot create case-differing siblings")
		} else if err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"Workspace", "workspace"} {
		testResolve(t, filepath.Join(dir, name), filepath.Join(testRealpath(t, dir), name))
	}
	// A missing third spelling must not resolve onto either sibling.
	got, err := Resolve(filepath.Join(dir, "WORKSPACE"))
	if got != "" || !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Resolve ambiguous spelling = (%q, %v), want empty path and fs.ErrNotExist", got, err)
	}
}

func TestMatchEntry(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		names  []string
		target string
		want   string
	}{
		{"exact after fold", []string{"WORKSPACE", "Workspace"}, "Workspace", "Workspace"},
		{"exact before fold", []string{"Workspace", "workspace"}, "Workspace", "Workspace"},
		{"unique fold", []string{"Workspace", "unrelated"}, "WorkSpace", "Workspace"},
		{"ambiguous fold", []string{"Workspace", "workspace"}, "WORKSPACE", "WORKSPACE"},
		{"absent", []string{"Workspace"}, "Other", "Other"},
		{"empty entries", nil, "Workspace", "Workspace"},
		{"unicode fold", []string{"Kelvin"}, "KELVIN", "Kelvin"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := fstest.MapFS{}
			for _, name := range tc.names {
				fixture[name] = &fstest.MapFile{}
			}
			entries, err := fs.ReadDir(fixture, ".")
			if err != nil {
				t.Fatal(err)
			}
			if got := matchEntry(entries, tc.target); got != tc.want {
				t.Fatalf("matchEntry(%v, %q) = %q, want %q", tc.names, tc.target, got, tc.want)
			}
		})
	}
}

func TestCanonicalCase_AbsentComponent(t *testing.T) {
	t.Parallel()
	path := filepath.Join(testRealpath(t, t.TempDir()), "Missing", "StillMissing")
	if got := canonicalCase(path); got != path {
		t.Fatalf("canonicalCase(%q) = %q, want unchanged missing components", path, got)
	}
}

func TestResolve_Permissions(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		mode fs.FileMode
	}{
		{"unreadable case probe", 0o100},
		{"inaccessible path", 0o000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			parent := filepath.Join(dir, "Restricted")
			if err := os.Mkdir(parent, 0o700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(parent, "MixedCaseFile")
			if err := os.WriteFile(path, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			want := testRealpath(t, path)
			t.Cleanup(func() {
				if err := os.Chmod(parent, 0o700); err != nil {
					t.Errorf("restore parent permissions: %v", err)
				}
			})
			if err := os.Chmod(parent, tc.mode); err != nil {
				t.Fatal(err)
			}
			if tc.mode == 0o100 {
				if _, err := os.ReadDir(parent); !errors.Is(err, fs.ErrPermission) {
					t.Skipf("effective permissions do not deny directory listing: %v", err)
				}
				if _, err := filepath.EvalSymlinks(path); err != nil {
					t.Skipf("filesystem cannot resolve through an execute-only directory: %v", err)
				}
				testResolve(t, path, want)
			} else {
				if _, err := filepath.EvalSymlinks(path); !errors.Is(err, fs.ErrPermission) {
					t.Skipf("effective permissions do not deny path traversal: %v", err)
				}
				got, err := Resolve(path)
				if got != "" || !errors.Is(err, fs.ErrPermission) {
					t.Fatalf("Resolve(%q) = (%q, %v), want empty path and fs.ErrPermission", path, got, err)
				}
			}
		})
	}
}

func TestResolve_DeletedCwd(t *testing.T) {
	t.Parallel()
	cwd := filepath.Join(t.TempDir(), "cwd")
	if err := os.Mkdir(cwd, 0o700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestHelperProcess$", "-test.v")
	cmd.Dir = cwd
	cmd.Env = append(os.Environ(), "GO_TEST_HELPER_PROCESS=1", "PWD="+cwd)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("deleted-cwd helper: %v\n%s", err, output)
	}
	if strings.Contains(string(output), "--- SKIP: TestHelperProcess") {
		t.Skipf("deleted-cwd prerequisite unavailable:\n%s", output)
	}
	t.Logf("deleted-cwd helper:\n%s", output)
}

func TestHelperProcess(t *testing.T) {
	if os.Getenv("GO_TEST_HELPER_PROCESS") != "1" {
		return
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(cwd); err != nil {
		t.Skipf("platform cannot remove the child process's cwd: %v", err)
	}
	if _, err := filepath.Abs("."); !errors.Is(err, fs.ErrNotExist) {
		t.Skipf("deleted cwd does not make absolute-path resolution fail with fs.ErrNotExist: %v", err)
	}
	for _, input := range []string{".", ""} {
		got, err := Resolve(input)
		if got != "" || !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("Resolve(%q) in deleted cwd = (%q, %v), want empty path and fs.ErrNotExist", input, got, err)
		}
	}
}
