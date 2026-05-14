# 341 — `internal/agentrun` trust-state helper

## Files to read first

- `internal/sessions/rotation/watcher.go:108-115` — canonical `filepath.EvalSymlinks` pattern for path comparison against claude-resolved paths (the macOS `/var → /private/var` rule). Mirror this shape in `ResolveWorkdir`.
- `internal/sessions/rotation/watcher.go:178-184` — second EvalSymlinks call site (probe-path comparison); confirms "EvalSymlinks on an absolute path" is the lingua franca for talking to claude's path keys.
- `internal/devices/registry.go:63-107` — canonical atomic-write recipe in pyrycode (`os.CreateTemp` in dir → chmod → encode → `Sync` → `Close` → `Rename`). The helper's write step copies this shape; we do NOT extract a shared helper yet (per PROJECT-MEMORY: "duplicated until a fifth registry forces extraction").
- `internal/install/install.go:119-134` — existing `ResolveWorkDir(flag, cwd, homeDir)` in a different package. NOT what we're reimplementing: that helper resolves CLI flag → absolute path; ours resolves absolute path → realpath. Same surface area, orthogonal job. The name overlap is package-scoped (`install.ResolveWorkDir` vs `agentrun.ResolveWorkdir`) — no clash. Read this only to confirm we are not duplicating its job.
- Parent #338 issue body — the original ticket before the split; the AC list there is authoritative on what the helper must guarantee. #341 is the helper; #338B is the wire-up.
- Sibling #337 spec (`docs/specs/architecture/337-agent-run-scaffold.md`) — `agent-run` verb's flag surface. Helps reason about what the eventual caller looks like (which workdir string it will pass), even though #341 has no caller in-diff.
- `docs/PROJECT-MEMORY.md` § "Atomic-write recipe for on-disk registries" — convention statement (`os.CreateTemp` … `Rename`); spec just points at it.

## Context

