//go:build e2e_realclaude

package realclaude

// #1951, #1952 and #1950 — the shape assertion over an AskUserQuestion capture:
// the decode target declared whole, the findings-returning check with its
// undecodable-input guard, the thin fatal wrapper other tests call, and EIGHT
// shape checks with a negative row apiece.
//
// Two later slices must decide whether a captured AskUserQuestion payload is
// well-formed — the live run (#1938), which must not report success on an empty
// or malformed capture, and the offline reader (#1939), which must redden when a
// claude release renames or drops a field. Two copies of a shape contract drift,
// so it is built once, here, against the tool's DOCUMENTED shape: the instrument
// has to exist before the bytes arrive, because it is what the capture is measured
// against. And a shape assertion that cannot fail is worse than none, since both
// consumers read its silence as evidence — so the negative rows ship in the same
// commit as the checks they pin, and the decode guard is specified rather than
// left to construction, a helper returning "nothing missing" on bytes it could not
// parse being the shortest route to exactly that silence.
//
// # The limit of this file, stated rather than left to be inferred
//
// EIGHT SHAPE CHECKS SHIP HERE: the tool-name field, the presence of at least one
// question, #1952's five over the FIRST question's contents — its question text,
// its header, its option count, its option labels and its option descriptions —
// and #1950's, that the first question carries a multiSelect KEY at all.
//
// Three limits bound all eight, stated here so nothing downstream over-reads them.
// ONLY THE FIRST QUESTION IS CHECKED, for the reason askQuestionShapeFindings
// argues: checking each would make the finding count depend on batch width. THE
// MULTISELECT CHECK IS PRESENCE, NOT TRUTH — it does not judge the value, so false
// passes, true passes, and an explicit null passes because the key IS there. What
// a multiSelect value means is the tool's own semantics, and a shape assertion
// reddening on a well-formed false would be reporting a defect in a perfectly good
// capture. And whether claude NESTS options under each question or FLATTENS them
// across the batch is still unmeasured; askQuestionInput's doc states that one,
// and #1938's capture is what settles it.
//
// Three counts, reconciled once so that no reader has to do it again: EIGHT shape
// checks; NINE reported names, the eight plus the decode guard, which is not a
// shape check though its negative row is additional; and TEN table rows, the nine
// negatives plus the positive control. The sole-redness property below is over
// that whole table, the guard's row included, and over no one slice's own rows.
//
// # This file is #1942's offline successor and execs nothing
//
// Nine shipped comments in this package name #1942 and eight describe it as a
// live-capture slice. One argues that "finOfflineExecBans is per-file, and #1942's
// and #1938's live capture files exec, so neither can ever carry an entry". That
// sentence is not a ruling about THIS file: this slice is #1942's successor, it
// starts no child and reads neither a directory nor a capture file, and its
// finOfflineExecBans entry below proves it. Those comments are not corrected here —
// that belongs with the slice that builds what they describe, the convention #1941
// applied when it left #1943's stale sentence alone. #1938 corrects them.
//
// # Offline, and further: no I/O in either direction
//
// This file reaches no live claude, no daemon, no subprocess, no credential and no
// directory. Its fixtures are synthetic literals; the only in-package helper it
// calls is askQuestionFullRecord, which is pure literals.
// TestFinOfflineFilesReachNoExecHelper enforces that over this file's AST rather
// than over this paragraph — it parses without parser.ParseComments, so the check
// cannot answer itself out of the header stating it — and this file's
// finOfflineExecBans entry says which names matter most here and why.
//
//	go test -tags e2e_realclaude -race -count=1 -v \
//	  -run 'TestAskQuestionShape|TestFinOfflineFilesReachNoExecHelper' \
//	  ./internal/e2e/realclaude/
//
// Every one must report PASS — not SKIP, not "no tests to run" — on a machine with
// no claude and no credentials. Read the count of tests that executed, never the
// exit code: this package is behind the e2e_realclaude tag, `make check` never
// compiles it, and the suite exits 0 both on a build failure and on a full
// credentials skip. `make preship` is the gate that proves the package builds.

import (
	"encoding/json"
	"reflect"
	"testing"
)

// --- the decode target, declared whole and read in part ------------------------

