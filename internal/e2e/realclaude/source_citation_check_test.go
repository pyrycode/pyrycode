//go:build e2e_realclaude

package realclaude

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

type sourceCitationFinding struct {
	Position token.Position
	Text     string
}

// Only the literals in this synthetic fixture declaration are exempt from the
// package scan. Other declarations and every comment in this file are checked.
var sourceCitationFixtures = []struct {
	name string
	text string
	want []string
}{
	{"filename", "see absent.go:123", []string{"absent.go:123"}},
	{"path range", "internal/deleted.go:12-20", []string{"internal/deleted.go:12-20"}},
	{"reversed range", "absent.go:99-2", []string{"absent.go:99-2"}},
	{"zero filename", "absent.go:0", []string{"absent.go:0"}},
	{"bare", ":123", []string{":123"}},
	{"quoted bare", `see ":123"`, []string{":123"}},
	{"single quoted bare", "see ' :123'", []string{":123"}},
	{"parenthesized", "the guard (:123)", []string{":123"}},
	{"bare reversed", "(:99-2)", []string{":99-2"}},
	{"comma bare", "see :123, :456-460", []string{":123, :456-460"}},
	{"comma inherited", "gone.go:123,456, 460-450", []string{"gone.go:123,456, 460-450"}},
	{"at reference", "the arm at :123", []string{":123"}},
	{"exported symbol", "Run:123", []string{"Run:123"}},
	{"lower camel", "trailGate:123-125", []string{"trailGate:123-125"}},
	{"qualified symbol", "ptyrunner.Run:123", []string{"ptyrunner.Run:123"}},
	{"method", "emitter.HandleFor:123", []string{"emitter.HandleFor:123"}},
	{"multiple", "gone.go:1 then (:2) and trailGate:3", []string{"gone.go:1", ":2", "trailGate:3"}},
	{"symbol only", "see trailGate and the guard in emitter.HandleFor", nil},
	{"ordinary numbers", "123 rows, 456-789 bytes; at 0600; issue #123", nil},
	{"time", "2026-08-10T12:34:56Z and 12:34", nil},
	{"wildcard port", ":0", nil},
	{"zero bare reference", "(:0)", []string{":0"}},
	{"ports", "localhost:8080 127.0.0.1:9000 [::1]:8080", nil},
	{"JSON key fragment", `":0`, nil},
	{"JSON value fragment", `":1}`, nil},
	{"quoted wildcard port", `listen on ":0"`, nil},
	{"json", "{m:200} and {\"count\":200,\"Run\":300}", nil},
	{"Go slices", "first.Options[:1] and line[:512]", nil},
	{"bracketed bare", "see [:123]", []string{":123"}},
	{"markdown", "docs/history.md:123-456", nil},
	{"escaped string", "see gone.go\u003a123", []string{"gone.go:123"}},
}

func sourceCitationFixtureSource(text, form string) string {
	switch form {
	case "line comment":
		return "package fixture\n// " + text + "\n"
	case "block comment":
		return "package fixture\n/* " + text + " */\n"
	case "interpreted string":
		return "package fixture\nvar explanation = " + strings.ReplaceAll(strconv.Quote(text), ":", `\u003a`) + "\n"
	default:
		return "package fixture\nvar explanation = `" + text + "`\n"
	}
}

func TestSourceCitationForms(t *testing.T) {
	t.Parallel()
	for _, tc := range sourceCitationFixtures {
		for _, form := range []string{"line comment", "block comment", "interpreted string", "raw string"} {
			t.Run(tc.name+"/"+form, func(t *testing.T) {
				dir := t.TempDir()
				path := filepath.Join(dir, "new.go")
				if err := os.WriteFile(path, []byte(sourceCitationFixtureSource(tc.text, form)), 0600); err != nil {
					t.Fatal(err)
				}
				got, err := scanSourceCitations(dir)
				if err != nil {
					t.Fatal(err)
				}
				if len(got) != len(tc.want) {
					t.Fatalf("findings = %+v, want %q", got, tc.want)
				}
				for i, want := range tc.want {
					if got[i].Text != want {
						t.Errorf("text = %q, want %q", got[i].Text, want)
					}
					if got[i].Position.Filename != path || got[i].Position.Line != 2 || got[i].Position.Column < 1 {
						t.Errorf("position = %v, want %s on second source line", got[i].Position, path)
					}
				}
			})
		}
	}
}

