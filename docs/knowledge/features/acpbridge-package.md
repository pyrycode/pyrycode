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
to, signalling `TurnEnd` to the held `session/prompt` call, writing `Stall` to
stderr, grouping chunks into a message — is the **consumer's** job
([#750](https://github.com/pyrycode/pyrycode/issues/750), now built:
[acp-package.md § Outbound streaming adapter](acp-package.md#outbound-streaming-adapter-acpturnstream-750)).
Keeping those out is what makes `MapUpdate` table-testable and isolates it from
the ACP session lifecycle.

- Spec: [`specs/architecture/769-acp-outbound-mapper.md`](../../specs/architecture/769-acp-outbound-mapper.md).
- Ticket record: [codebase/769.md](../codebase/769.md).
- Contract: [ADR 027](../decisions/027-acp-mapping.md) — internal turn-event ↔ ACP mapping.
- Pivot model: [turnevent-package.md](turnevent-package.md) (#606) — what `MapUpdate` maps OUT of.
- Template / mobile mirror: [turnbridge-package-outbound-adapter.md § The outbound adapter](turnbridge-package-outbound-adapter.md) (#627).

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
| `BackgroundTaskStarted{…}` | `BackgroundTaskStarted{…, TaskID, ToolCallID, Description, TaskType, TruncatedFields}` (#1402) | `""` | `true` |
| `BackgroundTaskUpdated{…}` | `BackgroundTaskUpdated{…, TaskID, Patch, TruncatedFields}` (#1402) | `""` | `true` |
| `BackgroundTaskRoster{…}` | `BackgroundTaskRoster{…, Tasks:mapBackgroundTasks(e.Tasks), DroppedTasks}` (#1402) | `""` | `true` |
| `TurnEnd` | `nil` | `""` | `false` |
| `Stall` | `nil` | `""` | `false` |
| `ThinkingProgress` / `ApiRetry` / `Compacting` / `Unrecognized` / `PermissionRequest` (`default`) | `nil` | `""` | `false` |
| `nil` | `nil` | `""` | `false` |

`turnevent.Event` is a sealed sum of **14** variants (`PermissionRequest`'s marker
sits in `internal/turnevent/permission.go`, outside `event.go`'s own — stale —
enumeration). After #1402, `MapUpdate` names **9**: 7 return `ok == true` (the
four original plus the three background-task arms above), and `TurnEnd`/`Stall`
are matched by name for a documented reason and return `ok == false`. The other
**5** named, existing variants fall to `default:` — `ThinkingProgress`,
`ApiRetry`, `Compacting`, `Unrecognized` (all built by `streamsup`'s parser) and
`PermissionRequest` (built in `internal/modalbridge`, travelling the **modal**
path rather than the `Event` stream, divergence 2 / #752) — plus a nil `Event`,
which is not itself a variant. None of the five is future or impossible; the
`default` arm stays explicit only so a genuinely *new* producer variant surfaces
as a visible drop rather than silently vanishing.

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
- **Zero-value / nil-safe.** A nil `ev` falls to `default → (nil, "", false)`
  alongside the five named variants above — see the `default:` set right after
  the mapping table.

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

## Background-task payload shapes — declared and wired ([#1401](https://github.com/pyrycode/pyrycode/issues/1401), [#1402](https://github.com/pyrycode/pyrycode/issues/1402))

claude's background-task lifecycle (`system/task_started` / `task_updated` /
`background_tasks_changed`, modelled neutrally as `turnevent.BackgroundTaskStarted`
/ `BackgroundTaskUpdated` / `BackgroundTaskRoster`) already reaches a **mobile**
client ([turnbridge-package.md](turnbridge-package.md), #1393/#1394) but had no
ACP shape at all until #1401. #1401 declared four new exported types in
`outbound.go`; #1402 wired the three `MapUpdate` arms (§ above) plus a
`mapBackgroundTasks` helper (§ below) that maps each roster entry. This was the
same declare-then-wire split #1393 (types) → #1394 (consumer) used on the mobile
lane.

**The remaining gap is the producer, not the mapper.** Nothing on the ACP lane
emits any of the three neutral events yet — `turnbridge.mapEvent`
(`internal/turnbridge/mapper.go`) has no background-task arm, so a desktop client
still sees nothing until [#1400](https://github.com/pyrycode/pyrycode/issues/1400)
gives this lane a producer. `MapUpdate` having an arm is not the lane having a
producer; the two facts stay separate (ADR 027 divergence 7).

**No in-taxonomy discriminant fits.** ACP's ten `sessionUpdate` variants have no
background-task shape, and reusing one of the four generic ones was rejected on a
dominance argument, not passed over: a **strict** host (serde internally-tagged
enum, zod discriminated union) rejects a `tool_call` body missing `toolCallId`/
`title`/`kind`/`status` just as hard as it rejects an unknown discriminant, while a
**lenient** host *believes* it — a phantom tool call appears in its tool view with
an empty, cross-variant-colliding `toolCallId`, which is strictly worse than the
facts being dropped. So `outbound.go` declares three **pyry extension**
discriminants in their own `const` block (the package-doc claim that the existing
four "ARE the ACP wire strings" must stay true of only the four it covers):

```go
const (
    SessionUpdateBackgroundTaskStarted = "pyry/background_task_started"
    SessionUpdateBackgroundTaskUpdated = "pyry/background_task_updated"
    SessionUpdateBackgroundTaskRoster  = "pyry/background_task_roster"
)
```

The `pyry/` prefix is deliberate, not decoration: a bare `background_task_started`
is shaped exactly like the ten spec strings, so it would collide silently with any
future spec-added variant of that name and be indistinguishable from spec truth in
a wire log. `namespace/name` mirrors ACP's own method shape (`session/update`,
`fs/read_text_file`), reading as in-protocol rather than malformed. [ADR 027
divergence 7](../decisions/027-acp-mapping.md) records the full argument
(including the residual, unmeasured risk that a strict host rejects — or tears
down the connection over — an unknown discriminant) and is these strings' only
home; they are deliberately **absent** from § "ACP taxonomy reference", which is a
verbatim port of the external spec.

```go
type BackgroundTaskStarted struct { SessionUpdate, TaskID, ToolCallID, Description, TaskType string; TruncatedFields []string `json:"truncatedFields,omitempty"` }
type BackgroundTaskUpdated struct { SessionUpdate, TaskID, Patch string; TruncatedFields []string `json:"truncatedFields,omitempty"` }
type BackgroundTaskRoster  struct { SessionUpdate string; Tasks []BackgroundTask `json:"tasks"`; DroppedTasks int `json:"droppedTasks"` }
type BackgroundTask        struct { TaskID, TaskType, Description string; TruncatedFields []string `json:"truncatedFields,omitempty"` } // roster entry, no discriminant
```

Notable divergences from the mobile lane's equivalent types
(`protocol.BackgroundTask*Payload`, `interactive.go:149-280`): **no
`ConversationID`** on any of the three (ACP session addressing is the consumer's
`sessionUpdateParams.SessionID`, not the payload's), and `TruncatedFields` carries
`omitempty` here (mobile ships `null` for nil / `[]` for empty on the same field;
both readings mean "nothing was cut" and no consumer branches on the difference,
so `omitempty`'s "both vanish" is a *stronger* realisation of that equivalence,
and matches this package's own optional-list convention, e.g. `locations`).

**`Tasks` is normalised, `TruncatedFields` is not.** `BackgroundTaskRoster` has a
value-receiver `MarshalJSON` that substitutes a nil `Tasks` for `[]BackgroundTask{}`
before marshalling (via a `type alias` indirection, to avoid recursion), so an
empty roster always serialises as `"tasks":[]` — never an omitted key, never
`"tasks":null`. That empty array **is the payoff of the feature**: it is the
positive "nothing is alive" signal a desktop client needs to distinguish a turn
that ended with background work still running from a genuine finish (#1240's
symptom). Two things that would look like the fix but aren't: dropping
`omitempty` from the `tasks` tag alone still ships `null` for a nil slice (the
tag decides key-presence, not nil-vs-empty rendering); and `turnevent.
BackgroundTaskRoster.Tasks` is nil both for an empty roster and when claude omits
the key, never an empty non-nil slice, so `MarshalJSON` is the *only* place this
normalisation can live — a `MapUpdate` arm that pre-allocated an empty slice
instead would produce identical bytes while hiding the guarantee inside a mapper
branch (the same call [turnbridge](turnbridge-package.md) already made on the
mobile side). The receiver must stay a **value** receiver: `MapUpdate` will box
payloads into an `any`, and `json.Marshal` on a value boxed in an interface only
finds value-receiver methods.

**Three `SECURITY:` doc-comment paragraphs**, one per site that carries claude's
raw text onto the wire — `BackgroundTaskStarted.Description`,
`BackgroundTask.Description` (the roster entry, repeated rather than delegated: a
roster is a *list* of command lines, a more tempting shape to feed somewhere
structured than a single one), and `BackgroundTaskUpdated.Patch`. `Description`
is claude's literal command line for its `local_bash` task type; `Patch` is an
unparsed blob **with no guarantee of being valid JSON** — the producer
(`streamsup`'s `maxTaskPatch` cap) truncates it, and a truncated JSON object no
longer parses, which is exactly why `Patch` is a plain `string` and never
`json.RawMessage` (that typing would make `json.Marshal` of the whole payload
*fail* on a truncated blob, turning a length-triggered truncation into total
loss of the update). All three: safe to render as inert text, never to execute,
re-shell, or feed to an HTML sink, an attribute, or a URL. `acpbridge` re-caps
nothing — every string is already bounded at construction by `streamsup`
(`maxTaskFieldID`, `maxTaskDescription`, `maxTaskPatch`, `maxTaskRosterEntries`,
`maxTaskRosterDescription`); a second cap here would be a second place the limit
is decided.

**`mapBackgroundTasks` — the roster-entry helper (#1402).** Mirrors `mapLocations`
exactly: `if len(tasks) == 0 { return nil }`, else `make([]BackgroundTask,
len(tasks))` and an index copy of the four fields. Returning nil for an empty
input is this helper's **contract**, not an implementation convenience — a
`[]BackgroundTask{}` built here would be **byte-indistinguishable** from the
correct mapper (the normalisation above happens in `MarshalJSON`, downstream of
either choice) and would pass any wire-shape golden while silently moving the
nil → `[]` guarantee out of the type that owns it. That is why the property is
asserted twice, on two different subjects: `TestMapUpdate_WireShape`'s empty-roster
row marshals `MapUpdate`'s actual return and checks the **bytes**
(`"tasks":[]`), and `TestMapUpdate_EmptyRosterForwardsNilTasks` checks the
**value** `MapUpdate` hands back has `Tasks == nil`. Neither rung alone covers the
other's failure mode.

`TestBackgroundTaskPayloadWireShape` (`outbound_test.go`) locks all four types by
marshalling payload values directly, with no mapper in the path. Since #1402 wired
the three arms, the three payload types are *also* driven through `MapUpdate` by
`TestMapUpdate_WireShape`, over different fixture values — but
`TestBackgroundTaskPayloadWireShape` keeps a job of its own: **`BackgroundTask`,
the roster's entry type, is never a `MapUpdate` return**, so this is its only
coverage, and pinning all four types independently of the mapper keeps a mapper
change from masking a renamed tag. Four rows, every field distinct and non-zero
except the deliberate all-zero empty roster row, which leaves `Tasks` nil (never a
hand-built `[]BackgroundTask{}`, which would marshal to `[]` without exercising
`MarshalJSON` at all).

See [codebase/1401.md](../codebase/1401.md) (types) and
[codebase/1402.md](../codebase/1402.md) (the arms + helper above).

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

**None.** `MapUpdate`, `mapToolContent`, `mapLocations`, and `mapBackgroundTasks`
(#1402) are pure functions — no goroutine, no channel, no mutex, no clock read, no
shared state. Reentrant and race-free by construction; `go test -race` has
nothing to catch. `context.Context` is correctly absent (no long-running
operation). Marshalling happens in the consumer, not here, so a marshal failure
is the consumer's concern; `RawInput` is opaque `json.RawMessage` pass-through,
never parsed, so a malformed blob cannot fault the mapper. The function is
total — no input is rejected, nothing panics. `mapBackgroundTasks` allocates a
fresh slice and copies fields; it never retains or mutates the caller's slice.

## Not `security-sensitive`

A pure function over daemon-internal neutral events, **outbound only** — no
untrusted-party input, no auth/crypto, no transport, no dispatch policy. The
streaming I/O and any dispatch decision live in the consumer (#750). Labelling
this package would track data lineage rather than a security-relevant design
decision — the same posture as [`turnbridge`](turnbridge-package.md#not-security-sensitive)'s
outbound half.

**Re-checked for #1402** (labelled `security-sensitive` at the ticket level,
because the new arms widen the *reach* of two hazardous strings — architect
security pass PASS, `docs/specs/architecture/1402-acp-background-task-arms.md`
§ Security review). The conclusion holds, and the reasoning is narrower than "no
untrusted-party input" alone: claude's output is untrusted-**content**, not
untrusted-**party** — it runs as the daemon's own child under this system's
threat model — and the trust boundary where that content is bounded is the
**producer**, `internal/streamsup/parser.go`, which caps every background-task
string at construction (`maxTaskFieldID`, `maxTaskDescription`, `maxTaskPatch`,
`maxTaskRosterDescription`, `maxTaskRosterEntries`). `BackgroundTaskStarted.Description`
(claude's literal command line) and `BackgroundTaskUpdated.Patch` (an unparsed,
not-necessarily-valid-JSON blob) become reachable by a third-party ACP host for
the first time through these arms, but the mapper re-caps neither — a second cap
here would be a second place the limit is decided, and the two could disagree
silently. Both are safe to render as inert text; the rendering obligation belongs
to the **host**, per the `SECURITY:` doc comments #1401 put on the declarations
(deliberately not restated at the arms, so there is only one copy to keep in
sync). The package still contains no `exec.Command`, no `sh -c`, and no call
reaching one — its import set stays `encoding/json` + `internal/turnevent` only.

## Consumer (built — #750)

The **streaming adapter [#750](https://github.com/pyrycode/pyrycode/issues/750)**
(now built — see
[acp-package.md § Outbound streaming adapter](acp-package.md#outbound-streaming-adapter-acpturnstream-750))
subscribes to a session's `turnevent` stream, calls `MapUpdate(ev)` per event,
and — on `ok` — wraps the payload in a `session/update` notification
(`params = {sessionId: <consumer-owned>, update}` via the new `Transport.Notify`).
It **discards `msgID`**: ACP has no per-message wire delimiter, so chunks sharing a
`MessageID` stream as separate `agent_message_chunk` notifications in arrival order
and the host concatenates them (no coalescing). On `!ok` the adapter type-switches
`TurnEnd` (→ signals the T7 owner #751 to resolve the held `session/prompt` with
`string(Reason)` as the stopReason) vs `Stall` (→ stderr) **before** `MapUpdate`,
since `MapUpdate` collapses both to `ok == false`. Producer wiring (the
`turnbridge` producer over the session's supervisor) is #751's, not built here.

## Related

- [ADR 027](../decisions/027-acp-mapping.md) — the internal turn-event ↔ ACP
  mapping contract: § Outbound table (the seven mapped variants + `TurnEnd`/`Stall`
  drops), § ACP taxonomy reference (`sessionUpdate` discriminants, tool
  kinds/statuses, content shapes), § Divergences 1, 5 & 7 (the background-task
  extension discriminants and the #1402 gating decision).
- [turnevent-package.md](turnevent-package.md) (#606) — the neutral pivot model
  `MapUpdate` maps OUT of; its `ToolKind`/`ToolStatus`/`TurnEndReason` values ARE
  the ACP strings, which is why kind/status is identity.
- [turnbridge-package.md](turnbridge-package.md) (#627) — the mobile mirror /
  template: same `turnevent` source, v2-envelope framing, `ThoughtChunk` dropped
  (the divergence this package reverses). Shares the exhaustive-sealed-switch +
  pure-value-to-value discipline.
- [acp-package.md](acp-package.md) (#755/#756/#757/#761/#762/#747/#750) — the
  sealed JSON-RPC transport floor this package deliberately does **not** import;
  the consumer #750 drives `session/update` frames through it via `Transport.Notify`.
- [modalbridge-package.md](modalbridge-package.md) — sibling `*bridge` adapter
  over `turnevent` (the outbound modal surface); same import-discipline framing.
- Consumer: [codebase/750.md](../codebase/750.md) (the streaming adapter, built);
  T7 held-`session/prompt` primitive
  [#751](https://github.com/pyrycode/pyrycode/issues/751).
- [codebase/1401.md](../codebase/1401.md) — background-task payload shapes
  (`BackgroundTaskStarted`/`Updated`/`Roster`, ADR 027 divergence 7).
- [codebase/1402.md](../codebase/1402.md) — the `MapUpdate` arms +
  `mapBackgroundTasks` + the six comment-site corrections above; still no
  ACP-lane producer ([#1400](https://github.com/pyrycode/pyrycode/issues/1400)).
