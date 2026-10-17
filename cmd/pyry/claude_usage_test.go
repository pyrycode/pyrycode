package main

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/dispatch"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/relay/handlers"
	"github.com/pyrycode/pyrycode/internal/streamsup"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// requestClaudeUsageWire sends request_claude_usage through the relay handler
// and returns the reply payload's bytes.
func requestClaudeUsageWire(t *testing.T, src handlers.ClaudeUsageSource) string {
	t.Helper()
	out := make(chan protocol.RoutingEnvelope, 1)
	conn := dispatch.NewTestConn("requester", out, nil)
	req := protocol.Envelope{ID: 3575, Type: protocol.TypeRequestClaudeUsage, Payload: json.RawMessage(`{}`)}
	if err := handlers.RequestClaudeUsage(src, quietLogger())(context.Background(), conn, req); err != nil {
		t.Fatal(err)
	}
	var env protocol.Envelope
	if err := json.Unmarshal((<-out).Frame, &env); err != nil {
		t.Fatal(err)
	}
	if env.Type != protocol.TypeClaudeUsage || env.InReplyTo == nil || *env.InReplyTo != req.ID {
		t.Fatalf("reply %s is not a correlated claude_usage", env.Type)
	}
	return string(env.Payload)
}

// TestClaudeUsageStore drives claude's lines through a session parser wired to
// the store exactly as newStreamRunnerFactory wires it, and reads the reply a
// client would get after each one.
func TestClaudeUsageStore(t *testing.T) {
	t.Parallel()
	clock := time.Date(2026, 10, 17, 12, 0, 0, 0, time.FixedZone("EEST", 3*3600))
	store := &claudeUsageStore{now: func() time.Time { return clock }}
	parser := streamsup.NewParser(func(turnevent.Event) {}, quietLogger())
	parser.SetUsageWindowsHandler(store.record)
	write := func(line string) {
		t.Helper()
		if _, err := parser.Write([]byte(line + "\n")); err != nil {
			t.Fatal(err)
		}
	}

	if got, want := requestClaudeUsageWire(t, store), `{"state":"none"}`; got != want {
		t.Fatalf("before any reading: %s, want %s", got, want)
	}

	write(`{"type":"rate_limit_event","rate_limit_info":{"status":"allowed","resetsAt":1790367600,"rateLimitType":"five_hour","unifiedWindows":{"five_hour":{"utilization":0.44,"resetsAt":1790367600},"seven_day":{"utilization":0.64,"resetsAt":1790575200}}}}`)
	both := `{"state":"observed","observed_at":"2026-10-17T09:00:00Z","five_hour":{"utilization":0.44,"resets_at":1790367600},"seven_day":{"utilization":0.64,"resets_at":1790575200}}`
	if got := requestClaudeUsageWire(t, store); got != both {
		t.Fatalf("both windows: %s, want %s", got, both)
	}

	// A line with no unifiedWindows keeps the earlier reading and its time.
	clock = clock.Add(time.Minute)
	write(`{"type":"rate_limit_event","rate_limit_info":{"status":"allowed","resetsAt":1790367600,"rateLimitType":"five_hour"}}`)
	if got := requestClaudeUsageWire(t, store); got != both {
		t.Fatalf("after a line without windows: %s, want the earlier %s", got, both)
	}

	// A warning line with one window and a past resetsAt replaces the reading.
	write(`{"type":"rate_limit_event","rate_limit_info":{"status":"allowed_warning","resetsAt":1000,"rateLimitType":"seven_day","utilization":0.76,"unifiedWindows":{"seven_day":{"utilization":0,"resetsAt":1000}}}}`)
	one := `{"state":"observed","observed_at":"2026-10-17T09:01:00Z","seven_day":{"utilization":0,"resets_at":1000}}`
	if got := requestClaudeUsageWire(t, store); got != one {
		t.Fatalf("one window: %s, want %s", got, one)
	}
}

func TestClaudeUsageStore_ConcurrentRecord(t *testing.T) {
	t.Parallel()
	store := &claudeUsageStore{}
	before := time.Now().UTC()
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			u := float64(i) / 10
			store.record(&streamsup.UsageWindow{Utilization: &u}, nil)
			_ = store.ClaudeUsage()
		}()
	}
	wg.Wait()
	got := store.ClaudeUsage()
	if got.State != protocol.ClaudeUsageStateObserved || got.FiveHour == nil || got.SevenDay != nil {
		t.Fatalf("reading = %+v, want an observed five_hour-only reading", got)
	}
	if got.ObservedAt == nil || got.ObservedAt.Before(before) || got.ObservedAt.After(time.Now().UTC()) {
		t.Fatalf("observed_at = %v, want daemon time during the test", got.ObservedAt)
	}
}
