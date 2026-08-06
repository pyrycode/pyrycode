# #1357 — Probe instrument: read a result trailer's top-level key names off the full matched line, before the cap

**Size:** S (confirmed — see § Size check). **Offline:** no live claude, no credentials, no daemon, no env gate, no `t.Skip`.

```
go test -tags e2e_realclaude -run '^TestTrail' ./internal/e2e/realclaude/
```

## Files to read first

| Path | What to extract |
|---|---|
| `internal/e2e/realclaude/result_trailer_observation_test.go:1-49` | File header + build tag + import block. **The two edits this ticket makes to this file land here and at :98 / :176.** |
| `internal/e2e/realclaude/result_trailer_observation_test.go:94-121` | `trailScanResult` — the four fields, and the `Line`-is-capped / `Trailer`-is-the-full-decode doc that the new field's doc must sit beside and extend. |
| `internal/e2e/realclaude/result_trailer_observation_test.go:164-208` | `trailScan` — the match return at :176-188 is the **only** invocation site for the new reader. `scanner.Bytes()` is whole there; `Line` at :182 is not. |
| `internal/e2e/realclaude/result_trailer_observation_test.go:276-317` | `trailFixtureTrailer`, `trailNeedle`, `trailPaddedTrailer(pad)` — the fixtures AC1/AC4 drive, and the plant-past-the-cap rule AC3's own fixture must honour (:297-300). |
| `internal/e2e/realclaude/result_trailer_observation_test.go:477-525` | **The trap.** `TestTrailScan`'s padded sub-test marshals the *whole* `trailScanResult` and sweeps it for `trailNeedle`. Correct here, and **fatal if copied for AC3** — see § AC3. |
| `internal/e2e/realclaude/tool_loop_test.go:185-210` | `resultTrailer`'s eight fields + `resultTrailerUsage`. AC5a pins exactly this set. Note `PermissionDenials *[]json.RawMessage` at :199. |
| `internal/e2e/realclaude/background_reach_probe_test.go:123-124, :945-950` | `reachMaxCommandBytes = 512`, `reachTruncationMarker` (29 bytes, contains no `"`), and `reachCapCommand`. |
| `internal/e2e/realclaude/finding_run_record_test.go:720-752` | `finRecordInputReaches` — the shipped reflect walk AC5b reuses verbatim. Follows struct fields, slice/array elems, pointers, map keys **and** values. |
| `internal/e2e/realclaude/finding_run_gather_test.go:1916-1932` | `TestFinSightingReachesNoScanType` — the **exact shape** AC5b copies (carrier type, forbidden-type slice, one walk per type). |
| `internal/e2e/realclaude/finding_run_gather_test.go:1936-1941` | `finGatherForbiddenKeys` — the six substrings the new JSON key must dodge, because #1358 carries this name onto `finSighting`, which *is* swept. |
| `internal/e2e/realclaude/finding_trailer_evidence_test.go:907-930` | The `map[string]json.RawMessage` key-scan idiom, **and** its own note (:913-917) that the scan is top-level only. |
| `internal/e2e/realclaude/trail_run_outcome_test.go:608-610` | The package's fixture rule: fixtures are **functions, never package-level vars** — `go test -race` runs these tests in parallel over a shared backing array. |

## Context

`trailScanResult.Trailer` is a decode into `resultTrailer` — eight fixed fields, deliberately **no `result` member**, which is what makes the 512-byte cap safe to apply to `Line` alone. That property is preserved here, not relaxed.

What the fixed decode cannot answer: `TerminalReason` is a plain `string` with `omitempty`, so an **absent** `terminal_reason` and an emitted `"terminal_reason":""` both decode to `""`. `terminal_reason` is a pyry invention (`streamjson/emitter.go:428-437`, `streamrunner/watchdog.go:253`); on the headless `PYRY_USE_STREAMJSON=1` path the trailer on a healthy run is **claude's own `result` line**, which carries no `terminal_reason` at all. Telling claude's line from pyry's synthesised one is therefore a question about *which keys the line carried*, not about any field's value.

Every number the ticket cites was re-derived against this tree before this spec was written:

| fixture | line bytes | capped bytes | top-level keys, full line | top-level keys, capped copy |
|---|---|---|---|---|
| `trailFixtureTrailer` | 342 | 342 | 11 | 11 |
| `trailPaddedTrailer(0)` | 385 | 385 | 11 | 11 |
| `trailPaddedTrailer(2000)` | 2385 | 541 | 11 | **0** |

