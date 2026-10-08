package main

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestReferences(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, body string
		want       []string
	}{
		{"prose", "See internal/pkg/file.go, then (cmd/tool/main.go).", []string{"cmd/tool/main.go", "internal/pkg/file.go"}},
		{"inline", "`internal/pkg/file.go` and **cmd/tool/**", []string{"cmd/tool/", "internal/pkg/file.go"}},
		{"fenced", "```go\ninternal/pkg/file.go\ncmd/tool/\n```", []string{"cmd/tool/", "internal/pkg/file.go"}},
		{"table", "|source|internal/pkg/file.go|\n| **cmd/tool/** |", []string{"cmd/tool/", "internal/pkg/file.go"}},
		{"link", "[internal/pkg/file.go](cmd/tool/main.go#entry)", []string{"cmd/tool/main.go", "internal/pkg/file.go"}},
		{"link with title", "[source file](internal/pkg/file.go \"source\")", []string{"internal/pkg/file.go"}},
		{"label spanning lines", "[source\ninternal/pkg/file.go](cmd/tool/main.go)", []string{"cmd/tool/main.go", "internal/pkg/file.go"}},
		{"multiword link label", "[source internal/pkg/file.go](cmd/tool/main.go)", []string{"cmd/tool/main.go", "internal/pkg/file.go"}},
		{"quoted", "\"internal/pkg/file.go\" '__cmd/tool/__'", []string{"cmd/tool/", "internal/pkg/file.go"}},
		{"angle destination", "[source](<internal/pkg/file.go#entry>)", []string{"internal/pkg/file.go"}},
		{"relative destination", "[internal/pkg/file.go](../../cmd/tool/main.go)", []string{"internal/pkg/file.go"}},
		{"URL destination", "[internal/pkg/file.go](https://example.test/cmd/tool/main.go)", []string{"internal/pkg/file.go"}},
		{"suffixes", "internal/pkg/file.go:12 internal/pkg/file.go:12-34#L12-L34 cmd/tool/#heading", []string{"cmd/tool/", "internal/pkg/file.go"}},
		{"components", "internal/a_1/a-b/c.d/.hidden.go cmd/v2.0/", []string{"cmd/v2.0/", "internal/a_1/a-b/c.d/.hidden.go"}},
		{"root directories", "cmd/ internal/", []string{"cmd/", "internal/"}},
		{"long line", strings.Repeat("x", 128*1024) + " internal/pkg/file.go", []string{"internal/pkg/file.go"}},
		{"URL whole", "https://example.test/internal/pkg/file.go https://x/(internal/pkg/file.go) file:///cmd/tool/", nil},
		{"absolute", "/internal/pkg/file.go /tmp/cmd/tool/main.go C:/internal/pkg/file.go", nil},
		{"traversal", "../internal/pkg/file.go ./cmd/tool/ internal/../pkg/file.go internal/./pkg/file.go", nil},
		{"trailing traversal", "internal/pkg/. internal/pkg/.. internal/pkg/... internal/pkg/file.go/..", nil},
		{"longer dotted names", "internal/pkg/file.go.. internal/pkg/file.go...", nil},
		{"sentence after suffix", "(internal/pkg/file.go:12-34).", []string{"internal/pkg/file.go"}},
		{"globs", "internal/*/file.go internal/pkg/*.go internal/[ab]/file.go internal/{a,b}/file.go **/cmd/tool/main.go internal/pkg/file.go?", nil},
		{"placeholders", "internal/<pkg>/file.go internal/{pkg}/file.go internal/$pkg/file.go internal/.../file.go", nil},
		{"basenames", "main.go file.go:12-34 pkg/file.go", nil},
		{"longer references", "internal/pkg/file.go.bak internal/pkg/file.go/extra internal/pkg/file.go_extra other/internal/pkg/file.go internal/pkg//file.go", nil},
		{"invalid components", "internal/päckage/file.go internal/pkg/@file.go internal/pkg/file+.go internal/pkg/file.go?query=1", nil},
		{"malformed lines", "internal/pkg/file.go:12- internal/pkg/file.go:12-34-56 internal/pkg/file.go:abc", nil},
		{"invalid nested delimiters", "internal/(pkg)/file.go internal/pkg/file.go[ab] internal/pkg/file.go{a,b} internal/<cmd/tool/main.go>/file.go", nil},
		{"quoted placeholders", "internal/\"pkg\"/file.go internal/`pkg`/file.go", nil},
		{"embedded link shape", "internal/[pkg](cmd/tool/main.go)/file.go", nil},
		{"embedded link without extension", "internal/[pkg](cmd/tool/main.go)", nil},
		{"pipe glob in inline code", "`internal/[foo|internal/a.go]`", nil},
		{"pipe glob in prose", "internal/[foo|internal/a.go]", nil},
		{"pipe glob in table", "| source | `internal/[foo|internal/a.go]` |", nil},
		{"pipe brace glob", "internal/{foo|internal/a.go}", nil},
		{"pipe placeholder", "internal/<foo|internal/a.go>", nil},
		{"pipe URL in inline code", "`https://example.test/foo|internal/a.go`", nil},
		{"pipe longer path in inline code", "`other/path|internal/a.go`", nil},
		{"pipe glob with link shape", "internal/[foo|[internal/a.go](cmd/a.go)]", nil},
		{"inline pipe glob with link shape", "`internal/[foo|[internal/a.go](cmd/a.go)]`", nil},
		{"inline pipe URL with link shape", "`https://example.test/foo|[internal/a.go](cmd/a.go)`", nil},
		{"pipe URL in prose", "https://example.test/foo|internal/a.go", nil},
		{"pipe glob outside table", "internal/*|internal/a.go", nil},
		{"pipe longer path outside table", "other/path|internal/a.go", nil},
		{"nested link label", "[source [internal/a.go]](cmd/a.go)", []string{"cmd/a.go", "internal/a.go"}},
		{"deeply nested link label", "[source [[internal/a.go]]](cmd/a.go#entry)", []string{"cmd/a.go", "internal/a.go"}},
		{"nested glob in label", "[source internal/[foo|internal/a.go]](cmd/a.go)", []string{"cmd/a.go"}},
		{"nested embedded link", "internal/[source [internal/a.go]](cmd/a.go)", nil},
		{"adjacent inline citations", "`internal/a.go`,`internal/b.go`", []string{"internal/a.go", "internal/b.go"}},
		{"adjacent double backticks", "``internal/a.go``,``internal/b.go``", []string{"internal/a.go", "internal/b.go"}},
		{"adjacent inline glob", "`internal/[foo|internal/a.go]`,`internal/b.go`", []string{"internal/b.go"}},
		{"inline table cells", "|`internal/a.go`|`cmd/a.go`|", []string{"cmd/a.go", "internal/a.go"}},
		{"table link cells", "|[source [internal/a.go]](cmd/a.go)|", []string{"cmd/a.go", "internal/a.go"}},
		{"table without outer pipes", "Source|Path\n---|---\nfile|internal/a.go\nlink|[internal/b.go](cmd/a.go)", []string{"cmd/a.go", "internal/a.go", "internal/b.go"}},
		{"Unicode space before link", "x\u2003[internal/a.go](cmd/a.go)", []string{"cmd/a.go", "internal/a.go"}},
		{"Unicode space in label", "[source\u00a0internal/a.go](cmd/a.go)", []string{"cmd/a.go", "internal/a.go"}},
		{"escaped opening bracket in label", `[source \[ internal/a.go](cmd/a.go)`, []string{"cmd/a.go", "internal/a.go"}},
		{"escaped closing bracket in label", `[source \] internal/a.go](cmd/a.go)`, []string{"cmd/a.go", "internal/a.go"}},
		{"escaped bracket before table", "Literal \\[ example.\n|source|internal/a.go|", []string{"internal/a.go"}},
		{"odd backslashes in label", `[source \\\[ internal/a.go](cmd/a.go)`, []string{"cmd/a.go", "internal/a.go"}},
		{"even backslashes in label", `[source \\[ internal/a.go]](cmd/a.go)`, []string{"cmd/a.go", "internal/a.go"}},
		{"escaped pipe in table", `|source|other/path\|internal/a.go|`, nil},
		{"even backslashes before table pipe", `|source\\|internal/a.go|`, []string{"internal/a.go"}},
		{"escaped bracket in excluded link shape", `internal/\[source](cmd/a.go)`, nil},
		{"escaped bracket in excluded table glob", `|source|internal/[foo\]|internal/a.go]|`, nil},
		{"escaped bracket in excluded inline glob", "`internal/[foo\\]|internal/a.go]`", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := references(tt.body); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("references = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestInventoryMarkdownBoundaries(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	testWrite(t, root, "internal/a.go", "package a\n")
	testWrite(t, root, "cmd/a.go", "package main\n")
	testWrite(t, root, specsDir+"/a.md", "| source | `internal/[foo|internal/false.go]` |\n")
	testWrite(t, root, specsDir+"/b.md", "[source [internal/a.go]](cmd/a.go)\n"+
		"`internal/a.go`,`internal/missing.go`\nx\u2003[internal/a.go](cmd/a.go)\n")
	want := specsDir + "/b.md\tcmd/a.go\texisting\n" +
		specsDir + "/b.md\tinternal/a.go\texisting\n" +
		specsDir + "/b.md\tinternal/missing.go\tmissing\n"
	var out bytes.Buffer
	if err := run(root, &out); err != nil {
		t.Fatal(err)
	}
	if out.String() != want {
		t.Fatalf("inventory = %q, want %q", out.String(), want)
	}
}

func TestInventoryMarkdownEscapes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, body string
		want       []string
	}{
		{"opening bracket", `[source \[ internal/a.go](cmd/a.go)`, []string{"cmd/a.go", "internal/a.go"}},
		{"closing bracket", `[source \] internal/a.go](cmd/a.go)`, []string{"cmd/a.go", "internal/a.go"}},
		{"bracket before table", "Literal \\[ example.\n|source|internal/a.go|", []string{"internal/a.go"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			testWrite(t, root, "internal/a.go", "package a\n")
			testWrite(t, root, "cmd/a.go", "package main\n")
			testWrite(t, root, specsDir+"/a.md", tt.body)
			var want, out bytes.Buffer
			for _, path := range tt.want {
				want.WriteString(specsDir + "/a.md\t" + path + "\texisting\n")
			}
			if err := run(root, &out); err != nil {
				t.Fatal(err)
			}
			if out.String() != want.String() {
				t.Fatalf("inventory = %q, want %q", out.String(), want.String())
			}
		})
	}
}

