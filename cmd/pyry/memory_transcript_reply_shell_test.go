package main

import (
	"fmt"
	"strings"
	"testing"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/history"
)

func TestVerifierReusedShellRecovery(t *testing.T) {
	for _, source := range []string{"claude", "legacy"} {
		for _, first := range []string{"Read", "Bash"} {
			for _, chunk := range []int{1, 8} {
				t.Run(fmt.Sprintf("%s/%s/chunk=%d", source, first, chunk), func(t *testing.T) {
					entries := testReplyEntries(
						fmt.Sprintf(`tool_use {"turn_id":"old","tool_use_id":"child","name":%q}`, first),
						`tool_use {"turn_id":"old","tool_use_id":"child","name":"Bash"}`,
						replyUser,
						`agent_ended_with_session {"conversation_id":"chat","tool_call_id":"child","call_observed_entry_id":2,"parent_tool_call_id":"outer","cause":"restart","occurred_at":"2026-01-01T00:00:00Z"}`,
						replyChildResult, replyText,
						`main_turn_interrupted {"turn_id":"t","turn_opened_entry_id":5,"cause":"restart","occurred_at":"2026-01-01T00:00:00Z"}`, replyEnd)
					testReplyLauncherSource(entries, source)
					w := newMemoryTranscriptReader(nil, "chat")
					for start := 0; start < len(entries); start += chunk {
						testMemoryMust(t, w.feed(entries[start:min(start+chunk, len(entries))]))
					}
					st := w.replies.text[6]
					if st == nil || st.opening != 5 || st.user != 3 || st.terminal != 7 || st.completed {
						t.Fatalf("unaccepted shell recovery bypassed interruption: %+v", st)
					}
					body := testReplyBody(t, w.files(conversations.Conversation{ID: "chat"}), "question")
					if strings.Contains(body, "Speaker: assistant") {
						t.Fatal("unaccepted recovery observation released interrupted reply", body)
					}
				})
			}
		}
	}
}

func TestVerifierShellRecoveryValidation(t *testing.T) {
	for _, source := range []string{"claude", "legacy"} {
		for _, extra := range []string{"", `,"occurred_at":null`, `,"task_id":null`} {
			for _, chunk := range []int{1, 7} {
				t.Run(fmt.Sprintf("%s/extra=%s/chunk=%d", source, extra, chunk), func(t *testing.T) {
					entries := testReplyEntries(`tool_use {"turn_id":"old","tool_use_id":"child","name":"Bash"`+extra+`}`, replyUser,
						`agent_ended_with_session {"conversation_id":"chat","tool_call_id":"child","call_observed_entry_id":1,"parent_tool_call_id":"outer","cause":"restart","occurred_at":"2026-01-01T00:00:00Z"}`,
						replyChildResult, replyText,
						`main_turn_interrupted {"turn_id":"t","turn_opened_entry_id":5,"cause":"restart","occurred_at":"2026-01-01T00:00:00Z"}`, replyEnd)
					testReplyLauncherSource(entries, source)
					w := newMemoryTranscriptReader(nil, "chat")
					for start := 0; start < len(entries); start += chunk {
						testMemoryMust(t, w.feed(entries[start:min(start+chunk, len(entries))]))
					}
					st := w.replies.text[5]
					if st == nil || st.opening != 5 || st.user != 2 || st.terminal != 6 || st.completed {
						t.Fatalf("accepted shell observation lost during recovery: %+v", st)
					}
					body := testReplyBody(t, w.files(conversations.Conversation{ID: "chat"}), "question")
					if strings.Contains(body, "Speaker: assistant") {
						t.Fatal("missing shell observation bypassed interruption", body)
					}
				})
			}
		}
	}
}

