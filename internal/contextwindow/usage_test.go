package contextwindow

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRead_Fixtures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		fixture    string
		wantUsed   int
		wantWindow int
	}{
		{
			// AC-1: report the sum of the LATEST usage-bearing assistant
			// entry's four fields — not the first, not a running total. The
			// fixture's trailing assistant entry carries no usage block (a
			// transitional thinking-block resolution); it must not clear the
			// remembered value. Last usage entry sums to
			// 5000+800+1500+200 = 7500.
			name:       "latest turn sum, trailing no-usage assistant ignored",
			fixture:    "latest_turn.jsonl",
			wantUsed:   7500,
			wantWindow: defaultWindowTokens,
		},
		{
			// AC-3: after an auto-compaction the latest turn's input shrinks,
			// so last-usage-wins reports the smaller, current figure — the
			// reset surfaces for free with no dedicated marker. Earlier turn
			// sums to 150000; later turn to 25000+1500+3000+500 = 30000.
			name:       "compaction reset reports smaller later figure",
			fixture:    "compaction_reset.jsonl",
			wantUsed:   30000,
			wantWindow: defaultWindowTokens,
		},
		{
			// AC-4: a transcript that exists but has no assistant/usage entry
			// yet (a fresh session before the first turn completes) reports a
			// deterministic zero rather than erroring.
			name:       "no usage entry yet reports zero",
			fixture:    "no_usage.jsonl",
			wantUsed:   0,
			wantWindow: defaultWindowTokens,
		},
		{
			// #2100: a sum above the believed window is proof the belief is
			// wrong, so the window collapses to 0 — "no trustworthy reading" —
			// while the used count still carries the true sum. The numbers are
			// the ones observed live on 2026-09-04 against a 1M-window session:
			// 2+950+221118+1005 = 223075, which against 200000 is 111%.
			name:       "used above the believed window reports no window",
			fixture:    "over_window.jsonl",
			wantUsed:   223075,
			wantWindow: 0,
		},
		{
			// #2100, the other side of the boundary: equality is not a
			// contradiction. A session exactly at its window is full, not
			// evidence of a wrong belief, and keeps its window. Sums to
			// 150000+20000+25000+5000 = 200000 exactly.
			name:       "used exactly at the believed window keeps it",
			fixture:    "exactly_window.jsonl",
			wantUsed:   defaultWindowTokens,
			wantWindow: defaultWindowTokens,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// nil windows on EVERY row: this table is the regression floor for
			// #2107's AC 3 — with nothing observed, the reading is exactly what
			// it was before the join existed, #2100's collapse included. The
			// join's own rows live in TestRead_Join below.
			got, err := Read(filepath.Join("testdata", tt.fixture), nil)
			if err != nil {
				t.Fatalf("Read(%s): %v", tt.fixture, err)
			}
			if got.UsedTokens != tt.wantUsed {
				t.Errorf("UsedTokens = %d, want %d", got.UsedTokens, tt.wantUsed)
			}
			// #856 AC-2 reported the default for every non-error case; #2100
			// makes the want per-row, because the one case that cannot report a
			// window is a used count that disproves it. Asserting the shared
			// default here would make an over-window fixture unappendable.
			if got.WindowTokens != tt.wantWindow {
				t.Errorf("WindowTokens = %d, want %d", got.WindowTokens, tt.wantWindow)
			}
		})
	}
}

func TestRead_EmptyPath(t *testing.T) {
	t.Parallel()

	// AC-4: the resolver's ("", 0, nil) "no transcript resolved yet" signal
	// maps to a deterministic zero usage without opening anything.
	// The windows map is deliberately NON-EMPTY: no transcript means no model,
	// so there is nothing to join on and an observed window must not be reported
	// for a session whose latest entry was never read (#2107).
	got, err := Read("", map[string]int{"claude-sonnet-5": 1_000_000})
	if err != nil {
		t.Fatalf("Read(\"\"): %v", err)
	}
	want := Usage{UsedTokens: 0, WindowTokens: defaultWindowTokens}
	if got != want {
		t.Errorf("Read(\"\") = %+v, want %+v", got, want)
	}
}

