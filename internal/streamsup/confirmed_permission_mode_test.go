package streamsup

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/turnevent"
)

const permissionModePlanForTest = "plan"

func permissionModeTestRunner(t *testing.T, p *Parser, w *mcpStatusQueryWriteCloser) *Runner {
	t.Helper()
	r := mcpStatusQueryRunner(t, p, w, false)
	if p != nil {
		p.beginMCPStatusChild(false, nil)
	}
	return r
}

func awaitPermissionModeRequest(t *testing.T, w *mcpStatusQueryWriteCloser) controlRequest {
	t.Helper()
	select {
	case raw := <-w.wrote:
		var req controlRequest
		if err := json.Unmarshal(bytes.TrimSpace(raw), &req); err != nil {
			t.Fatalf("decode permission-mode request: %v", err)
		}
		if req.Request.Subtype != "set_permission_mode" {
			t.Fatalf("request subtype = %q, want set_permission_mode", req.Request.Subtype)
		}
		return req
	case <-time.After(3 * time.Second):
		t.Fatal("permission-mode request was not written")
		return controlRequest{}
	}
}

func writePermissionModeLine(t *testing.T, p *Parser, line []byte) {
	t.Helper()
	if len(line) == 0 || line[len(line)-1] != '\n' {
		line = append(bytes.Clone(line), '\n')
	}
	if _, err := p.Write(line); err != nil {
		t.Fatalf("Parser.Write: %v", err)
	}
}

func permissionModeResponse(t *testing.T, requestID, subtype string, response any) []byte {
	t.Helper()
	wrapper := map[string]any{
		"subtype":    subtype,
		"request_id": requestID,
	}
	if response != nil {
		wrapper["response"] = response
	}
	raw, err := json.Marshal(map[string]any{
		"type":     "control_response",
		"response": wrapper,
	})
	if err != nil {
		t.Fatalf("marshal permission-mode response: %v", err)
	}
	return append(raw, '\n')
}

func assertConfirmedPermissionMode(t *testing.T, r *Runner, want string, wantOK bool) {
	t.Helper()
	got, ok := r.ConfirmedPermissionMode()
	if got != want || ok != wantOK {
		t.Fatalf("ConfirmedPermissionMode() = (%q, %v), want (%q, %v)", got, ok, want, wantOK)
	}
}

func TestRunner_ConfirmedPermissionMode_InitIsBoundedAndInformational(t *testing.T) {
	var events []turnevent.Event
	p := NewParser(func(ev turnevent.Event) { events = append(events, ev) }, discardLogger())
	w := newMCPStatusQueryWriter()
	r := permissionModeTestRunner(t, p, w)
	r.spawnMode = permissionModeDefault
	r.cfg.OperatorBypass = true

	assertConfirmedPermissionMode(t, r, "", false)
	if !p.PostureGate().ready() {
		t.Fatal("reading an unavailable mode changed the posture gate")
	}
	if got := w.count(); got != 0 {
		t.Fatalf("reading an unavailable mode wrote %d control request(s), want 0", got)
	}

	rawMode := strings.Repeat("m", maxPermissionModeField+17)
	line := fmt.Sprintf(`{"type":"system","subtype":"init","claude_code_version":"2.1.test","permissionMode":%q}`, rawMode)
	writePermissionModeLine(t, p, []byte(line))

	wantMode := strings.Repeat("m", maxPermissionModeField)
	assertConfirmedPermissionMode(t, r, wantMode, true)
	if got := w.count(); got != 0 {
		t.Fatalf("reading a confirmed mode wrote %d control request(s), want 0", got)
	}
	if !p.PostureGate().ready() {
		t.Fatal("retaining an init mode changed the posture gate")
	}
	if len(events) != 1 {
		t.Fatalf("init emitted %d events, want one SessionFacts: %#v", len(events), events)
	}
	facts, ok := events[0].(turnevent.SessionFacts)
	if !ok {
		t.Fatalf("init emitted no SessionFacts: %#v", events)
	}
	if facts.PermissionMode != wantMode {
		t.Fatalf("SessionFacts.PermissionMode = %q, want bounded %q", facts.PermissionMode, wantMode)
	}

	writePermissionModeLine(t, p, []byte(`{"type":"system","subtype":"init","claude_code_version":"2.1.next"}`))
	assertConfirmedPermissionMode(t, r, wantMode, true)
}

