# #2002 — Bound the mapped slash-command frame to the v2 application-envelope cap

## Files read

- `internal/turnbridge/outbound.go` → `MapEvent`'s `turnevent.SlashCommandList` arm — the code this slice amends; its "A FRAME-LEVEL SIZE BOUND IS DELIBERATELY ABSENT" and "NOTHING IS RE-CAPPED" paragraphs are the two that move with it.
- `internal/turnbridge/outbound.go` → `inputFields` — the budget-subtraction loop shape already in this file (a running budget, a per-item cost, `break` on the first item that does not fit). The new cut copies it.
- `internal/turnbridge/outbound_test.go` → `TestToolResultPayload_FitV2EnvelopeCap`, `maxV2AppEnvelope` — the per-frame cap test's shape, and the test-local constant whose doc this slice must reconcile.
- `cmd/pyry/interactive_turn_v2.go` → `maxDeltaTextBytes` — the constant half of the belt-and-suspenders pairing: a named budget whose doc itemises the reserve it leaves for everything outside the bounded field, and which bounds the INPUT rather than the envelope.
- `internal/protocol/interactive.go` → `SlashCommandListPayload`, `SlashCommandListPayload.MarshalJSON`, `SlashCommand`, `SlashCommand.MarshalJSON` — what the wire bytes actually are. Both normalisers run inside the measurement, which is why the measurement marshals the *protocol* row and not the *turnevent* one.
- `internal/streamsup/parser.go` → `maxSlashCommandListEntries` — the producer's entry cap, and one of the two doc blocks AC 5 corrects.
- `internal/turnevent/event.go` → `SlashCommandList`, `SlashCommandList.DroppedCommands` — the source event, and the other AC-5 doc block plus the one sentence in the type doc pointing at it.
- `internal/protocol/interactive.go` → `SlashCommandListPayload`'s own doc — carries a third claim this commit falsifies ("carries ... onto this field **verbatim**"). Not named by AC 5; see § Open questions.
- `docs/knowledge/features/streamsup-package-producing-turnevent-slashcommandlist.md` — three lessons that bind here: an equality against a fixture's own length is not a headroom proof (AC 3's exact shape); the realistic array size is **11,403** daemon-side bytes, not the raw-claude 14,277; re-derive a cited figure rather than transcribing it.
- `docs/knowledge/features/turnbridge-package.md` § `MapEvent` mapping table — records the absent bound as deliberate and names this ticket as its owner. Owned by the documentation phase; **not edited here**.

## Context

`MapEvent`'s `SlashCommandList` arm (#2001) is a verbatim translation with no size decision in it. The producer bounds every *content* dimension — four per-field byte caps, an alias-count cap, an entry-count cap — and the product of those bounds still does not fit a frame: 128 entries at the 1,280-byte per-entry term is 163,840 **raw** bytes against a 65,519-byte envelope, and Go's HTML escaping puts the true wire worst case several times higher again. No count cap closes that, which is why `maxSlashCommandListEntries`' own doc hands the frame axis forward.

The bound belongs in the mapping arm because both paths to the wire go through `MapEvent`: #2003's live-lane emission and #2005's connect-time resolver. One bound covers both; a bound at either consumer would be a second place the limit is decided.

No ADR is warranted — this is a constant and a loop inside an existing arm, decided by the same reasoning `maxDeltaTextBytes` already records for the sibling axis.

## Design

### The constant

`maxSlashCommandListBytes`, unexported, in `internal/turnbridge/outbound.go`.

It bounds the **serialised `commands` array**, brackets included — not the envelope, and not an entry count. Two things it deliberately is not:

- **Not `maxV2AppEnvelope`'s 65,519.** `maxDeltaTextBytes`' rule applies unchanged: the constant bounds the bounded field, and the difference is the reserve for everything outside it — the envelope frame (`id`, `type`, `ts`, `payload`, `event_id`) and the payload's own `conversation_id` and `dropped_commands` keys. A production constant named `maxV2AppEnvelope` is additionally a redeclaration against the test-local one already in `package turnbridge`.
- **Not an entry count.** 65,519 / 1,280 = 51 and the committed capture is 51 entries, so a worst-case-derived count cap fires on claude's ordinary output. The cut must be measured.

Its doc states: the arithmetic it defends against (128 × 1,280 = 163,840 raw bytes, and that this is raw-byte arithmetic rather than a wire measurement — Go escapes `<`, `>`, `&` and U+2028/U+2029 at six bytes each, so the wire worst case is higher still); the itemised reserve, with the measured figure from the cap test; and that the branch is unreachable on claude's ordinary output — the committed capture's array is ~11,403 daemon-side bytes, well under the budget — so it is a bound rather than dead code.

### The cut