Phase A spike (#329) verified that pre-writing `projects[<realpath(workdir)>].hasTrustDialogAccepted = true` in `~/.claude.json` side-steps the workspace-trust TUI dialog that would otherwise block claude at startup. Without this, pyry would have to drive the dialog via PTY, which is fragile under timing variance.

`~/.claude.json` is a shared single-file registry written by both the user's interactive claude sessions and (once #338B wires this helper in) `pyry agent-run`. Concurrent writes from multiple `pyry agent-run` processes — and from a foreground claude the user happens to run alongside — must serialize. The atomic-write recipe is insufficient on its own because the read-modify-write needs a lock spanning both ends, not just the rename.

This ticket lands the package and tests only. No caller. #338B wires `MarkWorkdirTrusted` into the `agent-run` verb immediately after this lands; #333's JSONL watcher will consume `ResolveWorkdir`.

## Design

### Package boundary

New package `internal/agentrun`. First file: `trust.go`. Test file: `trust_test.go`. Stdlib only (`encoding/json`, `errors`, `fmt`, `io/fs`, `os`, `path/filepath`, `sort`, `syscall`).

`syscall.Flock` is available on darwin + linux (BSD flock semantics on both); pyrycode does not target Windows. Build tag is NOT required — `syscall.Flock` exists on both supported platforms.

The package is named `agentrun` (no underscore) to match Go convention and the directory name. No exported types beyond the two functions.

### Public API

Two functions, both exported. No types. No constructor.

```go
// ResolveWorkdir returns the resolved absolute path of workdir, mirroring how
// claude resolves a workdir before reading ~/.claude.json's projects map.
// Resolves symlinks (macOS /var → /private/var).
func ResolveWorkdir(workdir string) (string, error)

// MarkWorkdirTrusted sets projects[<ResolveWorkdir(workdir)>].
// hasTrustDialogAccepted = true in <homeDir>/.claude.json, under a file lock
// spanning the entire read-modify-write window. Idempotent. Atomic on-disk.
func MarkWorkdirTrusted(homeDir, workdir string) error
```

**Why explicit `homeDir` (not `os.UserHomeDir`):** tests can use `t.TempDir()` directly without `t.Setenv("HOME", ...)`, which blocks `t.Parallel`. Production callers in #338B will pass `os.UserHomeDir()`. The same shape (`homeDir string` parameter) is already used by `internal/install.ResolveWorkDir`.

### `ResolveWorkdir` — body shape

One-line behaviour: `filepath.Abs(workdir)` first (so relative inputs resolve against the caller's cwd), then `filepath.EvalSymlinks` on the absolute path. Return the resolved path. Wrap errors with `fmt.Errorf("agentrun: resolve workdir %q: %w", workdir, err)`.

Pre-existence: `filepath.EvalSymlinks` returns an error if the path does not exist. That's acceptable — the eventual caller (#338B) is expected to have already validated workdir exists via `os.Stat` during flag parsing. The error wraps `fs.ErrNotExist`; callers can `errors.Is` if they need to discriminate.

### `MarkWorkdirTrusted` — body shape

Sequence (read top to bottom — each step is one or two lines of code, total ~70 lines):

1. Resolve key: `key, err := ResolveWorkdir(workdir)`. Return on error.
2. Compute paths: `dataPath := filepath.Join(homeDir, ".claude.json")`, `lockPath := dataPath + ".lock"`.
3. Acquire lock (see § "Lock strategy" below). `defer` release.
4. Read data file. ENOENT → start with an empty root. Other read errors → return wrapped.
5. Unmarshal with `json.Decoder.UseNumber()` into `map[string]any`. Empty file → empty map. Malformed JSON → return wrapped error (caller sees: a malformed `~/.claude.json` is a hard failure, not a silent overwrite).
6. Extract or create `projects` sub-map (assert `map[string]any`; on type-mismatch — e.g. claude rewrote the schema — return error rather than overwrite).
7. Extract or create the entry for `key` (assert `map[string]any`; same type-mismatch handling).
8. Set `entry["hasTrustDialogAccepted"] = true`. Re-assign back into `projects[key]`.
9. Atomic write (see § "Write step" below).
10. Lock auto-released via the deferred close+unlock.

### Lock strategy

`flock(2)` on a **sibling lock file** `~/.claude.json.lock`, NOT on the data file directly. Rationale: the atomic-write recipe replaces the data file's inode on every write. A second writer that opens the data file *after* our rename gets a different inode and acquires its own independent lock — the locks would not serialize. A stable sibling lock file (whose inode is never replaced) is the standard fix.

Open the lock file with `os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)`. Never delete it (deletion races acquisition). Call `syscall.Flock(int(f.Fd()), syscall.LOCK_EX)` — blocking; pyrycode has no nonblocking-acquire requirement. `defer` runs `syscall.Flock(fd, syscall.LOCK_UN)` and then `f.Close()` (order matters for explicitness, though `Close` releases the lock automatically too).

Process-crash safety: flock is released by the kernel on fd close (including process exit). Process killed mid-RMW → lock released, no stale lock to clean up. Data file is either pre- or post-rename; never partial.

Within-process safety: two goroutines calling `MarkWorkdirTrusted` concurrently each open their own fd and each call Flock — kernel serializes them on darwin and linux. The test for the concurrency AC drives exactly this case.

### Write step

Mirror `internal/devices/registry.go:Save` line-for-line:

1. `tmp, err := os.CreateTemp(homeDir, ".claude.json.tmp-*")` (same directory as the rename target so the rename is intra-filesystem).
2. `defer os.Remove(tmp.Name())` (best-effort cleanup if anything below fails).
3. `os.Chmod(tmp.Name(), mode)` — see § "File mode" below for `mode`.
4. Encode: `enc := json.NewEncoder(tmp); enc.SetIndent("", "  "); enc.Encode(root)`. Go's `encoding/json` writes map keys in sorted order, so output is deterministic for the same logical content.
5. `tmp.Sync()`, `tmp.Close()`.
6. `os.Rename(tmp.Name(), dataPath)`.

Each step wraps errors with `fmt.Errorf("agentrun: <step>: %w", err)`. The wraps name the step (`encode`, `fsync`, `rename`) but do NOT echo the file's content into the error chain — see § "Logging discipline" below.

### File mode

If `~/.claude.json` already exists, copy its mode to the temp file before rename (so we don't quietly tighten or loosen permissions on the user's file). Stat the existing file before reading; on missing file, use `0o600` as the create mode. Lock file is always `0o600`.

Implementation note: a single `os.Stat(dataPath)` (called once, before the lock acquisition does not matter — it's only racing against ourselves under the flock) is enough; on `fs.ErrNotExist` use the create-mode default. If you want to be defensive, restat inside the lock — but that's overkill: this is single-user data and the mode is informational, not a security boundary.

### Numeric precision (preservation)

`encoding/json` decodes JSON numbers into `float64` by default, which loses precision above 2^53. `~/.claude.json` is not under pyry's control and may store int64-sized values (timestamps, counters); a lossy round-trip would silently corrupt them, violating the "preserved verbatim" AC.

The fix is one line: call `dec.UseNumber()` on the `json.Decoder` before `Decode`. Numbers come in as `json.Number` (a string alias) and re-marshal byte-identically.

**This is mandatory, not optional.** A test row (§ "Testing strategy") asserts a large int64 round-trips losslessly.

### Idempotency

Two consecutive `MarkWorkdirTrusted(home, workdir)` calls must produce a byte-identical file on disk. Two contributors to this property:

1. `encoding/json` sorts map keys alphabetically on marshal → key order is stable.
2. `UseNumber` preserves numeric textual form → numbers don't drift.

Setting `hasTrustDialogAccepted = true` twice is a no-op assignment; the resulting map is `==`-equal in content and `MarshalIndent`-equal in bytes.

Empty-map → empty-string corner: if `projects` is empty after the first call (impossible because we always add the entry, but worth pinning), the second call sees the same input. Not a real edge case.

### Logging discipline

The package's doc-comment must include:

```
// MUST NOT log file contents at any layer. ~/.claude.json may contain
// tokens or claude-internal state pyry does not own; the helper takes a
// pass-through view (preserve fields verbatim) and emits a key+verdict on
// success, not the underlying bytes.
```

Error wraps may name the failing step (`encode`, `fsync`, `rename`, `lock acquire`, `read`) and the file path (paths are derivable from `homeDir` — not sensitive). They may NOT include the file's bytes or any unmarshalled values other than the workdir key the caller already supplied.

No `slog` calls inside the helper. The eventual caller (#338B) logs success at the verb level.

## Concurrency model

No goroutines spawned. The helper is purely sequential within a single invocation: lock → read → mutate → write → unlock.

Concurrent invocations across goroutines or processes are serialized by `flock(2)` on the sibling lock file. The kernel guarantees mutual exclusion across both axes (within and across processes, on darwin and linux).

No `context.Context` parameter. The helper is fast-bounded (filesystem read + write under a process-local lock); a hung flock acquire from another stuck process is recoverable by the operator killing the holder. If a future caller needs cancellable-acquire, add a context-taking sibling without changing this signature.

## Error handling

Three terminal classes:

1. **`fs.ErrNotExist` on the data file** — not an error; helper creates a fresh `{"projects": {<key>: {"hasTrustDialogAccepted": true}}}`.
2. **Malformed input** (unparseable JSON, `projects` not an object, `projects[key]` not an object) — return wrapped error. The helper refuses to silently destroy state it doesn't understand. Caller surfaces this as the verb's exit-1 path.
3. **I/O failure** (read, fsync, rename, flock) — return wrapped error with step name. Caller surfaces.

No retries. Caller chooses whether to retry (e.g. lock contention is rare; a panic on second failure is appropriate).

## Testing strategy

`internal/agentrun/trust_test.go` — table-driven, stdlib `testing`, no testify. Same-package tests (white-box if needed; in practice the public API is enough).

Each test uses `t.TempDir()` as `homeDir`. Real `~/.claude.json` is never touched.

### `ResolveWorkdir`

- **macOS realpath** — `runtime.GOOS == "darwin"` only; pass `t.TempDir()` (which is under `/var/folders/...` on macOS) and assert the result starts with `/private/var/`. Skip on linux with `t.Skip("darwin-only: /var symlink")`.
- **already-resolved absolute path** — pass `/tmp` on linux (or another stable absolute) and assert the result is the same path after `Clean`.
- **relative path** — pass `"."` and assert the result is absolute (via `filepath.IsAbs`).
- **missing path returns wrapped ErrNotExist** — pass `t.TempDir() + "/does-not-exist"` and assert `errors.Is(err, fs.ErrNotExist)`.

### `MarkWorkdirTrusted`

For each row below: `home := t.TempDir()`; create a real workdir via `wd := t.TempDir()` (so EvalSymlinks doesn't fail); call `MarkWorkdirTrusted(home, wd)`; read back `~/.claude.json` and assert.

- **missing file creates skeleton** — no pre-existing `~/.claude.json`. After call, file exists with mode `0o600`, parses as JSON, has `projects[<resolved(wd)>].hasTrustDialogAccepted == true`, and `projects` has exactly one entry.
- **preserves sibling project entries** — pre-write `~/.claude.json` containing `{"projects": {"/some/other/path": {"hasTrustDialogAccepted": false, "extra": "field"}}}`. After call, both project entries present; the sibling's `hasTrustDialogAccepted` is still `false` and its `extra` field still present.
- **preserves fields within same project entry** — pre-write the file with the target key already present and additional keys on its entry (e.g. `"foo": "bar"`, `"mcpServers": {…}`). After call, those keys still present unchanged; only `hasTrustDialogAccepted` differs (was `false` or absent, now `true`).
- **preserves top-level fields outside `projects`** — pre-write `{"projects": {…}, "userID": "abc", "telemetry": {"enabled": false}}`. After call, `userID` and `telemetry` unchanged.
- **preserves numeric precision** — pre-write `{"projects": {}, "lastLoginNanos": 1763123456789012345}` (a value that exceeds float64 mantissa precision). After call, parse the written file with `UseNumber` and assert the value's `String()` is `"1763123456789012345"`. This pins the `UseNumber` requirement.
- **idempotent** — call once; capture file bytes A. Call again with the same workdir; capture bytes B. Assert `bytes.Equal(A, B)`.
- **concurrency: two workdirs serialize** — spawn two goroutines via `sync.WaitGroup`, each marking a *different* workdir under the same `homeDir`. After both return, the written file's `projects` map has both entries; both have `hasTrustDialogAccepted == true`. Run under `-race`. The test passes regardless of which goroutine "wins" the race; the lock guarantees both writes are durable.
- **file mode preserved on existing file** — pre-write the data file with mode `0o644`. After call, `os.Stat(dataPath).Mode().Perm() == 0o644`.
- **malformed JSON fails loudly** — pre-write the file containing `"not json"`. Assert `MarkWorkdirTrusted` returns a non-nil error and the file is left untouched (bytes unchanged from the pre-write).
- **`projects` not an object fails** — pre-write `{"projects": "not an object"}`. Assert non-nil error; file untouched.
- **existing entry not an object fails** — pre-write `{"projects": {<resolved(wd)>: "not an object"}}` (need to compute the resolved key first via `ResolveWorkdir(wd)`). Assert non-nil error; file untouched.

### What NOT to test in #341

- The `agent-run` CLI verb — that's #337's territory + #338B's wire-up.
- Cross-process flock — out of test scope; flock semantics are kernel-level and unit tests rely on within-process Flock behaviour (which is the same code path on both supported platforms).
- Lock-file cleanup — the lock file is intentionally never deleted; a "stale lock file remains after call" assertion would be testing the design, not the behaviour.
- Specific JSON formatting beyond what idempotency requires — Go's `encoding/json` output is the implementation detail; tests parse + assert structure, not raw bytes (except for the idempotency byte-equality check, which is intra-test).

## Open questions

- **`os.UserHomeDir` vs explicit `homeDir`.** Spec picks explicit. If the developer finds the call shape clumsy in #338B (`agent_run.go` having to call `os.UserHomeDir()` just to feed it back into the helper), file a follow-up to add a thin convenience wrapper — don't change the underlying primitive.
- **File mode preservation when `~/.claude.json` was created by claude with an unusual mode.** Spec preserves whatever mode is on disk. If claude writes at `0o644` (likely), pyry continues writing at `0o644`. If a future security review wants `0o600` enforced regardless, that's a separate ticket and a separate trade-off conversation (it would re-tighten a permission the user may have intentionally widened).
- **`syscall.Flock` vs `golang.org/x/sys/unix.Flock`.** Spec uses `syscall.Flock` (stdlib, no new dep). Both are equivalent on darwin + linux. If a future Windows port becomes in-scope (unlikely; "Windows out of scope" per CLAUDE.md), the flock call site grows a `//go:build unix` tag and a Windows alternative — that's a porting concern, not a #341 concern.
- **What if the data file is a symlink?** `os.Rename` replaces the symlink with a regular file, breaking the symlink. Spec accepts this — the helper takes ownership of the path. If the user intentionally symlinked `~/.claude.json` to a non-standard location, they will discover this is broken by pyry the same way it would be broken by any atomic-rename writer (including claude itself when claude rewrites its own config). Not a security issue (same uid).

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** Single explicit boundary at `MarkWorkdirTrusted`'s call site. The helper trusts its `workdir` argument and stamps it as a JSON key. The eventual caller (#338B) is responsible for shape-validating `workdir` against flag-validation rules (`os.Stat` regular-dir, as #337's spec already enumerates). #341's contract is "treat workdir as the key; preserve everything else."
- **[Tokens, secrets, credentials]** `~/.claude.json` is not pyry's data — it may contain tokens or other sensitive fields claude wrote. SHOULD FIX → addressed inline: the package doc-comment explicitly forbids logging file contents at any layer (see § "Logging discipline"); error wraps name steps and paths but never echo bytes or unmarshalled values. The pass-through `map[string]any` discipline (combined with `UseNumber`) preserves the secrets without ever requiring pyry to know what's in them.
- **[File operations]**
  - Path traversal: both paths (`<home>/.claude.json` and `<home>/.claude.json.lock`) join under `homeDir`; `homeDir` is operator-supplied (production: `os.UserHomeDir`), not user-input. No traversal risk.
  - TOCTOU: the flock on the sibling lock file is acquired BEFORE the data-file read and held across the rename. The read-modify-write window is fully covered. The sibling-file choice (rather than locking the data file itself) is the specific countermeasure for the inode-swap-via-rename race documented in § "Lock strategy".
  - Symlinks on the data file: spec acknowledges (open questions) that `os.Rename` replaces a symlinked target with a regular file. Same-uid, intentional, matches other atomic-rename writers in the project; not an exploit vector.
  - Permissions: lock file forced to `0o600`; data file preserves existing mode (or `0o600` when creating). Spec is explicit (§ "File mode").
  - Atomic writes: yes, mandatory, mirrors `internal/devices/registry.go:Save` (§ "Write step").
- **[Subprocess / external command execution]** N/A — no subprocess.
- **[Cryptographic primitives]** N/A — no crypto.
- **[Network & I/O]** No network. Local file. Resource exhaustion: a hostile-sized `~/.claude.json` would be read entirely into memory via the default `json.Decoder`. The attacker who can grow that file is the same uid as pyry; same trust boundary as the running user. No size cap needed (out of scope: a future per-file size cap would be a generic hardening, not specific to this helper).
- **[Error messages, logs, telemetry]** Addressed inline (§ "Logging discipline"). Error wraps name the step (`encode` / `fsync` / `rename` / `lock acquire` / `read` / `parse`) and the path. They do NOT include file bytes or unmarshalled fields beyond the workdir key the caller already supplied.
- **[Concurrency]** Single lock (`flock` on sibling lock file). Held across read + rename. No nested locks. No goroutines spawned. Process-crash safety: flock auto-released by the kernel on fd close (including process exit); data file is either pre- or post-rename, never partial.
- **[Threat model alignment]** Local-file CLI helper; threat model is "another process on the same machine". Cross-uid: home-dir permissions already protect against other-uid readers; orthogonal to this helper. Same-uid: trusted per the Unix model.

**MUST FIX in spec (resolved inline before this verdict):**

- Numeric precision: `json.Decoder.UseNumber()` is mandatory, not optional (§ "Numeric precision"). Without it, round-trips silently corrupt int64-sized values, breaking the "preserved verbatim" AC. The dedicated test row pins it.
- Logging discipline: pinned in the package doc-comment + spec (§ "Logging discipline"). Prevents accidental token leakage via debug logs in #338B's wire-up.

**Reviewer:** architect (self-review per `agents/architect/security-review.md`)
**Date:** 2026-05-14
