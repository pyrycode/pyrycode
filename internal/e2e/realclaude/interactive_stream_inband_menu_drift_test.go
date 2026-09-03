//go:build e2e_realclaude

package realclaude

// #2045 — the one-run answer to "did claude's published model menu move?", read by
// phase 2 of TestInteractiveStream_InBandModelChange_LiveChildReportsNewModel when
// B1 or B2 goes red.
//
// # What went wrong, and why an assertion could not tell anyone
//
// On 2026-09-02 B1/B2 reddened on a branch that touched zero production source
// files. Claude published `claude-fable-5-1[1m]` → `claude-fable-5-1` in the branch
// run and `claude-fable-5[1m]` → `claude-fable-5` in the base re-run eight minutes
// later, at ONE binary version (2.1.239), and did not apply the first form in band.
// Three re-runs on the branch passed 3/3. The defect is claude's, and B1/B2 were
// right to go red — but the gate's "failing here, passing on origin/main"
// attribution is sound only while claude's own responses hold still across the two
// runs, and here they did not. A branch was blamed for a change nothing in a git
// branch can make, and that cost a rework leg.
//
// This file does not weaken B1/B2, does not retry them, and does not refresh the
// committed capture. It answers a question the assertions cannot: is the menu this
// run observed the one claude published when the baseline was taken?
//
// # Why a comparison rather than a capture
//
// The obvious shape — write a capture file on the failing path — does not work
// here, and the reason is structural rather than stylistic. A pipeline run happens
// in a worktree that is discarded when the run ends, and a red run commits nothing
// (CLAUDE.md § Testing), so a capture written on the failing path is thrown away
// unread. Nothing is lost by not writing one: phase 2 already logs the whole live
// menu unconditionally, before the assertions, and Go prints a failing test's
// buffered log output. The observed menu is therefore already in the gate log. What
// is missing is the BASELINE, and this repo already commits that —
// testdata/initialize_control_v<version>.json, #1688's capture, records the menu
// claude published at a given version under the same
// control_response → response → response → models nesting inbandTapRecorder.consume
// decodes off the live tap.
//
// # Addressed by exact name, never by a glob
//
// initControlArmFixtureGlob discovers captures by pattern because its subject is
// whichever arms are on disk. This file's subject is the ONE version the run is
// executing against, so it goes through initControlFixtureName — the one-input namer
// — and reads that path or nothing. A glob would find a capture at a DIFFERENT
// version and answer "match" against the wrong baseline with nothing red anywhere,
// and the absence of a capture at the running version is a verdict this file
// reports rather than a hole for a neighbouring version to fill.
//
// The initialize_control_v prefix is never interpolated here. It is load-bearing for
// three foreign fixture globs, and both namer locks in
// initialize_control_names_test.go exist to keep it out of reach of any input.
//
// It is the ONE-INPUT namer and not initControlArmFixtureName: that one mints the
// three arm captures of #1763's measurement, and it is #1688's single unarmed
// capture that carries the menu compared here.
//
// The capture's own claude_version is deliberately NOT cross-checked against the
// token. initControlDiscoverArms needs that bind because it discovers by glob and
// must learn the version FROM the file; here the version is the input and the name
// is derived from it, so the bind has nothing left to say.
//
// # Offline, and it reads testdata/ on purpose
//
// This file reaches no live claude, no daemon, no subprocess, no credential and no
// environment, and it never writes. It is the second offline file in this package
// with a legitimate reason to READ testdata/ — initialize_control_compare_test.go is
// the first — so its finOfflineExecBans entry is that one's with filepath.Glob and
// packageDir kept BANNED rather than dropped: addressing one capture by exact name
// needs neither, `go test` runs in the package source directory so a relative
// testdata/ prefix resolves with no wrapper at all, and a glob is precisely the
// wrong-baseline hazard above. os.ReadFile is the single name permitted, and every
// write name stays banned.
//
//	go test -tags e2e_realclaude -race -count=1 -v \
//	  -run 'TestInbandMenuDrift_|TestFinOfflineFilesReachNoExecHelper' \
//	  ./internal/e2e/realclaude/
//
// Both must report PASS — not SKIP, not "no tests to run" — on a machine with no
// claude and no credentials. Read the count of tests that executed, never the exit
// code: this package is behind the e2e_realclaude tag, `make check` never compiles
// it, and the suite exits 0 both on a build failure and on a full credentials skip.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// --- the verdict ---------------------------------------------------------------

