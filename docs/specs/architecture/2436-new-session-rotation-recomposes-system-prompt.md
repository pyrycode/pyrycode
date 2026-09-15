# #2436 — `new_session` rotation recomposes the appended system-prompt file

## Files read

- `internal/sessions/systemprompt.go` → `refreshSystemPrompt` — the only recompose
  funnel; its two properties (one caller, `stateActive` early return) are the whole
  defect. Also `composeSystemPromptFor`, `clientSection`, `admitClient`,
  `admissibleClientField`, `maxNamedClients` (its doc states the byte ceiling this
  feature may cost, which becomes the retention bound below), `attachedClients`,
  `conversationPrompt`, `writeSystemPromptFile`, `clientIdentityTimeout`.
- `internal/sessions/pool.go` → `saveLocked` → `saveRegistryLocked` — already fsyncs
  on the rotation's own goroutine, inside `p.mu`. That settles whether the recompose's
  write is a new cost class on that goroutine: it is not.
- `internal/relay/v2session.go` → `maxRetainedClientNameBytes` (256),
  `maxRetainedClientVersionBytes` (64) — what one conn may park, and therefore what a
  resolved identity slice can weigh before this ticket decides what to retain.
- `docs/protocol-mobile.md` § Security model, threats 1 (prompt injection — the
  privileged surface this file composes) and 7 (DoS — rate limiting deferred, which is
  what makes a stall on the dispatch goroutine remotely amplifiable).
- `internal/sessions/transition.go` → `RotateForNewSession` — the rotation the ticket
  opens: mint, `rekeyLocked`, `saveLocked`, `notifyTransition`, and nothing else.
  `AdoptAnnouncedID` beside it, to settle what this ticket deliberately leaves alone.
- `internal/sessions/pool.go` → `Activate` (the refresh's one caller, and AC #4's
  hot path), `rekeyLocked` (why the path must not be re-derived), `pickLRUVictim`
  (the `capMu` → `mu` order the refresh must not invert).
- `internal/sessions/session.go` → the `Session` struct's `systemPromptPath`,
  `systemPrompt`, `label` fields — the `Pool.mu`-not-`lcMu` discipline the new field
  joins. `Session.Activate` / `Session.Evict` — the only writers of the lifecycle
  state, which is why no rotation leaves `stateActive`.
- `cmd/pyry/main.go` → `startFreshRunner` — the caller. `RestartFresh` consumes
  `rotate()`'s return value, so rotate-before-relaunch is a data dependency, not a
  convention.
- `internal/streamsup/runner.go` → `RestartFresh` — cancels the live child
  immediately, which is what makes "the write must precede the relaunch" a race and
  not a preference.
- `internal/sessions/pool_system_prompt_test.go` → `assertPromptFileHolds`,
  `systemPromptArgPath`, `helperPoolWithConversations`, `conversationWithPrompt`,
  `TestPool_RotatedSession_KeepsItsPromptFile` (the nearest coverage, and the door
  this path does not go through).
- `internal/sessions/systemprompt_client_test.go` → `clientResolverHolder`,
  `assertPromptFileNames`, `TestPool_AttachedClients_Total` — the client-section
  test vocabulary this ticket reuses.
- `docs/knowledge/features/sessions-package-key-types-writesystemprompt-systemprompttext.md`
  § "Composition and resolution", § "Naming the attached client (#2148)" — the
  section is a **past-tense transcription** composed at spawn time and never
  restated mid-session. That sentence is the lever AC #3 is satisfied with.

## Context

`set_system_prompt` advertises that a new value takes effect at the conversation's
next session start. `new_session` **is** a next session start and does not honour it:
the respawned child comes up on the composition written before the prompt was saved,
and — because no rotation leaves `stateActive` — stays stale for every later turn too.

`refreshSystemPrompt` is the only recompose funnel and the rotation misses it twice
over: its one caller is `Pool.Activate`, which the rotation never reaches
(`handleNewSession` → `StartNewSession` → `startFreshRunner` → `RotateForNewSession`
→ `RestartFresh`), and its `stateActive` early return would suppress it even if it
were called. #2148's attached-client section rides the same function and is equally
stale.

No ADR is warranted: this adds a second entry point to an existing funnel and changes
no boundary.

## Design

Three moves, all inside `internal/sessions`.