// askQuestionInput is an AskUserQuestion call's input: a batch of questions.
//
// Whether claude nests options under each question or FLATTENS them across the
// batch is UNMEASURED — nothing in this repo holds a real AskUserQuestion tool_use
// block, so this target is written against the tool's documented shape and #1938's
// capture is what settles it. Do not add a second accepted form to absorb the
// other spelling in advance: a target accepting both cannot redden on either, and
// reddening is how the capture reports the divergence.
type askQuestionInput struct {
	Questions []askQuestionQuestion `json:"questions"`
}

// askQuestionQuestion is one question of the batch. EVERY FIELD IS READ by
// askQuestionShapeFindings now — Header, Question and both option fields by
// #1952's content checks, MultiSelect by #1950's presence check. It was declared
// WHOLE rather than grown one field per slice on the argument that growing it that
// way is three decodes of one shape; two slices have since extended the checks
// over it without touching the type at all, which is that argument vindicated
// rather than merely asserted.
//
// MULTISELECT IS json.RawMessage AND NOT A bool, and that is a LIVE CONSTRAINT now
// rather than a promise to a later slice. A bool collapses an ABSENT key into
// false; a raw message leaves an absent key nil and a present false as the four
// bytes `false`, and that is the only reason presence is distinguishable from
// truth here at all. The presence check is a LENGTH TEST over these raw bytes, so
// retyping this field bool does not COMPILE — the strongest form the rule can take
// and stronger than this paragraph, which is why the paragraph says why rather
// than merely forbidding it.
type askQuestionQuestion struct {
	Header      string              `json:"header"`
	Question    string              `json:"question"`
	MultiSelect json.RawMessage     `json:"multiSelect"`
	Options     []askQuestionOption `json:"options"`
}

// askQuestionOption is one offered answer.
//
// A NAMED TWO-FIELD STRUCT, NOT []json.RawMessage. The option-label and
// option-description checks in askQuestionShapeFindings read both fields; leaving
// the element type opaque would have forced #1952 to redo this decode rather than
// extend it.
type askQuestionOption struct {
	Label       string `json:"label"`
	Description string `json:"description"`
}

// --- the check names ------------------------------------------------------------

// The reportable outcomes, one constant each. They are shared between the emit
// site below and the table rows' want values.
//
// THE LIMIT, STATED RATHER THAN HIDDEN: a misspelled constant is invisible to
// every row, because both sides read the same identifier. That is accepted here
// and would NOT be accepted for #1943's field listing, and the difference is who
// consumes the strings: those names are JSON tags a downstream decoder reads, so a
// second hand-written copy earns its keep; these are internal diagnostics no code
// outside this file consumes.
// THE TWO OPTION NAMES ARE PLURAL DELIBERATELY. Their checks scan every option and
// report ONE name each, so the plural is the reader's cue that a single finding
// covers the whole option batch however wide it is. option_count carries no
// _nonempty suffix for a related reason: it is a bound, not an emptiness test.
// multi_select_present is SINGULAR and carries _present rather than _nonempty for
// that same reason once more — it reports on one key of one question, and it is a
// key-presence test rather than an emptiness test over a value.
const (
	askQuestionCheckToolName                   = "tool_name"
	askQuestionCheckToolInputDecodes           = "tool_input_decodes"
	askQuestionCheckQuestionsNonEmpty          = "questions_nonempty"
	askQuestionCheckQuestionTextNonEmpty       = "question_text_nonempty"
	askQuestionCheckHeaderNonEmpty             = "header_nonempty"
	askQuestionCheckOptionCount                = "option_count"
	askQuestionCheckOptionLabelsNonEmpty       = "option_labels_nonempty"
	askQuestionCheckOptionDescriptionsNonEmpty = "option_descriptions_nonempty"
	askQuestionCheckMultiSelectPresent         = "multi_select_present"
)

// askQuestionCheckNames lists every name askQuestionShapeFindings can report, in
// emit order. It exists for the vacuity control below — without which AC 1's "each
// missed check named individually" is unpinned, since two colliding constants
// would leave the wrapper's message unable to say which check fired while every
// row still passed. #1952 appended its five names here and #1950 appended its one,
// each inheriting that control for free rather than building its own, so it now
// covers all NINE rather than the original three.
//
// Nothing asserts that this listing matches the emit sites. A reviewer diffs them,
// the same instrument #1943 relies on for its tags.
func askQuestionCheckNames() []string {
	return []string{
		askQuestionCheckToolName,
		askQuestionCheckToolInputDecodes,
		askQuestionCheckQuestionsNonEmpty,
		askQuestionCheckQuestionTextNonEmpty,
		askQuestionCheckHeaderNonEmpty,
		askQuestionCheckOptionCount,
		askQuestionCheckOptionLabelsNonEmpty,
		askQuestionCheckOptionDescriptionsNonEmpty,
		askQuestionCheckMultiSelectPresent,
	}
}

