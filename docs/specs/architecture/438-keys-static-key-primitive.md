# Spec — internal/keys: X25519 static keypair primitive (#438)

## Files to read first

The developer's turn-1 reading list. Lift these into context before writing any code.

- `internal/identity/store.go:1-97` — **the shape to clone.** Atomic-write recipe with chmod-tmp-before-write umask defence, fsync-then-rename commit, ENOENT branch detection. Same package's `LoadOrCreate(path)` is the one-arg cousin of this ticket's two-arg `LoadOrCreate(baseDir, daemonName)`.
- `internal/identity/store_test.go:10-200` — the test shapes to mirror: fresh-create asserts mode `0700`/`0600` via `os.Stat`, existing-file path asserts mtime unchanged (no rewrite), corrupt-file table uses `errors.Is` against the sentinel, `ReadFileError` catch-all uses "directory-where-a-file-should-be" trap.
- `internal/identity/server_id.go:1-85` — package-layout precedent: pure type + validator predicate in one file (`server_id.go`), I/O wrapper in another (`store.go`). Doc-comment shape for `NewServerID`'s panic-on-rng-fail discipline (clone for `crypto/rand.Read` here).
- `docs/protocol-mobile.md:93-112` — § *Static keys — binary side*: on-disk JSON schema (the five fields), algorithm name (`Noise_25519`), daemon-name allowlist rationale, `0600` file + `0700` parent invariant. **Note the invariant lines about mode enforcement and `O_NOFOLLOW` belong to the sibling ticket #439, not this one.**
- `docs/knowledge/decisions/024-noise-ik-mobile-e2e.md` — ADR-024 § *Static keys — binary side* and § *Why per-binary, not per-phone, static keys on the binary side*. Anchors the threat model the daemon-name allowlist defends against.
- `docs/PROJECT-MEMORY.md:22` — § Project-level conventions: the canonical *atomic-write recipe* statement (CreateTemp → encode → Sync → Close → Rename, in the same dir). The keys package is the fifth registry against this recipe but per the same memo line, "duplicated until a fifth registry forces extraction" is one-after, so duplication stays here.
- `cmd/pyry/main.go:133-154` — existing `sanitizeName`. **Do NOT clone it.** `sanitizeName` is a transformer that replaces bad chars with `_` and permits `.` and uppercase; the keys package's validator REJECTS instead of transforming, and is stricter on charset. Reusing `sanitizeName` here would silently defeat the path-traversal defence the spec mandates. This callout exists because grep-driven exploration will surface `sanitizeName` and the developer might reach for it.
- `internal/devices/registry.go` — second example of the per-daemon `~/.pyry/<name>/<file>.json` atomic-write pattern (writes `devices.json`). Skim for the `0o600`/`0o700` mode constants and the temp-file naming convention; not load-bearing here, but confirms the pattern is uniform.

## Context

