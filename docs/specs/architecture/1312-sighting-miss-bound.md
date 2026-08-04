# #1312 — Prove the sighting carrier reports the miss bound, measured against a first-poll miss

**Size:** S (confirmed; PO's label stands)
**Files touched:** `internal/e2e/realclaude/finding_run_gather_test.go` only
**Build tag:** `e2e_realclaude` — `make check` / `make build` never compile it

---

## Files to read first

| Path | What to extract |
|---|---|
| `internal/e2e/realclaude/result_trailer_observation_test.go:529-569` | **The idiom to mirror.** `TestTrailWaitForTrailer`'s first subtest: goroutine + 500 ms sleep + append + `<-done`, asserting `trailBoundFromMiss`. Copy its *structure*, not its assertions. |
| `internal/e2e/realclaude/result_trailer_observation_test.go:242-274` | `trailWaitForTrailer`'s loop. The `lastMiss.IsZero()` branch at `:256-262` is the whole mechanism: zero `lastMiss` ⇒ `trailBoundFromStart`, non-zero ⇒ `trailBoundFromMiss`. |
| `internal/e2e/realclaude/result_trailer_observation_test.go:75-90` | The three `trailBoundFrom*` constants and their doc — in particular `trailBoundFromStart`'s "It BOUNDS NOTHING". |
| `internal/e2e/realclaude/result_trailer_observation_test.go:98-137` | `trailScanResult` / `trailObservation`. `.Line` is OPERATOR-REVIEW-BEFORE-PASTE; `.Trailer` is the decoded pointer. These two are why the carrier exists. |
| `internal/e2e/realclaude/finding_run_gather_test.go:414-445` | `finGatherReadings`' trailer leg. `readings.BoundFrom = obs.BoundFrom` (`:422`), then the carrier fill (`:434-445`). One `trailWaitForTrailer` call, `:421`. |
| `internal/e2e/realclaude/finding_run_gather_test.go:1186-1231` | `TestFinGatherSightingComesFromTheClassifiedPoll` — the premise-check idiom (`:1214-1218`) and the doc paragraph AC4 re-states (`:1193-1198`). |
| `internal/e2e/realclaude/finding_run_gather_test.go:1099-1120` | `TestFinGatherReturnsNoCapturedBytes` — **the shipped precedent for an inline `finGatherInputs` literal** (`:1104-1117`). Also the sweep this row must not weaken. |
| `internal/e2e/realclaude/finding_run_gather_test.go:105-122` | The header's failure-message licence: what a `t.Fatalf` MAY name, and the three things it MAY NEVER name. Binding on every message this row writes. |
| `internal/e2e/realclaude/finding_run_gather_test.go:137-165` | The constants block. `finGatherTrailerWait`'s doc (`:138-142`) is AC3's subject. |
| `internal/e2e/realclaude/finding_run_gather_test.go:852-872` | `finGatherNegativeInputs` — **it seeds the buffer at `:864`**, which is why this row cannot use it. See D1. |
| `internal/e2e/realclaude/background_trigger_probe_test.go:130-131` | `probePollInterval = 200 * time.Millisecond`. Do not introduce a second tick constant. |
| `internal/e2e/realclaude/background_trigger_probe_test.go:721-742` | `probeSyncBuffer`: mutex-guarded, append-only, `Bytes()` returns a copy. This is the `-race` argument. |
| `internal/e2e/realclaude/finding_trailer_evidence_test.go:203-215` | `finTrailerBuild`'s `Bounded: obs.BoundFrom == trailBoundFromMiss` — the one derivation of `lateness_bounded`. This row adds no second. |

---

## Context

`finGatherReadings` returns a `finSighting` carrier whose reason to exist is that
the `trailObservation` itself cannot be handed back: it embeds `trailScanResult`,
whose `.Line` is verbatim model output and whose `.Trailer` is a decoded pointer
carrying `PermissionDenials []json.RawMessage`. The carrier copies scalars out so
neither travels.

`BoundFrom` is the reason the carrier carries a bound at all, and today nothing
measures its interesting value. Every row in `finding_run_gather_test.go`
pre-seeds the buffer before the gather runs, so the first poll hits, `lastMiss`
stays zero, and the bound is always `trailBoundFromStart` — the discriminator
whose own doc says it BOUNDS NOTHING. `trailBoundFromMiss` is reachable only when
a poll misses first, and no row in this file does that.

The gap is stated in the shipped test's own doc
(`TestFinGatherSightingComesFromTheClassifiedPoll`, `:1193-1198`): a second
`trailWaitForTrailer` over a pre-seeded buffer would ALSO report
`trailBoundFromStart`, so that row does not go red against a carrier filled from
a re-scan. This ticket is the row that does.

Nothing about the carrier's shape changes. No call site changes. No production
(non-`_test.go`) file is touched.

---

## Design

One new test function in `finding_run_gather_test.go`, plus three doc edits in
the same file. No new type, no new constant, no new helper, no signature change.

### The row's shape

```
TestFinGatherSightingReportsTheMissBound(t *testing.T)
```

Behaviour, in order:

1. Build `finGatherInputs` inline against a **fresh, unseeded** `probeSyncBuffer`.
   All inputs are built on the test goroutine (`finGatherNeedles(t)` calls
   `t.TempDir()`).
2. Run `finGatherReadings(in)` on a spawned goroutine, storing its three returns
   into variables declared in the test function's scope; close a `done` channel
   on return.
3. On the **test** goroutine: sleep past two poll ticks, then append
   `trailFixtureTrailer + "\n"` to the buffer in a **single `Write` call**,
   `t.Fatalf`-ing on the write error. The single call is load-bearing: `Write`
   holds the mutex for its whole body, so a concurrent poll sees either none or
   all of the line. Two writes would let a poll observe a torn JSON line — which
   `trailScan` handles correctly (it fails to unmarshal and is simply not a match)
   but which makes the tick on which the trailer becomes visible
   non-deterministic.
4. `<-done`.
5. Take the second observation and bind **only its discriminator** (see D2).
6. Assert, in this order: the premise, the miss bound, the agreement, the contrast.

### D1 — the row builds its inputs inline; it must NOT call `finGatherNegativeInputs`

`finGatherNegativeInputs` calls `finGatherSeed(t, stdout, trailFixtureTrailer)` at
`:864`, which writes the trailer into the buffer before returning. That is exactly
the pre-seeding this row exists to avoid: a seeded buffer makes the first poll hit
and the bound `trailBoundFromStart`, i.e. the row would assert the opposite of its
own claim.

Nor should `finGatherNegativeInputs` be refactored to split seeding from
input-building. That helper has two shipped callers, and the ticket's own framing
is that this change "touches no call site."

An inline `finGatherInputs` literal is this file's shipped idiom, not a deviation:
`TestFinGatherReturnsNoCapturedBytes` builds one at `:1104-1117`. The file's
no-struct-literal doctrine is about what `finGatherReadings` **returns**
(`trailGateResult`, `trailAdmitResult`, `tdnReapOutcome`), never about the inputs
a caller hands in.

Field values: mirror `finGatherNegativeInputs`' non-`Stdout` fields
(`finGatherNeedles(t)`, an anchored reap line naming `finGatherNamedPGID`, pinned
`finGatherUnnamedPGID`, `PyryExited: true`) so this row differs from the file's
base fixture in exactly one dimension — the unseeded buffer. That is the package's
own vary-one-dimension idiom (`finGatherInputs` doc, `:200-214`). The attribution
and argv legs run as on every other row; **this row asserts nothing about either**
(see "What the row must not assert on").

### D2 — bind the second observation's discriminator, never the observation

The second observation is a direct `trailWaitForTrailer` call over the **same**
buffer after the gather has returned, reusing `finGatherTrailerWait` for its
timeout. Not a second `finGatherReadings` call — that would re-run the argv and
attribution legs this row asserts nothing about.

The value it returns is a `trailObservation`, so it carries `.Line` and the
decoded pointer — the two things the carrier exists to keep out of a caller's
reach. The file header (`:114-116`) forbids a failure message from naming "the
function-local observation's `Line`".

**Do not bind the observation to a variable at all.** Read the discriminator off
the call directly:

```go
secondBound := trailWaitForTrailer(&stdout, finGatherTrailerWait).BoundFrom
```

This is stronger than the ticket's "read `.BoundFrom` and nothing else off it,"
and it is the file's own stated doctrine: *"prefer the shape that cannot be got
wrong over the discipline that must not be"* (`:211-212`). With no observation in
scope, a later edit **cannot** `%v` one into a message; with one in scope, only
reviewer attention stops it. `trailFixtureTrailer` carries no needle, so a leak
here would trip no sweep and go silently green — which is precisely why the
structural form is the one that matters.

Why the second observation reports `trailBoundFromStart`: by then the trailer is
in the buffer, so the first poll matches, `lastMiss` is still zero, and
`:256-262` takes the start branch. The timeout's value is immaterial for that
reason; passing the file's own constant is what keeps a new literal out.

### D3 — timing, and why no new constant

`probePollInterval` is 200 ms. Polls land at ~0, ~200, ~400 ms (all miss); the
append lands at ~500 ms; the poll at ~600 ms hits, and the bound is measured from
the ~400 ms miss.

Express the delay as a **local sleep past two ticks**, as the shipped row does at
`result_trailer_observation_test.go:539-541`. Do **not** introduce a second tick
constant — the shipped row is the file-crossing precedent.

Wall clock: ~600 ms of poll loop, plus the gather's own argv leg (one `ps` exec
via `pinScanArgv`), plus ~0 for the second observation. This is the file's first
row that costs wall clock, and it is the cost the miss bound requires.

