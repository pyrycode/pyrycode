package relay

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/dispatch"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// --- #723 inbound dequeue_message → msgqueue.Remove fixtures ---

// dequeueCall records the arguments of one QueueRemover.Remove call so a test can
// assert what the manager routed across the seam.
type dequeueCall struct {
	convID string
	id     uint64
}

// fakeQueueRemover is a relay-side test double for QueueRemover: it records every
// (convID, id) call and returns a programmable bool. The mutex guards the
// cross-goroutine access (the Run goroutine writes via Remove, the test goroutine
// reads via snapshot), mirroring fakeInterrupter.
type fakeQueueRemover struct {
	mu      sync.Mutex
	calls   []dequeueCall
	removed bool // canned Remove return value
}

func (f *fakeQueueRemover) Remove(convID string, id uint64) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, dequeueCall{convID: convID, id: id})
	return f.removed
}

func (f *fakeQueueRemover) snapshot() []dequeueCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]dequeueCall(nil), f.calls...)
}

// dequeuePayload marshals a DequeueMessagePayload into the raw JSON an envelope
// carries.
func dequeuePayload(t *testing.T, convID string, id uint64) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(protocol.DequeueMessagePayload{ConversationID: convID, QueuedMsgID: id})
	if err != nil {
		t.Fatalf("marshal dequeue payload: %v", err)
	}
	return raw
}

// lockedBuffer is a goroutine-safe bytes.Buffer: the manager logs on its Run
// goroutine while the test reads on its own. Used by the never-echo assertion.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// TestV2Session_DequeueMessage_RemovesByCapability drives an inbound
// dequeue_message frame through the real Frames/Run loop and asserts the manager
// reaches msgqueue.Remove exactly once with the decoded (conversation_id,
// queued_msg_id) for an interactive conn — whether Remove returns true (AC-1) or
// false (AC-2, the no-op-is-success path) — and not at all for a non-interactive
// conn (AC-3, the new inbound capability gate). In every case the handler emits
// no reply and no broadcast (AC-2/AC-4): queue_state convergence is the automatic
// OnChange→#722-producer path, never a direct re-emit from the handler. After the
// dequeue frame a second conn is opened as a barrier: because Frames is a single
// FIFO drained by the one Run goroutine, the dequeue (enqueued first) is fully
// handled before the barrier conn opens, so the recorded calls are final.
func TestV2Session_DequeueMessage_RemovesByCapability(t *testing.T) {
	t.Parallel()

	const (
		convID = "11111111-1111-4111-8111-111111111111"
		msgID  = uint64(42)
	)
	cases := []struct {
		name      string
		caps      []string
		removed   bool
		wantCalls []dequeueCall
	}{
		{"interactive removes (true)", []string{protocol.CapabilityInteractive}, true, []dequeueCall{{convID, msgID}}},
		{"interactive no-op is success (false)", []string{protocol.CapabilityInteractive}, false, []dequeueCall{{convID, msgID}}},
		{"non-interactive is inert", nil, true, nil},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			respPriv, respPub := genV2Keypair(t)
			fake := &fakeQueueRemover{removed: tc.removed}
			frames := make(chan protocol.RoutingEnvelope, 8)
			rec := &v2Recorder{}
			mgr, stop := startManager(t, V2SessionConfig{
				Frames:       frames,
				Outbound:     rec.outbound,
				StaticPriv:   respPriv,
				Devices:      v2PairedRegistry(t, v2TestToken),
				ServerID:     v2TestServerID,
				Logger:       silentLogger(),
				QueueRemover: fake,
			})
			t.Cleanup(stop)

			send, _ := openModalConn(t, mgr, frames, rec, respPub, "c-int", tc.caps)
			frames <- sealAppFrameConn(t, send, "c-int", protocol.Envelope{
				Type:    protocol.TypeDequeueMessage,
				TS:      time.Now().UTC(),
				Payload: dequeuePayload(t, convID, msgID),
			})

			// Barrier: the dequeue is enqueued before this conn's noise_init, so
			// once the barrier conn is open the dequeue has been handled.
			openModalConn(t, mgr, frames, rec, respPub, "c-barrier", []string{protocol.CapabilityInteractive})

			if got := fake.snapshot(); !reflect.DeepEqual(got, tc.wantCalls) {
				t.Errorf("Remove calls = %+v, want %+v", got, tc.wantCalls)
			}
			// No reply, no broadcast: the handler never pushes a noise_msg app frame
			// to the conn — queue_state is the producer's job (AC-2/AC-4).
			if got := noiseMsgsForConn(t, rec, "c-int"); len(got) != 0 {
				t.Errorf("handler emitted %d app frame(s) to c-int, want 0 (no reply/broadcast)", len(got))
			}
		})
	}
}

