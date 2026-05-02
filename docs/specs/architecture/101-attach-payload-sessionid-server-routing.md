# Spec #101 — Phase 1.1e-C: `AttachPayload.SessionID` + `handleAttach` routes via `Pool.ResolveID`

**Status:** Architecture · **Size:** S · **Slice of:** Phase 1.1e (multi-session attach)
**Depends on:** #66 (`Pool.ResolveID` + `ErrAmbiguousSessionID` shipped)
**Unblocks:** Phase 1.1e-D (`pyry attach <id>` CLI surface)

---

## Context

Phase 1.1e wires multi-session attach end-to-end. #66 landed the resolver primitive (`Pool.ResolveID(arg) (SessionID, error)` + `ErrAmbiguousSessionID`). This slice is the wire + server-side half: an optional `sessionID` field on `AttachPayload`, and a `handleAttach` rewrite that routes through `Pool.ResolveID` before any bridge work.

The CLI consumer (`pyry attach <id>` positional, `attach_client.go` signature change, help text) is the next slice (1.1e-D). Until then the new field is reachable only via hand-crafted JSON or the test harness — that is intentional: wire + server is independently testable and independently shippable.

**Backwards compatibility is load-bearing.** A v0.5.x client talking to a v0.7.x server during the rollover window must keep working. The `omitempty` on `SessionID` is the contract that makes that true — empty-id `AttachPayload` marshals byte-identically to v0.5.x.

The seam already exists. `internal/control/server.go:340-342` carries the comment:

```go
// Phase 1.1 will swap "" → req.SessionID; the empty-id seam resolves
// to the bootstrap session today.
sess, err := s.sessions.Lookup("")
```

This spec is the swap.

## Design

### Wire shape

`internal/control/protocol.go`:

```go
type AttachPayload struct {
    Cols      int    `json:"cols,omitempty"`
    Rows      int    `json:"rows,omitempty"`
    SessionID string `json:"sessionID,omitempty"` // empty → bootstrap; full UUID or unique prefix per Pool.ResolveID
}
```

`omitempty` is the only acceptable tag. With it, an `AttachPayload{Cols: 80, Rows: 24}` (`SessionID` zero-valued) marshals to `{"cols":80,"rows":24}` — byte-identical to v0.5.x. Any other tag (`json:"sessionID"`, no tag, or `json:"sessionID,required"` if Go ever grows that) breaks v0.5.x byte-equivalence and is rejected.

The doc comment on `AttachPayload` should grow one paragraph naming `SessionID`'s shape (full UUID, unique prefix, or empty for bootstrap) and pointing at `Pool.ResolveID` for the resolution rules. Don't restate ResolveID's rules verbatim — link by name.

### `SessionResolver` interface evolution

`internal/control/server.go` defines `SessionResolver` as the consumer-side contract:

```go
type SessionResolver interface {
    Lookup(id sessions.SessionID) (Session, error)
}
```

Add one method:

```go
type SessionResolver interface {
    Lookup(id sessions.SessionID) (Session, error)
    // ResolveID maps a loose-input session selector (full UUID, unique
    // prefix, or empty for bootstrap) to a concrete SessionID. Errors
    // are returned verbatim — handleAttach wraps them as "attach: <err>".
    ResolveID(arg string) (sessions.SessionID, error)
}
```

Three production implementations need a 3-line addition each:

1. **`cmd/pyry/main.go` `poolResolver`** — adapter to `*sessions.Pool`. Trivial passthrough:
   ```go
   func (r poolResolver) ResolveID(arg string) (sessions.SessionID, error) {
       return r.p.ResolveID(arg)
   }
   ```

2. **`internal/control/server_test.go` `fakeResolver`** — needs a `resolveFn func(arg string) (sessions.SessionID, error)` field (default: return the configured single id with nil error, so existing tests using only `Lookup` are unaffected).

3. **`internal/control/server_test.go` `recordingResolver`** — delegates to embedded `SessionResolver`; add a passthrough.

No other call sites consume `SessionResolver` (verified by grep across `internal/control` and `cmd/pyry`).

### `handleAttach` routing

Current shape (server.go:339–346):

```go
func (s *Server) handleAttach(conn net.Conn, enc *json.Encoder) (handedOff bool) {
    sess, err := s.sessions.Lookup("")
    if err != nil {
        _ = enc.Encode(Response{Error: fmt.Sprintf("attach: %v", err)})
        return false
    }
    // ... clear deadline, Activate, Attach, etc.
}
```

