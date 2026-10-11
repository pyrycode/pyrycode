package main

import (
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/history"
)

func TestMemoryTranscriptReplyPartitions(t *testing.T) {
	for _, tc := range []struct {
		name       string
		entries    int
		exhaustive bool
		want       []int
	}{
		{"one entry", 1, false, []int{0}},
		{"two entries", 2, false, []int{0, 1}},
		{"every single cut", 4, false, []int{0, 7, 1, 2, 4}},
		{"full proof", 4, true, []int{0, 1, 2, 3, 4, 5, 6, 7}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := testReplyPartitions(tc.entries, tc.exhaustive); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("partitions = %v, want %v", got, tc.want)
			}
		})
	}
	const entries = 12
	partitions := testReplyPartitions(entries, false)
	if len(partitions) != entries+1 {
		t.Fatalf("ordinary coverage has %d partitions, want %d", len(partitions), entries+1)
	}
	seen := make(map[int]bool)
	for _, split := range partitions {
		if seen[split] {
			t.Fatalf("duplicate split=%b", split)
		}
		seen[split] = true
	}
	if !seen[0] || !seen[(1<<(entries-1))-1] {
		t.Fatal("missing whole-history or single-entry replay")
	}
	for cut := 0; cut < entries-1; cut++ {
		if !seen[1<<cut] {
			t.Fatalf("missing two-chunk replay at cut %d", cut+1)
		}
	}
}

func testReplyPartitions(entries int, exhaustive bool) []int {
	if exhaustive {
		partitions := make([]int, 1<<(entries-1))
		for split := range partitions {
			partitions[split] = split
		}
		return partitions
	}
	partitions := []int{0}
	all := (1 << (entries - 1)) - 1
	if all != 0 {
		partitions = append(partitions, all)
	}
	for cut := 0; cut < entries-1; cut++ {
		if split := 1 << cut; split != all {
			partitions = append(partitions, split)
		}
	}
	return partitions
}

func TestVerifierRepeatedMainLauncher(t *testing.T) {
	for _, tool := range []string{"Agent", "Task"} {
		for _, source := range []string{"claude", "legacy", "codex"} {
			for _, interrupted := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/source=%s/interrupted=%t", tool, source, interrupted), func(t *testing.T) {
					entries := testReplyEntries(replyUser,
						fmt.Sprintf(`tool_use {"turn_id":"old","tool_use_id":"same","name":%q}`, tool),
						`turn_end {"turn_id":"old","stop_reason":"end_turn"}`, replyUser,
						fmt.Sprintf(`tool_use {"turn_id":"t","tool_use_id":"same","name":%q}`, tool))
					testReplyLauncherSource(entries, source)
					testReplyLauncherReplay(t, entries, 5, 4, interrupted)
				})
			}
		}
	}
}

// Ordinary replay covers whole history, single entries and every single cut.
// PYRY_TRANSCRIPT_EXHAUSTIVE=1 exercises every partition with the same assertions.
// Public rows do not certify an opening; the immutable causal identity and first
// terminal must agree with publication at every selected intermediate cut.
func testReplyLauncherReplay(t *testing.T, prefix []history.Entry, opening, user uint64, interrupted bool) {
	t.Helper()
	entries := append([]history.Entry(nil), prefix...)
	textID := uint64(len(entries) + 1)
	terminal := replyEnd
	if interrupted {
		terminal = fmt.Sprintf(`main_turn_interrupted {"turn_id":"t","turn_opened_entry_id":%d,"cause":"restart","occurred_at":"2026-01-01T00:00:00Z"}`, opening)
	}
	for _, fact := range []string{replyText, terminal, replyEnd} {
		e := testReplyEntries(fact)[0]
		e.ID = uint64(len(entries) + 1)
		e.TS = entries[0].TS.Add(time.Duration(e.ID-1) * time.Second)
		e.Session = prefix[len(prefix)-1].Session
		entries = append(entries, e)
	}
	exhaustive := os.Getenv("PYRY_TRANSCRIPT_EXHAUSTIVE") == "1"
	partitions, replayed, prefixes := 0, 0, 0
	for _, split := range testReplyPartitions(len(entries), exhaustive) {
		if t.Run(fmt.Sprintf("split=%b", split), func(t *testing.T) {
			w := newMemoryTranscriptReader(nil, "chat")
			start := 0
			for end := 1; end <= len(entries); end++ {
				if end != len(entries) && split&(1<<(end-1)) == 0 {
					continue
				}
				testMemoryMust(t, w.feed(entries[start:end]))
				replayed += end - start
				prefixes++
				start = end
				if uint64(end) >= textID {
					st := w.replies.text[textID]
					if st == nil || st.opening != opening || st.user != user || w.replies.openings[opening] != st {
						t.Fatalf("accepted opening/user changed: %+v; want opening %d user %d", st, opening, user)
					}
					if uint64(end) > textID && (st.terminal != textID+1 || st.completed == interrupted) {
						t.Fatalf("first terminal changed: %+v; want terminal %d", st, textID+1)
					}
					found := false
					for _, item := range w.fold.Items() {
						if item.ID == textID && item.Kind == "assistant_message" && item.Parent == 0 {
							found = true
							if item.Active != (uint64(end) == textID) {
								t.Fatal("fold main text closure disagrees with first terminal")
							}
						}
					}
					if !found {
						t.Fatal("fold omitted main text")
					}
				}
				files := w.files(conversations.Conversation{ID: "chat"})
				if uint64(end) < user && len(files) == 0 {
					continue
				}
				body := testReplyBody(t, files, "question")
				published := uint64(end) > textID && !interrupted
				if strings.Contains(body, "Speaker: assistant") != published {
					t.Fatal("reused call changed first-terminal withholding", body)
				}
				if uint64(end) >= user {
					lastText := user
					if published {
						lastText = textID
						if !strings.Contains(body, fmt.Sprintf("Message: chat/%d\nTimestamp: %s", textID, entries[textID-1].TS.UTC().Format(time.RFC3339Nano))) || !strings.Contains(body, "answer") {
							t.Fatal("completion lost stable first-text identity/time", body)
						}
					}
					if !strings.Contains(body, fmt.Sprintf("Last delivered entry: %d\nLast text entry: %d\n", user, lastText)) {
						t.Fatal("nontext facts changed delivery/text boundaries", body)
					}
				}
			}
		}) {
			partitions++
		}
	}
	t.Logf("replay-work exhaustive=%t interrupted=%t entries-per-partition=%d partitions=%d entries=%d prefixes=%d", exhaustive, interrupted, len(entries), partitions, replayed, prefixes)
}

