//go:build e2e_realclaude

package realclaude

// #1943 — the record half of the AskUserQuestion capture: the JSON contract the
// three slices that follow write, name and read, one fully-populated instance of
// it, the single field listing applied to BOTH sides of its round trip, and the
// proof that the call's input reaches the wire as the bytes that were written.
//
// # What this file deliberately is not
//
// It holds no writer — that is #1941, which reads this record and this listing
// and adds a deny-scan. No file namer: that is #1944. No shape assertion over a
// real call: that is #1942. No live run: that is the capture, which spends the
// tokens and fills these fields from a real child. This file builds a record and
// asserts on its own literals. It settles with no claude binary and no
// credentials, and it performs no I/O in either direction.
//
// # Why the field set is four and not more
//
// claude offers AskUserQuestion alongside the permission-prompt tool, and every
// committed permission_protocol_* capture under testdata/ lists the name in its
// `system`/`init` tools array while holding no tool_use block for it — so nothing
// in this repo has ever recorded a CALL to it.
//
// The record's field set is load-bearing beyond this ticket, because #1941's
// writer refuses to write by scanning the record's MARSHALLED BYTES: a field that
// does not marshal into that blob is a field the scan cannot see, and every field
// added here widens the surface that refusal has to cover. Four fields keep it a
// fixed deny list rather than a redaction-table build. The comparison is measured
// rather than hypothetical — #1688's whole-stream capture ran past 1300 committed
// lines and its family needed three follow-up tickets (#1729, #1732, #1733) to
// build a redaction table for the operator paths it had swallowed. That is why
// the cap is PINNED by a hand-written name literal below and not merely described
// here: a fifth field reddens rather than being absorbed.
//
// # One diagnosis trap, expected rather than broken
//
// A dropped or `json:"-"`-tagged tool_input leaves the read-back side nil, and
// json.Compact over nil returns "unexpected end of JSON input" — so that mutant
// surfaces as compactRawMessages' own t.Fatalf, which carries #1662's ticket
// number and names a control response. The verdict is correct and the mutant IS
// red; only the diagnosis points at another file. Do not go chasing #1662.
//
// # Offline, and further: no I/O in either direction
//
// This file reaches no live claude, no daemon, no subprocess, no credential and
// no directory. It must not reach resolveClaudeBin, probeClaudeVersion,
// WithWorktree, WithWorktreeAuthenticated or captureClaudeVersion; nor os.Getenv,
// os.Environ or os.LookupEnv; nor packageDir, any of its wrappers
// setModeFixturePath, writeSetModeFixture and writeFixture, filepath.Glob, or any
// os read or write. That last group is not tidiness: `go test` runs in the
// package source directory, so a RELATIVE os.WriteFile("testdata/…") reaches the
// eight committed permission_protocol_* captures without naming packageDir at
// all. TestFinOfflineFilesReachNoExecHelper enforces all of it over this file's
// AST rather than over this paragraph — it parses without parser.ParseComments,
// so the check cannot answer itself out of the header that states it — and the
// entry's own doc comment says why captureClaudeVersion, which returns exactly
// this record's two version fields at once, is the one name a developer here is
// most likely to reach for.
//
//	go test -tags e2e_realclaude -race -count=1 -v \
//	  -run 'TestAskQuestion|TestFinOfflineFilesReachNoExecHelper' \
//	  ./internal/e2e/realclaude/
//
// Every one must report PASS — not SKIP, not "no tests to run" — on a machine
// with no claude and no credentials. Read the count of tests that executed, never
// the exit code: this package is behind the e2e_realclaude tag, `make check`
// never compiles it, and the suite exits 0 both on a build failure and on a full
// credentials skip. `make preship` is the gate that proves the package builds.

import (
	"encoding/json"
	"reflect"
	"testing"
)

// --- the record ---------------------------------------------------------------

