package main

import (
	"github.com/pyrycode/pyrycode/internal/contextwindow"
	"github.com/pyrycode/pyrycode/internal/transcript"
)

// snapshotUsageFor builds the by-id context-window usage reader behind the usage
// half of runConfigFor's seam: given a session id per CALL, it resolves that
// session's transcript BY ID and reports its occupancy (used tokens, window
// size). The id is an argument rather than a construction-time closure so one
// reader can answer for any resolved session, not only the bootstrap's (#1608).
//
// #2423 makes the FOLDER per session too. dirFor answers the directory claude
// writes THIS session's <id>.jsonl into, asked for the same id the transcript is
// then resolved by. Before it, the reader held one folder for the whole daemon —
// resolveClaudeSessionsDir of the daemon's own working directory — while
// Pool.buildSession spawns a conversation carrying a cwd in that directory
// instead, so every session_settings reply for such a conversation reported zero
// used tokens against the default window. The reader was never wrong; it was
// handed the wrong folder. sessionTranscriptDir is the production answerer and
// its doc carries the derivation.
//
// Returns nil when there is NO RESOLVER (foreground / unwired), a build-time
// decision that leaves runConfigFor's usage half unwired, so resolved answers
// report zeros — the pre-existing unwired contract, unchanged.
// Deciding it on the resolver's PRESENCE and not on what it answers is what keeps
// that reading: a per-call guard alone would turn an unwired daemon into a working
// reader reporting the default window against a zero used count, a different wire
// shape.
//
// The empty-folder guard survives as a PER-CALL one, and it protects the reader
// itself rather than the wiring: with an empty dir, StatByID would join a RELATIVE
// <id>.jsonl against the daemon's working directory, and a stray file of that name
// there would be read and reported as a session's occupancy. Every "" a resolver
// answers — an unknown id, a runner that cannot name a folder — therefore collapses
// to the same fresh-session report a missing transcript produces.
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
// Every failure collapses to contextwindow.Read("", nil), the same deterministic
// fresh-session report (zero used, default window) a brand-new session produces:
// an id that is empty or malformed, a transcript not yet written, and a file that
// raced away between the stat and the open. A usage reader has no business
// surfacing an error to a client, and it NEVER logs a path.
//
// #2107: windows is the second half of the context-window reading — the windows
// claude reported for the models THIS session used, which the transcript never
// carries. It is asked for the SAME id the transcript is resolved by, which is
// what makes the two halves two readings of one child (see sessionModelWindows,
// whose Pool.Lookup is the isolation enforcement point). contextwindow.Read
// performs the join and every one of its rules; nothing is decided here.
//
// A NIL windows FUNC MUST NOT COLLAPSE THE SEAM, and this is the spot where the
// neighbouring shape is close enough to be pattern-matched wrong. dirFor == nil
// collapses to a nil seam because without a folder there is no transcript to read
// at all — see above. A nil windows func is the runConfigFor case instead, whose
// doc argues it for its own usage half: it yields a WORKING reader whose answers
// carry the default window, which is the pre-#2107 reading exactly. A daemon that
// cannot resolve windows still has transcripts to read, and degrading one integer
// must not make a resolvable session unresolvable. Foreground / v1 is that daemon.
//
// SECURITY: since #2423 the folder derives from a directory a paired client can
// choose (resolveSpawnDir's confined, symlink-resolved output), so the never-log-
// a-path, never-surface-one contract below is load-bearing on this path rather
// than incidental to it. Two independent validators keep the join non-traversable
// and neither is here: sessions' encodeWorkdir replaces both '/' and '.' with '-',
// so an encoded workdir is one path component whatever the requested cwd spelled,
// and StatByID rejects an id failing ValidStem BEFORE the join below.
func snapshotUsageFor(dirFor func(sessionID string) string, windows func(sessionID string) map[string]int) func(id string) (usedTokens, windowTokens int) {
	if dirFor == nil {
		return nil
	}
	return func(id string) (int, int) {
		var path string
		if dir := dirFor(id); dir != "" {
			if res, err := transcript.StatByID(dir, id); err == nil {
				path = res.Path
			}
		}
		var observed map[string]int
		if windows != nil {
			observed = windows(id)
		}
		u, err := contextwindow.Read(path, observed)
		if err != nil {
			// A genuine open failure on a resolved path. Read("", nil) never errors
			// and yields the same fresh-session report, so this branch is
			// deterministic and content-free. It also discards the error rather than
			// propagating it, which is what keeps the resolved path off every
			// surface — the error contextwindow.Read returns wraps the full path.
			// The windows map is dropped with it: with no transcript entry there is
			// no model to join on, so passing it would change nothing.
			u, _ = contextwindow.Read("", nil)
		}
		return u.UsedTokens, u.WindowTokens
	}
}
