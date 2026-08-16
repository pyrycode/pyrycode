//go:build e2e_realclaude

package realclaude

// TestInteractiveChangeWorkspace is the #1029 deliverable: a real-claude gate for
// the change_workspace verb, driven over the Noise v2 wire against a freshly
// spawned daemon supervising a LIVE real `claude --model haiku` child. It proves
// two things the fake tier structurally cannot: the verb round-trips correctly on
// the real interactive stack, and it does not wedge or kill that live child.
//
// Per the 2026-07-08 real-claude-e2e-in-a-pre-ship-gate policy ("fake-mock e2e is
// necessary but not sufficient", proof cases #949 and #854/#930) every
// operator-facing happy-path flow needs a real-claude e2e in `make preship`.
// change_workspace had fake-tier coverage only (#980).
//
// The fake tier still OWNS the verb's shape and every negative path —
// TestRelayV2_ChangeWorkspace and its siblings cover the round-trip, the
// confine-reject-no-leak path and not-found. Do not re-assert those here and do
// not add an escaping-path case: the real tier adds the live child, not more
// shapes. A scripted fake cannot regress a real child dying.
//
// ANTI-GOAL — do not "fix" this test into a hang. It does NOT assert that any
// spawned claude runs in the NEW folder, and it must never be changed to. No
// production path reads a stored conv.Cwd and spawns with it: a session's spawn
// workdir is fixed at runner construction (Pool.CreateIn threads spawnDir into
// RunnerConfig.WorkDir), the only caller passing a non-empty spawnDir is the
// create_conversation handler reading its own PAYLOAD cwd, and respawn does not
// rebind either — (*streamsup.Runner).RestartFresh mutates only the session id and
// the rotate-pending flag, never re-reading the workdir. So the turn-2 delta below
// is produced by the child still running at the conversation's ORIGINAL cwd. It is
// a liveness assertion about the daemon surviving the verb, not evidence of cwd
// adoption. The "takes effect on the next fresh session spawn" claims on
// Conversation.Cwd and on the ChangeWorkspace doc comment are unbacked by code;
// that is a separate production/doc defect, filed separately, and this test must
// not try to compensate for it.
//
// Ordering rationale (do not reorder without cause). Turn 1 runs BEFORE the verb
// and does two load-bearing jobs: it proves the child was alive beforehand (without
// it a red turn 2 has two readings), and it drains the turn to its terminal
// turn_state{idle} so the verb runs against a quiescent wire — the same rationale
// TestInteractiveConversationLifecycle's header records. The verb's short
// metaVerbReplyBudget is correct ONLY because of that drain; if turn 1 ever stops
// draining to idle, the verb drain must move to perTurnReplyBudget.
//
// No config file is written. TestInteractiveStreamLiveness calls
// writeStreamInteractiveConfig before spawning; this test deliberately does not.
// Since #1348 the empty/default interactive_runner already selects the stream-json
// runner (see the "" arm of selectInteractiveRunner), so writing the config would
// add a moving part that no longer changes anything. Copying the spine from
// interactive_stream_liveness_test.go makes copying that call the default mistake.
//
// Every helper is reused from the restored harness (harness_daemon_test.go, #1473)
// and its two sibling spines; nothing new is rolled except readConversationCwdOnDisk.
// Sequential, no t.Parallel — WithWorktreeAuthenticated calls t.Setenv, and the
// four drains must run in order on one goroutine so the Noise receive nonce stays
// in lockstep.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakephone"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakerelay"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// Fixed identifiers for the seeded state, in the ticket-number idiom. Distinct
// from every other realclaude gate's pair so the same-package files never collide
// on an identifier. changeWSBootstrapUUID is the bootstrap session's POOL id
// (pinned via seedBootstrapRegistry); changeWSConvID is the driving conversation,
// bound to it via seedBoundConversation.
const (
	changeWSBootstrapUUID = "10290000-0000-4000-8000-000000000001"
	changeWSConvID        = "10290000-0000-4000-8000-000000000002"
)

