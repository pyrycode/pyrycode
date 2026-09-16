# #2463 — `set_session_settings` on a dormant session lands instead of being refused

The write half of #2449. A `set_session_settings` frame naming a session the
daemon holds only as a persisted dormant registry entry is answered
`session_settings_updated` and its model and effort are merged into that entry,
in place of today's `session.not_found`. The write materialises nothing, and a
frame naming a posture is still refused.

## Files read

- `internal/sessions/pool.go` → `Pool.dormant` — the field's own docstring carries
  the invariant this ticket has to extend: the key set is populated only in `New`
  and only ever shrinks, which is what `revivedSettings` and `DormantSettingsFor`
  rest on. A value-replacing write has to be folded into that statement.
- `internal/sessions/pool.go` → `Pool.UpdateSettings` — the live twin whose shape
  the new method carries over (lookup, validate, field-by-field merge, no-op
  short-circuit, `saveLocked`, rollback) and whose second half (argv recompose,
  posture install, in-band delivery, supervisor capture) has no dormant analogue.
- `internal/sessions/pool.go` → `Pool.DormantSettingsFor`, `Pool.revivedSettings` —
  the two readers of `p.dormant`. Both build the posture structurally from `Model`
  and `Effort` alone, which is why a persisted posture is invisible and therefore
  must not be accepted. `DormantSettingsFor` doubles as the adapter's existence
  probe, because it already has a miss of its own.
- `internal/sessions/pool.go` → `Pool.saveLocked` — writes `p.dormant`'s entries
  back beside the live sessions, so a merge into an entry reaches disk with no new
  persistence path.
- `internal/sessions/pool.go` → `Pool.Lookup` — resolves `""` to the bootstrap,
  which is why the adapter's `sessionID == ""` guard stays.
- `internal/sessions/registry.go` → `registryEntry`, `permissionModeForDisk`,
  `settingsFromEntry` — the on-disk shape the merge mutates, and the rule that the
  disk cannot hold a mode contradicting `yolo`.
- `internal/sessions/session.go` → `SettingsUpdate`, `claudeSettingsArgs` — the
  presence contract the new method reuses, and the argv sink a written model
  reaches on revive (`--model` carries `familyAlias`, not the stored bytes).
- `cmd/pyry/main.go` → `settingsUpdaterAdapter.UpdateSettings` — the seam that owns
  sentinel-to-wire mapping and the model-membership gate; the only consumer.
- `cmd/pyry/main.go` → `resolveBoundRunSettings` — the live-first composition this
  write mirrors, including its recorded reason for the order and its analysis of
  the window between the two halves.
- `cmd/pyry/session_model_list.go` → `retainedModelVocabulary`, `sessionRetainedModelList`
  — already degrades correctly for an id it cannot look up (bootstrap hold, then
  the saved store), which is what lets the gate run for a dormant id unchanged.
- `internal/relay/v2session_settings.go` → `handleSetSessionSettings` — confirms
  the relay half needs no change: it validates shape, then maps a nil error to
  success and `ErrSessionUnknown` to `session.not_found`.
- `docs/knowledge/features/sessions-package-key-types-pool-updatesettings.md` — the
  package overview's "This lookup is `p.sessions` only" paragraph is the statement
  this ticket changes; it already names #2463 as the deferred design.
- `docs/knowledge/features/sessions-package-key-types-pool-settingsfor.md` — #2449's
  recorded rationale for a second read rather than a fallback, which this write
  copies.
- `internal/sessions/pool_dormant_settings_test.go` → `liveIDs` — the
  materialises-nothing assertion's subject, reused verbatim.
- `internal/sessions/pool_dormant_entries_test.go` → `helperPoolWarmStart`,
  `helperDormantID`, `entryByID` — the warm-start fixture the new tests build on.
