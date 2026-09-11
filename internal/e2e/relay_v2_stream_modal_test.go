//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
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
// daemon under the stream toggle and sends one send_message. The original cases use
// the fakeclaude MCP rider; the stdio cases use its can_use_tool rider. Both park in
// the same permbridge registry, surface as modal_shown, and return the same verdict:
//
//	phone send_message(knownConvID) → ack
//	  → fakeclaude receives the user turn → control.Approve (blocks)
//	  → daemon parks in permbridge → streamApprovalBridge.Surface → modal_shown → phone
//	  → [answer]   phone modal_answer(allow_once|reject_once) → ResolveAnswer → ResolveStream
//	     [extended] phone waits past TWO windows, then answers — the daemon must not have denied
//	     [timeout]  phone's session ENDS, nobody can answer → permbridge timer denies
//	  → control.Approve returns the verdict → fakeclaude reflects a needle assistant echo
//	  → daemon stream drain → assistant_delta{needle} + modal_dismissed → phone asserts
//
// THE WINDOW IS A RE-CHECK INTERVAL, NOT A DEADLINE (#1932). The daemon asks its own
// liveness report at every expiry and re-arms the same window while the approval is
// still parked on a person AND an interactive client is still connected. That splits
// the fail-closed proof in two:
//
//   - extended: an answerer stays connected and answers late. Nothing may deny in the
//     meantime, so the late allow still lands. Without the report wired the approval
//     denies at 2s and this case reflects the forbidden approve-deny needle.
//   - timeout: the answerer is REMOVED first, and only then does the window deny. This
//     is the end-to-end proof that losing every answerer still denies — the property
//     the extension must not cost.
//
// Both rest on the same margin: fakeclaude blocks on control.Approve with a ctx (30s)
// far ABOVE the shrunk window (2s), so a deny it receives is the DAEMON's own
// permbridge time.AfterFunc firing rather than a fakeclaude self-timeout. The three
// needles are distinct: approve-allow (fail-open regression), approve-deny (the
// genuine daemon deny), approve-error (a client-side control.Approve failure that must
// NEVER appear). Each case asserts one and forbids the others, so a gate that fails
// open, hangs, or masks a client error goes red loudly rather than false-passing.
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
		// approvalTimeout sets the daemon's window via PYRY_APPROVAL_TIMEOUT. Since
		// #1932 that window is a RE-CHECK INTERVAL, not a hard deadline: it denies
		// only on a reading that says nobody can answer, and otherwise re-arms.
		approvalTimeout string
		// answer, when non-empty, is the OptionID the phone answers with; empty means
		// send NO answer (the timeout case).
		answer string
		// answerDelay holds the answer back this long past modal_shown. Non-zero only
		// in the extended case, where it must span MORE THAN ONE window so the answer
		// lands on an approval the daemon's own timer would already have denied
		// without the liveness report wired.
		answerDelay time.Duration
		// loseAnswerer drives the fail-closed arm: instead of answering, phone A's
		// session ENDS, so nobody is left able to answer — the condition the window
		// still denies on. A second, later conn witnesses the outcome (see the arm).
		loseAnswerer        bool
		stdio               bool
		offerAlwaysAllow    bool
		suppressAlwaysAllow bool
		question            bool
		childExit           bool
		// wantNeedle is the assistant_delta text the daemon's verdict must reflect;
		// forbidNeedles must never appear (fail-open / masked-error guards).
		wantNeedle    string
		forbidNeedles []string
		// wantOutcome / wantSource are the corroborating dismissal vocabulary: the
		// modal_dismissed payload for the answered cases, and — because that broadcast
		// goes out while nobody is connected in the loseAnswerer arm — retire's audit
		// record, which carries the same pair.
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
			name:             "stdio_allow",
			approvalTimeout:  "30s",
			answer:           allowOnce,
			stdio:            true,
			offerAlwaysAllow: true,
			wantNeedle:       "approve-allow",
			forbidNeedles:    []string{"approve-deny", "approve-error"},
			wantOutcome:      allowOnce,
			wantSource:       "remote",
		},
		{
			name:                "stdio_deny",
			approvalTimeout:     "30s",
			answer:              rejectOnce,
			stdio:               true,
			suppressAlwaysAllow: true,
			wantNeedle:          "approve-deny",
			forbidNeedles:       []string{"approve-allow", "approve-error"},
			wantOutcome:         rejectOnce,
			wantSource:          "remote",
		},
		{
			name:            "stdio_question_answer",
			approvalTimeout: "30s",
			answer:          allowOnce,
			stdio:           true,
			question:        true,
			wantNeedle:      "approve-allow",
			forbidNeedles:   []string{"approve-deny", "approve-error"},
			wantOutcome:     "answered",
			wantSource:      "remote",
		},
		{
			name:            "stdio_child_exit_late_answer",
			approvalTimeout: "30s",
			stdio:           true,
			childExit:       true,
		},
		{
			// The extension case (#1932): the phone stays connected and answers only
			// after MORE THAN ONE window has elapsed. Because an interactive client is
			// connected to a surfaced approval, every expiry re-arms instead of
			// denying, so the late answer still lands as the verdict.
			name:            "extended",
			approvalTimeout: "2s",
			answer:          allowOnce,
			answerDelay:     5 * time.Second,
			wantNeedle:      "approve-allow",
			forbidNeedles:   []string{"approve-deny", "approve-error"},
			wantOutcome:     allowOnce,
			wantSource:      "remote",
		},
		{
			// The load-bearing fail-closed case: no answer AND nobody left who could
			// give one, so the DAEMON denies on its own timer within one further
			// window, and the fake receives that deny (never allow, never a masked
			// error). Losing the answerer is what makes the deny reachable at all now.
			name:            "timeout",
			approvalTimeout: "2s",
			answer:          "",
			loseAnswerer:    true,
			wantNeedle:      "approve-deny",
			forbidNeedles:   []string{"approve-allow", "approve-error"},
			wantOutcome:     "denied_timeout",
			wantSource:      "timeout",
		},
		{
			name:            "stdio_timeout",
			approvalTimeout: "2s",
			loseAnswerer:    true,
			stdio:           true,
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

			stdinLog := filepath.Join(home, "stream-stdin.log")
			var h *Harness
			if tc.stdio {
				rider := `{"tool_name":"Bash","input":{"command":"true","reason_type":"input-lookalike"},"tool_use_id":"` + riderToolUseID + `",` +
					`"decision_reason":{"rule":"outside_read_only"},"decision_reason_type":"future_reason_kind",` +
					`"blocked_path":"/workspace/out","description":"Write output","default_to_no":true`
				if tc.offerAlwaysAllow || tc.suppressAlwaysAllow {
					rider += `,"permission_suggestions":[` +
						`{"type":"addRules","behavior":"allow","rules":[{"toolName":"Bash"}]},` +
						`{"type":"addRules","behavior":"allow","rules":[{"toolName":"Read","ruleContent":"//src/**"}]}` +
						`],"suppress_always_allow_rule":` + strconv.FormatBool(tc.suppressAlwaysAllow)
				}
				rider += `}`
				if tc.question {
					rider = `{"tool_name":"AskUserQuestion","input":{"questions":[{"question":"Pick?","header":"Pick","options":[{"label":"A","description":"first"},{"label":"B","description":"second"}],"multiSelect":false}]},"tool_use_id":"` + riderToolUseID + `"}`
				}
				env := []string{
					"PYRY_FAKE_CLAUDE_STREAM_CAN_USE_TOOL=" + rider,
					"PYRY_FAKE_CLAUDE_STDIN_LOG=" + stdinLog,
					"PYRY_APPROVAL_TIMEOUT=" + tc.approvalTimeout,
				}
				if tc.childExit {
					env = append(env, "PYRY_FAKE_CLAUDE_STREAM_EXIT_AFTER_CAN_USE_TOOL=1")
				}
				h = StartStreamInteractiveWithRelayStdio(t, home, initialUUID, fr.URL()+"/v2/server", env...)
			} else {
				h = StartStreamInteractiveWithRelay(t, home, initialUUID, fr.URL()+"/v2/server",
					"PYRY_FAKE_CLAUDE_STREAM_APPROVE=1",
					"PYRY_FAKE_CLAUDE_APPROVE_SOCKET_FILE="+socketFile,
					"PYRY_APPROVAL_TIMEOUT="+tc.approvalTimeout,
				)
			}
			t.Cleanup(func() { h.Stop(t) })

			// Hand the fake the daemon's real control socket (now known). It reads this at
			// dial time, well after this write.
			if !tc.stdio {
				if err := os.WriteFile(socketFile, []byte(h.SocketPath), 0o600); err != nil {
					t.Fatalf("write approve socket file: %v", err)
				}
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
			var questionShown protocol.QuestionShownPayload
			modalDeadline := time.Now().Add(20 * time.Second)
			for shown.ModalID == "" && questionShown.QuestionBatchID == "" {
				env, ok := nextEnv(modalDeadline)
				if !ok {
					t.Fatal("did not observe the permission surface before deadline; the fake request never parked or surfaced")
				}
				if env.Type == protocol.TypeError {
					t.Fatalf("unexpected error envelope while awaiting permission surface: %s", string(env.Payload))
				}
				if tc.question && env.Type == protocol.TypeQuestionShown {
					if err := json.Unmarshal(env.Payload, &questionShown); err != nil {
						t.Fatalf("decode question_shown payload: %v", err)
					}
				} else if !tc.question && env.Type == protocol.TypeModalShown {
					if err := json.Unmarshal(env.Payload, &shown); err != nil {
						t.Fatalf("decode modal_shown payload: %v", err)
					}
				}
			}
			if tc.question {
				if questionShown.ConversationID != knownConvID || len(questionShown.Questions) != 1 {
					t.Fatalf("question_shown = %+v, want one question scoped to %q", questionShown, knownConvID)
				}
			} else {
				if shown.Class != "permission" {
					t.Fatalf("modal_shown Class = %q, want %q", shown.Class, "permission")
				}
				if shown.ConversationID != knownConvID {
					t.Errorf("modal_shown ConversationID = %q, want %q", shown.ConversationID, knownConvID)
				}
				if tc.stdio {
					if string(shown.Reason) != `{"rule":"outside_read_only"}` || shown.ReasonType != "future_reason_kind" ||
						shown.BlockedPath != "/workspace/out" || shown.Description != "Write output" || !shown.DefaultToNo {
						t.Errorf("stdio modal context = %+v, want rider fields", shown)
					}
				}
				if shown.AlwaysAllow.Offered != tc.offerAlwaysAllow {
					t.Errorf("modal_shown always_allow offered = %v, want %v", shown.AlwaysAllow.Offered, tc.offerAlwaysAllow)
				}
				wantRules := []string{}
				if tc.offerAlwaysAllow {
					wantRules = []string{"Bash", "Read(//src/**)"}
				}
				if shown.AlwaysAllow.Rules == nil || !slices.Equal(shown.AlwaysAllow.Rules, wantRules) {
					t.Errorf("modal_shown always_allow rules = %v, want %v", shown.AlwaysAllow.Rules, wantRules)
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
			}

			if tc.childExit {
				dismissDeadline := time.Now().Add(10 * time.Second)
				for {
					env, ok := nextEnv(dismissDeadline)
					if !ok {
						t.Fatal("origin child exit did not retire its permission modal")
					}
					if env.Type != protocol.TypeModalDismissed {
						continue
					}
					var dismissed protocol.ModalDismissedPayload
					if err := json.Unmarshal(env.Payload, &dismissed); err != nil {
						t.Fatalf("decode child-exit modal_dismissed: %v", err)
					}
					if dismissed.ModalID == shown.ModalID {
						break
					}
				}

				replacementLog := childStdinLog(stdinLog, initialUUID)
				childReadyDeadline := time.Now().Add(10 * time.Second)
				for time.Now().Before(childReadyDeadline) {
					if _, err := os.ReadFile(replacementLog); err == nil {
						break
					}
					time.Sleep(25 * time.Millisecond)
				}
				if _, err := os.ReadFile(replacementLog); err != nil {
					t.Fatalf("replacement child did not become stdin-ready: %v", err)
				}

				sealSend(protocol.Envelope{
					ID:   answerReqID,
					Type: protocol.TypeModalAnswer,
					TS:   time.Now().UTC(),
					Payload: mustJSON(t, protocol.ModalAnswerPayload{
						ModalID:     shown.ModalID,
						OptionID:    allowOnce,
						AnswerToken: "e2e-2343-late-answer-token",
					}),
				})

				const syncNeedle = "e2e-2343-late-answer-sync"
				sealSend(protocol.Envelope{
					ID:   234302,
					Type: protocol.TypeSendMessage,
					TS:   time.Now().UTC(),
					Payload: mustJSON(t, protocol.SendMessagePayload{
						ConversationID: knownConvID,
						MessageID:      "u-sync",
						Text:           syncNeedle,
					}),
				})
				syncDeadline := time.Now().Add(10 * time.Second)
				for time.Now().Before(syncDeadline) {
					data, err := os.ReadFile(replacementLog)
					if err == nil && bytes.Contains(data, []byte(syncNeedle)) {
						break
					}
					time.Sleep(25 * time.Millisecond)
				}
				data, err := os.ReadFile(replacementLog)
				if err != nil {
					t.Fatalf("read replacement stdin log: %v", err)
				}
				if !bytes.Contains(data, []byte(syncNeedle)) {
					t.Fatalf("replacement child never received the synchronization turn; stdin=%s", data)
				}
				if bytes.Contains(data, []byte(`"request_id":"permission-1"`)) {
					t.Fatalf("late answer wrote the origin request's response to the replacement child: %s", data)
				}
				return
			}

			// --- Post-modal_shown arms. The approval is now parked AND answerable —
			// the state the extension protects, and the "was answerable at the window"
			// half of the fail-closed proof.
			if tc.question {
				sealSend(protocol.Envelope{
					ID:   answerReqID,
					Type: protocol.TypeQuestionAnswer,
					TS:   time.Now().UTC(),
					Payload: mustJSON(t, protocol.QuestionAnswerPayload{
						QuestionBatchID: questionShown.QuestionBatchID,
						AnswerToken:     "e2e-2343-question-answer-token",
						Answers: []protocol.QuestionAnswerEntry{{
							QuestionIndex: 0,
							Values:        []string{"B"},
						}},
					}),
				})
			} else if tc.loseAnswerer {
				// Step 1: END phone A's session. A plain WS close is NOT usable: the
				// relay↔binary leg is one multiplexed socket with no per-connection
				// disconnect frame (docs/protocol-mobile.md, close code 4408), so a
				// vanished phone stays in the daemon's active set until the 15-minute
				// idle sweep — far past fakeclaude's 30s dial ctx, which would land the
				// forbidden approve-error needle. A daemon-initiated close is the one
				// prompt, phone-driven way out: an inner frame whose type the daemon
				// does not recognise is rejected and its session deleted on the relay
				// manager's own goroutine. This case is honest that it ENDS the session
				// rather than pretending a WS close was observed.
				unknownFrame, err := json.Marshal(protocol.InnerFrameV2{
					Version: protocol.V2Version,
					Type:    "e2e-1932-not-a-real-inner-type",
				})
				if err != nil {
					t.Fatalf("marshal unknown inner frame: %v", err)
				}
				if err := phone.SendBytes(unknownFrame); err != nil {
					t.Fatalf("phone send unknown inner frame: %v", err)
				}
				// Gate on the daemon's own rejection so the next step cannot begin
				// while the session is still open — a sleep here would turn the
				// fail-closed proof into a flake. The reason field is deliberately not
				// asserted: the inner-frame decoder's type allowlist rejects the frame
				// before the dispatch switch's own default arm can, and both land the
				// same v2.state.reject and the same protocol-mismatch close.
				waitForLog(t, h.Stderr, "v2.state.reject", 10*time.Second)
				_ = phone.Close()

				// Step 2: the deny itself. retire writes a SINGLE audit line carrying
				// both the outcome vocabulary and THIS case's modal_id, so an unrelated
				// record cannot satisfy it. This is the proof AC-3 asks for: an
				// approval that was answerable one window ago is denied within one
				// further window of losing its last answerer, and never extended again.
				waitForLogLineAll(t, h.Stderr, []string{
					"modal_id=" + shown.ModalID,
					"outcome=" + tc.wantOutcome,
					"source=" + tc.wantSource,
				}, 20*time.Second)

				// Step 3: re-dial a FRESH interactive conn asking for the whole
				// retained tail (last_event_id 0 — the fresh-consumer input the ring
				// answers with everything it still holds, so no event-id bookkeeping is
				// needed). A witness is needed at all because a conn that cannot answer
				// also cannot observe: interactive is the daemon's only v2 capability,
				// and the turn stream and the modal fan-out both skip conns without it,
				// so there is no "observer that cannot answer" conn shape to lean on.
				//
				// Gating this on step 2 is what structurally excludes the re-extension
				// hazard the ticket flags: a phone returning BEFORE the window boundary
				// would be an answerer again and re-extend, but this one returns to an
				// approval that is already resolved.
				redialCtx, redialCancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer redialCancel()
				phone, err = fakephone.Dial(redialCtx, fr.URL(), serverID, payload.Token, "phone-a")
				if err != nil {
					t.Fatalf("phone re-dial: %v", err)
				}
				// FRESH CipherStates, rebound into nextEnv's closure. v2 has no session
				// resumption, so this is a new Noise_IK handshake: carrying the first
				// conn's cipher states across would desync the AEAD nonce and surface
				// as a decrypt failure that reads like a daemon bug. The send half is
				// unused — this conn only witnesses the replay.
				var fromStart uint64
				_, replayRecv := driveHandshakeToOpenDaemonInteractiveResuming(t, phone, pubKey, payload.Token, &fromStart)
				recvCS = replayRecv
			} else {
				// The extension arm's delay is the stimulus, not a synchronisation
				// hack: the point is that MORE THAN ONE window elapses on a parked,
				// answerable approval before the answer is sent. There is nothing to
				// gate on — permbridge is log-free and an extension re-arms silently —
				// and the wait is race-free in the safe direction: the phone stays
				// connected throughout, so a window landing after this sleep extends
				// too rather than racing the answer.
				if tc.answerDelay > 0 {
					time.Sleep(tc.answerDelay)
				}
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
			}

			// --- Drain until BOTH observables land (any order, bounded — no hang):
			//   1. assistant_delta whose Text reflects the daemon's verdict (wantNeedle),
			//      and NEVER a forbidden needle (a fail-open approve-allow, or a masked
			//      client error approve-error) — the fail-closed proof for the timeout case.
			//   2. modal_dismissed{wantOutcome, wantSource} — the corroborating vocabulary.
			// The deadline is generous over the 2s daemon window so the timeout case proves
			// the deny arrives promptly, not that the test outlasted a hang.
			// modal_dismissed is a bridge broadcast, not a ring event, so in the
			// loseAnswerer arm it went out while nobody was connected and is genuinely
			// unobservable on the replay conn. Its vocabulary is not lost — step 2
			// above gated on retire's audit line, which carries the same
			// denied_timeout/timeout pair — so that arm starts already satisfied. The
			// needle is what step 3's re-dial exists for: only it separates a genuine
			// daemon deny from a masked client-side approve-error, which would have
			// written a byte-identical audit record.
			sawNeedle := false
			sawDismissal := tc.loseAnswerer
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
				case protocol.TypeResync:
					// Only reachable on the loseAnswerer arm's replay conn: the ring
					// evicted this conversation's tail before the witness re-dialed, so
					// the verdict needle and the tool_use join are both unobservable.
					// Asserted so a future retention change fails legibly here instead
					// of as a confusing "never observed the verdict" timeout below.
					t.Fatalf("replay returned a resync marker (payload=%s): the conversation's retained tail was evicted "+
						"before the witness reconnected, so this case can no longer observe the verdict it proves", string(env.Payload))
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
				case protocol.TypeQuestionDismissed:
					if !tc.question {
						continue
					}
					var dis protocol.QuestionDismissedPayload
					if err := json.Unmarshal(env.Payload, &dis); err != nil {
						t.Fatalf("decode question_dismissed payload: %v", err)
					}
					if dis.QuestionBatchID != questionShown.QuestionBatchID || dis.Outcome != tc.wantOutcome || dis.Source != tc.wantSource {
						t.Fatalf("question_dismissed = %+v, want batch %q outcome %q source %q", dis, questionShown.QuestionBatchID, tc.wantOutcome, tc.wantSource)
					}
					sawDismissal = true
				case protocol.TypeModalDismissed:
					if tc.question {
						continue
					}
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
			if !sawToolUse && (!tc.stdio || tc.answer == allowOnce) {
				t.Fatalf("never observed a tool_use frame; the rider's gated call did not reach the phone, so the "+
					"feed a tool-call attribution report joins against (tool_use_id %q) is unreachable", riderToolUseID)
			}
			if sawToolUse && toolUse.ToolUseID != riderToolUseID {
				t.Errorf("tool_use ToolUseID = %q, want %q — the frame must carry the id the approval was raised with",
					toolUse.ToolUseID, riderToolUseID)
			}
			wantToolName := riderToolName
			if tc.question {
				wantToolName = "AskUserQuestion"
			}
			if sawToolUse && toolUse.Name != wantToolName {
				t.Errorf("tool_use Name = %q, want %q", toolUse.Name, wantToolName)
			}
			if sawToolUse && toolUse.ConversationID != knownConvID {
				t.Errorf("tool_use ConversationID = %q, want %q", toolUse.ConversationID, knownConvID)
			}

			if tc.stdio {
				data, err := os.ReadFile(childStdinLog(stdinLog, initialUUID))
				if err != nil {
					t.Fatalf("read fakeclaude stdin log: %v", err)
				}
				responses := 0
				for _, line := range strings.Split(string(data), "\n") {
					if strings.Contains(line, `"type":"control_response"`) &&
						strings.Contains(line, `"request_id":"permission-1"`) {
						responses++
					}
				}
				if responses != 1 {
					t.Errorf("correlated stdio control responses = %d, want exactly 1; stdin=%s", responses, data)
				}
				if tc.question && !bytes.Contains(data, []byte(`"answers":{"Pick?":"B"}`)) {
					t.Errorf("question control response did not carry the selected answer in updatedInput; stdin=%s", data)
				}
			}
		})
	}
}

// waitForLogLineAll blocks until ONE line of buf contains every substring in subs,
// or timeout elapses. It is waitForLog's line-scanning counterpart, and the
// distinction is load-bearing rather than stylistic: waitForLog's whole-buffer
// strings.Contains would be satisfied by two unrelated lines, whereas the audit
// record this gates on only proves anything if a single record carries both the
// deny vocabulary and the caller's own modal_id.
//
// It lives here rather than beside waitForLog for its first caller's sake; #2099's
// no-live-child refusal is the second, and gates on a single record carrying both
// the inert-arm event and the conversation id the client named.
func waitForLogLineAll(t *testing.T, buf *safeBuffer, subs []string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		for _, line := range strings.Split(buf.String(), "\n") {
			matched := true
			for _, sub := range subs {
				if !strings.Contains(line, sub) {
					matched = false
					break
				}
			}
			if matched {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("no single log line carried all of %v within %s; log=\n%s", subs, timeout, buf.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
}
