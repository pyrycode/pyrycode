# Rotation Watcher Pattern (`rotation_test.go`, #120)

`TestE2E_RotationWatcher_DetectsClear` is the consumer for #122's
fake-claude binary and #123's `StartRotation` primitive. It drives a
real `pyry` daemon through one `/clear`-shaped JSONL rotation and
asserts the registry's tracked id follows. Production code (the
fsnotify watcher, the real platform probe — `/proc/<pid>/fd` on Linux,
`lsof` on macOS — `Pool.RotateID`, `saveLocked`) is exercised exactly as
it ships; only the supervised child is replaced with `fakeclaude`.
(Pre-#839 this list also named `reconcileBootstrapOnNew` — removed; see
below.)

### Sequence

```
1.  newRegistryHome(t)                         // home + regPath
2.  sessionsDir := <home>/.claude/projects/<encoded(home)>
3.  os.MkdirAll(sessionsDir, 0o700)
4.  os.WriteFile(<sessionsDir>/<initialUUID>.jsonl, "{}\n", 0o600)
5.  StartRotation(t, home, sessionsDir, initialUUID, trigger)
                                                // socket ready;
                                                // #861: seedBootstrapRegistry
                                                // writes initialUUID into the
                                                // registry before spawn, so
                                                // Pool.New's warm-start
                                                // (pickBootstrap) picks it
                                                // directly — no scan/rotate
6.  pre := waitForBootstrapID(t, regPath, initialUUID, 5s)
7.  os.WriteFile(trigger, nil, 0o600)            // fake-claude rotates
8.  post := waitForBootstrapIDChange(t, regPath, initialUUID, 5s)
9.  assert uuidStemPattern.MatchString(post.ID)
10. assert post.LastActiveAt.After(pre.LastActiveAt)
11. time.Sleep(200ms); after := readBootstrap(t, regPath)
12. assert after.ID == post.ID                   // no path reverts
```

### Pre-create the initial JSONL before `StartRotation` (mechanism updated by #861/#839)

Pre-#861, this pre-created file did the id-establishing work directly:
`reconcileBootstrapOnNew` ran synchronously inside `Pool.New`, before the
control socket started listening, and rotated the bootstrap id to
`<initialUUID>` if it was the most-recent jsonl in the watched dir. #839
deleted that scan outright.

