package thread

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/pyrycode/pyrycode/internal/history"
)

// Follow values, not just inline types: an empty Item or an indirect recipe is
// still a redundant snapshot. Maps/slices are bounded independently of content.
func testChildMarkerShape(t *testing.T, root any) int {
	t.Helper()
	var walk func(reflect.Value) int
	walk = func(v reflect.Value) int {
		if v.Type() == reflect.TypeFor[Item]() || v.Type() == reflect.TypeFor[contentRecipe]() {
			t.Fatalf("final child retained snapshot %s", v.Type())
		}
		switch v.Kind() {
		case reflect.Pointer, reflect.Interface:
			if !v.IsNil() {
				return walk(v.Elem())
			}
			return 0
		case reflect.Struct:
			n := 0
			for i := 0; i < v.NumField(); i++ {
				if v.Type() == reflect.TypeFor[Fold]() {
					switch v.Type().Field(i).Name {
					case "items", "recipes", "source":
						continue // Explicit public projections and independently bounded source.
					}
				}
				n += walk(v.Field(i))
			}
			return n
		case reflect.Map:
			n := 0
			iter := v.MapRange()
			for iter.Next() {
				n += walk(iter.Key()) + walk(iter.Value())
			}
			return n
		case reflect.Array, reflect.Slice:
			n := 0
			for i := 0; i < v.Len(); i++ {
				n += walk(v.Index(i))
			}
			return n
		default:
			return 1
		}
	}
	return walk(reflect.ValueOf(root))
}

func TestReferenceChildMarkers(t *testing.T) {
	entries := []history.Entry{
		testDurable(1, "agent_call_observed", `,"tool_call_id":"root","tool":"Agent"`),
		testMain(2, "tool_use", `,"tool_use_id":"root","name":"Agent"`),
		testMain(3, "tool_use", `,"tool_use_id":"other","name":"Agent"`),
		testChild(4, "tool_use", "root", `,"tool_use_id":"nested","name":"Task"`),
		testChild(5, "assistant_delta", "nested", `,"text":"closed text"`),
		testChild(6, "tool_use", "nested", `,"tool_use_id":"done","name":"Read"`),
		testChild(7, "tool_result", "nested", `,"tool_use_id":"done","is_error":false`),
		testChild(8, "tool_use", "nested", `,"tool_use_id":"pending","name":"Read"`),
		testChild(9, "assistant_delta", "nested", `,"text":"unfinished"`),
		testMain(10, "tool_result", `,"tool_use_id":"root","is_error":false`),
		testChild(11, "assistant_delta", "nested", `,"text":" late"`),
		testMain(12, "background_task_started", `,"task_id":"task","tool_call_id":"root"`),
		testMain(13, "background_task_updated", `,"task_id":"task","status":"completed"`),
		testDurable(14, "agent_ended_with_session", `,"tool_call_id":"nested","cause":"child_exit"`),
		testDurable(15, "agent_call_result", `,"tool_call_id":"nested","parent_tool_call_id":"other","status":"completed"`),
		testChild(16, "tool_result", "other", `,"tool_use_id":"done","is_error":false`),
	}
	for i := 0; i < 32; i++ {
		entries = append(entries,
			testMain(uint64(len(entries)+1), "background_task_updated", fmt.Sprintf(`,"task_id":"task","summary":"update%d"`, i)),
			testChild(uint64(len(entries)+2), "tool_denied", "other", `,"tool_use_id":"done"`))
	}
	for _, chunk := range []int{1, 7, len(entries)} {
		t.Run(fmt.Sprint(chunk), func(t *testing.T) {
			f, source := testReferenceFold(entries)
			legacy := New("a")
			f.captureSet = make(map[int]struct{})
			var capture publicationCapture
			var fields int
			for start := 0; start < len(entries); start += chunk {
				source.chunk = entries[start:min(start+chunk, len(entries))]
				got, err := f.FeedChanges(source.chunk)
				want, legacyErr := legacy.FeedChanges(source.chunk)
				if err != nil || legacyErr != nil || !reflect.DeepEqual(got, want) || !reflect.DeepEqual(f.Items(), legacy.Items()) || !reflect.DeepEqual(f.touches, legacy.touches) || !reflect.DeepEqual(f.published, legacy.published) || !reflect.DeepEqual(f.publicVisible, legacy.publicVisible) || f.suppressed != legacy.suppressed || f.unprovenThrough != legacy.unprovenThrough {
					t.Fatalf("feed at %d differs: %v / %v", start, err, legacyErr)
				}
				_, _, err = capture.capture(f)
				if err != nil {
					t.Fatal(err)
				}
				published, err := renderShared(detachPublication(capture.slots), capture.recipes, source, f.items)
				if err != nil || !reflect.DeepEqual(published, legacy.Items()) {
					t.Fatal("captured publication differs", err)
				}
				if chunk == 1 && start >= 6 {
					for _, index := range []int{3, 4} { // closed text and independently finished Read
						n := testChildMarkerShape(t, f.children[index])
						if fields == 0 {
							fields = n
						}
						if n != fields || n > 16 {
							t.Fatal("final marker grew", n, fields)
						}
					}
					if start == 9 || start == 10 {
						testChildItem(t, f, 4, 2, "agent", "running", false)
						testChildItem(t, f, 8, 4, "tool_call", "interrupted", false)
						testChildItem(t, f, 9, 4, "assistant_message", "interrupted", false)
					}
					if start == 11 {
						testChildItem(t, f, 4, 2, "agent", "running", true)
						testChildItem(t, f, 8, 4, "tool_call", "running", true)
						testChildItem(t, f, 9, 4, "assistant_message", "running", true)
					}
					if start >= 13 {
						testChildMarkerShape(t, f.children[2])
						parent := uint64(2)
						if start >= 14 {
							parent = 3
						}
						testChildItem(t, f, 4, parent, "agent", "ended_with_session", false)
						if f.items[2].EndedOrder != 14 {
							t.Fatal("independent first ending changed")
						}
					}
					if start == 9 || start == 10 || start == 11 {
						for _, index := range []int{2, 5, 6} {
							if f.children[index].childSnapshot == nil {
								t.Fatal("reversible child lost intrinsic snapshot", index)
							}
						}
					}
				}
				testNoLifecyclePayload(t, f)
			}
		})
	}
}

