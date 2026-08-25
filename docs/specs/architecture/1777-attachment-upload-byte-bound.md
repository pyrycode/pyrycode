# #1777 — bound the bytes one attachment upload may accumulate

## Files to read first

| Read | Symbol | What to extract |
|---|---|---|
| `internal/attachments/admission.go` | `CheckDeclaration` | The shape the new sibling mirrors: two scalars in, one wrapped sentinel out, pure and stateless. Also its `WHAT IS NOT CHECKED IS MAGNITUDE` block — that paragraph is the hole this ticket fills, and it is one of the sweep sites. |
| `internal/attachments/admission.go` | `ErrInvalidDeclaration` | The neighbour the new sentinel must stay distinguishable from, and the doc line "One sentinel covers every refusal below" that this slice makes false unless reworded. |
| `internal/attachments/accumulator.go` | `Add` | Its documented fixed check order, and the no-copy PRECONDITION whose stated justification names this ticket's bound. |
| `internal/attachments/accumulator.go` | `reject` | The existing latch: records the refusal, drops the map. AC 2's "holds no bytes afterwards, and stays refused" is already built — **do not modify this function.** |
| `internal/attachments/accumulator.go` | `Assemble` | Why it sizes from the sum of arrived chunk lengths rather than any counter, and why the two integrity checks read the assembled slice and not a proxy. The new running total must not be substituted into either place. |
| `internal/attachments/accumulator.go` | `Accumulator` | Field-doc density and house tone; where the new running-total field goes. |
| `internal/attachments/accumulator_test.go` | `testFixture`, `testChunk`, `newTestAccumulator`, `testFixtureDigest` | The fixture conventions the new bound-sized fixtures follow — in particular that a declared digest is a **written-out literal** with its reproducing `shasum` command in the comment, never computed by a helper. `testFixture`'s own comment is a sweep site. |
| `internal/attachments/accumulator_test.go` | `TestAccumulator_IntegrityFaults_RejectAndDiscard` | The reject-row body: how "the transfer is discarded" is asserted (map nil, later `Add` and `Assemble` return the identical error value). The new crossing test reuses this shape. |
| `internal/attachments/admission_test.go` | `TestCheckDeclaration` | The table idiom, and the `math.MaxInt64` / 204963823041218 row whose comment is a sweep site. **No existing row's expectation changes in this ticket** — see § The fork. |
| `internal/protocol/attachments.go` | `MaxAttachmentChunkBytes` | 45000 raw bytes per chunk, the unit the bound converts into a chunk count. Its `NEVER ALLOCATE FROM A CLAIM` neighbour ends "and both must be within the receiver's own limits" — the clause this slice satisfies and **must not edit**. |
| `internal/relay/v2session_modal.go` | `pushQueueByteCeiling` | The repo's shape precedent for a retained-bytes ceiling: a `Derivation.` paragraph doing explicit arithmetic, plus a paragraph on what the ceiling legitimately refuses. Copy the doc discipline, not the number. |
| `docs/protocol-mobile.md` § Error codes | — | The published rows for `attachment.too_large` (permanent, "declared `size` on the first chunk **or** accumulated bytes later") and `attachment.too_many_uploads` (transient). Both rungs are already contracted; this slice builds them. |
| `docs/knowledge/features/attachments-package.md` § "Mutation-testing lessons" | — | Two traps that bite this ticket: a shared-constant mutant reddens every row whose arithmetic reads the constant (not just rows placed at it), and a presence/threshold assertion needs a fixture at the value that is indistinguishable from the wrong answer. |

## Context

