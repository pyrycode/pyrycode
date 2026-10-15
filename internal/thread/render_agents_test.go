package thread

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"testing"

	"github.com/pyrycode/pyrycode/internal/history"
)

func TestReferenceLifecycleGuard(t *testing.T) {
	seen := map[reflect.Type]bool{}
	var walk func(reflect.Type, string)
	walk = func(typ reflect.Type, path string) {
		if typ == reflect.TypeFor[json.RawMessage]() {
			t.Errorf("retained lifecycle payload: %s", path)
			return
		}
		// Public DTOs and recipes have explicit runtime checks below. Legacy
		// compatibility state is allowed only in New, never production folds.
		if typ == reflect.TypeFor[Item]() || typ == reflect.TypeFor[contentRecipe]() || seen[typ] {
			return
		}
		seen[typ] = true
		switch typ.Kind() {
		case reflect.Pointer, reflect.Slice, reflect.Array:
			walk(typ.Elem(), path+"[]")
		case reflect.Map:
			walk(typ.Key(), path+".key")
			walk(typ.Elem(), path+".value")
		case reflect.Struct:
			for i := 0; i < typ.NumField(); i++ {
				field := typ.Field(i)
				if typ == reflect.TypeFor[Fold]() && (field.Name == "source" || field.Name == "legacyOutcomes") {
					continue // Bounded source and legacy-only compatibility boundaries.
				}
				if typ == reflect.TypeFor[childState]() && field.Name == "legacyEnding" {
					continue
				}
				walk(field.Type, path+"."+field.Name)
			}
		}
	}
	walk(reflect.TypeFor[Fold](), "Fold")
}

func testNoLifecyclePayload(t *testing.T, f *Fold) {
	t.Helper()
	if len(f.legacyOutcomes) != 0 {
		t.Fatal("production used legacy compatibility storage")
	}
	for i, item := range f.items {
		if len(item.Content) != 0 {
			t.Fatalf("item %d retained Content", item.ID)
		}
		if child := f.children[i]; child != nil && (len(child.base.Content) != 0 || len(child.previous.Content) != 0 || child.legacyEnding != nil) {
			t.Fatal("child intrinsic/previous state retained payload")
		}
	}
	checkRecipe := func(r contentRecipe) {
		for key, field := range r.fields {
			if len(field.value) == 0 {
				continue
			}
			var scalar any
			if json.Unmarshal(field.value, &scalar) != nil {
				t.Fatal("invalid recipe constant", key)
			}
			switch scalar.(type) {
			case string, float64, bool, nil:
			default:
				t.Fatal("recipe retained a payload literal", key)
			}
		}
	}
	for _, r := range f.recipes {
		checkRecipe(r)
	}
	for _, child := range f.children {
		checkRecipe(child.baseRecipe)
		checkRecipe(child.previousRecipe)
	}
}

func TestReferenceAgentLifecycle(t *testing.T) {
	entries := testFoldCorpus(100)
	entries[3].Payload = append(entries[3].Payload[:len(entries[3].Payload)-1], []byte(`,"future":[1, 2],"ended_order":999}`)...)
	f, source := testReferenceFold(entries)
	legacy := New("a")
	for i, e := range entries {
		source.chunk = entries[i : i+1]
		want, err := legacy.FeedChanges(source.chunk)
		if err != nil {
			t.Fatal(err)
		}
		got, err := f.FeedChanges(source.chunk)
		if err != nil || !reflect.DeepEqual(got, want) || !reflect.DeepEqual(f.touches, legacy.touches) {
			t.Fatalf("entry %d changed observation/touches: %v", e.ID, err)
		}
		testNoLifecyclePayload(t, f)
		source.remember(source.chunk)
		source.chunk = nil
		for id := uint64(1000); id < 1300; id++ {
			source.remember([]history.Entry{testEntry(id, "ignored", `{}`)})
		}
		if !reflect.DeepEqual(f.Items(), legacy.Items()) {
			t.Fatal("evicted lifecycle differs")
		}
	}
}

func TestReferenceAgentDependencies(t *testing.T) {
	entries := testEndingEntries()
	for _, missing := range []uint64{1, 4, 6, 7} {
		for _, failure := range []string{"missing", "unreadable"} {
			t.Run(fmt.Sprint(missing, failure), func(t *testing.T) {
				f, source := testReferenceFold(entries)
				testFeed(t, f, entries...)
				want := f.Items()
				for id := uint64(100); id < 400; id++ {
					source.remember([]history.Entry{testEntry(id, "ignored", `{}`)})
				}
				lookup := source.lookup
				source.lookup = func(ids []uint64) ([]history.Entry, error) {
					if !slices.Contains(ids, missing) {
						t.Fatal("dependency was not requested", missing)
					}
					if failure == "unreadable" {
						return nil, errors.New("private payload/path")
					}
					out, err := lookup(ids)
					return slices.DeleteFunc(out, func(e history.Entry) bool { return e.ID == missing }), err
				}
				if got, err := f.Render(); got != nil || err != ErrUnavailable {
					t.Fatal("partial lifecycle escaped", err)
				}
				if o := f.Observe(); o.State != StateUnavailable || o.Err != ErrUnavailable || o.Items != nil || o.Version != 0 {
					t.Fatal("dependency failure disclosed state")
				}
				source.lookup = lookup
				if !reflect.DeepEqual(f.Items(), want) {
					t.Fatal("repair changed lifecycle")
				}
			})
		}
	}
}

func TestStoreReferenceAgentRetry(t *testing.T) {
	for _, missing := range []uint64{1, 4, 6, 7} {
		t.Run(fmt.Sprint(missing), func(t *testing.T) {
			testStoreReferenceRetry(t, testEndingEntries(), missing, "replay")
		})
	}
}

func TestReferenceRepeatedAgentReport(t *testing.T) {
	entries := []history.Entry{
		testMain(1, "tool_use", `,"tool_use_id":"c","name":"Agent"`),
		testMain(2, "background_task_started", `,"task_id":"task","tool_call_id":"c"`),
		testMain(3, "background_task_updated", `,"task_id":"task","summary":"same <&>"`),
		testMain(4, "background_task_updated", `, "task_id":"task", "summary":"same <&>"`),
	}
	f, source := testReferenceFold(entries)
	legacy := New("a")
	for i := range entries {
		source.chunk = entries[i : i+1]
		got, err := f.FeedChanges(source.chunk)
		want, legacyErr := legacy.FeedChanges(source.chunk)
		if err != nil || legacyErr != nil || !reflect.DeepEqual(got, want) || !reflect.DeepEqual(f.touches, legacy.touches) {
			t.Fatal("identical report changed revisions/public touches", entries[i].ID)
		}
	}
}
