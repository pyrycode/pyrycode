package streamsup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// The writer double, the runner builder and the response builder are
// mcp_status_query_test.go's, reused rather than re-declared: both correlators
// drive the same stdin handle and read the same control_response shape, and a
// second copy would drift from the one the shipped path is proved against.

func actuateAsync(ctx context.Context, fn func(context.Context) bool) <-chan bool {
	done := make(chan bool, 1)
	go func() { done <- fn(ctx) }()
	return done
}

func awaitActuationRequest(t *testing.T, w *mcpStatusQueryWriteCloser, wantSubtype string) controlRequest {
	t.Helper()
	select {
	case raw := <-w.wrote:
		var req controlRequest
		if err := json.Unmarshal(bytes.TrimSpace(raw), &req); err != nil {
			t.Fatalf("decode actuation request: %v", err)
		}
		if req.Request.Subtype != wantSubtype {
			t.Fatalf("request subtype = %q, want %q", req.Request.Subtype, wantSubtype)
		}
		if !strings.HasPrefix(req.RequestID, mcpActuationIDPrefix) {
			t.Fatalf("request_id = %q, want prefix %q", req.RequestID, mcpActuationIDPrefix)
		}
		return req
	case <-time.After(3 * time.Second):
		t.Fatalf("%s request was not written", wantSubtype)
		return controlRequest{}
	}
}

func awaitActuationOutcome(t *testing.T, done <-chan bool) bool {
	t.Helper()
	select {
	case got := <-done:
		return got
	case <-time.After(3 * time.Second):
		t.Fatal("actuation did not complete")
		return false
	}
}

func assertNoActuationSinkEvent(t *testing.T, sink <-chan turnevent.Event) {
	t.Helper()
	select {
	case ev := <-sink:
		t.Fatalf("claimed actuation ack reached shared sink as %T: %+v", ev, ev)
	default:
	}
}

// actuationAck builds one control_response for id. servers, when non-nil, adds the
// payload key the shape decoder emits on, proving a claimed ack is never handed to it.
func actuationAck(t *testing.T, requestID, subtype string, servers any) []byte {
	t.Helper()
	return mcpStatusQueryResponse(t, requestID, subtype, servers)
}

func TestRunner_ReconnectMCPServer_ExactAckAccepts(t *testing.T) {
	sink := make(chan turnevent.Event, 4)
	p := NewParser(func(ev turnevent.Event) { sink <- ev }, discardLogger())
	w := newMCPStatusQueryWriter()
	r := mcpStatusQueryRunner(t, p, w, true)

	done := actuateAsync(context.Background(), func(ctx context.Context) bool {
		return r.ReconnectMCPServer(ctx, "review-server")
	})
	req := awaitActuationRequest(t, w, "mcp_reconnect")
	if req.Request.ServerName == nil || *req.Request.ServerName != "review-server" {
		t.Fatalf("serverName = %v, want review-server", req.Request.ServerName)
	}
	if req.Request.Enabled != nil {
		t.Fatalf("reconnect carried enabled = %v, want absent", *req.Request.Enabled)
	}
	if _, err := p.Write(actuationAck(t, req.RequestID, "success", nil)); err != nil {
		t.Fatalf("Parser.Write: %v", err)
	}
	if !awaitActuationOutcome(t, done) {
		t.Fatal("exact success ack reported not-accepted")
	}
	assertNoActuationSinkEvent(t, sink)
}

// The caller's flag reaches the wire in both directions: a toggle that fixed one
// value, or derived it from the current state, would pass a true-only assertion.
func TestRunner_SetMCPServerEnabled_PassesFlagThrough(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		name := "disable"
		if enabled {
			name = "enable"
		}
		t.Run(name, func(t *testing.T) {
			sink := make(chan turnevent.Event, 4)
			p := NewParser(func(ev turnevent.Event) { sink <- ev }, discardLogger())
			w := newMCPStatusQueryWriter()
			r := mcpStatusQueryRunner(t, p, w, true)

			done := actuateAsync(context.Background(), func(ctx context.Context) bool {
				return r.SetMCPServerEnabled(ctx, "review-server", enabled)
			})
			req := awaitActuationRequest(t, w, "mcp_toggle")
			if req.Request.ServerName == nil || *req.Request.ServerName != "review-server" {
				t.Fatalf("serverName = %v, want review-server", req.Request.ServerName)
			}
			if req.Request.Enabled == nil || *req.Request.Enabled != enabled {
				t.Fatalf("enabled = %v, want %v", req.Request.Enabled, enabled)
			}
			_, _ = p.Write(actuationAck(t, req.RequestID, "success", nil))
			if !awaitActuationOutcome(t, done) {
				t.Fatal("exact success ack reported not-accepted")
			}
			assertNoActuationSinkEvent(t, sink)
		})
	}
}

