# Spec — #943: interactive session-pool claude spawn must pre-approve project MCP servers

**Size:** S (confirmed — 3 production files, ~250 total LOC, no signature changes → no edit fan-out)
**Security-sensitive:** No. Mirrors the operator-shipped, un-gated agent-run compat fix (`d10ce87`); the settings content is pyry-fixed, no untrusted input is parsed, and the spawn runs in the operator's own pre-trusted workspace. (See "MCP-consent context" below — noted for awareness, not a gate.)

## Files to read first

- `internal/sessions/session.go:91-122` — `claudeSettingsArgs` + `spawnArgs`. **The recompose seam.** `spawnArgs` = `spawnBase + claudeSettingsArgs(settings)`; this is why `--settings` must live in `spawnBase`, NOT in `claudeSettingsArgs`.
- `internal/sessions/session.go:145-161` — the `settings` and `spawnBase` field docs. `spawnBase` is the immutable "settings-free argv"; it already carries the `--session-id` resume suffix for minted sessions. `--settings <path>` is the new member of that base. Add the sibling field `settingsPath` here.
- `internal/sessions/pool.go:394-401` — bootstrap base composition (`Pool.New`). Injection point #1.
- `internal/sessions/pool.go:1202-1270` — `buildSession` minted base composition + the returned `Session` literal. Injection point #2 + where `settingsPath` is set on the minted session.
- `internal/sessions/pool.go:745-781` — `Pool.Remove`. Cleanup hook for minted sessions (after `Evict` returns).
- `internal/sessions/pool.go:1009-1028` — `Pool.Run` head. Captures `bootstrap := p.sessions[p.bootstrap]` (line 1011) and already has a `defer` block (1020-1024). Bootstrap file cleanup goes here.
- `internal/sessions/get_or_create.go:70` and `internal/sessions/pool.go:1150` — the two `buildSession` callers. **Confirm they stay unchanged** — the write happens inside `buildSession`, so its signature does not change and there is no call-site cascade.
- `internal/agentrun/settings/settings.go:35-123` — the agent-run writer to **mirror the shape/discipline of, but NOT reuse.** Note `settingsFile.Permissions` is non-omitempty and byte-order-load-bearing, and `writeSettings` always stamps `defaultMode:"dontAsk"` and requires non-empty `allowedTools` — all three forbidden by AC #2. Copy the tempfile + best-effort-remove-on-error discipline only.
- `internal/agentrun/ptyrunner/runner.go:610-620` — the exact flag form claude expects: `"--settings", <path>` as two argv elements. The session-pool path appends the identical pair.
- Twin fix for context: `git show d10ce87` (`fix(agent-run): enable project MCP servers in per-spawn settings`) — the un-gated operator commit this ticket mirrors onto the session-pool path.

## Context

The mobile/desktop remote-head daemon spawns its per-conversation claude through `internal/sessions`. Every spawn-argv is composed as `base + claudeSettingsArgs(settings)`, and `claudeSettingsArgs` only emits `--model` / `--effort` / `--dangerously-skip-permissions`. There is no `--settings` flag and no settings file anywhere under `internal/sessions`.

When the operator has any MCP server configured (user-level `~/.mcp.json` or a project `.mcp.json`), claude 2.1.199 renders its "N new MCP servers found in this project" enablement modal at startup. The PTY readiness check reports `tuidriver: unexpected dialog at startup`, and **every live turn fails**.

Agent-run already solved this (`d10ce87`) by writing a per-spawn settings file carrying `enableAllProjectMcpServers:true` and passing it via `--settings`. That fix never reached the session-pool spawn the phone and desktop drive. This ticket applies the same idea at the session-pool composition seam.

