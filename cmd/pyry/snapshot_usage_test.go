package main

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// defaultWindow mirrors internal/contextwindow's unexported defaultWindowTokens:
// the window size that package BELIEVES a session has absent anything better,
// and the value Read reports for every unresolvable case. It is a guess, not a
// fact — see #2100 and TestSnapshotUsageFor_WindowAgainstTheUsedCount below for
// the used count that disproves it.
const defaultWindow = 200_000

// writeUsageTranscript writes a minimal claude transcript at <dir>/<id>.jsonl
// whose latest assistant entry carries the given usage numbers. The reported
// used-token figure is the sum of the four fields, matching contextwindow.Read.
func writeUsageTranscript(t *testing.T, dir, id string, input, cacheCreate, cacheRead, output int) {
	t.Helper()
	line := `{"type":"user","message":{"role":"user","content":"hi"}}` + "\n" +
		`{"type":"assistant","message":{"model":"claude-opus-4-8","role":"assistant","stop_reason":"end_turn",` +
		`"content":[{"type":"text","text":"ok"}],"usage":{` +
		`"input_tokens":` + strconv.Itoa(input) +
		`,"cache_creation_input_tokens":` + strconv.Itoa(cacheCreate) +
		`,"cache_read_input_tokens":` + strconv.Itoa(cacheRead) +
		`,"output_tokens":` + strconv.Itoa(output) + `}}}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, id+".jsonl"), []byte(line), 0o600); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
}

// oneFolder answers snapshotUsageFor's per-session folder resolver with ONE
// folder for every id, and is nil for "" — the no-resolver shape the builder
// collapses to a nil reader. Production resolves the folder per session through
// sessionTranscriptDir; these tables need only one.
func oneFolder(dir string) func(sessionID string) string {
	if dir == "" {
		return nil
	}
	return func(string) string { return dir }
}

// TestSnapshotUsageFor_ReadsTheBoundTranscript covers the happy path: the
// session's transcript is resolved BY ID — no process probing — and its latest
// usage numbers are reported against the default window.
func TestSnapshotUsageFor_ReadsTheBoundTranscript(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	const id = "11111111-2222-4333-8444-555555555555"
	writeUsageTranscript(t, dir, id, 5000, 800, 1500, 200)

	read := snapshotUsageFor(oneFolder(dir), nil)
	if read == nil {
		t.Fatal("snapshotUsageFor returned nil for a wired dir")
	}
	used, window := read(id)
	if want := 5000 + 800 + 1500 + 200; used != want {
		t.Errorf("used = %d, want %d", used, want)
	}
	if window != defaultWindow {
		t.Errorf("window = %d, want %d", window, defaultWindow)
	}
}

// TestSnapshotUsageFor_WindowAgainstTheUsedCount walks the boundary #2100 draws:
// a used count ABOVE the believed window disproves it, so the window collapses to
// 0 ("no trustworthy reading") while the used count still carries the true sum;
// at or below it, both figures are what they were before.
//
// It runs through snapshotUsageFor rather than calling contextwindow.Read
// directly because that is the reader RunConfigFor's usage half bottoms out in,
// so proving the collapse here proves it for the session_settings payload. And
// it reads a transcript
// written to disk rather than injecting figures, so nothing hands the seam the
// answer; a relay-level test that injected (223075, 0) would assert only the
// pass-through, which already existed before this ticket.
//
// The three rows are what make the guard's strictness non-arbitrary: relax it to
// >= and the equality row reddens, drop the collapse entirely and the
// over-window row reddens.
func TestSnapshotUsageFor_WindowAgainstTheUsedCount(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name                                  string
		id                                    string
		input, cacheCreate, cacheRead, output int
		wantUsed                              int
		wantWindow                            int
	}{
		{
			// The figures observed live on 2026-09-04 against an Opus 5 session
			// on the 1M window: 2+950+221118+1005 = 223075. Against the believed
			// 200000 that is 111%, which the client clamps to a confident 100% —
			// the clamp is correct, and is what hid the overflow.
			name:  "above the window reports no window, used intact",
			id:    "aaaaaaaa-1111-4222-8333-444444444444",
			input: 2, cacheCreate: 950, cacheRead: 221118, output: 1005,
			wantUsed: 223075, wantWindow: 0,
		},
		{
			// Equality is not a contradiction: full is not the same as wrong.
			name:  "exactly at the window keeps it",
			id:    "bbbbbbbb-1111-4222-8333-444444444444",
			input: 150000, cacheCreate: 20000, cacheRead: 25000, output: 5000,
			wantUsed: defaultWindow, wantWindow: defaultWindow,
		},
		{
			name:  "one token under the window keeps it",
			id:    "cccccccc-1111-4222-8333-444444444444",
			input: 149999, cacheCreate: 20000, cacheRead: 25000, output: 5000,
			wantUsed: defaultWindow - 1, wantWindow: defaultWindow,
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			writeUsageTranscript(t, dir, tc.id, tc.input, tc.cacheCreate, tc.cacheRead, tc.output)

			read := snapshotUsageFor(oneFolder(dir), nil)
			if read == nil {
				t.Fatal("snapshotUsageFor returned nil for a wired dir")
			}
			used, window := read(tc.id)
			if used != tc.wantUsed {
				t.Errorf("used = %d, want %d", used, tc.wantUsed)
			}
			if window != tc.wantWindow {
				t.Errorf("window = %d, want %d", window, tc.wantWindow)
			}
		})
	}
}

// TestSnapshotUsageFor_SiblingTranscriptIsNeverRead is the isolation property.
//
// The old resolver asked the terminal child which transcript it held open, which
// is what kept it off a second claude's file when both shared a sessions
// directory. By-id resolution has to buy that same isolation structurally, and it
// does: the path IS the id, so a sibling with wildly different numbers sitting in
// the same directory cannot be picked up by accident. If this fails, the reader
// is scanning the directory again rather than resolving by id, and a client could
// be shown another session's context figure.
//
// ONE reader answers for BOTH ids, which is what makes the isolation testable in
// both directions now that the id is a call argument. Each assertion is the sole
// red for a distinct mutant: a reader that ignores its argument and uses some
// captured id returns 1000 twice (the sibling assertion catches it); a reader
// that scans the directory for the newest file returns 150000 twice (the bound
// assertion catches it). Any cross-read that reaches a result changes a number.
func TestSnapshotUsageFor_SiblingTranscriptIsNeverRead(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	const boundID = "11111111-2222-4333-8444-555555555555"
	const siblingID = "99999999-8888-4777-8666-555555555555"
	writeUsageTranscript(t, dir, boundID, 1000, 0, 0, 0)
	// The sibling is written second, so it is also the newest by modification
	// time — the tiebreak a directory scan would fall for.
	writeUsageTranscript(t, dir, siblingID, 150000, 0, 0, 0)

	read := snapshotUsageFor(oneFolder(dir), nil)
	if used, _ := read(boundID); used != 1000 {
		t.Errorf("read(boundID) used = %d, want 1000 (the bound session's figure, never the sibling's 150000)", used)
	}
	if used, _ := read(siblingID); used != 150000 {
		t.Errorf("read(siblingID) used = %d, want 150000 (the sibling's own figure — the same reader must answer per id)", used)
	}
}

// TestSnapshotUsageFor_UnresolvableReportsFreshSession covers every degrade path.
// All of them collapse to the same deterministic fresh-session report rather than
// to an error, because a usage reader has no business surfacing a failure to a
// client: zero used against the default window is exactly what a brand-new
// session looks like, which is the truthful answer in each of these cases.
func TestSnapshotUsageFor_UnresolvableReportsFreshSession(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeUsageTranscript(t, dir, "11111111-2222-4333-8444-555555555555", 5000, 0, 0, 0)
	read := snapshotUsageFor(oneFolder(dir), nil)

	// unreadableID's transcript path exists and stats clean but cannot be read,
	// because prepare creates a DIRECTORY there. That is the deterministic
	// stand-in for the unlink race between the stat and the open, which a unit
	// test cannot drive: read(2) on a directory fd is EISDIR on both Linux and
	// macOS, and it needs no chmod, so the row cannot silently pass as root.
	const unreadableID = "33333333-4444-4555-8666-777777777777"

	cases := []struct {
		name string
		id   string
		// prepare seeds the row's failure into dir; nil when the id fails inside
		// StatByID, which is every row that never reaches the filesystem.
		prepare func(t *testing.T, dir string)
	}{
		{name: "no transcript written yet", id: "22222222-3333-4444-8555-666666666666"},
		{name: "empty id", id: ""},
		{name: "path traversal attempt", id: "../../../etc/passwd"},
		{name: "not an id at all", id: "not-a-uuid"},
		{
			name: "resolved path stats clean but cannot be read",
			id:   unreadableID,
			prepare: func(t *testing.T, dir string) {
				t.Helper()
				if err := os.Mkdir(filepath.Join(dir, unreadableID+".jsonl"), 0o700); err != nil {
					t.Fatalf("seed unreadable transcript: %v", err)
				}
			},
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if tc.prepare != nil {
				tc.prepare(t, dir)
			}
			used, window := read(tc.id)
			if used != 0 {
				t.Errorf("used = %d, want 0", used)
			}
			// contextwindow.Read reports a ZERO Usage alongside its error, window
			// included, so this is the sole red for the recovery branch: drop it and
			// the last row reports a 0 window rather than a fresh session's.
			if window != defaultWindow {
				t.Errorf("window = %d, want %d", window, defaultWindow)
			}
		})
	}
}

// TestSnapshotUsageFor_UnwiredReturnsNilSeam pins the foreground / pre-wire case:
// with no sessions directory to resolve against, the builder returns a nil
// reader rather than one that would filepath.Join against the daemon's working
// directory. The relay handlers treat the nil seam this collapses into as "usage
// unavailable" and report zeros themselves, so the pre-existing unwired contract
// is preserved exactly.
func TestSnapshotUsageFor_UnwiredReturnsNilSeam(t *testing.T) {
	t.Parallel()

	if got := snapshotUsageFor(oneFolder(""), nil); got != nil {
		t.Error("no sessions dir: want a nil reader")
	}
	// Since #2423 the builder's own rule is stated on the RESOLVER, and
	// oneFolder("") is only one way to reach it. The production nil comes from
	// sessionTranscriptDir's gate instead, so the rule is pinned in its own terms
	// as well as through the helper.
	if got := snapshotUsageFor(nil, nil); got != nil {
		t.Error("no folder resolver: want a nil reader")
	}
}

// TestSnapshotUsageFor_EmptyFolderAnswerReadsNothing is the per-call half of the
// guard #2423 moved out of the builder: a resolver that ANSWERS "" for an id — an
// id the pool does not hold, a runner that cannot name a folder — must collapse to
// the fresh-session report without touching the filesystem.
//
// The discriminating part is where the stray file sits. A reader that skipped the
// empty check would hand "" to StatByID, which joins a RELATIVE <id>.jsonl against
// the PROCESS directory, so a file of that name there would be read and reported as
// this session's occupancy. t.Chdir makes that directory a temp dir holding exactly
// such a file: the correct reader reports zero used against the default window, and
// the mutant reports 9999.
func TestSnapshotUsageFor_EmptyFolderAnswerReadsNothing(t *testing.T) {
	const id = "11111111-2222-4333-8444-555555555555"

	cwd := t.TempDir()
	writeUsageTranscript(t, cwd, id, 9999, 0, 0, 0)
	t.Chdir(cwd)

	read := snapshotUsageFor(func(string) string { return "" }, nil)
	if read == nil {
		t.Fatal("snapshotUsageFor returned nil for a wired resolver — the nil rule is the resolver's presence, not its answers")
	}
	used, window := read(id)
	if used != 0 {
		t.Errorf("used = %d, want 0 — an empty folder answer must not join a relative <id>.jsonl against the process directory", used)
	}
	if window != defaultWindow {
		t.Errorf("window = %d, want %d (the fresh-session report)", window, defaultWindow)
	}
}

// TestSnapshotUsageFor_TwoFoldersTwoSessions is #2423's defect at the reader's
// seam, and the unit half of its first acceptance criterion: ONE reader answers for
// two sessions whose transcripts live in two DIFFERENT folders, each reporting its
// own used count.
//
// Both transcripts carry distinguishable sums and NEITHER folder is the reader's
// former single directory, so the pre-#2423 shape — one fixed folder for the whole
// daemon — cannot pass: it would report one session's count and zero for the other,
// whichever folder it happened to hold. The ids differ too, so a reader that took
// the folder from the right session but the id from the wrong one misses both.
func TestSnapshotUsageFor_TwoFoldersTwoSessions(t *testing.T) {
	t.Parallel()

	const (
		idA = "aaaaaaaa-2222-4333-8444-555555555555"
		idB = "bbbbbbbb-2222-4333-8444-555555555555"
	)
	dirA, dirB := t.TempDir(), t.TempDir()
	writeUsageTranscript(t, dirA, idA, 1000, 0, 0, 11)
	writeUsageTranscript(t, dirB, idB, 2000, 0, 0, 22)

	read := snapshotUsageFor(func(sessionID string) string {
		switch sessionID {
		case idA:
			return dirA
		case idB:
			return dirB
		}
		return ""
	}, nil)
	if read == nil {
		t.Fatal("snapshotUsageFor returned nil for a wired resolver")
	}
	if used, _ := read(idA); used != 1011 {
		t.Errorf("session A used = %d, want 1011 — its own transcript, in its own folder", used)
	}
	if used, _ := read(idB); used != 2022 {
		t.Errorf("session B used = %d, want 2022 — its own transcript, in its own folder", used)
	}
}

// transcriptModel is the model id writeUsageTranscript stamps on the assistant
// entry it writes. Named here because #2107's join is keyed on exactly that
// string: a test whose windows map used any other spelling would assert a MISS
// while looking like it asserted a hit.
const transcriptModel = "claude-opus-4-8"

// TestSnapshotUsageFor_ReportsTheObservedWindow is #2107 at the seam: the reader
// asks the windows resolver about the SAME id it resolved the transcript by, and
// contextwindow.Read pairs the two.
//
// The used count is the figure measured live on 2026-09-04, and the observed
// window is above it, so this row also proves the contradiction check runs AFTER
// the join — a check-then-resolve seam would report 0 here.
func TestSnapshotUsageFor_ReportsTheObservedWindow(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	const id = "11111111-2222-4333-8444-666666666666"
	writeUsageTranscript(t, dir, id, 2, 950, 221118, 1005)

	read := snapshotUsageFor(oneFolder(dir), func(sessionID string) map[string]int {
		if sessionID != id {
			t.Errorf("windows asked about %q, want the id the transcript was resolved by (%q)", sessionID, id)
		}
		return map[string]int{transcriptModel: 1_000_000}
	})
	if read == nil {
		t.Fatal("snapshotUsageFor returned nil for a wired dir")
	}
	used, window := read(id)
	if used != 223075 {
		t.Errorf("used = %d, want 223075", used)
	}
	if window != 1_000_000 {
		t.Errorf("window = %d, want 1000000 — the observed window for the model that produced the used count", window)
	}
}

// TestSnapshotUsageFor_NilWindowsStillReads pins the rule that is easiest to
// pattern-match wrong: dir == "" collapses the seam to nil, a nil windows func
// does NOT. A daemon that cannot resolve windows (foreground / v1) still has
// transcripts, and its reading must stay the pre-#2107 one rather than becoming
// no reading at all.
//
// The used count is deliberately below the default window so the assertion is
// "the default was reported", not #2100's collapse — which would pass for a
// broken seam too.
func TestSnapshotUsageFor_NilWindowsStillReads(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	const id = "11111111-2222-4333-8444-777777777777"
	writeUsageTranscript(t, dir, id, 1000, 0, 0, 0)

	read := snapshotUsageFor(oneFolder(dir), nil)
	if read == nil {
		t.Fatal("snapshotUsageFor(oneFolder(dir), nil) returned a nil seam — a nil windows resolver must degrade the window, not the reader")
	}
	used, window := read(id)
	if used != 1000 {
		t.Errorf("used = %d, want 1000", used)
	}
	if window != defaultWindow {
		t.Errorf("window = %d, want the default %d", window, defaultWindow)
	}
}

// TestSnapshotUsageFor_WindowIsolation is AC 5 at the seam: a window observed for
// one session is never reported for another. Two transcripts of the SAME size sit
// side by side and only one id has an observed window, so a reader that asked the
// resolver with a fixed or stale id would report 1M for both.
func TestSnapshotUsageFor_WindowIsolation(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	const (
		observedID = "11111111-2222-4333-8444-888888888888"
		siblingID  = "99999999-2222-4333-8444-888888888888"
	)
	writeUsageTranscript(t, dir, observedID, 1000, 0, 0, 0)
	writeUsageTranscript(t, dir, siblingID, 1000, 0, 0, 0)

	read := snapshotUsageFor(oneFolder(dir), func(sessionID string) map[string]int {
		if sessionID != observedID {
			return nil
		}
		return map[string]int{transcriptModel: 1_000_000}
	})

	if _, window := read(observedID); window != 1_000_000 {
		t.Errorf("observed session window = %d, want 1000000", window)
	}
	if _, window := read(siblingID); window != defaultWindow {
		t.Errorf("sibling session window = %d, want the default %d — one session's observed window must never be reported for another",
			window, defaultWindow)
	}
}