func testReplyLauncherSource(entries []history.Entry, source string) {
	for i := range entries {
		if source == "legacy" {
			entries[i].Session = nil
		} else {
			entries[i].Session.Kind = source
		}
	}
}

func TestMemoryTranscriptReplyReusedLauncherOwnership(t *testing.T) {
	for _, tool := range []string{"Agent", "Task"} {
		launch := fmt.Sprintf(`tool_use {"turn_id":"old","tool_use_id":"same","name":%q,"parent_tool_call_id":"outer"}`, tool)
		next := fmt.Sprintf(`tool_use {"turn_id":"t","tool_use_id":"same","name":%q}`, tool)
		oldEnd := `turn_end {"turn_id":"old","stop_reason":"end_turn"}`
		for _, source := range []string{"claude", "legacy", "codex"} {
			for _, tc := range []struct {
				name          string
				facts         []string
				opening, user uint64
				change        func([]history.Entry)
			}{
				{name: "retained child call", facts: []string{replyUser, launch, oldEnd, replyUser, next}, opening: 6, user: 4},
				{name: "ordinary child stays turn local", facts: []string{replyUser, strings.Replace(launch, fmt.Sprintf("%q", tool), `"Read"`, 1), oldEnd, replyUser, next}, opening: 5, user: 4},
				{name: "other session", facts: []string{replyUser, launch, oldEnd, replyUser, next}, opening: 5, user: 4, change: func(e []history.Entry) { e[1].Session = &history.SessionProvenance{Kind: "claude", SessionID: "B"} }},
				{name: "tagged legacy separation", facts: []string{replyUser, launch, oldEnd, replyUser, next}, opening: 5, user: 4, change: func(e []history.Entry) {
					if source == "legacy" {
						e[1].Session = &history.SessionProvenance{Kind: "claude", SessionID: "A"}
					} else {
						e[1].Session = nil
					}
				}},
				{name: "other agent", facts: []string{replyUser, launch, oldEnd, replyUser, next}, opening: 5, user: 4, change: func(e []history.Entry) {
					kind := "codex"
					if source == "codex" {
						kind = "claude"
					}
					e[1].Session = &history.SessionProvenance{Kind: kind, SessionID: "A"}
				}},
				{name: "fresh producer lifetime", facts: []string{replyUser, replyTaskLifetime, launch, oldEnd, replyUser, strings.Replace(replyTaskLifetime, "11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222", 1), next}, opening: 7, user: 5},
				{name: "fresh boundary scope", facts: []string{replyUser, launch, oldEnd, `session_divider {"cause":"idle_sleep","previous_session_id":"A","occurred_at":"2026-01-01T00:00:00Z"}`, replyUser, next}, opening: 6, user: 5},
				{name: "fresh opening generation", facts: []string{replyUser, next, replyEnd, replyUser, replyOpen, next}, opening: 5, user: 4},
				{name: "launch without new agent row", facts: []string{replyUser, strings.Replace(next, `"t"`, `"old"`, 1), oldEnd, replyUser, strings.TrimSuffix(next, "}") + `,"occurred_at":null}`}, opening: 5, user: 4},
			} {
				for _, interrupted := range []bool{false, true} {
					t.Run(fmt.Sprintf("%s/source=%s/%s/interrupted=%t", tool, source, tc.name, interrupted), func(t *testing.T) {
						entries := testReplyEntries(tc.facts...)
						testReplyLauncherSource(entries, source)
						if tc.change != nil {
							tc.change(entries)
						}
						opening := tc.opening
						if tc.name == "retained child call" && source == "codex" {
							opening = 5
						}
						testReplyLauncherReplay(t, entries, opening, tc.user, interrupted)
					})
				}
			}
		}
	}
}
