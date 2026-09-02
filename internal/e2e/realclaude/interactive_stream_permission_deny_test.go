//go:build e2e_realclaude

package realclaude

// TestInteractiveStreamPermissionDeny is the #1175 deliverable (split from
// #1083, T9): the security-relevant half of the remote permission round-trip on
// the production stream-json interactive runner, proven against a live
// `claude --model haiku`. #1154's TestInteractiveStreamModalResolution proved
// ALLOW on the same stack; this proves DENY — a permission-gated tool is refused
// (fails CLOSED) via an explicit, device-authorized reject to a modal that
// genuinely surfaced, the turn resolves to terminal idle rather than hanging, and
// the gated Write's file never materialises. A denial that silently EXECUTED the
// tool (fail-open), or a turn that HUNG on the denied modal, is exactly the
// real-claude-specific failure the fake tier cannot surface (the recurring
// fake-green/real-red class, e.g. #949).
//
// It is a near-clone of #1154's file with three deltas: (1) the answer option
// kind flips allow_once → reject_once; (2) real claude RETRIES a denied tool at
// least once (observed in the #1175 operator run: haiku reissues the Write as a
// second, distinct tool_use after the first reject), so the post-answer drain
// denies EVERY modal the turn raises until it reaches terminal turn_state{idle} —
// a single reject leaves the retry modal unanswered and the turn hangs on it (the
// #1175 operator-run failure). The hard AC is terminal idle reached by our
// explicit rejects, not a continuation delta, and it is bounded so a
// non-terminating turn fails loud rather than hanging the suite
// (denyModalsUntilIdle, below); (3) a filesystem walk proves the gated Write did
// not execute (requireTriggerFileAbsent, below).
//
// The load-bearing check unique to a deny test: "file absent + turn idle" is
// ALSO the outcome of several NON-deny paths — a timeout-deny (the #725 approval
// timer fires → modal_dismissed{source=timeout}), a device-gate rejection (no
// dismissal at all → later timeout), or any path that silently drops our answer.
// So the test actively attributes the resolution to OUR explicit reject: it
// drains modal_dismissed and asserts Source == "remote" AND
// Outcome == "reject_once" for the answered modal. That conjunction is the only
// thing that pins the deny to this answer; without it the test could green even
// if remote reject answers were completely broken.
//
// It reuses the #1154 harness (startStreamModalResolutionHarness — pairs the
// phone --allow-remote-permissions, the answer device gate that DENY needs too;
// writes the stream-json toggle; spawns via spawnPermissionDaemon WITHOUT
// --dangerously-skip-permissions so claude genuinely gates the Write) and the
// #1030 trigger/drain scaffold (writeFileTrigger, raiseRealPermissionModal,
// drainForControlEvent) VERBATIM — no new harness, no new seeded-UUID constant,
// no edit to any existing file. Like the sibling gates this is a standing
// real-claude gate in preship, not a deterministic RED/GREEN oracle; the fake
// tier owns timeout-deny (#1139) and the PTY real tier owns cancel (#1030
// Phase B). It skips cleanly when claude / creds are absent.

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakephone"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

