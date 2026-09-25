# `Runner` interface + `RunnerFactory` (#1077, corrected #1580 for #1348 fallout, widened #2042, `RevokeBypass` retired #2043, widened #2064/#2280/#1513/#2592)

`Session.sup` is typed `Runner` (`internal/sessions/runner.go`):

```go
type Runner interface {
    State() State
    WriteUserTurn(ctx context.Context, conversationID string, payload []byte) error
    WaitForPTY(ctx context.Context) error
    Run(ctx context.Context) error
    Restart(args []string)
    SetSpawnArgs(args []string) // #1580 — installs the NEXT spawn's argv, no kill
    SetModel(model string) error // #2280 — switches the LIVE child's model by control request, no turn or kill
    SetPermissionMode(mode string) error // #2042 — switches the LIVE child to a caller-named mode, no kill
    SetSpawnPermissionMode(mode string) // #2064 — installs the NEXT spawn's posture write, no kill, no live effect
    BeginTeardown() // #1513 — arms the write-refusal gate ahead of a deliberate kill; released by any successor child's bind
    Interrupt() error // #2592 — ends the live child's running turn without killing it
    RestartFresh(newID string) // #2592 — rotates the persistent session id and respawns under it
    BeginRotation() func() // #2592 — arms the rotation write-refusal gate ahead of a new_session rotation; disarm is never nil
}

type RunnerFactory func(cfg RunnerConfig) (Runner, error)
```

