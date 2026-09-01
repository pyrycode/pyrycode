package protocol

import (
	"errors"
	"testing"
)

func TestIsKnownAppType(t *testing.T) {
	allTypes := []string{
		TypeHello, TypeHelloAck, TypeError, TypeAck,
		TypeSendMessage, TypeMessage,
		TypeListConversations, TypeConversations,
		TypeCreateConversation, TypeConversationCreated,
		TypePromoteConversation, TypeConversationUpdated,
		TypeRenameConversation,
		TypeDeleteConversation, TypeConversationDeleted,
		TypeArchiveConversation, TypeUnarchiveConversation,
		TypeChangeWorkspace,
		TypeCreateWorkspaceFolder, TypeWorkspaceFolderCreated,
		TypeRecentWorkspaces, TypeRecentWorkspacesList,
		TypeRegisterPushToken,
	}

	for _, ty := range allTypes {
		t.Run("known/"+ty, func(t *testing.T) {
			if err := IsKnownAppType(Envelope{Type: ty}); err != nil {
				t.Errorf("got %v, want nil", err)
			}
		})
	}

	cases := []struct {
		name      string
		typ       string
		encrypted bool
		want      error
	}{
		{"empty-type-rejected", "", false, ErrUnknownType},
		{"unknown-type-rejected", "frobnicate", false, ErrUnknownType},
		{"typo-near-known-rejected", "helo", false, ErrUnknownType},
		{"encrypted-with-known-type", TypeHello, true, ErrUnsupported},
		{"encrypted-with-unknown-type", "frobnicate", true, ErrUnsupported},
		{"encrypted-with-empty-type", "", true, ErrUnsupported},
		// v2-only interactive events are not v1-compatible: an old phone
		// never receives them, so IsKnownAppType must reject each.
		{"turn_state-rejected", TypeTurnState, false, ErrUnknownType},
		{"assistant_delta-rejected", TypeAssistantDelta, false, ErrUnknownType},
		{"tool_use-rejected", TypeToolUse, false, ErrUnknownType},
		{"tool_result-rejected", TypeToolResult, false, ErrUnknownType},
		{"turn_end-rejected", TypeTurnEnd, false, ErrUnknownType},
		{"stall-rejected", TypeStall, false, ErrUnknownType},
		// the v2-only PTY-derived status peers of stall: outbound binary → phone
		// events an old phone never receives, so IsKnownAppType must reject both.
		{"api_retry-rejected", TypeApiRetry, false, ErrUnknownType},
		{"compacting-rejected", TypeCompacting, false, ErrUnknownType},
		{"unrecognized_message-rejected", TypeUnrecognizedMessage, false, ErrUnknownType},
		// the v2-only background-task frames: outbound binary → phone events an
		// old phone never receives, so IsKnownAppType must reject all three.
		{"background_task_started-rejected", TypeBackgroundTaskStarted, false, ErrUnknownType},
		{"background_task_updated-rejected", TypeBackgroundTaskUpdated, false, ErrUnknownType},
		{"background_task_roster-rejected", TypeBackgroundTaskRoster, false, ErrUnknownType},
		// the v2-only thinking-progress reading: an outbound binary → phone event
		// an old phone never receives, so IsKnownAppType must reject it.
		{"thinking_progress-rejected", TypeThinkingProgress, false, ErrUnknownType},
		// the v2-only usage-limit report: an outbound binary → phone event an old
		// phone never receives, so IsKnownAppType must reject it. Rejection is also
		// what keeps the type off the inbound path — a phone must never be able to
		// send a rate_limited frame into dispatch.Route.
		{"rate_limited-rejected", TypeRateLimited, false, ErrUnknownType},
		// the v2-only announced-model report: an outbound binary → phone event an
		// old phone never receives, so IsKnownAppType must reject it. Rejection is
		// also what keeps the type off the inbound path — a phone must never be
		// able to send a model_announced frame into dispatch.Route.
		{"model_announced-rejected", TypeModelAnnounced, false, ErrUnknownType},
		// the v2-only model-list report: an outbound binary → phone report an old
		// phone never receives, so IsKnownAppType must reject it. Rejection is also
		// what keeps the type off the inbound path — a phone must never be able to
		// send a model_list frame into dispatch.Route.
		{"model_list-rejected", TypeModelList, false, ErrUnknownType},
		// the v2-only slash-command-list report: an outbound binary → phone report
		// an old phone never receives, so IsKnownAppType must reject it. Rejection
		// is also what keeps the type off the inbound path — a phone must never be
		// able to send a slash_command_list frame into dispatch.Route.
		{"slash_command_list-rejected", TypeSlashCommandList, false, ErrUnknownType},
		// v2-only screen-snapshot types are likewise not v1-compatible.
		{"request_snapshot-rejected", TypeRequestSnapshot, false, ErrUnknownType},
		{"screen_snapshot-rejected", TypeScreenSnapshot, false, ErrUnknownType},
		// the v2-only reconnect resync marker is binary → phone; an old phone
		// must never receive it, so IsKnownAppType must reject it.
		{"resync-rejected", TypeResync, false, ErrUnknownType},
		// the v2-only session-boundary marker is binary → phone; an old phone
		// must never receive it, so IsKnownAppType must reject it.
		{"session_transition-rejected", TypeSessionTransition, false, ErrUnknownType},
		// the v2-only modal vocabulary: outbound modal events an old phone
		// never receives, and inbound modal controls that are never v1 types —
		// IsKnownAppType must reject all four.
		{"modal_shown-rejected", TypeModalShown, false, ErrUnknownType},
		{"modal_answer-rejected", TypeModalAnswer, false, ErrUnknownType},
		{"modal_cancel-rejected", TypeModalCancel, false, ErrUnknownType},
		{"modal_dismissed-rejected", TypeModalDismissed, false, ErrUnknownType},
		// the v2-only queue vocabulary: outbound queue_state an old phone never
		// receives, and inbound dequeue_message control that is never a v1 type.
		{"queue_state-rejected", TypeQueueState, false, ErrUnknownType},
		{"dequeue_message-rejected", TypeDequeueMessage, false, ErrUnknownType},
		// the v2-only interrupt control: an inbound control type an old phone
		// never sees, so IsKnownAppType must reject it.
		{"interrupt-rejected", TypeInterrupt, false, ErrUnknownType},
		// the v2-only debug-bundle streaming vocabulary: outbound binary → phone
		// chunk/completion events an old phone never receives, so IsKnownAppType
		// must reject both.
		{"debug_bundle_chunk-rejected", TypeDebugBundleChunk, false, ErrUnknownType},
		{"debug_bundle_done-rejected", TypeDebugBundleDone, false, ErrUnknownType},
		// the v2-only debug-bundle request verb: an inbound control type an old
		// phone never sees, so IsKnownAppType must reject it.
		{"request_debug_bundle-rejected", TypeRequestDebugBundle, false, ErrUnknownType},
		// the v2-only new_session control: an inbound control type an old phone
		// never sees, so IsKnownAppType must reject it.
		{"new_session-rejected", TypeNewSession, false, ErrUnknownType},
		// the v2-only set-session-settings vocabulary: an inbound control
		// request an old phone never sends and an outbound reply an old phone
		// never receives, so IsKnownAppType must reject both.
		{"set_session_settings-rejected", TypeSetSessionSettings, false, ErrUnknownType},
		{"session_settings_updated-rejected", TypeSessionSettingsUpdated, false, ErrUnknownType},
		{"request_session_settings-rejected", TypeRequestSessionSettings, false, ErrUnknownType},
		{"session_settings-rejected", TypeSessionSettings, false, ErrUnknownType},
		// the v2-only session-error frame is binary → phone; an old phone must
		// never receive it, so IsKnownAppType must reject it.
		{"session_error-rejected", TypeSessionError, false, ErrUnknownType},
		// the v2-only attachment chunk: an old phone must never receive one, so
		// IsKnownAppType must reject it. Rejection is also what keeps the type off
		// the v1 inbound path, and here that half is the load-bearing one — the
		// upload leg really is inbound, so this is the structural bar against a v1
		// client sending an attachment_chunk frame into dispatch.Route.
		{"attachment_chunk-rejected", TypeAttachmentChunk, false, ErrUnknownType},
		// the v2-only clarifying-question batch: an outbound binary → phone report
		// an old phone never receives, so IsKnownAppType must reject it. Rejection
		// is also what keeps the type off the inbound path — a phone must never be
		// able to send a question_shown frame into dispatch.Route.
		{"question_shown-rejected", TypeQuestionShown, false, ErrUnknownType},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := IsKnownAppType(Envelope{Type: tc.typ, PayloadEncrypted: tc.encrypted})
			if !errors.Is(err, tc.want) {
				t.Errorf("got %v, want %v", err, tc.want)
			}
		})
	}
}

