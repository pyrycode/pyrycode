# #2116 — answer a conversation-history request with a page of past entries

Ticket: pyrycode/pyrycode#2116 (child of #2091). Size `s`, `security-sensitive`.

Wires `protocol.TypeRequestHistory` (#2113, vocabulary only) to a handler that
serves `history.Store.Page` (#2112, uncalled outside its own package) over the
entries #2114 and #2115 append, and answers with `protocol.TypeHistoryPage`.

## Size overage, stated

The refiner's `Estimate:` is ~1600 lines across 5 production files, against the
table's 800-line line. Re-checked against this plan and the estimate holds:
5 production files (at the ceiling, not over), 0 new exported types on the
package's public surface beyond the two seam types below, ~2 consumer call sites,
5 acceptance criteria, 5 reject branches. **Only the line count is over, at ~2×.**

Every candidate cut is barred, and the bar is the sizing *floor* rather than a
preference:

- *Validation + rejects, without the paging reply.* A handler that refuses but
  answers nothing is not observable from outside the daemon, and its only
  consumer is its sibling. The floor says merge it back, and the floor wins over
  the ceiling.
- *The four reject codes alone.* § Conversation history's Rejects states the rule
  in as many words: "No new code is invented ahead of the code that would send
  it."
- *The page/live boundary e2e alone.* It is the liveness proof for the handler it
  would be split from.
- *Byte budgeting as a follow-up.* The first slice would then ship a handler that
  can emit a frame the transport refuses with `message.too_long` — a broken
  deliverable, not a slice.

Split depth is 1 (parent #2091, no grandparent), so splitting was available and
was declined on the floor rule, not because the depth gate barred it. The nearest
analogue, #2054, landed this exact shape in this area at ~1773 lines inside the
builder budget.

## Files read

- `internal/relay/v2session_attachment_request.go` → `handleRequestAttachment`,
  `attachmentStreamAborted`, `rejectAttachmentRequest` — the structural template:
  ordered gates, static messages, the worker/Run split, the never-log rules.
- `internal/relay/v2session_attachment.go` → `attachmentReject`,
  `attachmentReplyError`, `attachmentReply` — the reply plumbing shape this
  handler copies into its own pair (the package's posture is that each
  reply-owing handler owns its error helper).
- `internal/relay/v2session.go` → `dispatchAppFrame`, `enqueueAppFrame`,
  `appFrameJob`, `appFrameKind`, `appFrameWorker`, `forwardToRun` — where the
  case goes, why it hands off, and how a reply reaches Run for sealing.
- `internal/relay/v2session_seams.go` → `V2SessionConfig.KnownConversation`,
  `.AttachmentResolve` — the membership gate this verb uses, and the primitive
  seam shape the new one departs from and why.
- `internal/relay/v2session_replay.go` → `handleRequestSnapshot` — the
  in-package precedent for answering an unknown/foreign conversation id
  *distinguishably*, which is the choice this ticket makes and #2054 declined.
- `internal/history/log.go` → `Store.Page`, `Page`, `Entry`, `MaxPageEntries`,
  `ErrInvalidID`, `ErrInvalidPageSize`, `ErrInvalidCursor`, `ErrUnknownVersion`,
  `ErrCorruptSegment` — the served API, its clamp/refuse split, and the sentinel
  set the seam classifies.
- `internal/history/cursor.go` → `parseCursor`, `cursorRefusal` — confirms every
  cursor refusal wraps `ErrInvalidCursor`, including the "names a segment that is
  not in this log" one `Page` raises itself, so **one `errors.Is` covers all three
  merged causes**.
- `internal/protocol/history.go` → `RequestHistoryPayload`, `HistoryEntry`,
  `HistoryPagePayload` (+ its `MarshalJSON`) — the frozen wire shapes and the
  obligations they assign here.
- `internal/protocol/codes.go` → `CodeConversationNotFound`, `TypeRequestHistory`,
  `TypeHistoryPage` — the reused code and the two types.
- `cmd/pyry/conversation_history.go` → `appendConversationHistory`,
  `historyAppendFailure` — the in-repo pattern for classifying this package's
  sentinels by `errors.Is` on the `cmd/pyry` side of a seam.
- `cmd/pyry/interactive_turn_v2.go` → the chokepoint emit's hoisted
  `ts := time.Now().UTC()` — **load-bearing for the dedup key**: the identical
  value reaches `appendConversationHistory` and every per-conn envelope.
- `cmd/pyry/session_transition_v2.go` → `startSessionTransitionStreamV2`'s
  broadcast — same hoist, and the producer that skips the replay ring, so its
  live envelope carries **no** `event_id` and its entries live only in the log.
- `cmd/pyry/operator_message_history.go` → `newOperatorMessageHistory` — the
  #2115 producer that appends **without** a live twin, so it can never collide
  with the dedup key.
- `cmd/pyry/relay.go` → `w.hist`, the `KnownConversation` closure, the
  `attachmentResolve` adapter — where the new seam is wired and the adapter shape
  to mirror.
- `cmd/pyry/relay_guard_test.go` → `inboundTypes`, `excludedTypes`,
  `TestEveryInboundV2TypeHasHandler` — the registry move this ticket must make.
- `docs/protocol-mobile.md` § Conversation history (v2), § Application-envelope
  size cap, § Error codes — the published contract this implements, and the
  statement that an over-cap envelope is refused by the transport with
  `message.too_long` rather than silently dropped.
- `docs/knowledge/features/` — searched for a history/relay-inbound overview;
  the package overviews for this area predate #2112–#2115 and carry nothing that
  changes this design. Noted so the reading list is honest rather than padded.

`codegraph_context` was run once for this ticket's title + AC and returned
unrelated `run`/`Run` symbols with no history or relay entry points. Gap noted;
the surface above came from targeted `codegraph_search`-equivalent greps and
direct reads.

## Context

A client opening an existing conversation sees nothing that happened before it
connected, because nothing answers `request_history`. Everything under it has
landed: the log (#2112), the wire vocabulary (#2113), and three producers
(#2114, #2115). This ticket is the join.

Two things § Conversation history explicitly left here: the four reject **codes**,
and the **byte budgeting**. Both are decided below.

**No ADR is warranted.** The two decisions with contract weight — the dedup key
and the reject-code set — belong in `docs/protocol-mobile.md`, which is where a
client author looks, and this ticket publishes them there. Nothing here changes a
system-level structure that an ADR would record.

**Deleted conversations are a documentation decision, not a code one.**
`history.Store` has no delete, remove or archive method, so there is nothing to
mint. `handlers.DeleteConversation` and the idle sweep both drop a conversation
from the registry while its log stays on disk; the `KnownConversation` gate then
makes that history unreachable — the fail-safe direction. The residue is
on-disk only. This is the ring's #1502 gap in a durable form, and the spec text
cross-references it.

## Design

### Where it runs

`dispatchAppFrame` gains a `protocol.TypeRequestHistory` case that **tags and
falls through** to `enqueueAppFrame`, exactly as the `TypeAttachmentChunk` and
`TypeRequestAttachment` arms do. Answering a request reads log segments off disk
and marshals a page; that must not run on the Run goroutine (#965's cross-conn
head-of-line stall). `appFrameKind` gains `appFrameHistoryRequest`, and
`appFrameWorker` gains the arm that calls `handleRequestHistory`. The case
selector is what `TestEveryInboundV2TypeHasHandler` reads, so handing off
satisfies the guard.

**Both answers leave by the same route**, and that is the one place this differs
from #2054: a page is a single envelope, not a stream, so there is no `Push` leg.
Page and reject alike go through `forwardToRun` so Run seals them under `s.send`.
Emitting from the worker goroutine would be a concurrent `Encrypt` on the
single-owner send `CipherState` — a nonce reuse.

### The seam

`internal/relay` must not import `internal/history`, and a comma-ok seam of
`AttachmentResolve`'s shape would erase the sentinel identity this handler needs
— the merged cursor refusal has to be separable from the other failures to pick
its code. So the seam carries an **outcome discriminant** rather than an error:

```go
// V2SessionConfig
HistoryPage HistoryPager

type HistoryPager func(conversationID, cursor string, limit int) HistoryPageResult

type HistoryPageResult struct {
    Entries []protocol.HistoryEntry // newest-first
    Cursor  string
    AtStart bool
    Outcome HistoryPageOutcome
}

type HistoryPageOutcome uint8
const (
    HistoryPageOK HistoryPageOutcome = iota
    HistoryPageBadCursor
    HistoryPageUnavailable
)
```

Two new exported types (`HistoryPager`, `HistoryPageResult`) plus the outcome
enum and its three constants. `protocol` types cross the seam rather than
primitives: `internal/relay` already imports `internal/protocol` in every file,
`HistoryEntry` mirrors `history.Entry` key for key, and the alternative — four
parallel primitive slices — would put the entry's field correspondence in a
second place that can drift from the frozen wire shape.

`HistoryPageOK` is the zero value **deliberately**: an implementation that
forgets to set an outcome reports success with no entries, which is the terminal
empty page — inert, not a false refusal. Every construction site in
`cmd/pyry/relay.go` names its outcome anyway.

**A nil seam makes the frame inert but consumed**, mirroring the nil
`AttachmentResolve` and `AttachmentIntake` guards: the handler returns before
parsing a single remote-authored byte, and the frame no longer draws
`dispatch.Route`'s unknown-type reply.

### The `cmd/pyry` adapter

`cmd/pyry/relay.go` wires the seam over `w.hist`, classifying `Store.Page`'s
sentinels with `errors.Is` — the `historyAppendFailure` pattern, one package
boundary later:

| condition | outcome |
|---|---|
| `w.hist == nil` | `HistoryPageUnavailable` |
| `errors.Is(err, history.ErrInvalidCursor)` | `HistoryPageBadCursor` |
| any other `err != nil` | `HistoryPageUnavailable` |
| `err == nil` | `HistoryPageOK` + the mapped entries |

**The mapped slice is sized from `len(page.Entries)`, never from `limit`.** This
is `RequestHistoryPayload`'s "NEVER ALLOCATE FROM THIS CLAIM" rule at the one
place in this ticket where it could be broken: `make([]protocol.HistoryEntry, 0,
limit)` reads as ordinary Go and would pin capacity chosen by the client.

`ErrInvalidCursor` covers all three merged causes: `parseCursor` raises it for a
cursor that does not decode and for one minted for another conversation, and
`Page`'s own `cursorRefusal` raises it for a cursor naming a segment not in this
log. Verified against `cursorRefusal`, which wraps `ErrInvalidCursor` for every
refusal it builds.

`ErrInvalidID` and `ErrInvalidPageSize` fall to `HistoryPageUnavailable` and are
**unreachable by construction**: the handler's `KnownConversation` gate fires
first and the registry holds canonical ids only, and the handler never passes a
limit below 1. Mapping them to the retryable "unavailable" rather than to a
client-facing refusal is the fail-safe direction — a daemon bug reads as a daemon
problem, not as the client's malformed request.

**The error never leaves the adapter**, because `internal/history`'s messages
format absolute filesystem paths ("open segment %q", "resolve log directory %q").
The handler cannot disclose what it never receives. It is **not silently
discarded**, though — unlike `attachmentResolve`, whose error must die because
its message carries a raw client string. Here the adapter logs a **content-free
discriminant** derived by `errors.Is`, the `historyAppendFailure` pattern one
package boundary later: `invalid_cursor`, `not_contained`, `corrupt_segment`,
`unknown_version`, `read`. An operator has to be able to see that a segment has
gone corrupt; a client must not. `ErrNotContained` earns its own discriminant
because it is the symlink-containment signal and reads as an attack, even though
it is unreachable past the membership gate.

### The handler — ordered gates

`handleRequestHistory(ctx, s, plaintext)`, on the worker goroutine, taking the
plaintext because the worker receives bytes. The ordering carries the security
property, not merely the presence of the checks:

1. **Nil `HistoryPage` seam → inert and consumed.** Zero parsing of
   remote-authored bytes on an unwired daemon.
2. **Envelope decode failure → reply nothing.** Unreachable (`dispatchAppFrame`
   already decoded these bytes to match the type) and there is no envelope id
   left to correlate a reply to. Never echo the error.
3. **Payload decode failure → `history.invalid_request`.** Rejected, not
   tolerated — `RequestHistoryPayload`'s own block assigns that obligation here,
   and `handleRequestSnapshot`'s tolerance does not transfer because this
   payload's zero value would reach a path join. Nothing about the failure is
   echoed or logged: `encoding/json` quotes offending input into its error
   string, and those bytes are remote-authored.
4. **Membership gate → `conversation.not_found`.** `KnownConversation`, not a
   session router: `SessionRouter.Route` answers retryable
   `server.binary_offline` for a known-but-unbound conversation, which is
   precisely the reopened conversation this verb exists to serve. A nil
   `KnownConversation` refuses everything — the only fail-safe reading. This
   covers the non-canonical id too: the registry holds canonical ids only, so a
   malformed one fails membership.
   **Distinguishable, unlike #2054.** That handler merges an unknown conversation
   into `attachment.not_found` because a distinguishable answer would rebuild the
   path-existence oracle its merge exists to prevent. That reasoning does not
   transfer: there is no second id to protect here, the published Rejects list the
   conversation condition separately from the cursor's merged answer, and
   `handleRequestSnapshot` already answers a foreign id distinguishably.
5. **Negative limit → `history.invalid_page_size`.** `0` is not a reject.
6. **Page, budget, emit** (below). A `HistoryPageBadCursor` outcome →
   `history.invalid_cursor`; `HistoryPageUnavailable` → `history.unavailable`.

The conversation id is logged **only after step 4**, where membership has
established it is canonical — the § Conversation history rule that both
client-supplied strings are loggable only after their shape is validated. The
**cursor is never logged on any arm**: nothing validates its shape, `history`'s
own `cursorRefusal` already declines to echo one, and raw it is the log-injection
shape § Attachments forbids.

### Page size — the daemon's decision, never the client's ask

Three narrowings, in order:

- `limit == 0` (sent as zero or omitted) → `defaultHistoryPageEntries`, an
  unexported constant in `internal/relay` set to **50**, matching the committed
  `testdata/request_history.json` fixture's ask.
- `limit > maxHistoryPageEntries` → narrowed to `maxHistoryPageEntries` = **1024**,
  an unexported relay-side ceiling. This is **not** a second copy of
  `history.MaxPageEntries`; it is a different ceiling for a different reason, and
  the reason is the byte budget § Conversation history assigns here. **The
  arithmetic:** an entry's minimum wire cost is its four always-present keys —
  `{"id":1,"type":"a","payload":0,"ts":"2026-09-05T00:00:00Z"}` is 58 bytes, and
  no entry can serialise below ~48 — so at most `65519/48 ≈ 1365` entries can ever
  fit one page. Asking the log for 4096 is therefore work that is **guaranteed**
  to be thrown away by the budget loop below.
  **It is a resource bound, not tidiness.** `history.MaxPageEntries` is 4096 and
  `MaxSegmentBytes` bounds one entry line at 1 MiB, so `limit: 4096` from a paired
  but hostile client forces the daemon to open ~256 segments and hold up to
  ~256 MiB of decoded entries — transiently, per request — before a single byte
  reaches the wire. 1024 cuts that by 4× and costs nothing real, since no page
  that large can be emitted. The published count ceiling stays 4096 and stays the
  log's; this sits under it, and § Page size already permits the daemon to return
  fewer entries than asked to fit the cap.
- **The byte budget** — the part § Conversation history assigns here.

### Byte budgeting

`history.MaxPageEntries` bounds a *count*; nothing bounds a stored entry's
payload, so a full page can exceed the 65519-byte application-envelope cap and be
refused by the transport with `message.too_long`.

**The budget is enforced by re-asking with a smaller limit, never by truncating
the returned slice.** Truncating is the tempting one-liner and it is wrong: the
page's `Cursor` names the position just before the *oldest entry the log
returned*, so dropping entries from the tail while keeping that cursor makes the
client's next ask skip exactly the dropped ones. That is the gap AC-1 forbids,
and it would be invisible in every test that walks a log whose pages all fit.

The loop, bounded and strictly decreasing:

```
limit := effective(req.Limit)
for attempt := 1; ; attempt++ {
    res := seam(convID, req.Cursor, limit)
    ...outcome gates...
    frame := marshal(envelope(res))
    if len(frame) <= maxAppEnvelopeBytes || len(res.Entries) <= 1 { emit; return }
    limit = historyFitLimit(res.Entries, payloadBudget)   // in [1, len(Entries)-1]
    if attempt >= maxHistoryPageFitAttempts { limit = 1 }
}
```

- `historyFitLimit` walks the entries newest-first, marshalling each and
  accumulating, and returns the largest prefix that fits the payload budget —
  clamped into `[1, len(Entries)-1]` so the limit **strictly decreases** every
  iteration. Termination is therefore structural, not a matter of the attempt
  cap.
- The attempt cap (**3**) exists only for the concurrent-append case: with an
  empty cursor the window is anchored at the newest entry, so an append landing
  between two asks changes the entry set and a computed fit can miss. It is a
  belt on a suspender, and it is deterministic code rather than a second
  estimate.
- **A single entry that still does not fit is emitted anyway.** Refusing would
  stall the walk permanently with no way past, and the client would learn nothing;
  emitting yields the transport's `message.too_long`, which is a distinguishable
  signal, and the entry may well fit since the budget is measured against the real
  marshalled frame. The arm logs a Warn with the entry count and the byte length
  — both content-free lengths.
- **A shortened page never sets `AtStart` on that account.** `AtStart` comes from
  the log, and a re-ask with a smaller limit returns the log's own answer for that
  limit. Nothing in the handler synthesises it.

`maxAppEnvelopeBytes = 65519` is minted as an unexported constant in
`internal/relay` (the repo has the number only in comments today), documented
against § Application-envelope size cap.

### The page/live boundary — the dedup key

**The key is `(type, ts)`**: a history entry's `type` and `ts` matched against a
live envelope's `type` and `ts`. Named in `docs/protocol-mobile.md`
§ Conversation history so a client can rely on it.

Why this and not `turn_id` + `seq`:

- **Both lanes already carry both fields, for every type.** `HistoryEntry`'s key
  set is frozen at `id` / `type` / `payload` / `ts` and pinned by
  `TestHistoryPagePayload_WireKeys`, so the key cannot be a new field.
  `Envelope` carries `type` and `ts` unconditionally.
- **`turn_id` does not cover every entry.** `internal/protocol/interactive.go`
  gives it to turn-scoped payloads only; `turn_state`, `stall`, `api_retry`,
  `compacting` and `session_transition` carry none.
- **`id` cannot be the key.** It is the durable log id; the live lane's `event_id`
  is the ring's per-process one, and #2113 forbids joining the namespaces. Worse,
  the session-transition producer skips the ring entirely, so its live envelope
  carries no `event_id` at all.
- **The two lanes carry the *identical* `ts` by construction.** Both #2114
  producers hoist one `ts := time.Now().UTC()` per logical event above the
  per-conn fan-out and hand that same value to `appendConversationHistory` and to
  every envelope. `time.Time` marshals through `MarshalJSON` identically on both
  sides.
- **It holds for a type with no `turn_id`.** `session_transition` is exactly that,
  and it is the type for which the log is the *only* retention.
- **#2115's producer cannot collide.** `newOperatorMessageHistory` appends without
  emitting a live twin, so the key is never asked to dedup it.

Uniqueness: within one conversation the chokepoint mints each `ts` from a
sequential `time.Now().UTC()` on a single emit path, so two entries of the same
type sharing a nanosecond is not producible. The published text states the key
as `(type, ts)` and notes that a client wanting belt-and-braces may compare
`payload` bytes on a tie — the log stores them verbatim, so they match.

### Reject codes

Four minted here, one reused. Static messages, none derived from an error value,
an id, a cursor or a path.

| condition | code | retryable | message |
|---|---|---|---|
| conversation id not canonical / not hosted | `conversation.not_found` (reused) | no | existing |
| payload did not decode | `history.invalid_request` | no | `history request rejected` |
| negative `limit` | `history.invalid_page_size` | no | `history page size is not valid` |
| cursor: undecodable / foreign / not in this log | `history.invalid_cursor` | no | `history cursor is not valid` |
| the log could not be read | `history.unavailable` | yes | `conversation history is unavailable` |

`history.unavailable` is the fourth new code and is not in the published Rejects
list, because that list enumerates *client* faults. It answers the daemon-side
failures — a corrupt or unknown-version segment, an I/O error, and an unwired-log
daemon reached past the nil-seam guard — and it is the only retryable one, since
every cause can clear without the client changing its request.

**No refusal echoes the cursor, the conversation id, or any payload byte**, on the
wire or into the log. Reject messages are fixed constants; the daemon-side
`reason` field that separates them in the log is a call-site constant, the shape
`rejectAttachmentRequest` established.

## Concurrency model

No goroutine is spawned. The handler runs on the existing per-conn
`appFrameWorker` (one per session, spawned in `handleNoiseInit`'s open tail,
terminated by `s.done` or `runCtx`), strictly FIFO: a conn has at most one
history request in flight, so a second waits behind the first rather than
doubling the segment reads in play. **That is why no per-verb concurrency limit
is minted** — § Conversation history leaves any such bound receiver-configured
and unpublished.

Replies reach the wire through `forwardToRun` → Run's `forwardAppReply`, so
`s.send.Encrypt` stays single-owner. The handler never touches `s.send`,
`s.recv` or session state.

`history.Store` is internally synchronised by one leaf mutex and holds no file
handle between calls, so the reads here take no lock of their own and interleave
safely with the three appending producers.

Cross-conn: AC-5's "a second frame is handled while the request is still being
answered" is about *other* conns — Run is free the moment `enqueueAppFrame`
returns. Within one conn the worker is deliberately serial.

## Error handling

| failure | answer |
|---|---|
| nil `HistoryPage` seam | consumed, inert, Debug log, no reply |
| envelope does not decode | no reply (no id to correlate); Warn |
| payload does not decode | `history.invalid_request` |
| `KnownConversation` nil or false | `conversation.not_found` |
| `limit < 0` | `history.invalid_page_size` |
| seam outcome `HistoryPageBadCursor` | `history.invalid_cursor` |
| seam outcome `HistoryPageUnavailable` | `history.unavailable` |
| page marshal fails | Warn, no reply (closed structs; unreachable) |
| `forwardToRun` returns false | Debug, reply abandoned (session tearing down) |
| conversation with no log | `HistoryPageOK` + empty terminal page (`at_start` true), **not** an error |

## Testing strategy

`internal/relay/v2session_history_request_test.go` — table-driven against a
fake `HistoryPager`:

- the ordered gates, one row per reject, each asserting **both** the wire code
  and that the emitted frame contains neither the requested cursor nor the
  conversation id nor any payload byte;
- nil seam ⇒ no reply at all and no parse (the fake pager records that it was
  never called);
- `limit` 0 ⇒ the pager sees `defaultHistoryPageEntries`; `limit` omitted from
  the JSON entirely ⇒ same; a positive over-`MaxPageEntries` ask ⇒ the pager sees
  it unchanged (the clamp is the log's);
- byte budgeting: a pager returning entries whose page exceeds
  `maxAppEnvelopeBytes` ⇒ a second ask with a strictly smaller limit, the emitted
  frame within the cap, and `at_start` **not** set by the shortening;
- the one-oversized-entry arm emits rather than refuses;
- **the truncation mutant**: a page shortened by dropping entries instead of
  re-asking must redden a test that walks two pages and asserts no entry is
  skipped.

`internal/relay/v2session_history_dedup_test.go` (or a section of the above) —
pins the dedup key: a stored `session_transition` entry (no `turn_id`) and its
live envelope carry equal `type` and `ts` on the wire.

`cmd/pyry/relay_history_seam_test.go` — the adapter's `errors.Is` classification:
each `history` sentinel to its outcome, `ErrInvalidCursor` reached via all three
of its causes, and a nil store ⇒ `HistoryPageUnavailable`.

`cmd/pyry/relay_guard_test.go` — move `"TypeRequestHistory"` from
`excludedTypes` to `inboundTypes` as switch-intercepted.

`internal/e2e/relay_v2_history_test.go` — the end-to-end walk over a real daemon
against fakerelay/fakephone, modelled on
`internal/e2e/relay_v2_attachment_retrieval_test.go`:

- a conversation with recorded traffic answered without the client sending a
  message first;
- echoing the cursor back walks to the start: every entry exactly once,
  newest-first, none skipped or repeated;
- the boundary page — filling exactly at the first entry — reports `at_start`
  false with a usable cursor, and the call after it is the empty terminal one;
- a conversation with no log ⇒ the same empty terminal page, not an error;
- **the page/live boundary**: an entry appended between the request and the
  response is seen exactly once across the page and the live stream, keyed on
  `(type, ts)`;
- **no head-of-line stall**: a second frame sent immediately after the request is
  answered while the request is still in flight.

Verification gate (§ B2): `go test -race` on `./internal/relay/...`,
`./internal/history/...`, `./internal/protocol/...`, `./cmd/pyry/...`,
`./internal/e2e/...`, plus `go vet ./...` and `go build ./cmd/pyry`.

## Documentation

`docs/protocol-mobile.md` only — the published wire contract, which is where the
codes and the dedup key have to live for the two parked client tickets
(pyrycode-desktop#1088, pyrycode-mobile#623) to read them:

- § Conversation history → Page size: the byte-budgeting behaviour, and that a
  short page is produced by re-asking rather than truncating;
- § Conversation history → Rejects: the five codes against their conditions;
- § Conversation history: the dedup key, and the deleted-conversation note
  (unreachable-by-gate, on-disk residue, cross-referencing #1502);
- § Error codes: four new rows;
- the § request_history / § history_page "nothing answers it yet" paragraphs,
  which are now false;
- a changelog entry.

Nothing under `docs/knowledge/` is written here — that is the documentation
phase's.

## Open questions

1. **Does the e2e harness expose a way to append to a conversation's history log
   between the request and the response?** If the fake-daemon harness has no
   direct hook, the boundary assertion is driven by real traffic through the
   interactive chokepoint instead, which is the more faithful test anyway.
   Resolve in Phase B; record the resolution under `## Revisions` if it changes
   the testing strategy.
2. **`defaultHistoryPageEntries = 50`** — chosen to match the published fixture
   rather than measured. If Phase B finds a typical page of 50 interactive
   entries routinely trips the byte budget (turning every first ask into two
   disk reads), lower it and record the measurement.

## Security review

**Verdict:** PASS (after two MUST FIX revisions, applied above before this
section was written)

**Findings:**

- **[Trust boundaries]** No finding. One explicit boundary: `handleRequestHistory`
  parses the frame into `protocol.RequestHistoryPayload` and holds three
  client-asserted values with three different fates, each stated in the design.
  `conversation_id` becomes trusted at exactly one place, `KnownConversation`,
  and only after that is it logged or path-resolvable. `cursor` **never becomes
  trusted in `internal/relay` at all** — it is passed opaquely to the seam and
  `history.parseCursor` is the only validator anywhere, which is why the handler
  can neither log it nor branch on it. `limit` is branched on for sign and zero
  and otherwise handed downward. The gates are ordered so the membership check
  fires before the id can reach a path join, which is `Store.Page`'s stated
  precondition.
- **[Trust boundaries / Network & I/O]** **MUST FIX — applied.** The plan's first
  draft passed the client's `limit` straight through to `history.Store.Page`,
  reasoning that the log's own clamp owns the ceiling. That is a resource
  amplification: `MaxPageEntries` is 4096 and `MaxSegmentBytes` bounds one entry
  line at 1 MiB, so a paired but hostile `limit: 4096` forces ~256 segment opens
  and up to ~256 MiB of decoded entries held per request before any byte reaches
  the wire — and every one of those entries is then discarded by the byte budget,
  since no page above ~1365 entries can serialise inside 65519 B. Fixed by
  narrowing to a relay-side `maxHistoryPageEntries = 1024` derived from the
  envelope cap, with the arithmetic in § Page size.
- **[Trust boundaries]** **MUST FIX — applied.** The `cmd/pyry` adapter maps
  `[]history.Entry` to `[]protocol.HistoryEntry`, and the idiomatic
  `make([]protocol.HistoryEntry, 0, limit)` would pin capacity from an
  attacker-chosen integer — the exact rule `RequestHistoryPayload` states and the
  one place in this ticket where it is breakable by writing ordinary Go. The plan
  now requires sizing from `len(page.Entries)`.
- **[Tokens, secrets, credentials]** Not applicable by design, and the design says
  why rather than the category being empty: the cursor is the only opaque token on
  this path and § The cursor publishes that it is **not a secret and not a
  capability** — trivially reversible, deliberately unsigned, carrying only the
  conversation id the client already knows. This handler mints none, stores none
  and compares none. The one obligation that survives is never logging it, which
  is stated per-arm.
- **[File operations]** No finding **in this ticket's code**, because it opens no
  path: `history.Store` owns every path it builds, `resolveDir` performs the
  symlink-resolved containment check, and `ErrNotContained` is its refusal. What
  this ticket owes the boundary is the **ordering** — `KnownConversation` before
  the seam — which is `Store.Page`'s documented precondition and is step 4 of the
  gates. `ErrNotContained` is given its own operator-visible discriminant in the
  adapter (see below) while staying merged on the wire.
- **[Subprocess / external command execution]** Not applicable: this path executes
  nothing and builds no argv. Named rather than skipped because the neighbouring
  `request_snapshot` verb does reach a subprocess-rendered screen and a reader
  might assume the same here.
- **[Cryptographic primitives]** One finding, and it is the reason the handler
  runs where it does. `s.send` is the single-owner Noise send `CipherState`;
  emitting a page or a reject directly from the `appFrameWorker` goroutine would
  be a concurrent `Encrypt` — a **nonce reuse**, the failure
  `v2session_attachment_request.go` names explicitly. Addressed structurally:
  this handler has exactly **one** emission path, `historyReply` → `forwardToRun`
  → Run's `forwardAppReply`, and no `Push` leg at all (a page is one envelope, not
  a stream), so it is strictly simpler to keep correct than #2054's two-route
  split. The handler touches no key material and mints no randomness.
- **[Network & I/O — resource exhaustion]** Bounded, and the bound is named rather
  than assumed. Inbound: the frame is already capped by the transport. Outbound:
  the byte budget. Work per request: `Store.Page` is bounded by the page rather
  than the log, save for one directory listing that grows with segment count.
  Request rate: the per-conn `appFrameWorker` is strictly FIFO with **one history
  request in flight per conn**, and `enqueueAppFrame` tears the conn down at 4421
  past `appFrameQueueDepth`. That serialisation **is** the concurrency bound,
  which is why no per-verb limit is minted here — § Conversation history leaves
  any such bound receiver-configured and unpublished. Combined with the 1024
  ceiling above, one conn's worst-case transient footprint is one page's decoded
  entries, freed per request.
- **[Error messages, logs, telemetry]** No finding after the design's rules, which
  are stated per-arm rather than as a blanket sentence: reject messages are fixed
  constants and none is derived from an error, an id, a cursor or a path; the
  **cursor is never logged on any arm** (nothing validates its shape, and
  `cursorRefusal` already declines to echo one); the conversation id is logged
  **only past the membership gate**, where it is registry-canonical; **no entry
  payload, type or timestamp ever reaches the logger** — the success line carries
  conn id, conversation id, entry count, `at_start`, `in_reply_to` and a byte
  length, all content-free. The `history` error itself never crosses the seam,
  because its messages format absolute filesystem paths.
- **[Concurrency]** No finding, and one accepted race, named. The handler spawns
  no goroutine and takes no lock; it touches two locks **sequentially and never
  nested** — the conversations registry's inside `KnownConversation`, which has
  returned before the seam is called, and `history.Store`'s leaf mutex inside it —
  so there is no ordering rule to violate. Shutdown is all-or-nothing: a page is a
  single envelope and `forwardToRun` abandons it on `s.done` or `runCtx`, leaving
  no partial state. **The accepted race:** a conversation deleted between the
  membership gate returning true and `Store.Page` reading its log yields one last
  page for a conversation that no longer exists. Closing it would need the
  registry and the log under one lock, which no seam offers and which the
  deleted-conversation decision above explicitly declines to build; every
  subsequent request is refused, so the fail-safe direction holds after a
  one-scheduling-window gap.
- **[Threat model alignment]** § Security model **threat 1 lands on
  `history_page`** — an entry's `type` and `payload` are replayed content,
  `claude`-authored for a stored assistant frame — and #2113 puts the sanitisation
  on the client, which is only sound because a page mirrors the live frame exactly.
  The obligation that creates **for this ticket** is a negative one and is easy to
  violate helpfully: the handler must forward payload bytes **verbatim** and must
  not re-validate, re-encode or transform them, or a page stops being reducible
  through the client's live-lane reducer. Stated in the design.
  Threat: **cross-conversation read.** A cursor minted for conversation A and
  presented with conversation B is refused by `parseCursor`'s binding, merged into
  `history.invalid_cursor` with garbage and foreign-log cursors alike, so a probe
  cannot tell the three apart.
  **Inherited, not decided here:** `KnownConversation` is daemon-wide membership,
  not per-device authorization, so any paired device can read any hosted
  conversation's history. That matches `request_snapshot`, `request_attachment`
  and `send_message`, and § Conversation history publishes it — "naming a
  conversation here is not authorization... Authorization is pairing." Narrowing
  it would be a change to the pairing model and belongs to no ticket here.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-05

## Revisions

### 2026-09-05 — Phase B, resolving the Open Questions

**Open question 1 — how the e2e gets entries into a conversation's log.**
Resolved: the test seeds them through `history.New` over the daemon's instance
directory (`<home>/.pyry/test`) **before the daemon starts**, using the log's own
`Append`. That is the choice `TestRelayV2_AttachmentRetrieval` already makes about
a stored file and for the same reasons — the producers are #2114's and #2115's
subject and are covered where they live, driving one here would make this run
depend on them, and it would cost a full turn round trip to reach the interactive
chokepoint. Seeding through `Append` means what lands is byte-identical to what a
producer leaves, and doing it before startup makes this process the only writer at
any moment, so the "two stores mint duplicate ids" hazard never arises.

**Open question 2 — `defaultHistoryPageEntries = 50`.** Unchanged. Nothing in
Phase B contradicted it; the byte-budget tests drive shortening with deliberately
large fixtures rather than with realistic ones, so the default was never the
thing under measurement.

**Design departure — where the page/live boundary is proven.** The plan put the
whole of AC-4 in the e2e. Split in implementation, and the split is what makes
each half falsifiable:

- The **dedup key** (`type`, `ts`) is pinned in `internal/relay` by
  `TestV2Session_RequestHistory_DedupKeyHoldsWithoutTurnID`, over a
  `session_transition` entry — a type carrying no `turn_id`, whose producer skips
  the replay ring so its live envelope carries no `event_id` either. It compares
  the two lanes' **marshalled wire forms**, which is the claim that matters, and
  it does so deterministically.
- The **no gap, no duplicate** guarantee is pinned by
  `TestV2Session_RequestHistory_ShortensAPageToFitTheEnvelopeCap` and by the e2e's
  walk, both of which assert every entry exactly once across a whole walk. The
  shortening test is the one that kills the truncate-instead-of-re-ask
  implementation; a single-page assertion cannot.

An e2e that appended a live entry mid-request would have had to race the daemon's
own interactive chokepoint to produce a live twin at all, and a test whose subject
may or may not appear cannot assert "exactly once" — it would have read green
against a broken key. What the e2e does carry instead is the walk, both reject
codes on the wire, the static-message and never-log claims checked against the
values a leak would really contain, and the no-stall frame.

**Addition not in the plan — `historyPageFailure`.** The security review required
the adapter to log a content-free discriminant rather than discard the error
(§ Error messages, logs, telemetry). Implemented as `historyPageFailure` in
`cmd/pyry/conversation_history.go`, the read-path twin of `historyAppendFailure`,
naming six sentinels plus a default. `ErrNotContained` gets its own arm despite
being unreachable past the membership gate, because an occurrence is a
symlink-containment attack signal rather than a malfunction.