// inbandDriftOutcome classifies the comparison. It is a string rather than an int
// enum so the value reads as itself in the log line phase 2 emits, where an
// operator meeting this red for the first time is the audience.
type inbandDriftOutcome string

const (
	// inbandDriftMatch — the chosen row is recorded in the committed capture, on
	// BOTH fields. A red B1/B2 is not menu drift.
	inbandDriftMatch inbandDriftOutcome = "match"
	// inbandDriftDrift — the chosen row is not recorded there. A red B1/B2 is
	// consistent with claude's own menu having moved.
	inbandDriftDrift inbandDriftOutcome = "drift"
	// inbandDriftNoCapture — there is no readable baseline at this version, so
	// drift cannot be judged in either direction. NOT a failure.
	inbandDriftNoCapture inbandDriftOutcome = "no-capture"
)

// --- the reader ------------------------------------------------------------------

// inbandMenuCapturePath returns the RELATIVE path of the committed capture for
// versionToken. Relative is the whole mechanism: `go test` runs in the package
// source directory, which is the same fact initControlArmFixtureGlob's testdata/
// prefix rests on, so this resolves against the real directory while naming no
// wrapper — and packageDir stays banned for this file.
func inbandMenuCapturePath(versionToken string) string {
	return filepath.Join("testdata", initControlFixtureName(versionToken))
}

// inbandCommittedMenu reads the model menu out of the committed capture for
// versionToken.
//
// It returns the path IN EVERY CASE, including on error, because AC 2 asks the
// no-capture verdict to name what it looked for — and a verdict that cannot say
// which file was missing sends its reader looking for the wrong one.
//
// The outer decode goes through initControlFixtureRecord and never through a
// parallel struct, for initControlDiscoverArms' stated reason: a parallel shape is
// how a field rename lands as a silent zero value. The inner decode declares the
// two keys inbandMenuRow carries at the nesting inbandTapRecorder.consume already
// reads, so the capture and the live tap are decoded through one row type; the
// reply's other thirteen keys, the ~14 KB commands array included, are dropped by
// not being declared.
//
// The FIRST control_response carrying a non-empty models array wins, mirroring
// consume's non-empty guard rather than a subtype check: only an initialize success
// fills the array, so emptiness is the discriminator either way.
func inbandCommittedMenu(versionToken string) (string, []inbandMenuRow, error) {
	path := inbandMenuCapturePath(versionToken)

	raw, err := os.ReadFile(path)
	if err != nil {
		// Wrapped with %w so errors.Is(err, fs.ErrNotExist) still separates "this
		// version was never captured" from "the capture is there and broken".
		// Those read as different sentences in the verdict.
		return path, nil, fmt.Errorf("read committed capture: %w", err)
	}

	rec := initControlFixtureRecord{}
	if err := json.Unmarshal(raw, &rec); err != nil {
		return path, nil, fmt.Errorf("decode through initControlFixtureRecord: %w", err)
	}

	for _, resp := range rec.ControlResponses {
		var env struct {
			Response struct {
				Response struct {
					Models []inbandMenuRow `json:"models"`
				} `json:"response"`
			} `json:"response"`
		}
		if err := json.Unmarshal(resp, &env); err != nil {
			continue
		}
		if len(env.Response.Response.Models) > 0 {
			return path, env.Response.Response.Models, nil
		}
	}
	return path, nil, errors.New("no control_response in the capture carries a non-empty models array")
}

// inbandMenuPairs renders a menu as value→resolvedModel pairs.
//
// BOTH fields, never the values alone: the comparison keys on both, so a verdict
// listing only values would show two menus that look identical while disagreeing on
// exactly the field that drifted.
func inbandMenuPairs(menu []inbandMenuRow) string {
	if len(menu) == 0 {
		return "[]"
	}
	pairs := make([]string, 0, len(menu))
	for _, row := range menu {
		pairs = append(pairs, fmt.Sprintf("%s→%s", row.Value, row.ResolvedModel))
	}
	return "[" + strings.Join(pairs, " ") + "]"
}

// --- the comparison ----------------------------------------------------------------