// TestV2Session_DequeueMessage_NilRemoverInert proves a nil QueueRemover
// (foreground / pre-wire) makes an interactive dequeue_message inert: no panic,
// the manager keeps serving. If handleDequeueMessage mishandled the nil seam, the
// Run goroutine would be dead and the barrier conn would never open.
func TestV2Session_DequeueMessage_NilRemoverInert(t *testing.T) {
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
		// QueueRemover intentionally nil.
	})
	t.Cleanup(stop)

	send, _ := openModalConn(t, mgr, frames, rec, respPub, "c-int", []string{protocol.CapabilityInteractive})
	frames <- sealAppFrameConn(t, send, "c-int", protocol.Envelope{
		Type:    protocol.TypeDequeueMessage,
		TS:      time.Now().UTC(),
		Payload: dequeuePayload(t, "11111111-1111-4111-8111-111111111111", 1),
	})

	// Barrier: opening succeeds only if Run processed the dequeue without crashing
	// or hanging on the nil seam.
	openModalConn(t, mgr, frames, rec, respPub, "c-barrier", []string{protocol.CapabilityInteractive})
}

// TestV2Session_DequeueMessage_DecodeTolerant proves a malformed payload is
// tolerated: the handler does not panic, it still reaches Remove with zero-value
// fields (the engine's safe no-op), and the raw payload bytes never appear in any
// log line (the never-echo discipline — encoding/json can quote attacker bytes
// into its error). The payload is valid JSON (so the envelope decodes and the
// type switch routes to handleDequeueMessage) but malformed as a
// DequeueMessagePayload: queued_msg_id is a string where a uint64 is expected, so
// json.Unmarshal fails and leaves the struct zero — Remove("", 0).
func TestV2Session_DequeueMessage_DecodeTolerant(t *testing.T) {
	t.Parallel()

	const marker = "dequeue-marker-d3adb33f"

	respPriv, respPub := genV2Keypair(t)
	fake := &fakeQueueRemover{removed: false}
	frames := make(chan protocol.RoutingEnvelope, 8)
	rec := &v2Recorder{}
	logBuf := &lockedBuffer{}
	logger := slog.New(slog.NewTextHandler(logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	mgr, stop := startManager(t, V2SessionConfig{
		Frames:       frames,
		Outbound:     rec.outbound,
		StaticPriv:   respPriv,
		Devices:      v2PairedRegistry(t, v2TestToken),
		ServerID:     v2TestServerID,
		Logger:       logger,
		QueueRemover: fake,
	})
	t.Cleanup(stop)

	send, _ := openModalConn(t, mgr, frames, rec, respPub, "c-int", []string{protocol.CapabilityInteractive})
	frames <- sealAppFrameConn(t, send, "c-int", protocol.Envelope{
		Type:    protocol.TypeDequeueMessage,
		TS:      time.Now().UTC(),
		Payload: json.RawMessage(`{"queued_msg_id":"` + marker + `"}`),
	})

	// Barrier guarantees the dequeue (incl. its logging) completed.
	openModalConn(t, mgr, frames, rec, respPub, "c-barrier", []string{protocol.CapabilityInteractive})

	if got := fake.snapshot(); !reflect.DeepEqual(got, []dequeueCall{{convID: "", id: 0}}) {
		t.Errorf("Remove calls = %+v, want one call with zero-value fields", got)
	}
	if logs := logBuf.String(); strings.Contains(logs, marker) {
		t.Errorf("log output leaked raw payload bytes (marker %q present):\n%s", marker, logs)
	}
}

// TestV2Session_DequeueMessage_InterceptedNotRouted proves the v2-control
// discriminator in dispatchAppFrame routes dequeue_message to handleDequeueMessage
// (the QueueRemover seam fires) BEFORE dispatch.Route, so a handler registered in
// the application dispatch table under the same type is never consulted.
func TestV2Session_DequeueMessage_InterceptedNotRouted(t *testing.T) {
	t.Parallel()

	respPriv, respPub := genV2Keypair(t)
	fake := &fakeQueueRemover{removed: true}
	var routedToTable atomic.Bool
	frames := make(chan protocol.RoutingEnvelope, 8)
	rec := &v2Recorder{}
	mgr, stop := startManager(t, V2SessionConfig{
		Frames:       frames,
		Outbound:     rec.outbound,
		StaticPriv:   respPriv,
		Devices:      v2PairedRegistry(t, v2TestToken),
		ServerID:     v2TestServerID,
		Logger:       silentLogger(),
		QueueRemover: fake,
		Handlers: map[string]dispatch.Handler{
			// A sentinel under the same type: if interception failed and the frame
			// fell through to dispatch.Route, this would fire.
			protocol.TypeDequeueMessage: func(_ context.Context, _ *dispatch.Conn, _ protocol.Envelope) error {
				routedToTable.Store(true)
				return nil
			},
		},
	})
	t.Cleanup(stop)

	send, _ := openModalConn(t, mgr, frames, rec, respPub, "c-int", []string{protocol.CapabilityInteractive})
	frames <- sealAppFrameConn(t, send, "c-int", protocol.Envelope{
		Type:    protocol.TypeDequeueMessage,
		TS:      time.Now().UTC(),
		Payload: dequeuePayload(t, "11111111-1111-4111-8111-111111111111", 7),
	})

	openModalConn(t, mgr, frames, rec, respPub, "c-barrier", []string{protocol.CapabilityInteractive})

	if got := fake.snapshot(); len(got) != 1 {
		t.Errorf("Remove calls = %d, want 1 (interception reached the handler)", len(got))
	}
	if routedToTable.Load() {
		t.Error("dequeue_message reached the dispatch handler table; it must be intercepted before dispatch.Route")
	}
}
