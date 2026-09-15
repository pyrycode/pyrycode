# #2450 — persist the last model list so the daemon-wide vocabulary survives a restart

## Files read

- `cmd/pyry/session_model_list.go` → `retainedModelVocabulary`, `resolveBoundModelList`,
  `sessionRetainedModelList`, `modelListFor`, `retainedModelLists` — the whole read side.
  `retainedModelVocabulary`'s docstring carries the `RequestInitializeOnSpawn`
  premise this ticket removes.
- `cmd/pyry/session_model_hold.go` → `sessionModelHold`, `cloneModelList`,
  `newSessionParser`, `sessionRetentions` — where the retention lands, the deep copy
  to reuse, and the hold chain the persist decorator sits beside.
- `cmd/pyry/streamsup_runner.go` → `newStreamRunnerFactory` — the chain the decorator
  is spliced into, and the only place the parser's sink is assembled.
- `cmd/pyry/session_reset_follow.go` → `sessionResetFollower`, `newSessionResetFollower` —
  the precedent for a non-retaining decorator chained inside the factory rather than
  threaded through `newSessionParser`.
- `cmd/pyry/stream_context_usage.go` → `newTurnEndContextUsageRequester` — the same
  precedent, in its smallest form.
- `cmd/pyry/main.go` → `resolveRegistryPath`, `resolveInstanceDirPath`,
  `resolveConversationsRegistryPath` — the sibling path resolvers the new one joins;
  `selectInteractiveRunner` — where the factory is built; `settingsUpdaterAdapter`,
  `validateModelVocabulary` — the third consumer of the vocabulary.
- `internal/conversations/registry.go` → `Registry.Save` — the atomic-write recipe
  (`MkdirAll` 0700, `CreateTemp` in the destination dir, chmod 0600, encode, sync,
  close, rename) this file copies.
- `internal/turnevent/event.go` → `ModelList`, `ModelOption` — the value to round-trip,
  its never-empty contract, its three bounded dimensions, and the nil-vs-`[]`
  normalisation for `EffortLevels` / `TruncatedFields`.
- `internal/streamsup/parser.go` → `maxModelListEntries` (10), `maxModelResolved`,
  `maxModelValue`, `maxModelDisplayName` (256 each), `maxModelEffortLevel` (32),
  `maxModelEffortLevelCount` (8) — the producer-side bounds, all unexported, which is
  what decides the security review's re-bound question.
- `internal/relay/v2session_settings.go` → `validModel`, `validEffort` — the inbound
  shape gates that stay in force regardless of what the restored vocabulary says.
- `CODING-STYLE.md` § Persistent data conventions — temp file in the destination
  directory, then sync, close, rename.

## Context

The `model_list` frame is built from claude's `initialize` reply, retained per child on
`sessionModelHold`, and answered by `retainedModelVocabulary` from the bound session's
hold or the bootstrap's. #2085 removed the spawn-at-daemon-start that made the second
source non-empty on a cold daemon, so `retainedModelVocabulary`'s docstring now
documents a premise the code no longer has: a restarted daemon has no vocabulary
anywhere until a turn runs in the conversation bound to the bootstrap session, and
since every minted conversation gets its own session that can be never.

This ticket adds a third source — one file per daemon instance, `model_list.json`
beside `sessions.json` — written whenever a child's `initialize` reply lands, read once
at start. The vocabulary is a property of the machine and account rather than of a
conversation, which is what makes one daemon-wide file the right grain and what #2124
already relied on when it let one conversation read another child's menu.

No ADR is warranted: this is #2084's second shape landed behind #2124's first, inside
an existing boundary, and `retainedModelVocabulary`'s own docstring is where the rule
lives.

## Design

### The store — `cmd/pyry/model_vocabulary_store.go` (new)

One unexported type, `modelVocabularyStore`, built at the composition root from the
resolved path. It owns three things and nothing else: the restored/latest value in
memory, the file, and the sink decorator that feeds it.

