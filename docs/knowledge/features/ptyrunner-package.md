# `internal/agentrun/ptyrunner` — interactive-TUI claude spawn primitive

PTY-driven sibling of [`internal/agentrun/streamrunner`](streamrunner-package.md): spawns `claude` as an interactive TUI under [`github.com/pyrycode/tui-driver`](https://github.com/pyrycode/tui-driver), waits for the TUI to reach idle, checks for trust-folder / MCP-failure / network-failure modals at the post-idle snapshot, submits one user prompt via `Session.WritePrompt` (bracketed-paste), and tears the session down through `Session.Close` (SIGTERM → grace → SIGKILL → PTY close).

Introduced #471 as a scaffolding-only slice; extended by #478 (JSONL tail + stream-json emit + end-of-turn classification), #479 (pyry-side `MaxTurns` budget Counter + PTY-heartbeat/spinner-freeze watchdog with shared ctx-cancel teardown), #547 (a prompt-commit recovery loop that re-delivers a corrupted/uncommitted bracketed paste), #553 (a `Pasted text` chip gate on that re-delivery so a committed-but-slow turn is never destructively re-pasted — **#227** protection; see [Prompt-commit recovery & the chip gate](#prompt-commit-recovery--the-chip-gate)), and #552 (an opt-in TUI session flight recorder behind `PYRY_RECORD_DIR` that mirrors every PTY byte to an asciinema-v2 `.cast` file — OFF by default, byte-identical to today when unset; see [Session flight recorder](#session-flight-recorder-pyry_record_dir)). **#470 wired it in as the [`pyry agent-run`](pyry-agent-run-command.md) default**, cutting the verb over from `streamrunner` to land on the explicitly subscription-eligible interactive surface ahead of Anthropic's 2026-06-15 billing-policy deadline. The pivot is driven by Anthropic's policy article enumerating "Interactive Claude Code in the terminal or IDE" as subscription-eligible while not naming the stream-json subprocess surface; `streamrunner` is retained as a `PYRY_USE_STREAMJSON=1` rollback knob for empirical post-deadline billing-classification comparison.

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
    AllowedTools []string     // required (#498), non-nil; empty slice OK; wire-shape mirror
                              // of the names in the deny-default settings file at SettingsPath;
                              // stamped into the leading system/init envelope's `tools` field
                              // via streamjson.Config.Tools; NOT placed on argv (the interactive
                              // TUI carries the allowlist inside SettingsPath)
    MaxTurns     int          // required (#479); pyry-side budget counter cap
    PromptBytes  []byte       // required; user-turn prompt, submitted via Session.WritePrompt
    Stdout       io.Writer    // required (#478); stream-json re-emit target (passed through to streamjson)
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
--permission-mode dontAsk
--append-system-prompt-file <SystemPrompt>
--model <Model>
--effort <Effort>
```

Intentionally **absent** from the argv:

- `--input-format` / `--output-format` / `--verbose` — stream-json mode markers; the interactive TUI rejects them.
- `--dangerously-skip-permissions` — replaced by the deny-default settings JSON from #469.
- `--max-turns` — the interactive TUI ignores it; #472 enforces the cap pyry-side via a budget counter.
- `--allowed-tools` — carried as JSON inside the settings file produced by #469. `Config.AllowedTools` (#498) is the human-readable mirror used for the leading `system/init` envelope's `tools` field, populated from the same `parsed.allowedTools` slice at the `runAgentRunPty` wiring site so the envelope and the runtime enforcement cannot drift.

`TestBuildArgs` pins the six pairs and includes a forbidden-flag loop so the absences are regression-protected.

## Spawn sequence

```
1. Validate required fields → return wrapped errors.New("ptyrunner: <field> required") on miss.
2. exec.CommandContext(ctx, cfg.ClaudeBin, buildArgs(cfg)...)
   cmd.Dir = cfg.WorkDir; cmd.Stderr = cfg.Stderr
   if cfg.Env != nil { cmd.Env = append(os.Environ(), cfg.Env...) }
3. tuidriver.EnsureClaudeEnv(cmd)               // sets TERM=xterm-256color
   (3a. if PYRY_RECORD_DIR is set: create the .cast file + recorder and arm mirror — see
        "Session flight recorder" below; otherwise mirror stays nil)
4. sess, err := tuidriver.Spawn(cmd, tuidriver.SpawnOpts{Mirror: mirror})  // mirror nil when off
   defer sess.Close()
5. tuidriver.WaitUntil(ctx, func() bool { return tuidriver.IsIdle(sess.Buffer.Snapshot()) })
6. snap := sess.Buffer.Snapshot()
   HasTrustModal(snap)      → ErrTrustModalDetected
   HasMcpFailureBanner(snap) → ErrMcpFailureBanner
   HasNetworkFailure(snap)   → ErrNetworkFailure
7. Deliver-and-confirm loop: sess.WritePrompt(string(cfg.PromptBytes)), then confirm
   the turn committed and recover a corrupted/uncommitted paste — re-delivering only
   when the "Pasted text" chip proves it is still uncommitted (see below).
8. Drain the session JSONL, enforce the max-turns budget + watchdog (#478 / #479), return nil.
```

The modal/banner detection runs **after** idle — the trust modal renders the `❯` glyph in its input field, so `IsIdle` returns true even inside it; the post-idle check is precisely what disambiguates. Detector order (trust → mcp → network) prioritises the most actionable case but is not load-bearing for correctness (detectors are mutually exclusive in practice).

## `WritePrompt` vs `Write` is load-bearing

`session.WritePrompt(text)` (introduced in [tui-driver PR #43](https://github.com/pyrycode/tui-driver/pull/43)) wraps `text` with bracketed-paste markers (`\x1b[200~text\x1b[201~`) and writes a separate trailing `\r` outside the closer. Naive `session.Write(promptBytes + "\r")` does NOT commit a multi-line or >1 KB prompt: claude's TUI auto-paste-detects bytes arriving faster than human typing, groups them as `[Pasted text +N lines]` chips, swallows the trailing `\r` into the paste body, and waits indefinitely for an explicit Enter. The package always calls `WritePrompt`; the `string(cfg.PromptBytes)` conversion is the only `[]byte→string` boundary in the call graph. A future contributor refactoring to `Write` will silently break long prompts — pinned in the package doc-comment.

## Prompt-commit recovery & the chip gate

Under MCP-init churn the bracketed-paste byte stream can interleave with claude's TUI redraws, corrupting the pasted text and absorbing the trailing `\r` commit — claude is left idle with garbled, uncommitted input ("Mode B", root-caused #547). After `WritePrompt`, `Run` confirms the turn committed and reactively recovers, inside a bounded retry loop (`maxPromptAttempts = 3`):

- **`promptDidCommit(ctx, sess, jsonlPath, timeout)`** polls (`promptCommitPoll`, 150ms) for either signal that claude started a turn — `tuidriver.IsThinking(snapshot)` OR the per-session JSONL appearing (`os.Stat`). A healthy run trips this within ~1s, far inside `PromptCommitTimeout` (default `defaultPromptCommitTimeout` = 3s; override via `Config.PromptCommitTimeout`), so it never retries.
- On timeout, the **chip gate** (#553) decides whether to re-deliver, acting only on positive evidence the paste is still uncommitted: `hasPastedChip(snap) = bytes.Contains(tuidriver.StripANSI(snap), []byte("Pasted text"))` (a pure detector mirroring `tuidriver.IsThinking`'s shape). **Chip present** ⟺ genuine wedge → `ClearInputLine` + re-deliver. **Chip absent** ⟺ committed-but-slow (commit signals lagging a slow MCP cold-start) → set `committed = true` and stop retrying.

Re-delivering unconditionally on timeout — the pre-#553 behaviour PR #547 shipped — re-pastes an in-flight turn whose signals merely lag, the destructive **#227** path. The gate restores the discriminator: a closed N=60 probe (claude 2.1.158) established chip-present ⟺ uncommitted with zero counterexamples. **Both** break paths fall through to the downstream JSONL wait, so a committed-but-slow turn still completes once its lagging JSONL lands — the gate decides whether to *re-paste*, never whether to *wait*.

The two decisions log distinct **verbatim** WARN lines (test observables — do not paraphrase):

| Case | WARN line |
| --- | --- |
| genuine wedge (re-deliver) | `ptyrunner: prompt uncommitted (pasted-text chip present); re-delivering` |
| committed-but-slow (no re-deliver, #227 fix) | `ptyrunner: commit signals slow but input box empty (no pasted-text chip) — assuming committed-but-slow, not re-delivering` |

Only the wedge line carries the `(pasted-text chip present); re-delivering` marker; the committed-but-slow line ends `…not re-delivering`. Both contain "re-delivering", so the tests key on the marker. The `committed = true` on the no-chip path also suppresses the misleading `ptyrunner: prompt uncommitted after retries; proceeding (may wedge)` backstop warn. See [`codebase/553.md`](../codebase/553.md).

## Session flight recorder (`PYRY_RECORD_DIR`)

Opt-in flight recorder (#552). The **control channel** — the rendered PTY screen — is otherwise never persisted: it lives only in tui-driver's ~4 KB rolling buffer and is gone the moment claude exits, yet nearly every ptyrunner failure (hang, wrong state detection, modal mis-handling, paste corruption) lives there. (The content channel — claude's per-session JSONL — is already persisted by claude.) When the operator sets `PYRY_RECORD_DIR`, `Run` mirrors **every PTY byte** of the session to an asciinema-v2 `.cast` file so a bad session can be replayed (`asciinema play`) or parsed/diffed offline.

This is the pyrycode-side consumer wiring of `tuidriver.NewCastRecorder(w io.Writer, cols, rows int) *CastRecorder` (published by tui-driver#125) into the pre-existing `SpawnOpts.Mirror io.Writer` seam — tui-driver's PTY reader goroutine copies every chunk into `Mirror`, and production passed `Mirror: nil` before #552. **The recorder owns no lifecycle; the consumer owns and closes the underlying `*os.File`.**

- **OFF by default, byte-identical to today.** The env var unifies switch + location: set → ON and write there; unset/empty → OFF. There is **no baked-in default directory** (a fallback would risk ambient recording of sensitive content). When unset, `mirror` stays a pure nil `io.Writer` and `SpawnOpts{Mirror: nil}` is byte-for-byte the old `SpawnOpts{}` — no typed-nil trap. Suggested operator location: `~/.local/share/pyry-recordings/` (sibling of `~/.local/share/pyry-artifacts/`, outside Obsidian Sync / Time Machine reach).
- **Wiring site + named return.** The recorder block sits after `tuidriver.EnsureClaudeEnv(cmd)` and **before** `tuidriver.Spawn` (Mirror is read at spawn time). `Run`'s signature is `func Run(ctx, cfg) (err error)` (named return) so the finalize defer reads the run's **final** result at fire time, not nil at registration.
- **cols/rows = `tuidriver.DefaultPtyCols` / `DefaultPtyRows` (120 × 40).** `StartPTY` sets every PTY tui-driver spawns to exactly those and ptyrunner spawns with empty opts, so the cast's recorded dimensions match the bytes' origin — exported constants, no magic numbers.
- **Filenames.** Temp at create: `<UTC-stamp>-<sessionID>.cast` (stamp = `time.Now().UTC().Format("20060102T150405Z")`, sortable), opened `O_CREATE|O_EXCL|O_WRONLY, 0o600`. Session-identifiable *from creation*, so a crash / SIGKILL before the rename still leaves a session-tagged, replayable file. On clean close the outcome is inserted **inside the stem** — `<stem>-ok.cast` (nil run error) / `<stem>-err.cast` (non-nil) — so the file stays `*.cast` (required by both prune and replay). The rename *only adds* the suffix; it never changes the extension.
- **Close + rename is the strict LIFO tail.** `defer func() { finalizeRecording(f, tmpPath, err, logger) }()` is registered before `defer sess.Close()`, so it runs **last** in the cleanup chain (`cancel() → wg.Wait() → counter.Stop() → emitter.Close() → sess.Close() → finalizeRecording()`). This is load-bearing: `sess.Close()` closes the PTY then blocks on `<-readerDone`, joining the sole `Mirror` writer (the reader goroutine), so the close + rename can never race a mirror write. Race-clean under `go test -race`. The wrapping closure (not a bare `defer`) is mandatory so it reads the named `err` at fire time.
- **7-day prune, scoped to the dir.** On startup (only when the var is set) `pruneOldRecordings` globs `filepath.Join(dir, "*.cast")` — `*` never crosses a separator, so no recursion, no escape, only top-level `*.cast` hits — and `os.Remove`s those with `mtime` older than `recordingMaxAge = 7 * 24h`. Never touches a subdirectory, a non-`.cast` file, or a path outside the dir. The fresh run's own file (mtime `now`) survives.
- **Fail-fast on setup, best-effort on housekeeping.** `MkdirAll(0o700)` / `createRecordingFile` / `WriteHeader` fail-fast with a wrapped `ptyrunner: <op>: %w` error before `Spawn` (the operator opted in; silently continuing would betray that, and nothing is lost — no session has started). Prune and rename are best-effort: errors are Warn-logged (**path + err only, never recording content**) and never abort the run.

**SECURITY (`security-sensitive`):** an enabled recording captures the prompt + claude output + **ALL** tool output (file contents, possibly secrets/tokens). Mitigated by: OFF-by-default, mode `0600` (owner-only), `*.cast` gitignored, a flag-site warning, and guidance to a non-synced/non-backed-up location. The artifact is unencrypted by design (it must stay `asciinema play`-able); the threat model is a local opt-in debug aid on the single-user operator's own machine. `cfg.SessionID` becomes a filename component but is already trusted as a path component (`SessionJSONLPath`) and as `--session-id` argv (a `crypto/rand` UUIDv4), so no trust boundary widens. See the spec's `## Security review` (verdict PASS) and [`codebase/552.md`](../codebase/552.md).

## Return contract

| Stage | Outcome | Return |
| --- | --- | --- |
| Required-field validation | Missing field | `errors.New("ptyrunner: <field> required")` |
| Recording setup (only when `PYRY_RECORD_DIR` set) | dir / file / header failure | `fmt.Errorf("ptyrunner: recording dir\|create recording\|recording header: %w", err)` — fail-fast before Spawn (see [Session flight recorder](#session-flight-recorder-pyry_record_dir)) |
| Spawn | ctx-canceled / deadline-exceeded | `nil` (operator-shutdown collapse) |
| Spawn | other error | `fmt.Errorf("ptyrunner: spawn: %w", err)` |
| Idle wait | ctx-canceled / deadline-exceeded | `nil` (operator-shutdown collapse) |
| Idle wait | non-ctx error (defensive) | `fmt.Errorf("ptyrunner: wait idle: %w", err)` |
| Modal check | trust / mcp / network | `ErrTrustModalDetected` / `ErrMcpFailureBanner` / `ErrNetworkFailure` |
| WritePrompt | error | `fmt.Errorf("ptyrunner: write prompt: %w", err)` |
| Close (deferred) | error | Warn-log on genuine failure; Debug-log on benign teardown shape ([`agentrun.ExitErrIsBenign`](agentrun-package.md), #527); not surfaced |
| Clean cycle | — | `nil` |

The operator-shutdown collapse is delegated to a small `isCtxErr(ctx, err)` helper that checks both `errors.Is(err, context.Canceled|DeadlineExceeded)` and a bare `ctx.Err() != nil`. The double-check is defensive — `tuidriver.WaitUntil` returns `context.Cause(ctx)` which may be a wrapped cause; checking `ctx.Err()` directly handles that case.

Close errors are advisory: the body's return value already names the operator-visible outcome, and a non-nil `Close` after a clean cycle is best-effort cleanup. The deferred `sess.Close()` filter (#527) is required because `tuidriver.Session.Close()` bubbles claude's exit code through — when budget sends SIGTERM at `max_turns`, claude's signal handler exits 143, and `Close` returns `*exec.ExitError{ExitCode=143}`; without the predicate, every routine `max_turns` exhaustion would emit one WARN. Same pattern `streamrunner` uses for stdin close failures.

## Logging discipline

`Warn`-level diagnostics (genuine failures only — teardown-shape errors get downgraded by the predicate at the close site):

- `"ptyrunner: spawn failed"` with `err`
- `"ptyrunner: close failed"` with `err` — only when [`agentrun.ExitErrIsBenign(err)`](agentrun-package.md) is false. Benign shape logs as `"ptyrunner: close: child already exited"` at Debug.
- `"ptyrunner: trust modal detected"` / `"ptyrunner: mcp failure banner detected"` / `"ptyrunner: network failure detected"`

Never logs `cfg.PromptBytes` content, any substring of `sess.Buffer.Snapshot()`, or any rendered TUI content. Writers (`Stderr` now, `Stdout` in #472) are opaque. The rule is pinned in the package doc-comment.

## Concurrency

`tui-driver` owns the two background goroutines (PTY reader, `cmd.Wait` observer). `Run` is straight-line foreground code — no goroutines, channels, or timers in this package. `tuidriver.WaitUntil` polls at 50ms via an internal `time.Ticker`. `sess.Close()` (deferred) is idempotent and handles SIGTERM → 3s grace → SIGKILL → PTY close → reader-goroutine join. The #552 flight recorder adds **no** new goroutine either — it is driven entirely by tui-driver's existing PTY reader goroutine (the sole `Mirror` writer); `finalizeRecording`'s file close + rename is ordered strictly after that goroutine exits because its defer is the LIFO tail, running after `sess.Close()`'s `<-readerDone` join (happens-before).

## Dependency direction

- Stdlib: `bytes` (#553), `context`, `errors`, `fmt`, `io`, `log/slog`, `os`, `os/exec`, `path/filepath` (#552), `strings` (#552).
- External: `github.com/pyrycode/tui-driver/pkg/tuidriver` (pinned `v0.0.0-20260531143940-6bec180ad34c`, bumped in #552 to publish `NewCastRecorder` + `DefaultPtyCols`/`DefaultPtyRows`; was `v0.0.0-20260519122208-b09fe70e60a7`, the first `main` after PR #43 landed `WritePrompt`).
- **Must not** import `internal/supervisor`, `internal/agentrun/jsonl`, `internal/agentrun/streamjson`, or `internal/agentrun/budget`. Verify with:
  ```
  go list -deps ./internal/agentrun/ptyrunner/... | grep -E 'pyrycode/internal/(supervisor|agentrun/(jsonl|streamjson|budget))'
  ```
  Expected output: empty.

## Required-field validation

Each `Config` field marked required produces a wrapped one-line error of the shape `errors.New("ptyrunner: <field> required")` when missing. The validation block runs first inside `Run` before any spawn. `AllowedTools` (#498) is `nil`-rejected and `[]string{}`-accepted — an empty allowlist is a valid runtime configuration (the deny-default settings file still pins `defaultMode:"dontAsk"`). `TestRun_MissingRequiredFields` covers each required field with its expected error substring.

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
| `commit_wedge_chip` | `[Pasted text +3 lines] ❯` + space; JSONL body held back `commitModeJSONLDelay` (500ms) | `IsIdle` (chip carries no `✻`) + `hasPastedChip` → chip-gate re-delivers |
| `commit_slow_nochip` | `❯` + space; JSONL body held back `commitModeJSONLDelay` | `IsIdle`, no chip → chip-gate treats as committed-but-slow (#227 path) |

All modes drain stdin to `io.Discard` so the parent's `WritePrompt` bracketed-paste sequence doesn't backpressure into the PTY master write. All modes install a SIGTERM handler with a 30s fallback timeout so `Session.Close`'s SIGTERM step (not the 3s SIGKILL fallback) drives the helper's exit — keeps the seven scenario tests at ~3.6s total.

Test cases:

- `TestRun_HappyPath` (mode `idle`, expects `nil` return, elapsed < 5s)
- `TestRun_TrustModalDetected` / `TestRun_McpFailureDetected` / `TestRun_NetworkFailureDetected` (each asserts `errors.Is(err, Err*)` and substring on the operator-readable message — the trust case pins the `"#469's MarkWorkdirTrusted"` hint)
- `TestRun_CtxCancelDuringSpawn` (mode `slow_spawn`, cancels at 100ms, expects `nil`, elapsed < 8s)
- `TestBuildArgs` (six argv pairs + forbidden-flag loop)
- `TestRun_MissingRequiredFields` (nine subtests, one per required field)
- `TestHasPastedChip` (pure detector, 6 cases incl. an ANSI-escaped chip that only matches after `StripANSI`, plus a `"Paste text"` near-miss → false)
- `TestRun_CommitWedge_ChipPresent_ReDelivers` / `TestRun_CommitSlow_NoChip_DoesNotReDeliver` (logger-asserting via the captured `cfg.Logger` + `loggerSyncWriter`; the latter pins the #227 protection by asserting the wedge marker is **absent**. RED→GREEN was demonstrated by toggling the gate to `if false && …`)

The chip-gate integration tests observe the loop decision through the captured `cfg.Logger` (slog `TextHandler` @ `LevelWarn`), **not** a fake-claude stderr sentinel: under the PTY the helper's `os.Stderr` is not wired into the parent's `cfg.Stderr`, so a stderr sentinel observes nothing. The fixtures' `commitModeJSONLDelay` (500ms) only needs to exceed the test's `PromptCommitTimeout` (200ms) so the first commit window elapses with no JSONL and the gate is exercised; control always falls through to `WaitForSessionJSONL`, which keeps the suite `-race -count=5` stable.

Tests do **not** need real claude — the detectors are pure functions over `[]byte` snapshots; synthetic PTY bytes that contain the UTF-8 anchors satisfy them. Same approach `streamrunner`'s tests take.

CI: `tuidriver.Spawn` uses `pty.Start` which allocates a PTY pair from the kernel — no controlling terminal required. The same `creack/pty` v1.1.24 dep already used by `internal/supervisor` is the one tui-driver pulls in.

## Out of scope

- JSONL tail + stream-json re-emit + `result` trailer composition → landed in #478.
- Leading `system/init` envelope synthesis (wire-shape parity with streamrunner) → landed in #498 inside `streamjson.New`; ptyrunner threads `WorkDir` / `AllowedTools` / `Model` / `SessionID` into the streamjson Config so the envelope's six required fields are populated from ptyrunner's existing inputs.
- Pyry-side max-turns budget enforcement + watchdog → landed in #479.
- Trust pre-write + deny-default settings JSON file generation → landed as separate subpackages [`trust`](agentrun-trust-subpackage.md) (#475) and [`settings`](agentrun-settings-subpackage.md) (#476); together they produce `SettingsPath` and the remediation `ErrTrustModalDetected` points to.
- `cmd/pyry/agent_run.go` cutover from `streamrunner` to `ptyrunner` → landed in #470.
- Streamrunner deletion → not planned. Operator decision 2026-05-19: streamrunner stays as a sibling indefinitely for billing-classification comparison, selected via `PYRY_USE_STREAMJSON=1`.
- Operator-tunable timing knobs — the SIGTERM grace and `WaitUntil` poll interval are tui-driver defaults; no `Config` exposure.

## Related

- [streamrunner-package.md](streamrunner-package.md) — the no-PTY sibling whose stateless-`Run` + `Config` + sentinel-error package shape this primitive mirrors. The two coexist post-#470 under the `PYRY_USE_STREAMJSON` rollback knob.
- [pyry-agent-run-command.md](pyry-agent-run-command.md) — the verb that consumes the spawn primitives. Post-#470 wired to `ptyrunner` by default; falls back to `streamrunner` when `PYRY_USE_STREAMJSON=1`.
- [agentrun-package.md](agentrun-package.md) — the surrounding `internal/agentrun` package; `WriteSettings` / `MarkWorkdirTrusted` (used by #469 to produce `SettingsPath` and pre-write trust) live there.
- [`codebase/471.md`](../codebase/471.md) — build notes (file inventory, helper-process mode table, `TestMain` rationale, `GOPRIVATE` setup).
- [`codebase/553.md`](../codebase/553.md) — chip-gate build notes (decision table, evidence base, the stderr-sentinel-vs-logger testing lesson). Spec [`docs/specs/architecture/553-chip-gated-repaste.md`](../../specs/architecture/553-chip-gated-repaste.md). PR #547 introduced the recovery loop; issue #227 is the destructive re-paste regression the gate prevents.
- [`codebase/552.md`](../codebase/552.md) — flight-recorder build notes (env-read-in-`Run` rationale, the named-return + wrapping-closure-defer gotcha, fail-fast/best-effort split, the unresolved package-doc carve-out follow-up). Spec [`docs/specs/architecture/552-ptyrunner-session-flight-recorder.md`](../../specs/architecture/552-ptyrunner-session-flight-recorder.md). Consumes tui-driver#125's `NewCastRecorder` via the `SpawnOpts.Mirror` seam.
- Spec [`docs/specs/architecture/471-ptyrunner-skeleton.md`](../../specs/architecture/471-ptyrunner-skeleton.md) — architect spec.
- [tui-driver PR #43](https://github.com/pyrycode/tui-driver/pull/43) — `Session.WritePrompt` introduction; the bracketed-paste fix this primitive depends on for prompt commit.
