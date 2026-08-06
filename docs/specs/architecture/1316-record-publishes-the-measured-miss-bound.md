# #1316 — Probe instrument: prove the published record's lateness bound is the classified sighting's

**Size:** S (confirmed, not overridden). One test file gains one row; a second file gains two line-number bumps. No production code, no new symbol, no signature change.

**Blockers:** #1320, #1312, #1315, and siblings #1321/#1324/#1325 all merged. Branch-overlap check at `ac25ad8`: no in-flight `origin/feature/*` branch touches either file.

---

## Files to read first

| Path | What to extract |
|---|---|
| `internal/e2e/realclaude/finding_run_gather_test.go:1242-1367` | `TestFinGatherSightingReportsTheMissBound` — **the idiom to mirror in full**: inline `finGatherInputs`, goroutine, 500 ms sleep, one `Write`, `<-done`, second `trailWaitForTrailer(...).BoundFrom` read off the call. Its doc's four headings are the shape the new doc should take. |
| `internal/e2e/realclaude/finding_run_gather_test.go:137-151` | `finGatherTrailerWait`'s wall-clock accounting. The `:144-147` bullet is AC3's first correction target. |
| `internal/e2e/realclaude/finding_run_gather_test.go:105-122` | The file header's failure-message licence — what a `t.Fatalf` here MAY and MAY NEVER name. Read before writing any message. |
| `internal/e2e/realclaude/finding_run_gather_test.go:270-345` | `finSighting` and its `# What it deliberately does not carry`. The `NO Bounded` bullet at `:286-290` carries AC3's second correction target (`:287`). |
| `internal/e2e/realclaude/finding_run_gather_test.go:422-505` | `finGatherReadings` — the three returns, and the carrier fill at `:442-453` the new row's first record comes out of. |
| `internal/e2e/realclaude/finding_run_gather_test.go:577-580` | `finGatherNeedles(t)` — calls `t.TempDir()`, hence "inputs built on the TEST goroutine". |
| `internal/e2e/realclaude/finding_run_gather_test.go:870-881` | `finGatherNegativeInputs` — the base fixture the new row copies field-for-field **except** the seed. |
| `internal/e2e/realclaude/finding_trailer_evidence_test.go:241-272` | `finTrailerBuild` — the builder under test. `Bounded` is derived at `:247-253`; that range is AC3's third correction target. |
| `internal/e2e/realclaude/finding_trailer_evidence_test.go:292-328` | `finTrailerSighting(scan, staleness, boundFrom)` — the shipped derivation the **second** record's carrier must come through. Its doc states why it takes a scan and not a `trailObservation`. |
| `internal/e2e/realclaude/finding_trailer_evidence_test.go:330-449` | `TestFinTrailerRecordCarriesTheBoundAndItsDiscriminator` — the tier this row joins to. Note it asserts only `rec.Detail != ""`, so a Detail-content check here is new, not a restatement. |
| `internal/e2e/realclaude/finding_trailer_evidence_test.go:100-106` and `:184-190` | The two `(finding_run_gather_test.go:1696)` cites that this row's insertion point shifts. See § Cite maintenance. |
| `internal/e2e/realclaude/result_trailer_observation_test.go:73-90` | The three `trailBoundFrom*` constants and their doc — `trailBoundFromStart` "BOUNDS NOTHING". |
| `internal/e2e/realclaude/result_trailer_observation_test.go:242-274` | `trailWaitForTrailer` — the `lastMiss.IsZero()` branch at `:256-262` is the whole mechanism. Stamping order at `:216-228`. |
| `internal/e2e/realclaude/background_trigger_probe_test.go:131` and `:722-742` | `probePollInterval = 200ms`; `probeSyncBuffer` mutex-guarded, append-only, `Bytes()` returns a copy. |

Everything the row needs is already imported by `finding_run_gather_test.go` (`fmt`, `strings`, `testing`, `time`). **No import change.**

---

## Context

`finTrailerBuild` derives `Bounded` from `BoundFrom == trailBoundFromMiss` and nothing else. Since #1320 that builder takes the gather's `finSighting`, so one poll drives both the classified outcome and the published record — but that is a *structural* argument from the input type, and it is the only thing standing behind `lateness_bounded` today.

Two tiers exist and no row joins them:

