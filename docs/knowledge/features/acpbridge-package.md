# `internal/acpbridge` — outbound turnevent → ACP `session/update` mapper

The **outbound ACP adapter** for epic [#600](https://github.com/pyrycode/pyrycode/issues/600)
(`pyry acp`). `MapUpdate` is a pure, exhaustive function that maps each neutral
[`turnevent.Event`](turnevent-package.md) to its Agent Client Protocol
`session/update` payload — or reports "no notification". It is the **exact ACP
mirror of** [`turnbridge`](turnbridge-package.md)'s outbound `MapEvent`
(#627): same neutral source, different framing. Where the mobile head wraps each
event in a v2 envelope, this maps it to an ACP `sessionUpdate`-discriminated
payload; and where the mobile adapter **drops** `ThoughtChunk`, ACP **maps** it
(to `agent_thought_chunk`).

[ADR 027](../decisions/027-acp-mapping.md) (§ Outbound table, § ACP taxonomy
reference) is the authoritative mapping contract this package implements. It is
the **pure value-to-value layer** of the ACP adapter (ticket #769, split from the
streaming consumer #750); everything stateful — which session an update belongs
to, holding the `session/prompt` call open for `TurnEnd`, writing `Stall` to
stderr, grouping chunks into a message — is the **consumer's** job
([#750](https://github.com/pyrycode/pyrycode/issues/750), blocked on this and not
yet built). Keeping those out is what makes `MapUpdate` table-testable and
isolates it from the ACP session lifecycle.

- Spec: [`specs/architecture/769-acp-outbound-mapper.md`](../../specs/architecture/769-acp-outbound-mapper.md).
- Ticket record: [codebase/769.md](../codebase/769.md).
- Contract: [ADR 027](../decisions/027-acp-mapping.md) — internal turn-event ↔ ACP mapping.
- Pivot model: [turnevent-package.md](turnevent-package.md) (#606) — what `MapUpdate` maps OUT of.
- Template / mobile mirror: [turnbridge-package.md § The outbound adapter](turnbridge-package.md#the-outbound-adapter-mapevent--buildturnstate) (#627).

## Files

```
internal/acpbridge/
├── outbound.go       session/update payload structs + discriminant consts + MapUpdate + mapToolContent / mapLocations helpers
└── outbound_test.go  table-driven MapUpdate exhaustiveness + golden-JSON wire-shape tests
```

One production file, one test file. No changes to any other package — the slice
is additive (a new package plus its tests), zero call-site blast radius.

## Import discipline

The package imports **only `internal/turnevent` + `encoding/json`**. This is
deliberate and enforced by the package doc:

- It does **NOT** import [`internal/acp`](acp-package.md) — the JSON-RPC transport
  floor, which is sealed to the standard library and "knows nothing about any
  concrete ACP method (session/*)". The mapper is pure and never touches the
  transport, so adding `session/update` method payloads (and a `turnevent`
  import) there would break that seal. The wire types are new, so they live with
  the mapper in `acpbridge`.
- It does **NOT** import `internal/protocol` (the mobile v2 wire types), exactly
  as [`turnevent`](turnevent-package.md)'s doc requires the two adapters to stay
  independent over one neutral model.

The mobile mirror keeps the same separation (`turnbridge` mapper ≠ `internal/protocol`
wire types); here the ACP wire types are new, so `acpbridge` owns both the types
and the mapping. No import cycle, independent table-testability. `acpbridge` is a
sibling of `turnbridge` and `modalbridge`, following the established `*bridge`
adapter-over-`turnevent` convention.

## The `session/update` payload structs

Outbound-only wire types (this package marshals them; it never unmarshals), so
JSON tagged unions are modelled as **flat structs with `omitempty`** — the mapper
sets only the fields a given variant carries. Field names are the ACP camelCase
strings verbatim.

```go
// The ACP `sessionUpdate` discriminant values.
const (
    SessionUpdateAgentMessageChunk = "agent_message_chunk"
    SessionUpdateAgentThoughtChunk = "agent_thought_chunk"
    SessionUpdateToolCall          = "tool_call"
    SessionUpdateToolCallUpdate    = "tool_call_update"
)
const MethodSessionUpdate = "session/update" // the notification method the consumer sends

type AgentMessageChunk struct { SessionUpdate string; Content ContentBlock }        // agent_message_chunk
type AgentThoughtChunk struct { SessionUpdate string; Content ContentBlock }        // agent_thought_chunk
type ContentBlock      struct { Type string; Text string `omitempty` }             // text-only today
type ToolCall struct {  // a new tool invocation; NO content (no result yet)
    SessionUpdate, ToolCallID, Title, Kind, Status string
    RawInput  json.RawMessage    `omitempty`   // opaque pass-through, never parsed here
    Locations []ToolCallLocation `omitempty`
}
type ToolCallUpdate struct {  // changed fields of an existing tool call
    SessionUpdate, ToolCallID string
    Status  string            `omitempty`      // omitted for a degenerate empty-status update
    Content []ToolCallContent `omitempty`      // nil for a status-only update
}
type ToolCallLocation struct { Path string; Line int `omitempty` }  // Line 1-based; 0 omitted
type ToolCallContent  struct { // flat tagged union: "content" | "diff" | "terminal"
    Type       string
    Content    *ContentBlock `omitempty`  // Type == "content" (text)
    Path, OldText, NewText string        `omitempty`  // Type == "diff"
    TerminalID string        `omitempty`  // Type == "terminal"
}
```

Seven exported wire types = the ACP domain-variant count (the sanctioned
data-model-taxonomy sizing exception — precedent `turnevent` #606 declared ~14
types and held S). `AgentMessageChunk` / `AgentThoughtChunk` are kept as **two
named variants** (not one shared struct) so each call site matches the ADR 027
taxonomy 1:1; the tiny duplication is deliberate.

**`ContentBlock` is text-only today.** `turnevent` builds only text/diff/terminal
tool content; ACP `image` / `resource` blocks have no `turnevent` shape yet (ADR
027, open item), so `ContentBlock` carries just `{type, text}`. New fields are
added when a producer emits them.

## `MapUpdate` — the mapping function

```go
// Pure, exhaustive over the sealed turnevent.Event; no I/O, no goroutine, no
// clock; safe on a zero-value / nil Event. The ACP mirror of turnbridge.MapEvent.
func MapUpdate(ev turnevent.Event) (update any, msgID string, ok bool)
```

| `ev` | `update` | `msgID` | `ok` |
|---|---|---|---|
| `TextChunk{MessageID,Text}` | `AgentMessageChunk{…, Content:{Type:"text", Text}}` | `MessageID` | `true` |
| `ThoughtChunk{MessageID,Text}` | `AgentThoughtChunk{…, Content:{Type:"text", Text}}` | `MessageID` | `true` |
| `ToolStart{…}` | `ToolCall{…, Kind:string(e.Kind), Status:"pending", RawInput, Locations}` | `""` | `true` |
| `ToolUpdate{…}` | `ToolCallUpdate{…, Status:string(e.Status), Content:mapToolContent(e.Content)}` | `""` | `true` |
| `TurnEnd` | `nil` | `""` | `false` |
| `Stall` | `nil` | `""` | `false` |
| `nil` / unknown (`default`) | `nil` | `""` | `false` |

- **`update` is `any`** — the payload structs share no marker interface; the
  consumer `json.Marshal`s it directly under the `session/update` params. The
  variant discriminant rides **inside** `update` as its `sessionUpdate` field, so
  there is no separate `typ` return (unlike the mobile envelope). The method is
  always `MethodSessionUpdate`.
- **`msgID` is returned out-of-band** — see [§ Message-id placement](#message-id-placement--out-of-band-not-a-wire-field). Non-empty only for the two chunk variants.
- **Kind / status are the ACP taxonomy strings verbatim** (`string(e.Kind)` /
  `string(e.Status)`). **No translation table** is needed because the
  `turnevent.ToolKind` / `ToolStatus` values already ARE the ACP strings
  ([taxonomy.go](turnevent-package.md#the-four-enums-taxonomygo)), and `other` is already
  the neutral fallback kind, so no unknown-kind coalescing happens here. A code
  comment records this at the `ToolStart` / `ToolUpdate` arms (AC-3).
- **`ToolStart` → `status: "pending"`.** `ToolStart` carries no status field, so a
  new `tool_call` is emitted with the ACP default `pending`
  (`turnevent.ToolStatusPending`), and no content (a new invocation has no result
  yet — content first appears via a `ToolCallUpdate`).
- **Zero-value / nil-safe.** A nil `ev` (or any impossible future sealed variant)
  falls to `default → (nil, "", false)`. The `default` arm is kept explicit so a
  new producer variant surfaces as a visible drop rather than silently vanishing —
  the same posture as the `turnbridge` template.

### Two exhaustive helpers

Both pure, both mirroring `turnbridge.resultSummary`'s sealed-switch discipline:

- **`mapToolContent(c turnevent.ToolContent) []ToolCallContent`** — switch over the
  sealed [`ToolContent`](turnevent-package.md) (the `content.go` text/diff/terminal sum):
  `TextContent → [{Type:"content", Content:{Type:"text", Text}}]`,
  `DiffContent → [{Type:"diff", Path, OldText, NewText}]`,
  `TerminalContent → [{Type:"terminal", TerminalID}]`, `nil`/`default → nil` (the
  legal status-only `ToolUpdate`, so the payload's `content` is omitted). The
  `default` arm is kept even though the type is sealed.
- **`mapLocations(locs []turnevent.Location) []ToolCallLocation`** — maps each
  `{Path,Line}`; returns `nil` for empty input so the payload's `locations` field
  is omitted.

## Design decisions

### Message-id placement — out-of-band, not a wire field

ACP's `agent_message_chunk` / `agent_thought_chunk` carry only
`{sessionUpdate, content}`; there is **no `messageId` field on the ACP wire**
(ADR 027 taxonomy + upstream spec — "grouped by a `messageId`" is a *semantic*
note, not a wire field). Marshalling the neutral model's `MessageID` into the
payload would emit a non-spec field and violate the "spec-correct `session/update`
frames" goal. So `MapUpdate` returns it as a **third value** (`msgID`), `""` for
the non-chunk variants. This preserves the id for the consumer to group chunks by
message while keeping every emitted `update` pure ACP. `TestMapUpdate_WireShape`
locks this: `agent_message_chunk` marshals to
`{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"hi"}}`
with **no `messageId`**. All *stateful* grouping is the consumer's (#750).

### The `{sessionId, update}` params wrapper is the consumer's

`MapUpdate` returns only the `update` object. The `sessionId` is
session-lifecycle addressing the consumer supplies (mirroring how the mobile
mapper leaves envelope addressing to `TurnContext` / the consumer). This package
defines `MethodSessionUpdate` for the consumer's convenience but not the params
struct.

### `TurnEnd` and `Stall` have no `session/update`

- **`TurnEnd`** maps to the `stopReason` **return** of `session/prompt`
  ([divergence 1](../decisions/027-acp-mapping.md#divergences)), not a
  notification. Because `turnevent.TurnEndReason` values already ARE the ACP
  `stopReason` strings, the consumer does `string(e.Reason)` directly when it
  resolves the held `session/prompt` call. No mapping belongs in this layer;
  `MapUpdate` reports `ok == false`.
- **`Stall`** is an internal-only quiet-but-not-idle onset marker; ACP has no home
  for it, so ADR 027 drops it and surfaces it on **stderr**. That's the consumer's
  job; `MapUpdate` reports `ok == false`. (`Stall` is not itself a numbered
  divergence.)

## Concurrency model

**None.** `MapUpdate`, `mapToolContent`, and `mapLocations` are pure functions —
no goroutine, no channel, no mutex, no clock read, no shared state. Reentrant and
race-free by construction; `go test -race` has nothing to catch. `context.Context`
is correctly absent (no long-running operation). Marshalling happens in the
consumer, not here, so a marshal failure is the consumer's concern; `RawInput` is
opaque `json.RawMessage` pass-through, never parsed, so a malformed blob cannot
fault the mapper. The function is total — no input is rejected, nothing panics.

## Not `security-sensitive`

A pure function over daemon-internal neutral events, **outbound only** — no
untrusted-party input, no auth/crypto, no transport, no dispatch policy. The
streaming I/O and any dispatch decision live in the consumer (#750). Labelling
this package would track data lineage rather than a security-relevant design
decision — the same posture as [`turnbridge`](turnbridge-package.md#not-security-sensitive)'s
outbound half.

## Consumer (deferred — not built here)

The **streaming adapter #750** subscribes to a session's `turnevent` stream, calls
`MapUpdate(ev)` per event, and — on `ok` — wraps the payload in a `session/update`
notification (`params = {sessionId: <consumer-owned>, update}`, using `msgID` for
its own chunk grouping); on `!ok` it resolves the held `session/prompt` with the
`TurnEnd` stopReason (via the T7 primitive #751) or writes the `Stall` to stderr.
#750 is blocked on this ticket and not yet built.

## Related

- [ADR 027](../decisions/027-acp-mapping.md) — the internal turn-event ↔ ACP
  mapping contract: § Outbound table (the four mapped variants + `TurnEnd`/`Stall`
  drops), § ACP taxonomy reference (`sessionUpdate` discriminants, tool
  kinds/statuses, content shapes), § Divergences 1 & 5.
- [turnevent-package.md](turnevent-package.md) (#606) — the neutral pivot model
  `MapUpdate` maps OUT of; its `ToolKind`/`ToolStatus`/`TurnEndReason` values ARE
  the ACP strings, which is why kind/status is identity.
- [turnbridge-package.md](turnbridge-package.md) (#627) — the mobile mirror /
  template: same `turnevent` source, v2-envelope framing, `ThoughtChunk` dropped
  (the divergence this package reverses). Shares the exhaustive-sealed-switch +
  pure-value-to-value discipline.
- [acp-package.md](acp-package.md) (#755/#756/#757/#761/#762/#747) — the sealed
  JSON-RPC transport floor this package deliberately does **not** import; the
  consumer #750 will drive `session/update` frames through it.
- [modalbridge-package.md](modalbridge-package.md) — sibling `*bridge` adapter
  over `turnevent` (the outbound modal surface); same import-discipline framing.
- Consumer: [#750](https://github.com/pyrycode/pyrycode/issues/750) (streaming
  adapter, blocked on this); T7 held-`session/prompt` primitive
  [#751](https://github.com/pyrycode/pyrycode/issues/751).
</content>
</invoke>
