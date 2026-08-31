# 1878 — Pin the emitted slash-command inventory: cap boundary, committed capture, and the unchanged record

## Files to read first

This is the turn-1 data load. Every entry names a **symbol**, never a line — resolve
each with `codegraph_search` / `codegraph_node`, and read the symbol's whole doc block.
Four of the five test symbols below are *extended* rather than written fresh, and each
one's doc comment already states which claim it owns; over-writing a claim a
neighbouring test already pins is the failure mode this list exists to prevent.

| Where | Symbol | What to extract |
|---|---|---|
| `internal/streamsup/parser.go` | `emitModelList` | The four-rung classification, and specifically **where** the `SlashCommandList` block sits: below `logControlResponse`, below the `ModelList` emit, below rung 3's return. Every suppression row in this ticket is a fixture that would disagree if that block moved. Nothing in this function changes. |
| `internal/streamsup/parser.go` | `truncateField` | The exact behaviour every AC 1 row asserts: `<=` boundary, byte cut, and `strings.ToValidUTF8(…, "")` — a **deletion**, not a replacement. This is why a mid-rune cut comes back short. |
| `internal/streamsup/parser.go` | `maxSlashCommandName` | The production constant. Read it to understand the bound; **never build a fixture from it** — see § Reading the cap. |
| `internal/streamsup/parser.go` | `logControlResponse` | The six attributes, their types, and the closed reason set. AC 4's `wantAttrs` maps are unchanged; read this to confirm that rather than to change it. |
| `internal/turnevent/event.go` | `SlashCommand` | The two fields, and `TruncatedFields`' nil-not-empty convention — the distinction every AC 1 row asserts with `reflect.DeepEqual` rather than a length check. |
| `internal/turnevent/event.go` | `SlashCommandList` | Its `Commands` doc, which states that suppression is the producer's decision and that this field never carries zero entries. AC 3 is that statement's proof. |
| `internal/streamsup/parser_test.go` | `TestParser_SlashCommandNamesAreCapped` | **AC 1's home.** Read its doc block whole: it names exactly which rows it already carries and which ones it left to this ticket. |
| `internal/streamsup/parser_test.go` | `TestParser_ModelListFieldsAreCapped` | The table idiom for a cap test in this package, and its `twoByteRune` mid-rune row — the shape AC 1's mid-rune rows copy one array over. |
| `internal/streamsup/parser_test.go` | `TestParser_InitializeControlResponseCountsTheCapturedCommands` | **AC 2's home.** Its capture-shape guards run first and name the arm; its `wantAttrs` map stays untouched. |
| `internal/streamsup/parser_test.go` | `TestParser_InitializeControlResponseDecodesTheCapturedModels` | Read it, change **nothing**. Its per-entry byte-for-byte model comparison is AC 2's "model list unchanged" half — do not re-copy it and do not extend it. |
| `internal/streamsup/parser_test.go` | `TestParser_ModelListIsLoggedContentFree` | **AC 4's home.** Its five parallel structures (`lines`, `wantReasons`, `wantCounts`, `wantCommandCounts`, `leaks`) all grow together; getting them out of step is the realistic way this edit goes wrong. |
| `internal/streamsup/parser_test.go` | `TestParser_InitializeControlResponseAckReportsTheCommandCount` | Read it, change **nothing**. Its `commands and no models` row is AC 3's ack pin — see § AC 3 for why the new table does not restate it. |
| `internal/streamsup/parser_test.go` | `commandEntryFixture`, `initializeLineFixture`, `modelEntryFixture`, `collectEvents` | The four builders every new row composes. `commandEntryFixture` takes an `any`, so the JSON-`null` name row needs no new helper. **No new fixture builder is needed anywhere in this ticket.** |
| `internal/streamsup/parser_test.go` | `slashCommandNameCapFixture` and the const block it lives in | The hand-written cap literal and the comment stating why it must stay one. |
| `internal/streamsup/parser_test.go` | `capturedCommandEntries`, `capturedCommandString`, `commandNameIsPlainSlug`, `capturedInitializeLine` | The capture readers AC 2 and AC 4 use. `commandNameIsPlainSlug` is #1853's anonymous charset guard — AC 2 adds a named pin beside it rather than replacing it. |
| `docs/knowledge/features/streamsup-package.md` | the **"Declaring `commands` alongside `models` (#1853)"** and **"Producing `turnevent.SlashCommandList` (#1877)"** blocks, both inside § `Turn I/O — envelope write + stdout parser (#1088)` | Two lessons this ticket must apply rather than rediscover: the *gate-placement* testing lesson (a suppression fixture must make the two placements disagree) and the corrected *sink-reachability* enumeration. Read-only: the documentation phase owns this file. |

