package streamsup

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// resultLineWith mints one stream-json result line carrying the given modelUsage
// FRAGMENT verbatim. The fragment is spliced in as raw JSON rather than marshalled
// from a Go value, which is the whole point: the hostile rows below need shapes no
// Go type can hold (a bare number, an array, an object of numbers), and a fixture
// built through encoding/json could not express them.
//
// An empty fragment omits the key entirely — AC 2's absent case, which is also what
// the fake-claude harness emits today.
func resultLineWith(subtype, modelUsage string) string {
	line := `{"type":"result","subtype":"` + subtype + `"`
	if modelUsage != "" {
		line += `,"modelUsage":` + modelUsage
	}
	return line + `}`
}

// modelUsageFragment builds a well-formed modelUsage object from id/window pairs,
// in the order given. Ordering is the caller's because the JSON object's order is
// NOT what the decode preserves — several rows below feed a deliberately
// non-alphabetical order to prove the emitted slice is sorted rather than echoed.
func modelUsageFragment(pairs ...any) string {
	var b strings.Builder
	b.WriteString("{")
	for i := 0; i < len(pairs); i += 2 {
		if i > 0 {
			b.WriteString(",")
		}
		id, _ := json.Marshal(pairs[i])
		fmt.Fprintf(&b, `%s:{"contextWindow":%v}`, id, pairs[i+1])
	}
	b.WriteString("}")
	return b.String()
}

// parseOneLine runs one line through a fresh parser and returns everything it
// emitted. A fresh parser per call keeps the rows independent — the parser is
// long-lived in production, but nothing this decode does carries across lines and
// a shared one would let a row's failure cascade into its neighbours.
func parseOneLine(t *testing.T, line string) []turnevent.Event {
	t.Helper()
	var events []turnevent.Event
	p := NewParser(func(ev turnevent.Event) { events = append(events, ev) }, slog.Default())
	if _, err := p.Write([]byte(line + "\n")); err != nil {
		t.Fatalf("Write err = %v, want nil", err)
	}
	return events
}

// turnEndFrom asserts the line produced exactly one event and that it is a
// TurnEnd, returning it. Every row in this file goes through here: a decode that
// suppressed the turn boundary, or emitted a second event beside it, is a failure
// of AC 4 no per-field assertion below would catch.
func turnEndFrom(t *testing.T, events []turnevent.Event) turnevent.TurnEnd {
	t.Helper()
	if len(events) != 1 {
		t.Fatalf("event count: got %d, want exactly 1 TurnEnd: %#v", len(events), events)
	}
	end, ok := events[0].(turnevent.TurnEnd)
	if !ok {
		t.Fatalf("event type: got %T, want turnevent.TurnEnd", events[0])
	}
	return end
}

// capturedResultLine returns the FIRST result line of one committed capture, as
// claude wrote it. The bytes are taken as json.RawMessage rather than decoded and
// re-marshalled: a round trip through map[string]any would normalise key order and
// round every number through float64, so the fixture would stop being the capture
// and start being this test's opinion of it.
//
// json.Compact is the ONE transformation applied, and it is the harness's doing
// rather than the fixture's: the capture file is stored indented, and the parser
// splits its input on newlines, so an indented object would arrive as a hundred
// undecodable fragments. Compact removes whitespace BETWEEN tokens only — key
// order, every numeric literal and every string byte survive it — so what reaches
// the parser is still claude's own line.
func capturedResultLine(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(initCaptureDir, name))
	if err != nil {
		t.Fatalf("reading capture %s: %v", name, err)
	}
	var record struct {
		StdoutEvents []json.RawMessage `json:"stdout_events"`
	}
	if err := json.Unmarshal(raw, &record); err != nil {
		t.Fatalf("decoding capture %s: %v", name, err)
	}
	for _, ev := range record.StdoutEvents {
		var kind struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(ev, &kind); err != nil {
			continue
		}
		if kind.Type == "result" {
			var one bytes.Buffer
			if err := json.Compact(&one, ev); err != nil {
				t.Fatalf("compacting the captured result line of %s: %v", name, err)
			}
			return one.Bytes()
		}
	}
	t.Fatalf("capture %s carries no result line, so every assertion below would be vacuous", name)
	return nil
}

