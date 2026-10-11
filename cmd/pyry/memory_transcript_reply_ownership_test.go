package main

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/history"
)

const replyChildObservation = `agent_call_observed {"conversation_id":"chat","lifetime_id":"11111111-1111-4111-8111-111111111111","tool_call_id":"child","tool":"Agent","parent_tool_call_id":"outer","occurred_at":"2026-01-01T00:00:00Z"}`
const replyTaskLifetime = `background_task_observed {"conversation_id":"chat","lifetime_id":"11111111-1111-4111-8111-111111111111","task_id":"other","occurred_at":"2026-01-01T00:00:00Z"}`
const replyChildResult = `tool_result {"turn_id":"t","tool_use_id":"child","is_error":false}`

// Prove both interruption-first withholding and successful completion, including
// the implicit opening selected before the text. Later conflicting endings must
// never mask an ownership mismatch. Chunking and reconstruction must agree.
func testReplyOwnership(t *testing.T, prefix []history.Entry, opening uint64) {
	t.Helper()
	for _, interrupted := range []bool{false, true} {
		for _, chunk := range []int{1, len(prefix) + 3} {
			t.Run(fmt.Sprintf("interrupted=%t/chunk=%d", interrupted, chunk), func(t *testing.T) {
				entries := append([]history.Entry(nil), prefix...)
				textID := uint64(len(entries) + 1)
				text := testReplyEntries(replyText)[0]
				text.ID, text.TS = textID, time.Unix(int64(textID), 0)
				text.Session = entries[len(entries)-1].Session
				entries = append(entries, text)
				terminal := replyEnd
				if interrupted {
					terminal = fmt.Sprintf(`main_turn_interrupted {"turn_id":"t","turn_opened_entry_id":%d,"cause":"restart","occurred_at":"2026-01-01T00:00:00Z"}`, opening)
				}
				for _, fact := range []string{terminal, replyEnd} {
					e := testReplyEntries(fact)[0]
					e.ID, e.TS, e.Session = uint64(len(entries)+1), time.Unix(int64(len(entries)+1), 0), text.Session
					entries = append(entries, e)
				}
				w := newMemoryTranscriptReader(nil, "chat")
				for start := 0; start < int(textID); start += chunk {
					testMemoryMust(t, w.feed(entries[start:min(start+chunk, int(textID))]))
				}
				if b := testReplyBody(t, w.files(conversations.Conversation{ID: "chat"}), "question"); strings.Contains(b, "Speaker: assistant") {
					t.Fatal("unfinished reply published", b)
				}
				st := w.replies.text[textID]
				if st == nil || st.opening != opening {
					t.Fatalf("main text opening = %v, want %d", st, opening)
				}
				for start := int(textID); start < len(entries); start += chunk {
					testMemoryMust(t, w.feed(entries[start:min(start+chunk, len(entries))]))
				}
				files := w.files(conversations.Conversation{ID: "chat"})
				body := testReplyBody(t, files, "question")
				if strings.Contains(body, "Speaker: assistant") == interrupted {
					t.Fatal("ownership changed first-terminal completion", body)
				}
				lastText := uint64(1)
				if !interrupted {
					lastText = textID
					for _, want := range []string{fmt.Sprintf("Message: chat/%d\nTimestamp: %s", textID, text.TS.UTC().Format(time.RFC3339Nano)), "answer"} {
						if !strings.Contains(body, want) {
							t.Fatalf("missing %q: %s", want, body)
						}
					}
				}
				if !strings.Contains(body, fmt.Sprintf("Last text entry: %d\n", lastText)) || !strings.Contains(body, "Last delivered entry: 1\n") {
					t.Fatal("nontext ownership/terminal evidence advanced text boundary", body)
				}
				if !reflect.DeepEqual(files, testTranscriptView(t, entries)) {
					t.Fatal("whole and chunked reconstruction differ")
				}
			})
		}
	}
}

func TestVerifierReplyBoundaryOwnership(t *testing.T) {
	for _, cause := range []string{"idle_sleep", "capacity_eviction", "recovery", "workspace_change", "operator_reset", "agent_switch", "claude_clear"} {
		for _, report := range []string{replyChildResult, `tool_denied {"turn_id":"t","tool_use_id":"child"}`} {
			t.Run(cause+"/"+strings.Split(report, " ")[0], func(t *testing.T) {
				e := testReplyEntries(replyUser, replyChildObservation,
					fmt.Sprintf(`session_divider {"cause":%q,"previous_session_id":"A","occurred_at":"2026-01-01T00:00:00Z"}`, cause), report)
				testReplyOwnership(t, e, 4)
			})
		}
	}
}

