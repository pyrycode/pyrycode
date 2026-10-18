package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

func TestSendMessage_ResetAliases(t *testing.T) {
	t.Parallel()
	for _, command := range []string{"/clear", "/reset"} {
		for _, outcome := range []string{"success", "failed", "absent"} {
			t.Run(command+"/"+outcome, func(t *testing.T) {
				t.Parallel()
				c, recv, out := newSendMsgConn(t)
				reset := &fakeResetter{onStart: func() {
					if len(out) != 1 {
						t.Error("reset started before its ack")
					}
				}}
				var seam ConversationResetter = reset
				if outcome == "failed" {
					reset.err = errors.New("reset refused")
				} else if outcome == "absent" {
					seam = nil
				}
				var logs bytes.Buffer
				logger := slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
				reg, path := newAutoNameReg(t, nil)
				q, resolver, writer := &fakeEnqueuer{}, &fakeAttachmentResolver{}, &stubTurnWriter{}
				router := &stubSessionRouter{tw: writer}
				h := SendMessage(router, q, resolver.resolve, reg, path, nil, seam, logger)
				req := sendMsgRequest(t, protocol.SendMessagePayload{
					ConversationID: sendMsgConvID, MessageID: sendMsgMessageID,
					Text: command + " private trailing text", AttachmentIDs: []string{attachID(1)},
				})
				if err := h(context.Background(), c, req); err != nil {
					t.Fatalf("handler: %v", err)
				}
				assertSendMsgEnvelopeShape(t, recv(), protocol.TypeAck)
				if len(out) != 0 {
					t.Error("intercept emitted more than its ordinary ack")
				}
				wantCalls := 1
				if outcome == "absent" {
					wantCalls = 0
				}
				if len(reset.gotIDs) != wantCalls || (wantCalls == 1 && reset.gotIDs[0] != sendMsgConvID) {
					t.Errorf("reset calls = %v, want %d for the routed conversation", reset.gotIDs, wantCalls)
				}
				if len(router.gotIDs) != 1 || router.gotIDs[0] != sendMsgConvID {
					t.Errorf("routing calls = %v", router.gotIDs)
				}
				if len(q.calls) != 0 || len(resolver.calls) != 0 || writer.calls != 0 || writer.activateCalls != 0 {
					t.Error("intercept reached queue, attachments or child")
				}
				if storedName(t, reg, sendMsgConvID) != nil || !storedLastUsed(t, reg, sendMsgConvID).Equal(autoNameSeedTime) {
					t.Error("intercept changed conversation name or last-used time")
				}
				if strings.Contains(logs.String(), "private trailing text") {
					t.Error("logs contain message text")
				}
				wantEvents := []string{"send_message.clear_intercepted"}
				if outcome == "failed" {
					wantEvents = append(wantEvents, "send_message.clear_reset_failed")
				} else if outcome == "absent" {
					wantEvents = append(wantEvents, "send_message.clear_no_resetter")
				}
				lines := strings.Split(strings.TrimSpace(logs.String()), "\n")
				if len(lines) != len(wantEvents) {
					t.Fatalf("log records = %d, want %d: %s", len(lines), len(wantEvents), logs.String())
				}
				for i, line := range lines {
					var record map[string]any
					if err := json.Unmarshal([]byte(line), &record); err != nil {
						t.Fatal(err)
					}
					if record["event"] != wantEvents[i] || record["command"] != command {
						t.Errorf("log event/command = %v/%v, want %s/%s", record["event"], record["command"], wantEvents[i], command)
					}
				}
			})
		}
	}
}

func TestSendMessage_ResetAliasValidation(t *testing.T) {
	t.Parallel()
	for _, command := range []string{"/clear", "/reset"} {
		for _, scenario := range []string{"unknown", "unbound", "too many attachments"} {
			t.Run(command+"/"+scenario, func(t *testing.T) {
				t.Parallel()
				router, reset, q := &stubSessionRouter{}, &fakeResetter{}, &fakeEnqueuer{}
				p := protocol.SendMessagePayload{ConversationID: sendMsgConvID, Text: command}
				wantCode := protocol.CodeConversationNotFound
				switch scenario {
				case "unknown":
					router.err = conversations.ErrConversationNotFound
				case "unbound":
					router.err = errors.New("no bound session")
					wantCode = protocol.CodeServerBinaryOffline
				default:
					p.AttachmentIDs = make([]string, protocol.MaxAttachmentIDsPerMessage+1)
					wantCode = protocol.CodeProtocolMalformed
				}
				c, recv, _ := newSendMsgConn(t)
				h := SendMessage(router, q, nil, nil, "", nil, reset, sendMsgLogger(t))
				if err := h(context.Background(), c, sendMsgRequest(t, p)); err != nil {
					t.Fatal(err)
				}
				env := assertSendMsgEnvelopeShape(t, recv(), protocol.TypeError)
				if got := sendMsgErrorPayload(t, env); got.Code != wantCode || got.Retryable != (scenario == "unbound") {
					t.Errorf("error = %+v, want code %s with retryable=%v", got, wantCode, scenario == "unbound")
				}
				if len(reset.gotIDs) != 0 || len(q.calls) != 0 || (scenario == "too many attachments" && len(router.gotIDs) != 0) {
					t.Error("invalid frame reached routing, reset or enqueue")
				}
			})
		}
	}
}
