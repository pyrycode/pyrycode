package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/sessions"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

func TestTurnEndContextUsageRequester_ForwardsThenRequestsSummary(t *testing.T) {
	t.Parallel()

	events := []turnevent.Event{
		turnevent.TextChunk{Text: "opening text"},
		turnevent.RateLimited{},
		turnevent.TurnEnd{Reason: turnevent.TurnEndReasonEndTurn},
		turnevent.ToolStart{ToolCallID: "tool-1", Title: "Read"},
		turnevent.TurnEnd{Reason: turnevent.TurnEndReasonCancelled},
	}
	var forwarded []turnevent.Event
	var details []string
	var order []string
	requester := newTurnEndContextUsageRequester(func(ev turnevent.Event) {
		forwarded = append(forwarded, ev)
		order = append(order, "forward")
	}, discardLogger())
	requester.request = func(detail string) error {
		details = append(details, detail)
		order = append(order, "request")
		return nil
	}

	for _, ev := range events {
		requester.Sink(ev)
	}

	if !reflect.DeepEqual(forwarded, events) {
		t.Fatalf("forwarded events = %#v, want every input unchanged in order %#v", forwarded, events)
	}
	if want := []string{"summary", "summary"}; !reflect.DeepEqual(details, want) {
		t.Errorf("request details = %q, want %q", details, want)
	}
	if want := []string{"forward", "forward", "forward", "request", "forward", "forward", "request"}; !reflect.DeepEqual(order, want) {
		t.Errorf("operation order = %v, want %v", order, want)
	}
}

func TestTurnEndContextUsageRequester_RequestFailureIsContentFree(t *testing.T) {
	t.Parallel()

	const (
		requestIDSentinel = "context-request-id-secret-2353"
		responseSentinel  = "context-response-content-secret-2353"
		errorSentinel     = requestIDSentinel + ": " + responseSentinel
	)
	var forwarded []turnevent.Event
	var logs bytes.Buffer
	requester := newTurnEndContextUsageRequester(func(ev turnevent.Event) {
		forwarded = append(forwarded, ev)
	}, debugLogger(&logs))
	requester.request = func(string) error { return errors.New(errorSentinel) }
	want := turnevent.TurnEnd{Reason: turnevent.TurnEndReasonEndTurn}

	requester.Sink(want)

	if !reflect.DeepEqual(forwarded, []turnevent.Event{want}) {
		t.Fatalf("forwarded events = %#v, want the terminal event unchanged", forwarded)
	}
	lines := strings.Split(strings.TrimSpace(logs.String()), "\n")
	if len(lines) != 1 || lines[0] == "" {
		t.Fatalf("Debug records = %q, want exactly one", logs.String())
	}
	if !strings.Contains(lines[0], "stream_turn.context_usage_request_failed") {
		t.Errorf("Debug record = %q, want the daemon-authored event name", lines[0])
	}
	for _, secret := range []string{requestIDSentinel, responseSentinel, errorSentinel} {
		if strings.Contains(logs.String(), secret) {
			t.Errorf("Debug record contains protected sentinel %q: %s", secret, logs.String())
		}
	}

	// The logger really captured Debug; otherwise the content checks above could
	// pass against an empty buffer without exercising the failure record.
	if !debugLogger(&bytes.Buffer{}).Enabled(t.Context(), slog.LevelDebug) {
		t.Fatal("debugLogger does not enable Debug")
	}
}

func TestStreamRunnerFactory_RequestsSummaryAfterTurnEnd(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	requestsPath := filepath.Join(dir, "requests.jsonl")
	childPath := filepath.Join(dir, "fake-claude.sh")
	const (
		result = `{"type":"result","subtype":"success","session_id":"factory-session-2353"}`
		reply  = `{"type":"control_response","response":{"subtype":"success","request_id":"2","response":{"model":"claude-haiku-4-5","totalTokens":17,"maxTokens":200000,"percentage":1,"categories":[]}}}`
	)
	script := fmt.Sprintf(`#!/bin/sh
IFS= read -r initialize
printf '%%s\n' '%s'
IFS= read -r summary
printf '%%s\n' "$initialize" "$summary" > %q
printf '%%s\n' '%s'
exec sleep 3600
`, result, requestsPath, reply)
	if err := os.WriteFile(childPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake claude: %v", err)
	}

	sink := newStreamTurnSink(8, discardLogger())
	runner, err := newStreamRunnerFactory(sink, "", streamApprovalConfig{})(sessions.RunnerConfig{
		ClaudeBin: childPath,
		WorkDir:   dir,
		SessionID: "factory-session-2353",
		Logger:    discardLogger(),
	})
	if err != nil {
		t.Fatalf("newStreamRunnerFactory: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runner.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		<-done
	})

	requests := waitContextUsageRequests(t, requestsPath)
	if len(requests) != 2 {
		t.Fatalf("child received %d requests, want initialize then one summary: %q", len(requests), requests)
	}
	var got struct {
		Type      string `json:"type"`
		RequestID string `json:"request_id"`
		Request   struct {
			Subtype string `json:"subtype"`
			Detail  string `json:"detail"`
		} `json:"request"`
	}
	if err := json.Unmarshal([]byte(requests[1]), &got); err != nil {
		t.Fatalf("decode automatic request: %v", err)
	}
	if got.Type != "control_request" || got.Request.Subtype != "get_context_usage" || got.Request.Detail != "summary" {
		t.Errorf("automatic request = %+v, want one get_context_usage summary request", got)
	}
	if got.RequestID != "2" {
		t.Errorf("automatic request id = %q, want 2 after the same runner's initialize request", got.RequestID)
	}

	wantTypes := []any{turnevent.TurnEnd{}, turnevent.ContextUsage{}}
	for i, want := range wantTypes {
		select {
		case env := <-sink.ch:
			if reflect.TypeOf(env.ev) != reflect.TypeOf(want) {
				t.Fatalf("sink event %d = %T, want %T (and no Unrecognized)", i, env.ev, want)
			}
			if env.sessionID != "factory-session-2353" {
				t.Errorf("sink event %d session = %q, want the runner's session", i, env.sessionID)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("timed out waiting for sink event %d of type %T", i, want)
		}
	}
	select {
	case env := <-sink.ch:
		t.Fatalf("unexpected extra event %T; automatic response must not add Unrecognized", env.ev)
	default:
	}
	state := runner.State()
	if state.Phase != sessions.PhaseRunning || state.RestartCount != 0 || state.NextBackoff != 0 {
		t.Errorf("runner state after automatic round trip = %+v, want running without restart or backoff", state)
	}
}

func waitContextUsageRequests(t *testing.T, path string) []string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil {
			lines := strings.Split(strings.TrimSpace(string(data)), "\n")
			if len(lines) >= 2 {
				return lines
			}
		} else if !os.IsNotExist(err) {
			t.Fatalf("read child requests: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for initialize and summary requests in %s", path)
	return nil
}
