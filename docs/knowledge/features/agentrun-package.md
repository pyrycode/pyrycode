# `internal/agentrun` — workdir helpers and shared types for `pyry agent-run`

> **Post-#392 surface.** The parent `internal/agentrun` package now hosts only **workdir helpers** (`ResolveWorkdir` + `EncodeProjectDir`). The PTY driver (`Drive` / `DriveConfig`), the per-spawn settings writer (`WriteSettings` / `SettingsFilename`), and the original sibling-file `MarkWorkdirTrusted` were all deleted in [#392](https://github.com/pyrycode/pyrycode/issues/392) when stream-json subprocess mode replaced PTY drive. The 2026-05-19 pivot back to PTY drive ([codebase/471.md](../codebase/471.md)) resurrected the spawn primitive and the trust pre-write as **sibling subpackages** ([`ptyrunner`](ptyrunner-package.md), [`trust`](agentrun-trust-subpackage.md)). See § "Subpackages" below.

Stdlib-only helpers for `pyry agent-run`. The parent package's residual responsibility is **workdir encoding** — the macOS `/var → /private/var` realpath rule lives in one place so every consumer (`trust`, the JSONL watcher, `rotation/watcher.go`) shares the same definition of "claude's path key."

## Public API

```go
// ResolveWorkdir returns the resolved absolute path of workdir, mirroring how
// claude resolves a workdir before reading ~/.claude.json's projects map.
func ResolveWorkdir(workdir string) (string, error)

// EncodeProjectDir returns the dashed directory-name segment claude uses
// under ~/.claude/projects/ for the given workdir. Chains ResolveWorkdir
// then maps '/' AND '.' to '-' in the resolved absolute path.
func EncodeProjectDir(workdir string) (string, error)
```

Same shape as `internal/install.ResolveWorkDir` — the name overlap is package-scoped (`install.ResolveWorkDir` validates a CLI flag → absolute path; `agentrun.ResolveWorkdir` resolves an absolute path → realpath; orthogonal jobs).

## Subpackages