func TestInboundAppTypeSet_CoversAllExportedTypeConstants(t *testing.T) {
	all := []string{
		TypeHello, TypeHelloAck, TypeError, TypeAck,
		TypeSendMessage, TypeMessage,
		TypeListConversations, TypeConversations,
		TypeCreateConversation, TypeConversationCreated,
		TypePromoteConversation, TypeConversationUpdated,
		TypeRenameConversation,
		TypeDeleteConversation, TypeConversationDeleted,
		TypeArchiveConversation, TypeUnarchiveConversation,
		TypeChangeWorkspace,
		TypeCreateWorkspaceFolder, TypeWorkspaceFolderCreated,
		TypeRecentWorkspaces, TypeRecentWorkspacesList,
		TypeRegisterPushToken,
	}
	if got, want := len(all), 23; got != want {
		t.Fatalf("type-list length: got %d, want %d", got, want)
	}
	if got, want := len(inboundAppTypeSet), len(all); got != want {
		t.Errorf("inboundAppTypeSet size: got %d, want %d", got, want)
	}
	for _, ty := range all {
		if !inboundAppTypeSet[ty] {
			t.Errorf("inboundAppTypeSet missing %q", ty)
		}
	}
}

// v2OnlyTypes is the test-local allowlist of Mobile Protocol v2 envelope
// types that are deliberately excluded from inboundAppTypeSet. Two flavours live
// here: v2 control types (e.g. TypeRekeyRequest), intercepted at
// internal/relay/v2session.go's dispatch boundary before dispatch.Route;
// and v2 additive interactive application events (turn_state and friends),
// pushed outbound to capability-advertising phones and never dispatched
// inbound. Both are "v2-only" for the partition's purpose — adding either
// to inboundAppTypeSet would let an old phone (or dispatch.Route) see a type it
// must not, so the partition is the architectural seam between v1 traffic
// and v2 traffic.
var v2OnlyTypes = map[string]bool{
	TypeRekeyRequest:        true,
	TypeTurnState:           true,
	TypeAssistantDelta:      true,
	TypeToolUse:             true,
	TypeToolResult:          true,
	TypeTurnEnd:             true,
	TypeStall:               true,
	TypeApiRetry:            true,
	TypeCompacting:          true,
	TypeUnrecognizedMessage: true,
	TypeRequestSnapshot:     true,
	TypeScreenSnapshot:      true,
	TypeResync:              true,
	TypeSessionTransition:   true,
	// v2 modal vocabulary.
	TypeModalShown:     true,
	TypeModalAnswer:    true,
	TypeModalCancel:    true,
	TypeModalDismissed: true,
	// v2 queue vocabulary.
	TypeQueueState:     true,
	TypeDequeueMessage: true,
	// v2 interrupt control.
	TypeInterrupt: true,
	// v2 debug-bundle streaming vocabulary.
	TypeDebugBundleChunk: true,
	TypeDebugBundleDone:  true,
	// v2 debug-bundle request verb.
	TypeRequestDebugBundle: true,
	// v2 new_session control.
	TypeNewSession: true,
	// v2 set-session-settings vocabulary.
	TypeSetSessionSettings:     true,
	TypeSessionSettingsUpdated: true,
	// v2 read-session-settings vocabulary.
	TypeRequestSessionSettings: true,
	TypeSessionSettings:        true,
	// v2 session-error frame.
	TypeSessionError: true,
	// v2 background-task vocabulary.
	TypeBackgroundTaskStarted: true,
	TypeBackgroundTaskUpdated: true,
	TypeBackgroundTaskRoster:  true,
	// v2 thinking-progress reading.
	TypeThinkingProgress: true,
	// v2 usage-limit report.
	TypeRateLimited: true,
	// v2 announced-model report.
	TypeModelAnnounced: true,
	// v2 model-list report.
	TypeModelList: true,
	// v2 slash-command-list report.
	TypeSlashCommandList: true,
	// v2 attachment vocabulary.
	TypeAttachmentChunk: true,
	// v2 clarifying-question batch.
	TypeQuestionShown: true,
}

