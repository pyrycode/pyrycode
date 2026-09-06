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

### `spawnEnv` composes the child's environment from the same snapshot, so it can never name a different session than the argv (#2169)

```go
func spawnEnv(base []string, name, sessionID string) []string
```

Pure, allocates fresh, never appends into `base` (`Config.Env`, tested with spare capacity supplied so the no-aliasing claim is actually discriminating rather than hidden by a reallocation). An empty `name` binds nothing and returns `base` untouched — that's how a bypass-adjacent or PTY-side caller with no `SessionIDEnvVar` set gets exactly the environment it asked for.

`Runner.beginSpawn` calls both `buildArgs` and `spawnEnv` from the *same* `restartMu` critical section, off the *same* local `id` read — one lock acquisition producing both the argv and the environment, rather than two functions each reading the live id independently. That single-snapshot discipline is the whole fix for #2169's MUST FIX: an id put on `Config.Env` at runner *construction* (mapped once from `cfg.SessionID` in `mapStreamsupConfig`) goes stale the moment `RestartFresh` rotates the live id, because a process's environment is fixed at exec and nothing re-reads a construction-time field on respawn — see [Constructing a streamRunner § Why `SessionIDEnvVar`](streamsup-package-constructing-a-streamrunner-newstreamrunnerfacto.md) for the mapper-side half of this and [pyry-mcp-files-command.md](pyry-mcp-files-command.md) for the consumer this identity is for. A spawn-time re-read of the live id *instead* of reusing `beginSpawn`'s snapshot would reopen the same defect in a narrower window — it could land on the far side of a racing rotation and skew the argv and the environment apart again, just less often.

**What composing at spawn cannot fix: a `/clear` rotation never respawns.** `beginSpawn` runs only on a fresh exec; `/clear` reaches `Pool.onRotate → RotateID`, which re-keys the pool in place with no `RestartFresh` call, so the *running* child keeps the argv and environment it was last exec'd with until its own next spawn. That is pre-existing behaviour of the rotation seam (the pool observes the rotation; nothing pushes it into a live runner) and fail-closed either way — the general lesson is that a construction-fixed value cannot carry an identity that rotates, and neither can a value fixed at the wrong spawn's exec time; only the *next* exec sees the truth.
