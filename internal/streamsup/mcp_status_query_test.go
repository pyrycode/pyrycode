package streamsup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/turnevent"
)

type mcpStatusQueryWriteCloser struct {
	mu     sync.Mutex
	writes [][]byte
	write  func([]byte) (int, error)
	wrote  chan []byte
}

func newMCPStatusQueryWriter() *mcpStatusQueryWriteCloser {
	return &mcpStatusQueryWriteCloser{wrote: make(chan []byte, 8)}
}

func (w *mcpStatusQueryWriteCloser) Write(p []byte) (int, error) {
	copyOfP := bytes.Clone(p)
	w.mu.Lock()
	w.writes = append(w.writes, copyOfP)
	write := w.write
	w.mu.Unlock()
	if w.wrote != nil {
		w.wrote <- copyOfP
	}
	if write != nil {
		return write(copyOfP)
	}
	return len(p), nil
}

func (*mcpStatusQueryWriteCloser) Close() error { return nil }

func (w *mcpStatusQueryWriteCloser) count() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.writes)
}

func mcpStatusQueryRunner(t *testing.T, p *Parser, w io.WriteCloser, eligible bool) *Runner {
	t.Helper()
	cfg := helperRunCfg(t, "echo_lines", &safeBuffer{}, &safeBuffer{})
	if p != nil {
		cfg.Stdout = p
	}
	r, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	r.mu.Lock()
	r.stdin = w
	r.mcpStatusEligible = eligible
	r.mu.Unlock()
	t.Cleanup(func() {
		if old := r.takeStdin(); old != nil {
			_ = old.Close()
		}
	})
	return r
}

type mcpStatusQueryOutcome struct {
	status turnevent.MCPStatus
	ok     bool
}

func queryMCPStatusAsync(ctx context.Context, r *Runner) <-chan mcpStatusQueryOutcome {
	done := make(chan mcpStatusQueryOutcome, 1)
	go func() {
		status, ok := r.QueryMCPStatus(ctx)
		done <- mcpStatusQueryOutcome{status: status, ok: ok}
	}()
	return done
}

func awaitMCPStatusRequest(t *testing.T, w *mcpStatusQueryWriteCloser) controlRequest {
	t.Helper()
	select {
	case raw := <-w.wrote:
		var req controlRequest
		if err := json.Unmarshal(bytes.TrimSpace(raw), &req); err != nil {
			t.Fatalf("decode MCP status request: %v", err)
		}
		if req.Request.Subtype != "mcp_status" {
			t.Fatalf("request subtype = %q, want mcp_status", req.Request.Subtype)
		}
		return req
	case <-time.After(3 * time.Second):
		t.Fatal("MCP status request was not written")
		return controlRequest{}
	}
}

func awaitMCPStatusOutcome(t *testing.T, done <-chan mcpStatusQueryOutcome) mcpStatusQueryOutcome {
	t.Helper()
	select {
	case got := <-done:
		return got
	case <-time.After(3 * time.Second):
		t.Fatal("MCP status query did not complete")
		return mcpStatusQueryOutcome{}
	}
}

func mcpStatusQueryResponse(t *testing.T, requestID, subtype string, servers any) []byte {
	t.Helper()
	response := map[string]any{
		"subtype":    subtype,
		"request_id": requestID,
	}
	if servers != nil {
		response["response"] = map[string]any{"mcpServers": servers}
	}
	raw, err := json.Marshal(map[string]any{
		"type":     "control_response",
		"response": response,
	})
	if err != nil {
		t.Fatalf("marshal MCP status response: %v", err)
	}
	return append(raw, '\n')
}

func mcpStatusQueryServers(tag string, count int) []map[string]any {
	servers := make([]map[string]any, count)
	for i := range servers {
		servers[i] = map[string]any{
			"name":   tag + "-name-" + string(rune('a'+i)),
			"status": tag + "-status-" + string(rune('a'+i)),
			"error":  tag + "-error-" + string(rune('a'+i)),
			"scope":  tag + "-scope-" + string(rune('a'+i)),
			"serverInfo": map[string]any{
				"version": tag + "-version-" + string(rune('a'+i)),
			},
		}
	}
	return servers
}

func assertNoMCPStatusSinkEvent(t *testing.T, sink <-chan turnevent.Event) {
	t.Helper()
	select {
	case ev := <-sink:
		t.Fatalf("claimed MCP status reached shared sink as %T: %+v", ev, ev)
	default:
	}
}