- `internal/sessions/pool_settings_test.go` → `helperPoolArgvRecorder`, `waitArgvRaw`;
  `internal/sessions/pool_mcp_settings_test.go` → `waitArgv` — the argv proof for the
  revive half of AC 3.
- `internal/sessions/pool_update_settings_test.go` → `TestPool_UpdateSettings_NoOpWritesNothing`
  — the bytes-and-mtime shape the dormant no-op assertion copies.
- `cmd/pyry/run_config_test.go` → `TestResolveBoundRunSettings_DormantRealPool` — the
  cmd-side pattern for warm-starting a real pool from a hand-written registry.
- `cmd/pyry/session_model_list_test.go` → `newModelListTestPool`, `modelListPlan`,
  `modelListRunner`, `TestSettingsUpdaterAdapter_RejectsBeforeWholeFrameMutation` —
  the adapter's existing test surface and the runner that answers the vocabulary gate.

## Sizing — one ticket, with the line ceiling overrun stated

Five of the six boundary lines are comfortably clear: 2 production source files
(`internal/sessions/pool.go`, `cmd/pyry/main.go`), 0 new exported types or
interfaces (one method and one sentinel), 1 consumer call site, 5 acceptance
criteria, 2 reject branches.

The sixth is overrun. Total written work counts to roughly 990 lines against a
ceiling of 800: ~145 production, ~405 test, and this plan at ~450 — of which the
mandatory `## Security review` section is about a quarter. **The overrun is not
grounds for a split here, because the floor rule wins.** Every slice this ticket
decomposes into has exactly one consumer inside its own family:
`Pool.UpdateDormantSettings` is called only by `settingsUpdaterAdapter`, and the
adapter change cannot exist without the method. Cutting along AC lines is worse
still — the posture refusal is a guard inside the method, so shipping the write
without it would persist-and-report-success in the interim, which is the exact
failure this ticket exists to prevent. A one-consumer child cannot be verified on
its own, and no resume fixes that; a ceiling overrun costs at most a continuation
leg.

The refiner sized this at ~600 lines and 2 production files against #2449, which
shipped 791 added lines across 3 production files as one ticket. The structural
count agrees with that; the arithmetic overrun is carried by the spec document,
not by the change.

## Context

`set_session_settings` naming a dormant session is refused with
`session.not_found`, so a model or effort picked on a restarted channel before
its first message does not land. `Pool.New` materialises only the bootstrap, so
after a restart that is every channel. #2449 fixed the read half and made the
bug reachable: the menus now render correctly and the first write is refused.

The decision recorded in the ticket, and the one this plan implements: **persist
into the dormant entry, do not revive.** Reviving on a settings write has no
spawn directory to re-validate through `resolveSpawnDir` (the write is keyed by
session, and `conversations.Registry` exposes no session-keyed read), and a
settings frame must not be able to materialise a session — #2449 AC 3's rule,
which exists so N channel activations cannot become N sessions. The dormant
entry is already the authority for what a revive materialises, so a merge into
it is visible to the read immediately, is what the first message revives under,
and survives a second restart.

