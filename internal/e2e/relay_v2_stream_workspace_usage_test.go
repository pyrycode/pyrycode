//go:build e2e

package e2e

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakephone"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakerelay"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/paireddevice"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// TestRelayV2_StreamSessionSettingsReadsEachWorkspacesOwnTranscript is #2423's
// first acceptance criterion against a REAL daemon: two conversations spawned in
// two different working directories, NEITHER of them the daemon's, each report
// their own used-token count in session_settings.
//
// Only a full-stack run can carry it. The defect was never in the reader — given a
// folder, snapshotUsageFor faithfully reads it — it was that the daemon handed one
// folder, derived once at start-up from its own working directory, to every
// session. No unit table over the reader can tell a daemon that resolves the folder
// per session from one that does not, because both are handed whatever the test
// passes. What decides it is Pool.buildSession preferring the conversation's spawn
// dir, claude encoding its resolved cwd into the projects folder name, and the
// wiring in startRelayV2 — three layers a unit test replaces rather than exercises.
//
// THE FIXTURE IS DISCRIMINATING IN THREE DIRECTIONS, which is why plain equality
// assertions suffice:
//
//   - Neither workspace is the daemon's own directory, so the pre-#2423 daemon
//     reports 0 for BOTH conversations — the measured symptom, "Context: 0%".
//   - The two counts differ, so a daemon that resolved one folder for every session
//     (any single-folder fallback, including one to the daemon's own) reports one
//     conversation's number twice.
//   - The two session ids differ and each transcript is planted ONLY under its own
//     workspace, so a daemon that took the folder from one session and the id from
//     another misses both files and reports 0.
//
// No turn is driven and no rider is armed, unlike
// TestRelayV2_StreamSessionSettingsReportsTheObservedWindow: the used count comes
// off the planted transcript alone, and the window this spec does not assert is
// #2107's property, already proven there. Planting rather than letting claude write
// is not a shortcut either — stream-mode fakeclaude opens no transcript of its own,
// which is what makes a planted file the only transcript in any of these specs.
func TestRelayV2_StreamSessionSettingsReadsEachWorkspacesOwnTranscript(t *testing.T) {
	const (
		initialUUID = "11111111-1111-4111-8111-111111111111"
		createAID   = uint64(2430)
		createBID   = uint64(2431)
		settingsA1  = uint64(2432)
		settingsB1  = uint64(2433)
		settingsA2  = uint64(2434)
		settingsB2  = uint64(2435)

		// Distinct sums, both far from zero and from each other.
		wantUsedA = 4321
		wantUsedB = 8765
	)

	home := shortHome(t)

	// The two workspaces, created before they are requested so the daemon's
	// confine-and-create step is not also the step this spec depends on for its
	// path derivation. Both sit under $HOME, which resolveSpawnDir requires.
	workspaceA := filepath.Join(home, "workspace-a")
	workspaceB := filepath.Join(home, "workspace-b")
	for _, dir := range []string{workspaceA, workspaceB} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("mkdir workspace %s: %v", dir, err)
		}
	}

	fr := fakerelay.New(relayTestLogger())
	t.Cleanup(func() { _ = fr.Close() })
	relayURL := fr.URL() + "/v2/server"

	payloadA, err := paireddevice.Setup(paireddevice.Config{
		Home:                   home,
		InstanceName:           "test",
		Relay:                  relayURL,
		DeviceName:             "phone-a",
		AllowRemotePermissions: false,
	})
	if err != nil {
		t.Fatalf("setup paired device: %v", err)
	}
	pubKey, err := base64.StdEncoding.DecodeString(payloadA.ServerStaticPubkey)
	if err != nil {
		t.Fatalf("decode server static pubkey: %v", err)
	}

	h := StartStreamInteractiveWithRelay(t, home, initialUUID, relayURL)
	t.Cleanup(func() { h.Stop(t) })

	serverID := readPersistedServerID(t, home)
	waitBinaryHello(t, fr, serverID)

	dialCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	phoneA, err := fakephone.Dial(dialCtx, fr.URL(), serverID, payloadA.Token, "phone-a")
	if err != nil {
		t.Fatalf("phone A dial: %v", err)
	}
	t.Cleanup(func() { _ = phoneA.Close() })
	sendA, recvA := driveHandshakeToOpenDaemonInteractive(t, phoneA, pubKey, payloadA.Token)

	sealSend := func(env protocol.Envelope) {
		t.Helper()
		raw, err := json.Marshal(env)
		if err != nil {
			t.Fatalf("marshal %s envelope (id=%d): %v", env.Type, env.ID, err)
		}
		cipher, err := sendA.Encrypt(raw)
		if err != nil {
			t.Fatalf("seal %s envelope (id=%d): %v", env.Type, env.ID, err)
		}
		sendNoiseMsg(t, phoneA, cipher)
	}
	// Correlate on in_reply_to rather than on type, so a TypeError refusal is
	// reported as the regression it is instead of being drained past into a
	// timeout. Unsolicited frames interleave freely.
	awaitReply := func(want uint64) protocol.Envelope {
		t.Helper()
		deadline := time.Now().Add(20 * time.Second)
		for {
			remaining := time.Until(deadline)
			if remaining <= 0 {
				t.Fatalf("phone A never received a reply to request id=%d", want)
			}
			env := decryptInnerEnvelope(t, readInnerFrame(t, phoneA, remaining), recvA)
			if env.InReplyTo != nil && *env.InReplyTo == want {
				return env
			}
		}
	}
	createIn := func(reqID uint64, cwd string) string {
		t.Helper()
		sealSend(protocol.Envelope{
			ID:      reqID,
			Type:    protocol.TypeCreateConversation,
			TS:      time.Now().UTC(),
			Payload: mustJSON(t, protocol.CreateConversationPayload{Cwd: &cwd}),
		})
		reply := awaitReply(reqID)
		if reply.Type != protocol.TypeConversationCreated {
			t.Fatalf("create_conversation(%q) reply Type = %q, want %q (payload %s)",
				cwd, reply.Type, protocol.TypeConversationCreated, string(reply.Payload))
		}
		var got protocol.ConversationCreatedPayload
		if err := json.Unmarshal(reply.Payload, &got); err != nil {
			t.Fatalf("decode conversation_created payload: %v", err)
		}
		if got.ID == "" {
			t.Fatalf("conversation_created for %q carries an empty id", cwd)
		}
		return got.ID
	}
	readSettings := func(reqID uint64, convID string) protocol.SessionSettingsPayload {
		t.Helper()
		sealSend(protocol.Envelope{
			ID:      reqID,
			Type:    protocol.TypeRequestSessionSettings,
			TS:      time.Now().UTC(),
			Payload: mustJSON(t, protocol.RequestSessionSettingsPayload{ConversationID: convID}),
		})
		reply := awaitReply(reqID)
		if reply.Type != protocol.TypeSessionSettings {
			t.Fatalf("reply Type = %q, want %q (payload %s)", reply.Type, protocol.TypeSessionSettings, string(reply.Payload))
		}
		var got protocol.SessionSettingsPayload
		if err := json.Unmarshal(reply.Payload, &got); err != nil {
			t.Fatalf("decode session_settings payload: %v", err)
		}
		return got
	}

	convA := createIn(createAID, workspaceA)
	convB := createIn(createBID, workspaceB)

	// The session ids are learned from the daemon rather than assumed, because they
	// are what name the transcripts the daemon will resolve: the reader stats
	// <workspace sessions dir>/<id>.jsonl, so planting either file under the wrong
	// name or the wrong folder would leave the count at zero and the spec asserting
	// nothing.
	sessionA := readSettings(settingsA1, convA).SessionID
	sessionB := readSettings(settingsB1, convB).SessionID
	if sessionA == "" || sessionB == "" {
		t.Fatalf("session ids = %q / %q, want two non-empty ids to plant transcripts under", sessionA, sessionB)
	}
	if sessionA == sessionB {
		t.Fatalf("both conversations report session id %q — two conversations must hold two sessions", sessionA)
	}

	plantUsage(t, claudeSessionsDirFor(home, workspaceA), sessionA, wantUsedA)
	plantUsage(t, claudeSessionsDirFor(home, workspaceB), sessionB, wantUsedB)

	gotA := readSettings(settingsA2, convA)
	gotB := readSettings(settingsB2, convB)
	if gotA.UsedTokens != wantUsedA {
		t.Errorf("workspace-a used_tokens = %d, want %d — 0 means the daemon still resolves one folder for every "+
			"session (the #2423 defect); %d means it answered workspace-b's transcript",
			gotA.UsedTokens, wantUsedA, wantUsedB)
	}
	if gotB.UsedTokens != wantUsedB {
		t.Errorf("workspace-b used_tokens = %d, want %d — 0 means the daemon still resolves one folder for every "+
			"session (the #2423 defect); %d means it answered workspace-a's transcript",
			gotB.UsedTokens, wantUsedB, wantUsedA)
	}
}

