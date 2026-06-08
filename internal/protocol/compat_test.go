package protocol

import (
	"errors"
	"testing"
)

func TestIsV1Compatible(t *testing.T) {
	allTypes := []string{
		TypeHello, TypeHelloAck, TypeError, TypeAck,
		TypeSendMessage, TypeMessage,
		TypeListConversations, TypeConversations,
		TypeCreateConversation, TypeConversationCreated,
		TypePromoteConversation, TypeConversationUpdated,
		TypeBackfillSince, TypeMessageChunk, TypeBackfillDone,
		TypeRegisterPushToken,
	}

	for _, ty := range allTypes {
		t.Run("known/"+ty, func(t *testing.T) {
			if err := IsV1Compatible(Envelope{Type: ty}); err != nil {
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
		// never receives them, so IsV1Compatible must reject each.
		{"turn_state-rejected", TypeTurnState, false, ErrUnknownType},
		{"assistant_delta-rejected", TypeAssistantDelta, false, ErrUnknownType},
		{"tool_use-rejected", TypeToolUse, false, ErrUnknownType},
		{"tool_result-rejected", TypeToolResult, false, ErrUnknownType},
		{"turn_end-rejected", TypeTurnEnd, false, ErrUnknownType},
		// v2-only screen-snapshot types are likewise not v1-compatible.
		{"request_snapshot-rejected", TypeRequestSnapshot, false, ErrUnknownType},
		{"screen_snapshot-rejected", TypeScreenSnapshot, false, ErrUnknownType},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := IsV1Compatible(Envelope{Type: tc.typ, PayloadEncrypted: tc.encrypted})
			if !errors.Is(err, tc.want) {
				t.Errorf("got %v, want %v", err, tc.want)
			}
		})
	}
}

func TestV1TypeSet_CoversAllExportedTypeConstants(t *testing.T) {
	all := []string{
		TypeHello, TypeHelloAck, TypeError, TypeAck,
		TypeSendMessage, TypeMessage,
		TypeListConversations, TypeConversations,
		TypeCreateConversation, TypeConversationCreated,
		TypePromoteConversation, TypeConversationUpdated,
		TypeBackfillSince, TypeMessageChunk, TypeBackfillDone,
		TypeRegisterPushToken,
	}
	if got, want := len(all), 16; got != want {
		t.Fatalf("type-list length: got %d, want %d", got, want)
	}
	if got, want := len(v1TypeSet), len(all); got != want {
		t.Errorf("v1TypeSet size: got %d, want %d", got, want)
	}
	for _, ty := range all {
		if !v1TypeSet[ty] {
			t.Errorf("v1TypeSet missing %q", ty)
		}
	}
}

// v2OnlyTypes is the test-local allowlist of Mobile Protocol v2 envelope
// types that are deliberately excluded from v1TypeSet. Two flavours live
// here: v2 control types (e.g. TypeRekeyRequest), intercepted at
// internal/relay/v2session.go's dispatch boundary before dispatch.Route;
// and v2 additive interactive application events (turn_state and friends),
// pushed outbound to capability-advertising phones and never dispatched
// inbound. Both are "v2-only" for the partition's purpose — adding either
// to v1TypeSet would let an old phone (or dispatch.Route) see a type it
// must not, so the partition is the architectural seam between v1 traffic
// and v2 traffic.
var v2OnlyTypes = map[string]bool{
	TypeRekeyRequest:    true,
	TypeTurnState:       true,
	TypeAssistantDelta:  true,
	TypeToolUse:         true,
	TypeToolResult:      true,
	TypeTurnEnd:         true,
	TypeRequestSnapshot: true,
	TypeScreenSnapshot:  true,
}

// TestTypeConstants_V1V2Partition pins the architectural asymmetry that
// every exported Type* constant must be classified either as a v1
// application type (member of v1TypeSet) or a v2 control type (member of
// v2OnlyTypes), and never as both. A future contributor adding a v2
// control type is forced to amend the v2OnlyTypes literal here; a
// contributor accidentally adding a v2 control type to v1TypeSet is
// caught by the "in both" branch.
func TestTypeConstants_V1V2Partition(t *testing.T) {
	all := []string{
		// v1 application types.
		TypeHello, TypeHelloAck, TypeError, TypeAck,
		TypeSendMessage, TypeMessage,
		TypeListConversations, TypeConversations,
		TypeCreateConversation, TypeConversationCreated,
		TypePromoteConversation, TypeConversationUpdated,
		TypeBackfillSince, TypeMessageChunk, TypeBackfillDone,
		TypeRegisterPushToken,
		// v2 control types.
		TypeRekeyRequest,
		// v2 interactive application events.
		TypeTurnState, TypeAssistantDelta, TypeToolUse,
		TypeToolResult, TypeTurnEnd,
		// v2 screen-snapshot types.
		TypeRequestSnapshot, TypeScreenSnapshot,
	}
	for _, ty := range all {
		inV1 := v1TypeSet[ty]
		inV2 := v2OnlyTypes[ty]
		switch {
		case inV1 && inV2:
			t.Errorf("%q in BOTH v1TypeSet and v2OnlyTypes; the partition must be disjoint", ty)
		case !inV1 && !inV2:
			t.Errorf("%q missing from both v1TypeSet and v2OnlyTypes; classify it as v1 application or v2 control", ty)
		}
	}
	// And the union must equal the constant-count to catch the inverse:
	// a v1TypeSet entry that has no exported Type* constant.
	if got, want := len(v1TypeSet)+len(v2OnlyTypes), len(all); got != want {
		t.Errorf("v1TypeSet + v2OnlyTypes size: got %d, want %d", got, want)
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