The posture is excluded rather than persisted alongside, because both readers of
`p.dormant` build the posture structurally from `Model` and `Effort` alone: a
persisted posture is invisible to them, so accepting one would report success for
a change the very next read contradicts. Teaching the revive to read a persisted
posture back would resurrect the bypass a restart revokes (#1487, ADR 035 as
amended by #2448) and is a security decision of its own, not this fix.

No ADR is warranted — this is the write-side application of a boundary ADR 035
and #2449 already decided, and the package overview named #2463 as its deferred
half.

## Design

### `internal/sessions` — a second pool write beside the live one

```go
// Declared beside ErrSessionNotFound in pool.go.
var ErrDormantPostureUnsupported = errors.New("sessions: dormant session cannot store a permission posture")

// UpdateDormantSettings merges update's Model and Effort into id's dormant
// registry entry and persists it. Miss → ErrSessionNotFound; an update naming
// YOLO or PermissionMode → ErrDormantPostureUnsupported with nothing written.
func (p *Pool) UpdateDormantSettings(id SessionID, update SettingsUpdate) error
```

A **second write**, not a fallback folded into `UpdateSettings`, mirroring what
#2449 did for the read. `UpdateSettings`' body past its persist is entirely about
a live session — the recomposed argv, the posture install, the in-band delivery,
the supervisor capture — and none of it has a dormant analogue; folding the two
would also change what `ErrSessionNotFound` means for every other caller of the
live write, which is the reason `DormantSettingsFor` was not folded into
`SettingsFor`.

Behaviour, in order:

1. Take `p.mu` (write). Look up `p.dormant[id]`; a miss unlocks and returns
   `ErrSessionNotFound` — the same shape as `UpdateSettings`' own lookup, and no
   entry is ever created.
2. Refuse a posture: `update.YOLO != nil || update.PermissionMode != nil` →
   `ErrDormantPostureUnsupported`, before any mutation. This sits where
   `validatePermissionUpdate` sits in the live twin.
3. Merge **field by field** into a copy — `Model` and `Effort` only, each
   overwritten when its pointer is non-nil, `""` included (the explicit clear
   keeps its "run at claude's own default" reading; there is no child to restart,
   so the live path's restart semantics simply do not arise). The entry's `YOLO`
   and `PermissionMode` bytes are never read and never written, which is the
   structural exclusion `revivedSettings`, `mintSettings` and `DormantSettingsFor`
   already follow: a field added to `registryEntry` later is not carried until
   someone opts it in.
4. No-op short-circuit: if neither field's value changes, unlock and return nil
   without touching the registry. Compared on the two scalar fields rather than on
   the whole `registryEntry`, deliberately — `registryEntry` embeds two
   `time.Time`s, and `==` on those compares representation rather than instant.
5. Otherwise store the merged entry back under the same key, `saveLocked`, and on
   a save error restore the previous entry before unlocking and returning the
   error.

`Pool.dormant`'s field docstring is amended: the KEY SET is still populated only
in `New` and only ever shrinks, and this write replaces a value under an existing
key — adding no key, removing none — so the "no live `*Session` behind it" half of
the meaning is untouched and the live/dormant partition still holds. The nil-map
safety argument extends unchanged: the write is reachable only after a read hit,
which a nil map cannot produce.

### `cmd/pyry` — live-first composition at the adapter

`settingsUpdaterAdapter.UpdateSettings` gains two changes and keeps everything
else, including its `sessionID == ""` guard (`Pool.Lookup("")` resolves the
bootstrap).

**The membership gate learns about dormant ids.** Its live probe becomes a live
probe followed, on a miss, by `Pool.DormantSettingsFor` used purely as an
existence check (its value is discarded; it already has the miss this needs, so no
new pool surface is introduced). Only when both miss is it `ErrSessionUnknown`.
The check itself then runs unchanged for a dormant id, which is load-bearing: an
unvalidated model in a dormant entry becomes the revived child's `--model`, the
argv-injection boundary. `retainedModelVocabulary` already degrades correctly for
an id it cannot look up — bootstrap hold, then the saved store — the same sources
the client's menu was published from.

**The write composes live-first**, the way `resolveBoundRunSettings` composes the
two reads: `Pool.UpdateSettings` first, and only its `ErrSessionNotFound` falls
through to `Pool.UpdateDormantSettings`. Any other error (an unsupported mode, a
conflicting pair, a failed save) returns as it does today. A session the pool
holds is therefore never written into a stale persisted entry, and that is a
guarantee of this function rather than only of the pool's bookkeeping.

Both `sessions.ErrSessionNotFound` and `sessions.ErrDormantPostureUnsupported`
map to `relay.ErrSessionUnknown`, so the posture refusal keeps today's
`session.not_found` — the ticket's decision: no client has been observed
mis-reading it, the user-visible outcome is identical, and a new code is wire
vocabulary plus client work.

**Why a distinct sentinel rather than returning `ErrSessionNotFound` from the
pool.** Sentinel-to-wire mapping belongs at the consumer call site, which this
adapter's own docstring states, and one shared sentinel would mean two different
things one line apart in this function: "try the other half" for the live write's
miss, and "refuse" for the dormant write's. The pool method would also be
reporting "not found" about an id it did find. The wire answer is identical
either way.

The empty id gains no new path: neither `p.sessions` nor `p.dormant` carries a
`""` key, so both writes miss and the answer is `ErrSessionUnknown`, exactly as
today. Fall-through-to-bootstrap has no expression in this code path.

`internal/relay` is untouched: `handleSetSessionSettings` replies success on nil
and `session.not_found` on `ErrSessionUnknown` already.

## Concurrency model

No goroutines, no new locks, no new lock-order edge. `UpdateDormantSettings`
takes `p.mu` (write) exactly once and must be called with `p.mu` unheld — Go's
`RWMutex` is not reentrant, `UpdateSettings`' contract verbatim. It never takes
`Session.lcMu`; `p.dormant` is a `Pool.mu`-guarded field like `sessions` and
`label`. `saveLocked` runs under the held lock, as it does for every other
mutator.

**The window between the adapter's two writes.** A revive landing there
materialises the id and retires its dormant entry. `p.dormant` only ever shrinks,
so the id can move only that way: the dormant write finds a **clean miss**, never
a torn entry. That answers `session.not_found`, and the refusal is correct rather
than merely safe — the settings the client asked for were not applied to
anything. No retry is attempted; the operator's next pick lands on the now-live
session through the live half. This is recorded in the method's docstring.

The adapter's existence probe and its write are likewise not atomic with each
other. The same one-way movement bounds it: an id that was dormant at the probe is
either still dormant at the write or has become live, and the live write is tried
first, so neither ordering can write a validated model into the wrong half.

**A third window belongs to `Revive`, not to this seam, and is filed as #2492.**
`Pool.Revive` evaluates `p.revivedSettings(id)` as an argument to `materialise`,
so that read's RLock is released before `materialise` takes the write lock and
retires the entry. A `UpdateDormantSettings` landing in that gap is persisted and
then dropped: the session materialises under the value read before the write, and
the next `saveLocked` writes it back. This ticket does not fix it — the fix
changes `Revive`/`materialise`'s evaluate-then-materialise call shape, a live path
outside this ticket's scope (§ Scope Discipline). It carries no security
consequence and is self-correcting; #2492 has the reproduction and the analysis.
The new method's docstring names it so a reader of this seam is not left to
rediscover it.

## Error handling

| Condition | `internal/sessions` | adapter | wire |
|---|---|---|---|
| id live | `UpdateSettings`' own result | passthrough | as today |
| id dormant, model/effort only | nil after persist | nil | `session_settings_updated` |
| id dormant, no change | nil, registry untouched | nil | `session_settings_updated` |
| id dormant, frame names `yolo`/`permission_mode` | `ErrDormantPostureUnsupported` | `relay.ErrSessionUnknown` | `session.not_found` |
| id in neither half | `ErrSessionNotFound` from both writes | `relay.ErrSessionUnknown` | `session.not_found` |
| id dormant, model not in vocabulary | not reached | `relay.ErrModelNotOffered` | as today |
| id dormant, save fails | wrapped save error, entry rolled back | passthrough | server error |

Errors are returned bare, never wrapped with the caller-supplied id —
`DormantSettingsFor`'s posture verbatim, so a hostile id cannot be reflected into
a log line or a wire frame a consumer builds from the error. The new method takes
no logger and adds no logging.

## Testing strategy

Both seams are unit-testable and the relay handler is already covered against
doubles, so no new e2e (the ticket's own conclusion).

**`internal/sessions/pool_dormant_settings_test.go`** (the read half's tests live
here; the write's belong beside them), built on `helperPoolWarmStart`,
`helperDormantID`, `entryByID` and `liveIDs`:

- AC 1 — a write of model and/or effort into a dormant entry returns nil, is
  visible through `DormantSettingsFor` immediately, and reaches the registry file.
  Asserted on one id beside `UpdateSettings` still refusing that id with
  `ErrSessionNotFound`, the read tests' discipline: a write that quietly widened
  the live method would pass the first assertion alone. A partial update (model
  only) must leave the entry's effort intact.
- AC 2 — after several writes the live set from `liveIDs` is unchanged and
  `Lookup(target)` is still `ErrSessionNotFound`. The live-set comparison is what
  carries the claim; "still dormant" alone would pass under a write that
  materialised a *second* session.
- AC 3 — write, then `Revive` + `Activate`, then assert the recorded argv carries
  the written values. The fixture stores an exact model id so the assertion pins
  #2447's rewrite (`--model` names the family alias), which a bare `"opus"` would
  not distinguish.
- AC 5 — a table over `yolo:true`, `yolo:false`, `permission_mode:"plan"` and
  `permission_mode:"bypassPermissions"`, **each carrying a model and effort
  beside it**: every case returns `ErrDormantPostureUnsupported` and leaves the
  entry's model, effort, yolo and permission_mode byte-identical on disk. Carrying
  the model beside the posture is what makes "persists no field of itself"
  testable rather than assumed.
- Miss — an unknown id, the empty id, and the live bootstrap id all return
  `ErrSessionNotFound` and write nothing. The bootstrap case is the partition
  assertion: a live id is not reachable through the dormant seam.
- No-op — an update whose present fields already equal the entry's leaves the
  registry bytes and mtime untouched, copying
  `TestPool_UpdateSettings_NoOpWritesNothing`'s shape.

**`cmd/pyry`**, in a new test file beside the adapter's existing coverage, with a
pool warm-started from a hand-written registry (the `TestResolveBoundRunSettings_DormantRealPool`
pattern) whose runner factory is `modelListRunner`, so the vocabulary gate has an
armed source:

- AC 1 + AC 4 positive — an offered model plus an effort on a dormant id returns
  nil and is readable back from `DormantSettingsFor`; the live set is unchanged.
- AC 4 negative — a model the bootstrap's retained vocabulary does not offer is
  `relay.ErrModelNotOffered` and persists nothing, proving the gate runs for a
  dormant id rather than being skipped with the live lookup.
- AC 5 at the wire seam — a posture-bearing frame on a dormant id is
  `relay.ErrSessionUnknown` and persists nothing.
- Regression — an id in neither half is still `relay.ErrSessionUnknown` on a pool
  that *does* hold dormant entries (the existing unknown-id assertion runs against
  a cold pool and cannot state this).

RED first: each test is run and watched fail for the right reason before the
production change lands.

## Open questions

1. **Does the posture refusal need its own wire code?** Resolved by the ticket:
   no. Recorded here so the documentation handoff can say what `session.not_found`
   now covers.
2. **Does the adapter need a new pool predicate for the existence probe?**
   Resolved during design: no — `DormantSettingsFor` already answers exactly
   "does this pool hold a dormant entry for id", and reusing it adds no surface.
3. **Anything in `Pool.dormant`'s stated invariant that a value write breaks?**
   Resolved during design: no, once the docstring separates the key set (fixed at
   `New`, only shrinking) from the values (now mutable under an existing key).
4. **Is the `Revive` window (#2492) fixable inside this ticket?** Resolved during
   the security pass: no — see Concurrency above.

Any of these that moves during Phase B is recorded under `## Revisions`.

## Documentation handoff

Owned by the documentation stage; pending, not done here. Verbatim from the
ticket:

- `docs/protocol-mobile.md`, under the `set_session_settings` heading: record that
  a session the daemon holds only as a persisted dormant registry entry — every
  conversation but the bootstrap, between a daemon restart and that session's first
  message — accepts a `model` and `effort` write, which is merged into that entry
  and is what the session is revived under; and that a frame naming `yolo` or
  `permission_mode` for such a session applies nothing and is refused, because a
  revive does not restore a persisted posture (#1487).
- `docs/protocol-mobile.md`, the `session.not_found` row in the error-code table:
  it currently reads "names no live session". Split that — the code now means the
  target names no session this daemon has any record of, **or** names a dormant one
  while the frame carries a posture field.
- `docs/knowledge/features/sessions-package-key-types-pool-updatesettings.md`:
  record the dormant write beside the live one — what it merges, that it
  materialises nothing, and that the posture is excluded by the same structural
  rule `Pool.Revive` and `Pool.DormantSettingsFor` already follow. Its existing
  "This lookup is `p.sessions` only … reviving on a settings write is a distinct,
  deferred design (#2463)" paragraph is the text this ticket supersedes.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No finding on the boundary itself. A `set_session_settings`
  frame crosses untrusted→trusted at two named places, both pre-existing and both
  kept on the dormant path: `handleSetSessionSettings` owns the shape checks
  (`validModel`, `validEffort`, `validPermissionMode`, and the refusal of a frame
  carrying both posture spellings), and `settingsUpdaterAdapter.UpdateSettings`
  owns the exact membership check against the retained published vocabulary. What
  is new is a second write path into `p.dormant`, whose `Model` becomes a revived
  child's `--model`; the design keeps both checks ahead of it rather than
  inheriting only the first. **SHOULD FIX:** `UpdateDormantSettings`' docstring
  must state, as the package overview already states for `Pool.UpdateSettings`,
  that validating untrusted model and effort values is not this method's job and
  that the adapter owns the membership check — the method is exported, so a later
  caller that assumes otherwise is the realistic failure. Phase B; the verifier
  should check it landed.
- **[Trust boundaries]** Observation, not a finding: `retainedModelVocabulary`'s
  docstring claims its `boundSessionID` "comes off a resolved
  conversations.Conversation, never from a caller", which is already untrue at
  this adapter's call site — the id is the payload's. Pre-existing and not
  exploitable (the value is spent on a map lookup and never logged), so it is not
  touched here.
- **[Tokens, secrets, credentials]** No tokens. The security-relevant adjacent
  state is the permission posture, and the design refuses it twice over rather
  than once: `UpdateDormantSettings` returns `ErrDormantPostureUnsupported`
  before any mutation, and the merge is field-by-field over `Model` and `Effort`
  so deleting that guard still could not write a posture. The entry's persisted
  `yolo` / `permission_mode` bytes are preserved verbatim and read by nobody —
  `revivedSettings` and `DormantSettingsFor` both build the posture structurally
  — so a restart stays a revocation point for a phone-granted bypass (#1487,
  ADR 035 as amended by #2448). Traced every route to a written posture: a live id
  never falls through to the dormant write, a dormant id is refused, an unknown id
  misses both.
- **[File operations]** No finding. The write adds no filesystem path and no mode
  decision: it reaches disk only through `saveLocked` → `saveRegistryLocked`,
  which creates a temp file in the registry's own directory, chmods it `0600`,
  fsyncs, and renames — atomic, so a signal mid-write leaves the old file or the
  new one, never a truncated one. No caller-supplied value is concatenated into
  any path; the session id is a map key only.
- **[Subprocess / external command execution]** No finding, with the boundary
  stated honestly rather than credited to the wrong check. A written model reaches
  a revived child's argv through `claudeSettingsArgs` as two separate elements of
  an argv slice handed to `exec.Command` — no `sh -c`, nothing shell-parsed. The
  injection boundary is `validModel`'s closed byte class (≤64 bytes, first byte
  alphanumeric so no value can pose as a flag, no metachar/whitespace/control/
  high byte anywhere), machine-checked by `TestValidModel_ByteSetIsClosed`, and it
  applies to every frame regardless of which half writes. The vocabulary
  membership check is defence in depth, not the injection gate: it is fail-closed
  when no vocabulary is available (`ErrModelVocabularyUnavailable`), but it is
  spent at write time while the argv is composed at revive time, so a menu that
  changes in between cannot retroactively refuse a stored value. That widening
  already exists for a live session's persisted model and is not new here.
  `familyAlias` rewrites the value on the way to `--model` and, per
  `claudeSettingsArgs`' own record, cannot weaken what `validModel` buys.
- **[Cryptographic primitives]** Not applicable, concretely: this path generates
  no randomness, derives no key, and compares no secret. The one comparison is a
  model string against a published menu by plain equality, and it is not secret —
  the outcome is returned to the client explicitly, so a timing channel could
  reveal nothing the reply does not already state. `validateModelVocabulary`
  already excludes both the requested value and every menu value from its
  sentinels so the outcome can be logged without disclosing vocabulary.
- **[Network & I/O]** No finding. No new socket read, so no new size cap: the
  frame's bound and its field shapes are the relay handler's, unchanged. No
  resource-exhaustion vector is added — the write replaces a value under an
  existing key, so the registry cannot gain entries, and repeated writes rewrite
  one file, which the no-op short-circuit reduces to zero for an unchanged value.
  A client could already drive the same rewrites through the live path, where they
  additionally restart a child, so this path is strictly cheaper than one already
  exposed. Rate-limiting remains deferred, as `docs/protocol-mobile.md` § Security
  model threat 7 records.
- **[Error messages, logs, telemetry]** **SHOULD FIX (ordering constraint):** the
  adapter's existence probe must stay *ahead* of `retainedModelVocabulary`, as the
  live probe is today. That ordering is what keeps an unknown id from probing
  whether the bootstrap vocabulary is complete — the property the adapter's
  existing comment names — and reordering the dormant probe after the vocabulary
  read would silently drop it. Otherwise no finding: the new method takes no
  logger and adds none, returns bare sentinels, and never wraps the caller-supplied
  id into an error (`DormantSettingsFor`'s posture). On the oracle question: a
  paired client can now distinguish a dormant id from an unknown one by sending a
  model-only frame and seeing it succeed. That is not a new capability — #2449's
  read half already answers the same question for the same client, which holds its
  own channels' session ids anyway — and the posture refusal collapsing into
  `session.not_found` discloses less than a distinct code would, not more.
- **[Concurrency]** **OUT OF SCOPE — filed as #2492.** `Pool.Revive` evaluates
  `p.revivedSettings(id)` as an argument to `materialise`, releasing its RLock
  before `materialise` takes the write lock and retires the entry; a write landing
  in that gap is persisted and then dropped. Latent until now (nothing could write
  into `p.dormant`), reachable from this ticket on, low severity and
  self-correcting, and the fix changes `Revive`/`materialise` rather than this
  seam. The two windows this ticket does own are analysed under Concurrency model
  above and are bounded by `p.dormant` only ever shrinking. Within the new method
  there is no check-then-mutate gap at all: the lookup, the merge and the save all
  run under one held write lock. One lock, taken once, no new lock-order edge, no
  goroutine spawned.
- **[Threat model alignment]** `docs/protocol-mobile.md` § Security model, threat 1
  (prompt injection, partial) is unchanged — a model selection is not prompt
  content, and the argv sink is bounded above. Threat 5 (implementation bugs,
  defense-in-depth) is where this design's two-barrier posture exclusion and its
  layered model validation sit. Threat 7 (DoS) is unchanged and explicitly still
  deferred. The restart-as-revocation-point property is not in that list — it is
  ADR 035 / #1487 — and is the decision this plan turns on; the documentation
  handoff records what `session.not_found` now covers so a client author is not
  left to infer it.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-16
