# #1507 — Derive `pyry mcp-approve`'s client read deadline from the env-aware approval window

**Size:** XS. One production file (`cmd/pyry/mcp_approve.go`), one test file (`cmd/pyry/mcp_approve_test.go`), ~55 lines of total written work, no new exported names, one production call site.

## Files to read first

| Where | Symbol | What to extract |
|---|---|---|
| `cmd/pyry/mcp_approve.go` | `runMCPApprove` | The `&approveServer{…}` composite literal whose `timeout` field hardcodes `mcpApprovalTimeout + mcpApproveClientMargin`. This literal is what moves. |
| `cmd/pyry/mcp_approve.go` | `mcpApproveClientMargin` | The 30s margin, and a doc comment that states the old derivation ("added to `mcpApprovalTimeout`") — it must be reworded with the code. |
| `cmd/pyry/mcp_approve.go` | `approveServer`, `toolsCall` | `timeout` is read exactly once, by `toolsCall`'s `context.WithTimeout`. Confirms the field is the whole surface being changed and that the value is read-only after construction. |
| `cmd/pyry/main.go` | `approvalTimeout`, `envApprovalTimeout`, `mcpApprovalTimeout` | The env-aware accessor to switch to: reads `PYRY_APPROVAL_TIMEOUT`, falls back to the 2m constant on unset/empty/unparseable, and **does not clamp** a non-positive value. |
| `cmd/pyry/main.go` | `runSupervisor` | The daemon end of the same socket: `ctrl.SetApprovalRegistry(approvals, approvalTimeout())`. This is the source the client must now match, and it reads the env once at daemon start. |
| `cmd/pyry/approval_timeout_test.go` | `TestApprovalTimeout` | The table shape and `t.Setenv` idiom the new test mirrors — including the existing "empty falls back to default" row this ticket's table extends. |
| `cmd/pyry/mcp_approve_test.go` | `newApproveServer`, `testLogger` | **Name collision — read this before naming anything.** `newApproveServer` is already declared in this package as a test helper (fixed 5s timeout, discard logger). The production constructor cannot reuse that name. `testLogger(io.Discard)` is the logger the new test passes. |
| `cmd/pyry/mcp_approve_test.go` | import block | `io`, `os`, `testing`, `time` are already imported — the new test adds no imports. |
| `internal/control/client.go` | `Approve`, `request` | The doc contract this ticket restores ("callers MUST pass a ctx whose deadline is >= the daemon's approval window") and the `defer conn.Close()` in `request` that makes client-deadline expiry close the socket. |
| `internal/control/server.go` | `watchApproveConn` | EOF on that conn → `Deny(reasonApproveDisconnect)`. This is why premature expiry is a *deny*, not a split-brain — the correction the ticket body makes to its own filing. |
| `internal/streamsup/runner.go` | `Config.Env` and its use in the spawn | Production leaves `Env` nil, so `cmd.Env` stays nil and claude inherits the daemon's environment. Background for "does the env actually reach the subprocess" (see § Environment propagation). |
| `cmd/pyry/mcp_config.go` | `mcpServerSpec`, `renderMCPApproveConfig` | The mcp-config entry carries `command`/`args` only — no `env` key — so `pyry mcp-approve` inherits claude's environment. |
| `docs/knowledge/features/pyry-mcp-approve-command.md` | § "The fail-closed core (`tools/call approve`)" step 4, and the `mcpApproveClientMargin` paragraph under it | Both state the pre-change derivation. **Read-only for the developer** — see § Out of scope. |

## Context

The two ends of the `mcp.approve` control-socket call derive their windows from different sources. The daemon passes `approvalTimeout()` (env-aware) into the pending-approval registry; `runMCPApprove` builds the client read deadline from the raw `mcpApprovalTimeout` constant plus `mcpApproveClientMargin`. So with `PYRY_APPROVAL_TIMEOUT=10m` the client's ctx expires at 2m30s while the daemon is still holding a 10m window open.

`control.Approve`'s doc comment states the contract in the imperative — callers must pass a ctx whose deadline is ≥ the daemon's approval window — and `runMCPApprove` is a caller that violates it for every override above 2m.

