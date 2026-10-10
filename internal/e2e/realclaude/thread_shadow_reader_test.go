//go:build !e2e_realclaude

package realclaude

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/thread"
)

func TestThreadShadowReaderRejectsIncomplete(t *testing.T) {
	for _, tc := range []struct {
		name  string
		files []string
	}{
		{"both_missing", nil},
		{"history_missing", []string{shadowExpectedFile}},
		{"expected_missing", []string{shadowHistoryFile}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for _, name := range tc.files {
				// Empty synthetic schema controls are never retained as live evidence.
				if err := os.WriteFile(filepath.Join(dir, name), []byte(`{}`), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if _, _, err := shadowReadPair(dir); err == nil || err.Error() != "shadow evidence pair missing" {
				t.Fatal("missing evidence escaped strict reader rejection")
			}
		})
	}
	h := shadowHistory{}
	e := shadowExpected{}
	for _, pin := range []bool{false, true} {
		if err := shadowValidatePair(h, e, pin); err == nil {
			t.Fatal("empty evidence accepted")
		}
	}
	for _, raw := range []string{`{"text":"/home/operator/private"}`, `{"token":"sk-ant-secret"}`, `{"path":"/Users/operator/work"}`} {
		if err := shadowDeny([]byte(raw)); err == nil {
			t.Fatal("unsafe retained bytes accepted")
		}
	}
	raw, err := shadowSanitize([]byte(`{"id":7,"text":"/tmp/capture/work/alpha","parent":3}`))
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"id":7,"parent":3,"text":"$HOST_PATH"}` {
		t.Fatal("sanitizer changed joins or failed to replace path")
	}
}

func TestThreadShadowRawRowsRejectsLostText(t *testing.T) {
	h := shadowHistory{Entries: []history.Entry{{ID: 1, Type: protocol.TypeAssistantDelta, Payload: json.RawMessage(`{"text":"observed","turn_id":"turn"}`)}}}
	cp := shadowCheckpoint{Version: 1, Items: []thread.Item{{ID: 1, Kind: "assistant_message", Status: "running", Active: true, Content: json.RawMessage(`{"text":"observed"}`)}}}
	if err := shadowRawRows(h, cp); err != nil {
		t.Fatal(err)
	}
	cp.Items[0].Status = "done"
	if shadowRawRows(h, cp) == nil {
		t.Fatal("incorrect text status accepted")
	}
	cp.Items[0].Status = "running"
	cp.Items[0].Content = json.RawMessage(`{"text":"wrong"}`)
	if shadowRawRows(h, cp) == nil {
		t.Fatal("changed observed text accepted")
	}
	cp.Items = nil
	if shadowRawRows(h, cp) == nil {
		t.Fatal("missing observed row accepted")
	}
}

// Synthetic terminal-order controls are never retained as live history.
func TestThreadShadowRawToolTerminalOrder(t *testing.T) {
	creation := json.RawMessage(`{"turn_id":"turn","tool_use_id":"call","name":"Bash","input":{"command":"echo marker"}}`)
	result := json.RawMessage(`{"turn_id":"turn","tool_use_id":"call","is_error":false,"result_summary":"marker"}`)
	lateResult := json.RawMessage(`{"turn_id":"turn","tool_use_id":"call","is_error":true,"result_summary":"late interruption"}`)
	interruption := json.RawMessage(`{"turn_id":"turn","tool_call_id":"call","cause":"operator_reset","occurred_at":"2026-10-09T00:00:00Z"}`)
	ending := json.RawMessage(`{"turn_id":"turn","stop_reason":"end_turn"}`)
	for _, tc := range []struct {
		name, typ, field, status string
		raw                      json.RawMessage
	}{
		{"result", protocol.TypeToolResult, "result", "done", result},
		{"failed_result", protocol.TypeToolResult, "result", "failed", lateResult},
		{"interruption_before_result", "main_tool_interrupted", "interruption", "interrupted", interruption},
		{"turn_end_before_result", protocol.TypeTurnEnd, "ending", "interrupted", ending},
		{"turn_interruption_before_result", "main_turn_interrupted", "ending", "interrupted", interruption},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := shadowHistory{Entries: []history.Entry{{ID: 1, Type: protocol.TypeToolUse, Payload: creation}, {ID: 2, Type: tc.typ, Payload: tc.raw}, {ID: 3, Type: protocol.TypeToolResult, Payload: lateResult}}}
			var content map[string]json.RawMessage
			_ = json.Unmarshal(creation, &content)
			content[tc.field] = tc.raw
			raw, _ := json.Marshal(content)
			cp := shadowCheckpoint{Version: 3, Items: []thread.Item{{ID: 1, Kind: "tool_call", Status: tc.status, Content: raw}}}
			if err := shadowRawRows(h, cp); err != nil {
				t.Fatal(err)
			}
			cp.Items[0].Status = "running"
			if shadowRawRows(h, cp) == nil {
				t.Fatal("terminal status loss accepted")
			}
			cp.Items[0].Status = tc.status
			content[tc.field] = json.RawMessage(`{}`)
			cp.Items[0].Content, _ = json.Marshal(content)
			if shadowRawRows(h, cp) == nil {
				t.Fatal("terminal content loss accepted")
			}
		})
	}
	t.Run("closure_requires_raw_terminal", func(t *testing.T) {
		h := shadowHistory{Entries: []history.Entry{{ID: 1, Type: protocol.TypeToolUse, Payload: creation}}}
		cp := shadowCheckpoint{Name: "session_closed", Version: 1, Items: []thread.Item{{ID: 1, Kind: "tool_call", Status: "interrupted", Content: creation}}}
		if shadowRawRows(h, cp) == nil {
			t.Fatal("interruption inferred without raw terminal")
		}
		cp.Items[0].Status, cp.Items[0].Active = "running", true
		if err := shadowRawRows(h, cp); err != nil {
			t.Fatal(err)
		}
	})
}

// These schema controls are synthetic and never promoted to capture artifacts.
func TestThreadShadowPairRejectsBadProvenance(t *testing.T) {
	h := shadowHistory{Conversation: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"}
	e := shadowExpected{}
	for i, name := range shadowChecks {
		h.Entries = append(h.Entries, history.Entry{ID: uint64(i + 1), Type: "control", Payload: json.RawMessage(`{}`)})
		e.Checkpoints = append(e.Checkpoints, shadowCheckpoint{Name: name, Version: uint64(i + 1), Items: []thread.Item{{ID: 1}}})
	}
	h.Provenance = shadowProvenance{Schema: 1, Capture: "synthetic-control", ClaudeVersion: "control", DaemonCommit: strings.Repeat("a", 40), HistorySHA256: shadowDigest(h.Entries), Checks: map[string]shadowCheck{}}
	for _, name := range shadowChecks {
		h.Provenance.Checks[name] = shadowCheck{Executed: 1}
	}
	h.Provenance.Checks[shadowScenarioCheck] = shadowCheck{Executed: 1}
	e.Provenance = h.Provenance
	if err := shadowValidatePair(h, e, false); err == nil || err.Error() != "required legacy scenario absent" {
		t.Fatal("schema control did not reach independent witness check")
	}
	for _, tc := range []struct {
		name, want string
		change     func(*shadowHistory, *shadowExpected)
	}{
		{"unpinned", "counted dispatcher report", func(h *shadowHistory, e *shadowExpected) {}},
		{"mixed_pair", "inconsistent", func(h *shadowHistory, e *shadowExpected) { e.Provenance.Capture = "other" }},
		{"changed_history", "inconsistent", func(h *shadowHistory, e *shadowExpected) { h.Entries[0].Payload = json.RawMessage(`{"changed":true}`) }},
		{"skipped_check", "missing failed skipped", func(h *shadowHistory, e *shadowExpected) {
			h.Provenance.Checks[shadowChecks[0]] = shadowCheck{Skipped: 1}
			e.Provenance = h.Provenance
		}},
		{"skipped_scenario", "missing failed skipped", func(h *shadowHistory, e *shadowExpected) {
			h.Provenance.Checks[shadowScenarioCheck] = shadowCheck{Skipped: 1}
			e.Provenance = h.Provenance
		}},
		{"missing_checkpoint", "incomplete", func(h *shadowHistory, e *shadowExpected) { e.Checkpoints = e.Checkpoints[:3] }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, _ := json.Marshal(h)
			var hh shadowHistory
			_ = json.Unmarshal(raw, &hh)
			raw, _ = json.Marshal(e)
			var ee shadowExpected
			_ = json.Unmarshal(raw, &ee)
			if tc.name != "unpinned" {
				hh.Provenance.GateReport = "https://github.com/pyrycode/pyrycode/issues/3068#issuecomment-1"
				ee.Provenance = hh.Provenance
			}
			tc.change(&hh, &ee)
			if err := shadowValidatePair(hh, ee, true); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatal("invalid evidence escaped intended rejection")
			}
		})
	}
}
