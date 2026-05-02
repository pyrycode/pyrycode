# Spec: E2E harness restart primitive + active-sessions persistence test

**Ticket:** [#106](https://github.com/pyrycode/pyrycode/issues/106)
**Size:** S
**Depends on:** #68 + #69 (harness already shipped)

## Context

Phase 1.2 has shipped the registry (#34), reconcile (#38), rotation watcher (#39), idle eviction (#40), and the active-cap (#41). All five have unit-level coverage; none has binary-level coverage of the central guarantee — *sessions persist across daemon restart*. That guarantee is what makes the whole phase load-bearing, and the only honest way to test it is at the binary boundary: spawn pyry, kill it, spawn it again, prove the on-disk registry survived.

The current harness (`internal/e2e/harness.go`, from #69) has two concrete gaps blocking that test:

1. `Start(t)` always allocates a fresh `t.TempDir()` for `HOME`. There is no way to spawn a daemon against a pre-existing `HOME` that the test has pre-populated.
2. There is no public way to stop the daemon mid-test while preserving disk state. `teardown` is unexported and runs only via `t.Cleanup`.

This ticket closes both gaps with the minimum API surface that satisfies the ACs, and ships exactly one concrete test (`TestE2E_Restart_PreservesActiveSessions`) that proves the restart-survival guarantee at the binary boundary.

### Why the test works against today's pyry (no behaviour changes required)

The first daemon's startup path against a pre-populated registry is:

1. `loadRegistry` reads the file (`pool.go:257`).
2. `pickBootstrap` picks the lone `bootstrap: true` entry; non-bootstrap entries are *not* materialised into the in-memory pool.
3. `reg != nil` → cold-start save is skipped (`pool.go:342-346`).
4. `reconcileBootstrapOnNew` scans `~/.claude/projects/<encoded-cwd>` — under the test HOME this directory does not exist, so reconcile silently no-ops (`reconcile.go:104-110`); no `RotateID`, no `saveLocked`.
5. Bootstrap session enters `runActive`. Idle timer is disabled (`-pyry-idle-timeout=0`). Supervised child is `/bin/sleep infinity` — exits only on SIGTERM.
6. SIGTERM cancels ctx → `runActive` returns `ctx.Err()` BEFORE `transitionTo(stateEvicted)` (`session.go:233-238`); no terminal save.

Net effect: from the moment the test pre-writes `sessions.json` to the moment the second daemon's `loadRegistry` reads it back, no code path in pyry calls `saveLocked`. The on-disk file is byte-stable across the restart. The non-bootstrap entries the test pre-populated are preserved on disk *because pyry does not touch them*, not because pyry materialises them — that is the realistic-today shape of the guarantee, and it is what the AC verifies.

This invariant (no save without state change) is what the test is, in part, locking in. Future tickets that materialise non-bootstrap entries into the pool will need to preserve their lifecycle state across restart explicitly; this test will then catch any regression.

## Design

### Files

| File | Change | Lines (approx) |
|---|---|---|
| `internal/e2e/harness.go` | Add `StartIn`, add `Stop`, refactor `Start` to delegate, update package doc | +30 |
| `internal/e2e/restart_test.go` | New file: one test | +85 |

Total: 2 files, ~115 lines under `//go:build e2e`. Default `go test ./...` is unaffected.

### New harness surface

Two additions, both minimal.

```go
// StartIn behaves like Start but uses the caller-supplied home directory
// instead of allocating a fresh t.TempDir(). The directory must already
// exist; pre-populate it (e.g. <home>/.pyry/test/sessions.json) before
// calling StartIn to drive a daemon against a chosen on-disk state. The
// caller still owns the directory's lifecycle — StartIn does not register
// it with t.Cleanup. Use Start(t) for the common case.
func StartIn(t *testing.T, home string) *Harness

// Stop gracefully terminates the daemon (SIGTERM, grace, escalate to SIGKILL
// matching t.Cleanup teardown), waits for the process to exit, and removes
// the socket file. HomeDir is left intact on disk so the same directory can
// be passed to a subsequent StartIn for a restart-shaped test.
//
// Idempotent with the t.Cleanup teardown registered by Start/StartIn:
// whichever path fires first wins; the other is a no-op (sync.Once).
func (h *Harness) Stop(t *testing.T)
```

Implementation:

- `StartIn` is the new workhorse: factor the body of today's `Start` into `StartIn(t, home)`, then redefine `Start(t) *Harness { return StartIn(t, t.TempDir()) }`. Net diff in `Start`: one line.
- `Stop` is a public wrapper around the existing `teardown(t)` method. `cleanupOnce` already guards single-fire; no new state needed. (Don't rename `teardown` — keep the name internal to make the public/private split obvious to future readers.)

These are the *only* new public symbols. No `Options` struct, no method on `Harness` for "respawn in place" — adding them today would be speculative. `StartIn` accepts a positional `home` arg; if a future ticket needs a second knob, the migration to a struct (`Options{Home: ...}`) is mechanical and non-breaking on existing callers if `StartIn` stays as a thin alias.

### Package doc update

Append to the current doctop comment in `harness.go` (after the existing usage example, before `package e2e`):

```go
// To prove an on-disk invariant survives daemon restart, pre-populate HOME
// before the first Start, Stop the first daemon, and StartIn a second
// daemon against the same HOME:
//
//	home := t.TempDir()
//	if err := os.MkdirAll(filepath.Join(home, ".pyry", "test"), 0o700); err != nil {
//	    t.Fatal(err)
//	}
//	if err := os.WriteFile(filepath.Join(home, ".pyry", "test", "sessions.json"),
//	    []byte(registryJSON), 0o600); err != nil {
//	    t.Fatal(err)
//	}
//
//	h1 := e2e.StartIn(t, home)
//	h1.Stop(t)
//
//	h2 := e2e.StartIn(t, home)
//	// h2.HomeDir == home; assert on the registry file directly.
```

Use the snippet exactly as written — no contractions, no condensation. `os.MkdirAll` is the explicit pre-populate step the AC's "pre-populated with a registry file" implies; the e2e package shouldn't paper over it inside the harness.

### The test: `TestE2E_Restart_PreservesActiveSessions`

New file `internal/e2e/restart_test.go`. Build tag `//go:build e2e`. Reuses the `e2e` package — no helper relocation, no exported types from `internal/sessions` (the registry types are unexported, so the test writes the JSON literally).

#### Sketch

```go
//go:build e2e

package e2e

import (
    "encoding/json"
    "os"
    "path/filepath"
    "testing"
    "time"
)

// registryEntry mirrors the on-disk shape used by internal/sessions. Defined
// locally because the production type is unexported. The schema is small and
// stable; if it grows a field, this struct grows the field too — the point of
// the test is the restart, not chasing schema drift.
type registryEntry struct {
    ID             string    `json:"id"`
    Label          string    `json:"label"`
    CreatedAt      time.Time `json:"created_at"`
    LastActiveAt   time.Time `json:"last_active_at"`
    Bootstrap      bool      `json:"bootstrap,omitempty"`
    LifecycleState string    `json:"lifecycle_state,omitempty"`
}

type registryFile struct {
    Version  int             `json:"version"`
    Sessions []registryEntry `json:"sessions"`
}

func TestE2E_Restart_PreservesActiveSessions(t *testing.T) {
    home := t.TempDir()
    regDir := filepath.Join(home, ".pyry", "test")
    if err := os.MkdirAll(regDir, 0o700); err != nil {
        t.Fatalf("mkdir registry dir: %v", err)
    }
    regPath := filepath.Join(regDir, "sessions.json")

    now := time.Now().UTC().Truncate(time.Second)
    pre := registryFile{
        Version: 1,
        Sessions: []registryEntry{
            {
                ID:             "11111111-1111-4111-8111-111111111111",
                Label:          "",
                CreatedAt:      now,
                LastActiveAt:   now,
                Bootstrap:      true,
                LifecycleState: "active",
            },
            {
                ID:             "22222222-2222-4222-8222-222222222222",
                Label:          "second",
                CreatedAt:      now,
                LastActiveAt:   now,
                LifecycleState: "active",
            },
        },
    }
    writeRegistry(t, regPath, pre)

    h1 := StartIn(t, home)
    h1.Stop(t)

    if _, err := os.Stat(regPath); err != nil {
        t.Fatalf("registry file gone after first daemon Stop: %v", err)
    }

    h2 := StartIn(t, home)
    _ = h2 // ready-gate already passed inside StartIn; teardown via t.Cleanup.

    got := readRegistry(t, regPath)

    if got.Version != pre.Version {
        t.Errorf("registry version: got %d want %d", got.Version, pre.Version)
    }
    if len(got.Sessions) != len(pre.Sessions) {
        t.Fatalf("session count: got %d want %d\nfile:\n%s",
            len(got.Sessions), len(pre.Sessions), mustReadFile(t, regPath))
    }
    byID := make(map[string]registryEntry, len(got.Sessions))
    for _, e := range got.Sessions {
        byID[e.ID] = e
    }
    for _, want := range pre.Sessions {
        have, ok := byID[want.ID]
        if !ok {
            t.Errorf("session %s missing after restart", want.ID)
            continue
        }
        if have.LifecycleState != want.LifecycleState {
            t.Errorf("session %s lifecycle_state: got %q want %q",
                want.ID, have.LifecycleState, want.LifecycleState)
        }
        if have.Bootstrap != want.Bootstrap {
            t.Errorf("session %s bootstrap: got %v want %v",
                want.ID, have.Bootstrap, want.Bootstrap)
        }
    }
}

func writeRegistry(t *testing.T, path string, reg registryFile) {
    t.Helper()
    data, err := json.MarshalIndent(reg, "", "  ")
    if err != nil {
        t.Fatalf("marshal registry: %v", err)
    }
    if err := os.WriteFile(path, data, 0o600); err != nil {
        t.Fatalf("write registry: %v", err)
    }
}

func readRegistry(t *testing.T, path string) registryFile {
    t.Helper()
    data, err := os.ReadFile(path)
    if err != nil {
        t.Fatalf("read registry: %v", err)
    }
    var reg registryFile
    if err := json.Unmarshal(data, &reg); err != nil {
        t.Fatalf("parse registry: %v\nfile:\n%s", err, data)
    }
    return reg
}

func mustReadFile(t *testing.T, path string) string {
    t.Helper()
    data, err := os.ReadFile(path)
    if err != nil {
        return "(unreadable: " + err.Error() + ")"
    }
    return string(data)
}
```

#### Why these specific assertions

- **Count match.** Catches "pyry rewrote the file with only the bootstrap entry" — the most likely regression if a future patch starts calling `saveLocked` during restart-time paths. The non-bootstrap entry is the canary.
- **lifecycle_state preserved.** The AC's explicit promise; second daemon must read back the string the test wrote.
- **bootstrap flag preserved.** Same reason; if pyry rewrites and synthesises a fresh bootstrap, this catches it.
- **No assertion on byte-identity of the file.** `json.MarshalIndent` and `Pool.saveLocked`'s encoder both produce stable output, but coupling the test to byte-identity would invert the dependency direction (the test would fail every time the marshal indent changes). Field-level assertions are more durable.
- **No assertion on `LastActiveAt` equality.** The bootstrap entry's `lastActiveAt` is loaded by `pool.New` and *could* be touched by a future state-change path during run; pinning it to the pre-write value would couple to today's no-save invariant in a way that breaks the test the moment a benign change ships. The AC says "lifecycle state preserved" — that's what we assert.

#### UUID choice

Hand-written canonical UUIDv4-shaped strings (`uuidStemPattern` in `reconcile.go:18` is `^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`). Using literals avoids a dependency on `internal/sessions.NewID()` and keeps the test reproducible. `4xxx` and `8xxx` blocks satisfy the v4 + variant invariant, though pyry doesn't actually validate that.

### Why this file, not extending `cli_verbs_test.go`

`cli_verbs_test.go` is about *CLI surface coverage* (one test per shipped verb). This test is about *daemon-level disk-state survival*; it doesn't drive a CLI verb. A separate file keeps each file's purpose legible as we accumulate tests, mirroring the `harness_test.go` (harness mechanics) vs. `cli_verbs_test.go` (verb surface) split #52 already established.

### Reusing the same socket path across the two spawns

`StartIn` derives `socket := filepath.Join(home, "pyry.sock")` (same as today's `Start`). Both spawns therefore use the same socket path — the realistic case. The second daemon's `Server.Listen` (`internal/control/server.go:131-160`) handles a stale socket file via dial-probe → ECONNREFUSED → `os.Remove` → `net.Listen`; no test-level coordination required. By the time `Stop` returns, `cmd.Wait` has reaped the first process, the listener fd is closed, and ECONNREFUSED is the deterministic outcome.

The harness's defensive `os.Remove(h.SocketPath)` in teardown belt-and-suspenders the SIGKILL path. In the SIGTERM path the daemon's own `defer ctrl.Close()` removes the socket; in the SIGKILL path the harness removes it. Either way, the second `StartIn`'s readiness gate (`net.Dial` succeeds) is the only thing the test waits on.

## Concurrency model

No new goroutines. Per-test:

- `StartIn` spawns one daemon process and one wait-goroutine (existing pattern).
- `Stop` cancels the daemon and joins via the existing `doneCh`.
- `StartIn` (second call) spawns a fresh `exec.Cmd` and a fresh wait-goroutine. The first harness's resources are already drained.

`cleanupOnce` guarantees:

- If the test calls `Stop` and then returns: t.Cleanup's call to `teardown` is a no-op.
- If the test panics or t.Fatals before `Stop`: t.Cleanup's `teardown` runs; `Stop` is never called.
- If the test calls `Stop` twice (e.g. defensive): second call is a no-op.

The two harnesses (`h1`, `h2`) own independent `cleanupOnce` / `doneCh` / `cmd` — they do not share state. `t.Cleanup` runs LIFO, so `h2.teardown` fires first (against the live second daemon), then `h1.teardown` (no-op, already torn down via `Stop`).

`t.Parallel` is not requested; the test holds a fixed `HOME` and a fixed socket path on disk. If two instances of this test ran in parallel they'd collide on the socket. Defer parallelism if e2e wall-clock becomes an issue.

## Error handling

| Failure | Handling |
|---|---|
| `StartIn(t, home)` ready-deadline exceeded | existing `t.Fatalf` from `waitForReady` (unchanged) |
| `Stop` waits past `termGrace + killGrace` | existing `t.Logf` from `teardown` (unchanged); test still proceeds — the second `StartIn` will fail on stale-socket if the first daemon is still alive, surfacing the leak loudly |
| Registry file missing after first `Stop` | `t.Fatalf` with the err string — catches a regression where pyry deletes the registry on shutdown |
| Session count mismatch | `t.Fatalf` with the file contents dumped, so CI logs show the diff |
| Per-session field mismatch | `t.Errorf` (not Fatalf) so all mismatches are reported in one run |

The "second daemon comes up against a now-corrupt registry" failure mode is implicitly tested: `loadRegistry` returns an error on malformed JSON, `pool.New` propagates, and the daemon exits before ready — `waitForReady` then `t.Fatalf`s with the captured stderr. Exactly the failure shape the AC wants.

## Testing strategy

Self-validation:

```bash
go test -tags=e2e -race -run='TestE2E_Restart_PreservesActiveSessions' -v ./internal/e2e/...
```

Manual cross-check before merging:

```bash
# Full e2e suite: harness smoke, CLI verbs, restart — all pass.
go test -tags=e2e -race ./internal/e2e/...

# Default suite still untouched.
go test -race ./...
```

The test exercises a real binary against a real (pre-populated, then rewritten-by-pyry-or-not) on-disk file. There is no mocking. The supervised "claude" remains `/bin/sleep infinity` (existing harness default), keeping PTY/child-startup variability out of the assertions.

## Open questions

None. The code paths involved (`pool.New`, `loadRegistry`, `pickBootstrap`, `reconcileBootstrapOnNew`, `Server.Listen`'s stale-socket handling) are all read in this spec; the test design follows from the existing invariants.

## Out of scope (explicit)

- Driving session creation through CLI verbs. The ticket's Technical Notes call this out — `pyry` does not expose `session-create`/`mark-active` verbs today; pre-writing the registry is the supported test pattern.
- Asserting on `LastActiveAt` equality. See "Why these specific assertions" above.
- Asserting on byte-identity of the registry file. Same.
- Materialising non-bootstrap entries into `pool.sessions` in `pool.New`. That is a future ticket; this test will catch regressions in either direction (entries dropped, entries materialised then dropped) at the disk level.
- An `Options` struct for `StartIn`. Today only one knob is needed. A struct can be introduced non-breakingly later.
- `t.Parallel` migration. Defer until wall-clock pressure surfaces.
- Removing the test's local `registryFile`/`registryEntry` mirror types in favour of importing `internal/sessions`. The production types are unexported; exporting them solely for this test would invert the dependency direction.