func TestRead_OpenError(t *testing.T) {
	t.Parallel()

	// A non-empty path that cannot be opened is a genuine I/O failure, not a
	// "nothing to report" case — it surfaces as an error with a zero Usage,
	// so a consumer can distinguish "fresh session" from "couldn't read".
	got, err := Read(filepath.Join("testdata", "does-not-exist.jsonl"), nil)
	if err == nil {
		t.Fatalf("Read on missing path: want error, got nil (usage %+v)", got)
	}
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("error = %v, want fs.ErrNotExist", err)
	}
	if got != (Usage{}) {
		t.Errorf("Usage = %+v, want zero value", got)
	}
}

// TestRead_Join covers #2107's join: the window reported is the one observed for
// the model named on the LATEST usage-bearing entry, and every other shape is a
// miss that falls back to the default.
//
// Every fixture below sums to 223075 — the figure measured live on 2026-09-04 —
// so a row's wantWindow alone says whether the join fired, and #2100's collapse
// to 0 is visible on exactly the rows where the resolved window is below it.
func TestRead_Join(t *testing.T) {
	t.Parallel()

	// The alias pair, verbatim from permission_mode_switch_v2.1.239_plan.json:
	// two spellings claude keys the same model under, at the same window. No rule
	// relates the two, so the join must answer under EITHER.
	const (
		haikuDated   = "claude-haiku-4-5-20251001"
		haikuUndated = "claude-haiku-4-5"
	)

	tests := []struct {
		name       string
		fixture    string
		windows    map[string]int
		wantUsed   int
		wantWindow int
	}{
		{
			// AC 1: the join proper. over_window.jsonl names claude-opus-5 and
			// sums to 223075; observed at 1M, that is a true 22% rather than the
			// "no trustworthy reading" #2100 reports without it.
			name:       "observed window for the named model is reported",
			fixture:    "over_window.jsonl",
			windows:    map[string]int{"claude-opus-5": 1_000_000},
			wantUsed:   223075,
			wantWindow: 1_000_000,
		},
		{
			// AC 4: the contradiction check runs against the RESOLVED window, so
			// it does not fire at 1M. Its companion row below shows it still
			// fires when the resolved window is genuinely too small — the pair is
			// what pins the ORDER, since a check-then-resolve implementation
			// would collapse the row above to 0.
			name:       "an observed window below the used count still collapses",
			fixture:    "over_window.jsonl",
			windows:    map[string]int{"claude-opus-5": 200_000},
			wantUsed:   223075,
			wantWindow: 0,
		},
		{
			// AC 3, the miss: a map that names only OTHER models leaves the
			// reading exactly as it was — the default, collapsed by #2100.
			name:       "a model no window was observed for falls back",
			fixture:    "over_window.jsonl",
			windows:    map[string]int{"claude-sonnet-5": 1_000_000, haikuDated: 200_000},
			wantUsed:   223075,
			wantWindow: 0,
		},
		{
			// AC 2, the key: two models observed for one session, and the answer
			// follows which one the LATEST entry names. This row and the next are
			// the same map over transcripts differing only in that entry's model,
			// which is what kills "the largest", "the first", "the last observed"
			// and "the only one" together.
			name:    "two models observed, latest entry names the 1M one",
			fixture: "two_models_latest_sonnet.jsonl",
			windows: map[string]int{
				haikuDated:        200_000,
				"claude-sonnet-5": 1_000_000,
			},
			wantUsed:   223075,
			wantWindow: 1_000_000,
		},
		{
			name:    "two models observed, latest entry names the 200K one",
			fixture: "two_models_latest_haiku.jsonl",
			windows: map[string]int{
				haikuDated:        200_000,
				"claude-sonnet-5": 1_000_000,
			},
			wantUsed:   223075,
			wantWindow: 0, // 223075 > 200000 — #2100 collapses the RESOLVED window
		},
		{
			// AC 2, the alias pair: one model keyed twice at one window answers
			// with that window whichever spelling the transcript used. Here the
			// transcript names the dated form and the map carries both.
			name:    "alias pair answers under the dated spelling",
			fixture: "two_models_latest_haiku.jsonl",
			windows: map[string]int{
				haikuDated:   1_000_000,
				haikuUndated: 1_000_000,
			},
			wantUsed:   223075,
			wantWindow: 1_000_000,
		},
		{
			// The other half of the alias pair: the map carries ONLY the undated
			// spelling while the transcript names the dated one. A miss, because
			// the join is verbatim — no date stripping, no canonicalModel lookup,
			// no alias expansion. Claude keys the map by whatever string each
			// caller used, and no rule relating the two survives the data.
			name:       "the other alias spelling alone does not match",
			fixture:    "two_models_latest_haiku.jsonl",
			windows:    map[string]int{haikuUndated: 1_000_000},
			wantUsed:   223075,
			wantWindow: 0,
		},
		{
			// An entry with no message.model names no model, so it must not join
			// against a report entry keyed by the empty string — which #2101
			// retains when its window is positive, and which sorts FIRST in the
			// retained report. An exact string match without this rule would pair
			// two independent empties into a confident wrong answer.
			name:       "an entry with no model does not match an empty-keyed window",
			fixture:    "no_model.jsonl",
			windows:    map[string]int{"": 1_000_000},
			wantUsed:   223075,
			wantWindow: 0,
		},
		{
			// The same rule from the other side: a named entry against a map
			// carrying only the empty key is a miss, not a fallback-to-the-only-
			// entry.
			name:       "a named entry does not match an empty-keyed window",
			fixture:    "over_window.jsonl",
			windows:    map[string]int{"": 1_000_000},
			wantUsed:   223075,
			wantWindow: 0,
		},
		{
			// A non-positive value is not a window. The producer never emits one
			// (#2101 drops contextWindow <= 0), but Read takes a caller-supplied
			// map on an exported signature and defends its own contract.
			name:       "a non-positive observed value is treated as absent",
			fixture:    "over_window.jsonl",
			windows:    map[string]int{"claude-opus-5": 0},
			wantUsed:   223075,
			wantWindow: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := Read(filepath.Join("testdata", tt.fixture), tt.windows)
			if err != nil {
				t.Fatalf("Read(%s): %v", tt.fixture, err)
			}
			if got.UsedTokens != tt.wantUsed {
				t.Errorf("UsedTokens = %d, want %d", got.UsedTokens, tt.wantUsed)
			}
			if got.WindowTokens != tt.wantWindow {
				t.Errorf("WindowTokens = %d, want %d", got.WindowTokens, tt.wantWindow)
			}
		})
	}
}

