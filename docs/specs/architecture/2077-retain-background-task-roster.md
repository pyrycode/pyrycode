# #2077 — Retain the latest background-task roster for the session

Third application of the per-session sink-decorator retention: keep the newest
`turnevent.BackgroundTaskRoster` each session reports for that session's life, and expose
the read off the session's `streamRunner` so #2079's type assertion has a shape to find.

## Files read

- `cmd/pyry/session_slash_command_hold.go` → `sessionSlashCommandHold`, `newSessionSlashCommandHold`,
  `Sink`, `SlashCommandList`, `cloneSlashCommandList` — the direct twin. Its type doc states the
  placement, lifetime and concurrency arguments in full; this plan consumes them rather than
  re-deriving them.
- `cmd/pyry/session_model_hold.go` → `sessionModelHold`, `newSessionParser` — the chain the third
  link joins, and the one production caller of `streamsup.NewParser`.
- `cmd/pyry/streamsup_runner.go` → `streamRunner`, `ModelList`, `SlashCommandList`,
  `newStreamRunnerFactory` — the struct that carries the holds, the two forwarders this one
  mirrors, and the sole production `newSessionParser` call site.
- `cmd/pyry/streamsup_runner_test.go` → `TestStreamRunnerFactory_Construct`,
  `TestNewSessionParser_DecodesAndRetains`, `TestNewSessionParser_DecodesAndRetainsSlashCommands`,
  `TestSessionParser_MintsOneStablePostureGate`, and the two `var _ interface{...}` assertion
  blocks — the four `newSessionParser` call sites that move with the signature, plus the
  compile-time-shape pair this ticket adds a third member to.
- `cmd/pyry/session_slash_command_hold_test.go` → `holdSlashCommandListFixture` and its nine tests —
  the test shape to mirror, including the fixture-name-collision rule (`cmd/pyry` is one package
  across many files, so a package-level fixture builder must be prefixed by its consumer).
- `cmd/pyry/stream_turn_busy.go` → `turnMarkFor` — confirms `BackgroundTaskRoster` falls to the
  `default` arm and answers `turnMarkNone`, i.e. it is droppable class at `sinkFor`'s
  `droppableCap`. That is what makes AC 1's "still retains it under refusal" clause testable.
- `internal/turnevent/event.go` → `BackgroundTaskRoster`, `BackgroundTask` — the retained type and
  its element type. `Tasks` is nil for an empty roster; `DroppedTasks` is the roster's only
  truncation report; each entry has exactly one `[]string` (`TruncatedFields`) and no second one.
- `internal/streamsup/parser.go` → `emitBackgroundTaskRoster`, `maxTaskRosterEntries` (8),
  `maxTaskFieldID` (256), `maxTaskRosterDescription` (512), `defaultMaxParseBuf` (4 MiB),
  `truncateField` — the producer, its refusal paragraph, and the bounds the security review's
  footprint claim rests on.
- `docs/knowledge/features/streamsup-package-retaining-the-decoded-model-list-for-the-session.md` —
  the family's lesson home, and it changes how this ticket is built in three ways:
  - The mutex is for **one writer and many readers**, never for two overlapping writers;
    `cmd.Wait`'s join of the stdout-copy goroutine rules the second pair out. The corrected
    justification is written on the new type rather than the falsified one copied across.
  - A `strings.ToValidUTF8(s[:limit], "")` "capped" string is a slice of the decoded field, so
    #2004's security review understated its own retained footprint by ~25x. This plan's
    § Security review states the parse-line bound instead of the cap product.
  - Store-before-forward inside `Sink` is **not** what keeps the retention off the droppable send;
    being a decorator upstream of the channel is. A saturated-sink test pins the latter and no
    finer, so its comment must not claim the former.
- `docs/knowledge/features/v2-session-manager-state-machine-connect-time-slash-command-list-reconcile-retain.md`
  — where this retention is eventually consumed, and the source of the "a twin's stated *reason*
  does not transfer just because its conclusion does" rule this ticket applies twice.
- `CODING-STYLE.md`, `docs/PROJECT-MEMORY.md`, `docs/knowledge/architecture/system-overview.md`.

## Context