```go
func newModelVocabularyStore(path string) *modelVocabularyStore   // no I/O
func (s *modelVocabularyStore) Load()                             // once, at start
func (s *modelVocabularyStore) ModelList() (turnevent.ModelList, bool)
func (s *modelVocabularyStore) Retain(list turnevent.ModelList)   // never blocks/fails
func (s *modelVocabularyStore) sinkFor(next func(turnevent.Event)) func(turnevent.Event)
func (s *modelVocabularyStore) Close()                            // join the writer
```

`ModelList` answers the same comma-ok shape `sessionModelHold.ModelList` does, and
returns a `cloneModelList` deep copy for the same reason: the value lives for the
process's life and is read repeatedly by different consumers. That shared method set is
deliberate — it is the one-method interface `sessionRetainedModelList` already asserts
for, so the third source is the same contract as the first two rather than a new one.

`sinkFor` is `streamTurnSink.sinkFor`'s shape — a per-daemon object minting a
per-runner closure — and `sessionModelHold.Sink`'s behaviour: act on
`turnevent.ModelList`, forward every event of every variant unchanged. It retains
nothing of its own, so it is not a fifth `sessionRetentions` member; `sessionResetFollower`'s
doc already supplies that argument and this is the same case one more time.

Ingest clones. `sessionModelHold.Sink` stores what it is handed without copying,
documented as taking sole ownership; with a second retainer on the same chain that
sentence is no longer true of either, so this store takes its own copy and the claim
stays true of both. Once stored the value is replaced by assignment and never mutated,
so the writer goroutine may snapshot the struct header under the lock and marshal
outside it.

### The write — asynchronous, single-flight, coalescing

AC1 forbids the write from failing, blocking or delaying the event path, and the event
path is claude's stdout forwarder goroutine. So `Retain` does the memory update under
the leaf mutex, marks the value dirty, and — only if no writer is already running —
starts one goroutine that drains until clean:

- At most one writer goroutine exists at a time (`writing` under the mutex).
- It terminates on its own when nothing is dirty; that is its shutdown path, and it
  needs no context because its work is one bounded file write.
- Coalescing means a burst of spawns costs one write per drain rather than one per
  event, and the value written is always the newest.
- Every error from the write is discarded. Nothing is logged — see § Error handling.

`Close` sets a closed flag under the mutex and joins the in-flight writer through a
`sync.WaitGroup`. It is the daemon's shutdown path for the goroutine and the
deterministic join point tests use instead of polling for the file.

`Retain` never holds the mutex across the goroutine start or across any I/O; the mutex
participates in no ordering with `Pool.mu`, `Session.lcMu` or `capMu`, exactly as
`sessionModelHold`'s does.

### The file

`~/.pyry/<sanitized-name>/model_list.json`, resolved by `resolveModelVocabularyPath` in
`main.go` beside `resolveRegistryPath` and `resolveConversationsRegistryPath`, with
their `$HOME`-unresolvable fallback contract.

Its own record types, snake_case tags, holding the daemon-wide list and nothing else —
no conversation id, no session id, no timestamp:

```
{ "models": [ { "resolved_model", "value", "display_name",
                "effort_levels"?, "supports_auto_mode"?, "truncated_fields"? } ],
  "dropped_models"? }
```

`DroppedModels` and each entry's `TruncatedFields` round-trip, because they are how a
client learns the list it holds is a cut one; a restore that dropped either would turn a
truncated list into one that reads as complete. Explicit record types rather than
marshalling `turnevent.ModelList` directly: the on-disk format is a contract of its own
and must not follow an internal type's field renames.

`EffortLevels` and `TruncatedFields` are normalised on decode — length zero becomes nil —
because `turnevent` spells "nothing to report" as nil and a `[]` in the file would
otherwise reach a consumer as an empty non-nil slice the producer never emits.