**Current mechanism:** `StartRotation` calls `seedBootstrapRegistry(t, home,
initialUUID)` (#861) immediately before spawning the daemon, writing a
`bootstrap:true` registry entry for `initialUUID` directly into
`sessions.json`. `Pool.New`'s warm-start branch (`pickBootstrap`) then picks
it up with no directory scan at all — the daemon spawns claude with
`--session-id initialUUID` from the very first spawn. Step 6's poll finds it
on the first read, same as before.

The `<initialUUID>.jsonl` pre-create (step 4) is now a **confirming no-op**
for this particular test — the daemon's id already agrees with the on-disk
file by construction, since `fakeclaude` keys its jsonl filename off
`PYRY_FAKE_CLAUDE_INITIAL_UUID == initialUUID`, which `StartRotation` also
sets. It was left in place across the migration (see [codebase/861.md § Implementation](../codebase/861.md))
rather than removed, since deleting it would touch every call site for no
behavioral gain. `fakeclaude` still opens with `O_APPEND|O_CREATE`, so
writing to the already-existing file is harmless.

### Local `encodeWorkdir` — verbatim copy of the production rule

`internal/sessions.encodeWorkdir` is unexported. The test file
declares a one-liner local copy (`strings.NewReplacer("/", "-", ".",
"-")`) rather than reaching into the package. Both `/` and `.` map to
`-`; a worktree under `/Users/.../.pyrycode-worktrees/...` produces a
doubled dash. The naive `/`→`-` rule silently looks in the wrong dir;
see [docs/lessons.md § Claude session storage on disk](../../lessons.md).

### `uuidStemPattern` — local copy of `internal/sessions/rotation`'s

`rotation.uuidStemPattern` is also unexported. A local
`regexp.MustCompile` of the canonical 36-char lowercase UUIDv4 shape
keeps the test self-contained. The test consciously asserts on the
shape (UUIDv4 stem) rather than on a specific string — the new id is
random per fakeclaude rotation.

### Polling helpers — file-local

Three private helpers in `rotation_test.go`:

- `waitForBootstrapID(t, regPath, want, timeout) registryEntry` —
  polls until the bootstrap entry's id equals `want` or the deadline
  elapses; fatals with the latest registry contents on timeout.
- `waitForBootstrapIDChange(t, regPath, avoidID, timeout) registryEntry`
  — polls until the bootstrap entry's id is non-empty AND `!= avoidID`.
- `readBootstrap(t, regPath) registryEntry` — single read, fatals if
  no bootstrap entry.

All three call `readBootstrapIfPresent`, which treats "file missing,
parse error, no bootstrap entry" as keep-polling rather than fatal.
The first iteration may fire before pyry's `RotateID → saveLocked`
has produced the file; the parse-error tolerance also handles a torn
read in principle (the temp+rename discipline of `saveRegistryLocked`
makes torn reads unreachable in practice). Poll cadence is 25ms (40
reads/s × 5s = 200 reads — well within budget for cheap file reads).

The polling target is the registry file, **not** the JSONL directory
or the trigger file: the registry write is the production-side
observable this test exists to verify.

### Three assertions on the post-rotation state

1. **UUIDv4 stem shape** — `uuidStemPattern.MatchString(post.ID)`. The
   new id originates from `fakeclaude`'s `uuidV4()` (mirrors
   `internal/sessions/id.go` byte-for-byte, see #122).
2. **`lastActiveAt` strictly advances** — `post.LastActiveAt.After(pre.LastActiveAt)`.
   `RotateID` calls `time.Now().UTC()` and persists. Strict-after
   defends against a future implementation that leaves the timestamp
   unchanged on rotate.
3. **Stable-state re-read after 200ms sleep** — no background path
   reverts the pointer. 200ms is ~2× the watcher's typical
   fsnotify-to-save latency; a spurious second rotation would surface
   in this window. Polling for a non-event would still need a
   timeout — a fixed sleep is the simpler primitive.

### AC#3 (`pyry list`) deferred

The ticket's third AC called for an independent observation through
`h.Run(t, "list")`. `cmd/pyry/main.go` has no `list` verb (`pyry sessions
list` is tracked in #87 / #88 and hasn't shipped under that single-word
form); adding it would be a production change forbidden by the ticket.
The on-disk registry assertions at AC#1/#2 already exercise the full
chain: fsnotify CREATE → real probe → `OnRotate` → `Pool.RotateID` →
`saveLocked` → file on disk. The control-plane re-check naturally lives
in the e2e test for `pyry sessions list` itself once that ships. The
test file carries a comment block referencing this decision.

### 5s deadline rationale

Watcher worst-case is `probeRetryDelays = {0, 50ms, 200ms}` ≈ 250ms +
fsnotify latency (sub-50ms) + `saveLocked` rename (<10ms) +
fakeclaude's 50ms trigger poll. Round-trip should clear well under
500ms. 5s gives ~10× headroom — same flavour as the rest of the e2e
suite's "generous safety net, not steady-state expectation" tradeoff.

### Failure-mode visibility

| Scenario                                                | Effect                                                   |
|---------------------------------------------------------|----------------------------------------------------------|
| Reconcile doesn't pick up `<initialUUID>.jsonl`         | Step 6 timeout; diagnostic prints registry               |
| Watcher misses the CREATE event                         | Step 8 timeout                                            |
| Probe returns wrong path (e.g. symlink regression)      | Watcher's exact-match gate drops; step 8 timeout         |
| `RotateID` errors (session not found)                   | Watcher logs `OnRotate failed`; step 8 timeout           |
| fakeclaude doesn't observe the trigger                  | Step 8 timeout; daemon stderr surfaces fakeclaude logs   |
| Stray second rotation reverts the pointer               | Step 12 fails with `id reverted` + registry contents     |

Every failure mode surfaces as a deterministic timeout with diagnostic
output — no silent passes possible.

### Production diff is zero

Watcher, real probe, registry write path, reconcile, and `RotateID`
all shipped pre-#120. Test diff: ~155 LOC, single new file
(`internal/e2e/rotation_test.go`). The architect spec at
`docs/specs/architecture/120-e2e-clear-rotation-watcher.md` is the
detailed design record.

### Why this supplements (does not yet retire) `TestPool_Run_StartsWatcher`

The unit test at `internal/sessions/pool_test.go:785` substitutes the
real probe with a `dirProbe` that just returns the most-recent jsonl
in dir. It exercises the watcher's event loop and `RotateID` plumbing
but not the real `/proc`-or-`lsof` probe path; it has been observed
flaky under `-count=N` (2026-05-02). #120 closes the binary-boundary
gap. Retirement of the unit test is contingent on multiple stable CI
invocations of the e2e replacement and is tracked separately.
