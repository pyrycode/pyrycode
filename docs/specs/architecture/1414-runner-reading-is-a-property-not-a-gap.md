# 1414 — The gathers' constant runner reading is a property, not unclaimed work

**Size:** XS (confirmed; PO sized XS). Three comment blocks, ~15 lines of prose, three files, zero new symbols, zero new tests.

**Gate:** `go vet -tags e2e_realclaude ./internal/e2e/realclaude/...` — verified green on `main` at `ce0d768` during this run (exit 0). `make check` is **not** a gate here: these files carry `//go:build e2e_realclaude` and `make check` never compiles them.

---

## Files to read first

| Path | What to extract |
|---|---|
| `internal/e2e/realclaude/trail_ptyrunner_composition_test.go:19-26` | **The canonical paragraph.** Verified verbatim this run. It states both gathers fill the field with `trailRunnerUnread()` by construction, cites all three fill sites, and gives the reason. All three new comments cite this instead of restating it. Note its prohibition clause says "that gather's scan" — **singular**, scoped to the finding gather. |
| `internal/e2e/realclaude/trailer_admissibility_test.go:791-814` | Edit site 1 is `:805-810`. Read the whole doc comment: `:796-799` already states `tdnClaudeCommand`'s `""` rule, so site 1 must lean on it rather than restate it. `:812-813` and the `func` at `:814` must not move. |
| `internal/e2e/realclaude/finding_run_gather_test.go:537-552` | Edit site 2 is `:546-550`. The fill call is at `:551-552`; `:552` is cited from three other files. |
| `internal/e2e/realclaude/trail_run_rig_test.go:150-200` | Edit site 3 is `:157-160`. The fill call is at `:161-162`. `:196-197` states "no pyry runs here" / "no claude runs here". |
| `internal/e2e/realclaude/trail_run_rig_test.go:26-47` | **The rig's own reason.** `:30` "This rig stages no reap line and MUST NOT GROW ONE". `:40` "This rig runs no pyry and no claude, so PyryExited and ClaudeState have no producer to be gathered from." Both above the edit point, so both are stable cite targets. |
| `internal/e2e/realclaude/finding_exit_path_probe_test.go:264-272` | The finding gather's prohibition: FIFO path alone, no `tdnClaudeNeedle`, because the gather has no `finLivePinReduce`. Governs **site 2 only** — see § "The rig does not share the gathers' reason". |
| `internal/e2e/realclaude/teardown_liveness_probe_test.go:557-575` | `tdnClaudeCommand` — returns `""` unless exactly one row carries the claude needle. The func body is `:561-575`, which is the range the canonical paragraph cites. |
| `internal/e2e/realclaude/trailer_admissibility_test.go:139-143` | **Out of scope, already correct.** Carries the same claim without "UNOWNED". Do not edit. Its cites to `finding_run_gather_test.go:552, :789` and `trail_run_rig_test.go:162` are among those the neutrality constraint protects. |
| `internal/e2e/realclaude/finding_stage_held_group_test.go:70`, `:576` | Both cite `trail_run_rig_test.go:159-171`. `:576` is **inside a `t.Fatalf` format string** — the constraint that forces the design (§ "Why neutrality is forced, not preferred"). |

---

## Context

Three shipped comments describe the gathers' runner-path reading as `UNOWNED — no ticket holds it`. The statement is *true* — no ticket does hold it — but it misframes a property the design maintains deliberately and whose closure is forbidden with a stated reason one file away. A reader meeting "UNOWNED" reasonably concludes there is a gap worth filing.

The ticket's premise was re-verified on `main` at `ce0d768` during this run:

- `grep -rn "1374" internal/` → **empty**. AC3 is already satisfied; it is a regression guard.
- `grep -rn "UNOWNED" internal/e2e/` → **exactly the three sites**, no others.
- `trailGateCase.in`'s doc at `trailer_admissibility_test.go:762-766` is already correct. Out of scope.

The three sites, with their exact block boundaries:

| # | Site | Block | Lines | Prose budget |
|---|---|---|---|---|
| 1 | `trailer_admissibility_test.go` | `:805-810` | **6** | ~77 cols/line (top-level `// `) |
| 2 | `finding_run_gather_test.go` | `:546-550` | **5** | ~69 cols/line (one tab + `// `) |
| 3 | `trail_run_rig_test.go` | `:157-160` | **4** | ~69 cols/line (one tab + `// `) |

---

## Design

### The load-bearing constraint: line-count neutrality

Each block must be replaced by **exactly the same number of comment lines**. Not "about the same" — exactly.

This is not stylistic. These three files are cited by line from roughly ten others. A one-line growth at any site displaces every cite below it. Measured this run:

- **Site 1** displaces 4 cross-file cites (`:1039`, `:1552`, `:1715`, `:1949`) plus intra-file bare cites.
- **Site 2** displaces ~13 cite occurrences across 8 files — including `:552` and `:789`, cited from `trail_ptyrunner_composition_test.go:20`, `trailer_admissibility_test.go:140` and `:1365`.
- **Site 3** displaces ~10, including `trail_run_rig_test.go:162` cited from three files.

A non-neutral diff therefore converts an XS comment-polish ticket into a ~25-cite renumbering sweep across ten files. That is the shape that has failed repeatedly in this package. Neutrality discharges **AC4 by construction**: if no line moves, no cite can go stale, and no sweep is needed.

### Why neutrality is forced, not preferred

The two remaining ACs make the non-neutral path *unbuildable*, not merely expensive:

`finding_stage_held_group_test.go:576` cites `trail_run_rig_test.go:159-171` from **inside a `t.Fatalf` format string**. If site 3's block changes line count, that range shifts and the cite goes stale. Then:

- Fixing it means editing a string literal → violates **AC5** ("the diff changes only comments").
- Not fixing it → violates **AC4** ("every cross-file line cite this diff touches resolves").

The two ACs deadlock on any non-neutral edit at site 3. Neutrality is the only design that satisfies both. Treat it as a hard constraint, not a target.

**Stop condition.** If a site's obligations genuinely cannot be met within its line budget, do **not** grow the block and do **not** open a cite sweep. Note it on the ticket and stop. Per-block neutrality, not per-file: do not "pay" for a grown block by shrinking an unrelated adjacent comment.

### The rig does not share the gathers' reason

This is the finding that shapes site 3, and getting it wrong would ship a false citation.

The canonical paragraph's prohibition clause reads: `finding_exit_path_probe_test.go:264-272` "forbids adding that needle to **that gather's** scan — it has no `finLivePinReduce`". Singular. That site is the *finding probe's* construction of `finGatherInputs`, feeding `finGatherReadings`. It does not govern `trailRigGather`, which takes its needles as a parameter from two different callers.

**Do not cite `finding_exit_path_probe_test.go:264-272` at site 3.** It would attribute the rig's constancy to a rule that does not apply to it.

The rig has its own reason, and it is stronger than a prohibition — it is structural impossibility:

- `trail_run_rig_test.go:40` — "This rig runs no pyry and **no claude**, so PyryExited and ClaudeState have no producer to be gathered from." There is no claude process here for any argv scan to find.
- Both callers pass the FIFO path alone: `TestTrailRigFlipsAcrossOneSubjectLifetime:214` and `TestTrailRigCarriesMoreThanOneMatchedRow:513`. Neither carries `tdnClaudeNeedle`.
- `:26-36` states the same discipline for the reap line ("MUST NOT GROW ONE — synthesising one would fake the finding the live probes exist to earn"). Site 3 may invoke this as the **same-shaped** rule; it must not claim it governs the runner path directly.

Cite the stable `:40` (above the edit point, so unaffected). Do not cite `:214` or `:513` by line — they sit below the edit and, while neutrality protects them, naming them buys nothing.

### Per-site content obligations

Each site owes four things: **(a)** the reading is constant *by construction*, **(b)** one clause naming the immediate mechanism, **(c)** enough for a reader to tell closing it is not open work, **(d)** a citation to `trail_ptyrunner_composition_test.go:19-26` for the full reason. House style forbids restating the mechanism three times — state the one clause, then cite.

**Site 1** — `trailer_admissibility_test.go:805-810`, doc of `trailRunnerUnread`, 6 lines.
- Subject is the *gathers' use* of the helper, not the helper itself.
- Mechanism clause: neither gather's needle set carries `tdnClaudeNeedle`. The preceding paragraph at `:796-799` already explains `tdnClaudeCommand`'s `""` semantics — lean on it, do not restate it.
- Not-open-work: for the finding gather, adding the needle is forbidden by `finding_exit_path_probe_test.go:264-272`.
- Keep these surviving true clauses from the current text: #1420 reads the path at the gate without needing a live one; over a live run the absence arm reaches only its path-unnamed case; the fixture rows keep this helper regardless.

**Site 2** — `finding_run_gather_test.go:546-550`, inside `finGatherReadings`, 5 lines.
- Mechanism clause: this gather's needle list is the FIFO path alone.
- Not-open-work: `finding_exit_path_probe_test.go:264-272` forbids adding `tdnClaudeNeedle` — no `finLivePinReduce` here, so claude's row would land in the classifier's match-count arms and the published liveness list.
- Keep: the gate's absence arm reaches only its path-unnamed case here.