// askQuestionFixtureRecord is the durable artifact the AskUserQuestion capture
// commits: the claude version the call was observed under, the tool's name, and
// the call's input verbatim. IT IS THE CALL, NOT THE SESSION.
//
// FOUR FIELDS, AND THERE MUST NEVER BE A FIFTH WITHOUT AN EXPLICIT DECISION.
// There is deliberately no argv, no env map, no stderr capture, no surrounding
// stream, no tool_use_id, no session id and no timestamp. Each of those is a
// string #1941's deny-scan would have to reason about, and none of them is the
// call. TestAskQuestionFullRecord_PinsTheFourFieldsAndTheSlugShape's hand-written
// name literal is what makes a fifth field redden rather than arrive unnoticed.
//
// CLAUDE_VERSION_SLUG IS NOT THE FAMILY'S CLAUDE_VERSION TAG, AND THE DIVERGENCE
// IS DELIBERATE. initControlFixtureRecord and setModeFixtureRecord tag the
// version TOKEN — "2.1.239" — as `claude_version`. This field holds the SLUG,
// versionSlug applied to that token, which is a different quantity: reusing the
// family's tag would hand a downstream decoder a value the family's other
// captures do not carry, under a name that says they do. Do not harmonise it.
// ClaudeVersionRaw beside it does match the family's spelling, because it is the
// same quantity — the whole `claude --version` line.
//
// TOOLINPUT IS json.RawMessage AND NOTHING ELSE. A decoded-and-re-marshalled
// input is not the bytes claude sent: encoding/json emits map keys in SORTED
// order, so a map[string]any field silently rewrites the call's own key ordering
// and the artifact stops being a recording of the call. That mutant is invisible
// to the round-trip row over this field — both sides would then be maps and
// reflect.DeepEqual over maps is order-blind — which is why
// TestAskQuestionRecord_RoundTripsEveryFieldAndPreservesTheInputBytes asserts
// over the WIRE BYTES as well as over the row.
//
// No `omitempty` on any tag, matching every other record in this family, so every
// key stays visible in a committed artifact a human reads.
type askQuestionFixtureRecord struct {
	ClaudeVersionRaw  string          `json:"claude_version_raw"`
	ClaudeVersionSlug string          `json:"claude_version_slug"`
	ToolName          string          `json:"tool_name"`
	ToolInput         json.RawMessage `json:"tool_input"`
}

// askQuestionFixtureInput is the fixture's tool input: one compact line shaped
// like an AskUserQuestion call. It is SYNTHETIC AND NOT A CAPTURE — this package
// records captures under testdata/, and nothing in this file reads or writes that
// directory. Nothing in this repo holds a real AskUserQuestion tool_use block, so
// the shape is modelled on the tool's documented one.
//
// It is a package-level const rather than an inline literal inside the fixture
// for a reason that is not tidiness: it is what makes the map[string]any mutant
// COMPILE. An untyped string constant survives unused when the fixture switches
// to a Go map literal, so that mutant reaches the assertions instead of failing
// to build, and the wire-order subtest below is what reddens on it.
//
// Two constraints bind its content and neither is decoration:
//
//  1. ITS KEYS ARE NOT IN SORTED ORDER, AT TWO LEVELS. The question object's keys
//     run header, question, multiSelect, options — sorted would be header,
//     multiSelect, options, question. Each option object's run label, description
//     — sorted would be description, label. A value re-marshalled from a generic
//     decode reproduces NEITHER ordering, which is the whole discriminating power
//     of the wire-order property; that property is only as strong as this
//     literal, and the vacuity control beside it asserts the divergence rather
//     than assuming it. If you arrived here after "tidying" these keys into
//     alphabetical order, the control is what you reddened.
//
//  2. NO '<', '>' OR '&' ANYWHERE IN IT. encoding/json HTML-escapes all three
//     inside a json.RawMessage and the escape survives compaction, so either
//     character would introduce a SECOND, unrelated difference between the
//     fixture bytes and a re-marshal and muddy the one these tests are about.
//     #1662 states the same constraint for its own raw field.
//
// The -FIXTURE markers inside the string values are #1701's discipline: nobody
// must be able to mistake this literal for a recording of a real call.
const askQuestionFixtureInput = `{"questions":[{"header":"Scope-FIXTURE","question":"Which package should this synthetic -FIXTURE change land in?","multiSelect":false,"options":[{"label":"internal/sessions-FIXTURE","description":"the session pool and its rotation"},{"label":"internal/streamsup-FIXTURE","description":"the stream-json supervisor"}]}]}`

// --- the fully-populated fixture ----------------------------------------------

