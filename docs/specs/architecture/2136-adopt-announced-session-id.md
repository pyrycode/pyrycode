# #2136 — streamsup adopts the announced session id

## Files read

- `internal/streamsup/runner.go` → `Runner.sessionID` (field doc), `restartMu` (field doc),
  `RestartFresh`, `SetSpawnArgs`, `setArgsLocked`, `SetSpawnPermissionMode`, `beginSpawn`,
  `useCreateForm`, `buildArgs`, `spawnEnv`, `turnTarget`, `New` — the seam this ticket adds a
  sibling to, its two shape precedents, the single reader of the field, and the id-flag decision
  the acceptance criterion rides.
- `cmd/pyry/session_reset_follow.go` → `sessionResetFollower`, `newSessionResetFollower`,
  `sessionResetFollower.Sink`, `sessionResetFollower.follow` — the caller, and the ordering
  contract the runner-side adopt has to slot into without disturbing.
- `cmd/pyry/streamsup_runner.go` → `newStreamRunnerFactory`, `streamRunner` — the
  single-goroutine construction window where the runner first exists, and the wrapper that keeps
  `*streamsup.Runner` unexported from the sessions layer.
- `cmd/pyry/session_reset_follow_test.go` → `TestSessionResetFollower_PoolAnswerDecidesTheTag`,
  `TestSessionResetFollower_ForwardsEveryVariantAndToleratesNilNext`, `recordingAdopt` — the table
  AC 3's rows land on, and the nil-hook tolerance test the new field extends.
- `internal/streamsup/runner_test.go` → `TestRunner_RestartFresh_RotatesThenResumesNewID`,
  `TestRunner_RestartFresh_ProbeDecidesPerSpawn`,
  `TestRunner_BeginSpawn_FirstSpawnResumesExistingTranscript`,
  `TestRunner_SpawnSetupFailureRetainsSessionID`, `writeTranscript`, `spawnArgsRecorder`,
  `helperRunCfg`, `runInBackground`, `idFlagValue` — the shapes the ticket names, and the
  direct-construction pattern the mechanism-level test copies.
- `internal/sessions/runner.go` → `Runner` interface — read to confirm it is NOT widened; its own
  doc records that a method lands there only when the consumer sits inside `internal/sessions`.
- `docs/knowledge/features/streamsup-package-announced-reset-follower.md` → the #2135 lesson that
  a refusal only binds if the caller unwinds its own half. It is why AC 3's rule is stated
  positively over the pool's answer, and why the runner-side adopt must hang off that same rule
  rather off a second list of sentinels.
- `docs/knowledge/features/streamsup-package-session-rotation-notification-onsessionrotate.md` →
  records that a factory-tier assertion of an installed `Config` field is not reachable by
  inspection (the factory returns `sessions.Runner` over an unexported concrete type). The same
  limitation applies to the hook this ticket installs, and shapes the testing strategy.

## Context

The stream runner keeps its own copy of the live claude session id in `Runner.sessionID`, and at
`fb53f17e` exactly one thing writes it: `RestartFresh`, as part of a trio with `rotatePending` and
`freshSeq`. Every spawn reads it back in `beginSpawn`, which is where it reaches both the argv (via
`buildArgs`) and the child's environment (via `spawnEnv`).

An announced reset does not go through `RestartFresh`. #2135's `sessionResetFollower` rotates the
runner's live session tag and re-keys the pool registry; nothing touches the runner. So after a
reset the registry, the gauge reader and the drain's active-session gate all point at the new id
while the runner still holds the pre-reset one, and the next crash respawn asks `useCreateForm`
which id flag to use, finds the pre-reset transcript still on disk, and spawns
`--resume <pre-reset id>`. The crash resurrects the conversation the operator just cleared.

This was latent while the rotation watcher was the only reset path — its confirmation probe
practically never fires. #2135 made it reachable: the follower fires on every announcement.

No ADR is warranted. This adds no new decision; it completes one #2135 already recorded, under the
rule that document already states positively.

## Design

Two halves, one rule.

### The runner half — `(*streamsup.Runner).AdoptSessionID`

```go
// AdoptSessionID installs newID as the live session id the NEXT spawn resumes,
// and does nothing else.
func (r *Runner) AdoptSessionID(newID string)
```

