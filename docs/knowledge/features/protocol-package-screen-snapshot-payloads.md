# Screen-snapshot payloads (#617)

**`ScreenSnapshotPayload` has no producer (#2540).** `TypeScreenSnapshot` is never emitted on the wire: #2540 deleted `handleRequestSnapshot`'s render arm and the `ScreenSnapshotter` seam behind it, because #2539 had already left production unable to reach that reply. Both types stay declared below — a future producer would not need to re-mint the shape — but a reader should not expect either to cross the wire today. `RequestSnapshotPayload` is still live: `request_snapshot` is answered, just never with a render — see [v2-session-manager-state-machine-inbound-screen-snapshot-handler-handlere.md](v2-session-manager-state-machine-inbound-screen-snapshot-handler-handlere.md).

The request/response pair behind ADR 025's always-available, parser-independent
**screen snapshot** — the floor of the safe-degradation strategy (ADR 025 § Safe
degradation) — was intended to let the phone ask for a one-shot text picture of the
current claude screen at any time. Spec source: `docs/protocol-mobile.md`
§ Screen snapshot. The pair maps 1:1 to the `Type*` constants `TypeRequestSnapshot`
(phone → binary control) and `TypeScreenSnapshot` (binary → phone event, unproduced).

```go
type RequestSnapshotPayload struct {
    ConversationID string `json:"conversation_id"`
}

type ScreenSnapshotPayload struct {
    ConversationID string    `json:"conversation_id"`
    Text           string    `json:"text"` // plain rendered text only; never raw control codes
    TS             time.Time `json:"ts"`
    Model          string    `json:"model"`  // #847: bootstrap session's per-session model override; "" = inherited default
    Effort         string    `json:"effort"` // #847: bootstrap session's per-session effort override; "" = inherited default
    YOLO           bool      `json:"yolo"`   // #847: bypass-permissions on/off; false = permissions enforced (fail-safe)
    UsedTokens     int       `json:"used_tokens"`   // #857: current context size on the latest usage-bearing entry, NOT a running total
    WindowTokens   int       `json:"window_tokens"` // #857: believed context-window size; 0 = no trustworthy reading (#2100)
}
```

- **`request_snapshot` is an inbound v2 *control* envelope, not an application event.**
  Structurally like `TypeRekeyRequest`: the v2 session manager intercepts it at the
  dispatch boundary **before** `dispatch.Route`. There is **no `dispatch.Route`
  handler** for it — the doc comment says so explicitly so the next reader does not
  look for a handler that isn't there. #618 wired the interception, the tui-driver
  render, and the `screen_snapshot` push; #2540 deleted the render arm, so the
  interception now answers only `conversation.not_found` / `server.binary_offline`
  and never renders or pushes anything.
- **`ScreenSnapshotPayload.Text` is plain rendered text only, NEVER raw terminal
  control codes.** This is the load-bearing invariant: it preserves ADR 025's
  no-raw-bytes guarantee and the substrate seal — the snapshot is a literal-screen
  picture rendered to text, not a stream of escape sequences. The struct doc comment
  states this.
- **`TS` is `time.Time` (RFC3339Nano on the wire).** Records when the snapshot was
  rendered. The monotonic-clock reading strips on JSON marshal, so tests compare with
  `time.Time.Equal`, never `==` or `reflect.DeepEqual` — same discipline as
  `Envelope.TS` and every other `time.Time` payload field.
- **No `omitempty` on any field — the same deliberate inverse as the #607 interactive
  payloads.** Every field is always present on the wire so the fixtures pin the full
  shape and boundary values (an empty `conversation_id`, a zero `ts`) cannot silently
  vanish. `TestSnapshotPayloads_EmptyConversationID` pins the empty-`conversation_id`
  boundary for both payloads.
- **Pure DTOs: no methods, no constructors, no `Validate()`.** Identical posture to
  every prior slice. Accepting the inbound frame, rendering the screen, and returning
  content to the remote party are the consumer's trust decision — that consumer (the
  screen-snapshot handler child) carries the `security-sensitive` label; this leaf
  declaration does not.
