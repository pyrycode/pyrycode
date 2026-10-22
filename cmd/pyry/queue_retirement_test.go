package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestQueueSnapshotsRetired prevents either publication arm or its wire
// registration from returning, including publication to non-thread peers.
func TestQueueSnapshotsRetired(t *testing.T) {
	for _, dir := range []string{".", "../../internal/relay", "../../internal/protocol"} {
		files, err := filepath.Glob(filepath.Join(dir, "*.go"))
		if err != nil {
			t.Fatal(err)
		}
		if len(files) == 0 {
			t.Fatalf("no sources in %s", dir)
		}
		for _, path := range files {
			if strings.HasSuffix(path, "_test.go") {
				continue
			}
			file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
			if err != nil {
				t.Fatal(err)
			}
			ast.Inspect(file, func(node ast.Node) bool {
				lit, ok := node.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					return true
				}
				value, err := strconv.Unquote(lit.Value)
				if err != nil {
					t.Fatal(err)
				}
				if value == "queue_state" {
					t.Errorf("retired app vocabulary in %s", path)
				}
				return true
			})
		}
	}
}
