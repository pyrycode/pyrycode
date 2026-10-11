package main

import (
	"fmt"
	"strings"
	"testing"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/history"
)

func TestVerifierOwnershipReview(t *testing.T) {
	for _, which := range []string{"conflicting task", "call recovery", "task recovery"} {
		for _, legacy := range []bool{false, true} {
			for _, control := range []bool{false, true} {
				for _, chunk := range []int{1, 8} {
					t.Run(fmt.Sprintf("%s/legacy=%t/control=%t/chunk=%d", which, legacy, control, chunk), func(t *testing.T) {
						var facts []string
						opening := 5
						switch which {
						case "conflicting task":
							opening = 6
							ending := `agent_ended_with_session {"conversation_id":"chat","lifetime_id":"22222222-2222-4222-8222-222222222222","task_id":"task","tool_call_id":"wrong","cause":"restart","occurred_at":"2026-01-01T00:00:00Z"}`
							if control {
								ending = strings.Replace(ending, `"wrong"`, `"other"`, 1)
								opening = 5
							}
							facts = []string{replyUser, `background_task_linked {"conversation_id":"chat","lifetime_id":"22222222-2222-4222-8222-222222222222","task_id":"task","tool_call_id":"other","occurred_at":"2026-01-01T00:00:00Z"}`, replyChildObservation, ending, replyChildResult, replyText}
						default:
							observation := `agent_call_observed {"conversation_id":"chat","lifetime_id":"11111111-1111-4111-8111-111111111111","tool_call_id":"child","tool":"Agent","occurred_at":"2026-01-01T00:00:00Z"}`
							ref := `,"call_observed_entry_id":2`
							task := ""
							if which == "task recovery" {
								observation = `background_task_observed {"conversation_id":"chat","lifetime_id":"11111111-1111-4111-8111-111111111111","tool_call_id":"child","task_id":"task","occurred_at":"2026-01-01T00:00:00Z"}`
								ref = `,"task_observed_entry_id":2`
								task = `,"task_id":"task"`
							}
							if control {
								ref = ""
							}
							facts = []string{replyUser, observation, `agent_ended_with_session {"conversation_id":"chat","lifetime_id":"11111111-1111-4111-8111-111111111111","tool_call_id":"child","parent_tool_call_id":"outer","cause":"restart","occurred_at":"2026-01-01T00:00:00Z"` + ref + task + `}`, replyChildResult, replyText}
						}
						textID := len(facts)
						facts = append(facts, fmt.Sprintf(`main_turn_interrupted {"turn_id":"t","turn_opened_entry_id":%d,"cause":"restart","occurred_at":"2026-01-01T00:00:00Z"}`, opening), replyEnd)
						es := testReplyEntries(facts...)
						if legacy {
							testReplyLauncherSource(es, "legacy")
						}
						oracle := newMemoryTranscriptReader(nil, "chat")
						testMemoryMust(t, oracle.feed(es[:len(es)-1]))
						found := false
						for _, it := range oracle.fold.Items() {
							if it.ID == uint64(textID) && it.Kind == "assistant_message" {
								found = true
								if it.Active {
									t.Fatal("fold did not accept interruption at expected opening")
								}
							}
						}
						if !found {
							t.Fatal("fold omitted main text")
						}
						w := newMemoryTranscriptReader(nil, "chat")
						for start := 0; start < len(es); start += chunk {
							testMemoryMust(t, w.feed(es[start:min(start+chunk, len(es))]))
						}
						t.Logf("exporter lifetime %+v; expected opening %d", w.replies.text[uint64(textID)], opening)
						body := testReplyBody(t, w.files(conversations.Conversation{ID: "chat"}), "question")
						if strings.Contains(body, "Speaker: assistant") {
							t.Fatal("ownership mismatch released interrupted reply", body)
						}
					})
				}
			}
		}
	}
}

