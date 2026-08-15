// Tests for the citation guard.
//
// The package had none until 2026-08-14, and its model cmd/substrate-guard
// still has none, so this establishes the pattern rather than following one.
//
// Every case here is a regression that already cost real time, not a sweep for
// coverage. The pattern test is the important one: this guard reports a clean
// tree and a broken pattern identically, with an exit code of zero, so a silent
// regression in citeRe corrupts every message it prints instead of failing.
// That has already happened twice, once when an unclean tree emptied the diff
// scope (2026-08-13), and once when ranges turned out to be outside the pattern
// entirely (2026-08-14). Neither was caught by running the guard.
package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestCiteRe_CapturesTheWholeRange pins that a citation is never captured as a
// prefix of itself. A cleanup script once matched ":93" out of ":934-944" and
// rewrote the wrong reference, which is the failure this protects against.
//
// Note what does the protecting, because the first version of this test got it
// wrong. Greedy matching takes the whole range on the first attempt, so the
// trailing character class is NOT what holds this property up: dropping the
// class leaves every case here passing. That was found by mutating the pattern
// and watching this test stay green, which is why the malformed-citation test
// below exists as its own case.
func TestCiteRe_CapturesTheWholeRange(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		comment   string
		wantFile  string
		wantStart string
		wantEnd   string
	}{
		{"range captures both ends whole", "// see runner.go:934-944 for it", "runner.go", "934", "944"},
		{"single line has no end", "// see runner.go:315 for it", "runner.go", "315", ""},
		{"range at end of line", "// see runner.go:934-944", "runner.go", "934", "944"},
		{"single at end of line", "// see runner.go:315", "runner.go", "315", ""},
		{"range in parentheses", "// the fixture rule (runner.go:1139-1141) says", "runner.go", "1139", "1141"},
		{"underscored filename", "// trail_run_outcome_test.go:1027-1031 argues", "trail_run_outcome_test.go", "1027", "1031"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			m := citeRe.FindStringSubmatch(tt.comment)
			if m == nil {
				t.Fatalf("no match in %q", tt.comment)
			}
			if m[1] != tt.wantFile {
				t.Errorf("file: got %q, want %q", m[1], tt.wantFile)
			}
			if m[2] != tt.wantStart {
				t.Errorf("start: got %q, want %q — a short start is the prefix-capture bug", m[2], tt.wantStart)
			}
			if m[3] != tt.wantEnd {
				t.Errorf("end: got %q, want %q", m[3], tt.wantEnd)
			}
		})
	}
}

// TestCiteRe_RejectsMalformedCitations pins the property the trailing character
// class actually provides, once ranges are matched. It is the test that fails
// if someone drops the class as redundant.
//
// Both shapes below are malformed. Without the class the pattern matches a
// PREFIX of each and the guard then quotes a range the comment does not
// contain, which is worse than saying nothing: the message would name a span
// the author never wrote. Refusing a citation it cannot read whole is the same
// posture as refusing an ambiguous basename in index.lookup.
//
// The accepted cost is that a malformed range escapes the ban entirely. That is
// deliberate, and cheap, because the shape is rare and already broken.
func TestCiteRe_RejectsMalformedCitations(t *testing.T) {
	t.Parallel()

	for _, comment := range []string{
		"// see runner.go:934-944-955 odd",
		"// see runner.go:934- dangling",
	} {
		if m := citeRe.FindStringSubmatch(comment); m != nil {
			t.Errorf("matched malformed %q as %q:%q-%q; it should be refused whole, not read as a prefix",
				comment, m[1], m[2], m[3])
		}
	}
}

// TestCiteRe_IgnoresNonCitations keeps the pattern from firing on the shapes it
// deliberately does not own. A bare :NNN inherits the last-named file rather
// than the current one, so resolving it needs comment-context parsing this
// guard does not do, and guessing produced confidently wrong symbols before.
func TestCiteRe_IgnoresNonCitations(t *testing.T) {
	t.Parallel()

	for _, comment := range []string{
		"// see :1027 above",
		"// see :1027-1031 above",
		"// the ratio was 12:30-14:00 on the clock",
		"// no citation here at all",
	} {
		if m := citeRe.FindStringSubmatch(comment); m != nil {
			t.Errorf("matched %q as a citation: %q", comment, m[0])
		}
	}
}

// testSource is the fixture every resolve case reads. Line numbers matter, so
// the helper below reports them rather than making a reader count.
var testSource = []string{
	"package fixture",            // 1
	"",                           // 2
	"// AlphaDoc explains what",  // 3
	"// Alpha does, across two.", // 4
	"func Alpha() {",             // 5
	"\tx := 1",                   // 6
	"\t_ = x",                    // 7
	"}",                          // 8
	"",                           // 9
	"func Beta() {",              // 10
	"\ty := 2",                   // 11
	"\t_ = y",                    // 12
	"}",                          // 13
}

