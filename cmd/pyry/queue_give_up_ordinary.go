//go:build !e2e_realclaude

package main

import "time"

// queueGiveUpAfter returns 0, so msgqueue keeps its default give-up bound. An
// ordinary build ignores queueGiveUpAfterEnv entirely.
func queueGiveUpAfter() (time.Duration, error) { return 0, nil }
