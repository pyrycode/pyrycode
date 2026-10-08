package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testTree(t *testing.T, name, body string) (string, string) {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, specsDir)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	path := specsDir + "/" + name
	if err := os.WriteFile(filepath.Join(root, path), []byte(body), 0640); err != nil {
		t.Fatal(err)
	}
	return root, path
}
func testEvidence() snapshot {
	return snapshot{Repository: repository, RetrievedAt: "2026-10-08T11:00:00Z", Provenance: "GitHub GraphQL issues + pullRequests.closingIssuesReferences", Issues: []issue{{Number: 707, State: "CLOSED", PRs: []merge{{Number: 900, Repository: repository, MergedAt: "2026-09-01T00:00:00Z", Association: "closingIssuesReferences"}}}}}
}
func testRun(t *testing.T, root string, ev snapshot, approvals []approval, apply bool) report {
	t.Helper()
	var out bytes.Buffer
	if err := run(root, ev, approvals, apply, &out); err != nil {
		t.Fatal(err)
	}
	var result report
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	return result
}
func testRead(t *testing.T, root, path string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, path))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestEligibilityAndIdentity(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, body string
		modify     func(*snapshot)
		eligible   bool
	}{
		{"707-ok.md", "## Files to read first\nx\n", nil, true},
		{"707-ok.md", "---\nticket: 707\n---\n## Files to read first\nx\n", nil, true},
		{"front.md", "---\nticket: 707\n---\n## Files to read first\nx\n", nil, true},
		{"707-conflict.md", "---\nticket: 708\n---\n## Files to read first\nx\n", nil, false},
		{"707-bad.md", "---\nticket: nope\n---\n## Files to read first\nx\n", nil, false},
		{"707-duplicate.md", "---\nticket: 707\nticket: 707\n---\n", nil, false},
		{"0-invalid.md", "## Files to read first\nx\n", nil, false},
		{"missing.md", "## Files to read first\nx\n", nil, false},
		{"707-open.md", "## Files to read first\nx\n", func(e *snapshot) { e.Issues[0].State = "OPEN" }, false},
		{"707-unmerged.md", "## Files to read first\nx\n", func(e *snapshot) { e.Issues[0].PRs = nil }, false},
		{"707-mention.md", "## Files to read first\nx\n", func(e *snapshot) { e.Issues[0].PRs[0].Association = "mention" }, false},
		{"707-foreign.md", "## Files to read first\nx\n", func(e *snapshot) { e.Issues[0].PRs[0].Repository = "other/repo" }, false},
		{"707-time.md", "## Files to read first\nx\n", func(e *snapshot) { e.Issues[0].PRs[0].MergedAt = "" }, false},
		{"707-future.md", "## Files to read first\nx\n", func(e *snapshot) { e.Issues[0].PRs[0].MergedAt = "2027-01-01T00:00:00Z" }, false},
		{"707-duplicate.md", "## Files to read first\nx\n", func(e *snapshot) { e.Issues = append(e.Issues, e.Issues[0]) }, false},
		{"708-absent.md", "## Files to read first\nx\n", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, path := testTree(t, tc.name, tc.body)
			ev := testEvidence()
			if tc.modify != nil {
				tc.modify(&ev)
			}
			got := testRun(t, root, ev, nil, true)
			if (got.EligibleDocuments == 1) != tc.eligible {
				t.Fatalf("eligibility: %+v", got.Documents)
			}
			if !tc.eligible && testRead(t, root, path) != tc.body {
				t.Fatal("ineligible document changed")
			}
			if got.Documents[0].Reason == "" {
				t.Fatal("missing reason")
			}
		})
	}
}

