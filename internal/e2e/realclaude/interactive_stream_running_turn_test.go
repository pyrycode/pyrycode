//go:build e2e_realclaude

package realclaude

// TestInteractiveStreamRunningTurn is the #1172 deliverable (split from #1083,
// T9): reusable trigger infra that holds a live claude turn in the
// turn_state{responding} state for a bounded window, plus the smoke that proves
// the infra works. It stands up the interactive daemon under the production
// stream-json interactive runner (interactive_runner: "stream-json") against a
// live claude, drives ONE turn whose sole action is a bounded foreground shell
// loop, and asserts the turn enters responding and STAYS there — no terminal
// turn_state{idle} — for at least runningTurnHold after responding is first seen.
// Infra only: no interrupt or resume behaviour is asserted here.
//
// The reusable seam #1176 (interrupt) composes is startStreamRunningTurnHarness +
// driveRunningTurn + drainForResponding (same package, same build tag), exactly
// as #1153 flagged writeStreamInteractiveConfig for #1154. #1176 is already
// natively blocked-by this ticket.
//
// The load-bearing mechanism (why a Bash loop holds responding): the interactive
// turn emitter derives turn_state statefully (cmd/pyry/interactive_turn_v2.go).
// ToolStart -> responding is emitted the instant claude issues the Bash tool call,
// BEFORE the command runs; during the command's execution the emitter emits
// nothing new (currentState == responding, de-duped); TurnEnd -> idle is emitted
// once, at the very end of the whole turn (after the tool completes and claude
// replies). So for a turn that runs one Bash loop of L seconds the wire sequence
// is: (optional thinking) -> responding (at tool start) -> [loop runs Ls, no new
// turn_state] -> assistant_delta (the reply) -> idle (~Ls after responding).
// Observing responding then verifying no idle for a window hold < L is exactly the
// running-turn proof. If claude backgrounds the command or refuses to loop, idle
// arrives early -> the smoke fails loud (never a silent pass).
//
// The daemon is spawned via spawnBootstrapDaemon (--dangerously-skip-permissions),
// NOT spawnPermissionDaemon: we do NOT want a permission modal to block the Bash
// call. This mirrors interactive_stream_liveness (#1153) and is the opposite
// posture from the modal specs (#1030/#1154), which drop the flag to raise a modal.
//
// Contrast with the fake path: the fakeclaude interrupt proof (#794,
// TestRelayV2_StreamInterruptStopsRunningTurn) scripts a held turn via a JSONL
// trigger file — real claude can't be scripted, hence the foreground-loop prompt
// here (the port of desktop e1fe219, 2026-07-17: a silent `sleep` gets
// backgrounded and ends the turn early; a chatty per-iteration loop floods the
// frame stream and delays turn_state delivery; the working approach drives the
// turn with a bounded, silent, foreground shell loop inside a single Bash-tool
// call). No content/echo assertion anywhere — real claude's output is
// non-deterministic; the smoke asserts only turn_state transitions and elapsed
// time. Like #854/#997/#1030/#1153/#1154 this is a standing real-claude liveness
// gate in preship, not a deterministic RED/GREEN oracle; placement under the
// e2e_realclaude build tag wires it into `make e2e-realclaude` via the package
// glob (no Makefile change), mirroring interactive_stream_liveness.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakephone"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakerelay"
	"github.com/pyrycode/pyrycode/internal/noise"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// Seeded state for this gate. Distinct NAMES and LITERALS from every existing
// fixture in the package (streamBootstrapUUID/streamConvID,
// streamModalBootstrapUUID/streamModalConvID, liveBootstrapUUID/liveConvID,
// liveModalBootstrapUUID/liveModalConvID, livePerConvBootstrapUUID) so no file
// redeclares a name and no literal collides. runningTurnBootstrapUUID is the
// bootstrap session's POOL id (pinned via seedBootstrapRegistry);
// runningTurnConvID is the driving conversation, bound to it via
// seedBoundConversation. Each test owns its own authenticated tempdir HOME, so
// literals never collide on disk; distinct values only keep cross-test confusion
// impossible.
const (
	runningTurnBootstrapUUID = "33333333-3333-4333-8333-333333333333"
	runningTurnConvID        = "44444444-4444-4444-8444-444444444444"
)