func TestRunner_QueryMCPStatus_ExactResponseIsPrivateAndMapped(t *testing.T) {
	sink := make(chan turnevent.Event, 4)
	p := NewParser(func(ev turnevent.Event) { sink <- ev }, discardLogger())
	w := newMCPStatusQueryWriter()
	r := mcpStatusQueryRunner(t, p, w, true)

	done := queryMCPStatusAsync(context.Background(), r)
	req := awaitMCPStatusRequest(t, w)
	if !strings.HasPrefix(req.RequestID, mcpStatusQueryIDPrefix) {
		t.Fatalf("request_id = %q, want prefix %q", req.RequestID, mcpStatusQueryIDPrefix)
	}
	servers := mcpStatusQueryServers("exact", maxMCPStatusServers+2)
	if _, err := p.Write(mcpStatusQueryResponse(t, req.RequestID, "success", servers)); err != nil {
		t.Fatalf("Parser.Write: %v", err)
	}

	got := awaitMCPStatusOutcome(t, done)
	if !got.ok {
		t.Fatal("exact successful response reported unavailable")
	}
	if len(got.status.Servers) != maxMCPStatusServers || got.status.DroppedServers != 2 {
		t.Fatalf("status size/drop = (%d,%d), want (%d,2)",
			len(got.status.Servers), got.status.DroppedServers, maxMCPStatusServers)
	}
	for i, server := range got.status.Servers {
		letter := string(rune('a' + i))
		want := turnevent.MCPServerStatus{
			Name:    "exact-name-" + letter,
			Status:  "exact-status-" + letter,
			Error:   "exact-error-" + letter,
			Scope:   "exact-scope-" + letter,
			Version: "exact-version-" + letter,
		}
		if server != want {
			t.Errorf("Servers[%d] = %+v, want %+v", i, server, want)
		}
	}
	assertNoMCPStatusSinkEvent(t, sink)
}

func TestRunner_QueryMCPStatus_OverlappingQueriesCorrelateIndependently(t *testing.T) {
	sink := make(chan turnevent.Event, 4)
	p := NewParser(func(ev turnevent.Event) { sink <- ev }, discardLogger())
	w := newMCPStatusQueryWriter()
	r := mcpStatusQueryRunner(t, p, w, true)

	firstDone := queryMCPStatusAsync(context.Background(), r)
	firstReq := awaitMCPStatusRequest(t, w)
	secondDone := queryMCPStatusAsync(context.Background(), r)
	secondReq := awaitMCPStatusRequest(t, w)
	if firstReq.RequestID == secondReq.RequestID {
		t.Fatalf("overlapping queries reused request id %q", firstReq.RequestID)
	}

	_, _ = p.Write(mcpStatusQueryResponse(t, secondReq.RequestID, "success", mcpStatusQueryServers("second", 1)))
	second := awaitMCPStatusOutcome(t, secondDone)
	if !second.ok || second.status.Servers[0].Name != "second-name-a" {
		t.Fatalf("second query result = %+v, want second response", second)
	}
	select {
	case got := <-firstDone:
		t.Fatalf("first query completed from second id: %+v", got)
	default:
	}

	_, _ = p.Write(mcpStatusQueryResponse(t, firstReq.RequestID, "success", mcpStatusQueryServers("first", 1)))
	first := awaitMCPStatusOutcome(t, firstDone)
	if !first.ok || first.status.Servers[0].Name != "first-name-a" {
		t.Fatalf("first query result = %+v, want first response", first)
	}
	assertNoMCPStatusSinkEvent(t, sink)
}

func TestRunner_QueryMCPStatus_UnknownIDCannotCompleteQuery(t *testing.T) {
	sink := make(chan turnevent.Event, 4)
	p := NewParser(func(ev turnevent.Event) { sink <- ev }, discardLogger())
	w := newMCPStatusQueryWriter()
	r := mcpStatusQueryRunner(t, p, w, true)

	done := queryMCPStatusAsync(context.Background(), r)
	req := awaitMCPStatusRequest(t, w)
	_, _ = p.Write(mcpStatusQueryResponse(t, "987654", "success", mcpStatusQueryServers("automatic", 1)))
	select {
	case got := <-done:
		t.Fatalf("unknown id completed query: %+v", got)
	default:
	}
	select {
	case ev := <-sink:
		status, ok := ev.(turnevent.MCPStatus)
		if !ok || status.Servers[0].Name != "automatic-name-a" {
			t.Fatalf("unclaimed automatic response emitted %+v", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("unclaimed numeric response no longer follows automatic sink path")
	}

	_, _ = p.Write(mcpStatusQueryResponse(t, req.RequestID, "success", mcpStatusQueryServers("exact", 1)))
	got := awaitMCPStatusOutcome(t, done)
	if !got.ok || got.status.Servers[0].Name != "exact-name-a" {
		t.Fatalf("exact response result = %+v", got)
	}
	assertNoMCPStatusSinkEvent(t, sink)
}

func TestRunner_QueryMCPStatus_FirstMatchedResponseIsTerminal(t *testing.T) {
	tests := []struct {
		name    string
		subtype string
		servers any
	}{
		{name: "error", subtype: "error"},
		{name: "malformed payload", subtype: "success", servers: "not-an-array"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sink := make(chan turnevent.Event, 4)
			p := NewParser(func(ev turnevent.Event) { sink <- ev }, discardLogger())
			w := newMCPStatusQueryWriter()
			r := mcpStatusQueryRunner(t, p, w, true)

			done := queryMCPStatusAsync(context.Background(), r)
			req := awaitMCPStatusRequest(t, w)
			_, _ = p.Write(mcpStatusQueryResponse(t, req.RequestID, tc.subtype, tc.servers))
			if got := awaitMCPStatusOutcome(t, done); got.ok {
				t.Fatalf("first unusable response returned success: %+v", got)
			}
			_, _ = p.Write(mcpStatusQueryResponse(t, req.RequestID, "success", mcpStatusQueryServers("duplicate", 1)))
			assertNoMCPStatusSinkEvent(t, sink)
		})
	}
}

func TestRunner_QueryMCPStatus_UnavailableCasesWriteNothing(t *testing.T) {
	tests := []struct {
		name     string
		parser   bool
		writer   bool
		eligible bool
	}{
		{name: "no live child", parser: true, eligible: true},
		{name: "ineligible child", parser: true, writer: true},
		{name: "no parser", writer: true, eligible: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var p *Parser
			if tc.parser {
				p = NewParser(func(turnevent.Event) {}, discardLogger())
			}
			w := newMCPStatusQueryWriter()
			var target io.WriteCloser
			if tc.writer {
				target = w
			}
			r := mcpStatusQueryRunner(t, p, target, tc.eligible)
			if status, ok := r.QueryMCPStatus(context.Background()); ok {
				t.Fatalf("QueryMCPStatus = (%+v,true), want unavailable", status)
			}
			if got := w.count(); got != 0 {
				t.Fatalf("writes = %d, want 0", got)
			}
		})
	}
}

