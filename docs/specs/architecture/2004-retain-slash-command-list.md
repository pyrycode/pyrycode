# #2004 — Retain the decoded slash-command list for the session

## Files read

- `cmd/pyry/session_model_hold.go` → `sessionModelHold`, `newSessionModelHold`, `Sink`,
  `ModelList`, `cloneModelList`, `newSessionParser` — the sibling this slice mirrors. Its
  type doc owns the upstream-of-the-send argument, the no-logger posture and the
  no-registry lifetime claim; this design consumes those rather than re-deriving them.
- `cmd/pyry/session_model_hold_test.go` → `modelListFixture`, `recordingSink`, and the
  eight `TestSessionModelHold_*` functions — the suite shape this slice mirrors onto the
  new variant, and the source of the `recordingSink` helper the new tests reuse.
- `cmd/pyry/streamsup_runner.go` → `streamRunner`, `ModelList`, `newStreamRunnerFactory` —
  where the hold is bound to a runner and where the concrete off-interface reader lives.
  `ModelList`'s doc carries the interface-placement rule (a method goes ON
  `sessions.Runner` only when its consumer sits inside `internal/sessions`).
- `cmd/pyry/streamsup_runner_test.go` → `TestNewSessionParser_DecodesAndRetains`, the
  `var _ interface{ ModelList() … } = streamRunner{}` assertion, and the factory test's
  `sr.models == nil` arm — the three places this slice grows a twin.
- `internal/turnevent/event.go` → `SlashCommandList`, `SlashCommand` — the retained type.
  `Commands` is the only top-level slice, `DroppedCommands` is an int, and there is **no**
  top-level `TruncatedFields`; per entry, `Aliases` and `TruncatedFields` are both
  `[]string`. All three slice fields document nil-for-empty, never an empty non-nil slice.
- `internal/streamsup/parser.go` → `emitSlashCommandList`, its `boundAliases` closure,
  `maxSlashCommandName`, `commandEntryLine` — the producer. `boundAliases`' "THE RESULT IS
  ALWAYS A FRESH ALLOCATION" paragraph and the `make` in `emitSlashCommandList` are what
  license storing without copying on the write side. `commandEntryLine`'s tags
  (`name` / `argumentHint` / `description` / `aliases`) fix the wiring test's fixture line.
  Three doc sites here mention `sessionModelHold` and go false with this commit.
- `cmd/pyry/stream_turn_busy_test.go` → `TestTurnMarkFor_TotalOverEveryVariant` — pins
  `turnMarkNone` for `SlashCommandList`, i.e. this variant really is droppable class, which
  is the premise the whole placement argument rests on.
- `docs/knowledge/features/streamsup-package-retaining-the-decoded-model-list-for-the-session.md`
  — **three lessons that change how this ticket is built**, see Context below: the sibling's
  shipped mutex justification is measurably false; the saturated-sink test pins less than
  its name suggests; and a `cmd/pyry` package-level test fixture name collides silently
  across files in the package.
- `docs/knowledge/features/streamsup-package-producing-turnevent-slashcommandlist.md` — the
  producer's suppression gate (#1877), which is why the unreported state has to be a bool.
- `CODING-STYLE.md`, `docs/PROJECT-MEMORY.md`,
  `docs/knowledge/architecture/system-overview.md` — conventions and the current shape of
  the interactive stream path.

## Context

claude answers one `initialize` control request per child, and `emitSlashCommandList`
decodes exactly one `turnevent.SlashCommandList` from it. Nothing retains it: the parser's
only sink is `sink.sinkFor(cfg.SessionID)`, the droppable-class send into the turn-busy
fan-in, and `turnMarkFor` answers `turnMarkNone` for this variant. Under load the fan-in
refuses it at `droppableCap`, and the drop policy's "self-healing on the next event"
justification does not cover a value that arrives once per child. A client connecting after
that moment has no path to the workspace's slash commands.

#1840 solved the identical problem for the model list with a sink decorator that stores
before forwarding, placed **upstream** of that send. This slice applies the same placement
to the slash-command list, and #2005 reads it back off `Session.Runner` by type assertion —
which is why the runner-level forwarder and its compile-time assertion belong here.

**Three corrections this design takes from the package overview, against the ticket body.**

