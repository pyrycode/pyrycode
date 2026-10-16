package thread

import (
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"testing"

	"github.com/pyrycode/pyrycode/internal/history"
)

func TestCheckpointImportInvalid(t *testing.T) {
	entries := testFoldCorpus(151)
	f, source := testReferenceFold(entries)
	testFeed(t, f, entries...)
	d, rows, _ := f.ExportCheckpoint()
	cases := []struct {
		name   string
		mutate func(*CheckpointState, *[]CheckpointItem)
	}{
		{"rules", func(d *CheckpointState, _ *[]CheckpointItem) { d.Rules++ }},
		{"owner", func(d *CheckpointState, _ *[]CheckpointItem) { d.ConversationID = "other" }},
		{"missing", func(_ *CheckpointState, r *[]CheckpointItem) { *r = (*r)[:len(*r)-1] }},
		{"duplicate", func(_ *CheckpointState, r *[]CheckpointItem) { (*r)[1] = (*r)[0] }},
		{"order", func(_ *CheckpointState, r *[]CheckpointItem) { (*r)[0], (*r)[1] = (*r)[1], (*r)[0] }},
		{"manifest", func(d *CheckpointState, _ *[]CheckpointItem) { d.ItemIDs[1] = d.ItemIDs[0] }},
		{"turn-ref", func(d *CheckpointState, _ *[]CheckpointItem) { d.CurrentTurns[0].Ref = len(d.Turns) + 1 }},
		{"nil-turn", func(d *CheckpointState, _ *[]CheckpointItem) { d.CurrentTurns[0].Ref = 0 }},
		{"duplicate-root", func(d *CheckpointState, _ *[]CheckpointItem) {
			d.CurrentTurns = append(d.CurrentTurns, d.CurrentTurns[0])
		}},
		{"opening", func(d *CheckpointState, _ *[]CheckpointItem) { d.Openings[1] = 1 }},
		{"text-index", func(d *CheckpointState, _ *[]CheckpointItem) { d.Turns[0].Text = -2 }},
		{"text-kind", func(d *CheckpointState, _ *[]CheckpointItem) { d.Turns[0].Text = 0 }},
		{"call-index", func(d *CheckpointState, _ *[]CheckpointItem) { d.MainCalls[0].Item = len(d.ItemIDs) }},
		{"call-ref", func(d *CheckpointState, _ *[]CheckpointItem) { d.MainCalls[0].Terminal = -1 }},
		{"agent-ref", func(d *CheckpointState, _ *[]CheckpointItem) { d.AgentCalls[0].Ordinary = len(d.MainCalls) + 1 }},
		{"agent-index", func(d *CheckpointState, _ *[]CheckpointItem) { d.AgentCalls[0].Item = -2 }},
		{"launch-id", func(d *CheckpointState, _ *[]CheckpointItem) { d.AgentCalls[0].Launch++ }},
		{"report-ref", func(d *CheckpointState, _ *[]CheckpointItem) {
			d.AgentCalls[0].Reports.Pending = []int{len(d.Reports) + 1}
		}},
		{"report-digest", func(d *CheckpointState, _ *[]CheckpointItem) { d.Reports[0].Digest = "xx" }},
		{"task-digest", func(d *CheckpointState, _ *[]CheckpointItem) {
			task := d.Groups[0].Tasks
			for id, v := range task {
				v.LinkDigest = ""
				task[id] = v
				break
			}
		}},
		{"accepted-index", func(d *CheckpointState, _ *[]CheckpointItem) { d.Accepted[21] = 0 }},
		{"message-sentinel", func(d *CheckpointState, _ *[]CheckpointItem) { d.Messages[22] = -2 }},
		{"child-index", func(d *CheckpointState, _ *[]CheckpointItem) { d.Children[-1] = d.Children[3] }},
		{"child-snapshot", func(d *CheckpointState, _ *[]CheckpointItem) {
			for id, c := range d.Children {
				if c.Snapshot != nil {
					c.Snapshot.Base.Metadata.ID = 999
					d.Children[id] = c
					break
				}
			}
		}},
		{"child-call", func(d *CheckpointState, _ *[]CheckpointItem) { d.ChildCalls[0].Ref = -1 }},
		{"observation", func(d *CheckpointState, _ *[]CheckpointItem) { d.Observations[3][0].Group = len(d.Groups) + 1 }},
		{"touch", func(d *CheckpointState, _ *[]CheckpointItem) { d.Touches[99999] = 1 }},
		{"parent", func(_ *CheckpointState, r *[]CheckpointItem) { (*r)[0].Metadata.Parent = 99999 }},
		{"creating", func(_ *CheckpointState, r *[]CheckpointItem) { (*r)[0].Recipe.Creating = 0 }},
		{"field-item", func(_ *CheckpointState, r *[]CheckpointItem) {
			(*r)[0].Recipe.Fields = map[string]checkpointField{"bad": {Item: 99999, Digest: string(make([]byte, 64))}}
		}},
		{"field-digest", func(_ *CheckpointState, r *[]CheckpointItem) {
			(*r)[0].Recipe.Fields = map[string]checkpointField{"bad": {Digest: ""}}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			state, records := testCheckpointJSON(t, d), testCheckpointJSON(t, rows)
			tc.mutate(&state, &records)
			got, err := ImportCheckpoint("a", source, state, records)
			if got != nil || err == nil {
				t.Fatal("accepted malformed checkpoint")
			}
		})
	}
	// The manifest also protects a record that no continuation root references.
	f, source = testReferenceFold([]history.Entry{testEntry(10, "banner", `{"text":"unreferenced","level":"info","stops_turn":false}`)})
	testFeed(t, f, testEntry(10, "banner", `{"text":"unreferenced","level":"info","stops_turn":false}`))
	d, rows, _ = f.ExportCheckpoint()
	if got, err := ImportCheckpoint("a", source, d, nil); err == nil || got != nil {
		t.Fatal("lost unreferenced row accepted")
	}
	if got, err := ImportCheckpoint("a", source, d, rows); err != nil || got.unprovenThrough != 9 {
		t.Fatal("legitimate history gap rejected", err)
	}
}

