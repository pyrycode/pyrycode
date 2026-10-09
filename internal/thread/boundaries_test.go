package thread

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"

	"github.com/pyrycode/pyrycode/internal/history"
)

const testAt = "2026-01-01T00:00:00Z"

func testDivider(id uint64, cause, prev, next, prevAgent, nextAgent string) history.Entry {
	return testEntry(id, "session_divider", fmt.Sprintf(`{"cause":%q,"occurred_at":%q,"previous_session_id":%q,"new_session_id":%q,"previous_agent":%q,"next_agent":%q}`, cause, testAt, prev, next, prevAgent, nextAgent))
}
func testTransition(id uint64, reason, prev, next string) history.Entry {
	return testEntry(id, "session_transition", fmt.Sprintf(`{"reason":%q,"occurred_at":%q,"previous_session_id":%q,"new_session_id":%q}`, reason, testAt, prev, next))
}
func TestBoundaryPairing(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		cause, reason, next string
		shown               bool
	}{
		{"operator_reset", "clear", "n", true}, {"claude_clear", "clear", "n", true}, {"agent_switch", "clear", "n", true},
		{"idle_sleep", "idle_evict", "", false}, {"capacity_eviction", "idle_evict", "", true},
		{"recovery", "clear", "n", true}, {"workspace_change", "workspace_change", "n", true}, {"daemon_restart", "clear", "", false},
	} {
		t.Run(tc.cause, func(t *testing.T) {
			f := New("a")
			raw := testDivider(1, tc.cause, "s", tc.next, "claude", "codex")
			if tc.cause == "operator_reset" {
				raw.Payload = append(raw.Payload[:len(raw.Payload)-1], []byte(`,"reset_handoff_outcome":"written"}`)...)
			}
			testFeed(t, f, raw)
			before := f.Items()[0]
			next := tc.next
			if tc.reason == "idle_evict" {
				next = "s"
			}
			if tc.cause == "daemon_restart" {
				next = "n"
			}
			testFeed(t, f, testTransition(2, tc.reason, "s", next))
			matched := tc.cause != "recovery" && tc.cause != "workspace_change" && tc.cause != "daemon_restart"
			wantLen := 2
			if matched {
				wantLen = 1
			}
			if len(f.Items()) != wantLen || !reflect.DeepEqual(f.Items()[0], before) || before.Shown != tc.shown || before.Session != "s" || before.Agent != "claude" || f.Version() != 2 {
				t.Fatalf("wrong boundary: %#v", f.Items())
			}
		})
	}
	f := New("a")
	testFeed(t, f, testDivider(1, "claude_clear", "s", "n", "", ""), testDivider(2, "claude_clear", "s", "n", "", ""))
	first := testTransition(3, "clear", "s", "n")
	first.Payload = json.RawMessage(`{"reason":"clear","occurred_at":"2025-12-31T19:00:00-05:00","previous_session_id":"s","new_session_id":"n"}`)
	testFeed(t, f, first, testTransition(4, "clear", "s", "n"), testTransition(5, "clear", "s", "n"))
	if len(f.Items()) != 3 || f.Items()[2].ID != 5 || f.legacyScope != 3 {
		t.Fatalf("not one-to-one: %#v", f)
	}
	for _, raw := range []string{
		`{"reason":"clear","occurred_at":"2026-01-02T00:00:00Z","previous_session_id":"s","new_session_id":"n"}`,
		`{"reason":"clear","occurred_at":"2026-01-01T00:00:00Z","previous_session_id":"other","new_session_id":"n"}`,
		`{"reason":"clear","occurred_at":"2026-01-01T00:00:00Z","previous_session_id":"s","new_session_id":"other"}`,
	} {
		f := New("a")
		testFeed(t, f, testDivider(1, "claude_clear", "s", "n", "", ""), testEntry(2, "session_transition", raw))
		if len(f.Items()) != 2 {
			t.Fatal("paired mismatched occurrence or IDs")
		}
	}
}

