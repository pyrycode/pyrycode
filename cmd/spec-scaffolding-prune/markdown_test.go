package main

import (
	"fmt"
	"strings"
	"testing"
)

func testMarkdownPreviewApply(t *testing.T, body, want string, start, end int) {
	t.Helper()
	root, path := testTree(t, "707-spec.md", body)
	preview := testRun(t, root, testEvidence(), nil, false)
	if testRead(t, root, path) != body {
		t.Fatal("preview changed bytes")
	}
	count := 0
	if start >= 0 {
		count = 1
	}
	sections := preview.Documents[0].Sections
	if preview.EligibleDocuments != 1 || preview.ProposedRemovalSections != count || len(sections) != count {
		t.Fatalf("incorrect preview: %+v", preview)
	}
	if count == 1 && (sections[0].Start != start || sections[0].End != end || sections[0].Action != "remove") {
		t.Fatalf("incorrect removal bounds: %+v", sections)
	}
	for i := 0; i < 2; i++ {
		applied := testRun(t, root, testEvidence(), nil, true)
		if got := testRead(t, root, path); got != want {
			t.Fatalf("apply %d: got %q want %q", i+1, got, want)
		}
		if i == 1 && applied.ProposedRemovalSections != 0 {
			t.Fatal("repeated apply proposed further removals")
		}
	}
}

func TestCommonMarkStructuralWhitespace(t *testing.T) {
	t.Parallel()
	// Every TrimSpace whitespace character other than Markdown's spaces,
	// tabs and line endings is content in a structural blank-line check.
	for _, r := range "\v\f\u0085\u00a0\u1680\u2000\u2001\u2002\u2003\u2004\u2005\u2006\u2007\u2008\u2009\u200a\u2028\u2029\u202f\u205f\u3000" {
		t.Run(fmt.Sprintf("U+%04X", r), func(t *testing.T) {
			ws := string(r)
			for _, newline := range []string{"\n", "\r\n"} {
				before := "preamble\n"
				reading := "## Files to read first\nread\n\n"
				for _, tc := range []struct{ name, remove, keep string }{
					{"Setext continuation", reading, "Design\n" + ws + "\n---\nDurable design bytes"},
					{"nonempty bullet", reading + "Design\n- " + ws + "\n---\nlist bytes\n", "## Design\nkeep\n"},
					{"nonempty ordered item", reading + "Design\n1. " + ws + "\n---\nlist bytes\n", "## Design\nkeep\n"},
					{"trimmed title", "## " + ws + "FILES TO READ FIRST" + ws + "\nread\n", "## Design\nkeep"},
					{"trimmed resolution", "## Open questions\n" + ws + "None." + ws + "\n", "## Design\nkeep"},
				} {
					t.Run(tc.name+fmt.Sprintf("/%q", newline), func(t *testing.T) {
						prefix := strings.ReplaceAll(before+tc.remove, "\n", newline)
						kept := strings.ReplaceAll(tc.keep, "\n", newline)
						preamble := strings.ReplaceAll(before, "\n", newline)
						testMarkdownPreviewApply(t, prefix+kept, preamble+kept, len(preamble), len(prefix))
					})
				}
				for _, fence := range []string{"```", "~~~"} {
					t.Run(fence+fmt.Sprintf("/%q", newline), func(t *testing.T) {
						body := strings.ReplaceAll(fence+"\n"+fence+ws+"\n## Files to read first\ncode example\n"+fence+"\n## Design\nkeep\n", "\n", newline)
						testMarkdownPreviewApply(t, body, body, -1, -1)
					})
				}
			}
		})
	}
}

func TestCommonMarkWhitespaceAmbiguity(t *testing.T) {
	t.Parallel()
	for _, ws := range []string{"\v", "\f", "\u00a0", "\u2003", "\u2028", "\u3000"} {
		for _, construct := range []string{
			"  ```\n" + ws + "\n  ```\n",
			"  ~~~\n" + ws + "\n  ~~~\n",
			"> quote\n>     " + ws + "\nOpen questions\n---\nACP deferred.\n",
			"- item\n-     " + ws + "\nOpen questions\n---\nACP deferred.\n",
		} {
			for _, newline := range []string{"\n", "\r\n"} {
				t.Run(fmt.Sprintf("%q/%q/%q", ws, construct, newline), func(t *testing.T) {
					body := strings.ReplaceAll("# Files to read first\nread\n"+construct+"# Design\nkeep\n", "\n", newline)
					root, path := testTree(t, "707-spec.md", body)
					for _, apply := range []bool{false, true, true} {
						got := testRun(t, root, testEvidence(), nil, apply)
						if !strings.HasPrefix(got.Documents[0].Reason, "unsupported Markdown: ") || got.ProposedRemovalSections != 0 || testRead(t, root, path) != body {
							t.Fatalf("ambiguous Unicode content not retained: %+v", got)
						}
					}
				})
			}
		}
	}
}

func TestCommonMarkLineEndings(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, before, remove, keep string }{
		{"mixed LF and CR", "", "## Files to read first\nread\r", "## Design\rDurable design bytes\n"},
		{"CR only", "preamble\r", "## Files to read first\rread\r", "## Design\rDurable design bytes\r"},
		{"CR only no final newline", "preamble\r", "## Files to read first\rread\r", "## Design\rDurable design bytes"},
		{"mixed Setext", "intro\r\n\r", "Files to read first\r---\nread\r\n\r", "Design\rcontinued\n---\r\nkeep\r"},
		{"mixed frontmatter and questions", "---\rticket: 707\r\n---\n", "## Open questions\rNone.\r", "## Design\r\nkeep"},
		{"mixed backtick fence", "```\r## Files to read first\r\n```\n", "## Files to read first\rread\n", "## Design\r\nkeep\r"},
		{"CR tilde fence", "~~~\r## Files to read first\r~~~\r", "## Files to read first\rread\r", "## Design\rkeep"},
		{"consecutive endings", "\r\r\n\n", "## Files to read first\r\nread\r\r", "## Design\nkeep\r\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			testMarkdownPreviewApply(t, tc.before+tc.remove+tc.keep, tc.before+tc.keep, len(tc.before), len(tc.before+tc.remove))
		})
	}
}
