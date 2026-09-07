# #2166 — Announce a host-produced attachment and prove the retrieval round trip

## Files read

Production surface the design sits on:

- `cmd/pyry/attach_file.go` → `fileAttacher` — the store site. It already mints the id,
  calls `attachments.EnsureDir` / `attachments.Store`, and discards `Store`'s returned
  path. Its refusal sentences and its single content-free `log.Info` fix the logging
  discipline this slice must not break.
- `cmd/pyry/modal_resolve_v2.go` → `streamApprovalBridge.broadcast` — the fan-out to
  copy: one shared timestamp hoisted out of the loop, the #607 `c.Interactive`
  capability gate, a monotonic envelope id taken under the bridge's `mu`, an early
  `return` on `ctx.Err()`, and a Push-error `Debug` that names no payload byte.
  `streamApprovalBridge`'s own doc block fixes the "mu is a leaf lock" discipline.
- `cmd/pyry/queue_state_v2.go` → `queueStateEmitterV2.broadcast` — the sibling that
  *does* log `"err", err` on a dropped push. AC 3 asks for the transport sentinel, so
  this emitter's field set is the one to copy, not the bridge's (which omits `err`).
- `cmd/pyry/interactive_turn_v2.go` → `interactiveBroadcaster` — the two-method
  consumer-side interface (`ActiveConns`, `Push`) `*relay.V2SessionManager` satisfies.
  The announcer needs exactly this and nothing more.
- `cmd/pyry/relay.go` → `startRelay`, `startRelayV2` — where `mgr` is built and where
  `surface` is threaded out. `startRelay`'s early return on an empty `relayURL` is what
  makes the announce hook legitimately absent, and is the shape AC 2 names.
- `cmd/pyry/main.go` → `runSupervisor` — the composition root; the `SetApprovalSurfacer`
  / `SetFileAttacher` window between `control.NewServer` and `Listen`.
- `internal/attachments/storage.go` → `Store` — returns
  `filepath.Join(dir, SanitizeFilename(filename))`. That return value is the whole of
  AC 1's "the name the file was actually stored under", and `SanitizeFilename`'s own
  truncation to `protocol.MaxAttachmentFilenameBytes` is why no length check is needed.
- `internal/relay/v2attachmentstream.go` → `V2SessionManager.StreamAttachment` — the
  retrieval leg publishes `filepath.Base(path)`. Announcing `filepath.Base` of `Store`'s
  return makes the two one string rather than two derivations.
- `internal/relay/v2session.go` → `V2SessionManager.Push` — its entire error surface is
  `ctx.Err()` and `ErrConnNotFound`; both are content-free, which is what makes logging
  the sentinel safe. It never rewrites `Envelope.ID`, so a per-emitter counter is sound.
- `internal/protocol/attachments.go` → `AttachmentOfferedPayload` — three always-present
  strings, no `omitempty`, nothing enforced here by design.
- `internal/control/server.go` → `Server.Serve` — spawns a goroutine per accepted conn,
  so two `attachment.file` calls can be in flight at once. That is why `nextID` needs a
  mutex rather than the single-producer-goroutine assumption the queue-state and
  session-error emitters rest on.
- `internal/control/client.go` → `AttachFile` — the client the e2e dials; its doc already
  names #2166's e2e as one of the two reasons it was shipped beside the verb.

Tests and harness:

- `internal/e2e/relay_v2_attachment_retrieval_test.go` → `TestRelayV2_AttachmentRetrieval`,
  `sendRequestAttachment` — the fetch leg this run re-uses verbatim.
- `internal/e2e/relay_v2_attachment_upload_test.go` → `attachmentFixture`,
  `nextAttachmentEnvelope`, `assertNoAttachmentLeakInLogs` — the multi-chunk fixture, the
  envelope pump, and the never-log assertion, all reused.
- `internal/e2e/relay_v2_stream_modal_test.go` → `TestRelayV2_StreamModalPermissionRoundTrip`
  — the precedent for dialling a control client directly instead of driving an MCP child,
  and for why the seeded ids must line up.
