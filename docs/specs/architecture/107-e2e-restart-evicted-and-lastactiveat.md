# Spec: E2E restart-survival — evicted state + lastActiveAt timestamps

**Ticket:** [#107](https://github.com/pyrycode/pyrycode/issues/107)
**Size:** S
**Depends on:** #106 (harness restart primitive + first restart test, already shipped on `main`)

## Context

Phase 1.2 has unit-level coverage for two restart-survival invariants that the cap policy (#41) and warm-start reconciliation (#38) lean on:

1. **Evicted lifecycle state must persist across restart.** If a session was evicted before the daemon stopped, the new daemon must not silently re-promote it. `internal/sessions/session.go:46-50` parses `"evicted"` back to `stateEvicted` on warm start; `pool.New` (`pool.go:270-275`) feeds that into the bootstrap session's initial `lcState`. Today's only binary-level coverage is #106's `TestE2E_Restart_PreservesActiveSessions`, which by design does not vary the lifecycle field — it asserts active stays active.
2. **`lastActiveAt` timestamps must persist across restart.** The cap policy uses `lastActiveAt` for LRU ordering. If the field were truncated, normalised, or re-stamped to `time.Now()` during bootstrap, the LRU order would silently break. The unit tests in `internal/sessions/registry_test.go` cover the marshal/unmarshal roundtrip in isolation; nothing covers it across a daemon restart.

This ticket layers two more restart-survival tests onto the harness primitive shipped in #106 (`StartIn`, `Stop`). It introduces no new harness API.

### Why the tests work against today's pyry (no behaviour changes required)

The reasoning from #106's spec carries over verbatim and is the load-bearing invariant under both new tests: from the moment the test pre-writes `sessions.json` to the moment the second daemon's `loadRegistry` reads it back, no code path in pyry calls `saveLocked`. Briefly, on warm start:

1. `loadRegistry` reads the file (`pool.go:257`).
2. `pickBootstrap` finds the `bootstrap: true` entry; non-bootstrap entries are NOT materialised into the in-memory pool (`pool.go:270-284`).
3. `reg != nil` → cold-start save is skipped (`pool.go:342-346`).
4. `reconcileBootstrapOnNew` no-ops because the test HOME has no `~/.claude/projects/<encoded-cwd>` directory (`reconcile.go:104-110`).
5. The bootstrap session enters its lifecycle goroutine in whichever state `parseLifecycleState` returned. Idle timer disabled (`-pyry-idle-timeout=0`); supervised child is `/bin/sleep infinity` and only exits on SIGTERM. No state change → no save.
6. SIGTERM cancels ctx → the lifecycle goroutine returns `ctx.Err()` BEFORE `transitionTo` (`session.go:233-238` for active path; the evicted path is a `select` on ctx + `activateCh` and returns ctx.Err() the same way); no terminal save.

For non-bootstrap entries the chain is even simpler: pyry never touches them. The on-disk bytes for those entries are byte-stable across the restart because pyry reads but does not rewrite.

The two new tests are, in part, locking in this no-save-without-state-change invariant. Future tickets that materialise non-bootstrap entries into the pool, or that re-stamp `lastActiveAt` during bootstrap, will have to update both production and these tests in lockstep — which is what we want.

## Design

### Files

| File | Change | Lines (approx) |
|---|---|---|
| `internal/e2e/restart_test.go` | Extract small file-local fixture helper; add two tests | +140 |

Total: 1 file, ~140 lines under `//go:build e2e`. No new files. Default `go test ./...` is unaffected.

### Helper extraction (rule of three)

Once #107 lands the file holds three tests with identical HOME/registry-dir bootstrapping:

- `TestE2E_Restart_PreservesActiveSessions` (#106, already in tree)
- `TestE2E_Restart_PreservesEvictedSessions` (this ticket)
- `TestE2E_Restart_LastActiveAtSurvives` (this ticket)

Per the AC's rule-of-three, extract the shared prefix into a small file-local helper. Keep it package-internal — do not export, do not move into `harness.go`. The shared prefix is the four lines of `os.MkdirTemp` (sun_path workaround, see #106's lessons), `t.Cleanup(RemoveAll)`, and `mkdir -p <home>/.pyry/test`.

```go
// newRegistryHome creates a short-named temp HOME (sun_path-safe), pre-creates
// <home>/.pyry/test/, registers cleanup, and returns the home dir and the
// sessions.json path the harness's -pyry-name=test daemon will read.
func newRegistryHome(t *testing.T) (home, regPath string) {
    t.Helper()
    home, err := os.MkdirTemp("", "pyry-rs-*")
    if err != nil {
        t.Fatalf("mkdir home: %v", err)
    }
    t.Cleanup(func() { _ = os.RemoveAll(home) })

    regDir := filepath.Join(home, ".pyry", "test")
    if err := os.MkdirAll(regDir, 0o700); err != nil {
        t.Fatalf("mkdir registry dir: %v", err)
    }
    return home, filepath.Join(regDir, "sessions.json")
}
```

Refactor `TestE2E_Restart_PreservesActiveSessions` to call `newRegistryHome` as part of this ticket — that is the third caller that justifies the extraction. Net diff in the existing test: replace ~7 lines of setup with 1 call.

The `writeRegistry` / `readRegistry` / `mustReadFile` helpers from #106 stay as-is; both new tests reuse them unchanged.

The `registryEntry` / `registryFile` mirror types from #106 stay as-is. Reused unchanged.

### Test 1: `TestE2E_Restart_PreservesEvictedSessions`

#### Fixture choice

Three sessions:

- **Bootstrap, active.** Standard warm-start path; keeps the harness's ready gate working the conventional way. The supervisor spawns `/bin/sleep infinity` and the control server comes up.
- **Non-bootstrap, evicted.** The session whose lifecycle state the test cares about. Pyry never materialises it; the on-disk bytes for this entry are the assertion target.
- **Non-bootstrap, active.** A control entry — present so the assertion "evicted stays evicted" is meaningful next to a sibling that is provably not evicted on disk.

This deliberately avoids the bootstrap-evicted permutation. That path (warm-starting the bootstrap *itself* in `stateEvicted`) is functionally distinct: the lifecycle goroutine enters `runEvicted` instead of spawning the child. It would also implicitly probe whether the harness's ready gate races the absent-child case. That is a useful test, but it is a different shape from "evicted session survives" — it is "daemon comes up cleanly with an evicted bootstrap" — and it belongs in its own ticket so failures isolate cleanly. Keep this ticket scoped to non-bootstrap evicted survival.

The lifecycle string written to disk is `"evicted"` (matches `lifecycleState.String()` at `session.go:33-39`). The active string is `"active"` (the default branch). Do not invent or guess values; these are the two strings the production code emits and parses.

#### Sketch

```go
func TestE2E_Restart_PreservesEvictedSessions(t *testing.T) {
    home, regPath := newRegistryHome(t)

    now := time.Now().UTC().Truncate(time.Second)
    pre := registryFile{
        Version: 1,
        Sessions: []registryEntry{
            {
                ID:             "11111111-1111-4111-8111-111111111111",
                CreatedAt:      now,
                LastActiveAt:   now,
                Bootstrap:      true,
                LifecycleState: "active",
            },
            {
                ID:             "22222222-2222-4222-8222-222222222222",
                Label:          "evicted-one",
                CreatedAt:      now,
                LastActiveAt:   now,
                LifecycleState: "evicted",
            },
            {
                ID:             "33333333-3333-4333-8333-333333333333",
                Label:          "active-control",
                CreatedAt:      now,
                LastActiveAt:   now,
                LifecycleState: "active",
            },
        },
    }
    writeRegistry(t, regPath, pre)

    h1 := StartIn(t, home)
    h1.Stop(t)
    h2 := StartIn(t, home)
    _ = h2

    got := readRegistry(t, regPath)

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
            t.Errorf("session %s lifecycle_state: got %q want %q\nfile:\n%s",
                want.ID, have.LifecycleState, want.LifecycleState,
                mustReadFile(t, regPath))
        }
    }
    if len(got.Sessions) != len(pre.Sessions) {
        t.Errorf("session count: got %d want %d\nfile:\n%s",
            len(got.Sessions), len(pre.Sessions), mustReadFile(t, regPath))
    }
}
```

The assertion shape mirrors #106's: per-entry field check via `t.Errorf` so all mismatches surface in one run, plus a count check that catches "pyry rewrote the file with only the bootstrap entry". The evicted-canary fails on either silent re-promotion (lifecycle_state flips to "active") or silent drop (entry missing).

### Test 2: `TestE2E_Restart_LastActiveAtSurvives`

#### Fixture choice

Three sessions with deliberately spread `lastActiveAt` values:

- bootstrap, `lastActiveAt = now`
- non-bootstrap, `lastActiveAt = now - 10*time.Minute`
- non-bootstrap, `lastActiveAt = now - 1*time.Hour`

The 10-minute and 1-hour offsets are far larger than any plausible JSON-roundtrip drift and far larger than any plausible test wall-clock. They make a regression that re-stamps `lastActiveAt = time.Now()` impossible to mistake for noise: a 10-minute or 1-hour offset will *not* survive re-stamping.

All three sessions are in `"active"` lifecycle state — this test is about timestamp survival, not lifecycle survival. Cross-axis combinations are not the AC's ask and would confuse failure isolation.

#### Equality vs. tolerance

The AC says "byte-equal (or within a tight tolerance — single-millisecond round-trip is acceptable; do not allow re-stamping to time.Now())".

Use `time.Time.Equal` per entry. Justification:

- `time.Time.MarshalJSON` writes RFC3339Nano (`time/format_rfc3339.go`). `UnmarshalJSON` parses it back. The roundtrip is exact for any monotonic-stripped UTC time (and the test pre-writes UTC, monotonic-stripped via the unmarshal that immediately follows the writeRegistry). So `time.Time.Equal` against the pre-write value should hold byte-exact today.
- `time.Time.Equal` ignores location/monotonic-clock differences. If a future change starts re-encoding lastActiveAt through, e.g., `time.Now().UTC()` (which strips monotonic), `Equal` still passes — that's the AC's "tight tolerance".
- The catastrophic regression — re-stamping to `time.Now()` — produces a delta of seconds-to-hours, which `Equal` rejects loudly.

Do not assert on byte-identity of the registry file. The marshal indent and field-order in `registryFile` are stable today, but coupling the test to byte-identity inverts the dependency direction (test fails on benign formatting changes). Per-field `Equal` is more durable.

Note: ensure the test reads back the pre-write expectation through the same JSON marshal-then-unmarshal trip the daemon uses. Concretely: after `writeRegistry`, re-read the file via `readRegistry` to capture the post-marshal `time.Time` values, and use those as the "want" values in the post-restart comparison. This sidesteps the trap where `t.Time` written via `MarshalIndent` retains monotonic-clock state in the original Go value but strips it after JSON roundtrip — comparing the in-memory pre-write value to the post-restart parsed value would otherwise diverge on monotonic-clock alone, even though both files are byte-identical on disk.

#### Sketch

```go
func TestE2E_Restart_LastActiveAtSurvives(t *testing.T) {
    home, regPath := newRegistryHome(t)

    now := time.Now().UTC().Truncate(time.Second)
    pre := registryFile{
        Version: 1,
        Sessions: []registryEntry{
            {
                ID:             "11111111-1111-4111-8111-111111111111",
                CreatedAt:      now,
                LastActiveAt:   now,
                Bootstrap:      true,
                LifecycleState: "active",
            },
            {
                ID:             "22222222-2222-4222-8222-222222222222",
                Label:          "ten-min-old",
                CreatedAt:      now.Add(-10 * time.Minute),
                LastActiveAt:   now.Add(-10 * time.Minute),
                LifecycleState: "active",
            },
            {
                ID:             "33333333-3333-4333-8333-333333333333",
                Label:          "one-hour-old",
                CreatedAt:      now.Add(-1 * time.Hour),
                LastActiveAt:   now.Add(-1 * time.Hour),
                LifecycleState: "active",
            },
        },
    }
    writeRegistry(t, regPath, pre)

    // Re-read to obtain the canonical post-marshal time.Time values.
    // See "Equality vs. tolerance" — this avoids monotonic-clock false negatives.
    want := readRegistry(t, regPath)
    wantByID := make(map[string]registryEntry, len(want.Sessions))
    for _, e := range want.Sessions {
        wantByID[e.ID] = e
    }

    h1 := StartIn(t, home)
    h1.Stop(t)
    h2 := StartIn(t, home)
    _ = h2

    got := readRegistry(t, regPath)
    if len(got.Sessions) != len(want.Sessions) {
        t.Fatalf("session count: got %d want %d\nfile:\n%s",
            len(got.Sessions), len(want.Sessions), mustReadFile(t, regPath))
    }
    for _, have := range got.Sessions {
        w, ok := wantByID[have.ID]
        if !ok {
            t.Errorf("unexpected session %s after restart", have.ID)
            continue
        }
        if !have.LastActiveAt.Equal(w.LastActiveAt) {
            t.Errorf("session %s last_active_at: got %s want %s",
                have.ID, have.LastActiveAt.Format(time.RFC3339Nano),
                w.LastActiveAt.Format(time.RFC3339Nano))
        }
    }
}
```

### What this ticket explicitly does NOT change

- No new harness API. `StartIn` and `Stop` from #106 are sufficient.
- No new exported types. Reuses the `registryEntry`/`registryFile` mirror types and `writeRegistry`/`readRegistry`/`mustReadFile` helpers from #106.
- No production-code change in `internal/sessions` or anywhere else. The two new tests are pure verification of existing behaviour.

## Concurrency model

No new goroutines. Each test spawns two daemon processes serially (`StartIn` → `Stop` → `StartIn`). The harness's existing wait-goroutine and `cleanupOnce` semantics from #106 apply unchanged. The two harnesses (`h1`, `h2`) own independent state; `t.Cleanup` runs LIFO so `h2.teardown` fires first against the live second daemon, then `h1.teardown` is a no-op (already torn down via `Stop`).

`t.Parallel` is not requested for these tests. Each test fixes a HOME and a socket path on disk; parallel runs of the same test would collide. Defer parallelism if e2e wall-clock pressure surfaces.

## Error handling

| Failure | Handling |
|---|---|
| `newRegistryHome` mkdir failures | `t.Fatalf` |
| `StartIn` ready-deadline exceeded | existing `t.Fatalf` from `waitForReady` |
| `Stop` waits past `termGrace + killGrace` | existing `t.Logf` from `teardown`; second `StartIn` will then fail on stale-socket if first daemon still alive — surfaces the leak loudly |
| Registry malformed after restart | `readRegistry` calls `t.Fatalf` with file contents dumped |
| Per-session field mismatch | `t.Errorf` (not Fatalf) so all mismatches report in one run |
| Session count mismatch | `t.Fatalf` (evicted test) / `t.Fatalf` (lastActiveAt test) with file dumped |

The "second daemon comes up against a corrupt registry" case is implicitly covered: `loadRegistry` returns an error on malformed JSON, `pool.New` propagates, daemon exits before ready, `waitForReady` fails with stderr captured. Same shape as #106.

## Testing strategy

Self-validation:

```bash
go test -tags=e2e -race -run='TestE2E_Restart_(PreservesEvicted|LastActiveAtSurvives)' -v ./internal/e2e/...
```

Cross-check before merging:

```bash
# All three restart tests pass together (smoke that the helper refactor didn't break #106).
go test -tags=e2e -race -run='TestE2E_Restart_' -v ./internal/e2e/...

# Full e2e suite still passes.
go test -tags=e2e -race ./internal/e2e/...

# Default suite still untouched.
go test -race ./...
```

The tests exercise a real binary against a real on-disk file. The supervised "claude" remains `/bin/sleep infinity` (existing harness default), keeping PTY/child-startup variability out of the assertions.

## Open questions

None. The lifecycle string values (`"active"`, `"evicted"`), registry path (`<HOME>/.pyry/test/sessions.json` under `-pyry-name=test`), warm-start invariants (no save without state change), and harness primitives (`StartIn`, `Stop`) are all read in this spec.

## Out of scope (explicit)

- **Bootstrap-evicted warm-start.** The permutation where the bootstrap session itself starts in `stateEvicted` (lifecycle goroutine enters `runEvicted`, no claude child). Functionally distinct path; deserves its own ticket so failures isolate cleanly.
- **Materialising non-bootstrap entries in `pool.New`.** Future ticket. These tests are written to survive that change in either direction (entries dropped, or entries materialised then re-saved with byte-stable content).
- **Asserting on byte-identity of the registry file.** Couples to JSON formatting, brittle. Field-level assertions are durable.
- **Asserting on `CreatedAt` survival.** AC asks for `lastActiveAt`; CreatedAt survival is implicitly covered by the existing assertion shape from #106 (entry must still be present and the count must match, which transitively requires CreatedAt to roundtrip cleanly).
- **`t.Parallel` migration.** Defer until wall-clock pressure surfaces.
- **Promoting `registryEntry`/`registryFile` to package-level or exporting `internal/sessions` types.** Three test callers is enough to justify a file-local helper for HOME setup; it is not enough to invert the dependency direction on the production schema types.
- **Replacing `mustReadFile`-on-mismatch dumping with structural diffs.** Cheap, readable, sufficient.