Mobile Protocol v2 (#430) introduces `Noise_IK_25519_ChaChaPoly_BLAKE2s` E2E encryption between phone and binary. The binary owns one persistent X25519 static keypair per daemon, shared across all paired phones for that server-id, persisted under `~/.pyry/<daemon-name>/static_key.json`. The QR pairing payload (#432) emits the public half, and the Noise wrapper (#433) consumes both halves.

This ticket lands the **core primitive only**: the package, the `StaticKey` type, the daemon-name allowlist validator, X25519 keypair generation, the on-disk JSON schema, and the atomic-write at `0600`. The matching **filesystem hardening** (parent-dir mode enforcement at `0700`, post-`MkdirAll` re-stat, existing-file mode rejection at `0600`, `O_NOFOLLOW` on read) is **#439**, a hard prerequisite for any downstream consumer.

The daemon-name allowlist is load-bearing for security: `<daemon-name>` is operator-supplied (via `-pyry-name` / `PYRY_NAME` / `--name`) and the keys package is responsible for refusing any name that could escape `~/.pyry/<daemon-name>/`. The package owns the full `(baseDir, daemonName) → absolute path` mapping; callers pass the two components, never a precomputed path. A caller-precomputed path would bypass the allowlist and is therefore not part of the API.

Precedent for the per-daemon JSON-file pattern and atomic-write recipe: `internal/identity` (server-id), `internal/devices` (paired-device registry), `internal/conversations` (conversation registry), `internal/sessions` (session registry). This is the fifth — per `docs/PROJECT-MEMORY.md` § Project-level conventions, the recipe stays duplicated until a sixth forces extraction. Do not extract a shared helper.

## Design

### Package layout

```
internal/keys/
  static_key.go        Pure type + generator + validator (no I/O)
  static_key_test.go   Tests for the above
  store.go             LoadOrCreate + atomic write + JSON schema marshal/unmarshal
  store_test.go        Tests for LoadOrCreate (fresh-create, round-trip, corrupt-JSON)
```

Two-file split mirrors `internal/identity` (`server_id.go` + `store.go`). Test files use same-package (white-box) so the unexported allowlist validator and JSON-schema marshaller are directly testable.

### Public API

```go
// StaticKey is the binary's persistent X25519 keypair for Mobile Protocol v2.
type StaticKey struct {
    // unexported fields: priv [32]byte, pub [32]byte
}

// PrivateKey returns the raw 32-byte X25519 private scalar.
// Returned by value so callers cannot mutate package-internal state.
//
// SECURITY: callers MUST NOT log, wrap-into-error, or otherwise emit the
// returned bytes. Doc-comment forbids it; package contract.
func (k *StaticKey) PrivateKey() [32]byte

// PublicKey returns the raw 32-byte X25519 public point.
// Safe to log / emit (public half).
func (k *StaticKey) PublicKey() [32]byte

// LoadOrCreate returns the StaticKey stored at <baseDir>/<daemonName>/static_key.json,
// generating and persisting a fresh keypair if the file does not exist.
//
// baseDir is typically <home>/.pyry; daemonName is the operator-supplied
// per-daemon label. daemonName is validated against the package's allowlist
// before any filesystem access — on rejection returns ErrInvalidDaemonName
// wrapped with the offending name and nothing is created on disk.
//
// On first run: parent dir created with mode 0700 if absent, keypair minted
// from crypto/rand via crypto/ecdh.X25519().GenerateKey, JSON encoded at the
// schema documented below, written atomically (sibling temp file in the
// parent dir → chmod 0600 → encode → Sync → Close → Rename), and returned.
//
// On subsequent runs: file is read and decoded; schema-version + algorithm
// constants are checked, base64 fields are decoded to exactly 32 bytes, and
// the stored public_key is verified against the public point recomputed from
// private_key (anti-tamper). Any mismatch returns ErrCorruptKeyFile wrapped
// with the file path; the error message NEVER contains the file contents.
// File is NEVER overwritten on the existing-file path, even on validation
// failure — keys are bound to all paired devices and silent regeneration
// would invalidate every pairing without operator awareness (mirrors the
// existing-file-no-rewrite invariant in internal/identity/store.go:LoadOrCreate).
//
// LoadOrCreate is not safe for concurrent use against the same path;
// bootstrap runs once on daemon startup before any goroutines fan out.
// Same contract as internal/identity.LoadOrCreate.
//
// Filesystem hardening (parent-dir mode 0700 enforcement, file mode 0600
// enforcement on read, O_NOFOLLOW on read) is intentionally NOT in this
// function — it ships in #439 as a follow-up inside this same package.
func LoadOrCreate(baseDir, daemonName string) (*StaticKey, error)

// ErrInvalidDaemonName is returned (wrapped) when daemonName fails the
// allowlist validator. Use errors.Is.
var ErrInvalidDaemonName = errors.New("keys: invalid daemon name")

// ErrCorruptKeyFile is returned (wrapped) when an existing static_key.json
// fails to decode, fails schema validation, or whose public_key disagrees
// with the public point recomputed from private_key.
var ErrCorruptKeyFile = errors.New("keys: corrupt static key file")
```

No other exported symbols. The allowlist validator is unexported (`validDaemonName(string) bool`) and tested via same-package matrix.

### Daemon-name allowlist

Predicate `validDaemonName(s string) bool`. Mirrors `internal/identity.validUUIDv4` in shape — character-by-character scan, returning false on first violation, no regex.

Rules (each one is independently asserted by a test row):

| Rule | Rationale |
|---|---|
| `len(s) >= 1` | empty rejects path `~/.pyry//static_key.json` |
| `len(s) <= 64` | defense-in-depth — far below filesystem max (255) but big enough for any realistic daemon name |
| each byte ∈ `[a-z0-9_-]` | rejects `/`, `\`, `.`, `..`, uppercase, whitespace, NUL, every multi-byte UTF-8 byte. ASCII-only by design — a name appearing in a filesystem path is not a place for unicode |
| `s[0] != '-'` | rejects argv-injection shape (`--evil`) at the boundary; cheap, no downside |

Test matrix (white-box, same-package; not full Go code — the developer writes the table):

| Input | Verdict | Specific rule tripped |
|---|---|---|
| `""` | reject | empty |
| `"."` | reject | `.` not in charset |
| `".."` | reject | `.` not in charset |
| `"foo/bar"` | reject | `/` not in charset |
| `"foo/../bar"` | reject | `/` not in charset (first match wins) |
| `"foo\\bar"` | reject | `\` not in charset |
| `"Foo"` | reject | `F` uppercase |
| `"foo.bar"` | reject | `.` not in charset |
| `"-leading"` | reject | leading hyphen |
| `"foo bar"` | reject | space not in charset |
| `"foo\x00bar"` | reject | NUL not in charset |
| 65 × `"a"` | reject | length cap |
| `"default"` | accept | — |
| `"prod"` | accept | — |
| `"dev-1"` | accept | hyphen non-leading |
| `"my_daemon"` | accept | underscore |
| `"0abc"` | accept | digit-prefix is fine |
| `"a"` | accept | single char |
| `"a-1_b"` | accept | mixed |

On reject: `LoadOrCreate` returns `fmt.Errorf("keys: invalid daemon name %q: %w", daemonName, ErrInvalidDaemonName)` and **does not touch the filesystem** (assertion: post-reject `os.Stat(baseDir)` is unchanged from pre-call state, AND `os.Stat(filepath.Join(baseDir, daemonName))` is `fs.ErrNotExist`).

### Path construction

After validation passes:

- `dir := filepath.Join(baseDir, daemonName)`
- `path := filepath.Join(dir, "static_key.json")`
- File constant: `const filename = "static_key.json"` (lower_snake to match `sessions.json` / `devices.json` / `conversations.json`).

`baseDir` is trusted (caller's responsibility; typically `filepath.Join(home, ".pyry")`). The package does no validation on `baseDir` and does no `~` expansion — the caller resolves home. **Doc-comment must state this explicitly** so a confused caller doesn't pass e.g. `baseDir = "/"` with `daemonName = "etc"` and discover the package will happily write `/etc/static_key.json` if running as root. The allowlist defends against `daemonName` injection, NOT against an attacker-controlled `baseDir`.

### JSON schema

Locked to the spec in `docs/protocol-mobile.md:101-110`:

```json
{
  "version":     1,
  "algorithm":   "Noise_25519",
  "private_key": "<base64 standard, 32 raw bytes → 44 chars with padding>",
  "public_key":  "<base64 standard, 32 raw bytes → 44 chars with padding>",
  "created_at":  "<RFC 3339 UTC, e.g. 2026-05-16T08:00:00Z>"
}
```

Constants:

```go
const (
    schemaVersion  = 1
    algorithmName  = "Noise_25519"
    filename       = "static_key.json"
)
```

Encoding choices:

- **Base64:** `encoding/base64.StdEncoding` (with padding). 32 bytes → 44 chars. Not URL-safe; this is on-disk JSON, not a URL/QR. Matches what most Noise impls emit; opposite of `internal/pair`'s `RawURLEncoding` (which is correct for QR / wire). Worth pinning explicitly because two base64 flavours in the project will confuse the developer.
- **Time:** `time.Time` field marshalled via `time.Time.MarshalJSON` which emits RFC 3339; explicit `t.UTC().Format(time.RFC3339)` on write to defeat monotonic-clock retention (per the `time.Time` round-trip discipline in `docs/PROJECT-MEMORY.md:21`). Load goes through `time.Parse(time.RFC3339, …)`.
- **JSON indent:** none — `json.Marshal` (no `MarshalIndent`). The file is for the daemon, not for humans, and unindented JSON is the project default.
- **Trailing newline:** none. The file is JSON, not a UUID string; trailing whitespace is not idiomatic for `static_key.json` (unlike `internal/identity`'s server-id file which is a bare string).

Load-side validation order (fail-fast, all returning `ErrCorruptKeyFile`-wrapped errors):

1. JSON unmarshal — reject syntax errors.
2. `version == schemaVersion` — reject future or zero versions.
3. `algorithm == algorithmName` — reject suite mismatch.
4. `base64.StdEncoding.DecodeString(private_key)` succeeds and length is exactly 32.
5. `base64.StdEncoding.DecodeString(public_key)` succeeds and length is exactly 32.
6. `time.Parse(time.RFC3339, created_at)` succeeds.
7. **Public-key consistency:** `crypto/ecdh.X25519().NewPrivateKey(priv[:]).PublicKey().Bytes()` equals stored `pub[:]` via `crypto/subtle.ConstantTimeCompare` (not strictly needed for equality of public material, but it's cheap and consistent with the project's hygiene posture).

Error messages: include the file path, NEVER include any field value. Test row: seed a file with a known base64 private key, trigger a corruption (e.g. mutate the algorithm field), assert the returned error string does NOT contain the known private-key base64 (substring check).

### Generation

Use **stdlib `crypto/ecdh`** (not `flynn/noise`). Rationale:

- Pure stdlib; no new dependency for a primitive.
- `crypto/ecdh.X25519().GenerateKey(rand.Reader)` returns a `*ecdh.PrivateKey` whose `.Bytes()` and `.PublicKey().Bytes()` are raw 32-byte X25519 material — bit-for-bit wire-compatible with `flynn/noise.DHKey{Private, Public [32]byte}`. The downstream Noise wrapper (#433) consumes the raw bytes; it does not need a Go-level type bridge.
- If `crypto/rand.Reader` fails (documented infallible on Linux/macOS), `panic` with a wrapped error — mirrors `internal/identity.NewServerID`'s panic-on-rng-fail discipline. Silent zero-entropy keys would break the entire authentication model.

Generator helper (signature only; body is ~6 lines):

```go
func newStaticKey() (*StaticKey, error)
```

Returns a `*StaticKey` with `priv` and `pub` populated from a fresh `ecdh.GenerateKey`. Called from the `LoadOrCreate` write path; not exported.

### Atomic write

Clone `internal/identity/store.go:writeServerID` (lines 66-96) verbatim in shape, substituting the JSON body:

1. `os.MkdirAll(dir, 0o700)` — parent directory.
2. `f, err := os.CreateTemp(dir, ".static-key-*.tmp")` — sibling temp file (rename atomicity requires same directory).
3. `defer os.Remove(tmp)` — cleanup if any later step fails before rename.
4. `os.Chmod(tmp, 0o600)` — **explicit chmod before write** to defeat umask on macOS (per `internal/identity/store.go:77` and the same umask-defence note in `docs/PROJECT-MEMORY.md`).
5. `f.Write(jsonBytes)` — `json.Marshal` of the `onDiskKey` struct.
6. `f.Sync()` — fsync.
7. `f.Close()` — release fd before rename.
8. `os.Rename(tmp, path)` — atomic commit point.

Failure at any step returns a `keys:` prefixed wrapped error. No partial state is ever observable at `path` (rename is the commit; partial bytes live only in the `.tmp` sibling and the `defer os.Remove` cleans them up).

Concurrency: same as identity — not safe for concurrent calls on the same path. Document this in the doc-comment.

### Data-flow sketch

```
LoadOrCreate(baseDir, daemonName)
    │
    ├── validDaemonName(daemonName)? ─ no ─→ return ErrInvalidDaemonName (no filesystem access)
    │       yes
    ├── dir  = baseDir / daemonName
    ├── path = dir / static_key.json
    │
    ├── os.ReadFile(path)
    │     │
    │     ├── ok  ─→ unmarshal → schema-validate → derive-public-check → return *StaticKey
    │     │            │
    │     │            └── any check fails → ErrCorruptKeyFile (file NOT mutated)
    │     │
    │     ├── ErrNotExist ─→ generate → atomicWrite → return *StaticKey
    │     │
    │     └── other I/O err ─→ return wrapped error (NOT ErrCorruptKeyFile)
```

The three-way switch on `os.ReadFile` error matches `internal/identity/store.go:38-46` verbatim. The "I/O error is not corruption" distinction is preserved so an operator can tell from the sentinel whether the file is bad or unreachable.

## Concurrency model

None within the package. `LoadOrCreate` runs once, synchronously, on daemon startup (the call site is `pyry pair` and — in a future ticket — daemon bootstrap before any fan-out). No goroutines, no channels, no locks.

Concurrent calls against the same `(baseDir, daemonName)` from two processes are a misconfiguration outside the package's contract; two pyry daemons sharing the same `~/.pyry/<daemon-name>/` is a wider misconfiguration that affects `sessions.json`, `devices.json`, `server-id`, etc. — not a concern this package addresses.

## Error handling

Three sentinel categories, all `errors.Is`-matchable:

| Sentinel | Returned when | Operator action |
|---|---|---|
| `ErrInvalidDaemonName` | `daemonName` fails the allowlist | Fix the `-pyry-name` flag / `PYRY_NAME` env / `--name` arg |
| `ErrCorruptKeyFile` | Existing `static_key.json` fails JSON decode, schema check, or public/private mismatch | Investigate (operator-tampering? disk corruption?). Manual remediation: delete the file to regenerate; **all paired devices will be invalidated** |
| Bare wrapped `fmt.Errorf` (no sentinel) | Filesystem I/O errors (permission, disk full, ENOTDIR, etc.) | Check filesystem; operator-specific |

Error messages: prefix `"keys: "`. Include the path; NEVER include base64 fields, decoded bytes, or the offending file contents. The pyrycode-relay precedent (and `internal/identity:38-46`) is the shape.

Doc-comment SECURITY note on the package and on `StaticKey.PrivateKey()`: "The returned bytes are the X25519 static secret. Never log, never wrap-into-error, never emit on a wire. Compromise of these bytes lets any holder impersonate this binary to every paired phone." (Mirror `internal/devices`'s package-level SECURITY contract.)

## Testing strategy

Same-package (white-box) tests, table-driven, `testing` stdlib only. File layout: `static_key_test.go` for type + allowlist; `store_test.go` for `LoadOrCreate`.

Bullet-pointed scenarios — developer writes idiomatic Go test code:

### `static_key_test.go`

- **TestValidDaemonName_AllowlistMatrix.** Table-driven; rows per the "Daemon-name allowlist" table above. Asserts `validDaemonName(in) == want`.
- **TestNewStaticKey_PublicKeyMatchesPrivate.** Calls `newStaticKey()` (unexported), asserts that `crypto/ecdh.X25519().NewPrivateKey(priv[:]).PublicKey().Bytes()` equals `pub[:]`.
- **TestNewStaticKey_KeysAreNonZero.** Calls `newStaticKey()` twice; asserts both keys' private + public are non-zero (smoke against accidentally-defaulted-struct bugs) and that the two calls produce distinct keys (smoke against accidentally-deterministic RNG).
- **TestStaticKey_AccessorsReturnByValue.** Calls `k.PrivateKey()`, mutates the returned `[32]byte`, then calls `k.PrivateKey()` again and asserts the new return is unchanged — pins the by-value contract.

### `store_test.go`

- **TestLoadOrCreate_FreshCreate.** `dir := t.TempDir()`; `LoadOrCreate(dir, "test")`; assert returned `*StaticKey` is non-nil; assert `<dir>/test/` exists with mode `0700`; assert `<dir>/test/static_key.json` exists with mode `0600`; assert JSON parses, version=1, algorithm="Noise_25519", private+public base64-decode to 32 bytes each; assert public matches recomputed-from-private.
- **TestLoadOrCreate_RoundTripStable.** Mirror `TestLoadOrCreate_ExistingFileRoundTripsWithoutRewrite` from identity: seed a file via first `LoadOrCreate`, record mtime, second `LoadOrCreate`, assert returned public key matches the first, assert mtime unchanged (no rewrite on the load path).
- **TestLoadOrCreate_InvalidDaemonName.** Table-driven over the reject rows from the allowlist matrix. For each, assert `errors.Is(err, ErrInvalidDaemonName)`, assert returned `*StaticKey` is nil, AND assert `os.Stat(filepath.Join(baseDir, daemonName))` returns `fs.ErrNotExist` (no directory created on the reject path). For inputs that contain `/` like `"foo/bar"`, also assert no `<baseDir>/foo` directory was created.
- **TestLoadOrCreate_CorruptJSONReturnsSentinel.** Table-driven. For each row, write the bad-content fixture into `<dir>/test/static_key.json` (manually, bypassing `LoadOrCreate`'s write path) and assert `errors.Is(err, ErrCorruptKeyFile)`, returned key is nil, AND the file on disk is byte-identical to the seeded fixture (no mutation on the load path).
  - Rows: `"not json"`; missing closing brace; `version: 2`; `version: 0`; `algorithm: "X25519"` (wrong name); private_key is not base64 (`"@@@"`); private_key decodes to wrong length (e.g. base64 of 16 bytes); public_key wrong length; public_key valid base64 of 32 bytes but does NOT match the public recomputed from private_key (anti-tamper); `created_at` is not RFC 3339 (`"yesterday"`).
- **TestLoadOrCreate_CorruptJSONErrorDoesNotLeakPrivateKey.** Seed a fixture whose `private_key` is a known base64 string (e.g. `base64(0x01 repeated 32 times)`); mutate `algorithm` to trigger reject; assert the returned error's `err.Error()` string does NOT contain that base64 substring. Single defensive assertion against future log/error refactors.
- **TestLoadOrCreate_NonENOENTReadErrorIsNotCorruption.** Mirror `TestLoadOrCreate_ReadFileError` from identity: make the keystore path itself a directory (`os.Mkdir(<dir>/test/static_key.json, 0o700)`), assert err is non-nil, NOT `errors.Is(ErrCorruptKeyFile)`, and the returned key is nil. Pins the I/O-vs-corruption sentinel distinction.

No production code path logs the private key. Tests are not asked to enforce this globally (there's no production logging in this primitive at all); the leak-resistance test above pins the error-message channel.

## Open questions

None. Every ambiguity in the issue body resolves to a single defensible choice in the Design section above; the architect-question pattern that would have surfaced "ecdh vs flynn/noise" or "padded vs raw base64" has been collapsed by the rationales given inline.

## Out of scope (filed elsewhere or deferred)

- **Filesystem hardening** — parent-dir mode `0700` rejection, post-`MkdirAll` re-stat, existing-file mode `0600` rejection, `O_NOFOLLOW` on read, symlink-swap defence. **Filed as #439**, hard prerequisite for any production consumer.
- **QR payload extension** (`server_static_pubkey` field) — #432.
- **Noise handshake wrapper** consuming `StaticKey.PrivateKey()` — #433.
- **Key rotation verb** (`pyry rotate-static-key`) — v3 concern per `docs/protocol-mobile.md:112`.
- **Hardware-backed key storage** (Keychain / Keystore on the binary side) — v3 concern.
- **In-memory zeroisation of the private-key bytes after Noise consumes them.** Go does not give reliable zeroisation primitives (GC may copy the array, `unsafe`-based `memclr` is not idiomatic, and the bytes live in the `*StaticKey` for the daemon's lifetime anyway). Defence-in-depth against process-memory dumps belongs with hardware-backed storage (above), not with this primitive.
- **Aligning the rest of the codebase's `sanitizeName` (cmd/pyry/main.go:137) to this stricter validator.** `sanitizeName` is a transformer that is too permissive for keystore paths; this ticket does not retroactively tighten it for `sessions.json` / `devices.json` / `conversations.json` paths. A future hardening ticket may consolidate the two, but is not blocked-by anything here.
- **Caller wiring in `pyry pair`** (call site that invokes `keys.LoadOrCreate` and feeds the public half into `internal/pair.Payload`). That's the integration ticket on top of #432 + #439 + this one.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** Two boundaries, both explicit and single-point.
  1. **Operator → process** at the `daemonName` argument of `LoadOrCreate`: untrusted string crosses into the path-construction layer. Boundary is `validDaemonName(daemonName)` called as the first statement of `LoadOrCreate`, before any filesystem access. Allowlist is restrictive (lowercase alphanum + `-` + `_`, no leading `-`, length 1-64). Test matrix above pins every named threat (`..`, `/`, `\`, `.`, uppercase, leading-hyphen, empty, NUL byte, oversize).
  2. **Disk → process** at `os.ReadFile` of `static_key.json`: bytes from disk cross into the in-memory `StaticKey`. Boundary is the seven-step schema validation in `Design § JSON schema` (JSON, version, algorithm, base64+length, time-parse, public-derivation match). Each step returns `ErrCorruptKeyFile` on failure with the file path but NOT the contents. Downstream code (`*StaticKey` consumers) holds only the 32-byte private + 32-byte public arrays; no untrusted strings flow further.
- **[Tokens, secrets, credentials]**
  - Generated via `crypto/ecdh.X25519().GenerateKey(rand.Reader)` — `crypto/rand`, full entropy. RNG-failure panics (mirrors `identity.NewServerID`).
  - Stored plaintext on disk at `0600` (file) + `0700` (parent dir). Plaintext-at-rest is the spec's decision (`docs/protocol-mobile.md:98` mandates `0600`/`0700` as the storage choice; hardware-backed storage is a v3 concern explicitly out of scope). This ticket is responsible for the `0600` write; **#439** is responsible for the read-side mode rejection. The threat model that justifies plaintext-at-rest: an attacker with arbitrary read of the operator's UID's filesystem has already won (they hold every paired-device token via `devices.json` and the server-id via `server-id`). The keystore is no easier or harder a target than those siblings.
  - **No appearance in logs**: package doc-comment forbids it; no `slog` calls in the package (production code is loadless); error messages forbid it (test `TestLoadOrCreate_CorruptJSONErrorDoesNotLeakPrivateKey` pins this).
  - **Lifecycle:** creation = first `LoadOrCreate`; storage = `static_key.json`; rotation = explicitly out of scope (v3); revocation = out of scope (rotation invalidates all paired devices, but the verb is v3). The ticket does NOT regress any of these — the rotation/revocation hooks aren't here yet because they aren't supposed to be.
- **[File operations]**
  - **Path traversal** — defended at the `validDaemonName` boundary. The validator rejects `..`, `/`, `\`, `.`, NUL, and any path separator before `filepath.Join` runs. `filepath.Join`'s own cleaning logic is a second line of defence but is NOT relied upon (the validator is the first and authoritative gate).
  - **TOCTOU** — not introduced in this ticket. The check-then-use pattern (`os.Stat` then `os.OpenFile`) belongs to #439's read-path hardening (mode check + `O_NOFOLLOW` open). This ticket's read is a single `os.ReadFile`; there is no check-then-use gap to exploit. The write path uses `CreateTemp` + `Rename` which is itself TOCTOU-safe (the rename is the commit point; no `Stat` interleaves).
  - **Permissions** — file `0600` enforced via `os.Chmod(tmp, 0o600)` BEFORE write (umask defence), then `Rename`. Parent dir `0700` via `os.MkdirAll(dir, 0o700)`. **The post-`MkdirAll` re-stat (defends against umask collapsing the requested mode) is intentionally NOT here — it's filed as part of #439's hardening pass.** This is SHOULD FIX, not MUST FIX, because (a) #439 is named as a hard prerequisite in this ticket's body and in the spec's Out of scope section, (b) the issue body explicitly carves it out, (c) downstream consumers (#432, #433) are blocked on #439 landing, so the production exposure window is zero.
  - **Symlink handling** — `os.ReadFile` follows symlinks by default. Defence is **deferred to #439** (which adds `O_NOFOLLOW` on the read path). Same SHOULD FIX status with the same rationale — no production consumer reads `static_key.json` until #439 lands.
  - **Atomic writes** — yes, `CreateTemp` → `Chmod` → `Write` → `Sync` → `Close` → `Rename`. The atomic commit point is `Rename`. `defer os.Remove(tmp)` cleans up partial state on any earlier failure. Mirrors the project-wide convention in `docs/PROJECT-MEMORY.md:22`.
- **[Subprocess / external command execution]** N/A. The package executes no subprocess and does not pass `daemonName` (or anything else) to `exec.Command`. No shell-out, no env-var inheritance concerns at this layer.
- **[Cryptographic primitives]**
  - **RNG:** `crypto/rand` via `crypto/ecdh.X25519().GenerateKey(rand.Reader)`. Panic on RNG failure (mirrors identity).
  - **Primitives:** stdlib `crypto/ecdh` (X25519), no hand-rolled crypto. Algorithm constant `"Noise_25519"` is a wire-format name; the on-the-wire bytes are interoperable bit-for-bit with `flynn/noise`'s `DHKey`.
  - **Key reuse:** N/A — one keypair per daemon, never reused for a different purpose. The Noise wrapper (#433) will derive ephemeral keys per-session from `crypto/rand` independently.
  - **Constant-time comparison:** used in the public-key-consistency check (`crypto/subtle.ConstantTimeCompare(stored_pub, derived_pub)`). Strictly unnecessary for equality of public material, but consistent with project hygiene (`internal/devices.VerifyToken`).
  - **In-memory zeroisation:** the private-key `[32]byte` is not zeroed when `*StaticKey` becomes garbage. Go does not provide reliable zeroisation. Out of scope here (named in the Out of scope section); defence belongs with v3's hardware-backed key storage path.
- **[Network & I/O]** N/A. No network, no `http.Server`, no inbound bytes from a socket. Input size: `os.ReadFile` reads the entire file into memory. **An attacker who can write to `~/.pyry/<daemonName>/static_key.json` with the operator's UID has already escalated past this defence layer.** No cap is meaningful here (the disk-write threat model is a different posture — full filesystem access — and even a hypothetical 2GiB junk file would fail JSON unmarshal in O(file size) memory, surfacing as `ErrCorruptKeyFile`). Logging an `slog.Warn` with file size on parse failure would be operator-friendly but is downstream of `LoadOrCreate`; not in scope for the primitive.
- **[Error messages, logs, telemetry]**
  - Error messages include the file path (operator-actionable); NEVER include base64-encoded fields, decoded bytes, or file contents.
  - Logs: package emits no log calls. Doc-comment forbids future log additions for the private half.
  - Telemetry: N/A.
- **[Concurrency]** N/A. Single-threaded, called once on startup. The "not safe for concurrent use on the same path" contract is documented on `LoadOrCreate`. No locks, no goroutines, no shared mutable state.
- **[Threat model alignment]**
  - `docs/protocol-mobile.md` § *Static keys — binary side* threats:
    - "An untrusted daemon name MUST NOT be able to redirect the read/write to a different daemon's key file." → **addressed** by the allowlist boundary.
    - File mode `0600` enforcement and `O_NOFOLLOW` symlink defence and parent-dir `0700` enforcement → **out of scope here, filed as #439.** Named explicitly in the spec's Out of scope section so the linkage is unambiguous; the issue body also names #439 as a hard prerequisite for production consumers, so the gap is structurally bounded.
    - "Private static key MUST NOT be logged" (`docs/protocol-mobile.md:707`, security review row) → **addressed** by package doc-comment and `TestLoadOrCreate_CorruptJSONErrorDoesNotLeakPrivateKey`.
  - ADR-024 (Noise_IK rationale): no findings specific to this ticket beyond what's above.

**Reviewer:** architect (self-review per `agents/architect/security-review.md`)
**Date:** 2026-05-16
