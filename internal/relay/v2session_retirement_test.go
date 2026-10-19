package relay

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/protocol"
)

func TestPushRetirementCause(t *testing.T) {
	for _, tc := range []struct {
		name       string
		triggers   []string
		concurrent bool
		code       uint16
		event      string
	}{
		{"producer_gap", []string{"retire"}, false, 4414, "v2.push.producer_gap.teardown"},
		{"byte_ceiling", []string{"overflow"}, false, 4413, "v2.push.ceiling.teardown"},
		{"overflow_then_retire", []string{"overflow", "retire"}, false, 4413, "v2.push.ceiling.teardown"},
		{"retire_then_pushes", []string{"retire", "overflow"}, false, 4414, "v2.push.producer_gap.teardown"},
		{"duplicate_retirement", []string{"retire", "retire"}, false, 4414, "v2.push.producer_gap.teardown"},
		{"concurrent", []string{"retire", "overflow"}, true, 0, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			m, s, rec, _ := liveFixture(t, threadCaps, func(c *V2SessionConfig) {
				c.Logger = slog.New(slog.NewJSONHandler(&logs, nil))
				c.Connected = func() bool { return false }
			})
			logs.Reset()
			trigger := func(kind string) {
				if kind == "retire" {
					if err := m.RetireLiveState(t.Context(), s.connID); err != nil {
						t.Error(err)
					}
					return
				}
				payload := payloadOfLen(maxAppEnvelopePayload)
				for i := 0; i <= pushQueueByteCeiling/maxAppEnvelopePayload; i++ {
					if err := m.Push(t.Context(), s.connID, protocol.Envelope{Type: protocol.TypeToolResult, Payload: payload}); err != nil {
						t.Error(err)
						return
					}
				}
			}
			var wg sync.WaitGroup
			for _, kind := range tc.triggers {
				if tc.concurrent {
					wg.Add(1)
					go func() { defer wg.Done(); trigger(kind) }()
				} else {
					trigger(kind)
				}
			}
			wg.Wait()
			if !tc.concurrent && tc.triggers[0] == "retire" {
				if q := m.queues[s.connID]; len(q.items) != 0 || q.bytes != 0 {
					t.Fatalf("pushes grew a retired queue: items=%d bytes=%d", len(q.items), q.bytes)
				}
			}
			ctx, cancel := context.WithCancel(t.Context())
			done := make(chan struct{})
			go func() {
				defer close(done)
				if err := m.Run(ctx); !errors.Is(err, context.Canceled) {
					t.Errorf("Run = %v", err)
				}
			}()
			t.Cleanup(func() {
				cancel()
				select {
				case <-done:
				case <-time.After(5 * time.Second):
					t.Error("Run did not stop")
				}
			})
			// After queue removal, a Run-owned snapshot fences close publication.
			waitQueueGone(t, m, s.connID)
			m.ActiveConns(ctx)
			cancel()
			<-done
			frames := rec.snapshot()[1:]
			if len(frames) != 1 || frames[0].Frame != nil {
				t.Fatalf("terminal output = %+v", frames)
			}
			code, event := tc.code, tc.event
			if tc.concurrent {
				code = frames[0].CloseCode
				switch code {
				case 4413:
					event = "v2.push.ceiling.teardown"
				case 4414:
					event = "v2.push.producer_gap.teardown"
				default:
					t.Fatalf("concurrent close code = %d", code)
				}
			}
			if frames[0].CloseCode != code || s.state != V2StateClosed || m.sessions[s.connID] != nil || m.queues[s.connID] != nil {
				t.Fatalf("close=%d want=%d state=%v", frames[0].CloseCode, code, s.state)
			}
			select {
			case <-s.done:
			default:
				t.Error("session work was not cancelled")
			}
			if err := m.Push(t.Context(), s.connID, protocol.Envelope{}); !errors.Is(err, ErrConnNotFound) {
				t.Fatalf("post-teardown push = %v", err)
			}
			counts := make(map[string]int)
			for _, line := range bytes.Split(bytes.TrimSpace(logs.Bytes()), []byte("\n")) {
				var record struct {
					Level     string `json:"level"`
					Event     string `json:"event"`
					ConnID    string `json:"conn_id"`
					CloseCode uint16 `json:"close_code"`
				}
				if err := json.Unmarshal(line, &record); err != nil {
					t.Fatal(err)
				}
				counts[record.Event]++
				if record.Event == event && (record.Level != "WARN" || record.ConnID != s.connID || record.CloseCode != code) {
					t.Fatalf("teardown record = %+v", record)
				}
			}
			if counts[event] != 1 {
				t.Fatalf("teardown events = %v", counts)
			}
			if code == 4414 && (counts["v2.push.ceiling"] != 0 || counts["v2.push.ceiling.teardown"] != 0) {
				t.Fatalf("producer gap reported a ceiling: %v", counts)
			}
			if code == 4413 && (counts["v2.push.ceiling"] != 1 || counts["v2.push.producer_gap.teardown"] != 0) {
				t.Fatalf("overflow events = %v", counts)
			}
		})
	}
}

func TestRetireLiveStateErrors(t *testing.T) {
	m, s, _, _ := liveFixture(t, threadCaps, nil)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := m.RetireLiveState(ctx, s.connID); !errors.Is(err, context.Canceled) || m.pushRetired(s.connID) {
		t.Fatalf("cancelled retirement = %v, retired=%v", err, m.pushRetired(s.connID))
	}
	if err := m.RetireLiveState(t.Context(), "absent"); !errors.Is(err, ErrConnNotFound) {
		t.Fatalf("missing retirement = %v", err)
	}
}

func TestPushRetirementStaleNotification(t *testing.T) {
	m, s, rec, _ := liveFixture(t, threadCaps, nil)
	if err := m.RetireLiveState(t.Context(), s.connID); err != nil {
		t.Fatal(err)
	}
	m.handlePushOverflow(t.Context(), <-m.pushOverflow)
	// Replacement sessions start with a fresh, unlatched queue under the same ID.
	replacement := &V2Session{connID: s.connID, state: V2StateOpen}
	m.sessions[s.connID] = replacement
	m.queues[s.connID] = &pushQueue{}
	t.Cleanup(func() { m.teardown(replacement) })
	before := len(rec.snapshot())
	m.handlePushOverflow(t.Context(), s.connID)
	if replacement.state != V2StateOpen || len(rec.snapshot()) != before {
		t.Fatal("stale notification closed a replacement without a terminal trigger")
	}
}

func TestRetiredLivePendingDoesNotDrain(t *testing.T) {
	m, s, rec, _ := liveFixture(t, threadCaps, nil)
	r := liveReading(protocol.TypeTurnState, gateClaudeConv, "", `"session"`, 1, 1)
	s.livePending = &r
	s.liveSnapshot = func() (LiveState, bool) { t.Error("retired snapshot read"); return r, true }
	if err := m.RetireLiveState(t.Context(), s.connID); err != nil {
		t.Fatal(err)
	}
	m.drainOnce(t.Context())
	m.drainLiveOnce(t.Context(), false)
	m.signalLiveDrain()
	if len(rec.snapshot()) != 1 || s.livePending == nil {
		t.Fatal("retired pending output escaped")
	}
}
