package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
)

// run drives input through a Transport over in-memory pipes and returns the
// parsed output frames, the raw writer output, and the captured diagnostics.
// reg registers handlers before Serve. Serve must return nil (use runErr for
// error cases).
func run(t *testing.T, input string, reg func(tr *Transport)) (frames []map[string]any, raw, diag string) {
	t.Helper()
	raw, diag, err := runErr(t, input, reg)
	if err != nil {
		t.Fatalf("Serve returned error: %v", err)
	}
	for _, line := range strings.Split(strings.TrimRight(raw, "\n"), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if uerr := json.Unmarshal([]byte(line), &m); uerr != nil {
			t.Fatalf("output line is not valid JSON: %q: %v", line, uerr)
		}
		frames = append(frames, m)
	}
	return frames, raw, diag
}

// runErr is run without the Serve-must-succeed assertion, for error-path tests.
func runErr(t *testing.T, input string, reg func(tr *Transport)) (raw, diag string, err error) {
	t.Helper()
	var w, diagBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&diagBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	tr := New(strings.NewReader(input), &w, logger)
	if reg != nil {
		reg(tr)
	}
	err = tr.Serve(context.Background())
	return w.String(), diagBuf.String(), err
}

// echoParams returns its params verbatim as the result.
func echoParams(_ context.Context, params json.RawMessage) (any, error) {
	return json.RawMessage(params), nil
}

func errCode(t *testing.T, frame map[string]any) float64 {
	t.Helper()
	errObj, ok := frame["error"].(map[string]any)
	if !ok {
		t.Fatalf("frame has no error object: %v", frame)
	}
	code, ok := errObj["code"].(float64)
	if !ok {
		t.Fatalf("error object has no numeric code: %v", errObj)
	}
	return code
}

// idIsNull reports whether the frame's id member is present and JSON null.
func idIsNull(frame map[string]any) bool {
	v, ok := frame["id"]
	return ok && v == nil
}

func assertJSONRPC(t *testing.T, frame map[string]any) {
	t.Helper()
	if frame["jsonrpc"] != "2.0" {
		t.Fatalf("frame missing jsonrpc:2.0: %v", frame)
	}
}

func TestTransport_Request_SingleResponseMatchingID(t *testing.T) {
	t.Parallel()
	frames, _, _ := run(t, `{"jsonrpc":"2.0","method":"echo","params":{"a":1},"id":42}`+"\n",
		func(tr *Transport) { tr.Register("echo", echoParams) })
	if len(frames) != 1 {
		t.Fatalf("want exactly 1 response frame, got %d: %v", len(frames), frames)
	}
	f := frames[0]
	assertJSONRPC(t, f)
	if f["id"] != float64(42) {
		t.Fatalf("want echoed id 42, got %v", f["id"])
	}
	res, ok := f["result"].(map[string]any)
	if !ok || res["a"] != float64(1) {
		t.Fatalf("want result echoing params {a:1}, got %v", f["result"])
	}
	if _, hasErr := f["error"]; hasErr {
		t.Fatalf("success response must not carry error: %v", f)
	}
}

func TestTransport_Request_NullIDIsARequest(t *testing.T) {
	t.Parallel()
	// A present-but-null id is a request (not a notification): it must be
	// dispatched and get a response whose id is null.
	frames, _, _ := run(t, `{"jsonrpc":"2.0","method":"echo","params":null,"id":null}`+"\n",
		func(tr *Transport) { tr.Register("echo", echoParams) })
	if len(frames) != 1 {
		t.Fatalf("want 1 response for a null-id request, got %d: %v", len(frames), frames)
	}
	if !idIsNull(frames[0]) {
		t.Fatalf("want null id echoed, got %v", frames[0]["id"])
	}
}

func TestTransport_Notification_NoResponse(t *testing.T) {
	t.Parallel()
	var ran bool
	_, raw, _ := run(t, `{"jsonrpc":"2.0","method":"notify","params":{"x":1}}`+"\n",
		func(tr *Transport) {
			tr.Register("notify", func(_ context.Context, _ json.RawMessage) (any, error) {
				ran = true
				return nil, nil
			})
		})
	if !ran {
		t.Fatal("notification handler did not run")
	}
	if raw != "" {
		t.Fatalf("notification must produce no response, got %q", raw)
	}
}

func TestTransport_UnknownMethod_MethodNotFound(t *testing.T) {
	t.Parallel()
	frames, _, _ := run(t, `{"jsonrpc":"2.0","method":"nope","id":5}`+"\n", nil)
	if len(frames) != 1 {
		t.Fatalf("want 1 error frame, got %d", len(frames))
	}
	assertJSONRPC(t, frames[0])
	if got := errCode(t, frames[0]); got != CodeMethodNotFound {
		t.Fatalf("want code %d, got %v", CodeMethodNotFound, got)
	}
	if frames[0]["id"] != float64(5) {
		t.Fatalf("want echoed id 5, got %v", frames[0]["id"])
	}
}

