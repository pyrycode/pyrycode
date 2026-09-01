# #1885 — Pin the per-name cap on the commands-only rung

**Ticket:** [#1885](https://github.com/pyrycode/pyrycode/issues/1885) — `test(streamsup): pin the per-name cap on the commands-only rung`
**Labels:** `enhancement`, `size:s`, `security-sensitive`
**Size, re-counted against this spec:** XS — **0** production files, 1 test file, ~70 lines of total written work, 0 new exported types, 0 consumer call sites, 3 acceptance criteria, 0 reject branches. PO's `size:s` is an upper bound and is not being raised.

---

## Files to read first

| Read | Symbol | What to extract |
|---|---|---|
| `internal/streamsup/parser_test.go` | `TestParser_SlashCommandFieldsAreCapped` | The table this ticket extends: its row struct, its assertion loop, and the `The models array is carried by every row` paragraph AC 3 corrects. **This is where the whole change lands.** |
| `internal/streamsup/parser_test.go` | `TestParser_InitializeControlResponseCommandsOnlyRungEmits` | What is already pinned on this rung — the record's six attributes, the verbatim names incl. `__remote-workflow`, the event count. Read to know what NOT to re-prove, and for the two doc paragraphs AC 3 corrects (`THE CAP IS NOT PINNED BY THAT`, `NOT a row here: any cap boundary`). |
| `internal/streamsup/parser_test.go` | `initializeLineFixture`, `commandEntryFixture`, `modelEntryFixture`, `collectEvents` | The fixture set the new row is built from. `initializeLineFixture` is the only builder that can express a `commands` array with no `models` key. |
| `internal/streamsup/parser_test.go` | `slashCommandNameCapFixture` | The literal `256`, deliberately NOT `maxSlashCommandName` — its doc says why. Every length in the new row comes from it. |
| `internal/streamsup/parser_test.go` | `slashCommandNamePreview` | The bounded value printer the assertion loop already uses. The new row inherits it and adds nothing. |
| `internal/streamsup/parser.go` | `emitSlashCommandList` | The single loop holding the cut and the report. The symbol every mutant below edits. |
| `internal/streamsup/parser.go` | `emitModelList` | Its two calls to `emitSlashCommandList` — rung 4 (commands-only) and rung 5 (models). These are the mutants' other edit points, and rung 4's comment is the "one cap to keep in step" claim that stays. |
| `internal/streamsup/parser.go` | `truncateField`, `maxSlashCommandName` | The `<=` boundary and the *empty*-replacement scrub. Read so the new row re-proves neither. |
| `internal/streamsup/parser_test.go` | `TestParser_ModelListIsLoggedContentFree` | Confirm its commands-only line's name is far under the cap (it is — `commandsOnlyCommandSentinel`, 35 bytes), which is why the sweep stays green under M1. **Do not edit it.** |
| `docs/knowledge/features/streamsup-package.md` | § *Proving the bound with a matrix (#1878)* | Two lessons that bind this ticket directly: a row's claimed exclusivity must be **measured**, not reasoned from its inputs; and a `//`-claim sweep built on one phrase misses claims that say the same thing in other words. |
| `CODING-STYLE.md` | § *Comments — Citing Other Code* | Symbol, never line. `make cite-guard` runs inside `make check`. |

---

## Context

`emitModelList` classifies one top-level `control_response`. Since #1891 two of its rungs
emit a `turnevent.SlashCommandList`: rung 5, where a `commands` array rides behind a
`models` one, and rung 4, where a success carries `commands` and no `models`. Both call
the same `emitSlashCommandList`, whose one loop holds the per-name cut at
`maxSlashCommandName` and #1600's verbatim rule.

Every row that measures that cut today rides rung 5. That makes rung 4's cap **inherited
by construction** — true of the tree as it stands, and not a pin. The gap is not
hypothetical and is not a judgement call; it is measured below (§ Testing strategy, M1):
relocating the per-name bound out of the shared emitter and onto rung 5's call site leaves
**the entire `internal/streamsup` package green** while rung 4 copies uncut,
workspace-authored command names into a retained `turnevent.SlashCommandList`.

Why now: #1833 is blocked by this ticket and adds `description` to that same loop — a
second capped field and the entry's first `TruncatedFields` order. The cut site already
says *"The distinction starts mattering at argument_hint (#1833)."* The next edit to that
loop is the one most likely to drift, and today nothing measuring the bound would notice.

**No ADR.** This adds no decision — it measures a bound two shipped tickets already
decided.

---

## Design

Test-only. One file: `internal/streamsup/parser_test.go`. Three changes, in dependency
order.

### 1. `TestParser_SlashCommandFieldsAreCapped` gains a rung axis

The table's rows currently hardcode the rung twice in the harness: the `models` key is
always added to the fixture, and the harness reads `events[1]` after asserting
`len(events) == 2`. Both literals become consequences of one new row field.

Row struct gains exactly one field:

```go
// commandsOnly puts the row on the rung that carries NO models array, where the
// SlashCommandList is the line's only event. Zero value = the models rung, which is
// where the boundary rows below are measured and where they stay.
commandsOnly bool
```

Harness contract — the fixture and the two literals derive from that one bool:

- `inner` starts as `map[string]any{"commands": tt.entries}`; the `models` key with the
  existing one-entry `modelEntryFixture("claude-sonnet-5", "sonnet", "Sonnet")` literal is
  added **only** when `!tt.commandsOnly`. Keep that literal byte-identical so the five
  existing rows' input does not move.
- Expected event count and the `SlashCommandList` index derive from the same bool: `2, 1`
  on the models rung, `1, 0` on the commands-only one.
- The count assertion stays `t.Fatalf` and its message must **name the rung**, since the
  rung is now the row's own variable and a count failure that does not say which rung was
  asked for sends the reader to the wrong call site.

**One bool, not two int fields.** `wantEvents`/`wantIndex` as independent row fields would
make `(2, 0)` representable — a combination no rung produces. The rung is the fact; the
count and the index are derived from it, so a row cannot state a rung that disagrees with
itself.

**That count assertion is also the deterministic guard on this change.** A harness that
dropped the `models` key for *all* rows would silently move the whole boundary matrix onto
rung 4 — and then a mutant relocating the cut onto rung 4's call site would go green. It
cannot happen silently: a commands-only line emits one event, so the five existing rows
would fail their own `want 2` fatal. M3 below is the measurement of that.

### 2. One new row: identical to the first row except for the rung

| | |
|---|---|
| `name` | `on the commands-only rung, over the cap is cut and reported` |
| `commandsOnly` | `true` |
| `entries` | `commandEntryFixture(strings.Repeat("a", slashCommandNameCapFixture+1))`, then `commandEntryFixture("deep-research")` |
| `want` | `{Name: atCap, TruncatedFields: []string{"name"}}`, then `{Name: "deep-research"}` |

The entries and expectations are the existing `over the cap is cut and reported; a name
that fits is not` row's, unchanged. **That identity is the design.** The two rows differ in
exactly one variable, so what the new row proves is the rung and nothing else, and the
"sole red" claim in its comment is legible without re-deriving anything.

The row carries both live acceptance criteria:

- **AC 1** — the over-cap entry: emitted `Name` is `slashCommandNameCapFixture` bytes on a
  line carrying no `models`.
- **AC 2** — the fitting sibling: `deep-research` rides *beside* the cut entry with
  `TruncatedFields` nil, asserted through the loop's existing `reflect.DeepEqual`, so a
  producer naming the field on every entry reddens. `"name"` is the daemon's own snake_case
  field name and is written as a literal, exactly as the sibling row writes it.

The row's `why` must state, in its own words, **which mutant it is the sole red against**:
the relocation of the per-name bound onto rung 5's call site (M1), *not* a deletion. It
must also say that a deletion mutant does not grade this row, because a deletion reddens
the models rows too.

**What the row must not do.** No second copy of the `<=` boundary (that is
`truncateField`'s, pinned once by the exactly-at-the-cap row), no mid-rune input (that is
the scrub's, pinned twice already), no entry-count assertion, no record/attrs assertion —
the matrix uses `collectEvents`, which discards logs, and this rung's record is
`TestParser_InitializeControlResponseCommandsOnlyRungEmits`'s. One over-cap name and one
fitting sibling is the whole row.

### 3. The doc paragraphs AC 3 falsifies

Two symbols, three paragraphs. All three are true of today's tree and false after change 2,
which is exactly why they are in scope.

- `TestParser_SlashCommandFieldsAreCapped` — the paragraph from *"The models array is
  carried by every row…"* through *"A row therefore never states the models half."* Its
  new claim: most rows ride the models rung, which is where the matrix was measured and
  where the boundary rows stay; **one** row states its rung, because the identity of
  construction that the paragraph correctly describes is a fact about today's tree that a
  *relocation* falsifies without touching a single models-rung row. Keep the paragraph's
  surviving half — moving the *matrix* there still buys nothing, and the one row that
  moved is not a copy of the matrix but the proof that the identity holds.
- `TestParser_InitializeControlResponseCommandsOnlyRungEmits` — the `THE CAP IS NOT PINNED
  BY THAT` paragraph. Its first half stays true and is worth keeping: every name on that
  row is far under `maxSlashCommandName`, so a byte-exact comparison there cannot see the
  bound. What goes is the conclusion — *"inheritance by construction, not a pin"* — which
  must now point at the matrix's commands-only row by name as where this rung's cap **is**
  pinned.
- `TestParser_InitializeControlResponseCommandsOnlyRungEmits` — the `NOT a row here: any
  cap boundary` paragraph. Its conclusion survives (no boundary row belongs here) but its
  reason must be re-authored: the boundary rows are still measured on the models rung and
  are still not duplicated, and this rung's cap has its own single row in that same matrix.

**Sweep rather than trust this list.** The package's own #1890 lesson is that a claim sweep
keyed on one phrase misses the same claim worded differently. Run, in the worktree, a
sweep over Go comments — `git grep -n -F -e "not a pin" -e "by construction" -e "buy
nothing" -e "second copy" -e "inheritance" -e "wherever it is exercised" -e "measured on
the models rung" -- 'internal/*.go' 'cmd/*.go'` — and read the **whole doc block** of each
hit in `internal/streamsup`, not the matched line.

Sites checked while writing this spec and deliberately **unchanged** (they make
construction claims that stay true, not proof claims):

- `emitModelList`'s rung-4 comment — *"one decode target, one construction site, one cap
  … so there is no second decode, loop or cap to keep in step."* A claim about behaviour,
  and still true.
- `emitSlashCommandList`'s doc and `maxSlashCommandName`'s doc — neither says this rung's
  cap rests on construction for its *proof*.
- `TestParser_InitializeControlResponseCountsTheCapturedCommands`'s untruncated-path arm —
  says the capture's names are far under the cap. True, and about the models rung.

`docs/knowledge/features/streamsup-package.md` also carries prose about this bound. It is
the **documentation phase's** file, not this ticket's. Do not edit it; AC 3 is scoped to
committed `//` comments.

---

## Concurrency model

No goroutines, no channels, no shutdown sequence — the change is one table row and one
harness derivation.

The two properties that must hold under `go test -race`, which `make check` runs:

- The new subtest calls `t.Parallel()` like its four siblings. `collectEvents` builds a
  fresh `Parser` per subtest and `Parser.Write` is synchronous on the calling goroutine, so
  the appended `[]turnevent.Event` is never shared.
- `atCap` is computed once above the table and only ever read. The new row reads it like
  the existing rows do; it must not be mutated or rebuilt per row.

---

## Error handling

Failure-message discipline in the assertion loop is already correct and the new row
inherits it unchanged. Two rules the row must not break:

- **Length first, value second, and only through `slashCommandNamePreview`.** The over-cap
  entry is a 257-byte `a`-run; a bare `%q` of it prints two screens of `a`s and turns one
  red row into several reading passes. The existing loop already reports byte lengths
  first and only prints values when the lengths agree.
- **The message must name the rung.** With the rung now a row variable, a count or index
  failure that does not say which rung the row asked for points the reader at the wrong
  call site in `emitModelList`.

No production error path changes; no new failure mode is introduced.

---

## Testing strategy

Everything runs inside `make check` — `internal/streamsup` carries no build tag. Grade by
**mutating**, never by reasoning, and run every mutant through `go test -overlay` so no
production file is written to the worktree.

### Overlay recipe

Copy `internal/streamsup/parser.go` to an absolute scratch path, edit the copy, and point
an overlay at it. Paths in the overlay JSON must be **absolute**:

```json
{"Replace": {"<abs-worktree>/internal/streamsup/parser.go": "<abs-scratch>/parser_mutant.go"}}
```

Then, in a single call: `cd <worktree> && go test -count=1 -overlay=<abs-overlay.json> ./internal/streamsup/`.
The mutant copy lives outside the worktree; it must never be written into
`internal/streamsup/` and must never be committed.

### M1 — the relocation (AC 1; the new row must be the SOLE red)

The bound becomes the caller's rather than the shared emitter's:

- `emitSlashCommandList` takes a second parameter `nameLimit int` and its loop calls
  `truncateField(entry.Name, nameLimit)`.
- Rung 5's call passes `maxSlashCommandName`. Rung 4's passes an unbounded one (`1<<40`).

**This, and not a pre-cut at the call site, is the mutant AC 1 names.** A mutant that
pre-truncates the entries rung 5 passes and leaves the emitter a plain copy also destroys
rung 5's *report* — the emitter would see an at-cap name and compute `truncated == false`
— so the existing matrix reddens and the mutant fails AC 1's own second half. M1 relocates
the bound while leaving the report where it is, which is the drift #1833 actually
threatens.

Measured on today's tree, before the row exists:

- `go test ./internal/streamsup/` under M1 → `ok` (whole package). **That silence is the
  gap this ticket closes.**
- The proposed row, injected as a probe under M1 → fails with `entry 0 name: got 257
  bytes, want 256` and `entry 0 TruncatedFields: got []string(nil), want []string{"name"}`.
- The same probe on the unmutated tree → passes.

Expected after the change: under M1, `TestParser_SlashCommandFieldsAreCapped` reports
**exactly one failing subtest**, the new row, and no other test in the package fails.

### M2 — the unconditional report (AC 2; red, and not claimed sole)

In `emitSlashCommandList`'s loop, report on every entry: `cut := []string{"name"}` with the
`truncated` bool discarded (`_ = truncated`, or the copy will not compile). The new row's
fitting sibling reddens through the loop's `reflect.DeepEqual`. The existing over-cap row
reddens too — expected, and AC 2 claims no exclusivity.

### M3 — the reversed relocation (guards the harness change)

M1 with the two limits swapped: rung 4 keeps `maxSlashCommandName`, rung 5 passes the
unbounded one. This is the deterministic check that the harness change did not quietly move
the matrix off the models rung.

Measured on today's tree: three of the five existing rows redden — `over the cap is cut and
reported; a name that fits is not` and both `a cut landing mid-rune…` rows. The
exactly-at-the-cap and absent-name rows stay green, correctly: neither carries a name over
the cap, so no bound can be observed by them. After the change the new row must stay green
under M3, and those three must still redden.

### Regression

`make check` must be green with no mutant applied, including `make cite-guard`. No
`e2e-realclaude` or `e2e-liverelay` run is needed: nothing in this ticket compiles behind a
build tag and no production behaviour changes.

---

## Open questions

None blocking. Two judgement calls made here rather than deferred:

1. **Where the row lands** — inside `TestParser_SlashCommandFieldsAreCapped` rather than a
   new table beside `TestParser_InitializeControlResponseCommandsOnlyRungEmits`. A second
   table would duplicate the four-assertion loop (byte length, byte equality,
   `utf8.ValidString`, `TruncatedFields` DeepEqual) for one row, and would put the cap's
   proof in two places. Making the rung a row axis keeps one matrix, one loop, and lets the
   new row be its neighbour's twin in every variable but the rung.
2. **The sibling name** — `deep-research`, matching the models-rung row, rather than
   `__remote-workflow`. The verbatim/charset rule is already carried on this rung by
   `TestParser_InitializeControlResponseCommandsOnlyRungEmits`'s exact-name assertion, and
   AC 2 explicitly does not restate it. Reusing the twin's name keeps the rung the only
   difference between the two rows.

If a proof of AC 1 or AC 2 turns out to need a production change, **stop and report it on
the ticket** rather than making one — this ticket's surface is `parser_test.go` and nothing
else.

---

## Scope boundary

- **Production files: exactly zero.**
- Do not assert an entry-count cap or a `DroppedCommands` field — neither exists and both
  are #1826's.
- Do not assert a wire mapping or a `Handle` case — both are #1720's and neither exists.
- Do not widen, rename or re-decide the reason keywords or the record's six attributes;
  they are #1890's.
- Do not re-prove what shipped: suppression's three no-models shapes, the reject rungs, and
  the content-free sweep's commands-only line are all pinned already.
- No knowledge-base doc is a deliverable here. The documentation phase folds this ticket's
  lessons into `docs/knowledge/features/streamsup-package.md` after code review.

---

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No MUST FIX, and this is the category the ticket is *about*.
  claude's stdout is untrusted input; the command names it carries are **workspace-authored**
  — a `.claude/commands/*` entry written by whoever wrote the repository the operator
  pointed the session at, which is why `__remote-workflow` stands in the committed capture
  as the proof that no name charset may be assumed. The boundary is explicit and single:
  `emitSlashCommandList` is the only construction site, and the cut at
  `maxSlashCommandName` inside its loop is the only per-name judgement made there. What
  this ticket fixes is that **one of the two rungs crossing that boundary had no test
  proving the cut applies to it** — the unbounded transient array is bounded a level up by
  `defaultMaxParseBuf`, but the *retained* copy inside the emitted
  `turnevent.SlashCommandList` is bounded only by that loop. The design must not narrow a
  class guard over emitted names to `[a-z0-9-]`, and does not: the new row asserts a
  byte-length bound and an exact-equality on daemon-authored fixture strings, and adds no
  charset predicate.
- **[Resource exhaustion]** OUT OF SCOPE, ticket named. The retained slice has **no
  entry-count cap**: a 4 MiB line of `{"name":"a"}` entries decodes to hundreds of
  thousands of `turnevent.SlashCommand` values retained for the event's lifetime, with only
  `defaultMaxParseBuf`'s whole-line cap upstream of it. That bound is **#1826's**
  (`DroppedCommands` and the count cap), it is documented as such at the emit site, and
  this ticket is explicitly forbidden from asserting it. Naming it here so it is not
  mistaken for something this ticket covers.
- **[Error messages, logs, telemetry]** SHOULD FIX, addressed in § Error handling. The new
  row's failure messages are the one place this change can print a name. Its names are
  daemon-authored fixtures (an `a`-run and `deep-research`), so nothing workspace-authored
  reaches a message today — but the discipline has to hold for the next row: values print
  only through `slashCommandNamePreview`, and lengths print first. Separately, the row must
  **not** assert on log records: it uses `collectEvents`, which discards logs, so it cannot
  become a place where the content-free rule (#833's posture) is weakened by accident. The
  rule itself stays pinned by `TestParser_ModelListIsLoggedContentFree`, which this ticket
  does not touch.
- **[Concurrency]** No findings. No goroutine is spawned. The new subtest's `t.Parallel()`
  shares nothing across subtests — a fresh `Parser` per row, a synchronous `Write`, and a
  read-only `atCap` — and `make check` runs the package under `-race`.
- **[Tokens, secrets, credentials]** Not applicable by construction: no token, key or
  credential exists on this path, and the ticket adds no storage, no lifecycle and no
  comparison against a secret. `crypto/subtle` has no role here — every comparison the row
  makes is against a daemon-authored fixture, not a secret.
- **[File operations]** Not applicable: no path is built from input. The fixture is
  synthetic (`initializeLineFixture` + `commandEntryFixture`), and the committed capture is
  read through existing helpers this ticket neither adds nor changes. **One operational
  note:** the M1/M2/M3 mutants must live outside the worktree and reach the compiler only
  through `go test -overlay`. A mutant copy written into `internal/streamsup/` would be a
  redeclaration and fail the build loudly rather than silently, but it must not be written
  or committed regardless.
- **[Subprocess / external command execution]** Not applicable to this change. The data
  originates from the supervised `claude` child, which is why it is untrusted, but the
  ticket adds no `exec`, no argument construction and no environment handling.
- **[Cryptographic primitives]** Not applicable: no randomness is drawn and no primitive is
  selected. Fixture strings are deterministic `strings.Repeat` runs, not sampled values.
- **[Threat model alignment]** No findings. Nothing on this path reaches the wire —
  `turnevent.SlashCommandList` has no `Handle` case and no `MapEvent` arm (#1720 owns both)
  — so `docs/protocol-mobile.md` § Security model's client-facing surface is unchanged by
  this ticket. Nothing is published, so no client-visible threat is introduced or deferred.

**Reviewer:** architect (self-review per the security-review checklist)
**Date:** 2026-08-31
