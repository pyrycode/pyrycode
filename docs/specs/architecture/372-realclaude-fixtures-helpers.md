# Spec: e2e/realclaude — WithWorktree + ReadJSONL fixture helpers

Ticket: [#372](https://github.com/pyrycode/pyrycode/issues/372)
Size: S (one tagged source file ~55 LoC + one tagged test file)

## Files to read first

- `internal/e2e/realclaude/smoke_test.go:1-23` — scaffold that landed in #361. Mirror its `//go:build e2e_realclaude` header, package declaration (`package realclaude`, not `_test`), and overall file layout.
- `internal/agentrun/trust.go:23-52` — `projectDirReplacer`, `ResolveWorkdir`, `EncodeProjectDir`. `EncodeProjectDir` is the function `ReadJSONL` calls; both `/` AND `.` map to `-` (the encoding rule documented in `docs/lessons.md` § "Claude session storage on disk").
- `internal/agentrun/jsonl/reader.go:45-103` — `Event`, `UsageBlock`, `Config`, `Reader`, `NewReader`. `JSONLEntry` is a type alias for `Event`; `ReadJSONL` constructs a `Reader` with a zero-value `Config` and loops `Next()` until `io.EOF`.
- `internal/agentrun/jsonl/reader.go:188-260` — `Next` contract: returns `Event, error`; surfaces `io.EOF` as the end sentinel; non-EOF errors are wrapped as `jsonl: read at offset %d: %w`. Empty file → first `Next()` returns `io.EOF`. Malformed lines are skipped internally (logged at Warn), so the helper never has to filter them.
- `internal/agentrun/jsonl/reader_test.go` (first 80 lines) — table fixture style: assistant entries are written as raw JSON objects with `type`, `message.stop_reason`, `message.content[].text`. Reuse this exact shape for the `fixtures_test.go` happy-path lines.
- `docs/lessons.md` § "Claude session storage on disk" — encoded-cwd rule (`/` AND `.` → `-`). Confirms the helper must defer to `agentrun.EncodeProjectDir` rather than re-implement.
- `Makefile` — `e2e-realclaude` target landed in #361. No change here; build-tag gating handles `make test` exclusion.

## Context

The `internal/e2e/realclaude/` suite (scaffold from #361) needs two file-system primitives shared by every downstream test (#364–#368): a HOME-isolated workdir and a typed reader for the session JSONL the run produces. Both are pure file-system helpers — no `exec`, no subprocess wiring. The subprocess invocation helper (`RunPyryAgentRun`) is the sibling #373; it owns the build-cache + exec + trailer-parse machinery and is independent of this work — downstream tests will compose all three but no compile-time dep links the two specs.

Two design forces drive every choice:

1. **HOME pinning is load-bearing.** Both the in-test process and any subprocess it spawns must resolve `os.UserHomeDir()` to the same root, or `ReadJSONL` looks in the wrong directory. `t.Setenv("HOME", …)` (NOT `os.Setenv`) is mandatory: the framework restores the prior value on cleanup, which is what keeps parallel tests from leaking HOME mutations into each other.
2. **No re-implementation of `EncodeProjectDir` or the JSONL parser.** Both already exist, are tested, and own subtle invariants (symlink resolution; the `/` AND `.` → `-` rule; malformed-line skipping). The helper is a thin shim, not a parallel implementation.

## Design

### File layout

```
internal/e2e/realclaude/
  smoke_test.go        (existing, #361)
  fixtures.go          (new — production helpers, build-tagged)
  fixtures_test.go     (new — tests for the helpers, build-tagged)
```

Package `realclaude` (matches the scaffold — not `realclaude_test`). Both new files begin with:

```go
//go:build e2e_realclaude
```

`make test` continues to skip the suite via tag exclusion alone. No Makefile change.

### `fixtures.go` (the only new production file)

Three exported symbols. Total ~55 LoC including imports and the build tag.

**`type JSONLEntry = jsonl.Event`** — type alias (note the `=`), not a wrapper struct. Preserves field access (`entry.Usage.OutputTokens`, `entry.EndOfTurn`, etc.) without re-exporting `jsonl.UsageBlock`. The alias keeps downstream tests from importing `internal/agentrun/jsonl` directly, but stays zero-cost.

**`func WithWorktree(t *testing.T) (workdir string)`**

Contract:

- Calls `t.Helper()`.
- `workdir := t.TempDir()` — gives an isolated per-test directory cleaned up automatically.
- `t.Setenv("HOME", workdir)` — pins HOME for the whole test (including subtests and any subprocesses spawned later) and restores it on cleanup.
- Returns `workdir`.

No `t.Cleanup` calls — both `t.TempDir` and `t.Setenv` register their own. No `MkdirAll(.claude/…)` — claude/pyry create those on first write; pre-creating them would mask a regression where the runtime stops creating the directory.

**`func ReadJSONL(t *testing.T, workdir, sessionID string) []JSONLEntry`**

Contract — sequenced steps the implementation MUST perform in order:

1. `t.Helper()`.
2. `home, err := os.UserHomeDir()` — non-nil err → `t.Fatalf("realclaude.ReadJSONL: resolve HOME: %v", err)`.
3. `enc, err := agentrun.EncodeProjectDir(workdir)` — non-nil err → `t.Fatalf("realclaude.ReadJSONL: encode workdir %q: %v", workdir, err)`.
4. Compute `path := filepath.Join(home, ".claude", "projects", enc, sessionID+".jsonl")`.
5. `f, err := os.Open(path)` — non-nil err (includes missing-file) → `t.Fatalf("realclaude.ReadJSONL: open %s: %v", path, err)`. The resolved path MUST appear in the message so a test failure points the developer at the directory to inspect.
6. `defer f.Close()`.
7. `r := jsonl.NewReader(f, jsonl.Config{})` — zero-value config (default logger; `StartOffset` 0).
8. Loop: `ev, err := r.Next()`; on `errors.Is(err, io.EOF)` break; on any other non-nil err → `t.Fatalf("realclaude.ReadJSONL: parse %s: %v", path, err)`; otherwise `events = append(events, ev)`.
9. Return `events`.

Empty file: step 5 succeeds, step 8's first `Next()` returns `io.EOF`, returned slice is nil-or-empty. Both are valid empty slices per `len(empty) == 0`; no special-casing required.

The helper writes no logs and uses no goroutines. It is fully synchronous and self-contained inside a single test's call stack.

### Imports `fixtures.go` will need

```go
import (
    "errors"
    "io"
    "os"
    "path/filepath"
    "testing"

    "github.com/pyrycode/pyrycode/internal/agentrun"
    "github.com/pyrycode/pyrycode/internal/agentrun/jsonl"
)
```

(Module path per `go.mod`; mirror existing imports in `internal/agentrun/jsonl/reader_test.go`.)

### `fixtures_test.go`

Same build tag, same package. Five test scenarios. Use stdlib `testing` only (no testify). Write each as a small focused `func Test…(t *testing.T)`; no table is necessary at this scale.

- **`TestWithWorktree_ReturnsExistingHomeIsolatedDir`** — call `WithWorktree(t)`; assert the returned dir exists (`os.Stat`); assert `os.UserHomeDir()` returns the same path; record `t.Setenv` order with a sibling subtest to confirm HOME restores correctly when the subtest exits (verify by reading `os.Getenv("HOME")` before and after a `t.Run` block).
- **`TestReadJSONL_HappyPath`** — call `WithWorktree(t)`; compute `enc, _ := agentrun.EncodeProjectDir(workdir)`; mkdir `<home>/.claude/projects/<enc>/`; write two fixture lines to `<sessionID>.jsonl` (`sessionID := "00000000-0000-0000-0000-000000000001"` is fine — the helper doesn't validate UUID shape). Line 1: assistant entry with `message.stop_reason: "end_turn"` and one `content` block of `type: "text", text: "ok"`. Line 2: user entry with `type: "user"`. Call `ReadJSONL(t, workdir, sessionID)`; assert `len(events) == 2`; assert `events[0].EndOfTurn == true` and `events[0].Kind == "assistant"`; assert `events[1].Kind == "user"` and `events[1].EndOfTurn == false`.
- **`TestReadJSONL_EmptyFile`** — set up workdir, mkdir the projects subdir, `os.Create` the JSONL path with zero bytes, close. Call `ReadJSONL`; assert `len(events) == 0` and no `t.Fatalf` was triggered.
- **`TestReadJSONL_MissingFile`** — set up workdir but do NOT create the JSONL file. Use the `testing.T`-wrapper trick from `internal/e2e/harness.go` (or whichever existing test in the repo wraps `*testing.T` to capture `Fatalf` calls — if none exists, run the call in a subtest that intentionally fails and use `t.Run` + result inspection). The assertion: `Fatalf` fires, AND the captured message contains the fully resolved JSONL path (verify by computing `filepath.Join(home, ".claude", "projects", enc, sessionID+".jsonl")` and `strings.Contains`-ing the captured message). If the wrapper trick proves too fragile to implement quickly, the developer MAY refactor `ReadJSONL` internally to split into a private `resolveAndOpen(workdir, sessionID) (*os.File, string, error)` plus the public wrapper that calls `t.Fatalf` — then unit-test the private split directly on the returned error. Either approach satisfies the AC; document the choice in the test file with a one-line comment.
- **`TestJSONLEntry_AliasCompiles`** — compile-only assertion: `var _ jsonl.Event = JSONLEntry{}`. No `Run`-body; the file simply has to build.

The happy-path fixture lines should mirror the JSON shape used in `internal/agentrun/jsonl/reader_test.go` (look at the existing test fixtures there for an exact template). The two lines together exercise both the assistant-path and the non-assistant short-circuit inside `Next`, giving end-to-end coverage with minimal bytes.

## Concurrency model

N/A. Both helpers are fully synchronous, run inside a single test goroutine, hold no shared state. `t.Setenv` and `t.TempDir` are documented thread-safe with respect to the framework's cleanup ordering. If a downstream test calls `t.Parallel()`, each parallel instance gets its own `t.TempDir` and its own pinned `HOME`, so the helpers compose correctly without additional locking.

## Error handling

The helpers are test-only and follow the project's testing idiom: failure modes call `t.Fatalf` with enough context that a `make e2e-realclaude` log line points the developer at the offending path. Specifically:

- `os.UserHomeDir()` failure → fatal with the wrapped error. Vanishingly rare on the supported platforms (Linux/macOS); not worth a retry or skip.
- `EncodeProjectDir` failure → fatal with the workdir in the message. The only failure mode is the workdir not existing on disk, which means the caller passed something `WithWorktree` did not produce — programmer error.
- `os.Open` failure (file missing OR permission denied) → fatal with the resolved path. The path string is the diagnostic — without it the developer has nowhere to start.
- `Reader.Next` non-EOF error → fatal with the resolved path and wrapped error. Malformed-line skipping is internal to `Reader`, so any error surfaced here is a structural I/O failure (file truncated mid-read, etc.) and the test cannot continue.

Rationale for fatal-not-error-return: consistent with the existing `internal/e2e/harness.go` style. Tests that call `WithWorktree` / `ReadJSONL` should read top-to-bottom as a scenario, not as a sequence of `if err != nil` checks.

## Testing strategy

The five tests in `fixtures_test.go` ARE the verification of the helpers. Done-when:

1. `make test` produces zero output from `internal/e2e/realclaude/` (build-tag exclusion already in place — re-verify with `make test 2>&1 | grep realclaude` → empty).
2. `make e2e-realclaude` runs the five tests and they all pass on the developer's machine. (The suite gates on real `claude` on PATH via `smoke_test.go`; the new tests do NOT depend on `claude` and will pass even on a machine where `claude` is missing — only the smoke test will fail. The developer should still confirm the new tests pass independently.)
3. `make check` (vet + test + staticcheck) passes — confirms the build-tagged files don't break the default tag set.

No CI change needed. `make e2e-realclaude` continues to be opt-in / developer-local.

## Open questions

- **Missing-file test technique.** The two viable approaches (testing.T wrapper vs. private `resolveAndOpen` split) are both acceptable. The developer chooses based on what minimises footprint inside the ~60 LoC budget for `fixtures.go`. If the private-split route is taken, `resolveAndOpen` stays unexported and `ReadJSONL` is the only test-friendly entry point — i.e. the public surface stays at the three symbols listed above.

## Notes for the developer

- Helper source (excluding tests) must stay ≤ ~60 LoC per the ticket AC. The design above lands at ~55 with comments; a docstring under 3 lines per exported symbol is the right density. Multi-paragraph docstrings are forbidden by `CODING-STYLE.md` and will push you over budget.
- Stale branch `origin/feature/363` exists from a developer auto-commit before #363 was split into #372 + #373. The ticket is CLOSED and the branch will not be merged; ignore it. Your branch (`feature/372`) is the canonical one.
- Do not write a `.claude/` directory tree in `WithWorktree`. The runtime creates what it needs. Pre-creating masks regressions.
