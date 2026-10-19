//go:build e2e_realclaude

package realclaude

// This gate proves session_settings follows Claude's confirmation for the exact
// current child rather than the session's stored launch posture. It deliberately
// starts with stored default plus an operator-owned bypass flag, then downgrades
// the same child in band and finishes with #2474's outside-workspace Read modal.
//
// The dispatcher-owned real-Claude stage must record this test as executed. A
// credentials skip is not acceptance.

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/e2e/internal/scenario"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

const (
	runningModeBypass  = "bypassPermissions"
	runningModeDefault = "default"
)

func TestInteractiveStreamSessionSettingsReportsConfirmedPermissionMode(t *testing.T) {
	h, convID, _ := startObservedPermissionHarnessWithOperatorBypass(
		t,
		permissionDaemonModel,
		true,
		nil,
		true,
	)

	// The first turn makes the child and its system/init confirmation observable
	// before the first settings read. It is tool-free so no permission surface can
	// interfere with the posture witness.
	scenario.ConfirmedPermissionSettings(t, sharedHarnessWire(t, h, 2), scenario.PermissionSettingsConfig{ConversationID: convID, SessionID: streamModalBootstrapUUID, Model: permissionDaemonModel, TurnBudget: perTurnReplyBudget, ReadBudget: outsideReadTurnBudget, ChildPID: func() int { return liveChildPID(t, h) }, ReadFixture: func() (string, string) {
		token := mintPostureProbeToken(t)
		notePath := writeHandoffShapedNote(t, h.home, convID, token)
		prompt := "Use the Read tool once to read the file at " + notePath + ", then reply with its exact contents and nothing else. Do not use any other tool. If the read is denied or fails, do not retry it and reply with the single word blocked."
		return prompt, token
	}})
}

func requestSessionSettings(t *testing.T, h *perConvHarness, convID string, reqID uint64) protocol.SessionSettingsPayload {
	t.Helper()
	sealEnvelope(t, h.phone, h.initSend, protocol.Envelope{
		ID:      reqID,
		Type:    protocol.TypeRequestSessionSettings,
		TS:      time.Now().UTC(),
		Payload: mustJSON(t, protocol.RequestSessionSettingsPayload{ConversationID: convID}),
	})
	env := drainForReply(t, h.phone, h.initRecv, protocol.TypeSessionSettings, reqID, 15*time.Second)
	var settings protocol.SessionSettingsPayload
	if err := json.Unmarshal(env.Payload, &settings); err != nil {
		t.Fatalf("decode session_settings: %v", err)
	}
	return settings
}

func assertRunningPermissionSettings(
	t *testing.T,
	got protocol.SessionSettingsPayload,
	wantID, wantModel, wantEffort, wantMode string,
	wantYOLO bool,
) {
	t.Helper()
	if got.SessionID != wantID || got.Model != wantModel || got.Effort != wantEffort ||
		got.PermissionMode != wantMode || got.YOLO != wantYOLO {
		t.Errorf("session_settings = %+v, want id=%q model=%q effort=%q permission_mode=%q yolo=%v",
			got, wantID, wantModel, wantEffort, wantMode, wantYOLO)
	}
}
