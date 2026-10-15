package thread

import "testing"

func TestUnloadedLastShownVersionGuards(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"unloaded", "retiring", "rebuilding", "unavailable", "usable", "closed"} {
		t.Run(mode, func(t *testing.T) {
			s, h := testThreadStore(t)
			testCheckpointLoad(t, s, testStoreA, 0)
			if err := s.Unload(testStoreA); err != nil {
				t.Fatal(err)
			}
			if _, err := h.LatestEntryID(testStoreA); err != nil {
				t.Fatal(err)
			}
			s.mu.Lock()
			if mode == "closed" {
				s.closed = true
			}
			if mode != "unloaded" && mode != "closed" {
				state := StateUsable
				if mode == "rebuilding" {
					state = StateRebuilding
				}
				if mode == "unavailable" {
					state = StateUnavailable
				}
				s.workers[testStoreA] = &conversationWorker{retiring: mode == "retiring", snapshot: Snapshot{State: state}}
			}
			s.mu.Unlock()
			t.Cleanup(func() {
				s.mu.Lock()
				delete(s.workers, testStoreA)
				s.closed = false
				s.mu.Unlock()
			})
			s.replace = func(string, string) error { t.Error("metadata lookup wrote certificate"); return nil }
			s.queryCopies = func(int) { t.Error("metadata lookup copied items") }
			s.queryWork = func(bool) { t.Error("metadata lookup traversed items") }
			got, ok := s.UnloadedLastShownVersion(testStoreA)
			if got != 0 || ok != (mode == "unloaded") {
				t.Fatalf("watermark = %d, %t", got, ok)
			}
		})
	}
}
