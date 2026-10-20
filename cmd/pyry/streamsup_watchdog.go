package main

import (
	"sync"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/streamsup"
)

func streamPendingPermission(reg *conversations.Registry, report *approvalParkedReport) func(string) bool {
	return func(sessionID string) bool {
		conversationID, ok := conversationForSession(reg, sessionID)
		return ok && report.parked(conversationID)
	}
}

// streamRunnerWatchdog orders successful delivery against child exit. The runner
// closes stdin before its exit callback, so a blocked write finishes before reset;
// the supervisor cannot spawn the replacement until reset returns.
type streamRunnerWatchdog struct {
	mu       sync.Mutex
	detector *streamsup.Watchdog
}

func (w *streamRunnerWatchdog) write(write func() error) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.detector.TrackUserTurn(write)
}

func (w *streamRunnerWatchdog) childExited() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.detector.ChildExited()
}