// runningTurnHold is the minimum window the turn must remain in responding after
// it is first observed. Chosen much greater than #1176's interrupt round-trip
// (phone -> relay -> daemon -> claude interrupt is sub-second), with wide margin.
// runningTurnLoopSlack is extra Bash-loop wall-clock beyond hold, so idle cannot
// race the window boundary (the loop is still running comfortably at
// firstResponding + hold).
//
// Coupling constraint: the Bash loop's wall-clock L = hold + slack must satisfy
// L > hold (so no early idle) AND L < 120s (the claude Bash-tool default timeout —
// a loop that hits it ends the turn early). 20+20 = 40s satisfies both. If #1176
// later needs a longer in-flight window, bump runningTurnHold, keeping
// hold + runningTurnLoopSlack < 120s.
const (
	runningTurnHold      = 20 * time.Second
	runningTurnLoopSlack = 20 * time.Second
)

// TestInteractiveStreamRunningTurn drives one real claude turn into responding via
// a bounded foreground Bash loop and proves the turn stays running (no terminal
// idle) for runningTurnHold. Infra only — no interrupt/resume behaviour asserted.
func TestInteractiveStreamRunningTurn(t *testing.T) {
	h, convID := startStreamRunningTurnHarness(t)

	// Send id 2 — the first post-handshake message (mirrors the liveness/modal specs).
	driveRunningTurn(t, h, 2, convID, runningTurnHold)

	// AC1/AC3: responding observed, fail-loud if never. Generous budget: cold claude
	// spawn + model load, not fakeclaude milliseconds.
	t0 := drainForResponding(t, h.phone, h.initRecv, convID, perTurnReplyBudget)

	// AC2/AC3: no terminal idle for runningTurnHold after responding first seen;
	// fail-loud on early idle. h.initRecv threads through 3->4 sequentially — the
	// receive nonce stays continuous across the two drains (this resumes exactly
	// where drainForResponding left off).
	assertNoIdleWithin(t, h.phone, h.initRecv, convID, t0, runningTurnHold)
}

// --- harness ----------------------------------------------------------------

// startStreamRunningTurnHarness stands up the real interactive stack under the
// stream-json runner and returns a handshaken interactive phone plus the seeded
// bound conversation id. It transcribes TestInteractiveStreamLiveness's setup
// body with two deliberate choices: it pairs WITHOUT --allow-remote-permissions
// (no answer path — contrast the modal harness) and spawns via
// spawnBootstrapDaemon (--dangerously-skip-permissions) so the Bash loop runs
// with no modal to block it. This is the reusable seam #1176 composes. Skips
// cleanly when claude / creds are absent.
func startStreamRunningTurnHarness(t *testing.T) (*perConvHarness, string) {
	t.Helper()
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

	// Flip the production toggle to the stream-json interactive runner BEFORE the
	// daemon spawns — resolveConfigPath reads <home>/.pyry/config.json once at
	// startup. This is the seam this family exists to exercise end-to-end.
	writeStreamInteractiveConfig(t, home)

	// Pair a device BEFORE the daemon starts. WITHOUT --allow-remote-permissions:
	// there is no answer path here (contrast the modal harness). Pairing mints the
	// bearer token + the responder static pubkey the phone pins and writes the
	// server-id + devices registry the daemon loads at startup.
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
	// the daemon starts (the registry is loaded once at startup, no reload), so the
	// trigger's send_message routes to the bootstrap supervisor instead of
	// rejecting an empty binding (#678).
	seedBootstrapRegistry(t, home, runningTurnBootstrapUUID)
	seedBoundConversation(t, home, runningTurnConvID, runningTurnBootstrapUUID, workdir)

	fr := fakerelay.New(relayTestLogger())
	t.Cleanup(func() { _ = fr.Close() })

	// spawnBootstrapDaemon passes --dangerously-skip-permissions, so the Bash loop
	// runs with no permission modal to block it (opposite of the modal specs).
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
	return &perConvHarness{phone: phone, initSend: initSend, initRecv: initRecv, home: home, workdir: workdir}, runningTurnConvID
}