// askQuestionFullRecord returns a record in which every one of the four fields
// carries a non-zero value, and every pair of same-typed fields carries a
// DISTINCT one. Both properties are asserted below rather than trusted.
//
// It returns a FRESH POINTER per call and is never a package-level var, for
// initControlFullRecord's reason: a later cap or mutation test running parallel
// subtests over the returned record turns a shared var into a -race data race
// discovered two tickets away from the line that caused it.
//
// Three literal choices are load-bearing:
//
//  1. ClaudeVersionRaw is shaped like a real `claude --version` LINE, carrying a
//     space, parens and uppercase, so versionSlug rewrites it. That is one half
//     of the slug-shape property: a literal "tidied" into a bare clean version
//     would make #1944's "named exactly what the namer mints" assertion 0-red on
//     that column, which is #1722's measured lesson on the sibling family. It is
//     a claim about THIS FILE'S OWN LITERAL and about nothing else — it is not an
//     assertion about what `claude --version` emits, and it must not be sourced
//     from captureClaudeVersion, which execs. See this file's finOfflineExecBans
//     entry.
//
//  2. ClaudeVersionSlug is entirely inside versionSlug's [a-z0-9._-] class, so
//     re-slugging it is a FIXED POINT — the other half of that property, and the
//     sole red for the real mutant: a fixture populating both version fields from
//     the same raw line. It is deliberately NOT derived here from the raw literal
//     above: splitting a version token out of that line is captureClaudeVersion's
//     job and that helper execs, so re-deriving it here would be new logic with
//     no consumer in this slice. The limit that leaves is stated in the property
//     below rather than papered over.
//
//  3. ToolName is the REAL tool name, and it is capitalised, so it does not
//     survive versionSlug. That is what leaves #1944's namer assertion non-vacuous
//     on the tool-name column with no synthetic literal needed — which matters
//     precisely because ClaudeVersionSlug, by AC 1's construction, is slug-clean
//     and gives that assertion nothing.
//
// The three strings are pairwise DISTINCT, which the distinctness property below
// requires and asserts rather than trusts.
func askQuestionFullRecord() *askQuestionFixtureRecord {
	return &askQuestionFixtureRecord{
		ClaudeVersionRaw:  "2.1.239-FIXTURE (Claude Code)",
		ClaudeVersionSlug: "2.1.239-fixture",
		ToolName:          "AskUserQuestion",
		ToolInput:         json.RawMessage(askQuestionFixtureInput),
	}
}

// --- the field listing --------------------------------------------------------

// askQuestionFixtureField is one row of the listing: a JSON tag and the value the
// field carrying it holds.
//
// A LOCAL ROW TYPE, NOT initControlFixtureField. The pair is trivial to restate,
// and reusing the sibling family's type would drag a reader toward
// compactInitControlRawRows — whose type-scoped selection and touched-name return
// exist because that record has THREE raw-JSON fields of two Go types. This one
// has one. The duplication is four lines and buys file independence, which is the
// property this file's finOfflineExecBans entry exists to protect.
type askQuestionFixtureField struct {
	name  string
	value any
}

// askQuestionFixtureFields lists rec's four fields once, in declaration order.
// The round trip below applies it to both the marshalled record and its decode
// and zips the two rather than restating the fields; #1941 and #1942 reach it the
// same way.
//
// THE ROWS ARE HAND-WRITTEN ON PURPOSE. Do not regenerate them by walking the
// struct with reflection, for initControlFixtureFields' two reasons — and the
// second is the real one:
//
//   - A reflection-driven listing can never be missing a row, so the length
//     assertion below becomes tautological, which is the defect this family
//     spends most of its comment budget avoiding.
//   - These names are a SECOND, INDEPENDENT COPY of the JSON tags. The one defect
//     a symmetric struct round trip structurally cannot catch is a MISSPELLED
//     TAG: #1662's code review established that even a UNIQUE wrong tag
//     round-trips green, since only a colliding tag makes encoding/json drop
//     both. These tags are not read by humans alone — #1941's writer and the
//     offline read that follows the capture decode them — so a misspelling here
//     is a real defect two tickets downstream. The instrument that catches it is
//     a reviewer diffing this listing's names against the struct's tags, and a
//     reflection listing reads each name off the very tag it was meant to check.
//
// For the same reason there is deliberately no reflection check asserting these
// names equal the struct's tags: it would make disagreement the only thing anyone
// checks, which is what the second copy exists to avoid.
func askQuestionFixtureFields(rec *askQuestionFixtureRecord) []askQuestionFixtureField {
	return []askQuestionFixtureField{
		{"claude_version_raw", rec.ClaudeVersionRaw},
		{"claude_version_slug", rec.ClaudeVersionSlug},
		{"tool_name", rec.ToolName},
		{"tool_input", rec.ToolInput},
	}
}

