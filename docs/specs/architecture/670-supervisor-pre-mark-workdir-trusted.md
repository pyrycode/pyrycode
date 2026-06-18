# #670 — Supervisor pre-marks its workdir trusted before spawning claude

**Size:** S (single function in `cmd/pyry/main.go`, ~5 production lines, no new imports/types; tests across `cmd/pyry` + `internal/e2e`).

## Files to read first

- `cmd/pyry/main.go:419-512` — `runSupervisor`: the daemon serve path (foreground + service mode). Flag parse (`*workdir` at 424), `claudeSessionsDir`/`defaultCwd` derivation (439-440), `sessions.New` with `Bootstrap.WorkDir = *workdir` (483-499). **This is the single edit site.**
- `cmd/pyry/agent_run.go:28` and `:288-323` — the `trustMark` package-var seam (`var trustMark = trust.MarkWorkdirTrusted`) and the established cmd-layer pattern in `runAgentRunPty`: `realpath, err := trustMark(parsed.workdir)` → `WorkDir: realpath`. **Mirror this exactly.**
- `cmd/pyry/agent_run_test.go:696-720, 822, 967` — how existing tests save/restore + override `trustMark` (the test seam to reuse for the new `cmd/pyry` test).
- `internal/agentrun/trust/trust.go:28-130` — `MarkWorkdirTrusted` contract: returns the resolved realpath, atomic temp+rename, errors on missing workdir / malformed-or-unreadable `~/.claude.json`, never logs file contents.
- `internal/agentrun/workdir.go:23-33` — `ResolveWorkdir` (Abs + `EvalSymlinks`, wraps `fs.ErrNotExist` on a missing path) — the source of the AC-4 missing-workdir error.
- `internal/agentrun/trust/trust_test.go` — existing coverage to **reuse** (mark write, symlink→realpath, missing-workdir error, malformed-JSON error). No new trust tests.
- `internal/supervisor/supervisor.go:636-641` — `runOnce`: `cmd.Dir = s.cfg.WorkDir`. The existing, faithful child-cwd threading — confirms the supervisor needs **no change**; passing realpath as `WorkDir` satisfies AC-2.
- `internal/sessions/pool.go:351-361` and `:423` — `supervisor.Config.WorkDir = cfg.Bootstrap.WorkDir` (bootstrap) and `sessionTpl: cfg.Bootstrap` (per-session template). Confirms realpath placed in `Bootstrap.WorkDir` reaches both the bootstrap supervisor and `buildSession`; no `internal/sessions` change needed.
- `internal/e2e/harness.go:185-201` (`Start`/`StartIn` — custom HOME + `extraFlags`) and `:371` (`StartExpectingFailureIn` — captures a startup-failure `RunResult`). The harness entry points for the AC-1 and AC-4 e2e tests; the daemon already runs with `HOME=t.TempDir()`.
- `docs/specs/architecture/470-agent-run-ptyrunner-cutover.md` (Security review section) and `docs/knowledge/codebase/470.md` — the "thread the realpath, not the raw path" gotcha and a security-review precedent to mirror.

## Context