// --- the check ------------------------------------------------------------------

// askQuestionShapeFindings returns the name of every shape check rec fails, and
// nil when it fails none. It is pure, total over any record, and takes no
// testing.TB: a negative row cannot assert on a t.Fatalf, so the checking has to
// RETURN and the fataling has to be somewhere else. requireAskQuestionShape below
// is that somewhere.
//
// THE TOOL-NAME CHECK READS THE RECORD FIELD, NOT THE DECODED INPUT. That is what
// keeps the undecodable row at exactly ONE finding, which is what makes it a sole
// red for the guard rather than an over-determined row proving neither of two
// things.
//
// THE DECODE GUARD RETURNS EARLY, and the early return is itself pinned by a row
// rather than by construction: keep the appended finding but drop the return and
// the undecodable row reports TWO names, because a failed decode leaves the batch
// empty and the questions check then fires too. AC 1's "skipped rather than
// reported beside it" is that row.
//
// THE BATCH-LENGTH CHECK RETURNS EARLY TOO, and that one is FORCED rather than
// stylistic. The tempting alternative — guard the index, leave a zero-valued
// question in hand and let the content checks run over it — makes the empty-batch
// row report FOUR names on unmutated code: the batch length, plus question text,
// header and option count against that zero value. The row would be
// over-determined, and deleting any one of those three would redden it alongside
// its own row, destroying sole-redness for three checks at once.
//
// THE TWO OPTION CHECKS SCAN EVERY OPTION AND REPORT ONE NAME EACH, through one
// loop with two flags rather than an append inside the loop. Three properties come
// out of that shape and every one of them is load-bearing:
//
//   - The findings slice's length is independent of claude-supplied input.
//     Appending inside the loop makes requireAskQuestionShape's message grow with
//     the number of options a child chose to send, into a run log this pipeline
//     salvages.
//   - Emit order is fixed: labels always precede descriptions. Appending inside
//     the loop emits them in whatever order the offending options happen to sit
//     in, which breaks any exact-equality row tripping both.
//   - No index of an offending option is ever computed, so no positional
//     claude-derived value is in scope for a later edit to fold into a finding.
//
// ONLY THE FIRST QUESTION IS CHECKED. A real batch may carry several; checking
// each would make the finding count depend on batch width, the same property the
// option loop protects one level down.
//
// THE RETURN VALUE CARRIES NO BYTES FROM THE RECORD — only the fixed constants
// above are ever appended. Not the decode error, not a field value, not an
// excerpt. This is the design's security property and it is stronger than a "do
// not print it" rule at the call site: #1938 calls this over a live child's bytes,
// and a return type that structurally cannot carry child-derived text needs no
// discipline to stay clean. Discarding json.Unmarshal's error is deliberate; the
// malformed bytes are on disk in the artifact and inspectable there.
//
// IT RETURNS nil, NEVER make([]string, 0, 3). reflect.DeepEqual(nil-slice,
// []string{}) is FALSE, so a pre-allocated empty return reddens the positive
// control with a message reading `[] != []` and costs an hour to diagnose.
func askQuestionShapeFindings(rec *askQuestionFixtureRecord) []string {
	var findings []string

	// The literal here is a SECOND, INDEPENDENT COPY of the one in the positive
	// control's row, and that is the point: misspell it in either place and the
	// positive control reddens.
	if rec.ToolName != "AskUserQuestion" {
		findings = append(findings, askQuestionCheckToolName)
	}

	var in askQuestionInput
	if err := json.Unmarshal(rec.ToolInput, &in); err != nil {
		return append(findings, askQuestionCheckToolInputDecodes)
	}

	if len(in.Questions) == 0 {
		return append(findings, askQuestionCheckQuestionsNonEmpty)
	}

	first := in.Questions[0]
	if first.Question == "" {
		findings = append(findings, askQuestionCheckQuestionTextNonEmpty)
	}
	if first.Header == "" {
		findings = append(findings, askQuestionCheckHeaderNonEmpty)
	}
	// A BOUND, not == 0 or == 1. The single-option row is what pins it, and pins
	// nothing at all against a comparison that only rejects an empty slice.
	if len(first.Options) < 2 {
		findings = append(findings, askQuestionCheckOptionCount)
	}

	var labelMissing, descriptionMissing bool
	for _, opt := range first.Options {
		if opt.Label == "" {
			labelMissing = true
		}
		if opt.Description == "" {
			descriptionMissing = true
		}
	}
	if labelMissing {
		findings = append(findings, askQuestionCheckOptionLabelsNonEmpty)
	}
	if descriptionMissing {
		findings = append(findings, askQuestionCheckOptionDescriptionsNonEmpty)
	}

	// PRESENCE, NOT TRUTH, and a length test is what keeps the two apart: an absent
	// key leaves the json.RawMessage nil, while a present false is the four bytes
	// `false`. There is no present-but-empty raw value to worry about — the JSON
	// scanner skips leading whitespace before handing the token over.
	//
	// THE LENGTH IS NOT APPENDED, and neither is the value. Both are claude-derived,
	// this function's return type is the file's security property, and this check is
	// the one place where a "just for diagnostics" excerpt would be one edit away:
	// the raw bytes are sitting right here where a diagnostic wants them.
	//
	// APPENDED LAST rather than slotted in beside the other per-question checks.
	// #1952's five content rows stay contiguous that way, so the comment on the
	// first of them still describes the group it sits on; and emit order pins
	// nothing here, since every row trips exactly one check, so appending is simply
	// what keeps askQuestionCheckNames' "in emit order" claim true.
	if len(first.MultiSelect) == 0 {
		findings = append(findings, askQuestionCheckMultiSelectPresent)
	}

	return findings
}

