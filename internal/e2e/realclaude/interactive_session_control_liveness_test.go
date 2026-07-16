//go:build e2e_realclaude

package realclaude

// TestInteractiveSessionControlLiveness is the #1031 deliverable: a real-claude
// liveness gate for the two session-control verbs that respawn the live claude
// child — new_session (rotate, via /clear) and set_session_settings (respawn, via
// a live restart) — proven against a freshly-spawned daemon running real
// `claude --model haiku` over the Noise v2 wire. Before this, neither verb had
// ever executed against real claude: the fake tier owns their deterministic shape
// (relay_v2_new_session_test.go #1004, relay_v2_settings_test.go #1005), but
// "fake-mock e2e is necessary but not sufficient" — this gate proves the real
// interactive stack survives the respawn, per the 2026-07-08
// real-claude-e2e-in-a-pre-ship-gate policy.
//
// The operator-facing risk both verbs share is that the reply bridge fails to
// REBIND past the respawn — the same class of failure #854 guards on a cold
// bootstrap session. So each verb pairs its liveness send with a deterministic
// on-disk observable proving the respawn genuinely happened (a pure-liveness
// assertion is vacuous: a silently no-op'd rotate/respawn would answer from the
// un-rotated session and pass):
//
//   - new_session: the sessions.json bootstrap-row id rotates to a NEW value. A
//     baseline id is read AFTER Turn 1 settles and BEFORE the frame, so the "id
//     changed" assertion is non-vacuous (mirrors #1004). Structural causality:
//     real claude only rotates its JSONL on /clear (normal turns append), so an id
//     change in this window ⟺ the driven /clear.
//   - set_session_settings: the bootstrap-row Model reflects the new value. The
//     seeded row carries no model, so "" → "haiku" is a genuine change that
//     persists and live-restarts the child (a no-op update writes nothing and does
//     not restart), making the following liveness send non-vacuous (mirrors #1005).
//
// One daemon, one seeded bound conversation, one encrypted channel — a sequential
// spine (the #1028 shape): turn 1 → new_session-rotate → turn 2 → settings-respawn
// → turn 3, requests and replies strictly alternating so the Noise receive nonce
// stays in lockstep (the drain helpers decrypt every noise_msg in arrival order).
// Turn 1 is load-bearing: it binds the reply bridge and attaches the tui-driver
// session, so the later liveness sends are REBINDS past a respawn, not first-binds,
// and claude has an established transcript so /clear produces a genuine rotation.
//
// Why Model: "haiku" (credential-safe). claudeSettingsArgs appends --model haiku
// after the daemon's base --model haiku (spawnBootstrapDaemon), last-wins → the
// effective model stays haiku, the one model this harness has proven live. This
// avoids the credential/rate-limit risk of spawning a different real model
// (opus/sonnet) in a pre-ship gate; the fake tier (#1005) already proves the
// settings mechanism with opus. "" → "haiku" is still a genuine change on disk and
// still triggers a real respawn.
//
// Real-vs-fake rotation difference (why the id is settled before each live turn).
// Unlike fakeclaude's one-shot PYRY_FAKE_CLAUDE_CLEAR_ROTATES, real claude rotates
// its transcript on EVERY /clear and on every settings-driven restart, so a
// control verb can leave a rotation in flight. Waiting for the bootstrap id to
// stop changing before the next send_message prevents a straggler rotation from
// tearing down that turn's in-flight claude mid-stream (which would flake AC #4).
//
// Like #854/#997/#1028 this is a standing liveness gate in preship, not a
// deterministic RED/GREEN oracle — the fake tier owns the shape checks. Placement
// under the e2e_realclaude build tag wires it into `make e2e-realclaude` (and thus
// `make preship`) with no Makefile change; the reused setup skips cleanly when
// claude / creds are absent (AC #3).

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakephone"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakerelay"
	"github.com/pyrycode/pyrycode/internal/protocol"
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
// (clear + mint a fresh transcript + fsnotify + watcher rotate) is seconds, not
// fakeclaude milliseconds; and it rotates on every /clear, so the re-send cadence
// is deliberately slower than #1004's 250 ms to avoid stacking rotations while
// still recovering from a frame that lands before the tui-driver session
// re-attaches.
const (
	newSessionResend = 1 * time.Second
	rotateBudget     = 45 * time.Second
	idSettleQuiesce  = 2 * time.Second
	idSettleTimeout  = 45 * time.Second
)

