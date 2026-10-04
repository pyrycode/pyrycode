package relay

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/dispatch"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

const stopTaskConv = "REMOTE_CONVERSATION_2791"
const stopTaskID = "REMOTE_TASK_2791"

type fakeBackgroundTaskStopper struct {
	mu        sync.Mutex
	calls     [][2]string
	outcome   BackgroundTaskStopOutcome
	blockOn   string
	entered   chan struct{}
	release   chan struct{}
	cancelled chan struct{}
}

func (f *fakeBackgroundTaskStopper) StopBackgroundTask(ctx context.Context, conversationID, taskID string) BackgroundTaskStopOutcome {
	f.mu.Lock()
	f.calls = append(f.calls, [2]string{conversationID, taskID})
	f.mu.Unlock()
	if conversationID == f.blockOn && f.blockOn != "" {
		f.entered <- struct{}{}
		select {
		case <-f.release:
		case <-ctx.Done():
			if f.cancelled != nil {
				close(f.cancelled)
			}
			return BackgroundTaskStopCannotActOnConversation
		}
	}
	return f.outcome
}

func (f *fakeBackgroundTaskStopper) snapshot() [][2]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][2]string(nil), f.calls...)
}

func stopTaskConfig(t *testing.T, stopper BackgroundTaskStopper) (V2SessionConfig, chan protocol.RoutingEnvelope, *v2Recorder, []byte) {
	t.Helper()
	priv, pub := genV2Keypair(t)
	frames := make(chan protocol.RoutingEnvelope, 16)
	rec := &v2Recorder{}
	return V2SessionConfig{
		Frames: frames, Outbound: rec.outbound, StaticPriv: priv,
		Devices: v2PairedRegistry(t, v2TestToken), ServerID: v2TestServerID,
		Logger: silentLogger(), BackgroundTaskStopper: stopper,
	}, frames, rec, pub
}

// Run's inert gates must prevent even queueing: reply silence alone cannot prove
// that an invalid typed payload was never passed to a worker.
func TestV2Session_StopBackgroundTask_InertGatesStayOnRun(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name               string
		wired, interactive bool
	}{
		{"nil seam", false, true}, {"noninteractive", true, false}, {"both", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeBackgroundTaskStopper{}
			var stopper BackgroundTaskStopper
			if tc.wired {
				stopper = fake
			}
			cfg, _, _, _ := stopTaskConfig(t, stopper)
			mgr, err := NewV2SessionManager(cfg)
			if err != nil {
				t.Fatal(err)
			}
			s := &V2Session{interactive: tc.interactive, appFrames: make(chan appFrameJob, 8)}
			for _, payload := range []string{`{"conversation_id":"c","task_id":"t"}`, `{"conversation_id":["SECRET"]}`, `"SECRET"`} {
				frame := []byte(`{"id":1,"type":"stop_background_task","payload":` + payload + `}`)
				mgr.dispatchAppFrame(context.Background(), s, frame)
			}
			if len(s.appFrames) != 0 {
				t.Fatal("inert frame was enqueued")
			}
			if len(fake.snapshot()) != 0 {
				t.Fatal("inert frame called seam")
			}
		})
	}
}

func assertStopTaskRefusal(t *testing.T, env protocol.Envelope, id uint64, conversationID string) {
	t.Helper()
	if env.Type != protocol.TypeError || env.InReplyTo == nil || *env.InReplyTo != id || env.EventID != nil {
		t.Fatalf("refusal envelope = %#v", env)
	}
	var got map[string]any
	if err := json.Unmarshal(env.Payload, &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"code": "stop_background_task.refused", "message": "background task stop refused", "retryable": false, "conversation_id": conversationID}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("refusal = %#v, want %#v", got, want)
	}
}

