# Spec — #363 e2e/realclaude: shared fixture helpers

## Files to read first

Turn-1 reading list. Open these before writing code; they pin the contracts the helpers must compose over.

- `internal/e2e/realclaude/smoke_test.go` — the existing scaffold (build tag `e2e_realclaude`, package `realclaude`). New helpers live alongside it and share the tag.
- `internal/agentrun/jsonl/reader.go:39-132` — `Event`, `UsageBlock`, `Config`, `NewReader`, `Next`/`Offset`. Public surface the helpers wrap; do NOT duplicate the parser.
- `internal/agentrun/trust.go:25-51` — `ResolveWorkdir` + `EncodeProjectDir`. These compute the `~/.claude/projects/<encoded>` segment; helpers MUST call `EncodeProjectDir` rather than re-implement the `/`→`-` + `.`→`-` mapping.
- `cmd/pyry/agent_run.go:31-159` — `agentRunArgs` and `parseAgentRunArgs`. The required flag set is the contract `RunOpts` maps onto. The `--effort` enum (`low|medium|high|xhigh|max`), `--output-format stream-json` requirement, and `--max-turns > 0` invariant all matter.
- `cmd/pyry/agent_run.go:199-296` — `runAgentRun` stdout contract: `settings-file: <path>\n` line, then re-emitted stream-json events, then exactly one `type:"result"` trailer with `session_id`.
- `internal/agentrun/streamjson/emitter.go:254-273` — `trailer` JSON shape; `session_id` is the field the helper parses out for `pyry agent-run` mode.
- `internal/e2e/harness.go:106-141` — `ensurePyryBuilt` pattern (sync.Once + `go build`, `PYRY_E2E_BIN` short-circuit). Mirrored locally; the two `internal/e2e` packages live under different build tags and cannot share the binary cache directly.
- `internal/e2e/harness.go:507-520` — `childEnv` (env scrub + HOME override). Same pattern for the fake-`pyry` subprocess in unit tests.
- `docs/lessons.md` § "Claude session storage on disk" — encoded-cwd rule (`/` AND `.` → `-`). Implementation lives in `EncodeProjectDir`; cited here so the developer knows the lesson exists if the encoding ever surprises.

## Context