func TestSourceCitationDiscovery(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if _, err := scanSourceCitations(dir); err != nil {
		t.Fatal(err)
	}
	bad := sourceCitationFixtureSource(sourceCitationFixtures[0].text, "raw string")
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte(bad), 0600); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(dir, "nested")
	if err := os.Mkdir(nested, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "nested.go"), []byte(bad), 0600); err != nil {
		t.Fatal(err)
	}
	if got, err := scanSourceCitations(dir); err != nil || len(got) != 0 {
		t.Fatalf("non-Go/nested scan = %v, %v", got, err)
	}
	path := filepath.Join(dir, "newly_discovered.go")
	if err := os.WriteFile(path, []byte(bad), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := scanSourceCitations(dir)
	if err != nil || len(got) != 1 || got[0].Position.Filename != path {
		t.Fatalf("new file scan = %v, %v", got, err)
	}
}

func TestSourceCitationFailures(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"directory", "read", "parse"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "broken.go")
			switch kind {
			case "directory":
				dir = filepath.Join(dir, "missing")
			case "read":
				// A dangling symlink proves a read failure even when run as root.
				if err := os.Symlink(filepath.Join(dir, "missing"), path); err != nil {
					t.Fatal(err)
				}
			case "parse":
				if err := os.WriteFile(path, []byte("package fixture\nvar ="), 0600); err != nil {
					t.Fatal(err)
				}
			}
			_, err := scanSourceCitations(dir)
			if err == nil || !strings.Contains(err.Error(), kind) {
				t.Fatalf("error = %v, want visible %s failure", err, kind)
			}
			if kind != "directory" && !strings.Contains(err.Error(), path) {
				t.Errorf("error lacks source filename: %v", err)
			}
		})
	}
}

func TestSourceCitationFixtureExemption(t *testing.T) {
	t.Parallel()
	bad := sourceCitationFixtures[0].text
	for _, filename := range []string{"source_citation_check_test.go", "other_test.go"} {
		t.Run(filename, func(t *testing.T) {
			dir := t.TempDir()
			src := "package fixture\nvar sourceCitationFixtures = " + strconv.Quote(bad) + "\n" +
				"var unrelated = " + strconv.Quote(bad) + "\n// " + bad + "\n"
			if err := os.WriteFile(filepath.Join(dir, filename), []byte(src), 0600); err != nil {
				t.Fatal(err)
			}
			got, err := scanSourceCitations(dir)
			want := 3
			if filename == "source_citation_check_test.go" {
				want = 2
			}
			if err != nil || len(got) != want {
				t.Fatalf("scan = %+v, %v; want %d findings", got, err, want)
			}
		})
	}
}