**All three spawn paths wedge, not just one.** Bootstrap (`Pool.New`), minted (`buildSession`), and the live settings-restart recompose (`Session.spawnArgs`, #842) all route through the same base. A single injection point covers all three.

## Design

### Where `--settings <path>` goes: into `spawnBase`, not `claudeSettingsArgs`

The MCP-enable settings file is **per-session immutable** — its content is pyry-fixed and never varies with Model/Effort/YOLO. That makes `spawnBase` its natural home:

- `spawnBase` is the "template args plus any construction-time resume suffix (`--session-id <id>`), but WITHOUT the `claudeSettingsArgs` suffix" (session.go:153-161).
- `spawnArgs(settings)` = `slices.Clone(spawnBase) + claudeSettingsArgs(settings)` (session.go:120-122). Because the live-restart recompose derives from `spawnBase`, a `--settings` flag placed there **survives every recompose automatically** (AC #3, live settings-restart).
- A backoff restart re-execs the supervisor with the same `ClaudeArgs` (which is `spawnBase`-derived at construction), so it survives too (AC #3, backoff restart).

Do **not** thread the path through `claudeSettingsArgs` — that function recomputes from `SessionSettings` on every settings change and carries no path. `spawnBase` is the one seam that reaches bootstrap + minted + restart. This is exactly the "shared composition seam" the Technical Notes point at, and it keeps the entire change inside `internal/sessions`.

### New writer: `internal/sessions/settings.go` (new file)

A minimal, interactive-specific writer. **Build, don't reuse** — the agent-run `settings` package cannot be reused without violating AC #2 (its struct is deny-default with a byte-order-load-bearing `Permissions` field and an always-stamped `defaultMode:"dontAsk"`). Duplicating ~15 lines is consistent with the project's "resist over-DRY on duplicated primitives" convention (PROJECT-MEMORY § atomic-write recipe).

Contract:

```
// writeMCPSettings creates an os.TempDir settings file containing only
// {"enableAllProjectMcpServers":true} and returns its absolute path.
func writeMCPSettings() (string, error)
```

- JSON shape (compact, `json.Encoder.Encode` trailing `\n`): `{"enableAllProjectMcpServers":true}`. **No `permissions` key at all** — satisfies AC #2 (no allow/deny, no `defaultMode`).
- Struct: a one-field `mcpSettingsFile{ EnableAllProjectMcpServers bool \`json:"enableAllProjectMcpServers"\` }` encoded with value `true`.
- Tempfile discipline mirrors agent-run: `os.CreateTemp("", "pyry-session-settings-*.json")`; on any post-create failure (`Encode`, `Close`) best-effort `os.Remove` the tempfile before returning the error, so callers never see a leaked path on the error path.
- Uses `os.TempDir` (not `dataDir()`) to stay self-contained and testable — `dataDir()` is empty in test mode.

### Per-session file + path field

Store the path on the session so it can be threaded into the base and removed at teardown. Add one immutable field to `Session` (session.go, in the `spawnBase` neighbourhood):

```
settingsPath string // absolute path to the per-session MCP-enable --settings file; removed at teardown
```

Set it in both construction sites next to `spawnBase`.

### Injection point #1 — bootstrap (`Pool.New`, pool.go:394-401)

Before composing `base`, write the file; append `--settings <path>` to the base:

- `settingsPath, err := writeMCPSettings()` → on error return `nil, fmt.Errorf("sessions: write mcp settings: %w", err)` (hard fail; see Error handling).
- `base := append(slices.Clone(cfg.Bootstrap.ClaudeArgs), "--settings", settingsPath)`
- The existing `bootstrapArgs := append(slices.Clone(base), claudeSettingsArgs(settings)...)` line is **unchanged** — it already clones `base` before the settings suffix.
- Set `settingsPath: settingsPath` on the bootstrap `Session` literal (pool.go:474-490).

### Injection point #2 — minted (`buildSession`, pool.go:1202-1270)

- `settingsPath, err := writeMCPSettings()` → on error return `nil, fmt.Errorf("sessions: write mcp settings: %w", err)` (buildSession already returns `(*Session, error)`).
- `base := append(base, "--settings", settingsPath)` (after the existing `--session-id` append at line 1208; the existing `args := append(slices.Clone(base), ...)` at 1209 stays unchanged and still protects `base`).
- Set `settingsPath: settingsPath` on the returned `Session` literal (pool.go:1250-1269).

### Data flow (unchanged shape, one new element in the base)

```
writeMCPSettings() ──> settingsPath ──┐
                                       ├─> spawnBase = [...template, (--session-id id,) --settings path]
cfg.Bootstrap.ClaudeArgs / tpl.ClaudeArgs ┘
                                       │
        ClaudeArgs (construction) = spawnBase + claudeSettingsArgs(settings)   ← bootstrap & minted
        spawnArgs (live restart)  = spawnBase + claudeSettingsArgs(settings)   ← #842 recompose
        backoff restart re-execs the supervisor's ClaudeArgs verbatim          ← survives
```

## Concurrency model

No new goroutines, no new locks. `settingsPath` is immutable post-construction (same discipline as `spawnBase`), so it is read without a lock — including from the lifecycle goroutine and from `Pool.Run`'s captured bootstrap reference. The write (`writeMCPSettings`) happens synchronously during `Pool.New` / `buildSession`, before the session is registered or supervised.

## Error handling

- **Write failure at construction is a hard error.** Both `Pool.New` and `buildSession` already return errors for construction failures (registry load, `supervisor.New`); a settings-write failure joins them. Rationale: a daemon that cannot write the file would otherwise start and silently wedge *every* turn on the MCP modal — a loud startup failure is strictly better than a silent per-turn wedge. `os.CreateTemp` failure (disk full / permissions) is rare.
- **Cleanup is best-effort.** `os.Remove` errors are ignored (`_ = os.Remove(...)`); a leaked tempfile is harmless. Cleanup runs *after* the child is confirmed dead so a backoff respawn can never race the removal:
  - **Minted:** in `Pool.Remove`, after `evictErr := sess.Evict(ctx)` returns (child terminated), add `if sess.settingsPath != "" { _ = os.Remove(sess.settingsPath) }` before the existing return branches. The return values are unchanged.
  - **Bootstrap:** the bootstrap is never `Remove`d (`ErrCannotRemoveBootstrap`); its file lives for the daemon-process lifetime and is removed when `Pool.Run` returns (ctx cancel / shutdown). Add a `defer` after the bootstrap capture (pool.go:1011): `defer func() { if bootstrap != nil && bootstrap.settingsPath != "" { _ = os.Remove(bootstrap.settingsPath) } }()`.
- **Accepted minor leaks** (both harmless, both rare, both benign — a leftover tempfile in `os.TempDir`):
  - `buildSession` succeeds (file written) but the caller (`GetOrCreate` / `Create`) fails before the session is registered → no `Remove` fires. Rare; a single leaked tempfile. Not worth threading cleanup into every caller error path (Simplicity First).
  - `Pool.New` succeeds but `Pool.Run` is never called (test-only misuse) → bootstrap file leaks. Production always calls `Run`.
  - Embedded ACP mode (`BootstrapEvicted`) writes a bootstrap file the bootstrap claude never spawns with; it is still removed on `Run` shutdown. Uniform path > conditional skip.

## Testing strategy

Add `internal/sessions/settings_test.go` (writer unit test) and extend the argv/cleanup coverage (a new `internal/sessions/pool_mcp_settings_test.go`, or fold into the existing `pool_settings_test.go` / `pool_bootstrap_sessionid_test.go`). Scenarios (bullet form — developer writes them in the table-driven stdlib idiom):

- **Writer shape:** `writeMCPSettings()` returns a readable path; the file's JSON unmarshals to `{"enableAllProjectMcpServers":true}`; assert the raw bytes contain `enableAllProjectMcpServers` set true **and** contain no `permissions` / `defaultMode` / `allow` / `deny` substrings (AC #2 — no permission posture change).
- **Bootstrap argv (AC #4):** construct a Pool via `New`; assert the bootstrap's spawn argv contains the adjacent pair `--settings <path>`, and that reading `<path>` yields JSON with `enableAllProjectMcpServers:true`. (Reuse the argv-inspection approach already used in `pool_bootstrap_sessionid_test.go`.)
- **Minted argv (AC #4):** call `buildSession`; assert the same for the minted session's argv. (Reuse the `buildSession` harness in `pool_settings_test.go:103`.)
- **Live-restart recompose survives (AC #3):** after `UpdateSettings` triggers a live restart, `sess.spawnArgs(merged)` still contains `--settings <same path>`, and the file still exists on disk. (Reuse `pool_update_settings_restart_test.go:106`.)
- **Minted cleanup (AC #3):** after `Pool.Remove`, the minted session's `settingsPath` file no longer exists. (Reuse `pool_remove_test.go` harness.)
- **Bootstrap cleanup (AC #3):** run a Pool briefly, cancel its context so `Pool.Run` returns, then assert the bootstrap's `settingsPath` file is gone.
- **Zero permission drift (AC #2):** confirm the interactive argv gains only `--settings <path>` and no `--permission-mode` / allow / deny flag relative to today's spawn.
- **Live gate (AC #5):** manual — `LIVE=1 DEVICE=connected scripts/e2e-emulator.sh` with an unapproved `~/.mcp.json` present reaches ready and delivers, no `tuidriver: unexpected dialog at startup`. Out of `go test`'s scope; called out for the operator's verification run.

## MCP-consent context (awareness, not a gate)

Auto-enabling project MCP servers mirrors the already-shipped agent-run decision (`d10ce87`) and applies to the operator's own pre-trusted workspace (#685) with operator-declared servers (`~/.mcp.json`). The settings content is pyry-fixed; no untrusted input is parsed. The MCP modal is a claude-CLI UX affordance, not a pyry security boundary. This is why the ticket is **not** `security-sensitive` despite the remote-head blast radius — the design (local-subprocess compat, fixed content) is what the label tracks, and the twin shipped un-gated.

## Open questions

- **Shared-vs-per-session file.** This spec uses one file per session (matches AC #3's "removed when the session is removed" wording 1:1). A single shared per-Pool file would also satisfy "survives respawn / no leak" with one cleanup point, but deviates from the AC's per-session removal framing. Per-session chosen for the direct AC mapping; revisit only if tempfile count ever becomes a concern (it won't at daemon session counts).
- **Cleanup-during-ctx-cancel race.** If `Pool.Remove`'s `Evict(ctx)` returns early on ctx cancel, the child may not be fully dead when the file is removed. Benign: on the unlikely respawn the child renders the modal once, and the session is being removed regardless. Documented as accepted.
