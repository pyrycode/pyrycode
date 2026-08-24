//go:build e2e_realclaude

package realclaude

// #1732 — the table half of the `initialize` control-request capture's
// redaction: the substitution table this family's capture is rewritten through,
// built from the four path values its caller hands it and from NOTHING else.
//
// # Why this exists
//
// The committed artifact carries operator paths right now. Re-measured
// 2026-08-24 against testdata/initialize_control_v2.1.239.json: argv[0] under
// the operator's home, and the system/init line's cwd and memory_paths.auto
// under /private/var/folders/… and /var/folders/…. Three of the five fixed
// classes this package's own deny-scan arms, in a file that got a clean bill
// from two independent human reads.
//
// Redaction and scanning are deliberately different fabric. Redaction rewrites
// the values this harness KNOWS it produced — this file's table, applied to the
// record by #1733. The deny-scan (#1729) is the fail-closed net for the values
// it did not predict. Neither substitutes for the other.
//
// # Why a new construction rather than newDropcapRedactor
//
// The mechanism is reused whole: dropcapRedactor, dropcapRule, add, redact,
// dropcapPathSpellings, dropcapSlug and the four class constants all fit
// unchanged. Its CONSTRUCTOR does not, for two reasons that are shapes rather
// than bugs:
//
//   - It reads the environment twice on its own — realHome arms operator_home
//     and os.TempDir() arms temp_dir — so a redactor described as "constructed
//     from synthetic values" still carries two machine-dependent rules. That is
//     precisely how a table can look synthetic and carry the operator's machine.
//   - Its nonce is formatted with strconv.FormatInt, which never returns "", so
//     the empty-value guard in add cannot reach it and NO int64 avoids
//     installing a rule. Passing 0 installs one that rewrites every `0` byte it
//     meets — and #1733's consumer record carries TurnBoundaries []int{0, 7} and
//     a trailer with TotalCostUSD 0.
//
// Taking every path value as a parameter and taking no nonce, no session id and
// no FIFO path makes both failures impossible rather than guarded against. It
// also collapses two constructions into one: this file's fully test-determined
// table and #1733's shipped construction are the same function called with
// different arguments, so #1733's no-captured-bytes criterion runs against the
// construction that actually ships.
//
// # The trailing-slash trim lives in the construction, on purpose
//
// newDropcapRedactor normalises exactly one of its two environment reads,
// strings.TrimSuffix(os.TempDir(), "/"), and it is not cosmetic: measured
// 2026-08-24, os.TempDir() returns /var/folders/…/T/ WITH the slash on macOS,
// and an untrimmed rule rewrites …/T/TestRealClaude… to $TMPDIRTestRealClaude….
// Once the value arrives as a parameter the trim has to live somewhere, and at
// #1733's call site nothing catches the miss. It is the one permitted
// normalisation: each rule stays decidable from the caller's argument, which is
// the property, rather than copied from it verbatim.
//
// # Offline, and further: no I/O in either direction
//
// Every row here settles with no claude binary and no credentials, and must
// PASS rather than SKIP. This file must not reach resolveClaudeBin,
// probeClaudeVersion, WithWorktree, WithWorktreeAuthenticated or
// captureClaudeVersion; nor os.Getenv, os.Environ or os.LookupEnv; nor realHome
// or os.TempDir, which are exactly the two ambient reads the construction
// replaces with parameters. finOfflineExecBans carries the list and
// TestFinOfflineFilesReachNoExecHelper runs it — but that check is per-file
// SYNTAX, so it catches a direct reference and not a reference through a
// helper. The armed-values assertion below is what catches the second shape.
//
// The packageDir / os.WriteFile group the three nearest sibling entries carry is
// absent from this file's entry, and deliberately: this file has no writer, no
// reader and no fixture. It builds a table and substitutes into byte slices.

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// initControlRedactionInputs is the construction's parameter list as one value,
// so a row's expected rules can be assembled from the very inputs it passed.
type initControlRedactionInputs struct {
	operatorHome string
	tempHome     string
	workdir      string
	tempDir      string
}

