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

// initControlResultTrailer is ONE `result` trailer recorded inside the send-point
// window: the turn count that trailer carried, and its cost. #1715 fills these
// from bytes claude wrote; nothing in this slice computes one.
//
// THE TWO READ-FROM FIELDS MIRROR CLAUDE'S OWN KEY NAMES. A real `result` line
// carries `num_turns` and `total_cost_usd` — the committed capture under
// testdata/ is where both spellings come from. Mirroring them makes the record's
// provenance checkable against the stdout_events bytes sitting in the same file,
// and leaves #1715's read site unambiguous. A divergence here is not cosmetic:
// it makes that provenance claim false, and #1702's round trip structurally
// cannot catch it, because it decodes through the struct that wrote the file.
//
// TotalCostUSDPresent IS POPULATED FROM THE TRAILER'S RAW BYTES AND IS NEVER
// DERIVED FROM TotalCostUSD. A trailer that carried no cost field is a different
// fact from one that carried zero, and a reader who tidies this flag into
// TotalCostUSD != 0 deletes the measurement. initControlSummarize decides
// ModelsPresent on raw bytes for exactly this reason — `"models":[]` and an
// absent `models` both decode to nil — and ModelsPresent beside ModelsCount is
// the pairing this flag beside its value copies.
//
// The limit on that contract is stated rather than dressed up as coverage:
// NOTHING COMPUTES THIS FLAG IN THIS SLICE, so the derived-from-the-value mutant
// is not reachable here. What exists here is this paragraph, plus a fixture
// carrying one entry of each shape so that both survive #1702's round trip
// carrying their different flags. #1715 — where a trailer carrying
// `"total_cost_usd": 0` in its raw bytes can exist at all — is where that mutant
// becomes reachable. Do not build a test here that pretends otherwise.
//
// No `omitempty` on any field, matching every other tag in this family. That
// keeps `total_cost_usd: 0` and `total_cost_usd_present: false` visible as
// present keys in a committed artifact a human reads. It is NOT what makes the
// round trip pass: reflect.DeepEqual over the decoded slice holds either way,
// since both sides decode through this same struct.
//
// NO json.RawMessage ANYWHERE IN THIS TYPE, and NOTHING ENFORCES THAT — which is
// the reason it is written here in capitals. A float64 survives
// json.MarshalIndent and its decode exactly, so the round-trip row over a slice
// of these holds with no normaliser at all; wrapping the cost "to be safe" buys
// nothing and costs the reader a decimal byte dump in every failure message.
//
// The obvious guard is not one. compactInitControlRawRows selects BY THE ROW'S GO
// TYPE, and the row this type reaches it through is []initControlResultTrailer —
// so raw JSON nested INSIDE a struct-slice row is invisible to the type switch,
// the touched-name set is unchanged, and the touched-name subtest of
// TestInitControlFixture_RoundTripsEveryFieldIntoOneNamedEntry stays green.
// Measured on this toolchain 2026-08-24: wrapping TotalCostUSD in a
// json.RawMessage carrying `0.0731` reddens NOTHING in this package — the round
// trip holds too, because MarshalIndent has no interior to indent in a scalar.
// That canary guards a fourth raw-JSON field on the RECORD; it does not guard
// this type, and a reader who assumes otherwise is trusting a test that never
// runs over the value.
type initControlResultTrailer struct {
	NumTurns            int     `json:"num_turns"`
	TotalCostUSD        float64 `json:"total_cost_usd"`
	TotalCostUSDPresent bool    `json:"total_cost_usd_present"`
}