It is `SetSpawnArgs`' mirror image. `SetSpawnArgs` takes `restartMu` exactly once and writes one
field, and its doc is explicit that the field is "never `sessionID`, `rotatePending` or
`iterCancel`". This one takes `restartMu` exactly once and writes the one field that method is
careful not to: `sessionID`, and none of `rotatePending`, `freshSeq`, `iterCancel` or `restartCh`.
`beginSpawn`'s #1481 single-acquisition property therefore survives by construction, exactly as it
does for `SetSpawnArgs`: a racing `AdoptSessionID` serialises wholly before a spawn's section (that
spawn resumes the announced id) or wholly after it (that spawn keeps the previous id and the next
one takes the announced one). Both are correct — the contract is the NEXT spawn, and it promises
nothing about a spawn already in flight.

`restartMu` is a documented leaf, and this method honours it trivially: no call-out, no second
lock, no I/O under the section. The empty-id Warn sits ABOVE the section, mirroring
`RestartFresh`'s placement for the same reason its doc gives.

An empty `newID` is refused at Warn and nothing is written. This is the same last-resort guard on
`New`'s non-empty contract that `RestartFresh` carries, held for the same reason: the runner never
emits `--session-id ""`, and the validating boundary stays upstream. It gets no acceptance
criterion because the announcement path already gates on `transcript.ValidStem` in streamsup's
`emitConversationReset` — this is a guard on the primitive, not a validator.

Nothing hand-rolls a path. The `--session-id` vs `--resume` choice stays entirely with
`useCreateForm`, whose `transcript.StatByID` probe runs `ValidStem` before any path join.

`sessions.Runner` is NOT widened. Its own doc records the rule: a method lands there when its
consumer sits inside `internal/sessions`, and this consumer sits in `cmd/pyry`. The concrete
`*streamsup.Runner` is reached where it already exists in hand.

Two existing field docs gain a clause rather than being left to rot: `sessionID` names its second
writer, and `restartMu`'s charter records that this method writes that field alone.

### The caller half — `sessionResetFollower`

A new field, set post-construction:

```go
// adoptRunner installs the announced id as the runner's live spawn id.
adoptRunner func(newID string)
```

The runner does not exist when the follower is minted: `newStreamRunnerFactory` builds the
follower above `streamsup.New`, because the follower's `Sink` is what the parser writes into and
that parser is the runner's `Stdout`. So the factory assigns the field after `streamsup.New`
returns and before the sessions layer starts `Run` — the single-goroutine window the factory
already occupies, and the same window in which `newStreamRunnerFactory` already binds
`OnSessionRotate` and `OnChildExit`. A nil field calls nothing, matching `adopt`'s and `next`'s
stated nil behaviour, which is what keeps every existing follower test constructing three
arguments.

### The rule, made structural

AC 3 states one rule: **the runner's id ends on the announced id exactly when the session tag
does.** `follow` today expresses the tag half across three returns and one unwind. Replicating the
runner adopt at each of the three qualifying returns would make a sentinel added later to
`AdoptAnnouncedID` need to remember a fourth site — the precise failure mode the #2135 lesson
names, re-entered one seam over.

So `follow`'s pool half is extracted into one helper that answers the one question both halves
need, and the runner adopt gets exactly one call site:

```go
// rekeyPool runs the pool half and reports whether the session now stands on
// newID: nil when it does, the pool's error when it does not.
func (f *sessionResetFollower) rekeyPool(oldID, newID string) error
```

`rekeyPool` returns nil for the three answers that mean the session stands on the announced id —
no pool wired at all, the pool re-keyed (`nil`), someone else already did
(`sessions.ErrSessionNotFound`, logged at Debug beside the reason it is not a Warn) — and the
pool's error for every other answer, `ErrSessionIDTaken` today and any sentinel added later.
`follow` then reads:

- announced id equals the live tag → return untouched (unchanged);
- rotate the tag;
- `rekeyPool` returns non-nil → unwind the tag, Warn (unchanged, including the Warn sitting AFTER
  the unwind);
- otherwise → `adoptRunner(newID)`.

Every ordering argument in `follow`'s existing doc survives verbatim: the live-tag read, the
equal-id guard, rotate-before-the-pool-call, the unwind, and the window the unwind accepts. The
restructure moves no step and adds no branch — it gives the rule one place to live.

### Data flow

```
claude stdout line
  └─ streamsup emitConversationReset   (ValidStem gate — the shape boundary)
      └─ parser → retention holds → sessionResetFollower.Sink
          └─ follow(newID)
              ├─ tag.Rotate(newID)
              ├─ rekeyPool → Pool.AdoptAnnouncedID   (the destination boundary)
              │    └─ non-nil ⇒ tag.Rotate(oldID); Warn; STOP
              └─ adoptRunner(newID) → (*streamsup.Runner).AdoptSessionID
                   └─ restartMu { sessionID = newID }
                        └─ read by the NEXT beginSpawn → useCreateForm → --resume <newID>
```

