# Spec #588 — `internal/relay`: enumerate open v2 Noise session conn IDs for server-initiated fan-out

**Ticket:** [#588](https://github.com/pyrycode/pyrycode/issues/588) · **Size:** S · **Split from:** #572 · `security-sensitive`

## Summary

Add a concurrency-safe snapshot primitive to `V2SessionManager` that returns the
`conn_id` of every currently-**open** (authenticated) v2 session. This is the
enumeration half that [#571](../../knowledge/codebase/571.md) deliberately
deferred — `Push(ctx, connID, env)` can address one phone but cannot discover
*which* phones are connected. The first consumer (#572's assistant-turn bridge)
calls this snapshot, then `Push` on each returned id to fan one unsolicited
`message` envelope out to all connected phones.

Purely additive: one new public method + its funnel plumbing in a single
production file. No change to the inbound dispatch path, no new wire shapes, no
change to v1. It is the v2 analog of v1's `dispatch.Dispatcher.ActiveConns()`.

## Files to read first

- `internal/relay/v2session.go:1310-1391` — `Push` / `handlePush`: the exact
  funnel this method mirrors (public method does channel I/O on the caller's
  goroutine; private handler reads `m.sessions` on Run's goroutine). Copy the
  shape; drop the seal/marshal steps.
- `internal/relay/v2session.go:106-114` — `pushReq` struct (the `{…, reply chan T}`
  request shape to clone as `snapshotReq`).
- `internal/relay/v2session.go:350-357,383-390` — `push` channel field doc +
  allocation in `NewV2SessionManager`; the new `snapshot` channel sits beside it.
- `internal/relay/v2session.go:401-421` — `Run`'s `select`; add one arm next to
  the `m.push` arm.
- `internal/relay/v2session.go:1364-1373` — `handlePush`'s `m.sessions[connID]` /
  `s.state != V2StateOpen` gate: the same `V2StateOpen` predicate this snapshot
  filters on (the security gate — § Security review).
- `internal/relay/v2session.go:138-220` — `V2SessionState` constants +
  `V2Session` struct + the `State()` doc-comment that names "a broadcast layer
  added in a later slice" (this ticket) and flags the cross-goroutine-read concern.
- `internal/relay/v2session_test.go:2733-2777` — `TestV2Session_Push_NotOpen_…`:
  the **white-box session-injection** idiom (`mgr.sessions[id] = &V2Session{…}`)
  the state-filter tests reuse, and *why it is `-race` clean* (the request
  channel send is the happens-before edge; Run touches the map only when it
  services the request).
- `internal/relay/v2session_test.go:706-743` — `driveToOpen` + the `openSession`
  struct: the real-handshake harness the concurrency (`-race`) test drives.
- `internal/relay/v2session_test.go:2460-2562` — `TestV2Session_Push_Interleaved…`:
  the interleave-under-`-race` pattern the AC#3 test mirrors (hammer the new
  method from a goroutine while inbound frames drive `dispatchAppFrame`).
- `internal/relay/v2session_test.go:2839-2862` — `TestV2Session_Push_CtxCancelled_…`:
  the "Run not started ⇒ ctx.Done is the only ready case" deterministic pattern.
- `docs/knowledge/codebase/571.md` § "Out of scope (deferred)" — names this slice
  and the recommended `ActiveConnIDs() []string` direction.
- `docs/knowledge/features/v2-session-manager.md` — evergreen manager doc; its
  "no broadcast surface" deferral narrows to this enumeration primitive.

## Context

`V2SessionManager.sessions` (`map[string]*V2Session`) is owned exclusively by the
manager's single `Run` goroutine — there is **no mutex** (the loop is the lock;
flynn/noise `CipherState`s are not safe for concurrent use, so single-writer is
the package's deliberate concurrency model, see `v2session.go:309-323`). Reading
`m.sessions` from any other goroutine is a data race. #571 spelled out the fix
shape: "a cross-goroutine read of `m.sessions` requiring its own snapshot funnel
(e.g. an `ActiveConnIDs() []string` method through the same `select`)." This spec
implements exactly that.

The session lifecycle (`v2session.go:140-145`): `V2StateAwaitingInit` →
`V2StateHandshakeComplete` → `V2StateOpen` → `V2StateClosed`. Only `V2StateOpen`
is authenticated (the token was validated in `handleNoiseInit`'s accept branch
before the transition). `V2StateClosed` sessions are `delete`d from the map by
`closeWith` (`v2session.go:1212`), so they never appear in a map read.

## Design

### Public surface (one new method, one return type — `[]string`)

```go
// ActiveConnIDs returns a snapshot of the conn IDs of every session
// currently in V2StateOpen. Safe to call from any goroutine other than Run.
func (m *V2SessionManager) ActiveConnIDs(ctx context.Context) []string
```

- **Return `[]string`, not `([]string, error)`.** A snapshot has no failure mode
  the caller can act on. The only non-completion is ctx cancellation / Run already
  exited, both of which return `nil` — equivalent to "no open sessions" for the
  broadcast consumer (it fans out to nobody this round; the next assistant turn
  re-enumerates). `nil` and empty-slice are both `len 0` and interchangeable to
  callers. This avoids forcing #572 to handle an error it cannot meaningfully act on.
- **`ctx` parameter — divergence from v1's `ActiveConns()` is deliberate.** v1's
  `dispatch.Dispatcher.ActiveConns()` (`internal/dispatch/dispatch.go:347`) reads
  `d.conns` under `d.mu` — a mutex, no funnel, so no ctx. v2 has no mutex; the read
  must funnel through `Run`, so the public method needs a ctx escape arm or it
  blocks forever when `Run` has exited (Frames closed). Mirrors `Push`/`Rekey`,
  which take ctx for the same reason.

### Plumbing (the #571 funnel, fourth instance of the idiom)

1. **`snapshotReq` struct** — `{reply chan []string}`. Mirrors `pushReq`
   (`v2session.go:106-114`) minus the `connID`/`env` inputs (a snapshot takes no
   per-conn argument). `reply` is **`cap=1`** so Run's reply send never blocks even
   if the caller's ctx fired between enqueue and reply.
2. **`snapshot chan snapshotReq` field** on `V2SessionManager`, allocated
   `make(chan snapshotReq)` (unbuffered) in `NewV2SessionManager` beside `push`.
   Doc-comment mirrors `push`'s: unbuffered backpressure is correct; not closed on
   Run exit; in-flight callers unblock via `ctx.Done`.
3. **One new `Run` select arm**, next to the `m.push` arm:
   `case req := <-m.snapshot: req.reply <- m.handleActiveConnIDs()`.
4. **Public `ActiveConnIDs(ctx)`** — does *only* channel I/O on the caller's
   goroutine: a `select` send onto `m.snapshot` (or `ctx.Done` → return `nil`),
   then a `select` receive on `req.reply` (or `ctx.Done` → return `nil`). Byte-for-
   byte the `Push` shape with the `ErrConnNotFound` return swapped for `nil`.
5. **Private `handleActiveConnIDs() []string`** — runs on Run's goroutine, the only
   site that touches the map:

```go
out := make([]string, 0, len(m.sessions))
for connID, s := range m.sessions {
    if s.state == V2StateOpen { out = append(out, connID) }
}
return out
```

### Ordering: unsorted set, documented

`handleActiveConnIDs` returns conn IDs in Go's randomized map-iteration order. The
AC requires no ordering and the broadcast consumer fans out order-independently, so
the handler does **not** sort — keeping Run's per-call work minimal (sorting on the
single dispatch goroutine would add O(n log n) for no functional benefit). The
method's doc-comment states the result is an **unordered set**; tests sort-then-
compare (or set-compare), never assert positional order. This matches Go's map
convention and the package's "minimal work on Run" posture.

## Concurrency model

No new goroutine, no new lock, no atomic. The snapshot read executes on `Run`'s
existing dispatch goroutine when it services the `m.snapshot` arm — the same
goroutine that mutates `m.sessions` (lazy-create in `handleFrame`, `delete` in
`closeWith`, state transitions in the handshake handlers). Therefore the map read
is serialized against every map write by `Run`'s `select`; no read can observe a
half-updated map or a torn `s.state`. The unbuffered `m.snapshot` channel forces a
cross-goroutine caller to *wait its turn* rather than race. This is the
single-writer funnel idiom reused a fourth time (after `Rekey`/#462, `Push`/#571),
consistent with the package's documented no-mutex contract.

**Composition with re-key and teardown is free.** A `handleRekeyInit` CipherState
swap or a `closeWith` delete and a `handleActiveConnIDs` read are all on `Run`; a
snapshot reflects either the pre-event or post-event map, never an inconsistent
intermediate.

## Error handling

| Condition | Behaviour |
|---|---|
| One or more open sessions | `[]string` of their conn IDs (unordered, non-nil) |
| No open sessions (map empty, or all non-open) | empty non-nil slice (`len 0`) |
| Caller ctx cancelled before Run services the request | `nil` (ctx.Done arm) |
| `Run` already exited (Frames closed) — no receiver on `m.snapshot` | caller blocks on the send until its ctx fires, then `nil` |

No sentinel errors are introduced. `handleActiveConnIDs` emits no log line — it is a
pure read (consistent with `handlePush`, which logs only on its transport path).
Sessions in `V2StateAwaitingInit` / `V2StateHandshakeComplete` are silently excluded
(the filter, not an error). `V2StateClosed` sessions cannot appear — `closeWith`
already removed them from the map.

## Testing strategy

White-box tests in `internal/relay/v2session_test.go` (same package), stdlib
`testing` only, all `-race`-clean. Reuse `startManager`, `driveToOpen`,
`v2Recorder`, `genV2Keypair`, `v2PairedRegistry`, and the white-box
`mgr.sessions[id] = &V2Session{…}` injection idiom. Scenarios (developer writes the
bodies in the project idiom):

- **Open-only enumeration (AC#1).** Inject several sessions of mixed state into a
  started manager — e.g. `{a: Open, b: Open, c: HandshakeComplete, d: AwaitingInit}` —
  then `ActiveConnIDs`. Assert the returned set equals `{a, b}` exactly
  (sort-then-compare). Pins both halves of AC#1: every open id is present, every
  non-open id (handshaking / handshake-complete-token-unvalidated) is absent. Safe &
  `-race` clean via the same happens-before edge as `TestV2Session_Push_NotOpen` (no
  frames fed, no timers armed; Run touches the map only when servicing the snapshot).
- **Torn-down session absent (AC#2).** Drive a real open via `driveToOpen`, confirm
  its id appears, then drive an AEAD-failure 4421 teardown (flip a ciphertext byte —
  the `TestV2Session_Push_ClosedSession` recipe) so `closeWith` deletes it, and
  assert a subsequent `ActiveConnIDs` no longer contains that id.
- **Concurrent with in-flight dispatch (AC#3) — the `-race` proof.** Drive a real
  open session, then from a separate goroutine call `ActiveConnIDs` in a tight loop
  while feeding inbound sealed request frames that trigger `dispatchAppFrame` replies
  (mirror `TestV2Session_Push_InterleavedWithReply`). Every snapshot either contains
  the open conn or is empty (never garbage); the solicited replies still decrypt in
  order (nonce integrity intact — session state never corrupted). Run under `-race`.
- **Empty manager.** `ActiveConnIDs` on a started manager with zero sessions returns
  a `len 0` slice, no block.
- **Ctx cancelled (deterministic).** Construct the manager but do **not** start
  `Run`; call `ActiveConnIDs` with an already-cancelled ctx; assert it returns `nil`
  without blocking (the `ctx.Done` send arm is the only ready case — mirror
  `TestV2Session_Push_CtxCancelled`).

AC#4 (no regressions) is covered by the existing `internal/relay` suite passing
unmodified — the change is additive. Gate on `go test -race ./...`, `go vet ./...`,
`go build ./cmd/pyry`.

## Security review (`security-sensitive`)

> The architect's `security-review.md` file was not present in this worktree; this
> pass was conducted from the documented categories (trust boundaries, information
> disclosure, authz, concurrency-safety, DoS) and the package's established
> security discipline.

**Trust boundaries.** `ActiveConnIDs` takes no untrusted input — only a `ctx`. conn
IDs are internal routing-map keys (set by the relay, already used as routing keys
and logged throughout the package); they are **not secrets**. The return value is
handed only to the in-process consumer (#572) — it is never emitted on a wire and
never logged by this method. No token, device name, key byte, plaintext, or
ciphertext is read or returned. There is no new external attack surface.

**Authorization — the load-bearing control (matches AC#1's explicit requirement).**
The snapshot **must** exclude unauthenticated sessions. The filter `s.state ==
V2StateOpen` is that gate: only an open session has had its token validated (in
`handleNoiseInit`'s accept branch). A `V2StateHandshakeComplete` session holds
CipherStates but never passed the token check — it is excluded, identical to the
gate `handlePush` (`v2session.go:1371`) and the spec's #571 security review enforce.

*Belt-and-suspenders, different fabric.* Even if this filter regressed, the consumer
calls `Push(ctx, id, env)` per returned id, and `Push` independently gates on
`V2StateOpen` (returning `ErrSessionNotOpen` for a non-open session) — so a stray
unauthenticated id would still receive **no** server output. Two deterministic,
independent code-level checks in two different methods (enumeration filter +
`handlePush` gate); neither is a stochastic agent rule. The enumeration filter is
the primary; `Push`'s gate is the net.

**Concurrency-safety (AC#3).** The funnel guarantees the map read runs on `Run`'s
goroutine, serialized against every map write — no torn read of `m.sessions`, no
data race that could corrupt session state. Race-tested by the AC#3 scenario above.

**Information disclosure.** conn IDs are non-secret routing identifiers, returned
only in-process; no secret-bearing field is touched. `handleActiveConnIDs` emits no
log line, so no conn-id-of-unauthenticated-session can leak into the log channel.

**DoS.** `ActiveConnIDs` is O(open sessions) on the Run goroutine. The only caller is
the trusted in-process assistant-turn bridge (one call per assistant turn); no
external party can trigger it. Not a meaningful vector. Noted, not actioned.

**Verdict: PASS.** The single security-relevant decision — the `V2StateOpen` filter —
is explicit in the AC, structurally enforced by reused code, and backed by `Push`'s
independent gate as a deterministic net. No secrets flow through the new surface; the
concurrency model is safe by construction and race-tested.

## Open questions

- **Future cross-goroutine `State()` readers.** `V2Session.State()`'s doc-comment
  (`v2session.go:216-220`) already flags that a non-Run reader of `s.state` would
  need `atomic.Int32` or a mutex. This spec's snapshot does **not** create such a
  reader — `handleActiveConnIDs` runs *on* Run. Left as-is; revisit only if a future
  slice needs a state read genuinely off the Run goroutine outside the funnel.
- **Per-session richer snapshot.** #572 needs only conn IDs (it builds the envelope
  itself and calls `Push`). If a later consumer needs device name / open-since
  timestamp per conn, the return type evolves to a small struct slice — out of scope
  here; `[]string` is the minimal surface the AC asks for.
