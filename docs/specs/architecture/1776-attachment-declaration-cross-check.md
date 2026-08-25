# #1776 — Refuse an attachment declaration whose chunk count and size cannot both be true

**Ticket:** [#1776](https://github.com/pyrycode/pyrycode/issues/1776) · `size:s` · `security-sensitive`
**Package:** `internal/attachments` (new file), `internal/protocol` (doc comments only)

---

## Files to read first

| Where | Symbol | What to extract |
|---|---|---|
| `internal/protocol/attachments.go` | `MaxAttachmentChunkBytes` | The bound (45000) and its `THE CROSS-CHECK` paragraph — **doc site A**. Note the units: raw, pre-base64 bytes, which is the same unit `Size` counts, so the division is raw-over-raw. |
| `internal/protocol/attachments.go` | `AttachmentChunkPayload` | The `NEVER ALLOCATE FROM A CLAIM` block — **doc site B** — and the field contracts for `TotalChunks` (`int`) and `Size` (`int64`). The type asymmetry drives the comparison rule in § Design. |
| `internal/attachments/accumulator.go` | package doc, `Accumulator`, `NewAccumulator`, `Add`, `Assemble` | The six `#1767` mentions to re-point (**doc sites C, D, E** plus two more), and the house doc-comment voice the new file matches. |
| `internal/attachments/accumulator.go` | `Add`, `reject` | The sentinel-wrapping idiom the new refusals copy: `fmt.Errorf("attachments: … %d …: %w", …, ErrX)`, numbers only, never a client-supplied string. |
| `internal/attachments/filename.go` | `SanitizeFilename` | The nearest structural sibling — one exported symbol in its own file, doc block stating the postcondition **and** the non-goals. `admission.go` mirrors this shape. |
| `internal/attachments/accumulator_test.go` | `TestAccumulator_FramingFaults_RejectAndDiscard` | The table-driven refusal-test idiom (`errors.Is`, `t.Parallel()`) the new table copies. |
| `internal/attachments/accumulator_test.go` | `TestAccumulator_ZeroDeclaredCount_IsIncomplete`, `testFixture` | The test AC 5 requires to survive **unchanged**; `testFixture`'s comment block carries the seventh `#1767` mention. |
| `internal/attachments/filename_test.go` | `TestSanitizeFilename` | The table shape for a pure function with no accumulator state — the closer model for this ticket's test than the accumulator tables. |
| `docs/protocol-mobile.md` | § Attachments, **Chunking (the sender's obligation)** | The published `total_chunks = max(1, ceil(size / 45000))` form this slice adopts verbatim. **No edit** — it already says everything this slice enforces. |
| `docs/knowledge/features/attachments-package.md` | § "Mutation-testing lessons", § "Blocked family" | The two overlay traps the § Testing measurement must avoid (a mutant that breaks the build scores as *green*), and the description of #1767's four bounds, of which this slice ships exactly one. |
| `CODING-STYLE.md` | § "Comments — Citing Other Code" | Every comment this ticket writes names a symbol. `make cite-guard` is diff-scoped and fails on any line citation the branch adds. |

---

## Context

On the inbound leg every field of `attachment_chunk` is an unverified claim. `AttachmentChunkPayload`'s `NEVER ALLOCATE FROM A CLAIM` block names the cheapest attack the frame offers: a claimed `TotalChunks` of 2^31-1 drives a multi-gigabyte allocation from one ~60 KB frame. The defence is free from the **first** chunk, before a byte is accumulated — `total_chunks` and `size` cross-check each other through the published per-chunk bound.

The form is already settled and this slice enforces rather than re-opens it. `docs/protocol-mobile.md` § Attachments publishes the sender's obligation as `total_chunks = max(1, ceil(size / 45000))`; `docs/specs/architecture/1751-attachment-transfer-contract.md` § Open questions 2 calls that "the one rule that satisfies a strict receiver guard and is free for a client to obey" and requires the receiver to accept it, zero-byte case included. Adopting it decides the two consequences that doc left hanging: only the **equality** bounds `total_chunks` from above (a maximum chunk size yields only a *lower* bound on an honest count, so `>=` caps nothing), and the published `max(1, …)` is what resolves `size == 0`.

Today the package has no admission layer at all. `NewAccumulator`'s doc records that it "cannot fail: refusing an implausible declaration is the admission layer's job"; `Assemble`'s that refusing a declared count below 1 is "a decision to make once, at admission". Both point at #1767, which closed when it was split into this family. This slice is the half that answers them; the per-upload byte bound is #1777's and the concurrency bound plus the in-flight registry are #1778's.

**No ADR.** The decision this slice makes is already published — in `docs/protocol-mobile.md` § Attachments and in `MaxAttachmentChunkBytes`' own doc — and its rationale belongs beside the constant it rests on, which is where AC 5 puts it. A separate decision record would duplicate a published contract rather than record a new cross-cutting choice.

---

## Design

### New file: `internal/attachments/admission.go`

Two exported symbols. Nothing else is added, and no existing symbol changes shape.

```go
// ErrInvalidDeclaration reports a transfer declaration this receiver refuses
// before an Accumulator exists. Wrapped with the offending numbers by every
// refusal; matched with errors.Is, never by string.
var ErrInvalidDeclaration = errors.New("attachments: transfer declaration is not admissible")

// CheckDeclaration answers whether a first chunk's declared total_chunks and
// size can both be true. nil admits the transfer; every refusal wraps
// ErrInvalidDeclaration.
func CheckDeclaration(totalChunks int, size int64) error
```

**Why scalars, not `protocol.AttachmentChunkPayload`.** `NewAccumulator` already takes the three declared facts as scalars rather than the payload, and admission's output feeds straight into it. Taking the payload would also invite a reader to think the other six fields are checked here; they are not.

**Why this package and not `internal/protocol`.** `protocol` ships wire vocabulary with no validator — that is the posture the const block and `AttachmentChunkPayload` both state explicitly. The check is receiver policy, so it lives with the receiver.

**Why a new sentinel and not `ErrTotalChunksMismatch`.** That one means "this chunk's total disagrees with the count the transfer was *admitted under*" and is raised by `Add` after admission. This is a refusal of the admission itself, before any count exists to disagree with. One sentinel covers both refusals below — #1744 maps it to `attachment.invalid_chunk` at the dispatch site via `errors.Is`, and this slice emits no wire code, no log call and touches no disk.

### The two checks, in a fixed order

1. **`size < 0` → refuse.** Its own check, and load-bearing: measured, `max(1, ceil(-1 / 45000))` is **1** under either natural ceiling form, and so is `max(1, ceil(math.MinInt64 / 45000))`. The pair `size` −1 / `total_chunks` 1 therefore passes the cross-check unaided. The cross-check cannot substitute for this guard.
2. **`totalChunks != max(1, ceil(size / protocol.MaxAttachmentChunkBytes))` → refuse.**

The order affects only which wrapped message a doubly-bad declaration gets — the sentinel is the same either way, so unlike `Add`'s ordering (which `TestAccumulator_TotalChunksMismatchOutranksIndexOutOfRange` pins because the sentinels differ) **this order is not test-pinnable, and no test should claim to pin it.** Put the negative guard first anyway: it is what makes the arithmetic below reason about a non-negative operand.

### The ceiling, and the one form that must not be written

The ceiling is division-then-remainder, never `(size + bound - 1) / bound`:

```go
want := size / protocol.MaxAttachmentChunkBytes
if size%protocol.MaxAttachmentChunkBytes != 0 {
    want++
}
if want < 1 { // the published max(1, …); reachable only at size == 0
    want = 1
}
if int64(totalChunks) != want {
    return fmt.Errorf(…, ErrInvalidDeclaration)
}
```

Four properties, each load-bearing:

- **No overflow.** `size / bound` is at most `MaxInt64 / 45000` and the increment cannot overflow from there. The `size + bound - 1` numerator wraps for every `size` within 44999 of `math.MaxInt64`: measured, `(math.MaxInt64 + 44999) / 45000` evaluates to **−204963823041216**, which `max(1, …)` launders back to **1** — so `size` `math.MaxInt64` / `total_chunks` 1 is *admitted* by that form. The correct answer is **204963823041218** (AC 3). Go's `max` builtin is fine as the spelling of the clamp; the banned thing is the numerator, not the clamp. Note that `(math.MaxInt64 + 44999)` written over *constants* is a compile error, so the wrap only appears once `size` is a variable — which is exactly how the guard receives it.
- **Widen, never narrow.** Compare `int64(totalChunks) != want`. `int(want) != totalChunks` narrows on a platform where `int` is 32 bits and re-introduces a wrap in the comparison itself.
- **The clamp subsumes `total_chunks >= 1`.** `want` is always ≥ 1, so equality already refuses 0 and every negative count. No separate lower-bound check, and adding one would be dead code.
- **Read `protocol.MaxAttachmentChunkBytes`, never a local 45000.** A copied literal is a second place the bound can drift.

### What this slice deliberately does not check

- **No stride check.** This checks the two declared *numbers*, not how the client actually cut the file. A client whose stride differs but whose count happens to conform (45001 bytes at 32 KiB is 2 chunks either way) is admitted here and assembles normally — `Assemble` compares the assembled length and digest against the declaration, and nothing downstream needs the stride. A client whose stride makes the count differ (100000 bytes at 32 KiB declares 4 where the form says 3) is refused, and that is not a regression: the published contract already obliges every chunk but the last to carry exactly 45000 raw bytes.
- **No absolute size cap.** A declaration that is arithmetically conforming but enormous — `size` `math.MaxInt64` with its matching count 204963823041218 — is **admitted here** and refused by #1777's per-upload byte bound. Test row A5 exists to keep it that way.
- **No unexported helper for the ceiling.** Every branch of it is reachable through `CheckDeclaration`'s own table (rows A1, A2, A3, N8, A5), so a helper would add a symbol without adding a pin. That is the converse of #1772's `truncateToBytes` lesson, where an earlier pipeline stage made the later one unfalsifiable from outside.

---

## Concurrency model

None. `CheckDeclaration` is pure and stateless — no filesystem, no socket, no logger, no lock, no goroutine — so it is safe to call from any goroutine, exactly like `SanitizeFilename` and unlike `Accumulator`, which its own doc records as fed serially by one session's `appFrameWorker`. Say so in the function's doc block. Synchronising the in-flight registry is #1778's.

---

## Error handling

- Every refusal returns `fmt.Errorf(… : %w, ErrInvalidDeclaration)` carrying the offending numbers — `total_chunks`, `size`, and for the cross-check the count the form requires and the bound it was computed at. Callers match with `errors.Is`.
- **Numbers only.** No `Filename`, no `SHA256`, no `Data`, no `AttachmentID` reaches an error string here — the same rule `reject`'s doc states and honours. This function never sees those fields, which is one more reason the signature takes scalars.
- No wire code. #1744 maps this sentinel to `attachment.invalid_chunk`, whose published row leads with "framing claims are inconsistent or out of range". Do not invent a new code and do not import `codes.go`.
- No log call. The package makes zero, and the dispatch site is the only place that knows the attachment id and conn id worth logging.

---

## Doc-comment edits (AC 5)

Seven `#1767` mentions exist today — six in `accumulator.go`, one in `accumulator_test.go`. All seven must go, and `grep -rn '#1767' internal/attachments/` must come back empty.

**The five sites AC 5 names.** Prose is the developer's; the required content is below.

| # | Where | Must end up saying |
|---|---|---|
| A | `internal/protocol/attachments.go` → `MaxAttachmentChunkBytes`, the `THE CROSS-CHECK` paragraph | Keep the raw-over-raw units and the argument that only equality bounds `TotalChunks` from above. Then state the decision: the receiver requires `TotalChunks == max(1, ceil(Size / bound))`, the form `docs/protocol-mobile.md` § Attachments **Chunking** already publishes as the sender's obligation; the `max(1, …)` is what resolves `Size == 0`; the price — a client chunking at its own buffer size is refused — is accepted, because the contract already obliges every chunk but the last to carry exactly this many raw bytes. Name `attachments.CheckDeclaration` as the enforcement point. Name no ticket for this decision. |
| B | `internal/protocol/attachments.go` → `AttachmentChunkPayload`, the `NEVER ALLOCATE FROM A CLAIM` block | Correct `TotalChunks must equal ceil(Size / bound)` to `max(1, ceil(Size / bound))`, and say the bare `ceil` is wrong at `Size == 0`. Keep "available from the FIRST chunk, before a single byte is accumulated". Replace "#1741 owns the enforcement" with `attachments.CheckDeclaration`. Leave the "and both must be within the receiver's own limits" clause as it stands — it names no ticket today and its bound is #1777's. |
| C | `internal/attachments/accumulator.go` → package doc, the resource-bounds sentence | Split the three bounds it currently attributes to #1767 at once: how many uploads may be in flight is **#1778**'s, how many bytes one may accumulate is **#1777**'s, and the first-chunk `total_chunks`/`size` cross-check is `CheckDeclaration` in this package. Keep "admission runs before an Accumulator exists". |
| D | `internal/attachments/accumulator.go` → `NewAccumulator` | "refusing an implausible declaration is the admission layer's job (#1767)" → name `CheckDeclaration`, and add that it does **not** replace this constructor's own safety: `NewAccumulator` stays exported and constructible without passing through admission. The later sentence "which is what makes this slice safe standing alone, before #1767" loses its ticket the same way — the hint-free map is safe independently of whether the caller ran `CheckDeclaration`. |
| E | `internal/attachments/accumulator.go` → `Assemble` | "refusing the declaration itself is #1767's decision to make once, at admission" → `CheckDeclaration` refuses such a declaration at admission, **and the `totalChunks < 1` clause is not newly dead**, because `NewAccumulator` is constructible without it. |

**The two remaining mentions, re-pointed and nothing more:**

- `internal/attachments/accumulator.go` → `Accumulator` doc: "belongs to the slices that build it (#1767, #1744)" → **#1778**, #1744.
- `internal/attachments/accumulator.go` → `Add` doc: "against the byte bound #1767 introduces" → **#1777**.
- `internal/attachments/accumulator_test.go` → the `testFixture` comment: "an enforcement that lives in #1767" → **#1777**. The sentence stays true as written otherwise — this package still enforces no byte *cap*; `CheckDeclaration` checks consistency, not magnitude.

**Do not touch, and each is deliberate:**

- `Assemble`'s `totalChunks < 1` clause and `TestAccumulator_ZeroDeclaredCount_IsIncomplete` — both survive unchanged (AC 5), because admission runs *in front of* the accumulator and does not gate its constructor.
- The sentinel `var` block's header doc in `accumulator.go`. It opens "Sentinel errors returned by Add and Assemble", which scopes it correctly and leaves it true; `ErrInvalidDeclaration` is neither, and belongs in `admission.go` with its own doc. The package overview's sentinel table is the documentation phase's to extend, not this ticket's.
- Every other ticket reference in `internal/protocol/attachments.go`. #1741, #1743 and #1753 still stand there for *other* decisions — the const block's header sentence about inbound enforcement, the attachment-id canonical shape, the index range check that landed in #1769, the per-chunk bound's own attribution. That includes the `Index and TotalChunks are plain int` paragraph and its closing "The same holds for a negative Size". Re-attributing any of them is out of scope and pushes this ticket past `s`.

---

## Testing strategy

One new file, `internal/attachments/admission_test.go`. One table-driven test, `t.Parallel()` on the test and each subtest, `errors.Is(err, ErrInvalidDeclaration)` for every refusal and `err != nil` failure for every admission. Subtest names are built from the two numbers, so the #1772 NUL-in-a-subtest-name trap does not apply here.

### Rows

**Refused** (`errors.Is(err, ErrInvalidDeclaration)`):

| Row | `size` | `total_chunks` | Why it is here |
|---|---|---|---|
| N1 | −1 | 1 | AC 1. Passes the cross-check unaided (`want` is 1), so only the negative guard refuses it. |
| N2 | `math.MinInt64` | 1 | Same, at the extreme: the clamp launders this to `want` 1 too. Pins the guard as `size < 0`, not `size == -1`. |
| N3 | 0 | 0 | AC 2 — `total_chunks` 0. |
| N4 | 45000 | −1 | AC 2 — `total_chunks` negative. |
| N5 | 45001 | 1 | AC 2 — off-by-one below an honest count of 2. |
| N6 | 45000 | 2 | AC 2 — off-by-one above an honest count of 1. |
| N7 | 100000 | 4 | AC 2 — the 32 KiB-buffer client. Honest count is 3. |
| N8 | `math.MaxInt64` | 1 | AC 3. The wrapping ceiling admits this; the correct one refuses it. |

**Admitted** (`err == nil`):

| Row | `size` | `total_chunks` | Why it is here |
|---|---|---|---|
| A1 | 0 | 1 | AC 4 — the zero-byte file, the case the bare `ceil` gets wrong. |
| A2 | 45000 | 1 | AC 4 — exactly at the bound. |
| A3 | 45001 | 2 | AC 4 — one byte over the bound. |
| A4 | 100000 | 3 | N7's size with its honest count, so N7's refusal is about the count and not the size. |
| A5 | `math.MaxInt64` | 204963823041218 | Scope fence: arithmetically conforming but enormous is **admitted** here and refused by #1777. Also the only row that reads the ceiling's exact value rather than just "not 1". |

A5's count needs a 64-bit `int`, which every supported platform provides (linux/darwin on amd64/arm64). If it ever has to build for a 32-bit `int`, that row is the one that fails to compile, loudly — which is the right failure.

### Mutation measurement

This package's sole-redness claims are **measured** with `go test -overlay` against an absolute-path JSON manifest, not argued from the table — see § "Mutation-testing lessons" in the package overview. Run each mutant, record what actually reddened, and correct the table below where it disagrees with the measurement rather than the other way round.

| Mutant | Expected red rows |
|---|---|
| M1 — delete the `size < 0` guard | N1, N2 |
| M2 — ceiling as `(size + bound - 1) / bound` | N8 (false admit), A5 (false refuse) |
| M3 — drop the `max(1, …)` clamp | N3 (false admit), A1 (false refuse) |
| M4 — accept a high count (`totalChunks < want` refuses) | N6, N7 — and nothing else, which is the point of the equality |
| M5 — accept a low count (`totalChunks > want` refuses) | N3, N4, N5, N8 |
| M6 — floor instead of ceil (drop the remainder bump) | N5, A3, A4, A5 |
| M7 — add an absolute cap on `size` | A5 only |
| M8 — narrow with `int(want)` instead of widening | **none** on a 64-bit `int`. Not pinnable here; the widening form is required on correctness grounds alone, and no test should claim to catch this. |
| M9 — read some other constant as the bound (e.g. 32768) | A2, N7, A4 |

Every mutant above has at least one red row and every row is in at least one red set. Where a mutant reddens a pair (M1, M2, M3), the two rows are the false-admit and false-refuse directions of the same bug and both are worth keeping — a fix that flips only one direction stays red.

**Two overlay traps this package has already been bitten by,** both of which score a broken build as a *green* mutant: a mutant that deletes the only use of an import, and one that deletes the only *read* of a local. Deleting the cross-check outright would leave `want` "declared and not used". Keep the deleted construct's dependency alive in the mutant (`_ = want`), and grep the overlay run's output for both `build failed` and `declared and not used` before trusting any verdict.

### Verification steps beyond the table

- `grep -rn '#1767' internal/attachments/` returns nothing (AC 5).
- `TestAccumulator_ZeroDeclaredCount_IsIncomplete` and `Assemble`'s `totalChunks < 1` clause are untouched — check the diff, not just the green (AC 5).
- `make check` and `make cite-guard`. Every comment this ticket adds names symbols, never lines.

---

## Open questions

1. **Does #1777's byte bound extend `CheckDeclaration` or add a sibling?** Either works — the name is deliberately about the declaration rather than about this one arithmetic — and it is #1777's call. Nothing here should pre-build for it.
2. **The sentinel `var` block in `accumulator.go` now under-lists the package's sentinels.** Deliberately left, per AC 5's "five sites and no others"; its own opening sentence scopes it to `Add` and `Assemble`, so it is not stale. Flagged here so code review reads it as a decision rather than an oversight. The package overview's sentinel table gets its row at the documentation phase.
3. **`CheckDeclaration` has no production caller until #1744.** That is the same standing this package's other exported symbols have had since #1769, and it is why the test table is the whole verification surface for this slice.

---

## Scope check (re-applied to this written spec)

| Limit | Boundary | This spec |
|---|---|---|
| Production source files created or modified | ≤ 3 | **3** — `internal/attachments/admission.go` (new), `internal/attachments/accumulator.go` (doc only), `internal/protocol/attachments.go` (doc only) |
| Total written work | ≤ 400 | ~300 — ~85 in `admission.go` (house doc density), ~170 in `admission_test.go`, ~45 across the doc-comment edits |
| New exported types or interfaces | ≤ 5 | **2** — `ErrInvalidDeclaration`, `CheckDeclaration` |
| Consumer call sites needing simultaneous update | ≤ 10 | **0** — purely additive; the first caller is #1744's |
| Acceptance criteria | ≤ 5 | **5** |
| Distinct error/reject branches | ≤ 10 | **2** |

Ships as one `size:s` ticket.

---

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** MUST FIX, fixed in this spec before commit. The boundary is explicit and single: `CheckDeclaration` is the one place a claimed `total_chunks` is checked against a claimed `size`, and its two inputs are attacker-chosen on the inbound leg. Two holes surfaced on the adversarial re-read and are now closed in § Design: (a) the negative-size refusal *cannot* be folded into the cross-check, because `max(1, ceil(-1 / 45000))` is 1 and the pair `size` −1 / `total_chunks` 1 passes unaided — measured, not assumed, for both −1 and `math.MinInt64`; (b) the comparison must widen `totalChunks` to `int64`, because narrowing `want` to `int` re-introduces a wrap in the comparison on a 32-bit `int` after the ceiling was carefully computed without one. The boundary carries no type-system signal — `CheckDeclaration` returns `error`, not a validated type — so `NewAccumulator` remains constructible without passing through it. That is deliberate and is why AC 5 keeps `Assemble`'s `totalChunks < 1` clause alive as the second layer; the spec's doc site E states it in the code rather than only here.
- **[Resource exhaustion — the threat this ticket exists for]** No findings. The attack is `make([][]byte, TotalChunks)` or `make([]byte, Size)` from a single ~60 KB frame. The equality form is what bounds `TotalChunks` from **above**; the spec refuses `>=` explicitly and says why (a maximum chunk size yields only a lower bound on an honest count). Nothing this slice admits is allocated from: `NewAccumulator`'s map takes no capacity hint and `Assemble` sizes from the sum of arrived chunk lengths behind a `len(a.chunks) != a.totalChunks` gate. The residual — a conforming but enormous declaration — is named in § Design, pinned by row A5, and assigned to **#1777** (per-upload byte bound); the in-flight-upload count is **#1778**'s. Both are OUT OF SCOPE by the ticket's own fences, and neither is reachable as an allocation before they land.
- **[Integer overflow]** No findings after the fix above. `(size + bound - 1) / bound` wraps for every `size` within 44999 of `math.MaxInt64` and `max(1, …)` launders the negative result back to 1 — a *silent admission* of the largest declaration the wire can carry. The spec bans that numerator by name, gives the division-then-remainder form, states the measured values on both sides (−204963823041216 wrapped vs 204963823041218 correct), and pins the difference with row N8 (false admit) and row A5 (false refuse), plus mutant M2. Go's constant-overflow compile error is noted as *not* a defence, since `size` arrives as a variable.
- **[Error messages, logs, telemetry]** No findings. Refusals carry `total_chunks`, `size`, the required count and the bound — daemon-authored integers with no injection shape. `CheckDeclaration`'s signature structurally excludes `Filename`, `SHA256`, `AttachmentID` and `Data`, which is the strongest available form of the never-log rule `AttachmentChunkPayload`'s SECURITY block states and `reject`'s doc honours. Zero log calls; the dispatch site (#1744) is the only place that knows the attachment id and conn id worth logging.
- **[Concurrency]** No findings. `CheckDeclaration` is pure and stateless — no lock to order, no shared state to check-then-mutate, no goroutine to leak. It is safe to call from any goroutine, unlike `Accumulator`, and § Concurrency model requires that difference to be stated in the doc block so a reader of both symbols is not left to infer it. TOCTOU between admission and construction is not a concern because the two numbers checked are the same two values passed to `NewAccumulator` by value; the spec's scalar signature is what keeps a caller from re-reading a mutable payload between the check and the use.
- **[File operations]**, **[Subprocess execution]**, **[Tokens/secrets]**, **[Cryptographic primitives]** — not applicable, and each by a design decision rather than by omission: this slice touches no filesystem path, spawns no process, handles no credential, and performs no comparison against a secret. The one comparison it makes is two attacker-supplied integers against each other, where a timing side channel leaks nothing the sender does not already hold — the same reasoning `Assemble`'s digest comparison records for declining `subtle.ConstantTimeCompare`.
- **[Network & I/O]** No findings — this slice reads no socket and sets no deadline. The per-frame size cap it depends on (`MaxAttachmentChunkBytes`, and the v2 application-envelope cap behind it) is enforced by the transport before a payload reaches here; this check consumes that bound as a published number and does not re-derive it.
- **[Threat model alignment]** Aligned with `docs/protocol-mobile.md` § Attachments. The receiver's never-allocate-from-a-claim rule and the sender's `max(1, ceil(size / 45000))` obligation are both already published; this slice adopts both unchanged and publishes nothing new, so no wire-visible surface changes and no client can be broken by a rule it could not already read. Authorization is unchanged and structural — an unpaired device never reaches this path.

**Reviewer:** architect (self-review per the security-review checklist)
**Date:** 2026-08-25
