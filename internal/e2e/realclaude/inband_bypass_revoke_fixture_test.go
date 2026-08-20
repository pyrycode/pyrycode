//go:build e2e_realclaude

package realclaude

// #1662 — the write half of #1643's Pool-issued bypass-revocation substrate: the
// fixture record one arm commits, and the writer that puts it on disk through
// #1661's namer, into a directory its caller chooses, with the free-text child
// capture bounded.
//
// A measurement whose only trace is a test log evaporates when the run ends, so
// #1643's artifact is the point — and an artifact is worth committing only if it
// is COMPLETE (no field silently dropped on the way to disk), BOUNDED (no
// unbounded credential-bearing capture) and ATOMIC (no half-written residue
// stranded for a later commit). None of the three needs a claude binary, so all
// three settle here and #1643's first failure mode costs zero tokens instead of a
// whole three-arm run.
//
// # What the round trip catches, and what it cannot
//
// The read-back decodes through the struct that wrote the file, so it is
// symmetric and CANNOT catch a misspelled JSON tag. That is deliberately not
// claimed: these names are read by humans and by future re-measurement, never by
// a production decoder, so a misspelling is cosmetic. (#1651's decode goes
// through map[string]json.RawMessage with literal key names precisely because ITS
// key names are read by production code.)
//
// What non-zero, DISTINCT values do catch is real, and each of them reads back as
// exactly the zero value an all-zero fixture would have produced anyway: two
// fields sharing a tag (encoding/json drops both), a field tagged `json:"-"`, a
// field decoded from the wrong tag, and the truncation cap. Distinctness among
// the non-bool fields keeps two same-typed fields from satisfying each other's
// row.
//
// # Offline
//
// This file reaches no live claude, no daemon, no subprocess and no credential:
// it must not reach resolveClaudeBin, probeClaudeVersion, WithWorktree,
// WithWorktreeAuthenticated, os.Getenv or os.Environ, and it must not reach
// packageDir or any of the wrappers that resolve the committed testdata/ through
// it. It DOES read, write and list a directory — its entire subject — but only
// under a t.TempDir(). TestFinOfflineFilesReachNoExecHelper enforces the list
// over this file's AST rather than over this paragraph.
//
//	go test -tags e2e_realclaude -race -count=1 -v \
//	  -run 'TestPoolRevokeFixture_|TestFinOfflineFilesReachNoExecHelper' \
//	  ./internal/e2e/realclaude/
//
// All must report PASS — not SKIP, not "no tests to run" — on a machine with no
// claude and no credentials. Read the count of tests that executed, never the
// exit code: this package is behind the e2e_realclaude tag, `make check` never
// compiles it, and the suite exits 0 both on a build failure and on a full
// credentials skip.

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

// --- the record ---------------------------------------------------------------

// poolRevokeFixtureRecord is the durable artifact #1643 commits for ONE arm. It
// carries exactly the eighteen fields that probe can fill and no others: a field
// nothing fills reads back as a zero in a committed file and misleads whoever
// reads it next.
//
// There is deliberately no `env` field, and there must never be one — the
// constraint is inherited from setModeFixtureRecord. The credential reaches the
// child through the environment (WithWorktreeAuthenticated) while the argv
// carries none, so recording argv is safe and recording env would not be.
//
// ChildOutputCapture is capped by the writer at stderrFixtureCap, not by its
// caller: an auth failure can dump an unbounded, credential-bearing message into
// a file that is then committed, and a cap each caller has to remember is a cap
// #1643 can forget. #1595 applies it per call site; this type does not.
//
// CONSTRAINT ON #1643, which this ticket cannot enforce in code: ControlResponses,
// StdoutLines and NotDeliveredLogs are NOT capped, because capping the probe's
// structured evidence would destroy the artifact. ChildOutputCapture is the field
// designed to absorb unbounded child output — a free-text failure stream belongs
// there and must not be routed into StdoutLines.
type poolRevokeFixtureRecord struct {
	ClaudeVersionRaw string `json:"claude_version_raw"`
	ClaudeVersion    string `json:"claude_version"`

	Arm string `json:"arm"`

	// LaunchYOLO and TakesSettingsUpdate take their names from poolRevokeArm's
	// fields, not from setModeFixtureRecord's LaunchYOLOFlag, because they are
	// the same two things: LaunchYOLO is the arm's STORED bootstrap posture
	// lifted off the registry entry, not a runtime flag.
	LaunchYOLO          bool `json:"launch_yolo"`
	TakesSettingsUpdate bool `json:"takes_settings_update"`

	Argv    []string `json:"argv"`
	Prompts []string `json:"prompts"`

	SpawnCount int `json:"spawn_count"`
	PIDBefore  int `json:"pid_before_revocation"`
	PIDAfter   int `json:"pid_after_revocation"`

	ControlResponses    []json.RawMessage `json:"control_responses"`
	InitPermissionModes []string          `json:"init_permission_modes"`

	// StdoutLines is []string and not setModeFixtureRecord's []json.RawMessage:
	// that record pairs its typed events with a NonJSONLineCount, and this one is
	// bounded at eighteen fields with no room for the counter. The raw line
	// captures a non-JSON line as itself rather than dropping it and incrementing
	// a counter that does not exist here.
	StdoutLines    []string       `json:"stdout_lines"`
	TurnBoundaries []int          `json:"turn_boundaries"`
	ProbeOutcomes  []probeOutcome `json:"probe_outcomes"`

	// NotDeliveredLogs is a capture of EMITTED LOG RECORDS, not a decoded type.
	// deliverSettingsInBand's notDelivered closure calls p.log.Info and the
	// function returns nothing; no struct escapes it, so there is nothing to
	// decode. #1643 captures these through an slog handler.
	NotDeliveredLogs []string `json:"not_delivered_logs"`

	DeadlineTripped    bool   `json:"deadline_tripped"`
	ChildOutputCapture string `json:"child_output_capture"`
}

