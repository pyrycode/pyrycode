//go:build e2e_realclaude

package realclaude

// TestInteractiveStreamMultiTurnContinuity is the #1173 deliverable: the first
// real-claude e2e that drives MULTIPLE sequential turns on ONE held-open
// stream-json interactive session and proves the same live child retained
// context across turns. The interactive stream runner (internal/streamsup)
// exists to hold one live `claude` child's stdin open across many turns with
// context intact, yet its only real-claude coverage today is single-turn
// (interactive_stream_liveness #1153, interactive_stream_modal_resolution
// #1154); every multi-turn stream behaviour is proven only against a scripted
// fakeclaude, which cannot catch the recurring fake-green/real-red class (#949).
// This adds the missing rung: cross-turn continuity on a held-open child.
//
// The setup body is TRANSCRIBED from #1153's TestInteractiveStreamLiveness
// (interactive_stream_liveness_test.go) with two deltas: (1) it drives a
// 3-entry turn plan over the one held-open session instead of a single send,
// and (2) it asserts continuity by CONTENT — a planted per-run token that a
// later turn must recall. Every Noise-wire / spawn / seed / config helper is
// reused verbatim (same package, same build tag). No production change: the
// stream runner already exists and is green against fakeclaude.
//
// Turn plan (strictly sequential, each fully drained to turn_state{idle} before
// the next send, so no cross-turn frame bleed):
//
//   - Turn 1 (plant)  — instructs claude to remember a per-run-unique token.
//   - Turn 2 (filler) — an intervening turn. Load-bearing for AC #1: it proves
//     the child served a turn BETWEEN plant and recall without respawn; a
//     2-turn plant→recall would not establish "≥3 turns on one held-open child".
//   - Turn 3 (recall) — asks for the token back; its assistant_delta must
//     contain it.
//
// Continuity is the observable that distinguishes "same child served all turns"
// from "unexpected respawn" (AC #3): a freshly respawned, memory-less child has
// no knowledge of the planted token, so the recall can only succeed if one live
// child accumulated conversation context across turns 1→3. The token is kept
// per-run unique (PYRY<nonce> hex) to defeat cross-run caching giving a false
// green — the same run-nonce discipline #1153 uses. No pid/process inspection:
// content memory is a strictly stronger "no respawn" observable than a pid probe
// (and the harness does not expose the child pid) — a deliberate Simplicity-First
// choice.
//
// The two seeds gate the drain exactly as in #1153: the stream runner tags
// turnevents by its construction-time bootstrap pool id (pinned to
// streamMultiTurnBootstrapUUID by seedBootstrapRegistry); the drain gate forwards
// to the emitter only when the tag == activeSession(), which resolves
// streamMultiTurnConvID's binding via seedBoundConversation. A UUID mismatch
// drops every event and hangs the drain, surfacing as the M1 timeout — never a
// silent pass.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakephone"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakerelay"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/paireddevice"
	"github.com/pyrycode/pyrycode/internal/noise"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// Fixed identifiers for the seeded state, distinct in NAME from every same-package
// const (compile-safety) and in LITERAL from feature/1172's 33333333/44444444
// (hygiene — a UUID-literal collision is not a compile error). The 4/8
// version/variant nibbles keep the UUIDv4 shape of the existing fixtures.
// streamMultiTurnBootstrapUUID is the bootstrap session's POOL id (pinned via
// seedBootstrapRegistry); streamMultiTurnConvID is the driving conversation,
// bound to it via seedBoundConversation.
const (
	streamMultiTurnBootstrapUUID = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
	streamMultiTurnConvID        = "dddddddd-dddd-4ddd-8ddd-dddddddddddd"
)

