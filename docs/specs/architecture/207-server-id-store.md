# Spec: server-id persistence + first-run bootstrap (#207)

## Files to read first

- `internal/identity/server_id.go:1-83` — the `ServerID` type, `NewServerID`,
  `ParseServerID`, and `ErrInvalidServerID` from #206. The loader sits on top
  of these; do not modify them.
- `internal/identity/server_id_test.go:1-95` — established test style for the
  package (table-driven, `t.Parallel()`, `errors.Is` for sentinel matching).
  Mirror it.
- `internal/sessions/registry.go:53-92` — the canonical atomic-write recipe in
  this codebase: `MkdirAll(dir, 0o700)` → `CreateTemp(dir, ".prefix-*.tmp")` →
  `Chmod(0o600)` → write → `Sync` → `Close` → `Rename`. The new loader's write
  path is structurally identical, just for a 37-byte payload instead of JSON.
- `internal/sessions/registry.go:30-51` (`loadRegistry`) — the read-side
  pattern: `os.ReadFile`, distinguishing `fs.ErrNotExist` from other I/O
  errors, wrapping with `fmt.Errorf("registry: ...: %w", err)`. The new
  loader's read path follows the same shape.
- `docs/lessons.md` § "Atomic on-disk writes" — the load-bearing invariants
  (`os.Rename` is the commit point, same-directory rename only, `defer
  os.Remove(tmp)` after `CreateTemp`, no parent-dir fsync). All apply here.
- `docs/specs/architecture/206-server-id-type-and-generation.md:34-52` — the
  rationale for placing identity types in their own package and not in
  `internal/sessions`. Same rationale extends to keeping the loader here.
- `CODING-STYLE.md` (root) — error wrapping and naming conventions.

## Context

#206 landed the pure type + generator + parser for `ServerID` in a new
`internal/identity` package whose doc-comment declared "Pure types and
validation; no I/O." This ticket adds the binary-startup bootstrap on top:
*on first run, mint and persist; on subsequent runs, load and validate.*

