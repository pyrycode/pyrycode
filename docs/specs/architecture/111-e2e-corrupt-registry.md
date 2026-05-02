---
ticket: 111
title: E2E test — pyry fails loudly on corrupt registry
status: spec
size: S
---

# Context

`<HOME>/.pyry/<name>/sessions.json` is the source of truth for which sessions
exist. If it contains malformed JSON, the worst possible outcome is silent data
loss (e.g. "corrupt file → empty registry → drop everything"). The unit-level
behaviour is already tested in `internal/sessions/registry_test.go`
(`registry: parse %s: %w` is the stable wrapped error). What's missing is e2e
coverage at the binary boundary: prove that when the daemon is launched against
a corrupt `sessions.json`, the process exits non-zero, the operator sees a
human-readable hint that the registry is the problem, and the corrupt bytes are
left untouched on disk for forensic recovery.

The current e2e harness (`internal/e2e/harness.go`) only supports the
"happy-path spawn": `Start(t)` and `StartIn(t, home)` both block until the
daemon's control socket becomes dialable, and `t.Fatalf` if that doesn't
happen. To assert on a *failed* start, the harness needs a small extension that
spawns pyry, expects it to exit before becoming ready, and exposes the captured
exit code and stderr for assertions — exactly mirroring the `RunResult` shape
already used by `Harness.Run` / `RunBare`.

Split from #108. Sibling of #106/#107 (restart-survival e2e tests).

# Design

## Boot path under test (no production change)

The corrupt-registry failure already exists; this ticket only adds coverage.
For traceability, the existing path is:

```
cmd/pyry/main.go:run() → runSupervisor()
  → sessions.New(Config{RegistryPath: ...})
      → loadRegistry(path)
          → json.Unmarshal → returns "registry: parse <path>: <unmarshal err>"
      → New wraps:           "sessions: load registry: <above>"
  → runSupervisor returns:   "pool init: <above>"
  → main() prints to stderr: "pyry: pool init: sessions: load registry: registry: parse <path>: <unmarshal err>"
  → os.Exit(1)
```

The full stderr line therefore contains both the substring `registry` (twice)
and the absolute path (which itself ends in `sessions.json`). Any of those
three is a stable substring assertion target; the test will assert on
`registry` because it's the most informative-yet-durable choice (path varies
per run; `sessions.json` is the filename only, and `registry` is the
domain-level concept the operator needs to recognise). No assertion on the
exact wrap chain or exit code value beyond "non-zero".

## Harness extension

Add **one** new public function to `internal/e2e/harness.go`:

```go
// StartExpectingFailureIn spawns pyry against the given home, expects it to
// exit before the readiness deadline elapses, and returns the captured exit
// code, stdout, and stderr. Fails the test if pyry instead becomes ready
// (control socket dialable) or if it neither exits nor becomes ready within
// the readiness deadline.
//
// Unlike StartIn, no Harness is returned: there is no live daemon to drive,
// no socket to clean up. The caller pre-populates HOME (e.g. with a corrupt
// sessions.json) and asserts on the RunResult.
func StartExpectingFailureIn(t *testing.T, home string) RunResult
```

Shape choice (alternate constructor vs. Options field vs. lower-level helper):

| Option                       | Verdict                                                              |
|------------------------------|----------------------------------------------------------------------|
| `Options` field on `StartIn` | Adds a polymorphic return (`(*Harness, RunResult)` or interface)     |
| Lower-level `spawn` helper   | Bigger public surface than this one test needs                       |
| **Alternate constructor**    | Single-purpose, no return-shape compromise, mirrors `Run` / `RunBare`|

Picked: alternate constructor. The ticket explicitly forbids "unused API
surface" and "parallel implementation"; the constructor satisfies both by
factoring the shared spawn logic into an unexported helper.

### Internal refactor

Extract a private `spawn(t, home) (*exec.Cmd, *bytes.Buffer, *bytes.Buffer, chan struct{})`
that does everything `StartIn` currently does *up to and including* the
`go func() { _ = cmd.Wait(); close(doneCh) }()` goroutine, but **without**:

- constructing `*Harness`
- registering `t.Cleanup`
- calling `waitForReady`

`StartIn` becomes: `spawn`, build `Harness`, register cleanup, `waitForReady`,
done. `StartExpectingFailureIn` becomes: `spawn`, then a select-driven wait
loop over the readiness deadline that:

- returns `RunResult` populated from `cmd.ProcessState` if `<-doneCh` fires,
- `t.Fatalf`s if the socket becomes dialable (daemon unexpectedly came up),
- `t.Fatalf`s if the readiness deadline elapses with neither (daemon hung).

The "daemon unexpectedly came up" path must also tear the daemon down before
returning — otherwise the test leaks a process. Reuse the SIGTERM/SIGKILL
escalation already factored into `Harness.teardown` by constructing a
throwaway `Harness` for that one purpose, or by inlining the same
`Process.Signal(SIGTERM) → wait(termGrace) → SIGKILL → wait(killGrace)`
sequence. Inlining is fine for ~10 lines; either is acceptable.

### Constants reuse

