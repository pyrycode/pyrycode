//go:build e2e_realclaude

package realclaude

// Shared harness for the real-claude interactive suite: reading the on-disk
// session row and waiting for its id to settle.
//
// Transcribed from interactive_session_control_liveness_test.go (#1031), which
// #1348 deleted on 2026-08-16 along with the terminal-path test it served. The
// test is gone for good; these helpers stay because surviving stream-path tests
// call them. See harness_daemon_test.go for the full account of why this file
// exists.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"
)

// This file's fixed seeded ids. Distinct from every other realclaude gate's fixed
// ids (liveBootstrapUUID 7s, livePerConvBootstrapUUID 8s, liveModalBootstrapUUID
// 9s, liveConvID 5s) so same-package files never collide on an identifier. Both
// are valid UUIDv4 stems, matching the seeding convention.
const (
	sessionCtrlBootstrapUUID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	sessionCtrlConvID        = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
)

// Rotation / settle budgets for the new_session respawn. Real claude on /clear
// (clear + mint a fresh transcript + announce the reset + the daemon's re-key) is
// seconds, not fakeclaude milliseconds; and it rotates on every /clear, so the
// re-send cadence
// is deliberately slower than #1004's 250 ms to avoid stacking rotations while
// still recovering from a frame that lands before the tui-driver session
// re-attaches.
const (
	newSessionResend = 1 * time.Second
	rotateBudget     = 45 * time.Second
	idSettleQuiesce  = 2 * time.Second
	idSettleTimeout  = 45 * time.Second
)

// --- on-disk sessions.json reader (the only genuinely new code) --------------

// uuidStemPattern matches the canonical 36-char lowercase UUIDv4 stem claude uses
// for its <uuid>.jsonl filenames. Byte-identical to transcript.ValidStem's pattern,
// which is the production source of truth; it is transcribed rather than imported
// because this file's e2e_realclaude build tag has to stand alone.
var uuidStemPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// ptr returns a pointer to v. SetSessionSettingsPayload uses pointer fields as a
// presence contract (nil = leave unchanged), so a literal needs &value.
func ptr[T any](v T) *T { return &v }

// bootstrapRow is a settings-aware decode of one sessions.json row. Mirrors
// internal/e2e/relay_v2_settings_test.go's settingsRow and the on-disk shape in
// internal/sessions/registry.go.
type bootstrapRow struct {
	ID        string `json:"id"`
	Bootstrap bool   `json:"bootstrap"`
	Model     string `json:"model"`
	Effort    string `json:"effort"`
	YOLO      bool   `json:"yolo"`
}

// readBootstrapRowIfPresent reads <home>/.pyry/test/sessions.json (the
// -pyry-name=test daemon) and returns the Bootstrap==true row — matched by flag,
// so robust to a restart rotating the bootstrap id. The bool is false on any of:
// file missing, parse error, no bootstrap row.
func readBootstrapRowIfPresent(home string) (bootstrapRow, bool) {
	path := filepath.Join(home, ".pyry", "test", "sessions.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return bootstrapRow{}, false
	}
	var reg struct {
		Sessions []bootstrapRow `json:"sessions"`
	}
	if err := json.Unmarshal(data, &reg); err != nil {
		return bootstrapRow{}, false
	}
	for _, row := range reg.Sessions {
		if row.Bootstrap {
			return row, true
		}
	}
	return bootstrapRow{}, false
}

// waitBootstrapID polls the bootstrap row id until it is non-empty, then returns
// it. Bounded by timeout.
func waitBootstrapID(t *testing.T, home string, timeout time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if row, ok := readBootstrapRowIfPresent(home); ok && row.ID != "" {
			return row.ID
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("bootstrap row id never became non-empty within %s", timeout)
	return ""
}

// waitBootstrapIDSettled polls the bootstrap row id until it has been non-empty
// and unchanged for a full quiesce window, then returns it — i.e. no rotation is
// still in flight. A control verb (new_session /clear, settings restart) makes
// real claude mint a fresh transcript, and it can do so more than once before the
// id settles; waiting for stability before the next live turn keeps a straggler
// rotation from tearing that turn down mid-stream. Bounded by timeout.
func waitBootstrapIDSettled(t *testing.T, home string, quiesce, timeout time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var lastID string
	var since time.Time
	for time.Now().Before(deadline) {
		row, ok := readBootstrapRowIfPresent(home)
		switch {
		case !ok || row.ID == "":
			lastID, since = "", time.Time{}
		case row.ID != lastID:
			lastID, since = row.ID, time.Now()
		default:
			if time.Since(since) >= quiesce {
				return lastID
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("bootstrap id never settled (last=%q) within %s", lastID, timeout)
	return ""
}
