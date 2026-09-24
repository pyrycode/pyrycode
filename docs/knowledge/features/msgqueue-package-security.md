# Security — `internal/msgqueue`

Part of [`internal/msgqueue`](msgqueue-package.md). The queue buffers untrusted, phone-originated `send_message`
text and releases it into the live claude session; ordering, loss-prevention, drain-pacing, and bounding
are inbound message-dispatch **policy** on an internet-exposed surface (`#704` is
`security-sensitive`). The engine's stance:

- **`text` is opaque transit.** Stored, never inspected, parsed, or used in a
  control decision; converted to `[]byte` only at the `deliver` call; **never
  logged** (see § Error handling on the parent page).
- **`delivery` (#2038) carries the same opaque-transit, never-logged discipline
  as `text`, and is structurally incapable of reaching a client.**
  `EnqueueDelivery` lets a caller enqueue a payload distinct from what a client
  reads back — `internal/relay/handlers.SendMessage` uses it to hand claude a
  composed prompt naming a stored attachment's on-host path while `text` stays
  the user's own words, which `docs/protocol-mobile.md` § Error codes forbids
  putting on the wire. `QueuedMessage` gains no `delivery` field, so
  `Snapshot`/`SnapshotAll` — and so `queue_state`, on both the enqueue push and
  the connect-time reconcile — are structurally incapable of projecting one; a
  future consumer that wants the path on the wire has to widen the exported
  type to get it, rather than merely forgetting a filter. `Enqueue` is
  `EnqueueDelivery(convID, text, text)`, so none of its ~108 existing call
  sites needed touching.
- **`messageID` (#2092) is untrusted like `text`, but travels the opposite
  direction on purpose.** It is the client's own id for the `send_message` that
  produced this record, stored verbatim and projected by both `Snapshot` and
  `SnapshotAll` so `queue_state` can carry it back out — deliberately echoed to
  every paired device, as the key a client merges its own optimistic echo on.
  Nothing in this package or its consumers reads it to route, authorize, match
  or dedupe; a colliding id across two devices is a client-local
  merge-attribution question (`docs/protocol-mobile.md` § Queue), not a daemon
  trust decision. This is the same shape #2038's `attachment_id` warned about: a
  doc comment that enumerated `Text` as *the* untrusted field went stale the
  moment a second untrusted field landed beside it, so `QueuedMessage`'s and
  `QueuedItem`'s comments now name both.
- **`attachmentIDs` (#2596) is untrusted like `messageID`, and the security
  distinction is provenance, not shape.** They arrive already past
  `resolveAttachments`' canonical-shape check (`internal/relay/handlers`),
  deduplicated on first occurrence, so every id stored here names an
  attachment that existed in the message's own conversation. The queue does
  not re-validate them — it stores and, on delivery, hands them back to the
  same trust domain that authored them (`internal/history`'s producer, and
  from there any paired device), exactly as `messageID` does. That return
  path is **not** licence to project `delivery`: `attachmentIDs` is a
  separate argument to `EnqueueAttached`, copied onto the record independently
  of `delivery`, and the on-host path stays reachable only inside the opaque
  `delivery` payload above.
- **`convID` is a map key only.** Validating/resolving it to a real session is the
  **caller's** job, upstream of `Enqueue` (the `SessionRouter` / `ValidateConversation`
  in `send_message.go`). A hostile `convID` can at worst create an isolated FIFO that
  never drains — **never reach another conversation's session** (per-conversation maps
  + per-conversation drains are isolated by construction). No type-system signal is
  added, matching the `WriteUserTurn(ctx, id, payload)` convention where `id` is
  pre-validated by the caller.
- **No tokens/secrets/crypto/file/subprocess surface.** The `id` is a non-secret
  per-conversation counter, not a capability.
- **Inbound bound / backpressure — implemented (#869).** A phone flooding
  `send_message` while claude is persistently busy/wedged used to grow
  `convs[convID].items` without bound (an in-memory DoS; each queued message up
  to the transport's 1 MiB frame ceiling). #869 closes it at the single insertion
  point: `Config.MaxQueuedPerConversation` (`<= 0` ⇒ `defaultMaxQueuedPerConversation`,
  100) caps each conversation's not-yet-delivered backlog (`len(c.items)`,
  including the in-flight head). Past the cap, `Enqueue` **rejects** — returns `0`
  (never a valid id) without appending, consuming no id, notifying nothing,
  spawning no drain — **reject, never drop**, preserving losslessness (the mirror
  of ADR 025's "control never drops": inbound content gets "tell the sender," not
  silent-drop or drop-oldest). The `uint64` return arity is unchanged — `0` rides
  an already-impossible id value, so none of the ~45 pre-existing `Enqueue` call
  sites needed touching. The `send_message` handler maps `id == 0` to a retryable
  `protocol.CodeServerBinaryBusy` ("server.binary_busy") envelope and does not
  ack, so the phone re-issues once the backlog drains. Per-conversation only — N
  conversations can still hold up to N×cap; a daemon-wide ceiling is out of scope
  (matches `eventring`'s per-conversation `MaxEventsPerConversation` posture). See
  [codebase/869.md](../codebase/869.md).
- **`Snapshot` / `Remove` boundary crossings (#719) — convID trust is the
  consumer's job.** Both key by **caller-supplied `convID`**, and `Snapshot`
  *returns* the opaque `text` and (#2092) `messageID` (both will flow out to a
  phone via `queue_state`). Per-conversation maps give isolation-by-construction
  **once `convID` is trusted**; trusting it is #705's job (bind `convID` to the
  requesting phone's authorized conversation, exactly as `send_message` does).
  Cross-conversation **confidentiality** (`Snapshot` reading another conv's
  queued text) and **integrity** (`Remove` mutating another conv's FIFO) are
  prevented only there. The engine adds **zero** new log lines — `Snapshot`
  returns `text` and `messageID` as values, never logs either; the consumer must
  preserve the "never logged" discipline across the new exit for both. `Remove`
  never touches `nextID`, so the stable-id contract is preserved and a removed id
  is simply never reused. ADR 025 § Security model lists viewing/dequeuing as a
  paired phone's **ungated** capability (only answering permission-class modals
  is gated), so no permission gate is needed.
- **`SnapshotAll` boundary crossing (#878) — same posture as `Snapshot`, no new
  input trust decision.** `SnapshotAll` takes **no** caller-supplied parameter —
  it enumerates the engine's own `convs` keys, so there is no `convID` to trust or
  mistrust at this call. It **returns** the same opaque `text` and (#2092)
  `messageID` `Snapshot` does, fanned out to `internal/relay`'s connect-time
  reconcile ([#878](../codebase/878.md), `security-sensitive`) and from there to
  a possibly-untrusted v2 peer — the reconcile's own gates (Noise_IK auth +
  `interactive` capability + unicast addressing) are the trust boundary, not
  this engine. Zero new log lines; the never-logged discipline is unchanged for
  both fields.
