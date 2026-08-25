//go:build e2e_realclaude

package realclaude

// #1762 — the send-point window read of the `initialize` control-request capture:
// one pure function over the lines the recorder already holds, and the offline
// table that carries this slice's entire proof.
//
// # Why the proof is all here and none of it live
//
// The one arm the driver has today writes its control line after a completed
// turn, and the committed initialize_control_v2.1.239.json says exactly what that
// produces: twelve lines with the single `result` at index 9, so the anchor is 10
// and the window is a `control_response` plus one `system`/`background_tasks_changed`
// — no `init` line, no trailer. A live re-run of that arm therefore writes a zero
// count and an EMPTY trailer list, and cannot tell a correct empty window from a
// reader that measured nothing. Every discriminating row is below; the live run
// carries none of them. That asymmetry is why the coverage here is prescribed
// rather than left to taste, and why the non-nil-empty distinction is spelled out
// at both rows that depend on it.
//
// # The input is untrusted, and that shapes the reader rather than decorating it
//
// stdout_events is child output. #1723's security review handed this slice two
// obligations and initControlReadWindow is where both are discharged: a `result`
// line carrying a hostile-shaped `total_cost_usd` must cost the window neither
// that line's entry nor the lines after it, and the presence flag must be read
// from the raw bytes rather than from any decoded value. Every field crossing the
// boundary goes through initControlWindowField, which absorbs a decode failure at
// the FIELD — never at the line, never at the window.
//
// # Offline, and further: no I/O in either direction
//
// This file reaches no live claude, no daemon, no subprocess, no credential and no
// directory. It builds line literals and asserts on a returned struct. It must not
// reach resolveClaudeBin, probeClaudeVersion, WithWorktree, WithWorktreeAuthenticated
// or captureClaudeVersion; nor os.Getenv, os.Environ or os.LookupEnv; nor
// packageDir, any of its wrappers setModeFixturePath, writeSetModeFixture and
// writeFixture, filepath.Glob, or any os read or write. That last group is not
// tidiness and it is the one this file is most tempted by: the committed capture
// under testdata/ is where a reader checks a window read BY HAND, `go test` runs in
// the package source directory, and a relative os.ReadFile("testdata/…") therefore
// reaches it without naming any wrapper — turning a table of literals into a test
// that reads the artifact it exists to justify. TestFinOfflineFilesReachNoExecHelper
// enforces all of it over this file's AST rather than over this paragraph.
//
//	go test -tags e2e_realclaude -race -count=1 -v \
//	  -run 'TestInitControlReadWindow|TestFinOfflineFilesReachNoExecHelper' \
//	  ./internal/e2e/realclaude/
//
// Both must report PASS — not SKIP, not "no tests to run" — on a machine with no
// claude and no credentials. Read the count of tests that executed, never the exit
// code: this package is behind the e2e_realclaude tag, `make check` never compiles
// it, and the suite exits 0 both on a build failure and on a full credentials skip.

import (
	"encoding/json"
	"reflect"
	"testing"
)

// --- the window read ------------------------------------------------------------

// initControlWindow is the read that fills initControlFixtureRecord's
// AfterSendPointSystemInitCount and AfterSendPointResultTrailers. Its fields are
// unexported, matching initControlSummary: reflect.DeepEqual sees unexported
// fields within the package, which is what the table below compares against.
type initControlWindow struct {
	systemInitCount int
	resultTrailers  []initControlResultTrailer
}