**Problem.** The supervised claude (the daemon's long-lived interactive host) is spawned in `internal/supervisor` with `cmd.Dir = s.cfg.WorkDir` and **no trust pre-mark**. In a workdir claude has not yet trusted, claude renders its workspace-trust modal:

```
Quick safety check: Is this a project you created or one you trust?
❯ 1. Yes, I trust this folder
  2. No, exit
```

claude never reaches its input prompt, exits down the "No, exit" path, and the supervisor respawns it (`--continue`) into the same modal — a clean-exit restart loop (`claude exited cleanly` every ~2–90s) that never delivers a reply. Mobile `send_message` either races the restart or is typed into the modal and never commits.

**Confirmed (pyrycode-mobile#421, supervisor-side recording).** Manually setting `projects[<realpath>].hasTrustDialogAccepted = true` in `~/.claude.json` eliminated the loop entirely (zero exits) and the turn committed. **The trust modal is the whole cause; the pre-mark is the whole fix.**

**Precedent.** The agent-run path already solves this at the cmd layer: `cmd/pyry/agent_run.go`'s `runAgentRunPty` calls `trustMark(parsed.workdir)` and hands the returned realpath to `ptyrunner` as `WorkDir`. The daemon serve path (`runSupervisor`, same package) is the sibling that was never given the same pre-mark.

## Design

**One change, at the cmd/pyry layer.** Pre-mark the configured workdir trusted at the top of `runSupervisor`, before any workdir-derived value is computed, and thread the returned realpath into the bootstrap supervisor's `WorkDir`.

### Why cmd/pyry, not the supervisor or the pool

- **Established division of responsibility.** Trust-marking is a wiring-layer job here: `agent_run.go` marks at the cmd layer and hands `ptyrunner` a pre-resolved realpath; `ptyrunner` itself never pre-marks. The supervisor is the analogue of `ptyrunner` — it should receive a pre-resolved, pre-trusted realpath and stay unaware of `agentrun/trust`. `runSupervisor` is the daemon's `runAgentRunPty`.
- **Minimal blast radius.** `internal/supervisor` already does `cmd.Dir = s.cfg.WorkDir` (faithful, tested). Setting `WorkDir = realpath` upstream satisfies AC-2 with **zero supervisor changes** and **zero sessions changes**.
- **Zero new imports.** `cmd/pyry` already imports `internal/agentrun/trust` (via `agent_run.go`).
- **Single chokepoint covers every spawn.** Placing the realpath in `Bootstrap.WorkDir` flows it to the bootstrap supervisor (`pool.go:353`) **and** to `sessionTpl` (`pool.go:423` `sessionTpl: cfg.Bootstrap`), so any future `buildSession`/`--session-id` session inherits the same realpath. One mark, one realpath, all spawns. The bootstrap is exactly the supervisor the relay/mobile path drives (`main.go:512` `bootstrap.Supervisor()`).

### The change (contract sketch — not the implementation)

In `runSupervisor`, immediately after `*workdir` is parsed and before `resolveClaudeSessionsDir`/`resolveDefaultCwd`/`sessions.New`:

```go
realpath, err := trustMark(*workdir)        // reuse the agent_run.go:28 package var
if err != nil {
    return fmt.Errorf("mark workdir trusted in ~/.claude.json: %w", err)
}
```

Then set the bootstrap workdir to `realpath`:

```go
Bootstrap: sessions.SessionConfig{
    ...
    WorkDir: realpath,   // was *workdir
    ...
},
```

- `trustMark(*workdir)` resolves `*workdir` → realpath (Abs + `EvalSymlinks` inside `ResolveWorkdir`), writes `projects[realpath].hasTrustDialogAccepted = true` atomically, and returns the realpath. Empty `*workdir` resolves to the process cwd realpath (matching claude's behaviour today), so an unset `-pyry-workdir` still gets a trusted, explicit cwd.
- `cmd.Dir` in `runOnce` is unchanged code — it already does `cmd.Dir = s.cfg.WorkDir`, which is now the realpath. **AC-2 holds by construction:** the child's cwd is the exact realpath that was marked.

### Scope boundary: leave `resolveClaudeSessionsDir` / `resolveDefaultCwd` on the raw `*workdir`

`claudeSessionsDir` (line 439) and `defaultCwd` (line 440) keep their current `*workdir` argument. Threading realpath into `cmd.Dir` introduces **no new mismatch**: claude resolves both the raw path and the realpath to the *same* realpath when choosing its `~/.claude/projects/<encoded>/` JSONL dir, so claude's write location is identical before and after this change, and the existing relationship between `claudeSessionsDir = encode(Abs(*workdir))` and that location is untouched. (For a symlinked daemon workdir there is a *pre-existing*, unobserved `claudeSessionsDir` vs realpath gap; it is neither created nor worsened here — see Open questions.) Per Simplicity First + Evidence-Based, this change touches only what AC-2 requires.

## Error handling (AC-4): fail-fast at startup

`trustMark` returns a non-nil error when:
- the workdir does not exist (`ResolveWorkdir` → wrapped `fs.ErrNotExist`), or
- `~/.claude.json` is unreadable or malformed JSON (read/parse error), or `projects`/the target entry is a non-object.

`runSupervisor` returns the wrapped error. Because the mark runs **before** `config.Load`, `sessions.New`, and the claude-binary lookup, the daemon exits non-zero at startup **before** it ever spawns claude or claims readiness — rather than spinning a silent trust-modal restart loop.

- **Policy choice: fail-fast (not log-and-attempt).** Rationale: if the workdir cannot be trusted, *every* spawn wedges on the modal, so the daemon is non-functional; a loud startup error is strictly better than a silent loop. This matches the existing "malformed registry → fatal at startup" convention (PROJECT-MEMORY § Session Registry).
- **Accepted consequence:** a malformed *user* `~/.claude.json` blocks daemon startup. This is loud, operator-fixable, and the same failure the user's own interactive claude would hit — acceptable and consistent.
- **Error string discipline:** the wrap names the operation (`mark workdir trusted in ~/.claude.json`) and the workdir path; it MUST NOT include `~/.claude.json` *contents* (the file may carry tokens — `trust.go` already upholds this; the cmd-layer wrap must not undo it by interpolating file bytes).

## Concurrency model

No new goroutines, no new locks. The mark is a single synchronous call at startup, before the supervisor goroutine, control server, and relay are started. `MarkWorkdirTrusted` is best-effort single-writer with an atomic temp-file + rename (a crash mid-write cannot corrupt the user's `~/.claude.json`); a concurrent writer to `~/.claude.json` may produce a lost update — acceptable and unchanged from the agent-run path.

## Testing strategy

**Reuse (no new code):** `internal/agentrun/trust/trust_test.go` already covers the mark write (AC-1 mechanism), symlink→realpath resolution (AC-2 resolution), the missing-workdir error, and the malformed-JSON error (AC-4 mechanism). Do not duplicate these.

**New — `cmd/pyry` unit test (override `trustMark`, save/restore per `agent_run_test.go:702-712`):**
- *Fail-fast propagation (AC-4):* override `trustMark` to return an error; call `runSupervisor` with minimal args (e.g. `-pyry-workdir=…`, a bogus socket); assert it returns a non-nil error wrapping the trust failure, **before** any pool/spawn setup (the override returning early proves no spawn occurred). Confirms the "loud, not silent" contract.
- *Realpath is threaded, not the raw flag (AC-2):* override `trustMark` to return a sentinel realpath distinct from the input; assert `trustMark` was invoked with `*workdir`. Observing the sentinel reaching `Bootstrap.WorkDir` end-to-end is the e2e test below; at unit level, pin invocation + that the raw flag is *not* reused after the mark.

**New — `internal/e2e` test (real `pyry` binary via the harness; deterministic, CI-runnable under the `e2e` tag):**
- *AC-1, real serve path:* `StartIn(t, home, "-pyry-workdir="+freshDir)` where `freshDir` is a fresh `t.TempDir()` (never trusted). After the harness reports ready, read `filepath.Join(home, ".claude.json")` and assert `projects[<realpath(freshDir)>].hasTrustDialogAccepted == true`, where `realpath(freshDir) = filepath.EvalSymlinks(freshDir)`. The harness's default claude (`/bin/sleep infinity`) stays up, so readiness is reached and the mark must already be on disk → proves `runSupervisor` pre-marked the correct realpath key before the spawn loop. (Use the existing `Harness.HomeDir` field.)
- *AC-4, real serve path:* `StartExpectingFailureIn(t, home)` with **either** a pre-populated malformed `home/.claude.json` **or** a `-pyry-workdir=<nonexistent>` flag; assert the returned `RunResult` is a non-zero exit whose output names the trust-mark failure, and that no control socket became dialable (daemon never reached readiness).

**AC-3 (no restart loop) — evidence + note, not a mandated heavy test.** The "never-trusted workdir → claude reaches idle, no clean-exit loop" behaviour only manifests with a real claude that renders the modal; there is no daemon-serve `realclaude` harness today, and the `/bin/sleep` fake stays up regardless of trust. The ticket's manual confirmation (pre-marking → zero exits, turn committed) is the behavioural evidence; the deterministic CI proof is AC-1 (mark written at the correct key in the real serve path) + the reused `trust_test.go` keying. **Optional belt** (not required per Evidence-Based Fix Selection): a gating fake-claude (`-pyry-claude` pointing at a tiny script that exits 0 when the workdir is untrusted and `exec sleep infinity` when trusted) would make AC-3 deterministic in `internal/e2e`; document it if added, but do not build a new `realclaude` daemon harness for it.

## Acceptance criteria mapping

- **AC-1** (workdir pre-marked, `projects[<realpath>].hasTrustDialogAccepted = true`) — `trustMark(*workdir)` in `runSupervisor`; verified by the `internal/e2e` AC-1 test + reused `trust_test.go`.
- **AC-2** (child cwd == the marked realpath) — `Bootstrap.WorkDir = realpath` → `supervisor.Config.WorkDir` → existing `runOnce` `cmd.Dir = s.cfg.WorkDir`; verified by the cmd/pyry threading test + `trust_test.go` symlink resolution.
- **AC-3** (never-trusted workdir reaches idle, no clean-exit restart loop) — direct consequence of AC-1/AC-2; manual confirmation in the ticket + AC-1 deterministic proof; optional gating-fake belt.
- **AC-4** (pre-mark failure surfaces loudly, not a silent loop) — fail-fast return from `runSupervisor`; verified by the cmd/pyry fail-fast test + `internal/e2e` `StartExpectingFailureIn` test.

## Open questions

- **`claudeSessionsDir` realpath alignment for symlinked daemon workdirs.** `resolveClaudeSessionsDir` encodes `Abs(*workdir)` (no symlink resolution). For a *symlinked* daemon workdir this can diverge from claude's realpath-encoded JSONL dir, affecting #668's transcript resolver and the rotation watcher. This is **pre-existing**, unobserved (the confirmed scenario is a non-symlinked `/Users/...` path), and **not changed** by this ticket. Out of scope; file a follow-up only if a symlinked daemon workdir becomes a supported, exercised scenario.
- **Per-conversation per-workdir pre-marking.** If conversation-session work later gives each conversation its own workdir, the pre-mark must run per new workdir, not once for a single configured `WorkDir` (ticket forward note). That ticket moves/duplicates the seam; out of scope here.
- **Runtime trust-modal safety net on the bridge path.** Deferred by the ticket and reaffirmed here: ptyrunner *detects + aborts* (it does not *dismiss*) the modal, relying on a dispatcher retry the supervised host doesn't have; a bridge-path runtime net would be net-new and defends a post-pre-write failure mode the confirmed test showed eliminated. Per Evidence-Based Fix Selection, file a separate ticket only if the pre-mark proves insufficient in practice.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] No findings — the only input is the operator-controlled `-pyry-workdir` CLI flag, resolved through `EvalSymlinks` inside `trust.MarkWorkdirTrusted`. The pre-mark runs at startup *before* relay start, so no remote/phone input can influence the workdir or the marked entry; the marked `projects[realpath]` entry is the operator's own workdir in the operator's own `~/.claude.json` (operator intent, no privilege crossing). Mirrors #470's cmd-layer boundary.
- [Tokens, secrets, credentials] SHOULD FIX — `~/.claude.json` may carry tokens. The reused `MarkWorkdirTrusted` primitive preserves all sibling/extra fields + numeric precision and writes atomically (tested: `PreservesSiblingProjects`, `IdempotentPreservesExtraEntryFields`, `PreservesNumericPrecision`), so no token drop. The new cmd-layer error wrap is `fmt.Errorf("mark workdir trusted in ~/.claude.json: %w", err)` — content-free by construction; code-review must confirm the developer does not interpolate file *contents* into the error/log.
- [File operations] No findings — all paths are operator-owned (`~/.claude.json` via `UserHomeDir`; workdir via Abs+EvalSymlinks). Atomic temp+rename, 0o600 on create / mode-preserving on update. The TOCTOU/symlink-follow in the primitive operates on operator-owned paths only (not a remote vector) and is unchanged from #470/#475; symlink-following the workdir is the intended realpath resolution (AC-2).
- [Subprocess / external command execution] No findings — the realpath becomes `cmd.Dir` (not an argv element → no shell injection) and is a verified-existing directory; no `sh -c`, environment unchanged.
- [Cryptographic primitives] N/A — design writes a single boolean trust flag; no RNG/crypto surface.
- [Network & I/O] N/A — local filesystem + process spawn at startup only; the relay/WSS path is untouched and the mark runs before relay start.
- [Error messages, logs, telemetry] No findings — no new log line; `MarkWorkdirTrusted` is silent by contract. The startup error reaches the operator's own stderr/logs only (daemon exits before relay start → never reaches the phone) and names the operation + workdir path, not file contents.
- [Concurrency] No findings — no new goroutines/locks; synchronous pre-supervisor-goroutine call; atomic rename is signal-safe (SIGTERM mid-mark leaves old-or-new, never partial).
- [Threat model alignment] No findings — CLI/startup ticket; the mobile threat surface is unaffected because the pre-mark uses no remote input and runs pre-relay. The `security-sensitive` label correctly forced this audit (write to a possibly-token-bearing file on the relay-feeding daemon); the audit confirms the write is verbatim-preserving, atomic, content-free, and operator-input-only.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-06-18
