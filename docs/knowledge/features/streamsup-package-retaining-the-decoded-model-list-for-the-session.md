# Retaining the decoded model list for the session (#1840)

_See also: [the twin retention for the slash-command list](#retaining-the-decoded-slash-command-list-the-twin-2004) and
[the third application, for the background-task roster](#retaining-the-decoded-background-task-roster-the-third-application-2077), below._

`emitModelList` (above) mints one `turnevent.ModelList` per child and hands it to the parser's sink —
but that sink is `sink.sinkFor(cfg.SessionID)`, the droppable-class send into the turn-busy fan-in
(`turnMarkFor`'s default arm answers `turnMarkNone` for `ModelList`), and one `initialize` reply per
child means no later event ever replaces a copy lost there. `newSessionParser` (`cmd/pyry`) closes that
gap by minting the parser and a `sessionModelHold` together from one call, so the two halves can't be
wired to different holds: the hold's `Sink` method decorates `sink.sinkFor`, storing a `ModelList`
**before** forwarding every event unchanged downstream. Storing happens inside the decorator, which sits
on the parser's side of the channel entirely — the hold is never a candidate for the `droppableCap`
refusal, whatever it does internally. `streamRunner` carries the hold and exposes `ModelList()
(turnevent.ModelList, bool)` as a concrete method, off `sessions.Runner` (the fourth, after
`Interrupt`/`RestartFresh`/`BeginRotation` — consumer is `cmd/pyry`, not `internal/sessions`, so the
interface-placement rule argues against widening here too). `have` (a bool, not `len(Models) == 0`)
represents "nothing reported yet" as its own state, because the producer's `Models` is documented never
empty and no fixture can construct the state any other way. The hold has no logger field and no
constructor parameter for one, so `Value`/`ResolvedModel`/`DisplayName`/effort levels have no path to a
log record — the #833 posture enforced by construction, not by review. `Session.sup` is assigned once
and never reassigned, so the hold's lifetime is the session's; there is no session-keyed registry to grow
or prune. #1837 is the intended reader, via `Session.Runner()` and a type assertion.

**`Sink`'s shipped doc justifies its mutex with a writer-overlap race that doesn't happen — code review
flagged it as a SHOULD FIX and it shipped uncorrected anyway, since the finding didn't block merge.** The
claim is that a respawn's new stdout-forwarder goroutine can briefly overlap the outgoing one during
teardown. It can't: `spawnAndWait`'s single call site blocks on `cmd.Wait()`, which `os/exec` documents
as joining the goroutine copying the child's stdout into a non-`*os.File` `Stdout`, before
`spawnAndWait` returns — so forwarder N+1 cannot start until forwarder N has already finished. Were it
true, `streamsup.Parser`'s own doc ("`Write`ing — hence `buf` and the sink calls — is only ever invoked
serially from that one goroutine, across every respawn") would be describing a live data race in a file
this ticket never touched. The mutex is still genuinely required — for the *reader* (#1837's publisher,
concurrent with the sole writer), not for two writers that Go's own stdlib already serializes. Whoever
next edits `sessionModelHold`'s doc comment should fix the justification, not just trust that a passed
review means the prose is accurate. The general shape, for any future doc claiming "goroutine A can
overlap goroutine B": check what actually joins A before B starts — `cmd.Wait()` is one such join point
in this codebase, and it silently falsifies any respawn-overlap claim built on top of it.

**Store-before-forward inside `Sink` is not what keeps the retention off the droppable send — being a
decorator upstream of the channel at all is what does it, and a test can tell the difference.** A mutant
that swaps the two statements (forward first, store second) still passes every test in the suite,
`TestSessionModelHold_RetainsPastASaturatedSink` included: a refused channel send consumes nothing, so
storing "after" a refusal still runs. What that test actually pins is that retention doesn't depend on
the fan-in *admitting* the event — which is what rules out a retention point downstream of the channel —
and no finer. A decorator whose store step can itself block or panic before reaching `next` would be a
real ordering bug; statement order within an already-synchronous `Sink` is not the property to write the
test's name around.

**The live lane gained its own consumer of this event, separately from the retention (#1849).**
`interactiveTurnEmitterV2.Handle` (below) now carries a `turnevent.ModelList` case, forwarding the same
`ev` `Sink` already retained straight through `emitMapped` to every interactive conn — no lifecycle
mutation, no read of the hold. That closes the routed-conversation case only: a bootstrap child at daemon
start still drops the frame at the no-cursor guard (cursor is `""` until a message routes), and the
fan-in can still refuse the frame under load since `turnMarkFor` answers `turnMarkNone` for it. Both are
exactly the losses this retention exists to survive; reading `streamRunner.ModelList()` back for a client
that connects or reconnects afterward was #1846, re-cut twice (→ #1857/#1858 → #1863/#1864 → #1867/#1868). #1863 shipped the relay-side seam nil in production; #1867 wired a real read of the hold
into it (`retainedModelLists`, [v2-session-manager.md](v2-session-manager.md)), not a second read of the
hold from the live lane. **#1868** (open) is the cross-process e2e proof.

**Test-writing lesson: a `cmd/pyry` fixture-builder name collides silently across files in the same
package.** `session_model_hold_test.go` (#1840) already defines a `modelListFixture` builder function; #1849's emitter tests reaching for the same obvious name for the same `ModelList` shape hit a compile
error that reads like a type error (`modelListFixture(...) — not a function`) rather than what it is, a
same-package name collision across files. Prefixing a package-level test fixture with the consumer it
belongs to (`emitterModelListFixture`, for the emitter tests) is what keeps siblings in one ticket
sequence from colliding on the name each reaches for first.

## Retaining the decoded slash-command list, the twin (#2004)

`sessionSlashCommandHold` applies the identical placement to `turnevent.SlashCommandList`: a second
sink decorator, chained beside `sessionModelHold` inside `newSessionParser` (which now mints and wires
both in one call, so neither half can end up bound to the other's hold), storing before forwarding on
the parser's side of `sinkFor`'s droppable send. `streamRunner` exposes the read as a fifth concrete
method off `sessions.Runner`, alongside `ModelList`, for the same interface-placement reason: the
consumer (#2005) is in `cmd/pyry`, not `internal/sessions`.

Two things differ from the sibling rather than being copied from it, deliberately:

- **The clone is three levels, not two.** `Commands`, and within each entry both `Aliases` and
  `TruncatedFields`, all need `slices.Clone`; `DroppedCommands` is an int and copies by assignment.
  This variant has no top-level `TruncatedFields` field the way `ModelList` does — the count dimension
  is reported per entry instead — so the level `cloneModelList` clones does not exist here to clone.
- **The unreported-state bool has a different justification.** `ModelList.Models` is documented "never
  empty"; `SlashCommandList` has no such doc guarantee. What makes an empty list unreachable is the
  *producer*: `emitSlashCommandList` returns before emitting on a zero-length entry list (#1877), so no
  reader can ever be handed an empty-but-reported list. Copying the sibling's "never empty" justification
  across without checking would have cited a doc guarantee that doesn't exist on this type.

**Read the package overview before trusting a ticket's own technical notes — it may already have
disproven them.** #2004's ticket body repeated this file's own already-corrected mutex justification
("a respawn's new stdout-forwarder goroutine can overlap the outgoing one") almost verbatim, unaware
this doc had measured it false three paragraphs above. The build caught it by reading this file first
and wrote the correct premise (needed for the *reader*, concurrent with the sole serial writer, not for
two writers) on the new type — and, since it was already touching the sibling file, corrected the one
sentence here too. The general shape: a ticket's Technical Notes are written by a refiner working from
the ticket text, not from this file, so a claim here that already contradicts a shipped comment can
still get re-asserted by the next ticket that copies that comment's reasoning instead of this file's.

**A security review's capped-string footprint claim needs the same care as the code, and #2004's
shipped one is still wrong.** `docs/specs/architecture/2004-retain-slash-command-list.md`'s
`## Security review` states the retained `SlashCommandList` costs "O(one capped list) per session,"
computing that from the per-field caps' product. It doesn't: `truncateField`'s
`strings.ToValidUTF8(s[:limit], "")` returns its input slice unchanged for already-valid UTF-8 (Go's
`strings` package makes no copy in that case), so a "capped" field is a slice header into the *whole*
pre-cap `json.Unmarshal` line — the real bound is one parse line under `defaultMaxParseBuf` (4 MiB),
not the cap product (~164 KB), a ~25x understatement code review flagged as a non-blocking SHOULD FIX.
The same aliasing applies to `sessionModelHold`'s retained `ModelList` for the identical reason — its
strings are truncated through the same helper — so this file's own footprint reasoning inherits the
same correction if it is ever written down explicitly. `slices.Clone` on the read side does not fix
this: it copies the slice header, not the string's backing bytes, since Go strings are immutable and
`slices.Clone` of a `[]string` copies pointers-and-lengths, not string contents. #2077's own security
review derives the identical one-parse-line bound for the third retained type,
`turnevent.BackgroundTaskRoster`, correctly and from the outset — the ~25x understatement has not
recurred a third time.

**Two stale ticket-number references, corrected by #2077.** `sessionModelHold.Sink`'s doc and the
compile-time assertion comment in `streamsup_runner_test.go` both cited `#1867` as "the shape the
publisher reaches by type assertion." The actual production type assertion (`resolveBoundModelList`)
shipped in **#1857**; `#1867` is `retainedModelLists`, which calls that resolver, not the assertion
site itself. #2004's branch had introduced the wrong number (rewriting both comments from the correct
`#1837` to the incorrect `#1867`) while fixing the mutex-justification sentence next to one of them —
an unrelated, undeclared edit code review caught as a non-blocking SHOULD FIX at the time. This
package overview nominated the next editor of either file to fix both sites, and #2077, adding a
third sink decorator to the same chain, was that editor: both now cite `#1857`.

## Retaining the decoded background-task roster, the third application (#2077)

`sessionBackgroundTaskHold` applies the identical placement to `turnevent.BackgroundTaskRoster`: a
third sink decorator, chained beside `sessionModelHold` and `sessionSlashCommandHold` inside
`newSessionParser`, storing before forwarding on the parser's side of `sinkFor`'s droppable send.
`streamRunner` exposes the read as a sixth concrete method off `sessions.Runner`, for the same
interface-placement reason as its two siblings — the consumer is #2079's resolver, in `cmd/pyry`, not
`internal/sessions`.

Two things diverge from both siblings, and neither transfers by analogy:

- **`have` is load-bearing here, not merely tidier.** Both siblings make an empty retained value
  unreachable (`ModelList.Models` documented never empty; `emitSlashCommandList` returns early on a
  zero-length list, #1877), so `len(x) == 0` as a stand-in for "unreported" would merely be redundant
  there. `emitBackgroundTaskRoster` emits for an empty `tasks` array on purpose — claude positively
  reporting that nothing is alive — so `have` is the *only* thing separating that from "this session
  has reported no roster at all," and a mutant collapsing the two reddens four test arms. This is
  provable only through the real decoder: a hold-level unit test can construct the reported-and-empty
  state directly, but only a wiring test that decodes an actual `"tasks":[]` line shows the producer
  really reaches that state rather than returning early the way its sibling does.
- **`DroppedTasks` rides the retained value.** It's the roster's only truncation report — this variant
  has no top-level `TruncatedFields` the way `SlashCommandList` does — so a clone that dropped it would
  let a capped roster read back as a whole one.

**A refusal whose enforcement is "this component remembers nothing" needs its scope restated the
moment anything upstream starts remembering.** `emitBackgroundTaskRoster`'s refusal paragraph
(`internal/streamsup/parser.go`) makes parser amnesia the enforcement mechanism for the family's
no-synthesized-finish rule; that claim stays true of the parser, but its closing clause — detecting a
task's disappearance would require remembering the previous roster — read as a daemon-wide
impossibility argument until #2077 became the first ticket to hold a previous roster and a newer one
at the same instant, one layer up. The dated addendum narrows the claim rather than withdrawing it,
and states the actual rule explicitly on the new type instead of leaving it implicit: replace, never
diff — one roster retained, never a pair. `turnevent.BackgroundTaskRoster`'s own type doc needed no
edit: it is scoped to the event family rather than the parser, and its "diffing successive snapshots
is a legitimate thing for a *consumer* to do on its own terms" survives verbatim, since the hold makes
no inference.