func TestInventory(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	testWrite(t, root, "cmd/tool/main.go", "package main\n")
	testWrite(t, root, "internal/pkg/file.go", "package pkg\n")
	testWrite(t, root, "docs/specs/architecture/b.md", "cmd/tool/ internal/pkg/file.go internal/missing.go")
	testWrite(t, root, "docs/specs/architecture/a.md", "internal/pkg/file.go:12 internal/pkg/file.go cmd/tool/main.go cmd/tool/")
	testWrite(t, root, "docs/specs/architecture/nested/ignored.md", "internal/ignored.go")
	testWrite(t, root, "docs/specs/architecture/ignored.txt", "internal/ignored.go")
	testWrite(t, root, "docs/specs/elsewhere.md", "internal/ignored.go")
	before := testSnapshot(t, root)
	want := "docs/specs/architecture/a.md\tcmd/tool/\texisting\n" +
		"docs/specs/architecture/a.md\tcmd/tool/main.go\texisting\n" +
		"docs/specs/architecture/a.md\tinternal/pkg/file.go\texisting\n" +
		"docs/specs/architecture/b.md\tcmd/tool/\texisting\n" +
		"docs/specs/architecture/b.md\tinternal/missing.go\tmissing\n" +
		"docs/specs/architecture/b.md\tinternal/pkg/file.go\texisting\n"
	for range 2 {
		var out bytes.Buffer
		if err := run(root, &out); err != nil {
			t.Fatal(err)
		}
		if out.String() != want {
			t.Fatalf("inventory = %q, want %q", out.String(), want)
		}
	}
	if after := testSnapshot(t, root); !reflect.DeepEqual(after, before) {
		t.Fatal("inventory changed repository content or metadata")
	}
	for _, path := range []string{"internal/pkg/file.go", "cmd/tool/main.go", "cmd/tool"} {
		if err := os.Remove(filepath.Join(root, path)); err != nil {
			t.Fatal(err)
		}
	}
	var out bytes.Buffer
	if err := run(root, &out); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != strings.ReplaceAll(want, "existing", "missing") {
		t.Fatalf("after deletion = %q", got)
	}
}