### D4 — goroutine discipline

`finGatherReadings` takes no `*testing.T` (`:414`), so nothing inside it can call
`t.*`. The split is therefore:

- **Test goroutine:** every `t.*` call — `finGatherNeedles(t)`, the
  `probeSyncBuffer.Write` error `t.Fatalf`, and all assertions.
- **Spawned goroutine:** the `finGatherReadings` call and nothing else.

The reason is not style: `probeSyncBuffer.Write` returns an error that must be
reported with `t.Fatalf`, and calling `t.*` from a spawned goroutine after the
test function has returned panics.

`-race` safety: `probeSyncBuffer` is mutex-guarded and append-only and `Bytes()`
returns a copy, so the scan always runs over a private snapshot and the append
races nothing. The three return values are written by the spawned goroutine and
read by the test goroutine only after `<-done`; the channel close is the
happens-before edge. Identical to the shipped row at `:530-550`.

### Assertions (scenarios, not code)

1. **Premise — the poll matched.** `sighting.State != trailSeen` ⇒ `t.Fatalf`.
   Mirrors `:1214-1218`. At any other state the bound is `trailBoundNone` and the
   row would be asserting about a sighting that measured nothing; the premise
   makes that failure name itself instead of arriving as a bare bound mismatch.
2. **The miss bound (AC1a).** `sighting.BoundFrom != trailBoundFromMiss` ⇒
   `t.Fatalf`. The message should say that a non-matching poll was observed before
   the append, so the bound is a real one.
