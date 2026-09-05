# Retain the per-model context windows claude reports at each turn end (#2106)

## Files read

- `cmd/pyry/session_background_task_hold.go` → `sessionBackgroundTaskHold`, `newSessionBackgroundTaskHold`,
  `Sink`, `BackgroundTaskRoster`, `cloneBackgroundTaskRoster` — the nearest analogue and the shape this
  ticket applies a fourth time. Its `DroppedTasks` paragraph is the rule the two kept fields here inherit.
- `cmd/pyry/session_model_hold.go` → `sessionModelHold`, `newSessionParser`, `cloneModelList` — the family's
  origin, and the file that carries the chain and the return-shape note this ticket is nominated to settle.
- `cmd/pyry/session_slash_command_hold.go` → `sessionSlashCommandHold`, `cloneSlashCommandList` — the second
  application; read for how a sibling justifies its own unreported-state bool rather than copying one.
- `cmd/pyry/streamsup_runner.go` → `streamRunner`, `newStreamRunnerFactory`, `ModelList`,
  `SlashCommandList`, `BackgroundTaskRoster` — where the holds are minted and where the accessor lands.
  The interface-placement rule (concrete method, `sessions.Runner` un-widened) is stated on all three.
- `cmd/pyry/stream_turn_busy.go` → `turnMarkFor` — answers `turnMarkClose` for `turnevent.TurnEnd`. This is
  the single fact that makes the three siblings' droppability paragraph FALSE here.
- `cmd/pyry/stream_turn_drain.go` → `sinkFor`, `streamTurnSinkCloseReserve` — the closer arm: a `TurnEnd` is
  refused only when the channel is genuinely full, and the refusal writes a Warn carrying `eventKind` and
  the session id only.
- `internal/turnevent/event.go` → `TurnEnd`, `ModelWindow` — what is retained and what it already
  guarantees (`WindowTokens > 0`, sorted by `ModelID`, both dimensions bounded at construction, and the
  five-shape collapse to nil). Also the note that `TurnEnd` stopped being comparable with `==`.
- `internal/streamsup/parser.go` → `decodeModelWindows`, `resultLine`, `maxModelWindowEntries`,
  `maxModelWindowID`, and the `result` arm of the line dispatch — the producer. Read for the exact
  allocation the retained slice would otherwise pin (see the write-side clone under **Design**).
- `docs/knowledge/features/streamsup-package-retaining-the-decoded-model-list-for-the-session.md` — the
  family overview. Three lessons bind this ticket: a sibling's shipped doc comment may carry a premise this
  file has already measured false, so premises get re-derived rather than copied; store-before-forward is
  NOT what keeps the retention off the droppable send, so no test may be named around it; and a security
  review's footprint claim needs the same care as the code, the ~25x understatement in #2004's having
  survived into the shipped spec.
- `internal/e2e/realclaude/testdata/bypass_reescalation_v2.1.239_control_default.json` → its `result`
  records — the reference `modelUsage` shape, including the alias pair naming one model twice.
- `CODING-STYLE.md`, `docs/PROJECT-MEMORY.md`, `docs/knowledge/architecture/system-overview.md` — house
  conventions and the current system picture.

## Context

`turnevent.TurnEnd` has carried `ModelWindows` — claude's reported context window per model id, decoded off
the `result` line — since #2101. Nothing keeps it: the parser emits the event, the bridge pushes it to
whatever interactive connections exist at that instant, and the value is gone. #2107 reads at an arbitrary
moment on a relay-leg goroutine with no turn in flight, so a retention point has to exist before that read
can be joined to anything.

