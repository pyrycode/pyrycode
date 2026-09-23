# #1551 — correct evergreen docs that present the deleted `internal/sessions` transcript resolvers as live

Short plan: the change is docs-only and belongs entirely to the documentation stage.

## Files read

- `internal/sessions/reconcile.go` → `encodeWorkdir`, `DefaultClaudeSessionsDir` — confirmed these are the only two declarations left.
- `cmd/`, `internal/` (grep) → `newTranscriptResolver`, `availabilityReporter`, `probeUsable`, `newProbePreferredTranscriptResolver`, `mostRecentJSONL` — no declaration or reference remains. The only hit is a doc comment in `internal/transcript/transcript.go` naming `mostRecentJSONL` as an iteration precedent; it is a code comment, out of scope for a docs-only ticket (noted in the PR's Lessons learned).
- `docs/knowledge/features/` and `docs/knowledge/architecture/` (grep for the five names) → matches exactly the site list in the ticket's Documentation handoff: `sessions-package.md`, `sessions-package-status.md`, `sessions-package-key-types-newprobepreferredtranscriptresolver.md`, `sessions-package-key-types-pool-bootstrapid-supervisor-config-resol.md`, `sessions-package-key-types-runner-interface-runnerfactory.md` (leave: already history), `jsonl-reconciliation.md`, `transcript-package.md`, `contextwindow-package.md`, `architecture/system-overview.md`, plus `CATALOG.md`.

## Change

None under `cmd/` or `internal/`. The builder role may not edit `docs/knowledge/`, so every correction is carried forward verbatim as the Documentation handoff below, pending for the documentation stage. The plan file is the only file this branch adds.

## Documentation handoff (pending — documentation stage)

Find each site by the quoted phrase, not by line number.

1. **Delete `features/sessions-package-key-types-newprobepreferredtranscriptresolver.md`.** It documents only the deleted function and holds the second copy of the false "closure wired at `pool.go`'s bootstrap block" claim. Remove its map row in `features/sessions-package.md` under Key Types and its `CATALOG.md` entry. `codebase/838.md` keeps the history.
2. **`features/sessions-package.md`, `reconcile.go` file-map entry:** list `encodeWorkdir` and `DefaultClaudeSessionsDir` only.
3. **`features/sessions-package-status.md`, the `**#838:**` and `**#1149:**` bullets:** keep as history. Correct the #838 bullet's "replaces the `supervisor.Config.ResolveTranscript` wiring at `pool.go`'s bootstrap block" so it no longer states or implies that wiring existed. Record that #1550 deleted both resolvers and the `probeUsable`/`availabilityReporter` handshake.
4. **`features/sessions-package-key-types-pool-bootstrapid-supervisor-config-resol.md`, the "Growth-confirm resolver re-sourced (#1164)" note:** remove it, or mark it as describing a resolver #1550 deleted.
5. **`architecture/system-overview.md`:** (a) the `reconcile.go` file-map line lists `encodeWorkdir` and `DefaultClaudeSessionsDir` only; (b) in the `supervisor.Config` `Fields:` list's `ResolveTranscript` entry, remove "production wiring in `internal/sessions/pool.go` sets it on the bootstrap path only" and "Since #838 the wired closure is `newProbePreferredTranscriptResolver` …" (third copy of the false claim). Change nothing else in that field list — #1561 owns the rest of § Key Types.
6. **`features/jsonl-reconciliation.md`:** change only the two liveness claims — "`mostRecentJSONL` (below) also survives: it still backs `newTranscriptResolver`'s AC5 no-lsof fallback (#838)" and the tail of the `reconcileBootstrapOnNew` paragraph, "`mostRecentJSONL` (below) remains, still backing `newTranscriptResolver`'s …". Both should say the function was deleted in #1149. Leave `## mostRecentJSONL semantics`, the Flow diagram and the historical-reference framing unmodified.
7. **`features/transcript-package.md`:** (a) the Status paragraph ("all three consumers migrated") — say none of the three original families is live any more (#1550 deleted Family A, Family B's file is gone, #2137 retired the rotation watcher) and name the current importers: `snapshotUsageFor` in `cmd/pyry/snapshot_usage.go` and `internal/streamsup`; (b) delete the "Not in scope here" bullet promising "a later consolidation ticket may DRY" the `availabilityReporter`/`probeUsable` helper.
8. **`features/contextwindow-package.md`, `## Related`, the `sessions-package.md` bullet:** it names `ResolveTranscript` / `newProbePreferredTranscriptResolver` and `resolveOwnBootstrapJSONL` and ends "see above", whose target #2539 already removed. Mark both resolvers as deleted or drop the bullet; no sentence may imply the context-window package ever consumed the `internal/sessions` seam.
9. **Leave unmodified:** `sessions-package-key-types-runner-interface-runnerfactory.md`'s #1550 lesson, `rotation-watcher.md`, every `docs/knowledge/codebase/*.md`, `ResolveTranscript` mentions naming none of the four symbols (#1561, #1515), and `resolveOwnBootstrapJSONL` in `turnbridge-package.md` (#1544).
10. **Done when** a grep for the four symbol names under `docs/knowledge/features/` and `docs/knowledge/architecture/` matches only prose marking them deleted or historical. Run `qmd update && qmd embed` afterwards.

## Testing strategy

No code changes, so no new test. The existing `make check` (dispatcher's gate) must stay green; `make docs-guard` inside it covers the documentation stage's edits.