`background_task_roster` is pure pass-through today: `emitBackgroundTaskRoster` decodes claude's
`system/background_tasks_changed` line into one `turnevent.BackgroundTaskRoster`, `turnbridge`'s
`MapEvent` maps it onto the wire type, and it is pushed to whatever interactive connections exist at
that instant. Nothing retains it, so a client that connects mid-run learns nothing about background
tasks until claude next changes the roster — which may never happen while the work is running.

This is the **retention half only**, plus the runner-level read that makes the retention reachable.
The wire publication is #2079's, and the connect-time reconcile beyond it belongs to the Mode B
umbrella (#829).

No ADR is warranted: this applies an existing, twice-shipped pattern to a third variant. The two
places where this variant genuinely differs from the twin are recorded in § Design, and belong in the
package overview the documentation phase owns — not in a decision record.

### Size

The ticket's own estimate is ~920 lines of total written work, ~1.15x the 800-line size-S line, and
it states the overage rather than splitting. Re-checked against this written plan and I agree,
because the **floor** rules the split out: a hold with no chain and no reader changes nothing
observable, and the forwarder alone has exactly one consumer (#2079), so either cut yields a slice
that cannot be verified on its own. Where the floor and the ceiling disagree the floor wins. The
other five boundaries hold comfortably: **4 production files** (the new hold, `session_model_hold.go`
for the chain, `streamsup_runner.go` for the field and the forwarder, and a comment-only re-scoping
in `internal/streamsup/parser.go`), **0 new exported names** (every symbol here is package-private),
**4 call sites** moving with `newSessionParser`'s signature, **5 acceptance criteria**, **no state
machine and no reject branches**. #2004 is the same shape at ~1000 lines and landed clean as one
ticket.

## Design

### `sessionBackgroundTaskHold` — `cmd/pyry/session_background_task_hold.go` (new)

A sibling of the two existing holds, not a second retention inside either. Package-private, no
exported surface.

```go
type sessionBackgroundTaskHold struct {
    mu     sync.Mutex
    roster turnevent.BackgroundTaskRoster
    have   bool
    next   func(turnevent.Event)
}

func newSessionBackgroundTaskHold(next func(turnevent.Event)) *sessionBackgroundTaskHold
func (h *sessionBackgroundTaskHold) Sink(ev turnevent.Event)
func (h *sessionBackgroundTaskHold) BackgroundTaskRoster() (turnevent.BackgroundTaskRoster, bool)
func cloneBackgroundTaskRoster(r turnevent.BackgroundTaskRoster) turnevent.BackgroundTaskRoster
```

- `Sink` type-asserts for `turnevent.BackgroundTaskRoster`, stores it under the leaf mutex replacing
  any prior value, releases the lock, then forwards **every** event of **every** variant unchanged to
  `next` — this variant included (AC 4). Stored without copying: `emitBackgroundTaskRoster` allocates
  its `tasks` slice and each entry's `TruncatedFields` fresh per emit from a function-local decode
  target, so the hold takes sole ownership of what it is handed; the copy is made on the read side.
- `BackgroundTaskRoster()` returns a deep clone plus `have`. Nil-receiver-safe: a runner whose hold
  was never minted answers the unreported state rather than panicking (AC 5, second half).
- `cloneBackgroundTaskRoster` is **two levels**: `slices.Clone` the `Tasks` slice, and `slices.Clone`
  each entry's `TruncatedFields`. `DroppedTasks` is an `int` and rides the `out := r` assignment.
  `BackgroundTask` has no second inner slice, so the twin's third level does not exist here — the
  shape to follow is `cloneSlashCommandList`, not its depth.

Three properties are argued on the new type rather than inherited, because the twin's stated reason
does not transfer:

1. **The `have` bool is load-bearing here, where in both siblings it is merely the only spelling.**
   `emitBackgroundTaskRoster` emits for an absent or empty `tasks` array on purpose — an empty roster
   positively says nothing is alive. So `Tasks == nil` is a **reachable reported state**, and a
   reader that tested `len(Tasks) == 0` for "unreported" would be wrong, where in the two siblings it
   would merely be redundant (`ModelList.Models` is documented never empty; `emitSlashCommandList`
   returns early on a zero-length list, #1877). AC 2 is exactly this distinction.
2. **`DroppedTasks` travels with the roster.** It is the roster's only truncation report — this
   variant has no top-level `TruncatedFields` at all — so a retained roster that lost the count would
   let a capped roster be read as a whole one.
3. **Replace, never diff.** `turnevent.BackgroundTaskRoster`'s type doc reserves snapshot-diffing for
   a consumer on its own terms and refuses it as a daemon inference, because a task's disappearance
   has never been observed. This is the first place in the daemon that holds a previous roster and a
   newer one at the same instant, so the constraint is stated on `Sink` rather than assumed: store
   the newer value, derive nothing from the pair.

**No logger.** No `*slog.Logger` field, and the constructor takes none — the #833 posture enforced by
construction. Sharper here than on either sibling: an entry's `Description` is, for claude's
`local_bash` task type, the literal command line, and the producer's own drop path already refuses to
log even the entry *count*.

### The chain — `cmd/pyry/session_model_hold.go`

`newSessionParser` grows a third link and a third return value:

```go
func newSessionParser(next func(turnevent.Event), logger *slog.Logger) (
    *streamsup.Parser, *sessionModelHold, *sessionSlashCommandHold, *sessionBackgroundTaskHold)
```

Statements stay innermost-first (each link needs the one it forwards to); the event travels the other
way, parser → models → commands → tasks → `next`. The order carries no meaning: every link stores
unconditionally and forwards unconditionally, and what puts each retention upstream of the droppable
fan-in send is being on the parser's side of the channel at all. Its doc is updated from "the two
holds" to three, and from a two-link walk to a three-link one.

Returning a struct instead of a fourth value was considered and rejected: it would rewrite two
siblings' call sites for no behaviour change, and three returns is not yet the count that argues for
it. Noted so a fourth hold's author does not have to re-derive the trade.

### The runner — `cmd/pyry/streamsup_runner.go`

- `streamRunner` gains `tasks *sessionBackgroundTaskHold`, a pointer, so the adapter stays a value
  type and the existing `var _ sessions.Runner` assertion is untouched.
- `newStreamRunnerFactory` destructures the fourth return and passes it into the returned
  `streamRunner`.
- `BackgroundTaskRoster() (turnevent.BackgroundTaskRoster, bool)` forwards to the hold — the **sixth**
  concrete method **off** the un-widened `sessions.Runner` interface (#1077), after `Interrupt`,
  `RestartFresh`, `BeginRotation`, `ModelList` and `SlashCommandList`. Off for their stated rule, not
  by resemblance: the interface carries a method when its consumer sits inside `internal/sessions`,
  where a structural assertion would fail open. This consumer is #2079's resolver in `cmd/pyry`, so
  widening buys no compile-time guarantee and would drag every fake runner under `internal/sessions`
  and `cmd/pyry` into the diff. A compile-time assertion states the shape instead (§ Testing).

### Comment-only re-scoping — `internal/streamsup/parser.go`

`emitBackgroundTaskRoster`'s refusal paragraph makes "the parser holding no ROSTER and no per-task
memory" the enforcement mechanism for the family's no-synthesized-finish rule. That claim stays
**true** — the parser still holds neither — but its final clause ("detecting a task's disappearance
would require remembering the previous roster") reads as a daemon-wide impossibility argument, and
after this ticket a roster does outlive its line one layer up. A dated addendum in the file's own
`CORRECTED …` style re-scopes it: the retention is in `cmd/pyry`, it replaces rather than diffs, and
it holds one roster rather than a previous-and-current pair. The claim is narrowed, not withdrawn.

`turnevent.BackgroundTaskRoster`'s type doc is re-read and **needs no edit**: it is scoped to the
event family, not to the parser, and its "diffing successive snapshots is a legitimate thing for a
CONSUMER to do on its own terms — it is not the daemon's inference to make" survives verbatim,
because the hold makes no inference.

### Declared sibling correction (out of the ticket's own scope, in-scope by the package overview)

The package overview records two shipped comments citing `#1867` as "the shape the publisher reaches
by type assertion", where the actual assertion site (`resolveBoundModelList`) shipped in `#1857`, and
nominates the next editor of those files to fix them. This ticket is that editor for both:
`sessionModelHold.Sink`'s doc and the first `var _ interface{...}` comment in
`streamsup_runner_test.go`. Two tokens, comment-only, declared here so it is not an undeclared edit
the way #2004's was.

## Concurrency model

No goroutine is created, so there is no shutdown path to own. The mutex is a **leaf lock**, never held
across the call to `next`: holding it across a channel send would add an edge to the daemon's lock
order for no benefit, and as written it participates in no ordering with `Pool.mu`, `Session.lcMu` or
`capMu`.

The pair the lock exists for is **one writer, many readers** — not two writers. Writes are serial
across every respawn because `spawnAndWait` blocks on `cmd.Wait`, which `os/exec` documents as joining
the goroutine copying the child's stdout into a non-`*os.File` `Stdout`, so forwarder N+1 cannot start
until forwarder N has finished; `streamsup.Parser`'s own doc asserts that serialisation. What the lock
protects against is #2079's resolver reading on a relay-leg goroutine while that one writer runs.

Lifetime is the session's, with no registry and no removal hook: the hold is a field of the
per-session `streamRunner`, whose `Session.Runner` is assigned once at construction and never
reassigned. A daemon-global session-keyed map would outlive every session it keyed with nothing to
prune it.

One difference from both siblings worth stating: their producer fires **once per child** (one
`initialize` reply), where `emitBackgroundTaskRoster` fires **every time claude changes its roster**,
arbitrarily often. Replacement is therefore what keeps the retained footprint at one roster rather
than N — see § Security review.

## Error handling

Neither `Sink` nor `BackgroundTaskRoster()` can fail, and neither returns an error: the store is a
type assertion with the `, ok` form and a map-free assignment under a mutex, and the read is a clone.
There is consequently no error path on which claude-authored text could reach a message or a record.
The two non-happy inputs both have defined answers rather than failures:

- An event of any other variant: not stored, forwarded unchanged, `have` untouched.
- A nil receiver: the unreported state, not a panic.

`next == nil` forwards nothing — a test convenience; production always supplies the next link.

## Testing strategy

`cmd/pyry/session_background_task_hold_test.go` (new), mirroring the twin's file. Fixture:
`holdBackgroundTaskRosterFixture(descriptions ...string)` — consumer-prefixed, because a bare
package-level fixture name collides silently across `cmd/pyry`'s files. It fills every field a clone
must reach, including a non-zero `DroppedTasks` and a non-nil per-entry `TruncatedFields`.

- **Retains and forwards** — one report is retained *and* still reaches `next` unchanged
  (`reflect.DeepEqual`), so a "retain and drop" implementation fails. Asserts `DroppedTasks` survived.
- **Unreported is its own state** — a fresh hold answers `ok == false`. Assertion on the bool alone.
- **An empty roster reads back as reported** — AC 2, and the test with no counterpart in either
  sibling: `Sink(turnevent.BackgroundTaskRoster{})` then `ok == true` with `len(Tasks) == 0`,
  contrasted in the same test against the fresh hold's `false`. This is the arm an implementation
  that spells "unreported" as `len(Tasks) == 0` gets wrong.
- **Second report replaces** — count assertion separates replacement from concatenation, and a
  second row replaces a non-empty roster with an **empty** one (the transition "the last task
  finished"), which is the replacement case a non-empty→non-empty test cannot distinguish from a
  merge.
- **Other variants change nothing** — table over `TextChunk`, `TurnEnd`, `ModelAnnounced`, plus
  `ModelList` and `SlashCommandList` as the cross-store pins: no decorator in the chain may store a
  sibling's variant. Forwarded-unchanged compared with `reflect.DeepEqual`, never `!=`, since these
  carry slices and `==` on the interface would panic.
- **Read returns a deep copy** — AC 3. Mutates all three things a reader can reach: the `Tasks`
  slice, an entry's `TruncatedFields` element, and the entry itself. A clone that stops at `Tasks`
  still passes a one-mutation test.
- **Preserves nil slices** — `slices.Clone(nil)` is nil, which a hand-rolled `make`+`copy` clone
  breaks; the convention `BackgroundTask.TruncatedFields` documents (nil, never empty non-nil) is
  observable to a reader that distinguishes them.
- **Retains past a saturated sink** — AC 1's second clause. Fill a `newStreamTurnSink(1, …)` with a
  droppable `TextChunk`, assert `len(sink.ch) == 1` so the fixture is provably saturated, report the
  roster, assert the channel is still at 1 (refused, not queued) and the roster is retained. Its
  comment states what it pins **and no further**: that retention does not depend on the fan-in
  admitting the event, which rules out a retention point downstream of the channel. It does *not*
  pin statement order inside `Sink` — a refused send consumes nothing, so a forward-then-store mutant
  passes it.
- **Nil-receiver read** — AC 5's second half.
- **Concurrent sink and read** — the `-race` arm, one writer against one reader, the reader touching
  a field of every slice the clone reaches so a shared backing array is a race report rather than a
  tolerated read.

`cmd/pyry/streamsup_runner_test.go`:

- `TestStreamRunnerFactory_Construct` gains the `sr.tasks != nil` and "freshly constructed runner
  reports unreported" pair, beside the two that exist.
- A new `TestNewSessionParser_DecodesAndRetainsBackgroundTaskRoster`, twin of the two wiring tests:
  write one real `system`/`background_tasks_changed` line into the returned parser and read the
  returned hold, proving the production composition **through the real decoder** rather than by
  inspection. The line's shape is taken from the committed capture's field names, not invented.
- A **third** `var _ interface{ BackgroundTaskRoster() (turnevent.BackgroundTaskRoster, bool) } =
  streamRunner{}` block, standing beside the existing two rather than folded into one literal, so a
  failure names which reader lost its shape (AC 5's first half).
- The three existing `newSessionParser` call sites take a fourth blank.

Gate: `go test -race ./cmd/pyry/... ./internal/streamsup/...`, `go vet ./...`,
`go build ./cmd/pyry`.

## Open questions

1. **Does the roster's `Description` warning need repeating on the read method?** Resolved in
   § Security review: yes. The type doc repeats it at the list level "rather than delegated" for
   exactly this reason, and `BackgroundTaskRoster()` is a new site that hands out a list of command
   lines.
2. **Is the retained footprint the cap product or a parse line?** Resolved in § Security review: a
   parse line. `truncateField`'s `strings.ToValidUTF8(s[:limit], "")` returns its input for valid
   UTF-8, so a "capped" field is a slice of the full decoded value. Stating the cap product would
   repeat #2004's ~25x understatement.
3. **Does the parser's refusal paragraph need editing?** Resolved in § Design: yes, a re-scoping
   addendum. `turnevent.BackgroundTaskRoster`'s type doc needs none.

Any of these that moves during Phase B is recorded under `## Revisions` in the same commit as the
code that departs.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No blocking finding. The design adds **no new boundary crossing**. The one
  crossing in this family is `emitBackgroundTaskRoster`, which decodes from the **top-level line
  bytes** — never a nested field, which is what stops a tool result whose text is literally a
  `background_tasks_changed` payload from forging a roster, the most valuable variant in the family to
  forge because it is the one claiming what is *alive*. It bounds both dimensions at construction
  (`maxTaskRosterEntries` for the entry count claude chooses, `maxTaskFieldID` /
  `maxTaskRosterDescription` for the text inside each entry). The hold sits strictly downstream: it
  parses nothing, re-scans nothing, and accepts no caller input but a `turnevent.Event` handed to it
  by the parser's own sink. Everything it holds is therefore already bounded and already
  untrusted-but-typed. Downstream callers are signalled by the type: `BackgroundTask`'s own field docs
  carry the trust statement, and the retention hands out that same type rather than a flattened one.
- **[Subprocess / external command execution]** SHOULD FIX — documentation, landed in Phase B. Nothing
  in this slice execs, and no retained value reaches `exec.Command`. But for claude's `local_bash`
  task type `BackgroundTask.Description` **is the literal command line**, safe to render as text and
  never to execute or re-shell, and the retention creates a *new* site that hands out a **list** of
  them — the type doc's own stated reason for repeating that warning at the list level rather than
  delegating it. `BackgroundTaskRoster()`'s doc must repeat it too, for #2079's author reading the
  read method rather than the element type. Not a MUST FIX: no code path in or reachable from this
  slice executes anything, so this is a warning against a future misuse, not an open one.
- **[Error messages, logs, telemetry]** No finding, and the strongest guarantee in the design. The
  type has no `*slog.Logger` field and its constructor takes none, so there is **no path** by which a
  `TaskID`, `TaskType`, `Description` or a truncated-field name can reach a log record — the #833
  posture enforced by construction rather than by care. It is sharper than on either sibling: those
  hold claude's and the workspace's strings, this holds **command lines**, and the producer's own drop
  path already refuses to log even the entry *count* ("a roster is a list, lists read as diagnostics,
  and 'just the length' is the leak a content-free rule is most often bent for"). Reinforced by there
  being no error path at all (§ Error handling), so no message can carry content either. **The one
  edit that would reopen this channel is adding a logger to write a retention diagnostic**, stated on
  the type as a prohibition.
- **[Network & I/O — resource exhaustion]** No blocking finding, and the claim is derived here rather
  than copied, because #2004's shipped review got the analogous one wrong by ~25x. The retained
  footprint is **not** the cap product (8 × 1024 B = 8 KiB): `truncateField`'s
  `strings.ToValidUTF8(s[:limit], "")` returns its input slice unchanged for already-valid UTF-8, so
  each "capped" string is a slice header over the **full decoded field value**. Every one of those
  values was decoded from a single line, so the honest per-session bound is **one parse line, capped
  by `defaultMaxParseBuf` at 4 MiB** — the same class the two siblings already accepted. Three things
  keep it bounded there: `maxTaskRosterEntries` truncates the slice **before** the emitted entries are
  built, so entries past the 8th are released with the decode target; `Sink` **replaces**, so a
  producer that fires on every roster change (unlike both siblings' once-per-child producer) still
  costs one roster, not N; and `slices.Clone` over a `[]string` copies headers, not bytes, so N
  concurrent readers do not multiply the byte cost. Aggregate: O(one parse line × live sessions),
  released with the session.
- **[Concurrency]** No finding. One leaf mutex, taken and released inside `Sink` before `next` is
  called and inside the read, participating in no ordering with `Pool.mu`, `Session.lcMu` or `capMu`
  — so no lock-ordering edge is added and no deadlock is reachable. No check-then-mutate across a
  release: `Sink` stores unconditionally, and the read tests `have` and clones `roster` under one
  hold. No goroutine is spawned, so no leak and no shutdown path. The writer/reader pair is stated
  correctly on the new type (`cmd.Wait`'s join serialises writers) rather than inheriting the
  falsified two-writer claim the package overview measured wrong.
- **[Tokens, secrets, credentials]** Not applicable, by design rather than by omission: the retained
  value contains no credential. `TaskID` is claude's **opaque join handle** back to the
  `BackgroundTaskStarted` that opened the task — an identifier inside one child's stream, not a
  bearer token, not compared against a secret, and never used to authorise anything. Nothing is
  generated, persisted, rotated or revoked, so the four lifecycle questions have no subject.
- **[File operations]** Not applicable. The retention is memory-only for the session's life. No path
  is constructed, opened, stat-ed or written; nothing reaches disk, so traversal, TOCTOU, file mode,
  symlink and atomic-write questions have no subject.
- **[Cryptographic primitives]** Not applicable. No randomness of any kind (no id minted, no jitter),
  and no comparison against a secret, so neither the `crypto/rand` rule nor
  `crypto/subtle.ConstantTimeCompare` has a call site here.
- **[Threat model alignment]** No new wire surface, no new network-reachable path, and no new
  relay-side handler: nothing this slice adds is reachable from a peer. `docs/protocol-mobile.md`
  § Security model therefore gains no obligation here. **OUT OF SCOPE, named:** publishing the
  retained roster to a client is #2079, and the connect-time reconcile beyond it belongs to the Mode B
  umbrella **#829** — including that umbrella's known **per-device confinement gap**, where a paired
  interactive conn sees every retained value including conversations bound to other workspaces. That
  gap is inherited by any future roster reconcile and is not this slice's to close.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-03
