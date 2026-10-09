package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"testing"

	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/thread"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// These captures establish shell completion, not agent disappearance. Ordinary
// Bash creation/result context is absent from their quarry frames and synthetic.
func TestThreadShellRecordedReplay(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		file, ticket, version, captured, call, task string
		count                                       int
	}{
		{"task_notification_v2.1.259.json", "2247", "2.1.259 (Claude Code)", "2026-09-09T21:09:37+03:00", "toolu_01RVFCkACkpA1cv8gmvXjKSj", "buwavm27r", 2},
		{"roster_after_finish_v2.1.280.json", "2525", "2.1.280 (Claude Code)", "2026-09-22T22:02:30+03:00", "toolu_01CX7tQrqBqfeSv8Aik9zPtT", "b1t2d8z05", 6},
	} {
		t.Run(tc.ticket, func(t *testing.T) {
			raw, err := os.ReadFile("../../internal/e2e/realclaude/testdata/" + tc.file)
			if err != nil {
				t.Fatal(err)
			}
			var capture struct {
				Ticket   string `json:"ticket"`
				Version  string `json:"claude_version"`
				Captured string `json:"captured_at"`
				Capture  bool   `json:"is_capture"`
				Frames   []struct {
					Payload string
					Subtype string
				} `json:"frames"`
			}
			if json.Unmarshal(raw, &capture) != nil || capture.Ticket != tc.ticket || capture.Version != tc.version || capture.Captured != tc.captured || !capture.Capture || len(capture.Frames) != tc.count {
				t.Fatal("capture provenance changed")
			}
			store := history.New(t.TempDir())
			e := newInteractiveTurnEmitterV2(nil, historyOnlyBroadcaster{}, discardLogger())
			e.hist, e.runtimeFacts = store, true
			source := history.SessionProvenance{Kind: "claude", SessionID: "recorded-shell"}
			parser, _ := newSessionParser(func(ev turnevent.Event) { e.HandleFor(context.Background(), testConvID, ev, source) }, discardLogger())
			write := func(frame string) {
				t.Helper()
				if _, err := parser.Write([]byte(frame + "\n")); err != nil {
					t.Fatal(err)
				}
			}
			// Explicit synthetic context, using the recording's exact call identity.
			write(fmt.Sprintf(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":%q,"name":"Bash","input":{"command":"synthetic shell context"}}]}}`, tc.call))
			write(fmt.Sprintf(`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":%q,"content":"synthetic launch result","is_error":false}]}}`, tc.call))
			write(`{"type":"result","subtype":"success","is_error":false,"result":"synthetic turn end"}`)
			var omissionVersion uint64
			for i, frame := range capture.Frames {
				write(frame.Payload)
				if tc.ticket == "2525" && i == 2 {
					entries := historyEntries(t, store, testConvID)
					f := thread.New(testConvID)
					if err := f.Feed(entries); err != nil {
						t.Fatal(err)
					}
					for _, item := range f.Items() {
						if item.Kind == "tool_call" {
							if !item.Active || item.EndedOrder != 0 {
								t.Fatalf("roster omission finished shell: %#v", item)
							}
							omissionVersion = f.Version()
						}
					}
				}
			}
			entries := historyEntries(t, store, testConvID)
			full := thread.New(testConvID)
			if err := full.Feed(entries); err != nil {
				t.Fatal(err)
			}
			var calls []thread.Item
			for _, item := range full.Items() {
				if item.Kind == "agent" {
					t.Fatal("shell became agent")
				}
				if item.Kind == "tool_call" {
					calls = append(calls, item)
				}
			}
			if len(calls) != 1 {
				t.Fatalf("ordinary calls: %#v", calls)
			}
			call := calls[0]
			var content struct {
				Call   string            `json:"tool_use_id"`
				Task   string            `json:"task_id"`
				Input  map[string]string `json:"input"`
				Link   json.RawMessage   `json:"task_link"`
				Result struct {
					Summary string `json:"result_summary"`
				} `json:"result"`
				Report struct {
					Status  string `json:"status"`
					Summary string `json:"summary"`
				} `json:"task_report"`
			}
			if json.Unmarshal(call.Content, &content) != nil || content.Call != tc.call || content.Task != tc.task || content.Input["command"] != "synthetic shell context" || content.Result.Summary != "synthetic launch result" || len(content.Link) == 0 || content.Report.Status != "completed" || call.Status != "finished" || call.Active || call.EndedOrder == 0 || call.Session != source.SessionID || call.Agent != "claude" {
				t.Fatalf("joined shell: %#v %s", call, call.Content)
			}
			if tc.ticket == "2525" && (omissionVersion == 0 || call.EndedOrder <= omissionVersion) {
				t.Fatal("completion not after recorded omission")
			}
			creation, ending := false, false
			for _, entry := range entries {
				if entry.ID == call.ID && entry.Type == "tool_use" {
					creation = true
				}
				if entry.ID == call.EndedOrder && entry.Type == "background_task_outcome" {
					ending = true
				}
			}
			if !creation || !ending {
				t.Fatal("item identity or terminal evidence is not its raw history fact")
			}
			for split := 0; split <= len(entries); split++ {
				f := thread.New(testConvID)
				if err := f.Feed(entries[:split]); err != nil {
					t.Fatal(err)
				}
				if err := f.Feed(entries[split:]); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(f.Items(), full.Items()) || f.Version() != full.Version() {
					t.Fatalf("partition %d", split)
				}
			}
			incremental := thread.New(testConvID)
			for _, entry := range entries {
				if err := incremental.Feed([]history.Entry{entry}); err != nil {
					t.Fatal(err)
				}
			}
			if !reflect.DeepEqual(incremental.Items(), full.Items()) {
				t.Fatal("incremental differs")
			}
		})
	}
}
