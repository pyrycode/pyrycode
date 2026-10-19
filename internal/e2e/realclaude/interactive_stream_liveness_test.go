//go:build e2e_realclaude

package realclaude

// TestInteractiveStreamLiveness drives one real Claude turn through the daemon's
// stream-json interactive runner and encrypted relay path. It requires nonempty
// attributed text followed by terminal idle, without constraining model wording
// or chunk counts. The seeded session and conversation binding must agree so the
// stream drain can attribute output to the driving conversation.

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
	runParallel(t)
	// Parallel only through runParallel; see liveHome.
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skipf("realclaude: claude not on PATH: %v", err)
	}
	home := liveHome(t) // skips cleanly when no creds
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
	seedBootstrapRegistry(t, home, streamBootstrapUUID)
	seedBoundConversation(t, home, streamConvID, streamBootstrapUUID, workdir)

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

	// A cold live session must deliver nonempty text and close within the budget.
	// Chunk counts and generated wording are independent of basic liveness.
	nonce := time.Now().UnixNano()
	sealSendMessage(t, phone, initSend, 2, streamConvID, "m-1",
		fmt.Sprintf("Without using tools, briefly describe reliable process supervision. run=%d", nonce))
	drainForCompletedTurn(t, phone, initRecv, streamConvID, 120*time.Second)
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

// writeStdioPermissionPromptConfig selects the stream runner and the opt-in
// stdio permission transport in the one file the daemon reads at startup.
func writeStdioPermissionPromptConfig(t *testing.T, home string) {
	t.Helper()
	pyryDir := filepath.Join(home, ".pyry")
	if err := os.MkdirAll(pyryDir, 0o700); err != nil {
		t.Fatalf("realclaude: mkdir .pyry: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pyryDir, "config.json"),
		[]byte(`{"interactive_runner":"stream-json","stdio_permission_prompt":true}`), 0o600); err != nil {
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

// drainForCompletedTurn decrypts every noise_msg in receive order to preserve the
// receive nonce. It returns the driving conversation's concatenated reply after
// a non-whitespace delta (M1) followed by matching terminal idle (M2). Idle before
// M1 cannot close this turn. Unknown messages and unrecognized rate-limit statuses
// are global alarms, including frames attributed to other conversations.
func drainForCompletedTurn(t *testing.T, phone completedTurnReceiver, cs *noise.CipherState, convID string, timeout time.Duration) string {
	t.Helper()
	return drainForCompletedTurnOptions(t, phone, cs, convID, timeout, false)
}

func drainForCompletedTurnWithoutModal(t *testing.T, phone *fakephone.Client, cs *noise.CipherState, convID string, timeout time.Duration) string {
	t.Helper()
	return drainForCompletedTurnOptions(t, phone, cs, convID, timeout, true)
}

type completedTurnReceiver interface {
	ReceiveBytes(time.Duration) ([]byte, error)
}

func drainForCompletedTurnOptions(t *testing.T, phone completedTurnReceiver, cs *noise.CipherState, convID string, timeout time.Duration, rejectModal bool) string {
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
		case protocol.TypeModalShown:
			if rejectModal {
				t.Fatalf("unexpected second modal_shown while draining the session-granted turn: %s", env.Payload)
			}
		case protocol.TypeUnrecognizedMessage:
			// A normal live turn must not expose parser gaps. This global alarm
			// requires a mapping or a measured known-ignored classification.
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
			// Rate limits affect the account, so there is no conversation filter.
			// Tolerate only the measured warning status; unknown statuses require
			// checking account limits or remeasuring the provider's vocabulary.
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
			// Accumulate every matching fragment; callers can inspect the full reply.
			reply.WriteString(p.Text)
			// M1: liveness — non-empty streamed deltas for the driving conv. No
			// content/echo assertion (real claude's words are non-deterministic).
			if strings.TrimSpace(p.Text) != "" {
				nonEmptyDeltas++
				t.Logf("M1: non-empty assistant_delta %d (seq=%d, %d bytes) for %q",
					nonEmptyDeltas, p.Seq, len(p.Text), convID)
			}
		case protocol.TypeTurnState:
			var st protocol.TurnStatePayload
			if err := json.Unmarshal(env.Payload, &st); err != nil {
				t.Fatalf("decode turn_state payload: %v", err)
			}
			if st.State != "idle" || st.ConversationID != convID {
				continue // the leading responding state precedes the delta
			}
			if nonEmptyDeltas == 0 {
				// A prior cancelled turn can leave its terminal idle queued after
				// drainForCancelledTurnEnd returns at turn_end. Without a delta,
				// this idle cannot be attributed to the turn being drained.
				continue
			}
			// M2: after matching nonempty text, terminal idle closes the turn.
			t.Logf("M2: terminal turn_state{idle} for %q — the turn closed", convID)
			return reply.String()
		}
	}
}