func TestRunner_ConfirmedPermissionMode_WithoutParserIsUnavailable(t *testing.T) {
	r := permissionModeTestRunner(t, nil, newMCPStatusQueryWriter())
	assertConfirmedPermissionMode(t, r, "", false)
}

func TestRunner_ConfirmedPermissionMode_ConcurrentReadAndParserUpdate(t *testing.T) {
	p := NewParser(func(turnevent.Event) {}, discardLogger())
	r := permissionModeTestRunner(t, p, newMCPStatusQueryWriter())
	done := make(chan struct{})
	go func() {
		for range 10_000 {
			_, _ = r.ConfirmedPermissionMode()
		}
		close(done)
	}()
	for range 1_000 {
		writePermissionModeLine(t, p, []byte(`{"type":"system","subtype":"init","permissionMode":"concurrent-mode"}`))
	}
	<-done
	assertConfirmedPermissionMode(t, r, "concurrent-mode", true)
}

type permissionModeCaptureRecord struct {
	ClaudeVersion                   string                        `json:"claude_version"`
	Arm                             string                        `json:"arm"`
	RequestedMode                   string                        `json:"requested_mode"`
	ControlRequestID                string                        `json:"control_request_id"`
	ControlRequestSent              controlRequest                `json:"control_request_sent"`
	ControlResponses                []json.RawMessage             `json:"control_responses"`
	ControlResponseRequestIDMatched bool                          `json:"control_response_request_id_matched"`
	SecondControlRequest            *permissionModeCaptureRequest `json:"second_control_request"`
}

type permissionModeCaptureRequest struct {
	RequestedMode            string         `json:"requested_mode"`
	ControlRequestID         string         `json:"control_request_id"`
	ControlRequestSent       controlRequest `json:"control_request_sent"`
	ControlResponseIDMatched bool           `json:"control_response_id_matched"`
}

type capturedPermissionModeAck struct {
	Type     string `json:"type"`
	Response struct {
		Subtype   string `json:"subtype"`
		RequestID string `json:"request_id"`
		Response  struct {
			Mode string `json:"mode"`
		} `json:"response"`
	} `json:"response"`
}

type capturedPermissionModeCase struct {
	name string
	mode string
	ack  capturedPermissionModeAck
}

