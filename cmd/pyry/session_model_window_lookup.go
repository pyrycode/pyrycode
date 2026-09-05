package main

import (
	"github.com/pyrycode/pyrycode/internal/sessions"
)

// sessionModelWindows answers the context windows currently observed for a
// session, keyed by the model id claude reported each one under, so the usage
// reader can size a used count against the window belonging to the model that
// produced it (#2107). The map crosses into internal/contextwindow's Read as its
// windows argument, which is the only consumer.
//
// It is the resolveBoundModelList shape MINUS THE CONVERSATION HOP. That family
// is conversation-keyed because its payloads carry a conversation_id; this one is
// keyed on the SESSION id because the seam it feeds already holds one —
// snapshotUsageFor resolves a transcript by that id, and the invariant its doc
// rests on (the pool hands the session id to the runner, the stream runner spawns
// claude with that exact --session-id) is what makes the transcript and the
// retained report two readings of the SAME child. Pool.Lookup is therefore the
// whole of the isolation story: a window observed for one session cannot be
// reported for another, because no other id resolves to that session's runner.
//
// The retention is reached by TYPE ASSERTION off Session.Runner, the way
// resolveBoundModelList and interruptRunner reach theirs. internal/sessions'
// Runner doc states the rule: the interface carries a method when its consumer
// sits INSIDE internal/sessions, where a structural assertion would fail open.
// This consumer is in cmd/pyry, so widening would buy no compile-time guarantee
// and would drag every fake runner in two packages into the diff. A runner
// without the method is a REFUSAL, not a panic — and a nil Runner fails the
// assertion cleanly, so no nil check is needed.
//
// NIL IS THE ONLY REFUSAL. An unknown or empty id, a runner without the method,
// and a child that has never reported a usable window all answer nil, and Read
// reads a nil map as "nothing observed" — the same fallback-to-the-default
// reading a model with no entry gets. There is deliberately no second spelling
// (no bool, no empty non-nil map): the retention's own bool is already the single
// spelling of "never reported", and a consumer that must fall back either way has
// nothing to do with a finer distinction.
//
// THE COMMA-OK IS THE ONLY FILTER, retainedModelLists' rule. In particular there
// is no ModelID != "" check and no WindowTokens > 0 check here — both live in
// contextwindow.Read, which is where the join's rules are decided, and a second
// spelling here would be a second place they could drift. An entry keyed by the
// empty string is copied across and is simply never looked up, because a
// transcript entry with no model never asks for one.
//
// report.Dropped is deliberately not consulted. A truncated report's surviving
// entries are still true, and a model whose entry was cut is a miss, which already
// has a safe answer.
//
// SECURITY: model ids are claude-authored, unsanitized text (see
// sessionModelWindowHold.ModelWindows for the two obligations that ride them).
// This function is one of the two places #2107 discharges those by NARROWING: it
// copies ids into a map that is only ever compared against, and the value that
// leaves the join is an integer. It takes NO logger and MUST NOT grow one — the
// only thing a "why did this session contribute nothing" line could carry is a
// session id or the model ids themselves, which is exactly the channel the #833
// posture closes (internal/relay's v2session_settings.go and internal/sessions'
// pool.go restate it; sessionModelWindowHold enforces it by having no logger
// field). Pool.Lookup's error is DISCARDED rather than wrapped, for
// resolveBoundRunSettings' stated reason: returning it bare is what keeps a
// hostile or malformed id from being reflected into a log line or a wire frame a
// caller builds from it. No id, model or window reaches any surface from here.
//
// Concurrency: a synchronous read on the caller's goroutine — typically a relay
// leg with no turn in flight, which is the case sessionModelWindowHold's mutex
// exists for. Its two locks, the pool's and the hold's leaf mutex, are acquired
// SEQUENTIALLY and never nested, so this adds no edge to the daemon's lock order;
// do not restructure the body so the hold read happens inside the pool's scope.
// The Lookup → ModelWindows window is the benign TOCTOU the resolver family
// documents: a rotation landing in it yields either the windows the session bound
// a moment ago reported or a miss, and both are correct readings, because a
// window report is a property of one child's turns and a fresh child reports its
// own. No re-read, no retry.
//
// The returned map is a FRESH ALLOCATION per call over a copy the retention
// already owns, so no two callers share one and a mutation cannot reach the
// retained value. Its size is bounded upstream by internal/streamsup's
// maxModelWindowEntries, applied at construction.
func sessionModelWindows(pool *sessions.Pool) func(sessionID string) map[string]int {
	return func(sessionID string) map[string]int {
		sess, err := pool.Lookup(sessions.SessionID(sessionID))
		if err != nil {
			return nil
		}
		reporter, ok := sess.Runner().(interface {
			ModelWindows() (modelWindowReport, bool)
		})
		if !ok {
			return nil
		}
		report, ok := reporter.ModelWindows()
		if !ok {
			return nil
		}
		out := make(map[string]int, len(report.Windows))
		for _, w := range report.Windows {
			out[w.ModelID] = w.WindowTokens
		}
		return out
	}
}
