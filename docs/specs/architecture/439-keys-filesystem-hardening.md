# `internal/keys` filesystem hardening — `0700` dir + `0600` file mode + `O_NOFOLLOW` read

Spec for Pyrycode issue [#439](https://github.com/pyrycode/pyrycode/issues/439).

## Files to read first

- `internal/keys/store.go:65-81` — current `LoadOrCreate` flow (`validDaemonName` → `os.ReadFile` → `parsePersisted` / `mintAndPersist`). This is the function being rewired.
- `internal/keys/store.go:83-123` — `parsePersisted`. Unchanged; the new read path still feeds its raw bytes here.
- `internal/keys/store.go:125-178` — `mintAndPersist` → `writeStaticKey`. Note the existing `os.MkdirAll(dir, 0o700)` at line 139 (still useful as a no-op on the create path after this ticket's hardening also runs `MkdirAll`).
- `internal/keys/static_key.go:22-27` — existing `ErrInvalidDaemonName` / `ErrCorruptKeyFile` sentinels. The two new sentinels live alongside them.
- `internal/keys/store_test.go:17-90` — `TestLoadOrCreate_FreshCreate` already asserts dir mode `0700` and file mode `0600` on the create path; the new tests for chmod-after-create complement it rather than duplicate.
- `internal/keys/store_test.go:185-319` — fixture-seeding pattern (`os.MkdirAll(dir, 0o700)` + `os.WriteFile(path, …, 0o600)`) the new mode-mismatch / symlink tests reuse.

## Context

Mobile Protocol v2 (#430) persists each binary's X25519 static keypair at `~/.pyry/<daemon-name>/static_key.json`. The companion ticket (#438) landed the core primitive: `LoadOrCreate`, key generation, JSON envelope, atomic-write recipe at `0600`. This ticket lands the **filesystem hardening** around that primitive so an accidentally world-readable key file — or a TOCTOU symlink swap between the mode check and the read — cannot silently redirect the daemon to attacker-controlled bytes.

Both children must merge before any downstream consumer (#432 QR payload, #433 Noise wrapper) reads the static key. The hardening is a hard prerequisite, not a follow-up; without it, the loud-failure contract operators rely on (daemon refuses to start on weakened modes) does not exist.

The pattern is **stat-then-open with loud-failure semantics**: no auto-`chmod`, no silent fallback, no log-and-continue. If the mode is wrong, the daemon refuses to start and the operator fixes the mode themselves. This mirrors the relay's `~/.cache/pyry-relay/` TLS cert hardening referenced in the ticket body and aligns with [[atomic write recipe for on-disk registries]] — registries trust their own write contract on the create path; the hardening defends the *read* path against drift introduced outside the process (operator chmod, package-manager unpack, restore-from-backup, hostile process under the same UID).

This ticket has the `security-sensitive` label, so the architect-side security pass (§ Security review pass) runs before commit.

## Design

### Public surface (unchanged)

`LoadOrCreate(baseDir, daemonName string) (*StaticKey, error)` keeps its signature exactly as #438 landed it. No new exported helpers. **Two new exported sentinels**, and only two:

- `ErrInsecureKeyDirMode` — parent directory's mode is broader than the package permits.
- `ErrInsecureKeyFileMode` — `static_key.json`'s mode is not exactly `0600`.

Both are `var Err* = errors.New(…)` next to the existing sentinels in `internal/keys/static_key.go`. Callers match via `errors.Is`.

No new exported types. No new exported functions. The hardening is internal-only.

### Rewired `LoadOrCreate` flow

```
LoadOrCreate(baseDir, daemonName)
  ├─ validDaemonName(daemonName)?           → ErrInvalidDaemonName (unchanged)
  ├─ dir  = filepath.Join(baseDir, daemonName)
  ├─ path = filepath.Join(dir, "static_key.json")
  │
  ├─ ensureSecureKeyDir(dir)                ← NEW (§ ensureSecureKeyDir)
  │     stat dir; if missing → MkdirAll(0700) + re-stat
  │     reject if !IsDir() or mode & 0077 != 0
  │
  ├─ Lstat(path)
  │     ├─ exists → mode.Perm() == 0600?    → ErrInsecureKeyFileMode if not
  │     │           openSecureKeyFile(path) ← NEW (§ openSecureKeyFile)
  │     │           parsePersisted(path, raw)
  │     ├─ ErrNotExist → mintAndPersist(dir, path)   (unchanged path)
  │     └─ other       → wrapped stat error
```

Two helpers, both unexported, both new:

#### `ensureSecureKeyDir(dir string) error`

Single responsibility: guarantee `dir` exists, is a directory, and is owned-only.

Signature: `(dir string) error`.

Behavior (described, not pre-written):

1. `os.Stat(dir)`.
2. If `errors.Is(err, fs.ErrNotExist)`: `os.MkdirAll(dir, 0o700)` followed by a re-stat. Wrap any error as `keys: mkdir <dir>: %w` / `keys: re-stat <dir>: %w`.
3. If `err != nil` (other): wrap as `keys: stat <dir>: %w`.
4. After stat (initial or post-mkdir): reject `!fi.IsDir()` with `keys: <dir> is not a directory: %w` wrapping `ErrInsecureKeyDirMode` (the operator's fix is the same: remove the imposter, re-run).
5. Reject `fi.Mode().Perm() & 0o077 != 0` with `keys: <dir>: mode %#o: %w` wrapping `ErrInsecureKeyDirMode`.

The check `mode & 0o077 != 0` directly encodes the AC's "any group/other readable, writable, or executable bit set". A narrower mode (e.g. `0600`, `0500`) is permitted by this check — it isn't *insecure*, just non-functional, and the daemon's subsequent operation will fail loudly with EACCES if so. We don't gold-plate against narrower modes; the security contract is "no group/other access", not "exactly `0700`".

The re-stat after `MkdirAll` is defensive: under a normal umask, `mkdir(2)` cannot widen the requested mode (resulting mode is `mode & ~umask`, monotonically narrowing). The re-stat catches the pathological cases (default ACLs, exotic filesystems, future-proofing against changes to `os.MkdirAll`). It costs one `stat` syscall on first run and zero on subsequent runs (dir already exists path skips the mkdir+re-stat branch).

#### `openSecureKeyFile(path string) ([]byte, error)`

Single responsibility: read `path` as a regular file, refusing to follow a symlink under it.

Signature: `(path string) ([]byte, error)`.

Behavior:

1. `os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)`.
2. On `ELOOP` (or any open error): wrap as `keys: open %s: %w` and return. **Do not** include the resolved link target — name only the path the caller passed.
3. On success: `defer f.Close()`, `io.ReadAll(f)`, wrap any read error as `keys: read %s: %w`, return bytes.

`syscall.O_NOFOLLOW` is the canonical spelling on both Linux and macOS — `golang.org/x/sys/unix` is not needed. The Go runtime's `syscall` package exports the platform-correct value on both targets.

### Why `Lstat` + `O_NOFOLLOW` (belt-and-suspenders)

The Lstat-mode-check catches the *static* symlink-at-rest case: a symlink's `Mode().Perm()` is the symlink's own mode (typically `0777` on both Linux and macOS), which fails the `== 0600` check immediately. The check returns `ErrInsecureKeyFileMode` before the open ever runs.

`O_NOFOLLOW` catches the *dynamic* case: between Lstat (which sees a regular `0600` file) and the open, a hostile process under the same UID replaces the regular file with a symlink. `O_NOFOLLOW` makes the open return `ELOOP` instead of redirecting to attacker-controlled bytes.

This is genuine belt-and-suspenders: two different fabrics (file-mode contract vs. open-time symlink resolution), each catching a distinct failure mode the other can't.

### Why we never `chmod` to repair

The threat model assumes an attacker under the daemon's UID or an operator who unwittingly weakened the mode. Auto-`chmod` removes the loud-failure signal the operator needs to investigate the *why* — if the package manager unpacked the key as `0644`, silently fixing it would hide a packaging bug. If a hostile sibling process under the same UID weakened the mode (e.g. to read it via an open fd held by another reader), auto-`chmod` hands the attacker exactly the moment of vulnerability they need.

The spec is explicit: "No auto-`chmod`, no silent fallback. Fatal at the call site." The daemon refuses to start and the operator fixes the mode by hand.

### Error message contract

Both new sentinels' error messages name the **path** and the **observed mode in octal** (e.g. `0644`), and nothing else. No file contents. No symlink target. No private-key bytes. The existing `parsePersisted` error contract (which already redacts the file body — see `internal/keys/store.go:96` returning `private_key base64 decode failed` without echoing the bad bytes) is the model.

The path is operator-facing — it tells them which file to `chmod`. The mode is operator-facing — it tells them what the file *currently* is, so `0644 → 0600` is obvious. Both are non-secret. Private key bytes are secret; they must not appear in error context for any reason. Existing test `TestLoadOrCreate_CorruptJSONErrorDoesNotLeakPrivateKey` (`store_test.go:256`) is the precedent; the new symlink/mode-mismatch tests should follow the same shape (assert the error message does NOT contain the private-key base64).

### Files modified

- `internal/keys/static_key.go` — add two `var Err* = errors.New(…)` declarations next to existing sentinels, plus their doc comments. Net additions ≈ 12 LOC.
- `internal/keys/store.go` — add `ensureSecureKeyDir` (~25 LOC), add `openSecureKeyFile` (~10 LOC), rewire `LoadOrCreate` body (~15 LOC net delta). Update `LoadOrCreate`'s doc comment to remove the "intentionally NOT in this function — it ships in #439" paragraph (lines 60-64) and replace it with one short sentence noting the dir-mode + file-mode + `O_NOFOLLOW` guards are enforced before the read. Net additions ≈ 50 LOC production code.
- `internal/keys/store_test.go` — append new tests (§ Testing strategy). Net additions ≈ 100 LOC test code.

No new files. No changes to `mintAndPersist` / `writeStaticKey` (its existing `MkdirAll(dir, 0o700)` becomes a redundant no-op after `ensureSecureKeyDir` has run; leave it as defense-in-depth — it costs nothing and survives any future refactor that calls `writeStaticKey` from a different entry).

### Self-check before commit

- Production source files touched: `static_key.go` + `store.go` = **2**. Threshold for split is 5. ✓
- Net production LOC: ~50. Threshold for split is ~150. ✓
- New exported types: 2 sentinels (`ErrInsecureKeyDirMode`, `ErrInsecureKeyFileMode`) — the ticket explicitly names both. ✓
- Consumer call sites: 0. `LoadOrCreate` signature unchanged. ✓
- Acceptance criteria: 5. ✓

## Concurrency model

`LoadOrCreate` is documented as not safe for concurrent use against the same path (see existing comment at `internal/keys/store.go:58-59`); bootstrap runs once on daemon startup before any goroutines fan out. This ticket adds zero concurrency. No goroutines, no channels, no context. The stat/open syscalls are all synchronous.

The TOCTOU window between `Lstat` and `OpenFile` is real but inherent — it exists in any "check then act" filesystem pattern. `O_NOFOLLOW` closes the symlink-swap subspace of that window. A regular-file-to-regular-file swap during the window would still go through, but the attacker would need to write a fully-valid `static_key.json` envelope (correct schema, valid X25519 keypair) — a totally different threat model than "redirect to an attacker-controlled file". The spec is intentionally scoped to symlink defense, not arbitrary FS race defense.

## Error handling

Sentinel taxonomy after this ticket:

| Sentinel | Trigger | Caller fix |
|---|---|---|
| `ErrInvalidDaemonName` | `daemonName` fails allowlist (existing) | Pass a valid daemon name |
| `ErrCorruptKeyFile` | JSON / schema / algorithm / length / pub-priv mismatch (existing) | Investigate; never auto-recover (keys are bound to paired devices) |
| `ErrInsecureKeyDirMode` | parent dir mode has g/o bits, or parent isn't a dir | `chmod 0700 <dir>` (or remove the imposter) and re-start |
| `ErrInsecureKeyFileMode` | file mode is not exactly `0600` | `chmod 0600 <file>` and re-start |

The two new sentinels are intentionally **distinct** — the operator's fix differs (chmod the directory vs. chmod the file). Lumping them under a single `ErrInsecurePermissions` would force the operator to read the wrapped message to know which path to chmod, defeating the point of an `errors.Is`-matchable sentinel.

Wrapping pattern: `fmt.Errorf("keys: %s: mode %#o: %w", path, observedMode, ErrInsecure...)`. Mirrors `parsePersisted`'s style and is consistent with the rest of `internal/keys`.

Non-mode errors (stat fails, mkdir fails, open fails with non-ELOOP) are wrapped as raw `keys: <verb> %s: %w` returning the underlying `error`. They are *not* re-classified as one of the four sentinels — see the existing `TestLoadOrCreate_NonENOENTReadErrorIsNotCorruption` (`store_test.go:292`) for the precedent that I/O errors must not be misclassified as semantic errors.

## Testing strategy

All tests live in `internal/keys/store_test.go`, append-only. Each is `t.Parallel()` where independent. Use `t.TempDir()` so tests clean up after themselves.

### Mode-matrix test (parent directory)

Table-driven test exercising chmod-after-create:

- Seed: `os.MkdirAll(dir, 0o700)` + write a valid `static_key.json` at `0600` via a helper that calls `LoadOrCreate` once and reads the file back (or constructs an `onDiskKey` fixture directly — either is fine; whichever is shorter).
- Cases:
  - `0700` → accept (control case; the round-trip-stable path runs).
  - `0750` → reject with `errors.Is(err, ErrInsecureKeyDirMode)`.
  - `0755` → reject with `errors.Is(err, ErrInsecureKeyDirMode)`.
  - `0701` (other-execute bit set) → reject with `errors.Is(err, ErrInsecureKeyDirMode)`.
- Assertions per case: error matches expected sentinel (or is nil for accept); `StaticKey` is nil on reject; the on-disk file is **not** mutated (re-read post-call, compare bytes).
- Error-message assertions: on reject, the error string contains the directory path AND the observed mode in `%#o` format. The error string does NOT contain any private-key base64.

### Mode-matrix test (file)

Same shape:

- Seed: write a valid `static_key.json` at `0600`.
- Cases: chmod the file to `0644`, `0640`, `0660`, `0666` — each rejects with `errors.Is(err, ErrInsecureKeyFileMode)`. Then chmod back to `0600` and assert the load succeeds (proves we didn't mutate file content).
- Assertions: error sentinel match; `StaticKey` nil on reject; file bytes preserved.

### Fresh-create under hostile umask

- Set the process umask to `0o000` via `syscall.Umask(0)` (capture old, defer restore).
- Call `LoadOrCreate` against a fresh `t.TempDir()` base + unused daemon name.
- Assert success AND assert the post-create dir mode is exactly `0700` (`fi.Mode().Perm() == 0o700`).
- This proves the `MkdirAll(0o700)` + re-stat path produces a clean directory even when umask would have allowed broader bits to survive (it cannot widen, but the test pins the contract).
- Note for the developer: `syscall.Umask` is process-global — this test cannot use `t.Parallel()`. Mark it explicitly non-parallel and document why with one short comment.

### Symlink refusal

- Seed: create a real `decoy.json` at `<tempDir>/elsewhere/decoy.json` (mode `0600`) containing valid-looking but obviously-distinct bytes (e.g. a key envelope with a known sentinel public key the test can recognize).
- Create `<base>/<daemon>/` at `0700`.
- Place `os.Symlink("<tempDir>/elsewhere/decoy.json", "<base>/<daemon>/static_key.json")`.
- Call `LoadOrCreate(base, daemon)`.
- Assert: error is non-nil; `StaticKey` is nil.
- Assert: the error sentinel is `ErrInsecureKeyFileMode` (because `Lstat` on a symlink returns mode `0777`, which fails the `== 0600` check before the open). **Or** wrap-around: if the developer prefers a separate sentinel-free assertion ("the error wraps `ELOOP` from the open path"), either is acceptable — the security goal is "load fails, decoy bytes never read".
- Assert: error message does NOT contain the symlink target path.
- Assert: error message does NOT contain the decoy's bytes or the sentinel public key.

### Private-key non-leakage (extension of existing test)

`TestLoadOrCreate_CorruptJSONErrorDoesNotLeakPrivateKey` already covers `ErrCorruptKeyFile`. Extend its pattern with one new test case that triggers `ErrInsecureKeyFileMode` (chmod the seeded file to `0644`) and asserts the error message does NOT contain the file's private-key base64. Same shape as the existing test.

### Test naming

Follow the existing convention (`TestLoadOrCreate_*`):

- `TestLoadOrCreate_InsecureDirModeRejected`
- `TestLoadOrCreate_InsecureFileModeRejected`
- `TestLoadOrCreate_FreshCreateUnderHostileUmaskStill0700`
- `TestLoadOrCreate_SymlinkRefusedOnRead`
- `TestLoadOrCreate_InsecureFileModeErrorDoesNotLeakPrivateKey`

### Platform notes

All tests run on Linux + macOS (Pyrycode's two supported platforms). No Windows path. `syscall.O_NOFOLLOW` is present on both. `syscall.Umask` is present on both. No `//go:build` constraints needed.

## Security review pass

Required for the `security-sensitive` label. The spec is the artifact under review; the adversarial pass walks the categories below (trust boundaries → threat enumeration → refusal verdict → developer-side guardrails).

### Trust boundaries

- **`baseDir`**: caller-supplied; treated as trusted by the package (`internal/keys/static_key.go:38-41` documents this explicitly — the package validates `daemonName`, not `baseDir`). This ticket does not change that contract. The hardening operates on `filepath.Join(baseDir, daemonName)` and below; if `baseDir` itself is attacker-controlled, no amount of mode checking helps. Out of scope, by design.
- **`daemonName`**: validated by `validDaemonName` (existing). The hardening runs *after* that check, so we never construct paths from unvalidated input.
- **Filesystem state under `dir`**: untrusted. Could have been mutated by anything under the daemon's UID. The whole point of this ticket is to refuse to proceed if the state is wrong.

### Identified threats (adversarial walk)

1. **World-readable key file at rest.** Operator runs `chmod 644` accidentally, or unpacks a package that did. Defense: file-mode check, `ErrInsecureKeyFileMode`. **Closed.**

2. **World-readable key directory at rest.** Same shape, on the parent dir. Defense: dir-mode check, `ErrInsecureKeyDirMode`. **Closed.**

3. **Static symlink at the key path.** Attacker creates `static_key.json → /tmp/attacker.json` before the daemon's first run (or replaces a removed file). Defense: `Lstat` mode check (symlink mode ≠ `0600`) AND `O_NOFOLLOW` on open. **Closed.**

4. **TOCTOU swap: regular → symlink between Lstat and Open.** Hostile process under same UID races us. Defense: `O_NOFOLLOW` on open returns `ELOOP`. **Closed.**

5. **TOCTOU swap: regular → different-regular between Lstat and Open.** Hostile process under same UID atomically replaces our file with one of their choosing. The new file would have to be a fully-valid key envelope to pass `parsePersisted`. **Out of scope**, as documented. Defense at this layer is impractical without `openat` + `O_TMPFILE` patterns we don't need for the threat model. The trust assumption is: nothing else under our UID is hostile. If that fails, the entire daemon is compromised regardless of `static_key.json`.

6. **Umask-narrowed dir mode on fresh create.** Operator runs the daemon under umask `0177`, resulting dir is `0600` — non-functional. The re-stat catches the non-`0700` mode... wait, it doesn't, by our check (`& 0o077 != 0` permits narrower modes). Outcome: daemon proceeds with a `0600` dir, then fails on first subsequent open with EACCES. **Acceptable.** This is a functional failure, not a security failure. The daemon's logs will show the EACCES; the operator's fix is to widen umask. We don't gold-plate against this.

7. **Decoy file at the path.** Attacker places a 32MB log file at `static_key.json` to OOM us. `io.ReadAll` on a regular file is bounded by the file size, which the attacker (under our UID) controls. The on-disk envelope is ~200 bytes; a multi-MB file is the only signal we'd have that something is wrong. **Mitigation pending:** could cap with `io.LimitReader` at e.g. 64KB. **Decision: out of scope.** Threat #5 already establishes that a hostile process under our UID is outside the trust boundary. Calling out as a recommended follow-up if the threat model expands.

8. **Path traversal via `daemonName`.** Already closed by the allowlist (#438). The hardening doesn't widen the surface.

9. **Error message leaks key bytes.** The new error wrapping (`fmt.Errorf("keys: %s: mode %#o: %w", path, mode, sentinel)`) names the path and the mode — both non-secret. It does NOT touch the file body. The decoy / symlink test asserts this explicitly. **Closed.**

10. **Error message leaks symlink target.** `os.OpenFile(path, …, O_NOFOLLOW)` returns an `*os.PathError` whose `Path` field is the path passed in, not the resolved target. The default `Error()` method on `*os.PathError` formats `<op> <path>: <err>` — also no target. We do not call `os.Readlink` to enrich the error. **Closed.**

### Refusal verdict

**PASS.** The ticket's threat model is correctly scoped. The two-sentinel + two-helper design closes the in-scope threats with no dual-purpose code paths, no auto-repair, and no silent fallback. Out-of-scope threats (#5, #7) are documented with their rationale rather than silently waved off.

### Items the developer must not change without re-review

- The "no auto-chmod" rule. If the developer thinks "this could just `os.Chmod(path, 0o600)` and continue", they MUST stop and escalate. The loud-failure contract is the security feature; repair erases the signal.
- The error-message contents (path + mode in octal, nothing else). Adding the symlink target or the file body for "easier debugging" is a leak.
- The `Lstat` (not `Stat`) on the read path. Following the symlink during stat would defeat the mode check half of the belt-and-suspenders.

## Open questions

None blocking. The two below are pre-flagged for the documentation phase:

1. **Mode-check granularity** — we permit narrower-than-`0700` directories (e.g. `0600`) by checking `& 0o077 != 0`. This is intentional but worth a one-liner in `docs/knowledge/codebase/439.md` so a future contributor doesn't "tighten" it to `mode != 0o700` and reject a benign narrow mode without understanding why we permit it. Document, don't enforce.
2. **`io.LimitReader` on the read** — § Security threat #7 above. If the threat model later expands to "hostile process under same UID", revisit. Track as a comment on the closed ticket if the doc phase agrees.
