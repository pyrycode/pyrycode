package main

import (
	"github.com/pyrycode/pyrycode/internal/contextwindow"
	"github.com/pyrycode/pyrycode/internal/transcript"
)

// snapshotUsageFor builds the by-id context-window usage reader behind the
// relay's SnapshotUsage seam: given a session id per CALL, it resolves that
// session's transcript BY ID and reports its occupancy (used tokens, window
// size). The id is an argument rather than a construction-time closure so one
// reader over one sessions directory can answer for any resolved session, not
// only the bootstrap's (#1608).
//
// Returns nil when there is no sessions directory to resolve against
// (foreground / unwired). The guard belongs here rather than at the wiring
// point because it protects the reader itself: with an empty dir, StatByID
// would join a RELATIVE <id>.jsonl against the daemon's working directory, and
// a stray file of that name there would be read and reported as a session's
// occupancy. bootstrapSnapshotUsage collapses a nil reader into the nil seam
// that makes the handlers report zeros — the pre-existing unwired contract,
// unchanged.
//
// #1214: this replaces a resolver that asked the terminal child for its process
// id and followed whichever transcript that process held open. Two things were
// wrong with it.
//
// First, it could not work at all on the stream-json runner. It was built only
// when w.streamSink == nil, because its process lookup reads the typed-nil
// bootstrap supervisor (#1077) and there is no terminal child to ask. So on the
// runner now in production the seam was nil and every consumer reported zero
// usage, which the desktop renders as "context usage unavailable". That was a
// silent degradation introduced by the T9 stream cutover.
//
// Second, and worth stating because it changes what "it used to work" means: the
// process probe was already the weaker resolver on the terminal path too. #989
// introduced the pinned-by-id resolver precisely because real claude opens its
// transcript, appends, and closes within milliseconds, so the probe practically
// never observes an open descriptor (see resolveBootstrapJSONL's doc and
// interactive_turn_stream_v2.go). The turn stream migrated to the pinned id; this
// reader never did. Do NOT record #1214 as "usage regressed at the cutover"
// without evidence: it may only ever have been reliable against the e2e fake
// claude, which holds its descriptor open.
//
// By-id resolution is deterministic on BOTH runners and needs no probing. The
// pool hands the session id to the runner (internal/sessions/pool.go), the stream
// runner spawns claude with that exact --session-id (internal/streamsup), and the
// terminal path pins the same id (#839), so <dir>/<id>.jsonl IS the transcript.
// transcript.StatByID validates the stem before any path join, so a malformed or
// hostile id is rejected with no filesystem access at all.
//
// Every failure collapses to contextwindow.Read(""), the same deterministic
// fresh-session report (zero used, default window) a brand-new session produces:
// an id that is empty or malformed, a transcript not yet written, and a file that
// raced away between the stat and the open. A usage reader has no business
// surfacing an error to a client, and it NEVER logs a path.
func snapshotUsageFor(dir string) func(id string) (usedTokens, windowTokens int) {
	if dir == "" {
		return nil
	}
	return func(id string) (int, int) {
		var path string
		if res, err := transcript.StatByID(dir, id); err == nil {
			path = res.Path
		}
		u, err := contextwindow.Read(path)
		if err != nil {
			// A genuine open failure on a resolved path. Read("") never errors and
			// yields the same fresh-session report, so this branch is deterministic
			// and content-free. It also discards the error rather than propagating
			// it, which is what keeps the resolved path off every surface — the
			// error contextwindow.Read returns wraps the full path.
			u, _ = contextwindow.Read("")
		}
		return u.UsedTokens, u.WindowTokens
	}
}

// bootstrapSnapshotUsage builds the seam startRelayV2 hands to the relay: the
// by-id reader above, bound to the BOOTSTRAP session's id source, so every
// consumer still reports the bootstrap session's figures and the wire stays
// byte-identical to before #1608.
//
// Returns nil — the seam that makes the handlers report zeros — when either
// half is unwired: no sessions directory (snapshotUsageFor's guard) or no id
// source. bootstrapID is a func value whose zero is nil and which relayWiring
// documents as legitimately nil, so the second half pins a documented
// optional-field contract; relayWiring's single producer always sets it, so no
// path reaches it today. Deciding it at BUILD time, before any closure exists,
// is what makes "no path can invoke a nil id source" structural rather than a
// promise.
func bootstrapSnapshotUsage(dir string, bootstrapID func() string) func() (usedTokens, windowTokens int) {
	read := snapshotUsageFor(dir)
	if read == nil || bootstrapID == nil {
		return nil
	}
	return func() (int, int) {
		return read(bootstrapID())
	}
}
