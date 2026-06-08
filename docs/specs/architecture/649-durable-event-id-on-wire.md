# Spec #649 — Surface the durable per-conversation event id on the live structured stream

**Size: XS** (PO sized S; the additive-field path is the minimal one — the ticket's Technical Notes pre-authorize the S→XS downgrade. 2 production files, 0 new exported types, 1 changed construction site, purely additive.)

**Not `security-sensitive`** — outbound, additive wire surface carrying a non-secret monotonic id. No inbound trust boundary, no dispatch policy, does not touch the Noise send-nonce invariant (per the ticket body; the inbound `last_event_id` consumer is sibling #647, which carries the label).

## Files to read first

- `cmd/pyry/interactive_turn_v2.go:306-356` — `emit()`: the one place envelopes reach the wire. Line 319-328 already calls `e.ring.Append(...)` and **discards** the returned id; line 325 comment misattributes the wire surface to `#647`. Lines 336-342 build the per-conn `protocol.Envelope`. This is the only production edit site.
- `internal/protocol/envelope.go:19-30` — `Envelope` struct. The new field slots in next to `InReplyTo *uint64 \`json:"in_reply_to,omitempty"\`` (line 28) — the exact precedent to mirror (optional pointer, omitempty).
- `internal/eventring/ring.go:106-127` — `Append` returns `uint64` (the durable per-conversation id, `>= 1`, strictly increasing per conv, never reset). No change here; this is the id source.
- `internal/eventring/ring.go:50-57` — `Event.ID` doc: "durable per-conversation event id (>= 1, strictly increasing within a conversation)". Confirms the id is always ≥1, so a present `event_id` is never the zero value.
- `internal/protocol/envelope_test.go:29-94, 127-184` — `TestEnvelope_RoundTrip_Full/_Minimal` (already prove byte-identical round-trip; will pass unchanged with the nil-pointer field) and `TestRoutingEnvelope_TokenOmitempty` / `_CloseCodeOmitempty` (the omitempty-unit-test template to copy for `event_id`).
- `cmd/pyry/interactive_turn_v2_test.go:19-130` — `recordedPush` captures the pushed `protocol.Envelope`; `ringEventIDs` / `ringEventTypes` / `e.ring.After(convID, 0)` read the ring. These are exactly the hooks the new wire-id assertions need.
- `cmd/pyry/interactive_turn_v2_test.go:814-916` — the existing `// --- #646 durable event ring ---` test block. The new #649 wire-id tests extend this region and reuse the same scripted-event setup.
- `internal/protocol/testdata/envelope_full.json` / `envelope_minimal.json` — the fixtures AC-4 requires to round-trip byte-identically; no change needed (the nil pointer is omitted).
- `docs/knowledge/decisions/025-mobile-remote-head-interactive-session.md:128` — § Backpressure / replay: "the phone sends `hello` with `last_event_id`; the binary replays from a bounded per-conversation event ring." This slice is the producer that gives the phone a `last_event_id` to advertise.

## Context

EPIC #596 Phase 2 structured streaming. #646 (merged) built the per-conversation event ring: every structured event the interactive emitter fans out is recorded under a durable, connection-independent id (`eventring.Ring.Append` returns it). That id is **not yet on the wire** — `emit()` computes it and discards it.

Without the durable id on the live stream, a reconnecting phone has no position to advertise: the per-conn envelope `id` (`e.nextID`) increments per-conn-per-envelope and resets each reconnect, so it cannot serve as a replay cursor. This slice is the **producer half** of mid-turn reconnect — it surfaces the durable id outbound. The reconnect **consumer** (per-conn `last_event_id` in `hello`, ring replay, resync marker) is sibling **#647**, blocked on this slice.

## Design

### Wire mechanism: additive `omitempty` pointer field on `protocol.Envelope`

The ticket leaves the mechanism to the architect (additive field vs separate replay frame). **Choose the additive field.** It is minimal, mirrors the existing `InReplyTo *uint64` precedent exactly, requires no new envelope type or dispatch path, and keeps every non-interactive construction site byte-identical for free.

Add one field to `Envelope` (`internal/protocol/envelope.go`), placed after `InReplyTo`:

```go
// EventID is the durable, per-conversation event id (eventring) the
// interactive structured stream stamps so a phone can advertise it as
// last_event_id on reconnect. Distinct from ID (the per-conn envelope
// counter that resets each reconnect). A pointer + omitempty so every
// non-interactive / v1 construction site stays byte-identical: absent,
// not 0. Set only by the interactive emitter (#649); consumed by #647.
EventID *uint64 `json:"event_id,omitempty"`
```

**Why a pointer, not `uint64`:** AC-4 demands the field be *absent* (not `null`/`0`) when no durable id applies. A plain `uint64` with `omitempty` would omit the zero value too — but it could never represent "present and 0" vs "absent," and more importantly the intent is explicit nil-vs-set. Ring ids are always ≥1 (`nextID` starts at 1), so a non-nil pointer never encodes `0`; a nil pointer is omitted. This matches `InReplyTo` exactly.

**Field name `event_id`:** distinct from the envelope's existing `id` (per-conn counter). `protocol-mobile.md` is the wire-spec source of truth (see Open Questions for who updates it).

### `emit()` change (`cmd/pyry/interactive_turn_v2.go`)

Three edits inside `emit()`, all within the existing function — no new helper, no signature change:

1. **Capture** the returned id (currently discarded at line 328):
   `eventID := e.ring.Append(convID, typ, payloadJSON, ts)`
2. **Stamp** it on each per-conn envelope (the loop literal at lines 337-342):
   add `EventID: &eventID` to the `protocol.Envelope{...}`.
3. **Fix the comment** at line 325: rewrite `#647 surfaces it on the wire` → `#649 surfaces it on the wire` (the producer/consumer split moved this responsibility to #649). Adjust the surrounding sentence so it reads as "the returned id is stamped on each conn's envelope below" rather than "unused here."

**Pointer aliasing is safe.** `eventID` is a loop-invariant local, captured once per `emit()` call and never reassigned; every per-conn envelope in the fan-out loop shares `&eventID`. The pointee is immutable after capture and `Push` only ever *reads* the envelope (marshal/seal). So all conns observe the identical value (AC-2) with no race — consistent with the single-Run-goroutine contract and the ring's self-synchronisation (the emitter still takes no lock). Do **not** allocate a fresh `*uint64` per conn; it would be wasteful and is unnecessary.

### What does NOT change

- **`eventring`** — no change; `Append` already returns the id.
- **All other `protocol.Envelope{...}` construction sites** (13 production + ~18 test files: `internal/dispatch`, `internal/relay/auth.go`, `internal/relay/v2session.go`, `cmd/pyry/assistant_turn*.go`, etc.) — leave `EventID` unset → nil → omitted → byte-identical wire. This is the whole point of the additive-pointer choice; AC-4's "non-interactive frame" and "v1 frame" fall out for free with zero edits.
- **`assistant_turn_v2.go`** (the #589 coarse/screen-snapshot path) — no ring, no durable id; never sets `EventID`. Confirmed not in scope.

### Data flow (unchanged except the stamp)

```
turnevent.Event → Handle → emit():
    ts := now
    eventID := ring.Append(convID, typ, payloadJSON, ts)   // was discarded; now captured
    for each interactive conn:
        nextID++                                            // per-conn envelope counter (unchanged)
        Push(Envelope{ID: nextID, ..., EventID: &eventID})  // durable id stamped, identical across conns
```

## Concurrency model

No change. The emitter is a passive state machine on the producer's single Run goroutine; the ring carries its own mutex for the future cross-goroutine query path (#647). `eventID` is a goroutine-local capture of an immutable return value, shared by reference across the synchronous per-conn `Push` loop. No new goroutine, no new lock, no new shared mutable state.

## Error handling

No new failure modes. `Append` cannot fail (returns a plain `uint64`). The existing marshal-error early-return (lines 307-317) runs *before* the `Append` call, so a marshal failure still skips both the ring record and the wire stamp exactly as today. The per-conn `Push` error path (lines 343-354) is unchanged — `env_id` (the per-conn `nextID`) remains the log discriminant; the durable id is not logged (no AC asks for it, and the SECURITY log contract enumerates the allowed fields).

## Testing strategy

Stdlib `testing` only, table-driven where natural, `t.Parallel()`. Add to the two existing test files; reuse the established doubles (`fakeInteractiveBcast`, `recordedPush`, `stubCursor`, `ringEventIDs`, `e.ring.After`).

**`internal/protocol/envelope_test.go`** — one omitempty unit test, copy the `TestRoutingEnvelope_TokenOmitempty` shape:
- Marshal an `Envelope` with `EventID == nil` → bytes contain no `"event_id"` (AC-4: absent, not null/zero).
- Marshal with `EventID` set to a non-zero value → bytes contain `"event_id":<n>`; unmarshal back → pointer to the same value (round-trip).
- The existing `TestEnvelope_RoundTrip_Full/_Minimal` already assert byte-identical round-trip of the fixtures; they pass unchanged and *are* the AC-4 proof for the "absent on existing fixtures" half. No fixture edits.

**`cmd/pyry/interactive_turn_v2_test.go`** — extend the `// --- #646 durable event ring ---` block (around line 814) with #649 wire-id assertions. Reuse the existing scripted-event setup (thought → text → tool_start → turn_end):
- **AC-1 (id on the wire):** every recorded push's `env.EventID` is non-nil. Cross-check: for each logical event, the wire `*env.EventID` equals the corresponding ring id from `e.ring.After(convID, 0)` (same order, same value).
- **AC-2 (identical across conns):** with two interactive conns, for each logical event the `EventID` on conn "a"'s push equals the `EventID` on conn "b"'s push (one ring id fanned to both), while the per-conn `env.ID` differs between them. Use `pushesFor(pushes, "a")` / `pushesFor(pushes, "b")` and compare index-aligned.
- **AC-3 (strictly increasing in emit order):** the sequence of `*env.EventID` across one conn's pushes is strictly increasing (1,2,3,…), making the latest a valid `last_event_id`.
- **AC-4 (non-interactive / absent):** a non-interactive-only snapshot produces zero interactive pushes (already covered by `RingAppendsWithNoInteractiveConns`); add an assertion that any push to a non-interactive surface — n/a here since the gate filters them — is not needed. Instead, the "absent" half is fully covered by the protocol-package omitempty test above plus the unchanged fixture round-trips. Keep the emitter tests focused on AC-1/2/3 (the producer behaviour).

Write scenarios as bullet-pointed inputs+expectations; the developer writes the assertions in the project idiom. Run `go test -race ./internal/protocol/ ./cmd/pyry/` and `go vet` / `staticcheck`.

## Open questions

- **`docs/protocol-mobile.md` § Message envelope** should gain an `event_id` row (and `event_id` in the v2 application-envelope example at line ~286). Per the architect constraint, the developer's worktree mutates only code, tests, and this spec — **not** docs outside `docs/specs/architecture/`. The wire-spec doc update is therefore left to the documentation phase, which writes it post-merge from this spec + the diff. Flagged here so it is not lost: the field is `event_id` (int, optional, "durable per-conversation event id for replay-cursor; present only on interactive structured-stream frames; absent elsewhere").
- **Optional fixture strengthening (not required for XS):** a `testdata/envelope_event_id.json` fixture + a round-trip test would pin the present-field wire shape as a golden file. The omitempty unit test already covers the contract; add the fixture only if the developer finds it cheaper to assert than a programmatic marshal. Default: skip it.

## Acceptance criteria (developer deliverables)

1. `protocol.Envelope` gains `EventID *uint64 \`json:"event_id,omitempty"\`` with a doc comment; `emit()` captures `ring.Append`'s return and stamps `EventID: &eventID` on every per-conn envelope; the line-325 comment is corrected `#647` → `#649`.
2. The durable event id on the wire is identical across all interactive conns for a given logical event and stable across reconnects (it is the ring id, not the per-conn `ID` counter) — proven by an emitter test.
3. The durable event ids on one conversation's live stream are strictly increasing in emit order — proven by an emitter test.
4. The new field is additive: `EventID == nil` is omitted from the wire (a v1 frame, a non-interactive frame, and every existing `testdata/` fixture round-trip byte-identically) — proven by the protocol omitempty unit test and the unchanged fixture round-trips.
5. `go test -race ./...`, `go vet ./...`, `staticcheck ./...` pass.