// TestTypeConstants_V1V2Partition pins the architectural asymmetry that
// every exported Type* constant must be classified either as a v1
// application type (member of inboundAppTypeSet) or a v2 control type (member of
// v2OnlyTypes), and never as both. A future contributor adding a v2
// control type is forced to amend the v2OnlyTypes literal here; a
// contributor accidentally adding a v2 control type to inboundAppTypeSet is
// caught by the "in both" branch.
func TestTypeConstants_V1V2Partition(t *testing.T) {
	all := []string{
		// v1 application types.
		TypeHello, TypeHelloAck, TypeError, TypeAck,
		TypeSendMessage, TypeMessage,
		TypeListConversations, TypeConversations,
		TypeCreateConversation, TypeConversationCreated,
		TypePromoteConversation, TypeConversationUpdated,
		TypeRenameConversation,
		TypeDeleteConversation, TypeConversationDeleted,
		TypeArchiveConversation, TypeUnarchiveConversation,
		TypeChangeWorkspace,
		TypeCreateWorkspaceFolder, TypeWorkspaceFolderCreated,
		TypeRecentWorkspaces, TypeRecentWorkspacesList,
		TypeRegisterPushToken,
		// v2 control types.
		TypeRekeyRequest,
		// v2 interactive application events.
		TypeTurnState, TypeAssistantDelta, TypeToolUse,
		TypeToolResult, TypeTurnEnd, TypeStall,
		// v2 PTY-derived status peers of stall.
		TypeApiRetry, TypeCompacting,
		// v2 parser-gap diagnostic.
		TypeUnrecognizedMessage,
		// v2 screen-snapshot types.
		TypeRequestSnapshot, TypeScreenSnapshot,
		// v2 reconnect resync marker.
		TypeResync,
		// v2 session-boundary marker.
		TypeSessionTransition,
		// v2 modal vocabulary.
		TypeModalShown, TypeModalAnswer, TypeModalCancel, TypeModalDismissed,
		// v2 queue vocabulary.
		TypeQueueState, TypeDequeueMessage,
		// v2 interrupt control.
		TypeInterrupt,
		// v2 debug-bundle streaming vocabulary.
		TypeDebugBundleChunk, TypeDebugBundleDone,
		// v2 debug-bundle request verb.
		TypeRequestDebugBundle,
		// v2 new_session control.
		TypeNewSession,
		// v2 set-session-settings vocabulary.
		TypeSetSessionSettings, TypeSessionSettingsUpdated,
		TypeRequestSessionSettings, TypeSessionSettings,
		// v2 session-error frame.
		TypeSessionError,
		// v2 background-task vocabulary.
		TypeBackgroundTaskStarted, TypeBackgroundTaskUpdated,
		TypeBackgroundTaskRoster,
		// v2 thinking-progress reading.
		TypeThinkingProgress,
		// v2 usage-limit report.
		TypeRateLimited,
		// v2 announced-model report.
		TypeModelAnnounced,
		// v2 model-list report.
		TypeModelList,
		// v2 slash-command-list report.
		TypeSlashCommandList,
		// v2 attachment vocabulary.
		TypeAttachmentChunk,
		// v2 clarifying-question batch.
		TypeQuestionShown,
	}
	for _, ty := range all {
		inV1 := inboundAppTypeSet[ty]
		inV2 := v2OnlyTypes[ty]
		switch {
		case inV1 && inV2:
			t.Errorf("%q in BOTH inboundAppTypeSet and v2OnlyTypes; the partition must be disjoint", ty)
		case !inV1 && !inV2:
			t.Errorf("%q missing from both inboundAppTypeSet and v2OnlyTypes; classify it as v1 application or v2 control", ty)
		}
	}
	// And the union must equal the constant-count to catch the inverse:
	// a inboundAppTypeSet entry that has no exported Type* constant.
	if got, want := len(inboundAppTypeSet)+len(v2OnlyTypes), len(all); got != want {
		t.Errorf("inboundAppTypeSet + v2OnlyTypes size: got %d, want %d", got, want)
	}
}

