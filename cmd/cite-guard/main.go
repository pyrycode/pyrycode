// Command cite-guard fails the build when a Go comment cites another symbol
// by file and line where a symbol name would do.
//
// # Why this exists
//
// Comments used to reference code as `foo_test.go:315`. Nothing declared that
// convention; it propagated by each author copying its neighbours. It is
// expensive, because any insertion displaces an unknown subset of the
// citations and nothing maintains them. Measured over 25 commits touching
// internal/e2e/realclaude before the 2026-08-10 cleanup: 419 of 5509 added
// lines were pure renumbering, the small commits in one ticket family ran
// 35-49% renumbering, and one commit existed for nothing else. Two 25-minute
// developer timeouts (#1417, #1452) burned their budgets on it. 22 citations
// were already dead, pointing at lines that no longer existed.
//
// codegraph indexes this repo, including files behind the e2e_realclaude
// build tag, so a symbol name resolves on demand and never rots.
//
// # The rule, and why it is not "no line numbers ever"
//
// A line number is justified exactly when it points somewhere a symbol name
// cannot reach. So this guard bans only the citations a symbol name fully
// replaces:
//
//  1. the cited line IS a top-level declaration, or a doc comment attached to
//     one -- the symbol identifies it completely; and
//  2. the cited line sits within maxOffset lines of its enclosing declaration
//     -- close enough that "look in this function" lands the reader on the
//     spot, so the number was carrying nothing.
//
// A citation pointing deep inside a long declaration is ALLOWED, because
// there the number is doing real work. maxOffset is 20, about one screen,
// chosen from the measured distribution at cleanup time: median 15 lines
// deep, worst 371.
//
// Deliberately NOT flagged:
//
//   - Bare `:NNN` citations. They inherit the LAST-NAMED FILE in the comment,
//     not the current one, so resolving them needs comment-context parsing
//     that this guard does not do. A cleanup script that assumed self-file
//     produced confidently wrong symbols; trail_run_outcome_test.go documents
//     the same hazard on #1434's evidence.
//   - Line RANGES (`:2063-2107`). A range carries information a symbol name
//     does not.
//   - Anything outside a `//` comment. String literals and code are not this
//     guard's business.
//
// # Why a guard and not just a style rule
//
// This is the second, different-fabric check for a rule the style guide
// cannot hold on its own. A written convention is a soft instruction that
// gets skipped under budget pressure, which is exactly when the agents
// writing these comments are running, and mimicking the surrounding code is
// the default behaviour. A rule in CODING-STYLE.md protecting a rule in
// CODING-STYLE.md shares the same blind spot. Same reasoning as
// cmd/substrate-guard, which this is modelled on.
//
// Run via `make cite-guard`, wired into `make check` and the check.yml PR
// gate. Exits non-zero and prints file:line plus the symbol to use instead.
package main

import (
	"bufio"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// maxOffset is how far into a declaration a cited line may sit before the
// line number is considered to be carrying real information. See the package
// comment for how this number was chosen.
const maxOffset = 20

// allowlist holds path suffixes exempt from the scan. This guard's own source
// is exempt because it must spell the pattern it bans.
var allowlist = []string{
	"cmd/cite-guard/main.go",
}

var (
	// declRe matches a top-level declaration and captures its name.
	declRe = regexp.MustCompile(`^(?:func|type|const|var)\s+(?:\([^)]*\)\s*)?([A-Za-z_][A-Za-z0-9_]*)`)
	// citeRe matches a qualified citation. The trailing character class
	// rejects ranges, and rejecting a following digit stops the regex engine
	// backtracking into a shorter number to satisfy the check -- a cleanup
	// script hit exactly that, matching ":93" out of ":934-944".
	citeRe = regexp.MustCompile(`\b([A-Za-z0-9_]+\.go):([0-9]+)([^0-9\-]|$)`)
)

func isAllowlisted(rel string) bool {
	for _, a := range allowlist {
		if strings.HasSuffix(rel, a) {
			return true
		}
	}
	return false
}

// index maps a bare filename to every path in the repo carrying it. A
// citation names only the basename, and basenames are NOT unique in this repo
// -- `main.go` alone exists in every command package. Resolving one by taking
// the first match on disk produces a confident, wrong symbol, so the lookup
// below prefers the citing file's own directory and otherwise demands the
// basename be unique.
type index struct {
	byName map[string][]string
	cache  map[string][]string
}

func newIndex(root string) *index {
	ix := &index{byName: map[string][]string{}, cache: map[string][]string{}}
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "vendor", "node_modules", "dist", ".claude":
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(path, ".go") {
			n := filepath.Base(path)
			ix.byName[n] = append(ix.byName[n], path)
		}
		return nil
	})
	return ix
}

