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
