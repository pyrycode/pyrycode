//go:build e2e_realclaude

package realclaude

// The #997 deliverable: a real-claude PER-CONVERSATION reply-stream liveness gate,
// the sixth and final layer under the mobile #528 live-emulator gate, closing the
// #996 per-conversation reply-stream coverage gap. (See the Scope note below on why
// this is a liveness gate, not a standalone RED/GREEN oracle for #996.)
//
// Where the #854 bootstrap-liveness gate (interactive_bootstrap_liveness_test.go)
// drives a PRE-BOUND conversation on the daemon's own bootstrap session, this gate
// creates the conversation OVER THE WIRE — create_conversation, no seeded binding —
// so the daemon mints a dedicated per-conversation claude session and the reply
// must bind THAT per-conversation transcript. That is exactly the path #996 breaks.
//
// #996 mechanism: send_message routes a turn and stamps the active-conversation
// cursor (sessionRouter.Route), firing the follow-active switch. The structured
// turn-stream producer is mid pre-stream backoff when the switch lands; before the
// fix that backoff slept on the PARENT ctx, which a switch does not cancel, so the
// producer waited out the full delay while real claude opened-appended-closed the
// per-conversation transcript. The re-subscription's first os.Stat then found the
// file already present, classified it a warm resume, and tailed from EOF — past
// the whole reply. The phone got no assistant_delta, and nothing was logged. The
// bootstrap-only #854 gate never exercises this: its conversation is bound to the
// bootstrap session, so no per-conversation switch fires.
//
// Scope — a LIVENESS gate, not a deterministic #996 oracle. This proves the
// create-over-the-wire -> per-conversation session -> send -> reply round-trip
// works end-to-end with real claude. It does NOT by itself catch #996: the
// warm-skip is a timing race that fires only when the producer is mid pre-stream
// backoff at the instant the switch lands, so real claude has to write the
// transcript inside that ~500ms window. In this fast, co-located harness real
// claude takes seconds to reply, so the producer wins the re-subscribe race with
// OR without the fix — verified live 2026-07-15, all three cases green against both
// main+#989 and the fixed producer. The live #528 emulator (slower / warmer
// timing) is where #996 actually surfaced. The DETERMINISTIC RED->GREEN proof of
// #996 lives in the hermetic producer unit test
// (internal/turnbridge/producer_test.go, TestNewTargetSubscriber_SwitchDuring...),
// which forces the mid-backoff precondition directly. This gate's value is
// standing real-claude coverage of the per-conversation path in `make preship`,
// per the real-claude-e2e-in-a-pre-ship-gate rule.
//
// Placement alone wires this into `make preship` (= make check + make
// e2e-realclaude) via the e2e_realclaude build tag; no Makefile change, mirroring
// how #854 satisfied its AC #5. It reuses the #854 harness in this package
// (spawnBootstrapDaemon, driveHandshakeInteractive, sealSendMessage,
// drainForAssistantReply, and the pair/seed/relay helpers) and adds two wire-drive
// helpers (createConversationViaPhone, createWorkspaceFolderViaPhone) transcribed
// into this package because the e2e and e2e_realclaude build tags are disjoint, so
// the internal/e2e originals cannot be imported.
//
// Each case asserts ONLY liveness — a non-empty streamed assistant_delta for the
// specific conversation id within a generous timeout — never claude's words
// (asserting content would be non-deterministic and would risk the substrate guard).

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

// livePerConvBootstrapUUID is the seeded bootstrap POOL id for the
// per-conversation gate. Distinct from the #854 gate's liveBootstrapUUID so the
// two files never share a fixed identifier even though they run in the same
// package. The per-conversation sessions this gate creates get server-minted ids;
// only the bootstrap pool id is seeded (loaded once at startup).
const livePerConvBootstrapUUID = "88888888-8888-4888-8888-888888888888"

// perTurnReplyBudget is the per-turn liveness budget for a real-claude
// per-conversation reply: a cold PTY session (spawn + model load + first-turn
// reply + growth-confirm + the per-conversation transcript bind), not fakeclaude
// milliseconds. Matches the #854 gate's first-turn budget.
const perTurnReplyBudget = 120 * time.Second

// TestInteractivePerConversationLiveness_Default is case 1: a default
// per-conversation conversation (create_conversation with a null cwd → the daemon
// workdir) must stream its reply. This is the plainest reproduction of #996.
func TestInteractivePerConversationLiveness_Default(t *testing.T) {
	h := startPerConversationHarness(t)
	nonce := time.Now().UnixNano()

	convID := createConversationViaPhone(t, h.phone, h.initSend, h.initRecv, 2, nil)
	sealSendMessage(t, h.phone, h.initSend, 3, convID, "m-1",
		fmt.Sprintf("Reply with a single short word. run=%d", nonce))
	drainForAssistantReply(t, h.phone, h.initRecv, convID, 1, perTurnReplyBudget)
}

