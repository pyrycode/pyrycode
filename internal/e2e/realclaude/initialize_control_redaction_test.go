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
//
// # #1733 — the pass that applies the table to the record
//
// redactInitControlRecord and its two tests join this file rather than taking a
// file of their own, because finOfflineExecBans is keyed by FILENAME and
// TestFinOfflineFilesReachNoExecHelper drives its subtests from `for f := range
// finOfflineExecBans`: a new file with no entry produces no subtest and no
// failure. This file's entry already bans realHome and os.TempDir, which are the
// two ambient reads the pass must not acquire, and already permits t.TempDir,
// which initControlDivergentDir needs. Everything #1733 adds below builds its
// values from this file's own constants and from initControlDivergentDir, so the
// entry needs no change.
//
// The pass still writes no file and reads none. Its two callers are the live
// capture's fill site, which is in a file that execs and is correctly unbanned,
// and the two tests below.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
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

// --- #1733: the pass over the record ------------------------------------------

// initControlRedactRaws is redact for a []json.RawMessage field. It is shaped
// exactly like dropcapRedactor.strs — including the make(…, len(in)) that turns a
// nil field into an empty one — because the two slice shapes behaving identically
// is worth more than preserving a distinction this record does not use.
//
// The nil-becomes-empty consequence is ACCEPTED rather than guarded. It can only
// move a committed `null` to `[]`, only for models_entry_fields and
// stdin_write_errors, and only on a run where they carried nothing. The record
// deliberately carries no `omitempty` on any tag so that absent values stay
// visible as present keys in an artifact a human reads, and `[]` is the more
// legible of the two. Nothing decodes the committed bytes.
func initControlRedactRaws(red *dropcapRedactor, in []json.RawMessage) []json.RawMessage {
	out := make([]json.RawMessage, len(in))
	for i, raw := range in {
		out[i] = json.RawMessage(red.redact(raw))
	}
	return out
}

// redactInitControlRecord rewrites every string-bearing field of rec through red,
// in place, and returns the classes that fired. It assigns NOTHING else to rec.
//
// # Every field of every string-bearing shape, never a curated list
//
// The record carries four string-bearing Go shapes — string, []string,
// json.RawMessage and []json.RawMessage — and a pass that visits one and not
// another is green on a fixture that exercises only the shape it visits.
// control_request_sent is the trap: a bare json.RawMessage, a DIFFERENT Go type
// from the two raw-JSON slices beside it, so a type switch naming
// []json.RawMessage and forgetting json.RawMessage leaves it untouched.
// compactInitControlRawRows' doc records that same split as "red on arrival", not
// a latent risk.
//
// wait_error, scanner_error and stdin_write_errors carry a child's own error text,
// which is exactly where a path arrives that a targeted pass forgets. They are
// visited for that reason rather than singled out.
//
// # Never a whole-record round trip
//
// Marshalling the record, substituting into the bytes and unmarshalling back is
// NOT a legal implementation of "uniform". Measured on this toolchain 2026-08-24:
// it strips insignificant whitespace from every json.RawMessage and HTML-escapes
// '<', '>' and '&' inside them into numeric \u escapes, so a payload that named no
// path comes back CHANGED. Each payload is rewritten over its own bytes instead,
// and TestInitControlRedactRecord_ReplacesEveryClassInEveryShape's byte-identity
// subtest is what reports the round trip.
//
// # It REPORTS the census, it does not STORE it
//
// initControlFixtureRecord gains no field here. #1731 is the slice that puts the
// census on the record. A field added in this slice reddens
// TestInitControlRedactRecord_LeavesAPathFreeRecordByteIdentical on arrival — the
// marshal before the pass carries it at its zero value and the marshal after
// carries it populated — and the record's field listing counts with
// reflect.TypeOf(initControlFixtureRecord{}).NumField(), so it would not even have
// collided with #1731; it would simply be #1731's work in the wrong slice.
//
// after_send_point_result_trailers is deliberately NOT visited: every field of
// initControlResultTrailer is an int, a float64 or a bool, so no path can reach it
// and the pass has nothing to do there. Leaving it alone is also what keeps the
// path-free row honest — initControlFullRecord's two trailer entries survive byte
// for byte.
func redactInitControlRecord(red *dropcapRedactor, rec *initControlFixtureRecord) []dropcapSubstitution {
	rec.ClaudeVersionRaw = red.str(rec.ClaudeVersionRaw)
	rec.ClaudeVersion = red.str(rec.ClaudeVersion)
	rec.Arm = red.str(rec.Arm)
	rec.ControlRequestID = red.str(rec.ControlRequestID)
	rec.ControlResponseSubtype = red.str(rec.ControlResponseSubtype)
	rec.StderrCapture = red.str(rec.StderrCapture)
	rec.WaitError = red.str(rec.WaitError)
	rec.ScannerError = red.str(rec.ScannerError)

	rec.Argv = red.strs(rec.Argv)
	rec.Prompts = red.strs(rec.Prompts)
	rec.ModelsEntryFields = red.strs(rec.ModelsEntryFields)
	rec.StdinWriteErrors = red.strs(rec.StdinWriteErrors)

	// redact over a nil []byte returns nil, so a record that sent no control
	// request keeps its nil-ness here rather than acquiring `""`.
	rec.ControlRequestSent = json.RawMessage(red.redact(rec.ControlRequestSent))
	rec.ControlResponses = initControlRedactRaws(red, rec.ControlResponses)
	rec.StdoutEvents = initControlRedactRaws(red, rec.StdoutEvents)

	return red.substitutions()
}