func TestBoundaryAttribution(t *testing.T) {
	t.Parallel()
	f := New("a")
	testFeed(t, f, testMessage(1), testDivider(2, "operator_reset", "old", "next", "claude", ""), testMessage(3))
	legacy := testTransition(4, "clear", "old", "next")
	legacy.Session = testSource("codex", "next")
	testFeed(t, f, legacy, testMessage(5))
	explicit := testMessage(6)
	explicit.Session = testSource("none", "")
	testFeed(t, f, explicit)
	tagged := testMessage(7)
	tagged.Session = testSource("claude", "explicit")
	testFeed(t, f, tagged, testMessage(8))
	testFeed(t, f, testDivider(9, "agent_switch", "next", "new", "codex", "claude"), testMessage(10))
	testFeed(t, f, testDivider(11, "capacity_eviction", "new", "", "claude", ""))
	eviction := testTransition(12, "idle_evict", "new", "new")
	eviction.Session = testSource("claude", "new")
	testFeed(t, f, eviction, testMessage(13), testDivider(14, "recovery", "new", "new", "", "codex"), testMessage(15))
	restart := testDivider(16, "daemon_restart", "", "", "", "")
	restart.Session = testSource("none", "")
	testFeed(t, f, restart, testMessage(17))
	want := map[uint64]struct {
		session, agent string
		none           bool
	}{1: {}, 2: {"old", "claude", false}, 3: {"next", "", false}, 5: {"next", "codex", false}, 6: {"", "", true}, 7: {"explicit", "claude", false}, 8: {"next", "codex", false}, 10: {"new", "claude", false}, 13: {}, 15: {"new", "codex", false}, 16: {"", "", true}, 17: {}}
	for _, item := range f.Items() {
		if w, ok := want[item.ID]; ok && (item.Session != w.session || item.Agent != w.agent || item.NoChild != w.none) {
			t.Fatalf("id %d got %#v want %#v", item.ID, item, w)
		}
	}
	if f.legacyScope != 4 {
		t.Fatalf("restart split legacy scope: %d", f.legacyScope)
	}
	// A delayed old pair must not replace the newer boundary's successor.
	g := New("a")
	old := testTransition(3, "clear", "s", "n")
	old.Session = testSource("codex", "n")
	testFeed(t, g, testDivider(1, "claude_clear", "s", "n", "", ""), testDivider(2, "agent_switch", "n", "final", "", "claude"), old, testMessage(4))
	if got := g.Items()[2]; got.Session != "final" || got.Agent != "claude" || g.legacyScope != 2 {
		t.Fatalf("old pair leaked: %#v", got)
	}
	// Standalone transitions supply fallback only when they have a successor.
	for _, reason := range []string{"clear", "idle_evict", "recovered", "workspace_change"} {
		g := New("a")
		e := testTransition(1, reason, "s", "n")
		e.Session = testSource("codex", "n")
		testFeed(t, g, e, testMessage(2))
		want := ""
		if reason != "idle_evict" {
			want = "n"
		}
		if g.Items()[1].Session != want || g.Items()[0].Shown != (reason != "idle_evict") {
			t.Fatalf("legacy %s: %#v", reason, g.Items())
		}
	}
}

func TestBoundaryMalformedNeutrality(t *testing.T) {
	t.Parallel()
	for _, bad := range []string{
		`{"cause":"operator_reset","occurred_at":"bad","previous_session_id":"s","new_session_id":"n"}`,
		`{"cause":"operator_reset","occurred_at":"2026-01-01T00:00:00Z","previous_session_id":3,"new_session_id":"n"}`,
		`{"conversation_id":"foreign","cause":"operator_reset","occurred_at":"2026-01-01T00:00:00Z","previous_session_id":"s","new_session_id":"n"}`,
	} {
		f := New("a")
		testFeed(t, f, testDivider(1, "agent_switch", "before", "current", "claude", "codex"), testEntry(2, "session_divider", bad), testMessage(3), testTransition(4, "clear", "s", "n"))
		if f.Items()[1].Session != "current" || f.Items()[1].Agent != "codex" || len(f.Items()) != 3 || f.legacyScope != 2 {
			t.Fatalf("malformed boundary affected joins: %#v", f)
		}
	}
	// Visibility belongs to the raw boundary, not its companion.
	f := New("a")
	raw := testDivider(1, "operator_reset", "s", "n", "claude", "codex")
	shown := false
	raw.Shown = &shown
	companion := testTransition(2, "clear", "s", "n")
	yes := true
	companion.Shown = &yes
	companion.Session = testSource("codex", "n")
	testFeed(t, f, raw, companion)
	if got := f.Items()[0]; len(f.Items()) != 1 || got.Shown || got.Rev != 1 || got.Agent != "claude" {
		t.Fatalf("raw boundary changed: %#v", got)
	}
	// Saved prompt ownership survives a later boundary even without agent metadata.
	answer := testEntry(3, "prompt_answered", `{"correlation_id":"q","session_id":"old","decision":"deny","context":{"tool":"Read","class":"file"}}`)
	testFeed(t, f, answer, testMessage(4))
	if f.Items()[1].Session != "old" || f.Items()[1].Agent != "" || f.Items()[2].Session != "n" || f.Items()[2].Agent != "codex" {
		t.Fatal("saved answer misattributed")
	}
	// Metadata-free eviction retains its recorded ID but supplies no successor.
	g := New("a")
	testFeed(t, g, testTransition(1, "idle_evict", "evicted", "evicted"), testMessage(2))
	if g.Items()[0].Session != "evicted" || g.Items()[0].Agent != "" || g.Items()[1].Session != "" {
		t.Fatal("eviction provenance invented or lost")
	}
}