- `internal/e2e/harness.go` → `StartStreamInteractiveWithRelay`, `seedBootstrapRegistry`,
  `seedBoundConversation` — the first calls the second internally, pinning the bootstrap
  session to `initialUUID`; the third writes a row whose `cwd` is `home`. Together they
  give a live session bound to a conversation whose recorded workspace is `home`, which
  is exactly what `fileAttacher` resolves its destination from.
- `cmd/pyry/attach_file_test.go` → `newAttachFixture`, `assertContentFree` — the second
  `fileAttacher` call site, and the existing content-free assertion to extend.
- `cmd/pyry/interactive_turn_v2_test.go` → `fakeInteractiveBcast`, and
  `cmd/pyry/stream_approval_test.go` → `dismissalRecorder` — the in-tree
  `interactiveBroadcaster` doubles to mirror.

Package overviews:

- `docs/knowledge/features/control-plane-attachment-file-confine-and-store-a-claude-named-path.md`
  — #2164's record. Two things change how this ticket is built: refusals are static
  sentences that are *not logged at all*, so the announce must not become a per-refusal
  log; and a `/clear` rotation makes the verb refuse for a live child (fail-closed), which
  is why the e2e must not rotate between seeding and dialling.
- `docs/knowledge/features/attachments-package.md` — `Store` / `EnsureDir` / `ResolvePath`
  and the never-log-a-filename discipline this slice inherits unchanged.

## Context

`attachment_offered` was published by #2082 with no producer. #2164 gave the daemon a
verb that files a claude-named host file under the calling session's conversation, and
#2168/#2169 gave that verb a caller a real claude can drive. So a file now lands and no
client is ever told: the id exists only in the control-socket reply that went back to
claude. This slice closes that loop — the store announces, the document says so, and one
fake-daemon e2e proves a client can act on the announcement by fetching the bytes.

No wire shape is invented. `TypeAttachmentOffered`, `AttachmentOfferedPayload`, the golden
fixture and the relay guard's `"push"` classification all landed with #2082; this ticket
adds a producer and does not touch `internal/protocol`.

**Sizing overage, stated rather than hidden.** The refiner sized this at ~1000 lines of
total written work against the 800-line ceiling. Every other line of the boundary table
holds with margin: 4 production source files, 0 new exported types, 4 call sites needing
simultaneous update, 5 acceptance criteria, 2 new reject branches. The overage sits in the
doc correction and in this spec. The only available cut — a docs-only tail, or splitting
the e2e proof from the announce it proves — produces a child whose sole consumer is its
sibling, which is the one-consumer shape the floor rule forbids; and it would leave a
ticket that announces with no evidence a client can act on the announcement, the exact gap
this family exists to close. The floor wins over the ceiling, so it stays one ticket.
Re-counted against this written plan, not just against the pre-writing sketch.

**No ADR is warranted.** Every decision here is an application of an existing precedent
(`modal_shown`'s fan-out, `SetApprovalSurfacer`'s nil-tolerance, the never-log rule), and
the one genuinely new fact — that the announced name and the retrieved name are one
string — belongs in § Attachments, where this ticket writes it.

## Design

### The announcer — `cmd/pyry/attachment_offer_v2.go` (new)

One unexported type in `package main`, alongside the other v2 emitters:

```go
type attachmentOfferEmitterV2 struct {
    bcast  interactiveBroadcaster
    ctx    context.Context
    logger *slog.Logger

    mu     sync.Mutex
    nextID uint64
}

func newAttachmentOfferEmitterV2(bcast interactiveBroadcaster, ctx context.Context, logger *slog.Logger) *attachmentOfferEmitterV2

// announce fans one attachment_offered envelope to every interactive-capable conn.
func (e *attachmentOfferEmitterV2) announce(conversationID, attachmentID, filename string)
```

`announce` is `streamApprovalBridge.broadcast` with the payload fixed to
`protocol.AttachmentOfferedPayload` and the type fixed to `protocol.TypeAttachmentOffered`:

