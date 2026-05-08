# `internal/identity` — typed routing identifiers

Home for typed identifiers that span subsystems and the on-disk bootstrap of those identifiers. Today: `ServerID`, the public routing identifier for one pyrycode-binary instance — surfaced in QR pairing payloads and the relay handshake's `x-pyrycode-server` upgrade header. Future: potential `DeviceID`, `PairedDeviceID`.

The pure types and validation live next to the I/O wrapper that mints and persists them on first run. Foundation slice for Phase 3 (mobile + relay) work; no consumers wired yet.

## Surface

```go
type ServerID string

func NewServerID() ServerID
func ParseServerID(s string) (ServerID, error)
func LoadOrCreate(path string) (ServerID, error)

var ErrInvalidServerID = errors.New("identity: invalid server id")
```

Four exports. Construct `ServerID` only via `NewServerID`, `ParseServerID`, or `LoadOrCreate` — direct `ServerID(rawString)` outside the package is a review-enforced anti-pattern (Go's type system can't prevent it; the `internal/` boundary contains the exposure to the pyrycode module itself).

The empty `ServerID ("")` is the unset sentinel; never a valid generated id.

## Canonical form

UUIDv4, lowercase hex, 36 chars, dashes at positions 8/13/18/23, version-4 nibble (`4`) at position 14, RFC 4122 variant nibble (`8`/`9`/`a`/`b`) at position 19.

```
550e8400-e29b-41d4-a716-446655440000
        ^    ^    ^    ^
        8    13   18   23   ← dash positions
                  ^
                  position 14 must be '4' (version)
                       ^
                       position 19 must be 8/9/a/b (variant)
```

`protocol-mobile.md` pins this as the wire shape: server-id is "UUIDv4 (canonical hex form)" minted by the binary on first run, surfaced in QR codes and unencrypted on WS upgrade. ~122 bits of entropy; unguessability is the security model.

## `NewServerID` — generation

Reads 16 bytes from `crypto/rand`, sets the version (`0x40`) and variant (`0x80`) nibbles, formats as canonical UUIDv4. Returns `ServerID` directly — no error.

`crypto/rand.Read` is documented as infallible on supported platforms (Go 1.24+). If the system rng is unavailable we panic — silently degrading to a zero-entropy id would break the unguessability the relay-routing security model depends on. **Never** fall back to `math/rand`.

## `ParseServerID` — validation

Returns `(ServerID, error)`. Validates canonical UUIDv4 shape (36 chars, lowercase hex, dashes at fixed positions, version + variant nibbles). Rejects:

- empty string
- wrong length (35 or 37 chars)
- uppercase hex (`550E8400-...`)
- wrong version nibble (`-11d4-` v1, `-21d4-` v2, ...)
- wrong variant nibble (`-7716-`, `-c716-`, ...)
- non-hex characters
- missing or misplaced dashes

Returns `ErrInvalidServerID` on any failure — caller-supplied input is **not** embedded in the error message (avoids needless log-injection vector for relay-supplied input). Callers that need richer context wrap themselves via `fmt.Errorf`. Use `errors.Is(err, ErrInvalidServerID)` for branching.

Use this at every wire/disk boundary that accepts an externally-supplied server-id (persistence load, pairing payload unmarshal, relay handshake).

## `LoadOrCreate` — first-run bootstrap

```go
func LoadOrCreate(path string) (ServerID, error)
```

The full I/O lifecycle for the binary's server-id. Runs once at daemon startup, returns a `ServerID`, never called again. Caller resolves the absolute path (typically `~/.pyry/server-id` from config); `LoadOrCreate` operates on absolute paths so tests can use `t.TempDir()`.

**First run (path missing):** mint via `NewServerID`, ensure parent dir exists at mode `0700`, atomic-write to a sibling temp file (`.server-id-*.tmp`) chmod'd to `0600`, write `<id>\n`, fsync, close, `rename(2)` over the target. SIGKILL between any two syscalls either leaves no observable change (orphan temp cleaned via `defer os.Remove`) or commits the new file — readers never see a partial `server-id`. Mirrors the canonical atomic-write recipe in `internal/sessions/registry.go:53-92`.

**Subsequent runs (path present):** `os.ReadFile`, `strings.TrimSuffix(s, "\n")` (strict — strips at most one terminal `\n`; leading whitespace, `\r`, tabs, internal spaces still reach `ParseServerID` and fail), then `ParseServerID`. The file is **never** rewritten on the existing-file path, even on validation failure.

