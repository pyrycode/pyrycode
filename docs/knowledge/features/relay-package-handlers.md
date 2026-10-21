# `internal/relay/handlers` — per-envelope-type processors

Split out of [`relay-package.md`](relay-package.md) (parent doc — see there for the connection/transport layers this sub-package sits behind). This child covers the `handlers/` sub-package itself: its first inhabitant in depth, and the conventions later handlers in the package are expected to follow.

## Detailed handler contracts

| Topic | Coverage |
|---|---|
| [Push-token registration](relay-package-handlers-register-push-token.md) | Authentication, display safety, dedupe, persistence, logging and tests. |
| [Message delivery and auto-naming](relay-package-handlers-send-message.md) | Post-ack ordering, reset interception, attachments and sender metadata. |
| [Conversation read marks](relay-package-handlers-mark-conversation-read.md) | Durable advancement, rollback, shown-thread authority and announcement ordering. |
| [Conversation summaries](relay-package-handlers-list-conversations.md) | Registry projection, capability filtering and optional shown-thread evidence. |

## `SetConversationMuted` reuses `send_message`'s auto-naming push (#2572)

`SetConversationMuted` (`set_conversation_muted.go`) sets or clears `IsMuted` (#2571) through `Registry.SetMuted` (mirroring `ArchiveConversation`'s `SetArchived` + `Get`-read-back + eager `Save` shape), then replies `conversation_updated`. **Unlike `ArchiveConversation`, that reply is also fanned out**: the same record is pushed, uncorrelated, through the identical `ConversationAnnouncer` the [auto-naming push](relay-package-handlers-send-message.md) uses. `cmd/pyry/relay.go` hoists the closure the `send_message` wiring already built into a named local (`announceConversationHook`) and passes it to both handlers, rather than adding a second fan-out loop — mute needs every paired client to stop alerting without a re-list, which a requester-only reply (archive's posture) can't do.

**The reject-path log discipline diverges from the `ArchiveConversation` template it otherwise copies.** Archive's not-found branch logs the decoded `conversation_id` as a structured field, on the theory that it's a resolved id, not raw request bytes. This ticket's AC requires that no byte of the payload reach the log on *any* reject branch, and an unknown `conversation_id` is still client-supplied bytes — so both `SetConversationMuted` not-found branches (an outright miss, and the narrower case where `Get` misses after `SetMuted` already hit, i.e. a row deleted in between) log `conn_id` only, matching the malformed branch. Only the success and `Save`-failure branches, reached solely for an id the registry has already confirmed real, log `conversation_id`. **Copying a sibling handler's log fields is not safe by default — recheck each field against the copying ticket's own AC, not just against the template's precedent.**

Ordering is reply, then push, then return the reply's error: the push must still reach every other client even when the requester's own connection is torn down and the reply write fails.

**No interactive gate, deliberately asymmetric with `new_session`'s.** `handleNewSession` is intercepted ahead of `dispatch.Route` and gated on the negotiated `interactive` capability; `dispatch.Conn` (what `SendMessage` runs behind) carries no such flag, and plumbing one in would be new machinery for no authorization gain — pairing is the authorization boundary, and `new_session` is already published as exempt from the privileged per-device gate (#702, which since #2605 covers only minting a pairing and MCP actuation). A non-interactive client's `/clear` still resets; it simply does not see the `resetting` frames reporting it. See `docs/protocol-mobile.md`'s `send_message` row for the wire-facing statement of this asymmetry, including the one client-visible gap it produces: a client's own optimistic echo of `/clear` never finds a matching row in history, since the daemon never delivers the message.

## Conversation pins (#3537)

`SetConversationPinned` handles the authenticated daemon request
[`set_conversation_pinned`](../../protocol-mobile.md#set_conversation_pinned).
`startRelayV2` registers it beside mute (#3538) with the same registry, resolved
registry path and `announceConversationHook`; no new capability is needed.
`ConversationPinner` exposes registry metadata and persistence only; pinning
performs no session operation.

`Registry.SetPinned` checks eligibility and mutates under one lock. Pinning
requires a promoted conversation, including an archived channel. A new pin
stores the host's current UTC time; repeated pinning preserves that timestamp.
Unpinning clears `PinnedAt` on any existing row, including unpromoted, archived
and already-unpinned rows. Only the pin changes; re-pinning after an unpin
records a new time.

The handler finishes decoding and checks the required pointer bool before any
mutation. Malformed JSON/types or missing/null `pinned` return non-retryable
`protocol.malformed`; an empty/unknown ID returns `conversation.not_found`.
Pinning an unpromoted row returns `conversation.not_promoted` with the fixed
message `conversation not promoted`. Refusals neither save nor announce, and
rejection logs/replies contain no supplied IDs, payload bytes or decoder error
text. A decode error may leave a valid target ID populated: that partial value
must never reach the mutation, as `TestSetConversationPinned_Rejects` proves.

After mutation, `Get` supplies one stored snapshot. A concurrent deletion that
makes it miss returns not-found before saving or announcing, matching mute.
Every success, including a no-op, eagerly attempts `Save`. A successful save
survives a fresh `Load`; a failed save logs the error and retains the in-memory
change, still replying and announcing. The correlated `conversation_updated`
reply is attempted before `ConversationAnnouncer` receives the same record;
the announcement still runs if the reply fails. `pinned_at` is the stored
timestamp or explicit JSON null; other projected fields come from that snapshot.

**Calling Reply before announcing is insufficient to prove wire order.** Buffered
replies and manager pushes previously took independent paths, allowing a push to
overtake its reply. The shared [Run-processing fence](v2-session-manager-concurrency-hosted-discovery-and-ordered-delivery.md)
now preserves that order for the connected requester. The announcer sends an
uncorrelated push to every interactive client, including the requester.
`TestRelayV2_ChannelPinsReplyOrdering` checks authenticated receive order and
rejects an early matching push. Its functional sibling `TestRelayV2_ChannelPins`
buffers early pushes, so it proves receipt, timestamp/null, idempotence, reload,
restart and re-pin behavior but cannot independently prove ordering. Both tests
must execute without skips to establish the full contract.

### Pin-preserving projections and regression tests

`ListConversationsWithAgents`, `RenameConversation`, `autoNameConversation`,
`PromoteConversation`, `ArchiveConversation` in both directions,
`ChangeWorkspace`, `SetSystemPrompt`, `SetConversationMuted` and
`MarkConversationRead` all copy stored `PinnedAt`. List visibility, capability
filtering and ordering keep their existing semantics.
`channelCreator`, `conversationCreator` and `announceSwitchedConversation`
likewise project the stored snapshot, including on unrelated metadata changes.

**Valid serialization alone cannot prove pin preservation.** Omitting the
stored value from a payload builder still emits valid `"pinned_at":null`, which
silently clears the client's pin during an unrelated edit.
`TestConversationPinProjections` seeds a non-null pin on all ten paths (counting
archive/unarchive separately), checks its exact timestamp, and separately
requires an explicit null on unpinned records. See the
[protocol boundary testing guidance](development-verification-protocol-boundaries.md).
`TestCommandConversationPins` applies the same timestamp/null checks to all three
command producers. Creation normally starts unpinned, so its fixtures pin the
stored row at the creation-success logging boundary before announcement; testing
only the default state would leave a missing stored-value assignment undetected.

## Daemon-wide instructions and durable acknowledgement

`SetHostSystemPrompt` must not inherit `SetSystemPrompt`'s best-effort `Save`
success behavior: its acknowledgement promises durability. The consumer-declared
`HostSystemPromptStore` exposes only the pool's instructions getters and
`SetDaemonInstructions`, so validation, persistence and publication stay behind
one door and the handler cannot actuate sessions. `runSupervisor` passes the
existing conversation pool through `relayWiring.hostSystemPrompt`; wiring a
separate store would let relay tests pass while conversation starts consumed
stale instructions. `TestHostSystemPromptRelayUsesConversationPool` checks
active child/file stability, pre-minted activation, later starts and retention
of memory/disk after a real persistence failure. See
[pool composition](sessions-package-key-types-writesystemprompt-systemprompttext.md#durable-daemon-wide-instructions-2766).

Translate wrapped pool validation sentinels with `errors.Is` into non-retryable
`protocol.malformed`; other setter failures return retryable
`host_system_prompt.unavailable` and no success reply. Never pass parser or store
error text into a log or reply: either can contain private instructions.
`TestHostSystemPromptHandlers` injects secret-bearing wrapped errors to check
that boundary. Both host handlers use authenticated map dispatch without an
interactive gate or session lookup, and `dispatch.Conn.Reply` supplies
requester-only correlation. See the
[wire contract](../../protocol-mobile.md#daemon-wide-host-system-prompt).

## `ListConversations` grows an agent-aware sibling instead of a new parameter (#2643)

`ListConversations(reg ConversationLister) dispatch.Handler` had 13 call sites — over the pipeline's per-constructor limit for a signature change — so adding the agent field this ticket needed (`ConversationSummary.Agent`, #2643, gated on `Conn.MultiAgent()`) could not widen it in place. The fix is a second, wider constructor: `ListConversationsWithAgents(reg ConversationLister, harnessFor SessionHarnessFunc) dispatch.Handler` does the real work, and `ListConversations(reg)` becomes a one-line wrapper — `return ListConversationsWithAgents(reg, nil)`. `AgentOf` treats a `nil` `harnessFor` the same as a harness-miss: every row reads `AgentClaude`, which is exactly the pre-#2643 reply. None of the 13 existing call sites (12 of them tests) needed to change; the daemon's own wiring in `cmd/pyry/relay.go` is the only caller of the new constructor.

`agentOf` was exported to `AgentOf` for #2644, which reuses this exact resolution rule as the push-side capability gate (`internal/relay/v2session_agentgate.go`) rather than re-implementing it — see [Capability negotiation on the handshake](v2-session-manager-state-machine-capability-negotiation-on-the-handshake.md).

**This is the same shape [`SendMessage`'s repeated widenings](relay-package-handlers-send-message.md) avoid by not reusing a full signature change, generalised one step further: when the call-site count itself is the obstacle, add a new constructor that the old one delegates to, rather than touching the old one's signature at all.** The nil/no-op default (`nil` seam, `nil` registry method, or here a `nil` `SessionHarnessFunc`) is what makes the delegation exact rather than approximate — `ListConversations(reg)` is not a simplified or legacy form of the new handler, it is the literal value the new handler produces when there is no session pool to ask.

`ConversationSummary.CurrentSessionID` reads the registry binding directly,
independently of `AgentOf`'s pool lookup: a missing harness cannot erase a stored
ID, and discovering an ID must not require a transition or a running child.
It is always sent as a string, including `""` when unbound, on every emitted row;
a nonempty value establishes neither liveness nor mutation success.
`TestListConversations_CurrentSessionIDOnFirstList` uses two distinct bindings
and an unbound row without a pool or transition, then checks both reply bytes
and decoded-DTO serialization. A single bound row would miss accidental reuse
of another row's ID, while decoded empty-string checks alone would miss
`omitempty`. See [conversations-read payloads](protocol-package-types-conversations-read-payloads.md)
and the [wire contract](../../protocol-mobile.md#application-message-types).