3. **The agreement (AC1b).** `readings.BoundFrom != sighting.BoundFrom` ⇒
   `t.Fatalf`. This extends the shipped agreement check onto the value that
   actually bounds something: one gather call makes one `trailWaitForTrailer` call
   (`:421-422`, `:434`), so a disagreement means the carrier was filled from
   somewhere other than the poll whose scan result was classified.
4. **The contrast (AC2).** `secondBound != trailBoundFromStart` ⇒ `t.Errorf`.
   This is what makes assertion 2 discriminating rather than merely asserted: it
   measures, in this test and over these bytes, what a carrier filled from a
   second scan would have reported instead. `t.Errorf` rather than `t.Fatalf` —
   it is a corroborating measurement, not a premise for anything below it.

### What the row must not assert on

- **The gate and the attribution.** `trailFixtureTrailer`'s gate is
  `trailGateUsable` certifying `"completed"`, so the attribution leg runs. That is
  incidental; asserting on it would restate rows the file already ships.
- **That the carrier is filled independently of the verdict.** It already is, by
  construction: the fill sits at `:434-445` and the attribution guard on
  `Gate.Reason != ""` sits below it at `:459`. If the carrier turned out to be
  filled only under some verdict, that is a defect in the carrier, not a reason to
  change this fixture.
