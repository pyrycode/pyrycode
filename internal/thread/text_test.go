package thread

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

func TestTextAppendBytes(t *testing.T) {
	t.Parallel()
	for _, initial := range []string{
		`{ "turn_id":"t", "text":"  first\n", "z":{"text":""}, "a":9007199254740993 }`,
		`{"turn_id":"t","Text":"upper","text":"lower","TEXT":"last"}`,
		`{"turn_id":"t","text":"\u0000\t"}`,
		`{"turn_id":"t","text":"\ud800","z":"<&>"}`,
		"{\"turn_id\":\"t\",\"text\":\"\xff\"}",
	} {
		f := New("a")
		e := testEntry(1, "assistant_delta", initial)
		testFeed(t, f, e)
		want := append([]byte(nil), e.Payload...)
		oldSnapshot := f.Items()
		for i, suffix := range []string{"", " \x1b\n", "<&>\"\\雪🙂\u2028", "\xc3", "\xa9", "\v next\x00", "", "\u00a0end "} {
			var prior protocol.AssistantDeltaPayload
			_ = json.Unmarshal(want, &prior)
			raw, _ := json.Marshal(map[string]string{"turn_id": "t", "text": suffix})
			var delta protocol.AssistantDeltaPayload
			_ = json.Unmarshal(raw, &delta)
			if delta.Text != "" {
				var fields map[string]json.RawMessage
				_ = json.Unmarshal(want, &fields)
				fields["text"], _ = json.Marshal(prior.Text + delta.Text)
				want, _ = json.Marshal(fields)
			}
			testFeed(t, f, testEntry(uint64(i+2), "assistant_delta", string(raw)))
			item := f.Items()[0]
			if !bytes.Equal(item.Content, want) {
				t.Fatalf("suffix %q: got %s want %s", suffix, item.Content, want)
			}
			if delta.Text != "" {
				summary := plainSummary(prior.Text + delta.Text)
				if summary == "" {
					summary = "assistant message"
				}
				if item.Summary != summary {
					t.Fatalf("summary %q want %q", item.Summary, summary)
				}
			}
			item.Content[0] = '!'
		}
		if string(oldSnapshot[0].Content) != initial {
			t.Fatal("prior snapshot changed")
		}
		e.Payload[0] = '!'
		if !bytes.Equal(f.Items()[0].Content, want) {
			t.Fatal("input/snapshot alias")
		}
	}
}

func TestTextAppendChildren(t *testing.T) {
	t.Parallel()
	entries := []history.Entry{
		testMain(1, "tool_use", `,"tool_use_id":"p","name":"Agent"`),
		testChild(2, "assistant_delta", "p", `,"text":"initial ","z":"tail"`),
		testChild(3, "assistant_delta", "p", `,"text":"open"`),
		testMain(4, "tool_result", `,"tool_use_id":"p","is_error":false`),
		testChild(5, "assistant_delta", "p", `,"text":" closed"`),
		testChild(6, "assistant_delta", "p", `,"text":""`),
		testMain(7, "background_task_started", `,"task_id":"task","tool_call_id":"p"`),
		testChild(8, "assistant_delta", "p", `,"text":" reopened"`),
		testChild(9, "turn_end", "p", `,"stop_reason":"end_turn"`),
	}
	f := New("a")
	var snapshots [][]Item
	for _, e := range entries {
		testFeed(t, f, e)
		snapshots = append(snapshots, f.Items())
	}
	if snapshots[3][1].Active || !snapshots[6][1].Active {
		t.Fatal("closure/reopen")
	}
	if snapshots[4][1].Rev != snapshots[5][1].Rev {
		t.Fatal("empty delta changed revision")
	}
	for i := range snapshots {
		replay := New("a")
		testFeed(t, replay, entries[:i+1]...)
		if !reflect.DeepEqual(snapshots[i], replay.Items()) {
			t.Fatalf("snapshot %d mutated", i)
		}
	}
	testContent(t, f.Items()[1], "text", "initial open closed reopened")
}