`cmd/pyry` already has this exact shape three times over — `sessionModelHold` (#1840),
`sessionSlashCommandHold` (#2004), `sessionBackgroundTaskHold` (#2077). Each retains one variant behind a
`Sink` decorator that stores and forwards; `newSessionParser` chains them and binds them to the parser in
one call; `streamRunner` exposes each as a concrete method off the un-widened `sessions.Runner`. This is
that idea applied a fourth time.

No ADR is warranted. The sibling-versus-nested-retention choice is already argued on `newSessionParser` and
`sessionModelHold`, and nothing here reopens it. No wire change, no protocol-doc change, no live-claude run.

## Design

### The retained value

A package-private pair rather than the whole variant. This is the first hold in the family to retain FIELDS
of a variant, because `TurnEnd.Reason` is turn-specific and must not be retained:

```go
// modelWindowReport is one turn's per-model window report: the entries and the
// count of what the producer cut, which are ONE report and travel together.
type modelWindowReport struct {
	Windows []turnevent.ModelWindow
	Dropped int
}
```

`Dropped` rides the pair for `BackgroundTaskRoster.DroppedTasks`' reason unchanged: it is this variant's
only truncation report, `len(Windows) + Dropped` is exactly what claude sent, and a read that carried only
the entries would let a capped report be read as a whole one.

### The hold

New file `cmd/pyry/session_model_window_hold.go`, mirroring the three siblings' file layout:

| Symbol | Contract |
|---|---|
| `sessionModelWindowHold` | struct: leaf `sync.Mutex`, the retained `modelWindowReport`, a `have` bool, and `next func(turnevent.Event)`. **No `*slog.Logger` field.** |
| `newSessionModelWindowHold(next func(turnevent.Event)) *sessionModelWindowHold` | mints a hold that forwards to `next`; `nil` forwards nothing (test convenience). **Takes no logger.** |
| `(*sessionModelWindowHold).Sink(ev turnevent.Event)` | the decorator, used as a method value. Stores on a USABLE `TurnEnd` only; forwards every event of every variant unchanged. |
| `(*sessionModelWindowHold).ModelWindows() (modelWindowReport, bool)` | the read: a copy of the retained report and `true`, or the zero value and `false`. Nil-receiver-safe. |
| `cloneModelWindowReport(modelWindowReport) modelWindowReport` | one level — `slices.Clone` of `Windows`. `turnevent.ModelWindow` has no slice fields, so there is no second level; `Dropped` copies by assignment. |

Four decisions are this variant's own and none of them transfers by analogy from a sibling:

**1. Silence is a no-op, not an erasure.** The store predicate is `len(e.ModelWindows) > 0`. #2101 collapses
five "claude said nothing usable" shapes to a nil `ModelWindows`, so a `result` line without a usable
`modelUsage` is claude saying nothing about windows, not claude saying the window changed. Erasing on
silence would make a reader flicker between the true window and a fallback across turns. This is a
deliberate departure from `sessionBackgroundTaskHold.Sink`'s unconditional replace, which is correct there
because an empty roster is a positive statement and an absent window map is not.

A nil `ModelWindows` with a non-zero `DroppedModelWindows` is still silence and still a no-op: `Dropped`
alone names no model and no window, so there is nothing for #2107 to join on. `len(...) > 0` rather than
`!= nil` so the predicate is robust to an empty non-nil slice the producer does not currently emit.

**2. Replace whole; do not merge by id.** A later usable report supersedes the earlier one entirely.
Union-by-id would retain a window for a model the current turn did not touch and would need a cardinality
bound of its own; no case has been observed where a turn omits a model whose window is still wanted, and a
miss already has a safe answer downstream.

**3. Cloned on the WRITE side as well as the read side — the one place this hold departs from all three
siblings.** Every sibling stores what it is handed uncopied, on the ground that the producer allocates
fresh per emit and the parser retains no reference. Both halves of that are true here too, and the
conclusion still does not follow. `decodeModelWindows` pre-allocates its result as `make([]turnevent.
ModelWindow, 0, len(rl.ModelUsage))` — capacity sized by CLAUDE'S MAP, not by `maxModelWindowEntries` — and
its cardinality cut clones only on the over-cap path. So a `modelUsage` padded with unusable entries yields
a slice of length ≤ 16 whose backing array is sized by the padding, bounded only by one parse line under
`defaultMaxParseBuf`. Storing that as-handed would pin it for the session's life. `slices.Clone` on the
write side allocates exactly `len` elements and drops the pad. It preserves the nil-for-nil convention, and
it is unreachable on the silent path anyway, since the predicate has already excluded a nil.

**4. The type doc must NOT carry the siblings' droppability paragraph.** All three open with a variant of
"`turnMarkFor`'s default arm answers `turnMarkNone` for this variant, so `sinkFor` refuses it at
`droppableCap` under load". `turnMarkFor` answers `turnMarkClose` for `TurnEnd`, so it rides the
`streamTurnSinkCloseReserve` band the droppable class may never take and is never refused at
`droppableCap`. The retention still belongs upstream of the fan-in send, for a weaker but real reason the
doc states in its own words: past the reserve a closer can still be lost, and `sinkFor`'s closer arm logs
that loss at Warn.

### The chain and the return shape

`newSessionParser`'s doc already nominates this ticket to change its own shape: *"Returning a fourth value
rather than one struct is deliberate at THIS count: a struct would rewrite two siblings' call sites for no
behaviour change. A fourth hold is where that trade flips."* This is that fourth hold, so the tuple becomes
one struct and the doc paragraph is rewritten to describe what shipped rather than left describing the
tuple.

```go
// sessionRetentions is the set of per-session holds newSessionParser mints and
// binds to the parser it returns. One value rather than a positional tuple.
type sessionRetentions struct {
	models   *sessionModelHold
	commands *sessionSlashCommandHold
	tasks    *sessionBackgroundTaskHold
	windows  *sessionModelWindowHold
}

func newSessionParser(next func(turnevent.Event), logger *slog.Logger) (*streamsup.Parser, sessionRetentions)
```

The chain gains a fourth link, minted innermost-first exactly as today. Its position carries no meaning:
every link stores unconditionally with respect to variant and forwards unconditionally, and what puts each
retention upstream of the fan-in send is being on the parser's side of the channel at all.

`streamRunner` EMBEDS `sessionRetentions` rather than restating its four fields. The holds then cross the
factory as one value and reach the adapter unsplit, the construction site sets one field instead of four,
and field selectors (`a.models`, `sr.tasks`) keep working by promotion — so the three existing accessors
and `TestStreamRunnerFactory_Construct`'s existing assertions are untouched. `sessionRetentions` declares
no methods, so nothing is promoted into `streamRunner`'s method set and the compile-time
`var _ sessions.Runner = streamRunner{}` assertion is unaffected.

### The accessor

`(streamRunner).ModelWindows() (modelWindowReport, bool)` — the SEVENTH concrete method OFF the un-widened
`sessions.Runner`, after `Interrupt`, `RestartFresh`, `BeginRotation`, `ModelList`, `SlashCommandList` and
`BackgroundTaskRoster`. Off for their reason exactly rather than by resemblance: the rule those docs state
is that the interface carries a method when its consumer sits INSIDE `internal/sessions`, where a
structural assertion would fail open. This one's consumer is #2107, in `cmd/pyry`, which reaches it by type
assertion off `Session.Runner` the way `interruptRunner` already does.

The returned type being package-private is deliberate and costs nothing: every consumer is in `cmd/pyry`.

## Concurrency model

No new goroutines, so nothing to shut down.

One `sync.Mutex` on the hold, held only around the store and around the read's clone, NEVER across the call
to `next`. Holding it across a channel send would put a new edge into the daemon's lock order for no
benefit; as written it participates in no ordering with `Pool.mu`, `Session.lcMu` or `capMu`.

The pair the lock exists for is ONE WRITER AND MANY READERS — not two writers. Re-derived rather than
copied from a sibling, because the family overview records that this exact justification shipped wrong once
and was corrected in place: writes are serial across every respawn because `spawnAndWait` blocks on
`cmd.Wait`, which `os/exec` documents as joining the goroutine copying the child's stdout into a
non-`*os.File` `Stdout`, so forwarder N+1 cannot start until forwarder N has finished; `streamsup.Parser`'s
own doc asserts that serialisation. What the lock protects against is #2107's reader on a relay-leg
goroutine running while that one writer does.

Lifetime is the session's, with no registry and no removal hook, for the reason `sessionModelHold` states
in full: the hold is a field of the per-session `streamRunner`, whose `Session.Runner` is assigned once at
construction and never reassigned, and a daemon-global session-keyed map would outlive every session it
keyed with nothing to prune it.

## Error handling

The hold has no error path and no error return. Every input is a value:

- A non-`TurnEnd` event → forwarded, retention untouched.
- A `TurnEnd` with no usable entries → forwarded, retention untouched (no-op, not erasure).
- A `TurnEnd` with usable entries → stored, forwarded.
- A read before any usable report → the zero value and `false`.
- A read on a nil receiver → the zero value and `false`, so a runner whose retention was never minted
  answers the unreported state rather than panicking.

Nothing is logged on any path, at any level, by construction: no logger field, no logger parameter.

## Testing strategy

New `cmd/pyry/session_model_window_hold_test.go`, mirroring the sibling suite's arms with this variant's
own additions. Fixture builder named `holdModelWindowsFixture` — prefixed by its consumer, per the family
overview's recorded name-collision lesson (`cmd/pyry` is one package across many files).

- **RetainsAndForwards** — the entries AND `Dropped` are retained, and the event still reaches the
  downstream sink unchanged. The forward is compared with `reflect.DeepEqual`, never `==`: `TurnEnd` stopped
  being comparable at #2101 and `==` on the interface would panic at runtime rather than compare.
- **UnreportedIsItsOwnState** — a fresh hold reads `ok == false`. There is no reported-but-empty answer to
  distinguish here, so the bool is the only spelling of the unreported state.
- **SilentTurnIsANoOp** — table, and the arm no sibling has. Rows: a `TurnEnd` with nil windows and zero
  dropped; a `TurnEnd` with nil windows and a NON-ZERO dropped count (claude sent entries, none usable).
  Each row is run twice — on a fresh hold, where it must leave `ok == false`, and after a usable report,
  where it must leave that report intact. The second half is what an unconditional-replace mutant fails.
- **SecondUsableReportReplaces** — a later usable report supersedes the earlier one whole. Asserts the
  entry COUNT as well as the ids, which is what separates replacement from merge-by-id; the two agree on
  an id present in both reports.
- **OtherVariantsChangeNothing** — the variants that are not a `TurnEnd`. `model_list`,
  `slash_command_list` and `background_task_roster` are rows on purpose: they are what the other three
  links of this chain retain, so they pin that no decorator stores a sibling's variant.
- **ReadReturnsDeepCopy** — one level, per `cloneModelWindowReport`. Mutates the `Windows` slice element
  and `Dropped`, then re-reads.
- **RetainsPastASaturatedSink** — AC 4, and it needs the CLOSER arm rather than the droppable one. A
  one-slot `newStreamTurnSink` is filled with a droppable event so the channel is genuinely full; a
  `TurnEnd` is then refused by `sinkFor`'s closer arm rather than at `droppableCap`. `len(sink.ch)` is
  asserted before and after, so a green cannot be the sink having quietly queued the event. What it pins is
  that the retention does not depend on the fan-in ADMITTING the event — NOT statement order inside `Sink`,
  which a refused send makes unobservable.
- **NilReceiverRead** — the zero value and `false`, no panic.
- **ConcurrentSinkAndRead** — the `-race` arm. Writer is production's stdout-forwarder goroutine, reader is
  #2107's on a relay leg; the reader touches a field of every entry the clone reaches.

In `cmd/pyry/streamsup_runner_test.go`:

- **TestNewSessionParser_DecodesAndRetainsModelWindows** — the composition through the real decoder rather
  than by inspection. Writes one real `result` line into the parser `newSessionParser` returned and reads
  the hold it returned. The `modelUsage` is the committed capture's own, keys copied rather than invented,
  including the ALIAS PAIR naming one model twice and the `maxOutputTokens` / `canonicalModel` keys the
  producer's decode target deliberately does not declare. A second `result` line with no `modelUsage`
  follows, proving the no-op survives the real producer's nil rather than only a hand-built one.
- **TestStreamRunnerFactory_Construct** — one more block: `sr.windows` is non-nil and `ModelWindows()`
  reads unreported on a freshly constructed runner.
- The four `newSessionParser` call sites take the struct return.

Verification gate: `go test -race ./cmd/pyry/...`, `go vet ./...`, `go build ./cmd/pyry`. No live-claude
run: the retained value has no reader until #2107 and nothing on the wire changes.

## Open questions

1. **Does the write-side clone belong in this ticket or in the producer?** Resolved before writing code: in
   this ticket. The pre-allocation in `decodeModelWindows` is correct for a value the caller consumes and
   discards, and narrowing it there would be an out-of-scope production edit under § Scope Discipline. The
   hold is the component that makes the lifetime long, so it is the component that pays for it.
2. **Should `streamRunner` embed `sessionRetentions` or restate its fields?** Resolved: embed. It keeps the
   three existing accessors and the existing factory test compiling unchanged and makes the construction
   site set one field. Recorded here so the choice is visible rather than inferred from the diff.
3. **Does `ModelWindows()` returning a package-private type block #2107?** Resolved: no. #2107's consumer is
   in `cmd/pyry`, the same package. Should a later ticket need the value outside it, the export is a rename
   at one declaration, not a redesign.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries] SHOULD FIX.** The boundary is explicit and single — `decodeModelWindows`, the one
  place claude's stdout becomes daemon state on this path — but this ticket adds a SECOND site that hands
  what crossed it back out. `turnevent.ModelWindow.ModelID` is claude-authored text, bounded at 256 bytes by
  `maxModelWindowID` and deliberately NOT sanitized: no control-character and no terminal-escape stripping
  happens anywhere on this path, and the field's own doc places the render obligation on the client. Until
  now the only consumer was an event that lived for a microsecond; `ModelWindows()` hands the same text to a
  caller on an arbitrary goroutine for the session's whole life. Phase B restates the untrusted-text
  obligation on the accessor's doc, the way `sessionBackgroundTaskHold.BackgroundTaskRoster` restates
  `turnevent.BackgroundTask.Description`'s warning at the roster level rather than relying on the field doc
  alone.
- **[Subprocess / external command execution] SHOULD FIX.** Nothing in this design spawns, signals or writes
  to a subprocess. The nearest sibling's hazard does NOT transfer and must not be copied: no field retained
  here has ever been a command line, so `sessionBackgroundTaskHold`'s "safe to render, never to execute or
  re-shell" paragraph would be false ceremony on this type. A different hazard does apply and is written
  down nowhere yet — `ModelID` is claude's map key VERBATIM, with no allowlist check and no rejection of a
  leading `-`, so joining it back onto a spawn's argv as `--model <id>` would be argument injection into the
  daemon's own child. `withApprovalArgs` and `claudeSettingsArgs` are the argv composers a consumer might
  reach for. Phase B names this on the accessor's doc; enforcement stays the consumer's, and #2107 reads
  this value to size a gauge, not to compose a spawn.
- **[Tokens, secrets, credentials] No findings.** The retained pair is a list of model ids and two ints.
  Nothing here is, wraps or derives from a credential, and `TurnEnd.Reason` is deliberately excluded, so no
  turn-outcome signal is retained either.
- **[File operations] No findings.** The design constructs no path and opens, stats, writes and renames
  nothing. It is in-memory per-session state with no on-disk representation, so path traversal, TOCTOU, file
  modes, symlinks and atomic-write partial state are all unreachable rather than merely unaddressed.
- **[Cryptographic primitives] No findings.** No randomness of any kind and no comparison against a secret,
  so neither the RNG choice nor constant-time comparison arises. `Sink` performs one type assertion, one
  length test, one `slices.Clone` and one struct assignment; `ModelWindows` performs one clone.
- **[Network & I/O] No findings, with the retained footprint DERIVED rather than inherited.** This is the
  category the family overview nominates the next author to get right, #2004's shipped review having
  understated its own by ~25x. Two components, both bounded:
  - *The backing array.* Measured, and it is what drove Design §3 rather than something the design already
    happened to cover. `decodeModelWindows` pre-allocates `make([]turnevent.ModelWindow, 0,
    len(rl.ModelUsage))` — capacity sized by CLAUDE'S MAP, not by `maxModelWindowEntries` — and clones only
    on the over-cap path. A `modelUsage` padded with unusable entries therefore yields a slice of length ≤
    16 over an array sized by the padding, bounded only by one parse line under `defaultMaxParseBuf`
    (4 MiB). Storing as-handed, the way all three siblings store, would pin that for the session's life.
    The write-side `slices.Clone` allocates exactly `len` elements and drops the pad.
  - *The strings.* #2004's understatement does NOT recur here, and the reason is checked rather than
    assumed. Its fields alias the whole pre-cap parse line because `truncateField`'s
    `strings.ToValidUTF8(s[:limit], "")` returns its input unchanged for already-valid UTF-8. `ModelID`
    never takes that path: it is an `encoding/json` MAP KEY, and a decoded key is a fresh allocation rather
    than a slice of the input buffer — measured 2026-09-05 by unmarshalling a 300 KB line and comparing the
    key's `unsafe.StringData` against the input's bounds, which reported no aliasing. An entry whose id
    exceeds the cap is dropped rather than truncated, so no longer string can be retained. The bound is
    therefore genuinely `maxModelWindowEntries` × (`maxModelWindowID` + the struct) ≈ 4.5 KB per session,
    not one parse line.
- **[Error messages, logs, telemetry] No findings, and AC 5 is structural rather than careful.** The hold
  has no `*slog.Logger` field, its constructor takes none, and it writes no record on any path at any level
  — the posture `internal/relay/v2session_settings.go` and `internal/sessions/pool.go` restate and all three
  siblings enforce the same way. The only log record anywhere on this path is `sinkFor`'s closer-arm Warn,
  outside this diff, carrying `eventKind(ev)` and the session id: the variant name and nothing claude
  authored. The producer contributes nothing either — `decodeModelWindows` discards its decode error
  unlogged precisely because `encoding/json` quotes offending input bytes into its error text. The single
  edit that would reopen the channel is giving this type a logger to write a retention diagnostic.
- **[Concurrency] No findings.** One leaf mutex, never held across the call to `next`, participating in no
  ordering with `Pool.mu`, `Session.lcMu` or `capMu` — so there is no multi-lock order to document or keep
  consistent. No TOCTOU on shared state: the store predicate tests the incoming EVENT, a local, never the
  retention, so there is no check-then-mutate window; and the read holds the lock across the clone, so no
  reader can observe a torn `Windows`/`Dropped` pair. The write-side clone is computed OUTSIDE the critical
  section, leaving one assignment under the lock. No goroutine is spawned, so none can leak, and no state
  reaches disk, so a signal mid-write leaves nothing partial to recover.
- **[Threat model alignment] OUT OF SCOPE, named.** `docs/protocol-mobile.md` § Security model is not
  reached by this ticket: `turnbridge`'s `MapEvent` builds `protocol.TurnEndPayload` from `ConversationID`,
  `TurnID` and `StopReason` field by field rather than embedding the variant — verified 2026-09-05 — so
  `ModelWindows` has never touched the wire, and this design widens neither the variant nor the envelope.
  Publication belongs to #2107 under #2102, which inherits the sanitization obligation of finding 1 the
  moment it publishes a model id to a client.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-05