- **`Staleness`.** Already pinned as filled-from-this-sighting by
  `TestFinGatherSightingComesFromTheClassifiedPoll` (`:1226-1230`); do not restate.
  It travels as published evidence, is NOT a classifier input, and must not become
  one. The observation-level claim that a staleness covers the true lateness is
  `TestTrailWaitForTrailer`'s (`result_trailer_observation_test.go:559-562`) and is
  not re-made here.
- **`lateness_bounded`.** Derived from `BoundFrom == trailBoundFromMiss` and
  nothing else at `finding_trailer_evidence_test.go:209-215`. That stays its one
  source; this row adds no second.
- **No new no-captured-bytes sweep.** `TestFinGatherReturnsNoCapturedBytes`
  (`:1099`) already covers all three returns including the carrier. This row must
  not weaken it, and plants nothing — `trailFixtureTrailer` carries no needle.

---

## The three doc edits

### E1 — the constants block (AC3)

`finGatherTrailerWait`'s doc (`:138-142`) currently says NO ROW EVER WAITS IT OUT
*because* "the certifying rows pre-seed the buffer so the first poll hits, and the
non-certifying row seeds an over-long line…". After this ticket the file has a row
that does not pre-seed, so the stated reason no longer describes the file.

The claim itself stays true — no row burns the full 10 s — and must be **re-stated
with the reason it now rests on**, neither deleted nor pasted forward. All three
parts must hold as the PR leaves the file:

1. the pre-seeded rows still hit on the first poll;
2. this row's trailer arrives well inside the wait (~500 ms against 10 s);
3. the aborted row returns immediately, because abortion is monotone.

Keep the existing `(result_trailer_observation_test.go:264-266)` cite on part 3 —
verified correct at `817a0fc` (it is the `case trailAborted` return).

### E2 — the forward pointer (AC4)

`TestFinGatherSightingComesFromTheClassifiedPoll`'s doc at `:1193-1198` hands this
row forward as "**#1310's** along with its wall clock". #1310 was split into this
ticket and #1313 and is CLOSED, so the pointer resolves to a ticket that no longer
exists as work. Re-state that paragraph so it **names this row by name** as what
now proves the miss bound, instead of pointing forward at all. What the shipped row
itself closes — a carrier filled from anywhere other than the poll whose scan
result was classified — is unchanged and stays.

### E3 — the one dangling cite (AC4), measured LAST

`:1253` cites `(:129-135)` for "abortion is monotone". Verified stale at `817a0fc`:
`:129-135` is the tail of the **import block**. The intended target is
`finGatherTrailerWait`'s doc comment inside the constants block, currently
`:138-142`.

**E1 changes that block's own length, so the target moves under this very diff.**
Measure the cite against the file as this PR leaves it — write E1 and the new test
function first, then re-open the file and read the final line numbers off it.
Suggested check before committing:

```bash
grep -n 'finGatherTrailerWait is the trailer poll' -A6 internal/e2e/realclaude/finding_run_gather_test.go
grep -n 'abortion is monotone' internal/e2e/realclaude/finding_run_gather_test.go
```

