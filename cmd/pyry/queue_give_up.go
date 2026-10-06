package main

import (
	"fmt"
	"time"
)

// queueGiveUpAfterEnv names the test-only override for the message queue's
// give-up bound (msgqueue.Config.GiveUpAfter, default 2 minutes). Only a daemon
// built with the e2e_realclaude tag reads it; an ordinary build never looks.
// It exists so a live test of the give-up path can wait seconds instead of the
// full production bound.
const queueGiveUpAfterEnv = "PYRY_E2E_QUEUE_GIVE_UP_AFTER"

// parseQueueGiveUpAfter reads the override's value. Empty means unset, which
// returns 0 so msgqueue keeps its default. Anything else must be a positive Go
// duration: a test that sets the knob means it, so a typo fails the daemon's
// start rather than silently waiting the production bound.
func parseQueueGiveUpAfter(raw string) (time.Duration, error) {
	if raw == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", queueGiveUpAfterEnv, err)
	}
	if d <= 0 {
		return 0, fmt.Errorf("%s: %s is not a positive duration", queueGiveUpAfterEnv, raw)
	}
	return d, nil
}