The dispatcher at `server.go:282–305` already has `req` in scope but currently does not pass `req.Attach` into `handleAttach`. Two options:

- **(A)** Pass `req.Attach` (or just the resolved string) into `handleAttach`.
- **(B)** Pass the full `*Request`.

Pick **(A)** — `handleAttach` only needs the session selector, nothing else from the request. Concretely, change the signature to:

```go
func (s *Server) handleAttach(conn net.Conn, enc *json.Encoder, sessionID string) (handedOff bool)
```

and the call site to:

```go
case VerbAttach:
    var sessionID string
    if req.Attach != nil {
        sessionID = req.Attach.SessionID
    }
    if s.handleAttach(conn, enc, sessionID) {
        closeConn = false
    }
```

A nil `req.Attach` (no payload at all) is treated identically to an empty `SessionID` — both resolve to bootstrap. This preserves Phase 0 / v0.5.x clients that omit the payload entirely.

Rewritten body, top of `handleAttach`:

```go
id, err := s.sessions.ResolveID(sessionID)
if err != nil {
    _ = enc.Encode(Response{Error: fmt.Sprintf("attach: %v", err)})
    return false
}
sess, err := s.sessions.Lookup(id)
if err != nil {
    _ = enc.Encode(Response{Error: fmt.Sprintf("attach: %v", err)})
    return false
}
// ... unchanged: clear deadline, Activate, Attach, hand off conn.
```

Two-step (`ResolveID` then `Lookup`) over a single `ResolveSession`-style API — for two reasons:

1. **The 1.1e-A API ships `(SessionID, error)`, not `(*Session, error)`.** Spec #66 explicitly chose the smaller surface: "Returning `*Session` would tempt callers to skip the second lookup — but the second lookup is the lock-clean way to guard against a session being removed between resolve and use." Honour that decision; don't reopen it.
2. **Each call takes its own `Pool.mu` RLock.** Between the two locks a `Pool.Remove` could land — `Lookup` then returns `ErrSessionNotFound`, which encodes to the same `"attach: …"` error string the resolver itself produces. Window is microseconds; the operator's mental model ("you tried to attach to a session that no longer exists") is identical either way. No new locking required.

### Error encoding

All resolver / lookup errors flow through `fmt.Sprintf("attach: %v", err)`. The wrapped messages come through verbatim:

| Resolver outcome | Wire `Response.Error` |
|---|---|
| `ErrSessionNotFound` (no match) | `"attach: sessions: session not found"` |
| `ErrAmbiguousSessionID` (≥2 matches) | `"attach: sessions: ambiguous session id:\n<uuid> (<label>)\n<uuid> (<label>)"` |
| `Lookup` race after resolve | `"attach: sessions: session not found"` |

`errors.Is(err, sessions.ErrAmbiguousSessionID)` continues to match server-side because `fmt.Sprintf("%v", err)` only consumes the message — the original error never reaches the wire as a typed value. That's fine: clients match on the string today (and #66's wrapped format is already the documented client-facing UX).

The existing `sessions.ErrAttachUnavailable` mapping in `handleAttach` (server.go:373–376) is **untouched** — that path runs after `Attach`, not after `ResolveID`, so it cannot collide with this slice's error encoding. Same for the post-`Activate` path.

### Bridge state invariant

Both `ResolveID` and `Lookup` return before any bridge work — before `conn.SetDeadline(time.Time{})`, before `Activate`, before `Attach`. On the error path `handleAttach` returns `handedOff=false`, the dispatcher closes `conn`, and no `Session.Attach` was ever called. The bridge `attached` flag is unchanged. This is the invariant tests assert on (AC d/e: "before bridge open"), not just the response string.

## Concurrency model

No new goroutines, no new mutexes, no new lock-order edges.

