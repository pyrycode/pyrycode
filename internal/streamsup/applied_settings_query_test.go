package streamsup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/turnevent"
)

type appliedSettingsQueryOutcome struct {
	settings AppliedSettings
	ok       bool
}

func appliedSettingsQueryContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func appliedSettingsQueryRunner(t *testing.T, p *Parser, w *mcpStatusQueryWriteCloser) *Runner {
	t.Helper()
	// eligible=false deliberately: MCP provenance has no bearing on a settings read.
	var target io.WriteCloser
	if w != nil {
		target = w
	}
	return mcpStatusQueryRunner(t, p, target, false)
}

func queryAppliedSettingsAsync(ctx context.Context, r *Runner) <-chan appliedSettingsQueryOutcome {
	done := make(chan appliedSettingsQueryOutcome, 1)
	go func() {
		settings, ok := r.QueryAppliedSettings(ctx)
		done <- appliedSettingsQueryOutcome{settings: settings, ok: ok}
	}()
	return done
}

func awaitAppliedSettingsRequest(t *testing.T, w *mcpStatusQueryWriteCloser) (controlRequest, []byte) {
	t.Helper()
	select {
	case raw := <-w.wrote:
		var req controlRequest
		if err := json.Unmarshal(bytes.TrimSpace(raw), &req); err != nil {
			t.Fatalf("decode applied settings request: %v", err)
		}
		if req.Request.Subtype != "get_settings" {
			t.Fatalf("request subtype = %q, want get_settings", req.Request.Subtype)
		}
		return req, raw
	case <-time.After(3 * time.Second):
		t.Fatal("applied settings request was not written")
		return controlRequest{}, nil
	}
}

func awaitAppliedSettingsOutcome(t *testing.T, done <-chan appliedSettingsQueryOutcome) appliedSettingsQueryOutcome {
	t.Helper()
	select {
	case got := <-done:
		return got
	case <-time.After(3 * time.Second):
		t.Fatal("applied settings query did not complete")
		return appliedSettingsQueryOutcome{}
	}
}

// appliedSettingsQueryResponse builds the complete top-level line. A nil inner omits
// response.response; an empty map retains the object while omitting applied.
func appliedSettingsQueryResponse(t *testing.T, requestID, subtype string, inner map[string]any) []byte {
	t.Helper()
	response := map[string]any{
		"subtype":    subtype,
		"request_id": requestID,
	}
	if inner != nil {
		response["response"] = inner
	}
	raw, err := json.Marshal(map[string]any{
		"type":     "control_response",
		"response": response,
	})
	if err != nil {
		t.Fatalf("marshal applied settings response: %v", err)
	}
	return append(raw, '\n')
}

func appliedSettingsInner(model string, effort any) map[string]any {
	return map[string]any{
		"applied": map[string]any{
			"model":     model,
			"effort":    effort,
			"advisor":   "private-advisor-sentinel",
			"ultracode": true,
		},
		"effective": map[string]any{
			"environment": "private-environment-sentinel",
			"cwd":         "/private/path/sentinel",
		},
		"sources":     []string{"private-source-sentinel"},
		"credentials": "private-credential-sentinel",
	}
}

func assertNoAppliedSettingsSinkEvent(t *testing.T, sink <-chan turnevent.Event) {
	t.Helper()
	select {
	case ev := <-sink:
		t.Fatalf("claimed applied settings reached shared sink as %T: %+v", ev, ev)
	default:
	}
}

