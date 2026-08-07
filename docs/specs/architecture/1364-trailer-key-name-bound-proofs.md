# #1364 — Probe instrument: prove the published trailer key-name bounds bite

**Size:** S (PO's `size:s` confirmed — one new test file, no production file touched, no consumer call site changed)
**Verification gate:** `go test -race -tags e2e_realclaude -run '^TestFin|^TestTrail' ./internal/e2e/realclaude/` plus `go vet -tags e2e_realclaude ./internal/e2e/realclaude/...`. `make check` never compiles this diff — every file here carries the `e2e_realclaude` build tag, so a green standard gate says nothing about it.

## Files to read first

| path | what to extract |
|---|---|
| `internal/e2e/realclaude/finding_run_gather_test.go:418-471` | The two constants and `finBoundKeyNames`' five-clause doc comment. **This is the contract every assertion below pins.** Clause 4 (truncate-and-mark) is why `len(name) <= finTrailerMaxKeyNameBytes` is the wrong assertion. |
| `internal/e2e/realclaude/finding_run_gather_test.go:386-417` | Why 32 and 64, and the scanner-ceiling argument that makes the fixture-size preconditions load-bearing. |
| `internal/e2e/realclaude/finding_trailer_evidence_test.go:391-402` | `finTrailerSighting` — the fixture-side fill site that applies the bound. This is the first hand the new tests drive. |
| `internal/e2e/realclaude/finding_trailer_evidence_test.go:303-339` | `finTrailerBuild`, and the plain slice assignment at `:333` whose comment names clause 5 as its reason for being safe. |
| `internal/e2e/realclaude/finding_run_record_test.go:1059-1165` | `TestFinRecordPublishesTheTrailerKeyNamesTheReaderRead` — the UNDER-bounds complement of this ticket. **Mirror its idiom**: fatal non-vacuity preconditions first, expectation derived from a shipped producer rather than hand-written, one `reflect.DeepEqual`. |
| `internal/e2e/realclaude/result_trailer_observation_test.go:301-342` | `trailFixtureTrailer`, `trailPaddedTrailer`, `trailNeedle`, `trailOverlongPad` — the fixture renderers the new ones build on. |
| `internal/e2e/realclaude/result_trailer_observation_test.go:157-233` | `trailScan`: the match return that fills `KeyNames` from the whole line, and the ABORT arm past 64 KiB that makes a naive hostile fixture green by breakage. |
| `internal/e2e/realclaude/trailer_key_names_test.go:1-60` | The build tag and the file-header shape to copy (measured fixture table, the "runs offline: no live claude, no credentials, no exec, no clock" paragraph, the `go test` line). |
| `internal/e2e/realclaude/trailer_key_names_test.go:70-123` | `trailKeyNames` (the unbounded reader) and `trailExpectedKeyNames()` — the eleven envelope names that serve as AC1's "short names unchanged" expectation. |
| `internal/e2e/realclaude/background_reach_probe_test.go:117-124` | `reachMaxCommandBytes` = 512 and `reachTruncationMarker` (29 bytes) — the marker clause 4 appends. |
| `internal/e2e/realclaude/finding_trailer_evidence_test.go:175-215` | What the bound does to a reader (the alphabetic-prefix consequence AC2's fixture makes observable), and the Detail prohibition that is **#1362's**, not this ticket's. |
| `internal/e2e/realclaude/trail_run_outcome_test.go:604-610` | Why every slice-returning fixture is a function and never a package-level var: `go test -race` runs this package in parallel. |

## Context

#1363 shipped `finBoundKeyNames` and stated five clauses in its doc comment. None of the five is pinned. The artifact's standing safety sentence (`finding_artifact_write_test.go:98-118`) now describes the key-name field as bounded, so "bounded" is a claim an operator relies on and nothing fails when it stops being true.

Two things make the proofs measurable rather than tautological:

1. **The bounding is applied at the fill, not at the reader.** `trailScanResult.KeyNames` is the *unbounded* reader output for a line; the record's field is the bounded one. A row can therefore assert both halves — what the line really carried, and what the record published — entirely from shipped code.
2. **The hostile fixtures must be small.** `trailScan`'s `bufio.Scanner` buffer is deliberately not raised past the 64 KiB default (`result_trailer_observation_test.go:177-179`); 65535 bytes are accepted, 65536 aborts the scan. On the aborted arm `KeyNames` is nil and `CarriesTrailer` is false, so the field renders `null` — and *"the rendered field is bounded"* is trivially true over a fixture that produced not one name. Both fixtures here are a few hundred bytes, and both rows assert `trailer-seen` **as a fatal precondition** rather than trusting that.

Everything is offline: no live claude, no credentials, no env gate, no `t.Skip`, no exec, no clock, no goroutine.

## Design

### Placement — a new file, and nothing else touched

Create **`internal/e2e/realclaude/finding_key_name_bounds_test.go`**. Build tag `//go:build e2e_realclaude`, package `realclaude`.

A new file adds no line to any existing file, so AC4 is satisfied by construction: every inbound line-number pointer in `internal/` still resolves. **No existing file may be edited by this ticket.** In particular:

> **The trap.** This family's convention is to name the enforcing test at the contract it enforces (`finTrailerRecord`'s comment does it at `:214-215`). Doing that here means editing `finBoundKeyNames`' doc comment at `finding_run_gather_test.go:423-454` — and cites sit below it. Measured on `c13774f`: **seven filename-anchored inbound cites in `internal/` point at or past `:455`** (`:557` ×2, `:605`, `:2043` ×4), and the bare `(:NNN)` and symbol-anchored `<Type>:NNN` forms add more on top (the ticket counts fourteen across all three). Any insertion there re-opens the full three-form sweep across the package. The reference is therefore **one-directional**: the new file's header names the clauses it pins and cites the helper; the helper says nothing about the new file. State that decision in the new file's header so a later editor does not "fix" it.

Outbound cites (the new file naming other files' lines) are free and expected.

### The two fixtures

Both are functions, never package-level vars — they return slices' source strings and this package runs `-race` in parallel (`trail_run_outcome_test.go:604-610`).

```go
// 65 bytes: finTrailerMaxKeyNameBytes + 1, sized off the constant so it moves with it.
func finOverlongKeyName() string

// trailPaddedTrailer(0) with finOverlongKeyName() spliced in as one extra top-level key.
func finOverlongKeyNameTrailer() string

// {"type":"result", plus `extra` synthetic short keys} — the count fixture.
func finManyKeyNamesTrailer(extra int) string
```

- `finOverlongKeyName` builds a **descriptive stem** padded to `finTrailerMaxKeyNameBytes + 1` bytes (e.g. `"pyry_probe_overlong_top_level_key_name_" + strings.Repeat("x", …)`). The stem matters: the published form keeps the first 64 bytes, so a reader of a failure message can see *what* was truncated.
- `finOverlongKeyNameTrailer` **splices onto the shipped renderer** rather than writing a fresh literal — `strings.TrimSuffix(trailPaddedTrailer(0), "}") + `,"` + name + `":"v"}``. That is what makes the other eleven names the **real envelope names**, so `trailExpectedKeyNames()` is a valid expectation for AC1's "short names still published unchanged" half. A hand-written literal would pin the fixture's own key set instead.
  - It inherits `trailNeedle` in `result` from `trailPaddedTrailer`. **Inert here**: no row in this file renders the artifact, sweeps for the needle, or reads `trailScanResult.Line`. The artifact-wide sweeps are #1362's.
- `finManyKeyNamesTrailer(extra)` renders `{"type":"result","k00":0,…}`. `type` is mandatory — without it `trailScan` never matches and both rows compare two empty reads. Called with `finTrailerMaxKeyNames`, so the fixture **sizes itself off the constant**; the row then asserts the resulting count against the bound rather than trusting the arithmetic.

### The drive helper

```go
// Drives the shipped chain and returns (unbounded reader names, published names).
// Asserts the trailer-seen precondition as t.Fatalf before returning.
func finPublishedKeyNames(t *testing.T, line string) (read, published []string)
```

Chain: `trailScan([]byte(line+"\n"))` → `finTrailerSighting(scan, 250*time.Millisecond, trailBoundFromMiss)` → `finTrailerBuild(trailOutcomeVoidBudgetFired, sighting)`; returns `scan.KeyNames` and `rec.KeyNames`.

**It stops at `finTrailerBuild` deliberately.** The remaining hands are already pinned: `TestFinRecordEmbedsTrailerRecordWhole` (`finding_run_record_test.go:757`) pins that `finRecordRun` embeds the sub-record whole, and `TestFinRecordPublishesTheTrailerKeyNamesTheReaderRead` pins the carriage through `finRecordBuild`. A fourth hand adds cost and no discrimination.

**The fatal precondition is the ceiling defence.** It must report `scan.State`, whether a decode came back, and `scan.Detail` — an aborted scan is exactly the state that would make every bound assertion vacuously green, so the failure message names it.

### The four tests

All four match the `^TestFin|^TestTrail` gate selector.

**`TestFinPublishedKeyNamesBoundAnOverLongName`** — AC1, over `finOverlongKeyNameTrailer()`.

- Precondition (fatal): at least one name in the **unbounded reader output** exceeds `finTrailerMaxKeyNameBytes`. Without it there is nothing to truncate and the row is green over any implementation.
- Assertion A — the AC's headline, marker-aware: for every published name, `len(strings.TrimSuffix(name, reachTruncationMarker)) <= finTrailerMaxKeyNameBytes`. **`len(name) <= finTrailerMaxKeyNameBytes` is red against a correct build** — clause 4 appends the 29-byte marker, so a truncated name is published at 93 bytes. The bound is on what is kept *from the line*; the marker is this rig's own bytes.
- Assertion B — one `reflect.DeepEqual` covering both halves the AC calls non-optional: expectation is `trailExpectedKeyNames()` plus `finOverlongKeyName()[:finTrailerMaxKeyNameBytes] + reachTruncationMarker`, sorted. This is red against a wholesale drop of the set, red against a drop of the over-long entry alone, red against truncation without the marker, and red against any short name being altered. `sort.Strings` on the expectation is valid because truncation preserves the first 64 bytes, so the marked entry sorts where the full name did.

**`TestFinPublishedKeyNamesBoundTheNameCount`** — AC2, over `finManyKeyNamesTrailer(finTrailerMaxKeyNames)`.

- Preconditions (fatal): the unbounded reader output carries **more** than `finTrailerMaxKeyNames` names; and every one of its names is **short**, so this row engages the count bound alone and stays independent of AC1's.
- Assertion A: `len(published) == finTrailerMaxKeyNames` — **exactly**, not `<=`. "No more than N" is trivially true of a field carrying none, which is the AC's stated failure shape.
- Assertion B: `reflect.DeepEqual(published, read[:finTrailerMaxKeyNames])` — the alphabetic prefix, in the reader's order. Note what this makes observable: the fixture's `type` sorts last of the 33 and is therefore the name the bound cuts, which is exactly the consequence `finTrailerRecord`'s comment warns a reader about at `:178-183`.

**`TestFinBoundKeyNamesReturnsNilForEmptyInput`** — AC3 clause 1, direct call, table of two rows (`nil`, `[]string{}`). Assert the returned slice is `nil`. `len(got) == 0` is **not** the assertion — that is exactly the mutant. The failure message must say why: a non-nil empty slice renders `[]` where the artifact must carry `null`.

**`TestFinBoundKeyNamesAllocatesItsOwnBackingArray`** — AC3 clause 5, direct call, three rows:

| row | input | why it exists |
|---|---|---|
| under both bounds | `[]string{"a","b","c"}` | **The AC's named row.** The only shape clause 5 forbids; the `if there is nothing to do, return names` fast path is invisible to every other row. Also asserts contents unchanged. |
| over the count bound | 33 one-byte names | Catches a `return names[:kept]` "optimisation" of the count path — content-identical, so no other assertion in this ticket sees it. |
| over the per-name bound | one short, one 65-byte | Completes "on every path". |

- Each row copies its input first, then asserts `&got[0] != &in[0]`. Guard with a fatal `len(got) == 0` check, or the address comparison is vacuous.
- The failure message must carry the consequence, not just the fact: `finTrailerBuild` copies this field by plain slice assignment (`finding_trailer_evidence_test.go:333`), so a pass-through puts two carriers on one backing array while `-race` runs this package in parallel.

## Concurrency model

None introduced: no goroutine, no channel, no clock, no shared state. The one concurrency-relevant rule is inherited — every fixture returning a slice or a slice-producing string is a **function**, never a package-level var, because this package's tests run in parallel under `-race` and a shared backing array lets one row's mutation reach another's.

## Error handling

Test-tier only, and the split is load-bearing:

- **`t.Fatalf` for preconditions** — trailer-seen, the reader genuinely over the bound, names short (AC2), non-empty result (clause 5). These are the "fails on its own preconditions rather than passing through them" requirement. A precondition that degrades to `t.Errorf` still runs the assertion below it against a broken fixture and muddies which thing failed.
- **`t.Errorf` for the contract assertions** — a row should report every violated clause in one run.
- The helper `finBoundKeyNames` is pure and never returns an error; the tests introduce no new failure mode into shipped code.

## Testing strategy

The design was validated before this spec was written, by running the drafted tests against six mutants of `finBoundKeyNames` via `go test -overlay` (scratchpad copies; the worktree was never written). Measured on `c13774f`, `-race`, tag `e2e_realclaude`:

**Fixtures, shipped build:**

| fixture | line bytes | `trailScan` state | reader names | published |
|---|---|---|---|---|
| `finOverlongKeyNameTrailer()` | **457** | `trailer-seen` | 12 (one at 65 bytes) | 12 — the over-long one at **93** bytes, truncated and marked |
| `finManyKeyNamesTrailer(32)` | **273** | `trailer-seen` | 33 | **32** (`type` cut, the alphabetic tail) |

Both are two orders of magnitude below the 65535-byte scanner ceiling. (The ticket's 295-byte figure for the count fixture is its author's generator; 273 is the shape prescribed above. Neither is near the ceiling.)

**Mutant × row matrix — every mutant red, each on exactly one row:**

| mutant of `finBoundKeyNames` | sole red row |
|---|---|
| clause 1 — `return []string{}` for empty input | `…ReturnsNilForEmptyInput` (both sub-rows) |
| clause 5 — `if under both bounds { return names }` fast path | `…AllocatesItsOwnBackingArray` / under both bounds |
| count path returns `names[:kept]` (aliases) | `…AllocatesItsOwnBackingArray` / over the count bound |
| over-long entry dropped instead of truncated | `…BoundAnOverLongName` (assertion B) |
| over-count input returns nil (wholesale drop) | `…BoundTheNameCount` (both assertions) |
| count bound never applied | `…BoundTheNameCount` (both assertions) |

Every row is some mutant's sole red, and no row is redundant — the third mutant is the whole reason the clause-5 table has more than the AC's one named row.

**Gate, on a shell with no Anthropic credentials in the environment:** `go test -race -tags e2e_realclaude -run '^TestFin|^TestTrail' ./internal/e2e/realclaude/` reported **75 PASS, 0 SKIP** with the drafted file in place (71 before), and `go vet -tags e2e_realclaude ./internal/e2e/realclaude/...` was clean. This is not a `needs-real-claude` ticket.

**Also verify before finishing:** `gofmt -l` on the new file, and that `git diff origin/main --stat` names exactly one file.

## Explicitly out of scope

- **A second row under `TestFinTrailerRecordCarriesNoCapturedBytes` (`:930`)** for the Detail prohibition. The ticket measured it: the over-long fixture's names-interpolating mutant reaches 444 bytes against a 470-byte budget — **green over the violation it claims to detect**. It belongs to **#1362**, whose names-field-only needle sweep goes red on it with no dependence on a byte budget.
- **The artifact-wide byte sweep and the `finRecordInputReaches` ban** — #1362.
- **The ``` ```json ``` fence-escape exposure via `stop_reason`** — pre-existing, uncapped by design (`finding_trailer_evidence_test.go:128-136`), a separate ticket if it is anyone's.
- **`docs/knowledge/codebase/1364.md`** — written by the documentation phase after merge, not a developer deliverable.

## Open questions

None blocking. One judgement call recorded rather than left to be re-derived: the drive helper stops at `finTrailerBuild` rather than continuing to `finRecordBuild`. If code review prefers the full chain, the change is one call and the assertions are unaffected — but it duplicates coverage `TestFinRecordEmbedsTrailerRecordWhole` already holds.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No findings, and this ticket exists *because* of one. The boundary is claude-authored line bytes → published artifact, and it is explicit and single: `trailKeyNames` (`trailer_key_names_test.go:87`) discards its `map[string]json.RawMessage` so no **value** can cross structurally, and `finBoundKeyNames` (`finding_run_gather_test.go:455`) bounds the **names** at the fill site both carriers reach. This ticket adds no new crossing — it pins the existing one. Downstream holders know what they hold: `finTrailerBuild:329-333` states in code that its plain slice assignment is safe only because the producer's clause 5 holds, and this ticket makes that dependency red-on-violation instead of comment-only.
- **[Trust boundaries — residual, named not fixed]** The bound is per-name and never a joined cap, which is the #1284 defect's shape. Assertion A checks bytes *kept from the line* per element; a joined-total assertion would let a leak in a late name be truncated away and go green. This is enforced at the assertion, not just described.
- **[Error messages, logs, telemetry]** SHOULD FIX → already designed in. Test failure messages here interpolate **key names**, including the hostile fixture's — and those strings are generated by this file, not by claude, so no model-authored bytes reach a failure message. The developer must keep it that way: **do not interpolate `scan.Line` or `trailNeedle` into any `t.Errorf`**, since a failure message from a `-realclaude` run can be pasted into a public issue. Nothing in the prescribed assertions needs `Line`.
- **[Network & I/O — input size limits]** No findings; the cap is the subject. The 64 KiB scanner ceiling is deliberately not raised, and the design's response is a fatal `trailer-seen` precondition on both fixtures so a future fixture that grows past it fails loudly rather than passing vacuously. Both fixtures measured at 457 and 273 bytes.
- **[Concurrency]** No findings. No goroutine, no lock, no shared mutable state. The one hazard in reach — two carriers on one backing array under `-race` — is the exact property clause 5's row pins, and every fixture is a function rather than a package-level var per `trail_run_outcome_test.go:604-610`.
- **[File operations]** Not applicable: the diff adds one test file and the tests touch no path, open no file, and write no artifact. The artifact writer is not driven here.
- **[Subprocess / external command execution]** Not applicable by design, and it is the family's stated rule: this file execs nothing, reads no `ps`, and takes no clock. Everything is driven from fixture strings through pure functions.
- **[Tokens, secrets, credentials]** Not applicable: no credential is read, and the gate is confirmed to run rather than skip with no Anthropic credentials in the environment (75 PASS, 0 SKIP).
- **[Cryptographic primitives]** Not applicable: no randomness, no hashing, no comparison against a secret. Fixtures are deterministic literals sized off named constants.
- **[Threat model alignment]** The relevant threat is the probe family's own: model-influenced bytes reaching an artifact an operator is told is safe to paste unreviewed (`finWriteSafetyClaim`, `finding_artifact_write_test.go:124`). This ticket closes the "bounded is a claim, not a checked property" half. The remaining half — that nothing *else* from the line reaches the artifact — is #1362's needle sweep, named here as out of scope with its owner.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-08-07
