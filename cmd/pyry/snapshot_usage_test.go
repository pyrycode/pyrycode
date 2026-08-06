package main

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// defaultWindow mirrors internal/contextwindow's unexported defaultWindowTokens:
// the window size every current model exposes, and the value Read reports for
// every unresolvable case.
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

// TestSnapshotUsageFor_ReadsTheBoundTranscript covers the happy path: the
// bootstrap session's transcript is resolved BY ID — no process probing — and
// its latest usage numbers are reported against the default window.
func TestSnapshotUsageFor_ReadsTheBoundTranscript(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	const id = "11111111-2222-4333-8444-555555555555"
	writeUsageTranscript(t, dir, id, 5000, 800, 1500, 200)

	usage := snapshotUsageFor(dir, func() string { return id })
	if usage == nil {
		t.Fatal("snapshotUsageFor returned nil for a wired dir + id")
	}
	used, window := usage()
	if want := 5000 + 800 + 1500 + 200; used != want {
		t.Errorf("used = %d, want %d", used, want)
	}
	if window != defaultWindow {
		t.Errorf("window = %d, want %d", window, defaultWindow)
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
func TestSnapshotUsageFor_SiblingTranscriptIsNeverRead(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	const boundID = "11111111-2222-4333-8444-555555555555"
	const siblingID = "99999999-8888-4777-8666-555555555555"
	writeUsageTranscript(t, dir, boundID, 1000, 0, 0, 0)
	// The sibling is written second, so it is also the newest by modification
	// time — the tiebreak a directory scan would fall for.
	writeUsageTranscript(t, dir, siblingID, 150000, 0, 0, 0)

	usage := snapshotUsageFor(dir, func() string { return boundID })
	used, _ := usage()
	if used != 1000 {
		t.Errorf("used = %d, want 1000 (the BOUND session's figure, never the sibling's 150000)", used)
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

	cases := []struct {
		name string
		id   string
	}{
		{"no transcript written yet", "22222222-3333-4444-8555-666666666666"},
		{"empty id", ""},
		{"path traversal attempt", "../../../etc/passwd"},
		{"not an id at all", "not-a-uuid"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			usage := snapshotUsageFor(dir, func() string { return tc.id })
			used, window := usage()
			if used != 0 {
				t.Errorf("used = %d, want 0", used)
			}
			if window != defaultWindow {
				t.Errorf("window = %d, want %d", window, defaultWindow)
			}
		})
	}
}

// TestSnapshotUsageFor_UnwiredReturnsNilSeam pins the foreground / pre-wire case:
// with nothing to resolve from, the builder returns a nil seam rather than a
// closure that always reports zeros. The relay handlers treat a nil seam as
// "usage unavailable" and report zeros themselves, so this preserves the
// pre-existing unwired contract exactly.
func TestSnapshotUsageFor_UnwiredReturnsNilSeam(t *testing.T) {
	t.Parallel()

	if got := snapshotUsageFor("", func() string { return "id" }); got != nil {
		t.Error("no sessions dir: want a nil seam")
	}
	if got := snapshotUsageFor(t.TempDir(), nil); got != nil {
		t.Error("no id source: want a nil seam")
	}
}