// capturedModelUsage reads one capture's result line back through literal key
// strings and returns its modelUsage as id→window. It is the VACUITY GUARD the
// #1828 collapse lesson calls for: without it, a row asserting "both entries
// reached the event" is satisfied by a fixture that only ever carried one, and the
// test would prove one wire shape twice.
func capturedModelUsage(t *testing.T, line []byte) map[string]int {
	t.Helper()
	var decoded struct {
		ModelUsage map[string]struct {
			ContextWindow *int `json:"contextWindow"`
		} `json:"modelUsage"`
	}
	if err := json.Unmarshal(line, &decoded); err != nil {
		t.Fatalf("decoding the captured result line: %v", err)
	}
	out := make(map[string]int, len(decoded.ModelUsage))
	for id, entry := range decoded.ModelUsage {
		if entry.ContextWindow == nil {
			t.Fatalf("captured entry %q carries no contextWindow; this capture cannot pin the decode", id)
		}
		out[id] = *entry.ContextWindow
	}
	if len(out) < 2 {
		t.Fatalf("captured modelUsage has %d entries, want at least 2 — a single-entry fixture cannot "+
			"show that two entries reach the event under their OWN ids", len(out))
	}
	return out
}

// TestParser_ResultModelUsage_CapturePin is the ticket's central claim measured
// against claude's own bytes: the two-model arm, whose sonnet entry reports 1M while
// its haiku helper reports 200K, and an alias arm, whose two entries name ONE model
// under two ids with the identical window.
//
// The alias arm is not redundant with the two-model one. It is the row that fails a
// decode which deduplicates by window, or which treats "two entries" as "a helper
// plus a session model" and tries to pick one — both of which the two-model arm
// alone would pass.
func TestParser_ResultModelUsage_CapturePin(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		capture string
	}{
		{"two models, 200K beside 1M", "permission_mode_switch_v2.1.239_dontAsk.json"},
		{"alias pair, one model under two ids", "permission_protocol_v2.1.143_default.json"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			line := capturedResultLine(t, tc.capture)
			want := capturedModelUsage(t, line)

			end := turnEndFrom(t, parseOneLine(t, string(line)))
			if end.DroppedModelWindows != 0 {
				t.Errorf("DroppedModelWindows = %d, want 0 — nothing in a real capture is over any cap",
					end.DroppedModelWindows)
			}
			got := make(map[string]int, len(end.ModelWindows))
			for _, w := range end.ModelWindows {
				if _, dup := got[w.ModelID]; dup {
					t.Errorf("model id %q reported twice", w.ModelID)
				}
				got[w.ModelID] = w.WindowTokens
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("ModelWindows = %v, want %v — every captured entry must reach the event under "+
					"its own id, neither collapsed into nor overriding the other", got, want)
			}
			// Sorted by id, which is what makes the slice reproducible across runs.
			for i := 1; i < len(end.ModelWindows); i++ {
				if end.ModelWindows[i-1].ModelID >= end.ModelWindows[i].ModelID {
					t.Errorf("ModelWindows not sorted by id: %+v", end.ModelWindows)
				}
			}
		})
	}
}

