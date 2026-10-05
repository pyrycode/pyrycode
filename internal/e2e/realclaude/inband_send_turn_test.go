//go:build e2e_realclaude

package realclaude

// #2843 — inbandSendTurn, driven against a test double instead of a live claude.
//
// The live flake: turn one took ~57 s, past the 45 s window after which the
// helper used to write the prompt again. Both copies ran, turn two's wait returned
// on the duplicate's result, and the model test read the pre-set_model model
// twice. This file pins the guarantee that replaced the resend: one successful
// write per call, so the result that releases a call is its own turn's.
//
// Everything here runs OFFLINE: no claude, no daemon, no credential.
//
//	go test -tags e2e_realclaude -race -count=1 -v \
//	  -run TestInbandSendTurn ./internal/e2e/realclaude/

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/sessions"
	"github.com/pyrycode/pyrycode/internal/streamsup"
)

// queuedTurn is one written turn and the moment the double answers it.
type queuedTurn struct {
	prompt string
	doneAt time.Time
}

// serialTurnRunner answers turns one at a time in write order, the way claude
// works through its stdin: a turn starts when it is written or when the previous
// turn is answered, whichever is later. Answers are computed from the clock, so
// the double runs no goroutine. Only WriteUserTurn is implemented; the embedded
// nil Runner panics on anything else, which inbandSendTurn never calls.
type serialTurnRunner struct {
	sessions.Runner

	liveAt time.Time // WriteUserTurn returns ErrNoLiveChild before this
	first  time.Duration
	later  time.Duration

	mu    sync.Mutex
	turns []queuedTurn
}

func (r *serialTurnRunner) WriteUserTurn(_ context.Context, _ string, payload []byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	if now.Before(r.liveAt) {
		return streamsup.ErrNoLiveChild
	}
	start, latency := now, r.first
	if n := len(r.turns); n > 0 {
		latency = r.later
		if prev := r.turns[n-1].doneAt; prev.After(start) {
			start = prev
		}
	}
	r.turns = append(r.turns, queuedTurn{prompt: string(payload), doneAt: start.Add(latency)})
	return nil
}

// written lists every successfully written prompt, in order.
func (r *serialTurnRunner) written() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0, len(r.turns))
	for _, turn := range r.turns {
		out = append(out, turn.prompt)
	}
	return out
}

// answered lists the prompts whose result has landed by now, in order.
func (r *serialTurnRunner) answered() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	var out []string
	for _, turn := range r.turns {
		if turn.doneAt.After(now) {
			break
		}
		out = append(out, turn.prompt)
	}
	return out
}

func (r *serialTurnRunner) resultCount() int { return len(r.answered()) }

func TestInbandSendTurn_SlowTurnIsWrittenOnce(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		liveIn time.Duration
		first  time.Duration
	}{
		// Past the 45 s window after which the helper resent a turn until #2843.
		{name: "first turn slower than the old resend window", first: 46 * time.Second},
		{name: "no live child at first", liveIn: 300 * time.Millisecond, first: 50 * time.Millisecond},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := &serialTurnRunner{
				liveAt: time.Now().Add(tc.liveIn),
				first:  tc.first,
				later:  50 * time.Millisecond,
			}
			var sent []string
			for _, prompt := range []string{"one", "two"} {
				inbandSendTurn(t, r, r, prompt)
				sent = append(sent, prompt)
				// The result that released this call must be this call's turn, and
				// nothing written earlier may still be queued behind it.
				if got := r.answered(); !slices.Equal(got, sent) {
					t.Fatalf("after inbandSendTurn(%q): answered turns %q, want %q (written %q)",
						prompt, got, sent, r.written())
				}
			}
			if got := r.written(); !slices.Equal(got, sent) {
				t.Errorf("written turns %q, want each prompt exactly once: %q", got, sent)
			}
		})
	}
}
