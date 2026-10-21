# Thread delivery and summary projection

`ThreadReady` is a daemon-supplied attestation that both bounded query providers,
live publication, authoritative watermark lookup and session-state
reconciliation providers are installed. Nil/false disables `thread`;
advertisement alone is insufficient.
`handleNoiseInit` records `s.thread` only on authenticated admission, and rekey
preserves it. `ActiveConn.Thread` exposes the decision for open connections.
Production `startRelayV2` installs `ThreadCatchUp`, `ThreadPage` and authoritative
`ThreadLastShownVersion` through `installDaemonThreadRelay`, after the retained
`ThreadLiveState` and `LiveStateCurrent` providers. The queries forward directly
to `Store.CatchUp`/`Store.HistoryPage` on the real publisher store with the request
context and named conversation. Readiness stays false without any provider or
a running publisher on that same store, and becomes false before publisher
teardown. Production negotiation, live publication, bounded queries and
thread-only replay/resync bypass are installed. Readiness attests complete
wiring, not that every conversation has completed replay. See
[daemon publication and lifecycle](thread-package-shadow-lifecycle-and-evidence.md#shadow-lifecycle-and-evidence).

With both expansion handlers and encoders compiled, `handleNoiseInit` supplies
expansion readiness from the presence of **both** `ThreadToolBody` and
`ThreadChildren`. `negotiateCapabilities` grants `thread_expansion` only when
existing `ThreadReady` is also true and the client advertises all three of
`thread_expansion`, `thread` and `interactive`. The authenticated tail derives
`s.threadExpansion` from the same negotiated slice echoed in `hello_ack`;
rekey preserves it. Either provider alone withholds expansion while preserving
existing `thread` negotiation. `TestThreadExpansionHandshake` covers the
provider/readiness/client-capability matrix through real authenticated
handshakes, including successful child dispatch. Production installs both
expansion callbacks on the publisher's Store (#3523). `negotiateCapabilities` grants
`CapabilityThreadCompact` (#3573) only alongside a `thread_expansion` grant in
the same call, with `thread_compact` advertised and a fourth readiness true;
`s.threadCompact` is set beside `s.threadExpansion` from that negotiated slice.
Production installs both compact providers through `queryDaemonThread` (#3563).
`ThreadCompactReady` requires complete thread readiness, a running publisher on
that same Store, both compact providers and both expansion reads. Clearing the
installation resets compact callbacks and readiness; publisher stop withdraws
readiness before cancelling and joining in-flight work.

**A conversation's join records projection, not just epoch/version, and owns
that conversation's view per connection.** `threadJoin{epoch, version, projection}`
is keyed per conversation; `admitThreadQuery` refuses a non-initializing
(nonzero catch-up, or any page) request whose `projection` differs from the
recorded join before calling any provider, while a version-zero request
always re-initializes, including switching projection. One connection can hold
a compact join on one conversation and a complete join on another at the same
time. Reading a tool/agent body or a conversation's children never creates or
changes a join; only a catch-up/page final does. While a join is compact,
`threadQueryGate` withholds an unsolicited complete live item update for that
conversation before sealing instead of cancelling the in-flight compact query;
a complete live update still cancels only an in-flight *complete*
re-initialization. See [thread query messages](../../../protocol-mobile.md#thread-query-messages).

**Compact live delivery reuses the same join and never advances it (#3574).**
`DeliverThread` admits `thread_compact_update` through `threadContent` on the
identical ordered lane as `thread_item_added`/`thread_item_changed`/`thread_text_append`.
`threadQueryGate` takes a separate branch for it, `hostedInbox.compactLiveGate`,
requiring projection `compact-v1` and a compact join of the frame's epoch
strictly below its version, else `ErrThreadWithheld`. That branch keys on the
envelope type alone — unlike the complete branch, which skips a frame carrying
`InReplyTo` — because a compact update is never actually a reply; a compact
frame carrying a reply ID still has to clear the join check. The gate never
writes `threadJoins` itself, only a successful query final does, so two
different roots consumed at one version both pass rather than being fenced
against each other. `cancelThreadQuery` mirrors the split on the Push side: a
compact update cancels only an in-flight *compact* load of its conversation,
while a complete load in flight withholds the frame instead of being
cancelled by it. `threadWithheld` additionally requires `s.threadCompact` and
a hosted inbox (`s.hosted != nil`) before a compact frame reaches either
sealing path — the legacy Push path seals directly when a session has no
inbox, so without that check a connection `threadQueryGate` never runs for
could receive a compact frame unchecked. `TestThreadCompactLiveRouting`,
`TestThreadCompactLiveFencing`, `TestThreadCompactLiveQueryRace` and
`TestThreadCompactLiveScope` (`v2session_compact_live_test.go`) cover routing,
fencing, the query race on both `DeliverThread` and Push, and epoch
reset/reconnect scope. Production `daemonThreadPublisher.publish` selects the
recipient family before encoding and coalesces compact root replacements on
each 50 ms publication pass, with one progress update for hidden-only consumed
intervals and a final changed-state flush after activity stops (#3563). See
[compact projection](../../protocol-mobile.md#compact-projection-declared) and
[query/live ordering](../../protocol-mobile.md#querylive-ordering).

**Correlation is not item authorization.** `withheldFromConn` exempts ordinary
replies, but `threadWithheld` independently gates live additions/changes/text
appends and their continuations, plus correlated `thread_items` batches, assembly
parts/completion, `thread_tool_body` data/assembly/finals, `thread_children`
data/finals and `thread_reset`. Every expansion frame additionally requires the
recipient's expansion flag. Upstream checks
at `forwardEnvelope` and `forwardAppReply` cannot authorize a later queued send:
`hostedDeliver` rechecks the selected unsealed thread-content head immediately
before Noise sealing. The connection must still be open/authenticated, currently
paired, thread-capable and interactive; its payload must name a currently
registered conversation and pass the existing Codex/`multi_agent` gate. Removal
or eligibility loss while queued withholds content without spending a sender ID
or Noise nonce. Withheld heads release lane capacity and do not strand permitted
output. Missing conversation identity fails closed. Neither `thread` alone nor
`in_reply_to` grants interactive or multi-agent access. Unaffected legacy caller
and provider signatures and reply behavior remain unchanged. Unsolicited content
replaced by the thread is suppressed on live and connect-reconcile paths.
`handleNoiseInit` bypasses event-ring replay and `resync` entirely for thread
connections, including a gap-producing `hello.last_event_id`; supplied current
live-state reconciliation continues. Legacy peers retain replay/resync; raw
`request_history` is retired for all peers (#3715). Supplied live state has independent source-conversation gates,
including for correlated replies and empty clears; see
[supplied live-state ordering](v2-session-manager-state-machine-capability-negotiation-on-the-handshake.md#supplied-live-state-ordering).
Suppression returns normally so a withheld head cannot strand permitted output.
See the
[wire filtering contract](../../protocol-mobile.md#daemon-thread-updates-v2-supplied-delivery-contract)
and [replay drain](v2-session-manager-state-machine-reconnect-replay-hello-last-event-id-rin.md).

Keep the late `threadWithheld` check restricted to `threadContent`. Reapplying
its unsolicited-legacy suppression to supplied `LiveState` would discard
authorized fresh readings and metadata-only clears after source ordering had
advanced their watermarks. Supplied state retains `forwardLiveState`'s source
gate and `forwardEnvelopeSource`'s supplied bypass. Authenticated coverage must
install both the ordered lane (`OutboundContext`) and `ThreadLiveState`:
`TestThreadDeliverySuppliedLiveState` exercises clear/fresh pushes and replies,
and `TestThreadDeliveryLateEligibility` covers queued thread-content denial.
An `Outbound`-only fixture can pass provider readiness but still fail query
admission before the store is called: successful sequences require the
cancellable ordered transport callback. `testDaemonThreadQueryWire` installs
that seam for the real-store-to-wire integration.

`DeliverThread` certifies each envelope only after the ordered transport callback
returns success. Enqueue, Run handoff and batch assembly completion are weaker
boundaries; [`runThreadQuery`](../../../internal/relay/v2session_catchup.go)
delivers serially and stops on any error before its separate success marker.
See [ordered delivery and eligibility](../../protocol-mobile.md#ordered-delivery-and-eligibility)
for withholding, failure, retirement and cancellation outcomes, and
[lane ownership](v2-session-manager-concurrency-hosted-discovery-and-ordered-delivery.md#hosted-discovery-and-ordered-delivery)
for capacity and detached input.

[`admitThreadQuery`](../../../internal/relay/v2session_catchup.go) validates field
presence/types, canonical conversation ID, current pairing, `thread`/interactive
capability, `KnownConversation` and the Codex/`multi_agent` gate before invoking
`ThreadCatchUp` or `ThreadPage`. Normalize limits even for nonzero catch-up: the
backend ignoring its window limit does not authorize a negative wire limit.
Apps must explicitly request catch-up for each open conversation; handshake
and listing do not query, and the active cursor cannot substitute.
The worker maps detached full items and authoritative coverage without deriving
either coverage or reset from item revisions. Missing/nonusable/erroring
providers and codec failures return a content-free `thread.query_failed` error;
reset and failure cannot certify partial items. A page result with reset state
fails instead of emitting catch-up-only `thread_reset`. Nonusable views fail
explicitly even with complete production wiring. Installed daemon providers
share `queryDaemonThread` demand to load and wait cancelably for a usable view;
the relay accepts only a usable captured result. Installed-provider readiness,
clean-reopen queries, reset outcomes
and requests over 36,000 entries are covered by `TestDaemonThreadQueriesReadiness`,
`TestDaemonThreadQueriesRecovery`, `TestDaemonThreadQueriesReset` and
`TestDaemonThreadQueriesBounded`. See the
[wire query contract](../../protocol-mobile.md#thread-query-messages).

Both expansion reads also require `thread_expansion`, a nonempty string epoch
and nonzero unsigned 64-bit item ID/expected revision. Body admission refuses
any cursor key, including null. `ThreadToolBody` must enforce public ownership in
the named conversation and honor cancellation. `runThreadToolBody` rejects nonusable/error
results and passes the captured identity, kind and content to
`EncodeThreadToolBody`, which refuses stale/mismatched identity or wrong kind.
There is no active-conversation or other-client fallback. Empty rendered
input/output is available content; missing/private/unavailable bodies are errors.
See [bounded expansion reads](../../protocol-mobile.md#bounded-expansion-reads).

Child admission accepts an omitted cursor or a nonnull object with every
`ThreadExpansionCursor` field correctly typed. `parseThreadChildCursor` requires
matching conversation/epoch/parent/revision and a nonzero last ID, while explicit
zero version/order remain valid. Child limits accept unsigned 64-bit integers:
omitted/zero defaults to 50 and positive values clamp to 256 before conversion
to `int`; null, negative, fractional or out-of-range values refuse. Existing
complete-query and body limits retain their signed-int ceiling.
`ThreadChildren` owns current epoch/revision/version and public parent/cursor
membership in the named conversation; globally reused IDs cannot establish that
scope. Changed-version continuations refuse even with unchanged parent revision,
requiring a fresh first page. `TestThreadChildrenAdmission` and
`TestThreadChildrenProviderScope` distinguish wire rejection from provider checks.

`runThreadChildren` rejects nonusable/error results, mismatched result
conversation and contradictory `More`/`Complete` evidence. It adapts detached
direct summaries to `EncodeThreadChildren` without rendering complete items.
Preserve capture epoch/version, parent revision and recorded child session
attribution independently of transport generation. The encoder's ascending
`(order, id)` prefix includes ties/order-zero rows, caps headlines at 200 code
points, and omits bodies, parent extras and descendants. Its whole escaped,
stamped page including the separate marker fits `MaxThreadEnvelopeBytes`;
byte shortening derives the cursor from the last delivered row, rather than
`ChildPageResult.CursorAfter`, so selected but undelivered rows remain reachable.
No-row-fits refuses; a usable empty page succeeds. `TestThreadChildrenPages`
checks repeated/nested paging, byte shortening and unchanged joins/live delivery;
`TestThreadChildrenRefusals` separates empty, unavailable, stale and invalid results.

**Validate the final raw field, not a retained scalar value.** JSON null decoded
into a scalar DTO can leave an earlier duplicate-key value intact. Expansion
admission reads final raw epoch/item/revision and supplied cursor fields into
pointers, rejecting a trailing null instead of accepting earlier valid scope.
`TestThreadToolBodyAdmission` exercises duplicate keys through authenticated
dispatch before provider work; child cursor validation applies the same rule.

`ThreadPage` receives required unsigned `before_order`, including zero, and the
normalized limit. `runThreadQuery` forwards every supplied full item and all four
backend page fields through `EncodeThreadReply`; only the separate empty final
marker certifies `[LowerOrder, UpperOrder)`. Lower-bound ties and hidden/dropped
public rows remain intact, as do complete deduplicated out-of-range parents.
Parents do not expand coverage, and a short page cannot establish `OlderExists`.
Use `Continuation == LowerOrder` as the next exclusive upper bound; an empty page
keeps the requested upper bound with lower/continuation zero and older-exists
false. Selection and parent closure belong to
[`Store.HistoryPage`](thread-package-background-store-queries.md#bounded-queries),
not another relay query engine. `TestThreadPageResults` and
`TestThreadPageContinuation` pin forwarding and successive ranges, while
`TestThreadPageLargeReply` checks assembly beyond `hostedQueueLimit` escaped,
stamped envelopes without dropping supplied content or request correlation.

The large-reply wait includes complete `EncodeThreadReply` work before first delivery;
provider readiness alone cannot distinguish a stalled sender from expensive escaped
JSON and race scheduling. `testHostedPeer.nextBefore` shares one 15-second absolute
deadline across reconstruction and the separate coverage marker, checks expiry before
consuming buffered replies, and does not renew the budget per fragment. Ordinary
`next` waits remain three seconds. Keep the oversized-item identity/order and
byte-for-byte reconstruction assertions when adjusting timing; see
[reply-timeout diagnosis](development-verification-test-execution-and-artifact-survival.md#diagnosing-reply-timeouts)
for the contention evidence and bounded missing-reply/final-marker checks.

**Dispatch probing must reach the owning validator.** Decoding a full `Envelope`
just to identify a query type rejects wrong-type IDs before `admitThreadQuery`
can apply its connection-close policy. `dispatchAppFrame` probes the type
independently for catch-up, page, tool-body and child requests; other malformed
types keep their existing fallback. `TestThreadPageAdmission`,
`TestThreadToolBodyAdmission` and `TestThreadChildrenAdmission` send malformed
IDs through the authenticated wire, because direct admission tests would miss
this bypass.

**A refusal must preserve the legacy delivery path.** An Outbound-only peer has
no cancellable ordered sender. Initializing a lane merely to deny an unnegotiated
query would close it before its owed correlated error. `admitThreadQuery`
uses Run-owned `forwardEnvelope` when no lane exists; accepted sequences require
the cancellable ordered sender. `TestThreadCatchUpLegacyRefusal` and
`TestThreadPageLegacyRefusal` pin that split; the
[legacy raw-history path](../../protocol-mobile.md#conversation-history-v2)
retains its cursor, disk reads and delivery behavior.

**Completion must fence live admission.** One generation-specific query token
owns each connection's unfinished catch-up, page, tool-body or child read; all share
the slot.
`cancelThreadQuery` cancels it when a live item update for that conversation is
admitted; `threadQueryGate` catches already queued updates immediately before sealing. No extra live buffer is
introduced. For complete queries, cancellation checking and capture watermark
installation at the separate success marker share `hostedMu`; checking them
separately could let a live patch cross the completion cut and be overwritten
by older full items.
Matching-epoch updates at or below the captured version are withheld, including
fragments. Later updates retain source order, even when distinct patches share
one consumed version; the capture watermark is not advanced for each patch.
The map caps joined conversations at 256 per connection and rejects further
new joins. Existing queue bounds and generation fencing remain authoritative;
lost/withheld/cancelled parts never earn a success marker, and retirement cannot
carry a join into a reused connection ID. The catch-up and page variants of
`TestThreadCatchUpLiveRace`, `TestThreadCatchUpInterrupted` and
`TestThreadCatchUpJoinedLive` exercise these cuts; `TestThreadPageSharedAdmission`
pins cross-query slot contention and the join cap. Both requested-scope tests
pair gap-producing thread and legacy handshakes with named requests.

Expansion finals take a separate `threadQueryGate` branch: cancellation checking
and release of that query share `hostedMu`, without writing `threadJoins`. Expansion
completion cannot advance full-thread coverage, replace an existing join or
suppress unrelated live rows; expansion admission also bypasses the join-count cap.
`deliverThread` binds even late refusals to the captured inbox generation, so a
reused connection ID cannot receive old work. Cleanup compares the query token
before releasing the slot, including when a refusal is withheld; it cannot clear
a later read. `TestThreadToolBodyResults`, `TestThreadToolBodySharedSlot`,
`TestThreadToolBodyFullQuerySlot` and `TestThreadToolBodyGenerationFence` pin those
boundaries. Failed/withheld data or final delivery cannot grant success; held
providers and cancellation remain independent across connections.
`TestThreadChildrenSharedSlot`, `TestThreadChildrenProviderShutdown` and
`TestThreadChildrenInterrupted` extend these boundaries to child/body/complete
contention, held providers, connection reuse and failed/withheld final delivery.

A racing complete query may fail explicitly; retry from the last completed checkpoint.
Only fully assembled batches followed by the separate successful marker advance
that checkpoint. A terminal refusal can arrive before already queued live
updates; correlate retry replies by `in_reply_to` while continuing to handle
unsolicited live frames. A wire test that reads the next frame as its retry
reply can mistake a late live update for query output. `TestDaemonThreadQueriesJoin`
awaits both the refusal and queued updates before retry, then reconstructs full
states through subsequent real publisher changes.

**Project fields without round-tripping a partial DTO.** `conversation_updated`
has supplied fields that `ConversationUpdatedPayload` does not represent,
including binding and legacy watermark fields. Rebuilding the row through that
DTO would silently discard those fields and introduce absent zero/null fields.
`agentTaggedForConn` and `threadSummary` therefore use `replaceObjectField` to
edit only the projected fields, preserving unrelated encoded values and
presence. Fresh payload bytes isolate each connection from shared pushes and
replay; unchanged legacy replies retain their original bytes.

`ThreadLastShownVersion` is the only authority for every `conversations` row
and every pushed or replied `conversation_updated`. Usable zero emits numeric
`0`; missing/rebuilding/unavailable state removes even a stale supplied value.
Item revisions and partial recovery cannot substitute for that reading. Thread
unread is `last_shown_version > read_up_to`; stored binding, durable read mark
and legacy watermark keep their meanings and presence. Non-thread delivery
strips the reading and envelope session metadata and omits clear frames entirely.
`TestV2Session_ThreadSummaries` checks distinct readings, zero/unavailable,
legacy literal bytes and unrelated field presence, including the combination
with agent tagging. `TestV2Session_ThreadDelivery` covers item denial even in
handler replies, and `TestV2Session_ThreadReplay` checks thread replay bypass,
an untouched replay watermark and permitted live traffic. Continuation
payload/order and the envelope budget are pinned by
`TestV2Session_ThreadContinuations`; projection must not
grow the codec's supplied `SessionID`. See the
[summary contract](../../protocol-mobile.md#conversations).

**Retirement quarantines before teardown.** A fold continuity loss uses
`RetireLiveState`'s producer-gap close, `StatusProducerGap` (`4414`), rather than
the actual push-queue byte ceiling (`4413`). The first terminal cause latched
under `pushMu` wins: prior byte overflow stays `4413`, and pushes after retirement
cannot relabel it or grow the queue. Latching the queue must
immediately stop `drainOnce`, `drainLiveOnce` and `signalLiveDrain` from consuming
dependent items or current readings: Run may select a ready drain before the
queued teardown. `TestRetiredThreadQueueDoesNotDrain` authenticates a held
append and explicitly drains in that order before checking final removal.
`TestRetiredLivePendingDoesNotDrain` also preserves the pending reading and
avoids invoking its snapshot reader. Recovery uses a fresh Noise handshake,
current transient-state reconciliation and explicit thread catch-up from the
last completed checkpoint; live publication cannot backfill the missing range.
See [close codes and recovery](../../protocol-mobile.md#error-codes) and
[hosted-admission quarantine](v2-session-manager-concurrency-hosted-discovery-and-ordered-delivery.md#hosted-discovery-and-ordered-delivery).

`ActiveThreadConns(ctx, conversation)` enumerates on Run using delivery's
shared conversation/interactive/agent gates, excluding latched queues. Ordinary
`ActiveConns` keeps its existing enumeration. The publisher refreshes prior
consumer tracking from eligible peers and records successful enqueues. Recording
every negotiated peer would let a Codex gap retire a basic connection that
never received the affected items. `TestDaemonThreadGapPreservesAgentIneligiblePeer`
checks that only the eligible consumer is retired and that the basic peer
continues authenticated Claude publication.

**Completed and pending views both determine publication (#3563).**
`ActiveThreadConns` fills the additive `View` field
(`ThreadViewUnjoined`, `ThreadViewComplete`, `ThreadViewCompact`) from the
connection's recorded join for that conversation. `threadViews` captures it
atomically with `PendingView`, the admitted items query's projection, under
`hostedMu`; an expansion read leaves the pending view unjoined.
`ActiveThreadConns` also records the negotiated `ThreadCompact` flag so an
unjoined complete-only peer incurs no compact encoding/read work. A connection
with no hosted inbox or no join reports an unjoined completed view.
`ActiveConns` leaves these fields at zero because it has no conversation scope.
One connection can report compact for one conversation and complete for another in the same
snapshot. `TestThreadCompactLiveRouting` pins that per-connection,
per-conversation split; `TestThreadCompactLiveScope` pins it against a
reconnect (a reused connection ID starts unjoined again) and an epoch reset
(the new epoch reports unjoined until re-initialization). Expansion
completion stays independent of this view: a tool-body or children read
neither writes nor reads it, and changes no join either way.

Using only the completed join loses updates during complete↔compact switches:
the old family's update cannot cancel a stale query of the new projection,
yet the publisher cursor advances. Publication therefore includes completed
and pending families. Unjoined compact-capable peers also participate in both
families for cancellation; compact frames remain withheld without a compact
join. Admission after the snapshot crosses the daemon publication gate before
Store capture, so it cannot miss that pass. Late relay checks authorize content,
not the snapshot. `TestDaemonThreadCompactSwitch` covers both directions and
both capture/admission orderings through the installed authenticated daemon;
see [daemon evidence](thread-package-shadow-lifecycle-and-evidence.md#shadow-lifecycle-and-evidence).
