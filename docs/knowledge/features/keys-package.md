# `internal/keys` — binary-side X25519 static keypair

Owns the binary's persistent X25519 static keypair for [Mobile Protocol v2](../../protocol-mobile.md) (Noise_IK). One keypair per pyrycode daemon, shared across every paired phone for that server-id, persisted at `~/.pyry/<daemon-name>/static_key.json` and minted on first run. The QR pairing payload publishes the public half; the Noise wrapper consumes both halves to authenticate the binary cryptographically to the phone through the stateless relay.

This package owns the `(baseDir, daemonName) → absolute path` mapping behind a restrictive allowlist, because the daemon-name component is operator-supplied and untrusted with respect to path traversal.

## Surface

```go
type StaticKey struct{ /* unexported */ }

func (k *StaticKey) PrivateKey() [32]byte
func (k *StaticKey) PublicKey()  [32]byte

func LoadOrCreate(baseDir, daemonName string) (*StaticKey, error)

var ErrInvalidDaemonName   = errors.New("keys: invalid daemon name")
var ErrCorruptKeyFile      = errors.New("keys: corrupt static key file")
var ErrInsecureKeyDirMode  = errors.New("keys: insecure key directory mode")
var ErrInsecureKeyFileMode = errors.New("keys: insecure key file mode")
```

Seven exports. Construct `StaticKey` only via `LoadOrCreate` — there is no public constructor; the type is opaque on purpose so the private bytes have a single ingress path.

## SECURITY contract

The `[32]byte` returned by `PrivateKey()` is the X25519 static secret. Compromise lets the holder impersonate this binary to every paired phone. Callers MUST NOT:

- log it (no `slog` calls, no `fmt.Printf`),
- wrap it into an error string (no `fmt.Errorf("…: %x", priv)`),
- emit it on a wire (the public half is the wire half).

Pinned in the package doc-comment + `StaticKey.PrivateKey()` doc-comment. Mirrors the same contract `internal/devices` imposes on plaintext device-tokens.

## Accessors return by value

`PrivateKey()` and `PublicKey()` return `[32]byte`, not `[]byte` and not pointers. Callers receive a copy; mutating the returned array does not affect package-internal state. Pinned by `TestStaticKey_AccessorsReturnByValue`. Costs 64 bytes per call (32 on the wire to copy in + 32 to copy out); microseconds at startup, no per-frame cost.

## `LoadOrCreate(baseDir, daemonName)` — first-run bootstrap

```go
func LoadOrCreate(baseDir, daemonName string) (*StaticKey, error)
```

The full I/O lifecycle for the daemon's static key. Runs once at daemon startup, returns a `*StaticKey`, never called again. `baseDir` is typically `filepath.Join(home, ".pyry")` (caller resolves home); `daemonName` is operator-supplied via `-pyry-name` / `PYRY_NAME` / `--name`.

**`daemonName` is validated against the allowlist before any filesystem access** — on reject the function returns `ErrInvalidDaemonName` (wrapped) and the filesystem is untouched. The package does NOT validate `baseDir`; an attacker-controlled `baseDir` is outside the threat model (`baseDir = "/"` with `daemonName = "etc"` would happily write `/etc/static_key.json` if running as root — the allowlist defends against `daemonName` injection only).

**Before any I/O** (`ensureSecureKeyDir`): the parent directory is stat'd. If it doesn't exist, `MkdirAll(dir, 0o700)` runs followed by a re-stat (defensive against default ACLs and exotic filesystems widening the requested mode). If the path exists but is not a directory, or any group/other bit is set (`mode & 0o077 != 0`), the function returns `ErrInsecureKeyDirMode` wrapped with the path and the observed mode in octal. No auto-`chmod`.

