# 342 — `pyry agent-run`: invoke `MarkWorkdirTrusted` after flag parse

## Files to read first

- `cmd/pyry/agent_run.go:177-188` — `runAgentRun` body. The new wiring slots **between** the `parseAgentRunArgs` success and the `agentrun.WriteSettings` call. Mark trust first → settings second → print marker. Rationale in § "Ordering" below.
- `cmd/pyry/agent_run.go:84-92` — `agentRunArgs.workdir` is the trimmed, existence-validated absolute-or-relative path. Pass it through verbatim; `MarkWorkdirTrusted` calls `ResolveWorkdir` internally (which does `filepath.Abs` + `filepath.EvalSymlinks`).
- `internal/agentrun/trust.go:38-44` — `MarkWorkdirTrusted(homeDir, workdir)` signature + invariants (idempotent, atomic on-disk, file-locked, never logs file contents). Note `homeDir` is *explicit* — the caller resolves `os.UserHomeDir()` and passes it in.
- `cmd/pyry/main.go:85-90` — `resolveSocketPath` is the canonical `os.UserHomeDir()` call site in this package. Match its `err != nil || home == ""` posture? **No** — see § "Error contract" below; we treat `UserHomeDir()` failure as fatal, not fall-through, because mark-trust has no sensible fallback.
- `cmd/pyry/main.go:1203-1210` — `runInstallService` is the closest precedent: it calls `os.UserHomeDir()` and returns `fmt.Errorf("install-service: home dir: %w", err)` on failure. Mirror this exact shape (`agent-run: `-prefixed wrap, no fall-through).
- `cmd/pyry/agent_run_test.go:23-55` — `newValidArgsFixture` is shared by every `runAgentRun` test. The HOME-redirection (`t.Setenv("HOME", t.TempDir())`) belongs here so every test that calls `runAgentRun` gets HOME isolation for free — see § "Testing".
- `cmd/pyry/agent_run_test.go:283-320` — `TestRunAgentRun_EmitsSettingsFile`. This existing test calls `runAgentRun`; once #342 lands, `runAgentRun` writes to `~/.claude.json`. Without the fixture change, the test would mutate the developer's real `~/.claude.json`. **This is a correctness requirement, not a stylistic one.**
- Sibling spec `docs/specs/architecture/341-agentrun-trust-helper.md` § "`MarkWorkdirTrusted` — body shape" — the helper's read-modify-write contract (file lock, json.Number, atomic rename). #342 is the first consumer; nothing in this spec re-specifies that contract.
- Sibling spec `docs/specs/architecture/339-agent-run-settings-file.md` § "Context" — confirms `agent-run` runs as the per-spawn wrapper that #332 will exec. The dispatcher's stdout-scrape contract is the `settings-file: <path>` marker line; that contract must remain intact after #342.

## Context

`pyry agent-run` will, in #332, spawn a supervised `claude` headlessly. Without `projects[<realpath(workdir)>].hasTrustDialogAccepted = true` pre-written into `~/.claude.json`, that supervised claude blocks at startup on the workspace-trust TUI dialog — fragile under PTY timing. Spike #329 established the side-step; #341 shipped the helper (`internal/agentrun.MarkWorkdirTrusted`) with full lock + atomic-write semantics. #342 is the consumer wire-up: one call site in `cmd/pyry/agent_run.go`, one error path, one integration test.

The wire-up is intentionally separated from #341 so the helper's design and tests stand alone, and so the JSONL watcher (#333) can consume `ResolveWorkdir` from the same package without waiting on this verb-side glue.

## Design

### Wiring change in `runAgentRun`

After `parseAgentRunArgs` returns successfully and before `agentrun.WriteSettings` is called, the body resolves `homeDir` via `os.UserHomeDir()` and invokes `agentrun.MarkWorkdirTrusted(homeDir, parsed.workdir)`. On either error, return early with an `agent-run: <operation>: %w`-wrapped error; `main.run`'s top-level printer prepends `pyry: ` and exits non-zero.

Pseudocode (do NOT copy-paste verbatim; treat as contract sketch):

- Call `os.UserHomeDir()`. On error → `return fmt.Errorf("agent-run: resolving home directory: %w", err)`.
- Call `agentrun.MarkWorkdirTrusted(home, parsed.workdir)`. On error → `return fmt.Errorf("agent-run: pre-populating workspace trust: %w", err)`.
- Fall through to the existing `agentrun.WriteSettings` call. No other change.

Net diff: ~6 lines added in one function. No new exported types, no new files, no helper extraction. The two error wraps are distinct so the stderr line names which operation failed (per AC: "naming the operation").

### Ordering: `MarkWorkdirTrusted` BEFORE `WriteSettings`

Both writes are atomic in isolation, but they have asymmetric blast radius:

- `MarkWorkdirTrusted` modifies a shared file (`~/.claude.json`) under a file lock; an idempotent re-run is cheap and correct.
- `WriteSettings` creates a per-spawn file inside `workdir`; if mark-trust fails after settings is written, that settings file is stale per-spawn cruft inside the user's workdir.

