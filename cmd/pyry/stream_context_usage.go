package main

import (
	"log/slog"

	"github.com/pyrycode/pyrycode/internal/turnevent"
)

const summaryContextUsageDetail = "summary"

// turnEndContextUsageRequester decorates one session parser's sink with the
// daemon policy of asking that same session for a fresh summary after each
// completed turn. It retains no event or request state: Parser calls Sink
// serially in stream order, and each TurnEnd invocation owns exactly one ask.
type turnEndContextUsageRequester struct {
	next func(turnevent.Event)
	// request is assigned once after streamsup.New returns because the parser
	// feeding Sink is itself part of that runner. newStreamRunnerFactory finishes
	// the assignment before sessions starts Run, so goroutine creation publishes
	// it to the stdout forwarder. nil is a construction-time/test no-op.
	request func(string) error
	logger  *slog.Logger
}

func newTurnEndContextUsageRequester(next func(turnevent.Event), logger *slog.Logger) *turnEndContextUsageRequester {
	if logger == nil {
		logger = slog.Default()
	}
	return &turnEndContextUsageRequester{next: next, logger: logger}
}

// Sink forwards every event before acting on TurnEnd. The ordering keeps the
// terminal event observable even when the follow-up write races child teardown.
func (r *turnEndContextUsageRequester) Sink(ev turnevent.Event) {
	if r.next != nil {
		r.next(ev)
	}
	if _, ok := ev.(turnevent.TurnEnd); !ok || r.request == nil {
		return
	}
	if err := r.request(summaryContextUsageDetail); err != nil {
		// SECURITY: wholly daemon-authored and content-free. In particular, omit
		// the returned error so a future error widening cannot disclose a request
		// id, response content, or event payload through this call site.
		r.logger.Debug("relay: context usage request not delivered",
			"event", "stream_turn.context_usage_request_failed")
	}
}
