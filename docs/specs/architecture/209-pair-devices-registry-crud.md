# #209 — `internal/devices` package: `devices.json` registry CRUD

**Size:** S (architect-confirmed). One new file `internal/devices/registry.go`
(~115 prod LOC) + co-located `internal/devices/registry_test.go`. One new
exported type (`Registry`) with six exported methods (`Load`, `Save`, `Add`,
`Remove`, `List`, `FindByTokenHash`); zero consumers. Within all S red lines
(≤3 new files, ≤150 prod lines, ≤5 new exported types, no consumer cascade —
the package's two existing exports `Device` / `HashToken` / `VerifyToken`
from #208 are imported, not re-defined).

**Status:** ready for development.

**Depends on:** #208 (merged) — uses `devices.Device` from `device.go`. No
other consumers (the `pyry pair` command, revoke command, and auth handler are
follow-up tickets).

## Files to read first

The developer's turn-1 data load. Each entry is paged in deliberately —
don't grep for them.

- `internal/devices/device.go` (the whole file, 50 lines) — the `Device`
  struct shape, JSON tags, and the SECURITY package doc comment that this
  ticket extends. The new file lives in the same package; the package doc
  comment is already written and does not need updating.
- `internal/sessions/registry.go` (the whole file, 122 lines) — **the
  reference implementation**. Mirror:
  - The atomic-rename pattern in `saveRegistryLocked` (lines 60-92):
    `os.MkdirAll(dir, 0o700)` → `os.CreateTemp(dir, ".devices-*.json.tmp")`
    → `defer os.Remove(tmp)` → `os.Chmod(tmp, 0o600)` → encode → `f.Sync()`
    → `f.Close()` → `os.Rename(tmp, path)`. Wrap each error with
    `fmt.Errorf("registry: <op> %s: %w", path, err)`.
  - The load semantics in `loadRegistry` (lines 35-51): missing file →
    `(nil, nil)` (here: empty `*Registry`); empty file → `(nil, nil)` (here:
    empty `*Registry`); malformed JSON → wrapped error.
  - The stable-sort discipline in `sortEntriesByCreatedAt` (lines 114-121).
- `internal/sessions/registry_test.go` (the whole file, 357 lines) — **the
  reference test layout**. Mirror table-driven AC coverage:
  - `TestSaveLoad_RoundTrip` — add → save → load → all-fields-preserved.
  - `TestLoad_MissingFile` — load of nonexistent path → empty registry, nil
    error.
  - `TestLoad_EmptyFile` — load of zero-byte file → empty registry, nil
    error.
  - `TestLoad_MalformedJSON` — load of `{not json` → error wrapped with
    `registry: parse`.
  - `TestSave_FilePermissions` — saved file mode is `0o600`, parent dir mode
    is `0o700`. Skip on `runtime.GOOS == "windows"`.
  - `TestSave_StableOrdering` — same content saved in different in-memory
    order produces byte-identical files.
  - `TestSave_AtomicRenamePreservesOldFile` — chmod-the-dir-readonly trick
    proves the pre-existing file survives a failed save unchanged.
- `docs/lessons.md` § "Atomic on-disk writes" (lines 32-37) — **the rules
  this ticket enforces**: rename is the commit point; same-fs temp dir;
  `defer os.Remove(tmp)`; no parent-dir fsync; sort before serialize for
  byte-deterministic output. Re-read before writing `Save`.
- `docs/lessons.md` § "JSON roundtrip strips monotonic-clock state from
  `time.Time`" (lines 196-200) — `Device.PairedAt` and `LastSeenAt` are
  `time.Time`. Round-trip tests must compare with `time.Time.Equal`, never
  `==` or `reflect.DeepEqual`.
- `docs/specs/architecture/208-pair-device-entry-and-token-hashing.md` —
  the SECURITY contract this ticket inherits. Section "Why no bcrypt or
  salt" and "Security review" already establish the threat model;
  re-reference, don't relitigate.
- `docs/protocol-mobile.md:62` — protocol-level commitment that
  `devices.json` stores the hash, never the plaintext. The on-disk file
  this ticket creates is the named artefact.
- `docs/protocol-mobile.md:618` — security review note flags TOCTOU on
  `devices.json` writes. The atomic-rename pattern this ticket inherits
  is the structural defense — name it explicitly in the design.
- `CODING-STYLE.md` § "Error Handling" (lines 27-33) — `fmt.Errorf("X: %w",
  err)` shape, no panics, no silent error swallowing. § "Concurrency"
  (lines 54-59) — `sync.Mutex` for state, `t.Parallel()`, race detector
  always on.
- The ticket body itself (#209) — five AC bullets. The mapping to test
  cases is one-to-one with one exception: the AC says "Add(d Device)"
  with no return value; this ticket implements it as `Add(d Device)`
  returning nothing (caller owns uniqueness).

## Context

#208 delivered `Device` and the token hashing primitives (`HashToken`,
`VerifyToken`). They're pure functions over a leaf struct; nothing
persists them yet.

This ticket adds the on-disk layer: a mutex-guarded `Registry` that
atomically reads and writes `~/.pyry/<name>/devices.json`. It's the
storage primitive the next three follow-up tickets will consume:

1. `pyry pair` — mints a token, builds a `Device`, calls `Add` then `Save`.
2. `pyry pair revoke <name>` — calls `Remove(name)` then `Save`.
3. WS handshake auth — calls `Load` once at daemon startup, then
   `FindByTokenHash(HashToken(presented))` per phone connect.

The ticket is intentionally storage-only: no CLI plumbing, no daemon
wiring, no bootstrap-on-launch. Those concerns live in the consumers.

### Path note

The AC describes the typical caller-time path as `~/.pyry/devices.json`.
The follow-up consumers will resolve to `~/.pyry/<name>/devices.json` (the
per-instance subdirectory pattern `internal/sessions` already uses, where
`<name>` is the daemon instance name). The registry API itself is
path-agnostic — `Load` and `Save` take `path string`. Resolving the
final path is the caller's job; this ticket only enforces atomic-write
discipline at whichever path the caller passes in.

## Design

### Package placement

The new file lives in the existing `internal/devices` package next to
`device.go`. No subpackage. Per CODING-STYLE: flat packages preferred,
one package per concern. The registry is the on-disk extension of the
same concern (`Device`); they share the same package boundary, the
same `package devices` doc comment, and the same SECURITY discipline.

```
internal/devices/
  device.go          (#208) Device struct, HashToken, VerifyToken
  device_test.go     (#208)
  registry.go        (this ticket) Registry, Load, Save, Add, Remove, List, FindByTokenHash
  registry_test.go   (this ticket)
```

### Exported surface

```go
// Registry is the in-memory device list, guarded by a mutex. Construct
// via Load (cold-start or warm-start from disk); persist via Save. All
// methods are safe for concurrent use.
type Registry struct {
    mu      sync.Mutex
    devices []Device
}

// Load reads path. A missing file returns an empty *Registry with no
// error (cold start). A zero-byte file returns an empty *Registry with
// no error. Malformed JSON returns a wrapped error and a nil *Registry.
//
// The returned *Registry is independent of the on-disk file: subsequent
// Save calls re-encode from the in-memory slice; the file may move or
// be deleted between Load and Save without affecting in-memory state.
func Load(path string) (*Registry, error)

// Save writes the registry atomically: temp file in filepath.Dir(path)
// at mode 0600, fsync, rename into place. Parent directory is created
// with mode 0700 if missing. Returns a wrapped error on any step
// failure; on failure the pre-existing target file (if any) is left
// untouched (rename is the commit point — see docs/lessons.md
// "Atomic on-disk writes").
//
// Entries are sorted by PairedAt then Name before serialization to
// guarantee byte-identical output for the same logical content (defends
// against Go's randomized slice-iteration if a consumer ever reorders
// in memory).
func (r *Registry) Save(path string) error

// Add appends d to the in-memory list. Caller owns uniqueness — Add
// does not validate that d.Name or d.TokenHash is unique within the
// registry. The pair-mint consumer (#TBD) is the only producer that
// reaches this method and validates uniqueness against List() before
// calling Add.
func (r *Registry) Add(d Device)

// Remove deletes the first device whose Name equals name. Returns true
// iff a device was removed; false if no entry matched. Comparison is
// byte-exact; no Unicode normalization (operators picked the name).
func (r *Registry) Remove(name string) bool

// List returns a copy of the in-memory device list. Callers may mutate
// the returned slice and its elements without affecting registry
// state (the slice header and each Device value are shallow-copied;
// no Device field is a reference type that would alias).
func (r *Registry) List() []Device

// FindByTokenHash returns the device whose TokenHash equals hash, and
// true if one was found. Comparison is byte-exact (==). Constant-time
// comparison is not required here: hash is the already-derived
// SHA-256 hex of the wire-presented plain token; the constant-time
// concern is for the plain↔hash boundary, which devices.VerifyToken
// owns. See devices/device.go SECURITY note and #208's security
// review for the full reasoning.
func (r *Registry) FindByTokenHash(hash string) (Device, bool)
```

### On-disk shape

```json
{
  "devices": [
    {
      "token_hash": "ba7816bf...",
      "name": "Juhana's Pixel 8",
      "paired_at": "2026-05-09T12:34:56.789Z",
      "last_seen_at": "2026-05-09T12:35:01.012Z"
    }
  ]
}
```

Envelope shape (`{"devices": [...]}`), not bare top-level array. Future
top-level fields (e.g. push-token registration metadata per
`protocol-mobile.md:495`) can be added without breaking jq pipelines or
the Go JSON-decoder discipline. Same future-proofing rationale as the
sessions registry's `{"sessions": [...]}` envelope (see
`docs/lessons.md` § "Don't pick a real verb name as the still-unknown
placeholder...").

No `version` field. Per AC: "Out of scope: schema versioning (defer
until first migration)." When the first migration lands (sibling
ticket), add the `version` field then.

