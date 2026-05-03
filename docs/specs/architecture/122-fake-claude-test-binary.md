---
ticket: 122
title: e2e fake-claude test binary (rotation primitive)
status: spec
size: S
---

# Files to read first

Read these before doing any exploration of your own — they are the load-bearing surfaces this spec composes.

- `internal/sessions/id.go` — full file (~30 lines). The hand-rolled UUIDv4 generator the binary's `uuidV4()` mirrors. Same crypto/rand + version/variant byte fixup, just inlined into `package main` to avoid pulling in `internal/sessions` from a test-only binary.
- `internal/sessions/rotation/watcher.go:17-19` — the `uuidStemPattern` regex (`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`). The binary's freshly-generated stem must satisfy this so a downstream consumer (the next ticket) gets a CREATE event the watcher actually accepts. The initial UUID handed in via env must satisfy the same regex.
- `internal/e2e/harness.go:107-132` — `ensurePyryBuilt`. Pattern reference: `sync.Once`-cached `go build` into a tmp dir. The next-ticket harness consumer will mirror this for the fake claude binary; this ticket's test does the simpler one-shot `go build` (no caching needed — single test, single process).
- `internal/e2e/harness.go:271-292` — `killSpawned`. Pattern reference for the test's SIGTERM-then-grace-then-SIGKILL teardown of the binary it spawns.
- `docs/lessons.md` § "Claude session storage on disk" — the on-disk shape the binary mimics. The binary itself does not encode a path; the harness (next ticket) hands it the encoded directory via env. This lesson is here so the developer understands *why* the env contract is "give me a directory that already exists" rather than "give me HOME and I'll encode."
- `docs/specs/architecture/55-clear-rotation-watcher-e2e.md` (commit 840bad0) — the parent spec that combined this binary with its harness consumer. The binary sketch around the "NEW `internal/e2e/internal/fakeclaude/main.go`" heading is useful reference; the harness/test plumbing on the rest of that page is **out of scope here** (sibling slice).

# Context

The e2e harness today hardcodes the supervised child to `/bin/sleep 99999` (`internal/e2e/harness.go:243-252`). That child opens no JSONLs, so it cannot exercise `internal/sessions/rotation`'s production behaviour: matching `<uuid>.jsonl` CREATE events to a tracked PID via `/proc/<pid>/fd` (Linux) or `lsof` (macOS), and calling `Pool.RotateID` when claude has clearly pivoted to a fresh UUID.

This ticket ships the **first half** of that fix: a test-only Go binary that opens a JSONL fd in a configured sessions dir and rotates it on a triggerable signal, plus an in-isolation Go test that verifies the binary's externally observable behaviour by `exec`'ing it directly. **No harness wiring, no pyry involvement.** The harness consumer (`Harness.StartRotation`, `ensureFakeClaudeBuilt`) is the next ticket; the rotation-watcher driver test is the one after that.

