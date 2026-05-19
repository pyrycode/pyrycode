# `internal/agentrun/trust` — pre-mark workdirs trusted in `~/.claude.json`

Pre-writes `projects[<realpath(workdir)>].hasTrustDialogAccepted = true` in `~/.claude.json` so interactive `claude` (spawned by [`ptyrunner`](ptyrunner-package.md) under PTY drive) skips the workspace-trust modal at startup. The dispatcher's automated flow has no human present to dismiss that modal; pre-marking side-steps it entirely.

Introduced [#475](https://github.com/pyrycode/pyrycode/issues/475) as a **slimmed resurrection** of the helper [#341](https://github.com/pyrycode/pyrycode/issues/341) shipped and [#392](https://github.com/pyrycode/pyrycode/issues/392) deleted. The 2026-05-19 pivot back to PTY drive ([`codebase/471.md`](../codebase/471.md)) made the modal a problem again. The slimming drops the cross-process flock the original required, because `ptyrunner`'s post-idle `HasTrustModal` detector is the runtime safety net.

## Public API

```go
// MarkWorkdirTrusted ensures
//   ~/.claude.json :: projects[<realpath(workdir)>].hasTrustDialogAccepted = true
// Idempotent. Atomic — writes to a tempfile in the same directory then renames
// over the target. Returns the resolved realpath on success.
func MarkWorkdirTrusted(workdir string) (realpath string, err error)
```

No exported types, no constructor, one function. Returns the resolved realpath so the caller (#470's `runAgentRun`) can pass it straight to `ptyrunner.Config.WorkDir` without a second `agentrun.ResolveWorkdir` call.

## Internal test seam

The public wrapper is two lines: `os.UserHomeDir()` then delegate to unexported `markWorkdirTrustedIn(homeDir, workdir string) (string, error)`. Tests pass `t.TempDir()` directly as `homeDir`, which lets them run `t.Parallel()` — Go's testing docs forbid `t.Setenv` (the only alternative way to redirect `os.UserHomeDir`) from a `t.Parallel` ancestor. The behavioural matrix targets the parallel-safe seam; a single non-parallel smoke test exercises the public `MarkWorkdirTrusted` via `t.Setenv("HOME", ...)` to pin the `os.UserHomeDir` plumbing.

## Why a subpackage instead of `internal/agentrun/trust.go`

#341 lived as a sibling file under `internal/agentrun/`. The subpackage layout (`internal/agentrun/trust/`) was chosen for #475 to mirror the sibling spawn primitives `internal/agentrun/ptyrunner/` and `internal/agentrun/streamrunner/`. The parent `internal/agentrun` package now hosts only workdir helpers (`ResolveWorkdir`, `EncodeProjectDir`) that all three subpackages import; spawn concerns and trust concerns are package-scoped, not file-scoped.

## Key shape — realpath, not abspath

`projects` map keys are the **`filepath.EvalSymlinks`-resolved** absolute path. The macOS `/var → /private/var` symlink means a non-resolved key never matches claude's lookup. The helper delegates to `agentrun.ResolveWorkdir`, which does `filepath.Abs` then `filepath.EvalSymlinks` — the single pyrycode-wide source of truth for "claude's realpath rule." `internal/sessions/rotation/watcher.go` uses the same helper for path comparison against platform-probe results.

## Pass-through preservation

`~/.claude.json` is not pyry's data — claude stores OAuth tokens, timestamps, counters, and other fields pyry does not own. The helper round-trips through `map[string]any` so unknown fields survive verbatim.

**`json.Decoder.UseNumber()` is mandatory.** Default decoding drops JSON numbers into `float64`, losing precision above 2^53; `~/.claude.json` may carry int64-sized values (e.g. `lastLoginNanos`). The pinning test `TestMarkWorkdirTrusted_PreservesNumericPrecision` writes a known precision-eater (`1763123456789012345`) and asserts the value round-trips byte-identically.

`encoding/json` sorts map keys alphabetically on marshal, so output is deterministic for the same logical content. Combined with `UseNumber`, this gives byte-identical idempotency for repeated calls (pinned by `TestMarkWorkdirTrusted_IdempotentPreservesExtraEntryFields`).

## No lock — best-effort cross-process write

Unlike #341, this helper holds no file-lock. Concurrent invocations against the same `~/.claude.json` (e.g. pyry pre-writing while the user's interactive claude rewrites its own config) can race. The worst case is a lost update — one writer's `projects` entry overwrites the other's — which means pyry's trust pre-mark may not be present when `ptyrunner` spawns claude.

The safety net is `ptyrunner.Run`'s post-idle `HasTrustModal(snap)` detector. If the modal appears because pre-marking lost the race, `ptyrunner` returns `ErrTrustModalDetected` and the operator-facing surface (cutover in #470) names the remediation hint (which still says "#469's `MarkWorkdirTrusted`" — the issue this ticket was split out of).

**The single-writer atomic-write property is preserved.** A SIGKILL'd pyry mid-write must not leave `~/.claude.json` torn for the user's own interactive claude sessions. Tempfile + `Sync` + `Close` + `os.Rename` guarantees that the rename point is either pre- or post-write; the file is never partial.

If a future incident shows the racing-writers case is painful (e.g. `HasTrustModal`'s detection is too slow under some specific timing), the fix is a follow-up ticket that re-introduces flock — not retrofitting it here. *Evidence-Based Fix Selection*: defend an observed failure, don't pre-defend a hypothetical one.

## Atomic write

Mirrors `internal/devices/registry.go:Save` line-for-line (the canonical pyrycode atomic-write recipe — convention is duplication-not-extraction until a fifth registry forces it):

1. `os.CreateTemp(homeDir, ".claude.json.tmp-*")` — same dir as rename target so the rename is intra-filesystem.
2. `defer os.Remove(tmp.Name())` — best-effort cleanup if any later step fails.
3. `os.Chmod(tmp.Name(), mode)` — preserves the existing file's mode, or `0o600` when creating.
4. `enc.SetIndent("", "  ")` + `enc.Encode(root)`.
5. `tmp.Sync()` → `tmp.Close()` → `os.Rename(tmp.Name(), dataPath)`.

Each step wraps errors as `fmt.Errorf("agentrun/trust: <step>: %w", err)` naming the step (`create temp` / `chmod temp` / `encode` / `fsync` / `close temp` / `rename` / `stat` / `read` / `parse` / `home dir`). They MUST NOT include file bytes or unmarshalled fields.

## File mode

- If `~/.claude.json` already exists, copy its mode to the temp file before rename so we don't quietly tighten or loosen the user's file. Claude defaults to `0o644` in practice; pyry does not override.
- If absent, create at `0o600`.
- Mode is informational, not a security boundary (single-user data inside `$HOME`); a single `os.Stat` before the read is sufficient — no double-stat dance.

Pinned by `TestMarkWorkdirTrusted_PreservesFileMode`.

## Error handling

Three terminal classes:

1. **`fs.ErrNotExist` on the data file** — not an error; helper creates a fresh `{"projects": {<key>: {"hasTrustDialogAccepted": true}}}` skeleton. Parent directory (`$HOME`) is assumed to exist.
2. **Malformed input** (unparseable JSON, `projects` not an object, `projects[realpath]` not an object) — wrapped error; the file is left untouched (pinned via `bytes.Equal` pre/post in `TestMarkWorkdirTrusted_MalformedJSONFails`, `TestMarkWorkdirTrusted_ProjectsNotObjectFails`, and `TestMarkWorkdirTrusted_EntryNotObjectFails`). The helper refuses to silently destroy state it doesn't understand.
3. **I/O failure** (read, stat, chmod, encode, fsync, close, rename) — wrapped error with the step name.

**Workdir-missing short-circuits via `ResolveWorkdir` BEFORE any `~/.claude.json` access** — pinned by `TestMarkWorkdirTrusted_WorkdirMissingReturnsError`. The error wraps `fs.ErrNotExist` (via `errors.Is`) and `~/.claude.json` is not created. The eventual caller (#470) surfaces the failure as the verb's exit-1 path with a `pyry: agent-run: …` stderr line.

No retries. The caller chooses.

## Logging discipline

The package doc-comment is load-bearing:

```
MUST NOT log file contents at any layer. ~/.claude.json may contain
tokens or claude-internal state pyry does not own; the helper takes a
pass-through view (preserve fields verbatim) and emits nothing to logs.
```

- No `slog` calls inside the helper.
- Error wraps name the step + path; never file bytes or unmarshalled fields.
- AC's "MUST NOT log workdir paths beyond the resolved realpath returned" is satisfied trivially — the helper makes no log calls.

## Concurrency model

No goroutines spawned. Purely sequential within an invocation: stat → read → mutate → write. No `context.Context` parameter — the operation is fast-bounded (local filesystem read + write). If a future caller needs cancellable-acquire, add a context-taking sibling without changing this signature.

Cross-process concurrent invocations are explicitly **not** serialised — see § "No lock" above.

## Dependency direction

- Stdlib: `bytes`, `encoding/json`, `errors`, `fmt`, `io/fs`, `os`, `path/filepath`.
- Internal: `github.com/pyrycode/pyrycode/internal/agentrun` (for `ResolveWorkdir` only).
- External: none.

## Testing

`internal/agentrun/trust/trust_test.go` — same-package, stdlib `testing` only, no testify.

Each behavioural test uses `home := t.TempDir()` + `wd := t.TempDir()` and calls `markWorkdirTrustedIn(home, wd)`. All nine behavioural tests call `t.Parallel()`. Two helpers — `writeJSON(t, path, root, mode)` (encode + write + chmod for fixtures) and `readJSON(t, path)` (decode with `UseNumber` for assertions) — keep test bodies focused.

Test cases:

- `TestMarkWorkdirTrusted_CreatesFileWhenMissing` — no pre-existing file → creates mode-`0o600` file with one-entry skeleton.
- `TestMarkWorkdirTrusted_AddsToExistingFileWithoutProjects` — pre-existing top-level fields (`userID`, `telemetry`) preserved; `projects` added.
- `TestMarkWorkdirTrusted_PreservesSiblingProjects` — pre-existing `projects["/some/other/path"]` with its own `hasTrustDialogAccepted: false` + `extra` field survives untouched alongside the new entry.
- `TestMarkWorkdirTrusted_IdempotentPreservesExtraEntryFields` — pre-existing target entry with `mcpServers` subfield → repeat call produces byte-identical output (pins idempotency AND within-entry preservation).
- `TestMarkWorkdirTrusted_MalformedJSONFails` — pre-existing file containing `"not json"` → non-nil error; file bytes unchanged.
- `TestMarkWorkdirTrusted_WorkdirMissingReturnsError` — workdir does not exist → `errors.Is(err, fs.ErrNotExist)`; `~/.claude.json` not created.
- `TestMarkWorkdirTrusted_WorkdirSymlinkResolvesToRealpath` — `os.Symlink(target, link)`, call with `link` → returned realpath equals `agentrun.ResolveWorkdir(target)` (NOT `link`); `projects` has one entry under the resolved key.
- `TestMarkWorkdirTrusted_PreservesNumericPrecision` — pre-existing `lastLoginNanos: 1763123456789012345` → value round-trips through `json.Number.String()` byte-identically.
- `TestMarkWorkdirTrusted_PreservesFileMode` — pre-existing file at `0o644` → post-rename mode still `0o644`.
- `TestMarkWorkdirTrusted_ProjectsNotObjectFails` — pre-existing `{"projects": "not an object"}` → non-nil error; file untouched.
- `TestMarkWorkdirTrusted_EntryNotObjectFails` — pre-existing `{"projects": {<realpath>: "not an object"}}` → non-nil error; file untouched.
- `TestMarkWorkdirTrusted_PublicSmoke` (non-parallel) — `t.Setenv("HOME", t.TempDir())` → `MarkWorkdirTrusted(wd)` succeeds and writes the expected entry; pins `os.UserHomeDir` plumbing without duplicating the full behavioural matrix.

## What this helper deliberately does NOT do

- **No cross-process serialisation.** No flock. Concurrent writers may produce lost updates; `ptyrunner.HasTrustModal` is the runtime safety net.
- **No retries.** Caller decides.
- **No logging.** Operator-visible diagnostics happen at the consumer (#470).
- **No size cap on `~/.claude.json`.** A hostile-sized file is the same-uid threat model as pyry; same trust boundary as the running user. Generic hardening, not specific to this helper.

## Consumers

- `pyry agent-run` (cutover in #470) — after flag validation, calls `MarkWorkdirTrusted(parsed.workdir)`, then passes the returned realpath to `ptyrunner.Config.WorkDir`. The trust pre-write must run before `ptyrunner.Run` to side-step the modal; if pre-writing fails the verb exits 1 (the helper surfaces the failure; the caller chooses to abort).

## Out of scope

- The ptyrunner consumer → [`ptyrunner-package.md`](ptyrunner-package.md) (#471 / #472).
- The `cmd/pyry/agent_run.go` wiring → #470.
- Cross-process concurrency serialisation — explicitly descoped; revisit only on an observed failure.

## Related

- [agentrun-package.md](agentrun-package.md) — surrounding parent package; `ResolveWorkdir` (the realpath rule) lives there.
- [ptyrunner-package.md](ptyrunner-package.md) — the spawn primitive that consumes the pre-marked trust state and provides the runtime safety net via the post-idle `HasTrustModal` detector. `ErrTrustModalDetected`'s message embeds the remediation hint.
- [devices-registry.md](devices-registry.md) — the canonical atomic-write recipe this package mirrors.
- [rotation-watcher.md](rotation-watcher.md) — existing user of the same `EvalSymlinks`-via-`ResolveWorkdir` pattern.
- [`codebase/475.md`](../codebase/475.md) — build notes (file inventory, patterns, lessons).
- [`docs/specs/architecture/475-agentrun-trust-helper.md`](../../specs/architecture/475-agentrun-trust-helper.md) — architect spec.
- [`codebase/392.md`](../codebase/392.md) — the deletion this ticket reverses.
- [`codebase/341.md`](../codebase/341.md) — the original (pre-deletion) helper; this slimmed version's contract is a strict subset.
