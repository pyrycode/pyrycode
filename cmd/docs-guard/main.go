// Command docs-guard bounds the size of the package overviews and keeps
// their heading structure honest.
//
// Run via `make docs-guard`, wired into `make check`. Scans every .md file
// under docs/knowledge/features and exits non-zero on either of two faults:
// a file over the size cap, and a line that markdown reads as a heading only
// because a wrapped paragraph put a ticket reference first.
//
// **Why the size cap.** QMD is the search surface every agent uses. It cuts a
// document into roughly 900-token chunks, and it does prefer a heading
// boundary: its break table scores an h1 at 100 down to a bare newline at 1.
// But it only looks for that boundary inside a narrow window around each
// 900-token mark. When a document's sections run much larger than one chunk,
// no heading falls inside the window, so the cut lands on a paragraph break
// and the chunk carries no heading with it.
//
// Measured 2026-08-31 against the then-315KB v2-session-manager.md, whose
// sections averaged 7000 bytes: a semantic query, a hybrid query and a
// keyword query with reranking off, all aimed at a topic whose canonical home
// was a section of that file, and none of the three returned the file. What
// came back instead was the frozen docs/knowledge/codebase/ archive. After
// the split the owning section document ranks first on the same query.
//
// So the cap is really a statement about section size: keep a document small
// enough that its sections are comparable to a chunk, and the chunker's
// heading preference starts working for you instead of being unreachable.
//
// **Why the heading check.** A paragraph line that wraps with a ticket
// reference first, "#1840 (below), and published on...", is a top-level
// heading as far as markdown is concerned. 72 of them had accumulated across
// 23 overviews, corrupting every document outline.
//
// **Why code and not a rule in a prompt.** Both faults are produced by the
// documentation agent, which already carries a prose rule against them. A
// prose rule is roughly 80% advisory, and the repo's own doctrine is that a
// safety net for one must be a different fabric: deterministic code, not a
// second rule that shares the first one's blind spot.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// featuresDir is the only directory scanned. Specs, ADRs and the frozen
// per-ticket archive are deliberately out of scope: a spec is written once
// and read by the ticket that owns it, and the archive is closed.
const featuresDir = "docs/knowledge/features"

// capBytes is the largest acceptable overview. It must agree with
// FEATURE_DOCS_CAP_BYTES in the dispatcher's src/docs-size.ts, which flags
// the same files to the documentation agent before it writes.
//
// Set against the collection that already retrieves well: after the
// 2026-08-31 split the median overview is about 8000 bytes and the 90th
// percentile about 20000. 50000 leaves room for a genuinely large package
// without reaching the size where a document's chunks stop sharing a topic.
const capBytes = 50_000

// falseHeading matches a line markdown reads as a heading because it opens
// with a hash and a digit. A real heading always has a space after its
// hashes, so this cannot match one.
var falseHeading = regexp.MustCompile(`^#[0-9]`)

func main() {
	var problems []string

	err := filepath.WalkDir(featuresDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".md") {
			return nil
		}

		info, err := d.Info()
		if err != nil {
			return err
		}
		if info.Size() > capBytes {
			problems = append(problems, fmt.Sprintf(
				"%s: %d bytes, over the %d-byte cap — split it at its ## headings, keeping the parent as a map",
				path, info.Size(), capBytes))
		}

		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for i, line := range strings.Split(string(body), "\n") {
			if falseHeading.MatchString(line) {
				problems = append(problems, fmt.Sprintf(
					"%s:%d: parses as a heading because it opens with a ticket reference — join it to the line above, or escape the hash",
					path, i+1))
			}
		}
		return nil
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "docs-guard: %v\n", err)
		os.Exit(1)
	}

	if len(problems) > 0 {
		fmt.Fprintf(os.Stderr, "docs-guard: %d problem(s)\n\n", len(problems))
		for _, p := range problems {
			fmt.Fprintf(os.Stderr, "  %s\n", p)
		}
		os.Exit(1)
	}
}