1. marshal the payload once; on error, log one content-free `Warn` and return;
2. take one `time.Now().UTC()` for the whole fan-out;
3. for each `bcast.ActiveConns(e.ctx)`, skip `!c.Interactive` (the #607 gate);
4. bump `nextID` under `mu` and build the envelope;
5. `bcast.Push`; on error, return early if `e.ctx.Err() != nil` (teardown), otherwise log
   one `Debug` and continue to the next conn.

`mu` is a leaf lock held only around the counter bump, never across `Push` or
`ActiveConns` — the same rule `streamApprovalBridge.mu` records. It is not decorative:
`control.Server.Serve` runs each conn on its own goroutine, so two `attachment.file`
calls can announce concurrently.

The counter is per-emitter, matching every sibling emitter in this package
(`interactiveTurnEmitterV2`, `queueStateEmitterV2`, `sessionErrorEmitterV2`,
`streamApprovalBridge` each carry their own). `V2SessionManager.Push` does not rewrite
`Envelope.ID`, so this is a property of the existing design rather than a new claim.

**Log fields.** Two events, both content-free:

- `attachment_offer.marshal_err` (`Warn`) — no field beyond `event`. Defensive:
  `AttachmentOfferedPayload` is three strings and cannot fail to marshal in practice.
- `attachment_offer.push_err` (`Debug`) — `conversation_id`, `attachment_id`, `conn_id`,
  `env_id`, `err`. `err` is `Push`'s own return, whose entire surface is `ctx.Err()` and
  `relay.ErrConnNotFound` — a transport sentinel carrying no content.

**`filename` reaches no log field on any branch.** That is the § Attachments ban, which
sanitising does not lift, and it is the reason `attachment_offer.push_err` names the two
ids rather than "the offer".

### The seam — a bare func, nil-tolerant

`fileAttacher` gains a fourth dependency before its logger:

```go
func fileAttacher(
    convReg *conversations.Registry,
    live func(sessions.SessionID) error,
    instanceDir string,
    announce func(conversationID, attachmentID, filename string),
    log *slog.Logger,
) func(sessionID, path string) (string, error)
```

A bare func rather than an interface, for the reason `snapshotSettings` and
`settingsUpdaterAdapter` are bare funcs: the value crossing out of `startRelayV2` should
be a primitive-shaped closure, and a test double is then a recording closure rather than a
type. `announce` may be nil and a nil hook is silently skipped — the nil-tolerance
`SetApprovalSurfacer(nil)` already has, not the always-installed approval registry's
shape. The hook is absent exactly when the relay leg is: `startRelay` returns early when
`relayURL` is empty, before any manager exists.

### Where the hook is built and how it travels

`startRelayV2` builds the emitter over `mgr` — the `*relay.V2SessionManager` that already
satisfies `interactiveBroadcaster` — **outside** the `if w.approvals != nil` block, since
it needs `mgr` and nothing from `w.approvals`. Both `startRelayV2` and `startRelay` gain a
third result:

```go
(drain func(), surface func(permbridge.Request) func(), announce func(conversationID, attachmentID, filename string), err error)
```

`startRelay`'s empty-`relayURL` early return yields `nil` for it, alongside the `nil`
surface it already returns. `runSupervisor` receives it and passes it to `fileAttacher` at
the existing `SetFileAttacher` call.

Construction ordering: after `mgr` is built, before `mgr.Run`'s goroutine starts — the
same window the bridge's post-construction assignments use, so there is no write to any
field a Run-goroutine reader could see mid-write.

### The announced name

`fileAttacher` stops discarding `Store`'s return:

```go
stored, err := attachments.Store(dir, filepath.Base(resolved), data)
// …
announce(string(conv.ID), string(id), filepath.Base(stored))
```

`Store` returns `filepath.Join(dir, SanitizeFilename(filename))`, and `filepath.Base` of
that **is** the sanitised leaf — not by convention but because `SanitizeFilename`'s
allowlist is `[a-zA-Z0-9_.-]` with every other rune rewritten to `_`, so no separator can
survive, and its leading-`.` prefix rule makes `"."` and `".."` unreachable. `Join`
therefore cleans nothing away and `Base` returns exactly what was written.
`StreamAttachment` publishes `filepath.Base` of `ResolvePath`'s output for the same file,
so the announced name and the retrieved name are one string derived one way, which is what
AC 1 asks a test to pin.

Three obligations discharge at once and none needs new code: sanitisation is `Store`'s;
the 255-byte ceiling is `SanitizeFilename`'s truncation to
`protocol.MaxAttachmentFilenameBytes`; the `attachment_id` shape is `conversations.NewID`'s.
**No redundant length check is added** — the ticket says so explicitly and the doc will say
why.

### Call ordering inside the closure

The announce fires after `Store` returns nil and after the existing `log.Info`, immediately
before `return string(id), nil`. Two consequences, both AC text:

- a refused store announces nothing, because every refusal returns before this point;
- a failed push does not turn a successful store into a refusal, because `announce`
  returns nothing and the closure's return value is unchanged by it.

## Concurrency model

No goroutine is spawned. `announce` runs synchronously on the control-server handler
goroutine that is servicing `attachment.file`, mirroring `streamApprovalBridge.broadcast`,
which runs on the handler goroutine servicing `mcp.approve`. It is bounded: `ActiveConns`
is a snapshot under the manager's own lock and `Push` enqueues without blocking (a full
queue drops or latches overflow rather than waiting).

