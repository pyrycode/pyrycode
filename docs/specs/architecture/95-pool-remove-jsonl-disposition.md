# Spec — Pool.Remove JSONL disposition (leave / archive / purge)

Ticket: #95 (split from #64). Phase 1.1d-A2. Builds on #94 (`Pool.Remove`
core, merged).

## Context

#94 landed `Pool.Remove(ctx, id) error` — terminate + registry remove,
JSONL untouched. This ticket layers configurable on-disk JSONL handling
on top: callers pick **leave** (default — same as #94), **archive** (move
to a per-instance archive subdir), or **purge** (delete).

The disposition belongs in `Pool` for the same reason `Pool.Remove`
itself does: the pool already owns the registry mutex, the data-dir
plumbing, and the disk-write discipline. The control-plane / CLI layer
(#65) consumes the final `(ctx, id, opts)` shape once this ships.

The shape is deliberately a sibling of `Pool.Rename`/`Pool.Create`/
`Pool.Remove` (#94): a small surface, one entry point, ride existing
seams. No new exported sentinels; one new exported value type
(`RemoveOptions`) plus its policy enum.

## Design

### Public surface

One new exported type, one new exported enum, three constants, and a
**signature change** to `Pool.Remove`:

```go
// JSONLPolicy controls how Pool.Remove handles a session's on-disk
// JSONL transcript file. The zero value (JSONLLeave) preserves the
// 64-A1 behaviour: the JSONL is untouched.
type JSONLPolicy uint8

const (
    JSONLLeave   JSONLPolicy = iota // do not touch the JSONL (default)
    JSONLArchive                    // mv to <pyry-data-dir>/archived-sessions/<uuid>.jsonl
    JSONLPurge                      // delete the JSONL
)

// RemoveOptions extends Pool.Remove with disposition policy. The zero
// value behaves identically to the 64-A1 (#94) Pool.Remove: terminate
// the child, drop the registry entry, leave the JSONL on disk.
type RemoveOptions struct {
    JSONL JSONLPolicy
}

// Remove terminates the named session's claude process (if running),
// drops its registry entry, and applies opts.JSONL to the on-disk
// transcript file.
//
// opts.JSONL == JSONLLeave (zero value): JSONL untouched (64-A1 behaviour).
// opts.JSONL == JSONLArchive: mv <claudeSessionsDir>/<uuid>.jsonl into
//   <pyry-data-dir>/archived-sessions/<uuid>.jsonl. Subdir is auto-created.
//   Errors if the destination already exists. Source-absent is a no-op.
// opts.JSONL == JSONLPurge: rm <claudeSessionsDir>/<uuid>.jsonl.
//   Source-absent is a no-op.
//
// Errors mirror 64-A1: ErrSessionNotFound for unknown id,
// ErrCannotRemoveBootstrap for the bootstrap entry. Disposition errors
// are returned verbatim after the registry entry has already been
// persisted (see *Failure ordering* below).
func (p *Pool) Remove(ctx context.Context, id SessionID, opts RemoveOptions) error
```

**Why an enum, not two booleans.** A `bool Archive`/`bool Purge` shape
makes (true, true) a representable-but-illegal state — the design has
to either reject it at runtime or pick one silently. An enum makes the
"exactly one disposition" property type-level: the zero value is well-
defined (Leave), every value is meaningful, and the switch in
`disposeJSONLLocked` is exhaustive. The cost is one new exported type;
the savings is the entire (true, true) error-handling branch.

**Why a struct (`RemoveOptions{JSONL: …}`), not a positional
`JSONLPolicy` parameter.** The positional shape works today
(`Remove(ctx, id, JSONLArchive)`) but every later option needs a new
positional parameter or a new method overload. An options struct keeps
future fields (`Force`, `Reason`, etc.) additive at zero call-site
churn. Same precedent as `RemoveOptions` in stdlib `os` and the
`Pool.Activate` evolution roadmap.

**No new sentinels.** Disposition errors are wrapped paths (e.g.
`"sessions: archive destination exists: <path>"` and `os.Rename` /
`os.Remove` errors propagated). Callers that need to distinguish
"destination exists" from other archive failures can compare via
`errors.Is(err, fs.ErrExist)` if we use that explicitly — see
implementation note below. No exported sentinel until a consumer
needs one.

### Sequence

```
Pool.Remove(ctx, id, opts)
  ├─ p.mu.Lock()
  │   sess, ok := p.sessions[id]
  │   ! ok                → unlock; ErrSessionNotFound  (state byte-identical)
  │   sess.bootstrap      → unlock; ErrCannotRemoveBootstrap
  │   delete(p.sessions, id)
  │   if err := p.saveLocked(); err != nil:
  │       p.sessions[id] = sess          // rollback in-memory
  │       unlock; return err              // disk + memory consistent
  │   disposeErr := p.disposeJSONLLocked(id, opts.JSONL)
  │ p.mu.Unlock()
  │
  │   evictErr := sess.Evict(ctx)         // ALWAYS — child must die
  │
  └─ return first non-nil of (disposeErr, evictErr)
```

The single-mutex critical section covers: lookup → pre-checks →
in-memory delete → registry persist → JSONL disposition. `Session.Evict`
runs *outside* `Pool.mu` (same reason as #94 — the lifecycle goroutine's
`transitionTo → Pool.persist` re-acquires `Pool.mu` and would deadlock).

### Why JSONL disposition under `Pool.mu` (rather than after Evict)

The AC says "JSONL disposition runs after the registry-remove + persist
completes successfully and is performed under Pool.mu along with the
rest of the read-modify-write." Three motivations make this the right
shape (over the alternative "release lock, Evict, then dispose"):

1. **Single critical section, single observable transition.** A
   concurrent `Pool.List` either sees the session present (registry +
   JSONL both untouched) or absent (registry + JSONL both at their
   final state). No "registry says gone, JSONL still live" or "JSONL
   archived but registry still claims session lives" intermediate.

2. **No re-locking gymnastics for the disposition error path.** The
   AC says "if disposition fails, the registry entry stays removed
   (already persisted) and the disposition error is returned." Doing
   disposition under the same `Pool.mu` window as `saveLocked` keeps
   that semantic in one place: `saveLocked` succeeded → registry is
   committed → disposition runs → its error (if any) propagates. No
   "partial rollback" complexity.

3. **Time under lock is bounded.** The disposition is one stat + one
   rename or one unlink — single-syscall granularity, microseconds on
   a healthy filesystem. `saveLocked` already does much heavier I/O
   (encode + temp + fsync + rename) under `Pool.mu`; adding one more
   small syscall sits inside the established envelope.

The trade-off: the syscall happens while claude may still have the
JSONL fd open. POSIX inode semantics make this safe — `os.Rename`
preserves the fd's binding to the inode (the dirent moves, the data
stays), and `os.Remove` unlinks but lets the fd's writes drain into
the soon-to-be-orphaned inode (claude exits seconds later via Evict;
its post-rename writes are captured into the archived file, its post-
unlink writes are discarded along with the now-unreferenced inode).
This is consistent with how operators have always treated `mv`/`rm`
of an active log file.

### `disposeJSONLLocked` — disposition policy

```go
// disposeJSONLLocked applies opts.JSONL to the named session's JSONL.
// Caller MUST hold p.mu (write). Returns nil for JSONLLeave or when the
// pool's claudeSessionsDir is empty (test/disabled mode).
func (p *Pool) disposeJSONLLocked(id SessionID, policy JSONLPolicy) error
```

Logic:

```
JSONLLeave         → return nil
claudeSessionsDir == "" → return nil  (no JSONL plumbing; nothing to act on)
JSONLPurge         →
    err := os.Remove(<claudeSessionsDir>/<uuid>.jsonl)
    if errors.Is(err, fs.ErrNotExist) → return nil  (purge intent: ensure gone)
    return err
JSONLArchive       →
    src := <claudeSessionsDir>/<uuid>.jsonl
    if !exists(src) → return nil        (symmetric with purge no-op)
    archiveDir := <pyry-data-dir>/archived-sessions
    dst := archiveDir/<uuid>.jsonl
    if exists(dst) → return fmt.Errorf("sessions: archive destination exists: %s: %w", dst, fs.ErrExist)
    os.MkdirAll(archiveDir, 0o700)
    return os.Rename(src, dst)
default            → return nil  (forward-compat: unknown policy ≡ Leave)
```

**Pyry data-dir resolution.** The ticket forbids a new config knob and
points at the per-instance data-dir already plumbed into the pool. The
data-dir candidate is the **parent of `Pool.registryPath`** —
`~/.pyry/<sanitized-name>/sessions.json` ⇒ `~/.pyry/<sanitized-name>/`
is the per-instance dir pyry already owns and writes into. So:

```go
func (p *Pool) dataDir() string {
    if p.registryPath == "" {
        return ""
    }
    return filepath.Dir(p.registryPath)
}
```

`claudeSessionsDir` (`~/.claude/projects/<encoded-wd>/`) is **not** the
pyry-owned data-dir — it's claude's directory, where claude writes
JSONLs. It's the *source* for archive/purge, not the destination root.

If `dataDir() == ""` (registry persistence disabled — test-only path),
`JSONLArchive` returns an error: `"sessions: archive requires a registry
path"`. Production callers always set `RegistryPath`; this guard exists
so a misconfigured test fails loudly rather than archiving into a
relative path.

**Source-absent semantics.**

| Policy   | JSONL absent at source     | Rationale                            |
|----------|----------------------------|--------------------------------------|
| Leave    | n/a (never reads source)   | —                                    |
| Archive  | success no-op              | Symmetric with Purge; intent is "move it if there is one" |
| Purge    | success no-op (per AC)     | Per AC: "ensure the file is gone"    |

**Destination-exists semantics.** Per AC: archive **errors** if
destination already exists. Re-archiving the same UUID is almost
always a bug (the pool can't re-mint the same UUID; `Pool.RegisterAllocatedUUID`
+ rotation watcher prevent this on the live path; the only way the
dest exists is a prior archive of the same UUID, then the registry
entry was somehow re-created — operator-driven misuse). Silent
overwrite would lose transcript history. The error wraps `fs.ErrExist`
so a future CLI can offer a `--force` UX with `errors.Is(err, fs.ErrExist)`.

**Why `os.Rename` (not copy + unlink).** The single-fs assumption holds
because both the source (`~/.claude/projects/...`) and destination
(`~/.pyry/<name>/archived-sessions/...`) live under `$HOME` on the
operator's machine — same filesystem in every operationally normal
deployment. If a future operator splits `$HOME` across mounts, the
rename returns `EXDEV` (`os.LinkError` / `errors.Is(err, syscall.EXDEV)`),
which surfaces as a clear error rather than silent corruption. We do
NOT add a copy-then-delete fallback today: no observed failure, and the
fallback's error semantics (partial copy on a write failure → decision
on whether to roll back, atomicity loss) belong in a follow-up only if
the EXDEV case ever lands.