// --- the writer -----------------------------------------------------------------

// writePoolRevokeFixture writes rec into dir and returns the written path.
//
// dir is a PARAMETER, deliberately — unlike writeSetModeFixture, which hardcodes
// packageDir(t)/testdata and is correct for #1595's live run. That is what lets
// this writer settle here against a t.TempDir(), with #1643 passing the real
// testdata/ later. The filename is minted by poolRevokeFixtureName with both
// arguments passed through UNMODIFIED (that namer slugs both): a writer that
// formats its own name puts #1643's committed artifact back inside the overwrite
// hazard #1661 exists to close, with #1661's test still green.
//
// rec is never mutated — the cap lands on a local copy. Failure is always
// t.Fatalf naming the arm and the error and NOTHING ELSE: a %+v of the record
// would move up to stderrFixtureCap bytes of child output out of the bounded file
// and into an unbounded run log, the exact thing the cap exists to prevent.
func writePoolRevokeFixture(t *testing.T, dir string, rec *poolRevokeFixtureRecord) string {
	t.Helper()

	out := *rec
	out.ChildOutputCapture = capFixtureCapture(out.ChildOutputCapture)

	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("#1662: mkdir fixture dir for arm %q: %v", out.Arm, err)
	}
	path := filepath.Join(dir, poolRevokeFixtureName(out.ClaudeVersion, out.Arm))
	data, err := json.MarshalIndent(&out, "", "  ")
	if err != nil {
		t.Fatalf("#1662: marshal fixture for arm %q: %v", out.Arm, err)
	}
	// Temp file and rename, the discipline writeSetModeFixture uses: an
	// interrupted run must not strand a half-written fixture for a later commit.
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		t.Fatalf("#1662: write fixture tmp for arm %q: %v", out.Arm, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		t.Fatalf("#1662: rename fixture for arm %q: %v", out.Arm, err)
	}
	return path
}

// capFixtureCapture bounds a free-text capture at stderrFixtureCap bytes without
// ever splitting a rune. The cap constant and the byte-slicing helper are #1595's;
// only the boundary trim is new.
//
// The trim is not decoration. truncateString slices BYTES, and encoding/json does
// not error on invalid UTF-8 — it substitutes U+FFFD, three bytes per invalid
// byte. Measured on this toolchain, a five-byte string cut mid-rune reads back at
// SEVEN bytes, so a plain byte cap over a multi-byte capture reads back at up to
// cap+6 and the stated bound would be one this writer does not hold.
// DecodeLastRuneInString reports (RuneError, 1) for exactly the split-tail case
// and (RuneError, 3) for a legitimately encoded U+FFFD, so the size check leaves
// genuine content alone; three iterations is UTF-8's maximum continuation run.
//
// Nothing here pins the early return, and the under_cap row does not: measured,
// removing it reddens nothing, because truncateString carries the same
// len(s) <= max guard and the trim loop breaks immediately on a valid tail. It
// stays because its one real job is keeping that loop off under-cap input — a
// short capture whose tail is already invalid would otherwise lose up to three
// bytes the cap was never meant to touch.
func capFixtureCapture(s string) string {
	if len(s) <= stderrFixtureCap {
		return s
	}
	out := truncateString(s, stderrFixtureCap)
	for i := 0; i < utf8.UTFMax-1; i++ {
		r, size := utf8.DecodeLastRuneInString(out)
		if r != utf8.RuneError || size != 1 {
			break
		}
		out = out[:len(out)-1]
	}
	return out
}

