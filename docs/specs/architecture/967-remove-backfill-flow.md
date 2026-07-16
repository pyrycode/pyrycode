# Spec #967 — Remove the dead `backfill_since` wire flow

**Ticket:** [#967](https://github.com/pyrycode/pyrycode/issues/967) — *protocol: `backfill_since` is allowlisted and documented but has no handler — remove or implement*
**Size:** S · **Resolution:** REMOVE · **Security-sensitive:** No (see § Security note)

---

## Context

`backfill_since`, `message_chunk`, and `backfill_done` are the three wire types of the v1 bulk-history backfill flow (`backfill_since` → `message_chunk*` → `backfill_done`). All three are **dead in production**: zero emitters, zero handlers. A client sending `backfill_since` falls through both v2 dispatch surfaces to a generic `protocol.unsupported` reply. This is the same latent class as #949 (`promote_conversation`), but the **opposite resolution**: #949 had live shipping-client senders → it was *implemented*; the backfill flow has **no senders anywhere** (cross-repo pre-verified in `pyrycode-mobile` / `pyrycode-desktop` during refinement, and re-confirmed in-repo during this spec: no production code constructs `BackfillSincePayload` / `MessageChunkPayload` / `BackfillDonePayload`, and no dispatch code references the three `Type*` constants). The v2 reconnect path (ADR 025 / event ring #646/#647: `request_snapshot` + `hello.last_event_id` bounded-ring replay) replaced backfill entirely; no message-history store exists that a handler could read from. #913 ("Remove the v1 relay dispatch branch") is CLOSED/shipped and removed the v1 dispatch code but left these orphaned type constants behind — this ticket is the cleanup it didn't cover.

**This is one dead flow, not one dead verb.** The three types share one guard-exclusion block and sit on single lines in three `compat_test.go` lists; removing only one leaves the others referencing an undefined constant → the package would not compile. The coherent, buildable scope is all three together. Completeness is **compiler + guard enforced**, not coordination-dependent: if the developer misses any reference, `go build` or a guard test fails loudly and points at the exact line.

---

## Files to read first

Every edit site lives in `internal/protocol` (leaf data package), one guard test in `cmd/pyry`, and one doc. There is no call graph (dead code has zero callers) — the deterministic surface is the token grep, not codegraph.

| Path / lines | What to extract |
|---|---|
| `internal/protocol/codes.go:141-144` | The `// Backfill.` block: three `Type*` const declarations to delete. |
| `internal/protocol/messaging.go:26-34, 67-81` | The three payload structs + doc comments to delete (`BackfillSincePayload`, `MessageChunkPayload`, `BackfillDonePayload`). |
| `internal/protocol/messaging.go:52-53` | `SessionTransitionPayload.WorkspaceCwd` comment — **cites `BackfillSincePayload.ConversationID`** as its `*string`-no-omitempty precedent; a stale ref after deletion (see § Comment cross-refs). |
| `internal/protocol/envelope.go:118-145` | `v1TypeSet` map literal — three entries (`TypeBackfillSince`/`TypeMessageChunk`/`TypeBackfillDone` at 141-143) to delete. Note the package doc contract: v1TypeSet is the closed accept-set. |
| `internal/protocol/settings.go:22-31` | `SetSessionSettingsPayload` comment — its sibling-precedent parenthetical **names `BackfillSincePayload`** (AC #2 site 1). |
| `internal/protocol/handshake.go:26-27, 55` | `HelloClientPayload.LastSeenTS` comment cites `§ Backfill semantics` (AC #2 site 2). **`LastSeenTS` (`:55`) is a LIVE field — keep it**; only re-cite the section. |
| `internal/protocol/messaging_test.go:82-118, 204, 212-~275` | Three round-trip test funcs to delete; the `:204` comment (in the *surviving* `TestSessionTransitionPayload_RoundTrip`) says `mirrors backfill_since's guard` — a stale ref (see § Comment cross-refs). |
| `internal/protocol/compat_test.go:21, 119, 122, 202` | Three lists each carry `TypeBackfillSince, TypeMessageChunk, TypeBackfillDone,`; **line 122 hard-codes the count `26`** (must become `23`). |
| `cmd/pyry/relay_guard_test.go:60-135` | The completeness guard `TestEveryInboundV2TypeHasHandler`. The three types are in `excludedTypes` "v1-legacy" (`:129-134`) — the **2nd totality guard** (Assertion #3 inverse); see § Guard. |
| `docs/protocol-mobile.md:29, 421-423, 602, 853-870` | Type list (29), descriptor rows (421-423), deferred-handler note (602), the `## Reconnect / Backfill semantics` section + Mode-A bullet (853/859). **Keep the section heading** (anchor). |
| `internal/protocol/testdata/{backfill_since,message_chunk,backfill_done}.json` | Three fixtures to delete. |

---

## Design — deletion inventory

Pure deletion + four comment rewords. No new types, no logic, no goroutines, no error paths. Organized by file.

### Production `.go` (5 files)

1. **`codes.go`** — delete the `// Backfill.` comment and the three const lines (`:141-144`).
2. **`envelope.go`** — delete the three `v1TypeSet` entries (`:141-143`). This *shrinks* the untrusted accept-set: the verbs now reject earlier as `ErrUnknownType`.
3. **`messaging.go`** — delete the three payload structs with their doc comments (`BackfillSincePayload` 26-34, `MessageChunkPayload` 67-74, `BackfillDonePayload` 76-81). **Also** reword the `SessionTransitionPayload.WorkspaceCwd` comment (`:52-53`) — see § Comment cross-refs.
4. **`settings.go`** — reword the comment at `:23`: drop `BackfillSincePayload` from the sibling parenthetical, leaving `SessionTransitionPayload` (which still exists and uses the same `*string`-no-omitempty idiom).
5. **`handshake.go`** — reword the citation at `:27`: `§ Backfill semantics` → `§ Reconnect / Backfill semantics` (the surviving heading). **Do not touch the `LastSeenTS` field** — it is live wire vocabulary this ticket does not remove.

### Tests + fixtures

6. **`messaging_test.go`** — delete the three round-trip test funcs (`TestBackfillSincePayload_RoundTrip`, `TestMessageChunkPayload_RoundTrip`, `TestBackfillDonePayload_RoundTrip`). Reword the `:204` comment (see § Comment cross-refs).
7. **`compat_test.go`** — remove `TypeBackfillSince, TypeMessageChunk, TypeBackfillDone,` from all three lists (`:21`, `:119`, `:202`) **and update the hard-coded count `26 → 23`** (`:122`).
8. **`relay_guard_test.go`** — delete the `// v1-legacy` comment block + three `excludedTypes` entries (`:129-134`). See § Guard.
9. **`testdata/`** — `git rm` the three JSON fixtures.

### Docs — `protocol-mobile.md`

10. Remove the three verb **names** from: the inline type list (`:29`), the three descriptor-table rows (`:421-423`), the deferred-handler clause (`:602` — trim `; the dedicated backfill_since reload handler is a deferred follow-up — it needs a message-history store that does not exist yet`, ending the sentence at `a full reload (today via a fresh subscription)`), and the Mode-A bullet (`:859` — reword so it no longer names the three verbs; the surviving `hello.last_event_id` event-ring bullet at `:860` is the live mechanism).
    - **KEEP the section heading `## Reconnect / Backfill semantics` (`:853`) verbatim.** It preserves the anchor `#reconnect--backfill-semantics`, keeping all referrers (~635, ~695, ~860, ~870) resolvable — the low-risk path AC #4 offers. Do **not** rename it.
    - **Leave `:386`** (`… to trigger backfill`, bare word, no verb name) as-is — the conceptual reconnect/backfill section still exists, it is not gated by AC #1, and touching it widens scope without benefit.
    - Run `qmd update && qmd embed` after the doc edit (CLAUDE.md convention).

---

## Comment cross-refs — there are FOUR sites, not two

AC #2 enumerates two (`settings.go:23`, `handshake.go:27`). Two more contain a removed token and are therefore caught by **AC #1's grep**, so the developer must fix all four or AC #1 fails:

| Site | Stale text | Fix |
|---|---|---|
| `settings.go:23` *(AC #2)* | `(BackfillSincePayload, SessionTransitionPayload)` | Drop `BackfillSincePayload,`; keep `SessionTransitionPayload`. |
| `handshake.go:27` *(AC #2)* | `§ Backfill semantics` | Re-cite `§ Reconnect / Backfill semantics` (surviving heading). |
| `messaging.go:52-53` *(caught by grep)* | `mirroring BackfillSincePayload.ConversationID` | Describe the `*string`-no-omitempty idiom directly (this comment is *on* `SessionTransitionPayload`, now the canonical example — no external citation needed). |
| `messaging_test.go:204` *(caught by grep)* | `(mirrors backfill_since's guard)` | Reword to name no removed symbol, e.g. "the same `*string`-without-omitempty regression check". |

The point: do not stop at AC #2's two. The grep is the real gate and it catches all four.

---

## Guard — the 2nd totality guard (`relay_guard_test.go`)

`TestEveryInboundV2TypeHasHandler` reads `codes.go` for every `Type*` constant and asserts each is classified in exactly one of `inboundTypes` / `excludedTypes` (Assertion #3), and — the **inverse** (`:199-203`) — that every classified name still maps to a real constant. The three backfill types are in `excludedTypes` as `"v1-legacy"` (`:132-134`).

Removing the constants from `codes.go` **without** deleting these `excludedTypes` entries trips the inverse: *"classified but is not an application `Type*` constant … a removed/moved constant?"*. So the guard edit is mandatory and coupled to the `codes.go` deletion. After deleting both, Assertion #3 and its partition invariant hold (AC #3). This is the removal-direction twin of the known "new `Type*` trips the 2nd totality guard" pattern — specs routinely forget this file.

The analogous partition test in `compat_test.go` (`TestTypeConstants_V1V2Partition`) is self-consistent under the removal: it drops 3 from both the `all` list and `v1TypeSet`, so its computed union check `len(v1TypeSet)+len(v2OnlyTypes) == len(all)` still balances — **no** magic number there. The only hard-coded count is `compat_test.go:122` (`26 → 23`).

---

## Testing strategy / verification

- **AC #1 grep is unsatisfiable as literally written — this is the single biggest trap.** `message_chunk` / `MessageChunk` are **substrings of the live, unrelated `agent_message_chunk` / `AgentMessageChunk`** (ACP bridge: `internal/acpbridge/outbound{,_test}.go`, `cmd/pyry/acp_*.go`). The ticket's literal `grep -rIn "…" internal/ cmd/ docs/` will **always** return those ACP hits, even after a perfect removal. **Do not touch acpbridge / acp_* — those verbs are live and out of scope.** Verify removal with word-boundary anchoring (BSD/macOS grep uses `[[:<:]]` / `[[:>:]]`, not `\b`):
  ```
  grep -rInE "[[:<:]](backfill_since|message_chunk|backfill_done|BackfillSince|MessageChunk|BackfillDone)[[:>:]]" internal/ cmd/ docs/
  ```
  This must return **nothing** — it excludes the `agent_message_chunk` family by construction (verified: the word-boundary form already yields zero non-backfill matches on main).
- **Guard:** `go test ./cmd/pyry/ -run TestEveryInboundV2TypeHasHandler` passes with the `excludedTypes` block deleted (AC #3).
- **Partition/count:** `go test ./internal/protocol/` passes after the `26 → 23` edit (the failing message is `type-list length: got 23, want 26` if forgotten).
- **Doc anchor:** confirm `#reconnect--backfill-semantics` still has a matching heading and that no referrer points at a removed sub-section.
- **`make check`** green (`go build`, `go vet`, `staticcheck`, `go test -race ./...`) — AC #5.

---

## Concurrency model

None. `internal/protocol` is a pure, stdlib-only leaf data package: no goroutines, no context, no I/O. The change is deletion of type declarations, test functions, fixtures, and documentation.

## Error handling

None introduced. The removal *narrows* one failure mode: a `backfill_since` frame that previously reached `protocol.unsupported` now rejects one step earlier at `IsV1Compatible` as `ErrUnknownType` (unrecognized type). No new parsing, no recovery paths.

---

## Security note (not security-sensitive)

Confirmed **not** security-sensitive, matching the ticket and the "REMOVE resolution" rule: deleting three never-handled entries from the `v1TypeSet` allowlist *tightens* the untrusted-wire accept-set — the verbs are rejected earlier as unknown. No new inbound-content parsing, no loosened validation gate, no crypto/nonce surface. This is the mirror of #949: the *implement* resolution there added a handler over an untrusted client-supplied `conversation_id` (a new inbound-content surface) and carried the label; *removal* does not. Because the ticket lacks the `security-sensitive` label, no security-review pass runs.

**Safety valve (unchanged from ticket):** if implementation surfaces a live consumer contrary to the cross-repo + in-repo pre-checks, **stop and route back to PO (`needs-rework:po`)** — do not add an inbound handler inline. A `backfill_since` handler would parse an untrusted client-supplied `conversation_id` and return message content (cross-conversation over-fetch risk) and presupposes a message-history store that does not exist — a separate, security-sensitive ticket.

---

## Scope check (§ transparency)

- **§1 red lines — all clear:** 0 new files, net *deletion* (~40 production LOC + ~90 test LOC removed, 3 fixtures deleted), 0 new exported types, 0 production consumer call sites (the defining property of dead code), 5 ACs that are facets of one deletion, 0 state-machine reject branches.
- **§4 file-count gate — 5 production `.go` files, override documented:** the count reaches 5 (`codes.go`, `envelope.go`, `messaging.go` are deletions; `settings.go`, `handshake.go` are **single-line comment rewords** with zero logic). This trips the `≥5` line by the letter, but the gate is a proxy for the developer's turn budget, and for a **pure compiler-enforced deletion the proxy over-predicts**: there is no new logic, no cascade to reason about (`go build` is the cascade tracker), and the flow is mechanically coupled so a split would produce a non-compiling or dangling-comment intermediate state with no reduction in total work. PO sized this S on exactly this basis (coupled dead flow, compiler+guard enforce completeness = no coordination risk). Proceeding as one S is the correct, PO-aligned call; the count is recorded here honestly rather than argued down.
- **File-overlap check:** the only flagged sibling branch (`origin/feature/449`, on `codes.go`) is a **stale false positive** — CLOSED 2026-05-17, 1138 commits behind main, and its sole `codes.go` change (`TypeRekeyRequest`) already landed on main via a different path. Not a live merge target → no block.

## Open questions

None blocking. The one judgment already made and recorded above: preserve the `## Reconnect / Backfill semantics` heading (vs. renaming + updating every referrer) — chosen for minimal blast radius per AC #4.