func TestMarkdownPreservation(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, in, want string }{
		{"ATX", "preamble\r\n## FILES TO READ FIRST ###\r\nx\r\n### child\r\ny\r\n## Design\r\nkeep", "preamble\r\n## Design\r\nkeep"},
		{"Setext", "intro\n\nFiles to read first\n-------------------\nx\n\nDesign\n======\nkeep\n", "intro\n\nDesign\n======\nkeep\n"},
		{"fences", "```md\n## Files to read first\n```\n~~~\nOpen questions\n---\n~~~\n## Design\nkeep\n", "```md\n## Files to read first\n```\n~~~\nOpen questions\n---\n~~~\n## Design\nkeep\n"},
		{"fenced boundary", "## Files to read first\n```\n## Design\n```\n~~~\n# fake\n~~~\n## Real\nkeep\n", "## Real\nkeep\n"},
		{"frontmatter", "---\nticket: 707\n## Files to read first\n---\n## Files to read first\nx\n## Keep\ny\n", "---\nticket: 707\n## Files to read first\n---\n## Keep\ny\n"},
		{"similar", "## 1. Files to read first\nx\n## Files to read first (old)\ny\n## Open questions resolved\nz\n", "## 1. Files to read first\nx\n## Files to read first (old)\ny\n## Open questions resolved\nz\n"},
		{"tab code", "\tOpen questions\n---\nkeep", "\tOpen questions\n---\nkeep"},
		{"EOF", "keep\n## Files to read first\nx", "keep\n"},
		{"multiline boundary", "## Files to read first\nx\n\nMulti\nline title\n---\nkeep", "Multi\nline title\n---\nkeep"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, path := testTree(t, "707-spec.md", tc.in)
			testRun(t, root, testEvidence(), nil, true)
			if got := testRead(t, root, path); got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
}

func TestQuestionApprovalsAndPreview(t *testing.T) {
	t.Parallel()
	body := "## Open questions\nNone.\n## Open questions\n- Multi-phone and ACP deferred.\n## Open questions\nNone blocking. Still need judgment.\n"
	root, path := testTree(t, "707-spec.md", body)
	preview := testRun(t, root, testEvidence(), nil, false)
	if testRead(t, root, path) != body || preview.ProposedRemovalSections != 1 || preview.DeferredQuestionSections != 2 {
		t.Fatalf("bad preview: %+v", preview)
	}
	approved := approval{Document: path, SHA256: hash([]byte(body)), Heading: 2}
	for _, tc := range []struct {
		name      string
		approvals []approval
		removals  int
	}{
		{"one", []approval{approved}, 2}, {"duplicate", []approval{approved, approved}, 1},
		{"stale", []approval{{Document: path, SHA256: "stale", Heading: 2}}, 1},
		{"unmatched", []approval{{Document: path, SHA256: hash([]byte(body)), Heading: 99}}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := testRun(t, root, testEvidence(), tc.approvals, false)
			if got.ProposedRemovalSections != tc.removals {
				t.Fatalf("%+v", got)
			}
			if tc.name != "one" && len(got.ApprovalDiagnostics) == 0 {
				t.Fatal("no approval diagnostic")
			}
		})
	}
	ev := testEvidence()
	ev.Issues[0].State = "OPEN"
	if got := testRun(t, root, ev, []approval{approved}, false); got.ProposedRemovalSections != 0 {
		t.Fatal("approval bypassed eligibility")
	}
	testRun(t, root, testEvidence(), []approval{approved}, true)
	want := "## Open questions\nNone blocking. Still need judgment.\n"
	if got := testRead(t, root, path); got != want {
		t.Fatalf("got %q", got)
	}
	if got := testRun(t, root, testEvidence(), nil, true); got.ProposedRemovalSections != 0 {
		t.Fatal("not idempotent")
	}
	info, err := os.Stat(filepath.Join(root, path))
	if err != nil || info.Mode().Perm() != 0640 {
		t.Fatal("permissions changed")
	}
}
func TestResolutionLiterals(t *testing.T) {
	t.Parallel()
	for _, body := range []string{"", "None", "NONE.", "No open questions.", "No outstanding questions.", "None.\nprose", "None blocking", "- None"} {
		root, _ := testTree(t, "707-spec.md", "## Open questions\n"+body)
		got := testRun(t, root, testEvidence(), nil, false)
		want := 0
		if body == "" || body == "None" || body == "NONE." || strings.HasPrefix(body, "No ") {
			want = 1
		}
		if got.ProposedRemovalSections != want {
			t.Fatalf("body %q: %+v", body, got)
		}
	}
}

type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, errors.New("output failed") }
func TestFailuresPreventWrites(t *testing.T) {
	root, path := testTree(t, "707-spec.md", "## Files to read first\nx\n")
	if err := run(root, testEvidence(), nil, true, failWriter{}); err == nil {
		t.Fatal("output failure ignored")
	}
	if testRead(t, root, path) != "## Files to read first\nx\n" {
		t.Fatal("write before preview")
	}
	if err := run(t.TempDir(), testEvidence(), nil, false, io.Discard); err == nil {
		t.Fatal("discovery error ignored")
	}
	if err := os.Symlink(filepath.Join(root, path), filepath.Join(root, specsDir, "708-link.md")); err != nil {
		t.Fatal(err)
	}
	if err := run(root, testEvidence(), nil, true, io.Discard); err == nil {
		t.Fatal("symlink accepted")
	}
}