Locks: `attachmentOfferEmitterV2.mu` is a leaf, taken only around `nextID++`. It never
nests inside the manager's `pushMu`, because it is released before `Push` is called, so
there is no lock order to reason about.

Shutdown: the emitter holds the daemon ctx captured at construction. On teardown
`ActiveConns` returns nil once the ctx is cancelled or `Run` has exited, so a late announce
fans out to nobody; a `Push` that races teardown returns `ctx.Err()` and the loop returns
early rather than logging per conn.

## Error handling

| Failure | Behaviour |
|---|---|
| `announce` is nil (no relay leg) | Skipped. The store still succeeds and the verb returns the minted id. |
| Payload marshal fails | One content-free `Warn`; no frame; the store still succeeded. |
| `Push` returns `ErrConnNotFound` (torn-down conn) | One `Debug` naming the sentinel and the correlation ids; the loop continues to the next conn. |
| `Push` returns a ctx error (daemon teardown) | The loop returns immediately; no per-conn logging. |
| Store, read, confinement or lookup refusal | Returns before the announce; nothing is broadcast and nothing is logged, matching #2164's "refusals are not logged at all". |

No new reject code and no new wire error: this frame is a push with no reply, so a failed
push has nobody to tell.

## Testing strategy

### `cmd/pyry/attachment_offer_v2_test.go` (new)

Table-driven over an `interactiveBroadcaster` double, mirroring `fakeInteractiveBcast`:

- one interactive conn receives exactly one `attachment_offered` whose decoded payload
  carries the three values passed in;