func capturedPermissionModeCases(t *testing.T) []capturedPermissionModeCase {
	t.Helper()
	fixtures := []struct {
		name          string
		version       string
		arm           string
		responseCount int
	}{
		{"set_permission_mode_v2.1.220_revoke.json", "2.1.220", "revoke", 1},
		{"bypass_reescalation_v2.1.239_reescalate.json", "2.1.239", "reescalate", 2},
	}
	var out []capturedPermissionModeCase
	for _, fixture := range fixtures {
		path := filepath.Join("../e2e/realclaude/testdata", fixture.name)
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read committed permission-mode capture %s: %v", path, err)
		}
		var rec permissionModeCaptureRecord
		if err := json.Unmarshal(raw, &rec); err != nil {
			t.Fatalf("decode committed permission-mode capture %s: %v", fixture.name, err)
		}
		if rec.ClaudeVersion != fixture.version || rec.Arm != fixture.arm {
			t.Fatalf("%s identifies version/arm (%q, %q), want (%q, %q)",
				fixture.name, rec.ClaudeVersion, rec.Arm, fixture.version, fixture.arm)
		}
		if len(rec.ControlResponses) != fixture.responseCount {
			t.Fatalf("%s has %d control responses, want %d", fixture.name, len(rec.ControlResponses), fixture.responseCount)
		}
		requests := []permissionModeCaptureRequest{{
			RequestedMode:            rec.RequestedMode,
			ControlRequestID:         rec.ControlRequestID,
			ControlRequestSent:       rec.ControlRequestSent,
			ControlResponseIDMatched: rec.ControlResponseRequestIDMatched,
		}}
		if rec.SecondControlRequest != nil {
			requests = append(requests, *rec.SecondControlRequest)
		}
		if len(requests) != len(rec.ControlResponses) {
			t.Fatalf("%s has %d requests for %d responses", fixture.name, len(requests), len(rec.ControlResponses))
		}
		for i, request := range requests {
			if !request.ControlResponseIDMatched || request.ControlRequestID == "" || request.RequestedMode == "" {
				t.Fatalf("%s request %d lacks correlated id/mode evidence: %+v", fixture.name, i, request)
			}
			if request.ControlRequestSent.RequestID != request.ControlRequestID ||
				request.ControlRequestSent.Request.Subtype != "set_permission_mode" ||
				request.ControlRequestSent.Request.Mode != request.RequestedMode {
				t.Fatalf("%s request %d metadata disagrees with captured request: %+v", fixture.name, i, request)
			}
			var ack capturedPermissionModeAck
			if err := json.Unmarshal(rec.ControlResponses[i], &ack); err != nil {
				t.Fatalf("%s response %d decode: %v", fixture.name, i, err)
			}
			if ack.Type != "control_response" || ack.Response.Subtype != controlResponseSuccess ||
				ack.Response.RequestID != request.ControlRequestID || ack.Response.Response.Mode != request.RequestedMode {
				t.Fatalf("%s response %d does not exactly echo its successful request: %+v", fixture.name, i, ack)
			}
			out = append(out, capturedPermissionModeCase{
				name: fmt.Sprintf("%s/response-%d", fixture.name, i+1),
				mode: request.RequestedMode,
				ack:  ack,
			})
		}
	}
	return out
}

func TestRunner_SetPermissionMode_CapturedSuccessEchoConfirms(t *testing.T) {
	for _, tc := range capturedPermissionModeCases(t) {
		t.Run(tc.name, func(t *testing.T) {
			p := NewParser(func(turnevent.Event) {}, discardLogger())
			w := newMCPStatusQueryWriter()
			r := permissionModeTestRunner(t, p, w)
			writePermissionModeLine(t, p, []byte(`{"type":"system","subtype":"init","permissionMode":"prior-mode"}`))

			if err := r.SetPermissionMode(tc.mode); err != nil {
				t.Fatalf("SetPermissionMode: %v", err)
			}
			req := awaitPermissionModeRequest(t, w)
			if req.Request.Mode != tc.mode {
				t.Fatalf("request mode = %q, want captured %q", req.Request.Mode, tc.mode)
			}
			assertConfirmedPermissionMode(t, r, "prior-mode", true)

			ack := tc.ack
			ack.Response.RequestID = req.RequestID
			raw, err := json.Marshal(ack)
			if err != nil {
				t.Fatalf("marshal captured response with live request id: %v", err)
			}
			writePermissionModeLine(t, p, raw)
			assertConfirmedPermissionMode(t, r, tc.mode, true)
		})
	}
}