**Correct that single cite only — do not sweep the file for others.** In
particular:

- `:1272`'s `(:413-423)` (finGatherCases' C4 row) is also stale and belongs to
  **#1313**. Leave it alone.
- `:1242`'s "and it is #1310's" (the past-the-cap discrimination, inside
  `TestFinGatherSightingCarriesTheDecodedScalars`' doc at `:1239-1242`) is
  **#1313's**, not this ticket's. Leave it alone.

This PR therefore lands with one *known* dangling #1310 reference at `:1242` and
one stale cite at `:1272`. Both are #1313's, which is blocked on this ticket and
lands after it. A reviewer should not flag either.

---

## Concurrency model

One spawned goroutine for the lifetime of one test function.

```
test goroutine                          spawned goroutine
──────────────                          ─────────────────
build inputs (t.TempDir via
  finGatherNeedles)
go func ──────────────────────────────► finGatherReadings(in)
                                          └─ trailWaitForTrailer polls
sleep past two ticks (~500 ms)               @0, 200, 400 ms → all miss
buf.Write(trailFixtureTrailer)               @600 ms → hit, lastMiss=400 ms
  (t.Fatalf on error)                        ⇒ BoundFrom = trailBoundFromMiss
                                          └─ argv leg (pinScanArgv → ps)
                                        close(done) ◄── defer
<-done ◄────────────────────────────────
second observation (first poll hits)
  ⇒ trailBoundFromStart
assertions
```

Shutdown: the goroutine has exactly one exit path and closes `done` on a `defer`,
so `<-done` cannot hang past `finGatherTrailerWait` + the argv leg. No context, no
`errgroup` — one goroutine and one channel is the shipped idiom here.

Ordering note: **every assertion sits after `<-done`.** The only `t.*` call that
precedes it is the `buf.Write` error `t.Fatalf` — see the security review's
concurrency finding for why that is left as-is rather than restructured.

---

## Error handling / failure modes

| Mode | Behaviour |
|---|---|
| `probeSyncBuffer.Write` fails | `t.Fatalf` on the test goroutine. Cannot fail in practice (append to a slice), but the error must not be dropped. |
| Goroutine starved past 500 ms on a loaded runner | First poll hits after the append ⇒ `trailBoundFromStart` ⇒ assertion 2 goes **RED**. Flaky-red, never flaky-green. Identical exposure to the shipped row at `result_trailer_observation_test.go:529`, which has carried it since it landed. **Do not add a retry or widen the sleep** — that trades a visible failure for a silent one. |
| Trailer never appended (impossible here) | The gather burns `finGatherTrailerWait` = 10 s, then `State == trailAbsent` ⇒ the premise check `t.Fatalf`s with a named reason. |
| Attribution / argv legs behave unexpectedly | Not this row's subject; no assertion fires. Other rows in the file own those. |

---

## Testing strategy

The row *is* the test. Verification of the change itself:

```bash
go vet -tags e2e_realclaude ./internal/e2e/realclaude/...
go test -race -tags e2e_realclaude ./internal/e2e/realclaude/...
go test -race -tags e2e_realclaude -run '^TestFinGather' -v ./internal/e2e/realclaude/
```

- `make check` and `make build` **never** compile `e2e_realclaude`-tagged files,
  so a PR whose whole diff sits under that tag is a vacuous green. The two
  commands above are the real gate.
- Expect a **PASS/SKIP split**, not all-PASS: the live rows in this package skip
  without credentials. `TestFinGather*` must all PASS.
- Sanity-check the wall clock: the new row should report ~0.6–0.8 s under `-v`.
  A sub-100 ms run means the append beat the first poll and the row is not
  measuring what it claims — it should already be red, but the duration is the
  cheaper tell.

Discrimination check (recommended, not an AC): temporarily pre-seed the buffer
before the `go func`. Assertion 2 must go red with the `trailBoundFromStart`
message. Revert before committing.

---

## Open questions

