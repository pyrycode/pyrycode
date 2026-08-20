// Tests for the substrate guard.
//
// This file is scanned by the guard it tests. It does not end with any
// allowlist suffix and must not be added to one, so it may not spell a banned
// substrate literal anywhere in its own source. Every fixture below therefore
// takes its literal from the patterns table at run time, via bannedLiteral —
// which also means a rename in that table reddens here instead of quietly
// turning each fixture into an ordinary file and each assertion into a
// tautology.
package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// fixturePattern names the pattern whose substr every fixture carries. The
// name is safe to spell here: it differs from its own substr in case and
// separator, and no pattern name in the table contains any pattern's substr.
// If that ever stops holding, the guard flags this file on the next run.
const fixturePattern = "pasted-text chip"

// bannedLiteral returns the substr of the pattern registered under name, and
// fails the test when no such pattern exists.
func bannedLiteral(t *testing.T, name string) []byte {
	t.Helper()

	for _, p := range patterns {
		if p.name == name {
			return p.substr
		}
	}
	t.Fatalf("no pattern named %q in patterns; every fixture below would carry no banned literal and every assertion would pass for the wrong reason", name)
	return nil
}

// writeFixture creates <root>/<rel>, parents included, as a file whose body
// contains body. The body is always Go-shaped source; rel alone decides
// whether the walk treats it as a .go file, which is what the non-.go rows
// exercise.
func writeFixture(t *testing.T, root, rel string, body []byte) {
	t.Helper()

	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("creating parents of %s: %v", rel, err)
	}
	var src bytes.Buffer
	src.WriteString("package fixture\n\nconst s = \"")
	src.Write(body)
	src.WriteString("\"\n")
	if err := os.WriteFile(path, src.Bytes(), 0o644); err != nil {
		t.Fatalf("writing %s: %v", rel, err)
	}
}

// scanFiles runs scan over root and returns the path of every hit, in walk
// order.
func scanFiles(t *testing.T, root string) []string {
	t.Helper()

	hits, err := scan(root)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	files := make([]string, 0, len(hits))
	for _, h := range hits {
		files = append(files, h.file)
	}
	return files
}

// rootWith builds a temp tree carrying a banned literal at each rel and
// returns its root.
func rootWith(t *testing.T, rels ...string) string {
	t.Helper()

	root := t.TempDir()
	literal := bannedLiteral(t, fixturePattern)
	for _, rel := range rels {
		writeFixture(t, root, rel, literal)
	}
	return root
}

// TestAllowlistIsExact pins the allowlist to its two surviving entries. The
// retired ptyrunner path was a standing exemption for a file #1348 deleted, so
// this reddens if it returns — and equally if a third entry is added, since
// widening the exemption surface is the tempting escape from the trap the
// second assertion states.
func TestAllowlistIsExact(t *testing.T) {
	t.Parallel()

	want := []string{
		"internal/e2e/internal/fakeclaude/main.go",
		"cmd/substrate-guard/main.go",
	}
	if len(allowlist) != len(want) {
		t.Fatalf("allowlist = %q, want exactly %q", allowlist, want)
	}
	for i := range want {
		if allowlist[i] != want[i] {
			t.Errorf("allowlist[%d] = %q, want %q", i, allowlist[i], want[i])
		}
	}
	// The trap this file works around, stated as a fact rather than a comment:
	// the guard scans its own test source like any other file.
	if isAllowlisted("cmd/substrate-guard/main_test.go") {
		t.Error("this test file is allowlisted; that widens the exemption surface instead of narrowing it")
	}
}

