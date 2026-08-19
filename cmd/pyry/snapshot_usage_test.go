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
// session's transcript is resolved BY ID — no process probing — and its latest
// usage numbers are reported against the default window.
func TestSnapshotUsageFor_ReadsTheBoundTranscript(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	const id = "11111111-2222-4333-8444-555555555555"
	writeUsageTranscript(t, dir, id, 5000, 800, 1500, 200)

	read := snapshotUsageFor(dir)
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

	read := snapshotUsageFor(dir)
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
	read := snapshotUsageFor(dir)

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
// is preserved exactly. The other half of that contract — no id source ⇒ nil
// seam — now lives at the wiring point, in TestBootstrapSnapshotUsage.
func TestSnapshotUsageFor_UnwiredReturnsNilSeam(t *testing.T) {
	t.Parallel()

	if got := snapshotUsageFor(""); got != nil {
		t.Error("no sessions dir: want a nil reader")
	}
}

// TestBootstrapSnapshotUsage covers the wiring-point seam builder startRelayV2
// feeds to V2SessionConfig.SnapshotUsage. startRelayV2 itself has no test and
// cannot cheaply get one, which is why the seam is built by a named function
// rather than an inline expression: both halves of the nil contract and the
// choice of WHICH id reaches the reader are decided here, so they are pinned
// here.
func TestBootstrapSnapshotUsage(t *testing.T) {
	t.Parallel()

	t.Run("no sessions dir is a nil seam", func(t *testing.T) {
		t.Parallel()
		if got := bootstrapSnapshotUsage("", func() string { return "id" }); got != nil {
			t.Error("bootstrapSnapshotUsage(no dir) is non-nil, want a nil seam")
		}
	})

	// bootstrapIDFn is a func field whose zero value is nil, and relayWiring
	// documents it as legitimately nil. This pins that optional-field contract; it
	// is not averting a live panic, because relayWiring's single producer always
	// sets the field and no test constructs one. Deciding it at BUILD time is what
	// makes it structural: no closure exists that could invoke a nil id source.
	t.Run("no id source is a nil seam", func(t *testing.T) {
		t.Parallel()
		if got := bootstrapSnapshotUsage(t.TempDir(), nil); got != nil {
			t.Error("bootstrapSnapshotUsage(no id source) is non-nil, want a nil seam")
		}
	})

	// The discriminating half: wired normally, the seam reports the BOOTSTRAP
	// session's occupancy. The e2e cannot prove this — stream-mode fakeclaude
	// binds no sessions dir and writes no transcript, so the used_tokens assertion
	// in internal/e2e's TestRelayV2_StreamRequestSessionSettings is zero against
	// zero and every id yields 0 there. A sibling with a different figure sits in
	// the same directory, so 0 means the id never reached the reader and 150000
	// means the wrong id did.
	t.Run("wired reports the bootstrap session's occupancy", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		const bootstrapID = "11111111-2222-4333-8444-555555555555"
		const siblingID = "99999999-8888-4777-8666-555555555555"
		writeUsageTranscript(t, dir, bootstrapID, 4200, 0, 0, 0)
		writeUsageTranscript(t, dir, siblingID, 150000, 0, 0, 0)

		usage := bootstrapSnapshotUsage(dir, func() string { return bootstrapID })
		if usage == nil {
			t.Fatal("bootstrapSnapshotUsage(wired) returned a nil seam")
		}
		used, window := usage()
		if used != 4200 {
			t.Errorf("used = %d, want 4200 (not 0 — the id never reached the reader; not 150000 — the wrong id did)", used)
		}
		if window != defaultWindow {
			t.Errorf("window = %d, want %d", window, defaultWindow)
		}
	})
}
