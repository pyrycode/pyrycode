# Spec #544 — Decouple `TestResolveWorkdir_DarwinRealpath` from `$TMPDIR`

**Ticket:** #544 · **Size:** XS · **Scope:** test-only, single file (`internal/agentrun/workdir_test.go`)

## Files to read first

- `internal/agentrun/workdir_test.go:11-24` — `TestResolveWorkdir_DarwinRealpath`, the test to rewrite. The offending line is `const want = "/private/var/"` (line 20) and the prefix check (lines 21-23).
- `internal/agentrun/workdir_test.go:26-40` — `TestResolveWorkdir_AlreadyResolved`, the sibling that already asserts the canonical-form property `got == filepath.EvalSymlinks(input)`. The fix adopts the same property; mirror its structure.
- `internal/agentrun/workdir.go:23-33` — `ResolveWorkdir`. Confirms the exact contract: `filepath.Abs(workdir)` → `filepath.EvalSymlinks(abs)`. Because `t.TempDir()` is already absolute, `filepath.EvalSymlinks(wd)` in the test reproduces the function's output exactly (no `Abs` mismatch to worry about).
- `docs/lessons.md:219-223` — "fsnotify reports as-watched, kernel probes report canonicalised" / "two sources of paths, one canonical and one not." Confirms the macOS `/var → /private/var` default symlink that `t.TempDir()` exercises, and that `/tmp → /private/tmp` resolves the same way. This is the behaviour-class the darwin test exists to guard.

## Context

`TestResolveWorkdir_DarwinRealpath` hardcodes the assertion `got` has prefix `/private/var/`. That literal holds only when `$TMPDIR` is unset/default on macOS, where `t.TempDir()` lands under `/var/folders/...` which canonicalises to `/private/var/folders/...`.

In the dispatch sandbox `TMPDIR=/tmp/claude-501`, so `t.TempDir()` returns a path under `/tmp/claude-501/...`, which macOS canonicalises to `/private/tmp/claude-501/...` — failing the `/private/var/` prefix check. The failure is **pre-existing and not a product bug**: `ResolveWorkdir` is correct; the test asserts an environment-specific path instead of the behaviour it means to pin (that on darwin, `ResolveWorkdir` returns the symlink-resolved form of its input).

The fix: assert the *property* (resolved form derived from the input at runtime), not a fixed literal.

## Design

No production code changes. Rewrite the body of `TestResolveWorkdir_DarwinRealpath` to:

1. Keep the existing darwin skip: `if runtime.GOOS != "darwin" { t.Skip("darwin-only: /var symlink") }`.
2. Take `wd := t.TempDir()` as the (unresolved) input — same as today.
3. Derive the expected value at runtime: `want, err := filepath.EvalSymlinks(wd)` (fatal on error).
4. Call `got, err := ResolveWorkdir(wd)` (fatal on error).
5. Assert the canonical-form property: `got == want` (fatal on mismatch). This satisfies AC1/AC2 — no literal prefix, expected derived from input.
6. Assert the resolution-fired delta: `got != wd`. This is what makes the darwin-only test distinct from `TestResolveWorkdir_AlreadyResolved` — it proves the `/private` symlink resolution actually changed the path, rather than the function being a no-op. On both target environments the delta holds: default `$TMPDIR` crosses `/var → /private/var`; the dispatch sandbox crosses `/tmp → /private/tmp`.

The developer chooses the exact assertion phrasing/order; the two properties above (`got == EvalSymlinks(wd)` and `got != wd`) are the contract.

**Why both assertions.** AC2 is met by step 5 alone, but step 5 by itself makes the test nearly redundant with `TestResolveWorkdir_AlreadyResolved`. Step 6 gives the darwin-only test its reason to exist: it is the only place that asserts resolution *fired*. Keep both.

`t.Parallel()` is intentionally **not** added — the current test omits it, and adding it is out of scope (minimal diff). Leave it off.

## Concurrency model

N/A — single synchronous test function, no goroutines.

## Error handling

- `EvalSymlinks(wd)` and `ResolveWorkdir(wd)` errors → `t.Fatalf` with the input path and error (mirror the existing fatal style in the file: `t.Fatalf("...(%q): %v", wd, err)`).
- Never format file *contents* into failure messages — only paths and errors (see the `MUST NOT log file contents` note in `workdir.go:6`). The current test already complies; preserve that.

## Testing strategy

This change *is* the test. Verify by running it under both conditions:

```bash
# Reproduces the dispatch-sandbox condition — must pass after the fix (fails today):
TMPDIR=/tmp/claude-501 go test -race -run TestResolveWorkdir ./internal/agentrun/

# Default $TMPDIR — must continue to pass:
go test -race -run TestResolveWorkdir ./internal/agentrun/

# Full package gate:
go test -race ./internal/agentrun/
go vet ./internal/agentrun/
```

`TMPDIR=/tmp/claude-501` need not pre-exist; `os.MkdirTemp` (under `t.TempDir()`) creates the leaf directories, but the sandbox path's parent must be writable — on a dev box prefer an existing writable root, e.g. `TMPDIR="$(mktemp -d /tmp/claude-XXXX)"`, to reproduce the `/tmp → /private/tmp` condition without depending on a literal `claude-501` directory existing.

On non-darwin (CI Linux runners), the test must still skip — confirm `runtime.GOOS != "darwin"` skip is preserved.

## Acceptance criteria mapping

- AC1 (no env-specific literal prefix) — step 5 removes `const want = "/private/var/"`.
- AC2 (derive expected from input at runtime) — `want = filepath.EvalSymlinks(wd)`.
- AC3 (passes with `TMPDIR` under `/tmp`) — `got != wd` and `got == EvalSymlinks(wd)` both hold for `/tmp → /private/tmp`.
- AC4 (passes on default macOS, skips on non-darwin) — `/var → /private/var` delta holds; skip preserved.
- AC5 (no production code under `internal/agentrun` changes) — only `workdir_test.go` is touched.

## Open questions

- **Pathological `TMPDIR` already canonical (`/private/...`).** If a future environment sets `TMPDIR` to an already-resolved `/private/...` path, `t.TempDir()` would return a canonical path and the `got != wd` delta would not hold (the symlink resolution is a genuine no-op there, so there is nothing for the darwin test to demonstrate). This is **not an observed environment** — neither default macOS nor the dispatch sandbox produces it — so per the pipeline's evidence-based-fix principle, do **not** add a guard/skip for it. Flagged here only so a future failure under such a `TMPDIR` is immediately diagnosable rather than mysterious.