- **#847 adds `Model`/`Effort`/`YOLO`, always present (no `omitempty`), after `TS`.**
  They reflect the bootstrap session's persisted `SessionSettings` (`sessions.Pool.DefaultSettings()`,
  [sessions-package.md § `Pool.DefaultSettings`](sessions-package.md)) so the phone can
  render the current model / reasoning-effort / permissions posture. Empty `Model`/`Effort`
  = inherited daemon default (no per-session override); `YOLO: false` = permissions
  enforced (the fail-safe default). Field order is load-bearing: `roundTripEnvelope`
  compares `json.Compact`ed bytes (key order preserved, not sorted), so the fixture's
  payload key order must match struct declaration order exactly — `screen_snapshot.json`
  carries representative non-default values (`model:"opus"`, `effort:"high"`, `yolo:true`).
  Shipped unwired here (the handler serialized the three fields at their zero values);
  wired by #848 via the `SnapshotSettings` seam on `V2SessionConfig`, populating them
  from `Pool.DefaultSettings()`. #2540 deleted that seam along with the render arm
  that read it — see
  [Inbound `request_snapshot` handler](v2-session-manager-state-machine-inbound-screen-snapshot-handler-handlere.md).
  Not `security-sensitive` — read-only reflection of existing, non-secret session config.
  See [codebase/847.md](../codebase/847.md) and [codebase/848.md](../codebase/848.md).
- **#857 adds `UsedTokens`/`WindowTokens`, always present (no `omitempty`),
  after `YOLO`.** They reflect the bootstrap session's current context-window
  occupancy from [`internal/contextwindow.Read`](contextwindow-package.md)
  (#856): `UsedTokens` is the current context size on the transcript's latest
  usage-bearing entry (input + cache-read + cache-creation + output), **not**
  a running total — a post-compaction snapshot reports a smaller figure with
  no dedicated marker, since the reader is last-usage-wins. `WindowTokens` is
  the believed context-window size, or 0 when the daemon has no trustworthy
  reading (#2100) — which now covers **two** cases: the usage seam was not
  wired (foreground / unwired, reporting `(0, 0)`), and the used count came out
  *above* the window the daemon believed, which disproves the belief
  (reporting `(usedTokens, 0)` with `usedTokens` still the true current
  context size). Either way a client should treat "X of Y" as unavailable
  rather than divide by zero; the two are told apart by whether `used_tokens`
  is zero, not by `window_tokens` alone. This is distinct from a
  wired-but-fresh session, which reports `(0, 200000)`. See
  [contextwindow-package.md](contextwindow-package.md#context-window-size--a-believed-default-not-an-asserted-fact)
  for where the comparison lives. The two fields are sufficient for a client
  to compute "N% used (X of Y)" as `used_tokens / window_tokens`
  (pyrycode-desktop#182). Shipped unwired at #856 (the handler serialized both
  fields at their zero values); wired by #857 via the `SnapshotUsage` seam on
  `V2SessionConfig`. #2540 deleted that seam along with the render arm that
  read it — see
  [Inbound `request_snapshot` handler](v2-session-manager-state-machine-inbound-screen-snapshot-handler-handlere.md).
  Not `security-sensitive` — read-only reflection of two non-secret aggregate
  integers; the transcript content itself never crosses the wire.
  See [codebase/856.md](../codebase/856.md) and [codebase/857.md](../codebase/857.md).

Two golden round-trips in `snapshot_test.go` decode each fixture through `Envelope`
→ `Envelope.Payload` → per-type struct and re-marshal byte-equivalently via the shared
`roundTripEnvelope`. `screen_snapshot.json` carries a **multi-line** `text`
(`"line one\nline two\nline three\n"`, asserted with `strings.Contains(…, "\n")`) so
the canonical compare pins the escaped multi-line shape, and a whole-second payload
`ts` (`"2026-05-08T10:33:14Z"`) so the re-marshal is byte-identical — `time.Time`'s
RFC3339Nano output trims trailing fractional zeros, so a `.120Z` value would re-emit
as `.12Z` and break the compare (the same fixture-`ts` gotcha that governs the
envelope's own timestamp).
