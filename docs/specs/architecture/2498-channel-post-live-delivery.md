# #2498 — deliver a posted channel message to an already-connected client

## Files read

- `cmd/pyry/channel.go` → `channelPoster`, `newChannelPostMessageID`, `msgChannelPostRecordFailed` — the verb this slice extends; its doc block owns the "a failed append must be a failed post" rule and the never-echo refusal contract.
- `cmd/pyry/channel.go` → `channelCreator` — the announce-hook shape this slice copies (bare func, may be nil, result never consulted).
- `cmd/pyry/conversation_update_v2.go` → `conversationUpdateEmitterV2`, `announce` — the host-side fan-out this slice's emitter is modelled on, down to the leaf mutex, the #607 gate and the returns-nothing contract.
- `cmd/pyry/interactive_turn_v2.go` → `interactiveBroadcaster`, `emit`, `flushDelta`, `splitDeltaText`, `maxDeltaTextBytes` — the only producer of `assistant_delta` today. `flushDelta` is the chunking this slice reuses verbatim; `maxDeltaTextBytes` is why chunking is mandatory rather than optional.
- `cmd/pyry/relay.go` → `startRelay`, `startRelayV2` — where the emitter is constructed and how its hook leaves the relay leg.
- `cmd/pyry/main.go` → `runSupervisor` — the composition root; `createChannel` / `SetChannelPoster` wiring.
- `cmd/pyry/relay_guard_test.go` → `excludedTypes`, `TestLocalPairingProviderWiredFromRelayConstructionToControl` — `TypeAssistantDelta` is already classified `"push"`, so AC 1's "mints no mobile-protocol type" holds; the wiring guard pins the two `startRelay*` call lines as literal substrings and moves with the signature.
- `internal/protocol/interactive.go` → `AssistantDeltaPayload` — the payload's five fields and `ParentToolUseID`'s grouping-hint-not-a-capability semantics.
- `internal/control/protocol.go` → `MaxChannelPostBytes` (64 KiB) — the input bound that forces the chunking decision.
- `cmd/pyry/conversation_history.go` → `appendConversationHistory`, `historyAppendFailure` — the seam the poster deliberately does not use, and the failure discriminant it does.
- `internal/e2e/channel_post_test.go` → `waitForHistoryEntries`, `messageBody` — the on-disk shape assertions that move with the record.
- `internal/e2e/relay_v2_attachment_offer_test.go`, `internal/e2e/handshake_interactive_helpers_test.go` → `driveHandshakeToOpenDaemonInteractive`, `nextAttachmentEnvelope` — the host-side-push-observed-by-fakephone pattern this slice's e2e copies.
- `docs/knowledge/features/control-plane.md` — the channel-post section this family added; the documentation stage extends it (handoff below).

## Context

#2497 landed `pyry channel post`: it records the content in the channel's durable
log as a `message` entry with role `assistant`. Nothing pushes, and — read at
`d79d90f` in pyrycode-desktop — nothing draws either: `translateTimelineEvent`'s
`messageReceived` arm returns a row only for `role === 'user'` and `null` for
`role === 'assistant'`, on the live path and on the served-history path alike
(`reduceHistoryPage` takes the same translator). So the post is invisible on both
halves of #2496's "live when connected, on the next connect otherwise".

What the current clients DO draw as assistant text, live and from a page, is
`assistant_delta`, folded through `appendDelta` and coalesced by `turn_id`. No
client change is in scope, so the design is forced: the post must reach the
client as `assistant_delta`, and — since AC 2 asks for the durable record to be
in the shape it was pushed — the durable record becomes `assistant_delta` too,
replacing the `message` entry #2497 wrote.

No ADR is warranted. This slice adds no boundary; it repoints one producer at an
existing wire type and copies an existing fan-out shape.

## Design

### The record and the frame are one shape

`channelPoster` stops writing `protocol.MessagePayload` / `protocol.TypeMessage`
and writes `protocol.AssistantDeltaPayload` / `protocol.TypeAssistantDelta`
instead. That is what makes AC 2 and AC 3 one decision rather than two: there is
exactly one shape of record per post, it is the shape that was pushed, and the
`message` entry that drew nothing is gone rather than kept beside it.

```go
protocol.AssistantDeltaPayload{
    ConversationID: string(convID), // daemon-derived, as today
    TurnID:         turnID,         // fresh per post; see below
    Seq:            i,              // 0-based, per chunk
    ParentToolUseID: "",            // main lane — a post is not a subagent's text
    Text:           chunk,
}
```

