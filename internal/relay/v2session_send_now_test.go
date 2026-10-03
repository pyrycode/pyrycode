package relay

import (
	"encoding/json"
	"log/slog"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/protocol"
)

// fakeQueueSender is the relay-side double for QueueSender, shaped like
// fakeQueueRemover: it records every (convID, id) call and returns a canned bool.
type fakeQueueSender struct {
	mu    sync.Mutex
	calls []dequeueCall
	sent  bool
}

func (f *fakeQueueSender) SendNow(convID string, id uint64) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, dequeueCall{convID: convID, id: id})
	return f.sent
}

func (f *fakeQueueSender) snapshot() []dequeueCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]dequeueCall(nil), f.calls...)
}

func sendNowPayload(t *testing.T, convID string, id uint64) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(protocol.SendQueuedNowPayload{ConversationID: convID, QueuedMsgID: id})
	if err != nil {
		t.Fatalf("marshal send_queued_now payload: %v", err)
	}
	return raw
}

// send_queued_now reaches QueueSender.SendNow once with the decoded ids on an
// interactive conn, whatever SendNow answers, and never on a non-interactive one.
// The handler emits nothing to the conn: the acknowledgement is queue_state.
// The log never carries anything but the fields dequeue_message logs.
func TestV2Session_SendQueuedNow_SendsByCapability(t *testing.T) {
	t.Parallel()

	const (
		convID = "11111111-1111-4111-8111-111111111111"
		msgID  = uint64(42)
	)
	cases := []struct {
		name      string
		caps      []string
		sent      bool
		wantCalls []dequeueCall
	}{
		{"interactive delivered (true)", []string{protocol.CapabilityInteractive}, true, []dequeueCall{{convID, msgID}}},
		{"interactive no-op is success (false)", []string{protocol.CapabilityInteractive}, false, []dequeueCall{{convID, msgID}}},
		{"non-interactive is inert", nil, true, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			respPriv, respPub := genV2Keypair(t)
			fake := &fakeQueueSender{sent: tc.sent}
			logs := &lockedBuffer{}
			frames := make(chan protocol.RoutingEnvelope, 8)
			rec := &v2Recorder{}
			mgr, stop := startManager(t, V2SessionConfig{
				Frames:      frames,
				Outbound:    rec.outbound,
				StaticPriv:  respPriv,
				Devices:     v2PairedRegistry(t, v2TestToken),
				ServerID:    v2TestServerID,
				Logger:      slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
				QueueSender: fake,
			})
			t.Cleanup(stop)

			send, _ := openModalConn(t, mgr, frames, rec, respPub, "c-int", tc.caps)
			frames <- sealAppFrameConn(t, send, "c-int", protocol.Envelope{
				Type:    protocol.TypeSendQueuedNow,
				TS:      time.Now().UTC(),
				Payload: sendNowPayload(t, convID, msgID),
			})
			// Barrier for Run's gate; the call itself runs on c-int's worker, so an
			// expected call is awaited rather than assumed.
			openModalConn(t, mgr, frames, rec, respPub, "c-barrier", []string{protocol.CapabilityInteractive})
			deadline := time.Now().Add(5 * time.Second)
			for len(fake.snapshot()) < len(tc.wantCalls) && time.Now().Before(deadline) {
				time.Sleep(5 * time.Millisecond)
			}

			if got := fake.snapshot(); !reflect.DeepEqual(got, tc.wantCalls) {
				t.Errorf("SendNow calls = %+v, want %+v", got, tc.wantCalls)
			}
			if got := noiseMsgsForConn(t, rec, "c-int"); len(got) != 0 {
				t.Errorf("handler emitted %d app frame(s) to c-int, want 0", len(got))
			}
			for _, line := range strings.Split(logs.String(), "\n") {
				if !strings.Contains(line, "v2.send_now") {
					continue
				}
				var rec map[string]any
				if err := json.Unmarshal([]byte(line), &rec); err != nil {
					t.Fatalf("log line is not JSON: %v", err)
				}
				for k := range rec {
					switch k {
					case "time", "level", "msg", "event", "conn_id", "conversation_id", "queued_msg_id":
					default:
						t.Errorf("send_queued_now log carries unexpected field %q", k)
					}
				}
			}
		})
	}
}

// A nil QueueSender leaves send_queued_now inert and the manager serving.
func TestV2Session_SendQueuedNow_NilSenderInert(t *testing.T) {
	t.Parallel()

	respPriv, respPub := genV2Keypair(t)
	frames := make(chan protocol.RoutingEnvelope, 8)
	rec := &v2Recorder{}
	mgr, stop := startManager(t, V2SessionConfig{
		Frames:     frames,
		Outbound:   rec.outbound,
		StaticPriv: respPriv,
		Devices:    v2PairedRegistry(t, v2TestToken),
		ServerID:   v2TestServerID,
		Logger:     silentLogger(),
	})
	t.Cleanup(stop)

	send, _ := openModalConn(t, mgr, frames, rec, respPub, "c-int", []string{protocol.CapabilityInteractive})
	frames <- sealAppFrameConn(t, send, "c-int", protocol.Envelope{
		Type:    protocol.TypeSendQueuedNow,
		TS:      time.Now().UTC(),
		Payload: sendNowPayload(t, "11111111-1111-4111-8111-111111111111", 1),
	})
	openModalConn(t, mgr, frames, rec, respPub, "c-barrier", []string{protocol.CapabilityInteractive})
}