The server-id is the public routing identifier for one pyrycode-binary
instance (QR pairing, relay handshake's `x-pyrycode-server` header). It must
be stable across binary lifecycles — paired phones bind their device-tokens
to a server-id, and rotating the id silently invalidates every pairing. That
makes corruption recovery a one-way decision: the right action on a corrupt
file is to surface the error and require operator intervention, never to
silently regenerate.

## Design

### Package placement

Add the loader as `internal/identity/store.go`. **Relax** the package
doc-comment in `server_id.go:1-3` so it no longer claims "no I/O":

```go
// Package identity owns typed identifiers that span subsystems — server-id
// today; potential future device-id, paired-device-id — and the bootstrap
// of those identifiers from disk. The pure types and validation live next
// to the I/O wrapper that mints and persists them on first run.
package identity
```

Rationale for co-locating instead of using a sibling `internal/identitystore`:

- The loader's only job is to produce a `ServerID` — it has no API surface
  beyond the type. Splitting into two packages forces every caller to import
  both (`identity.ServerID` + `identitystore.LoadOrCreate`) for no abstraction
  win.
- Phase 3 will likely add an analogous loader for `~/.pyry/devices.json`
  (paired-device list). Keeping each type's persistence next to its type
  ("`devices.go` + `devices_store.go`") scales linearly; a parallel
  `internal/devicestore` would replicate the package boundary for the same
  reason.
- "No I/O" was a #206-scope claim, not a permanent invariant. The package's
  real contract is "owns identity types"; persisting an identity to disk is a
  natural extension of that contract.

### Public API

```go
// LoadOrCreate returns the ServerID stored at path, generating and persisting
// a fresh one if path does not exist.
//
// The caller is responsible for resolving the absolute path (typically
// ~/.pyry/server-id from config); LoadOrCreate operates on absolute paths so
// tests can use t.TempDir().
//
// On first run (path does not exist), the parent directory is created with
// mode 0700 if missing, a fresh UUIDv4 is minted via NewServerID, written
// atomically (sibling temp file + rename) with mode 0600 and a single
// trailing newline, and returned.
//
// On subsequent runs (path exists), the file is read and validated via
// ParseServerID. A single trailing newline is tolerated; any other deviation
// from canonical UUIDv4 form returns an error matching ErrInvalidServerID
// via errors.Is. The file is NEVER overwritten on the existing-file path,
// even on validation failure — paired devices bind their tokens to a
// specific server-id, and silently regenerating would invalidate every
// pairing without operator awareness.
//
// LoadOrCreate is not safe for concurrent use against the same path;
// bootstrap runs once on daemon startup before any goroutines fan out.
// Two pyry processes sharing a HOME directory is a misconfiguration
// outside this loader's contract.
func LoadOrCreate(path string) (ServerID, error)
```

That's the entire new public surface. No new types, no new sentinels.

### Implementation sketch (`store.go`)

```go
package identity

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

func LoadOrCreate(path string) (ServerID, error) {
	raw, err := os.ReadFile(path)
	switch {
	case err == nil:
		return parsePersisted(path, raw)
	case errors.Is(err, fs.ErrNotExist):
		return mintAndPersist(path)
	default:
		return "", fmt.Errorf("identity: read %s: %w", path, err)
	}
}

func parsePersisted(path string, raw []byte) (ServerID, error) {
	s := strings.TrimSuffix(string(raw), "\n")
	id, err := ParseServerID(s)
	if err != nil {
		// Wrap with the path for operator diagnostics; %w preserves
		// errors.Is(err, ErrInvalidServerID) for callers and tests.
		return "", fmt.Errorf("identity: parse %s: %w", path, err)
	}
	return id, nil
}

func mintAndPersist(path string) (ServerID, error) {
	id := NewServerID()
	if err := writeServerID(path, id); err != nil {
		return "", err
	}
	return id, nil
}

func writeServerID(path string, id ServerID) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("identity: mkdir %s: %w", dir, err)
	}
	f, err := os.CreateTemp(dir, ".server-id-*.tmp")
	if err != nil {
		return fmt.Errorf("identity: create temp in %s: %w", dir, err)
	}
	tmp := f.Name()
	defer func() { _ = os.Remove(tmp) }()
	if err := os.Chmod(tmp, 0o600); err != nil {
		_ = f.Close()
		return fmt.Errorf("identity: chmod temp: %w", err)
	}
	if _, err := f.Write([]byte(string(id) + "\n")); err != nil {
		_ = f.Close()
		return fmt.Errorf("identity: write temp: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("identity: fsync temp: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("identity: close temp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("identity: rename to %s: %w", path, err)
	}
	return nil
}
```

### Three deliberate decisions

1. **`strings.TrimSuffix(s, "\n")`, not `strings.TrimSpace`.** The AC says
   "tolerates an *optional* trailing newline but otherwise validate
   strictly." `TrimSuffix` strips at most one terminal `\n` and leaves
   leading whitespace, `\r`, tabs, and trailing spaces in place — `ParseServerID`
   then rejects them. This is the strictest reading consistent with the AC.
   `TrimSpace` would tolerate `\r\n`, surrounding spaces, and form-feeds,
   broadening the parser's accept set for no concrete reason.

2. **Wrap the parse error with the path; do NOT include file contents.**
   `fmt.Errorf("identity: parse %s: %w", path, err)` gives operators the
   path they need to investigate, while `%w` keeps `errors.Is(err,
   ErrInvalidServerID)` working for AC #5. The wrapped error from
   `ParseServerID` is the bare sentinel (no caller-supplied bytes) — see
   `server_id.go:48-51`. We do **not** add the corrupt contents to the error
   string: a future log site could exfiltrate them, and they're useless for
   diagnostics ("look at the file" is what the path is for).

3. **No parent-directory fsync.** Per `docs/lessons.md` § "Atomic on-disk
   writes": the registry doesn't fsync parents either; the rename's
   directory-entry update is durable enough on Linux ext4 / macOS APFS for
   operator-recoverable identity data. Adds one syscall per first-run write
   and defends against a power-loss window we don't optimize for elsewhere.
   Stay consistent.

## Data flow

```
Phase 3 startup (not in this ticket):
  configured path = ~/.pyry/server-id (resolved by caller)
                    │
                    ▼
  identity.LoadOrCreate(path)
                    │
        ┌───────────┴───────────┐
        │ ReadFile              │
        │   ENOENT?             │ no  → TrimSuffix("\n") → ParseServerID
        │     yes               │                          │
        ▼                       │            ┌─────────────┴────────────┐
  NewServerID                   │            ▼                          ▼
        │                       │      ErrInvalidServerID        canonical id
        ▼                       │            │                          │
  MkdirAll(dir, 0700)           │            ▼                          ▼
  CreateTemp(dir, ".server-id-* │     return wrapped err      return id (no rewrite)
  Chmod(tmp, 0600)              │
  Write(id + "\n")              │
  Sync, Close                   │
  Rename(tmp, path)             │
        │                       │
        ▼                       │
  return id ◄─────────────────-─┘
```

The function is the entire I/O lifecycle for the identifier. It runs once
at startup, returns a `ServerID`, and is never called again.

## Concurrency model

Stateless. Two consequences for the implementation:

- **No locking inside the function.** AC #3's atomic-write guarantee is
  already a SIGKILL guarantee; intra-process concurrent calls against the
  same path are out of contract. The single-process assumption is documented
  in the doc-comment.
- **No goroutines, no channels, no `context.Context`.** First-run write is a
  handful of syscalls; subsequent reads are a single `ReadFile`. A ctx
  parameter would suggest cancellable I/O semantics that the implementation
  does not honor and the caller does not need.

## Error handling

| Path                                | Returned                                                                    |
|-------------------------------------|-----------------------------------------------------------------------------|
| File missing → mint + persist OK    | `(id, nil)`                                                                 |
| File missing → mkdir/write/rename fails | `("", wrapped fmt.Errorf)` — non-Is(ErrInvalidServerID)                  |
| File present, valid                 | `(id, nil)`                                                                 |
| File present, parse fails           | `("", wrapped err)` such that `errors.Is(err, ErrInvalidServerID)` is true; **file is not modified** |
| File present, ReadFile fails (EACCES, EIO) | `("", wrapped err)` — non-Is(ErrInvalidServerID)                     |

The "do not regenerate on corruption" rule is enforced by *structure* (the
parse path simply doesn't call `mintAndPersist`) rather than by a flag a
future maintainer could flip. That's load-bearing: silently regenerating
would break paired devices invisibly.

## Testing strategy

`store_test.go`. All tests use `t.TempDir()` and `t.Parallel()`.

1. **`TestLoadOrCreate_FirstRunGeneratesAndPersists`**
   - `dir := t.TempDir(); path := filepath.Join(dir, "subdir", "server-id")`
     (subdir verifies `MkdirAll` creates the parent).
   - First call: `id1, err := LoadOrCreate(path)`; assert no error, `id1 != ""`,
     `ParseServerID(string(id1))` round-trips.
   - Read the file directly and assert contents are exactly `string(id1) + "\n"`.
   - Stat the parent dir, assert mode `0o700`.
   - Stat the file, assert mode `0o600`.
   - Second call: `id2, err := LoadOrCreate(path)`; assert no error, `id2 == id1`.

2. **`TestLoadOrCreate_ExistingFileRoundTripsWithoutRewrite`**
   - Pre-populate `path` by writing `"550e8400-e29b-41d4-a716-446655440000\n"`
     directly with `os.WriteFile(path, ..., 0o600)`. Capture the file's mtime
     via `os.Stat`.
   - Call `LoadOrCreate(path)`; assert returned id equals the fixture and
     no error.
   - Stat the file again; assert `ModTime().Equal(preMtime)` AND assert
     contents are byte-identical to the fixture (mtime granularity on some
     filesystems is 1s; bytes are the harder check).

3. **`TestLoadOrCreate_ToleratesNoTrailingNewline`**
   - Pre-populate `path` with the canonical UUIDv4 string and **no**
     trailing newline.
   - Assert `LoadOrCreate` returns the id without error and does not rewrite
     the file (mtime + bytes stable). The AC says newline is *optional* on
     read.

4. **`TestLoadOrCreate_CorruptFileReturnsErrInvalidServerID`**
   - Subtests for: `"not-a-uuid\n"`, `""` (empty file), `"550E8400-E29B-41D4-A716-446655440000\n"`
     (uppercase), `"  550e8400-e29b-41d4-a716-446655440000\n"` (leading
     whitespace), `"550e8400-e29b-41d4-a716-446655440000\r\n"` (CRLF — strict
     newline rule), `"550e8400-e29b-41d4-a716-446655440000\n\n"` (double
     newline).
   - For each: pre-populate, capture pre-bytes, call `LoadOrCreate`, assert
     `errors.Is(err, ErrInvalidServerID)`, assert returned id is `""`, and
     assert the file's bytes are unchanged after the call (the no-rewrite
     invariant on corruption).

5. **`TestLoadOrCreate_PersistedFileMode`** *(absorbed into test 1 above —
   keep separate only if test 1 grows past ~30 lines.)*

6. **`TestLoadOrCreate_ReadFileError`**
   - `path := filepath.Join(t.TempDir(), "server-id")`; `os.Mkdir(path, 0o700)`
     so `path` is itself a directory. `os.ReadFile` returns a non-ENOENT
     error (`EISDIR` or similar). Assert `LoadOrCreate` returns a non-nil
     error that is **not** `errors.Is(err, ErrInvalidServerID)` (i.e., I/O
     errors are distinguishable from corruption).

No fixtures, no helper functions beyond what `os` provides. Mode assertions
use `info.Mode().Perm()` to mask off non-permission bits. Mtime comparison
uses `time.Time.Equal` (not `==`) per the lessons-doc rule on JSON-roundtrip
clock semantics — even though no JSON is involved here, `Equal` is the
right comparator on principle.

## Security review

This ticket has the `security-sensitive` label. Adversarial pass below;
verdict at the end.

### Trust boundaries

- The on-disk file is **not** an external-input boundary in the wire-protocol
  sense; it's owned by the operator's account. The threat model is "another
  local process running as a different user / a downgraded HOME perm /
  filesystem corruption," not "a remote attacker controls the bytes."
- The server-id itself is **not a secret**. It's published in QR codes and
  WS upgrade headers. The security property the file guards is **stability**
  (paired devices bind to it) and **integrity** (the bytes a future load
  reads are the bytes a prior write committed), not confidentiality.

### Categories walked

- **File mode (confidentiality of co-located data).** AC requires `0o600`
  file and `0o700` parent dir. The implementation chmods the temp file to
  `0o600` *before* writing data, then renames; the rename preserves the
  temp's mode, so the final file is `0o600` from byte one. There is no
  window in which the file is more permissive. `os.CreateTemp` itself
  defaults to `0o600` on Unix (Go documents this), so the explicit
  `os.Chmod` is belt-and-suspenders against future Go-stdlib changes. **Pass.**

- **Atomicity / partial-write exposure.** SIGKILL between any two syscalls
  in `writeServerID` either leaves no observable change (failure between
  CreateTemp and Rename — temp file orphaned, cleaned up by `defer
  os.Remove`) or commits the new file (Rename is the commit point). A
  reader on the next startup never sees a partial server-id. **Pass.**

- **Symlink / hardlink TOCTOU on the target.**
  - **Write path:** an attacker who can write into the parent dir
    pre-creates `path` as a symlink to `/etc/passwd`. We `Rename(tmp, path)`.
    POSIX rename replaces the directory entry — the symlink at `path` is
    unlinked, our regular file takes its place, the original target
    (`/etc/passwd`) is never touched. **No write-through-symlink risk.**
  - **Read path:** an attacker pre-creates `path` as a symlink to
    `/etc/shadow`. `os.ReadFile` follows symlinks. If we have permission
    to read the target, we'd read it; if not, we'd get `EACCES`. The contents
    fail `ParseServerID` and surface as `ErrInvalidServerID`. We do **not**
    log the contents — only the path. So a passive content read does not
    leak via this loader's return value. *However*, a future caller that
    formats the file's contents into a log line on parse failure would
    leak. Mitigation: the doc-comment for `LoadOrCreate` notes that the
    error message contains the path only; the implementation enforces this
    by passing the bare sentinel into `%w` (no `%v` of `raw`). **Pass with note.**
  - **Both paths assume the parent dir is operator-owned and `0o700`.**
    `MkdirAll(dir, 0o700)` enforces this on first creation but does NOT
    tighten an existing looser-perm directory. If `~/.pyry` was created by
    a prior tool with `0o755`, that's outside this ticket's authority —
    the install/setup story is the right place to enforce home-dir perms,
    not the loader. **Documented residual risk.**

- **Corruption regeneration as a misuse-resistance property.** AC #5
  forbids regenerating on parse failure. The implementation enforces this
  structurally (the parse path has no fallback into mintAndPersist). A
  silent regeneration would invalidate every paired device's binding —
  this is a bigger blast radius than any file-corruption operator would
  expect from a "first-run bootstrap." **Pass; structurally enforced.**

- **Concurrent first-run race.** Two pyry processes started simultaneously
  against the same HOME both observe `ENOENT`, both mint, both rename. The
  later rename wins; the earlier loser's id is gone. This is undefined
  behavior at the application level (two daemons sharing a HOME is itself
  a misconfiguration), and the AC explicitly says no locking is required.
  No security property is violated — the user gets exactly one server-id
  on disk, which both processes will read consistently on next startup.
  **Documented; out of contract.**

- **RNG quality.** Inherited from `NewServerID` (#206) — `crypto/rand`,
  panic on RNG failure. Unchanged here. **Pass.**

- **Logging the id.** The id is non-secret; logging is fine. Devices'
  `token_hash` (separate file in #208) is the secret-equivalent and is
  governed by `internal/devices`'s logging discipline, not this loader.
  **N/A.**

### Verdict: **PASS**

Residual risks documented (existing-loose parent-dir perms, concurrent-pyry
misconfig). Both are out of this loader's authority and explicitly noted in
the doc-comment or this spec.

## Open questions

None. The AC fully constrains the shape; the only judgment call (placement
in `internal/identity` vs sibling) is resolved above with explicit rationale.

## Out of scope

- **Path resolution.** `~/.pyry/server-id` resolution from config lives
  with the config loader (#205). `LoadOrCreate` takes an absolute path.
- **Caller wiring at daemon startup.** Calling `LoadOrCreate` from
  `cmd/pyry/main.go` is a Phase-3 wiring ticket; this one ships the
  primitive only.
- **`devices.json` persistence.** Sibling concern for paired devices,
  follows the same shape but operates on a JSON file with multiple records.
  Separate ticket.
- **CLI surface (`pyry server-id` to print the value).** Defer.
- **Mode tightening for pre-existing parent dirs.** Belongs in `pyry
  install` / setup tooling, not in this loader.
