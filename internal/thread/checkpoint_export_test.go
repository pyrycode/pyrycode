package thread

import (
	"encoding/json"
	"reflect"
	"slices"
	"testing"

	"github.com/pyrycode/pyrycode/internal/history"
)

func testCheckpointJSON[T any](t *testing.T, value T) T {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var decoded T
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(value, decoded) {
		t.Fatalf("JSON round trip changed %T", value)
	}
	return decoded
}

func TestCheckpointEmpty(t *testing.T) {
	f, source := testReferenceFold(nil)
	source.lookup = func([]uint64) ([]history.Entry, error) { t.Fatal("export read history"); return nil, nil }
	state, records, err := f.ExportCheckpoint()
	if err != nil || state.ConversationID != "a" || state.Version != 0 || state.Schema != 2 || state.Rules != foldingRules || len(records) != 0 || len(state.ItemIDs) != 0 {
		t.Fatal("empty checkpoint", state, err)
	}
	testCheckpointJSON(t, state)
	testCheckpointJSON(t, records)
	if _, rows, err := New("a").ExportCheckpoint(); err == nil || rows != nil {
		t.Fatal("inline export accepted")
	}
}

func TestCheckpointRelationships(t *testing.T) {
	entries := []history.Entry{
		testMain(1, "main_turn_opened", `,"occurred_at":"2026-01-01T00:00:00Z"`),
		testMain(2, "tool_use", `,"tool_use_id":"shell","name":"Bash"`),
		testMain(3, "assistant_delta", `,"text":"first"`),
		testMain(4, "main_turn_opened", `,"occurred_at":"2026-01-01T00:00:00Z"`),
		testMain(5, "assistant_delta", `,"text":"second"`),
		testMain(6, "tool_result", `,"tool_use_id":"pending","is_error":false`),
	}
	f, _ := testReferenceFold(entries)
	testFeed(t, f, entries...)
	// Two equal independent reports must not be interned by value; repeated
	// marker slots referring to the same report must retain their shared identity.
	g := f.agentGroups[agentKey{}]
	r := &agentReport{id: 2, content: 6, field: "result", status: "done", digest: [32]byte{7}, mapped: true}
	equal := *r
	g.calls["shell"].reports = agentReports{pending: []*agentReport{r, &equal}, markers: [4]*agentReport{r, r}}
	state, _, err := f.ExportCheckpoint()
	if err != nil {
		t.Fatal(err)
	}
	state = testCheckpointJSON(t, state)
	old, current := state.Openings[1], state.Openings[4]
	if old == 0 || current == old || state.CurrentTurns[0].Ref != current || len(state.OpenTexts) != 2 || state.OpenTexts[0] != old || state.OpenTexts[1] != current {
		t.Fatal("turn generations or text owners lost", state)
	}
	turn := state.Turns[old-1]
	callID := turn.Calls["shell"]
	ordinary := state.MainCalls[callID-1]
	shell := state.AgentCalls[ordinary.Shell-1]
	if shell.Ordinary != callID || state.Groups[0].Calls["shell"] != ordinary.Shell {
		t.Fatal("shared shell owner lost")
	}
	if shell.Reports.Markers[0] != shell.Reports.Markers[1] || shell.Reports.Pending[0] != shell.Reports.Markers[0] || shell.Reports.Pending[0] == shell.Reports.Pending[1] {
		t.Fatal("report identity lost")
	}
	report := state.Reports[shell.Reports.Pending[0]-1]
	if report.ID != 2 || report.Content != 6 || report.Digest[:2] != "07" {
		t.Fatal("order/content provenance or digest lost")
	}
	if state.MainCalls[state.Turns[current-1].Calls["pending"]-1].Item != -1 {
		t.Fatal("uncreated call lost")
	}
}

func TestCheckpointRecords(t *testing.T) {
	entries := append(testReferenceSendEntries(),
		testDivider(4, "operator_reset", "s", "n", "claude", "codex"),
		testMain(5, "tool_use", `,"tool_use_id":"agent","name":"Agent"`),
		testChild(6, "tool_use", "agent", `,"tool_use_id":"done","name":"Read"`),
		testChild(7, "tool_result", "agent", `,"tool_use_id":"done","is_error":false`),
		testChild(8, "assistant_delta", "missing", `,"text":"hidden unfinished"`),
		testEntry(10, "send_accepted", `{"text":"pending"}`),
		testEntry(11, "banner", `{"text":"otherwise unreferenced","level":"info","stops_turn":false}`),
	)
	f, source := testReferenceFold(entries)
	testFeed(t, f, entries...)
	source.lookup = func([]uint64) ([]history.Entry, error) { t.Fatal("export read history"); return nil, nil }
	state, records, err := f.ExportCheckpoint()
	if err != nil {
		t.Fatal(err)
	}
	state, records = testCheckpointJSON(t, state), testCheckpointJSON(t, records)
	if len(records) != len(f.items) || len(state.ItemIDs) != len(records) {
		t.Fatal("missing internal records")
	}
	for i, row := range records {
		if row.Metadata.ID != f.items[i].ID || state.ItemIDs[i] != row.Metadata.ID || !reflect.DeepEqual(row, checkpointRecord(f.items[i], f.recipes[f.items[i].ID])) {
			t.Fatal("internal order/metadata/recipe changed", i)
		}
		metadata, item := reflect.ValueOf(row.Metadata), reflect.ValueOf(f.items[i])
		for field := 0; field < metadata.NumField(); field++ {
			if !reflect.DeepEqual(metadata.Field(field).Interface(), item.FieldByName(metadata.Type().Field(field).Name).Interface()) {
				t.Fatal("metadata field changed", i, metadata.Type().Field(field).Name)
			}
		}
	}
	accepted, exists := state.Accepted[10]
	if !exists || records[accepted].Metadata.Status != "queued" || state.Messages[2] != -1 || state.Suppressed != 3 || state.UnprovenThrough != 9 || len(state.PendingBoundaries) != 1 || state.PendingBoundaries[0].IDs[0] != 4 || state.LegacyScope != 1 || state.Successor.Kind != "codex" || records[0].Metadata.Order != 2 || records[0].Recipe.Delivery != 2 {
		t.Fatal("send/boundary continuation lost", state)
	}
	if !reflect.DeepEqual(state.Touches, f.touches) || !reflect.DeepEqual(state.Published, f.published) || !reflect.DeepEqual(state.PublicVisible, f.publicVisible) || state.LastShown != f.lastShown {
		t.Fatal("publication facts lost")
	}
	var final, reversible bool
	for index, child := range state.Children {
		if child.Final && child.Snapshot == nil {
			final = true
		}
		if child.Snapshot != nil && records[index].Metadata.ID == 8 && child.Parent == "missing" && records[index].Metadata.Parent == 0 {
			reversible = true
		}
	}
	if !final || !reversible || records[len(records)-1].Metadata.ID != 11 {
		t.Fatal("final/reversible/unreferenced records missing")
	}
}