Splitting the binary from its first harness consumer follows the always-split "new package AND its first consumer" pattern. The original combined ticket (#55, then #119) hit AC-count and "and"-in-title red lines on every recovery attempt.

# Design

## Approach

A single-file `package main` Go program at `internal/e2e/internal/fakeclaude/main.go`. Three env vars define its job; one trigger file drives the single state transition; the rest is `os.OpenFile` and `time.Sleep`. No goroutines, no signal handlers, no flags — env is the entire input surface.

The `internal/e2e/internal/fakeclaude/` location is deliberate: nesting under `internal/e2e/internal/` visibility-fences the binary so only e2e-package code can import it. Since it's `package main` that's mostly moot, but the path also signals intent to readers.

## End-to-end trajectory

```
test setup                                           binary
─────────────────────────────────────────────────────────────────────────────
go build -o $TMP/fakeclaude
$TMP/sessions/                                       (dir handed in via env)
$TMP/trigger                                         (path handed in via env)
initialUUID = "11111111-1111-4111-8111-111111111111"

exec.Command(binPath)                          ───>  main():
  env: PYRY_FAKE_CLAUDE_SESSIONS_DIR=...               open(<dir>/<initialUUID>.jsonl,
       PYRY_FAKE_CLAUDE_INITIAL_UUID=...                    O_WRONLY|O_APPEND|O_CREATE, 0o600)
       PYRY_FAKE_CLAUDE_TRIGGER=...                     write("{}\n"); fsync
                                                       loop every 50ms:
                                                         if stat(trigger) ok && !rotated:
poll(50ms, deadline 3s):                                   close(oldFd)                  <- strict order:
  stat(<dir>/<initialUUID>.jsonl) ok?  ────────────>       newU = uuidV4()                  close OLD
  → yes                                                    open(<dir>/<newU>.jsonl, ...)    before
                                                           write("{}\n"); fsync             open NEW
os.WriteFile(trigger, nil, 0o600)              ───>        os.Remove(trigger)
                                                           rotated = true

poll(50ms, deadline 3s):
  list <dir> for *.jsonl
  find one whose stem != initialUUID and
  matches uuidStemPattern
  → yes (= newU)

cmd.Process.Signal(SIGTERM)                    ───>  (Go default handler: terminate)
cmd.Wait()
assert: ProcessState.Signaled() &&
        ProcessState.Sys().Signal() == SIGTERM
```

## Package structure

Two new files, one new directory.

### NEW `internal/e2e/internal/fakeclaude/main.go`

`package main`, no build tag (a tiny `package main` is cheap to compile under `./...` and not having a tag means the test can `go build` it without `-tags`). Approximate shape (~80 LOC):

```go
package main

import (
    "crypto/rand"
    "fmt"
    "os"
    "path/filepath"
    "time"
)

const (
    envSessionsDir = "PYRY_FAKE_CLAUDE_SESSIONS_DIR"
    envInitialUUID = "PYRY_FAKE_CLAUDE_INITIAL_UUID"
    envTrigger     = "PYRY_FAKE_CLAUDE_TRIGGER"
    pollInterval   = 50 * time.Millisecond
)

func main() {
    dir   := mustEnv(envSessionsDir)
    initU := mustEnv(envInitialUUID)
    trig  := mustEnv(envTrigger)

    f := openSession(dir, initU)
    rotated := false
    for {
        if !rotated {
            if _, err := os.Stat(trig); err == nil {
                _ = f.Close()                      // close OLD before open NEW
                newU := uuidV4()
                f = openSession(dir, newU)
                _ = os.Remove(trig)
                rotated = true
            }
        }
        time.Sleep(pollInterval)
    }
}

func openSession(dir, uuid string) *os.File {
    path := filepath.Join(dir, uuid+".jsonl")
    f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
    if err != nil { fatalf("open %s: %v", path, err) }
    if _, err := f.WriteString("{}\n"); err != nil { fatalf("write %s: %v", path, err) }
    if err := f.Sync(); err != nil { fatalf("fsync %s: %v", path, err) }
    return f
}

func uuidV4() string {
    var b [16]byte
    if _, err := rand.Read(b[:]); err != nil { fatalf("rand: %v", err) }
    b[6] = b[6]&0x0f | 0x40
    b[8] = b[8]&0x3f | 0x80
    return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func mustEnv(k string) string {
    v := os.Getenv(k)
    if v == "" { fatalf("missing env %s", k) }
    return v
}

func fatalf(format string, a ...any) {
    fmt.Fprintf(os.Stderr, "fakeclaude: "+format+"\n", a...)
    os.Exit(1)
}
```

Behaviour notes:

- **No signal handler.** Go's runtime default kills the process on SIGTERM with no goroutine wind-down. The OS auto-closes any open fd on process death. The next ticket's harness consumer needs the binary to die when pyry SIGTERMs the PTY child during teardown; default behaviour suffices.
- **Strict close-OLD-before-open-NEW order.** This is what makes the *consumer* ticket's exact-match probe check (`watcher.go:167`) deterministic: at the instant the watcher's CREATE-driven probe runs, only the new path is on the PID's fd table. Don't reorder; don't keep both fds open across the rotation.
- **Single rotation; subsequent triggers ignored.** The `rotated bool` guard and the `os.Remove(trig)` together make the trigger one-shot. If the harness consumer ever wants multiple rotations, that's a future spec change — not a "make it general now" exercise.
- **`uuidV4()` matches `rotation/watcher.go:19`'s `uuidStemPattern` exactly** (lowercase hex, 8-4-4-4-12, version 4, variant RFC 4122). The initial UUID handed in via env must satisfy the same regex; the test uses the canonical literal `11111111-1111-4111-8111-111111111111`.
- **No `internal/sessions` import.** The hand-rolled `uuidV4` duplicates `sessions.NewID`. Importing `sessions` from a test-only `package main` under `internal/e2e/internal/` is technically allowed by Go's visibility rules but pulls a chunk of production surface into a test binary for one function. Inline copy (~8 lines) is the simpler call.
- **No `flag` package.** Env-only is shorter and matches how the next ticket's harness will configure the child (env vars set on `cmd.Env`, no flag plumbing in pyry).

### NEW `internal/e2e/internal/fakeclaude/main_test.go`

`//go:build e2e` so plain `go test ./...` doesn't pay the `go build` cost. Single test, `package main` (so it can share consts like `envSessionsDir` if useful — but the test can also just hardcode the env-var names; either is fine).

```go
//go:build e2e

package main

import (
    "os"
    "os/exec"
    "path/filepath"
    "regexp"
    "syscall"
    "testing"
    "time"
)

func TestFakeClaude_OpensInitialAndRotatesOnTrigger(t *testing.T) {
    tmp := t.TempDir()
    sessionsDir := filepath.Join(tmp, "sessions")
    if err := os.MkdirAll(sessionsDir, 0o700); err != nil { t.Fatal(err) }
    triggerPath := filepath.Join(tmp, "rotate.trigger")
    initialUUID := "11111111-1111-4111-8111-111111111111"
    binPath := filepath.Join(tmp, "fakeclaude")

    // Build the binary into the test's tmp dir.
    out, err := exec.Command("go", "build", "-o", binPath,
        "github.com/pyrycode/pyrycode/internal/e2e/internal/fakeclaude").CombinedOutput()
    if err != nil { t.Fatalf("go build: %v\n%s", err, out) }

    cmd := exec.Command(binPath)
    cmd.Env = append(os.Environ(),
        "PYRY_FAKE_CLAUDE_SESSIONS_DIR="+sessionsDir,
        "PYRY_FAKE_CLAUDE_INITIAL_UUID="+initialUUID,
        "PYRY_FAKE_CLAUDE_TRIGGER="+triggerPath,
    )
    var stderr []byte
    cmd.Stderr = nil // see note below; capture if useful for debugging
    if err := cmd.Start(); err != nil { t.Fatalf("start: %v", err) }

    doneCh := make(chan error, 1)
    go func() { doneCh <- cmd.Wait() }()

    // Defensive: SIGKILL on test exit if anything below fails before the
    // explicit SIGTERM step.
    t.Cleanup(func() {
        if cmd.ProcessState == nil {
            _ = cmd.Process.Kill()
            <-doneCh
        }
    })

    // Phase 1: poll for the initial JSONL.
    initialPath := filepath.Join(sessionsDir, initialUUID+".jsonl")
    if !waitForFile(initialPath, 3*time.Second) {
        t.Fatalf("initial JSONL not created within deadline: %s\nstderr:\n%s",
            initialPath, stderr)
    }

    // Phase 2: drop the trigger.
    if err := os.WriteFile(triggerPath, nil, 0o600); err != nil { t.Fatal(err) }

    // Phase 3: poll for a fresh <uuid>.jsonl distinct from the initial one.
    uuidStem := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
    var rotatedUUID string
    deadline := time.Now().Add(3 * time.Second)
    for time.Now().Before(deadline) {
        entries, _ := os.ReadDir(sessionsDir)
        for _, e := range entries {
            name := e.Name()
            if filepath.Ext(name) != ".jsonl" { continue }
            stem := name[:len(name)-len(".jsonl")]
            if stem == initialUUID { continue }
            if !uuidStem.MatchString(stem) { continue }
            rotatedUUID = stem
            break
        }
        if rotatedUUID != "" { break }
        time.Sleep(50 * time.Millisecond)
    }
    if rotatedUUID == "" {
        t.Fatalf("no rotated JSONL appeared in %s within deadline", sessionsDir)
    }

    // Trigger should have been consumed.
    if _, err := os.Stat(triggerPath); !os.IsNotExist(err) {
        t.Fatalf("trigger file still present after rotation: err=%v", err)
    }

    // Phase 4: SIGTERM and assert clean termination.
    if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
        t.Fatalf("signal SIGTERM: %v", err)
    }
    select {
    case waitErr := <-doneCh:
        // Go's default SIGTERM handler terminates the process; cmd.Wait
        // returns an *exec.ExitError whose ProcessState reports
        // Signaled()==true with Signal()==SIGTERM. That's "clean" for a
        // signal-killed program with no handler.
        if !assertSignaledBy(t, cmd.ProcessState, syscall.SIGTERM) {
            t.Fatalf("unexpected exit: err=%v state=%+v", waitErr, cmd.ProcessState)
        }
    case <-time.After(3 * time.Second):
        _ = cmd.Process.Kill()
        <-doneCh
        t.Fatalf("did not exit within 3s of SIGTERM")
    }
}

func waitForFile(path string, timeout time.Duration) bool {
    deadline := time.Now().Add(timeout)
    for time.Now().Before(deadline) {
        if _, err := os.Stat(path); err == nil { return true }
        time.Sleep(50 * time.Millisecond)
    }
    return false
}

func assertSignaledBy(t *testing.T, ps *os.ProcessState, sig syscall.Signal) bool {
    t.Helper()
    ws, ok := ps.Sys().(syscall.WaitStatus)
    if !ok { return false }
    return ws.Signaled() && ws.Signal() == sig
}
```

(Sketch only — actual byte counts and helper boundaries are the developer's call.)

# Concurrency model

**Binary:** single goroutine (main). Main loop is a `for { ... time.Sleep(50ms) }` polling spin. No channels, no select, no signal handler — Go's runtime handles SIGTERM by default-terminating the process. The OS auto-closes the open fd on process death.

**Test:** main goroutine drives the assertions; one auxiliary goroutine reads `cmd.Wait()` into a buffered(1) channel so the SIGTERM-then-deadline `select` can distinguish "exited" from "stuck." Standard `exec`-then-wait pattern; nothing exotic.

# Error handling

**Binary:** any failure (`os.OpenFile`, `Write`, `Sync`, missing env var, `crypto/rand`) prints to stderr and `os.Exit(1)`. There is no recovery path — a fake claude that can't open its sessions file is a bug in the test setup, and exit-1 surfaces it loudly to whoever spawned the binary. `os.Remove(trigger)` and `f.Close()` errors are deliberately ignored: the OS will reclaim the fd, and a missing trigger file at remove time would be a benign concurrent-removal race that doesn't affect correctness.

**Test:** any deadline miss is `t.Fatalf` with the relevant path and (where useful) accumulated stderr. The defensive `t.Cleanup` SIGKILLs the binary if a `t.Fatal` fires before the explicit SIGTERM phase — without it, a leaked process would survive the test and hold an fd in the now-deleted tmp dir.

# Testing strategy

The test in this ticket is the *only* verification of the binary in this ticket. It exercises every property an end-to-end harness consumer will rely on:

1. **Initial fd opens** — the platform probe in the consumer ticket sees this fd on the PID's table. Verified by file existence + `{}\n` content (size > 0 implies write+fsync succeeded).
2. **Trigger drives one rotation** — verified by a new `<uuid>.jsonl` appearing whose stem matches `uuidStemPattern` and is not the initial UUID.
3. **Strict close-OLD-before-open-NEW** — *not* directly observable from the test (it would require attaching `lsof` mid-rotation, which races the 50ms poll). The order is enforced by code review of the binary itself; the consumer ticket's end-to-end driver is what proves it works against the real probe. This ticket covers the property "rotation happens" and the consumer ticket covers "rotation is observable to the watcher."
4. **Trigger is consumed** — the file is gone after rotation; subsequent triggers are ignored (the `rotated` guard, exercised by code review; this ticket's test does not drop a second trigger).
5. **Clean SIGTERM exit** — `ProcessState.Signaled() && Signal()==SIGTERM`.

What the test does **not** do:

- Spawn pyry. Out of scope; that's the next ticket's harness wiring.
- Verify the JSONL content beyond "non-empty." The bytes don't matter to the rotation watcher, only the file's existence on the PID's fd table.
- Cross-platform fork: `/proc` vs `lsof` is irrelevant here because no probe runs in this test.

# Why the test goes behind `//go:build e2e`

AC#3 leaves it as a developer call: "no slowdown to default test runs and no required external state." A `go build` per test invocation costs ~1-2 seconds — meaningful enough that hiding it behind the same build tag as the rest of `internal/e2e/` is the right default. Anyone running `go test -tags=e2e ./...` already accepts the e2e cost.

The binary itself has **no** build tag. A tiny `package main` is essentially free to compile, and not gating it means the test (and the next-ticket harness) can `go build` it without passing `-tags`.

# Open questions

- **Should the binary print anything on a successful rotation?** Adding `fmt.Fprintf(os.Stderr, "fakeclaude: rotated %s -> %s\n", initU, newU)` would help debug a future flake at the cost of a tiny bit of noise. The next ticket's harness consumer might want this; the in-isolation test in *this* ticket does not. Recommend: leave it out for now — easy to add later if a debugging case appears.
- **Should the polling interval be configurable via env?** The 50ms cadence matches the rotation watcher's own retry schedule (`docs/lessons.md` § "Probing open files cross-platform"). Don't add a knob until a concrete consumer needs one; YAGNI.

# Out of scope

- Harness wiring (`Harness.StartRotation`, `Harness.ClaudeSessionsDir`, `ensureFakeClaudeBuilt`) — sibling slice, separate ticket.
- Driving pyry's rotation watcher end-to-end — two tickets downstream.
- Retiring the existing flaky `TestPool_Run_StartsWatcher` — separate concern, only relevant once the e2e driver test is consistently green.
