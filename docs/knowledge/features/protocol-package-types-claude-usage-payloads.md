# Claude usage allowance payloads (#3575)

Wire vocabulary only, in `internal/protocol/claude_usage.go` — the request a
paired client sends to ask for the daemon's latest Claude usage allowance
reading, and the daemon's reply. Mirrors the [Claude account
payloads](protocol-package-types-claude-account-payloads.md) shape: declared
and wired in the same ticket, both read-only, both answered by authenticated
paired-client map dispatch with no conversation lookup and no `interactive`
gate. See [claude-usage-reading.md](claude-usage-reading.md) for the daemon
store this reply projects. Published contract: [`docs/protocol-mobile.md` §
Claude usage allowance
reading](../../protocol-mobile.md#claude-usage-allowance-reading).

```go
type RequestClaudeUsagePayload struct{}

type ClaudeUsagePayload struct {
    State      string             `json:"state"`
    ObservedAt *time.Time         `json:"observed_at,omitempty"`
    FiveHour   *ClaudeUsageWindow `json:"five_hour,omitempty"`
    SevenDay   *ClaudeUsageWindow `json:"seven_day,omitempty"`
}

type ClaudeUsageWindow struct {
    Utilization *float64 `json:"utilization,omitempty"`
    ResetsAt    *int64   `json:"resets_at,omitempty"`
}

const CapabilityClaudeUsage = "claude_usage"
```

`State` is `ClaudeUsageStateNone` (`"none"`) or `ClaudeUsageStateObserved`
(`"observed"`); a client must accept an unknown value. `State` always
serializes; every other field uses a pointer with `omitempty`, so `none`
produces exactly `{"state":"none"}` and an absent window or an absent
`utilization`/`resets_at` drops only that one key — never a `null` and never
a synthesized `0`. **`omitempty` on a pointer omits only `nil`**, so a
`Utilization` pointing at `0.0` still serializes as `"utilization":0`; this
is why the type uses pointers throughout rather than plain `float64`/`int64`,
the same reasoning [`RateLimitedPayload.Utilization`](protocol-package-rate-limited-event-payload.md)
already established for the sibling frame. `TestClaudeUsagePayload_Wire`
pins three shapes byte-for-byte: `none`, both windows present, and one window
present with the other key explicitly zero.

`ObservedAt` is a `*time.Time`, not a Unix integer — unlike
`RateLimitedPayload.ResetsAt`, this value is the **daemon's own clock**, not
claude's, so there is no reason to avoid the richer type the way that field's
doc explains for a claude-authored number. It marshals as RFC 3339 UTC
(`"2026-10-17T12:00:00Z"`).

Every number inside a window is claude's own, carried verbatim and
unvalidated, with the same two warnings
[`rate_limited`](../../protocol-mobile.md#rate_limited) and
[its payload doc](protocol-package-rate-limited-event-payload.md) already
give for `Utilization`/`ResetsAt`: `utilization` has been observed as a
0-to-1 fraction but is not clamped, and a `resets_at` in the past passes
through unchanged. Absence and `0` are different facts on both fields, and a
client collapsing the two will misrender a fresh window as an exhausted one
or the reverse.

`TypeRequestClaudeUsage = "request_claude_usage"` / `TypeClaudeUsage =
"claude_usage"`, filed in `v2OnlyTypes` and the partition test's `all`
slice; only the request joins `inboundAppTypeSet` — `IsKnownAppType` accepts
`request_claude_usage` inbound and rejects `claude_usage` — the same shape
the [claude account pair](protocol-package-types-claude-account-payloads.md)
uses. `CapabilityClaudeUsage` is appended to `supportedV2Capabilities`
(`internal/relay/v2session_handshake.go`); it is declared in this file,
beside the frames it detects, rather than in `handshake.go` — see
[claude-usage-reading.md § the capability constant's
location](claude-usage-reading.md#the-capability-constant-lives-beside-the-frames-it-detects-not-in-handshakego)
for why.

No error code is declared for this family, for the same reason the claude
account pair has none: the reply only ever projects an in-memory snapshot,
so there is nothing a read of it can fail to do beyond the generic
`protocol.malformed` a decode failure on the request already gives.

## Related

- [Claude account source status payloads](protocol-package-types-claude-account-payloads.md) —
  the nearest sibling, declared and answered the same way one ticket
  earlier; its "no error code for a read-only pair" rule is the one this
  pair follows too.
- [Rate-limited event payload](protocol-package-rate-limited-event-payload.md) —
  the `*float64`-with-`omitempty` absent-vs-zero pattern this type reuses,
  and the independent `unifiedWindows` decode this pair's producer needed to
  avoid disturbing that frame's own decode.
- [claude-usage-reading.md](claude-usage-reading.md) — the daemon-side
  store, parser seam and wiring this reply projects.