func TestTransport_ParseError(t *testing.T) {
	t.Parallel()
	frames, _, _ := run(t, "not json at all\n", nil)
	if len(frames) != 1 {
		t.Fatalf("want 1 error frame, got %d", len(frames))
	}
	if got := errCode(t, frames[0]); got != CodeParseError {
		t.Fatalf("want code %d, got %v", CodeParseError, got)
	}
	if !idIsNull(frames[0]) {
		t.Fatalf("parse error must carry null id, got %v", frames[0]["id"])
	}
}

func TestTransport_InvalidRequest(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"number":       `42`,
		"string":       `"x"`,
		"bool":         `true`,
		"jsonNull":     `null`,
		"emptyObject":  `{}`,
		"onlyJsonrpc":  `{"jsonrpc":"2.0"}`,
		"methodNumber": `{"jsonrpc":"2.0","method":5,"id":1}`,
	}
	for name, line := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			frames, _, _ := run(t, line+"\n", nil)
			if len(frames) != 1 {
				t.Fatalf("want 1 error frame, got %d: %v", len(frames), frames)
			}
			if got := errCode(t, frames[0]); got != CodeInvalidRequest {
				t.Fatalf("want code %d, got %v", CodeInvalidRequest, got)
			}
			if !idIsNull(frames[0]) {
				t.Fatalf("invalid request must carry null id, got %v", frames[0]["id"])
			}
		})
	}
}

func TestTransport_ResponseFrameTolerated(t *testing.T) {
	t.Parallel()
	for name, line := range map[string]string{
		"result": `{"jsonrpc":"2.0","id":7,"result":{"ok":true}}`,
		"error":  `{"jsonrpc":"2.0","id":7,"error":{"code":-1,"message":"x"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, raw, _ := run(t, line+"\n", nil)
			if raw != "" {
				t.Fatalf("response frame must be dropped with no writer output, got %q", raw)
			}
		})
	}
}

func TestTransport_BatchRejected(t *testing.T) {
	t.Parallel()
	frames, _, _ := run(t, `[{"jsonrpc":"2.0","method":"m","id":1}]`+"\n", nil)
	if len(frames) != 1 {
		t.Fatalf("want exactly 1 error frame for a batch, got %d: %v", len(frames), frames)
	}
	if got := errCode(t, frames[0]); got != CodeInvalidRequest {
		t.Fatalf("want code %d, got %v", CodeInvalidRequest, got)
	}
	if !idIsNull(frames[0]) {
		t.Fatalf("batch rejection must carry null id, got %v", frames[0]["id"])
	}
}

func TestTransport_HandlerError_RPCvsPlain(t *testing.T) {
	t.Parallel()

	t.Run("rpcError controls the wire code", func(t *testing.T) {
		t.Parallel()
		frames, _, _ := run(t, `{"jsonrpc":"2.0","method":"bad","id":1}`+"\n",
			func(tr *Transport) {
				tr.Register("bad", func(_ context.Context, _ json.RawMessage) (any, error) {
					return nil, NewError(CodeInvalidParams, "bad params")
				})
			})
		if got := errCode(t, frames[0]); got != CodeInvalidParams {
			t.Fatalf("want code %d, got %v", CodeInvalidParams, got)
		}
	})

	t.Run("plain error maps to internal error and is not leaked", func(t *testing.T) {
		t.Parallel()
		frames, raw, diag := run(t, `{"jsonrpc":"2.0","method":"boom","id":1}`+"\n",
			func(tr *Transport) {
				tr.Register("boom", func(_ context.Context, _ json.RawMessage) (any, error) {
					return nil, errors.New("secret boom detail")
				})
			})
		if got := errCode(t, frames[0]); got != CodeInternalError {
			t.Fatalf("want code %d, got %v", CodeInternalError, got)
		}
		if strings.Contains(raw, "secret boom detail") {
			t.Fatalf("internal error detail leaked to the wire: %q", raw)
		}
		if !strings.Contains(diag, "secret boom detail") {
			t.Fatalf("internal error detail missing from diagnostics: %q", diag)
		}
	})
}

func TestTransport_ErrorData(t *testing.T) {
	t.Parallel()
	frames, _, _ := run(t, `{"jsonrpc":"2.0","method":"bad","id":1}`+"\n",
		func(tr *Transport) {
			tr.Register("bad", func(_ context.Context, _ json.RawMessage) (any, error) {
				return nil, &Error{Code: CodeInvalidParams, Message: "bad", Data: map[string]any{"field": "x"}}
			})
		})
	errObj := frames[0]["error"].(map[string]any)
	data, ok := errObj["data"].(map[string]any)
	if !ok || data["field"] != "x" {
		t.Fatalf("want error data {field:x}, got %v", errObj["data"])
	}
}

func TestTransport_DiagnosticsIsolation(t *testing.T) {
	t.Parallel()
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","method":"echo","params":{"secret":"prompt-content"},"id":1}`,
		`not json`,
		`{"jsonrpc":"2.0","method":"notify"}`,
		`[1,2,3]`,
		`{"jsonrpc":"2.0","id":9,"result":{}}`,
	}, "\n") + "\n"

	_, raw, _ := run(t, input, func(tr *Transport) {
		tr.Register("echo", echoParams)
		tr.Register("notify", func(_ context.Context, _ json.RawMessage) (any, error) { return nil, nil })
	})

	// Every line on the writer is a valid JSON-RPC frame.
	for _, line := range strings.Split(strings.TrimRight(raw, "\n"), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("writer line is not JSON: %q", line)
		}
		if m["jsonrpc"] != "2.0" {
			t.Fatalf("writer line is not a JSON-RPC frame: %q", line)
		}
	}
}

