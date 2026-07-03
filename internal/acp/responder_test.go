package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
)

// deferHandler registers method under name on tr, sending the injected Responder
// on respCh and deferring the response. respCh must be buffered (or drained) so
// the read loop is never blocked when the handler stashes its Responder.
func deferHandler(respCh chan<- *Responder) Handler {
	return func(ctx context.Context, _ json.RawMessage) (any, error) {
		respCh <- ResponderFrom(ctx)
		return nil, ErrDeferred
	}
}

func TestResponder_DeferSuppressesSyncFrame(t *testing.T) {
	t.Parallel()
	// A handler that defers must produce no synchronous response frame; Serve
	// runs to EOF and the deferred request is never resolved. run drives Serve
	// synchronously on this goroutine, so reading got afterwards is race-free.
	var got *Responder
	_, raw, _ := run(t, `{"jsonrpc":"2.0","method":"defer","id":1}`+"\n",
		func(tr *Transport) {
			tr.Register("defer", func(ctx context.Context, _ json.RawMessage) (any, error) {
				got = ResponderFrom(ctx)
				return nil, ErrDeferred
			})
		})
	if raw != "" {
		t.Fatalf("a deferred request must emit no synchronous frame, got %q", raw)
	}
	if got == nil {
		t.Fatal("ResponderFrom returned nil inside a dispatched request")
	}
}

func TestResponder_ResolveLaterWritesOneFrame(t *testing.T) {
	t.Parallel()

	t.Run("Reply success echoes id and result", func(t *testing.T) {
		t.Parallel()
		respCh := make(chan *Responder, 1)
		lt := newLiveTransportReg(t, func(tr *Transport) {
			tr.Register("hold", deferHandler(respCh))
		})
		lt.feedResp(`{"jsonrpc":"2.0","method":"hold","id":7}`)

		// Resolve from the test goroutine — not Serve's read loop.
		resp := <-respCh
		if err := resp.Reply(map[string]any{"stopReason": "end_turn"}); err != nil {
			t.Fatalf("Reply returned error: %v", err)
		}

		frame := lt.nextResponse()
		if frame["id"] != float64(7) {
			t.Fatalf("want echoed id 7, got %v", frame["id"])
		}
		res, ok := frame["result"].(map[string]any)
		if !ok || res["stopReason"] != "end_turn" {
			t.Fatalf("want result {stopReason:end_turn}, got %v", frame["result"])
		}
		if _, hasErr := frame["error"]; hasErr {
			t.Fatalf("success resolve must not carry error: %v", frame)
		}
	})

	t.Run("ReplyError echoes id and code", func(t *testing.T) {
		t.Parallel()
		respCh := make(chan *Responder, 1)
		lt := newLiveTransportReg(t, func(tr *Transport) {
			tr.Register("hold", deferHandler(respCh))
		})
		lt.feedResp(`{"jsonrpc":"2.0","method":"hold","id":8}`)

		resp := <-respCh
		if err := resp.ReplyError(NewError(CodeInvalidParams, "bad")); err != nil {
			t.Fatalf("ReplyError returned error: %v", err)
		}

		frame := lt.nextResponse()
		if frame["id"] != float64(8) {
			t.Fatalf("want echoed id 8, got %v", frame["id"])
		}
		if got := errCode(t, frame); got != CodeInvalidParams {
			t.Fatalf("want code %d, got %v", CodeInvalidParams, got)
		}
	})
}

func TestResponder_ReadLoopAliveWhileHeld(t *testing.T) {
	t.Parallel()
	respCh := make(chan *Responder, 1)
	lt := newLiveTransportReg(t, func(tr *Transport) {
		tr.Register("hold", deferHandler(respCh))
		tr.Register("quick", echoParams)
	})

	// Request #1 is held (deferred); request #2 is answered synchronously.
	lt.feedResp(`{"jsonrpc":"2.0","method":"hold","id":1}`)
	lt.feedResp(`{"jsonrpc":"2.0","method":"quick","params":{"ok":true},"id":2}`)

	// #2's response is written while #1 is still unresolved — proof the read loop
	// kept classifying and dispatching while a request was held.
	frame2 := lt.nextResponse()
	if frame2["id"] != float64(2) {
		t.Fatalf("want #2 answered while #1 held (id 2), got id %v", frame2["id"])
	}
	res, ok := frame2["result"].(map[string]any)
	if !ok || res["ok"] != true {
		t.Fatalf("want #2 result {ok:true}, got %v", frame2["result"])
	}

	// Now resolve #1 and read its frame.
	resp1 := <-respCh
	if err := resp1.Reply(map[string]any{"held": true}); err != nil {
		t.Fatalf("resolving #1: %v", err)
	}
	frame1 := lt.nextResponse()
	if frame1["id"] != float64(1) {
		t.Fatalf("want #1 resolved (id 1), got id %v", frame1["id"])
	}
}

