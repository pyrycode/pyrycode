package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"slices"
	"strings"
	"testing"

	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/relay"
	"github.com/pyrycode/pyrycode/internal/streamsup"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

const (
	initialThinkingSession = "session-initial-thinking"
	initialThinkingOther   = "22222222-2222-4222-8222-222222222222"
	initialThinkingSecret  = "INITIAL-THINKING-TEXT-MUST-STAY-PRIVATE-2522"
)

type initialThinkingPipeline struct {
	parser  *streamsup.Parser
	emitter *interactiveTurnEmitterV2
	busy    *turnBusyTracker
	bcast   *fakeInteractiveBcast
	logs    *bytes.Buffer
}

func newInitialThinkingPipeline(t *testing.T) *initialThinkingPipeline {
	t.Helper()
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	cur := &stubCursor{}
	cur.set(testConvID)
	bcast := &fakeInteractiveBcast{
		snapshots: [][]relay.ActiveConn{{{ConnID: "client", Interactive: true}}},
		pushErr:   map[string]error{"client": relay.ErrConnNotFound},
	}
	emitter := newInteractiveTurnEmitterV2(cur, bcast, logger)
	busy := newTurnBusyTracker(stubBusyResolve(map[string]string{
		initialThinkingSession: testConvID,
		"session-other":        initialThinkingOther,
	}), logger)
	parser := streamsup.NewParser(func(ev turnevent.Event) {
		busy.observe(initialThinkingSession, ev)
		emitter.Handle(context.Background(), ev)
	}, logger)
	return &initialThinkingPipeline{parser: parser, emitter: emitter, busy: busy, bcast: bcast, logs: &logs}
}

func writeInitialThinkingLines(t *testing.T, parser *streamsup.Parser, lines ...string) {
	t.Helper()
	for _, line := range lines {
		if _, err := parser.Write([]byte(line + "\n")); err != nil {
			t.Fatalf("Parser.Write() error = %v", err)
		}
	}
}

func TestInitialThinkingParserToEmitterTerminalPaths(t *testing.T) {
	t.Parallel()
	sources := []struct {
		name      string
		lines     []string
		wantTypes []string
	}{
		{
			name: "partial message thinking delta",
			lines: []string{
				`{"type":"stream_event","event":{"type":"message_start","message":{"id":"msg-thinking"}}}`,
				`{"type":"stream_event","event":{"type":"content_block_start","index":0,"content_block":{"type":"thinking"}}}`,
				`{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"` + initialThinkingSecret + `"}}}`,
			},
			wantTypes: []string{protocol.TypeTurnState},
		},
		{
			name:      "thinking progress reading",
			lines:     []string{`{"type":"system","subtype":"thinking_tokens","estimated_tokens":64,"estimated_tokens_delta":64}`},
			wantTypes: []string{protocol.TypeTurnState, protocol.TypeThinkingProgress},
		},
	}
	terminals := []struct {
		name       string
		line       string
		stopReason string
	}{
		{"normal completion", `{"type":"result","subtype":"success","is_error":false}`, "end_turn"},
		{"interruption", `{"type":"result","subtype":"error_during_execution","is_error":true}`, "cancelled"},
		{"error result", `{"type":"result","subtype":"error_max_turns","is_error":true}`, "end_turn"},
	}

	for _, source := range sources {
		source := source
		for _, terminal := range terminals {
			terminal := terminal
			t.Run(source.name+"/"+terminal.name, func(t *testing.T) {
				t.Parallel()
				p := newInitialThinkingPipeline(t)
				writeInitialThinkingLines(t, p.parser, source.lines...)

				if !p.emitter.inTurn || p.emitter.currentState != "thinking" || p.emitter.turnID == "" {
					t.Fatalf("initial thinking did not open the emitter: inTurn=%v state=%q turnID=%q",
						p.emitter.inTurn, p.emitter.currentState, p.emitter.turnID)
				}
				openedTurnID := p.emitter.turnID
				if !p.busy.Busy(testConvID) {
					t.Fatal("initial thinking did not mark its conversation busy")
				}
				if p.busy.Busy(initialThinkingOther) {
					t.Fatal("initial thinking changed a second conversation's busy state")
				}
				if got := pushTypes(p.bcast.pushes); !slices.Equal(got, source.wantTypes) {
					t.Fatalf("opening envelope order: got %v, want %v", got, source.wantTypes)
				}
				if got := turnStateValues(t, p.bcast.pushes); !slices.Equal(got, []string{"thinking"}) {
					t.Fatalf("opening turn states: got %v, want [thinking]", got)
				}

				writeInitialThinkingLines(t, p.parser, terminal.line)
				wantTypes := append(append([]string{}, source.wantTypes...), protocol.TypeTurnEnd, protocol.TypeTurnState)
				if got := pushTypes(p.bcast.pushes); !slices.Equal(got, wantTypes) {
					t.Fatalf("terminal envelope order: got %v, want %v", got, wantTypes)
				}
				if got := turnStateValues(t, p.bcast.pushes); !slices.Equal(got, []string{"thinking", "idle"}) {
					t.Fatalf("terminal turn states: got %v, want [thinking idle]", got)
				}
				ends := pushesOfType(p.bcast.pushes, protocol.TypeTurnEnd)
				if len(ends) != 1 {
					t.Fatalf("turn_end count = %d, want 1", len(ends))
				}
				var end protocol.TurnEndPayload
				if err := json.Unmarshal(ends[0].env.Payload, &end); err != nil {
					t.Fatalf("decode turn_end: %v", err)
				}
				if end.ConversationID != testConvID || end.TurnID != openedTurnID || end.StopReason != terminal.stopReason {
					t.Fatalf("turn_end = %#v, want conversation=%q turn=%q stop_reason=%q",
						end, testConvID, openedTurnID, terminal.stopReason)
				}
				if p.emitter.inTurn || p.emitter.turnID != "" || p.emitter.currentState != "" {
					t.Fatalf("terminal result left emitter open: inTurn=%v state=%q turnID=%q",
						p.emitter.inTurn, p.emitter.currentState, p.emitter.turnID)
				}
				if p.busy.Busy(testConvID) || p.busy.Busy(initialThinkingOther) {
					t.Fatal("terminal result left a conversation busy")
				}
				for _, push := range p.bcast.pushes {
					if bytes.Contains(push.env.Payload, []byte(initialThinkingSecret)) {
						t.Fatalf("thinking text leaked into %q payload", push.env.Type)
					}
					if push.env.Type == protocol.TypeAssistantDelta || push.env.Type == protocol.TypeToolUse {
						t.Fatalf("%q preceded a thinking-only terminal result", push.env.Type)
					}
				}
				if p.logs.Len() == 0 {
					t.Fatal("expected push-error diagnostics; no log path was exercised")
				}
				if strings.Contains(p.logs.String(), initialThinkingSecret) {
					t.Fatalf("thinking text leaked into logs: %s", p.logs.String())
				}
			})
		}
	}
}
