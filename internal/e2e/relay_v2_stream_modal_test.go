//go:build e2e

package e2e

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakephone"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakerelay"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// TestRelayV2_StreamModalPermissionRoundTrip is the LIVE, end-to-end proof of the
// stream-json permission round-trip under interactive_runner:"stream-json" (#1139) —
// the final leg of the streamrunner permission mechanism. Every piece is already
// built and unit-tested (permbridge #1103, mcp.approve #1104, streamApprovalBridge
// #1080, modalbridge #716, the stream keystroker guard #1131); this is the first time
// they run TOGETHER, live, against a spawned fakeclaude that ORIGINATES the approval.
//
// Per case one interactive phone (paired --allow-remote-permissions) handshakes to a
// daemon under the stream toggle, sends one send_message, and the fakeclaude approve
// rider dials control.Approve — the SAME client `pyry mcp-approve` calls — which parks
// in permbridge, surfaces as modal_shown, blocks, and returns the daemon's verdict:
//
//	phone send_message(knownConvID) → ack
//	  → fakeclaude receives the user turn → control.Approve (blocks)
//	  → daemon parks in permbridge → streamApprovalBridge.Surface → modal_shown → phone
//	  → [answer] phone modal_answer(allow_once|reject_once) → ResolveAnswer → ResolveStream
//	     [timeout] phone sends nothing → permbridge timer denies (PYRY_APPROVAL_TIMEOUT=2s)
//	  → control.Approve returns the verdict → fakeclaude reflects a needle assistant echo
//	  → daemon stream drain → assistant_delta{needle} + modal_dismissed → phone asserts
//
// THE TIMEOUT CASE IS LOAD-BEARING (fail-closed). fakeclaude blocks on control.Approve
// with a ctx (30s) far ABOVE the daemon's shrunk window (2s), so the deny it receives
// is the DAEMON's own permbridge time.AfterFunc firing — not a fakeclaude self-timeout.
// The three needles are distinct: approve-allow (fail-open regression), approve-deny
// (the genuine daemon deny), approve-error (a client-side control.Approve failure that
// must NEVER appear). The timeout case asserts approve-deny specifically AND forbids
// approve-allow / approve-error, so a gate that fails open, hangs, or masks a client
// error goes red loudly rather than false-passing.
//
// WHY THE IDS MUST LINE UP (same invariant as the send / interrupt siblings). The
// assistant_delta is gated by the stream drain: its sink tag (the runner's
// construction-time cfg.SessionID = the bootstrap pool id, pinned to initialUUID by
// seedBootstrapRegistry) must equal activeSession() (the active conversation's bound
// session id = initialUUID via seedBoundConversation). So seedBoundConversation is what
// lets the reflected needle reach the phone; modal_shown / modal_dismissed are
// operator-global broadcasts and reach it regardless.
func TestRelayV2_StreamModalPermissionRoundTrip(t *testing.T) {
	const (
		initialUUID = "11111111-1111-4111-8111-111111111111"
		knownConvID = "22222222-2222-4222-8222-222222222222"
		userText    = "e2e-1139-user:approve\n"
		sendReqID   = uint64(1139)
		answerReqID = uint64(1140)
		// The id runStreamJSONApprove's per-turn scheme mints for turn 1, and the id it
		// raises the approval with. A literal because this package cannot import the
		// fake's package main — the same reason the needles above are literals.
		riderToolUseID = "tu-1139-1"
		riderToolName  = "Bash"
	)

	allowOnce := string(turnevent.PermissionOptionKindAllowOnce)
	rejectOnce := string(turnevent.PermissionOptionKindRejectOnce)

	cases := []struct {
		name string
		// approvalTimeout shrinks (timeout case) or leaves generous (answer cases) the
		// daemon's fail-closed window via PYRY_APPROVAL_TIMEOUT.
		approvalTimeout string
		// answer, when non-empty, is the OptionID the phone answers with; empty means
		// send NO answer (the timeout case).
		answer string
		// wantNeedle is the assistant_delta text the daemon's verdict must reflect;
		// forbidNeedles must never appear (fail-open / masked-error guards).
		wantNeedle    string
		forbidNeedles []string
		// wantOutcome / wantSource are the corroborating modal_dismissed vocabulary.
		wantOutcome string
		wantSource  string
	}{
		{
			name:            "allow",
			approvalTimeout: "30s",
			answer:          allowOnce,
			wantNeedle:      "approve-allow",
			forbidNeedles:   []string{"approve-deny", "approve-error"},
			wantOutcome:     allowOnce,
			wantSource:      "remote",
		},
		{
			name:            "deny",
			approvalTimeout: "30s",
			answer:          rejectOnce,
			wantNeedle:      "approve-deny",
			forbidNeedles:   []string{"approve-allow", "approve-error"},
			wantOutcome:     rejectOnce,
			wantSource:      "remote",
		},
		{
			// The load-bearing fail-closed case: no answer, the DAEMON denies on its own
			// timer, and the fake receives that deny (never allow, never a masked error).
			name:            "timeout",
			approvalTimeout: "2s",
			answer:          "",
			wantNeedle:      "approve-deny",
			forbidNeedles:   []string{"approve-allow", "approve-error"},
			wantOutcome:     "denied_timeout",
			wantSource:      "timeout",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			home := shortHome(t)

			// Pair the phone WITH --allow-remote-permissions: ResolveAnswer gates on
			// dev.MayAnswerRemotePermission() (pairing defaults it OFF). Without the flag
			// the allow/deny answer denies at the device gate and the case would observe
			// approve-deny — so the flag is load-bearing for the answered cases.
			r := RunBareIn(t, home, "pair", "-pyry-name=test", "--name=phone-a", "--allow-remote-permissions")
			if r.ExitCode != 0 {
				t.Fatalf("pyry pair exit=%d\nstdout:\n%s\nstderr:\n%s", r.ExitCode, r.Stdout, r.Stderr)
			}
			payload := decodePairPayload(t, r.Stdout)
			pubKey, err := base64.StdEncoding.DecodeString(payload.ServerStaticPubkey)
			if err != nil {
				t.Fatalf("decode server static pubkey: %v", err)
			}

			// Bind knownConvID → the bootstrap session (initialUUID): sessionRouter.Route
			// resolves the send and stamps the active cursor (so the modal + delta scope to
			// knownConvID), and the drain gate's activeSession() equals the sink tag. Must
			// exist before the daemon loads conversations.json at startup.
			seedBoundConversation(t, home, knownConvID, initialUUID)

			fr := fakerelay.New(relayTestLogger())
			t.Cleanup(func() { _ = fr.Close() })

			// The socket path rides in as a FILE path (known pre-spawn); the daemon's
			// random control socket is unknown until spawn, so the fake reads it lazily
			// from this file, which the test writes after startup.
			socketFile := filepath.Join(home, "approve-socket.txt")

			h := StartStreamInteractiveWithRelay(t, home, initialUUID, fr.URL()+"/v2/server",
				"PYRY_FAKE_CLAUDE_STREAM_APPROVE=1",
				"PYRY_FAKE_CLAUDE_APPROVE_SOCKET_FILE="+socketFile,
				"PYRY_APPROVAL_TIMEOUT="+tc.approvalTimeout,
			)
			t.Cleanup(func() { h.Stop(t) })

			// Hand the fake the daemon's real control socket (now known). It reads this at
			// dial time, well after this write.
			if err := os.WriteFile(socketFile, []byte(h.SocketPath), 0o600); err != nil {
				t.Fatalf("write approve socket file: %v", err)
			}

			serverID := readPersistedServerID(t, home)
			waitBinaryHello(t, fr, serverID)

			dialCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			phone, err := fakephone.Dial(dialCtx, fr.URL(), serverID, payload.Token, "phone-a")
			if err != nil {
				t.Fatalf("phone dial: %v", err)
			}
			t.Cleanup(func() { _ = phone.Close() })
			sendCS, recvCS := driveHandshakeToOpenDaemonInteractive(t, phone, pubKey, payload.Token)

			sealSend := func(env protocol.Envelope) {
				t.Helper()
				raw, err := json.Marshal(env)
				if err != nil {
					t.Fatalf("marshal envelope: %v", err)
				}
				ciphertext, err := sendCS.Encrypt(raw)
				if err != nil {
					t.Fatalf("seal envelope: %v", err)
				}
				sendNoiseMsg(t, phone, ciphertext)
			}

			// The rider's tool_use frame (#1918), recorded at the SINGLE decrypt point
			// rather than in any await loop. It rides the child's stdout while the
			// approval rides the control socket — two transports, two goroutines — so
			// nothing the daemon guarantees orders it against modal_shown, and every
			// await loop below `continue`s past frames it does not match. Recording
			// centrally is what makes the later assertion ordering-independent.
			var toolUse protocol.ToolUsePayload
			sawToolUse := false

			// nextEnv decrypts the next binary→phone application envelope, skipping
			// non-noise_msg frames in capture order so the receive nonce stays in sequence.
			// One recvCS for the whole case (the single reader). ok=false on deadline.
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
					env := decryptInnerEnvelope(t, inner, recvCS)
					if !sawToolUse && env.Type == protocol.TypeToolUse {
						if err := json.Unmarshal(env.Payload, &toolUse); err != nil {
							t.Fatalf("decode tool_use payload: %v", err)
						}
						sawToolUse = true
					}
					return env, true
				}
			}

			// --- Drive one send_message and await its sealed ack. The ack proves the turn
			// was accepted and the active cursor stamped to knownConvID; it precedes
			// delivery (the fake dials control.Approve only after the child is stdin-ready),
			// so it also precedes the modal_shown surfaced when the daemon parks the dial.
			sealSend(protocol.Envelope{
				ID:   sendReqID,
				Type: protocol.TypeSendMessage,
				TS:   time.Now().UTC(),
				Payload: mustJSON(t, protocol.SendMessagePayload{
					ConversationID: knownConvID,
					MessageID:      "u-1",
					Text:           userText,
				}),
			})
			ackDeadline := time.Now().Add(15 * time.Second)
			gotAck := false
			for !gotAck {
				env, ok := nextEnv(ackDeadline)
				if !ok {
					t.Fatal("did not receive the send_message ack before deadline; the turn was never accepted")
				}
				if env.Type == protocol.TypeError {
					t.Fatalf("unexpected error envelope while awaiting the send_message ack: %s", string(env.Payload))
				}
				if env.Type == protocol.TypeAck && env.InReplyTo != nil && *env.InReplyTo == sendReqID {
					gotAck = true
				}
			}

			// --- Await modal_shown: the fake's originated approval, surfaced as the same
			// permission modal clients already answer. Assert the permission class, the
			// fixed 4 option IDs, a non-empty ModalID nonce, and the conversation scoping.
			// The ~20s deadline absorbs spawn + stdin-ready + first-turn latency (the fake
			// only dials once the child is live). This precedes the answer, so a later
			// assertion cannot pass vacuously over a modal that never surfaced.
			var shown protocol.ModalShownPayload
			modalDeadline := time.Now().Add(20 * time.Second)
			for shown.ModalID == "" {
				env, ok := nextEnv(modalDeadline)
				if !ok {
					t.Fatal("did not observe modal_shown before deadline; the fake's control.Approve never parked / surfaced " +
						"(the approval registry or the stream surfacer may not be wired under the stream toggle)")
				}
				if env.Type == protocol.TypeError {
					t.Fatalf("unexpected error envelope while awaiting modal_shown: %s", string(env.Payload))
				}
				if env.Type != protocol.TypeModalShown {
					continue
				}
				if err := json.Unmarshal(env.Payload, &shown); err != nil {
					t.Fatalf("decode modal_shown payload: %v", err)
				}
				if shown.ModalID == "" {
					t.Fatal("modal_shown carried an empty modal_id")
				}
			}
			if shown.Class != "permission" {
				t.Fatalf("modal_shown Class = %q, want %q", shown.Class, "permission")
			}
			if shown.ConversationID != knownConvID {
				t.Errorf("modal_shown ConversationID = %q, want %q", shown.ConversationID, knownConvID)
			}
			wantIDs := []string{
				string(turnevent.PermissionOptionKindAllowOnce),
				string(turnevent.PermissionOptionKindAllowAlways),
				string(turnevent.PermissionOptionKindRejectOnce),
				string(turnevent.PermissionOptionKindRejectAlways),
			}
			gotIDs := make([]string, len(shown.Options))
			for i, o := range shown.Options {
				gotIDs[i] = o.ID
			}
			if !slices.Equal(gotIDs, wantIDs) {
				t.Fatalf("modal_shown option IDs = %v, want %v (the fixed screen-independent permission set)", gotIDs, wantIDs)
			}

			// --- Answer arm: gated phone resolves the verdict. Timeout arm: send nothing —
			// the daemon's permbridge timer denies at PYRY_APPROVAL_TIMEOUT (2s).
			if tc.answer != "" {
				sealSend(protocol.Envelope{
					ID:   answerReqID,
					Type: protocol.TypeModalAnswer,
					TS:   time.Now().UTC(),
					Payload: mustJSON(t, protocol.ModalAnswerPayload{
						ModalID:     shown.ModalID,
						OptionID:    tc.answer,
						AnswerToken: "e2e-1139-answer-token",
					}),
				})
			}

			// --- Drain until BOTH observables land (any order, bounded — no hang):
			//   1. assistant_delta whose Text reflects the daemon's verdict (wantNeedle),
			//      and NEVER a forbidden needle (a fail-open approve-allow, or a masked
			//      client error approve-error) — the fail-closed proof for the timeout case.
			//   2. modal_dismissed{wantOutcome, wantSource} — the corroborating vocabulary.
			// The deadline is generous over the 2s daemon window so the timeout case proves
			// the deny arrives promptly, not that the test outlasted a hang.
			sawNeedle := false
			sawDismissal := false
			drainDeadline := time.Now().Add(25 * time.Second)
			for !(sawNeedle && sawDismissal) {
				env, ok := nextEnv(drainDeadline)
				if !ok {
					switch {
					case !sawNeedle:
						t.Fatalf("never observed the verdict assistant_delta (want Text containing %q); the round-trip "+
							"never completed — the fake's control.Approve never returned, or the reflected needle was gated/dropped", tc.wantNeedle)
					default:
						t.Fatalf("observed the verdict needle %q but never modal_dismissed{%s,%s}", tc.wantNeedle, tc.wantOutcome, tc.wantSource)
					}
				}
				if env.Type == protocol.TypeError {
					t.Fatalf("unexpected error envelope during drain: %s", string(env.Payload))
				}
				switch env.Type {
				case protocol.TypeAssistantDelta:
					var d protocol.AssistantDeltaPayload
					if err := json.Unmarshal(env.Payload, &d); err != nil {
						t.Fatalf("decode assistant_delta payload: %v", err)
					}
					for _, bad := range tc.forbidNeedles {
						if strings.Contains(d.Text, bad) {
							t.Fatalf("assistant_delta carried the FORBIDDEN needle %q (Text=%q); the %s case must reflect only %q — "+
								"approve-allow is a fail-open, approve-error is a masked client-side failure, and either must fail loudly",
								bad, d.Text, tc.name, tc.wantNeedle)
						}
					}
					if strings.Contains(d.Text, tc.wantNeedle) {
						if d.ConversationID != knownConvID {
							t.Errorf("assistant_delta ConversationID = %q, want %q", d.ConversationID, knownConvID)
						}
						sawNeedle = true
					}
				case protocol.TypeModalDismissed:
					var dis protocol.ModalDismissedPayload
					if err := json.Unmarshal(env.Payload, &dis); err != nil {
						t.Fatalf("decode modal_dismissed payload: %v", err)
					}
					if dis.ModalID != shown.ModalID {
						t.Fatalf("modal_dismissed ModalID = %q, want %q", dis.ModalID, shown.ModalID)
					}
					if dis.Outcome != tc.wantOutcome {
						t.Errorf("modal_dismissed Outcome = %q, want %q", dis.Outcome, tc.wantOutcome)
					}
					if dis.Source != tc.wantSource {
						t.Errorf("modal_dismissed Source = %q, want %q", dis.Source, tc.wantSource)
					}
					sawDismissal = true
				}
			}

			// --- The rider's gated call reached the phone as a tool_use frame carrying
			// the SAME tool_use_id it raised the approval with (#1918). That join is what
			// a later attribution report — "this approval is parked on a conversation
			// with that call in flight" — is built on, so proving the feed reachable here
			// is what keeps such a report from reporting negative whether the daemon is
			// right or wrong.
			//
			// Asserted after the drain rather than inside it, and never against
			// modal_shown's arrival: the two ride different transports. It is nonetheless
			// not a race — the block and the verdict text ride the same child stdout in
			// that order through one parser, one drain goroutine and one conn, and nextEnv
			// is the single reader, so the frame is necessarily consumed before the needle
			// that ended the loop.
			if !sawToolUse {
				t.Fatalf("never observed a tool_use frame; the rider's gated call did not reach the phone, so the "+
					"feed a tool-call attribution report joins against (tool_use_id %q) is unreachable", riderToolUseID)
			}
			if toolUse.ToolUseID != riderToolUseID {
				t.Errorf("tool_use ToolUseID = %q, want %q — the frame must carry the id the approval was raised with",
					toolUse.ToolUseID, riderToolUseID)
			}
			if toolUse.Name != riderToolName {
				t.Errorf("tool_use Name = %q, want %q", toolUse.Name, riderToolName)
			}
			if toolUse.ConversationID != knownConvID {
				t.Errorf("tool_use ConversationID = %q, want %q", toolUse.ConversationID, knownConvID)
			}
		})
	}
}