// initControlFixtureRecord is the durable artifact #1688's live run commits, and
// the JSON contract #1690's decoder and #1692's fake read. Nineteen of its
// twenty-eight fields are setModeFixtureRecord's, carrying that record's JSON tags
// and Go types unchanged: three slices decode this shape, and a gratuitous
// divergence in a shared field is a defect that surfaces two tickets away. Arm is
// the nineteenth and is copied whole from that record — same tag, same Go type,
// same position relative to the version pair — for exactly that reason.
//
// There is deliberately no `env` field, and there must never be one — the
// constraint and its reasoning are inherited from setModeFixtureRecord. The
// credential reaches the child through the environment while the argv carries
// none, so recording argv is safe and recording env would not be.
//
// Arm names the send point the run INTENDED, and it is one of initControlArms'
// identifiers for a live capture — initControlFullRecord's is deliberately not one
// (see its literal-choice note). RECORDING AN ARM IS NOT A CLAIM THAT THE TURN
// COMPLETED: runInitControlChild already logs the case where its probe turn
// produced no result line inside the budget, and turn_boundaries is what tells a
// reader so. Read `after_completed_turn` as the arrangement the run was aiming at,
// never as an assertion about what happened.
//
// It also puts the two inputs initControlArmFixtureName mints a filename from
// adjacent in this declaration, which is what the fixture's path is now derived
// from end to end.
//
// ControlResponseWithinWait is MEASURED, NOT DERIVED, and it is NOT DERIVABLE —
// which is what separates it from both neighbouring paragraphs.
// ControlResponseRequestIDMatched is a function of two fields recorded verbatim
// beside it; ModelsPresent, ModelsCount and ModelsEntryFields are populated from a
// response a live run holds in hand. This field is a function of a WAIT THAT HAS
// ALREADY EXPIRED. No other field can reconstruct it and neither can the verbatim
// bytes: a control_response carries no arrival time relative to a budget the
// harness chose, and runInitControlChild snapshots ControlResponses after the child
// exits, so a response arriving past the budget still lands in that field. "There
// are response bytes" and "the wait was satisfied" are two different facts. A
// reader who tidies this into len(ControlResponses) > 0 deletes the measurement;
// TestInitControlFixture_RoundTripsAnUnansweredWaitBesideCapturedBytes is what
// reddens. It sits with the response fields rather than in the instrument-health
// block for ControlResponseRequestIDMatched's reason: a response that arrived late
// is a finding about claude's latency, not a fault in the harness.
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
// SendPointIndex is the ONE positional anchor for the send point: the number of
// stdout_events lines already recorded when the arm reached it, in the same index
// units turn_boundaries uses — setModeRecorder.add assigns both from the position
// a line lands at. The window the two fields after it read is
// stdout_events[SendPointIndex:], and it is one anchor rather than one per read
// so that two reads cannot disagree about which window they measured.
//
// BOTH EDGES ARE LEGITIMATE VALUES, NOT ERRORS. 0 means the send point preceded
// every recorded line, which is `before_first_turn`'s real value and not a
// sentinel; len(StdoutEvents) means nothing followed it. DO NOT ADD A PRESENCE
// FLAG BESIDE THIS ANCHOR. The symmetry with initControlResultTrailer's
// TotalCostUSDPresent is the trap: a flag there separates two facts that are
// genuinely different, while one here would make `before_first_turn`'s honest
// reading indistinguishable from a bug.
//
// The `control_no_request` arm writes no request and still carries an anchor: the
// equivalent point its drive sequence reached, the one corresponding to where the
// other two arms write. That is what makes the three arms' reads cover comparable
// windows. It is a contract #1715 implements; nothing here enforces it, and
// whether two arms' windows are then genuinely comparable is that ticket's to
// arrange rather than this record's.
//
// AfterSendPointSystemInitCount counts the `system`/`init` lines in that window —
// `"type":"system"` with `"subtype":"init"`, the same pair setModeRecorder.add
// switches on. That switch is the definition; this is deliberately not a second
// copy of it. AfterSendPointResultTrailers carries one entry per `result` line in
// the window, in arrival order.
//
// NO WHOLE-RUN FIGURE IS RECORDED A SECOND TIME. The whole-run `result` count is
// len(TurnBoundaries); the window's is len(AfterSendPointResultTrailers). Neither
// gets a field of its own, and a later slice must not add one.
//
// All three are POPULATED, never computed here, exactly as the models fields are.
// Until #1715 fills them a live re-run leaves all three at their zero values, so
// an artifact carrying `send_point_index: 0` today is an UNPOPULATED FIELD and
// not a `before_first_turn` capture — only the ticket that fills them makes the
// two distinguishable. That is accepted rather than papered over with a presence
// flag: the committed initialize_control_v2.1.239.json already predates arm and
// control_response_within_wait.
//
// ScannerError is load-bearing for #1688, which caps its reader per line: an
// over-long line must surface HERE rather than truncating silently.
//
// Redaction is the census redactInitControlRecord returned — the classes that
// actually fired, the placeholder each rewrote to, and how often. It is
// POPULATED at the fill site from what that pass RETURNS, exactly as
// ModelsPresent, ModelsCount and ModelsEntryFields are populated rather than
// computed: the pass reports the census and stores nothing, and
// TestInitControlRedactRecord_LeavesAPathFreeRecordByteIdentical is what keeps
// that true.
//
// AN EMPTY CENSUS IS NOT AN ABSENT ONE. dropcapRedactor.substitutions returns a
// NON-NIL EMPTY slice when nothing fired, while a record that never reached the
// pass holds a nil one — so `[]` says the redactor ran and matched nothing, and
// `null` says it never ran. That whole distinction lives in the marshalled
// bytes, and one `omitempty` on the tag — or one helper that normalises nil to
// empty on the way to the file — erases it with every other test in this package
// still green. TestInitControlFixture_DistinguishesAnEmptyCensusFromAnAbsentOne
// is what reddens.
//
// AN EMPTY CENSUS IS ALSO NOT A CLEAN ARTIFACT, and that is the reading a
// reviewer is most likely to supply unprompted. `[]` says the redactor RAN; it
// says nothing about whether the file is free of operator paths. dropcapRedactor
// installs no rule for an empty value, so a class armed with "" — or with a
// wrong or transposed path — matches nothing, and the census then honestly
// reports `[]` while the committed file still carries the real path under a class
// the table never armed. This field is an AUDIT TRAIL OVER THE REDACTOR, not a
// clean bill of health over the artifact; #1729's deny-scan is the fail-closed
// net that makes the second claim, and this one must not be read as making it.
//
// It also makes dropcapSubstitution a COMMITTED shape for this family. Until
// #1731 that type reached the initialize capture only through a t.Logf; every
// field of it is now marshalled into testdata/ and committed to git, and per the
// exception below it is not visited by the redaction pass. That is safe only
// because every field of it is harness-minted — Class is one of the dropcapClass*
// constants, Replacement one of the `$`-prefixed literals in
// newInitControlRedactor's table, Count an int — and it stops being safe the
// moment that type, which belongs to the DROPCAP family and whose next author is
// not reading this file, gains a field carrying a matched value, a sample or a
// path. NOTHING IN THIS PACKAGE REDDENS WHEN IT DOES; this paragraph is the
// guard, and #1729's deny-scan over the written bytes is the net behind it.
//
// It needs NO CAP, and the reason is structural rather than a judgement call:
// its length is bounded by the number of armed classes — at most the four
// newInitControlRedactor installs — and Count is an int, so it cannot grow with
// child output. It is not the shape a cap exists for. Do not add one.
//
// A STRING-BEARING FIELD ADDED HERE MUST BE VISITED BY redactInitControlRecord,
// which since #1733 rewrites every string, []string, json.RawMessage and
// []json.RawMessage field of this type before the record reaches the writer. That
// pass visits by field rather than by reflection, so a new field escapes it
// silently; the NumField assertion in
// TestInitControlFullRecord_PinsEveryFieldAndTheSluggableVersionToken forces a
// conscious edit to this file but says nothing about the pass.
//
// REDACTION IS THAT INSTRUCTION'S ONE EXCEPTION, scoped to this one field and
// never to "fields the author judges safe" — the instruction's whole value is
// that it admits no judgement call, and a string-bearing field added here that is
// not this one gets no exception. Two reasons, and the second is not a
// convenience: its strings are the REDACTOR'S OWN VOCABULARY — class identifiers
// and the $HOME, $WORKDIR, $TEMP_HOME and $TMPDIR placeholders minted by
// newInitControlRedactor, never child output, never a path, never read off the
// environment — so rewriting a placeholder is meaningless; and the field is
// assigned from what the pass RETURNS, so a pass that visited it would have to
// read a value that does not exist until it returns. The obligation is circular,
// not merely unnecessary.
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

	Arm string `json:"arm"`

	Argv    []string `json:"argv"`
	Prompts []string `json:"prompts"`

	ControlRequestID                string            `json:"control_request_id"`
	ControlRequestSent              json.RawMessage   `json:"control_request_sent"`
	ControlResponses                []json.RawMessage `json:"control_responses"`
	ControlResponseSubtype          string            `json:"control_response_subtype"`
	ControlResponseRequestIDMatched bool              `json:"control_response_request_id_matched"`
	ControlResponseWithinWait       bool              `json:"control_response_within_wait"`

	ModelsPresent     bool     `json:"models_present"`
	ModelsCount       int      `json:"models_count"`
	ModelsEntryFields []string `json:"models_entry_fields"`

	StdoutEvents     []json.RawMessage `json:"stdout_events"`
	NonJSONLineCount int               `json:"non_json_line_count"`
	TurnBoundaries   []int             `json:"turn_boundaries"`

	SendPointIndex                int                        `json:"send_point_index"`
	AfterSendPointSystemInitCount int                        `json:"after_send_point_system_init_count"`
	AfterSendPointResultTrailers  []initControlResultTrailer `json:"after_send_point_result_trailers"`

	StdinWriteErrors       []string `json:"stdin_write_errors"`
	StderrCapture          string   `json:"stderr_capture"`
	ExitCode               int      `json:"exit_code"`
	WaitError              string   `json:"wait_error"`
	ContextDeadlineTripped bool     `json:"context_deadline_tripped"`
	DurationMs             int64    `json:"duration_ms"`
	ScannerError           string   `json:"scanner_error"`

	Redaction []dropcapSubstitution `json:"redaction"`
}

