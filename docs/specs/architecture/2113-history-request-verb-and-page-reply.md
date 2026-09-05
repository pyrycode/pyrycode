# #2113 — declare the conversation-history request verb and its page reply

Wire vocabulary and publication only. Two Type\* constants, three payload types,
four decode fixtures, registry filing in both drift detectors, and the published
section in `docs/protocol-mobile.md`. **No handler, no producer, no validator** —
those are #2116's.

## Files read

- `internal/history/log.go` → `Entry`, `Page`, `Store.Page`, `MaxPageEntries`,
  `ErrInvalidPageSize`, `ErrInvalidID` — the shapes this ticket mirrors onto the
  wire, and the page-size rule (`limit < 1` errors, `limit > MaxPageEntries`
  **clamps**). `Page`'s doc comment carries the termination rule verbatim.
- `internal/history/cursor.go` → `mintCursor`, `parseCursor`, `cursorRefusal` —
  cursor opacity, and that every refusal is one merged sentinel that never echoes
  the cursor back. `mintCursor`'s comment cites this ticket as why it is opaque.
- `internal/protocol/attachments.go` → `RequestAttachmentPayload` — the closest
  analogue in shape and in doc posture: the decode-failure-is-a-rejected-frame
  rule, the no-request-id-key decision, the lookup-key-not-authorization rule for
  a client-named `conversation_id`.
- `internal/protocol/codes.go` → `TypeRequestAttachment`, `TypeAttachmentStored` —
  the two constant blocks to mirror: an inbound request verb pending its handler,
  and an outbound `in_reply_to`-correlated reply. Both state the
  must-not-go-in-`inboundAppTypeSet` rule this ticket also obeys.
- `internal/protocol/settings.go` → `RequestSessionSettingsPayload`,
  `SessionSettingsPayload` — the request/reply pair precedent on this wire. The
  request names a conversation; **the reply does not echo it**. Settles design
  decision D3 below.
- `internal/protocol/questions.go` → `QuestionShownPayload.MarshalJSON` — the
  nil→`[]` normalisation pattern, value receiver plus type alias to stop the
  recursion.
- `internal/protocol/envelope.go` → `inboundAppTypeSet` — the v1-only set neither
  new type joins.
- `internal/protocol/compat_test.go` → `v2OnlyTypes`, `TestIsKnownAppType`,
  `TestTypeConstants_V1V2Partition`, `TestInboundAppTypeSet_CoversAllExportedTypeConstants` —
  the three edit points per type, and the count assertion (23) that must **not**
  move.
- `internal/protocol/interactive_test.go` → `roundTripEnvelope`, `canonical` — the
  fixture round-trip helper. It canonicalises before comparing, so fixture key
  order and whitespace are free.
- `internal/protocol/envelope_test.go` → `readFixture` — fixture loader.
- `internal/protocol/attachments_test.go` → `TestRequestAttachmentPayload_RoundTrip`,
  `TestRequestAttachmentPayload_WireKeys` — the test shapes to copy, including the
  two-sided key-set pin and the request↔answer fixture tie.
- `cmd/pyry/relay_guard_test.go` → `inboundTypes`, `excludedTypes` — Assertion #3
  fails an unclassified constant, so both types need an entry from the moment they
  exist.
- `docs/protocol-mobile.md` → § Application message types, § Attachments
  (`request_attachment`), § Reconnect / Backfill semantics, § Changelog — the row
  format, the published-section shape, and the paragraph this ticket makes stale.
- `docs/knowledge/features/history-package.md` → § "The cursor does no arithmetic
  on the number it doesn't trust" — why the cursor is opaque and why its refusals
  are merged; both are published here rather than re-derived.

## Context

A client opening an existing conversation sees only what arrived after it
connected. `replayMissed` is catch-up across a dropped connection over the
1024-entry in-memory ring, empty after a restart, and § Reconnect / Backfill says
in as many words that there is no verb to ask for more. #2112 landed the log
(`internal/history`); this ticket puts its shapes on the wire so
pyrycode-desktop#1088 and pyrycode-mobile#623 can be written against a published
contract before #2116's handler exists.

No ADR is warranted. Every decision below either mirrors a landed shape in
`internal/history` or follows an existing precedent on this wire; nothing here
chooses between architectures.