func TestInteractiveStreamPermissionDeny(t *testing.T) {
	h, convID := startStreamModalResolutionHarness(t, permissionDaemonModel)
	// A per-run nonce keeps the trigger's target filename unique (defeats
	// accidental caching AND makes the absence walk unambiguous — no other run's
	// file can false-match).
	nonce := time.Now().UnixNano()

	// AC #1 + non-vacuity: raise a genuine permission modal via the gated Write.
	// The non-vacuity gate lives inside the helper — it drains to modal_shown and
	// asserts Class == "permission" + non-empty modal_id BEFORE returning, so a
	// modal that never surfaced deadlines the drain rather than passing silently.
	// This proves the Write was requested and HELD — the anchor that makes the
	// later absence check non-vacuous.
	modalID := raiseRealPermissionModal(t, h, 2, convID, writeFileTrigger(nonce))

	// Answer DENY. The ONLY change from #1154's allow envelope is
	// allow_once → reject_once. AnswerToken is a client-minted idempotency key,
	// NOT authorization (authorization is ModalID validity + the device gate).
	sealEnvelope(t, h.phone, h.initSend, protocol.Envelope{
		ID:   3,
		Type: protocol.TypeModalAnswer,
		TS:   time.Now().UTC(),
		Payload: mustJSON(t, protocol.ModalAnswerPayload{
			ModalID:     modalID,
			OptionID:    string(turnevent.PermissionOptionKindRejectOnce),
			AnswerToken: "e2e-1175-answer-token",
		}),
	})

	// Attribution — the deny is OURS, not a timeout or a dropped answer. Drain to
	// modal_dismissed and assert Source == "remote" && Outcome == "reject_once"
	// for the answered modal. remote is set only on the gated-answer path
	// (modal_resolve_v2.go), never on the #725 timeout path — so a timeout-deny
	// (source=timeout) or a gate-rejected answer (no dismissal at all) cannot
	// green in place of this explicit reject.
	dismissedEnv := drainForControlEvent(t, h.phone, h.initRecv, protocol.TypeModalDismissed, modalSurfaceBudget)
	var dis protocol.ModalDismissedPayload
	if err := json.Unmarshal(dismissedEnv.Payload, &dis); err != nil {
		t.Fatalf("decode modal_dismissed payload: %v", err)
	}
	if dis.ModalID != modalID {
		t.Errorf("modal_dismissed ModalID = %q, want %q", dis.ModalID, modalID)
	}
	if dis.Source != "remote" {
		t.Errorf("modal_dismissed Source = %q, want %q — the deny must be attributable to our explicit answer, not a timeout or a dropped/gate-rejected answer", dis.Source, "remote")
	}
	if dis.Outcome != string(turnevent.PermissionOptionKindRejectOnce) {
		t.Errorf("modal_dismissed Outcome = %q, want %q", dis.Outcome, string(turnevent.PermissionOptionKindRejectOnce))
	}

	// AC #3: the turn resolves (aware it was denied) rather than hanging on the
	// denied modal. Real claude retries a denied tool at least once (the #1175
	// operator run: haiku reissued the Write as a second, distinct tool_use), so a
	// single reject does not resolve the turn — the retry modal sits unanswered and
	// the turn hangs on it. denyModalsUntilIdle answers EVERY retry modal with a
	// fresh reject_once until terminal turn_state{idle}, bounded on two axes so a
	// non-terminating turn fails loud: at most maxRetryDenies retry modals (proven:
	// claude retries once; NOT proven it gives up after N — the cap keeps the loop
	// finite and its diagnostic distinct from the wall-clock's), and within
	// perTurnReplyBudget.
	const (
		firstRetryReqID uint64 = 4 // the trigger used reqID 2, the first answer reqID 3
		maxRetryDenies         = 4 // observed one retry; cap well above that, still bounded
	)
	denyModalsUntilIdle(t, h, convID, firstRetryReqID, maxRetryDenies, perTurnReplyBudget)

	// AC #2, fail-closed observable: the gated Write did NOT execute — its file is
	// absent under the daemon workdir. Checked AFTER idle so the tool phase is
	// definitively over.
	requireTriggerFileAbsent(t, h.workdir, nonce)
}