Inside the existing arm, replacing the unconditional append loop. Contract:

- Walk `e.Commands` in order, building the `protocol.SlashCommand` row exactly as today.
- Cost of a row is `len(json.Marshal(row))`, plus one byte for its separating comma when it is not the first kept row; the running total starts at 2 for `[` and `]`. Marshalling the **protocol** row, not the turnevent one, is what makes the measurement the wire's: `SlashCommand.MarshalJSON`'s nil-`Aliases`→`[]` normalisation and the JSON key names are both inside it.
- `break` — never `continue` — on the first row whose cost would carry the total past `maxSlashCommandListBytes`. Skipping a large row to fit later small ones would violate "cut from the tail", make the retained set order-dependent, and present a hole rather than a truncation to a client matching against the sibling `slash_commands` name list.
- `DroppedCommands` on the payload is `e.DroppedCommands + (len(e.Commands) - len(kept))`. Added to, never replacing: `len(commands) + dropped_commands` stays the list's true size across both cuts.

Everything else in the arm is unchanged: order is claude's, no re-cap of any field, no charset check, nil `Commands` still forwarded rather than pre-allocated, `TruncatedFields` nil still load-bearing, no suppression branch.

The measurement is an equality worth pinning rather than assuming: `len(json.Marshal(payload.Commands))` equals the running total the loop computed, because `encoding/json` compacts an already-compact `MarshalJSON` result byte-for-byte.

## Concurrency model

No goroutines. `MapEvent` stays a pure value-to-value adapter; the package doc's "no I/O, no state" is untouched, since `json.Compact` and `json.Unmarshal` already run inside this function's tool-input handling.

The arm's carry-never-mutate rule tightens rather than relaxes: the cut builds a **fresh outer slice** and stops early, so it neither sorts, dedupes, filters in place, nor reslices the event's own `Commands`. `Aliases` and `TruncatedFields` still cross as slice headers sharing the event's backing arrays — read-only on both sides. This binds now because #2005 reads a mapped payload on a relay-leg goroutine.

## Error handling

`json.Marshal` of a `protocol.SlashCommand` cannot fail in practice: the type is four strings and two string slices, and the encoder coerces invalid UTF-8 to U+FFFD rather than rejecting it. Go still forces the branch, and it is handled **fail-closed** — `break`, counting that row and every row after it as dropped. An entry that cannot be measured cannot be admitted to a measured budget, and the frame stays sendable either way.

That same `break` covers the state the ticket forbids inventing a path for. A row larger than the whole budget is unreachable under the producer's caps (~7,700 escaped bytes at worst against a ~64 KB budget), but were a future producer to hand one over, the loop drops it like any other over-budget row and the frame still fits. No "nothing fits" branch is written, because none is needed for that outcome.

The error branch constructs no error value and writes no log line — see § Security review.

## Testing strategy

All in `internal/turnbridge/outbound_test.go`. No fixture, assertion or helper hardcodes a list length taken from a real claude; every fill is synthetic.

- **`TestSlashCommandListPayload_FitV2EnvelopeCap`** — the suspenders, copying `TestToolResultPayload_FitV2EnvelopeCap`'s shape. Drives the real `MapEvent` with a worst-case event: `maxSlashCommandListEntries` rows, each field filled to the producer's own cap with `<` so every byte costs six on the wire, a full alias array, a populated `TruncatedFields`. Marshals the result into a worst-case `protocol.Envelope` (max-uint64 ids, populated `EventID`) with a hostile 64-rune `conversation_id`, logs the byte count and the percentage of the cap, and asserts it is under `maxV2AppEnvelope`. This is what supplies the reserve figure the constant's doc quotes.
- **Nothing-cut row, with a headroom guard** — an under-budget list maps byte-identically to what the unbounded arm produced: every row present, in order, `DroppedCommands` carried unchanged. Preceded by a `t.Fatalf` precondition asserting the fixture's own measured array size is comfortably under the budget, so a fixture that grows to meet the budget reddens and names itself as the cause. The equality alone holds at any budget and proves no headroom — the lesson `docs/knowledge/features/streamsup-package-producing-turnevent-slashcommandlist.md` records.
- **Over-budget row** — the kept rows are a prefix of the input (tail cut, order preserved), the measured `len(json.Marshal(payload.Commands))` is within the budget, and at least one row was cut.
- **Additive drop row** — an over-budget list arriving with a non-zero `DroppedCommands` reports the sum, and `len(Commands) + DroppedCommands` equals the input's true size.
- **Non-mutation** — `TestMapEventSlashCommandListDoesNotMutateTheEvent` already stands and must stay green with a cut firing.

Gate: `go test -race` on `internal/turnbridge`, `internal/streamsup`, `internal/turnevent`, `internal/protocol`; `go vet ./...`; `go build ./cmd/pyry`.