`docs/protocol-mobile.md` § Error codes already publishes `attachment.too_large` as detected "either from the declared `size` on the first chunk **or from accumulated bytes later**", permanent for that file. Both rungs are contracted; neither exists. `CheckDeclaration` (#1776) deliberately admits an arithmetically conforming but enormous declaration, and `Accumulator` stores whatever arrives, so today one client can make the daemon hold an arbitrarily large file in memory.

The second rung is not implied by the first. A chunk's actual `data` length has no inbound validator — only the transport's 65519-byte application-envelope cap bounds it — so an admitted transfer can deliver materially more bytes than it declared, and nothing notices until `Assemble`, after the bytes are already held. The declared rung refuses the honest oversize file on frame one for free; the accumulated rung is what makes the number a ceiling rather than a ceiling plus a per-chunk fudge factor.

Both rungs answer one sentinel and read one constant. Rejections here are Go sentinels; mapping them to `attachment.too_large` is #1744's job at the dispatch site (`errors.Is`), as it is for every other sentinel this package exports.

No ADR is warranted: the decision recorded below (sibling function rather than extending `CheckDeclaration`) is package-local and belongs in `docs/knowledge/features/attachments-package.md`, which the documentation phase owns.

## Design

Two production files, both already in `internal/attachments`. No new file, no new exported type or interface, no consumer call site — nothing production-side feeds this package until #1744.

### The fork: a sibling, not an extension of `CheckDeclaration`

#1776 § Open questions 1 left this open. It resolves to a **sibling function in `admission.go`**, and the reason is a measurable one rather than a taste:

- `TestCheckDeclaration`'s `math.MaxInt64` / 204963823041218 row is, by its own comment, "the only row that reads the ceiling's exact value rather than merely asserting it is not 1, so it is what pins the arithmetic itself." Folding a magnitude check into `CheckDeclaration` flips that row to a refusal and destroys the sole pin on the ceiling arithmetic — the arithmetic whose wrapping form silently admits the largest declaration the wire can carry. Keeping the two checks in separate functions keeps that row admitted and that pin alive.
- The two refusals have different published repairs: `attachment.invalid_chunk` says re-chunk, `attachment.too_large` says shrink the file. One function returning two sentinels with two client-visible repairs is a worse contract than two functions returning one each.
- The cost of a sibling — a caller can run one and forget the other — is answered structurally rather than by hope: the accumulated rung in `Add` sits on the data path and cannot be skipped, so the memory property holds even for a caller that never calls either admission function. That is the belt-and-suspenders shape, with different fabric on each layer.

### `admission.go` — the constant, the sentinel, the declared rung

**The constant.** Package-level, unexported, untyped, in `pushQueueByteCeiling`'s form:

```go
const maxUploadBytes = 16 << 20 // 16 MiB
```

Unexported because it is receiver policy that § Attachments deliberately leaves unpublished ("receiver-configured and unpublished — a client learns them by being rejected"); untyped because both readers want different types (`int64` in the comparisons, `int` in a test's `make`). A later ticket that publishes the number to clients (#1751 § Open questions 3 anticipates one) can export it then.

Its doc must record the derivation, in the shape `pushQueueByteCeiling`'s doc uses — the recorded lesson from #1505 is that such a constant's doc carries the arithmetic, not a citation of some other mechanism that supposedly bounds it. It must state:

1. **What it bounds**: the retained bytes of ONE upload — Σ `len(chunk.Data)` over the chunks an `Accumulator` holds. That is exactly what the accumulator holds, because `Add` retains each chunk's slice without copying.
2. **Why this magnitude**: the inbound population is phone-authored attachments — screenshots, photos, short documents, log excerpts. A 12 MP HEIC photo is single-digit MB and a 4K PNG screenshot is under 10 MB, so 16 MiB clears the largest of those with headroom while refusing what is plainly a file transfer rather than an attachment.
3. **The chunk arithmetic it implies**: 16777216 raw bytes at `protocol.MaxAttachmentChunkBytes` is `max(1, ceil(16777216 / 45000))` = 373 chunks, the last of them 37216 bytes. The bound creates no new frame-size pressure — 45000 raw base64-encodes to exactly 60000 bytes, under the 65519-byte envelope cap, and that is a property of the chunk constant rather than of this one.
4. **The multiplication AC 5 requires**: worst-case resident attachment bytes is this bound × #1778's concurrency bound. At a concurrency bound of 4 that is 64 MiB; at 8, 128 MiB. State the budget #1778 inherits — hold the **product** at or under roughly 64 MiB, so 4 concurrent uploads at this number, and if #1778 wants more concurrency one of the two numbers moves.
5. **Retained versus peak**: `Assemble` allocates a fresh slice of the assembled length, so one upload's peak is 2× its retained bytes for the duration of that call, and the worst case adds one more bound to the product while an assembly is in flight. `Add`'s deliberate refusal to copy each chunk is what keeps retained at 1× rather than 2× — that is the sentence in `Add`'s doc this constant now backs.
6. **The slack that is not slack**: the crossing chunk's bytes exist in the caller's decoded frame when `Add` measures them, so the transient overshoot is one frame — bounded by the 65519-byte envelope cap, never by this constant, and never retained.
7. **Why it is not derived from `pushQueueByteCeiling`**: that ceiling is per-session retained push-queue bytes and was derived as ~2× a structural maximum (256 × 65519). This bound has no structural maximum to double; it is a policy pick from the file population, multiplied by a concurrency bound rather than by a queue cap. Same doc discipline, different derivation shape.

**The sentinel.** In `admission.go`, next to `ErrInvalidDeclaration`, in the package's `errors.New("attachments: …")` house style:

```go
var ErrUploadTooLarge = errors.New("attachments: upload exceeds the per-upload byte bound")
```

It lives beside `ErrInvalidDeclaration` rather than in `accumulator.go`'s var block because that block's opening sentence scopes itself to "Sentinel errors returned by Add and Assemble", and this one is returned by `Add` **and** by an admission function. Its doc must say why it is not folded into either neighbour:

- Not `ErrInvalidDeclaration`: that one is committed to `attachment.invalid_chunk`, whose published repair is to re-chunk. Both codes are non-retryable, so collapsing them would not hot-loop a client — it would send it to re-chunk the same oversized file, which fails identically, forever.
- Not a general resource-limit sentinel #1778 could share: `attachment.too_many_uploads` is marked transient in `internal/protocol/codes.go` because it clears when other uploads finish, while this one never clears. Collapsing them would tell a client either to re-upload an oversized file in a loop or to abandon a bound that clears in seconds. The name is specific to the byte bound for that reason.

**The declared rung.**

```go
func CheckDeclaredSize(size int64) error
```

Refuses `size > maxUploadBytes` with `ErrUploadTooLarge` wrapped around the two numbers; admits everything else. Pure and stateless like `CheckDeclaration`, so safe to call from any goroutine.

It checks magnitude and nothing else. A negative `size` is **admitted here** — `CheckDeclaration` already refuses it with its own sentinel, and a second owner for that fault would make which sentinel a caller sees depend on the order it happened to run the two checks. The doc must therefore state the pairing obligation explicitly, because it is load-bearing for more than tidiness: `CheckDeclaration` bounds `total_chunks` from above, this one bounds `size` from above, and only **together** do they bound the accumulator's key space — with both run, an admitted transfer declares at most 373 chunks, so the map can hold at most 373 entries. Run alone, this one admits a declaration of 2³¹−1 zero-byte chunks, which no byte bound can refuse because Σ `len(Data)` stays 0. #1744 must call both, on the first chunk, before constructing an `Accumulator`.

`ErrInvalidDeclaration`'s doc line "One sentinel covers every refusal below" becomes false the moment a second sentinel appears below it in this file, so it must be reworded to scope itself to `CheckDeclaration`'s refusals — the doc's own opening sentence already says exactly that — and to name `ErrUploadTooLarge` as the deliberate second one. This is the sixth sweep site #1776's spec anticipated as fork-conditional; the sibling fork does not avoid it, it just makes the edit a one-line rescoping instead of a reversal.

### `accumulator.go` — the accumulated rung

One new field on `Accumulator`:

```go
received int64 // Σ len(data) of the chunks currently held; invariant 0 <= received <= maxUploadBytes
```

Its doc must state the invariant and that it is never reset — `reject` drops the map and latches the refusal, and every path out of a refused transfer short-circuits on the latch before reading this field, so zeroing it would be a write nothing can observe.

One new check in `Add`, as **step 5** of the documented fixed order — after the duplicate check, immediately before the map store:

- Refuse when `int64(len(chunk.Data)) > maxUploadBytes-a.received`, with `ErrUploadTooLarge` wrapped around the chunk index, the arriving length, the already-held total and the bound. Route it through `a.reject(...)` exactly as the three framing refusals do.
- Otherwise store the chunk, then add its length to `received`.

Three properties of that formulation are contract, not preference, and each belongs in the doc:

- **Last, not first.** The running total must count only bytes the accumulator actually retains, so the check belongs with the store rather than ahead of the checks that decide whether a store happens at all. Its one *observable* consequence is which sentinel a frame carrying two faults gets — every unstored chunk rejects the transfer, so a counter that moved for one could never be read afterwards — and that is exactly the kind of order the package documents rather than leaves to inference. Placing it last keeps every existing two-fault frame answering the sentinel it answers today.
- **Subtraction, not a sum.** `received + len > bound` forms a sum that could in principle wrap; `len > bound - received` cannot, because the invariant keeps the right operand in `[0, maxUploadBytes]`. The addend here is a materialised slice length rather than a claimed number, so the wrap is not reachable in practice — write the safe form anyway and say which of the two facts is doing the work, so a later reader does not "simplify" it back or, worse, conclude that `CheckDeclaration`'s overflow discipline was cargo cult.
- **`>` not `>=`.** A transfer landing exactly on the bound is admitted; the bound is a ceiling on what may be held, not on what may be approached.

**What must not change.** `Assemble` keeps sizing its output from the sum of the arrived chunk lengths and keeps comparing the assembled slice itself against the declared size and digest. `received` is now a value that looks substitutable into both places and is not: it is a counter maintained by `Add`, so sizing or comparing from it would reintroduce exactly the proxy the package's "the bytes verified are the bytes returned" property forbids, and a future regression in the assembly loop would then be invisible to a green check. `reject` is not touched at all.

## Concurrency model

No goroutines, no channels, no locks, no change to any of them.

`CheckDeclaredSize` is pure and stateless — no filesystem, socket, logger, lock or goroutine — so it joins `CheckDeclaration` and `SanitizeFilename` as safe to call concurrently from any goroutine.

`Accumulator` remains NOT SAFE FOR CONCURRENT USE and deliberately carries no mutex. The new field is written by `Add` under the existing caller obligation: one accumulator is owned by one session and fed serially by relay's `appFrameWorker`, one goroutine per session with strict FIFO ordering. Synchronising the registry of in-flight uploads is #1778's, not this type's.

## Error handling

| Failure | Answer | Latches? |
|---|---|---|
| Declared `size` above `maxUploadBytes` | `ErrUploadTooLarge`, wrapped with the size and the bound | n/a — no accumulator exists yet |
| Arriving chunk would take the held total past `maxUploadBytes` | `ErrUploadTooLarge`, wrapped with the index, the arriving length, the held total and the bound | yes — via the existing `reject`, so the map is dropped and every later `Add`/`Assemble` returns the identical error value |
| Declared `size` negative, or `total_chunks` inconsistent with `size` | unchanged: `ErrInvalidDeclaration` from `CheckDeclaration` | n/a |

Every wrapped message carries integers and the bound only — no `Data`, no `Filename`, no declared digest — so the never-log rules `protocol.AttachmentChunkPayload`'s SECURITY block states hold unchanged. The bound itself appears in the message: that is a log line for the daemon operator, and #1744 must map the sentinel to a wire code rather than forwarding Go error text to a client, which is the house pattern for every sentinel this package already exports.

## Testing strategy

Scenarios, not test bodies. Two additions in `admission_test.go`, three new tests plus one fixture block in `accumulator_test.go`; nothing existing changes except two comments.

**Fixtures** (`accumulator_test.go`, alongside `testFixture`): **one** package-level bound-sized fixture, shared by all three new accumulator tests and sliced by them — `maxUploadBytes` bytes, all zero except the **last**, which is `'z'`. Shared rather than per-test so the suite pays for it once, and none of the three may mutate it: the recorded fixture-mutation trap in this package is a test that scribbled on a shared fixture and reddened every other parallel test under an unrelated mutant. The cost is 16 MiB resident for the whole test binary, including runs filtered to unrelated tests, which is accepted rather than worked around with lazy-init machinery this package has no other use for. The non-uniform tail is deliberate — a uniform buffer cannot falsify a digest-over-a-prefix or assembled-from-the-wrong-chunk mutant, which is #1770's recorded lesson about the zero-byte row applied at the other end of the size range. Its declared digest is a written-out literal, per the convention `testFixtureDigest` establishes, traceable outside this package to:

```
{ head -c 16777215 /dev/zero; printf 'z'; } | shasum -a 256
1415568bd87f16157ad7572d21f328e0dee83ff58782efde7ab4e7058d0a0d0a
```

Peak test memory is roughly two copies of the bound (the fixture plus `Assemble`'s fresh slice) in the one test that assembles, so no test needs more than ~32 MiB.

**`admission_test.go` — `TestCheckDeclaredSize`**, one table, rows:

- `size` 0 → admitted. The zero-byte file.
- `size` `maxUploadBytes - 1` → admitted.
- `size` `maxUploadBytes` → **admitted**. AC 3's first rung; the row that dies if the comparison is written `>=`.
- `size` `maxUploadBytes + 1` → refused. AC 1; the row that dies if the comparison is written `>`-with-an-off-by-one or reads any other constant.
- `size` `math.MaxInt64` → refused. The declaration `TestCheckDeclaration`'s A5 row admits, refused here — the division of labour made observable rather than argued.
- `size` −1 → **admitted**, with the comment recording that magnitude is this function's whole subject and the negative case is `CheckDeclaration`'s, so both must run.

Refusal rows assert `errors.Is(err, ErrUploadTooLarge)` **and** `!errors.Is(err, ErrInvalidDeclaration)`, so a future refactor that wrapped one sentinel in the other is red here.

**`admission_test.go` — a shared assertion helper** taking a `*testing.T` and an error, asserting `errors.Is(err, ErrUploadTooLarge)` is true and that `errors.Is` is false against **all seven** sentinels the package already exports — `ErrInvalidDeclaration`, `ErrTotalChunksMismatch`, `ErrIndexOutOfRange`, `ErrDuplicateIndex`, `ErrSizeMismatch`, `ErrDigestMismatch`, `ErrIncomplete` — enumerated by name in one slice, and in **both** directions (neither is the new one, and the new one is neither). Called from the admission refusal rows and from the crossing test below, which is how AC 4's "both refusals answer with one sentinel" is pinned against the two real wrapped errors rather than against the bare sentinel value.

**`accumulator_test.go` — the crossing test** (AC 2, all three of its clauses in one body, following `TestAccumulator_IntegrityFaults_RejectAndDiscard`'s reject-row shape):

- Construct directly with `NewAccumulator` — a declared count of 2 and a small declared size, neither of which is ever reached, because this is the case the rung exists for: no inbound validator enforces the 45000-byte per-chunk contract, so the realistic attack is a small declared count delivering oversized chunks, not bound-many honest ones.
- Feed chunk 0 carrying `maxUploadBytes` bytes → accepted, the held total now exactly at the bound.
- Feed chunk 1 carrying **one** byte → refused. Crossing by exactly one is what makes this the off-by-one pin on the accumulated rung.
- Then assert the discard: the chunk map is nil, a later well-formed `Add` returns the identical error value, and `Assemble` returns the identical error value and no bytes.

A single oversized **first** chunk — the other shape of the same attack — needs no row of its own: it is the identical branch with `received` still 0, and no mutant reddens one of the two without reddening the other.

**`accumulator_test.go` — the exactly-the-bound round trip** (AC 3's second rung): declare 2 chunks, `size` `maxUploadBytes`, digest as above; feed `maxUploadBytes - 1` zero bytes at index 0 and the single `'z'` at index 1; `Assemble` returns the fixture bytes with no error. Two chunks rather than one on purpose — a one-chunk fixture cannot falsify a mutant that tracks only the latest chunk's length instead of a running sum.

**`accumulator_test.go` — the check-order test.** Feed the bound-sized chunk at index 0 (accepted, total now at the bound), then re-send index 0 carrying one byte: the answer must be `ErrDuplicateIndex`, not `ErrUploadTooLarge`. It reuses the shared fixture and allocates nothing, and it is the sole red for moving the new check above the duplicate check — where `1 > maxUploadBytes - maxUploadBytes` is true and the wrong sentinel wins. It pins the sentinel choice only; the counter's non-movement for an unstored chunk is unobservable by construction, per § Design.

Sole-redness claims here are predictions, not measurements. If the developer verifies them, do it with `go test -overlay` against an absolute-path JSON manifest (no worktree write), and read the package overview's mutation-testing lessons first — in particular that a mutant perturbing a shared constant reddens every row whose arithmetic reads it, not only the rows placed at it, so the predicted red set for a wrong-`maxUploadBytes` mutant is "re-run every row above under the new value".

## Comment sweep (AC 5)

Seven edits in the two production files and their tests: the five sites that defer the byte bound to this ticket in the future tense, the one #1776 left fork-conditional, and `Add`'s fixed-order block, which gains step 5. `git grep '#1777' internal/attachments/` must come back empty afterwards.

| Site | Today | Required change |
|---|---|---|
| `accumulator.go` package doc, the resource-bounds sentence | "how many bytes one may accumulate is #1777's" | Present tense: it is `maxUploadBytes`, enforced twice in this package — the declared `size` at `CheckDeclaredSize`, the accumulated bytes in `Add`. Keep the in-flight-upload count as #1778's and the cross-check as `CheckDeclaration`. Add that the KEY SPACE claim in the same paragraph is now stronger: with both admission checks run, an admitted transfer declares at most 373 chunks. |
| `accumulator.go` → `Add`'s no-copy PRECONDITION | "would double peak memory for every upload against the byte bound #1777 introduces" | Against `maxUploadBytes`, which now exists; the sentence's arithmetic is the constant's doc point 5. |
| `accumulator.go` → `Add`'s fixed-order doc block | four numbered steps | Add step 5 with the three contract properties from § Design (last not first, subtraction not sum, `>` not `>=`). |
| `admission.go` → `CheckDeclaration`'s `WHAT IS NOT CHECKED IS MAGNITUDE` block | "the per-upload byte bound that refuses it is #1777's" | Name `CheckDeclaredSize`, in this file, and say why it is a sibling rather than folded in — the A5 row is the reason and stays admitted here. The rest of the block stays true as written. |
| `admission.go` → `ErrInvalidDeclaration` doc | "One sentinel covers every refusal below." | Rescope to `CheckDeclaration`'s refusals and name `ErrUploadTooLarge` as the deliberate second sentinel in the file, with the re-chunk-versus-shrink reason. |
| `accumulator_test.go` → `testFixture` comment | "this package enforces no byte cap — a bound-sized fixture … would imply an enforcement that lives in #1777" | The package now enforces one. The reason this fixture stays tens of bytes changes: the properties it exists for (uneven tail, arrival order, index addressing) are independent of magnitude, and the tests that exercise the bound build their own bound-sized fixtures. |
| `admission_test.go` → the A5 row comment | "bounding the magnitude of one upload is the per-upload byte bound's job (#1777)" | It is `CheckDeclaredSize`'s job, in a sibling function, and that separation is what keeps **this row admitted** — folding the bound into `CheckDeclaration` would refuse it and destroy the only pin on the ceiling's exact value. |

Two nearby comments are **deliberately not touched**, and their absence is not an oversight: `internal/protocol/attachments.go`'s `NEVER ALLOCATE FROM A CLAIM` block ends "and both must be within the receiver's own limits", a generic clause naming no ticket which this slice satisfies rather than invalidates; and `docs/knowledge/features/attachments-package.md`, which the documentation phase owns and which mentions #1777 twice.

## Acceptance criteria → deliverables

| AC | Where it lands | What pins it |
|---|---|---|
| 1 — declared `size` above the bound refused on that chunk | `CheckDeclaredSize` | `TestCheckDeclaredSize`, the `maxUploadBytes + 1` row. It refuses before an `Accumulator` is constructed, which is the same "runs in front of" relation `CheckDeclaration` already has. |
| 2 — accumulated crossing refused, holds no bytes, stays refused | `Add` step 5 + the existing `reject` | The crossing test's three assertions |
| 3 — exactly the bound admitted and completes | both rungs | `TestCheckDeclaredSize`'s `maxUploadBytes` row + the exactly-the-bound round trip |
| 4 — one sentinel, `errors.Is`-separated from all seven existing | `ErrUploadTooLarge` | The shared assertion helper, called from both refusal sites, both directions |
| 5 — named constant with a derivation doc, no stale deferral comments | `maxUploadBytes` + the sweep table | The seven doc points; `git grep '#1777' internal/attachments/` empty |

## Open questions

1. **Does #1778 read `maxUploadBytes` or restate the product?** If #1778's registry lands in this package it can read the constant directly and assert the product in one place. If it lands elsewhere, exporting is the cheaper move at that point, not now.
2. **Does the bound belong in a client-visible capability advertisement?** #1751 § Open questions 3 anticipates a ticket publishing both numbers so a client can pre-flight a large file rather than discovering the limit by rejection. Publishing is explicitly not this slice's, and § Attachments' "receiver-configured and unpublished" stays true until that ticket lands.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No findings. The boundary is explicit and unchanged: `size` and `total_chunks` are attacker-chosen integers, `Data` is attacker-supplied bytes, and this slice adds two checks and zero new inputs. `CheckDeclaredSize` takes one scalar rather than a `protocol.AttachmentChunkPayload`, mirroring `CheckDeclaration`, which structurally excludes `Filename`, `SHA256`, `AttachmentID` and `Data` from ever reaching its error strings. Nothing downstream is newly told a value is trusted — a successful `Assemble` still means integrity, not authenticity.
- **[Resource exhaustion — the threat this ticket exists for]** No MUST FIX. Retained bytes per upload are now bounded by `maxUploadBytes` at all times: the crossing chunk is refused before the map stores it, and `reject` drops what was already held. Transient overshoot is one decoded frame, bounded by the 65519-byte envelope cap rather than by this constant. Peak per upload is 2× the bound during `Assemble` only, which the constant's doc must state. Worst case across the daemon is bound × #1778's concurrency bound, stated in the doc so #1778 inherits a budget instead of re-deriving one.
- **[Resource exhaustion — key space, the residual]** SHOULD FIX, addressed in-spec by required doc text rather than by new code. `Add`'s range check bounds map entries by the **declared** count, and no byte bound can refuse a declaration of 2³¹−1 **zero-byte** chunks, because Σ `len(Data)` stays 0. What closes that is the pairing: `CheckDeclaration` bounds `total_chunks` from above, `CheckDeclaredSize` bounds `size` from above, and together they cap an admitted transfer at 373 chunks. The obligation is about the **count**, not about `size`'s sign — a negative `size` reaching `NewAccumulator` unchecked is contained, since nothing is ever allocated from it and `Assemble`'s length comparison can never match it, so such a transfer dies at `ErrSizeMismatch` having held only what the byte rung already bounded. The obligation is on #1744 to run both on the first chunk before constructing an `Accumulator`; § Design mandates that both `CheckDeclaredSize`'s doc and the package doc state it, since neither function can enforce the other's presence. Enforcing it structurally would mean a failing constructor or a validated type, which #1776 deliberately declined and this slice does not reopen.
- **[Integer overflow]** No findings. The accumulated rung is specified as `len > bound - received`, never as a sum, so no addition can wrap regardless of the arriving length; the invariant `0 <= received <= maxUploadBytes` keeps the right operand non-negative. `CheckDeclaredSize` performs no arithmetic at all — one comparison against a constant — which is why a negative `size` cannot be laundered here the way `(size + bound - 1) / bound` launders one in `CheckDeclaration`. On a 32-bit platform `maxUploadBytes` still fits an `int`, so a test's `make([]byte, maxUploadBytes)` is not a second wrap site.
- **[Error messages, logs, telemetry]** No findings, one obligation restated. Both new messages carry integers and the bound only — never `Data`, `Filename` or the declared `sha256`, matching the rules `protocol.AttachmentChunkPayload`'s SECURITY block states and this package already honours. The bound appears in the message; that is a daemon-operator log value, and #1744 maps the sentinel to `attachment.too_large` rather than forwarding Go error text, as it does for every other sentinel here. Disclosing the number to a client is not itself a hazard — § Attachments' "unpublished" is a deliberate non-commitment, not a secret, and its own sentence says a client learns the bound by being rejected.
- **[Concurrency]** No findings. No goroutine, channel or lock is added. `CheckDeclaredSize` is pure and stateless. The one new field is written only by `Add`, under `Accumulator`'s existing documented caller obligation (one owner, fed serially by relay's `appFrameWorker`). There is no check-then-mutate window on shared state, because there is no shared state: the check and the store happen in one call on a single-owner value. A registry that would introduce one is #1778's and is explicitly not built here.
- **[Timing side channels]** No findings. Both comparisons are on lengths, and neither operand is a secret. The digest comparison this slice does not touch remains a plain `!=`, correctly, since both the bytes and the claimed digest are attacker-supplied.
- **[Denial of service — holding capacity]** OUT OF SCOPE, named. A client can still park up to the bound indefinitely by sending 372 of 373 chunks and stopping; expiry and abandonment of a partial upload is #1742's, and how many such uploads may exist at once — plus releasing the capacity a refusal frees — is #1778's. Both are the ticket's own scope fences, and neither is newly reachable because of this slice: before it, the same client could park unbounded bytes.
- **[File operations / subprocess / cryptographic primitives / network & I/O]** Not applicable, by a design decision rather than by omission: this slice is in-memory only — no disk, no wire dispatch, no wire codes, no logging, no subprocess, no randomness, and no new I/O path. The only crypto in the package is `Assemble`'s existing `crypto/sha256`, which this slice does not touch, and the only new byte buffers are test fixtures.
- **[Threat model alignment]** No findings. `docs/protocol-mobile.md` § Attachments' "Never allocate from a claim" is the relevant published rule, and this slice strengthens it: it adds no allocation sized from a claim, and it is what makes "both must be within the receiver's own limits" — the clause that same block ends on — true for the byte axis. The published `attachment.too_large` row's two detection points are the two rungs built here, and its "permanent for that file" is why the sentinel stays separate from #1778's transient one.

**Reviewer:** architect (self-review per the security-review checklist)
**Date:** 2026-08-25