// --- the fixture the round trip is run over ---------------------------------------

// poolRevokeFullRecord returns a record in which every one of the eighteen fields
// carries a non-zero value, and every pair of same-typed non-bool fields carries a
// DISTINCT one. Both properties are asserted rather than trusted; see the subtests.
//
// Two literals are load-bearing. The arm carries a space and a separator, both of
// which versionSlug rewrites: without that, "the entry is named exactly what
// poolRevokeFixtureName mints" is green under a writer that interpolates its own
// format string, since every real arm name and slugged version token is already
// slug-clean. It is this file's OWN literal and must never be added to
// poolRevokeArms, which is read-only and pinned by name elsewhere. And
// ControlResponses carries a realistic control_response envelope containing no
// '<', '>' or '&' — encoding/json escapes those inside a json.RawMessage, the
// escape survives compaction, and the round-trip row would redden for a perfectly
// correct writer.
func poolRevokeFullRecord() *poolRevokeFixtureRecord {
	return &poolRevokeFixtureRecord{
		ClaudeVersionRaw:    "2.1.220 (Claude Code)",
		ClaudeVersion:       "2.1.220",
		Arm:                 "revoke arm/2",
		LaunchYOLO:          true,
		TakesSettingsUpdate: true,
		Argv:                []string{"claude", "--dangerously-skip-permissions", "--print"},
		Prompts:             []string{"probe turn one", "probe turn two"},
		SpawnCount:          1,
		PIDBefore:           4242,
		PIDAfter:            4243,
		ControlResponses: []json.RawMessage{
			json.RawMessage(`{"type":"control_response","response":{"subtype":"success","request_id":"req_1"}}`),
		},
		InitPermissionModes: []string{"bypassPermissions", "default"},
		StdoutLines:         []string{`{"type":"system","subtype":"init"}`, "not json at all"},
		TurnBoundaries:      []int{0, 7},
		ProbeOutcomes: []probeOutcome{{
			ToolUseNames:      []string{"Write"},
			ToolResultSeen:    true,
			ToolResultIsError: true,
			PermissionDenials: 1,
			ResultSubtype:     "success",
			ResultIsError:     true,
			ResultObserved:    true,
		}},
		NotDeliveredLogs:   []string{"sessions: in-band settings command not delivered session=s1"},
		DeadlineTripped:    true,
		ChildOutputCapture: "child stderr: the free-text stream this field absorbs",
	}
}

// compactRawMessages normalises the whitespace json.MarshalIndent introduces
// INSIDE an embedded json.RawMessage: an envelope written and read back is not
// byte-equal, because it comes back carrying the indentation. Both sides of the
// row go through this, so what is blinded is that whitespace and nothing else — a
// dropped field (nil against non-nil), a `json:"-"` and a field decoded from the
// wrong tag all still redden.
func compactRawMessages(t *testing.T, in []json.RawMessage) []json.RawMessage {
	t.Helper()

	out := make([]json.RawMessage, 0, len(in))
	for _, raw := range in {
		var buf bytes.Buffer
		if err := json.Compact(&buf, raw); err != nil {
			t.Fatalf("#1662: compact a control response: %v; a malformed literal makes the "+
				"control_responses row meaningless", err)
		}
		out = append(out, json.RawMessage(buf.Bytes()))
	}
	return out
}

// fixtureFieldNonZero reports whether a table row's want value is non-zero.
//
// The kind switch is the point: a []string{} is !IsZero() and proves nothing, so
// the container kinds are judged on length and everything else on IsZero.
func fixtureFieldNonZero(v any) bool {
	rv := reflect.ValueOf(v)
	if !rv.IsValid() {
		return false
	}
	switch rv.Kind() {
	case reflect.String, reflect.Slice, reflect.Map:
		return rv.Len() > 0
	default:
		return !rv.IsZero()
	}
}

// --- the round trip ----------------------------------------------------------------