**Why stat-then-rename is not racy here.** Disposition runs under
`Pool.mu` (write). Same-pyry-process concurrent `Pool.Remove(uuid)` for
the same uuid is impossible — the first call removes the entry from
`p.sessions` under the lock, so the second call hits `! ok`. The only
remaining concurrent writer to `<archiveDir>/<uuid>.jsonl` would be a
*different pyry instance* sharing the data-dir, which violates the
per-instance-data-dir invariant. Stat-then-rename is sufficient.

### Failure ordering

`saveLocked` failure ⇒ rollback in-memory delete, return error
verbatim, JSONL untouched, child still alive.

`disposeJSONLLocked` failure ⇒ registry already committed (entry is
gone on disk and in memory), JSONL in whatever state the failure left
it (rename pre-failure ≡ source still present; rename post-failure
≡ destination present, source gone — but `os.Rename` is atomic on
POSIX, so this halfway state is unreachable; for purge, `os.Remove`
either unlinked or didn't). Lock is released. **`Session.Evict` is
still called** — the registry says the session is gone, the child must
follow. The disposition error is returned to the caller; if Evict
also fails (e.g. ctx cancelled), disposition error wins (it's the new
failure mode this ticket introduces, and the more actionable one for
the caller — Evict-cancellation is already a known shape from #94).

```
disposeErr != nil && evictErr == nil → return disposeErr
disposeErr != nil && evictErr != nil → return disposeErr
disposeErr == nil && evictErr != nil → return evictErr  (mirrors #94)
disposeErr == nil && evictErr == nil → return nil
```

### What does NOT change

- `Session` struct: no new fields.
- `Session.Evict`, `Session.Run`, lifecycle state machine: untouched.
- `Pool.Run`, `Pool.supervise`, `Pool.persist`, `Pool.saveLocked`,
  `saveRegistryLocked`: untouched.
- `Pool.mu` lock-order graph: unchanged. `Pool.capMu → Pool.mu →
  Session.lcMu` still holds. `disposeJSONLLocked` takes no other lock.
- `claudeSessionsDir` / `registryPath` Config plumbing: reused, no new
  Config fields.
- Wire protocol, control plane, `cmd/pyry`: untouched.
- Registry JSON schema: untouched.

### Edge cases & error semantics

- `id == ""` — falls into `! ok` branch; `ErrSessionNotFound` (same as
  #94, destructive operations require an explicit id).
- `opts.JSONL` is an unknown future value — treated as `JSONLLeave`
  (forward-compat with old binaries reading new options).
- Bootstrap rejection runs **before** disposition — even
  `Remove(bootstrapID, {JSONL: JSONLPurge})` does not touch the JSONL.
  Reason: bootstrap rejection is a structural invariant; an operator
  passing destructive opts shouldn't get a free side-effect on the
  bootstrap's transcript.
- Unknown id rejection runs **before** disposition — same reasoning;
  no JSONL is acted on for an unknown id. AC: "in-memory + on-disk
  state byte-identical to before" is preserved.
- ctx cancelled during Evict — registry is already updated, JSONL is
  already in its target state. Same shape as #94's late-cancel case.
- `claudeSessionsDir == ""` (test/disabled) — disposition is a no-op
  for all policies. No error. Test code that doesn't care about JSONL
  doesn't have to set up a directory.

### Concurrency

Lock order unchanged: `Pool.capMu → Pool.mu → Session.lcMu`.
`disposeJSONLLocked` takes no additional lock. The held `Pool.mu`
window grows by one syscall (rename or unlink), well under the existing
`saveLocked` envelope.

| Concurrent caller | Outcome |
|---|---|
| `Pool.List` / `Snapshot` during disposition | Blocks on `Pool.mu` RLock briefly; sees the session as fully removed once unblocked |
| `Pool.Remove(sameID, anyOpts)` racing | One wins the `Pool.mu` deletion; the other gets `ErrSessionNotFound` and does no disposition |
| `Pool.Activate(id)` racing | Same as #94 — blocks on `Pool.Lookup`'s RLock; once the delete commits, gets `ErrSessionNotFound` |
| Lifecycle goroutine `transitionTo → persist` | Waits on `Pool.mu`; rewrites registry without the removed session (harmless, idempotent) |

### Files touched

- `internal/sessions/pool.go`
  - `+` `JSONLPolicy` type + constants (~12 lines incl. doc)
  - `+` `RemoveOptions` struct (~6 lines incl. doc)
  - `~` `Pool.Remove` signature: add `opts RemoveOptions` parameter,
    call `disposeJSONLLocked` after `saveLocked` (net ~+8 lines)
  - `+` `Pool.disposeJSONLLocked` method (~30 lines)
  - `+` `Pool.dataDir` helper (~5 lines)
  - `~` `Pool.Remove` doc comment expanded (~+10 lines)
- `internal/sessions/pool_remove_test.go`
  - `~` Five existing tests updated to pass `RemoveOptions{}` (zero
    value → identical semantics to #94)
  - `+` Four new tests covering archive/purge end-to-end (see Testing
    strategy)

Total production code delta: ~70-80 lines. Test code delta: ~150 lines.

No changes to `Session`, `cmd/pyry`, `internal/control`, the registry
schema, the wire protocol, or any other package.

## Testing strategy

Tests follow the established `pool_remove_test.go` shape. Helpers
`helperPoolCreate`, `helperPoolPersistent`, `runPoolInBackground`,
`pollUntil` are reused.

### Update existing tests (mechanical)

Five existing tests pass `RemoveOptions{}` (zero value) at the call
site. The zero value is `JSONLLeave` so semantics are identical to #94.
The existing JSONL-byte-identity assertions in `HappyPath`,
`Bootstrap_Rejected`, and `UnknownID` remain valid (and now also serve
as regression checks for the leave path).

### New tests

1. **`TestPool_Remove_Archive_HappyPath`** — `helperPoolCreate(t,
   regPath, 0)` → `runPoolInBackground` → `Create` a non-bootstrap
   session → wait for active+spawned → write a marker into
   `<claudeSessionsDir>/<uuid>.jsonl` (e.g. `[]byte("marker-"+uuid)`)
   → `pool.Remove(ctx, id, RemoveOptions{JSONL: JSONLArchive})`. Assert:
   - Returns nil.
   - `<claudeSessionsDir>/<uuid>.jsonl` no longer exists.
   - `<dataDir>/archived-sessions/<uuid>.jsonl` exists and its bytes
     equal the marker (proves it's the same file the live path had).
   - Registry on disk: bootstrap only; child PID gone.

2. **`TestPool_Remove_Archive_AutoCreatesSubdir`** — same setup as #1,
   but `os.RemoveAll(<dataDir>/archived-sessions)` after `Create` (it
   may exist from a prior run only theoretically; explicit absence is
   the assertion). After `Remove(…, JSONLArchive)`:
   - Returns nil.
   - `<dataDir>/archived-sessions` exists as a directory.
   - File at `<dataDir>/archived-sessions/<uuid>.jsonl` is the marker.

3. **`TestPool_Remove_Archive_DestExists`** — pre-create
   `<dataDir>/archived-sessions/<uuid>.jsonl` containing `[]byte("old")`
   *before* calling `Remove`. Write `[]byte("new")` to the live JSONL.
   Call `pool.Remove(ctx, id, RemoveOptions{JSONL: JSONLArchive})`.
   Assert:
   - Returns a non-nil error matching `errors.Is(err, fs.ErrExist)`.
   - Live JSONL at `<claudeSessionsDir>/<uuid>.jsonl` still contains
     `"new"` (untouched).
   - Archive file unchanged: still `"old"`.
   - **Registry entry is gone** (this is the AC: "registry entry stays
     removed; disposition error returned"). Re-load `sessions.json`
     and assert the uuid is absent from `Sessions`. `pool.Lookup(id)`
     returns `ErrSessionNotFound`.
   - Child has been terminated (Evict still ran).

4. **`TestPool_Remove_Purge_HappyPath`** — `Create` → spawn → write
   marker JSONL → `pool.Remove(ctx, id, RemoveOptions{JSONL: JSONLPurge})`.
   Assert:
   - Returns nil.
   - `<claudeSessionsDir>/<uuid>.jsonl` no longer exists.
   - `<dataDir>/archived-sessions` is NOT created (purge does not
     touch the archive subdir).
   - Registry on disk: bootstrap only; child PID gone.

5. **`TestPool_Remove_Purge_AbsentNoop`** — `Create` → spawn → do NOT
   write a JSONL on disk → `pool.Remove(ctx, id, RemoveOptions{JSONL:
   JSONLPurge})`. Assert:
   - Returns nil (per AC: "if the JSONL does not exist on disk, treat
     as success").
   - Registry on disk: bootstrap only; child PID gone.

### Coverage check

| AC test requirement | Test |
|---|---|
| Each policy (leave / archive / purge) end-to-end | Updated `HappyPath` (leave, zero value) + `Archive_HappyPath` (#1) + `Purge_HappyPath` (#4) |
| Archive subdir auto-creation | `Archive_AutoCreatesSubdir` (#2) |
| Archive errors when destination exists | `Archive_DestExists` (#3) |
| Purge no-op when JSONL absent | `Purge_AbsentNoop` (#5) |
| All existing #94 ACs (bootstrap rejection, unknown id, race-clean, uncooperative child) | Existing tests updated to pass `RemoveOptions{}`; assertions unchanged |

## Quality gates

- `go test -race ./...` clean.
- `go vet ./...` clean.
- `staticcheck ./...` clean.
- No new dependencies.
- `qmd update && qmd embed` after the knowledge doc note lands.

## Knowledge capture (during implementation)

Append to `docs/knowledge/features/sessions-package.md` § Pool.Remove
(landed in #94) a short note describing the 1.1d-A2 evolution:
signature change to `(ctx, id, opts)`, the three policies and their
zero-value behaviour, the data-dir resolution rule (parent of
`registryPath`), and the failure-ordering rule (registry committed
before disposition; disposition error trumps Evict error). No new ADR
— this design follows the established mutator-with-options pattern.

The Errors table in the same doc gains entries for:
- `Pool.Remove` archive destination exists → wrapped error matching
  `fs.ErrExist`; registry already removed.
- `Pool.Remove` archive when registry persistence disabled → error;
  registry already removed.

`PROJECT-MEMORY.md` gets a one-paragraph entry under "Codebase (Phase
1.1d-A2, ticket #95)" summarising the surface and the under-`Pool.mu`
disposition discipline.

## Open questions

None — design is fully specified. The two architect-discretion items
called out by the AC (the field-shape choice and the partial-failure
ordering) are resolved above with rationale.

## Out of scope (reaffirmed)

- Process termination, registry remove, bootstrap rejection,
  unknown-uuid error, locking semantics — all #94.
- Control verb / CLI surface for `pyry sessions rm --archive` /
  `--purge` — #65.
- Bulk remove / TTL-based remove.
- `--force` overwrite for archive — defer; the `fs.ErrExist`-wrapping
  error makes adding it later a one-line `errors.Is` check at the CLI
  layer.
- Cross-filesystem archive (copy + delete fallback) — defer until
  EXDEV is observed; the current `os.Rename` failure surfaces clearly.
- Per-session terminate signal to eliminate the orphan goroutine —
  defer until evidence (same posture as #94).