func TestVerifierReplyPromotedChildOwnership(t *testing.T) {
	for _, tool := range []string{"Bash", "local_bash"} {
		for _, legacy := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/legacy=%t", tool, legacy), func(t *testing.T) {
				e := testReplyEntries(replyUser,
					fmt.Sprintf(`tool_use {"turn_id":"t","tool_use_id":"child","name":%q,"parent_tool_call_id":"outer"}`, tool),
					replyTaskLifetime, replyChildResult)
				if legacy {
					for i := range e {
						e[i].Session = nil
					}
				}
				testReplyOwnership(t, e, 5)
			})
		}
	}
}

func TestVerifierReplyMappedChildAcrossTurns(t *testing.T) {
	for _, tool := range []string{"Agent", "Task"} {
		for _, legacy := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/legacy=%t", tool, legacy), func(t *testing.T) {
				e := testReplyEntries(replyUser,
					fmt.Sprintf(`tool_use {"turn_id":"childturn","tool_use_id":"child","name":%q,"parent_tool_call_id":"outer"}`, tool), replyChildResult)
				if legacy {
					for i := range e {
						e[i].Session = nil
					}
				}
				testReplyOwnership(t, e, 4)
			})
		}
	}
}

func TestMemoryTranscriptReplyOwnershipControls(t *testing.T) {
	divider := `session_divider {"cause":"idle_sleep","previous_session_id":"A","occurred_at":"2026-01-01T00:00:00Z"}`
	bash := `tool_use {"turn_id":"childturn","tool_use_id":"child","name":"Bash","parent_tool_call_id":"outer"}`
	for _, tc := range []struct {
		name    string
		facts   []string
		opening uint64
		change  func([]history.Entry)
	}{
		{name: "restart preserves active lifetime", facts: []string{replyChildObservation, strings.Replace(divider, "idle_sleep", "daemon_restart", 1), replyChildResult}, opening: 5},
		{name: "paired divider clears once", facts: []string{replyChildObservation, divider, replyChildObservation, `session_transition {"reason":"idle_evict","previous_session_id":"A","new_session_id":"A","occurred_at":"2026-01-01T00:00:00Z"}`, replyChildResult}, opening: 7},
		{name: "standalone transition clears", facts: []string{replyChildObservation, `session_transition {"reason":"recovered","previous_session_id":"A","new_session_id":"A","occurred_at":"2026-01-01T00:00:00Z"}`, replyChildResult}, opening: 4},
		{name: "boundary-separated shell", facts: []string{bash, divider, replyTaskLifetime, replyChildResult}, opening: 5},
		{name: "established lifetime never transfers", facts: []string{bash, replyTaskLifetime, strings.Replace(replyTaskLifetime, "11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222", 1), replyChildResult}, opening: 5},
		{name: "destination call collision", facts: []string{strings.Replace(replyChildObservation, `,"parent_tool_call_id":"outer"`, "", 1), divider, bash, replyTaskLifetime, replyChildResult}, opening: 6},
		{name: "created Agent never transfers", facts: []string{strings.Replace(bash, `"Bash"`, `"Agent"`, 1), replyTaskLifetime, replyChildResult}, opening: 4},
		{name: "ordinary child never transfers", facts: []string{strings.Replace(bash, `"Bash"`, `"Read"`, 1), replyTaskLifetime, replyChildResult}, opening: 4},
		{name: "ordinary child stays turn-local", facts: []string{strings.Replace(bash, `"Bash"`, `"Read"`, 1), replyChildResult}, opening: 3},
		{name: "mapped recovery reference rejected", facts: []string{strings.Replace(strings.Replace(bash, `"Bash"`, `"Agent"`, 1), `}`, `,"call_observed_entry_id":1}`, 1), replyChildResult}, opening: 3},
		{name: "mapped scalar null rejected", facts: []string{strings.Replace(strings.Replace(bash, `"Bash"`, `"Task"`, 1), `}`, `,"occurred_at":null}`, 1), replyChildResult}, opening: 3},
		{name: "source isolation", facts: []string{strings.Replace(bash, `"Bash"`, `"Agent"`, 1), replyChildResult}, opening: 3, change: func(e []history.Entry) { e[1].Session.SessionID = "B" }},
		{name: "tagged legacy isolation", facts: []string{strings.Replace(bash, `"Bash"`, `"Task"`, 1), replyChildResult}, opening: 3, change: func(e []history.Entry) { e[1].Session = nil }},
		{name: "Codex ownership isolation", facts: []string{bash, replyTaskLifetime, replyChildResult}, opening: 4, change: func(e []history.Entry) { e[1].Session.Kind = "codex" }},
		{name: "pending report parent transfers", facts: []string{`tool_result {"turn_id":"childturn","tool_use_id":"child","is_error":false,"parent_tool_call_id":"outer"}`, replyTaskLifetime, replyChildResult}, opening: 5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := testReplyEntries(append([]string{replyUser}, tc.facts...)...)
			if tc.change != nil {
				tc.change(e)
			}
			testReplyOwnership(t, e, tc.opening)
		})
	}
}
