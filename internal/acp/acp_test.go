package acp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
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

// --- Outbound-request primitive (Transport.Call) ---

// syncBuffer is a concurrency-safe diagnostics sink: Serve runs on its own
// goroutine and writes log records while the test reads them. A bare
// bytes.Buffer would be a -race failure (see docs/lessons.md).
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

// liveTransport drives the outbound direction: Serve runs in its own goroutine
// over in-memory pipes while the test reads outbound request frames off the
// writer and feeds response lines back on the reader. The single-shot run
// harness cannot express this — it parses output only after Serve reaches EOF,
// but an outbound Call needs a response written *after* the request is observed.
//
// All nextRequest / feedResp / assertion calls run on the test goroutine; only
// Call is issued from a spawned goroutine (its outcome collected over a channel)
// so t.Fatalf is never called off the test goroutine.
type liveTransport struct {
	t        *testing.T
	tr       *Transport
	respW    *io.PipeWriter // test writes response lines → Transport reads
	reqR     *bufio.Reader  // test reads outbound request frames
	diag     *syncBuffer
	serveErr chan error
}

func newLiveTransport(t *testing.T) *liveTransport {
	t.Helper()
	rIn, wIn := io.Pipe()   // Transport reads rIn; test feeds responses on wIn
	rOut, wOut := io.Pipe() // Transport writes wOut; test reads requests on rOut
	diag := &syncBuffer{}
	logger := slog.New(slog.NewTextHandler(diag, &slog.HandlerOptions{Level: slog.LevelDebug}))
	tr := New(rIn, wOut, logger)
	serveErr := make(chan error, 1)
	go func() { serveErr <- tr.Serve(context.Background()) }()

	lt := &liveTransport{t: t, tr: tr, respW: wIn, reqR: bufio.NewReader(rOut), diag: diag, serveErr: serveErr}
	t.Cleanup(func() {
		_ = wIn.Close() // EOF the reader → Serve returns nil
		if err := <-serveErr; err != nil {
			t.Errorf("Serve returned error: %v", err)
		}
		_ = rOut.Close() // unblock any stray outbound write (buggy-test safety net)
	})
	return lt
}

// nextRequest reads one outbound request frame off the writer and returns its
// id, method, and raw params, asserting the JSON-RPC envelope shape.
func (lt *liveTransport) nextRequest() (id uint64, method string, params json.RawMessage) {
	lt.t.Helper()
	line, err := lt.reqR.ReadBytes('\n')
	if err != nil {
		lt.t.Fatalf("reading outbound request: %v", err)
	}
	var req struct {
		Jsonrpc string          `json:"jsonrpc"`
		ID      uint64          `json:"id"`
		Method  string          `json:"method"`
		Params  json.RawMessage `json:"params"`
	}
	if uerr := json.Unmarshal(line, &req); uerr != nil {
		lt.t.Fatalf("outbound request is not valid JSON: %q: %v", line, uerr)
	}
	if req.Jsonrpc != "2.0" {
		lt.t.Fatalf("outbound request missing jsonrpc:2.0: %q", line)
	}
	if req.ID == 0 {
		lt.t.Fatalf("outbound request must carry a nonzero id: %q", line)
	}
	return req.ID, req.Method, req.Params
}

// feedResp writes one response line (a trailing newline is appended) onto the
// transport's reader.
func (lt *liveTransport) feedResp(line string) {
	lt.t.Helper()
	if _, err := io.WriteString(lt.respW, line+"\n"); err != nil {
		lt.t.Fatalf("feeding response line: %v", err)
	}
}

// callOutcome carries a Call's return values back to the test goroutine.
type callOutcome struct {
	res json.RawMessage
	err error
}

// goCall issues Call on a spawned goroutine and returns a channel that yields
// its outcome once it resolves.
func (lt *liveTransport) goCall(ctx context.Context, method string, params any) <-chan callOutcome {
	out := make(chan callOutcome, 1)
	go func() {
		res, err := lt.tr.Call(ctx, method, params)
		out <- callOutcome{res, err}
	}()
	return out
}

func TestTransport_Call_ResultPath(t *testing.T) {
	t.Parallel()
	lt := newLiveTransport(t)
	done := lt.goCall(context.Background(), "session/x", map[string]any{"a": 1})

	id, method, params := lt.nextRequest()
	if method != "session/x" {
		t.Fatalf("want method session/x, got %q", method)
	}
	var p map[string]any
	if err := json.Unmarshal(params, &p); err != nil || p["a"] != float64(1) {
		t.Fatalf("want params {a:1}, got %s (err %v)", params, err)
	}

	lt.feedResp(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"result":{"ok":true}}`, id))

	oc := <-done
	if oc.err != nil {
		t.Fatalf("Call returned error: %v", oc.err)
	}
	var r map[string]any
	if err := json.Unmarshal(oc.res, &r); err != nil || r["ok"] != true {
		t.Fatalf("want result {ok:true}, got %s (err %v)", oc.res, err)
	}
}

func TestTransport_Call_ErrorPath(t *testing.T) {
	t.Parallel()
	lt := newLiveTransport(t)
	done := lt.goCall(context.Background(), "session/x", nil)

	id, _, params := lt.nextRequest()
	if params != nil {
		t.Fatalf("nil params must be omitted from the wire, got %s", params)
	}
	lt.feedResp(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"error":{"code":-32602,"message":"bad","data":{"field":"x"}}}`, id))

	oc := <-done
	if oc.res != nil {
		t.Fatalf("error response must yield a nil result, got %s", oc.res)
	}
	var rpcErr *Error
	if !errors.As(oc.err, &rpcErr) {
		t.Fatalf("want *Error, got %T: %v", oc.err, oc.err)
	}
	if rpcErr.Code != CodeInvalidParams || rpcErr.Message != "bad" {
		t.Fatalf("want code %d message \"bad\", got %d %q", CodeInvalidParams, rpcErr.Code, rpcErr.Message)
	}
	data, ok := rpcErr.Data.(map[string]any)
	if !ok || data["field"] != "x" {
		t.Fatalf("want error data {field:x}, got %v", rpcErr.Data)
	}
}

