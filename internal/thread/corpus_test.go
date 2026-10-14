package thread

import (
	"reflect"
	"testing"
)

func TestFoldCorpus(t *testing.T) {
	small, large := testFoldCorpus(101), testFoldCorpus(30000)
	if len(large) != 30000 || !reflect.DeepEqual(small, large[:101]) {
		t.Fatal("count scaling changed prefix")
	}
	for _, e := range large {
		if len(e.Payload) > 1024 {
			t.Fatal("unbounded payload", e.ID)
		}
	}
	f := New("a")
	for _, e := range small {
		testFeed(t, f, e)
		items := f.Items()
		byID := make(map[uint64]Item)
		for _, item := range items {
			byID[item.ID] = item
		}
		for _, item := range items {
			if item.Parent != 0 && byID[item.Parent].Kind != "agent" {
				t.Fatal("non-agent parent", item)
			}
		}
		if e.ID == 10 && !byID[9].Active {
			t.Fatal("missing nested child")
		}
		if e.ID == 11 && byID[9].Active {
			t.Fatal("ancestor closure lost")
		}
		if e.ID == 12 && !byID[9].Active {
			t.Fatal("late link did not reopen child")
		}
		if e.ID == 15 && byID[14].Parent != 5 {
			t.Fatal("late parent enrichment lost")
		}
		if e.ID == 23 && byID[21].Order != 22 {
			t.Fatal("queued delivery lost")
		}
		if e.ID >= 23 {
			if _, ok := byID[22]; ok {
				t.Fatal("delivery row not suppressed")
			}
		}
	}
}

func BenchmarkFoldCorpus30000(b *testing.B) {
	entries := testFoldCorpus(30000)
	b.ReportAllocs()
	b.ResetTimer()
	var f *Fold
	for i := 0; i < b.N; i++ {
		f = New("a")
		if err := f.Feed(entries); err != nil {
			b.Fatal(err)
		}
	}
	b.StopTimer()
	b.ReportMetric(float64(len(entries)), "entries/op")
	b.ReportMetric(float64(len(f.Items())), "items/op")
}