// claudeSessionsDirFor is claudeSessionsDir for a workdir that is NOT the daemon's
// $HOME — the projects folder claude keys a session spawned in workdir under. Same
// derivation and same symlink resolution as its sibling, which is what #1655
// measured against the folder claude actually writes; the base stays home because
// that is the daemon's $HOME, while the ENCODED path is the session's own cwd.
func claudeSessionsDirFor(home, workdir string) string {
	resolved, err := filepath.EvalSymlinks(workdir)
	if err != nil {
		resolved = workdir
	}
	return filepath.Join(home, ".claude", "projects", encodeWorkdir(resolved))
}

// plantUsage writes a minimal two-line transcript for sessionID under dir whose
// single usage-bearing entry sums to used, creating dir if needed. The model is
// left unnamed: this spec asserts only the used count, and an unnamed model is what
// keeps the window at contextwindow's default rather than depending on a report no
// child made here.
func plantUsage(t *testing.T, dir, sessionID string, used int) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir claude sessions dir %s: %v", dir, err)
	}
	line := `{"type":"user","message":{"role":"user","content":"hi"}}` + "\n" +
		`{"type":"assistant","message":{"role":"assistant","stop_reason":"end_turn",` +
		`"content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":` +
		strconv.Itoa(used) + `,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":0}}}` + "\n"
	path := filepath.Join(dir, sessionID+".jsonl")
	if err := os.WriteFile(path, []byte(line), 0o600); err != nil {
		t.Fatalf("write transcript %s: %v", path, err)
	}
}