// TestInteractivePerConversationLiveness_WorkspaceFolder is case 2: a conversation
// cwd'd into a phone-created workspace folder (create_workspace_folder →
// create_conversation with that path as cwd) must stream its reply. It exercises
// the per-Cwd session directory the by-id resolver tails for a non-default cwd.
func TestInteractivePerConversationLiveness_WorkspaceFolder(t *testing.T) {
	h := startPerConversationHarness(t)
	nonce := time.Now().UnixNano()

	folder := createWorkspaceFolderViaPhone(t, h.phone, h.initSend, h.initRecv, 2,
		"~", fmt.Sprintf("ws-%d", nonce))
	convID := createConversationViaPhone(t, h.phone, h.initSend, h.initRecv, 3, &folder)
	sealSendMessage(t, h.phone, h.initSend, 4, convID, "m-1",
		fmt.Sprintf("Reply with a single short word. run=%d", nonce))
	drainForAssistantReply(t, h.phone, h.initRecv, convID, 1, perTurnReplyBudget)
}

// TestInteractivePerConversationLiveness_ActiveSwitch is case 3: two
// per-conversation conversations, driven turn A then turn B. Each reply must stream
// to its OWN conversation id — drainForAssistantReply only returns on a non-empty
// assistant_delta whose conversation_id matches, so a reply mis-bound to the other
// conversation would time out. This is the follow-active switch under real load.
//
// Since #2085 the two creates spawn nothing, so each conversation's claude comes
// up on its own turn and the switch lands on a child that is starting rather than
// one already warm. Liveness is unaffected — both turns still have to stream to
// their own id. The timing note in the Scope block above only gets more true: a
// cold spawn lengthens the gap before the transcript appears, so the producer
// wins the re-subscribe race by an even wider margin, and this gate remains a
// standing liveness gate rather than a #996 oracle.
func TestInteractivePerConversationLiveness_ActiveSwitch(t *testing.T) {
	h := startPerConversationHarness(t)
	nonce := time.Now().UnixNano()

	convA := createConversationViaPhone(t, h.phone, h.initSend, h.initRecv, 2, nil)
	convB := createConversationViaPhone(t, h.phone, h.initSend, h.initRecv, 3, nil)

	sealSendMessage(t, h.phone, h.initSend, 4, convA, "m-a",
		fmt.Sprintf("Reply with a single short word. run=%d conv=A", nonce))
	drainForAssistantReply(t, h.phone, h.initRecv, convA, 1, perTurnReplyBudget)

	sealSendMessage(t, h.phone, h.initSend, 5, convB, "m-b",
		fmt.Sprintf("Reply with a single short word. run=%d conv=B", nonce))
	drainForAssistantReply(t, h.phone, h.initRecv, convB, 1, perTurnReplyBudget)
}

// --- harness ----------------------------------------------------------------

// perConvHarness bundles a live daemon and a handshaken interactive phone.
type perConvHarness struct {
	phone    *fakephone.Client
	initSend *noise.CipherState
	initRecv *noise.CipherState
	home     string
	workdir  string
	// daemon is the live pyry process. Exposed for its captured stderr, which
	// carries the daemon's own "spawning claude" records — the read
	// TestInteractiveSystemPromptFile_LiveSpawnArgv takes its spawn-argv assertion
	// from (#2093). The cases in this file do not use it.
	daemon *bootstrapDaemon
}

// startPerConversationHarness stands up the real interactive stack exactly like
// the #854 bootstrap gate MINUS the seeded bound conversation: each case creates
// its conversation over the wire. Skips cleanly when claude / creds are absent.
func startPerConversationHarness(t *testing.T) *perConvHarness {
	t.Helper()
	return startPerConversationHarnessSeeded(t, nil)
}

// startPerConversationHarnessSeeded is startPerConversationHarness with a hook
// that runs after the bootstrap registry is seeded and BEFORE the daemon starts,
// for a case whose conversation state must already be on disk when the daemon
// loads conversations.json. #2150 is the first such case: no verb writes a
// conversation's system prompt yet (#2151 is that ticket), so the only way to
// have one stored is to seed the file the daemon reads at startup.
func startPerConversationHarnessSeeded(t *testing.T, seed func(home, workdir string)) *perConvHarness {
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
	// state (empty claude sessions dir) and the null-cwd conversation's default cwd.
	workdir := filepath.Join(home, "work")
	if err := os.MkdirAll(workdir, 0o700); err != nil {
		t.Fatalf("realclaude: mkdir workdir: %v", err)
	}

	// Pair a device BEFORE the daemon starts.
	exit, stdout, stderr := runPyry(t, "pair", "-pyry-name=test", "--name=phone-a")
	if exit != 0 {
		t.Fatalf("pyry pair exit=%d\nstdout:\n%s\nstderr:\n%s", exit, stdout, stderr)
	}
	payload := decodePairPayload(t, stdout)
	pubKey, err := base64.StdEncoding.DecodeString(payload.ServerStaticPubkey)
	if err != nil {
		t.Fatalf("decode server static pubkey: %v", err)
	}

	// Seed ONLY the bootstrap pool id (loaded once at startup). NO
	// seedBoundConversation — each case creates its conversation over the wire.
	seedBootstrapRegistry(t, home, livePerConvBootstrapUUID)
	if seed != nil {
		seed(home, workdir)
	}

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
	return &perConvHarness{phone: phone, initSend: initSend, initRecv: initRecv, home: home, workdir: workdir, daemon: d}
}