## Concurrency model

No goroutine is created and none is signalled. Every step runs synchronously on claude's stdout
forwarder goroutine, the same goroutine that produces every later event of this session, which is
what keeps the ordering contract in `follow`'s doc intact.

`AdoptSessionID` takes `restartMu` once and releases it before returning. The lock order is
unchanged because no second lock is ever held: `restartMu` stays a leaf, and this method takes
nothing else — no `mu`, no `stateMu`, no pool lock. That is also why the sessions layer's
constraint is untouched; nothing here can be reached with another lock held that matters.

Against `beginSpawn` the write is serialised by `restartMu` itself, and the outcome either way is
a correct spawn (see the design above). Against `RestartFresh` the two serialise on the same
mutex; whichever lands second decides the id, and the pair cannot interleave a half-written state
because each takes the mutex exactly once.

The follower's `adoptRunner` field is written once in the factory goroutine and read on the stdout
forwarder goroutine, which does not exist until `Run` spawns a child — the goroutine-creation
happens-before edge covers it, and it is the same edge `OnSessionRotate` and `OnChildExit` already
rely on for fields bound on the adjacent lines. It is never written again.

## Error handling

| Failure | Behaviour |
|---|---|
| Empty announced id reaches `AdoptSessionID` | Warn, no write. Unreachable in production (`ValidStem` upstream); a last-resort guard on `New`'s contract, matching `RestartFresh`. |
| Pool refuses the announced id (`ErrSessionIDTaken`, or any later sentinel) | Tag unwound, runner NOT adopted, one Warn. The runner's next spawn still resumes the previous transcript — today's behaviour, and the only safe one when the announced id belongs to a different live session. |
| Watcher won the race (`ErrSessionNotFound`) | Session stands on the announced id, so the runner adopts. Debug, not Warn — the normal path until #2137 retires the watcher. |
| No pool callback wired (`adopt == nil`) | Tag rotates and the runner adopts along with it. AC 3 is a rule about the tag's final position, not about which sentinel came back. |
| No runner hook wired (`adoptRunner == nil`) | Nothing is called; the tag half is unaffected. A test-constructed follower, never production. |
| Announced id equals the live tag | Nothing happens at all — the existing guard returns before the rotation, so the runner is never called either. |

Adoption cannot fail: it is one field write under a leaf mutex, with no I/O and no fallible call.
There is no error to return and therefore no return value.

## The window this leaves open, deliberately

The runner adopts AFTER the pool's answer, and the tag rotates before it. Between the two the tag
reads the announced id while the runner still holds the previous one, and a crash inside that
window resumes the pre-reset transcript. That is today's behaviour on every reset, and it is
strictly better than resuming a different live session's transcript, which is what adopting before
the pool's answer would risk on the refusal path. It is left open; a rollback is not wanted.

## Testing strategy

RED first in every case. No live claude: #2138 owns the live proof for this family, and a
fakeclaude e2e is not required.

### `internal/streamsup` — AC 1, AC 2, AC 4

1. **`TestRunner_AdoptSessionID_RespawnResumesAdoptedID`** (AC 1). A real supervised run with the
   `crash` helper child and a tiny backoff, mirroring
   `TestRunner_RestartFresh_ProbeDecidesPerSpawn`. `cfg.ClaudeSessionsDir` is a fixture directory
   holding a transcript for the ADOPTED id only; a `sync.Once`-guarded `onSpawn` adopts on the
   first spawn. Asserted argv sequence:

   - spawn 1: `--session-id <previous>` — no transcript for it, so the probe says create;
   - spawn 2: `--resume <adopted>` — the crash respawn, riding the probe against the staged
     transcript, and carrying the previous id nowhere in its argv.

   The staged transcript is what makes the assertion ride `useCreateForm`'s probe rather than its
   empty-`ClaudeSessionsDir` route, which answers the resume form by a different path and would
   prove nothing. Without the adopt, spawn 2 reads `--session-id <previous>`, so the fixture
   discriminates.

