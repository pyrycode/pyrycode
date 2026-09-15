# #2448 — A daemon restart keeps a dropped session's model and effort, and `Revive` restores them

## Files read

- `internal/sessions/pool.go` → `Pool` (the struct the new map joins), `New` (the only `loadRegistry` consumer; `pickBootstrap` is its only reader of `reg.Sessions`), `saveLocked` (rewrites the file from `p.sessions` alone — the erasure), `Pool.Remove` (the operator's drop), `mintSettings` (the fail-closed field-by-field precedent this ticket's revive settings mirror), `buildSession` (composes `claudeSettingsArgs(settings)` into the spawn argv at construction), `Rename` (the cheapest save trigger a test can drive).
- `internal/sessions/get_or_create.go` → `materialise` — the take-or-register core, its critical section and its **two** rollback paths. `GetOrCreateIn` reaches it with a caller-supplied id too, so it is the retire site, not `Revive`.
- `internal/sessions/revive.go` → `Pool.Revive` — the zero-`SessionSettings` contract paragraph this ticket rewrites.
- `internal/sessions/registry.go` → `registryEntry` (the persisted shape, `model`/`effort`/`yolo`/`permission_mode`), `permissionModeForDisk`, `settingsFromEntry`, `loadRegistry`, `sortEntriesByCreatedAt` (already orders a merged list), `saveRegistryLocked` (atomic temp+fsync+rename).
- `internal/sessions/session.go` → `canonicalSettings` / `canonicalPermissionMode` (turn the cleared posture into the spelled-out default), `claudeSettingsArgs` (the `--model` / `--effort` / posture composition, unchanged by this ticket), `permissionModeInBand`.
- `internal/sessions/modelfamily.go` → `familyAlias` — why AC#4's argv assertion must expect the family, not the stored bytes (#2447).
- `internal/sessions/pool_revive_test.go` → `TestPool_Revive_PoolNotRunning` — the byte-level registry-unchanged assertion the rollback paths have to keep satisfying.
- `internal/sessions/pool_mint_settings_test.go` → `helperPoolMintBootstrap` (the pre-write-a-registry-then-warm-start fixture recipe), `TestPool_Revive_DoesNotInheritOperatorSettings` (must keep passing unchanged — it revives an id with no persisted entry).
- `internal/sessions/pool_settings_test.go` → `helperPoolArgvRecorder`, `waitArgv`; `internal/sessions/session_settings_test.go` → `alwaysOnPosture`.
- `docs/knowledge/decisions/035-lazy-revive-not-registry-rehydrate.md` — the zero-settings rationale and its "do not thread persisted settings into `Revive` without re-opening this ADR" instruction. This ticket is the re-opening, partial: lazy revive stays, the posture stays revoked.
- `docs/knowledge/features/sessions-package-key-types-reviving-a-dropped-session-pool-revive.md` § "Zero settings — fail-closed by construction" — the paragraph the documentation stage rewrites.

## Context

Two independent defects lose a session's `model` and `effort` across a daemon restart.

**The erasure.** `New` materialises only the bootstrap entry, and `saveLocked` rebuilds the whole file from `p.sessions`. Every other entry `loadRegistry` decoded is dropped on the floor, so the first save after a restart — a bootstrap idle-eviction is enough — deletes it permanently. ADR 035 listed this as its first accepted residue; this ticket closes it.

**The revive.** `Pool.Revive` passes a zero `SessionSettings` to `materialise`. The #1487 security review wanted a phone-granted YOLO not to outlive a restart, which holds for YOLO and permission mode; it does not transfer to model and effort, which carry no privilege. The zero value was the simplest fail-closed spelling, not a decision that the model should go.

Nothing new is needed to get restored settings onto the child: `buildSession` already composes `claudeSettingsArgs(settings)` into the spawn argv, and a revived session registers evicted and spawns on its first `Activate`, which since #2085 is the first message. Only the posture needs an in-band correction (#2065); model and effort have always been launch flags.

This re-opens ADR 035 deliberately and partially. The documentation stage records the amendment; this plan's **Documentation handoff** names it.

## Design

One new piece of `Pool` state, four call sites, no new exported symbol and no `registryEntry` field.

### `Pool.dormant`

```go
// dormant holds the entries loadRegistry returned that this pool did not
// materialise, keyed by id. Guarded by p.mu, the same lock as p.sessions.
dormant map[SessionID]registryEntry
```