func TestSourceCitationPackage(t *testing.T) {
	t.Parallel()
	findings, err := scanSourceCitations(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, finding := range findings {
		t.Errorf("%s: numeric Go-source citation %q; cite the symbol instead", finding.Position, finding.Text)
	}
}

func scanSourceCitations(dir string) ([]sourceCitationFinding, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read source directory %s: %w", dir, err)
	}
	var findings []sourceCitationFinding
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		src, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read source %s: %w", path, err)
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, src, parser.ParseComments|parser.SkipObjectResolution)
		if err != nil {
			return nil, fmt.Errorf("parse source %s: %w", path, err)
		}
		// Positions use the physical citing file, ignoring any line directives.
		check := func(pos token.Pos, text string, comment bool) {
			if comment {
				text = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(text, "//"), "/*"), "*/"))
			}
			if text == ":0" {
				return
			} // net.Listen wildcard-port literal.
			for _, match := range sourceCitationPattern.FindAllStringSubmatchIndex(text, -1) {
				start, end := match[2], match[3]
				if start < 0 {
					start, end = match[4], match[5]
					if sourceCitationSlicePrefix.MatchString(text[:start]) || sourceCitationDataKeyPrefix.MatchString(text[:start]) {
						continue
					}
					// String concatenation can put the end of a JSON key in its own
					// literal. A closing-key fragment has no opening quote in this text.
					if start == 1 && text[0] == '"' && (end == len(text) || text[end] == '}' || text[end] == ',') {
						continue
					}
					// Quoted wildcard listen addresses also appear in explanatory prose.
					if text[start:end] == ":0" && start > 0 && text[start-1] == '"' && end < len(text) && text[end] == '"' {
						continue
					}
				}
				findings = append(findings, sourceCitationFinding{
					Position: fset.PositionFor(pos, false), Text: text[start:end],
				})
			}
		}
		for _, group := range file.Comments {
			for _, comment := range group.List {
				check(comment.Pos(), comment.Text, true)
			}
		}
		var fixtureStart, fixtureEnd token.Pos
		if entry.Name() == "source_citation_check_test.go" {
			for _, decl := range file.Decls {
				gen, ok := decl.(*ast.GenDecl)
				if !ok || gen.Tok != token.VAR {
					continue
				}
				for _, spec := range gen.Specs {
					value, ok := spec.(*ast.ValueSpec)
					if ok && len(value.Names) == 1 && value.Names[0].Name == "sourceCitationFixtures" {
						fixtureStart, fixtureEnd = value.Pos(), value.End()
					}
				}
			}
		}
		var decodeErr error
		ast.Inspect(file, func(node ast.Node) bool {
			literal, ok := node.(*ast.BasicLit)
			if !ok || literal.Kind != token.STRING {
				return true
			}
			if literal.Pos() >= fixtureStart && literal.End() <= fixtureEnd {
				return true
			}
			decoded, err := strconv.Unquote(literal.Value)
			if err != nil {
				decodeErr = fmt.Errorf("decode source string %s: %w", fset.PositionFor(literal.Pos(), false), err)
				return false
			}
			check(literal.Pos(), decoded, false)
			return true
		})
		if decodeErr != nil {
			return nil, decodeErr
		}
	}
	return findings, nil
}

// Named citations need no target lookup. Camel-case identifiers distinguish
// symbol references from ordinary lowercase hosts and data keys. Bare citations
// start at a prose boundary; slice expressions are handled separately.
var sourceCitationPattern = regexp.MustCompile(
	`(?:^|[^A-Za-z0-9_./:])(` +
		`(?:[A-Za-z0-9_./-]+\.go:[0-9]+|` +
		`(?:[A-Za-z_][A-Za-z0-9_]*\.)*(?:[A-Z][A-Za-z0-9_]*|[a-z][a-z0-9_]*[A-Z][A-Za-z0-9_]*):[0-9]+)` +
		`(?:\s*-\s*[0-9]+)?(?:,\s*:?[0-9]+(?:\s*-\s*[0-9]+)?)*` +
		`)|(?:^|[\s(,\[;` + "`" + `"'{])(:[0-9]+(?:\s*-\s*[0-9]+)?(?:,\s*:?[0-9]+(?:\s*-\s*[0-9]+)?)*` +
		`)`,
)

// A colon inside a Go slice expression is data, rather than a standalone cite.
var sourceCitationSlicePrefix = regexp.MustCompile(`[A-Za-z0-9_)\]]\[$`)

// A colon following a complete quoted data key is JSON, not a bare reference.
var sourceCitationDataKeyPrefix = regexp.MustCompile(`(?:"[^"\\]*(?:\\.[^"\\]*)*"|'[^']*')$`)
