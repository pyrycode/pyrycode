# Testing

Three test files mirror the production layout. Stdlib `testing` only.

- **`id_test.go`** — format regex match, 1000-iteration uniqueness smoke test for `crypto/rand` wiring.
- **`pool_test.go`** — bootstrap installation, `Lookup("")` ↔ `Default()` identity, lookup by ID, unknown-ID sentinel match. Uses `/bin/sleep` as the "claude" binary; tests never call `Run`, so it's never spawned.
- **`pool_create_test.go`** (1.1a-A2) — `HappyPath` (UUID + entry shape after `Run` is live); `BootstrapUnchanged` (`Default()` returns the same `*Session` pointer pre/post `Create`); `LabelRoundTrip` (empty + non-empty labels round-trip via JSON unmarshal, not string match); `CapPassthrough_EvictsLRU` (`ActiveCap=1` evicts the bootstrap when `Create` activates); `SuperviseFails_EntryOnDisk` (no `Run` ⇒ `ErrPoolNotRunning`, valid id, entry on disk, ChildPID=0); `PersistFails_NoEntry_NoSpawn` (`registryPath` set to a non-directory path ⇒ empty id, no entry, only bootstrap in `Snapshot`).
- **`pool_list_test.go`** (1.1b-A) — `BootstrapOnly` (single entry, `Bootstrap=true`, `Label="bootstrap"`, on-disk `label` re-read from `sessions.json` is still empty); `OrderingByLastActive` (mutate three sessions' `lastActiveAt` under `lcMu` directly to `t0` / `t0+1m` / `t0+2m`, assert desc order; add a fourth equal-time entry, assert id-asc tiebreak is stable across two `List` calls); `BootstrapLabelPassthrough` (warm-start from a `sessions.json` whose bootstrap entry has `label: "main"` — synthetic substitution does NOT clobber); `RaceClean` (N goroutines × 100 `List` calls plus a mutator goroutine, `-race`-clean).
- **`pool_rename_test.go`** (1.1c-A) — `RoundTrip` (rename bootstrap to `"main"`; assert in-memory via `List`, on-disk by re-reading `sessions.json`); `EmptyClears` (rename to `"foo"` then to `""`; assert on-disk label is empty AND `List[0].Label == "bootstrap"` synthetic substitution resumes); `UnknownID` (zero-UUID returns `ErrSessionNotFound`, `bytes.Equal(before, after)` for the on-disk file, `List` snapshot deep-equal); `RaceWithList` (concurrent `Rename` + `List` goroutines under `-race`); `BootstrapPersistsAndShows` (rename bootstrap to `"primary"`; assert on-disk `Bootstrap=true` AND `Label="primary"`, `List[0].Label == "primary"` — no synthetic substitution).
- **`pool_resolve_id_test.go`** (1.1e-A) — `EmptyReturnsBootstrap`; `FullUUID`; `UniquePrefix` (1/4/8/16/35-char prefixes against a single-bootstrap pool); `FullUUIDBeatsPrefix` (synthetic two-session pool built via in-package `pool.sessions[id] = &Session{…}` writes; passes a full id whose prefix would also match a sibling and asserts the exact match wins); `AmbiguousPrefix` (synthetic two-session pool sharing a prefix; asserts `errors.Is(err, ErrAmbiguousSessionID)` plus the exact sorted match-list substring including the `"bootstrap"` substitution); `NoMatch` (zero-UUID + clearly-non-prefix `"zzzz"` both return `ErrSessionNotFound`); `RaceWithList` (concurrent `ResolveID` + `List` goroutines under `-race`).
- **`pool_remove_test.go`** (1.1d-A1) — `HappyPath` (Create + Remove a non-bootstrap session: assert child PID gone, `Lookup` returns `ErrSessionNotFound`, registry on disk has bootstrap only, stub JSONL byte-identical); `Bootstrap_Rejected` (Remove bootstrap returns `ErrCannotRemoveBootstrap`; registry bytes/mtime + `List` snapshot + JSONL byte-identical); `UnknownID` (zero-UUID returns `ErrSessionNotFound`; same byte-identity assertions); `RaceWithList` (concurrent Create+Remove writers and `List` readers under `-race`); `TerminatesUncooperativeChild` (`/bin/sh -c 'trap "" TERM INT HUP; exec sleep 86400'` as the fake claude — SIGKILL via `exec.CommandContext` cancel terminates it inside a 10s budget, no real-time `time.Sleep` in the assertion).
- **`session_test.go`** — `State` delegation, `Attach` with no bridge, `Attach` busy via `io.Pipe` (first attach blocks on input, second races and gets `supervisor.ErrBridgeBusy`), `Run` ctx-cancel via a real `/bin/sleep 3600` child.

### Pool readiness in runner fixtures

`recordingRunnerFactory` captures argv when a runner is constructed, including
the bootstrap during `Pool.New`. `waitArgvRaw` proves those arguments were
composed; it does not prove that `Pool.Run` has installed `runGroup`/`runCtx`.
After starting `Run` in the background, wait for [`Pool.Ready()`](sessions-package-key-types-config-bootstrapevicted-pool-ready.md)
before calling `Mint` or `Create`. Using argv capture as the startup signal can
intermittently return `ErrPoolNotRunning`; a single green run can miss the race
([#2765](https://github.com/pyrycode/pyrycode/issues/2765)). Register cancellation
and joining as cleanup before assertions, with bounded readiness and shutdown
waits, so a fatal assertion still cancels and waits for the pool to stop.

Shutdown tests must also cancel and join explicitly before checking removal;
`t.Cleanup` runs after those assertions. Close a `chan struct{}` after `Run`
returns so both the explicit shutdown and cleanup can observe completion.
Sending one result leaves only one waiter able to consume it, causing the
other to time out. Wait for readiness before minting or cancelling, prove the
file exists before shutdown, then check its removal after completion: `Run`'s
removal defers finish before it returns. The prompt and bootstrap-settings
shutdown tests use five seconds for readiness and fifteen seconds for joining.

### Cancellation and completed eviction

`TestPool_RunnerStoppedWaitsForProducerJoin` uses `raceRunner` to hold producer
return after teardown begins. It proves `Config.OnRunnerStopped` stays silent
until that gate opens, then reports the correct session before `Evict` completes.
Observing `stateEvicted` or the early transition alone would let this test pass
while the old producer could still emit a tail. See the
[confirmed-stop callback contract](sessions-package-key-types-transition-observer.md#confirmed-runner-stop-configonrunnerstopped).

An already-cancelled context cannot deterministically force `Session.Evict`
to fail: its `select` may choose the completed eviction channel and return
nil when both completion and `ctx.Done()` are ready. `Pool.Remove` calls this
after committing registry removal, so cancellation alone also cannot guarantee
a post-commit removal error. Completed removal may legitimately report success
despite cancellation; error-path fixtures must control completion rather than
change that production contract.

`TestConversationAgentSwitch_PostCommitRemovalErrorReportsNewID` uses
`gatedRunner.Run`, which waits for cancellation and then for a test-owned
`release` channel to close. `Session.runActive` cannot finish draining the
runner or reach `endEvict` to close `evictedCh` while the gate is held, leaving
`ctx.Done()` as the only ready case during `Switch`. The test checks that the
new binding is committed with one history entry and exactly one transition is
recorded even when old-session removal reports an error
([#2773](https://github.com/pyrycode/pyrycode/issues/2773)); repeating a race run
until green with an ungated runner does not prove that branch.

Register the release cleanup after `runPoolReady`: `t.Cleanup` runs LIFO, so the
gate opens before the helper cancels and joins the pool. Reversing registration
makes shutdown wait on runners whose release cannot run until the join's
15-second timeout expires. This applies even when a fatal assertion ends the
test early. See [development verification](development-verification.md#prove-that-tests-distinguish-the-change).

### Published model selection

An adapter acceptance test alone misses a model-selection failure: canonical
family storage can leave readback naming an alias absent from the menu and
effort-only writes accepting the broad fallback. In `cmd/pyry`,
`TestPublishedModelSelection_SettingsRoundTrip` composes actual publication,
the settings adapter and encrypted conversation-bound reads. It checks the
published value after canonical storage, repeated reads and reopening, for
both live and dormant sessions, plus an effort-only refusal and unchanged
sibling settings. The acknowledgement's bytes must contain only `session_id`;
model confirmation comes from the subsequent settings reply.
`TestPublishedModelSelection_VocabularyRefresh` checks that replacing the
vocabulary changes readback identity while storage still follows the family.
See [stored and published model identities](sessions-package-key-types-sessionsettings-claudesettingsargs.md).

### Lifecycle fact provenance and persistence

`TestLifecycleDelayedResetProvenance` holds `Session.spawnArgsMu` so reset
prompt composition waits after A→B rekey but before notification, then adopts
B→C. Delaying the observer itself would miss the old out-of-order rebind bug:
rebinding had already happened by observer entry. Assert the final C binding,
both original-owner facts with their own pairs and mutation timestamps, and
the unowned variant where a late foreign binding to A stays untouched.
See [the observer ordering contract](sessions-package-key-types-transition-observer.md).

Both session and conversation registry writers create missing parent
directories, so a nonexistent-directory fixture does not exercise save
failure. `TestLifecycleRotationFacts` uses a regular file as the parent and
checks both persistence-failure log events before asserting that reset, clear
and recovery still notify. Its callbacks acquire pool, session and capacity
locks and inspect the guarded registration, identity and active state:
meaningful checks prove off-lock delivery and avoid staticcheck's SA2001
empty-critical-section rejection. `TestLifecycleEvictionCapturedOwner`
checks the same lock availability before the eviction state flip and retains
the captured owner after registry deletion.

### Why no `TestHelperProcess` re-exec helper

The parent spec considered duplicating `internal/supervisor`'s `TestHelperProcess` re-exec pattern into the sessions package (~20 lines) per the project's "duplicate, don't export test surface" convention. The blocker: `supervisor.Config.helperEnv` is unexported and is the only way to pass test-only env to the spawned child without polluting the parent test process's `os.Environ()`. External packages cannot set it.

The chosen workaround is to use a real benign binary (`/bin/sleep`) as the fake claude. No re-exec, no env injection, no helper duplication. The supervisor spawns it, ctx cancellation kills it, `supervisor.Run` returns `ctx.Err()` — which is the only contract the test asserts.

`/bin/sleep` exists on both Linux and macOS; CI runs both. If a future CI environment lacks it, `t.Skipf` on `exec.LookPath` failure rather than silently passing.
