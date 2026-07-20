package streamsup

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// ErrNoLiveChild is returned by WriteTurn when the child's stdin handle is nil —
// no claude child is currently live (before the first spawn, between spawns, or
// mid-restart). Runner.Stdin returns an untyped nil in exactly those windows,
// which WriteTurn's w == nil check maps to this sentinel. The wiring slice maps
// it to a retryable "no live child" outcome.
var ErrNoLiveChild = errors.New("streamsup: no live child")

// userTurn is the stream-json envelope written to claude's stdin. The shape
// mirrors streamrunner's verbatim (the 2026-05-14 probe):
//
//	{"type":"user","message":{"role":"user","content":[{"type":"text","text":"…"}]}}
//
// The one divergence from streamrunner is at the call site, not the shape:
// streamrunner writes ONE envelope then closes stdin; here stdin stays open for
// the next turn (WriteTurn takes an io.Writer, which cannot close it).
type userTurn struct {
	Type    string          `json:"type"`
	Message userTurnMessage `json:"message"`
}

type userTurnMessage struct {
	Role    string                `json:"role"`
	Content []userTurnContentText `json:"content"`
}

type userTurnContentText struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// marshalTurnEnvelope returns the single newline-terminated stream-json line for
// prompt. The prompt is carried as a JSON string value (content[].text) and
// json.Marshal-escaped, so every metacharacter — critically every newline —
// becomes an escape sequence. The marshalled envelope is therefore a single
// physical line and the appended '\n' is the only raw newline: an untrusted
// prompt cannot introduce a second stream-json line, so it cannot forge a
// `result` (fake turn-end), a `control_request` (interrupt), or a permission
// approval on claude's stdin. This is enforced by construction (structured
// encoding, never string concatenation).
func marshalTurnEnvelope(prompt []byte) ([]byte, error) {
	env := userTurn{
		Type: "user",
		Message: userTurnMessage{
			Role: "user",
			Content: []userTurnContentText{{
				Type: "text",
				Text: string(prompt),
			}},
		},
	}
	b, err := json.Marshal(env)
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// WriteTurn writes one user-turn stream-json envelope for prompt onto w, the
// child's held-open stdin (from Runner.Stdin). It writes exactly once and never
// closes w — holding stdin open for the next turn is the whole point of this
// path, and the io.Writer type structurally forbids a half-close/EOF forgery.
//
// A nil w means no live child: WriteTurn returns ErrNoLiveChild and writes
// nothing. A write failure (e.g. EPIPE when the pipe closed mid-teardown) is
// returned wrapped; a closed-pipe write returns an error rather than panicking,
// so the teardown race (Stdin() captures the handle, teardown closes it, then
// WriteTurn writes) surfaces as the returned error, never a panic or false ack.
//
// The caller writes turn N+1 by calling WriteTurn(runner.Stdin(), next) again on
// the same handle — no re-open, no per-turn stdin lifecycle.
func WriteTurn(w io.Writer, prompt []byte) error {
	if w == nil {
		return ErrNoLiveChild
	}
	env, err := marshalTurnEnvelope(prompt)
	if err != nil {
		return fmt.Errorf("streamsup: marshal turn: %w", err)
	}
	if _, err := w.Write(env); err != nil {
		return fmt.Errorf("streamsup: write turn: %w", err)
	}
	return nil
}
