package contextwindow

import (
	"errors"
	"io/fs"
	"path/filepath"
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