// initControlReadWindow reads lines[anchor:] and returns that window's
// `system`/`init` count and one trailer per `result` line, in arrival order.
//
// ITS INPUT IS UNTRUSTED CHILD OUTPUT. []json.RawMessage does not say so, so it is
// said here: these are the bytes claude wrote, and the reader is total over them.
// A line it cannot decode into an object is skipped and the read continues; a
// field it cannot decode leaves its destination at the zero value and the line
// still lands. Nothing here returns an error, and nothing here may grow one — a
// hostile-shaped line must cost the capture nothing.
//
// THE `result` CASE APPENDS EXACTLY ONE TRAILER, UNCONDITIONALLY, whatever the
// state of that line's other fields. len(AfterSendPointResultTrailers) IS the
// window's `result` count and nothing else records it, so a line dropped here
// silently changes a number in a committed artifact and surfaces nowhere.
//
// THE SUBTYPE HALF OF THE `system` TEST IS LOAD-BEARING, not decorative. The pair
// is setModeRecorder.add's — that switch is the definition — and the committed
// initialize_control_v2.1.239.json carries six `system` lines that are not `init`:
// five `thinking_tokens` and one `background_tasks_changed`. A count keyed on
// `type` alone reads 7 over that run where 1 is right.
//
// AN EMPTY WINDOW RETURNS A NON-NIL EMPTY SLICE. A committed `[]` says the window
// was measured and `null` says the field was never filled, and that whole
// distinction lives in the marshalled bytes — the same one initControlFixtureRecord's
// Redaction paragraph draws for an empty census. It costs one initialiser, and a
// `var` declaration silently inverts it.
//
// PRECONDITION: 0 <= anchor <= len(lines), and BOTH EDGES ARE LEGITIMATE — 0 is
// `before_first_turn`'s honest reading and len(lines) means nothing followed the
// send point. There is deliberately no bounds guard: the precondition holds
// structurally at the one call site, where the recorder's line slice is
// append-only and the anchor is its length read before the write, so a clamp would
// add a return site no test row reaches.
func initControlReadWindow(lines []json.RawMessage, anchor int) initControlWindow {
	out := initControlWindow{resultTrailers: []initControlResultTrailer{}}
	for _, raw := range lines[anchor:] {
		var obj map[string]json.RawMessage
		// A decode failure here means the line is not a JSON object — the shape
		// setModeRecorder.add stores for non-JSON child output, which it retains as a
		// JSON string. Skipping it must not cost the window the lines after it. A
		// JSON `null` needs no guard of its own: it decodes into a nil map, carries
		// no `type`, and falls through both cases producing nothing.
		if err := json.Unmarshal(raw, &obj); err != nil {
			continue
		}
		var typ string
		initControlWindowField(obj, "type", &typ)
		switch typ {
		case "system":
			var subtype string
			initControlWindowField(obj, "subtype", &subtype)
			if subtype == "init" {
				out.systemInitCount++
			}
		case "result":
			var tr initControlResultTrailer
			initControlWindowField(obj, "num_turns", &tr.NumTurns)
			// PRESENCE IS READ FROM THE KEY, never from the decoded value: a trailer
			// carrying `"total_cost_usd": 0` and one carrying no cost key are
			// different facts, and TotalCostUSD != 0 collapses them.
			tr.TotalCostUSDPresent = initControlWindowField(obj, "total_cost_usd", &tr.TotalCostUSD)
			out.resultTrailers = append(out.resultTrailers, tr)
		}
	}
	return out
}

// initControlWindowField decodes obj[key] into dst and reports whether the key was
// PRESENT — independently of whether that decode succeeded.
//
// The split is the point. A non-numeric cost reads present with a zero value:
// `"total_cost_usd": "0.02"` carried a cost key, and reporting it absent would
// collapse exactly the two facts TotalCostUSDPresent exists to keep apart, while
// additionally making an unreadable cost indistinguishable from a missing one.
func initControlWindowField(obj map[string]json.RawMessage, key string, dst any) bool {
	raw, ok := obj[key]
	if !ok {
		return false
	}
	_ = json.Unmarshal(raw, dst) // best-effort: a hostile value leaves dst at its zero value on purpose
	return true
}

// --- the offline table ------------------------------------------------------------

