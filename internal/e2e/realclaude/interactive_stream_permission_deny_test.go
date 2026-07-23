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
// kind flips allow_once → reject_once; (2) the post-answer drain proves the turn
// RESOLVES (reaches terminal turn_state{idle}) without requiring a continuation
// delta — real claude's post-denial continuation is non-deterministic, so the
// hard AC is terminal idle, not a delta (drainForTurnIdle, below); (3) a
// filesystem walk proves the gated Write did not execute
// (requireTriggerFileAbsent, below).
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
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakephone"
	"github.com/pyrycode/pyrycode/internal/noise"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

func TestInteractiveStreamPermissionDeny(t *testing.T) {
	h, convID := startStreamModalResolutionHarness(t)
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
	// denied modal. drainForTurnIdle tolerates zero or more continuation deltas
	// and returns on terminal turn_state{idle}; a hang deadlines it.
	drainForTurnIdle(t, h.phone, h.initRecv, convID, perTurnReplyBudget)

	// AC #2, fail-closed observable: the gated Write did NOT execute — its file is
	// absent under the daemon workdir. Checked AFTER idle so the tool phase is
	// definitively over.
	requireTriggerFileAbsent(t, h.workdir, nonce)
}

// drainForTurnIdle reads binary→phone noise_msg frames in receive order — the
// receive nonce is sequential, so every frame MUST be decrypted in order to keep
// the CipherState in sync — and returns once it observes the terminal
// turn_state{idle} for convID. It is drainForCompletedTurn (#1153) MINUS the
// mandatory M1 assistant_delta gate: under DENY the Write is refused and real
// claude's post-denial continuation is non-deterministic (it may emit an
// acknowledgement delta, or go straight to idle), so requiring a delta first
// would risk a flaky false-RED. The hard AC-3 requirement is terminal idle (the
// turn doesn't hang), not a continuation delta — deltas for convID are tolerated
// (logged) and never required.
//
// It is sound to accept the first idle-for-conv as terminal because this drain is
// sequenced AFTER raiseRealPermissionModal (leading turn_state{responding} and
// any pre-turn resting idle already consumed in order) and AFTER the
// modal_dismissed drain (the modal is proven resolved-by-us) — so no earlier idle
// is left on the wire. On timeout: t.Fatalf naming the fail-closed hang.
func drainForTurnIdle(t *testing.T, phone *fakephone.Client, cs *noise.CipherState, convID string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			t.Fatalf("never observed a terminal turn_state{idle} for %q within %s — the turn hung on the denied modal and never reached terminal idle", convID, timeout)
		}
		raw, err := phone.ReceiveBytes(remaining)
		if err != nil {
			if errors.Is(err, fakephone.ErrReceiveTimeout) {
				continue // deadline reached — re-loop into the t.Fatalf above
			}
			t.Fatalf("phone receive (idle drain): %v", err)
		}
		var inner protocol.InnerFrameV2
		if err := json.Unmarshal(raw, &inner); err != nil {
			t.Fatalf("decode inner frame (idle drain): %v", err)
		}
		if inner.Type != protocol.TypeNoiseMsg {
			// A non-noise_msg control frame (e.g. rekey) does not advance the
			// receive nonce — skip without decrypting.
			continue
		}
		cipher, err := base64.StdEncoding.DecodeString(inner.Data)
		if err != nil {
			t.Fatalf("decode inner data (idle drain): %v", err)
		}
		plain, err := cs.Decrypt(cipher)
		if err != nil {
			t.Fatalf("phone decrypt (receive-nonce desync?): %v", err)
		}
		var env protocol.Envelope
		if err := json.Unmarshal(plain, &env); err != nil {
			t.Fatalf("decode envelope (idle drain): %v", err)
		}
		switch env.Type {
		case protocol.TypeError:
			t.Fatalf("unexpected error envelope while awaiting terminal idle: %s", string(env.Payload))
		case protocol.TypeAssistantDelta:
			var p protocol.AssistantDeltaPayload
			if err := json.Unmarshal(env.Payload, &p); err != nil {
				t.Fatalf("decode assistant_delta payload: %v", err)
			}
			// Tolerated, never required: real claude may or may not acknowledge the
			// denial with text before idling.
			if p.ConversationID == convID && strings.TrimSpace(p.Text) != "" {
				t.Logf("post-denial assistant_delta (seq=%d, %d bytes) for %q — tolerated", p.Seq, len(p.Text), convID)
			}
		case protocol.TypeTurnState:
			var st protocol.TurnStatePayload
			if err := json.Unmarshal(env.Payload, &st); err != nil {
				t.Fatalf("decode turn_state payload: %v", err)
			}
			if st.State == "idle" && st.ConversationID == convID {
				t.Logf("terminal turn_state{idle} for %q — the turn resolved without hanging", convID)
				return
			}
		}
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