func TestV2Session_StopBackgroundTask_Contract(t *testing.T) {
	t.Parallel()
	valid := `{"conversation_id":"` + stopTaskConv + `","task_id":"` + stopTaskID + `"}`
	for _, tc := range []struct {
		name, payload  string
		outcome        BackgroundTaskStopOutcome
		calls, replies int
		conversationID string
	}{
		{name: "absent"},
		{name: "null", payload: `null`},
		{name: "string", payload: `"REMOTE_PAYLOAD_2791"`},
		{name: "array", payload: `["REMOTE_PAYLOAD_2791"]`},
		{name: "bad conversation type", payload: `{"conversation_id":2791,"task_id":"REMOTE_TASK_2791"}`},
		{name: "bad task type", payload: `{"conversation_id":"REMOTE_CONVERSATION_2791","task_id":{}}`},
		{name: "missing conversation", payload: `{"task_id":"REMOTE_TASK_2791"}`},
		{name: "empty conversation", payload: `{"conversation_id":"","task_id":"REMOTE_TASK_2791"}`},
		{name: "empty object", payload: `{}`},
		{name: "missing task", payload: `{"conversation_id":"REMOTE_CONVERSATION_2791"}`, replies: 1, conversationID: stopTaskConv},
		{name: "empty task unknown conversation", payload: `{"conversation_id":"REMOTE_UNKNOWN_2791","task_id":""}`, replies: 1, conversationID: "REMOTE_UNKNOWN_2791"},
		{name: "accepted", payload: valid, outcome: BackgroundTaskStopAccepted, calls: 1},
		{name: "cannot act on conversation", payload: valid, outcome: BackgroundTaskStopCannotActOnConversation, calls: 1},
		{name: "refused", payload: valid, outcome: BackgroundTaskStopRefused, calls: 1, replies: 1, conversationID: stopTaskConv},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fake := &fakeBackgroundTaskStopper{outcome: tc.outcome}
			cfg, frames, rec, pub := stopTaskConfig(t, fake)
			logger, logBuf := bufferLogger()
			cfg.Logger = logger
			var routed atomic.Bool
			// Membership must stay in the seam, even for the missing-task refusal.
			cfg.KnownConversation = func(string) bool { t.Error("relay consulted membership"); return false }
			cfg.Handlers = map[string]dispatch.Handler{
				protocol.TypeStopBackgroundTask: func(context.Context, *dispatch.Conn, protocol.Envelope) error { routed.Store(true); return nil },
				protocol.TypeSendMessage: func(ctx context.Context, c *dispatch.Conn, env protocol.Envelope) error {
					return c.Reply(ctx, env, protocol.TypeAck, json.RawMessage(`{}`))
				},
			}
			mgr, stop := startManager(t, cfg)
			t.Cleanup(stop)
			send, recv := openModalConn(t, mgr, frames, rec, pub, "requester", []string{protocol.CapabilityInteractive})
			openModalConn(t, mgr, frames, rec, pub, "peer", []string{protocol.CapabilityInteractive})
			var payload json.RawMessage
			if tc.payload != "" {
				payload = json.RawMessage(tc.payload)
			}
			frames <- sealAppFrameConn(t, send, "requester", protocol.Envelope{
				ID: 2791, Type: protocol.TypeStopBackgroundTask, TS: time.Now().UTC(), Payload: payload,
			})
			// An ordinary FIFO handler reply proves even silent stop outcomes finished.
			sendMCPActuation(t, frames, send, "requester", protocol.TypeSendMessage, 2792, `{}`)
			if tc.replies == 1 {
				assertStopTaskRefusal(t, waitMCPStatusReply(t, rec, "requester", recv, 0), 2791, tc.conversationID)
			}
			barrier := waitMCPStatusReply(t, rec, "requester", recv, tc.replies)
			if barrier.Type != protocol.TypeAck || barrier.InReplyTo == nil || *barrier.InReplyTo != 2792 {
				t.Fatalf("barrier = %#v", barrier)
			}
			if got := len(noiseMsgsForConn(t, rec, "requester")); got != tc.replies+1 {
				t.Fatalf("replies = %d", got)
			}
			if got := len(noiseMsgsForConn(t, rec, "peer")); got != 0 {
				t.Fatalf("peer received %d replies", got)
			}
			calls := fake.snapshot()
			if len(calls) != tc.calls {
				t.Fatalf("calls = %#v", calls)
			}
			if tc.calls == 1 && calls[0] != [2]string{stopTaskConv, stopTaskID} {
				t.Fatalf("ids = %#v", calls)
			}
			if routed.Load() {
				t.Fatal("verb reached v1 handler")
			}
			logs := logBuf.String()
			for _, forbidden := range []string{"REMOTE_", "cannot unmarshal", "UnmarshalTypeError", "invalid character"} {
				if strings.Contains(logs, forbidden) {
					t.Errorf("log contains %q: %s", forbidden, logs)
				}
			}
		})
	}
}

