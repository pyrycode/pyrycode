package main

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/history"
)

func TestVerifierRuntimeChildCall(t *testing.T) {
	for _, tool := range []string{"Read", "Agent", "Task"} {
		for _, source := range []string{"claude", "legacy", "codex"} {
			for _, extra := range []string{"", `,"tool_use_id":"child"`} {
				t.Run(fmt.Sprintf("%s/%s/extra=%t", tool, source, extra != ""), func(t *testing.T) {
					entries := testReplyEntries(replyUser,
						fmt.Sprintf(`tool_use {"turn_id":"t","tool_use_id":"child","name":%q,"parent_tool_call_id":"outer"}`, tool),
						replyOpen, replyText,
						fmt.Sprintf(`main_turn_interrupted {"turn_id":"t","turn_opened_entry_id":3,"cause":"restart","occurred_at":"2026-01-01T00:00:00Z"%s}`, extra), replyEnd)
					testReplyLauncherSource(entries, source)
					testReplyRuntimeReplay(t, entries, func(t *testing.T, w *memoryTranscriptReader, end int, body string) {
						if end >= 4 {
							st := w.replies.text[4]
							terminal := uint64(0)
							if end >= 5 {
								terminal = 5
							}
							if st == nil || st.opening != 3 || st.user != 1 || st.terminal != terminal || st.completed {
								t.Fatalf("main runtime interruption lost its lifetime: %+v", st)
							}
						}
						if strings.Contains(body, "Speaker: assistant") || !strings.Contains(body, "Last delivered entry: 1\nLast text entry: 1\n") {
							t.Fatal("conflicting ending released interrupted text", body)
						}
					})
				})
			}
		}
	}
}

func TestVerifierRuntimeOpeningChildCall(t *testing.T) {
	for _, tool := range []string{"Read", "Agent", "Task"} {
		for _, source := range []string{"claude", "legacy", "codex"} {
			for _, extra := range []string{"", `,"tool_use_id":"child"`} {
				t.Run(fmt.Sprintf("%s/%s/extra=%t", tool, source, extra != ""), func(t *testing.T) {
					entries := testReplyEntries(replyUser,
						fmt.Sprintf(`tool_use {"turn_id":"t","tool_use_id":"child","name":%q,"parent_tool_call_id":"outer"}`, tool),
						replyOpen, replyText, replyEnd, replyUser,
						fmt.Sprintf(`main_turn_opened {"turn_id":"t","occurred_at":"2026-01-01T00:00:00Z"%s}`, extra),
						`assistant_delta {"turn_id":"t","text":"unfinished secret"}`, replyEnd)
					testReplyLauncherSource(entries, source)
					testReplyRuntimeReplay(t, entries, func(t *testing.T, w *memoryTranscriptReader, end int, body string) {
						if end >= 8 {
							st, previous := w.replies.text[8], w.replies.text[4]
							terminal := uint64(0)
							if end == 9 {
								terminal = 9
							}
							if st == nil || st == previous || st.opening != 7 || st.user != 6 || st.terminal != terminal || st.completed != (end == 9) {
								t.Fatalf("fresh runtime opening inherited previous completion: %+v", st)
							}
							if previous == nil || previous.opening != 3 || previous.user != 1 || previous.terminal != 5 || !previous.completed {
								t.Fatalf("fresh opening changed completed predecessor: %+v", previous)
							}
						}
						rows, delivered, text := 0, 1, 1
						if end >= 5 {
							rows, text = 1, 4
						}
						if end >= 6 {
							delivered, text = 6, 6
						}
						if end == 9 {
							rows, text = 2, 8
						}
						if strings.Count(body, "Speaker: assistant") != rows || strings.Contains(body, "unfinished secret") != (end == 9) || !strings.Contains(body, fmt.Sprintf("Last delivered entry: %d\nLast text entry: %d\n", delivered, text)) {
							t.Fatal("runtime opening changed withholding or text boundaries", body)
						}
						for _, id := range []int{4, 8} {
							if (id == 4 && end >= 5) || (id == 8 && end == 9) {
								identity := fmt.Sprintf("Message: chat/%d\nTimestamp: %s", id, entries[id-1].TS.UTC().Format(time.RFC3339Nano))
								if !strings.Contains(body, identity) {
									t.Fatal("completion changed first-text identity/time", body)
								}
							}
						}
					})
				})
			}
		}
	}
}

func TestMemoryTranscriptReplyRuntimeParents(t *testing.T) {
	for _, source := range []string{"claude", "legacy", "codex"} {
		for _, kind := range []string{"main_turn_opened", "main_turn_interrupted"} {
			for _, parent := range []string{"parent_tool_use_id", "parent_tool_call_id"} {
				t.Run(source+"/"+kind+"/"+parent, func(t *testing.T) {
					entries := testReplyEntries(replyUser, replyOpen, replyText,
						fmt.Sprintf(`%s {"turn_id":"t","turn_opened_entry_id":2,"cause":"restart","occurred_at":"2026-01-01T00:00:00Z","tool_use_id":"child",%q:"outer"}`, kind, parent),
						`main_turn_interrupted {"turn_id":"t","turn_opened_entry_id":2,"cause":"restart","occurred_at":"2026-01-01T00:00:00Z"}`, replyEnd)
					testReplyLauncherSource(entries, source)
					testReplyRuntimeReplay(t, entries, func(t *testing.T, w *memoryTranscriptReader, end int, body string) {
						if end >= 3 {
							st := w.replies.text[3]
							terminal := uint64(0)
							if end >= 5 {
								terminal = 5
							}
							if st == nil || st.opening != 2 || st.user != 1 || st.terminal != terminal || st.completed || w.replies.openings[4] != nil {
								t.Fatalf("parented runtime fact changed main lifetime: %+v", st)
							}
						}
						if strings.Contains(body, "Speaker: assistant") {
							t.Fatal("parented runtime fact released text", body)
						}
					})
				})
			}
		}
	}
}

func testReplyRuntimeReplay(t *testing.T, entries []history.Entry, check func(*testing.T, *memoryTranscriptReader, int, string)) {
	t.Helper()
	for split := 0; split < 1<<(len(entries)-1); split++ {
		t.Run(fmt.Sprintf("split=%b", split), func(t *testing.T) {
			w := newMemoryTranscriptReader(nil, "chat")
			start := 0
			for end := 1; end <= len(entries); end++ {
				if end != len(entries) && split&(1<<(end-1)) == 0 {
					continue
				}
				testMemoryMust(t, w.feed(entries[start:end]))
				start = end
				check(t, w, end, testReplyBody(t, w.files(conversations.Conversation{ID: "chat"}), "question"))
			}
		})
	}
}