// TestParser_ResultModelUsage_UndecodableReportsNothing covers AC 2's absent and
// empty inputs and AC 4's hostile-type one together, because they share an
// expectation: no window, no drop count, and a turn that still ends normally.
//
// The dropped count is 0 rather than "however many entries there were" on every
// row here BY CONSTRUCTION: nothing decoded, so there is no entry to have counted.
func TestParser_ResultModelUsage_UndecodableReportsNothing(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		fragment string
	}{
		{"key absent entirely", ""},
		{"explicit null", `null`},
		{"empty object", `{}`},
		{"a number", `1000000`},
		{"a string", `"claude-sonnet-5"`},
		{"a bool", `true`},
		{"an array", `[{"contextWindow":1000000}]`},
		{"an object of numbers", `{"claude-sonnet-5":1000000}`},
		{"an object of strings", `{"claude-sonnet-5":"1000000"}`},
		{"contextWindow is a string", `{"claude-sonnet-5":{"contextWindow":"1000000"}}`},
		{"contextWindow is an object", `{"claude-sonnet-5":{"contextWindow":{"tokens":1}}}`},
		{"contextWindow past int64", `{"claude-sonnet-5":{"contextWindow":99999999999999999999}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			end := turnEndFrom(t, parseOneLine(t, resultLineWith("success", tc.fragment)))
			if end.ModelWindows != nil {
				t.Errorf("ModelWindows = %+v, want nil", end.ModelWindows)
			}
			if end.DroppedModelWindows != 0 {
				t.Errorf("DroppedModelWindows = %d, want 0 — nothing decoded, so nothing was dropped",
					end.DroppedModelWindows)
			}
			if end.Reason != turnevent.TurnEndReasonEndTurn {
				t.Errorf("Reason = %q, want %q — the modelUsage decode must not disturb segmentation",
					end.Reason, turnevent.TurnEndReasonEndTurn)
			}
		})
	}
}

// TestParser_ResultModelUsage_UnusableWindowsAreDropped is AC 2's per-entry half:
// an entry whose window is absent, zero or negative reports NO window rather than a
// window of zero, and is counted.
//
// Every row pairs the unusable entry with a usable one, which is what keeps it from
// passing against a decode that gave up on the whole map at the first bad entry.
func TestParser_ResultModelUsage_UnusableWindowsAreDropped(t *testing.T) {
	t.Parallel()
	const good = `"claude-sonnet-5":{"contextWindow":1000000}`
	for _, tc := range []struct {
		name string
		bad  string
	}{
		{"contextWindow absent", `"claude-haiku-4-5":{"maxOutputTokens":32000}`},
		{"contextWindow null", `"claude-haiku-4-5":{"contextWindow":null}`},
		{"contextWindow zero", `"claude-haiku-4-5":{"contextWindow":0}`},
		{"contextWindow negative", `"claude-haiku-4-5":{"contextWindow":-1}`},
		{"entry is an empty object", `"claude-haiku-4-5":{}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			end := turnEndFrom(t, parseOneLine(t, resultLineWith("success", "{"+tc.bad+","+good+"}")))
			want := []turnevent.ModelWindow{{ModelID: "claude-sonnet-5", WindowTokens: 1000000}}
			if !reflect.DeepEqual(end.ModelWindows, want) {
				t.Errorf("ModelWindows = %+v, want %+v — the usable entry survives alone",
					end.ModelWindows, want)
			}
			if end.DroppedModelWindows != 1 {
				t.Errorf("DroppedModelWindows = %d, want 1", end.DroppedModelWindows)
			}
		})
	}
}