// TestScan_DoesNotDescendIntoDotClaude is the hermetic form of the criterion
// that the guard scans this tree and not the agent working copies parked
// beside it. The operator clone's .claude/worktrees/ holds a checkout of a
// branch predating the ptyrunner deletion, so before this change the walk
// found that copy's helper and only the stale allowlist entry hid it. Each row
// gets its own tree and expects nothing at all.
func TestScan_DoesNotDescendIntoDotClaude(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		rel  string
	}{
		{
			// The operator-machine failure, reconstructed exactly.
			"the worktree copy of the deleted helper",
			".claude/worktrees/focused-goldstine-41bfd6/internal/agentrun/ptyrunner/helper_test.go",
		},
		{
			// Matches no allowlist suffix, so only the skip can suppress it.
			"a worktree copy no exemption could ever cover",
			".claude/worktrees/other/internal/relay/client.go",
		},
		{
			// The skip keys on the directory name, not on the repo root.
			"a nested .claude anywhere in the tree",
			"internal/tooling/.claude/x.go",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := scanFiles(t, rootWith(t, tt.rel)); len(got) != 0 {
				t.Errorf("scan reported %q; nothing under .claude/ is this tree's source", got)
			}
		})
	}
}

// TestScan_ReportsTheRetiredPtyrunnerPath tells retirement from relocation. A
// clean run cannot: it looks the same whether the exemption is gone or has
// merely stopped being reachable. A banned literal at the deleted helper's own
// path, in the main tree, must be reported like any other file.
func TestScan_ReportsTheRetiredPtyrunnerPath(t *testing.T) {
	t.Parallel()

	const rel = "internal/agentrun/ptyrunner/helper_test.go"

	hits, err := scan(rootWith(t, rel))
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("got %d hits, want exactly 1 at %s — the exemption is still live", len(hits), rel)
	}
	if hits[0].file != rel {
		t.Errorf("hit file = %q, want %q", hits[0].file, rel)
	}
	if hits[0].pat.name != fixturePattern {
		t.Errorf("hit pattern = %q, want %q", hits[0].pat.name, fixturePattern)
	}
}

// TestScan_ReportsBannedLiteralsAcrossTheTree proves skipping .claude narrowed
// nothing else: the ordinary source tree is still scanned everywhere it was.
func TestScan_ReportsBannedLiteralsAcrossTheTree(t *testing.T) {
	t.Parallel()

	for _, rel := range []string{
		"main.go",
		"cmd/pyry/main.go",
		"internal/relay/client.go",
		"internal/agentrun/ptyrunner/helper_test.go",
	} {
		t.Run(rel, func(t *testing.T) {
			t.Parallel()

			got := scanFiles(t, rootWith(t, rel))
			if len(got) != 1 || got[0] != rel {
				t.Errorf("scan reported %q, want exactly [%q]", got, rel)
			}
		})
	}
}

// TestScan_HonoursTheSurvivingAllowlist fences the two entries that stay. Both
// sit directly under the doc block this ticket rewrites, which is exactly the
// edit that could drop one by accident.
func TestScan_HonoursTheSurvivingAllowlist(t *testing.T) {
	t.Parallel()

	root := rootWith(t,
		"internal/e2e/internal/fakeclaude/main.go",
		"cmd/substrate-guard/main.go",
	)
	if got := scanFiles(t, root); len(got) != 0 {
		t.Errorf("scan reported %q; both files are allowlisted and must stay exempt", got)
	}
}

// TestScan_StillSkipsTheOtherDirectories is the regression fence around the
// skip case the .claude entry was appended to.
func TestScan_StillSkipsTheOtherDirectories(t *testing.T) {
	t.Parallel()

	for _, rel := range []string{
		".git/x.go",
		"vendor/v/x.go",
		"node_modules/n/x.go",
		"dist/d/x.go",
	} {
		t.Run(rel, func(t *testing.T) {
			t.Parallel()

			if got := scanFiles(t, rootWith(t, rel)); len(got) != 0 {
				t.Errorf("scan reported %q; that directory is not descended into", got)
			}
		})
	}
}

// TestScan_IgnoresNonGoFiles keeps the scan keyed on the .go extension. The
// bodies here are identical to the ones the rows above report.
func TestScan_IgnoresNonGoFiles(t *testing.T) {
	t.Parallel()

	root := rootWith(t, "docs/notes.md", "internal/relay/fixture.txt")
	if got := scanFiles(t, root); len(got) != 0 {
		t.Errorf("scan reported %q; only .go files are scanned", got)
	}
}