func TestInputAndChangedDocumentFailures(t *testing.T) {
	root, path := testTree(t, "707-spec.md", "## Files to read first\nx\n")
	for _, input := range []string{`{`, `{} {}`, `{"unknown":1}`} {
		file := filepath.Join(t.TempDir(), "input.json")
		if err := os.WriteFile(file, []byte(input), 0600); err != nil {
			t.Fatal(err)
		}
		var s snapshot
		if err := decode(file, &s); err == nil {
			t.Fatalf("accepted %s", input)
		}
	}
	for _, field := range []string{"repository", "retrieval time"} {
		s := testEvidence()
		if field == "repository" {
			s.Repository = "other/repo"
		} else {
			s.RetrievedAt = "bad"
		}
		if err := run(root, s, nil, true, io.Discard); err == nil {
			t.Fatal("invalid envelope accepted")
		}
	}
	if err := replace(filepath.Join(root, path), []byte("stale"), nil); err == nil {
		t.Fatal("stale bytes overwritten")
	}
}
func TestNestedQuestionsAndDiscovery(t *testing.T) {
	body := "## Files to read first\nlist\n### Open questions\nACP deferred.\n## Keep\ntext\n"
	root, path := testTree(t, "707-spec.md", body)
	if err := os.Mkdir(filepath.Join(root, specsDir, "nested.md"), 0755); err != nil {
		t.Fatal(err)
	}
	got := testRun(t, root, testEvidence(), nil, true)
	if got.ScannedDocuments != 1 || got.ProposedRemovalSections != 0 || got.DeferredQuestionSections != 1 || testRead(t, root, path) != body {
		t.Fatalf("%+v", got)
	}
}

func TestSetextQuestionBlockBoundaries(t *testing.T) {
	t.Parallel()
	for _, block := range []string{
		"***\n", "---\n", " _ _ _\n", "- - -\n", ">\n", "-\n", "*\n", "+\n", "1.\n", "2)\n",
		"[ref]: /url\n", "<!-- comment -->\n", "<?instruction?>\n", "<!DOCTYPE html>\n", "<![CDATA[text]]>\n",
		"<script>text</script>\n", "<div>\ntext\n</div>\n\n", "<custom>\ntext\n</custom>\n\n",
		"    code\n", "\tcode\n", "```\ntext\n```\n", "~~~\ntext\n~~~\n",
	} {
		t.Run(block, func(t *testing.T) {
			for _, newline := range []string{"\n", "\r\n"} {
				body := strings.ReplaceAll("# Files to read first\nread\n\n"+block+"Open questions\n---\nMulti-phone and ACP deferred.\n# Design\nkeep", "\n", newline)
				root, path := testTree(t, "707-spec.md", body)
				preview := testRun(t, root, testEvidence(), nil, false)
				sections := preview.Documents[0].Sections
				start := strings.Index(body, "Open questions")
				end := strings.Index(body, "# Design")
				if len(sections) != 2 || sections[1].Heading != 2 || sections[1].Title != "Open questions" || sections[1].Start != start || sections[1].End != end || preview.DeferredQuestionSections != 1 || preview.ProposedRemovalSections != 0 {
					t.Fatalf("incorrect Setext recognition/overlap protection: %+v", preview)
				}
				if testRead(t, root, path) != body {
					t.Fatal("preview changed document")
				}
				testRun(t, root, testEvidence(), nil, true)
				if testRead(t, root, path) != body {
					t.Fatal("apply removed deferred questions or neighboring bytes")
				}
				approved := approval{Document: path, SHA256: hash([]byte(body)), Heading: 2}
				testRun(t, root, testEvidence(), []approval{approved}, true)
				if testRead(t, root, path) != body[end:] {
					t.Fatal("approved removal changed neighboring bytes")
				}
			}
		})
	}
}

func TestNonHeadingQuestionText(t *testing.T) {
	t.Parallel()
	for _, prefix := range []string{
		"Multi-line\n", "> quoted\n", "- item\n", "1. item\n",
		"<div>\n", "<!--\n", "<script>\n", "text\n<custom>\n", "text\n[ref]: /url\n",
	} {
		t.Run(prefix, func(t *testing.T) {
			body := prefix + "Open questions\n---\nNone."
			root, path := testTree(t, "707-spec.md", body)
			got := testRun(t, root, testEvidence(), nil, true)
			if got.ProposedRemovalSections != 0 || testRead(t, root, path) != body {
				t.Fatalf("non-heading text was removed: %+v", got)
			}
		})
	}
}

