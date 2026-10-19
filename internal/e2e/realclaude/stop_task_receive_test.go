//go:build e2e_realclaude

package realclaude

import (
	"encoding/base64"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/protocol"
)

type stopTaskRead struct {
	raw []byte
	err error
}

// stopTaskReader owns the socket until stop. Consumer polling never cancels a
// WebSocket read; only the test goroutine decrypts, in channel arrival order.
type stopTaskReader struct {
	h      *perConvHarness
	frames <-chan stopTaskRead
	stop   func()
}

func newStopTaskReader(t *testing.T, h *perConvHarness) *stopTaskReader {
	t.Helper()
	frames := make(chan stopTaskRead)
	stopped, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		defer close(frames)
		for {
			raw, err := h.phone.ReceiveBytes(perTurnReplyBudget + time.Minute)
			select {
			case frames <- stopTaskRead{raw, err}:
			case <-stopped:
				return
			}
			if err != nil {
				return
			}
		}
	}()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			close(stopped)
			_ = h.phone.Close() // Unblock a socket read; Close is bounded and idempotent.
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Error("stop task reader shutdown timed out")
			}
		})
	}
	t.Cleanup(stop)
	return &stopTaskReader{h: h, frames: frames, stop: stop}
}

func (r *stopTaskReader) receive(t *testing.T, timeout time.Duration) (protocol.Envelope, bool) {
	t.Helper()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		select {
		case <-timer.C:
			return protocol.Envelope{}, false
		case frame, ok := <-r.frames:
			if !ok || frame.err != nil {
				t.Fatal("stop task phone receive failed")
			}
			var inner protocol.InnerFrameV2
			if json.Unmarshal(frame.raw, &inner) != nil {
				t.Fatal("decode stop task inner frame")
			}
			if inner.Type != protocol.TypeNoiseMsg {
				continue
			}
			cipher, err := base64.StdEncoding.DecodeString(inner.Data)
			if err != nil {
				t.Fatal("decode stop task inner data")
			}
			plain, err := r.h.initRecv.Decrypt(cipher)
			if err != nil {
				t.Fatal("decrypt stop task envelope")
			}
			var env protocol.Envelope
			if json.Unmarshal(plain, &env) != nil {
				t.Fatal("decode stop task envelope")
			}
			return env, true
		}
	}
}