// requireAskQuestionShape fails the test when rec is not a well-formed
// AskUserQuestion capture. It is the form other tests in this package call.
//
// WHAT NOTHING PROVES, so that the rows below are not credited with it: nothing
// proves that this wrapper CALLS askQuestionShapeFindings. A negative row cannot
// assert on a t.Fatalf — it would take the calling subtest down with it, there is
// no fake testing.TB in this package and testing.TB cannot be implemented outside
// testing. That link is construction, and it is the ONE untestable link here.
// TestInitControlFixture_ScanRefusesAPlantedCredential states the same limit for
// the sibling family; the difference is that it sidesteps its writer's fatal by
// calling the pure scan directly, whereas here the pure function IS the
// deliverable and the untestable link is confined to this one call.
//
// NEVER %v, %+v OR %#v THE RECORD. %+v on askQuestionFixtureRecord prints
// ToolInput, and #1938 calls this wrapper with a live child's bytes in that field,
// into run logs this pipeline salvages. ToolName is admitted because it is a
// bounded tool identifier and because scanAskQuestionFixture and
// writeAskQuestionFixture already print it; that is the bound, not a licence to
// widen the set.
//
// t.Fatalf requires the test goroutine, so call this directly from one — NOT from
// #1938's stdout reader. Same rule scanAskQuestionFixture's doc states.
func requireAskQuestionShape(t *testing.T, rec *askQuestionFixtureRecord) {
	t.Helper()

	if findings := askQuestionShapeFindings(rec); len(findings) > 0 {
		t.Fatalf("#1951: the AskUserQuestion capture for claude_version_slug %q tool_name %q "+
			"fails %d shape check(s): %v. The offending bytes are deliberately not printed — "+
			"tool_input is claude-supplied and this message reaches a salvaged run log; read "+
			"them from the committed artifact instead",
			rec.ClaudeVersionSlug, rec.ToolName, len(findings), findings)
	}
}

// --- the rows -------------------------------------------------------------------

// askQuestionShapeRecord builds a row's record: #1943's fully-populated fixture
// with the two columns this assertion reads overridden.
//
// It exists so that EVERY row names both columns explicitly, the positive control
// included, which makes over-determination visible at a glance — each negative row
// differs from the control in exactly one column. The other two fields are carried
// untouched and unread; no control asserts on them, because a helper that
// corrupted ClaudeVersionRaw would change nothing about this file's subject.
//
// Do NOT add a fifth field to the record and do NOT reorder
// askQuestionFixtureInput's keys. Both are pinned by #1943's own tests; the key
// order in particular is load-bearing, and tidying it into alphabetical order
// reddens that file's vacuity control.
func askQuestionShapeRecord(toolName, input string) *askQuestionFixtureRecord {
	rec := askQuestionFullRecord()
	rec.ToolName = toolName
	rec.ToolInput = json.RawMessage(input)
	return rec
}

