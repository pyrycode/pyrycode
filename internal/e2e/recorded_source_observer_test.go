//go:build e2e

package e2e

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/e2e/internal/claudetape"
)

// Native evidence stays in bounded private memory and never enters diagnostics.
// Publication precedes forwarding, in capture and replay, so a client modal
// cannot manufacture the upstream evidence used to validate it.
type recordedSourceObserver struct {
	listener net.Listener
	mu       sync.Mutex
	frames   []claudetape.Frame
	bytes    int
	err      error
	done     chan struct{}
}

func newRecordedSourceObserver(t *testing.T) *recordedSourceObserver {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal("native source observer unavailable")
	}
	o := &recordedSourceObserver{listener: l, done: make(chan struct{})}
	go func() {
		defer close(o.done)
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
			raw, err := io.ReadAll(io.LimitReader(c, claudetape.MaxBytes+1))
			_ = c.Close()
			o.mu.Lock()
			if err != nil || len(raw) > claudetape.MaxBytes || !json.Valid(raw) || len(o.frames) >= 512 || o.bytes+len(raw) > claudetape.MaxBytes {
				o.err = errors.New("native source evidence exceeded its validated bound")
			} else {
				o.frames = append(o.frames, claudetape.Frame{Direction: "stdout", Data: append([]byte(nil), raw...)})
				o.bytes += len(raw)
			}
			o.mu.Unlock()
		}
	}()
	t.Cleanup(func() { _ = l.Close(); <-o.done })
	return o
}
func (o *recordedSourceObserver) snapshot(t *testing.T) []claudetape.Frame {
	t.Helper()
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.err != nil {
		t.Fatal("native source observer failed")
	}
	return append([]claudetape.Frame(nil), o.frames...)
}
func publishRecordedSource(c tapeProcessConfig, raw []byte) error {
	if len(raw) > claudetape.MaxBytes {
		return errors.New("native source frame exceeded its bound")
	}
	if c.ObserverAddress == "" {
		return nil
	}
	var v struct{ Type, Subtype string }
	if json.Unmarshal(raw, &v) != nil {
		return errors.New("invalid native source frame")
	}
	if v.Type != "control_request" && v.Type != "control_response" && v.Type != "result" && v.Type != "prompt_suggestion" && !(v.Type == "system" && v.Subtype == "init") {
		return nil
	}
	conn, err := net.DialTimeout("tcp", c.ObserverAddress, 5*time.Second)
	if err != nil {
		return errors.New("native source observer connection failed")
	}
	defer conn.Close()
	_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if _, err = conn.Write(raw); err != nil {
		return errors.New("native source evidence publication failed")
	}
	return nil
}

func TestRecordedSourceObserver(t *testing.T) {
	o := newRecordedSourceObserver(t)
	c := tapeProcessConfig{ObserverAddress: o.listener.Addr().String()}
	raw := []byte(`{"type":"control_request","request_id":"source","request":{"subtype":"can_use_tool","decision_reason":null}}`)
	if err := publishRecordedSource(c, raw); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for len(o.snapshot(t)) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	rows := o.snapshot(t)
	if len(rows) != 1 || string(rows[0].Data) != string(raw) {
		t.Fatal("native observation changed bytes or presence")
	}
	if err := publishRecordedSource(c, []byte(`{"type":"stream_event"}`)); err != nil {
		t.Fatal(err)
	}
	if err := publishRecordedSource(c, []byte(`{invalid`)); err == nil {
		t.Fatal("invalid native source accepted")
	}
	if len(o.snapshot(t)) != 1 {
		t.Fatal("unselected source crossed observer boundary")
	}
}