func TestV2Session_StopBackgroundTask_BlockingSeam(t *testing.T) {
	t.Parallel()
	for _, cancelInstead := range []bool{false, true} {
		t.Run(map[bool]string{false: "release and reply", true: "shutdown cancels"}[cancelInstead], func(t *testing.T) {
			fake := &fakeBackgroundTaskStopper{outcome: BackgroundTaskStopRefused, blockOn: stopTaskConv,
				entered: make(chan struct{}, 1), release: make(chan struct{}), cancelled: make(chan struct{})}
			cfg, frames, rec, pub := stopTaskConfig(t, fake)
			interrupt := &fakeInterrupter{}
			cfg.Interrupter = interrupt
			mgr, stop := startManager(t, cfg)
			t.Cleanup(stop)
			var once sync.Once
			t.Cleanup(func() { once.Do(func() { close(fake.release) }) })
			sendA, recvA := openModalConn(t, mgr, frames, rec, pub, "blocked", []string{protocol.CapabilityInteractive})
			sendB, recvB := openModalConn(t, mgr, frames, rec, pub, "free", []string{protocol.CapabilityInteractive})
			sendMCPActuation(t, frames, sendA, "blocked", protocol.TypeStopBackgroundTask, 2793,
				`{"conversation_id":"REMOTE_CONVERSATION_2791","task_id":"REMOTE_TASK_2791"}`)
			select {
			case <-fake.entered:
			case <-time.After(2 * time.Second):
				t.Fatal("seam never entered")
			}
			// Same connection's interrupt stays inline on Run while its worker blocks.
			sendMCPActuation(t, frames, sendA, "blocked", protocol.TypeInterrupt, 2794, `{"conversation_id":"interrupt-conversation"}`)
			sendMCPActuation(t, frames, sendB, "free", protocol.TypeStopBackgroundTask, 2795,
				`{"conversation_id":"free-conversation","task_id":"free-task"}`)
			assertStopTaskRefusal(t, waitMCPStatusReply(t, rec, "free", recvB, 0), 2795, "free-conversation")
			if got := interrupt.conversationIDs(); !reflect.DeepEqual(got, []string{"interrupt-conversation"}) {
				t.Fatalf("interrupt did not progress: %#v", got)
			}
			if len(noiseMsgsForConn(t, rec, "blocked")) != 0 {
				t.Fatal("blocked conn replied early")
			}
			if cancelInstead {
				stop()
				select {
				case <-fake.cancelled:
				case <-time.After(2 * time.Second):
					t.Fatal("seam context was not cancelled")
				}
			} else {
				once.Do(func() { close(fake.release) })
				assertStopTaskRefusal(t, waitMCPStatusReply(t, rec, "blocked", recvA, 0), 2793, stopTaskConv)
				if len(noiseMsgsForConn(t, rec, "blocked")) != 1 {
					t.Fatal("refusal was not exactly once")
				}
			}
		})
	}
}