// The two payloads the byte-identity subtest measures, named as constants so that
// the comparison uses the SAME constant on both sides rather than a variable the
// record also holds. Reading the expectation back off the subject asserts nothing.
//
// Each MUST carry at least one of '<', '>', '&' or insignificant whitespace, and
// that is the whole reason for their content. Without it the byte-identity clause
// is 0-RED against the whole-record round trip it exists to reject, because a
// re-marshal moves nothing else here: the event carries '<', '>' and '&' in a
// realistic assistant-text shape, and the response carries a space after each
// colon.
//
// initControlFullRecord must NOT acquire any of these characters — literal-choice
// 2 in its own doc comment forbids exactly that, and the path-free row below is
// that record. These belong here instead.
const (
	initControlUntouchedEvent    = `{"type":"assistant","message":{"content":[{"type":"text","text":"if a < b && c > d"}]}}`
	initControlUntouchedResponse = `{"type": "control_response", "response": {"subtype": "success"}}`
)

// TestInitControlRedactRecord_ReplacesEveryClassInEveryShape is AC2, AC3 and AC5
// over ONE record the parent builds, redacts once, and hands to four read-only
// subtests.
//
// The fixture puts a value of every class in a field of each of the four
// string-bearing Go shapes, so a pass that visits one shape and not another
// reddens HERE rather than on the next live run:
//
//   - []string — the operator home, in argv[0].
//   - string — the system temp dir, in stderr_capture.
//   - []json.RawMessage — the workdir in its RESOLVED spelling, as the captured
//     system/init line's cwd, beside the composite memory_paths.auto value.
//   - json.RawMessage — the workdir in the HANDED spelling, in
//     control_request_sent. A real initialize request carries no cwd; this one
//     does, because that field's SHAPE is what no other row here exercises and a
//     type switch that forgets the bare json.RawMessage is green on every other
//     row.
//
// The composite reproduces what line-by-line measurement found in the committed
// artifact's memory_paths.auto, with its MIXED SPELLINGS: the temp home as a
// literal prefix in the spelling the harness handed over, then /.claude/projects/,
// then the project-slug spelling of the workdir's RESOLVED form, then /memory/.
// The mix is the measurement, not a flourish — it is why one value collapses to
// $TEMP_HOME and the other to $WORKDIR through rules of two different spellings,
// and a row built with both values in their handed spellings is green even against
// a table that never enumerated a resolved form, which is precisely the miss the
// measured cwd proved.
//
// Every value is assembled from this row's OWN inputs and never from the measured
// bytes, which name the operator's machine. No value is read from a credential or
// from a live claude: each is a literal this test controls or the pair
// initControlDivergentDir mints, so the whole test runs on a machine with no
// claude and no credentials.
//
// The comparison is against expected BYTES rather than an absence, and on the
// composite that is the only thing that discriminates: both substitution orderings
// leave zero denied values behind there, so an absence check passes on the mangled
// string.
func TestInitControlRedactRecord_ReplacesEveryClassInEveryShape(t *testing.T) {
	t.Parallel()

	handed, resolved := initControlDivergentDir(t)

	composite := initControlTempHomeValue + "/.claude/projects/" + dropcapSlug(resolved) + "/memory/"
	initEvent := `{"type":"system","subtype":"init","cwd":"` + resolved +
		`","memory_paths":{"auto":"` + composite + `"}}`
	requestSent := `{"type":"control_request","request_id":"req_init_1",` +
		`"request":{"subtype":"initialize","cwd":"` + handed + `"}}`

	rec := &initControlFixtureRecord{
		ClaudeVersionRaw: "2.1.239 (Claude Code)",
		ClaudeVersion:    "2.1.239",

		Arm: "after_completed_turn",

		Argv:    []string{initControlOperatorHomeValue + "/.local/bin/claude", "--verbose"},
		Prompts: []string{"probe turn one"},

		ControlRequestID:       "req_init_1",
		ControlRequestSent:     json.RawMessage(requestSent),
		ControlResponses:       []json.RawMessage{json.RawMessage(initControlUntouchedResponse)},
		ControlResponseSubtype: "success",

		ModelsEntryFields: []string{"model", "displayName"},

		StdoutEvents: []json.RawMessage{
			json.RawMessage(initEvent),
			json.RawMessage(initControlUntouchedEvent),
		},

		StdinWriteErrors: []string{"write |1: broken pipe"},
		StderrCapture:    "child stderr: shim log at " + initControlTempDirValue + "/claude-shim.log",
		WaitError:        "signal: killed",
		ScannerError:     "bufio.Scanner: token too long",
	}

	beforeEvents := len(rec.StdoutEvents)
	beforeResponses := len(rec.ControlResponses)

	// The SHIPPED construction, over real values — never a rule table hand-built
	// beside it, and never four empty strings, which hold no rules and cannot
	// rewrite anything. The workdir slot takes the HANDED spelling, exactly as the
	// live call site hands it the workdir it created.
	red := newInitControlRedactor(
		initControlOperatorHomeValue,
		initControlTempHomeValue,
		handed,
		initControlTempDirValue,
	)

	subs := redactInitControlRecord(red, rec)

	t.Run("each value is replaced by the placeholder of its own class", func(t *testing.T) {
		t.Parallel()

		// Every want is a literal assembled from this row's own inputs. The
		// stdout_events[0] row compares the WHOLE payload rather than the two
		// leaked values separately: that covers cwd and the composite at once and
		// also asserts that nothing else in the payload moved, which two substring
		// rows would not.
		tests := []struct {
			field string
			got   string
			want  string
		}{
			{"argv[0]", rec.Argv[0], "$HOME/.local/bin/claude"},
			{"stderr_capture", rec.StderrCapture, "child stderr: shim log at $TMPDIR/claude-shim.log"},
			{"stdout_events[0]", string(rec.StdoutEvents[0]),
				`{"type":"system","subtype":"init","cwd":"$WORKDIR",` +
					`"memory_paths":{"auto":"$TEMP_HOME/.claude/projects/$WORKDIR/memory/"}}`},
			{"control_request_sent", string(rec.ControlRequestSent),
				`{"type":"control_request","request_id":"req_init_1",` +
					`"request":{"subtype":"initialize","cwd":"$WORKDIR"}}`},
		}
		for _, tc := range tests {
			if tc.got != tc.want {
				t.Errorf("#1733: %s redacted to %q, want %q; every value naming the operator's "+
					"home, the run's temp home, the child's workdir or the system temp dir must "+
					"carry the placeholder of its OWN class before the record reaches the writer",
					tc.field, tc.got, tc.want)
			}
		}
	})

	t.Run("the captured payloads still count and still decode", func(t *testing.T) {
		t.Parallel()

		if got := len(rec.StdoutEvents); got != beforeEvents {
			t.Errorf("#1733: the pass left %d stdout_events, want %d unchanged", got, beforeEvents)
		}
		if got := len(rec.ControlResponses); got != beforeResponses {
			t.Errorf("#1733: the pass left %d control_responses, want %d unchanged", got, beforeResponses)
		}

		if !json.Valid(rec.ControlRequestSent) {
			t.Errorf("#1733: control_request_sent no longer decodes as JSON after the pass: %s",
				rec.ControlRequestSent)
		}
		for i, raw := range rec.StdoutEvents {
			if !json.Valid(raw) {
				t.Errorf("#1733: stdout_events[%d] no longer decodes as JSON after the pass: %s", i, raw)
			}
		}
		for i, raw := range rec.ControlResponses {
			if !json.Valid(raw) {
				t.Errorf("#1733: control_responses[%d] no longer decodes as JSON after the pass: %s", i, raw)
			}
		}
	})

	t.Run("payloads that named no path come back byte-identical", func(t *testing.T) {
		t.Parallel()

		// Against the CONSTANTS the record was built from, never against a
		// variable the record also holds and never against the written file:
		// writeInitControlFixture marshals with json.MarshalIndent, which
		// re-indents INSIDE an embedded raw message, so a file-level assertion
		// here reddens for a perfectly correct pass.
		//
		// This is the sole red for a pass implemented as a whole-record marshal,
		// substitute and unmarshal back: that round trip strips the response's
		// spaces after its colons and escapes the event's '<', '>' and '&' into
		// numeric \u escapes.
		if got := rec.StdoutEvents[1]; !bytes.Equal(got, []byte(initControlUntouchedEvent)) {
			t.Errorf("#1733: stdout_events[1] named no path yet came back changed:\n got %s\nwant %s",
				got, initControlUntouchedEvent)
		}
		if got := rec.ControlResponses[0]; !bytes.Equal(got, []byte(initControlUntouchedResponse)) {
			t.Errorf("#1733: control_responses[0] named no path yet came back changed:\n got %s\nwant %s",
				got, initControlUntouchedResponse)
		}
	})

	t.Run("the census names exactly the classes that fired, with their counts", func(t *testing.T) {
		t.Parallel()

		// Re-derived against the fixture above rather than assumed. Counting is
		// per RULE within a class and accumulates on the redactor across every
		// field the pass visits, and longest-first means a longer rule's
		// replacement hides the shorter rules' values from the bytes that follow:
		//
		//   operator_home 1 — argv[0].
		//   temp_dir      1 — stderr_capture. It does NOT also fire inside the
		//                     composite, because $TEMP_HOME has already consumed
		//                     that prefix by the time the shorter rule runs.
		//   temp_home     1 — the composite's literal prefix.
		//   workdir       3 — `handed` in control_request_sent, `resolved` in cwd,
		//                     and dropcapSlug(resolved) inside the composite.
		//
		// Which is why the composite contributes one temp-home hit and one
		// workdir hit rather than three of anything.
		want := []dropcapSubstitution{
			{Class: dropcapClassOperatorHome, Replacement: "$HOME", Count: 1},
			{Class: dropcapClassTempDir, Replacement: "$TMPDIR", Count: 1},
			{Class: dropcapClassTempHome, Replacement: "$TEMP_HOME", Count: 1},
			{Class: dropcapClassWorkdir, Replacement: "$WORKDIR", Count: 3},
		}
		if !reflect.DeepEqual(subs, want) {
			t.Errorf("#1733: the pass reported %+v, want %+v; the census is what #1731 puts on "+
				"the record, and a wrong substitution order silently mis-assigns it", subs, want)
		}
	})
}

