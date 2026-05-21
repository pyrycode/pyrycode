# Spec — #501: `agentrun.EncodeProjectDir` delegates to `tuidriver.EncodeCwd`

Confirms PO's size: **XS**. One production file, one test file, signature unchanged.

## Files to read first

- `internal/agentrun/workdir.go` (full file, 44 LOC) — current encoder. Lines 15 (`projectDirReplacer`), 33–43 (`EncodeProjectDir` body + doc comment) are the surface to change. `ResolveWorkdir` is reused unchanged.
- `internal/agentrun/workdir_test.go` (full file, 124 LOC) — all four `TestEncodeProjectDir_*` cases live here. Pay attention to:
  - `TestEncodeProjectDir_LiteralSubstitution` (lines 82–97) — its oracle is the **old** narrow replacer; this is the test whose oracle must flip to `tuidriver.EncodeCwd`.
  - `TestEncodeProjectDir_DarwinRealpath` (lines 67–80) — passes either encoder (alnum + `/` only).
  - `TestEncodeProjectDir_DotInPathSegment` (lines 99–112) — passes either encoder (`/.hidden` → `--hidden` under both rules).
  - `TestEncodeProjectDir_MissingPath` (lines 114–124) — error path; unchanged.
- `github.com/pyrycode/tui-driver/pkg/tuidriver/cwd.go` (in module cache, 25 LOC) — the encoder we're delegating to. Confirms exported signature `func EncodeCwd(cwd string) string` and the per-byte rule (every byte outside `[a-zA-Z0-9]` → `-`, no run collapse). Idempotent on already-encoded strings because `-` itself is non-alnum and maps to `-`.
- `github.com/pyrycode/tui-driver/pkg/tuidriver/cwd_test.go` (in module cache) — upstream's own table tests, including the loop-2 B-4 reference case and `unicode bytes mapped per-byte to hyphen`. We do **not** duplicate these cases; we add only the differential cases (where old vs. new encoder diverge).
- `internal/agentrun/ptyrunner/runner.go` and `internal/agentrun/ptyrunner/watchdog.go` (imports only, no edits) — confirm `tui-driver/pkg/tuidriver` is already imported by sibling files in `internal/agentrun/`, so the new dependency on it from `workdir.go` adds no module-graph surface.
- Ticket #501 body — empirical evidence that the wedge in real-claude tests is the 30s PTY-quiet watchdog firing because ptyrunner tails the wrong file. Not strictly needed to write the code, but it pins **why** this fix is the root cause and frames the third AC.

No QMD / docs reading required — the encoder rule itself is fully captured in `tui-driver/pkg/tuidriver/cwd.go`'s doc comment.

## Context

`internal/agentrun/workdir.go:15` defines `projectDirReplacer = strings.NewReplacer("/", "-", ".", "-")`, which only maps `/` and `.` to `-`. Claude's real encoder maps **every non-alphanumeric byte** to `-` (per-byte, no run collapse). The two encoders diverge on `_`, ` `, and other special bytes — so for any workdir containing those (e.g. `t.TempDir()` on a test named with underscores), ptyrunner's JSONL tail watches the pyry-encoded path while claude writes JSONL at the per-byte-encoded path. Result: PTY-quiet watchdog at 30s, 55s wall before SIGTERM, `error_during_execution num_turns:0` on every real-claude test with `_` in the test name.

