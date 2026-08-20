# #1639 — Correct the twelve announced-model "nothing emits it" claims

**Status:** spec
**Size:** S (re-checked below)
**Date:** 2026-08-20
**Derived at:** `f3512075` (`main`), **three commits past the `f4a3208` the ticket body was measured at**

---

## Read this first: what moved since the ticket body was written

The body says it was re-derived at `f4a3208`. `main` has advanced by three commits since
(`175c842`, `11dc0b6`, merge `f351207` — all #1543). **Every one of the twelve rows was
re-verified individually at `f3512075` and every cited line still reads exactly as the body
quotes it.** The table is good. Three things around it are not:

1. **The `turnbridge-package.md:335` cite in § What must not change is stale.** #1543 deleted the
   package's inbound half and re-homed the doc onto `outbound.go`; the file is now **213 lines**,
   not 337. The `ModelAnnounced` row is alive and well — it is the last mapped row of the `MapEvent`
   table, sitting directly below the `RateLimited` (#1410) row and directly above the `ThoughtChunk`
   drop row. **Exactly one such row exists.** Locate it by those neighbours, never by line number.
   The constraint is unchanged: do not add a second, do not edit it, do not import a `MarshalJSON`
   clause into it.
2. **`docs/knowledge/INDEX.md` now names the variant.** The body says it has "zero hits for
   `ModelAnnounced` or `model_announced`" — true at `f4a3208`, false now. #1543's documentation
   phase rewrote the `turnbridge-package.md` INDEX row and it now carries
   `` `ModelAnnounced`→`model_announced` (#1638) ``. **It reads correctly** — it describes the
   mapping as live and attributes #1638. The AC-2 sweep will surface this file; it needs **no
   edit** and must not get one. INDEX.md belongs to the documentation phase.
3. **The variant-bearing live-file set is now 21 files, not 20** — INDEX.md is the addition, for
   the reason above. Nine carry rows; the other twelve are cleared (the body clears eight, #1638
   owns two, INDEX.md and `turnbridge-package.md` are the two shipped-voice docs above).

Everything else in the body holds. **Re-derive anyway if further commits land** — that is AC-2's
whole point, and this section is the worked example of why.

## Branch-overlap check — measured, not skipped

Run at `f3512075` over all 65 remote `origin/feature/<N>` branches. Two files reported an overlap;
**neither can conflict**, and the measurement is recorded here so it can be checked rather than
trusted:

| branch | issue state | shared file | why it cannot conflict |
|---|---|---|---|
| `origin/feature/449` | **CLOSED** 2026-05-17 | `internal/protocol/codes.go` | Abandoned three months ago. A branch that never merges cannot produce a merge conflict. Not "in-flight" under the check's own terms. |
| `origin/feature/1504` | OPEN, PR #1619 unmerged | `docs/knowledge/features/streamsup-package.md` | Its three hunks in that file are at lines **491, 510, 529**. This ticket's only edit region there is **`:329-334`**. Minimum gap **157 lines**; three-way merge uses 3 lines of context. |

**The one condition that changes this answer:** if the AC-2 sweep turns up a *thirteenth* site inside
`streamsup-package.md` **below line 480**, that lands in #1504's neighbourhood. Stop, say so on the
issue, and let a human decide — do not merge-resolve it silently. Nothing in the sweep run for this
spec found one, and #1504 is a stall-watchdog change that never names this variant.

## Files to read first

Symbols and sections, not line numbers — the body's own line cites are already one commit-set stale
in one place, and yours will be too.

- `internal/turnbridge/outbound.go` → the `case turnevent.ModelAnnounced:` arm of `MapEvent` — **the
  fact that falsifies all twelve claims.** Read what it actually returns before writing a word.
- `cmd/pyry/interactive_turn_v2.go` → `Handle`'s `turnevent.ModelAnnounced` case, and `eventKind`'s
  `ModelAnnounced` arm. **The `eventKind` arm's comment is #1638's corrected voice on a Go
  comment** — the closest template in the repo for rows 1–6. Note how it keeps the #833 log bar
  intact while retiring the client-absence half.
- `cmd/pyry/relay_guard_test.go` → the `"TypeRateLimited": "push"` comment block. It sits **directly
  above** row 3 and is the same sentence already corrected once, after #1410 shipped. Row 3's
  correction is that block's shape with the numbers changed.
- `docs/knowledge/features/streamsup-package.md` → the `rate_limited` paragraph's sentence beginning
  `` `turnbridge.MapEvent` no longer drops it `` — the template for row 11, in the same file, ~30
  lines above it.
- `docs/protocol-mobile.md` → § `rate_limited`'s producer sentence (`#1410 wired the producer —
  internal/turnbridge's MapEvent`) — the template for row 9's § **Declared, not emitted.**
- `docs/knowledge/features/turnbridge-package.md` → the `MapEvent` table's `ModelAnnounced` row —
  **read-only, do not touch.** Confirm it is there exactly once.
- `docs/knowledge/INDEX.md` → the `turnbridge-package.md` row — **read-only.** Confirm it already
  reads correctly so the sweep does not tempt an edit.
- `CODING-STYLE.md` § "Comments — Citing Other Code" — the citation rule the build enforces.

## Context

#1600 taught the parser to translate claude's `system`/`init` line into `turnevent.ModelAnnounced`.
#1616 declared the v2 wire shape. **#1638 added the `MapEvent` arm and the `cmd/pyry` `Handle`
case**, so the frame reaches an interactive v2 client today. Twelve live sites still say it does
not. This ticket is that correction and nothing else: no behaviour, no code, no tests.

No ADR is warranted. The declare-then-emit-then-correct sequencing already has its record on
#1405→#1410 and #1393→#1394, and this is the third instance of the same shape.

## Design

There is no design to make — the work is a bounded prose sweep. What follows is the part a
developer gets wrong.

### The claim is fused, and only half of it expires

Every site fuses two propositions in one sentence: *`MapEvent` drops the variant* **and** *nothing
emits it / no client receives one*. #1638's single code change falsified both, everywhere, at once.
Rows 1 and 12 fuse a **third** proposition that does **not** expire: the value reaches no daemon
**log** at any level (#833's posture). An event is not a log; a wire frame is not a log. Retiring
the log half is the failure mode to watch for on those two rows.

### The three shapes of correction

| shape | rows | what to write |
|---|---|---|
| retire the claim, keep the sentence | 2, 3, 6, 7, 10, 11 | The sibling `rate_limited` block in the same file is already this shape. Match its voice. |
| retire the claim, keep an explicit before/after record | 1, 12 | These already carry dated `CORRECTED … (#1616)` blocks. Append a **new** `CORRECTED 2026-08-20 (#1639)` paragraph in the existing form. |
| retire the claim **and** move a count | 8 (+ the count at `:652-656`) | See below — the only non-prose edit. |
| rewrite a whole titled block | 4, 5, 9 | § **Declared, not emitted.** is no longer true as a *heading*, not just as prose. The block needs a new lead-in, not a patched sentence. |

For rows 1 and 12: **correct the text surviving at the cited line, never the quoted-and-deleted
earlier wording.** Both blocks quote a retired #1616-era phrasing (`"so no wire frame exists for it
yet"`) that is already dead. Editing the quotation instead of the live claim leaves the bug in place
and corrupts the historical record in one move.

### The count that moves

`docs/protocol-mobile.md` § `unrecognized_message`, the two-tier list: **"Four of the five reach this
wire"** becomes five of five, `model_announced` joins the documented-below enumeration, and row 8's
"does **not** reach the wire yet" clause is deleted with it. This is where the work departs from
#1410's precedent — that record reads "emitting an already-documented frame changes no count", true
there because `rate_limit_event` is a top-level line type, false here because `init` is a `system`
subtype.

**The counts that stay put**, all in the same file, two within ten lines of the one that moves:

- the parser-subtype count (**five** `system` subtypes) — about the *parser*, not the wire
- both "fifteen turn-stream events" literals — already corrected by #1616
- the dated changelog entries, including the 2026-08-19 `model_announced` entry whose "stays four"
  sentence is a correct record of what #1616 *decided*, not a live claim

### Every surviving producer reference names #1638

`#1617` was the split parent and is closed; the mapping landed in **#1638**. Where a corrected
sentence keeps a ticket reference, it names #1638. `#1617` should not survive anywhere in the twelve
except inside a quoted historical record. The `rate_limited` sites set this precedent — they kept
"the turnbridge mapping is #1410" after #1410 shipped.

## Sweep methodology — the part that silently fails

AC-2 is a sweep AC, and **a sweep that returns nothing looks identical to a sweep that found
nothing.** Three traps, all of which fired during this spec's own run:

1. **zsh does not word-split unquoted variables.** `FILES="a b c"; git grep -nP 'pat' -- $FILES`
   returns **zero hits** — `$FILES` arrives as one nonexistent pathspec. This is correct-looking
   bash and silently broken zsh. Use an array: `FILES=(a b c)` … `-- "${FILES[@]}"`.
2. **`git grep -- '<pattern>'` misparses.** A `--` *before* the pattern makes git read the pattern as
   a pathspec. Use `-e '<pattern>'` or no leading `--`.
3. **`git grep -E` silently drops `\b`** (no word boundaries in POSIX ERE) and reads as clean
   absence. Use `-F`, `-P`, or `-e`.

**Run every sweep against a known-present control first.** A control that returns zero means the
command is broken, not that the repo is clean.

### The needles

Scope: live `*.go` and `*.md`, excluding `docs/specs/` and `docs/knowledge/codebase/`.

- `git grep -nF "1617"` — 15 hits, reaching **only 10 of the 14** sites, and one of the 15 is the
  dated changelog entry that must **not** be touched.
- `git grep -nF "MapEvent"` — reaches the other four, which state the claim purely in `MapEvent` /
  `Handle` vocabulary.
- A claim-vocabulary sweep (`nothing emits`, `no client receives`, `drops the variant`, `no case
  for`, `declared ahead`, `still receives nothing`, `no wire frame`, `stops at the daemon`, `reaches
  no`, `renders it today`, `unemitted`, `Shipped unwired`) — run at `f3512075`, **returns no
  thirteenth site for this variant.** The `Shipped unwired` hits that do land belong to
  `contextwindow`, `debugbundle`, `msgqueue`, `sessions`, and `streamsup`'s `Busy`/`WaitIdle`.

The union is the body's table. **The table is the authority, not any single needle** — #1410 ran this
same sweep one variant earlier, enumerated six sites, and code review found a seventh whose phrasing
matched none of the developer's needles.

### Row 4 is invisible to the obvious grep, and this is measurable

`internal/protocol/codes.go` has **two independent edit points** at opposite ends of one const
block's comment. The `1617` needle finds only the second. And the first is invisible to a
line-oriented `drops the variant` grep too, because the phrase **straddles a line break**
(`…default drops` / `// the variant…`). Confirmed: a `drops the variant` sweep reports one match in
`codes.go`, and it is the *closing* paragraph. **Correcting one end and calling the file done is the
single easiest miss in this ticket.**

## Concurrency model

None. No goroutines, no channels, no shutdown sequence. This ticket adds no code.

## Error handling

None. No new failure modes.

## Testing strategy

No tests are written and none are modified. The bar is that the existing gates stay green.

- **`make check` is the functional bar.** It runs `vet`, race tests, `staticcheck`, `substrate-guard`,
  **`cite-guard`**, and the fake-claude e2e suite. It is green on the clean tree.
- **`make cite-guard` will see every Go comment you touch.** It is diff-scoped against `merge-base`,
  so the lines you add or modify are in scope. **Cite symbols, never `file.go:NNN`, never a range,
  never a bare `:NNN`** — there are no exemptions left. This matters most on rows 1 and 12, where the
  house form invites writing a dated block that references other code.
- **`make check` never compiles row 1's file.** `internal/e2e/realclaude/interactive_stream_inband_model_test.go`
  is behind the `e2e_realclaude` build tag. The package can fail to build while the standard gate
  stays honestly green.

  **Use the hermetic compile check instead of spending tokens:**

  ```
  go vet -tags e2e_realclaude ./internal/e2e/realclaude/
  ```

  Measured on the clean tree at `f3512075`: exit 0 in ~0.5s, no credentials, no network, no tokens.
  For a comment-only edit this is the honest bar — it proves the package still builds, which is the
  only thing a comment edit can break. If the live suite is run anyway, **read the count of `=== RUN`
  lines, never the exit code**: a package that fails to build reports zero and still exits 0 through
  a shell wrapper.

- **Two green tests stay green and unmodified.** `cmd/pyry/stream_turn_busy_test.go`'s
  `ModelAnnounced → turnMarkNone` row and its surrounding turn-mark-whitelist comment expire
  nothing. `cmd/pyry/interactive_turn_v2_test.go`'s
  `TestInteractiveTurnEmitterV2_ModelAnnouncedEventKindNamesTheVariant` pins that `eventKind` names
  the variant without leaking the value into the log; #1638 refreshed its prose and left its
  assertions alone. Neither is this ticket's business.

- **Verification of the change itself is the re-run sweep**, per AC-2: after the edits, both needles
  plus the claim vocabulary return no live site still asserting the frame is unemitted or that
  `MapEvent` drops the variant, and no surviving `#1617` outside a quoted historical record.

## Size — re-checked against this written spec

| Limit | Boundary | This ticket |
|---|---|---|
| Production source files created or modified | ≤ 3 | **3** — `internal/turnevent/event.go`, `internal/protocol/codes.go`, `internal/protocol/interactive.go`, comments only |
| Total written work | ≤ 400 | **~110** |
| New exported types or interfaces | ≤ 5 | 0 |
| Consumer call sites needing simultaneous update | ≤ 10 | 0 |
| Acceptance criteria | ≤ 5 | 4 |
| Distinct error/reject branches | ≤ 10 | 0 |

The line figure is anchored on the nearest analogue rather than estimated: #1410's identical
doc sweep is commit **`ca2b07e`**, measured **+29/−25 across 6 files for 6 sites**. This is 12 sites
across 9 files plus one count move — roughly double, ~110 lines. No tests are written, which is
where the 2026-05-16 salvages hid their overruns.

The twelve edit sites are **not** a call-site cascade: each is a self-contained comment or paragraph,
no build depends on any other being edited, and a half-finished sweep compiles. No boundary is
exceeded and no boundary is argued down.

## Open questions

1. **Rows 1 and 12: append a dated `CORRECTED` block, or rewrite in place?** Both already carry
   `CORRECTED 2026-08-19 (#1616)` blocks, which is the house form and the reason the record is
   readable at all. Appending a `CORRECTED 2026-08-20 (#1639)` paragraph is the recommendation, and
   it is also the more expensive one in lines. Rewriting in place is acceptable if the resulting
   paragraph does not leave a dangling reference to a claim no longer present. The developer decides
   per site; what is not acceptable is editing the *quoted* earlier wording.
2. **Row 9's § heading.** **Declared, not emitted.** is a bolded lead-in, not a markdown heading, so
   nothing anchors to it and it can be renamed freely. Worth a `git grep -F "Declared, not emitted"`
   to confirm nothing cross-references the phrase before renaming.