Meaning, exactly: *a persisted entry with no live `Session` behind it*. That precision is what #2449 builds its dormant settings read on, and it is why the retire below sits in `materialise` rather than only in `Revive`.

A nil map is safe for every operation this design performs on it outside `New` — read, `delete`, `range` — and the one write (the rollback restore) is reachable only after a read hit, which a nil map cannot produce. The bare `&Pool{}` literals in this package's tests therefore need no change.

### `New` — keep what it does not materialise

After `pickBootstrap` has chosen `bootstrapID`, every `reg.Sessions` entry whose `ID` is not `bootstrapID` becomes a `dormant` entry, and the map joins the `&Pool{}` literal. `reg == nil` (cold start) yields an empty map. The cold-start-with-entries-but-no-bootstrap case — `pickBootstrap` returns nil, a fresh id is minted — keeps *every* file entry, which is the correct reading of "an entry the pool did not materialise". Entries are preserved verbatim rather than filtered or repaired: they are the operator's data, and duplicate ids in a hand-edited file collapse to one by map key, which is strictly better than what the file held.

### `saveLocked` — merge, with the live session winning

After the `p.sessions` loop, append each dormant entry whose id is **not** in `p.sessions`, then `sortEntriesByCreatedAt` as today. The slice is pre-sized to `len(p.sessions)+len(p.dormant)`.

The skip-if-live guard is the single write-point enforcement of the invariant *an id reaches disk exactly once, from the live session whenever one exists*. It is unreachable on today's paths — `materialise` retires and `Remove` drops — and it is kept anyway because `rekeyLocked` (`RotateID`, `AdoptAnnouncedID`) moves a live session onto an id this package does not choose, claude does. One `continue` at the write point covers every id-mutating path, present and future, instead of a `delete` per path; a duplicate id in `sessions.json` is silent corruption no `loadRegistry` reader can disambiguate.

### `materialise` — retire the entry it takes over

