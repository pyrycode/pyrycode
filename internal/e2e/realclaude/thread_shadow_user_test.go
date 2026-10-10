//go:build !e2e_realclaude

package realclaude

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/thread"
)

type testShadowFact struct{ typ, payload string }

// Synthetic user-content controls stay in memory and never become live artifacts.
func testShadowUserPair(t *testing.T) (shadowHistory, shadowExpected) {
	t.Helper()
	return testShadowPairWith(t, nil)
}

// testShadowPairWith inserts extra parent-attributed facts after the controlled
// child's text, before the foreground Agent result, and rebuilds every
// checkpoint from the fold. The result is synthetic, never a live artifact.
func testShadowPairWith(t *testing.T, children []testShadowFact) (shadowHistory, shadowExpected) {
	t.Helper()
	h := shadowHistory{Conversation: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"}
	e := shadowExpected{}
	facts := []testShadowFact{
		{protocol.TypeAssistantDelta, `{"turn_id":"main","text":"SHADOW_BEFORE"}`},
		{protocol.TypeToolUse, `{"turn_id":"main","tool_use_id":"bash","name":"Bash","input":{"command":"echo SHADOW_TOOL"}}`},
		{"send_accepted", `{"message_id":"shadow-queued","device_id":"accepted-device","text":"SHADOW_QUEUED: accepted text","attachment_ids":["accepted-attachment"],"accepted_at":"2026-10-09T00:00:00Z","client_sent_at":"2026-10-08T23:59:59Z"}`},
		{protocol.TypeToolResult, `{"turn_id":"main","tool_use_id":"bash","result_summary":"SHADOW_TOOL","is_error":false}`},
		{protocol.TypeAssistantDelta, `{"turn_id":"main","text":"SHADOW_AFTER"}`},
		{protocol.TypeToolUse, `{"turn_id":"main","tool_use_id":"agent","name":"Agent","input":{}}`},
		{protocol.TypeAssistantDelta, `{"turn_id":"child","parent_tool_use_id":"agent","text":"SHADOW_CHILD"}`},
		{protocol.TypeToolResult, `{"turn_id":"main","tool_use_id":"agent","result_summary":"SHADOW_CHILD","is_error":false}`},
		{protocol.TypeTurnEnd, `{"turn_id":"main","stop_reason":"end_turn"}`},
		{protocol.TypeMessage, `{"role":"user","message_id":"shadow-queued","device_id":"receiving-device","text":"SHADOW_QUEUED: delivered text","attachment_ids":["delivered-attachment"],"client_sent_at":"2026-10-09T00:00:01Z"}`},
		{"send_delivered", `{"accepted_entry_id":3,"delivery_entry_id":%d,"reason":"delivered","occurred_at":"2026-10-09T00:00:02Z"}`},
		{protocol.TypeAssistantDelta, `{"turn_id":"reply","text":"SHADOW_REPLY"}`},
		{protocol.TypeTurnEnd, `{"turn_id":"reply","stop_reason":"end_turn"}`},
		{protocol.TypeToolUse, `{"turn_id":"closure","tool_use_id":"closure-bash","name":"Bash","input":{}}`},
		{"main_tool_interrupted", `{"turn_id":"closure","tool_call_id":"closure-bash","cause":"operator_reset","occurred_at":"2026-10-09T00:00:03Z"}`},
		{"main_turn_interrupted", `{"turn_id":"closure","cause":"operator_reset","occurred_at":"2026-10-09T00:00:03Z"}`},
		{"session_divider", `{"cause":"operator_reset","previous_session_id":"session","new_session_id":"next","previous_agent":"claude","next_agent":"claude","occurred_at":"2026-10-09T00:00:03Z"}`},
	}
	facts = append(facts[:7], append(append([]testShadowFact(nil), children...), facts[7:]...)...)
	var versions []uint64
	for i, fact := range facts {
		switch {
		case fact.typ == "send_accepted", fact.typ == protocol.TypeTurnEnd && len(versions) == 1 && !strings.Contains(fact.payload, "parent_tool_use_id"), fact.typ == "send_delivered", fact.typ == "session_divider":
			versions = append(versions, uint64(i+1))
		}
		if fact.typ == "send_delivered" {
			facts[i].payload = fmt.Sprintf(fact.payload, i)
			fact = facts[i]
		}
		var payload map[string]any
		if err := json.Unmarshal([]byte(fact.payload), &payload); err != nil {
			t.Fatal(err)
		}
		payload["conversation_id"] = h.Conversation
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		h.Entries = append(h.Entries, history.Entry{ID: uint64(i + 1), Type: fact.typ, Payload: raw, Session: &history.SessionProvenance{SessionID: "session", Kind: "claude"}})
		switch fact.typ {
		case protocol.TypeAssistantDelta, protocol.TypeToolUse, protocol.TypeToolResult, protocol.TypeMessage, protocol.TypeTurnEnd:
			e.Legacy = append(e.Legacy, protocol.Envelope{Type: fact.typ, Payload: raw})
		case "send_accepted":
			queued, err := json.Marshal(protocol.QueueStatePayload{ConversationID: h.Conversation, Queued: []protocol.QueuedItem{{MessageID: "shadow-queued", Text: payload["text"].(string)}}})
			if err != nil {
				t.Fatal(err)
			}
			e.Legacy = append(e.Legacy, protocol.Envelope{Type: protocol.TypeQueueState, Payload: queued})
		case "session_divider":
			e.Legacy = append(e.Legacy, protocol.Envelope{Type: protocol.TypeSessionTransition, Payload: raw})
		}
	}
	h.Provenance = shadowProvenance{Schema: 1, Capture: "synthetic-user-control", ClaudeVersion: "control", DaemonCommit: strings.Repeat("a", 40), HistorySHA256: shadowDigest(h.Entries), Checks: map[string]shadowCheck{shadowScenarioCheck: {Executed: 1}}}
	if len(versions) != len(shadowChecks) {
		t.Fatal("synthetic checkpoints missing")
	}
	for i, version := range versions {
		fold := thread.New(h.Conversation)
		if err := fold.Feed(h.Entries[:version]); err != nil {
			t.Fatal(err)
		}
		e.Checkpoints = append(e.Checkpoints, shadowCheckpoint{Name: shadowChecks[i], Version: version, Items: fold.Items()})
		h.Provenance.Checks[shadowChecks[i]] = shadowCheck{Executed: 1}
	}
	e.Provenance = h.Provenance
	if err := shadowValidatePair(h, e, false); err != nil {
		t.Fatal(err)
	}
	return h, e
}

func TestThreadShadowPairRejectsUserContent(t *testing.T) {
	for cp, name := range shadowChecks {
		t.Run(name, func(t *testing.T) {
			for _, field := range []string{"content", "text", "attachment_ids", "device_id", "message_id", "accepted_at", "client_sent_at"} {
				for _, lost := range []bool{false, true} {
					label := "changed_" + field
					if lost {
						label = "lost_" + field
					}
					t.Run(label, func(t *testing.T) {
						h, e := testShadowUserPair(t)
						for i := range e.Checkpoints[cp].Items {
							item := &e.Checkpoints[cp].Items[i]
							if item.ID != 3 {
								continue
							}
							var content map[string]any
							if err := json.Unmarshal(item.Content, &content); err != nil {
								t.Fatal(err)
							}
							if field == "content" {
								content = map[string]any{"text": "unrelated"}
								if lost {
									content = nil
								}
							} else if lost {
								delete(content, field)
							} else {
								content[field] = "unrelated"
							}
							var err error
							item.Content, err = json.Marshal(content)
							if err != nil {
								t.Fatal(err)
							}
						}
						if err := shadowValidatePair(h, e, false); err == nil {
							t.Fatal("lost or changed user content accepted")
						}
					})
				}
			}
		})
	}
}

func TestThreadShadowPairRejectsUserLegacyMismatch(t *testing.T) {
	for _, typ := range []string{protocol.TypeMessage, protocol.TypeQueueState} {
		t.Run(typ, func(t *testing.T) {
			h, e := testShadowUserPair(t)
			for i := range e.Legacy {
				if e.Legacy[i].Type == typ {
					e.Legacy[i].Payload = json.RawMessage(strings.ReplaceAll(string(e.Legacy[i].Payload), "SHADOW_QUEUED:", "SHADOW_QUEUED: changed"))
				}
			}
			if shadowValidatePair(h, e, false) == nil {
				t.Fatal("raw and independently observed legacy user text mismatch accepted")
			}
		})
	}
}
