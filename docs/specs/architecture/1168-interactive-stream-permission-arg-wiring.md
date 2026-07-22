# Spec: Wire the approval-tool flags onto the interactive stream-json spawn (#1168)

**Size:** S (2 production files, ~40 production LOC, 0 new exported types). Security-sensitive.

## Files to read first

- `cmd/pyry/mcp_config.go:35-45` — `permissionArgs(yolo bool, mcpConfigPath string) []string`: the exact non-YOLO arg set to inject. Runner-agnostic by design (#1106); this ticket is its first interactive-path consumer.
- `cmd/pyry/mcp_config.go:105-127` — `writeMCPApproveConfig(pyryBin, socketPath string) (string, error)`: writes the per-daemon `--mcp-config` tmp file (mode 0600), fail-closed on empty inputs. Caller owns removal.
- `cmd/pyry/streamsup_runner.go:69-101` — `newStreamRunnerFactory` + its closure: the sole stream-path-specific construction site, where the injection lands. Note `mapStreamsupConfig` is deliberately **pure** (comment at :103-114); keep it that way — inject in the closure, not the mapper.
- `cmd/pyry/streamsup_runner.go:115-160` — `mapStreamsupConfig` / `stripSessionIDFlags`: `Config.Args = stripSessionIDFlags(cfg.ClaudeArgs)`; the strip drops only `--session-id`/`--resume`, so `--dangerously-skip-permissions` survives it (the yolo signal is still visible on `scfg.Args`).
- `cmd/pyry/main.go:667-677` — `selectInteractiveRunner`: gains one `mcpApprovePath string` param, threaded to `newStreamRunnerFactory`. Sole prod caller at `main.go:787`.
- `cmd/pyry/main.go:681-812` — `runSupervisor`: `socketPath` resolved at :698, pool built at :791; this is where the config is written once and removed at shutdown.
- `cmd/pyry/update.go:86-93` — `resolveExecutable() string`: existing self-path helper (`os.Executable()` with `os.Args[0]` fallback). Use it for `pyryBin`.
- `internal/sessions/session.go:93-107` — `claudeSettingsArgs`: confirms **per-session YOLO also emits `--dangerously-skip-permissions`** (not just the operator's bootstrap pass-through). This is why the yolo probe reads the flag off the args, per-spawn.
- `internal/streamsup/runner.go:593-601` — `buildArgs`: prepends the stream-format flags, appends `Config.Args` verbatim as `base`, then the id flag. Injected approval flags land inside `base` — a valid position (claude accepts flags order-independently).
- `internal/e2e/realclaude/interactive_stream_modal_resolution_test.go` (whole file) — the #1154 gate this fix greens (AC #3). Uses `spawnPermissionDaemon` (no `--dangerously-skip-permissions` → non-yolo).
- `internal/e2e/realclaude/interactive_stream_liveness_test.go:118` — #1153 uses `spawnBootstrapDaemon` (**YOLO**), so AC #4 is satisfied by the yolo pass-through: the factory injects nothing on a yolo spawn, leaving #1153's argv byte-identical.
- `docs/knowledge/codebase/1106.md` — the upstream ticket. Its "Out of scope, deferred" list (line 37) names the exact four concerns this ticket closes: yolo source, socket value, config-file removal lifecycle, and the streamsup live-wiring.

## Context

This is the final wire in the Streamrunner-Interactive permission bridge (#1079 → #1103 `permbridge` registry → #1104 `mcp.approve` control-socket verb → #1105 `pyry mcp-approve` stdio server → #1106 arg-injection primitives). #1106 wired the non-YOLO `--permission-prompt-tool`/`--mcp-config` flags onto the **`pyry agent-run`** stream spawn and explicitly deferred the interactive-daemon live-wiring downstream. That downstream wire was never laid.

The result, found live on 2026-07-22: the interactive stream-json runner spawns claude with **no** approval-tool flags and no `--dangerously-skip-permissions` either. On a permission-bearing turn claude has no daemon approval tool to call, `permbridge` records no pending request, and no `modal_shown` fans to the phone. `TestInteractiveStreamModalResolution` (#1154) fails `no modal_shown broadcast within 2m0s`; the whole daemon-side chain (`streamApprovalBridge` in `cmd/pyry/modal_resolve_v2.go`) is wired but starved. This blocks #1083 (T9 cutover).

The daemon-side registry, socket verb, and forwarder all already exist and are tested. This ticket only threads the two #1106 primitives onto the one spawn path that was skipped.

## Design

### Injection seam: the stream runner factory

`newStreamRunnerFactory`'s returned closure is the **only** stream-path-specific spawn-construction site. The PTY path resolves `selectInteractiveRunner` to a `nil` RunnerFactory (`main.go:669-670`) and never enters this code, so **AC #5 (PTY argv byte-equivalent) holds structurally** — no gate, no branch, the PTY path simply cannot reach the injection.

The factory covers **both** the bootstrap runner and per-conversation runners (`Pool.newRunner` is threaded through both construction sites — `pool.go:361`). This is deliberate and is the correct security posture: a per-conversation stream session must not silently bypass approval. The per-spawn yolo probe (below) handles each spawn's own yolo intent.

### Yolo signal: read `--dangerously-skip-permissions` off the args, per-spawn

Yolo is expressed to claude as exactly one flag, `--dangerously-skip-permissions`, and both yolo entry points funnel through it:
- bootstrap: the operator's pass-through claude args (`main.go:805`),
- per-session: `claudeSettingsArgs` appends it when `YOLO==true` (`session.go:106-107`).

So the presence of `--dangerously-skip-permissions` in the spawn's args is the single deterministic yolo signal, robust to both entry points, evaluated **per-spawn** inside the factory closure. This is the "source of the yolo boolean at a live spawn" that #1106 deferred.

New pure helper in `cmd/pyry/streamsup_runner.go`:

```
func withApprovalArgs(args []string, mcpApprovePath string) []string
```

Behavior contract:
- If `slices.Contains(args, "--dangerously-skip-permissions")` → return `args` **unchanged** (yolo: the flag is already present; injecting nothing keeps AC #2 "unchanged from today" and avoids a duplicate flag). Do **not** call `permissionArgs(true, …)` here — that would duplicate the flag already in `base`.
- Else → return `append(slices.Clone(args), permissionArgs(false, mcpApprovePath)...)` (non-yolo: the enforcement flag set). `slices.Clone` keeps the helper free of aliasing surprises even though `scfg.Args` is freshly owned.

Invariant asserted by test: `TestWithApprovalArgs` (see Testing).

### Config-file lifecycle: write once at startup, remove at shutdown

`writeMCPApproveConfig` creates a tmp file whose content is `(pyryBin, socketPath)` — both **daemon-global constants**, identical across every session and every respawn. So it is written **once**, in `runSupervisor`, gated on `cfg.InteractiveRunner == "stream-json"`, and removed when `runSupervisor` returns (daemon shutdown). This resolves the "per-spawn config-file removal lifecycle" #1106 deferred: it is per-*daemon*, not per-spawn.

Sketch (insert in `runSupervisor` after `socketPath` is resolved, before/around `selectInteractiveRunner` at `main.go:787`):

```
var mcpApprovePath string
if cfg.InteractiveRunner == "stream-json" {
    mcpApprovePath, err = writeMCPApproveConfig(resolveExecutable(), socketPath)
    if err != nil {
        return fmt.Errorf("write mcp-approve config: %w", err)   // fail-closed
    }
    defer func() { _ = os.Remove(mcpApprovePath) }()
}
runnerFactory, streamSink, err := selectInteractiveRunner(cfg, logger, mcpApprovePath)
```

- **Fail-closed startup.** If the write fails, the daemon does not start. It must never proceed into a state where a non-yolo session would receive `--mcp-config ""`. (`writeMCPApproveConfig`/`renderMCPApproveConfig` already reject empty `socketPath`/`pyryBin`; both are non-empty in practice — `resolveSocketPath` and `resolveExecutable` always return a value — so this only trips on a genuine filesystem error.)
- **Unconditional in stream mode** (not gated on bootstrap-yolo). A yolo daemon can still mint non-yolo per-conversation sessions (per-session YOLO defaults off / fail-safe), so the config must exist. The tmp file is ~200 bytes, mode 0600, unreferenced-but-harmless when every session is yolo.

### Threading

`mcpApprovePath` flows: `runSupervisor` → `selectInteractiveRunner(cfg, logger, mcpApprovePath)` → `newStreamRunnerFactory(sink, mcpApprovePath)` → the closure, which calls `withApprovalArgs` after `mapStreamsupConfig`:

```
scfg := mapStreamsupConfig(cfg)
scfg.Args = withApprovalArgs(scfg.Args, mcpApprovePath)
scfg.Stdout = streamsup.NewParser(sink.sinkFor(cfg.SessionID), cfg.Logger)
```

`mapStreamsupConfig` stays pure (its `Stdout == nil` assertion and PTY-field-exclusion contract are untouched — the injection is a runtime concern one layer up, same rationale as the Parser install). On the `""`/`"pty"` path `mcpApprovePath` is `""` and unused — the factory is never built.

### Signature changes (fan-out: 8 call sites, all mechanical single-arg additions)

- `selectInteractiveRunner(cfg, logger)` → `(cfg, logger, mcpApprovePath string)`. Callers: `main.go:787` (prod) + 4 in `cmd/pyry/interactive_runner_test.go` (lines 24, 38, 52, 79) — pass `""` in tests.
- `newStreamRunnerFactory(sink)` → `(sink, mcpApprovePath string)`. Callers: `main.go:673` (prod) + 2 in `cmd/pyry/streamsup_runner_test.go` (lines 195, 240) — pass `""` (or a fixed test path where asserting injection).

No new exported types. No type cascade. Under the 10-site red line.

## Concurrency model

None introduced. The write is a one-shot synchronous call on the startup goroutine; `withApprovalArgs` is a pure function called on whatever goroutine the pool uses to build a runner. The tmp file is read-only after the single write and shared by reference (its path string) across all runners — no writer races it. `defer os.Remove` runs on the startup goroutine at daemon teardown, after `runSupervisor`'s pool/supervisor drain returns, so no live claude child is still reading it.

## Error handling

| Failure | Behavior |
|---|---|
| `writeMCPApproveConfig` errors (filesystem) | `runSupervisor` returns a wrapped error → daemon startup fails (fail-closed). No session ever spawns with an empty `--mcp-config`. |
| `mcpApprovePath == ""` reaches a non-yolo spawn | Cannot happen: `""` only occurs on the PTY path (factory not built) or when the write failed (startup already aborted). In stream mode a non-yolo spawn always sees a non-empty, valid path. |
| yolo spawn | `withApprovalArgs` returns args unchanged; `--dangerously-skip-permissions` (already in `base`) is emitted, no approval flags — AC #2. |
| tmp file removed early / missing at respawn | Out of scope: the file lives for the daemon's whole lifetime; removal is deferred to shutdown only. |

## Testing strategy

Unit (stdlib table-driven, `cmd/pyry`):

- **`TestWithApprovalArgs`** — the core logic, tested in isolation:
  - yolo row: input args containing `--dangerously-skip-permissions` → output **equals input** (no approval flags added, no duplicate skip flag). Assert `slices.Equal`.
  - non-yolo row: input args without the skip flag → output = input followed by exactly `permissionArgs(false, "/tmp/cfg.json")`. Assert the tail equals `permissionArgs(false, path)` and that `--dangerously-skip-permissions` is absent.
  - non-mutation: assert the input slice is not aliased/mutated (append on a clone).
- **Factory injection** — extend `cmd/pyry/streamsup_runner_test.go`: build the factory with a non-empty `mcpApprovePath`, invoke it with a `supervisor.Config` whose `ClaudeArgs` lack the skip flag, and assert the resulting `streamsup.Config.Args` (via the runner's inspectable args, mirroring the existing no-double-inject test at :152) contains `--permission-prompt-tool`, `--mcp-config <path>`, `--strict-mcp-config`, `--permission-mode default`. A second row with a yolo `ClaudeArgs` asserts none of those four appear and `--dangerously-skip-permissions` appears exactly once.
- **Signature threading** — update the existing `selectInteractiveRunner`/`newStreamRunnerFactory` call sites (pass `""`); the existing assertions (nil-factory on pty, shared-sink instance, loud error on garbage) are unchanged.

E2E (the ACs, no new file):

- **AC #3** — `TestInteractiveStreamModalResolution` (#1154) goes green under `make e2e-realclaude`: the permission-bearing turn surfaces `modal_shown`, the `allow_once` answer resolves it, and `drainForCompletedTurn` sees the continuation delta + terminal idle.
- **AC #4** — `TestInteractiveStreamLiveness` (#1153) still passes. It runs the **yolo** daemon, so the factory injects nothing and its argv is byte-identical to today — no risk from this change.

## Open questions

- **`--allowed-tools` breadth (carried from #1106).** Allowlisted tools bypass the approval-prompt tool by design; an overly broad `--settings`/`--allowed-tools` on the interactive spawn narrows the enforcement gate even with the flags wired. The interactive daemon's settings come from `claudeSettingsArgs` (`--settings <path>`), not `--allowed-tools`, and this ticket does not change them — flagged for awareness, not a change here.
- **Coexistence of `--settings` and `--mcp-config`/`--strict-mcp-config`.** The interactive spawn already carries `--settings <path>` (per-session tool settings); `--strict-mcp-config` governs only MCP-server loading (orthogonal to `--settings`). Expected to coexist cleanly (agent-run uses `permissionArgs` without `--settings`; this is the first pairing). The #1154 real-claude gate is the empirical check — if claude rejects the pairing, that surfaces there, and the resolution would be a claude-flag question, not a design change to this wiring.

---

## Security review

**Verdict: PASS.** (Label-gated self-review; `security-review.md` absent in this worktree — #1139 PASS format.)

**Trust boundaries.** The value being authorized is a remotely-answerable tool-permission decision. The untrusted input (the phone's `modal_answer`) is gated downstream by `permbridge` + the device `MayAnswerRemotePermission()` check (unchanged here). This ticket controls only whether claude is *told to route* permission-bearing tool uses through that gate. The failure mode it fixes is **fail-open**: today a non-yolo interactive stream spawn silently skips the gate entirely.

**Category walk:**

- **Enforcement completeness.** The fix injects `--strict-mcp-config` in the non-yolo arm (via `permissionArgs(false, …)`, unchanged from #1106) — a workdir-planted `.mcp.json` cannot register a shadowing `pyry_approve` server. The `--mcp-config` path is written by `writeMCPApproveConfig`, which is fail-closed on empty socket/bin and can never emit a bare `["mcp-approve"]` resolving to the wrong daemon. The socket value passed is the daemon's **own** `resolveSocketPath(*socketFlag, *name)` result — the "correctness of the socket value at the live site" #1106 deferred is resolved to the same daemon whose `permbridge` will receive the request.
- **Fail-closed posture.** A config-write failure aborts daemon startup rather than proceeding into an empty-`--mcp-config` spawn. There is no code path where a non-yolo stream spawn receives approval flags pointing at nothing.
- **No approval bypass via per-conversation sessions.** Injection is per-spawn in the shared factory, so per-conversation stream sessions are enforced too — not just the bootstrap. A per-session YOLO opt-out is honored only through the same `--dangerously-skip-permissions` signal that already governs claude's own permission bypass; there is no independent bypass introduced.
- **YOLO handling is not weakened.** The yolo arm injects nothing and relies on the pre-existing `--dangerously-skip-permissions` already in the args; it does not add, remove, or reinterpret any bypass. AC #2 keeps it byte-identical to today.
- **PTY path untouched.** The PTY interactive spawn (nil factory) never reaches this code; its trust-folder/on-screen modal surface is unchanged (AC #5).
- **Secret/PII exposure.** The written config contains only the pyry binary path and the control-socket path (no tokens, keys, or user data), mode 0600, in the process's temp dir, removed at shutdown. No new secret crosses a boundary.

**Residual (non-gating):** the `--allowed-tools` breadth caveat under Open questions is a pre-existing property of the permission bridge (#1106's downstream note), not a regression this ticket introduces.
