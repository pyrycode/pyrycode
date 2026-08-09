# #1433 — The trailer gate reads the runner path on the presence side and names the owes-none case

**Ticket:** https://github.com/pyrycode/pyrycode/issues/1433
**Size:** S (confirmed, not overridden — see § Size check)
**Labels:** `size:s`, `security-sensitive`
**Baseline:** `ffe4120` (every line cite below re-derived against it)

One new return site in `trailGate`, one new test driver, one new companion sub-test inside the
shipped runner-path sweep, one widened exemption, one leak-sweep row, and the count/claim
corrections the new site forces. Two files, both `_test.go`. No new constant, no new closed-set
member, no change to `trailClassifyRun`.

---

## Files to read first

Turn-1 reading list. Every entry is line-anchored at `ffe4120`; read the range, extract the named
thing, move on. The whole ticket lives in the first file — read it end to end once, then use this
list as the index.

| Path + lines | What to extract |
|---|---|
| `internal/e2e/realclaude/trailer_admissibility_test.go:366-559` | `trailGate` in full — the ten shipped return sites, where the budget arm sits (`:537-548`), where the usable return sits (`:550-558`). This is where the new arm goes. |
| `…:441-524` | The absence branch: `trailReasonAgainstPath` **called** at `:496`, `switch against.Value`, three sites. The precedent the new arm follows exactly — including the fmt-ARGUMENT rule at `:484-485`. |
| `…:461-468` | The "why the count is ten and not eleven" argument. AC5 requires it re-stated, not renumbered. |
| `…:104-158` | The gate value space. `trailGateAbsentOwesNone`'s doc (`:136-138`, the asserting `#1369` cite) and `trailGateOutOfContract`'s enumerated sub-case count (`:145-156`, FIVE). |
| `…:754-781` | `trailGateCase` + the `pathVaries` field doc (`:772-780`). |
| `…:854-952` | `trailGateCases()` — the uniform-path premise (`:860-869`), the usable row (`:872-878`), the max_turns row (`:879-885`), row nine's comment (`:928-950`). |
| `…:1225-1235` | `trailGateAbsenceCaseMarkers()` — the marker set the new constant must **not** join. |
| `…:1237-1454` | `TestTrailGateNamesWhichAbsenceCaseFired` — the shape the new driver mirrors: pairwise-containment premise (`:1312-1323`), per-row want/marker table, headroom on the output (`:1438-1451`). |
| `…:1456-1481` | `trailGateRunnerReadings()` — `readings[0]` is ptyrunner, `readings[1]` is streamrunner. |
| `…:1483-1713` | The sweep. Doc (`:1483-1551`), clause A/B, the exemption (`:1605-1637`), the absence companion (`:1649-1712`) — the shape the new companion mirrors. |
| `…:2002-2113` | The leak sweep's key-name sub-test: the per-row `reading` field, the two OUTPUT assertions (`:2089-2099`), the "five of ten" precondition (`:2075-2079`). |
| `internal/e2e/realclaude/trailer_terminal_reason_test.go:87-133` | The reduction's value space. `trailReasonPresentOwesNone` (`:109`) and its claim limit (`:94-108`) — including *"Empty or named, both land here"* (`:105-108`). |
| `…:222-278` | `trailReasonAgainstPath` itself; the presence-side arm at `:236-244` whose Detail is the 395 B this ticket refuses to embed. |
| `internal/e2e/realclaude/trail_ptyrunner_composition_test.go:9-94` | `TestTrailComposesUnderAPtyrunnerReading`'s doc. **Carries a claim this arm falsifies** — `:29-33`, "trailGateUsable — an arm that ignores the runner path". Assertions stay unamended; that sentence does not. |
| `internal/e2e/realclaude/trail_run_outcome_test.go:398-431` | C2 and C4. Neither is amended; the design's job is to keep both green. |
| `internal/e2e/realclaude/background_reach_probe_test.go:123` | `reachMaxCommandBytes = 512`. |
| `internal/e2e/realclaude/result_trailer_observation_test.go:307-331` | `trailFixtureTrailer`, `trailPaddedTrailer`, `trailNeedle` (42 B). |

---

## Context

`trailGate` falls through to `trailGateUsable` for any non-empty `terminal_reason` other than
`max_turns`, and the Detail it publishes cites **ptyrunner's** `emitter.Close()` and reap defer
(`runner.go:479-485`, `:398`). On the stream path neither exists: `streamrunner.Run` tee-parses
claude's stdout and passes the bytes through unchanged, and the reap lives in `cmd.Cancel`, whose
own comment says it never fires on a clean exit. So a watchdog-killed stream run carrying
`"idle_stall"` is certified usable today, reaches `trailAdmitProof`, and lands on
`trailOutcomeRunningAtTrailer` — the probe's headline finding, from a hook that structurally cannot
have fired.

This ticket makes that input reach `trailGateOutOfContract` instead. C4 then forces `Admit` empty,
step 2 cannot fire, and the run lands on `trailOutcomeOutOfContract` with **no amendment to the
classifier at all**.

It is the #1420 step for the presence side: read the path, name the case, answer the shipped value.
The #1417 step — promoting it to a value of its own — is **#1434**.

---

## Size check

Confirmed **S**. Not overridden.

