# `RoutingEnvelope`

The relay-prepended `{conn_id, frame}` wrapper used on the binary↔relay leg only (spec § Routing envelope, lines 100–122). Phones never see it. The relay strips it before forwarding to phones and prepends it before forwarding to the binary.

```go
type RoutingEnvelope struct {
    ConnID    string          `json:"conn_id"`
    Frame     json.RawMessage `json:"frame"`
    Token     string          `json:"token,omitempty"`        // #308; phone→binary, first frame per conn_id only
    CloseCode uint16          `json:"close_code,omitempty"`   // #308; binary→relay only
}
```

`Frame` is `json.RawMessage` so the relay can splice without parsing payloads — a structural property of the design (the relay holds zero per-user state). The `routing_envelope.json` round-trip test pins the byte-preservation invariant: a future change to typed `*Envelope` for `Frame` would surface as a fixture mismatch.

`Token` (#308) carries the phone's device-pairing token from the relay to the binary on the **first** frame for a given `ConnID` only. Empty on subsequent frames and on every binary→phone frame. Populated by the relay from the `x-pyrycode-token` HTTP header at WS upgrade; its original consumer, `relay.AuthenticateFirstFrame` via the dispatcher's `FirstFrameGate` (#308), was deleted by #1040 (zero production callers since #913 slice 1) — the field is retained on the wire type with no live consumer, its own removal deferred as separate follow-on scope (see [codebase/1040.md](../codebase/1040.md)). **SECURITY:** plaintext credential material — no layer may log it. `TestRoutingEnvelope_TokenOmitempty` pins the omitempty wire shape.

`CloseCode` (#308), when non-zero on a binary→relay routing envelope, asks the relay to forward `Frame` (if non-empty) to the phone and then close that phone's WS with this WS close code. Used today for the auth-reject path (4401) and other binary-side close intents (idle sweep 4408, rekey-failure 4426, etc.).

**Since #2601, a non-zero `CloseCode` on an inbound (relay→binary) envelope is meaningful too**: it is the relay's report that the named `conn_id`'s phone WebSocket already ended (`docs/protocol-mobile.md` § Routing envelope, relay side pyrycode-relay#152). `V2SessionManager.handleFrame` checks it ahead of both the lazy session create and the `lastActivityAt` stamp, tears any live session down with no reply via `handlePeerClose`, drops a notice for an unknown `conn_id` silently, and never decodes `Frame` on this path. Before #2601 the dispatcher ignored inbound `CloseCode` outright; a relay that never sends the notice still behaves exactly as before, reaped only by the idle sweep. `TestRoutingEnvelope_CloseCodeOmitempty` pins the omitempty wire shape.
