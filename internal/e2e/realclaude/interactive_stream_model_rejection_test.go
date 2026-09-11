//go:build e2e_realclaude

package realclaude

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakephone"
	"github.com/pyrycode/pyrycode/internal/noise"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

const modelRejectionBudget = 30 * time.Second

// TestInteractiveStreamRejectsModelAbsentFromCurrentMenu repeats the client
// contract against a real child. The unavailable value is derived after reading
// that child's current menu, so a Claude release adding or removing rows cannot
// turn the negative path into a false positive.
func TestInteractiveStreamRejectsModelAbsentFromCurrentMenu(t *testing.T) {
	h, convID := startStreamModalResolutionHarness(t, permissionDaemonModel)

	var menuEnv protocol.Envelope
	menuDeadline := time.Now().Add(modelRejectionBudget)
	for attempt := uint64(1); ; attempt++ {
		sealEnvelope(t, h.phone, h.initSend, protocol.Envelope{
			ID:      attempt,
			Type:    protocol.TypeRequestModelList,
			TS:      time.Now().UTC(),
			Payload: mustJSON(t, protocol.RequestModelListPayload{ConversationID: convID}),
		})
		menuEnv = drainForCorrelatedEnvelope2281(t, h.phone, h.initRecv, attempt, time.Until(menuDeadline))
		if menuEnv.Type == protocol.TypeModelList {
			break
		}
		var unavailable protocol.ErrorPayload
		if menuEnv.Type != protocol.TypeError || json.Unmarshal(menuEnv.Payload, &unavailable) != nil ||
			unavailable.Code != protocol.CodeModelListUnavailable || !unavailable.Retryable {
			t.Fatalf("request_model_list reply = %q %s, want model_list or retryable model_list.unavailable", menuEnv.Type, menuEnv.Payload)
		}
		if time.Now().After(menuDeadline) {
			t.Fatal("current child model vocabulary remained unavailable after initialize deadline")
		}
		time.Sleep(50 * time.Millisecond)
	}
	var menu protocol.ModelListPayload
	if err := json.Unmarshal(menuEnv.Payload, &menu); err != nil {
		t.Fatalf("decode current model list: %v", err)
	}
	if len(menu.Models) == 0 {
		t.Fatal("current child published no models; absence cannot be proved")
	}
	if menu.DroppedModels != 0 {
		t.Fatalf("current child menu dropped %d rows; absence cannot be proved", menu.DroppedModels)
	}
	offered := make(map[string]bool, len(menu.Models))
	for _, option := range menu.Models {
		for _, field := range option.TruncatedFields {
			if field == "value" {
				t.Fatal("current child menu contains a truncated value; absence cannot be proved")
			}
		}
		offered[option.Value] = true
	}
	candidate := liveAbsentModelCandidate2281(offered)

	effort, mode := "high", "plan"
	sealEnvelope(t, h.phone, h.initSend, protocol.Envelope{
		ID:   1000,
		Type: protocol.TypeSetSessionSettings,
		TS:   time.Now().UTC(),
		Payload: mustJSON(t, protocol.SetSessionSettingsPayload{
			SessionID:      streamModalBootstrapUUID,
			Model:          &candidate,
			Effort:         &effort,
			PermissionMode: &mode,
		}),
	})
	rejected := drainForCorrelatedEnvelope2281(t, h.phone, h.initRecv, 1000, modelRejectionBudget)
	if rejected.Type != protocol.TypeError {
		t.Fatalf("set_session_settings reply Type = %q, want %q", rejected.Type, protocol.TypeError)
	}
	var rejection protocol.ErrorPayload
	if err := json.Unmarshal(rejected.Payload, &rejection); err != nil {
		t.Fatalf("decode settings rejection: %v", err)
	}
	if rejection.Code != protocol.CodeProtocolMalformed || rejection.Retryable {
		t.Fatalf("settings rejection = %+v, want non-retryable protocol.malformed", rejection)
	}

	sealSendMessage(t, h.phone, h.initSend, 1001, convID, "m-after-model-reject",
		fmt.Sprintf("Reply with one short word and nothing else. run=%d", time.Now().UnixNano()))
	announced, seen := drainForAnnouncedModel(t, h.phone, h.initRecv, convID, perTurnReplyBudget)
	if !seen {
		t.Fatal("the turn after model rejection emitted no model_announced frame")
	}
	if want := announcedTargetFor(t, permissionDaemonModel); announced.Model != want {
		t.Errorf("post-rejection model = %q, want prior model %q", announced.Model, want)
	}
}

func liveAbsentModelCandidate2281(offered map[string]bool) string {
	for i := 0; ; i++ {
		candidate := fmt.Sprintf("claude-no-such-model-2281-%d", i)
		if !offered[candidate] {
			return candidate
		}
	}
}

// drainForCorrelatedEnvelope2281 returns any reply type for one request. The
// settings assertion needs to inspect an expected error rather than treating it
// as a harness failure, while the menu request needs the same receive-nonce-safe
// correlation behavior.
func drainForCorrelatedEnvelope2281(t *testing.T, phone *fakephone.Client, cs *noise.CipherState, reqID uint64, timeout time.Duration) protocol.Envelope {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			t.Fatalf("no reply to request %d within %s", reqID, timeout)
		}
		raw, err := phone.ReceiveBytes(remaining)
		if err != nil {
			if errors.Is(err, fakephone.ErrReceiveTimeout) {
				t.Fatalf("no reply to request %d within %s", reqID, timeout)
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
		ciphertext, err := base64.StdEncoding.DecodeString(inner.Data)
		if err != nil {
			t.Fatalf("decode inner data: %v", err)
		}
		plain, err := cs.Decrypt(ciphertext)
		if err != nil {
			t.Fatalf("phone decrypt: %v", err)
		}
		var env protocol.Envelope
		if err := json.Unmarshal(plain, &env); err != nil {
			t.Fatalf("decode envelope: %v", err)
		}
		if env.InReplyTo != nil && *env.InReplyTo == reqID {
			return env
		}
	}
}