The write is `Registry.Save`'s recipe: `MkdirAll` 0700 (the instance dir is created by
the pool's registry save, which need not have run by the first write —
`writeMCPSettings` documents this), `CreateTemp` in the destination directory with a
dotted `.model-list-*.json.tmp` pattern, chmod 0600, encode, sync, close, rename.

### The read — three sources, one ordering

`retainedModelVocabulary` gains the third source at the tail. Its second parameter
changes from `pool *sessions.Pool` to a small value carrying both sources:

```go
type modelVocabularySources struct {
    pool  *sessions.Pool
    saved interface{ ModelList() (turnevent.ModelList, bool) }  // nil ⇒ two sources
}
```

and `resolveBoundModelList`, `modelListFor` and `retainedModelLists` take it in the
pool's place. Threading a fourth parameter instead would have rewritten 35 call sites
for no behaviour gained; substituting the type leaves every existing call site
byte-identical and changes only where the value is built. The parameter is also more
honest than it was: these functions resolve a menu from the daemon's vocabulary
sources, of which the pool is one.

Ordering is unchanged where it exists and appended where it does not: bound session's
hold → bootstrap's hold → the store. A live child's own report still wins, for the
reason already stated in `retainedModelVocabulary` — a child that has answered
`initialize` is the authority on what it will accept, and every fallback is a stand-in
for silence rather than a second opinion. The file is last for the same reason applied
once more: a hold is this process's observation, the file is a previous process's.

The `saved` source is reached through the same one-method interface
`sessionRetainedModelList` asserts for, so a nil store is simply a daemon with two
sources and no arm has to special-case it.

`settingsUpdaterAdapter` carries the sources instead of the bare pool, so
`UpdateSettings`' membership gate and its `validateModelVocabulary` call read the same
three sources every other consumer does.

`retainedModelVocabulary`'s docstring is corrected in the same change: the paragraph
beginning "The daemon-wide copy exists because `RequestInitializeOnSpawn` is true for
every child" states the premise #2085 removed, and it becomes the three-source rule with
the cold-daemon case named.

### Wiring

`newStreamRunnerFactory` takes the store as a fourth parameter and splices
`vocab.sinkFor(contextUsage.Sink)` between the context-usage requester and
`newSessionParser`. Position within the chain is immaterial — what puts a link upstream
of the fan-in send is sitting on the parser's side of the channel, which the chain's own
doc states — and the tail is where the two existing non-retaining decorators already
sit. `selectInteractiveRunner` forwards it. A nil store makes the decorator a plain
forwarder, which is what every test call site that does not care will pass.

`runSupervisor` builds the store beside the other path resolvers, calls `Load` before
the pool is constructed (AC5: once, at start, never on the request path), defers
`Close`, and hands it to `selectInteractiveRunner` and to the three read-side wirings.

## Concurrency model

- `Retain` runs on claude's stdout forwarder goroutine, one per live child. The leaf
  mutex is held for the field writes only, never across the goroutine start and never
  across I/O.
- At most one writer goroutine per store, started by `Retain` and self-terminating when
  the value is clean. `Close` joins it and refuses to start another.
- `ModelList` runs on relay-leg and control goroutines; it takes the same leaf mutex and
  copies out.
- `Load` runs on the composition root's goroutine before any reader or writer exists.
- Lock order unchanged: this mutex is a leaf and nests inside nothing. `retainedModelVocabulary`
  acquires the pool's locks, the holds' and now this one **sequentially**, never nested,
  so it still adds no edge to the daemon's lock order.
- A process killed mid-write leaves the temp file and the previous `model_list.json`
  intact; rename is the commit point.

## Error handling

- **Write.** Every failure is discarded — no return value, no log, no retry. A daemon
  that cannot write this file keeps serving live vocabularies exactly as today.
- **Load.** Absent, unreadable, oversized, undecodable, or carrying zero models — all
  answer "no list" and the daemon starts. `os.IsNotExist` is not special-cased; it is one
  of the five, and the fifth exists because `ModelList.Models` is documented never empty,
  so a list with no models is not a value any reader may be handed.