**`RunnerConfig.Harness` (#2593) is what a second `RunnerFactory` implementation is selected by.** Every `RunnerConfig` the pool builds carries a canonical (never-empty) harness — `HarnessClaude` for the bootstrap and every minted session, a dormant entry's own persisted value for a revive (see [sessions-registry.md § `harness`](sessions-registry.md)). `internal/sessions` does not validate it; it only carries it, construction-fixed, through to whichever `RunnerFactory` the daemon wired in. The daemon's own factory, `harnessRunnerFactory` (`cmd/pyry/main.go`), wraps `newStreamRunnerFactory` and is the actual decision point: `""`/`HarnessClaude` reach the stream runner, anything else is refused as an ordinary construction error without calling it — so reviving a dormant non-`claude` session fails before a claude runner is ever built, and the entry stays dormant with its harness intact. A `RunnerConfig` built outside the pool (a test double) accepts `""` the same way, so existing fixtures that never set the field keep working. Today `claude` is the only harness any path mints; the seam exists for Codex (#2585) to plug in a second factory without touching this one.

**`Interrupt`, `RestartFresh` and `BeginRotation` moved onto this interface in #2592, closing the last fail-open
capability assertions cmd/pyry reached off `Session.Runner()`.** With exactly one production implementation
(`streamRunner`, which already had all three) the assertions were speculative-surface avoidance; a second
runner implementation (Codex, #2585) turns that into a real hazard, because an assertion that doesn't match is
a silent no-op — a Codex runner missing `Interrupt` would drop the keystroke while `v2.interrupt.dispatched`
still logged a dispatch. The widening deletes the inert arms outright rather than leaving them reachable:
`armNone` and the `v2.interrupt.no_actuator` record (`cmd/pyry/main.go`'s `activeInterrupter.SendEsc`), the
`v2.new_session.no_restart` record (`activeSessionStarter.StartNewSession`), and `beginRotationOrNoop` itself.
`interruptRunner` and `startFreshRunner` still exist as named call sites — `interruptRunner` because the arm
string it returns feeds the `arm` field of `v2.interrupt.dispatched` — but neither asserts any more; both call
the interface directly. See [Rotation-delivery gate (#1330) §
BeginRotation](streamsup-package-per-conversation-turn-busy-track-rotation-delivery-gate.md) and
[Interrupt seam](v2-session-manager-state-machine-inbound-interrupt-interrupter-seam-esc.md) for the record
contracts that survived the cut.

A doc comment that counts its own siblings goes stale the moment the set changes: `streamRunner`'s later
forwards (`ModelList` through `SetSpawnWorkDir`, in `cmd/pyry/streamsup_runner.go`) used to number themselves
"the Nth method OFF `sessions.Runner`, after `Interrupt`, `RestartFresh`, `BeginRotation`" and justify their own
assertion "the way `interruptRunner` reaches `Interrupt`" — both false once those three moved onto the
interface. State the placement *rule* in each doc instead of counting or naming a sibling: a capability whose
absence is harmless, with its consumer confined to `cmd/pyry`, stays an optional assertion; a capability whose
absence would fail open across an `internal/sessions` seam goes on the interface. When you move a method
between the two, grep for both spellings — "OFF `sessions.Runner`" adapter/consumer docs *and* the concrete
implementing type's own "deliberately NOT on `sessions.Runner`" claims (`(*streamsup.Runner)`'s own doc
comments carried one for `Interrupt` and `RequestInitialize` that #2592's sweep missed; flagged in PR #2594's
review, left for a follow-up comment sweep alongside the four `cmd/pyry` files below).

**`BeginTeardown` is on this interface for the same reason `SetSpawnPermissionMode` is (#1513).**
Its three callers — both eviction arms of `Session.runActive` and `Pool.UpdateSettings`' restart
branch — live inside `internal/sessions`, which must not import `internal/streamsup`, so a
capability type-assertion at those call sites would fail **open**: a runner double missing the
method would silently leave the teardown ungated while every layer reported success. It is a
sibling of the `new_session` rotation gate (`BeginRotation`, on this interface since #2592) rather
than a call into it — the two gates refuse identically but release on different, incompatible
thresholds. See
[Rotation-delivery gate (#1330) §
teardown](streamsup-package-per-conversation-turn-busy-track-rotation-delivery-gate.md) for why
reusing the rotation gate here would wedge a session.

**#1348 deleted `internal/supervisor`.** There is now exactly **one** production implementation:
`cmd/pyry`'s `streamRunner` adapter, wrapping `*streamsup.Runner` — the adapter exists because Go has no
covariant return on interface satisfaction and the concrete runner's `State` returns `streamsup.State`,
not `sessions.State`. Its compile-time proof, `var _ sessions.Runner = streamRunner{}`, lives with it in
`cmd/pyry/streamsup_runner.go` — **not** in this file, since the type it asserts about is not in this
package. Five test doubles implement the interface directly: `fakeRunner`/`lifecycleRunner`
(`internal/sessions/runner_test.go`), `raceRunner` (`internal/sessions/session_evict_race_test.go`),
`stubRunner` (`cmd/pyry/session_router_test.go`), `baseRunner`
(`cmd/pyry/inbound_deliver_rotation_test.go`). Two more doubles inherit the complete method set by
embedding `stubRunner`: `modelListRunner` (`cmd/pyry/session_model_list_test.go` — plus the one method
`resolveBoundModelList` asserts for, so `stubRunner` itself stays the
ready-made not-implemented fixture for that resolver's refusal case, #1857),
and `slashCommandListRunner` (`cmd/pyry/session_slash_command_list_test.go` —
the same `stubRunner`-plus-one-method shape, for `resolveBoundSlashCommandList`'s
refusal case, #2005).

`Config.RunnerFactory` has **no default**: it is mandatory, and a nil factory is a construction error out
of `sessions.New` (`"sessions: Config.RunnerFactory is required"`) — there is no implicit PTY
implementation to fall back to. Production supplies `cmd/pyry`'s `newStreamRunnerFactory`; tests supply
their own doubles. The "nil selects a wrapper over `supervisor.New`" rollback story this section used to
tell was true for the #1077 Strangler-Fig slice and stopped being true when #1348 removed the thing being
rolled back to.

There is no `Session.Supervisor()` accessor. `cmd/pyry`'s interrupt / new_session / reset wiring reaches
`Interrupt`, `RestartFresh` and `BeginRotation` directly through `Session.Runner()`'s `sessions.Runner`
value since #2592 — no assertion, because all three are on the interface now. Other consumers that need a
method the interface doesn't carry still reach it through a **capability type-assertion** on an anonymous
method-set interface, e.g., since #1857,
`interface{ ModelList() (turnevent.ModelList, bool) }` (`resolveBoundModelList`), which lives in its own
`cmd/pyry/session_model_list.go` rather than `main.go`: a fourth twin in the conversation-keyed resolver
family (alongside `resolveBoundRunner` / `resolveBoundSession` / `resolveBoundRunSettings`), placed in a
topic file the way `outstandingQueues` is, so adding it manufactures no merge conflict in the
high-churn wiring file. #2005 added a fifth: `interface{ SlashCommandList() (turnevent.SlashCommandList, bool) }`
(`resolveBoundSlashCommandList`, its own `cmd/pyry/session_slash_command_list.go`), same placement
reason. None of these assert to the concrete `*streamsup.Runner` type; `Session.Runner()`
holds the value-typed `streamRunner` adapter in production, so a literal `.(*streamsup.Runner)` assertion
would be `ok == false` always and fall through silently to an inert default — the same class of hazard AC
5's #1580 review round caught in this file's own first-draft correction.

**#2420 is where this stopped being a documented hazard and became a real bug, caught only
by a daemon-level test.** `boundMCPChildActuator` (`cmd/pyry/mcp_actuate_v2.go`) asserts a
sixth capability interface, `mcpChildActuator` (`ReconnectMCPServer`/`SetMCPServerEnabled`),
against `resolveBoundRunner`'s return — same family, same call-site shape. Its author read
`resolveBoundMCPStatus`'s working `mcpStatusQuerier` assertion as proof that the concrete
`*streamsup.Runner` satisfies a narrow interface directly, rather than as evidence that
**someone had already widened `streamRunner` with a forward for it**. `streamRunner` carried
no forward for the two new methods, so the assertion was `ok == false` on every call —
exactly the silent-fallthrough failure this section already named — and every unit test
stayed green, because each one injects `mcpChildActuator` directly rather than resolving it
through the adapter the way production does. Only the daemon-level e2e spec caught it, on
its first run. **The check for the next capability assertion in this family:** grep the
method name against `cmd/pyry/streamsup_runner.go` before assuming a concrete runner method
reaches the interface — a working sibling assertion is evidence the adapter was widened for
*that* method, not that the adapter is transparent to every method the concrete type has.
`ModelList`/`SlashCommandList` stay off `sessions.Runner` deliberately: widening for either would drag
every test double in both packages into the diff for no compile-time guarantee, since both consumers sit
in `cmd/pyry`, not `internal/sessions`. `SetSpawnArgs`, `SetModel`, `SetPermissionMode` and
`SetSpawnPermissionMode` are on it instead, because — per their own docs — widening is
compile-checked across the whole one-production/five-double set, whereas a type assertion at a future
call site would fail silently at runtime and either fall back to `Restart` or leave a posture stuck
while the caller reports success — the exact outcomes these swap/posture-changing methods exist to
avoid. `SetModel` follows the same rule: `Pool.deliverSettingsInBand` must either have the live control
capability or fail compilation, because an optional assertion could persist the setting while silently
leaving the running child unchanged. All four share the same placement rule: their consumers are inside `internal/sessions`, so there
is no `cmd/pyry` dispatch site to type-assert at. `Interrupt`, `RestartFresh` and `BeginRotation` are on the
interface for a different reason even though their own consumers sit in `cmd/pyry` (see above,
\#2592) — the risk widening closes for them is a second runner implementation lacking the method, not the
absence of a `cmd/pyry` dispatch site. `SetSpawnPermissionMode` closes a staleness
gap the other three don't have to: `Pool.UpdateSettings` never reconstructs a runner, so without an
interface method a construction-time-only spawn posture would silently outlive the operator's own
change and get re-asserted on the next crash-respawn. See [streamsup-package's Posture
gate](streamsup-package-posture-gate-spawn-permission-mode-ack.md).

**`RevokeBypass` sat on the interface transitionally, side by side with `SetPermissionMode`, and the
pair has since collapsed (#2043).** #2042 generalised the fixed-`"default"` revoke into a
mode-carrying method without touching `RevokeBypass` itself (re-expressed as
`SetPermissionMode(permissionModeDefault)`), specifically so `Pool.UpdateSettings`'s delivered bytes
stayed provably unchanged across that slice. #2043 then rewrote `Pool.UpdateSettings` /
`deliverSettingsInBand` to send an operator-chosen mode and removed `RevokeBypass` from this
interface, from `streamRunner`'s forward, and from all five test doubles — a method retired alongside
the consumer that held it, not in the slice that introduced its replacement. Keeping both clauses in
`deliverSettingsInBand` through the collapse would have emitted two identical control requests for one
revocation (the derivation makes a `yolo:false` update also carry a non-bypass mode); the wire bytes of
a revocation are unchanged, since `RevokeBypass` was always `SetPermissionMode("default")` under the
hood. `(*streamsup.Runner).RevokeBypass` itself is untouched and keeps its own package-level coverage —
only the `internal/sessions` seam lost the method, because nothing outside that package called it
through the interface. See [codebase/1580.md](../codebase/1580.md) for the swap-only installer,
[codebase/1604.md](../codebase/1604.md) for the in-band revocation, and
[streamsup-package-content-blocks-are-held-as-json-rawmessage.md § Permission-mode send
primitive](streamsup-package-content-blocks-are-held-as-json-rawmessage.md) for the writer and its
closed allow-list.

A runner double armed for a **bootstrap-session** capability test cannot be armed at construction time:
`sessions.New` invokes `RunnerFactory` while building the bootstrap entry, so the test does not know the
bootstrap's session id until after the runner already exists. `modelListRunner` (#1857) solves this by
reading a shared, mutex-guarded plan **keyed by session id at call time** rather than storing an answer on
the runner value itself — which also gives "nothing reported" for free as the plan's zero state, and lets
a refusal test arm the bootstrap with a distinguishable sentinel list so a broken isolation guard produces
a visibly wrong (leaked) answer instead of an empty one that could pass unnoticed.

**Capability type-assertions evade `staticcheck`'s unused check from the other direction too
(#1550).** This package's one prior instance of the pattern — `probeUsable`'s
`probe.(interface{ Available() bool })` assertion over `rotation.Probe` — orphaned
`noopProbe.Available` invisibly for as long as it existed: `staticcheck` never flagged the
method, because a method reachable only from its own test still counts as "used", and the
assertion (not an interface implementation) is the only thing that made it reachable at all.
Deleting the resolver that held the assertion (#1550) is what stranded it, and finding that
required a by-hand repo-wide grep, not a gate failure. The remaining capability interfaces above
share the same structural blind spot: removing `resolveBoundModelList` or
`resolveBoundSlashCommandList` (or, in `internal/relay`, `mcpChildActuator`/`mcpStatusQuerier`)
would not, by itself, surface any strandable method on `*streamsup.Runner` via a build or vet
failure — that has to be checked by hand at deletion time, the same way #1550's spec did.
`interruptRunner`, `startFreshRunner` and `beginRotationOrNoop` no longer belong to this family:
\#2592 moved `Interrupt`/`RestartFresh`/`BeginRotation` onto `sessions.Runner`, so the first two
now call the interface directly and the third is deleted — none can strand a method silently any
more, because removing the call site would be a build failure, not a silent unused-method drop.
`resolveBoundModelList`
(#1857) shipped with **no production caller at all**, and stayed reported as "used" purely
because a `_test.go` reference counts — verified empirically against the `staticcheck` version
`make check` installs before relying on it, rather than assumed. It gained its first production
caller in #1867: `retainedModelLists` (`cmd/pyry/session_model_list.go`), the enumerator that adapts it
to the relay's connect-time reconcile seam (see [v2-session-manager.md § Connect-time
model-list reconcile](v2-session-manager.md#connect-time-model-list-reconcile-1863--retainedmodellists-seam--reconcilemodellists)).
`resolveBoundSlashCommandList` (#2005) shipped into the same no-caller state. It gained its first
production caller in #2007: `retainedSlashCommandLists` (`cmd/pyry/session_slash_command_list.go`),
the enumerator that adapts it to the relay's connect-time reconcile seam (see
[v2-session-manager.md § Connect-time slash-command-list
reconcile](v2-session-manager-state-machine-connect-time-slash-command-list-reconcile-retain.md)).

**A twin's stated reason does not transfer just because its conclusion does (#2005).** When
`resolveBoundSlashCommandList` was built arm-for-arm off `resolveBoundModelList` (#1857), the
conclusion both share — "the bool is the only spelling of 'nothing to send', no arm returns true
with an empty payload" — held, but the *reason* the twin's doc gives for it did not:
`resolveBoundModelList`'s doc leans on `turnevent.ModelList.Models` being documented "Never
empty," a type-level guarantee `turnevent.SlashCommandList.Commands` does not carry (it's
documented nil for a zero-length list, deferring to its producer). Carrying the twin's sentence
across verbatim would have made a doc comment state something false. The actual guarantee here
comes from the *producer* — streamsup's `emitSlashCommandList` suppresses the empty list before it
ever reaches retention — and that producer-sourced reason is what
`sessionSlashCommandHold.SlashCommandList`'s own doc states, so the resolver inherited that
wording instead of the twin's. The general rule for the next twin in this family: mirror the
twin's *shape* freely, but verify each doc-comment *justification* against the new type's own
contract before copying it — a shared conclusion is not proof the reasoning under it survived the
swap.

**Typed-nil-in-interface trap for downstream consumers (#1101).** A call site that assigns `w.sup` (the `*supervisor.Supervisor` returned by `Supervisor()`) straight into a consumer-declared interface field inherits a footgun on the stream-json path: a nil `*supervisor.Supervisor` wrapped in an interface value is a **non-nil interface holding a nil pointer**, so the consumer's `== nil` guard silently fails and any method call on it panics on the nil receiver. `cmd/pyry/relay.go`'s `Snapshotter: w.sup` wiring hit exactly this and was fixed by a `screenSnapshotterOrNil` helper that returned a genuine nil when `sup == nil` — see [codebase/1101.md](../codebase/1101.md). Both the field and the helper are gone: #1348 deleted `screenSnapshotterOrNil` when the stream cutover removed its last caller, and #2540 deleted `Snapshotter` itself along with the render arm it gated. Two sibling wiring sites carried the same unfixed trap **as of #1101** — `SessionStarter: w.sup` and the modal resolver's `w.sup` argument (both `cmd/pyry/relay.go`), flagged out of scope there, not yet guarded — but neither wires `w.sup` directly any more: `SessionStarter` wires `w.activeSessionStarter` since #1125 and the modal resolver wires through `newModalResolverV2`, so the trap this note illustrates is historical rather than a live gap.

See [codebase/1077.md](../codebase/1077.md) and spec [`1077-sessions-runner-seam.md`](../../specs/architecture/1077-sessions-runner-seam.md).