`MessageID` has no analogue on this payload, so `newChannelPostMessageID` becomes
`newChannelPostTurnID` — same `conversations.NewID` mint, different failure
contract (below).

### A fresh turn id per post, and no turn lifecycle at all

The post emits `assistant_delta` and NOTHING ELSE — no `turn_state`, no
`turn_end`. Three reasons, all from the ticket's own reading of the tree:

- An `assistant_delta` alone renders; a fresh `turn_id` starts a fresh bubble.
- A synthetic `turn_end` would assert `TurnEndPayload`'s four claude-authored
  strings and four numbers that no claude reported.
- A channel is an ordinary bound conversation and the operator can be mid-turn in
  it. A `turn_end` would close a turn actually in flight; a `turn_state` would
  claim a lifecycle edge this verb does not cause. The post must not disturb a
  turn in flight, and the way to guarantee that is to emit no lifecycle frame.

The fresh `turn_id` cannot collide with a running turn's: both come from
`conversations.NewID` (UUIDv4, `crypto/rand`).

### Chunking is mandatory, not a refinement

`MaxChannelPostBytes` is 64 KiB. `maxDeltaTextBytes` is 10000, and its doc block
records the arithmetic: `encoding/json` escaping can cost six bytes per input
byte, so the v2 application-envelope cap (65519 B) is the reason that constant is
what it is. A 64 KiB post as one `assistant_delta` would exceed the cap by an
order of magnitude.