- **No logging anywhere on this path, at any level** (AC4). `sessionModelHold` enforces
  this by construction — no logger field, no constructor parameter — and this store does
  the same. That is not only the #833 model/effort posture: a decode error from
  `encoding/json` quotes the offending input into its text, so logging a load failure
  would put file bytes into a record. The store takes no `*slog.Logger` and must not grow
  one.

## Testing strategy

`cmd/pyry/model_vocabulary_store_test.go` (new):

- Round trip: a list with `DroppedModels` > 0, an entry with `TruncatedFields`, an entry
  with `EffortLevels`, and an entry with neither survives `Retain` → `Close` → a fresh
  store's `Load` → `ModelList` with every field equal.
- `EffortLevels`/`TruncatedFields` written as `[]` in the file decode to nil.
- `Load` answers "no list" for: absent file, a directory in its place / unreadable mode,
  malformed JSON, a file past the size cap, and `{"models":[]}`. The daemon still starts —
  asserted as `Load` returning without panicking and `ModelList` reporting false.
- The written file is mode 0600 and its parent directory is created 0700 when absent.
- No temp file survives a successful write.
- `ModelList` hands back a deep copy: mutating the returned `Models`, `EffortLevels` and
  `TruncatedFields` does not disturb a second caller's copy.
- `sinkFor` forwards every variant unchanged and retains only `ModelList`; a nil `next`
  forwards nothing.
- A nil-receiver `ModelList` answers the unreported state, matching `sessionModelHold`.
- Coalescing: several `Retain` calls then `Close` leave the file holding the last value.
- `-race`: concurrent `Retain` and `ModelList` from several goroutines.

`cmd/pyry/session_model_list_test.go` (extended):

- The third source answers when neither hold does, through `resolveBoundModelList`,
  `retainedModelLists` and `modelListFor` — a daemon that spawned nothing.
- A bound session's own list wins over the store; the bootstrap's hold wins over the
  store. Order asserted with three distinguishable sentinel lists.
- Nothing retained anywhere and no store ⇒ refusal, unchanged.
- `DroppedModels` and `TruncatedFields` survive from the store into the payload.
- The existing "logs nothing" tests extend to cover a store-answered call.

`cmd/pyry/main_test`-side: `resolveModelVocabularyPath` lands inside the instance dir
beside `sessions.json`, using `args_test.go`'s existing `assertInsideInstanceDir`.

## Open questions

1. Whether the restored list is re-bounded against streamsup's caps or treated as
   daemon-written — decided in § Security review below.
2. Whether `Close` needs to be called before or after the pool drains at shutdown. It is
   independent of the pool (the store owns no session state), so `defer` order at the
   composition root is the only constraint; resolve in Phase B and record here if it moves.

## Sizing note — stated overage

This plan exceeds two lines of the one-ticket boundary and is built anyway, under the
floor rule: the only seam this work has is "write the file" / "read the file", and the
write half's sole consumer is the read half, so a split would produce a child no sibling
outside the family consumes. The refiner reached the same conclusion on the estimate
line. Recorded so the overage is visible rather than discovered:

- **Consumer call sites: ~27, against a limit of 10.** `newStreamRunnerFactory` (1
  production, 9 test), `selectInteractiveRunner` (2 production, 8 test), and ~7 on the
  read side. The read side is already at its minimum: the `modelVocabularySources`
  substitution above exists precisely to keep 35 further call sites byte-identical.
- **Total written work: ~950 lines, against a ceiling of 800.** In the same band as the
  refiner's named analogues #2449 (+791) and #2448 (+732).

Production source files (4), new exported types (0), acceptance criteria (5) and reject
branches (5) are all inside their lines.

## Documentation handoff

Owned by the documentation stage; **pending**, not done here. The prose below states the
bootstrap-at-start premise this ticket removes and must name the file as the last source.