// inbandCompareMenu classifies the menu row phase 2 chose against the committed
// capture for the running claude version, and renders the verdict phase 2 logs.
//
// IT NEVER FAILS ANYTHING and takes no *testing.T. A missing baseline is a fact to
// report, not a defect (AC 2), and a drift verdict arrives on a path where B1 or B2
// has ALREADY failed the test — a second red would double-count one finding.
//
// THE MATCH IS ON BOTH FIELDS. A row whose value is published under a different
// resolvedModel is exactly as much drift as one whose value is gone, and the
// 2026-09-02 observation moved both at once. The table below carries one row per
// direction, each varying a single field, so neither half can go vacuous alone.
//
// It compares THE CHOSEN ROW rather than the whole menu. The chosen row is what
// inbandPickBracketedValue handed to UpdateSettings and therefore what B1/B2 acted
// on; a menu that merely grew an unrelated row is not the drift being diagnosed,
// and reporting it as such would put a false lead in front of the first operator to
// meet this red.
func inbandCompareMenu(versionToken string, chosen inbandMenuRow, live []inbandMenuRow) (inbandDriftOutcome, string) {
	path, committed, err := inbandCommittedMenu(versionToken)
	if err != nil {
		reason := fmt.Sprintf("its capture could not be read: %v", err)
		if errors.Is(err, fs.ErrNotExist) {
			reason = "no capture is committed for this version"
		}
		return inbandDriftNoCapture, fmt.Sprintf(
			"#2045: menu drift could not be judged for claude %s — %s. Looked for %s. "+
				"This does not fail the test: the baseline is committed per claude version, so a "+
				"version this repo has never captured has nothing to compare against, and a red "+
				"B1/B2 above stays unattributed in either direction.",
			versionToken, reason, path)
	}

	for _, row := range committed {
		if row.Value == chosen.Value && row.ResolvedModel == chosen.ResolvedModel {
			return inbandDriftMatch, fmt.Sprintf(
				"#2045: no menu drift — claude %s published %q → %q, which is what the committed "+
					"capture %s records for this version, on both fields. A red B1/B2 above is "+
					"therefore NOT the published-menu flap recorded on #2045; the branch under "+
					"test is the thing to look at.",
				versionToken, chosen.Value, chosen.ResolvedModel, path)
		}
	}

	return inbandDriftDrift, fmt.Sprintf(
		"#2045: MENU DRIFT — claude %s published %q → %q, which the committed capture %s does "+
			"not record. committed %s; live %s. That is the shape of the flap recorded on #2045: "+
			"a value claude publishes at one binary version that the capture for that same "+
			"version does not carry, and that claude then does not apply in band. A red B1/B2 "+
			"above is consistent with claude's own menu having moved rather than with a defect "+
			"in this branch — re-run before attributing it, and do NOT weaken B1/B2 or refresh "+
			"the capture, which would erase the baseline this comparison reads.",
		versionToken, chosen.Value, chosen.ResolvedModel, path,
		inbandMenuPairs(committed), inbandMenuPairs(live))
}

// --- the lock ------------------------------------------------------------------------