func TestMemoryTranscriptReplyRecoveryOwnership(t *testing.T) {
	observation := strings.Replace(replyChildObservation, `,"parent_tool_call_id":"outer"`, "", 1)
	task := strings.Replace(observation, `agent_call_observed`, `background_task_observed`, 1)
	task = strings.TrimSuffix(task, "}") + `,"task_id":"task"}`
	ending := `agent_ended_with_session {"conversation_id":"chat","lifetime_id":"11111111-1111-4111-8111-111111111111","tool_call_id":"child","parent_tool_call_id":"outer","cause":"restart","occurred_at":"2026-01-01T00:00:00Z"}`
	callRef := strings.TrimSuffix(ending, "}") + `,"call_observed_entry_id":2}`
	taskRef := strings.TrimSuffix(ending, "}") + `,"task_id":"task","task_observed_entry_id":2}`
	linked := `background_task_linked {"conversation_id":"chat","lifetime_id":"22222222-2222-4222-8222-222222222222","task_id":"task","tool_call_id":"other","occurred_at":"2026-01-01T00:00:00Z"}`
	conflict := `agent_ended_with_session {"conversation_id":"chat","lifetime_id":"22222222-2222-4222-8222-222222222222","task_id":"task","tool_call_id":"wrong","cause":"restart","occurred_at":"2026-01-01T00:00:00Z"}`
	lifetime2 := strings.Replace(replyTaskLifetime, "11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222", 1)
	noLifetime := func(fact string) string {
		return strings.Replace(fact, `"lifetime_id":"11111111-1111-4111-8111-111111111111",`, "", 1)
	}
	for _, source := range []string{"claude", "legacy", "codex"} {
		for _, tc := range []struct {
			name    string
			facts   []string
			opening uint64
		}{
			{"conflicting task", []string{linked, replyChildObservation, conflict, replyChildResult}, 6},
			{"matching task", []string{linked, replyChildObservation, strings.Replace(conflict, `"wrong"`, `"other"`, 1), replyChildResult}, 5},
			{"call recovery", []string{observation, callRef, replyChildResult}, 5},
			{"task recovery", []string{task, taskRef, replyChildResult}, 5},
			{"no reference control", []string{observation, ending, replyChildResult}, 5},
			{"no lifetime recovery", []string{observation, noLifetime(callRef), replyChildResult}, 5},
			{"both references", []string{observation, task, strings.TrimSuffix(callRef, "}") + `,"task_id":"task","task_observed_entry_id":3}`, replyChildResult}, 6},
			{"active lifetime unchanged", []string{observation, lifetime2, callRef, replyChildResult}, 5},
			{"original ownership retained", []string{observation, lifetime2, callRef, replyTaskLifetime, replyChildResult}, 7},
			{"boundary recovery", []string{observation, `session_divider {"cause":"idle_sleep","previous_session_id":"A","occurred_at":"2026-01-01T00:00:00Z"}`, replyUser, callRef, replyTaskLifetime, replyChildResult}, 8},
			{"mapped task observation", []string{`background_task_started {"task_id":"task","tool_call_id":"child"}`, noLifetime(taskRef), replyChildResult}, 5},
			{"roster task observation", []string{`background_task_roster {"tasks":[{"task_id":"task","tool_call_id":"child"}]}`, noLifetime(taskRef), replyChildResult}, 5},
			{"mapped call without main turn", []string{`tool_use {"tool_use_id":"child","name":"Agent"}`, noLifetime(callRef), replyChildResult}, 5},
			{"mapped call observation", []string{`tool_use {"turn_id":"t","tool_use_id":"child","name":"Agent"}`, noLifetime(callRef), replyChildResult}, 2},
			{"mapped shell observation", []string{`tool_use {"turn_id":"t","tool_use_id":"child","name":"Bash"}`, noLifetime(callRef), replyChildResult}, 2},
		} {
			for _, interrupted := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/interrupted=%t", source, tc.name, interrupted), func(t *testing.T) {
					t.Parallel()
					entries := testReplyEntries(append([]string{replyUser}, tc.facts...)...)
					testReplyLauncherSource(entries, source)
					opening := tc.opening
					if source == "codex" && opening > uint64(len(entries)) {
						opening = uint64(len(entries))
					}
					user := uint64(1)
					if tc.name == "boundary recovery" {
						user = 4
					}
					testReplyLauncherReplay(t, entries, opening, user, interrupted)
				})
			}
		}
	}
}