// TestParser_ResultModelUsage_OverlongIDsAreDroppedNotCut is AC 3's sharpest
// clause. A cut id names no model, and #2102 joins on the id, so the entry is
// dropped whole rather than reported under a prefix.
//
// The far-over row's id is built from a repeated marker so a truncating
// implementation is caught by the SHAPE of what it emitted, not only by its length:
// any prefix of that id is still a string this assertion rejects by exact match.
func TestParser_ResultModelUsage_OverlongIDsAreDroppedNotCut(t *testing.T) {
	t.Parallel()
	atCap := strings.Repeat("m", maxModelWindowID)
	overByOne := strings.Repeat("m", maxModelWindowID+1)
	farOver := strings.Repeat("model-", maxModelWindowID)

	for _, tc := range []struct {
		name        string
		id          string
		wantCarried bool
	}{
		{"exactly at the cap", atCap, true},
		{"one byte over", overByOne, false},
		{"far over", farOver, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			end := turnEndFrom(t, parseOneLine(t, resultLineWith("success",
				modelUsageFragment(tc.id, 1000000))))
			if tc.wantCarried {
				want := []turnevent.ModelWindow{{ModelID: tc.id, WindowTokens: 1000000}}
				if !reflect.DeepEqual(end.ModelWindows, want) {
					t.Errorf("ModelWindows = %+v, want the id carried whole", end.ModelWindows)
				}
				if end.DroppedModelWindows != 0 {
					t.Errorf("DroppedModelWindows = %d, want 0", end.DroppedModelWindows)
				}
				return
			}
			if end.ModelWindows != nil {
				t.Errorf("ModelWindows = %+v, want nil — an over-long id is DROPPED, never reported "+
					"under a cut id", end.ModelWindows)
			}
			if end.DroppedModelWindows != 1 {
				t.Errorf("DroppedModelWindows = %d, want 1", end.DroppedModelWindows)
			}
		})
	}
}

// TestParser_ResultModelUsage_EntryCountIsCapped is AC 3's other dimension.
//
// EVERY NUMBER IN THE TABLE IS A LITERAL, and that is the whole reason the table
// pins anything. The first draft wrote them as maxModelWindowEntries-1, the
// constant itself, +1 and *4, with the expectation derived from the same constant
// — so an overlay moving the cap to 15 moved the fixture and the expectation in
// lockstep and the table stayed GREEN. Measured, not reasoned: the mutant survived
// a full run. A cap's own test cannot be written in terms of the cap. With
// literals, that same overlay reddens the 16, 17 and 64 rows, and so does a raise.
// The cost is that a deliberate re-derivation of the cap must edit this table,
// which is the point rather than the price.
//
// The "exactly at the cap" row does NOT prove `>` against `>=`: at len == cap the
// guarded block computes a zero drop and slices to identity either way, and no
// report flag rides inside it, so the two are an equivalent mutant there — the
// streamsup lesson from #1812, applied rather than re-learned. What it pins is the
// cap's VALUE, per the paragraph above.
//
// Ids are minted zero-padded so their lexicographic order is their numeric one,
// which is what lets the over-cap rows assert exactly WHICH entries survived rather
// than only how many.
func TestParser_ResultModelUsage_EntryCountIsCapped(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name        string
		n           int
		wantKept    int
		wantDropped int
	}{
		{"one under the cap", 15, 15, 0},
		{"exactly at the cap", 16, 16, 0},
		{"one over the cap", 17, 16, 1},
		{"far over the cap", 64, 16, 48},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			pairs := make([]any, 0, tc.n*2)
			for i := range tc.n {
				pairs = append(pairs, fmt.Sprintf("model-%04d", i), 200000+i)
			}
			end := turnEndFrom(t, parseOneLine(t, resultLineWith("success", modelUsageFragment(pairs...))))

			if len(end.ModelWindows) != tc.wantKept {
				t.Fatalf("len(ModelWindows) = %d, want %d", len(end.ModelWindows), tc.wantKept)
			}
			if end.DroppedModelWindows != tc.wantDropped {
				t.Errorf("DroppedModelWindows = %d, want %d", end.DroppedModelWindows, tc.wantDropped)
			}
			// Truncation is from the TAIL of the sorted order, so the survivors are the
			// lowest-sorting ids — model-0000 upward, with their own windows intact.
			for i, w := range end.ModelWindows {
				wantID := fmt.Sprintf("model-%04d", i)
				if w.ModelID != wantID || w.WindowTokens != 200000+i {
					t.Errorf("ModelWindows[%d] = %+v, want {%s %d}", i, w, wantID, 200000+i)
				}
			}
		})
	}
}

