package supervisor

import (
	"testing"
	"time"
)

func TestBackoffTimer(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		initial  time.Duration
		max      time.Duration
		reset    time.Duration
		uptimes  []time.Duration // uptime of each child run
		expected []time.Duration // expected delay after each run
	}{
		{
			name:     "first backoff returns initial",
			initial:  500 * time.Millisecond,
			max:      30 * time.Second,
			reset:    60 * time.Second,
			uptimes:  []time.Duration{1 * time.Second},
			expected: []time.Duration{500 * time.Millisecond},
		},
		{
			name:    "successive calls double the delay",
			initial: 500 * time.Millisecond,
			max:     30 * time.Second,
			reset:   60 * time.Second,
			uptimes: []time.Duration{
				1 * time.Second,
				1 * time.Second,
				1 * time.Second,
				1 * time.Second,
			},
			expected: []time.Duration{
				500 * time.Millisecond,
				1 * time.Second,
				2 * time.Second,
				4 * time.Second,
			},
		},
		{
			name:    "backoff caps at max",
			initial: 1 * time.Second,
			max:     4 * time.Second,
			reset:   60 * time.Second,
			uptimes: []time.Duration{
				1 * time.Second,
				1 * time.Second,
				1 * time.Second,
				1 * time.Second,
				1 * time.Second,
			},
			expected: []time.Duration{
				1 * time.Second,
				2 * time.Second,
				4 * time.Second,
				4 * time.Second,
				4 * time.Second,
			},
		},
		{
			name:    "long uptime resets backoff",
			initial: 500 * time.Millisecond,
			max:     30 * time.Second,
			reset:   60 * time.Second,
			uptimes: []time.Duration{
				1 * time.Second,  // short — no reset
				1 * time.Second,  // short — no reset
				90 * time.Second, // long — triggers reset
				1 * time.Second,  // should be back to initial
			},
			expected: []time.Duration{
				500 * time.Millisecond,
				1 * time.Second,
				500 * time.Millisecond, // reset because uptime > 60s
				1 * time.Second,        // doubles from initial again
			},
		},
		{
			name:    "exact reset threshold does not reset",
			initial: 500 * time.Millisecond,
			max:     30 * time.Second,
			reset:   60 * time.Second,
			uptimes: []time.Duration{
				1 * time.Second,
				60 * time.Second, // exactly at threshold — no reset (must exceed)
			},
			expected: []time.Duration{
				500 * time.Millisecond,
				1 * time.Second, // not reset — 60s is not > 60s
			},
		},
		{
			name:    "reset then doubling works correctly",
			initial: 1 * time.Second,
			max:     16 * time.Second,
			reset:   10 * time.Second,
			uptimes: []time.Duration{
				1 * time.Second,  // 1s delay
				1 * time.Second,  // 2s delay
				1 * time.Second,  // 4s delay
				20 * time.Second, // reset
				1 * time.Second,  // 1s delay (reset)
				1 * time.Second,  // 2s delay
			},
			expected: []time.Duration{
				1 * time.Second,
				2 * time.Second,
				4 * time.Second,
				1 * time.Second, // reset
				2 * time.Second,
				4 * time.Second,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			bo := newBackoffTimer(tt.initial, tt.max, tt.reset)

			for i, uptime := range tt.uptimes {
				got := bo.next(uptime)
				if got != tt.expected[i] {
					t.Errorf("step %d: next(%v) = %v, want %v", i, uptime, got, tt.expected[i])
				}
			}
		})
	}
}

func TestBackoffTimer_Reset(t *testing.T) {
	t.Parallel()

	bo := newBackoffTimer(500*time.Millisecond, 30*time.Second, 60*time.Second)

	// Advance backoff a few times.
	bo.next(1 * time.Second) // 500ms
	bo.next(1 * time.Second) // 1s
	bo.next(1 * time.Second) // 2s

	// Manual reset should return to initial.
	bo.reset()
	got := bo.next(1 * time.Second)
	if got != 500*time.Millisecond {
		t.Errorf("after reset: next() = %v, want 500ms", got)
	}
}