2. **`TestRunner_AdoptSessionID_InstallsIDWithoutRotationMachinery`** (AC 2 + AC 4). Table-driven
   over a different id, the id already held, and the empty id, against a directly constructed
   `Runner` (the `TestRunner_BeginSpawn_FirstSpawnResumesExistingTranscript` pattern — no child
   process, state observed at the source). Each row asserts, after the call:

   - `sessionID` is the expected one (the announced id; unchanged for the same-id and empty rows);
   - `rotatePending` is false and `beginSpawn` reports `forceFirst` false — the fresh-restart form
     is not armed;
   - `freshSeq` is unmoved — the rotation gate's release authorisation did not advance;
   - `rotating` is false, so `turnTarget` reports un-gated and no turn is refused;
   - a recording `iterCancel` was never invoked, and `restartCh` is empty — the two mechanisms by
     which `RestartFresh` and `Restart` end a live child. Asserting them is what "the child is not
     killed" means at the seam: `RestartFresh`'s own doc states these methods drive only
     `restartMu`, a ctx cancel and `restartCh`, so a cancel that never fires and a hint that was
     never sent is the complete statement.

### `cmd/pyry` — AC 3

3. Rows on the existing **`TestSessionResetFollower_PoolAnswerDecidesTheTag`**, which is already a
   table with one row per sentinel and opposite expectations. Each row gains a recorded
   `adoptRunner` double and an expectation over it: `ErrSessionNotFound` adopts the announced id,
   `ErrSessionIDTaken` records no call at all. The table's existing per-row assertion — a later
   event fed through the real `sinkForTag`, read back for the id its envelope carries — already
   pins the tag half as a consequence rather than a field, and the new expectation is checked
   against the same row so the "exactly when" is asserted as one rule and not two.

4. **`TestSessionResetFollower_ForwardsEveryVariantAndToleratesNilNext`** extends by one
   assertion: with a nil `adopt` the runner hook is still called with the announced id — AC 3's
   "a runner with no pool callback adopts along with its tag" — and a follower with a nil
   `adoptRunner` does not panic.

5. **`TestSessionResetFollower_EqualIDChangesNothing`** extends by one assertion: an announced id
   equal to the live tag reaches the runner hook not at all.

### Not tested, and why

A factory-tier assertion that `newStreamRunnerFactory` actually installed the hook is not
reachable by inspection: the factory returns `sessions.Runner` over the unexported `streamRunner`,
so neither the follower nor the `streamsup.Config` can be read back — the limitation
`Config.OnSessionRotate`'s own overview records, and which
`TestSessionParser_MintsOneStablePostureGate` hit before it. Proving it behaviourally needs a real
spawned child driven through the real factory, which is the tier #2138 owns for this family. The
ticket sets the proof level here at unit coverage in `internal/streamsup` plus rows on the
follower's table, and this plan does not exceed it.

## Verification

`go test -race ./internal/streamsup/... ./cmd/pyry/...`, `go vet ./...`, `go build ./cmd/pyry`.
The full-module race suite is the verifier's gate.

## Open questions

- **Does the runner-side hook belong on the follower or on the factory's closure?** Resolved in
  favour of a follower field: the factory has no event to act on, and the rule AC 3 states is
  about `follow`'s own control flow. Recorded here rather than left implicit.
- **Should `follow` keep its three-return shape and repeat the adopt call?** Resolved in favour of
  `rekeyPool`, above. If implementation shows the extraction disturbs an ordering argument in
  `follow`'s doc, the fallback is the repeated call plus a comment naming every site, and this
  section gets a `## Revisions` entry saying so.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No finding, but the delta is worth stating because it is not the one
  #2135 audited. That ticket let a line on the supervised child's stdout reach the session
  registry and the sink tag. This one lets the same line reach the runner's SPAWN IDENTITY, and
  through `beginSpawn` that value becomes an argument to `exec.CommandContext` (via `buildArgs`)
  and an entry in the child's environment (via `spawnEnv`) — two destinations neither the tag nor
  the registry has. Verified, not assumed: `emitConversationReset` is the sole non-test producer
  of `turnevent.ConversationReset`, and it gates on `transcript.ValidStem`, whose
  `uuidStemPattern` is an anchored full match (`^…$`) over a 36-character lowercase UUID stem.
  `AdoptSessionID` re-derives none of that, matching `sessionResetFollower`'s own stated stance.
  **Do not copy `RestartFresh`'s provenance argument onto this method**: its callers hand it a
  daemon-minted id from `rotate` / `RotateForNewSession`, whereas this one's caller hands it
  claude's. The conclusion is the same but the upstream being relied on is a different one, and
  the two must not be read as one case.