func TestFrontmatterTicketWhitespace(t *testing.T) {
	t.Parallel()
	for _, key := range []string{"  ticket:", "ticket :", "\tticket\t:"} {
		for _, tc := range []struct{ value, reason string }{
			{"708", "conflicting ticket identities"}, {"nope", "malformed frontmatter ticket"},
			{"", "malformed frontmatter ticket"}, {"+707", "malformed frontmatter ticket"},
			{"707\nticket: 707", "malformed frontmatter ticket"}, {"707\n" + key + " 707", "malformed frontmatter ticket"},
		} {
			t.Run(key+tc.value, func(t *testing.T) {
				body := "---\n" + key + " " + tc.value + "\n---\n## Files to read first\nread\n"
				root, path := testTree(t, "707-spec.md", body)
				for _, apply := range []bool{false, true} {
					got := testRun(t, root, testEvidence(), nil, apply)
					if got.EligibleDocuments != 0 || got.ProposedRemovalSections != 0 || got.Documents[0].Reason != tc.reason || testRead(t, root, path) != body {
						t.Fatalf("unsafe ticket fallback: %+v", got)
					}
				}
			})
		}
		body := "---\n" + key + " 707\n---\n## Files to read first\nread\n## Design\nkeep"
		root, path := testTree(t, "707-spec.md", body)
		if got := testRun(t, root, testEvidence(), nil, true); got.EligibleDocuments != 1 || testRead(t, root, path) != "---\n"+key+" 707\n---\n## Design\nkeep" {
			t.Fatalf("agreement not recognized: %+v", got)
		}
	}
}

func TestCrossIssuePRIdentity(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, timestamp, repo string }{
		{"shared PR", "2026-09-01T00:00:00Z", repository},
		{"equivalent timestamp", "2026-09-01T01:00:00+01:00", repository},
		{"conflicting time", "2026-09-02T00:00:00Z", repository},
		{"malformed time", "invalid", repository},
		{"conflicting repository", "2026-09-01T00:00:00Z", "other/repo"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := "## Files to read first\nread\n## Design\nkeep\n"
			root, path := testTree(t, "707-spec.md", body)
			other := specsDir + "/708-spec.md"
			if err := os.WriteFile(filepath.Join(root, other), []byte(body), 0640); err != nil {
				t.Fatal(err)
			}
			ev := testEvidence()
			pr := ev.Issues[0].PRs[0]
			pr.MergedAt, pr.Repository = tc.timestamp, tc.repo
			ev.Issues = append(ev.Issues, issue{Number: 708, State: "CLOSED", PRs: []merge{pr}})
			valid := tc.name == "shared PR" || tc.name == "equivalent timestamp"
			for _, apply := range []bool{false, true} {
				got := testRun(t, root, ev, nil, apply)
				if (got.EligibleDocuments == 2 && got.ProposedRemovalSections == 2) != valid {
					t.Fatalf("incorrect shared PR eligibility: %+v", got)
				}
				for _, doc := range got.Documents {
					want := body
					if valid && apply {
						want = "## Design\nkeep\n"
					}
					if testRead(t, root, doc.Document) != want || (!valid && !strings.Contains(doc.Reason, "contradictory")) {
						t.Fatalf("incorrect shared PR write/reason: %+v (first %s)", doc, path)
					}
				}
			}
		})
	}
}

