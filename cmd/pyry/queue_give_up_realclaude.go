//go:build e2e_realclaude

package main

import (
	"os"
	"time"
)

// queueGiveUpAfter returns the live-test override of the queue's give-up bound,
// or 0 for the default. See queueGiveUpAfterEnv.
func queueGiveUpAfter() (time.Duration, error) {
	return parseQueueGiveUpAfter(os.Getenv(queueGiveUpAfterEnv))
}