// TestRead_VariantJoin covers #2118's fallback: after an exact match has MISSED,
// a windows key that is the transcript's model id plus exactly ONE trailing
// bracket group answers the join, and every other spelling is still a miss.
//
// It is a table of its own rather than rows appended to TestRead_Join, because
// that table is the exact-match regression floor and must keep passing verbatim.
//
// Every fixture below sums to 223075, so a row's wantWindow alone says which of
// three things happened: the exact key answered, the variant key answered, or
// nothing did and #2100 collapsed the default.
func TestRead_VariantJoin(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		fixture    string
		windows    map[string]int
		wantUsed   int
		wantWindow int
	}{
		{
			// AC 1, the defect this ticket exists for. over_window.jsonl names
			// claude-opus-5, exactly as a live 1M transcript does; the result
			// line keys its modelUsage map claude-opus-5[1m]. Before this rule
			// the lookup missed, the default was disproved by 223075, and the
			// gauge drew nothing.
			name:       "a variant-suffixed key answers for the unsuffixed id",
			fixture:    "over_window.jsonl",
			windows:    map[string]int{"claude-opus-5[1m]": 1_000_000},
			wantUsed:   223075,
			wantWindow: 1_000_000,
		},
		{
			// AC 2: an exact match still wins. The exact key's window is
			// deliberately ABOVE the used count, so this row separates THREE
			// outcomes rather than two — 400000 is exact-wins, 1000000 is
			// variant-wins, 0 is a miss. Pinning it at 200000 would have made
			// exact-wins and miss both read 0.
			name:    "an exact key wins over a variant key",
			fixture: "over_window.jsonl",
			windows: map[string]int{
				"claude-opus-5":     400_000,
				"claude-opus-5[1m]": 1_000_000,
			},
			wantUsed:   223075,
			wantWindow: 400_000,
		},
		{
			// AC 3: a date suffix is not a variant group. The evidence behind
			// the verbatim rule is unchanged — no rule relating a dated to an
			// undated spelling survives the observed captures — so this stays a
			// miss and the disproved default still collapses.
			name:       "a date suffix is still a miss",
			fixture:    "over_window.jsonl",
			windows:    map[string]int{"claude-opus-5-20260101": 1_000_000},
			wantUsed:   223075,
			wantWindow: 0,
		},
		{
			// AC 3: the base comparison is byte equality, not a fold. A key
			// whose group is well-formed still misses when its base differs by
			// case alone.
			name:       "a case change in the base is still a miss",
			fixture:    "over_window.jsonl",
			windows:    map[string]int{"Claude-Opus-5[1m]": 1_000_000},
			wantUsed:   223075,
			wantWindow: 0,
		},
		{
			// AC 3: no alias expansion. "opus" is the short form claude
			// publishes in its own model menu, and a well-formed group on it
			// buys it nothing here.
			name:       "an alias base is still a miss",
			fixture:    "over_window.jsonl",
			windows:    map[string]int{"opus[1m]": 1_000_000},
			wantUsed:   223075,
			wantWindow: 0,
		},
		{
			// AC 3: two distinct suffixed keys sharing one base REFUSE rather
			// than guess. Beyond the AC this is what makes the answer a function
			// of the data alone — Go randomises map iteration, so "report the
			// first match" would differ between two runs on identical input.
			name:    "two variant keys on one base refuse rather than guess",
			fixture: "over_window.jsonl",
			windows: map[string]int{
				"claude-opus-5[1m]": 1_000_000,
				"claude-opus-5[2m]": 500_000,
			},
			wantUsed:   223075,
			wantWindow: 0,
		},
		{
			// The group must be the key's FINAL element: its "]" must be the
			// last byte. A trailing suffix after the group is a different id.
			name:       "a group that is not final is a miss",
			fixture:    "over_window.jsonl",
			windows:    map[string]int{"claude-opus-5[1m]x": 1_000_000},
			wantUsed:   223075,
			wantWindow: 0,
		},
		{
			name:       "an empty group is a miss",
			fixture:    "over_window.jsonl",
			windows:    map[string]int{"claude-opus-5[]": 1_000_000},
			wantUsed:   223075,
			wantWindow: 0,
		},
		{
			// The interior draws from the same closed byte class as a model id,
			// so a space — or any shell metachar, separator or byte >= 0x80 —
			// is refused.
			name:       "an interior byte outside the closed class is a miss",
			fixture:    "over_window.jsonl",
			windows:    map[string]int{"claude-opus-5[1 m]": 1_000_000},
			wantUsed:   223075,
			wantWindow: 0,
		},
		{
			// A nested group needs no check of its own: brackets are not in the
			// interior's byte class, so nothing inside a group can open or close
			// another one.
			name:       "a nested group is a miss",
			fixture:    "over_window.jsonl",
			windows:    map[string]int{"claude-opus-5[a[b]]": 1_000_000},
			wantUsed:   223075,
			wantWindow: 0,
		},
		{
			// Read's existing rule reaches the new path unchanged: a
			// non-positive observed value is not a window and is treated as
			// absent, whether the key is exact or suffixed.
			name:       "a non-positive variant window is treated as absent",
			fixture:    "over_window.jsonl",
			windows:    map[string]int{"claude-opus-5[1m]": 0},
			wantUsed:   223075,
			wantWindow: 0,
		},
		{
			// The PAIR of the row above, and the one that pins WHERE the
			// non-positive filter runs: it must drop the entry before it is
			// counted, or an entry that is absent by contract would manufacture
			// an ambiguity and collapse a reading that is not ambiguous at all.
			name:    "an absent variant entry does not create an ambiguity",
			fixture: "over_window.jsonl",
			windows: map[string]int{
				"claude-opus-5[1m]": 0,
				"claude-opus-5[2m]": 1_000_000,
			},
			wantUsed:   223075,
			wantWindow: 1_000_000,
		},
		{
			// The empty-base rule from the transcript side: an entry with no
			// message.model performs no lookup at all, so no stripping rule can
			// pair it with a windows key whose base is empty.
			name:       "an entry with no model does not match an empty-base key",
			fixture:    "no_model.jsonl",
			windows:    map[string]int{"[1m]": 1_000_000},
			wantUsed:   223075,
			wantWindow: 0,
		},
		{
			// The same rule from the windows side: "[1m]" has no base to carry
			// it, so it is refused outright rather than matching whatever id the
			// transcript happened to name.
			name:       "a named entry does not match an empty-base key",
			fixture:    "over_window.jsonl",
			windows:    map[string]int{"[1m]": 1_000_000},
			wantUsed:   223075,
			wantWindow: 0,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := Read(filepath.Join("testdata", tt.fixture), tt.windows)
			if err != nil {
				t.Fatalf("Read(%s): %v", tt.fixture, err)
			}
			if got.UsedTokens != tt.wantUsed {
				t.Errorf("UsedTokens = %d, want %d", got.UsedTokens, tt.wantUsed)
			}
			if got.WindowTokens != tt.wantWindow {
				t.Errorf("WindowTokens = %d, want %d", got.WindowTokens, tt.wantWindow)
			}
		})
	}
}