Internal type:

```go
type registryFile struct {
    Devices []Device `json:"devices"`
}
```

The on-disk envelope `registryFile` is unexported (it exists only as
the JSON-marshal target); `Registry` is the exported in-memory type.
Same split as `internal/sessions`'s `registryFile` vs. `Pool`.

### Concurrency

Single mutex (`Registry.mu`) guards the slice. All six methods take
the lock at entry and release on exit:

| Method            | Critical section                                                  |
|-------------------|-------------------------------------------------------------------|
| `Save`            | snapshot slice → release lock → atomic-write outside lock         |
| `Add`             | append                                                            |
| `Remove`          | linear scan → splice                                              |
| `List`            | shallow-copy slice                                                |
| `FindByTokenHash` | linear scan → return (Device, bool)                               |

`Save` releases the lock **before** performing I/O. The serialized
bytes are computed from a slice copy taken under the lock; the file
write happens outside. Holding the lock across `os.Rename` would
serialize all writes (fine for low call volume but unnecessarily
exclusive against `List` / `FindByTokenHash` reads, which the auth
hot path will do once per WS connect). The lock-then-snapshot-then-
write pattern is a one-line addition over `internal/sessions`'s
hold-across-I/O — worth it because the auth path is the high-frequency
reader and pairing is the rare writer.