func TestVerifierRejectedRuntimeTransfer(t *testing.T) {
	for _, source := range []string{"claude", "legacy", "codex"} {
		// Child text must still transfer the existing ordinary call. A guard
		// that rejects every non-tool event would fabricate the same observation.
		for _, kind := range []string{"main_turn_opened", "main_turn_interrupted", "assistant_delta"} {
			for _, parent := range []string{"parent_tool_call_id", "parent_tool_use_id"} {
				for _, shell := range []string{"Bash", "local_bash"} {
					for _, interrupted := range []bool{false, true} {
						t.Run(fmt.Sprintf("%s/%s/%s/%s/interrupted=%t", source, kind, parent, shell, interrupted), func(t *testing.T) {
							entries := testReplyEntries(
								`tool_use {"turn_id":"old","tool_use_id":"child","name":"Read"}`,
								fmt.Sprintf(`%s {"turn_id":"old","tool_use_id":"child",%q:"outer","cause":"restart","occurred_at":"2026-01-01T00:00:00Z","text":"child text"}`, kind, parent),
								fmt.Sprintf(`tool_use {"turn_id":"old","tool_use_id":"child","name":%q}`, shell),
								replyUser,
								`agent_ended_with_session {"conversation_id":"chat","tool_call_id":"child","call_observed_entry_id":3,"parent_tool_call_id":"outer","cause":"restart","occurred_at":"2026-01-01T00:00:00Z"}`,
								replyChildResult)
							testReplyLauncherSource(entries, source)
							testReplyLauncherReplay(t, entries, 6, 4, interrupted)
						})
					}
				}
			}
		}
	}
}

