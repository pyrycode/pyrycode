package streamsup

import "time"

// backoffTimer computes restart delays using exponential backoff with a
// stability reset. If the supervised process stays up longer than resetAfter,
// the delay resets to the initial value — the crash was likely transient.
//
// Copied verbatim from internal/supervisor/backoff.go: streamsup must not
// import the PTY supervisor (the dependency-boundary rule in the package doc),
// so the ladder is duplicated rather than shared. The two are expected to stay
// byte-identical; the lifted backoff_test.go ladder table guards both.
type backoffTimer struct {
	initial    time.Duration
	max        time.Duration
	resetAfter time.Duration
	current    time.Duration
}

func newBackoffTimer(initial, max, resetAfter time.Duration) *backoffTimer {
	return &backoffTimer{
		initial:    initial,
		max:        max,
		resetAfter: resetAfter,
		current:    initial,
	}
}

// next returns the delay to wait before the next restart attempt. It advances
// the internal state: doubling the delay each call, capping at max. If the
// child's uptime exceeded resetAfter, the counter resets first.
func (b *backoffTimer) next(uptime time.Duration) time.Duration {
	if uptime > b.resetAfter {
		b.current = b.initial
	}

	delay := b.current

	b.current *= 2
	if b.current > b.max {
		b.current = b.max
	}

	return delay
}

// reset returns the backoff to its initial delay.
func (b *backoffTimer) reset() {
	b.current = b.initial
}