func TestReferenceChildMarkerIdentity(t *testing.T) {
	var entries []history.Entry
	for group := 0; group < 5; group++ {
		part := []history.Entry{
			testDurable(1, "agent_call_observed", `,"tool_call_id":"a","tool":"Agent"`),
			testMain(2, "tool_use", `,"tool_use_id":"a","name":"Agent"`),
			testMain(3, "tool_use", `,"tool_use_id":"b","name":"Task"`),
			testMain(4, "tool_result", `,"tool_use_id":"c","is_error":false`),
			testChild(5, "tool_use", "a", `,"tool_use_id":"c","name":"Read"`),
			testChild(6, "tool_denied", "b", `,"tool_use_id":"c"`),
		}
		if group >= 3 {
			part = part[1:]
		}
		for _, e := range part {
			e.ID = uint64(len(entries) + 1)
			if group < 3 {
				e.Session = testSource("claude", "s1")
				if group == 1 {
					e.Session = testSource("claude", "s2")
				}
				if group == 2 {
					e.Payload = []byte(strings.ReplaceAll(string(e.Payload), "11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222"))
				}
			}
			entries = append(entries, e)
		}
		if group == 3 {
			entries = append(entries, testDivider(uint64(len(entries)+1), "operator_reset", "s", "n", "claude", "claude"))
		}
	}
	for _, chunk := range []int{1, 5, len(entries)} {
		f, source := testReferenceFold(entries)
		legacy := New("a")
		for i := 0; i < len(entries); i += chunk {
			source.chunk = entries[i:min(i+chunk, len(entries))]
			got, err := f.FeedChanges(source.chunk)
			want, legacyErr := legacy.FeedChanges(source.chunk)
			if err != nil || legacyErr != nil || !reflect.DeepEqual(got, want) || !reflect.DeepEqual(f.touches, legacy.touches) {
				t.Fatal("scoped final child differs", i, err, legacyErr)
			}
		}
		if len(f.children) != 5 {
			t.Fatal("identity groups merged", len(f.children))
		}
		for _, child := range f.children {
			testChildMarkerShape(t, child)
		}
	}
}

func TestReferenceFinalChildReachability(t *testing.T) {
	entries := []history.Entry{
		testMain(1, "tool_use", `,"tool_use_id":"outer","name":"Agent"`),
		testChild(2, "tool_use", "missing", `,"tool_use_id":"root","name":"Agent"`),
		testChild(3, "tool_use", "root", `,"tool_use_id":"read","name":"Read"`),
		testChild(4, "tool_result", "root", `,"tool_use_id":"read","is_error":false`),
		testChild(5, "tool_result", "outer", `,"tool_use_id":"root","is_error":false`),
	}
	f, source := testReferenceFold(entries)
	legacy := New("a")
	for i := range entries {
		source.chunk = entries[i : i+1]
		got, err := f.FeedChanges(source.chunk)
		want, legacyErr := legacy.FeedChanges(source.chunk)
		if err != nil || legacyErr != nil || !reflect.DeepEqual(got, want) || !reflect.DeepEqual(f.touches, legacy.touches) || !reflect.DeepEqual(f.publicVisible, legacy.publicVisible) {
			t.Fatal("final child reachability differs", i, err, legacyErr)
		}
		if i == 3 && len(f.Items()) != 1 {
			t.Fatal("unresolved subtree escaped")
		}
		if i >= 3 {
			testChildMarkerShape(t, f.children[2])
		}
	}
	testChildItem(t, f, 3, 2, "tool_call", "done", false)
	if f.items[2].Rev != 4 || len(f.Items()) != 3 {
		t.Fatal("parent repair revised final child or lost publication")
	}
}

func TestReferenceFinalChildTextEnrichment(t *testing.T) {
	entries := []history.Entry{
		testMain(1, "tool_use", `,"tool_use_id":"a","name":"Agent"`),
		testChild(2, "turn_end", "a", `,"stop_reason":"end_turn"`),
	}
	for i := 0; i < 32; i++ {
		entries = append(entries, testChild(uint64(len(entries)+1), "assistant_delta", "a", `,"text":"`+strings.Repeat("x", i*128)+`"`))
	}
	for _, chunk := range []int{1, 7, len(entries)} {
		f, source := testReferenceFold(entries)
		legacy := New("a")
		for i := 0; i < len(entries); i += chunk {
			source.chunk = entries[i:min(i+chunk, len(entries))]
			got, err := f.FeedChanges(source.chunk)
			want, legacyErr := legacy.FeedChanges(source.chunk)
			if err != nil || legacyErr != nil || !reflect.DeepEqual(got, want) || !reflect.DeepEqual(f.touches, legacy.touches) {
				t.Fatal("final text enrichment differs", i, err, legacyErr)
			}
			for _, child := range f.children {
				testChildMarkerShape(t, child)
			}
		}
		if len(f.recipes[3].text) != 32 {
			t.Fatal("public text recipe was compacted")
		}
	}
}
