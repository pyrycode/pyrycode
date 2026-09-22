//go:build e2e

package e2e

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakephone"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakerelay"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/paireddevice"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// TestE2E_IdleEviction_DefersWhileStreamTurnOpen proves at the binary boundary
// that the daemon hands its stream-path turn-busy tracker to the pool (#1486):
// with a short -pyry-idle-timeout, a session whose conversation has a turn open
// past that window is NOT evicted, and IS evicted once the turn ends.
//
// The open turn is the interrupt spec's fixture: PYRY_FAKE_CLAUDE_STREAM_INTERRUPT
// makes fakeclaude echo the prompt and then withhold the turn's result, so the turn
// stays in flight until an interrupt control_request ends it. The echoed
// assistant_delta is the vacuity guard — it proves the turn opened on a live child
// before any "not evicted" assertion is taken. (The startup-hold fixture cannot
// stand in: a held child never answers the posture control request, so the first
// turn is refused before it is written and no turn ever opens.)
//
// The eviction oracle is the daemon's session.idle_eviction WARN, counted rather
// than merely looked for: before the fix the timer SIGKILLed the child mid-turn and
// a later delivery could respawn it before a registry poll saw "evicted". Counting
// from a baseline taken once the turn is known open also tolerates an eviction the
// bootstrap takes on its own during startup, before msg1 is delivered.
func TestE2E_IdleEviction_DefersWhileStreamTurnOpen(t *testing.T) {
	const (
		initialUUID    = "14861486-1486-4486-8486-148614861486"
		knownConvID    = "86148614-8614-4614-8614-861486148614"
		msgMarker      = "e2e-1486-long-turn"
		sendReqID      = uint64(14861)
		interruptReqID = uint64(14862)
		idle           = 5 * time.Second
		evictNeedle    = "session.idle_eviction"
	)

	home := shortHome(t)
	fr := fakerelay.New(relayTestLogger())
	t.Cleanup(func() { _ = fr.Close() })
	relayURL := fr.URL() + "/v2/server"

	pairPayload, err := paireddevice.Setup(paireddevice.Config{
		Home:         home,
		InstanceName: "test",
		Relay:        relayURL,
		DeviceName:   "phone-a",
	})
	if err != nil {
		t.Fatalf("setup paired device: %v", err)
	}
	pubKey, err := base64.StdEncoding.DecodeString(pairPayload.ServerStaticPubkey)
	if err != nil {
		t.Fatalf("decode server static pubkey: %v", err)
	}

	// Bound to the bootstrap before the daemon loads conversations.json, so the
	// tracker's session→conversation resolve finds it.
	seedBoundConversation(t, home, knownConvID, initialUUID)

	// 5s rather than the 8s of the idle sibling: a startup eviction before the
	// send lands is tolerated (see the baseline below), and the delivery retry
	// chain the 8s protects fits in one window here too.
	h := startPerConvHarnessEnv(t, home, initialUUID, relayURL,
		[]string{"PYRY_FAKE_CLAUDE_STREAM_INTERRUPT=1"},
		"-pyry-idle-timeout="+idle.String())
	regPath := filepath.Join(home, ".pyry", "test", "sessions.json")
	evictions := func() int { return strings.Count(h.Stderr.String(), evictNeedle) }

	phone, initSend, initRecv := dialHelloPhone(t, home, fr, pubKey, pairPayload.Token)

	// nextEnv decrypts every noise_msg in arrival order (the receive nonce is a
	// lockstep counter). ok=false on deadline.
	nextEnv := func(deadline time.Time) (protocol.Envelope, bool) {
		t.Helper()
		for {
			remaining := time.Until(deadline)
			if remaining <= 0 {
				return protocol.Envelope{}, false
			}
			raw, err := phone.ReceiveBytes(remaining)
			if err != nil {
				if errors.Is(err, fakephone.ErrReceiveTimeout) {
					return protocol.Envelope{}, false
				}
				t.Fatalf("phone receive: %v", err)
			}
			var inner protocol.InnerFrameV2
			if err := json.Unmarshal(raw, &inner); err != nil {
				t.Fatalf("decode inner frame: %v", err)
			}
			if inner.Type != protocol.TypeNoiseMsg {
				continue
			}
			return decryptInnerEnvelope(t, inner, initRecv), true
		}
	}

	sendSealedEnvelope(t, phone, initSend, protocol.Envelope{
		ID:   sendReqID,
		Type: protocol.TypeSendMessage,
		TS:   time.Now().UTC(),
		Payload: mustJSON(t, protocol.SendMessagePayload{
			ConversationID: knownConvID,
			MessageID:      "u-1486",
			Text:           msgMarker,
		}),
	})

	// Vacuity guard: the echo proves the turn opened on a live child.
	openDeadline := time.Now().Add(20 * time.Second)
	for opened := false; !opened; {
		env, ok := nextEnv(openDeadline)
		if !ok {
			t.Fatalf("never observed the turn's assistant_delta; no turn opened, so the assertions below "+
				"would be vacuous\nstderr tail:\n%s", stderrTail(h, 4000))
		}
		if env.Type == protocol.TypeError {
			t.Fatalf("unexpected error envelope: %s", string(env.Payload))
		}
		if env.Type != protocol.TypeAssistantDelta {
			continue
		}
		var d protocol.AssistantDeltaPayload
		if err := json.Unmarshal(env.Payload, &d); err != nil {
			t.Fatalf("decode assistant_delta: %v", err)
		}
		opened = d.ConversationID == knownConvID && strings.Contains(d.Text, msgMarker)
	}

	// The turn is open. Two full windows must pass without an idle eviction.
	baseline := evictions()
	time.Sleep(2 * idle)
	if got := evictions(); got != baseline {
		t.Fatalf("idle eviction fired %d time(s) while the turn was open; the pool is not deferring on "+
			"the stream turn-busy signal\nstderr tail:\n%s", got-baseline, stderrTail(h, 4000))
	}
	assertActive(t, regPath, initialUUID)

	// End the turn: the interrupt is the only thing that closes it.
	sendSealedEnvelope(t, phone, initSend, protocol.Envelope{
		ID:      interruptReqID,
		Type:    protocol.TypeInterrupt,
		TS:      time.Now().UTC(),
		Payload: mustJSON(t, protocol.InterruptPayload{ConversationID: knownConvID}),
	})
	closeDeadline := time.Now().Add(15 * time.Second)
	for closed := false; !closed; {
		env, ok := nextEnv(closeDeadline)
		if !ok {
			t.Fatalf("the interrupted turn never closed with turn_state{idle}\nstderr tail:\n%s", stderrTail(h, 4000))
		}
		if env.Type != protocol.TypeTurnState {
			continue
		}
		var st protocol.TurnStatePayload
		if err := json.Unmarshal(env.Payload, &st); err != nil {
			t.Fatalf("decode turn_state: %v", err)
		}
		closed = st.ConversationID == knownConvID && st.State == "idle"
	}

	// No turn open now: the next fire evicts, at most one window late.
	waitForSessionState(t, regPath, initialUUID, "evicted", 3*idle)
	if got := evictions(); got <= baseline {
		t.Fatalf("session evicted but no %s record followed the turn ending (count %d, baseline %d)",
			evictNeedle, got, baseline)
	}
}
