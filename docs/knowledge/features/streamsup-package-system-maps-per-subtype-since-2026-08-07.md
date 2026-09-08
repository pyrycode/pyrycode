# `system` maps per-subtype since 2026-08-07 (#1380) — the wholesale-drop design's first crack
**`system` maps per-subtype since 2026-08-07 (#1380) — the wholesale-drop design's first crack.** `status`
and any never-seen subtype stay silent exactly as before. Five subtypes are now mapped:
`system/task_started` → `turnevent.BackgroundTaskStarted` (`TaskID`, `ToolCallID` — claude's
`tool_use_id`, renamed to match `ToolStart`/`ToolUpdate`'s field name for the same identifier —
`Description`, `TaskType`, `TruncatedFields`); `system/task_updated` → `turnevent.BackgroundTaskUpdated`
(`TaskID`, `Patch`, `TruncatedFields`); `system/background_tasks_changed` → `turnevent.
BackgroundTaskRoster` (`Tasks []BackgroundTask`, `DroppedTasks`) — the aggregate variant, snapshotting
every task claude is tracking at that moment rather than reporting what happened to one;
`system/thinking_tokens` → `turnevent.ThinkingProgress` (`EstimatedTokens`, `EstimatedTokensDelta`,
**no** `TruncatedFields` — two `int`s cannot grow) — the one **rate-bounded** variant, described below;
and `system/init` → `turnevent.ModelAnnounced` (`Model`, `Truncated`) — the one variant naming what
claude is actually running rather than something about a turn or a task, described below (#1600). The
first three mappings fix #1240's symptom: previously a backgrounded command's lifecycle was
indistinguishable from a genuine turn end (`turn_end`/`end_turn`, state `idle`) because the whole
`system` family was dropped regardless of subtype. `thinking_tokens` fixes a different gap: it is
claude's only mid-turn proof of life on this surface, so mapping it gives a client watching a long turn
something to distinguish "slow" from "wedged." `init` fixes a third: the daemon's own `model` fields mean
the per-session override and read empty in the ordinary case, so nothing previously said what claude was
actually running.

The match (`emitSystemSubtype`) sits **inside** `consumeLine`'s existing `ignoredLineTypes` branch rather
than beside it — `system` stays on the list unchanged, so `emitUnrecognized` (the surfaced tier) stays
structurally unreachable from any `system` line whatever its subtype, and `TestParser_
IgnoredLineTypesIsTheMeasuredSet` above is unaffected. `emitSystemSubtype`'s `case` arms are the single
enumeration of the mapped set; every comment describing the drop rule (this file included) points there
rather than restating it — a fifth captured subtype is a new case arm there, not a new sibling ticket.

**Compaction's seam is now observed, still unmapped (#2229).** A live capture against claude 2.1.259
(2026-09-08) settled the question #1074 left open: compaction arrives as two more subtypes on this
same enumeration, not a new top-level type. `system/status` carries `status:"compacting"` while
compaction runs and `status:null` plus `compact_result`/`compact_error` when it ends; a separate
`system/compact_boundary` line carries `compact_metadata` (`trigger`, `pre_tokens`, `post_tokens`,
`duration_ms`). Both still fall through `emitSystemSubtype`'s `default` to `emitUnrecognized` today,
so mapping either onto `turnevent.Compacting` (declared since #1074, unconstructed since #1348 deleted
its only producer) is a sixth and seventh `case` arm here, exactly like the fifth. See
[the capture that observed this](e2e-realclaude-compaction-capture-test-go.md) for why a mapper still
can't be built from this paragraph alone — the fixture that would back a reader's assertion did not
survive the run that produced it.