// Matching the id must not by itself read as accepted, and the first response
// matching a pending id is terminal for it whatever it says.
func TestRunner_ActuateMCP_NonSuccessAckIsNotAccepted(t *testing.T) {
	tests := []struct {
		name    string
		subtype string
	}{
		{name: "error", subtype: "error"},
		{name: "absent subtype", subtype: ""},
		{name: "unknown subtype", subtype: "deferred"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sink := make(chan turnevent.Event, 4)
			p := NewParser(func(ev turnevent.Event) { sink <- ev }, discardLogger())
			w := newMCPStatusQueryWriter()
			r := mcpStatusQueryRunner(t, p, w, true)

			done := actuateAsync(context.Background(), func(ctx context.Context) bool {
				return r.ReconnectMCPServer(ctx, "review-server")
			})
			req := awaitActuationRequest(t, w, "mcp_reconnect")
			_, _ = p.Write(actuationAck(t, req.RequestID, tc.subtype, nil))
			if awaitActuationOutcome(t, done) {
				t.Fatal("non-success ack reported accepted")
			}
			// A late success for the retired id is inert rather than a second answer.
			_, _ = p.Write(actuationAck(t, req.RequestID, "success", nil))
			assertNoActuationSinkEvent(t, sink)
		})
	}
}

// An unclaimed ack of any shape must reach no consumer of this package, and the
// automatic once-per-eligible-child status path must be undisturbed by the claim.
func TestRunner_ActuateMCP_ClaimedAckReachesNoSinkAndLeavesAutomaticPathAlone(t *testing.T) {
	sink := make(chan turnevent.Event, 4)
	p := NewParser(func(ev turnevent.Event) { sink <- ev }, discardLogger())
	w := newMCPStatusQueryWriter()
	r := mcpStatusQueryRunner(t, p, w, true)

	done := actuateAsync(context.Background(), func(ctx context.Context) bool {
		return r.ReconnectMCPServer(ctx, "review-server")
	})
	req := awaitActuationRequest(t, w, "mcp_reconnect")

	// An automatic numeric id keeps its existing path to the shared sink.
	_, _ = p.Write(mcpStatusQueryResponse(t, "987654", "success", mcpStatusQueryServers("automatic", 1)))
	select {
	case ev := <-sink:
		status, ok := ev.(turnevent.MCPStatus)
		if !ok || status.Servers[0].Name != "automatic-name-a" {
			t.Fatalf("unclaimed automatic response emitted %+v", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("automatic status response no longer reaches the shared sink")
	}
	select {
	case got := <-done:
		t.Fatalf("automatic response completed a pending actuation: %v", got)
	default:
	}

	// An unregistered id inside the actuation namespace completes nothing either.
	_, _ = p.Write(actuationAck(t, mcpActuationIDPrefix+"404", "success", nil))
	select {
	case got := <-done:
		t.Fatalf("unknown actuation id completed the pending actuation: %v", got)
	default:
	}

	// The exact ack carries a payload the shape decoder would emit on; claiming it
	// must keep that payload away from the sink entirely.
	_, _ = p.Write(actuationAck(t, req.RequestID, "success", mcpStatusQueryServers("claimed", 1)))
	if !awaitActuationOutcome(t, done) {
		t.Fatal("exact success ack reported not-accepted")
	}
	assertNoActuationSinkEvent(t, sink)
}

func TestRunner_ActuateMCP_OverlappingActuationsCorrelateIndependently(t *testing.T) {
	sink := make(chan turnevent.Event, 4)
	p := NewParser(func(ev turnevent.Event) { sink <- ev }, discardLogger())
	w := newMCPStatusQueryWriter()
	r := mcpStatusQueryRunner(t, p, w, true)

	reconnectDone := actuateAsync(context.Background(), func(ctx context.Context) bool {
		return r.ReconnectMCPServer(ctx, "first-server")
	})
	reconnectReq := awaitActuationRequest(t, w, "mcp_reconnect")
	toggleDone := actuateAsync(context.Background(), func(ctx context.Context) bool {
		return r.SetMCPServerEnabled(ctx, "second-server", false)
	})
	toggleReq := awaitActuationRequest(t, w, "mcp_toggle")
	if reconnectReq.RequestID == toggleReq.RequestID {
		t.Fatalf("overlapping actuations reused request id %q", reconnectReq.RequestID)
	}

	// Answering the second says nothing about the first, and the two verdicts differ
	// so neither can be passing on the other's result.
	_, _ = p.Write(actuationAck(t, toggleReq.RequestID, "error", nil))
	if awaitActuationOutcome(t, toggleDone) {
		t.Fatal("toggle error ack reported accepted")
	}
	select {
	case got := <-reconnectDone:
		t.Fatalf("reconnect completed from the toggle's id: %v", got)
	default:
	}

	_, _ = p.Write(actuationAck(t, reconnectReq.RequestID, "success", nil))
	if !awaitActuationOutcome(t, reconnectDone) {
		t.Fatal("reconnect success ack reported not-accepted")
	}
	assertNoActuationSinkEvent(t, sink)
}

func TestRunner_ActuateMCP_UnavailableCasesWriteNothing(t *testing.T) {
	tests := []struct {
		name     string
		parser   bool
		writer   bool
		eligible bool
		rotating bool
	}{
		{name: "no live child", parser: true, eligible: true},
		{name: "ineligible child", parser: true, writer: true},
		{name: "rotating child", parser: true, writer: true, eligible: true, rotating: true},
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
			if tc.rotating {
				r.mu.Lock()
				r.rotating = true
				r.mu.Unlock()
			}
			if r.ReconnectMCPServer(context.Background(), "review-server") {
				t.Error("ReconnectMCPServer reported accepted, want unavailable")
			}
			if r.SetMCPServerEnabled(context.Background(), "review-server", true) {
				t.Error("SetMCPServerEnabled reported accepted, want unavailable")
			}
			if got := w.count(); got != 0 {
				t.Fatalf("writes = %d, want 0", got)
			}
		})
	}
}

