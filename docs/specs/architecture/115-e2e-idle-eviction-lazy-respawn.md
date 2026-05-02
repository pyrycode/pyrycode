---
ticket: 115
title: Multi-session lifecycle e2e — idle eviction + lazy respawn
status: spec
size: S
---

# Files to read first

Read these before doing any exploration of your own. They're the load-bearing surfaces this spec composes; reading them up front is cheaper than rediscovering them via grep.

- `internal/e2e/harness.go` — full file; `spawn`, `StartIn`, constants (`readyDeadline`, `readyPollGap`), package doc. The variadic-flags extension lives here.
- `internal/e2e/restart_test.go:13-49,117-148` — `registryFile`/`registryEntry` mirror types, `newRegistryHome` / `writeRegistry` / `readRegistry` / `mustReadFile` helpers. Reuse verbatim — do not duplicate, do not promote to harness.go.
- `internal/sessions/pool.go:264-352` — `New`'s warm-vs-cold init. Notice that with no pre-populated registry the bootstrap starts in `stateActive` (default), so `runActive`'s idle timer arms shortly after pyry boots.
- `internal/sessions/pool.go:980-1000` — `saveLocked`'s `LifecycleState` write rule. `if state == stateEvicted` writes `"evicted"`; active is omitted (`omitempty`). Tests assert on the **string `"evicted"`**, not on the field's presence in the active case.
- `internal/sessions/session.go:155-213` — `Activate` / `Evict` semantics. Activate returns when `activeCh` is closed by `transitionTo`, which fires **before** `pool.persist()`. Test polls for the persist completing, not for Activate returning.
- `internal/control/protocol.go` — `Request`, `Response`, `Verb`, `AttachPayload`. Test 2 imports these directly to issue a raw `VerbAttach`.
- `internal/control/server.go:347-410` — `handleAttach`. The Activate-before-Attach call site this test exercises end-to-end.
- `cmd/pyry/main.go:255-295` — flag parsing; `-pyry-idle-timeout` flag exists already (default `15m`), so no CLI surface change is needed — only the harness extra-args hook.
- `cmd/pyry/main.go:81-89` — `resolveRegistryPath` (`~/.pyry/<name>/sessions.json`). The harness uses `-pyry-name=test` so the registry is at `<HomeDir>/.pyry/test/sessions.json`.
- `docs/specs/architecture/40-idle-eviction-lazy-respawn.md` — full spec. The state machine, idle-timer rearm-while-attached behaviour, and the `omitempty` rule on `lifecycle_state` are all defined there.

# Context

Idle eviction and lazy respawn shipped in #40, exercised today only by package-level integration tests in `internal/sessions/`. Those tests build `Pool` in-process with stub bridges; they do not run the assembled `pyry` binary, do not exercise the control plane's `handleAttach` Activate-before-Attach call, and do not observe the on-disk registry under daemon ownership.