- a non-interactive conn receives nothing (the #607 gate), and a mixed set delivers only
  to the interactive members;
- a double whose `Push` returns `relay.ErrConnNotFound` for the first conn still delivers
  to the second, and `announce` returns normally;
- a cancelled ctx makes the loop return without pushing past the first error;
- two announces produce strictly increasing envelope ids;
- the captured log buffer never contains the filename, on the success path and on both
  error branches. The needle is a distinctive fixture name so the assertion cannot pass
  vacuously; the test asserts the log *does* contain the attachment id first, so an empty
  buffer fails loudly (`assertNoAttachmentLeakInLogs`'s own guard, borrowed).

### `cmd/pyry/attach_file_test.go` (extended)

`newAttachFixture` gains a recorded announce hook (a slice of observed triples under a
mutex) and a variant constructor for the nil-hook daemon:

- a successful store announces once, and the announced `filename` equals the leaf of the
  single file actually present in the attachment directory on disk — read from the
  filesystem, not recomputed from the input, so a wrong derivation reddens;
- the announced conversation id and attachment id are the ones the verb resolved and
  minted (the returned id, and the fixture's conversation);
- a claude-authored name that sanitisation changes (e.g. one carrying a separator or
  leading dot) announces the **sanitised** name, which is the case that distinguishes
  `filepath.Base(stored)` from `filepath.Base(resolved)`;
- each refusal branch announces nothing — table-driven over the existing refusal cases;
- the nil-hook fixture returns a minted id and writes the file, proving AC 2's
  no-announce-hook daemon.

### `internal/e2e/relay_v2_attachment_offer_test.go` (new) — the round trip

`TestRelayV2_AttachmentOfferedRoundTrip`, following `TestRelayV2_AttachmentRetrieval`'s
shape:

1. `pair`, then `seedBoundConversation(t, home, knownConvID, initialUUID)` so the
   conversation is in the registry the daemon loads at startup, bound to the bootstrap
   session `StartStreamInteractiveWithRelay` pins via `seedBootstrapRegistry`, with `cwd`
   set to `home`;
2. write the multi-chunk `attachmentFixture` bytes to a file **under `home`** — inside the
   conversation's recorded workspace, so `confineFile` admits it — under a distinctive
   name that doubles as the never-log needle;
3. start the daemon with the fake relay, handshake one interactive phone;
4. from the test process, dial `control.AttachFile(ctx, h.SocketPath, {SessionID:
   initialUUID, Path: <the file>})` — the same client `pyry mcp-files` calls, and the
   `control.Approve` shape the stream-modal round trip uses. No routed turn is needed and
   no MCP child is driven;
5. pump envelopes until an `attachment_offered` arrives; assert its `conversation_id` is
   `knownConvID`, its `attachment_id` equals the id `AttachFile` returned (the pin that the
   announcement names the file that was actually stored), and its `filename` is the stored
   leaf;
6. send `request_attachment` naming **the announced id** — never the one from the control
   reply — so the run proves a client can act on the announcement alone;
7. reassemble with `relay.ReassembleAttachment` and assert the bytes equal the fixture,
   and that the `attachment_chunk` frames' `filename` equals the announced one — AC 1's
   "one string, not two derivations", checked end to end across two independent legs;
8. `assertNoAttachmentLeakInLogs` over the daemon's stderr.

The run costs no routed turn: `attachment.file` resolves its destination from the session,
and `request_attachment` reads its conversation off the frame. That keeps it in the same
cost class as the retrieval neighbour, one seeded conversation heavier.

### Doc

`docs/protocol-mobile.md`:

- § Attachments gains the producer's half: a host-produced attachment is stored under the
  conversation exactly as an uploaded one, is announced rather than delivered, and is
  fetched with the same verb — there is no second retrieval path; the announced name is
  the stored name; and the offer is **live-only** — unlike `modal_shown` there is no
  outstanding-offer registry and no list verb, so an offer missed while disconnected is
  not replayed.
- Every live sentence naming #2083 as an unbuilt future producer is corrected: the
  `attachment_offered` row in § Application message types, the `####` subsection's
  "nothing emits it yet" and "#2083 takes the name from the model's own tool call", and
  the closing paragraph assigning sanitisation and both ceilings to #2083 — which this
  ticket answers by taking the name from `Store`'s return, so the doc records that they
  are discharged rather than adding checks. Afterwards `grep -n '#2083'
  docs/protocol-mobile.md` returns only the dated `2026-09-06` changelog entry, which is
  historical and is not rewritten.
- § Changelog gains one dated entry; existing dated entries are appended to, never
  rewritten.
- § Attachments' blanket "Nothing emits, accepts or enforces any of this yet" is
  **deliberately left alone** — it went stale when #2053/#2054 landed for reasons
  unrelated to this frame, #2082 examined it and left it, and this slice corrects only
  what it itself falsifies.

### Verification gate

`go test -race ./cmd/pyry/... ./internal/e2e/...` (the latter with `-tags e2e`),
`go vet ./...`, `go build ./cmd/pyry`. The full-module race suite is the verifier's gate.

## Open questions

1. **Does `attachments.ResolvePath`'s leaf equal `Store`'s leaf for the same file?** The
   design asserts they are one string; step 7 of the e2e is the check. If they diverge the
   e2e reddens and the fix is in this ticket's scope, since AC 1 names the property.
2. **Does the e2e's `attachment_offered` arrive before or after the bootstrap session's
   own pushes?** Immaterial to correctness — the envelope pump classifies by type — but
   the deadline must be generous enough for the daemon to finish reading a multi-chunk
   file off disk before announcing. Start from the retrieval neighbour's 15s.
3. **Does `attachment_offered` need to survive the relay guard's classification test?**
   #2082 classified it `"push"` in `cmd/pyry/relay_guard_test.go`, so no change is
   expected; confirm the guard stays green rather than assuming it.

Each is resolved in Phase B and any that changes the design is recorded under
`## Revisions`.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No findings — the boundary is single and explicit. The one
  untrusted value in this frame is `filename`, which is claude-authored, and the design
  takes it from `attachments.Store`'s **return value** rather than re-deriving it from the
  request path. `Store` sanitises through `SanitizeFilename` before writing, so there is
  no second derivation that could route around the sanitiser; announcing
  `filepath.Base(resolved)` instead would have been exactly that bypass, and is the
  mistake this design is shaped to make unavailable. The other two fields cross no
  boundary: `conversation_id` is the row `fileAttacher` resolved from the calling
  **session** (never from the request — #2164's destination rule carries into the
  announcement unchanged, so a hostile caller cannot announce into a conversation it has
  no part in), and `attachment_id` is `conversations.NewID()`'s crypto/rand UUIDv4.
- **[Trust boundaries]** No findings — both announced ids are **shape-checked before the
  announce is reachable**. `attachments.EnsureDir` validates `conversationID` and
  `attachmentID` against `conversations.ValidID` because both become path components, and
  it runs before `Store`; a malformed or empty id fails there and returns
  `errAttachStoreFailed` without announcing. So the "a receiver must not join an empty id
  into a path" hazard `AttachmentOfferedPayload` warns about cannot be produced by this
  producer — structurally, not by a guard this ticket adds.
- **[Tokens, secrets, credentials]** No findings — no token is minted, stored or rotated.
  The attachment id is not a capability: `attachmentResolve` builds the retrieval path
  from `ResolvePath(instanceDir, conversationID, attachmentID)` where `conversationID`
  came off the request frame and was already validated by `KnownConversation`, so an
  announced id for conversation A cannot be redeemed by naming conversation B — the
  lookup is keyed on both and answers `attachment.not_found`. Announcing therefore grants
  nothing an unannounced id would not, which is what § Attachments already publishes.
- **[File operations]** No findings — this slice opens no file, constructs no path and
  creates nothing. `filepath.Base(stored)` is consumed only as payload text and never
  reaches a filesystem call, so even a pathological value cannot traverse anything
  daemon-side. The confinement, TOCTOU and mode work is #2164's and is untouched.
- **[Subprocess]** Not applicable — no command is executed and no environment is passed.
- **[Cryptographic primitives]** Not applicable — the envelope is sealed by the manager's
  existing single-owner Noise send state on its drain goroutine; nothing here touches key
  material or chooses a primitive.
- **[Network & I/O]** No findings on frame size. The doc's stated hazard — an unbounded
  claude-authored name making the daemon's own outbound frame exceed the envelope cap —
  is closed by `SanitizeFilename`'s `truncateToBytes` at
  `protocol.MaxAttachmentFilenameBytes`, which is a **byte** bound, not a rune bound.
  Verified rather than assumed, and the margin is larger than the bound suggests: the
  allowlist is pure ASCII, so the sanitised name is one byte per rune and the whole
  payload is two 36-byte ids plus at most 255 bytes of name — under 1 KB against a ~64 KB
  cap. This is why the plan adds no redundant length check.
- **[Network & I/O]** No findings on queue pressure, and the reasoning is named rather
  than waved past. `pushQueue.enqueue` marks only `TypeAssistantDelta` droppable, so
  `attachment_offered` is control-class and is never evicted; a sustained burst admits
  past nominal cap up to `pushQueueByteCeiling`, then latches `overflowed` and the
  transport tears the session down, freeing the retained bytes. That is the *existing*
  policy for every control frame, `modal_shown` included, which claude can already drive
  at will through `mcp.approve`. The rate here is additionally bounded by claude having to
  make a tool call and the daemon having to read and store a whole file per announcement.
  No new exposure, and no new ceiling is warranted.
- **[Error messages, logs, telemetry]** No findings, and this is the category the design
  is most constrained by. `filename` reaches no log field on any branch — success,
  marshal failure or push failure — which is the § Attachments ban that sanitising does
  not lift. The push-error line carries `conversation_id`, `attachment_id`, `conn_id`,
  `env_id` and `err`; `err` is safe because `V2SessionManager.Push`'s entire error surface
  is `ctx.Err()` and `relay.ErrConnNotFound`, both content-free sentinels (read, not
  assumed). The marshal-error line carries no field but `event`. No refusal path gains a
  log line, preserving #2164's "refusals are not logged at all".
- **[Concurrency]** No findings. `control.Server.Serve` runs each accepted conn on its own
  goroutine, so concurrent `attachment.file` calls are reachable and `nextID` is bumped
  under `mu` — a leaf lock held around the counter and nothing else, released before
  `Push`, so it never nests inside the manager's `pushMu`. No lock is held across the
  fan-out at all: `conversationForCurrentSession` copies the slice header under the
  registry mutex and releases before returning, so the announce runs lock-free. No
  goroutine is spawned, so none can leak. On teardown `ActiveConns` returns nil and the
  loop returns early on a ctx error.
- **[Concurrency]** No findings — the benign race is named for the record: a conversation
  deleted between `Store` returning and the client's `request_attachment` arriving makes
  the fetch answer `attachment.not_found`. The announcement is advisory and retrieval
  re-validates, so the race costs a fetch, not a leak.
- **[Threat model alignment]** § Security model threat 1 (prompt injection) lands on this
  frame in the inverse direction, and the daemon's half of it is discharged: sanitise
  (`Store`), bound (`SanitizeFilename`), never log raw (above). The residual is published
  and accepted — a client **MUST** sanitise before rendering, **MUST NOT** use the name as
  a path, **MUST NOT** treat the extension as evidence of content — and it stays the
  consumer's, unchanged by this slice. One property worth stating: claude cannot announce
  an arbitrary string, only the sanitised leaf of a file that actually exists inside the
  conversation's confined workspace, since `resolved` passed `confineFile`.
- **[Threat model alignment]** No findings on the inbound direction — `attachment_offered`
  is binary→phone only and `IsKnownAppType` rejects it inbound, so a paired phone can
  neither send one nor cause one; the only producer is a local control-socket verb behind
  a 0600 socket.
- **[Threat model alignment]** SHOULD FIX (doc, in scope) — § Attachments currently says
  of `filename` that *"nothing on this path strips control characters or terminal escape
  sequences"*. That was true while the frame had no producer, and **this slice falsifies
  it**: `SanitizeFilename`'s allowlist rewrites every control character and every escape
  byte to `_`. The sentence sits inside the same paragraph as the never-log rule and one
  paragraph above *"Whether the producer sanitises before announcing is #2083's to
  decide"*, which this ticket answers, so correcting it is squarely inside the AC's
  "every live sentence naming #2083 as an unbuilt future producer" rather than an audit of
  the section. Correct it to state what the producer does, and **keep every consumer MUST
  unchanged** — a name drawn from an allowlist is still attacker-*chosen* text, so
  extension dispatch stays the hazard it was. Do not weaken the never-log rule: the
  privacy half of it never depended on control characters.
- **[Threat model alignment]** Out of scope, deferred, no ticket filed — the offer is
  **live-only**: no outstanding-offer registry, no list verb, no connect-time replay, so a
  client disconnected at announce time never learns that id. Named by the ticket as out of
  scope and documented as a property in § Attachments. It is an availability/UX gap, not a
  security one, and the absence of a registry is the safer posture (no growing daemon-side
  store of claude-authored strings). Whether an offer is also recorded in the
  conversation-history log is #2082's open question and is a human call, not this slice's.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-07