// --- the properties -----------------------------------------------------------

// TestAskQuestionFullRecord_PinsTheFourFieldsAndTheSlugShape is #1943's AC 1 and
// the fixture half of AC 2: the listing covers exactly the record's four fields
// under exactly those four names, every listed field carries a non-zero value,
// every pair of same-typed fields carries a distinct one, and the raw version
// field is not a slug while the slug field is.
//
// It reads nothing off disk and spawns nothing. The parent computes the record
// and the listing once; the subtests only read them, so the shared slice is safe
// under t.Parallel().
func TestAskQuestionFullRecord_PinsTheFourFieldsAndTheSlugShape(t *testing.T) {
	t.Parallel()

	rec := askQuestionFullRecord()
	rows := askQuestionFixtureFields(rec)

	t.Run("the listing is exactly the record's four fields", func(t *testing.T) {
		t.Parallel()

		// Over the ZERO VALUE, deliberately: both claims here are about the TYPE's
		// field set and the fixture's values play no part in them. #1723's measured
		// lesson is the reason — a listing subtest that indexes a fixture's values
		// panics where a sibling vacuity check was meant to report cleanly, and a
		// panic takes the whole test binary down.
		typeRows := askQuestionFixtureFields(&askQuestionFixtureRecord{})

		// Two assertions with SEPARATE failure messages, because they catch
		// different mutants and neither catches the other's.
		if want := reflect.TypeOf(askQuestionFixtureRecord{}).NumField(); len(typeRows) != want {
			t.Errorf("#1943: the listing has %d rows, want %d — one per record field; a field "+
				"added later with no row goes silently unchecked by both properties below "+
				"and by the round trip, which reads its rows from here", len(typeRows), want)
		}

		// AC 1's cap, and a deliberate THIRD hand-written copy of the tag set with a
		// different job from the listing's: the listing exists to be zipped, this
		// literal exists to say "nothing else". It catches a fifth field that
		// arrives WITH a row — which the length check cannot, since both counts move
		// together — and it catches a rename, a duplicated name and an omitted one
		// in the same stroke, so no separate uniqueness pass is needed.
		//
		// The cap is not housekeeping: #1941's writer refuses by scanning this
		// record's marshalled bytes, so every field added here widens the surface
		// that refusal has to cover.
		names := make([]string, 0, len(typeRows))
		for _, row := range typeRows {
			names = append(names, row.name)
		}
		wantNames := []string{"claude_version_raw", "claude_version_slug", "tool_name", "tool_input"}
		if !reflect.DeepEqual(names, wantNames) {
			t.Errorf("#1943: the listing names %v, want exactly %v in that order; this record is "+
				"the CALL — tool name, input, version — and not the session, and its field set "+
				"is the leak surface #1941's deny-scan has to cover. A fifth field is a "+
				"decision, not an edit: #1688's whole-stream capture needed three follow-up "+
				"tickets to redact what it had swallowed", names, wantNames)
		}
	})

	t.Run("every listed field carries a non-zero value", func(t *testing.T) {
		t.Parallel()

		// fixtureFieldNonZero is #1662's and is called rather than rewritten: its
		// kind switch is the point, since a []string{} is !IsZero() and proves
		// nothing, so container kinds are judged on length. json.RawMessage is a
		// slice kind, so an empty tool_input is caught here rather than sailing
		// through the round trip as nil-against-nil.
		for _, row := range rows {
			if !fixtureFieldNonZero(row.value) {
				t.Errorf("#1943: the fixture leaves %s at its zero value; a zero-valued field "+
					"round-trips under ANY tag arrangement, so the round trip's row over it "+
					"settles nothing", row.name)
			}
		}
	})

	t.Run("same-typed fields carry distinct values", func(t *testing.T) {
		t.Parallel()

		// NO reflect.Bool skip here, unlike #1701's version of this property. This
		// record has no bool field, so the `continue` would be dead code — stated
		// rather than carried, so that harmonisation against the sibling does not
		// restore it.
		//
		// reflect.TypeOf distinguishes named types, so json.RawMessage is its own
		// group and never compares against the three strings; it has no same-typed
		// sibling here and this property gives that row nothing.
		for i := range rows {
			for j := i + 1; j < len(rows); j++ {
				if reflect.TypeOf(rows[i].value) != reflect.TypeOf(rows[j].value) {
					continue
				}
				if reflect.DeepEqual(rows[i].value, rows[j].value) {
					// Printing the shared value is safe HERE AND ONLY HERE: every
					// value in this file is a synthetic literal. The ticket that fills
					// this record from a live child must not inherit the pattern — and
					// nothing anywhere may %+v the record.
					t.Errorf("#1943: %s and %s carry the same value %v; two same-typed fields "+
						"carrying one value make a record with their two JSON tags swapped "+
						"round-trip green, so a listing row wired to the wrong field passes "+
						"by reading an identical one",
						rows[i].name, rows[j].name, rows[i].value)
				}
			}
		}
	})

	t.Run("the raw version is not a slug and the slug field is", func(t *testing.T) {
		t.Parallel()

		// Two checks, deliberately not folded into a loop: each names the mutant its
		// own column goes dead against, and those two sets are different.
		//
		// THE LIMIT, STATED RATHER THAN TESTED: nothing here asserts that the slug is
		// versionSlug of THIS raw line's leading token. Splitting that token out of
		// the line is captureClaudeVersion's job and that helper execs, so
		// re-deriving it here would be new logic with no consumer in this slice.
		// #1941's fill site is where the two fields are minted from one call and
		// where that coupling becomes checkable.
		if got := versionSlug(rec.ClaudeVersionRaw); got == rec.ClaudeVersionRaw {
			t.Errorf("#1943: claude_version_raw %q survives versionSlug unchanged (slugs to %q); "+
				"a real `claude --version` line carries a space and parens, and a literal "+
				"tidied into a bare clean version makes #1944's \"named exactly what the "+
				"namer mints\" assertion 0-RED on this column — #1722's measured lesson on "+
				"the sibling family. If you arrived here after tidying this literal, that is "+
				"what you emptied", rec.ClaudeVersionRaw, got)
		}

		if got := versionSlug(rec.ClaudeVersionSlug); got != rec.ClaudeVersionSlug {
			t.Errorf("#1943: claude_version_slug %q does not survive versionSlug unchanged (slugs "+
				"to %q); this field holds a SLUG, not a version token, so re-slugging it must "+
				"be a fixed point. This reddens against the real mutant: a fixture populating "+
				"both version fields from the same raw line",
				rec.ClaudeVersionSlug, got)
		}
	})
}

