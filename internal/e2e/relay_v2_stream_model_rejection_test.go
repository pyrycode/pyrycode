//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakephone"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakerelay"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

const (
	modelRejectSessionID = "22810000-0000-4000-8000-000000000001"
	modelRejectConvID    = "22810000-0000-4000-8000-000000000002"
	modelRejectTurnText  = "e2e-model-reject-still-alive"
)

// TestRelayV2_StreamRejectsModelAbsentFromPublishedMenu proves the whole
// client-to-child negative path. The candidate is checked against #2279's
// committed live vocabulary and the menu this daemon actually advertised; the
// rejected frame then reaches neither settings storage nor fake Claude's stdin.
func TestRelayV2_StreamRejectsModelAbsentFromPublishedMenu(t *testing.T) {
	home := shortHome(t)
	r := RunBareIn(t, home, "pair", "-pyry-name=test", "--name=phone-a")
	if r.ExitCode != 0 {
		t.Fatalf("pyry pair exit=%d\nstdout:\n%s\nstderr:\n%s", r.ExitCode, r.Stdout, r.Stderr)
	}
	pair := decodePairPayload(t, r.Stdout)
	pubKey, err := base64.StdEncoding.DecodeString(pair.ServerStaticPubkey)
	if err != nil {
		t.Fatalf("decode server static pubkey: %v", err)
	}

	seedBoundConversation(t, home, modelRejectConvID, modelRejectSessionID)
	fr := fakerelay.New(relayTestLogger())
	t.Cleanup(func() { _ = fr.Close() })
	stdinStem := filepath.Join(home, "model-reject-stdin")
	h := StartStreamInteractiveWithRelay(t, home, modelRejectSessionID, fr.URL()+"/v2/server",
		"PYRY_FAKE_CLAUDE_STDIN_LOG="+stdinStem)
	t.Cleanup(func() { h.Stop(t) })

	serverID := readPersistedServerID(t, home)
	waitBinaryHello(t, fr, serverID)
	dialCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	phone, err := fakephone.Dial(dialCtx, fr.URL(), serverID, pair.Token, "phone-a")
	if err != nil {
		t.Fatalf("phone dial: %v", err)
	}
	t.Cleanup(func() { _ = phone.Close() })
	send, recv := driveHandshakeToOpenDaemonInteractive(t, phone, pubKey, pair.Token)

	seal := func(env protocol.Envelope) {
		t.Helper()
		raw, err := json.Marshal(env)
		if err != nil {
			t.Fatalf("marshal %s: %v", env.Type, err)
		}
		ciphertext, err := send.Encrypt(raw)
		if err != nil {
			t.Fatalf("seal %s: %v", env.Type, err)
		}
		sendNoiseMsg(t, phone, ciphertext)
	}
	awaitReply := func(id uint64) protocol.Envelope {
		t.Helper()
		deadline := time.Now().Add(15 * time.Second)
		for {
			remaining := time.Until(deadline)
			if remaining <= 0 {
				t.Fatalf("no reply to request %d", id)
			}
			env := decryptInnerEnvelope(t, readInnerFrame(t, phone, remaining), recv)
			if env.InReplyTo != nil && *env.InReplyTo == id {
				return env
			}
		}
	}

	var menuReply protocol.Envelope
	menuDeadline := time.Now().Add(10 * time.Second)
	for attempt := uint64(1); ; attempt++ {
		seal(protocol.Envelope{ID: attempt, Type: protocol.TypeRequestModelList, TS: time.Now().UTC(),
			Payload: mustJSON(t, protocol.RequestModelListPayload{ConversationID: modelRejectConvID})})
		menuReply = awaitReply(attempt)
		if menuReply.Type == protocol.TypeModelList {
			break
		}
		var unavailable protocol.ErrorPayload
		if menuReply.Type != protocol.TypeError || json.Unmarshal(menuReply.Payload, &unavailable) != nil ||
			unavailable.Code != protocol.CodeModelListUnavailable || !unavailable.Retryable {
			t.Fatalf("request_model_list reply = %q %s, want model_list or retryable model_list.unavailable", menuReply.Type, menuReply.Payload)
		}
		if time.Now().After(menuDeadline) {
			t.Fatal("model vocabulary remained unavailable after initialize deadline")
		}
		time.Sleep(25 * time.Millisecond)
	}
	var menu protocol.ModelListPayload
	if err := json.Unmarshal(menuReply.Payload, &menu); err != nil {
		t.Fatalf("decode advertised model list: %v", err)
	}

	captured := capturedModelValues2281(t)
	for _, option := range menu.Models {
		if !captured[option.Value] {
			t.Errorf("advertised Value %q is not derived from #2279's committed 2.1.259 vocabulary", option.Value)
		}
	}
	candidate := absentModelCandidate2281(captured)
	for _, option := range menu.Models {
		if option.Value == candidate {
			t.Fatalf("candidate %q unexpectedly appears in the advertised menu", candidate)
		}
	}

	effort, mode := "high", "plan"
	seal(protocol.Envelope{ID: 100, Type: protocol.TypeSetSessionSettings, TS: time.Now().UTC(),
		Payload: mustJSON(t, protocol.SetSessionSettingsPayload{
			SessionID: modelRejectSessionID, Model: &candidate, Effort: &effort, PermissionMode: &mode,
		})})
	rejected := awaitReply(100)
	if rejected.Type != protocol.TypeError {
		t.Fatalf("set_session_settings reply Type = %q, want %q", rejected.Type, protocol.TypeError)
	}
	var rejectedPayload protocol.ErrorPayload
	if err := json.Unmarshal(rejected.Payload, &rejectedPayload); err != nil {
		t.Fatalf("decode rejection: %v", err)
	}
	if rejectedPayload.Code != protocol.CodeProtocolMalformed || rejectedPayload.Retryable {
		t.Errorf("rejection = %+v, want non-retryable protocol.malformed", rejectedPayload)
	}

	stdin, err := os.ReadFile(childStdinLog(stdinStem, modelRejectSessionID))
	if err != nil {
		t.Fatalf("read fake Claude stdin: %v", err)
	}
	if bytes.Contains(stdin, []byte(`"subtype":"set_model"`)) || bytes.Contains(stdin, []byte(candidate)) {
		t.Fatalf("rejected model reached fake Claude stdin: %s", stdin)
	}

	seal(protocol.Envelope{ID: 101, Type: protocol.TypeRequestSessionSettings, TS: time.Now().UTC(),
		Payload: mustJSON(t, protocol.RequestSessionSettingsPayload{ConversationID: modelRejectConvID})})
	settingsReply := awaitReply(101)
	var settings protocol.SessionSettingsPayload
	if settingsReply.Type != protocol.TypeSessionSettings {
		t.Fatalf("request_session_settings reply Type = %q, want %q", settingsReply.Type, protocol.TypeSessionSettings)
	}
	if err := json.Unmarshal(settingsReply.Payload, &settings); err != nil {
		t.Fatalf("decode session settings: %v", err)
	}
	if settings.Model != "" || settings.Effort != "" || settings.PermissionMode != "default" {
		t.Errorf("settings after rejected whole frame = %+v, want prior defaults", settings)
	}

	seal(protocol.Envelope{ID: 102, Type: protocol.TypeSendMessage, TS: time.Now().UTC(),
		Payload: mustJSON(t, protocol.SendMessagePayload{
			ConversationID: modelRejectConvID, MessageID: "m-after-reject", Text: modelRejectTurnText,
		})})
	deadline := time.Now().Add(20 * time.Second)
	var sawEcho, sawEnd bool
	for !sawEnd {
		env := decryptInnerEnvelope(t, readInnerFrame(t, phone, time.Until(deadline)), recv)
		switch env.Type {
		case protocol.TypeAssistantDelta:
			var p protocol.AssistantDeltaPayload
			if err := json.Unmarshal(env.Payload, &p); err != nil {
				t.Fatalf("decode assistant_delta: %v", err)
			}
			sawEcho = sawEcho || bytes.Contains([]byte(p.Text), []byte(modelRejectTurnText))
		case protocol.TypeTurnEnd:
			sawEnd = true
		case protocol.TypeError:
			t.Fatalf("later turn returned error: %s", env.Payload)
		}
	}
	if !sawEcho {
		t.Fatal("later turn ended without the fake Claude echo; the prior child did not remain usable")
	}
}

func capturedModelValues2281(t *testing.T) map[string]bool {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("realclaude", "testdata", "set_model_v2.1.259_accept.json"))
	if err != nil {
		t.Fatalf("read #2279 capture: %v", err)
	}
	var capture struct {
		SetModel struct {
			Published []struct {
				Value string `json:"value"`
			} `json:"published_models"`
		} `json:"set_model_capture"`
	}
	if err := json.Unmarshal(raw, &capture); err != nil {
		t.Fatalf("decode #2279 capture: %v", err)
	}
	values := make(map[string]bool, len(capture.SetModel.Published))
	for _, option := range capture.SetModel.Published {
		values[option.Value] = true
	}
	if len(values) == 0 {
		t.Fatal("#2279 capture carries no published model values")
	}
	return values
}

func absentModelCandidate2281(offered map[string]bool) string {
	for i := 0; ; i++ {
		candidate := fmt.Sprintf("claude-no-such-model-2281-%d", i)
		if !offered[candidate] {
			return candidate
		}
	}
}
