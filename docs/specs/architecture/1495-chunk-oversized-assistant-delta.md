# #1495 — Chunk the coalesced `assistant_delta` so no v2 envelope exceeds the 65519-byte cap

**Size:** S (confirmed, not overridden). One production file, one new test file, no signature change, no consumer call sites.

## Files to read first

Symbols, not line numbers — resolve each with `codegraph_search` / `codegraph_node`.

| File | Symbol | What to extract |
|---|---|---|
| `cmd/pyry/interactive_turn_v2.go` | `flushDelta` | The whole contract you are changing: no-op-on-empty, one `emitMapped`, one `seq++`, reset, `flushTimer.Stop()`. Every line of its doc comment is load-bearing and needs updating. |
| `cmd/pyry/interactive_turn_v2.go` | `coalesceWindow` | Where the new constant goes, and the file's comment register for a tunable. |
| `cmd/pyry/interactive_turn_v2.go` | `emitMapped`, `emit` | `emitMapped` reads `e.seq` off the struct (so `seq++` must land *between* chunk emits, not before or after the loop); `emit` mints one ring `eventID` and one per-conn `env.ID` per call. Both stay unchanged. |
| `internal/relay/v2bundlestream.go` | `bundleChunkBytes`, `bundleEnvelopes` | **The pattern this ticket mirrors.** The cap-constant comment style (observed expansion, arithmetic, "if the test fails, LOWER this — never raise it") and the ceil-division chunk loop. |
| `internal/relay/v2bundlestream_test.go` | `TestStreamBundle_EveryFrameWithinCap` | The "different fabric" per-frame cap test this ticket's cap test mirrors. |
| `internal/protocol/interactive_test.go` | `maxV2AppEnvelope`, `TestBackgroundTaskPayloads_FitV2EnvelopeCap` | The test-local `65519` constant convention (do **not** export one) and the `<`-fill rationale — including the two secondary facts worth restating: a NUL fill measures identically, and a 4-byte emoji fill measures like `a` because Go emits multi-byte runes raw. Copy the reasoning; write your own code. |
| `internal/protocol/envelope.go` | `Envelope` | The fields that ride *outside* the payload — `id`, `type`, `ts`, `payload`, `event_id`. This struct's marshalled form is exactly what the cap measures. |
| `internal/protocol/interactive.go` | `AssistantDeltaPayload` | The four payload fields and their JSON names; no `omitempty` anywhere, so all four always cost bytes. |
| `internal/turnbridge/outbound.go` | `MapEvent` | Confirms `TextChunk.Text` crosses to `AssistantDeltaPayload.Text` verbatim with no cap. **Do not add one here** — see § What not to touch. |
| `internal/relay/v2session.go` | `forwardEnvelope`, `marshalInnerFrameV2` | Read to confirm the cap surface is the marshalled `protocol.Envelope` (`envJSON`), before `Encrypt` adds the 16-byte tag. **Do not modify** — explicitly out of scope. |
| `cmd/pyry/interactive_turn_v2_test.go` | `fakeInteractiveBcast`, `assistantDeltas`, `pushTypes`, `stubCursor`, `discardLogger`, `testConvID` | The harness the new tests reuse (same package, no new doubles needed). `fakeInteractiveBcast.pushes` records the exact `protocol.Envelope` that `forwardEnvelope` would marshal — that is the AC #1 measurement point. |
| `cmd/pyry/interactive_turn_v2_test.go` | `TestInteractiveTurnEmitterV2_OneSeqPerCoalescedDelta`, `TestInteractiveTurnEmitterV2_NoAppOutputLogLeak` | Two of the tests that must stay green **unmodified** — see § Stays green. |
| `internal/eventring/ring.go` | `MaxEventsPerConversation` | 1024 per conversation; chunking now consumes N slots where it consumed 1. Read for the note in § Interactions. |
| `internal/relay/v2session_modal.go` | `queuedEnv` | The `droppable = env.Type == protocol.TypeAssistantDelta` policy (#610). Read for the note in § Interactions. |
| `docs/protocol-mobile.md` | § "Application-envelope size cap" and § `assistant_delta` | The 65519 derivation, and the field table that **already** permits N deltas per turn — which is why this ticket changes no wire contract. |

## Context

`docs/protocol-mobile.md` § Application-envelope size cap caps the decrypted v2 application envelope at 65519 bytes (Noise's 65535-byte transport message minus the 16-byte AEAD tag). Nothing on the outbound assistant-text path enforces it: `flushDelta` emits the whole `deltaBuf` as one `assistant_delta`, `MapEvent` passes `Text` verbatim, and `forwardEnvelope` marshals-and-seals with no size check. `flynn/noise`'s `CipherState.Encrypt` checks only `invalid` and `MaxNonce`, so there is no accidental backstop either.

Two things make one flush larger than one claude text block: the coalescer **concatenates** every `TextChunk` sharing a `MessageID`, and a single stream-json line is bounded only by `defaultMaxParseBuf` (4 MiB). So the upper bound on one flush is megabytes.

The user-visible failure is a completed, empty turn: a spec-conforming phone rejects the over-cap frame while `turn_state` / `turn_end` arrive normally, and the `eventring` retains the oversized event so every mid-turn reconnect replay re-fails identically.

`internal/streamsup/parser.go`'s cap constants (`maxUnrecognizedRaw`, `maxTaskFieldID`, `maxTaskDescription`, `maxTaskPatch`) each size themselves against 65519 with the arithmetic in the comment. Assistant text is the one claude-derived string with no such cap. This ticket adds it at the only point that sees the concatenated size — the flush.

## Design

Three changes, all in `cmd/pyry/interactive_turn_v2.go`. No new file, no new type, no exported symbol, no signature change.

### 1. `maxDeltaTextBytes` — a conservative raw-byte constant

A package-level `const maxDeltaTextBytes = 10000`, placed next to `coalesceWindow`, with the arithmetic written into its comment in `bundleChunkBytes`'s style.

**The arithmetic to write into the comment** (the developer verifies it with the cap test's `t.Logf`, see § Testing):

- Worst-case JSON expansion is **6 bytes out per input byte**. `encoding/json` has `SetEscapeHTML` on by default, so `<`, `>`, `&` each become a six-byte `\u00XX` form, as does every control byte without a short escape. `"` and `\` are 2×. Multi-byte runes are **not** the worst case — Go emits them raw, so a 4-byte emoji stays 4 bytes. 6 is the true ceiling because the cut is a **byte** cut.
- `10000 × 6 = 60000` bytes of escaped text.
- Non-text overhead, budgeted at **512 bytes**: payload keys and punctuation ≈ 52 B, `conversation_id` (36 B UUID; budget 64), `turn_id` (36 B UUID from `conversations.NewID`), `seq` digits; plus the envelope's `{"id":…,"type":"assistant_delta","ts":"…","payload":…,"event_id":…}` ≈ 140 B.
- `60000 + 512 = 60512` — roughly **5 KB (7.6 %) under 65519**.

**Why the 512-byte budget holds against a hostile caller.** `text` is the only non-text field a caller can inflate, and it can't: `turn_id`, `seq`, `id`, `event_id` and `ts` are all minted locally, and `conversation_id` reaches the emitter from `activeConversation.CurrentConversation`, which `sessionRouter.Route` stamps **only after `resolve` succeeds** — so the id is one the conversation registry already holds, and registry ids are 36-byte UUIDv4s from `conversations.NewID` gated by `ValidID` at the primitive boundary. An unregistered or over-long id fails `resolve` and never reaches the cursor. Budgeting 64 B for a 36-B field is headroom, not a guess.

The comment must carry `bundleChunkBytes`'s closing instruction verbatim in spirit: *the invariant is ENFORCED by the cap test; if that ever fails, **LOWER** this constant — never raise it.* The conservative constant is the belt; the deterministic per-frame cap test is the suspenders, and they are different fabric.

Do **not** name the constant after the envelope cap (`maxDeltaEnvelopeBytes` etc.) — it bounds raw input text, not the envelope, and a name that claims otherwise invites someone to "correct" it to 65519.

### 2. `splitDeltaText` — a pure, rune-safe splitter

```go
// splitDeltaText splits s into consecutive chunks of at most max bytes each,
// never cutting through a multi-byte rune. Concatenating the result in order
// reproduces s byte-for-byte. Returns nil for empty s.
func splitDeltaText(s string, max int) []string
```

Behaviour contract:

- Chunks are substrings of `s` (`s[start:end]`), so no copying and no re-encoding — this is what makes byte-for-byte reassembly structural rather than hoped-for.
- Cut points back up to the nearest rune start: from the stride boundary, walk `end` back while `!utf8.RuneStart(s[end])`. For valid UTF-8 this terminates within 3 steps.
- **Degenerate guard:** if backing up would reach `start` (only reachable on invalid UTF-8, i.e. `max` consecutive continuation bytes), cut at the unadjusted stride boundary instead. This cannot loop forever and cannot drop bytes — a moved cut point changes *where* the split lands, never *what* is emitted.
- No chunk is empty when `s` is non-empty.
- Pure and package-level (not a method): it is the one piece here worth table-testing in isolation.

Invalid UTF-8 is not expected in practice — `e.Text` originates from `encoding/json`'s string decoding, which already replaces invalid bytes with U+FFFD — but the guard costs one line and removes a non-terminating loop from the reachable surface.

### 3. `flushDelta` — loop instead of single emit

The rewritten contract, in order:

1. No-op on an empty buffer (unchanged — this is what makes every flush trigger harmless).
2. Capture `text`, `convID`, `msgID` from the coalescing fields into locals *before* emitting, so the loop reads no field the reset will clear.
3. For each chunk of `splitDeltaText(text, maxDeltaTextBytes)`: call `emitMapped` with a synthetic `turnevent.TextChunk{MessageID: msgID, Text: chunk}`, then `e.seq++`.
4. Reset `deltaBuf` / `deltaMsgID` / `deltaConvID` and `flushTimer.Stop()` (unchanged, still after the emits).

`seq++` **inside** the loop, after each `emitMapped`, is the load-bearing detail: `emitMapped` reads `e.seq` off the struct, so hoisting the increment either way gives every chunk the same `seq` or off-by-one ordering. AC #4's "seq advancing once per emitted envelope" is exactly this placement.

The under-cap case is unchanged by construction: `splitDeltaText` returns one chunk, one `emitMapped`, one `seq++` — byte-identical behaviour to today, which is why #609's tests stay green unmodified.

Update the `flushDelta` doc comment: it currently says "emits the buffered assistant text as ONE coalesced assistant_delta" and "seq advances once per emitted coalesced delta, NEVER once per buffered TextChunk". Both sentences become wrong in the first half and right in the second. Rewrite to: one flush emits **one or more** `assistant_delta` envelopes, each under the v2 envelope cap; `seq` advances once per **emitted envelope**, never once per buffered `TextChunk`. Also add a sentence to the struct's "Delta coalescing (#609)" paragraph noting that a flush larger than `maxDeltaTextBytes` splits.

### Data flow after the change

```
TextChunk ──┐
TextChunk ──┼─► deltaBuf (concatenated, unbounded, up to 4 MiB)
TextChunk ──┘
                   │  flush trigger (message boundary / non-text event /
                   │  turn end / ~250 ms coalesceWindow)
                   ▼
            splitDeltaText(text, 10000)  ── rune-safe, byte-preserving
                   │
                   ├─► emitMapped(chunk₀) ─► MapEvent ─► emit ─► ring.Append(eventID₀) ─► Push per conn   seq=n
                   ├─► emitMapped(chunk₁) ─► …                                                            seq=n+1
                   └─► emitMapped(chunkₖ) ─► …                                                            seq=n+k
```

Each chunk is a fully independent logical event: its own ring `eventID`, its own per-conn `env.ID`, its own `seq`. Nothing downstream of `emitMapped` changes.

## Concurrency model

Unchanged. `flushDelta` runs only on the producer's single `Run` goroutine (reached from `Handle`'s `OnEvent` and from the producer's `OnFlush`), the emitter spawns no goroutine, and the coalescing fields stay unguarded-but-race-free. The loop adds no new shared state — `text`, `convID`, `msgID` and the chunk slice are locals. `emit` still takes a fresh `ActiveConns` snapshot per envelope, so a conn that joins between chunks sees the remainder of the message and a conn that drops surfaces as a `Push` error on the next chunk — the same per-envelope semantics that already applied between separate deltas.

**Do not add a `ctx.Err()` check between chunks.** It looks like an improvement and is not: on teardown it would abandon the remaining chunks, turning a shutdown race into silently truncated assistant text — a *correctness* regression traded for skipping a handful of cheap `ActiveConns` calls. `emit` already returns early on `ctx.Err() != nil` inside its `Push`-error branch, which bounds the wasted work to one snapshot per remaining chunk. Leave the loop unconditional.

## Error handling

No new failure modes and no new error path.

- `splitDeltaText` cannot fail: it returns substrings and has no allocation that can be partial. The degenerate invalid-UTF-8 case is handled by the guard above, not by an error.
- `emitMapped`'s `!ok` branch stays unreachable for `TextChunk` (`MapEvent` maps it unconditionally).
- `emit`'s `json.Marshal` failure and its `Push` failure keep their existing per-envelope debug-log-and-continue posture. A `Push` failure on chunk *k* does not abort chunks *k+1…*; the fan-out loop already continues past a failing conn, and aborting mid-message would turn a one-conn transport hiccup into truncated text for every other conn.
- Nothing in this ticket references `protocol.CodeMessageTooLong`; it stays unreferenced (ticket § Technical Notes).

**No new log call — and specifically no chunk count and no chunk length.** The emitter's `SECURITY` paragraph states the rule as *lengths-free discriminants*: `event`, `kind`, `conversation_id`, `turn_id`, `env_id`, `conn_id`, and `Push`'s transport-sentinel `err`. A tempting `"chunks", len(chunks)` or `"bytes", len(chunk)` field carries no assistant text but does carry its **length**, which is metadata about content and is exactly what that rule excludes. Test D asserts the *marker* is absent and would stay green against such a field, so this is a spec-level prohibition, not a test-enforced one. `emit`'s existing fields are the complete allowed set; add none.

## Testing strategy

New file `cmd/pyry/interactive_turn_v2_chunk_test.go` (same package — reuses the existing harness; mirrors the existing `interactive_turn_v2_switch_test.go` split rather than growing the 2 000-line main test file).

A test-local `const maxV2AppEnvelope = 65519`, commented with `docs/protocol-mobile.md` § Application-envelope size cap as its source. **Do not export a production constant** — nothing in `cmd/pyry` enforces the cap, and an exported one would imply an enforcement that lives elsewhere.

### A. `TestSplitDeltaText` — the splitter in isolation

Table rows (input, `max`, expected chunk count / boundaries):

- Empty string → zero chunks.
- Shorter than `max` → one chunk equal to the input.
- Exactly `max` bytes → one chunk.
- `max + 1` bytes → two chunks, sized `max` and 1.
- `2.5 × max` ASCII → three chunks.
- `strings.Repeat("世", n)` sized so stride boundaries land mid-rune (3-byte runes; `max` chosen not divisible by 3) → chunks back up, none is 3-byte-aligned to the stride.
- One ASCII byte followed by 4-byte runes → no stride boundary is rune-aligned.

Assertions applied to **every** row: every chunk is at most `max` bytes; every chunk is `utf8.ValidString`; `strings.Join(chunks, "") == input` (byte equality, not length); no empty chunk for non-empty input.

### B. `TestInteractiveTurnEmitterV2_OversizedDeltaFitsEnvelopeCap` — AC #1, #2, #3, #4

Table over fills, each ~200 000 bytes fed as several same-`MessageID` `TextChunk`s followed by a `TurnEnd` (the turn-boundary flush trigger):

| row | fill | why |
|---|---|---|
| ascii | `"a"` | the common case; ~1.06× expansion |
| html | `"<"` | worst case; 6× expansion — the row that actually binds the constant |
| cjk | `"世"` (3 bytes) | multi-byte, and stride boundaries land mid-rune |
| mixed | `"a"` + repeated 4-byte emoji | forces a mid-rune boundary at a different offset |

Per-row assertions:

- More than one `assistant_delta` was emitted (AC #1 — a single-frame result would make everything below vacuous).
- For **every** recorded push of type `assistant_delta`: `len(json.Marshal(p.env)) < maxV2AppEnvelope`. Marshal `p.env` — the whole `protocol.Envelope`, not `p.env.Payload` — because that is what `forwardEnvelope` seals.
- `t.Logf` the largest observed envelope and its percentage of the cap, so headroom is visible in `-v` output rather than only in the assertion (matches `TestBackgroundTaskPayloads_FitV2EnvelopeCap`).
- Concatenating `deltas[i].Text` in emission order equals the fed text **byte-for-byte** (`==` on the strings; not a length check, not a prefix check). This is the assertion that catches a rune split — `encoding/json` silently substitutes U+FFFD for the invalid halves, so only byte equality sees it.
- The joined text contains no `utf8.RuneError` that the input did not contain (a second, more legible rung on the same property).
- All deltas share one `TurnID`; `deltas[i].Seq == i` for every `i`, starting at 0 (AC #4 — index/seq agreement is itself the strictly-increasing-in-emission-order proof, since `assistantDeltas` returns pushes in call order).

**Anti-vacuity rung — do not skip.** For the `cjk` and `mixed` rows, assert that the *naive* cut would have split a rune: `!utf8.RuneStart(text[maxDeltaTextBytes])`. Without it, a future change to `maxDeltaTextBytes` that happens to be a multiple of 3 or 4 silently turns both rows into ASCII-equivalents and the rune-safety property stops being tested while the test stays green.

### C. `TestInteractiveTurnEmitterV2_UnderCapDeltaEmitsExactlyOne` — AC #4's second half

Three sub-cases, each ending in `TurnEnd`:

- A short buffer → exactly one `assistant_delta`, `Seq == 0`.
- A buffer of exactly `maxDeltaTextBytes` bytes → exactly one.
- A buffer of `maxDeltaTextBytes + 1` bytes → exactly two, seqs 0 and 1.

The boundary pair is what pins the comparison operator in `splitDeltaText`'s stride.

### D. `TestInteractiveTurnEmitterV2_OversizedDeltaNoLogLeak` — AC #5

Modelled directly on `TestInteractiveTurnEmitterV2_NoAppOutputLogLeak`: a `bytes.Buffer` handler at `slog.LevelDebug`, `fakeInteractiveBcast.pushErr` set for the one conn so the `push_err` debug branch fires for **every** chunk (the most log-heavy path), and an oversized `<`-filled buffer carrying a distinctive marker (e.g. `SECRETCHUNKZZZ`) embedded in the text.

Assert `logs != ""` first (otherwise the no-leak assertion proves nothing), then that the marker is absent.

### Stays green — unmodified

AC #5's "every existing flush trigger still flushes" is discharged by these passing without edits. **Do not touch them**; if one goes red, the design is wrong, not the test:

- `TestInteractiveTurnEmitterV2_SameIDCoalesce`, `_NewIDBoundaryFlush` (message boundary)
- `TestInteractiveTurnEmitterV2_TimerFlushMidMessage`, `TestStreamTurnDrainV2_FlushTimerCoalesces` (the ~250 ms window)
- `TestInteractiveTurnEmitterV2_FlushBeforeNonText`, `_UnrecognizedFlushesPendingDeltaFirst`, `_BackgroundTasksFlushPendingDeltaFirst` (interleaved non-text events)
- `TestInteractiveTurnEmitterV2_FlushAtTurnBoundary` (turn end)
- `TestInteractiveTurnEmitterV2_OneSeqPerCoalescedDelta` (one seq per delta, not per buffered chunk)
- `TestInteractiveTurnV2_SwitchFlushesAbandonedDeltaBeforeNewState` (#1062 follow-active flush)
- `TestInteractiveTurnEmitterV2_NoAppOutputLogLeak` (log hygiene, small case)

### Mutant → red-row matrix

Each mutant must redden at least one named row, and each row must be the sole killer of at least one mutant. Run these with `go test -overlay` (no worktree writes) before claiming the suite discriminates:

| mutant | red row |
|---|---|
| `flushDelta` emits the whole buffer as one delta (revert the loop) | B — delta count and the cap assertion |
| hard byte cut, no `utf8.RuneStart` back-up | B `cjk` / `mixed` — byte equality and the `RuneError` rung |
| `seq++` hoisted outside the loop | B — the `deltas[i].Seq == i` assertion |
| `maxDeltaTextBytes` raised to 65519 | B `html` — the cap assertion only |
| chunks emitted in reverse order | B — byte equality |
| `<` changed to `<=` in the stride comparison | C — the `maxDeltaTextBytes` / `+1` boundary pair |
| a `slog` call added carrying the chunk text | D |

## What not to touch

- **`forwardEnvelope`.** No runtime length check on the shared seal-and-forward path. The ticket names it out of scope; the repo's pattern for this invariant is a conservative constant plus a deterministic per-frame cap test, and a second gate there is its own concern with its own blast radius. File separately if wanted.
- **`protocol.CodeMessageTooLong`.** Stays unreferenced outside `compat_test.go`.
- **`turnbridge.MapEvent`.** Text still crosses verbatim. The mapper sees one block; only the flush sees the concatenated size. A second cap here would be a second place the limit is decided, and the two could disagree silently (the reasoning `MapEvent`'s `RateLimited` arm already spells out).
- **`internal/streamsup/parser.go`.** `maxUnrecognizedRaw`'s "escaping is mild" justification is wrong for `<` / `>` / `&` (16384 × 6 ≈ 99.5 KB, over the cap). Same class of defect, different constant — the ticket routes it to a separate ticket. Do not fix it here.
- **`docs/protocol-mobile.md`.** No change owed. § `assistant_delta` already describes `text` as "incremental assistant text, coalesced" ordered by a per-turn `seq`, so N deltas per message is already the contract; this ticket emits more of them, it does not change their meaning.
- **`docs/knowledge/codebase/1495.md`.** Owned by the documentation phase, written after merge. Not a developer deliverable.

## Interactions worth knowing (no action)

- **Event ring.** `eventring.MaxEventsPerConversation` is 1024 per conversation, and a flush now consumes N slots where it consumed 1. An 80 KB reply costs 8; a pathological 4 MiB flush costs ~420. Still bounded, still under the cap, and replay is *more* correct than before (an over-cap event that no phone could accept was retained and re-failed on every reconnect).
- **Droppable deltas (#610) and `pushQueueCap`.** `queuedEnv.droppable` is `env.Type == protocol.TypeAssistantDelta` and `pushQueueCap` is 256 envelopes per session. A typical 80 KB reply is 8 chunks — nowhere near the queue. The pathological case (a 4 MiB flush, the `defaultMaxParseBuf` ceiling) is ~420 chunks against a 256-deep queue, so on a *slow* phone the earliest chunks evict and the phone sees a `seq` gap. Two reasons that is acceptable and not a new lever: the drop policy evicts **deltas only**, so a chunk burst can never displace a control frame (the property `StreamBundle` relies on from the other side); and today that same flush is one frame the phone rejects *entirely*, so partial text is a strict improvement. The `seq` gap is already the phone's detection signal.
- **`deltaBuf` accumulation is still unbounded** — bounded only by `defaultMaxParseBuf` per line and by however many same-`MessageID` chunks concatenate. This ticket caps what goes *on the wire*, not what accumulates in memory. Pre-existing, unchanged, and out of scope: the fix belongs at the flush (ticket § Technical Notes), and a second cap on accumulation would be a second place the limit is decided.
- **`emit`'s fresh snapshot.** A conn that becomes interactive between chunk 3 and chunk 4 receives chunks 4… only, with a `seq` starting mid-sequence. Identical to the pre-existing mid-message join behaviour for separate deltas, and #647 replay is the mechanism that fills the gap.

## Open questions

- **Rejected alternative: split on escaped size instead of raw size.** Tracking each rune's escaped cost (1 / 2 / 6 bytes) and cutting when the running total would exceed a budget is exact, single-path, and would emit one ~65 KB frame where the conservative constant emits seven. It was rejected: it duplicates `encoding/json`'s escaping rules (HTML-escape set, `U+2028`/`U+2029`, invalid-byte substitution) in our code, where they must then stay in sync with Go's encoder forever — a real drift hazard bought for a purely cosmetic reduction in frame count. `bundleChunkBytes` made the same call. If frame count ever becomes a measured problem, that is the ticket to file, and it should carry a differential test against `encoding/json` rather than a re-derivation.
- **Verify the comment's arithmetic against the measurement.** The 60512-byte figure above is derived, not measured. The developer must read the `t.Logf` from row B `html` and, if the measured maximum differs, update the constant's comment to the measured number. Never raise `maxDeltaTextBytes` to close a gap — lower it.
- **10 000 is a round number, not a tuned one.** It is ~92 % of the 10 834 the arithmetic permits, chosen round for the same reason `bundleChunkBytes` is 48 000. If a future ticket wants fewer frames on the ASCII path, the honest lever is the rejected alternative above, not a larger constant.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No findings — but the boundary is the reason, so it is named rather than waved past. The untrusted data is claude's stdout, and the only new code touching it is `splitDeltaText`, which returns **substrings** of its input: it never re-encodes, never interprets, and never allocates on a content-derived size. Escaping stays entirely inside `encoding/json`, so hostile content cannot hit a hand-rolled escaper we got wrong. The one place hostile content drives control flow is the `utf8.RuneStart` back-up loop, which is why the degenerate guard is specified: without it, `max` consecutive continuation bytes would walk `end` to `start`, emit an empty chunk, and hang the daemon's single `Run` goroutine on claude-controlled input. Verified that input cannot arise today — `streamsup`'s `Text` field is decoded by `encoding/json` into a Go `string`, which substitutes U+FFFD for invalid UTF-8 — so the guard is defence in depth, not the primary control. Both facts are in § Design so a developer cannot "simplify" the guard away.
- **[Trust boundaries — envelope overhead]** No findings, verified rather than assumed. The 512-byte non-text budget is only sound if no caller can inflate a non-`text` field. Walked each: `turn_id` (`conversations.NewID`, 36 B), `seq`, `id`, `event_id`, `ts` are all locally minted; `conversation_id` is stamped by `sessionRouter.Route` **only on the successful-`resolve` path**, so it is always a registry-held `ValidID` UUIDv4. An attacker-supplied over-long id fails `resolve` and never reaches the cursor. Recorded in § Design.
- **[Tokens, secrets, credentials]** Not applicable, with the reason: this path handles no token, key, or credential. `conversations.NewID` (the only randomness in reach) is `crypto/rand`-backed, is not modified, and its call site and cadence are unchanged — chunking happens strictly after a turn id exists.
- **[File operations]** Not applicable — the design opens no file, builds no path, and writes nothing to disk. The `eventring` is in-memory.
- **[Subprocess / external command execution]** Not applicable — no `exec`, no environment handling, no signal. The change is downstream of claude's output, never upstream of its invocation.
- **[Cryptographic primitives]** No findings. No new primitive, no key or nonce handling in this code. The one consequence of emitting more frames is more `CipherState.Encrypt` calls, i.e. faster nonce consumption: worst case ~420 extra nonces for a 4 MiB flush against `MaxNonce` of 2⁶⁴−1, which is not reachable in any lifetime of a session. No rekey trigger is message-counted (grepped — none exists), so chunk cadence cannot perturb rekey timing.
- **[Network & I/O]** No findings; this category **is** the ticket, and the specific checks are: the cap is enforced against the marshalled `protocol.Envelope` (what `forwardEnvelope` seals) rather than the payload, by a deterministic per-frame test with a worst-case `<` fill — the belt-and-suspenders shape, where the constant is stochastic-free arithmetic and the test is different fabric. Two adjacent limits were walked and named in § Interactions rather than silently inherited: `pushQueueCap` (256) versus a ~420-chunk pathological flush, where eviction is delta-only and so cannot displace control frames; and `deltaBuf`'s unbounded accumulation, which this ticket deliberately does not address (it caps the wire, not memory) and which is called out as pre-existing. No amplification: chunking adds ~300 B of envelope overhead per frame to a byte stream that already expanded by the same escaping factor as one frame.
- **[Error messages, logs, telemetry]** SHOULD FIX — **addressed in the spec.** The emitter's own rule is "lengths-free discriminants", and the natural instinct when writing a chunk loop is a `"chunks", len(chunks)` debug field. That leaks assistant-text length, and test D (marker absence) would stay green against it — so the test cannot be the control here. § Error handling now prohibits it explicitly and names `emit`'s existing fields as the complete allowed set. No error string in this design carries content: `splitDeltaText` cannot fail, and the pre-existing marshal/`Push` branches already avoid echoing payload bytes.
- **[Concurrency]** No findings — single-goroutine throughout, no new lock, no new shared state, no goroutine spawned, so no lock ordering and no leak to analyse. One shutdown-safety decision is now stated in § Concurrency model rather than left implicit: do **not** add an inter-chunk `ctx.Err()` check, because abandoning the remaining chunks would convert a shutdown race into silently truncated assistant text — a correctness regression bought for a negligible saving. The ring's per-append mutex absorbs N appends where it absorbed 1, all from the same goroutine, so ordering is unchanged.
- **[Threat model alignment]** No findings. Against `docs/protocol-mobile.md` § Security model: all chunk content stays sealed under the Noise channel, so the relay's zero-knowledge property is untouched — it forwards more `noise_msg` frames of ordinary size. The honest residual is traffic analysis: a relay observing 8 frames instead of 1 learns the reply's length more precisely and sees its cadence. That signal already exists at far finer granularity, because normal coalescing emits a delta roughly every 250 ms of streaming; chunking adds nothing qualitatively new, and the alternative (a frame the phone must reject) is not a privacy improvement. Deliberately out of scope and named as such by the ticket: the runtime length check in `forwardEnvelope` and `maxUnrecognizedRaw`'s wrong escaping justification, each owed its own ticket.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-08-18