## Context

#1877 merged (PR #1879). `emitModelList` is now a producer: on rung 4 — `subtype ==
"success"` **and** a non-empty decoded `models` array — it emits one
`turnevent.SlashCommandList` behind the existing `turnevent.ModelList` whenever the
decoded `commands` array is non-empty, each entry's `name` copied under
`maxSlashCommandName` via `truncateField`.

#1877 shipped that with its own liveness proof — one over-cap entry and one that fits
— so the bound was never unproven for a merge window. **It did not ship the matrix.
This ticket is the matrix.**

**This ticket is TEST-ONLY.** Zero production files. It falsifies no `//` claim and
corrects no prose, because #1877 moved every falsified claim with the code that
falsified it. `internal/streamsup/parser.go`, `internal/turnevent/event.go` and
`cmd/pyry/interactive_turn_v2.go` are not touched. If a doc correction looks necessary
while writing these tests, that is a defect in #1877 to **report on the ticket**, not
to fix in passing.

**No ADR is warranted.** Nothing is decided here that is not already decided in
`emitModelList`'s and `turnevent.SlashCommandList`'s own doc comments; this ticket
turns those decisions into failing tests.

### Why the matrix, stated as what a missing row lets through

Every string in this inventory is **workspace-authored** — a command defined in a
repository, written by whoever wrote that repository, read by this daemon in whatever
directory the operator points a session at — and the arm is live rather than latent:
`RequestInitializeOnSpawn` fires the ask once per spawned child and
`mapStreamsupConfig` sets it true. The bound on that text is one `truncateField` call
in one loop. Without the rows below, these edits ship green:

| Edit | Which row reddens it |
|---|---|
| halve `maxSlashCommandName` | AC 1's **exactly at the cap** row (nothing else: the over-cap row follows the constant) |
| `<=` → `<` in `truncateField`'s guard | AC 1's **exactly at the cap** row |
| scrub with `"�"` instead of `""` | AC 1's **mid-rune** rows |
| skip entries with an empty `name` | AC 1's **absent-name** row (by position, not only by count) |
| hoist the emit block above rung 3's return | AC 3's **ack** row (pinned in the existing ack test) |
| hoist it above the subtype gate | AC 3's **nak** row |
| hoist it above the undecodable return | AC 3's **undecodable** row |
| emit an empty list instead of suppressing | AC 3's three in-rung-4 rows |
| log the cut name at the truncation site | AC 4's over-cap command sentinel |
| add `"err", err` on the undecodable rung | AC 4's undecodable-rung command sentinel |

### Sizing

**0** production files, **0** new exported types, **0** new constants outside the test
file's sentinel block, **4** acceptance criteria, **0** consumer call sites, **0** new
error branches. Estimated total written work, counting doc comments (which run
15–30 lines per test in this file), fixtures, rows and assertions:

| Work item | Lines |
|---|---|
| W1 — AC 1: `TestParser_SlashCommandNamesAreCapped` → table | ~140 |
| W2 — AC 2: extend the captured-commands test | ~50 |
| W3 — AC 3: new suppression table | ~95 |
| W4 — AC 4: extend the content-free sweep | ~50 |
| **Total** | **~335** |

Inside the 400-line boundary, one file, no production cascade. Ships as `size:s`.

## Design

Four work items, all in `internal/streamsup/parser_test.go`. Nothing else in the tree
is created, modified or deleted.

