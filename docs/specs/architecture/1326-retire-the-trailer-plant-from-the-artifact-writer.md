# #1326 — Retire #1286's trailer plant from the artifact writer's channel list

**Size:** S · **One file:** `internal/e2e/realclaude/finding_artifact_write_test.go` · **Offline. No signature, no type, no constant, no new symbol, no new sweep, no repad.**

Line numbers are as of `ac25ad8`. **Locate by symbol, never by line.**

---

## Files to read first

| Path | What to extract |
|---|---|
| `internal/e2e/realclaude/finding_artifact_write_test.go` — **the whole file (970 lines)** | The only file this ticket edits. Read it end to end before the first edit: every site below is surgery on prose that argues for itself, and §7's "do not touch" list is as load-bearing as the edits. |
| `…/finding_artifact_write_test.go:35-59` | The file header's **forbidden-symbol rule**: every exec-bearing helper is referenced by file:line **and never by name**, so the grep reports on this file's CODE and cannot be defeated by its own prose. §6 explains why this survives the ticket's otherwise-prefer-symbols instruction. |
| `…/finding_artifact_write_test.go:165-192` | `finWriteTrailerPad` (`:167-185`) and `finWritePlantedTrailerScan` (`:187-192`) — AC4's two kept symbols. Note `:178-180`'s precedent cite (AC5's second correction) and `:180-184`'s measured offsets. |
| `…/finding_artifact_write_test.go:207-291` | `finWriteInputs` — the header claim (`:208`), the carried-whole list (`:216-222`), the numbered plant list (`:224-230`), and the marked call site (`:278-286`) carrying the dangling `#1321's to revisit` deferral. |
| `…/finding_artifact_write_test.go:622-667` | `TestFinWriteArtifactsCarryNoCapturedBytes`' doc: the header claim (`:624-626`), "# Why zero" (`:628-636`), "# Why the trailer plant lands INSIDE the cap" (`:638-642`, retired), "# The mandated mutations" (`:644-667`, untouched). |
| `…/finding_artifact_write_test.go:668-794` | The test body. Channels 1–2 (`:675-685`), channel 3 (`:687-703`, retired/relocated), channel 4 (`:705-726`), the **pre-build sub-record clean check** (`:728-747`, AC2's landing site), the write (`:749-751`), the **headroom walk** (`:753-780`, untouchable), the sweep (`:782-793`). |
| `…/finding_artifact_write_test.go:904-946` | The pairing subtest — AC3's second survivor. `:919-923` is the wire-values comment; `:941-945` is AC5's first stale cite. |
| `…/finding_trailer_evidence_test.go:814-849` | **#1325's `RETIRED BY #1325` note — the template for this ticket's note.** Shape to copy: what died, why it was a test of nothing, where the claim now holds *by symbol*, what remains and why, and "NOT REPADDED past the cap" stated explicitly. |
| `…/finding_trailer_evidence_test.go:318-328` | `finTrailerSighting` — **the sub-builder that now drops the line.** Reads `scan.State`, `scan.Trailer`'s four scalars; never `scan.Line`. This is the whole factual basis of the retirement. |
| `…/finding_trailer_evidence_test.go:484-581` | `TestFinTrailerSightingScalarsComeFromTheFullLineDecode` — AC5's first correction target. Pad **2000** (`:539`), precondition that `terminal_reason` is cut from `Line` (`:543-547`). **Same file as the stale cite points at** — see §8. |
| `…/finding_trailer_evidence_test.go:872-905` | #1325's shipped comment naming *this file's* Detail walk — "asserts it found at least six with the trailer's among them, and applies this identical `room < len(trailNeedle)` test per path". This is why AC5 freezes the headroom walk. |
| `…/finding_run_gather_test.go:1077-1192` | `TestFinGatherReturnsNoCapturedBytes` — **where the retired claim now holds.** The in-cap precondition asserted **in code** is `:1138-1146` (the shape §3's relocated guard mirrors); the carrier is swept as the third return at `:1158-1191`, argued at `:1164-1172`. |
| `…/finding_run_gather_test.go:1477-1524` | `finGatherOverCapPad`'s doc — its **two** references to `finWriteTrailerPad` (`:1494`, `:1521`). **Read-only.** AC4 keeps them valid by not deleting the constant. |
| `…/result_trailer_observation_test.go:176-188` | `trailScan` sets `Line: reachCapCommand(string(scanner.Bytes()))`. This one line is the proof behind §3's collapse of two equivalent guards into one. |
| `…/result_trailer_observation_test.go:297-313` | `trailNeedle` (42 bytes) and `trailPaddedTrailer` — the needle is spliced into `result` at **every** pad, which is what keeps AC3's pairing check non-vacuous with no cap guard of its own. |
| `…/tool_loop_test.go:194-203` | `resultTrailer` — **no `result` member.** Why the four decoded scalars cannot carry the needle at any pad. |
| `docs/specs/architecture/1325-narrow-trailer-evidence-to-the-carrier.md:215-219` | #1325's Open Question 2 hands this ticket the complete list of stale facts at `:941-945`: symbol renamed, line range stale since #1320, and "pad 200" wrong. |

**Do not edit** `finding_trailer_evidence_test.go` (#1325's, merged), `finding_run_gather_test.go` or `finding_run_record_test.go` (#1324's, merged).

---

## Context

`finTrailerBuild` takes a `finSighting` since #1320. The carrier holds eight scalars — no `.Line`, no `*resultTrailer`. The needle planted in `finWritePlantedTrailerScan()`'s line is therefore consumed at **fixture-construction** time: `finTrailerSighting` copies four scalars off the decode and never reads `scan.Line`. Nothing the trailer channel plants ever enters `finRecordInputs`, so the writer under test performs no reduction there.

Two consequences the file still gets wrong:

1. **The plant list names a reduction this writer does not perform.** Its third entry, "the trailer scan's Line — dropped by `finTrailerBuild`", is wrong twice over: the builder does not drop it (it never sees it), and the dropping happens one tier below the writer.
2. **The file contradicts itself in the same doc comment.** `finWriteInputs:216-222` already lists `Trailer` among the inputs "carried verbatim BY DESIGN … Trailer whole including the four decoded scalars", and then `:228` lists it again as an input that is reduced. After the retirement the trailer appears **only** in the carried-whole list, where it already appears and where it belongs. That is the cleanest statement of why this retirement is right, and it belongs in the note.

The dangling deferral at `:278-283` (`SUPERSEDED BY #1320 AND RETIRED BY #1321 … the plant list at :228 is #1321's to revisit`) has nowhere left to go: #1321 closed without touching either site. **It is discharged here, not forwarded.** No site this ticket writes may name a future ticket as the owner of remaining work.

**The trap** — and #1323's body conflated these — is that the plant's surface and the fixture's surface are different sets. Retiring the *claim* is right; deleting the *fixture* is not. `finWriteTrailerPad` and `finWritePlantedTrailerScan` both keep live consumers, and the constant is referenced twice by prose in a file this ticket may not edit.

---

## Design

Everything below is prose surgery plus **one relocation and one deletion of a logically redundant check**. No signature, type or constant changes; no new sweep; no repad; no import change (verify with `go vet`, do not assume).

### §1 — The retirement / survivor map

| Site | Content | Verdict |
|---|---|---|
| `:208` | `finWriteInputs`' header claim: "planted in every input the pipeline REDUCES OR DROPS" | **Kept, qualified** (§2) |
| `:224-230` | the numbered plant list, entry 3 | **Entry 3 RETIRED**; list renumbered 1–3 (§2) |
| `:278-286` | the marked call site + its `#1321's to revisit` deferral | **Mark replaced by a retirement note; deferral discharged** (§2) |
| `:624-626` | the test's header claim | **Kept, qualified** (§2) |
| `:638-642` | "# Why the trailer plant lands INSIDE the cap" | **REPLACED** by the retirement note — the full argument lives here (§2) |
| `:687-692` | channel 3: `line :=` local + `scan.State != trailSeen` | **`line` local deleted; state check RELOCATED** as a diagnosis guard (§3) |
| `:693-698` | channel 3: the needle-offset-vs-cap check | **DELETED as redundant** — logically identical to `:699-703` (§3) |
| `:699-703` | channel 3: `scan.Line` carries the needle | **RELOCATED** to the pre-build clean check, re-argued (§3) |
| `:705-726` | channel 4 (reap stderr) premises | **Kept**, renumbered to channel 3 (§2) |
| `:728-747` | the pre-build sub-record clean check | **Kept; argument RE-STATED**, and it acquires the relocated guard (§3, §4) |
| `:753-780` | the headroom walk — floor + per-path `room` test | **UNTOUCHED. Byte-for-byte.** (§6) |
| `:787-791` | the sweep's failure message enumeration | **Enumeration corrected** (§2) |
| `:167-185` | `finWriteTrailerPad`'s doc | **Kept; doc RE-STATED**, cite corrected (§5, §6) |
| `:187-192` | `finWritePlantedTrailerScan`'s doc | **Kept; doc RE-STATED** (§5) |
| `:919-923`, `:941-945` | the pairing subtest's comment and its stale cite | **Kept; argument RE-STATED**, cite corrected (§4, §6) |

### §2 — AC1: the six sites, and the transported claim

**Where the full argument lives: once.** It replaces `:638-642` on `TestFinWriteArtifactsCarryNoCapturedBytes`' doc, in #1325's `RETIRED BY` shape. Everything else carries a short, locally-true statement. Six copies of one argument is the drift surface this family keeps paying to fix; #1325's shipped note is one note plus marks.

The note must carry, in this order:

1. **What is retired**: #1286's Plant #3, the trailer scan's `Line`, and the channel-3 preconditions that certified it.
2. **Why it was a test of nothing at this tier**: since #1320 the needle is consumed at fixture-construction time by `finTrailerSighting`, which copies four scalars off the decode and never reads `scan.Line` — so it never enters `finRecordInputs` and the writer under test reduces nothing there. Name `finTrailerSighting`, not `finTrailerBuild`.
3. **Where the claim now holds, by SYMBOL**: `TestFinGatherReturnsNoCapturedBytes` (`finding_run_gather_test.go`) sweeps the gather's `finSighting` carrier as its third return and asserts the same **in-cap** precondition **in code** — that the needle survived the 512-byte cap in the retained copy — so what it plants is something a leaking value would actually leak. **Transport the in-cap claim, never the past-the-cap one**: a record carrying the CAPPED `Line` publishes ~415 bytes of model-chosen text while a past-the-cap sweep passes green. Note it sweeps the **carrier**, not this record: the transport argument is that the carrier holds no needle and the builder reads only the carrier.
4. **That the trailer input now appears only where it always belonged**: in `finWriteInputs`' carried-whole list, which already names `Trailer` whole including the four decoded scalars.
5. **What still runs, by symbol**: the pre-build clean check on the trailer sub-record (§3) and the pairing subtest (§4) — with a one-line pointer, not a restatement.

**No site may re-defer.** No "#N's to revisit", no "a later ticket decides". If something is genuinely out of scope, say it is out of scope and say why (§7 has the list).

**The call site (`:278-286`).** Replace the `SUPERSEDED BY … RETIRED BY … is #1321's to revisit` mark with two or three lines: the trailer input is carried whole, its fixture line carries the needle by construction and that is not a plant of this sweep, and the retirement note on `TestFinWriteArtifactsCarryNoCapturedBytes` holds the argument. Name `TestFinGatherReturnsNoCapturedBytes` here too — this is where a reader meets the fixture, and one by-symbol pointer at each of the two natural entry points is worth the repetition. Nowhere else.

**The plant list (`:224-230`).** Delete entry 3. **Renumber the remaining three contiguously (1, 2, 3)**, and renumber the matching `channel N (…)` prefixes in the surviving `t.Fatalf` messages at `:705-726`. Add to the end of "# Plant only where the pipeline reduces" a short paragraph: the trailer fixture's line still carries the needle by construction of `trailPaddedTrailer`, it is **not** a plant of this sweep, and its two consumers are §3's guard and §4's pairing.

> Considered and rejected: leaving a gap (a message naming "channel 4" against a three-entry list sends the reader looking for an entry that no longer exists) and dropping the numbers entirely in favour of names (a better end state, but a refactor of surviving prose this ticket has no mandate for).

**The two header claims (`:208`, `:624-626`).** "Planted in every input the pipeline REDUCES OR DROPS" stays true of the three that remain, but must be qualified in the same breath so a reader does not count the trailer fixture's needle as a fourth plant: the trailer fixture carries the needle too, by construction, and is not one of them.

**The sweep's failure message (`:787-791`).** Drop "the trailer scan's Line" from the enumeration. Everything else in that message — "the record retains no trailer line in any form, capped or otherwise, so one occurrence means a reduction was widened back into a retention" — stays true and stays. **Add one clause** naming the trailer fixture's line as a needle-bearing input that is not a plant of this sweep. Without it, a red result caused by a future `finTrailerSighting` that started copying `Line` would print an enumeration of three channels that are all clean, and send the reader to the wrong tier.

### §3 — AC2: the one guard that survives, and where it lands

**The surviving assertion that rests on the in-cap plant is the pre-build clean check on the embedded trailer sub-record** (`:728-747`, the `{"trailer", in.Trailer}` row). Its only leak channel is the retained `Line`: `resultTrailer` has no `result` member, so the four decoded scalars cannot carry the needle at **any** pad. Without the in-cap guard the row degenerates into asserting a shape the type already forbids.

**So the guard moves to that check rather than being deleted with the plant list.** Delete the `line := trailPaddedTrailer(finWriteTrailerPad)` local — it has no other reader — and place immediately above the sub-record loop:

- `scan := finWritePlantedTrailerScan()`
- a `t.Fatalf` if `scan.State != trailSeen`, **labelled honestly as a DIAGNOSIS guard, not a non-vacuity guard**: one `Fatalf` naming the state and its Detail beats a confusing "the retained copy carries no needle". `TestFinTrailerSightingScalarsComeFromTheFullLineDecode:551-556` is the shipped precedent for labelling a guard this way rather than overclaiming it.
- a `t.Fatalf` if `!strings.Contains(scan.Line, trailNeedle)` — **the guard**. Its message names `reachMaxCommandBytes`, `len(scan.Line)` and `finWriteTrailerPad`, and states what a failure means: a repad past the cap leaves the row below asserting the absence of a needle the sub-builder was never handed. `finding_run_gather_test.go:1141-1146` is the shipped model for both the shape and the message.

**Delete the byte-offset check (`:693-698`) rather than relocating it.** It is not a second guard; it is the same predicate computed a second way, and the two cannot disagree: `trailScan` records `Line: reachCapCommand(string(scanner.Bytes()))`, and `reachCapCommand` returns the line unchanged at ≤ 512 bytes and `line[:512] + marker` above it — so `strings.Contains(scan.Line, trailNeedle)` is true **iff** the needle's end offset is ≤ `reachMaxCommandBytes`. Prefer the check that **reads the retained copy** over the one that **re-implements the capping rule**: if `reachCapCommand` ever gains rune-boundary trimming, the offset arithmetic becomes a stale model of the cap while the containment check keeps reading the truth. The offset check's diagnostic value — telling a developer who repadded what went wrong — is preserved by naming the pad constant and the cap in the surviving message.

The failure must stay **loud** (`Fatalf`, before the build). A repad past the cap that silently passed is the exact "green proof of a false claim" this family refuses.

### §4 — AC3: the two checks the retirement does not reach

Each survivor's comment must say **why it survives a retirement its neighbours did not**. Neither may inherit its old argument unchanged.

**The pre-build clean check (`:728-747`).** Its current line — "Both are built from planted inputs; what is asserted is that the plant did not survive the sub-builder" — names the wrong sub-builder for the trailer half. Re-state: the attribution's sub-builder is `finAttributeFanOut`; the **trailer's is `finTrailerSighting`**, which is where the line is dropped — `finTrailerBuild` never sees it. Then the survival argument: the row's only leak channel is the retained `Line`, which is what §3's guard keeps live, and it is a **prospective** guard against a future `finTrailerSighting` or `finTrailerBuild` that started reading it. Do not claim a needle reaches this sub-record today; it does not.

**The pairing subtest's needle half (`:936-939`, argued at `:919-923`).** It survives on a **weaker property than the in-cap one**: `trailPaddedTrailer` splices `trailNeedle` into its `result` field at **every** pad, so the wire line the four published scalars sat beside carries the needle regardless of the cap. The claim is about the **wire** line, not the retained copy. Therefore:

- it needs **no** in-cap guard of its own and **must not acquire a copy of one** — a cap guard here would state a precondition its claim does not use, and would read as a second, redundant plant channel;
- its four scalar assertions are a live claim about **this** writer and stay exactly as they are;
- the comment says both: why the pairing is untouched by the retirement, and why the needle half is non-vacuous without a guard.

### §5 — AC4: the two kept symbols, re-stated

**Both are kept. The pad is NOT changed and the trailer input is NOT swapped.** Swapping to a needle-free fixture is worse than either option: `trailFixtureTrailer` renders `is_error` **false**, so the pairing's `is_error` assertion would compare false to false and a quarter of that contrast becomes theatre. `trailPaddedTrailer` is the all-non-zero fixture.

**`finWriteTrailerPad`'s doc (`:167-185`)** currently argues entirely for a plant position no test asserts any more ("the reason is the whole of AC2's trailer channel"). Re-state it to the position it now holds:

- the pad is 0 deliberately, and the in-cap position is what keeps **§3's guard and the pre-build clean check** live — name that consumer, not a plant-list channel;
- the past-the-cap trap sentence **stays**: a needle past the cap never reaches `Line`, so its absence downstream would prove nothing, and this family's usual pads (200, `trailOverlongPad`) would be the wrong plant here;
- the measured offsets stay, **with the measurement commit re-stated rather than carried forward untested**. Re-verified at `ac25ad8`: `len(trailNeedle)` = 42; pad 0 → 385 bytes, needle at bytes 104–146; pad 200 → 585 bytes, needle still in-cap but `terminal_reason` cut from the retained copy; the needle first crosses the 512-byte cap at pad **367**. Re-run the measurement and stamp the commit measured at;
- the disclaimer stays: the offsets are a property of the **pad**, not an invariant of the needle — which is why the surviving guard asserts it rather than trusting the comment. Re-point "AC2 asserts it" at §3's relocated guard;
- **the precedent cite is corrected** (§6);
- add the second reason the pad is not changed: `finGatherOverCapPad`'s doc pairs with it across files (`finding_run_gather_test.go:1494`, `:1521`) in a file this ticket may not edit, and AC4 keeps those references valid by keeping the constant.

**`finWritePlantedTrailerScan`'s doc (`:187-192`)** keeps the helper's name — the scanned bytes still carry the needle, which is what §4's pairing rests on, so "planted" and "the in-cap trailer plant" both read true. Re-state what the plant is **for**: not a channel of the directory sweep, but §3's guard and §4's pairing. Its existing "one construction site, so the fixture and AC2's precondition cannot disagree about which bytes were scanned" is re-pointed at the surviving guard — and is now doing more work than before, because the guard invokes the helper a **second** time (`finWriteInputs` invoked it first). Determinism over a shipped scanner is what makes the second invocation safe; say so.

### §6 — AC5: the sweep stays non-vacuous, the walk is frozen, two cites corrected

**The three remaining plants stay live and their preconditions stay:** each matched row's `Command` (`:675-681`), `ClaudeCommand` (`:682-685`), and the reap stderr with its `trailAdmitProof` premise (`:705-726`). Nothing in this ticket touches them beyond the channel renumbering.

**The headroom walk is left intact, byte-for-byte** (`:753-780`): the `len(details) < 6` floor, the per-path `room < len(trailNeedle)` test, `finWriteObservedDetails` and the `finWriteSorted` call that feeds them. #1325's shipped comment on `TestFinTrailerRecordCarriesNoCapturedBytes` (`finding_trailer_evidence_test.go:880-890`) names all of it as *this* test's behaviour, in a file this ticket may not edit. An edit here silently falsifies a merged sibling's argument. `len(trailNeedle)` is a **size** here, not a plant, so the retirement does not reach it.

**Cite 1 — `:941-945`, the "NOT RESTATED HERE" cite.** `TestFinTrailerRecordReadsTheDecodedTrailer` was **deleted outright** by #1325; the surviving row was promoted to top level. Correct to, **by symbol and with no line number**:

- symbol: `TestFinTrailerSightingScalarsComeFromTheFullLineDecode`;
- subject: it pins **`finTrailerSighting`** — the helper this fixture calls — rather than a record;
- fixture: `trailPaddedTrailer(2000)`, **not** "pad 200". Correct the parenthetical in the same edit;
- precondition: that `terminal_reason` is cut from the capped `Line`, asserted in code.

**Cite 2 — `:178-180`, `finWriteTrailerPad`'s precedent cite.** #1290's plant-position precedent at `finding_trailer_evidence_test.go:600-612` was not moved by #1325 — it was **reversed**. That location now reads "The pad is irrelevant here — the carrier holds no line for a needle to sit in", and its sibling at `:851-856` says the fixture is "NOT REPADDED past the cap" precisely because nothing reads the line. Citing it would point at prose arguing the opposite of the sentence it supports. Re-point to **`TestFinGatherReturnsNoCapturedBytes`** (`finding_run_gather_test.go`), the live in-code precedent, which asserts the in-cap position with the same reasoning at `:1138-1146`. (Dropping the precedent claim is the permitted alternative; re-pointing is better, because the in-cap position is unusual in this family and a reader needs the second instance.)

**The file header's forbidden-symbol rule survives the by-symbol preference.** `:35-59` references every exec-bearing helper **by file and line and never by name**, deliberately, so a forbidden-symbol grep reports on this file's CODE and cannot be defeated by its own prose. The ticket's "prefer citing by SYMBOL" instruction applies to the tests and fixtures this spec names; it does **not** license converting the header's file:line references into symbol names, and no prose this ticket writes may name `finGatherReadings`, `pinScanArgv`, `pinReadState` or any other exec-bearing helper. Naming `TestFinGatherReturnsNoCapturedBytes` — a test — is fine and is required.

### §7 — Explicitly not touched

- **`finWriteArtifacts`, `finWriteReadDir`, `finWriteDeclaredPaths`, `finWriteObservedPaths`, `finWriteObservedDetails`, `finWriteLeafKey`, `finWriteSorted`, `finWriteSafetyClaim`, `finWriteRender`, `finWritePlantedReapLog`** — no change.
- **`TestFinWriteArtifactRendersEveryDeclaredField`** (AC1's census) and **`TestFinWriteArtifactCarriesNoCapturedByteShapedKey`** (AC3's key scan) — no change.
- **The "# The mandated mutations, applied and observed" section (`:644-667`)** — M1 and M2 are historical records of mutations run against this test. Both remain true statements about what was observed; neither rests on the trailer channel. Leave them.
- **`finWriteInputs`' other sections** — "# Why each omitempty field is filled", "# The liveness outcome is hand-built", "# The certified reason is 'completed'". Unchanged.
- **Stale cross-file line cites other than AC5's two.** #1320 grew `finding_trailer_evidence_test.go` by ~250 lines and #1325 rewrote 387 more, so cites into it from this file point a few hundred lines off — including `:93` and `:632`'s `finding_trailer_evidence_test.go:142`. Out of scope, and **out of scope even where they sit inside a paragraph this ticket rewrites**: correct exactly the two AC5 names and leave every other cite byte-identical. `finWriteTrailerPad`'s cite of `result_trailer_observation_test.go:297-300` was re-checked and is still accurate — leave it.
- **The three sibling files.** #1324's and #1325's, both merged.
- **`docs/knowledge/codebase/1326.md`** — the documentation phase writes it from this spec plus the merged diff. Not a developer deliverable.

### §8 — One correction to the ticket body

AC5 says of its two cites that "for both the honest target is a symbol in a *different* file than the one cited". **That holds for cite 2 and not for cite 1.** Cite 1's stale target and its honest successor are both in `finding_trailer_evidence_test.go`: #1325 deleted the shell `TestFinTrailerRecordReadsTheDecodedTrailer` and promoted its surviving row **in place**, as `TestFinTrailerSightingScalarsComeFromTheFullLineDecode` (`:533`). The AC's own naming of the symbol, its pad (2000) and its precondition are all correct and all verified; only the "different file" gloss is wrong. Nothing else in the AC changes: the corrected cite still names the file it names today.

---

## Concurrency model

None. Every function touched is pure test code — no goroutine, no lock, no package state. Two inherited rules apply and must not be "simplified" away:

- **Fixtures are functions, never package-level vars** (`trail_run_outcome_test.go:608-610`): a shared backing value is reachable from every test in the package and `go test -race` runs them in parallel. §3's guard invokes `finWritePlantedTrailerScan()` a **second** time; hoisting it to a package-level var to avoid the second call is the tempting, wrong move.
- **`finWriteSorted` returns keys and never values**, so a failure that just tripped a leak check cannot write the leak into CI logs. No message this ticket writes may print a file's contents, a Detail's contents, or `scan.Line`.

## Error handling

No production error paths. The failure modes that matter are test-shaped:

- **A vacuous green** — the defect being removed: preconditions certifying a channel that runs one tier below the writer under test.
- **A silently disarmed guard** — a later repad past the cap turning the pre-build clean check into an assertion about a shape the type already forbids. §3's relocated `Fatalf` is the guard; loud, before the build.
- **A deleted regression guard** — a developer reading "retire the trailer plant" beside the pre-build clean check, or beside the headroom walk, and concluding they died with it. §4's re-stated arguments and §6's freeze are the mitigations, and they are why each survivor's comment must change rather than be left alone.
- **Message discipline**, unchanged and binding on every message this ticket edits: name `len(scan.Line)`, byte counts, the cap, the pad constant, closed-set values, JSON paths and file names — **never** render `scan.Line`, `scan`, `scan.Trailer`, a whole `resultTrailer`, a file's bytes or a Detail's string.

## Testing strategy

The deliverable *is* tests. Verification is that the surviving set still bites and the retired claim is genuinely held elsewhere.

- **Build tags.** `make check` and `make build` never compile `e2e_realclaude`-tagged files, so a PR whose whole diff sits under that tag is a **vacuous green**. Run both explicitly:
  - `go vet -tags e2e_realclaude ./internal/e2e/realclaude/...`
  - `go test -race -tags e2e_realclaude ./internal/e2e/realclaude/...`
  - Expect a **PASS/SKIP split**, not all-PASS. Both were green on `ac25ad8` before this ticket.
- **Targeted run while iterating:** `go test -race -tags e2e_realclaude -run '^TestFinWrite' -v ./internal/e2e/realclaude/` — the file header's own invocation. All four `TestFinWrite…` tests must pass.
- **Imports:** no drop is expected (`bytes`, `strings`, `time`, `reflect`, `sort`, `json`, `fmt`, `os`, `filepath`, `testing` all keep live uses after the edit). Verify with `go vet` rather than by inspection — `strings.Index` leaves the file with §3's deletion, and an unused local or import is a compile error, not a style note.
- **The one mutation worth running.** A subtractive-and-prose diff cannot be verified by a green run. Run this and record the observation in the PR:
  - repad the fixture past the cap (`finWriteTrailerPad` → 400, above the measured 367 threshold) and confirm §3's relocated guard fails **loudly** in `TestFinWriteArtifactsCarryNoCapturedBytes`, naming the pad and the cap. Revert.
  - Expected side effect while mutated: `TestFinWriteArtifactPublishesNoVerbatimModelOutput` stays **green**, because §4's pairing rests on the wire line rather than the retained copy. That asymmetry is AC3's claim, observed rather than argued.
  - Use `go test -overlay=<abs path to a JSON overlay in the scratchpad>` so the mutated copy never lands in the worktree.
- **Optional, cheap:** confirm the three surviving plant preconditions still bite by blanking one plant (drop the `" " + trailNeedle` suffix on `finRecordMatchedRows`) and observing the channel-1 `Fatalf`. Revert.
- **No `t.Skip`, no live claude, no credentials, no daemon, no turn.** Everything here is offline.

---

## Open questions

1. **The channel numbers are renumbered, not removed.** `docs/knowledge/codebase/1286.md:61` and this spec both refer to "Plant #3" historically; after renumbering, "channel 3" in the source means the reap stderr. §2 requires the retirement note to carry the old→new mapping explicitly so the historical references stay resolvable. If a future retirement hits this list again, dropping the numbers in favour of names is the better end state — but it is a refactor of surviving prose, not this ticket's mandate.
2. **`finTrailerSighting`'s agreement with `finGatherReadings`' fill remains held by comment and by review.** Unchanged by this ticket and not this ticket's to close; noted because §2's note names `finTrailerSighting` as the dropping step and a reader may reasonably ask what pins it.
3. **`:93` and `:632`'s cite of `finding_trailer_evidence_test.go:142`** (`finTrailerRecord` is "ten scalars with no Line") is stale by a few hundred lines after #1320 and #1325, and sits inside prose adjacent to this ticket's edits. §7 rules it out of scope deliberately — the claim it makes is still true, only its line number drifted, and the mechanical cite sweep across this family is a separate concern.

---

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries] No findings, and the ticket's whole subject is one.** The untrusted→published boundary is verbatim model output: `trailScanResult.Line` (~415 of the 512 retained bytes chosen by the model, marked OPERATOR-REVIEW-BEFORE-PASTE at `result_trailer_observation_test.go:100-107`) and `*resultTrailer`, which carries `PermissionDenials *[]json.RawMessage` behind a deref that panics by design. #1320 moved that boundary **one tier up** to the carrier fill; this spec moves no code across it, adds no field, and changes no type. What it does is delete a *claim* that the boundary is enforced at a tier where it is not, and relocate the one guard that keeps a live check non-vacuous.
- **[Trust boundaries] No findings — the deleted check is provably redundant, and its deletion strictly improves the boundary.** §3 removes the needle-offset-vs-cap check. `trailScan` records `Line: reachCapCommand(string(scanner.Bytes()))` (`result_trailer_observation_test.go:182`) and `reachCapCommand` returns the line unchanged at ≤ 512 bytes and `line[:512] + marker` above it (`background_reach_probe_test.go:945-949`), so `strings.Contains(scan.Line, trailNeedle)` is true **iff** the needle's end offset is ≤ `reachMaxCommandBytes` — verified across pads 0, 200, 366, 367 on `ac25ad8`. The surviving check **reads the retained copy**; the deleted one **re-implemented the capping rule**, and would have become a stale model of it under any future change to `reachCapCommand` (rune-boundary trimming being the realistic one). The guard is also `Fatalf` and runs before the build, so a repad can never produce an artifact at all.
- **[Tokens, secrets, credentials] No findings, and this is why the label is on the ticket.** The channel this family exists to keep shut is an operator's `CLAUDE_CODE_OAUTH_TOKEN` / `ANTHROPIC_API_KEY` reaching a publicly-pasted artifact through a verbatim argv or a ps column. Those are precisely the plants this ticket does **not** retire: each matched row's `Command`, `ClaudeCommand`, and pyry's captured reap stderr all keep their plants and their preconditions (§6). The retired channel carries **model output, not credentials**, and it is retired because the writer under test performs no reduction there — not because the exposure was judged acceptable.
- **[Tokens, secrets, credentials] SHOULD FIX — the channel renumbering is the one edit that can silently drop a credential-channel precondition.** §2 renumbers `channel 4 (reap stderr)` to `channel 3` inside three surviving `t.Fatalf` messages (`:705-726`), including the `len(in.Attribution.Entries) != 1 || first != trailAdmitProof` premise that is the *only* thing making the reap-stderr plant non-vacuous. A renumbering pass that touched the block wholesale could drop it and still compile and pass. Code-review must verify the three surviving preconditions by **content and count** — row `Command`, `ClaudeCommand`, reap-log containment **plus** the `trailAdmitProof` premise — not merely that the test is green.
- **[Subprocess / external command execution] No findings, and the enumeration is the check rather than the grep — but the file's grep-integrity rule is a live constraint on this ticket.** Every symbol on the touched path is pure over bytes: `trailScan`, `trailPaddedTrailer`, `reachCapCommand`, `trailDetail`, `finTrailerSighting`, `finTrailerBuild`, `finAttributeFanOut`, `trailReapLine`, `finRecordMatchedRows`, `finRecordBuild`, `json.Marshal`, plus `os.ReadDir` / `os.ReadFile` / `os.WriteFile` under `t.TempDir()`, which the file header explicitly rules "expected and not an exec". The package's exec-bearing helpers — `pinReadState` (execs `ps`), `pinScanArgv`, `probeProcessSnapshot`, `tdnScan`, `holdProbeFIFO`, `WithWorktreeAuthenticated`, the version probe and the claude-binary resolver — are on no path this ticket touches. A grep for `exec.` reads clean here **for the wrong reason** (every route off the offline path runs through a helper that execs *inside*), which is exactly why `:35-59` references each one by file:line and never by name. §6 forbids this ticket's prose from naming any of them, so the forbidden-symbol grep keeps reporting on CODE. This is the one way a comments-only diff could break a shipped security control.
- **[Error messages, logs, telemetry] SHOULD FIX — the relocated guard must not transplant its predecessor's message.** The retired offset check named `len(line)` — the length of the **uncapped** rendered line — and a developer moving the block wholesale would carry that variable along with a local this spec deletes. §3's message names `len(scan.Line)`, `reachMaxCommandBytes` and `finWriteTrailerPad`, and **never** renders `scan.Line`, `scan` or `scan.Trailer`. The standing rule for every message this ticket edits: name lengths, counts, byte offsets, the cap, closed-set values, JSON paths and file names; never a file's bytes, a Detail's string, or a captured line. `finding_run_gather_test.go:1141-1146` is the shipped model. `finWriteSorted`'s keys-never-values doctrine (`:519-527`) is untouched and must stay that way — it is what stops a failed leak check from writing the leak into CI logs.
- **[Error messages, logs, telemetry] SHOULD FIX — the re-stated arguments must stay in the prospective register.** §4 requires the pre-build clean check to say its leak channel is the retained `Line`. A re-statement reading "a needle that leaked through the trailer sub-builder would be caught here" is **false today** — `finTrailerSighting` reads no line — and would be a green-looking claim about a channel that does not exist: the exact defect this ticket removes at the plant list. The honest register is the one #1325 already uses for its key scan: a guard against a **future** field or a future read. Code-review must check the shipped wording, not just its presence.
- **[File operations] Not applicable by design decision.** No path is constructed, opened or written by anything this ticket touches. `finWriteArtifacts` (`0o600` into a `t.TempDir()`) and `finWriteReadDir` (`os.ReadDir` over every regular file, rather than the two names the writer wrote — the property that catches a `reach.ps.txt`-shaped third file) are both in §7's untouched list.
- **[Concurrency] No findings; one tempting wrong move named.** Everything touched is pure test code with no goroutine, lock or package state. §3's guard invokes `finWritePlantedTrailerScan()` a **second** time; hoisting it to a package-level var to "avoid the duplicate scan" would violate the family's function-not-var fixture rule (`trail_run_outcome_test.go:608-610`), whose reason is that `go test -race` runs this package's tests in parallel and a shared backing value is reachable from all of them. The two scans allocate independent `*resultTrailer` values; there is nothing shared to race on as specified.
- **[Cryptographic primitives] Not applicable.** No randomness, hashing, key material or comparison against a secret anywhere in the touched surface. Go's randomized map iteration is defeated by `finWriteSorted` for message stability, and that helper is untouched.
- **[Network & I/O] Not applicable.** No socket and no reader. The one cap in reach — `reachMaxCommandBytes` = 512, single-sourced at `background_reach_probe_test.go:123` — is asserted *about* and never changed. §5 requires the fixture offsets to be **re-measured and the commit re-stamped** rather than inherited from `4bc5f5b`, which is what keeps the fixture's relationship to that cap an observation instead of a memory.
- **[Threat model alignment] Aligned, and the retirement is of a claim rather than of a check.** The standing threat is "an artifact published unreviewed carries bytes an operator would have had to review". Surviving intact: the three credential-bearing plants, the directory-wide sweep over every file `os.ReadDir` returns, the headroom walk that stops a lengthened Detail from truncating a leak away (frozen byte-for-byte by §6, because a merged sibling's shipped comment argues from it), the recursive forbidden-key scan, and the pre-build clean check on the trailer sub-record with its guard relocated rather than dropped. The retired claim is shown by symbol to hold at `TestFinGatherReturnsNoCapturedBytes`, which asserts the **in-cap** precondition in code over the carrier the gather actually returns — a position this tier could never hold, since its input structurally cannot carry the needle. No coverage lapses.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-08-05