func TestErrorCode_Constants_MatchSpec(t *testing.T) {
	cases := map[string]string{
		"CodeProtocolUnknownType":         CodeProtocolUnknownType,
		"CodeProtocolMalformed":           CodeProtocolMalformed,
		"CodeProtocolUnsupported":         CodeProtocolUnsupported,
		"CodeAuthInvalidToken":            CodeAuthInvalidToken,
		"CodeAuthTokenRevoked":            CodeAuthTokenRevoked,
		"CodeServerBinaryOffline":         CodeServerBinaryOffline,
		"CodeServerBinaryBusy":            CodeServerBinaryBusy,
		"CodeConversationNotFound":        CodeConversationNotFound,
		"CodeConversationAlreadyPromoted": CodeConversationAlreadyPromoted,
		"CodeMessageTooLong":              CodeMessageTooLong,
		"CodeRelayNoServer":               CodeRelayNoServer,
		"CodeRelayServerIDConflict":       CodeRelayServerIDConflict,
		"CodeSessionNotFound":             CodeSessionNotFound,
		"CodeSessionBlocked":              CodeSessionBlocked,
		"CodeAttachmentInvalidChunk":      CodeAttachmentInvalidChunk,
		"CodeAttachmentIntegrityFailed":   CodeAttachmentIntegrityFailed,
		"CodeAttachmentTooManyUploads":    CodeAttachmentTooManyUploads,
		"CodeAttachmentTooLarge":          CodeAttachmentTooLarge,
		"CodeAttachmentStorageFailed":     CodeAttachmentStorageFailed,
		"CodeAttachmentNotFound":          CodeAttachmentNotFound,
		"CodeAttachmentStreamAborted":     CodeAttachmentStreamAborted,
	}
	want := map[string]string{
		"CodeProtocolUnknownType":         "protocol.unknown_type",
		"CodeProtocolMalformed":           "protocol.malformed",
		"CodeProtocolUnsupported":         "protocol.unsupported",
		"CodeAuthInvalidToken":            "auth.invalid_token",
		"CodeAuthTokenRevoked":            "auth.token_revoked",
		"CodeServerBinaryOffline":         "server.binary_offline",
		"CodeServerBinaryBusy":            "server.binary_busy",
		"CodeConversationNotFound":        "conversation.not_found",
		"CodeConversationAlreadyPromoted": "conversation.already_promoted",
		"CodeMessageTooLong":              "message.too_long",
		"CodeRelayNoServer":               "relay.no_server",
		"CodeRelayServerIDConflict":       "relay.server_id_conflict",
		"CodeSessionNotFound":             "session.not_found",
		"CodeSessionBlocked":              "session.blocked",
		"CodeAttachmentInvalidChunk":      "attachment.invalid_chunk",
		"CodeAttachmentIntegrityFailed":   "attachment.integrity_failed",
		"CodeAttachmentTooManyUploads":    "attachment.too_many_uploads",
		"CodeAttachmentTooLarge":          "attachment.too_large",
		"CodeAttachmentStorageFailed":     "attachment.storage_failed",
		"CodeAttachmentNotFound":          "attachment.not_found",
		"CodeAttachmentStreamAborted":     "attachment.stream_aborted",
	}
	if len(cases) != len(want) {
		t.Fatalf("case-count drift: got %d, want %d", len(cases), len(want))
	}
	for name, got := range cases {
		if got != want[name] {
			t.Errorf("%s: got %q, want %q", name, got, want[name])
		}
	}
}
