//go:build e2e_realclaude

package realclaude

// TestInteractiveStreamLiveness is the #1153 deliverable: the real-claude
// counterpart of the fake-side stream liveness proof
// (TestRelayV2_StreamSendMessageDrainsTurn, #1141). It stands up the interactive
// daemon under the production stream-json interactive runner
// (interactive_runner: "stream-json") against a live claude and asserts one real
// turn streams end-to-end — a non-empty assistant_delta FOLLOWED BY the terminal
// turn_state{idle}, never a canned or fake response.
//
// Per the always-a-real-claude-gate policy (2026-07-08) every operator-facing
// happy-path flow needs a real-claude e2e that actually RUNS in the pre-ship gate
// before the Mac daemon binary is swapped — the operator must never be the first
// real-stack execution. The stream-json interactive runner (#1081) is green
// against fakeclaude (#1141 liveness + the #1136/#1137/#1138/#1139 riders) but had
// never run the interactive relay path against REAL claude. This adds that rung.
//
// The daemon body is TRANSCRIBED from #854's TestInteractiveBootstrapLiveness
// (interactive_bootstrap_liveness_test.go) with three deltas: (1) the stream-json
// toggle is flipped via writeStreamInteractiveConfig before spawn, (2) it drives
// ONE turn (AC #2, #1141 parity) instead of two, and (3) the drain continues past
// the first delta to the terminal turn_state{idle} (drainForCompletedTurn) rather
// than stopping at the first delta (#854's drainForAssistantReply). Every Noise-
// wire / spawn / seed helper is reused verbatim from #854's file (same package,
// same build tag).
//
// The two seeds still gate the drain, exactly as they do on the fake side (#1141):
// the stream runner tags turnevents by its construction-time bootstrap pool id
// (pinned to streamBootstrapUUID by seedBootstrapRegistry); the drain gate forwards
// to the emitter only when the tag == activeSession(), which resolves streamConvID's
// binding = streamBootstrapUUID via seedBoundConversation. A UUID mismatch drops
// every event and hangs the drain, surfacing as the M1 timeout — never a silent
// pass. Unlike the PTY path (#854) the stream runner reads claude's stdout directly,
// so there is no transcript-resolution deadlock here; only the gate matters.
//
// The config-writer (writeStreamInteractiveConfig) is a standalone helper — not a
// spawn wrapper — so the permission-flow rider #1154 can compose it with
// spawnPermissionDaemon (no --dangerously-skip-permissions) instead of
// spawnBootstrapDaemon.

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
	"github.com/pyrycode/pyrycode/internal/noise"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// Fixed identifiers for the seeded state, distinct from #854's liveBootstrapUUID /
// liveConvID (same package — the two files must not redeclare). streamBootstrapUUID
// is the bootstrap session's POOL id (pinned via seedBootstrapRegistry); streamConvID
// is the driving conversation, bound to it via seedBoundConversation. The two tests
// never share on-disk state (each gets its own authenticated tempdir HOME), but the
// distinct literals keep any cross-test confusion impossible.
const (
	streamBootstrapUUID = "88888888-8888-4888-8888-888888888888"
	streamConvID        = "66666666-6666-4666-8666-666666666666"
)

func TestInteractiveStreamLiveness(t *testing.T) {
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
	// startup. This is the seam this test exists to prove end-to-end.
	writeStreamInteractiveConfig(t, home)

	// Pair a device BEFORE the daemon starts (mints the bearer token + the
	// responder static pubkey the phone pins; writes server-id + devices registry
	// the daemon loads at startup).
	exit, stdout, stderr := runPyry(t, "pair", "-pyry-name=test", "--name=phone-a")
	if exit != 0 {
		t.Fatalf("pyry pair exit=%d\nstdout:\n%s\nstderr:\n%s", exit, stdout, stderr)
	}
	payload := decodePairPayload(t, stdout)
	pubKey, err := base64.StdEncoding.DecodeString(payload.ServerStaticPubkey)
	if err != nil {
		t.Fatalf("decode server static pubkey: %v", err)
	}

	// Seed the deterministic bootstrap id + the driving conversation binding
	// BEFORE the daemon starts (the registry is loaded once at startup, no reload).
	seedBootstrapRegistry(t, home, streamBootstrapUUID)
	seedBoundConversation(t, home, streamConvID, streamBootstrapUUID, workdir)

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

	// Drive ONE real turn (AC #2; the fake counterpart #1141 drives one). The
	// deliberately long response crosses multiple 250 ms emitter coalescing
	// windows, while the per-run nonce defeats accidental caching without making
	// the assertion depend on content. Generous budget: real claude on a cold
	// stream session (spawn + model load + first-turn reply), not fakeclaude
	// milliseconds.
	nonce := time.Now().UnixNano()
	sealSendMessage(t, phone, initSend, 2, streamConvID, "m-1",
		fmt.Sprintf("Without using tools, write exactly ten numbered paragraphs about reliable process supervision. "+
			"Each paragraph must contain at least two complete sentences. run=%d", nonce))
	drainForCompletedTurnWithMinimumDeltas(t, phone, initRecv, streamConvID, 120*time.Second, 2)
}