### Reading the cap

Every boundary string is built from **`slashCommandNameCapFixture`** — the test-side
constant — never from the production `maxSlashCommandName` and never from a bare number
written into a row.

It is a hand-written literal on purpose. A fixture reading the production constant
would follow it green if someone halved the cap, and halving the cap is precisely the
edit the exactly-at-the-cap row exists to redden. The const block's own comment states
this rule for the model caps and #1877 extended it to this one.

### Diagnostics — a stated requirement, not a nicety

Several rows compare 253–256-byte strings. A bare `t.Errorf("got %q, want %q", …)` on
those produces two screens of `a`s and is unreadable, which turns one red row into
several developer turns. Every assertion on a long name must report **lengths first**
and, when it prints values at all, a bounded prefix. Concretely, per entry: assert
`len(got.Name)` against the expected length with its own message, then the exact string
equality with a message that names the row and the index rather than dumping both
values in full.

### W1 — AC 1: the cap boundary, as a table

Extend `TestParser_SlashCommandNamesAreCapped` into a table-driven test. **Keep the
name.** Verified this pass: no `//` comment, no doc and no other test cites it, so a
rename would be free — but it would buy nothing and the rows below are all about what
the per-entry name transform does at and around the cap.

Each row supplies the `commands` array; the test wraps it with a one-entry `models`
array (via `modelEntryFixture`) and `initializeLineFixture`, because the commands emit
rides the model-list rung. A row therefore never states the models half.

Row-invariant assertions, in this order — the order is load-bearing:

1. `len(events) == 2`, `events[1]` is a `turnevent.SlashCommandList` (`Fatalf`).
2. `len(list.Commands)` equals the row's expected entry count (`Fatalf`). **This runs
   before any index.** A producer that dropped an entry must fail here rather than
   panic below — the cap cuts a NAME, never an ENTRY, and #1877's test already
   establishes that ordering.
3. Per index: the expected `Name` (length first, then equality — see § Diagnostics) and
   the expected `TruncatedFields` by `reflect.DeepEqual`, so `nil` and `[]string{}`
   stay distinguishable.

The rows. `cap` below is `slashCommandNameCapFixture`; `A(n)` is `strings.Repeat("a", n)`.

- **over the cap, and a name that fits** *(both rows exist today — carry them across
  unchanged)*. Entries: `A(cap+1)`, `"deep-research"`. Expect `{A(cap), ["name"]}` then
  `{"deep-research", nil}`. The second entry is what makes the first non-vacuous
  against a producer that names `"name"` on every entry, and it is simultaneously the
  "a cut on one entry does not appear on the entries after it" pin.
- **exactly at the cap** *(new — the row the ticket exists for)*. Entries: `A(cap)`,
  `"design"`. Expect `{A(cap), nil}` then `{"design", nil}`. **This is the only row in
  the tree that reddens on a halved cap or on `truncateField`'s `<=` becoming `<`.**
  The over-cap row cannot: its input is built from the same fixture, so it follows the
  constant down and stays green. Say that in the row's `why`.
- **mid-rune cut, two-byte rune** *(new)*. Entry: `A(cap-1) + "é" + "z"` (258 bytes at
  `cap == 256`). Expect `{A(cap-1), ["name"]}` — the cut lands inside the two-byte
  rune, the scrub **deletes** the partial rune, and the value comes back one byte under
  the cap while still reporting the cut. Verified against `truncateField` this pass:
  255 bytes, all `a`, `truncated == true`.
- **mid-rune cut, four-byte rune** *(new)*. Entry: `A(cap-3) + "😀" + "z"` (258 bytes).
  Expect `{A(cap-3), ["name"]}` — 253 bytes, three under. This row is what makes the
  stated "1–3 bytes under the cap" a **range** rather than a one-byte anecdote, and it
  is the row a `"�"` scrub fails most visibly. Verified this pass.
