# #1324 — Re-state the gather's and the record's prose about the trailer builder's old input

**Size:** S (confirmed; PO's label stands). **Behaviour change:** none. No signature, no type, no constant, no test row.
**Baseline measured on `eea9f91` by this spec run:** `go vet -tags e2e_realclaude ./internal/e2e/realclaude/...` exit 0; `go test -race -tags e2e_realclaude ./internal/e2e/realclaude/...` → `ok … 13.052s`. (The ticket cites 7.6 s; the number varies with machine load. What matters is that it is not a ~3 s exit, which would mean everything SKIPPED.)

---

## Files to read first

| Path + lines | What to extract |
|---|---|
| `internal/e2e/realclaude/finding_run_gather_test.go:270-345` | `finSighting`'s doc block. Holds sites A (`:273-282`), B (`:311`) and C (`:341-342`). Note the section headings — they are all still correct and none of them move. |
| `internal/e2e/realclaude/finding_run_gather_test.go:427-453` | The carrier fill. Site D is `:445-446` only; `:443-444` stays verbatim. **This whole block is cited by line from a forbidden file.** |
| `internal/e2e/realclaude/finding_run_gather_test.go:1369-1394` | `TestFinGatherSightingCarriesTheDecodedScalars`' doc. Site E is `:1384-1387`. Read `:1379-1382` — the file's own reason for symbol-only cites, which this ticket generalises. |
| `internal/e2e/realclaude/finding_run_gather_test.go:1455-1463` | Site F: the `CarriesTrailer` `t.Fatalf`. Four lines, must stay compiling and `gofmt`-clean. |
| `internal/e2e/realclaude/finding_run_record_test.go:42-58` | Site G: the "Two properties, and only one of them is structural" block. **`THE BUILDER` at `:54` means `finRecordBuild`, not `finTrailerBuild`.** |
| `internal/e2e/realclaude/finding_trailer_evidence_test.go:174-241` (read-only) | `finTrailerBuild`'s current doc + signature. `:183-190` is the "TRAP-FREE BY CONSTRUCTION … held BY THE SHAPE OF THE INPUT" passage site G must re-point at. `:216-223` delegates the ordering argument to site D. `:241` is the signature: `(outcome string, sighting finSighting)`. |
| `internal/e2e/realclaude/finding_trailer_evidence_test.go:256-266` (read-only) | The guard as shipped: `if !sighting.CarriesTrailer`. One bool. This is the fact sites B and F must be re-stated against. |
| `internal/e2e/realclaude/finding_trailer_evidence_test.go:292-328` (read-only) | `finTrailerSighting` — its doc's "THE AGREEMENT OBLIGATION LANDS HERE" paragraph and its body. This is the second computation site D must name. |
| `internal/e2e/realclaude/finding_trailer_evidence_test.go:583-603` (read-only) | Where "a pairing NO SCAN PRODUCES" actually lives (`:588-589`) and the test that owns it (`:603`). **The ticket cites `:572-575` for this; that is wrong — see § Corrections.** |

Nothing outside these two edited files is written. `docs/knowledge/features/e2e-realclaude.md` was brought current by #1320 and is not touched.

---

## Context

`finTrailerBuild` took a `trailObservation` until #1320 moved it onto the gather's scalar-only `finSighting` carrier. Two files that #1320 never opened still reason about the builder's old posture in order to justify their own. Nine prose claims — eight comments and one failure string — are now false, stale, or forward-looking about work that has landed.

The interesting constraint is not the prose. It is that **`finding_trailer_evidence_test.go` and `finding_artifact_write_test.go` cite these two files by exact line number, sixteen anchor endpoints in all, and this ticket may not edit either citing file.** Every anchor sits below the prose being rewritten, and one anchor *range* physically contains one of the rewrites. A net line-count change anywhere silently repoints a live cite in a file nobody is allowed to fix.

So this is a design with exactly one real invariant, and it is a layout invariant rather than a behavioural one.

---

## Design

### The invariant: every inbound anchor still resolves

**Primary gate (necessary and sufficient): the sixteen anchor endpoints below still resolve to the same construct after the work.** Verified exact on `eea9f91` by this spec run.

| File | Endpoints |
|---|---|
| `finding_run_gather_test.go` | 346, 442, 453, 1107, 1595, 1696 |
| `finding_run_record_test.go` | 92, 112, 137, 148, 151, 153, 173, 214, 221, 320, 325, 386, 390, 934, 944, 996, 1014, 1036, 1041 |

**Working discipline that makes the gate pass by construction: every hunk replaces exactly N lines with exactly N lines.** `wc -l` equality and `git diff --numstat` add==del are *necessary but not sufficient* — two compensating hunks (one `+1`, one `−1`) satisfy both while moving every anchor between them. Per-hunk neutrality is the checkable form; a recipe is in § Verification.

Two refinements, both load-bearing:

- **Site D is inside a cited range and is therefore hard.** `finding_trailer_evidence_test.go:220` and `:291` both cite `finding_run_gather_test.go:442-453` — `:291` calls it "a COPY of it". Site D rewrites `:445-446`, which is *inside* that range. If that comment goes from 4 lines to 3, the block becomes `442-452` and loses its closing brace, and both cites break even with `wc -l` unchanged. **`:443-446` must remain exactly four lines, of which `:443-444` are unchanged. Site D is exactly two lines in and two lines out.**
- **Site G may trade lines internally.** Both record-file hunks (`:50-52` and `:57`) sit above `:92`, the lowest anchor endpoint in that file. They may borrow a line from each other provided the file total is unchanged. This is the only relaxation; everywhere else, hold per-hunk.

### The cite rule for anything newly written

**Nothing written in this ticket carries a new line number.** Cite by symbol.

The file already argues this at `finding_run_gather_test.go:1380-1382` ("a number written here would be measured before the lines it points at existed"). Here the reason is stronger and empirical: the ticket's own line numbers into `finding_trailer_evidence_test.go` are stale (§ Corrections). A single rule — *no new line-number cites* — neutralises all of them at once and is also the cheapest way to stay line-count-neutral, since dropping a `:NNN` frees characters for the re-statement.

Existing cites are corrected **only when the sentence carrying them is itself being re-stated.** `finding_run_gather_test.go:279`'s `result_trailer_observation_test.go:80-84` sits inside site A's paragraph but its sentence is unchanged and true — leave it. Likewise `:287`, `:299`, `:333`, `:1297`, `:658`, `:931`: out of scope as a class.

### The nine, as truth conditions

Each site states what must be true afterwards and what must not survive. Wording is the developer's, subject to the line budget.

**Site A — `finding_run_gather_test.go:273-282`** (items 1 and 2, one paragraph, 10 lines in / 10 lines out)

- `:274-275`'s pointer at the builder drops `:203` and keeps the file name: `finTrailerBuild (finding_trailer_evidence_test.go)`. The sentence already names the symbol.
- `:281-282` states the move as **made**, attributed to **#1320**. "nothing consumes it yet" is false — the builder consumes it — and must not survive in any form.
- Everything from "recovering it with a SECOND trailWaitForTrailer" through "not a cost" is unchanged and true.

**Site B — `finding_run_gather_test.go:311`** (item 3, reflow within `:309-318`)

- The section heading `# CarriesTrailer records the outcome of a PAIR, not a State` is exactly right and stays.
- What must now be said: **the gather computes the pair `obs.State == trailSeen && obs.Trailer != nil` and records its outcome in this field; `finTrailerBuild` reads that one bool.** The `(:218)` cite goes; cite `finTrailerBuild` by symbol.
- `:312-318` is unchanged and remains true — "Once this value is forbidden the pointer, that pair is unrecomputable downstream" is now more visibly the *reason* the field exists.
- Must not survive: any claim that `finTrailerBuild` gates on a State/pointer pair.

**Site C — `finding_run_gather_test.go:341-342`** (item 4, reflow within `:339-345`)

- The move is **made**, attributed to **#1320**, and what held is that the projection was **rename-free** — the keys mirror `finTrailerRecord`'s and the move needed no rename. Say that, in past or present-perfect, not as a forthcoming event.
- `:342-345`'s omitempty argument is unchanged.

**Site D — `finding_run_gather_test.go:445-446`** (item 5 / AC3, exactly 2 lines in / 2 lines out)

The obligation did not disappear when the builder stopped computing the pair — **it moved to `finTrailerSighting`**, which recomputes `scan.State == trailSeen && scan.Trailer != nil` on the fixture side. `finTrailerSighting`'s own doc already states its half ("this is the second computation of the pair … held by this comment and by review, exactly as it is on the gather's side"). This site is the gather's side of that pair of comments; it must name the counterpart and the agreement requirement.

- Must not survive, and must not be written in any paraphrase: **"the gather is now the sole computer of the pair"**, or any claim that there is no second computation. It is false, and it would contradict the shipped comment in a file this ticket may not edit.
- Must not survive: `finTrailerBuild:218` as the thing the fill is identical to.
- `:443-444` (the State-operand-first ordering argument) is not part of this rewrite. `finTrailerBuild`'s doc delegates that argument to this site — collapsing it leaves a forbidden file pointing at nothing.

One fitting that satisfies the budget, offered as proof that two lines suffice and **not** as prescribed wording:

```go
// deliberately identical to finTrailerSighting's fill on the fixture side,
// and the two computations of the pair are required to agree.
```

**Site E — `finding_run_gather_test.go:1384-1387`** (item 6 / AC4, 4 lines in / 4 lines out)

- Still true and stays: no reachable sighting produces `trailSeen` with a nil `Trailer`, because `trailWaitForTrailer` fills the pointer on every seen result, so the inconsistent pair is not exercised by these rows.
- Falsified on both halves and must not survive: "which is all `finTrailerBuild` ever sees" (the builder sees no pair at all now — it sees one bool) and "hand-built fixture" (the shape that plays this role downstream is `CarriesTrailer` false beside four non-zero scalars, produced by flipping one bit off a scan-filled carrier, not typed in).
- If the downstream shape is named, name it **by symbol** — `TestFinTrailerRecordFillsTheFourScalarsOnlyBehindCarriesTrailer` — never by line. See § Corrections.

**Site F — `finding_run_gather_test.go:1459-1462`** (item 7 / AC2, 4 lines in / 4 lines out, `t.Fatalf`)

- Re-state the first clause against the one bool the builder reads.
- **What the field is FOR is unchanged and the message keeps saying it**: a reader with only a `State` cannot tell "there was no trailer" from "the trailer's fields were empty". Do not drop that clause; it is the field's whole justification.
- Constraint the other sites do not have: this is a Go string concatenation. It must compile and survive `gofmt` at four lines. Keep the escaped quotes.

**Site G — `finding_run_record_test.go:50-52` and `:57`** (items 8 and 9 / AC5)

Read `:44-58` as a whole before editing. The block's framing is a *parallel* — "the record is trap-free by construction, the builder is not, exactly `finTrailerBuild`'s posture" — and #1320 inverted the second half of it.

What is now true:

- `:44-49` is unchanged. The record is trap-free by construction because `finRecordInputs` carries neither a `trailObservation` nor a `trailScanResult`.
- `finTrailerBuild` now holds that same structural property, by the same route: since #1320 its input is a `finSighting`, which carries neither `trailScanResult.Line` nor the `*resultTrailer`. Its doc says so ("TRAP-FREE BY CONSTRUCTION … held BY THE SHAPE OF THE INPUT"). **Re-point `:52`'s cite at that passage by symbol — `finTrailerBuild`'s doc — not by line.**
- `finRecordBuild` does **not** hold it: `in.Rows[i].Command` and `in.ClaudeCommand` are verbatim argv in reach, and its no-captured-bytes property is still held by the Detail content rule plus `TestFinRecordCarriesNoCapturedBytes`. That part of `:54-56` is unchanged and stays.
- Therefore `:57`'s relation to `finTrailerBuild` is a **contrast**, not a parallel. `:58`'s reason for saying it in both places ("so that neither claim is read as covering the other") holds and stays.

Must not survive: any present-tense claim that `finTrailerBuild` takes the observation or that `.Line` is in its reach; the `finding_trailer_evidence_test.go:168-171` cite (it lands on a struct's closing brace, thirteen lines above the passage that says the opposite).

**Trap to avoid:** in this file "THE BUILDER" (`:54`) means `finRecordBuild`. A re-statement that says "the builder is trap-free too" under that heading reads as a claim about `finRecordBuild` and is false — and it is false in the direction that matters, since `finRecordBuild` is where operator argv is in reach. Name `finTrailerBuild` in full every time.

### Corrections to the ticket body

Two of the ticket's own line numbers are wrong. Neither changes what the work is; both would propagate a fresh stale cite if copied.

1. **`finding_trailer_evidence_test.go:572-575` is not "a pairing NO SCAN PRODUCES".** Measured on `eea9f91`, `:572-575` is a `t.Errorf` about `terminal_reason` sitting past the cap. The phrase is at `:588-589`, and the test that owns the synthetic row is `TestFinTrailerRecordFillsTheFourScalarsOnlyBehindCarriesTrailer` at `:603`. Site E must cite by symbol, so the number is not needed at all.
2. **The ticket's other `finding_trailer_evidence_test.go` numbers run +2.** It gives `finTrailerBuild` at `:239` (actual `:241`), `finTrailerSighting` at `:316` (actual `:318`), and its doc at `:294-301` (actual `:296-303`). The measurements were taken against a tree two lines shorter than `eea9f91`.

The ticket's numbers into the two **edited** files were all verified exact — every one of the nine sites and every anchor endpoint. The drift is confined to the forbidden file, which is exactly the file the no-new-line-numbers rule keeps out of the diff.

### Concurrency model

None. No goroutine, no channel, no shared state, no lifecycle. The one process-level fact the prose touches — that `go test -race` runs tests in this package in parallel, which is why `finTrailerSighting` is a function rather than a package-level var — is stated in a file this ticket may not edit and is unchanged.

### Error handling

None. No new failure mode, no new branch, no reject arm. Site F edits the *text* of an existing `t.Fatalf`; the condition it fires on (`sighting.CarriesTrailer != tc.wantCarries`) is untouched, as is its choice of `Fatalf` over `Errorf`.

---

## Testing strategy

No new test, no changed assertion, no changed row. The whole verification is that the diff is prose-only, layout-neutral, and still compiles.

### 1. Anchor gate (the primary check)

Run before and after; must print `all anchors resolve` both times.

```bash
check_anchors() {
  fail=0
  while IFS='|' read -r spec want; do
    f="${spec%%:*}"; n="${spec##*:}"
    got=$(sed -n "${n}p" "$f")
    case "$got" in *"$want"*) ;; *) echo "BROKEN ANCHOR $spec: want ~'$want' got '$got'"; fail=1;; esac
  done <<'EOF'
internal/e2e/realclaude/finding_run_gather_test.go:346|type finSighting struct
internal/e2e/realclaude/finding_run_gather_test.go:442|sighting = finSighting{State: obs.State
internal/e2e/realclaude/finding_run_gather_test.go:453|}
internal/e2e/realclaude/finding_run_gather_test.go:1107|func TestFinGatherReturnsNoCapturedBytes
internal/e2e/realclaude/finding_run_gather_test.go:1595|func TestFinGatherSightingScalarsComeFromTheFullLineDecode
internal/e2e/realclaude/finding_run_gather_test.go:1696|func TestFinSightingReachesNoScanType
internal/e2e/realclaude/finding_run_record_test.go:92|finRecordProc is one matched row's identity
internal/e2e/realclaude/finding_run_record_test.go:112|trailRunReadings.MatchCount
internal/e2e/realclaude/finding_run_record_test.go:137|# What is carried whole, and why that is safe
internal/e2e/realclaude/finding_run_record_test.go:148|do not add a row-index
internal/e2e/realclaude/finding_run_record_test.go:151|not a reason to strip the field here
internal/e2e/realclaude/finding_run_record_test.go:153|# The Detail's content rule
internal/e2e/realclaude/finding_run_record_test.go:173|SCALARS and never an input struct
internal/e2e/realclaude/finding_run_record_test.go:214|THERE IS NO trailObservation FIELD
internal/e2e/realclaude/finding_run_record_test.go:221|cannot add one silently
internal/e2e/realclaude/finding_run_record_test.go:320|Pure over its inputs
internal/e2e/realclaude/finding_run_record_test.go:325|not a reason to abort the turn
internal/e2e/realclaude/finding_run_record_test.go:386|rec.Detail = trailDetail
internal/e2e/realclaude/finding_run_record_test.go:390|rec.RunnerAgreement
internal/e2e/realclaude/finding_run_record_test.go:934|slice-valued fields
internal/e2e/realclaude/finding_run_record_test.go:944|exitCode    int
internal/e2e/realclaude/finding_run_record_test.go:996|name string
internal/e2e/realclaude/finding_run_record_test.go:1014|Rows:          rows,
internal/e2e/realclaude/finding_run_record_test.go:1036|long-form argument belongs in a comment
internal/e2e/realclaude/finding_run_record_test.go:1041|A %v verb applied to in.Rows
EOF
  [ "$fail" = 0 ] && echo "all anchors resolve"
}
check_anchors
```

Verified by this spec run: prints `all anchors resolve` on `eea9f91`.

### 2. Per-hunk neutrality

```bash
git diff -U0 -- internal/e2e/realclaude/finding_run_gather_test.go \
                internal/e2e/realclaude/finding_run_record_test.go \
| grep -E '^@@' | while read -r _ old new _; do
    ob=${old#*,}; [ "$ob" = "$old" ] && ob=1
    nb=${new#*,}; [ "$nb" = "$new" ] && nb=1
    [ "$ob" = "$nb" ] || echo "NON-NEUTRAL HUNK: $old $new"
  done
```

Verified by this spec run against synthetic input: silent on `@@ -274,10 +274,10 @@` and on the omitted-count form `@@ -311 +311 @@`; fires on `@@ -445,2 +445,3 @@` and on `@@ -57 +57,2 @@`. Expect silence, with the one permitted exception in § Design (the two record-file hunks may trade a line if the anchor gate and `wc -l` both hold).

### 3. Line counts and diff shape

```bash
wc -l internal/e2e/realclaude/finding_run_gather_test.go   # must be 1835
wc -l internal/e2e/realclaude/finding_run_record_test.go   # must be 1057
git diff --numstat -- internal/e2e/realclaude/            # additions == deletions, per file
gofmt -l internal/e2e/realclaude/finding_run_gather_test.go internal/e2e/realclaude/finding_run_record_test.go   # empty
```

### 4. Content sweeps, each with a control

A sweep that reports absence unconditionally proves nothing, so each negative is paired with a positive that must hold.

| Sweep | Before | After |
|---|---|---|
| `grep -c '#1308' <both files>` | gather 3, record 0 | **0 and 0** |
| `grep -c '#1320' <both files>` | 0 and 0 | gather **≥ 2** (control: the replacement names the right ticket) |
| `grep -n 'nothing consumes it yet' <both>` | 1 hit | empty |
| `grep -n 'sole computer' <both>` | empty | **empty** (AC3's forbidden replacement) |
| `grep -n '572-575' <both>` | empty | **empty** (the ticket's wrong cite) |
| `grep -nE 'finding_trailer_evidence_test\.go:[0-9]+' <both>` | 8 hits: gather `:275 :287 :299 :333 :1297`, record `:52 :658 :931` | **exactly 6** — `:275` (site A) and record `:52` (site G) gone; the surviving set must equal the ticket's leave-alone list `:287 :299 :333 :1297 :658 :931` |
| `grep -n '(:218)' <gather>` | 1 hit at `:311` | empty (site B's stale bare-colon cite) |
| `grep -n 'finTrailerBuild:218' <gather>` | 1 hit at `:445` | empty (site D's stale cite) |
| `grep -c 'finTrailerSighting' <gather>` | 0 | **≥ 1** (site D names the counterpart) |

### 5. Build and test

`make check` / `make build` never compile `e2e_realclaude`-tagged files, so a green from either is vacuous for this diff.

```bash
go vet -tags e2e_realclaude ./internal/e2e/realclaude/...
go test -race -tags e2e_realclaude ./internal/e2e/realclaude/...
```

Expect vet exit 0 and the package `ok`, with a PASS/SKIP split rather than all-PASS. Baseline on `eea9f91` measured by this spec run: `ok … 13.052s`. **A run finishing in ~3 s means the live specs all SKIPPED and is not a pass.** Everything here is offline: no live claude, no credentials, no daemon, no turn, no `t.Skip`.

---

## Open questions

1. **Do sites A and C both need to name #1320, or does one attribution suffice?** AC1 says both are re-stated "against the ticket that actually made it", so name it at both. If the line budget at site C proves tight, the projection-was-rename-free fact is the load-bearing half and the ticket number is the droppable half — but try the budget first; `#1308`→`#1320` is character-neutral.
2. **Should site G's re-statement keep the "#1290 could not buy" history?** Either form is admissible provided no present-tense claim survives that `finTrailerBuild` takes the observation. A historical form ("#1290 could not buy it; #1320 did") is accurate and preserves the block's provenance; a purely present-tense form is shorter. Developer's call under the line budget.
3. **`finding_run_record_test.go:780`'s "the observation is not reachable from the record's inputs" walk does not forbid `finSighting`.** It stays non-vacuous because `finRecordInputs.Trailer` is a `finTrailerRecord`, which reaches neither forbidden type. Whether the walk should be widened is a real question and explicitly **not this ticket's** — file it separately if it matters.
4. **The fourteen stale cites into `finding_trailer_evidence_test.go` left by #1320's ~250-line growth** are a separate mechanical concern across the package. Out of scope here as a class; only the three inside re-stated sentences are corrected.

---

## Security review

**Verdict:** PASS

The category walk is not vacuous for a prose-only ticket. The comments being rewritten *are* the documentation of this package's argv-prohibition — the rule that keeps an operator's `CLAUDE_CODE_OAUTH_TOKEN` or `ANTHROPIC_API_KEY` out of an artifact destined for a public issue. That rule is held, by the family's own admission, "by comment and by review". A restatement that garbles it weakens a real control.

**Findings:**

- **[Trust boundaries] MUST-NOT-WEAKEN, addressed in § Design.** The boundary is `finSighting`: verbatim model output (`trailScanResult.Line`) and the raw-bytes pointer (`*resultTrailer`, carrying `PermissionDenials *[]json.RawMessage`) stop on the gather's side of it; only scalars cross. Three of the nine sites document that boundary (`:281-282`, `:311`, and record `:50-52`). The spec pins each with an explicit "must not survive" list rather than leaving the wording open, and the *reason* clauses ("carries neither `.Line` nor the decoded pointer"; "that pair is unrecomputable downstream") are marked unchanged at every site. No re-statement in this design removes a statement of what the boundary excludes.
- **[Error messages, logs, telemetry] Finding, addressed.** Site F edits a `t.Fatalf` — a failure string, i.e. output. Its second clause is the field's justification (`State` alone cannot distinguish "no trailer" from "empty trailer fields"). AC2 and § Design both require it kept; the spec forbids dropping it. The string interpolates `sighting.CarriesTrailer` and `tc.wantCarries`, both bools — no captured bytes enter it and the edit does not add a verb.
- **[Error messages, logs, telemetry] Second finding, addressed.** Record `:54-56` states that `finRecordBuild` holds argv in reach and that its no-captured-bytes property rests on the Detail content rule plus `TestFinRecordCarriesNoCapturedBytes`. Site G rewrites the sentence *adjacent* to it (`:57`). The realistic failure is a developer inverting the block's framing and writing "the builder is trap-free too" under a heading where "THE BUILDER" means `finRecordBuild` — which would document the argv channel as structurally closed when it is not. § Design names this trap explicitly and requires `finTrailerBuild` be spelled in full at every mention. Code-review should treat any weakening of `:54-56` as a defect regardless of what the ACs asked for.
- **[Trust boundaries, second-order] Finding, addressed.** Site D's forbidden replacement ("the gather is now the sole computer of the pair") is not merely false — it would retire, in prose, the only control on an agreement obligation that has no deterministic pin (`finGatherReadings` reaches `ps` through `pinScanArgv`/`pinReadState`, and the evidence file forbids exec). Writing it would leave `finTrailerSighting`'s shipped half of the comment pair pointing at a counterpart that denies the pair exists. The spec forbids the sentence and every paraphrase of it, and requires the counterpart be named.
- **[Subprocess / external command execution] Not applicable by design.** No `exec.Command`, and no shipped helper that execs internally (`pinScanArgv`, `probeProcessSnapshot`, `tdnScan`, `holdProbeFIFO`, `WithWorktreeAuthenticated`) is reached — the diff adds no executable statement at all. Verified by the shape of the work: every hunk is inside a `//` comment or inside an existing format string. § Testing's `gofmt` + `git diff --numstat` + per-hunk checks are what make that claim checkable rather than asserted.
- **[Tokens, secrets, credentials] Not applicable — no token is created, stored, compared or logged.** The tokens named in this package's prose (`CLAUDE_CODE_OAUTH_TOKEN`, `ANTHROPIC_API_KEY`) appear only as the thing the argv prohibition exists to protect; the sentences naming them (`finding_run_gather_test.go:262`, `:405`; `finding_run_record_test.go:98`) are none of the nine sites and are untouched.
- **[File operations] Not applicable.** No path is constructed, opened, or written. Everything is offline: no credentials, no daemon, no live turn.
- **[Cryptographic primitives] Not applicable.** No RNG, no hash, no comparison against a secret.
- **[Network & I/O] Not applicable.** No socket, no reader, no cap. `reachCapCommand`'s 512-byte cap is referenced by the surrounding prose but is neither changed nor re-stated — none of the nine sites touch it, and #1284's headroom rule is discharged structurally here (the carrier has no Detail field), which the untouched `:294-299` continues to state.
- **[Concurrency] Not applicable.** No lock, no goroutine, no shared state; the tests' `-race` posture is unchanged and re-verified in § Testing.
- **[Threat model alignment] In scope and aligned.** The relevant threat is the one this package exists to hold shut: verbatim argv or verbatim model output reaching a published artifact. This ticket neither opens nor narrows the channel; its risk is documentation drift that makes a later author believe a closed channel is open, or an open one closed. The mitigation is the per-site "must not survive" lists plus the `THE BUILDER` disambiguation, both above.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-08-05
