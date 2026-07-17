//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakephone"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakerelay"
	"github.com/pyrycode/pyrycode/internal/noise"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// wantSettingsMalformedMsg is the fixed reject message handleSetSessionSettings
// emits for an invalid model/effort (internal/relay/v2session.go:2709,
// msgSettingsMalformed). That constant is package-private to internal/relay, so
// this e2e asserts the literal string.
const wantSettingsMalformedMsg = "malformed set_session_settings request"

// TestRelayV2_SetSessionSettings is the fake-daemon e2e for the phone-driven
// set_session_settings v2 control verb (#845, split from #962). It proves the
// interactive-gated request/reply path end-to-end over the encrypted v2 wire —
// including the wire-boundary validation (validModel / validEffort) that keeps an
// untrusted model/effort out of the claude spawn argv — rather than only at the
// handleSetSessionSettings unit tier.
//
// Two phases run against ONE daemon on ONE interactive conn:
//
//   - Phase 1 (AC-1/AC-2, valid): a sealed set_session_settings{opus,high,true}
//     frame yields a session_settings_updated reply correlated on InReplyTo, and
//     the change lands in the on-disk registry (bootstrap row == opus/high/true),
//     matching the shape internal/sessions/pool_update_settings_restart_test.go
//     asserts at its tier.
//
//   - Phase 2 (AC-3, invalid, table-driven): a well-formed-but-out-of-set model
//     and effort each yield a TypeError / protocol.malformed reply with the fixed
//     message, persist NOTHING (disk still shows the Phase-1 values), and never
//     leak the sentinel value into the reply frame or the daemon logs.
//
// NON-VACUITY is structural, not incidental: the invalid values are well-formed
// JSON strings, so json.Unmarshal into *string CANNOT fail — the malformed reply
// cannot originate at the decode step; the conn is interactive, so it cannot
// originate at the capability gate; therefore it originates at validModel /
// validEffort, the argv-injection boundary. Phase 1 proves the SAME decode path
// admits a valid model, so the divergence is provably at the validator. A JSON
// number for model (decode-fail) or a shape-valid out-of-set model like "gpt-4"
// (validModel is a shape check, not an allowlist — it would PASS) would each
// false-pass; both are deliberately avoided.
func TestRelayV2_SetSessionSettings(t *testing.T) {
	const initialUUID = "77777777-7777-4777-8777-777777777777"

	home := shortHome(t)

	// Pair one interactive device: yields the bearer token and the responder
	// static pubkey the phone pins.
	r := RunBareIn(t, home, "pair", "-pyry-name=test", "--name=phone-a")
	if r.ExitCode != 0 {
		t.Fatalf("pyry pair exit=%d\nstdout:\n%s\nstderr:\n%s", r.ExitCode, r.Stdout, r.Stderr)
	}
	payload := decodePairPayload(t, r.Stdout)
	pubKey, err := base64.StdEncoding.DecodeString(payload.ServerStaticPubkey)
	if err != nil {
		t.Fatalf("decode server static pubkey: %v", err)
	}

	// Align the sessions dir to the daemon's COMPUTED path and pre-create
	// <initialUUID>.jsonl BEFORE the daemon starts. The bootstrap id starts at
	// initialUUID deterministically — seedBootstrapRegistry warm-starts it and the
	// daemon spawns claude with --session-id (#839), no startup scan (the #642 recipe). This
	// makes the frame's SessionID known ahead of time and the persistence
	// assertions non-vacuous. Unlike the new_session e2e this test does NOT rotate,
	// so no PYRY_FAKE_CLAUDE_CLEAR_ROTATES / stdin-log machinery.
	sessionsDir := claudeSessionsDir(home)
	if err := os.MkdirAll(sessionsDir, 0o700); err != nil {
		t.Fatalf("mkdir sessions dir: %v", err)
	}
	initialJSONL := filepath.Join(sessionsDir, initialUUID+".jsonl")
	if err := os.WriteFile(initialJSONL, []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("pre-create initial jsonl: %v", err)
	}

	tmp := t.TempDir()
	neverCreated := filepath.Join(tmp, "rotate.trigger.never-created")
	stdinLog := filepath.Join(tmp, "fakeclaude-stdin.log")

	fr := fakerelay.New(relayTestLogger())
	t.Cleanup(func() { _ = fr.Close() })

	// PYRY_MOBILE_V2=1 wires the v2 manager and therefore SettingsUpdater
	// (settingsUpdaterAdapter over *sessions.Pool). Without it the handler's
	// nil-seam guard fires and the reply is server.binary_offline / "unavailable".
	h := StartRotationWithRelay(t, home, sessionsDir, initialUUID, neverCreated,
		stdinLog, fr.URL()+"/v2/server",
		"PYRY_MOBILE_V2=1",
	)
	t.Cleanup(func() { h.Stop(t) })

	regPath := filepath.Join(home, ".pyry", "test", "sessions.json")

	serverID := readPersistedServerID(t, home)
	waitBinaryHello(t, fr, serverID)

	dialCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	phone, err := fakephone.Dial(dialCtx, fr.URL(), serverID, payload.Token, "phone-a")
	if err != nil {
		t.Fatalf("phone dial: %v", err)
	}
	t.Cleanup(func() { _ = phone.Close() })

	// Interactive handshake — the capability handleSetSessionSettings requires.
	// The negative cases below reach validModel / validEffort only on an
	// interactive conn (a non-interactive conn is inert), so this grant is what
	// makes AC-3's non-vacuity hold.
	initSend, initRecv := driveHandshakeToOpenDaemonInteractive(t, phone, pubKey, payload.Token)

	// Precondition: the Pool must have registered the bootstrap at initialUUID
	// before the first frame, else UpdateSettings returns ErrSessionNotFound →
	// session.not_found instead of session_settings_updated. This wait also makes
	// the persistence assertions non-vacuous.
	waitForBootstrapID(t, regPath, initialUUID, 5*time.Second)

	// ---- Phase 1: valid (AC-1, AC-2) ------------------------------------------
	// The phone conn is single-writer and the verb is strict request/reply: send
	// one frame, read its one correlated reply, before sending the next — keeping
	// the Noise send/recv nonce sequences aligned.
	const reqValid uint64 = 41
	sendSettingsFrame(t, phone, initSend, reqValid, protocol.SetSessionSettingsPayload{
		SessionID: initialUUID,
		Model:     ptr("opus"),
		Effort:    ptr("high"),
		YOLO:      ptr(true),
	})
	// AC-1: readSettingsReply returns only the frame whose InReplyTo == reqValid.
	reply := readSettingsReply(t, phone, initRecv, reqValid, 5*time.Second)
	if reply.Type != protocol.TypeSessionSettingsUpdated {
		t.Fatalf("valid phase: reply Type = %q, want %q (payload=%s)",
			reply.Type, protocol.TypeSessionSettingsUpdated, reply.Payload)
	}
	var updated protocol.SessionSettingsUpdatedPayload
	if err := json.Unmarshal(reply.Payload, &updated); err != nil {
		t.Fatalf("valid phase: decode session_settings_updated payload: %v", err)
	}
	if updated.SessionID != initialUUID {
		t.Errorf("valid phase: reply SessionID = %q, want %q", updated.SessionID, initialUUID)
	}

	// AC-2: the change is persisted on disk (the handler persists atomically before
	// forwarding the reply, so this normally matches on the first poll).
	waitBootstrapSettings(t, regPath, "opus", "high", true, 3*time.Second)

	// ---- Phase 2: invalid, table-driven (AC-3) --------------------------------
	// Each value is a well-formed JSON string that passes json.Unmarshal and
	// reaches validModel / validEffort, and is rejected THERE — not short-circuited
	// by decode or the capability gate. The sentinels are unique strings that
	// cannot collide with any legitimate argv/log token — explicitly NOT
	// --dangerously-skip-permissions, which the valid YOLO:true spawn emits.
	cases := []struct {
		name     string
		payload  protocol.SetSessionSettingsPayload
		sentinel string
	}{
		{
			// First byte '-' is non-alnum: the exact argv-injection shape validModel
			// bars (a value posing as a claude CLI flag).
			name:     "model_argv_injection",
			payload:  protocol.SetSessionSettingsPayload{SessionID: initialUUID, Model: ptr("--pyry-e2e-not-a-model")},
			sentinel: "--pyry-e2e-not-a-model",
		},
		{
			// Out of the closed effort enum {"",low,medium,high,xhigh,max}.
			name:     "effort_out_of_set",
			payload:  protocol.SetSessionSettingsPayload{SessionID: initialUUID, Effort: ptr("pyry-e2e-not-an-effort")},
			sentinel: "pyry-e2e-not-an-effort",
		},
	}
	for i, tc := range cases {
		reqID := uint64(42 + i) // distinct id per case ⇒ InReplyTo correlation is meaningful.
		sendSettingsFrame(t, phone, initSend, reqID, tc.payload)
		reply := readSettingsReply(t, phone, initRecv, reqID, 5*time.Second)

		// AC-3(a): TypeError / protocol.malformed / fixed message.
		if reply.Type != protocol.TypeError {
			t.Fatalf("%s: reply Type = %q, want %q (payload=%s)",
				tc.name, reply.Type, protocol.TypeError, reply.Payload)
		}
		var errPayload protocol.ErrorPayload
		if err := json.Unmarshal(reply.Payload, &errPayload); err != nil {
			t.Fatalf("%s: decode error payload: %v", tc.name, err)
		}
		if errPayload.Code != protocol.CodeProtocolMalformed {
			t.Errorf("%s: error Code = %q, want %q", tc.name, errPayload.Code, protocol.CodeProtocolMalformed)
		}
		if errPayload.Message != wantSettingsMalformedMsg {
			t.Errorf("%s: error Message = %q, want %q", tc.name, errPayload.Message, wantSettingsMalformedMsg)
		}

		// AC-3(b): nothing persisted — the disk still shows the Phase-1 values (a
		// validator bypass would have written the sentinel). Matched by the
		// Bootstrap flag, so robust to a restart rotating the bootstrap id.
		if row := readBootstrapSettings(t, regPath); row.Model != "opus" || row.Effort != "high" || !row.YOLO {
			t.Errorf("%s: disk changed after reject: got model=%q effort=%q yolo=%v, want opus/high/true",
				tc.name, row.Model, row.Effort, row.YOLO)
		}

		// AC-3(c): the sentinel appears NOWHERE in the reply frame nor the daemon
		// logs. The reject reply carries only the fixed message; the handler never
		// echoes the value and never logs it at any level.
		rawReply, err := json.Marshal(reply)
		if err != nil {
			t.Fatalf("%s: marshal reply for leak scan: %v", tc.name, err)
		}
		if bytes.Contains(rawReply, []byte(tc.sentinel)) {
			t.Errorf("%s: AC-3(c) sentinel %q leaked into the reply frame: %s", tc.name, tc.sentinel, rawReply)
		}
		if bytes.Contains(reply.Payload, []byte(tc.sentinel)) {
			t.Errorf("%s: AC-3(c) sentinel %q leaked into the reply payload: %s", tc.name, tc.sentinel, reply.Payload)
		}
		if got := h.Stderr.String(); strings.Contains(got, tc.sentinel) {
			t.Errorf("%s: AC-3(c) sentinel %q leaked into the daemon logs", tc.name, tc.sentinel)
		}
	}
}

