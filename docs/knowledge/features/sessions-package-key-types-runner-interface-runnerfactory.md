# `Runner` interface + `RunnerFactory` (#1077, corrected #1580 for #1348 fallout)

`Session.sup` is typed `Runner` (`internal/sessions/runner.go`):

```go
type Runner interface {
    State() State
    WriteUserTurn(ctx context.Context, conversationID string, payload []byte) error
    WaitForPTY(ctx context.Context) error
    Run(ctx context.Context) error
    Restart(args []string)
    SetSpawnArgs(args []string) // #1580 — installs the NEXT spawn's argv, no kill
    RevokeBypass() error        // #1604 — drops bypass on the LIVE child, no kill
}

type RunnerFactory func(cfg RunnerConfig) (Runner, error)
```

**#1348 deleted `internal/supervisor`.** There is now exactly **one** production implementation:
`cmd/pyry`'s `streamRunner` adapter, wrapping `*streamsup.Runner` — the adapter exists because Go has no
covariant return on interface satisfaction and the concrete runner's `State` returns `streamsup.State`,
not `sessions.State`. Its compile-time proof, `var _ sessions.Runner = streamRunner{}`, lives with it in
`cmd/pyry/streamsup_runner.go` — **not** in this file, since the type it asserts about is not in this
package. The other five implementations are all test doubles: `fakeRunner`/`lifecycleRunner`
(`internal/sessions/runner_test.go`), `raceRunner` (`internal/sessions/session_evict_race_test.go`),
`stubRunner` (`cmd/pyry/session_router_test.go`), `baseRunner`
(`cmd/pyry/inbound_deliver_rotation_test.go`), `modelListRunner`
(`cmd/pyry/session_model_list_test.go` — `stubRunner` plus the one method
`resolveBoundModelList` asserts for, so `stubRunner` itself stays the
ready-made not-implemented fixture for that resolver's refusal case, #1857).

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
high-churn wiring file. None of these assert to the concrete `*streamsup.Runner` type; `Session.Runner()`
holds the value-typed `streamRunner` adapter in production, so a literal `.(*streamsup.Runner)` assertion
would be `ok == false` always and fall through silently to an inert default — the same class of hazard AC
5's #1580 review round caught in this file's own first-draft correction. `Interrupt`/`RestartFresh`/
`BeginRotation`/`ModelList` stay off `sessions.Runner` deliberately (adding any would be speculative
surface, or in `ModelList`'s case would drag every test double in both packages into the diff for no
compile-time guarantee since its consumer sits in `cmd/pyry`, not `internal/sessions`); `SetSpawnArgs` and
`RevokeBypass` are on it instead, because — per their own docs — widening is compile-checked across the
whole one-production/five-double set, whereas a type assertion at a future call site would fail silently
(open, for `RevokeBypass`) at runtime and either fall back to `Restart` or leave a posture un-revoked while
the caller reports success — the exact outcomes these two swap/revoke-only methods exist to avoid. Both
share the same placement rule: their consumer, `Pool.UpdateSettings`, is inside `internal/sessions`, so
there is no `cmd/pyry` dispatch site to type-assert at. See [codebase/1580.md](../codebase/1580.md) for the
swap-only installer and [codebase/1604.md](../codebase/1604.md) for the in-band revocation.

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
required a by-hand repo-wide grep, not a gate failure. The five capability interfaces above
share the same structural blind spot: removing `interruptRunner`, `startFreshRunner`,
`beginRotationOrNoop`, or `resolveBoundModelList` would not, by itself, surface any strandable
method on `*streamsup.Runner` via a build or vet failure — that has to be checked by hand at
deletion time, the same way #1550's spec did. `resolveBoundModelList` (#1857) shipped with
**no production caller at all**, and stayed reported as "used" purely because a `_test.go`
reference counts — verified empirically against the `staticcheck` version `make check`
installs before relying on it, rather than assumed. It gained its first production caller in #1867: `retainedModelLists` (`cmd/pyry/session_model_list.go`), the enumerator that adapts it
to the relay's connect-time reconcile seam (see [v2-session-manager.md § Connect-time
model-list reconcile](v2-session-manager.md#connect-time-model-list-reconcile-1863--retainedmodellists-seam--reconcilemodellists)).

**Typed-nil-in-interface trap for downstream consumers (#1101).** A call site that assigns `w.sup` (the `*supervisor.Supervisor` returned by `Supervisor()`) straight into a consumer-declared interface field inherits a footgun on the stream-json path: a nil `*supervisor.Supervisor` wrapped in an interface value is a **non-nil interface holding a nil pointer**, so the consumer's `== nil` guard silently fails and any method call on it panics on the nil receiver. `cmd/pyry/relay.go`'s `Snapshotter: w.sup` wiring hit exactly this and was fixed by a `screenSnapshotterOrNil` helper that returns a genuine nil when `sup == nil` — see [codebase/1101.md](../codebase/1101.md). Two sibling wiring sites carry the same unfixed trap as of #1101: `SessionStarter: w.sup` and the modal resolver's `w.sup` argument (both `cmd/pyry/relay.go`) — flagged out of scope there, not yet guarded.

See [codebase/1077.md](../codebase/1077.md) and spec [`1077-sessions-runner-seam.md`](../../specs/architecture/1077-sessions-runner-seam.md).