// The nested synthetic values Rows 3 and 5 share. The NESTING is load-bearing
// rather than decorative: temp dir ⊃ temp home ⊃ workdir is what makes each
// class's slug spelling a substring of the next, and over unrelated values
// longest-first and shortest-first agree and prove nothing.
const (
	initControlTempDirValue      = "/synthetic/tmp"
	initControlTempHomeValue     = "/synthetic/tmp/home"
	initControlWorkdirValue      = "/synthetic/tmp/home/work"
	initControlOperatorHomeValue = "/synthetic/operator/home"
)

// newInitControlRedactor builds this family's substitution table from the four
// path values its caller hands it and from nothing else: one rule per spelling
// of each class, all sharing that class's name and replacement.
//
// Add order is innermost-first — workdir, temp home, operator home, temp dir —
// because add dedups by VALUE and keeps the first rule that claimed it, so when
// two classes are handed the same directory the inner class owns the
// placeholder. newDropcapRedactor orders the same way for the same reason; do
// not reorder alphabetically.
//
// The final sort is what makes the composite value collapse correctly, and it is
// the same descending-length sort newDropcapRedactor ends with.
func newInitControlRedactor(operatorHome, tempHome, workdir, tempDir string) *dropcapRedactor {
	r := &dropcapRedactor{counts: map[string]int{}}
	addPath := func(class, replacement, path string) {
		for _, spelling := range dropcapPathSpellings(strings.TrimSuffix(path, "/")) {
			r.add(class, replacement, spelling)
		}
	}
	addPath(dropcapClassWorkdir, "$WORKDIR", workdir)
	addPath(dropcapClassTempHome, "$TEMP_HOME", tempHome)
	addPath(dropcapClassOperatorHome, "$HOME", operatorHome)
	addPath(dropcapClassTempDir, "$TMPDIR", tempDir)

	sort.SliceStable(r.rules, func(i, j int) bool {
		return len(r.rules[i].value) > len(r.rules[j].value)
	})
	return r
}

// initControlDivergentDir returns a directory whose filepath.EvalSymlinks form
// genuinely differs from the spelling it hands back, on macOS AND on Linux.
//
// t.TempDir() alone is not enough. Measured 2026-08-24: on macOS it already
// diverges (/var/folders/… -> /private/var/folders/…), on Linux it typically
// does not — so a row resting on it is green here and green-and-VACUOUS there.
// The symlink makes the last path segment differ, and that difference exists on
// every platform.
//
// The directory must be real: filepath.EvalSymlinks("/synthetic/tmp") returns
// `lstat /synthetic: no such file or directory`, so an invented path enumerates
// two spellings instead of four and a construction that forgot the resolved form
// is green on every such row.
//
// Extracted rather than inlined because #1733 needs the same divergent value for
// its own fixture, and the t.Fatalf on a non-divergent result lives here so
// #1733 inherits the loud failure.
func initControlDivergentDir(t *testing.T) (handed, resolved string) {
	t.Helper()

	root := t.TempDir()
	target := filepath.Join(root, "real")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatalf("create %s: %v", target, err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("symlink %s -> %s: %v", link, target, err)
	}
	got, err := filepath.EvalSymlinks(link)
	if err != nil {
		t.Fatalf("resolve %s: %v", link, err)
	}
	if got == link {
		t.Fatalf("no divergence to test: %s resolves to itself", link)
	}
	return link, got
}