// --- the fully-populated fixture -------------------------------------------------

// initControlFullRecord returns a record in which every one of the twenty-eight
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
// four bools are true for that same reason. A reader who takes this for a
// recording will reconcile it and empty half the properties doing it.
//
// That is also why the ONE pair the record most needs to be able to express is
// absent here: captured response bytes alongside control_response_within_wait
// FALSE. A bool is non-zero only when true, so this fixture necessarily carries
// the coherent combination — bytes and a satisfied wait. The incoherent pair gets
// its own record instance in
// TestInitControlFixture_RoundTripsAnUnansweredWaitBesideCapturedBytes rather than
// a change here; do not "improve" this literal to demonstrate it.
//
// The anchor's OTHER legitimate edge — send_point_index 0 — is likewise absent
// here, and unlike the pair above it gets NO second record instance. The
// resolutions differ for a reason worth stating, since the two cases otherwise
// look identical: #1722's second instance exists because a
// control_response_within_wait DERIVED from the response count reads back wrong
// there, so that instance is the sole red for a real mutant. #1723 computes
// nothing, so an instance carrying send_point_index 0 would pin 0 == 0 through a
// writer that never touched the value. Do not build one.
//
// Seven literal choices are load-bearing:
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
//  3. Arm must NOT survive versionSlug unchanged either, and it must NOT be one
//     of initControlArms' identifiers. All three of those are already slug-clean —
//     lowercase letters and `_`, every character inside versionSlug's [a-z0-9._-]
//     class — so a clean "after_completed_turn" copied in here would slug to
//     itself and #1702's "named exactly what the namer mints" assertion would go
//     0-RED ON THE ARM COLUMN against BOTH a writer interpolating the arm raw and
//     a writer passing a hardcoded arm instead of the record's. The uppercase run
//     is what makes the slug differ, exactly as it is for claude_version above,
//     and the word FIXTURE is what stops a later reader "correcting" the literal
//     into a declared arm. 28 bytes, comfortably inside versionSlug's 32-character
//     clamp. This is not in tension with the live contract: initControlArms binds
//     what a CAPTURE records, and this is a synthetic record that is deliberately
//     not a coherent capture.
//
//  4. ControlResponses and StdoutEvents must carry DIFFERENT content. Both are
//     []json.RawMessage, so distinctness binds them — and in a real capture the
//     control response arrives ON stdout, which is exactly the literal a
//     developer repeats into both. The resolution is two literals, not a weakened
//     assertion.
//
//  5. SendPointIndex and AfterSendPointSystemInitCount must not be 1, 2 or 3, and
//     must differ from each other. The same-typed-non-bool distinctness subtest
//     binds every int row against models_count (2), non_json_line_count (3) and
//     exit_code (1) as well as against each other, so a "tidier" value collides
//     and that subtest reddens. DurationMs is an int64 — a different reflect type
//     — so it constrains nothing here. The anchor's 4 sits past the end of the
//     two-element StdoutEvents, exactly as TurnBoundaries' {0, 7} already does:
//     incoherence is what buys this fixture its distinctness, and coherence here
//     would cost precisely that.
//
//  6. AfterSendPointResultTrailers carries ONE ENTRY OF EACH COST SHAPE — one
//     with the presence flag true beside a non-zero cost, one with it false
//     beside a zero cost. That is the discriminating pair, and it is why the
//     absent-cost fact needs no second record instance: unlike a bool field, a
//     slice holds both shapes at once, so both reach #1702's round trip carrying
//     their different flags. NumTurns is non-zero in both for realism; the
//     property over this type asks only for non-zero in AT LEAST ONE entry, which
//     is what lets the absent-cost entry exist beside the record's own non-zero
//     property at all. The cost is an ORDINARY DECIMAL: json.Marshal errors on
//     NaN and ±Inf, and a value whose shortest representation is exponential is a
//     needless risk, while a plain decimal marshals and decodes exactly and keeps
//     the round-trip row honest with no normaliser.
//
//  7. Redaction carries AT LEAST ONE ENTRY, and no path inside it.
//     fixtureFieldNonZero judges container kinds BY LENGTH, so an empty
//     []dropcapSubstitution{} fails the non-zero property above — the census a
//     genuinely path-free run produces is precisely the value this fixture
//     cannot carry. ONE entry is enough: every substitution has the same shape
//     and nothing in this file asserts the census's ORDER, so there is no
//     discriminating-pair argument of the kind note 6 makes for the trailers. A
//     class identifier and a `$`-placeholder carry no path, and a realistic one
//     put in Replacement "for realism" reddens
//     TestInitControlRedactRecord_LeavesAPathFreeRecordByteIdentical. Note 2
//     does NOT extend to this row: that constraint is specific to the three
//     raw-JSON literals, and dropcapSubstitution is an ordinary struct of
//     ordinary strings, which round-trips '<', '>' and '&' unchanged.
//     Distinctness constrains this row NOT AT ALL, stated so nobody counts on it
//     either way — that subtest groups by reflect.TypeOf of the ROW's value,
//     []dropcapSubstitution has no same-typed sibling here, and it never looks
//     inside the slice, so the nested Count cannot collide with models_count,
//     non_json_line_count or exit_code.
//
// Where self-consistency costs nothing it is kept: ControlRequestID, the
// request_id inside ControlRequestSent and the one inside ControlResponses all
// agree, which is what ControlResponseRequestIDMatched claims; ModelsCount and
// ModelsEntryFields match the envelope's models array.
func initControlFullRecord() *initControlFixtureRecord {
	return &initControlFixtureRecord{
		ClaudeVersionRaw: "2.1.220-FIXTURE (Claude Code)",
		ClaudeVersion:    "2.1.220-FIXTURE",

		Arm: "after_completed_turn-FIXTURE",

		Argv:    []string{"claude", "--input-format", "stream-json", "--output-format", "stream-json", "--verbose"},
		Prompts: []string{"probe turn one", "probe turn two"},

		ControlRequestID:   "req_init_1",
		ControlRequestSent: json.RawMessage(`{"type":"control_request","request_id":"req_init_1","request":{"subtype":"initialize"}}`),
		ControlResponses: []json.RawMessage{
			json.RawMessage(`{"type":"control_response","response":{"subtype":"success","request_id":"req_init_1","models":[{"model":"claude-opus-5","displayName":"Opus 5","supportedReasoningEfforts":["low","medium","high"]},{"model":"claude-haiku-4-5","displayName":"Haiku 4.5","supportedReasoningEfforts":["low"]}]}}`),
		},
		ControlResponseSubtype:          "success",
		ControlResponseRequestIDMatched: true,
		ControlResponseWithinWait:       true,

		ModelsPresent:     true,
		ModelsCount:       2,
		ModelsEntryFields: []string{"model", "displayName", "supportedReasoningEfforts"},

		StdoutEvents: []json.RawMessage{
			json.RawMessage(`{"type":"system","subtype":"init","session_id":"sess_init_fixture","model":"claude-opus-5"}`),
			json.RawMessage(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"listing the models now"}]}}`),
		},
		NonJSONLineCount: 3,
		TurnBoundaries:   []int{0, 7},

		SendPointIndex:                4,
		AfterSendPointSystemInitCount: 5,
		AfterSendPointResultTrailers: []initControlResultTrailer{
			{NumTurns: 6, TotalCostUSD: 0.0731, TotalCostUSDPresent: true},
			{NumTurns: 9, TotalCostUSD: 0, TotalCostUSDPresent: false},
		},

		StdinWriteErrors:       []string{"write |1: broken pipe"},
		StderrCapture:          "child stderr: the free-text stream this field absorbs",
		ExitCode:               1,
		WaitError:              "signal: killed",
		ContextDeadlineTripped: true,
		DurationMs:             1842,
		ScannerError:           "bufio.Scanner: token too long",

		Redaction: []dropcapSubstitution{
			{Class: dropcapClassWorkdir, Replacement: "$WORKDIR", Count: 3},
		},
	}
}

// --- the field listing -------------------------------------------------------

// initControlFixtureField is one row of the listing: a JSON tag and the value the
// field carrying it holds.
type initControlFixtureField struct {
	name  string
	value any
}

// initControlFixtureFields lists rec's twenty-eight fields once, in declaration
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
		{"arm", rec.Arm},
		{"argv", rec.Argv},
		{"prompts", rec.Prompts},
		{"control_request_id", rec.ControlRequestID},
		{"control_request_sent", rec.ControlRequestSent},
		{"control_responses", rec.ControlResponses},
		{"control_response_subtype", rec.ControlResponseSubtype},
		{"control_response_request_id_matched", rec.ControlResponseRequestIDMatched},
		{"control_response_within_wait", rec.ControlResponseWithinWait},
		{"models_present", rec.ModelsPresent},
		{"models_count", rec.ModelsCount},
		{"models_entry_fields", rec.ModelsEntryFields},
		{"stdout_events", rec.StdoutEvents},
		{"non_json_line_count", rec.NonJSONLineCount},
		{"turn_boundaries", rec.TurnBoundaries},
		{"send_point_index", rec.SendPointIndex},
		{"after_send_point_system_init_count", rec.AfterSendPointSystemInitCount},
		{"after_send_point_result_trailers", rec.AfterSendPointResultTrailers},
		{"stdin_write_errors", rec.StdinWriteErrors},
		{"stderr_capture", rec.StderrCapture},
		{"exit_code", rec.ExitCode},
		{"wait_error", rec.WaitError},
		{"context_deadline_tripped", rec.ContextDeadlineTripped},
		{"duration_ms", rec.DurationMs},
		{"scanner_error", rec.ScannerError},
		{"redaction", rec.Redaction},
	}
}

// initControlTrailerFields lists tr's fields once, in declaration order, and
// carries initControlFixtureFields' hand-written obligation unchanged: these
// names are a SECOND, INDEPENDENT COPY of the tags, and a reflection-driven
// listing reads each name off the very tag it was meant to check. That matters
// twice over here, because num_turns and total_cost_usd are claude's own key
// names — a misspelling makes the record's provenance claim false against the
// stdout_events bytes in this same file, and no round trip can catch it.
//
// It takes tr BY VALUE: three scalars, and every caller ranges a slice of them.
//
// One obligation is this listing's alone.
// reflect.TypeOf(initControlFixtureRecord{}).NumField() counts the record's OWN
// fields, so every field of initControlResultTrailer is invisible to all three of
// the record's properties. This listing, plus the two trailer subtests in
// TestInitControlFullRecord_PinsEveryFieldAndTheSluggableVersionToken, is the only
// thing standing between a field added to that type and no coverage anywhere.
//
// What does NOT bind here, stated so that nobody counts on it: the record's
// distinctness property groups by reflect.TypeOf, and []initControlResultTrailer
// has no same-typed sibling in the record, so distinctness gives that row nothing
// — and nothing at all binds the fields INSIDE the type. Their whole coverage is
// the length assertion plus the non-zero-in-at-least-one-entry property.
//
// The two listings cannot be folded into one. The row type is shared, but the
// field sets are different, and a single reflection-driven listing over both is
// exactly what both doc comments forbid.
func initControlTrailerFields(tr initControlResultTrailer) []initControlFixtureField {
	return []initControlFixtureField{
		{"num_turns", tr.NumTurns},
		{"total_cost_usd", tr.TotalCostUSD},
		{"total_cost_usd_present", tr.TotalCostUSDPresent},
	}
}

// --- the properties ----------------------------------------------------------

// TestInitControlFullRecord_PinsEveryFieldAndTheSluggableVersionToken is #1701
// whole: the listing covers every field of the record exactly once, every listed
// field carries a non-zero value, every pair of same-typed non-bool fields
// carries a distinct one, and NEITHER of the two literals the fixture's filename
// is minted from survives slugging.
//
// The name is incomplete after #1722 widened the last subtest to the arm column
// and #1723 added three subtests over initControlResultTrailer, not false — it
// still pins every field and the sluggable version token. Renaming it would
// cascade into prose in initialize_control_writer_test.go and into this file's
// finOfflineExecBans entry for no behavioural gain, and every -run filter in this
// family keys on the TestInitControlFullRecord_ prefix.
//
// The last three subtests are the record's three properties again, applied to the
// nested type the record's own properties cannot see: reflect over the record
// counts its OWN fields, so a field on initControlResultTrailer is checked by
// those three subtests or by nothing.
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
		// non-zero values — there is only one — so all four of this record's are
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

	t.Run("neither minting input survives slugging", func(t *testing.T) {
		t.Parallel()

		// Two checks, deliberately not folded into a loop: each names the mutants
		// its own column goes 0-red against, and those sets are different.
		// writeInitControlFixture mints its path from BOTH of these literals, so
		// #1702's "named exactly what the namer mints" assertion is only as
		// discriminating as the weaker column.
		if got := versionSlug(rec.ClaudeVersion); got == rec.ClaudeVersion {
			t.Errorf("#1701: claude_version %q survives versionSlug unchanged (slugs to %q); "+
				"initControlArmFixtureName over a slug-clean token is byte-identical to what a "+
				"writer formatting its own \"initialize_control_v%%s_%%s.json\" produces, so "+
				"#1702's \"named exactly what the namer mints\" assertion goes 0-RED against "+
				"precisely the self-formatting writer #1696's lock exists to close — with "+
				"#1696's own test still green. If you arrived here after tidying this literal "+
				"into a clean version, that is what you emptied",
				rec.ClaudeVersion, got)
		}

		if got := versionSlug(rec.Arm); got == rec.Arm {
			t.Errorf("#1722: arm %q survives versionSlug unchanged (slugs to %q); every "+
				"identifier in initControlArms is already slug-clean, so a clean arm here "+
				"makes #1702's \"named exactly what the namer mints\" assertion 0-RED ON THE "+
				"ARM COLUMN against BOTH a writer interpolating the arm raw and a writer "+
				"passing a hardcoded arm instead of the record's. If you arrived here after "+
				"\"correcting\" this literal into a declared arm, that is what you emptied — "+
				"this record is a synthetic fixture, not a capture, and initControlArms binds "+
				"what a capture records", rec.Arm, got)
		}
	})

	t.Run("the trailer listing covers every trailer field exactly once", func(t *testing.T) {
		t.Parallel()

		// Over the ZERO VALUE, deliberately: this is a claim about the TYPE's
		// field set, and the values play no part in it. Taking it over
		// rec.AfterSendPointResultTrailers[0] instead would panic — not redden —
		// against a fixture whose trailer list was emptied, and a panic takes the
		// whole test binary down with it. The emptied list is the next subtest's
		// t.Fatalf to report.
		trRows := initControlTrailerFields(initControlResultTrailer{})

		if want := reflect.TypeOf(initControlResultTrailer{}).NumField(); len(trRows) != want {
			t.Errorf("#1723: the trailer listing has %d rows, want %d — one per field of "+
				"initControlResultTrailer; the record's own NumField check counts the RECORD's "+
				"fields and cannot see this type at all, so a field added here with no row is "+
				"unchecked by every property in this file and by #1702's round trip",
				len(trRows), want)
		}

		// Both halves, for the record listing's stated reason: length alone is
		// green against a listing that names one field twice and omits another.
		seen := make(map[string]bool, len(trRows))
		for _, row := range trRows {
			if seen[row.name] {
				t.Errorf("#1723: the trailer listing names %q twice, so it holds the right number "+
					"of rows while some other trailer field has none at all", row.name)
			}
			seen[row.name] = true
		}
	})

	t.Run("every trailer field is non-zero in at least one entry", func(t *testing.T) {
		t.Parallel()

		// The vacuity control, and it must Fatalf rather than skip — precheck's
		// precedent in TestInitControlFixture_WriterCapsStderrCapture. An empty
		// list makes the property below vacuously true, and a property that cannot
		// discriminate is a broken instrument, not a passing test.
		if len(rec.AfterSendPointResultTrailers) == 0 {
			t.Fatalf("#1723: the fixture carries no after_send_point_result_trailers, so every " +
				"claim about that type's fields below is vacuously true and #1702's round trip " +
				"zips an empty slice against an empty slice")
		}

		// PER FIELD ACROSS ENTRIES, not per entry, and that is not a weakening:
		// the record's own non-zero property applied to each entry would forbid
		// the absent-cost entry this fixture exists to carry — total_cost_usd 0
		// beside total_cost_usd_present false. Row order is stable across entries
		// because the listing is hand-written in declaration order.
		trRows := initControlTrailerFields(rec.AfterSendPointResultTrailers[0])
		nonZero := make([]bool, len(trRows))
		for _, tr := range rec.AfterSendPointResultTrailers {
			for i, row := range initControlTrailerFields(tr) {
				if fixtureFieldNonZero(row.value) {
					nonZero[i] = true
				}
			}
		}
		for i, row := range trRows {
			if !nonZero[i] {
				t.Errorf("#1723: %s is zero in all %d after_send_point_result_trailers entries; a "+
					"zero-valued field round-trips under ANY tag arrangement, so #1702's row over "+
					"this slice settles nothing about it", row.name, len(rec.AfterSendPointResultTrailers))
			}
		}
	})

	t.Run("the trailer list carries an entry of each cost shape", func(t *testing.T) {
		t.Parallel()

		// SCOPED TO THE FLAG ALONE, never to the flag paired with a non-zero cost:
		// pairing them would make this subtest fire as collateral every time the
		// non-zero subtest above fires, and each subtest here is meant to be the
		// sole red for its own mutant.
		var present, absent int
		for _, tr := range rec.AfterSendPointResultTrailers {
			if tr.TotalCostUSDPresent {
				present++
				continue
			}
			absent++
		}
		if present == 0 || absent == 0 {
			t.Errorf("#1723: the fixture carries %d trailer(s) with total_cost_usd_present true and "+
				"%d with it false, want at least one of each; collapsed to one shape, #1702's "+
				"round trip stops proving that two entries survive carrying DIFFERENT flags, "+
				"which is the whole of the claim that a trailer carrying no cost field stays "+
				"distinguishable from one carrying zero", present, absent)
		}
	})
}