// --- stream-json toggle + turn drain ----------------------------------------

// writeStreamInteractiveConfig opts the daemon into the stream-json interactive
// runner via the production config toggle. resolveConfigPath reads
// <home>/.pyry/config.json once at startup (per-user, NOT per-instance), so this
// must land BEFORE the daemon spawns. A raw JSON literal (no internal/config
// import) keeps the realclaude package import-lean, mirroring seedBootstrapRegistry;
// a partial config leaves every other field at its default. Transcribed from the
// fake harness StartStreamInteractiveWithRelay (internal/e2e/harness.go). This is
// the reusable seam the permission-flow rider #1154 composes with
// spawnPermissionDaemon.
func writeStreamInteractiveConfig(t *testing.T, home string) {
	t.Helper()
	pyryDir := filepath.Join(home, ".pyry")
	if err := os.MkdirAll(pyryDir, 0o700); err != nil {
		t.Fatalf("realclaude: mkdir .pyry: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pyryDir, "config.json"),
		[]byte(`{"interactive_runner":"stream-json"}`), 0o600); err != nil {
		t.Fatalf("realclaude: write config.json: %v", err)
	}
}

// warnRateLimitStatus is the ONE non-benign rate_limit_info.status this drain
// TOLERATES: claude reporting that the account sits inside its usage-limit
// warning band while still ALLOWING the turn. The daemon is right to emit the
// frame for it — warning before the wall is the whole point of #1404's mapping,
// and folding this value into streamsup.benignRateLimitStatus would leave the
// frame firing only once the user is already blocked, which is too late to act
// on. What was wrong was this drain's assumption that a healthy turn is a SILENT
// one.
//
// MEASURED, not chosen: status "allowed_warning" against limit_type "seven_day",
// resets_at 1787551200, observed on claude 2.1.239 on 2026-08-22 in all five
// tests that share this drain. Every capture on record predates it and reports
// "allowed" against "five_hour", a window that was nowhere near its ceiling. So
// this is a NEW SIBLING value rather than a rename of the benign one, and
// streamsup.benignRateLimitStatus stays exactly as it is.
//
// Matched byte-exact — no fold, no trim, and deliberately NOT a prefix match on
// "allowed". streamsup.benignRateLimitStatus's doc rejects a prefix for the
// reason that applies here verbatim: it would swallow whatever claude names the
// state after this one, and swallowing it HERE is silent, in the one tier that
// reads what claude sends today.
const warnRateLimitStatus = "allowed_warning"

// drainForCompletedTurn reads binary→phone noise_msg frames in receive order —
// the receive nonce is sequential, so every frame MUST be decrypted in order to
// keep the CipherState in sync — and returns once it observes a full turn: a
// non-empty assistant_delta for convID (M1) FOLLOWED BY the terminal
// turn_state{idle} for convID (M2). This is the stronger drain AC #2 requires;
// #854's drainForAssistantReply stops at M1.
//
// It also asserts a THIRD thing, a negative: that the turn produces no
// unrecognized_message frame. See that arm for why it is the most valuable
// assertion in this file.
//
// And a FOURTH, the same shape (#1411): that the turn produces no rate_limited
// frame. claude emits its rate_limit_event line once per run whatever the state of
// the usage-limit window, so a healthy run's silence rests on the parser's gate
// matching claude's benign status value — another measurement, with its own day to
// go stale. See that arm for what it tolerates and the three readings a red run
// carries.
//
// It mirrors the fake-side two-milestone drain (#1141,
// `TestRelayV2_StreamSendMessageDrainsTurn`) with the real-claude adaptation: NO
// content/echo assertion — real claude's words are non-deterministic, so M1
// asserts only non-empty text. A leading turn_state{responding} precedes the
// delta; turn_states are ignored until sawDelta. On the deadline, a milestone-
// specific t.Fatalf names the likely cause (a UUID mismatch between the two seeds
// drops every event and hangs the drain).
//
// IT RETURNS THE DRIVING CONVERSATION'S REPLY TEXT, concatenated across deltas,
// for callers that need one (#2087). The drain itself still asserts nothing about
// content — the paragraph above holds, and adding a content assertion here would
// impose it on all nine callers. What a returned string buys is a caller-local
// assertion for the case where the turn's SHAPE is not enough: #2087 needs to
// know that claude actually loaded a skill, and only a token that exists solely
// inside the skill body can say so. Returning a value rather than taking a sink
// keeps every existing call site compiling unchanged, since a Go call used as a
// statement may discard results.
func drainForCompletedTurn(t *testing.T, phone *fakephone.Client, cs *noise.CipherState, convID string, timeout time.Duration) string {
	t.Helper()
	return drainForCompletedTurnWithMinimumDeltas(t, phone, cs, convID, timeout, 1)
}

// drainForCompletedTurnWithMinimumDeltas applies drainForCompletedTurn's full
// frame inventory and terminal-idle contract, but does not accept idle until at
// least minDeltas non-empty frames for the driving conversation have arrived.
func drainForCompletedTurnWithMinimumDeltas(t *testing.T, phone *fakephone.Client, cs *noise.CipherState, convID string, timeout time.Duration, minDeltas int) string {
	t.Helper()
	nonEmptyDeltas := 0
	var reply strings.Builder
	deadline := time.Now().Add(timeout)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			if nonEmptyDeltas == 0 {
				t.Fatalf("M1: never observed a non-empty assistant_delta for %q within %s — the turn never drained "+
					"end-to-end (delivery never reached the child, or the parser / drain gate / emitter dropped it — "+
					"most likely a UUID mismatch between seedBootstrapRegistry and seedBoundConversation)", convID, timeout)
			}
			if nonEmptyDeltas < minDeltas {
				t.Fatalf("M1: observed %d non-empty assistant_delta frame(s) for %q within %s, want at least %d — "+
					"the reply did not stream across multiple emitter coalescing windows",
					nonEmptyDeltas, convID, timeout, minDeltas)
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
		case protocol.TypeUnrecognizedMessage:
			// THE REGRESSION ALARM on the parser's known-ignored list.
			//
			// A normal turn against live claude must produce ZERO of these. The
			// parser splits claude's output two ways: types we knowingly ignore
			// stay silent, anything else surfaces as this frame. That split rests
			// on a MEASUREMENT (streamsup/parser.go ignoredLineTypes, taken
			// 2026-07-27), and a measurement goes stale the day claude ships a new
			// message type or content block.
			//
			// So this assertion is the alarm the whole unrecognized-message feature
			// exists to provide, and it fires HERE — in the pre-ship gate, before a
			// binary swap — rather than in front of a user afterwards. It sits in
			// the shared drain deliberately: every stream spec that drives a real
			// turn becomes a sentinel for free.
			//
			// Going red does NOT necessarily mean something is broken. It means
			// claude's output grew a shape we do not map. Read the payload, decide
			// whether it deserves a mapping or an entry on the known-ignored list,
			// and re-run the slice 0 measurement.
			var p protocol.UnrecognizedMessagePayload
			if err := json.Unmarshal(env.Payload, &p); err != nil {
				t.Fatalf("decode unrecognized_message payload: %v", err)
			}
			t.Fatalf("a NORMAL turn produced an unrecognized_message: site=%q type=%q truncated=%v\n"+
				"raw: %s\n\n"+
				"claude emitted output the stream parser has no mapping for. Either it needs a "+
				"mapping, or it belongs on streamsup.ignoredLineTypes — re-run the line-inventory "+
				"measurement before deciding.",
				p.Site, p.MessageType, p.Truncated, p.Raw)
		case protocol.TypeRateLimited:
			// THE STANDING ALARM on the parser's benign-status measurement (#1411).
			//
			// claude emits its rate_limit_event line ONCE PER RUN whatever the state of
			// the usage-limit window — every capture on record reported "allowed", i.e.
			// no limit in force, and the line fired anyway. So the parser gates it:
			// the one measured-benign status is silent, any other non-empty status
			// emits. A normal turn against live claude must therefore produce zero
			// frames THIS DRAIN DOES NOT RECOGNISE, and this is the only tier that can
			// say so about what claude sends TODAY. Its hermetic sibling
			// (internal/e2e/relay_v2_stream_rate_limit_test.go) feeds the captured bytes
			// and would stay green through a change in claude's status vocabulary; this
			// one would not.
			//
			// No conversation filter, matching the arm above: a usage limit is a
			// condition of the ACCOUNT, so a frame bound to any conversation is the
			// alarm.
			//
			// warnRateLimitStatus is LOGGED AND TOLERATED rather than fatal — see its
			// doc for the measurement and for why the daemon emitting it is correct.
			// Anything else is fatal, and going red has THREE readings, with status the
			// discriminator:
			//
			//  1. A USAGE LIMIT REALLY IS IN FORCE on this account right now. Not a
			//     daemon bug — this is the frame working. The window resets; re-run then.
			//
			//  2. claude renamed or recapitalised the benign status, so
			//     streamsup.benignRateLimitStatus ("allowed") has gone stale and every
			//     healthy run now emits. One constant edit fixes it — confirm against a
			//     fresh drop-census capture before making it.
			//
			//  3. claude grew ANOTHER allowed-but-notable status beside the two already
			//     measured, the way "allowed_warning" appeared beside "allowed" on
			//     2026-08-22. The daemon is behaving correctly and it is this drain that
			//     needs to learn the value — capture it before adding it, exactly as
			//     reading 2 requires.
			var p protocol.RateLimitedPayload
			if err := json.Unmarshal(env.Payload, &p); err != nil {
				t.Fatalf("decode rate_limited payload: %v", err)
			}
			if p.Status == warnRateLimitStatus {
				t.Logf("rate_limited frame TOLERATED (not a failure): status=%q limit_type=%q "+
					"resets_at=%d conversation_id=%q — the account is inside its usage-limit "+
					"warning band and the turn was still allowed, so the daemon is reporting "+
					"this correctly. Expect it on every run until the window resets.",
					p.Status, p.LimitType, p.ResetsAt, p.ConversationID)
				continue
			}
			t.Fatalf("a NORMAL turn produced an UNRECOGNISED rate_limited frame: status=%q "+
				"limit_type=%q resets_at=%d truncated_fields=%v conversation_id=%q\n\n"+
				"Either a usage limit is genuinely in force on this account (re-run after the "+
				"window resets), or claude changed the benign status value and "+
				"streamsup.benignRateLimitStatus is stale, or claude grew another "+
				"allowed-but-notable status this drain has not measured — status above is what "+
				"tells those apart.",
				p.Status, p.LimitType, p.ResetsAt, p.TruncatedFields, p.ConversationID)
		case protocol.TypeAssistantDelta:
			var p protocol.AssistantDeltaPayload
			if err := json.Unmarshal(env.Payload, &p); err != nil {
				t.Fatalf("decode assistant_delta payload: %v", err)
			}
			if p.ConversationID != convID {
				continue
			}
			// Every delta is accumulated: a reply arrives in pieces and a caller's
			// token can land in any of them. The requested minimum decides when M1
			// is satisfied; the default drain keeps its one-delta contract.
			reply.WriteString(p.Text)
			// M1: liveness — non-empty streamed deltas for the driving conv. No
			// content/echo assertion (real claude's words are non-deterministic).
			if strings.TrimSpace(p.Text) != "" {
				nonEmptyDeltas++
				t.Logf("M1: non-empty assistant_delta %d/%d (seq=%d, %d bytes) for %q",
					nonEmptyDeltas, minDeltas, p.Seq, len(p.Text), convID)
			}
		case protocol.TypeTurnState:
			var st protocol.TurnStatePayload
			if err := json.Unmarshal(env.Payload, &st); err != nil {
				t.Fatalf("decode turn_state payload: %v", err)
			}
			if st.State != "idle" || st.ConversationID != convID {
				continue // the leading responding state precedes the delta
			}
			if nonEmptyDeltas < minDeltas {
				t.Fatalf("M1: terminal turn_state{idle} arrived after %d non-empty assistant_delta frame(s) "+
					"for %q, want at least %d", nonEmptyDeltas, convID, minDeltas)
			}
			// M2: after the required deltas, the terminal idle state closes the turn.
			t.Logf("M2: terminal turn_state{idle} for %q — the turn closed", convID)
			return reply.String()
		}
	}
}