// initControlExpectedRules assembles the triples a construction over these
// inputs must hold, from the INPUTS alone. It never reads the table under test:
// reading the expectation back off the subject asserts nothing.
//
// It reproduces add's first-claimer dedup so that a row handing two classes the
// same directory would still describe the table the contract promises.
func initControlExpectedRules(in initControlRedactionInputs) []dropcapRule {
	out := []dropcapRule{}
	claimed := map[string]bool{}
	add := func(class, replacement, path string) {
		for _, spelling := range dropcapPathSpellings(strings.TrimSuffix(path, "/")) {
			if claimed[spelling] {
				continue
			}
			claimed[spelling] = true
			out = append(out, dropcapRule{class: class, value: spelling, replacement: replacement})
		}
	}
	add(dropcapClassWorkdir, "$WORKDIR", in.workdir)
	add(dropcapClassTempHome, "$TEMP_HOME", in.tempHome)
	add(dropcapClassOperatorHome, "$HOME", in.operatorHome)
	add(dropcapClassTempDir, "$TMPDIR", in.tempDir)
	return out
}

// initControlRuleKey renders one rule as the (class, replacement, value) triple
// the comparison is over. The VALUE is in the key deliberately: a construction
// that arms the caller's operator home and ALSO arms realHome behind the
// caller's back holds the same class set, so a class-set comparison is green
// against exactly the mutant these rows target.
func initControlRuleKey(r dropcapRule) string {
	return fmt.Sprintf("class=%s replacement=%s value=%q", r.class, r.replacement, r.value)
}

// initControlDiffRules reports BOTH directions. The extra direction is the one
// this comparison exists for, and it prints the offending value rather than a
// set-size mismatch — on the fallback mutant that value IS the operator's home
// path, which is the finding. A `go test` log is not a committed artifact; the
// committed artifact is #1733's, and this file writes none. Do not mask it.
func initControlDiffRules(t *testing.T, got, want []dropcapRule) {
	t.Helper()

	gotKeys, wantKeys := map[string]bool{}, map[string]bool{}
	for _, r := range got {
		gotKeys[initControlRuleKey(r)] = true
	}
	for _, r := range want {
		wantKeys[initControlRuleKey(r)] = true
	}

	var missing, extra []string
	for k := range wantKeys {
		if !gotKeys[k] {
			missing = append(missing, k)
		}
	}
	for k := range gotKeys {
		if !wantKeys[k] {
			extra = append(extra, k)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)

	for _, k := range missing {
		t.Errorf("rule missing from the table: %s", k)
	}
	for _, k := range extra {
		t.Errorf("rule the caller's values cannot explain: %s", k)
	}
}

// TestInitControlRedactorArmsOnlyItsCallersValues is AC1. Both rows compare
// VALUES, not class names, and both assemble the expectation from their own
// inputs.
//
// The absent row is where an ambient fallback surfaces: with operator home and
// system temp handed nothing, a construction reaching for realHome or
// os.TempDir() shows up as extra triples the row's inputs cannot explain.
//
// The trailing slash on the present row's temp dir makes the trim decidable
// rather than assumed. A construction that dropped it arms "/synthetic/tmp/" and
// "-synthetic-tmp-" where the trimmed one arms "/synthetic/tmp" and
// "-synthetic-tmp": a different value set, so the row reddens.
func TestInitControlRedactorArmsOnlyItsCallersValues(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   initControlRedactionInputs
	}{
		{
			name: "all four handed, temp dir with a trailing slash",
			in: initControlRedactionInputs{
				operatorHome: initControlOperatorHomeValue,
				tempHome:     initControlTempHomeValue,
				workdir:      initControlWorkdirValue,
				tempDir:      initControlTempDirValue + "/",
			},
		},
		{
			name: "operator home and system temp absent",
			in: initControlRedactionInputs{
				tempHome: initControlTempHomeValue,
				workdir:  initControlWorkdirValue,
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			red := newInitControlRedactor(tc.in.operatorHome, tc.in.tempHome, tc.in.workdir, tc.in.tempDir)
			initControlDiffRules(t, red.rules, initControlExpectedRules(tc.in))
		})
	}
}

