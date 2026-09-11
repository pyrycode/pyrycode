package streamsup

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/pyrycode/pyrycode/internal/turnevent"
)

type contextUsageTestWriteCloser struct {
	write func([]byte) (int, error)
}

func (w contextUsageTestWriteCloser) Write(p []byte) (int, error) { return w.write(p) }
func (contextUsageTestWriteCloser) Close() error                  { return nil }

type capturedContextUsagePayload struct {
	Model       string `json:"model"`
	TotalTokens int    `json:"totalTokens"`
	MaxTokens   int    `json:"maxTokens"`
	Percentage  int    `json:"percentage"`
	Categories  []struct {
		Name   string `json:"name"`
		Tokens int    `json:"tokens"`
	} `json:"categories"`
}

func contextUsageTestRunner(t *testing.T, p *Parser, w io.WriteCloser) *Runner {
	t.Helper()
	cfg := helperRunCfg(t, "echo_lines", &safeBuffer{}, &safeBuffer{})
	cfg.Stdout = p
	r, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	r.mu.Lock()
	r.stdin = w
	r.mu.Unlock()
	t.Cleanup(func() {
		r.mu.Lock()
		r.stdin = nil
		r.mu.Unlock()
	})
	return r
}

func requestContextUsageID(t *testing.T, r *Runner, detail string, dst *bytes.Buffer) string {
	t.Helper()
	if err := r.RequestContextUsage(detail); err != nil {
		t.Fatalf("RequestContextUsage(%q): %v", detail, err)
	}
	var request controlRequest
	if err := json.Unmarshal(bytes.TrimSpace(dst.Bytes()), &request); err != nil {
		t.Fatalf("decoding written request: %v", err)
	}
	if request.RequestID == "" {
		t.Fatal("written request carries an empty request_id")
	}
	return request.RequestID
}

func capturedContextUsageResponse(t *testing.T, raw json.RawMessage, requestID string) ([]byte, capturedContextUsagePayload) {
	t.Helper()
	var line struct {
		Type     string `json:"type"`
		Response struct {
			Subtype   string                      `json:"subtype"`
			RequestID string                      `json:"request_id"`
			Response  capturedContextUsagePayload `json:"response"`
		} `json:"response"`
	}
	if err := json.Unmarshal(raw, &line); err != nil {
		t.Fatalf("decoding captured response: %v", err)
	}
	line.Response.RequestID = requestID
	encoded, err := json.Marshal(line)
	if err != nil {
		t.Fatalf("rewriting captured request id: %v", err)
	}
	return encoded, line.Response.Response
}

func TestParser_ContextUsageCaptureReplay(t *testing.T) {
	capture := contextUsageRead(t, "2357")
	for _, arm := range capture.Arms {
		arm := arm
		t.Run(arm.Arm, func(t *testing.T) {
			if len(arm.ControlResponses) != 1 {
				t.Fatalf("captured arm has %d responses, want exactly 1", len(arm.ControlResponses))
			}
			var events []turnevent.Event
			p := NewParser(func(ev turnevent.Event) { events = append(events, ev) }, discardLogger())
			var request bytes.Buffer
			r := contextUsageTestRunner(t, p, contextUsageTestWriteCloser{write: request.Write})
			id := requestContextUsageID(t, r, arm.Detail, &request)
			line, expected := capturedContextUsageResponse(t, arm.ControlResponses[0], id)

			if _, err := p.Write(append(line, '\n')); err != nil {
				t.Fatalf("Parser.Write: %v", err)
			}
			if len(events) != 1 {
				t.Fatalf("event count = %d, want 1: %#v", len(events), events)
			}
			got, ok := events[0].(turnevent.ContextUsage)
			if !ok {
				t.Fatalf("event = %T, want turnevent.ContextUsage", events[0])
			}
			if got.Model != expected.Model || got.TotalTokens != expected.TotalTokens ||
				got.MaxTokens != expected.MaxTokens || got.Percentage != expected.Percentage {
				t.Errorf("totals = %+v, want model=%q total=%d max=%d percentage=%d",
					got, expected.Model, expected.TotalTokens, expected.MaxTokens, expected.Percentage)
			}
			sort.SliceStable(expected.Categories, func(i, j int) bool {
				return expected.Categories[i].Tokens > expected.Categories[j].Tokens
			})
			wantCategories := make([]turnevent.ContextUsageCategory, 0, len(expected.Categories))
			for _, category := range expected.Categories {
				wantCategories = append(wantCategories, turnevent.ContextUsageCategory{
					Name: category.Name, Tokens: category.Tokens,
				})
			}
			if !reflect.DeepEqual(got.Categories, wantCategories) {
				t.Errorf("categories = %+v, want %+v", got.Categories, wantCategories)
			}
			if got.DroppedCategories != 0 {
				t.Errorf("DroppedCategories = %d, want 0", got.DroppedCategories)
			}
		})
	}
}