// ptr returns a pointer to v. The set_session_settings payload uses pointer fields
// as a presence contract (nil = leave unchanged), so tests need &value for literals.
func ptr[T any](v T) *T { return &v }

// sendSettingsFrame seals a set_session_settings control envelope carrying payload
// with cs (the phone's send CipherState) under reqID and writes it to phone.
// Mirrors sendNewSessionFrame. The verb is interactive-gated request/reply, so
// every send pairs with exactly one readSettingsReply on the peer recv CipherState.
func sendSettingsFrame(t *testing.T, phone *fakephone.Client, cs *noise.CipherState, reqID uint64, payload protocol.SetSessionSettingsPayload) {
	t.Helper()
	env, err := json.Marshal(protocol.Envelope{
		ID:      reqID,
		Type:    protocol.TypeSetSessionSettings,
		TS:      time.Now().UTC(),
		Payload: mustJSON(t, payload),
	})
	if err != nil {
		t.Fatalf("marshal set_session_settings envelope: %v", err)
	}
	cipher, err := cs.Encrypt(env)
	if err != nil {
		t.Fatalf("seal set_session_settings envelope: %v", err)
	}
	sendNoiseMsg(t, phone, cipher)
}

// readSettingsReply reads binary→phone frames off phone in capture order and
// returns the first envelope whose InReplyTo == reqID — the reply the handler
// correlated to this request (AC-1). It decrypts EVERY frame it reads (keeping the
// recv nonce aligned) and skips unsolicited interactive-stream pushes, which carry
// InReplyTo == nil: the valid-phase UpdateSettings live-restarts the bootstrap
// child (#842/ADR 031), and that restart can emit a session_transition (and, on a
// stateful conn, queue_state / modal_shown) that would otherwise be mistaken for
// the reply. The total wait is bounded by timeout.
func readSettingsReply(t *testing.T, phone *fakephone.Client, cs *noise.CipherState, reqID uint64, timeout time.Duration) protocol.Envelope {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			t.Fatalf("no set_session_settings reply correlated to req id %d within %s", reqID, timeout)
		}
		env := decryptInnerEnvelope(t, readInnerFrame(t, phone, remaining), cs)
		if env.InReplyTo != nil && *env.InReplyTo == reqID {
			return env
		}
		t.Logf("readSettingsReply: skipping unsolicited %q frame (InReplyTo=%v) while awaiting reply to req %d",
			env.Type, env.InReplyTo, reqID)
	}
}

