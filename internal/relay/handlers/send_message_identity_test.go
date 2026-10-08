package handlers

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/devices"
	"github.com/pyrycode/pyrycode/internal/dispatch"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

type identifiedEnqueueCall struct {
	enqueueCall
	deviceID string
}

type fakeIdentifiedEnqueuer struct {
	fakeEnqueuer
	identified []identifiedEnqueueCall
}

func (f *fakeIdentifiedEnqueuer) EnqueueIdentified(convID, messageID, text, delivery string, attachmentIDs []string, deviceID, deviceName, clientVersion string, clientSentAt time.Time) uint64 {
	f.identified = append(f.identified, identifiedEnqueueCall{
		enqueueCall: enqueueCall{convID: convID, messageID: messageID, text: text, delivery: delivery,
			attachmentIDs: attachmentIDs, deviceName: deviceName, clientVersion: clientVersion, clientSentAt: clientSentAt},
		deviceID: deviceID,
	})
	if f.reject {
		return 0
	}
	return uint64(len(f.identified))
}

func TestSendMessage_IdentifiedAuthenticatedMetadata(t *testing.T) {
	t.Parallel()
	keyA, keyB := strings.Repeat("a1", 32), strings.Repeat("b2", 32)
	const tokenHash = "AUTH_TOKEN_HASH_DO_NOT_LOG"
	const path = "/private/attachments/secret-file"
	cases := []struct {
		name, key, deviceName string
		absentAuth            bool
	}{
		{"first device", keyA, "same name", false},
		{"distinct device equal name and app id", keyB, "same name", false},
		{"renamed install", keyA, "new name", false},
		{"reconnected install", keyA, "same name", false},
		{"unbound key with name and token hash", "", "same name", false},
		{"no authentication", "", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			auth := &devices.Device{StaticKey: tc.key, Name: tc.deviceName, TokenHash: tokenHash, ClientVersion: "pyrycode-mobile/1.4.0"}
			version := auth.ClientVersion
			if tc.absentAuth {
				auth, version = nil, ""
			}
			out := make(chan protocol.RoutingEnvelope, 1)
			c := dispatch.NewTestConn(sendMsgConnIDForTest, out, auth)
			_ = c.NextID()
			logger, logs := sendMsgCapturingLogger(t)
			q := &fakeIdentifiedEnqueuer{}
			resolve := func(convID, id string) (string, bool) {
				if convID != sendMsgConvID || id != "attachment-1" {
					t.Fatalf("resolver called with (%q, %q)", convID, id)
				}
				return path, true
			}
			req := sendMsgRequest(t, map[string]any{
				"conversation_id": sendMsgConvID, "message_id": " same app id ", "text": sendMsgText,
				"attachment_ids": []string{"attachment-1", "attachment-1"}, "client_sent_at": "2026-10-08T12:30:00+02:00",
				"device_id": "FORGED_ID", "sender_identity": "FORGED_ID", "static_key": "FORGED_ID",
				"device_name": "FORGED_NAME", "token": "FORGED_TOKEN",
			})
			if err := SendMessage(routeTo(&stubTurnWriter{}), q, resolve, nil, "", nil, nil, logger)(context.Background(), c, req); err != nil {
				t.Fatalf("handler: %v", err)
			}
			if len(q.calls) != 0 || len(q.identified) != 1 {
				t.Fatalf("legacy/identified calls = %d/%d, want 0/1", len(q.calls), len(q.identified))
			}
			want := identifiedEnqueueCall{
				enqueueCall: enqueueCall{convID: sendMsgConvID, messageID: " same app id ", text: sendMsgText,
					delivery:      sendMsgText + "\n\nAttached files (use the Read tool to view each):\n" + path,
					attachmentIDs: []string{"attachment-1"}, deviceName: tc.deviceName, clientVersion: version,
					clientSentAt: time.Date(2026, 10, 8, 10, 30, 0, 0, time.UTC)},
				deviceID: tc.key,
			}
			if got := q.identified[0]; !reflect.DeepEqual(got, want) {
				t.Errorf("queued = %#v, want %#v", got, want)
			}
			resp := <-out
			assertSendMsgEnvelopeShape(t, resp, protocol.TypeAck)
			for _, value := range []string{keyA, keyB, tokenHash, "FORGED_ID", "FORGED_TOKEN", path} {
				if strings.Contains(logs.String(), value) || strings.Contains(string(resp.Frame), value) {
					t.Errorf("private metadata leaked: %q", value)
				}
			}
		})
	}
}