// TestParser_ResultModelUsage_EveryDropCauseCountsOnce pins the single-counter
// invariant: len(ModelWindows) + DroppedModelWindows equals what claude sent,
// whatever mix of causes did the dropping.
//
// One map carries all three causes at once, which is what separates a counter that
// totals every cause from one that reports only the last, only the largest, or only
// how many CAUSES fired — three wrong shapes that a single-cause fixture cannot
// tell apart.
func TestParser_ResultModelUsage_EveryDropCauseCountsOnce(t *testing.T) {
	t.Parallel()
	const (
		unusable   = 3
		overlongID = 2
	)
	// Enough usable entries to overflow the cap on their own, so the over-cap cause
	// fires alongside the other two rather than instead of them.
	usable := maxModelWindowEntries + 5

	pairs := make([]any, 0, (usable+unusable+overlongID)*2)
	for i := range usable {
		pairs = append(pairs, fmt.Sprintf("good-%04d", i), 200000+i)
	}
	for i := range unusable {
		pairs = append(pairs, fmt.Sprintf("zero-%04d", i), 0)
	}
	for i := range overlongID {
		pairs = append(pairs, strings.Repeat("x", maxModelWindowID+1)+fmt.Sprint(i), 1000000)
	}
	sent := usable + unusable + overlongID

	end := turnEndFrom(t, parseOneLine(t, resultLineWith("success", modelUsageFragment(pairs...))))
	if len(end.ModelWindows) != maxModelWindowEntries {
		t.Fatalf("len(ModelWindows) = %d, want %d", len(end.ModelWindows), maxModelWindowEntries)
	}
	if got, want := len(end.ModelWindows)+end.DroppedModelWindows, sent; got != want {
		t.Errorf("len(ModelWindows)+DroppedModelWindows = %d, want %d (what claude sent) — the counter "+
			"must total EVERY cause, not the last or the largest one", got, want)
	}
	// The unusable and over-long entries are gone rather than merely uncounted: an
	// entry with a zero window or a cut id must not appear at all.
	for _, w := range end.ModelWindows {
		if w.WindowTokens <= 0 {
			t.Errorf("entry %q carries a non-positive window %d", w.ModelID, w.WindowTokens)
		}
		if len(w.ModelID) > maxModelWindowID {
			t.Errorf("entry %q is longer than the id cap", w.ModelID)
		}
		if !strings.HasPrefix(w.ModelID, "good-") {
			t.Errorf("entry %q survived but is not one of the usable ones", w.ModelID)
		}
	}
}

// TestParser_ResultModelUsage_IsDeterministic is the assertion the map-versus-slice
// decision exists for. Go randomises map iteration, so without the sort both the
// ORDER of the emitted slice and — once the cap fires — WHICH entries are in it
// differ between runs on identical bytes. Measured: dropping the SortFunc call
// reddens this test.
//
// The line is over-cap on purpose, and the reason is narrower than "otherwise it
// proves nothing": an under-cap line already catches order nondeterminism, since
// every entry survives but in a shuffled slice. What only an over-cap line catches
// is MEMBERSHIP nondeterminism, which is the failure the cut introduces and the
// more damaging of the two — a consumer joining on ModelID would find a different
// model missing on each run.
func TestParser_ResultModelUsage_IsDeterministic(t *testing.T) {
	t.Parallel()
	pairs := make([]any, 0, maxModelWindowEntries*8)
	for i := range maxModelWindowEntries * 4 {
		pairs = append(pairs, fmt.Sprintf("model-%04d", i), 200000+i)
	}
	line := resultLineWith("success", modelUsageFragment(pairs...))

	first := turnEndFrom(t, parseOneLine(t, line))
	for i := range 24 {
		got := turnEndFrom(t, parseOneLine(t, line))
		if !reflect.DeepEqual(got, first) {
			t.Fatalf("run %d differs from the first: got %+v, want %+v — the emitted set must not "+
				"depend on Go's map iteration order", i, got, first)
		}
	}
}

