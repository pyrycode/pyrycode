package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestScanUsesGoSyntaxAndBuildConstraints(t *testing.T) {
	dir := t.TempDir()
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