// --- wire-drive helpers (transcribed into this package) ---------------------

// createConversationViaPhone seals a create_conversation with the given cwd (nil
// ⇒ server default = the daemon workdir), drains to the conversation_created reply
// (correlated by InReplyTo), and returns the server-minted conversation id. The
// reply is sent after the handler mints + binds + persists the dedicated session
// — so the conversation exists and its session id is bound by the time this
// returns, but since #2085 NO claude is running for it: the child comes up on the
// conversation's first message. Every caller in this package sends one, so each
// case's own send_message is what spawns, and its drain is what waits on the
// cold PTY. Transcribed from internal/e2e/per_conversation_eviction_test.go
// (which cannot be imported: the e2e and e2e_realclaude build tags are
// disjoint). The 60s budget is now slack for a loaded host rather than a spawn
// allowance.
func createConversationViaPhone(t *testing.T, phone *fakephone.Client, initSend, initRecv *noise.CipherState, reqID uint64, cwd *string) string {
	t.Helper()
	sealEnvelope(t, phone, initSend, protocol.Envelope{
		ID:      reqID,
		Type:    protocol.TypeCreateConversation,
		TS:      time.Now().UTC(),
		Payload: mustJSON(t, protocol.CreateConversationPayload{Cwd: cwd}),
	})
	env := drainForReply(t, phone, initRecv, protocol.TypeConversationCreated, reqID, 60*time.Second)
	var p protocol.ConversationCreatedPayload
	if err := json.Unmarshal(env.Payload, &p); err != nil {
		t.Fatalf("unmarshal conversation_created payload: %v", err)
	}
	if p.ID == "" {
		t.Fatalf("conversation_created (req %d) has empty id", reqID)
	}
	return p.ID
}

// createWorkspaceFolderViaPhone seals a create_workspace_folder (parent + single
// folder name), drains to the workspace_folder_created reply, and returns the
// canonical symlink-resolved path the daemon created under $HOME. No e2e helper
// exists for this verb, so it is hand-written to the same shape as
// createConversationViaPhone.
func createWorkspaceFolderViaPhone(t *testing.T, phone *fakephone.Client, initSend, initRecv *noise.CipherState, reqID uint64, parent, name string) string {
	t.Helper()
	sealEnvelope(t, phone, initSend, protocol.Envelope{
		ID:      reqID,
		Type:    protocol.TypeCreateWorkspaceFolder,
		TS:      time.Now().UTC(),
		Payload: mustJSON(t, protocol.CreateWorkspaceFolderPayload{Parent: parent, Name: name}),
	})
	env := drainForReply(t, phone, initRecv, protocol.TypeWorkspaceFolderCreated, reqID, 30*time.Second)
	var p protocol.WorkspaceFolderCreatedPayload
	if err := json.Unmarshal(env.Payload, &p); err != nil {
		t.Fatalf("unmarshal workspace_folder_created payload: %v", err)
	}
	if p.Path == "" {
		t.Fatalf("workspace_folder_created (req %d) has empty path", reqID)
	}
	return p.Path
}

// sealEnvelope marshals env, seals it under the phone's send CipherState, and
// writes it as an InnerFrameV2(noise_msg) — the generalisation of sealSendMessage
// for the control verbs this file drives.
func sealEnvelope(t *testing.T, phone *fakephone.Client, cs *noise.CipherState, env protocol.Envelope) {
	t.Helper()
	raw, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal %s envelope: %v", env.Type, err)
	}
	ct, err := cs.Encrypt(raw)
	if err != nil {
		t.Fatalf("seal %s: %v", env.Type, err)
	}
	sendNoiseMsg(t, phone, ct)
}

// drainForReply reads binary→phone frames in receive order — every noise_msg MUST
// be decrypted in order to keep the receive nonce in sync — and returns the first
// envelope of wantType whose InReplyTo matches reqID. Interleaved acks / turn_state
// / earlier replies are decrypted and skipped; a non-noise_msg control frame (e.g.
// rekey) is skipped WITHOUT decrypting so it does not advance the nonce. The mirror
// of drainForAssistantReply, correlating a request/reply pair instead of a delta.
func drainForReply(t *testing.T, phone *fakephone.Client, cs *noise.CipherState, wantType string, reqID uint64, timeout time.Duration) protocol.Envelope {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			t.Fatalf("no %s reply to request %d within %s", wantType, reqID, timeout)
		}
		raw, err := phone.ReceiveBytes(remaining)
		if err != nil {
			if errors.Is(err, fakephone.ErrReceiveTimeout) {
				t.Fatalf("no %s reply to request %d within %s", wantType, reqID, timeout)
			}
			t.Fatalf("phone receive (awaiting %s): %v", wantType, err)
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
		if env.Type == wantType && env.InReplyTo != nil && *env.InReplyTo == reqID {
			return env
		}
		// ack / turn_state / an earlier reply — keep draining in order.
	}
}