**Site 3** — `trail_run_rig_test.go:157-160`, inside `trailRigGather`, 4 lines.
- Mechanism clause: no caller's needle list carries `tdnClaudeNeedle`.
- Not-open-work: this rig runs no claude at all (`:40`) — there is no producer to read a runner from, and growing one would change what the rig is.
- Must not cite `finding_exit_path_probe_test.go`.

*Non-normative fit proof for the tightest site — evidence 4 lines suffices, not text to paste:*

```
	// The runner path is CONSTANT by construction, not an open gap: this rig
	// runs no claude at all (:40) and no caller's needles carry
	// tdnClaudeNeedle, so none can be read. Full mechanism and the other two
	// fill sites: trail_ptyrunner_composition_test.go:19-26.
```

### Bare-cite ordering

A bare `:NNN` inherits the last filename named in the surrounding comment. Each new comment cites both same-file lines (bare) and one cross-file file. **Every bare `:NNN` must appear before the first cross-file filename in that comment**, or be written fully qualified. A bare cite written after `trail_ptyrunner_composition_test.go` would silently retarget to that file.

---

## Error handling

No runtime surface. The failure modes are all build-time:

| Failure | Detected by |
|---|---|
| A block changed line count | `git diff --numstat` — additions ≠ deletions for that file |
| A cited anchor moved | Anchor greps below return the wrong line numbers |
| A non-comment line changed | The comments-only diff filter below is non-empty |
| The package stopped compiling | `go vet -tags e2e_realclaude` |

---

## Testing strategy

No new tests. This is a comment-only diff; the closed sets do not grow, so no coverage row is owed.

Run all five checks against the tree being committed. Every one is deterministic and hermetic — no credentials, no network, no tokens.

**1. Neutrality (AC4, primary).** Per file, additions must equal deletions:
```bash
git diff --numstat -- internal/e2e/realclaude/
```
Expect exactly `6 6`, `5 5`, `4 4` against the three paths.

**2. Anchors held (AC4, different fabric).** Neutrality checks the diff's shape; this checks the outcome that actually matters — that the cited lines still hold what the citing prose names:
```bash
grep -n "RunnerPath: trailRunnerUnread()})" internal/e2e/realclaude/finding_run_gather_test.go   # 552, 789
grep -n "RunnerPath: trailRunnerUnread()})" internal/e2e/realclaude/trail_run_rig_test.go        # 162
grep -n "^func trailRunnerUnread" internal/e2e/realclaude/trailer_admissibility_test.go          # 814
```

**3. Phrase sweep (AC1).** Note the wrap: at site 3 the phrase breaks across `:159-160` as `no ticket holds` / `it —`, so the AC's literal string `"no ticket holds it"` matches only two of the three sites. Grep the unwrapped stem:
```bash
grep -rn "UNOWNED" internal/          # must be empty
grep -rni "no ticket holds" internal/ # must be empty
```
Both recipes were run against the unedited tree this run and returned 3 and 2 hits respectively — that is the control proving they can find what they are looking for. A recipe that returns nothing on the *unedited* tree proves nothing on the edited one.

**4. Regression guard (AC3).** `grep -rn "1374" internal/` — must be empty. Already true at `ce0d768`.

**5. Compile + comments-only (AC5).**
```bash
go vet -tags e2e_realclaude ./internal/e2e/realclaude/...
git diff -U0 -- internal/e2e/realclaude/ | grep -E '^[+-]' | grep -vE '^(\+\+\+|---)' | grep -vE '^[+-][[:space:]]*//'
```
The second must print nothing: every changed line is a comment line.

---

## Open questions

None blocking. Two decisions recorded so review does not re-litigate them:

1. **`finding_stage_held_group_test.go:70` and `:576` cite `trail_run_rig_test.go:159-171`, a range whose first two lines this diff rewrites.** The claim those cites name (`tdnClassifyReapLog` over nil reaching `tdnReapNoLine` → `trailAdmitVoidNoLine`) lives at `:164-176`, inside the range and untouched. The range start at `:159` was already loose before this diff. Under neutrality the cite still resolves to the claim it names, so it stays correct and is **not** this ticket's to tighten — `:576` sits inside a `t.Fatalf` string that AC5 forbids editing.

2. **The canonical paragraph at `trail_ptyrunner_composition_test.go:19-26` is slightly loose about the rig** — it lists `trail_run_rig_test.go:162` among the fill sites covered by a prohibition it scopes to "that gather's scan". Correcting it is #1415's text and out of scope here. Site 3 works around it by carrying the rig's own reason rather than deferring the whole explanation to the citation.
