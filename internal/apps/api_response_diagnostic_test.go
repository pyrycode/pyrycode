package apps

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"
)

// apiResponseOwner confirms startup health and leaves runtime probes test-driven.
func apiResponseOwner(t *testing.T) (*Registry, *Owner, chan time.Time) {
	t.Helper()
	r, o := healthFixture(t, healthReply{API: true, Body: healthyBody()})
	ticks := make(chan time.Time)
	o.healthTicks = ticks
	healthStart(t, r, o)
	healthTarget(t, o)
	return r, o, ticks
}

func TestAPIResponseFailureClasses(t *testing.T) {
	for _, tc := range []struct {
		name     string
		reply    apiReply
		capacity int64
		want     error
		reserve  bool
		read     bool
	}{
		{"overflow", apiReply{Status: 200, Body: bytes.Repeat([]byte("x"), 1048577)}, 2 << 20, ErrResponseLimit, true, true},
		{"capacity", apiReply{Status: 200, Body: []byte("no room")}, 0, ErrResponseCapacity, true, false},
		{"request_io", apiReply{Status: 200, Stall: "disconnect"}, 100, ErrResponseIO, false, false},
		{"body_io", apiReply{Status: 200, Truncate: true}, 100, ErrResponseIO, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, o, _ := apiResponseOwner(t)
			target := healthTarget(t, o)
			apiWrite(t, r, tc.reply)
			b := &responseBudget{ceiling: tc.capacity}
			ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
			defer cancel()
			started := time.Now()
			in := apiRequest(b)
			reservations, reads := 0, 0
			in.Reserve = func(n int64) bool { reservations++; return b.reserve(n) }
			in.Progress = func() { reads++ }
			p, err := o.ForwardAPI(ctx, testHost, testApp, "1.0.0", in)
			if p != nil {
				p.Close()
			}
			t.Logf("elapsed=%s error=%v target_valid=%t requests=%d budget=%d reservations=%d reads=%d", time.Since(started), err, target.valid(), apiCount(r), b.used(), reservations, reads)
			if (reservations > 0) != tc.reserve || (reads > 0) != tc.read {
				t.Fatalf("wrong failure phase: reservations=%d reads=%d", reservations, reads)
			}
			if !errors.Is(err, tc.want) || p != nil || !target.valid() || apiCount(r) != 1 || b.used() != 0 {
				t.Fatalf("got %v want %v; result=%t target_valid=%t requests=%d budget=%d", err, tc.want, p != nil, target.valid(), apiCount(r), b.used())
			}
		})
	}
	t.Run("health_invalidation", func(t *testing.T) {
		r, o, ticks := apiResponseOwner(t)
		target := healthTarget(t, o)
		apiWrite(t, r, apiReply{Status: 200, Stall: "headers"})
		b := &responseBudget{ceiling: 2 << 20}
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer cancel()
		done := make(chan struct{})
		var forwardErr error
		started := time.Now()
		go func() {
			p, err := o.ForwardAPI(ctx, testHost, testApp, "1.0.0", apiRequest(b))
			if p != nil {
				p.Close()
			}
			forwardErr = err
			close(done)
		}()
		defer func() { cancel(); <-done }()
		healthWait(t, func() bool { return apiCount(r) == 1 })
		healthWrite(t, r, healthReply{API: true, Body: `{}`})
		for failures := 1; failures <= 3; failures++ {
			select {
			case ticks <- time.Now():
			case <-ctx.Done():
				t.Fatal("health probe admission: ", ctx.Err())
			}
			healthWait(t, func() bool {
				o.mu.Lock()
				defer o.mu.Unlock()
				return target.generation.healthFailures >= failures
			})
		}
		select {
		case <-done:
		case <-ctx.Done():
			t.Fatal("forwarding did not retire: ", ctx.Err())
		}
		t.Logf("elapsed=%s error=%v target_valid=%t requests=%d budget=%d", time.Since(started), forwardErr, target.valid(), apiCount(r), b.used())
		if !errors.Is(forwardErr, ErrUnavailable) || target.valid() || apiCount(r) != 1 || b.used() != 0 {
			t.Fatal("invalidation did not fence response: ", forwardErr)
		}
		zero := &responseBudget{ceiling: 0}
		p, err := o.ForwardAPI(ctx, testHost, testApp, "1.0.0", apiRequest(zero))
		if p != nil {
			p.Close()
		}
		t.Logf("subsequent capacity: error=%v requests=%d budget=%d", err, apiCount(r), zero.used())
		if !errors.Is(err, ErrUnavailable) || p != nil || apiCount(r) != 1 || zero.used() != 0 {
			t.Fatal("invalid target reused: ", err)
		}
	})
}
