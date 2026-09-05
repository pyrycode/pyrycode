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
			got, err := Read(filepath.Join("testdata", tt.fixture))
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
	got, err := Read("")
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
	got, err := Read(filepath.Join("testdata", "does-not-exist.jsonl"))
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