func TestRunner_SetPermissionMode_RejectsUnconfirmedResponses(t *testing.T) {
	tests := []struct {
		name string
		line func(string) []byte
	}{
		{"unknown id", func(string) []byte {
			return permissionModeResponse(t, "unknown", "success", map[string]any{"mode": "plan"})
		}},
		{"missing id", func(string) []byte {
			return []byte(`{"type":"control_response","response":{"subtype":"success","response":{"mode":"plan"}}}`)
		}},
		{"missing mode", func(id string) []byte { return permissionModeResponse(t, id, "success", map[string]any{}) }},
		{"mismatched mode", func(id string) []byte {
			return permissionModeResponse(t, id, "success", map[string]any{"mode": "default"})
		}},
		{"non-string mode", func(id string) []byte { return permissionModeResponse(t, id, "success", map[string]any{"mode": 7}) }},
		{"non-success", func(id string) []byte { return permissionModeResponse(t, id, "error", map[string]any{"mode": "plan"}) }},
		{"missing subtype", func(id string) []byte { return permissionModeResponse(t, id, "", map[string]any{"mode": "plan"}) }},
		{"malformed json", func(string) []byte { return []byte(`{"type":"control_response","response":`) }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := NewParser(func(turnevent.Event) {}, discardLogger())
			w := newMCPStatusQueryWriter()
			r := permissionModeTestRunner(t, p, w)
			writePermissionModeLine(t, p, []byte(`{"type":"system","subtype":"init","permissionMode":"prior-mode"}`))
			if err := r.SetPermissionMode(permissionModePlanForTest); err != nil {
				t.Fatalf("SetPermissionMode: %v", err)
			}
			req := awaitPermissionModeRequest(t, w)
			writePermissionModeLine(t, p, tc.line(req.RequestID))
			assertConfirmedPermissionMode(t, r, "prior-mode", true)
		})
	}
}

func TestRunner_SetPermissionMode_EarlyReplyLosesToFailedWrite(t *testing.T) {
	writeEntered := make(chan struct{})
	releaseWrite := make(chan struct{})
	w := newMCPStatusQueryWriter()
	w.write = func([]byte) (int, error) {
		close(writeEntered)
		<-releaseWrite
		return 0, errors.New("write-failure-sentinel")
	}
	p := NewParser(func(turnevent.Event) {}, discardLogger())
	r := permissionModeTestRunner(t, p, w)
	writePermissionModeLine(t, p, []byte(`{"type":"system","subtype":"init","permissionMode":"prior-mode"}`))

	writeDone := make(chan error, 1)
	go func() { writeDone <- r.SetPermissionMode(permissionModePlanForTest) }()
	select {
	case <-writeEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("SetPermissionMode did not enter the blocked writer")
	}
	req := awaitPermissionModeRequest(t, w)
	response := permissionModeResponse(t, req.RequestID, "success", map[string]any{"mode": permissionModePlanForTest})
	parseStarted := make(chan struct{})
	parseDone := make(chan struct{})
	go func() {
		close(parseStarted)
		_, _ = p.Write(response)
		close(parseDone)
	}()
	<-parseStarted
	select {
	case <-parseDone:
		t.Fatal("response completed before the blocked write published its verdict")
	case <-time.After(30 * time.Millisecond):
	}
	close(releaseWrite)
	if err := <-writeDone; err == nil || !strings.Contains(err.Error(), "write-failure-sentinel") {
		t.Fatalf("SetPermissionMode error = %v, want write failure", err)
	}
	select {
	case <-parseDone:
	case <-time.After(3 * time.Second):
		t.Fatal("response did not finish after the write verdict")
	}
	assertConfirmedPermissionMode(t, r, "prior-mode", true)
}

