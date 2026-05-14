# `internal/agentrun` — workspace-trust helper

Pre-populates the workspace-trust flag in `~/.claude.json` so headless `claude` invocations launched from `pyry agent-run` (#338B) skip the trust-dialog TUI that would otherwise block startup. The package also exports the path-resolution primitive that the JSONL watcher (#333) will consume so the key shape used to talk to claude's on-disk state lives in one place.

Phase A spike (#329) verified pre-writing `projects[<realpath(workdir)>].hasTrustDialogAccepted = true` side-steps the dialog reliably; driving the dialog via PTY would be fragile under timing variance.

## Public API

Stdlib only. Two functions; no types; no constructor.

```go
// ResolveWorkdir returns the resolved absolute path of workdir, mirroring how
// claude resolves a workdir before reading ~/.claude.json's projects map.
func ResolveWorkdir(workdir string) (string, error)

// MarkWorkdirTrusted sets projects[<ResolveWorkdir(workdir)>].
// hasTrustDialogAccepted = true in <homeDir>/.claude.json, under a file lock
// spanning the entire read-modify-write window. Idempotent. Atomic on-disk.
func MarkWorkdirTrusted(homeDir, workdir string) error
```

`homeDir` is explicit (not `os.UserHomeDir`) so tests use `t.TempDir()` directly without `t.Setenv("HOME", ...)` (which blocks `t.Parallel`). Production callers pass `os.UserHomeDir()`. Same shape as `internal/install.ResolveWorkDir` — the name overlap is package-scoped (`install.ResolveWorkDir` validates a CLI flag → absolute path; `agentrun.ResolveWorkdir` resolves an absolute path → realpath; orthogonal jobs).

## Key shape

`projects` map keys are the **resolved** absolute path. The macOS `/var → /private/var` symlink means a non-resolved key never matches claude's lookup. `ResolveWorkdir` does `filepath.Abs` then `filepath.EvalSymlinks`. The same pattern is used in `internal/sessions/rotation/watcher.go` for path comparison against the platform probe.

Scope note: the dashed `-private-var-folders-...` encoding used under `~/.claude/projects/` directory naming is a **different** encoding, not handled by this package.

## Lock strategy — sibling file, not data file

`flock(2)` on `~/.claude.json.lock`, NOT on `~/.claude.json` directly.

The atomic-write recipe replaces the data file's inode on every write. A second writer that opens the data file *after* the first writer's rename gets a different inode and acquires its own independent lock — the locks would not serialize. A stable sibling lock file (whose inode is never replaced) is the standard fix. **Any future helper that takes a file-lock around an atomic-rename writer in this codebase must follow the same pattern.**

- Lock file opened `os.OpenFile(lockPath, O_CREATE|O_RDWR, 0o600)`; never deleted (deletion races acquisition).
- `syscall.Flock(fd, LOCK_EX)` — blocking; no nonblocking-acquire requirement.
- `syscall.Flock` is stdlib and works on darwin + linux (BSD flock on both); no build tag needed.
- Crash-safety: kernel releases flock on fd close (including process exit). Data file is either pre- or post-rename; never partial.
- Within-process: two goroutines calling `MarkWorkdirTrusted` each open their own fd and Flock — kernel serializes. The concurrency AC test drives exactly this case.

## Pass-through preservation

`~/.claude.json` is not pyry's data — claude may store tokens, timestamps, counters, and other fields pyry does not own. The helper round-trips through `map[string]any` so unknown fields survive verbatim.

**`json.Decoder.UseNumber()` is mandatory.** Default decoding drops JSON numbers into `float64`, losing precision above 2^53; `~/.claude.json` may carry int64-sized values (e.g. `lastLoginNanos`). The pinning test `TestMarkWorkdirTrusted_PreservesNumericPrecision` writes a known-precision-eater (`1763123456789012345`) and asserts a byte-identical round-trip.

`encoding/json` sorts map keys alphabetically on marshal, so output is deterministic for the same logical content. Combined with `UseNumber`, this gives byte-identical idempotency for repeated calls.

## Atomic write

Mirrors `internal/devices/registry.go:Save` line-for-line:

1. `os.CreateTemp(homeDir, ".claude.json.tmp-*")` — same dir as rename target so the rename is intra-filesystem.
2. `os.Chmod(tmp.Name(), mode)` — preserves the existing file's mode, or `0o600` when creating.
3. `enc.SetIndent("", "  ")` + `enc.Encode(root)`.
4. `tmp.Sync()` → `tmp.Close()` → `os.Rename(tmp.Name(), dataPath)`.
5. `defer os.Remove(tmp.Name())` best-effort cleanup if any step above fails.

The flock is held across read, encode, fsync, AND rename — the entire RMW window.

## Error handling

Three terminal classes:

1. **`fs.ErrNotExist` on the data file** — not an error; helper creates a fresh `{"projects": {<key>: {"hasTrustDialogAccepted": true}}}` skeleton. Parent directory (`$HOME`) is assumed to exist.
2. **Malformed input** (unparseable JSON, `projects` not an object, `projects[key]` not an object) — wrapped error. The helper refuses to silently destroy state it doesn't understand. File untouched on these failures (pin: `TestMarkWorkdirTrusted_MalformedJSONFailsLoudly` asserts byte-identical pre/post).
3. **I/O failure** (read, fsync, rename, flock) — wrapped error with step name.

No retries. The caller (`pyry agent-run` in #338B) surfaces the failure as the verb's exit-1 path.

## Logging discipline

The package doc-comment is load-bearing:

```
MUST NOT log file contents at any layer. ~/.claude.json may contain
tokens or claude-internal state pyry does not own; the helper takes a
pass-through view (preserve fields verbatim) and emits a key+verdict on
success, not the underlying bytes.
```

Error wraps name the step (`encode` / `fsync` / `rename` / `lock acquire` / `read` / `parse`) and the file path. They MUST NOT include file bytes or unmarshalled fields beyond the workdir key the caller already supplied. No `slog` calls inside the helper — the eventual caller (#338B) logs success at the verb level.

## Concurrency model

No goroutines spawned. Purely sequential within an invocation: lock → read → mutate → write → unlock. No `context.Context` parameter — the operation is fast-bounded. If a future caller needs cancellable-acquire, add a context-taking sibling without changing this signature.

## Consumers

- `pyry agent-run` (#338B, immediately downstream) — calls `MarkWorkdirTrusted(os.UserHomeDir(), args.workdir)` after flag validation, before claude spawn.
- JSONL watcher (#333) — calls `ResolveWorkdir` to compute the same key shape claude uses when associating watched files with workdirs.

## Out of scope

- Cross-process flock testing — kernel-level, same code path as within-process Flock.
- A pyrycode-wide atomic-write helper — convention is "duplicated until a fifth registry forces extraction" (`internal/devices/registry.go:Save` and this package are the third and fourth).
- Windows port — pyrycode targets darwin + linux only.

## Related

- [pyry-agent-run-command.md](pyry-agent-run-command.md) — the verb that consumes `MarkWorkdirTrusted`.
- [rotation-watcher.md](rotation-watcher.md) — existing user of the same `EvalSymlinks` pattern for path comparison against claude-resolved paths.
- [devices-registry.md](devices-registry.md) — the canonical atomic-write recipe this package mirrors.
