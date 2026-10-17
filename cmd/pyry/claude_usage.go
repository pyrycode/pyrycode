package main

import (
	"sync"
	"time"

	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/streamsup"
)

// claudeUsageStore holds the daemon's latest Claude usage allowance reading. One
// store serves every Claude session: the reading covers the whole account and
// each daemon instance runs one account. It lives in memory only, so a daemon
// starts with no reading. The zero value is ready to use and safe for concurrent
// use: record runs on every Claude session's stdout forwarder, ClaudeUsage on
// relay dispatch.
type claudeUsageStore struct {
	mu         sync.Mutex
	fiveHour   *streamsup.UsageWindow
	sevenDay   *streamsup.UsageWindow
	observedAt time.Time // zero means no reading
	now        func() time.Time
}

// record replaces the reading with one rate_limit_event's windows, stamped with
// the daemon's time now. A nil window was absent on that line and stays absent.
// The parser hands over freshly decoded values it never touches again, so the
// pointers are kept as they are.
func (s *claudeUsageStore) record(fiveHour, sevenDay *streamsup.UsageWindow) {
	now := time.Now
	if s.now != nil {
		now = s.now
	}
	at := now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fiveHour, s.sevenDay, s.observedAt = fiveHour, sevenDay, at
}

// ClaudeUsage reports the reading as wire data: state none before the first
// record, otherwise every value verbatim.
func (s *claudeUsageStore) ClaudeUsage() protocol.ClaudeUsagePayload {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.observedAt.IsZero() {
		return protocol.ClaudeUsagePayload{State: protocol.ClaudeUsageStateNone}
	}
	at := s.observedAt
	return protocol.ClaudeUsagePayload{
		State:      protocol.ClaudeUsageStateObserved,
		ObservedAt: &at,
		FiveHour:   claudeUsageWindow(s.fiveHour),
		SevenDay:   claudeUsageWindow(s.sevenDay),
	}
}

// claudeUsageWindow copies one window into its wire form, absent for nil.
func claudeUsageWindow(w *streamsup.UsageWindow) *protocol.ClaudeUsageWindow {
	if w == nil {
		return nil
	}
	return &protocol.ClaudeUsageWindow{Utilization: w.Utilization, ResetsAt: w.ResetsAt}
}