// settingsRow is a settings-aware decode of one sessions.json row. Defined locally
// rather than extending restart_test.go's shared registryEntry (which lacks these
// fields and is shared across tests); the tags mirror internal/sessions/registry.go
// — the same on-disk shape internal/sessions/pool_update_settings_restart_test.go
// asserts at its tier.
type settingsRow struct {
	ID        string `json:"id"`
	Bootstrap bool   `json:"bootstrap"`
	Model     string `json:"model"`
	Effort    string `json:"effort"`
	YOLO      bool   `json:"yolo"`
}

// readBootstrapSettingsIfPresent reads regPath and returns the Bootstrap==true row
// (matched by flag, so robust to a restart rotating the bootstrap id). The bool is
// false on any of: file missing, parse error, no bootstrap row.
func readBootstrapSettingsIfPresent(regPath string) (settingsRow, bool) {
	data, err := os.ReadFile(regPath)
	if err != nil {
		return settingsRow{}, false
	}
	var reg struct {
		Sessions []settingsRow `json:"sessions"`
	}
	if err := json.Unmarshal(data, &reg); err != nil {
		return settingsRow{}, false
	}
	for _, row := range reg.Sessions {
		if row.Bootstrap {
			return row, true
		}
	}
	return settingsRow{}, false
}

// readBootstrapSettings returns the Bootstrap==true row or Fatals.
func readBootstrapSettings(t *testing.T, regPath string) settingsRow {
	t.Helper()
	row, ok := readBootstrapSettingsIfPresent(regPath)
	if !ok {
		t.Fatalf("no bootstrap row in registry\nfile:\n%s", mustReadFile(t, regPath))
	}
	return row
}

// waitBootstrapSettings polls regPath until the bootstrap row's settings equal
// (model, effort, yolo) or timeout elapses. UpdateSettings persists (temp-file +
// rename, atomic) before the reply is sent, so this normally returns on the first
// poll; the bounded poll only closes a cross-process fs-visibility window.
func waitBootstrapSettings(t *testing.T, regPath, model, effort string, yolo bool, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last settingsRow
	for time.Now().Before(deadline) {
		if row, ok := readBootstrapSettingsIfPresent(regPath); ok {
			last = row
			if row.Model == model && row.Effort == effort && row.YOLO == yolo {
				return
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("bootstrap settings did not reach model=%q effort=%q yolo=%v within %s; last=%+v\nfile:\n%s",
		model, effort, yolo, timeout, last, mustReadFile(t, regPath))
}
