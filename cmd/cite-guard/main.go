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
// # The rule: name the symbol, at any depth, single line or range
//
// If a citation resolves to a declaration -- because the line IS one, or is a
// doc comment on one, or sits anywhere inside one -- name that declaration.
// A RANGE is a citation too, and rots the same way. Both its ends are
// resolved: when they agree it reads exactly like a single-line citation, and
// when they disagree the range spans declarations and the message names both
// ends, because there is no single symbol to offer.
//
// An earlier version exempted citations more than 20 lines deep, reasoning
// that there a line number pointed somewhere a name could not reach. The
// operator overturned it on 2026-08-13, and the argument is better: if a
// symbol name is not precise enough to locate something, the problem is the
// size of the declaration, not the citation. A line number used that way is
// accommodating a defect rather than describing one.
//
// The measurement agrees. All 67 citations the exemption used to permit sit
// in the biggest declarations in the repo -- trailClassifyRun at 454 lines
// takes 10 of them, trailRunCases 381, trailGate 343, runProbeRep 170. So a
// deep citation is a reliable pointer at an oversized function, and the
// imprecision of naming it is a finding rather than a cost.
//
// Deliberately NOT flagged:
//
//   - Bare `:NNN` citations. They inherit the LAST-NAMED FILE in the comment,
//     not the current one, so resolving them needs comment-context parsing
//     that this guard does not do. A cleanup script that assumed self-file
//     produced confidently wrong symbols; trail_run_outcome_test.go documents
//     the same hazard on #1434's evidence.
//   - A range whose ends do not both resolve. Same posture as an ambiguous
//     basename below: a wrong answer is worse than no answer.
//   - Anything outside a `//` comment. String literals and code are not this
//     guard's business.
//
// Ranges were exempt until 2026-08-14, on the reasoning that a span carries
// information a symbol name does not. The 2026-08-13 sweep had already
// converted 151 of them on the opposite argument and left the exemption
// standing, so the written rule spent a day recruiting exactly the citations
// the sweep removed. The operator closed it by keeping ranges out.
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
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// deepThreshold is not a permission boundary -- every resolvable citation is
// banned regardless of depth. It only shapes the message, so a reader is told
// when a citation is deep enough that the enclosing declaration is itself
// worth a look.
const deepThreshold = 20

// allowlist holds path suffixes exempt from the scan. This guard's own source
// is exempt because it must spell the pattern it bans, and so is its test,
// which must spell it in quantity. The scan keys on a line containing `//`,
// so an un-allowlisted test for this guard would flag itself.
var allowlist = []string{
	"cmd/cite-guard/main.go",
	"cmd/cite-guard/main_test.go",
}

var (
	// declRe matches a top-level declaration and captures its name.
	declRe = regexp.MustCompile(`^(?:func|type|const|var)\s+(?:\([^)]*\)\s*)?([A-Za-z_][A-Za-z0-9_]*)`)
	// citeRe matches a qualified citation, single line or range. The end of a
	// range is optional, so group 3 is empty for a single-line citation.
	//
	// # What the trailing character class does, measured rather than assumed
	//
	// Before ranges were matched, it did two jobs: it rejected ranges, and it
	// stopped the engine settling for a shorter number, the failure a cleanup
	// script hit when it matched ":93" out of ":934-944".
	//
	// Adding the optional end group retired the second job. Greedy matching now
	// takes ":934-944" whole on the first attempt, so there is no backtracking
	// to prevent. Measured on 2026-08-14 by running both patterns over the same
	// inputs: dropping the class changes the result on exactly two shapes, and
	// both are MALFORMED citations rather than well-formed ones.
	//
	//	runner.go:934-944-955   with the class: no match. Without: ":934-944",
	//	                        i.e. a silent prefix of what the author wrote.
	//	runner.go:934-          with the class: no match. Without: ":934".
	//
	// So the class is kept for a different reason than it was written for: it
	// refuses a citation it cannot read whole, rather than reporting part of
	// one and quoting a range the comment does not contain. The cost is that a
	// malformed range escapes the ban entirely, which is accepted because the
	// shape is rare and already broken.
	//
	// TestCiteRe_RejectsMalformedCitations pins the surviving property and
	// fails if the class is dropped. TestCiteRe_CapturesTheWholeRange pins the
	// greediness now doing the first job. Both matter because this guard
	// reports a clean tree and a broken pattern identically, with exit 0.
	citeRe = regexp.MustCompile(`\b([A-Za-z0-9_]+\.go):([0-9]+)(?:-([0-9]+))?([^0-9\-]|$)`)
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

// changedLines reports, per repo-relative path, the set of line numbers this
// branch ADDED or MODIFIED relative to its merge base. nil means "no base
// resolvable, or we are the base" — the caller then scans the whole tree.
//
// # Why the guard is diff-scoped at all
//
// A citation is resolved against its target line AS IT STANDS NOW. So when a
// developer inserts lines into file A, every citation pointing below that
// insertion — including citations in OTHER files, which this developer never
// touched — now addresses different content. One that was legal because it
// pointed deep inside a long declaration can land on a declaration and become
// illegal. Full-tree scanning bills that to whoever happened to be editing.
//
// Observed on pyrycode#1452 (2026-08-11), which is why this exists: the
// developer spent the last 8 minutes of a 26-minute run, and was still going
// when the wall clock killed it, on "the guard caught cites my edit displaced
// ... this is the renumbering tail". The guard had become an instance of the
// tax it was built to remove.
//
// So the contract is: this guard stops the INFLOW of new citations. It does
// not continuously revalidate the existing stock. Combined with opportunistic
// cleanup when someone genuinely touches a comment, the stock only shrinks.
// A displaced pre-existing citation rots exactly as it did before the guard
// existed, which is no worse than the status quo it replaced.
//
// Note the asymmetry with substrate-guard, which is correctly full-tree: a
// banned screen literal is always somebody's deliberate act and cannot be
// created at a distance by an unrelated edit.
func changedLines(root string) map[string]map[int]bool {
	base := os.Getenv("CITE_GUARD_BASE")
	if base == "" {
		for _, ref := range []string{"origin/main", "main"} {
			out, err := exec.Command("git", "-C", root, "merge-base", "HEAD", ref).Output()
			if err == nil {
				base = strings.TrimSpace(string(out))
				break
			}
		}
	}
	if base == "" {
		return nil // not a git checkout, or no base — scan everything
	}
	out, err := exec.Command("git", "-C", root, "diff", "--unified=0", base, "--", "*.go").Output()
	if err != nil {
		return nil
	}
	changed := map[string]map[int]bool{}
	hunk := regexp.MustCompile(`^@@ -\S+ \+(\d+)(?:,(\d+))? @@`)
	var cur string
	for _, l := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(l, "+++ b/") {
			cur = strings.TrimPrefix(l, "+++ b/")
			continue
		}
		m := hunk.FindStringSubmatch(l)
		if m == nil || cur == "" {
			continue
		}
		start, _ := strconv.Atoi(m[1])
		count := 1
		if m[2] != "" {
			count, _ = strconv.Atoi(m[2])
		}
		if changed[cur] == nil {
			changed[cur] = map[int]bool{}
		}
		for i := 0; i < count; i++ {
			changed[cur][start+i] = true
		}
	}
	if len(changed) == 0 {
		// We ARE the base (e.g. on main after a merge). Scanning nothing would
		// make the gate vacuous exactly where it most needs to hold, so fall
		// back to the whole tree.
		return nil
	}
	return changed
}