So the poster splits with the package's existing `splitDeltaText(text,
maxDeltaTextBytes)` — the same call `flushDelta` makes — and emits one payload per
chunk, sharing one `turnID`, with `Seq` advancing from 0. Every chunk is a
substring of the input, so concatenation reproduces the post byte-for-byte, and
the client coalesces the chunks into one bubble on the shared `turn_id`.

This is how AC 3 is met for a post of any size: one post is ONE `turn_id`, which
is one rendering. The common cron-sized post is one chunk and therefore literally
one record; a larger one is N records that no client draws separately. What AC 3
forbids — a second record of the same post in a different shape — is what
dropping the `message` entry removes.

An empty post is already refused daemon-side by `handleChannelPost`, so
`splitDeltaText` is never reached with `""` (it would return nil and emit
nothing).

### The fan-out: `channelPostEmitterV2` (new file `cmd/pyry/channel_post_v2.go`)

A near-copy of `conversationUpdateEmitterV2`, deliberately rather than by
resemblance — that is the frame with the same origin (a host-side control verb,
broadcast from the control-server handler goroutine), so its delivery semantics
are the ones a client already reasons about.

```go
type channelPostEmitterV2 struct { bcast interactiveBroadcaster; ctx context.Context; logger *slog.Logger; mu sync.Mutex; nextID uint64 }
func newChannelPostEmitterV2(bcast interactiveBroadcaster, ctx context.Context, logger *slog.Logger) *channelPostEmitterV2
func (e *channelPostEmitterV2) announce(p protocol.AssistantDeltaPayload)
```

`announce` marshals once, snapshots `ActiveConns(ctx)`, skips non-`Interactive`
conns (the #607 gate), stamps a per-emitter monotonic envelope id under the leaf
mutex, and pushes. It **returns nothing** — AC 4's contract: a failed fan-out must
not turn a recorded post into a refusal, because the record is on disk whether or
not anyone heard.

`EventID` is deliberately absent, matching `conversation_updated` and
`attachment_offered`. The emitter owns no `eventring`; the ring belongs to
`interactiveTurnEmitterV2` and minting an id into it from here would need that
emitter threaded across the composition root for a replay path AC 2 already covers
with the durable log. A reconnecting client reads the post from history, not from
ring replay.

Like both of those frames this is LIVE-ONLY: no outstanding-post registry, no
connect-time replay. The difference from `conversation_updated` is that this one
HAS a durable half, which is what makes "live-only" acceptable for a frame that
also sits in the droppable class of `pushQueue.enqueue` and `convRing.evictOldest`.

### Wiring

`startRelayV2` builds the emitter beside the two existing ones — after `mgr`,
before `mgr.Run`'s goroutine starts — and returns its `announce` as a seventh
value, which `startRelay` carries outward and `runSupervisor` hands to
`channelPoster`. `relayWiring`'s no-URL early return leaves it nil, exactly as it
leaves the other two, and a nil hook records the post and tells nobody.

The hook takes ONE payload per call and the poster loops, which keeps the chunking
in the one place that also writes the chunks to the log, and keeps this emitter the
same fifteen lines its neighbour is. Per-chunk `ActiveConns` snapshots are
`flushDelta`'s own behaviour, not a new exposure.

## Concurrency model

No goroutine is spawned and none is owned. `announce` runs synchronously on the
control-server handler goroutine servicing `channel.post` — `fileAttacher`'s and
`channelCreator`'s shape — and is bounded there: `ActiveConns` is a snapshot under
the manager's own lock and `Push` enqueues without blocking, so a wedged phone
cannot hold the verb open.

`mu` is a leaf lock guarding `nextID` and nothing else, held around the counter
bump alone and never across `ActiveConns` or a `Push`, so it can never nest inside
the manager's `pushMu` and there is no lock order to reason about. It is
load-bearing rather than copied: `control.Server.Serve` accepts each conn onto its
own goroutine, so two crons can post concurrently.

`ctx` is the daemon context captured at construction. Once cancelled `ActiveConns`
answers empty and a racing `Push` returns its error, so a late post fans out to
nobody rather than blocking teardown.

The emitter is built after `mgr` and before `mgr.Run`'s goroutine starts, the
window the two existing emitters use, so nothing that goroutine reads is written
concurrently.

## Error handling

| Failure | Answer |
|---|---|
| Turn-id mint (`crypto/rand`) fails | Refuse with `msgChannelPostRecordFailed`; nothing appended, nothing pushed. See below. |
| Payload marshal fails | Existing branch, retargeted at the new payload: Error-log with no payload and no `err.Error()`, refuse with `msgChannelPostRecordFailed`. |
| History append fails (any chunk) | Existing branch: Warn with `historyAppendFailure(err)`, refuse with `msgChannelPostRecordFailed`. Nothing is pushed. |
| `Push` fails for one conn | Debug-logged and skipped; the loop continues and the post still succeeds (AC 4). |
| No client attached / nil hook | The post succeeds and is recorded (AC 4). |

**Why a turn-id mint failure refuses the post**, where `newChannelPostMessageID`
fell back to `""`: a message id is an identity a client dedupes on, and the old doc
block was right that refusing delivery over an rng hiccup trades a deliverable for
a cosmetic. A turn id is not that — it is the ADDRESS that decides which bubble the
content lands in, and every post minting `""` would coalesce into a single bubble
with every other one, which is precisely the "one post is one rendering" contract
of AC 3. `startTurnIfNeeded` is the in-tree precedent: it declines to emit without
a turn id. It can retry on the next event; a post has none, so refusing is the only
form that precedent can take here. A cron reads a non-zero exit.

**A multi-chunk post whose Nth append fails leaves chunks 0..N-1 on disk and
returns a failure.** `history.Store` is append-only, so this is not transactional
and cannot be made so in this slice. It is `flushDelta`'s existing exposure (each
chunk appends independently) and it is bounded: appends run before any push, so a
failed post pushes nothing at all, and the partial is a prefix of the message
rather than a mixture. Named here rather than defended against.

## Testing strategy

**`cmd/pyry/channel_post_test.go`** (extend; `newTestPoster` grows an announce hook,
and a `stubAnnouncer` records payloads):

- Happy path, retargeted: exactly one append, `typ == protocol.TypeAssistantDelta`, payload decodes to an `AssistantDeltaPayload` whose `ConversationID` is the matched row, `Seq` is 0, `ParentToolUseID` is `""`, `Text` is the post, and `TurnID` satisfies `conversations.ValidID`.
- The announced payload and the appended payload are byte-identical — AC 2's "in the shape it was pushed" as one assertion rather than two derivations that agree today.
- Two posts mint two different `TurnID`s (AC 3: one post is one rendering).
- A post larger than `maxDeltaTextBytes`: N appends and N announces, one shared `TurnID`, `Seq` 0..N-1, every chunk under the bound, concatenation equals the input.
- A nil announce hook still records and still returns nil (AC 4).
- **The per-frame envelope cap, deterministically.** A `control.MaxChannelPostBytes`-sized post filled with `<` (the byte `encoding/json` expands six-to-one) is posted, and every announced payload is marshalled into a `protocol.Envelope` and measured against `maxV2AppEnvelope` — the constant `cmd/pyry/interactive_turn_v2_chunk_test.go` already declares in this package. Rejoining the chunks must reproduce the input byte-for-byte. This is `TestInteractiveTurnEmitterV2_OversizedDeltaFitsEnvelopeCap`'s shape and its rule: `maxDeltaTextBytes` is the belt, this measurement is the suspenders, and if it ever fails the constant goes DOWN. Required by the security pass, § Network & I/O.
- An announce hook that panics is NOT tested; the hook returns nothing and the emitter swallows its own errors, so "fan-out failure never refuses" is proven at the emitter instead.
- Retained unchanged: the ambiguous-name refusal, the archived/unpromoted filter, the create-on-miss arm, the forwarded create refusal, the append-failure-is-a-post-failure test (its assertion that nothing leaks `internal/history` text still holds).

**`cmd/pyry/channel_post_v2_test.go`** (new; `fakeInteractiveBcast` already exists in
this package):

- One payload fans one envelope per INTERACTIVE conn and none to a non-interactive one (#607 gate).
- `Type == protocol.TypeAssistantDelta`, `InReplyTo` nil (nothing solicited it), `EventID` nil.
- Envelope ids are strictly increasing across successive announces.
- A `Push` error for one conn does not stop the loop or panic — the other conn still receives (AC 4).
- A cancelled ctx returns without pushing.

**`internal/e2e/channel_post_live_test.go`** (new, fake-daemon tier, so `make check`
covers it): a fakephone handshakes to interactive, THEN `pyry channel post` runs
against the same daemon's control socket; the phone receives an `assistant_delta`
carrying the posted text for the created channel, and the on-disk log holds exactly
one entry of type `assistant_delta` whose payload is that same text. The ordering is
load-bearing: the frame is live-only, so a phone that handshakes afterwards is never
told.

**`internal/e2e/channel_post_test.go`** (edit): `messageBody` and the `"message"` /
role assertions move to the `assistant_delta` shape. This is #2497's "whatever it
asserts about the stored entry shape moves with the record".

**`cmd/pyry/relay_guard_test.go`** (edit): the two pinned `startRelay*` call-line
fragments gain the seventh value. `excludedTypes` needs no change —
`TypeAssistantDelta` is already classified `"push"`.

## Open questions

1. **Does a `turn_id` that names no supervised turn confuse a client that later
   receives a real `turn_state` for the conversation?** Resolved in the design by
   emitting no lifecycle frame at all: the post's turn id appears on
   `assistant_delta` only, so nothing invites a client to track a lifecycle for it.
   Verify during implementation that no daemon-side consumer keys off `turn_id`
   expecting a matching `turn_state` — grep the fan-out path before wiring.
2. **Should the frame carry `EventID` so a ring-replay reconnect includes it?**
   Answered no in the design. Record the resolution in `## Revisions` if
   implementation shows the absent id breaks a client's `last_event_id` bookkeeping.