func TestTransport_Call_ConcurrentDistinctIDs(t *testing.T) {
	t.Parallel()
	lt := newLiveTransport(t)

	const n = 32
	outs := make([]<-chan callOutcome, n)
	for i := 0; i < n; i++ {
		outs[i] = lt.goCall(context.Background(), "m", map[string]int{"n": i})
	}

	// Read all n requests and echo each one's params back as its result, keyed
	// by the request's own id. Distinct ids ⇒ each response reaches its own
	// waiter. Writes serialise through writeMu, so ids arrive one line at a time.
	ids := make(map[uint64]bool, n)
	for i := 0; i < n; i++ {
		id, _, params := lt.nextRequest()
		if ids[id] {
			t.Fatalf("duplicate outbound id %d", id)
		}
		ids[id] = true
		lt.feedResp(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"result":%s}`, id, params))
	}
	if len(ids) != n {
		t.Fatalf("want %d distinct ids, got %d", n, len(ids))
	}

	// Every call must resolve to the response echoing its own params (n==i).
	for i := 0; i < n; i++ {
		oc := <-outs[i]
		if oc.err != nil {
			t.Fatalf("call %d returned error: %v", i, oc.err)
		}
		var r struct {
			N int `json:"n"`
		}
		if err := json.Unmarshal(oc.res, &r); err != nil {
			t.Fatalf("call %d result not decodable: %s (%v)", i, oc.res, err)
		}
		if r.N != i {
			t.Fatalf("call %d resolved to the wrong response: got n=%d", i, r.N)
		}
	}
}

func TestTransport_Call_UnknownIDDropped(t *testing.T) {
	t.Parallel()
	lt := newLiveTransport(t)

	// A response with no outstanding call: dropped, logged, Serve stays live.
	lt.feedResp(`{"jsonrpc":"2.0","id":999999,"result":{}}`)

	// A subsequent real call still resolves — proof Serve was not stalled and
	// the unknown response never corrupted the registry.
	done := lt.goCall(context.Background(), "m", nil)
	id, _, _ := lt.nextRequest()
	lt.feedResp(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"result":{"ok":true}}`, id))
	oc := <-done
	if oc.err != nil {
		t.Fatalf("real call after an unknown-id response failed: %v", oc.err)
	}
	if !strings.Contains(lt.diag.String(), "no waiter") {
		t.Fatalf("unknown-id drop not recorded in diagnostics: %q", lt.diag.String())
	}
}

func TestTransport_Call_ContextCancelledReclaims(t *testing.T) {
	t.Parallel()
	lt := newLiveTransport(t)

	ctx, cancel := context.WithCancel(context.Background())
	done := lt.goCall(ctx, "m", nil)
	id, _, _ := lt.nextRequest() // observe the request, then cancel with no response fed
	cancel()

	oc := <-done
	if !errors.Is(oc.err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", oc.err)
	}

	// A late response for the cancelled id must be dropped (slot reclaimed) —
	// never delivered, no panic.
	lt.feedResp(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"result":{"late":true}}`, id))

	// A fresh call with a new id still resolves, proving no waiter lingers.
	done2 := lt.goCall(context.Background(), "m", nil)
	id2, _, _ := lt.nextRequest()
	if id2 == id {
		t.Fatalf("fresh call reused the reclaimed id %d", id)
	}
	lt.feedResp(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"result":{"ok":true}}`, id2))
	if oc2 := <-done2; oc2.err != nil {
		t.Fatalf("fresh call after cancellation failed: %v", oc2.err)
	}
}

func TestTransport_Call_MalformedErrorObject(t *testing.T) {
	t.Parallel()
	lt := newLiveTransport(t)
	done := lt.goCall(context.Background(), "m", nil)

	id, _, _ := lt.nextRequest()
	// error is a string, not an error object: Call must not hang and must
	// return a synthesized *Error.
	lt.feedResp(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"error":"not an object"}`, id))

	oc := <-done
	var rpcErr *Error
	if !errors.As(oc.err, &rpcErr) {
		t.Fatalf("want a non-nil *Error for a malformed error object, got %T: %v", oc.err, oc.err)
	}
	if rpcErr.Code != CodeInternalError {
		t.Fatalf("want synthesized code %d, got %d", CodeInternalError, rpcErr.Code)
	}
}

func TestTransport_Call_MarshalParamsError(t *testing.T) {
	t.Parallel()
	lt := newLiveTransport(t)
	// A channel cannot be marshalled: Call returns before any id is burned or
	// any frame is written.
	res, err := lt.tr.Call(context.Background(), "m", make(chan int))
	if err == nil {
		t.Fatal("want a marshal error, got nil")
	}
	if res != nil {
		t.Fatalf("want nil result on marshal failure, got %s", res)
	}
	if !strings.Contains(err.Error(), "marshal params") {
		t.Fatalf("want a marshal-params error, got %v", err)
	}
}
