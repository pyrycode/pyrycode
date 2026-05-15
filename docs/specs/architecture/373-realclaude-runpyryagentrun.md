# Spec: e2e/realclaude — `RunPyryAgentRun` fixture helper

Ticket: [#373](https://github.com/pyrycode/pyrycode/issues/373)
Size: S (one production-file extension ~95 LoC + one test-file extension)

## Files to read first

- `internal/e2e/realclaude/fixtures.go` (entire file, ~70 lines) — the file this ticket extends. Same build tag (`//go:build e2e_realclaude`), same package (`realclaude`). Note the existing imports (`errors`, `fmt`, `io`, `os`, `path/filepath`, `testing`, plus `agentrun` and `agentrun/jsonl`); merge new imports rather than re-declaring an import block.
- `internal/e2e/realclaude/fixtures_test.go` (entire file, ~135 lines) — file the new tests extend. Reuse the `testSessionID` const and the `writeFixtureLines` test helper style; do not duplicate either.
- `cmd/pyry/agent_run.go:24-152` — `agentRunArgs` field set and `parseAgentRunArgs` invariants. `RunOpts` mirrors this flag surface 1:1. Key invariants the helper must respect: `--effort` ∈ {`low`,`medium`,`high`,`xhigh`,`max`}; `--max-turns` > 0; `--output-format` is always `stream-json`; `--workdir` must exist (the helper relies on the caller's `WithWorktree` for that); `--prompt-file` and `--system-prompt-file` must be regular files (the helper writes them before exec).
- `cmd/pyry/agent_run.go:189-233` — `runAgentRun` stdout contract. Claude's stdout (the canonical stream-json event stream including `system init` and `result` events) is forwarded byte-for-byte to pyry's stdout. The helper's `parseInitSessionID` reads this stream verbatim.
- `cmd/pyry/agent_run.go:208-213` — `PYRY_CLAUDE_BIN` env knob. Tests inject a fake claude through `RunOpts.ExtraEnv`; the helper does not own that wiring.
- `cmd/pyry/agent_run.go:255-267` — `buildClaudeArgs`. The argv `pyry` lowers to claude. Helper does NOT assert on the lowered argv (that surface belongs to `pyry agent-run`); helper only owns its own pyry argv.
- `internal/e2e/harness.go:106-141` — `binOnce` / `binPath` / `binErr` + `ensurePyryBuilt(t)`. The exact pattern to clone. Disjoint build tags (`e2e` vs. `e2e_realclaude`) prevent direct import; duplicate intentionally per `docs/PROJECT-MEMORY.md` "Resist over-DRY on duplicated registry primitives" precedent.
- `internal/agentrun/streamjson/testdata/captured_run.jsonl` (line 1) — canonical shape of the `{"type":"system","subtype":"init", ..., "session_id":"…"}` envelope. The parser only needs `Type`, `Subtype`, `SessionID`; nothing else.
- `CODING-STYLE.md` § "Testing" — `TestHelperProcess` re-exec pattern. The fake-pyry tests gate on `GO_TEST_HELPER_PROCESS=1` and select a behaviour via a second env var (e.g. `PYRY_E2E_FAKE_MODE`).

## Context

The `internal/e2e/realclaude/` suite (scaffold from #361, file-system helpers from #372) is missing one primitive: a synchronous subprocess invoker that builds `pyry` once per test process, writes the prompt + system-prompt files into the test workdir, invokes `pyry agent-run` with the eight required flags, captures all three streams plus exit code, and returns the resolved session_id parsed out of claude's stream-json output.

Under the post-#391 architecture, `pyry agent-run` forwards claude's stream-json output byte-for-byte. The session_id appears in **two** places in that stream:

- The first line: `{"type":"system","subtype":"init", ..., "session_id":"…"}`
- The trailer: `{"type":"result", ..., "session_id":"…"}`

The init line lands earliest in stdout — picking it lets failing tests still see the session_id even when the run aborted before producing a trailer. A `bufio.Scanner` over the captured stdout, stopping at the first parseable `system`/`init` line with a non-empty `session_id`, is the entire parser.

Design forces:

1. **Mirror the `pyry agent-run` flag contract verbatim.** `RunOpts` fields map 1:1 to the flags from `cmd/pyry/agent_run.go:66-152`. A rename in either place is a spec violation by definition. The argv-contract test below pins this in code.
2. **Duplicate `ensurePyryBuilt`, don't extract.** `internal/e2e` is `//go:build e2e`; `internal/e2e/realclaude` is `//go:build e2e_realclaude`. Disjoint build tags mean no shared compilation unit is reachable. A "common" package would either need a third tag (multi-tag headers are a known footgun) or be untagged (would force the agentrun + harness code into every regular `go test` run). Duplicate ~20 lines and move on. Precedent: `docs/PROJECT-MEMORY.md` § "Resist over-DRY on duplicated registry primitives".
3. **Validation as a returned error, fatal at the public boundary.** Pull a private `validateRunOpts(opts) error` so the validation path is unit-testable as a returned error; the public helper calls it and converts to `t.Fatalf`. This is the same shape `#372` used for `resolveAndOpenJSONL`.
4. **Non-zero exit is NOT a `t.Fatalf`.** The helper is testing the real CLI; the run's failure modes (max-turns exceeded, claude crash, permission errors) are the things downstream tests assert against. Only structural failures (validation, build, exec start, timeout) are fatal.

## Design

### File extension layout

Appended to the existing `internal/e2e/realclaude/fixtures.go`:

```
//go:build e2e_realclaude
package realclaude

// (existing) JSONLEntry, WithWorktree, ReadJSONL, resolveAndOpenJSONL — from #372

// new symbols (this ticket)
type RunOpts struct { ... }
type RunResult struct { ... }
func RunPyryAgentRun(t *testing.T, opts RunOpts) RunResult { ... }

// private helpers (this ticket)
func validateRunOpts(opts RunOpts) error { ... }
func ensurePyryBuilt(t *testing.T) string { ... }   // package-private; mirrors internal/e2e/harness.go
func parseInitSessionID(stdout []byte) string { ... }
var (
    pyryBinOnce sync.Once
    pyryBinPath string
    pyryBinErr  error
)
```

New imports merged into the existing block: `bufio`, `bytes`, `context`, `encoding/json`, `os/exec`, `path/filepath` (already imported), `sync`, `time`.

### Exported types

**`RunOpts`** — input to the helper. Field order and types are part of the contract.

| Field          | Type            | Notes                                                                                                     |
|----------------|-----------------|-----------------------------------------------------------------------------------------------------------|
| `Workdir`      | `string`        | Required. Must exist (caller's `WithWorktree` guarantees this).                                           |
| `Prompt`       | `string`        | Required. Helper writes to `<Workdir>/prompt.txt` before exec.                                            |
| `SystemPrompt` | `string`        | Required. Helper writes to `<Workdir>/system.txt` before exec.                                            |
| `AllowedTools` | `[]string`      | Required, non-empty. Joined with `,` for `--allowed-tools`.                                               |
| `MaxTurns`     | `int`           | Required, > 0.                                                                                            |
| `Effort`       | `string`        | Required. Helper does NOT re-validate the enum; pyry rejects bad values and the helper surfaces that exit. |
| `Model`        | `string`        | Required.                                                                                                 |
| `ExtraEnv`     | `[]string`      | Optional. Appended after `os.Environ()`. Used for `PYRY_CLAUDE_BIN=<fake>` etc.                            |
| `Timeout`      | `time.Duration` | Optional. Zero ⇒ 5 minutes.                                                                               |

No `SessionID` field — `pyry agent-run` has no resume flag; session_id is an output, not an input. No `Mode` enum — `pyry agent-run` is the only invocation shape.

**`RunResult`** — output from the helper. Returned only on structural success (the subprocess started, ran, and exited within the timeout). Non-zero `ExitCode` is normal.

| Field       | Type     | Notes                                                                                  |
|-------------|----------|----------------------------------------------------------------------------------------|
| `ExitCode`  | `int`    | `cmd.ProcessState.ExitCode()`. -1 only if exec returned no `ExitError` (treat as bug). |
| `SessionID` | `string` | First `system`/`init` line's `session_id` (or `""` if none was emitted).               |
| `Stdout`    | `[]byte` | Full captured stdout. Includes the init line and any subsequent events.                |
| `Stderr`    | `[]byte` | Full captured stderr. Tests assert on this for error paths.                            |

### `RunPyryAgentRun` flow

1. `t.Helper()`.
2. `validateRunOpts(opts)` → on error, `t.Fatalf("realclaude.RunPyryAgentRun: %v", err)`.
3. `bin := ensurePyryBuilt(t)` (fatal-on-error inside).
4. Write `<Workdir>/prompt.txt` (0o600) with `opts.Prompt`; write `<Workdir>/system.txt` (0o600) with `opts.SystemPrompt`. Fatal on any write error with the path embedded.
5. `timeout := opts.Timeout; if timeout == 0 { timeout = 5 * time.Minute }`.
6. `ctx, cancel := context.WithTimeout(context.Background(), timeout); defer cancel()`.
7. Build argv (see § "Subprocess argv" below).
8. `cmd := exec.CommandContext(ctx, bin, args...)`; `cmd.Env = append(os.Environ(), opts.ExtraEnv...)`; `cmd.Dir` left unset (the `--workdir` flag tells pyry where to chdir for claude; the helper's own cwd is irrelevant).
9. `var stdout, stderr bytes.Buffer; cmd.Stdout = &stdout; cmd.Stderr = &stderr`.
10. `runErr := cmd.Run()`.
11. Branch on result:
    - If `errors.Is(ctx.Err(), context.DeadlineExceeded)`: `t.Fatalf("realclaude.RunPyryAgentRun: timed out after %s\nstdout:\n%s\nstderr:\n%s", timeout, stdout.Bytes(), stderr.Bytes())`.
    - If `runErr != nil` and it is not an `*exec.ExitError`: `t.Fatalf` with the error (this is exec start failure — fork/exec returned ENOENT, EACCES, …).
    - Otherwise (success OR `*exec.ExitError`): build `RunResult` from `cmd.ProcessState.ExitCode()`, `parseInitSessionID(stdout.Bytes())`, `stdout.Bytes()`, `stderr.Bytes()`; return.

### Subprocess argv

```
pyry agent-run
  --prompt-file=<Workdir>/prompt.txt
  --system-prompt-file=<Workdir>/system.txt
  --allowed-tools=<strings.Join(AllowedTools, ",")>
  --max-turns=<MaxTurns>
  --effort=<Effort>
  --model=<Model>
  --workdir=<Workdir>
  --output-format=stream-json
```

Use the `--flag=value` form (not `--flag value`) so the wire format is unambiguous in the argv-contract test. Eight flags, total. Anything else is a regression.

### `validateRunOpts(opts RunOpts) error`

Returns the first violation encountered. Order is: `Workdir`, `Prompt`, `SystemPrompt`, `AllowedTools`, `MaxTurns`, `Effort`, `Model`. Each error is `realclaude.RunOpts: <field>: <reason>`.

Required-field rules:

- `Workdir == ""` → `Workdir: required`.
- `Prompt == ""` → `Prompt: required`.
- `SystemPrompt == ""` → `SystemPrompt: required`.
- `len(AllowedTools) == 0` → `AllowedTools: required, non-empty`.
- `MaxTurns <= 0` → `MaxTurns: must be > 0 (got N)`.
- `Effort == ""` → `Effort: required`.
- `Model == ""` → `Model: required`.

Effort enum is NOT re-validated here — pyry rejects bad values and the helper surfaces that as a non-zero exit. (Spec choice: avoid duplicating the enum, since the helper's only role is to put what the caller said onto argv.)

### `ensurePyryBuilt(t *testing.T) string`

Verbatim clone of `internal/e2e/harness.go:118-141`, renamed to use package-private `pyryBinOnce` / `pyryBinPath` / `pyryBinErr`. Same env knob: `PYRY_E2E_BIN`. Same temp dir pattern: `os.MkdirTemp("", "pyry-realclaude-*")`. Same `go build -o <out> github.com/pyrycode/pyrycode/cmd/pyry`.

### `parseInitSessionID(stdout []byte) string`

Contract: scan `stdout` line-by-line. For each line, attempt `json.Unmarshal` into:

```go
struct {
    Type      string `json:"type"`
    Subtype   string `json:"subtype"`
    SessionID string `json:"session_id"`
}
```

Return `SessionID` from the first decoded record where `Type == "system" && Subtype == "init" && SessionID != ""`. Non-JSON lines and lines without those fields are skipped silently (the stream may legitimately contain other event types if `pyry agent-run` prepends framing — currently it doesn't, but the parser is conservative). Returns `""` if no init line is found.

Use `bufio.NewScanner(bytes.NewReader(stdout))`. Default scanner buffer (64 KiB) is fine for the init envelope; pyrycode/claude does not emit pre-init garbage that approaches that size.

## Concurrency model

- `RunPyryAgentRun` runs synchronously on the caller's goroutine. No goroutines spawned. `exec.CommandContext` handles SIGKILL on timeout for us; we do not need to read stdout/stderr in goroutines because `cmd.Run()` blocks on `cmd.Wait()` which finishes reading the pipes (the `bytes.Buffer` sinks have no backpressure to deadlock on).
- The build cache is concurrency-safe via `sync.Once`. Parallel `t.Parallel()` tests calling `RunPyryAgentRun` simultaneously each call `ensurePyryBuilt`; the first wins the build, the rest read its result.
- No shared state between calls beyond the build cache. Each call writes its own `prompt.txt` / `system.txt` into the caller's workdir, which is per-test from `t.TempDir()`.

## Error handling

| Failure                       | Behaviour                                                                                  |
|-------------------------------|--------------------------------------------------------------------------------------------|
| Validation (bad `RunOpts`)    | `t.Fatalf` from the public helper. `validateRunOpts` returns the error for unit-test access. |
| `go build pyry` failure        | `t.Fatalf` from `ensurePyryBuilt`. Same shape as `internal/e2e/harness.go`.                |
| Prompt/system file write fail | `t.Fatalf` with the resolved path embedded.                                                |
| Exec start failure (`ENOENT`) | `t.Fatalf` — `cmd.Run` returns a non-`*exec.ExitError`.                                    |
| Context deadline              | `t.Fatalf` with `stdout` and `stderr` embedded so the operator can see what was running.  |
| Subprocess non-zero exit      | NOT fatal. Populate `RunResult`, return. Caller asserts on `ExitCode`/`Stderr`.            |
| Init line absent              | `SessionID == ""` in the returned `RunResult`. Not fatal.                                  |

## Testing strategy

Tests live in `fixtures_test.go` under the same build tag. They drive the helper against a `TestHelperProcess`-style fake `pyry` set via `PYRY_E2E_BIN` (so `ensurePyryBuilt` short-circuits to the test binary itself). The fake selects its behaviour via `PYRY_E2E_FAKE_MODE`, passed through `RunOpts.ExtraEnv`.

The fake-pyry harness:

- One `TestHelperProcess(t *testing.T)` function that exits early unless `GO_TEST_HELPER_PROCESS=1`. Branch on `PYRY_E2E_FAKE_MODE` to emit fixture stdout/stderr/exit-code.
- A `fakePyryBin(t *testing.T) string` helper that returns the path to the test binary itself (`os.Args[0]`) and wires `GO_TEST_HELPER_PROCESS=1` via `RunOpts.ExtraEnv`. Set `PYRY_E2E_BIN` via `t.Setenv` so `ensurePyryBuilt` returns the test binary instead of building real pyry. (The fake must accept `pyry agent-run …` as its first argv tokens; the helper passes them through unchanged.)

Required scenarios (each is one `t.Run` subtest or top-level test):

- **Happy path**: fake emits one init line, one assistant event, one result trailer; exits 0. Assert `RunResult.SessionID == "11111111-1111-4111-8111-111111111111"`, `ExitCode == 0`, `Stdout` contains both the init line and the trailer (substring checks).
- **Validation positive case**: `validateRunOpts(fullyPopulatedOpts) == nil`.
- **Validation failures**: table-driven over each required-field violation. Each row asserts the returned error message names the field. Covers `Workdir`, `Prompt`, `SystemPrompt`, `AllowedTools`, `MaxTurns <= 0`, `Effort`, `Model`. Tests call `validateRunOpts` directly (NOT `RunPyryAgentRun`) so the `t.Fatalf` path doesn't get in the way.
- **Init-event absent**: fake exits non-zero (say, `2`) before emitting any stream-json. Assert `RunResult.ExitCode == 2`, `RunResult.SessionID == ""`, `t.Fatalf` is NOT called (the test still finishes).
- **Timeout**: fake sleeps 30s; `RunOpts.Timeout = 100 * time.Millisecond`. Assert the test fails with a `t.Fatalf` message embedding "timed out" and the stdout/stderr buffers. Use the same trick `internal/e2e/realclaude/fixtures_test.go` (#372) uses for missing-file: call into a private split that returns the error so it can be inspected, OR run the assertion inside a `t.Run` and capture via a buffered `testing.T` wrapper. Developer's choice; both satisfy the AC.
- **Argv contract**: fake echoes its argv to stderr (one per line) and exits 0. Assert each of the eight flags appears in stderr with the expected value. The assertion uses substring matching (e.g. `bytes.Contains(stderr, []byte("--prompt-file="+workdir+"/prompt.txt"))`) — order is not part of the contract.
- **`parseInitSessionID` unit tests**: small table driving the parser directly. Cases:
  - Empty input → `""`.
  - One init line with `session_id` → match.
  - Init line with empty `session_id` → `""` (skip).
  - Non-init `system` line first, init line second → match the init.
  - Result trailer with `session_id` but no init line → `""` (init only, by design).
  - Malformed JSON line before init line → skip, match the init.

### Test-package layout

All new tests live in package `realclaude` (same as production code, matches #372 pattern). Reuse `testSessionID` only if a test needs a session id literal that overlaps; otherwise introduce per-test UUIDs.

## Files touched

- `internal/e2e/realclaude/fixtures.go` — modified (~95 LoC appended).
- `internal/e2e/realclaude/fixtures_test.go` — modified (test extensions; `TestHelperProcess` + table tests).

No new files. No Makefile change. No changes outside `internal/e2e/realclaude/`.

## Open questions

- **Should `parseInitSessionID` also surface the trailer as a fallback?** No — spec choice. The AC mandates "init only" so failing tests still see a session_id when the run aborted before the trailer. A future ticket can grow a `ResultSessionID` field on `RunResult` if the asymmetry is ever observed in practice.
- **Should `RunOpts` re-validate the effort enum locally?** No. Same reason — keep the helper as a thin wire-format shim; let `pyry agent-run` own its enum gate.

## Out of scope

- Any helper that consumes `RunResult.SessionID` to find the session JSONL — that's `ReadJSONL` from #372, which downstream tests compose.
- Any session-resume plumbing. `pyry agent-run` doesn't accept it; if a future ticket grows it, this helper can grow with it.
- The fake-claude binary (`internal/e2e/internal/fakeclaude`). That's a different package and isn't reachable here. Tests inject behaviour via a fake **pyry**, not a fake claude.