// TestInitControlRedactRecord_LeavesAPathFreeRecordByteIdentical is AC4, run
// through the SAME construction the capture's fill site uses — the same function,
// over real values, never a rule table hand-built beside it.
//
// initControlFullRecord carries no path value: its argv[0] is the literal "claude"
// and no field carries a denied prefix, so the writer's offline callers stay green
// and this row asks only that the pass rewrite nothing it was not asked to.
//
// It is not tautological. That record carries a `0` in three places —
// turn_boundaries [0, 7], total_cost_usd 0.0731 and a second trailer entry whose
// cost is zero — so it goes RED the moment the construction acquires a nonce rule
// (strconv.FormatInt never returns "", so add's empty-value guard cannot stop one,
// and 0 installs a rule that rewrites every `0` byte) or any other rule over a
// value it legitimately carries. It is also the sole red for a pass that stores its
// census on a new record field: the marshal before carries the field at its zero
// value and the marshal after carries it populated.
func TestInitControlRedactRecord_LeavesAPathFreeRecordByteIdentical(t *testing.T) {
	t.Parallel()

	rec := initControlFullRecord()

	before, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("#1733: marshal the record before the pass: %v", err)
	}

	red := newInitControlRedactor(
		initControlOperatorHomeValue,
		initControlTempHomeValue,
		initControlWorkdirValue,
		initControlTempDirValue,
	)
	redactInitControlRecord(red, rec)

	after, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("#1733: marshal the record after the pass: %v", err)
	}

	if !bytes.Equal(before, after) {
		t.Errorf("#1733: a record carrying no path value did not survive the pass unchanged:\n"+
			"before %s\n after %s", before, after)
	}
}