The `0` is the whole difficulty: the cap lands 407 bytes into the `result` string, leaving it unterminated, so a reader fed `Line` returns nothing on every realistic trailer while staying correct on every short fixture. Both fixtures carry the **same** eleven names, sorted:

```
duration_ms  is_error  num_turns  result  session_id  stop_reason
subtype  terminal_reason  total_cost_usd  type  usage
```

## Design

Three edits. Two of them are one line each.

### 1. The reader — new file `internal/e2e/realclaude/trailer_key_names_test.go`

```go
// trailKeyNames returns the sorted names of line's top-level JSON keys, and
// nothing else. Returns nil when line does not decode as a JSON object.
func trailKeyNames(line []byte) []string
```

Behaviour: decode into `map[string]json.RawMessage`, collect the keys, `sort.Strings`, return. **The map is discarded inside the function** — it is never stored, returned, formatted, or reachable from any returned value. Its `json.RawMessage` values are the raw bytes; a `%v` on the map prints them, and this package already formats a trailer sub-record with `%+v` (`finding_run_record_test.go:775`).

Three contract decisions, each load-bearing:

- **`[]byte` in, `[]string` out — and no `error`.** The failure arm must return `nil` and render *no part* of the decode error. `json.SyntaxError` embeds a byte offset into its own input and `json.UnmarshalTypeError` names the offending value; returning or wrapping either re-admits a value-capture channel that **no needle can catch**, because the captured byte is chosen by the error, not by the fixture. This is pinned by construction with its reason stated at the return, not by a sweep. State that reason in the code comment.
- **Top level only.** `map[string]json.RawMessage` decodes one level. That is not an accident to work around — it is exactly what AC1's "`usage` contributes `usage` and never its four sub-keys" asserts.
- **Sorted.** A map decode offers no other stable order; AC4 asserts the exact slice.

Two consequences worth stating in the doc comment rather than discovering: duplicate top-level keys collapse to one name, and the input is already bounded by `bufio.Scanner`'s 64 KiB default (`trailScan` aborts past it), so no additional size cap is introduced here.

### 2. The field — `result_trailer_observation_test.go:98-121`

```go
KeyNames []string `json:"trailer_keys,omitempty"`
```

`omitempty`, matching this tier's convention for fields that are empty unless `State == trailSeen` (`Line`, `Trailer`). The JSON key is clear of all six of `finGatherForbiddenKeys`' substrings (`command`, `args`, `comm`, `argv`, `line`, `stderr`) — deliberately, because #1358 carries this name onto `finSighting`, which **is** swept. There is no rule to satisfy at *this* tier: `finGatherForbiddenKeys` runs over `readings`, the attribution record and `finSighting`, never over `trailScanResult`.

The doc comment carries the argument the field exists for: these are names read off the **full** line, so an absent key and a key emitted as its zero value are distinguishable here and nowhere else on this record.

### 3. The wiring — `result_trailer_observation_test.go:176-188`

One line inside the existing match return:

```go
KeyNames: trailKeyNames(scanner.Bytes()),
```

`scanner.Bytes()` — **not** `Line`, and not `reachCapCommand(...)`. The existing comment at :178-181 already argues why the decode runs against the full line; extend it to cover the names. This is the entire ticket, and AC1 is what makes getting it wrong go red.

### Data flow

```
stdout bytes
  └─ trailScan: bufio.Scanner, line at a time
       ├─ json.Unmarshal → resultTrailer   (full line; 8 fixed fields, no `result`)
       ├─ trailKeyNames(scanner.Bytes())   (full line; names only, map discarded)
       └─ reachCapCommand(string(...))     (→ Line, 512 B + marker, TRUNCATED JSON)
```

Two readers of the full line, one capped copy. Nothing reads the capped copy back.

## Concurrency model

No goroutines, no channels, no clock, no locks. `trailScan` and `trailKeyNames` are pure over their bytes — that is what lets every arm be driven offline. The one package rule that applies: **fixtures are functions, never package-level vars** (`trail_run_outcome_test.go:608-610`) — `go test -race` runs these tests in parallel and a shared backing array is reachable from all of them. The expected-names slice and AC3's fixture line are therefore both functions.

## Error handling

| Path | Behaviour |
|---|---|
| Reader, called from `trailScan` | **Cannot fail.** The match return is reached only once `tr.Type == "result"`, settable only from a JSON object, so the map decode always succeeds and always carries at least `type`. There is no scan-tier test to write for the malformed-line or no-trailer arms — `KeyNames` is empty there because the reader was never invoked, the same rule that leaves `Line` and `Trailer` empty off `trailSeen`. |
| Reader, called directly on a capped copy (AC1) | Returns `nil`. **The reachable failure arm.** Renders nothing from the decode error — see § Design 1. |
| `trailScan` states | Unchanged. `trailAborted` / `trailAbsent` returns gain no field. |