### Size: over the line ceiling, deliberately, and the floor is why

Estimated total written work **≈ 950 lines** against the 800 ceiling — about 19%
over, and below the ticket's own ~1200 estimate because comment density is
trimmed toward `SessionSettingsPayload`'s rather than `RequestAttachmentPayload`'s.
Every other line of the table is well inside its bound: **2** production source
files (`codes.go`, a new `history.go`; `.md` files do not count), **3** new
exported types, **0** consumer call sites, **4** acceptance criteria, **0** reject
branches (nothing is wired).

The floor rule decides it. The only available cut is request-from-reply, and
neither half alone unblocks either client ticket — both are parked on the
published section, not on the verb. The section would then be written twice, the
second pass rewriting the first's framing, and each half's fixtures would describe
one end of a walk whose other end does not exist. Split depth is already 1
(parent #2091), so a split is permitted by the depth gate and is declined on the
floor rather than blocked. The overage is prose.

## Design

### Decisions the ticket leaves open

**D1 — the request carries an optional page size; absent and zero both mean "the
daemon chooses".** `Store.Page` refuses `limit < 1` and clamps above
`history.MaxPageEntries`, so an absent key must not reach it as a literal zero. A
JSON key cannot be made mandatory, so the only shape with no trap is one where the
zero value is *meaningful*: `limit` 0 (absent, or explicitly zero) asks the daemon
to pick, a positive value is honoured up to the clamp, and a negative value is a
reject condition. No `omitempty`, so an encoder always emits the key and both
routes to zero agree. Published alongside it: a client reads the reply's actual
entry count and never assumes its ask was honoured, because the clamp narrows a
large ask silently rather than refusing it.

**D2 — the request names a `conversation_id`.** `Store.Page`'s precondition is a
bound conversation, and this wire is multi-conversation: `request_session_settings`
and `request_attachment` both name one. The scope posture is #2052's, published
with the field — a lookup key validated against the daemon's registry before any
path join, never a value trusted as sent, and naming a conversation is not
authorization.

**D3 — the reply does **not** echo `conversation_id`.** Correlation rides
`Envelope.InReplyTo`, and `SessionSettingsPayload` is the reply precedent on this
wire: it answers a request that named a conversation and carries none itself. The
consequence is stated in the published section rather than left implicit — a
client keeps its outstanding asks keyed by envelope id. The gain is bounded and
must be stated as bounded: **nothing in a page is echoed back from the request**,
which is *not* the same as "every field is daemon-authored". See D4.

**D4 — a page's fields are in two trust classes, and the split is published.**
The page's own frame — `cursor`, `at_start`, and each entry's `id` and `ts` — is
daemon-authored. `HistoryEntry.Type` and `HistoryEntry.Payload` are **replayed
content**: the bytes of a frame that was appended to the log, which for a stored
`send_message` are operator-authored and for a stored assistant frame are
**claude-authored**. Each entry therefore carries **exactly the trust class of the
live frame it mirrors**, and a client applies the same sanitisation it applies on
the live lane. § Security model's **threat 1 lands on this frame**, which is the
opposite of what `request_attachment` publishes for itself — the sentence is
written here rather than borrowed, because borrowing it would license a client to
render replayed claude output unsanitised. `Type` is a stored string that nothing
re-validates against the `Type*` vocabulary, so a client must tolerate an entry
type it does not know, per this document's existing unknown-field rule.

### Types

Two constants in `internal/protocol/codes.go`, each in its own block with the
doc-comment shape `TypeRequestAttachment` and `TypeAttachmentStored` use:

- `TypeRequestHistory = "request_history"` — phone → binary, inbound v2 control.
  Follows the three existing "ask the daemon for X" verbs rather than inventing a
  fourth idiom.
- `TypeHistoryPage = "history_page"` — binary → phone, reply correlated by
  `in_reply_to`. Named for what the frame *is* to a client, the rule
  `TypeQuestionShown`'s block records.

Three payload types in a new `internal/protocol/history.go`:

- `RequestHistoryPayload{ConversationID, Cursor string; Limit int}` — wire keys
  `conversation_id`, `cursor`, `limit`. No `omitempty` on any of the three.
- `HistoryEntry{ID uint64; Type string; Payload json.RawMessage; TS time.Time}` —
  wire keys `id`, `type`, `payload`, `ts`, mirroring `history.Entry` key for key.
  `ID` is the durable per-conversation entry id, **not** an `event_id`: the ring's
  ids are per-process and do not survive a restart, this one is on disk.
- `HistoryPagePayload{Entries []HistoryEntry; Cursor string; AtStart bool}` — wire
  keys `entries`, `cursor`, `at_start`, mirroring `history.Page`. Carries a
  `MarshalJSON` on a value receiver normalising nil `Entries` to `[]`, the
  `QuestionShownPayload.MarshalJSON` pattern (type alias to stop the recursion).

Nothing in this package imports `internal/history`; the mirror is by shape, and
the doc cites `history.MaxPageEntries` by symbol rather than duplicating 4096 as
a second constant.

### Published contract

The rules the section fixes, all of them properties of the landed log rather than
inventions here:

- **Newest-first, walking backwards.** A client opens at the newest entries and
  asks for older ones; it never reads forward.
- **A walk terminates on `at_start`, never on an empty `entries`.** A page filling
  exactly at the log's first entry reports `at_start` false with a usable cursor;
  the call after it returns no entries with `at_start` true. `cursor` is empty
  whenever `at_start` is set, so the two are never both meaningful.
- **The cursor is opaque, and opacity is a convention rather than a security
  primitive.** Daemon-minted text; a client echoes back exactly what it was handed
  and parses nothing. An absent or empty cursor means "start at the newest".
  `parseCursor` is the only validator anywhere, and it runs **before the lock and
  before any path is built**, so a forged cursor costs nothing and touches
  nothing. Published explicitly, because `mintCursor`'s own comment says it and a
  reader can otherwise mistake the base64 for one: a cursor is **not a secret and
  not a capability**. It is trivially reversible and it contains the conversation
  id a client already knows; it is bound to that conversation by `parseCursor`,
  and it carries no authorization — authorization is pairing. It is
  correspondingly **not signed and needs no MAC**: adding one would imply a
  capability it does not have.
- **Never allocate from `limit`.** It is an untrusted claim, the one count in this
  vocabulary, and `AttachmentChunkPayload`'s NEVER ALLOCATE FROM A CLAIM rule
  applies to it verbatim. A receiver hands it to `Store.Page` and lets the clamp
  decide; it never sizes a buffer from it.
- **The clamp bounds entries, not bytes, and a short page is not an end-of-log
  signal.** `history.MaxPageEntries` caps the entry *count*; nothing bounds an
  entry's stored payload, so a full 4096-entry page can exceed the v2
  application-envelope cap and be undeliverable. The daemon may therefore return
  **fewer entries than asked for** to fit that cap. This is exactly why the
  termination rule is `at_start` and not an entry count: byte-driven truncation is
  safe only for a client that never infers "short page ⇒ start of log". The
  byte-budgeting itself is #2116's, which emits the frame.
- **A page rides `in_reply_to`**, naming the request's envelope id.
- **Both client-supplied strings are loggable only after their shape is
  validated** — `conversation_id` and `cursor` alike, the log-injection rule this
  document already applies to `filename`. No refusal echoes the cursor back, which
  is `cursorRefusal`'s existing posture rather than a new rule.
- **Reject *conditions* are published; their codes are #2116's.** A conversation
  id of non-canonical shape or absent from the registry; a negative `limit` (zero
  is not a reject); a cursor that does not decode, was minted for another
  conversation, or names a segment not in this log — deliberately **one merged
  refusal**, because `cursorRefusal` merges exactly those distinctions on purpose
  and no refusal echoes the cursor back; and a payload that does not decode.
- **A payload that fails to decode is a rejected frame**, never a tolerated zero
  value — `RequestAttachmentPayload`'s posture, and load-bearing for the same
  reason: the empty conversation id names nothing, and a receiver that resolves it
  addresses the log root.

### Registries

Per type, exactly the edit points the ticket enumerates:

| Registry | `TypeRequestHistory` | `TypeHistoryPage` |
|---|---|---|
| `v2OnlyTypes` (`compat_test.go`) | yes | yes |
| `all` slice in `TestTypeConstants_V1V2Partition` | yes | yes |
| `IsKnownAppType` rejects table | yes | yes |
| `excludedTypes` (`relay_guard_test.go`) | `pending handler (#2116)` | `reply` |
| `inboundAppTypeSet` | **no** | **no** |

`TestInboundAppTypeSet_CoversAllExportedTypeConstants`' asserted count stays at
23. `inboundTypes` gets neither entry — Assertion #1 requires a dispatch case, and
this ticket ships none; the request moves there when #2116's `dispatchAppFrame`
case lands, which Assertion #2 makes mandatory rather than optional.

## Concurrency model

None. Wire vocabulary only — no goroutines, no channels, no shared state, no
shutdown path. The package declares shapes and runs nothing.

## Error handling

No error paths ship. `internal/protocol` declares and validates nothing; every
reject named above is #2116's to implement and to map onto a wire code. The one
error-adjacent behaviour in the diff is `HistoryPagePayload.MarshalJSON`, which
returns `json.Marshal`'s error unchanged.

## Testing strategy

Four fixtures under `internal/protocol/testdata/`, and the three page shapes are
three separate files so no shape is left to inference:

- `request_history.json` — the first ask: envelope id 140, **no** `in_reply_to`,
  empty `cursor`, `limit` 50.
- `history_page.json` — non-terminal, `in_reply_to` 140, two entries newest-first,
  a non-empty `cursor`, `at_start` false. Ties to the request fixture by envelope
  id, so the two files describe one walk.
- `history_page_at_start_entries.json` — terminal **carrying** entries: `at_start`
  true, `cursor` empty, one entry.
- `history_page_at_start.json` — terminal empty: `at_start` true, `cursor` empty,
  `"entries":[]` written literally.

Tests in a new `internal/protocol/history_test.go`, as bullet-pointed scenarios:

- Round-trip each of the four fixtures through `Envelope` + payload +
  `roundTripEnvelope`, asserting the type constant, the `in_reply_to` shape
  (nil on the request, 140 on the non-terminal page), and each payload field.
- The newest-first ordering is asserted on `history_page.json` as strictly
  descending entry ids, so a fixture regenerated in the wrong order reddens.
- The empty-list form is pinned **twice, independently**: once by
  `history_page_at_start.json` round-tripping byte-for-byte, and once by
  marshalling a `HistoryPagePayload` zero value (nil `Entries`) and asserting the
  raw bytes under key `entries` equal exactly `[]`. The second assertion reads the
  outer key directly rather than reaching an inner one — reaching through an entry
  would force the array non-empty and go vacuous.
- Two-sided wire-key pins on all three types (every expected key present **and**
  no unexpected key), the `TestRequestAttachmentPayload_WireKeys` shape. On the
  request this is the machine-checked form of "no request-id key"; on the page it
  is the form of "no echoed `conversation_id`".
- A zero-value key-presence pin on each payload, so an `omitempty` added later for
  tidiness reddens instead of silently changing the wire.

Gate: `go test -race ./internal/protocol/... ./cmd/pyry/...`, `go vet ./...`,
`go build ./cmd/pyry`. The whole-module race suite is the verifier's.

## Open questions

1. Does `docs/protocol-mobile.md` § Reconnect / Backfill semantics' sentence
   *"has no verb to ask for them and receives none"* need amending, or only its
   forward reference to #2091? **Leaning: amend precisely.** The verb now exists,
   so the first clause becomes false while "receives none" stays true until #2116.
   Mode A's framing — the ring carries no history — is untouched and must stay
   untouched; the amendment is to that one paragraph and must not re-anchor the
   sentences around it. Resolve while editing, and record the outcome under
   `## Revisions` if the edit lands wider than one paragraph.
2. Where does the new `###` section sit? **Leaning: after § Session settings (v2),
   immediately before § Reconnect / Backfill semantics**, so the paragraph amended
   in (1) can link back to a section the reader has just passed.

## Security review

**Verdict:** PASS (on the second pass; the first FAILed with four MUST FIX items,
all addressed in the revision recorded below)

**Findings:**

- [Trust boundaries] **MUST FIX — addressed in D4.** The first draft's D3 claimed
  *"every field of a page is daemon-authored"*. That is false of
  `HistoryEntry.Payload` and `HistoryEntry.Type`, which replay the bytes of a
  stored frame — operator-authored for a `send_message`, **claude-authored** for a
  stored assistant frame. Shipping that sentence would have licensed a client to
  render replayed claude output unsanitised. The claim is now scoped to
  "nothing is echoed back from the request", and D4 publishes the two trust
  classes with the rule that each entry carries the trust class of the live frame
  it mirrors. The generalisation was inherited from `RequestAttachmentPayload`,
  where it is true because that frame carries no content at all — a borrowed
  blanket trust sentence being false of the borrower's own echo fields.
- [Trust boundaries] SHOULD FIX — the plan says the doc posture is #2052's but did
  not commit to putting the rule where #2116's author reads it. In Phase B,
  `RequestHistoryPayload`'s own doc comment must carry **every field is an
  unverified claim** and the decode-failure-is-a-rejected-frame rule; the empty
  conversation id names nothing and must not reach a path join, where it resolves
  to the log root rather than erroring.
- [Tokens, secrets, credentials] **MUST FIX — addressed.** Publishing the cursor
  as *"opaque"* with nothing further invites a client to treat it as a bearer
  token — log it, persist it as a capability, or gate on it. It is base64url of
  `(version, conversation id, segment, offset)`, trivially reversible, and carries
  the conversation id the client already knows. The section now states it is not a
  secret and not a capability, that opacity is a convention (`mintCursor`'s own
  words), and that it is deliberately unsigned because a MAC would imply an
  authorization it does not carry. Authorization is pairing.
- [File operations] No findings — no file operation ships in this diff. The
  traversal boundary is downstream and named: `Store.Page` validates via
  `conversations.ValidID` and resolves through `resolveDir`, and #2116 owns the
  shape check before the id becomes a path component. `parseCursor` refuses
  structurally before the lock and before any path is built, so a forged cursor
  reaches no filesystem call — published as a property rather than left implicit.
- [Subprocess] No findings — not applicable by shape. `internal/protocol` executes
  nothing; the package declares wire types and runs no code paths at all.
- [Cryptographic primitives] No findings — no primitive is introduced. The one
  crypto-adjacent decision is the deliberate absence of a cursor MAC, recorded
  with its reasoning so a later contributor does not "harden" the cursor into a
  capability.
- [Network & I/O] **MUST FIX ×2 — both addressed.** (a) `limit` is the one count
  in this vocabulary and the first draft published no allocation rule for it, so
  #2116's author reading this type could have written `make([]HistoryEntry, limit)`
  against an attacker-chosen int64. NEVER ALLOCATE FROM A CLAIM is now published
  verbatim for it: hand it to `Store.Page` and let the clamp decide. (b) The
  `history.MaxPageEntries` clamp bounds the entry *count*, not bytes, and nothing
  bounds a stored entry's payload — so a faithful 4096-entry page can exceed the
  65519-byte v2 application-envelope cap and be dropped as the daemon's *own*
  outbound frame. The section now publishes that the daemon may return fewer
  entries than asked, and ties it to the termination rule: byte-driven truncation
  is safe only because a walk terminates on `at_start` and never on a short page.
- [Errors, logs, telemetry] SHOULD FIX — published rather than enforced here.
  `conversation_id` and `cursor` are client-supplied strings and are loggable only
  after their shape is validated, the log-injection rule this document already
  applies to `filename`. No refusal echoes the cursor, which is `cursorRefusal`'s
  existing posture. Enforcement is #2116's.
- [Concurrency] OUT OF SCOPE — nothing concurrent ships. Two properties #2116
  inherits and this ticket does not decide: `Store.Page` holds the per-store mutex
  across its segment reads, so a large page serialises that conversation's log;
  and any bound on **concurrent or repeated history requests** from one paired
  client is receiver-configured and unpublished, learned by being rejected — the
  posture § Attachments already takes for concurrent retrievals.
- [Threat model alignment] **Threat 1 lands on this frame** and is stated as
  landing, rather than borrowing `request_attachment`'s "does not land here"
  sentence — a page replays claude-authored content to a client surface. Threats
  2+ (pairing, transport) are unchanged: this verb adds no authorization path,
  sending it is not a capability, and receiving an answer is not one either.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-05