None blocking. Two notes for the implementer:

1. **Test name.** `TestFinGatherSightingReportsTheMissBound` is the suggestion;
   any `TestFinGather*` name that says "the carrier reports the miss bound" is
   fine. It must start with `TestFinGather` so the header's own run recipe
   (`:13`) picks it up.
2. **Where to place it.** Immediately after
   `TestFinGatherSightingComesFromTheClassifiedPoll` (`:1231`), since E2's
   re-stated paragraph names it and the two read as a pair. Placing it there also
   keeps E3's target — the constants block — above everything this diff adds,
   which is what makes the final cite measurement a single re-read.

---

## Scope self-check

| Red line | Threshold | This spec |
|---|---|---|
| New files | ≤3 | **0** |
| Total written LOC (prod + tests + helpers + log calls + spec edits) | ≤600 | **~200** (one ~80-line test incl. doc, ~25 changed doc lines, this spec) |
| New exported types / interfaces | ≤5 | **0** |
| Consumer call sites updated simultaneously | ≤10 | **0** |
| Acceptance criteria | ≤5 | **4** |
| Distinct error/reject branches | ≤10 | **0 new** |
| Production source files (non-`_test.go`) | <5 | **0** |

Analogue floor: the two nearest prior commits to this file — #1309 (`1a8c2e1`,
357 insertions) and #1302 (`fc33196`, 390 insertions) — each added a new type or
new struct fields plus multiple test functions. This ticket is strictly less work
than either: no new type, no new field, no signature change, no call-site change.
The floor argument does not push it up.

---

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No findings. Two crossings run on this row, both shipped
  and both unchanged: the process table → `pinScanArgv`
  (`process_pin_liveness_test.go:191`), and buffer bytes → `trailScan`. The row
  reads neither one's verbatim output. The argv boundary stays closed by the two
  shipped mechanisms — `finGatherInputs.Pinned` is `[]int` and never `[]reachProc`
  (`:239-241`, `:395-399`), and `trailRunReadings` carries `MatchCount` /
  `RowsScanned` / `ArgvScanErrored` rather than `pinScan.Matches`. This ticket adds
  no field, no return and no call site, so it cannot widen either boundary.

- **[Tokens, secrets, credentials]** No findings, by construction — no token is
  generated, stored, rotated or revoked. The credential channel this file exists to
  keep shut is an operator's `CLAUDE_CODE_OAUTH_TOKEN` / `ANTHROPIC_API_KEY`
  appearing in a **ps column** (verbatim argv) that reaches an artifact destined
  for a public issue (`:250-258`, `:395-399`). This row does not open it: it never
  reads `pinScan.Matches`, and its `Pinned` is `[]int{finGatherUnnamedPGID}`.

- **[File operations]** Not applicable. The row creates no file and concatenates no
  untrusted input into a path. `finGatherNeedles(t)` builds a `t.TempDir()`-based
  path that is a **match pattern handed to a Go-side matcher, never a filesystem
  operand** — `NOTHING IS EVER CREATED AT THAT PATH` (`:156-157`). The spec reuses
  that helper verbatim, so the property is inherited rather than re-decided.

- **[Subprocess / external command execution]** No findings. `pinScanArgv` execs
  `ps` on this row as on every other row of the file; "offline" in this file's
  vocabulary means no live claude, no credentials, no daemon, no turn and no
  `t.Skip` (`:9-11`) — it has never meant no-exec, and an `exec.` grep reading
  clean here would be a false negative. What is *new* is that the exec now runs on
  a spawned goroutine. That is safe and verified, not assumed: `finGatherReadings`
  takes no `*testing.T` (`:414`) and `pinScanArgv(needles []string, exclude
  map[int]string)` takes none either, so nothing on that goroutine can call `t.*`.
  No `sh -c`; no user-controlled argument; environment inherited unchanged from the
  shipped helper.

