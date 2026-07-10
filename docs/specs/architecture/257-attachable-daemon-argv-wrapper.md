# Spec #257 — e2e: migrate `spawnAttachableDaemon` to the argv-dropping wrapper

**Ticket:** [#257](https://github.com/pyrycode/pyrycode/issues/257) — *e2e: spawnAttachableDaemon tests reject `--session-id` — migrate to argv-dropping wrapper*
**Size:** S · **Security-sensitive:** no (test-only harness plumbing over the local control socket; no untrusted input, no security property under test)
**Decomposition:** mechanism 2 of #918 — the `os.Args[0]` test-binary stand-in. #918 (`/bin/sleep` default) and #929 (argv-immune fakeclaude) are the sibling mechanisms; both landed, no file overlap.

## Files to read first

- `internal/e2e/auto_attach.go:257-310` — `spawnAutoAttachDaemon`, the **line-for-line template**. Copy its args/env shape into `spawnAttachableDaemon`.
- `internal/e2e/auto_attach.go:103-127` — `echoClaudeScript` + `writeEchoClaude(t, home)`, the reusable shell wrapper. **Reuse verbatim — do not redefine.**
- `internal/e2e/auto_attach.go:67-101` — `safeBuffer` (`Write`/`String`/`Bytes`, mutex-guarded). The type AC#4 reuses for `StdioAttachClient.Stderr`. **Already exists — do not redefine.**
- `internal/e2e/attach_pty.go:151-199` — `spawnAttachableDaemon`, the function to migrate (Change 1). Its doc comment (151-155) also needs updating.
- `internal/e2e/attach_pty.go:66-95` — `StartAttach`, caller #1. Confirms the returned stdout buffer stays (signature is preserved; see Change 1).
- `internal/e2e/attach_stdio.go:27-54` — `StdioAttachClient` struct; `Stderr` field type change target is line 41 (Change 2).
- `internal/e2e/attach_stdio.go:98-132` — `startStdioAttach`, caller #2 (discards the stdout buffer via `_`); `Stderr` init (line 103) and the `attachCmd.Stderr = c.Stderr` assignment (line 132).
- `internal/e2e/attach_stdio_test.go:26-59` — the `t.Skip` to remove (line 34) and the stale rationale comment to clean (27-33) (Change 3).
- `internal/e2e/attach_stdio_no_pty_test.go:28-44` — the non-gating conditional unskip (line 31 carries a stale `#167` gate) (Change 4).
- `internal/sessions/pool.go:1208` — `base := append(slices.Clone(tpl.ClaudeArgs), "--session-id", string(id))`. The root cause: every built session (including the bootstrap session) gets `--session-id <uuid>` appended. Read-only context.

## Context

`spawnAttachableDaemon` (`attach_pty.go:156`) wires the supervised "claude" as `-pyry-claude=os.Args[0]` — the Go e2e test binary itself, unwrapped — and passes `-- -test.run=TestHelperProcess` so the binary re-execs into `TestHelperProcess` (echo mode).

When the daemon builds a session, `Pool.Create` appends `--session-id <uuid>` to the supervised claude's args (`pool.go:1208`). That suffix reaches the Go test framework's flag parser, which rejects `-session-id` and exits 2 **before** `TestHelperProcess` runs:

```
flag provided but not defined: -session-id
```

The daemon never reaches readiness; `waitDaemonReady` short-circuits on the early child exit and the test fails. This trips **every** test routing through `spawnAttachableDaemon`, because even the bootstrap session (`StartAttach(t, "")`) is built through the same `pool.go:1208` path — the `--session-id` suffix is unconditional, not "non-bootstrap only".

Currently **red on main** (via `StartAttach` → `spawnAttachableDaemon`):
`TestE2E_Attach_RoundTripsBytes`, `TestE2E_Attach_HandlesSIGWINCH`, `TestE2E_Attach_DetachesCleanly`, `TestE2E_Attach_SurvivesClaudeRestart`.

Currently **skipped** (via `startStdioAttach` → `spawnAttachableDaemon`): `TestE2E_AttachStdio_BytesRoundTrip` (`attach_stdio_test.go:34`).

The fix already exists in-tree: `spawnAutoAttachDaemon` (`auto_attach.go:269`), the already-migrated twin, swaps `os.Args[0]` for the `echoClaudeScript` shell wrapper. The wrapper `exec`s `$E2E_HELPER_BIN -test.run=TestHelperProcess` while **ignoring its own argv**, so the appended `--session-id <uuid>` is dropped before the Go binary parses flags. `spawnAttachableDaemon` needs the identical treatment. Both live in package `e2e`, so `writeEchoClaude` / `echoClaudeScript` / `safeBuffer` are directly reusable — this ticket adds **no new helper**.

## Design

Four changes, all within `internal/e2e`. Two production (`.go`) files, two test files. No new files, no new exported symbols.

### Change 1 — migrate `spawnAttachableDaemon` (`attach_pty.go:156-199`)

`spawnAutoAttachDaemon` (`auto_attach.go:269-309`) is the template. Apply its four deltas to `spawnAttachableDaemon`, **preserving the existing signature** `func(t, home) (string, *exec.Cmd, *bytes.Buffer, *bytes.Buffer, chan struct{})` so both callers stay untouched:

1. After `socket := filepath.Join(...)`, add `claudeBin := writeEchoClaude(t, home)`.
2. Change the claude arg from `"-pyry-claude=" + os.Args[0]` to `"-pyry-claude=" + claudeBin`.
3. **Drop** the trailing two args `"--", "-test.run=TestHelperProcess"`. The wrapper supplies `-test.run=TestHelperProcess` itself via its `exec` line.
4. Add `"E2E_HELPER_BIN="+os.Args[0]` to the `cmd.Env = append(childEnv(home), ...)` block (alongside the existing `GO_TEST_HELPER_PROCESS=1` / `GO_TEST_HELPER_MODE=echo`).

**Keep unchanged:** the `stdout := &bytes.Buffer{}` creation, `cmd.Stdout = stdout`, and returning `stdout` (caller #1 `StartAttach` stores it in `AttachHarness.daemonOut`; caller #2 `startStdioAttach` discards it via `_`). This is the one shape difference from `spawnAutoAttachDaemon`, which throws its stdout away — do **not** adopt that difference. Keep `-pyry-resume=false` (parity with the twin; the wrapper ignores argv anyway).

Resulting daemon args (the contract the wrapper migration produces):

```
-pyry-socket=<home>/pyry.sock
-pyry-name=test
-pyry-claude=<home>/echo-claude.sh   # was os.Args[0]
-pyry-idle-timeout=0
-pyry-workdir=<home>
-pyry-resume=false
                                     # "--" and "-test.run=TestHelperProcess" removed
```

Env passed to the daemon (flows through `supervisor.runOnce` → wrapper → `TestHelperProcess`):
`GO_TEST_HELPER_PROCESS=1`, `GO_TEST_HELPER_MODE=echo`, `E2E_HELPER_BIN=<os.Args[0]>`.

Update the doc comment (151-155): replace "the e2e test binary running TestHelperProcess … and no sleep sentinel" with a description of the wrapper indirection — the supervised claude is now `echoClaudeScript`, which drops `Pool.Create`'s appended `--session-id` before re-execing the test binary. Mirror the wording of `spawnAutoAttachDaemon`'s doc comment (`auto_attach.go:257-268`).

### Change 2 — guard `StdioAttachClient.Stderr` with `safeBuffer` (AC#4, `attach_stdio.go`)

Change the field type and its initializer; every read site is already compatible.

- Line 41: `Stderr *bytes.Buffer` → `Stderr *safeBuffer`.
- Line 103: `Stderr: &bytes.Buffer{}` → `Stderr: &safeBuffer{}`.
- Update the field doc comment (38-40) to note the mutex guard, mirroring `ForegroundAutoAttachClient.Stderr`'s comment (`auto_attach.go:43-47`).

No other edits: `attachCmd.Stderr = c.Stderr` (line 132) satisfies `io.Writer` because `*safeBuffer` implements `Write`; the three `c.Stderr.String()` sites (`attach_stdio.go:165`, `attach_stdio_test.go:49`, `attach_stdio_no_pty_test.go:42`) are unchanged because `*safeBuffer` implements `String`. The `bytes` import stays (still used by `daemonErr *bytes.Buffer`, `ReadUntil`, `bytes.Contains`).

### Change 3 — unskip `TestE2E_AttachStdio_BytesRoundTrip` (AC#3, `attach_stdio_test.go`)

- Remove the `t.Skip(...)` at line 34.
- Remove/rewrite the now-stale skip-rationale comment (27-33) — the blocker it describes (`#257`, the missing shell wrapper) is exactly what this ticket resolves. Keep the test's own behavioral doc comment (11-25).

Once Change 1 lands, `startStdioAttach`'s `control.SessionsNew` call (line 120) drives `Pool.Create` → appends `--session-id` → hits the wrapper → argv dropped → helper echoes normally. No change to `startStdioAttach` itself is needed.

### Change 4 (non-gating) — `TestE2E_AttachStdio_NoPTYInProcessTree` (`attach_stdio_no_pty_test.go`)

This test also routes through `startStdioAttach` → `spawnAttachableDaemon`, but is hard-skipped at line 31 with a **stale** `#167` reason (#167 landed — confirmed by `attach_stdio_test.go:27`'s own comment). The test already self-skips gracefully when fd-inspection is unavailable (`openPTYDeviceTargets` err → `t.Skipf`, lines 37-39), so the line-31 hard skip is the only thing keeping it dark.

After Changes 1-3, remove the line-31 `t.Skip` and run it under `-race -tags e2e`:
- **If it passes** (or cleanly self-skips on fd-inspection unavailability in the sandbox): leave it unskipped, drop the stale `#167` reference. Preferred outcome.
- **If it fails for a real reason** (not the stale gate): leave it skipped, but replace the reason with an accurate, non-stale one (do not cite `#167`).

Not a gating AC — flagged so the seam isn't left with a stale skip. Record the decision in the PR description.

### Build-tag note (the one subtlety — no action needed, do not relocate helpers)

`attach_pty.go` is tagged `//go:build e2e || e2e_install`; `writeEchoClaude` / `echoClaudeScript` live in `auto_attach.go`, tagged `//go:build e2e` only. Referencing the wrapper from `spawnAttachableDaemon` therefore makes `attach_pty.go` structurally depend on an `e2e`-only symbol.

**This is fine and already-established practice — do not move or retag anything.** `harness.go` (also `e2e || e2e_install`) already references `safeBuffer` (an `e2e`-only symbol from `auto_attach.go`). Verified on current `main`:

| Build tags | Result |
|---|---|
| `-tags e2e` (the AC mode) | compiles ✓ |
| `-tags "e2e e2e_install"` (install path) | compiles ✓ |
| `-tags e2e_install` (pure) | **already broken** on main — `harness.go: undefined: safeBuffer` |

Pure `e2e_install` is not a supported build mode (the e2e package is only ever built with `e2e` present — see the #918 lesson: *"internal/e2e never builds under pure e2e_install; use `-tags \"e2e e2e_install\"`"*). Both real modes include `auto_attach.go`, so `writeEchoClaude` resolves. Adding this reference changes nothing observable; it follows the `harness.go` → `safeBuffer` precedent exactly.

## Concurrency model

Relevant only to AC#4. `os/exec` launches a background copier goroutine (`io.Copy(cmd.Stderr, childStderrPipe)`) at `attachCmd.Start()` when `Stderr` is not an `*os.File`. That goroutine writes to `c.Stderr` for the child's lifetime. The test goroutine reads `c.Stderr.String()` in failure diagnostics (early-death detector at `attach_stdio.go:158-167`, and test-body fatals). With a bare `*bytes.Buffer`, an overlapping child-stderr write + diagnostic read is a data race the `-race` detector flags. `*safeBuffer` serializes `Write` and `String`/`Bytes` under a `sync.Mutex` — the same fix already applied to `ForegroundAutoAttachClient.Stderr` and `ForegroundSupervisedClient.Stderr`. No new synchronization is designed here; the change is a type swap onto an existing guarded buffer.

## Error handling

No new failure modes. The migration removes one (the `flag provided but not defined: -session-id` early exit) by construction. Existing harness error paths are unchanged: `waitDaemonReady` still short-circuits on early daemon exit; the 500ms settle-window early-death detectors in `StartAttach` / `startStdioAttach` still surface handshake failures with daemon+attach stderr. `writeEchoClaude` fatals the test on `os.WriteFile` failure (reused as-is).

## Testing strategy

No new tests are authored — this ticket turns five existing tests green and guards one field against `-race`.

- **AC#2 (four PTY tests) + AC#3 (stdio round-trip):**
  ```
  go test -race -tags e2e -run 'TestE2E_Attach_RoundTripsBytes|TestE2E_Attach_HandlesSIGWINCH|TestE2E_Attach_DetachesCleanly|TestE2E_Attach_SurvivesClaudeRestart|TestE2E_AttachStdio_BytesRoundTrip' ./internal/e2e/
  ```
  All five must pass. Then run the full `-race -tags e2e ./internal/e2e/...` once to confirm no collateral regression (the migration touches a shared spawn helper).
- **AC#1:** implied by the above going green — the supervised helper no longer exits 2 on `--session-id`. To confirm the mechanism directly, a failed run's daemon stderr should no longer contain `flag provided but not defined: -session-id`.
- **AC#4:** the `-race` flag on the stdio round-trip run is the assertion — the detector must report **no** data race on `StdioAttachClient.Stderr`.
- **Change 4:** run `TestE2E_AttachStdio_NoPTYInProcessTree` under `-race -tags e2e` after unskipping; decide per Change 4's branch.

Build-mode sanity (cheap, catches the tag subtlety early): `go vet -tags e2e ./internal/e2e/` and `go vet -tags "e2e e2e_install" ./internal/e2e/` should both stay clean. Do **not** expect pure `-tags e2e_install` to build (already broken on main; not a supported mode).

Environment caveats (from prior e2e runs): PTY-dependent tests self-skip when `/dev/ptmx` is unavailable; `realclaude` is not involved here. If a PTY test skips rather than passes in the sandbox, that is the harness's designed `pty.Open` skip, not a failure of this change — note it in the PR.

## Open questions

- **Change 4 outcome** — whether `TestE2E_AttachStdio_NoPTYInProcessTree` passes, self-skips, or needs a new accurate skip reason is resolved by running it (non-gating). No design decision pending; the developer records which branch was taken.