**First run (path missing):** keypair minted via `ecdh.X25519().GenerateKey(rand.Reader)`, encoded to the JSON schema below, written atomically to a sibling temp file in the parent dir (chmod'd to `0600` before write to defeat umask), fsync'd, closed, renamed into place. Rename is the commit point; partial state lives only in the `.static-key-*.tmp` sibling and the deferred `os.Remove` cleans it up on any earlier failure.

**Subsequent runs (path present):** `os.Lstat` the file (NOT `Stat` — a symlink under the key path must not be transparently followed during the mode check); reject anything but a regular file with exactly mode `0600` (`ErrInsecureKeyFileMode`); then `os.OpenFile(path, O_RDONLY|syscall.O_NOFOLLOW, 0)`, `io.ReadAll`, `json.Unmarshal`, seven-step schema validation (see below). The file is **never** rewritten on the load path, even on validation failure.

### Daemon-name allowlist

`validDaemonName(s string) bool` — unexported, white-box tested via `TestValidDaemonName_AllowlistMatrix`:

| Rule | Rationale |
|---|---|
| `len(s) >= 1` | empty would compose `~/.pyry//static_key.json` |
| `len(s) <= 64` | defence-in-depth below the filesystem max (255) |
| each byte ∈ `[a-z0-9_-]` | rejects `/`, `\`, `.`, `..`, uppercase, whitespace, NUL, every multi-byte UTF-8 byte. ASCII-only by design |
| `s[0] != '-'` | rejects argv-injection shape (`--evil`) at the boundary |

Character-by-character scan, no regex. Returns on first violation.

Reject behaviour: returns `fmt.Errorf("keys: invalid daemon name %q: %w", daemonName, ErrInvalidDaemonName)`, returns `nil` for the `*StaticKey`, performs **zero** filesystem operations. Pinned by `TestLoadOrCreate_InvalidDaemonName`, which asserts `baseDir` has zero entries after every reject row (including reject inputs like `"foo/bar"` that would otherwise create a `foo/` directory if `filepath.Join` were reached).

### Deliberately NOT shared with `cmd/pyry/main.go:sanitizeName`

`sanitizeName` is a transformer that replaces bad chars with `_` and permits `.` and uppercase. The keystore validator rejects on the same inputs and is stricter on charset. Reusing `sanitizeName` for the keystore path would silently defeat the path-traversal defence (`sanitizeName("..") == ".."`). The two surfaces solve different problems and stay separate.

### JSON schema

Locked to [`docs/protocol-mobile.md`](../../protocol-mobile.md) § Static keys — binary side:

```json
{
  "version":     1,
  "algorithm":   "Noise_25519",
  "private_key": "<base64 standard, 32 raw bytes → 44 chars with padding>",
  "public_key":  "<base64 standard, 32 raw bytes → 44 chars with padding>",
  "created_at":  "2026-05-16T08:00:00Z"
}
```

Constants in `store.go`:

```go
const (
    schemaVersion = 1
    algorithmName = "Noise_25519"
    filename      = "static_key.json"
)
```

Encoding choices:

- **`base64.StdEncoding`** (padded, not URL-safe). Opposite of `internal/pair`'s `RawURLEncoding`, which is right for QR/wire but wrong for on-disk JSON. Padded encoding matches what most Noise implementations emit and is the format reviewers expect.
- **RFC 3339 UTC** time via `time.Time.MarshalJSON`. Explicit `time.Now().UTC()` on write strips monotonic-clock readings per the project's `time.Time` round-trip discipline.
- **Unindented JSON.** `json.Marshal`, not `MarshalIndent`. The file is for the daemon, not for humans.
- **No trailing newline.** The file is JSON, not a bare string like `internal/identity`'s `server-id`.

### Load-side validation (fail-fast, all `ErrCorruptKeyFile`)

1. `json.Unmarshal` syntax check.
2. `version == 1`.
3. `algorithm == "Noise_25519"`.
4. `base64.StdEncoding.DecodeString(private_key)` succeeds + decoded length is exactly 32.
5. `base64.StdEncoding.DecodeString(public_key)` succeeds + decoded length is exactly 32.
6. `created_at` is non-zero (`time.Time.IsZero()` — `encoding/json` already rejects non-RFC3339 strings at step 1).
7. `ecdh.X25519().NewPrivateKey(priv).PublicKey().Bytes()` matches stored `public_key` (`crypto/subtle.ConstantTimeCompare`). Catches operator-tampering / disk-corruption that mutates one half without the other.

Each step returns `fmt.Errorf("keys: %s: <reason>: %w", path, ErrCorruptKeyFile)`. Error messages include the path (operator-actionable) and **never** include base64 fields, decoded bytes, or file contents. `TestLoadOrCreate_CorruptJSONErrorDoesNotLeakPrivateKey` pins this.

### Three-way `os.Lstat` switch

```go
fi, err := os.Lstat(path)
switch {
case err == nil:
    if mode := fi.Mode().Perm(); mode != 0o600 {
        return nil, fmt.Errorf("keys: %s: mode %#o: %w", path, mode, ErrInsecureKeyFileMode)
    }
    raw, err := openSecureKeyFile(path) // O_NOFOLLOW
    if err != nil {
        return nil, err
    }
    return parsePersisted(path, raw)
case errors.Is(err, fs.ErrNotExist):
    return mintAndPersist(dir, path)
default:
    return nil, fmt.Errorf("keys: stat %s: %w", path, err)
}
```

I/O errors are distinguishable from corruption and from mode-rejection — callers can branch on `errors.Is(err, ErrCorruptKeyFile)`, `…ErrInsecureKeyFileMode`, `…ErrInsecureKeyDirMode`, `…ErrInvalidDaemonName` without false positives between them. `TestLoadOrCreate_NonENOENTReadErrorIsNotCorruption` traps this by making `static_key.json` itself a directory; the trap now surfaces as `ErrInsecureKeyFileMode` (Lstat reports the directory's `0700` mode ≠ `0600`) before the read happens — neither `ErrCorruptKeyFile` nor `ErrInvalidDaemonName`, which is what the test asserts.

### Corruption is operator-escalated, never auto-regenerated

A file that fails validation returns `ErrCorruptKeyFile` (wrapped) and **the file on disk is byte-identical post-call** (pinned by `TestLoadOrCreate_CorruptJSONReturnsSentinel`). Silent regeneration would mint a fresh public key and invalidate every paired phone without operator awareness — the same invariant `internal/identity.LoadOrCreate` upholds for `server-id`. Manual remediation: delete the file to regenerate; **all paired devices will be invalidated** (no key rotation verb exists in v2).

### Atomic-write recipe

`os.MkdirAll(dir, 0o700)` → `os.CreateTemp(dir, ".static-key-*.tmp")` → **`os.Chmod(tmp, 0o600)` before writing data** → `f.Write(json.Marshal(d))` → `f.Sync()` → `f.Close()` → `os.Rename(tmp, path)`. `defer os.Remove(tmp)` cleans up partial state on any earlier failure.

The chmod-before-write defends against macOS umask collapsing the requested mode — the same defence in `internal/identity/store.go:77`. `os.CreateTemp` already defaults to `0600` on Unix; the explicit `Chmod` is belt-and-suspenders against future Go-stdlib changes.

### Concurrency

Not safe for concurrent use against the same path. Bootstrap runs once at daemon startup before any goroutines fan out. Two pyry daemons sharing a HOME is a wider misconfiguration that affects `sessions.json`, `devices.json`, `server-id`, etc. — not a concern this package addresses. Same contract as `internal/identity.LoadOrCreate`.

## Filesystem hardening (parent-dir mode, file mode, `O_NOFOLLOW`)

Landed in [#439](../codebase/439.md) on top of the #438 primitive. `LoadOrCreate` refuses to start the daemon when:

- the parent directory has any group/other readable/writable/executable bit set (`mode & 0o077 != 0`) — returns `ErrInsecureKeyDirMode`. The same sentinel covers the "path exists but is not a directory" case (operator fix is the same: remove the imposter, re-run).
- `static_key.json` has any mode other than exactly `0600` — returns `ErrInsecureKeyFileMode`. The strict equality (rather than the dir's "no g/o bits") is intentional: the package writes the file at exactly `0600`, so anything else (including narrower) indicates tampering.
- a between-`Lstat`-and-open symlink swap would otherwise redirect the read — `os.OpenFile(path, O_RDONLY|syscall.O_NOFOLLOW, 0)` returns `ELOOP`.

Two layers, two fabrics: `Lstat` catches the symlink-at-rest case (a symlink's own mode is `0755`/`0777`, fails the `== 0600` check); `O_NOFOLLOW` catches the dynamic swap during the TOCTOU window. The directory check on entry catches the parent-mode case before any read or write touches the filesystem. Wrapped error messages name the path and the observed mode in `%#o` and nothing else — never the file contents, never a resolved symlink target.

**No auto-`chmod` ever.** The loud-failure contract is the security feature; auto-repair would hide packaging bugs and hand a hostile process under the same UID the moment of vulnerability they need. The operator chmods by hand and restarts.

`syscall.O_NOFOLLOW` is the canonical spelling on both Linux and macOS — no `golang.org/x/sys/unix` import.

## Why `crypto/ecdh` over `flynn/noise`

`crypto/ecdh.X25519().GenerateKey(rand.Reader)` returns a `*ecdh.PrivateKey` whose `.Bytes()` and `.PublicKey().Bytes()` are bit-for-bit wire-compatible with `flynn/noise.DHKey{Private, Public [32]byte}`. The Noise wrapper (#433) consumes the raw 32-byte arrays; it does not need a Go-level type bridge. Choosing stdlib avoids a new dependency for a primitive that's already implementable in the standard library.

`crypto/rand.Read` is documented infallible on supported platforms (Linux/macOS). If the system RNG is unavailable `newStaticKey` panics with a wrapped error — mirrors `identity.NewServerID`'s panic-on-rng-fail discipline. Silent zero-entropy keys would break the entire Noise_IK authentication model.

## Tests

Same-package (white-box) so the unexported `validDaemonName`, `newStaticKey`, `onDiskKey`, and `schemaVersion`/`algorithmName` constants are directly testable. `t.Parallel()` everywhere. Stdlib only.

`static_key_test.go`:

- `TestValidDaemonName_AllowlistMatrix` — 19 rows covering every reject (empty, `.`, `..`, `/`, `\`, uppercase, embedded `.`, leading `-`, space, NUL, oversize 65 chars) and every accept (`default`, `prod`, `dev-1`, `my_daemon`, `0abc`, `a`, `a-1_b`, `aaaa...` × 64).
- `TestNewStaticKey_PublicKeyMatchesPrivate` — generator round-trips through `ecdh.X25519().NewPrivateKey`.
- `TestNewStaticKey_KeysAreNonZero` — two calls produce distinct non-zero keys (smoke against constant-RNG and accidentally-defaulted-struct bugs).
- `TestStaticKey_AccessorsReturnByValue` — mutate returned `[32]byte`, assert internal storage unchanged across re-calls.

`store_test.go`:

- `TestLoadOrCreate_FreshCreate` — no file exists; assert returned `*StaticKey` non-nil, `<dir>/test/` mode `0700`, `<dir>/test/static_key.json` mode `0600`, schema fields decode to 32 bytes each, in-memory key matches on-disk key, public matches recomputed-from-private.
- `TestLoadOrCreate_RoundTripStable` — first call seeds, capture mtime, second call returns identical keys, mtime unchanged + bytes byte-identical (the no-rewrite-on-load invariant).
- `TestLoadOrCreate_InvalidDaemonName` — 12 reject rows; each asserts `errors.Is(err, ErrInvalidDaemonName)`, `sk == nil`, AND `baseDir` has zero entries after the call.
- `TestLoadOrCreate_CorruptJSONReturnsSentinel` — 11 corruption rows (not JSON, missing brace, wrong version, wrong algorithm, private not base64, private wrong length, public not base64, public wrong length, public mismatched private, created_at not RFC3339). Each row asserts `errors.Is(err, ErrCorruptKeyFile)`, `sk == nil`, AND on-disk bytes byte-identical to the seeded fixture.
- `TestLoadOrCreate_CorruptJSONErrorDoesNotLeakPrivateKey` — seed a fixture with a known base64 private key, trigger `algorithm` mismatch, assert `err.Error()` does not contain the known base64 substring.
- `TestLoadOrCreate_NonENOENTReadErrorIsNotCorruption` — make `static_key.json` itself a directory; assert non-nil error that is NOT `Is(ErrCorruptKeyFile)` and NOT `Is(ErrInvalidDaemonName)`. (After #439 this now surfaces as `ErrInsecureKeyFileMode` — Lstat sees the directory's mode ≠ `0600` — but the test's assertions still hold because `ErrInsecureKeyFileMode` is neither corruption nor invalid-name.)
- `TestLoadOrCreate_InsecureDirModeRejected` (#439) — table-driven `0700` accept / `0750`, `0755`, `0701` reject; asserts `errors.Is(ErrInsecureKeyDirMode)`, message contains dir path + observed mode in `%#o`, file bytes unchanged, no private-key leak.
- `TestLoadOrCreate_InsecureFileModeRejected` (#439) — table-driven `0644`, `0640`, `0660`, `0666` all reject; chmod-back-to-`0600` proves the reject path didn't mutate content.
- `TestLoadOrCreate_FreshCreateUnderHostileUmaskStill0700` (#439) — `syscall.Umask(0)` (process-global; non-parallel) then `LoadOrCreate` on a fresh tempdir; asserts post-create dir mode is exactly `0700` and file mode `0600`.
- `TestLoadOrCreate_SymlinkRefusedOnRead` (#439) — symlink a decoy with a sentinel keypair to `<dir>/static_key.json`; asserts `errors.Is(ErrInsecureKeyFileMode)` (Lstat sees symlink mode `0755`/`0777`, fails the `== 0600` check before the open) and that the error message contains neither the decoy path nor its key bytes.
- `TestLoadOrCreate_InsecureFileModeErrorDoesNotLeakPrivateKey` (#439) — extension of the existing no-leak shape for the new sentinel.

## Out of scope (deferred or in follow-up tickets)

- **QR payload extension** — `server_static_pubkey` (base64 32-byte public key) added to `internal/pair.Payload`. #432.
- **Noise wrapper** consuming `StaticKey.PrivateKey()` for the IK handshake state machine. #433.
- **Caller wiring in `pyry pair`** that calls `keys.LoadOrCreate` and feeds the public half into `internal/pair.Payload`. Integration ticket on top of #432 + #438 + #439.
- **`io.LimitReader` (e.g. 64KB) on the keyfile read** — defers a hostile-process-under-same-UID OOM. Threat model excludes that adversary today; revisit if it expands. (See `docs/specs/architecture/439-keys-filesystem-hardening.md` § Security threat #7.)
- **Key rotation verb** (`pyry rotate-static-key`) — v3.
- **Hardware-backed key storage** on the binary side (TPM, macOS Secure Enclave) — v3.
- **In-memory zeroisation** of the private bytes — Go gives no reliable zeroisation primitives; defence-in-depth belongs with hardware-backed storage.

## Related

- [ADR 024](../decisions/024-noise-ik-mobile-e2e.md) — Mobile Protocol v2 (Noise_IK) parent decision.
- [`features/identity-package.md`](identity-package.md) — the shape this package clones (per-daemon JSON file + first-run mint + atomic write + corruption-is-operator-escalated).
- [`features/devices-package.md`](devices-package.md) — sibling per-daemon JSON store under the same parent directory.
- [`features/pair-package.md`](pair-package.md) — downstream consumer of `PublicKey()` (#432 will add the field to the QR payload).
- [`docs/protocol-mobile.md`](../../protocol-mobile.md) § Static keys — binary side — wire-format source of truth for the on-disk schema.
- [`docs/PROJECT-MEMORY.md`](../../PROJECT-MEMORY.md) § Project-level conventions — atomic-write recipe statement.
