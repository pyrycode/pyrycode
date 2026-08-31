# Pool.Rename (1.1c-A)

The typed write primitive Phase 1.1c-B's `pyry sessions rename` CLI verb (#47-B) calls instead of poking at `sessions.json` directly. One method, no new types, no new sentinel errors.

```go
func (p *Pool) Rename(id SessionID, newLabel string) error
```

**Sequence under `Pool.mu` (write):**

1. Resolve `id` in `p.sessions`. Unknown ⇒ `ErrSessionNotFound`; in-memory and on-disk state byte-identical to before (no `saveLocked` call).
2. If `sess.label == newLabel`, no-op return — skips `saveLocked` so the registry mtime stays stable for an idempotent rename. Same precedent as `RotateID(x, x)`.
3. Otherwise: snapshot `prev := sess.label`, set `sess.label = newLabel`, call `saveLocked()`. On save failure, restore `sess.label = prev` and return the error verbatim.

**Why `Pool.mu`, not `Session.lcMu`.** `Session.label` is read under `Pool.mu` by `List` (RLock) and `saveRegistryLocked` (Lock — caller holds write). The lifecycle goroutine in `Session.Run` does not read `label`. So `Pool.mu` is the correct guard; taking `lcMu` would add a lock-order edge for no benefit. The doc-comment on `Session.label` (`session.go:68-72`) was updated to reflect that `label` is mutable via `Pool.Rename` under `Pool.mu` (write).

**Why hold `Pool.mu` across the disk write.** Releasing it between the in-memory mutation and the disk write would let a concurrent `Lookup` observe the new label while the disk still has the old one — exactly the in-memory-vs-on-disk drift the existing locking discipline rules out. Matches `RotateID` and every `Pool.persist`-from-`Session.transitionTo` callback.

**Save-failure rollback is belt-and-suspenders.** `saveRegistryLocked`'s temp+rename discipline makes the rename the commit point — partial writes are unreachable, so disk state never disagrees with the rolled-back in-memory state. The rollback exists so a subsequent retry has consistent inputs and a `Lookup` after a failed `Rename` doesn't return a label that isn't on disk. Error returned verbatim (no `Rename:`-specific wrap) — `saveLocked`/`saveRegistryLocked` already produce well-prefixed `registry: …` errors and double-wrapping breaks `errors.Is` symmetry with other persist sites (`RotateID`, `Create`, `Session.transitionTo`).

**Bootstrap-label semantics.** `Rename` writes the verbatim string through to disk and does **not** branch on `sess.bootstrap`. Two cases fall out for free:

- `Rename(bootstrapID, "")` — clears on-disk label; `Pool.List` then re-applies the synthetic `"bootstrap"` substitution introduced in #60.
- `Rename(bootstrapID, "primary")` — persists `"primary"`; `Pool.List` reflects it verbatim (no synthetic substitution, since the on-disk value is non-empty). The disk record is still bootstrap-flagged (`Bootstrap: true`).

The substitution rule lives in `List`, not in `Rename` — keeping the writer simple and the substitution uniform across consumers.

**No validation.** Empty strings are explicitly permitted by the AC; nothing else is mentioned. Length caps, character-class restrictions, and uniqueness checks belong at the CLI layer (47-B) where operator-input policy lives. Same posture as `Pool.Create`'s unvalidated `label` parameter.

**Strict full-`SessionID` only.** UUID-prefix resolution is a CLI/UX concern; the pool primitive matches on the full id to keep the API surface and the test matrix small. Phase 1.1c-B can resolve a prefix via `Pool.List` and then call `Rename` with the full id.