func TestMemoryTranscriptReplyRecoveryValidation(t *testing.T) {
	observation := strings.Replace(replyChildObservation, `,"parent_tool_call_id":"outer"`, "", 1)
	ending := `agent_ended_with_session {"conversation_id":"chat","lifetime_id":"11111111-1111-4111-8111-111111111111","tool_call_id":"child","call_observed_entry_id":2,"parent_tool_call_id":"outer","cause":"restart","occurred_at":"2026-01-01T00:00:00Z"}`
	for _, source := range []string{"claude", "legacy"} {
		for _, tc := range []struct {
			name   string
			change func([]history.Entry)
		}{
			{"null", func(e []history.Entry) {
				e[2].Payload = []byte(strings.Replace(ending, `"call_observed_entry_id":2`, `"call_observed_entry_id":null`, 1))
			}},
			{"zero", func(e []history.Entry) {
				e[2].Payload = []byte(strings.Replace(ending, `"call_observed_entry_id":2`, `"call_observed_entry_id":0`, 1))
			}},
			{"out of range", func(e []history.Entry) {
				e[2].Payload = []byte(strings.Replace(ending, `"call_observed_entry_id":2`, `"call_observed_entry_id":9007199254740992`, 1))
			}},
			{"forward", func(e []history.Entry) {
				e[2].Payload = []byte(strings.Replace(ending, `"call_observed_entry_id":2`, `"call_observed_entry_id":3`, 1))
			}},
			{"wrong kind", func(e []history.Entry) {
				e[2].Payload = []byte(strings.Replace(ending, `"call_observed_entry_id":2`, `"call_observed_entry_id":1`, 1))
			}},
			{"wrong identity", func(e []history.Entry) {
				e[2].Payload = []byte(strings.Replace(ending, `"tool_call_id":"child"`, `"tool_call_id":"wrong"`, 1))
			}},
			{"wrong lifetime", func(e []history.Entry) {
				e[2].Payload = []byte(strings.Replace(ending, "11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222", 1))
			}},
			{"foreign conversation", func(e []history.Entry) { e[2].Payload = []byte(strings.Replace(ending, `"chat"`, `"foreign"`, 1)) }},
			{"lost reference", func(e []history.Entry) {
				e[2].Payload = []byte(strings.TrimSuffix(ending, "}") + `,"dropped_fields":["call_observed_entry_id"]}`)
			}},
			{"lost parent", func(e []history.Entry) {
				e[2].Payload = []byte(strings.TrimSuffix(ending, "}") + `,"truncated_fields":["parent_tool_call_id"]}`)
			}},
			{"different reference kind", func(e []history.Entry) {
				e[2].Payload = []byte(strings.Replace(ending, `"call_observed_entry_id":2`, `"task_id":"task","task_observed_entry_id":2`, 1))
			}},
			{"session isolation", func(e []history.Entry) { e[1].Session = &history.SessionProvenance{Kind: "claude", SessionID: "B"} }},
			{"agent isolation", func(e []history.Entry) { e[1].Session = &history.SessionProvenance{Kind: "codex", SessionID: "A"} }},
			{"tagged legacy isolation", func(e []history.Entry) {
				if source == "legacy" {
					e[1].Session = &history.SessionProvenance{Kind: "claude", SessionID: "A"}
				} else {
					e[1].Session = nil
				}
			}},
			{"all references must resolve", func(e []history.Entry) {
				e[2].Payload = []byte(strings.TrimSuffix(ending, "}") + `,"task_id":"task","task_observed_entry_id":1}`)
			}},
		} {
			t.Run(source+"/"+tc.name, func(t *testing.T) {
				t.Parallel()
				entries := testReplyEntries(replyUser, observation, ending, replyChildResult)
				testReplyLauncherSource(entries, source)
				tc.change(entries)
				for _, interrupted := range []bool{false, true} {
					testReplyLauncherReplay(t, entries, 4, 1, interrupted)
				}
			})
		}
	}
}

