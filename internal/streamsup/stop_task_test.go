package streamsup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"runtime"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/turnevent"
)

func TestWriteStopTask(t *testing.T) {
	for _, tc := range []struct{ task, encoded string }{
		{"task-1", `"task-1"`},
		{"", `""`},
		{"\"\\\n\r\t<>&", `"\"\\\n\r\t\u003c\u003e\u0026"`},
	} {
		t.Run(tc.encoded, func(t *testing.T) {
			w := newMCPStatusQueryWriter()
			if err := WriteStopTask(w, "fixed-id", tc.task); err != nil {
				t.Fatal(err)
			}
			raw := <-w.wrote
			want := `{"type":"control_request","request_id":"fixed-id","request":{"subtype":"stop_task","task_id":` + tc.encoded + `}}` + "\n"
			if string(raw) != want || bytes.Count(raw, []byte{'\n'}) != 1 || w.count() != 1 {
				t.Fatalf("request = %q, want %q in one write", raw, want)
			}
			var req controlRequest
			if err := json.Unmarshal(raw, &req); err != nil {
				t.Fatal(err)
			}
			if req.Request.TaskID == nil || *req.Request.TaskID != tc.task {
				t.Fatalf("task id = %v", req.Request.TaskID)
			}
		})
	}
	if err := WriteStopTask(nil, "id", "task"); !errors.Is(err, ErrNoLiveChild) {
		t.Fatalf("nil writer: %v", err)
	}
	for _, short := range []bool{false, true} {
		w := newMCPStatusQueryWriter()
		sentinel := errors.New("private-stop-writer-error-sentinel")
		w.write = func(raw []byte) (int, error) {
			if short {
				return len(raw) - 1, nil
			}
			return 0, sentinel
		}
		want := sentinel
		if short {
			want = io.ErrShortWrite
		}
		if err := WriteStopTask(w, "id", "task"); !errors.Is(err, want) {
			t.Fatalf("write error = %v, want %v", err, want)
		}
	}
}

func stopAsync(t *testing.T, ctx context.Context, r *Runner, task string) <-chan bool {
	t.Helper()
	return actuateAsync(ctx, func(ctx context.Context) bool { return r.StopTask(ctx, task) })
}

// Wait for the write gate rather than the writer's entry signal: only then is
// cancellation known to be response-wait cancellation rather than write cancellation.
func awaitStopWrite(t *testing.T, p *Parser, id string) {
	t.Helper()
	p.mcpActuations.mu.Lock()
	pending := p.mcpActuations.pending[id]
	p.mcpActuations.mu.Unlock()
	if pending == nil {
		t.Fatal("stop registration absent before reply")
	}
	select {
	case <-pending.writeDone:
	case <-time.After(3 * time.Second):
		t.Fatal("stop write did not resolve")
	}
}

