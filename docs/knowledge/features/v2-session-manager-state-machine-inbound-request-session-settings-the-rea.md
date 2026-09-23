# Inbound `request_session_settings` (#491/#1214, extended #1586, conversation-keyed #1610, dormant reply #2449, confirmed permission mode #2510, effective effort #2516/#2517) — the read half of the #844 cluster

`request_session_settings` is a v2 **control** envelope (phone → binary),
intercepted in `dispatchAppFrame`'s discriminator switch **before**
`dispatch.Route` — same boundary as `set_session_settings` / `request_snapshot`
/ `request_debug_bundle`, no `dispatch.Route` handler. It consumes
[the read-side wire vocabulary](protocol-package-types-session-settings-read-payloads.md)
(`RequestSessionSettingsPayload` / `SessionSettingsPayload`) and answers with
the current run configuration: the session id a client must address a
`set_session_settings` to, stored model / effort, the current child's last
confirmed YOLO / permission mode when available, and the context-window
occupancy — **all seven saved/run fields describing the one session bound to
the conversation the client named** (#1610). An eighth, optional
`effective_effort` field reports Claude's independently observed applied value
without turning an inherited default into the stored `effort` choice (#2516).
Before #1610 the reported id
and the reported values could describe different sessions: the id followed
whichever conversation the request named, but the values were always the
shared **bootstrap** session's, so a client changing model/effort/YOLO for
conversation A silently reconfigured a background session it was not looking
at. See [codebase/1586.md](../codebase/1586.md) for the #1586 field addition
(#491's own predecessor ticket has no per-ticket file — it landed before that
convention).

**It deliberately shares no seam with `handleRequestSnapshot`.** A client used to
get these values off `screen_snapshot`'s side-load, but that reply was gated on
a live terminal: on the stream-json runner `Snapshotter` was nil by
construction (#1077/#1101), so `handleRequestSnapshot` short-circuited to
`server.binary_offline` and dragged the settings — which have nothing to do
with a terminal — down with it. #2540 deleted that render arm and the
`Snapshotter`/`SnapshotSettings`/`SnapshotUsage` seams entirely, so
`handleRequestSnapshot` now answers only the two errors and has no reply left
to drag anything down with. Saved fields still come through one
conversation-keyed `RunConfigFor` read on both runners. The separate `EffectiveEffortFor` provider remains optional at the
relay boundary. Production wires it only when both the conversation registry
and session pool exist; otherwise the existing reply is preserved with
`effective_effort` omitted.

Control flow, in load-bearing order:

1. **Capability gate and worker handoff.** `dispatchAppFrame` runs on `Run`.
   `if !s.interactive { return }` keeps a non-interactive conn fully inert: no
   queueing, decode, dependency call, or reply. An interactive frame is tagged
   `appFrameSessionSettingsRequest` and enqueued on that connection's existing
   `appFrameWorker`. `EffectiveEffortFor` may wait on a child round trip, so
   provider availability is deliberately *not* a dispatch gate: a nil provider
   still owes the complete saved-settings reply.
2. **Decode on the worker, tolerated at the payload boundary.** The handler
   re-decodes the immutable plaintext envelope to recover its correlation id.
   Failure is unreachable after `dispatchAppFrame` decoded the same bytes; it
   logs only `conn_id` and emits nothing because no id can be trusted. A bare or
   malformed payload still leaves `ConversationID == ""` and produces the
   historical zero-valued reply without consulting either dependency. The
   payload decode error is never echoed or logged.
3. **Resolve saved fields, or don't.** One conversation-keyed read replaces
   the pre-#1610 `addressable` boolean and the three bootstrap-scoped seam reads:

   ```go
   var cfg RunConfig
   if p.ConversationID != "" && m.cfg.RunConfigFor != nil {
       if got, ok := m.cfg.RunConfigFor(p.ConversationID); ok {
           cfg = got
       }
   }
   ```

   Three properties, each deliberate:

   - **The empty-id guard stays, and now means the opposite of what it meant
     before #1610.** It used to short-circuit *to* the bootstrap answer; now
     it short-circuits *to* the zero answer — a request naming no conversation
     names no session, so there is nothing for it to describe. Keeping the
     guard in `internal/relay` (rather than letting the seam refuse `""`)
     makes "an empty `conversation_id` addresses nothing" a property of this
     package alone, provable against any `RunConfigFor` double, and is the
     relay-side half of the #678 `Pool.Lookup("") == bootstrap` hazard the
     `cmd/pyry` producer separately guards.
   - **The nil-seam guard stays.** Foreground / v1 wire no `RunConfigFor`. Nil
     ⇒ nothing resolves ⇒ the zero reply — the same nil-seam fail-closed
     posture `handleRequestSnapshot` already has.
   - **The comma-ok is honoured, not discarded.** `cfg` is assigned only on
     `ok == true`. Writing `cfg, _ = m.cfg.RunConfigFor(...)` would happen to
     work today because the `cmd/pyry` producer zeroes its refusal return, but
     `RunConfigFor`'s doc forbids reading the fields on `ok == false`.
     Honouring it makes the relay fail closed on its own contract rather than
     on the producer's good behaviour — pinned by a **poisoned** refusal
     double in the unit tests (§ Test design).
4. **Read applied effort only after acceptance.** For an accepted `RunConfig`,
   a non-nil `EffectiveEffortFor` is called exactly once with the worker context
   and the same conversation id. `available == true` preserves either a string
   or explicit null; `available == false` ignores even a poisoned non-nil value.
   The relay adds no cache, so every fresh resolvable request performs a fresh
   provider call.
5. **Marshal + Run-owned reply.** All seven existing fields come only from
   `cfg`; the provider can populate only `EffectiveEffort`. `PermissionMode`
   and `YOLO` may be zero values even when stored fields and session id are
   present; that is the defined unavailable posture, not a partial marshal.
   The worker builds an unsealed correlated reply and passes it through
   `forwardToRun`; `forwardAppReply` performs Noise sealing on `Run`. No reject
   branch logs the caller's conversation id or any settings value.

- **`RunConfigFor func(conversationID string) (RunConfig, bool)` — the sole
  source of every saved/run field (#1609 built it, #1610 wires it in).** One call
  resolves a *named* conversation to its own bound session id, stored model /
  effort, confirmed YOLO / permission mode when available, and context-window
  figures together, with a comma-ok for "not addressable". `cmd/pyry` composes
  it (`runConfigFor`, layering `resolveBoundRunSettings` over the conversations
  registry and exact-id pool reads with the by-id `snapshotUsageFor`
  context-window reader). On a live hit, only that exact session's runner can
  supply `ConfirmedPermissionMode`; a lifecycle race degrades the pair to
  unavailable rather than falling back to settings, argv, or another runner.
  **Since #2449, a resolved id that the pool does not hold live is not
  automatically a refusal.** `resolveBoundRunSettings` tries `Pool.SettingsFor`
  first and, on a miss, `Pool.DormantSettingsFor` — the dormant registry entry
  #2448 keeps across a restart, when `Pool.New` has materialised only the
  bootstrap. A dormant hit still reaches this handler as one ordinary
  `RunConfig`, `ok == true`: the seam's shape and this handler's control flow
  are unchanged, because the live/dormant distinction is resolved entirely on
  the `cmd/pyry` side of the seam (it rides an unexported `live` bool on
  `boundRunSettings` that `RunConfig` itself does not carry) and is spent
  there gating the context-usage read — see the field table below.
  See [`internal/relay/v2session_seams.go`](../../../internal/relay/v2session_seams.go)'s
  `RunConfig`/`RunConfigFor` doc comments, [codebase/1609.md](../codebase/1609.md)
  and [`Pool.SettingsFor`](sessions-package-key-types-pool-settingsfor.md) for
  `DormantSettingsFor`.
- **`EffectiveEffortFor func(context.Context, string) (*string, bool)` — one
  nullable applied scalar, not a second settings source.** A non-nil pointer
  with `true` produces a JSON string. A nil pointer with `true` produces
  explicit JSON null, meaning Claude confirmed that no effort parameter is
  applied. `false` means unavailable and requires omission, regardless of the
  pointer; a nil provider has the same omission posture. It receives only an id
  already accepted by `RunConfigFor`, must honor the worker context so manager
  shutdown releases it, and must not return a full Claude settings response
  across the `internal/relay` boundary. #2516 defines and consumes this seam.
  Production #2517 builds it only when both the conversation registry and
  session pool are available. Every call resolves the conversation's current
  registry binding and exact pool runner afresh through `resolveBoundRunner`,
  whose empty-binding guard prevents `Pool.Lookup("")` from selecting the
  bootstrap runner. The provider then asserts the narrow
  `effectiveEffortQuerier` capability and calls `QueryAppliedSettings` under a
  30-second timeout derived from the worker context; an earlier caller deadline
  or cancellation still wins. Only `AppliedSettings.Effort` crosses this seam:
  the applied model and every other child setting are discarded. Unknown,
  unbound, dormant, pool-missing, not-yet-started, unsupported, failed,
  cancelled, and timed-out reads all remain unavailable. The read starts,
  replaces, and rebinds no child and retains no runner for a later call, so a
  rebind is observed on the next request rather than leaking the predecessor's
  value.
- **`KnownConversation` and `BootstrapSessionID` are no longer read here.**
  `KnownConversation` (`func(conversationID string) bool`) is now consulted
  by both `handleRequestSnapshot` and `handleMCPStatusRequest` — this handler
  stopped calling it because a known-but-**unbound** conversation reads as
  addressable through a pure membership check, which would have leaked the
  bootstrap's values for exactly the case this ticket closes.
  `BootstrapSessionID func() string` is **deleted from `V2SessionConfig`
  entirely** rather than left wired and unread, closing the one remaining live
  route to a value AC #2 of #1610 forbids. At the time (#1610), `SnapshotSettings`
  / `SnapshotUsage` stayed on `V2SessionConfig`, bootstrap-scoped and unread by
  this handler — `handleRequestSnapshot`'s `screen_snapshot` side-load was
  still their only reader, and that path was already unreachable in production
  (`Snapshotter` was hardcoded `nil`). #2540 later deleted `SnapshotSettings`,
  `SnapshotUsage`, `Snapshotter` and the render arm that read them, closing the
  declaration along with the unreachable path.
- **The handler.** `handleRequestSessionSettings(ctx, s, plaintext)` runs on
  the addressed connection's `appFrameWorker`. Every ordinary decoded branch
  produces exactly one `session_settings` reply — never a `TypeError`:

  | Condition | Reply |
  |---|---|
  | Non-interactive conn | *(none — inert)* |
  | Decode failure / no payload / empty `conversation_id` | Zero-valued `session_settings` |
  | `RunConfigFor` nil (foreground / v1 / unwired) | Zero-valued `session_settings` |
  | Named `conversation_id`, `RunConfigFor` returns `ok == false` (unhosted, or bound to no session and no persisted record of one) | Zero-valued `session_settings` |
  | Accepted `RunConfig`; provider nil or returns `available == false` | All seven fields from that `RunConfig`; `effective_effort` omitted |
  | Accepted `RunConfig`; provider returns non-nil pointer with `available == true` | All seven fields from that `RunConfig`; `effective_effort` is the pointed-to string |
  | Accepted `RunConfig`; provider returns nil pointer with `available == true` | All seven fields from that `RunConfig`; `effective_effort: null` is present |
  | Named `conversation_id`, bound to a session the pool holds live | That session's id and stored model/effort, context usage, and its exact current child's confirmed permission pair; provider result follows the three rows above |
  | Named `conversation_id`, bound to a session the pool holds only as a dormant registry entry (#2449) | That entry's `session_id`, stored `model` and stored `effort`; `permission_mode: ""` / `yolo: false` and usage zero; if the session remains absent from the live pool at the provider's fresh lookup, `effective_effort` is omitted |

  `session_id: ""` is already the wire contract's defined "no session to
  address" answer, so the zero rows above are a real answer, not an error
  dressed up as one — the reply shape stays constant and a client parses one
  thing rather than branching on two. An unknown conversation and a
  known-but-unbound one now produce the **identical** zero reply, which is a
  strict disclosure improvement over the pre-#1610 `KnownConversation` check:
  that pure membership test distinguished the two *and* reported bootstrap
  values for the unbound case.

**Scope, after #1610, #2516, and #2517.** The reported id and every saved/run value
always describe the same session, because all seven come from the one
`RunConfig` `RunConfigFor` returns — a client can never read one session's
values and write to another via `set_session_settings`. `EffectiveEffortFor`
cannot replace `Effort`, mutate settings, alter permissions, or send an ordinary
user message; it adds only an effort display value observed from the exact live
child reached by a fresh resolution of the accepted conversation. It neither
persists an inherited level nor falls back to a retained reading. There is no
bootstrap-scoped fallback on this verb for any unresolvable request, including
an empty or stale binding; that route (the field gating *whether* the answer is
populated rather than *which* session it describes) was the defect #1610
closed.
`handleRequestSnapshot`'s `screen_snapshot` side-load used to be the one place
in the manager that still reported bootstrap-scoped settings/usage, out of
scope by design (see the seam bullet above); #2540 deleted that side-load
along with the render arm, so this verb is now the only place in the manager
that reports run configuration at all. #2449's dormant answer does not
reopen that route: it still names only the one session the conversation is
bound to, sourced from that session's own persisted registry entry, never the
bootstrap's. The relay deliberately has no live/dormant flag; provider
unavailability is expressed only by omitting the optional field.

**Security / log discipline.** `conversation_id` is untrusted network input,
a lookup key only: `RunConfigFor` must accept it before the same value can reach
`EffectiveEffortFor`. The `cmd/pyry` run-config producer resolves it against
the daemon's own registry and uses only the registry-owned bound id for live
settings, dormant settings, current-runner confirmation, and context reads.
The caller's string and both saved and applied values reach no log line, error
string, filesystem path, or reply field other than the typed settings values
the contract intentionally exposes. Handler logs carry only `conn_id`, a
static event name, and for the envelope-marshal helper the static reply type.
`TestV2Session_RequestSessionSettings_IgnoresAnyPayload` probes with a
`"../../etc/passwd"`-shaped value under an unrelated key to pin that a
non-`conversation_id` field can never select another session's data.

**Test design — count the seam actually consulted, poison every refusal, and
assert JSON presence separately from value.** `countingReadSeams`
(`internal/relay/v2session_settings_read_test.go`) wires `RunConfigFor` to
non-zero, conversation-keyed fixture values for a known bound conversation
and, for **every other** id, to a **poisoned** refusal — a distinct marker
session id and non-default model/effort/YOLO alongside `ok == false`, not the
zero value. Without the poison, a handler that discarded the comma-ok would
still pass every row, because the `cmd/pyry` producer happens to zero its
refusal return; the poison makes "fail closed on `ok == false`" a tested
property of `internal/relay` itself. `readCounts.runConfigCalls` (`resolves`)
replaces the pre-#1610 `KnownConversation` counter (`wantLookups`), which
would read zero unconditionally once this handler stopped calling it — the
exact vacuous-counter trap `TestV2Session_RequestSessionSettings_NonInteractiveMakesNoLookup`
exists to close, now re-pointed at `RunConfigFor`. A second counter,
`bootstrapReads` (the retired `settings` + `usage` fixture calls), used to be
kept in the harness purely as a leak detector, wanted **0 on every row** and
red under exactly one mutation class: a re-introduced bootstrap-scoped read.
\#2540 deleted the `SnapshotSettings`/`SnapshotUsage` fields those fixtures
existed to poison, so `bootstrapReads` and the `bootstrapRead*` constants are
gone too — with the fields deleted, the compiler enforces what the counter
guarded, and a zero-read assertion on a seam that cannot exist would be
vacuous. `_ConversationGate`'s five-row table is the mutation-coverage matrix
for this seam: dropping the empty-id guard reddens the unnamed rows on
`resolves`; dropping the nil-seam guard panics the nil-seam row; discarding
the comma-ok reddens the unhosted row's payload via the poison. The
effective-effort rows apply the same technique to the availability bit: a
non-nil poisoned pointer with `available == false` must still leave the key
absent. Two requests for one conversation return call-specific values and
count two provider calls, so an accidental relay cache cannot stay green.

Wire assertions must inspect raw payload JSON for the `effective_effort` key:
decoding alone collapses omission and explicit null to the same nullable Go
value. Conversely, a decoded present `protocol.NullableString` contains a
pointer, so direct struct equality against a separately constructed present
value compares pointer identity and fails even when the strings match. Compare
`Value()` semantics, then zero the field before comparing the remaining
`SessionSettingsPayload`. Keep the saved `Effort` deliberately different from
the applied value so a provider that overwrites the persisted choice cannot
pass. The blocking two-connection test proves provider A cannot stall provider
B or cross-correlate replies; cancellation waits on the provider's received
context and proves that no late reply is sealed. Mutation and ordinary-message
spies keep this read path read-only.

**Concurrency.** No new goroutine, channel, lock, or shared cache was added.
Each open connection already owns one FIFO `appFrameWorker`; both
`RunConfigFor` and the possibly blocking `EffectiveEffortFor` run synchronously
there. A blocked provider delays only later frames for that connection, while
`Run` and every other connection worker remain serviceable. Moving
`RunConfigFor` off `Run` means different connections may now call it
concurrently; its production registry, pool, permission-confirmation, and
usage readers synchronize their own state, and test doubles must do the same.

The provider receives the manager's Run-derived context. A compliant provider
returns on cancellation; `forwardToRun` also selects on that context and on
`s.done`, so shutdown or connection teardown drops a pending unsealed reply.
`forwardAppReply` remains the only step that touches the Noise send cipher, on
the single-owner `Run` goroutine. The `cmd/pyry` producer takes its registry
and pool read locks sequentially, never nested. A lifecycle transition between
its snapshots can make permission or applied effort unavailable, but cannot
make saved fields describe another session.