// TestPoolRevokeFixture_RoundTripsEveryFieldIntoOneNamedEntry is #1662's AC 1 and
// AC 2: every field of a fully-populated record survives write-then-read-back, and
// the target directory afterwards holds exactly one entry, named exactly what
// poolRevokeFixtureName mints.
//
// The subtests share one written artifact and are deliberately NOT parallel; the
// parent owns the tempdir.
func TestPoolRevokeFixture_RoundTripsEveryFieldIntoOneNamedEntry(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	rec := poolRevokeFullRecord()
	path := writePoolRevokeFixture(t, dir, rec)

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("#1662: read back the fixture for arm %q: %v", rec.Arm, err)
	}
	var got poolRevokeFixtureRecord
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("#1662: decode the fixture for arm %q: %v", rec.Arm, err)
	}

	rows := []struct {
		name       string
		want, have any
	}{
		{"claude_version_raw", rec.ClaudeVersionRaw, got.ClaudeVersionRaw},
		{"claude_version", rec.ClaudeVersion, got.ClaudeVersion},
		{"arm", rec.Arm, got.Arm},
		{"launch_yolo", rec.LaunchYOLO, got.LaunchYOLO},
		{"takes_settings_update", rec.TakesSettingsUpdate, got.TakesSettingsUpdate},
		{"argv", rec.Argv, got.Argv},
		{"prompts", rec.Prompts, got.Prompts},
		{"spawn_count", rec.SpawnCount, got.SpawnCount},
		{"pid_before_revocation", rec.PIDBefore, got.PIDBefore},
		{"pid_after_revocation", rec.PIDAfter, got.PIDAfter},
		{"control_responses", compactRawMessages(t, rec.ControlResponses), compactRawMessages(t, got.ControlResponses)},
		{"init_permission_modes", rec.InitPermissionModes, got.InitPermissionModes},
		{"stdout_lines", rec.StdoutLines, got.StdoutLines},
		{"turn_boundaries", rec.TurnBoundaries, got.TurnBoundaries},
		{"probe_outcomes", rec.ProbeOutcomes, got.ProbeOutcomes},
		{"not_delivered_logs", rec.NotDeliveredLogs, got.NotDeliveredLogs},
		{"deadline_tripped", rec.DeadlineTripped, got.DeadlineTripped},
		{"child_output_capture", rec.ChildOutputCapture, got.ChildOutputCapture},
	}

	t.Run("every field carries a non-zero value", func(t *testing.T) {
		for _, row := range rows {
			if !fixtureFieldNonZero(row.want) {
				t.Errorf("#1662: the fixture leaves %s at its zero value; a zero-valued record "+
					"round-trips under ANY tag arrangement, so that row settles nothing — a "+
					"dropped tag, a json:\"-\" and a wrong-tag decode all read back as exactly "+
					"this value", row.name)
			}
		}
	})

	t.Run("every field survives the round trip", func(t *testing.T) {
		for _, row := range rows {
			if !reflect.DeepEqual(row.want, row.have) {
				t.Errorf("#1662: %s did not survive the round trip: wrote %v, read back %v; "+
					"#1643 commits this record as its only durable trace",
					row.name, row.want, row.have)
			}
		}
	})

	t.Run("same-typed fields carry distinct values", func(t *testing.T) {
		for i := range rows {
			// Bools are skipped: all three are true by AC 1, which is sound
			// because a tag collision between two bools drops BOTH and reads back
			// false — caught by the non-zero subtest above.
			if reflect.ValueOf(rows[i].want).Kind() == reflect.Bool {
				continue
			}
			for j := i + 1; j < len(rows); j++ {
				if reflect.TypeOf(rows[i].want) != reflect.TypeOf(rows[j].want) {
					continue
				}
				if reflect.DeepEqual(rows[i].want, rows[j].want) {
					t.Errorf("#1662: %s and %s carry the same value %v; two same-typed fields "+
						"must not be able to satisfy each other's assertion, or a wrong-tag "+
						"decode reads one out of the other and stays green",
						rows[i].name, rows[j].name, rows[i].want)
				}
			}
		}
	})

	// One assertion, four hazards: a .tmp stranded by a missing rename, any stray
	// file, a write that escaped to the package's real testdata/ (which leaves this
	// tempdir at ZERO entries — the relative-path hazard no AST ban can close), and
	// a writer that minted its target name some other way instead of calling
	// poolRevokeFixtureName. The last is non-vacuous only because the fixture arm
	// carries characters versionSlug rewrites.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("#1662: list the fixture directory for arm %q: %v", rec.Arm, err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	want := poolRevokeFixtureName(rec.ClaudeVersion, rec.Arm)
	if len(names) != 1 || names[0] != want {
		t.Errorf("#1662: the target directory holds %q, want exactly [%q]: a .tmp left here is a "+
			"half-written fixture a later commit would pick up, an empty directory means the "+
			"bytes went somewhere the caller never chose, and a differently-spelled entry means "+
			"the writer formats its own name instead of minting through poolRevokeFixtureName",
			names, want)
	}
}

// --- the cap ------------------------------------------------------------------------

