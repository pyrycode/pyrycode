// Command spec-reference-inventory lists historical source references and their
// existence in the current checkout. Run from the repository root. Missing
// targets are review candidates, not errors or judgments about historical claims.
package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

const specsDir = "docs/specs/architecture"

type record struct {
	spec, path, status string
}

func main() {
	if err := run(".", os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "spec-reference-inventory: %v\n", err)
		os.Exit(1)
	}
}

func run(root string, stdout io.Writer) error {
	entries, err := os.ReadDir(filepath.Join(root, specsDir))
	if err != nil {
		return fmt.Errorf("discover specs in %s: %w", specsDir, err)
	}
	var records []record
	// ReadDir sorts entries by name; references sorts each spec's unique paths.
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		spec := specsDir + "/" + entry.Name()
		body, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(spec)))
		if err != nil {
			return fmt.Errorf("read %s: %w", spec, err)
		}
		for _, path := range references(string(body)) {
			status := "existing"
			_, err := os.Stat(filepath.Join(root, filepath.FromSlash(path)))
			if errors.Is(err, fs.ErrNotExist) {
				status = "missing"
			} else if err != nil {
				return fmt.Errorf("inspect %s referenced by %s: %w", path, spec, err)
			}
			records = append(records, record{spec, path, status})
		}
	}
	for _, record := range records {
		if _, err := fmt.Fprintf(stdout, "%s\t%s\t%s\n", record.spec, record.path, record.status); err != nil {
			return fmt.Errorf("write inventory: %w", err)
		}
	}
	return nil
}
