//go:build e2e_realclaude

package realclaude

// TestInteractiveStreamNewSessionRotatesAndSpawnsFresh is the #1174 deliverable:
// the real-claude proof that `new_session` on the production stream-json
// interactive runner (interactive_runner: "stream-json") rotates the bootstrap
// session id AND restarts the bound runner FRESH — a genuinely new live claude
// child under the rotated id, NOT a `--resume` of turn one's session — against a
// live claude.
//
// Stream new_session rotation is proven today only against fakeclaude
// (TestRelayV2_StreamNewSessionRotatesAndRestartsFresh, #1137): that test proves
// the routing and the on-disk rotation, but the REAL spawn — a fresh live child
// under a new id, not a `--resume` — is unproven, and a scripted fake cannot
// regress it (the recurring fake-green/real-red class, e.g. #949). Per the
// always-a-real-claude-gate policy (2026-07-08) this adds the third stream rung,
// alongside interactive_stream_liveness (#1153, one ungated turn) and
// interactive_stream_modal_resolution (#1154, one permission-gated turn).
//
// This is the real-claude CROSS of two proven tests:
//   - the fakeclaude stream sibling #1137 supplies the rotation-milestone structure
//     (registry-id rotation read, the new_session re-send/actuation loop, and the
//     session_transition{clear} drain — M2/M3 below), and
//   - the real-claude session-control sibling TestInteractiveSessionControlLiveness
//     (#1031) supplies the entire real-claude spine and every rotation helper this
//     test reuses (bootstrapRow, readBootstrapRowIfPresent, waitBootstrapID,
//     waitBootstrapIDSettled, uuidStemPattern, and the newSessionResend/rotateBudget/
//     idSettle* budgets) — but drives new_session on the DEFAULT (PTY) runner (a
//     /clear keystroke → claude self-rotates → the fsnotify watcher rotates the
//     registry).
//
// This test differs from #1031 in exactly one axis: the stream-json interactive
// runner. On that path new_session is a DAEMON-minted rotation (Pool.RotateForNewSession
// mints a fresh id; (*streamsup.Runner).RestartFresh re-arms first-run form so the
// next spawn is `claude --session-id <newID>` — a FRESH transcript, never `--resume`
// — see internal/streamsup/runner.go:322-398,583-604). No /clear, no watcher. That
// fresh-spawn form is what mints a brand-new <newID>.jsonl on disk, and that
// on-disk transcript is the real-claude-specific, word-independent observable at
// the heart of AC3 (§ the fresh-spawn observable below).
//
// THE LOAD-BEARING TENSION — why turn 2 asserts the ACK, not a phone-side delta.
// On the opt-in stream path the turn-drain gate forwards an event only when the
// producing runner's sink tag equals the active conversation's bound session id.
// The sink tag is fixed at runner CONSTRUCTION (== idBefore) and RestartFresh
// re-spawns the child IN PLACE — it never rebuilds the runner/Parser/sink — so a
// turn issued AFTER the rotation has its assistant_delta DROPPED at the gate and
// never reaches the phone; asserting a phone-side second-turn delta would HANG
// (documented in #1137's header ~lines 61-71, a production follow-up #1081, OUT OF
// SCOPE here). The fakeclaude sibling worked around this by asserting the fresh
// child's STDIN via a stdin-log tee — a hook real claude does not have. So turn 2
// asserts only its ACK (produced by the send_message handler on delivery, NOT
// gated by the drain sink tag, so it reaches the phone), proving the rotated
// session accepted/served the turn (Route resolved convID → the re-keyed pool id).
// The "fresh spawn, not --resume" proof is instead the on-disk transcript below.
//
// THE FRESH-SPAWN OBSERVABLE (AC3). M2's registry rotation and M3's session_transition
// are NOT real-claude-specific (fakeclaude #1137 regresses both). AC3 demands an
// observable a scripted fake could not regress and robust to claude's non-deterministic
// wording: after the rotation a fresh <idAfter>.jsonl transcript appears under the
// authenticated claude sessions dir, alongside the untouched turn-1 <idBefore>.jsonl.
// RestartFresh(idAfter) spawns `claude --session-id idAfter` (a fresh transcript);
// a `--resume idBefore` (the failure mode AC3 guards) would append to <idBefore>.jsonl
// and never mint an <idAfter>.jsonl. Two coexisting files ⟹ a fresh session, not a
// resume-into-old-file. The dir is located EMPIRICALLY (streamNewSessionTranscriptDir)
// rather than recomputed via DefaultClaudeSessionsDir, because the stream runner's
// child cwd is ResolveWorkdir(workdir) (canonicalCase, the #989 hazard) which can
// diverge from the encodeWorkdir reference — finding turn-1's transcript pins the
// real dir AND is the non-vacuity guard. The fresh no-memory / fresh-context angle
// is DEFERRED: proving "the fresh child cannot see turn one's content" needs the
// turn-2 reply, which is the very delta dropped at the gate (#1081); it is not
// observable on the stream path today.
//
// One daemon, one seeded bound conversation, one encrypted channel — a sequential
// spine (the #1028/#1031 shape) so the Noise receive nonce stays in lockstep (every
// drain helper decrypts every noise_msg in arrival order). fakephone.Client buffers
// inbound frames, so the session_transition the binary emits while the actuation
// loop is polling the registry (not reading the phone) is still available when the
// M3 drain reads it. No t.Parallel: WithWorktreeAuthenticated calls t.Setenv. The
// reused setup skips cleanly (exit 0) when claude / creds are absent, exactly like
// every sibling stream spec — the actual green requires a live claude (needs-real-claude).

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakephone"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakerelay"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// Fixed identifiers for the seeded state. Distinct from every other realclaude
// gate's fixed ids (7s #854, 8s/6s #1153, 9s #1154, a/b #1031) and from the
// in-flight sibling reservations (3/4 #1172, c/d #1173) so same-package files never
// collide on an identifier. Both are valid UUIDv4 stems (version nibble 4, variant
// nibble 8), matching the seeding convention. streamNewSessionBootstrapUUID is the
// bootstrap session's POOL id (pinned via seedBootstrapRegistry, which also makes
// the stream runner spawn `claude --session-id <it>` #839, so turn 1's transcript
// lands at <it>.jsonl); streamNewSessionConvID is the driving conversation, bound
// to it via seedBoundConversation.
const (
	streamNewSessionBootstrapUUID = "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee"
	streamNewSessionConvID        = "ffffffff-ffff-4fff-8fff-ffffffffffff"
)

