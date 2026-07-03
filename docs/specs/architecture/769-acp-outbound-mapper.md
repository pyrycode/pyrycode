# Spec #769 — pure outbound mapper: `turnevent.Event` → ACP `session/update` payloads

**Ticket:** [#769](https://github.com/pyrycode/pyrycode/issues/769) · **Size:** S · **Security-sensitive:** no
**Epic:** #600 (`pyry acp`) · **Consumer:** #750 (streaming adapter, blocked on this) · **Contract:** [ADR 027](../../knowledge/decisions/027-acp-mapping.md)

## Files to read first

- `internal/turnbridge/outbound.go:49-104` — **the template.** `MapEvent(ev, tc) (typ, payload any, ok bool)`: exhaustive switch over the sealed `turnevent.Event`, pure value-to-value, `ok == false` for the no-wire-representation cases. Mirror its structure and doc-comment discipline exactly; ACP framing differs (see Design).
- `internal/turnbridge/outbound_test.go:14-187` — **the test template.** `TestMapEventOutbound`: one table, one case per sealed variant + drop cases, `reflect.DeepEqual` on the payload struct. Copy this shape.
- `internal/turnevent/event.go:23-103` — the sealed `Event` sum type and the four mapped variants' exact fields (`TextChunk{MessageID,Text}`, `ThoughtChunk{MessageID,Text}`, `ToolStart{ToolCallID,Title,Kind,RawInput,Locations}`, `ToolUpdate{ToolCallID,Status,Content}`) plus `TurnEnd`, `Stall`, and `Location{Path,Line}`.
- `internal/turnevent/taxonomy.go:9-32` — `ToolKind` / `ToolStatus` are string-backed and their values **are** the ACP strings; `ToolStatusPending == "pending"`, `ToolKindOther == "other"`. This is why kind/status "mapping" is `string(e.Kind)`, not a table.
- `internal/turnevent/content.go` — the sealed `ToolContent` sum (`TextContent{Text}`, `DiffContent{Path,OldText,NewText}`, `TerminalContent{TerminalID}`, `nil`). The `mapToolContent` helper switches over this exhaustively (mirror `resultSummary` in the template, `outbound.go:144-155`).
- `internal/modalbridge/modal.go:1-14` — sibling-package doc-comment style + the relay-free / import-discipline framing to echo (this package imports only `turnevent` + stdlib).
- ADR 027 §"Outbound" table + §"ACP taxonomy reference" (`docs/knowledge/decisions/027-acp-mapping.md:35-47,77-88`) — the authoritative outbound mapping + the `session/update` variant discriminants and `stopReason` values.
- **Exact ACP wire field names** (vault `structured-event-bridge-acp-mapping.md`, ported below so the developer needs no vault access): `sessionUpdate`, `content`, `toolCallId`, `title`, `kind`, `status`, `rawInput`, `locations` (`path`, `line`), tool content shapes `content`/`diff` (`path`,`oldText`,`newText`)/`terminal` (`terminalId`).

## Context

Epic #600 makes `pyry acp` a thin adapter over the neutral `turnevent` core (ADR 027). This ticket is the **pure value-to-value outbound layer**: it defines the ACP `session/update` payload structs and one exhaustive mapping function `turnevent.Event → session/update payload`. It is the exact mirror of the mobile head's `turnbridge.MapEvent` — same neutral source, different framing (ACP `sessionUpdate` discriminant vs. the mobile v2 envelope; `ThoughtChunk` **is** mapped here where mobile drops it).

The streaming adapter that subscribes to a session's event stream, wraps each payload in a `session/update` notification, and drives the transport is **#750** — this ticket's consumer, blocked on this one. Everything stateful (which session, holding the `session/prompt` call open for `TurnEnd`, writing `Stall` to stderr, grouping chunks into messages) is the consumer's job; this layer is a stateless function.

The neutral model was shaped ~90% like ACP, so this is near pass-through. `ToolKind`/`ToolStatus` values already **are** the ACP strings, so kind/status is identity, not a translation table.

## Design

### Package placement

New package **`internal/acpbridge`** — a sibling to `internal/turnbridge` and `internal/modalbridge`, following the established `*bridge` convention (adapter over the neutral `turnevent` model). One production file `outbound.go`, one test file `outbound_test.go`.

**Why a new package, not `internal/acp`.** `internal/acp` is the JSON-RPC transport floor; its package doc is explicit and deliberate — it "knows nothing about any concrete ACP method (session/*)" and "imports only the standard library." Adding `session/update` method payloads (and a `turnevent` import) there would break that seal. The mobile mirror keeps the same separation: `turnbridge` (mapper) is a distinct package from `internal/protocol` (wire types); here the wire types are new, so they live with the mapper in `acpbridge`. This package imports **only `turnevent` + `encoding/json`** — zero coupling to `internal/acp` (the mapper is pure and never touches the transport), so no import cycle and independent table-testability.

### The ACP `session/update` payload structs

Outbound-only wire types (this package marshals them; it never unmarshals), so JSON tagged unions are modelled as flat structs with `omitempty` — the mapper sets only the fields a given variant carries. Field names are the ACP camelCase strings verbatim.

Contract sketch (fields + tags; the developer writes the doc comments):

```go
// session/update variant discriminants (the ACP `sessionUpdate` field).
const (
    SessionUpdateAgentMessageChunk = "agent_message_chunk"
    SessionUpdateAgentThoughtChunk = "agent_thought_chunk"
    SessionUpdateToolCall          = "tool_call"
    SessionUpdateToolCallUpdate    = "tool_call_update"
)
const MethodSessionUpdate = "session/update" // the notification method the consumer sends

type AgentMessageChunk struct { SessionUpdate string `json:"sessionUpdate"`; Content ContentBlock `json:"content"` }
type AgentThoughtChunk struct { SessionUpdate string `json:"sessionUpdate"`; Content ContentBlock `json:"content"` }
type ContentBlock struct { Type string `json:"type"`; Text string `json:"text,omitempty"` } // text-only today
```

```go
type ToolCall struct {
    SessionUpdate string             `json:"sessionUpdate"`
    ToolCallID    string             `json:"toolCallId"`
    Title         string             `json:"title"`
    Kind          string             `json:"kind"`
    Status        string             `json:"status"`              // always "pending" from a ToolStart
    RawInput      json.RawMessage    `json:"rawInput,omitempty"`  // opaque pass-through
    Locations     []ToolCallLocation `json:"locations,omitempty"`
    // No Content: a new invocation has no result yet (content first appears via ToolUpdate).
}
type ToolCallUpdate struct {
    SessionUpdate string            `json:"sessionUpdate"`
    ToolCallID    string            `json:"toolCallId"`
    Status        string            `json:"status,omitempty"`  // omit if unset (degenerate)
    Content       []ToolCallContent `json:"content,omitempty"` // nil ToolContent → omitted
}
type ToolCallLocation struct { Path string `json:"path"`; Line int `json:"line,omitempty"` }
type ToolCallContent struct { // flat tagged union: "content" | "diff" | "terminal"
    Type       string        `json:"type"`
    Content    *ContentBlock `json:"content,omitempty"`  // Type=="content" (text)
    Path       string        `json:"path,omitempty"`     // Type=="diff"
    OldText    string        `json:"oldText,omitempty"`  // Type=="diff"
    NewText    string        `json:"newText,omitempty"`  // Type=="diff"
    TerminalID string        `json:"terminalId,omitempty"` // Type=="terminal"
}
```

Seven exported wire types = ACP domain-variant count (the sanctioned data-model-taxonomy exception; precedent `turnevent` #606 ~14 types held S). `AgentMessageChunk`/`AgentThoughtChunk` are kept as two named variants (not one shared struct) to match the ADR taxonomy 1:1 at call sites; the tiny duplication is deliberate.

### The mapping function

```go
// MapUpdate maps one neutral turnevent.Event to its ACP session/update payload,
// or reports "no notification". Pure: no I/O, no goroutine, no clock. Safe on a
// zero-value / unknown Event. Mirror of turnbridge.MapEvent.
func MapUpdate(ev turnevent.Event) (update any, msgID string, ok bool)
```

Behaviour, exhaustive over the sealed `turnevent.Event`:

| Event | `update` | `msgID` | `ok` |
|---|---|---|---|
| `TextChunk{MessageID,Text}` | `AgentMessageChunk{…, Content: ContentBlock{Type:"text", Text}}` | `MessageID` | `true` |
| `ThoughtChunk{MessageID,Text}` | `AgentThoughtChunk{…, Content: ContentBlock{Type:"text", Text}}` | `MessageID` | `true` |
| `ToolStart{…}` | `ToolCall{…, Kind: string(e.Kind), Status: string(turnevent.ToolStatusPending), RawInput, Locations: mapLocations(…)}` | `""` | `true` |
| `ToolUpdate{…}` | `ToolCallUpdate{…, Status: string(e.Status), Content: mapToolContent(e.Content)}` | `""` | `true` |
| `TurnEnd` | `nil` | `""` | `false` |
| `Stall` | `nil` | `""` | `false` |
| `nil` / unknown (`default`) | `nil` | `""` | `false` |

- **`update` is `any`** — mirrors the template (the payload structs share no marker interface; the consumer `json.Marshal`s it directly under `params.update`). The method is always `MethodSessionUpdate`, and the variant discriminant rides *inside* `update` as `sessionUpdate` — so there is no separate `typ` return (unlike the mobile envelope).
- **`msgID` is returned out-of-band** — see Open Questions §1 for why this, not a wire field.
- Two small exhaustive helpers, both pure, both mirroring `resultSummary`'s sealed-switch discipline:
  - `mapToolContent(c turnevent.ToolContent) []ToolCallContent` — switch over the sealed `ToolContent`: `TextContent → [{Type:"content", Content:&ContentBlock{Type:"text",Text}}]`, `DiffContent → [{Type:"diff", Path, OldText, NewText}]`, `TerminalContent → [{Type:"terminal", TerminalID}]`, `nil`/`default → nil` (status-only update, `content` omitted). Keep the `default` arm even though the type is sealed (a future producer variant must not silently vanish — same posture as the template).
  - `mapLocations(locs []turnevent.Location) []ToolCallLocation` — map each `{Path,Line}`; return `nil` for empty (so `locations` is omitted).
- **Required comment (AC-3):** at the `ToolStart`/`ToolUpdate` arms, record that kind/status are emitted as the ACP taxonomy strings verbatim (`string(e.Kind)` / `string(e.Status)`), that **no translation table is needed** because the `turnevent` enum values already ARE the ACP strings, and that `other` is already the neutral fallback kind so no unknown-kind coalescing is needed here.

### Data flow

```
session turnevent stream ──▶ (consumer #750, per event) ──▶ acpbridge.MapUpdate(ev)
                                                                  │
                                       ┌── ok=true  ──────────────┤ update, msgID
                                       │   consumer wraps: session/update notification
                                       │   params = { sessionId: <consumer-owned>, update }
                                       │   (consumer uses msgID for its own chunk grouping)
                                       │
                                       └── ok=false ── TurnEnd → resolve held session/prompt
                                                       Stall   → stderr           (both #750's job)
```

## Concurrency model

None. `MapUpdate` and both helpers are pure functions — no goroutine, no channel, no mutex, no clock read, no shared state. Reentrant and race-free by construction. `go test -race` has nothing to catch here; the guarantee is structural.

## Error handling

No error path. The function is total over its input:
- Every one of the six sealed `turnevent.Event` variants has an explicit arm; `nil` and any (impossible) unknown variant fall to `default → (nil, "", false)`.
- No input is rejected, no error is returned, nothing panics. `RawInput` is opaque pass-through (`json.RawMessage`), never parsed here, so a malformed blob cannot fault the mapper.
- Marshalling happens in the consumer, not here; a marshal failure is the consumer's concern.

## Testing strategy

Same-package table test in `outbound_test.go`, mirroring `TestMapEventOutbound`. `t.Parallel()` on the test and each subtest; `reflect.DeepEqual` for payload comparison (the payloads contain a `json.RawMessage` slice, so `==` won't work).

**`TestMapUpdate` — one case per sealed variant + drops (this is the exhaustiveness assertion, AC-4):**
- `TextChunk` → `AgentMessageChunk{SessionUpdate:"agent_message_chunk", Content:{Type:"text", Text:"hi"}}`, `msgID=="m1"`, `ok`.
- `ThoughtChunk` → `AgentThoughtChunk{…"agent_thought_chunk"…}`, `msgID=="m9"`, `ok` (asserts ThoughtChunk is **not** dropped here — the mobile divergence).
- `ToolStart` with kind `execute`, a `rawInput` blob, one location → `ToolCall` with `kind:"execute"`, **`status:"pending"`** (AC-2 explicit assertion), `rawInput` preserved verbatim, `locations` mapped, `msgID==""`.
- `ToolUpdate` status `completed` + `TextContent` → `ToolCallUpdate{status:"completed", content:[{type:"content", content:{type:"text", text}}]}`.
- `ToolUpdate` status `failed` + `nil` content → `ToolCallUpdate{status:"failed", content:nil}` (status-only, content omitted).
- `ToolUpdate` + `DiffContent` → `[{type:"diff", path, oldText, newText}]`; + `TerminalContent` → `[{type:"terminal", terminalId}]` (exercises every `mapToolContent` arm).
- `TurnEnd{end_turn}` → `(nil, "", false)`.
- `Stall{}` → `(nil, "", false)`.
- `nil` event → `(nil, "", false)` (zero-value safety, AC-5).
- Loop invariant asserted every case: when `!ok`, `update == nil` **and** `msgID == ""`.

**`TestMapUpdate_WireShape` — lock the camelCase field names (the actual contract).** `json.Marshal` the `update` for one of each variant and string-compare against a golden JSON literal. This is what guarantees `sessionUpdate`/`toolCallId`/`rawInput`/`oldText`/`terminalId` don't drift and — critically — that **`agent_message_chunk` marshals to `{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"hi"}}` with no `messageId` field** (Open Questions §1). Set `enc.SetEscapeHTML(false)` if using an encoder, or compare with `json.Marshal` (default) and pick text without `<>&`.

**Green bar:** `make check` (`go vet` + `staticcheck` + `go test -race ./...`) passes (AC-5).

## Open questions (resolved decisions)

1. **Message-id placement → out-of-band return, not a wire field.** ACP's `agent_message_chunk` / `agent_thought_chunk` carry only `{sessionUpdate, content}`; there is **no `messageId` field on the ACP wire** (ADR 027 taxonomy + upstream spec — "grouped by a `messageId`" is a semantic note, not a wire field). Putting the neutral model's `MessageID` into the marshalled payload would emit a non-spec field and violate the user story's "spec-correct `session/update` frames." So `MapUpdate` returns it as a third value (`msgID`), `""` for the non-chunk variants. This honours AC-1 ("the message id is preserved so the consumer can group chunks by message") and the Technical Note's explicit "returned out-of-band … is the architect's decision," while keeping every emitted `update` pure ACP. The consumer (#750) owns all *stateful* grouping.
2. **`{sessionId, update}` params wrapper is the consumer's, not this ticket's.** `MapUpdate` returns only the `update` object; the `sessionId` is session-lifecycle addressing the consumer supplies (mirrors how the mobile mapper leaves envelope addressing to `TurnContext`/the consumer). This ticket defines `MethodSessionUpdate` for the consumer's convenience but not the params struct.
3. **`TurnEnd`'s `stopReason` is not mapped here.** `TurnEnd` maps to the `session/prompt` **return** (divergence 1), and since `turnevent.TurnEndReason` values already ARE the ACP `stopReason` strings, the consumer does `string(e.Reason)` directly. No mapping function belongs in this layer; `MapUpdate` correctly reports "no `session/update`" (`ok == false`) for it.
4. **Image/resource content blocks are out of scope.** `turnevent`'s `ToolContent` builds only text/diff/terminal today (`content.go`); ACP `image`/`resource` blocks have no `turnevent` shape yet (ADR 027). `ContentBlock` is text-only; new fields are added when the producer emits them.
