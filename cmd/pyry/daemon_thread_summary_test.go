package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/relay"
)

func TestDaemonThreadSummaryRefreshLifecycle(t *testing.T) {
	for _, mode := range []string{"stopped", "removed", "cancelled", "push-failure"} {
		t.Run(mode, func(t *testing.T) {
			s, _, reg, _ := testShadow(t)
			if mode != "removed" {
				reg.Create(conversations.Conversation{ID: testConvID})
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var logs bytes.Buffer
			entered := make(chan struct{})
			calls := 0
			p := &daemonThreadPublisher{ctx: ctx, stopped: mode == "stopped", log: slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))}
			p.active = func(ctx context.Context, id string) []relay.ActiveConn {
				if mode != "cancelled" && mode != "push-failure" || id != testConvID {
					t.Error("ineligible refresh reached relay selection")
				}
				close(entered)
				if mode == "cancelled" {
					<-ctx.Done()
					return nil
				}
				return []relay.ActiveConn{
					{ConnID: "failed", Interactive: true, Thread: true},
					{ConnID: "legacy", Interactive: true},
					{ConnID: "non-interactive", Thread: true},
					{ConnID: "healthy", Interactive: true, Thread: true},
				}
			}
			p.push = func(_ context.Context, peer string, e protocol.Envelope) error {
				calls++
				if peer != "failed" && peer != "healthy" || e.Type != protocol.TypeConversationUpdated || e.ID == 0 || e.InReplyTo != nil {
					t.Error("refresh bypassed peer or envelope contract")
				}
				if peer == "failed" {
					return errors.New("private transport details")
				}
				return nil
			}
			s.publisher = p
			done := make(chan struct{})
			go func() { defer close(done); s.refreshThreadSummary(t.Context(), testConvID) }()
			if mode == "cancelled" {
				awaitSwitchPublication(t, entered)
				p.mu.Lock()
				p.stopped = true
				cancel()
				p.mu.Unlock()
				p.wg.Wait()
			}
			awaitSwitchPublication(t, done)
			want := 0
			if mode == "push-failure" {
				want = 2
			}
			if calls != want || strings.Contains(logs.String(), "private") {
				t.Fatalf("refresh calls=%d, want=%d; private diagnostic=%t", calls, want, strings.Contains(logs.String(), "private"))
			}
		})
	}
}
