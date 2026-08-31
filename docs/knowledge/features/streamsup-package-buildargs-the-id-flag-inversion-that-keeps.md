# `buildArgs` — the id-flag inversion that keeps the on-disk session stable

```go
func buildArgs(base []string, create bool, sessionID string) []string
```

Pure function, assembled fresh each spawn (never mutates `base`), and — since #1630 — no longer the decider of its own `create` argument:

1. Fixed stream-json prefix: `--input-format stream-json --output-format stream-json --verbose`. **Never `-p`/`--print`** — the non-`-p` choice is billing-classification-tied and was spike-verified live (#1075): multi-turn, interrupt, resume, and the approval round-trip all work without it.
2. Then the caller's `base` (`Config.Args`, e.g. `--model <m>`).
3. Then the id flag: `create == true` → `--session-id <sessionID>` (establishes the on-disk transcript under a known id); `create == false` → `--resume <sessionID>` (reattach, append, **no fork** — `--fork-session` is the explicit, unused opt-in).

Passing the *same* `sessionID` to both flags is why the on-disk session id survives a kill-and-restart untouched — pool id bookkeeping (eviction/reactivation) never has to reconcile a forked id. Mirrors the daemon bootstrap's deterministic-`--session-id` precedent (#839).

### `useCreateForm` — a by-id transcript probe, inert until `Config.ClaudeSessionsDir` is set (#1630)

```go
func useCreateForm(sessionsDir, id string, latchCreate bool) bool
```

`beginSpawn` calls this to produce `buildArgs`'s `create` argument, in place of passing the `firstRun`/`forceFirst` latch straight through. Two modes, chosen by whether `Config.ClaudeSessionsDir` is set:

- **Empty.** Returns `latchCreate` verbatim — no syscall — so the emitted argv is byte-identical to pre-#1630. This was every production path until **#1631** armed the probe (see `mapStreamsupConfig` below); it remains the live behaviour on every arm where the sessions directory can't be derived (empty or unresolvable workdir, unresolvable `$HOME`).
- **Set — every production path since #1631.** Decides *outright* — `latchCreate` is ignored, including on the first spawn and including when a `RestartFresh` rotation re-armed `forceFirst`. A confirmed by-id hit via `transcript.StatByID(sessionsDir, id)` is the only answer that yields `--resume`; every non-hit — absent file, unreadable directory, an id `ValidStem` rejects — yields `--session-id`. No error return: every non-hit is a legitimate answer, not a failure, so the probe can never fail a spawn.

This carries [ADR 032](../decisions/032-bootstrap-resume-per-spawn-existence-probe.md)'s by-id-existence rule — previously applied only to the PTY bootstrap path — into `streamsup`, and closes two defects the `firstRun` latch alone leaves open (see the gate below): a session that launched and ran no turn has no transcript, so a `firstRun`-driven `--resume` against it exits 1 on a widening backoff forever ([#1655](session-transcript-and-resume-probe.md)/[#1656](session-transcript-and-resume-probe.md)); and a transcript that survived a daemon restart makes the latch's first spawn emit a `--session-id` claude refuses (ADR 032). Two things it deliberately never does: hand-roll `filepath.Join(sessionsDir, id+".jsonl")` + `os.Stat` in place of `StatByID` (`StatByID`'s `ValidStem` gate runs *before* the join, and neither `New` nor `RestartFresh` validates `SessionID`'s shape, so a hand-rolled join would turn a non-canonical id into an arbitrary-path existence oracle); and fall back to `transcript.Newest` (a directory scan) on a miss — #839 deleted `--continue` and the adopt-by-mtime scan specifically to close a confused-deputy gap where a restart could adopt a *different* claude's newer transcript out of the shared sessions dir, and a scan fallback would reopen it.

A rotated id colliding with an existing transcript (out of `sessions.NewID`'s reach, never observed) resolves to "resume" simply by falling out of the one rule — deliberately no branch, flag, or test of its own.
