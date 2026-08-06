package main

import (
	"github.com/pyrycode/pyrycode/internal/contextwindow"
	"github.com/pyrycode/pyrycode/internal/transcript"
)

// snapshotUsageFor builds the relay's SnapshotUsage seam: it resolves the
// bootstrap session's transcript BY ID and reports its context-window occupancy
// (used tokens, window size). Returns nil when there is no sessions directory or
// no id source to resolve from (foreground / unwired), which makes the handlers
// report zeros — the pre-existing unwired contract, unchanged.
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
func snapshotUsageFor(dir string, bootstrapID func() string) func() (usedTokens, windowTokens int) {
	if dir == "" || bootstrapID == nil {
		return nil
	}
	return func() (int, int) {
		var path string
		if res, err := transcript.StatByID(dir, bootstrapID()); err == nil {
			path = res.Path
		}
		u, err := contextwindow.Read(path)
		if err != nil {
			// A genuine open failure on a resolved path. Read("") never errors and
			// yields the same fresh-session report, so this branch is deterministic
			// and content-free.
			u, _ = contextwindow.Read("")
		}
		return u.UsedTokens, u.WindowTokens
	}
}
