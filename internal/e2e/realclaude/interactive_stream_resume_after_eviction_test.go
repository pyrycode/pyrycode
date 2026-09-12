//go:build e2e_realclaude

package realclaude

// TestInteractiveStreamResumeAfterEviction is the #1177 deliverable (split from
// #1083, T9): the real-claude proof that an idle-evicted interactive STREAM
// session resumes via --resume with prior context intact. It stands up the
// interactive daemon under the production stream-json interactive runner
// (interactive_runner: "stream-json") against a live claude, drives ONE turn to
// completion that plants a per-run-unique token, forces idle eviction of that
// stream session, and then drives a second turn whose reply must recall the
// planted token — the observable that distinguishes a true --resume from a forked
// fresh spawn.
//
// Why this rung exists: the streamrunner plan flagged "restart after eviction" as
// a risk. On idle eviction the streamsup.Runner respawns through buildArgs, whose
// post-eviction spawn resumes an EXISTING transcript under the same id — since
// #1631 directly, because useCreateForm probes the sessions directory by id,
// finds the real transcript claude wrote, and emits --resume on the first spawn.
// (Before that the first spawn was --session-id, claude refused it, the child
// exited non-zero, firstRun flipped false and the backed-off respawn resumed;
// same destination, one wasted spawn plus a backoff slower.) Whether a real
// claude, evicted mid-conversation and put through that one crash-recovery cycle,
// actually reattaches to the same transcript and retains context is unproven
// end-to-end. Idle-evict + respawn is covered only against fakeclaude and only on
// the PTY/bootstrap runner (TestE2E_IdleEviction_RespawnsOnSendMessage, #396); the
// stream path's #680 (TestE2E_PerConversation_IdleEvictsAndReactivates) proves
// lifecycle/routing but explicitly defers content-recall to "realclaude's domain."
// This spec closes that gap for the stream toggle.
//
// TWO source-verified mechanics drive the whole design:
//
//   (1) The idle timer arms at session ACTIVATION and never resets per-turn
//       (session.go runActive: time.NewTimer(s.idleTimeout) at activation; the only
//       re-arm is attached>0, and relay/phone conns are not bridge-attached so
//       attached stays 0). Turn delivery runs on a different goroutine and never
//       touches the timer. Consequence: a short idle-timeout fires MID-plant-turn,
//       killing claude before the token commits — so the plant turn MUST reach
//       turn_state{idle} before the timer fires. This is why every existing stream
//       realclaude test DISABLES idle (-pyry-idle-timeout=0); #1177 is the first to
//       enable it, hence the coupling constraint in § Timing below.
//
//   (2) Re-activation re-arms firstRun (runner.go Run: firstRun := true per Run()),
//       but since #1631 that latch no longer decides: with a sessions directory
//       supplied, useCreateForm probes by id and the confirmed on-disk transcript
//       makes the re-activation spawn --resume outright. This is the exact
//       "restart after eviction" path this test verifies live — now without the
//       refusal-and-backoff detour the latch used to take, which leaves the
//       recovery budget below slack it did not previously have. The budget stays
//       as it is: it is sized for a real claude cold spawn plus a reply, and the
//       removed detour was never its binding term.
//
// The setup body is TRANSCRIBED from TestInteractiveStreamLiveness (#1153) — the
// stream-json toggle, isolated workdir, pair (no --allow-remote-permissions), the
// two seeds, handshake — with the single spawn delta that this test enables the
// idle timer (spawnBootstrapDaemonWithIdle, below). The plant turn reuses
// drainForCompletedTurn (#1153) verbatim; the recall turn uses a fork
// (drainForResumedTurnText) that accumulates the delta text so the planted token
// can be matched against it. The continuity discriminator is the #1173 technique:
// a per-run-unique planted token recalled by a later turn, case-fold Contains
// against real-claude non-determinism — never an exact-content assertion.
//
// Like the sibling stream specs this is a standing real-claude liveness gate in
// preship, not a deterministic RED/GREEN oracle; placement under the e2e_realclaude
// build tag wires it into `make e2e-realclaude` via the package glob (no Makefile
// change). It SKIPs cleanly (exit 0) when claude / credentials are absent; the
// ticket carries needs-real-claude and is operator-gated.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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