Mark-trust first means a mark-trust failure (e.g. `~/.claude.json` permission error, lock contention timeout, parse error on a hand-edited file) short-circuits before any per-spawn artefact lands in `workdir`. This satisfies the AC's "No partial state writes are attempted after a failed mark" rule directly.

`MarkWorkdirTrusted` is idempotent, so re-running `agent-run` after a transient failure does not duplicate state in `~/.claude.json`.

### Error contract

| Failure | Wrap | Renders as |
|---|---|---|
| `os.UserHomeDir()` non-nil error | `fmt.Errorf("agent-run: resolving home directory: %w", err)` | `pyry: agent-run: resolving home directory: <err>` |
| `MarkWorkdirTrusted` non-nil error | `fmt.Errorf("agent-run: pre-populating workspace trust: %w", err)` | `pyry: agent-run: pre-populating workspace trust: agentrun: <op>: <err>` |

The `pyry: ` prefix is supplied by `main.main`'s `fmt.Fprintln(os.Stderr, "pyry:", err)` line. The double `agent-run:` / `agentrun:` is not a duplication — the first is the verb namespace, the second is the helper package's wrap-prefix introduced in #341. Mirrors how `install-service` errors render today (`pyry: install-service: home dir: <err>`).

**Do not fall through to a CWD-relative `home == ""` path** the way `resolveSocketPath` / `resolveRegistryPath` do. Those functions degrade gracefully because they only need a writable path; `MarkWorkdirTrusted` needs the *actual* `~/.claude.json` claude itself reads. A guessed-empty `homeDir` would write `./.claude.json` and the trust mark would have no effect at spawn time. Fail loud instead.

### Helper invocation count

Exactly one `MarkWorkdirTrusted` call per `runAgentRun` invocation. No retries on transient errors at this layer — the helper's `flock(LOCK_EX)` blocks until the lock is acquired, so "lock contention" surfaces only on a genuine deadlock / interrupt path, which is correct to fail.

### Out of scope (do NOT touch)

- No new flags. No `--skip-mark-trust` escape hatch. If a future ticket needs that, it will be a separate spec.
- No realpath / lock / atomic-write logic at the call site. If `MarkWorkdirTrusted`'s signature turns out to be wrong (e.g. callers need a context for cancellation), open a follow-up against `internal/agentrun` or route back via `needs-rework:architect`. Do NOT inline.
- No log line. Successful trust-mark is silent; the only stdout contract remains the `settings-file: <path>` marker from #339.
- `os.UserHomeDir()` is called exactly once. Do NOT cache it via a package-level var.

## Testing

### Fixture update (shared)

In `cmd/pyry/agent_run_test.go`, `newValidArgsFixture` MUST be updated to set `t.Setenv("HOME", t.TempDir())` before constructing the fixture (or as the first action of the helper). Rationale: every test that already calls `runAgentRun` (today: `TestRunAgentRun_EmitsSettingsFile`) will, post-#342, call `os.UserHomeDir()` → `MarkWorkdirTrusted` and would otherwise mutate the developer's real `~/.claude.json`. This is a correctness fix, not a stylistic one.

Concretely:

- Add `t.Setenv("HOME", t.TempDir())` as the first statement of `newValidArgsFixture` (before `t.TempDir()` is called for the prompt/system/work dirs, or as a separate call — `t.TempDir()` is fine to call multiple times in a single test).
- Tests that exercise *only* `parseAgentRunArgs` (the table tests in `TestParseAgentRunArgs_*`) are unaffected by this change in behaviour but harmless to share the fixture with — they ignore HOME entirely. The `t.Setenv` call adds maybe a microsecond per test; not material.

### New integration test