// A write that fails after the parser has already claimed a reply must still report
// unavailable: the ack proves the child answered some request, not that this one
// reached it intact.
func TestRunner_ActuateMCP_WriteFailureWinsOverEarlyAck(t *testing.T) {
	sink := make(chan turnevent.Event, 4)
	p := NewParser(func(ev turnevent.Event) { sink <- ev }, discardLogger())
	w := newMCPStatusQueryWriter()
	writeErr := errors.New("MCP actuation write failure sentinel")
	parseDone := make(chan struct{})
	w.write = func(raw []byte) (int, error) {
		var req controlRequest
		if err := json.Unmarshal(bytes.TrimSpace(raw), &req); err != nil {
			return 0, err
		}
		go func() {
			_, _ = p.Write(actuationAck(t, req.RequestID, "success", nil))
			close(parseDone)
		}()
		deadline := time.Now().Add(3 * time.Second)
		for {
			p.mcpActuations.mu.Lock()
			_, stillPending := p.mcpActuations.pending[req.RequestID]
			p.mcpActuations.mu.Unlock()
			if !stillPending {
				break
			}
			if time.Now().After(deadline) {
				t.Error("parser did not claim ack while writer was blocked")
				break
			}
			runtime.Gosched()
		}
		return 0, writeErr
	}
	r := mcpStatusQueryRunner(t, p, w, true)
	if r.ReconnectMCPServer(context.Background(), "review-server") {
		t.Fatal("failed write reported accepted")
	}
	select {
	case <-parseDone:
	case <-time.After(3 * time.Second):
		t.Fatal("early parser ack did not finish after write failure")
	}
	assertNoActuationSinkEvent(t, sink)
}

func TestRunner_ActuateMCP_CancellationRemovesCorrelation(t *testing.T) {
	sink := make(chan turnevent.Event, 4)
	p := NewParser(func(ev turnevent.Event) { sink <- ev }, discardLogger())
	w := newMCPStatusQueryWriter()
	r := mcpStatusQueryRunner(t, p, w, true)

	ctx, cancel := context.WithCancel(context.Background())
	done := actuateAsync(ctx, func(ctx context.Context) bool {
		return r.SetMCPServerEnabled(ctx, "review-server", false)
	})
	req := awaitActuationRequest(t, w, "mcp_toggle")
	cancel()
	_, _ = p.Write(actuationAck(t, req.RequestID, "success", nil))
	if awaitActuationOutcome(t, done) {
		t.Fatal("canceled actuation reported accepted")
	}
	assertNoActuationSinkEvent(t, sink)
}

// No waiter outlives its child, by either boundary hook, and no late ack can
// satisfy a retired one.
func TestRunner_ActuateMCP_ChildBoundaryFailsPendingActuation(t *testing.T) {
	tests := []struct {
		name   string
		retire func(*Runner, *Parser)
	}{
		{
			name:   "child exit",
			retire: func(r *Runner, _ *Parser) { _ = r.takeStdin() },
		},
		{
			name:   "child replacement",
			retire: func(_ *Runner, p *Parser) { p.beginMCPStatusChild(true, nil) },
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sink := make(chan turnevent.Event, 4)
			p := NewParser(func(ev turnevent.Event) { sink <- ev }, discardLogger())
			w := newMCPStatusQueryWriter()
			r := mcpStatusQueryRunner(t, p, w, true)

			done := actuateAsync(context.Background(), func(ctx context.Context) bool {
				return r.ReconnectMCPServer(ctx, "review-server")
			})
			req := awaitActuationRequest(t, w, "mcp_reconnect")
			tc.retire(r, p)
			if awaitActuationOutcome(t, done) {
				t.Fatal("actuation survived the child boundary")
			}
			_, _ = p.Write(actuationAck(t, req.RequestID, "success", nil))
			assertNoActuationSinkEvent(t, sink)
		})
	}
}
