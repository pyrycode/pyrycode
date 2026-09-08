# Concurrency-safe open-session enumeration (#588) — `ActiveConns` method + `snapshot` funnel

`ActiveConns(ctx context.Context) []ActiveConn` is the **enumeration** half of server-initiated fan-out: [`Push`](#concurrency-safe-unsolicited-push-571--push-method--push-funnel) can address one open session by `conn_id`, but cannot discover *which* sessions are open. `ActiveConns` returns a snapshot of every session currently in `V2StateOpen`, paired with its negotiated `interactive` flag, so a producer goroutine can fan an unsolicited frame out to all connected phones by calling `ActiveConns` then `Push` per returned conn-id. It is the v2 analog of v1's `dispatch.Dispatcher.ActiveConns()`, and the missing piece [#571](../codebase/571.md) deferred. See [`codebase/588.md`](../codebase/588.md).

It is the structural twin of `Push`/`Rekey`, the **fourth** instance of the single-writer funnel: a new unbuffered `snapshot chan snapshotReq` field + a sixth `Run` select arm route each request onto the single dispatch goroutine, where the private `handleActiveConns` (renamed from `handleActiveConnIDs` in #626) reads `m.sessions` under the single-owner-goroutine invariant — serialised by `Run`'s `select` against every map write (lazy-create, `delete` in `closeWith`, handshake/re-key state transitions). `ActiveConns` itself does only channel I/O on the **caller's** goroutine (a `select` send onto `m.snapshot`, then a `select` receive on the per-request `reply chan []ActiveConn`, both with `ctx.Done` escape arms returning `nil`); the seal/marshal steps a `Push` would run are simply absent — a snapshot touches no CipherState. No new lock, no new goroutine, no new wire shape, no new exported type beyond the method.

```go
// The reply was widened from chan []string to chan []ActiveConn in #626; the
// handler below appends the negotiated interactive flag. See § Capability
// negotiation (#626).
type snapshotReq struct {
    reply chan []ActiveConn // cap=1 per request; Run's reply send is non-blocking
}

// In Run's select, beside the m.drainCh arm:
case req := <-m.snapshot:
    req.reply <- m.handleActiveConns()

// handleActiveConns, on the Run goroutine — the only site reading m.sessions:
out := make([]ActiveConn, 0, len(m.sessions))
for connID, s := range m.sessions {
    if s.state == V2StateOpen {
        out = append(out, ActiveConn{ConnID: connID, Interactive: s.interactive,
            DeviceName: s.clientName, ClientVersion: s.clientVersion}) // #2148
    }
}
return out
```

**The `s.state == V2StateOpen` filter is the load-bearing security gate** (`security-sensitive`). Only an open session has had its token validated (in `handleNoiseInit`'s accept branch); a `V2StateHandshakeComplete` session holds CipherStates but never passed the token check, and is excluded — identical to the gate [`forwardEnvelope`](#concurrency-safe-unsolicited-push-571--push-method--push-funnel) enforces on the push-drain side (#610), so a server push never reaches an un-authenticated peer. **Belt-and-suspenders, different fabric:** even if this filter regressed, the consumer `Push`es per returned id — a non-open conn has no queue (`ErrConnNotFound`), and a conn that closes between enqueue and drain is caught by `forwardEnvelope`'s `V2StateOpen` re-check before sealing — two deterministic, independent code-level checks (enumeration filter + `forwardEnvelope` gate), neither a stochastic agent rule. A `V2StateClosed` session cannot appear: `closeWith` already `delete`d it from the map.

**Returns `[]ActiveConn`, not `([]ActiveConn, error)`.** A snapshot has no failure mode the caller can act on; the only non-completion (ctx cancelled, or `Run` already exited with `Frames` closed and no receiver on `m.snapshot`) returns `nil` — equivalent to "no open sessions" for the broadcast consumer, which fans out to nobody this round and re-enumerates on the next assistant turn. `nil` and an empty non-nil slice are both `len 0` and interchangeable. The result is an **unordered set** (Go's randomized map-iteration order); the handler does not sort (no AC requires it; the broadcast consumer fans out order-independently — paying O(n log n) on the single dispatch goroutine would buy nothing). `handleActiveConns` emits no log line and reads no secret-bearing field, handed only to the in-process consumer, never to a wire.

**Since #2148, `ActiveConn` also carries `DeviceName` and `ClientVersion`** —
appended fields, since the type's 129 keyed literals (all in tests) would
otherwise all need touching. The doc's previous claim that the return "holds
only non-secret conn-id routing keys + the negotiated `interactive` bool" is
now false and is corrected here rather than left to rot: these two are
remote-authored, unvalidated display strings, retained verbatim in
`handleNoiseInit`'s token-OK tail (before `V2StateOpen`, so an unauthenticated
peer's strings are never enumerable) but bounded at that retention site —
`maxRetainedClientNameBytes` / `maxRetainedClientVersionBytes`, over-bound
dropped to `""` rather than truncated — because this snapshot is what the
fan-out copies several times per turn across every open conn, and an
authenticated client parking ~64KB per string here would multiply that copy
cost. That bound is deliberately **not** the same door as
`internal/sessions`' `admitClient` (see
[the system-prompt client-naming section](sessions-package-key-types-writesystemprompt-systemprompttext.md#naming-the-attached-client-2148)):
this one caps memory and copy cost at the point of retention; character-set
and display-safety validation happen once, downstream, at the single door
that renders the values into a prompt. A consumer of this struct MUST NOT log
either field, interpolate them into an error, or format the struct wholesale
(`%+v`, `slog.Any`) — doing so would leak them by accident. No such wholesale
format exists among today's ~8 fan-out consumers in `cmd/pyry` (checked
against every one when the fields landed); a new one is the thing to watch
for.

**Widened to a capability-aware enumeration in #626.** The reply was originally `chan []string`; #626 widened it to `chan []ActiveConn` (conn-id + the negotiated `interactive` flag) so the handshake's capability negotiation is observable to a fan-out consumer. A test-only `ActiveConnIDs(ctx) []string` projection over `ActiveConns` shipped alongside the widening — reachable only from `internal/relay` tests, pending a #572 production wire-up. **#572 closed NOT_PLANNED; the wire-up never landed.** #1542 deleted the projection and retargeted its five-test coverage directly onto `ActiveConns` — see § Same-package unit tests. The deleted projection's nil-guard needed no replacement code: `ActiveConns` already supplies both halves the guard preserved natively (`ctx.Done()` returns a literal `nil`; `handleActiveConns` returns a non-nil `make([]ActiveConn, 0, ...)` on an empty manager), which is what made the guard redundant rather than load-bearing.