- The **record tier** (`TestFinTrailerRecordCarriesTheBoundAndItsDiscriminator`, and all nine `finTrailerBuild` call sites in the package) reaches the builder through `finTrailerSighting(scan, staleness, boundFrom)` with the discriminator **handed in** as a literal or a table field. Nothing at that tier ever met a poll.
- The **carrier tier** (`TestFinGatherSightingReportsTheMissBound`, #1312) measures the discriminator against a poll that really missed — and stops at the carrier, deliberately; its own doc says it "adds a second source of none of the three".

So the boolean is pinned where it is not measured and measured where it is not published. This row publishes a record whose miss bound was measured end to end, beside a record built over the same bytes from a second observation that reports the start bound — separating them by measurement rather than by argument.

---

## Design

### Placement

One new test function in `internal/e2e/realclaude/finding_run_gather_test.go`, **immediately after `TestFinGatherSightingReportsTheMissBound`'s closing brace** (currently `:1367`) and before `TestFinGatherSightingCarriesTheDecodedScalars`. This placement is what makes the `:1297` cite correction load-bearing: that cite sits in the doc of the row directly above, and a new doc citing the derivation correctly beside an old one citing it wrongly reads as a contradiction.

Suggested name: `TestFinGatherRecordPublishesTheMeasuredMissBound`. It must keep the `TestFinGather` prefix so the header's `-run '^TestFinGather'` invocation (`:13`) still selects it.

This is the **first `finTrailerBuild` call from this file**. The package is one; nothing needs moving.

### The composition

Mirror #1312's structure exactly — inline `finGatherInputs` with an unseeded buffer, goroutine, 500 ms sleep, one `Write`, `<-done`. Then two builds off one held outcome value:

```go
// First record — the measured one. `sighting` is what the gather returned.
first := finTrailerBuild(outcome, sighting)

// Second record — the contrast. The discriminator is MEASURED off a real second
// poll; the rest of the carrier is derived from the same bytes.
secondBound := trailWaitForTrailer(&stdout, finGatherTrailerWait).BoundFrom
second := finTrailerBuild(outcome, finTrailerSighting(trailScan(stdout.Bytes()), 0, secondBound))
```

Four decisions in those four lines, each of which the row's doc must state:

**1. The observation is never bound.** `.BoundFrom` is read off the call, as #1312's row does and as its doc requires in capitals. A `trailObservation` carries `.Line` and the decoded pointer — the two things the carrier exists to keep out of a caller's reach and the two the file header forbids a failure message from naming (`:114-116`). With no observation in scope a later edit *cannot* `%v` one into a failure.

**2. No `finSightingFrom` constructor.** `finGatherReadings`' own doc rejects one by name ("a constructor would add a symbol whose tests either duplicate this one's or do not exist"). `finTrailerSighting` is the shipped derivation and takes exactly the needed shape; it is how every existing call site reaches the builder. **This ticket adds no symbol.**

**3. The second carrier's scan is `trailScan(stdout.Bytes())`** — pure over bytes, no clock, no observation. It returns `trailSeen` with the decode attached, so `finTrailerSighting` reports `CarriesTrailer` and fills the same four scalars the gather's carrier holds. That is what makes the two records agree on everything except the bound.

**4. The second carrier's `Staleness` is handed `0`, and this is a decision the doc must own.**

- The row asserts nothing about it, so a measured value buys nothing.
- Reading a *second* field off the second poll needs either a bound `trailObservation` (forbidden above) or a **third** `trailWaitForTrailer` call — and a carrier whose duration and discriminator come from two different observations is the exact defect shape this family exists to prevent.
- Handing the **first** sighting's `Staleness` is rejected more sharply for the same reason: it would build, inside the test that proves a record's lateness fields come from one poll, a record whose `staleness_ns` and `lateness_bound_from` came from different polls. It would also contradict the ticket's own accounting ("they will NOT agree on `Staleness`").
- `0` is the no-measurement value. The pairing is therefore **synthetic in that one dimension** — a real start-bound poll stamps a positive `now.Sub(start)` (`result_trailer_observation_test.go:257`) — and the doc must say so in this file's own `synthetic` register.

**Consequence to state plainly in the doc, so a reviewer does not read it as a hole:** because the second record carries `Staleness == 0` beside `Bounded == false`, this row does **not** discriminate a builder deriving `Bounded` from `Staleness != 0`. That mutant is killed by `TestFinTrailerRecordCarriesTheBoundAndItsDiscriminator`'s aborted row (250 ms staleness, `trailBoundNone`, `Bounded` false) and by its start-bound row. Restating it here is not this row's job; this row's job is that the *published* bound is the one the *classifying* sighting measured.

### The outcome argument

Held **identical** across both builds, so the published difference is attributable to the bound and not to the verdict. It is asserted on neither record beyond the shared-agreement check.

Use `trailOutcomeNoRowMatched`. Reason: it is the outcome this file's own header documents for exactly these inputs — a certifying gate over `trailFixtureTrailer`, `MatchCount 0`, `trailAdmitVoidGroupUnnamed` (header `:59-66`) — and this row's inputs are `finGatherNegativeInputs`' minus the seed. The row makes **no claim** that the composition classifies to it; any other member of the closed set would not weaken the row, but it must be the same value on both builds. Do **not** call `trailClassifyRun` to obtain it — that would add a leg the row asserts nothing about.

### Inducing the first-poll miss

Unchanged from #1312, and the mechanism is `trailWaitForTrailer`'s `lastMiss.IsZero()` branch: a zero `lastMiss` yields `trailBoundFromStart`, a non-zero one `trailBoundFromMiss`. A pre-seeded buffer **cannot reach the miss bound at all**, which is why the delayed append is load-bearing rather than decorative.

- `probePollInterval` is 200 ms, `finGatherTrailerWait` is 10 s. **Introduce no second tick constant.** Sleep 500 ms — past two poll ticks, so at least one non-matching poll is certainly observed before the append.
- **No row may wait out `finGatherTrailerWait`.** The trailer arrives well inside it; the loop costs ~600 ms. A genuinely absent trailer burns the full 10 s and is not the arm to use.
- One `Write` call, deliberately: it holds the mutex for its whole body, so a concurrent poll sees either none of the line or all of it. Two writes would let a poll observe a torn JSON line — ordinary input to `trailScan`, which simply does not match, but enough to make the tick on which the trailer becomes visible non-deterministic.

---

## Concurrency model

One spawned goroutine, and only the `finGatherReadings` call crosses into it.

- **Inputs are built on the test goroutine.** `finGatherNeedles` calls `t.TempDir()`, and `probeSyncBuffer.Write` returns an error that needs `t.Fatalf`; calling `t.*` from a spawned goroutine after the test function has returned panics. `finGatherReadings` takes no `*testing.T`, which is what makes the split safe.
- **Lifecycle:** the goroutine `defer close(done)`s and the test blocks on `<-done` before reading `readings` / `sighting`. That channel is the only synchronisation, and it is also what makes the writes to those two variables happen-before the reads. No leak: the goroutine's only exit path is `finGatherReadings` returning, and the append guarantees it does so inside the wait.
- **Shared state is the buffer alone.** `probeSyncBuffer` is mutex-guarded and append-only; `Bytes()` returns a copy, so every scan runs over a private snapshot. The append races nothing under `-race`.
- **Ordering:** both `trailWaitForTrailer(&stdout, …)` and `trailScan(stdout.Bytes())` for the second record run on the test goroutine **after `<-done`**, with no writer left. The bytes are frozen, so the two reads see identical content.

---

## Failure messages

The file header's licence (`:105-122`) is enumerated over the composition's three returns. A `finTrailerRecord` is not one of them, so state in the row's doc where its licence comes from: **`TestFinTrailerRecordCarriesNoCapturedBytes` (`finding_trailer_evidence_test.go:850`)** sweeps that record in the other file, which is what licenses naming one here.

Keep it narrower than that licence allows in practice: name `BoundFrom` (explicitly on the header's MAY list), the derived `Bounded`, the scan state, the outcome, and the four decoded scalars (also on the MAY list, `stop_reason` inherited knowingly). Never name a `trailObservation`, a `trailScanResult`'s trailer, or `pinScan.Matches` — none of which are in scope here by construction.

**This row plants no needle** and must not weaken `TestFinGatherReturnsNoCapturedBytes` (`:1107`), which binds the third return. `trailFixtureTrailer` carries no needle; any `trailNeedle` reachable through these fixtures is incidental rather than a plant of its own.

---

## Testing strategy

Assertions on the **record**, not on the sighting it was built from — the record-tier assertion subsumes the premise it would otherwise need. Do not restate #1312's carrier-tier claims (`sighting.State`, `sighting.BoundFrom`, agreement with `readings.BoundFrom`).

Scenarios, in order:

1. **Premise — the poll matched.** `first.State` must be `trailSeen`; `t.Fatalf` otherwise. At any other state `BoundFrom` is `trailBoundNone` and everything below would be asserting about a record whose sighting measured nothing, arriving as a bare bound mismatch rather than naming itself. Message names the append timing and the wait constant.
2. **AC1, measured.** `first.BoundFrom == trailBoundFromMiss`; `t.Fatalf`. Message: the buffer was EMPTY when the gather started and the append landed past two poll ticks, so a non-matching poll was certainly observed before it.
3. **AC1, published.** `first.Bounded == true`. Message: `lateness_bounded` is `BoundFrom == trailBoundFromMiss` and nothing else, and this is the first row where that `BoundFrom` came from a poll rather than from a table field.
4. **AC2, measured.** `secondBound == trailBoundFromStart`, read via `second.BoundFrom`. Message: over these very bytes, this is what a record filled from a second scan would have published instead.
5. **AC2, published.** `second.Bounded == false`.
6. **The two are separated by measurement, not by argument.** One check over a `[]struct{ field string; got, want any }` table — `Outcome`, `State`, `Subtype`, `IsError`, `TerminalReason`, `StopReason` — comparing `second` against `first`. One branch inside the loop, naming the field on failure. This is the load-bearing half of AC2: without it the two records could differ for any reason at all.
7. **The published Detail tracks the measured bound.** `first.Detail` names `trailBoundFromMiss` and `second.Detail` names `trailBoundFromStart` (the two constants are not substrings of one another, so the check is clean in both directions). New, not a restatement: the record-tier test asserts only `rec.Detail != ""`.

**Do not assert:**

- `first.Staleness` — that is `TestFinGatherSightingComesFromTheClassifiedPoll`'s, and `:1294` says so.
- `second.Staleness` — no claim is made about it (see § Design decision 4).
- The two records' `Staleness` equal — red by construction, and no AC asks for it.
- The gate or the attribution leg — `trailFixtureTrailer` certifies as on every row here; those are other rows'.

Use `t.Fatalf` for 1–2 (a wrong premise or a wrong measured bound makes everything after it noise) and `t.Errorf` for the rest, so one run reports every divergence. Seven branches total.

### Verification

`make check` and `make build` never compile `e2e_realclaude`-tagged files, so a PR whose whole diff sits under that tag is a **vacuous green**. Run both explicitly and expect a PASS/SKIP split rather than all-PASS:

```bash
go vet -tags e2e_realclaude ./internal/e2e/realclaude/...
go test -race -tags e2e_realclaude ./internal/e2e/realclaude/...
go test -race -tags e2e_realclaude -run '^TestFinGather' -v ./internal/e2e/realclaude/
```

Everything here runs offline: synthetic stdout, synthetic stderr, no live claude, no credentials, no daemon, no turn, no `t.Skip`. (`finGatherReadings` execs `ps` via `pinScanArgv` on every row in this file, unchanged by this ticket.)

---

## AC3 — the prose corrections

Three edits in `finding_run_gather_test.go`, plus two cite bumps in the sibling file (§ Cite maintenance). **Locate by symbol, not by the line numbers below** — they are as of `ac25ad8` and the insertion itself moves everything past `:1367`.

**(a) `finGatherTrailerWait`'s wall-clock accounting (`:144-147`).** Currently:

> `- TestFinGatherSightingReportsTheMissBound leaves the buffer empty on purpose and appends the trailer past two poll ticks, so its trailer arrives well inside the wait and the loop costs roughly 600ms — this file's only wall clock, and what the miss bound costs;`

The `only wall clock` clause goes false the moment this row lands. Rewrite the bullet to name **both** rows and keep it a complete accounting — "these two are the file's only wall clock, roughly 600 ms each, and what the miss bound costs". The constant's `NO ROW EVER WAITS IT OUT` claim at `:139` stays true and must survive the edit.

**(b) `TestFinGatherSightingReportsTheMissBound`'s own doc (`:1247`, `:1249`).** *Not enumerated in the ticket; found while sizing. It is inside AC3's leading clause ("the file's prose about the lateness bound is true once the row lands") and it is this row's landing that falsifies it.* Two clauses go false together:

- the heading `# Why this row costs wall clock, and why no other one does`
- the sentence `Every other row here pre-seeds the buffer, so the first poll hits, …`

Both must be re-stated to exempt the new row, which by construction also leaves the buffer unseeded. The rest of that paragraph — the `~0/200/400 ms miss, ~500 ms append, ~600 ms hit` accounting and the `no cheaper route to the interesting value` claim — is unaffected and should not be touched.

**(c) The two `Bounded`-derivation cites (`:287`, `:1297`).** Both read `(finding_trailer_evidence_test.go:209-215)`. That range now lands on unrelated prose about outcome membership ("Nor is the field asked to reject a non-member…"), shifted there by #1320 and #1325. The derivation is at **`:247-253`** — the `Bounded is BoundFrom == trailBoundFromMiss AND NOTHING ELSE` comment through the `Bounded:` field itself. Re-derive that range against the file as it stands when you make the edit (see the ordering rule below); do not copy `247-253` on faith.

### Cite maintenance — two cites the insertion shifts

`finding_trailer_evidence_test.go:104` and `:188` both cite `TestFinSightingReachesNoScanType (finding_run_gather_test.go:1696)`. Inserting the new row at `:1368` moves that symbol down by the row's length, so both numbers go stale — created by this row, exactly the class AC3 exists for. Bump both.

This makes the diff two files. That is the whole of the second file's change: two numbers, no prose. (The package also ships symbol-only cites for this same test — `finding_trailer_evidence_test.go:461-462` names it with no line number — so dropping the numeric suffix is an acceptable alternative if the developer prefers the more robust shape. Pick one and apply it to both cites.)

### Edit order (deterministic — the edits shift each other)

1. Add the new row after `TestFinGatherSightingReportsTheMissBound`.
2. Apply (a) and (b) — both sit **above** the insertion point, so they do not move it, but they do change the gather file's total length.
3. Re-locate `func TestFinSightingReachesNoScanType` in the gather file and bump the two cites in `finding_trailer_evidence_test.go`. Keep those two comment lines the same length if you can, so the evidence file's own line count is unchanged.
4. Re-locate the `Bounded:` derivation in `finding_trailer_evidence_test.go` **after step 3** (step 3 edits sit above it) and apply (c) to `:287` and `:1297` with the range you just read.
5. `gofmt`, then run the three commands above.

---

## What this ticket does not touch

- `Bounded` keeps its single source: `BoundFrom == trailBoundFromMiss` and nothing else. No second derivation, no signature change, no new type, no new constant, **no new symbol**.
- No new no-captured-bytes sweep. `TestFinGatherReturnsNoCapturedBytes` (`:1107`) is unchanged and must not be weakened.
- The gate and the attribution leg run as on every other row and are asserted on by neither record. The carrier is filled from the sighting **regardless** of the gate's verdict — a property of the source (the fill sits at the trailer leg, the attribution guard on `Gate.Reason` sits below it), not of this fixture.
- `docs/knowledge/codebase/1316.md` is **not** a developer deliverable. The documentation phase writes it from this spec plus the merged diff.

---

## Open questions

1. **The second record's `Staleness`.** Specced as `0` with the reasoning above. If review prefers a live pairing over a synthetic-in-one-dimension one, the alternative is a third `trailWaitForTrailer(&stdout, finGatherTrailerWait).Staleness` read — cheap (first poll hits, ~0 wall clock) and still binding no observation, at the cost of a carrier assembled from two indistinguishable-but-distinct observations. The row's claims are unaffected either way; the doc must say which was chosen and why.
2. **The outcome constant.** `trailOutcomeNoRowMatched` is specced because it is what the header documents for these inputs. If a reviewer reads that as an unasserted claim about the classification, any member of the closed set works — the requirement is only that both builds get the same one.

---

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No findings. The only boundary this row crosses is synthetic-stdout-bytes → `trailScan` → `finSighting` → `finTrailerRecord`, and it is *narrowed* rather than widened: `finSighting` structurally carries neither `trailScanResult.Line` (verbatim, capped model output) nor `*resultTrailer`, which is checked — not asserted — by `TestFinSightingReachesNoScanType`. The row introduces no new producer of either type and binds no `trailObservation`, which is the one value in scope that *would* re-expose both. Both inputs (`trailFixtureTrailer`, the reap line) are in-repo constants; nothing model-authored or network-sourced enters.
- **[Tokens, secrets, credentials]** MUST-NOT-FIX-BY-DESIGN, and it is the reason for the label. The credential channel this family exists to keep shut is verbatim argv reaching a published artifact — an operator's `CLAUDE_CODE_OAUTH_TOKEN` / `ANTHROPIC_API_KEY` on a `ps` command line, from `pinScan.Matches` or a raw `ps` column (`finGatherInputs.ClaudeState`, `:255-267`). This row touches neither: it hands `Pinned` a pinned `[]int` (never `[]reachProc`), leaves `ClaudeState` at its zero `""`, and asserts on no field downstream of the argv leg. The `Staleness == 0` decision in § Design is the only place the spec chose *not* to add a reading, and a duration carries no secret in either direction.
- **[File operations]** No findings. The only path constructed is `filepath.Join(t.TempDir(), finGatherNeedleName)` inside the existing `finGatherNeedles` helper, and its own doc pins that **nothing is ever created at that path** — it is a match pattern handed to a Go-side matcher, never a filesystem operand. The row adds no `os.*` call, no write outside `t.TempDir()`, and no path derived from any input.
- **[Subprocess execution]** No findings, and one inherited exec is named rather than left implicit: `finGatherReadings` → `pinScanArgv` execs `ps`, on this row exactly as on the nine that already exist. Its arguments are the `t.TempDir()`-derived needle and `nil` exclusions; no value in this row's diff reaches an `exec.Command` argument. No `sh -c`. The row adds no subprocess and changes no argv construction.
- **[Cryptographic primitives]** Not applicable — no randomness, no hashing, no comparison against a secret. The row's only non-determinism is `time.Sleep` and the poll loop's wall clock, which are timing, not entropy, and are not security-relevant.
- **[Network & I/O]** Not applicable by the ticket's own offline constraint: no live claude, no credentials, no daemon, no turn, no socket, no HTTP server. The one size limit in play is inherited and unchanged — `bufio.Scanner`'s 64 KiB default in `trailScan`, deliberately not raised (`result_trailer_observation_test.go:161-163`).
- **[Error messages, logs, telemetry]** The one category where this spec had to make a call rather than inherit one, because the file header's failure-message licence is enumerated over the composition's **three returns**, and a `finTrailerRecord` is not one of them. Resolved in § Failure messages: the licence is named explicitly (`TestFinTrailerRecordCarriesNoCapturedBytes`, the other file's sweep), and the specced messages stay strictly inside the header's MAY list — `BoundFrom`, the derived `Bounded`, scan state, outcome, and the four decoded scalars, with `stop_reason`'s uncapped model-authored exposure inherited knowingly rather than by omission. The prohibition that actually bites here — a `trailObservation` `%v`'d into a failure — is held **structurally**: the spec forbids binding one, so no later edit can reach it. SHOULD FIX for code-review, not a gate: verify no message in the delivered row names a `trailScanResult`'s trailer or `pinScan.Matches`.
- **[Concurrency]** No findings. One goroutine, one `close(done)` exit path, one `<-done` join before any read of the values it writes. The only shared state is `probeSyncBuffer` — mutex-guarded, append-only, `Bytes()` returns a copy — so the delayed append races nothing under `-race`. The single-`Write` rule is specced for determinism (a torn JSON line is ordinary non-matching input, but it makes the visibility tick non-deterministic), and both post-`<-done` reads run on the test goroutine with no writer left, so the second observation and the second scan see identical frozen bytes. Goroutine leakage is bounded by the append landing well inside `finGatherTrailerWait`.
- **[Threat model alignment]** No relay surface and no CLI surface; this is a test-tier instrument. The one threat it is *aligned to* is the family's own: a published artifact destined for a public issue must not carry verbatim argv or verbatim model output, and must not carry a non-bound wearing a bound's label. This row strengthens the second half — it is the first proof that the published `lateness_bounded` was measured by the poll that classified the run — and is neutral on the first.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-08-05
