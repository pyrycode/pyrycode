---
ticket: 123
title: e2e harness StartRotation primitive wires fake-claude
status: spec
size: S
---

# Files to read first

- `internal/e2e/harness.go` — full file (~460 lines). The `Start`/`StartIn`/`spawn`/`childEnv`/`ensurePyryBuilt`/`Harness` shape that the new primitive mirrors and extends. Pay particular attention to:
  - L77-99 — `Harness` struct (you add one field)
  - L101-132 — `ensurePyryBuilt` pattern: `sync.Once` + `PYRY_E2E_BIN` env short-circuit. `ensureFakeClaudeBuilt` is a near-clone with a different env var name and a different `go build` target.
  - L237-269 — `spawn`'s body. This is what gets factored into `spawnWith` (or sibling `spawnRotation`); the existing two callers (`StartIn` L164, `StartExpectingFailureIn` L195) must continue to compile and behave identically.
  - L294-307 — `childEnv`. The new primitive layers the three `PYRY_FAKE_CLAUDE_*` vars on top of this.
- `internal/supervisor/supervisor.go:230-234` — `cmd.Env = append(os.Environ(), s.cfg.helperEnv...)`. The supervised child inherits pyry's env, which is how the harness-set `PYRY_FAKE_CLAUDE_*` vars reach fake-claude. No supervisor changes needed.
- `internal/e2e/internal/fakeclaude/main.go` — full file (~90 lines). Already landed (#122). The env-var contract this ticket wires up: `PYRY_FAKE_CLAUDE_SESSIONS_DIR`, `PYRY_FAKE_CLAUDE_INITIAL_UUID`, `PYRY_FAKE_CLAUDE_TRIGGER`. The binary's import path is `github.com/pyrycode/pyrycode/internal/e2e/internal/fakeclaude`.
- `docs/specs/architecture/122-fake-claude-test-binary.md` — sibling slice's spec. Section "End-to-end trajectory" describes the binary's externally-observable timeline; this ticket's test polls against the same trajectory but spawns it via pyry instead of `exec`'ing it directly.
- `cmd/pyry/main.go:174-180, 251-257` — confirms `-pyry-claude`, `-pyry-workdir`, `-pyry-idle-timeout` flag names. No changes; just the surface the new constructor pokes at.
- `docs/lessons.md` § "Claude session storage on disk" — encoded-cwd rule. **Not** load-bearing for this ticket: the harness hands fake-claude a sessions directory directly via env (`PYRY_FAKE_CLAUDE_SESSIONS_DIR`), so neither the harness nor the binary encodes a path. Read so you understand why we bypass the encoding entirely here — the next ticket (rotation watcher driver) will care, this one does not.

# Context

Today the e2e harness hardcodes the supervised child to `/bin/sleep 99999` (`harness.go:248-252`). That child opens no JSONLs, so production code that observes filesystem behaviour of the supervised child — notably `internal/sessions/rotation` — cannot be exercised end-to-end through pyry.

#122 landed the missing piece: a tiny `package main` test binary that opens a JSONL fd in a configured directory and rotates it on a trigger. This ticket wires that binary into the harness behind a new `StartRotation` constructor. It deliberately does **not** drive pyry's rotation watcher — that's the next consumer ticket. The split is "ship the primitive, then ship the consumer": when the consumer test fails it fails for one reason, not two.

# Design

## Approach

One new constructor (`StartRotation`), one new internal helper (`ensureFakeClaudeBuilt`), one shared spawn core (`spawnWith`) that the existing `spawn` becomes a thin wrapper over, and one new struct field (`Harness.ClaudeSessionsDir`). All inside `internal/e2e/harness.go`. Plus one new e2e test under `//go:build e2e` that exercises the primitive without touching pyry's session machinery.

The shared spawn core takes an options struct so the two call sites read symmetrically — `spawn` becomes `spawnWith(t, home, spawnOpts{}.defaults())`-shaped and `StartRotation` is `spawnWith(t, home, spawnOpts{claudeBin: fakeBin, extraEnv: rotEnv})`-shaped. No exported new types, no new packages.

## Public surface (delta only)

```go
// Harness gains one field. Empty for Start/StartIn; populated by StartRotation.
type Harness struct {
    SocketPath          string
    HomeDir             string
    ClaudeSessionsDir   string  // NEW. Empty unless created via StartRotation.
    PID                 int
    Stdout, Stderr      *bytes.Buffer
    // ... unchanged
}

// StartRotation builds pyry, builds the fake-claude binary, makes
// sessionsDir under home if it doesn't exist, and spawns pyry with
// fake-claude as the supervised child. The three PYRY_FAKE_CLAUDE_*
// env vars are set on pyry's process so the supervisor inherits them
// via os.Environ() and propagates them to the supervised child.
//
// home: the daemon's $HOME. Caller-owned (mirrors StartIn).
// sessionsDir: directory the fake-claude opens its <uuid>.jsonl under.
//   Created by StartRotation if missing. Recorded on h.ClaudeSessionsDir.
// initialUUID: stem for the first jsonl. Must satisfy uuidStemPattern
//   (consumer ticket cares; this ticket is opaque).
// trigger: filesystem path the test will create to trigger rotation.
//
// Idle eviction is left at the spawn default (-pyry-idle-timeout=0),
// matching Start/StartIn. Caller can pass extra flags via the variadic
// tail in a future iteration if needed; this slice keeps it fixed.
func StartRotation(t *testing.T, home, sessionsDir, initialUUID, trigger string) *Harness
```

`Start`, `StartIn`, and `StartExpectingFailureIn` keep their current signatures and behaviour. The refactor of `spawn` into `spawnWith` is purely internal.

## Internal shape

```go
// spawnOpts captures the per-test variations on top of pyry's standard
// e2e flag set. Zero-value gives the existing /bin/sleep 99999 behaviour
// so spawn() stays a one-liner over spawnWith.
type spawnOpts struct {
    claudeBin   string   // default "/bin/sleep"
    claudeArgs  []string // default {"99999"}
    extraEnv    []string // appended to childEnv(home); zero-value = none
    extraFlags  []string // existing extraFlags-passthrough channel
}

// spawnWith does the work the current spawn() does, parameterised on
// spawnOpts. spawn(t, home, extraFlags...) becomes a one-liner that
// applies the sleep-as-claude defaults.
func spawnWith(t *testing.T, home string, o spawnOpts) (string, *exec.Cmd, *bytes.Buffer, *bytes.Buffer, chan struct{})

// ensureFakeClaudeBuilt mirrors ensurePyryBuilt: sync.Once-cached
// `go build` into a tmp dir, with PYRY_E2E_FAKE_CLAUDE_BIN as the
// CI prebuild short-circuit.
func ensureFakeClaudeBuilt(t *testing.T) string
```

## Data flow

```
test                                                pyry process                       supervised child (fakeclaude)
─────────────────────────────────────────────────────────────────────────────────────────────────────────────────────
home := t.TempDir()
sessionsDir := home/.pyry-fakesessions
initialU := "11111111-1111-4111-8111-111111111111"
trigger := home/rotate.trigger

h := e2e.StartRotation(t, home, sessionsDir, initialU, trigger)
  │
  ├─ ensurePyryBuilt(t)        → pyryBin
  ├─ ensureFakeClaudeBuilt(t)  → fakeBin
  ├─ os.MkdirAll(sessionsDir, 0o700)
  └─ spawnWith(t, home, spawnOpts{
        claudeBin:  fakeBin,
        claudeArgs: nil,
        extraEnv:   {SESSIONS_DIR=…, INITIAL_UUID=…, TRIGGER=…},
     })
            │
            └── exec.Command(pyryBin,
                  -pyry-socket=…, -pyry-name=test,
                  -pyry-claude=fakeBin,
                  -pyry-workdir=home,
                  -pyry-idle-timeout=0)
                cmd.Env = childEnv(home) + extraEnv
                cmd.Start()
                              │
                              └─> pyry main → supervisor.runOnce
                                    cmd.Env = append(os.Environ(), helperEnv…)
                                              ↑ inherits PYRY_FAKE_CLAUDE_* from pyry
                                    pty.Start(fakeBin)
                                                          │
                                                          └─> fakeclaude:
                                                                open(sessionsDir/initialU.jsonl)
                                                                stat(trigger) loop …

waitForReady (existing) — control socket dialable
return *Harness with ClaudeSessionsDir = sessionsDir
```

## Files touched

- **MODIFY** `internal/e2e/harness.go`:
  - Add `ClaudeSessionsDir string` field on `Harness` (one line, comment one line).
  - Refactor body of `spawn` into new `spawnWith(t, home, spawnOpts)`. Existing `spawn(t, home, extraFlags...)` becomes a thin wrapper supplying the sleep-as-claude defaults. `StartExpectingFailureIn` and `StartIn` callers untouched.
  - Add `ensureFakeClaudeBuilt(t)` mirroring `ensurePyryBuilt(t)`. New `sync.Once` + cached `string` + cached `error` triple — keep the same naming convention (`fakeClaudeOnce`/`fakeClaudeBin`/`fakeClaudeErr`). Env short-circuit: `PYRY_E2E_FAKE_CLAUDE_BIN`.
  - Add `StartRotation(t, home, sessionsDir, initialUUID, trigger) *Harness`.

  Estimated delta: ~70-90 lines added/changed; existing call sites unchanged.

- **NEW** `internal/e2e/fakeclaude_test.go` — `//go:build e2e`. One test, ~80-100 lines. See "Testing strategy" below.

No changes to `cmd/pyry`, `internal/supervisor`, `internal/sessions`. The supervisor already propagates `os.Environ()` to its child (`supervisor.go:234`); that is the mechanism by which `PYRY_FAKE_CLAUDE_*` reaches fake-claude. **Do not** add a helperEnv knob.

# Concurrency model

Identical to today: harness uses one wait goroutine per spawned process to close `doneCh` on `cmd.Wait()` return. The new test's polling loops run on the test's main goroutine — no select-on-channel-vs-deadline gymnastics needed beyond the `case <-h.doneCh:` early-exit pattern already shown in `waitForReady`.

The fake-claude binary itself is single-threaded (see #122 spec). Pyry sees it as a normal PTY child; teardown via `Harness.teardown` SIGTERMs pyry, pyry's supervisor SIGTERMs the PTY child, the OS auto-closes any open jsonl fd.

# Error handling

Only one new error path worth calling out: `os.MkdirAll(sessionsDir, 0o700)` inside `StartRotation` — `t.Fatalf` on failure. Everything else reuses existing harness machinery (`ensure*Built` already `t.Fatalf`s; `spawnWith` `t.Fatalf`s on `cmd.Start` failure; readiness gate already handles "pyry exited before ready" via `doneCh`).

If pyry dies before ready because the fake-claude binary failed to open its initial jsonl (e.g. sessions dir not creatable, env var unset), the existing `waitForReady` returns `pyry exited before ready: <stderr>` — the supervisor logs the child's nonzero exit, which surfaces in pyry's stderr. That's enough; do not invent a fakeclaude-specific diagnostic path.

# Testing strategy

One new test, `TestE2E_StartRotation_PrimitiveWiresfakeclaude` (or shorter name — developer's call) under `//go:build e2e` in `internal/e2e/fakeclaude_test.go`.

Phases:

1. **Setup**. `home := t.TempDir()`, `sessionsDir := filepath.Join(home, "fakesessions")`, `initialUUID := "11111111-1111-4111-8111-111111111111"`, `trigger := filepath.Join(home, "rotate.trigger")`.
2. **`h := e2e.StartRotation(t, home, sessionsDir, initialUUID, trigger)`**. This implicitly verifies pyry came up — `waitForReady` is part of `StartRotation` (via `StartIn`-style) so a daemon failure surfaces as a `t.Fatalf` here.
3. **Assert `h.ClaudeSessionsDir == sessionsDir`** — the field is populated.
4. **Poll for initial jsonl**. `<sessionsDir>/<initialUUID>.jsonl` should appear. Use a 5s deadline + 50ms gap (matching harness conventions). Exit with `t.Fatalf` including the path and `h.Stderr` on miss.
5. **Drop trigger**. `os.WriteFile(trigger, nil, 0o600)`.
6. **Poll for rotation**. List `sessionsDir` for `*.jsonl`; succeed when there is a stem matching `^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$` that is not the initial. 5s deadline. Same `t.Fatalf` shape on miss.
7. **(Lightweight)** Assert the initial jsonl file is no longer being held open by checking `cmd.Wait()` hasn't fired (i.e. fake-claude is still alive) plus the rotated jsonl exists. The "no longer being written" property in the AC is verified indirectly: the rotated jsonl is non-empty (size > 0) implies fake-claude wrote the `{}\n` payload to the new fd, and #122's spec guarantees close-OLD-before-open-NEW order. Do not reach into `/proc/<pid>/fd` here — that's the next ticket's job.
8. **Teardown** is automatic via `t.Cleanup` registered inside `StartRotation`'s call to `StartIn`-equivalent path.

Hermeticity check: no network, no real `claude`, no API key, all writes under `t.TempDir()` (the harness's home). Default `go test ./...` does not compile this file (build tag).

What this test does **NOT** do:
- Touch `internal/sessions/rotation`.
- Assert anything about pyry's session registry (`<home>/.pyry/test/sessions.json`).
- Drive `Harness.Run` for any verb. The whole point is "did the wiring work" — readiness + jsonl observation is sufficient.

Existing e2e tests (`TestE2E_*` in `idle_test.go`, `restart_test.go`, etc.) stay green because `Start`/`StartIn` signatures and behaviour are unchanged.

# Open questions

- **Should `StartRotation` accept a variadic `extraFlags ...string` like `StartIn`?** Argument for: future caller in the next ticket might want `-pyry-idle-timeout=1s` to exercise eviction during rotation. Argument against: YAGNI; the consumer ticket can extend the signature when it needs it. **Recommendation:** leave it out; add when a concrete consumer asks. A future signature bump is cheap (one new caller).
- **Should `sessionsDir` be required to exist already (mirroring `StartIn`'s "directory must already exist" contract for `home`) or auto-created?** The AC implies the harness creates it (the test passes a path under `t.TempDir()` that doesn't yet exist). **Recommendation:** auto-create with `os.MkdirAll(sessionsDir, 0o700)` inside `StartRotation`. Matches the ergonomic norm for a test primitive; the only failure mode (parent dir unwritable) is already a setup bug.
- **Naming: `spawnWith` vs sibling `spawnRotation`.** Either works; ticket body explicitly accepts both. **Recommendation:** `spawnWith(t, home, spawnOpts{})`. Generalises cleanly when a third caller appears. Keep `spawn(t, home, extraFlags...)` as the thin wrapper so existing callers don't churn.

# Out of scope

- The fake-claude binary itself (#122, landed).
- Driving `internal/sessions/rotation`'s watcher end-to-end / asserting against pyry's session registry — next consumer ticket.
- Retiring `TestPool_Run_StartsWatcher`.
- Multi-rotation, configurable poll cadence, signal-driven trigger, or any other binary feature growth.
- Phase 1.2 `/clear` handling.