**1. The composition step becomes shared.** A new unexported
`(*Pool).writeComposedPrompt(sess *Session, clients []ClientIdentity)` owns what both
entry points do identically: resolve the operator bytes from the conversations
registry, compose through `composeSystemPromptFor`, write
`sess.systemPromptPath` **verbatim** via `writeSystemPromptFile`, and record what the
session was composed with. It keeps `refreshSystemPrompt`'s existing posture in full —
the write runs off `p.mu`, a failure is logged and swallowed (the rename leaves the
previous complete composition in place), and no prompt bytes reach the log.

**2. The session carries the ADMITTED client set it was last composed against.** A new
`Session.promptClients []ClientIdentity` field, written by `writeComposedPrompt` under
`p.mu` (write) and read under `p.mu` (RLock) — `systemPrompt`'s discipline exactly, and
deliberately not `lcMu`.

What it holds is the finding the security pass turned up: it must hold only values that
have already crossed `admitClient`, never the resolver's raw answer. So `clientSection`'s
admit-sort-dedup-cap prologue is extracted as `admittedClients(clients) []ClientIdentity`,
returning nil whenever nothing would render (no admissible identity, or more than
`maxNamedClients` of them); `clientSection` calls it and becomes render-only, and
`writeComposedPrompt` stores its result. Two properties follow. Retention is capped at
`maxNamedClients × (maxClientNameBytes + maxClientVersionBytes)` per session — the exact
ceiling `maxNamedClients`' doc already claims is the most this feature may cost —
rather than at whatever a client with many conns can make `ActiveConns` return. And no
unadmitted remote byte is ever held past the call that resolved it.

