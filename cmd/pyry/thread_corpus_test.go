package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/streamsup"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// testRecordedFrames is shared with the independent recorded-history assertions.
// Shell recordings require the explicitly synthetic creation/result/turn-end
// context; only the supplied frames are captured evidence.
func testRecordedFrames(t *testing.T, frames []string, shellCall string) ([]history.Entry, []int) {
	t.Helper()
	store := history.New(t.TempDir())
	e := newInteractiveTurnEmitterV2(nil, historyOnlyBroadcaster{}, discardLogger())
	e.hist, e.runtimeFacts = store, true
	source := history.SessionProvenance{Kind: "claude", SessionID: "recorded"}
	if shellCall != "" {
		source.SessionID = "recorded-shell"
	}
	sink := func(ev turnevent.Event) { e.HandleFor(context.Background(), testConvID, ev, source) }
	var parser io.Writer = streamsup.NewParser(sink, discardLogger())
	if shellCall != "" {
		parser, _ = newSessionParser(sink, discardLogger())
	}
	write := func(frame string) {
		t.Helper()
		if _, err := parser.Write([]byte(frame + "\n")); err != nil {
			t.Fatal(err)
		}
	}
	if shellCall != "" {
		write(fmt.Sprintf(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":%q,"name":"Bash","input":{"command":"synthetic shell context"}}]}}`, shellCall))
		write(fmt.Sprintf(`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":%q,"content":"synthetic launch result","is_error":false}]}}`, shellCall))
		write(`{"type":"result","subtype":"success","is_error":false,"result":"synthetic turn end"}`)
	}
	var boundaries []int
	for _, frame := range frames {
		write(frame)
		boundaries = append(boundaries, len(historyEntries(t, store, testConvID)))
	}
	return historyEntries(t, store, testConvID), boundaries
}

// Normalization v1 changes only top-level emitter fields: turn_id/lifetime_id
// use distinct UUID namespaces and first-occurrence ordinals, preserving every
// reference. Entry TS and emitter occurred_at use 2026-01-01 + entry ID ns.
// Recorded nested content, IDs, durations and timestamps are retained verbatim.
// No turn_id/lifetime_id in these recordings is supplied by a captured frame.
func testNormalizeRecorded(t *testing.T, entries []history.Entry) []history.Entry {
	t.Helper()
	ids := map[string]map[string]string{"turn_id": {}, "lifetime_id": {}}
	for i := range entries {
		e := &entries[i]
		e.TS = time.Date(2026, 1, 1, 0, 0, 0, int(e.ID), time.UTC)
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(e.Payload, &fields); err != nil {
			t.Fatal(err)
		}
		for namespace, key := range []string{"turn_id", "lifetime_id"} {
			var old string
			if raw, ok := fields[key]; ok {
				if err := json.Unmarshal(raw, &old); err != nil {
					t.Fatal(err)
				}
				if old != "" {
					if ids[key][old] == "" {
						ids[key][old] = fmt.Sprintf("%08d-0000-4000-8000-%012d", namespace+1, len(ids[key])+1)
					}
					fields[key], _ = json.Marshal(ids[key][old])
				}
			}
		}
		if _, ok := fields["occurred_at"]; ok {
			fields["occurred_at"], _ = json.Marshal(e.TS)
		}
		var err error
		e.Payload, err = json.Marshal(fields)
		if err != nil {
			t.Fatal(err)
		}
	}
	return entries
}

// Explicit generation: go test ./cmd/pyry -run '^TestThreadRecordedBaselines$'
// -args -write-fold-baselines. See corpus_golden_test.go for digest conventions.
func TestThreadRecordedBaselines(t *testing.T) {
	for _, tc := range []struct{ file, call string }{
		{"parent_tool_use_v2.1.259.json", ""},
		{"task_notification_v2.1.259.json", "toolu_01RVFCkACkpA1cv8gmvXjKSj"},
		{"roster_after_finish_v2.1.280.json", "toolu_01CX7tQrqBqfeSv8Aik9zPtT"},
	} {
		t.Run(tc.file, func(t *testing.T) {
			raw, err := os.ReadFile("../../internal/e2e/realclaude/testdata/" + tc.file)
			if err != nil {
				t.Fatal(err)
			}
			var capture struct{ Frames []struct{ Payload string } }
			if err := json.Unmarshal(raw, &capture); err != nil {
				t.Fatal(err)
			}
			var frames []string
			for _, frame := range capture.Frames {
				frames = append(frames, frame.Payload)
			}
			derive := func() []history.Entry {
				entries, _ := testRecordedFrames(t, frames, tc.call)
				return testNormalizeRecorded(t, entries)
			}
			entries := derive()
			testRecordedQueryIndexes(t, entries)
			if !reflect.DeepEqual(entries, derive()) {
				t.Fatal("repeated normalized derivations differ")
			}
			// Small recordings retain every emitted version, including companion,
			// link, child appearance, roster omission and ending boundaries.
			var versions []int
			for i := range entries {
				versions = append(versions, i+1)
			}
			testFoldGoldenCheck(t, "testdata/"+tc.file+".golden", testConvID, "recorded-normalization-v1/synthetic-shell-context-v1", raw, entries, versions)
		})
	}
}