// TestInitControlRedactorCollapsesTheNestedComposite is AC2: the shape the
// committed fixture's memory_paths.auto was measured carrying — the temp home as
// a literal prefix, then /.claude/projects/, then the project-slug spelling of
// the workdir, then /memory/.
//
// The input is assembled from this test's OWN values and never from the measured
// bytes, which name the operator's machine.
//
// The comparison is against expected BYTES rather than against an absence, and
// that is the whole point. Both orderings were run against the measured values
// 2026-08-24 and neither leaves a denied value behind — the machine-identifying
// token sits inside $TMPDIR either way, so a deny-scan and an absence assertion
// both pass on the mangled string. What shortest-first destroys is the
// PLACEHOLDER ASSIGNMENT: over these values it yields
// $TMPDIR/home/.claude/projects/$TMPDIR-home-work/memory/, crediting every path
// to temp_dir and leaving a value no reader can map back to a shape.
func TestInitControlRedactorCollapsesTheNestedComposite(t *testing.T) {
	t.Parallel()

	red := newInitControlRedactor(
		initControlOperatorHomeValue,
		initControlTempHomeValue,
		initControlWorkdirValue,
		initControlTempDirValue,
	)

	in := initControlTempHomeValue + "/.claude/projects/" + dropcapSlug(initControlWorkdirValue) + "/memory/"
	const want = "$TEMP_HOME/.claude/projects/$WORKDIR/memory/"

	if got := string(red.redact([]byte(in))); got != want {
		t.Errorf("redact(%q) = %q, want %q", in, got, want)
	}
}

// TestInitControlRedactorSubstitutesTheResolvedSpelling is AC3. The measured cwd
// leak is precisely this forgotten form: the harness built the workdir under the
// pinned $HOME in its /var/folders/… spelling and handed that to cmd.Dir, macOS
// resolved it on the way through the child, and a table carrying only the
// spellings it was handed leaves cwd untouched.
//
// The divergent directory is the ONLY armed class, so no cross-class shadowing
// can explain the result and "a class handed no value arms nothing" rides along.
// The four spellings cannot shadow one another either: the last segment differs
// between the handed and resolved forms, so neither slug contains the other.
func TestInitControlRedactorSubstitutesTheResolvedSpelling(t *testing.T) {
	t.Parallel()

	handed, resolved := initControlDivergentDir(t)
	if handed == resolved {
		t.Fatalf("no divergence to test: %q resolves to itself", handed)
	}

	red := newInitControlRedactor("", "", handed, "")

	in := strings.Join([]string{handed, dropcapSlug(handed), resolved, dropcapSlug(resolved)}, " | ")
	const want = "$WORKDIR | $WORKDIR | $WORKDIR | $WORKDIR"

	if got := string(red.redact([]byte(in))); got != want {
		t.Errorf("redact(%q) = %q, want %q", in, got, want)
	}
}

// TestInitControlRedactorLeavesUnrelatedBytesIdentical is AC4, over the widest
// table these rows build.
//
// The discriminating byte is `0`. This row reddens on a construction that
// inherits the sibling family's nonce parameter: strconv.FormatInt never returns
// "", so add's empty-value guard cannot stop such a rule, and passing 0 installs
// one that rewrites every `0` byte it meets. `<`, `>` and `&` ride along in the
// shape #1733's record pass will meet. The input carries none of the handed
// values, so a byte that changed changed because a rule the table should not
// hold fired — no prompt-nonce rule, no session-id rule, no FIFO-path rule.
func TestInitControlRedactorLeavesUnrelatedBytesIdentical(t *testing.T) {
	t.Parallel()

	red := newInitControlRedactor(
		initControlOperatorHomeValue,
		initControlTempHomeValue,
		initControlWorkdirValue,
		initControlTempDirValue,
	)

	const in = `{"turn_boundaries":[0,7],"total_cost_usd":0,"text":"<a> & <b>"}`

	if got := string(red.redact([]byte(in))); got != in {
		t.Errorf("redact(%q) = %q, want it back unchanged", in, got)
	}
}
