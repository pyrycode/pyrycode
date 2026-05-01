# Architecture spec — #34 Phase 1.2a: `sessions.json` registry — sessions survive pyry restart

**Ticket:** [#34](https://github.com/pyrycode/pyrycode/issues/34)
**Tracking parent:** [#20](https://github.com/pyrycode/pyrycode/issues/20) (Phase 1)
**Locked design source:** [`docs/multi-session.md`](../../multi-session.md) — Phase 1.2 / "Locked decisions"
**Status:** Draft for development
**Size:** **S** (~120 lines production, ~150 lines tests)

## Context

Today `internal/sessions.Pool` is in-memory only. Every pyry restart calls `NewID()` to mint a fresh bootstrap UUID — the on-disk JSONL that claude resumed via `--continue` survives, but pyry's own session identity does not. Phase 1.1 (`pyry sessions new/list/rm/rename`) needs stable identity to refer to; Phase 1.2c (idle eviction) needs persistent metadata to drive its policy. 1.2a is the persistence foundation that lets both ship.

The locked design fixes the path (`~/.pyry/<pyry-name>/sessions.json`) and the identity model (UUIDv4, per-pyry-name namespace). This spec defines the JSON schema, the atomic-write mechanism, the load path on `Pool.New`, and the seam Phase 1.1's `Pool.Add`/`Rename`/`Remove` will plug into.

Phase 1.2a's only state-changing operation against the current Pool API is **bootstrap creation in `Pool.New`** — there is no `Add` / `Rename` / `Remove` yet. The persistence machinery is exposed package-internally so 1.1's mutating operations call `saveLocked` before returning success, with no further redesign.

This ticket explicitly does NOT include: `/clear` rotation detection (1.2b), idle eviction / lazy respawn (1.2c), the `pyry sessions ...` user-facing CLI (1.1), or `claude --session-id <uuid>` plumbing (1.1+).

## Design

### Package layout

One new file in the existing `internal/sessions` package:

```
internal/sessions/
  id.go         (unchanged)
  session.go    (new fields: label, createdAt, lastActiveAt, bootstrap — package-private)
  pool.go       (New gains load+save; new private registryPath field)
  registry.go   (new — schema, atomicWrite, loadRegistry, saveRegistryLocked)
```

No new package. No new public types except a `RegistryPath string` field on the existing `sessions.Config`. The wire protocol, `internal/control`, and `internal/supervisor` are byte-identical post-merge.

### Registry path

Resolution lives in `cmd/pyry/main.go` next to `resolveSocketPath` — same shape, same fallback ladder:

```go
// resolveRegistryPath returns ~/.pyry/<sanitized-name>/sessions.json. Falls
// back to a CWD-relative path if $HOME can't be resolved (matches
// resolveSocketPath's contract).
func resolveRegistryPath(name string) string {
    home, err := os.UserHomeDir()
    if err != nil || home == "" {
        return filepath.Join(sanitizeName(name), "sessions.json")
    }
    return filepath.Join(home, ".pyry", sanitizeName(name), "sessions.json")
}
```

`sanitizeName` already exists; reuse it. The path is passed to `sessions.New` via the new `Config.RegistryPath` field. Empty `RegistryPath` disables persistence — used by unit tests; never used in production.

The directory `~/.pyry/<name>/` is created on first save (`os.MkdirAll(dir, 0700)`). Permissions: directory `0700`, file `0600`. Same convention as the existing socket and the user's home claude state.

Note: this introduces a new directory under `~/.pyry/` for each pyry name. The existing socket file at `~/.pyry/<name>.sock` is unaffected — they live as siblings (`~/.pyry/pyry.sock` and `~/.pyry/pyry/sessions.json`).

### JSON schema

```json
{
  "version": 1,
  "sessions": [
    {
      "id": "8a4cf9b2-7e5d-4d3a-9fb2-12c4f8a1de91",
      "label": "",
      "created_at": "2026-05-01T12:34:56.789Z",
      "last_active_at": "2026-05-01T12:34:56.789Z",
      "bootstrap": true
    }
  ]
}
```

Top-level `version` is a forward-marker. 1.2a writes `1` and accepts any value on read (no branching yet — the field's job is to give a future schema-break a concrete handle). Unknown top-level fields and unknown per-session fields are tolerated on read (default `encoding/json` decoder behaviour — do **not** call `DisallowUnknownFields`). New fields land additively in later phases (`claude_session_id`, idle policy, etc.) without breaking older pyry binaries.

Per-session fields:

| Field | Type | Phase 1.2a semantics |
|---|---|---|
| `id` | string (36-char UUIDv4) | Required. The `SessionID`. |
| `label` | string | Always `""` in 1.2a (no setter yet). 1.1's `pyry sessions rename` populates it. |
| `created_at` | RFC3339Nano | Set once at session creation; never updated thereafter. |
| `last_active_at` | RFC3339Nano | Set to `created_at` on first write. 1.2c's idle eviction starts updating it. |
| `bootstrap` | bool | `true` on the bootstrap entry; absent or `false` for non-bootstrap entries. 1.1+ may demote / promote; 1.2a always writes one entry with `bootstrap: true`. |

Why explicitly mark bootstrap rather than rely on "first entry wins": the bootstrap concept survives Phase 1.1 — `Lookup("")` keeps resolving to a designated entry. Marking it on disk decouples the wire from "first in array", which would otherwise become a load-bearing ordering invariant for the file. With the marker, 1.1's `pyry sessions rm <bootstrap-uuid>` has a clean question to answer (refuse, or pick a new bootstrap) instead of relying on file ordering.

### Go types

In `registry.go`:

```go
package sessions

import (
    "encoding/json"
    "errors"
    "fmt"
    "io/fs"
    "os"
    "path/filepath"
    "time"
)

// registryFile is the on-disk schema for ~/.pyry/<name>/sessions.json.
// Encoder/decoder is encoding/json with default lenient field handling — new
// per-session fields can be added in later phases without breaking old pyry.
type registryFile struct {
    Version  int             `json:"version"`
    Sessions []registryEntry `json:"sessions"`
}

type registryEntry struct {
    ID           SessionID `json:"id"`
    Label        string    `json:"label"`
    CreatedAt    time.Time `json:"created_at"`
    LastActiveAt time.Time `json:"last_active_at"`
    Bootstrap    bool      `json:"bootstrap,omitempty"`
}

// loadRegistry reads sessions.json from path. Returns (nil, nil) when the file
// is missing — this is the cold-start signal that triggers fresh bootstrap
// generation. A malformed file is a hard error (operator must fix or remove).
func loadRegistry(path string) (*registryFile, error)

// saveRegistryLocked writes the registry atomically: temp file in the same
// directory, fsync, rename into place. Caller MUST hold Pool.mu (write).
// MkdirAll on the parent directory is performed inside this function.
func saveRegistryLocked(path string, reg *registryFile) error
```

Atomic write body:

```go
func saveRegistryLocked(path string, reg *registryFile) error {
    dir := filepath.Dir(path)
    if err := os.MkdirAll(dir, 0o700); err != nil {
        return fmt.Errorf("registry: mkdir %s: %w", dir, err)
    }
    f, err := os.CreateTemp(dir, ".sessions-*.json.tmp")
    if err != nil {
        return fmt.Errorf("registry: create temp: %w", err)
    }
    tmp := f.Name()
    // Best-effort cleanup if rename never happens.
    defer func() { _ = os.Remove(tmp) }()
    if err := os.Chmod(tmp, 0o600); err != nil {
        _ = f.Close()
        return fmt.Errorf("registry: chmod temp: %w", err)
    }
    enc := json.NewEncoder(f)
    enc.SetIndent("", "  ")
    if err := enc.Encode(reg); err != nil {
        _ = f.Close()
        return fmt.Errorf("registry: encode: %w", err)
    }
    if err := f.Sync(); err != nil {
        _ = f.Close()
        return fmt.Errorf("registry: fsync: %w", err)
    }
    if err := f.Close(); err != nil {
        return fmt.Errorf("registry: close temp: %w", err)
    }
    if err := os.Rename(tmp, path); err != nil {
        return fmt.Errorf("registry: rename: %w", err)
    }
    return nil
}
```

`os.Rename` on the same filesystem is atomic on Linux and macOS — this is what guarantees "SIGKILL during a write leaves the on-disk file as either the pre-update or post-update version, never partial JSON." The `defer os.Remove(tmp)` is defensive: if anything between `CreateTemp` and `Rename` fails, the orphan tmp goes away. Successful `Rename` makes `os.Remove(tmp)` a no-op (file no longer at `tmp`) — harmless.

`fsync` before rename ensures the file's contents are durable before the directory entry flips. We do **not** also fsync the directory — Linux ext4 / macOS APFS rename is durable enough for this use case (pyry's registry is operator-recoverable; we are not a database). Open question 1 below revisits this if it surfaces as a problem.

Load body:

```go
func loadRegistry(path string) (*registryFile, error) {
    data, err := os.ReadFile(path)
    if err != nil {
        if errors.Is(err, fs.ErrNotExist) {
            return nil, nil // cold-start signal
        }
        return nil, fmt.Errorf("registry: read %s: %w", path, err)
    }
    if len(data) == 0 {
        return nil, nil // treat empty file as cold-start
    }
    var reg registryFile
    if err := json.Unmarshal(data, &reg); err != nil {
        return nil, fmt.Errorf("registry: parse %s: %w", path, err)
    }
    return &reg, nil
}
```

Empty file → cold-start. Missing file → cold-start. Malformed JSON → hard error (operator fixes or removes). The atomic-write guarantee makes malformed JSON unreachable except via external corruption / tampering — failing loud is the right call.

### `Session` struct additions

`session.go` gains four package-private fields, all set under `Pool.mu` (write) and never mutated outside the `sessions` package:

```go
type Session struct {
    id     SessionID
    sup    *supervisor.Supervisor
    bridge *supervisor.Bridge
    log    *slog.Logger

    // Persisted metadata. Read by saveRegistryLocked. Written by Pool.New
    // (and by Phase 1.1's Pool.Add / Pool.Rename / Pool.Remove). Mutations
    // happen with Pool.mu held (write).
    label        string
    createdAt    time.Time
    lastActiveAt time.Time
    bootstrap    bool
}
```

No public accessors in 1.2a. 1.1 will add `Label()`, `CreatedAt()`, `LastActiveAt()` for `pyry sessions list`. Adding them now would be unused surface; deferring keeps the package minimal per the project's "don't add features beyond what the task requires" rule.

### `Pool.New` — load + save sequence

`Config` gains one field:

```go
type Config struct {
    Bootstrap   SessionConfig
    Logger      *slog.Logger
    RegistryPath string // empty → persistence disabled (tests only)
}
```

`Pool` gains a private `registryPath` field. The new `New` body:

```go
func New(cfg Config) (*Pool, error) {
    if cfg.Logger == nil {
        cfg.Logger = slog.Default()
    }

    var bootstrapID SessionID
    var createdAt, lastActiveAt time.Time
    var label string

    // 1. Try to load existing registry.
    var reg *registryFile
    if cfg.RegistryPath != "" {
        var err error
        reg, err = loadRegistry(cfg.RegistryPath)
        if err != nil {
            return nil, fmt.Errorf("sessions: load registry: %w", err)
        }
    }

    // 2. Pick the bootstrap identity. If the registry has a bootstrap entry,
    //    reuse its id, label, timestamps. Otherwise mint fresh values.
    if entry := pickBootstrap(reg); entry != nil {
        bootstrapID = entry.ID
        label = entry.Label
        createdAt = entry.CreatedAt
        lastActiveAt = entry.LastActiveAt
    } else {
        id, err := NewID()
        if err != nil {
            return nil, fmt.Errorf("sessions: generate bootstrap id: %w", err)
        }
        bootstrapID = id
        now := time.Now().UTC()
        createdAt, lastActiveAt = now, now
    }

    // 3. Construct the supervisor (always from cfg, never from registry —
    //    invocation config tracks the live CLI flags, not stored state).
    supCfg := supervisor.Config{ /* ... same translation as today ... */ }
    sup, err := supervisor.New(supCfg)
    if err != nil {
        return nil, fmt.Errorf("sessions: bootstrap supervisor: %w", err)
    }

    sess := &Session{
        id:           bootstrapID,
        sup:          sup,
        bridge:       cfg.Bootstrap.Bridge,
        log:          cfg.Logger,
        label:        label,
        createdAt:    createdAt,
        lastActiveAt: lastActiveAt,
        bootstrap:    true,
    }
    p := &Pool{
        sessions:     map[SessionID]*Session{bootstrapID: sess},
        bootstrap:    bootstrapID,
        log:          cfg.Logger,
        registryPath: cfg.RegistryPath,
    }

    // 4. Persist if we minted anything new (no registry file) — the AC says
    //    "writes the registry on every state-changing operation", and bootstrap
    //    creation IS the state-changing op in 1.2a.
    if cfg.RegistryPath != "" && reg == nil {
        if err := p.saveLocked(); err != nil {
            return nil, fmt.Errorf("sessions: save registry: %w", err)
        }
    }
    return p, nil
}
```

`pickBootstrap` is a small private helper:

```go
// pickBootstrap returns the entry marked bootstrap=true, or nil if none.
// Tolerates a registry that contains entries we haven't materialized in 1.2a
// (e.g. a 1.1-written file with multiple sessions): we still find and use the
// bootstrap entry. Non-bootstrap entries are silently ignored in 1.2a — they
// are not deleted from the file (saveLocked only rewrites what the in-memory
// pool knows about, but in 1.2a saveLocked is only called when we minted a
// fresh bootstrap, which means there was no prior file).
func pickBootstrap(reg *registryFile) *registryEntry {
    if reg == nil {
        return nil
    }
    for i := range reg.Sessions {
        if reg.Sessions[i].Bootstrap {
            return &reg.Sessions[i]
        }
    }
    return nil
}
```

The "do not silently delete unmaterialized entries" property holds because in 1.2a, `saveLocked` is only invoked at fresh-cold-start (no file existed). Once 1.1's `Pool.Add` lands, the in-memory pool grows to mirror the registry, and `saveLocked` rewrites the full set.

### `saveLocked` — registry write under the lock

```go
// saveLocked snapshots the current in-memory sessions into a registryFile and
// writes it atomically. Caller MUST hold p.mu (write). No-op if registryPath
// is empty.
func (p *Pool) saveLocked() error {
    if p.registryPath == "" {
        return nil
    }
    reg := &registryFile{
        Version:  1,
        Sessions: make([]registryEntry, 0, len(p.sessions)),
    }
    for _, s := range p.sessions {
        reg.Sessions = append(reg.Sessions, registryEntry{
            ID:           s.id,
            Label:        s.label,
            CreatedAt:    s.createdAt,
            LastActiveAt: s.lastActiveAt,
            Bootstrap:    s.bootstrap,
        })
    }
    sortEntriesByCreatedAt(reg.Sessions) // stable disk ordering for idempotent reload
    return saveRegistryLocked(p.registryPath, reg)
}
```

Sorting by `CreatedAt` (then by `ID` as a tiebreaker) ensures the file's byte content is a function of the in-memory set, not Go's randomized map iteration order. This is what makes the AC's "loading the same registry file twice produces the same in-memory state" hold across the load → save → load round-trip Phase 1.1 will exercise.

For 1.2a there is exactly one entry, so the sort is degenerate; the cost is paying for a future correctness property up front, which fits the "introduce-then-rewire" pattern this project already uses.

`Pool.New` is the only caller in 1.2a. It runs before any other goroutine touches `p` (the pool isn't returned to the caller yet), so the "lock held" precondition is trivially satisfied without acquiring the lock — but the contract is documented for 1.1's `Pool.Add` callers.

### Concurrency model

No new goroutines. `Pool.mu` (the existing `sync.RWMutex`) is the only synchronization primitive.

Lock disciplines:

| Operation | Lock | Holds lock during disk I/O? |
|---|---|---|
| `Pool.Lookup`, `Pool.Default`, `Pool.Run` | `mu.RLock` | No. (No I/O.) |
| `Pool.New` (1.2a entry path) | none — pool not yet shared | Yes (single-threaded; the `saveLocked` runs before the Pool is returned) |
| `Pool.saveLocked` (called from 1.1's mutating ops) | `mu.Lock` (caller's responsibility) | **Yes** — disk I/O happens with the write lock held |

Holding `mu` across `os.Rename` is unusual but accepted here: registry mutations are infrequent (session create/rename/rm — not per-message activity), the file is tiny (kilobytes), and the property we buy is that other goroutines reading the in-memory map see a consistent view that matches the file on disk. Phase 1.2c's idle-eviction `last_active_at` updates may want a different cadence (per-message touches would be too lock-hot) — that is 1.2c's design problem, not 1.2a's.

Single-writer per file: the per-pyry-name namespace means one pyry process owns one `sessions.json`. Two pyry instances with the same `-pyry-name` already conflict over `~/.pyry/<name>.sock`; the registry inherits that exclusion. No file locks (`flock`/`fcntl`) needed.

### Error handling

| Condition | Surface |
|---|---|
| `loadRegistry`: file missing | `(nil, nil)` — cold-start signal, not an error. |
| `loadRegistry`: empty file | `(nil, nil)` — cold-start, idempotent with missing. |
| `loadRegistry`: malformed JSON | `error` — wrapped; propagated by `Pool.New`. Operator must fix or `rm` the file. |
| `loadRegistry`: read I/O error (perms, EIO) | `error` — wrapped; fatal at startup. |
| `saveRegistryLocked`: mkdir / create / encode / sync / close / rename failure | `error` — wrapped at each step; propagated by `Pool.New`. **No partial state**: pre-existing file (if any) is untouched because `os.Rename` is the commit point. |
| `Pool.New`: fresh-bootstrap save fails | `error` — Pool is **not** returned. Supervisor is GC'd unspawned. Same fail-fast shape as `supervisor.New` failure. |
| Forward-compat decode (unknown fields) | Silently ignored (default `encoding/json` behaviour). No error. |

No new exported sentinels in 1.2a. Phase 1.1 may add (e.g. `ErrSessionExists` for duplicate `Add`); 1.2a does not need them.

### What does NOT change

- `Pool.Lookup`, `Pool.Default`, `Pool.Run`: byte-identical bodies.
- `Session.ID`, `State`, `Attach`, `Run`: byte-identical bodies.
- `internal/control`, `internal/supervisor`: unchanged.
- Wire protocol, log lines, CLI surface: unchanged.
- `--continue` claude invocation: unchanged. (1.0's session continuity continues working through the registry-recovered restart.)
- Bootstrap UUID is **not logged** (parent #27 spec open question #3 — still "wait").

`cmd/pyry/main.go`'s `runSupervisor` gains exactly two lines: compute the registry path, set `Config.RegistryPath`. The existing startup log line (`pyrycode starting`) gets no new fields.

## Testing strategy

Stdlib `testing` only. New test file `internal/sessions/registry_test.go` for the registry-specific unit tests; existing `pool_test.go` gets one new test covering the round-trip path through `Pool.New`. Tests use `t.TempDir()` for the registry path — no real `~/.pyry` access.

### `internal/sessions/registry_test.go`

| Test | Verifies |
|---|---|
| `TestSaveLoad_RoundTrip` | `saveRegistryLocked` followed by `loadRegistry` returns a `registryFile` whose `Sessions` slice deep-equals (by `id`, `label`, `bootstrap`, RFC3339-truncated timestamps) the input. |
| `TestLoad_MissingFile` | `loadRegistry("nonexistent")` returns `(nil, nil)`. |
| `TestLoad_EmptyFile` | `loadRegistry` against a path containing `""` returns `(nil, nil)`. |
| `TestLoad_MalformedJSON` | `loadRegistry` against `"{not json"` returns a non-nil error wrapping a JSON parse error. |
| `TestLoad_TolerateUnknownFields` | A registry file with an unknown top-level field and an unknown per-session field decodes successfully; known fields round-trip correctly. |
| `TestSave_AtomicRenamePreservesOldFile` | Pre-write a known `sessions.json`. Inject a forced failure between `CreateTemp` and `Rename` (see open question 2 below). Assert the original file's bytes are unchanged. |
| `TestSave_FilePermissions` | After `saveRegistryLocked`, `os.Stat` reports mode `0600` on the file and `0700` on the parent directory. |
| `TestSave_StableOrdering` | Save the same in-memory pool twice (two distinct files). The byte content is identical. (Defends against Go map-iteration randomness.) |

`TestSave_AtomicRenamePreservesOldFile` is the closest unit-level proxy for the SIGKILL-mid-write smoke. It exercises the rename-is-the-commit invariant without needing a real signal. The integration smoke (next subsection) covers the actual SIGKILL path.

### `internal/sessions/pool_test.go` — new tests

| Test | Verifies |
|---|---|
| `TestPool_New_ColdStartCreatesRegistry` | `New(Config{RegistryPath: <tmp>/sessions.json, ...})` against a non-existent path: returns a Pool whose default ID matches the on-disk single entry. The file exists, has mode `0600`, contains exactly one session marked `bootstrap: true`. |
| `TestPool_New_WarmStartReusesUUID` | Save a hand-built registry with a known UUID at `<tmp>/sessions.json`. Call `New` with that path. `pool.Default().ID()` equals the known UUID. The file's byte content is unchanged (no rewrite — the AC says writes only happen on state-changing ops, and warm-start is not one). |
| `TestPool_New_IdempotentReload` | `New` twice in a row with the same `RegistryPath` (closing the first pool between, no in-process state shared): both returned pools have the same default ID. |
| `TestPool_New_PersistenceDisabled_NoFile` | `New(Config{RegistryPath: ""})` does not create any file in the test's TempDir. (Belt-and-suspenders for the test-only "disabled" code path.) |
| `TestPool_New_MalformedRegistryIsFatal` | A pre-written malformed `sessions.json` causes `New` to return an error. (No silent wipe.) |

`TestPool_New_WarmStartReusesUUID`'s "no rewrite on warm start" assertion is the AC's "writes only on state-changing ops" promise made testable. If 1.2c's `last_active_at` updates change this, that ticket carries the test update.

### Integration smoke (manual, documented in spec — not run in CI)

The two smokes the AC calls out are operator-facing manual checks documented in the feature doc Phase 1.2a will add (`docs/knowledge/features/sessions-registry.md`):

1. **Restart preserves UUID.** Start pyry; capture the bootstrap UUID from `~/.pyry/<name>/sessions.json`. `pyry stop`. Restart. Assert the same UUID is still in the file and addressable through the Pool API. (The `TestPool_New_WarmStartReusesUUID` unit test is the automated proxy.)

2. **SIGKILL mid-write is safe.** Start pyry, stop it (registry file written). `kill -9` is not directly testable since 1.2a only writes once at first start — there is no recurring write to interrupt. Phase 1.1's `pyry sessions new` will be the natural mid-write target; until then this AC is satisfied by the rename-atomicity invariant proved at the unit level (`TestSave_AtomicRenamePreservesOldFile`). The `docs/lessons.md` entry for atomic writes notes this caveat so 1.1 picks up the live SIGKILL smoke.

### Race detector + static analysis

`go test -race ./...`, `go vet ./...`, `staticcheck ./...` clean. The `Session` struct's new fields are written by `Pool.New` and read by `saveLocked` — both reachable, both exercised by the new tests. No U1000 risk.

## Acceptance criteria mapping

| AC bullet | Spec artefact |
|---|---|
| Registry created at `~/.pyry/<pyry-name>/sessions.json` on first state change, persists across restart | `resolveRegistryPath` (cmd/pyry) + `Pool.New` step 4 + `saveLocked` |
| `NewPool` reads registry on startup; missing/empty → bootstrap-only Phase-0/1.0 behaviour bit-for-bit | `Pool.New` step 1+2 ("cold-start path" branch); `loadRegistry` empty/missing returns `(nil, nil)` |
| Pool writes registry on every state-changing op (create/rename/remove); write before API success | `Pool.New` step 4 (only state-changing op in 1.2a). 1.1+ Add/Rename/Remove plug into `saveLocked` |
| Atomic write via temp + `os.Rename` | `saveRegistryLocked` body |
| Reload idempotent (same UUIDs, same labels, ordering) | Stable sort in `saveLocked`; `TestPool_New_IdempotentReload`; `TestSave_StableOrdering` |
| Forward-compat: unknown fields tolerated on read | Default `encoding/json` decoder (not `DisallowUnknownFields`); `TestLoad_TolerateUnknownFields` |
| `go test -race ./...`, `go vet`, `staticcheck` clean | "Race detector + static analysis" section |
| Manual smoke: same UUID across restart | `TestPool_New_WarmStartReusesUUID` (automated proxy) + manual procedure in feature doc |
| Manual smoke: SIGKILL mid-write leaves pre- or post-update state | `TestSave_AtomicRenamePreservesOldFile` (unit-level invariant proof); manual SIGKILL natural target lands with 1.1 |

## Open questions (resolve during build)

1. **Skip directory `fsync` after `os.Rename`?** Recommendation: yes (skip). On modern Linux ext4 / macOS APFS the rename's directory entry update is durable enough for an operator-recoverable file. Adding `dir.Sync()` is one extra syscall per write and the failure mode it defends against (power-loss race between rename and dir entry flush) is not what 1.2a's resilience model targets. Revisit if a real corruption is observed.

2. **How to test `TestSave_AtomicRenamePreservesOldFile` without a fault-injection seam in `saveRegistryLocked`?** Two options:
   (a) Keep `saveRegistryLocked` as written; in the test, drop the registryFile through a small wrapper that intercepts the temp file path and corrupts/truncates it before letting the real rename happen — this exercises the partial-write tolerance, not the rename-failure path.
   (b) Write the test against a known-failing rename (e.g. point the temp dir at `/dev/null/foo` so `CreateTemp` fails, or chmod the parent to read-only after temp creation). Asserts the pre-existing target file is untouched.
   Recommendation: option (b). It tests the actual invariant (rename is the commit point) rather than a hypothetical mid-write corruption that the atomic-rename design makes unreachable. Use `t.TempDir()` + `os.Chmod(dir, 0o500)` after pre-writing the original file; the rename will fail; assert the original bytes round-trip unchanged. `defer os.Chmod(dir, 0o700)` so `t.TempDir`'s cleanup works.

3. **Should `bootstrap: true` be a writable property in 1.2a?** No. 1.2a always writes `bootstrap: true` for the lone entry. Phase 1.1's `Pool.Add` writes new entries with `bootstrap: false` (or omits the field, since `omitempty`). Phase 1.1's `Pool.Remove(bootstrap-id)` is the moment the design has to choose: refuse, or promote another entry. That decision is 1.1's, not 1.2a's. The schema accommodates both.

4. **`time.Time` JSON shape — RFC3339 with or without nanoseconds?** Go's default `time.Time.MarshalJSON` is RFC3339Nano. Recommendation: keep the default. It's strictly readable by every JSON time decoder; the trailing nanos are aesthetic noise that operators rarely read. If a future "reproducible test fixture" need surfaces, truncate-to-second in test-only fixtures.

5. **Where does the "feature documentation" land in the 1.2a PR?** Add `docs/knowledge/features/sessions-registry.md` (mirroring the existing `sessions-package.md`) with: schema, atomic-write mechanics, restart smoke procedure, SIGKILL caveat. Update `docs/knowledge/INDEX.md`. Do **not** add an ADR — there's no decision-with-tradeoffs to record beyond what `docs/multi-session.md` already locks.

## Why M, not split

N/A — sized at S. Production scope is bounded: one new file (~80 lines), an additive `Config` field, ~25 lines of new logic in `Pool.New`, and 15-line `resolveRegistryPath` in `cmd/pyry/main.go`. Edit fan-out is two files (`pool.go`, `cmd/pyry/main.go`) plus three test files; no consumer cascade. The only abstract risk is the developer over-investing in 1.1's `Pool.Add` shape — the spec's "deferred to Phase 1.1" notes are explicit guards against that.

## Out of scope (deferred)

- `Pool.Add(SessionConfig) (*Session, error)`, `Rename`, `Remove` → Phase 1.1.
- `pyry sessions new/list/rm/rename` CLI verbs → Phase 1.1.
- `claude --session-id <uuid>` invocation, claude-side session id field in registry → Phase 1.1+.
- `last_active_at` live updates (idle eviction, lazy respawn) → Phase 1.2c.
- `/clear` rotation detection / registry desync recovery → Phase 1.2b.
- File locking (`flock`/`fcntl`) for cross-process exclusion → not needed (per-pyry-name single-writer invariant).
- Registry schema version branching → no `version`-driven branching in 1.2a; the field is a future hook.
- Bootstrap UUID logging at startup → still deferred (open question #3 in #27 parent spec).

## Implementation order (suggested)

1. `registry.go`: types, `loadRegistry`, `saveRegistryLocked`, atomic-write tests (`registry_test.go`).
2. `session.go`: add `label`, `createdAt`, `lastActiveAt`, `bootstrap` fields. No code reads them yet — this commit compiles and existing tests still pass.
3. `pool.go`: add `Config.RegistryPath`, `Pool.registryPath`, `Pool.saveLocked`, rewrite `New` with the load-or-mint logic. Update existing `pool_test.go` helpers if they fail to compile (they should keep working — `Config.RegistryPath: ""` is the test default).
4. `pool_test.go`: add the five new `TestPool_New_*` tests.
5. `cmd/pyry/main.go`: add `resolveRegistryPath`, plumb into `sessions.Config`.
6. `docs/knowledge/features/sessions-registry.md` + `INDEX.md` update.

Each step is independently compilable and testable; if step 3's load logic regresses an existing test, the diff is contained.
