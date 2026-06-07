package turnevent

import (
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

// TestImportBoundary_StdlibOnly is the deterministic safety net for AC#5: the
// package depends on nothing but the standard library. It parses the imports of
// every production .go file in the package directory and rejects any import
// whose first path segment is dotted (the signature of a third-party module
// path such as github.com/… or golang.org/x/…). This forbids importing any
// transport, relay, wire-protocol, or external package — not just enumerated
// ones. A second, redundant-but-clearer check names the most likely violation
// (an intra-repo import) directly.
func TestImportBoundary_StdlibOnly(t *testing.T) {
	t.Parallel()

	// Go runs package tests with the working directory set to the package dir.
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}

	fset := token.NewFileSet()
	checked := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		checked++

		f, err := parser.ParseFile(fset, name, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, spec := range f.Imports {
			path := strings.Trim(spec.Path.Value, `"`)
			first, _, _ := strings.Cut(path, "/")
			if strings.Contains(first, ".") {
				t.Errorf("%s imports non-stdlib %q (first segment %q is dotted)", name, path, first)
			}
			if strings.HasPrefix(path, "github.com/pyrycode/pyrycode/") {
				t.Errorf("%s imports intra-repo %q; turnevent must be stdlib-only", name, path)
			}
		}
	}

	if checked == 0 {
		t.Fatal("no production .go files found to check; boundary test is vacuous")
	}
}