func TestCheckpointLifecycleFacts(t *testing.T) {
	entries := []history.Entry{
		testDurable(1, "agent_call_observed", `,"tool_call_id":"root","tool":"Agent"`),
		testMain(2, "tool_use", `,"tool_use_id":"root","name":"Agent"`),
		testChild(3, "tool_use", "root", `,"tool_use_id":"nested","name":"Agent"`),
		testChild(4, "tool_use", "nested", `,"tool_use_id":"pending","name":"Read"`),
		testMain(5, "tool_result", `,"tool_use_id":"root","is_error":false`),
		testMain(6, "background_task_started", `,"task_id":"task","tool_call_id":"root"`),
		testMain(7, "background_task_updated", `,"task_id":"task","status":"completed"`),
		testMain(8, "turn_end", `,"stop_reason":"end_turn"`),
	}
	f, _ := testReferenceFold(entries)
	testFeed(t, f, entries...)
	state, _, err := f.ExportCheckpoint()
	if err != nil {
		t.Fatal(err)
	}
	state = testCheckpointJSON(t, state)
	if len(state.Lifetimes) != 1 || state.Lifetimes[0].Lifetime == "" || len(state.Observations[1]) != 1 {
		t.Fatal("lifetime/observation lost")
	}
	g := state.Groups[state.Observations[1][0].Group-1]
	if g.Key.Lifetime != state.Lifetimes[0].Lifetime || g.Tasks["task"].Call != "root" || g.Tasks["task"].Link != 6 {
		t.Fatal("group/task ownership lost")
	}
	markers := g.Tasks["task"].Reports.Markers
	if markers[0] == 0 || len(g.Tasks["task"].Reports.Pending) != 0 || state.Reports[markers[0]-1].Content != 7 {
		t.Fatal("final task marker expanded or lost")
	}
	ending := state.ChildEndings[1]
	if ending.Item != 2 || ending.Revision != 7 || state.Children[2].Snapshot == nil || state.Children[2].Snapshot.Base.Metadata.Active != true {
		t.Fatal("inherited ending/reversible intrinsic state lost")
	}
	if len(state.ChildTurns) != len(f.childTurns) || len(state.ChildCalls) != len(f.childCalls) || len(state.ChildReports) != len(f.childReports) {
		t.Fatal("child roots lost")
	}
	for _, call := range state.ChildCalls {
		if state.MainCalls[call.Ref-1].Group != call.Group {
			t.Fatal("shared child call scope lost")
		}
	}
	for _, turn := range state.Turns {
		if turn.Opening == 2 && (len(turn.Finished) != 1 || turn.Calls != nil || turn.Finished["root"].Item != -2) {
			t.Fatal("launcher marker expanded or lost")
		}
	}
}

func TestCheckpointOwnership(t *testing.T) {
	entries := testFoldCorpus(151)
	f, _ := testReferenceFold(entries)
	testFeed(t, f, entries...)
	// A separate replay supplies an independent graph for the read-only assertion.
	control, _ := testReferenceFold(entries)
	testFeed(t, control, entries...)
	state, records, err := f.ExportCheckpoint()
	if err != nil {
		t.Fatal(err)
	}
	left, right := *f, *control
	left.source, right.source = nil, nil
	if !reflect.DeepEqual(left, right) {
		t.Fatal("export changed the fold")
	}
	before, _ := json.Marshal(state)
	beforeRows, _ := json.Marshal(records)
	// Mutate every nested exported container, including recipe literal bytes.
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
	mutate(reflect.ValueOf(&state))
	mutate(reflect.ValueOf(records))
	again, againRows, _ := f.ExportCheckpoint()
	got, _ := json.Marshal(again)
	gotRows, _ := json.Marshal(againRows)
	if !slices.Equal(got, before) || !slices.Equal(gotRows, beforeRows) {
		t.Fatal("export leaked mutable storage or mutated source")
	}
	testFeed(t, f, testMain(152, "assistant_delta", `,"text":"tail"`))
	got, _ = json.Marshal(again)
	gotRows, _ = json.Marshal(againRows)
	if !slices.Equal(got, before) || !slices.Equal(gotRows, beforeRows) {
		t.Fatal("later feed changed saved checkpoint")
	}
}