func TestCheckpointImportSourceFailure(t *testing.T) {
	entries := []history.Entry{testMain(1, "assistant_delta", `,"text":"open"`), testMain(2, "assistant_delta", `,"text":"tail"`)}
	f, source := testReferenceFold(entries)
	testFeed(t, f, entries[:1]...)
	d, rows, _ := f.ExportCheckpoint()
	lookup := source.lookup
	calls := 0
	source.lookup = func([]uint64) ([]history.Entry, error) { calls++; return nil, errors.New("unavailable") }
	r, err := ImportCheckpoint("a", source, testCheckpointJSON(t, d), testCheckpointJSON(t, rows))
	if err != nil || calls != 0 || r.version != 1 {
		t.Fatal("import performed I/O", calls, err)
	}
	if items, err := r.Render(); !errors.Is(err, ErrUnavailable) || items != nil || calls == 0 {
		t.Fatal("partial or successful render", err)
	}
	if o := r.Observe(); o.Err != ErrUnavailable || o.Items != nil || o.Version != 0 {
		t.Fatal("partial observation", o)
	}
	source.lookup = lookup
	testFeed(t, r, entries[1:]...)
	if r.version != 2 || messageText(r.Items()[0]) != "opentail" {
		t.Fatal("suffix-only continuation failed")
	}
	testNoLifecyclePayload(t, r)
	if len(r.texts) != 0 || len(r.legacyOutcomes) != 0 {
		t.Fatal("restoration retained inline payloads")
	}
	legacy := New("a")
	testFeed(t, legacy, entries...)
	if !reflect.DeepEqual(legacy.Items(), r.Items()) {
		t.Fatal("legacy behavior changed")
	}
}