func TestEmptyInventory(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, specsDir), 0755); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := run(root, &out); err != nil || out.Len() != 0 {
		t.Fatalf("empty inventory: %q, %v", out.String(), err)
	}
}

func TestCommand(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, diagnostic string
		setup            func(*testing.T, string)
	}{
		{"missing targets succeed", "", func(t *testing.T, root string) {
			testWrite(t, root, specsDir+"/a.md", "internal/missing.go")
		}},
		{"discovery", "discover specs", func(t *testing.T, root string) {}},
		{"read", "read docs/specs/architecture/b.md", func(t *testing.T, root string) {
			testWrite(t, root, specsDir+"/a.md", "internal/missing.go")
			testSymlink(t, "missing-spec", filepath.Join(root, specsDir, "b.md"))
		}},
		{"inspect", "inspect internal/loop.go", func(t *testing.T, root string) {
			testWrite(t, root, specsDir+"/a.md", "internal/a.go internal/loop.go")
			if err := os.MkdirAll(filepath.Join(root, "internal"), 0755); err != nil {
				t.Fatal(err)
			}
			testSymlink(t, "loop.go", filepath.Join(root, "internal/loop.go"))
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			tt.setup(t, root)
			cmd := exec.Command(os.Args[0], "-test.run=^TestHelperProcess$")
			cmd.Dir = root
			cmd.Env = append(os.Environ(), "GO_TEST_HELPER_PROCESS=1")
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			err := cmd.Run()
			if tt.diagnostic == "" {
				if err != nil || stderr.Len() != 0 || stdout.String() != specsDir+"/a.md\tinternal/missing.go\tmissing\n" {
					t.Fatalf("successful command: %v, stdout %q, stderr %q", err, stdout.String(), stderr.String())
				}
				return
			}
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 1 {
				t.Fatalf("exit = %v, want code 1", err)
			}
			if stdout.Len() != 0 || !strings.Contains(stderr.String(), "spec-reference-inventory: "+tt.diagnostic) {
				t.Fatalf("failure: stdout %q, stderr %q", stdout.String(), stderr.String())
			}
		})
	}
}

func TestHelperProcess(t *testing.T) {
	if os.Getenv("GO_TEST_HELPER_PROCESS") != "1" {
		return
	}
	main()
	os.Exit(0)
}

type testFailWriter struct{ err error }

func (w testFailWriter) Write(p []byte) (int, error) { return 0, w.err }

func TestOutputFailure(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	testWrite(t, root, specsDir+"/a.md", "internal/missing.go")
	want := errors.New("output unavailable")
	if err := run(root, testFailWriter{want}); !errors.Is(err, want) || !strings.Contains(err.Error(), "write inventory") {
		t.Fatalf("output error = %v", err)
	}
}

func testWrite(t *testing.T, root, path, body string) {
	t.Helper()
	abs := filepath.Join(root, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(abs), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
}

func testSymlink(t *testing.T, target, path string) {
	t.Helper()
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
}

func testSnapshot(t *testing.T, root string) map[string]any {
	t.Helper()
	result := make(map[string]any)
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		result[path] = []any{info.Mode(), info.Size(), info.ModTime()}
		if !entry.IsDir() {
			body, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			result[path+":content"] = string(body)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}
