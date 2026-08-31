# Sessions: has-id seam (1.3c-1)

The fifth `sessions.*` verb is `sessions.has-id` — a one-bit existence query. The 1.3c-2 foreground auto-attach path uses it to decide whether to attach to an existing daemon session under a known UUID or spawn a fresh supervised claude. Cheaper than `sessions.list` for the single-id case (~36-byte input + ~30-byte output instead of an O(N) registry table).

### Why no new sub-interface

Unlike `Remover` / `Renamer` / `Lister` / `GetOrCreator`, `sessions.has-id` introduces **no** new Sessioner sub-interface. The registry-read primitive it needs — "is this id registered?" — is already reachable through the existing `SessionResolver.Lookup` seam: a known id returns `(*Session, nil)`, an unknown id returns `(nil, ErrSessionNotFound)`. `*sessions.Pool` already satisfies this through the `poolResolver` adapter. Adding a parallel `HasIDer` interface + a parallel `Pool.HasID` method would double the surface for the same map read.

The handler reaches for `s.sessions` (the existing `SessionResolver`) — not `s.sessioner`. As a consequence, `VerbSessionsHasID` answers correctly even when `sessioner == nil`. `NewServer`'s signature is unchanged.

### Wire shape

`Request.Sessions.ID` carries the UUID to query — the same field `sessions.rm` and `sessions.rename` already use for "the session this verb operates on". No new `SessionsPayload` field. Response gains one omitempty field:

```go
type Response struct {
    Status        *StatusPayload       `json:"status,omitempty"`
    Logs          *LogsPayload         `json:"logs,omitempty"`
    SessionsNew   *SessionsNewResult   `json:"sessionsNew,omitempty"`
    SessionsList  *SessionsListPayload `json:"sessionsList,omitempty"`
    SessionsHasID *SessionsHasIDResult `json:"sessionsHasID,omitempty"` // 1.3c-1
    OK            bool                 `json:"ok,omitempty"`
    Error         string               `json:"error,omitempty"`
    ErrorCode     ErrorCode            `json:"errorCode,omitempty"`
}

type SessionsHasIDResult struct {
    Has bool `json:"has"` // not omitempty — see below
}
```

`SessionsHasIDResult` is defined in `protocol.go` so external Go callers don't transitively import `internal/sessions`.

### Why `Has` is **not** omitempty

`omitempty` elides the boolean zero value. With `Has bool` + `omitempty`, the wire shape for an absent session would be `{"sessionsHasID":{}}` — indistinguishable from a malformed response that forgot to populate the field. Emitting the boolean unconditionally costs ~10 bytes and produces an unambiguous shape: `{"sessionsHasID":{"has":false}}` for absent, `{"sessionsHasID":{"has":true}}` for present. The wrapper-pointer's omitempty on `Response.SessionsHasID` is sufficient to keep other verbs' byte-output unchanged.

### Validation gate (strict UUID)

The handler rejects malformed input at the seam boundary, *before* calling `Lookup`:

| Input | Wire shape |
|---|---|
| Missing payload / empty ID | `Response{Error: "sessions.has-id: missing id"}` |
| Non-UUIDv4 string | `Response{Error: "sessions.has-id: invalid uuid"}` (via `sessions.ValidID`) |
| Well-formed UUID, present in registry | `Response{SessionsHasID: &SessionsHasIDResult{Has: true}}` |
| Well-formed UUID, absent | `Response{SessionsHasID: &SessionsHasIDResult{Has: false}}` |
| `Lookup` returns non-`ErrSessionNotFound` (defensive) | `Response{Error: "sessions.has-id: <err>"}` |

`Pool.Lookup` would already return `ErrSessionNotFound` for any non-canonical string, but failing fast at the seam distinguishes "client typed garbage" from "well-formed UUID that happens to be absent" — the AC's contract.

### No prefix resolution

Unlike attach (which calls `ResolveID` to accept loose-input prefixes), `sessions.has-id` is strict-UUID by design: the verb answers "given the canonical UUID I expect, does it exist?" and prefix ambiguity is meaningless in that context. Malformed input is an error, not a `Has: false`.

### No typed sentinels, no context.WithTimeout

`Pool.Lookup`'s only typed error is `ErrSessionNotFound`, and that case is *not* propagated as an error — it's the absence-signal the verb returns as `Has: false`. Boundary-validation errors are plain strings; no new `ErrorCode` is introduced. The handler runs no `context.WithTimeout`: `Pool.Lookup` is an O(1) map read under `Pool.mu` RLock, and the conn's handshake deadline already bounds slow-write clients.

### Concurrency

`handleSessionsHasID` runs on the per-conn goroutine; no new goroutines spawned. Lock order is `Pool.mu` (RLock) only — no `Session.lcMu` involvement, no new lock-order edges. Read consistency: a session minted between the handler's read and the client's decode will not appear; one removed will still appear. Acceptable for a yes/no query — the caller (1.3c-2's startup detection) treats the answer as a decision input, not a durable contract.

See `docs/specs/architecture/157-control-sessions-has-id.md` for the full ticket-time design; this section is the canonical evergreen reference.