func TestCheckpointImportOwnership(t *testing.T) {
	entries := []history.Entry{testMain(1, "main_turn_opened", `,"occurred_at":"2026-01-01T00:00:00Z"`), testShellCall(2), testMain(3, "assistant_delta", `,"text":"first"`), testMain(4, "main_turn_opened", `,"occurred_at":"2026-01-01T00:00:00Z"`), testMain(5, "assistant_delta", `,"text":"second"`)}
	f, source := testReferenceFold(entries)
	testFeed(t, f, entries...)
	g := f.agentGroups[agentKey{}]
	report := &agentReport{id: 2, content: 2, field: "result", status: "done"}
	equal := *report
	g.calls["c"].reports = agentReports{pending: []*agentReport{report, &equal}, markers: [4]*agentReport{report, report}}
	d, rows, _ := f.ExportCheckpoint()
	d, rows = testCheckpointJSON(t, d), testCheckpointJSON(t, rows)
	r, err := ImportCheckpoint("a", source, d, rows)
	if err != nil {
		t.Fatal(err)
	}
	old, current := r.openings[1], r.openings[4]
	if old == current || r.openTexts[0] != old || r.openTexts[1] != current || old.textSlot != 1 || current.textSlot != 2 {
		t.Fatal("turn sharing lost")
	}
	c := r.agentGroups[agentKey{}].calls["c"]
	if c == g.calls["c"] || c.ordinary.shell != c || c.ordinary != old.calls["c"] || c.reports.pending[0] != c.reports.markers[0] || c.reports.markers[0] != c.reports.markers[1] || c.reports.pending[0] == c.reports.pending[1] {
		t.Fatal("graph identity lost")
	}
	before, beforeRows, _ := r.ExportCheckpoint()
	// Recursively mutate every document map/slice, including snapshot/literal bytes.
	var mutate func(reflect.Value)
	mutate = func(v reflect.Value) {
		switch v.Kind() {
		case reflect.Pointer:
			if !v.IsNil() {
				mutate(v.Elem())
			}
		case reflect.Struct:
			for i := 0; i < v.NumField(); i++ {
				mutate(v.Field(i))
			}
		case reflect.Map:
			for _, key := range v.MapKeys() {
				mutate(v.MapIndex(key))
				v.SetMapIndex(key, reflect.Value{})
			}
		case reflect.Slice:
			for i := 0; i < v.Len(); i++ {
				mutate(v.Index(i))
			}
		default:
			if v.CanSet() {
				v.SetZero()
			}
		}
	}
	mutate(reflect.ValueOf(&d))
	mutate(reflect.ValueOf(rows))
	after, afterRows, _ := r.ExportCheckpoint()
	if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(beforeRows, afterRows) {
		t.Fatal("document aliases restored graph")
	}
	testFeed(t, f, testMain(6, "assistant_delta", `,"text":"original"`))
	after, afterRows, _ = r.ExportCheckpoint()
	if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(beforeRows, afterRows) {
		t.Fatal("original aliases restored graph")
	}
	// Feeding the restored graph must preserve the saved document and original.
	stateRaw, _ := json.Marshal(before)
	rowsRaw, _ := json.Marshal(beforeRows)
	original, originalRows, _ := f.ExportCheckpoint()
	testFeed(t, r, testMain(6, "assistant_delta", `,"text":"restored"`))
	stateAgain, _ := json.Marshal(before)
	rowsAgain, _ := json.Marshal(beforeRows)
	if !slices.Equal(stateRaw, stateAgain) || !slices.Equal(rowsRaw, rowsAgain) {
		t.Fatal("restored feed changed document")
	}
	got, gotRows, _ := f.ExportCheckpoint()
	if !reflect.DeepEqual(original, got) || !reflect.DeepEqual(originalRows, gotRows) {
		t.Fatal("restored feed changed original")
	}
	c.reports.markers[0].status = "changed"
	if report.status != "done" || before.Reports[0].Status != "done" || c.reports.pending[1].status != "done" {
		t.Fatal("equal independent report aliases")
	}
}