- **[Cryptographic primitives]** Not applicable. No RNG, no keys, no nonces, no
  hashing. Every comparison the row makes is `==`/`!=` over a closed constant set
  (`trailBoundFrom*`, `trailSeen`); none compares an attacker-controlled value to a
  secret, so `crypto/subtle` has no role.

- **[Network & I/O]** Not applicable — no socket, no HTTP server, no TLS. The one
  input-size cap in play is `bufio.Scanner`'s 64 KiB default, deliberately NOT
  raised (`result_trailer_observation_test.go:161-163`) because reading
  `scanner.Err()` is what separates "no trailer" from "unreadable". Inherited
  unchanged; this row's fixture is ~350 bytes.

- **[Error messages, logs, telemetry]** **SHOULD FIX — resolved in the design at
  D2.** The second observation is a `trailObservation`, which embeds
  `trailScanResult` and therefore carries `.Line` (verbatim model output,
  OPERATOR-REVIEW-BEFORE-PASTE, ~415 of its retained 512 bytes being the model's
  last message) and the decoded `*resultTrailer`. The file header forbids a failure
  message from naming either (`:114-116`). The ticket's own phrasing — *"read
  `.BoundFrom` and nothing else off it"* — leaves that as a **discipline**, and the
  failure would be silent: `trailFixtureTrailer` carries no needle, so a stray `%v`
  would trip no sweep and go green. D2 replaces the discipline with a shape — bind
  the discriminator off the call, never the observation — so no observation exists
  in scope to print. This is the file's own doctrine at `:211-212`.
  Secondary note, no action: printing a whole `sighting` **is** licensed
  (`:118-122`) and `sighting.StopReason` crosses uncapped as the one
  model-influenced field (`:322-327`); on this row that field is the fixture
  constant `"end_turn"`, so nothing model-authored exists to leak. The row plants
  no needle and must not weaken `TestFinGatherReturnsNoCapturedBytes` (`:1099`),
  whose coverage is unchanged because no return or field is added.

- **[Concurrency]** **SHOULD FIX — noted, deliberately not fixed.** The
  `buf.Write` error `t.Fatalf` (step 3) is the one `t.*` call that precedes
  `<-done`. Were it ever to fire, `runtime.Goexit` would end the test goroutine
  while the spawned goroutine is still polling (up to `finGatherTrailerWait`) and
  then running `ps`, leaving it to outlive the test function with `t.TempDir()`'s
  cleanup racing the needle path. It is **unreachable**:
  `probeSyncBuffer.Write` returns `len(p), nil` unconditionally
  (`background_trigger_probe_test.go:727-732`). The shipped row this design mirrors
  has byte-for-byte the same exposure (`result_trailer_observation_test.go:547-549`).
  Recommendation: **do not restructure.** Per Evidence-Based Fix Selection, do not
  ship a defence for a failure mode that cannot occur and has never been observed;
  diverging from the shipped idiom would cost more than it buys. Recorded so
  code-review need not rediscover it.
  Otherwise clean: one goroutine, one exit path, `defer close(done)`; one lock
  (`probeSyncBuffer.mu`), never nested, so no ordering question; no check-then-mutate
  on shared state, because `Bytes()` returns a copy and the scan runs over a private
  snapshot; `close(done)`/`<-done` is the happens-before edge for the three return
  values. The single-`Write` requirement added to the design closes the torn-line
  window that two writes would open.

- **[Threat model alignment]** No findings. This repo has no `docs/threat-model.md`
  and the ticket is not a relay ticket, so `protocol-mobile.md` § Security model
  does not apply. The governing model is the file header's own (`:68-103`): verbatim
  model output and operator credentials must not reach a published artifact. The
  design is aligned on all three of its levers — it plants nothing, it prints
  nothing outside the header's licence, and it opens no new channel. Out of scope
  and named as such: capping `StopReason`, which is explicitly out of scope both
  here and at `finding_trailer_evidence_test.go:114-124`, and which no ticket
  currently owns.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-08-05