- `docs/protocol-mobile.md`, § `model_list` — the paragraph beginning "What the snapshot
  covers, and the one case it does not", which says a menu is "drawn from the daemon-wide
  copy the daemon's first child retained at startup" and gives "no first child in the
  pool, or its `initialize` reply not yet arrived" as the one uncovered case. Both halves
  change.
- `docs/protocol-mobile.md`, § Reconnect / Backfill semantics — the "Reconcile on connect"
  bullet's `model_list` clause.
- `docs/knowledge/features/v2-session-manager-state-machine-connect-time-model-list-reconcile-retain.md`
  — the "The daemon-wide fallback (#2124)" paragraph, which describes
  `retainedModelVocabulary` as a two-source read.

Observable requirement: a reader of either document can tell, without reading code, that
a restarted daemon that has spawned nothing answers from the persisted file, and that
"nothing retained anywhere" now means the file is absent too.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries] Decided — the file is treated as daemon-written, not re-bounded.**
  This is Open Question 1. The design adds one new boundary, file → memory, and it is a
  single explicit one: `modelVocabularyStore.Load` is the only decoder and the only place
  the file's bytes become a `turnevent.ModelList`. Every string in it is claude-authored
  and already crossed the subprocess boundary once, where streamsup's `maxModelListEntries`,
  `maxModelResolved`, `maxModelValue`, `maxModelDisplayName`, `maxModelEffortLevel` and
  `maxModelEffortLevelCount` bounded all three of the list's dimensions before the daemon
  wrote it. `Load` does **not** re-apply those caps, for two reasons: they are unexported
  in `internal/streamsup`, so re-applying them means either exporting six constants or
  restating them in `cmd/pyry`, and a second spelling of a bound is a second place it can
  disagree — the argument `resolveBoundModelList` already makes about its own refusal
  rules. What `Load` applies instead is one **structural aggregate bound** that needs no
  knowledge of the producer's per-dimension numbers: a 64 KiB read cap. Derivation: the
  producer's worst case is `maxModelListEntries` × (256 + 256 + 256 + 8 × 32) = 10 × 1024 ≈
  10 KiB of claude-derived text, so 64 KiB is roughly six times the largest list the
  producer can emit, covers JSON overhead, and keeps a restored frame the same order of
  magnitude as a live one. Plus the never-empty refusal, which is a contract check rather
  than a bound. **Residual risk, stated rather than hidden:** a hand-edited file within
  64 KiB can carry more entries, or longer strings, than any live child could. That value
  reaches a client as inert text whose sanitisation is already the client's
  responsibility (`turnevent.ModelOption.DisplayName` says so), and reaches nothing else
  unbounded — see the next two findings.
- **[Subprocess] No finding — a poisoned list cannot reach the argv as a flag.** The one
  path from the vocabulary to `exec.Command` is `settingsUpdaterAdapter.UpdateSettings`,
  where the vocabulary is a **membership** gate only: the inbound value has already passed
  `internal/relay`'s `validModel`, whose first-byte rule (`modelAlnumByte`) its own doc
  calls "the whole of the bar that keeps a leading-dash value from posing as a claude flag
  on the argv sink", plus a 64-byte cap and a closed byte class. Shape is gated
  independently of membership, so widening the menu cannot widen the argv. The real
  change this ticket makes here is one of **lifetime**, not of shape: a menu entry claude
  authored now outlives the process that heard it. It is the same value with a longer
  life, it still admits nothing `validModel` refuses, and an actor who can author it
  already controls the supervised child.
- **[File operations] No finding on traversal, TOCTOU, permissions or atomicity.** The
  path is built by `resolveModelVocabularyPath` from the operator's own `-pyry-name`
  through `sanitizeName`, exactly as `resolveRegistryPath` and
  `resolveConversationsRegistryPath` build their siblings; no remote input reaches it and
  nothing is joined onto it. `Load` is a single open with no stat-then-use gap. The write
  is `Registry.Save`'s recipe — `MkdirAll` 0700, `CreateTemp` in the destination
  directory, chmod 0600, encode, sync, close, rename, with `defer os.Remove` on the temp —
  so an interrupted write leaves the previous file intact and a SIGKILL leaves only a
  dotted `.model-list-*.json.tmp` scratch that cannot be mistaken for the real name.