// TestAskQuestionShape_ReportsEachMissedCheckAndSkipsAfterAnUndecodableInput is
// #1951's AC 1 and AC 2, #1952's and #1950's: the fully-populated fixture reports
// nothing, which it does unchanged across #1952's five content checks and #1950's
// presence check — its "multiSelect":false passing is #1950's AC 1 in full, the
// half of presence-not-truth no negative row can carry — each check
// has its own record failing that check and no other, and an undecodable
// tool_input is reported under its own name with the decoded checks skipped rather
// than reported beside it.
//
// THE COMPARISON IS EXACT EQUALITY, NEVER CONTAINMENT, and that is what buys AC
// 2's sole-redness by CONSTRUCTION instead of by an overlay mutation run per check:
//
//   - Delete the tool-name check and the wrong-tool-name row returns nil against a
//     want of one name and reddens; every other row returns exactly its own
//     finding and stays green. Sole red.
//   - Delete the questions check and the no-question row reddens alone, same shape.
//   - Delete the guard's early return, keeping its finding, and the undecodable row
//     returns TWO findings and reddens alone.
//   - Delete the guard entirely and that row returns one finding under the WRONG
//     name and reddens alone.
//
// #1952's five content checks and #1950's multi-select presence check are uniform
// in exactly that way, so they are stated once rather than six times: delete any
// one check's append and that check's own row compares an empty result against a
// one-name want and reddens, while the other nine rows return exactly their own
// findings and stay green. The positive control stays green under all nine,
// because deleting a check can only REMOVE findings.
//
// The STRUCTURAL mutants are the ones worth naming individually, because each is
// pinned by one fixture choice a later editor could undo without noticing:
//
//   - Append inside the option loop instead of flagging, and the both-blank-labels
//     row returns TWO findings against a want of one and reddens alone. A row
//     blanking a single label leaves that mutant green, which is why that row
//     blanks both.
//   - Narrow the loop to first.Options[:1] and the second-option empty-description
//     row returns nothing and reddens alone. That is what pins the loop scanning
//     past index 0, and it is why that row blanks the SECOND option's description.
//   - Loosen the option-count bound to < 1 and the single-option row returns
//     nothing and reddens alone. A row carrying ZERO options would trip
//     option_count alone too — the option checks are vacuously satisfied over an
//     empty loop — but it would leave that mutant green.
//   - Drop the batch-length check's early return, keeping its finding, and the
//     empty-batch row PANICS on the first question rather than reddening on a
//     mismatch. That is a red, but one that takes the package down with it: that
//     gate's early return is load-bearing in a way the decode guard's is not.
//   - Weaken the multi-select check from PRESENCE into TRUTH — decode the field and
//     require true, or compare the raw bytes against `true` — and this one is
//     caught LOUDLY rather than precisely, so it is stated as what it is. The
//     absent-key row still reports its own name and stays green; SEVEN rows redden,
//     the positive control and the wrong-tool-name row (both carrying
//     askQuestionFixtureInput's "multiSelect":false) and all five content rows,
//     each returning one finding more than it wants. The row that names the mutant
//     is the POSITIVE CONTROL: a record carrying "multiSelect":false and expecting
//     NO finding is the only thing that can tell presence from truth, which is why
//     that half of the property needs no row of its own.
//   - Retype MultiSelect bool and the package does not BUILD — len does not compile
//     against a bool. Stated here beside the batch-length panic for the same
//     reason: it is a red that does not look like a table row failing, and a reader
//     expecting every mutant to surface as one is the reader who misreads it.
//
// Over-determination is unshippable for the same reason: a fixture tripping two
// checks returns two findings and fails its exact match on UNMUTATED code, so such
// a row cannot reach the branch. A containment assertion gives neither property.
//
// Each row mints its own record through askQuestionFullRecord, which returns a
// fresh pointer per call, so the parallel subtests share nothing.
func TestAskQuestionShape_ReportsEachMissedCheckAndSkipsAfterAnUndecodableInput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		rec  *askQuestionFixtureRecord
		want []string
	}{
		{
			// The positive control. want is OMITTED, i.e. nil, and must never become
			// []string{}: reflect.DeepEqual tells the two apart.
			name: "the fully-populated fixture reports nothing",
			rec:  askQuestionShapeRecord("AskUserQuestion", askQuestionFixtureInput),
		},
		{
			// The input is unchanged, so it still decodes and still carries a
			// question — this row differs from the control in the tool-name column
			// alone. The name is synthetic and carries the -FIXTURE marker for
			// #1701's reason: nobody must mistake it for a recording of a real call.
			name: "a wrong tool name is reported alone",
			rec:  askQuestionShapeRecord("ExitPlanMode-FIXTURE", askQuestionFixtureInput),
			want: []string{askQuestionCheckToolName},
		},
		{
			// An EXPLICITLY EMPTY batch rather than an omitted key: that is the
			// realistic malformed capture, and it reads as a batch-length check
			// rather than a key-presence one.
			name: "an empty question batch is reported alone",
			rec:  askQuestionShapeRecord("AskUserQuestion", `{"questions":[]}`),
			want: []string{askQuestionCheckQuestionsNonEmpty},
		},
		{
			// Truncated JSON. This row is the one that pins the guard's EARLY
			// RETURN: without it the empty batch of a failed decode makes the
			// questions check fire too and this want of one name goes red.
			name: "an undecodable tool_input is reported alone and skips the decoded checks",
			rec:  askQuestionShapeRecord("AskUserQuestion", `{"questions":`),
			want: []string{askQuestionCheckToolInputDecodes},
		},
		{
			// The five content rows below share one base shape and each degrades
			// exactly ONE value from it: one question, keys in askQuestionFixtureInput's
			// order, "multiSelect":false present, two options, -FIXTURE markers
			// throughout for #1701's reason. They are five flat literals rather than a
			// builder because five independent statements are five things a reviewer
			// diffs against the control and against each other, where a builder
			// centralises the mistake — and because a new in-file helper would make
			// this file's finOfflineExecBans entry say something untrue about the only
			// helper it calls.
			//
			// EVERY ONE OF THEM CARRIES "multiSelect":false, and the forward reference
			// that used to explain why has arrived: the multi-select presence check
			// reads that key now, so carrying it is what keeps these five rows at ONE
			// finding each rather than two. The prediction held — no row here was
			// rewritten when that check landed, and the absent-key row after this group
			// is the only fixture it had to add.
			//
			// This row and the one below it are the pair that is easy to get wrong. A
			// literal blanking BOTH the question text and the header trips two checks,
			// fails its own exact match on unmutated code, and pins neither. Each
			// blanks one and leaves the other populated — present-and-empty rather than
			// an omitted key, the convention the empty-batch row above states.
			name: "an empty question text is reported alone",
			rec: askQuestionShapeRecord("AskUserQuestion",
				`{"questions":[{"header":"Scope-FIXTURE","question":"","multiSelect":false,"options":[{"label":"first-FIXTURE","description":"the first synthetic option"},{"label":"second-FIXTURE","description":"the second synthetic option"}]}]}`),
			want: []string{askQuestionCheckQuestionTextNonEmpty},
		},
		{
			// The other half of that pair: the header is blank and the question text
			// is populated, so this row trips the header check and nothing else.
			name: "an empty header is reported alone",
			rec: askQuestionShapeRecord("AskUserQuestion",
				`{"questions":[{"header":"","question":"Which synthetic -FIXTURE option?","multiSelect":false,"options":[{"label":"first-FIXTURE","description":"the first synthetic option"},{"label":"second-FIXTURE","description":"the second synthetic option"}]}]}`),
			want: []string{askQuestionCheckHeaderNonEmpty},
		},
		{
			// ONE option, not zero, and its label and description are both non-empty,
			// so the option-count check is the only one this row trips. One is also
			// what pins the bound — see the loosen-to-< 1 mutant in this test's doc.
			//
			// A LOCAL LITERAL, deliberately not askQuestionPlantedInput. That helper is
			// this package's only other one-option AskUserQuestion input and is the
			// obvious thing to reach for, but its signature takes a plant and every
			// call site hands it askQuestionPlantedKeyPrefix or askQuestionPlantedPath.
			// Calling it here would put a credential-shaped literal into a record this
			// file's wrapper is designed to be handed, in a file whose stated defining
			// property is carrying none.
			name: "a single option is reported alone",
			rec: askQuestionShapeRecord("AskUserQuestion",
				`{"questions":[{"header":"Scope-FIXTURE","question":"Which synthetic -FIXTURE option?","multiSelect":false,"options":[{"label":"only-FIXTURE","description":"the only synthetic option"}]}]}`),
			want: []string{askQuestionCheckOptionCount},
		},
		{
			// BOTH labels are blank, and both descriptions are populated. Blanking both
			// still fails exactly one check, and it additionally pins "one name each":
			// the append-inside-the-loop mutant returns two findings on this row and
			// reddens alone, where a single blank label would leave it green.
			name: "empty option labels are reported alone and once",
			rec: askQuestionShapeRecord("AskUserQuestion",
				`{"questions":[{"header":"Scope-FIXTURE","question":"Which synthetic -FIXTURE option?","multiSelect":false,"options":[{"label":"","description":"the first synthetic option"},{"label":"","description":"the second synthetic option"}]}]}`),
			want: []string{askQuestionCheckOptionLabelsNonEmpty},
		},
		{
			// The SECOND option's description alone, which is what pins the loop
			// scanning past index 0. Paired with the both-blank-labels row above, the
			// loop's two failure shapes are covered by two rows and no tenth one.
			name: "a later option's empty description is reported alone",
			rec: askQuestionShapeRecord("AskUserQuestion",
				`{"questions":[{"header":"Scope-FIXTURE","question":"Which synthetic -FIXTURE option?","multiSelect":false,"options":[{"label":"first-FIXTURE","description":"the first synthetic option"},{"label":"second-FIXTURE","description":""}]}]}`),
			want: []string{askQuestionCheckOptionDescriptionsNonEmpty},
		},
		{
			// #1950's row, and it sits AFTER the content group rather than inside it so
			// that group's shared comment above still describes the five rows it sits
			// on. ONLY THE KEY IS MISSING from this literal — the header, the question
			// text and both fully-populated options are all still here, in
			// askQuestionFixtureInput's key order minus the one key. A fixture that also
			// blanked a header or dropped an option would return TWO findings, fail its
			// own exact match on unmutated code, and pin neither reason.
			//
			// ABSENT, not null and not false. "multiSelect":false is what the positive
			// control carries and must PASS; "multiSelect":null decodes to the four
			// bytes `null` and is therefore PRESENT by this check, which is correct —
			// see the presence-not-truth limit in this file's header.
			//
			// A LOCAL LITERAL, deliberately not askQuestionPlantedInput, for the reason
			// the single-option row above states — and the pull is stronger here,
			// because that helper's own literal happens to carry "multiSelect":false and
			// so looks like a near-complete base for this row. It is not: its signature
			// takes a plant and every call site hands it a credential-shaped or
			// operator-path value.
			name: "an absent multi-select key is reported alone",
			rec: askQuestionShapeRecord("AskUserQuestion",
				`{"questions":[{"header":"Scope-FIXTURE","question":"Which synthetic -FIXTURE option?","options":[{"label":"first-FIXTURE","description":"the first synthetic option"},{"label":"second-FIXTURE","description":"the second synthetic option"}]}]}`),
			want: []string{askQuestionCheckMultiSelectPresent},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := askQuestionShapeFindings(tt.rec)
			if !reflect.DeepEqual(got, tt.want) {
				// Both lengths are printed alongside both slices because the one
				// mismatch %q cannot show is nil against empty, and that is the
				// mismatch a pre-allocated return produces.
				t.Errorf("#1951: the shape check reported %q (%d finding(s)), want %q (%d); each "+
					"row names the ONE check it fails, so a mismatch here is either a check that "+
					"stopped firing or a row that trips a second one",
					got, len(got), tt.want, len(tt.want))
			}
		})
	}

	t.Run("the check names are distinct and non-empty", func(t *testing.T) {
		t.Parallel()

		// THE VACUITY CONTROL. Two constants collapsed onto one string would leave
		// the wrapper's message unable to say which check fired, and every row above
		// would still pass — the rows compare against the same identifiers.
		seen := map[string]bool{}
		for i, name := range askQuestionCheckNames() {
			if name == "" {
				t.Errorf("#1951: check name %d is empty; a finding under an empty name tells "+
					"#1938 and #1939 that something failed and nothing about what", i)
				continue
			}
			if seen[name] {
				t.Errorf("#1951: check name %d, %q, is already used by an earlier check; two "+
					"checks sharing a name make every row above pass while the wrapper's "+
					"message cannot say which one fired", i, name)
			}
			seen[name] = true
		}
	})
}
