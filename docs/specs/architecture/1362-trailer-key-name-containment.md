# #1362 — Probe instrument: prove end to end that no value from the trailer line reaches the artifact through the published key names (offline)

**Size:** S (confirmed; see § 8 for the measurement)
**Package:** `internal/e2e/realclaude` (build tag `e2e_realclaude`)
**Files touched:** one new `_test.go`, one 5-line line-count-neutral comment repair. Zero production source files.

---

## 1. Files to read first

Turn-1 data load. Read these before writing anything; every design decision below rests on one of them.

| Path | What to extract |
|---|---|
| `internal/e2e/realclaude/finding_artifact_write_test.go:161-193` | `finWriteArtifacts` — the writer under test: what `run.json` and `run.md` contain, and that the note embeds the marshalled record in a fence |
| `:329-374` | `finWriteInputs` (the maximal input set, three live `trailNeedle` plants) and `finWriteRender` (build → write → read the directory back). AC1/AC4 substitute **only** `.Trailer` on the former |
| `:274-296` | **The plant-only-where-the-pipeline-reduces rule.** The four positions AC1's plant list must exclude, and why |
| `:387-409` | `finWriteReadDir` — reads every regular file, fatals on an unreadable one and on an empty directory |
| `:557-607` | `finWriteObservedDetails` (every Detail by JSON path) and `finWriteSorted` (**keys, never values** — the no-leak-into-CI-logs rule AC1 and AC4 both inherit) |
| `:778-932` | `TestFinWriteArtifactsCarryNoCapturedBytes` — the shape AC1 mirrors: per-channel non-vacuity Fatalfs, a guard, a per-path Detail headroom walk, then the directory sweep |
| `:1042-1095` | The shipped in-cap `trailNeedle` row (`:1084-1087`) and its **MUST NOT ACQUIRE A COPY OF ONE** comment. AC1 extends around it; it is not edited |
| `internal/e2e/realclaude/finding_trailer_evidence_test.go:189-231` | `finTrailerRecord`'s Detail content rule, the key-name prohibition at `:198-204` (**the repair site**), and the field set |
| `:303-339` | `finTrailerBuild` — the true arm copies `KeyNames` by plain assignment; the false arm zeroes it. M5's and M6's mutation points |
| `:391-402` | `finTrailerSighting` — the fixture-side carrier fill; `finBoundKeyNames(scan.KeyNames)` at `:399` is M1/M2's mutation point |
| `:955-985` | The **per-row Detail headroom check**. `512 - 424 = 88 ≥ 42`, so a names-interpolating Detail passes it — the measurement AC4 exists because of |
| `internal/e2e/realclaude/finding_key_name_bounds_test.go:45-63` | The 64 KiB `bufio.Scanner` ceiling hazard: past it the scan **aborts**, `KeyNames` is nil, and every assertion below goes vacuously green |
| `:153-188` | `finPublishedKeyNames(t, line) (read, published []string)` — **AC2 reuses this verbatim**; it already carries the ceiling Fatalf |
| `:101-116` | `finOverlongKeyNameTrailer` — the splice-onto-a-shipped-renderer pattern AC4's fixture copies |
| `internal/e2e/realclaude/trailer_key_names_test.go:87-106` | `trailKeyNames` — M1's mutation point (`append(names, name)` → the raw value) |
| `:129-156` | `trailKeyNamesNeedles()` (five distinct needles, 52–56 B) and `trailKeyNamesNeedledTrailer(pad)` — **AC2's fixture, reused not re-rendered** |
| `:293-356` | `TestTrailKeyNamesCarryNoValues` — the reader-tier check AC2 must be distinct from, and its pad-600 argument AC2 deliberately inverts |
| `:400-435` | `TestTrailScanResultReachesNoRawMessageMap` — **AC3's exact shape**, ban plus control |
| `internal/e2e/realclaude/finding_run_record_test.go:720-752` | `finRecordInputReaches` — the walker AC3 calls (struct fields, map keys *and* values, slices, arrays, pointers) |
| `internal/e2e/realclaude/finding_run_gather_test.go:386-471` | `finTrailerMaxKeyNames = 32`, `finTrailerMaxKeyNameBytes = 64` (`:418-421`) and `finBoundKeyNames`' five clauses |
| `:2043-2059` | `TestFinSightingReachesNoScanType` — the shipped precedent for AC3's ban |
| `internal/e2e/realclaude/result_trailer_observation_test.go:180-233` | `trailScan` — `KeyNames` is read from `scanner.Bytes()` (the **full** line), `Line` from `reachCapCommand` of it |
| `:303-338` | `trailFixtureTrailer` (AC4's base), `trailNeedle` (42 B) and `trailPaddedTrailer` — note `session_id` is hard-coded to a UUID at `:334`, which is why AC1 needs its own renderer |
| `internal/e2e/realclaude/background_reach_probe_test.go:117-124`, `:945-951` | `reachMaxCommandBytes = 512`, `reachTruncationMarker` (29 B), `reachCapCommand` |

---

## 2. Context

#1363 published the trailer's top-level **key names** on `finSighting` and `finTrailerRecord`, bounded by `finBoundKeyNames`, reaching the written artifact through `finRecordRun.Trailer`. That field is the first string on the record that **claude authored**. #1364 proved the bounds bite. `TestTrailKeyNamesCarryNoValues` proves no value reaches the names *at the reader's own return*.

Nothing yet checks that the two builders between the reader and the file, and the writer after them, kept it that way **in the rendered files**. This ticket closes that gap with four checks, no one of which subsumes another.

The mis-implementation this is shaped against: a reader or builder that carried the decoded map's **values** rather than its key names would put the model's whole `result` text into a file an operator pastes into a public issue.

### The two traps that would make this ship green over a leak

**Trap 1 — the plant list.** The artifact carries four of the trailer's own values verbatim **by design**: `subtype`, `is_error`, `terminal_reason`, `stop_reason` (`finding_artifact_write_test.go:274-289` states the rule). A single artifact-wide sweep that planted in every value position would fail a **correct** build. So AC1's plant list excludes those four and AC2 — which plants in all five string-valued positions — sweeps the **names field alone**. A containment check is identified by the pair *(where the needle is planted, which field is swept)*; these two differ in both halves.

**Trap 2 — truncation.** Both of #1363's bounds truncate, and `reachCapCommand` caps the retained line at 512 B. A needle truncated away before it is looked for turns a sweep green over a record that leaked — the defect #1284 shipped and had to fix. Every fixture below therefore asserts its non-vacuity as a **precondition in code**, never as a comment.

### The mutant matrix (from the ticket body, unchanged)

| # | mis-implementation | caught by |
|---|---|---|
| M1 | a reader/builder carrying the decoded map's **values** into the names field | **AC1** (`session_id`'s value is the needle whole → survives the 64 B bound) |
| M2 | the same, itself bounded to 64 B per entry | **AC1** (`result`'s truncates to padding; `session_id`'s does not — why both plants are needed) |
| M3 | a record re-admitting the **capped** `trailScanResult.Line` | shipped, `finding_artifact_write_test.go:1084-1087` — needs an **in-cap** needle |
| M4 | a record re-admitting the **full, uncapped** line | **AC1** — needs a **past-the-cap** needle; the shipped in-cap row is blind to it |
| M5 | a builder copying a value in from one of the four **published** positions | **AC2** (AC1 is green here by design) |
| M6 | a `Detail` interpolating the key **names** | **AC4** (the mutant Detail is 424 B with 88 B of room → the shipped headroom check passes it) |
| M7 | a `Detail` interpolating the key **count** | **nothing** — no instrument exists; out of scope, said plainly at the row |
| M8 | widening a carrier to reach `map[string]json.RawMessage` | **AC3** — a type-level property no fixture can exercise |