func TestInteractiveChangeWorkspace(t *testing.T) {
	// No t.Parallel: WithWorktreeAuthenticated calls t.Setenv.
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skipf("realclaude: claude not on PATH: %v", err)
	}
	home := WithWorktreeAuthenticated(t) // skips cleanly when no creds
	claudeBin, err := exec.LookPath("claude")
	if err != nil {
		t.Fatalf("realclaude: resolve claude: %v", err)
	}

	// Isolated workdir under the authenticated HOME: the daemon workdir AND the
	// seeded conversation's ORIGINAL cwd — the folder the live child actually runs
	// in, before and after the verb. It differs from the change target by
	// construction, which is what stops the on-disk assertion below from passing
	// against un-mutated state.
	workdir := filepath.Join(home, "work")
	if err := os.MkdirAll(workdir, 0o700); err != nil {
		t.Fatalf("realclaude: mkdir workdir: %v", err)
	}

	// The change target, minted directly rather than via create_workspace_folder:
	// resolveWorkspaceDir uses the STRICT, non-creating confiner, so a missing
	// target is a reject, not a create. That verb's real-tier proof is
	// TestInteractivePerConversationLiveness_WorkspaceFolder's; driving it here
	// would re-add a second concern.
	nonce := time.Now().UnixNano()
	targetName := fmt.Sprintf("ws-%d", nonce)
	if err := os.MkdirAll(filepath.Join(home, targetName), 0o700); err != nil {
		t.Fatalf("realclaude: mkdir change target: %v", err)
	}
	// Sent in the tilde form a real phone sends — expandTilde's contract is that a
	// client cannot know the daemon's absolute home. That is also what makes the
	// "resolved realpath, not the raw request" assertion bite on EVERY platform: an
	// absolute request would differ from its realpath only where $HOME sits under a
	// symlink, true for a macOS tempdir under /var but generally false on Linux, so
	// the clause would silently stop discriminating on half the supported systems.
	requested := "~/" + targetName
	wantCwd, err := filepath.EvalSymlinks(filepath.Join(home, targetName))
	if err != nil {
		t.Fatalf("realclaude: resolve change target realpath: %v", err)
	}

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

	// Both seeds must land BEFORE the daemon starts — the registries are loaded
	// once at startup, with no reload.
	seedBootstrapRegistry(t, home, changeWSBootstrapUUID)
	seedBoundConversation(t, home, changeWSConvID, changeWSBootstrapUUID, workdir)

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

	// Turn 1 — the control, drained all the way to turn_state{idle}. A red here is
	// about the daemon/child never coming up and implies nothing about the verb;
	// drainForCompletedTurn's milestone-specific fatals already say so, and its
	// standing unrecognized_message / rate_limited alarms are inherited on purpose.
	sealSendMessage(t, phone, initSend, 2, changeWSConvID, "m-1",
		fmt.Sprintf("Reply with a single short word. run=%d turn=1", nonce))
	drainForCompletedTurn(t, phone, initRecv, changeWSConvID, perTurnReplyBudget)

	// The verb. A red on this drain means no correlated conversation_updated
	// arrived against a quiescent wire — check the REJECT case first: a rejected
	// path replies with an `error` envelope, which drainForReply skips, so a
	// rejection is invisible here and looks exactly like an unwired route.
	sealEnvelope(t, phone, initSend, protocol.Envelope{
		ID:   3,
		Type: protocol.TypeChangeWorkspace,
		TS:   time.Now().UTC(),
		Payload: mustJSON(t, protocol.ChangeWorkspacePayload{
			ConversationID: changeWSConvID,
			Cwd:            requested,
		}),
	})
	changed := drainForReply(t, phone, initRecv, protocol.TypeConversationUpdated, 3, metaVerbReplyBudget)

	// The reply carries the $HOME-confined realpath, not the raw requested path.
	// drainForReply already correlated in_reply_to. A red here (or on the on-disk
	// read below) means the confine/store contract changed: if the fake tier's
	// TestRelayV2_ChangeWorkspace is also red the fault is the handler, if only
	// this one is red the fault is in resolveWorkspaceDir's $HOME resolution under
	// the tempdir HOME.
	assertConversationUpdated(t, "change_workspace", changed, changeWSConvID, func(p protocol.ConversationUpdatedPayload) {
		if p.Cwd == requested {
			t.Errorf("change_workspace reply Cwd = %q — the RAW requested path, not the resolved realpath", p.Cwd)
		}
		if p.Cwd != wantCwd {
			t.Errorf("change_workspace reply Cwd = %q, want the $HOME-confined realpath %q", p.Cwd, wantCwd)
		}
	})

	// The change is persisted. The handler eager-Saves BEFORE replying, so a read
	// straight after the reply is correct — a poll loop here would hide a
	// regression in exactly that ordering.
	if got := readConversationCwdOnDisk(t, home, changeWSConvID); got != wantCwd {
		t.Errorf("on-disk cwd for %q = %q, want %q", changeWSConvID, got, wantCwd)
	}

	// Turn 2 — the liveness assertion this file exists for. drainForAssistantReply's
	// built-in fatal blames the #854 fresh-daemon deadlock, which is the wrong
	// explanation here; this log line is the right one, and it lands directly above
	// that fatal in the output.
	t.Logf("turn 1 was green and the change_workspace round-trip landed: a failure on turn 2 means the VERB disturbed "+
		"the live supervised session (most likely a newly-added reader of the stored conv.Cwd on the live path), NOT the "+
		"fresh-daemon deadlock the next fatal names. The child still runs at the conversation's ORIGINAL cwd %q — this "+
		"turn is liveness, never cwd adoption.", workdir)
	sealSendMessage(t, phone, initSend, 4, changeWSConvID, "m-2",
		fmt.Sprintf("Reply with a single short word. run=%d turn=2", nonce))
	drainForAssistantReply(t, phone, initRecv, changeWSConvID, 2, perTurnReplyBudget)
}

// readConversationCwdOnDisk returns the cwd recorded for convID in the "test"
// instance registry (<home>/.pyry/test/conversations.json — the -pyry-name=test
// daemon). Fatals when the file or the row is absent: this test seeds the row
// before startup, so either absence is a failure, not an empty-registry case.
// Mirrors readConversationIDsOnDisk's anonymous-struct decode with cwd added.
func readConversationCwdOnDisk(t *testing.T, home, convID string) string {
	t.Helper()
	path := filepath.Join(home, ".pyry", "test", "conversations.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read conversations.json: %v", err)
	}
	var onDisk struct {
		Conversations []struct {
			ID  string `json:"id"`
			Cwd string `json:"cwd"`
		} `json:"conversations"`
	}
	if err := json.Unmarshal(raw, &onDisk); err != nil {
		t.Fatalf("decode on-disk registry: %v", err)
	}
	for _, c := range onDisk.Conversations {
		if c.ID == convID {
			return c.Cwd
		}
	}
	t.Fatalf("on-disk registry holds no row for %q (%d rows)", convID, len(onDisk.Conversations))
	return ""
}
