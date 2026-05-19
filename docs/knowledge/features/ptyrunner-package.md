# `internal/agentrun/ptyrunner` — interactive-TUI claude spawn primitive

PTY-driven sibling of [`internal/agentrun/streamrunner`](streamrunner-package.md): spawns `claude` as an interactive TUI under [`github.com/pyrycode/tui-driver`](https://github.com/pyrycode/tui-driver), waits for the TUI to reach idle, checks for trust-folder / MCP-failure / network-failure modals at the post-idle snapshot, submits one user prompt via `Session.WritePrompt` (bracketed-paste), and tears the session down through `Session.Close` (SIGTERM → grace → SIGKILL → PTY close).

Introduced #471 as a scaffolding-only slice. No production caller imports it yet — [`pyry agent-run`](pyry-agent-run-command.md) will cut over from `streamrunner` to `ptyrunner` in #470 once #469 (trust pre-write + deny-default settings JSON) and #472 (JSONL tail + stream-json re-emit + pyry-side budget + watchdog) land. The pivot is driven by Anthropic's 2026-06-15 billing policy: the article enumerates "Interactive Claude Code in the terminal or IDE" as subscription-eligible but does not name the stream-json subprocess surface that `streamrunner` drives — pyrycode pivots proactively to the named surface before the deadline.

## Public API

```go
type Config struct {
    ClaudeBin    string       // required; resolved path to claude
    WorkDir      string       // required; child cwd
    SessionID    string       // required; pyry-minted UUID, passed via --session-id
    SettingsPath string       // required; deny-default settings JSON (produced by #469), --settings
    SystemPrompt string       // required; system-prompt file, --append-system-prompt-file
    Model        string       // required; --model
    Effort       string       // required; --effort
    MaxTurns     int          // declared for #472; NOT consumed in this slice
    PromptBytes  []byte       // required; user-turn prompt, submitted via Session.WritePrompt
    Stdout       io.Writer    // declared for #472 (stream-json re-emit); NOT written in this slice
    Stderr       io.Writer    // required; child stderr
    Env          []string     // optional; appended to os.Environ() in the child
    Logger       *slog.Logger // optional; defaults to slog.Default()
}

// Run spawns claude under tui-driver with the argv buildArgs produces, waits
// for IsIdle, runs the trust / mcp-failure / network-failure detectors at the
// post-idle snapshot, calls Session.WritePrompt(cfg.PromptBytes), and returns.
func Run(ctx context.Context, cfg Config) error

var (
    ErrTrustModalDetected = errors.New("ptyrunner: trust-folder modal detected; pre-write trust via #469's MarkWorkdirTrusted before invoking Run")
    ErrMcpFailureBanner   = errors.New("ptyrunner: MCP failure banner detected; check claude's MCP server config")
    ErrNetworkFailure     = errors.New("ptyrunner: network failure detected (FailedToOpenSocket); claude API unreachable")
)
```

No constructor, no long-lived object — same stateless shape `streamrunner` ships. The three sentinel errors are matched with `errors.Is` so #470 (cmd cutover) can route the trust-modal case to a remediation hint distinct from MCP/network surfacing.

## Argv shape

`buildArgs(cfg)` returns the fixed six-pair sequence:

```
--session-id <SessionID>
--settings <SettingsPath>
--permission-mode default
--append-system-prompt-file <SystemPrompt>
--model <Model>
--effort <Effort>
```

Intentionally **absent** from the argv:

- `--input-format` / `--output-format` / `--verbose` — stream-json mode markers; the interactive TUI rejects them.
- `--dangerously-skip-permissions` — replaced by the deny-default settings JSON from #469.
- `--max-turns` — the interactive TUI ignores it; #472 enforces the cap pyry-side via a budget counter.
- `--allowed-tools` — carried as JSON inside the settings file produced by #469.

`TestBuildArgs` pins the six pairs and includes a forbidden-flag loop so the absences are regression-protected.

## Spawn sequence

```
1. Validate required fields → return wrapped errors.New("ptyrunner: <field> required") on miss.
2. exec.CommandContext(ctx, cfg.ClaudeBin, buildArgs(cfg)...)
   cmd.Dir = cfg.WorkDir; cmd.Stderr = cfg.Stderr
   if cfg.Env != nil { cmd.Env = append(os.Environ(), cfg.Env...) }
3. tuidriver.EnsureClaudeEnv(cmd)               // sets TERM=xterm-256color
4. sess, err := tuidriver.Spawn(cmd, tuidriver.SpawnOpts{})
   defer sess.Close()
5. tuidriver.WaitUntil(ctx, func() bool { return tuidriver.IsIdle(sess.Buffer.Snapshot()) })
6. snap := sess.Buffer.Snapshot()
   HasTrustModal(snap)      → ErrTrustModalDetected
   HasMcpFailureBanner(snap) → ErrMcpFailureBanner
   HasNetworkFailure(snap)   → ErrNetworkFailure
7. sess.WritePrompt(string(cfg.PromptBytes))
8. return nil
```

The modal/banner detection runs **after** idle — the trust modal renders the `❯` glyph in its input field, so `IsIdle` returns true even inside it; the post-idle check is precisely what disambiguates. Detector order (trust → mcp → network) prioritises the most actionable case but is not load-bearing for correctness (detectors are mutually exclusive in practice).

## `WritePrompt` vs `Write` is load-bearing

`session.WritePrompt(text)` (introduced in [tui-driver PR #43](https://github.com/pyrycode/tui-driver/pull/43)) wraps `text` with bracketed-paste markers (`\x1b[200~text\x1b[201~`) and writes a separate trailing `\r` outside the closer. Naive `session.Write(promptBytes + "\r")` does NOT commit a multi-line or >1 KB prompt: claude's TUI auto-paste-detects bytes arriving faster than human typing, groups them as `[Pasted text +N lines]` chips, swallows the trailing `\r` into the paste body, and waits indefinitely for an explicit Enter. The package always calls `WritePrompt`; the `string(cfg.PromptBytes)` conversion is the only `[]byte→string` boundary in the call graph. A future contributor refactoring to `Write` will silently break long prompts — pinned in the package doc-comment.

## Return contract

| Stage | Outcome | Return |
| --- | --- | --- |
| Required-field validation | Missing field | `errors.New("ptyrunner: <field> required")` |
| Spawn | ctx-canceled / deadline-exceeded | `nil` (operator-shutdown collapse) |
| Spawn | other error | `fmt.Errorf("ptyrunner: spawn: %w", err)` |
| Idle wait | ctx-canceled / deadline-exceeded | `nil` (operator-shutdown collapse) |
| Idle wait | non-ctx error (defensive) | `fmt.Errorf("ptyrunner: wait idle: %w", err)` |
| Modal check | trust / mcp / network | `ErrTrustModalDetected` / `ErrMcpFailureBanner` / `ErrNetworkFailure` |
| WritePrompt | error | `fmt.Errorf("ptyrunner: write prompt: %w", err)` |
| Close (deferred) | error | Warn-log; not surfaced |
| Clean cycle | — | `nil` |

The operator-shutdown collapse is delegated to a small `isCtxErr(ctx, err)` helper that checks both `errors.Is(err, context.Canceled|DeadlineExceeded)` and a bare `ctx.Err() != nil`. The double-check is defensive — `tuidriver.WaitUntil` returns `context.Cause(ctx)` which may be a wrapped cause; checking `ctx.Err()` directly handles that case.

Close errors are advisory: the body's return value already names the operator-visible outcome, and a non-nil `Close` after a clean cycle is best-effort cleanup. Same pattern `streamrunner` uses for stdin close failures.

## Logging discipline

Only `Warn`-level diagnostics emitted:

- `"ptyrunner: spawn failed"` with `err`
- `"ptyrunner: close failed"` with `err`
- `"ptyrunner: trust modal detected"` / `"ptyrunner: mcp failure banner detected"` / `"ptyrunner: network failure detected"`

Never logs `cfg.PromptBytes` content, any substring of `sess.Buffer.Snapshot()`, or any rendered TUI content. Writers (`Stderr` now, `Stdout` in #472) are opaque. The rule is pinned in the package doc-comment.

## Concurrency

`tui-driver` owns the two background goroutines (PTY reader, `cmd.Wait` observer). `Run` is straight-line foreground code — no goroutines, channels, or timers in this package. `tuidriver.WaitUntil` polls at 50ms via an internal `time.Ticker`. `sess.Close()` (deferred) is idempotent and handles SIGTERM → 3s grace → SIGKILL → PTY close → reader-goroutine join.

## Dependency direction

- Stdlib: `context`, `errors`, `fmt`, `io`, `log/slog`, `os`, `os/exec`.
- External: `github.com/pyrycode/tui-driver/pkg/tuidriver` (pinned `v0.0.0-20260519122208-b09fe70e60a7`; first `main` after PR #43 landed `WritePrompt`).
- **Must not** import `internal/supervisor`, `internal/agentrun/jsonl`, `internal/agentrun/streamjson`, or `internal/agentrun/budget`. Verify with:
  ```
  go list -deps ./internal/agentrun/ptyrunner/... | grep -E 'pyrycode/internal/(supervisor|agentrun/(jsonl|streamjson|budget))'
  ```
  Expected output: empty.

## Forward-compatibility fields

`MaxTurns` and `Stdout` are declared in `Config` but unused in this slice. Field comments name the consuming sibling (#472) explicitly and say "NOT consumed in this slice" so a casual reader is not misled. The precedent is to let #472 extend `Run` in-place to read these fields without breaking the public struct shape.

## Testing

Same-package `_test.go` with a `TestMain`-dispatched fake-claude (`GO_PTYRUNNER_HELPER=1`). The `TestMain` form is required because `Run` builds its own argv (`--session-id`, `--settings`, …); those flags are unknown to `go test`, which would `os.Exit(2)` at flag parsing if the helper used `streamrunner`'s sentinel-`if-env-not-set-return` pattern inside a test function. The dispatcher intercepts the env var BEFORE calling `m.Run()` and routes to `runHelper`, which terminates via `os.Exit` and never reaches the flag parser.

Helper modes keyed by `GO_PTYRUNNER_HELPER_MODE`:

| Mode | Bytes written | Detector that fires |
| --- | --- | --- |
| `idle` | `❯` + space | `IsIdle` only |
| `trust` | `Quicksafetycheck` + `❯` + space | `HasTrustModal` + `IsIdle` |
| `mcp_failure` | `1 MCP server failed ` + `❯` + space | `HasMcpFailureBanner` + `IsIdle` |
| `network_failure` | `FailedToOpenSocket ` + `❯` + space | `HasNetworkFailure` + `IsIdle` |
| `slow_spawn` | sleeps 5s before writing anything | parent's `WaitUntil` ctx-cancels first |

All modes drain stdin to `io.Discard` so the parent's `WritePrompt` bracketed-paste sequence doesn't backpressure into the PTY master write. All modes install a SIGTERM handler with a 30s fallback timeout so `Session.Close`'s SIGTERM step (not the 3s SIGKILL fallback) drives the helper's exit — keeps the seven scenario tests at ~3.6s total.

Test cases:

- `TestRun_HappyPath` (mode `idle`, expects `nil` return, elapsed < 5s)
- `TestRun_TrustModalDetected` / `TestRun_McpFailureDetected` / `TestRun_NetworkFailureDetected` (each asserts `errors.Is(err, Err*)` and substring on the operator-readable message — the trust case pins the `"#469's MarkWorkdirTrusted"` hint)
- `TestRun_CtxCancelDuringSpawn` (mode `slow_spawn`, cancels at 100ms, expects `nil`, elapsed < 8s)
- `TestBuildArgs` (six argv pairs + forbidden-flag loop)
- `TestRun_MissingRequiredFields` (nine subtests, one per required field)

Tests do **not** need real claude — the detectors are pure functions over `[]byte` snapshots; synthetic PTY bytes that contain the UTF-8 anchors satisfy them. Same approach `streamrunner`'s tests take.

CI: `tuidriver.Spawn` uses `pty.Start` which allocates a PTY pair from the kernel — no controlling terminal required. The same `creack/pty` v1.1.24 dep already used by `internal/supervisor` is the one tui-driver pulls in.

## Out of scope

- JSONL tail + stream-json re-emit + `result` trailer composition → #472 (consumes `Stdout`).
- Pyry-side max-turns budget enforcement → #472 (consumes `MaxTurns`).
- Trust pre-write + deny-default settings JSON file generation → #469 (produces `SettingsPath` and the remediation `ErrTrustModalDetected` points to).
- `cmd/pyry/agent_run.go` cutover from `streamrunner` to `ptyrunner` → #470.
- Streamrunner deletion → not in this migration phase; the two primitives stay side-by-side until the cutover lands.
- Operator-tunable timing knobs — the SIGTERM grace and `WaitUntil` poll interval are tui-driver defaults; no `Config` exposure.

## Related

- [streamrunner-package.md](streamrunner-package.md) — the no-PTY sibling whose stateless-`Run` + `Config` + sentinel-error package shape this primitive mirrors. The two coexist until #470 cuts the consumer over.
- [pyry-agent-run-command.md](pyry-agent-run-command.md) — the verb that consumes the spawn primitives. Currently wired to `streamrunner` (#391); #470 cuts it over to `ptyrunner`.
- [agentrun-package.md](agentrun-package.md) — the surrounding `internal/agentrun` package; `WriteSettings` / `MarkWorkdirTrusted` (used by #469 to produce `SettingsPath` and pre-write trust) live there.
- [`codebase/471.md`](../codebase/471.md) — build notes (file inventory, helper-process mode table, `TestMain` rationale, `GOPRIVATE` setup).
- Spec [`docs/specs/architecture/471-ptyrunner-skeleton.md`](../../specs/architecture/471-ptyrunner-skeleton.md) — architect spec.
- [tui-driver PR #43](https://github.com/pyrycode/tui-driver/pull/43) — `Session.WritePrompt` introduction; the bracketed-paste fix this primitive depends on for prompt commit.
