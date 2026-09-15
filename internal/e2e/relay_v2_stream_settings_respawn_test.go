//go:build e2e

package e2e

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
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

// TestE2E_SettingsChangeSurvivesTheNextRespawn is #2446 AC 3: a session that had
// a live settings change survives its next child exit.
//
// # The defect
//
// Pool.UpdateSettings recomposes argv as Session.spawnBase + claudeSettingsArgs
// and installs it on the adapter, which forwarded it to the runner verbatim. The
// base carries the "--session-id <id>" Pool.buildSession bakes into every
// per-conversation session, and the runner injects its own id flag per spawn —
// so the first respawn after any settings change spawned
// "--session-id X … --resume X", which claude refuses outright. Observed on
// pyrybox 2026-09-15: every respawn died in a quarter second and the desktop's
// queued messages sat unanswered until the daemon was restarted.
//
// # Why each step is here
//
// The subject is a PHONE-CREATED conversation because that is the only session
// kind with a baked id — the bootstrap's spawnBase carries none, so it cannot
// reproduce this at all.
//
// The transcript fixture is what makes the respawn take the RESUME form.
// streamsup decides the id flag per spawn from a by-id probe of the directory
// claude writes <id>.jsonl into, and stream-mode fakeclaude establishes no
// transcript anywhere, so without the fixture every spawn takes the create form,
// the composed pair is the harmless "--session-id" twice, and this test would
// pass on the unfixed tree. It is seeded before the first turn, so spawn 1 is
// itself a resume — one id flag, which the fake accepts.
//
// The forced exit is an IDLE EVICTION rather than a SIGKILL: the control plane
// reports a child_pid for the bootstrap runner only, so a per-conversation child
// has no pid on any surface a test can read, and the incident report names idle
// eviction as one of the two ways a live session meets a respawn.
//
// # What makes it red on main rather than merely slow
//
// The refusal is the fake's, and it is deterministic: refusesIDPair rejects the
// composed argv before the child reads a byte of stdin, exiting 1 with claude's
// own line. So the reactivated turn is never served and the drain below reaches
// its deadline with no delta — no timing window decides it.
func TestE2E_SettingsChangeSurvivesTheNextRespawn(t *testing.T) {
	const (
		initialUUID = "24460000-0000-4000-8000-000000000001"
		primeText   = "e2e-2446-prime\n"
		wakeText    = "e2e-2446-wake:after-settings\n"
		wakeNeedle  = "e2e-2446-wake:after-settings"
	)

	home := shortHome(t)
	fr := fakerelay.New(relayTestLogger())
	t.Cleanup(func() { _ = fr.Close() })
	relayURL := fr.URL() + "/v2/server"

	pairPayload, err := paireddevice.Setup(paireddevice.Config{
		Home:                   home,
		InstanceName:           "test",
		Relay:                  relayURL,
		DeviceName:             "phone-a",
		AllowRemotePermissions: false,
	})
	if err != nil {
		t.Fatalf("setup paired device: %v", err)
	}
	pubKey, err := base64.StdEncoding.DecodeString(pairPayload.ServerStaticPubkey)
	if err != nil {
		t.Fatalf("decode server static pubkey: %v", err)
	}

	// The idle window carries the same constraint the sibling per-conversation
	// test documents: it must leave room for spawn plus three msgqueue retries,
	// because Session.runActive arms the idle timer on entering active and turn
	// activity does not reset it. 8s there, 8s here, for that reason.
	h := startPerConvHarness(t, home, initialUUID, relayURL, "-pyry-idle-timeout=8s")

	regPath := filepath.Join(home, ".pyry", "test", "sessions.json")
	convPath := filepath.Join(home, ".pyry", "test", "conversations.json")

	phone, initSend, initRecv := dialHelloPhone(t, home, fr, pubKey, pairPayload.Token)

	conv := createConversationViaPhone(t, phone, initSend, initRecv, 2)
	bound := boundSessionID(t, convPath, conv)

	seedClaudeTranscript(t, home, bound)

	// Spawn 1, and the precondition for everything below: a session that is
	// genuinely active with a live child is what "a live settings change" means.
	activateViaTurn(t, phone, initSend, initRecv, regPath, conv, bound, 3, "m-prime", primeText)

	// The change. A permission mode is deliverable in band, so this drives the
	// SetSpawnArgs branch — the one the incident took, and the one whose whole
	// point is that the install outlives the child that is running now.
	mode := "acceptEdits"
	settings := protocol.Envelope{
		ID:   4,
		Type: protocol.TypeSetSessionSettings,
		TS:   time.Now().UTC(),
		Payload: mustJSON(t, protocol.SetSessionSettingsPayload{
			SessionID:      bound,
			PermissionMode: &mode,
		}),
	}
	settingsRaw, err := json.Marshal(settings)
	if err != nil {
		t.Fatalf("marshal set_session_settings envelope: %v", err)
	}
	settingsCT, err := initSend.Encrypt(settingsRaw)
	if err != nil {
		t.Fatalf("seal set_session_settings envelope: %v", err)
	}
	sendNoiseMsg(t, phone, settingsCT)
	// The reply is the daemon's own witness that the change persisted AND that
	// the recomposed argv was installed — both happen before it is emitted. A
	// test that killed the child without waiting for it could be racing the
	// install and would prove nothing about the argv the respawn carries.
	drainForReply(t, phone, initRecv, protocol.TypeSessionSettingsUpdated, 4, 15*time.Second)

	// The forced exit.
	waitForSessionState(t, regPath, bound, "evicted", 20*time.Second)

	// And the respawn, under the argv the settings change installed.
	wake := protocol.Envelope{
		ID:   5,
		Type: protocol.TypeSendMessage,
		TS:   time.Now().UTC(),
		Payload: mustJSON(t, protocol.SendMessagePayload{
			ConversationID: conv,
			MessageID:      "m-wake",
			Text:           wakeText,
		}),
	}
	wakeRaw, err := json.Marshal(wake)
	if err != nil {
		t.Fatalf("marshal send_message envelope: %v", err)
	}
	wakeCT, err := initSend.Encrypt(wakeRaw)
	if err != nil {
		t.Fatalf("seal send_message envelope: %v", err)
	}
	sendNoiseMsg(t, phone, wakeCT)
	// The ack is not the proof — the send handler acks on accept-into-backlog,
	// well before any child exists to serve the turn.
	drainForReply(t, phone, initRecv, protocol.TypeAck, 5, 15*time.Second)

	// The proof: the conversation answers. On the unfixed tree the respawned
	// child is refused at argv parse on every attempt, so nothing ever echoes.
	waitForConversationDelta(t, phone, initRecv, conv, wakeNeedle, 25*time.Second, h)
}

