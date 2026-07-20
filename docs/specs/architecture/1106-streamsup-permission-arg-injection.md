# Spec #1106 — streamsup spawn-arg injection: non-YOLO permission-prompt-tool + mcp-config, YOLO skip-permissions

**Ticket:** [#1106](https://github.com/pyrycode/pyrycode/issues/1106) · size **S** · `security-sensitive`
**Chain:** #1079 (permbridge M) → #1103 (registry) / #1104 (blocking verb) / #1105 (`pyry mcp-approve` stdio) → **#1106** (this: the enforce-vs-skip switch)

## Files to read first

- `cmd/pyry/agent_run.go:332-366` — `buildStreamRunnerClaudeArgs`: the current shape and the **hardcoded `--dangerously-skip-permissions`** (line 359) this ticket makes conditional. Note the flag slot: after `--verbose`, before `--append-system-prompt-file`.
- `cmd/pyry/agent_run.go:280-293` — `runAgentRunStreamRunner`: the **sole production caller** of `buildStreamRunnerClaudeArgs`. It is the legacy `PYRY_USE_STREAMJSON=1` path; it stays always-YOLO (pass `yolo=true, mcpConfigPath=""`) — behaviour-preserving.
- `cmd/pyry/mcp_approve.go:19-34` — `mcpServerName = "pyry_approve"`, `approveToolName = "approve"`. **Reuse these constants; do not re-literal.** The full tool reference `mcp__pyry_approve__approve` is derived from them (satisfies AC-3 structurally — the names cannot drift from what the merged subcommand advertises).
- `cmd/pyry/mcp_approve.go:62-69` — `runMCPApprove` resolves the socket via `parseClientFlags("pyry mcp-approve", args)`. This pins the config's server-command arg vector: `["mcp-approve", "-pyry-socket", <socket>]`.
- `cmd/pyry/main.go:237-238` — dispatch: `runMCPApprove(os.Args[2:])`. Everything after `pyry mcp-approve` is what `parseClientFlags` sees, so `-pyry-socket` must be the **first token after** `mcp-approve`.
- `cmd/pyry/main.go:336-358` — `splitClientFlags` peels `-pyry-*` value-flags **off the front** and stops at the first non-`pyry-*` token. Confirms the arg ordering above; a trailing `-pyry-socket` would not be peeled.
- `cmd/pyry/update.go:85-93` — `resolveExecutable()` (`os.Executable()` with `os.Args[0]` fallback): the source for the config's `command` (the running pyry binary, so the fork is the same binary).
- `internal/agentrun/settings/settings.go:92-125` — `writeSettings` tmp-file recipe (`os.CreateTemp` → `json.NewEncoder(f).Encode` → `f.Close()` → return name, remove-on-error). Mirror this for `writeMCPApproveConfig`.
- `cmd/pyry/agent_run_test.go:444-492` — `TestBuildStreamRunnerClaudeArgs_Shape`: the golden-argv table test to extend for the new signature + non-YOLO case.

## Context

`buildStreamRunnerClaudeArgs` (`cmd/pyry/agent_run.go`) today **unconditionally** injects `--dangerously-skip-permissions`. That flag disables claude's permission path entirely. The permbridge (#1103–#1105) built the other half of the switch: `pyry mcp-approve` is an MCP stdio server that routes every non-allowlisted tool use through the daemon's approval registry and blocks on an allow/deny verdict. The T1 spike (#1075) proved that claude, spawned with `--permission-prompt-tool mcp__pyry_approve__approve --mcp-config <file> --strict-mcp-config --permission-mode default` (and **no** `--dangerously-skip-permissions`), synchronously gates every tool through that server.

This ticket assembles those args as a **YOLO toggle** and generates the tmp mcp-config that points claude at `pyry mcp-approve`. It is the enforce-vs-skip switch for the whole mechanism — getting the non-YOLO branch wrong (omitting the tool, or pointing the config at the wrong daemon socket) *silently* disables enforcement — so the branch and its tests are security-load-bearing.

**Scope boundary (from the ticket):** this is a self-contained, unit-testable unit — assert argv + generated config content for both branches. The *source* of the YOLO boolean at a live spawn, the per-spawn config-file lifecycle (defer-remove) at the live call site, and the streamsup runner-selection wiring that calls this are **downstream**. This ticket wires the toggle through the one existing caller (`runAgentRunStreamRunner`, always YOLO) and leaves the non-YOLO branch exercised by unit tests only.

**Note on the `streamsup` vs `streamrunner` naming.** The ticket title says "streamsup"; `buildStreamRunnerClaudeArgs` today feeds the *legacy `streamrunner`* one-shot path, not the long-lived `streamsup` path (streamsup's `buildArgs` appends `Config.Args` verbatim and is not yet wired to a production spawn). The design resolves this by making the two new units **runner-agnostic** (they take no `agentRunArgs`): the downstream streamsup live-wiring reuses `permissionArgs` + `writeMCPApproveConfig` directly when assembling streamsup's `Config.Args`. `buildStreamRunnerClaudeArgs` is simply the first consumer to get the toggle plumbed through.

## Design

All new code lives in `cmd/pyry` (`package main`), alongside the existing arg-assembly and the `mcp_approve.go` constants it reuses. Two new units + one signature change.

### 1. `permissionArgs` — the security-load-bearing switch (new, `cmd/pyry/agent_run.go` or new `cmd/pyry/mcp_config.go`)

```go
// approveToolRef is the full MCP tool reference claude is pointed at,
// derived from the merged subcommand's constants so the two cannot drift.
var approveToolRef = fmt.Sprintf("mcp__%s__%s", mcpServerName, approveToolName)

func permissionArgs(yolo bool, mcpConfigPath string) []string
```

Behaviour contract:

- `yolo == true` → returns exactly `["--dangerously-skip-permissions"]`. No permission-prompt-tool, no mcp-config, no permission-mode.
- `yolo == false` → returns exactly, in order:
  `["--permission-prompt-tool", approveToolRef, "--mcp-config", mcpConfigPath, "--strict-mcp-config", "--permission-mode", "default"]`.
  No `--dangerously-skip-permissions`.

Rationale for each non-YOLO flag (all spike-verified, all security-relevant):
- `--permission-prompt-tool <ref>` — routes every non-allowlisted tool use to the approve tool.
- `--mcp-config <path>` — registers the `pyry_approve` stdio server (the generated config).
- `--strict-mcp-config` — claude loads **only** the servers in the supplied config, ignoring any project/user `.mcp.json`. Without it, a rogue project-level MCP config could add or shadow servers → do not omit.
- `--permission-mode default` — pins claude to the mode where the prompt tool is consulted (not `bypassPermissions`/`acceptEdits`/`plan`).

This function is the reusable switch; the downstream streamsup wiring calls it directly. It does not know about `agentRunArgs`.

### 2. `buildStreamRunnerClaudeArgs` — thread the toggle (modify, `cmd/pyry/agent_run.go:354`)

New signature:

```go
func buildStreamRunnerClaudeArgs(parsed agentRunArgs, yolo bool, mcpConfigPath string) []string
```

Replace the hardcoded `"--dangerously-skip-permissions"` element (line 359) with the spread of `permissionArgs(yolo, mcpConfigPath)` in the same slot (after `--verbose`, before `--append-system-prompt-file`). Everything else is unchanged.

Sole production caller — `runAgentRunStreamRunner` (`agent_run.go:288`) — passes `buildStreamRunnerClaudeArgs(parsed, true, "")` (legacy path is always YOLO; preserves current byte-for-byte behaviour). Update the existing doc comment on the `--dangerously-skip-permissions` bullet to reflect that it is now the YOLO branch.

### 3. mcp-config generation (new, `cmd/pyry/mcp_config.go`)

Config shape (contract — the exact JSON claude's `--mcp-config` expects for a stdio server):

```go
type mcpApproveConfig struct {
	MCPServers map[string]mcpServerSpec `json:"mcpServers"`
}
type mcpServerSpec struct {
	Command string   `json:"command"`
	Args    []string `json:"args"`
}
```

Serialises to:

```json
{"mcpServers":{"pyry_approve":{"command":"<pyryBin>","args":["mcp-approve","-pyry-socket","<socketPath>"]}}}
```

- **Server key** is `mcpServerName` (the constant), not a literal — matches AC-3.
- **`command`** is `pyryBin` (the caller passes `resolveExecutable()` at the live site) so the fork is the same pyry binary.
- **`args`** is exactly `["mcp-approve", "-pyry-socket", socketPath]` — the socket-targeting identity that satisfies **AC-4**. A bare `["mcp-approve"]` (no socket) would resolve to the default-named instance's socket → forwarded approvals reach the wrong daemon → **silent** enforcement failure. This is the fail-closed invariant of this ticket.

Two functions:

```go
// renderMCPApproveConfig builds the config bytes. Fail-closed guards:
// empty socketPath or empty pyryBin returns an error and nil bytes —
// never a config that omits -pyry-socket or names an empty command.
func renderMCPApproveConfig(pyryBin, socketPath string) ([]byte, error)

// writeMCPApproveConfig writes the rendered config to a per-spawn tmp file
// (os.CreateTemp "pyry-mcp-approve-*.json"), mirroring the settings writer,
// and returns the path. The caller owns removal (defer os.Remove) at the
// live-spawn site — this slice does not wire that lifecycle.
func writeMCPApproveConfig(pyryBin, socketPath string) (string, error)
```

The empty-`socketPath` guard in `renderMCPApproveConfig` is the deterministic enforcement of AC-4: a caller that fails to supply a socket gets an error, not a silently-misdirected config. (Belt-and-suspenders: the config generation refuses; it is not merely a convention that the caller must pass a socket.)

## Data flow

```
live spawn (downstream, non-YOLO):
  resolveExecutable() ─┐
  daemon control socket┼─> writeMCPApproveConfig(pyryBin, socket) ─> /tmp/pyry-mcp-approve-*.json (path)
                       │                                                    │
                       └────────────> permissionArgs(false, path) ─────────┘
                                             │
                                             v
                    [...--verbose, --permission-prompt-tool mcp__pyry_approve__approve,
                        --mcp-config <path>, --strict-mcp-config, --permission-mode default, ...]
                                             │
                                             v  (claude spawns per tool use)
                    pyry mcp-approve -pyry-socket <socket>  ──> daemon approval registry (#1103)

this ticket wires only: runAgentRunStreamRunner ─> buildStreamRunnerClaudeArgs(parsed, true, "")  [YOLO]
```

## Concurrency model

None. `permissionArgs` and `renderMCPApproveConfig` are pure functions of their inputs; `writeMCPApproveConfig` does a single synchronous `CreateTemp`/encode/close with no shared state. No goroutines, no locks.

## Error handling

- `renderMCPApproveConfig`: returns an error (nil bytes) when `pyryBin == ""` or `socketPath == ""` (fail-closed, per AC-4). The `json.Marshal` of the fixed-shape struct with string fields is effectively unreachable-fail; propagate it anyway for hygiene.
- `writeMCPApproveConfig`: mirror `writeSettings` — on `CreateTemp` error return it; on render/encode/close error, `os.Remove` the tmp file and return the wrapped error. Error strings prefixed `mcp-approve config:` for grep-ability.
- `permissionArgs` / `buildStreamRunnerClaudeArgs`: total functions, no error return.

## Testing strategy

All tests are `package main`, table-driven, stdlib `testing` only (no live claude). Scenarios (developer writes them in the project idiom):

- **`permissionArgs` YOLO** — `(true, "")` → asserts exact `["--dangerously-skip-permissions"]`; asserts `--permission-prompt-tool` and `--mcp-config` are absent.
- **`permissionArgs` non-YOLO** — `(false, "/tmp/cfg.json")` → asserts the exact 7-element slice; asserts the `--permission-prompt-tool mcp__pyry_approve__approve` pair, `--mcp-config /tmp/cfg.json`, `--strict-mcp-config`, `--permission-mode default` are present and `--dangerously-skip-permissions` is absent. (**AC-1, AC-2.**)
- **`buildStreamRunnerClaudeArgs` YOLO regression** — extend `TestBuildStreamRunnerClaudeArgs_Shape` to call `(parsed, true, "")`; the existing golden argv must be unchanged (guards the legacy path).
- **`buildStreamRunnerClaudeArgs` non-YOLO** — `(parsed, false, "/tmp/cfg.json")` → full argv with the permission pair in the permission slot and no `--dangerously-skip-permissions`. (**AC-1.**)
- **`renderMCPApproveConfig` content** — `("/opt/pyry", "/home/u/.pyry/elli.sock")` → `json.Unmarshal` round-trips into `mcpApproveConfig`; assert exactly one server keyed `pyry_approve`; `command == "/opt/pyry"`; `args == ["mcp-approve","-pyry-socket","/home/u/.pyry/elli.sock"]`. Explicitly assert `-pyry-socket` is present and carries the exact socket value. (**AC-3, AC-4.**)
- **`renderMCPApproveConfig` fail-closed** — `socketPath == ""` → error, nil bytes (never a bare `["mcp-approve"]`). Same for `pyryBin == ""`. (**AC-4.**)
- **`writeMCPApproveConfig`** — writes a file; `os.ReadFile` it back; bytes equal `renderMCPApproveConfig` output; `t.Cleanup` removes it.
- **Drift guard** — assert `approveToolRef == "mcp__"+mcpServerName+"__"+approveToolName` and the config server key `== mcpServerName`, so renaming a constant in `mcp_approve.go` cannot silently desync the tool reference from the advertised server. (**AC-3.**)

## Open questions

1. **Source of the `yolo` boolean at a live spawn** — downstream (runner-selection wiring). Out of scope; this unit takes it as an input the tests set directly.
2. **Direct reuse vs. streamsup-specific assembler** — recommend the downstream streamsup wiring call `permissionArgs` + `writeMCPApproveConfig` directly (both are runner-agnostic) rather than duplicating. No new abstraction needed now.
3. **`env` key in the stdio server spec** — omitted. The forked `pyry mcp-approve` inherits the parent's environment via claude's spawn; no config-level `env` is required. Add only if a downstream spawn shows `mcp-approve` missing `HOME`/`PATH`.
4. **Home for the new code** — proposed `cmd/pyry/mcp_config.go` for the config functions; `permissionArgs` may live there or in `agent_run.go`. Developer's call; keep it in `package main`.

## Security review

**Verdict:** PASS

This ticket *is* the enforce-vs-skip switch for the whole permission bridge, so the pass was run adversarially assuming the switch has a silent-bypass hole. The three security-relevant invariants — (a) the two branches are exhaustive and exactly what the tests pin, (b) no untrusted/model input reaches the argv or the config, (c) the config always targets a concrete socket — are each satisfied by deterministic code (branch structure + the empty-socket guard in `renderMCPApproveConfig`), not by convention. Findings:

- **[Trust boundaries] No MUST FIX.** The only boundary this unit owns is the YOLO switch and the config it emits. Inputs (`yolo`, `pyryBin`, `socketPath`, `mcpConfigPath`) are daemon-state, never network- or model-controlled — the model-controlled tool `input` that `pyry mcp-approve` carries (reviewed in #1105) never reaches this arg-assembly. The boundary is a single exhaustive `if/else` in `permissionArgs`, unit-tested for the exact slice on both arms. The *correctness* of which socket value is passed (that it is the spawning daemon's, not a default) is the caller's contract, explicitly out of scope (Open Question #1/#2) — this unit only guarantees a socket is *present*.

- **[Subprocess execution] No MUST FIX — and one non-negotiable inclusion.** The generated config drives claude to `exec` `pyry mcp-approve`. `--strict-mcp-config` (in the non-YOLO branch) is what closes the hostile-workdir vector: without it, a `.mcp.json` planted in the workdir could register an *additional* or name-colliding `pyry_approve` server (a fake auto-allow forwarder or a data-exfil server), silently defeating enforcement. Its omission would be **MUST FIX**; the design includes it, so this is satisfied. MCP stdio servers are spawned by argv vector, not `sh -c`, so `socketPath` is passed as a single literal arg element — even a socket path beginning with `-` is consumed as the flag *value* by `splitClientFlags` (`main.go:350`, unconditional next-token grab), so there is no arg/shell injection through it.

- **[File operations] No MUST FIX.** `writeMCPApproveConfig` mirrors `writeSettings` (`settings.go:97-122`): `os.CreateTemp` yields a **0600**, random-named file, so `pyryBin`/`socketPath` never construct the path (no traversal) and the unpredictable name defeats symlink pre-plant / TOCTOU. The file content (socket path, binary path) is not secret, and 0600 keeps it owner-only. **SHOULD FIX (downstream):** the writer returns the path and does *not* remove it; the live-spawn wiring must `defer os.Remove(path)` (mirroring `runAgentRunPty`'s `defer os.Remove(settingsPath)`, `agent_run.go:309`) or leak 0600 tmp files. Documented on the writer; belongs to the live-wiring ticket, not this unit.

- **[Error messages / logs] No MUST FIX.** No secrets exist to leak here. The socket path travels *inside the config file*, not on claude's command line, so even if `streamsup.Run`'s `"spawning claude", "args"` log (`runner.go:329`) prints the full argv, only the tmp `--mcp-config <path>` and the fixed tool reference appear — not the socket. Config-writer errors are prefixed `mcp-approve config:` and wrap stdlib errors carrying at most the (non-secret) temp-dir path.

- **[Fail-closed on empty socket] Satisfied by design.** `renderMCPApproveConfig` returns an error (nil bytes) when `socketPath == ""` or `pyryBin == ""`, so it can never emit a bare `["mcp-approve"]` (which would resolve to the default instance = wrong daemon = silent enforcement failure, the AC-4 hazard). This is the deterministic safety net (different fabric) behind the stochastic caller-supplies-the-right-socket rule. **SHOULD FIX (downstream):** `permissionArgs(false, "")` would emit a broken `--mcp-config ` with an empty value; the composed flow (`writeMCPApproveConfig` → check err → `permissionArgs(false, path)`) makes the path non-empty by construction, and this precondition is documented on `permissionArgs`. Fail-closed regardless (claude with an unreachable/empty prompt-tool config denies rather than silently allows), so noted, not gated.

- **[Cryptographic primitives] N/A.** No randomness is security-relevant (the only RNG is `CreateTemp`'s tmp-name entropy, which is a mild anti-TOCTOU plus, not a security dependency); no keys, no comparisons against secrets.

- **[Network & I/O] N/A.** This unit opens no socket and reads no external input. The daemon control-socket connection is made by `pyry mcp-approve` (#1105) / `internal/control` (#1103), both already reviewed; caps and deadlines live there.

- **[Concurrency] N/A.** `permissionArgs` and `renderMCPApproveConfig` are pure; `writeMCPApproveConfig` is one synchronous `CreateTemp`/encode/close with no shared state, no goroutines, no locks. Each spawn's `CreateTemp` yields a distinct path, so concurrent spawns cannot collide.

- **[Threat-model alignment] Covered.** Primary threat — an agent turn escaping the daemon approval gate — is addressed by (a) exhaustive branches + exact-argv tests, (b) `--strict-mcp-config` + `--permission-mode default`, (c) the empty-socket fail-closed guard. Out of scope, named: source of the `yolo` boolean, correctness of the socket *value* at the live site, per-spawn config removal lifecycle, and `pyry mcp-approve`'s own dial/log behaviour (#1105) and the daemon approval registry (#1103).

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-07-20
