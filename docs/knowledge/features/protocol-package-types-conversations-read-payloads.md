# Conversations-read payloads (#273)

Bodies of the conversation-listing request/response pair (`docs/protocol-mobile.md` § Message types → `list_conversations` / `conversations`). `list_conversations` is phone → binary; `conversations` is the binary's reply with `in_reply_to` set to the request's id. `ConversationSummary` is the row type, exported because it is the element type of `ConversationsPayload.Conversations`.

```go
type ListConversationsPayload struct{}

type ConversationsPayload struct {
    Conversations []ConversationSummary `json:"conversations"`
}

type ConversationSummary struct {
    ID             string    `json:"id"`
    Name           *string   `json:"name"` // *string + no omitempty: spec wire shows literal `null`; omitempty would drop the key.
    IsPromoted     bool      `json:"is_promoted"`
    CurrentSessionID string  `json:"current_session_id"` // no omitempty (#940): stored binding, "" when unbound; no liveness guarantee.
    IsArchived     bool      `json:"is_archived"` // no omitempty (#880): a client counts active vs. archived, so it must read the flag on active rows too.
    IsMuted        bool      `json:"is_muted"` // no omitempty (#2571): mirrors IsArchived; see write-payloads doc for the direct-marshal test this field needed.
    PinnedAt       *time.Time `json:"pinned_at"` // no omitempty: timestamp or explicit null, including unpinned rows.
    ReadUpTo       uint64    `json:"read_up_to"` // no omitempty (#2779): the stored durable read mark, including 0.
    LatestEntryID  uint64    `json:"latest_entry_id"` // no omitempty: newest displayable durable entry id, 0 if none; exclusions below.
    LastShownVersion *uint64 `json:"last_shown_version,omitempty"` // declared thread watermark; nil omits, pointer to zero emits 0.
    ArchivedAt     *time.Time `json:"archived_at"` // *time.Time + no omitempty (#2698): always serialized; null for an active row and for one archived before this field existed.
    Cwd            string    `json:"cwd"`
    WorkspaceLabel *string   `json:"workspace_label"` // *string + no omitempty, same discipline as Name — but here it's a client-visible contract, not only a round-trip property (see below).
    LastMessageTS  time.Time `json:"last_message_ts"`
    LastUsedAt     time.Time `json:"last_used_at"`
    Agent          string    `json:"agent,omitempty"` // #2643; AgentClaude/AgentCodex; see below.
}

const (
    AgentClaude = "claude"
    AgentCodex  = "codex"
)
```