- **absent name, both spellings** *(new)*. Entries: `commandEntryFixture("")`,
  `commandEntryFixture(nil)`, `commandEntryFixture("design")`. Expect `{"", nil}`,
  `{"", nil}`, `{"design", nil}`. One row rather than two, because the claim is that
  the two absence spellings decode **alike**: `""` and JSON `null` both arrive as `""`,
  both are still entries, and the trailing real name pins their positions. A producer
  that skipped empty names emits two entries and fails at assertion 2. Note in the
  row's `why` that the gate is on the **array** length, not on any entry's content —
  per-entry values are never validated beyond the cap, absence being claude's to choose.

Do **not** add a row asserting that an over-cap name is dropped, reordered, lowercased
or trimmed; #1600's verbatim rule is carried by the "fits" row's exact equality.

### W2 — AC 2: the committed capture, all fifty-one names

Extend `TestParser_InitializeControlResponseCountsTheCapturedCommands` — do not stand a
new test beside it. It already runs per responding arm, already guards the capture's
shape first, already asserts `events[0]` is the `ModelList` and `events[1]` is the
`SlashCommandList`, and already compares the entry count against the capture's own
array length. Three changes:

- **Length check becomes fatal, then a per-index loop.** Today the count mismatch is a
  `t.Errorf`; it must become a `Fatalf` so the loop below cannot index past the end.
  Then, for each index, compare `list.Commands[i].Name` against
  `capturedCommandString(t, want[i], "name")` — the capture's own bytes, never a
  transcription — and assert `list.Commands[i].TruncatedFields == nil`. Index `i` on
  both sides is what pins **claude's order** as well as the values, exactly as the
  models test's loop does.
- **`__remote-workflow`, pinned by name.** Assert that the emitted names contain the
  literal `"__remote-workflow"`, failing with a message saying what it is: the
  committed proof that a name outside `[a-z0-9-]` crosses the daemon verbatim, and the
  reason no charset may be assumed. Written as a literal deliberately — riding
  anonymously inside a 51-element comparison, it disappears the moment a re-capture
  swaps it out, and the failure a reader would then see says nothing about charsets.
  Keep `commandNameIsPlainSlug`'s existing anonymous guard beside it: it carries the
  general class claim, this pin carries the specific one.
- **`TruncatedFields` nil on every entry.** The capture's longest name is 24 bytes
  (`fewer-permission-prompts`), an order of magnitude under the cap, so a report here
  can only mean the cap fired on a value that fits. Mirrors the models test's own
  untruncated-path assertion.

Explicitly unchanged in that test: `wantAttrs` and all six of its values, the
fifty-one-entry guard, the `aliases` guard, the `sawNameOutsideSlug` guard, and the
`models: 6` value that kills an argument swap at the `logControlResponse` call site.

Explicitly unchanged **elsewhere**: `TestParser_InitializeControlResponseDecodesTheCapturedModels`.
Its per-entry byte-for-byte model comparison is AC 2's "the model list still arrives
first, unchanged" half. Do not re-copy it, do not extend it, do not touch it.

Measured against the committed bytes this pass, so the developer does not have to
re-derive them: all three responding arms carry a byte-identical 51-entry `commands`
array (same SHA-256), `name` present and non-empty on every entry, longest 24 bytes,
`__remote-workflow` the sole name outside `[a-z0-9-]`. The fourth arm
(`initCaptureArmNoRequest`) recorded no `control_response` and is skipped by the
existing arm loop.

### W3 — AC 3: the suppression table

One new test — suggested name `TestParser_SlashCommandListIsSuppressed`, in the file's
claim-sentence idiom. Table-driven, five rows, each supplying a whole line.

Row-invariant assertions:

1. `len(events)` equals the row's expectation.
2. **No event in the slice is a `turnevent.SlashCommandList`** — a type scan over the
   whole slice, not an index check. This is the direct statement of the claim and it
   holds whatever the count is.
3. When the row expects one event, `events[0]` is a `turnevent.ModelList`.

The rows:

| Row | Line | Expect |
|---|---|---|
| `commands key absent` | rung 4: non-empty `models`, no `commands` key | 1 event, the `ModelList` |
| `commands null` | rung 4: non-empty `models`, `"commands": nil` | 1 event, the `ModelList` |
| `commands empty array` | rung 4: non-empty `models`, `"commands": []any{}` | 1 event, the `ModelList` |
| `nak rung, non-empty commands` | `initializeLineFixture(t, "error", …)` carrying **both** a non-empty `models` and a non-empty `commands` | 0 events |
| `undecodable rung, valid commands` | `subtype: "success"`, `"models": 5` (a number where the array belongs), `"commands"` a **valid non-empty** array | 0 events |

The first three are the decode's own collapse made visible: `controlResponseLine.Commands`
is a plain slice precisely so that an absent key, a JSON null and a published `[]` are
one reading, and each must reach the gate and suppress. State in the table's doc that
this is the collapse being pinned, not three separate behaviours.

Precise, because a reader will otherwise assume more than is true — verified against
`encoding/json` this pass: an absent key and a JSON `null` both leave the field **nil**,
while a published `[]` leaves an **empty non-nil** slice. The gate is `len(…) == 0`, and
that is what collapses all three. The rows therefore pin the **gate's** reading, not a
claim that the three shapes are indistinguishable in the decoded struct.

The last two rows follow the package overview's **gate-placement lesson** verbatim, and
that is why their fixtures look over-specified. A suppression fixture proves an ordering
only if the two placements disagree on it:

- The nak row carries a **non-empty** `commands` **and** a non-empty `models`. A nak row
  with an absent or empty array reads zero events under a mutant that hoisted the emit
  above the subtype gate, and proves nothing.
- The undecodable row's `commands` array is **well-formed and non-empty**, and the
  decode failure is in the `models` half. A row whose `commands` was itself the
  malformed value would leave a hoisted emit with nothing to emit. As written, a mutant
  that moved the block above the undecodable return has a populated array available to
  it and reddens. Verified this pass rather than assumed: `encoding/json` records the
  type error and keeps decoding, and `initializeLineFixture` marshals a map — so the
  keys emerge alphabetically, `commands` decodes **before** `models` fails, and
  `Commands` comes back holding its entry alongside a non-nil error.

**The ack rung is the sixth row and it lives in
`TestParser_InitializeControlResponseAckReportsTheCommandCount`**, whose `commands and
no models` row already carries three entries, asserts zero events, and asserts the
record's six attributes besides — a strictly stronger assertion than this table's. Do
**not** restate it here. Name it in this test's doc comment as the row that completes
the table, and say that reddening it means the ack rung's classification was changed,
which is #1876's scope and not this ticket's.

### W4 — AC 4: the record, swept on every rung

Extend `TestParser_ModelListIsLoggedContentFree`. Its structure stays: one `lines`
slice, one record per line, `reflect.DeepEqual` against an exact six-key `wantAttrs`,
and a `leaks` sweep over every record's message and every attribute value.

