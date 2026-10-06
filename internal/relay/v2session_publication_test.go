package relay

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/protocol"
)

func TestV2SessionFlushPushes(t *testing.T) {
	for _, action := range []string{"drain", "disconnect", "cancel"} {
		t.Run(action, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			q := &pushQueue{}
			q.enqueue(protocol.Envelope{Type: protocol.TypeResetting, Payload: []byte(`{}`)})
			s := &V2Session{connID: "peer", state: V2StateOpen}
			mgr := &V2SessionManager{
				cfg:    V2SessionConfig{Logger: testLogger(t)},
				queues: map[string]*pushQueue{"peer": q}, sessions: map[string]*V2Session{"peer": s},
				drainCh: make(chan struct{}, 1),
			}
			result := make(chan error, 1)
			go func() { result <- mgr.FlushPushes(ctx) }()
			select {
			case <-mgr.drainCh:
			case <-time.After(time.Second):
				t.Fatal("flush not queued")
			}
			select {
			case err := <-result:
				t.Fatalf("flush completed before delivery: %v", err)
			default:
			}
			switch action {
			case "drain":
				// Observe the marker behind the prior control envelope. Neither
				// pushes after the marker nor a marker itself are wire traffic.
				mgr.pushMu.Lock()
				if len(q.items) != 2 || q.items[1].barrier == nil || q.items[1].env.Type != "" {
					t.Fatal("missing internal FIFO barrier")
				}
				q.popHead() // preceding envelope's delivery, before the next drain
				q.enqueue(protocol.Envelope{Type: protocol.TypeConversationUpdated})
				mgr.pushMu.Unlock()
				mgr.drainOnce(ctx) // marker completes without attempting Noise encryption
				if len(q.items) != 1 || q.items[0].env.Type != protocol.TypeConversationUpdated {
					t.Fatal("flush consumed subsequent pushes")
				}
			case "disconnect":
				mgr.teardown(s)
			case "cancel":
				cancel()
			}
			select {
			case err := <-result:
				if action == "cancel" && !errors.Is(err, context.Canceled) {
					t.Fatalf("cancel=%v", err)
				}
				if action != "cancel" && err != nil {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				t.Fatal("publication waiter stranded")
			}
		})
	}
}
