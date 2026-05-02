---
ticket: 55
title: E2E coverage for /clear rotation watcher
status: spec
size: S
---

# Context

`internal/sessions/rotation` watches `~/.claude/projects/<encoded-cwd>/` for
new `<uuid>.jsonl` files appearing during a session's runtime, matches each
CREATE to a tracked PID via the platform probe (`/proc/<pid>/fd` on Linux,
`lsof` on macOS), and calls `Pool.RotateID` when claude has clearly pivoted
to a fresh UUID — the on-disk shape of `/clear`.

The only test for this property today is `TestPool_Run_StartsWatcher`
(`internal/sessions/pool_test.go:785`). It substitutes the real probe with a
`dirProbe` that just returns the most-recent jsonl in dir, so it exercises
the watcher's event loop and `RotateID` plumbing but **not** the real
`/proc`-or-`lsof` probe path. It is also intermittently flaky under
`-count=N` (observed 2026-05-02).

This ticket adds binary-level coverage that drives a real `pyry` daemon
through the same rotation flow with the **real** probe doing its real work
on a **real** child process's FD table. If the new test passes consistently,
a follow-up ticket can retire the flaky unit test (out of scope here).

The behaviour under test is fixed in `docs/lessons.md` § "Claude session
storage on disk": `/clear` causes claude to stop writing the original
`<uuid>.jsonl` and start writing a fresh `<new-uuid>.jsonl` in the same
directory; pyry's watcher must follow.

# Design

## Approach

Per the technical-notes recommendation, **option 1**: a tiny test-only "fake
claude" binary that opens a JSONL fd in the encoded sessions dir, holds it
open until nudged, then closes it and opens a different `<uuid>.jsonl` in
the same dir. The harness points pyry at this binary via `-pyry-claude=<path>`.