func TestRunner_StopTask_VerdictsPrivacyAndPreservation(t *testing.T) {
	for _, active := range []bool{false, true} {
		for _, subtype := range []string{"success", "error", "", "unknown", "cancel"} {
			t.Run(subtype+"/"+map[bool]string{false: "idle", true: "reply"}[active], func(t *testing.T) {
				var logs bytes.Buffer
				logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
				sink := make(chan turnevent.Event, 8)
				p := NewParser(func(ev turnevent.Event) { sink <- ev }, logger)
				w := newMCPStatusQueryWriter()
				r := mcpStatusQueryRunner(t, p, w, false)
				r.log = logger
				if active {
					_, _ = p.Write([]byte(`{"type":"stream_event","event":{"type":"message_start","message":{"id":"current-reply"}}}` + "\n"))
					_, _ = p.Write([]byte(`{"type":"stream_event","event":{"type":"content_block_start","index":0,"content_block":{"type":"text"}}}` + "\n"))
				}
				beforeID, beforeOpen := p.streamMessageID, p.streamBlockOpen
				ctx, cancel := context.WithCancel(appliedSettingsQueryContext(t))
				defer cancel()
				done := stopAsync(t, ctx, r, "private-task-id-sentinel")
				req := awaitActuationRequest(t, w, "stop_task")
				if req.Request.TaskID == nil || *req.Request.TaskID != "private-task-id-sentinel" {
					t.Fatal("task id was not forwarded")
				}
				awaitStopWrite(t, p, req.RequestID)
				if subtype == "cancel" {
					cancel()
				} else {
					line := appliedSettingsQueryResponse(t, req.RequestID, subtype, map[string]any{"models": []any{map[string]any{"value": "private-model-sentinel", "displayName": "private-model-sentinel"}}})
					line = bytes.Replace(line, []byte(`"response":{`), []byte(`"response":{"error":"private-claude-error-sentinel",`), 1)
					_, _ = p.Write(line)
				}
				if got := awaitActuationOutcome(t, done); got != (subtype == "success") {
					t.Fatalf("accepted = %v for %q", got, subtype)
				}
				// Duplicate or canceled replies must not reach emitting shape decoders.
				_, _ = p.Write(actuationAck(t, req.RequestID, "success", mcpStatusQueryServers("private-task-id-sentinel", 1)))
				assertNoActuationSinkEvent(t, sink)
				if r.Stdin() != w || w.count() != 1 || p.streamMessageID != beforeID || p.streamBlockOpen != beforeOpen {
					t.Fatal("stop changed child, reply state or wrote extra input")
				}
				if active && (!beforeOpen || beforeID != "current-reply") {
					t.Fatal("test did not establish an open reply")
				}
				if logs.Len() != 0 {
					t.Fatalf("stop response reached logs: %s", logs.String())
				}
				if active {
					_, _ = p.Write([]byte(`{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"still replying"}}}` + "\n"))
					select {
					case ev := <-sink:
						chunk, ok := ev.(turnevent.TextChunk)
						if !ok || chunk.MessageID != beforeID || chunk.Text != "still replying" {
							t.Fatalf("current reply did not continue: %+v", ev)
						}
					default:
						t.Fatal("current reply no longer emitted text")
					}
				}
				if r.ReconnectMCPServer(ctx, "server") || r.SetMCPServerEnabled(ctx, "server", true) || w.count() != 1 {
					t.Fatal("MCP eligibility gate changed")
				}
			})
		}
	}
}

func TestRunner_StopTask_OverlapsAndRetiredIDs(t *testing.T) {
	sink := make(chan turnevent.Event, 8)
	p := NewParser(func(ev turnevent.Event) { sink <- ev }, discardLogger())
	w := newMCPStatusQueryWriter()
	r := mcpStatusQueryRunner(t, p, w, true)
	ctx := appliedSettingsQueryContext(t)
	first := stopAsync(t, ctx, r, "first")
	req1 := awaitActuationRequest(t, w, "stop_task")
	second := stopAsync(t, ctx, r, "second")
	req2 := awaitActuationRequest(t, w, "stop_task")
	mcp := actuateAsync(ctx, func(ctx context.Context) bool { return r.ReconnectMCPServer(ctx, "server") })
	reqMCP := awaitActuationRequest(t, w, "mcp_reconnect")
	if req1.RequestID == req2.RequestID || req1.RequestID == reqMCP.RequestID || req2.RequestID == reqMCP.RequestID {
		t.Fatal("requests reused ids")
	}
	_, _ = p.Write(actuationAck(t, mcpActuationIDPrefix+"unknown", "success", mcpStatusQueryServers("unrelated", 1)))
	_, _ = p.Write(actuationAck(t, "12345", "success", nil))
	select {
	case got := <-first:
		t.Fatalf("unrelated id completed first: %v", got)
	default:
	}
	_, _ = p.Write(actuationAck(t, req2.RequestID, "error", nil))
	if awaitActuationOutcome(t, second) {
		t.Fatal("second stop accepted refusal")
	}
	_, _ = p.Write(actuationAck(t, req2.RequestID, "success", mcpStatusQueryServers("duplicate", 1)))
	_, _ = p.Write(actuationAck(t, reqMCP.RequestID, "success", nil))
	if !awaitActuationOutcome(t, mcp) {
		t.Fatal("MCP request did not correlate independently")
	}
	select {
	case got := <-first:
		t.Fatalf("other requests completed first: %v", got)
	default:
	}
	_, _ = p.Write(actuationAck(t, req1.RequestID, "success", nil))
	if !awaitActuationOutcome(t, first) {
		t.Fatal("first stop refused its exact success")
	}
	assertNoActuationSinkEvent(t, sink)
}