func TestResponder_DoubleResolveWritesOneFrame(t *testing.T) {
	t.Parallel()
	respCh := make(chan *Responder, 1)
	lt := newLiveTransportReg(t, func(tr *Transport) {
		tr.Register("hold", deferHandler(respCh))
		tr.Register("quick", echoParams)
	})
	lt.feedResp(`{"jsonrpc":"2.0","method":"hold","id":3}`)

	resp := <-respCh
	if err := resp.Reply("first"); err != nil {
		t.Fatalf("first Reply: %v", err)
	}
	frame := lt.nextResponse()
	if frame["id"] != float64(3) || frame["result"] != "first" {
		t.Fatalf("want first frame id 3 result \"first\", got %v", frame)
	}

	// Both a second Reply and a ReplyError lose the once-guard: no frame, the
	// well-defined ErrAlreadyResolved sentinel.
	if err := resp.Reply("second"); !errors.Is(err, ErrAlreadyResolved) {
		t.Fatalf("want ErrAlreadyResolved on second Reply, got %v", err)
	}
	if err := resp.ReplyError(NewError(CodeInternalError, "x")); !errors.Is(err, ErrAlreadyResolved) {
		t.Fatalf("want ErrAlreadyResolved on ReplyError after resolve, got %v", err)
	}

	// Feed a sentinel request; the next frame on the wire must be its response,
	// not a second frame for id 3 — proving the double-resolves wrote nothing.
	lt.feedResp(`{"jsonrpc":"2.0","method":"quick","params":42,"id":99}`)
	next := lt.nextResponse()
	if next["id"] != float64(99) {
		t.Fatalf("a double-resolve wrote a second frame: wanted sentinel id 99 next, got %v", next)
	}
}

// deadWriter is an io.Writer whose Write fails once dead is set, modelling a
// transport whose underlying stream closed. Serve here runs synchronously on the
// test goroutine, so the flip and the writes never overlap — no lock needed.
type deadWriter struct {
	dead bool
	buf  bytes.Buffer
}

func (w *deadWriter) Write(p []byte) (int, error) {
	if w.dead {
		return 0, io.ErrClosedPipe
	}
	return w.buf.Write(p)
}

func TestResponder_ResolveAfterTeardownSafe(t *testing.T) {
	t.Parallel()
	var resp *Responder
	w := &deadWriter{}
	var diag bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&diag, &slog.HandlerOptions{Level: slog.LevelDebug}))
	tr := New(strings.NewReader(`{"jsonrpc":"2.0","method":"hold","id":5}`+"\n"), w, logger)
	tr.Register("hold", func(ctx context.Context, _ json.RawMessage) (any, error) {
		resp = ResponderFrom(ctx)
		return nil, ErrDeferred
	})
	// Serve runs to EOF (reader drained) and returns — the transport is now torn
	// down from the read loop's side.
	if err := tr.Serve(context.Background()); err != nil {
		t.Fatalf("Serve: %v", err)
	}
	if w.buf.Len() != 0 {
		t.Fatalf("deferred request must have written nothing during Serve, got %q", w.buf.String())
	}

	// The underlying stream is now dead. Resolving must not panic; the write
	// failure is logged and dropped, and no second-frame path exists.
	w.dead = true
	if err := resp.Reply("late"); err != nil {
		t.Fatalf("resolve after teardown returned error: %v", err)
	}
	if !strings.Contains(diag.String(), "write frame failed") {
		t.Fatalf("want a logged write-failure on a dead writer, got diag %q", diag.String())
	}
}