func (ix *index) read(path string) []string {
	if v, ok := ix.cache[path]; ok {
		return v
	}
	data, err := os.ReadFile(path)
	if err != nil {
		ix.cache[path] = nil
		return nil
	}
	ix.cache[path] = strings.Split(string(data), "\n")
	return ix.cache[path]
}

// lookup resolves a citation's target relative to the citing file. Returns nil
// when the basename is ambiguous, because a wrong answer here is worse than no
// answer: it would name a real symbol from the wrong package.
func (ix *index) lookup(citingFile, target string) []string {
	candidates := ix.byName[target]
	if len(candidates) == 0 {
		return nil
	}
	dir := filepath.Dir(citingFile)
	for _, c := range candidates {
		if filepath.Dir(c) == dir {
			return ix.read(c)
		}
	}
	if len(candidates) == 1 {
		return ix.read(candidates[0])
	}
	return nil
}

// resolve reports the symbol a citation identifies and how deep the cited
// line sits inside it. ok is false when the target cannot be read.
func resolve(lines []string, n int) (sym string, offset int, atDecl bool, ok bool) {
	if n < 1 || n > len(lines) {
		return "", 0, false, false
	}
	if m := declRe.FindStringSubmatch(lines[n-1]); m != nil {
		return m[1], 0, true, true
	}
	// A doc comment attached to a declaration names that declaration.
	if s := strings.TrimSpace(lines[n-1]); strings.HasPrefix(s, "//") || s == "" {
		for j := n - 1; j < len(lines) && j < n+40; j++ {
			if m := declRe.FindStringSubmatch(lines[j]); m != nil {
				return m[1], 0, true, true
			}
			t := strings.TrimSpace(lines[j])
			if t != "" && !strings.HasPrefix(t, "//") {
				break
			}
		}
	}
	// Otherwise walk up to the enclosing declaration.
	for j := n - 1; j >= 0; j-- {
		if m := declRe.FindStringSubmatch(lines[j]); m != nil {
			return m[1], (n - 1) - j, false, true
		}
	}
	return "", 0, false, false
}

func main() {
	root := "."
	if len(os.Args) > 1 {
		root = os.Args[1]
	}
	ix := newIndex(root)
	var hits []string

	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "vendor", "node_modules", "dist", ".claude":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		rel := filepath.ToSlash(path)
		if isAllowlisted(rel) {
			return nil
		}
		f, rerr := os.Open(path)
		if rerr != nil {
			return nil
		}
		defer f.Close()
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 0, 1024*1024), 1024*1024)
		for lineNo := 1; sc.Scan(); lineNo++ {
			idx := strings.Index(sc.Text(), "//")
			if idx < 0 {
				continue
			}
			comment := sc.Text()[idx:]
			for _, m := range citeRe.FindAllStringSubmatch(comment, -1) {
				target, numStr := m[1], m[2]
				n, cerr := strconv.Atoi(numStr)
				if cerr != nil {
					continue
				}
				lines := ix.lookup(path, target)
				if lines == nil {
					// Not in this repo, or an ambiguous basename we refuse to
					// guess at. Either way, not a finding.
					continue
				}
				sym, offset, atDecl, ok := resolve(lines, n)
				if !ok {
					continue
				}
				switch {
				case atDecl:
					hits = append(hits, fmt.Sprintf(
						"%s:%d: cite `%s` instead of %s:%d — the line IS its declaration",
						rel, lineNo, sym, target, n))
				case offset <= maxOffset:
					hits = append(hits, fmt.Sprintf(
						"%s:%d: cite `%s` instead of %s:%d — %d lines into that declaration",
						rel, lineNo, sym, target, n, offset))
				}
				// Deeper than maxOffset: allowed, the number earns its keep.
			}
		}
		return nil
	})
	if walkErr != nil {
		fmt.Fprintln(os.Stderr, "cite-guard: walk error:", walkErr)
		os.Exit(2)
	}
	if len(hits) > 0 {
		fmt.Fprintf(os.Stderr, "cite-guard: %d line-number citation(s) a symbol name replaces:\n\n", len(hits))
		for _, h := range hits {
			fmt.Fprintln(os.Stderr, "  "+h)
		}
		fmt.Fprintln(os.Stderr, "\nWhy: line numbers rot on every insertion and nothing maintains them.")
		fmt.Fprintln(os.Stderr, "codegraph resolves a symbol name on demand. A line number is fine when it")
		fmt.Fprintf(os.Stderr, "points deeper than %d lines into a declaration, where a name cannot reach.\n", maxOffset)
		os.Exit(1)
	}
}
