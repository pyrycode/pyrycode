//go:build e2e_realclaude

package realclaude

// TestInteractiveStream_NativeReplySuggestionSetThenClear is the #2831 live gate:
// the persistent stream child is spawned with --prompt-suggestions, claude emits
// its native prompt_suggestion after a turn, and the daemon publishes it to an
// interactive client as reply_suggestion. A later accepted send_message must clear
// it with an explicit suggested_reply null at a higher revision.
//
// claude skips suggestions for short conversations and cold caches, so the run
// drives up to suggestTurnBudget conversational turns, each ending on a question
// to the user, and fails (never skips) when no suggestion arrives across all of
// them. The suggestion is claude-authored and untrusted: only its length is logged.

import (
	"encoding/json"
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
)

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

// pumpUntil reads envelopes in order until done reports true or deadline passes.
func (w *suggestWatch) pumpUntil(t *testing.T, h *perConvHarness, timeout time.Duration, done func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !done() {
		env, ok := nextAttachReadEnvelope(t, h, deadline)
		if !ok {
			return false
		}
		w.observe(t, env)
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

	prompts := []string{
		fmt.Sprintf("I want to plan a small vegetable garden on a sunny 3x2 metre balcony this spring. "+
			"Give me a short first suggestion and end your answer with one question to me. run=%d", nonce),
		"Mostly tomatoes and herbs, and I can water once a day. What should I plant first? End with one question to me.",
		"I have about 50 euros for containers and soil. How should I split that budget? End with one question to me.",
		"Good idea. Which herbs grow well next to tomatoes in pots? End with one question to me.",
		"I can start this weekend. What should my first weekend's checklist be? End with one question to me.",
		"Thanks. How do I tell if I am overwatering? End with one question to me.",
	}

	w := &suggestWatch{}
	var envID uint64 = 1
	turns := 0
	for ; turns < suggestTurnBudget && w.setRev == 0; turns++ {
		envID++
		w.sawDelta, w.idle = false, false
		sealSendMessage(t, h.phone, h.initSend, envID, suggestConvID, fmt.Sprintf("m-%d", turns+1), prompts[turns])
		if !w.pumpUntil(t, h, perTurnReplyBudget, func() bool { return w.idle }) {
			t.Fatalf("turn %d never reached turn_state{idle} for %q within %s (sawDelta=%v)",
				turns+1, suggestConvID, perTurnReplyBudget, w.sawDelta)
		}
		w.pumpUntil(t, h, suggestWindow, func() bool { return w.setRev != 0 })
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
	if !w.pumpUntil(t, h, perTurnReplyBudget, func() bool { return w.clearRev != 0 }) {
		t.Fatalf("no reply_suggestion with suggested_reply null and revision > %d for %q within %s after the "+
			"accepted send_message", w.setRev, suggestConvID, perTurnReplyBudget)
	}
	t.Logf("suggestion cleared at revision %d (set was %d)", w.clearRev, w.setRev)
}
