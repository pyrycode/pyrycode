# Idle-Eviction + Lazy-Respawn Pattern (`idle_test.go`, #115)

Two tests in `internal/e2e/idle_test.go` (build tag `e2e`) exercise the
idle-eviction state machine and lazy respawn at the binary boundary —
the assembled `pyry` daemon, the real `internal/sessions` lifecycle
goroutine, the real control server, the real on-disk `sessions.json`.
Package-level integration tests in `internal/sessions/` already cover
the in-process pool primitives; #115 closes the binary-boundary gap.

| Test | Asserts |
|---|---|
| `TestE2E_IdleEviction_EvictsBootstrap` | with `-pyry-idle-timeout=1s`, the bootstrap entry's `lifecycle_state` becomes `"evicted"` on disk within 5s; `pyry status` does not report `Phase: running` |
| `TestE2E_IdleEviction_LazyRespawn` | after eviction, a raw `VerbAttach` over the control socket triggers respawn; `lifecycle_state` returns to active and `pyry status` reports `Phase: running` while the conn is held |

### Variadic-flags harness extension

Both tests need `-pyry-idle-timeout=1s` (the harness default is `=0`).
`spawn` and `StartIn` grew a variadic `extraFlags ...string` parameter;
the standard set is built into a slice, `extraFlags` are appended, then
`--` and the claude args. Go's `flag` package processes left-to-right
with last-wins semantics, so a duplicate flag in `extraFlags` overrides
the standard default. `StartExpectingFailureIn` was deliberately *not*
extended — no failed-start test in #115 needs flags; one-line change
when a future scenario does.

Existing call sites (`Start(t)`, `StartIn(t, home)`, restart-test calls)
are unchanged — variadic is backwards-compatible at every site. Net
public API delta: zero new exported names; two existing signatures grew
one variadic parameter.

### Why raw `VerbAttach` over `pyry attach` as a subprocess

Determinism. `pyry attach` enters a stdin-byte loop (`copyWithEscape`)
that requires careful pipe management to keep the conn alive while
assertions run; closing stdin too early ends the attach before the next
idle eviction fires. A raw `net.Dial` + `json.Encode` + `defer
conn.Close()` lets the test own the timeline exactly. The control
protocol's `Request` / `Response` / `AttachPayload` types are public on
`internal/control`; importing them from `internal/e2e` is in-module and
intended.

### Why poll for `Phase: running` and not `attached > 0`

`attached` is package-private state inside `Session`. The wire surface
(`pyry status`) reports the supervisor's phase, which is what "alive
again" means at the AC level. Phase transitions to `running` once the
supervisor's child-process spawn loop has the PID — exactly the
"lazy respawn happened" signal the AC asks for.

### `waitForBootstrapState` helper — file-local

Polls the registry file for the bootstrap entry's `lifecycle_state` to
match `"evicted"` or `"active"`. The `"active"` arm tolerates either an
empty/missing field (today's `omitempty` default for `stateActive`) or
the literal string `"active"` — decouples the test from a future toggle
that starts writing the field explicitly. File-local; promoted to
`harness.go` only if a third caller justifies it (rule of three).

### Why poll registry first, then `pyry status`

The registry is the load-bearing AC ("registry state observable").
`pyry status` is the cross-check — its phase string comes from
`internal/supervisor`'s state and is byte-stable in `runStatus`'s
`"Phase:         %s\n"` format. The eviction test asserts *negation*
(`!Contains "Phase:         running"`), not the exact non-running phase,
so a future change from `PhaseStopped` to a sibling phase doesn't break
the test.

### Why hold the conn open across Phase C

`handleAttach` writes the OK ack and binds the bridge to the conn. The
conn now streams PTY bytes from `/bin/sleep 99999` (which writes
nothing) and back. The test never reads from the conn after the ack —
just holds it open via `defer conn.Close()` so `attached > 0` defers
the next idle eviction while the assertions run. Bridge teardown fires
server-side on `conn.Close()` when the test returns.

### Sleep-arg portability — `99999`, not `infinity`

Pre-#115 the harness passed `infinity` as the `/bin/sleep` argument.
GNU coreutils accepts it; macOS BSD sleep does not (and the
unit-suffixed forms its man page advertises don't all work — `99999d`
exits with the usage banner on macOS Tahoe 26.3). For tests that don't
care about the supervised child surviving (`TestStatus_E2E`, etc.) the
short-circuit child exit was invisible — the readiness gate trips on
the control socket, not the child. Lazy respawn surfaces it: the test
needs the child to live long enough for `Phase: running` to be
observed after respawn, otherwise the supervisor enters perpetual
backoff and the assertion never fires. `99999` (a plain integer in
seconds, ~27h) works on both. See `lessons.md § Test helpers across
packages`.

### Production diff is zero

`-pyry-idle-timeout` already existed (`cmd/pyry/main.go:257`). The
state machine, persistence, and control-plane Activate-before-Attach
all shipped in #40. #115 adds binary-boundary coverage. Test diff
~125 LOC (single new file) plus the variadic-flags signature change in
`harness.go`.