// Fixed identifiers for the seeded state. Single-char-repeat UUID stems are
// exhausted in this package (1111–bbbb merged; cccc/dddd reserved by #1173,
// eeee/ffff by #1174), so these are ticket-encoded — collision-free by
// construction and valid UUIDv4 (version nibble 4, variant nibble 8).
// evictResumeBootstrapUUID is the bootstrap session's POOL id (pinned via
// seedBootstrapRegistry); evictResumeConvID is the driving conversation, bound to
// it via seedBoundConversation. Each test owns its own authenticated tempdir HOME,
// so literals never collide on disk; distinct values only keep cross-test
// confusion impossible.
const (
	evictResumeBootstrapUUID = "11770000-0000-4000-8000-000000000001"
	evictResumeConvID        = "11770000-0000-4000-8000-000000000002"
)

// resumeAfterEvictionIdle is the -pyry-idle-timeout window D. The idle timer arms
// at daemon start and fires at D; the plant turn is sent right after the handshake
// and MUST reach turn_state{idle} before D (see mechanic (1) above). A cold haiku
// plant turn (--session-id cold spawn + model load + a one-word reply) is typically
// ~5–15s, so D=30s gives ~2× margin.
//
// Coupling constraint (analogous to the running-turn spec's L < 120s note): D must
// exceed the wall-clock from daemon start to plant-turn completion. If the plant
// turn REDs at its drain (drainForCompletedTurn never sees idle), the idle timer
// evicted mid-turn — raise D. This is a standing preship liveness gate, not a
// deterministic RED/GREEN oracle; it fails loud, never false-green.
const resumeAfterEvictionIdle = "30s"

// resumeAfterEvictionWARNTimeout bounds the poll for the session.idle_eviction
// WARN. The poll begins after the plant drain returns; the WARN fires ~D after
// daemon start, so this must be ≥ D + eviction-processing slack to always cover
// the remaining window.
const resumeAfterEvictionWARNTimeout = 40 * time.Second

func TestInteractiveStreamResumeAfterEviction(t *testing.T) {
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
	// startup. This is the seam this family exercises end-to-end.
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

	// Seed the deterministic bootstrap id + the driving conversation binding BEFORE
	// the daemon starts (the registry is loaded once at startup, no reload).
	seedBootstrapRegistry(t, home, evictResumeBootstrapUUID)
	seedBoundConversation(t, home, evictResumeConvID, evictResumeBootstrapUUID, workdir)

	// Spawn with the idle timer ENABLED (D). Keep the *bootstrapDaemon handle so the
	// test can read the daemon's stderr for the session.idle_eviction WARN.
	d := spawnBootstrapDaemonWithIdle(t, home, workdir, claudeBin, relayURL, resumeAfterEvictionIdle)
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

	// Per-run-unique, unbroken, upper-case-friendly recall token. Per-run
	// uniqueness defeats accidental caching / coincidental matches; an unbroken
	// token survives strings.Contains even if claude wraps it in punctuation.
	token := fmt.Sprintf("PYRYRESUME%X", time.Now().UnixNano())

	// Plant turn (AC1, AC4-setup): instruct claude to remember the exact token and
	// reply with just "ok" — the token is never asserted here; it only has to reach
	// the transcript (the user message carries it). drainForCompletedTurn blocks
	// until turn_state{idle}, the synchronization point that guarantees the token is
	// committed BEFORE eviction. If the idle timer evicts mid-turn this drain never
	// sees idle and REDs at perTurnReplyBudget — loud, never a false green.
	sealSendMessage(t, phone, initSend, 2, evictResumeConvID, "m-1",
		fmt.Sprintf("Remember this exact token for later, but do NOT repeat it now: %s. "+
			"Reply with just the word: ok", token))
	drainForCompletedTurn(t, phone, initRecv, evictResumeConvID, perTurnReplyBudget)

	// Force + observe eviction (AC1, AC2): after the plant drain returns, poll the
	// daemon stderr for the session.idle_eviction WARN. This is the deterministic
	// gate proving the bootstrap stream session was evicted BEFORE the resume turn —
	// without it the continuity assertion is vacuous (a never-killed child trivially
	// retains context).
	waitForIdleEvictionWARN(t, d, evictResumeBootstrapUUID, resumeAfterEvictionWARNTimeout)

	// Resume turn (AC3, AC4): ask claude for the exact token it was told to
	// remember. drainForResumedTurnText returns the concatenated assistant_delta
	// text after M1(non-empty)→M2(idle). The budget must absorb the re-activation
	// recovery — since #1631 a --resume cold spawn plus the reply, the by-id probe
	// having skipped the --session-id refusal and its backoff; perTurnReplyBudget
	// =120s covered even the longer pre-#1631 shape and so covers this one with
	// room to spare. A RED here is the honest surface for the restart-after-
	// eviction risk — if re-activation does not reattach to the transcript, the
	// resume turn never drains.
	sealSendMessage(t, phone, initSend, 3, evictResumeConvID, "m-2",
		"What was the exact token I asked you to remember earlier? "+
			"Reply with only that token, nothing else.")
	recallText := drainForResumedTurnText(t, phone, initRecv, evictResumeConvID, perTurnReplyBudget)

	// Assert continuity (AC4): a true --resume reloads the transcript and recalls
	// the token; a forked fresh spawn would not → RED. Case-fold Contains tolerates
	// claude's non-deterministic formatting without weakening the discriminator.
	// (M1 in the drain already enforced AC3's non-empty delta; Contains("", token)
	// is false, so an empty reply also REDs.)
	if !strings.Contains(strings.ToUpper(recallText), strings.ToUpper(token)) {
		t.Fatalf("resume-after-eviction: post-eviction reply did not recall the planted token %q — "+
			"the session did not --resume the pre-eviction transcript (a forked fresh spawn has no memory of it).\n"+
			"captured assistant_delta text: %q", token, recallText)
	}
}

