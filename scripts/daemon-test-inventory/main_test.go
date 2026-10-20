package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestScanUsesGoSyntaxAndBuildConstraints(t *testing.T) {
	_, dir := inventoryTestTree(t)
	sources := map[string]string{
		"live_test.go":    "//go:build e2e_realclaude\n\npackage example\nimport (\"testing\";\"os/exec\")\n// func TestNotADeclaration(t *testing.T) {}\nfunc TestActual(\n t *testing.T,\n){ helper(t) }\nfunc helper(t *testing.T){ exec.LookPath(\"claude\") }\n",
		"offline_test.go": "//go:build !e2e_realclaude\n\npackage example\nimport \"testing\"\nfunc TestOffline(t *testing.T){}\n",
	}
	for name, source := range sources {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(source), 0600); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := scan(dir)
	if err != nil {
		t.Fatal(err)
	}
	indexed := map[string]function{}
	for _, row := range rows {
		indexed[row.Name] = row
	}
	if len(rows) != 3 || !indexed["TestActual"].Test || !indexed["TestActual"].LiveTagged || indexed["TestOffline"].LiveTagged || !indexed["helper"].ProviderCommand {
		t.Fatalf("incorrect AST facts: %+v", rows)
	}
	if len(indexed["TestActual"].References) != 1 || indexed["TestActual"].References[0] != "helper" {
		t.Fatal("lost package helper reference")
	}
	before := indexed["TestActual"].SourceSHA256
	path := filepath.Join(dir, "live_test.go")
	if err := os.WriteFile(path, []byte(sources["live_test.go"]+"\nfunc other(){}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	rows, err = scan(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.Name == "TestActual" && row.SourceSHA256 != before {
			t.Fatal("unrelated function changed the reviewed root fingerprint")
		}
	}
	beforeContext := rows[0].ContextSHA256
	path = filepath.Join(dir, "offline_test.go")
	if err := os.WriteFile(path, []byte(sources["offline_test.go"]+"\nconst cancellationBudget = 0\n"), 0600); err != nil {
		t.Fatal(err)
	}
	rows, err = scan(dir)
	if err != nil {
		t.Fatal(err)
	}
	if rows[0].ContextSHA256 == beforeContext {
		t.Fatal("package-level boundary change did not invalidate review context")
	}

}

func TestImportedAssertionsInvalidateReview(t *testing.T) {
	root, dir := inventoryTestTree(t)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "live_test.go"), []byte("package example\nfunc TestActual() {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, pkg := range []string{"scenario", "claudetape"} {
		t.Run(pkg, func(t *testing.T) {
			rows, err := scan(dir)
			if err != nil {
				t.Fatal(err)
			}
			before := rows[0].ContextSHA256
			shared := filepath.Join(root, "internal", "e2e", "internal", pkg)
			if err := os.MkdirAll(shared, 0700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(shared, "assertions.go")
			if err := os.WriteFile(path, []byte("package "+pkg+"\nfunc Require() {}\n"), 0600); err != nil {
				t.Fatal(err)
			}
			rows, err = scan(dir)
			if err != nil {
				t.Fatal(err)
			}
			if rows[0].ContextSHA256 == before {
				t.Fatal("imported assertion source did not invalidate review")
			}
			before = rows[0].ContextSHA256
			if err := os.WriteFile(path, []byte("package "+pkg+"\nfunc Require() { panic(1) }\n"), 0600); err != nil {
				t.Fatal(err)
			}
			rows, err = scan(dir)
			if err != nil {
				t.Fatal(err)
			}
			if rows[0].ContextSHA256 == before {
				t.Fatal("imported assertion edit did not invalidate review")
			}
		})
	}
}

func inventoryTestTree(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "internal", "e2e", "realclaude")
	if os.MkdirAll(dir, 0700) != nil {
		t.Fatal("fixture source directory")
	}
	for _, relative := range []string{"internal/e2emcp/boundary.go", "internal/acp/jsonrpc.go", "cmd/pyry/mcp_recording_stub.go", "cmd/pyry/main.go", "cmd/pyry/jsonrpc_stdio.go"} {
		path := filepath.Join(root, relative)
		if os.MkdirAll(filepath.Dir(path), 0700) != nil || os.WriteFile(path, []byte("package fixture\n"), 0600) != nil {
			t.Fatal("boundary fixture")
		}
	}
	return root, dir
}
func TestMCPTransportHookAndHandlersInvalidateReview(t *testing.T) {
	root, dir := inventoryTestTree(t)
	if os.WriteFile(filepath.Join(dir, "live_test.go"), []byte("package fixture\nfunc TestActual(){}\n"), 0600) != nil {
		t.Fatal("live fixture")
	}
	for _, relative := range []string{"internal/e2emcp/boundary.go", "internal/acp/jsonrpc.go", "cmd/pyry/mcp_recording_stub.go", "cmd/pyry/main.go", "cmd/pyry/jsonrpc_stdio.go", "cmd/pyry/mcp_files.go"} {
		rows, e := scan(dir)
		if e != nil {
			t.Fatal(e)
		}
		before := rows[0].ContextSHA256
		if os.WriteFile(filepath.Join(root, relative), []byte("package fixture\nfunc ChangedBoundary(){panic(1)}\n"), 0600) != nil {
			t.Fatal("actual boundary edit")
		}
		rows, e = scan(dir)
		if e != nil || rows[0].ContextSHA256 == before {
			t.Fatal("MCP source edit failed to invalidate review", relative, e)
		}
	}
}