| Subpackage | Doc | Purpose |
| --- | --- | --- |
| `internal/agentrun/trust` | [agentrun-trust-subpackage.md](agentrun-trust-subpackage.md) | Pre-mark workdirs trusted in `~/.claude.json` so PTY-driven claude skips the workspace-trust modal (#475; slimmed resurrection of #341 / #392). |
| `internal/agentrun/ptyrunner` | [ptyrunner-package.md](ptyrunner-package.md) | Interactive-TUI spawn primitive driven by `tui-driver` — the production spawn after #470 lands the cutover (#471). |
| `internal/agentrun/streamrunner` | [streamrunner-package.md](streamrunner-package.md) | Stream-json subprocess spawn primitive — the current production spawn until #470 cuts over to `ptyrunner`. |
| `internal/agentrun/jsonl` | [jsonl-reader.md](jsonl-reader.md) | Pure JSONL line reader + deterministic end-of-turn detector consumed by the JSONL watcher (#348). |

All four subpackages import `internal/agentrun` for `ResolveWorkdir` / `EncodeProjectDir`; the realpath rule lives in the parent so the spawn / trust / watcher consumers share a single definition.

## Key shape

`projects` map keys are the **resolved** absolute path. The macOS `/var → /private/var` symlink means a non-resolved key never matches claude's lookup. `ResolveWorkdir` does `filepath.Abs` then `filepath.EvalSymlinks`. The same pattern is used in `internal/sessions/rotation/watcher.go` for path comparison against the platform probe.

## Two output shapes, two helpers

`ResolveWorkdir` produces the resolved abs path (`/private/var/folders/...`) used as the `projects[...]` key inside `~/.claude.json`. `EncodeProjectDir` (#347) chains it and applies `strings.NewReplacer("/", "-", ".", "-")` to produce the dashed directory-name segment claude uses under `~/.claude/projects/<encoded-cwd>/<sid>.jsonl`. The two encodings are distinct: same input, different transforms for different consumers. Both helpers share the resolve half so the macOS realpath rule lives in one place; only the post-resolve transform differs.

The substitution rule covers **both** `/` and `.`. Direct observation of `~/.claude/projects/` shows entries where a workdir segment like `/.pyrycode-worktrees/` encodes to `--pyrycode-worktrees-` — a doubled dash from `/` + `.`. The #347 ticket body specified only `/` → `-`; the architect spec amended the rule from observation and `docs/lessons.md:53`. Shipping the AC-literal rule would silently produce keys that never match claude's directory names for any workdir containing a `.` segment (`.git`, `.venv`, hidden-dir parents).

`EncodeProjectDir` returns `ResolveWorkdir`'s error **unchanged** on failure — `errors.Is(err, fs.ErrNotExist)` continues to work through the chain. Result does NOT include the `~/.claude/projects/` prefix or any `.jsonl` suffix; it is the directory-name segment only.

## Consumers

- `internal/agentrun/trust` — calls `ResolveWorkdir(workdir)` to produce the `projects[...]` key inside `~/.claude.json`. See [agentrun-trust-subpackage.md](agentrun-trust-subpackage.md).
- `internal/agentrun/ptyrunner` — passes a `ResolveWorkdir`-resolved path as `Config.WorkDir` (delegated from the trust pre-write's return value).
- JSONL watcher (#333, fsnotify wrapper #349) — calls `EncodeProjectDir` (#347) to compute the `~/.claude/projects/<encoded-cwd>/` directory name and `ResolveWorkdir` for any `projects[...]` key comparison; consumes [`internal/agentrun/jsonl`](jsonl-reader.md) (#348) for the per-turn line reader + deterministic end-of-turn detector.
- `internal/sessions/rotation/watcher.go` — uses `ResolveWorkdir` for path comparison against the platform probe.

## History — deleted surfaces

Three surfaces lived in this package historically and were removed in #392 when stream-json subprocess mode replaced PTY drive:

| Removed symbol | #392 outcome | Resurrected as |
| --- | --- | --- |
| `MarkWorkdirTrusted(homeDir, workdir string) error` | deleted | [`internal/agentrun/trust`](agentrun-trust-subpackage.md) subpackage (#475, slimmed signature `(workdir) (realpath, error)`, no flock) |
| `WriteSettings` + `SettingsFilename` | deleted | not yet resurrected; cutover ticket #469 (sibling of #475) will reintroduce the deny-default settings JSON writer for PTY drive |
| `Drive` + `DriveConfig` | deleted | [`internal/agentrun/ptyrunner`](ptyrunner-package.md) subpackage (#471), driven via `tui-driver` instead of a hand-rolled `supervisor.SpawnPTY` scripted sequence |

The 2026-05-19 pivot back to PTY drive (#329 tracking) drives all three resurrections. The new shape sits in sibling subpackages rather than the parent package because the spawn / trust / settings concerns are now package-scoped (each with its own dependency direction and test surface). See [codebase/392.md](../codebase/392.md) for the deletion context and [codebase/475.md](../codebase/475.md) for the trust resurrection.

## Out of scope

- A pyrycode-wide atomic-write helper — convention is "duplicated until a fifth registry forces extraction." See [devices-registry.md](devices-registry.md) for the canonical recipe each subpackage mirrors.
- Windows port — pyrycode targets darwin + linux only.

## Related

- [agentrun-trust-subpackage.md](agentrun-trust-subpackage.md) — `internal/agentrun/trust`, the workspace-trust pre-write (#475).
- [ptyrunner-package.md](ptyrunner-package.md) — `internal/agentrun/ptyrunner`, the interactive-TUI spawn primitive (#471).
- [streamrunner-package.md](streamrunner-package.md) — `internal/agentrun/streamrunner`, the stream-json spawn primitive (#390).
- [jsonl-reader.md](jsonl-reader.md) — `internal/agentrun/jsonl` (#348), the pure JSONL line reader + deterministic end-of-turn detector the JSONL watcher (#349) wraps.
- [pyry-agent-run-command.md](pyry-agent-run-command.md) — the verb that consumes the subpackages.
- [rotation-watcher.md](rotation-watcher.md) — existing user of the same `EvalSymlinks` pattern for path comparison against claude-resolved paths.
- [devices-registry.md](devices-registry.md) — the canonical atomic-write recipe each subpackage mirrors.