func TestRunner_QueryAppliedSettings_ExactEnvelopeAndThreeOutcomes(t *testing.T) {
	tests := []struct {
		name       string
		effort     any
		wantEffort *string
	}{
		{name: "string effort", effort: "future-level", wantEffort: ptrString("future-level")},
		{name: "empty string effort", effort: "", wantEffort: ptrString("")},
		{name: "explicit null effort", effort: nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			sink := make(chan turnevent.Event, 4)
			p := NewParser(
				func(ev turnevent.Event) { sink <- ev },
				slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
			)
			w := newMCPStatusQueryWriter()
			r := appliedSettingsQueryRunner(t, p, w)

			done := queryAppliedSettingsAsync(appliedSettingsQueryContext(t), r)
			req, rawRequest := awaitAppliedSettingsRequest(t, w)
			if !strings.HasPrefix(req.RequestID, appliedSettingsQueryIDPrefix) {
				t.Fatalf("request_id = %q, want prefix %q", req.RequestID, appliedSettingsQueryIDPrefix)
			}
			wantRequest := []byte(`{"type":"control_request","request_id":"` + req.RequestID + `","request":{"subtype":"get_settings"}}` + "\n")
			if !bytes.Equal(rawRequest, wantRequest) {
				t.Fatalf("request = %q, want exact get_settings envelope %q", rawRequest, wantRequest)
			}

			line := appliedSettingsQueryResponse(t, req.RequestID, controlResponseSuccess,
				appliedSettingsInner("claude-opus-live", tc.effort))
			if _, err := p.Write(line); err != nil {
				t.Fatalf("Parser.Write: %v", err)
			}
			// Destroy the caller-owned response buffer after parsing. The returned model
			// and optional effort must own their bytes independently.
			for i := range line {
				line[i] = 'x'
			}

			got := awaitAppliedSettingsOutcome(t, done)
			if !got.ok || got.settings.Model != "claude-opus-live" {
				t.Fatalf("QueryAppliedSettings = (%+v,%v), want exact successful model", got.settings, got.ok)
			}
			switch {
			case tc.wantEffort == nil && got.settings.Effort != nil:
				t.Fatalf("Effort = %q, want explicit null", *got.settings.Effort)
			case tc.wantEffort != nil && (got.settings.Effort == nil || *got.settings.Effort != *tc.wantEffort):
				t.Fatalf("Effort = %v, want %q", got.settings.Effort, *tc.wantEffort)
			}
			assertNoAppliedSettingsSinkEvent(t, sink)
			for _, forbidden := range []string{
				controlResponseMsg,
				"private-advisor-sentinel",
				"private-environment-sentinel",
				"/private/path/sentinel",
				"private-source-sentinel",
				"private-credential-sentinel",
			} {
				if strings.Contains(logs.String(), forbidden) {
					t.Fatalf("claimed settings leaked %q to logs: %s", forbidden, logs.String())
				}
			}
		})
	}
}

func ptrString(v string) *string { return &v }

func TestRunner_QueryAppliedSettings_OverlappingQueriesCorrelateIndependently(t *testing.T) {
	p := NewParser(func(turnevent.Event) {}, discardLogger())
	w := newMCPStatusQueryWriter()
	r := appliedSettingsQueryRunner(t, p, w)

	firstDone := queryAppliedSettingsAsync(appliedSettingsQueryContext(t), r)
	firstReq, _ := awaitAppliedSettingsRequest(t, w)
	secondDone := queryAppliedSettingsAsync(appliedSettingsQueryContext(t), r)
	secondReq, _ := awaitAppliedSettingsRequest(t, w)
	if firstReq.RequestID == secondReq.RequestID {
		t.Fatalf("overlapping queries reused request id %q", firstReq.RequestID)
	}

	_, _ = p.Write(appliedSettingsQueryResponse(t, secondReq.RequestID, controlResponseSuccess,
		appliedSettingsInner("second-model", "second-effort")))
	second := awaitAppliedSettingsOutcome(t, secondDone)
	if !second.ok || second.settings.Model != "second-model" || second.settings.Effort == nil || *second.settings.Effort != "second-effort" {
		t.Fatalf("second query result = %+v, want second response", second)
	}
	select {
	case got := <-firstDone:
		t.Fatalf("first query completed from second id: %+v", got)
	default:
	}

	_, _ = p.Write(appliedSettingsQueryResponse(t, firstReq.RequestID, controlResponseSuccess,
		appliedSettingsInner("first-model", nil)))
	first := awaitAppliedSettingsOutcome(t, firstDone)
	if !first.ok || first.settings.Model != "first-model" || first.settings.Effort != nil {
		t.Fatalf("first query result = %+v, want first response with null effort", first)
	}
}

func TestRunner_QueryAppliedSettings_UnknownAndLateIDsStayPrivate(t *testing.T) {
	var logs bytes.Buffer
	sink := make(chan turnevent.Event, 4)
	p := NewParser(
		func(ev turnevent.Event) { sink <- ev },
		slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
	)
	w := newMCPStatusQueryWriter()
	r := appliedSettingsQueryRunner(t, p, w)

	done := queryAppliedSettingsAsync(appliedSettingsQueryContext(t), r)
	req, _ := awaitAppliedSettingsRequest(t, w)
	_, _ = p.Write(appliedSettingsQueryResponse(t, appliedSettingsQueryIDPrefix+"9999", controlResponseSuccess,
		appliedSettingsInner("forged-model", "forged-effort")))
	select {
	case got := <-done:
		t.Fatalf("unknown id completed query: %+v", got)
	default:
	}
	assertNoAppliedSettingsSinkEvent(t, sink)

	_, _ = p.Write(appliedSettingsQueryResponse(t, req.RequestID, controlResponseSuccess,
		appliedSettingsInner("exact-model", "exact-effort")))
	got := awaitAppliedSettingsOutcome(t, done)
	if !got.ok || got.settings.Model != "exact-model" {
		t.Fatalf("exact response result = %+v", got)
	}
	_, _ = p.Write(appliedSettingsQueryResponse(t, req.RequestID, controlResponseSuccess,
		appliedSettingsInner("late-model", "late-effort")))
	assertNoAppliedSettingsSinkEvent(t, sink)
	if strings.Contains(logs.String(), "forged-model") || strings.Contains(logs.String(), "late-model") {
		t.Fatalf("private unknown/late response reached logs: %s", logs.String())
	}
}