func TestRunner_StopTask_UnavailableAndPreCanceled(t *testing.T) {
	for _, state := range []string{"no child", "no parser", "rotation", "canceled", "expired"} {
		t.Run(state, func(t *testing.T) {
			p := NewParser(func(turnevent.Event) {}, discardLogger())
			if state == "no parser" {
				p = nil
			}
			w := newMCPStatusQueryWriter()
			var target io.WriteCloser = w
			if state == "no child" {
				target = nil
			}
			r := mcpStatusQueryRunner(t, p, target, false)
			r.rotating = state == "rotation"
			ctx := appliedSettingsQueryContext(t)
			if state == "canceled" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			if state == "expired" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithDeadline(ctx, time.Now().Add(-time.Second))
				defer cancel()
			}
			if r.StopTask(ctx, "task") || w.count() != 0 {
				t.Fatal("unavailable stop wrote or accepted")
			}
		})
	}
}

func TestRunner_StopTask_SilentChildDeadline(t *testing.T) {
	sink := make(chan turnevent.Event, 8)
	p := NewParser(func(ev turnevent.Event) { sink <- ev }, discardLogger())
	w := newMCPStatusQueryWriter()
	r := mcpStatusQueryRunner(t, p, w, false)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	done := stopAsync(t, ctx, r, "task")
	req := awaitActuationRequest(t, w, "stop_task")
	awaitStopWrite(t, p, req.RequestID)
	if awaitActuationOutcome(t, done) || ctx.Err() != context.DeadlineExceeded {
		t.Fatal("silent child did not respect deadline")
	}
	_, _ = p.Write(actuationAck(t, req.RequestID, "success", mcpStatusQueryServers("late", 1)))
	assertNoActuationSinkEvent(t, sink)
	if r.Stdin() != w {
		t.Fatal("response-wait expiry retired child")
	}
}

func TestRunner_StopTask_ChildBoundary(t *testing.T) {
	for _, replace := range []bool{false, true} {
		t.Run(map[bool]string{false: "exit", true: "replacement"}[replace], func(t *testing.T) {
			sink := make(chan turnevent.Event, 8)
			p := NewParser(func(ev turnevent.Event) { sink <- ev }, discardLogger())
			w := newMCPStatusQueryWriter()
			r := mcpStatusQueryRunner(t, p, w, false)
			done := stopAsync(t, appliedSettingsQueryContext(t), r, "task")
			req := awaitActuationRequest(t, w, "stop_task")
			awaitStopWrite(t, p, req.RequestID)
			old := r.takeStdin()
			_ = old.Close()
			successor := newMCPStatusQueryWriter()
			if replace {
				p.beginMCPStatusChild(false, nil)
				r.setStdin(successor, 0, false)
			}
			if awaitActuationOutcome(t, done) {
				t.Fatal("stop survived child boundary")
			}
			if replace {
				next := stopAsync(t, appliedSettingsQueryContext(t), r, "next")
				nextReq := awaitActuationRequest(t, successor, "stop_task")
				_, _ = p.Write(actuationAck(t, req.RequestID, "success", mcpStatusQueryServers("retired", 1)))
				select {
				case got := <-next:
					t.Fatalf("retired id completed successor stop: %v", got)
				default:
				}
				_, _ = p.Write(actuationAck(t, nextReq.RequestID, "success", nil))
				if !awaitActuationOutcome(t, next) {
					t.Fatal("successor stop was refused")
				}
			} else {
				_, _ = p.Write(actuationAck(t, req.RequestID, "success", mcpStatusQueryServers("retired", 1)))
			}
			assertNoActuationSinkEvent(t, sink)
		})
	}
}

