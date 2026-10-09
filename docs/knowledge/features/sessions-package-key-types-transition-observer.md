# Transition observer (#659)

The injectable, in-process signal a `cmd/pyry`-side consumer (#657) wires to map
legacy session boundaries onto the v2 `session_transition` wire event, while
retaining richer internal lifecycle facts — **without**
`internal/sessions` importing `internal/protocol` / `internal/relay` (the import
cycle that forces the producer to the `cmd/pyry` boundary). All the machinery
lives in `transition.go`.

**Update-only, not an ID-discovery feed.** Creation (`sessions.New`,
`Pool.Mint` / `CreateIn`, `GetOrCreateIn`) emits no transition. Equal-ID/no-op
and refused mutations, daemon shutdown, crashes retaining the session ID and
removal teardown also emit no session-change fact. `Pool.RotateID` remains a
silent compatibility primitive. Listening alone therefore cannot discover a
fresh conversation's session ID.

Successful `Pool.RotateBootstrapForSelfHeal` emits an internal recovery fact,
and the runtime history consumer records a shown divider without a legacy wire
delimiter. Self-heal remains uncalled in production; it adds no automatic
recovery policy. Workspace change is lifecycle vocabulary only, with no producer.

Clients discover or re-read IDs through
[`request_session_settings`](../../protocol-mobile.md#request_session_settings):
supply the named conversation's `conversation_id`, then read
`session_settings.session_id`. An empty ID means no addressable session; session
settings remain read-only. `handleRequestSessionSettings` uses `RunConfigFor`,
whose production source, `resolveBoundRunSettings`, resolves that conversation's
live or persisted dormant binding rather than a shared bootstrap fallback.
See the [wire contract](../../protocol-mobile.md#session_transition).

```go
type TransitionReason string // legacy delimiter vocabulary
type LifecycleCause string  // distinct internal causes; empty means unknown

type SessionTransition struct {
    PreviousID          SessionID
    NewID               SessionID // empty for eviction (no successor)
    Reason              TransitionReason
    OccurredAt          time.Time // nonzero UTC occurrence time
    AgentSwitch         bool      // dedicated committed-switch publication
    Cause               LifecycleCause
    ConversationID      string
    PreviousAgent       string
    NextAgent           string
    ResetHandoffOutcome *string
}

type SwitchTransitionMetadata struct {
    ConversationID string
    PreviousAgent  string
    NextAgent      string
}

type TransitionObserver func(SessionTransition)

func (p *Pool) SetTransitionObserver(obs TransitionObserver)
func (p *Pool) SetSwitchTransitionPublisher(publish func(SessionTransition))
func (p *Pool) RotateForNewSessionWithHandoff(oldID SessionID, outcome *string) (SessionID, error)
func (p *Pool) PublishSwitchTransition(oldID, newID SessionID, metadata ...SwitchTransitionMetadata)
```

**Internal causes and legacy delimiters are separate contracts.** The reason
and `AgentSwitch` flag retain their existing meanings; both observer setters
and existing rotation signatures remain compatible. The daemon's
`toWirePayload` maps only `ReasonClear` and `ReasonEviction`, rejecting empty
or unknown reasons before legacy history append or wire fan-out. Runtime
history-only dividers and predecessor closure use `Cause` independently; see
[runtime history boundaries](history-package-producers-runtime-lifecycle.md#runtime-boundaries-and-main-work-closure-3013).

| Lifecycle cause | Producer | Legacy reason → wire delimiter |
|---|---|---|
| `CauseOperatorReset` (`operator_reset`) | `RotateForNewSession` / `RotateForNewSessionWithHandoff` | `ReasonClear` → `clear` |
| `CauseClaudeClear` (`claude_clear`) | `AdoptAnnouncedID` | `ReasonClear` → `clear` |
| `CauseAgentSwitch` (`agent_switch`) | `PublishSwitchTransition` | `ReasonClear`, `AgentSwitch: true` → `clear` |
| `CauseRecovery` (`recovery`) | `RotateBootstrapForSelfHeal` | Empty reason; no legacy delimiter; shown runtime divider |
| `CauseWorkspaceChange` (`workspace_change`) | None | No event produced |
| `CauseIdleSleep` (`idle_sleep`) | `Session.runActive` idle timer | `ReasonEviction` → `idle_evict` |
| `CauseCapacityEviction` (`capacity_eviction`) | `Session.runActive` cap signal | `ReasonEviction` → `idle_evict` |

**Captured provenance, with explicit unknowns.** `ConversationID` is captured
from the current binding at rotation or eviction, or supplied by the caller
that committed a switch. Empty ownership and agent fields mean unknown;
never fill them from bootstrap, labels, historical session lookup or an
agent/session ID. Empty session IDs mean an absent prior or successor session.
`OccurredAt` records the rotation mutation, eviction decision or switch
publication in UTC, rather than later consumer processing time.

The daemon's legacy emitter fills missing exact-session agent facts at the
observer/publication handoff, before delayed delivery. Captured ownership remains
authoritative for history and live routing; missing source facts stay untagged.
Clear provenance names the successor, while eviction provenance names the
evicted session. See [legacy transition provenance](history-package-producers.md#legacy-transition-provenance-2982).

`RotateForNewSession` delegates to `RotateForNewSessionWithHandoff` with nil.
The additive method copies an optional caller-supplied outcome string so later
caller mutation cannot rewrite the fact. `ResetHandoffOutcome` is a
classification, never handoff text; nil means unknown. The daemon's
`activeSessionStarter.resetThenRotate` passes its actual wrap-up outcome as
`written` or `skipped`. The runtime divider retains only these known outcomes;
callers without the observation leave it absent, never pending or assumed.
Runtime history closes predecessor main work before dividers; daemon-start
reconciliation remains [#3014](https://github.com/pyrycode/pyrycode/issues/3014)
under [ADR 042](../decisions/042-daemon-built-thread.md).

**Func type, not interface.** Matches the package's closure-injection precedent
(`RunnerConfig.AdoptAnnouncedReset`).

**Post-construction setter, set-once-before-`Run`.** The pool is built via
`sessions.New` in `cmd/pyry.main`; the consumer/emitter (#657) comes up
later (`startRelay`), so the observer cannot be a `Config` field. `SetTransitionObserver`
writes `Pool.transitionObserver`; the field is then **read-only**, read lock-free
by the per-session lifecycle goroutines and the runners they start via `Run`'s
goroutine-start happens-before edge — the same "read-only after New" convention as
`convReg` / `activeCap`. A set-after-`Run` call is a programming error the race
detector flags. `nil` (the zero value) disables signalling.

**Binding mutation precedes notification.** Rotation producers rekey under
`Pool.mu`, then `rotationTransitionLocked` calls
`Registry.RebindSessionOwner` under that same pool lock to capture the owner,
move `CurrentSessionID` and append the retired ID to `SessionHistory`.
Lock order is pool then registry; `rekeyLocked` releases `Session.lcMu`
before registry access. Conversation persistence and callback fan-out run
after unlocking. Pool registry persistence retains its existing locked save;
either save failing is best effort and cannot suppress a successful in-memory
change. `notifyTransition` only delivers the completed fact, without rebinding.

Rebinding at notification time would let delayed reset prompt composition put
an A→B notification behind B→C and leave a stale binding or capture a late
foreign owner. Serializing binding updates with rekey leaves the binding at C
and both facts retain the original conversation and their own session pairs,
even when delivery order differs from mutation order. A miss stays unowned
through later delivery and skips conversation persistence. See
[the binding flow](conversation-session-binding.md#maintaining-the-binding-across-rotation-739)
and [the deterministic test](sessions-package-testing.md#lifecycle-fact-provenance-and-persistence).

**Current notification paths, with callbacks off all pool/session/capacity locks:**

- **Operator reset and Claude clear** — `RotateForNewSession` captures one
  operator-reset fact and recomposes the prompt before fan-out;
  `AdoptAnnouncedID` captures one Claude-clear fact. Equal-ID announcements
  and refused rotations fire nothing. Both retain `ReasonClear`. The fsnotify
  watcher and `onRotate` were retired by #2137; neither is a current source.
  See [announced reset](sessions-package-key-types-adoptannouncedid.md).
- **Agent switch** — `conversationAgentSwitcher.Switch` supplies the known
  conversation and actual previous/target agents through
  `SwitchTransitionMetadata`, retaining them across old-session removal.
  `PublishSwitchTransition` delivers one committed-switch fact after inactive
  reset status, including when post-commit cleanup fails, without another
  rebind or save. Existing two-argument callers leave metadata unknown even
  if the pool could look it up; empty/equal session pairs emit nothing.
  The ordinary observer skips duplicate switch enqueue;
  `SetSwitchTransitionPublisher` owns the single ordered outcome through the
  runtime drain's daemon-cancellable boundary lane, including predecessor
  closure before the divider. Compatibility installations retain the emitter
  lane. Only that dedicated publisher may wait; ordinary observers must return
  without waiting.
- **Recovery** — successful `RotateBootstrapForSelfHeal` captures and rebinds
  the owner like other rotations, then notifies internally with `CauseRecovery`
  and an empty reason. Runtime history closes predecessor work and records a
  shown divider; legacy consumers produce no wire delimiter. There is no
  production caller.
- **Idle sleep and capacity eviction** — `Session.beginEvict` calls
  `notifyEviction` before the lifecycle state flip and child teardown.
  It captures the session ID, membership and current owner under the pool lock
  without rebinding; `NewID` stays empty and the legacy reason stays
  `ReasonEviction`. The idle timer waits until `Config.TurnBusy` permits sleep;
  cap eviction can force teardown mid-turn. Removed entries are suppressed so
  teardown cannot duplicate a switch boundary. Spontaneous exit and shutdown
  remain silent. `Session.endEvict` persists after the child stops.

**Synchronous, no new goroutine.** Fires run on the goroutine that already owns
the transition (lifecycle for eviction; the runner's parse goroutine or the
control-plane caller for clear) — a per-fire
goroutine would add goroutines to paths that deliberately have none and could
reorder signals. The non-blocking burden is therefore the observer's: the
`TransitionObserver` contract documents "MUST NOT block — hand off to a buffered
channel"; #657 owns the non-blocking impl. See [codebase/659.md](../codebase/659.md).

The compatibility ordinary 16-entry drop-on-full queue is unsuitable for committed switches:
a dropped signal would leave a changed binding with no transition, history
boundary or row update. The switch publisher waits through consumer delay and
Run-owned sealing before reset exclusion releases, so queue pressure delays
publication without discarding it. Increasing the ordinary buffer would only
move the failure threshold. The two paths consume one switch signal without
duplicating history or fanout; `TestRelayAgentSwitchPublicationQueuePressure`
fills the ordinary queue before commitment and checks the one durable boundary
and ordered row. See [switch publication](conversation-session-binding.md#switching-to-the-other-agent-2672)
and [FIFO push completion](v2-session-manager-concurrency.md).

Runtime-enabled installations retain each ordinary fact in the stream sink
behind its predecessor's accepted-output watermark, with a coalescing wake
rather than drop-on-full transition transport. Callbacks still do no publication
I/O and do not wait for the drain or post gate. Committed switches wait through
closure, divider/legacy publication, row publication and transport sealing.
Evictions wait for the captured producer incarnation's consumed stop and late
parsed tails; routing IDs alone are insufficient because reactivation reuses
them. See [drain retirement](streamsup-package-draining-turnevents-into-the-interactive-emitter.md)
for source registration before output and retained delivery holds.

**This pattern does not transfer to every pool setter — check whose goroutines
read the field, not which precedent the setter resembles.** #2148's
`Pool.SetClientIdentityResolver` first copied this contract verbatim (plain
field, set-once-before-`Run`) on the reasoning that its readers are also
`Activate`'s callers. They are not: the resolver is read by goroutines the
*relay's* v2 manager spawns (a per-conn `appFrameWorker` on every handshake),
and `startRelayV2` starts that manager's `Run` before it installs anything —
unlike `SetTransitionObserver`'s readers, which are exclusively `Pool.Run`'s
own descendants and therefore provably created after `startRelay` returns. A
conn completing its handshake in that window would read the field concurrently
with the install's write. `Pool.clientIdentity` is an `atomic.Pointer` instead.
The general form: this setter shape is race-free only when every reader
goroutine is a descendant of the *same* `Run` the install precedes — verify
that per field, since two setters that look identical can differ in exactly
this way.

## Confirmed runner stop: `Config.OnRunnerStopped`

`sessions.Config.OnRunnerStopped func(SessionID)` is optional and fixed at
`Pool.New`. `Session.runActive` invokes it after **each** `Runner.Run` returns,
without pool/session locks, before sending the run result that permits eviction
completion or reactivation. It must not block. This callback confirms producer
shutdown; `ReasonEviction` only announces the earlier request, before
cancellation/join. An empty event queue at that request cannot prove shutdown:
the old producer can still append a late tail. A real completion can also arrive
while eviction remains pending, so completion alone cannot retire that hold.

`runSupervisor` routes the callback to `streamTurnSink.runnerStopped`, which
retains a stamped boundary after all successfully queued preceding output.
This also covers a child exit already offered before the eviction request;
waiting only for a newer child exit would strand the hold when no child remains
to produce one. The callback returns without waiting for queue capacity,
history or broadcaster I/O; the stream drain applies the publication close.
See [confirmed teardown publication](streamsup-package-per-conversation-turn-busy-track-session-teardown-clear.md)
and [retained exit transport](streamsup-package-per-conversation-turn-busy-track-exit-lane-on-the-turn-busy-fan.md).