// TestInbandMenuDrift_ClassifiesTheChosenRowAgainstTheCommittedCapture is #2045's AC 4:
// all three verdicts, over the capture this repo actually commits, with no claude
// binary, no credentials, no subprocess and no live child.
//
// It reads testdata/initialize_control_v2.1.239.json rather than a synthetic fixture,
// and that is the point rather than a shortcut: the live path reads exactly that file,
// and a table built on a hand-written stand-in would stay green through a capture that
// was deleted, renamed or emptied. The match row is this file's LIVENESS CONTROL —
// remove the capture and it reports no-capture where match is wanted, so the read is
// proven to reach real bytes before any drift row is believed.
func TestInbandMenuDrift_ClassifiesTheChosenRowAgainstTheCommittedCapture(t *testing.T) {
	t.Parallel()

	// The version the committed capture carries, and one that will never be captured.
	// Both go through versionSlug unchanged — every character is already in
	// [a-z0-9._-] — so the minted names are readable in the expectations below.
	const (
		capturedVersion   = "2.1.239"
		uncapturedVersion = "0.0.0-uncaptured"
		capturedPath      = "testdata/initialize_control_v2.1.239.json"
		uncapturedPath    = "testdata/initialize_control_v0.0.0-uncaptured.json"
	)

	// The one bracketed row testdata/initialize_control_v2.1.239.json records, and
	// the pair claude published in its place on 2026-09-02. Both fields differ.
	var (
		captured = inbandMenuRow{Value: "claude-fable-5[1m]", ResolvedModel: "claude-fable-5"}
		observed = inbandMenuRow{Value: "claude-fable-5-1[1m]", ResolvedModel: "claude-fable-5-1"}
	)

	// A live-menu sibling the COMMITTED capture cannot contain: 2.1.220 published an
	// `opus[1m]` row and 2.1.239 dropped it, which the live test's own header records.
	// Its rendered pair is the needle proving the LIVE list reached the verdict —
	// no other argument can produce that string.
	liveOnly := inbandMenuRow{Value: "opus[1m]", ResolvedModel: "claude-opus-5"}

	// The capture's first row, rendered. Nothing but the COMMITTED list can produce
	// it, which is what stops a drift row passing on a verdict that lists neither menu.
	const committedOnlyPair = "default→claude-sonnet-5"
	const liveOnlyPair = "opus[1m]→claude-opus-5"

	tests := []struct {
		name    string
		version string
		chosen  inbandMenuRow
		live    []inbandMenuRow
		want    inbandDriftOutcome
		// wantIn are substrings the rendered verdict must carry. Every entry is
		// checked for emptiness before use: strings.Contains(s, "") is true for
		// every s, so an empty needle asserts nothing while looking like an
		// assertion.
		wantIn []string
	}{
		{
			name:    "the chosen row is the one the capture records",
			version: capturedVersion,
			chosen:  captured,
			live:    []inbandMenuRow{captured, liveOnly},
			want:    inbandDriftMatch,
			wantIn: []string{
				"#2045", capturedVersion, capturedPath,
				"claude-fable-5[1m]", "no menu drift",
			},
		},
		{
			// The literal 2026-09-02 observation. Both fields moved at once, which
			// is why it cannot stand in for either single-field row below.
			name:    "both fields moved — the flap this ticket records",
			version: capturedVersion,
			chosen:  observed,
			live:    []inbandMenuRow{observed, liveOnly},
			want:    inbandDriftDrift,
			wantIn: []string{
				"#2045", capturedVersion, capturedPath, "MENU DRIFT",
				"claude-fable-5-1[1m]", committedOnlyPair, liveOnlyPair,
			},
		},
		{
			// SOLE RED for a comparator keying on value alone: this value IS in the
			// capture, so such a comparator answers match where drift is wanted. The
			// two rows above stay green under it — one matches on both fields, the
			// other's value is absent from the capture either way.
			name:    "the resolution moved under an unchanged value",
			version: capturedVersion,
			chosen:  inbandMenuRow{Value: captured.Value, ResolvedModel: observed.ResolvedModel},
			live:    []inbandMenuRow{{Value: captured.Value, ResolvedModel: observed.ResolvedModel}, liveOnly},
			want:    inbandDriftDrift,
			wantIn: []string{
				"#2045", capturedVersion, capturedPath, "MENU DRIFT",
				committedOnlyPair, liveOnlyPair,
			},
		},
		{
			// SOLE RED for a comparator keying on resolvedModel alone: this
			// resolution IS in the capture. The mirror of the row above, and the
			// reason neither is over-determined — each varies exactly one field.
			name:    "the value moved onto an already-published resolution",
			version: capturedVersion,
			chosen:  inbandMenuRow{Value: observed.Value, ResolvedModel: captured.ResolvedModel},
			live:    []inbandMenuRow{{Value: observed.Value, ResolvedModel: captured.ResolvedModel}, liveOnly},
			want:    inbandDriftDrift,
			wantIn: []string{
				"#2045", capturedVersion, capturedPath, "MENU DRIFT",
				committedOnlyPair, liveOnlyPair,
			},
		},
		{
			// AC 2. The chosen row is the one the capture WOULD have recorded, so
			// nothing about the row itself explains the verdict — only the absent
			// baseline does.
			name:    "no capture is committed for the running version",
			version: uncapturedVersion,
			chosen:  captured,
			live:    []inbandMenuRow{captured, liveOnly},
			want:    inbandDriftNoCapture,
			wantIn: []string{
				"#2045", uncapturedVersion, uncapturedPath, "could not be judged",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, text := inbandCompareMenu(tt.version, tt.chosen, tt.live)
			if got != tt.want {
				t.Errorf("#2045: inbandCompareMenu(%q, %+v) outcome %q, want %q; verdict was: %s",
					tt.version, tt.chosen, got, tt.want, text)
			}
			for _, needle := range tt.wantIn {
				if needle == "" {
					t.Fatalf("#2045: this row carries an empty expected substring, and "+
						"strings.Contains(s, \"\") is true for every s — the row would pass "+
						"against a verdict saying nothing at all (outcome %q)", got)
				}
				if !strings.Contains(text, needle) {
					t.Errorf("#2045: the %q verdict does not mention %q, so an operator meeting "+
						"this red is not told what AC 1 and AC 2 say they must be told; verdict was: %s",
						got, needle, text)
				}
			}
		})
	}
}