`internal/e2e/realclaude/` is the build-tagged suite that exercises the real `claude` binary end-to-end (#361 landed the scaffold). Every downstream test (#364–#368) needs three primitives in common: a throwaway workdir, a way to invoke `pyry agent-run` (or `claude -p`) capturing exit+session+stdout+stderr, and a way to read the resulting session JSONL. Without shared helpers each test would re-implement ~50 lines of setup and small drift between copies would defeat the entire point of the suite (catching real-vs-test divergence). This ticket lands the helpers; downstream tickets consume them.

## Design

### Package layout

One source file, one test file, both build-tag-gated by `e2e_realclaude` (mirrors `smoke_test.go`):

```
internal/e2e/realclaude/
  smoke_test.go           // existing scaffold (#361)
  fixtures.go             // NEW — //go:build e2e_realclaude
  fixtures_test.go        // NEW — //go:build e2e_realclaude
```

**Build-tag choice rationale.** The AC offers three shapes: untagged `fixtures.go`, `fixtures_test.go`, or tagged `fixtures.go`. Untagged would compile under plain `go build` even though the helpers are test-only utility code — wasted compile + risk of accidentally leaking into a non-test binary. `_test.go` works for in-package callers but rules out external test packages (e.g. a sibling `realclaude_test` package). Tagged `fixtures.go` is the only option that (a) keeps `make test` from compiling it, (b) lets `make e2e-realclaude` compile and use it, and (c) keeps it importable as a non-test symbol if a future external test package wants it. Picks the tagged-`fixtures.go` shape.

### Public surface

```go
//go:build e2e_realclaude

package realclaude

// Mode selects which CLI the runner invokes. Exported because callers
// build RunOpts literally; the zero value is intentionally invalid so a
// missing Mode fails fast rather than silently picking a default.
type Mode int

const (
    ModeUnset    Mode = iota // zero value; RunPyryAgentRun rejects.
    ModeAgentRun             // invokes `pyry agent-run` with the full flag set.
    ModeClaudeP              // invokes `claude -p --output-format stream-json --session-id <uuid>`.
)

// RunOpts is the input to RunPyryAgentRun. Field names mirror the
// `pyry agent-run` flag surface in cmd/pyry/agent_run.go so the
// translation table is direct.
type RunOpts struct {
    Mode         Mode     // required; ModeUnset → error
    Workdir      string   // required; the `--workdir` for pyry agent-run, the cwd for claude -p
    Prompt       string   // user-prompt content; helper writes to <Workdir>/prompt.txt
    SystemPrompt string   // appended-system-prompt content; helper writes to <Workdir>/system.txt
    AllowedTools []string // joined by "," for `--allowed-tools`
    MaxTurns     int      // `--max-turns`; must be > 0 for ModeAgentRun
    Effort       string   // `--effort`; one of low|medium|high|xhigh|max
    Model        string   // `--model`
    // SessionID is optional. Empty for ModeAgentRun (pyry mints its
    // own and the helper parses it back from the trailer). For
    // ModeClaudeP the helper mints a UUIDv4 if empty and passes it as
    // claude's `--session-id`.
    SessionID string
    // ExtraEnv is appended verbatim to the subprocess env after HOME
    // override. Each entry is "KEY=VALUE".
    ExtraEnv []string
    // Timeout caps the subprocess wall-clock. Zero defaults to 5 minutes.
    Timeout time.Duration
}

// RunResult is the outcome of one RunPyryAgentRun invocation.
type RunResult struct {
    ExitCode  int
    SessionID string // resolved post-run; non-empty on success
    Stdout    []byte
    Stderr    []byte
}

// JSONLEntry is a thin alias over the underlying parser type so
// callers don't import internal/agentrun/jsonl directly.
type JSONLEntry = jsonl.Event

// WithWorktree allocates a fresh tmpdir, pins the test process's HOME
// to it via t.Setenv (so os.UserHomeDir() in this process AND any child
// subprocess that inherits env both resolve to the same root), and
// returns the workdir path. The workdir IS the HOME root — claude
// writes its session JSONL under <HOME>/.claude/projects/<encoded>/.
func WithWorktree(t *testing.T) (workdir string)

// RunPyryAgentRun builds (once per test process) the pyry binary,
// writes the prompt/system-prompt to files under opts.Workdir, invokes
// the configured CLI, captures all three streams, parses the session
// ID, and returns. Fails the test on exec error or timeout; a non-zero
// exit is NOT a failure — callers assert on RunResult.ExitCode.
func RunPyryAgentRun(t *testing.T, opts RunOpts) RunResult

// ReadJSONL opens <HOME>/.claude/projects/<EncodeProjectDir(workdir)>/<sessionID>.jsonl
// and consumes it through jsonl.Reader, collecting every Event into a
// slice. Fails the test on open or read error. Returns an empty slice
// if the file is present but empty.
func ReadJSONL(t *testing.T, workdir, sessionID string) []JSONLEntry
```

### Behaviour contracts

**`WithWorktree`.**
- One-liner: `tmpdir := t.TempDir(); t.Setenv("HOME", tmpdir); return tmpdir`.
- `t.TempDir()` registers cleanup automatically. `t.Setenv` restores the prior HOME on cleanup. No additional `t.Cleanup` needed.
- Pinning HOME via `t.Setenv` is what makes `ReadJSONL`'s later `os.UserHomeDir()` resolve to the same directory the subprocess wrote into. The subprocess inherits HOME naturally through `os.Environ()`.
- No mkdir of `.claude/` — claude / pyry create it on first write.

**`RunPyryAgentRun`.**
- `ModeUnset` returns a zero-value `RunResult` and `t.Fatalf`s with a usable message.
- Builds the pyry binary lazily via a package-local `sync.Once` + `go build`. Honours `PYRY_E2E_BIN` for a pre-built binary (CI). The build cache is a fresh `os.MkdirTemp` per test process. **Duplicated** from `internal/e2e/harness.go`'s `ensurePyryBuilt` rather than extracted into a shared helper: the two `internal/e2e` packages compile under disjoint build tags (`e2e || e2e_install` vs `e2e_realclaude`), and a shared helper would need a third tag-set. Per PROJECT-MEMORY's "Resist over-DRY on duplicated registry primitives" pattern — accept the duplication until a fourth tag-set forces extraction.
- Writes `opts.Prompt` to `<Workdir>/prompt.txt` and `opts.SystemPrompt` to `<Workdir>/system.txt` with `0o600`. These are the `--prompt-file` and `--system-prompt-file` inputs.
- `ModeAgentRun`: invokes `pyry agent-run --prompt-file=… --system-prompt-file=… --allowed-tools=… --max-turns=… --effort=… --model=… --workdir=… --output-format=stream-json`. Captures stdout/stderr in `bytes.Buffer`. Subprocess env starts from `os.Environ()` (which already has HOME=workdir from `WithWorktree`) then appends `opts.ExtraEnv`.
- `ModeClaudeP`: invokes `claude -p --output-format=stream-json --session-id=<uuid> --model=<model>` with the user prompt piped via stdin (the prompt content, not a file — claude -p reads the prompt argv-or-stdin). Mints a UUIDv4 if `opts.SessionID` is empty (lift the same crypto/rand recipe from `cmd/pyry/agent_run.go:316`). The `claude -p` flag surface is intentionally narrow here — downstream tests pick this mode only to assert the parity case against `pyry agent-run`; they will not exercise the full `--allowed-tools` / `--effort` set on raw claude.
- Timeout: `context.WithTimeout` with `opts.Timeout` (default 5min). On timeout, `t.Fatalf` after recording stdout/stderr so the operator sees what the run was doing.
- Session-ID resolution (the architect's "pick one approach" choice):
  - `ModeAgentRun`: scan `RunResult.Stdout` line-by-line, find the single `{"type":"result", …}` trailer, decode just the `session_id` field via `json.RawMessage`. Trailer presence is guaranteed by `streamjson.Emitter.Close` — emitter contract pinned by `cmd/pyry/agent_run.go:281-283`. If the trailer is absent (subprocess crashed before close), `SessionID` is left empty in `RunResult` and the helper returns; callers can still assert on `ExitCode`/`Stderr`.
  - `ModeClaudeP`: `opts.SessionID` was either supplied or minted before exec; the helper returns it verbatim.

**`ReadJSONL`.**
- Resolves `home, err := os.UserHomeDir()` — wraps the `t.Setenv` HOME from `WithWorktree`.
- Computes `enc, err := agentrun.EncodeProjectDir(workdir)`. Path = `filepath.Join(home, ".claude", "projects", enc, sessionID + ".jsonl")`.
- Opens the file (`os.Open` — `t.Fatalf` on error so the missing-JSONL case surfaces with the resolved path in the message).
- Constructs `jsonl.NewReader(f, jsonl.Config{})` and loops `Next()` until `io.EOF`, appending each `Event` to the result slice. `jsonl.ErrLineTooLarge` or other non-EOF error → `t.Fatalf`.
- Returns `[]JSONLEntry` (the alias).

### Test plan (fixtures_test.go)

All three helpers tested with stdlib only, behind the same `e2e_realclaude` tag (so `make test` skips them; `make e2e-realclaude` runs them). Tests do NOT depend on the real `claude` binary — they use a `TestHelperProcess`-style fake `pyry` invoked via `PYRY_E2E_BIN`.

- **`TestWithWorktree`** — verify the returned path exists, is a directory, equals the test process's `os.UserHomeDir()` post-call, and the directory is gone after the subtest exits. Scenario list:
  - dir exists and is empty
  - `os.UserHomeDir()` equals the returned workdir
  - HOME restored on subtest cleanup (use `t.Run` and re-check after)
- **`TestRunPyryAgentRun_AgentRunMode`** — set `PYRY_E2E_BIN` to a `TestHelperProcess` fake that prints a known `settings-file:` line, a single assistant event, and a trailer with `session_id=11111111-1111-4111-8111-111111111111`. Assert `RunResult.SessionID` matches; assert `ExitCode == 0`; assert `Stdout` contains the trailer.
- **`TestRunPyryAgentRun_ClaudePMode`** — fake binary echoes args to stderr; the helper minted a UUIDv4; assert `RunResult.SessionID == opts.SessionID`-or-newly-minted-UUIDv4 (length 36, hyphen positions); assert `--session-id` appeared in the fake's args. Tests the mint path even though no real claude runs.
- **`TestRunPyryAgentRun_ModeUnset`** — `RunOpts{}` triggers `t.Fatalf`. Verify with the `testing.T` subtest trick (a `*testing.T` wrapped via a goroutine + recover is overkill — instead use a small `t.Run` that's expected to fail and assert via a custom helper, or just document that the failure path is exercised manually since reproducing a `t.Fatalf` inside a sibling test is awkward). **Simpler: pull the validation into a private `validate(opts) error` helper and unit-test that; the public function's `t.Fatalf(err)` wrapper is trivial.** Recommend the refactor.
- **`TestRunPyryAgentRun_Timeout`** — fake `pyry` sleeps longer than `opts.Timeout`; assert the helper records the timeout via `t.Fatalf` (use the same private validate-then-execute split so the timeout path can be unit-tested without invoking the public function).
- **`TestReadJSONL_HappyPath`** — pre-populate `<HOME>/.claude/projects/<enc>/<uuid>.jsonl` with two fixture lines (one assistant `end_turn`, one user). Call `ReadJSONL`, assert `len(events) == 2`, assert `events[0].EndOfTurn == true`.
- **`TestReadJSONL_EmptyFile`** — create the JSONL file empty, assert `len(events) == 0`, no error.
- **`TestReadJSONL_MissingFile`** — no file at expected path → `t.Fatalf` with the resolved path embedded in the message. Use the same validate-then-execute split.
- **`TestJSONLEntry_IsAlias`** — single-line check: `var _ jsonl.Event = JSONLEntry{}`. Pins the alias contract so a future refactor to a wrapper struct doesn't silently break re-export.

### Concurrency model

None. All three helpers are synchronous from the test's perspective. The `sync.Once` guarding `ensurePyryBuilt` is the only concurrency primitive, and it's used identically to `internal/e2e/harness.go`. Subprocess wait is `cmd.Run()` (blocking) under a `context.WithTimeout`.

### Error handling

Helpers `t.Fatalf` on any failure that prevents producing a meaningful `RunResult`:

- `WithWorktree`: `t.TempDir()` already `t.Fatal`s internally; nothing else can fail.
- `RunPyryAgentRun`: build failure, exec failure, timeout, malformed `RunOpts` (`ModeUnset`, missing `Workdir`, missing required fields per mode). Non-zero exit codes are NOT failures.
- `ReadJSONL`: open error, parse error, encode error. An empty file is success with an empty slice.

No `error` returns on any public function — `*testing.T` is the error channel, matching `internal/e2e/harness.go`'s convention.

## Open questions

- **claude -p prompt delivery shape.** The current spec assumes `claude -p` takes the user prompt on stdin. If the downstream consumer in #364–#368 needs argv-form (`claude -p "<prompt>"`), the helper grows a `RunOpts.PromptVia` field. Defer until a real consumer needs it; the stdin form covers the common case.
- **PYRY_E2E_REALCLAUDE_BIN vs PYRY_E2E_BIN.** This spec reuses `PYRY_E2E_BIN` as the env var for the cached pyry binary. If CI ever wants to ship a different pre-built binary to the realclaude suite than to the regular e2e suite, this conflicts. Acceptable in v1; rename to `PYRY_E2E_REALCLAUDE_BIN` only if the conflict materialises.
- **Fake-binary plumbing.** `TestHelperProcess` is the canonical Go pattern but it requires the test binary to recognise a sentinel arg. The simpler alternative is a tiny `internal/e2e/realclaude/internal/fakepyry` Go binary built once per test process (mirroring `internal/e2e/internal/fakeclaude`). Both shapes work; the developer picks based on whether they want a second `go build` in the test process. The size budget (~100 LoC for fixtures.go + a roughly-equivalent test file) leaves no room for a vendored fakepyry binary in this ticket — recommend `TestHelperProcess`.

## Notes for the developer

- The size budget (≤~100 LoC for `fixtures.go`) is tight. If the implementation goes above ~120 LoC, stop and route back via `needs-rework:po` rather than expanding the spec — the AC explicitly calls this out.
- Do NOT call into `internal/e2e` (the other tag). The two packages are siblings, not parent/child; the build-tag disjunction means any direct import would compile-fail under `e2e_realclaude` alone.
- The `JSONLEntry` alias keeps callers from importing `internal/agentrun/jsonl` directly; preserve that by making `jsonl` an unexported dependency of `fixtures.go` and never re-exporting `jsonl.UsageBlock` etc. (consumers can reach them through `entry.Usage` because the field types travel with the alias).
- The trailer-parsing code is small enough to inline; do NOT factor a `parseTrailer` helper into `internal/agentrun/streamjson` — keep the dependency direction one-way.