func contextUsageResponseFixture(t *testing.T, requestID, subtype, model string, categories any) []byte {
	t.Helper()
	line := map[string]any{
		"type": "control_response",
		"response": map[string]any{
			"subtype": subtype, "request_id": requestID,
			"response": map[string]any{
				"model": model, "totalTokens": 101, "maxTokens": 202,
				"percentage": 50, "categories": categories,
			},
		},
	}
	b, err := json.Marshal(line)
	if err != nil {
		t.Fatalf("marshalling context usage fixture: %v", err)
	}
	return b
}

func TestParser_ContextUsageCategoriesAreBounded(t *testing.T) {
	t.Parallel()
	const overlongPrefix = "overlong-category-sentinel-2357"
	categories := make([]map[string]any, 0, 36)
	categories = append(categories,
		map[string]any{"name": overlongPrefix + strings.Repeat("x", 257), "tokens": 10_000},
		map[string]any{"name": strings.Repeat("y", 257), "tokens": 9_000},
	)
	for i := range 34 {
		categories = append(categories, map[string]any{
			"name": fmt.Sprintf("valid-%02d", i), "tokens": i,
		})
	}

	var events []turnevent.Event
	p := NewParser(func(ev turnevent.Event) { events = append(events, ev) }, discardLogger())
	var request bytes.Buffer
	r := contextUsageTestRunner(t, p, contextUsageTestWriteCloser{write: request.Write})
	id := requestContextUsageID(t, r, "full", &request)
	line := contextUsageResponseFixture(t, id, "success", "bounded-model", categories)
	_, _ = p.Write(append(line, '\n'))

	if len(events) != 1 {
		t.Fatalf("event count = %d, want 1: %#v", len(events), events)
	}
	got := events[0].(turnevent.ContextUsage)
	if len(got.Categories) != 32 || got.DroppedCategories != 4 {
		t.Fatalf("categories/dropped = %d/%d, want 32/4", len(got.Categories), got.DroppedCategories)
	}
	for i, category := range got.Categories {
		wantName := fmt.Sprintf("valid-%02d", 33-i)
		wantTokens := 33 - i
		if category.Name != wantName || category.Tokens != wantTokens {
			t.Errorf("category[%d] = %+v, want name=%q tokens=%d", i, category, wantName, wantTokens)
		}
		if strings.Contains(category.Name, overlongPrefix) {
			t.Errorf("overlong category retained at %d: %+v", i, category)
		}
	}
}

func TestParser_ContextUsageRequiresOneSuccessfulPendingRequest(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		first      func(string) []byte
		duplicate  bool
		wantEvents int
	}{
		{
			name: "unknown id",
			first: func(string) []byte {
				return contextUsageResponseFixture(t, "unknown-2357", "success", "model", []any{})
			},
		},
		{
			name: "duplicate response",
			first: func(id string) []byte {
				return contextUsageResponseFixture(t, id, "success", "model", []any{})
			},
			duplicate: true, wantEvents: 1,
		},
		{
			name: "invalid first response retires id",
			first: func(id string) []byte {
				return contextUsageResponseFixture(t, id, "success", "invalid-model-2357", 42)
			},
			duplicate: true,
		},
		{
			name: "error first response retires id",
			first: func(id string) []byte {
				return contextUsageResponseFixture(t, id, "error", "error-model-2357", []any{})
			},
			duplicate: true,
		},
		{
			name: "overlong model is invalid and retires id",
			first: func(id string) []byte {
				return contextUsageResponseFixture(t, id, "success", strings.Repeat("m", 257), []any{})
			},
			duplicate: true,
		},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var events []turnevent.Event
			p := NewParser(func(ev turnevent.Event) { events = append(events, ev) }, discardLogger())
			var request bytes.Buffer
			r := contextUsageTestRunner(t, p, contextUsageTestWriteCloser{write: request.Write})
			id := requestContextUsageID(t, r, "summary", &request)
			first := tc.first(id)
			_, _ = p.Write(append(first, '\n'))
			if tc.duplicate {
				valid := contextUsageResponseFixture(t, id, "success", "duplicate-model-2357", []any{})
				_, _ = p.Write(append(valid, '\n'))
			}
			if len(events) != tc.wantEvents {
				t.Errorf("event count = %d, want %d: %#v", len(events), tc.wantEvents, events)
			}
		})
	}
}