What is missing today is a **command-name sentinel** on any rung but the captured one,
and the captured names are all short — so no line in the tree exercises the truncation
path through the sweep. Add four lines (existing lines are untouched, per "extends
rather than replaces") and one sentinel per new rung:

| # | Line | reason | models | commands |
|---|---|---|---|---|
| 1 | *(existing)* synthetic model-list, three model sentinels, no `commands` | `model_list` | `1` | `0` |
| 2 | **new** synthetic model-list + `commands` = [a fitting sentinel name, an over-cap sentinel name] | `model_list` | `1` | `2` |
| 3 | *(existing)* the captured line | `model_list` | `6` | `len(capturedCommands)` |
| 4 | *(existing)* nak line carrying `nakSentinel` | `nak` | `0` | `0` |
| 5 | **new** nak line carrying a `commands` array with a sentinel name | `nak` | `0` | `0` |
| 6 | *(existing)* undecodable line (`models` a number) | `undecodable` | `0` | `0` |
| 7 | **new** undecodable line (`models` a number) carrying a `commands` array with a sentinel name | `undecodable` | `0` | `0` |
| 8 | *(existing)* ack line (empty `models` array) | `ack` | `0` | `0` |
| 9 | **new** ack line: no `models`, `commands` = [a sentinel name] | `ack` | `0` | `1` |

Consequences to carry through, all four parallel structures at once:

- **Event count moves from 3 to 5.** Only line 2 adds events, and it adds two: a
  `ModelList` and a `SlashCommandList`. Lines 5, 7 and 9 emit nothing, which is their
  rungs' whole point.
- **Record count moves from 5 to 9** — the assertion is already `len(all) != len(lines)`,
  so it moves on its own.
- `wantReasons`, `wantCounts` and `wantCommandCounts` gain four entries each, per the
  table above. The `commands` column is the one worth re-deriving rather than guessing:
  it is `0` on nak and undecodable **because the count is taken below the success gate**,
  even though lines 5 and 7 carry non-empty arrays. That is `logControlResponse`'s
  documented placement and these two rows are its proof on this path.
- `wantAttrs` keeps its six keys, their names and their values. Nothing in this ticket
  changes a record, so nothing in it changes a `wantAttrs` keyword — the reason set
  stays #1876's.

The sentinels — one per rung so a failure names the rung, all added to `leaks`:

- The **over-cap** sentinel must carry its distinctive prefix at the **front**, padded
  with `a`s past the cap. If the sentinel sat at the end, the cut would remove it and
  the sweep would go silently vacuous on exactly the path it exists to cover. State this
  in the constant's comment.
- Give the over-cap sentinel's **prefix** to `leaks`, not the padded name: a leak of the
  cut value must match too.
- Line 2's `models` entry uses plain `modelEntryFixture` values, not the model
  sentinels, so a failure unambiguously names which line leaked.

Line 7 is the highest-value addition and its comment should say why: `encoding/json`
quotes the offending input bytes into its error text, so an `"err", err` added to the
undecodable rung would route **workspace-authored command names** into the daemon log
through a channel no per-attribute check can see. `emitModelList`'s undecodable arm
argues that rule in prose; line 7 makes it a red test.

## Concurrency model

Unchanged, and out of scope. No goroutine, no shared state, no lock is added or
observed. `Parser.emit` is the existing synchronous callback and every event a row
observes has already been appended by the time `Write` returns. Every new test carries
`t.Parallel()` at both levels, matching this file throughout; the fixtures share nothing
mutable, and `logRecorder` is per-subtest.

## Error handling

No new error paths — this ticket adds no production code. The only failure modes are
test-side, and two need naming because getting them wrong is how the developer loses
turns:

- **Index-before-length.** Every per-entry loop must be preceded by a `Fatalf` on the
  entry count. A row that indexes first turns a producer bug into a panic and a useless
  failure message.
- **Unreadable comparisons on long names.** See § Diagnostics. Lengths first, bounded
  prefixes, never a bare `%q` of a 256-byte string.

`truncateField` cannot fail; it returns a value and a bool. Its scrub deleting rather
than replacing a partial rune, and a scrub removal not being reported as a truncation,
are its documented behaviour — inherited, asserted here, not restated in prose.

## Testing strategy

Everything runs inside `make check`. `internal/streamsup` carries no build tag and
`capturedInitializePayload` reads the committed captures as bytes, which is #1810's
whole reason for existing — no real-claude run is involved and no credential is needed.

- `go test -race ./internal/streamsup/...` is the inner loop.
- `make check` before commit — it runs `cite-guard` as a prerequisite, and a line-number
  citation in a new `//` comment fails it at any depth, with no range exemption. Every
  cite in every new comment names a **symbol**. This family's cites went stale twice in
  three days as siblings merged.
- **No test outside `internal/streamsup/parser_test.go` may change.** If
  `TestParser_InitializeControlResponseDecodesTheCapturedModels`,
  `TestParser_InitializeControlResponseAckReportsTheCommandCount`, or either
  `cmd/pyry` slash-command test reddens, a scope boundary was crossed — production
  behaviour changed, and this ticket changes none.

## Open questions

- **Nothing blocking.** Every row's expected value is derived above from the committed
  bytes or verified against `truncateField` this pass.
- **Row placement within W1 is the developer's call.** The five bullets are the required
  coverage; whether the two mid-rune rows carry a trailing second entry (they need not —
  the first row already pins the no-bleed property) is a readability judgement.
- **The new test's name in W3** is a suggestion. Any name in the file's claim-sentence
  idiom is fine, provided its doc comment names the ack test as the sixth row's home.

## What is deliberately out of scope

- **No production file.** Not `parser.go`, not `event.go`, not
  `cmd/pyry/interactive_turn_v2.go`. A doc correction that looks necessary is a #1877
  defect to report on the ticket.
- **No `wantAttrs` keyword, count or value changes.** The record is unchanged by this
  ticket, which is what keeps the `reflect.DeepEqual` maps outside it. The reason set and
  the ack rung's classification are #1876's.
- **No entry-count cap row and no `DroppedCommands` assertion** — that bound is #1826's
  and does not exist yet. The emitted count equalling the decoded count is asserted
  through the capture's own array length, not through a cap.
- **No publish assertion.** `turnbridge` has no arm and `Handle` no case; that is
  #1720's. `TestInteractiveTurnEmitterV2_SlashCommandListIsNotPublished` stays green
  untouched.
- **No knowledge-base doc.** `docs/knowledge/features/streamsup-package.md` belongs to
  the documentation phase; read it, never write it.

## Security review

**Verdict:** PASS

This ticket adds no production code and no new data path. The review below therefore
asks a different question from #1877's: **does the test matrix actually bound what it
claims to bound, and does the matrix itself introduce any exposure?**

- **[Trust boundaries] No findings — and the matrix is what converts #1877's bound from
  prose into a gate.** The untrusted input is claude's `control_response` line, decoded
  from **top-level** bytes by `emitModelList`; every string in the `commands` array is
  workspace-authored, a *lower*-trust origin than claude's own, and the arm is live
  (`RequestInitializeOnSpawn` fires once per spawned child, `mapStreamsupConfig` sets it
  true). The single mechanism bounding that text is one `truncateField` call. Before
  this ticket the only proof of that bound was one over-cap row whose input is built
  from the cap fixture — so **halving `maxSlashCommandName` was a green edit.** AC 1's
  exactly-at-the-cap row closes that, and § Context's table records it as the row's sole
  purpose so a later reader does not delete it as redundant with the over-cap row.

- **[Error messages, logs, telemetry] SHOULD FIX, and it is fixed in the spec — the
  existing leak sweep is vacuous on the truncation path.**
  `TestParser_ModelListIsLoggedContentFree` sweeps a captured command name, but every
  captured name is 24 bytes or shorter, so no line in the tree drives a command name
  through `truncateField` while the sweep is watching. A `p.log.Debug("cut", "name",
  entry.Name)` added at the truncation site logs a **full-length workspace-authored
  name** and today reddens only the record-count assertion, not the content sweep — and
  a version that appended to the existing record instead would be caught only by the
  `reflect.DeepEqual`. W4's line 2 carries an over-cap sentinel with its distinctive
  prefix at the **front** specifically so the cut value still matches `strings.Contains`.
  Code review should check that the shipped sentinel is prefix-anchored and that the
  `leaks` entry is the prefix rather than the padded name; a suffix-anchored sentinel
  reinstates exactly the gap this finding names.

- **[Error messages, logs, telemetry] No findings on the undecodable rung, and W4's line
  7 is what makes that deterministic.** `emitModelList`'s undecodable arm deliberately
  does **not** log the unmarshal error, because `encoding/json` quotes the offending
  input bytes into its error text — on a line carrying a `commands` array those bytes
  are workspace-authored names, routed into the daemon log through a channel no
  per-attribute check can see. That rule is argued in prose today and pinned by nothing:
  the existing undecodable line carries no `commands` array at all. Line 7 supplies one
  with a sentinel name, turning the rule into a red test.

- **[Trust boundaries] No findings, and one limit stated so the matrix is not read as
  proving more than it does.** `maxSlashCommandName`, `maxModelResolved`,
  `maxModelValue` and `maxModelDisplayName` all hold **256**, so swapping which constant
  `emitModelList`'s command loop reads is an **equivalent mutant today** — no fixture can
  separate two equal numbers, and none is proposed. The matrix pins the cap's **value**
  (the exactly-at-the-cap row against a hand-written literal) but not the **identity** of
  the constant read. The exposure of that gap is nil while the numbers agree, and the row
  becomes live the moment any one of them moves — which is precisely the scenario the
  separate-constant-per-field rule exists for, and why the test-side fixture is likewise
  its own literal. Recorded rather than fixed: adding a row now would assert nothing.

- **[Trust boundaries — charset]** No findings. `__remote-workflow` is the committed
  proof that no charset may be assumed, and AC 2 pins it **by name** rather than letting
  it ride anonymously inside a 51-element comparison. Verified against the committed
  bytes this pass: it is the sole name outside `[a-z0-9-]` in all three responding arms.
  If a re-capture drops it, the named pin says what was lost; the anonymous
  `commandNameIsPlainSlug` guard alone would say only that some property went missing.

- **[Subprocess / external command execution] No findings — and the reachability claim
  is stated in its corrected form.** The bounded `Name` reaches no `exec.Command`
  argument, no `filepath.Join`, no `filepath.Match`, no `regexp`, no argv element and no
  wire frame. Correcting #1877's spec, which credited `turnbridge.MapEvent`'s `default`
  arm with reach it does not have: `MapEvent` has exactly two production call sites,
  `emitMapped` (reached only from `Handle`'s typed arms, none of which is
  `SlashCommandList`) and `resolveBoundModelList` (handed a `ModelList` explicitly), so
  `SlashCommandList` never reaches `MapEvent` at all — it stops at
  `interactiveTurnEmitterV2.Handle`'s own default and is logged **by kind**. Recorded
  here because it is the enumeration a later slice will inherit, and the package
  overview's own lesson is that an enumeration transcribed from a spec is not verified by
  transcription. Nothing in this ticket changes that reach in either direction.

- **[Network & I/O — resource exhaustion] Not applicable to this ticket, and its
  boundary is deliberately untested here.** The entry-count bound is #1826's and does not
  exist; the transient bound is `defaultMaxParseBuf` and is unchanged. AC 2 asserts the
  emitted count against the capture's own array length rather than against any cap, which
  is the correct shape while no cap exists — a row asserting a count limit would encode a
  bound the tree does not have. No new-fixture row approaches a size that stresses the
  parse buffer: the largest string any row builds is `cap + 1` bytes.

- **[File operations] Not applicable — no path is constructed, opened or matched.** The
  only file any new test reads is the committed capture, through the existing
  `capturedInitializePayload` reader, on a path minted from constants by
  `capturedInitialize` from a **closed** arm set.

- **[Tokens, secrets, credentials] Not applicable.** No token, secret or credential is
  generated, stored, compared or logged on this path, and the leak sentinels are
  daemon-authored literals carrying no real-world value.

- **[Cryptographic primitives] Not applicable.** No randomness, no comparison against a
  secret. The only comparisons are lengths against a fixture cap and strings against
  captured bytes.

- **[Concurrency] No findings.** No goroutine, shared state or lock is added.
  `t.Parallel()` at both levels is safe here: every subtest builds its own `Parser`, its
  own `logRecorder` and its own event slice, and the shared fixtures
  (`slashCommandNameCapFixture`, the capture readers) are read-only. The capture readers
  are already used from parallel subtests across three arms today.

- **[Threat model alignment] No findings.** Nothing here publishes, so no obligation
  moves onto or off the client. `turnevent.SlashCommandList`'s `SECURITY` paragraph and
  `protocol.SlashCommand`'s — inert text only, never an HTML sink, an attribute or a URL
  — stand unchanged, and `docs/protocol-mobile.md` § `slash_command_list`'s "nothing
  emits it yet (#1720)" is about the wire frame and is untouched by a test-only ticket.

**Reviewer:** architect (self-review per the security-review checklist)
**Date:** 2026-08-31