// --- helpers (local to this file; distinct names to avoid same-package collision) ---

// spawnBootstrapDaemonWithIdle is spawnBootstrapDaemon
// (interactive_bootstrap_liveness_test.go) with its sole delta being
// -pyry-idle-timeout=<idleTimeout> instead of the hardcoded =0, so this test can
// exercise idle eviction. It reuses the bootstrapDaemon type, shortSocketPath,
// ensurePyryBuilt, lockedBuffer, and waitForReady (all same-package). A signature
// change to the shared spawnBootstrapDaemon would fan out to #1153/#1172 and edit a
// file other in-flight siblings compose from; a self-contained variant keeps this
// ticket's worktree to one new file with zero shared-file merge surface — the
// discipline the whole realclaude family already follows.
func spawnBootstrapDaemonWithIdle(t *testing.T, home, workdir, claudeBin, relayURL, idleTimeout string) *bootstrapDaemon {
	t.Helper()
	bin := ensurePyryBuilt(t) // builds with real HOME (warm cache); runs with isolated HOME
	socket := shortSocketPath(t)
	stderr := &lockedBuffer{}

	args := []string{
		"-pyry-socket=" + socket,
		"-pyry-name=test",
		"-pyry-claude=" + claudeBin,
		"-pyry-idle-timeout=" + idleTimeout,
		"-pyry-workdir=" + workdir,
		"-pyry-relay=" + relayURL,
		"--",
		"--model", "haiku",
		"--dangerously-skip-permissions",
	}
	cmd := exec.Command(bin, args...)
	// os.Environ() already carries the isolated HOME and the credential
	// (WithWorktreeAuthenticated t.Setenv's both). Add the relay switches.
	cmd.Env = append(os.Environ(), "PYRY_ALLOW_INSECURE_RELAY=1", "PYRY_MOBILE_V2=1")
	cmd.Stderr = io.MultiWriter(os.Stderr, stderr) // DEBUG tee + eviction-WARN source

	if err := cmd.Start(); err != nil {
		t.Fatalf("realclaude: pyry start: %v", err)
	}
	doneCh := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(doneCh)
	}()

	d := &bootstrapDaemon{socketPath: socket, cmd: cmd, doneCh: doneCh, stderr: stderr}
	if err := d.waitForReady(10 * time.Second); err != nil {
		t.Fatalf("realclaude: daemon not ready: %v\nstderr:\n%s", err, stderr.String())
	}
	return d
}