// TestInitControlReadWindow_ReadsAnAnchoredWindowOfChildOutput is this slice's whole
// proof. The live arm's window is empty, so it discriminates nothing; every shape
// that separates a correct reader from a plausible one is a row here.
//
// THE ROWS ARE RAW LINE LITERALS, never a marshalled initControlResultTrailer.
// Marshalling the record's own types structurally cannot produce a line missing a
// key, and a line missing `total_cost_usd` is the subject of one row and the
// control for another.
func TestInitControlReadWindow_ReadsAnAnchoredWindowOfChildOutput(t *testing.T) {
	t.Parallel()

	lines := func(raw ...string) []json.RawMessage {
		out := make([]json.RawMessage, 0, len(raw))
		for _, r := range raw {
			out = append(out, json.RawMessage(r))
		}
		return out
	}

	// ONE list, shared by the two anchor rows below, and it must stay one: the pair
	// reads as redundant otherwise. The len(anchorLines) row is the sole red for a
	// reader that ignores the anchor and measures the whole slice; the anchor-0 row
	// is what proves that row's emptiness came from the anchor rather than from a
	// line list with nothing in it to find. An anchor of 0 cannot catch the
	// ignore-the-anchor mutant on its own, since lines[0:] IS the whole slice.
	anchorLines := lines(
		`{"type":"system","subtype":"init","permissionMode":"default"}`,
		`{"type":"result","num_turns":7,"total_cost_usd":0.31}`,
		`{"type":"system","subtype":"thinking_tokens"}`,
		`{"type":"result","num_turns":8}`,
	)

	tests := []struct {
		name   string
		lines  []json.RawMessage
		anchor int
		want   initControlWindow
	}{
		// With the row below, the sole red for a reader deriving the flag from
		// TotalCostUSD != 0: that reader reports this trailer as carrying no cost key.
		{"a cost of zero is PRESENT", lines(`{"type":"result","num_turns":1,"total_cost_usd":0}`), 0,
			initControlWindow{resultTrailers: []initControlResultTrailer{{NumTurns: 1, TotalCostUSDPresent: true}}}},

		// The other half of that pair: same zero value, absent key, different fact.
		{"no cost key is ABSENT", lines(`{"type":"result","num_turns":2}`), 0,
			initControlWindow{resultTrailers: []initControlResultTrailer{{NumTurns: 2}}}},

		// Sole red for a reader that aborts the line — or the window — on a per-field
		// decode error. Such a reader drops the entry, and the window's `result` count
		// then reads 0 where 1 is right, in a committed artifact, with nothing else
		// recording the number.
		{"a cost that is not a number is present with a zero value",
			lines(`{"type":"result","num_turns":3,"total_cost_usd":"0.02"}`), 0,
			initControlWindow{resultTrailers: []initControlResultTrailer{{NumTurns: 3, TotalCostUSDPresent: true}}}},

		// Sole red for a count keyed on `type` alone, which reads 2 here — and 7 over
		// the committed capture, where 1 is right.
		{"a system line with another subtype is not counted",
			lines(`{"type":"system","subtype":"thinking_tokens"}`, `{"type":"system","subtype":"init"}`), 0,
			initControlWindow{systemInitCount: 1, resultTrailers: []initControlResultTrailer{}}},

		// The recorder's non-JSON shape, a bare JSON string. The FOLLOWING line is
		// what makes the row discriminate: without it, "skipped the line" and "aborted
		// the whole window" produce the same answer.
		{"a non-object line is skipped and the window continues",
			lines(`"claude: unparseable output"`, `{"type":"result","num_turns":4,"total_cost_usd":0.5}`), 0,
			initControlWindow{resultTrailers: []initControlResultTrailer{{NumTurns: 4, TotalCostUSD: 0.5, TotalCostUSDPresent: true}}}},

		// Distinguishable on BOTH fields, so a reader that reversed, deduplicated or
		// overwrote is red. reflect.DeepEqual over the slice is what pins arrival order.
		{"two result lines arrive in order",
			lines(`{"type":"result","num_turns":5,"total_cost_usd":0.11}`, `{"type":"result","num_turns":6,"total_cost_usd":0.22}`), 0,
			initControlWindow{resultTrailers: []initControlResultTrailer{
				{NumTurns: 5, TotalCostUSD: 0.11, TotalCostUSDPresent: true},
				{NumTurns: 6, TotalCostUSD: 0.22, TotalCostUSDPresent: true},
			}}},

		// Anchor 0 — `before_first_turn`'s value, and the control for the row below.
		{"an anchor of zero reads every line", anchorLines, 0,
			initControlWindow{systemInitCount: 1, resultTrailers: []initControlResultTrailer{
				{NumTurns: 7, TotalCostUSD: 0.31, TotalCostUSDPresent: true},
				{NumTurns: 8},
			}}},

		// Anchor == line count over that same list, which has an `init` line and two
		// trailers to find. resultTrailers is spelled out as an empty literal on
		// purpose: reflect.DeepEqual treats nil and []initControlResultTrailer{} as
		// different, and this is the ONLY instrument for the non-nil-empty contract —
		// a `want` written as initControlWindow{} carries a NIL slice and would pass
		// for a reader returning nil while failing for the correct one.
		{"an anchor at the line count reads an empty, measured window", anchorLines, len(anchorLines),
			initControlWindow{resultTrailers: []initControlResultTrailer{}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := initControlReadWindow(tc.lines, tc.anchor); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("#1762: read %+v, want %+v; this is what a committed capture says followed "+
					"the control request — the window's `result` count is the length of that "+
					"trailer list and is recorded nowhere else", got, tc.want)
			}
		})
	}
}
