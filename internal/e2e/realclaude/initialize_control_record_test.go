//go:build e2e_realclaude

package realclaude

// #1701 — the record half of the `initialize` control-request capture: the JSON
// contract three downstream slices decode against, one fully-populated instance
// of it, and the single field listing #1702 applies to BOTH sides of its round
// trip rather than restating the fields.
//
// # What this file deliberately is not
//
// It holds no writer — that is #1702, which reads this record and this listing
// and adds nothing to either. No cap proof: that is #1700, which mutates the
// record this file's fixture function returns. No live run: that is #1688, which
// spends the tokens and fills these fields from a real child. This file builds a
// record and asserts on its own literals. It settles with no claude binary and no
// credentials, and it performs no I/O in either direction.
//
// # Why the contract is fixed before the run that measures it
//
// The daemon wants to publish claude's model list — identifiers, display names,
// reasoning-effort levels — to connected clients. Measured by hand on 2026-08-21
// against claude 2.1.220, OUTSIDE this repo: a control_request with subtype
// "initialize", written on the child's already-held-open stdin, comes back with a
// models array. #1688 captures it, #1690 decodes it, #1692's fake replays it —
// three slices reading one shape that nothing in the tree records. Fixing the
// shape here is what lets the two slices that are not the capture be built
// against a shape that exists.
//
// # What the two properties buy, and what they cannot
//
// Nothing round-trips in this file, so non-zero and distinctness here are
// PRECONDITIONS on the fixture rather than findings about a writer. They are
// asserted so that #1702's and #1700's rows cannot silently degenerate:
//
//   - A zero-valued field round-trips green under ANY tag arrangement, so a row
//     over one settles nothing.
//   - Two same-typed fields carrying the same value make a writer that swapped
//     their two JSON tags round-trip green.
//
// Those two mutants are the whole claim. This file does NOT catch "a field
// decoded from the wrong tag": #1662's code review established that a symmetric
// struct round trip catches only a COLLIDING tag, because encoding/json drops
// both — a unique wrong tag round-trips green even in #1702, and here nothing
// round-trips at all.
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
// committed fixtures without naming packageDir at all.
// TestFinOfflineFilesReachNoExecHelper enforces all of it over this file's AST
// rather than over this paragraph, and the entry's own doc comment says why
// captureClaudeVersion — which returns exactly this record's two version fields
// at once — is the one name a developer here is most likely to reach for.
//
//	go test -tags e2e_realclaude -race -count=1 -v \
//	  -run 'TestInitControlFullRecord_|TestFinOfflineFilesReachNoExecHelper' \
//	  ./internal/e2e/realclaude/
//
// Both must report PASS — not SKIP, not "no tests to run" — on a machine with no
// claude and no credentials. Read the count of tests that executed, never the
// exit code: this package is behind the e2e_realclaude tag, `make check` never
// compiles it, and the suite exits 0 both on a build failure and on a full
// credentials skip.

import (
	"encoding/json"
	"reflect"
	"testing"
)

// --- the record ---------------------------------------------------------------

// initControlFixtureRecord is the durable artifact #1688's live run commits, and
// the JSON contract #1690's decoder and #1692's fake read. Eighteen of its
// twenty-two fields are setModeFixtureRecord's, carrying that record's JSON tags
// and Go types unchanged: three slices decode this shape, and a gratuitous
// divergence in a shared field is a defect that surfaces two tickets away.
//
// There is deliberately no `env` field, and there must never be one — the
// constraint and its reasoning are inherited from setModeFixtureRecord. The
// credential reaches the child through the environment while the argv carries
// none, so recording argv is safe and recording env would not be.
//
// ControlResponseRequestIDMatched is the one DERIVED field: its value is a
// function of ControlRequestID and ControlResponses, both of which are recorded
// verbatim beside it. It is recorded rather than recomputed because it states
// what the capturing run actually observed at the moment it observed it, and it
// sits with the response fields rather than in the instrument-health block on
// purpose — a request-id mismatch is a finding about claude's protocol, not a
// fault in the harness. A reader who disagrees with the flag treats the verbatim
// bytes as authoritative; the flag is a claim about the run, not a second copy of
// the data.
//
// ModelsPresent, ModelsCount and ModelsEntryFields are POPULATED, never computed.
// #1688's live run fills them from a real response; a summarizer built here would
// have no response to summarize.
//
// ScannerError is load-bearing for #1688, which caps its reader per line: an
// over-long line must surface HERE rather than truncating silently.
//
// NOTHING IN THIS FILE CAPS ANYTHING. StderrCapture is bounded by #1702's writer
// through capFixtureCapture and proven by #1700; this type carries no bound and
// must not be read as implying one. The three verbatim-bytes fields
// (ControlRequestSent, ControlResponses, StdoutEvents) are uncapped by design,
// for the reason poolRevokeFixtureRecord's doc gives for its own control
// responses: capping structured evidence destroys the artifact.
type initControlFixtureRecord struct {
	ClaudeVersionRaw string `json:"claude_version_raw"`
	ClaudeVersion    string `json:"claude_version"`

	Argv    []string `json:"argv"`
	Prompts []string `json:"prompts"`

	ControlRequestID                string            `json:"control_request_id"`
	ControlRequestSent              json.RawMessage   `json:"control_request_sent"`
	ControlResponses                []json.RawMessage `json:"control_responses"`
	ControlResponseSubtype          string            `json:"control_response_subtype"`
	ControlResponseRequestIDMatched bool              `json:"control_response_request_id_matched"`

	ModelsPresent     bool     `json:"models_present"`
	ModelsCount       int      `json:"models_count"`
	ModelsEntryFields []string `json:"models_entry_fields"`

	StdoutEvents     []json.RawMessage `json:"stdout_events"`
	NonJSONLineCount int               `json:"non_json_line_count"`
	TurnBoundaries   []int             `json:"turn_boundaries"`

	StdinWriteErrors       []string `json:"stdin_write_errors"`
	StderrCapture          string   `json:"stderr_capture"`
	ExitCode               int      `json:"exit_code"`
	WaitError              string   `json:"wait_error"`
	ContextDeadlineTripped bool     `json:"context_deadline_tripped"`
	DurationMs             int64    `json:"duration_ms"`
	ScannerError           string   `json:"scanner_error"`
}

