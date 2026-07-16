//go:build e2e

package e2e

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/protocol"
)

// TestRelayV2_ModalCancelDismisses is the live fake-daemon e2e for the
// `modal_cancel` v2 control verb (#1003, split from #962). It confirms — over one
// spawned daemon + a fake claude that raises a permission modal — the
// phone-driven dismissal path the unit tier (#791/#793) proved deterministically:
//
//	phone modal_cancel{modal_id} frame → Noise decrypt → dispatchAppFrame intercept →
//	  handleModalCancel → ModalResolver.ResolveCancel →
//	    reg.Resolve(modal_id) [one-shot gate] → SendEsc() → lone 0x1b → fakeclaude stdin log
//	  → broadcastModalDismissed → modal_dismissed{modal_id,"cancelled","remote"} → phone
//
// modal_cancel is fire-and-broadcast: there is NO reply correlated to the request.
// The observables are the modal_dismissed broadcast frame plus the bare ESC to the
// child. No send_message/prompt is driven, so the stdin log contains no
// bracketed-paste marker (0x1b 0x5b) — the only ESC in the log is SendEsc's lone
// 0x1b. The bare-ESC oracle (hasBareESC) is still used because a plain "contains
// 0x1b" check is vacuous by policy (every bracketed-paste marker begins 0x1b 0x5b).
//
// Assertion order is load-bearing (the sibling discipline): the modal must be
// observed raised as a permission → its dismissal observed with cancelled/remote →
// the bare ESC on disk, each with a dedicated t.Fatal naming its failure mode, so a
// later assertion cannot pass vacuously over a modal that never surfaced.
//
// AC-4 disposition (one-shot timeout-vs-cancel): the literal 2-minute deny-on-timeout
// variant is not cheap (the deny window is the real modalDenyTimeout inside the pyry
// subprocess and cannot be shrunk from the e2e — cf. TestRelayV2_RemotePermissionDeniedOnTimeout
// paying 2m+20s). The one-shot property is enforced at a single point for both
// cancel-replay and cancel-then-timeout: modalbridge.Registry.Resolve (ResolveCancel
// and ResolveTimeout both bail on ok=false). Step 5's replayed-cancel negative
// exercises that exact gate cheaply (~3s), so this test covers the one-shot distinction
// via the replay path and explicitly defers the literal 2-minute timeout variant.
func TestRelayV2_ModalCancelDismisses(t *testing.T) {
	h := bringUpModalHarness(t)

	// 1. [Vacuous-pass precondition — a real permission modal was raised]. Raise one
	//    modal and assert its permission class BEFORE the cancel, else the dismissal
	//    assertion is vacuous. Capture the modal_id to name in the cancel frame.
	shown := awaitModalShown(t, h)
	if shown.Class != "permission" {
		t.Fatalf("modal_shown Class = %q, want %q (no permission modal was raised — the cancel/dismissal assertions would be vacuous)", shown.Class, "permission")
	}
	modalID := shown.ModalID

	// 2. Send the cancel naming the raised modal. Fire-and-broadcast: no ack is
	//    correlated to this request, so the request ID is cosmetic.
	h.sealSend(protocol.Envelope{
		ID:      41,
		Type:    protocol.TypeModalCancel,
		TS:      time.Now().UTC(),
		Payload: mustJSON(t, protocol.ModalCancelPayload{ModalID: modalID}),
	})

	// 3. [AC-2 — the dismissal fans back over the wire]. Drain under one long deadline
	//    with back-to-back reads, skipping non-dismissal frames; t.Fatal on an
	//    unexpected error envelope and on deadline (each naming its failure mode). The
	//    cancel path carries Outcome="cancelled", Source="remote" (ResolveCancel's
	//    fixed vocabulary).
	dismissDeadline := time.Now().Add(20 * time.Second)
	for {
		env, ok := h.nextEnv(dismissDeadline)
		if !ok {
			t.Fatal("did not observe modal_dismissed after the cancel before deadline (the cancel did not route to ResolveCancel / broadcastModalDismissed)")
		}
		if env.Type == protocol.TypeError {
			t.Fatalf("unexpected error envelope while awaiting modal_dismissed: %s", string(env.Payload))
		}
		if env.Type != protocol.TypeModalDismissed {
			continue
		}
		var dis protocol.ModalDismissedPayload
		if err := json.Unmarshal(env.Payload, &dis); err != nil {
			t.Fatalf("decode modal_dismissed payload: %v", err)
		}
		// Wrong/absent modal is a hard fail: it invalidates the bare-ESC oracle below.
		if dis.ModalID != modalID {
			t.Fatalf("modal_dismissed ModalID = %q, want %q", dis.ModalID, modalID)
		}
		if dis.Outcome != "cancelled" {
			t.Errorf("modal_dismissed Outcome = %q, want %q", dis.Outcome, "cancelled")
		}
		if dis.Source != "remote" {
			t.Errorf("modal_dismissed Source = %q, want %q", dis.Source, "remote")
		}
		break
	}

	// 4. [AC-3 — the bare ESC reached the child]. modal_dismissed is a happens-after
	//    fence, but the ESC crosses a process boundary (SendEsc writes the PTY;
	//    fakeclaude reads+fsyncs asynchronously), so mirror the interrupt test's bounded
	//    poll rather than a single read. hasBareESC requires a 0x1b NOT followed by 0x5b
	//    ('['): a plain "contains 0x1b" check is vacuous since every bracketed-paste
	//    marker begins 0x1b 0x5b. No prompt is driven here, so the lone SendEsc 0x1b is
	//    the only ESC in the log.
	var logAfterCancel []byte
	escDeadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(escDeadline) {
		logAfterCancel, _ = os.ReadFile(h.stdinLog)
		if hasBareESC(logAfterCancel) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !hasBareESC(logAfterCancel) {
		t.Fatalf("stdin log has no bare ESC (0x1b not followed by 0x5b) after the cancel; the cancel keystroke never routed to SendEsc\nstdin log: %q", logAfterCancel)
	}
	// Belt-and-suspenders, different fabric from the wire report: cancel routes the ESC
	// deny, never a "<n>\r" answer digit.
	assertNoAnswerDigit(t, logAfterCancel, "after modal_cancel")

	// 5. [AC-4 cheap one-shot negative — a replayed cancel is inert]. Re-send the
	//    IDENTICAL cancel. The registry Resolve already consumed the modal, so the
	//    replay misses the one-shot gate ⇒ no SendEsc, no broadcast. Drain under a short
	//    deadline: break on drain (the expected no-op); t.Fatal on a second dismissal or
	//    any error. Then assert the stdin log is byte-for-byte unchanged — no second ESC
	//    routed. This is the cheap substitute for the literal 2-minute timeout variant
	//    (see the doc comment's AC-4 disposition).
	h.sealSend(protocol.Envelope{
		ID:      42,
		Type:    protocol.TypeModalCancel,
		TS:      time.Now().UTC(),
		Payload: mustJSON(t, protocol.ModalCancelPayload{ModalID: modalID}),
	})

	replayDeadline := time.Now().Add(3 * time.Second)
	for {
		env, ok := h.nextEnv(replayDeadline)
		if !ok {
			break // drained — no second dismissal (the expected one-shot outcome)
		}
		if env.Type == protocol.TypeModalDismissed {
			t.Fatalf("replayed cancel produced a second modal_dismissed (one-shot gate violated): %s", string(env.Payload))
		}
		if env.Type == protocol.TypeError {
			t.Fatalf("replayed cancel produced an error envelope: %s", string(env.Payload))
		}
	}

	logAfterReplay, err := os.ReadFile(h.stdinLog)
	if err != nil {
		t.Fatalf("read stdin log after the replayed cancel: %v", err)
	}
	if !bytes.Equal(logAfterReplay, logAfterCancel) {
		t.Fatalf("stdin log changed after the replayed cancel (a second ESC routed — one-shot gate violated)\nbefore: %q\nafter:  %q", logAfterCancel, logAfterReplay)
	}
}