// seedClaudeTranscript writes the transcript claude would have written for this
// session into the directory the daemon's own per-spawn probe reads, so every
// later spawn of that session takes the resume form.
//
// The directory is derived with claudeSessionsDir, which mirrors the production
// derivation for a daemon whose workdir is its home — the harness default. A
// mis-derived directory is not a silent failure here: the probe would read
// "absent", every spawn would take the create form, and the test would pass
// against the very defect it exists to catch. So it also asserts the file landed
// somewhere the daemon could see at all.
func seedClaudeTranscript(t *testing.T, home, sessionID string) {
	t.Helper()
	dir := claudeSessionsDir(home)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("create claude sessions dir %s: %v", dir, err)
	}
	path := filepath.Join(dir, sessionID+".jsonl")
	// One line of the shape claude appends. Nothing reads its content — stream
	// mode opens no transcript — so what matters is that the file exists under
	// the id the probe stats.
	line := `{"type":"summary","summary":"e2e-2446 seeded transcript"}` + "\n"
	if err := os.WriteFile(path, []byte(line), 0o600); err != nil {
		t.Fatalf("seed transcript %s: %v", path, err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("seeded transcript %s is not readable: %v", path, err)
	}
}

// waitForConversationDelta drains the phone until an assistant_delta scoped to
// convID carries needle, failing with the daemon's stderr tail on timeout —
// which is where the fake's refusal line lands, so a run that failed for this
// ticket's reason says so in the failure message.
//
// turn_state frames and any other conversation's events are skipped rather than
// treated as progress: the claim is that THIS conversation answered.
func waitForConversationDelta(t *testing.T, phone *fakephone.Client, recv *noise.CipherState,
	convID, needle string, budget time.Duration, h *Harness) {
	t.Helper()
	deadline := time.Now().Add(budget)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			t.Fatalf("conversation %s never answered %q within %s — the respawn under the recomposed argv never served a turn\ndaemon stderr:\n%s",
				convID, needle, budget, stderrTail(h, 6000))
		}
		rawFrame, err := phone.ReceiveBytes(remaining)
		if err != nil {
			if errors.Is(err, fakephone.ErrReceiveTimeout) {
				continue // re-loop into the deadline branch above
			}
			t.Fatalf("phone receive (drain): %v", err)
		}
		var inner protocol.InnerFrameV2
		if err := json.Unmarshal(rawFrame, &inner); err != nil {
			t.Fatalf("phone decode inner frame (drain): %v", err)
		}
		// Skip on the INNER type only, never on a decrypted envelope: the receive
		// CipherState opens sealed frames in arrival order, so dropping a noise_msg
		// undecrypted desynchronises the nonce and every later open fails.
		if inner.Type != protocol.TypeNoiseMsg {
			continue
		}
		got := decryptInnerEnvelope(t, inner, recv)
		if got.Type != protocol.TypeAssistantDelta {
			continue
		}
		var delta protocol.AssistantDeltaPayload
		if err := json.Unmarshal(got.Payload, &delta); err != nil {
			t.Fatalf("phone decode assistant_delta payload: %v", err)
		}
		if delta.ConversationID == convID && strings.Contains(delta.Text, needle) {
			return
		}
	}
}