// denyModalsUntilIdle drives a fully-denied turn to terminal turn_state{idle} for
// convID by rejecting EVERY permission modal claude raises along the way. Real
// claude retries a denied tool at least once (observed in the #1175 operator run:
// haiku reissued the Write as a second, distinct tool_use after the first reject),
// so a single reject does not resolve the turn — the retry modal sits unanswered
// and the turn hangs on it until the daemon's own approval timeout fires. This
// loop answers each retry with a fresh reject_once so the turn reaches idle by OUR
// explicit denials, not by the daemon's fail-closed timeout.
//
// It reads binary→phone noise_msg frames in receive order (the receive nonce is
// sequential, so every noise_msg MUST be decrypted in order to keep h.initRecv in
// sync; non-noise_msg control frames are skipped without decrypting) and:
//   - on modal_shown{permission}: asserts class + non-empty modal_id (a retry
//     modal is as non-vacuous as the first), then seals a reject_once answer with
//     a FRESH AnswerToken and a unique request id;
//   - on modal_dismissed: asserts Source == "remote" — every dismissal here must be
//     OUR reject, never a daemon timeout-deny (Source == "timeout"); a timeout
//     dismissal means our answer was dropped and the turn fell closed on its own,
//     which the deny attribution must not silently accept;
//   - on turn_state{idle} for convID: returns — the turn resolved without hanging.
//
// It is bounded on two axes so a pathologically non-terminating turn fails LOUD
// rather than hanging the suite: maxRetryDenies (a cap on retry modals answered)
// and the wall-clock timeout. It is proven claude retries once; it is NOT proven
// it gives up after a bounded number of denies, so the two bounds produce distinct
// diagnostics — the cap ("claude keeps reissuing past the cap", a product question
// about whether a fully-denied turn ever terminates) vs. the wall-clock ("no idle
// after the final deny", the daemon stalled).
//
// Accepting the first idle-for-conv as terminal is sound: this drain is sequenced
// AFTER raiseRealPermissionModal (leading turn_state{responding} and any pre-turn
// resting idle already consumed in order) and AFTER the modal_dismissed drain (the
// first modal proven resolved-by-us), so no earlier idle is left on the wire.
// startReqID is the first request id for a retry answer (the trigger used 2, the
// first answer 3, so retries start at 4); each answer increments it.
func denyModalsUntilIdle(t *testing.T, h *perConvHarness, convID string, startReqID uint64, maxRetryDenies int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	reqID := startReqID
	retryDenies := 0
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			t.Fatalf("turn for %q never reached terminal turn_state{idle} within %s after %d retry reject(s) — the turn hung on a denied modal our reject never resolved", convID, timeout, retryDenies)
		}
		raw, err := h.phone.ReceiveBytes(remaining)
		if err != nil {
			if errors.Is(err, fakephone.ErrReceiveTimeout) {
				continue // deadline reached — re-loop into the t.Fatalf above
			}
			t.Fatalf("phone receive (deny-until-idle): %v", err)
		}
		var inner protocol.InnerFrameV2
		if err := json.Unmarshal(raw, &inner); err != nil {
			t.Fatalf("decode inner frame (deny-until-idle): %v", err)
		}
		if inner.Type != protocol.TypeNoiseMsg {
			// A non-noise_msg control frame (e.g. rekey) does not advance the
			// receive nonce — skip without decrypting.
			continue
		}
		cipher, err := base64.StdEncoding.DecodeString(inner.Data)
		if err != nil {
			t.Fatalf("decode inner data (deny-until-idle): %v", err)
		}
		plain, err := h.initRecv.Decrypt(cipher)
		if err != nil {
			t.Fatalf("phone decrypt (receive-nonce desync?): %v", err)
		}
		var env protocol.Envelope
		if err := json.Unmarshal(plain, &env); err != nil {
			t.Fatalf("decode envelope (deny-until-idle): %v", err)
		}
		switch env.Type {
		case protocol.TypeError:
			t.Fatalf("unexpected error envelope while denying to idle: %s", string(env.Payload))
		case protocol.TypeModalShown:
			var shown protocol.ModalShownPayload
			if err := json.Unmarshal(env.Payload, &shown); err != nil {
				t.Fatalf("decode retry modal_shown payload: %v", err)
			}
			if shown.Class != "permission" {
				t.Fatalf("retry modal_shown Class = %q, want %q", shown.Class, "permission")
			}
			if shown.ModalID == "" {
				t.Fatal("retry modal_shown carried an empty modal_id")
			}
			if retryDenies >= maxRetryDenies {
				t.Fatalf("claude raised more than %d retry permission modals for %q without reaching idle — it keeps reissuing the denied tool past the cap; whether a fully-denied turn ever terminates is a product question to raise separately, not a test flake to widen the cap for", maxRetryDenies, convID)
			}
			retryDenies++
			sealEnvelope(t, h.phone, h.initSend, protocol.Envelope{
				ID:   reqID,
				Type: protocol.TypeModalAnswer,
				TS:   time.Now().UTC(),
				Payload: mustJSON(t, protocol.ModalAnswerPayload{
					ModalID:     shown.ModalID,
					OptionID:    string(turnevent.PermissionOptionKindRejectOnce),
					AnswerToken: fmt.Sprintf("e2e-1175-retry-token-%d", reqID),
				}),
			})
			t.Logf("denied retry permission modal %q (reject #%d) for %q", shown.ModalID, retryDenies, convID)
			reqID++
		case protocol.TypeModalDismissed:
			var dis protocol.ModalDismissedPayload
			if err := json.Unmarshal(env.Payload, &dis); err != nil {
				t.Fatalf("decode retry modal_dismissed payload: %v", err)
			}
			if dis.Source != "remote" {
				t.Fatalf("retry modal_dismissed Source = %q, want %q — a modal resolved by the daemon's approval timeout (or any non-remote source) means the turn fell closed on its own, not by our explicit reject; the deny attribution must hold for every modal, not only the first", dis.Source, "remote")
			}
		case protocol.TypeTurnState:
			var st protocol.TurnStatePayload
			if err := json.Unmarshal(env.Payload, &st); err != nil {
				t.Fatalf("decode turn_state payload: %v", err)
			}
			if st.State == "idle" && st.ConversationID == convID {
				t.Logf("terminal turn_state{idle} for %q after %d retry reject(s) — the turn resolved without hanging", convID, retryDenies)
				return
			}
		}
		// assistant_delta / tool_use / turn_end / ack — decrypted above (the receive
		// nonce stays in sync) and drained in order.
	}
}

// requireTriggerFileAbsent walks the daemon workdir subtree and fails if any
// entry's base name is pyrycode-<nonce>.txt — the file the gated Write targeted.
// Absence proves the deny fell CLOSED: the parked stream Write was refused and
// nothing materialised. A walk (not a single os.Stat of
// <workdir>/pyrycode-<nonce>.txt) is deterministic belt-and-suspenders in
// DIFFERENT fabric to the stochastic modal gate: absence only at the one path we
// happened to check would be a vacuous green if claude wrote to a sub-path. The
// unique per-run nonce guarantees the walk cannot false-match any other file.
func requireTriggerFileAbsent(t *testing.T, workdir string, nonce int64) {
	t.Helper()
	target := fmt.Sprintf("pyrycode-%d.txt", nonce)
	var found string
	err := filepath.WalkDir(workdir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !d.IsDir() && d.Name() == target {
			found = path
			return fs.SkipAll
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk workdir %q for %s: %v", workdir, target, err)
	}
	if found != "" {
		t.Fatalf("fail-open regression: the gated Write executed despite reject_once — %s materialised at %s", target, found)
	}
}