`clientSection` keeps admitting its own input rather than trusting callers to have done
it, so `composeSystemPromptFor` stays total over hostile input for its direct callers
(the #2148 tests drive it with exactly that). Re-admitting an admitted set is a no-op —
every predicate already holds, the set is already sorted and deduped — so the section
the rotation composes is byte-identical to the one the previous compose wrote.

The stored slice is treated as immutable once stored: a later compose replaces the slice
header and MUST never append into the backing array, which is what makes the off-lock
read in the rotation path race-free (the same argument `conversationPrompt` makes about
`SetSystemPrompt` replacing a pointer rather than mutating a pointee). The field's doc
states that as a prohibition, because an `append` is how a future writer would break it.

**3. The rotation recomposes with the carried set and resolves nothing.** A new
`(*Pool).refreshSystemPromptForRotation(sess *Session)`, called from
`RotateForNewSession` after its `p.mu` release and above `notifyTransition`. It differs
from `refreshSystemPrompt` on exactly two axes, and both are the ticket:

- **No `stateActive` guard.** That guard keeps a disk write off `Activate`'s LRU-touch
  hot path and honours "setting a prompt does not restart a running session". Neither
  is in tension with a rotation that is already discarding the child, and the guard is
  left untouched at its own site so AC #4 holds unchanged.
- **No client-identity resolve.** The whole dispatch from `handleNewSession` down runs
  on `V2SessionManager`'s single Run goroutine, and `attachedClients` funnels its
  request back onto that same goroutine — called from Run it cannot be answered, so it
  would stall the dispatch for `clientIdentityTimeout` and then silently drop the
  section. That stall is not merely a latency bug: the rotation is driven by a client
  frame and § Security model's threat 7 leaves rate limiting deferred, so a
  `new_session` loop would stall *all* v2 dispatch — every conn, every session — for
  250 ms a frame. The carried `promptClients` is used instead. `clientSectionLead` is written
  in the past tense as a transcription of the clients attached when the session
  started, so carrying the previous resolve forward keeps that sentence true, and it is
  what keeps AC #3's "must not silently drop the client section" a structural property
  rather than a timing accident.

Nothing is re-derived from the post-rotation id: `rekeyLocked` has already moved the
session onto the minted id, and the path frozen into `spawnBase` still carries the
pre-rotation one. `writeSystemPromptFile` takes a resolved path for exactly this.

**Deliberately unchanged.** `AdoptAnnouncedID` follows a reset claude has already
performed — its child is up and has read its prompt file — so a recompose there would
reach nobody; `RotateID` and `RotateBootstrapForSelfHeal` likewise drive no fresh
spawn of their own. `Pool.Activate` and `refreshSystemPrompt` keep their bodies apart
from the extracted helper. The `new_session` rotation is the only path this opens.

## Concurrency model

No new goroutine and no new lock. Ordering inside `RotateForNewSession`:

1. `p.mu` (write): membership check, capture the `*Session`, `rekeyLocked`,
   `saveLocked`. Unchanged.
2. `p.mu` released → `refreshSystemPromptForRotation`: one RLock to read `label` and
   `promptClients`, the file write **off** the lock, one Lock to record what was
   composed. The documented `capMu` → `mu` → `lcMu` order cannot invert: no lock is
   held across the write and `capMu` is not taken on this path.
3. `notifyTransition` off `p.mu` — the established leaf-callback discipline, now with
   the write already landed, so nothing an observer drives synchronously can observe a
   half-updated file.

The write lands before `RotateForNewSession` returns and `startFreshRunner` consumes
that return value before calling `RestartFresh`, which is what puts the write ahead of
the child cancel.

The write includes an fsync, and it now runs on the relay's Run dispatch goroutine.
That is not a new cost class on this path: `saveLocked` → `saveRegistryLocked` already
fsyncs there on every rotation, and does it *inside* `p.mu` where this one is outside.
A slow disk therefore doubles an existing stall rather than introducing one, which is
why the write is not handed to a goroutine — an async write would race the respawn the
ordering above exists to win.

`Pool.Remove` racing this rotation could remove the prompt file between the capture
and the write, leaving a recreated file behind. That window is pre-existing and
identical in shape to `Activate`'s (`Lookup` then refresh); this ticket adds no guard
for a failure mode nothing has observed.

## Error handling

- Mint failure / absent `oldID`: unchanged — `("", err)`, no mutation, no transition,
  and now also no recompose (both returns precede it).
- `sess.systemPromptPath == ""` (the bootstrap, and any hand-built `Session` literal
  that never spawns): return without writing, as `refreshSystemPrompt` does.
- Write failure: logged at Warn with the error and no prompt bytes, then swallowed.
  The rename leaves the previous complete composition in place, so the worst case is
  one-revision-stale bytes rather than a failed rotation — `refreshSystemPrompt`'s
  ruling, inherited. `sess.systemPrompt` is left alone on that path so it keeps
  describing what the file actually holds.
- No new error value, no new sentinel, and `RotateForNewSession`'s signature is
  unchanged.

## Testing strategy

Hermetic, in `internal/sessions`, in a new `pool_rotate_system_prompt_test.go`. All
five drive `pool.RotateForNewSession` directly — the pool-side half is where the
missing recompose lives — and assert on **the bytes behind the path the argv names**,
never on the argv, which is byte-identical either way.

- `TestPool_RotateForNewSession_RecomposesPromptFile` (AC #1, #2): prompt changed while
  the session runs, then rotate. The file at the argv's path holds the current prompt,
  asserted **synchronously** with no polling — that the assertion can be made at all
  is the proof the write landed before the call returned. Also: no file appears under
  the minted id's name, and `SystemPromptFor(newID)` reports the new operator bytes.
- `TestPool_RotateForNewSession_KeepsTheClientSection` (AC #3): with a client attached
  and the prompt changed, the post-rotation file is constant, then section, then
  operator bytes — wanted bytes assembled from the rule locally rather than by calling
  the composer, so a changed rule cannot satisfy both sides at once.
- `TestPool_RotateForNewSession_DoesNotResolveClients` (Technical Notes, never-from-Run):
  a counting resolver whose second call blocks until cleanup. After the rotation the
  count is still 1 and the section survived — a fresh resolve on this path would both
  stall and return nil, so a design that resolves fails both assertions deterministically.
- `TestPool_RotateForNewSession_NoStoredPrompt_KeepsConstant` (AC #3, second clause):
  no stored prompt, no resolver — the constant byte for byte.
- `TestPool_Activate_AlreadyActive_WritesNothing` (AC #4): a second `Activate` on a
  live session after the stored prompt moved performs no resolve (count unchanged) and
  no write (content and mtime unchanged), so it is the rotation alone that opens.
- `TestPool_RotateForNewSession_RetainsOnlyAdmittedClients` (security review, trust
  boundaries): a hostile identity resolved at activation — a name carrying a newline,
  beside an admissible one — is absent from the post-rotation file, which names the
  admissible client alone. The file cannot by itself distinguish retaining raw from
  retaining admitted values (`clientSection` re-admits either way), so this test also
  asserts the session retained the admitted set, which is the property the pass found
  and the only place it is observable.
- `TestPool_RotateForNewSession_NeverLogsPromptBytes` (security review, logs):
  `TestPool_SpawnPath_NeverLogsPromptBytes`' guarantee extended to the path this ticket
  opens, reusing that test's `syncBuffer` capture — a rotation whose write fails must
  put the error in the log and no fragment of the operator's prompt. Driven with an
  unwritable prompt directory so the failure branch is the one under assertion.

`startFreshRunner`'s rotate-before-`RestartFresh` order needs no new test: the second
half consumes the first half's return value, so the order is a data dependency, and
`cmd/pyry/dispatch_arms_test.go` plus `cmd/pyry/inbound_deliver_rotation_test.go`
already drive the real function in production order.

Proof tier is hermetic, per the ticket: no `e2e_realclaude` test is in scope, and the
live half already exists as pyrycode-desktop#1433's `test.fixme` plus #2150's live
marker test for the non-rotation flow.

## Documentation handoff

Pending, owned by the documentation stage. Required change:
`docs/knowledge/features/sessions-package-key-types-writesystemprompt-systemprompttext.md`

- § **Composition and resolution**: record that a prompt saved while a session is
  running reaches that conversation's next child whether that child comes from a first
  message, a revive, or a `new_session` rotation, and state which funnel recomposes on
  each (`refreshSystemPrompt` from `Pool.Activate` for the first two;
  `refreshSystemPromptForRotation` from `Pool.RotateForNewSession` for the third).
- § **Naming the attached client (#2148)**: note the never-from-Run constraint on the
  client-identity resolve — the rotation dispatch runs on `V2SessionManager`'s Run
  goroutine, which `attachedClients` funnels back onto, so that path carries the
  session's last-resolved set forward instead of resolving. This is the property a
  later reader is most likely to break.

## Open questions

- Store the carried client set as identities or as the rendered section string?
  Resolved during the design: identities, so `admitClient` stays the single door every
  client byte crosses at compose time. Recorded here because it is the choice a reader
  of the new field will re-ask.
- Whether `writeComposedPrompt` should also skip the `sess.systemPrompt` update when
  the operator bytes are unchanged (one fewer `p.mu` acquisition). Deferred: no
  observed cost, and the unconditional write keeps the field's meaning — what the file
  was last composed with — trivially true.

## Security review

**Verdict:** PASS (after one revision — the first draft failed on trust boundaries and
was changed before this section was written; see the first finding.)

**Findings:**

- [Trust boundaries] **MUST FIX, fixed in the plan above.** The first draft stored the
  resolver's raw answer in `Session.promptClients` as a `slices.Clone`. `ClientIdentity`
  is remote-authored and unvalidated — `admitClient` is the one door it crosses — and
  retention moved those bytes from a single call frame onto a long-lived struct for the
  first time. Nothing unadmitted could reach the file (`clientSection` still admits),
  but an authenticated client holding many conns could park
  `conns × (256 + 64)` bytes per session indefinitely, where before this ticket the
  same bytes were garbage the moment the compose returned. Fixed by extracting
  `admittedClients` and retaining only what renders, capping it at
  `maxNamedClients × (maxClientNameBytes + maxClientVersionBytes)` — 4 × 96 bytes per
  session, the ceiling `maxNamedClients`' own doc claims. The trust boundary now sits
  at the moment of *retention* as well as the moment of rendering.
- [Trust boundaries] No further findings. `admitClient` remains the single door and
  gains no second opinion: `clientSection` keeps admitting its own input, so
  `composeSystemPromptFor` stays total over hostile values for callers that reach it
  directly. The operator's bytes keep #2149's `Registry.SetSystemPrompt` as their only
  validating door — this ticket adds no second validation and no new writer.
- [Trust boundaries] The section's *claim* after a rotation: `clientSectionLead` says
  "clients attached when this session started", and a rotation starts a fresh child on
  a carried set. The sentence is a past-tense transcription by construction — #2148
  documents it as spawn-time and never restated — so carrying it forward keeps it true
  rather than making it stale. Design decision, taken deliberately per the ticket's
  Technical Notes, not a finding.
- [Tokens, secrets, credentials] No findings. No token is minted, stored, compared or
  logged. `NewID`'s `crypto/rand` mint is untouched and nothing is derived from the
  minted id (AC #2 forbids it, and it is also the property that keeps the write inside
  the `ValidID`-gated path). The operator's prompt is the only sensitive payload and it
  reaches no new location: one file, one path, `0600`.
- [File operations] No findings. Path traversal is structurally impossible on this
  path: the write targets `sess.systemPromptPath` verbatim, frozen at construction by
  `systemPromptPathFor`, which gates the id on `ValidID` before it becomes a filename.
  No `Stat`-then-`Open`, so no TOCTOU. Mode is `os.CreateTemp`'s `0600` preserved by
  the rename, inside the `0700` directory `systemPromptPathFor` creates. The rename
  *replaces* a symlink at the destination instead of writing through it. Atomicity is
  load-bearing here rather than incidental: a child is about to read this file, and
  temp-plus-fsync-plus-rename is what guarantees it reads a complete composition — the
  new one, or on failure the complete previous one, never a truncated prefix.
- [File operations] The trap avoided, named because a plausible design falls in: the
  bootstrap session's `systemPromptPath` is `""` and the early return covers it. A
  variant that fell back to the pool's daemon-scoped `systemPromptPath` would write one
  conversation's operator text into the file every other session reads.
- [Subprocess / external command execution] No findings — nothing about the argv
  changes, which is AC #2's clause and also the security-relevant one: the prompt keeps
  reaching claude as a *file path*, never as an argument, so no prompt byte enters a
  command line or a spawn log. The composed file's content is read by claude as a
  privileged surface, and the two properties that hold its structure — `admitClient`'s
  character-set refusal (control characters and the quote delimiter) and the placement
  of client bytes inside quotes after `clientSectionLead`, never at line start — are
  untouched. No `sh -c`, no env change.
- [Cryptographic primitives] Not applicable: the design introduces no primitive, no
  key, no nonce and no comparison of a secret.
- [Network & I/O] No findings for this design, and the category is the reason its
  central choice exists. A client-identity resolve from the rotation path would funnel
  a request back onto the `V2SessionManager` Run goroutine that is already executing
  the rotation, stalling *all* v2 dispatch for `clientIdentityTimeout` and then
  silently dropping the section. With § Security model's threat 7 leaving rate limiting
  deferred, a `new_session` frame loop makes that a remotely-triggerable 250 ms stall
  per frame. The carried set means no resolve, and
  `TestPool_RotateForNewSession_DoesNotResolveClients` is what keeps a future edit from
  reintroducing it. No socket read, header, timeout or TLS surface is touched.
- [Errors, logs, telemetry] SHOULD FIX, addressed in Phase B. The failure branch logs
  at Warn, and it must carry the error alone — no composed text, no operator bytes, no
  client name. A hostile client name in particular must gain no log line: this package's
  rule is that a refusal is silent precisely so it has none.
  `TestPool_RotateForNewSession_NeverLogsPromptBytes` asserts it on the failing write.
- [Concurrency] No findings. Only `p.mu` is taken, never across the write, and `capMu`
  is not taken at all, so the documented `capMu` → `mu` → `lcMu` order cannot invert.
  The membership check, the `*Session` capture and `rekeyLocked` stay inside one `p.mu`
  hold, so there is no check-then-mutate gap. No goroutine is spawned, so none can leak.
  Interruption mid-write is covered by the rename. The one invariant a future writer
  could break — `promptClients` must be replaced, never appended into — is stated as a
  prohibition in the field's doc rather than left to inference.
- [Concurrency] Two accepted windows, both pre-existing in shape and neither a
  privilege or injection concern. (a) `Pool.Remove` racing a rotation can remove the
  prompt file between the capture and the write, leaving one recreated file behind; it
  lives inside `session-prompts/`, which `Pool.Run`'s shutdown defer and `Pool.New`'s
  startup purge both sweep, so it cannot outlive the daemon. (b) An `Activate` refresh
  of a just-evicted session can interleave with a rotation and land one-revision-stale
  operator bytes over fresher ones; both writes are complete compositions, and closing
  it would mean composing inside `p.mu`, which is the I/O-in-the-critical-section this
  design exists not to do. Same class as the staleness tolerance #2150 already accepts.
- [Threat model alignment] Threat 1 (prompt injection, high / partial) is the relevant
  one: this file is a privileged surface, and text landing in it instructs an agent
  holding tool access. The design changes *where* composition happens, not *what*
  crosses the door, and after the first finding's fix it narrows what the daemon retains
  from a client. Threat 7 (DoS, deferred) is engaged and answered above. Threats 2–6 and
  8 concern the relay leg, the wire and the static key, none of which this ticket
  reaches; no relay, transport, crypto or pairing code is touched.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-15