func main() {
	root := "."
	if len(os.Args) > 1 {
		root = os.Args[1]
	}
	ix := newIndex(root)
	scope := changedLines(root)
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
		// Diff-scoped when a merge base resolved: only lines this branch wrote.
		var only map[int]bool
		if scope != nil {
			only = scope[strings.TrimPrefix(rel, "./")]
			if only == nil {
				return nil // file untouched by this branch
			}
		}
		f, rerr := os.Open(path)
		if rerr != nil {
			return nil
		}
		defer f.Close()
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 0, 1024*1024), 1024*1024)
		for lineNo := 1; sc.Scan(); lineNo++ {
			if only != nil && !only[lineNo] {
				continue
			}
			idx := strings.Index(sc.Text(), "//")
			if idx < 0 {
				continue
			}
			comment := sc.Text()[idx:]
			for _, m := range citeRe.FindAllStringSubmatch(comment, -1) {
				target, numStr, endStr := m[1], m[2], m[3]
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
				// cited is what the message echoes back, so a range is quoted
				// as the author wrote it rather than as its start line alone.
				cited := fmt.Sprintf("%s:%d", target, n)
				if endStr != "" {
					end, eerr := strconv.Atoi(endStr)
					if eerr != nil {
						continue
					}
					endSym, _, _, endOK := resolve(lines, end)
					if !endOK {
						continue
					}
					cited = fmt.Sprintf("%s:%d-%d", target, n, end)
					if endSym != sym {
						// Spans declarations, so no single symbol replaces it.
						// Naming both ends is the honest report: it states only
						// what actually resolved.
						hits = append(hits, fmt.Sprintf(
							"%s:%d: name the symbols instead of %s — spans `%s` to `%s`, so say which you mean or split the reference",
							rel, lineNo, cited, sym, endSym))
						continue
					}
				}
				switch {
				case atDecl:
					hits = append(hits, fmt.Sprintf(
						"%s:%d: cite `%s` instead of %s — the line IS its declaration",
						rel, lineNo, sym, cited))
				case offset <= deepThreshold:
					hits = append(hits, fmt.Sprintf(
						"%s:%d: cite `%s` instead of %s — %d lines into that declaration",
						rel, lineNo, sym, cited, offset))
				default:
					hits = append(hits, fmt.Sprintf(
						"%s:%d: cite `%s` instead of %s — %d lines deep, so `%s` is probably too big",
						rel, lineNo, sym, cited, offset, sym))
				}
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
		if scope != nil {
			fmt.Fprintln(os.Stderr, "\nScope: only lines this branch added or modified. A citation displaced by")
			fmt.Fprintln(os.Stderr, "someone else's edit is not yours to fix and is not reported here.")
		} else {
			fmt.Fprintln(os.Stderr, "\nScope: whole tree (no merge base resolved, or this IS the base).")
		}
		fmt.Fprintln(os.Stderr, "\nWhy: line numbers rot on every insertion and nothing maintains them,")
		fmt.Fprintln(os.Stderr, "and codegraph resolves a symbol name on demand. A range rots the same way.")
		fmt.Fprintln(os.Stderr, "If a name is not precise enough to locate what you mean, that declaration is")
		fmt.Fprintln(os.Stderr, "too big; the imprecision is the finding, not a reason to keep the number.")
		os.Exit(1)
	}
}