Add one test in `cmd/pyry/agent_run_test.go`. Name suggestion: `TestRunAgentRun_MarksWorkdirTrusted`. Scenarios (bullet form — developer writes the Go in the project's testing idiom):

- Build a `validArgsFixture` (so HOME is redirected to a temp dir via the fixture update above).
- Invoke `runAgentRun(fx.argv)` and require `err == nil`. Stdout is captured-or-discarded; the marker-line assertion belongs to `TestRunAgentRun_EmitsSettingsFile`, no need to duplicate.
- Compute `homeDir := os.Getenv("HOME")` (or pull it back out of the fixture if you cache it there).
- Assert `<homeDir>/.claude.json` exists and is a regular file.
- Decode the file as `map[string]any` (use `json.NewDecoder(...).UseNumber()` to mirror the helper's read shape; otherwise plain `json.Unmarshal` is fine for the asserted boolean).
- Resolve the expected key: `wantKey, err := filepath.EvalSymlinks(fx.workdir)` (the fixture's workdir is created under `t.TempDir()`; on macOS that lives under `/var/folders/...` which symlinks through `/private/var/...`, so `EvalSymlinks` is required to match what `MarkWorkdirTrusted` writes).
- Assert `root["projects"]` is an object, `root["projects"][wantKey]` is an object, and that object's `"hasTrustDialogAccepted"` is the boolean `true`.

The test asserts the *observable contract* (file path + key + bool field). It does NOT re-test the helper's lock semantics, the atomic-rename, json.Number preservation, or idempotency under concurrent writers — those are covered by `internal/agentrun/trust_test.go` per #341.

### Regression coverage (no diff expected)

- `TestParseAgentRunArgs_HappyPath`, `TestParseAgentRunArgs_Errors`, `TestParseAgentRunArgs_EffortValidValues`, `TestParseAgentRunArgs_AllowedToolsForms`, `TestSplitAllowedTools` — must pass unchanged. They exercise `parseAgentRunArgs` / `splitAllowedTools`, which #342 does not touch.
- `TestRunAgentRun_EmitsSettingsFile` — must continue to pass. Its marker-line and settings-file-content assertions are unchanged by #342. The HOME redirection via the fixture update is the only delta.

### Test isolation note

`t.Setenv` and `t.TempDir()` together give full HOME isolation per-test. `t.Setenv` precludes `t.Parallel()` on tests that touch it; the existing `cmd/pyry` tests are not parallel (none call `t.Parallel`), so this is not a regression.

## Open questions

None. The wire-up has one shape; the helper's signature already accommodates it (explicit `homeDir` parameter, set by #341 precisely to keep tests hermetic and the call site obvious).

## Implementation size estimate

- `cmd/pyry/agent_run.go`: ~6 net production lines (one `os.UserHomeDir()` call + error wrap + one `MarkWorkdirTrusted` call + error wrap).
- `cmd/pyry/agent_run_test.go`: ~1 line in `newValidArgsFixture` (the `t.Setenv` call) + one new `TestRunAgentRun_MarksWorkdirTrusted` test (~30 lines including helper boilerplate for `json` decode and `filepath.EvalSymlinks`).

XS. Comfortably under every red line.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No MUST FIX. `parsed.workdir` is user-controlled CLI input that crosses into "trusted key in `~/.claude.json`" via `ResolveWorkdir`. The boundary is single-function and explicit. Map-key shape is JSON-escaped by `encoding/json`; no injection. The write target is `<homeDir>/.claude.json` — the *invoking user's own* state, no cross-user privilege.
- **[Tokens/secrets]** No findings. The wire-up handles no secrets. The helper's "MUST NOT log file contents at any layer" invariant (`internal/agentrun/trust.go:5-8`) is preserved — the spec explicitly mandates "successful trust-mark is silent".
- **[File operations]** SHOULD NOTE — TOCTOU between `requireDir` (parse-time `os.Stat`) and `MarkWorkdirTrusted` (helper-time `EvalSymlinks`). The swap window exists. Impact analysis: an attacker who can swap the workdir between `os.Stat` and `EvalSymlinks` causes a different path to be written as the trusted key in `~/.claude.json`. The spawned `claude` (#332) resolves its own `--workdir` independently; a mismatch surfaces as the trust-dialog reappearing, NOT as a privilege bypass. The deny-default settings file (#339) governs capability scope, not the trust mark. **Not exploitable.** No spec change required; documented here so code-review can confirm the analysis.
- **[Permissions / atomic write / symlink handling]** No findings — fully delegated to the helper per #341, which uses preserve-existing-mode (default 0o600), `flock(LOCK_EX)`, and atomic `os.Rename`.
- **[Subprocess execution]** N/A — this ticket does not exec anything. `claude`-spawn lives in #332.
- **[Cryptographic primitives]** N/A — no crypto in this wire-up.
- **[Network & I/O]** N/A — no network surface; file I/O is encapsulated in the helper.
- **[Error messages / logs]** No findings. The two error wraps (`agent-run: resolving home directory: %w` and `agent-run: pre-populating workspace trust: %w`) surface file paths from the underlying errors but never file contents. `<homeDir>` is not sensitive in a local-CLI context; the helper's wraps already avoid leaking decoded JSON.
- **[Concurrency]** No findings. The helper owns its own `syscall.Flock` spanning the read-modify-write window (#341). Two concurrent `pyry agent-run` invocations serialize on the lock and write the same idempotent boolean. The wire-up introduces no new goroutines or shared state. SIGINT during the helper call is handled by the helper's `defer` unwind + atomic-rename semantics — partial state is structurally impossible.
- **[Threat model alignment]** No findings. Pre-marking `hasTrustDialogAccepted = true` is functionally equivalent to the user accepting the workspace-trust TUI dialog. Capability scope for the supervised claude is enforced by `--settings <path>` (#332) with the deny-default permission set (#339), orthogonal to the trust state. No new attack surface introduced.

**Reviewer:** architect (self-review per `agents/architect/security-review.md`)
**Date:** 2026-05-14