// --- trigger + drains -------------------------------------------------------

// driveRunningTurn sends one send_message whose prompt forces real claude to run a
// bounded foreground Bash loop of L = hold + runningTurnLoopSlack seconds, keeping
// the turn in responding for at least hold after it first enters that state. It
// mints a per-run nonce (defeats accidental caching) and seals via sealSendMessage.
// Reusable by #1176 (which fires its interrupt right after responding).
func driveRunningTurn(t *testing.T, h *perConvHarness, reqID uint64, convID string, hold time.Duration) {
	t.Helper()
	loopSeconds := int((hold + runningTurnLoopSlack).Seconds())
	nonce := time.Now().UnixNano()
	sealSendMessage(t, h.phone, h.initSend, reqID, convID, fmt.Sprintf("m-%d", reqID),
		runningTurnPrompt(loopSeconds, nonce))
}

// runningTurnPrompt is the busy-loop prompt (the port of desktop e1fe219). It
// forces exactly one Bash-tool call running a bounded, SILENT shell loop
// (`for i in $(seq 1 <loopSeconds>); do sleep 1; done` — a loop, NOT a bare
// `sleep <L>` which claude backgrounds, and NOT a chatty per-iteration echo that
// floods the frame stream and delays turn_state), instructs claude to wait for it
// in the foreground ("do not run it in the background"), and ends with "reply with
// a single short word" (guarantees a terminal turn once the loop ends; the word is
// never asserted). run=<nonce> defeats accidental caching. No content/echo
// assertion anywhere — real claude's output is non-deterministic.
func runningTurnPrompt(loopSeconds int, nonce int64) string {
	return fmt.Sprintf("Use the Bash tool exactly once to run this command and wait for it to "+
		"finish in the foreground — do NOT run it in the background: "+
		"for i in $(seq 1 %d); do sleep 1; done. "+
		"After it completes, reply with a single short word. run=%d", loopSeconds, nonce)
}

// drainForResponding reads binary->phone noise_msg frames in receive order — the
// receive nonce is sequential, so every noise_msg MUST be decrypted in order to
// keep the CipherState in sync — and returns time.Now() the moment it observes a
// turn_state{responding} for convID. It skips thinking and every other envelope
// (decrypting each in order); a non-noise_msg control frame is skipped WITHOUT
// decrypting so it does not advance the nonce. Mirrors drainForTurnState
// (interactive_turn_state_liveness_test.go) narrowed to responding. On the
// deadline it t.Fatalf's naming the likely cause (the trigger never drove the turn
// into responding — most often a UUID mismatch between the two seeds hanging the
// drain). Reusable by #1176.
func drainForResponding(t *testing.T, phone *fakephone.Client, cs *noise.CipherState, convID string, timeout time.Duration) time.Time {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			t.Fatalf("no turn_state{responding} for %q within %s — the trigger never drove the turn into responding "+
				"(delivery never reached the child, or a UUID mismatch between seedBootstrapRegistry and "+
				"seedBoundConversation dropped every event and hung the drain)", convID, timeout)
		}
		raw, err := phone.ReceiveBytes(remaining)
		if err != nil {
			if errors.Is(err, fakephone.ErrReceiveTimeout) {
				continue // deadline reached — re-loop into the t.Fatalf above
			}
			t.Fatalf("phone receive (awaiting responding): %v", err)
		}
		var inner protocol.InnerFrameV2
		if err := json.Unmarshal(raw, &inner); err != nil {
			t.Fatalf("decode inner frame: %v", err)
		}
		if inner.Type != protocol.TypeNoiseMsg {
			continue // non-noise_msg control frame does not advance the receive nonce
		}
		cipher, err := base64.StdEncoding.DecodeString(inner.Data)
		if err != nil {
			t.Fatalf("decode inner data: %v", err)
		}
		plain, err := cs.Decrypt(cipher)
		if err != nil {
			t.Fatalf("phone decrypt (receive-nonce desync?): %v", err)
		}
		var env protocol.Envelope
		if err := json.Unmarshal(plain, &env); err != nil {
			t.Fatalf("decode envelope: %v", err)
		}
		if env.Type != protocol.TypeTurnState {
			continue // ack, thinking, assistant_delta, tool_use, … — keep draining in order
		}
		var p protocol.TurnStatePayload
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			t.Fatalf("decode turn_state payload: %v", err)
		}
		if p.ConversationID == convID && p.State == "responding" {
			t.Logf("turn_state{responding} for %q observed", convID)
			return time.Now()
		}
	}
}