func TestRunner_QueryMCPStatus_CancellationRemovesCorrelation(t *testing.T) {
	sink := make(chan turnevent.Event, 4)
	p := NewParser(func(ev turnevent.Event) { sink <- ev }, discardLogger())
	w := newMCPStatusQueryWriter()
	r := mcpStatusQueryRunner(t, p, w, true)
	ctx, cancel := context.WithCancel(context.Background())
	done := queryMCPStatusAsync(ctx, r)
	req := awaitMCPStatusRequest(t, w)
	cancel()
	_, _ = p.Write(mcpStatusQueryResponse(t, req.RequestID, "success", mcpStatusQueryServers("late", 1)))
	if got := awaitMCPStatusOutcome(t, done); got.ok {
		t.Fatalf("canceled query returned success: %+v", got)
	}
	assertNoMCPStatusSinkEvent(t, sink)
}

func TestRunner_QueryMCPStatus_WriteFailureWinsOverEarlyResponse(t *testing.T) {
	sink := make(chan turnevent.Event, 4)
	p := NewParser(func(ev turnevent.Event) { sink <- ev }, discardLogger())
	w := newMCPStatusQueryWriter()
	writeErr := errors.New("MCP status write failure sentinel")
	parseDone := make(chan struct{})
	w.write = func(raw []byte) (int, error) {
		var req controlRequest
		if err := json.Unmarshal(bytes.TrimSpace(raw), &req); err != nil {
			return 0, err
		}
		go func() {
			_, _ = p.Write(mcpStatusQueryResponse(t, req.RequestID, "success", mcpStatusQueryServers("early", 1)))
			close(parseDone)
		}()
		deadline := time.Now().Add(3 * time.Second)
		for {
			p.mcpStatusQueries.mu.Lock()
			_, stillPending := p.mcpStatusQueries.pending[req.RequestID]
			p.mcpStatusQueries.mu.Unlock()
			if !stillPending {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("parser did not claim response while writer was blocked")
			}
			runtime.Gosched()
		}
		return 0, writeErr
	}
	r := mcpStatusQueryRunner(t, p, w, true)
	if got, ok := r.QueryMCPStatus(context.Background()); ok {
		t.Fatalf("failed write returned success: %+v", got)
	}
	select {
	case <-parseDone:
	case <-time.After(3 * time.Second):
		t.Fatal("early parser response did not finish after write failure")
	}
	assertNoMCPStatusSinkEvent(t, sink)
}

func TestRunner_QueryMCPStatus_ChildExitFailsPendingQuery(t *testing.T) {
	sink := make(chan turnevent.Event, 4)
	p := NewParser(func(ev turnevent.Event) { sink <- ev }, discardLogger())
	w := newMCPStatusQueryWriter()
	r := mcpStatusQueryRunner(t, p, w, true)
	done := queryMCPStatusAsync(context.Background(), r)
	req := awaitMCPStatusRequest(t, w)
	if old := r.takeStdin(); old == nil {
		t.Fatal("takeStdin returned nil for installed child")
	}
	if got := awaitMCPStatusOutcome(t, done); got.ok {
		t.Fatalf("query survived child exit: %+v", got)
	}
	_, _ = p.Write(mcpStatusQueryResponse(t, req.RequestID, "success", mcpStatusQueryServers("departed", 1)))
	assertNoMCPStatusSinkEvent(t, sink)
	if status, ok := r.QueryMCPStatus(context.Background()); ok {
		t.Fatalf("post-exit query = (%+v,true), want unavailable", status)
	}
}