## Testing strategy

Five top-level tests covering six obligations. All offline; scenarios as bullets, the developer writes them in this package's idiom.

**T1 — `TestTrailKeyNamesReadsTheFullLine` (AC1 + AC4, table-driven).** AC1 and AC4 assert the same shape against different fixtures, so one table serves both.

- `trailFixtureTrailer` through `trailScan` → `KeyNames` equals the exact eleven-name sorted slice. (AC4: exact slice, not "two reads agree" — whether that weaker form catches anything is decided by the fixture's key count, not by the implementation.)
- `trailPaddedTrailer(2000)` through `trailScan` → `KeyNames` equals the **same** exact eleven. **This row is the red** that pins the ordering: an implementation reading `Line` yields 0 here.
- That same scan's `.Line` passed directly to `trailKeyNames` → empty. Documents the ordering and exercises the reachable failure arm; note in the test that this row is green under either implementation and the row above is what discriminates.
- Assert with `reflect.DeepEqual` against one shared `trailExpectedKeyNames()` **function**. One assertion covers exactness, sortedness, and non-descent (`usage` present, its four sub-keys absent).

**T2 — `TestTrailKeyNamesSeparatesAbsenceFromZeroValue` (AC2).**

- Fixture A: a trailer with no `terminal_reason`. Fixture B: the same line with `"terminal_reason":""`.
- **Both must carry `"type":"result"`** or `trailScan` never matches and the test compares two empty reads.
- Assert the two name reads **differ** (B carries `terminal_reason`, A does not).
- Assert both decode through `resultTrailer` to the **same** `TerminalReason` (`""`). This half is asserted, not assumed — it is what establishes the existing decode could not have answered this.

**T3 — `TestTrailKeyNamesCarryNoValues` (AC3).** See § AC3 below before writing this one.

- A fixture the test renders itself, with a **distinct** needle in each of `result`, `session_id`, `subtype`, `stop_reason`, `terminal_reason`. `trailNeedle` is a single shared constant spliced into `result` alone and does not serve.
- `result`'s needle must sit **past** the 512-byte cap (`trailNeedle`'s rule at :297-300). Padding `result` with ~600 x's is enough; verified against this tree, the needle lands at offset 733 with pad 600 and 2133 with pad 2000.
- **Non-vacuity precondition, asserted first:** `State == trailSeen` **and** `KeyNames` equals the expected eleven. Without it a reader returning nothing passes this sweep vacuously — the ticket names this failure mode explicitly.
- Then: none of the five needles appears as a substring of any recorded name. **Scoped to `KeyNames` alone.**

**T4 — `TestTrailResultTrailerFieldSetIsPinned` (AC5a).**

- Walk `reflect.TypeOf(resultTrailer{})`; assert `NumField() == 8` and the eight `(name, json tag, type)` triples.
- Compare types against `reflect.TypeOf(...)` values, **not** string literals: `Type.String()` renders a named type package-qualified (`realclaude.resultTrailerUsage`), which makes a string comparison brittle for no gain.
- The failure message carries the reason: `resultTrailer` has no `result` member, and that is what makes the cap safe to apply to `Line` alone. A later widening goes red here with the argument attached.

**T5 — `TestTrailScanResultReachesNoRawMessageMap` (AC5b).**

- `finRecordInputReaches(reflect.TypeOf(trailScanResult{}), reflect.TypeOf(map[string]json.RawMessage{}), map[reflect.Type]bool{})` must be `false`. Same shape as `TestFinSightingReachesNoScanType`.
- **Ban the map type, never the element type.** Re-derived on this tree: `trailScanResult` **already reaches** `json.RawMessage` via `Trailer *resultTrailer` → `PermissionDenials *[]json.RawMessage` (`tool_loop_test.go:199`). A ban naming `json.RawMessage` is **red against correct shipped code on the day it is written**. The map type is green today and is a live guard against the field that would change that.
- The walk is transitive, so this one call also covers "no type transitively holding one".

### <a name="ac3"></a>AC3 — the sweep that goes red against a correct build

`TestTrailScan`'s padded sub-test (`:517-524`, forty lines above the field you are editing) marshals the **whole** `trailScanResult` and sweeps the bytes for `trailNeedle`. That is correct *there*, because `trailPaddedTrailer` plants only in `result` — a field `resultTrailer` does not decode, past a cap `Line` applies.