func TestMemoryTranscriptReplyShellObservation(t *testing.T) {
	launch := `tool_use {"turn_id":"old","tool_use_id":"child","name":"Bash"}`
	ending := `agent_ended_with_session {"conversation_id":"chat","tool_call_id":"child","call_observed_entry_id":1,"parent_tool_call_id":"outer","cause":"restart","occurred_at":"2026-01-01T00:00:00Z"}`
	lifetime := `,"lifetime_id":"11111111-1111-4111-8111-111111111111"`
	repeated := strings.Replace(ending, `:1,`, `:3,`, 1)
	repeated = strings.TrimSuffix(repeated, "}") + lifetime + `}`
	fresh := strings.Replace(replyTaskLifetime, "11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222", 1)
	freshEnding := strings.Replace(strings.Replace(repeated, `:3,`, `:4,`, 1), "11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222", 1)
	for _, source := range []string{"claude", "legacy", "codex"} {
		for _, shell := range []string{"Bash", "local_bash"} {
			for _, tc := range []struct {
				name          string
				facts         []string
				opening, user uint64
				change        func([]history.Entry)
			}{
				{"created shell", []string{launch, replyUser, ending, replyChildResult}, 5, 2, nil},
				{"unknown timestamp null", []string{strings.TrimSuffix(launch, "}") + `,"occurred_at":null}`, replyUser, ending, replyChildResult}, 5, 2, nil},
				{"unknown task null", []string{strings.TrimSuffix(launch, "}") + `,"task_id":null}`, replyUser, ending, replyChildResult}, 5, 2, nil},
				{"DTO null", []string{strings.TrimSuffix(launch, "}") + `,"input_summary":null}`, replyUser, ending, replyChildResult}, 4, 2, nil},
				{"identity null", []string{strings.TrimSuffix(launch, "}") + `,"parent_tool_use_id":null}`, replyUser, ending, replyChildResult}, 4, 2, nil},
				{"lost identity", []string{strings.TrimSuffix(launch, "}") + `,"dropped_fields":["tool_use_id"]}`, replyUser, ending, replyChildResult}, 4, 2, nil},
				{"ordinary reuse", []string{strings.Replace(launch, `"Bash"`, `"Read"`, 1), launch, replyUser, strings.Replace(ending, `:1,`, `:2,`, 1), replyChildResult}, 5, 3, nil},
				{"launcher reuse", []string{strings.Replace(launch, `"Bash"`, `"Agent"`, 1), launch, replyUser, strings.Replace(ending, `:1,`, `:2,`, 1), replyChildResult}, 5, 3, nil},
				{"child ordinary reuse", []string{strings.TrimSuffix(strings.Replace(launch, `"Bash"`, `"Read"`, 1), "}") + `,"parent_tool_call_id":"outer"}`, strings.TrimSuffix(launch, "}") + `,"parent_tool_call_id":"outer"}`, replyUser, strings.Replace(ending, `:1,`, `:2,`, 1), replyChildResult}, 5, 3, nil},
				{"main call transferred to child", []string{strings.Replace(launch, `"Bash"`, `"Read"`, 1), strings.TrimSuffix(launch, "}") + `,"parent_tool_call_id":"outer"}`, replyUser, strings.Replace(ending, `:1,`, `:2,`, 1), replyChildResult}, 5, 3, nil},
				{"shell reuse keeps first observation", []string{launch, launch, replyUser, strings.Replace(ending, `:1,`, `:2,`, 1), replyChildResult}, 5, 3, nil},
				{"reference original reused shell", []string{launch, launch, replyUser, ending, replyChildResult}, 6, 3, nil},
				{"ordinary reuse after lifetime", []string{strings.Replace(launch, `"Bash"`, `"Read"`, 1), replyTaskLifetime, launch, replyUser, repeated, replyChildResult}, 6, 4, nil},
				{"shell reuse after promotion", []string{launch, replyTaskLifetime, launch, replyUser, repeated, replyChildResult}, 6, 4, nil},
				{"recover promoted original shell", []string{launch, replyTaskLifetime, launch, replyUser, strings.TrimSuffix(ending, "}") + lifetime + `}`, replyChildResult}, 7, 4, nil},
				{"fresh producer resets shell", []string{launch, replyTaskLifetime, fresh, launch, replyUser, freshEnding, replyChildResult}, 8, 5, nil},
				{"new turn creates shell", []string{strings.Replace(launch, `"Bash"`, `"Read"`, 1), strings.Replace(launch, `"old"`, `"next"`, 1), replyUser, strings.Replace(ending, `:1,`, `:2,`, 1), replyChildResult}, 6, 3, nil},
				{"new generation creates shell", []string{strings.Replace(launch, `"Bash"`, `"Read"`, 1), strings.Replace(replyOpen, `"t"`, `"old"`, 1), launch, replyUser, strings.Replace(ending, `:1,`, `:3,`, 1), replyChildResult}, 7, 4, nil},
				{"other session", []string{launch, replyUser, ending, replyChildResult}, 4, 2, func(e []history.Entry) { e[0].Session = &history.SessionProvenance{Kind: "claude", SessionID: "B"} }},
				{"other source kind", []string{launch, replyUser, ending, replyChildResult}, 4, 2, func(e []history.Entry) { e[0].Session = &history.SessionProvenance{Kind: "codex", SessionID: "A"} }},
				{"tagged legacy separation", []string{launch, replyUser, ending, replyChildResult}, 4, 2, func(e []history.Entry) {
					if source == "legacy" {
						e[0].Session = &history.SessionProvenance{Kind: "claude", SessionID: "A"}
					} else {
						e[0].Session = nil
					}
				}},
			} {
				for _, interrupted := range []bool{false, true} {
					t.Run(fmt.Sprintf("%s/%s/%s/interrupted=%t", source, shell, tc.name, interrupted), func(t *testing.T) {
						entries := testReplyEntries(tc.facts...)
						testReplyLauncherSource(entries, source)
						for i := range entries {
							entries[i].Payload = []byte(strings.ReplaceAll(string(entries[i].Payload), `"Bash"`, fmt.Sprintf("%q", shell)))
						}
						if tc.change != nil {
							tc.change(entries)
						}
						opening := tc.opening
						if source == "codex" && opening > uint64(len(entries)) {
							opening = uint64(len(entries))
						}
						testReplyLauncherReplay(t, entries, opening, tc.user, interrupted)
					})
				}
			}
		}
	}
}
