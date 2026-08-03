# #1266 — Result-trailer observation with an honest lateness bound

**Ticket:** [#1266](https://github.com/pyrycode/pyrycode/issues/1266) — split from #1254.
**Consumer:** #1267 (blocked by this ticket; keys its admissibility predicate on the record's decoded `terminal_reason` and on the three-valued state).
**Size:** S. One new file, purely additive, offline-provable.

---

## Files to read first

| Path | What to extract |
|---|---|
| `internal/e2e/realclaude/tool_loop_test.go:185-228` | `resultTrailer` — confirm with your own eyes it has **no** `result` member (Type, Subtype, StopReason, NumTurns, PermissionDenials, IsError, TerminalReason, Usage). That absence is the whole leak argument. Also `parseResultTrailer`'s body: the matching rule to copy verbatim, in the file you must **not** edit. |
| `internal/agentrun/streamjson/emitter.go:454-469` | The pinned on-the-wire field order. `result` is **6th**, `terminal_reason` is **last**. This is why a cap applied to the line destroys the field the consumer branches on, and why the decode must run against the full line. |
| `internal/e2e/realclaude/background_trigger_probe_test.go:718-757` | `probeSyncBuffer` (mutex-guarded, `Bytes()` returns a copy) and `probeWaitForSessionID` — the scan → check-deadline → sleep shape to mirror exactly. |
| `internal/e2e/realclaude/background_trigger_probe_test.go:759-788` | `probeWaitForBashToolUse` — the precedent for "poll, then return the verbatim line bytes". |
| `internal/e2e/realclaude/background_trigger_probe_test.go:131` | `probePollInterval = 200 * time.Millisecond`. Reuse it; do not introduce a second tick constant. |
| `internal/e2e/realclaude/background_reach_probe_test.go:111-125` | `reachMaxCommandBytes = 512`, `reachTruncationMarker`, and the comment stating the threat model the cap serves. Read the reasoning, not just the numbers. |
| `internal/e2e/realclaude/background_reach_probe_test.go:945-950` | `reachCapCommand`'s body — byte-sliced, marker appended. |
| `internal/e2e/realclaude/background_reach_probe_test.go:132-140` | The "three-valued, never collapsed; the fourth value is not a collapse of the three" constant-block precedent. Your two constant blocks follow this shape. |
| `internal/e2e/realclaude/teardown_liveness_test.go:1-61` | File-header discipline: build tag, the explicit "runs offline, no credentials, no `t.Skip`" claim, the `go test -run '^TestX'` recipe line. Yours needs the same, at a fraction of the length. |
| `internal/e2e/realclaude/teardown_liveness_test.go:112-127` | `tdnReapOutcome` — the record-shape discipline: json tags on every field, a `Detail` that says which arm fired and why. |
| `internal/e2e/realclaude/teardown_liveness_test.go:144-166` | `tdnClassifyReapLog`'s contract (pure over bytes, no `*testing.T`, never fails a test) and line 166 — the precedent for capping a captured line **as it enters** the record. |
| `internal/e2e/realclaude/teardown_liveness_test.go:325-345` | Closed-value-space constant style: kebab-case strings, a positive allowlist, each constant carrying the argument for why it is not a collapse of its neighbour. |
| `internal/e2e/realclaude/process_pin_liveness_test.go:225-232` | `pinStateColumns`' prohibition. Read it to confirm it is **not engaged here** — this ticket spawns no process and reads no process table. |

Not code, but read before writing prose: `docs/specs/architecture/1251-teardown-liveness-probe.md` (record-shape discipline for this family).

---

## Context

A downstream probe asks: *was a backgrounded command still running when pyry declared the turn finished?* The only available signal for "pyry declared the turn finished" is the `{"type":"result",...}` trailer on pyry's stdout, and the rig learns of it by polling on a 200 ms tick. The trailer was therefore written some time **before** the poll that first saw it.

A record that says only "the trailer appeared" invites its reader to treat the observation instant as the write instant. Every liveness reading taken at that instant then inherits an unstated error. This ticket ships the observation and the bound; deciding what a run's observations *mean* is #1267's work.

Two gaps make a naive version wrong:

1. **A bare duration is sometimes a bound and sometimes not.** The bound is the elapsed time since the last poll that did **not** match. If the very first poll already matches, no non-matching poll was ever observed, and the only duration available is measured from the loop's start — which bounds nothing, because the write may precede the loop entirely. A record that cannot say which of the two it holds reproduces, one level up, the exact defect this ticket exists to close.

2. **`parseResultTrailer` discards `scanner.Err()`.** A line past `bufio.Scanner`'s 64 KiB default returns the *same* `errors.New("no type:result line in stdout")` as a genuinely absent trailer. The trailer's `result` field carries the last assistant message verbatim, so this is not hypothetical — the function's own doc comment anticipates it. A consumer that treats "no trailer" as a named nothing-was-measured outcome would file the instrument's own breakage under it.

### The cap and the consumer must not compete

These records are pasted into public issues, so the retained line is capped — same discipline as `teardown_liveness_test.go:166`. But `reachMaxCommandBytes` is 512 and `result` sits **sixth** on a pinned wire order, so a 512-byte cap truncates *inside* the assistant message and destroys every field after it. `terminal_reason` is **last** on the wire and is therefore the first casualty — and it is precisely the field #1267 branches on.

The resolution costs nothing and is already in the package: **decode the full line, then cap only the verbatim copy.** `resultTrailer` has no `result` member (verified — zero hits), so the decoded value structurally *cannot* carry the assistant payload. The cap then degrades human-readable evidence only, never a machine-readable field, and the leak surface stays closed by construction rather than by a byte count.

---

## Design

One new file: **`internal/e2e/realclaude/result_trailer_observation_test.go`**, `//go:build e2e_realclaude`, package `realclaude`. Nothing else in the repository is edited.

Identifier prefix **`trail*`** — re-verified free at spec time: `git grep -nP '\btrail[A-Z]' -- internal/ cmd/` returns 0 against a `tdn[A-Z]` control returning 489, so the recipe is known to find things. Use `-P`; this repo's `git grep -E` does not support `\b` and would report 0 for a symbol with hundreds.

### Two value spaces, both closed, neither collapsible

```go
// What a scan over stdout found. Three states, never collapsed.
const (
	trailSeen    = "trailer-seen"
	trailAbsent  = "trailer-absent"
	trailAborted = "scan-aborted"
)

// What the staleness bound was measured FROM. The third value is not a
// collapse of the first two: it is the record saying no bound exists.
const (
	trailBoundFromMiss  = "since-last-non-matching-poll"
	trailBoundFromStart = "since-poll-start"
	trailBoundNone      = "no-bound-measured"
)
```

Every constant is a non-empty distinct string. That is load-bearing, not cosmetic:

- `trailAbsent` is not the zero value of a `State` field, so a record nobody filled in reads as *invalid*, never as *the trailer never appeared*.
- `trailBoundFromMiss` is not the zero value of a `BoundFrom` field, so a record carrying no bound can never read as one bounded by a non-matching poll. This is AC1's central demand and it is enforced by a test (§ Testing, T1).
- `trailBoundFromStart` names a duration that **does not bound the lateness**. A consumer must be able to tell that case apart from a genuine bound by reading the discriminator alone — never by reading prose.

### Two types: a scan has no clock

A scan over bytes cannot know when it ran, and a value that carries a zero `time.Time` in a field named "the observation instant" is a lie the type system can prevent. So the scan's product and the observation are different types, composed by embedding.

```go
// trailScanResult is what a pure scan over stdout bytes produces. It carries
// no instant and no bound, because a pure function has no clock.
type trailScanResult struct {
	State   string         `json:"state"`
	Line    string         `json:"trailer_line,omitempty"` // capped; empty unless State == trailSeen
	Trailer *resultTrailer `json:"trailer,omitempty"`      // nil unless State == trailSeen
	Detail  string         `json:"detail"`
}

// trailObservation is a complete observation: a scan result plus when it was
// taken and how late it may be.
type trailObservation struct {
	trailScanResult
	ObservedAt time.Time     `json:"observed_at"`
	Staleness  time.Duration `json:"staleness_ns"`
	BoundFrom  string        `json:"staleness_from"`
}
```

Notes on the shape, each of which is a decision and not an accident:

- **`Trailer` is a pointer, deliberately.** It is nil on `trailAbsent` and `trailAborted`. A consumer that dereferences it without first checking `State` panics **loudly** — which is strictly better than a value type handing it `TerminalReason == ""` and letting an empty terminal reason pass as a real one. That silent-zero collapse is the same defect AC1 forbids one level up. `parseResultTrailer` already returns `*resultTrailer`, so this is the package's existing shape.
- **`Staleness` is a `time.Duration` with the unit in the json key** (`staleness_ns` — `encoding/json` renders a `Duration` as an integer nanosecond count). A reader cannot misread the unit. Human-readable rendering belongs to whoever publishes, i.e. #1267.
- **The embedded type is unexported with exported fields**, so `encoding/json` inlines it: a marshalled `trailObservation` is one flat object. `obs.State`, `obs.Line`, `obs.Trailer` are promoted, so the consumer reads them without naming the embedded type.
- **`time.Time` round-trip discipline** (`docs/PROJECT-MEMORY.md`): monotonic readings strip on marshal. Nothing here round-trips the record through JSON and compares instants — do not add a test that does, and if you ever do, compare with `time.Time.Equal`, never `==`.

### `trailScan` — pure over bytes

```go
// trailScan answers "is there a result trailer in these stdout bytes?" over a
// snapshot of them. Pure: no exec, no file read, no clock, no *testing.T.
func trailScan(stdout []byte) trailScanResult
```

Behaviour, in order:

1. `bufio.Scanner` over `bytes.NewReader(stdout)`, default buffer — **not** raised. Per line, `json.Unmarshal` into a `resultTrailer`; on error, `continue`; on `tr.Type == "result"`, this is the match. This is `parseResultTrailer`'s matching rule, character for character. A divergence here would make this a *second* classifier of the same bytes, which the family forbids.
2. On match, **return immediately**: `State = trailSeen`, `Trailer` = the decoded value (decoded from the **full** line — the scanner's token, uncapped), `Line = reachCapCommand(string(scanner.Bytes()))`. The cap is applied to the copy only, at the moment it enters the record.
3. Loop ended with no match. If `scanner.Err() != nil` → `State = trailAborted`, `Detail` embeds the error verbatim. **This check comes only after the loop**, which is what makes step 2's early return correct.
4. Otherwise → `State = trailAbsent`.

The ordering of 2 and 3 has an observable consequence worth stating in the doc comment: **an over-long line that appears *after* the trailer never affects the answer**, because the scan has already returned. Only an over-long line at or before the trailer's position can abort. Test T2 pins this.

`Detail` is built with `reachCapCommand(fmt.Sprintf(...))` at each of the three return sites — one line each, no new helper, and deliberately **not** a call to `tdnDetail`: the `trail*` family stays out of the `tdn*` teardown classifier's reach, which is the whole point of a distinct prefix.

**Deliberately not built:** the scan does not count how many `type:result` lines it saw. #1235's anti-first-match discipline (`pinScan.Matches` is a slice because nothing there resolves to "the" one) does not transfer: the poll path returns at the first match *by construction* — it cannot wait to learn whether a second arrives — so a count would be a lower bound no consumer could act on. #1267 keys on `terminal_reason`, not on multiplicity. Nine existing call sites have taken the first match without incident. Revisit only if a second trailer line is ever observed.

### `trailWaitForTrailer` — the poll, and the stamping order that makes the bound honest

```go
// trailWaitForTrailer polls stdout until a result trailer is visible, the scan
// aborts, or the timeout expires. It never fails a test.
func trailWaitForTrailer(stdout *probeSyncBuffer, timeout time.Duration) trailObservation
```

Loop shape, mirroring `probeWaitForSessionID`:

1. `start := time.Now()`; `lastMiss` is the zero `time.Time`, meaning *no non-matching poll has been observed*.
2. Each iteration: **stamp `now := time.Now()` first, then read `stdout.Bytes()`, then `trailScan`.**
3. `trailSeen` → `ObservedAt = now`; if `lastMiss.IsZero()` then `Staleness = now.Sub(start)`, `BoundFrom = trailBoundFromStart`; else `Staleness = now.Sub(lastMiss)`, `BoundFrom = trailBoundFromMiss`. Return.
4. `trailAborted` → return immediately with `ObservedAt = now`, `Staleness = 0`, `BoundFrom = trailBoundNone`.
5. `trailAbsent` → `lastMiss = now`. If `now` is at or past the deadline, return with `ObservedAt = now`, `Staleness = 0`, `BoundFrom = trailBoundNone`. Otherwise `time.Sleep(probePollInterval)` and loop.

**The stamp-before-read ordering is the whole correctness argument and must be doc-commented as such.** A miss poll stamped *after* its read can postdate the append that made the trailer visible; the resulting `now - lastMiss` would then be *smaller* than the true lateness — a non-bound wearing a bound's label, which is exactly the defect this ticket exists to close. Stamped before the read, `lastMiss ≤ readInstant ≤ visibilityInstant` holds unconditionally, so `Staleness ≥ ObservedAt - visibilityInstant` holds unconditionally. Test T3 is that inequality's regression guard.

**Returning immediately on `trailAborted` is sound because abortion is monotone.** `probeSyncBuffer.Write` only appends. If a scan over prefix *P* aborted, the too-long line sits at a fixed offset in *P* with fixed bytes (or is a trailing partial line already past 64 KiB, which appending can only lengthen). Every scan over any *P′* ⊇ *P* reaches the same line and aborts identically. Waiting out the deadline could not change the answer.

**What the bound bounds — and what it does not.** There are two unmeasured gaps between pyry writing the trailer and this record's instant:

```
pyry writes trailer ──(gap A: os/exec copier)──> visible in probeSyncBuffer ──(gap B: poll tick)──> ObservedAt
                                                        └──────────── Staleness bounds gap B only ────────────┘
```

`Staleness` bounds **gap B only**. Gap A is unmeasured and unbounded by this record. The doc comment on `Staleness` must say this in those terms: the field bounds how long the trailer had been *visible in the buffer*, never how long ago pyry wrote it. A record that claimed the latter would be the same category of lie the discriminator exists to prevent.

### Threading and the file header

The helper is called from the test's own goroutine while `os/exec`'s copier writes into the same `probeSyncBuffer`. That is the only shared state, it is mutex-guarded, and `Bytes()` returns a copy — so `trailScan` never reads a buffer being appended to. The file header states the offline claim in this family's style, at ~25 lines, not 57: build tag, "no live claude, no credentials, no env gate, no `t.Skip`", the run recipe, and the two-gap diagram above.

---

## Error handling

| Condition | Outcome | Why not the neighbouring value |
|---|---|---|
| Line > 64 KiB at or before the trailer's position | `trailAborted`, `Detail` carries `scanner.Err()` verbatim | Reporting `trailAbsent` files the instrument's own breakage under "pyry never finished the turn". This is the gap in `parseResultTrailer` that motivates the ticket. |
| Line > 64 KiB strictly after the trailer | `trailSeen` | The scan returned at the match; the tail was never read. The answer is about the trailer, and the trailer was read whole. |
| Malformed / non-JSON lines | skipped, scan continues | Ordinary input. A partial trailing line mid-write fails to unmarshal and is simply not a match *yet*. |
| No trailer, clean scan, deadline expired | `trailAbsent`, `BoundFrom = trailBoundNone` | Distinct from `trailAborted` by `scanner.Err()`, and distinct from `trailSeen` by not being its zero value. |
| Trailer present on the first poll | `trailSeen`, `BoundFrom = trailBoundFromStart` | The duration is real but bounds nothing — the write may precede the loop entirely. The discriminator, not prose, is what tells a consumer this. |

`trailScan` and `trailWaitForTrailer` take no `*testing.T` and never fail a test — the same contract as `tdnClassifyReapLog`, `pinReadState` and `fifoLiveRead`. An instrument failure observed mid-turn is a datum to publish, not a reason to abort the turn, and it is what lets every arm be driven offline.

---

## Testing strategy

All test functions carry the `TestTrail` prefix so `-run '^TestTrail'` is a zero-SKIP suite. Every case is offline: synthetic `[]byte` and synthetic `probeSyncBuffer`, no credentials, no live claude, no `t.Skip` anywhere in the file.

**T1 — `TestTrailConstantsAreClosed`.** AC1's structural claim, executable:
- each of the six constants is non-empty (so no zero-valued field reads as any of them);
- the three state constants are pairwise distinct; the three bound-origin constants are pairwise distinct.
This test fails the moment someone "simplifies" `trailBoundNone` to `""`.

**T2 — `TestTrailScan`**, table-driven, pure inputs:
- a buffer whose only line is an ordinary trailer → `trailSeen`; `Trailer.TerminalReason`, `Subtype` and `IsError` all match the fixture; `Line` equals the input line uncapped.
- a buffer of ordinary non-trailer stream-json lines → `trailAbsent`, `Trailer == nil`, `Line == ""`. **This is the control assertion AC5 requires** — without it, "aborted" could be the answer to everything.
- a single trailer line whose `result` field is padded past 64 KiB → `trailAborted`, `Trailer == nil`, `Detail` non-empty and containing the scanner error's text. Never `trailAbsent`.
- an ordinary trailer line **followed by** a >64 KiB line → `trailSeen`. Pins the early-return-before-`Err()` ordering.
- **the cap case:** a trailer whose `result` is `strings.Repeat("x", 2000) + needle`, with `terminal_reason` present (last on the wire, ~2 KiB past the cap). Assert: `State == trailSeen`; `len(Line) == reachMaxCommandBytes + len(reachTruncationMarker)`; `strings.HasSuffix(Line, reachTruncationMarker)`; `Trailer.TerminalReason` equals the fixture's value — i.e. **a field living far past the cap survived intact**; and `!strings.Contains(Line, needle)`.
- **the leak assertion:** marshal the whole scan result with `json.Marshal` and assert the needle appears nowhere in the bytes. The needle can only reach the record through `Line`, and `Line` is capped at 512 bytes, so this is deterministic — and it is the executable form of "the decoded value structurally cannot carry the assistant payload".

**T3 — `TestTrailWaitForTrailer`**, three subtests, each driving one arm:
- *late append, miss-derived bound.* Start `trailWaitForTrailer` over an empty buffer in a goroutine; sleep past two poll ticks (≈500 ms with `probePollInterval` at 200 ms) so at least one non-matching poll is certainly observed; stamp `appendAt := time.Now()` **before** writing the trailer line plus `"\n"`; join the goroutine. Assert `State == trailSeen`, `BoundFrom == trailBoundFromMiss`, and — AC5's headline — `obs.Staleness >= obs.ObservedAt.Sub(appendAt)`. Stamping `appendAt` before the write makes the computed lateness slightly *larger* than the truth, so the assertion is strictly harder than the real invariant and cannot pass by luck. Also assert `Trailer.TerminalReason` decodes, proving the record is usable at the instant it reports.
- *pre-filled buffer, start-derived bound.* Write the trailer, then call the helper synchronously. Assert `State == trailSeen`, `BoundFrom == trailBoundFromStart`, `Staleness >= 0`. A consumer distinguishes this from the previous subtest by the discriminator alone.
- *no trailer, deadline expires.* Empty buffer, a short timeout (~600 ms). Assert `State == trailAbsent`, `BoundFrom == trailBoundNone`, `Staleness == 0`, `Trailer == nil`.

The first subtest writes to the buffer from one goroutine while the helper polls from another, so `-race` covers the `probeSyncBuffer` boundary this design depends on. Join the goroutine before asserting; nothing may outlive the subtest.

**Verification recipe** (run all four; the ticket is not done until each is green):

```bash
go vet -tags e2e_realclaude ./internal/e2e/realclaude/
gofmt -l internal/e2e/realclaude/result_trailer_observation_test.go   # must print NOTHING
go test -race -tags e2e_realclaude -run '^TestTrail' -v ./internal/e2e/realclaude/
git diff --name-only origin/main   # must list exactly the new file + this spec
```

`gofmt -l` is dirty on `main` for three unrelated files in this package — scope the check to the new file, and do not "fix" the others. In the `-v` output, grep for both `--- PASS` and `--- SKIP`: a `TestTrail` subtest that skips is a failure of the offline claim, not a pass.

---

## Open questions

1. **Should `Staleness` also be carried in a human-readable form?** Deferred. The unit is in the json key and #1267 owns publication; adding a second representation of one number invites the two to disagree.
2. **Is 512 bytes the right cap for a trailer line specifically?** `reachMaxCommandBytes` was calibrated for a `ps` argv row. See § Security review, [Tokens] — the number is inherited on purpose and changing it is out of scope for this ticket.
3. **Does gap A (pyry's write → the copier's append) deserve its own instrument?** Not here. It would need a timestamp inside pyry's own emitter, which is a production change this probe-family ticket must not make. If a downstream finding ever turns on gap A, it needs its own ticket.

## Out of scope

Classifying a run's observations into an outcome (#1267). Live staging, artifact publication, and the finding itself (the live probes downstream of #1267). Editing `parseResultTrailer` or any of its nine call sites. Any `ps` read, and therefore any `command`/`args`/`comm` column — `process_pin_liveness_test.go:225-232`'s prohibition is not engaged by this design because it spawns no process at all.

Per the architect's standing rule, `docs/knowledge/codebase/1266.md` is **not** a deliverable of this ticket — the documentation phase writes it from this spec plus the merged diff.

---

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** The design has exactly one boundary, and it is a single function: `trailScan`. Untrusted bytes (a subprocess's stdout, which contains model-generated text verbatim) cross into trusted state there and nowhere else. Downstream holds only two things: a `*resultTrailer` whose field set is fixed and closed, and a `Line` string bounded at 512 bytes + marker. **The adversarial question is whether assistant-controlled text can forge a trailer line.** It cannot: the assistant's output reaches stdout inside the trailer's `result` field as a JSON string, and `encoding/json` escapes a newline as `\n`, so injected text can never begin its own line. This is a property of stream-json's escaping, not of this scan — recorded here because a future change that emitted an unescaped payload would make forgery possible, and this is the file that would then be wrong. No finding.

- **[Tokens, secrets, credentials]** SHOULD FIX, accepted and documented. The trailer's `result` field is the last assistant message verbatim. With `result` sixth on the wire and the cap at 512 bytes, the retained `Line` contains roughly 95 bytes of JSON scaffolding followed by **~415 bytes of verbatim model output**. If a turn's last assistant message echoed an environment dump, those bytes could carry the operator's `CLAUDE_CODE_OAUTH_TOKEN` / `ANTHROPIC_API_KEY` prefix. This is inherited, not introduced: `reachMaxCommandBytes`' own comment (`background_reach_probe_test.go:117-123`) already states the package's position — the cap "bounds the blast radius to a skim" rather than redacting. Raising the bar is out of scope because AC2 pins the cap to `reachCapCommand` and the existing marker, and inventing a second cap would fork the discipline. Three things make the residual risk acceptable and must appear in the code: (a) the decoded `Trailer` **structurally cannot** carry the payload — no `result` member — so it is safe to publish unreviewed, and T2's `json.Marshal` needle assertion proves it; (b) `Line` carries a truncation marker, so a reader always knows it is partial; (c) the doc comment on `Line` must state that it holds verbatim model output and is **operator-review-before-paste**, while `Trailer` is not. This ticket publishes nothing, so the actual publish decision is #1267's and this finding is the input to it.

- **[File operations]** Not applicable by design, not by omission: neither function opens, stats, creates or names a path. `trailScan` takes `[]byte`; `trailWaitForTrailer` takes an in-memory `*probeSyncBuffer`. No traversal surface, no TOCTOU, no mode question.

- **[Subprocess / external command execution]** Not applicable by design: this ticket spawns nothing. There is no `exec.Command`, no `sh -c`, no signal handling, no environment inheritance decision. This is also why `pinStateColumns`' prohibition on argv/environment columns is not engaged — the safe design here is, as in that file, *not having the capability*.

- **[Cryptographic primitives]** Not applicable: no randomness, no hashing, no comparison against a secret. Nothing in the design is security-relevant in a way an RNG or a constant-time compare would address.

- **[Network & I/O]** The input size limit is `bufio.Scanner`'s 64 KiB default, and it is **deliberately not raised** — raising it only moves the threshold, whereas reading `scanner.Err()` is what separates "no trailer" from "unreadable". Beyond the cap, an over-long line is refused rather than buffered, so a hostile-sized stdout line cannot inflate this scan's memory: `bufio` stops at 64 KiB per token. The unbounded surface is `probeSyncBuffer` itself, which grows with the child's whole stdout — pre-existing, owned by whichever probe spawns pyry, and untouched here. Cost note, not a finding: the scan is O(buffer) per 200 ms tick, so a long run costs O(n · t / 200 ms). That is a probe-lifetime cost on a synthetic tick, not a remotely reachable DoS surface.

- **[Error messages, logs, telemetry]** Every `Detail` string is built through `reachCapCommand`, so no error path can emit an uncapped string. The one place an error's own text enters a record is the `trailAborted` arm, which embeds `scanner.Err()` — and over a `bytes.Reader`, which never fails, the only reachable error is `bufio.ErrTooLong`, whose message is the fixed string `"bufio.Scanner: token too long"` and carries **none of the input bytes**. So the abort path cannot leak the over-long line it refused to read. The code must still cap it rather than rely on that, because the guarantee is `bufio`'s and not this file's. No telemetry, no metrics, no logging.

- **[Concurrency]** One piece of shared state: the `*probeSyncBuffer`, mutex-guarded, whose `Bytes()` returns a copy — so `trailScan` always operates on a private snapshot and never reads memory being appended to. Exactly one lock, so no ordering question. No goroutine is spawned by either function, so no lifecycle or leak question in production code; T3's first subtest spawns one and must join it before asserting. `-race` is mandatory in the verification recipe precisely because that subtest exercises the write-while-polling boundary. TOCTOU is not merely absent but is the subject of the design: the stamp-before-read ordering exists because the check (a miss poll) and the state it describes (buffer contents) are separated in time, and the record is honest only if the stamp precedes the read.

- **[Threat model alignment]** The package-level rules this design is measured against are `background_reach_probe_test.go`'s redaction rule (cap every retained operator-visible string — followed) and `process_pin_liveness_test.go`'s environment-column prohibition (not engaged; no process table is read). No relay or network threat in `docs/protocol-mobile.md` is reachable from a test-only, offline scan over in-memory bytes. The one threat this ticket *adds* to the family's surface is the ~415 bytes of verbatim model output in `Line`, named above under [Tokens] and handed to #1267 as an explicit input to its publication decision.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-08-03