- `s.sessions.ResolveID` takes `Pool.mu` (RLock) for the duration of the resolution scan (#66's design).
- `s.sessions.Lookup` takes `Pool.mu` (RLock) for the map lookup.
- Two sequential RLock acquires; releaseable concurrent readers proceed in parallel. Concurrent writers (`Rename`, `Create`, `Remove`, `RotateID`, `saveLocked`) serialise behind `Pool.mu` write — same as today.
- Concurrent attaches against **different** sessions remain fully parallel — `handleAttach` is per-conn, the dispatcher spawns one goroutine per `Accept`, and the only shared state is the `Pool` (lock-clean).
- Per-session `ErrBridgeBusy` continues to propagate from `internal/sessions.Session.Attach` unchanged. No pool-level locking introduced.

## Error handling

Three failure modes, all encoded as `"attach: <err>"` and all leaving the bridge untouched:

1. **Unknown id / no prefix match.** `ResolveID` returns `ErrSessionNotFound`.
2. **Ambiguous prefix.** `ResolveID` returns `ambiguousError(matches)` wrapping `ErrAmbiguousSessionID` — the multi-line `<uuid> (<label>)` list is part of the message and flows verbatim through `%v`.
3. **Resolve-then-Lookup race.** Session removed between the two RLocks. `Lookup` returns `ErrSessionNotFound`. Same encoding as case 1; the operator sees the same diagnostic.

After the two-step resolution, the existing failure modes (Activate timeout, `ErrAttachUnavailable`, `ErrBridgeBusy`) are unchanged.

## Testing strategy

`internal/control/server_test.go` (or a new `attach_resolve_test.go` if the existing file gets unwieldy — ~250+ lines after these additions; architect's call). Five cases per AC:

1. **`TestAttach_WireBackCompat_EmptySessionID`** — marshal `AttachPayload{Cols: 80, Rows: 24}`, assert `bytes.Equal(got, []byte(`{"cols":80,"rows":24}`))`. The literal is the v0.5.x baseline. Regression guard for `omitempty` accidentally being dropped from `SessionID`. Stdlib `encoding/json` only.

2. **`TestAttach_ResolvesByFullUUID`** — `fakeResolver` configured with two sessions (bootstrap + one minted). Send a `Request{Verb: "attach", Attach: {SessionID: "<full-uuid-of-minted>"}}`. Assert `recordingResolver` saw `ResolveID("<full-uuid>")` followed by `Lookup("<full-uuid>")`, server returned `{"ok":true}`, and the bridge attached to the minted session (not the bootstrap).

3. **`TestAttach_ResolvesByUniquePrefix`** — same setup, send a unique 8-char prefix. Assert `ResolveID(prefix)` returned the minted id and `Lookup(<minted-id>)` was called. Bridge attached to the minted session.

4. **`TestAttach_AmbiguousPrefix_ErrorBeforeBridge`** — `fakeResolver.resolveFn` returns `fmt.Errorf("%w:\n<uuid-a> (alpha)\n<uuid-b> (beta)", sessions.ErrAmbiguousSessionID)`. Send the ambiguous prefix. Assert:
   - `Response.Error == "attach: sessions: ambiguous session id:\n<uuid-a> (alpha)\n<uuid-b> (beta)"`
   - `fakeSession.attachCalls == 0` (or whatever counter the existing fake exposes — the bridge was never opened)
   - Connection closed by the server (not handed off — `handeOff=false` path).

5. **`TestAttach_UnknownID_ErrorBeforeBridge`** — `fakeResolver.resolveFn` returns `sessions.ErrSessionNotFound`. Send a non-matching id. Assert `Response.Error == "attach: sessions: session not found"` and `fakeSession.attachCalls == 0`.

The bridge-untouched assertion (AC explicitly: "asserted in tests via the bridge's pre/post state, not just by string-matching the response") needs the fake to expose either an `attachCalls int` counter or a `wasAttached bool` flag. Pick whichever is already in `fakeSession`; if neither, add a counter. The existing fake at server_test.go:23 has the structure for it.

End-to-end coverage from the existing `attach_test.go` is sufficient for the happy-path bridge-after-resolve flow — don't duplicate. Tests 2 and 3 above prove the resolver is wired correctly; the existing tests prove what happens after.

`go test -race ./...` and `go vet ./...` pass per AC. No new staticcheck concerns expected (one new exported method on a non-exported interface; nothing to flag).

## Open questions

None blocking.

- **Does `recordingResolver` need new assertion helpers?** Up to the developer. The existing one records `Lookup` calls; recording `ResolveID` symmetrically is the obvious extension.

## Out of scope (regression guards, not deliverables)

- No inline prefix matching anywhere in `internal/control`. `Pool.ResolveID` is the sole resolver. Reviewer should `grep -rn 'HasPrefix' internal/control/` and reject any match.
- No client-side change. `internal/control/attach_client.go` and `cmd/pyry/main.go` `runAttach` are untouched. The CLI surface lands in 1.1e-D.
- No refactor of 47-B / 48-B's inlined resolvers (rename / rm). Opportunistic, not a precondition.
- No minimum prefix length enforcement. Deferred until ergonomics complaint.
- No new sentinel errors. `ErrSessionNotFound` and `ErrAmbiguousSessionID` (both from `internal/sessions`) are the only ones that surface.