The consequence is fail-closed throughout and the ticket body's correction of its own filing is right: `request` defers `conn.Close()`, the daemon's `watchApproveConn` sees EOF and resolves the pending approval to `Deny(reasonApproveDisconnect)`, and `handleApprove`'s deferred `retire()` dismisses the client modal. Nothing is left outstanding; there is no allow/deny split-brain. What is actually lost is (a) the remainder of the window the operator configured, and (b) the informative `"approval request timed out"` message, replaced by the generic `"approval unavailable"` — which is precisely the message-quality guarantee `mcpApproveClientMargin` exists to provide, and which the margin silently stops providing the moment the env is raised.

This is a one-line semantic change plus the seam that makes it assertable. It does not warrant an ADR.

## Design

### The seam: a production constructor, not a timeout helper

The expression is currently inline in `runMCPApprove`, which ends in the blocking `serveJSONRPCStdio` — nothing can observe the value without starting a server. A seam is required.

**It must be a constructor that owns the whole `approveServer` literal, not a `func approveClientTimeout() time.Duration` helper.** With a helper, the composite literal stays in `runMCPApprove` and a test asserting the helper is vacuous against the ticket's own mandated mutant: reverting the *literal* to `mcpApprovalTimeout + mcpApproveClientMargin` leaves the helper untouched and the test green. Moving the literal into the constructor makes the constructor the call site, so the mutant lands where the test can see it.

```go
// newMCPApproveServer is the sole production construction site for approveServer.
// The per-call client read deadline is derived from approvalTimeout() — the same
// env-aware accessor the daemon hands the pending-approval registry — so raising
// PYRY_APPROVAL_TIMEOUT raises both ends of the socket together.
func newMCPApproveServer(socketPath string, log *slog.Logger) *approveServer
```

Behaviour: returns `&approveServer{socketPath: socketPath, timeout: approvalTimeout() + mcpApproveClientMargin, log: log}`. Nothing else. Asserted by `TestMCPApproveServer_ClientDeadline` (§ Testing strategy).

**Name.** `newApproveServer` is unavailable: `cmd/pyry/mcp_approve_test.go` already declares it as a test helper in the same package, and test files compile into `package main` alongside production code. `newMCPApproveServer` matches the file's existing `mcp*` prefix family (`mcpServerName`, `mcpApprovalTimeout`, `mcpApproveClientMargin`) and the function it serves (`runMCPApprove`). **Do not rename the test helper, and do not route it through the new constructor.** Renaming is adjacent churn (seven call sites in that file) this ticket does not need, and delegating would make the file's eleven `t.Parallel()` tests read the ambient `PYRY_APPROVAL_TIMEOUT` — an operator with a negative value exported would see red round-trip tests nobody else can reproduce. The helper keeps its fixed 5s timeout.

**`runMCPApprove` becomes** two lines where it was six:

```go
s := newMCPApproveServer(socketPath, logger)
return serveJSONRPCStdio(ctx, os.Stdin, os.Stdout, logger, s.register)
```

Everything above it (flag parsing, `signal.NotifyContext`, the stderr logger) is unchanged. The logger is passed in rather than built inside the constructor, so the test can supply `testLogger(io.Discard)` and keep the run quiet.

**Invariant to preserve:** `newMCPApproveServer` is the only place production code constructs an `approveServer`. A deterministic check the developer can run — `grep -n 'approveServer{' cmd/pyry/mcp_approve.go` must return exactly one hit, inside `newMCPApproveServer`. This is the belt to the test's suspenders: no test can observe whether `runMCPApprove` actually calls the constructor (it ends in a blocking serve loop), so the grep is the deterministic half.

### Comment correction (same file, in scope)

`mcpApproveClientMargin`'s doc comment currently opens "is added to `mcpApprovalTimeout` to form the client read deadline" and closes "Inert until #1106 wires `--permission-prompt-tool` in production; tune then." Both clauses are now false — the first as of this change, the second since #1168 wired the flags onto every non-yolo interactive spawn. Reword to say the margin is added to `approvalTimeout()`, and drop the inertness clause. Keep the *reason* the margin exists (the daemon's timer must fire first so its informative deny wins the race) — that is the invariant the new test pins.