// drainForResumedTurnText is a fork of drainForCompletedTurn
// (interactive_stream_liveness_test.go): same M1(non-empty assistant_delta)→
// M2(terminal turn_state{idle}) milestones and the same in-order noise-decrypt
// discipline (the receive nonce is sequential, so every noise_msg MUST be
// decrypted in order; a non-noise_msg control frame is skipped without decrypting).
// The one behavioural delta: it ACCUMULATES every matching delta's Text and RETURNS
// the concatenation at M2, so the caller can match the planted token against the
// full reply. Distinct name from #1173's drainForCompletedTurnText (not on main; a
// shared name would redeclare once #1173 lands).
func drainForResumedTurnText(t *testing.T, phone *fakephone.Client, cs *noise.CipherState, convID string, timeout time.Duration) string {
	t.Helper()
	sawDelta := false
	var sb strings.Builder
	deadline := time.Now().Add(timeout)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			if !sawDelta {
				t.Fatalf("M1: never observed a non-empty assistant_delta for %q within %s — the resume turn never "+
					"drained end-to-end (the re-activation did not recover --session-id→--resume, or delivery never "+
					"reached the respawned child)", convID, timeout)
			}
			t.Fatalf("M2: observed the assistant_delta for %q but never a terminal turn_state{idle} within %s — "+
				"the resume turn opened but never closed", convID, timeout)
		}
		raw, err := phone.ReceiveBytes(remaining)
		if err != nil {
			if errors.Is(err, fakephone.ErrReceiveTimeout) {
				continue // deadline reached — re-loop into the milestone-specific t.Fatalf above
			}
			t.Fatalf("phone receive (resume drain): %v", err)
		}
		var inner protocol.InnerFrameV2
		if err := json.Unmarshal(raw, &inner); err != nil {
			t.Fatalf("decode inner frame (resume drain): %v", err)
		}
		if inner.Type != protocol.TypeNoiseMsg {
			// A non-noise_msg control frame (e.g. rekey) does not advance the
			// receive nonce — skip without decrypting.
			continue
		}
		cipher, err := base64.StdEncoding.DecodeString(inner.Data)
		if err != nil {
			t.Fatalf("decode inner data (resume drain): %v", err)
		}
		plain, err := cs.Decrypt(cipher)
		if err != nil {
			t.Fatalf("phone decrypt (receive-nonce desync?): %v", err)
		}
		var env protocol.Envelope
		if err := json.Unmarshal(plain, &env); err != nil {
			t.Fatalf("decode envelope (resume drain): %v", err)
		}
		switch env.Type {
		case protocol.TypeAssistantDelta:
			var p protocol.AssistantDeltaPayload
			if err := json.Unmarshal(env.Payload, &p); err != nil {
				t.Fatalf("decode assistant_delta payload: %v", err)
			}
			if p.ConversationID != convID {
				continue
			}
			// Accumulate every chunk for the driving conv (the token may span deltas
			// or be wrapped in punctuation). M1: a non-empty streamed delta marks the
			// turn live — no content/echo assertion here (real claude is
			// non-deterministic); the planted-token match is the caller's job.
			sb.WriteString(p.Text)
			if !sawDelta && strings.TrimSpace(p.Text) != "" {
				sawDelta = true
				t.Logf("M1: non-empty assistant_delta (seq=%d, %d bytes) for %q", p.Seq, len(p.Text), convID)
			}
		case protocol.TypeTurnState:
			if !sawDelta {
				continue // the leading responding state precedes the delta
			}
			var st protocol.TurnStatePayload
			if err := json.Unmarshal(env.Payload, &st); err != nil {
				t.Fatalf("decode turn_state payload: %v", err)
			}
			// M2: after the delta, the terminal idle state closes the turn — return
			// the accumulated reply text for the token match.
			if st.State == "idle" && st.ConversationID == convID {
				t.Logf("M2: terminal turn_state{idle} for %q — the resume turn closed (%d bytes captured)", convID, sb.Len())
				return sb.String()
			}
		}
	}
}

// waitForIdleEvictionWARN polls the daemon's captured stderr until it carries the
// session.idle_eviction WARN for bootstrapUUID: all of event=session.idle_eviction,
// session_id=<bootstrapUUID>, and bootstrap=true (pin the keys/id, tolerate slog
// field reordering). On the stream path the bootstrap pool id is warm-started to
// bootstrapUUID and stays stable across --resume (buildArgs never forks), so
// s.currentID() — the WARN's session_id — is exactly bootstrapUUID; pinning the
// value proves it was OUR session that evicted. Mirrors the #396 reference's
// substring poll; containsAll lives in package e2e (disjoint build tag), so the
// all-substrings check is inlined here.
func waitForIdleEvictionWARN(t *testing.T, d *bootstrapDaemon, bootstrapUUID string, timeout time.Duration) {
	t.Helper()
	want := []string{
		"event=session.idle_eviction",
		"session_id=" + bootstrapUUID,
		"bootstrap=true",
	}
	deadline := time.Now().Add(timeout)
	for {
		stderr := d.stderr.String()
		if containsAllSubstrings(stderr, want) {
			t.Logf("observed session.idle_eviction WARN for %q", bootstrapUUID)
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("session.idle_eviction WARN for %q missing one or more of %v within %s — the stream session "+
				"was never idle-evicted, so the resume turn would not land on a respawned child (continuity would be "+
				"vacuous).\nstderr:\n%s", bootstrapUUID, want, timeout, stderr)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// containsAllSubstrings reports whether s contains every substring in want.
// Order-independent so the slog text handler can reorder keys without breaking the
// assertion. Package-local twin of internal/e2e's containsAll (that file's e2e
// build tag is disjoint from e2e_realclaude, so it cannot be imported).
func containsAllSubstrings(s string, want []string) bool {
	for _, w := range want {
		if !strings.Contains(s, w) {
			return false
		}
	}
	return true
}