func TestTransport_NoPanic(t *testing.T) {
	t.Parallel()
	adversarial := []string{
		``,
		`   `,
		"\t",
		`{`,
		`{"method":`,
		`{"method":5}`,
		`{"jsonrpc":"2.0","params":{"deeply":{"nested":{"a":{"b":{"c":1}}}}}}`,
		`[`,
		`[}`,
		`"unterminated`,
		`{"id":1,"method":true}`,
		`tru`,
	}
	// One Serve over all the adversarial lines: if any line panicked, the test
	// process would crash. Reaching the assertion proves totality.
	_, _, err := runErr(t, strings.Join(adversarial, "\n")+"\n", nil)
	if err != nil {
		t.Fatalf("Serve over adversarial input returned error: %v", err)
	}
}

func TestTransport_BlankLinesSkipped_EOFReturnsNil(t *testing.T) {
	t.Parallel()
	frames, _, _ := run(t, "\n   \n\t\n{\"jsonrpc\":\"2.0\",\"method\":\"m\",\"id\":1}\n\n", nil)
	// Only the one request line produces output (method-not-found here).
	if len(frames) != 1 {
		t.Fatalf("blank lines must be skipped; want 1 frame, got %d: %v", len(frames), frames)
	}
}

func TestTransport_NoTrailingNewline(t *testing.T) {
	t.Parallel()
	// A final line without a trailing newline must still be processed.
	frames, _, _ := run(t, `{"jsonrpc":"2.0","method":"nope","id":1}`, nil)
	if len(frames) != 1 {
		t.Fatalf("want 1 frame for a newline-less final line, got %d", len(frames))
	}
}

func TestTransport_OverlongLine_ReturnsWrappedError(t *testing.T) {
	t.Parallel()
	// A single line larger than maxLineBytes with no newline breaks the stream.
	huge := `{"jsonrpc":"2.0","method":"m","params":"` + strings.Repeat("a", maxLineBytes) + `"}`
	_, _, err := runErr(t, huge, nil)
	if err == nil {
		t.Fatal("want a wrapped error for an over-long line, got nil")
	}
	if !strings.Contains(err.Error(), "acp: serve:") {
		t.Fatalf("want error wrapped with 'acp: serve:', got %v", err)
	}
}

func TestTransport_ContextCancelledBetweenFrames(t *testing.T) {
	t.Parallel()
	var w bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&w, &slog.HandlerOptions{}))
	tr := New(strings.NewReader(`{"jsonrpc":"2.0","method":"m","id":1}`+"\n"), &w, logger)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancelled before the first frame is processed
	if err := tr.Serve(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
}

func TestTransport_RegisterGuards(t *testing.T) {
	t.Parallel()

	t.Run("duplicate method panics", func(t *testing.T) {
		t.Parallel()
		tr := New(strings.NewReader(""), &bytes.Buffer{}, nil)
		tr.Register("m", echoParams)
		assertPanics(t, func() { tr.Register("m", echoParams) })
	})

	t.Run("register after Serve panics", func(t *testing.T) {
		t.Parallel()
		tr := New(strings.NewReader(""), &bytes.Buffer{}, nil)
		if err := tr.Serve(context.Background()); err != nil {
			t.Fatalf("Serve on empty reader should return nil, got %v", err)
		}
		assertPanics(t, func() { tr.Register("late", echoParams) })
	})
}

func TestNew_NilArgs(t *testing.T) {
	t.Parallel()
	assertPanics(t, func() { New(nil, &bytes.Buffer{}, nil) })
	assertPanics(t, func() { New(strings.NewReader(""), nil, nil) })
	// nil logger is allowed (defaults to slog.Default()).
	if tr := New(strings.NewReader(""), &bytes.Buffer{}, nil); tr == nil {
		t.Fatal("New with nil logger returned nil")
	}
}

func assertPanics(t *testing.T, fn func()) {
	t.Helper()
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic, got none")
		}
	}()
	fn()
}