func TestResolve(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		line     int
		wantSym  string
		wantDecl bool
		wantOK   bool
	}{
		{"the declaration line itself", 5, "Alpha", true, true},
		{"inside the declaration", 6, "Alpha", false, true},
		{"a doc comment names the declaration BELOW it", 3, "Alpha", true, true},
		{"second doc comment line, same owner", 4, "Alpha", true, true},
		{"second declaration", 10, "Beta", true, true},
		{"inside the second", 11, "Beta", false, true},
		{"past the end of the file", 99, "", false, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			sym, _, atDecl, ok := resolve(testSource, tt.line)
			if ok != tt.wantOK {
				t.Fatalf("ok: got %v, want %v", ok, tt.wantOK)
			}
			if !ok {
				return
			}
			if sym != tt.wantSym {
				t.Errorf("symbol: got %q, want %q", sym, tt.wantSym)
			}
			if atDecl != tt.wantDecl {
				t.Errorf("atDecl: got %v, want %v", atDecl, tt.wantDecl)
			}
		})
	}
}

// TestResolve_DocCommentOwnershipIsTheTrap states the doc-comment rule as its
// own case because getting it backwards is not hypothetical. A throwaway
// measurement script walked UPWARD from a comment on 2026-08-13, misattributed
// 142 range citations, and produced a false finding that small declarations
// were attracting citations. A doc comment belongs to the declaration BELOW it.
func TestResolve_DocCommentOwnershipIsTheTrap(t *testing.T) {
	t.Parallel()

	sym, _, _, ok := resolve(testSource, 3)
	if !ok {
		t.Fatal("the doc comment did not resolve")
	}
	if sym == "" {
		t.Fatal("resolved to an empty symbol")
	}
	if sym != "Alpha" {
		t.Fatalf("doc comment resolved UPWARD to %q; it belongs to Alpha, the declaration below it", sym)
	}
}

// TestRangeEndsDecideTheMessage covers the branch added on 2026-08-14. Ends
// that agree read like a single-line citation; ends that disagree span
// declarations, where no single symbol replaces the range.
func TestRangeEndsDecideTheMessage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		start     int
		end       int
		wantSame  bool
		wantStart string
		wantEnd   string
	}{
		{"both inside one declaration", 6, 7, true, "Alpha", "Alpha"},
		{"doc comment through its declaration", 3, 5, true, "Alpha", "Alpha"},
		{"spanning two declarations", 6, 11, false, "Alpha", "Beta"},
		{"declaration to declaration", 5, 10, false, "Alpha", "Beta"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			startSym, _, _, okStart := resolve(testSource, tt.start)
			endSym, _, _, okEnd := resolve(testSource, tt.end)
			if !okStart || !okEnd {
				t.Fatalf("both ends must resolve: start ok=%v, end ok=%v", okStart, okEnd)
			}
			if startSym != tt.wantStart || endSym != tt.wantEnd {
				t.Fatalf("symbols: got %q/%q, want %q/%q", startSym, endSym, tt.wantStart, tt.wantEnd)
			}
			if same := startSym == endSym; same != tt.wantSame {
				t.Errorf("ends agree: got %v, want %v", same, tt.wantSame)
			}
		})
	}
}

// TestIndexLookup_RefusesAnAmbiguousBasename is the guard's central safety
// property. A citation names only a basename, and basenames are not unique
// here: main.go exists in every command package. Resolving one by taking the
// first match on disk yields a real symbol from the wrong package, which reads
// as authoritative in a way a wrong number never does.
func TestIndexLookup_RefusesAnAmbiguousBasename(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	for _, dir := range []string{"alpha", "beta"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatalf("creating %s: %v", dir, err)
		}
		body := "package " + dir + "\n\nfunc " + dir + "Only() {}\n"
		if err := os.WriteFile(filepath.Join(root, dir, "shared.go"), []byte(body), 0o644); err != nil {
			t.Fatalf("writing %s: %v", dir, err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "unique.go"), []byte("package root\n\nfunc RootOnly() {}\n"), 0o644); err != nil {
		t.Fatalf("writing unique.go: %v", err)
	}

	ix := newIndex(root)

	// Cited from a file in neither directory, so the citing-file tiebreak
	// cannot apply and the basename is genuinely ambiguous.
	if got := ix.lookup(filepath.Join(root, "unique.go"), "shared.go"); got != nil {
		t.Error("resolved an ambiguous basename; a wrong symbol is worse than no answer")
	}
	// The citing file's own directory wins, which is what makes most real
	// citations resolvable at all.
	if got := ix.lookup(filepath.Join(root, "alpha", "shared.go"), "shared.go"); got == nil {
		t.Error("failed to prefer the citing file's own directory")
	}
	if got := ix.lookup(filepath.Join(root, "unique.go"), "unique.go"); got == nil {
		t.Error("failed to resolve an unambiguous basename")
	}
	if got := ix.lookup(filepath.Join(root, "unique.go"), "absent.go"); got != nil {
		t.Error("resolved a file that does not exist")
	}
}

// TestAllowlistCoversThisTest is a self-check. This file must spell citations
// in quantity, and the scan keys on any line containing a comment marker, so
// without its allowlist entry the guard flags its own test and the gate goes
// red for the wrong reason.
func TestAllowlistCoversThisTest(t *testing.T) {
	t.Parallel()

	if !isAllowlisted("cmd/cite-guard/main_test.go") {
		t.Error("this test file is not allowlisted, so the guard will flag it")
	}
	if !isAllowlisted("cmd/cite-guard/main.go") {
		t.Error("the guard's own source is not allowlisted")
	}
	if isAllowlisted("internal/e2e/realclaude/finding_run_gather_test.go") {
		t.Error("an ordinary file is allowlisted, which would silently narrow the scan")
	}
}
