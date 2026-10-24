# Capability negotiation on the handshake (#626) — `negotiateCapabilities` + `s.interactive` + capability-aware `ActiveConns`

The daemon-side **trust decision** [#607](../codebase/607.md) deferred. #607 landed the wire vocabulary (`CapabilityInteractive`, the `omitempty` `Capabilities []string` field on both `HelloClientPayload` and `HelloAckPayload`) but `handleNoiseInit` **decoded the phone's advertised capabilities and ignored them** — the `hello_ack` echoed nothing, the session recorded no capability state, and `ActiveConnIDs` returned every open conn with no capability filter. This slice closes that boundary; it is `security-sensitive` (the daemon deciding which internet-facing phones are *granted* the interactive capability) and is the enforcement half of ADR 025's deliberately split design: #607 = vocabulary (no trust), #626 = trust decision. See [`codebase/626.md`](../codebase/626.md).

**Authoritative negotiation and mandatory admission.** `negotiateCapabilities`
intersects the advertisement with `supportedV2Capabilities` in supported-set
order, deduplicates values and drops unknown strings. It separately grants
`thread`, hosted apps, expansion and compact only with their supplied readiness.
The client cannot authorize itself by advertising an unfamiliar name.

`handleNoiseInit` derives the thread refusal after `Devices.Validate` accepts
the token/static key and existing client-version policy accepts the hello, but
before `BindStaticKey`, workspace-root selection, redemption, client-version
persistence or opening. Missing `thread` receives nonretryable
`client.update_required`/4412 without a minimum-version field; advertised but
ungrantable `thread` receives nonretryable `protocol.unsupported`/4421, including
nil/false `ThreadReady`. Unknown/expired tokens and mismatched keys always retain
indistinguishable `auth.invalid_token`/4401 precedence, independent of capability
advertisement/readiness; version refusals keep their existing payloads and win
before thread refusals. Atomic binding still decides competing installs only
after these checks. Ordinary authentication `LastSeenAt` bookkeeping remains.

**Echo is distinct from admission.** The same `negotiated` slice supplies the
ack and accepted session flags. Refusals first complete Noise with a correlated
`hello_ack` in `noise_resp`, omitting `workspace_root`, then send one routing
envelope containing the correlated sealed error and close. Echoed capabilities
on a rejected ack confer no authority: the session never opens, starts dispatch
or acquires a push route. `TestV2Session_ThreadAdmission` checks the two-envelope
sequence, privacy and unchanged pairing admission fields in memory and on disk;
`TestV2Session_ThreadAdmissionAuthPrecedence` and
`TestV2Session_ThreadAdmissionVersionPrecedence` pin the earlier verdicts.
See the [wire handshake](../../protocol-mobile.md#handshake).

**Optional grants stay independent.** Accepted sessions always record
`s.thread`, but `s.interactive` remains
`slices.Contains(negotiated, protocol.CapabilityInteractive)`, never a nonempty-set
check. Mandatory thread makes that distinction especially important: every open
connection now has at least one grant, including peers without interactive access.
Expansion, compact and multi-agent likewise retain value-specific gates;
`handleActiveConns` enumerates only `V2StateOpen`. Rekey preserves admitted flags
rather than renegotiating them. Positive fixtures must provide genuine readiness,
advertise thread and check its echo; negative helpers preserve exact omissions
and nil/false readiness. Noise completion alone cannot prove supported admission:
held-response tests use low-level handshakes until release, then await an open
manager snapshot. See [test boundaries](development-verification-test-execution-and-artifact-survival.md#diagnosing-reply-timeouts).

**`ActiveConns`/`ActiveConn` — the capability-aware enumeration.** The downstream consumer reads the negotiated flag via the [widened snapshot funnel](#concurrency-safe-open-session-enumeration-588--activeconns-method--snapshot-funnel): `ActiveConns(ctx) []ActiveConn` returns each open conn paired with its `Interactive` flag. The #596 structured-stream fan-out (since [#699](../codebase/699.md) the **single** v2 assistant-turn delivery path — #589's coarse `message` broadcast, re-targeted to non-interactive conns in [#634](../codebase/634.md), was deleted as dead code once the 2026-06-22 ADR 025 amendment made every phone `interactive`; the `Interactive` flag survives because the structured path still gates on it) consumes this: [`#632`](../codebase/632.md)'s `interactiveTurnEmitterV2` (`cmd/pyry/interactive_turn_v2.go`) snapshots `ActiveConns`, filters `Interactive == true` (the load-bearing capability gate, `if !c.Interactive { continue }`), then `Push`es a sealed structured envelope per conn-id — the emitter is built and unit-tested, with #633 wiring it to the live producer. [`#657`](../codebase/657.md)'s `sessionTransitionEmitterV2` (`cmd/pyry/session_transition_v2.go`) is the **second** interactive-only consumer of this exact primitive: it reuses the same `interactiveBroadcaster` interface and `if !c.Interactive { continue }` gate to fan a `session_transition` envelope (a `/clear` rotation or idle/cap eviction surfaced by [#659]'s pool observer) to interactive phones — but stamps **no** `EventID` (a session boundary is not a turn-stream event, so it does not join the #647 ring) and adds no manager-side path. The former #722 queue snapshot emitter and its hand-off goroutine are retired
for all apps (#3727). [`#1008`](../codebase/1008.md)'s `sessionErrorEmitterV2` (`cmd/pyry/session_error_v2.go`) also consumes this enumeration: it fans a `session_error` envelope (terminal `CodeSessionBlocked`, `SessionErrorPayload{ConversationID,Code,Message}` per [#1007](../codebase/1007.md)) to interactive phones whenever [#1000](../codebase/1000.md)'s `msgqueue` give-up seam (`OnGiveUp`) fires — through the same reused interface + gate, likewise `EventID`-nil (a give-up is a one-shot edge with no connect-time reconcile, unlike a replay-eligible event). It retains the hand-off-channel + dedicated-Run-goroutine shape, **without a queue reference**: the emitter holds no `Snapshot`/`*msgqueue.Queue` field, so queued (untrusted) phone text is unreachable by construction rather than by discipline. #2209's `workspaceUpdateEmitterV2` (`cmd/pyry/workspace_update_v2.go`) is the **fifth**, fanning a `workspace_updated` record to every other interactive conn once `rename_workspace` (#2207) has stored a label change — and the first of the five triggered directly from a dispatch handler rather than a pool/queue observer. That distinction is what decides whether a hand-off channel is needed, and it is not: `rename_workspace` is map-dispatched, so `dispatchAppFrame` falls through to `enqueueAppFrame` and `announce` runs synchronously on the requesting conn's own `appFrameWorker` goroutine — never on `Run`. A bare `ActiveConns` call is therefore safe despite `ActiveConns`' own doc comment reading, out of context, as though any non-`Run` goroutine needs care ("safe to call from any goroutine other than the dispatch goroutine"): the snapshot request funnels onto `Run` and `Run` is always free to service it while the handler waits. `session_error` needs its hand-off-channel-plus-dedicated-goroutine shape because its `OnGiveUp` trigger fires from a mix of goroutines that sometimes includes `Run` itself, not because calling `ActiveConns` off-`Run` is ever unsafe on its own — a future handler-triggered emitter can call it inline the way this one does. `workspaceUpdateEmitterV2` also adds a shape none of the first four needed: it excludes one conn from its own fan-out, keyed on `ActiveConn.ConnID` against the requester's `dispatch.Conn.ConnID()` (one string space, no mapping) — the requester already holds its correlated reply, but a requester with a second conn open is still pushed on that one, since exclusion is per conn and not per device. The single-`Interactive`-bool shape held while `supportedV2Capabilities` had one member (YAGNI); #2020 added a second, `CapabilityQuestion = "question"`, as the deliberate, separately-reviewed change this always required, and #2172 added a third, `CapabilityModelList = "model_list"` (detecting the pair of #2124's daemon-wide model-list fallback and #2125's `request_model_list` verb — one string covers both, since neither is separately useful without the other). Both confirm the shape still holds without a new bool. `question` and `model_list` are both **detection only**: neither grants a gate of its own, and `reconcileQuestions`, `handleRequestModelList`/`modelListFor`, and the four #632-shaped emitters above all stay gated on `interactive` alone, so a phone advertising either alongside required `thread` but without `interactive` negotiates as non-interactive and gets none of them. The one property that had been a hypothetical fragility is now load-bearing and test-pinned: `s.interactive` must stay `slices.Contains(negotiated, protocol.CapabilityInteractive)`, a **value-specific** membership test, never `len(negotiated) > 0` — with more than one capability in the supported set, an emptiness check would grant interactive access to a client that advertised only the others. #2020's `question_alone_grants_no_interactive` and #2172's `model_list_alone_grants_no_interactive` subtests each assert `ActiveConns(...).Interactive == false` for their case and are, by mutation, the only rows in either handshake test table that die if `Contains` is weakened to a length check.

`CapabilityContextUsage = "context_usage"` (#2431) joined the supported set as a fourth, detection-only member the same way `question` and `model_list` did — its verb, `request_context_usage`, gates on `interactive` alone.

**`CapabilityMultiAgent = "multi_agent"` (#2643) is the fifth member, and the first that is not pure detection.** Its neighbours only let a client tell which daemon build it is talking to; this one changes what the daemon sends. `V2Session` gained a second per-conn bool beside `interactive`, `multiAgent`, set in the same token-OK tail via the same value-specific `s.multiAgent = slices.Contains(negotiated, protocol.CapabilityMultiAgent)` — never a length check, for the reason above. `routeAppFrame` copies it onto the per-frame `*dispatch.Conn` with `conn.SetMultiAgent(s.multiAgent)` before `Route` runs, so `internal/relay/handlers.ListConversationsWithAgents` (and #2644/#2645's handlers) read the decision as `c.MultiAgent()` without reaching back into the session — see [`dispatch-package.md`](dispatch-package.md). Re-key preserves `s.multiAgent` by never touching it, the same as `interactive` and `device`/`peerStatic`; `TestV2Session_MultiAgent_ReachesHandlerAndSurvivesRekey` pins the flag across a same-static re-key. It still grants **no** interactive access — no `interactive` gate reads it, and `multi agent alone grants no interactive` in `TestV2Session_Handshake_CapabilityNegotiation` pins that — but unlike `question`/`model_list`/`context_usage`, a client that negotiates `thread` and `multi_agent` without `interactive` still gets a *shaped* `conversations` reply (every row carries `agent`, and Codex rows are included), because that verb's own gate is `c.MultiAgent()`, not `s.interactive`. The two flags are independent booleans on both `V2Session` and `Conn`, not one capability implying the other.

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

**#2644 gives `multi_agent` a second gate, at the push choke point rather than a reply.** `forwardEnvelope` — the one seal path every pushed frame reaches (the push-queue drain, reconnect replay, the resync marker) — calls `withheldFromConn(s, env)` (`v2session_agentgate.go`) before sealing; a conn without `s.multiAgent` gets nothing for a frame whose `pushedConversationID` resolves, through the reused resolution rule (`handlers.agentOf` exported to `AgentOf` for this second caller), to a Codex-bound conversation. Ordinary handler replies (`forwardAppReply`) retain their existing exemption, so a client that already learned a Codex conversation id from an accepted request still gets ordinary replies naming it. Thread item updates and continuations are gated independently at both sealing boundaries, even when correlated; see [thread delivery and summary projection](v2-session-manager-state-machine-capability-negotiation-on-the-handshake-thread-delivery-and-summary-projection.md#thread-delivery-and-summary-projection).

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
caller's complete readiness attestation. Production also supplies its `apps.Owner`
as both resource providers and caches `Owner.CheckRuntime` before message
processing; readiness requires the registry, owner, cancellable delivery and a
supported runtime. Empty inventory or stopped app health does not revoke host
infrastructure support; per-app health still gates resource access. See
[hosted-app wiring](../../protocol-mobile.md#hosted-apps-v2).
Ready in-process fakes exercise wiring without an environment bypass.
Hosted-app requests require neither `interactive` nor an active channel, but
the enclosing app session must first negotiate mandatory `thread`. The hosted
grant does not substitute for thread admission; every request/completion/delivery
also checks current paired membership. See the [wire gates](../../protocol-mobile.md#hosted-apps-v2).

An offered hosted capability initializes the ordered output lane on an otherwise
admitted thread session even when the hosted grant is withheld. Unsupported
hosted requests still need ordered sender IDs. Retained low-level legacy lane
paths preserve previously accepted transport output, but no-thread peers can no
longer reach them through admission. Neither lane selection nor a capability
echo authorizes discovery. See
[sender IDs and delivery ordering](v2-session-manager-concurrency-hosted-discovery-and-ordered-delivery.md#hosted-discovery-and-ordered-delivery)
and [transport admission](transport-package.md#lifecycle-and-goroutines).

### Thread delivery and summary projection

Split out (over the 50000-byte cap) to
[thread delivery and summary projection](v2-session-manager-state-machine-capability-negotiation-on-the-handshake-thread-delivery-and-summary-projection.md):
`ThreadReady`/expansion/compact capability readiness, projection-aware joins,
`threadWithheld`/`threadQueryGate` admission and eligibility (including the
compact live fence, #3574), bounded catch-up/page/tool-body/children queries,
summary projection and `ActiveConn.View`.

### Supplied live-state ordering

`startRelayV2` installs the [daemon retained-reading delivery path](streamsup-package-draining-turnevents-into-the-interactive-emitter-daemon-retained-live-state.md#relay-delivery):
`daemonRelayCatalogSnapshot` composes a finite `ThreadLiveState` cursor from
`daemonRelaySnapshot`'s other retained families and lazy catalog resolution for
captured registered targets, including those with no own reading or turn.
`startDaemonLivePublisher` supplies bounded `PushLiveState`
publication, and `installDaemonLiveRelay` wires all five source-bearing reply/
settings providers. Accepted producers hand off publication before legacy
enumeration or fan-in can refuse/delay delivery. Captured source/order fields survive adaptation;
original payload session fields and prompt answer IDs stay intact. This wiring
now participates in production `ThreadReady` alongside the installed fold
publisher and watermark provider. Negotiated thread connections receive current
readings at connect/reconnect and source-bound live updates; these snapshots
initialize transient state only. Thread item backfill requires explicit catch-up
for each open conversation. Production bounded catch-up/page providers are
installed; event replay/resync is retired for every connection (#3738).

Snapshot completion needs cursor exhaustion, not only `FlushPushes`: a finite
`ThreadLiveState` iterator can still be enumerating when the push queue reports
progress. Reconnect capacity fixtures retain generated clears alongside all
readings and hold transport capacity so enumeration cannot finish early. Generated
clears are distinct observations; decoding `{}` as a fresh menu or prompt would
fail before exercising its actual payload or eligibility gates.

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
its source-bound counterpart and bypass conversation checks. Retained legacy delivery branches are not fallback admission routes; their final
removal and integrated compatibility wording belong to #3741.

`LiveStateCurrent` checks owner generation and accepted reading revision before
unsolicited Run delivery, including captured cursors and queued pushes. Removing
owner state alone cannot invalidate a detached cursor; without this check it can
restore stale indicators after removal/recreation. An unfinished query reservation
does not invalidate prior accepted state. Dismissals use terminal ordering and
clears use current generation. Correlated replies instead preserve captured
evidence under relay ordering. `TestDaemonLiveRelayRetiredSnapshot` and
`TestDaemonLiveRelayReservedQuerySnapshot` cover those distinctions;
`TestLiveStateCapturedEligibility` pins detached eligibility.

Catalog freshness also includes accepted reply-only resolutions without retaining
their borrowed bytes. Another peer can supersede a reconnect catalog parked
behind its clear without publishing replacement inventory. `refreshLiveCatalog`
therefore resolves an overtaken unsolicited catalog once after access checks,
requires the same target and captured generation, and removes correlation before
ordinary validation, projection and freshness checks. A new generation or
removal/recreation still withholds the old capture; other families keep their
retained-reading checks. See [catalog reconciliation and the two-peer regression](v2-session-manager-state-machine-connect-time-model-list-reconcile-retain.md#supplied-thread-reconciliation).

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