Copying that idiom for AC3's five-needle fixture fails a correct build. Measured against this tree, for both pad 600 and pad 2000:

| needle planted in | in a whole-record marshal | in `KeyNames` |
|---|---|---|
| `result` | no | no |
| `session_id` | no | no |
| `subtype` | **yes** | no |
| `stop_reason` | **yes** | no |
| `terminal_reason` | **yes** | no |

Three of the five are carried **by design** through `Trailer` — `resultTrailer` decodes `Subtype`, `StopReason` and `TerminalReason`, and downstream publishes them verbatim. The artifact-wide sweep that would catch a leak in them is #1358's, with its own narrower plant list (`finding_artifact_write_test.go:244-259` states the plant-only-where-the-pipeline-reduces rule).

**So: scope AC3's containment assertion to `KeyNames` alone** — iterate the slice, or marshal `got.KeyNames` and `bytes.Contains` that. Do not marshal `got`. State the reason in the test, because the neighbouring idiom is the obvious thing to reach for.

(A `bytes.Contains` over the marshalled names *is* depth-independent and adequate here: `KeyNames` is a flat `[]string`. The flat→nested transplant hazard that bit #1280 does not apply to this field — but it is why the assertion must not be widened to the enclosing record.)

## What this ticket does not do

- **`resultTrailer` is not widened**, and no arbitrary-value capture is introduced. AC5 pins both halves.
- **`TerminalReason` is not retyped to `*string`.** `.TerminalReason` has 30 read sites across seven files in this package — a cascade, not a field change — and it answers only about `terminal_reason`, while the question here is what the trailer carried at all.
- **Key names are not bounded.** They are attacker-influenced in principle (they arrive from claude's output). `trailScanResult` is published by nothing — `finRecordInputs` and `finRecordRun` are pinned to reach neither it nor `trailObservation` at any depth (`finding_run_record_test.go:787-812`), and `finSighting` likewise (`finding_run_gather_test.go:1916-1932`). There is no rendering surface here to bound. The per-name cap belongs at the tier that publishes and is specified there — **#1358**.
- **No knowledge-base doc.** `docs/knowledge/codebase/1357.md` is the documentation phase's, written after the PR merges.

## Open questions

None blocking. Two judgment calls made here, either reversible in review:

1. **Reader in a new file, not beside `trailScan`.** The package precedent is one file per ticket (`finding_live_assembly_test.go`, `finding_exit_path_probe_test.go`), it keeps the already-617-line `result_trailer_observation_test.go` edit to a field plus one line, and it puts the `sort` import in the new file. The cost is that the reader sits away from its only production call site — mitigated by the comment at the call site.
2. **JSON key `trailer_keys`.** Clear of all six forbidden substrings and it is the name that arrives at `finSighting` in #1358. If #1358 wants a different name, changing it here is a one-line edit with no consumers.

## Size check

Sized **S**; no red line trips. Counts are measured, not argued.

| Red line | Limit | This ticket |
|---|---|---|
| New files | ≤ 3 | **1** |
| Total written LOC | ≤ ~600 | **~430–500 projected** (see below) |
| New exported types | ≤ 5 | **0** — everything is package-internal |
| Consumer call sites needing simultaneous update | ≤ 10 | **0 — verified by the compiler**, see below |
| Acceptance criteria | ≤ 5 | **5** |
| Error/reject branches | < ~10 | **1** (the reader's decode-failure arm) |

**The fan-out claim is measured, not asserted.** `trailScanResult` has 69 mentions across 10 files, which is where a line-count-only sizing would go wrong. The proposed change — field, reader, and wiring — was applied to a scratchpad copy and run against the real package via `go test -overlay`:

```
go vet  -tags e2e_realclaude -overlay=<scratch>/overlay.json ./internal/e2e/realclaude/   → clean
go test -tags e2e_realclaude -overlay=<scratch>/overlay.json -run '^TestTrail|^TestFin' … → ok, 6.1s
```

Zero other edits. Every code-level mention is a keyed struct literal or a type reference; the rest are comments. The two shipped guards that could plausibly have broken — `TestTrailScan`'s whole-record needle sweep (`:517-524`) and `TestTrailAdmissibilityRecordsCarryNoCapturedBytes` (`:982`) — both stay green with `KeyNames` populated.

**LOC projection** against the nearest analogues in this same package and shape: #1343 shipped 428 Go LOC, #1340 446, #1337 516+43, all as `size:s`. Six test obligations at this package's density (~70 LOC each incl. doc comment), plus ~35 for the reader, ~20 for the field, ~8 for the wiring, ~25 for AC3's fixture and ~40 for the new file's header. That lands at the middle of the shipped band, not above it.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries] No findings, and this ticket *is* one.** The untrusted→trusted crossing is claude's stdout → `trailScan`. The boundary is explicit and single: `trailKeyNames` is the only new reader, invoked at exactly one site. The type system carries the signal — the reader returns `[]string`, so no value can cross it by construction, which is stronger than the comment-plus-review guard the rest of this record relies on. **Names are attacker-influenced** and the spec says so; the record is published by nothing (pinned at `finding_run_record_test.go:787-812` and `finding_run_gather_test.go:1916-1932`), so no bound is required at this tier.
- **[Error messages, logs, telemetry] MUST FIX, addressed in § Design 1.** The reader's first-draft shape — returning `([]string, error)` and letting the caller render the error — leaks. `json.SyntaxError` carries a byte offset into its own input and `json.UnmarshalTypeError` names the offending value, both attacker-chosen; **no fixture needle can catch that capture**, because the captured bytes are selected by the error rather than planted by the test. The spec pins the signature to `func(line []byte) []string` with a nil return on failure and no error rendered on any path, with the reason stated at the return. Re-walked after the revision: the reader now has no channel that carries a byte of its input outward.
- **[Error messages, logs, telemetry] MUST FIX, addressed in § AC3.** AC3's containment assertion, if scoped to a marshal of the whole `trailScanResult` (the idiom forty lines above the edit), goes **red against a correct build** on three of its five needles — measured, table in § AC3. A developer who then "fixes" the red by deleting needles, widening an exemption, or shortening the fixture would be weakening a *correct* containment guard to satisfy a mis-scoped test. Scoping the assertion to `KeyNames` alone removes the pressure. Re-walked: the assertion now fails only on an implementation that actually returns values.
- **[Error messages, logs, telemetry] No further findings.** The `map[string]json.RawMessage` is discarded inside the reader and never reachable from a returned value, so the `%v`/`%+v` hazard (live in this package at `finding_run_record_test.go:775`) has nothing to print. AC5b pins that structurally rather than by comment.
- **[Network & I/O] Not applicable, and the ticket's offline constraint is why.** No socket, no HTTP server, no TLS, no live claude, no credentials. The one size limit in play is inherited and unchanged: `bufio.Scanner`'s 64 KiB default in `trailScan`, deliberately not raised (`:161-163`). It bounds the reader's input, so the map decode's key count and total allocation are bounded by the same constant — no new resource-exhaustion surface. The 512-byte cap is unchanged and still applies to `Line` alone.
- **[File operations] Not applicable.** No path is constructed, opened, created or written. The developer's worktree mutates two Go files and this spec.
- **[Subprocess / external command execution] Not applicable, and it is a property to keep.** Nothing on this path execs. `trailScan` is pure over its bytes, which is what lets every arm run offline with no credentials; the new reader adds no `exec`, no file read, no clock.
- **[Tokens, secrets, credentials] Not applicable.** No token is generated, stored, compared or transmitted. Note the fixtures' `session_id` is a fixed all-digits placeholder, not a credential.
- **[Cryptographic primitives] Not applicable.** No randomness, hashing, key material or comparison against a secret. `sort.Strings` is the only ordering primitive and its input is non-secret.
- **[Concurrency] No findings.** No goroutine, channel or lock is added; both functions are pure. The one live hazard is the package's own: `go test -race` runs these tests in parallel, so a package-level fixture var would share a backing array across them. The spec requires the expected-names slice and AC3's fixture to be **functions** (`trail_run_outcome_test.go:608-610`). `scanner.Bytes()` is a reused buffer, but the decoder allocates fresh strings for map keys, so no returned name aliases it.
- **[Threat model alignment] Aligned.** The relevant threat is this family's own: verbatim model output reaching a public issue unreviewed. `trailScanResult.Line` is marked OPERATOR-REVIEW-BEFORE-PASTE (`:100-107`) and `Trailer` is deliberately not, because `resultTrailer` structurally cannot carry the payload. `KeyNames` joins `Trailer` on the safe side of that line — **by construction**, pinned by AC3 (no value reaches it) and AC5b (no raw-bytes map is reachable). **OUT OF SCOPE, named:** bounding attacker-influenced key names is deferred to **#1358**, the tier that publishes them.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-08-06
