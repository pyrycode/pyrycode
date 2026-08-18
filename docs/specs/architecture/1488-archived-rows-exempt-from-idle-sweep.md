# #1488 — exempt `IsArchived` rows from the idle sweep

Reconcile the destructive auto-archive sweep with the recoverable manual soft-archive: a
conversation the user archived is never hard-deleted by the idle sweep.

## Files to read first

| File | Symbol / section | What to extract |
|---|---|---|
| `internal/conversations/archive.go` | `ShouldArchive`, `archiveIdleThreshold` | The whole file. This is the one production edit. Note the existing `IsPromoted` short-circuit and the doc-comment's `iff` sentence — both matter. |
| `internal/conversations/archive_test.go` | `TestShouldArchive` | The four-row table shape: inline `Conversation` literals, single `now := time.Date(...)`, `LastUsedAt: now.Add(-d)`. The new row copies it. |
| `internal/conversations/sweep_test.go` | `TestSweep`, its local `seedSpec` type and `mk` helper | `seedSpec` currently carries `idleDays` + `isPromoted`; `mk` builds IDs as `fmt.Sprintf("%08d-2222-4333-8444-555555555555", i)` and `Cwd` as `/seed-%d`. Both are the identity handles the new row's survivor assertion uses. |
| `internal/conversations/sweep.go` | `Sweep` | Read-only. Confirms `Sweep` delegates the whole decision to `ShouldArchive` — no second decision site to patch. |
| `internal/conversations/conversation.go` | `Conversation` — the `IsArchived` and `IsPromoted` field comments | The `IsArchived` comment enumerates its consumers and is falsified by this change. |
| `internal/conversations/registry.go` | `SetArchived` | Read-only. Confirms the single-field guarantee (it never bumps `LastUsedAt`) — the reason the exemption, not a timestamp bump, is the fix. |
| `internal/sessions/pool_conv_sweep_test.go` | `TestPool_Run_RegistersSweepLoop_HappyPath` | The **only** out-of-package caller of `ShouldArchive`. Confirm it needs no edit (see § Blast radius). Do not modify it. |
| `docs/knowledge/features/conversations-auto-archive.md` | §§ *What it is*, *How it works*, *Decisions → Predicate / sweep / wiring split*, *Tests → `TestShouldArchive`*, *Tests → `TestSweep`*, *Out of scope* | Six sites restating the predicate or the row counts. All six are AC-listed. |
| `docs/knowledge/features/conversations-registry.md` | § *Durable manual-archive primitive (#880)* bullet, § `SetArchived`, § *Related* | Two sites call the sweep "unrelated" / "distinct". AC-listed. |

`codegraph_context` on this task returned exactly the `conversations` package plus two
`internal/e2e/realclaude` trailer tests; the latter are name-collision noise (neither
references `ShouldArchive` or `Sweep` — confirmed by `git grep`). The real blast radius is
one package plus one out-of-package test read.

## Context

Two archive mechanisms exist and neither reads the other:

- **Auto-archive** (#219/#237/#242/#243) — `ShouldArchive` + `Sweep` **hard-delete** unpromoted
  rows idle ≥ 30 days via `Registry.Delete`. Wired into production inside `Pool.Run` whenever
  `Pool.convReg` is non-nil.
- **Manual soft-archive** (#880/#881) — `Conversation.IsArchived` + `Registry.SetArchived`,
  flipped by the `archive_conversation` / `unarchive_conversation` verbs. Explicitly
  *recoverable*.

`ShouldArchive` never reads `IsArchived`, so the destructive mechanism is blind to the
recoverable one. A user who archives a conversation to keep it loses it 30 days after its
last **activity** — `SetArchived` sets exactly one field and does not bump `LastUsedAt`
(pinned by `TestRegistry_SetArchived_HitSetsAndClears`), so archiving a row already idle for
29 days buys it one more day, not thirty.

**The decision this ticket encodes:** manual archive is durable until the user unarchives.
Auto-archive deletion applies only to rows the user has *not* archived. Archived rows are
retained indefinitely; unbounded registry growth is the accepted cost, and a retention
policy — if ever wanted — is a separate ticket.

Explicitly **out of scope**: the alternative fix direction where `Sweep` *sets* `IsArchived`
instead of deleting, paired with a longer retention policy. That changes what auto-archive
means for every existing idle discussion and needs its own decision.

## Design

### The guard

One new guard in `ShouldArchive`, placed **after** the existing `IsPromoted` short-circuit:

```go
func ShouldArchive(c Conversation, now time.Time) bool  // unchanged signature
```

Behaviour after the change — `ShouldArchive` returns `true` iff
`!c.IsPromoted && !c.IsArchived && now.Sub(c.LastUsedAt) >= archiveIdleThreshold`.

Guard placement is not behaviourally significant (both exemptions return `false`
unconditionally). Prescribed shape: a **separate** `if c.IsArchived { return false }` block
rather than folding into `if c.IsPromoted || c.IsArchived`. Reasons: the existing
`IsPromoted` line stays byte-identical, each guard carries its own one-line rationale
comment, and the two new test rows map 1:1 onto the two guards.

Nothing else changes. `Sweep` delegates its entire decision to `ShouldArchive`, so there is
no second decision site. `Sweep`'s return value and `sweepOnce`'s `count` log field keep
their current meaning (rows removed). No change to `RunSweepLoop`, the `Save` contract, or
`SweepInterval`.

### Doc comments falsified by the guard

Three comment sites in the package assert the old predicate. All three are prose-only edits.

1. `archive.go` — `ShouldArchive`'s doc comment. Its `iff` sentence ("unpromoted … AND its
   `LastUsedAt` is at least `archiveIdleThreshold` in the past") is now wrong. Restate with
   the archived exemption and one clause on *why*: a manually archived row is recoverable by
   design, so the destructive sweep must not claim it.
2. `archive.go` — `archiveIdleThreshold`'s doc comment. "Promoted channels are exempt
   regardless of `LastUsedAt`" is still true but now incomplete; add manually archived rows
   to the same sentence.
3. `conversation.go` — the `IsArchived` field comment. It reads "Flipped by
   `Registry.SetArchived` and consumed by the archive/unarchive verbs (#881)". That
   consumer enumeration is now short by one: `ShouldArchive` reads it too. Add the sweep as a
   consumer.

Also in `conversation.go`, the `IsPromoted` field comment's `false — discussion (ephemeral,
eligible for auto-archive)` line: qualify "eligible" so it does not read as the complete
eligibility rule (eligibility now also requires `!IsArchived`). One clause; do not restructure
the comment.

**Bound:** these four comment edits are the entire non-test production surface outside the
guard itself. No other symbol in `conversation.go` or `registry.go` changes.

### Blast radius

`codegraph_impact ShouldArchive` plus a `git grep` sweep gives one out-of-package reader:
`TestPool_Run_RegistersSweepLoop_HappyPath` in `internal/sessions/pool_conv_sweep_test.go`,
which asserts its single survivor is not archive-eligible. That test seeds only
non-archived rows, so the predicate's value on every row it constructs is unchanged.
**Expected edit: none.** If the developer finds themselves editing it, the guard is wrong.

No test anywhere currently sets `IsArchived` on a row that reaches `Sweep` or `ShouldArchive`
— the ten test files that mention `IsArchived` are registry, protocol, verb-handler and e2e
tests that never invoke the sweep. So no existing test pins the bug, and no existing
assertion needs its direction flipped.

## Concurrency model

Unchanged. `ShouldArchive` stays a pure function over a value copy — no I/O, no clock, no
lock. It reads one more field of the same copy. `Sweep` still runs on the caller's
goroutine over a detached `reg.List()` snapshot. The sweep goroutine's registration in
`Pool.Run`'s errgroup is untouched.

## Error handling

No new failure modes. The guard cannot fail; it reads a `bool` field.

Behavioural consequence worth stating: the set of rows `Sweep` removes strictly shrinks.
`sweepOnce`'s `n == 0` short-circuit therefore fires more often, meaning fewer `Save` calls —
which is the existing, already-tested no-op-tick contract, not a new path. A registry whose
only idle rows are archived now produces zero sweep log lines instead of an
`archived idle conversations` line; that is the intended outcome, not a regression in
observability.

## Testing strategy

Two new table rows. Both must be RED against the unmodified predicate.

### `TestShouldArchive` (`archive_test.go`) — one new row

- **`archived, very idle`** — `IsPromoted: false`, `IsArchived: true`,
  `LastUsedAt: now.Add(-365 * 24 * time.Hour)`, `want: false`.

`IsPromoted` **must** be `false`. If the row were promoted, the existing `IsPromoted`
short-circuit would absorb the fixture and the row would pass green with the new guard
deleted — proving nothing. The row's whole job is to reach the new guard.

The four existing rows stay byte-identical and stay green: all four leave `IsArchived` at its
zero value, and the guard is a no-op on `false`.

### `TestSweep` (`sweep_test.go`) — one new row plus a survivor-identity assertion

The local `seedSpec` gains an `isArchived bool` field, and `mk` passes it through to the
`Conversation` literal it hands `r.Create`. Existing rows use keyed struct literals, so they
compile and behave unchanged with the field defaulting to `false`.

- **`archived-idle-row-survives`** — seeds two unpromoted rows, both idle 31 days: seed 0
  archived, seed 1 not. Expected count: `1`.

Assertions for this row, beyond the existing generic ones:

- The returned count is `1`.
- `len(reg.List())` is `1` (the existing generic assertion already covers this).
- **The survivor is the archived row** — identify it by the deterministic handle `mk` assigns
  (`Cwd == "/seed-0"`, or equivalently the seed-0 `ConversationID`), and assert `IsArchived`
  is `true` on it.

That identity assertion is load-bearing and cannot be dropped in favour of the existing
generic survivor loop. Two distinct mutants make the point:

| Mutant | Count assertion | Generic `!ShouldArchive(survivor)` loop | Identity assertion |
|---|---|---|---|
| Guard deleted entirely | **RED** (2 ≠ 1) | — | **RED** |
| Guard inverted to `if !c.IsArchived { return false }` | green (still 1) | green (predicate is `false` for the non-archived survivor under this mutant too) | **RED** (wrong row survived) |

The inverted-guard mutant is killed **only** by the identity assertion. Verify both mutants
before calling the row done — `go test -overlay=<abs-path json>` runs a mutated tree with no
worktree writes.

The generic survivor loop (`if ShouldArchive(c, now) { t.Errorf(...) }`) needs no change: an
archived survivor evaluates `false` under the shipped predicate and passes it.

Existing `TestSweep` rows stay green; none seeds an archived row.

### Gate

`make check`. No new build tags, no e2e tier involved — the `internal/e2e/conv_sweep_test.go`
daemon e2e seeds only non-archived rows and is unaffected.

## Documentation

Both files are evergreen feature docs, both AC-mandated. Sweep every site; a numeric grep
will not find all of them (two spell their row counts as words).

### `docs/knowledge/features/conversations-auto-archive.md` — six sites

1. **§ What it is** — the prose sentence "`ShouldArchive` returns `true` iff
   `!c.IsPromoted && now.Sub(c.LastUsedAt) >= 30*24*time.Hour`". Add the `!c.IsArchived`
   conjunct.
2. **§ How it works** — the **verbatim `ShouldArchive` source block**. It must match the
   shipped function body exactly, including the new guard. Paste from the edited
   `archive.go`, do not hand-transcribe.
3. **§ Decisions → *Predicate / sweep / wiring split*** — the row counts are spelled as
   words: "a **four-row** table-driven unit test" → five-row; "a **six-row** table-driven
   test" → seven-row.
4. **§ Tests → `TestShouldArchive`** — the "with **four** rows" sentence → five, and add the
   new row to the markdown table (columns: Name / `IsPromoted` / `LastUsedAt` offset /
   Expected — the table needs an `IsArchived` column or the new row's distinguishing field
   stated in its Name; prefer adding the column so the four existing rows read as
   `false`).
5. **§ Tests → `TestSweep`** — add the new row to the row table, and extend the paragraph
   that explains which row pins what: state that `archived-idle-row-survives` pins the
   manual-archive exemption and that its survivor-identity assertion (not the count) is what
   catches an inverted guard.
6. **§ Out of scope** — the **Archive destination** bullet currently says the reconciliation
   "is still out of scope". Replace with the decided lifecycle: archived rows are exempt from
   the idle sweep; deletion applies only to non-archived idle discussions. Keep the bullet's
   remaining, still-true content (`Sweep` archives by removing the row; history retention is
   a separate concern), and keep the `Sweep`-sets-`IsArchived`-instead-of-deleting direction
   listed as an explicit non-goal so the deferral stays documented rather than unstated.

Additionally, add a short **§ Decisions** subsection — *Manual archive is durable until the
user unarchives* — recording the decision and its accepted consequence (archived rows are
retained indefinitely; recoverability outranks bounded registry growth for a small per-user
registry). Three or four sentences; this is the doc's home for the lifecycle rule, and § Out
of scope should point at it rather than restate it.

### `docs/knowledge/features/conversations-registry.md` — two sites

1. **§ *Durable manual-archive primitive (#880)*** bullet — "Distinct from the auto-archive
   `Sweep` …, which permanently deletes rather than flagging". Keep "distinct" (the two
   mechanisms remain separate); drop any implication of independence and state that the sweep
   now reads `IsArchived` and exempts archived rows (#1488).
2. **§ Related** — "the pre-existing, **unrelated** `Sweep`/`ShouldArchive` hard-delete
   auto-archive". "Unrelated" is now false. Restate as: a distinct soft-archive mechanism
   that the hard-delete sweep now honours.

### Do not touch

- `docs/knowledge/codebase/880.md` — its "the pre-existing, untouched `Sweep`/`ShouldArchive`"
  sentence was true at #880's time. Per-ticket codebase notes are historical records owned by
  the documentation phase; leave it.
- `docs/knowledge/codebase/1488.md` — **not a developer deliverable.** The documentation
  phase writes it from this spec plus the merged diff.
- `docs/knowledge/INDEX.md` — documentation phase is the sole writer. No new doc file is
  created here, so nothing to add anyway.

Run `qmd update && qmd embed` after the doc edits.

## Open questions

None blocking. Two judgement calls resolved above rather than deferred:

- **Guard placement** — after `IsPromoted`, as a separate block. Behaviourally equivalent to
  any other placement; chosen for diff minimality and 1:1 guard-to-test-row mapping.
- **The `IsPromoted` field-comment qualifier** in `conversation.go` — included. Its
  "eligible for auto-archive" clause is not literally falsified (it describes the promotion
  axis), but leaving it while sweeping six prose sites elsewhere would leave the type's own
  doc asserting the superseded eligibility rule. One clause, no restructuring.