`Load` is a package-level function, not a method; it constructs a new
`*Registry` and never aliases the file's contents. No locking concerns.

#### Save concurrency note

Two concurrent `Save` calls on the same `*Registry` could race at the
filesystem layer: both write temp files, both rename to the same
target. `os.Rename` is atomic per call; the second rename overwrites
the first. The result is deterministic ("the later rename wins") and
not corrupting (each temp file is itself a complete encode). No extra
lock needed at the file level — the in-memory snapshot under
`Registry.mu` is already a consistent point-in-time view per Save call.

If the consumer ever needs "Save once, then everyone observes the new
state," they call `Save` from a single goroutine (the pair command's
goroutine; the daemon's auth path is read-only). This is consistent
with how `internal/sessions` works today.

### Sort discipline on Save

```go
sort.SliceStable(out, func(i, j int) bool {
    if !out[i].PairedAt.Equal(out[j].PairedAt) {
        return out[i].PairedAt.Before(out[j].PairedAt)
    }
    return out[i].Name < out[j].Name
})
```

Primary key `PairedAt` (older devices first, matching the operator's
mental model: "this is the order I paired them"); tiebreaker `Name`
(byte-exact, since names are operator-typed UTF-8 strings).
`time.Time.Equal` (not `==`) for the primary comparator — JSON
roundtrip strips monotonic-clock state, and `==` would treat two
otherwise-equal timestamps as unequal if one carries the monotonic
component (see `docs/lessons.md` § "JSON roundtrip strips
monotonic-clock state").

Sort runs on the Save-side snapshot, not on the live in-memory slice.
This keeps Add insertion-order-stable in memory (the pair UI may
display "most recently added first" later) while keeping disk output
deterministic.

### Error wrapping

All filesystem errors wrap with `fmt.Errorf("registry: <op> %s: %w",
path, err)` matching `internal/sessions/registry.go`'s convention. The
sentinel-free shape is intentional: callers don't branch on these
errors today (the pair command surfaces them to the operator
verbatim; the daemon's startup-time `Load` failure is fatal).

`Load` distinguishes:
- `errors.Is(err, fs.ErrNotExist)` → `(empty *Registry, nil)`
- `len(data) == 0` → `(empty *Registry, nil)`
- `json.Unmarshal` error → wrapped error, nil registry

"Empty registry" means a freshly-allocated `*Registry{}` with a nil
or zero-length `devices` slice. `Add` / `Remove` / `List` /
`FindByTokenHash` all behave correctly on the zero value (the slice
nil-vs-empty distinction is not user-visible because none of the
methods returns the slice header directly — `List` always allocates).

### Logger / config / context

None. The registry is a leaf primitive over the filesystem; the
existing `slog` discipline applies to **callers** of `Save` (the pair
command logs "paired device <name>" after Save returns; the
daemon's startup `Load` logs "loaded N paired devices" or surfaces
the error). Putting a logger inside the registry would force every
test to construct one; the call-site logging pattern matches
`internal/sessions/registry.go` (which also has no logger).

No `context.Context`. The atomic-write is two syscalls (`Sync`,
`Rename`) totaling a few hundred microseconds at p99 on a healthy
local fs — adding ctx-cancel plumbing for that window would be
ceremony without value. If a future ticket needs to bound disk I/O
(e.g. an SSD that's wedged), revisit then.

## Testing strategy

Same-package tests in `internal/devices/registry_test.go`. Each test
`t.Parallel()` (per-test `t.TempDir()` makes them independent).
Table-driven where it helps. No external test deps.

### `TestRegistry_LoadMissingFile`

```go
got, err := Load(filepath.Join(t.TempDir(), "nope.json"))
// Want: err == nil, got != nil, len(got.List()) == 0
```

Maps to AC: "A missing file returns an empty registry with no error."

### `TestRegistry_LoadEmptyFile`

```go
path := filepath.Join(t.TempDir(), "devices.json")
os.WriteFile(path, nil, 0o600)
got, err := Load(path)
// Want: err == nil, len(got.List()) == 0
```

Maps to AC: "an empty file returns an empty registry with no error."

### `TestRegistry_LoadMalformedJSON`

```go
os.WriteFile(path, []byte("{not json"), 0o600)
got, err := Load(path)
// Want: err != nil, contains "registry: parse", got == nil
```

Maps to AC: "malformed JSON is a hard error."

### `TestRegistry_AddSaveLoadRoundTrip`

```go
when := mustParseTime(t, "2026-05-09T12:34:56.789Z")
later := when.Add(time.Second)
r := &Registry{}
r.Add(Device{
    TokenHash:  HashToken("plain-1"),
    Name:       "Juhana's Pixel 8",
    PairedAt:   when,
    LastSeenAt: when,
})
r.Add(Device{
    TokenHash:  HashToken("plain-2"),
    Name:       "Phone 2",
    PairedAt:   later,
    LastSeenAt: later,
})
path := filepath.Join(t.TempDir(), "devices.json")
must(t, r.Save(path))
back, err := Load(path)
// Want: err == nil, List length 2, all four fields preserved per device
//       (compare time fields with time.Time.Equal, not ==).
```

Maps to AC: "add/save/load roundtrip preserves all fields." Compare
times with `time.Time.Equal` per `docs/lessons.md` § JSON roundtrip
monotonic-clock.

### `TestRegistry_RemovePresent`

```go
r := &Registry{}
r.Add(Device{Name: "alice", TokenHash: HashToken("a")})
r.Add(Device{Name: "bob",   TokenHash: HashToken("b")})
ok := r.Remove("alice")
// Want: ok == true, List() returns one entry whose Name == "bob"
```

Maps to AC: "remove of present and absent names" + "Remove returns
true iff a device was removed."

### `TestRegistry_RemoveAbsent`

```go
r := &Registry{}
r.Add(Device{Name: "alice", TokenHash: HashToken("a")})
ok := r.Remove("ghost")
// Want: ok == false, List() unchanged (one entry, "alice")
```

### `TestRegistry_FindByTokenHash`

Table-driven: hit and miss.

| name              | setup                        | hash arg                    | want device   | want ok |
|-------------------|------------------------------|-----------------------------|---------------|---------|
| hit               | Add(name=alice, hash=H("a")) | `HashToken("a")`            | the alice row | true    |
| miss-empty        | Add(name=alice, hash=H("a")) | `""`                        | zero Device   | false   |
| miss-non-matching | Add(name=alice, hash=H("a")) | `HashToken("z")`            | zero Device   | false   |
| miss-empty-reg    | (empty registry)             | `HashToken("a")`            | zero Device   | false   |

Maps to AC: "find-by-hash hit and miss."

### `TestRegistry_SaveFilePermissions`

Mirror `internal/sessions/registry_test.go:TestSave_FilePermissions`.
Skip on Windows (POSIX semantics required). Save to `<tmp>/pyry/devices.json`
(parent dir doesn't exist) and assert:
- Parent dir mode is `0o700`.
- File mode is `0o600`.

Maps to AC: "saved file is mode 0600" + the Save contract for
"parent directory created with mode 0700 if missing."

### `TestRegistry_SaveStableOrdering`

Mirror `internal/sessions/registry_test.go:TestSave_StableOrdering`.
Build two `*Registry` instances with the same logical content but
different Add-order; Save both; assert byte-identical output. Tests
the `sort.SliceStable` discipline.

```go
mk := func(order []int) *Registry {
    devs := []Device{
        {Name: "alice", TokenHash: HashToken("a"), PairedAt: t1, LastSeenAt: t1},
        {Name: "bob",   TokenHash: HashToken("b"), PairedAt: t2, LastSeenAt: t2},
        {Name: "carol", TokenHash: HashToken("c"), PairedAt: t3, LastSeenAt: t3},
    }
    r := &Registry{}
    for _, i := range order {
        r.Add(devs[i])
    }
    return r
}
// Save mk([0,1,2]) → A; Save mk([2,0,1]) → B; require bytes.Equal(A, B).
```

### `TestRegistry_SaveAtomicRenamePreservesOldFile`

Optional but high-value. Mirror
`internal/sessions/registry_test.go:TestSave_AtomicRenamePreservesOldFile`
— pre-write a known JSON blob, chmod the parent dir to `0o500`,
attempt Save, assert (1) Save returns an error, (2) the pre-existing
file's bytes are unchanged. Skip on Windows. This is the structural
defense against the TOCTOU concern called out in
`protocol-mobile.md:618`.

### `TestRegistry_ConcurrentReadWrite` (small race-detector probe)

```go
r := &Registry{}
var wg sync.WaitGroup
for i := 0; i < 8; i++ {
    wg.Add(1)
    go func(i int) {
        defer wg.Done()
        r.Add(Device{Name: fmt.Sprintf("d-%d", i), TokenHash: HashToken(fmt.Sprintf("p-%d", i))})
        _ = r.List()
        _, _ = r.FindByTokenHash(HashToken("p-0"))
    }(i)
}
wg.Wait()
// Final List length is 8 (all Adds completed; no concurrent removes).
```

Confirms the mutex is actually held — if any method drops the lock,
`go test -race` will flag the slice header / element accesses. No
assertion on ordering (Add order is non-deterministic across goroutines
by design).

## Open questions

Resolved during refinement; recorded so the developer doesn't relitigate:

- **Should `Add` return an error on duplicate Name?** No. The AC
  signature `Add(d Device)` is void. Pairing-side uniqueness check
  belongs in the consumer (#TBD pair command), not the storage
  primitive. If the consumer ever wants to reject silent duplicates,
  it calls `List()` first and inspects.
- **Should `Remove` save automatically?** No. AC keeps Save separate.
  Consumers orchestrate ("Add(d); if err := Save(path); err != nil
  { ... }") — same shape as `internal/sessions/Pool` does.
- **Should `FindByTokenHash` use `subtle.ConstantTimeCompare`?** No.
  The constant-time concern is the plain↔hash boundary, owned by
  `VerifyToken` in #208. Once the wire-presented plain has been
  hashed (deterministic SHA-256), comparing two 64-char hex strings
  is byte-exact — and any timing leak from `==` early-exit on a
  prefix mismatch reveals a public derivative (a hash that the
  attacker could have computed themselves), not a secret. See #208's
  security review § "Constant-time compare" for the full chain of
  reasoning. A linear scan with `==` is the simpler, correct shape.
- **Should `List` return a deep copy?** Shallow copy is sufficient.
  `Device`'s fields are all value types (`string`, `time.Time`); no
  pointer or slice fields would alias the registry's storage. The
  returned slice header is fresh per call, so caller mutation of the
  returned slice can't reach the registry's slice. A deep copy
  would be ceremony without semantic gain.
- **Should `Save` fsync the parent directory after rename?** No.
  Per `docs/lessons.md` § "Atomic on-disk writes": on Linux ext4 /
  macOS APFS, the rename's directory entry update is durable enough
  for operator-recoverable JSON. Pyrycode does not optimize for the
  power-loss window. If real-world `devices.json` corruption ever
  surfaces, revisit then.
- **Should the registry hold its own path?** No. `Load(path)` and
  `Save(path)` keep the file path as a caller-supplied parameter.
  This matches `internal/sessions`'s split (`loadRegistry(path)` /
  `saveRegistryLocked(path, reg)`). Storing the path inside the
  `Registry` would couple it to a single file location and prevent
  test patterns that load/save against `t.TempDir()`.
- **Should the on-disk shape carry a `version` field today?** No.
  Per AC, schema versioning is out of scope. The envelope shape
  (`{"devices": [...]}` rather than a bare array) reserves room for
  a future `version` field without a wire break.

## Security review

Per CLAUDE.md (architect's pipeline-wide instructions): this ticket has
the `security-sensitive` label, so the architect runs an adversarial
self-review of the spec before commit. The pass walks the standard
categories from `agents/architect/security-review.md` (referenced by
the label gate) and outputs a verdict.

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No findings. The registry is a pure on-disk
  CRUD layer; no untrusted input crosses its API. `Load` reads from a
  caller-supplied path that the daemon resolves at startup
  (`~/.pyry/<name>/devices.json`); the operator owns this directory
  via filesystem permissions. The `path` argument is **not** user-
  controlled — it is daemon-internal — so path-traversal concerns do
  not apply. The pair-command consumer (sibling ticket) is responsible
  for treating the operator's `--name` flag, if any reaches Add, as a
  string label only, not as a filesystem fragment.

- **[Tokens, secrets, credentials]** No findings.
  - Storage: `Device.TokenHash` is the SHA-256 hex of a 256-bit random
    token (per #208 + minting sibling). The registry persists hash
    only — no plain ever reaches this layer (the pair command hashes
    before calling `Add`; the auth path hashes the wire token before
    calling `FindByTokenHash`). The on-disk shape contains hash, name,
    paired_at, last_seen_at — none of which are sensitive in the
    threat model (`protocol-mobile.md` § "Out of scope (security)"
    explicitly excludes leak-of-paired-set scenarios).
  - Logging: the registry never logs. Callers log `device.Name` and
    `paired_at` in operator-facing output; never `TokenHash` (it's a
    hash, but the package doc comment in `device.go` already enforces
    "treat hash like a secret in logs" by treating the whole package
    as security-sensitive).
  - Lifecycle: revocation is `Remove(name)` followed by `Save` — the
    AC confirms `Remove` returns true iff a device was removed, so
    consumers can assert "the device I just revoked actually existed"
    before logging the revocation. Per-device revocation falls out
    structurally; whole-set revocation is `os.Remove(devices.json)`
    followed by daemon restart (operator-level action, no API
    surface needed).

- **[File operations]** No findings.
  - **Atomic write.** `Save` uses the lessons-codified pattern:
    `os.MkdirAll(parent, 0o700)` → `os.CreateTemp(parent,
    ".devices-*.json.tmp")` → `defer os.Remove(tmp)` → `os.Chmod(tmp,
    0o600)` → encode → `f.Sync()` → `f.Close()` → `os.Rename(tmp,
    path)`. SIGKILL anywhere before rename leaves the pre-existing
    `devices.json` byte-identical; SIGKILL after leaves the new
    version. Partial JSON in the target is unreachable. This is the
    structural defense against the TOCTOU concern named in
    `protocol-mobile.md:618`.
  - **Permissions.** Parent dir `0o700`, file `0o600` — operator-only
    read/write, no group, no world. Test
    `TestRegistry_SaveFilePermissions` asserts both. `os.CreateTemp`
    creates the temp file mode `0o600` by default, but
    `os.Chmod(tmp, 0o600)` is included unconditionally to defend
    against a future umask-permissive env or stdlib behaviour change
    — same belt-and-suspenders pattern as `saveRegistryLocked`.
  - **No symlink follow.** `os.OpenFile`-style symlink-resistance is
    not added here. The threat is "operator's home directory has a
    malicious symlink at `~/.pyry/<name>/devices.json` pointing
    elsewhere." This is out of the threat model — the operator owns
    `~/.pyry/`. If a future ticket extends pyrycode to multi-user or
    drop-privileges scenarios, revisit.
  - **No path traversal.** `path` is daemon-internal (the daemon
    resolves it from config + `<name>`). Pair-command-side validation
    of `<name>` (no `/`, no `..`, etc.) is the consumer's concern,
    not this primitive's. Naming this boundary explicitly so the
    pair-command spec catches it.

- **[Subprocess / external command]** N/A. No `os/exec`, no shell-out.

- **[Cryptographic primitives]** No findings. The registry stores
  hashes computed by `devices.HashToken` (SHA-256, deterministic) and
  compares them with `==` in `FindByTokenHash`. The constant-time
  concern (covered by `devices.VerifyToken`) does not apply at the
  hash↔hash boundary — see "Open questions" above for the full
  chain of reasoning. No new crypto code in this ticket.

- **[Network & I/O]** N/A. No network, no `net`, no `http`. Local
  filesystem only. `Save` writes one file; size is bounded by the
  number of paired devices (operator-controlled, expected single
  digits).

- **[Error messages, logs, telemetry]** No findings. All errors wrap
  via `fmt.Errorf("registry: <op> %s: %w", path, err)`. The path is
  daemon-internal (not secret); no hash, name, or other device field
  ever reaches an error message (the registry knows nothing about
  the device payload at the I/O layer — encode failures surface
  `json.Unmarshal`/`json.Encoder` errors verbatim, which never embed
  the input value).

- **[Concurrency]** No findings.
  - Single mutex; entry/exit on every method. The
    `TestRegistry_ConcurrentReadWrite` race-detector probe confirms.
  - `Save` releases the lock before file I/O, snapshotting the slice
    under lock and encoding outside. Two concurrent Saves on the same
    `*Registry` produce two complete temp files and two renames; the
    later rename wins. No torn write (rename is atomic per call), no
    partial JSON, no lost concurrent-Add updates *committed before
    each Save's snapshot* — but a concurrent Add that interleaves
    with two Saves may land in zero, one, or both. This is the
    expected concurrent-writer model: callers serialize Save calls
    if they need "Save sees all Adds up to T."
  - No deadlock paths. The mutex is leaf — no callbacks into other
    locks, no re-entrant takes. Same single-mutex shape as the
    sessions registry's pre-#41 lifecycle.

- **[Threat model alignment]**
  - `protocol-mobile.md:62` ("binary stores `sha256(token)` in
    `devices.json`, never the plaintext") — implemented; the registry
    persists `Device.TokenHash` (already-hashed) verbatim.
  - `protocol-mobile.md:618` (TOCTOU on `devices.json` writes) —
    addressed structurally by the atomic-rename pattern. The window
    "compute → write → rename" never exposes a partially-written
    file at the canonical path; the rename is the commit point.
  - `protocol-mobile.md:97-98` (binary validates device-token on
    first frame) — out of scope for this ticket. The auth handler
    will call `Load` once at startup, hash the wire token, call
    `FindByTokenHash`. The hash-comparison primitive is correct;
    the rate-limiting / error-envelope concerns belong to the auth
    handler.
  - Out of scope and named so: per-device push-token persistence
    (per `protocol-mobile.md:495`); per-device `last_seen_at` updates
    on each WS connect (auth-handler concern; this ticket persists
    the field, doesn't update it); migration to a database (defer);
    encrypting `devices.json` at rest (defer; current threat model
    doesn't justify the operator UX cost).

**Reviewer:** architect (self-review per `agents/architect/security-review.md`)
**Date:** 2026-05-09
