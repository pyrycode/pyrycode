//go:build e2e_realclaude

package realclaude

// TestInteractiveStream_NativeReplySuggestionSetThenClear is the #2831 live gate:
// the persistent stream child is spawned with --prompt-suggestions, claude emits
// its native prompt_suggestion after a turn, and the daemon publishes it to an
// interactive client as reply_suggestion. A later accepted send_message must clear
// it with an explicit suggested_reply null at a higher revision.
//
// claude skips suggestions for short conversations, cold caches and turns whose
// next step is not obvious, so the run drives up to suggestTurnBudget small coding
// steps, each with an obvious follow-up, and fails (never skips) when no suggestion arrives across all of
// them. The suggestion is claude-authored and untrusted: only its length is logged.

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/protocol"
)

const suggestConvID = "28310000-0000-4000-8000-000000000002"

const (
	suggestTurnBudget = 6
	// suggestWindow bounds the wait for a suggestion after a turn's idle state.
	suggestWindow = 30 * time.Second
	// suggestReadBudget is the single read deadline of the reader goroutine. It
	// outlasts every wait the test makes; the waits time out on its channel.
	suggestReadBudget = 15 * time.Minute
)

// suggestFrame is one envelope the reader goroutine decoded, or the error that
// ended it.
type suggestFrame struct {
	env protocol.Envelope
	err error
}

// startSuggestReader owns every phone read for the run. A fakephone read whose
// timeout fires closes the websocket (coder/websocket ties the conn to the read
// context), so a wait for a suggestion that never comes would leave the next
// turn's send writing to a closed conn. The waits time out on the returned
// channel instead, and reads stay on one goroutine so receive nonces stay ordered.
func startSuggestReader(t *testing.T, h *perConvHarness) <-chan suggestFrame {
	t.Helper()
	frames := make(chan suggestFrame, 64)
	done := make(chan struct{})
	t.Cleanup(func() { close(done) })
	go func() {
		defer close(frames)
		emit := func(f suggestFrame) bool {
			select {
			case frames <- f:
				return true
			case <-done:
				return false
			}
		}
		for {
			env, err := readSuggestEnvelope(h)
			if errors.Is(err, errSkipFrame) {
				continue
			}
			if !emit(suggestFrame{env: env, err: err}) || err != nil {
				return
			}
		}
	}()
	return frames
}

var errSkipFrame = errors.New("non-noise_msg frame")

// readSuggestEnvelope reads and opens one frame. A non-noise_msg control frame
// (e.g. rekey) carries no envelope and does not advance the receive nonce.
func readSuggestEnvelope(h *perConvHarness) (protocol.Envelope, error) {
	raw, err := h.phone.ReceiveBytes(suggestReadBudget)
	if err != nil {
		return protocol.Envelope{}, fmt.Errorf("phone receive: %w", err)
	}
	var inner protocol.InnerFrameV2
	if err := json.Unmarshal(raw, &inner); err != nil {
		return protocol.Envelope{}, fmt.Errorf("decode inner frame: %w", err)
	}
	if inner.Type != protocol.TypeNoiseMsg {
		return protocol.Envelope{}, errSkipFrame
	}
	cipher, err := base64.StdEncoding.DecodeString(inner.Data)
	if err != nil {
		return protocol.Envelope{}, fmt.Errorf("decode inner data: %w", err)
	}
	plain, err := h.initRecv.Decrypt(cipher)
	if err != nil {
		return protocol.Envelope{}, fmt.Errorf("phone decrypt (receive-nonce desync?): %w", err)
	}
	var env protocol.Envelope
	if err := json.Unmarshal(plain, &env); err != nil {
		return protocol.Envelope{}, fmt.Errorf("decode envelope: %w", err)
	}
	return env, nil
}

// suggestWatch folds every envelope the run reads into the state it asserts on.
type suggestWatch struct {
	sawDelta, idle bool
	setRev         uint64 // latest non-empty suggestion's revision; 0 = none yet
	clearRev       uint64 // revision of a clear above setRev; 0 = none yet
}

