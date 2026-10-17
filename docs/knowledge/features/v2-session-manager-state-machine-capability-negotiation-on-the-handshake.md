# Capability negotiation on the handshake (#626) — `negotiateCapabilities` + `s.interactive` + capability-aware `ActiveConns`

The daemon-side **trust decision** [#607](../codebase/607.md) deferred. #607 landed the wire vocabulary (`CapabilityInteractive`, the `omitempty` `Capabilities []string` field on both `HelloClientPayload` and `HelloAckPayload`) but `handleNoiseInit` **decoded the phone's advertised capabilities and ignored them** — the `hello_ack` echoed nothing, the session recorded no capability state, and `ActiveConnIDs` returned every open conn with no capability filter. This slice closes that boundary; it is `security-sensitive` (the daemon deciding which internet-facing phones are *granted* the interactive capability) and is the enforcement half of ADR 025's deliberately split design: #607 = vocabulary (no trust), #626 = trust decision. See [`codebase/626.md`](../codebase/626.md).

**The authoritative supported set + the pure intersection.** The supported set is the daemon's own constant — **never** a mirror of the phone's claims:

```go
var supportedV2Capabilities = []string{
    protocol.CapabilityInteractive,
    protocol.CapabilityQuestion,
    protocol.CapabilityModelList,
    protocol.CapabilityContextUsage,
    protocol.CapabilityMultiAgent,
    protocol.CapabilityStopBackgroundTask,
}

// Intersect advertisements with the supported set in its order, then grant
// thread, hosted apps and expansion only with supplied readiness. Dedup and
// drop unknown strings; nil means no capabilities were negotiated.
func negotiateCapabilities(advertised []string, readiness ...bool) []string {
    var out []string
    for _, name := range supportedV2Capabilities { // `name`, not `cap` (builtin)
        if slices.Contains(advertised, name) { out = append(out, name) }
    }
    if len(readiness) > 0 && readiness[0] && slices.Contains(advertised, protocol.CapabilityThread) {
        out = append(out, protocol.CapabilityThread)
    }
    if len(readiness) > 1 && readiness[1] && slices.Contains(advertised, protocol.CapabilityHostedAppsV1) {
        out = append(out, protocol.CapabilityHostedAppsV1)
    }
    if len(readiness) > 2 && readiness[0] && readiness[2] && slices.Contains(advertised, protocol.CapabilityThreadExpansion) && slices.Contains(advertised, protocol.CapabilityThread) && slices.Contains(advertised, protocol.CapabilityInteractive) {
        out = append(out, protocol.CapabilityThreadExpansion)
    }
    return out
}
```

**Iterating the supported set (not the advertised set) is the security primitive** — "a spoofed capability can never be granted" is a structural property of the loop shape, deterministic, not a runtime guard a later refactor could bypass (Threat 1 / AC#3). A pure receiver-less function, directly table-testable for the whole negotiation matrix.

**Echo + record, single source of truth.** `handleNoiseInit` computes `negotiated` with `negotiateCapabilities` and the supplied thread/hosted/expansion readiness decisions **before** the `hello_ack` literal, and adds `Capabilities: negotiated` to `HelloAckPayload`. Token validation precedes ack construction so host-only metadata can be withheld. The ack is sealed via `WriteResp` before enforcing the token verdict, so it is built for accepted and token-refused handshakes — `omitempty` keeps the key absent for a no-capability phone (v1 byte-stability, AC#5). The per-conn `interactive bool` (a new `V2Session` field beside `device`/`peerStatic`, same set-once / single-owner-goroutine discipline) is set in the token-OK tail, **between `s.device = &device` and `s.state = V2StateOpen`**, via `s.interactive = slices.Contains(negotiated, protocol.CapabilityInteractive)` — derived from the *same* `negotiated` slice the ack echoed, so the echoed capability and the recorded flag can never disagree.

**Fail-closed, two independent gates.** The flag is written **only** on the token-OK branch (after `Devices.Validate`, before `V2StateOpen`); every other path leaves it at its `false` zero value (advertise-nothing / `null` / `[]` / only-unsupported → `negotiated == nil` → `false`). And `handleActiveConns` filters on `V2StateOpen` — so even if the flag were mis-set, a non-open (un-authenticated) session is never enumerated, and the negotiated flag of an un-authenticated peer is never observable. Belt-and-suspenders of different fabric — two deterministic code-level gates, the same shape #588/#589 established. Re-key (`handleRekeyInit`) preserves `s.interactive` by never touching it, like `device`/`peerStatic`.

**Token-fail ack echo grants nothing.** On a token-fail handshake the `noise_resp` carries the ack (with `negotiated`) to a cryptographically-authenticated-but-unauthorized peer that is then closed at 4401 and deleted from `m.sessions`. The echo grants nothing (the session never opens, is never enumerated or pushed-to) and leaks nothing (`CapabilityInteractive` is public protocol vocabulary). Suppressing the encrypted ack on token refusal would break the pinned "state → `HandshakeComplete` before token validation" invariant for zero security gain — accepted non-issue (spec § Security review, verdict PASS).

**`ActiveConns`/`ActiveConn` — the capability-aware enumeration.** The downstream consumer reads the negotiated flag via the [widened snapshot funnel](#concurrency-safe-open-session-enumeration-588--activeconns-method--snapshot-funnel): `ActiveConns(ctx) []ActiveConn` returns each open conn paired with its `Interactive` flag. The #596 structured-stream fan-out (since [#699](../codebase/699.md) the **single** v2 assistant-turn delivery path — #589's coarse `message` broadcast, re-targeted to non-interactive conns in [#634](../codebase/634.md), was deleted as dead code once the 2026-06-22 ADR 025 amendment made every phone `interactive`; the `Interactive` flag survives because the structured path still gates on it) consumes this: [`#632`](../codebase/632.md)'s `interactiveTurnEmitterV2` (`cmd/pyry/interactive_turn_v2.go`) snapshots `ActiveConns`, filters `Interactive == true` (the load-bearing capability gate, `if !c.Interactive { continue }`), then `Push`es a sealed structured envelope per conn-id — the emitter is built and unit-tested, with #633 wiring it to the live producer. [`#657`](../codebase/657.md)'s `sessionTransitionEmitterV2` (`cmd/pyry/session_transition_v2.go`) is the **second** interactive-only consumer of this exact primitive: it reuses the same `interactiveBroadcaster` interface and `if !c.Interactive { continue }` gate to fan a `session_transition` envelope (a `/clear` rotation or idle/cap eviction surfaced by [#659]'s pool observer) to interactive phones — but stamps **no** `EventID` (a session boundary is not a turn-stream event, so it does not join the #647 ring) and adds no manager-side path. [`#722`](../codebase/722.md)'s `queueStateEmitterV2` (`cmd/pyry/queue_state_v2.go`) is the **third**: it fans a `queue_state` envelope (the inbound `msgqueue` backlog for one conversation, snapshotted on each [#719] `OnChange`) to interactive phones through the same reused interface + gate, likewise `EventID`-nil (idempotent full-state, off the #647 ring, a never-drop control event). Its one wrinkle — the `OnChange` seam fires from a *mix* of goroutines (the enqueue/remove callers on the manager Run goroutine, each drain on its own), so an inline `ActiveConns` would deadlock Run — is solved the same way #657 solves it: a buffered hand-off channel + dedicated Run goroutine, so the blocking `ActiveConns` runs off the dispatch goroutine. [`#1008`](../codebase/1008.md)'s `sessionErrorEmitterV2` (`cmd/pyry/session_error_v2.go`) is the **fourth**: it fans a `session_error` envelope (terminal `CodeSessionBlocked`, `SessionErrorPayload{ConversationID,Code,Message}` per [#1007](../codebase/1007.md)) to interactive phones whenever [#1000](../codebase/1000.md)'s `msgqueue` give-up seam (`OnGiveUp`) fires — through the same reused interface + gate, likewise `EventID`-nil (a give-up is a one-shot edge with no connect-time reconcile, unlike a replay-eligible event). Structurally near-identical to `queue_state`'s hand-off-channel + dedicated-Run-goroutine shape, **minus the queue reference**: the emitter holds no `Snapshot`/`*msgqueue.Queue` field, so queued (untrusted) phone text is unreachable by construction rather than by discipline. #2209's `workspaceUpdateEmitterV2` (`cmd/pyry/workspace_update_v2.go`) is the **fifth**, fanning a `workspace_updated` record to every other interactive conn once `rename_workspace` (#2207) has stored a label change — and the first of the five triggered directly from a dispatch handler rather than a pool/queue observer. That distinction is what decides whether a hand-off channel is needed, and it is not: `rename_workspace` is map-dispatched, so `dispatchAppFrame` falls through to `enqueueAppFrame` and `announce` runs synchronously on the requesting conn's own `appFrameWorker` goroutine — never on `Run`. A bare `ActiveConns` call is therefore safe despite `ActiveConns`' own doc comment reading, out of context, as though any non-`Run` goroutine needs care ("safe to call from any goroutine other than the dispatch goroutine"): the snapshot request funnels onto `Run` and `Run` is always free to service it while the handler waits. `queue_state` and `session_error` need their hand-off-channel-plus-dedicated-goroutine shape because *their* trigger (`OnChange`/`OnGiveUp`) fires from a mix of goroutines that sometimes includes `Run` itself, not because calling `ActiveConns` off-`Run` is ever unsafe on its own — a future handler-triggered emitter can call it inline the way this one does. `workspaceUpdateEmitterV2` also adds a shape none of the first four needed: it excludes one conn from its own fan-out, keyed on `ActiveConn.ConnID` against the requester's `dispatch.Conn.ConnID()` (one string space, no mapping) — the requester already holds its correlated reply, but a requester with a second conn open is still pushed on that one, since exclusion is per conn and not per device. The single-`Interactive`-bool shape held while `supportedV2Capabilities` had one member (YAGNI); #2020 added a second, `CapabilityQuestion = "question"`, as the deliberate, separately-reviewed change this always required, and #2172 added a third, `CapabilityModelList = "model_list"` (detecting the pair of #2124's daemon-wide model-list fallback and #2125's `request_model_list` verb — one string covers both, since neither is separately useful without the other). Both confirm the shape still holds without a new bool. `question` and `model_list` are both **detection only**: neither grants a gate of its own, and `reconcileQuestions`, `handleRequestModelList`/`modelListFor`, and the four #632-shaped emitters above all stay gated on `interactive` alone, so a phone advertising either without `interactive` negotiates as non-interactive and gets none of them. The one property that had been a hypothetical fragility is now load-bearing and test-pinned: `s.interactive` must stay `slices.Contains(negotiated, protocol.CapabilityInteractive)`, a **value-specific** membership test, never `len(negotiated) > 0` — with more than one capability in the supported set, an emptiness check would grant interactive access to a client that advertised only the others. #2020's `question_alone_grants_no_interactive` and #2172's `model_list_alone_grants_no_interactive` subtests each assert `ActiveConns(...).Interactive == false` for their case and are, by mutation, the only rows in either handshake test table that die if `Contains` is weakened to a length check.

`CapabilityContextUsage = "context_usage"` (#2431) joined the supported set as a fourth, detection-only member the same way `question` and `model_list` did — its verb, `request_context_usage`, gates on `interactive` alone.

**`CapabilityMultiAgent = "multi_agent"` (#2643) is the fifth member, and the first that is not pure detection.** Its neighbours only let a client tell which daemon build it is talking to; this one changes what the daemon sends. `V2Session` gained a second per-conn bool beside `interactive`, `multiAgent`, set in the same token-OK tail via the same value-specific `s.multiAgent = slices.Contains(negotiated, protocol.CapabilityMultiAgent)` — never a length check, for the reason above. `routeAppFrame` copies it onto the per-frame `*dispatch.Conn` with `conn.SetMultiAgent(s.multiAgent)` before `Route` runs, so `internal/relay/handlers.ListConversationsWithAgents` (and #2644/#2645's handlers) read the decision as `c.MultiAgent()` without reaching back into the session — see [`dispatch-package.md`](dispatch-package.md). Re-key preserves `s.multiAgent` by never touching it, the same as `interactive` and `device`/`peerStatic`; `TestV2Session_MultiAgent_ReachesHandlerAndSurvivesRekey` pins the flag across a same-static re-key. It still grants **no** interactive access — no `interactive` gate reads it, and `multi agent alone grants no interactive` in `TestV2Session_Handshake_CapabilityNegotiation` pins that — but unlike `question`/`model_list`/`context_usage`, a client that negotiates only `multi_agent` and not `interactive` still gets a *shaped* `conversations` reply (every row carries `agent`, and Codex rows are included), because that verb's own gate is `c.MultiAgent()`, not `s.interactive`. The two flags are independent booleans on both `V2Session` and `Conn`, not one capability implying the other.

**Detection must not become an actuation gate.**
`CapabilityStopBackgroundTask = "stop_background_task"` is the sixth supported
member (#2797). Clients show a per-task stop button only when intersection
negotiation echoes it in `hello_ack`; an omitted advertisement is never echoed.
Keep `s.interactive` value-specific: advertising this detection string alone
leaves the connection non-interactive. `dispatchAppFrame` still checks only the
wired `BackgroundTaskStopper` seam and `s.interactive` for this verb, so requiring
the detection string there would reject existing interactive clients.
`TestV2Session_Handshake_CapabilityNegotiation` pins the detection-only
connection's noninteractive state; `TestV2Session_StopBackgroundTask_Contract`
exercises actuation with only `interactive` advertised. Both negotiation tables
also assert all six capabilities in supported-set order against a reversed
advertisement containing a duplicate and an unknown string. See the
[wire contract](../../protocol-mobile.md#stop-background-task-v2) and
[background-task payloads](protocol-package-background-task-event-payloads.md).

**#2644 gives `multi_agent` a second gate, at the push choke point rather than a reply.** `forwardEnvelope` — the one seal path every pushed frame reaches (the push-queue drain, reconnect replay, the resync marker) — calls `withheldFromConn(s, env)` (`v2session_agentgate.go`) before sealing; a conn without `s.multiAgent` gets nothing for a frame whose `pushedConversationID` resolves, through the reused resolution rule (`handlers.agentOf` exported to `AgentOf` for this second caller), to a Codex-bound conversation. Ordinary handler replies (`forwardAppReply`) retain their existing exemption, so a client that already learned a Codex conversation id from an accepted request still gets ordinary replies naming it. Thread item updates and continuations are gated independently at both sealing boundaries, even when correlated; see [thread delivery and summary projection](#thread-delivery-and-summary-projection).

A withheld frame **must** make `forwardEnvelope` return `nil`, not an error: `drainReplayOnce` abandons a conn's whole replay tail on an error, and only releases the held live stream once the tail finishes draining. An error return would look correct against a test that checks one withheld event, while silently dropping every Claude event queued behind it.

`pushedConversationID` decodes only two keys, and picks between them by envelope type: `conversation_updated` carries the conversation under `id`, every other pushed type under `conversation_id` — the one pushed frame keyed differently, and the one a gate that reads only `conversation_id` would leak straight through. `modal_dismissed`, `question_dismissed` and `workspace_updated` carry neither key and are delivered unchanged: the first two name only an opaque modal/question-batch id, and the `*_shown` frame that introduced that id was itself withheld from the same conn, so the dismissal is a stray a non-`multi_agent` client is expected to ignore.

Resolution reuses the list's fail-open rule rather than a stricter one: an unknown conversation, one with no bound session yet, and a harness-lookup miss all read as Claude and are delivered — AC 2 forbids dropping a Claude frame on uncertainty, so every uncertain case delivers rather than withholds. That leaves one named, non-silent gap: whatever the daemon pushes about a Codex conversation before its session is bound still reaches an old conn, until a later ticket binds the session ahead of the first push about it.

**#2652 gives `multi_agent` a third use at the same choke point: a rewrite, not just a withhold.** `forwardEnvelope` calls `mergedForConn(s, env)` after `withheldFromConn` and the replay dedup, immediately before marshalling — decoding a pushed `model_list`'s payload, replacing `Models` with the `V2SessionConfig.MergedModelOptions` seam's tagged Claude-then-Codex rows, and returning a copy of `env` whose `Payload` is fresh bytes, never writing through the shared push/ring copy. `env` is a by-value parameter for exactly this reason: every other conn, and a later replay of the same ring entry, still gets the original bytes.

**The obvious gate — `env.InReplyTo == nil` means "this is a push, not a reply" — is wrong, and a green test would have hidden it.** `reconcileModelLists` also sends its already-merged connect-time snapshot with `InReplyTo` nil, so a first cut of `mergedForConn` gating on that alone ran the seam a *second* time over an already-tagged list on every capable reconnect: a Codex row got re-tagged `agent: claude`, and the Codex rows were appended again, doubling them. A test that only pushed one live frame to one capable conn stayed green; the bug needed a test that also exercises the reconcile path with the same seam wired, which is what `TestV2Session_ModelListReconcile_PassesTheConnsMultiAgentDecision` was rewritten to do. The fix gates on `env.EventID != nil` instead — every live push (`interactiveTurnEmitterV2`'s per-conn envelope) and every ring replay carries one; both #2651 paths (the `request_model_list` reply and the reconcile snapshot) leave it nil and pass through `mergedForConn` untouched. `EventID`-presence, not `InReplyTo`-absence, is the load-bearing signal for "has this frame been through the merge already."

`negotiateCapabilities` is O(k·n) in the supported-set size k; ADR 037 flagged the third capability as "a reasonable point to reconsider a set lookup." #2172 asked the question at k=3 and answered no: a `map[string]struct{}` would cost a package-level allocation and an init-order dependency to save three string comparisons on a once-per-connection path, and would lose the ordering guarantee `slices.Equal` in both handshake test tables depends on (emit order is supported-set order). See [ADR 037](../decisions/037-capability-strings-not-version-numbers.md) for the cross-repo convention this ticket established: any future user-facing wire feature adds its own capability string the same way, purely for client-side build detection, and decides independently whether it also becomes a gate. #2124 and #2125 shipped the on-demand model list itself without following that convention, and the omission was expensive to a second repo: pyrycode-desktop#1169 burned three rework cycles because, from the client, a stale daemon (one built before #2172) and a broken feature both present identically as "this chat has no published levels" — the capability string is what lets a client tell the two apart at connect time instead of by watching a live-gate spec fail.

### Hosted-app admission

Offered `hosted_apps_v1` is admitted only with `HostedAppsDiscovery`,
`HostedAppsSend` and a true `HostedAppsReady` attesting registration, supervision
and resource routing together. A registry or transport adapter alone proves
neither complete hosting nor readiness. `newDaemonAppsManager` installs initialized
storage as discovery and `OutboundContext` as `HostedAppsSend`, preserving the
caller's complete readiness attestation. Production supplies no such attestation
while supervision (#3122) and resource routing (#3123) are absent, so it withholds
the capability; `TestHostedAppsProductionUnsupported` checks both `hello_ack`
omission and `protocol.unsupported` for `list_apps` through the real daemon.
Ready in-process fakes exercise wiring without an environment bypass.
Apps require neither `interactive` nor `thread` nor
an active channel; every request/completion/delivery also checks current paired
membership. See the [wire gates](../../protocol-mobile.md#hosted-apps-v2).

An offered capability initializes the ordered output lane even when admission
is withheld; otherwise legacy replies could reuse `hello_ack`'s sender ID before
the first unsupported app request. A first hosted request on an existing legacy
connection also has to preserve previously accepted transport output. Neither
lane selection nor a capability echo authorizes discovery. See
[sender IDs and delivery ordering](v2-session-manager-concurrency-hosted-discovery-and-ordered-delivery.md#hosted-discovery-and-ordered-delivery)
and [transport admission](transport-package.md#lifecycle-and-goroutines).

### Thread delivery and summary projection

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
handshakes, including successful child dispatch. Production installs neither
expansion callback, so advertisement remains withheld pending real Store
installation/activation in #3523. `negotiateCapabilities` grants
`CapabilityThreadCompact` (#3573) only alongside a `thread_expansion` grant in
the same call, with `thread_compact` advertised and a fourth readiness true;
`s.threadCompact` is set beside `s.threadExpansion` from that negotiated slice.
Production installs none of the compact providers, so it stays withheld
pending #3563.

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
live-state reconciliation continues. Legacy peers retain replay/resync and raw
`request_history`. Supplied live state has independent source-conversation gates,
including for correlated replies and empty clears; see
[supplied live-state ordering](#supplied-live-state-ordering).
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
explicitly even with complete production wiring; queries do not load or wait
for recovery. Installed-provider readiness, clean-reopen queries, reset outcomes
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
`RetireLiveState`'s existing queue-overflow gap close. Latching the queue must
immediately stop `drainOnce`, `drainLiveOnce` and `signalLiveDrain` from consuming
dependent items or current readings: Run may select a ready drain before the
queued teardown. `TestRetiredThreadQueueDoesNotDrain` authenticates a held
append and explicitly drains in that order before checking final removal.

`ActiveThreadConns(ctx, conversation)` enumerates on Run using delivery's
shared conversation/interactive/agent gates, excluding latched queues. Ordinary
`ActiveConns` keeps its existing enumeration. The publisher refreshes prior
consumer tracking from eligible peers and records successful enqueues. Recording
every negotiated peer would let a Codex gap retire a basic connection that
never received the affected items. `TestDaemonThreadGapPreservesAgentIneligiblePeer`
checks that only the eligible consumer is retired and that the basic peer
continues authenticated Claude publication.

### Supplied live-state ordering

`startRelayV2` installs the [daemon retained-reading delivery path](streamsup-package-draining-turnevents-into-the-interactive-emitter-daemon-retained-live-state.md#relay-delivery):
`daemonRelaySnapshot` supplies a finite `ThreadLiveState` cursor spanning owner
conversations, `startDaemonLivePublisher` supplies bounded `PushLiveState`
publication, and `installDaemonLiveRelay` wires all five source-bearing reply/
settings providers. Accepted producers hand off publication before legacy
enumeration or fan-in can refuse/delay delivery. Captured source/order fields survive adaptation;
original payload session fields and prompt answer IDs stay intact. This wiring
now participates in production `ThreadReady` alongside the installed fold
publisher and watermark provider. Negotiated thread connections receive current
readings at connect/reconnect and source-bound live updates; these snapshots
initialize transient state only. Thread item backfill requires explicit catch-up
for each open conversation; production bounded catch-up/page providers and
thread-only replay bypass are installed.

Carry provenance with the originating reading through `PushLiveState` and
`ThreadLiveState`, rather than resolving a conversation's current binding when
sealing delayed output. `liveWithheld` uses `LiveState.ConversationID` even when
the wire payload is `{}` or names only a prompt/session ID. Correlation is not
authorization: a supplied reply and its clear require interactive access, a
known source conversation and the existing Codex gate. Withheld readings do not
advance delivery state. See the [wire contract](../../protocol-mobile.md#session-scoped-live-state-v2-supplied-delivery-contract).

`forwardLiveState` stamps omitted `Envelope.ConversationID` from that retained
source on detached snapshots, queued pushes/replies and generated/supplied
clears. Equal, null or omitted session tags cannot distinguish conversations;
the top-level routing key must survive even when the payload stays `{}` and
`in_reply_to` is removed. `validateLiveState` rejects malformed or conflicting
envelope/payload identity before admission or delivery changes any watermark.
`PushLiveState` clones the raw routing buffer so caller reuse cannot reroute
queued state. `TestLiveStateConversationRouting`,
`TestLiveStateConversationIdentityDetached` and `TestLiveStateRoutingLegacyBytes`
inspect authenticated decrypted bytes, including key presence and legacy omission.

Supplied-reply fixtures must align payload identity with the retained source,
while deliberately allowing the request to name a different conversation.
Otherwise identity validation rejects the fixture before it can prove that
source access gates ignore request correlation. The source-bearing reply tests
exercise this distinction; a request ID identifies the answer, not its authority.

Prompt shows use detached captured `Answerable`; the daemon snapshot excludes
missing/false eligibility, and relay delivery withholds false eligibility without
re-reading mutable prompt state. Existing authorized resolvers remain the answer
authority. Source-bearing dismissals preserve original prompt IDs and permission
outcome/source and retire only that identity; they do not appear in snapshots. With
`ThreadLiveState` installed, unsolicited legacy live frames are withheld from
thread recipients. Otherwise an unsourced legacy dismissal could arrive before
its source-bound counterpart and bypass conversation checks. Legacy recipients
keep their existing frames and bytes.

`LiveStateCurrent` checks owner generation and accepted retained revision before
unsolicited Run delivery, including captured cursors and queued pushes. Removing
owner state alone cannot invalidate a detached cursor; without this check it can
restore stale indicators after removal/recreation. An unfinished query reservation
does not invalidate prior accepted state. Dismissals use terminal ordering and
clears use current generation. Correlated replies instead preserve captured
evidence under relay ordering. `TestDaemonLiveRelayRetiredSnapshot` and
`TestDaemonLiveRelayReservedQuerySnapshot` cover those distinctions;
`TestLiveStateCapturedEligibility` pins detached eligibility.

Canonical daemon model inventories carry `ModelListUnprojected`, so
`mergedForConn` projects separately for each recipient on snapshots, pushes and
correlated replies, even without a legacy `EventID`. Retaining the requester's
projection would expose multi-agent rows to basic clients. Empty Claude
inventories from Codex-only fallback are withheld from basic recipients before
generating clears, preserving refusal; model clears stay `{}` and projected
envelopes are size-checked before encryption. Other supplied providers keep their
existing projection contract. `TestDaemonLiveRelayModelProjection` and
`TestDaemonLiveRelayCodexOnlyProjection` cover both requester capabilities and
fanout. See [cached evidence and borrowed bootstrap vocabulary](streamsup-package-draining-turnevents-into-the-interactive-emitter-daemon-retained-live-state.md#control-and-on-demand-readings)
for reply-only target ordering that cannot publish foreign cache state.

Select an optional source-bearing provider once, only for a negotiated thread
connection. Source-only context/MCP installation must pass admission without a
legacy provider; otherwise the request is silently consumed before its provider
can run. A selected provider's refusal must not trigger a legacy query or repeat
a settings mutation. `pushLiveReply` adds only request correlation and returns
the supplied `LiveState` to the bounded `PushLiveState` queue for Run-owned
delivery. Copy `thread` and `multiAgent` into `appFrameJob` on `Run` before
worker handoff; reading negotiated session fields from workers would violate
their ownership. See [settings enrichment](v2-session-manager-state-machine-inbound-request-session-settings-the-rea.md#supplied-settings-reads)
and [connection cancellation](v2-session-manager-concurrency.md#connection-scoped-reading-cancellation).

`forwardLiveState` orders session generations across all families for one
conversation, then revisions by normalized family and `ReadingID`. Sorting
opaque session IDs would misorder transitions. A family-wide revision alone
would discard another outstanding prompt or tool/task reading. Conversely,
shown and dismissed messages for one prompt must share a family: separate type
keys let an older shown snapshot restore an answered prompt. `liveStateClear`
normalizes modal/question dismissal and settings-update types to their matching
shown/settings families. Clears reset once per family and generation; individual
reading identities keep their revisions independent.

Duplicate unsolicited readings are suppressed, but equal-revision correlated
replies still answer the request. Treating them as duplicate pushes would leave
the request unanswered. Strictly older replies and older-session output are
suppressed. Source ordering also supersedes the legacy `replySuggestionStale`
guard for supplied suggestion readings; its payload-only conversation revision
cannot express session transitions. `TestLiveStateOrdering` covers overtaken
snapshots, answered prompts, distinct IDs/conversations, delayed replies and
late clears. Delivery watermarks are connection-local and disappear on teardown.