func TestSendMessage_EnqueueAPIGates(t *testing.T) {
	t.Parallel()
	key := strings.Repeat("c3", 32)
	cases := []struct {
		name, text, wantCode string
		routeErr             error
		attachments          []string
		backlog, reset       bool
	}{
		{name: "accepted", text: sendMsgText},
		{name: "backlog", text: sendMsgText, backlog: true, wantCode: protocol.CodeServerBinaryBusy},
		{name: "unknown conversation", text: sendMsgText, routeErr: conversations.ErrConversationNotFound, wantCode: protocol.CodeConversationNotFound},
		{name: "offline", text: sendMsgText, routeErr: context.Canceled, wantCode: protocol.CodeServerBinaryOffline},
		{name: "foreign attachment", text: sendMsgText, attachments: []string{"other-conversation-id"}, wantCode: protocol.CodeAttachmentNotFound},
		{name: "clear", text: "/clear", reset: true},
	}
	for _, identified := range []bool{false, true} {
		api := "legacy"
		if identified {
			api = "identified"
		}
		for _, tc := range cases {
			t.Run(api+"/"+tc.name, func(t *testing.T) {
				t.Parallel()
				legacy := &fakeEnqueuer{reject: tc.backlog}
				modern := &fakeIdentifiedEnqueuer{fakeEnqueuer: fakeEnqueuer{reject: tc.backlog}}
				var queue Enqueuer = legacy
				if identified {
					queue = modern
				}
				c, recv, _ := newSendMsgConn(t)
				c.Auth().StaticKey = key
				logger, logs := sendMsgCapturingLogger(t)
				reset := &fakeResetter{}
				resolverCalls := 0
				resolve := func(string, string) (string, bool) { resolverCalls++; return "", false }
				router := &stubSessionRouter{tw: &stubTurnWriter{}, err: tc.routeErr}
				req := sendMsgRequest(t, protocol.SendMessagePayload{ConversationID: sendMsgConvID, MessageID: sendMsgMessageID, Text: tc.text, AttachmentIDs: tc.attachments})
				if err := SendMessage(router, queue, resolve, nil, "", nil, reset, logger)(context.Background(), c, req); err != nil {
					t.Fatalf("handler: %v", err)
				}
				wantCalls := 0
				if tc.routeErr == nil && tc.attachments == nil && !tc.reset {
					wantCalls = 1
				}
				calls := len(legacy.calls)
				if identified {
					calls = len(modern.identified)
					if len(modern.calls) != 0 {
						t.Error("identified queue retried through legacy API")
					}
				}
				if calls != wantCalls {
					t.Errorf("enqueue calls = %d, want %d", calls, wantCalls)
				}
				if wantCalls == 1 {
					want := enqueueCall{convID: sendMsgConvID, messageID: sendMsgMessageID, text: tc.text, delivery: tc.text, deviceName: "phone"}
					if identified {
						if got := modern.identified[0]; !reflect.DeepEqual(got, identifiedEnqueueCall{enqueueCall: want, deviceID: key}) {
							t.Errorf("identified call = %#v", got)
						}
					} else if got := legacy.calls[0]; !reflect.DeepEqual(got, want) {
						t.Errorf("legacy call = %#v", got)
					}
				}
				if tc.routeErr != nil && resolverCalls != 0 {
					t.Error("attachment resolver reached before authorization")
				}
				if tc.reset && !reflect.DeepEqual(reset.gotIDs, []string{sendMsgConvID}) {
					t.Errorf("reset ids = %v", reset.gotIDs)
				}
				resp := recv()
				if tc.wantCode == "" {
					assertSendMsgEnvelopeShape(t, resp, protocol.TypeAck)
				} else {
					env := assertSendMsgEnvelopeShape(t, resp, protocol.TypeError)
					var p protocol.ErrorPayload
					if err := json.Unmarshal(env.Payload, &p); err != nil {
						t.Fatal(err)
					}
					if p.Code != tc.wantCode || p.Retryable != (tc.backlog || tc.routeErr == context.Canceled) {
						t.Errorf("error payload = %#v", p)
					}
				}
				for _, value := range []string{key, "plain-token", devices.HashToken("plain-token")} {
					if strings.Contains(logs.String(), value) {
						t.Errorf("identity or credential logged: %q", value)
					}
				}
			})
		}
	}
}