func TestUnsupportedMarkdownStaysUnchanged(t *testing.T) {
	t.Parallel()
	question := "Open questions\n---\nMulti-phone and ACP deferred.\n# Design\nkeep\n"
	for _, tc := range []struct{ name, body string }{
		{"multiline reference destination", "# Files to read first\nread\n\n[ref]:\n  /url\n" + question},
		{"next-line reference title", "# Files to read first\nread\n\n[ref]: /url\n  \"title\"\n" + question},
		{"invalid reference definition", "[ref]: invalid destination\nFiles to read first\n---\nDesign bytes\n"},
		{"multiline reference label", "# Files to read first\nread\n\n[a\nb]: /u\n" + question},
		{"reference then HTML", "# Files to read first\nread\n\n[ref]:\n  /url\n<span>\n# Design\nkeep\n"},
		{"tilde list fence", "- ~~~\n  ## Files to read first\n  code example\n  ~~~\n\n## Design\nkeep\n"},
		{"backtick list fence", "- ```\n  ## Files to read first\n  code example\n  ```\n\n## Design\nkeep\n"},
		{"nested list fence", "## Files to read first\nread\n- a\n  - ~~~\n    ## Design\n    ~~~\n\n## Keep\nkeep\n"},
		{"block quote fence", "## Files to read first\nread\n> ```\n> ## Design\n> ```\n\n## Keep\nkeep\n"},
		{"deep list fence", "## Files to read first\n- a\n\n     ```\n     ## Design\n     ```\n## Keep\nkeep\n"},
		{"fence closed by its container", "## Files to read first\n- a\n\n  ```\n## Design\n  ```\nkeep\n"},
		{"tab fence in list", "## Files to read first\n1. one\n\t```\n## Design\nkeep\n"},
		{"list item heading", "## Files to read first\n- a\n\n  ## Design\nkeep\n"},
		{"block quote heading", "## Files to read first\n> ## Design\nkeep\n"},
		{"block quote Setext", "## Files to read first\n> Design\n> ---\nkeep\n"},
		{"list Setext", "## Files to read first\n- Design\n  ---\nkeep\n"},
		{"lazy line after indented list content", "## Files to read first\n- a\n\n    more\nOpen questions\n---\nACP deferred.\n## Keep\nkeep\n"},
		{"HTML after indented list content", "## Files to read first\n- a\n\n    more\n<span>\n## Design\nkeep\n"},
		{"block quote HTML", "## Files to read first\n> <span>\n> - item\nDesign\n-\nkeep\n"},
		{"block quote code continuation", "## Files to read first\n> quote\n>     code\n<span>\n## Design\nkeep\n"},
		{"tab after block quote", "## Files to read first\n>\tquote\nOpen questions\n---\nACP deferred.\n## Keep\nkeep\n"},
		{"indented HTML block", "## Files to read first\n- a\n\n  <!--\n## Design\n-->\nkeep\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, newline := range []string{"\n", "\r\n"} {
				body := strings.ReplaceAll(tc.body, "\n", newline)
				root, path := testTree(t, "707-spec.md", body)
				preview := testRun(t, root, testEvidence(), nil, false)
				doc := preview.Documents[0]
				if !strings.HasPrefix(doc.Reason, "unsupported Markdown: ") || preview.EligibleDocuments != 0 || preview.ProposedRemovalSections != 0 {
					t.Fatalf("ambiguous boundaries not retained and reported: %+v", preview)
				}
				for _, sec := range doc.Sections {
					if sec.Action != "retain" || sec.Reason != doc.Reason {
						t.Fatalf("section not retained with document reason: %+v", sec)
					}
				}
				testRun(t, root, testEvidence(), nil, true)
				if testRead(t, root, path) != body {
					t.Fatal("apply changed a document with unsupported Markdown")
				}
			}
		})
	}
}

func TestCommonMarkParagraphBoundaries(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, in, kept string }{
		{"space continuation", "## Files to read first\nread\n\n", "Design\n    continued\n---\nDurable design bytes\n"},
		{"tab continuation", "## Files to read first\nread\n\n", "Design\n\tcontinued\n---\nDurable design bytes\n"},
		{"ordered item that cannot interrupt", "## Files to read first\nx\n\n", "Design\n2. step\n---\nkeep\n"},
		{"empty item that cannot interrupt", "## Files to read first\nx\n\n", "Design\n*\n---\nkeep\n"},
		{"lazy HTML-like text", "## Files to read first\n- item\n<span>\n", "## Design\nkeep\n"},
		{"single-line reference", "## Files to read first\n[ref]: <a b> 'T'\n", "Design\n---\nkeep\n"},
		{"fully indented list fence", "## Files to read first\n- a\n\n  ```\n  ## not a heading\n  ```\n", "## Design\nkeep\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, newline := range []string{"\n", "\r\n"} {
				in := strings.ReplaceAll(tc.in, "\n", newline)
				kept := strings.ReplaceAll(tc.kept, "\n", newline)
				root, path := testTree(t, "707-spec.md", in+kept)
				preview := testRun(t, root, testEvidence(), nil, false)
				sections := preview.Documents[0].Sections
				if preview.Documents[0].Reason != "eligible" || len(sections) != 1 || sections[0].Start != 0 || sections[0].End != len(in) || sections[0].Action != "remove" {
					t.Fatalf("wrong removal bounds: %+v", preview)
				}
				testRun(t, root, testEvidence(), nil, true)
				if got := testRead(t, root, path); got != kept {
					t.Fatalf("got %q want %q", got, kept)
				}
				testRun(t, root, testEvidence(), nil, true)
				if got := testRead(t, root, path); got != kept {
					t.Fatal("repeated apply changed the document")
				}
			}
		})
	}
}