func TestInteractiveSessionControlLiveness(t *testing.T) {
	// No t.Parallel: WithWorktreeAuthenticated calls t.Setenv.
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skipf("realclaude: claude not on PATH: %v", err)
	}
	home := WithWorktreeAuthenticated(t) // skips cleanly when no creds
	claudeBin, err := exec.LookPath("claude")
	if err != nil {
		t.Fatalf("realclaude: resolve claude: %v", err)
	}

	// Isolated workdir under the authenticated HOME: guarantees the fresh-daemon
	// state (empty claude sessions dir) and sidesteps the shared-session-folder
	// collision (#828) by construction.
	workdir := filepath.Join(home, "work")
	if err := os.MkdirAll(workdir, 0o700); err != nil {
		t.Fatalf("realclaude: mkdir workdir: %v", err)
	}

	// Pair a device BEFORE the daemon starts (mints the bearer token + responder
	// static pubkey; writes server-id + devices registry the daemon loads).
	exit, stdout, stderr := runPyry(t, "pair", "-pyry-name=test", "--name=phone-a")
	if exit != 0 {
		t.Fatalf("pyry pair exit=%d\nstdout:\n%s\nstderr:\n%s", exit, stdout, stderr)
	}
	payload := decodePairPayload(t, stdout)
	pubKey, err := base64.StdEncoding.DecodeString(payload.ServerStaticPubkey)
	if err != nil {
		t.Fatalf("decode server static pubkey: %v", err)
	}

	// Seed the deterministic bootstrap id + the driving conversation binding BEFORE
	// the daemon starts (the registry is loaded once at startup, no reload).
	seedBootstrapRegistry(t, home, sessionCtrlBootstrapUUID)
	seedBoundConversation(t, home, sessionCtrlConvID, sessionCtrlBootstrapUUID, workdir)

	fr := fakerelay.New(relayTestLogger())
	t.Cleanup(func() { _ = fr.Close() })

	d := spawnBootstrapDaemon(t, home, workdir, claudeBin, fr.URL()+"/v2/server")
	t.Cleanup(func() { d.stop(t) })

	serverID := readPersistedServerID(t, home)
	waitBinaryHello(t, fr, serverID)

	dialCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	phone, err := fakephone.Dial(dialCtx, fr.URL(), serverID, payload.Token, "phone-a")
	if err != nil {
		t.Fatalf("phone dial: %v", err)
	}
	t.Cleanup(func() { _ = phone.Close() })

	initSend, initRecv := driveHandshakeInteractive(t, phone, pubKey, payload.Token)

	// A per-run nonce so reruns differ — enough to defeat any accidental caching,
	// without asserting on content.
	nonce := time.Now().UnixNano()

	// Envelope ids: only the set_session_settings frame's id is load-bearing
	// (drainForReply correlates on InReplyTo). The send_message ids are matched by
	// conversation_id + non-empty delta, and new_session is fire-and-forget; all
	// three are cosmetic, so a single monotonic counter keeps them fresh.
	var reqID uint64 = 2

	// --- Turn 1: pre-control liveness -----------------------------------------
	// Binds the reply bridge and attaches the tui-driver session, and gives claude
	// an established transcript so /clear later produces a genuine rotation. Without
	// this a post-control send would be a first-bind, not a rebind.
	sealSendMessage(t, phone, initSend, reqID, sessionCtrlConvID, "m-1",
		fmt.Sprintf("Reply with a single short word. run=%d turn=1", nonce))
	reqID++
	drainForAssistantReply(t, phone, initRecv, sessionCtrlConvID, 1, perTurnReplyBudget)

	// --- Baseline id (AC #1a non-vacuity anchor) -------------------------------
	// Read after Turn 1 settles. The id is stable here (Turn 1 appends to the same
	// transcript; only /clear rotates), so this baseline is the value the rotation
	// below must move away from. Whether it is the seeded value or a claude-minted
	// stem does not matter — the assertion is "changed", not "== seeded".
	idBefore := waitBootstrapID(t, home, idSettleTimeout)

	// --- new_session rotate (AC #1a) ------------------------------------------
	// Fire-and-forget re-send until the registry bootstrap id rotates away from
	// idBefore. new_session drops silently on a not-yet-attached session, hence the
	// re-send; after Turn 1 the session is attached, so the first /clear normally
	// rotates. Structural causality: only /clear rotates the JSONL, so an id change
	// here ⟺ the driven /clear.
	rotated := false
	rotateDeadline := time.Now().Add(rotateBudget)
	nextSend := time.Time{}
	for !rotated && time.Now().Before(rotateDeadline) {
		if time.Now().After(nextSend) {
			sealEnvelope(t, phone, initSend, protocol.Envelope{
				ID:   reqID,
				Type: protocol.TypeNewSession,
				TS:   time.Now().UTC(),
			})
			reqID++
			nextSend = time.Now().Add(newSessionResend)
		}
		if row, ok := readBootstrapRowIfPresent(home); ok && row.ID != "" && row.ID != idBefore {
			rotated = true
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if !rotated {
		t.Fatalf("new_session: bootstrap id never rotated away from %q within %s — the /clear keystroke "+
			"did not drive a real-claude rotation (the frame was dropped on a detached session, or /clear "+
			"did not mint a fresh transcript)", idBefore, rotateBudget)
	}
	// Settle before Turn 2: a re-send may have queued extra /clear frames (real
	// claude rotates on every one), so wait until no rotation is still in flight.
	// The settled id is the rotation anchor.
	idAfter := waitBootstrapIDSettled(t, home, idSettleQuiesce, idSettleTimeout)
	if idAfter == idBefore {
		t.Fatalf("new_session: settled id %q equals the baseline — no rotation occurred", idAfter)
	}
	if !uuidStemPattern.MatchString(idAfter) {
		t.Errorf("new_session: rotated id %q does not match the UUIDv4 stem pattern", idAfter)
	}

	// --- Turn 2: post-rotation liveness (AC #1b) ------------------------------
	// Proves the reply bridge rebound past the rotation — buildable because the
	// /clear's onRotate re-pointed sessionCtrlConvID's binding to the rotated id.
	sealSendMessage(t, phone, initSend, reqID, sessionCtrlConvID, "m-2",
		fmt.Sprintf("Reply with a single short word. run=%d turn=2", nonce))
	reqID++
	drainForAssistantReply(t, phone, initRecv, sessionCtrlConvID, 2, perTurnReplyBudget)

	// --- set_session_settings respawn (AC #2a) --------------------------------
	// Target the CURRENT bootstrap id (Turn 2 appends, does not rotate, so this is
	// idAfter — but re-read to stay robust). "" → "haiku" is a genuine change that
	// persists and live-restarts the child; the reply is not gated on the restart.
	sid := readBootstrapRow(t, home).ID
	settingsReqID := reqID
	reqID++
	sealEnvelope(t, phone, initSend, protocol.Envelope{
		ID:      settingsReqID,
		Type:    protocol.TypeSetSessionSettings,
		TS:      time.Now().UTC(),
		Payload: mustJSON(t, protocol.SetSessionSettingsPayload{SessionID: sid, Model: ptr("haiku")}),
	})
	reply := drainForReply(t, phone, initRecv, protocol.TypeSessionSettingsUpdated, settingsReqID, perTurnReplyBudget)
	var updated protocol.SessionSettingsUpdatedPayload
	if err := json.Unmarshal(reply.Payload, &updated); err != nil {
		t.Fatalf("decode session_settings_updated payload: %v", err)
	}
	if updated.SessionID == "" {
		t.Errorf("session_settings_updated reply has empty session_id")
	}

	// Persisted-change assertion: the bootstrap row (matched by flag, robust to the
	// restart rotating the id) reflects the new model.
	waitBootstrapModel(t, home, "haiku", idSettleTimeout)
	// Settle the settings-restart rotation before Turn 3 (same straggler-rotation
	// hazard as after new_session).
	waitBootstrapIDSettled(t, home, idSettleQuiesce, idSettleTimeout)

	// --- Turn 3: post-respawn liveness (AC #2b) -------------------------------
	// Proves the reply bridge rebound past the settings-driven restart.
	sealSendMessage(t, phone, initSend, reqID, sessionCtrlConvID, "m-3",
		fmt.Sprintf("Reply with a single short word. run=%d turn=3", nonce))
	reqID++
	drainForAssistantReply(t, phone, initRecv, sessionCtrlConvID, 3, perTurnReplyBudget)
}

// --- on-disk sessions.json reader (the only genuinely new code) --------------

// uuidStemPattern matches the canonical 36-char lowercase UUIDv4 stem claude uses
// for its <uuid>.jsonl filenames. Transcribed from
// internal/sessions/rotation/watcher.go (the e2e and e2e_realclaude build tags are
// disjoint, so it cannot be imported).
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

// readBootstrapRow returns the Bootstrap==true row or Fatals.
func readBootstrapRow(t *testing.T, home string) bootstrapRow {
	t.Helper()
	row, ok := readBootstrapRowIfPresent(home)
	if !ok {
		t.Fatalf("no bootstrap row in %s", filepath.Join(home, ".pyry", "test", "sessions.json"))
	}
	return row
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

// waitBootstrapModel polls the bootstrap row until its Model equals want or
// timeout elapses. UpdateSettings persists atomically before the reply is sent, so
// this normally matches on the first poll; the bounded poll only closes a
// cross-process fs-visibility window and absorbs a restart rotating the id.
func waitBootstrapModel(t *testing.T, home, want string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last bootstrapRow
	for time.Now().Before(deadline) {
		if row, ok := readBootstrapRowIfPresent(home); ok {
			last = row
			if row.Model == want {
				return
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("bootstrap row Model did not reach %q within %s; last=%+v", want, timeout, last)
}
