# Per-child-exit seam (#1206)

Split from #1203, alongside #1207 (open, blocked-by this ticket). #1201/#1202 can clear the turn-busy
tracker on a `TurnEnd` or a pool transition, but neither fires when a child simply crashes and the runner
respawns it: no `result` line for the abandoned turn, no pool transition (`RotateBootstrapForSelfHeal`
deliberately fires none). Nothing outside `internal/streamsup` could observe that exit at all — the only
escaping lifecycle state, `PhaseStopped`, means "`Run` has returned" and fires once, on permanent
shutdown, never on a crash-respawn.

```go
// exported, unlike onSpawn, because #1207's consumer lives outside this package
OnChildExit func()
```

One new field on `Config`, called once per **completed supervision iteration** — after the spawn attempt
finishes, before `Run` decides whether to shut down, relaunch immediately, or back off. The single call
site sits between `uptime := time.Since(start)` and `Run`'s post-spawn `if ctx.Err() != nil { return
ctx.Err() }`, i.e. above both the shutdown return *and* the `claude exited` log — `spawnAndWait` returns
from exactly one point and the loop only then branches on why the child is gone, so one unconditional call
there covers the crash, deliberate-restart, and shutdown paths without enumerating them. The two anchors
that look obvious instead — the `claude exited` log, or a `return ctx.Err()` site (there are three; only
one sits in the actual fire window) — each satisfy two of the three exit paths and silently miss the
third, which is why the shutdown-path test is the load-bearing one.

**Cardinality is per iteration, not per live child.** The call is unconditional, so it also fires when
`spawnAndWait` reports `started == false` — a pre-launch setup failure where no claude process ever
existed. Deliberate: no child existed, so no turn of this runner's can be open, and #1207's intended clear
is idempotent either way. A caller that needs "a real child died" cannot get that from this seam.

**Runs synchronously on the `Run` goroutine with no Runner lock held**, so a callback may call any Runner
method without deadlocking — but the state some of those methods return has not caught up yet at this
exact line: `State()` still reports `PhaseRunning` plus the dead child's PID (cleared only below the fire,
and never cleared on the shutdown path), and `Restart()` called from inside the callback skips the backoff
ladder entirely, since the fire precedes `drainRestart()`. Not a bug in this slice (no consumer yet), but a
sharp edge any future caller of this seam — #1207 included — has to read past the "no lock held" framing
to see. The callback must not block (it stalls the restart ladder) or panic (no `recover`, matching
`onSpawn`), and it is not a drain barrier: bytes the dead child already wrote may still be in flight in a
downstream sink when it fires, so a consumer needing ordering against those events must get it from that
sink, not from this callback.

**Ships unwired, with zero `cmd/pyry` diff.** Every `streamsup.Config` literal tree-wide is named-field, so
the new field is nil on the sole production construction path (`streamsup_runner.go`'s
`mapStreamsupConfig`) with no edit required. #1209/#1210 are the consumers — the exit lane (#1209, below)
and its production wiring (#1210, open) that together close the mid-turn crash clear the #1201/#1202 pair
couldn't reach. See [codebase/1206.md](../codebase/1206.md).