M3 and M4 need opposite pad positions. That is why AC1 is a **sibling** of the shipped row and never a precondition bolted onto it.

---

## 3. Design

One new file: **`internal/e2e/realclaude/finding_key_name_containment_test.go`**, build tag `e2e_realclaude`, package `realclaude`.

Test-name prefix `TestFinContain`, so the file's own selector is one alternation:

```
go test -race -tags e2e_realclaude -run '^TestFinContain' -v ./internal/e2e/realclaude/
```

### 3.1 Shared drive helper (AC1 + AC4)

```go
func finContainRender(t *testing.T, line string) (files map[string][]byte, read []string)
```

Contract:

1. `scan := trailScan([]byte(line + "\n"))`. **Fatal** unless `scan.State == trailSeen && scan.Trailer != nil`, reporting `len(line)`, `scan.State` and `scan.Detail` — **never `scan.Line`**. This is the 64 KiB ceiling defence in `finPublishedKeyNames`' words: past the ceiling the scan aborts, the names field renders null, and every assertion downstream is vacuously green.
2. `in := finWriteInputs()`; replace **only** `in.Trailer` with `finTrailerBuild(trailOutcomeVoidBudgetFired, finTrailerSighting(scan, 250*time.Millisecond, trailBoundFromMiss))` — the same outcome, staleness and bound the shipped fixture uses, so nothing but the trailer line differs from `finWriteRender`'s input.
3. `dir := t.TempDir()`; `finWriteArtifacts(t, dir, finRecordBuild(in))`; return `finWriteReadDir(t, dir)` and **`scan.KeyNames` alone**.

Why a helper rather than two copies: AC1 and AC4 render the same pipeline over different fixture lines, and one construction site is what keeps them from disagreeing about which hands ran.