func TestRunner_ContextUsageWriteFailureCannotEmit(t *testing.T) {
	t.Parallel()
	var events []turnevent.Event
	p := NewParser(func(ev turnevent.Event) { events = append(events, ev) }, discardLogger())
	var attempted bytes.Buffer
	writeErr := errors.New("context usage write failed sentinel 2357")
	w := contextUsageTestWriteCloser{write: func(b []byte) (int, error) {
		_, _ = attempted.Write(b)
		return 0, writeErr
	}}
	r := contextUsageTestRunner(t, p, w)
	if err := r.RequestContextUsage("summary"); !errors.Is(err, writeErr) {
		t.Fatalf("RequestContextUsage error = %v, want %v", err, writeErr)
	}
	var request controlRequest
	if err := json.Unmarshal(bytes.TrimSpace(attempted.Bytes()), &request); err != nil {
		t.Fatalf("decoding attempted request: %v", err)
	}
	line := contextUsageResponseFixture(t, request.RequestID, "success", "failed-write-model-2357", []any{})
	_, _ = p.Write(append(line, '\n'))
	if len(events) != 0 {
		t.Errorf("response for failed write emitted %#v, want nothing", events)
	}
}

func TestRunner_ContextUsageRegistersBeforeWrite(t *testing.T) {
	t.Parallel()
	p := NewParser(func(turnevent.Event) {}, discardLogger())
	registeredDuringWrite := false
	w := contextUsageTestWriteCloser{write: func(b []byte) (int, error) {
		var request controlRequest
		if err := json.Unmarshal(bytes.TrimSpace(b), &request); err != nil {
			return 0, err
		}
		p.contextUsageRequests.mu.Lock()
		registeredDuringWrite = p.contextUsageRequests.pending[request.RequestID] != nil
		p.contextUsageRequests.mu.Unlock()
		return len(b), nil
	}}
	r := contextUsageTestRunner(t, p, w)
	if err := r.RequestContextUsage("summary"); err != nil {
		t.Fatalf("RequestContextUsage: %v", err)
	}
	if !registeredDuringWrite {
		t.Fatal("request id was not pending when the request writer was entered")
	}
}

func TestParser_ContextUsageLogsOnlyExistingContentFreeRecord(t *testing.T) {
	t.Parallel()
	const (
		requestSentinel  = "context-request-sentinel-2357"
		modelSentinel    = "context-model-sentinel-2357"
		categorySentinel = "context-category-sentinel-2357"
		decoderSentinel  = "context-decoder-sentinel-2357"
		errorSentinel    = "context-error-sentinel-2357"
	)
	rec := &logRecorder{}
	p := NewParser(func(turnevent.Event) {}, slog.New(rec))
	p.registerContextUsageRequest(requestSentinel).resolve(true)
	p.registerContextUsageRequest("context-error-id-2357").resolve(true)
	p.registerContextUsageRequest("context-invalid-id-2357").resolve(true)
	lines := [][]byte{
		contextUsageResponseFixture(t, requestSentinel, "success", modelSentinel,
			[]any{map[string]any{"name": categorySentinel, "tokens": 1}}),
		contextUsageResponseFixture(t, requestSentinel, "success", "duplicate-model", []any{}),
		[]byte(`{"type":"control_response","response":{"subtype":"error","request_id":"context-error-id-2357","error":"` + errorSentinel + `"}}`),
		[]byte(`{"type":"control_response","response":{"subtype":"success","request_id":"context-invalid-id-2357","response":{"categories":"` + decoderSentinel + `"}}}`),
		contextUsageResponseFixture(t, "failed-write-id-2357", "success", "failed-write-model", []any{}),
	}
	for _, line := range lines {
		_, _ = p.Write(append(line, '\n'))
	}

	all := rec.all()
	if len(all) != len(lines) {
		t.Fatalf("record count = %d, want one per control_response (%d): %+v", len(all), len(lines), all)
	}
	for i, record := range all {
		if record.msg != controlResponseConsumeMsgFixture {
			t.Errorf("record[%d] message = %q, want %q", i, record.msg, controlResponseConsumeMsgFixture)
		}
		for _, secret := range []string{requestSentinel, modelSentinel, categorySentinel, decoderSentinel,
			errorSentinel, "context-error-id-2357", "context-invalid-id-2357", "failed-write-id-2357", `"response"`} {
			if strings.Contains(record.msg, secret) {
				t.Errorf("record[%d] message leaks %q: %q", i, secret, record.msg)
			}
			for key, value := range record.attrs {
				if strings.Contains(value, secret) {
					t.Errorf("record[%d] attr %q leaks %q: %q", i, key, secret, value)
				}
			}
		}
	}
}