// --- the round trip -----------------------------------------------------------

// TestAskQuestionRecord_RoundTripsEveryFieldAndPreservesTheInputBytes is #1943's
// AC 2 and AC 3: every field of the fully-populated record survives
// marshal-then-read-back through the single listing, and the call's input reaches
// the wire as the BYTES THAT WERE WRITTEN rather than as a value re-marshalled
// from a decoded object.
//
// The subtests share one marshalled blob and are deliberately NOT parallel: the
// vacuity control must run before the property it guards.
//
// It does not restate the fixture's properties. Non-zero and same-typed
// distinctness ship in TestAskQuestionFullRecord_PinsTheFourFieldsAndTheSlugShape
// and are preconditions this test consumes rather than assertions it repeats.
//
// json.MarshalIndent is the form the family's writers use, and it is the form
// that puts indentation INSIDE the raw message — so the compaction below is
// load-bearing rather than decorative. Both json.Marshal and json.MarshalIndent
// rewrite an embedded raw message (MarshalIndent indents inside it, plain Marshal
// compacts it), so a naive byte comparison after a round trip is false for any
// input carrying insignificant whitespace and a PERFECTLY CORRECT record reddens.
// Compaction preserves key ORDER — json.Compact only strips insignificant
// whitespace — so it does not blunt the wire-order property.
func TestAskQuestionRecord_RoundTripsEveryFieldAndPreservesTheInputBytes(t *testing.T) {
	t.Parallel()

	rec := askQuestionFullRecord()
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		t.Fatalf("#1943: marshal the AskUserQuestion fixture record: %v; a malformed "+
			"askQuestionFixtureInput leaves every assertion below comparing zero values, "+
			"which is green", err)
	}
	var got askQuestionFixtureRecord
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("#1943: decode the AskUserQuestion fixture record: %v", err)
	}

	// Normalised at this single site rather than through a compactInitControlRawRows
	// clone, so the blinding is visibly scoped to the ONE raw field this record has.
	// That helper's type switch and touched-name canary exist because its record has
	// three raw fields of two Go types; with one field there is nothing for a widened
	// normaliser to blind and nothing for a narrowed one to miss that the row itself
	// would not report. If a second raw-JSON field is ever added here, that helper's
	// type-scoped shape is the thing to adopt THEN, not now.
	wantRec := *rec
	wantRec.ToolInput = compactRawMessages(t, []json.RawMessage{wantRec.ToolInput})[0]
	haveRec := got
	haveRec.ToolInput = compactRawMessages(t, []json.RawMessage{haveRec.ToolInput})[0]

	// Both sides go through the SAME listing. There is deliberately no length guard
	// before the zip: both slices come from one function over one struct type, so a
	// divergence is impossible and the guard could never redden.
	want := askQuestionFixtureFields(&wantRec)
	have := askQuestionFixtureFields(&haveRec)

	t.Run("every field survives the round trip", func(t *testing.T) {
		for i := range want {
			if !reflect.DeepEqual(want[i].value, have[i].value) {
				// Printing both values is safe HERE AND ONLY HERE: every value in this
				// record is a synthetic literal, and the diagnostic is worth more than
				// protecting one. The ticket that fills this record from a live child
				// must not inherit the pattern.
				t.Errorf("#1943: %s did not survive the round trip: wrote %v, read back %v; "+
					"this record is the capture's only durable trace",
					want[i].name, want[i].value, have[i].value)
			}
		}
	})

	t.Run("a re-marshalled decode would not reproduce the fixture's input", func(t *testing.T) {
		// THE VACUITY CONTROL, and it runs before the property it guards. If a later
		// editor "tidies" askQuestionFixtureInput's keys into sorted order, this
		// reddens and says so, instead of the next subtest going quietly 0-red.
		var decoded any
		if err := json.Unmarshal([]byte(askQuestionFixtureInput), &decoded); err != nil {
			t.Fatalf("#1943: decode askQuestionFixtureInput generically: %v", err)
		}
		remarshalled, err := json.Marshal(decoded)
		if err != nil {
			t.Fatalf("#1943: re-marshal the generically decoded askQuestionFixtureInput: %v", err)
		}
		compacted := compactRawMessages(t, []json.RawMessage{json.RawMessage(askQuestionFixtureInput)})[0]

		if string(remarshalled) == string(compacted) {
			t.Errorf("#1943: askQuestionFixtureInput is byte-identical to its own generic decode "+
				"re-marshalled (%s), so the wire-order property below cannot discriminate a "+
				"record that carries the call's bytes from one that carries a re-marshalled "+
				"map. encoding/json emits map keys in SORTED order, so this literal's keys "+
				"must not be sorted — the question object runs header, question, "+
				"multiSelect, options, and each option runs label, description",
				remarshalled)
		}
	})

	t.Run("the input reaches the wire in the fixture's own key order", func(t *testing.T) {
		// AC 3, and it is written over the WIRE BYTES rather than over the decoded
		// record on purpose. The mutant it exists for is `ToolInput map[string]any`
		// instead of json.RawMessage: that mutant is INVISIBLE to the round-trip row
		// above — both sides would then be maps and reflect.DeepEqual over maps is
		// order-blind — and it COMPILES, because askQuestionFixtureInput is an untyped
		// string constant that survives unused when the fixture switches to a Go map
		// literal. Under it, MarshalIndent emits sorted keys and this comparison goes
		// red.
		var envelope map[string]json.RawMessage
		if err := json.Unmarshal(data, &envelope); err != nil {
			t.Fatalf("#1943: decode the marshalled record into an envelope: %v", err)
		}
		raw, ok := envelope["tool_input"]
		if !ok {
			t.Fatalf("#1943: the marshalled record carries no tool_input key; the record's " +
				"whole purpose is to carry the call's input verbatim")
		}
		onWire := compactRawMessages(t, []json.RawMessage{raw})[0]
		wantBytes := compactRawMessages(t, []json.RawMessage{json.RawMessage(askQuestionFixtureInput)})[0]

		// Compared as strings so the failure prints both blobs; safe here for the
		// reason the distinctness subtest gives.
		if string(onWire) != string(wantBytes) {
			t.Errorf("#1943: tool_input reached the wire as\n%s\nwant\n%s\n— the record must "+
				"carry the call's own bytes, not a value re-marshalled from a decoded "+
				"object. encoding/json emits map keys in sorted order, so a map-typed "+
				"tool_input silently rewrites the call's key ordering and the artifact "+
				"stops being a recording of the call", onWire, wantBytes)
		}
	})
}