func TestInteractiveStreamMultiTurnContinuity(t *testing.T) {
	// No t.Parallel: WithWorktreeAuthenticated calls t.Setenv.
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skipf("realclaude: claude not on PATH: %v", err)
	}
	home := WithWorktreeAuthenticated(t) // skips cleanly when no creds
	claudeBin, err := exec.LookPath("claude")
	if err != nil {
		t.Fatalf("realclaude: resolve claude: %v", err)
	}

	// Isolated workdir under the authenticated HOME. Load-bearing twice: it
	// guarantees the fresh-daemon state (empty claude sessions dir) AND sidesteps
	// the shared-session-folder collision (#828) by construction.
	workdir := filepath.Join(home, "work")
	if err := os.MkdirAll(workdir, 0o700); err != nil {
		t.Fatalf("realclaude: mkdir workdir: %v", err)
	}

	// Flip the production toggle to the stream-json interactive runner BEFORE the
	// daemon spawns — resolveConfigPath reads <home>/.pyry/config.json once at
	// startup. This is the seam this test exercises end-to-end.
	writeStreamInteractiveConfig(t, home)

	fr := fakerelay.New(relayTestLogger())
	t.Cleanup(func() { _ = fr.Close() })
	relayURL := fr.URL() + "/v2/server"

	payload, err := paireddevice.Setup(paireddevice.Config{
		Home:                   home,
		InstanceName:           "test",
		Relay:                  relayURL,
		DeviceName:             "phone-a",
		AllowRemotePermissions: false,
	})
	if err != nil {
		t.Fatalf("paireddevice.Setup: %v", err)
	}
	pubKey, err := base64.StdEncoding.DecodeString(payload.ServerStaticPubkey)
	if err != nil {
		t.Fatalf("decode server static pubkey: %v", err)
	}

	// Seed the deterministic bootstrap id + the driving conversation binding
	// BEFORE the daemon starts (the registry is loaded once at startup, no reload).
	seedBootstrapRegistry(t, home, streamMultiTurnBootstrapUUID)
	seedBoundConversation(t, home, streamMultiTurnConvID, streamMultiTurnBootstrapUUID, workdir)

	d := spawnBootstrapDaemon(t, home, workdir, claudeBin, relayURL)
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

	// A per-run nonce defeats cross-run caching from producing a false green; the
	// token is a contiguous uppercase alphanumeric identifier (%X → uppercase hex,
	// no internal separators for claude to reformat).
	nonce := time.Now().UnixNano()
	token := fmt.Sprintf("PYRY%X", nonce)

	// Three sequential turns on the ONE held-open session (initSend/initRecv,
	// streamMultiTurnConvID). The filler turn is load-bearing — see the file
	// header. Each turn fully drains to turn_state{idle} before the next send.
	turns := []struct {
		id     uint64
		msgID  string
		prompt string
	}{
		{2, "m-1", fmt.Sprintf("Remember this exact identifier for the rest of our conversation: %s. Reply with just the word ok.", token)},
		{3, "m-2", fmt.Sprintf("Reply with a single short word. run=%d", nonce)},
		{4, "m-3", "What was the exact identifier I asked you to remember earlier? Reply with only that identifier and nothing else."},
	}

	// After the loop, recallText holds the recall turn's (the last turn's) reply.
	// Only that turn's text drives the continuity assertion.
	var recallText string
	for _, turn := range turns {
		sealSendMessage(t, phone, initSend, turn.id, streamMultiTurnConvID, turn.msgID, turn.prompt)
		recallText = drainForCompletedTurnText(t, phone, initRecv, streamMultiTurnConvID, perTurnReplyBudget)
	}

	// Continuity by content: the recall reply must echo the planted token. A
	// respawned/memory-less child cannot produce it, so this cannot pass
	// vacuously. ToUpper absorbs any hex case-folding by claude; Contains
	// (substring, not equality) absorbs surrounding words.
	if !strings.Contains(strings.ToUpper(recallText), token) {
		t.Fatalf("continuity broken: recall turn's reply %q does not contain the planted token %q — "+
			"the held-open child lost context across turns (an unexpected respawn, or dropped conversation memory)",
			recallText, token)
	}
}

// --- capturing turn drain ---------------------------------------------------

