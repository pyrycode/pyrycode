# Spec #606 — Neutral internal turn-event model (`internal/turnevent`)

Outbound turn-event core for Phase 2 structured streaming (EPIC #596). Pure
value types, no transport, no I/O, **standard library only**. This is the
daemon-owned neutral contract that the event-stream bridge (#608) maps tui-driver
`Events()` **into** and the v2 wire types (#607) map **out of**. No `Events()`
draining and no envelope mapping live here.

---

## Size check — kept at S (one red line tripped, documented false positive)

The design declares **~14 exported types** (5 event structs + 3 enums + 1 event
sum-type interface + 1 content sum-type interface + 3 content shapes + 1
location type), which trips the architect `>5 exported types or interfaces`
red line. This is kept at S deliberately, not by re-counting:

1. **It is the documented domain-variant false positive.** The `>5 types` line
   is a proxy for "large API surface that ripples through consumers." Here the
   count reflects how many variants the ACP domain defines — each type is a
   ~4-line declaration with no logic, no untrusted input, no concurrency, and
   **no consumer in scope**. The PO body names #606 as exactly this case, and it
   matches the recorded calibration `po-data-model-taxonomy-size-red-line`.
2. **The always-split type/consumer seam is already satisfied.** #608 is the
   consumer, #607 is the wire adapter. This ticket *is* "slice 1: introduce the
   types."
3. **Decisive test — would the pre-authorized split clear the line? No.** The
   PO's A/B cut puts `ToolStart`, `ToolUpdate`, `ToolKind`, `ToolStatus`,
   `ToolContent`, `TextContent`, `DiffContent`, `TerminalContent`, `Location`
   (= 9 exported types) in the B half — **still over 5**. A split that doesn't
   clear the tripped metric proves the metric is the wrong proxy for this work;
   splitting would only double the architect runs while B stays "oversized" by
   the same rule. That is the waste the size check exists to prevent.

Every other dimension is comfortably S: **~330 total LOC** (≈150 production +
≈180 tests), **3 production files**, **0 consumer call sites** (greenfield
package), **0 state-machine reject branches**, **5 ACs**. Developer max_turns
risk is near zero (pure declarations + table-driven tests). The PO's
pre-authorized cut is therefore **not exercised**.

Branch-overlap check: clean. `internal/turnevent` is a brand-new package; no
in-flight `feature/*` branch can touch its files. #607/#608 have no branches yet.

---

## Files to read first

This is a greenfield package, so the reading list is **conventions to mirror**
and the **source-of-truth taxonomy** (the design doc lives in the vault, *not*
the worktree — its authoritative shapes are inlined in § Design below, so you do
not need vault access):

- `internal/protocol/codes.go:1-62` — grouped string-constant taxonomy with
  doc-commented category blocks (`Code*`, `Type*`). **Mirror this exact shape**
  for the three enum const blocks (naming `ToolKind<Value>`, grouped, commented).
- `internal/protocol/compat_test.go:50-71` (`TestV1TypeSet_CoversAllExportedTypeConstants`)
  and `:121-158` (`TestErrorCode_Constants_MatchSpec`) — the **drift-detector /
  exactness** test pattern (hardcoded expected set + `len(...) == N` count guard +
  membership loop). This is the template for the AC#2 "exactly the ACP taxonomy"
  test.
- `internal/sessions/id.go:43-69` (`ValidID`) — a `bool` validity predicate at a
  type boundary. Mirror for each enum's `Valid()` method (AC#4).
- `CODING-STYLE.md` §Naming (acronyms: `ID` all-caps), §Testing (table-driven,
  same-package white-box tests, `t.Parallel()`), §Interface Design (small,
  consumer-defined — note the sealed-marker exception justified in § Design).
- `docs/knowledge/architecture/system-overview.md:88` — the `go list -deps`
  import-direction invariant; background for the AC#5 boundary test.
- `docs/knowledge/decisions/025-mobile-remote-head-interactive-session.md`
  §"The event model" + §"Wire-protocol extension" — how these neutral events
  feed the wire later (context only; do not implement any wire mapping here).

---

## Context

ADR 025 commits the daemon to drive an interactive claude session through
tui-driver and emit a structured turn-event stream. Per the design doc
(*Structured-Event Bridge — internal model and ACP mapping*, 2026-06-07), the
daemon owns a **neutral** event model; the mobile wire (now) and `pyry acp`
(#600, later) are thin adapters on top of it. The model is shaped ~90% like ACP
so the future ACP adapter is near pass-through, but it is **owned by us** — so
churn in the external ACP spec stays inside the ACP adapter and never reaches the
daemon core or the mobile wire. Same containment logic the tui-driver substrate
seal applies to claude's screen.

This ticket builds the **outbound turn-event core only**: the pure types. It is
the foundation #608 is blocked on.

---

## Design

### Package

New package `internal/turnevent` (module
`github.com/pyrycode/pyrycode/internal/turnevent`). Package doc comment states:
the neutral, daemon-owned outbound turn-event model; pure value types; the
contract #608 maps into and #607 maps out of; stdlib-only by invariant.

**Naming rationale:** "turn event" = an event that occurs during one claude
turn. `turnevent.TextChunk`, `turnevent.ToolKindRead` read cleanly and
distinguish this neutral model from tui-driver's own `Events()` (its upstream
source). Inbound commands (`Prompt`, `Cancel`, …) and internal-only events
(`BusyState`, `Stall`, …) are **out of scope** (Technical Notes) and get a home
in a later ticket; do not add them.

### Production file layout (3 files)

| File | Holds |
|---|---|
| `event.go` | `Event` sealed interface; the 5 event structs; `Location`; their `isTurnEvent()` markers; compile-time `var _ Event = …` assertions |
| `taxonomy.go` | `ToolKind`, `ToolStatus`, `TurnEndReason` enums + const blocks + canonical-value slices + `Valid()` methods |
| `content.go` | `ToolContent` sealed interface; `TextContent`, `DiffContent`, `TerminalContent`; their `isToolContent()` markers; compile-time `var _ ToolContent = …` assertions |

### The two sum-type seams (sealed marker interfaces)

A neutral model that "the bridge maps **into**" and "the wire maps **out of**"
is, by construction, one ordered heterogeneous stream plus one polymorphic
content field. Go's idiomatic closed sum type is a sealed interface (unexported
marker method) — this is **not** a preemptive interface (CODING-STYLE warns
against those): there are already five/three concrete implementations and a
known consumer (#608) that needs a single typed stream element. Sealing keeps
the variant set owned by this package, so external ACP-spec churn cannot inject
a variant.

```go
// Event is the sealed sum type of outbound turn events. The unexported marker
// keeps the variant set closed to this package; the bridge (#608) ranges a
// stream of Event and the wire adapter (#607) type-switches to map each kind.
type Event interface{ isTurnEvent() }

// ToolContent is the sealed sum type a ToolUpdate carries: text, diff, or
// terminal. A consumer recovers the concrete shape with a type switch.
type ToolContent interface{ isToolContent() }
```

Each concrete type implements its marker on a **value receiver** (AC#1: the
events are pure value types, so `TextChunk{}` — not only `&TextChunk{}` —
satisfies `Event`).

### The five event types (AC#1) — all implement `Event`

| Type | Fields | Notes |
|---|---|---|
| `TextChunk` | `MessageID string`, `Text string` | incremental assistant text, grouped by message |
| `ThoughtChunk` | `MessageID string`, `Text string` | streaming reasoning text |
| `ToolStart` | `ToolCallID string`, `Title string`, `Kind ToolKind`, `RawInput json.RawMessage`, `Locations []Location` | a new tool invocation |
| `ToolUpdate` | `ToolCallID string`, `Status ToolStatus`, `Content ToolContent` | changed fields of an existing tool call; `Content` may be `nil` (status-only update) |
| `TurnEnd` | `Reason TurnEndReason` | carries the reason only — see ACP divergence below |

- **`RawInput` is opaque** (Technical Notes). Type `json.RawMessage`
  (`encoding/json`, stdlib): undecoded pass-through bytes. This package **never
  inspects or parses it**. `json.RawMessage` is preferred over `map[string]any`
  precisely because it does *not* force a parse.
- **`TurnEnd` carries `reason` only.** ACP models end-of-turn as the `stopReason`
  *return value* of `session/prompt`, not an event; converting `TurnEnd` back
  into that RPC return is the ACP adapter's job (design-doc divergence 1), **not
  this ticket's**.

### `Location` (field of `ToolStart`)

```go
// Location is a file a tool call touches (ACP tool-call location). Line is
// 1-based; 0 means unspecified.
type Location struct {
    Path string
    Line int
}
```

`Line int` with `0 = unspecified` keeps `Location` a clean value type. See Open
Questions if absent-vs-line-0 must later be distinguished.

### The three content shapes (AC#3) — all implement `ToolContent`

| Type | Fields |
|---|---|
| `TextContent` | `Text string` |
| `DiffContent` | `Path string`, `OldText string`, `NewText string` |
| `TerminalContent` | `TerminalID string` |

A consumer recovers the shape via `switch c := upd.Content.(type)`. `ToolUpdate`'s
`Content` is typed `ToolContent`; a `nil` value is a valid "no content change".

### The three enums (AC#2, AC#4)

String-backed (`type ToolKind string`, …): the values **are** the ACP taxonomy
strings, which keeps the model faithful and lets adapters marshal them directly.
Mirror `internal/protocol/codes.go`'s grouped, doc-commented const layout.

```go
type ToolKind string

const (
    ToolKindRead    ToolKind = "read"
    ToolKindEdit    ToolKind = "edit"
    ToolKindDelete  ToolKind = "delete"
    ToolKindMove    ToolKind = "move"
    ToolKindSearch  ToolKind = "search"
    ToolKindExecute ToolKind = "execute"
    ToolKindThink   ToolKind = "think"
    ToolKindFetch   ToolKind = "fetch"
    ToolKindOther   ToolKind = "other"
)
```

| Enum | Type | Exact taxonomy values |
|---|---|---|
| `ToolKind` | `string` | `read, edit, delete, move, search, execute, think, fetch, other` |
| `ToolStatus` | `string` | `pending, in_progress, completed, failed` |
| `TurnEndReason` | `string` | `end_turn, max_tokens, max_turn_requests, refusal, cancelled` |

**Single source of truth → no drift.** For each enum, declare an **unexported
canonical slice** (e.g. `var toolKinds = []ToolKind{ToolKindRead, …}`) and
implement `Valid()` as a linear scan over it:

```go
// Valid reports whether k is one of the ACP tool kinds. The seam consumer
// (#608/#607) calls this to reject an out-of-taxonomy value.
func (k ToolKind) Valid() bool // scans the canonical toolKinds slice
```

`Valid()` deriving from the same slice the exactness test asserts against makes
slice/predicate drift structurally impossible (≤9 elements; perf is irrelevant —
this runs at seams, not hot loops). The canonical slices stay **unexported**
(YAGNI: no consumer needs to enumerate yet; the AC#2 test is white-box
same-package). See Open Questions for the exported-accessor option #607 may want.

### Why no errors, no constructors

This package returns **no errors** and exposes **no constructors/validation at
construction**. Validity is a `bool` predicate (`Valid()`); the *consumer* at the
seam rejects an out-of-taxonomy value and maps it to its own wire/sentinel error.
This is the established project convention — "refusal-to-wire-code mapping is the
consumer's job, NOT the primitive's" (PROJECT-MEMORY). The types are plain data;
constructing an invalid one is allowed and caught downstream via `Valid()`.

---

## Concurrency model

**None.** Pure value types; no goroutines, channels, mutexes, or I/O. Values are
passed by value and are immutable by convention. Thread-safety of any *stream* of
these events is the consumer's concern (#608 owns the stream); this package
imposes nothing.

---

## Error handling

No function in this package returns an `error`. Out-of-taxonomy enum values are
surfaced as `Valid() == false`, never as a returned error. `RawInput` is never
parsed, so it cannot fail here. `nil` `ToolContent` is a legal value (status-only
`ToolUpdate`), not an error.

---

## Import boundary (AC#5)

The deterministic safety net is a **white-box test** (`boundary_test.go`,
stdlib-only) that proves the package depends on nothing but the standard library:

- Walk the package directory's `.go` files, **excluding `_test.go`**.
- Parse each with `go/parser.ParseFile` in imports-only mode; collect import
  paths from `go/ast` (`go/parser`, `go/ast`, `go/token` are all stdlib).
- Assert every import path's **first segment contains no `.`** → it is stdlib.
  Any third-party path (`github.com/…`, `golang.org/x/…`) has a dotted first
  segment and fails. This deterministically forbids importing *any* transport,
  relay, wire-protocol, or external package — not just enumerated ones.
- Add a clearer-message second assertion: no import path has prefix
  `github.com/pyrycode/pyrycode/` (redundant with the rule above, but names the
  most likely violation directly).

The only stdlib import the production code needs is `encoding/json` (for
`json.RawMessage`) — explicitly allowed, since `encoding/json` is stdlib.

---

## Testing strategy

Table-driven, `stdlib testing` only, `t.Parallel()`, **same-package (white-box)**
so tests can read the unexported canonical slices. Scenarios (the developer
writes the Go in the project idiom — do not paste these as code):

**`taxonomy_test.go` (AC#2 + AC#4)** — model on `compat_test.go`:
- *Exactness, per enum:* assert the unexported canonical slice deep-equals the
  hardcoded expected ACP list (independent literal), plus a `len(...) == N` count
  guard (9 / 4 / 5). A new const without a slice entry — or vice versa — fails.
- *Consistency:* every canonical value reports `Valid() == true`.
- *Validity table, per enum:* one in-taxonomy value → `Valid() == true`; one
  fabricated out-of-taxonomy value (e.g. `ToolKind("nope")`, and the empty
  string) → `Valid() == false`.

**`content_test.go` (AC#3):**
- Construct one `TextContent`, one `DiffContent`, one `TerminalContent` as
  `ToolContent` values; type-switch each; assert the recovered concrete type and
  every field value. A wrong-branch match fails.
- Assert a `nil` `ToolContent` (status-only `ToolUpdate`) is distinguishable from
  a present shape.

**`event_test.go` (AC#1):**
- Construct each of the five events; assert field round-trip by value equality.
- Build a `[]Event{TextChunk{}, ThoughtChunk{}, ToolStart{}, ToolUpdate{},
  TurnEnd{}}` and type-switch to recover each kind — exercises the `Event` seam
  the bridge relies on. (Compile-time `var _ Event = …` assertions live in
  `event.go`.)
- Assert `ToolStart.RawInput` round-trips arbitrary opaque bytes unchanged (the
  package never mutates/parses it).

**`boundary_test.go` (AC#5):** as specified in § Import boundary.

Gate: `make check` (gofmt + `go vet` + `go test -race` + staticcheck +
`cmd/substrate-guard`). No claude screen literals appear in this package, so the
substrate guard stays trivially green.

---

## Open questions (resolve during implementation; none block)

1. **`Location.Line` representation.** Spec uses `int` with `0 = unspecified`.
   If a downstream consumer must distinguish "line absent" from "line 0" (no
   valid 1-based line is 0, so unlikely), switch to `*int`. Defaulting to `int`
   for value-type cleanliness.
2. **Exported enum enumeration.** Canonical slices are unexported. If #607's wire
   mapping wants to range all valid values, add a small exported accessor (e.g.
   `func ToolKinds() []ToolKind` returning a copy) *in that ticket*, not now
   (YAGNI).
3. **`Event` marker vs. distinct channels.** The design assumes #608 emits one
   ordered `[]Event` / `chan Event` stream, which the sealed `Event` interface
   serves. If #608 instead prefers per-kind typed channels, the marker becomes
   optional — but the neutral-model framing ("the contract the bridge maps into")
   makes one stream the expected shape, so `Event` is defined now.
4. **`messageId` / `toolCallId` identity types.** Kept as plain `string` for this
   slice (the model is neutral data). Promoting to newtypes (cf.
   `sessions.SessionID`) is deferred unless a consumer needs validation.