func TestRunner_StopTask_EarlyReplyWriteGate(t *testing.T) {
	for _, outcome := range []string{"success", "error", "short", "cancel", "replacement"} {
		t.Run(outcome, func(t *testing.T) {
			var logs bytes.Buffer
			logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
			sink := make(chan turnevent.Event, 8)
			p := NewParser(func(ev turnevent.Event) { sink <- ev }, logger)
			w := newMCPStatusQueryWriter()
			r := mcpStatusQueryRunner(t, p, w, false)
			r.log = logger
			ctx, cancel := context.WithCancel(appliedSettingsQueryContext(t))
			defer cancel()
			parsed := make(chan struct{})
			w.write = func(raw []byte) (int, error) {
				var req controlRequest
				if err := json.Unmarshal(raw, &req); err != nil {
					return 0, err
				}
				go func() { _, _ = p.Write(actuationAck(t, req.RequestID, "success", nil)); close(parsed) }()
				deadline := time.Now().Add(3 * time.Second)
				for {
					p.mcpActuations.mu.Lock()
					_, pending := p.mcpActuations.pending[req.RequestID]
					p.mcpActuations.mu.Unlock()
					if !pending {
						break
					}
					if time.Now().After(deadline) {
						t.Error("early response was not claimed")
						break
					}
					runtime.Gosched()
				}
				switch outcome {
				case "error":
					return 0, errors.New("private-stop-writer-error-sentinel")
				case "short":
					return len(raw) - 1, nil
				case "cancel":
					cancel()
				case "replacement":
					r.setStdin(newMCPStatusQueryWriter(), 0, false)
				}
				return len(raw), nil
			}
			if got := r.StopTask(ctx, "private-task-id-sentinel"); got != (outcome == "success") {
				t.Fatalf("early response accepted = %v for %s", got, outcome)
			}
			select {
			case <-parsed:
			case <-time.After(3 * time.Second):
				t.Fatal("parser write gate did not release")
			}
			assertNoActuationSinkEvent(t, sink)
			if logs.Len() != 0 {
				t.Fatalf("private stop wrote logs: %s", logs.String())
			}
		})
	}
}

func TestRunner_StopTask_BlockedWriteCancellation(t *testing.T) {
	for _, mode := range []string{"cancel", "deadline", "replacement"} {
		t.Run(mode, func(t *testing.T) {
			replace := mode == "replacement"
			p := NewParser(func(turnevent.Event) {}, discardLogger())
			w := newBlockingAppliedSettingsWriter()
			defer w.Close()
			r := mcpStatusQueryRunner(t, p, w, false)
			ctx, cancel := context.WithCancel(appliedSettingsQueryContext(t))
			if mode == "deadline" {
				cancel()
				ctx, cancel = context.WithTimeout(context.Background(), 100*time.Millisecond)
			}
			defer cancel()
			done := stopAsync(t, ctx, r, "task")
			select {
			case <-w.started:
			case <-time.After(3 * time.Second):
				t.Fatal("write did not start")
			}
			successor := newMCPStatusQueryWriter()
			if replace {
				r.setStdin(successor, 0, false)
			}
			if mode != "deadline" {
				cancel()
			}
			if replace {
				// An actual replacement closes its predecessor; cancellation owns only the
				// captured generation and therefore cannot close this replacement for it.
				_ = w.Close()
			}
			select {
			case got := <-done:
				if got {
					t.Fatal("canceled blocked write accepted")
				}
			case <-time.After(time.Second):
				_ = w.Close()
				<-done
				t.Fatal("cancellation did not release blocked write")
			}
			if replace {
				if r.Stdin() != successor {
					t.Fatal("cancellation retired replacement")
				}
			} else if r.Stdin() != nil {
				t.Fatal("cancellation did not retire captured child")
			}
		})
	}
}