The canonical encoder already exists in `tui-driver/pkg/tuidriver.EncodeCwd` (added 2026-05-18, verified empirically against claude's on-disk behaviour). `internal/agentrun/ptyrunner` already imports `github.com/pyrycode/tui-driver/pkg/tuidriver`. Single-source-of-truth fix: delegate.

## Design

**Option A (chosen)** from the ticket: re-export the tui-driver encoder by calling it from `EncodeProjectDir`.

### `internal/agentrun/workdir.go`

Replace the body of `EncodeProjectDir` to delegate. Delete `projectDirReplacer`. Drop the `strings` import (no longer used in this file). Add `github.com/pyrycode/tui-driver/pkg/tuidriver` to the import block.

Signature is unchanged:

```go
func EncodeProjectDir(workdir string) (string, error)
```

Behaviour:

1. `resolved, err := ResolveWorkdir(workdir)` — unchanged. Wraps `fs.ErrNotExist` for missing paths.
2. Return `tuidriver.EncodeCwd(resolved), nil` — every non-`[a-zA-Z0-9]` byte → `-`, no run collapse, idempotent on already-encoded strings.

Update the doc comment on `EncodeProjectDir` (currently says "maps '/' and '.' to '-'") to describe the new rule. One sentence: *"Maps every byte outside `[a-zA-Z0-9]` to '-' (matching how claude derives the on-disk projects-dir name; see tuidriver.EncodeCwd)."*

`ResolveWorkdir` is untouched.

**Why not Option B (inline the byte-loop):** the package already pulls in `tuidriver` via its ptyrunner sibling, so the coupling argument has no weight. Two encoders means future drift; one encoder is the win.

### Call sites — no changes

`EncodeProjectDir`'s exported signature `(string, error)` is unchanged. All consumers (`internal/agentrun/jsonl/tail/watcher.go:115`, `internal/e2e/realclaude/fixtures.go:366`, `prompt_fidelity_test.go:84`, `fixtures_test.go:296`/`:559`, `watcher_test.go:94`/`:260`, `ptyrunner/runner_test.go:35`) compose the returned encoded string into a path and treat it as opaque. They were *already* calling `EncodeProjectDir` expecting it to match claude's on-disk path — they were just getting the wrong answer. The fix makes them right; no consumer edits needed.

Grep search before commit will be the developer's belt-and-suspenders check: confirm no test in `internal/` hardcodes an expected encoded string that contained a `_` or space under the old rule.

## Concurrency model

N/A. `EncodeProjectDir` is a pure helper plus one syscall (`filepath.EvalSymlinks` inside `ResolveWorkdir`).

## Error handling

Unchanged. Only error source is `ResolveWorkdir`, which already wraps `fs.ErrNotExist` correctly. `tuidriver.EncodeCwd` cannot fail (returns `string`, not `(string, error)`).

## Testing strategy

All test changes live in `internal/agentrun/workdir_test.go`.

**Update existing test:** `TestEncodeProjectDir_LiteralSubstitution` (lines 82–97). The current oracle is `strings.NewReplacer("/", "-", ".", "-").Replace(resolved)` — that *is* the old behaviour. Replace the oracle with `tuidriver.EncodeCwd(resolved)`. This case then becomes the parity test the AC requires (`EncodeProjectDir(p) == tuidriver.EncodeCwd(realpath(p))`). It also tightens automatically: on macOS `t.TempDir()` emits a path containing the test-name string with underscores, so this case differentially exercises the `_` → `-` mapping every run.

**Add a new table-driven test** named `TestEncodeProjectDir_NonAlnumBytes`. Use `t.TempDir()` as the base directory, then `os.MkdirAll` a subdirectory with the special-char input for each case, encode, and assert the suffix matches the expected per-byte encoding. Scenarios (bullets — write the test in the project's table-driven idiom, stdlib `testing` only):

- Underscore segment — sub-dir literal `"Test_With_Underscores"` → suffix `"-Test-With-Underscores"`. The differential case vs. the old encoder.
- Space segment — sub-dir literal `"with space"` → suffix `"-with-space"`. (Skip the case if the test runner's filesystem refuses spaces; macOS and Linux tmpfs accept them.)
- Pre-existing `-` idempotent — sub-dir literal `"already-dashed"` → suffix `"-already-dashed"` (each `-` is non-alnum, maps to `-`; result is identical to input segment).
- Mixed specials — sub-dir literal `"a_b-c.d e"` → suffix `"-a-b-c-d-e"`. Adjacent specials produce adjacent hyphens (no run collapse) is already covered by tui-driver's own table; not duplicated here, but the mixed case demonstrates the property holds end-to-end through `ResolveWorkdir`.

Each case constructs the workdir with `filepath.Join(t.TempDir(), <literal>)` + `os.MkdirAll(_, 0o755)`, then asserts `strings.HasSuffix(got, want)` because the `t.TempDir()` prefix is variable. Use `strings.HasSuffix` (not full-string equality) to keep the test independent of the temp-dir layout.

**Unchanged tests:**

- `TestEncodeProjectDir_DarwinRealpath` — checks prefix `-private-var-folders-`. All chars in the prefix are alnum or `/`; both encoders agree.
- `TestEncodeProjectDir_DotInPathSegment` — sub-dir `.hidden`, asserts suffix `--hidden`. `.` is non-alnum under both encoders; passes either way.
- `TestEncodeProjectDir_MissingPath` — pure error-path test; unaffected.
- All `TestResolveWorkdir_*` cases — `ResolveWorkdir` is not modified.

**Out-of-process verification (third AC, no new test):** `make e2e-realclaude` is the regression gate. Developer is not required to add a new e2e assertion — the existing real-claude tests in `internal/e2e/realclaude/` (which call `agentrun.EncodeProjectDir` via fixtures.go) already encode their `t.TempDir()` workdirs through `_`-bearing test names, so the moment the encoder is right the on-disk JSONL tail finds the file claude is writing. Per AC #3, remaining failures of *other* root causes are out of scope; the goal is to remove the `54-55s error_during_execution num_turns:0` failure mode.

`go test -race ./...` and `go vet ./...` are the standard gates (AC #4). No staticcheck-relevant changes.

## Open questions

None.

## Acceptance criteria mapping

| AC | Where addressed |
|---|---|
| `EncodeProjectDir(p) == tuidriver.EncodeCwd(realpath(p))` pinned by unit test | `TestEncodeProjectDir_LiteralSubstitution` (retargeted) + new `TestEncodeProjectDir_NonAlnumBytes` table |
| `workdir.go` no longer maintains a separate encoder | `projectDirReplacer` deleted, `strings` import removed, body delegates to `tuidriver.EncodeCwd` |
| `make e2e-realclaude` 55s wedge resolved (or fails for a different reason) | Out-of-band verification; not a new test deliverable. The fix is the change. |
| `go test -race ./...` and `go vet ./...` clean | Standard pre-commit gate, no new constraints introduced |