// TestPoolRevokeFixture_WriterCapsChildOutputCapture is #1662's AC 3: the WRITER
// bounds the free-text capture, so #1643 cannot forget to.
//
// Every row asserts through the writer's output ON DISK rather than by calling
// capFixtureCapture directly — the claim is about what reads back, and a direct
// call would not catch a writer that forgot to apply the cap at all. Every length
// assertion is on len(), never on rune count: the cap is a byte cap and
// truncateString slices bytes.
//
// Failure messages report LENGTHS and at most a short prefix. Printing want and
// got here would dump 8 KiB of capture into the run log, which defeats the field's
// entire purpose.
func TestPoolRevokeFixture_WriterCapsChildOutputCapture(t *testing.T) {
	t.Parallel()

	const shortCapture = "child exited 1: no tool call observed"

	tests := []struct {
		name    string
		capture string
		check   func(t *testing.T, capture, back string)
	}{
		{
			name:    "over_cap_ascii",
			capture: strings.Repeat("A", stderrFixtureCap+1024),
			check: func(t *testing.T, capture, back string) {
				if len(back) != stderrFixtureCap {
					t.Errorf("#1662: a %d-byte capture reads back at %d bytes, want exactly %d; the "+
						"writer leaves the cap to its caller, and an auth failure then dumps an "+
						"unbounded credential-bearing message into a committed file",
						len(capture), len(back), stderrFixtureCap)
				}
			},
		},
		{
			name:    "under_cap",
			capture: shortCapture,
			check: func(t *testing.T, capture, back string) {
				if back != capture {
					t.Errorf("#1662: a %d-byte capture, well under the %d-byte cap, reads back at %d "+
						"bytes and altered; the writer truncates unconditionally and damages every "+
						"short capture in #1643's record", len(capture), stderrFixtureCap, len(back))
				}
			},
		},
		{
			// One ASCII byte then two-byte runes, so the byte at the cap is a
			// CONTINUATION byte and truncateString alone would split a rune.
			name:    "over_cap_multibyte",
			capture: "A" + strings.Repeat("é", 5000),
			check: func(t *testing.T, capture, back string) {
				if !utf8.ValidString(back) {
					t.Errorf("#1662: the %d-byte read-back is not valid UTF-8; the writer split a "+
						"rune", len(back))
				}
				if len(back) > stderrFixtureCap || len(back) < stderrFixtureCap-(utf8.UTFMax-1) {
					t.Errorf("#1662: a %d-byte multi-byte capture reads back at %d bytes, want within "+
						"[%d, %d]; encoding/json substitutes U+FFFD per invalid byte, so a byte cap "+
						"with no rune-boundary trim reads back OVER the cap it states",
						len(capture), len(back), stderrFixtureCap-(utf8.UTFMax-1), stderrFixtureCap)
				}
				if !strings.HasPrefix(capture, back) {
					t.Errorf("#1662: the %d-byte read-back (first bytes %q) is not a prefix of the "+
						"original; the writer rewrote content rather than trimming a split tail",
						len(back), back[:min(16, len(back))])
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			// The multibyte row's vacuity control. Without it the row silently
			// degrades to an ASCII-equivalent case the day somebody edits the
			// literal, and it would prove nothing while staying green. A row that
			// cannot discriminate is a broken instrument, hence Fatalf.
			if tc.name == "over_cap_multibyte" && utf8.RuneStart(tc.capture[stderrFixtureCap]) {
				t.Fatalf("#1662: byte %d of this row's capture starts a rune, so truncateString "+
					"alone would not split one and the rune-boundary trim is never exercised",
					stderrFixtureCap)
			}

			rec := poolRevokeFullRecord()
			rec.ChildOutputCapture = tc.capture
			path := writePoolRevokeFixture(t, t.TempDir(), rec)

			// The no-mutation contract: the writer caps a copy, so a caller's
			// record still holds everything it held.
			if len(rec.ChildOutputCapture) != len(tc.capture) {
				t.Errorf("#1662: the writer left the caller's record at %d bytes, want the "+
					"original %d; it caps in place, so an assertion against the caller's "+
					"record compares a mutated value with its own decode and proves nothing",
					len(rec.ChildOutputCapture), len(tc.capture))
			}

			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("#1662: read back the capped fixture for arm %q: %v", rec.Arm, err)
			}
			var back poolRevokeFixtureRecord
			if err := json.Unmarshal(data, &back); err != nil {
				t.Fatalf("#1662: decode the capped fixture for arm %q: %v", rec.Arm, err)
			}
			tc.check(t, tc.capture, back.ChildOutputCapture)
		})
	}
}
