package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/history"
)

func testReplyEntries(facts ...string) []history.Entry {
	entries := make([]history.Entry, 0, len(facts))
	for i, fact := range facts {
		typ, payload, _ := strings.Cut(fact, " ")
		entries = append(entries, testTranscriptEntry(uint64(i+1), typ, payload, "A"))
	}
	return entries
}
func testReplyBody(t *testing.T, files map[string]string, text string) string {
	t.Helper()
	for _, body := range files {
		if strings.Contains(body, text) {
			return body
		}
	}
	t.Fatalf("missing %q in %v", text, files)
	return ""
}

const replyUser = `message {"role":"user","text":"question"}`
const replyOpen = `main_turn_opened {"turn_id":"t","occurred_at":"2026-01-01T00:00:00Z"}`
const replyText = `assistant_delta {"turn_id":"t","text":"answer"}`
const replyEnd = `turn_end {"turn_id":"t","stop_reason":"end_turn"}`

func TestMemoryTranscriptReplyObservedChildReport(t *testing.T) {
	for _, report := range []string{
		`tool_result {"turn_id":"t","tool_use_id":"child","is_error":false}`,
		`tool_denied {"turn_id":"t","tool_use_id":"child"}`,
	} {
		for _, legacy := range []bool{false, true} {
			for _, interrupted := range []bool{false, true} {
				for _, chunk := range []int{1, 6} {
					t.Run(fmt.Sprintf("%s/legacy=%t/interrupted=%t/chunk=%d", strings.Split(report, " ")[0], legacy, interrupted, chunk), func(t *testing.T) {
						terminal := replyEnd
						if interrupted {
							terminal = `main_turn_interrupted {"turn_id":"t","turn_opened_entry_id":4,"cause":"restart","occurred_at":"2026-01-01T00:00:00Z"}`
						}
						entries := testReplyEntries(replyUser,
							`agent_call_observed {"conversation_id":"chat","lifetime_id":"11111111-1111-4111-8111-111111111111","tool_call_id":"child","tool":"Agent","parent_tool_call_id":"outer","occurred_at":"2026-01-01T00:00:00Z"}`,
							report, replyText, terminal, replyEnd)
						if legacy {
							for i := range entries {
								entries[i].Session = nil
							}
						}
						w := newMemoryTranscriptReader(nil, "chat")
						for start := 0; start < len(entries); start += chunk {
							testMemoryMust(t, w.feed(entries[start:min(start+chunk, len(entries))]))
						}
						files := w.files(conversations.Conversation{ID: "chat"})
						body := testReplyBody(t, files, "question")
						if strings.Contains(body, "Speaker: assistant") == interrupted {
							t.Fatal("observed child report changed main completion or exchange permission", body)
						}
						boundary := "Last text entry: 1\n"
						if !interrupted {
							boundary = "Last text entry: 4\n"
							if !strings.Contains(body, "Message: chat/4\nTimestamp: 1970-01-01T00:00:04Z") {
								t.Fatal("first-text identity changed", body)
							}
						}
						if !strings.Contains(body, boundary) || !strings.Contains(body, "Last delivered entry: 1\n") {
							t.Fatal("nontext facts advanced text/delivery boundaries", body)
						}
						if !reflect.DeepEqual(files, testTranscriptView(t, entries)) {
							t.Fatal("chunked reconstruction differs")
						}
					})
				}
			}
		}
	}
}