// assertNoIdleWithin is the smoke-only verifier: it proves the turn stays running
// by confirming NO terminal turn_state{idle} for convID arrives within hold after
// since (the moment responding was first observed). Same in-order decrypt
// discipline as drainForResponding. It is drainForCompletedTurn's structure
// inverted — there receive-timeout = failure and idle = success; here idle =
// failure and window-elapsed = success:
//   - top: if now is at/after since+hold -> return (the window elapsed with no
//     idle, the turn is still running — success);
//   - else ReceiveBytes(remaining); a receive timeout re-loops to the success
//     check (a silent loop produces no frames, and that IS the success path);
//   - a turn_state{idle} for convID BEFORE since+hold -> t.Fatalf (the turn ended
//     early — the loop was backgrounded, refused, or too short; AC3);
//   - all other envelopes (interleaved responding, assistant_delta, tool_use) are
//     decrypted and skipped.
func assertNoIdleWithin(t *testing.T, phone *fakephone.Client, cs *noise.CipherState, convID string, since time.Time, hold time.Duration) {
	t.Helper()
	deadline := since.Add(hold)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			t.Logf("turn still running: no terminal turn_state{idle} for %q within %s of responding", convID, hold)
			return // window elapsed with no idle — the turn is still in flight (success)
		}
		raw, err := phone.ReceiveBytes(remaining)
		if err != nil {
			if errors.Is(err, fakephone.ErrReceiveTimeout) {
				continue // a silent loop produces no frames — re-loop to the success check
			}
			t.Fatalf("phone receive (asserting no idle): %v", err)
		}
		var inner protocol.InnerFrameV2
		if err := json.Unmarshal(raw, &inner); err != nil {
			t.Fatalf("decode inner frame: %v", err)
		}
		if inner.Type != protocol.TypeNoiseMsg {
			continue // non-noise_msg control frame does not advance the receive nonce
		}
		cipher, err := base64.StdEncoding.DecodeString(inner.Data)
		if err != nil {
			t.Fatalf("decode inner data: %v", err)
		}
		plain, err := cs.Decrypt(cipher)
		if err != nil {
			t.Fatalf("phone decrypt (receive-nonce desync?): %v", err)
		}
		var env protocol.Envelope
		if err := json.Unmarshal(plain, &env); err != nil {
			t.Fatalf("decode envelope: %v", err)
		}
		if env.Type != protocol.TypeTurnState {
			continue // assistant_delta, tool_use, ack, … — decrypt and skip
		}
		var p protocol.TurnStatePayload
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			t.Fatalf("decode turn_state payload: %v", err)
		}
		if p.State == "idle" && p.ConversationID == convID {
			t.Fatalf("terminal turn_state{idle} for %q arrived %s after responding, before the %s window elapsed — "+
				"the turn ended early (the Bash loop was backgrounded, refused, or too short)",
				convID, time.Since(since), hold)
		}
		// an interleaved responding for convID is fine — keep draining.
	}
}