- **[Subprocess / external command execution]** No finding, after examining this as the MUST-FIX
  candidate. The adopted id reaches `exec.CommandContext` as one element of an argv slice — there
  is no `sh -c` and no string interpolation anywhere on the path. `ValidStem` excludes a leading
  `-`, so the value can never be read as a flag; it excludes `/`, `.` and every non-hex byte, so
  it cannot escape a path join or carry a traversal. `spawnEnv` appends a single `NAME=<id>`
  element to a slice, so no separator or second-entry injection is expressible.
- **[File operations]** No finding. `AdoptSessionID` performs no I/O at all. It feeds exactly one:
  `useCreateForm`'s `transcript.StatByID`, which runs `ValidStem` BEFORE its `filepath.Join` —
  confirmed by reading `StatByID`, not inferred from the ticket. This plan hand-rolls no join, so
  a non-canonical id can never become an arbitrary-path existence oracle that flips the spawn's id
  flag. The check-then-use gap between `useCreateForm`'s stat and claude's own `--resume` open is
  pre-existing, unchanged in kind, and bounded by the same anchored stem.
- **[Concurrency]** OUT OF SCOPE, examined and deliberately not fixed. `restartMu` is taken
  exactly once, released before return, nests no other lock and does no I/O, so its leaf charter
  and the `restartMu → mu` ordering are untouched. The real finding is that `sessionID` gains a
  second writer while `follow` is not a transaction: a concurrent `RestartFresh` can land between
  the pool's answer and the runner adopt, leaving the runner on the announced id while
  `rotatePending`/`freshSeq` describe the daemon's rotation. A compare-and-swap
  (`adopt only if sessionID == oldID`) was considered and **rejected**: it would fail in the
  direction that hurts — the runner silently keeping a stale id while the tag moved is exactly the
  dark-conversation shape #2135 exists to prevent — it contradicts AC 3's rule, and the ticket
  explicitly designs against a rollback. The interleaving is not introduced here (today it leaves
  the tag and the registry disagreeing instead), and no failure of this shape has been observed.
- **[Error messages, logs, telemetry]** No finding. The one new record is `AdoptSessionID`'s
  empty-id Warn, a fixed string with no id and no claude-authored content, mirroring
  `RestartFresh`'s. `follow`'s two existing records are unchanged and already content-free. `Run`'s
  existing Info "spawning claude" puts the composed argv — and so the adopted id — in the daemon
  log; that is pre-existing for every id this runner has ever spawned under, and a session id is
  the pool's registry key rather than a secret.
- **[Callback nullability]** No finding, after verification that the plan's own safety rests on
  it. AC 3 has the runner adopt even when no pool callback is wired, so if
  `RunnerConfig.AdoptAnnouncedReset` were ever nil in production, an announced id would be adopted
  with NO collision check and the next spawn could resume an arbitrary other live session's
  transcript. Verified: both non-test assignments live in `internal/sessions/pool.go`, there is no
  third, and `newStreamRunnerFactory` passes the field straight through — so the nil path is
  test-only, as the follower's field doc states. Recorded because a future runner-construction
  site that forgets the field would silently delete the destination boundary while every test
  here stays green.
- **[Tokens, secrets, credentials]** Not applicable, and specifically: this change creates, reads,
  stores, rotates and compares no credential of any kind. The single value it handles is a session
  id that is already the registry key and already present in argv and logs.
- **[Cryptographic primitives]** Not applicable: no randomness, no hashing, no key material, and
  no comparison of an attacker-controlled value against a secret. The id is minted upstream by
  `sessions.NewID` on the paths that mint one; this change mints nothing.
- **[Network & I/O]** Not applicable: no socket, no reader, no deadline and no size cap belong
  here. The value arrives already length-bounded — `ValidStem`'s anchored match IS the cap, which
  is why `emitConversationReset` documents a `truncateField` beside it as dead code.
- **[Threat model alignment]** The relevant threat is the one `sessionResetFollower`'s own doc
  names: a line on the supervised child's stdout mutating daemon state. This ticket extends its
  reach to the next spawn's identity and answers it with the same two-boundary structure — SHAPE
  settled upstream by `ValidStem`, DESTINATION settled downstream by `Pool.AdoptAnnouncedID`'s
  collision refusal, which binds only because `follow` unwinds. Placing the single runner-adopt
  call site on the far side of that unwind is what keeps the extension inside the existing
  boundary instead of beside it.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-07
