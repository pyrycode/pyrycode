# Inbound `request_session_settings` (#491/#1214, extended #1586, conversation-keyed #1610, dormant reply #2449, confirmed permission mode #2510) — the read half of the #844 cluster

`request_session_settings` is a v2 **control** envelope (phone → binary),
intercepted in `dispatchAppFrame`'s discriminator switch **before**
`dispatch.Route` — same boundary as `set_session_settings` / `request_snapshot`
/ `request_debug_bundle`, no `dispatch.Route` handler. It consumes
[the read-side wire vocabulary](protocol-package-types-session-settings-read-payloads.md)
(`RequestSessionSettingsPayload` / `SessionSettingsPayload`) and answers with
the current run configuration: the session id a client must address a
`set_session_settings` to, stored model / effort, the current child's last
confirmed YOLO / permission mode when available, and the context-window
occupancy — **all seven fields describing the one session bound to the
conversation the client named**
(#1610). Before #1610 the reported id
and the reported values could describe different sessions: the id followed
whichever conversation the request named, but the values were always the
shared **bootstrap** session's, so a client changing model/effort/YOLO for
conversation A silently reconfigured a background session it was not looking
at. See [codebase/1586.md](../codebase/1586.md) for the #1586 field addition
(#491's own predecessor ticket has no per-ticket file — it landed before that
convention).

**It deliberately reads none of the same seams `handleRequestSnapshot` uses to
render a screen.** A client used to get these values off `screen_snapshot`'s
side-load, but that reply is gated on a live terminal: on the stream-json
runner `Snapshotter` is nil by construction (#1077/#1101), so
`handleRequestSnapshot` short-circuits to `server.binary_offline` and drags the
settings — which have nothing to do with a terminal — down with it. This
handler consults one conversation-keyed seam, so it answers identically on
both runners.

Control flow, in load-bearing order — this verb never fails a request
outright; every branch produces the same reply shape:

1. **Capability gate.** `if !s.interactive { return }` — a non-interactive
   conn is fully inert: no decode, no resolution, no seam call, no reply.
   It must precede the steps below, so such a conn cannot learn whether a
   conversation or a session exists (`_NonInteractiveMakesNoLookup` is the
   test that pins the ordering, not just the no-reply half `_ReportsRunConfig`
   already covered — see § Test design below).
2. **Decode, tolerated.** `json.Unmarshal(env.Payload, &protocol.RequestSessionSettingsPayload{})`,
   tolerated on failure — a bare frame from an un-updated client carries a nil
   `Payload`, which `Unmarshal` rejects while leaving the id at `""`. There is
   no emptiness guard beyond what step 3 already needs, no error check on the
   `Unmarshal` return, and no malformed-payload reply branch; the decode error
   is never echoed or logged.
3. **Resolve, or don't.** One conversation-keyed read replaces the pre-#1610
   `addressable` boolean and the three bootstrap-scoped seam reads:

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
4. **Marshal + reply.** Unchanged shape, sourcing all seven fields from `cfg`.
   `PermissionMode` and `YOLO` may be their zero values even when the stored
   fields and session id are present; that is the defined unavailable posture,
   not a partial marshal.
   No new log call on any reject branch — this verb fires on every sheet
   open, so a per-reject line would log more than the write path, and the
   only thing it could add is the caller's untrusted conversation id.

- **`RunConfigFor func(conversationID string) (RunConfig, bool)` — the sole
  seam this handler consults (#1609 built it, #1610 wires it in).** One call
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
- **`KnownConversation` and `BootstrapSessionID` are no longer read here.**
  `KnownConversation` (`func(conversationID string) bool`) is now consulted
  **only** by `handleRequestSnapshot` — this handler stopped calling it
  because a known-but-**unbound** conversation reads as addressable through a
  pure membership check, which would have leaked the bootstrap's values for
  exactly the case this ticket closes. `BootstrapSessionID func() string` —
  along with the two seams it used to accompany here,
  `SnapshotSettings func() (model, effort string, yolo bool)` and
  `SnapshotUsage func() (usedTokens, windowTokens int)` — is **deleted from
  `V2SessionConfig` entirely** rather than left wired and unread, closing the
  one remaining live route to a value AC #2 of #1610 forbids. `SnapshotSettings`
  / `SnapshotUsage` themselves stay on `V2SessionConfig`, bootstrap-scoped,
  unchanged — `handleRequestSnapshot`'s `screen_snapshot` side-load is still
  their only reader, and that path is unreachable in production today
  (`Snapshotter` is hardcoded `nil`).
- **The handler.** `handleRequestSessionSettings(ctx, s, env)` runs on the
  single `Run` dispatch goroutine, like every other intercepted verb. Every
  branch produces exactly one `session_settings` reply — never a `TypeError`:

  | Condition | Reply |
  |---|---|
  | Non-interactive conn | *(none — inert)* |
  | Decode failure / no payload / empty `conversation_id` | Zero-valued `session_settings` |
  | `RunConfigFor` nil (foreground / v1 / unwired) | Zero-valued `session_settings` |
  | Named `conversation_id`, `RunConfigFor` returns `ok == false` (unhosted, or bound to no session and no persisted record of one) | Zero-valued `session_settings` |
  | Named `conversation_id`, bound to a session the pool holds live | That session's id and stored model/effort, context usage, and its exact current child's confirmed permission pair; before confirmation, the pair is `permission_mode: ""` / `yolo: false` |
  | Named `conversation_id`, bound to a session the pool holds only as a dormant registry entry (#2449) | That entry's `session_id`, stored `model` and stored `effort`; `permission_mode: ""` / `yolo: false` because there is no current child; `used_tokens` and `window_tokens` both zero |

  `session_id: ""` is already the wire contract's defined "no session to
  address" answer, so the zero rows above are a real answer, not an error
  dressed up as one — the reply shape stays constant and a client parses one
  thing rather than branching on two. An unknown conversation and a
  known-but-unbound one now produce the **identical** zero reply, which is a
  strict disclosure improvement over the pre-#1610 `KnownConversation` check:
  that pure membership test distinguished the two *and* reported bootstrap
  values for the unbound case.

**Scope, after #1610.** The reported id and the reported values always
describe the same session, because both come from the one `RunConfig`
`RunConfigFor` returns — a client can never read one session's values and
write to another via `set_session_settings`. There is no bootstrap-scoped
fallback on this verb for any unresolvable request; that route (the field
gating *whether* the answer is populated rather than *which* session it
describes) was the defect #1610 closed. `handleRequestSnapshot`'s
`screen_snapshot` side-load is the one place in the manager that still
reports bootstrap-scoped settings/usage, and it is out of scope by design
(see the seam bullet above). #2449's dormant answer does not reopen that
route: it still names only the one session the conversation is bound to,
sourced from that session's own persisted registry entry, never the
bootstrap's.

**Security / log discipline.** `conversation_id` is untrusted network input,
a lookup key only: it crosses to trusted only through `RunConfigFor`, whose
`cmd/pyry` producer resolves it against the daemon's own registry and
uses only the registry-owned bound id for the live settings, dormant settings,
current-runner confirmation, and context reads.
Nothing downstream of the seam ever holds the caller's string — it reaches no
log line, no error string, no filesystem path, and not the reply. The
handler's only logging is the pre-existing `Debug` (`conn_id` only) and the
defensive marshal-error `Warn` (`conn_id` only) — no reject-branch log was
added, matching the verb's existing "logs less than the write path" posture
for something that fires on every sheet open. `TestV2Session_RequestSessionSettings_IgnoresAnyPayload`
probes with a `"../../etc/passwd"`-shaped value under an unrelated key to pin
that a non-`conversation_id` field can never select another session's data.

**Test design — counting the seam the handler actually consults, and
poisoning its refusal.** `countingReadSeams`
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
`bootstrapReads` (the retired `settings` + `usage` fixture calls, kept in the
harness purely as leak detectors), stays wanted **0 on every row** and is red
under exactly one mutation class: a re-introduced bootstrap-scoped read.
`_ConversationGate`'s five-row table is the mutation-coverage matrix for this
seam: dropping the empty-id guard reddens the unnamed rows on `resolves`;
dropping the nil-seam guard panics the nil-seam row; discarding the comma-ok
reddens the unhosted row's payload via the poison; a re-added bootstrap read
reddens every addressable-adjacent row on both counters and the payload.

**Concurrency.** No new goroutine, channel, or lock — runs only on `Run`,
same as `handleRequestSnapshot`. The `cmd/pyry` producer takes its registry
and pool read locks sequentially, never nested. A live answer adds an exact-id
`Pool.Lookup` and the runner's concurrency-safe confirmation read after
`Pool.SettingsFor`; a dormant answer uses `DormantSettingsFor` instead. A
lifecycle transition between those snapshots can make the permission pair
unavailable, but cannot make it describe another session.
