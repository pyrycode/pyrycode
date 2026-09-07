# #2137 — Retire the rotation watcher and its open-descriptor probe

## Files read

- `internal/sessions/rotation/` (whole package: `watcher.go`, `probe.go`, `probe_linux.go`,
  `probe_darwin.go`, plus tests and `testdata/lsof_basic.txt`) — the deletion target. `New`,
  `Config` (`Snapshot` / `IsAllocated` / `OnRotate` closures), `handleCreate`, `DefaultProbe`.
- `internal/sessions/pool.go` → `Run` — the sole production construction site of the watcher, and
  the only place the three closures are wired. Also `allocatedTTL`, `newProbe`,
  `snapshotForRotation`, `RegisterAllocatedUUID`, `registerAllocatedUUIDLocked`, `IsAllocated`,
  `pruneAllocatedLocked`, the `allocated` map field, and `Activate` (registration site 3, whose doc
  block is entirely about the watcher). `RotateID` and `RotateBootstrapForSelfHeal` carry
  watcher-justified paragraphs but keep their bodies.
- `internal/sessions/transition.go` → `onRotate` (the watcher's `OnRotate` seam — goes),
  `RotateForNewSession` (registration site 2 + the skip-set asymmetry paragraph),
  `AdoptAnnouncedID` (the surviving re-key path; its "deliberately un-allocated" bullet and its
  `ErrSessionNotFound` rationale both name the watcher).
- `internal/sessions/get_or_create.go` → the `registerAllocatedUUIDLocked` call inside the
  register-and-supervise critical section — registration site 1.
- `internal/sessions/transition_test.go` → the seven `pool.onRotate` tests and the five
  `AdoptAnnouncedID` tests. Which of the seven duplicate an `AdoptAnnouncedID` sibling and which
  cover something nothing else does is the one real design call in this ticket (see Design).
- `internal/e2e/rotation_test.go` → `TestE2E_RotationWatcher_DetectsClear` plus eight file-scope
  helpers. Verified the ticket's helper table by grep: `claudeSessionsDir`, `uuidStemPattern`,
  `waitForBootstrapID`, `readBootstrap`, `readBootstrapIfPresent` have callers in four sibling
  `package e2e` tests; `waitForBootstrapIDChange` has none. `encodeWorkdir`'s only other hit is in
  `package realclaude`, a different package behind a disjoint build tag — so it is uncallable from
  there and goes with the test, as the ticket says.
- `internal/transcript/transcript.go` → package doc (import-cycle rationale), `Ext`,
  `uuidStemPattern`, `Probe`, `CanonicalDir` — five prose sites naming the watcher, one more than
  the ticket's three. `Probe` / `Probed` / `GuardProbedPath` stay; only their prose changes.
- `cmd/pyry/session_reset_follow.go` → `rekeyPool` (its `ErrSessionNotFound` branch and its
  Debug-not-Warn rationale are both justified by the race this ticket ends) and `follow`'s unwind.
- `docs/knowledge/features/streamsup-package-announced-reset-follower.md` § "What `Sink` does on a
  reset" step 5 — states the unwind contract: the tag ends on `newID` only when the pool's answer
  means the session now stands there, and `ErrSessionNotFound` is listed as such an answer
  *because the watcher won the race*. That premise is what this ticket removes; it drives the
  Security review's one real finding.
- `docs/knowledge/features/rotation-watcher.md` — the package overview for the code being deleted.
  Read for the dependency-direction rule (rotation must not import sessions; the contract is
  primitive-typed closures) which explains why `snapshotForRotation` exists at all.
- `CODING-STYLE.md`, `docs/knowledge/architecture/system-overview.md`, `docs/PROJECT-MEMORY.md`.

## Context

`internal/sessions/rotation` guesses at a fact claude now states. On a CREATE in claude's session
directory it asks the OS which transcript each tracked pid holds open — `lsof` on Darwin, a
`/proc/<pid>/fd` walk on Linux, on a 250ms retry schedule — and reads a match as "this session
rotated into that file". Real claude opens, appends and closes within milliseconds, so the probe
practically never observes an open descriptor; it may only ever have been reliable against the e2e
fake claude, which holds its descriptor open. `conversation_reset` (#2134/#2135/#2136) answers the
same question directly, on the session's own stream, and now drives the rotation.

Retire-versus-keep was settled on the parent. This is not pure subtraction: today both paths fire
on an announced `/clear` and the watcher usually wins, so the follower's `AdoptAnnouncedID` returns
`ErrSessionNotFound` and takes its already-applied branch. After this ticket the follower's own
re-key is the one that applies the id. Nothing is load-bearing about the ordering — the follower
already treats both answers as success — but several comments justify themselves by the race and
would be lying the moment the watcher is gone.

**This design warrants no ADR.** The retire-versus-keep decision was made and recorded on #2088;
this ticket executes it. `docs/knowledge/features/rotation-watcher.md` describes a package that
will not exist and should be retired or reduced to a historical note — that is the documentation
phase's call, not mine, and it is flagged in the PR body.

## Design

### What is deleted

1. **The package** `internal/sessions/rotation` in full, including `testdata/`.
2. **Its wiring** in `Pool.Run`: the `rotation.New` block, the three closures, and the
   `rotation watcher disabled` Warn. The `dir != ""` branch disappears entirely; `dir` itself stays
   read because nothing else in `Run` needs it — so the local goes too.
3. **The skip-set**, whole: `allocatedTTL`, the `allocated` map field on `Pool`,
   `RegisterAllocatedUUID`, `registerAllocatedUUIDLocked`, `IsAllocated`, `pruneAllocatedLocked`,
   and all three registration sites — `GetOrCreate`'s in-critical-section prime, the
   `registerAllocatedUUIDLocked` in `RotateForNewSession`, and the `RegisterAllocatedUUID` in
   `Activate`.
4. **`snapshotForRotation`** (its return type is `[]rotation.SessionRef`, so it cannot outlive the
   import) and **`newProbe`** (the `rotation.DefaultProbe` indirection). `Pool.Snapshot` stays —
   it has other callers.
5. **`Pool.onRotate`**, per the ticket's stated line: unexported, and its doc says it *is* the
   watcher's `OnRotate` seam.

`RotateID` stays. It loses its last production caller here, but it predates the watcher, is
exported, and carries ~40 test references across five files; removing it is a separate deliberate
call. The orphan must not tempt a wider sweep.

### The one real design call: the seven `onRotate` tests

`Pool.onRotate` is `RotateID` + `notifyTransition`. Seven tests in `transition_test.go` drive it,
and they are not all testing the watcher — most use it as the cheapest vehicle for reaching the
transition fan-out. Deleting all seven would silently drop coverage this ticket has no mandate to
drop. `AdoptAnnouncedID` is the surviving sibling with the same shape (re-key + fire one
`ReasonClear`), so it is the re-point target. Two properties make the substitution safe for these
tests: it refuses a *taken* destination, and every test here rotates onto a freshly-constructed
distinct id; and it no-ops on *equal* ids, which none of them exercise.

Split by whether an `AdoptAnnouncedID` sibling already asserts the same thing:

| `onRotate` test | Disposition | Why |
|---|---|---|
| `TestPool_TransitionObserver_ClearFiresOnRotate` | **delete** | `TestPool_AdoptAnnouncedID_RekeysAndFiresOneClear` asserts the same set: one `ReasonClear`, both ids, non-zero `OccurredAt`, entry moved. |
| `TestPool_OnRotate_UnknownIDNoSignal` | **delete** | `TestPool_AdoptAnnouncedID_UnknownOldID` asserts exactly this: `ErrSessionNotFound`, zero signals. |
| `TestPool_TransitionObserver_NilIsNoOp` | **re-point** | Nil-observer path; no sibling covers it. |
| `TestPool_OnRotate_RebindsOwningConversation` | **re-point** | Covers rebind **survives a reload from disk**; `TestPool_AdoptAnnouncedID_RebindsTheOwningConversation` is to be read in Phase B — if it already asserts reload survival, delete instead. Recorded as an Open Question. |
| `TestPool_OnRotate_NoOwnerNoOp` | **re-point** | Asserts the registry file bytes are untouched when no conversation owns the session. Unique. |
| `TestPool_OnRotate_RebindRaceConcurrentSave` | **re-point** | Concurrent rebind + atomic save under `-race`. Unique. |
| `TestPool_TransitionObserver_RaceConcurrentFires` | **re-point** | Lock-free observer read under concurrent fires. Unique. |

Re-pointed tests are renamed off the `OnRotate` stem and their doc comments restated in terms of an
announced reset rather than a watcher-observed one. The assertions do not change — that is the
point of choosing a same-shape sibling.

### Comment corrections (AC5)

The ticket enumerates five locations. Grep found three more that AC1's wording covers ("no comment
left in the tree pointing a reader at it as live code"); they are in scope and listed here rather
than deferred.

Enumerated by the ticket:

- `internal/sessions/transition.go` — `RotateForNewSession`'s skip-set asymmetry paragraph (the
  asymmetry it describes ceases to exist), and `AdoptAnnouncedID`'s "deliberately does not register
  the skip-set" bullet. Also `AdoptAnnouncedID`'s `Errors:` paragraph, which names the watcher as
  the ordinary source of `ErrSessionNotFound` — see the Security review.
- `cmd/pyry/session_reset_follow.go` — `rekeyPool`'s doc: the `ErrSessionNotFound` answer and the
  Debug-not-Warn rationale.
- `internal/e2e/relay_v2_stream_announced_reset_test.go` — the header's *"'Exactly one' is the
  load-bearing word"* explanation and the `more than 1 means the rotation watcher fired a second
  delimiter` assertion message. Assertions stay green; the stated reason evaporates.
- `internal/transcript/transcript.go` — package doc's import-cycle rationale, `uuidStemPattern`'s
  "three local regexps", `Probe`'s "Redeclared here (not imported from …)". Plus two the ticket did
  not name: `Ext`'s "the three families and the rotation watcher" and `CanonicalDir`'s "both
  families and the rotation watcher already do".
- `internal/e2e/realclaude/harness_session_control_test.go` — `uuidStemPattern`'s transcribed-from
  attribution, and the `rotateBudget` block's `fsnotify + watcher rotate` timing description.

Found by grep, same rule:

- `cmd/pyry/relay.go` — `claudeSessionsDir`'s field doc ("Empty disables reconcile, the rotation
  watcher, and …") and the interactive-turn-stream gating paragraph that repeats it.
- `internal/e2e/internal/fakeclaude/main.go` — three sites describing fake-claude's `/clear` rotate
  as something "pyry's rotation watcher follows into the registry". Fake-claude's own behaviour is
  unchanged; the sentence about what pyry does with it is what is now wrong.
- `internal/e2e/fakeclaude_test.go` — `TestE2E_StartRotation_PrimitiveWiresFakeClaude`'s
  "without touching pyry's rotation watcher". The test stays (it observes files on disk only), and
  so do `StartRotation` / `StartRotationWithRelay` in `internal/e2e/harness.go`.
- `cmd/pyry/session_reset_follow_test.go`, `internal/sessions/pool_bootstrap_sessionid_test.go`,
  `internal/sessions/pool_mint_test.go`, `internal/sessions/pool_test.go`,
  `internal/sessions/selfheal_test.go` — test-side prose naming the watcher.

`cmd/pyry/snapshot_usage.go` needs no edit: its "probe" is that file's own by-pid resolver history
and it never names the deleted package.

### The e2e test file

`TestE2E_RotationWatcher_DetectsClear` is deleted; the file keeps the five helpers with sibling
callers and loses `encodeWorkdir` and `waitForBootstrapIDChange` (leaving them would redden
`make check` as staticcheck U1000). The file is renamed to something naming what it now holds —
registry/bootstrap reading helpers for `package e2e`.

## Concurrency model

Subtractive. One goroutine disappears: the `g.Go(func() error { return w.Run(gctx) })` in
`Pool.Run`'s errgroup. It had a clean shutdown path already (ctx cancel → `w.Run` returns), so
nothing is orphaned and no shutdown sequence changes.

`RotateID`'s doc records that `sess.id` is guarded by both `Pool.mu` and `Session.lcMu` because
#839 wired it into "the live fsnotify rotation watcher, whose goroutine runs concurrently with the
per-session lifecycle goroutines". The *invariant* survives — `AdoptAnnouncedID` re-keys from the
follower's decorator, which runs on the runner's parse goroutine, equally concurrent with the
lifecycle goroutines. Only the named justification changes. The two-lock discipline and the
`Pool.mu → Session.lcMu` order are untouched.

Removing the `allocated` map removes state guarded by `Pool.mu`; no lock is removed and no
ordering changes.

## Error handling

- The `rotation watcher disabled` Warn on `rotation.New` failure disappears with its only caller.
  It was the AC of an older ticket ("startup proceeds without a watcher rather than failing") and
  has no successor: there is nothing left to fail to construct.
- `AdoptAnnouncedID`'s two sentinels (`ErrSessionNotFound`, `ErrSessionIDTaken`) and
  `rekeyPool`'s branch structure are **unchanged**. Only their doc prose changes. The one
  substantive consequence — what can still produce `ErrSessionNotFound` once the watcher is gone —
  is a Security review finding, deferred rather than fixed here.
- No new error paths; no reject branches added.

## Testing strategy

RED is unusual for a deletion, so the gate is stated explicitly rather than implied:

1. **The two ACs that must stay green, unchanged in their assertions** —
   `TestRelayV2_StreamAnnouncedResetFollowsClaude` (AC3: an announced `/clear` re-keys and draws
   exactly one delimiter) and `TestRelayV2_StreamNewSessionRotatesAndRestartsFresh` +
   `TestRelayV2_StreamNewSessionNamedConversationRotatesThatOne` (AC4: a daemon-driven
   `new_session` still rotates and draws exactly one delimiter, with `RotateForNewSession` no
   longer priming a skip-set). These are the ticket's real proof: they are the two paths the
   watcher was racing, and they must survive its removal untouched.
2. **The re-pointed transition tests** carry their assertions over to `AdoptAnnouncedID` verbatim.
3. **A negative check for AC1**, run as a command rather than a test: a repo-wide grep proving no
   `sessions/rotation` import, no `lsof`, no `/proc/<pid>/fd` walk and no `rotation watcher
   disabled` string survives outside the deleted package.
4. `go test -race ./internal/sessions/... ./internal/e2e/... ./internal/transcript/... ./cmd/pyry/...`,
   `go vet ./...`, `go build ./cmd/pyry`.
5. `go vet -tags e2e_realclaude ./internal/e2e/realclaude/` — `make check` never compiles that
   package, so it can break while the gate stays green. Explicitly in AC5.

No new tests are written. The coverage this ticket must not lose is preserved by re-pointing, and
the coverage it deliberately drops is the watcher's own.

## Open questions

1. Does `TestPool_AdoptAnnouncedID_RebindsTheOwningConversation` already assert that the rebind
   survives a reload from disk? If yes, `TestPool_OnRotate_RebindsOwningConversation` is a
   duplicate and is deleted rather than re-pointed. Resolve by reading the test in Phase B.
2. Does `rekeyPool`'s Debug level still fit once the already-applied branch is the exception
   rather than the rule? Current lean: **keep Debug**, because the outcome is still benign and
   non-actionable, and a Warn on a rare race would compete with `follow`'s refusal Warn, which is
   the record that matters. Confirm against what `cmd/pyry/session_reset_follow_test.go` asserts
   about the level before finalising.
3. ~~Does anything outside `Pool.Run` read `Pool.claudeSessionsDir`?~~ **Resolved during the
   security review:** yes — `Pool.removeJSONL`'s archive/purge path joins it to build
   `<dir>/<uuid>.jsonl`. The field stays; only `Run`'s local `dir` goes.

## Sizing

The size-S table is exceeded on two lines, and this section is the declaration required when the
floor rule overrides the ceiling.

| Limit | Boundary | This ticket |
|---|---|---|
| Production source files created or modified | ≤ 5 | **7 modified** (`internal/sessions/pool.go`, `transition.go`, `get_or_create.go`, `internal/transcript/transcript.go`, `cmd/pyry/session_reset_follow.go`, `cmd/pyry/relay.go`, `internal/e2e/internal/fakeclaude/main.go`) **+ 6 deleted** with the package |
| Total written work | ≤ 800 | ~400 written (comment rewrites + test re-points); ~1900 deleted |
| New exported types or interfaces | ≤ 5 | 0 |
| Consumer call sites needing simultaneous update | ≤ 10 | **~40 test references** across five `internal/sessions` test files |
| Acceptance criteria | ≤ 5 | 5 |
| Distinct error/reject branches | ≤ 10 | 0 |

Split depth is `parent 2088, grandparent none`, so a split is permitted and the depth cap is not
what decides this. Every candidate slice was checked against the floor rule and fails it:

- **Package deletion vs. skip-set removal.** After the package goes, the skip-set has zero
  consumers — its sole consumer is the sibling slice. That is the floor rule verbatim. It also
  cannot compile apart: `snapshotForRotation` returns `[]rotation.SessionRef`, so dropping the
  import forces dropping it, and the ticket's own AC2 names the skip-set as part of the deliverable.
  Shipping the first half alone leaves `Activate` priming a set that protects nothing.
- **Production deletion vs. test pruning.** Not splittable in Go: deleting a symbol breaks its
  tests' compilation in the same commit.
- **Deletion vs. comment corrections.** The comment half is downstream, touches the same files
  (guaranteed conflict shape), and AC1 folds it into the definition of done — a tree whose comments
  point at a package that no longer exists is a worse intermediate state than today's.

So the floor wins over the ceiling: the overage is declared and the ticket is built. The refiner
declared the file-count overage for the same reason; the call-site overage is mine, found by
grepping the five `internal/sessions` test files, and is recorded here rather than passed over.
Note for honesty about the budget, not as a re-count that gets under the line: roughly thirteen of
those references are inside test functions that are deleted or re-pointed whole, so the turn cost
is nearer thirteen edits than forty — but the raw count is over the boundary and that is what the
table records.

## Security review

**Verdict:** PASS

The ticket is `security-sensitive` because this change promotes a session id read off the
supervised child's stdout from "second writer, usually loses" to the **sole** writer of the
registry re-key. The review's central question is therefore not "what does the deletion add" —
it adds nothing — but **"do `AdoptAnnouncedID`'s bounds still hold once nothing else re-keys?"**

**Findings:**

- **[Trust boundaries] No finding on the boundary itself — both bounds verified live and
  independent of the watcher.** The boundary is explicit and single: `AdoptAnnouncedID` is the one
  function through which child-authored bytes reach `rekeyLocked`. Its two bounds were checked
  against the tree rather than taken from its own doc, because a doc that names the watcher is
  exactly the kind this ticket is correcting. **Shape** — `emitConversationReset`
  (`internal/streamsup/parser.go`) gates on `transcript.ValidStem` before the event is constructed;
  `ValidStem` is an anchored full match over lowercase hex, so it is simultaneously the character
  allowlist (no separator, no traversal) and the length cap. It sits upstream in a package this
  ticket does not touch. **Destination** — `ErrSessionIDTaken` refuses a `newID` naming another
  live session, checked inside the same `p.mu` hold as the mutation. Neither bound was ever
  supplied by the watcher, so neither weakens. Adversarially: the watcher was not an implicit brake
  either — when it won the race it rotated to whatever filename appeared on disk, with no
  validation of its own, and it is being replaced by the strictly more validated path.

- **[Trust boundaries / Error handling] OUT OF SCOPE — `ErrSessionNotFound` no longer means what
  the follower's unwind assumes; filing as a separate bug, not fixing here.** `rekeyPool` reads
  `ErrSessionNotFound` as "the session now stands on `newID`" and returns nil, which makes `follow`
  keep the tag on `newID` and call `adoptRunner(newID)`. That reading was sound only because the
  watcher had applied *this same* rotation. Once it is gone, the surviving producers of that
  sentinel are `RotateForNewSession`, `RotateBootstrapForSelfHeal` and entry removal — and the
  first two rotate onto a **freshly minted** id, not the announced one. In that race the tag and
  the runner's spawn id end on an id the registry never adopted: the tag/registry divergence whose
  consequence class is a conversation going dark. The race is narrow, the child cannot cause the
  daemon-driven rotation (it originates from a phone frame), and it is **pre-existing** — the same
  interleaving was possible before, merely rarer and masked by the watcher usually winning. Fixing
  it means changing `follow`'s unwind, which is production behaviour outside this ticket's scope,
  and the ticket's own text warns that unwinding on this sentinel is worse than the state it would
  replace. Per § Scope Discipline: file it, correct the comment to state the truth rather than the
  stale race rationale, do not fix it here.

- **[Concurrency] SHOULD FIX — `RotateID`'s two-lock invariant must not be "corrected" into the
  stale claim #866 removed.** `RotateID`'s doc justifies `sess.id` being guarded by **both**
  `Pool.mu` and `Session.lcMu` by naming the watcher goroutine as the concurrent reader, and
  explicitly records that the older "no concurrent reader exists" claim went stale. The careless
  rewrite — "the watcher is gone, so there is no concurrent reader" — restores precisely the bug
  that history documents. The invariant **survives**: `AdoptAnnouncedID` re-keys from the
  follower's decorator on the runner's parse goroutine, equally concurrent with the per-session
  lifecycle goroutines. The rewrite must swap the *named* justification and keep the invariant, the
  `Pool.mu → Session.lcMu` order, and the stale-claim warning. This is the single most dangerous
  comment edit in the ticket; the verifier should check it landed this way.

- **[Subprocess / external command execution] No finding — strict reduction.** The change deletes
  the daemon's `exec.Command("lsof", …)` shell-out (`probe_darwin.go`), which today fires in
  response to filesystem events in a directory the supervised child writes. No `sh -c` was
  involved and the pid argument was daemon-owned, so nothing was exploitable; but a subprocess
  spawned on child-triggerable events is surface, and it goes away entirely. No new subprocess.

- **[File operations] No finding — strict reduction.** Deleted: an fsnotify watch on claude's
  session directory, a `/proc/<pid>/fd` readlink walk, `lsof` output parsing, and the two-sided
  `EvalSymlinks` canonicalisation the watcher needed to compare event paths against probe paths
  (#118/#221). No path is constructed from untrusted input by any code this ticket adds.
  `transcript`'s `Probe`, `Probed` and `GuardProbedPath` — including `GuardProbedPath`'s
  path-confinement check — are explicitly kept and only their prose changes; they already had no
  production callers, so their reachability is unchanged by the deletion. `Pool.removeJSONL`'s
  `filepath.Join(p.claudeSessionsDir, string(id)+".jsonl")` is untouched and keeps its own id
  provenance.

- **[Cryptographic primitives] No finding — id minting is untouched.** Removing the skip-set
  removes bookkeeping *about* minted ids, never the minting: `RotateForNewSession` and
  `RotateBootstrapForSelfHeal` still mint through `NewID` (`crypto/rand`). The skip-set was a
  30-second TTL map used to suppress a watcher CREATE, never a security control and never
  consulted in an authorisation decision, so deleting it removes no check.

- **[Errors, logs, telemetry] No finding, with one thing to preserve.** The deleted
  `rotation watcher disabled` Warn carried only a construction error, no untrusted content. The
  retained `rekeyPool` Debug logs two session ids (both already `ValidStem`-validated) and the
  pool's own error. Its doc carries a SECURITY paragraph recording that the record is content-free
  and that `emitConversationReset` deliberately has no logging surface because `encoding/json`
  quotes offending input into its error text. That paragraph is still true and **must survive the
  rewrite** — only the sentences about the watcher race change.

- **[Concurrency, goroutine lifecycle] No finding.** One goroutine is removed
  (`g.Go(w.Run)` in `Pool.Run`'s errgroup); it already exited on ctx cancel, so nothing is
  orphaned and no shutdown sequence changes. No lock is removed — the `allocated` map was
  `Pool.mu`-guarded state, and `Pool.mu` remains — and no lock ordering changes.

- **[Tokens, secrets, credentials] Not applicable — no credential material is in scope.** The only
  value crossing the boundary is a UUID stem, which is an identifier, not a secret: it is logged
  deliberately, appears in argv as `--session-id`, and names a file on disk. Nothing in the deleted
  package or the corrected comments touches token generation, storage, rotation or revocation.

- **[Network & I/O] Not applicable — no wire surface changes.** No socket, HTTP server or TLS
  configuration is added, removed or reconfigured. The adjacent `claudeSessionsDir` gate in
  `cmd/pyry/relay.go` (empty dir disables reconcile and the interactive turn/modal streams) is
  **prose-only** in this ticket: the gating expression is not touched, only the clause that lists
  the watcher among what an empty dir disables.

- **[Threat model alignment] No finding.** `docs/protocol-mobile.md` § Security model governs the
  relay wire surface, which this ticket does not reach. The CLI-side threat this ticket does bear
  on — child-authored input mutating daemon state — is the trust boundary audited in the first two
  findings, and is the one #2135 introduced and bounded.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-07