### Corruption is operator-escalated, not auto-recovered

A file that fails `ParseServerID` returns a wrapped error matching `errors.Is(err, ErrInvalidServerID)`. The loader does **not** regenerate. Paired devices bind their device-tokens to a specific server-id; silently minting a fresh id would invalidate every pairing without operator awareness. The "do not regenerate" rule is enforced *structurally* — the parse path simply doesn't call `mintAndPersist` — not via a flag a future maintainer could flip.

Errors include the path for operator diagnostics; **file contents are never echoed** into error strings (a future log site could exfiltrate them, and "look at the file" is what the path is for). The wrapped sentinel from `ParseServerID` carries no caller bytes (see `server_id.go`'s sentinel-only return).

### Three deliberate decisions

1. **`strings.TrimSuffix(s, "\n")`, not `strings.TrimSpace`.** AC says "tolerates an *optional* trailing newline but otherwise validate strictly." `TrimSuffix` strips at most one terminal `\n`; `TrimSpace` would tolerate `\r\n`, surrounding spaces, and form-feeds, broadening the parser's accept set for no concrete reason.
2. **No parent-directory fsync.** Per `lessons.md` § "Atomic on-disk writes": the rename's directory-entry update is durable enough on Linux ext4 / macOS APFS for operator-recoverable identity data. Stays consistent with `sessions.json`'s write recipe.
3. **No locking, no `context.Context`.** Bootstrap runs once at daemon startup before any goroutines fan out. Two pyry processes sharing a HOME is a misconfiguration outside this loader's contract.

### File modes

- Parent directory: `MkdirAll(dir, 0o700)` — applies on first creation only; does NOT tighten an existing looser-perm directory (install/setup tooling owns that).
- Target file: `0600` from byte one. The temp is chmod'd to `0600` *before* writing data; `rename(2)` preserves the temp's mode, so there is no window in which the file is more permissive. `os.CreateTemp` defaults to `0600` on Unix; the explicit `Chmod` is belt-and-suspenders against future Go-stdlib changes.

### Error taxonomy

| Path                                | Returned                                                                    |
|-------------------------------------|-----------------------------------------------------------------------------|
| File missing → mint + persist OK    | `(id, nil)`                                                                 |
| File missing → mkdir/write/rename fails | `("", wrapped err)` — non-`Is(ErrInvalidServerID)`                       |
| File present, valid                 | `(id, nil)`                                                                 |
| File present, parse fails           | `("", wrapped err)` such that `errors.Is(err, ErrInvalidServerID)`; **file unmodified** |
| File present, ReadFile fails (EACCES, EISDIR) | `("", wrapped err)` — non-`Is(ErrInvalidServerID)`                |

I/O errors are distinguishable from corruption — callers can branch on `errors.Is(err, ErrInvalidServerID)` without false positives from a permissions issue.

### Symlink / hardlink notes

- **Write path:** `Rename(tmp, path)` replaces the directory entry. A pre-existing symlink at `path` pointing to an attacker-chosen target is unlinked; the original target is never touched.
- **Read path:** `os.ReadFile` follows symlinks. A pre-existing symlink whose target is readable returns its contents, which fail `ParseServerID` and surface as `ErrInvalidServerID`. The path-only error message ensures the contents do not leak via this loader's return value.

See [ADR 019](../decisions/019-identity-loader-co-located.md) for the package-placement rationale.

## Three deliberate divergences from `sessions.NewID` / `sessions.ValidID`

The spec mirrors the sessions-id pattern almost verbatim. Three divergences are forced by the AC:

1. **`NewServerID` returns `ServerID` (no error).** AC #2. The defensive panic on `rand.Read` failure is a runtime-abort, not a returned error. Document with one short comment.
2. **`ParseServerID` returns `(ServerID, error)`, not a bool.** AC #3 specifies parser semantics. Validation logic is identical to `sessions.ValidID`; the wrapper differs.
3. **No code reuse between packages.** `internal/identity` does NOT import `internal/sessions`. The dependency direction is wrong: sessions are an implementation detail that should be free to import identity later, not the reverse. The duplication is six lines of obvious switch/case — accepted intentionally.

## Why a separate package from `internal/sessions`

`sessions.ServerID` would suggest a per-session id, which is wrong — there is one server-id per binary instance, independent of session lifecycle. The split also keeps `internal/sessions` focused on the supervised-claude lifecycle and frees `internal/identity` to grow with future identifier types (device-id, paired-device-id) without bloating the sessions package.

## Concurrency

`NewServerID` and `ParseServerID` are stateless and safe for concurrent use by definition. `crypto/rand.Read` is goroutine-safe per its package docs.

`LoadOrCreate` is **not** safe for concurrent use against the same path. Two pyry daemons sharing a HOME both observe ENOENT, both mint, both rename — the later rename wins, the earlier loser's id is gone. Two daemons sharing HOME is a misconfiguration; bootstrap is documented as single-call before any fan-out.

## Tests

Same-package, table-driven, `t.Parallel()` everywhere, stdlib only.

`server_id_test.go`:
- `TestNewServerID_Format` — generate one id; assert `len == 36` and matches the canonical regexp `^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$` (tighter than `sessions/id_test.go`'s pattern because `ParseServerID` enforces version + variant).
- `TestNewServerID_Unique` — 1000 iterations, no duplicates. Catches a constant-zero rng wiring bug.
- `TestParseServerID` — table covering valid (variants 8/9/a/b), empty, wrong length, uppercase, wrong version, wrong variant, non-hex, missing dash, dash at wrong position. Negative assertions use `errors.Is(err, ErrInvalidServerID)` to verify the sentinel is reachable.
- `TestNewServerID_RoundTripsParseServerID` — generate → parse → equal.

`store_test.go`:
- `TestLoadOrCreate_FirstRunGeneratesAndPersists` — fresh `t.TempDir() + "/subdir/server-id"` (subdir verifies `MkdirAll`); first call mints, persists, returns; assert file contents are exactly `<id>\n`, parent dir mode `0700`, file mode `0600`; second call returns the same id.
- `TestLoadOrCreate_ExistingFileRoundTripsWithoutRewrite` — pre-seed canonical UUIDv4 + `\n`; capture mtime; call returns the seeded id; bytes byte-identical and `ModTime().Equal(preMtime)`.
- `TestLoadOrCreate_ToleratesNoTrailingNewline` — pre-seed without trailing newline; round-trips, no rewrite.
- `TestLoadOrCreate_CorruptFileReturnsErrInvalidServerID` — table covers `not-a-uuid\n`, empty file, uppercase, leading whitespace, CRLF, double newline. Each row asserts `errors.Is(err, ErrInvalidServerID)`, returned id `""`, **file bytes unchanged** (the no-rewrite-on-corruption invariant).
- `TestLoadOrCreate_ReadFileError` — make `path` itself a directory so `os.ReadFile` returns `EISDIR`; assert non-nil error that is **not** `Is(ErrInvalidServerID)` (I/O errors distinguishable from corruption).

Mode assertions use `info.Mode().Perm()`. Mtime comparison uses `time.Time.Equal`.

## Out of scope (deferred to follow-up tickets)

- **Path resolution.** `~/.pyry/server-id` resolution lives with the config loader (#205). `LoadOrCreate` takes an absolute path.
- **Caller wiring at daemon startup** — Phase-3 wiring ticket calls `LoadOrCreate` from `cmd/pyry/main.go`.
- **`devices.json` persistence** — sibling concern with multiple records; same atomic-write recipe.
- **CLI surface** (`pyry server-id` to print) — defer.
- **Mode tightening for pre-existing parent dirs** — belongs in `pyry install` / setup tooling.
- **JSON round-trip tests** — string newtype, library behavior.
- **Human label suffix** — QR-encoding concern (`protocol-mobile.md`), not an id-type concern.
- **Pairing payload / relay handshake wiring** — Phase 3 tickets.

## Related

- `internal/sessions/id.go` — the precedent (UUIDv4-shaped string newtype, `crypto/rand` generation, canonical-shape validator). Mirrored almost verbatim with three AC-forced divergences above.
- `docs/protocol-mobile.md:61` — wire contract: server-id shape and routing role.
- `docs/protocol-mobile.md:575-583` — security framing: ~122 bits of entropy, `crypto/rand` is not optional.
- [`features/config-package.md`](config-package.md) — Phase 3 sibling foundation slice (`internal/config`, also no consumers wired in its own slice).