func TestRunner_QueryAppliedSettings_MalformedAndOversizedFieldsAreUnavailable(t *testing.T) {
	valid := func() map[string]any { return appliedSettingsInner("model", "effort") }
	tests := []struct {
		name    string
		subtype string
		inner   func() map[string]any
	}{
		{name: "non-success", subtype: "error", inner: valid},
		{name: "missing response payload", subtype: controlResponseSuccess},
		{name: "missing applied", subtype: controlResponseSuccess, inner: func() map[string]any { return map[string]any{} }},
		{name: "null applied", subtype: controlResponseSuccess, inner: func() map[string]any { return map[string]any{"applied": nil} }},
		{name: "non-object applied", subtype: controlResponseSuccess, inner: func() map[string]any { return map[string]any{"applied": "wrong"} }},
		{name: "missing model", subtype: controlResponseSuccess, inner: func() map[string]any { return map[string]any{"applied": map[string]any{"effort": "high"}} }},
		{name: "null model", subtype: controlResponseSuccess, inner: func() map[string]any {
			return map[string]any{"applied": map[string]any{"model": nil, "effort": "high"}}
		}},
		{name: "non-string model", subtype: controlResponseSuccess, inner: func() map[string]any { return map[string]any{"applied": map[string]any{"model": 7, "effort": "high"}} }},
		{name: "empty model", subtype: controlResponseSuccess, inner: func() map[string]any { return map[string]any{"applied": map[string]any{"model": "", "effort": "high"}} }},
		{name: "oversized model", subtype: controlResponseSuccess, inner: func() map[string]any {
			return map[string]any{"applied": map[string]any{"model": strings.Repeat("m", maxModelResolved+1), "effort": "high"}}
		}},
		{name: "missing effort", subtype: controlResponseSuccess, inner: func() map[string]any { return map[string]any{"applied": map[string]any{"model": "model"}} }},
		{name: "boolean effort", subtype: controlResponseSuccess, inner: func() map[string]any {
			return map[string]any{"applied": map[string]any{"model": "model", "effort": true}}
		}},
		{name: "numeric effort", subtype: controlResponseSuccess, inner: func() map[string]any { return map[string]any{"applied": map[string]any{"model": "model", "effort": 2}} }},
		{name: "oversized effort", subtype: controlResponseSuccess, inner: func() map[string]any {
			return map[string]any{"applied": map[string]any{"model": "model", "effort": strings.Repeat("e", maxModelEffortLevel+1)}}
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := NewParser(func(turnevent.Event) {}, discardLogger())
			w := newMCPStatusQueryWriter()
			r := appliedSettingsQueryRunner(t, p, w)
			done := queryAppliedSettingsAsync(appliedSettingsQueryContext(t), r)
			req, _ := awaitAppliedSettingsRequest(t, w)
			var inner map[string]any
			if tc.inner != nil {
				inner = tc.inner()
			}
			_, _ = p.Write(appliedSettingsQueryResponse(t, req.RequestID, tc.subtype, inner))
			if got := awaitAppliedSettingsOutcome(t, done); got.ok {
				t.Fatalf("unusable response returned success: %+v", got)
			}
			// Matching is terminal even for an unusable payload.
			_, _ = p.Write(appliedSettingsQueryResponse(t, req.RequestID, controlResponseSuccess, valid()))
		})
	}
}

func TestRunner_QueryAppliedSettings_UnserviceableAsksWriteNothing(t *testing.T) {
	tests := []struct {
		name   string
		parser bool
		writer bool
		ctx    func(t *testing.T) context.Context
		arm    func(*Runner)
	}{
		{name: "no parser", writer: true, ctx: appliedSettingsQueryContext},
		{name: "no live child", parser: true, ctx: appliedSettingsQueryContext},
		{name: "no caller deadline", parser: true, writer: true, ctx: func(*testing.T) context.Context { return context.Background() }},
		{name: "rotation in flight", parser: true, writer: true, ctx: appliedSettingsQueryContext, arm: func(r *Runner) {
			r.mu.Lock()
			r.rotating = true
			r.mu.Unlock()
		}},
		{name: "context already ended", parser: true, writer: true, ctx: func(t *testing.T) context.Context {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			cancel()
			return ctx
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var p *Parser
			if tc.parser {
				p = NewParser(func(turnevent.Event) {}, discardLogger())
			}
			w := newMCPStatusQueryWriter()
			var target *mcpStatusQueryWriteCloser
			if tc.writer {
				target = w
			}
			r := appliedSettingsQueryRunner(t, p, target)
			if tc.arm != nil {
				tc.arm(r)
			}
			if settings, ok := r.QueryAppliedSettings(tc.ctx(t)); ok {
				t.Fatalf("QueryAppliedSettings = (%+v,true), want unavailable", settings)
			}
			if got := w.count(); got != 0 {
				t.Fatalf("writes = %d, want 0", got)
			}
		})
	}
}

