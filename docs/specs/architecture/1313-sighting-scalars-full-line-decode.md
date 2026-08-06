# #1313 — Prove the sighting carrier's four scalars come from the full-line decode

**Ticket:** [#1313](https://github.com/pyrycode/pyrycode/issues/1313) (split from #1310) · **Size:** S · **Labels:** `security-sensitive`
**Baseline:** `8ce6a7f` (current `main`, after #1312 merged). Every line number below was re-measured at that commit.

## Files to read first

| Path + lines | What to extract |
|---|---|
| `internal/e2e/realclaude/finding_run_gather_test.go:1369-1471` | `TestFinGatherSightingCarriesTheDecodedScalars` — the fill check this ticket turns into a measurement. Its recompute idiom (`:1426-1430`) is what AC3 mandates; its doc holds two of the three stale cites. |
| `internal/e2e/realclaude/finding_run_gather_test.go:1242-1367` | `TestFinGatherSightingReportsTheMissBound` (#1312) — **the shape to mirror.** Standalone test, a measured contrast, a doc that says what it does not assert. |
| `internal/e2e/realclaude/finding_run_gather_test.go:105-122` | The file header's failure-message rule. A message MAY name the four scalars; it MAY NEVER name the observation's `Line` or a `trailScanResult`'s trailer. This is the binding constraint on the new test. |
| `internal/e2e/realclaude/finding_run_gather_test.go:422-505` | `finGatherReadings` — the gather. The carrier fill is `:442-453`; the gate is computed at `:431` and the attribution guard at `:467`. |
| `internal/e2e/realclaude/finding_run_gather_test.go:1107-1146` | `TestFinGatherReturnsNoCapturedBytes` — the shipped idiom for asserting about `Line` **without printing it** (`:1141-1145` prints `len(line)` only). Do not modify this test. |
| `internal/e2e/realclaude/finding_run_gather_test.go:560-571`, `:644-651` | The two correct targets for stale cites 2 and 3 (C4's row; the C2 recompute idiom). |
| `internal/e2e/realclaude/result_trailer_observation_test.go:98-121` | `trailScanResult` — `Line` is capped and OPERATOR-REVIEW-BEFORE-PASTE; `Trailer` is "the decode of the FULL line, not of `Line`". This doc is the claim under test. |
| `internal/e2e/realclaude/result_trailer_observation_test.go:164-208` | `trailScan` — `Line: reachCapCommand(...)` at `:182`, `Trailer: &tr` at `:183` decoded from the full line. The two reads, three lines apart. |
| `internal/e2e/realclaude/result_trailer_observation_test.go:297-313` | `trailNeedle`'s "placed PAST the cap" design intent, and `trailPaddedTrailer`'s wire order with `terminal_reason` last. |
| `internal/e2e/realclaude/finding_artifact_write_test.go:167-191` | **`finWriteTrailerPad` — the precedent for this ticket's new constant.** A single-consumer pad in its own `--- the fixture ---` section, doc carrying the measured byte offsets, declared immediately above its consumer. Its `:180-183` already measured pad 200 independently. |
| `internal/e2e/realclaude/background_reach_probe_test.go:123-124`, `:945-950` | `reachMaxCommandBytes = 512`, `reachTruncationMarker`, and `reachCapCommand`'s `s[:512] + marker` shape. |
| `internal/e2e/realclaude/tool_loop_test.go:194-203` | `resultTrailer`'s field set. It is comparable with `==`; it has no `result` member. |

## Context

`finGatherReadings` returns a `finSighting` whose four decoded scalars must come from `trailScanResult.Trailer` — the decode of the **full** line — and never from `trailScanResult.Line`, which `reachCapCommand` truncates at 512 bytes. That separation is what makes the cap safe: truncation degrades human-readable evidence and never a field the consumer branches on.

The shipped fill check cannot prove it and says so. Its decoded arm runs over `trailFixtureTrailer`, 342 bytes — under the cap. There the capped copy **is** the full line, so both reads give the same answer and the row passes whichever one the implementation used. This ticket supplies a fixture where the two reads give different answers, and asserts the disagreement as a precondition so the row cannot go quietly vacuous.

Nothing about the carrier's shape changes. No call site moves. One file is touched.

## Design

### D1 — A standalone test, not a third table row

The row ships as **`TestFinGatherSightingScalarsComeFromTheFullLineDecode`**, inserted after `TestFinGatherSightingCarriesTheDecodedScalars` ends (`:1471`) and before `TestFinSightingReachesNoScanType`'s doc begins (`:1473`).

The file already splits exactly this way for exactly this reason, one property earlier: `TestFinGatherSightingComesFromTheClassifiedPoll` (`:1214`) makes the cheap, pre-seeded, non-discriminating claim about the bound, and `TestFinGatherSightingReportsTheMissBound` (`:1299`) is a separate test that pays for a fixture where the two candidate reads diverge and **measures** the contrast. #1312 established that shape four days ago for the bound; this is its exact analogue for the scalars.

Folding it into the table instead would put preconditions on the struct that two of three rows carry as dead fields, and branch the shared body on a per-row flag — diluting both claims.

Consequence for the table's doc: its opening sentence, "across the two arms the shipped fixtures already reach" (`:1369-1371`), stays **true** and must not be touched. Only the `#1310` pointer inside it changes — see D5.

### D2 — `pad = 200`, and why the precondition pins the KEY

`trailPaddedTrailer(200)` renders a 585-byte line. Measured at `8ce6a7f`:

| | byte offset on the line | inside the 512-byte cap? |
|---|---|---|
| `"subtype"` | 17 | yes |
| `"is_error"` | 45 | yes |
| `"stop_reason"` | 348 | yes |
| `"terminal_reason"` | 555 | **no — key and value both cut** |
| `trailNeedle` | [304, 346) | yes |

The usable window is **pad ∈ [141, 366]** under the strict pin (below 141 the `"terminal_reason"` key survives the cap; above 366 the needle falls past it). 200 sits comfortably inside.

**The precondition pins the key `"terminal_reason"`, never the value `max_turns`.** `trailPaddedTrailer` renders `subtype: "error_max_turns"`, so `"max_turns"` first occurs at byte **34** and survives every cap — `strings.Contains(scan.Line, "max_turns")` is `true` even when `terminal_reason` was cut. A value pin is silently vacuous. The developer must state this in the assertion's comment; it is the one edit a later "simplification" would reach for.

Pinning the bare key is stricter than pinning the `"terminal_reason":"max_turns"` pair (the key pin rejects pad ≤ 140, the pair pin only rejects pad ≤ 128), so pin the key.

### D3 — Where the new constant lives, and what its doc must carry

Declare **`finGatherOverCapPad = 200`** at package level, in its own `// --- the over-cap fixture ---` section header immediately above the new test — not in the top constants block at `:137`.

Two reasons, both load-bearing:

1. `finWriteTrailerPad` (`finding_artifact_write_test.go:167-185`) is the same package's precedent: a single-consumer pad, declared beside its consumer, with a doc that carries the measured offsets rather than asserting them in prose elsewhere.
2. **Declaring it below `:1471` leaves every line above it unshifted**, so the two line-number corrections in D5 (`:560-571`, `:649`) are still correct in the landed file. A constant added at `:137` would shift both targets by its own length and re-break the cites this ticket exists to fix. This is a structural guarantee, not a discipline the developer has to remember.

The doc must state, at minimum:

- the measured byte offsets above, and that the offsets are a property of the **pad**, not an invariant of the fixture (`finWriteTrailerPad:183-184` makes the same disclaimer);
- the usable window [141, 366] and what each end is bounded by;
- the `max_turns`-at-byte-34 trap;
- **the needle note.** At this pad `trailNeedle` lands *inside* the retained `Line`, which is the opposite of the intent stated on `trailNeedle` itself ("placed PAST the cap so a record that leaked it could only have done so by recording the line in full"). That is safe for this test, which plants no needle of its own, marshals nothing and asserts no leak claim. It means this pad **must not be reused by a sweep whose argument is "a needle sighting proves the line was recorded in full"** — for that pad the needle in `Line` is expected. (`finWriteTrailerPad` deliberately takes the other position for its own sweep, where in-cap is what makes the plant non-vacuous. Both conventions are correct; the doc has to say which one this pad is.)

### D4 — The measurement

The test body, in order. Shapes only — the developer writes the messages in the file's idiom.

1. **Seed and scan.** `seed := finGatherSeed(t, &stdout, trailPaddedTrailer(finGatherOverCapPad))`, then `scan := trailScan(seed)` — the shipped scanner over the same bytes the gather will read. `want := *scan.Trailer`, guarded on `scan.State == trailSeen` and `scan.Trailer != nil` first, exactly as `:1426-1430` does. **This is AC3: no scalar is typed in.**

2. **Precondition — the decode has it.** `want.TerminalReason != ""`. Literal-free, and the zero `resultTrailer` has `""`, so non-empty means the full-line decode genuinely filled it.

3. **Precondition — the capped copy lacks it.** `!strings.Contains(scan.Line, "\"terminal_reason\"")`. Together with (2) this is **AC2**: the two reads demonstrably disagree, and a fixture edit that removed the disagreement fails loudly here.

4. **The contrast — what a re-read of `Line` would have reported.** Unmarshal `scan.Line` into a **freshly declared** `var reread resultTrailer`:
   - the error is non-nil. The capped copy is `line[:512]` plus the truncation marker — JSON cut mid-token — and `json.Unmarshal` returns `unexpected end of JSON input`. Assert non-nil; **do not pin the message text.**
   - `reread` disagrees with `want` on **all four** scalars — one check, an `||` of equalities, the mirror image of the fill check's `||` of inequalities at `:1460-1462`.

5. **The claim.** Run the gather (`Stdout`, `Needles`, `PyryExited: true` — no `Stderr`, no `Pinned`, as the shipped table row does), assert `sighting.State == trailSeen`, `sighting.CarriesTrailer`, and the four scalars `== want`'s.

**Why the contrast must be measured and not argued.** `json.Unmarshal` validates the whole input before it decodes anything, so a truncated line fills **nothing** — the alternative read is all-or-nothing, not per-key. That is the entire reason all four assertions bite when only `terminal_reason`'s bytes were cut, and it is a property of `encoding/json` rather than of this fixture. Verified at `8ce6a7f` against a pre-poisoned destination: on the syntax error `Unmarshal` left the destination untouched, so a freshly declared `reread` is **exactly** the zero `resultTrailer` (`subtype ""`, `is_error false`, `stop_reason ""`, `terminal_reason ""`) against a full-line decode of (`error_max_turns`, `true`, `end_turn`, `max_turns`). Declaring `reread` fresh is therefore load-bearing: reusing a filled variable would leave stale values in it.

**The failure-message constraint (this is the security-relevant one).** The header rule at `:114-116` forbids a message from naming the observation's `Line`. It binds harder here than anywhere else in the file, because at this pad the retained `Line` really does carry padded stand-in payload plus the needle. Messages in this test may name: `scan.State`, `scan.Detail` (counts only — no captured bytes), `len(scan.Line)`, `reachMaxCommandBytes`, `finGatherOverCapPad`, the key being sought, and the four scalars on either side. They may **never** `%v` / `%q` `scan.Line`, `scan`, `scan.Trailer`, `want` whole, or `reread` whole. `:1141-1145` is the shipped model: it reasons about `Line` and prints only `len(line)`.

### D5 — The three stale pointers, and nothing else

Correct exactly these three. **Do not sweep the file for others.**

| Site | Now reads | Must read |
|---|---|---|
| `:1378` | "That discrimination … is **#1310's**" | Name **`TestFinGatherSightingScalarsComeFromTheFullLineDecode`** — **by symbol only, no line number.** #1310 is CLOSED (split into #1312 and this ticket). A bare symbol name cannot go stale under this same diff; a self-cite would. |
| `:1408` | "finGatherCases()' C4 row **(:413-423)**" | **`(:560-571)`**. `:413-423` points inside `finGatherReadings`' body. |
| `:1423` | "the file's own C2 idiom **(:502)**" | **`(:649)`** (block `:644-651`). `:502` is `readings.ClaudeState = in.ClaudeState`; `:649` is `if want := trailGate(trailScan(seed)); readings.Gate != want` — the recompute-through-the-shipped-producer shape AC3 tells the implementation to copy. In scope precisely because it labels that idiom. |

The neighbouring `(:148-150)` cite at `:1389` was fixed by #1312 and is correct — **leave it alone.**

Both line-number corrections point at targets *above* the edit region, and D3 keeps the whole edit *below* `:1471`. Nothing shifts. If the developer nonetheless adds a line above `:560`, both cites must be re-measured before commit.

### What this test does not assert, and must not

- **The gate and the attribution.** `terminal_reason: "max_turns"` makes `trailGate` return `trailGateBudgetFired` with a non-empty `Reason` (`trailer_admissibility_test.go:302-312`), so the gather's attribution guard at `:467` is satisfied and that leg runs, returning a structural void for the budget path. Incidental. This row is about the trailer leg; asserting on either would restate rows the file already ships. (The leg already runs with nil `Stderr`/`Pinned` on the shipped table's first row, so this is not a new path.)
- **That the fill is independent of the gate's verdict.** If the doc mentions it at all, say the fill reads only `obs` and never reads `readings.Gate` — **independence by data dependence.** It is *not* an ordering argument: `readings.Gate` is computed at `:431`, *ahead* of the fill at `:442-453`. Do not paste an ordering phrasing forward.
- **`Staleness` and `BoundFrom`.** Owned by `TestFinGatherSightingComesFromTheClassifiedPoll` and `TestFinGatherSightingReportsTheMissBound`. `Staleness` travels as published evidence and is not a classifier input; nothing here may make it one.
- **Any leak claim.** This test plants no needle of its own and marshals nothing. `TestFinGatherReturnsNoCapturedBytes` (`:1107`) already sweeps all three returns; this row must not weaken it and must not be added to it. `finGatherNeedleTrailer` and `trailFixtureTrailer` are **not to be modified** — the former's in-cap needle position is a premise of that sweep (`:1138-1146`).

### Cost and wall clock

The buffer is pre-seeded, the 585-byte line is far under `bufio.Scanner`'s 64 KiB default, and it decodes as `type:result` — so the first poll hits and the loop returns before it ever sleeps. That falls under the existing first bullet of `finGatherTrailerWait`'s doc ("the pre-seeded rows still hit on the FIRST poll", `:142-143`); **no edit to that doc is needed.**

## Testing strategy

The deliverable *is* a test. Scenarios it must discriminate — each is a mutation that has to go RED:

- **The carrier filled from a re-read of `Line`.** Reject: all four scalars go red against `want`.
- **The carrier filled from a re-read of `Line` for `terminal_reason` only.** Reject: `terminal_reason` goes red.
- **The pad shrunk below 141.** Reject: the AC2 precondition fires — the capped copy still carries the key, so the two reads no longer disagree.
- **The pad raised above 366.** Not rejected by this test, and it need not be: the disagreement still holds. The pad's doc records why 366 is the ceiling (the needle would fall past the cap and the pad would stop being reusable for the sweeps that want it in-cap).
- **`reachCapCommand` changed so the capped copy is valid JSON again.** Reject: the non-nil-error check in D4.4 names the cause directly instead of leaving a confusing four-way agreement.
- **The precondition weakened from the key to the value `max_turns`.** Would pass vacuously — which is why D2's comment is mandatory and why code-review should look for it.

Verification commands. `make check` and `make build` never compile `e2e_realclaude`-tagged files, so a PR whose whole diff sits under that tag is a **vacuous green**:

```bash
gofmt -l internal/e2e/realclaude/finding_run_gather_test.go
go vet -tags e2e_realclaude ./internal/e2e/realclaude/...
go test -race -tags e2e_realclaude -run '^TestFinGather' -v ./internal/e2e/realclaude/
go test -race -tags e2e_realclaude ./internal/e2e/realclaude/...
```

Expect a PASS/SKIP split on the last one, not all-PASS. The new test must PASS, not SKIP — it takes no live claude, no credentials, no daemon and no turn, and carries no `t.Skip`.

## Sizing

Bottom-up: one new constant with its doc (~22 lines), one new test with its doc (~100 lines), three cite corrections (~5 lines). One file. Zero new exported types. Zero call sites.

Checked against the nearest analogue rather than trusted bottom-up: **#1312 (`8aead05`)** — the same shape, same file, one standalone test plus doc plus one cite fix — measured **145 insertions / 9 deletions**. This ticket is that plus one constant and two more cite fixes, so ~145 is the floor and ~190 the projection. Comfortably inside S; no red line is near.

## Open questions

None blocking. Two notes for the developer:

- **#1315** is open against this same file at disjoint regions (`finSighting`'s doc `:281-282`/`:311`, and `finGatherReadings`' body `:445-446`) and has no branch on `origin` as of this spec. It touches nothing in the region edited here and there is no API dependency either way — but whichever lands second rebases, so re-measure any line number before writing it down.
- Locate by symbol name, not by line. Every number in this spec was measured at `8ce6a7f` and is stated to be re-checked, not trusted.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No new boundary. The one boundary in play is the existing `trailScanResult` split — `Line` (untrusted, verbatim model output, OPERATOR-REVIEW-BEFORE-PASTE) versus `Trailer` (the decode, structurally unable to carry the `result` payload because `resultTrailer` has no such member). This ticket does not move it; it *asserts* it, which strengthens it. The test holds a `trailScanResult` in scope — which the shipped fill check already does at `:1426` — and the type signals the distinction by the field docs at `result_trailer_observation_test.go:100-118`.
- **[Error messages, logs, telemetry]** **MUST FIX — addressed in D4.** This is the one real finding, and it is specific to this pad. Every other fixture in `finding_run_gather_test.go` keeps the modelled payload short or past the cap; at `pad = 200` the retained `Line` carries 200 bytes of stand-in payload **plus `trailNeedle` at [304,346), inside the cap**, and no sweep in the file covers this row. A `t.Fatalf("...%q", scan.Line)` here would put that into a published artifact and **no test would catch it** — `TestFinGatherReturnsNoCapturedBytes` runs over its own fixture, not this one. D4 therefore states the permitted and forbidden message operands explicitly and points at `:1141-1145` as the shipped pattern for reasoning about `Line` while printing only `len(line)`. Code-review should grep the new test for `scan.Line`, `%v`-of-`scan`, and `%+v`-of-`want`/`reread`.
- **[Error messages — second order]** SHOULD FIX. `scan.Detail` is safe to print (it is `reachCapCommand` over counts, no captured bytes) and the shipped fill check prints it at `:1433`. Named here so the developer does not over-correct the finding above into removing a diagnostic the file already licenses.
- **[Subprocess / external command execution]** No findings, but not vacuously: `finGatherReadings` calls `pinScanArgv`, which execs `ps` and reads the ambient process table. That is unchanged shipped behaviour on every row of this file, the needles are `t.TempDir()`-derived paths under which nothing is staged, and `Pinned` is `nil` here. The `[]int` signature that keeps verbatim argv out of the readings (header `:43-47`) is untouched. This ticket adds no exec and no new argv reader.
- **[Tokens, secrets, credentials]** Not applicable by construction. Nothing here reads `CLAUDE_CODE_OAUTH_TOKEN`, `ANTHROPIC_API_KEY`, or any credential; the modelled secret is the synthetic `trailNeedle` constant. The channel by which a real credential could reach an artifact is the `ps` command column, which `Pinned []int` and `pinStateColumns` structurally close and which this ticket does not widen.
- **[File operations]** Not applicable. The test writes no file. Artifact writing is `finding_artifact_write_test.go`'s and is not touched.
- **[Concurrency]** Not applicable, and deliberately so: unlike `TestFinGatherSightingReportsTheMissBound`, this row pre-seeds the buffer and spawns no goroutine, so there is no `t.*`-from-a-goroutine hazard and no shared state beyond the mutex-guarded `probeSyncBuffer` the gather reads.
- **[Network & I/O]** Not applicable — no socket, no server, no reader without a cap. The only size limit in play is `reachMaxCommandBytes = 512`, which this ticket asserts against rather than changes.
- **[Cryptographic primitives]** Not applicable — no randomness, no comparison against a secret. The `==` comparisons are between test-fixture scalars, not credentials, so constant-time comparison is not owed.
- **[Threat model alignment]** The relevant threat is the one this whole instrument family exists for: an operator pasting a probe artifact into a public issue and shipping verbatim model output or argv with it. This ticket serves it — it converts "a comment says the scalars come from the decode" into a measurement, which is what licenses the cap being applied to `Line` alone. The residual risk is entirely the message-operand one above.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-08-05