This ticket adds binary-boundary e2e coverage: a real `pyry` daemon, the real `internal/sessions` lifecycle goroutine, the real `internal/control` server, the real on-disk `sessions.json`. The supervised "claude" remains `/bin/sleep infinity` (per #68) — claude's own behaviour is not what this test is about.

`pyry` already exposes `-pyry-idle-timeout` (`cmd/pyry/main.go:257`); the only harness-side gap is the lack of a way to override the default `=0` baked into `spawn`. The minimal hook (per AC#4) is a variadic `flags ...string` on `StartIn` and the underlying `spawn`.

The bootstrap session is sufficient: idle eviction operates uniformly on any pool member, and respawn semantics are policy-uniform. No `sessions.new` dependency.

# Design

## Approach

Two tests in a new file `internal/e2e/idle_test.go`:

1. **`TestE2E_IdleEviction_EvictsBootstrap`** — start pyry with `-pyry-idle-timeout=1s`; poll the on-disk registry for `lifecycle_state == "evicted"` on the bootstrap entry; cross-check via `pyry status` (`Phase` no longer `running`).
2. **`TestE2E_IdleEviction_LazyRespawn`** — same setup; wait for eviction; trigger lazy respawn by issuing a raw `VerbAttach` request over the control socket; poll for `Phase == "running"` and `lifecycle_state` no longer `"evicted"` while the attach is held; close the connection; test exits.

Driving respawn directly via `net.Dial` + JSON over the control socket (rather than spawning `pyry attach` as a subprocess) is what AC#3 asks for ("re-activate the bootstrap session in test code via existing control surface"). The wire types are public on `internal/control`; e2e is in the same module subtree, so the import is allowed and lets the test issue exactly one `VerbAttach` request and own the connection's lifetime.

## Harness extension — variadic flags

One signature change on `spawn` and `StartIn`. No new exported names. No `Options` struct (#106 precedent: defer struct migration until a second knob lands).

```go
// spawn — extend signature; body appends extraFlags before the `--` claude-arg sentinel.
func spawn(t *testing.T, home string, extraFlags ...string) (string, *exec.Cmd, *bytes.Buffer, *bytes.Buffer, chan struct{}) {
    // ... unchanged: bin, socket, buffers
    args := []string{
        "-pyry-socket=" + socket,
        "-pyry-name=test",
        "-pyry-claude=/bin/sleep",
        "-pyry-idle-timeout=0",
    }
    args = append(args, extraFlags...)
    args = append(args, "--", "infinity")
    cmd := exec.Command(bin, args...)
    // ... unchanged: stdout/stderr/env wiring, Start, wait goroutine
}

// StartIn — extend signature; pass extraFlags through to spawn.
func StartIn(t *testing.T, home string, flags ...string) *Harness {
    t.Helper()
    socket, cmd, stdout, stderr, doneCh := spawn(t, home, flags...)
    // ... unchanged
}
```

**Why `last-wins` works.** Go's `flag` package processes args left-to-right and each occurrence updates the value. A caller passing `-pyry-idle-timeout=1s` after the harness's default `=0` results in `1s`. Verified by the package's documented semantics; no special-casing in spawn.

**Existing call sites are unchanged.** `Start(t)` calls `StartIn(t, t.TempDir())` with no flags. `StartExpectingFailureIn` calls `spawn(t, home)` with no flags. Restart-test call sites of `StartIn(t, home)` work unchanged. Variadic is backwards-compatible at every call site.

**`StartExpectingFailureIn` is NOT extended.** No test in this ticket needs it. Adding the variadic there is a one-line change when a future failed-start scenario needs flags.

## Test 1 — `TestE2E_IdleEviction_EvictsBootstrap`

```go
func TestE2E_IdleEviction_EvictsBootstrap(t *testing.T) {
    home, regPath := newRegistryHome(t)
    h := StartIn(t, home, "-pyry-idle-timeout=1s")

    // Poll the registry file for the bootstrap entry's lifecycle_state == "evicted".
    // Deadline 5s: 1s timer + the runActive→transitionTo→saveLocked tail.
    deadline := time.Now().Add(5 * time.Second)
    var bootstrap registryEntry
    for time.Now().Before(deadline) {
        reg := readRegistry(t, regPath)
        for _, e := range reg.Sessions {
            if e.Bootstrap && e.LifecycleState == "evicted" {
                bootstrap = e
                goto evicted
            }
        }
        time.Sleep(50 * time.Millisecond)
    }
    t.Fatalf("bootstrap not evicted within deadline\nfile:\n%s", mustReadFile(t, regPath))

evicted:
    // Cross-check via the control plane: status should report Phase != "running"
    // for an evicted session (supervisor isn't running).
    r := h.Run(t, "status")
    if r.ExitCode != 0 {
        t.Fatalf("pyry status exit=%d stderr=%s", r.ExitCode, r.Stderr)
    }
    if bytes.Contains(r.Stdout, []byte("Phase:         running")) {
        t.Errorf("status reports Phase: running for an evicted session\nstdout:\n%s", r.Stdout)
    }
    _ = bootstrap // captured for diagnostic if needed; no further fields asserted
}
```

**Why poll registry first, then `pyry status`.** The registry is the load-bearing AC ("registry state observable"). Status is the cross-check — its phase string is `internal/supervisor`'s (`PhaseStopped` typically) and is byte-stable in `runStatus` formatting (`"Phase:         %s\n"`). Asserting *negation* (`!Contains "Phase:         running"`) avoids coupling to which non-running phase shows up; an evicted session's supervisor is in `PhaseStopped`, but the test passes any non-`running` phase.

**No assertion on which non-running phase.** The supervisor's lifecycle has `PhaseStarting`, `PhaseBackoff`, `PhaseStopped` as non-running states. After `cancelSup() → <-runErr`, the supervisor's last write to its state mutex is `PhaseStopped`. Coupling to that exact word is fragile; coupling to "not running" is enough for the AC.

## Test 2 — `TestE2E_IdleEviction_LazyRespawn`

```go
func TestE2E_IdleEviction_LazyRespawn(t *testing.T) {
    home, regPath := newRegistryHome(t)
    h := StartIn(t, home, "-pyry-idle-timeout=1s")

    // Phase A — wait for the bootstrap to evict (same poll as Test 1).
    waitForBootstrapState(t, regPath, "evicted", 5*time.Second)

    // Phase B — issue a raw VerbAttach over the control socket. handleAttach
    // calls Session.Activate before binding the bridge; on success we get a
    // Response{OK: true}. We hold the conn open for the duration of the
    // assertions so the session stays active (attached>0 defers the next
    // idle eviction).
    conn, err := net.Dial("unix", h.SocketPath)
    if err != nil {
        t.Fatalf("dial control socket: %v", err)
    }
    defer conn.Close()

    if err := json.NewEncoder(conn).Encode(control.Request{
        Verb:   control.VerbAttach,
        Attach: &control.AttachPayload{Cols: 80, Rows: 24},
    }); err != nil {
        t.Fatalf("send attach: %v", err)
    }
    var resp control.Response
    if err := json.NewDecoder(conn).Decode(&resp); err != nil {
        t.Fatalf("decode attach ack: %v", err)
    }
    if resp.Error != "" {
        t.Fatalf("attach error: %s", resp.Error)
    }
    if !resp.OK {
        t.Fatalf("attach ack missing OK: %+v", resp)
    }

    // Phase C — assert respawn. Activate returns when activeCh closes (in
    // transitionTo), which races with both saveLocked and runActive starting.
    // Poll both surfaces with a 5s deadline.
    waitForBootstrapState(t, regPath, "active", 5*time.Second) // omitempty → empty/missing field
    deadline := time.Now().Add(5 * time.Second)
    for time.Now().Before(deadline) {
        r := h.Run(t, "status")
        if r.ExitCode == 0 && bytes.Contains(r.Stdout, []byte("Phase:         running")) {
            return // success
        }
        time.Sleep(100 * time.Millisecond)
    }
    t.Fatalf("supervisor never reached Phase: running after lazy respawn")
}

// waitForBootstrapState polls regPath until the bootstrap entry's
// lifecycle_state matches want ("evicted" or "active"). "active" matches
// either an empty/missing field (omitempty default) or the literal string
// "active" — production today writes empty, but the test tolerates either
// to stay decoupled from the omitempty toggle.
func waitForBootstrapState(t *testing.T, regPath, want string, timeout time.Duration) {
    t.Helper()
    deadline := time.Now().Add(timeout)
    for time.Now().Before(deadline) {
        reg := readRegistry(t, regPath)
        for _, e := range reg.Sessions {
            if !e.Bootstrap {
                continue
            }
            got := e.LifecycleState
            if want == "active" && (got == "" || got == "active") {
                return
            }
            if want == "evicted" && got == "evicted" {
                return
            }
        }
        time.Sleep(50 * time.Millisecond)
    }
    t.Fatalf("bootstrap lifecycle_state never became %q within %s\nfile:\n%s",
        want, timeout, mustReadFile(t, regPath))
}
```

**Why raw `VerbAttach` and not `pyry attach` as a subprocess.** Determinism. `pyry attach` enters a stdin-byte loop (`copyWithEscape`) that requires careful pipe management to keep the conn alive while assertions run; closing stdin too early ends the attach before the next idle eviction fires. A raw conn is a one-line `defer conn.Close()` and the test owns the timeline. The control protocol's `Request`/`Response`/`AttachPayload` types are public on `internal/control`; importing them from `internal/e2e` is in-module and intended.

**Why we DON'T `<-doneCh` on the conn.** `handleAttach` writes the OK ack and binds the bridge to the conn. The conn now streams PTY bytes from `/bin/sleep infinity` (which writes nothing) and back. The test never reads from the conn after the ack. It just holds the conn open and asserts on side surfaces (registry file, `pyry status`). When the test returns, the deferred `conn.Close()` triggers bridge teardown server-side — `attached--` runs in the wrapper goroutine, idle timer eventually re-evicts, no leak.

**Why poll for `Phase: running` and not for `attached > 0` directly.** `attached` is package-private state inside `Session`. The wire surface (`pyry status`) reports the supervisor's phase, which is what the AC means by "alive again". Phase transitions to `running` once the supervisor's child-process spawn loop has the PID — exactly the "lazy respawn happened" signal.

**The "omit vs. empty" wrinkle.** Today's `saveLocked` writes `lifecycle_state: "evicted"` and omits the field for active (the `omitempty` default). The helper `waitForBootstrapState(t, regPath, "active", ...)` tolerates both. If a future change starts writing `"active"` explicitly, the test still passes; if a future change keeps omitting, the test still passes. The test is asserting "no longer evicted", not "exact serialisation of the active state".

## File layout

```
internal/e2e/
├── harness.go             [modified — variadic flags on spawn + StartIn]
├── idle_test.go           [new — two tests, ~150 LOC]
├── restart_test.go        [unchanged — newRegistryHome / readRegistry / writeRegistry / mustReadFile reused]
└── ...
```

`idle_test.go` reuses `newRegistryHome`, `readRegistry`, `mustReadFile`, and the local `registryFile`/`registryEntry` mirror types from `restart_test.go` via package scope. They're already file-local helpers in the `e2e` package; no move, no promote, no duplication.

# Concurrency model

No new goroutines in production code. The harness changes are signature-only.

Test-side goroutines: zero in Test 1, zero in Test 2. The raw `VerbAttach` connection is held by the test goroutine itself; `defer conn.Close()` runs on test return. Server-side, `handleAttach` spawns its existing detach-watcher goroutine (the one already there in production); the test does not introduce any new goroutine on either side.

**Locking:** the test reads `sessions.json` from disk while pyry may be writing it. `saveLocked` uses `os.WriteFile` (atomic `O_TRUNC` write, not rename — see `internal/sessions/registry.go`); a concurrent reader can in principle observe a torn read. Mitigation: `readRegistry` uses `json.Unmarshal`, which fails on torn input; the polling loop retries (`time.Sleep(50ms)`) on the next iteration. If the unmarshal fails, the helper currently `t.Fatal`s — that's a real bug if it fires, but the AC is "registry state observable", and a documented torn-write behaviour would surface as flake. **Mitigation if observed:** wrap `readRegistry` calls in the poll with a recover-and-retry. Not adding speculatively; the existing restart tests poll the same file with the same helper and have not flaked.

# Error handling

| Failure | Handling |
|---|---|
| `StartIn` extra-flag spawn fails | `spawn`'s existing `t.Fatalf` on `cmd.Start` covers it; no new path. |
| Bootstrap never evicts within 5s | `t.Fatalf` with `mustReadFile(regPath)` for diagnostic. |
| `net.Dial` fails | `t.Fatalf("dial control socket: %v", err)`. |
| `VerbAttach` returns `Response.Error` | `t.Fatalf("attach error: %s", resp.Error)` — the activate path failed, surface verbatim. |
| `Phase: running` never observed within 5s | `t.Fatalf` — respawn never completed; production regression. |
| Torn registry read (concurrent `os.WriteFile`) | Poll's `readRegistry` `t.Fatal`s — see Concurrency note. Defer fix until observed. |

# Testing strategy

The two tests defined in this spec ARE the testing strategy. No new test scaffolding, no helper file beyond `waitForBootstrapState` (file-local; deleted if a third caller justifies promotion).

**Manual smoke (recorded for the PR description):**
1. `go test -tags=e2e -run TestE2E_IdleEviction ./internal/e2e/...`
2. Both tests pass within ~10s combined.
3. `go test ./...` (no tag) — both new tests skipped, default suite unaffected.

**Backwards-compat verification:** existing `Start(t)`, `StartIn(t, home)`, and `StartExpectingFailureIn(t, home)` call sites continue to compile and pass. The variadic addition is a no-op at every existing call site.

# Open questions

1. **`pyry status -pyry-name=test` vs. `-pyry-socket=...`.** `Harness.Run` auto-injects `-pyry-socket=<h.SocketPath>` after the verb. That's correct here — `pyry status` will dial the harness's specific socket regardless of the daemon's `-pyry-name`. No change.
2. **`pyry list` doesn't exist yet.** The ticket lists "registry file inspection" and "`pyry status`" as observability options. We use both. `pyry list` (#88, not yet shipped) would offer a cleaner cross-check via wire output; integrate when it lands.
3. **Idle-timer resolution.** `1s` is generous (the timer fires at `runActive`-start + 1s; total wall-clock to evicted ≈ 1.1-1.3s). If the test ever flakes on slow CI, bump to `2s` and the deadline to `8s`. Don't shrink below `1s` — the AC explicitly suggests `1s`.

# Why size:S

PO sized this S. Re-checking against the architect red lines:

- **Files added:** 1 (`idle_test.go`). Files modified: 1 (`harness.go`). Total: 2 files. ≤ 3 ✓
- **Production lines:** ~5 (variadic on two signatures + one `append`). Test lines: ~150 (two tests + one helper). Total: ~155, dominated by tests. Production well under 100. ✓
- **New exported types/interfaces:** 0. ✓
- **Consumer call sites that need updating:** 0 — variadic is backwards-compatible. ✓
- **Acceptance criteria worth of work:** 4 ACs, but AC#4 ("extra-args hook") is satisfied by the same 5-LOC change that AC#1 / AC#2 / AC#3 use. Effectively two test cases. ✓

No red line tripped. Single architect run; one developer run; no split.