Do **not** touch `mcpApprovalTimeout`'s doc comment in `cmd/pyry/main.go`, which carries a similar stale inertness clause. It is a second production file for no acceptance benefit; § Out of scope records it.

### Environment propagation

The fix is only useful if `PYRY_APPROVAL_TIMEOUT` actually reaches the `pyry mcp-approve` subprocess. It does, by inheritance at both hops:

```
daemon (reads PYRY_APPROVAL_TIMEOUT via approvalTimeout() for the registry)
  └─ claude          spawned by streamsup; Config.Env is nil in production, so
     │               cmd.Env stays nil and the child inherits os.Environ()
     └─ pyry mcp-approve   spawned by claude from the mcp-config entry, whose
                           mcpServerSpec carries command/args only — no env key
```

If some future claude release were to sanitize the environment for stdio MCP servers, `approvalTimeout()` in the subprocess falls back to `mcpApprovalTimeout` — i.e. exactly today's shipped behaviour, still fail-closed, still with the margin intact relative to its own (default) window. The change is monotone-safe: it can restore the operator's window, and in the worst case leaves it where it already is.

## Concurrency model

Unchanged. `acp.Transport.Serve` still runs one read-loop goroutine dispatching inline; `approveServer` is still read-only after construction, so `timeout` needs no lock. The only shift is *when* the env is read: `approvalTimeout()` is now called once per `pyry mcp-approve` process, at construction, rather than never. `pyry mcp-approve` is a short-lived subprocess claude spawns per session, so a construction-time read is the right granularity, and it mirrors the daemon, which reads the env once at `SetApprovalRegistry`. No re-read per `tools/call`, no mid-process reconfiguration.

## Error handling

No new failure modes and no change to any existing branch. `toolsCall`'s outcome mapping is untouched: every terminus is still a well-formed tool result with `isError:false`, and the only self-originated verdict is still deny.

What changes is which of two denies wins a race that today the *wrong* side wins under an enlarged env:

| `PYRY_APPROVAL_TIMEOUT` | Before | After |
|---|---|---|
| unset | daemon fires at 2m, client at 2m30s → `"approval request timed out"` | identical |
| `10m` | client fires at 2m30s → `"approval unavailable"`, 7m30s of the window lost | daemon fires at 10m, client at 10m30s → `"approval request timed out"` |

Deliberately out of scope, per the ticket: `approvalTimeout()` does not clamp, so a non-positive `PYRY_APPROVAL_TIMEOUT` (e.g. `-1h`) yields an already-expired client ctx and an immediate deny. Both ends stay fail-closed (permbridge documents a non-positive timeout as denying immediately), no such value has been observed in use, and clamping belongs to `approvalTimeout()` itself, not to this call site.

## Testing strategy

