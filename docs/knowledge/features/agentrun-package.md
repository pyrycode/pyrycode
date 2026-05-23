# `internal/agentrun` — workdir helpers and shared types for `pyry agent-run`

> **Post-#392, post-#508 surface.** The parent `internal/agentrun` package hosts a single exported function: `ResolveWorkdir`. The PTY driver (`Drive` / `DriveConfig`), the per-spawn settings writer (`WriteSettings` / `SettingsFilename`), and the original sibling-file `MarkWorkdirTrusted` were all deleted in [#392](https://github.com/pyrycode/pyrycode/issues/392) when stream-json subprocess mode replaced PTY drive; the workdir-encoder wrapper `EncodeProjectDir` was deleted in [#508](../codebase/508.md) after the encoder fully migrated to [`tui-driver/pkg/tuidriver.EncodeCwd`](https://github.com/pyrycode/tui-driver). The 2026-05-19 pivot back to PTY drive ([codebase/471.md](../codebase/471.md)) resurrected the spawn primitive, the trust pre-write, and the settings writer as **sibling subpackages** ([`ptyrunner`](ptyrunner-package.md), [`trust`](agentrun-trust-subpackage.md), [`settings`](agentrun-settings-subpackage.md)). See § "Subpackages" below. [#516](https://github.com/pyrycode/pyrycode/issues/516) tracks deletion of `ResolveWorkdir` itself once [`trust`](agentrun-trust-subpackage.md) migrates off it.

