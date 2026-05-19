# #471 — `internal/agentrun/ptyrunner/` skeleton (spawn + idle wait + WritePrompt + clean shutdown)

Sub-issue of [#329](https://github.com/pyrycode/pyrycode/issues/329) (tracking). Split from #468. Siblings: #469, #470, #472.

## Files to read first

- `internal/agentrun/streamrunner/runner.go` — package doc + Config struct + `Run` shape this slice mirrors (package-doc style, "Required" / "Optional" field comments, named-return error contract). Read in full (~196 lines).
- `internal/agentrun/streamrunner/runner_test.go` — table-light test layout (one `helperRunCfg` builder + one test per scenario). Mirror the `t.Parallel()` + `context.WithTimeout` shape.
- `internal/agentrun/streamrunner/helper_test.go` — `TestStreamRunnerHelperProcess` pattern (env-keyed mode switch, `GO_*_HELPER=1` gate, modes return via `os.Exit`). The ptyrunner helper is structurally identical but writes its bytes such that the parent's `tuidriver.IsIdle` (and modal/banner detectors) react.
- `cmd/pyry/agent_run.go:233-282` — current streamrunner argv shape (`buildClaudeArgs`). Reference for which flags the PTY path strips (`--input-format`, `--output-format`, `--verbose`, `--dangerously-skip-permissions`, `--max-turns`) versus keeps (`--append-system-prompt-file`, `--model`, `--effort`).
- `docs/knowledge/codebase/392.md` — last package-level surgery on agentrun (dead-code removal of legacy `Drive` after stream-json migration); read for the "Pre-flight grep every export" lesson and to see the `internal/agentrun/workdir.go` post-#392 layout.
- `docs/knowledge/codebase/463.md` — knowledge-doc format reference (one-paragraph header, Implementation bullets, Files, Patterns established, Lessons learned, Related). The 471 knowledge doc must mirror this shape.
- **External read (GitHub)** — `pkg/tuidriver/session.go` at SHA `b09fe70e60a73d6d52c24707a16802bc1483d532` for the `Session` / `Spawn` / `WritePrompt` / `Close` contracts. Quote the doc-comments verbatim in the package doc where useful — they explain why naive `Write(prompt + "\r")` doesn't commit a long prompt.

The dependency is `github.com/pyrycode/tui-driver` (singular `driver`). Pin to SHA `b09fe70e60a73d6d52c24707a16802bc1483d532` — the first `main` commit after PR #43 (`WritePrompt`) merged on 2026-05-19. Use `go get github.com/pyrycode/tui-driver@b09fe70e60a73d6d52c24707a16802bc1483d532` to install; `go mod tidy` will populate `go.sum` for the transitive `github.com/hinshun/vt10x` and `github.com/google/uuid` deps tui-driver pulls in.

## Context

The current `pyry agent-run` drives claude as a stream-json subprocess via `internal/agentrun/streamrunner` (no PTY). Anthropic's 2026-06-15 billing policy article enumerates "Interactive Claude Code in the terminal or IDE" as subscription-eligible but does NOT explicitly name stream-json subprocess mode — leaving it in the "classification unknown" zone. Juhana's strategic decision (2026-05-19) is to pivot proactively to interactive-TUI claude driven via PTY (the explicitly-named subscription surface) before 2026-06-15.

The 2026-05-14 reasoning that ruled out PTY drive (`/doctor` poisoning, brittle ANSI handling, missing state-detection primitives) no longer applies — `github.com/pyrycode/tui-driver` landed comprehensively 2026-05-18 → 19 with the missing infrastructure (PTY lifecycle, state detection, modal handling, vt10x render, parsers, banner detectors, `WaitUntil`/`EncodeCwd`/`StripANSI`/`WritePrompt` helpers).

This slice is the **scaffolding-only** step: introduce `internal/agentrun/ptyrunner/` with the spawn → idle wait → WritePrompt → clean shutdown path. Nothing imports it yet. JSONL tail + stream-json re-emit + budget enforcement + watchdog land in [#472](https://github.com/pyrycode/pyrycode/issues/472); the `cmd/pyry/agent_run.go` cutover lands in [#470](https://github.com/pyrycode/pyrycode/issues/470).

## Design

### Package layout

```
internal/agentrun/ptyrunner/
  runner.go        — package doc + Config + Run
  helper_test.go   — TestPtyRunnerHelperProcess (fake-claude PTY child)
  runner_test.go   — test cases (happy, trust, mcp, network, ctx-cancel)
```

The package sits alongside `internal/agentrun/streamrunner/` (peers, not parent/child). No exports beyond `Config` and `Run`. The package doc mirrors streamrunner's structure (purpose, scope, dependency-direction note with the verifying `go list -deps` command, logging-discipline rule).

### `Config` (verbatim per AC #1)

```go
type Config struct {
    ClaudeBin    string
    WorkDir      string
    SessionID    string
    SettingsPath string
    SystemPrompt string
    Model        string
    Effort       string
    MaxTurns     int        // declared for #472; NOT consumed in this slice
    PromptBytes  []byte
    Stdout       io.Writer  // declared for #472; NOT written in this slice
    Stderr       io.Writer
    Env          []string
    Logger       *slog.Logger
}
```

**Required at entry:** `ClaudeBin`, `WorkDir`, `SessionID`, `SettingsPath`, `SystemPrompt`, `Model`, `Effort`, `PromptBytes`, `Stderr`. (`MaxTurns` and `Stdout` are declared per AC #1 for forward-compatibility with #472 but the field comments must say "declared for #472; NOT consumed in this slice" so a casual reader doesn't expect them to do anything.) `Env` and `Logger` are optional; nil `Logger` falls back to `slog.Default()`.

Field comments mirror streamrunner's style — one-line summary then a "Why" / "Note" sentence where relevant. Document the `MaxTurns` / `Stdout` forward-compat fields prominently: a reader scanning the struct should not be confused about why they aren't used in `Run`'s body.

`PromptBytes` MUST NOT appear in any log line or any wrapped-error message. The field doc-comment says so explicitly. The package's doc-comment repeats the rule at the file level (matches streamrunner's discipline).

### `Run` signature and contract

```go
func Run(ctx context.Context, cfg Config) error
```

**Return contract:**

- `nil` on a clean spawn → idle → WritePrompt → Close cycle.
- `nil` on `ctx.Err() != nil` after spawn (operator shutdown is success — same contract streamrunner uses).
- Wrapped error from required-field validation (e.g. `errors.New("ptyrunner: SessionID required")`). Plain `errors.New`, no `%w` — there's nothing to wrap.
- Wrapped error from spawn failure: `fmt.Errorf("ptyrunner: spawn: %w", err)`.
- Wrapped error from idle-wait ctx-cancel (pre-WritePrompt): collapsed to `nil` per the operator-shutdown rule. Only return the error if it is not `context.Canceled` / `context.DeadlineExceeded` — those collapse.
- Wrapped error from each modal/banner detector with a **distinct, named message** (AC #5). See § Modal safety below.
- Wrapped error from `WritePrompt` failure: `fmt.Errorf("ptyrunner: write prompt: %w", err)`.
- `Close` is called via `defer` and its error is logged at Warn level, not returned (close errors during shutdown are advisory; the operator-shutdown / spawn-success outcome already determined the return value).

### Argv construction

Per AC #2, an unexported helper `buildArgs(cfg Config) []string` returns:

```
--session-id <SessionID>
--settings <SettingsPath>
--permission-mode default
--append-system-prompt-file <SystemPrompt>
--model <Model>
--effort <Effort>
```

**No** `--max-turns`, **no** `--input-format`, **no** `--output-format`, **no** `--verbose`, **no** `--dangerously-skip-permissions`, **no** `--allowed-tools` (the settings file produced by #469 carries the allowed-tools list as JSON instead). The helper is a pure function returning a fresh slice; trivial to table-test (one happy case is enough — there's no branching).

### Spawn sequence (verbatim mapping to ACs #3, #4, #5, #6, #7)

```
1. Validate required fields → return wrapped error on miss.
2. cmd := exec.CommandContext(ctx, cfg.ClaudeBin, buildArgs(cfg)...)
   cmd.Dir = cfg.WorkDir
   cmd.Stderr = cfg.Stderr
   if cfg.Env != nil { cmd.Env = append(os.Environ(), cfg.Env...) }
3. tuidriver.EnsureClaudeEnv(cmd)        // adds TERM=xterm-256color
4. sess, err := tuidriver.Spawn(cmd, tuidriver.SpawnOpts{})
   if err != nil { return fmt.Errorf("ptyrunner: spawn: %w", err) }
   defer sess.Close()
5. err = tuidriver.WaitUntil(ctx, func() bool {
       return tuidriver.IsIdle(sess.Buffer.Snapshot())
   })
   if err != nil { return nil if ctx-cancel else wrapped error }
6. snap := sess.Buffer.Snapshot()
   if tuidriver.HasTrustModal(snap)      → return errTrustModal
   if tuidriver.HasMcpFailureBanner(snap) → return errMcpFailure
   if tuidriver.HasNetworkFailure(snap)   → return errNetworkFailure
7. if err := sess.WritePrompt(string(cfg.PromptBytes)); err != nil {
       return fmt.Errorf("ptyrunner: write prompt: %w", err)
   }
8. return nil
```

`PromptBytes` is `[]byte` (per AC #1) but `Session.WritePrompt` takes `string`. The conversion `string(cfg.PromptBytes)` is the only call site that crosses the boundary; no helper warranted.

### Modal safety check

Three sentinel-shape wrapped errors. Define them as package vars so consumers (and the developer's tests) can match with `errors.Is`. Each one names the failing detector explicitly and points to the remediation:

- `var ErrTrustModalDetected = errors.New("ptyrunner: trust-folder modal detected; pre-write trust via #469's MarkWorkdirTrusted before invoking Run")`
- `var ErrMcpFailureBanner = errors.New("ptyrunner: MCP failure banner detected; check claude's MCP server config")`
- `var ErrNetworkFailure = errors.New("ptyrunner: network failure detected (FailedToOpenSocket); claude API unreachable")`

The three messages must be **distinct strings** (AC #5: "Same wrapped-error shape (distinct messages naming the failing detector)"). The "naming the failing detector" requirement is the load-bearing part: `errors.Is(err, ErrTrustModalDetected)` is how #470 (cmd cutover) will distinguish the trust-modal case from the others to surface a remediation hint.

The detector calls are ordered: trust, mcp-failure, network. Trust is first because it's the most actionable (one-shot pre-write fixes it forever). Order does not affect correctness — the detectors are mutually exclusive in practice.

**Why no detector polling during the WaitUntil loop:** the AC explicitly pins the idle-then-detect ordering ("Modal safety check at idle"). The trust modal renders the `❯` glyph in its input field, so `IsIdle` returns `true` even inside the modal — the post-idle check is precisely what disambiguates. The MCP failure banner renders in the status bar alongside idle (it's not a full modal). The network failure case (`FailedToOpenSocket` + indefinite spinner) won't normally satisfy `IsIdle` before context timeout; the post-idle check is a defensive measure for the rare race where the failure marker lands in the buffer just as idle is reached. The watchdog ([#472](https://github.com/pyrycode/pyrycode/issues/472)) is the catchall for the spinner-frozen case.

### Concurrency model

tuidriver owns both background goroutines (PTY reader, `cmd.Wait` observer). The reader appends bytes into `sess.Buffer`; the wait observer closes `sess.exited` on child termination. `Run` is straight-line foreground code — no goroutines, no channels, no timers in this package. `tuidriver.WaitUntil` polls at 50ms via a `time.Ticker` internal to that package.

`sess.Close()` (deferred) handles SIGTERM → grace → SIGKILL → PTY close → reader-goroutine join. Idempotent — multiple `Close` calls from defer-stacks are safe.

### Logging discipline

`slog` only, structured fields. Logger comes from `cfg.Logger` or `slog.Default()`. Allowed log lines (Warn level):

- `"ptyrunner: spawn failed"` with `err`
- `"ptyrunner: close failed"` with `err`
- `"ptyrunner: trust modal detected"` / `"ptyrunner: mcp failure banner detected"` / `"ptyrunner: network failure detected"` (no buffer contents in the log fields)

Forbidden (mirrored from streamrunner's discipline + AC #11): any log call that includes `cfg.PromptBytes`, any substring of `sess.Buffer.Snapshot()`, or any rendered TUI content.

### Package doc

Three-paragraph package doc on `runner.go`, mirroring `streamrunner/runner.go`'s structure:

1. **Purpose** — what `ptyrunner` does (spawn claude in PTY under tui-driver, wait for idle, submit a prompt, tear down). Names the consumer (`pyry agent-run` post-cutover via #470) and the boundary with sibling agentrun subpackages.
2. **Logging discipline** — never log `PromptBytes` content, never log buffer substrings; writers (Stderr now, Stdout in #472) are opaque.
3. **Dependency direction** — must not import `internal/supervisor`, `internal/agentrun/jsonl`, `internal/agentrun/streamjson`, `internal/agentrun/budget`. Verify with:
   ```
   go list -deps ./internal/agentrun/ptyrunner/... | grep -E 'pyrycode/internal/(supervisor|agentrun/(jsonl|streamjson|budget))'
   ```
   Expected output: empty. (Identical pattern to streamrunner's package doc.)

## Error handling — failure modes table

| Stage | Failure | Return |
| --- | --- | --- |
| Required-field validation | Missing field | `errors.New("ptyrunner: <field> required")` |
| Spawn | `tuidriver.Spawn` returns error | `fmt.Errorf("ptyrunner: spawn: %w", err)` |
| Idle wait | `ctx` cancelled / deadline exceeded | `nil` (operator-shutdown rule) |
| Idle wait | Other error from `WaitUntil` (currently none; defensive) | `fmt.Errorf("ptyrunner: wait idle: %w", err)` |
| Modal check | `HasTrustModal` true | `ErrTrustModalDetected` |
| Modal check | `HasMcpFailureBanner` true | `ErrMcpFailureBanner` |
| Modal check | `HasNetworkFailure` true | `ErrNetworkFailure` |
| WritePrompt | Write returns error | `fmt.Errorf("ptyrunner: write prompt: %w", err)` |
| Close (via defer) | Returns non-nil | Warn-log; do not surface |

Ctx-cancel-during-spawn (`tuidriver.Spawn` itself fails before returning the session) returns the wrapped spawn error if it's not a ctx error, or `nil` if `errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)`. Same collapse rule as streamrunner.

## Testing strategy

Use `TestHelperProcess`-style fake-claude (mirror `streamrunner/helper_test.go`). The test binary re-execs itself with `GO_PTYRUNNER_HELPER=1` and a `GO_PTYRUNNER_HELPER_MODE` env var keyed switch. Each mode writes a small payload to `os.Stdout` that satisfies (or violates) the tuidriver detectors:

| Mode | Bytes written | Then |
| --- | --- | --- |
| `idle` | `❯ ` (UTF-8 `\xe2\x9d\xaf` + space) — satisfies `IsIdle` (❯ present, ✻ absent). | Read stdin to EOF; install SIGTERM handler; sleep 30s. |
| `trust` | The trust-folder header `"Quicksafetycheck"` (anchor for `HasTrustModal`) + the `❯` glyph (so `IsIdle` returns true and the post-idle check fires). | Same SIGTERM-then-sleep. |
| `mcp_failure` | `"1 MCP server failed"` + `❯`. | Same. |
| `network_failure` | `"FailedToOpenSocket"` + `❯`. | Same. |
| `slow_spawn` | (Used by ctx-cancel test — sleep 5s **before** writing anything, so the parent's ctx cancel fires inside `WaitUntil`.) | Same. |

The helper's SIGTERM handler is critical because `sess.Close()` SIGTERMs the child during teardown. Without a handler, the child gets killed and the test exit-code path differs across modes.

### Test cases (bullet-pointed, not pre-written)

- `TestRun_HappyPath`
  - Mode `idle`, prompt `"hello"`. Expect `Run` returns `nil`. The helper does not parse stdin, so we can't directly assert that WritePrompt's bytes arrived — instead assert that the test's PTY observer (a small `tuidriver.Spawn` Mirror writer attached via a custom path, OR a check that `Run` returned without error) saw clean completion. Simplest assertion: `Run` returned `nil` and elapsed < 2s.
  - **Optional richer assertion:** swap the mode helper to echo any PTY-received bytes to a side file (`GO_PTYRUNNER_HELPER_CAPTURE_FILE=...`), then read the file and verify it contains the bracketed-paste sequence `\x1b[200~hello\x1b[201~\r`. This validates the contract that `WritePrompt` (not `Write`) was used. Recommended.

- `TestRun_TrustModalDetected`
  - Mode `trust`. Expect `Run` returns an error and `errors.Is(err, ErrTrustModalDetected)` is true. Message contains the substring `"#469's MarkWorkdirTrusted"` (so the operator-facing prefix points at the remediation).

- `TestRun_McpFailureDetected`
  - Mode `mcp_failure`. Expect `errors.Is(err, ErrMcpFailureBanner)` is true. Message names the MCP failure banner.

- `TestRun_NetworkFailureDetected`
  - Mode `network_failure`. Expect `errors.Is(err, ErrNetworkFailure)` is true. Message names the network failure with the `FailedToOpenSocket` anchor.

- `TestRun_CtxCancelDuringSpawn`
  - Mode `slow_spawn`. `ctx, cancel := context.WithCancel(ctx)`; goroutine sleeps 100ms then `cancel()`. Expect `Run` returns `nil` (operator-shutdown collapse). Elapsed < 6s (SIGTERM grace ≤ 3s tui-driver default, + slack).

`Config.PromptBytes` for all tests is a short non-empty byte slice (`[]byte("hi")` is enough). One test should use a deliberately tricky prompt with a newline + UTF-8 sequence to verify `WritePrompt`'s bracketed-paste wrapping survives the round-trip — fold into `TestRun_HappyPath` as a second sub-test if the capture-file richer assertion is taken.

### Test helpers

`helperRunCfg(t *testing.T, mode string, stderr *bytes.Buffer, extraEnv ...string) Config` — mirrors streamrunner's `helperRunCfg`. Returns a `Config` wired to `TestPtyRunnerHelperProcess` via `os.Args[0]` + `GO_PTYRUNNER_HELPER=1` + mode env. Stdout is omitted (this slice does not consume it); Stderr is the buffer; required string fields (`SessionID`, `SettingsPath`, `SystemPrompt`, `Model`, `Effort`) get arbitrary non-empty test values.

### CI considerations

PTY-dependent tests must work on CI runners (no controlling terminal). `tuidriver.Spawn` uses `pty.Start` which allocates a PTY pair from the kernel — does not require the parent process to own a terminal. The existing `creack/pty` v1.1.24 (already in go.mod, used by `internal/supervisor`) is the same dep tui-driver pulls in via its own go.mod. CI is fine.

### Tests do NOT need real claude

The fake-claude helper writes synthetic PTY bytes that satisfy the tuidriver detectors. The detectors are pure functions over `[]byte` snapshots — they don't care that a real claude wrote the bytes. This mirrors the streamrunner test pattern.

## Open questions

- **Should `Run` accept a hook for "after idle, before WritePrompt"?** No — #472 will extend `Run` in-place; no hook needed. Out of scope for this slice.
- **What if `sess.Close()` returns an error after the body returned `nil`?** Log Warn, don't surface — the body's `nil` return is already the operator-visible outcome, and the close error is best-effort cleanup. Same pattern streamrunner uses for stdin close failures.
- **Should the package expose a constructor like `NewRunner(cfg) (*Runner, error)`?** No — `Run` is one-shot per spawn. The package follows streamrunner's stateless function shape, not a long-lived object.

## Out of scope (for the developer)

- Anything touching `internal/agentrun/jsonl`, `internal/agentrun/streamjson`, `internal/agentrun/budget` — wired in #472.
- The trust pre-write + settings JSON file generation — produced by #469.
- The `cmd/pyry/agent_run.go` cutover from streamrunner to ptyrunner — wired in #470.
- Deletion of streamrunner — not in this migration phase.
- `--max-turns` enforcement — #472 implements pyry-side budget counter; the flag itself is intentionally absent from the argv per AC #2.

## Knowledge-base note

After implementation, write `docs/knowledge/codebase/471.md` mirroring the format of `docs/knowledge/codebase/392.md` and `docs/knowledge/codebase/463.md`. Content checklist:

- One-paragraph header naming the slice, citing the parent (#329) and split (#468), and naming the three siblings (#469, #470, #472).
- **Implementation** bullets: file inventory (one production file + two test files, LOC counts), the argv shape (what flags are passed, which are intentionally absent + why), the three `Err*` sentinels and how `errors.Is` is used downstream, the helper-process mode table, the WritePrompt-vs-Write rationale (link to tui-driver PR #43).
- **Patterns established:** Spawn primitive package shape (mirrors streamrunner: stateless `Run` function + Config struct + sentinel-error vars exported via `errors.Is`); forward-compatibility fields declared-but-not-wired with clear comments (the `MaxTurns` / `Stdout` precedent for the next slice to fill in without breaking the public shape).
- **Lessons learned:** anything that surfaced during implementation. If the architect's argv list missed a flag claude requires, document it the way #392 documents the workdir-helper miss.
- **Related** links: #329 (parent tracking), #468 (source split), #469/#470/#472 (siblings), `codebase/390.md` + `codebase/391.md` (streamrunner introduction + runtime cutover — the precedent this slice extends), tui-driver PR #43 (WritePrompt rationale).

The knowledge doc is part of the developer's deliverable, not a follow-up — it ships in the same PR as the code.

## File inventory

| File | New / Modified | Lines (est.) |
| --- | --- | --- |
| `internal/agentrun/ptyrunner/runner.go` | New | ~200 |
| `internal/agentrun/ptyrunner/helper_test.go` | New | ~120 |
| `internal/agentrun/ptyrunner/runner_test.go` | New | ~160 |
| `docs/knowledge/codebase/471.md` | New | ~50 |
| `go.mod` | Modified | +1 require line |
| `go.sum` | Modified | several lines (tui-driver + vt10x + uuid hashes) |

Total: ~530 lines of written work. One production source file, two test files. Zero consumer-side edits.