The watcher's correctness property — *new JSONL appears in the watched dir
while a tracked PID has it open → registry follows* — is observable end-to-
end with this setup, against the real platform probe. Option 2 (drive a
real `claude --resume` through pyry's PTY) is rejected: it would need an
API key, a real `claude` binary in CI, and a way to feed `/clear` into
pyry's PTY from a test, none of which the property under test requires.

## End-to-end trajectory

```
test setup
  home   = os.MkdirTemp("", "pyry-rot-*")          # short-prefix; sun_path budget
  workdir= home (use HOME itself as the workdir for path determinism)
  encDir = home/.claude/projects/<encode(workdir)>  # encode = "/" and "." → "-"
  os.MkdirAll(encDir, 0o700)
  initialUUID = "11111111-1111-4111-8111-111111111111"  # canonical literal
  os.WriteFile(encDir/<initialUUID>.jsonl, []byte("{}\n"), 0o600)

harness spawn (StartRotation)
  pyry  -pyry-socket=<home>/pyry.sock
        -pyry-name=test
        -pyry-workdir=<home>
        -pyry-claude=<fakeclaudeBin>
        -pyry-idle-timeout=0
        --
  pyry env: HOME=<home>, plus
            PYRY_FAKE_CLAUDE_SESSIONS_DIR=<encDir>,
            PYRY_FAKE_CLAUDE_INITIAL_UUID=<initialUUID>,
            PYRY_FAKE_CLAUDE_TRIGGER=<home>/rotate.trigger

pyry startup
  sessions.New → reconcile sees <initialUUID>.jsonl → bootstrap.id = initialUUID
  pyry spawns fake claude under PTY
  fake claude opens encDir/<initialUUID>.jsonl O_APPEND, writes "{}\n", fsync
  pyry's rotation.Watcher is running on encDir

test phase 1 — pre-rotation observation
  poll <home>/.pyry/test/sessions.json until len(sessions)==1 && id == initialUUID
  capture pre := registry entry (id, lastActiveAt)
  capture preChildPID via h.Run(t, "status") or registry's runtime fields
    (only used as a debug field on failure)

test phase 2 — trigger /clear
  os.Create(<home>/rotate.trigger).Close()    // fake claude polls every 50ms
  fake claude:
    close current fd                          // strict order: close OLD before open NEW
    newUUID = uuid.NewV4()                    // any well-formed v4 stem
    open encDir/<newUUID>.jsonl O_CREATE|O_APPEND, write "{}\n", fsync
    os.Remove(<home>/rotate.trigger)          // idempotency: trigger consumed

watcher reaction (already covered by production code — not under test design)
  fsnotify CREATE → IsAllocated(newUUID)=false → snapshot=[(initialUUID, fakePID)]
  probeWithRetry → /proc/<fakePID>/fd or lsof → returns encDir/<newUUID>.jsonl
  match → OnRotate(initialUUID, newUUID) → Pool.RotateID → saveLocked

test phase 3 — post-rotation observation
  poll <home>/.pyry/test/sessions.json until len(sessions)==1 && id == newUUID
                                          (deadline 5s, gap 50ms)
  capture post := registry entry
  assertions:
    AC#1: post.id == newUUID && post.id != initialUUID
    AC#2: post.LastActiveAt.After(pre.LastActiveAt)
    AC#3a: post.id != initialUUID  (the pointer moved off the pre-rotation file)
    AC#3b: re-read registry once more after a small sleep — assert id stays newUUID
           (no background path reverts the pointer)
    AC#3c: drive `pyry list` via h.Run; assert stdout contains newUUID and not
           initialUUID  (independent observation through the control plane)

teardown (existing t.Cleanup path)
  SIGTERM → wait → SIGKILL grace → socket removed
  fake claude exits when its parent PTY closes (read on stdin returns EOF/EIO)
```

## Package structure

Three artefacts. The two new files live under `internal/e2e/`; the harness
edit is small.

### NEW `internal/e2e/internal/fakeclaude/main.go` (~80 lines, `package main`)

A standalone Go binary built once per test process via `sync.Once`,
analogous to `ensurePyryBuilt`. The `internal/` directory under
`internal/e2e/` keeps it visibility-fenced: only e2e tests can import it,
and `go test ./...` won't build it (no `_test.go` consumers outside e2e).

Behaviour:

```go
func main() {
    dir   := mustEnv("PYRY_FAKE_CLAUDE_SESSIONS_DIR")
    initU := mustEnv("PYRY_FAKE_CLAUDE_INITIAL_UUID")
    trig  := mustEnv("PYRY_FAKE_CLAUDE_TRIGGER")

    // Open the initial JSONL so the platform probe can see it on PID's fd table.
    f, err := os.OpenFile(filepath.Join(dir, initU+".jsonl"),
        os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
    if err != nil { fatal(err) }
    if _, err := f.WriteString("{}\n"); err != nil { fatal(err) }
    if err := f.Sync(); err != nil { fatal(err) }

    // Poll for trigger, then rotate ONCE. Subsequent triggers are ignored —
    // the test only needs the single /clear-shaped transition.
    rotated := false
    for {
        if _, err := os.Stat(trig); err == nil && !rotated {
            // Order matters: close OLD before opening NEW so the probe never
            // sees both paths simultaneously and the post-rotation probe
            // returns exactly the new path.
            _ = f.Close()
            newU := uuidV4()  // tiny crypto/rand v4 stem, no external dep
            nf, err := os.OpenFile(filepath.Join(dir, newU+".jsonl"),
                os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
            if err != nil { fatal(err) }
            if _, err := nf.WriteString("{}\n"); err != nil { fatal(err) }
            if err := nf.Sync(); err != nil { fatal(err) }
            f = nf
            _ = os.Remove(trig)
            rotated = true
        }
        time.Sleep(50 * time.Millisecond)
    }
}
```

Notes on the binary:
- Reads its three knobs from env (set by the harness via `cmd.Env`, inherited
  by the supervised child via `supervisor.go:234`'s
  `cmd.Env = append(os.Environ(), s.cfg.helperEnv...)`).
- No reads from stdin. The PTY parent (pyry) writes nothing useful; the
  child polls a file. PTY-as-stdin works without involvement.
- `uuidV4()` is a 30-line `crypto/rand`-backed helper — no external dep.
  The same canonical v4 stem shape (`xxxxxxxx-xxxx-4xxx-yxxx-xxxxxxxxxxxx`)
  the watcher's `uuidStemPattern` matches.
- Exits on SIGTERM (no signal handler needed — Go's default kills the
  process). Pyry's supervisor SIGTERMs the PTY child during teardown; the
  fake claude dies cleanly with the open fd auto-closed by the kernel.
- Idempotent against re-execs: pyry's supervisor restarts the child on exit.
  The test workflow exits cleanly (SIGTERM during harness teardown) before
  any backoff/restart can fire, so the "single rotation" property holds.

### NEW `internal/e2e/rotation_test.go` (~120 lines, `//go:build e2e`)

One test function, `TestE2E_RotationWatcher_DetectsClear`. Sketch:

```go
//go:build e2e
package e2e

import (
    "os"
    "path/filepath"
    "strings"
    "testing"
    "time"
)

func TestE2E_RotationWatcher_DetectsClear(t *testing.T) {
    home, regPath := newRegistryHome(t)  // reused from restart_test.go

    encDir := filepath.Join(home, ".claude", "projects", encodeWorkdir(home))
    if err := os.MkdirAll(encDir, 0o700); err != nil { t.Fatal(err) }

    const initialUUID = "11111111-1111-4111-8111-111111111111"
    initialPath := filepath.Join(encDir, initialUUID+".jsonl")
    // Plain WriteFile is enough — reconcile uses ModTime, the fake claude
    // re-opens the file with O_APPEND afterward so its FD becomes visible
    // to the platform probe.
    if err := os.WriteFile(initialPath, []byte("{}\n"), 0o600); err != nil {
        t.Fatal(err)
    }

    triggerPath := filepath.Join(home, "rotate.trigger")
    h := StartRotation(t, home, encDir, initialUUID, triggerPath)

    // Phase 1 — pre-rotation. Bootstrap should reconcile to initialUUID.
    pre := pollForRegistryID(t, regPath, initialUUID, 5*time.Second)

    // Phase 2 — trigger /clear-shaped rotation.
    if err := os.WriteFile(triggerPath, nil, 0o600); err != nil { t.Fatal(err) }

    // Phase 3 — post-rotation. The watcher must move the registry pointer.
    post := pollForRegistryChange(t, regPath, initialUUID, 5*time.Second)

    if post.ID == initialUUID {
        t.Fatalf("registry id never moved off pre-rotation UUID %s", initialUUID)
    }
    if !uuidStemPattern.MatchString(post.ID) {  // local copy of the UUID regex
        t.Errorf("post-rotation id %q is not a canonical UUIDv4", post.ID)
    }
    if !post.LastActiveAt.After(pre.LastActiveAt) {
        t.Errorf("LastActiveAt did not advance: pre=%s post=%s",
            pre.LastActiveAt.Format(time.RFC3339Nano),
            post.LastActiveAt.Format(time.RFC3339Nano))
    }

    // AC#3 — pointer is not reverted by a subsequent registry read or by a
    // control-plane query.
    time.Sleep(200 * time.Millisecond)
    again := readRegistry(t, regPath)
    if len(again.Sessions) != 1 || again.Sessions[0].ID != post.ID {
        t.Errorf("registry id reverted after stable wait: got %v want %s",
            again.Sessions, post.ID)
    }
    r := h.Run(t, "list")
    if r.ExitCode != 0 {
        t.Fatalf("pyry list exit=%d stderr=%s", r.ExitCode, r.Stderr)
    }
    out := string(r.Stdout)
    if !strings.Contains(out, post.ID) {
        t.Errorf("pyry list missing post-rotation id %s; got:\n%s", post.ID, out)
    }
    if strings.Contains(out, initialUUID) {
        t.Errorf("pyry list still references pre-rotation id %s; got:\n%s",
            initialUUID, out)
    }
}

// encodeWorkdir mirrors internal/sessions.encodeWorkdir. Local copy because
// the production fn is unexported; the rule is a one-liner and stable
// (lessons.md "Claude session storage on disk").
func encodeWorkdir(s string) string { ... }

// uuidStemPattern: local copy of the production regex. Same justification.
var uuidStemPattern = regexp.MustCompile(`^[0-9a-f]{8}-...$`)

// pollForRegistryID polls regPath until the single session's ID matches
// want, or deadline. Returns the matched entry. Fatals on timeout.
func pollForRegistryID(t, regPath, want, dl) registryEntry { ... }

// pollForRegistryChange polls regPath until the single session's ID is
// non-empty AND != avoid, or deadline. Returns the matched entry. Fatals.
func pollForRegistryChange(t, regPath, avoid, dl) registryEntry { ... }
```

The `registryEntry` / `registryFile` / `readRegistry` helpers already exist
in `restart_test.go` (same package) and are reused as-is. `newRegistryHome`
gives the short-prefix `MkdirTemp` path that keeps `<home>/pyry.sock`
under macOS's 104-byte `sun_path` budget — same constraint as
`TestE2E_Restart_PreservesActiveSessions`.

### EDIT `internal/e2e/harness.go` (~40 added lines)

Two additions:

1. New field on `Harness`:
   ```go
   // ClaudeSessionsDir is the encoded `<HomeDir>/.claude/projects/<enc>/`
   // path for the harness's workdir. Set only by StartRotation; nil-equivalent
   // ("") for the standard Start / StartIn paths that don't pin a workdir.
   ClaudeSessionsDir string
   ```

2. New constructor `StartRotation(t, home, sessionsDir, initialUUID, trigger)`
   that calls into a parameterised `spawn` variant. Signature:

   ```go
   // StartRotation spawns pyry with the test-only fake claude binary
   // (internal/e2e/internal/fakeclaude) as the supervised child, pinned to
   // home as its workdir so the encoded sessions dir is deterministic. The
   // fake claude reads sessionsDir, initialUUID, and trigger from env,
   // opens the initial jsonl on startup, and rotates to a fresh UUID once
   // when trigger appears on disk.
   //
   // Used only by rotation_test.go; the standard Start / StartIn keep using
   // /bin/sleep.
   func StartRotation(t *testing.T, home, sessionsDir, initialUUID, trigger string) *Harness
   ```

   Implementation reuses the existing `spawn` machinery by extending it to
   accept a `spawnOpts` struct, or by adding a sibling `spawnRotation`
   helper. Either shape is fine; the developer picks based on what reads
   cleaner. Concretely:

   ```go
   func spawn(t, home string) (... existing signature ...) {
       return spawnWith(t, home, spawnOpts{
           claudeBin: "/bin/sleep",
           claudeArgs: []string{"infinity"},
       })
   }

   type spawnOpts struct {
       claudeBin  string   // -pyry-claude
       claudeArgs []string // post-`--` args
       workdir    string   // -pyry-workdir; "" → flag omitted
       extraEnv   []string // appended after childEnv(home); for fake claude knobs
   }

   func spawnWith(t, home string, opts spawnOpts) (... same returns ...) {
       socket := filepath.Join(home, "pyry.sock")
       args := []string{
           "-pyry-socket="+socket, "-pyry-name=test",
           "-pyry-claude="+opts.claudeBin, "-pyry-idle-timeout=0",
       }
       if opts.workdir != "" { args = append(args, "-pyry-workdir="+opts.workdir) }
       args = append(args, "--")
       args = append(args, opts.claudeArgs...)
       cmd := exec.Command(ensurePyryBuilt(t), args...)
       cmd.Env = append(childEnv(home), opts.extraEnv...)
       ...
   }
   ```

   `StartRotation` builds the fake claude (via a sibling `ensureFakeClaudeBuilt`
   that mirrors `ensurePyryBuilt` — `sync.Once`, env-override
   `PYRY_E2E_FAKE_CLAUDE_BIN` for CI optimisation), then calls `spawnWith`
   with `claudeBin=fakeBin`, `workdir=home`, and the three env knobs.

   `StartIn` keeps its current signature unchanged — no behaviour drift in
   existing tests.

The `ensureFakeClaudeBuilt` helper is the parallel of `ensurePyryBuilt`:
one `sync.Once`-guarded `go build internal/e2e/internal/fakeclaude` per test
process, optional `PYRY_E2E_FAKE_CLAUDE_BIN` env-var short-circuit so CI
can pre-build both binaries once.

# Concurrency model

No new goroutines in test code — the test polls synchronously (existing
`pollForRegistryChange`-shape helpers, mirroring the readiness poll in
`waitForReady`).

The fake claude is single-goroutine: write→loop(stat trigger; rotate
once)→sleep. Pyry's supervisor exits the child via SIGTERM at teardown.

Pyry-side concurrency is unchanged: the rotation watcher runs in pyry's
existing errgroup goroutine (`pool.go:745`). The watcher's event loop and
all its callbacks (`Snapshot`, `IsAllocated`, `OnRotate` → `RotateID` →
`saveLocked`) are exactly what production runs — this test does not stub
or substitute them.

Lock order (unchanged from production): `Pool.mu` (taken in `RotateID`) →
`Session.lcMu` (briefly, to bump `lastActiveAt`).

# Error handling

| Failure                                                | Surfaced as                                              |
|--------------------------------------------------------|----------------------------------------------------------|
| pyry exits before becoming ready                       | `Start*` → `t.Fatalf` from `waitForReady` (existing)     |
| fake claude binary build fails                         | `ensureFakeClaudeBuilt` → `t.Fatalf`                     |
| fake claude exits unexpectedly (write/open error)      | pyry restarts it via supervisor backoff; the watcher would still see the second CREATE → rotation observable. If the test still fails the failure mode is captured in `h.Stderr` and surfaced via `t.Fatalf` from the post-rotation poll's deadline. |
| Watcher misses the rotation (probe race / dropped event) | `pollForRegistryChange` deadline → `t.Fatalf` with the registry contents and `h.Stderr` snapshot for diagnosis |
| Probe binary missing (`lsof` on macOS)                 | `pyry` log "rotation probe disabled" → watcher noops → poll timeout. The platform-probe absence story is ALREADY enforced at unit level (`probe_test.go`); reproducing it here would be belt-and-suspenders without a real failure mode. Accept the deadline-and-stderr diagnosis. |
| Rotation triggers but `lastActiveAt` does not advance  | AC#2 assertion fails with both timestamps printed       |
| `pyry list` fails or omits the new UUID                | AC#3c assertion fails with stdout printed for diagnosis |

The test uses `t.Fatalf` for liveness/poll deadlines (downstream assertions
have no meaning if the rotation never happened) and `t.Errorf` for the
property assertions on the post-rotation snapshot (so all AC failures show
in one run, not just the first).

# Testing strategy

## What this test covers that the unit test does not

- Real platform probe (`/proc/<pid>/fd` on Linux, `lsof` on macOS) reading
  a real child process's FD table — vs. the unit test's `dirProbe` shim.
- Real fsnotify events on a real directory — vs. the unit test's same path
  but with the rotation pre-staged via `os.WriteFile` (no held FD).
- Real `Pool.Run` errgroup wiring, control server in `Serve`, and registry
  on-disk path through `saveLocked` — vs. the unit test's bare `Pool` and
  in-memory check.
- The encoded-cwd path rule (`/` AND `.` → `-`) is exercised end-to-end —
  if the harness or the production resolver drift on this rule, the test
  observes the JSONL in the wrong dir and fails.

## What this test deliberately does NOT cover

- `TestPool_Run_StartsWatcher` is **not** removed in this ticket. The follow-
  up to retire it depends on this test demonstrating stability across
  several CI runs (per the ticket body's "if it passes consistently we have
  grounds to retire").
- Multi-session rotation is out of scope (per ticket). The bootstrap-only
  path is the primary `/clear` flow today.
- Watcher-while-claude-not-running is covered by unit tests; not duplicated
  here.

## Stability guards baked in

- Pre-creating `<initialUUID>.jsonl` before `StartRotation` means bootstrap
  reconciles to a known UUID. Without this, the test would have to handle
  a two-step sequence (random-bootstrap → initialUUID → newUUID) and the
  first rotation would be racy against the readiness gate.
- The fake claude **closes the old FD before opening the new one**.
  `watcher.go:167`'s exact-match check (`probe-reported open path ==
  CREATE event path`) requires that, at the moment the probe runs, the
  ONLY jsonl on the PID's fd table is the one whose CREATE just fired.
- The fake claude rotates **once**, then idles. Repeated triggers are
  ignored. This eliminates "did the watcher catch the right rotation"
  ambiguity.
- The `pollForRegistryChange` deadline is 5s — the watcher's
  `probeRetryDelays` total worst case is 250ms, plus fsnotify latency
  (sub-second), plus the saveLocked write. 5s gives 20× headroom.
- Trigger polling cadence (50ms) is the same order of magnitude as
  `probeRetryDelays`'s middle entry; fast enough that the test isn't
  bottlenecked on rotation-trigger latency.

## CI

Build tag `e2e` keeps the test out of `go test ./...`. The existing CI
e2e job already runs `go test -tags=e2e ./internal/e2e/...`, which will
pick up `rotation_test.go` automatically. No CI YAML change required.

The fake claude binary is built per test process (cached via `sync.Once`).
For the GitHub Actions e2e job, the per-process cost is ~200ms once. If
that becomes painful, the same `PYRY_E2E_BIN`-style override
(`PYRY_E2E_FAKE_CLAUDE_BIN`) lets CI pre-build it once per workflow run.

# Open questions

None expected to block implementation. Two minor judgment calls noted:

1. **Use `uuid` package or hand-roll v4?** Pyry's prod code hand-rolls UUIDs
   in `internal/sessions/id.go`. The fake claude can do the same (~10
   lines, `crypto/rand`) to avoid pulling a new dep into the test tree.
   Recommend: hand-roll, mirroring the prod approach.

2. **Where to put the fake claude binary path?** Two candidates:
   `internal/e2e/internal/fakeclaude/main.go` (visibility-fenced; only the
   e2e package can build it via `go build`) or `internal/e2e/fakeclaude/`
   (no fence, but slightly shorter import path). Recommend: the
   `internal/internal` form — the binary is a test fixture, not API
   surface; fencing it forecloses any accidental import from production
   code.

# Out of scope

- Retirement of `TestPool_Run_StartsWatcher`. That is a follow-up
  contingent on this test showing stable runs across several CI invocations.
- Multi-session `/clear` rotation (a single bootstrap session is the only
  supported case today).
- Rotation while pyry has no live child (covered by unit tests).
- Phase 1.2's `/clear` rotation handling (separate, untriaged feature).
- Any production-code change. The watcher, probe, registry write path, and
  reconcile path are exercised exactly as they ship; this ticket adds tests
  only.