Stdlib-only helper for `pyry agent-run`. The parent package's residual responsibility is **`projects[...]` key canonicalisation** — the macOS `/var → /private/var` realpath rule used by `agentrun/trust` to key into `~/.claude.json`'s projects map. The dashed `~/.claude/projects/<encoded>/` directory-name encoding lives in [`tui-driver/pkg/tuidriver.EncodeCwd`](https://github.com/pyrycode/tui-driver), not here.

## Public API

```go
// ResolveWorkdir returns the resolved absolute path of workdir, mirroring how
// claude resolves a workdir before reading ~/.claude.json's projects map.
// Sole remaining caller after #508: internal/agentrun/trust.
func ResolveWorkdir(workdir string) (string, error)
```

Same shape as `internal/install.ResolveWorkDir` — the name overlap is package-scoped (`install.ResolveWorkDir` validates a CLI flag → absolute path; `agentrun.ResolveWorkdir` resolves an absolute path → realpath; orthogonal jobs).

## Subpackages

| Subpackage | Doc | Purpose |
| --- | --- | --- |
| `internal/agentrun/trust` | [agentrun-trust-subpackage.md](agentrun-trust-subpackage.md) | Pre-mark workdirs trusted in `~/.claude.json` so PTY-driven claude skips the workspace-trust modal (#475; slimmed resurrection of #341 / #392). |
| `internal/agentrun/settings` | [agentrun-settings-subpackage.md](agentrun-settings-subpackage.md) | Write the per-spawn deny-default permissions JSON (`{"permissions":{"allow":[...],"defaultMode":"deny"}}`) to `os.TempDir()` so PTY-driven claude enforces the same tool whitelist `-p --allowedTools` had natively (#476; slimmed resurrection of #339 / #392). |
| `internal/agentrun/ptyrunner` | [ptyrunner-package.md](ptyrunner-package.md) | Interactive-TUI spawn primitive driven by `tui-driver` — the production spawn after #470 lands the cutover (#471). |
| `internal/agentrun/streamrunner` | [streamrunner-package.md](streamrunner-package.md) | Stream-json subprocess spawn primitive — the current production spawn until #470 cuts over to `ptyrunner`. |
| `internal/agentrun/jsonl` | [jsonl-reader.md](jsonl-reader.md) | Pure JSONL line reader + deterministic end-of-turn detector (#348). Post-[#512](../codebase/512.md) consumed only by `selfcheck` (parses the `streamjson.Emitter` pipe) and `e2e/realclaude/fixtures.go` (parses captured fixtures); both will migrate in a follow-up that finally deletes the package. |
| `internal/agentrun/budget` | [budget-package.md](budget-package.md) | `Counter` enforces the per-agent `--max-turns` budget; consumed by `ptyrunner.Run` (#334 / #479 / #512). |

Only the `trust` subpackage imports `internal/agentrun` after [#508](../codebase/508.md) (for `ResolveWorkdir` — the `projects[...]` key shape). The `ptyrunner`, `streamrunner`, and pre-#512 `jsonl/tail` subpackages used to import the parent for `EncodeProjectDir` but now call [`tui-driver/pkg/tuidriver.SessionJSONLPath`](https://github.com/pyrycode/tui-driver) (or `EncodeCwd`) directly — the canonicalisation lives inside tui-driver. The `jsonl/tail` subpackage was deleted in [#512](../codebase/512.md) (`ptyrunner.Run` drains `tuidriver.TailJSONL` inline). `settings` has never imported this parent package; it has no workdir input (writes to `os.TempDir()`) and is stdlib-only.

## Key shape

`projects` map keys are the **resolved** absolute path. The macOS `/var → /private/var` symlink means a non-resolved key never matches claude's lookup. `ResolveWorkdir` does `filepath.Abs` then `filepath.EvalSymlinks`. The same pattern is used in `internal/sessions/rotation/watcher.go` for path comparison against the platform probe.

## Encoder lives in tui-driver

`ResolveWorkdir` produces the resolved abs path (`/private/var/folders/...`) used as the `projects[...]` key inside `~/.claude.json`. The dashed directory-name segment claude uses under `~/.claude/projects/<encoded-cwd>/<sid>.jsonl` is produced by [`tui-driver/pkg/tuidriver.EncodeCwd`](https://github.com/pyrycode/tui-driver). The two encodings are distinct (same logical input, different transforms for different consumers) but they no longer share a wrapper here — pre-[#508](../codebase/508.md) `EncodeProjectDir` chained `ResolveWorkdir` then `EncodeCwd`, which resolved symlinks twice (linux) or did `EvalSymlinks`-then-`F_GETPATH` (darwin). #508 deleted the wrapper; callers that need the dashed name now use `tuidriver.EncodeCwd(workdir)` directly (or `tuidriver.SessionJSONLPath(home, workdir, sessionID)` for the full path), and `tuidriver.EncodeCwd`'s internal `canonicalisePath` handles the realpath rule once (darwin: `F_GETPATH` via fd; linux: `filepath.EvalSymlinks`).

The substitution rule (canonicalised in `tuidriver.EncodeCwd` as of 2026-05-19) is **every byte outside `[a-zA-Z0-9]` → `-`** (per-byte, no run collapse — idempotent on already-encoded strings because `-` itself is non-alnum and maps to `-`). The rule is verified empirically against claude's on-disk behaviour. The #347 ticket body specified only `/` → `-`; the architect spec amended to `/` AND `.` from direct observation (workdir segments like `/.pyrycode-worktrees/` encoding to `--pyrycode-worktrees-`); [#501](../codebase/501.md) generalised the rule to the full character class after the narrow encoder caused a 55 s wedge in real-claude e2e tests for any workdir containing `_` or ` ` (Go test temp dirs whose names include underscores). Shipping anything narrower than the full per-byte rule produces keys that never match claude's directory names for non-trivial inputs.

**Single source of truth.** The encoder lives in `tui-driver/pkg/tuidriver.EncodeCwd` (the package closest to claude's protocol). The pre-#508 `EncodeProjectDir` wrapper that delegated to it is gone — there is no `internal/agentrun`-side replacer left. Two narrow-replacer call sites in `internal/sessions/reconcile.go` and `internal/e2e/rotation_test.go` still carry the old `/`-and-`.` rule (flagged as a follow-up since [#501](../codebase/501.md)); the "single source of truth" property is `internal/agentrun/`-scoped today, not yet project-wide.

## Consumers

- `internal/agentrun/trust` — calls `ResolveWorkdir(workdir)` to produce the `projects[...]` key inside `~/.claude.json`. Sole remaining production caller after [#508](../codebase/508.md); [#516](https://github.com/pyrycode/pyrycode/issues/516) tracks migrating this call off `ResolveWorkdir` so the parent package can be deleted. See [agentrun-trust-subpackage.md](agentrun-trust-subpackage.md).
- `internal/agentrun/settings` — does **not** import this parent package; writes its tempfile to `os.TempDir()` and is stdlib-only. See [agentrun-settings-subpackage.md](agentrun-settings-subpackage.md).
- `internal/agentrun/ptyrunner` — does not import this parent post-[#508](../codebase/508.md). Receives its `Config.WorkDir` from the caller; the trust pre-write supplies the resolved value via `trust.MarkWorkdirTrusted`'s return.
- JSONL watcher (`internal/agentrun/jsonl/tail`) — **deleted by [#512](../codebase/512.md)**. `ptyrunner.Run` drains [`tuidriver.TailJSONL`](https://github.com/pyrycode/tui-driver) inline; the canonicalisation rule and the dashed-name encoding live inside tui-driver.
- `internal/sessions/rotation/watcher.go` — uses `ResolveWorkdir` for path comparison against the platform probe.

## History — deleted surfaces

Three surfaces lived in this package historically and were removed in #392 when stream-json subprocess mode replaced PTY drive:

| Removed symbol | #392 outcome | Resurrected as |
| --- | --- | --- |
| `MarkWorkdirTrusted(homeDir, workdir string) error` | deleted | [`internal/agentrun/trust`](agentrun-trust-subpackage.md) subpackage (#475, slimmed signature `(workdir) (realpath, error)`, no flock) |
| `WriteSettings(workdir, allowed) (string, error)` + `SettingsFilename` | deleted | [`internal/agentrun/settings`](agentrun-settings-subpackage.md) subpackage (#476, slimmed signature `(allowedTools) (string, error)`, writes to `os.TempDir()` with random suffix, no atomic-rename, caller-owned cleanup) |
| `Drive` + `DriveConfig` | deleted | [`internal/agentrun/ptyrunner`](ptyrunner-package.md) subpackage (#471), driven via `tui-driver` instead of a hand-rolled `supervisor.SpawnPTY` scripted sequence |

The 2026-05-19 pivot back to PTY drive (#329 tracking) drives all three resurrections. The new shape sits in sibling subpackages rather than the parent package because the spawn / trust / settings concerns are now package-scoped (each with its own dependency direction and test surface). See [codebase/392.md](../codebase/392.md) for the deletion context, [codebase/475.md](../codebase/475.md) for the trust resurrection, and [codebase/476.md](../codebase/476.md) for the settings resurrection.

## Out of scope

- A pyrycode-wide atomic-write helper — convention is "duplicated until a fifth registry forces extraction." See [devices-registry.md](devices-registry.md) for the canonical recipe each subpackage mirrors.
- Windows port — pyrycode targets darwin + linux only.

## Related

- [agentrun-trust-subpackage.md](agentrun-trust-subpackage.md) — `internal/agentrun/trust`, the workspace-trust pre-write (#475).
- [agentrun-settings-subpackage.md](agentrun-settings-subpackage.md) — `internal/agentrun/settings`, the per-spawn deny-default permissions JSON writer (#476).
- [ptyrunner-package.md](ptyrunner-package.md) — `internal/agentrun/ptyrunner`, the interactive-TUI spawn primitive (#471).
- [streamrunner-package.md](streamrunner-package.md) — `internal/agentrun/streamrunner`, the stream-json spawn primitive (#390).
- [jsonl-reader.md](jsonl-reader.md) — `internal/agentrun/jsonl` (#348), the pure JSONL line reader + deterministic end-of-turn detector. Post-[#512](../codebase/512.md) consumed only by `selfcheck` and `e2e/realclaude/fixtures.go`; the tail-watcher consumer is gone.
- [pyry-agent-run-command.md](pyry-agent-run-command.md) — the verb that consumes the subpackages.
- [rotation-watcher.md](rotation-watcher.md) — existing user of the same `EvalSymlinks` pattern for path comparison against claude-resolved paths.
- [devices-registry.md](devices-registry.md) — the canonical atomic-write recipe each subpackage mirrors.
