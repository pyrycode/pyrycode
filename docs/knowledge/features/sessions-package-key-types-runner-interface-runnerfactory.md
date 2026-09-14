# `Runner` interface + `RunnerFactory` (#1077, corrected #1580 for #1348 fallout, widened #2042, `RevokeBypass` retired #2043, widened #2064/#2280)

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
}

type RunnerFactory func(cfg RunnerConfig) (Runner, error)
```

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

There is no `Session.Supervisor()` accessor. Consumers that need methods off the concrete runner
(`cmd/pyry`'s interrupt / new_session wiring) reach `*streamsup.Runner` via `Session.Runner()` — which
keeps returning the `Runner` interface — and a **capability type-assertion** on an anonymous method-set
interface, e.g. `interface{ Interrupt() error }` (`interruptRunner`),
`interface{ RestartFresh(string) }` (`startFreshRunner`), `interface{ BeginRotation() func() }`
(`beginRotationOrNoop`) — all in `cmd/pyry/main.go` — and, since #1857,
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
`Interrupt`/`RestartFresh`/
`BeginRotation`/`ModelList` stay off `sessions.Runner` deliberately (adding any would be speculative
surface, or in `ModelList`'s case would drag every test double in both packages into the diff for no
compile-time guarantee since its consumer sits in `cmd/pyry`, not `internal/sessions`); `SetSpawnArgs`,
`SetModel`, `SetPermissionMode` and `SetSpawnPermissionMode` are on it instead, because — per their own docs — widening is
compile-checked across the whole one-production/five-double set, whereas a type assertion at a future
call site would fail silently at runtime and either fall back to `Restart` or leave a posture stuck
while the caller reports success — the exact outcomes these swap/posture-changing methods exist to
avoid. `SetModel` follows the same rule: `Pool.deliverSettingsInBand` must either have the live control
capability or fail compilation, because an optional assertion could persist the setting while silently
leaving the running child unchanged. All four share the same placement rule: their consumers are inside `internal/sessions`, so there
is no `cmd/pyry` dispatch site to type-assert at. `SetSpawnPermissionMode` closes a staleness
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
required a by-hand repo-wide grep, not a gate failure. The six capability interfaces above
share the same structural blind spot: removing `interruptRunner`, `startFreshRunner`,
`beginRotationOrNoop`, `resolveBoundModelList`, or `resolveBoundSlashCommandList` would not, by
itself, surface any strandable method on `*streamsup.Runner` via a build or vet failure — that has
to be checked by hand at deletion time, the same way #1550's spec did. `resolveBoundModelList`
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

**Typed-nil-in-interface trap for downstream consumers (#1101).** A call site that assigns `w.sup` (the `*supervisor.Supervisor` returned by `Supervisor()`) straight into a consumer-declared interface field inherits a footgun on the stream-json path: a nil `*supervisor.Supervisor` wrapped in an interface value is a **non-nil interface holding a nil pointer**, so the consumer's `== nil` guard silently fails and any method call on it panics on the nil receiver. `cmd/pyry/relay.go`'s `Snapshotter: w.sup` wiring hit exactly this and was fixed by a `screenSnapshotterOrNil` helper that returns a genuine nil when `sup == nil` — see [codebase/1101.md](../codebase/1101.md). Two sibling wiring sites carry the same unfixed trap as of #1101: `SessionStarter: w.sup` and the modal resolver's `w.sup` argument (both `cmd/pyry/relay.go`) — flagged out of scope there, not yet guarded.

See [codebase/1077.md](../codebase/1077.md) and spec [`1077-sessions-runner-seam.md`](../../specs/architecture/1077-sessions-runner-seam.md).
