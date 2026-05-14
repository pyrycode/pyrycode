# 347 — `EncodeProjectDir` helper for `~/.claude/projects/` dashed name

## Files to read first

- `internal/agentrun/trust.go:22-36` — `ResolveWorkdir` contract: `filepath.Abs` then `filepath.EvalSymlinks`, error wrapping format. The new helper chains this verbatim.
- `internal/agentrun/trust.go:1-20` — package doc + import set already present (`path/filepath`, `strings` not yet imported).
- `internal/agentrun/trust_test.go:15-68` — existing `ResolveWorkdir` test patterns: `runtime.GOOS != "darwin"` skip, `t.TempDir()`, `errors.Is(..., fs.ErrNotExist)`. The new tests mirror these shapes.
- `docs/lessons.md:52` — "Don't trust ticket bodies on filesystem layout — observe." The ticket AC says only `/` is replaced; observation under `~/.claude/projects/` shows `.` is ALSO replaced with `-` (e.g. `/Projects/.pyrycode-worktrees/` → `-Projects--pyrycode-worktrees-`). See Open Questions.

## Context

`pyry agent-run`'s JSONL watcher (#333, sibling ticket) and other future consumers need to compute the dashed directory name claude uses to store JSONL session files. That encoding rule must live in one named place — not be inlined as `strings.ReplaceAll` at each call site — so every consumer agrees on the key shape and a single test pins the rule against claude's actual on-disk layout.

`ResolveWorkdir` already handles the symlink-resolution half (macOS `/var → /private/var`). This ticket adds a sibling helper that chains `ResolveWorkdir` and the dash transformation.

## Design

Add `EncodeProjectDir` in `internal/agentrun/trust.go`, immediately after `ResolveWorkdir`:

```go
// EncodeProjectDir returns the dashed directory-name segment claude uses
// under ~/.claude/projects/ for the given workdir. Chains ResolveWorkdir
// then maps '/' and '.' to '-' in the resolved absolute path. The result
// does NOT include the ~/.claude/projects/ prefix or any .jsonl suffix.
func EncodeProjectDir(workdir string) (string, error)
```

Behavior contract:

- Returns `ResolveWorkdir(workdir)`'s error unchanged on failure (no extra wrapping). This preserves `errors.Is(err, fs.ErrNotExist)` for callers and keeps the error message identical to what `ResolveWorkdir` already produces.
- On success, applies `strings.NewReplacer("/", "-", ".", "-").Replace(resolved)` to the resolved path. The leading `/` of the absolute path becomes a leading `-` as a natural consequence — no special case.
- No `~/.claude/projects/` prefix prepended. No `.jsonl` suffix appended. Result is the directory-name segment only, as the AC requires.

Imports added to `internal/agentrun/trust.go`: `strings`. No new file. No new package. No new exported types.

### Why a `Replacer` and not two `ReplaceAll`s

`strings.NewReplacer` does a single pass over the string and is the idiomatic Go construct for "replace several runes with the same target". Two sequential `ReplaceAll`s would also work; the `Replacer` form makes the "both characters map to dash" rule visible at the call site and avoids the trap of someone later assuming the two replacements compose in order. The cost difference is negligible at this call frequency.

### Why a separate function and not folded into `ResolveWorkdir`

`ResolveWorkdir` is also used by `MarkWorkdirTrusted` to compute the `projects[...]` key in `~/.claude.json`. That key is the **resolved absolute path** (`/private/var/folders/...`), NOT the dashed encoding. Folding the dash transformation into `ResolveWorkdir` would break the existing caller. Keeping the two helpers as `ResolveWorkdir` → resolved abs path, `EncodeProjectDir` → dashed name, lets each caller pick the right shape.

## Concurrency model

None — pure function over its input. Safe to call concurrently from any goroutine.

## Error handling

Single failure mode: `ResolveWorkdir` returns an error (path does not exist, or `filepath.Abs` failed). Surface that error unchanged. Do not wrap — the wrapper would duplicate the `"agentrun: resolve workdir %q: %w"` prefix already applied by `ResolveWorkdir`. Callers asserting `errors.Is(err, fs.ErrNotExist)` continue to work.

## Testing strategy

Add to `internal/agentrun/trust_test.go` (do not create a new test file; the helper sits next to `ResolveWorkdir` and its tests should sit next to `ResolveWorkdir`'s):

- **`TestEncodeProjectDir_DarwinRealpath`** (skipped on non-darwin via `runtime.GOOS != "darwin"`): call with `t.TempDir()` (which lives under `/var/folders/...` on macOS). Assert the result has prefix `"-private-var-folders-"`. This pins the realpath step — a non-resolved encoding would produce `"-var-folders-..."` and the test would fail, catching any future refactor that bypasses `ResolveWorkdir`.
- **`TestEncodeProjectDir_LiteralSubstitution`**: build a non-symlinked absolute path (use `t.TempDir()` + `filepath.EvalSymlinks` to pre-resolve, OR construct a path from the resolved temp dir) and assert the result equals that path with `strings.NewReplacer("/", "-", ".", "-").Replace(...)` applied. This pins the substitution rule independent of platform-specific symlinks.
- **`TestEncodeProjectDir_DotInPathSegment`**: create `t.TempDir() + "/.hidden"` via `os.MkdirAll`, call `EncodeProjectDir` on it, assert the result contains `"--hidden"` (the `/.` sequence becomes `--`). This pins the `.` → `-` half of the rule — the ticket AC is silent on this and would let a developer implement only `/` → `-`, which would NOT match claude's on-disk layout (see Open Questions).
- **`TestEncodeProjectDir_MissingPath`**: call with `filepath.Join(t.TempDir(), "does-not-exist")`; assert `errors.Is(err, fs.ErrNotExist)`. Mirrors `TestResolveWorkdir_MissingPath` and proves the error pass-through contract.

All tests `t.Parallel()` where the existing siblings do (every test in this file except the platform-gated one).

## Open questions

**The AC's substitution rule is incomplete.** The ticket body says "replaces every `/` with `-`". Direct observation of `~/.claude/projects/` on this machine (2026-05-14) shows entries like `-Users-juhanailmoniemi-Workspace-Projects--pyrycode-worktrees-architect-1` — the `/.pyrycode-worktrees/` segment produces `--pyrycode-worktrees-`, meaning `.` is ALSO mapped to `-`. The architect CLAUDE.md cites the rule as `(/ AND . → -)` and `docs/lessons.md:52` explicitly warns against trusting ticket bodies over observation for `~/.claude/projects/` layout. This spec implements the observed rule (`/` AND `.` → `-`) and tests it; if PO disagrees, route back with the discrepancy noted. Shipping the AC-literal `/` → `-` rule would produce keys that never match claude's actual directory names — silently broken for any workdir containing a `.` (extremely common: `.git` checkouts, `node_modules`/`.venv` projects, hidden-dir parents like `.pyrycode-worktrees`).

No other open questions — single-function helper, no caller in this diff, no new public types.