1. *The mutex justification.* The ticket's Technical Notes repeat #1840's shipped
   justification — "across a respawn a new stdout forwarder goroutine writes the same
   retention while a relay-leg goroutine reads it". The package overview records that as
   **measurably false**: `spawnAndWait` blocks on `cmd.Wait()`, which `os/exec` documents as
   joining the goroutine copying the child's stdout, so forwarder N+1 cannot start until
   forwarder N has finished — `streamsup.Parser`'s own doc says as much. The lock is still
   *required*, for the **reader**: #2005's resolver reads on a relay-leg goroutine
   concurrently with the sole serial writer. This plan writes that justification, not the
   ticket's. Same conclusion (the lock is needed, not defensive), correct premise.
2. *What the saturated-sink test pins.* The overview measured that a mutant swapping
   store-and-forward inside `Sink` passes the whole suite, because a refused channel send
   consumes nothing. The test therefore claims only that retention does not depend on the
   fan-in **admitting** the event — enough to rule out a retention point downstream of the
   channel, and no finer. The new test's name and doc say exactly that and no more.
3. *Fixture naming.* `modelListFixture` is already taken at package scope in `cmd/pyry`, and
   #1849 hit a same-package collision that reads like a type error. The new fixture is
   `holdSlashCommandListFixture`, prefixed by its consumer, leaving the obvious bare name
   free — `emitterSlashCommandListFixture` already occupies the emitter's half of that space.

**The sibling's own false sentence is corrected in this commit.** The overview says
explicitly that whoever next edits `sessionModelHold`'s doc should fix the justification
rather than trust that a passed review means the prose is accurate. This slice edits that
file, so it fixes the one sentence — comment-only, no behaviour change, recorded here so the
verifier reads it as a decision rather than as drift.

**No ADR.** This is the third instance of an established pattern (`sessionModelHold` is the
decision record); nothing is being decided that #1840 did not already decide.

## Design

### Choice: a sibling type, chained — not a second retention inside `sessionModelHold`

The ticket leaves this to the builder. A sibling type wins:

- `sessionModelHold`'s name and its forty-line doc stay honest. Growing it to hold two
  unrelated values either makes the name lie or forces a rename cascade across the type, its
  constructor, its two methods, the `streamRunner` field and the test file — a refactor
  bought for cosmetics.
- Two leaf mutexes, each over one value, keep the reasoning local; a shared mutex would let
  a slash-command write block a model read for no gain.
- The chain is the existing idea applied twice, not a new idea: the path is already a
  decorator over `sinkFor`, and a second link changes nothing about the property that
  matters — both stores happen on the parser's side of the channel, so neither is a
  candidate for the `droppableCap` refusal whatever it does internally.

Order within the chain is immaterial (both links store-then-forward unconditionally) and the
plan does not pretend otherwise.

### New file: `cmd/pyry/session_slash_command_hold.go`

```go
type sessionSlashCommandHold struct {
    mu   sync.Mutex
    list turnevent.SlashCommandList
    have bool
    next func(turnevent.Event)   // nil forwards nothing (a test convenience)
}

func newSessionSlashCommandHold(next func(turnevent.Event)) *sessionSlashCommandHold
func (h *sessionSlashCommandHold) Sink(ev turnevent.Event)
func (h *sessionSlashCommandHold) SlashCommandList() (turnevent.SlashCommandList, bool)
func cloneSlashCommandList(list turnevent.SlashCommandList) turnevent.SlashCommandList
```

- `Sink` stores a `SlashCommandList` — replacing any prior value — then forwards **every**
  event unchanged, this variant included. Stored without copying: `emitSlashCommandList`
  allocates `slashCommands` with `make` per emit and `boundAliases` documents that its
  result is always a fresh allocation and never the resliced input, so the hold takes sole
  ownership of what it is handed. The copy is made on the read side.
- The mutex is a **leaf** lock, never held across the call to `next`. It participates in no
  ordering with `Pool.mu`, `Session.lcMu` or `capMu`.
- `SlashCommandList` is nil-receiver-safe, returning the zero value and `false`.
- **No `*slog.Logger` field and no logger parameter**, by construction. Every string on
  `turnevent.SlashCommand` is workspace-authored and never sanitized; a retention diagnostic
  is the one channel that would put those bytes into a log record.

### `cloneSlashCommandList` — three levels, not the sibling's two