func TestTextEmptyProvisionalClosure(t *testing.T) {
	t.Parallel()
	f := New("a")
	entries := []history.Entry{
		testMain(1, "tool_use", `,"tool_use_id":"p","name":"Agent"`),
		testChild(2, "assistant_delta", "p", `,"text":" first ","z":{"html":"<&>"}`),
		testMain(3, "tool_result", `,"tool_use_id":"p","is_error":false`),
		testChild(4, "assistant_delta", "p", `,"text":""`),
		testChild(5, "assistant_delta", "p", `,"text":""`),
		testChild(6, "assistant_delta", "p", `,"text":"\nsecond"`),
		testMain(7, "background_task_started", `,"task_id":"task","tool_call_id":"p"`),
	}
	testFeed(t, f, entries[:3]...)
	closed := f.Observe()
	testFeed(t, f, entries[3:5]...)
	if !reflect.DeepEqual(closed.Items, f.Items()) || closed.LastShownVersion != f.lastShown {
		t.Fatal("empty delta changed closure")
	}
	testFeed(t, f, entries[5:]...)
	item := f.Items()[1]
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(item.Content, &fields)
	if fields["parent_ending"] != nil || !item.Active {
		t.Fatal("derived closure survived reopening")
	}
	testContent(t, item, "text", " first \nsecond")
	if closed.Items[1].Rev != 3 {
		t.Fatal("closure snapshot changed")
	}
}

func TestTextAppendScaling(t *testing.T) {
	for _, lane := range []string{"main", "open", "closed", "reopened", "lifecycle"} {
		t.Run(lane, func(t *testing.T) {
			var small int
			for _, n := range []int{256, 1024} {
				f := testCountedFold()
				feed := func(e history.Entry) { e.ID = f.version + 1; testFeed(t, f, e) }
				if lane != "main" {
					feed(testMain(1, "tool_use", `,"tool_use_id":"p","name":"Agent"`))
					feed(testChild(1, "assistant_delta", "p", `,"text":"start"`))
					if lane == "closed" || lane == "reopened" {
						feed(testMain(1, "tool_result", `,"tool_use_id":"p","is_error":false`))
					}
					if lane == "reopened" {
						feed(testMain(1, "background_task_started", `,"task_id":"task","tool_call_id":"p"`))
					}
				}
				phases := 1
				if lane == "lifecycle" {
					phases = 3
				}
				for phase := 0; phase < phases; phase++ {
					if phase == 1 {
						feed(testMain(1, "tool_result", `,"tool_use_id":"p","is_error":false`))
					}
					if phase == 2 {
						feed(testMain(1, "background_task_started", `,"task_id":"task","tool_call_id":"p"`))
					}
					for i := 0; i < n; i++ {
						e := testMain(1, "assistant_delta", `,"text":"<&>雪\nfixed "`)
						if lane != "main" {
							e = testChild(1, "assistant_delta", "p", `,"text":"<&>雪\nfixed "`)
						}
						feed(e)
					}
				}
				work := testIngestionWork(f)
				t.Logf("n=%d phases=%d work=%d per delta=%d", n, phases, work, work/(n*phases))
				if small != 0 && work >= small*8 {
					t.Fatalf("4x deltas cost >=8x: %d -> %d", small, work)
				}
				small = work
			}
		})
	}
}

func TestFoldIngestionScaling(t *testing.T) {
	var small int
	for _, n := range []int{1000, 4000} {
		f := testCountedFold()
		testFeed(t, f, testFoldCorpus(n)...)
		work := testIngestionWork(f)
		t.Logf("entries=%d work=%d per entry=%d", n, work, work/n)
		if small != 0 && work >= small*8 {
			t.Fatalf("4x corpus cost >=8x: %d -> %d", small, work)
		}
		small = work
	}
}

func testCountedFold() *Fold {
	f := New("a")
	f.ingestCounts = &ingestionWorkCounts{}
	f.agentCounts = &agentWorkCounts{}
	f.publicCounts = &publicWorkCounts{}
	f.childCounts = &childWorkCounts{}
	f.closureCounts = &closureWorkCounts{}
	return f
}

func testIngestionWork(f *Fold) int {
	c := f.childCounts
	return f.ingestCounts.work + f.agentCounts.work + f.publicCounts.work + f.closureCounts.work + c.registered + c.restored + c.baseCopies + c.expanded + c.edges + c.resolved
}