// A call that starts between children must not acquire a successor merely because
// that child binds while correlation registration is in flight. Holding the
// correlator lock makes the old register-before-snapshot ordering wait until the
// successor is installed; the correct snapshot-first ordering observes no child
// and refuses before touching the correlator.
func TestRunner_SetPermissionMode_BetweenChildrenDoesNotDriftToSuccessor(t *testing.T) {
	p := NewParser(func(turnevent.Event) {}, discardLogger())
	predecessor := newMCPStatusQueryWriter()
	r := permissionModeTestRunner(t, p, predecessor)
	if old := r.takeStdin(); old == nil {
		t.Fatal("takeStdin returned nil for installed predecessor")
	}

	p.permissionModes.mu.Lock()
	locked := true
	defer func() {
		if locked {
			p.permissionModes.mu.Unlock()
		}
	}()

	done := make(chan error, 1)
	go func() { done <- r.SetPermissionMode(permissionModePlanForTest) }()

	var callErr error
	finished := false
	select {
	case callErr = <-done:
		finished = true
		// Snapshot-first code can return while registration remains blocked.
	case <-time.After(3 * time.Second):
		// Register-first code is now blocked on the correlator lock below.
	}

	// Install the successor exactly as beginMCPStatusChild followed by setStdin
	// would, while retaining the barrier that distinguishes the two orderings.
	p.permissionModes.generation++
	p.permissionModes.active = true
	successor := newMCPStatusQueryWriter()
	r.setStdin(successor, 0, false)
	p.permissionModes.mu.Unlock()
	locked = false

	if !finished {
		select {
		case callErr = <-done:
		case <-time.After(3 * time.Second):
			t.Fatal("SetPermissionMode did not finish after successor setup")
		}
	}
	if !errors.Is(callErr, ErrNoLiveChild) {
		t.Fatalf("SetPermissionMode error = %v, want ErrNoLiveChild", callErr)
	}
	if got := successor.count(); got != 0 {
		t.Fatalf("successor writes = %d, want 0", got)
	}
	assertConfirmedPermissionMode(t, r, "", false)
}

func TestRunner_ConfirmedPermissionMode_ChildBoundaryClearsStateAndPending(t *testing.T) {
	p := NewParser(func(turnevent.Event) {}, discardLogger())
	w := newMCPStatusQueryWriter()
	r := permissionModeTestRunner(t, p, w)
	writePermissionModeLine(t, p, []byte(`{"type":"system","subtype":"init","permissionMode":"prior-mode"}`))
	if err := r.SetPermissionMode(permissionModePlanForTest); err != nil {
		t.Fatalf("SetPermissionMode: %v", err)
	}
	oldReq := awaitPermissionModeRequest(t, w)

	if old := r.takeStdin(); old == nil {
		t.Fatal("takeStdin returned nil for installed predecessor")
	}
	assertConfirmedPermissionMode(t, r, "", false)

	successorWriter := newMCPStatusQueryWriter()
	p.beginMCPStatusChild(false, nil)
	r.setStdin(successorWriter, 0, false)
	writePermissionModeLine(t, p, permissionModeResponse(t, oldReq.RequestID, "success", map[string]any{"mode": permissionModePlanForTest}))
	assertConfirmedPermissionMode(t, r, "", false)

	writePermissionModeLine(t, p, []byte(`{"type":"system","subtype":"init","permissionMode":"successor-mode"}`))
	assertConfirmedPermissionMode(t, r, "successor-mode", true)
}

func TestRunner_ConfirmedPermissionMode_LogsNoClaudeContent(t *testing.T) {
	const sentinel = "claude-authored-mode-sentinel-2511"
	recorder := &logRecorder{}
	p := NewParser(func(turnevent.Event) {}, slog.New(recorder))
	w := newMCPStatusQueryWriter()
	r := permissionModeTestRunner(t, p, w)
	writePermissionModeLine(t, p, []byte(`{"type":"system","subtype":"init","permissionMode":"prior-mode"}`))
	if err := r.SetPermissionMode(permissionModePlanForTest); err != nil {
		t.Fatalf("SetPermissionMode: %v", err)
	}
	req := awaitPermissionModeRequest(t, w)
	writePermissionModeLine(t, p, permissionModeResponse(t, req.RequestID, "success", map[string]any{"mode": sentinel}))
	assertConfirmedPermissionMode(t, r, "prior-mode", true)

	records := recorder.all()
	if len(records) == 0 {
		t.Fatal("control response produced no record; content-free assertion would be vacuous")
	}
	for _, record := range records {
		if strings.Contains(record.msg, sentinel) {
			t.Fatalf("record message contains Claude-authored mode: %q", record.msg)
		}
		for key, value := range record.attrs {
			if strings.Contains(key, sentinel) || strings.Contains(value, sentinel) {
				t.Fatalf("record %q attr %q=%q contains Claude-authored mode", record.msg, key, value)
			}
		}
	}
}