Inside the existing critical section, immediately after `p.sessions[id] = sess` and **before** `saveLocked`: capture the dormant entry and delete it. Both rollback paths — the `saveLocked` failure and the `ErrPoolNotRunning` one — restore it beside their existing `delete(p.sessions, id)`, so the registry the second `saveLocked` writes is byte-identical to the one on disk before the call (`TestPool_Revive_PoolNotRunning`'s assertion, which a dormant-id fixture now exercises for real). The take path touches nothing: a live id is never dormant.

This is in `materialise` rather than in `Revive` because `GetOrCreateIn` also takes a caller-supplied id and can land on a dormant one.

### `Pool.Remove` — drop it for good

`delete(p.dormant, id)` beside the existing `delete(p.sessions, id)`, before `saveLocked`. With the retire above it is a no-op on every reachable path, and it is the half of the invariant that belongs at the one site that deletes a live session: without it, a design where the retire moved or weakened would resurrect a removed session on the next save. The `saveLocked`-failure rollback restores `p.sessions[id]` only — a live id's dormant entry is skipped at the write point either way, so the file stays byte-identical with nothing else to undo.

### `Pool.Revive` — restore model and effort, revoke the posture

```go
// revivedSettings returns the settings a revive of id starts from: the persisted
// model and effort, never a persisted posture. p.mu must be unheld.
func (p *Pool) revivedSettings(id SessionID) SessionSettings
```

`Revive` calls it before `materialise` and forwards the result. Two notes on the shape:

- **Field-by-field, not `settingsFromEntry` plus a clearing statement.** The ticket's technical notes spell it as `settingsFromEntry` followed by clearing `YOLO` and `PermissionMode`; this plan deviates to the two-field literal `mintSettings` already uses in this file, for `mintSettings`' own recorded reason — the posture is then excluded *structurally* rather than by a statement someone can later delete, and any field added to `registryEntry` in future is likewise not inherited until someone opts it in. Same observable behaviour, strictly more fail-closed, and it is the shape the sibling constructor of per-session settings already has.
- **The cleared posture is spelled out downstream, not here.** `buildSession`'s `canonicalSettings` turns the zero `PermissionMode` with `YOLO` false into `permissionModeDefault`, exactly as it does for today's zero value — so a revived session's stored mode and its argv's `--permission-mode default` are unchanged from today whatever the entry carried (AC#3).

An id with no dormant entry yields the zero value, i.e. today's behaviour byte for byte (AC#5), which is why `TestPool_Revive_DoesNotInheritOperatorSettings` and the two `runner_config_posture_test.go` revive arms keep passing unchanged — they revive ids that were never persisted.

`Revive`'s docstring's zero-settings contract paragraph is **rewritten**, not appended to: it currently states the opposite of what ships. The `materialise` docstring's matching sentence ("Revive passes the zero value, so a revived one inherits nothing") and `buildSession`'s are corrected in the same commit.

### Deliberately unchanged

`List`, `Snapshot`, `Lookup`, `ResolveID` and `SettingsFor` keep answering from live sessions alone — #2449 adds the dormant read. Idle eviction keeps the `Session` in `p.sessions` with its settings, so it never consults `dormant`. No `registryEntry` field is added; a revived entry still takes a fresh `CreatedAt` (ADR 035's third residue, out of scope).

## Concurrency model

No new goroutine, no new lock, no new lock order. `p.dormant` is guarded by `p.mu` on exactly the discipline `p.sessions` already has: mutated under the write lock inside `materialise`'s and `Remove`'s existing critical sections, read under the write lock by `saveLocked` (whose caller holds it) and under the read lock by `revivedSettings`.

`revivedSettings` **must be called with `p.mu` unheld** — `RWMutex` is not reentrant and `materialise` takes the write lock — which is `mintSettings`' existing contract and `Revive`'s existing call shape (evaluate, then `materialise`). The window between that read and `materialise`'s `Lock` is benign: a concurrent revive of the same id wins the map, and our settings are dropped on the take path, which is the take path's documented contract.

## Error handling

No new failure mode and no new error value. The two `materialise` rollbacks and `Remove`'s gain one map write each, none of which can fail. `saveLocked`'s and `saveRegistryLocked`'s error paths are untouched. The single behavioural change on an error path is that a rolled-back `materialise` of a **dormant** id now restores the entry, so the registry bytes are unchanged — previously that id had no entry to restore and the same assertion held vacuously.

A malformed or hand-edited entry is preserved rather than rejected: `loadRegistry` already fails the whole parse on malformed JSON, and anything that decodes is written back as decoded.

## Testing strategy

New file `internal/sessions/pool_dormant_entries_test.go`, plus one fixture helper that pre-writes a registry carrying a bootstrap entry and named dormant entries and warm-starts `helperPoolArgvRecorder` from it (the `helperPoolMintBootstrap` recipe, generalised to more than one entry).

- **Entries survive an unrelated save (AC#1).** Warm-start over a file holding bootstrap + two entries, one carrying `model`/`effort`; drive a save with `Rename` on the bootstrap; re-`loadRegistry` and assert both entries are still there with `Model` and `Effort` intact. This is the RED test — today's `saveLocked` erases them.
- **`Remove` is final (AC#1, second half).** Revive a dormant id, `Remove` it, assert it is gone from the file, then drive another save and assert it has not come back.
- **`Revive` restores model and effort (AC#2).** Revive a dormant id carrying both; assert through `SettingsFor`.
- **`Revive` revokes the posture (AC#3).** Table-driven over the two persisted postures — `yolo: true`, and a non-default in-band `permission_mode` — asserting `YOLO` false and `PermissionMode == permissionModeDefault` for both.
- **The first spawn carries them (AC#4).** Revive then `Activate`, and assert the recorded argv equals `--session-id <id> --model <familyAlias(stored)> --effort <stored>` plus `alwaysOnPosture(permissionModeDefault)`. The stored model is an **exact id** so the assertion pins the #2447 rewrite rather than passing on an alias that happens to be its own family.
- **Nothing persisted is unchanged (AC#5).** An entry with neither model nor effort, and an id with no entry at all, both revive to the zero settings and spawn the argv they do today.
- **Rollback stays byte-identical.** `TestPool_Revive_PoolNotRunning`'s shape against a **dormant** id, on a pool that never ran: `ErrPoolNotRunning`, no in-memory entry, and the registry bytes unchanged.

Gate: `go test -race ./internal/sessions/...`, `go vet ./...`, `go build ./cmd/pyry`. `cmd/pyry`'s `sessionRouter.revive` is untouched — no signature moves — so no consumer needs updating.

## Documentation handoff

Pending for the documentation stage; not edited by this ticket.

- `docs/knowledge/decisions/035-lazy-revive-not-registry-rehydrate.md` — record the amendment. Its zero-settings rationale and its "do not thread persisted settings into `Revive` without re-opening this ADR" instruction are what this ticket changes, **for model and effort only**; the posture stays revoked and lazy revive stays. Its first listed residue (an untouched conversation's entry erased by the next unrelated `saveLocked`) closes.
- `docs/knowledge/features/sessions-package-key-types-reviving-a-dropped-session-pool-revive.md`, the "Zero settings — fail-closed by construction" paragraph — say what `Revive` restores, what it still clears, and why the two differ.

## Open questions

1. Does any existing test assert that a warm start *erases* non-bootstrap entries? If one pins the old behaviour it has to be re-pointed, not deleted, and the rewrite recorded here. (Resolve by running the package suite in Phase B.)
2. Does `sortEntriesByCreatedAt` over the merged list reorder a file written before this change? It sorts on `(CreatedAt, ID)` and the dormant entries carry the timestamps they were written with, so the expectation is no — confirm against the byte-identical rollback assertion.

Both are resolved in Phase B; anything that moves the design lands in `## Revisions`.

## Revisions

**2026-09-15 — Open questions resolved, design unchanged.**

1. ~~No existing test pinned the erase.~~ **Wrong — corrected by the 2026-09-15 rework entry below.** One test did pin it, in a package this gate never ran.
2. `sortEntriesByCreatedAt` over the merged list does not reorder a file this process passes through — dormant entries carry the `CreatedAt` they were written with. Confirmed by `TestPool_Revive_PoolNotRunning_RestoresDormantEntry`, which is byte-level and reddens today.

No design change followed from either, so the implementation is the plan above as committed.

**2026-09-15 — two production files beyond the three the plan named.** Five in total, still inside the one-ticket boundary, and neither addition changes the design:

- `internal/sessions/registry.go` gains `dormantEntries`, the helper `New` populates the map through. It sits beside `pickBootstrap`, whose complement it is, rather than inline in `New` — the plan described the behaviour and not its address.
- `internal/sessions/session.go` — a four-line docstring correction only. `canonicalSettings` named `Pool.Revive`'s *zero value* as one of the two it normalises, which this ticket makes false; it now names the unset posture that `mintSettings` and `revivedSettings` both leave behind. `buildSession`'s and `materialise`'s matching sentences were corrected in `pool.go` and `get_or_create.go` for the same reason.

**2026-09-15 — rework: Open Question #1 was answered wrongly, and the gate is what hid it.** Verifier triage on PR #2462 found one regression, `TestSessionRouter_Resolve_RevivesAfterDaemonRestart` in `cmd/pyry`. The finding is correct; no production file changes because of it.

- **What Open Question #1 actually resolves to.** One existing test did pin the erasure, and it states it as a *precondition*: `TestSessionRouter_Resolve_RevivesAfterDaemonRestart` warm-starts a second pool over the first's registry, drives a bootstrap `Rename`, and asserts the minted entry is gone from `sessions.json`. That is exactly the behaviour AC#1 deletes, so the rung reddens by design. Re-checked tree-wide across `*.go` this time rather than in the two packages the first pass happened to run: it is the only such site.
- **Why the first answer read as true.** The plan's Testing-strategy gate line runs `go build ./cmd/pyry` but never `go test` there, and `cmd/pyry` is where the pin lives. A persistence change to `sessions.Pool` is consumed by `sessionRouter`, whose restart test is the only end-to-end pin of the behaviour being changed — so the consumer package belonged in the gate, not merely in the compile. The gate for this push is `go test -race ./internal/sessions/... ./cmd/pyry/...`.
- **The fix, and it is the plan's own instruction: re-pointed, not deleted.** The pre-touch persist rung inverts — after the `Rename` the entry must now be *present* — and its comment is rewritten rather than amended, the same treatment `Revive`'s docstring got. The `Lookup`/`ErrSessionNotFound` rung above it is untouched and stays load-bearing: a warm start still does not materialise the entry, it now lands in `Pool.dormant`, and that rung is what keeps the assertions below it non-vacuous. The AC#2 `registryHasSession` check after the post-revive persist is unaffected and still passes, but what makes it non-vacuous shifts from the erasure to `materialise`'s retire — the rewritten comment says so, since the sentence that explained the old rationale is gone.

No design change followed: the regression was a stale test precondition, not a defect in the change.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No finding, but the boundary moves and is worth stating. Before this ticket only the *bootstrap* entry crossed from `sessions.json` into a live session's settings; after it, any entry can, via `Revive`. The values crossing are not fresh untrusted input: a non-bootstrap entry's `model` and `effort` were written by `Pool.UpdateSettings` from a frame `internal/relay`'s `validEffort` (a closed five-value enum) and `validModel` (a shape check), plus `cmd/pyry`'s `validateModelVocabulary`, had already gated. The one *new* property is durability — a value that previously stopped being live at the next restart now does not — and the sink it reaches is unchanged. A hand-edited registry can still hold anything, which is the operator writing to their own `0700` data dir and is the case `familyAlias`'s own SECURITY note already answers structurally ("every output byte is an input byte"; a rewritten value is strictly narrower than `validModel` accepts).
- **[#1487 / threat model]** No finding on the grant path, one hazard handed forward. No path lets a persisted `yolo: true` or non-default `permission_mode` reach a revived session: `revivedSettings` is a two-field literal, so the posture is excluded structurally rather than cleared; `canonicalSettings` then spells the default out; `buildSession` seeds `RunnerConfig.PermissionMode` from those settings and derives `OperatorBypass` from the settings-free `base`, neither of which reads the entry. **The hazard this ticket creates is at rest, not at spawn:** the escalation bit now *survives on disk* where ADR 035 recorded it being erased, so #2449's dormant settings read must apply the same revoke `Revive` does, or it will report an escalated posture to the phone that the next revive will not honour. Naming it here because the fix belongs to #2449 and stripping the posture at write time is not available: the ticket's byte-identical-rollback invariant requires dormant entries be written back verbatim.
- **[File operations]** No finding — and the reason is structural, not incidental. A dormant entry's `ID` never reaches a filesystem path: it is a map key only. The id that reaches `writeMCPSettings` / `writeSystemPrompt` comes from the *caller* (`sessionRouter.revive`, off the conversation's `CurrentSessionID`) and `materialise` gates it on `ValidID` before `buildSession` runs, so a corrupt id on disk can only fail to match a lookup. No new path construction, no check-then-use; persistence stays on `saveRegistryLocked`'s existing atomic temp-`0600`-fsync-rename into a `0700` dir, with no field added to `registryEntry`.
- **[Subprocess]** No finding. Restored `model` and `effort` reach claude as separate argv *elements* through `claudeSettingsArgs`, the same single sink a live session's respawn already composes them at; no `sh -c` anywhere on the path, no environment change. `--effort` is emitted with no rewrite, which is why the wire-side `validEffort` enum noted above is load-bearing rather than incidental.
- **[Errors, logs, telemetry]** No finding, and one rule to hold in Phase B: nothing added by this ticket may log. `Revive` must not log `spawnDir` (the #741 precedent), and the restored `model`/`effort` are per-session state that nothing logs today. `revivedSettings`, the retire, the restore and the merge are all silent; no error string carries a value.
- **[Concurrency]** No finding. `p.dormant` takes `p.sessions`' existing discipline under the same `p.mu`; no new lock, no new order, no goroutine. The one lock-free window — `revivedSettings` reading before `materialise` takes the write lock, forced by `RWMutex` non-reentrancy and identical to `mintSettings`' contract — is benign in both directions: a concurrent revive of the same id sends us down the take path, where the settings are dropped by contract, and `Pool.Remove` cannot race the read at all, because it requires the id to be *live* and a live id is never dormant.
- **[Tokens, secrets, credentials]** Not applicable — this design handles no credential material. `sessions.json` gains no field and no value that was not already written for every live session.
- **[Cryptographic primitives]** Not applicable — no randomness, no key material, and no comparison of an attacker-controlled value against a secret. Session ids are generated by `NewID` on paths this ticket does not touch.
- **[Network & I/O]** OUT OF SCOPE, named. There is no network surface here, but the registry stops being self-pruning: entries were erased by the first post-restart save, and now accumulate for the life of the install. `p.dormant` is bounded by the file and only ever shrinks after `New`, so the cost is a few hundred bytes per session ever created — bounded by operator action, not by anything a phone can drive, and prunable today through `sessions.remove`. A retention policy is a separate ticket, not this one: keeping the entry is precisely what AC#1 asks for.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-15
