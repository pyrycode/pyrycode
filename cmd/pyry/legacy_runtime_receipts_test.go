package main

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

func TestStoredHistoryVocabulary(t *testing.T) {
	t.Parallel()
	// Explicit wire vocabulary keeps this oracle independent of legacyHistoryType.
	types := []string{
		"message", "assistant_delta", "tool_use", "tool_result", "tool_denied",
		"turn_end", "banner", "background_task_started", "background_task_updated",
		"compaction_boundary", "model_refusal_fallback", "model_refusal_no_fallback",
		"unrecognized_message", "session_transition", "turn_state", "stall", "api_retry",
		"compacting", "tool_progress", "thinking_progress", "background_task_roster",
		"background_task_progress", "rate_limited", "context_usage", "model_announced",
		"session_facts", "mcp_status", "model_list", "slash_command_list",
		"main_turn_opened", "main_tool_interrupted", "main_turn_interrupted", "session_divider",
		"future_content", "invalid_runtime_fact",
	}
	for _, typ := range types {
		t.Run(typ, func(t *testing.T) {
			for _, visibility := range []string{"absent", "false", "true"} {
				t.Run(visibility, func(t *testing.T) {
					dir := t.TempDir()
					store := history.New(dir)
					var shown *bool
					if visibility != "absent" {
						value := visibility == "true"
						shown = &value
					}
					entryType, payload := typ, json.RawMessage(`{}`)
					if typ == "invalid_runtime_fact" {
						entryType = historyTurnOpened
					}
					metadata := history.Metadata{Shown: shown, Session: &history.SessionProvenance{Kind: "claude", SessionID: "stored-session"}}
					id := conversations.ConversationID(testConvID)
					if _, err := store.AppendWithMetadata(id, entryType, payload, historyTS, metadata); err != nil {
						t.Fatal(err)
					}
					for _, reader := range []*history.Store{store, history.New(dir)} {
						raw, err := reader.Page(id, "", 1)
						if err != nil || len(raw.Entries) != 1 || raw.Entries[0].Type != entryType || !bytes.Equal(raw.Entries[0].Payload, payload) || !raw.Entries[0].TS.Equal(historyTS) || !reflect.DeepEqual(raw.Entries[0].Shown, shown) || !reflect.DeepEqual(raw.Entries[0].Session, metadata.Session) {
							t.Fatalf("raw entry changed: %+v / %v", raw, err)
						}
						if got, err := reader.LatestEntryID(id); err != nil || got != 1 {
							t.Fatalf("raw cursor=%d,%v", got, err)
						}
					}
				})
			}
		})
	}
}

func TestRuntimeHistoryCompletionTails(t *testing.T) {
	t.Parallel()
	for _, ending := range []struct {
		name  string
		event turnevent.TurnEnd
		shown bool
	}{
		{"normal", turnevent.TurnEnd{Reason: turnevent.TurnEndReasonEndTurn}, false},
		{"failed", turnevent.TurnEnd{Reason: turnevent.TurnEndReasonEndTurn, IsError: true, Outcome: "error_during_execution"}, true},
		{"stopped", turnevent.TurnEnd{Reason: turnevent.TurnEndReasonCancelled}, true},
	} {
		t.Run(ending.name, func(t *testing.T) {
			dir := t.TempDir()
			store := history.New(dir)
			e := newInteractiveTurnEmitterV2(nil, historyOnlyBroadcaster{}, discardLogger())
			e.hist, e.runtimeFacts = store, true
			ctx := context.Background()
			source := history.SessionProvenance{Kind: "claude", SessionID: "session"}
			e.emit(ctx, testConvID, protocol.TypeMessage, protocol.MessagePayload{ConversationID: testConvID, Role: "user", Text: "request"})
			e.HandleFor(ctx, testConvID, turnevent.TextChunk{Text: "reply"}, source)
			e.HandleFor(ctx, testConvID, ending.event, source)
			raw := historyEntries(t, store, testConvID)
			if len(raw) != 6 || raw[4].Type != protocol.TypeTurnEnd || raw[4].Shown == nil || *raw[4].Shown != ending.shown {
				t.Fatalf("completion raw visibility=%+v", raw)
			}
		})
	}
}
