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
	"slices"
	"strings"
	"testing"
	"time"

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
	nextID := uint64(2)
	sealSendMessage(t, h.phone, h.initSend, nextID, convID, "m-2510-start",
		"Reply with the single word ready. Do not use tools.")
	nextID++
	drainForCompletedTurn(t, h.phone, h.initRecv, convID, perTurnReplyBudget)

	pid := liveChildPID(t, h)
	initial := requestSessionSettings(t, h, convID, nextID)
	nextID++
	assertRunningPermissionSettings(t, initial, streamModalBootstrapUUID, "", "", runningModeBypass, true)

	// Stored permission mode is already default, so model supplies the real stored
	// change that keeps UpdateSettings out of its no-op return. Naming default in
	// the same frame makes the daemon send set_permission_mode to this live child.
	model := permissionDaemonModel
	mode := runningModeDefault
	updateID := nextID
	sealEnvelope(t, h.phone, h.initSend, protocol.Envelope{
		ID:   updateID,
		Type: protocol.TypeSetSessionSettings,
		TS:   time.Now().UTC(),
		Payload: mustJSON(t, protocol.SetSessionSettingsPayload{
			SessionID:      streamModalBootstrapUUID,
			Model:          &model,
			PermissionMode: &mode,
		}),
	})
	nextID++
	updatedEnv := drainForReply(t, h.phone, h.initRecv, protocol.TypeSessionSettingsUpdated, updateID, modalSurfaceBudget)
	var updated protocol.SessionSettingsUpdatedPayload
	if err := json.Unmarshal(updatedEnv.Payload, &updated); err != nil {
		t.Fatalf("decode session_settings_updated: %v", err)
	}
	if updated.SessionID != streamModalBootstrapUUID {
		t.Fatalf("session_settings_updated session_id = %q, want %q", updated.SessionID, streamModalBootstrapUUID)
	}

	confirmedDeadline := time.Now().Add(30 * time.Second)
	var after protocol.SessionSettingsPayload
	for {
		after = requestSessionSettings(t, h, convID, nextID)
		nextID++
		if after.PermissionMode == runningModeDefault && !after.YOLO {
			break
		}
		if time.Now().After(confirmedDeadline) {
			t.Fatalf("session_settings never refreshed to confirmed default/false within 30s; last reply: %+v", after)
		}
		time.Sleep(100 * time.Millisecond)
	}
	assertRunningPermissionSettings(t, after, streamModalBootstrapUUID, permissionDaemonModel, "", runningModeDefault, false)
	if got := liveChildPID(t, h); got != pid {
		t.Fatalf("permission switch respawned the child: pid %d -> %d", pid, got)
	}

	// Reuse #2474's exact enforcement case on the same process. The operator
	// supplied the stdio prompt tool at launch because operator-owned bypass makes
	// the daemon correctly skip injecting an approval gate of its own.
	token := mintPostureProbeToken(t)
	notePath := writeHandoffShapedNote(t, h.home, convID, token)
	prompt := "Use the Read tool once to read the file at " + notePath +
		", then reply with its exact contents and nothing else. Do not use any other tool. " +
		"If the read is denied or fails, do not retry it and reply with the single word blocked."
	if strings.Contains(prompt, token) {
		t.Fatal("outside-workspace prompt contains the witness token")
	}
	sealSendMessage(t, h.phone, h.initSend, nextID, convID, "m-2510-read", prompt)
	nextID++
	reply, tools, modal, _ := driveOutsideWorkspaceRead(
		t, h, convID, time.Now().Add(outsideReadTurnBudget), nextID,
	)
	if modal == nil {
		t.Fatal("the confirmed default child raised no permission modal for the outside-workspace Read")
	}
	if want := []string{postureProbeReadTool}; !slices.Equal(tools, want) {
		t.Fatalf("outside-workspace turn called distinct tools %q, want exactly %q so the observed modal belongs to Read", tools, want)
	}
	if !strings.Contains(reply, token) {
		t.Fatal("outside-workspace Read completed without returning the file's witness token")
	}
	if got := liveChildPID(t, h); got != pid {
		t.Fatalf("outside-workspace enforcement ran on a replacement child: pid %d -> %d", pid, got)
	}
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