// --- the fully-populated fixture -------------------------------------------------

// initControlFullRecord returns a record in which every one of the twenty-two
// fields carries a non-zero value, and every pair of same-typed non-bool fields
// carries a DISTINCT one. Both properties are asserted below rather than trusted.
//
// It returns a FRESH POINTER per call and is never a package-level var: #1700's
// cap test mutates the returned record across parallel subtests exactly as
// TestPoolRevokeFixture_WriterCapsChildOutputCapture does with
// poolRevokeFullRecord, and a shared var would be a -race data race discovered
// two tickets away from the line that caused it.
//
// THIS IS NOT A COHERENT CAPTURE, and it must not be "fixed" into one. The
// non-zero property forces every instrument-health field to report trouble at the
// same time — a non-zero exit, a wait error, a scanner error, a tripped deadline,
// a stdin write error — alongside a successful control_response subtype, and all
// three bools are true for that same reason. A reader who takes this for a
// recording will reconcile it and empty half the properties doing it.
//
// Three literal choices are load-bearing:
//
//  1. ClaudeVersion must NOT survive versionSlug unchanged. That helper
//     lowercases, rewrites runs outside [a-z0-9._-] to _, then clamps at 32, so a
//     realistic "2.1.220" is already slug-clean and initControlFixtureName over it
//     is byte-identical to what a writer formatting its own
//     "initialize_control_v%s.json" produces. #1702 asserts its entry is "named
//     exactly what the namer mints", and against a slug-clean token that
//     assertion is 0-red under precisely the self-formatting writer #1696's lock
//     exists to close — with #1696's own test still green. One uppercase run is
//     enough, since the slug lowercases first, and the word FIXTURE is itself the
//     signal that stops a later reader "correcting" the literal to a clean
//     version. ClaudeVersionRaw then has to differ from it too, which the
//     distinctness property requires of those two strings anyway. This is a claim
//     about this file's OWN literal and about nothing else; it is not an
//     assertion about what `claude --version` emits, and it must not be sourced
//     from captureClaudeVersion — see this file's finOfflineExecBans entry.
//
//  2. No '<', '>' or '&' in ANY of the three embedded raw-JSON literals.
//     encoding/json escapes all three inside a json.RawMessage and the escape
//     survives compaction, so #1702's read-back row for such a field reddens for
//     a perfectly correct writer. #1662 states this for its single raw field;
//     following the sibling's types this record has three. StdoutEvents is the
//     easiest to get wrong, because a realistic assistant-text event is where an
//     '&' or a '<' actually turns up. A plain string field carrying the same
//     characters round-trips unchanged — the constraint is specific to the
//     raw-JSON fields.
//
//  3. ControlResponses and StdoutEvents must carry DIFFERENT content. Both are
//     []json.RawMessage, so distinctness binds them — and in a real capture the
//     control response arrives ON stdout, which is exactly the literal a
//     developer repeats into both. The resolution is two literals, not a weakened
//     assertion.
//
// Where self-consistency costs nothing it is kept: ControlRequestID, the
// request_id inside ControlRequestSent and the one inside ControlResponses all
// agree, which is what ControlResponseRequestIDMatched claims; ModelsCount and
// ModelsEntryFields match the envelope's models array.
func initControlFullRecord() *initControlFixtureRecord {
	return &initControlFixtureRecord{
		ClaudeVersionRaw: "2.1.220-FIXTURE (Claude Code)",
		ClaudeVersion:    "2.1.220-FIXTURE",

		Argv:    []string{"claude", "--input-format", "stream-json", "--output-format", "stream-json", "--verbose"},
		Prompts: []string{"probe turn one", "probe turn two"},

		ControlRequestID:   "req_init_1",
		ControlRequestSent: json.RawMessage(`{"type":"control_request","request_id":"req_init_1","request":{"subtype":"initialize"}}`),
		ControlResponses: []json.RawMessage{
			json.RawMessage(`{"type":"control_response","response":{"subtype":"success","request_id":"req_init_1","models":[{"model":"claude-opus-5","displayName":"Opus 5","supportedReasoningEfforts":["low","medium","high"]},{"model":"claude-haiku-4-5","displayName":"Haiku 4.5","supportedReasoningEfforts":["low"]}]}}`),
		},
		ControlResponseSubtype:          "success",
		ControlResponseRequestIDMatched: true,

		ModelsPresent:     true,
		ModelsCount:       2,
		ModelsEntryFields: []string{"model", "displayName", "supportedReasoningEfforts"},

		StdoutEvents: []json.RawMessage{
			json.RawMessage(`{"type":"system","subtype":"init","session_id":"sess_init_fixture","model":"claude-opus-5"}`),
			json.RawMessage(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"listing the models now"}]}}`),
		},
		NonJSONLineCount: 3,
		TurnBoundaries:   []int{0, 7},

		StdinWriteErrors:       []string{"write |1: broken pipe"},
		StderrCapture:          "child stderr: the free-text stream this field absorbs",
		ExitCode:               1,
		WaitError:              "signal: killed",
		ContextDeadlineTripped: true,
		DurationMs:             1842,
		ScannerError:           "bufio.Scanner: token too long",
	}
}

// --- the field listing -------------------------------------------------------

// initControlFixtureField is one row of the listing: a JSON tag and the value the
// field carrying it holds.
type initControlFixtureField struct {
	name  string
	value any
}

// initControlFixtureFields lists rec's twenty-two fields once, in declaration
// order. #1702 applies it to both the written record and its decode and zips the
// two rather than restating the fields; #1700 reaches it the same way.
//
// THE ROWS ARE HAND-WRITTEN ON PURPOSE. Do not regenerate them by walking the
// struct with reflection, for two reasons and the second is the real one:
//
//   - A reflection-driven listing can never be missing a row, so the length
//     assertion below becomes tautological — a vacuous assertion, which is the
//     defect this family spends most of its comment budget avoiding.
//   - These names are a SECOND, INDEPENDENT COPY of the JSON tags. The one defect
//     a symmetric struct round trip structurally cannot catch is a misspelled tag
//     (#1662's header says so; its code review sharpened it — even a UNIQUE wrong
//     tag round-trips green, since only a colliding tag makes encoding/json drop
//     both). These tags are not read by humans alone: #1690's decoder and #1692's
//     fake read them, so a misspelling here is a real defect two tickets
//     downstream. The instrument that catches it is a reviewer diffing this
//     listing's names against the struct's tags — and a reflection listing reads
//     the name off the tag, so a misspelled tag produces a matching misspelled
//     row and that instrument is gone.
//
// For the same reason there is deliberately no reflection check asserting these
// names equal the struct's tags: it would make disagreement the only thing anyone
// checks, which is what the second copy exists to avoid.
func initControlFixtureFields(rec *initControlFixtureRecord) []initControlFixtureField {
	return []initControlFixtureField{
		{"claude_version_raw", rec.ClaudeVersionRaw},
		{"claude_version", rec.ClaudeVersion},
		{"argv", rec.Argv},
		{"prompts", rec.Prompts},
		{"control_request_id", rec.ControlRequestID},
		{"control_request_sent", rec.ControlRequestSent},
		{"control_responses", rec.ControlResponses},
		{"control_response_subtype", rec.ControlResponseSubtype},
		{"control_response_request_id_matched", rec.ControlResponseRequestIDMatched},
		{"models_present", rec.ModelsPresent},
		{"models_count", rec.ModelsCount},
		{"models_entry_fields", rec.ModelsEntryFields},
		{"stdout_events", rec.StdoutEvents},
		{"non_json_line_count", rec.NonJSONLineCount},
		{"turn_boundaries", rec.TurnBoundaries},
		{"stdin_write_errors", rec.StdinWriteErrors},
		{"stderr_capture", rec.StderrCapture},
		{"exit_code", rec.ExitCode},
		{"wait_error", rec.WaitError},
		{"context_deadline_tripped", rec.ContextDeadlineTripped},
		{"duration_ms", rec.DurationMs},
		{"scanner_error", rec.ScannerError},
	}
}

// --- the properties ----------------------------------------------------------

// TestInitControlFullRecord_PinsEveryFieldAndTheSluggableVersionToken is #1701
// whole: the listing covers every field of the record exactly once, every listed
// field carries a non-zero value, every pair of same-typed non-bool fields
// carries a distinct one, and the version token does not survive slugging.
//
// It reads nothing off disk and spawns nothing. The parent computes the record
// and the listing once; the subtests only read them, so the shared slice is safe
// under t.Parallel().
func TestInitControlFullRecord_PinsEveryFieldAndTheSluggableVersionToken(t *testing.T) {
	t.Parallel()

	rec := initControlFullRecord()
	rows := initControlFixtureFields(rec)

	t.Run("the listing covers every field exactly once", func(t *testing.T) {
		t.Parallel()

		if want := reflect.TypeOf(initControlFixtureRecord{}).NumField(); len(rows) != want {
			t.Errorf("#1701: the listing has %d rows, want %d — one per record field; a field "+
				"added later with no row goes silently unchecked by both properties below "+
				"and by #1702's round trip, which reads its rows from here", len(rows), want)
		}

		// Length alone is green against a listing that names one field twice and
		// omits another, which is exactly the outcome the count exists to
		// prevent. Both halves are needed; neither catches the other's mutant.
		seen := make(map[string]bool, len(rows))
		for _, row := range rows {
			if seen[row.name] {
				t.Errorf("#1701: the listing names %q twice, so it holds the right number of "+
					"rows while some other field has none at all", row.name)
			}
			seen[row.name] = true
		}
	})

	t.Run("every listed field carries a non-zero value", func(t *testing.T) {
		t.Parallel()

		// fixtureFieldNonZero is #1662's and is called rather than rewritten: its
		// kind switch is the point, since a []string{} is !IsZero() and proves
		// nothing, so container kinds are judged on length.
		for _, row := range rows {
			if !fixtureFieldNonZero(row.value) {
				t.Errorf("#1701: the fixture leaves %s at its zero value; a zero-valued field "+
					"round-trips under ANY tag arrangement, so #1702's and #1700's rows over "+
					"it settle nothing", row.name)
			}
		}
	})

	t.Run("same-typed non-bool fields carry distinct values", func(t *testing.T) {
		t.Parallel()

		// Scoped to non-bool fields because booleans cannot carry distinct
		// non-zero values — there is only one — so all three of this record's are
		// true and a swapped pair of them stays invisible here. That is stated
		// rather than written as an assertion which cannot hold. What still
		// catches a tag COLLISION between two bools is the non-zero subtest
		// above: encoding/json drops both and they read back false.
		//
		// reflect.TypeOf distinguishes named types, so json.RawMessage,
		// []json.RawMessage and []string are three groups and never compare
		// against each other.
		for i := range rows {
			if reflect.ValueOf(rows[i].value).Kind() == reflect.Bool {
				continue
			}
			for j := i + 1; j < len(rows); j++ {
				if reflect.TypeOf(rows[i].value) != reflect.TypeOf(rows[j].value) {
					continue
				}
				if reflect.DeepEqual(rows[i].value, rows[j].value) {
					// Printing the shared value is safe here and only here: every
					// value in this file is a synthetic literal. #1688 fills this
					// same record from a live child, so it must not inherit the
					// pattern — and nothing anywhere may %+v the record.
					t.Errorf("#1701: %s and %s carry the same value %v; two same-typed fields "+
						"carrying one value make a writer that swapped their two JSON tags "+
						"round-trip green in #1702",
						rows[i].name, rows[j].name, rows[i].value)
				}
			}
		}
	})

	t.Run("the version token does not survive slugging", func(t *testing.T) {
		t.Parallel()

		if got := versionSlug(rec.ClaudeVersion); got == rec.ClaudeVersion {
			t.Errorf("#1701: claude_version %q survives versionSlug unchanged (slugs to %q); "+
				"initControlFixtureName over a slug-clean token is byte-identical to what a "+
				"writer formatting its own \"initialize_control_v%%s.json\" produces, so "+
				"#1702's \"named exactly what the namer mints\" assertion goes 0-RED against "+
				"precisely the self-formatting writer #1696's lock exists to close — with "+
				"#1696's own test still green. If you arrived here after tidying this literal "+
				"into a clean version, that is what you emptied",
				rec.ClaudeVersion, got)
		}
	})
}