**Why the second return is `[]string` and never the `trailScanResult`.** Handing the scan back would promote `.Line` — up to 512 bytes of model-chosen text, marked OPERATOR-REVIEW-BEFORE-PASTE at `result_trailer_observation_test.go:100-107` — and the `*resultTrailer` into the reach of every test in this file, where a later `%+v` in a failure message prints it. That is precisely the reach `finGatherReadings` refuses to hand back (`finding_run_gather_test.go:483-489`) and that `finSighting` exists to sever. A prose rule saying "never print `scan.Line`" is discipline where the family's own doctrine is shape: *prefer the shape that cannot be got wrong over the discipline that must not be* (`finding_artifact_write_test.go:28-32`). Both callers' preconditions are about the names the reader read; neither needs `.Line`, `.Trailer` or `.Detail`, so the narrow return costs nothing and closes the door structurally.

Why the other three `finWriteInputs` plants stay live: they are `trailNeedle` in a matched row's argv, in `ClaudeCommand` and in the reap stderr, all of which the pipeline reduces or drops — `TestFinWriteArtifactsCarryNoCapturedBytes` asserts they reach no file. Keeping them makes AC1's sweep strictly stronger at zero cost.

**Failure-message rule for the whole file** (inherited from `finWriteSorted`'s doc and security-review item [7]): name the **file**, the **JSON path**, a **byte length**, an **offset** or a **count**. Never the file's contents, never a Detail's string, never `scan.Line`, never a needle-bearing value.

### 3.2 AC1 — the artifact-wide value sweep

**Fixture:** `finContainValuePlantedTrailer(pad int) string`.

Renders `trailPaddedTrailer`'s wire order and its four published values (`subtype: error_max_turns`, `is_error: true`, `stop_reason: end_turn`, `terminal_reason: max_turns`) unchanged, with two differences:

- `result` = `pad` bytes of padding then `trailNeedle` (as `trailPaddedTrailer` does)
- `session_id` = `trailNeedle` **whole** (`trailPaddedTrailer` hard-codes a UUID at `result_trailer_observation_test.go:334`, which is why a separate renderer is required rather than a repad)

Keeping the four published values identical to the shipped fixture is deliberate: nothing this ticket adds may drift the values `TestFinWriteArtifactPublishesNoVerbatimModelOutput` pins.

`session_id` sits **after** `result` on the wire, so substituting it does not move `result`'s needle; the shipped measurement at `finding_artifact_write_test.go:215-218` (pad ≥ 367 puts the needle past the 512-byte cap) still holds. Call it with **pad 600** — comfortably past, and ~1 KB of line, four orders of magnitude below the scanner ceiling.

**The plant list carries its own argument.** The fixture's doc comment is the plant list, and per the AC it must state both:

- *why the four published positions are excluded* — the record carries them verbatim by design (`finding_artifact_write_test.go:274-289`), so a needle in any of them appears in the artifact **correctly**, and a sweep planting there is red against a correct build;
- *what the list does catch* — a reader or builder that carried the decoded map's **values** rather than its key names (M1/M2), and a record re-admitting the **full, uncapped** line (M4).

**Test:** `TestFinContainArtifactCarriesNoTrailerValue`. Ordered scenarios:

- **Offset precondition A (M4).** The first occurrence of `trailNeedle` in the rendered line — the one inside `result` — sits at an index **greater than `reachMaxCommandBytes`**. Fatal otherwise, naming the offset and the line length: an in-cap plant here would duplicate the shipped row at `:1084-1087` instead of complementing it.
- **Offset precondition B (M2).** Locate `session_id`'s value start (the index just past the literal `"session_id":"`) and assert `trailNeedle` begins exactly there **and** `len(trailNeedle) <= finTrailerMaxKeyNameBytes`. Fatal otherwise: a needle further into its own value than the per-name bound would be truncated away by a values-carrying mis-implementation and this check would pass over M2.
- **Render** via `finContainRender`.
- **Non-vacuity of the field under sweep.** The reader's names — `finContainRender`'s second return — equal `trailExpectedKeyNames()`: eleven names, both bounds inert, nothing truncated. Fatal otherwise: with no names published there is nothing for a values-carrying build to have leaked into.
- **Non-vacuity of the directory.** Both `finWriteRecordFile` and `finWriteNoteFile` are present in the returned map. Fatal otherwise, naming `finWriteSorted(files)`: the AC's claim is `run.md` **included** and not `run.json` alone.
- **Detail headroom, per path.** Walk `files[finWriteRecordFile]` with `finWriteObservedDetails`; require at least the shipped floor of six; for each path assert `reachMaxCommandBytes - len(detail) >= len(trailNeedle)`. This is **not** a duplicate of the shipped walk: that one runs over `finWriteInputs`' own trailer fixture and never over this one, and #1284's defect is precisely a sweep going green because a Detail ate its own budget. Report path and byte length only.
- **The sweep.** For each `finWriteSorted(files)`, `bytes.Contains(files[name], []byte(trailNeedle))` → `t.Errorf` naming the file and the directory listing. The message states what one occurrence means: the needle sits in `result` past the cap and **as** `session_id`'s whole value, both of which the pipeline reduces to nothing, so a hit means a value crossed into the names field or a line was re-admitted whole.

### 3.3 AC2 — the names-field-only sweep

**Fixture:** shipped, reused unchanged — `trailKeyNamesNeedledTrailer(pad)` with **pad 0**, and its five distinct needles from `trailKeyNamesNeedles()`.

**The pad choice is the deliberate inversion of the shipped one, and must carry its argument.** `TestTrailKeyNamesCarryNoValues` uses pad 600 and asserts `result`'s needle lands *past* the 512-byte cap, because its claim is about which **copy** of the line the reader read. This check's claim is about which **bytes** a builder copied into a bounded field, so the cap is irrelevant and the per-name bound is everything: at pad 600 a values-carrying builder would truncate `result`'s value at 64 bytes of padding and the needle would never be looked at. Pad 0 puts every needle within the first 64 bytes of its own value. A reader meeting pad 0 here after pad 600 there must find the reason at the call site, in the shape `finWriteTrailerPad`'s comment uses (`finding_artifact_write_test.go:197-229`).

**Drive:** `finPublishedKeyNames(t, line)` — shipped in `finding_key_name_bounds_test.go:174`. It runs `trailScan` → `finTrailerSighting` → `finTrailerBuild` (the two builders the AC names), returns `(read, published)`, and already carries the ceiling Fatalf. Reusing it rather than re-driving the chain is what keeps this check about the copy and not about the scan.

**Test:** `TestFinContainPublishedNamesCarryNoValue`. Scenarios:

- **The needle set is five and distinct.** Fatal otherwise, in `TestTrailKeyNamesCarryNoValues:311-323`'s shape: a shared needle cannot say *which* position leaked.
- **Per-needle detectability precondition.** For each of the five, find its value's start in the rendered line and assert `offsetWithinValue + len(needle) <= finTrailerMaxKeyNameBytes`. Fatal otherwise, naming the field and both numbers. This is the pad-0 argument asserted rather than trusted.
- **Non-vacuity.** `read` equals `trailExpectedKeyNames()` (eleven names, the reader saw the whole line) **and** `published` equals `read` (both bounds inert, so the sweep looks at the whole set and not a prefix). Fatal otherwise.
- **The sweep.** For each published name × each needle, `strings.Contains(name, needle)` → `t.Errorf` naming the **field whose value leaked** (the needle map's key) and never the needle or the name. The message states what AC1 cannot say: four of these five positions are published verbatim by design, so this is the only check that can see a builder copying one of them into the names field (M5).

### 3.4 AC3 — the structural ban and its control

**Test:** `TestFinContainCarriersReachNoRawMessageMap`. No fixture, no scan, no artifact — a `reflect` walk, in `TestTrailScanResultReachesNoRawMessageMap`'s shape.

- For each carrier in `{finSighting{}, finTrailerRecord{}}`: `finRecordInputReaches(carrier, reflect.TypeOf(map[string]json.RawMessage{}), map[reflect.Type]bool{})` is **false**. The failure message states the hazard concretely: `json.RawMessage` values are the raw bytes of the line, and this package already formats a whole trailer sub-record with `%+v` (`finding_run_record_test.go:775`), so a map reachable from either carrier prints the assistant payload into a failure message.
- **The control**, per carrier: the same walk **does** reach `reflect.TypeOf([]string{})`. Both carriers reach only scalars and this one slice today, so without the control the ban is green against a walk that finds nothing at all. The comment names the field — `KeyNames` — as the sole route, so a later editor adding a second `[]string` knows the comment, not the assertion, is what needs revising.
- Ban and control are asserted **per carrier**, not once over the pair. Sweeping a rule over one input and calling it covered is the shape that let a two-parameter contract be checked 3× on one parameter and 0× on the other.

The walk is transitive (`finding_run_record_test.go:743-749` descends into map keys *and* values), so "no type transitively holding one" is proved rather than aspirational.

### 3.5 AC4 — the Detail prohibition, discharged

**Fixture:** `finContainNeedleKeyName() string` and `finContainNeedleKeyTrailer() string`.

- The name is a needle constant **distinct from `trailNeedle` and from all five of `trailKeyNamesNeedles()`**, and the distinctness is load-bearing rather than tidy: this needle reaches `trailer_keys` **legitimately**, so if it shared a constant with a value-position plant, AC1's sweep could no longer tell a value leak from the by-design name carriage and would be red against a correct build. It leads with ASCII uppercase so it sorts **first** of the set (`trailKeyNames` sorts, and uppercase precedes lowercase) — so even a Detail truncating its name list would still carry it, and the count bound's alphabetic-prefix cut can never reach it. Size it so it is under `finTrailerMaxKeyNameBytes` and assert that in code rather than pinning a literal length.
- The trailer splices that name on as **one extra top-level key** onto `trailFixtureTrailer`, in `finOverlongKeyNameTrailer:113-116`'s shape (`strings.TrimSuffix(base, "}") + ,"<name>":"v"}`). Splicing onto a shipped renderer is what makes the other eleven names the real envelope names. `trailFixtureTrailer` rather than `trailPaddedTrailer(0)` deliberately: it carries no `trailNeedle`, so a red here names this plant and nothing else.

**Test:** `TestFinContainNoDetailNamesAKeyName`. Both halves asserted, per the AC.

- **Render** via `finContainRender`.
- **Non-vacuity, half one — the reader.** The reader's names — `finContainRender`'s second return — hold twelve entries, all within both bounds, with the needle among them **whole** (exact element equality, not a substring). Fatal otherwise: the reader must have read the thing the sweep looks for, unbounded and untruncated.
- **Non-vacuity, half two — the artifact.** Decode `trailer_keys` off `files[finWriteRecordFile]` (an anonymous struct in `TestFinWriteArtifactPublishesNoVerbatimModelOutput:1045-1052`'s shape, reading the artifact's own keys as an operator would) and assert the needle is present and is **element 0**. Fatal otherwise — this is the AC's stated precondition, and reading it off the written file rather than the in-memory record is the difference between measuring the artifact and measuring the record.
- **The claim.** `finWriteObservedDetails(files[finWriteRecordFile], details)`; require at least six (the shipped floor — a walk that found fewer stopped descending and the claim would hold over a subset); for each path, `strings.Contains(details[path], needle)` → `t.Errorf` naming **the JSON path and the byte length only**, per that helper's own rule and `finWriteSorted`'s.
- **The count half, said at the row.** A comment at this test states plainly that the prohibition's *count* half (M7) has **no instrument** and stays unproven: `finTrailerMaxKeyNames` is 32, so interpolating the count costs two digits, no byte budget can be made to exceed, no needle can be a count, and no `finTrailerRecord.Detail` is pinned by exact equality anywhere. It is not implied to be discharged.

**Scope note:** the Detail walk runs over `run.json` alone. `run.md` embeds the same marshalled bytes inside a fence, so its Details are byte-identical, and `finWriteObservedDetails` takes a `json.RawMessage`. AC1's sweep is the one that covers `run.md`.

### 3.6 The comment repair

`internal/e2e/realclaude/finding_trailer_evidence_test.go:200-204` claims the prohibition is "made red by a hostile-name fixture in #1364". #1364 measured that fixture at 444 B against a 470 B budget — 27 B short of red — cut it, and handed the obligation here. The sentence is now false.

**Strictly line-count-neutral: five comment lines replaced by five.** `:193` and `:198` are untouched. The site sits below an inbound named cite at `:193` and above nine more at `:309-315`, `:391`, `:413`, `:550`, `:693`, `:723-737`, `:766-772`, `:768` and `:930`; any net line change breaks all nine.

Replace lines 200–204 with exactly:

```go
// no COUNT of them, both being derived from the line. The shipped row below cannot
// detect either: its fixture's eleven short names fit inside the headroom even if
// the Detail interpolated them. The NAMES half is made red by #1362's needle sweep,
// which plants a needle AS a top-level key name and asserts no Detail in the
// artifact carries it. The COUNT half has no instrument and stays unproven.
```

After the diff, re-run the stateful cite scan (carrying the last-named `.go` file forward through each comment block, so bare `(:NNN)` and symbol-anchored `<Type>:NNN` forms resolve) and confirm each pointer landing past an insertion point still names what it named. The new file adds no line to any existing file, so this should confirm rather than repair — but confirm it.

---

## 4. What this ticket does not touch

- **`finding_artifact_write_test.go:1084-1087`**, the shipped in-cap `trailNeedle` row. Its comment says it "needs no in-cap guard of its own and MUST NOT ACQUIRE A COPY OF ONE", and per M3/M4 it is the only row that catches a **capped**-line re-admission. AC1 is a sibling with its own fixture and its own preconditions, never a precondition bolted onto that row.
- **`finWriteTrailerPad = 0`** and `finWritePlantedTrailerScan`. AC1's fixture is a separate renderer, not a repad; the guard at `:854-866` fails if that constant moves, and `finGatherOverCapPad`'s doc pairs with it across files.
- **`TestFinWriteArtifactsCarryNoCapturedBytes`' plant list.** #1326 retired #1286's trailer entry on the argument that the names are computed at the scan and what enters `finRecordInputs` is a carrier holding already-reduced names. That argument survives this ticket unchanged. Everything here is a **new** end-to-end check, never a fourth channel appended to that list.
- **`finding_run_gather_test.go:581`**, the live-fill call site of `finBoundKeyNames`. Driven by no offline test (#1364's code review, NIT 3). All four checks here drive the fixture side through `finTrailerSighting`.
- **The fence escape.** `run.md` embeds the record in a ```` ```json ```` fence and `json.MarshalIndent` escapes control characters and quotes but **not backticks**, so a model-influenced string carrying a fence-closing sequence escapes the fence when the file is pasted. The ticket body attributes this to `stop_reason` alone; that understates it. Since #1363 the key-name field is a **second carrier** — a hostile top-level key name reaches `trailer_keys` inside the same fence, and #1363's 64-byte per-name bound does not help, three backticks being well under it. Neither carrier is created or widened by this ticket, which adds checks and no new field; closing it for one door while the other stays open is not a fix, and there is **no ticket filed** for it. Named here so the next author of this area does not read the body's framing as the whole exposure.
- **`docs/knowledge/codebase/1362.md`.** Owned by the documentation phase, written after the PR merges. Not a developer deliverable.

---

## 5. Concurrency model

None. Every function added is pure over its inputs or writes only under `t.TempDir()`. No goroutine, no clock, no exec, no network, no daemon, no env gate, no `t.Skip`.

The one concurrency constraint that binds: `go test -race` runs this package's tests in parallel, so **every fixture is a function, never a package-level `var`** (`trail_run_outcome_test.go:608-610`). A shared backing array is reachable from every test in the package. This applies to `finContainValuePlantedTrailer`, `finContainNeedleKeyTrailer` and any needle set returned as a slice or map. Constants (`string`, `int`) are fine.

`finContainRender` calls `trailScan` once per invocation and returns that scan's `KeyNames` rather than letting each caller re-scan — deterministic over the same bytes either way, but one construction site is what keeps a precondition and the thing it guards from disagreeing. The returned slice aliases a scan result that is function-local, freshly built on every call and mutated by nothing, so it is not a shared backing array in the sense `finBoundKeyNames`' clause 5 forbids; the published copy's independence is the producer's clause and is unaffected here.

---

## 6. Error handling

This file's two directions of failure are the ones `finding_artifact_write_test.go:61-70` states, and they are asymmetric on purpose:

- **The writer never fails a test.** `finWriteArtifacts` is the instrument under measurement; a marshal error is `t.Errorf` and the remaining write still happens. Unchanged here.
- **This file's own reads fail loudly.** `finWriteReadDir` and every decode are `t.Fatalf`. A sweep that silently skipped a file it could not read would report clean on the one file that leaked.

Every non-vacuity precondition is **`t.Fatalf`, before the thing it guards**, never `t.Errorf`. A precondition reported as an error lets the vacuous sweep below it run and print a green-looking result beside the failure.

Leak-safe reporting, everywhere: file name, JSON path, byte length, offset, count, field name. Never the file's contents, a Detail's string, `scan.Line`, or a needle-bearing value. `finWriteSorted` returns keys and never values precisely for this.

---

## 7. Testing strategy

`make check` is **not** the gate — it never compiles this diff. Every file here carries `//go:build e2e_realclaude`, and `make check`'s untagged `go vet ./...` / `go test -race ./...` / `staticcheck ./...` skip it entirely. A green standard gate says nothing about this work (observed on PR #1359).

**The gate:**

```
go vet -tags e2e_realclaude ./internal/e2e/realclaude/...
go test -race -tags e2e_realclaude -run '^TestFin|^TestTrail' ./internal/e2e/realclaude/
gofmt -l internal/e2e/realclaude/finding_key_name_containment_test.go internal/e2e/realclaude/finding_trailer_evidence_test.go
```

Baseline on `473d40d`: **75 PASS, 0 SKIP**, vet clean. Expect 79 PASS, 0 SKIP after this ticket. These tests are offline, carry no env gate and no `t.Skip`, and **run rather than skip without credentials** — this is not a `needs-real-claude` ticket, the same shape #1357 and #1364 shipped.

**Mandated mutations — applied, observed, reverted.** Run them with `go test -overlay=<abs-path>.json` over scratchpad copies so the worktree is never written to. Record the observed result for each in the PR body, in #1364's table shape.

| mutation | one-line change | expected |
|---|---|---|
| **M1/M2** | `trailKeyNames` (`trailer_key_names_test.go:102`) appends `string(keyed[name])` instead of `name` | **AC1 RED**, naming both `run.json` and `run.md` — `session_id`'s value is the needle whole, under the 64 B bound. **AC2 RED** on all five positions. Also demonstrates M2: `result`'s value truncates to padding with no needle in it while `session_id`'s survives, which is why the plant list needs both |
| **M5** | `finTrailerSighting:399` → `finBoundKeyNames(append(slices.Clone(scan.KeyNames), scan.Trailer.StopReason))` | **AC2 RED** naming `stop_reason`; **AC1 GREEN** — the row that proves AC2 is not subsumed by AC1 |
| **M6** | `finTrailerBuild`'s true arm interpolates `sighting.KeyNames` **inside** the `trailDetail` format string (never appended after it returns — appended, the cap has already run and the result proves nothing about #1284's defect) | **AC4 RED**; the shipped per-row headroom check at `finding_trailer_evidence_test.go:977` **GREEN** (424 B, 88 B of room against the 42 B yardstick). That pairing is AC4's whole reason to be a needle sweep rather than a second headroom row |
| **M8** | add a `map[string]json.RawMessage` field to `finSighting` | **AC3 RED** on the `finSighting` carrier; the control stays green |

**M4 is not mutation-demonstrated**, and that is stated rather than left to be noticed: no shipped field can hold the full uncapped line without a type change, so the mutant is a schema edit rather than a one-liner. AC1's past-the-cap plant is the standing guard for it, and it is the reason the shipped in-cap row at `:1084-1087` is left exactly where it sits — the two need opposite pad positions and each is blind to the other's mutant.

**Non-vacuity of the vacuity guards.** Before shipping, confirm each Fatalf precondition can actually fire: flip the pad on AC1's fixture below 367 and observe offset precondition A fail; set AC2's pad to 600 and observe the per-needle detectability precondition fail. Both reverted.

---

## 8. Size — the measurement

Sized **S**, and the arithmetic is recorded so review can check it rather than take it.

Red lines: 1 new file (≤3 ✓), 0 new exported types (≤5 ✓), 4 ACs (≤5 ✓), no state machine and no reject branches ✓, and **1 consumer edit** — the 5-line neutral comment repair (≤10 ✓). `codegraph_impact` is not needed for a fan-out of one.

The LOC line is the only one in play. Two independent measurements, both from measured blocks in this package rather than bottom-up:

- **Block sum.** header ~70 (#1364's is 71) + AC1 ~165 (its analogue, `TestFinWriteArtifactsCarryNoCapturedBytes`, is 237, with fewer channels here) + AC2 ~80 (analogue `TestTrailKeyNamesCarryNoValues` is 64) + AC3 ~65 (analogue `TestTrailScanResultReachesNoRawMessageMap` is 36, ×2 carriers + controls) + AC4 ~95 (analogue `TestFinWriteArtifactPublishesNoVerbatimModelOutput` is 103) + fixtures and drive helper ~110 = **~585**.
- **Nearest whole-file analogue plus delta.** #1364 shipped 382 in one new file with four tests, one clean review pass. This is the same shape with two of its four tests moved up to the artifact tier (+125 and +55 over a record-tier test), a render drive helper (+40) and the repair (+5) = **~607**.

Both land inside the envelope this package demonstrably ships at `size:s`: `#1364` 382, `#1316` 246, `#1343` 428, `#1340` 446, `#1357` 466, `#1342` 489, `#1338` 514, `#1363` 520, `#1337` 559, `#1304` 602, `#1366` 668 — none carrying a `max_turns` or salvage marker. The `~600` red line is calibrated for production Go with cascading call-site edits; here the edit fan-out is one line-count-neutral comment block, and #1290's architect already recorded the miscalibration for this family as an operator note. Not split.

**File-overlap check (§ 1.5), run on `e9b7864`:** `git fetch origin --prune` then a scan of every `origin/feature/<N>` branch's diff against `main` for `finding_trailer_evidence_test.go` and `finding_artifact_write_test.go` — **no overlap**. No `addBlockedBy` needed.

---

## 9. Open questions

1. **Does AC1's Detail headroom walk belong here at all?** Specified as included (§ 3.2) because AC1's fixture is *not* the fixture the shipped walk runs over, and #1284's observed defect is exactly a sweep going green because a Detail ate its budget. The counter-argument is `finding_artifact_write_test.go:1063-1071`'s doctrine that a check must not acquire a precondition its claim does not use — but this claim *does* use it: a truncated Detail hides a leak from the sweep eight lines below. If code review disagrees, dropping it is 8 lines and no other assertion depends on it.
2. **Should AC3 walk `finRecordRun` as a third carrier?** Left out — the AC names `finSighting` and `finTrailerRecord`, and `finRecordRun` reaches the latter, so the sub-record ban is the tighter statement. The residual gap is a `map[string]json.RawMessage` field added **directly** to `finRecordRun`, which `TestFinWriteArtifactCarriesNoCapturedByteShapedKey`'s `raw`-shaped-key rule would catch on the rendered artifact but no type-level walk would. Widening is one line if review wants it; it is not in this ticket's ACs.
3. **The needle-key-name constant's exact length.** The ticket body measured 55 bytes over a 288-byte fixture of its own construction; this spec splices onto `trailFixtureTrailer` (342 B → ~404 B), so the byte figures differ while every asserted property (12 names, under both bounds, sorts first, far below the scanner ceiling) is identical. The spec pins the **properties in code** and no literal length; a developer reconciling the two numbers should trust the assertions.
4. **Whether AC2's failure message may name the leaking field.** Specified yes — the needle map's **key** (`result`, `session_id`, …) is a field name this rig chose, not a byte from the line, so naming it leaks nothing while making the failure diagnosable. The needle and the published name itself are never printed.

---

## 10. Security review

**Verdict:** PASS (second pass — the first found a MUST FIX in § 3.1, revised inline and re-walked)

**Findings:**

- **[Trust boundaries] MUST FIX — fixed in § 3.1.** The first draft's `finContainRender` returned the whole `trailScanResult`, promoting `.Line` — up to 512 bytes of model-chosen text marked OPERATOR-REVIEW-BEFORE-PASTE (`result_trailer_observation_test.go:100-107`) — and the `*resultTrailer` into every test in the new file, held out of failure messages by a prose rule alone. That is the reach `finGatherReadings` deliberately refuses to hand back (`finding_run_gather_test.go:483-489`) and that `finSighting` exists to sever, and it inverts this family's stated doctrine (`finding_artifact_write_test.go:28-32`). Narrowed to `[]string`; neither caller needed more. After the fix no test in this file holds a `trailScanResult`, a `*resultTrailer` or a `trailObservation` — AC2's drive `finPublishedKeyNames` also returns `[]string` pairs — which makes the file's containment structural, complementing AC3's type-level ban.
- **[Trust boundaries] No further findings.** The untrusted→trusted crossing is a single explicit point: `trailKeyNames` (`trailer_key_names_test.go:87-106`) discards its `map[string]json.RawMessage` internally and returns `[]string`. Every check here sits downstream of it; none re-decodes the line.
- **[Tokens/secrets] Not applicable, by design rather than by absence.** No token, credential, key or env read on any path; the file's own header rule is "no credentials". The needles are **fixed public constants deliberately** — a `crypto/rand` needle would make failures irreproducible and destroy the per-position diagnosis AC2 rests on.
- **[File operations] No findings.** The only writes are `finWriteArtifacts`' two files at `0o600` under a per-test `t.TempDir()`; both names are constants, so no input reaches a path and traversal is unreachable. `finWriteReadDir`'s `os.ReadDir`-then-`os.ReadFile` is a check-then-use gap that is pre-existing and inert in a temp dir no other process is told about; `os.ReadFile` would follow a planted symlink, and nothing plants one. No atomic-write requirement — these are throwaway artifacts, not a registry.
- **[Subprocess] No findings, and one constraint carried forward.** No exec is introduced; the design reuses `finWriteInputs` wholesale, whose exec-freedom is the argument `finding_artifact_write_test.go:34-59` makes by enumerating every exec-reaching helper **by file:line rather than by name**, so a forbidden-symbol grep reports on that file's code and cannot be defeated by its prose. The new file's header must follow the same rule: cite any exec-reaching helper by file:line, never by symbol.
- **[Cryptographic primitives] Not applicable** — no randomness, no key material, no comparison against a secret. See the fixed-constant decision above.
- **[Network & I/O] No findings.** No socket, no HTTP, no timeout surface. The input size limit is real and addressed: `bufio.Scanner`'s 64 KiB default **aborts** rather than truncates, and past it `KeyNames` is nil and every assertion goes vacuously green — so the ceiling is a `t.Fatalf` precondition inside `finContainRender` and, for AC2, inside the shipped `finPublishedKeyNames`. The other three caps (`reachMaxCommandBytes = 512`, `finTrailerMaxKeyNames = 32`, `finTrailerMaxKeyNameBytes = 64`) are asserted per fixture rather than assumed.
- **[Error messages, logs] SHOULD FIX — specified, code review should hold the line.** The file-wide rule (§ 3.1, § 6) is: name the file, JSON path, byte length, offset, count or rig-chosen field name; never a file's contents, a Detail's string, `scan.Line`, or a needle-bearing value. After the MUST FIX the strongest channel is closed by shape. One residual is accepted knowingly: the non-vacuity Fatalfs print key **names**, and under mutation M1 those names would be values — but they are fixture constants on a fixture-only path, and the shipped precedent is `TestTrailKeyNamesCarryNoValues:342-345`.
- **[Concurrency] No findings.** `go test -race` runs this package in parallel, so every fixture is a function and never a package-level `var` (§ 5); each test owns its `t.TempDir()`; the returned name slice aliases a function-local scan nothing mutates. No goroutine, no lock, no shared state.
- **[Threat model alignment] OUT OF SCOPE, one item, corrected and unfiled.** The fence escape (§ 4): `json.MarshalIndent` does not escape backticks, so a model-influenced string carrying ```` ``` ```` breaks out of `run.md`'s fence when pasted. The ticket body attributes this to `stop_reason` alone; since #1363 the **key-name field is a second carrier**, and the 64-byte per-name bound does not help. This ticket creates and widens neither — it adds checks and no field — and **no ticket is filed**. AC4's key-count half (M7) is likewise named as having no instrument at the row rather than implied discharged, and `finding_run_gather_test.go:581`, the live fill site no offline test drives (#1364's NIT 3), is named and left.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-08-07
