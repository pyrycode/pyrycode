# ADR 032: Bootstrap resumes (never rotates) its pinned id, decided per-spawn by a by-id transcript-existence probe

## Status

Accepted (ticket #1164).

## Context

#839 made the bootstrap claude spawn with `--session-id <bootstrapID>` on
**every** spawn, resolved fresh each time from the daemon's own persisted id
(`Config.ResolveSessionID`, a pull seam) — deterministic, and closing a
confused-deputy isolation gap where a restart could adopt a second claude's
newer transcript from the shared sessions dir.

claude 2.1.199 refuses `--session-id <uuid>` when `<uuid>.jsonl` already
exists on disk ("Session ID … is already in use", exits in ~220ms). On a
hard restart (`launchctl kickstart -k`, a binary cutover, a crash) the
transcript survives, so the reused persisted id collides **deterministically**
— every backoff respawn fails identically and the daemon crash-loops with no
working bootstrap session. `internal/streamsup` has the same
transcript-exists constraint but doesn't loop: it spawns `--session-id` on
`firstRun` and `--resume` on every respawn thereafter, so it wastes exactly
one ~220ms crash on daemon restart then self-heals. The PTY supervisor's
`buildClaudeArgs` had no `--resume` branch at all — when `sessionID != ""` it
always emitted `--session-id`, regardless of `firstRun` — so it could never
escape the loop. That missing branch was the root cause.

The fix had to preserve #839's determinism: no `--continue` (resumes
"most recent", non-deterministic), no adopt-by-mtime dir scan of the shared
sessions dir. Both were deleted by #839 specifically to close the isolation
gap.

## Decision

**Resume, not rotate**, decided **per spawn** by a **by-id existence probe**:

> Spawn `--resume <id>` iff `<id>.jsonl` already exists (by-id `transcript.StatByID`,
> no dir scan); else `--session-id <id>` (create).

`Config.ResolveSessionID` widened from `func() string` to `func() (id string,
resume bool)`, resolved in one call at the start of every spawn so the
`StatByID` probe always targets the id that's about to be spawned (no
id/decision skew if a `/clear` rotation races the loop). `buildClaudeArgs`
gained a `resume bool` parameter and a `--resume <id>` branch alongside the
existing `--session-id <id>` branch; both suppress `--continue`.

## Rationale

### Resume over rotate

| | Resume (chosen) | Rotate (rejected) |
|---|---|---|
| Preserves the idle bootstrap conversation | **Yes** — the whole point of #839 pinning | No — drops it on every restart, permanently; pinning becomes pointless |
| Within-process respawn after first spawn | Handled uniformly (transcript exists → `--resume`) | Still collides (`--session-id <fresh-id>` refused on the 2nd spawn) — relies on #1165 |
| Determinism / isolation | `StatByID` stats exactly `<id>.jsonl` — no dir scan | Same (by-id), but the id churns every restart |
| Relationship to #1165 | Different fabric (resume vs. rotate) → clean belt-and-suspenders | Same fabric as #1165 → redundant |

The user story's value is "a working bootstrap session after restart" **and**
#839 pinned the id specifically to keep *that idle conversation* across
restart. Rotate yields a working-but-empty session and defeats #839's
purpose; resume yields working-and-preserved. `--resume <id>` (always with an
id, never bare) is the same reattach form `internal/streamsup` already uses
and `docs/lessons.md:54` documents as safe — bare `--resume` opens claude's
interactive session picker, which is the case #839's predecessor rejected;
passing an id sidesteps it entirely.

### Per-spawn over a one-shot decision at `New()` or a `firstRun` heuristic

A decision fixed at construction time is insufficient: cold start correctly
picks `--session-id` (no transcript yet), but the moment the first child
creates `<id>.jsonl`, the next in-process respawn with that same fixed
decision collides. Only a per-spawn rule is uniformly correct across all
three cases (cold start / in-process respawn / daemon restart) with a single
branch and no bookkeeping. This is a deliberate improvement over
`streamsup`'s `firstRun` heuristic, which is only correct because it wastes
exactly one crash on daemon restart before self-healing — the PTY path has
no such self-heal, so `firstRun` alone would still loop forever on the very
case this ticket exists to fix. The probe rides #839's existing "resolve
fresh every spawn" cadence; no new seam in the supervisor loop.

### Why not defer to #1165 entirely

#1165 (independent, no blocked-by relationship) is a generic N-fast-crash
self-heal that rotates to a fresh id after *any* sustained crash-loop —
correct as a last-resort net, but it doesn't distinguish "transcript exists,
resumable" from "genuinely broken," so it would rotate (and drop the
conversation) on every restart, same cost as today's manual runbook recovery,
just automatic. Resume is strictly better for the common cases (daemon
restart, in-process respawn) and #1165 remains a clean, independent net for
the residual case only (a corrupt/unresumable transcript where `--resume`
itself fails).

## Consequences

**Going forward:**

- A hard daemon restart with a surviving `<bootstrapID>.jsonl` reattaches to
  the same conversation instead of crash-looping. The manual runbook
  recovery (stop daemon, blank `sessions.json`, restart) is no longer needed
  for this failure mode.
- Any future spawn-time flag decision that depends on mutable on-disk state
  should follow the same shape: widen the existing per-spawn provider's
  return tuple rather than adding a `firstRun`-style heuristic or a
  construction-time snapshot.
- `#1165`'s rotate net still exists and is unaffected — it now only fires for
  genuinely unresumable transcripts, not the common restart case.

**Trade-offs accepted:**

- A transcript that claude refuses to `--resume` for reasons other than
  non-existence (corruption, partial write) still crashes and depends on
  `#1165` to eventually rotate out of it. #1164 alone cannot detect
  "exists but unresumable" versus "exists and resumable" — only
  `StatByID`'s existence, not claude's own resume-validity check.
- Interactive `--resume <id>` under the PTY/tui-driver readiness gates had
  not been exercised in production before this ticket (the bootstrap
  previously used `--continue`, then #839's `--session-id`); the `-tags e2e`
  real-claude run is the validating ground truth, not a first-principles
  proof that every readiness gate behaves identically on a resumed session.

## Related

- [#839 (codebase/839.md)](../codebase/839.md) — the deterministic `--session-id`-every-spawn design this ADR extends; the create-path stays byte-identical when no transcript exists.
- [codebase/1164.md](../codebase/1164.md) — implementation summary.
- [features/sessions-package.md](../features/sessions-package.md) § `Pool.BootstrapID` + `supervisor.Config.ResolveSessionID`.
- [architecture/system-overview.md](../architecture/system-overview.md) § `supervisor.Config`.
- `docs/specs/architecture/1164-bootstrap-resume-existing-transcript.md` — build-time spec (resume-vs-rotate comparison table, full design).
- `docs/lessons.md:54` — `--resume <uuid>` (with id) is a real, safe, used spawn form; bare `--resume` is not.
- Sibling: #1165 — independent generic N-fast-crash rotate safety net, different fabric, not implemented by this ticket.