| Red line | Count | Verdict |
|---|---|---|
| >3 new files | 0 | pass |
| >~600 LOC total written | ~420 (≈15 arm + ≈180 new test code + ≈225 rewritten prose) | pass |
| >5 new exported types/interfaces | 0 (no new constant, no new type, no new closed-set member) | pass |
| >10 consumer call sites needing simultaneous update | 0 — no signature, type or interface changes. `codegraph_context` returns no cross-package consumer; `trailGate`'s callers are all in-package tests and none of their call expressions change. | pass |
| >5 acceptance criteria | 5 | pass |
| >~10 error/reject branches | +1 return site (ten → eleven) | pass |

**Not refactor-shaped.** Nothing is renamed and no signature moves, so there is no edit cascade —
the ~26 prose edits below are the ticket's *content*, each at a line this spec names, in a file the
developer reads once. No build breaks from a comment edit.

Sized against the nearest analogue commits rather than by feel: `faa3fbc` (#1420) changed **588
lines in this same file** plus four others; `573aca0` (#1417) touched sixteen files. Neither hit a
turn-budget incident (`docs/knowledge/codebase/1417.md`, `1419.md`, `1420.md` record none). This
ticket is structurally *smaller* than both — no closed set grows, no classifier arm is added, no
cross-file cite renumbering beyond one sentence.

**File-overlap check (2026-08-10, post-`git fetch --prune`):** the only remote `feature/*` branch
touching `internal/e2e/realclaude/` is `origin/feature/363`, and it touches `fixtures.go` +
its own spec — no overlap with this ticket's two files. No `blockedBy` needed.

---

## Design

### D0. The byte budget — settle it first, it decides the arm's whole shape

`trailDetail` caps at `reachMaxCommandBytes` (512) and `reachCapCommand` **truncates and marks
rather than failing**, so prose that outgrew the cap ships a severed sentence that still satisfies a
marker assertion (`:470-483`, `:1300-1310`).

Measured at `ffe4120`:

| thing | bytes |
|---|---|
| `trailReasonPresentOwesNone`'s Detail from the reduction | **395** (byte-identical for the named and the empty input) |
| `trailReasonPresentOwesNone` the constant | **32** |
| `trailNeedle` | **42** |
| cap | **512** |

**Decision: cite the constant, do not embed the Detail.** Embedding leaves 117 B for the arm's own
prose, and 75 B once the leak row requires the needle to have fitted. The embed-and-explain pattern
the three absence arms use does not fit here, and it is not needed: the marker assertions key on the
**constant** (`trailGateAbsenceCaseMarkers()` returns constants; the assertions are
`strings.Contains(Detail, marker)`), so citing it preserves every marker-shaped assertion, requires
no trim to the shipped reduction Detail, and creates no severed-sentence hazard.

**The arm's hard ceiling is 470 B**, not 512: the leak row asserts the 42 B needle would still have
fitted (AC2's non-vacuity). The constant is 32 of that, leaving **438 B of own prose**.

That is tight. This Detail is measured at **445 B — 25 B spare**, and is offered as a fitting
starting point rather than as required wording. Any reword must be re-measured, and the test in D4
is what reddens an overflow:

```go
trailDetail("%s: terminal_reason is on the line and the path owes none, so the line is not that "+
    "path's healthy shape and nothing is certified. NEVER that pyry wrote it — the path passes "+
    "claude's bytes through unchanged (streamrunner/runner.go:177-179), so claude produces the "+
    "same reading. About what the trailer CARRIED, never whether a process was alive. The "+
    "presence side of the absence reading; a value of its own is #1434", trailReasonPresentOwesNone)
```

For calibration: the same sentence written slightly longer measures 471 B and blows the ceiling by
one byte. Measure, do not estimate.

**The Detail must not interpolate `reason`.** The shipped usable and budget arms do, because they
*certify* it. This arm certifies nothing, so putting the decoded scalar into its Detail would put a
value from the trailer into a record that certifies nothing — AC2's "no value from the trailer
enters any record this ticket adds" — and would make the byte budget a function of untrusted input.
Fixed prose over this file's own constants and file cites, like every other arm.

### D1. The arm

Placed **after** the budget arm (`:537-548`), before the usable return (`:550-558`).

Contract, not implementation:

```go
// (the doc block D2 specifies goes here)
against := trailReasonAgainstPath(in.RunnerPath, in.Scan.KeyNames, reason)
if against.Value == trailReasonPresentOwesNone {
    return trailGateResult{
        Value:      trailGateOutOfContract,   // shipped value; no new member
        Detail:     trailDetail(/* D0 */),    // names the constant; embeds nothing
        RunnerPath: in.RunnerPath,            // echoed, like every other site
    }
}
return trailGateResult{ /* the shipped usable site, textually unchanged */ }
```

Four properties this shape buys, each load-bearing:

1. **Called, never re-switched** (AC1, Technical Notes). The reduction owns the six meanings; the
   gate consults one answer. It is an `if` and not a `switch` because exactly one of the reduction's
   answers diverts — the other two reachable here (`trailReasonNamedOwesOne` on ptyrunner,
   `trailReasonPathUnnamed` on an indeterminate reading) fall through to the shipped usable site,
   which stays the fall-through rather than becoming a `default:`.
2. **`Reason` stays the zero value.** C2 (`trail_run_outcome_test.go:398-409`) stays green
   unamended, C4 (`:423-431`) forces `Admit` empty, and `TestTrailGate`'s certification invariant
   (`:1112-1116`) holds without amendment.
3. **Eleven return sites, not twelve.** `trailReasonBlankOwesOne` is unreachable from here (`reason
   != ""` by the enclosing flow), so no site is spent on it.
4. **Presence still comes from the key names.** The reduction computes presence from `keyNames`
   internally (`trailer_terminal_reason_test.go:223`); the gate's `reason != ""` test discriminates
   *emptiness*, never presence. The collapse the key-name reading was landed to prevent
   (`:203-206`) is not reintroduced.

**One consequence to state at the arm rather than discover later.** A hand-built record whose
decoded `TerminalReason` is non-empty while `KeyNames` lacks `terminal_reason` reduces to an
*absence* answer, so the new arm does not fire and the shipped usable site answers. That record is
one `trailScan` cannot emit (it derives both from the same line), it is out of this ticket's stated
shape ("`terminal_reason` is present and non-empty"), and giving it an arm would spend a return site
no fixture reaches — the same argument `:461-468` makes about the absence switch's missing default.
Record it; do not defend it.

### D2. The two ordering decisions, recorded at the arm

Both are **decided here** and both must be stated in the arm's doc block, with their reasons. Return
order alone is not a record.

**Ordering 1 — `max_turns` on a path that owes none: the budget arm wins, so the new arm sits after
it.** The budget arm keys on `terminal_reason` alone, deliberately (`:337-342`), so such a trailer
has two candidate arms and the reduction answers `trailReasonPresentOwesNone` for it. The decision
is forced by what it does to the sweep, measured both ways at `ffe4120`:

| placement | sweep result |
|---|---|
| **after** the budget arm | only the usable row (`:872-878`) goes red → exemption set is exactly `{the usable row}` |
| before the budget arm | the usable row **and** the `max_turns` row (`:879-885`) both go red, all three comparisons each |

So placing it after is what buys AC4's "the usable row is the **only** row that declares
`pathVaries`". The substantive reason is the one already written at `trailAdmitVoidBudgetFired`
(`:171-174`) and `trailAdmitAttribution` (`:582-589`): **the budget void is STRUCTURAL and outranks
every reap-side void**, so it keeps its trailer.

**Ordering 2 — present-and-empty on a path that owes none: the shipped path-invariant arm keeps
winning.** The gate returns from its `reason == ""` branch (`:525-534`) before the non-empty path is
reached. The reduction **disagrees** — `trailReasonPresentOwesNone` absorbs both, deliberately
(*"Empty or named, both land here"*, `trailer_terminal_reason_test.go:105-108`), and its Detail is
byte-identical at 395 B for both inputs. Keeping #1419's arm winning is the expected answer and is
what preserves the present-and-empty record that the reduction's one value cannot express — the
`NO LIVE REPRO EXISTS` claim, which is true of a blank key and false of an absent one. The
divergence is deliberate, not an accident of return order, and the arm says so.

### D3. `trailGateCases()` — one row declares, the premise survives

- The usable row (`:872-878`) gains `pathVaries: true`, with a one-line comment saying why: since
  this ticket its arm diverts by the reading, and it is the **first row in the tree whose CERTIFIED
  REASON moves with the reading** — `"completed"` under a ptyrunner reading, nothing under a
  streamrunner one. That is what widens the exemption in D5.
- The `max_turns` row does **not** declare it. Ordering 1 is what buys that.
- **No row's `RunnerPath` changes.** All nine keep `trailRunnerUnread()`, so the uniform-path
  premise (`:860-869`) stays true and `TestTrailGateThenAdmit`'s `calls != 2` (`:1907`), its
  non-certifying switch (`:1890-1897`) and `TestTrailRunComposesWithGateCases`' want map stay
  unamended. Any row needing a runner-naming reading lives in D4's driver or D5's companion.
- **New fixture helper `trailGateUsableScan()`**, mirroring `trailGateAbsentReasonScan()` /
  `trailGateEmptyReasonScan()`: returns `trailScan([]byte(trailFixtureTrailer + "\n"))`. A function
  and not a package-level var, for the reason `:831-834` already states (the value holds a
  `*resultTrailer`, `-race` runs these tests in parallel). It exists so the usable row, D4's driver
  and D5's companion drive **one** fixture, and so the companion drives a helper rather than
  indexing the slice — the discipline `:1645-1647` already states for the absence companion.

### D4. The new driver — `TestTrailGateNamesThePresenceCaseOnAPathThatOwesNone`

Mirrors `TestTrailGateNamesWhichAbsenceCaseFired`'s shape (`:1311-1454`). Three rows, each with its
own `reading`. Scenarios, not code:

**Premise (before the table).** Pairwise containment between `trailReasonPresentOwesNone` and each
of `trailGateAbsenceCaseMarkers()`, **in both directions**, `t.Fatalf` on a hit — the shape
`:1312-1323` already uses. This is AC2's shipped assertion; the ticket's measurement of it is not
inherited. (`"reason-present-on-owes-none-path"` (32 B) vs `"reason-absent-on-owes-none-path"` (31),
`"reason-absent-on-owes-one-path"` (30), `"reason-path-names-no-runner"` (27) — the shared
`on-owes-none-path` suffix is exactly why this must be checked rather than assumed.)

**Row fields:** `scan`, `reading`, `wantValue`, `wantReason`, `wantMarker` (`""` means the presence
marker must be **absent**), `alsoCarries []string`.

| row | scan | reading | wantValue | wantReason | wantMarker | alsoCarries |
|---|---|---|---|---|---|---|
| **P1** the headline | `trailGateUsableScan()` | `tdnRunnerFromArgv(tdnFixtureStreamArgv)` | `trailGateOutOfContract` | `""` | `trailReasonPresentOwesNone` | the passthrough cite `streamrunner/runner.go:177-179`; the never-that-pyry-wrote-it clause |
| **P2** ordering 1 | `trailScan(trailPaddedTrailer(2000) + "\n")` | same | `trailGateBudgetFired` | `trailBudgetTerminalReason` | `""` | — |
| **P3** ordering 2 | `trailGateEmptyReasonScan()` | same | `trailGateOutOfContract` | `""` | `""` | `terminal_reason is empty`; `NO LIVE REPRO EXISTS` |

Per-row assertions:

- `got.Value == wantValue` (`t.Fatalf` — non-vacuity precondition; **six** arms answer
  `trailGateOutOfContract` after this ticket, so on P1 and P3 the value alone does not say which ran).
- `got.Reason == wantReason`.
- The presence marker is present on P1 and absent on P2/P3 — `strings.Contains(got.Detail,
  trailReasonPresentOwesNone)`.
- Every `alsoCarries` phrase present.
- **Headroom on the OUTPUT, every row:** no `reachTruncationMarker`, and
  `len(got.Detail)+len(trailNeedle) <= reachMaxCommandBytes`. This is the 470 B ceiling made
  executable and is what reddens a reword that overflows.

**Why P2 and P3 are not "some arm fired" assertions** (AC3): P2 asserts the *value* — a swap of the
two arms moves it from `gate-budget-fired` to `gate-out-of-contract` and it goes red. P3 cannot use
the value, because this ticket's arm answers the **same** `trailGateOutOfContract` the
present-and-empty arm answers; it asserts the **case named in the Detail**, in the shape `:1171-1177`
already uses for the absence markers.

**Mutants this driver is the sole red for:**

| mutant | sole red |
|---|---|
| the arm fires ahead of the budget arm | P2 (value) |
| the arm swallows the present-and-empty case | P3 (marker appears; `alsoCarries` lost) |
| the arm certifies a reason | P1 (`Reason`), plus an independent red at C2 |
| the arm's Detail outgrows the ceiling | P1 (headroom) |

The remaining mutant — **awarding the new answer on a ptyrunner reading** — is D5's companion's sole
red, by Technical Notes' own assignment. `trailRunnerUnread()` rows are covered there too.

Deferred to #1434's matrix ticket and named as deferred: discriminating on the literal
`"idle_stall"` rather than on the path, and the two indeterminate-reading confusions.

### D5. The sweep — widen the exemption, pay for it positively

**Code change (`:1605-1637`).** Move the certified-reason comparison **below** the `pathVaries`
guard, so the exemption covers `Value`, `Reason` and `Detail` together:

```go
if tc.pathVaries {
    continue                       // value, certified reason AND Detail exempt here and only here
}
if got.Reason != base.Reason { … } // now inside the non-declaring path
if got.Value  != base.Value  { … }
if !bytes.Equal(…)           { … }
```

Clause B (`:1586-1590`) stays **above** the guard and keeps running unconditionally on every row and
every reading. That is non-negotiable: it is what proves all eleven sites echo `RunnerPath`.

**Three argument sites must be rewritten, not renumbered** (AC4) — all three assert the same
now-false thing:

1. `:1528-1533` — the doc bullet ending *"Narrowing it to match the value's exemption would be
   under-delivery: **nothing in the tree forces it**."* This arm forces it, and is the first thing in
   the tree to: every path-varying arm so far certifies nothing on every reading, whereas this one
   certifies `"completed"` under a ptyrunner reading and nothing under a streamrunner one. The
   replacement must also state **what the sweep no longer covers there** and where it is paid for
   (the companion below, per reading, value *and* certified reason).
2. `:1605-1609` — the inline guard comment (*"NOT exempted on pathVaries rows, deliberately … all
   three absence cases certify nothing whatever the reading, so this stays green there"*).
3. `:1610-1614` — the error message's universal *"NO arm may vary what it certifies by the runner
   path, the absence arm included"*. The widened exemption makes that untrue as stated. Rewrite in
   the shape its two neighbours already use: *this row does not declare `pathVaries`, so its arm
   ignores the path entirely and cannot vary what it certifies.*

**New companion sub-test — "the usable arm diverts on one reading".** Mirrors `:1649-1712` exactly.
Drives `trailGateUsableScan()` once, assigns only `RunnerPath` per reading (the aliasing discipline
at `:1543-1551`), and asserts per reading the **triple**:

| reading | value | certified reason | marker |
|---|---|---|---|
| 0 ptyrunner | `trailGateUsable` | `"completed"` | none |
| 1 streamrunner | `trailGateOutOfContract` | `""` | `trailReasonPresentOwesNone` |
| 2, 3, 4 indeterminate | `trailGateUsable` | `"completed"` | none |

Plus, mirroring the absence companion: a `len(want) != len(readings)` guard, and a **distinct-Detail
count pinned at exactly two** — one for the four usable readings, one for the diverted reading. That
pin is what replaces the byte comparison the exemption withdrew: it proves the usable arm's Detail is
still one fixed string across the four readings that reach it.

Derivation of the table, so it is not copied: `trailFixtureTrailer` carries a present, non-empty
`terminal_reason`, so the reduction answers `trailReasonNamedOwesOne` under reading 0,
`trailReasonPresentOwesNone` under reading 1, and `trailReasonPathUnnamed` under 2–4. Only the second
matches the arm's key.

### D6. The leak sweep must reach the new arm

`TestTrailAdmissibilityRecordsCarryNoCapturedBytes`' key-name sub-test (`:2002`) gains a **fourth**
row. The three shipped rows all hand `Trailer: &resultTrailer{Type: "result"}` — whose
`TerminalReason` is `""` — so they route into the empty branch and cannot reach the new arm. The row
struct therefore gains a `reason string` field, and the `resultTrailer` is built per row as
`&resultTrailer{Type: "result", TerminalReason: tc.reason}` (the literal is already constructed
inside the loop, so no fixture is shared).

| field | value |
|---|---|
| `keyNames` | `{"result", trailNeedle, trailReasonKeyName, "type"}` — the needle plants as a key name, and `trailReasonKeyName` is what makes the reduction read PRESENT |
| `reason` | a plain source literal, e.g. `"completed"` — **never `trailNeedle`**. The needle's job on this row is to test the KEY-NAME channel; planting it in the decoded reason instead would test a channel the shipped usable arm legitimately publishes (`Reason`), so a mutant that made the new arm certify would redden the leak sweep rather than the `Reason` assertion and lose its sole red |
| `reading` | `tdnRunnerFromArgv(tdnFixtureStreamArgv)` |
| `want` | `trailGateOutOfContract` |
| `marker` | `trailReasonPresentOwesNone` |

The three shipped rows get `reason: ""`, which is their current behaviour exactly. Both OUTPUT
assertions (`:2089-2099`) keep running on the new row and are what make its no-captured-bytes claim
non-vacuous at 445 B + 42 B = 487 B against the 512 B cap.

`:2075-2079`'s precondition — *"five of trailGate's ten return sites answer it"* — becomes **six of
eleven**.

### D7. Counts — derived from the eleven sites, never adjusted by one

The enumeration everything below is derived from:

| # | site | value | path consulted by the arm? |
|---|---|---|---|
| 1 | `:370` state not one of three | `OutOfContract` | no |
| 2 | `:383` absent state | `NoTrailer` | no |
| 3 | `:392` aborted | `ScanAborted` | no |
| 4 | `:407` nil trailer | `OutOfContract` | no |
| 5 | `:499` absence / owes one | `OutOfContract` | **yes** |
| 6 | `:508` absence / owes none | `AbsentOwesNone` | **yes** |
| 7 | `:517` absence / path unnamed | `OutOfContract` | **yes** |
| 8 | `:525` present-and-blank | `OutOfContract` | no |
| 9 | `:538` budget | `BudgetFired` | no |
| **10** | **NEW** present-and-named / owes none | `OutOfContract` | **yes** |
| 11 | `:550` usable | `Usable` | no |

Verified against the shipped tree: `grep -c 'return trailGateResult{'` = 10 today, of which exactly
five answer `trailGateOutOfContract` (`:370`, `:407`, `:499`, `:517`, `:525`). After: eleven and six.

**`:344` heading** → "CARRIED to all **eleven** return sites and READ at **four**."

**`:346-351` — four claims that move differently.** Derived, per the ticket's own table, with one
correction:

| clause | today | after | derivation |
|---|---|---|---|
| "Seven of the ten are decided without consulting it at all" | 7 of 10 | **7 of eleven** | 11 − 4 consulting sites (5, 6, 7, 10). The seven does not move; the new site is a **fourth** that *is* decided by the path. |
| "the reading now decides the VALUE at one of the ten sites" | one | **see below** | |
| "and the DETAIL at three of them" | three | **four** | sites 5, 6, 7, 10 — the four whose Detail names a path-derived case |
| "the site count itself is unchanged" | true of #1417 | **false — this is the ticket that changes it** | ten → eleven |

On the VALUE clause: the ticket's table gives "two — the new site and the usable site it diverts
from", which names the two sites this arm adds but drops the one #1417 added. Derived honestly, the
reading decides the value for **two input shapes across three sites**: the absence shape, where site
6 answers a different value from its two siblings (#1417's), and the present-and-named shape, where
the reading picks between site 10 and site 11 (this ticket's). Write the sentence about the shapes,
not about a bare numeral — a numeral here is what went stale twice already.

**`:1503-1510` — "at any single reading they reach eight" is derived, and it does NOT move.** With
eleven sites, one reading still leaves three unreached: two of the three absence sites, and exactly
one of `{site 10, site 11}`. Enumerated at any single reading: site 1 (shared by "a state nobody
defined" and "the zero scan result"), 2, 3, 4, 8, 9, one of {5,6,7}, one of {10,11} = **eight**. The
clause that changes is "the nine rows reach all ten" → **all eleven**, and its *because* gains a
third term: the usable row reaches exactly one of the usable site and the presence site.

**`:461-468` — re-state the argument, do not renumber it.** The shipped argument is that an eleventh
site would be one no fixture row can reach, forcing clause B to weaken from "the nine rows reach all
ten" to "reach most of". After this ticket the count *is* eleven and the eleventh site **is** reached
— by `trailGateCases()`' usable row under `readings[1]`. The re-statement: the absence switch still
has no default guard, for the unchanged reason that a fourth site *there* is unreachable by any
fixture row; the rule was never "ten sites", it is "**every site a fixture row reaches**", and this
ticket's site satisfies it, which is why clause B stays total.

**`:145-156` — `trailGateOutOfContract`'s enumerated sub-cases, FIVE → SIX**, with the new one named
in the enumeration (a present-and-named `terminal_reason` on a path that owes NONE) and flagged as
the **first PRESENCE case** in that value. `:155`'s "the five that remain" moves with it.

**The "five arms answer it" claim lives at FOUR sites, not three.** The ticket's AC5 enumerates
three; a residual sweep of the shipped tree finds a fourth. All four become six:

| site | form | text |
|---|---|---|
| `:2075-2079` | comment | "five of trailGate's ten return sites answer it" → six of eleven |
| `:1124-1132` | comment, **enumerates the five inline** | the sixth must be named in the enumeration, and `:1134-1139`'s "Four of the five are covered here; the one that is not…" becomes "four of the six … the **two** that are not", naming this ticket's driver alongside `TestTrailGateNamesWhichAbsenceCaseFired` |
| `:1396-1397` | **live `t.Fatalf` format string** | "five arms answer it" → six. A sweep reading only `//` lines misses this one |
| **`:1243`** | comment — **missed by the ticket's enumeration** | `TestTrailGateNamesWhichAbsenceCaseFired`'s doc: "Five arms answer `trailGateOutOfContract`, so on R1, R3 and R4 got.Value says nothing about WHICH one ran" → six |

**Verified unchanged — do not touch:** `trailGateResult.Reason`'s doc "empty on the four that do not"
(`:256-261`) counts *values*, and no value is added. `TestTrailGateThenAdmit`'s "FOUR since #1417"
(`:1878`) counts the same four. `trailIsGateValue` (`:732`),
`TestTrailAdmissibilityConstantsAreClosed` (`:988`) and the non-certifying switch (`:1890-1897`) all
stay unamended — **no closed set grows in this ticket.**

### D8. Claims this arm falsifies, and the two `#1369` pointers

Every site below asserts something that becomes false the moment the arm lands. All are in
`trailer_admissibility_test.go` unless named otherwise.

| site | shipped claim | why it breaks |
|---|---|---|
| `:136-138` | the presence shape is "#1369's sibling shape, **reaching `trailGateUsable` today**" | at a streamrunner reading it now reaches the new arm. **An asserting cite, not a naming one** — a renumber does not fix it; the sentence must be rewritten to say where the shape lands and that promotion is #1434 |
| `:495` | "The sibling shape … is **#1369's**; these arms stay silent about it" | a *naming* cite: the absence arms do still stay silent, so only the issue number is wrong. Re-point to #1433 (this arm) and #1434 (the promotion) |
| `:222`, `:228-232` | heading "# Exactly one arm reads it"; "Every other arm ignores the field today" | two arms read it |
| `:263-267` | `RunnerPath` field doc: "The ABSENCE arm consults it since #1420 and **no other arm does**" | false |
| `:437-440` | "the absence branch is where #1420 reads `in.RunnerPath`, and it is the **ONLY branch in this function** that does" | false |
| `:360-362` | "the **declaring row's** variance the same test then proves positively" | two declaring… no — one declaring row, but **two** proven arms and two companions; the sentence must cover both |
| `:772-773` | `pathVaries` doc enumerating only "its VALUE and its DETAIL" as what may differ | the certified reason may now differ too |
| `:860-869` | `trailGateCases()` doc: "the gate's **absence arm** does read the path now"; "the per-case proof therefore lives in **its own driver** (`TestTrailGateNamesWhichAbsenceCaseFired`)" | two arms, two drivers |
| `:933` | row nine: "**Three of ten** rather than one of eight" | three of eleven. (Its trailing "which is why the exemption below is scoped to the byte comparison" was **already** stale at #1417, which added the value to the exemption; correct it in the same edit rather than leaving a half-true sentence behind.) |
| `:937` | row nine is "the **ONE** row whose Detail depends on the reading" | the usable row's does too |
| `:1512` | heading "# The exemption covers the Detail AND the value, **on one row**" | and the certified reason |
| `:1536` | "silent about **the only arm** it no longer covers, so the final sub-test drives that arm" | two arms, two companions |
| `:1483-1487` | "the claim that is true after #1420 and **stays true when a second arm reads the path**" | this ticket *is* that second arm; the sentence should record it happened rather than anticipate it |
| `trail_ptyrunner_composition_test.go:29-33` | "driven over `trailFixtureTrailer`, which reaches `trailGateUsable` — **an arm that ignores the runner path** — so THIS composition cannot vary by it" | the route into that arm now depends on the reading. The composition is still green **because its reading reduces to ptyrunner**, which is a different and now-load-bearing reason. Its **assertions stay unamended** (AC2); this sentence does not |

**Scope of the `#1369` re-point: the two code sites only.** `docs/specs/architecture/1417-*.md`,
`1420-*.md` and `docs/knowledge/codebase/1417.md` also name #1369; those are frozen per-ticket build
artifacts describing what was true when they were written, and re-pointing them would rewrite
history. Leave them.

### D9. The residual sweep (AC5's own requirement)

Run both forms over `trailer_admissibility_test.go`, and read **format strings as well as
comments** — one of the four "five arms" sites is only reachable that way, and #1419's own note
(`docs/knowledge/codebase/1419.md:86`) records a numeral-form miss caught only by code review:

```bash
grep -nE '\b(three|four|five|six|seven|eight|nine|ten|eleven)\b' \
  internal/e2e/realclaude/trailer_admissibility_test.go
grep -nE '\b(3|4|5|6|7|8|9|10|11)\b' \
  internal/e2e/realclaude/trailer_admissibility_test.go
```

Where a count is written as a numeral, prefer rewriting it as a **word** — that is what keeps the
next ticket's word-sweep able to find it.

---

## What must not move

- No new constant, no new gate value, no new run outcome. `trailIsGateValue`,
  `TestTrailAdmissibilityConstantsAreClosed` and `trailClassifyRun` are untouched. A diff that grows
  one of them belongs to #1434.
- `trailGateAbsenceCaseMarkers()` does **not** gain `trailReasonPresentOwesNone` — it is a presence
  case. `:1171-1177` and `TestTrailGateNamesWhichAbsenceCaseFired`'s R4 row stay green unamended.
- `trailReasonAgainstPath` and its file are **read-only** here. No arm of it changes; no Detail of it
  is trimmed.
- Every `trailGateCases()` row keeps `trailRunnerUnread()`.
- The ptyrunner side is byte-identical from the same inputs.
- No documentation outside `docs/specs/architecture/1433-*.md` is written by this ticket — the
  feature-doc and knowledge-doc updates belong to the documentation phase.

---

## Error handling

`trailGate` is pure, returns no error, takes no `*testing.T` and never fails a test. The new arm
inherits that contract exactly: it reports a datum. There is no failure mode to recover from — an
input outside the shape reaches an existing site, and the site it reaches is enumerated in D7.

The one hazard with teeth is the cap: `reachCapCommand` truncates and marks rather than failing, so a
Detail that outgrew 470 B ships a severed sentence that still satisfies its marker assertion. D4's
per-row headroom assertion and D6's `n+len(trailNeedle) > reachMaxCommandBytes` check are the two
tripwires, both **on the output**.

---

## Testing strategy

Everything is offline. Measured at `ffe4120` with `ANTHROPIC_API_KEY` and `CLAUDE_CODE_OAUTH_TOKEN`
stripped, `-run '^TestTrail'` reports **149 RUN, 0 SKIP, 0 FAIL** — the `e2e_realclaude` build tag is
not by itself a live-claude marker, which is why this ticket carries no `needs-real-claude` label.

```bash
go test -count=1 -race -tags e2e_realclaude -run '^TestTrail' ./internal/e2e/realclaude/
```

The tag matters: `make check` does not compile build-tagged files, so a green `make check` says
nothing about this diff. Run the tagged command and read the counts.

Order of work that keeps the feedback loop short:

1. Land the arm (D1) with a first-draft Detail and the new driver (D4). Run the tagged suite — the
   shipped sweep should go red exactly three ways on the usable row, at `:1610`, `:1626` and `:1632`.
   Seeing those three confirms the arm is placed correctly *before* any of the sweep work.
2. Set `pathVaries: true` on the usable row. Two of the three clear; **the certified-reason red
   stands** — that is the measured proof that the shipped exemption mechanism is necessary and not
   sufficient, and it is what forces D5.
3. Widen the exemption and add the companion (D5).
4. Leak-sweep row (D6), then the counts and claims (D7, D8), then the residual sweep (D9).

Mutation demonstrations, in the shape this family uses — `go test -overlay=<abs-path json>` against a
copy of the file, so no worktree write is needed. Each must be the **sole** red for its row:

- the new answer awarded on a ptyrunner reading → D5's companion, reading 0
- the arm placed before the budget arm → D4's P2
- the arm reached by present-and-empty → D4's P3
- the arm certifying a reason → D4's P1 (`Reason`) and C2 independently

Note for anyone re-measuring line numbers under an overlay: an overlay that **inserts** lines above a
site reports the shifted number. Shipped = measured − lines inserted above. The ticket's own
`:1610` / `:1626` / `:1632` are the shipped numbers, already corrected from a draft that quoted the
overlay's `:1620` / `:1636` / `:1642` minus a wrong offset.

---

## Handoff to documentation

Not developer deliverables — the documentation phase writes these after the PR merges:

- `docs/knowledge/codebase/1433.md` — the arm, the two ordering decisions, the cite-the-constant
  byte resolution, and the four-site "five arms" residual (one of which the ticket's own enumeration
  missed).
- `docs/knowledge/features/e2e-realclaude.md` carries `trailGate` arm/row counts, last updated by
  #1419/#1420. Ten → eleven return sites, five → six out-of-contract arms, and one more declaring row
  in the sweep.

---

## Open questions

1. **Does #1434 fold the present-and-empty case in?** This ticket keeps #1419's arm winning, so
   present-and-empty on a streamrunner reading is answered path-invariantly while the reduction
   absorbs both. When #1434 promotes the presence case to a value of its own it will have to decide
   whether that value also claims the blank input — and if it does, the `NO LIVE REPRO EXISTS`
   scoping has to survive the move. Recorded here; not decided here.
2. **The inconsistent hand-built record** (decoded non-empty, key name absent) reaches the usable
   site. Unproducible by `trailScan` and out of this ticket's shape; if a future gather can produce
   it, it needs its own arm rather than a widening of this one.
3. **Live reachability is unchanged.** Both shipped gathers fill `RunnerPath` with
   `trailRunnerUnread()`, so no live run reaches the new arm. Supplying a live reading remains
   unowned, exactly as `trail_ptyrunner_composition_test.go:15-26` records.

---

## Security review

**Verdict:** PASS

The asset here is the **published probe record**: these Details land in a public GitHub issue
unreviewed, and the run-level outcome is a verdict about pyry. So the two threats with teeth are
*captured bytes reaching a published artifact* and *untrusted output manufacturing a positive
verdict*. Both are walked below against the design as specified, not against the ticket's summary.

**Findings:**

- **[Trust boundaries]** No finding, by an enumerated decision rather than by inspection. The new arm
  **reads** two untrusted values — `in.Scan.KeyNames` (key names off claude's line, unbounded and
  attacker-influenced in principle, per the leak sweep's own statement at
  `trailer_admissibility_test.go:2106-2109`) and `reason` (decoded model output) — and **publishes
  neither**. Its `Value` is a shipped constant, its `Reason` is empty, its `RunnerPath` is
  `tdnRunnerFromArgv`'s constant answer, and its `Detail` is fixed prose plus the 32 B
  `trailReasonPresentOwesNone` constant. The one path by which an untrusted byte could reach the
  record is interpolating `reason`, which **§ D0 forbids explicitly** and which the extended leak
  sweep (§ D6) enforces on the marshalled output. The reduction it calls publishes nothing either:
  every arm of `trailReasonAgainstPath` is fixed prose over its own file's constants, stated at
  `trailer_terminal_reason_test.go:208-221` and verified by reading `:226-269`.

- **[Threat model — positive verdict from untrusted output]** No finding; this is the ticket's
  purpose. It removes the only path from a stream-path trailer to `trailAdmitProof` →
  `trailOutcomeRunningAtTrailer`. The **inverse** hazard was checked and is absent: an attacker who
  controls claude's *output* cannot move the runner reading, because that reading is reduced from the
  process table's argv (`tdnRunnerFromArgv`) and never from the trailer — so a genuine ptyrunner
  proof cannot be voided by a crafted trailer. An attacker controlling the *argv* already controls
  the run.

- **[Threat model — a void must not read as a negative]** Addressed, with the residual named. The arm
  answers `trailGateOutOfContract`, whose documented meaning is "the input is not a reading", so the
  shape is filed as a caller's bug rather than as a measured void. That is a **known and stated**
  under-description — the shape is a *reading* — and it is owned by **#1434**, which promotes it to a
  value of its own. Named as out of scope with its owner, per the ticket's own § "What this ticket
  deliberately leaves undone".

- **[Error messages, logs, telemetry]** SHOULD FIX, folded into § D6 rather than left as a note: the
  leak-sweep row's `reason` must be a source literal and never `trailNeedle`. Planted in the decoded
  reason, the needle would exercise a channel the shipped usable arm legitimately publishes, so a
  mutant that made the new arm certify would redden the leak sweep instead of the `Reason` assertion
  and lose its sole red. No other new message prints an untrusted value: the driver's failure output
  prints `got.Detail` (fixed prose by construction) and marker constants, the same shape as
  `:1101` and `:1425-1428`.

- **[Coverage withdrawn by the widened exemption]** The principal design risk, and it is mitigated
  with **different fabric**. Widening the exemption to cover `Reason` stops the sweep from checking
  that the declaring row's certification is path-invariant — the property whose whole point was
  "a path-varying arm must not quietly start to certify". Two independent detectors replace it:
  § D5's companion asserts the certified reason **positively, per reading** (`"completed"` at
  readings 0/2/3/4, `""` at reading 1), and `trailClassifyRun`'s C2
  (`trail_run_outcome_test.go:398-409`) rejects a non-certifying gate value carrying a reason at a
  different layer entirely. The distinct-Detail pin of exactly two replaces the withdrawn byte
  comparison. AC4's requirement that this be proven positively is what makes the withdrawal safe.

- **[Network & I/O — input size]** No finding for this ticket; one pre-existing property named. The
  new arm's Detail length is **independent of input size** (fixed prose, 445 B measured, 25 B under
  the 470 B ceiling), so no untrusted string can drive it into the cap. The **shipped** usable arm
  does interpolate the unbounded decoded `reason` into a 512 B capped Detail; `reachCapCommand`
  truncates and marks rather than overflowing, so it is bounded, and this ticket **narrows** its
  reach by diverting one class of input away from it. Out of scope, unowned, and not made worse here.

- **[Subprocess execution]** No finding, stated as a symbol list rather than as a grep for `exec.` —
  a grep reads clean whenever the exec sits inside a helper. The new code calls exactly
  `trailReasonAgainstPath`, `trailDetail` (→ `fmt.Sprintf`, `reachCapCommand`), and in tests
  `trailScan`, `trailGateUsableScan`, `trailPaddedTrailer`, `tdnRunnerFromArgv`, `strings.Contains`,
  `json.Marshal`, `bytes.Contains`. All are pure over their arguments; `tdnRunnerFromArgv` reduces an
  argv **string** and is driven here over source-constant fixtures, never over `tdnClaudeCommand`,
  which is the symbol that reads the process table.

- **[Concurrency]** No finding, by two stated disciplines. `trailGateUsableScan()` is a **function
  and not a package-level var**, because its result holds a `*resultTrailer` and a `[]string` and
  `-race` runs this package's tests in parallel — a shared backing array would let one row's mutation
  reach another's (`trailer_admissibility_test.go:831-834`). The new companion copies the input per
  reading and assigns **only** `RunnerPath`, never writing through the aliased pointer
  (`:1543-1551`). `trailGate` holds no lock and needs none: pure, no shared state, no goroutine.

- **[Tokens / credentials]** Not applicable, by measurement rather than by assumption: the whole
  suite runs with `ANTHROPIC_API_KEY` and `CLAUDE_CODE_OAUTH_TOKEN` stripped (149 RUN, 0 SKIP, 0
  FAIL), and this ticket adds no env read, no exec, no network and no clock. `trailGate`'s purity
  contract (`:316-320`) is inherited unchanged.

- **[File operations]** Not applicable: no path is constructed, no file is opened, nothing is
  written. The arm is pure over its input struct.

- **[Cryptographic primitives]** Not applicable: no randomness of any kind, which is also what keeps
  the driver and the companion deterministic — every reading is obtained by driving the shipped
  reader over a source-constant argv.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-08-10
