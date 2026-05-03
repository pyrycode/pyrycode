---
ticket: 120
title: e2e: /clear rotation watcher detects rotation end-to-end
status: spec
size: S
---

## Context

`internal/sessions/rotation` watches `~/.claude/projects/<encoded-cwd>/` for new
`<uuid>.jsonl` files appearing during a session's runtime, matches each CREATE
to a tracked PID via the platform probe (`/proc/<pid>/fd` on Linux, `lsof` on
macOS), and calls `Pool.RotateID` when the probe confirms claude has pivoted
to a fresh UUID — the on-disk shape of `/clear`.

The only test today (`internal/sessions/pool_test.go:785` —
`TestPool_Run_StartsWatcher`) substitutes the real probe with a `dirProbe` test
double. It exercises the watcher's event loop and `RotateID` plumbing but not
the real `/proc`-or-`lsof` probe path, and it has been observed flaky under
`-count=N` (2026-05-02).

This ticket adds a binary-level test that drives a real `pyry` daemon through
one rotation against the real probe. Production code is untouched; the harness
primitive `StartRotation` (delivered by #123) and the `fakeclaude` test binary
(delivered by #122) are consumed as-is.

## Files to read first

- `internal/sessions/pool.go:371-391` — `RotateID` semantics and error contract; the production path the watcher invokes via `OnRotate`. Save under `Pool.mu`, `lastActiveAt` advanced under `Session.lcMu`.
- `internal/sessions/rotation/watcher.go:142-188` — `handleCreate`: the exact-match probe gate at line 178-180 is why fake-claude must close OLD before opening NEW; also where `IsAllocated(stem)` short-circuits already-tracked ids (matters for the initial-jsonl CREATE event).
- `internal/sessions/rotation/watcher.go:21-24` — `probeRetryDelays = {0, 50ms, 200ms}`; ~250ms worst case before the gate fires. Sets the headroom for the 5s test deadline.
- `internal/sessions/reconcile.go:91-121` — `reconcileBootstrapOnNew` runs synchronously inside `Pool.New`; if `<initialUUID>.jsonl` is the most-recent jsonl in the watched dir at startup, the bootstrap session's id is rotated to `initialUUID` and persisted before the harness's readiness gate releases. The test's "poll until id == initialUUID" should be near-instant.
- `internal/sessions/reconcile.go:20-48` — `encodeWorkdir` (replaces BOTH `/` and `.` with `-`) and `DefaultClaudeSessionsDir`. Production helper is unexported; copy `encodeWorkdir` verbatim into the new test file as a one-liner. Don't reach into the package.
- `internal/e2e/harness.go:222-271` — `StartRotation(t, home, sessionsDir, initialUUID, trigger)`: builds pyry + fakeclaude, mkdirs `sessionsDir` 0o700, sets `-pyry-workdir=home`, env-injects the three `PYRY_FAKE_CLAUDE_*` vars, gates on socket readiness, registers teardown. Ready when it returns.
- `internal/e2e/internal/fakeclaude/main.go` — full file; close-OLD-before-open-NEW is in `main()` and the order is load-bearing. Reads only — don't change.
- `internal/e2e/restart_test.go:13-49` — `registryEntry`, `registryFile`, `newRegistryHome`, `readRegistry`, `mustReadFile`. Reuse all four; do not redefine.
- `internal/e2e/restart_test.go:211-275` — `TestE2E_Restart_LastActiveAtSurvives`: shows the "marshal/unmarshal trip strips monotonic clock state" pattern; relevant for any `time.Time` comparison the new test does on values read from disk.
- `cmd/pyry/main.go:92-108` — `resolveClaudeSessionsDir`: `home` from `-pyry-workdir` flows through `filepath.Abs` → `sessions.DefaultClaudeSessionsDir` → `<home>/.claude/projects/<encoded(home)>`. The test computes the same path locally and pre-creates `<initialUUID>.jsonl` there.
- `docs/lessons.md` § "Claude session storage on disk" — encoded-cwd rule (`/` AND `.` → `-`); `<encoded-cwd>/` directly contains the jsonls (no `sessions/` subdir).
- `docs/lessons.md` § fsnotify+symlink (line 197) — `EvalSymlinks` is applied watcher-side (#118 fix); on macOS `/var → /private/var` makes this load-bearing. Test does not need to compensate; the production fix already does.

## Acceptance Criteria (from ticket)

1. New test under `//go:build e2e` at `internal/e2e/rotation_test.go` named `TestE2E_RotationWatcher_DetectsClear`. Pre-create `<initialUUID>.jsonl` in the encoded sessions dir, call `StartRotation`, poll the registry until it reflects `initialUUID`, drop the trigger, then poll until the registry's tracked id changes — within a 5s deadline.
2. After rotation: assert the new id matches the canonical UUIDv4 stem pattern, `lastActiveAt` strictly advances past the pre-rotation snapshot, and a re-read after a brief sleep still shows the new id.
3. ~~Independent observation through `pyry list`.~~ **DEFERRED — see Open Question 1 below.**
4. Tests run only under `-tags=e2e`; default `go test ./...` is unaffected.
5. Hermetic: no network, no writes outside `HomeDir`, no real `claude`, no API key.

## Design

### Test layout

One file: `internal/e2e/rotation_test.go`. One test function:
`TestE2E_RotationWatcher_DetectsClear`. Build tag: `//go:build e2e` (same as
the other e2e tests in the package).

### Local helpers in this file

Two function-private helpers; nothing exported.

```go
// encodeWorkdir mirrors internal/sessions.encodeWorkdir (unexported).
// One-line copy: replace both '/' and '.' with '-'.
func encodeWorkdir(workdir string) string {
    if workdir == "" {
        return ""
    }
    r := strings.NewReplacer("/", "-", ".", "-")
    return r.Replace(workdir)
}

// uuidStemPattern matches the canonical 36-char lowercase UUIDv4 stem.
var uuidStemPattern = regexp.MustCompile(
    `^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
```

The `internal/sessions.encodeWorkdir` and
`internal/sessions/rotation.uuidStemPattern` are unexported; a one-liner local
copy is fine and is what the ticket explicitly recommends. Don't add an
exported accessor.

### Sequence

```
1.  home, regPath := newRegistryHome(t)               // restart_test.go helper
2.  sessionsDir := filepath.Join(home, ".claude",
                                 "projects",
                                 encodeWorkdir(home))
3.  os.MkdirAll(sessionsDir, 0o700)
4.  initialUUID := "11111111-1111-4111-8111-111111111111"
5.  initialJSONL := filepath.Join(sessionsDir, initialUUID+".jsonl")
6.  os.WriteFile(initialJSONL, []byte("{}\n"), 0o600)  // present BEFORE pyry starts
7.  trigger := filepath.Join(home, "rotate.trigger")
8.  h := StartRotation(t, home, sessionsDir, initialUUID, trigger)
                                                       // ↑ socket ready;
                                                       //   reconcile already
                                                       //   ran inside Pool.New
9.  pre := waitForBootstrapID(t, regPath, initialUUID, 5*time.Second)
10. os.WriteFile(trigger, nil, 0o600)                  // fake-claude rotates
11. post := waitForBootstrapIDChange(t, regPath, initialUUID, 5*time.Second)
12. assert uuidStemPattern.MatchString(post.ID)
13. assert post.LastActiveAt.After(pre.LastActiveAt)
14. time.Sleep(200 * time.Millisecond)
15. after := readBootstrap(t, regPath)
16. assert after.ID == post.ID                         // no path reverts it
17. _ = h                                              // teardown via t.Cleanup
```

The pre-rotation file (`initialJSONL`) is created **before** `StartRotation`
so `reconcileBootstrapOnNew` (synchronous inside `Pool.New`, before the
control socket starts listening) sees it as the most-recent jsonl and rotates
the bootstrap entry to `initialUUID` + persists. By the time
`StartRotation` returns, the registry on disk already reflects `initialUUID`
— the poll at step 9 should find it on the first read.

### Why pre-create instead of letting fake-claude do it

`fakeclaude` opens its initial jsonl during `main()`, but it races with
pyry startup. If reconcile runs before fake-claude's first write, it sees an
empty dir and the registry stays at the freshly-minted bootstrap UUID. The
test's pre-condition ("registry reflects initialUUID") becomes nondeterministic.

`fakeclaude` opens with `O_APPEND|O_CREATE`, so writing to an already-existing
file is harmless. Pre-creating the file removes the race.

### Helper functions (private to this test file)

```go
// waitForBootstrapID polls regPath until the bootstrap entry's id equals
// want, or deadline elapses. Returns the entry on match. The bootstrap is
// the entry with Bootstrap == true; there is exactly one.
func waitForBootstrapID(t *testing.T, regPath, want string, timeout time.Duration) registryEntry

// waitForBootstrapIDChange polls regPath until the bootstrap entry's id is
// non-empty and != avoidID, or deadline elapses.
func waitForBootstrapIDChange(t *testing.T, regPath, avoidID string, timeout time.Duration) registryEntry

// readBootstrap reads regPath and returns the bootstrap entry. Fatals on missing.
func readBootstrap(t *testing.T, regPath string) registryEntry
```

Poll cadence: 25ms (40 reads/s × 5s = 200 reads — well within budget; cheap
file reads). Each iteration calls `readRegistry` (exists in `restart_test.go`),
scans `Sessions` for `Bootstrap == true`, returns the entry. Use
`t.Helper()` so failure callsites are useful.

Do **not** poll the JSONL directory or the trigger file as a substitute for
polling the registry — the registry write is the production-side observable
this test exists to verify.

### Why the existing harness is enough

`StartRotation` already:

- builds pyry and fakeclaude
- mkdirs `sessionsDir` (we additionally pre-create the initial jsonl inside
  it before calling StartRotation)
- sets `-pyry-workdir=home` so production's `resolveClaudeSessionsDir`
  computes the same path the test computed locally
- env-injects `PYRY_FAKE_CLAUDE_{SESSIONS_DIR,INITIAL_UUID,TRIGGER}` so
  fakeclaude opens the right files
- gates on socket readiness (waitForReady)
- registers teardown via `t.Cleanup`

No new harness wiring needed.

## Concurrency

The test itself is single-threaded — sequential polling against the registry
file. Concurrency lives entirely in the production daemon under test:

- pyry's main goroutine + supervisor goroutine + control listener
- `Pool.Run` goroutine that drives the rotation watcher
- `rotation.Watcher.run` goroutine that consumes fsnotify events and calls
  `OnRotate` → `Pool.RotateID` → `saveLocked`

The test relies on `RotateID`'s existing guarantee: the `saveLocked` write is
synchronous within `RotateID`, and the registry file is renamed atomically.
The polling test sees either the old contents or the new contents — never a
torn read.

`os.WriteFile(trigger, ...)` is the only side-effect the test produces during
the run. fakeclaude's poll loop is 50ms; the trigger should be observed within
~50ms of the test's `os.WriteFile`. Combined with fsnotify (sub-50ms) + probe
retries (≤250ms) + saveLocked rename (<10ms), the rotation completes well
inside the 5s budget.

## Error handling

Each polling helper takes `(t *testing.T, ..., timeout)` and `t.Fatalf`s with
a useful diagnostic on timeout, including the latest registry contents
(via `mustReadFile`) so a CI failure is debuggable from the log alone.

`os.WriteFile` failures and `os.MkdirAll` failures `t.Fatal` immediately —
test setup, not production behaviour.

The brief sleep at step 14 (200ms) is intentionally fixed, not polled. The
assertion at step 16 is "the id has NOT changed back," which is a stable-state
property — polling for a non-event would require a timeout anyway. 200ms is
~2× the watcher's typical fsnotify-to-save latency; if a spurious second
rotation were going to happen, this is enough time for it to surface.

## Testing the test

Local manual verification before pushing:

```bash
go test -tags=e2e -run TestE2E_RotationWatcher_DetectsClear ./internal/e2e/...
go test -tags=e2e -run TestE2E_RotationWatcher_DetectsClear -count=10 ./internal/e2e/...
go test ./...   # default build must still pass; no e2e tag
```

The `-count=10` run is the "is it flaky?" check — this test exists in part to
replace `TestPool_Run_StartsWatcher`'s flakiness, so the e2e replacement must
itself be stable across multiple invocations before any retirement work.
Retirement of `TestPool_Run_StartsWatcher` is **out of scope** here (separate
follow-up).

## Open questions

### 1. AC#3 (`pyry list`) — verb does not exist

The ticket body's third acceptance criterion calls
`h.Run(t, "list")` and asserts on stdout containing the post-rotation UUID
(and not the initial UUID). It also says "no production-code change in this
ticket."

`cmd/pyry/main.go:142-160` dispatches `version`, `status`, `stop`, `logs`,
`attach`, `install-service`, `help` — there is no `list` verb. `pyry sessions
list` is tracked in OPEN tickets #87 (control verb) and #88 (CLI verb); the
predecessor #61 closed but the verb appears not to have shipped under the
single-word `list` form the ticket assumes. None of the existing verbs
enumerate sessions: `status` returns the supervisor's `ChildPID` /
`RestartCount`, not the session id; `attach` opens a PTY round-trip and is too
heavy for an assertion; `logs` returns the in-memory ring buffer.

There is no way to satisfy AC#3 without either:

- (a) **adding a `list` verb** (production change — explicitly forbidden by
  the ticket); or
- (b) **promoting the `pyry sessions list` work** (#87 + #88) to a blocking
  prerequisite for #120; or
- (c) **dropping AC#3** and relying on the on-disk registry assertions in
  AC#1/#2 (which already prove the production rotation path: fsnotify CREATE
  → real probe → OnRotate → RotateID → saveLocked → file on disk).

Recommended resolution: **(c) — drop AC#3 from this ticket.** The on-disk
registry observations already exercise every link in the chain end-to-end
against the real probe; the "control plane re-check" was an independent
observation, not an additional production property. The control-plane re-check
naturally lives in the e2e test for `pyry sessions list` itself once that
ships.

The developer should implement AC#1, AC#2, AC#4, AC#5; skip AC#3 with a
one-line comment in the test file pointing at this spec section. PO should
either ack-and-update the ticket body or override.

### 2. Trigger file location

Spec puts the trigger at `<home>/rotate.trigger`. fakeclaude polls it with
`os.Stat` every 50ms (`pollInterval` in `internal/e2e/internal/fakeclaude/main.go`).
The trigger MUST be outside the watched `sessionsDir` — a CREATE event for a
non-`.jsonl` file inside the watched dir is filtered out by
`handleCreate`'s suffix check (line 146-148), so it would not be incorrect,
just noisy. Keeping it under `home` (sibling of `.claude/`) is cleaner.

### 3. macOS sun_path — already handled

`newRegistryHome` already uses `os.MkdirTemp("", "pyry-rs-*")` instead of
`t.TempDir()` to keep `<home>/pyry.sock` under macOS's 104-byte `sun_path`
limit. The new test reuses this helper, so no new exposure.

## Failure modes

| Scenario                                                | Effect on test                                                     |
|---------------------------------------------------------|--------------------------------------------------------------------|
| Reconcile doesn't pick up `initialUUID.jsonl`           | `waitForBootstrapID` times out at step 9; diagnostic prints registry |
| Watcher misses the CREATE event for the new jsonl       | `waitForBootstrapIDChange` times out at step 11                     |
| Probe returns wrong path (symlink resolution regression)| Watcher's exact-match gate drops the rotation; step 11 times out    |
| `RotateID` errors (e.g. session not found)              | Watcher logs `OnRotate failed`; step 11 times out                   |
| fakeclaude doesn't observe the trigger                  | Step 11 times out; check daemon stderr for fakeclaude logs          |
| Stray second rotation (background path reverts)         | Step 16 fails with `id reverted` and registry contents               |

Every failure mode surfaces as a deterministic timeout with diagnostic output
— no silent passes possible.

## Out of scope

- Implementation or wiring of `pyry list` / `pyry sessions list` (#87, #88).
- Retirement of `TestPool_Run_StartsWatcher` (follow-up; needs CI stability data).
- Multi-session rotation semantics.
- Watcher behaviour when claude is not running (covered by unit tests).
- Any change to production code (watcher, probe, reconcile, registry, main).

## Definition of Done

- [ ] `internal/e2e/rotation_test.go` exists with `TestE2E_RotationWatcher_DetectsClear` under `//go:build e2e`.
- [ ] Test pre-creates `<initialUUID>.jsonl` in the encoded sessions dir, calls `StartRotation`, polls the registry to confirm it reflects `initialUUID`, drops the trigger, polls until the bootstrap id changes within 5s, asserts UUID stem + `lastActiveAt` advance + post-sleep stability.
- [ ] No `pyry list` invocation (AC#3 deferred per Open Question 1).
- [ ] `go test ./...` passes with no e2e tag (build-tag isolation works).
- [ ] `go test -tags=e2e -run TestE2E_RotationWatcher_DetectsClear -count=10 ./internal/e2e/...` passes.
- [ ] No changes anywhere outside `internal/e2e/rotation_test.go` and this spec.
