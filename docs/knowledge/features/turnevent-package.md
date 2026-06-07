# `internal/turnevent` — neutral outbound turn-event model

Pure-data leaf package. Declares the daemon-owned, neutral set of **outbound
turn-event** types for Phase 2 structured streaming (EPIC #596): five event
structs behind a sealed `Event` sum type, three string-backed ACP enums, and a
sealed `ToolContent` sum type. No transport, no I/O, no goroutines, no `context`,
no `slog` — **standard library only** (`encoding/json` for `json.RawMessage` is
the sole import).

The daemon owns this model; the mobile wire (now) and the future `pyry acp`
adapter (#600) are **thin adapters on top of it**. It is shaped ~90% like ACP so
the ACP adapter is near pass-through, but it is owned by us — so churn in the
external ACP spec stays inside the ACP adapter and never reaches the daemon core
or the mobile wire. Same containment logic the tui-driver substrate seal applies
to claude's screen.

This is the **outbound turn-event core only**: pure types. It is the stable
contract the event-stream bridge (#608) maps tui-driver `Events()` **into** and
that the v2 wire types (#607) map **out of**. No `Events()` draining and no
envelope mapping live here. Landed in #606.

- Decision anchor: [ADR 025](../decisions/025-mobile-remote-head-interactive-session.md)
  § "The event model".
- Design source-of-truth: *Structured-Event Bridge — internal model and ACP
  mapping* (Obsidian vault, `2026-04-10-pyrycode/structured-event-bridge-acp-mapping.md`).
- Spec: [`specs/architecture/606-neutral-turn-event-model.md`](../../specs/architecture/606-neutral-turn-event-model.md).
- Ticket record: [codebase/606.md](../codebase/606.md).

## Files

```
internal/turnevent/
├── event.go         Event sealed sum type; the 5 event structs; Location; value-receiver markers; var _ Event = … assertions
├── taxonomy.go      ToolKind / ToolStatus / TurnEndReason enums + const blocks + unexported canonical slices + Valid() methods
├── content.go       ToolContent sealed sum type; TextContent / DiffContent / TerminalContent; markers; var _ ToolContent = … assertions
├── event_test.go    field round-trip, the Event-stream type switch, RawInput opacity
├── taxonomy_test.go per-enum exactness (canonical slice == hardcoded ACP list + count guard), Valid() table
├── content_test.go  ToolContent shape recovery via type switch + nil-is-status-only
└── boundary_test.go AC#5 stdlib-only import boundary, enforced via go/parser
```

Three production files (~150 LOC), four test files (~180 LOC). Every type is a
~4-line declaration with no logic, no untrusted input, no concurrency.

## The two sum-type seams (sealed marker interfaces)

A neutral model that "the bridge maps **into**" and "the wire maps **out of**" is,
by construction, one ordered heterogeneous stream plus one polymorphic content
field. Both are modelled as Go's idiomatic **closed sum type**: a sealed
interface with an unexported marker method.

```go
type Event interface{ isTurnEvent() }       // event.go
type ToolContent interface{ isToolContent() } // content.go
```

- The unexported marker keeps the variant set **closed to this package**, so
  external ACP-spec churn cannot inject a variant. The bridge (#608) ranges a
  stream of `Event`; the wire adapter (#607) type-switches to map each kind.
- This is **not** a preemptive interface (which `CODING-STYLE.md` warns against):
  there are already five / three concrete implementations and a known consumer
  (#608) that needs a single typed stream element. The sealed-marker exception is
  justified by the closed-set, known-consumer shape.
- Each marker is implemented on a **value receiver** (`func (TextChunk)
  isTurnEvent() {}`), so `TextChunk{}` — not only `&TextChunk{}` — satisfies the
  interface. The events are pure value types. Compile-time `var _ Event =
  TextChunk{}` (and one per content shape) assertions live alongside the markers.

## The five event types (`event.go`)

| Type | Fields | Notes |
|---|---|---|
| `TextChunk` | `MessageID, Text string` | incremental assistant text, grouped by message |
| `ThoughtChunk` | `MessageID, Text string` | streaming reasoning ("thinking") text |
| `ToolStart` | `ToolCallID, Title string`, `Kind ToolKind`, `RawInput json.RawMessage`, `Locations []Location` | a new tool invocation |
| `ToolUpdate` | `ToolCallID string`, `Status ToolStatus`, `Content ToolContent` | changed fields of an existing tool call; `Content` may be `nil` (status-only update) |
| `TurnEnd` | `Reason TurnEndReason` | end of a claude turn; carries the reason only |

- **`RawInput` is opaque.** Typed `json.RawMessage` (undecoded pass-through
  bytes). The package **never inspects, parses, or mutates it** — consumers decode
  it on their own terms. `json.RawMessage` is preferred over `map[string]any`
  precisely because it does not force a parse. `TestToolStart_RawInputOpaque`
  round-trips structured JSON, invalid-JSON bytes, and `nil` unchanged.
- **`TurnEnd` carries `reason` only — an ACP divergence.** ACP models end-of-turn
  as the `stopReason` *return value* of `session/prompt`, not as an event.
  Converting `TurnEnd` back into that RPC return is the **ACP adapter's** job
  (design-doc divergence 1), not this model's. The internal model is the
  event-stream shape; here we just carry the reason.

### `Location` (field of `ToolStart`)

```go
type Location struct {
    Path string
    Line int   // 1-based; 0 means unspecified
}
```

A file a tool call touches (ACP tool-call location). `Line int` with
`0 = unspecified` keeps `Location` a clean value type. If a future consumer must
distinguish "line absent" from "line 0" (no valid 1-based line is 0, so unlikely),
switch to `*int` — deferred (YAGNI).

## The three content shapes (`content.go`) — all implement `ToolContent`

| Type | Fields |
|---|---|
| `TextContent` | `Text string` |
| `DiffContent` | `Path, OldText, NewText string` |
| `TerminalContent` | `TerminalID string` |

A consumer recovers the shape via `switch c := upd.Content.(type)`. `ToolUpdate`'s
`Content` is typed `ToolContent`; a **`nil` value is a legal "no content change"**
(status-only `ToolUpdate`), not an error — pinned by
`TestToolUpdate_NilContentIsStatusOnly`.

## The three enums (`taxonomy.go`)

String-backed (`type ToolKind string`, …): the values **are** the ACP taxonomy
strings, which keeps the model faithful and lets adapters marshal them directly.
Layout mirrors `internal/protocol/codes.go` (grouped, doc-commented const blocks,
named `<Type><Value>`).

| Enum | Exact ACP taxonomy values |
|---|---|
| `ToolKind` | `read, edit, delete, move, search, execute, think, fetch, other` (9) |
| `ToolStatus` | `pending, in_progress, completed, failed` (4) |
| `TurnEndReason` | `end_turn, max_tokens, max_turn_requests, refusal, cancelled` (5) |

**Single source of truth → no drift.** For each enum, an **unexported canonical
slice** (`toolKinds`, `toolStatuses`, `turnEndReasons`) is the one list that both
`Valid()` scans and the exactness test asserts against (deep-equal vs. an
independent hardcoded ACP literal + a `len(...) == N` count guard). A new const
without a slice entry — or vice versa — fails the test. Slice/predicate/const
drift is structurally impossible.

```go
func (k ToolKind) Valid() bool // linear scan of the canonical slice
```

`Valid()` is the **taxonomy-checkability** at the seam (AC#4): the consumer
(#608/#607) calls it to reject an out-of-taxonomy value and map it to its own
wire/sentinel error. Perf is irrelevant — ≤9 elements, runs at seams, not hot
loops. The canonical slices stay **unexported** (no consumer needs to enumerate
yet); #607's wire mapping may add an exported accessor (e.g. `ToolKinds()
[]ToolKind` returning a copy) when it does.

## Why no errors, no constructors

The package returns **no errors** and exposes **no constructors / construction-time
validation**. Validity is a `bool` predicate (`Valid()`); the *consumer* at the
seam rejects an out-of-taxonomy value and maps it to its own wire/sentinel error.
This is the established project convention — *"refusal-to-wire-code mapping is the
consumer's job, NOT the primitive's"* (PROJECT-MEMORY § Project-level
conventions; same idiom as `internal/protocol`). The types are plain data;
constructing an invalid one is allowed and caught downstream via `Valid()`.

## Import boundary (AC#5)

`boundary_test.go` is a deterministic, stdlib-only safety net proving the package
depends on nothing but the standard library:

- Reads the package directory, parses each non-`_test.go` `.go` file with
  `go/parser.ParseFile(..., parser.ImportsOnly)`.
- Rejects any import whose **first path segment is dotted** — the signature of a
  module path (`github.com/…`, `golang.org/x/…`). A stdlib path's first segment
  (`encoding`, `go`, `os`, …) never contains a `.`.
- A second, redundant-but-clearer assertion names the most likely violation
  directly: no import has prefix `github.com/pyrycode/pyrycode/`.
- A `checked == 0` guard fails loud so the test can't pass vacuously.

This forbids importing **any** transport, relay, wire-protocol, or external
package — not an enumerated denylist — without a third-party linter. It is the
deterministic enforcement of the daemon-core/ACP-churn containment the package
exists to provide.

## Concurrency

**None.** Pure value types; no goroutines, channels, mutexes, or I/O. Values are
passed by value and immutable by convention. Thread-safety of any *stream* of
these events is the consumer's concern (#608 owns the stream); this package
imposes nothing.

## What's deliberately NOT in the package

- **Inbound commands** — `Prompt`, `PermissionResponse`, `Cancel`, `DropQueued`.
  This is the *outbound* turn-event core only.
- **Non-turn / internal-only events** — `PermissionRequest`, `BusyState`,
  `QueueState`, `Stall`, `ScreenSnapshot`. Out of scope for #606; a later ticket
  gives them a home.
- **Any transport / wire / envelope mapping.** `Events()` draining is #608; v2
  wire types are #607; the ACP `stopReason` return conversion is the #600 adapter.
- **Constructors, `Validate()` returning `error`, exported enumeration accessors.**
  YAGNI until a consumer needs them.

## Consumers (deferred — none wired in #606)

- `internal/turnevent` bridge core (#608) — maps tui-driver `Events()` into this
  model; ranges a `[]Event` / `chan Event` stream; the package #608 is blocked on.
- v2 wire types (#607) — type-switch each `Event` to map it OUT to the mobile wire.
- `pyry acp` adapter (#600) — near pass-through; re-derives ACP's `stopReason`
  return from `TurnEnd.Reason`.

## Related

- [ADR 025](../decisions/025-mobile-remote-head-interactive-session.md) — mobile
  remote-head plan; § "The event model" + § "Wire-protocol extension".
- [codebase/606.md](../codebase/606.md) — ticket record (patterns + lessons).
- [protocol-package.md](protocol-package.md) — the sibling pure-data leaf package
  whose const-block layout, drift-detector test pattern, and consumer-owns-wire-
  codes convention this package mirrors.
- [codebase/595.md](../codebase/595.md) — Phase-1 sibling; the coarse `#589`
  fan-out this typed stream eventually replaces (§ Phase 2).