- **[File operations] OUT OF SCOPE — symlink and FIFO substitution at the read path.**
  `Load` uses a plain open, so a symlink planted at `model_list.json` is followed, and a
  FIFO there would block daemon start. Not fixed here, and not fixed with a one-file
  divergence: `sessions.json` and `conversations.json` are read the same way at the same
  point in startup, so an actor able to plant a FIFO in `~/.pyry/<name>/` (mode 0700,
  owned by the operator) can already hang the daemon through either, and can also replace
  the `pyry` binary. If it is ever worth closing it is worth closing for all three at
  once, in a ticket that owns that decision; this one would only add an inconsistency.
- **[Network & I/O] SHOULD FIX — write amplification from a repeating child.** One
  `initialize` exchange per child produces one `ModelList`, but nothing in the daemon
  forbids a buggy or hostile child from emitting many, and every one currently schedules
  an `fsync` + `rename`. The single-flight coalescing writer already bounds this to one
  write in flight and one value per drain, and the event path never blocks on it, so this
  is disk churn rather than an availability break — and a hostile child running as the
  operator can churn the disk directly anyway. Still cheap to close: in Phase B, `Retain`'s
  writer compares the **encoded bytes** against the last bytes it successfully wrote and
  skips the write when they are equal. That is computed on the way to the write regardless,
  it makes the common case (a respawn re-reporting the same menu) cost zero writes, and it
  needs no rate limiter or clock. The verifier should check it landed.
- **[Errors, logs, telemetry] No finding — enforced by construction, not by care.** The
  store has no `*slog.Logger` field and its constructor takes none, so AC4 ("no log line
  anywhere on this path carries a model value, at any level") cannot be violated without a
  structural change a reviewer would see. `sessionModelHold` takes the same posture for the
  same #833 reason. The load path has a second, independent reason to stay silent that is
  worth writing down: `encoding/json` quotes the offending input into its error text, so a
  "could not decode model_list.json" line would put file bytes — claude-authored strings —
  into a log record. Write errors are discarded without a record for the same reason.
- **[Concurrency] No finding.** The store's mutex is a leaf that nests inside nothing and
  is never held across the goroutine start or across I/O, so `retainedModelVocabulary`
  still acquires every lock it touches sequentially and adds no edge to the daemon's lock
  order. The `dirty`/`writing` pair is read and written inside one critical section, so
  there is no check-then-mutate gap. At most one writer goroutine exists per store; it
  self-terminates when the value is clean, `Close` joins it through a `sync.WaitGroup`,
  and the `closed` flag is set under the same mutex every `wg.Add` is taken under — so an
  `Add` can never race the `Wait`. A process killed mid-write is recoverable by
  construction: rename is the commit point.
- **[Threat model alignment] No finding against `docs/protocol-mobile.md` § Security
  model.** The cross-conversation isolation threat is the relevant one, and this ticket
  does not touch its enforcement point: `resolveBoundModelList`'s registry lookup stays the
  whole security boundary, and the vocabulary-only exception #2124 documented gains one
  more source of the same kind of data rather than a new kind. The store is daemon-wide
  and carries no conversation id, session id or timestamp (AC5), so there is nothing in it
  to attribute to a conversation even by accident. Every path that reads it is
  post-handshake and post-token-validation, and unicast to the conn that asked.
- **[Tokens, secrets, credentials] Not applicable — no credential material is created,
  stored or compared.** The file holds model identifiers and display labels, which are not
  secrets; it is written 0600 in a 0700 directory regardless, matching its siblings.
- **[Cryptographic primitives] Not applicable — the design introduces no randomness, key
  material or comparison against a secret.** `os.CreateTemp`'s name randomness serves
  collision avoidance, not secrecy.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-15
