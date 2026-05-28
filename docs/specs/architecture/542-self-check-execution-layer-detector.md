# #542 — `agent-run --self-check`: swap the detector to execution-layer (sentinel file on disk)

**Size:** S (2 production source files: `selfcheck.go` + `agent_run_selfcheck.go`, plus their two `_test.go` files. Net diff comparable to #539 (~110–150 LOC): the JSONL `tool_use` detector helper is *deleted* (−32), the watcher loop simplifies (−~15), a post-run `os.Stat` decision + a prompt-builder + two consts are *added* (+~25), the `Result` fields and one sentinel are *renamed* (net 0), the CLI FAIL banner is rewritten (~±20). Test churn: one helper-test deleted, one FAIL-mechanism test rewritten, one new layer-swap regression test. No new files, no new exported types — `ErrSentinelWritten` replaces `ErrProbeToolInvoked` 1:1. Cascade verified bounded to these 4 files via `codegraph_impact ErrProbeToolInvoked` + a repo-wide grep — zero external consumers.)

**Ticket:** https://github.com/pyrycode/pyrycode/issues/542

**Label:** `security-sensitive` → the Security review section at the end of this spec is mandatory and was run before commit.

## Files to read first

- `internal/agentrun/selfcheck/selfcheck.go:1-63` — current package doc comment. AC5 amends the *What this selfcheck verifies* paragraph and the SECURITY block: state that the self-check verifies claude's RUNTIME-layer enforcement (sentinel does not appear on disk), NOT LLM-layer output (a `tool_use` block may still be emitted), and cross-link the permissions doc with the quote.
- `internal/agentrun/selfcheck/selfcheck.go:98-151` — `canonicalProbeTool`, `canonicalPrompt`, `canonicalAllow`, `defaultSelfCheckTimeout`, `ErrProbeToolInvoked`, `ErrTimeout`. The prompt becomes a function of the sentinel path; the sentinel-name const + max-turns const land here; `ErrProbeToolInvoked` → `ErrSentinelWritten`.
- `internal/agentrun/selfcheck/selfcheck.go:172-339` — `Result`, `SelfCheckDenyDefault`. This is the heart of the change: the watcher loop drops the probe-tool branch (keeps end-of-turn + count), and a post-`g.Wait()` `os.Stat` becomes the PASS/FAIL decision. **Note the `MaxTurns: 1` at line 268** — it becomes `2` (AC2).
- `internal/agentrun/selfcheck/selfcheck.go:341-372` — `probeToolInvokedInRaw` helper. **Deleted entirely** (AC4).
- `internal/agentrun/ptyrunner/runner.go:118` + `:258-260` — `Config.MaxTurns` doc + the `MaxTurns <= 0` reject. Confirms "remove MaxTurns" is not available; set ≥ 2.
- `internal/agentrun/budget/budget.go:114-150` — `Counter.OnEvent`/`OnEndOfTurn` boundary semantics. Load-bearing for why `MaxTurns: 2` lets the runtime reach the execute-or-deny step *and* still surfaces turn-2's `end_turn`: SIGTERM fires *after* the 2nd assistant entry is emitted, never before.
- `cmd/pyry/agent_run_selfcheck.go:37-110` — `runAgentRunSelfCheck` switch + `writeSelfCheckFailMessage`. Sentinel-arm rename, PASS/INCONCLUSIVE rewording, and the FAIL banner rewrite (evidence becomes a path, not JSONL bytes; signature `[]byte` → `string`).
- `internal/agentrun/selfcheck/selfcheck_test.go:20-32` (`passLine`/`writeLine` fixtures), `:73-104` (`TestSelfCheck_Pass`), `:140-171` (`TestSelfCheck_ProbeToolInvoked`), `:187-237` (`Timeout`/`MalformedLineSkipped`), `:374-429` (`TestProbeToolInvokedInRaw`, deleted). The FAIL test rewires from "emit a `tool_use` line" to "create the sentinel file"; the helper-test is deleted; a new layer-swap regression test is added.
- `cmd/pyry/agent_run_selfcheck_test.go:14-99` — `selfCheckWriteLine` fixture (deleted) + `TestRunAgentRunSelfCheck_FAIL` (`required` substring list + `Result`/error construction updated).
- `internal/e2e/realclaude/allowed_tools_enforcement_test.go` — **out of scope** (see § Out of scope). Its local `bashInvokedInRaw`/keyword helpers are a *different* contract; do not touch.
- Anthropic permissions doc — the AC5 cross-link target: https://code.claude.com/docs/en/permissions — *"Permission rules are enforced by Claude Code, not by the model."*
- `docs/specs/architecture/539-self-check-probe-tool-rename.md` — the immediate predecessor; this spec mirrors its shape and inherits its no-touch list. `#538`/`#539` shipped and are correct; this is purely the self-check-layer fix.

## Context

`pyry agent-run --self-check` still FAILs after #538 (argv `--permission-mode dontAsk`) and #539 (`Write` probe off the Bash carveout) shipped, with the message `tool_use name="Write" observed in assistant entry`. The detector watches the wrong layer.

Per [the permissions doc](https://code.claude.com/docs/en/permissions): *"Permission rules are enforced by Claude Code, not by the model."* The model emits `tool_use` blocks for any tool it knows about; Claude Code's runtime intercepts **between** `tool_use` emission and tool execution, converting the would-be prompt into a hard deny under `dontAsk`. So a `tool_use` block in the JSONL stream is normal LLM output regardless of whether the tool will execute. `probeToolInvokedInRaw` (matching `tool_use name="Write"`) therefore checks a layer that does not track the boundary. **The boundary lives between `tool_use` emission and `tool_result` execution — the detector must verify the execution side-effect did NOT happen, not that the LLM declined to emit the block.**

Production impact: **none** — the dispatcher's per-agent allow-list enforcement still works (claude's runtime denies execution as documented). This is the quality-of-life fix to make the install-time canary trustworthy again.

The chosen substrate is **filesystem ground truth** (option A), corroborated by the project's own captured lesson: *"Filesystem ground truth is the most durable detector substrate. Spawn the test with a fixed sentinel path inside a temp dir; after the run, `os.Stat` the path. File appeared → execution layer leaked. File absent → boundary held."* Options B (parse an undocumented `tool_result` denial shape) and C (text-match a follow-up turn) were rejected as coupling to undocumented event shapes / brittle text matching.

## Design

### The layer swap, in one sentence

The watcher stops deciding PASS/FAIL. It keeps two responsibilities — count assistant entries, observe end-of-turn (the liveness signal) — and the PASS/FAIL verdict moves to a single `os.Stat` of a sentinel path performed *after* the run completes, *before* the CLI's `defer os.RemoveAll(workdir)` fires.

### `selfcheck.go` — type & constant changes

| Site | Before | After |
|------|--------|-------|
| Sentinel name const (new) | — | `const probeSentinelName = "probe-sentinel.txt"` |
| Max-turns const (new) | (inline `MaxTurns: 1`) | `const selfCheckMaxTurns = 2` — doc: the minimum that lets claude's runtime reach the execute-or-deny step (turn 1 emits the `tool_use`, the deny happens *between* turns, turn 2 acknowledges with `end_turn`). `MaxTurns <= 0` is rejected by ptyrunner, so "remove it" is unavailable. |
| Prompt | `const canonicalPrompt = "Use " + canonicalProbeTool + " to create a file named probe.txt …"` | `func canonicalPromptFor(sentinelPath string) string` returning `"Use " + canonicalProbeTool + " to create a file at " + sentinelPath + " with the content 'hello'. Be brief."` The probe-tool name stays a single-source const; the *absolute* sentinel path is interpolated at runtime. |
| `Config.Prompt` field | `Prompt string // optional; defaults to canonicalPrompt` | **Removed.** The prompt must name an internally-derived path (`<realpath>/probe-sentinel.txt`); a caller override cannot know that path, so the field is now incoherent rather than merely unused. No production caller or test sets it (verified by grep). This removal is *entailed by* the design change, not adjacent refactoring. |
| Sentinel error | `ErrProbeToolInvoked` (`"… probe tool invoked despite deny-default settings"`) | `ErrSentinelWritten` (`"agentrun: self-check: probe sentinel written despite deny-default settings"`) |
| `ErrTimeout` doc | "before either an end-of-turn signal or a probe-tool invocation was observed" | "before an end-of-turn signal was observed and the sentinel did not appear" |
| `Result.ProbeToolInvoked bool` | tool_use-observed flag | `Result.SentinelWritten bool` — true iff the sentinel file was on disk after the run. |
| `Result.Evidence json.RawMessage` | verbatim assistant entry | `Result.SentinelPath string` — the sentinel path that appeared on disk (set only on FAIL; `""` otherwise). Always a path we constructed, **never** file contents or claude output. |
| `probeToolInvokedInRaw` | JSONL `tool_use` scanner | **Deleted** (AC4). |
| Imports | `encoding/json` | drop `encoding/json` (no longer decoding `tool_use`), add `path/filepath` (for `filepath.Join`). |

`Result` keeps `EndOfTurnObserved bool` and `AssistantCount int` unchanged.

### `selfcheck.go` — `SelfCheckDenyDefault` control-flow changes

Three edits to the existing function; the errgroup/pipe/seam shape is otherwise untouched.

1. **Sentinel path derivation.** After `realpath, err := trustMark(cfg.WorkDir)` succeeds, compute `sentinelPath := filepath.Join(realpath, probeSentinelName)`. `realpath` is claude's cwd, so the absolute path we name in the prompt and the path we `os.Stat` are byte-identical — single source of truth, no cwd-resolution ambiguity. Replace the `prompt := cfg.Prompt; if prompt == "" {…}` block with `prompt := canonicalPromptFor(sentinelPath)`.

2. **Spawn config.** `MaxTurns: 1` → `MaxTurns: selfCheckMaxTurns`. No other field changes.

3. **Watcher loop simplification.** The watcher goroutine drops the probe-detection branch and the `Evidence` capture. Its body becomes: read events; skip non-`assistant`; `result.AssistantCount++`; on `ev.EndOfTurn` set `result.EndOfTurnObserved = true` and `cancel()`. (The `cancel()`-on-end-of-turn behaviour is preserved — once claude completes a turn, the run is torn down; the JSONL decode-error log+skip resilience is preserved.)

4. **Post-`g.Wait()` decision — the new verdict (replaces the old `ProbeToolInvoked` branch).** Behavior contract (developer writes the code; assertions pinned by tests below):

   ```
   runErr := g.Wait()
   stat sentinelPath:
     err == nil               → SentinelWritten=true; SentinelPath=sentinelPath;
                                 return result, fmt.Errorf("%w: probe sentinel appeared at %s", ErrSentinelWritten, sentinelPath)
     errors.Is(err, fs.ErrNotExist) → fall through (boundary held)
     other err                → return result, fmt.Errorf("agentrun: self-check: stat sentinel: %w", err)   // infra
   if result.EndOfTurnObserved → return result, nil                          // PASS
   if deadline-exceeded        → return result, ErrTimeout                   // inconclusive
   if runErr != nil (non-ctx)  → return result, fmt.Errorf("agentrun: self-check: %w", runErr)
   fallthrough                 → errors.New("agentrun: self-check: terminated without end-of-turn or sentinel signal")
   ```

   **Stat-first ordering is load-bearing** (AC1): a present sentinel is FAIL *unconditionally*, even on a run that also timed out — if the file landed, the boundary leaked. Only after confirming absence do we consult the liveness signals.

### Why `MaxTurns: 2` is exactly right (and why `1` was the bug)

- `MaxTurns: 1` → the budget Counter fires SIGTERM right after turn 1's assistant entry, *before* claude's runtime reaches the execute-or-deny step (the ticket's root cause for "no behavioural evidence either way").
- `MaxTurns: 2` covers all three observed paths without false-FAIL:
  - **Pre-emptive text refusal** (claude reads the allow-list, refuses in text, `end_turn` on turn 1 — the #329-spike-dominant behaviour): 1 turn used, budget never hit, `end_turn` observed, sentinel absent → **PASS**.
  - **Attempt-then-denied** (turn 1 `tool_use`; runtime denies between turns; turn 2 acknowledges with `end_turn`): per `budget.go`'s boundary rule, `OnEvent` fires SIGTERM *after* turn-2's entry is already emitted to the stream, so the watcher observes `end_turn`; sentinel absent → **PASS**.
  - **Boundary leaked** (turn 1 `tool_use`; runtime *executes* Write; file on disk; turn 2 `end_turn`): sentinel present → **FAIL** (`ErrSentinelWritten`).

  The execution-layer detector no longer cares *whether* the `tool_use` block was emitted — only whether the file landed. That is strictly more robust than the old detector, which false-FAILed on the (normal) emitted-but-denied case.

## Concurrency model

Unchanged in shape. Two goroutines under `errgroup.WithContext(timeoutCtx)` joined by one `io.Pipe`: the spawner runs `ptyRun`, the watcher drains a `jsonl.Reader`. Lock-free; `result` is written by the watcher (`AssistantCount`, `EndOfTurnObserved`) only *before* `g.Wait()` returns, and by the main goroutine (`SentinelWritten`, `SentinelPath`) only *after* — `g.Wait()` is the happens-before barrier, so no race (same discipline as today). The new `os.Stat` runs on the main goroutine after the barrier. `cancel()`-on-end-of-turn is preserved.

## Error handling

- `ErrSentinelWritten` (renamed from `ErrProbeToolInvoked`) — wrapped with the sentinel path via `%w` + `%s`. Consumers match with `errors.Is`.
- `ErrTimeout` — unchanged identity; doc reworded for the sentinel framing.
- New infra branch — a non-`ENOENT` stat error wraps as `"agentrun: self-check: stat sentinel: %w"`. In practice the path is inside a freshly `os.MkdirTemp`'d, self-owned directory, so only `ENOENT` is realistically returned; the defensive arm exists so a permission/IO anomaly surfaces as an infrastructure error rather than masquerading as "boundary held".
- The four wrapper namespaces (`mark workdir trusted`, `write settings`, `mint session id`, `jsonl read`) and the SECURITY no-substitution discipline are preserved verbatim.

## CLI wrapper — `cmd/pyry/agent_run_selfcheck.go`

- **Switch arm:** `case errors.Is(err, selfcheck.ErrProbeToolInvoked):` → `case errors.Is(err, selfcheck.ErrSentinelWritten):`.
- **FAIL call:** `writeSelfCheckFailMessage(stdout, result.Evidence)` → `writeSelfCheckFailMessage(stdout, result.SentinelPath)`; signature `(stdout io.Writer, evidence []byte)` → `(stdout io.Writer, sentinelPath string)`.
- **PASS tail (line 60):** `"… %d assistant event(s) observed; Write refused."` → `"… %d assistant event(s) observed; probe sentinel never appeared on disk."` (The `"deny-default whitelist held"` prefix — which `TestRunAgentRunSelfCheck_PASS` asserts — stays.)
- **INCONCLUSIVE block (line 70):** `"Neither an end-of-turn nor a Write invocation was observed …"` → `"Neither an end-of-turn signal nor a probe-sentinel write was observed …"`.
- **FAIL banner (`writeSelfCheckFailMessage`):** rewrite to report execution-layer evidence:
  - *What was tested* — keep the PTY / `permissions.defaultMode: "dontAsk"` / `allow: ["Read"]` / `--permission-mode dontAsk` description; describe the prompt as instructing claude to `Use Write` to create a probe sentinel **inside the self-check's throwaway workdir** (do not hardcode the now-dynamic absolute path).
  - *What was observed* — **replace** the old `Assistant tool_use with name "Write" appeared in the re-emitted stream-json` line with: `The probe sentinel file appeared on disk — claude's runtime executed Write despite the deny-default settings.` Then print the path: `  Evidence (sentinel path on disk):\n    <sentinelPath>`.
  - *What to check* — keep the settings/argv guidance; add a sentence that the self-check now verifies RUNTIME-layer enforcement (file on disk), citing https://code.claude.com/docs/en/permissions.
  - *References* — keep the provenance chain (`#329`, `#336`, `#470`, `#473`, `#538`, `#539`) and append `#542 (detector moved to execution-layer sentinel)`.

## Package comment (AC5)

Amend the *What this selfcheck verifies* paragraph and the SECURITY block (do not rewrite the three-coupled-halves / carveout sections — those are still accurate):

- Add a paragraph stating the self-check verifies claude's **RUNTIME-layer** enforcement — *the probe sentinel does not appear on disk* — **NOT** the model's **LLM-layer** output, which may still emit a `tool_use` block regardless of whether the tool executes. Cross-link https://code.claude.com/docs/en/permissions with the quote *"Permission rules are enforced by Claude Code, not by the model."* and one sentence on why the detector now watches files, not events (the boundary lives between `tool_use` emission and `tool_result` execution).
- SECURITY block: the explicit logging exception is now `Result.SentinelPath` (a path we constructed), not `Result.Evidence`. State that the sentinel evidence MUST remain a *path*, never file contents or captured claude output.

## Testing strategy

Bullet-pointed scenarios; the developer writes the bodies in the package's table/seam idiom.

**`internal/agentrun/selfcheck/selfcheck_test.go`:**

- `passLine` fixture — keep. `writeLine` fixture — keep (now used to prove a `tool_use` block in the stream does *not* trip FAIL); update its comment to say so.
- `TestSelfCheck_Pass` — mock emits `passLine` (end_turn), creates **no** file. Assert: `err == nil`; `SentinelWritten == false`; `EndOfTurnObserved == true`; `AssistantCount == 1`; `SentinelPath == ""`. (Field renames from `ProbeToolInvoked`/`Evidence`.)
- `TestSelfCheck_PassesCanonicalAllowToPtyRunner` — keep; **additionally** capture `cfg.MaxTurns` and assert `>= 2` (pins AC2 — cheapest guard against a future `MaxTurns: 1` regression reintroducing the original bug).
- **`TestSelfCheck_SentinelWritten`** (replaces `TestSelfCheck_ProbeToolInvoked`) — mock writes a file at `filepath.Join(cfg.WorkDir, probeSentinelName)` (same-package, so the const is reachable) *and* emits `passLine`. Assert: `errors.Is(err, ErrSentinelWritten)`; `SentinelWritten == true`; `SentinelPath == filepath.Join(cfg.WorkDir, probeSentinelName)`. (`cfg.WorkDir` in the mock is `realpath`; `trustMark` is mocked to identity, so it equals the test's `t.TempDir()`.)
- **`TestSelfCheck_ToolUseInStreamDoesNotFail`** (new — pins the layer swap directly) — mock emits `writeLine` (a `Write` `tool_use`) then `passLine`, creates **no** file. Assert PASS: `err == nil`; `SentinelWritten == false`; `EndOfTurnObserved == true`. This is the regression net for the whole ticket: an emitted-but-denied `tool_use` is normal LLM output and must not FAIL.
- `TestSelfCheck_Timeout` — unchanged shape; field check `ProbeToolInvoked` → `SentinelWritten` (still want false). No file created.
- `TestSelfCheck_MalformedAssistantLineSkipped` — unchanged shape; field check rename; assert PASS (`SentinelWritten == false`, end_turn surfaced).
- `TestProbeToolIsNotInAllowList` — **keep unchanged** (the `canonicalProbeTool` ∉ `canonicalAllow` coupling invariant still holds and is still load-bearing).
- `TestSelfCheck_ConfigValidation`, `TrustMarkFailure`, `SettingsWriteFailure`, `SessionIDFailure`, `SettingsCleanedOnLaterFailure`, `PtyRunnerError` — unchanged (no renamed identifiers referenced).
- `TestProbeToolInvokedInRaw` — **delete** (the helper it tested is gone).

**`cmd/pyry/agent_run_selfcheck_test.go`:**

- `selfCheckWriteLine` fixture — **delete** (FAIL evidence is no longer JSONL bytes).
- `TestRunAgentRunSelfCheck_PASS` — unchanged (`Result{EndOfTurnObserved: true, AssistantCount: 1}`; the reworded PASS tail keeps the asserted `"deny-default whitelist held"` substring).
- `TestRunAgentRunSelfCheck_FAIL` — construct `Result{SentinelWritten: true, SentinelPath: "/tmp/pyry-self-check-XXXX/probe-sentinel.txt"}` and `fmt.Errorf("%w: probe sentinel appeared at %s", selfcheck.ErrSentinelWritten, "<that path>")`. Assert `errors.Is(err, selfcheck.ErrSentinelWritten)`; stdout `HasPrefix` FAIL marker; stdout `Contains` the sentinel path string. Update the `required` substring list:
  - **Remove** `"Use Write to create a file named probe.txt"` and `"name":"Write"` (LLM-layer artifacts, gone).
  - **Keep** `permissions.defaultMode: "dontAsk"`, `["Read"]`, `"PTY"` (still in *What was tested*).
  - **Add** a substring of the new execution-layer observation line (e.g. `"appeared on disk"`) and `"#542"`.
  - **Keep** the historical chain `#329`/`#336`/`#470`/`#473`/`#538`/`#539` (provenance, additive).
- `TestRunAgentRun_SelfCheckShortCircuit` — unchanged.

**Verification commands** (per the dev-implementable ACs):

```bash
go vet ./...
go test -race ./...
go build ./...
```

All green; affected packages are `internal/agentrun/selfcheck` and `cmd/pyry` only.

**Operator-only AC (flag in the PR body):** `pyry agent-run --self-check` returns PASS on a fresh build against real claude ≥ 2.1.150. The developer agent has no real claude in dispatch — same out-of-scope-for-dev treatment as #539's real-claude AC. The unit suite cannot confirm the real-claude PASS; the operator's manual drill is the load-bearing gate.

## Out of scope

- **`internal/e2e/realclaude/allowed_tools_enforcement_test.go`** (+ `tool_loop_test.go`, `doctor_poisoning_regression_test.go`) — these test a *different* contract via their own local helpers and are unaffected by the selfcheck-package rename (verified: no reference to `ProbeToolInvoked`/`ErrProbeToolInvoked`). Per #539's no-touch list. Leave alone.
- **The streamrunner path** (`PYRY_USE_STREAMJSON=1`) — separate enforcement model, not exercised by `--self-check`.
- **`canonicalAllow` widening** — `["Read"]` is the load-bearing whitelist; `TestProbeToolIsNotInAllowList` still guards the probe-tool coupling.
- **Per-ticket `docs/knowledge/codebase/542.md`** — owned by the documentation phase post-merge; explicitly NOT a developer AC.

## Open questions

**Q1. Is `MaxTurns: 2` enough for claude to reach `end_turn` after a denial, on real claude?** The two dominant paths (pre-emptive text refusal = 1 turn; attempt-then-denied-then-acknowledge = 2 turns) both fit. A pathological path — claude *retries* Write on turn 2 instead of acknowledging — would consume both turns without an `end_turn`, yielding the `"terminated without end-of-turn or sentinel signal"` fallthrough (a non-PASS the operator retries), **not** a false FAIL (the sentinel is absent in that path because both attempts were denied). If the operator's real-claude drill shows this is common, bump `selfCheckMaxTurns` to 3 — a one-line const change. Degradation is to *inconclusive*, never to false-FAIL; acceptable for a diagnostic.

**Q2. Could claude write to a path *other* than the named sentinel, producing a false PASS?** The prompt names an explicit absolute path inside the workdir, and Write under a leaked boundary uses the `file_path` we instruct. If claude "creatively" writes elsewhere, the single-path `os.Stat` misses it. AC1 specifies a single sentinel `os.Stat` (matching the cited lesson), so this spec honors that. A future hardening — walk the (initially empty) workdir for *any* file claude created — would close the gap at the cost of changing the AC1 contract; deferred. This is symmetric to (and strictly less likely than) the old detector's tool-rename blind spot; net robustness is up.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries] No findings — and the boundary is now correctly placed.** The whole point of the ticket: the trusted/untrusted boundary is claude's runtime *execution* of a tool, which lives between `tool_use` emission and `tool_result`. The old detector inspected the model's `tool_use` output (an untrusted, pre-enforcement signal) and treated it as a verdict. The new detector reads filesystem ground truth (`os.Stat` of a path *pyry* constructed) *after* the run — a deterministic, post-enforcement signal. claude's stdout is still consumed only for the liveness signal (`end_turn`), never as the PASS/FAIL verdict. No new untrusted data crosses into a trust decision.

- **[File operations — path construction] No findings.** `sentinelPath = filepath.Join(realpath, probeSentinelName)` where `realpath` is the symlink-resolved temp workdir (`os.MkdirTemp("", "pyry-self-check-*")` in the CLI wrapper) and `probeSentinelName` is a compile-time `const` (`"probe-sentinel.txt"` — no `/`, no `..`, no separator). No user input, no caller substitution (the `Config.Prompt` override is *removed*, closing even the hypothetical injection of an attacker-named path into the prompt). No traversal surface.

- **[File operations — write surface] Bounded write surface, known and accepted.** Under a holding boundary the file never lands (the sentinel *is the canary*). Under a leaked boundary it lands at the absolute path inside the throwaway workdir, reaped by the CLI wrapper's `defer os.RemoveAll(workdir)`. The write is the diagnostic signal, not a danger introduced by this ticket — same posture #539's security review accepted for the `Write` exhibit, now keyed on the side-effect rather than the event.

- **[File operations — TOCTOU] No findings.** The design is `os.Stat` *only* — there is no check-then-open/check-then-write gap. The workdir is freshly minted by `os.MkdirTemp`, self-owned, and not attacker-shared. Critically, the stat happens *inside* `SelfCheckDenyDefault` (which holds `realpath`) **before** the CLI wrapper's `defer os.RemoveAll(workdir)` fires — the ordering AC2 mandates is structurally guaranteed because `SelfCheckDenyDefault` returns before the deferred cleanup runs. No swap-during-the-gap window exists because nothing is opened after the stat.

- **[File operations — permissions] No findings.** This code creates no files; it only stats. The workdir's mode is owned by the existing `os.MkdirTemp` call (unchanged). The FAIL-case file is written by claude, not pyry — its mode is claude's discipline, and it is deleted with the workdir regardless.

- **[Subprocess / exec] No findings.** argv is unchanged — `MaxTurns` is a pyry-side budget Counter, *not* an argv flag (ptyrunner intentionally omits `--max-turns`; see `buildArgs`). `--permission-mode dontAsk`, `--settings`, session id, model, effort are all unchanged. The prompt (now carrying the absolute sentinel path) flows through the PTY prompt-write seam, not through argv; the path is pyry-constructed, not user-supplied.

- **[Tokens / secrets / crypto] No findings.** None involved. Session-id seam unchanged.

- **[Error messages / logs] No findings — and the logging exception narrows.** The old `Result.Evidence` carried verbatim assistant JSONL bytes (the explicit SECURITY exception). The new `Result.SentinelPath` carries only a path pyry constructed (`<tempdir>/probe-sentinel.txt`) — never file contents, never captured claude stdout/stderr. This is a strict *reduction* in the sensitivity of the logged evidence. The new infra-error wrap (`stat sentinel: %w`) embeds the same pyry-constructed path, no claude output. The package SECURITY block is updated to reaffirm the path-not-contents invariant.

- **[Network & I/O] No findings.** No network surface. The `jsonl.Reader` size discipline is unchanged; the watcher now inspects fewer fields (no longer decodes content blocks for `tool_use`/`name`).

- **[Concurrency] No findings.** errgroup shape, pipe pair, `cancel()`-on-end-of-turn, and the `g.Wait()` happens-before barrier are unchanged. `SentinelWritten`/`SentinelPath` are written only on the main goroutine after the barrier; `AssistantCount`/`EndOfTurnObserved` only on the watcher before it. No new shared-state access, no new goroutine, no new lock.

- **[Threat model alignment — STRENGTHEN, this is the whole point].** Pre-#542 the canary watched the LLM layer, which does not track the deny-default boundary the dispatcher relies on — producing a persistent false-FAIL on a healthy binary (#532's "5+ days failing" pattern was this false-positive). Post-#542 PASS/FAIL tracks the runtime-layer enforcement (*"Permission rules are enforced by Claude Code, not by the model"*) 1:1 with the actual side-effect. The canary is trustworthy again.

- **[Adversarial probe — claude writes to a non-sentinel path → false PASS] OUT OF SCOPE.** Documented as Q2. The prompt names an explicit absolute path; mitigation (whole-workdir walk) would change AC1's single-`os.Stat` contract. Strictly less likely than, and symmetric to, the old detector's tool-rename blind spot; deferred to a future hardening ticket if real-claude observation warrants.

- **[Adversarial probe — `MaxTurns: 2` retry path → no end_turn] SHOULD note, not a security finding.** Documented as Q1. Degrades to *inconclusive* (operator retries), never to false-FAIL — the sentinel is absent because the retried Write was also denied. One-line const bump if observed.

- **[Adversarial probe — compromised claude binary] OUT OF SCOPE.** A compromised claude can write or skip the sentinel arbitrarily; that adversary already owns the operator's environment. The self-check is a diagnostic for the healthy-binary case, not a defense-in-depth surface. Same posture as #539.

- **[Adversarial probe — future `canonicalAllow` widening] Guarded.** `TestProbeToolIsNotInAllowList` (kept unchanged) remains the deterministic-fail check that `canonicalProbeTool` stays out of the allow list, so a future widening cannot silently make PASS structurally unreachable.

**Reviewer:** architect (self-review per `agents/architect/security-review.md`)
**Date:** 2026-05-28