## Documentation handoff

Pending for the documentation stage; not implemented by this slice:

- `docs/knowledge/features/control-plane.md` — extend this family's channel-post section with how a posted message reaches an already-connected client, beside the existing "Fanning `channel.new` out: `conversation_updated` as an unsolicited push (#2156)" section. The durable record's shape CHANGES in this slice (`message`/role-assistant → `assistant_delta`), so that section's description of it must be corrected in the same pass.
- `docs/protocol-mobile.md` — in the v2 application-message-type table, the `assistant_delta` row gains a second kind of producer: a host-side control verb, not only a supervised claude turn. Record it the way the `conversation_updated` row already records its two producers.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] **Finding, accepted — this slice moves `text` across a boundary it did not cross before.** `channelPoster`'s `text` is caller-authored content arriving over the control socket; #2497 wrote it to disk and stopped, and this slice puts it on the wire to every paired interactive device. Two controls bound it and neither is new: `internal/control/server.go`'s `0600` chmod on the socket path is, by its own comment, "the only authentication boundary" on who may post, and the `#607` interactive gate in `announce` is who may receive. The content itself is bounded at `handleChannelPost` by `MaxChannelPostBytes` before the poster ever sees it. Downstream, `AssistantDeltaPayload.Text` is a DISPLAY STRING and not a capability — `ToolUsePayload`'s doc block already states that posture for this wire — so a client renders it inert.
- [Trust boundaries] OUT OF SCOPE — **a local process that can write the control socket can make text appear as though claude said it.** `role: "assistant"` is #2497's existing decision and AC 1 restates it ("carries the posted content as assistant text"); this slice changes WHEN that text is seen, not WHO can produce it. The `0600` socket is the control, and distinguishing host-authored from claude-authored assistant text on the wire would be a protocol change no client in scope could read. Belongs to whoever revisits `role` semantics, not here.
- [Network & I/O] **MUST FIX — found during design, addressed before this pass, and now enforced by a test.** `MaxChannelPostBytes` is 64 KiB and the v2 application-envelope cap is 65519 B. `encoding/json` escaping costs up to six bytes per input byte, so a single-frame post would have exceeded the cap by roughly an order of magnitude — `maxDeltaTextBytes`' own doc block records exactly this arithmetic. The design splits with `splitDeltaText(text, maxDeltaTextBytes)`, and the pass adds the deterministic worst-case measurement to § Testing strategy (`<`-filled, `MaxChannelPostBytes`-sized, every marshalled envelope under `maxV2AppEnvelope`). The constant alone was the belt; the measurement is different fabric.
- [Network & I/O] No further findings — **resource exhaustion is bounded by the same socket.** One maximum-size post is at most seven frames per interactive conn, and `assistant_delta` is the single droppable class in `pushQueue.enqueue` and `convRing.evictOldest`, so a burst of posts can evict other deltas rather than wedging a queue. No new per-conn cap is added: the poster is reachable only through the `0600` socket, and a second differently-shaped filter here is the hazard `interactive_turn_v2.go`'s status-peer arms each name.
- [Error messages, logs, telemetry] **SHOULD FIX — enforce in Phase B; the verifier must check it landed.** The existing `channel_post.posted` line logs `conversation_id` and `created` and no content, and `channelPoster`'s doc block owns that rule. Chunking introduces two new temptations that are both content-derived and must NOT be logged: the chunk COUNT and the per-frame `seq` are proxies for message length. The push-error line copies `conversationUpdateEmitterV2`'s fields exactly — `conversation_id`, `conn_id`, `env_id`, the transport sentinel — and adds nothing else. The refusal text stays `msgChannelPostRecordFailed`, a constant, because `SetChannelPoster` forwards it to the wire verbatim.
- [Tokens, secrets, credentials] No findings — the only value minted is the turn id, from `conversations.NewID` (UUIDv4, `crypto/rand`). It is an ADDRESS, not an authorisation value: `AssistantDeltaPayload`'s doc block already gives `ParentToolUseID` grouping-hint-not-a-capability semantics and `TurnID` sits in the same class. Nothing is stored, rotated or revoked. A mint failure refuses the post rather than emitting `""` — see § Error handling for why that differs from `newChannelPostMessageID`'s old fallback.
- [File operations] No findings — this slice adds no path handling. The durable log keys on the daemon-minted conversation id (a registry match or `channelCreator`'s freshly minted one, never a caller assertion), and `create` still owns `resolveSpawnDir`'s confine-then-trust order whole. No `Stat`-then-`Open`, no new file mode, no symlink decision.
- [Subprocess / external command execution] Not applicable — nothing here executes anything. `text` reaches a JSON payload, a log segment and a sealed envelope, and never an `argv`.
- [Cryptographic primitives] No findings — no new primitive. The push is sealed by `V2SessionManager`'s existing Noise transport; randomness is `crypto/rand` through `conversations.NewID`; nothing is compared against a secret, so no constant-time question arises.
- [Concurrency] No findings — `mu` is a leaf lock over `nextID` alone, never held across `ActiveConns` or `Push`, so it cannot nest inside the manager's `pushMu`. No goroutine is spawned, so none can leak. Two concurrent posts into one channel cannot merge: each mints its own turn id, and each post's appends are sequential on its own handler goroutine, so a client coalescing by `turn_id` reassembles each in order however the two interleave on disk. Envelope ids are per-emitter and therefore not globally monotonic per conn — pre-existing and deliberate, since `V2SessionManager.Push` never rewrites `Envelope.ID` and every v2 emitter in this package numbers its own frames.
- [Threat model alignment] No findings — the relevant `docs/protocol-mobile.md` § Security model concern for a new push is audience control, and this frame passes the same `#607` interactive gate as every other v2 frame, with no second gate written into the caller. The frame carries no `in_reply_to`, so it cannot be mistaken for an answer to a request a client did not make.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-16
