package thread

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/history"
)

func TestAssistantMessageTimeAfterReopen(t *testing.T) {
	s, h := testThreadStore(t)
	post := testMain(1, "assistant_delta", `,"text":"cron channel post"`)
	post.Session = testSource("none", "")
	post.TS = time.Date(2026, 10, 16, 6, 30, 0, 123000000, time.UTC)
	testStoreAppend(t, h, testStoreA, post, testMain(2, "turn_end", `,"stop_reason":"end_turn","producer":"channel_post"`))
	first := testCheckpointLoad(t, s, testStoreA, 2)
	for pass := 0; pass < 2; pass++ {
		if pass == 1 {
			if err := s.Unload(testStoreA); err != nil {
				t.Fatal(err)
			}
			if got := testCheckpointLoad(t, s, testStoreA, 2); got.Epoch != first.Epoch {
				t.Fatal("clean reopen changed epoch")
			}
		}
		for _, page := range []bool{false, true} {
			var got QueryResult
			var err error
			if page {
				got, err = s.HistoryPage(context.Background(), testStoreA, 3, 10)
			} else {
				got, err = s.NewestWindow(context.Background(), testStoreA, 10)
			}
			if err != nil || got.State != StateUsable {
				t.Fatalf("query: %v, %v", got.State, err)
			}
			found := false
			for _, item := range got.Items {
				if item.Kind != "assistant_message" {
					continue
				}
				found = true
				var c struct {
					TS   time.Time `json:"ts"`
					Text string    `json:"text"`
				}
				if err := json.Unmarshal(item.Content, &c); err != nil {
					t.Fatal(err)
				}
				if !c.TS.Equal(post.TS) || c.Text != "cron channel post" {
					t.Fatalf("reopen=%d page=%v: lost posted message date: %+v", pass, page, c)
				}
			}
			if !found {
				t.Fatal("query omitted the posted message")
			}
		}
	}
}

func TestAssistantMessageRecordedTime(t *testing.T) {
	for _, reference := range []bool{false, true} {
		for _, source := range []*history.SessionProvenance{nil, testSource("claude", "session"), testSource("codex", "session"), testSource("none", "")} {
			t.Run(sourceName(source)+map[bool]string{false: "/inline", true: "/reference"}[reference], func(t *testing.T) {
				first := testMain(1, "assistant_delta", `,"text":"first"`)
				first.TS = time.Date(2026, 10, 16, 9, 30, 0, 123000000, time.FixedZone("offset", 3*60*60))
				entries := []history.Entry{first,
					testMain(2, "assistant_delta", `,"text":" continued"`),
					testMain(3, "tool_use", `,"tool_use_id":"read","name":"Read"`),
					testMain(4, "assistant_delta", `,"text":"second"`),
					testMain(5, "turn_end", `,"stop_reason":"end_turn"`)}
				for i := range entries {
					entries[i].Session = source
				}
				f := New("a")
				if reference {
					f, _ = testReferenceFold(entries)
				}
				for _, e := range entries {
					testFeed(t, f, e)
					items, err := f.Render()
					if err != nil {
						t.Fatal(err)
					}
					for _, item := range items {
						if item.Kind != "assistant_message" {
							continue
						}
						var c struct {
							TS   time.Time `json:"ts"`
							Text string    `json:"text"`
						}
						if err := json.Unmarshal(item.Content, &c); err != nil {
							t.Fatal(err)
						}
						want := entries[item.ID-1].TS
						if !c.TS.Equal(want) {
							t.Fatalf("entry %d, bubble %d: time = %s, want %s", e.ID, item.ID, c.TS, want)
						}
						if item.ID == 1 && e.ID >= 2 && c.Text != "first continued" {
							t.Fatalf("lost streamed text: %q", c.Text)
						}
					}
				}
			})
		}
	}
}

func TestAssistantMessageMissingRecordedTime(t *testing.T) {
	entry := testMain(1, "assistant_delta", `,"text":"legacy"`)
	entry.TS = time.Time{}
	for _, reference := range []bool{false, true} {
		f := New("a")
		if reference {
			f, _ = testReferenceFold([]history.Entry{entry})
		}
		testFeed(t, f, entry)
		items, err := f.Render()
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(items[0].Content, &fields); err != nil {
			t.Fatal(err)
		}
		if _, exists := fields["ts"]; exists {
			t.Fatal("invented a time for an undated entry")
		}
	}
}