func TestMemoryTranscriptReplyRecoveryState(t *testing.T) {
	observation := strings.Replace(replyChildObservation, `,"parent_tool_call_id":"outer"`, "", 1)
	ending := `agent_ended_with_session {"conversation_id":"chat","lifetime_id":"11111111-1111-4111-8111-111111111111","tool_call_id":"child","call_observed_entry_id":2,"parent_tool_call_id":"outer","cause":"restart","occurred_at":"2026-01-01T00:00:00Z"}`
	lifetime2 := strings.Replace(replyTaskLifetime, "11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222", 1)
	for _, source := range []string{"claude", "legacy"} {
		for _, tc := range []struct {
			name   string
			facts  []string
			ending string
			child  bool
		}{
			{"recover original while successor active", []string{observation, lifetime2}, ending, true},
			{"repeated observation is not original", []string{observation, observation}, strings.Replace(ending, `"call_observed_entry_id":2`, `"call_observed_entry_id":3`, 1), false},
			{"references in different groups", []string{observation, lifetime2}, strings.Replace(strings.TrimSuffix(ending, "}"), `"lifetime_id":"11111111-1111-4111-8111-111111111111",`, "", 1) + `,"task_id":"other","task_observed_entry_id":3}`, false},
			{"conflicting referenced task link", []string{observation, `background_task_observed {"conversation_id":"chat","lifetime_id":"11111111-1111-4111-8111-111111111111","task_id":"task","occurred_at":"2026-01-01T00:00:00Z"}`, `background_task_linked {"conversation_id":"chat","lifetime_id":"11111111-1111-4111-8111-111111111111","task_id":"task","tool_call_id":"other","occurred_at":"2026-01-01T00:00:00Z"}`}, strings.TrimSuffix(ending, "}") + `,"task_id":"task","task_observed_entry_id":3}`, false},
		} {
			t.Run(source+"/"+tc.name, func(t *testing.T) {
				t.Parallel()
				entries := testReplyEntries(append(append([]string{replyUser}, tc.facts...), tc.ending)...)
				testReplyLauncherSource(entries, source)
				for _, chunk := range []int{1, len(entries) - 1} {
					w := newMemoryTranscriptReader(nil, "chat")
					for start := 0; start < len(entries)-1; start += chunk {
						testMemoryMust(t, w.feed(entries[start:min(start+chunk, len(entries)-1)]))
					}
					key := w.replies.childKey(entries[1], "")
					key.lifetime, key.scope = "11111111-1111-4111-8111-111111111111", 0
					active := w.replies.lifetimes[memoryReplySource(entries[0])]
					testMemoryMust(t, w.feed(entries[len(entries)-1:]))
					if w.replies.lifetimes[memoryReplySource(entries[0])] != active || w.replies.owners[key]["child"].child != tc.child {
						t.Fatal("recovery changed active lifetime or recorded ownership")
					}
				}
			})
		}
		t.Run(source+"/conflict before promotion", func(t *testing.T) {
			entries := testReplyEntries(replyUser, `background_task_started {"task_id":"task","tool_call_id":"other"}`, `agent_ended_with_session {"conversation_id":"chat","lifetime_id":"11111111-1111-4111-8111-111111111111","task_id":"task","tool_call_id":"wrong","parent_tool_call_id":"outer","cause":"restart","occurred_at":"2026-01-01T00:00:00Z"}`)
			testReplyLauncherSource(entries, source)
			w := newMemoryTranscriptReader(nil, "chat")
			testMemoryMust(t, w.feed(entries[:2]))
			key := w.replies.childKey(entries[1], "")
			task := w.replies.tasks[key]["task"]
			testMemoryMust(t, w.feed(entries[2:]))
			if w.replies.lifetimes[memoryReplySource(entries[0])] != "" || w.replies.tasks[key]["task"] != task || len(w.replies.owners) != 1 || w.replies.owners[key]["wrong"] != nil {
				t.Fatal("rejected ending promoted or mutated ownership")
			}
		})
	}
}