// TestRead_MeasuredChannelDivergence pins BOTH halves of the join against the
// strings measured end to end on 2026-09-05 and recorded on #2118 — a live probe
// of the surface the daemon spawns, not a reading of a committed capture. No
// capture in this tree carries a bracketed modelUsage key.
//
// The two halves are what the defect was made of: claude reports the window
// under a variant-suffixed id while the transcript records the id without the
// suffix, and neither channel is wrong on its own. Asserting the join alone
// would not catch a future divergence, because a test that writes both halves
// from one constant passes whatever the strings drift to — which is exactly how
// the full-stack e2e stayed green through this bug. So both literals are pinned
// here, the transcript half against the committed fixture's own bytes.
func TestRead_MeasuredChannelDivergence(t *testing.T) {
	t.Parallel()

	const (
		// The result line's per-model map key, measured 2026-09-05.
		measuredResultKey = "claude-opus-5[1m]"
		// The transcript's spelling of the same model, measured on every
		// usage-bearing entry of the same live 1M session. The trailing comma is
		// load-bearing: it terminates the id, so a fixture widened to a dated or
		// suffixed spelling fails here instead of matching as a prefix.
		measuredTranscriptField = `"model":"claude-opus-5",`
	)

	fixture := filepath.Join("testdata", "over_window.jsonl")
	raw, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatalf("read fixture %s: %v", fixture, err)
	}
	if !strings.Contains(string(raw), measuredTranscriptField) {
		t.Fatalf("%s no longer carries %s — the transcript half of the join has drifted from what #2118 measured",
			fixture, measuredTranscriptField)
	}

	got, err := Read(fixture, map[string]int{measuredResultKey: 1_000_000})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	want := Usage{UsedTokens: 223075, WindowTokens: 1_000_000}
	if got != want {
		t.Errorf("Read = %+v, want %+v — the window claude reported under %q must size the used count the transcript "+
			"recorded under its unsuffixed spelling; WindowTokens 0 is the blank gauge #2118 was filed for",
			got, want, measuredResultKey)
	}
}

// TestRead_DoesNotRetainOrMutateWindows pins the ownership half of Read's
// contract: the map is read, never written, and never held past the call. A
// caller building one per call (cmd/pyry's sessionModelWindows) would otherwise
// have no guarantee that two concurrent reads over one session are independent.
func TestRead_DoesNotRetainOrMutateWindows(t *testing.T) {
	t.Parallel()

	windows := map[string]int{"claude-opus-5": 1_000_000}
	if _, err := Read(filepath.Join("testdata", "over_window.jsonl"), windows); err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(windows) != 1 || windows["claude-opus-5"] != 1_000_000 {
		t.Errorf("windows = %v after Read, want it unchanged", windows)
	}
}