`Commands`, then within each entry both `Aliases` and `TruncatedFields`. `DroppedCommands`
is an int and copies by assignment, and this variant has **no** top-level `TruncatedFields`
— the count dimension reports per entry — so the field `cloneModelList` clones at that level
does not exist here. `slices.Clone` returns nil for nil, which preserves turnevent's
convention that these fields are nil when there is nothing to report.

### `newSessionParser` grows a third return value

```go
func newSessionParser(next func(turnevent.Event), logger *slog.Logger) (
    *streamsup.Parser, *sessionModelHold, *sessionSlashCommandHold)
```

It mints both holds and the parser in one call, chaining them so the parser's sink is the
outer decorator and `next` is the innermost downstream. The single call is what makes the
three halves impossible to wire to different holds, and what keeps the composition testable
through the real decoder. Two call sites update: `newStreamRunnerFactory` and
`TestNewSessionParser_DecodesAndRetains`.

### `streamRunner` gains a field and a forwarder

`commands *sessionSlashCommandHold` beside `models`, assigned from the factory's third
return, and:

```go
func (a streamRunner) SlashCommandList() (turnevent.SlashCommandList, bool)
```

**OFF `sessions.Runner`**, for `ModelList`'s stated reason exactly: the consumer (#2005) sits
in `cmd/pyry` and reaches it by type assertion off `Session.Runner`, so widening the
interface would buy no compile-time guarantee and would drag every fake runner under
`internal/sessions` and `cmd/pyry` into the diff. It is the fifth such concrete method, after
`Interrupt`, `RestartFresh`, `BeginRotation` and `ModelList`. A runner whose hold was never
minted answers the unreported state rather than panicking, because the read is
nil-receiver-safe.

The compile-time statement of that shape is a **twin** of #1840's, placed beside it in
`streamsup_runner_test.go` rather than folded into it, so each ticket's asserted shape stands
alone:

```go
var _ interface {
    SlashCommandList() (turnevent.SlashCommandList, bool)
} = streamRunner{}
```

### `internal/streamsup/parser.go` — comment-only corrections

Three doc sites state that nothing retains this array; `git grep -n sessionModelHold --
internal/streamsup/` finds all six mentions and these are the three that go false:

- `maxSlashCommandName`'s doc — "there is no `sessionModelHold` analogue for this array, so
  the retention is the event's own lifetime", and the sentence after it contrasting the cap
  with `maxModelResolved`, "whose capped result cmd/pyry holds for the child's life". This is
  the site that **owns** the claim.
- `commandEntryLine`'s doc — the retained bounded copy is "still shorter than the models
  array's, whose CAPPED result is retained for the child's life by cmd/pyry's
  `sessionModelHold`".
- `emitSlashCommandList`'s doc — "NOTHING RETAINS IT either: there is no `sessionModelHold`
  analogue for this array, which `maxSlashCommandName`'s doc already states and owns." A
  restatement that explicitly defers to the site above, so it goes false with it.

The ticket names two; the third is the same claim restated one level away and correcting it
is part of the same sweep. Nothing else changes in that file.

**Out of scope, observed while reading.** `emitSlashCommandList`'s doc also states that
`interactiveTurnEmitterV2.Handle` "has NO CASE for this variant" and that Handle's case "is
#2003's and still open". #2003 merged (`2ff8072b`) without touching `parser.go`, so both
sentences are already false — pre-existing staleness this commit neither causes nor fixes.
Flagged in the PR body for the documentation phase rather than swept in here.

## Concurrency model

No goroutine is created and none is changed. The retention is written by exactly one
goroutine and read by others:

- **Writer:** the stdout-forwarder goroutine `os/exec` runs for the child's non-`*os.File`
  `Stdout`, which is the parser. `spawnAndWait` blocks on `cmd.Wait()` before returning, and
  `cmd.Wait()` joins that goroutine, so forwarder N+1 cannot start until forwarder N has
  finished — writes are serial across every respawn, which is what `streamsup.Parser`'s own
  doc asserts.
- **Readers:** #2005's resolver, on a relay-leg goroutine, concurrent with that writer.

The mutex exists for the writer/reader pair, not for two overlapping writers. It is a leaf:
held around the store and around the clone, never across the forward to `next`, so holding
it can never be an edge in the daemon's lock order.

## Error handling

There is no error path to add. The only failure modes worth naming:

- **Nothing reported** — the comma-ok's `false`. It is the *only* spelling of that state.
  An empty list cannot stand in for it, and the reason differs from the sibling's:
  `ModelList.Models` is documented "Never empty", whereas here it is the **producer** that
  suppresses — `emitSlashCommandList` returns early on a zero-length entry list (#1877) — so
  nothing with zero entries ever reaches the retention and an empty list is not a value any
  reader can be handed.
- **Nil hold** — a `streamRunner` whose retention was never minted (a hand-constructed value
  in a test) reads as unreported rather than panicking.
- **A refused fan-in send** — already handled by placement: the store happens before the
  forward, so a refusal discards the event and never the retention.

## Testing strategy

`cmd/pyry/session_slash_command_hold_test.go` (new), mirroring the sibling's eight, with a
`holdSlashCommandListFixture(names ...string)` builder that fills every field a clone must
reach — `Aliases`, `TruncatedFields`, `ArgumentHint`, `Description` and a non-zero
`DroppedCommands` — and never builds an empty list, since the producer's gate makes that a
shape production cannot reach:

- **RetainsAndForwards** — both halves of `Sink`'s contract at once: retained *and*
  forwarded unchanged, so a retain-and-swallow implementation fails.
- **UnreportedIsItsOwnState** — on the bool alone, never on `len(Commands) == 0`.
- **SecondReportReplaces** — entry-count assertion separates replacement from accumulation.
- **OtherVariantsChangeNothing** — table over non-`SlashCommandList` variants; each is
  forwarded and none flips the retention. This is the arm a store-anything implementation
  fails. `turnevent.ModelList` is one of the rows, which additionally pins that the two
  chained holds do not cross-store.
- **ReadReturnsDeepCopy** — mutates all four things a reader can reach: the `Commands` slice
  itself, and within an entry `Aliases`, `TruncatedFields`, and the entry value. A clone
  stopping at `Commands` passes a one-mutation test, so all four are mutated.
- **RetainsPastASaturatedSink** — a `newStreamTurnSink(1, …)` filled with a droppable event,
  with a `len(sink.ch)` assertion *before* the report proving the fixture really is
  saturated. What it pins: retention does not depend on the fan-in admitting the event —
  which rules out a retention point downstream of the channel and, per the package
  overview's measurement, nothing finer. The test's doc says that and does not claim to pin
  statement order inside `Sink`.
- **NilReceiverRead** — zero value and `false`; asserts `Commands == nil` and
  `DroppedCommands == 0`.
- **ConcurrentSinkAndRead** — the `-race` arm: one goroutine reporting, one reading and
  touching a field of every slice the clone reaches, so a shared backing array is a race
  report rather than a tolerated read.

`cmd/pyry/streamsup_runner_test.go` (extended):

- **`TestNewSessionParser_DecodesAndRetainsSlashCommands`** — the wiring proof through the
  real decoder: one `control_response` initialize line whose `response.commands` array
  carries entries with `name` / `argumentHint` / `description` / `aliases`, written into the
  parser `newSessionParser` returned, read back off the returned hold, with the downstream
  sink still seeing the event. Commands-only (no `models` key), so the line takes the
  producer's commands-only rung and isolates this variant.
- The factory test's per-shape arm grows a `sr.commands == nil` check and an
  `sr.SlashCommandList()` unreported assertion beside the existing `sr.models` pair.
- The compile-time assertion twin described above.

Gate: `go test -race ./cmd/pyry/... ./internal/streamsup/...`, `go vet ./...`,
`go build ./cmd/pyry`.

## Open questions

1. **Chain order — models outermost or commands outermost?** Immaterial to behaviour; both
   links store unconditionally and forward unconditionally. Resolve by writing the
   construction inner-to-outer (`commands` wrapping `next`, `models` wrapping
   `commands.Sink`) so the reading order of the three statements matches the event's path,
   and say so in `newSessionParser`'s doc rather than leaving a reader to infer that the
   order carries meaning.
2. **Does the wiring test need its own line, or can it extend the existing models line?** A
   line carrying both arrays would exercise the producer's both-arrays rung and couple two
   tickets' fixtures. Resolve toward a separate commands-only line and a separate test
   function unless the producer turns out to reject a commands-only success response — the
   rung enumeration in `emitInitializeControlResponse`'s doc says it does not.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries] No findings, and this slice moves one.** The boundary is claude's
  stdout → daemon memory, crossed in `emitSlashCommandList`, which is where every value on
  `turnevent.SlashCommand` is bounded at construction. What changes is the *lifetime* on the
  trusted side: bytes that lived for one event now live for the session. The type they are
  held in is unchanged, and `turnevent.SlashCommandList`'s SECURITY paragraph — "IT IS A
  REPORT, NEVER A CONTROL INPUT", no field may reach a child as an argv element, the render
  boundary is the client's — travels with the value rather than with its lifetime, so a
  downstream holder is told the same thing it was told before. The hold adds no parsing, no
  validation and no re-interpretation: it is a store and a clone. The one boundary claim
  this design makes on its own is that it does not *widen* trust, and the way it makes that
  checkable is by holding the declared type rather than any projection of it.
- **[Tokens, secrets, credentials] Not applicable, by what the retained type carries.** No
  token, key or credential is on `turnevent.SlashCommandList`; every string is a
  workspace-authored command name, hint, description or alias. Nothing is generated, stored
  to disk, rotated or revoked. The lifetime question a credential would raise — "how does
  this stop being valid?" — is answered structurally: the hold is a field of the per-session
  `streamRunner` and dies with the session, with no registry and no removal hook, so there is
  no residue to expire.
- **[File operations] Not applicable.** No path is constructed, no file opened, created or
  removed. The design is memory-only.
- **[Subprocess / external command execution] No findings, and the risk is real enough to
  name rather than dismiss.** The retained strings are workspace-authored, and a command
  *name* is exactly the shape that invites being passed to `exec.Command`. Nothing in this
  slice execs, and `turnevent.SlashCommandList`'s own doc forbids any field here reaching a
  child as an argv element. What retention changes is opportunity, not permission: a value
  that is now readable at any time is easier to reach for. The mitigation this design can
  offer is that the reader (#2005) gets the declared type with its doc attached, and the
  re-open trigger `commandEntryLine`'s doc already names — "the first slice that gives any
  of them a SYNTAX sink … or an argv element" — covers exactly that future.
- **[Cryptographic primitives] Not applicable.** No randomness, no comparison against a
  secret, no key material.
- **[Network & I/O] No findings; every bound is inherited and none is loosened.** Three
  dimensions are already bounded at construction by the producer — per-entry text under
  `maxSlashCommandName` / `maxSlashCommandArgumentHint` / `maxSlashCommandDescription` /
  `maxSlashCommandAlias`, the alias count under `maxSlashCommandAliasCount`, the entry count
  under `maxSlashCommandListEntries` — and the whole line under `defaultMaxParseBuf` before
  the decoder runs. The retention holds exactly one such capped value: memory is
  O(one capped list) per session and is *replaced*, never appended, so a hostile workspace
  respawning repeatedly cannot grow it. The frame-level cut on the way out
  (`maxSlashCommandListBytes`, #2002) is downstream and untouched.
- **[Error messages, logs, telemetry] No findings — this is the category the ticket's fifth
  AC exists for, and the enforcement is structural.** `sessionSlashCommandHold` has no
  `*slog.Logger` field and its constructor takes no logger, so no workspace-authored string
  can reach a log record by construction rather than by care. That is the same posture
  `sessionModelHold` states for model values, and it is stronger here in one respect worth
  naming: these strings are the *workspace's* rather than claude's, so an author who controls
  a repository controls them. There is no error return and no error string, so nothing leaks
  through that channel either. **A retention diagnostic must never be added to this type** —
  that is the one edit that would reopen the channel.
- **[Concurrency] No findings.** One mutex, taken and released inside `Sink` and inside the
  read, never held across the forward to `next`, so it is a leaf and adds no edge to the
  daemon's lock order. No lock ordering to document because no second lock is ever held. No
  check-then-mutate: `Sink` stores unconditionally under the lock, and the read tests `have`
  and clones under the same hold. No goroutine is spawned, so there is none to leak. On
  shutdown the value is dropped with the session; there is no partial state to recover
  because nothing is persisted.
- **[Threat model alignment] Addressed, and one item is explicitly not this slice's.**
  `docs/protocol-mobile.md` § Security model governs what crosses to a phone; nothing crosses
  here — this slice publishes nothing and adds no wire field. The client-side render boundary
  that `turnevent.SlashCommandList`'s SECURITY paragraph assigns to whichever slice publishes
  this list is **out of scope** and belongs to the publication chain (#2005 and its
  successors), which is where a value first reaches a client.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-02