Reuse the existing `readyDeadline = 5*time.Second` and `readyPollGap`. The
corrupt-registry path exits in milliseconds (synchronous JSON parse), so 5
seconds is generous; no new constant needed.

### Why no `StartExpectingFailure` (without `In`)

`Start(t)` exists because the happy-path test usually wants a fresh
`t.TempDir()` with no on-disk pre-state. The failure-start test always wants
to pre-populate `<home>/.pyry/test/sessions.json`, so it always needs a
caller-supplied home. Adding a zero-arg `StartExpectingFailure(t)` would be
unused surface — skip it.

## Test

Add `TestE2E_Startup_CorruptRegistryFailsClean` to a new file
`internal/e2e/startup_test.go` (build-tagged `e2e`). New file rather than
extending `restart_test.go` because the test's domain is "startup failure",
not "restart survival"; future startup-shaped e2e tests have a natural home.

Sketch:

```go
//go:build e2e

package e2e

import (
    "bytes"
    "os"
    "path/filepath"
    "testing"
)

func TestE2E_Startup_CorruptRegistryFailsClean(t *testing.T) {
    home, regPath := newRegistryHome(t)        // reused from restart_test.go

    corrupt := []byte("{not valid json")
    if err := os.WriteFile(regPath, corrupt, 0o600); err != nil {
        t.Fatalf("seed corrupt registry: %v", err)
    }

    res := StartExpectingFailureIn(t, home)

    if res.ExitCode == 0 {
        t.Errorf("exit code = 0, want non-zero (stderr=%s)", res.Stderr)
    }
    if !bytes.Contains(res.Stderr, []byte("registry")) {
        t.Errorf("stderr does not mention registry: %s", res.Stderr)
    }

    got, err := os.ReadFile(regPath)
    if err != nil {
        t.Fatalf("read registry after failed start: %v", err)
    }
    if !bytes.Equal(got, corrupt) {
        t.Errorf("registry mutated by failed start:\nwant: %q\ngot:  %q", corrupt, got)
    }
}
```

Reuses `newRegistryHome` from `restart_test.go` (same `_test.go` package,
same build tag). No production changes.

# Concurrency model

`StartExpectingFailureIn` runs entirely in the test goroutine. It:

1. Calls `spawn`, which forks the child and starts one wait-goroutine that
   closes `doneCh` on exit (identical to `StartIn`).
2. Polls `(net.Dial, doneCh, time.After(readyPollGap))` in a loop bounded by
   `readyDeadline`.
3. On `<-doneCh`, reads `cmd.ProcessState.ExitCode()` (safe — wait has
   already returned by the time the goroutine closes the channel).

No new goroutines beyond the one `spawn` already starts. No shared mutable
state across goroutines other than the `bytes.Buffer` stdout/stderr that
`exec.Cmd` already serialises, plus `doneCh` (closed exactly once).

# Error handling

Failure modes inside `StartExpectingFailureIn` and how they're surfaced:

| Mode                                            | Action                                  |
|-------------------------------------------------|-----------------------------------------|
| `cmd.Start` fork error                          | `t.Fatalf` (matches `StartIn` behaviour)|
| Child exits before readiness                    | Return `RunResult` (the success path)   |
| Child becomes dialable before exiting           | Tear down child, then `t.Fatalf`        |
| Neither exit nor readiness within `readyDeadline` | Tear down child, then `t.Fatalf`      |

The test itself uses `t.Errorf` for assertion failures (so multiple violations
surface in one run) and `t.Fatalf` only for setup errors (file write, file
read after the failed start).

# Testing strategy

How we know the harness extension is correct:

- The new test exercises every public branch of `StartExpectingFailureIn`
  (the success path: child exits before ready). The "child became ready" and
  "neither happened" paths are defensive — they `t.Fatalf` and would only
  trigger if production behaviour regressed (corrupt JSON stops failing) or
  if the test environment hangs the daemon. We don't add unit tests for
  those branches because they have no behavioural value beyond what the
  happy-path branch provides; the ticket says "exercised exclusively by this
  test".
- The substring `registry` is checked, not the full error string. If a
  future refactor changes the wrap chain from `pool init: sessions: load
  registry: registry: parse <path>:` to anything that still names "registry",
  the test stays green. If it ever loses that word, the test fails loudly —
  which is the right outcome (operator-facing diagnostics regressed).
- Byte-equal assertion on the registry catches the worst-case regression
  (silent overwrite). It does not depend on JSON parsing the corrupt input.

# Open questions

None. The harness shape is constrained by the ticket; the production path is
already in place.

# Out of scope

- Adding analogous "expect failed start" helpers for other startup-failure
  modes (missing claude binary, unreachable workdir, port-in-use socket, …).
  This ticket adds the primitive once, exercised by one test. Future startup
  e2e tests may reuse `StartExpectingFailureIn` as-is.
- Changing the corrupt-registry error wording or exit code. The test asserts
  durable substrings; the production behaviour is unchanged.
- Recovery / repair of corrupt registries. The contract is "fail loud, leave
  the file untouched"; a future ticket may add `pyry registry verify` or
  similar, but that's not this ticket.
