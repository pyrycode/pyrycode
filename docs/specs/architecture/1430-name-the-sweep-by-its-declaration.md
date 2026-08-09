# #1430 — Name the runner-path sweep after the per-row declaration it keys on

**Size:** XS (PO's `size:xs` confirmed, not overridden). **Production source files touched: 0** — every Go file in scope is `_test.go`.
All facts below were re-derived against `e20f4d2`, which is this worktree's `HEAD`.

---

## Files to read first

| Read | What to extract |
|---|---|
| `internal/e2e/realclaude/trailer_admissibility_test.go:1483-1552` | The renamed test's own doc comment **and** its opening sentence — site A, and the biggest single finding in this spec. Note the doc already carries the declaration-keyed truth at `:1520-1521` ("on exactly the rows that declare pathVaries") while opening with the arm-keyed one. |
| `…:1575-1640` | The sweep body. `:1615-1624` is the `pathVaries` exemption (value **and** bytes); `:1625-1637` is the undeclared-row invariance. This is the "both directions" the new name claims. |
| `…:1642-1712` | The companion sub-test — proves the declaring row's variance positively (three distinct Details across five readings). Do not touch; it is the other half of what makes a declaration-keyed name accurate. |
| `…:760-781` | `trailGateCase`, with `pathVaries` at `:772-780` — site D. |
| `…:224-232` | `trailGateInput`'s doc — site B. |
| `…:353-365` | The Detail-is-fixed-prose doc — site C. `:364` is cited by name from `trail_ptyrunner_composition_test.go:55`, one line below the block being rewritten. |
| `internal/e2e/realclaude/trail_ptyrunner_composition_test.go:28-60` | The file header. `:32-34` is the retraction that makes site E's message stale; `:53-60` is the surviving reason site E must argue from. Also `:56`'s bare `(:1585-1590)`. |
| `…:196-215` | Site E's assertion, its message, and the sibling at `:205-208` that stays untouched. |
| `internal/e2e/realclaude/trail_run_outcome_test.go:1213-1220` | A pure naming cite — already row-scoped and already says "declared". Substitution only; it is the model the other cites should read like. |
| `docs/knowledge/features/e2e-realclaude.md:786-802` | Site F, and the two claims that must be separated. |
| `docs/specs/architecture/1414-runner-reading-is-a-property-not-a-gap.md:47-70` | **The house idiom for this exact constraint.** Per-block line-count neutrality, the stop condition, and why `--numstat` equality is necessary but not sufficient. Follow it. |

---

## Context

Four places describe the gate's path-reading **by arm**; the tree stopped keying on the arm at #1420 and keys on a per-row `pathVaries` declaration. The ticket enumerates those four. This spec adds **three more**, all in Go, all left contradicting by a literal reading of the ACs — see § The gap.

### Ticket premises, re-verified at `e20f4d2`

Every load-bearing number in the body checks out. Recorded so the developer does not re-measure:

| Claim | Verified |
|---|---|
| Identifier appears 12× across 3 Go files, at the exact lines listed | ✅ exact match |
| Every Go occurrence is a comment or the declaration; none inside `t.Errorf`/`t.Fatalf` | ✅ |
| `TestTrailGateReadsTheRunnerPathOnlyWhereARowDeclaresIt` is 54 chars, same as the old name | ✅ both 54 |
| That name is collision-free repo-wide | ✅ zero hits, all file types |
| 109 line-anchored cites into the three files from Go sources (32 / 74 / 3) | ✅ 32 + 74 + 3 = 109 |
| 9 bare `(:NNN)` back-references inside them | ✅ 6 + 2 + 1 = 9 |
| The 12 sites sit on lines 69–83 chars long | ✅ min 69, max 83 |
| `:1585-1590` is clause B (`got.RunnerPath != reading` + its `t.Fatalf`) | ✅ `:1585` is its comment, `:1586-1590` the check |
| The markdown residual is exactly `codebase/1417.md`, `codebase/1420.md`, `e2e-realclaude.md:1734-1736`, `docs/specs/` | ✅ complete and exact; `:1734-1736` are `- Ticket [#…]` log lines |
| Provable offline | ✅ `go test -count=1 -tags e2e_realclaude -run '^TestTrail' ./internal/e2e/realclaude/` → `ok … 3.388s`, no creds |

**File-overlap check (§1.5): clean.** `git fetch origin --prune` then a scan of every `origin/feature/<N>` branch against the four files in scope found no overlap. No `blockedBy` needed.

---

## The gap: three Go cites that "re-pointed" does not repair

AC2 obliges the 12 Go occurrences to be "re-pointed". For nine of them, substituting the identifier is the whole fix — they *name* the test as a pointer. Three of them **assert what the test proves**, or **describe its mechanism as arm-keyed**. Substitution leaves those claims standing, now under a name that says the opposite. The user story is explicit that this is in scope: *"a reader trusting its name **and its supporting prose** builds the model the shipped contract enforces"*.

A literal-compliant implementation passes AC1's grep, AC2's re-point, AC3, AC4 and AC5 while shipping three self-contradictions. AC5 permits the repair — every changed line is a comment.

Classification of all 12:

| Site | Kind | Action |
|---|---|---|
| `trailer_admissibility_test.go:229` | **Asserting** — "Every other arm ignores the field, and \<N\> is what makes that narrower claim a proof rather than a claim" | **Site B.** Word-for-word the defect the ticket names in §4, in Go rather than markdown. |
| `…:359` | **Mechanism, arm-keyed** — "\<N\> … requires a BYTE-IDENTICAL Detail across all five on every row but the absence one" | **Site C.** Sharpest of the three: after substitution the name and the contradicting clause sit in one sentence. |
| `…:1483` | **Asserting** — "\<N\> is AC2 made deterministic, under the NARROWER claim … EVERY ARM EXCEPT THE ABSENCE ONE ignores it" | **Site A.** The renamed test's own opening sentence — the most-read location, and a Go doc comment leads with the identifier. |
| `…:466`, `:868`, `:929`, `:1356` | Naming | Substitution only. |
| `…:774` | Field doc | Site D (AC3). |
| `…:1552` | Declaration | Substitution only. |
| `trail_ptyrunner_composition_test.go:42`, `:56` | Naming | Substitution only. `:56`'s bare `(:1585-1590)` must still resolve — see § Neutrality. |
| `trail_run_outcome_test.go:1216` | Naming, already row-scoped | Substitution only. |

---

## Design

### The name

Use **`TestTrailGateReadsTheRunnerPathOnlyWhereARowDeclaresIt`** — 54 characters, byte-for-byte the same length as the name it replaces, verified collision-free.

It satisfies AC1's property: true while the absence branch is the only path reader, and still true after #1427's presence-side arm lands, because it quantifies over *rows that declare* rather than over arms.

**On its accuracy, since the register is loose in both directions.** Read strictly, the gate does not know about rows — what the sweep proves is that the gate's *decision* varies by the reading only on rows declaring `pathVaries`, and clause B separately proves every arm *carries* the path. "Reads" is therefore shorthand for "reads into the decision". That is exactly as tight as "Ignores" was in the outgoing name, which had the complementary looseness: every arm echoes the path, so no arm literally ignored it. The register is the file's own, and site A's rewritten doc states the precise mechanism three lines below. Accepted.

**Any alternative must be ≤ 54 characters.** Longer is not forbidden by AC5, but the 12 sites sit on lines already 69–83 chars in a file that wraps around 80, and a longer name invites a re-wrap — which is the one edit that breaks neutrality. Shorter is safe. Either way: **never re-flow a comment paragraph.** `gofmt` does not rewrap comments (verified: a 100-column comment passes `gofmt -l` untouched), so no tool will force it and no tool will catch it.

### The seven edit sites

Per-site **obligations and budgets**. The line budget is hard; see § Neutrality.

| # | Site | Lines | Budget | Obligation |
|---|---|---|---|---|
| **A** | `trailer_admissibility_test.go:1483-1487` | 5 | ~80 cols, `// ` prefix | Opening sentence states what the sweep proves: a row's decision may vary by the reading **only where the row declares `pathVaries`**; undeclared rows are invariant in value, certified reason and Detail. Must **not** say "every arm except the absence one". Go doc convention: lead with the new identifier. The block's existing `:1520-1521` declaration-keyed reasoning already agrees — this makes the opening agree with it. |
| **B** | `…:228-232` | 5 | ~80 cols | Keep the true fact that the absence arm is today's only decision-side reader (`:224-227` is out of scope and already correct). Replace the attribution: the sweep proves the **row-scoped, declaration-keyed** property, and the arm-level statement is an inference from today's row set. Keep the companion-sub-test clause. Preserve the count word **"one"** (one exempted/declaring row — true either way). |
| **C** | `…:358-362` | 5 | ~80 cols | Replace "on every row but the absence one" with the declaration-keyed scope. Preserve the numerals **"five"** (twice: "five distinct answers", "all five"). Keep "whose variance the same test then proves positively". |
| **D** | `…:772-774` | 3 (inside the 772-780 block) | ~80 cols | AC3. Drop "which is true of exactly the rows reaching the absence arm". State what the field *means*: the row declares that **its own arm reads the runner path**, so its **value and its Detail** may both differ across the readings. Note "value **and** Detail" is a second correction the AC's own phrasing implies — the shipped text says "DETAIL" alone, but `:1615-1624` has exempted the value too since #1417, and `:1512` already says so. `:775-780` ("DECLARED rather than detected" … "goes red, which is the direction that matters") is **kept verbatim**. |
| **E** | `trail_ptyrunner_composition_test.go:199-201` | 3 | ~94 cols; `t.Errorf` continuation strings | AC4. Drop "no arm reads the path today". Argue from `:53-60`: the sweep is over the **Detail string** and never the marshalled record, because `trailGateResult` carries the reading in its own `RunnerPath` field by design and clause B requires it to arrive intact — so an interpolated reading in the Detail is a republication, not the carriage. **The `if strings.Contains(gate.Detail, reading)` condition, the format verbs and the argument list are unchanged.** The sibling at `:205-208` is not touched. |
| **F** | `docs/knowledge/features/e2e-realclaude.md:796-801` | 6 | markdown, 2-space continuation indent | AC2. Keep "Until #1420 no arm read it; since #1420 exactly one does — the absence arm" (a true fact about the gate). Keep **`9 rows × 5`** and **`readings`** verbatim. Delete "the narrower claim is **proved, not stated**, by". Replace with: what the sweep proves is the **per-row declaration** — a row that does not declare is byte-identical across the readings, the declaring row's variance is proved positively by the same test. Keep the trailing `See [`codebase/1373.md`](../codebase/1373.md).` continuation intact. |
| **R** | The rename | 12 in-place substitutions | zero length change | 9 pure substitutions; the 3 inside sites A/B/C land as part of those rewrites. |

**One wording that fits site D's 3-line budget** — illustrative, not mandated:

```
// pathVaries declares that this row's own arm reads the runner path, so its
// VALUE and its DETAIL may both differ across the readings. It is what scopes
// TestTrailGateReadsTheRunnerPathOnlyWhereARowDeclaresIt's comparisons, and
```

### What must not move

AC5 is the binding constraint on content as well as on lines. Do not touch: any conditional, any comparison, any `want`/`reason` value, the six-value gate allowlist, or any count. The counts living inside or adjacent to the rewrite blocks — "nine rows", "all ten" return sites, "five" readings, "three" absence cases, "forty-five" comparisons, "9 rows × 5 readings" — all stay verbatim.

---

## Neutrality — the load-bearing constraint

**Every hunk must add exactly as many lines as it removes.** Per-block, not per-file.

Per-file `added == deleted` is insufficient and the ticket says so: +2 at the top and −2 at the bottom nets to zero while displacing every line between. This is not theoretical here — the sites are spread from `:228` to `:1487` in one file.

Blast radius, measured this run (distinct cite targets below each site in `trailer_admissibility_test.go`, named cites from Go sources only):

| Site | Distinct cite targets displaced by a one-line shift |
|---|---|
| B (`:228-232`) | **20** |
| C (`:358-362`) | **13** |
| D (`:772-774`) | **4** |
| A (`:1483-1487`) | **3** |

Two of these are concrete and named:

- `trailer_admissibility_test.go:364` is cited from `trail_ptyrunner_composition_test.go:55` — **two lines below site C's block**. A one-line growth at C makes that cite point at the wrong line.
- `trailer_admissibility_test.go:1552` (the declaration) is cited from `trail_ptyrunner_composition_test.go:43` — **below site D, inside site A's neighbourhood**. Growth at A, B, C or D moves it.

**The bare-cite hazard.** `trail_ptyrunner_composition_test.go:56` carries `(:1585-1590)` with no filename, inheriting `trailer_admissibility_test.go` from the line above. A `grep 'trailer_admissibility_test.go:1585'` returns **zero hits** — the bare form is invisible to a filename sweep. There are 9 such back-references across the three files. Neutrality is what protects them, because nothing else will find them.

### The deterministic gate

Run before committing. It reports a breach per non-neutral hunk:

```bash
git diff -U0 -- internal/e2e/realclaude/ docs/knowledge/features/e2e-realclaude.md | awk '
  /^@@/ { split($2,a,","); split($3,b,",");
          d=(a[2]==""?1:a[2]); i=(b[2]==""?1:b[2]);
          n++; if (d!=i) { bad++; print "BREACH " $0 } }
  END { printf "hunks=%d breaches=%d\n", n, bad+0 }'
```

**This gate was validated in both directions this run** — it is not asserted to work:

- Against `798d9af` (#1414, the known-neutral 15/15 comment diff in this same package): `hunks=3 breaches=0`.
- Against `faa3fbc` restricted to `trail_ptyrunner_composition_test.go` (known non-neutral): `hunks=4 breaches=2`.

A gate that only ever reports green proves nothing; this one discriminates.

**Stop condition** (inherited from #1414's spec, `:70`). If a block's obligation genuinely cannot be met inside its line budget, do **not** grow the block and do **not** open a cite-renumbering sweep. Note it on the ticket and stop. Do not "pay" for a grown block by shrinking an unrelated adjacent comment — that satisfies the file total and breaks the gate, correctly.

---

## Testing strategy

No new tests. The diff changes no assertion, so nothing new is provable by a new test; what must be demonstrated is that nothing moved.

- **Compile + behaviour.** `go test -count=1 -tags e2e_realclaude -run '^TestTrail' ./internal/e2e/realclaude/` is green. Baseline this run: `ok … 3.388s`, offline, no credentials, zero skips. Use `-v` and confirm the pass count is unchanged if any doubt arises — an exit code cannot tell a skip from a pass.
- **Cheaper compile-only loop while editing:** `go vet -tags e2e_realclaude ./internal/e2e/realclaude/`.
- **`make check` is not evidence here** and must not be cited as such: these files are `//go:build e2e_realclaude`, which `make check` excludes.
- **AC1:** `grep -rn TestTrailGateIgnoresTheRunnerPathExceptAtTheAbsenceArm --include='*.go' .` → empty.
- **AC2 residual:** the same grep repo-wide, all file types, returns exactly `docs/knowledge/codebase/1417.md`, `docs/knowledge/codebase/1420.md`, `docs/knowledge/features/e2e-realclaude.md` (only the `:1734-1736` `- Ticket [#…]` log lines), `docs/specs/architecture/1417-*.md`, `docs/specs/architecture/1420-*.md` — and nothing else. Verified as the complete set this run.
- **AC5 neutrality:** the awk gate above, `breaches=0`.
- **AC5 line totals:** `wc -l` unchanged on all four files (necessary, not sufficient — the gate is what actually holds).
- **The load-bearing back-ref:** after the edit, confirm `trailer_admissibility_test.go:1585-1590` is still clause B, and `:364` is still the "reading is echoed onto the result" line.
- **`gofmt -l` on the two Go files** — expect clean. Note the repo-wide baseline is dirty, so scope it to the changed files.

---

## Size check (recorded, not argued)

| Red line | This ticket |
|---|---|
| > 3 new files | **0 new files** |
| > ~600 total written lines | **~40 changed lines**, all in-place and length-neutral |
| > 5 new exported types | **0** |
| > 10 consumer call sites | see below |
| > 5 AC of work | 5 AC, all prose edits |
| ≥ 10 reject branches | **0** — no state machine, no new branch |
| ≥ 5 production source files (`*.go` non-test) | **0** — all three Go files are `_test.go` |

The one figure above a line is the 12 identifier occurrences. That is not edit fan-out: eleven are comment mentions, which create no dependency edge and no compile obligation, and a Go test function has no callers to cascade through — a missed occurrence fails a grep, not a build.

The evidence is the nearest analogue rather than an argument about how easy the edits look. **#1414** (`798d9af`) is the same shape — comment-only, line-neutral, three Go files, same package, same 80-column wrap, same cite-protection requirement — and landed at **15 insertions / 15 deletions in one commit**. #1430 is that shape with more sites and one markdown block. This is distinct from the #75 failure mode (26 `NewServer(...)` call sites with differing surrounding code, real compile dependencies, `replace_all` failing per-file): here the 12 occurrences are one byte-identical 54-character string, and the replacement is the same length.

**Verdict: XS, no split.**

---

## Open questions

1. **Site A's opening register.** Go doc comments lead with the identifier, so site A must open `// TestTrailGateReadsTheRunnerPathOnlyWhereARowDeclaresIt is …`. That name is 54 chars, leaving ~23 columns on line 1 for the rest of the clause. If the sentence cannot be made to land inside 5 lines, prefer moving material into lines 2–5 over growing the block; the stop condition applies if it still will not fit.
2. **Whether site C's paragraph wants the word "arm" at all.** It currently anchors on the absence arm to explain the exemption. Declaration-keyed phrasing removes the anchor, and the paragraph's neighbours (`:353-357`) already carry the fixed-prose argument. Developer's call, inside the 5-line budget.
3. **#1427 is wired blocked-by this ticket** (stated in the body as verified; not re-checked here). Nothing in this spec anticipates that arm beyond the name's forward-compatibility, which is AC1's stated property.
