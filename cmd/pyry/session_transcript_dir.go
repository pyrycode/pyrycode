package main

import (
	"github.com/pyrycode/pyrycode/internal/sessions"
)

// sessionTranscriptDir answers the directory claude writes a named SESSION's
// <id>.jsonl into, so the usage reader stats that session's transcript in the
// folder belonging to the working directory it was actually spawned in (#2423).
// It is snapshotUsageFor's dirFor argument and its only consumer.
//
// It is sessionModelWindows with one substitution, and deliberately so: the two
// are the same hop — Pool.Lookup on the session id, then a TYPE ASSERTION off
// Session.Runner — because they answer two halves of one reading about one
// child. internal/sessions' Runner doc states the rule that keeps the assertion
// out of the interface: a method goes ON it when its consumer sits INSIDE
// internal/sessions, where a structural assertion would fail open. This consumer
// is in cmd/pyry, so widening would buy no compile-time guarantee and would drag
// every fake runner in two packages into the diff. A runner without the method is
// a REFUSAL, not a panic, and a nil Runner fails the assertion cleanly, so no nil
// check is needed.
//
// THE DEFECT IT FIXES is a mis-wiring, not a wrong primitive. Before #2423 the
// reader was handed ONE folder for the whole daemon — resolveClaudeSessionsDir of
// the daemon's own working directory — while Pool.buildSession spawns a
// conversation that carries a cwd in THAT directory instead. claude writes the
// transcript under the projects folder named for the path it resolved, the stat
// missed, and every session_settings reply for such a conversation reported zero
// used tokens against the default window, which the desktop draws as "Context: 0%".
//
// THE FOLDER IS NOT DERIVED HERE, and that is the whole of the fix's correctness.
// What comes back is the value the session's own runner holds for its spawn probe
// (streamClaudeSessionsDir, called in mapStreamsupConfig at construction and again
// in streamRunner.SetSpawnWorkDir on a rotation that moves the workspace), so the
// reader and the probe read ONE field of ONE struct and no path exists on which
// they can name different folders for one session. Since #1475 that field is the
// RUNNER'S rather than the adapter's, which is what keeps the two together now
// that it can change: a copy taken at construction would answer the pre-move
// folder for every reading after a workspace move, restoring the pre-#2423
// reading on exactly the conversations that moved.
// A second streamClaudeSessionsDir call here would agree today and is
// exactly the shape that drifts: agentrun.ResolveWorkdir applies canonicalCase
// and symlink resolution, which neither confineWorkdirToHome nor resolveSpawnDir
// does, so re-deriving from a differently-spelled workdir names a folder claude
// never writes.
//
// "" IS THE ONLY REFUSAL. An unknown or empty id, a runner without the method,
// and a runner whose own derivation degraded (an unresolvable workdir, no $HOME)
// all answer it, and snapshotUsageFor reads "" as "do not stat" — which is what
// keeps a relative <id>.jsonl from joining against the daemon's process
// directory. There is deliberately no second spelling and no fallback to the
// daemon's own folder: falling back would restore the pre-#2423 reading on
// exactly the sessions this ticket exists for, silently.
//
// daemonSessionsDir is a GATE, NEVER A VALUE. When it is "" this returns nil
// before any closure exists, which is what keeps AC 5's unwired shape:
// snapshotUsageFor collapses a nil resolver into the nil seam that makes the
// handlers report zeros, rather than a working reader reporting the default
// window against a zero used count — a different wire shape. Deciding it on the
// resolver's PRESENCE rather than on what it answers is bootstrapSnapshotUsage's
// and runConfigFor's own structural rule. Reading the daemon's directory to
// answer a question about a session's is sound because the two are one question:
// both bottom out in sessions.DefaultClaudeSessionsDir, which needs $HOME, so a
// daemon that cannot name its own projects root cannot name a session's either.
//
// SECURITY: the folder now derives from a directory a paired client can choose —
// resolveSpawnDir's confined, symlink-resolved output — so the reader's existing
// contract is load-bearing on this path rather than incidental to it. Nothing
// here surfaces a path: this function takes NO logger and MUST NOT grow one, for
// sessionModelWindows' reason verbatim — the only fields a diagnostic could carry
// are a session id and a filesystem path, which is exactly the channel the #833
// posture closes. Pool.Lookup's error is DISCARDED rather than wrapped, for
// resolveBoundRunSettings' stated reason: returning it bare is what keeps a
// hostile or malformed id from being reflected into a log line or a wire frame.
// Traversal is closed twice over and neither check lives here: sessions'
// encodeWorkdir replaces both '/' and '.' with '-', so the encoded workdir is one
// path component whatever the requested cwd spelled, and transcript.StatByID
// rejects an id failing ValidStem before any path join.
//
// Concurrency: a synchronous read on the caller's goroutine. The pool's lock and
// the runner's field read are acquired SEQUENTIALLY and never nested, so this
// adds no edge to the daemon's lock order. The Lookup → ClaudeSessionsDir window
// is the benign TOCTOU the resolver family documents. It used to be weaker than
// sessionModelWindows' because no rotation could change a session's working
// directory; #1475 made a new_session rotation able to, so a rotation landing in
// the window can now make the two readings name different folders. The residual is
// one session_settings reply computed against one of two REAL folders for that
// session, both of which claude has written to, and the next reply is correct.
// Closing it would mean holding Pool.mu across a runner lock — a new lock-order
// edge bought for a stale read that is already self-correcting.
func sessionTranscriptDir(pool *sessions.Pool, daemonSessionsDir string) func(sessionID string) string {
	if daemonSessionsDir == "" {
		return nil
	}
	return func(sessionID string) string {
		sess, err := pool.Lookup(sessions.SessionID(sessionID))
		if err != nil {
			return ""
		}
		holder, ok := sess.Runner().(interface{ ClaudeSessionsDir() string })
		if !ok {
			return ""
		}
		return holder.ClaudeSessionsDir()
	}
}