func TestMemoryTranscriptReplyChildCallReuse(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		for _, chunk := range []int{1, 3, 8} {
			t.Run(fmt.Sprintf("legacy=%t/chunk=%d", legacy, chunk), func(t *testing.T) {
				entries := testReplyEntries(replyUser,
					`agent_call_observed {"conversation_id":"chat","lifetime_id":"11111111-1111-4111-8111-111111111111","tool_call_id":"child","tool":"Agent","parent_tool_call_id":"outer","occurred_at":"2026-01-01T00:00:00Z"}`,
					`tool_use {"turn_id":"t","tool_use_id":"child","name":"Agent"}`,
					`agent_call_observed {"conversation_id":"chat","lifetime_id":"22222222-2222-4222-8222-222222222222","tool_call_id":"child","tool":"Agent","occurred_at":"2026-01-01T00:00:00Z"}`,
					`tool_use {"turn_id":"t","tool_use_id":"child","name":"Agent"}`,
					replyText,
					`main_turn_interrupted {"turn_id":"t","turn_opened_entry_id":5,"cause":"restart","occurred_at":"2026-01-01T00:00:00Z"}`, replyEnd)
				if legacy {
					for i := range entries {
						entries[i].Session = nil
					}
				}
				w := newMemoryTranscriptReader(nil, "chat")
				for start := 0; start < len(entries); start += chunk {
					testMemoryMust(t, w.feed(entries[start:min(start+chunk, len(entries))]))
				}
				files := w.files(conversations.Conversation{ID: "chat"})
				if strings.Contains(testReplyBody(t, files, "question"), "Speaker: assistant") {
					t.Fatal("child exclusion crossed agent lifetime and hid the main opening")
				}
				if !reflect.DeepEqual(files, testTranscriptView(t, entries)) {
					t.Fatal("chunked reconstruction differs")
				}
			})
		}
	}
}