## Open questions

1. **Does `internal/protocol/interactive.go` need editing?** AC 5 names exactly two doc blocks plus one sentence, none of them in `protocol`. But `SlashCommandListPayload`'s doc claims the arm carries the count "onto this field **verbatim**, never recomputed from len(Commands)", which AC 2 falsifies. Resolution: correct it. It is one sentence, and it is the fourth production file — measured, labelled `needs-human:sizing` and commented on the ticket, per the depth-capped path.
2. **What reserve does the constant leave?** Fixed in Phase B from the cap test's measured output, not guessed here. Record the number in the constant's doc and in a `## Revisions` entry if it moves the constant.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] No findings, but the boundary moves and the plan says where. `Name`, `ArgumentHint`, `Description` and every alias are workspace-authored — a lower-trust origin than claude's own strings — and this slice adds one new *transient* consumer of them: the `json.Marshal` call inside the cut. Its output is used for its **length only** and is discarded; it is not the bytes that ship, not compared against anything, and not retained. The render boundary owing sanitization stays the CLIENT's, exactly as `SlashCommand`'s and `turnevent.SlashCommandList`'s SECURITY paragraphs assign. `DroppedCommands` remains the one daemon-derived value here.
- [Tokens, secrets, credentials] Not applicable by construction: this path carries a slash-command inventory and a conversation id. No credential, nonce or key is read, minted or compared anywhere in the arm.
- [File operations] Not applicable: `MapEvent` is a pure value-to-value adapter. No path is built, opened, stat-ed or written; nothing here reaches `filepath.Join` or `filepath.Match`.
- [Subprocess / external command execution] No findings, and the invariant is worth restating because the values are command *names*. This slice adds exactly one sink to the enumeration `maxSlashCommandName` owns — a field of a `protocol.SlashCommandListPayload` — plus the transient marshal above. No `exec.Command` argument, no argv element, no `sh -c`. Publishing a name still grants nothing; the frame declares no inbound verb.
- [Cryptographic primitives] Not applicable — no randomness. Worth stating as a design property rather than an absence: the cut must be **deterministic**, and it is, because it walks a slice in claude's order and takes a prefix. A map iteration or any randomised selection would make a client's menu flicker between two frames of the same list.
- [Network & I/O] This category is the deliverable, and it carries the one finding that shapes the design. **Counting entries is not a bound.** Go escapes `<`, `>`, `&` and U+2028/U+2029 at six bytes each, so a workspace author whose descriptions are markup-dense produces a frame several times the size a raw-byte count predicts — a workspace-triggerable path to an unsendable menu, which is the user story's own failure. The cut is therefore measured against marshalled bytes, post-escaping, which is what actually crosses. Two consequences the plan takes on: the reserve is validated against a **hostile** `conversation_id` rather than a 36-byte UUID (SHOULD FIX — the cap test fills it to 64 runes of `<`, as `TestToolResultPayload_FitV2EnvelopeCap` does, and the verifier should check that landed); and resource use is bounded by the budget itself, since the loop `break`s on the first over-budget row and so marshals at most the rows that fit plus one, regardless of how many a producer hands it.
- [Error messages, logs, telemetry] SHOULD FIX, and it is the trap this arm is most likely to fall into. The arm writes no log line today, by rule, because every string in it is workspace-authored. The new `json.Marshal` error branch must not become the exception: it constructs **no error value wrapping the row, and no log attribute** — it breaks and counts a drop. A `fmt.Errorf("marshalling %q: %w", c.Name, err)` would put a workspace string on a path the whole arm exists to keep it off, and would do it in the one place a reader would call it good practice. The drop count is daemon-derived and is the one value here safe to log, if a caller ever wants one.
- [Concurrency] No findings. No goroutine is spawned, so no lifecycle to leak. The carry-never-mutate rule is held by construction: a fresh outer slice, an early `break`, no in-place sort, dedupe or filter, and no reslice of the event's own `Commands`. `Aliases` and `TruncatedFields` still share backing arrays with the event, read-only on both sides — which is why the cut must not be implemented as a reslice of `e.Commands`, since #2005 reads a mapped payload on a relay-leg goroutine while the event may be retained elsewhere.
- [Threat model alignment] Addressed for the relevant threat in `docs/protocol-mobile.md` § Security model — untrusted workspace text reaching a client, bounded but not sanitized, with the client owning the render boundary. Two threats are named out of scope: the frame's **delivery** guarantees and its capability gating are #2003's, and the connect-time snapshot is #2005's; both are `blockedBy` this ticket, so neither can ship an over-cap frame ahead of this bound. `docs/protocol-mobile.md` itself is #2010's.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-02