func (w *suggestWatch) observe(t *testing.T, env protocol.Envelope) {
	t.Helper()
	switch env.Type {
	case protocol.TypeError:
		var ep protocol.ErrorPayload
		if err := json.Unmarshal(env.Payload, &ep); err != nil {
			t.Fatalf("daemon sent an error whose payload did not decode: %v", err)
		}
		t.Fatalf("daemon sent error code %q (retryable=%v)", ep.Code, ep.Retryable)
	case protocol.TypeAssistantDelta:
		var p protocol.AssistantDeltaPayload
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			t.Fatalf("decode assistant_delta payload: %v", err)
		}
		if p.ConversationID == suggestConvID && strings.TrimSpace(p.Text) != "" {
			w.sawDelta = true
		}
	case protocol.TypeTurnState:
		var st protocol.TurnStatePayload
		if err := json.Unmarshal(env.Payload, &st); err != nil {
			t.Fatalf("decode turn_state payload: %v", err)
		}
		if st.State == "idle" && st.ConversationID == suggestConvID && w.sawDelta {
			w.idle = true
		}
	case protocol.TypeReplySuggestion:
		// Raw suggested_reply so an explicit null is told apart from an omission,
		// which the protocol says does not clear.
		var p struct {
			ConversationID string          `json:"conversation_id"`
			SessionID      string          `json:"session_id"`
			Revision       uint64          `json:"revision"`
			SuggestedReply json.RawMessage `json:"suggested_reply"`
		}
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			t.Fatalf("decode reply_suggestion payload: %v", err)
		}
		if p.ConversationID != suggestConvID {
			return
		}
		if string(p.SuggestedReply) == "null" {
			t.Logf("reply_suggestion clear: revision=%d session_id set=%v", p.Revision, p.SessionID != "")
			if w.setRev != 0 && p.Revision > w.setRev {
				w.clearRev = p.Revision
			}
			return
		}
		var text string
		if err := json.Unmarshal(p.SuggestedReply, &text); err != nil {
			t.Fatalf("reply_suggestion suggested_reply is neither null nor a string: %v", err)
		}
		t.Logf("reply_suggestion set: revision=%d length=%d session_id set=%v", p.Revision, len(text), p.SessionID != "")
		if strings.TrimSpace(text) != "" && p.Revision > w.setRev {
			w.setRev = p.Revision
		}
	}
}

// pumpUntil observes envelopes in order until done reports true or timeout passes.
// A timeout leaves the conn open: only the channel wait expires.
func (w *suggestWatch) pumpUntil(t *testing.T, frames <-chan suggestFrame, timeout time.Duration, done func() bool) bool {
	t.Helper()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for !done() {
		select {
		case f, ok := <-frames:
			if !ok {
				t.Fatalf("phone reader stopped")
			}
			if f.err != nil {
				t.Fatalf("%v", f.err)
			}
			w.observe(t, f.env)
		case <-timer.C:
			return false
		}
	}
	return true
}

func TestInteractiveStream_NativeReplySuggestionSetThenClear(t *testing.T) {
	h := startPerConversationHarnessSeeded(t, func(home, workdir string) []string {
		writeStreamInteractiveConfig(t, home)
		seedBoundConversation(t, home, suggestConvID, livePerConvBootstrapUUID, workdir)
		return nil
	})
	nonce := time.Now().UnixNano()
	frames := startSuggestReader(t, h)

	// Coding steps with an obvious next one: claude's suggestion prompt stays
	// silent when the next step is not obvious, so open questions to the user
	// (the first version of this test) drew no suggestion in six turns.
	prompts := []string{
		fmt.Sprintf("Write a Go function that reverses a string. Reply in chat only, do not create files. Just the code, briefly. run=%d", nonce),
		"Now add a unit test for it. Just the code.",
		"Now make it handle unicode correctly. Just the code.",
		"Now add a benchmark for it. Just the code.",
		"Now add a doc comment to the function. Just the code.",
		"Now add an example test for it. Just the code.",
	}

	w := &suggestWatch{}
	var envID uint64 = 1
	turns := 0
	for ; turns < suggestTurnBudget && w.setRev == 0; turns++ {
		envID++
		w.sawDelta, w.idle = false, false
		sealSendMessage(t, h.phone, h.initSend, envID, suggestConvID, fmt.Sprintf("m-%d", turns+1), prompts[turns])
		if !w.pumpUntil(t, frames, perTurnReplyBudget, func() bool { return w.idle }) {
			t.Fatalf("turn %d never reached turn_state{idle} for %q within %s (sawDelta=%v)",
				turns+1, suggestConvID, perTurnReplyBudget, w.sawDelta)
		}
		w.pumpUntil(t, frames, suggestWindow, func() bool { return w.setRev != 0 })
	}
	if w.setRev == 0 {
		t.Fatalf("no non-empty reply_suggestion for %q after %d completed turns (each followed by a %s window); "+
			"CLAUDE_CODE_ENABLE_PROMPT_SUGGESTION=%q in the test env", suggestConvID, turns, suggestWindow,
			os.Getenv("CLAUDE_CODE_ENABLE_PROMPT_SUGGESTION"))
	}
	t.Logf("suggestion set at revision %d after %d turn(s)", w.setRev, turns)

	envID++
	sealSendMessage(t, h.phone, h.initSend, envID, suggestConvID, "m-clear",
		"Sounds good, thank you. Reply with a single short sentence.")
	if !w.pumpUntil(t, frames, perTurnReplyBudget, func() bool { return w.clearRev != 0 }) {
		t.Fatalf("no reply_suggestion with suggested_reply null and revision > %d for %q within %s after the "+
			"accepted send_message", w.setRev, suggestConvID, perTurnReplyBudget)
	}
	t.Logf("suggestion cleared at revision %d (set was %d)", w.clearRev, w.setRev)
}
