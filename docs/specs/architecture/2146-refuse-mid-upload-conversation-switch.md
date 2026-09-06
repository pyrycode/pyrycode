# #2146 — refuse a chunk naming a different conversation than its transfer was admitted under

## Files read

- `internal/attachments/registry.go` → `entry`, `Registry` (type doc), `insertLocked`, `Admit`, `Lookup`, `lookupAndStamp`, `Deliver`, `ErrUnknownUpload` — the whole seam. `lookupAndStamp` is the only critical section reached on every delivered chunk that runs *before* the idle stamp; `entry` is the thing AC 1 calls "the transfer".
- `internal/attachments/intake.go` → `Receive` — the fork (`Lookup`, then `Admit` only when unheld, then `Deliver` **unconditionally**). That unconditional `Deliver` is what makes a latch on the delivery path reach the admitting chunk. Its doc block is AC 4 site 1.
- `internal/relay/v2session_attachment.go` → `attachmentRejectFor`, `handleAttachmentChunk`, `rejectAttachmentChunk` — the wire mapping, and the gate that discharges `Receive`'s destination precondition. `handleAttachmentChunk` passes `chunk.ConversationID` **verbatim** as `Receive`'s `conversationID` argument; that is the one production call site and it is what makes the chunk field and the parameter one value.
- `internal/protocol/attachments.go` → `AttachmentChunkPayload.ConversationID` — "Meaningful on the UPLOAD leg only … validated against the daemon's registry before it becomes a path component, never a value trusted as sent, and naming a conversation is not authorization." The field the comparison reads.
- `internal/attachments/registry_test.go` → `TestRegistry_AdmitRefusedRepeatUnderAHeldPair_KeepsTheIncumbent` (the posture to mirror), `packageSentinels`, `TestRegistry_DeliverUnheldPair_RefusesAndCreatesNothing`, `boundChunk` / `testBoundTotal` fixtures.
- `internal/relay/v2session_attachment_test.go` → `TestV2Session_AttachmentChunk_SentinelsMapToWireCodes` — the arm table this slice adds a row to.
- `docs/knowledge/features/attachments-package-mutation-testing-lessons-measured-across.md` — two lessons change what this slice must do. **`packageSentinels` is a fixture whose drift is silent**: "a sentinel added later and not listed here weakens the claim silently, with no test going red to flag the drift" — this slice adds a sentinel, so it must append. And **"every by-construction sentence needs its own mutant"** (#1796, #1880, twice paid for) — the stamp invariant below is exactly a by-construction claim, so it gets an assertion rather than an argument.
- `docs/knowledge/features/attachments-package-in-flight-upload-registry.md` — the locked-method roster and the acquisition-count table this slice must leave true.
- `docs/protocol-mobile.md` § `attachment_chunk` (`conversation_id` row), § Changelog — AC 4 site 4.

## Context

`Receive` reads the destination conversation **once, on the completing chunk**, and its own doc block says so and names this ticket as what closes it. So a transfer whose first chunk declares conversation A and whose last chunk names conversation B is filed under B. Both ids passed #2143's `KnownConversation` gate and `EnsureDir` still refuses anything escaping its root, so this is a **consistency defect, not a containment hole**: the transfer's declaration and its outcome can disagree, and nothing tells the operator the bytes moved.

Closing it needs the one thing #2143 did not add — somewhere to record the destination of a transfer *in flight*, so a later chunk can be compared against it.

No ADR is warranted. This adds a field to an unexported struct and one comparison inside an existing critical section; every decision it makes is already committed by `entry`'s and `Deliver`'s doc blocks.

## Design

### The seam, and what each candidate costs

| Shape | Call sites to edit | Verdict |
|---|---|---|
| Widen `Registry.Admit` to carry the destination | **41** (40 test, 1 production) | 4× the size table's call-site line. Also **unreachable**: `Receive` skips `Admit` entirely for a pair already held, so the comparison would never see the chunk that trips it. |
| A new locked `Registry` compare method called from `Receive` | 0 | Breaks `Deliver`'s published "AT MOST TWO acquisitions of `mu` per delivered chunk", and splits the compare and the deliver across two acquisitions — the split-lock shape this type's own doc argues against three times. |
| **Latch-and-compare inside `lookupAndStamp`, reached from `Deliver`'s existing chunk parameter** | **1** (`Deliver`; `lookupAndStamp` has exactly one caller and no test caller) | Chosen. |

`Deliver` already takes the whole `protocol.AttachmentChunkPayload`, which has carried `ConversationID` since #2142, and `Receive` calls `Deliver` on **every** chunk including immediately after a successful `Admit`. So the admitting chunk records the destination and every later chunk is compared against it, at one call-site edit.

That is why AC 1 is written as *recorded on the transfer* rather than *recorded inside `Admit`*: the observable is that the destination is fixed by the transfer's **first delivered chunk** and no later chunk may move it.

### `entry` gains a third field

```go
type entry struct {
	acc            *Accumulator
	lastChunkAt    time.Time
	conversationID string // the destination the transfer's first delivered chunk named
}
```

**The empty string means "not yet latched", and that is a decision rather than an accident.** An entry exists un-latched only between `Admit` and the `Deliver` that follows it in the same `Receive` call, and an empty destination is unreachable as a *real* one on the production path twice over: `handleAttachmentChunk`'s first gate arm refuses `chunk.ConversationID == ""` before the seam, and `EnsureDir`'s `conversations.ValidID` refuses `""` with `ErrInvalidID` regardless, so empty bytes can never be stored. A second `bool` field would encode a state the production path cannot enter, for a distinction nothing can observe.

### The comparison goes inside `lookupAndStamp`, ahead of the stamp

New signature — it takes the **chunk** rather than three adjacent strings:

```go
func (r *Registry) lookupAndStamp(connID string, chunk protocol.AttachmentChunkPayload) (*Accumulator, bool, error)
```

Three outcomes, in the order the body must run them:

| Outcome | Answer | Entry |
|---|---|---|
| pair absent (or just reaped) | `(nil, false, nil)` | nothing stored — unchanged |
| pair held, destination disagrees | `(nil, false, ErrConversationMismatch)` | **untouched — not stamped, not stored, not deleted** |
| pair held, destination agrees or is unlatched | `(acc, true, nil)` | latched if unlatched; stamped; stored |

`ok` answers *is there a transfer*; `err` answers *is this chunk admissible to it*. Keeping both preserves every landed doc claim about who raises `ErrUnknownUpload`: `Lookup`'s doc says "it is THAT method's bool — not this one — that becomes … `ErrUnknownUpload`", and `Deliver`'s says it constructs `ErrUnknownUpload` bare. Folding the miss into the error return would falsify both for no gain.

**Taking the chunk rather than `(connID, conversationID, attachmentID string)` is load-bearing.** Three adjacent same-typed strings at a call site are silently transposable, and this is the exact hazard `Deliver`'s doc already names — "Two ids in one call can disagree, and a caller passing the wrong one would route this chunk's bytes into a DIFFERENT transfer's accumulator". One struct in play forecloses it structurally.

`Deliver`'s body becomes:

```go
upload, ok, err := r.lookupAndStamp(connID, chunk)
if err != nil { return nil, err }      // mismatch: NOTHING released, NOTHING fed
if !ok { return nil, ErrUnknownUpload } // bare, as before
```

### Why the placement is the whole ticket

The reap runs first (unchanged — a mismatching chunk for an *expired* pair must answer `ErrUnknownUpload`, not the mismatch). Then the map read. Then the comparison. **Only then** the stamp and the store.

A comparison placed after the stamp is a **resource-exhaustion bug**, not a cosmetic one: `lastChunkAt` is `reapExpiredLocked`'s only input, so a client spamming mismatching chunks would renew its slot indefinitely and re-open exactly the path `uploadIdleTimeout` closes. This is what AC 1's "the incumbent untouched — **its idle stamp included**" means concretely, and it is the single thing a reviewer should check first.

### The sentinel and its wire code

`ErrConversationMismatch`, declared beside `ErrUnknownUpload` in `registry.go` — the precedent is `storage.go`, a sentinel living with its raiser, and neither package-wide var block fits (`accumulator.go`'s opens "returned by `Add` and `Assemble`"; `admission.go`'s three all refuse a *declaration*).

It joins `attachmentRejectFor`'s **existing** `rejectInvalidChunk` arm. No code is minted. #2143 already answers every bad-destination case — absent, and naming a conversation the daemon does not host — with `attachment.invalid_chunk`; answering a mismatch differently would split one client-visible class across two codes. Its published `retryable:false` is correct advice rather than merely tolerable: resending the same chunk against the same live transfer reproduces the refusal, and the repair is to finish the transfer under the conversation it was admitted under or start a new one.

### What this slice deliberately does not do

`Receive` still builds the path from its **`conversationID` parameter** while the registry latches and compares **`chunk.ConversationID`**. They are one value: the sole production caller passes the chunk's own field verbatim after gating it. No guard is added for a caller that passes two different values — that caller does not exist, and § Evidence-Based Fix Selection says not to ship a defence for an unobserved failure mode. `Receive`'s rewritten doc block states the relationship precisely rather than leaving it implied.

## Concurrency model

No new goroutine, no new lock, no shutdown path. Everything this slice adds runs inside `lookupAndStamp`'s **existing** acquisition of `Registry.mu`.

- **`mu` stays a leaf.** The comparison is a string equality on two values already in hand; it calls nothing.
- **The locked-method roster is unchanged at eight.** No method is added, none is removed, and none takes `mu` and then calls another that takes it.
- **`Deliver`'s acquisition count is unchanged and its published table gains a row:**

  | Outcome | Acquisitions |
  |---|---|
  | miss | 1 (`lookupAndStamp`) |
  | **destination mismatch** | **1 (`lookupAndStamp`)** — returns before `Add`, so no `Release` |
  | `Add` refuses | 2 |
  | `Assemble` → `ErrIncomplete` | 1 |
  | complete or integrity failure | 2 |

  Still "AT MOST TWO", and the new row is the cheapest one.
- **No TOCTOU.** The comparison, the latch, the stamp and the store are one critical section, exactly as the read-stamp-store already were. A compare under one acquisition and a deliver under another is the split-lock shape `Registry`'s type doc rejects for the capacity gate, and this slice must not introduce it.
- The by-value map entry is unchanged: the third field is a `string`, copied with the rest, so it still cannot be mutated off-lock.

## Error handling

- `ErrConversationMismatch` travels out of `Deliver` and through `Receive` **verbatim** — `Receive` wraps, annotates and reinterprets nothing, and interprets only `ErrIncomplete`. `errors.Is` reaches it unchanged at the relay.
- **The refusal constructs no format string.** It is a bare sentinel. `chunk.ConversationID` is client-authored and `AttachmentChunkPayload`'s SECURITY block plus `handleAttachmentChunk`'s own rule ("THE CONVERSATION ID IS NOT LOGGED ON ANY ARM") keep it out of every error string and every log line; a `fmt.Errorf` naming either conversation would breach both. The existing `v2.attachment.chunk.refused` record already logs the mapped code, the conn, the attachment id, the index and the total, and needs no edit.
- The client sees `attachment.invalid_chunk`, `retryable:false`, with the same static message every other arm answers — indistinguishable from the other destination refusals on the wire, which is the posture #2143 chose so the upload leg is not a conversation-existence oracle.

## Testing strategy

`go test -race ./internal/attachments/... ./internal/relay/...`, `go vet ./...`, `go build ./cmd/pyry`.

**RED first.** Every new registry test must fail before the production change — the mismatch is simply delivered today. The AC 3 clause ("a transfer whose chunks all name the same conversation is unaffected") is proven by the **existing** upload tests staying green, not by a new assertion, exactly as the AC asks.

New rows, as scenarios:

1. **`TestRegistry_DeliverMismatchedConversation_RefusesAndKeepsTheIncumbent`** — the mirror of `TestRegistry_AdmitRefusedRepeatUnderAHeldPair_KeepsTheIncumbent`. Admit a multi-chunk transfer (`boundChunk` fixtures — `CheckDeclaration` refuses a multi-chunk declaration of a short file, so multi-chunk is only reachable through the bound fixture), deliver chunk 0 naming A, then deliver chunk 1 naming B. Assert:
   - the error `errors.Is` `ErrConversationMismatch`, and the returned bytes are nil;
   - **the entry's `lastChunkAt` is exactly what chunk 0's delivery left it** — read through `lastChunkAt` under a fake clock advanced between the two deliveries. This is AC 1's stamp clause and it is the assertion that makes the placement measurable rather than by-construction;
   - the pair is still held and `count()` is 1 — nothing dropped;
   - **chunk 1 stored no bytes in the accumulator**: re-delivering chunk 1 under conversation A afterwards succeeds rather than answering `ErrDuplicateIndex`, and the transfer then assembles to the fixture. That is AC 2's "stores no bytes and does not release" as a *behavioural* assertion rather than an inspection of private state.
2. **`TestRegistry_DeliverMismatchedConversation_IsDistinctFromEverySentinel`** — the mismatch refusal wraps no member of `packageSentinels`. **`ErrConversationMismatch` is appended to `packageSentinels` in the same edit**; the fixture's own doc records that a sentinel added and not listed weakens every claim over it silently, and this slice is that ticket.
3. **`TestRegistry_DeliverMismatchedConversation_UnderAnExpiredPair_IsUnknownUpload`** — the reap runs ahead of the comparison, so an idle-expired pair answers `ErrUnknownUpload` and the chunk is not blamed for the destination. Pins the ordering of the reap against the new check.
4. **`TestIntake_ReceiveMismatchedConversation_RefusesAndStoresNothing`** — through the composite entry point: admit + deliver chunk 0 under A, deliver chunk 1 under B, assert the sentinel travels out of `Receive` verbatim and **the instance directory is unchanged** (no conversation-B directory, no file anywhere). AC 2's "stores no bytes" at the layer that writes them.
5. **`TestV2Session_AttachmentChunk_SentinelsMapToWireCodes`** — one row: `ErrConversationMismatch` → `attachment.invalid_chunk`, `retryable:false`. Table extension, not a new test.

**Mutants to run** (`go test -overlay`, unfiltered, `grep -a`, and check for `build failed` / `declared and not used` before trusting a verdict — all four traps are recorded in the package's mutation-lessons doc):

| # | Mutant | Predicted sole red |
|---|---|---|
| 1 | Comparison moved **after** the stamp-and-store | Test 1's `lastChunkAt` clause. This is the by-construction claim the package has paid for twice; it does not ship on an argument. |
| 2 | Comparison deleted entirely | Tests 1, 2, 4, 5 |
| 3 | Latch made unconditional (every chunk overwrites `conversationID`) | Tests 1, 4 — the "recorded, not re-read" half of AC 1 |
| 4 | Reap moved **after** the comparison | Test 3 alone |

Predict each red set from what each fixture **executes**, not from the test's name (#1784, #1817), and expect a superset rather than treating the prediction as the measurement.

## Open questions

1. **Does the mismatch refusal want its own `reason` in the daemon's log, the way `rejectAttachmentChunk`'s two destination arms have one?** Leaning no: those two arms refuse *before* the seam and share one call site, while this one arrives as a sentinel through `attachmentRejectFor` alongside eleven others, none of which carries a reason. Resolve while editing `v2session_attachment.go`; record in `## Revisions` if the answer changes the design.
2. **Do any of `Deliver`'s or `Receive`'s landed doc claims outside AC 4's four sites go stale?** `Lookup`'s "it is THAT method's bool … that becomes `ErrUnknownUpload`" is the near miss and survives by design (see § Design). Sweep `registry.go` for `lookupAndStamp` mentions before committing and fix any that the new signature falsifies — the package's culture is that prose is load-bearing, and AC 4 lists the sites that assert something *false*, not the complete set of sites this change touches.

## Doc sweep (AC 4)

Four sites, each of which asserts something this change makes false:

1. `internal/attachments/intake.go` → `Receive`'s doc — the "It is read on the COMPLETING CHUNK ONLY … accepted rather than mitigated" paragraph, which names this ticket as what closes it. Rewritten to state that the destination is fixed by the transfer's first delivered chunk and a later chunk naming a different one is refused, plus the parameter-vs-chunk-field relationship § Design leaves explicit.
2. `internal/attachments/registry.go` → `entry`'s doc and `Registry`'s type doc, each of which describes an entry as exactly "that pair's `*Accumulator` together with the time a chunk last arrived for it". A third field makes both incomplete. `Deliver`'s acquisition-count paragraph gains the mismatch row; `lookupAndStamp`'s doc gains the latch-and-compare and the ahead-of-the-stamp reason.
3. `internal/relay/v2session_attachment.go` → `attachmentRejectFor`'s "**NO ARM ANSWERS A BAD DESTINATION**, and that is #2143 rather than an omission" — false the moment a mismatch sentinel gets an arm. The surviving true half is that no arm answers a destination the daemon *cannot host*; that gate is still ahead of the seam.
4. `docs/protocol-mobile.md` → `attachment_chunk`'s `conversation_id` row, whose "Enforced since #2143" enumeration gains a third cause. A new dated changelog entry beside #2143's; **#2143's own entry stays as written** — it is dated history, and its "#2146 refuses the switch" clause becomes true rather than false.

`NewIntake`'s doc block is **not** one of them: it describes the pre-#2143 *cursor* misfile as history and says nothing about the mid-upload switch.

## Size check

| Limit | Boundary | This ticket |
|---|---|---|
| Production source files created or modified | ≤ 5 | **3** (`registry.go`, `intake.go`, `v2session_attachment.go`) |
| Total written work | ≤ 800 | **~700** (spec ~330, production ~90, doc prose ~90, tests ~180, protocol-mobile ~15) |
| New exported types or interfaces | ≤ 5 | **0 types**; one exported sentinel var |
| Consumer call sites needing simultaneous update | ≤ 10 | **1** (`Deliver`, the sole `lookupAndStamp` caller) |
| Acceptance criteria | ≤ 5 | **4** |
| Distinct error/reject branches in a state machine | ≤ 10 | **1** new |

Within every line. Nearest analogue #2142 landed 653.

## Security review

**Verdict:** PASS

**The property this whole slice rests on, stated as a chain** (checked before anything below, because every category's answer depends on it): the file lands under the latched destination. `Receive` files under its `conversationID` parameter; the sole production caller passes `chunk.ConversationID` verbatim; the completing chunk's `chunk.ConversationID` equals the latched value or it never reaches `EnsureDir` at all. Break any link and the check guards a value that is not the one becoming a path component.

**Findings:**

- **[Trust boundaries]** SHOULD FIX — the boundary is unmoved (`handleAttachmentChunk`'s two gate arms, per chunk, ahead of the seam) and the comparison is *downstream* of it: both values compared have already passed `KnownConversation`. But the latch stores a client-authored string on a registry entry for the first time, and **nothing in `entry`'s doc would stop a later ticket filing bytes under the latched value instead of the freshly-gated parameter.** That swap is not obviously wrong — the latched value passed the gate too — but it passed it on an *earlier* chunk, so a conversation deleted mid-upload would be filed under a stale-validated id where today the next chunk is refused. Phase B must state in `entry`'s doc that `conversationID` is a **comparison witness only and never a path input**. Not a MUST FIX: no code reads it that way today.
- **[Tokens, secrets, credentials]** No findings, and **not by absence** — `conversation_id` is published as "a lookup key validated against the daemon's registry … naming a conversation is not authorization", so the comparison must not be read as an authorization check and this plan says so under § Design's boundaries. **No `crypto/subtle` is needed and the reason is structural rather than a judgement call:** `uploadKey` carries the `connID`, so a conn can only ever reach entries it admitted itself — the latched value is always one this same conn chose. A timing side channel on `!=` would leak a value back to the party that supplied it.
- **[File operations]** No findings. The refusal returns from `Deliver`, and `Receive`'s `if err != nil` return sits **ahead of both `EnsureDir` and `Store`**, so a mismatching chunk reaches no filesystem call at all — AC 2's "stores no bytes" is structural, not a discipline. No path is built from anything this slice adds; `EnsureDir`'s `conversations.ValidID` check and its containment refusal are untouched.
- **[Subprocess / external command execution]** Not applicable — this slice executes nothing and touches no `exec.Command` path.
- **[Cryptographic primitives]** No findings. The integrity guarantee is unweakened and the reason is worth stating because it is the obvious smuggling question: a refused chunk **never reaches `Add`**, so it contributes no bytes, so `Assemble`'s declared-size and declared-digest comparison still covers exactly the accepted bytes. The refusal cannot be used to slip bytes past the digest.
- **[Network & I/O — resource exhaustion]** No findings, and this is the category the `security-sensitive` label is on. Walked concretely: a paired-but-hostile client admits a transfer under A and spams chunks naming B. Each one takes the handler gate (B is hosted — trivially arranged), skips `Admit` (pair held), and is refused inside `lookupAndStamp` **before the stamp**, so `lastChunkAt` stays at the last *delivered* chunk and `uploadIdleTimeout` still reclaims the slot. The mismatch path is also strictly *cheaper* than the accept path — no `Add`, no `Assemble` — so it adds no new CPU vector, and the reap it runs is the same `maxInFlightUploads`-bounded sweep every delivery already runs. **Net direction is an improvement:** today a mismatching chunk is delivered and therefore *renews* the slot. **One honest counter-direction, deliberate and bounded:** because the comparison runs ahead of `Add`, a mismatching chunk that would previously have released the transfer through a duplicate-index refusal now merely bounces, so the slot lives until the idle timeout instead of being freed immediately. That is exactly what AC 1's "nothing latched and nothing dropped" mandates, and `uploadIdleTimeout` is what bounds it.
- **[Error messages, logs, telemetry]** SHOULD FIX, and it resolves Open Question 1: **the mismatch gets no `reason` log field.** The sentinel is bare with no format string, and `chunk.ConversationID` must reach neither an error string nor a log line — `AttachmentChunkPayload`'s SECURITY block and `handleAttachmentChunk`'s own "THE CONVERSATION ID IS NOT LOGGED ON ANY ARM" both bind here, since this handler validates the id's membership and not its shape. Adding a reason field to this arm is the move that would tempt a later edit to name the value in it. On the wire the refusal is the existing static `attachment.invalid_chunk` / `retryable:false`, **indistinguishable from #2143's two destination arms** — which is what keeps the upload leg from becoming the conversation-existence oracle the retrieval leg deliberately closes.
- **[Concurrency]** No findings. No lock, goroutine or shutdown path is added; the comparison, the latch, the stamp and the store are **one critical section**, which is the property a split-lock shape would destroy and which `Registry`'s type doc rejects three separate times. The adversarial case walked: during the window where an entry is admitted but not yet latched (`conversationID == ""`), can a second feeder latch a conversation the transfer did not declare? It cannot — entries are per-conn, `Receive`'s single-feeder precondition is discharged by one `appFrameWorker` per session, and `mu` serialises the read-modify-write regardless, so the worst reachable outcome is that one chunk latches and the other is refused. **No byte lands under a conversation the transfer did not declare on either interleaving.**
- **[Threat model alignment]** No findings. § Security model's threat 1 does not land — no value this slice touches reaches a render surface or `claude`. Explicitly **out of scope and named**: containment stays `EnsureDir`'s, membership stays #2143's, and this slice adds neither. The published rule that naming a conversation is not authorization is unchanged, which is why the ticket's own Boundaries forbid restating this check as a security boundary — it detects two *valid* registry-validated ids disagreeing.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-06
</content>
</invoke>

## Revisions

### 2026-09-06 — `Receive` stamps the validated destination onto the chunk it delivers

**What changed.** § Design closed with "`Receive` still builds the path from its `conversationID` parameter while the registry latches and compares `chunk.ConversationID`. They are one value: the sole production caller passes the chunk's own field verbatim." Implementation added one line to `Receive` — `chunk.ConversationID = conversationID`, on the frame's own copy, ahead of the `Deliver` call — so the latched value and the value `EnsureDir` resolves are **the same value by construction** rather than by agreement between a caller and a callee.

**What drove it.** Two things, one of which the plan could not have known.

1. The plan's own § Security review raised this as a [Trust boundaries] SHOULD FIX: nothing stopped a later reader treating the latched id as a path input, and the three-link chain proving the compared value *is* the filed value ran through a caller contract rather than through code.
2. Writing the tests falsified the plan's factual premise. `uploadChunk` and `boundUploadChunk` in `intake_test.go` leave `ConversationID` **zero** and pass the destination as the argument only — so under the planned design every existing intake test would have latched `""`, matched `""`, and exercised a shape the wire never produces. The mismatch would have been unreachable at the `Intake` layer without a fixture that production does not resemble.

**The new contract.** The destination is `Receive`'s argument, full stop; the chunk's own field is decoration by the time the registry sees it. A caller that passed an argument disagreeing with the chunk gets its **argument** honoured — which is what `Receive`'s doc block has said since #2143 ("THE DESTINATION IS conversationID, AND ITS VALIDATION IS A PRECONDITION, AND IT IS THE CALLER'S") — rather than a refusal for a caller bug that no production path can produce.

**What it bought in tests.** `TestIntake_ReceiveMismatchedConversation_RefusesAndStoresNothing` now drives the mismatch exactly as the wire does — two `Receive` calls differing in their `conversationID` argument, which is what `handleAttachmentChunk` produces when a phone switches conversations mid-upload — instead of through a hand-built chunk. The registry-level tests still set the field directly, since `Deliver` is the layer where it is read.

**Open questions, resolved.** (1) The mismatch gets **no `reason` log field**; it arrives through `attachmentRejectFor` alongside eleven other sentinels, none of which carries one, and adding one is the move that would tempt a later edit to name a conversation id in it. (2) The `registry.go` sweep found `Lookup`'s "it is THAT method's bool — not this one — that becomes … `ErrUnknownUpload`" still true, which is why `lookupAndStamp` keeps its bool alongside the new error return.

### Mutants, measured

Run with `go test -overlay`, unfiltered, output `grep -a`'d, and checked for `build failed` / `declared and not used` before each verdict.

| # | Mutant | Predicted | Measured |
|---|---|---|---|
| 1 | Comparison moved **below** the stamp-and-store (still refuses, but stamps first) | Test 1's stamp clause | **Sole red** — `TestRegistry_DeliverMismatchedConversation_RefusesAndKeepsTheIncumbent`. The by-construction claim is measured, not argued. |
| 2 | Comparison deleted | Tests 1, 2, 4, 5 | Red on exactly those. **Measured as the pre-change RED run** rather than as a separate overlay: the code before this slice *is* that mutant, and it failed for the right reason — the mismatching chunk came back `ErrIncomplete`, i.e. delivered. |
| 3 | Latch made unconditional (every chunk overwrites the destination) | Tests 1, 4 | Red on tests 1, 2 and 4. Superset — test 2 also drives a mismatch to obtain the sentinel it checks distinctness of, per #1784's "predict from what each test executes, not from what it is named after". |
| 4 | Reap moved **below** the comparison | Test 3 alone | **Sole red** — `TestRegistry_DeliverMismatchedConversation_UnderAnExpiredPair_IsUnknownUpload`. |