One new test in the existing `cmd/pyry/mcp_approve_test.go` (the file the feature doc already names as this subcommand's test home). No new file, no new imports, no new helpers. Table-driven, `t.Setenv`, no `t.Parallel()` (env mutation forbids it — `TestApprovalTimeout` has the same constraint).

`TestMCPApproveServer_ClientDeadline` — for each row, set the env, construct via `newMCPApproveServer("/nonexistent/p.sock", testLogger(io.Discard))` (never dialled; construction does no I/O), and assert both properties:

1. **The derived deadline** equals the expected value.
2. **The ordering invariant** — `s.timeout - approvalTimeout()` equals `mcpApproveClientMargin` exactly. This is the property the margin exists for: the daemon's timer must fire first so its `"approval request timed out"` deny beats the client's generic `"approval unavailable"`.

| Row | `PYRY_APPROVAL_TIMEOUT` | Expected client deadline |
|---|---|---|
| unset falls back to the default window | *genuinely unset* | `2m30s` |
| unparseable falls back to the default window | `not-a-duration` | `2m30s` |
| short override | `2s` | `32s` |
| generous override | `10m` | `10m30s` |

The first row must be a **real unset**, not an empty string — the AC says "unset" and the existing `TestApprovalTimeout` already covers empty. `testing` has no `t.Unsetenv`, so the row does `t.Setenv(envApprovalTimeout, "")` (which registers the restore-at-cleanup) followed by `os.Unsetenv(envApprovalTimeout)`. `t.Setenv` captures the prior value at call time, so the later unset does not defeat cleanup, and an operator's real `PYRY_APPROVAL_TIMEOUT` in the developer's shell cannot leak into the row.

### Mutants — measured, not predicted

Both mandated mutants were run during design against the proposed code via `go test -overlay` (no worktree writes). The developer must re-run them and report the row-level result; the expected matrix is not a guess:

| Mutant | unset | `not-a-duration` | `2s` | `10m` |
|---|---|---|---|---|
| *(unmutated)* | PASS | PASS | PASS | PASS |
| **A** — revert to `mcpApprovalTimeout + mcpApproveClientMargin` | PASS | PASS | **FAIL** | **FAIL** |
| **B** — drop the margin, leave `approvalTimeout()` | **FAIL** | **FAIL** | **FAIL** | **FAIL** |

Both assertions fire on every red row. Mutant A's `10m` row is the ticket's defect stated as a number: the ordering assertion reports the delta as **−7m30s** — the client deadline *shorter* than the daemon's window, which is the truncation itself, sign and all.

Recipe (per the repo's established overlay practice — nothing enters the worktree):

```
# overlay.json — absolute paths on both sides, "Replace" is a MAP not an array
{"Replace": {"<worktree>/cmd/pyry/mcp_approve.go": "<scratchpad>/mutA_mcp_approve.go"}}
go test -count=1 -overlay=<abs>/ov-mutA.json -run '^TestMCPApproveServer_ClientDeadline$' -v ./cmd/pyry/
```

Build each mutant with python `.replace()` and assert the replacement changed something — a silent no-op mutation runs green and reads as a weak test. Eyeball each run for `[build failed]` before reading a red as a kill.

### Regression surface

`go test -race ./cmd/pyry/` must stay green — the whole package was run under the proposed change during design (green, ~24s). `TestApprovalTimeout` is untouched and must remain so: it pins `approvalTimeout()` itself, which this ticket does not modify.

No e2e and no live-claude coverage. `internal/e2e/relay_v2_stream_modal_test.go` exercises the approval round-trip via fakeclaude dialling `control.Approve` with its own ctx, never through `pyry mcp-approve`, so it neither covers this line nor needs changing. The acceptance here is a deterministic duration derivation, not a modal round-trip.

## Acceptance criteria (developer deliverables)

1. `pyry mcp-approve`'s per-call client read deadline derives from `approvalTimeout() + mcpApproveClientMargin`, inside `newMCPApproveServer`, which `runMCPApprove` calls; `grep -n 'approveServer{' cmd/pyry/mcp_approve.go` returns exactly one hit.
2. `TestMCPApproveServer_ClientDeadline` asserts the derived deadline across the four rows above, the first two proving production is unchanged when the env is absent or unparseable.
3. The same test asserts, on every row, that the derived deadline exceeds `approvalTimeout()` by exactly `mcpApproveClientMargin`.
4. Both mutants are confirmed red by **running** them, with the row-level pattern in the matrix above reported in the PR.

`mcpApproveClientMargin`'s doc comment is corrected as part of AC 1 (same file, same change).

## Out of scope

- **`docs/knowledge/features/pyry-mcp-approve-command.md` (the filing's AC 5) is not a developer deliverable.** `docs/knowledge/` is owned by the documentation phase; making it an implementation AC pushes fixed-cost housekeeping into the build budget. The correction it needs, recorded here so the documentation phase can fold it in:
  - § "The fail-closed core", **step 4** — reads `context.WithTimeout(ctx, mcpApprovalTimeout + mcpApproveClientMargin)`. Should be `approvalTimeout() + mcpApproveClientMargin`.
  - The **`mcpApproveClientMargin` paragraph** below the steps — reads "added to `mcpApprovalTimeout` (2 min, #1104)". Should say the margin is added to the env-aware `approvalTimeout()` (default 2m, overridable via `PYRY_APPROVAL_TIMEOUT`), so both ends of the socket track one source and an enlarged window is not truncated at 2m30s.
  - Same section's `mcpApprovalTimeout` reference should no longer imply the client end is frozen at the constant.
- **`mcpApprovalTimeout`'s doc comment in `cmd/pyry/main.go`** still says the approve path is "inert for now because nothing invokes the verb until the `pyry mcp-approve` sibling wires `--permission-prompt-tool`". Stale since #1168. Touching it adds a second production file for no acceptance benefit; file separately if it bothers a reader.
- **Clamping a non-positive `PYRY_APPROVAL_TIMEOUT`.** Belongs in `approvalTimeout()`, affects the daemon end too, and no such value has been observed. See § Error handling.
- **The stale note in `docs/knowledge/codebase/1139.md`** calling this path "inert until the wiring gap is closed". That directory is frozen (2026-08-19) and is read as history; nobody edits it.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries] Finding — the client deadline gains an input it did not have.** Before this change `approveServer.timeout` was a compile-time constant, uninfluenceable at runtime. After it, it is a function of `PYRY_APPROVAL_TIMEOUT` read by `approvalTimeout()` in the `pyry mcp-approve` process. That is a real widening of the input surface and is named here rather than assumed benign. It is not exploitable, for two independent reasons. First, **neither direction yields an allow**: `allow` reaches claude only by passing through `resp.Approve` in `toolsCall`, sourced from the daemon's trusted in-process resolver; every outcome this ticket can influence — ctx expiry, socket error, nil verdict — terminates in `deny`. A hostile value moves *which deny message* arrives and *when*, never whether an allow exists. Second, the party best placed to set that env is claude itself (it spawns the subprocess), and **a claude that can set its child's environment can already decline to call the prompt tool at all** — the bridge's trust anchor is claude honouring `--permission-prompt-tool` under `--strict-mcp-config` (`permissionArgs`), which this ticket does not touch. Env influence therefore adds no capability to an actor who does not already have a strictly stronger one. Classification: no action.
- **[Trust boundaries] No further findings — the model-controlled path is untouched.** `toolsCall` still carries `input` as opaque `json.RawMessage`, never parses or dispatches on it, and `logVerdict` remains the sole decision log emitting only `tool_use_id` and `behavior`. `newMCPApproveServer` sees neither. The socket boundary is unchanged: the constructor receives the `socketPath` `parseClientFlags` produced, and `renderMCPApproveConfig` remains the deterministic net keeping the forwarder pointed at the spawning daemon.
- **[Tokens, secrets, credentials] No findings — none are in play by design.** The change creates, stores, compares and logs no credential material. The env value is consumed by `time.ParseDuration` inside `approvalTimeout()` and discarded on failure; it never reaches an argv, a filename, a log field, or a deny message. `denyResult`'s reasons remain fixed constants.
- **[File operations] No findings — no filesystem operation is added.** `newMCPApproveServer` performs no I/O; the test constructs against `/nonexistent/p.sock` and never dials. The mcp-config write (`writeMCPApproveConfig`) and its lifecycle are untouched, and no path is built from the environment.
- **[Subprocess / external execution] Finding — inheritance is the delivery mechanism, and it is unchanged.** The env reaches the process because `streamsup`'s runner leaves `Config.Env` nil (so `cmd.Env` is nil and claude inherits) and `mcpServerSpec` carries `command`/`args` with no `env` key. This ticket changes *what is read*, not *what is inherited* — the same full environment crossed both hops before it. No `exec.Command` argument derives from the env, and no `sh -c` exists on this path. If a future claude release scrubs the environment for stdio MCP servers, `approvalTimeout()` falls back to `mcpApprovalTimeout` and behaviour is byte-identical to today: fail-safe on the degradation.
- **[Cryptographic primitives] Not applicable by design decision.** The change is one duration-arithmetic expression. No randomness is drawn, no key or nonce is derived or reused, and nothing attacker-controlled is compared against a secret.
- **[Network & I/O] Finding — the client read deadline is now unbounded above, deliberately.** Timeout discipline is the category this ticket lives in, so the ceiling deserves an explicit statement. Before: a fixed 2m30s cap on how long `control.Approve` holds its conn read. After: whatever the operator configured, plus 30s. Three consequences, all accepted: (a) **claude blocks for the full configured window** rather than being released at 2m30s — that is the ticket's intent, and code review should read a longer block as the fix working, not as a regression; (b) the **wedged-daemon backstop is correspondingly later** — but the client deadline was never the security timer, the daemon's registry timer is, and it is the one `approvalTimeout()` already drove; the margin keeps the client strictly later so the daemon's informative deny keeps winning, which is exactly the ordering the enlarged-env case broke *before* this fix (the client fired first, at a negative delta of −7m30s on the `10m` row — see the mutant matrix); (c) **resource exhaustion is bounded elsewhere** — at most one in-flight approval per `pyry mcp-approve` process (claude calls the prompt tool synchronously), so the conn count is bounded by session count, not by the deadline. No new read is added and no input-size limit changes.
- **[Network & I/O] OUT OF SCOPE — clamping a non-positive or absurd `PYRY_APPROVAL_TIMEOUT`.** `-1h` yields an already-expired ctx and an immediate client-side deny (fail-closed, and reached without the daemon registering a pending approval at all — the operator loses the modal, not the enforcement). A very large value yields a long client wait bounded in practice by the daemon's own window. The clamp belongs in `approvalTimeout()`, where it would cover the daemon end too, not at this call site. **No ticket exists yet**; #1507's Technical Notes defer it and PO should file one if the unbounded property ever matters. This spec's call site inherits any future clamp for free.
- **[Error messages, logs, telemetry] No findings — no message or log field is added or changed.** The deny reasons stay fixed constants, `logVerdict` stays content-free, and no derived duration is emitted anywhere. The only place the env value is rendered is the new test's failure message, against fixture values.
- **[Concurrency] Finding — SHOULD FIX (already specified): the new test must stay serial, and `newApproveServer` must not be made to delegate.** `t.Setenv` mutates process-global state, and `cmd/pyry/mcp_approve_test.go` contains eleven `t.Parallel()` tests. The test's own safety is self-enforcing (`t.Setenv` panics in a test with a parallel ancestor) and top-level parallel tests are released only after the package's serial phase, which is why `TestApprovalTimeout` already coexists with them. The live hazard is the tempting tidy-up: routing the existing test helper `newApproveServer` through `newMCPApproveServer` would make eleven parallel tests read the ambient `PYRY_APPROVAL_TIMEOUT`, so a developer with `-1h` exported would get a negative timeout and red round-trip tests that are green for everyone else. § Design forbids the rename and the delegation; keep the helper's fixed 5s. Beyond that: no goroutine is spawned, no lock is taken, and `approveServer` remains read-only after construction — the env is read exactly once per short-lived subprocess, mirroring the daemon's single read at `SetApprovalRegistry`.
- **[Threat model alignment] No findings.** `docs/threat-model.md` does not exist in this repo (checked). This is not a relay ticket — nothing here crosses the relay boundary, so the mobile protocol's security model is not engaged. The governing statement for this path is the fail-closed invariant recorded in `docs/knowledge/features/pyry-mcp-approve-command.md` § "The fail-closed core": always a well-formed tool result, never a hang, deny as the only self-originated verdict. The change preserves all three, and strengthens the message-quality guarantee the margin was introduced for by making it hold at every window rather than only at the default.

**Reviewer:** architect (self-review per the security-review checklist)
**Date:** 2026-08-19

## Open questions

- **Does claude pass its environment through to stdio MCP servers in every supported release?** The mcp-config entry carries no `env` key, which is the "inherit" shape, and this is how the wiring is used today. If a release ever sanitizes it, the failure is benign and silent: `approvalTimeout()` falls back to the constant and the subprocess behaves exactly as it does today. Not worth a probe on this ticket; worth remembering if an operator reports that raising the env still truncated the window.
- **Should `approvalTimeout()` clamp?** Deferred by the ticket. If it ever does, this call site inherits the clamp for free — which is an argument for putting it there rather than here.
