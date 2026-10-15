package main

import (
	"context"
	"errors"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/relay"
	"github.com/pyrycode/pyrycode/internal/thread"
)

func testDaemonThreadQueryTrace(t *testing.T, c *relay.V2SessionConfig, started time.Time) {
	t.Helper()
	catch, page, send := c.ThreadCatchUp, c.ThreadPage, c.OutboundContext
	c.ThreadCatchUp = func(ctx context.Context, p protocol.ThreadCatchUpPayload) (thread.QueryResult, error) {
		t.Logf("catch-up provider entered: %v", time.Since(started))
		r, err := catch(ctx, p)
		t.Logf("catch-up provider completed: %v", time.Since(started))
		return r, err
	}
	c.ThreadPage = func(ctx context.Context, p protocol.ThreadPagePayload) (thread.QueryResult, error) {
		t.Logf("page provider entered: %v", time.Since(started))
		r, err := page(ctx, p)
		t.Logf("page provider completed: %v", time.Since(started))
		return r, err
	}
	c.OutboundContext = func(ctx context.Context, e protocol.RoutingEnvelope) error {
		t.Logf("application transport: %v", time.Since(started))
		return send(ctx, e)
	}
}

func testDaemonThreadFrame(w *switchWire, deadline time.Time) (protocol.RoutingEnvelope, error) {
	if !time.Now().Before(deadline) {
		return protocol.RoutingEnvelope{}, context.DeadlineExceeded
	}
	if len(w.pending) > 0 {
		e := w.pending[0]
		w.pending[0] = protocol.RoutingEnvelope{}
		w.pending = w.pending[1:]
		return e, nil
	}
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	select {
	case e := <-w.out:
		if time.Now().Before(deadline) {
			return e, nil
		}
	case <-timer.C:
	}
	return protocol.RoutingEnvelope{}, context.DeadlineExceeded
}

func TestDaemonThreadQueriesRecoveryDelayedProvider(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		defer close(release)
		select {
		case <-entered:
		case <-ctx.Done():
			return
		}
		timer := time.NewTimer(3200 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
		}
	}()
	defer func() { cancel(); <-done }()
	testDaemonThreadQueriesRecovery(t, func(c *relay.V2SessionConfig) {
		provider := c.ThreadCatchUp
		var once sync.Once
		c.ThreadCatchUp = func(ctx context.Context, p protocol.ThreadCatchUpPayload) (thread.QueryResult, error) {
			once.Do(func() {
				close(entered)
				select {
				case <-release:
				case <-ctx.Done():
				}
			})
			return provider(ctx, p)
		}
	})
}

func TestDaemonThreadReplyDeadline(t *testing.T) {
	for _, scenario := range []string{"missing", "expired buffered", "expired pending", "partial"} {
		t.Run(scenario, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				w := &switchWire{out: make(chan protocol.RoutingEnvelope, 1)}
				deadline := time.Now().Add(30 * time.Millisecond)
				switch scenario {
				case "expired buffered":
					w.out <- protocol.RoutingEnvelope{}
					deadline = time.Now().Add(-time.Second)
				case "expired pending":
					w.pending = []protocol.RoutingEnvelope{{}}
					deadline = time.Now().Add(-time.Second)
				case "partial":
					w.out <- protocol.RoutingEnvelope{}
					// Virtual time consumes part of the same budget without making
					// successful receipt depend on wall-clock scheduling.
					time.Sleep(20 * time.Millisecond)
					if _, err := testDaemonThreadFrame(w, deadline); err != nil {
						t.Fatal(err)
					}
				}
				wantExpiry := time.Now()
				if wantExpiry.Before(deadline) {
					wantExpiry = deadline
				}
				if _, err := testDaemonThreadFrame(w, deadline); !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("missing absolute expiry: %v", err)
				}
				if now := time.Now(); !now.Equal(wantExpiry) {
					t.Fatalf("expiry at %v, want %v", now, wantExpiry)
				}
			})
		})
	}
}
