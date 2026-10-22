//go:build e2e

package e2e

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/scenario"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakephone"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakerelay"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/paireddevice"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// The arrival proof for #2431: a real daemon, a real relay leg and a real paired
// phone drive one request_context_usage end to end, through the relay handler, the
// cmd/pyry resolver, a live fake-claude child and back as an encrypted frame.
//
// WHAT ONLY THIS TIER CAN SEE is the CORRELATION, and it is the whole reason the leg
// exists. Every layer below has its own coverage, but the automatic post-turn reading
// (#2371) travels the SAME outbound lane as this reply and carries the SAME frame
// type — so "a context_usage arrived" proves nothing here. Envelope.InReplyTo is the
// only thing that separates them, and nothing short of a wired daemon emitting both
// kinds on one connection can show that it does. This test therefore drains a turn
// FIRST, precisely so an unsolicited reading is in flight before the request is sent.
//
// THE DETAIL VALUE IS DELIBERATELY NOT ASSERTED HERE. fakeclaude's
// contextUsageRequestID accepts "summary" and "full" alike and answers both from one
// canned payload, so this tier cannot distinguish them; the daemon's choice of "full"
// is pinned where it is decided, by cmd/pyry's TestContextUsageResolver_AsksAtFullDetail.
const (
	reqCtxUsageInitialUUID = "24310000-0000-4000-8000-000000000001"
	reqCtxUsageConvID      = "24310000-0000-4000-8000-000000000002"
	reqCtxUsageUnhostedID  = "24310000-0000-4000-8000-00000000dead"
	reqCtxUsageWarmupText  = "e2e-ctxusage-request:warmup\n"
	reqCtxUsageWarmupID    = uint64(24310)
	reqCtxUsageRequestID   = uint64(24311)
	reqCtxUsageUnhostedReq = uint64(24312)
)

// drainReqCtxUsageTurn runs one turn to completion and reports how many context_usage
// frames arrived UNSOLICITED (in_reply_to absent). A reply correlated to a request is
// never counted here — that separation is the property the test is about.
//
// IT DRAINS PAST turn_end, and must: the post-turn reading is SOLICITED BY turn_end
// (cmd/pyry's turnEndContextUsageRequester asks on that event), so it always FOLLOWS
// it. A loop that stopped at turn_end would collect zero of them every time and read
// as a regression rather than as a broken test — the trap #2371's own leg documents.
// The settle window after turn_end is what turns "none yet" into "none".
func drainReqCtxUsageTurn(t *testing.T, phone mcpQueryPhone, text string) (unsolicited int) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	settled := false
	for {
		env, ok := phone.source.Next(phone.next, deadline)
		if !ok {
			if settled {
				return unsolicited
			}
			t.Fatal("source turn did not settle")
		}
		if env.Type == protocol.TypeError {
			t.Fatal("turn refused")
		}
		if env.Type == protocol.TypeContextUsage && env.InReplyTo == nil {
			unsolicited++
		}
		if !settled && phone.source.Ended(reqCtxUsageConvID) {
			if !strings.Contains(phone.source.Text(reqCtxUsageConvID), strings.TrimSpace(text)) {
				t.Fatal("context turn lost fresh echo")
			}
			settled = true
			deadline = time.Now().Add(10 * time.Second)
		}
		if settled && unsolicited > 0 {
			return unsolicited
		}
	}
}