func TestMemoryTranscriptReplyChildOwnershipScopes(t *testing.T) {
	observation := `agent_call_observed {"conversation_id":"chat","lifetime_id":"11111111-1111-4111-8111-111111111111","tool_call_id":"child","tool":"Agent","parent_tool_call_id":"outer","occurred_at":"2026-01-01T00:00:00Z"}`
	for _, tc := range []struct {
		name, observation, successor string
		change                       func([]history.Entry)
	}{
		{name: "foreign conversation", observation: strings.Replace(observation, `"chat"`, `"elsewhere"`, 1)},
		{name: "invalid lifetime", observation: strings.Replace(observation, `11111111-1111-4111-8111-111111111111`, "invalid", 1)},
		{name: "null parent", observation: strings.Replace(observation, `"outer"`, `null`, 1)},
		{name: "lost lifetime", observation: strings.Replace(observation, `}`, `,"truncated_fields":["lifetime_id"]}`, 1)},
		{name: "lost parent", observation: strings.Replace(observation, `}`, `,"dropped_fields":["parent_tool_call_id"]}`, 1)},
		{name: "unexpected reference", observation: strings.Replace(observation, `}`, `,"call_observed_entry_id":1}`, 1)},
		{name: "other session", change: func(e []history.Entry) { e[1].Session.SessionID = "B" }},
		{name: "other agent", change: func(e []history.Entry) { e[1].Session.Kind = "codex" }},
		{name: "legacy observation", change: func(e []history.Entry) { e[1].Session = nil }},
		{name: "fresh result lifetime", successor: `agent_call_result {"conversation_id":"chat","lifetime_id":"22222222-2222-4222-8222-222222222222","tool_call_id":"other","status":"completed","occurred_at":"2026-01-01T00:00:00Z"}`},
		{name: "fresh task lifetime", successor: `background_task_observed {"conversation_id":"chat","lifetime_id":"22222222-2222-4222-8222-222222222222","task_id":"other","occurred_at":"2026-01-01T00:00:00Z"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.observation == "" {
				tc.observation = observation
			}
			if tc.successor == "" {
				tc.successor = `thinking_delta {"turn_id":"t","text":"private"}`
			}
			entries := testReplyEntries(replyUser, tc.observation, tc.successor,
				`tool_result {"turn_id":"t","tool_use_id":"child","is_error":false}`, replyText,
				`main_turn_interrupted {"turn_id":"t","turn_opened_entry_id":4,"cause":"restart","occurred_at":"2026-01-01T00:00:00Z"}`, replyEnd)
			if tc.change != nil {
				tc.change(entries)
			}
			for _, chunk := range []int{1, len(entries)} {
				w := newMemoryTranscriptReader(nil, "chat")
				for start := 0; start < len(entries); start += chunk {
					testMemoryMust(t, w.feed(entries[start:min(start+chunk, len(entries))]))
				}
				if strings.Contains(testReplyBody(t, w.files(conversations.Conversation{ID: "chat"}), "question"), "Speaker: assistant") {
					t.Fatal("unrelated ownership hid a valid main report opening")
				}
			}
		})
	}
}

func TestMemoryTranscriptReplies(t *testing.T) {
	entries := testReplyEntries(replyUser, replyOpen,
		`assistant_delta {"turn_id":"t","text":"  before\n"}`,
		"assistant_delta {\"turn_id\":\"t\",\"text\":\"\\n```  \"}",
		`tool_use {"turn_id":"t","tool_use_id":"call","name":"Bash","input_summary":"secret tool"}`,
		`assistant_delta {"turn_id":"t","text":"between  "}`,
		`tool_use {"turn_id":"t","tool_use_id":"call2","name":"Read"}`,
		`assistant_delta {"turn_id":"t","text":"after\n"}`,
		`message {"role":"user","text":"another question"}`, replyEnd)
	for _, kind := range []string{"claude", "codex"} {
		t.Run(kind, func(t *testing.T) {
			for i := range entries {
				entries[i].Session.Kind = kind
			}
			w := newMemoryTranscriptReader(nil, "chat")
			testMemoryMust(t, w.feed(entries[:9]))
			before := testReplyBody(t, w.files(conversations.Conversation{ID: "chat"}), "question")
			if strings.Contains(before, "Speaker: assistant") {
				t.Fatal("run closure released unfinished reply")
			}
			testMemoryMust(t, w.feed(entries[9:]))
			after := w.files(conversations.Conversation{ID: "chat"})
			body := testReplyBody(t, after, "question")
			for _, want := range []string{"  before\n\n```  ", "between  ", "after\n", "Message: chat/3\nTimestamp: 1970-01-01T00:00:03Z", "Agent: " + kind, "Last text entry: 9\n", "Last delivered entry: 9\n"} {
				if !strings.Contains(body, want) {
					t.Fatalf("missing %q: %s", want, body)
				}
			}
			if strings.Count(body, "Speaker: assistant") != 3 || strings.Contains(body, "secret tool") {
				t.Fatal(body)
			}
			if !reflect.DeepEqual(after, testTranscriptView(t, entries)) {
				t.Fatal("reconstruction differs")
			}
		})
	}
}
func TestMemoryTranscriptReplyTerminals(t *testing.T) {
	for _, tc := range []struct {
		name, terminal string
		release        bool
	}{
		{"normal", replyEnd, true},
		{"error", `turn_end {"turn_id":"t","stop_reason":"error","is_error":true,"outcome":"error"}`, true},
		{"cancelled", `turn_end {"turn_id":"t","stop_reason":"cancelled","terminal_reason":"cancelled"}`, true},
		{"interrupted", `main_turn_interrupted {"turn_id":"t","cause":"child_exit","occurred_at":"2026-01-01T00:00:00Z","turn_opened_entry_id":2}`, false},
		{"child", `turn_end {"turn_id":"t","stop_reason":"end_turn","parent_tool_use_id":"child"}`, false},
		{"foreign", `turn_end {"conversation_id":"elsewhere","turn_id":"t","stop_reason":"end_turn"}`, false},
		{"wrong turn", `turn_end {"turn_id":"other","stop_reason":"end_turn"}`, false},
		{"null", `turn_end {"turn_id":"t","stop_reason":null}`, false},
		{"missing", `turn_end {"turn_id":"t"}`, false},
		{"lost identity", `turn_end {"turn_id":"t","stop_reason":"end_turn","truncated_fields":["turn_id"]}`, false},
		{"null reference", `main_turn_interrupted {"turn_id":"t","cause":"child_exit","occurred_at":"2026-01-01T00:00:00Z","turn_opened_entry_id":null}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			entries := testReplyEntries(replyUser, replyOpen, replyText, tc.terminal)
			body := testReplyBody(t, testTranscriptView(t, entries), "question")
			if strings.Contains(body, "Speaker: assistant") != tc.release {
				t.Fatal(body)
			}
			entries = append(entries, testTranscriptEntry(5, "turn_end", `{"turn_id":"t","stop_reason":"end_turn"}`, "A"))
			if strings.Contains(testReplyBody(t, testTranscriptView(t, entries), "question"), "Speaker: assistant") != (tc.name != "interrupted") {
				t.Fatal("first terminal did not win")
			}
		})
	}
}
func TestMemoryTranscriptReplyScopes(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func([]history.Entry)
	}{
		{"agent", func(e []history.Entry) { e[3].Session.Kind = "codex" }},
		{"session", func(e []history.Entry) { e[3].Session.SessionID = "B" }},
		{"tagged legacy", func(e []history.Entry) { e[3].Session = nil }},
		{"legacy tagged", func(e []history.Entry) { e[0].Session = nil; e[1].Session = nil; e[2].Session = nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			entries := testReplyEntries(replyUser, replyOpen, replyText, replyEnd)
			tc.change(entries)
			if strings.Contains(testReplyBody(t, testTranscriptView(t, entries), "question"), "Speaker: assistant") {
				t.Fatal("foreign source completed reply")
			}
		})
	}
	entries := testReplyEntries(replyUser, replyOpen, replyText,
		`main_turn_interrupted {"turn_id":"t","cause":"restart","occurred_at":"2026-01-01T00:00:00Z","turn_opened_entry_id":2}`,
		replyUser, replyOpen, `assistant_delta {"turn_id":"t","text":"fresh answer"}`,
		`main_turn_interrupted {"turn_id":"t","cause":"restart","occurred_at":"2026-01-01T00:00:00Z","turn_opened_entry_id":2}`, replyEnd)
	body := testReplyBody(t, testTranscriptView(t, entries), "question")
	if strings.Count(body, "Speaker: assistant") != 1 || !strings.Contains(body, "fresh answer") {
		t.Fatal(body)
	}
	// A divider's displayed successor does not join legacy and tagged evidence.
	entries = testReplyEntries(`session_divider {"cause":"agent_switch","previous_session_id":"old","new_session_id":"A","next_agent":"claude","occurred_at":"2026-01-01T00:00:00Z"}`, replyUser, replyText, replyEnd)
	entries[1].Session = nil
	entries[2].Session = nil
	if strings.Contains(testReplyBody(t, testTranscriptView(t, entries), "question"), "Speaker: assistant") {
		t.Fatal("display fallback joined tagged ending")
	}
	entries[3].Session = nil
	if !strings.Contains(testReplyBody(t, testTranscriptView(t, entries), "question"), "Speaker: assistant") {
		t.Fatal("legacy completion lost")
	}
	// An untagged lifetime cannot cross a legacy boundary, but a referenced interruption can.
	entries = testReplyEntries(replyUser, replyText, `session_divider {"cause":"idle_sleep","previous_session_id":"A","occurred_at":"2026-01-01T00:00:00Z"}`, replyEnd)
	for i := range entries {
		entries[i].Session = nil
	}
	if strings.Contains(testReplyBody(t, testTranscriptView(t, entries), "question"), "Speaker: assistant") {
		t.Fatal("legacy scope crossed")
	}
}
func TestMemoryTranscriptReplyExclusions(t *testing.T) {
	entries := testReplyEntries(replyUser, replyOpen, replyText, replyEnd,
		replyOpen, `assistant_delta {"turn_id":"t","text":"wrap-up excluded"}`, replyEnd,
		`send_accepted {"text":"queued excluded","accepted_at":"2026-01-01T00:00:00Z"}`,
		`main_turn_opened {"turn_id":"background","occurred_at":"2026-01-01T00:00:00Z"}`,
		`assistant_delta {"turn_id":"background","text":"background excluded"}`,
		`turn_end {"turn_id":"background","stop_reason":"end_turn"}`,
		`thinking_delta {"turn_id":"t","text":"reasoning excluded"}`,
		`assistant_delta {"turn_id":"t","text":"child excluded","parent_tool_use_id":"child"}`,
		`message {"role":"system","text":"instructions excluded"}`)
	body := testReplyBody(t, testTranscriptView(t, entries), "question")
	if strings.Contains(body, "excluded") || strings.Count(body, "Speaker: assistant") != 1 {
		t.Fatal(body)
	}
	for _, provenance := range []string{"none", "unknown", "different"} {
		t.Run(provenance, func(t *testing.T) {
			e := testReplyEntries(replyUser, replyText, replyEnd)
			switch provenance {
			case "none":
				e[0].Session = &history.SessionProvenance{Kind: "none"}
			case "unknown":
				e[0].Session = nil
			case "different":
				e[0].Session.SessionID = "B"
			}
			for _, b := range testTranscriptView(t, e) {
				if strings.Contains(b, "Speaker: assistant") {
					t.Fatal(b)
				}
			}
		})
	}
	e := testReplyEntries(replyUser, replyText, replyEnd)
	for i := range e {
		e[i].Session = nil
	}
	body = testReplyBody(t, testTranscriptView(t, e), "question")
	if !strings.Contains(body, "Session: unknown") || !strings.Contains(body, "Agent: unknown") || !strings.Contains(body, "Speaker: assistant") {
		t.Fatal(body)
	}
}
func TestMemoryTranscriptReplyBoundaries(t *testing.T) {
	entries := testReplyEntries(replyUser, replyText, replyEnd,
		`session_divider {"cause":"agent_switch","previous_session_id":"A","new_session_id":"B","previous_agent":"claude","next_agent":"codex","occurred_at":"2026-01-01T00:00:00Z"}`,
		`message {"role":"user","text":"successor question"}`,
		`assistant_delta {"turn_id":"next","text":"successor answer"}`,
		`turn_end {"turn_id":"next","stop_reason":"end_turn"}`)
	for i := 4; i < 7; i++ {
		entries[i].Session = &history.SessionProvenance{Kind: "codex", SessionID: "B"}
	}
	w := newMemoryTranscriptReader(nil, "chat")
	testMemoryMust(t, w.feed(entries))
	before := w.files(conversations.Conversation{ID: "chat"})
	entries = append(entries, testTranscriptEntry(8, "assistant_delta", `{"turn_id":"t","text":"late "}`, "A"), testTranscriptEntry(9, "assistant_delta", `{"turn_id":"t","text":"tail  \n"}`, "A"))
	testMemoryMust(t, w.feed(entries[7:]))
	after := w.files(conversations.Conversation{ID: "chat"})
	if testReplyBody(t, before, "successor answer") != testReplyBody(t, after, "successor answer") {
		t.Fatal("successor changed")
	}
	body := testReplyBody(t, after, "late tail  \n")
	for _, want := range []string{"Closing entry: 4\n", "Last delivered entry: 1\n", "Last text entry: 9\n", "Message: chat/8\nTimestamp: 1970-01-01T00:00:08Z"} {
		if !strings.Contains(body, want) {
			t.Fatal(body)
		}
	}
	testMemoryMust(t, w.feed([]history.Entry{testTranscriptEntry(10, "assistant_delta", `{"turn_id":"t","text":"extended"}`, "A")}))
	body = testReplyBody(t, w.files(conversations.Conversation{ID: "chat"}), "late tail  \nextended")
	if !strings.Contains(body, "Last text entry: 10\n") || !strings.Contains(body, "Message: chat/8\nTimestamp: 1970-01-01T00:00:08Z") {
		t.Fatal(body)
	}
}
func TestMemoryTranscriptReplyWorker(t *testing.T) {
	home := testManagedHome(t)
	base := filepath.Join(home, "vault")
	testMemoryMust(t, os.Mkdir(base, 0700))
	settings := testManagedMemory()
	reg, err := conversations.Load(filepath.Join(home, "registry.json"))
	testMemoryMust(t, err)
	id, err := conversations.NewID()
	testMemoryMust(t, err)
	reg.Create(conversations.Conversation{ID: id, CurrentSessionID: "wrong"})
	h := history.New(filepath.Join(home, "history"))
	appendFact := func(fact string) {
		t.Helper()
		typ, payload, _ := strings.Cut(fact, " ")
		_, err := h.AppendWithMetadata(id, typ, json.RawMessage(payload), time.Now(), history.Metadata{Session: &history.SessionProvenance{Kind: "claude", SessionID: "A"}})
		testMemoryMust(t, err)
	}
	appendFact(replyUser)
	appendFact(replyOpen)
	appendFact(replyText)
	ticks := make(chan time.Time, 1)
	published := make(chan struct{}, 10)
	stop := startMemoryTranscripts(context.Background(), &settings, base, h, reg, slog.New(slog.NewTextHandler(io.Discard, nil)), memoryTranscriptHooks{ticks: ticks, beforeRename: func() { published <- struct{}{} }})
	t.Cleanup(stop)
	wait := func() {
		t.Helper()
		select {
		case <-published:
		case <-time.After(5 * time.Second):
			t.Fatal("no publication")
		}
	}
	wait()
	dir := filepath.Join(home, ".pyry", "memory", "recent-transcripts")
	testShadowWait(t, func() bool { return len(testTranscriptDisk(t, dir)) == 1 })
	if strings.Contains(testReplyBody(t, testTranscriptDisk(t, dir), "question"), "Speaker: assistant") {
		t.Fatal("unfinished published")
	}
	appendFact(replyEnd)
	ticks <- time.Now().Add(memoryTranscriptInterval)
	wait()
	testShadowWait(t, func() bool {
		for _, b := range testTranscriptDisk(t, dir) {
			if strings.Contains(b, "Speaker: assistant") {
				return true
			}
		}
		return false
	})
	if memoryTranscriptInterval > 60*time.Second {
		t.Fatalf("interval %s exceeds bound", memoryTranscriptInterval)
	}
}

func TestMemoryTranscriptReplyReuse(t *testing.T) {
	// A second delivered message still belongs to the unfinished interval, so
	// completing it cannot qualify an assistant-only lifetime opened afterwards.
	e := testReplyEntries(replyUser, replyOpen, replyText, replyUser, replyEnd, replyOpen,
		`assistant_delta {"turn_id":"t","text":"wrap-up excluded"}`, replyEnd)
	if b := testReplyBody(t, testTranscriptView(t, e), "question"); strings.Contains(b, "excluded") {
		t.Fatal(b)
	}
	e = testReplyEntries(replyUser, replyText, replyEnd, replyUser, replyEnd,
		replyOpen, `assistant_delta {"turn_id":"t","text":"next exchange"}`, replyEnd)
	if b := testReplyBody(t, testTranscriptView(t, e), "question"); !strings.Contains(b, "next exchange") {
		t.Fatal("duplicate ending consumed next user exchange")
	}

	// Missing/malformed/repeated openings cannot move a lifetime into another generation.
	e = testReplyEntries(replyUser, replyOpen, replyText,
		`main_turn_opened {"turn_id":"t","occurred_at":null}`, replyEnd)
	if b := testReplyBody(t, testTranscriptView(t, e), "question"); !strings.Contains(b, "Speaker: assistant") {
		t.Fatal(b)
	}
	// Reusing the routing ID does not move late runs to its successor generation.
	e = testReplyEntries(replyUser, replyText, replyEnd,
		`session_divider {"cause":"operator_reset","previous_session_id":"A","new_session_id":"B","occurred_at":"2026-01-01T00:00:00Z"}`,
		`session_divider {"cause":"agent_switch","previous_session_id":"B","new_session_id":"A","occurred_at":"2026-01-02T00:00:00Z"}`,
		`message {"role":"user","text":"reused question"}`,
		`assistant_delta {"turn_id":"fresh","text":"fresh reply"}`,
		`turn_end {"turn_id":"fresh","stop_reason":"end_turn"}`)
	w := newMemoryTranscriptReader(nil, "chat")
	testMemoryMust(t, w.feed(e))
	before := w.files(conversations.Conversation{ID: "chat"})
	testMemoryMust(t, w.feed([]history.Entry{testTranscriptEntry(9, "assistant_delta", `{"turn_id":"t","text":"late original"}`, "A")}))
	after := w.files(conversations.Conversation{ID: "chat"})
	if testReplyBody(t, before, "fresh reply") != testReplyBody(t, after, "fresh reply") {
		t.Fatal("reused successor changed")
	}
	if b := testReplyBody(t, after, "late original"); !strings.Contains(b, "Closing entry: 4\n") || strings.Contains(b, "fresh reply") {
		t.Fatal(b)
	}
	// A legacy recovery reference crosses boundaries only to its original opening.
	e = testReplyEntries(replyUser, replyText,
		`session_divider {"cause":"idle_sleep","previous_session_id":"A","occurred_at":"2026-01-01T00:00:00Z"}`,
		`main_turn_interrupted {"turn_id":"t","turn_opened_entry_id":2,"cause":"restart","occurred_at":"2026-01-01T00:00:00Z"}`,
		replyEnd)
	for i := range e {
		e[i].Session = nil
	}
	if b := testReplyBody(t, testTranscriptView(t, e), "question"); strings.Contains(b, "Speaker: assistant") {
		t.Fatal(b)
	}
}

func TestMemoryTranscriptReplyImplicitOpening(t *testing.T) {
	for _, first := range []string{`tool_use {"turn_id":"t","tool_use_id":"call","name":"Read"}`, `tool_result {"turn_id":"t","tool_use_id":"call","is_error":false}`} {
		e := testReplyEntries(replyUser, first, replyText,
			`main_turn_interrupted {"turn_id":"t","turn_opened_entry_id":2,"cause":"restart","occurred_at":"2026-01-01T00:00:00Z"}`, replyEnd)
		if b := testReplyBody(t, testTranscriptView(t, e), "question"); strings.Contains(b, "Speaker: assistant") {
			t.Fatal(b)
		}
	}
	e := testReplyEntries(replyUser, `tool_use {"turn_id":"t","tool_use_id":"child","name":"Read","parent_tool_use_id":"parent"}`,
		`tool_result {"turn_id":"t","tool_use_id":"child","is_error":false}`, replyText,
		`main_turn_interrupted {"turn_id":"t","turn_opened_entry_id":4,"cause":"restart","occurred_at":"2026-01-01T00:00:00Z"}`, replyEnd)
	if b := testReplyBody(t, testTranscriptView(t, e), "question"); strings.Contains(b, "Speaker: assistant") {
		t.Fatal(b)
	}
}

func TestMemoryTranscriptReplyObservedChild(t *testing.T) {
	e := testReplyEntries(replyUser,
		`agent_call_observed {"conversation_id":"chat","lifetime_id":"11111111-1111-4111-8111-111111111111","tool_call_id":"child","tool":"Agent","parent_tool_call_id":"outer","occurred_at":"2026-01-01T00:00:00Z"}`,
		`tool_use {"turn_id":"t","tool_use_id":"child","name":"Agent"}`,
		`tool_result {"turn_id":"t","tool_use_id":"child","is_error":false}`, replyText,
		`main_turn_interrupted {"turn_id":"t","turn_opened_entry_id":5,"cause":"restart","occurred_at":"2026-01-01T00:00:00Z"}`, replyEnd)
	if b := testReplyBody(t, testTranscriptView(t, e), "question"); strings.Contains(b, "Speaker: assistant") {
		t.Fatal(b)
	}
}

func TestMemoryTranscriptReplyTerminalBeforeOpening(t *testing.T) {
	for _, source := range []string{"claude", "codex", "legacy"} {
		for _, terminal := range []struct{ name, fact string }{
			{"ending", replyEnd},
			{"interruption", `main_turn_interrupted {"turn_id":"t","cause":"child_exit","occurred_at":"2026-01-01T00:00:00Z"}`},
		} {
			for _, chunk := range []int{1, 4} {
				t.Run(fmt.Sprintf("%s/%s/chunk%d", source, terminal.name, chunk), func(t *testing.T) {
					entries := testReplyEntries(terminal.fact, replyUser, replyOpen, replyText, replyEnd)
					for i := range entries {
						if source == "legacy" {
							entries[i].Session = nil
						} else {
							entries[i].Session.Kind = source
						}
					}
					w := newMemoryTranscriptReader(nil, "chat")
					for start := 0; start < 4; start += chunk {
						testMemoryMust(t, w.feed(entries[start:min(start+chunk, 4)]))
					}
					before := w.files(conversations.Conversation{ID: "chat"})
					body := testReplyBody(t, before, "question")
					if strings.Contains(body, "Speaker: assistant") || !strings.Contains(body, "Last text entry: 2\n") {
						t.Fatalf("terminal before opening qualified fresh text: %s", body)
					}
					if !reflect.DeepEqual(before, testTranscriptView(t, entries[:4])) {
						t.Fatal("withholding differs on reconstruction")
					}
					testMemoryMust(t, w.feed(entries[4:]))
					after := w.files(conversations.Conversation{ID: "chat"})
					body = testReplyBody(t, after, "answer")
					for _, want := range []string{"Speaker: assistant", "Message: chat/4\nTimestamp: 1970-01-01T00:00:04Z", "Last delivered entry: 2\nLast text entry: 4\n"} {
						if !strings.Contains(body, want) {
							t.Fatalf("missing %q after fresh ending: %s", want, body)
						}
					}
					if !reflect.DeepEqual(after, testTranscriptView(t, entries)) {
						t.Fatal("completion differs on reconstruction")
					}
				})
			}
		}
	}
}

func TestMemoryTranscriptReplyReplacementPermission(t *testing.T) {
	for _, established := range []bool{false, true} {
		for _, boundary := range []string{"session_divider", "session_transition"} {
			for _, chunk := range []int{1, 10} {
				t.Run(fmt.Sprintf("established%t/%s/chunk%d", established, boundary, chunk), func(t *testing.T) {
					facts := []string{replyUser}
					if established {
						facts = append(facts, replyOpen, replyText, replyEnd, `message {"role":"user","text":"unmatched question"}`)
					}
					closing := len(facts) + 1
					facts = append(facts,
						boundary+` {"cause":"operator_reset","reason":"clear","previous_session_id":"A","new_session_id":"B","occurred_at":"2026-01-01T00:00:00Z"}`,
						boundary+` {"cause":"agent_switch","reason":"clear","previous_session_id":"B","new_session_id":"A","occurred_at":"2026-01-02T00:00:00Z"}`,
						`main_turn_opened {"turn_id":"fresh","occurred_at":"2026-01-02T00:00:00Z"}`,
						`assistant_delta {"turn_id":"fresh","text":"successor reply excluded"}`,
						`turn_end {"turn_id":"fresh","stop_reason":"end_turn"}`)
					entries := testReplyEntries(facts...)
					w := newMemoryTranscriptReader(nil, "chat")
					for start := 0; start < len(entries); start += chunk {
						testMemoryMust(t, w.feed(entries[start:min(start+chunk, len(entries))]))
					}
					before := w.files(conversations.Conversation{ID: "chat"})
					for _, body := range before {
						if strings.Contains(body, "excluded") {
							t.Fatalf("pending predecessor exchange qualified successor: %s", body)
						}
					}
					if !reflect.DeepEqual(before, testTranscriptView(t, entries)) {
						t.Fatal("replacement differs on reconstruction")
					}
					if !established {
						return
					}
					lateID := uint64(len(entries) + 1)
					entries = append(entries,
						testTranscriptEntry(lateID, "assistant_delta", `{"turn_id":"t","text":"late "}`, "A"),
						testTranscriptEntry(lateID+1, "assistant_delta", `{"turn_id":"t","text":"tail  \n"}`, "A"))
					testMemoryMust(t, w.feed(entries[len(entries)-2:len(entries)-1]))
					testMemoryMust(t, w.feed(entries[len(entries)-1:]))
					after := w.files(conversations.Conversation{ID: "chat"})
					for name, body := range before {
						if !strings.Contains(body, "question") && after[name] != body {
							t.Fatal("late predecessor text changed successor file")
						}
					}
					body := testReplyBody(t, after, "late tail  \n")
					for _, want := range []string{"answer", fmt.Sprintf("Closing entry: %d\n", closing), "Last delivered entry: 5\n", fmt.Sprintf("Last text entry: %d\n", lateID+1), fmt.Sprintf("Message: chat/%d\nTimestamp: %s", lateID, time.Unix(int64(lateID), 0).UTC().Format(time.RFC3339Nano))} {
						if !strings.Contains(body, want) {
							t.Fatalf("missing %q after late predecessor text: %s", want, body)
						}
					}
					if !reflect.DeepEqual(after, testTranscriptView(t, entries)) {
						t.Fatal("late predecessor reconstruction differs")
					}
				})
			}
		}
	}
}

func TestMemoryTranscriptReplyPendingAcrossUnloading(t *testing.T) {
	for _, cause := range []string{"idle_sleep", "capacity_eviction", "daemon_restart", "recovery"} {
		t.Run(cause, func(t *testing.T) {
			entries := testReplyEntries(replyUser,
				fmt.Sprintf(`session_divider {"cause":%q,"previous_session_id":"A","new_session_id":"A","occurred_at":"2026-01-01T00:00:00Z"}`, cause),
				replyOpen, replyText, replyEnd)
			body := testReplyBody(t, testTranscriptView(t, entries), "question")
			if !strings.Contains(body, "answer") || !strings.Contains(body, "State: open\n") || strings.Contains(body, "Closing entry:") {
				t.Fatal(body)
			}
		})
	}
}