- **`ListConversationsPayload` is `struct{}`.** Spec shows `{}` on the wire; the type exists so the dispatcher can decode into a concrete value rather than `json.RawMessage`.
- **`ConversationSummary.Name` is `*string` WITHOUT `omitempty` — same discipline as `SessionTransitionPayload.WorkspaceCwd`.** Spec example shows literal `"name": null` on one of the two rows (an unnamed scratch conversation). `*string` distinguishes a null value (nil pointer) from an empty string, but decoding alone cannot distinguish null from an absent key; without `omitempty`, a nil pointer marshals as an explicit `null`. The AC body said "`*T` + `omitempty`"; honouring that literally would silently break the byte-equivalent round-trip the same AC requires — spec wire shape wins. The field comment records the explicit-null contract. `TestConversationsPayload_RoundTrip` checks decoded names and marshals the decoded DTO back into the envelope before comparing bytes; see the fixture completeness requirement below. The fixture carries both branches (one row `name=<string>`, one row `name=null`) so the round-trip exercises both.
- **`CurrentSessionID` is the stored binding, not a process check (#940).**
  `ListConversationsWithAgents` copies each emitted row's own
  `Conversation.CurrentSessionID` directly, including `""` when unbound; the wire
  field has no `omitempty`, even though the persisted conversation omits empty
  bindings. The first snapshot supplies an ID before any transition: creation
  emits no `session_transition`, so waiting for that push leaves initial
  session-scoped controls without an address. A nonempty ID guarantees neither
  liveness nor mutation success. `request_session_settings` remains the route for
  reading the live or persisted dormant session's settings. See the
  [protocol reference](../../protocol-mobile.md#application-message-types) and
  [list-handler contract](relay-package-handlers.md#listconversations-grows-an-agent-aware-sibling-instead-of-a-new-parameter-2643).
- **`ConversationsPayload.Conversations` order is preserved verbatim from the wire — this type does not reorder.** Doc comment notes that the binary is the source of truth for ordering (e.g. most-recently-used first); a `Sort` / `SortMRU` helper or any ordering predicate is explicitly out of scope.
- **`LastMessageTS` / `LastUsedAt` are `time.Time` (RFC3339Nano-on-the-wire envelope rule).** Spec example uses `"2026-05-08T10:31:02Z"` (no fractional seconds); `time.Time.MarshalJSON` emits RFC3339Nano which omits the fractional component when none is present, so the round-trip is byte-identical with no custom marshaller. Tests use `time.Time.Equal`, never `==`. Same discipline as `Envelope.TS`.
- **Other required fields are non-pointer, no `omitempty`.** `ID` / `Cwd` are required `string`; `IsPromoted` is required `bool` (fixture covers both `true` and `false`). Validation that `IsPromoted == true` implies `Name != nil`, ID uniqueness, ordering invariants — all dispatcher / registry concerns.
- **`IsArchived` (#880) is a durable flag, not a filter.** `ListConversations` lists unfiltered, so an archived row carries every other field — including `WorkspaceLabel` (#2208) — exactly like an active one; archiving a conversation does not un-name its workspace's folder.
- **`IsMuted` (#2571) mirrors `IsArchived`: durable, unfiltered, read from the stored conversation by `ListConversations`'s only producer, `false` by default since no write verb shipped with this ticket.** Unlike `IsArchived`, proving it stays on the wire needed a new kind of test — see the bool-zero-value note in [`protocol-package-types-conversations-write-payloads.md`](protocol-package-types-conversations-write-payloads.md), which applies here too since `TestIsMuted_AlwaysSerialized` covers both structs in one test.
- **`ReadUpTo` and `LatestEntryID` are the legacy unread pair: `latest_entry_id > read_up_to`.**
  `ReadUpTo` is `Conversation.ReadUpTo` copied verbatim — the host operator's
  durable read mark, shared by every paired client and device. `LatestEntryID`
  is resolved per row, after `multi_agent` filtering, through
  `historyLatestReader.LatestDisplayableEntryID(conv.ID)` (`legacyHistoryReader` over `*history.Store` in
  production). The daemon target is the newest visible conversational content
  in the original durable ID space. The reader shares `historyEntryShown` with
  producer metadata and vetoes explicit `Shown == false`; absent metadata uses
  the same payload-aware classification, while explicit true cannot promote
  metadata or unknown types. Unknown vocabulary defaults to non-content.
  Hidden statuses, context usage, successful turn endings, nonvisual info banners
  and restart/sleep dividers do not raise it; valid receipts provide accounting
  without establishing content visibility. Missing/empty or hidden-only logs
  yield `0`; see [visible-content clamping](../../protocol-mobile.md#marking-a-conversation-read).
  [`MarkConversationRead`](relay-package-handlers-mark-conversation-read.md#markconversationread-cannot-copy-setconversationmuteds-best-effort-save-2780)
  uses the same target for `max(held, min(up_to, latest))`. Reading the newest
  visible entry clears unread despite hidden tails; reading an older entry leaves
  newer content unread. Higher held marks never decrease, even at latest zero.
  Reopening and hidden-only restart reconciliation preserve the read/unread
  result; genuinely visible interruption/loss notices can raise it. Hidden entries
  retain their IDs and receipt accounting contract; see
  [history watermarks](history-package-watermarks.md#unread-state-uses-a-separate-lazily-recovered-watermark-2954).
  Both fields are `uint64` without `omitempty`: clients read explicit zero,
  rather than interpreting a missing key as unknown. A lookup failure on any
  surviving row fails the whole correlated reply with retryable
  `history.unavailable`; see [the list handler](relay-package-handlers-list-conversations.md#listconversations-gains-a-third-required-parameter-the-history-dependency-is-not-optional-2779).
- **`PinnedAt` uses `*time.Time` without `omitempty` on both list and update DTOs.**
  Nil must serialize as explicit `null` so an unpin can clear a client's cached
  stamp; decoding an absent key from an older host also means unpinned.
  `TestConversationPinnedAt` marshals decoded DTOs directly and inspects literal
  `pinned_at` output for timestamp, null and absent inputs on both types.
  Pins are host-local and clients merge hosts by pin time, without a new
  capability. `ListConversationsWithAgents` and every update producer copy the
  stored pin, including command-package announcements (#3537/#3538). Checking
  null serialization alone would miss a producer that forgot that assignment;
  [projection regressions](relay-package-handlers.md#pin-preserving-projections-and-regression-tests)
  seed non-null timestamps too. See the
  [request contract](../../protocol-mobile.md#set_conversation_pinned).
- **`ArchivedAt` (#2698) is the archive-time stamp, projected verbatim from `Conversation.ArchivedAt` by `ListConversationsWithAgents`.** Pointer without `omitempty`, the same discipline as `WorkspaceLabel`: the key is always serialized, and `nil` is genuinely ambiguous on the wire between "active" and "archived before this field existed" — a client resolves that ambiguity by falling back to `LastUsedAt` for ordering, not by asking the daemon to disambiguate. Re-archiving a row never invents a stamp for it (see [`conversations-registry-crud.md`](conversations-registry-crud.md) § `SetArchived`): stamping a legacy archived row on re-archive would reorder it to the top of every client's Archive screen, which is exactly the bug the registry-level test `TestRegistry_SetArchived_LegacyArchivedStaysUnstamped` exists to catch before it reaches this payload.
- **`LastShownVersion` is an authoritative optional thread watermark.**
  Adding a shown item or appending text to one raises it; taking the maximum
  current item revision would also count hidden or state-only changes and
  falsely report new shown content. Nil omits the key, while a non-nil pointer
  to zero emits numeric `0`; a plain `uint64` with `omitempty` would lose that
  known-zero state. `TestConversationLastShownVersion` marshals the decoded DTO
  back into its envelope and checks absent/zero/nonzero alongside distinct
  `read_up_to` and `latest_entry_id` values and a stored session binding.
  Relay projection uses only `ThreadLastShownVersion` for each row and each
  `conversation_updated` push/reply: usable zero is present, while missing,
  rebuilding or unavailable readings remove even supplied stale metadata.
  Non-thread connections always omit the key. Thread unread is
  `last_shown_version > read_up_to`; this changes neither read-mark storage nor
  the legacy watermark, and `CurrentSessionID` remains the stored binding.
  Projection adds no absent binding/watermark fields to `conversation_updated`.
  Production installs the fold-backed provider through `installDaemonThreadRelay`;
  usable loaded status and eligible unloaded clean certificates can supply a
  reading, including authoritative zero. Hidden appends invalidate stale unloaded
  certificates by advancing raw progress without raising shown progress. Missing,
  rebuilding or unavailable evidence stays omitted; raw consumed progress never
  substitutes for the shown watermark. Client thread catch-up/pages and reconnect
  history initialization remain #2963. See
  [relay projection](v2-session-manager-state-machine-capability-negotiation-on-the-handshake.md#thread-delivery-and-summary-projection)
  and [the summary contract](../../protocol-mobile.md#conversations).
- **`WorkspaceLabel` (#2208) is resolved per row from that row's own `Cwd`, never inherited.** It belongs to the workspace, not the conversation — N rows sharing one `Cwd` carry one label — and is filled by `internal/relay/handlers.ListConversations` via a `ConversationLister.WorkspaceLabel(cwd string) (string, bool)` read method, widened onto the narrow interface for this handler alone. Presence comes from that method's second return, never from `label != ""`: the registry keeps a stored empty label distinct from an absent one (see [`conversations-registry-crud.md`](conversations-registry-crud.md) § `WorkspaceLabel`), and deriving the pointer from the string would collapse the two into the same `null`.
- **`Agent` (#2643) is gated by `multi_agent`, independently of the thread watermark.** It names the agent running the conversation's bound session — `AgentClaude` or `AgentCodex`, a closed two-value wire vocabulary — and is `""` (so `omitempty` drops the key) unless the requesting conn negotiated `protocol.CapabilityMultiAgent`. For a negotiating client the value is never empty: `internal/relay/handlers.AgentOf` (exported for a second caller in #2644, see below) reads the session's harness through a `SessionHarnessFunc` (built over `sessions.Pool.HarnessFor`, #2629) and falls back to `AgentClaude` for a conversation with no bound session, a nil reader, or a session id the pool doesn't hold — fail-open, so a harness miss still shows the row rather than hiding it. **For a non-negotiating client the row is not merely unlabelled — a `conversations` reply omits every row whose agent is `AgentCodex` entirely**, so the list such a client sees is byte-identical to the one it saw before Codex conversations existed; that filtering, not just the empty `agent` key, is what "detection only" does not cover for this capability (see [handshake payloads](protocol-package-handshake-control-payloads.md)). `ListConversations(reg, hist)` is a thin wrapper over `ListConversationsWithAgents(reg, nil, hist)`, so a caller with no session pool gets the agent-blind reply without a `SessionHarnessFunc` to supply; the filtered history reader is required in both forms; a nil `harnessFor` makes `AgentOf` read every row as `AgentClaude`, which is exactly the pre-#2643 shape. See [`dispatch-package.md`](dispatch-package.md) for where the negotiated decision is carried per-conn (`Conn.MultiAgent()`), and [ADR 039](../decisions/039-capability-belongs-to-agent-and-model-together.md) for the general "resolve the session's own agent through `Pool.HarnessFor` first" pattern this handler follows. #2644 reuses the identical resolution rule to withhold *pushed* frames (not just this reply) about a Codex conversation from a non-negotiating conn — see [Capability negotiation on the handshake](v2-session-manager-state-machine-capability-negotiation-on-the-handshake.md).
- **Typed fixture round trips require the complete serialized DTO shape.**
  `TestConversationsPayload_RoundTrip` marshals the decoded `ConversationsPayload`
  into `Envelope.Payload` before re-encoding the envelope. Its fixture includes
  the always-present `is_muted` and `current_session_id` keys, even at false and
  empty-string values. Leaving the original `json.RawMessage` untouched had let
  both omissions pass; weakening the byte comparison would hide that mismatch
  again. Field order follows the DTO declaration because the comparison uses
  `json.Compact`, which removes whitespace without sorting keys.
  `TestConversationReadStateSerialization` separately proves zero and above-2^63
  watermarks; `TestConversationsPayload_CurrentSessionID` checks distinct bindings
  and the present empty key on an unbound row.
- **Pure DTOs: no methods, no constructors, no `Validate()`.** Identical posture to `RegisterPushTokenPayload` and the messaging slice. `internal/relay/handlers.ListConversations` maps each `Conversation` row to a `ConversationSummary` and sends a `conversations` envelope with `in_reply_to` set to the request id — registry-to-payload mapping is a downstream concern, not this package's.

Golden round-trip tests in `conversations_read_test.go` decode each spec example through `Envelope` → `Envelope.Payload` → per-type struct and re-marshal byte-equivalently against `testdata/list_conversations.json` / `testdata/conversations.json`. `TestConversationsPayload_RoundTrip` asserts all three rows: row 0 has a non-nil `Name` pointer and stamped `ArchivedAt` / `PinnedAt`; row 1 has `Name == nil` (NOT `*c1.Name == ""` — would panic on nil deref AND be the wrong check) and `ArchivedAt == nil` (active); row 2 (#2698) is archived with `ArchivedAt == nil` — archived before the stamp existed, distinguishing "active" from "legacy archived" is exactly why the fixture needed a third row. The `conversations.json` envelope rides with `in_reply_to: 3`, the first protocol fixture pinning `in_reply_to` alongside an array-carrying payload.

**Re-encode the decoded DTO, not the fixture's original raw payload.**
`Envelope.Payload` is `json.RawMessage`; merely marshalling the original envelope
cannot detect a missing field or a wrong DTO tag. The list and
[`conversation_updated`](protocol-package-types-conversations-write-payloads.md)
round trips now replace that raw payload with `json.Marshal(decodedPayload)`.
The list fixture includes both stamped and null `pinned_at` values; the update
fixtures exercise each separately. Complete required-field fixtures make these
comparisons useful, while direct key assertions distinguish null from omission.
See [protocol test boundaries](development-verification-protocol-boundaries.md).

**A nullable-and-present key (no `omitempty`) needs a raw-bytes assertion, not a decoded one.** A decoded `*string` reads `nil` identically whether the key arrived as `"workspace_label":null` or was dropped entirely by an `omitempty` that should not be there — decoded-field checks alone cannot see the regression. A typed round trip or direct DTO marshal must inspect the emitted bytes. `internal/relay/handlers`' `TestListConversations_UnlabelledWorkspaceSendsExplicitNull` instead counts literal `"workspace_label":null` occurrences in the raw reply bytes. The same trap applies to `Name` above and to a required empty string: decoded `CurrentSessionID == ""` cannot distinguish a present `"current_session_id":""` from an omitted key. Inspect the JSON produced by marshalling the decoded DTO, rather than the untouched raw envelope payload.