func TestInteractiveStreamNewSessionRotatesAndSpawnsFresh(t *testing.T) {
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

	// Seed the deterministic bootstrap id + the driving conversation binding BEFORE
	// the daemon starts (the registry is loaded once at startup, no reload).
	seedBootstrapRegistry(t, home, streamNewSessionBootstrapUUID)
	seedBoundConversation(t, home, streamNewSessionConvID, streamNewSessionBootstrapUUID, workdir)

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

	// A per-run nonce so reruns differ — enough to defeat any accidental caching,
	// without asserting on content. reqID is a single monotonic counter: only the
	// turn-2 send_message id is load-bearing (drainForReply correlates the ack on
	// InReplyTo); the turn-1 id and the fire-and-forget new_session ids are cosmetic.
	nonce := time.Now().UnixNano()
	var reqID uint64 = 2

	// --- Turn 1: pre-rotation liveness (M1) ------------------------------------
	// Drives one real turn end-to-end on the stream runner and binds the reply
	// bridge, so new_session below resolves to the bound runner and the later
	// rotation is a rebind, not a first-bind. drainForCompletedTurn asserts a
	// non-empty assistant_delta FOLLOWED BY the terminal turn_state{idle} (no
	// content assertion — real claude's words are non-deterministic).
	sealSendMessage(t, phone, initSend, reqID, streamNewSessionConvID, "m-1",
		fmt.Sprintf("Reply with a single short word. run=%d turn=1", nonce))
	reqID++
	drainForCompletedTurn(t, phone, initRecv, streamNewSessionConvID, perTurnReplyBudget)

	// Baseline id (non-vacuity anchor for M2). Read after Turn 1 settles; a normal
	// turn appends to the same transcript and never rotates, so this is the value
	// the rotation must move away from (== streamNewSessionBootstrapUUID, whose
	// <id>.jsonl turn 1 wrote — the M5 dir anchor).
	idBefore := waitBootstrapID(t, home, idSettleTimeout)

	// --- new_session actuation (AC1) + on-disk rotation (M2, AC2) --------------
	// Fire-and-forget re-send until the registry bootstrap id rotates away from
	// idBefore. new_session drops silently on a not-yet-attached session, hence the
	// re-send; after Turn 1 the session is attached, so the first frame normally
	// actuates. Fresh envelope ids per send keep the send nonce advancing.
	regPath := filepath.Join(home, ".pyry", "test", "sessions.json")
	rotated := false
	rotateDeadline := time.Now().Add(rotateBudget)
	nextSend := time.Time{}
	for !rotated && time.Now().Before(rotateDeadline) {
		if time.Now().After(nextSend) {
			sealEnvelope(t, phone, initSend, protocol.Envelope{
				ID:   reqID,
				Type: protocol.TypeNewSession,
				TS:   time.Now().UTC(),
			})
			reqID++
			nextSend = time.Now().Add(newSessionResend)
		}
		if row, ok := readBootstrapRowIfPresent(home); ok && row.ID != "" && row.ID != idBefore {
			rotated = true
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if !rotated {
		data, _ := os.ReadFile(regPath)
		t.Fatalf("M2 (AC1/AC2): the bootstrap id never rotated away from %q within %s — the stream "+
			"fresh-restart did not actuate (the frame dropped on a detached session, RestartFresh is inert, "+
			"or the active cursor was never stamped)\nsessions.json:\n%s", idBefore, rotateBudget, data)
	}
	// Settle before Turn 2: a re-send may have queued extra new_session frames (real
	// claude, daemon-minted rotation is fast but the loop re-sends at 1s), so wait
	// until no rotation is still in flight. The settled id is the rotation anchor.
	idAfter := waitBootstrapIDSettled(t, home, idSettleQuiesce, idSettleTimeout)
	if idAfter == idBefore {
		t.Fatalf("M2: settled id %q equals the baseline — no rotation occurred", idAfter)
	}
	if !uuidStemPattern.MatchString(idAfter) {
		t.Errorf("M2: rotated id %q does not match the UUIDv4 stem pattern", idAfter)
	}
	t.Logf("M2: registry rotated %s → %s — the id rotated and the bound runner restarted fresh", idBefore, idAfter)

	// --- session_transition (M3, AC2 "the client observes …") ------------------
	// notifyTransition(ReasonClear) fans a session_transition to every interactive
	// conn — the phone-side observable of the fresh-session break. drainForControlEvent
	// returns the FIRST session_transition (Fatals on any TypeError); it is the
	// rotation FROM idBefore even in the rare double-actuation case, so prev==idBefore
	// always holds. Assert new is a valid stem != idBefore rather than tying it to
	// idAfter (a second stacked rotation could advance idAfter past this transition's
	// new id).
	transition := drainForControlEvent(t, phone, initRecv, protocol.TypeSessionTransition, rotateBudget)
	var st protocol.SessionTransitionPayload
	if err := json.Unmarshal(transition.Payload, &st); err != nil {
		t.Fatalf("decode session_transition payload: %v", err)
	}
	if st.Reason != "clear" {
		t.Errorf("session_transition Reason: got %q, want %q", st.Reason, "clear")
	}
	if st.PreviousSessionID != idBefore {
		t.Errorf("session_transition PreviousSessionID: got %q, want %q", st.PreviousSessionID, idBefore)
	}
	if st.NewSessionID == idBefore || !uuidStemPattern.MatchString(st.NewSessionID) {
		t.Errorf("session_transition NewSessionID %q is not a fresh UUIDv4 stem distinct from the baseline %q",
			st.NewSessionID, idBefore)
	}
	if st.ConversationID != streamNewSessionConvID {
		t.Errorf("session_transition ConversationID: got %q, want %q", st.ConversationID, streamNewSessionConvID)
	}
	t.Logf("M3: observed session_transition{clear, %s → %s} for the conversation — the client saw the rotation",
		st.PreviousSessionID, st.NewSessionID)

	// --- Turn 2: the rotated session serves a subsequent turn (M4, AC3) --------
	// send_message #2 to the (now rebound) conversation → await its ack: Route
	// resolves streamNewSessionConvID → CurrentSessionID == idAfter → Pool.Lookup HIT
	// on the re-keyed pool (the #1125 routing), proving the rotated session accepted
	// the turn. Generous budget: the fresh child cold-spawns (spawn + model load) and
	// the inbound queue retries delivery until it is stdin-ready. We deliberately do
	// NOT drain a phone-side assistant_delta for turn 2 — on the opt-in stream path
	// its delta is dropped at the gate (see the header); asserting one would HANG.
	turn2ReqID := reqID
	reqID++
	sealSendMessage(t, phone, initSend, turn2ReqID, streamNewSessionConvID, "m-2",
		fmt.Sprintf("Reply with a single short word. run=%d turn=2", nonce))
	drainForReply(t, phone, initRecv, protocol.TypeAck, turn2ReqID, perTurnReplyBudget)
	t.Logf("M4: the rotated session accepted the subsequent turn (ack for send_message #%d)", turn2ReqID)

	// --- Fresh-spawn observable (M5, AC3): a fresh <idAfter>.jsonl on disk ------
	// Locate the dir empirically by finding turn-1's <idBefore>.jsonl (non-vacuity:
	// turn 1 must have written a transcript), then require a fresh <idAfter>.jsonl to
	// appear alongside it. A fresh file under the rotated id ⟹ RestartFresh spawned a
	// fresh child (`--session-id idAfter`), NOT a `--resume idBefore` (which would
	// append to the old file and never mint <idAfter>.jsonl). Fakeclaude cannot
	// regress this — it uses a stdin-log tee, not real claude transcripts.
	dir := streamNewSessionTranscriptDir(t, home, idBefore, rotateBudget)
	requireTranscriptAppears(t, dir, idAfter, rotateBudget)
}

// --- fresh-spawn transcript observable (the only genuinely new code) ---------

// streamNewSessionTranscriptDir polls <home>/.claude/projects/<*>/ for the subdir
// containing <id>.jsonl and returns that dir. DefaultClaudeSessionsDir(workdir)
// (internal/sessions/reconcile.go) is the production reference for where claude
// writes <uuid>.jsonl, but the stream runner's child cwd is ResolveWorkdir(workdir)
// (which applies canonicalCase — the #989 hazard) so recomputing the encoded folder
// name in the test is fragile; finding the real dir empirically sidesteps that.
// Called with idBefore, this is BOTH the non-vacuity guard (turn 1 must have written
// a transcript) AND the pin for M5's real dir. On timeout, Fatal listing the
// projects tree so a dir-mismatch is self-diagnosing (Open question #1174: validate
// where stream-json claude writes on the first live run).
func streamNewSessionTranscriptDir(t *testing.T, home, id string, timeout time.Duration) string {
	t.Helper()
	projects := filepath.Join(home, ".claude", "projects")
	target := id + ".jsonl"
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		entries, _ := os.ReadDir(projects)
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			dir := filepath.Join(projects, e.Name())
			if _, err := os.Stat(filepath.Join(dir, target)); err == nil {
				return dir
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("stream new_session: turn-1 transcript %s never appeared under %s within %s — the stream "+
		"runner did not write claude's session transcript where expected (relocate the dir), so the M5 "+
		"fresh-spawn assertion cannot anchor\ntree:\n%s", target, projects, timeout, projectsTree(projects))
	return ""
}

// requireTranscriptAppears polls dir for <id>.jsonl until it exists or timeout.
// Called with idAfter, it proves the post-rotation child was a fresh spawn under the
// rotated id (a fresh transcript), not a `--resume` of the old one (which would
// append to <idBefore>.jsonl and never mint <idAfter>.jsonl). On timeout, Fatal
// listing dir's contents.
func requireTranscriptAppears(t *testing.T, dir, id string, timeout time.Duration) {
	t.Helper()
	target := id + ".jsonl"
	path := filepath.Join(dir, target)
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			t.Logf("M5: fresh transcript %s appeared alongside the turn-1 transcript — a fresh spawn under the "+
				"rotated id, not a --resume of the old session", target)
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("M5 (AC3): fresh transcript %s never appeared in %s within %s — RestartFresh did not spawn a "+
		"fresh child under the rotated id (or it --resumed the old transcript, appending to <idBefore>.jsonl "+
		"and never minting <idAfter>.jsonl)\ntree:\n%s", target, dir, timeout, projectsTree(dir))
}

// projectsTree returns a newline-joined listing of every path under root, for
// self-diagnosing a transcript-dir mismatch in a Fatal. Best-effort: a walk error
// is folded into the listing rather than masking the original failure.
func projectsTree(root string) string {
	var b strings.Builder
	_ = filepath.WalkDir(root, func(path string, _ fs.DirEntry, err error) error {
		if err != nil {
			fmt.Fprintf(&b, "%s: %v\n", path, err)
			return nil
		}
		fmt.Fprintln(&b, path)
		return nil
	})
	return b.String()
}