func TestRelayV2_RequestContextUsageAnswersCorrelatedReading(t *testing.T) {
	home := shortHome(t)
	fr := fakerelay.New(relayTestLogger())
	t.Cleanup(func() { _ = fr.Close() })
	relayURL := fr.URL() + "/v2/server"
	payload, err := paireddevice.Setup(paireddevice.Config{
		Home:         home,
		InstanceName: "test",
		Relay:        relayURL,
		DeviceName:   "phone-ctxusage",
	})
	if err != nil {
		t.Fatalf("setup phone: %v", err)
	}
	pubKey, err := base64.StdEncoding.DecodeString(payload.ServerStaticPubkey)
	if err != nil {
		t.Fatalf("decode server static pubkey: %v", err)
	}

	seedBoundConversation(t, home, reqCtxUsageConvID, reqCtxUsageInitialUUID)
	h := StartStreamInteractiveWithRelay(t, home, reqCtxUsageInitialUUID, relayURL)
	t.Cleanup(func() { h.Stop(t) })
	serverID := readPersistedServerID(t, home)
	waitBinaryHello(t, fr, serverID)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	phoneClient, err := fakephone.Dial(ctx, fr.URL(), serverID, payload.Token, "phone-ctxusage")
	if err != nil {
		t.Fatalf("dial phone: %v", err)
	}
	t.Cleanup(func() { _ = phoneClient.Close() })
	send, recv := driveHandshakeToOpenDaemonInteractive(t, phoneClient, pubKey, payload.Token)
	phone := mcpQueryPhone{client: phoneClient, send: send, recv: recv, source: scenario.NewSourceObserver(t, home), next: scenario.PollingReceive(t, phoneClient, recv)}

	// A completed turn does two things at once: it gives the conversation a live
	// child to ask, and it puts an UNSOLICITED context_usage on the same lane the
	// reply will travel — which is what makes the correlation assertion below
	// meaningful rather than vacuous.
	phone.source.Watch(reqCtxUsageConvID)
	sendSealedEnvelope(t, phone.client, phone.send, protocol.Envelope{
		ID:   reqCtxUsageWarmupID,
		Type: protocol.TypeSendMessage,
		TS:   time.Now().UTC(),
		Payload: mustJSON(t, protocol.SendMessagePayload{
			ConversationID: reqCtxUsageConvID,
			MessageID:      "message-ctxusage-warmup",
			Text:           reqCtxUsageWarmupText,
		}),
	})
	if got := drainReqCtxUsageTurn(t, phone, reqCtxUsageWarmupText); got == 0 {
		t.Fatal("no unsolicited context_usage followed the warmup turn; the lane this reply shares is not live, so the correlation claim below would be vacuous")
	}

	// AC-1: a hosted conversation is answered with one context_usage correlated to
	// the request.
	sendSealedEnvelope(t, phone.client, phone.send, protocol.Envelope{
		ID:   reqCtxUsageRequestID,
		Type: protocol.TypeRequestContextUsage,
		TS:   time.Now().UTC(),
		Payload: mustJSON(t, protocol.RequestContextUsagePayload{
			ConversationID: reqCtxUsageConvID,
		}),
	})

	var reading protocol.ContextUsagePayload
	deadline := time.Now().Add(20 * time.Second)
	for {
		env, ok := nextAttachmentEnvelope(t, phone.client, phone.recv, deadline)
		if !ok {
			t.Fatal("no correlated context_usage reply arrived")
		}
		if env.Type == protocol.TypeError {
			t.Fatalf("request_context_usage returned an error: %s", env.Payload)
		}
		if env.Type != protocol.TypeContextUsage || env.InReplyTo == nil {
			// An unsolicited post-turn reading, or another frame on the lane. Skipping
			// it IS the test: the reply is identified by its correlation and by
			// nothing else.
			continue
		}
		if *env.InReplyTo != reqCtxUsageRequestID {
			t.Fatalf("context_usage in_reply_to = %d, want %d", *env.InReplyTo, reqCtxUsageRequestID)
		}
		if env.EventID != nil {
			t.Fatalf("reply event_id = %v, want nil: a requester-only reply must not enter the replay ring", env.EventID)
		}
		if err := json.Unmarshal(env.Payload, &reading); err != nil {
			t.Fatalf("decode context_usage reply: %v", err)
		}
		break
	}

	// The conversation id is the DAEMON's, taken from its registry record rather than
	// echoed from the request — the two happen to agree here, which is exactly why the
	// reading's own scalars are checked too: they can only have come from the child.
	if reading.ConversationID != reqCtxUsageConvID {
		t.Errorf("reply conversation_id = %q, want %q", reading.ConversationID, reqCtxUsageConvID)
	}
	if reading.Model == "" || reading.TotalTokens == 0 || reading.MaxTokens == 0 {
		t.Errorf("reply = %+v, want a populated reading from the live child", reading)
	}
	if len(reading.Categories) == 0 {
		t.Error("reply carries no categories; the reading did not come from the child")
	}

	// AC-3: a conversation this daemon does not host is refused distinguishably, and
	// is NOT met with silence.
	sendSealedEnvelope(t, phone.client, phone.send, protocol.Envelope{
		ID:   reqCtxUsageUnhostedReq,
		Type: protocol.TypeRequestContextUsage,
		TS:   time.Now().UTC(),
		Payload: mustJSON(t, protocol.RequestContextUsagePayload{
			ConversationID: reqCtxUsageUnhostedID,
		}),
	})

	deadline = time.Now().Add(20 * time.Second)
	for {
		env, ok := nextAttachmentEnvelope(t, phone.client, phone.recv, deadline)
		if !ok {
			t.Fatal("an unhosted conversation was met with silence; every refusal is answered")
		}
		if env.Type == protocol.TypeContextUsage && env.InReplyTo != nil && *env.InReplyTo == reqCtxUsageUnhostedReq {
			t.Fatal("an unhosted conversation was answered with a reading")
		}
		if env.Type != protocol.TypeError || env.InReplyTo == nil || *env.InReplyTo != reqCtxUsageUnhostedReq {
			continue
		}
		var errPayload protocol.ErrorPayload
		if err := json.Unmarshal(env.Payload, &errPayload); err != nil {
			t.Fatalf("decode error reply: %v", err)
		}
		if errPayload.Code != protocol.CodeConversationNotFound {
			t.Errorf("error code = %q, want %q", errPayload.Code, protocol.CodeConversationNotFound)
		}
		if strings.Contains(errPayload.Message, reqCtxUsageUnhostedID) {
			t.Errorf("error message %q echoes the requested id; it must be a static constant", errPayload.Message)
		}
		break
	}
}