// TestParser_ResultSegmentationIsUnchanged is AC 4 in full: for every subtype the
// ticket names, the turn still ends with today's reason whatever the modelUsage
// decode does — including a modelUsage that does not decode at all.
//
// The subtype and the fragment are varied INDEPENDENTLY rather than paired, so a
// decode that swallowed the line would have to do so on every combination to pass.
func TestParser_ResultSegmentationIsUnchanged(t *testing.T) {
	t.Parallel()
	fragments := map[string]string{
		"absent":      "",
		"well formed": modelUsageFragment("claude-sonnet-5", 1000000),
		"hostile":     `[[["contextWindow"]]]`,
		"over cap":    modelUsageFragment("a", 1, "b", 2, "c", 3),
	}
	for _, subtype := range []struct {
		subtype string
		want    turnevent.TurnEndReason
	}{
		{"success", turnevent.TurnEndReasonEndTurn},
		{"error_during_execution", turnevent.TurnEndReasonCancelled},
		{"error_max_turns", turnevent.TurnEndReasonEndTurn},
	} {
		for name, fragment := range fragments {
			t.Run(subtype.subtype+"/"+name, func(t *testing.T) {
				t.Parallel()
				end := turnEndFrom(t, parseOneLine(t, resultLineWith(subtype.subtype, fragment)))
				if end.Reason != subtype.want {
					t.Errorf("Reason = %q, want %q", end.Reason, subtype.want)
				}
			})
		}
	}
}

// TestParser_ResultModelUsageIsLoggedContentFree is AC 5. It asserts ZERO records
// rather than sweeping the records for sentinels, which is the stronger claim: a
// value that reaches no slog call cannot leak from one, and no later attribute can
// reintroduce the leak without reddening this count.
//
// The sentinels are swept anyway, because the count alone would not catch a record
// emitted by some OTHER path on the same line.
func TestParser_ResultModelUsageIsLoggedContentFree(t *testing.T) {
	t.Parallel()
	const (
		sentinelID     = "model-sentinel-210100"
		sentinelWindow = "987654321"
	)
	rec := &logRecorder{}
	var events []turnevent.Event
	p := NewParser(func(ev turnevent.Event) { events = append(events, ev) }, slog.New(rec))

	lines := []string{
		// The emit path, then all three drop paths, then the undecodable one — every
		// route a model id or a window value could travel.
		resultLineWith("success", modelUsageFragment(sentinelID, sentinelWindow)),
		resultLineWith("success", modelUsageFragment(sentinelID+"-zero", 0)),
		resultLineWith("success", modelUsageFragment(strings.Repeat(sentinelID, 40), sentinelWindow)),
		resultLineWith("success", modelUsageFragment(
			sentinelID+"-a", 1, sentinelID+"-b", 2, sentinelID+"-c", 3)),
		resultLineWith("success", `{"`+sentinelID+`":`+sentinelWindow+`}`),
		string(capturedResultLine(t, "permission_mode_switch_v2.1.239_dontAsk.json")),
	}
	for _, line := range lines {
		if _, err := p.Write([]byte(line + "\n")); err != nil {
			t.Fatalf("Write err = %v, want nil", err)
		}
	}
	if len(events) != len(lines) {
		t.Fatalf("event count: got %d, want %d (one TurnEnd per line)", len(events), len(lines))
	}
	if all := rec.all(); len(all) != 0 {
		t.Fatalf("records: got %d, want 0 — this path logs nothing at all, which is what makes AC 5 "+
			"structural rather than a per-attribute check: %+v", len(all), all)
	}
	// The capture's own ids are swept too: a record naming claude-sonnet-5 would leak
	// a real model id even though it carries no sentinel.
	leaks := []string{sentinelID, sentinelWindow, "claude-sonnet-5", "claude-haiku-4-5", "1000000"}
	for _, r := range rec.all() {
		for _, leak := range leaks {
			if strings.Contains(r.msg, leak) {
				t.Errorf("record message carries claude-derived content (%q): %q", leak, r.msg)
			}
			for k, v := range r.attrs {
				if strings.Contains(v, leak) {
					t.Errorf("record %q attr %q carries claude-derived content (%q)", r.msg, k, leak)
				}
			}
		}
	}
}
