# Spec #868 — Serialize `conversations.Registry.Save` with a dedicated save mutex

**Ticket:** [#868](https://github.com/pyrycode/pyrycode/issues/868) · **Size:** XS · **Security-sensitive:** no (label absent — write-serialization/lost-update fix; provenance of stored records is not the design surface)

## Files to read first

- `internal/conversations/registry.go:34-37` — `Registry` struct: `mu sync.Mutex` + `conversations []Conversation`. Add the new save mutex here.
- `internal/conversations/registry.go:72-116` — `Save`: the exact snapshot→sort→temp-write→chmod→encode→fsync→close→rename sequence. This is the whole change surface. Note it takes `r.mu` **only** to copy the snapshot (73-76), then does all disk I/O unlocked — that unlocked disk I/O is the bug.
- `internal/sessions/pool.go:1352-1396` — sibling `Pool.saveLocked`: the posture to mirror *in spirit* (serialize the whole save), but note the mechanism differs — sessions serializes under `Pool.mu` (the state lock itself), which blocks read/mutate during the write. We must NOT copy that; a separate save mutex is what preserves read/mutate concurrency (AC #2). Read the lock-order comment at 1356-1358.
- `internal/conversations/registry_test.go:75-124` — `Save`/`Load` round-trip test: the harness idiom (`&Registry{}`, `r.Create(...)`, `t.TempDir()`, `Load` + `List` assertions). The new regression test reuses this shape.
- `internal/conversations/registry_test.go:340-373` — byte-stable-ordering test: shows how to compare on-disk content and how `mk(order)` builds a registry from a fixed slice.
- `internal/conversations/registry_test.go:375-414` — `SaveAtomicRenamePreservesOldFile`: the atomic-write invariants (0600, rename-as-commit, pre-existing target untouched on failure) that AC #3 says must still hold. Do not disturb this test.
- `internal/conversations/conversation.go` — `Conversation` shape (`ID`, `LastUsedAt`, etc.) for building distinct records in the regression test.

Call sites of `Save` (for context — none change, signature is untouched): `internal/conversations/sweep_loop.go:58` (auto-archive sweep), `internal/sessions/transition.go:81` (`/clear` rebind observer), and the v2 relay handlers `create_conversation.go:208` / `rename_conversation.go:110` / `archive_conversation.go:108` / `delete_conversation.go:86`. These are the three-plus production goroutines that call `Save` concurrently.

## Context

`Registry.Save` holds `r.mu` only long enough to copy the in-memory snapshot (registry.go:73-76), then performs temp-write → fsync → **rename** with no lock held. Nothing serializes two overlapping `Save` calls, so their renames can land in an order **inverted** from their snapshot order: an older snapshot's rename lands last and clobbers a newer one on disk. The file self-heals on the next `Save`, but if the daemon restarts before that, the newer mutation is durably lost — a phone-created conversation vanishes, or a `/clear` rebind reverts so the conversation points at a retired session id.

The fix is the reporter's suggested direction, which is correct: a **dedicated save mutex** on `Registry`, held across the whole snapshot→encode→fsync→rename sequence, kept separate from `r.mu`. Taking the snapshot *inside* the save mutex is precisely what forces rename order to match snapshot order; keeping the save mutex separate from `r.mu` is what preserves read/mutate concurrency during the (slow) disk write.

## Design

### Change 1 — add a save mutex to `Registry`

Add one field to the struct at registry.go:34:

```go
type Registry struct {
	mu       sync.Mutex   // guards `conversations` (in-memory reads/mutations)
	saveMu   sync.Mutex   // serializes Save end-to-end; see Save + lock-order note
	conversations []Conversation
}
```

Two-line doc comment on `saveMu`: it serializes the full snapshot→rename sequence so a later snapshot always renames later; it is deliberately separate from `mu` so the disk write does not block concurrent reads/mutations. Zero-value ready (no constructor change — `Load` returns `&Registry{...}`, `&Registry{}` literals in tests keep working).

### Change 2 — acquire `saveMu` across the whole of `Save`

`Save` signature is unchanged: `func (r *Registry) Save(path string) error`. The only edit is to wrap the existing body:

- Acquire `r.saveMu` at the very top; `defer r.saveMu.Unlock()`.
- Leave the rest of the body byte-for-byte as it is — including the existing brief `r.mu.Lock()` / copy / `r.mu.Unlock()` for the snapshot (registry.go:73-76). That inner `r.mu` critical section stays; it is what makes the snapshot copy consistent against concurrent mutators.

Result: the snapshot is taken *after* `saveMu` is held, and the rename happens *before* `saveMu` is released — so for any two `Save` calls, snapshot order == mutex-acquire order == rename order. No behavioural change to the atomic-write recipe (temp file, chmod 0600, fsync, rename-as-commit); AC #3 holds by construction because those lines are untouched.

This is the entire production change: **one field + two lines** (`Lock` + `defer Unlock`) in one file.

## Concurrency model

**Lock order is one-directional: `saveMu` → `mu`, never the reverse.**

- `Save` acquires `saveMu`, then *briefly* acquires `mu` for the snapshot copy and releases it before any disk I/O. It never holds `mu` while acquiring `saveMu`.
- Every mutator (`Create`, `Update`, `RebindSession`, `Delete`, `SetArchived`, `Promote`) and every reader (`Get`, `List`) acquires **only** `mu`. None of them ever touch `saveMu`.

Because no code path holds `mu` while waiting on `saveMu`, the classic AB/BA deadlock is structurally impossible. `Save`-vs-`Save` serializes on `saveMu`; `Save`-vs-mutator contends only on the short `mu` snapshot window, exactly as today. Read/mutate concurrency during the slow disk write is preserved (AC #2): while one `Save` is mid-fsync holding `saveMu`, mutators freely take and release `mu`.

**Why snapshot order == rename order gives the durability guarantee.** Let `S_last` be the last `Save` in `saveMu`-acquire order. Any other `Save` `S'` acquired `saveMu` before `S_last`, so `S'` fully completed its rename before `S_last` even took its snapshot. Every mutation each goroutine performs happens-before that goroutine's own `Save` snapshot. Therefore `S_last`'s snapshot observes every mutation that any completed `Save` observed, and `S_last` renames last → the on-disk file can never end up older than a snapshot a prior completed `Save` already wrote. That is exactly AC #1.

**Reentrancy:** `Save` is never called with `mu` or `saveMu` already held (verified against all call sites — sweep loop, transition observer, relay handlers all call `Save` after their mutators have returned and released `mu`). `sync.Mutex` non-reentrancy is not a hazard here.

## Error handling

No change. Every existing failure path (mkdir, create-temp, chmod, encode, fsync, close, rename) still returns its wrapped error and still leaves the pre-existing target untouched because rename remains the sole commit point. The added `defer r.saveMu.Unlock()` releases the save mutex on every return path, including the error returns — so a failed `Save` does not strand `saveMu` and wedge subsequent saves.

## Testing strategy

Add one regression test to `internal/conversations/registry_test.go`; keep the existing `Save` tests (AC #3 — they must still pass unchanged). The test must be **non-vacuous**: it fails on `main` (pre-fix) and passes on the fix, and passes under `-race`.

**`TestRegistry_SaveConcurrentNoLostUpdate`** — the shape the AC prescribes:

- Outer loop of several iterations (≥10) to convert the timing-dependent lost-update into a near-certain detection across trials. Fresh `t.TempDir()` path per iteration.
- Inner: spawn N goroutines (≥20). Each goroutine **mutates then saves**: `r.Create(<unique conversation i>)` immediately followed by `r.Save(path)`. Coordinate with a `sync.WaitGroup`; no barrier between mutate and save (see the vacuity note below).
- After all goroutines return: `Load(path)` and assert the on-disk file contains **all N** conversations for that iteration (membership by ID). Since each goroutine's `Create` happens-before its own `Save`, the final in-memory state is all N records; AC #1 says the on-disk file must match it.
- Run the whole package under `-race` (`go test -race ./internal/conversations/`) to satisfy the "no `Save`-vs-mutator deadlock, no new race under `-race`" half of AC #1/#2.

**Why a mutate-all-*then*-save-all barrier would be vacuous — do NOT write it that way.** If every goroutine finishes all its `Create`s before any `Save` runs, then every `Save` snapshots the identical full state, so no `Save` can ever clobber another with a stale snapshot — the test passes even on the unfixed code and proves nothing. The lost-update only exists when mutation is *interleaved* with saving, which is why each goroutine must `Create` then immediately `Save` (one mutation per save, saves overlapping in time). This is the same non-vacuity trap flagged repeatedly in prior specs: a test that passes on `main` is not a regression guard.

**Detection is probabilistic per single run but reliable across the outer loop.** On the unfixed code, a `Save` that snapshotted when only k < N records existed can rename after a `Save` that snapshotted all N — leaving k records on disk and failing the membership assertion. The real `fsync` in the write window makes this interleaving common; the outer loop drives per-iteration detection probability toward 1. On the fixed code the assertion holds every iteration deterministically. Keep mutations to `Create` (append-only) so the membership assertion is clean and the snapshot's shallow copy shares no field a concurrent mutator rewrites — this keeps the `-race` run focused on the lock discipline the fix introduces, not on pre-existing shallow-copy sharing that this ticket does not change.

**Dev-time confirmation (not shipped):** before finalizing, temporarily revert the `saveMu` acquisition locally and confirm the new test fails within the outer loop — this proves the guard is real. Restore the fix; the test must then pass under `-race`.

## Open questions

- **Extract vs. duplicate:** `internal/devices/registry.go:141` has the byte-identical `Save` shape and the same latent bug. PROJECT-MEMORY's "Atomic-write recipe" note explicitly says to duplicate until a fifth registry forces extraction, and this ticket is scoped to conversations only. **Do not** touch `internal/devices` or attempt a shared helper here — that is a separate ticket if the devices registry is confirmed to have concurrent savers. Note the parallel in the codebase knowledge doc, nothing more.
- None blocking. The design is fully determined by the AC; no implementation-time decisions remain.