// drainForCompletedTurnText is a text-capturing superset of the sibling
// drainForCompletedTurn (#1153): its milestone semantics are byte-identical — a
// non-empty assistant_delta for convID (M1) FOLLOWED BY the terminal
// turn_state{idle} for convID (M2), the same full-turn drain AC #2 requires —
// with exactly one addition: it accumulates EVERY matching assistant_delta.Text
// (not just the first) and returns the concatenation when M2 fires, so the recall
// assertion can inspect the full reply. A separate helper (rather than editing
// the shared drain) keeps this to one new file with no production change and
// leaves the two merged sibling call sites untouched.
//
// It mirrors the sibling's frame loop: read binary→phone noise_msg frames in
// receive order — the receive nonce is sequential, so every noise_msg MUST be
// decrypted in order to keep the CipherState in sync — and skip non-noise_msg
// control frames WITHOUT decrypting (they do not advance the receive nonce;
// decrypting them would desync the CipherState). No content/echo assertion here:
// real claude's words are non-deterministic, so M1 asserts only non-empty text;
// the continuity check lives in the caller. On the deadline, a milestone-specific
// t.Fatalf names the likely cause (a UUID mismatch between the two seeds drops
// every event and hangs the drain).
func drainForCompletedTurnText(t *testing.T, phone *fakephone.Client, cs *noise.CipherState, convID string, timeout time.Duration) string {
	t.Helper()
	sawDelta := false
	var text strings.Builder
	deadline := time.Now().Add(timeout)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			if !sawDelta {
				t.Fatalf("M1: never observed a non-empty assistant_delta for %q within %s — the turn never drained "+
					"end-to-end (delivery never reached the child, or the parser / drain gate / emitter dropped it — "+
					"most likely a UUID mismatch between seedBootstrapRegistry and seedBoundConversation)", convID, timeout)
			}
			t.Fatalf("M2: observed the assistant_delta for %q but never a terminal turn_state{idle} within %s — "+
				"the turn opened but never closed", convID, timeout)
		}
		raw, err := phone.ReceiveBytes(remaining)
		if err != nil {
			if errors.Is(err, fakephone.ErrReceiveTimeout) {
				continue // deadline reached — re-loop into the milestone-specific t.Fatalf above
			}
			t.Fatalf("phone receive (drain): %v", err)
		}
		var inner protocol.InnerFrameV2
		if err := json.Unmarshal(raw, &inner); err != nil {
			t.Fatalf("decode inner frame (drain): %v", err)
		}
		if inner.Type != protocol.TypeNoiseMsg {
			// A non-noise_msg control frame (e.g. rekey) does not advance the
			// receive nonce — skip without decrypting.
			continue
		}
		cipher, err := base64.StdEncoding.DecodeString(inner.Data)
		if err != nil {
			t.Fatalf("decode inner data (drain): %v", err)
		}
		plain, err := cs.Decrypt(cipher)
		if err != nil {
			t.Fatalf("phone decrypt (receive-nonce desync?): %v", err)
		}
		var env protocol.Envelope
		if err := json.Unmarshal(plain, &env); err != nil {
			t.Fatalf("decode envelope (drain): %v", err)
		}
		switch env.Type {
		case protocol.TypeAssistantDelta:
			var p protocol.AssistantDeltaPayload
			if err := json.Unmarshal(env.Payload, &p); err != nil {
				t.Fatalf("decode assistant_delta payload: %v", err)
			}
			// M1: liveness — a non-empty streamed delta for the driving conv. No
			// content/echo assertion (real claude's words are non-deterministic).
			// Every matching delta is accumulated so the caller sees the full reply.
			if p.ConversationID == convID && strings.TrimSpace(p.Text) != "" {
				if !sawDelta {
					sawDelta = true
					t.Logf("M1: non-empty assistant_delta (seq=%d, %d bytes) for %q", p.Seq, len(p.Text), convID)
				}
				text.WriteString(p.Text)
			}
		case protocol.TypeTurnState:
			if !sawDelta {
				continue // the leading responding state precedes the delta
			}
			var st protocol.TurnStatePayload
			if err := json.Unmarshal(env.Payload, &st); err != nil {
				t.Fatalf("decode turn_state payload: %v", err)
			}
			// M2: after the delta, the terminal idle state closes the turn.
			if st.State == "idle" && st.ConversationID == convID {
				t.Logf("M2: terminal turn_state{idle} for %q — the turn closed", convID)
				return text.String()
			}
		}
	}
}