func TestRunner_QueryAppliedSettings_CancellationAndDeadlineRetireCorrelation(t *testing.T) {
	tests := []struct {
		name string
		ctx  func() (context.Context, context.CancelFunc)
	}{
		{name: "cancellation", ctx: func() (context.Context, context.CancelFunc) {
			base, stop := context.WithTimeout(context.Background(), 3*time.Second)
			ctx, cancel := context.WithCancel(base)
			return ctx, func() { cancel(); stop() }
		}},
		{name: "deadline", ctx: func() (context.Context, context.CancelFunc) {
			return context.WithTimeout(context.Background(), 20*time.Millisecond)
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sink := make(chan turnevent.Event, 4)
			p := NewParser(func(ev turnevent.Event) { sink <- ev }, discardLogger())
			w := newMCPStatusQueryWriter()
			r := appliedSettingsQueryRunner(t, p, w)
			ctx, cancel := tc.ctx()
			done := queryAppliedSettingsAsync(ctx, r)
			req, _ := awaitAppliedSettingsRequest(t, w)
			if tc.name == "cancellation" {
				cancel()
			} else {
				defer cancel()
			}
			if got := awaitAppliedSettingsOutcome(t, done); got.ok {
				t.Fatalf("ended query returned success: %+v", got)
			}
			_, _ = p.Write(appliedSettingsQueryResponse(t, req.RequestID, controlResponseSuccess,
				appliedSettingsInner("late-model", "late-effort")))
			assertNoAppliedSettingsSinkEvent(t, sink)
		})
	}
}

func TestRunner_QueryAppliedSettings_WriteFailureWinsOverEarlyResponse(t *testing.T) {
	sink := make(chan turnevent.Event, 4)
	p := NewParser(func(ev turnevent.Event) { sink <- ev }, discardLogger())
	w := newMCPStatusQueryWriter()
	writeErr := errors.New("applied settings write failure sentinel")
	parseDone := make(chan struct{})
	w.write = func(raw []byte) (int, error) {
		var req controlRequest
		if err := json.Unmarshal(bytes.TrimSpace(raw), &req); err != nil {
			return 0, err
		}
		go func() {
			_, _ = p.Write(appliedSettingsQueryResponse(t, req.RequestID, controlResponseSuccess,
				appliedSettingsInner("early-model", "early-effort")))
			close(parseDone)
		}()
		deadline := time.Now().Add(3 * time.Second)
		for {
			p.appliedSettingsQueries.mu.Lock()
			_, stillPending := p.appliedSettingsQueries.pending[req.RequestID]
			p.appliedSettingsQueries.mu.Unlock()
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
	r := appliedSettingsQueryRunner(t, p, w)
	if got, ok := r.QueryAppliedSettings(appliedSettingsQueryContext(t)); ok {
		t.Fatalf("failed write returned success: %+v", got)
	}
	select {
	case <-parseDone:
	case <-time.After(3 * time.Second):
		t.Fatal("early parser response did not finish after write failure")
	}
	assertNoAppliedSettingsSinkEvent(t, sink)
}

func TestRunner_QueryAppliedSettings_ChildBoundariesFailPendingQuery(t *testing.T) {
	tests := []struct {
		name   string
		retire func(*Runner, *Parser)
	}{
		{name: "exit", retire: func(r *Runner, _ *Parser) {
			if old := r.takeStdin(); old != nil {
				_ = old.Close()
			}
		}},
		{name: "replacement", retire: func(_ *Runner, p *Parser) {
			p.beginMCPStatusChild(false, nil)
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sink := make(chan turnevent.Event, 4)
			p := NewParser(func(ev turnevent.Event) { sink <- ev }, discardLogger())
			w := newMCPStatusQueryWriter()
			r := appliedSettingsQueryRunner(t, p, w)
			done := queryAppliedSettingsAsync(appliedSettingsQueryContext(t), r)
			req, _ := awaitAppliedSettingsRequest(t, w)
			tc.retire(r, p)
			if got := awaitAppliedSettingsOutcome(t, done); got.ok {
				t.Fatalf("query survived child %s: %+v", tc.name, got)
			}
			_, _ = p.Write(appliedSettingsQueryResponse(t, req.RequestID, controlResponseSuccess,
				appliedSettingsInner("departed-model", "departed-effort")))
			assertNoAppliedSettingsSinkEvent(t, sink)
		})
	}
}
