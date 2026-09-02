# `internal/turnevent` — neutral turn-event model

Pure-data leaf package. Declares the daemon-owned, neutral set of event types
for Phase 2/3 structured streaming (EPIC #596 / #597). Predominantly **outbound**:
six ACP-shaped outbound event structs (`TextChunk`, `ThoughtChunk`, `ToolStart`,
`ToolUpdate`, `TurnEnd`, and `PermissionRequest`, #700) plus three internal-only,
PTY-derived status-peer events (`Stall` #638, `ApiRetry` / `Compacting` #1074)
behind a sealed `Event` sum type. It also seals the
**inbound** members behind a sealed `Inbound` sum type — `PermissionResponse`
(#700, first) and `Cancel` (#707, the neutral remote-Esc / ACP `session/cancel`
command). Four string-backed ACP enums and a sealed `ToolContent` sum type round
it out. No transport, no I/O, no goroutines, no `context`, no `slog` — **standard
library only** (`encoding/json` for `json.RawMessage` is the sole import;
`permission.go` needs none).

The daemon owns this model; the mobile wire (now) and the future `pyry acp`
adapter (#600) are **thin adapters on top of it**. It is shaped ~90% like ACP so
the ACP adapter is near pass-through, but it is owned by us — so churn in the
external ACP spec stays inside the ACP adapter and never reaches the daemon core
or the mobile wire. Same containment logic the tui-driver substrate seal applies
to claude's screen.

This is the **neutral model core**: pure types, no wiring. It is the stable
contract the event-stream bridge (#608) maps tui-driver `Events()` **into** and
that the v2 wire types (#607) map **out of**. No `Events()` draining and no
envelope mapping live here. The outbound `Event` family landed in #606; #700
added the permission **request/response** seam (`PermissionRequest` outbound +
the first inbound member `PermissionResponse`, see [codebase/700.md](../codebase/700.md)).

- Decision anchor: [ADR 025](../decisions/025-mobile-remote-head-interactive-session.md)
  § "The event model".
- Design source-of-truth: *Structured-Event Bridge — internal model and ACP
  mapping* (Obsidian vault, `2026-04-10-pyrycode/structured-event-bridge-acp-mapping.md`).
- Spec: [`specs/architecture/606-neutral-turn-event-model.md`](../../specs/architecture/606-neutral-turn-event-model.md).
- Ticket record: [codebase/606.md](../codebase/606.md).

## Files

```
internal/turnevent/
├── event.go         Event sealed sum type; the 5 ACP-shaped event structs + the internal-only status peers Stall (#638), ApiRetry / Compacting (#1074) + the internal-only diagnostic Unrecognized; Location; value-receiver markers; var _ Event = … assertions
├── permission.go    (#700) PermissionRequest (outbound Event variant) + PermissionOption + NewPermissionRequest; Inbound sealed sum type + PermissionResponse (first member) + Cancel (#707, fieldless inbound command); markers + assertions. Zero imports.
├── taxonomy.go      ToolKind / ToolStatus / TurnEndReason / PermissionOptionKind (#700) enums + const blocks + unexported canonical slices + Valid() methods
├── content.go       ToolContent sealed sum type; TextContent / DiffContent / TerminalContent; markers; var _ ToolContent = … assertions
├── event_test.go    field round-trip, the Event-stream type switch, RawInput opacity
├── permission_test.go (#700) constructor round-trip, Event/Inbound membership type switches, response selected/cancelled cases, option-kind validity
├── taxonomy_test.go per-enum exactness (canonical slice == hardcoded ACP list + count guard), Valid() table
├── content_test.go  ToolContent shape recovery via type switch + nil-is-status-only
└── boundary_test.go AC#5 stdlib-only import boundary, enforced via go/parser
```

Four production files (~220 LOC), five test files (~280 LOC). Every type is a
~4-line declaration with no logic, no untrusted input, no concurrency.

## The three sum-type seams (sealed marker interfaces)

A neutral model that "the bridge maps **into**" and "the wire maps **out of**" is,
by construction, one ordered heterogeneous outbound stream, one polymorphic
content field, and (since #700) one inbound command set. All three are modelled
as Go's idiomatic **closed sum type**: a sealed interface with an unexported
marker method.

```go
type Event interface{ isTurnEvent() }         // event.go — outbound turn events
type ToolContent interface{ isToolContent() } // content.go — a ToolUpdate's content
type Inbound interface{ isInbound() }          // permission.go (#700) — inbound commands
```

- The unexported marker keeps the variant set **closed to this package**, so
  external ACP-spec churn cannot inject a variant. The bridge (#608) ranges a
  stream of `Event`; the wire adapter (#607) type-switches to map each kind.
- This is **not** a preemptive interface (which `CODING-STYLE.md` warns against):
  there are already seven / three / one concrete implementations and a known
  consumer (#608) that needs a single typed stream element. The sealed-marker
  exception is justified by the closed-set, known-consumer shape. `Inbound` is
  defined ahead of its consumer for the same reason the whole package is — it is
  the neutral contract the downstream inbound parser maps onto (see § The
  permission seam).
- Each marker is implemented on a **value receiver** (`func (TextChunk)
  isTurnEvent() {}`), so `TextChunk{}` — not only `&TextChunk{}` — satisfies the
  interface. The events are pure value types. Compile-time `var _ Event =
  TextChunk{}` (and one per content shape) assertions live alongside the markers.
- **Adding an `Event` variant means checking every production type switch over
  it, not just the totality gate.** `TestTurnMarkFor_TotalOverEveryVariant` is
  AST-derived and proves only that `turnMarkFor` (`cmd/pyry/stream_turn_busy.go`)
  is total — it says nothing about `cmd/pyry`'s `eventKind`
  (`interactive_turn_v2.go`), `interactiveTurnEmitterV2.Handle`, or
  `turnbridge.MapEvent`, none of which are exhaustiveness-checked by the build.
  `SlashCommandList` (#1854) landed with all four walked by hand: `Handle`'s
  `default` arm is a live drop site for it (no `case` claims the variant),
  `eventKind` got a name-only arm, the totality row asserts the `turnMarkFor`
  whitelist's existing answer, and `MapEvent`'s `default` stays armless. At
  #1854 time all four were *reachable-but-unreached*, since nothing produced the
  variant; `internal/streamsup` became the producer in #1877, and from that
  ticket on `Handle`'s default and `MapEvent`'s default are both genuinely
  reached in production — armless is no longer the same claim as unreached, and
  the two doc paragraphs that conflated them (`eventKind`'s `ModelAnnounced` arm,
  in a category clause naming "anything else the production producer emits"
  rather than `SlashCommandList` by name) were the hardest of #1877's eight
  corrections to find by grep. Deriving which drop sites are reachable for a new
  variant from its **turn mark** plus whether a `Handle` case claims it is the
  reliable method — copying a neighbouring arm's site-list prose is not: the
  `ModelList`/`ModelAnnounced` arms' own lists still name `acp_turn_stream.go`,
  deleted with the terminal-driving path in #1348.

## The outbound `Event` variants (`event.go`, `permission.go`)

Six ACP-shaped outbound turn events plus three internal-only, PTY-derived status
peers (`Stall`, `ApiRetry`, `Compacting`):

| Type | Fields | Notes |
|---|---|---|
| `TextChunk` | `MessageID, Text string` | incremental assistant text, grouped by message |
| `ThoughtChunk` | `MessageID, Text string` | streaming reasoning ("thinking") text |
| `ToolStart` | `ToolCallID, Title string`, `Kind ToolKind`, `RawInput json.RawMessage`, `Locations []Location` | a new tool invocation |
| `ToolUpdate` | `ToolCallID string`, `Status ToolStatus`, `Content ToolContent` | changed fields of an existing tool call; `Content` may be `nil` (status-only update) |
| `TurnEnd` | `Reason TurnEndReason` | end of a claude turn; carries the reason only |
| `Stall` (#638) | *none* (`struct{}`) | **internal-only** onset marker; no ACP equivalent — mobile adapter sends it, the future ACP adapter (#600) drops it; see below |
| `ApiRetry` (#1074) | `Active bool`, `Current, Total int` | **internal-only** status peer of `Stall`: claude's live API-error retry state. `Active` is the rising/falling edge; `Current`/`Total` are the parsed `attempt N/M` counter (`{0,0}` when unparsed) |
| `Compacting` (#1074) | `Active bool` | **internal-only** status peer of `Stall`: claude's auto-compaction banner. Banner-only — tui-driver streams no progress payload, so `Active` is the only field |
| `Unrecognized` | `Site UnrecognizedSite`, `Kind string`, `Raw string`, `Truncated bool` | **internal-only** diagnostic, and the one variant that is not a claude sub-state: the stream parser met output it has no mapping for. `Site` is a closed enum (`line_type` / `assistant_block` / `user_block` / `undecodable`); `Kind` is the offending type, empty for `undecodable`; `Raw` is the offending JSON already truncated by the producer, a `string` and not `json.RawMessage` because a truncated blob is no longer valid JSON |
| `PermissionRequest` (#700, `permission.go`) | `RequestID, ToolCallID, Title string`, `Options []PermissionOption` | daemon asks the consumer to answer a permission modal; correlated to its `PermissionResponse` by `RequestID`; see § The permission seam |
| `SlashCommandList` (#1854, produced #1877) | `Commands []SlashCommand`, `DroppedCommands int` | claude's slash-command inventory for this session + working directory — the `commands` array of the same `initialize` reply `ModelList` carries `models` from. Constructed and emitted since #1877, entry-count bounded and its drop counted since #1826; not yet published — see below |
| `SlashCommand` (#1854, element type — not an `Event`, no marker) | `Name, ArgumentHint, Description string`, `Aliases, TruncatedFields []string` | one inventory entry, mirroring `protocol.SlashCommand`'s field order |

- **`SlashCommandList` / `SlashCommand` (#1854) were declared ahead of their
  producer; #1877 shipped the producer, and nothing publishes them yet.**
  `internal/streamsup`'s `emitModelList` decodes `commands`, caps each entry's
  `Name` at `maxSlashCommandName` (256 bytes) at construction, and emits one
  `SlashCommandList` beside `ModelList` on the model-list rung — an absent, null
  or empty `commands` array emits nothing (the decode collapses all three onto
  one nil slice, so the producer can't make the positive statement an empty
  emit would be making; see [streamsup-package.md](streamsup-package.md)).
  `turnbridge.MapEvent`'s `default` drops the variant and
  `interactiveTurnEmitterV2.Handle` has no case for it — both now genuinely
  reached rather than merely armless, since a production path emits the
  variant. The entry count is bounded and its drop counted since #1826
  (`maxSlashCommandListEntries`, truncation from the tail, reported on
  `DroppedCommands`) — one ticket after the first emit, exactly the gap
  `ModelList`'s own count bound (`maxModelListEntries`) took after its first
  emit (#1811 → #1812). `turnMarkFor` answers it correctly by construction
  (`turnMarkNone`, same as `ModelList`): the inventory is reported once per
  `initialize` exchange, which opens and closes no turn and is not even
  per-turn.
- **`SlashCommand` now carries all five of `protocol.SlashCommand`'s fields**,
  in that type's own declaration order — `ArgumentHint` (#1957), `Description`
  (#1904) and `Aliases` (#1825) arrived one at a time after `Name`, following
  `ModelOption`'s one-field-at-a-time growth across #1819/#1827/#1828. Fixing
  the order before the second field existed is what let each later field land
  in its mirrored position instead of being appended.
- **`DroppedCommands` arrived with its entry-count bound (#1826), one ticket
  after the wire type declared its own** — the same order `ModelList.DroppedModels`
  arrived in with `maxModelListEntries`. A daemon-internal struct isn't a
  compatibility surface, so this field could wait for the bound that produces
  it rather than being declared ahead of it the way the wire type was; see
  [Producing `turnevent.SlashCommandList`](streamsup-package-producing-turnevent-slashcommandlist.md)
  for the derivation.
- **SECURITY: every string on these two types is workspace-authored** — a
  command defined in a repository was written by whoever wrote that repository,
  a *lower*-trust origin than claude's own strings, which strengthens rather than
  restates `ModelOption`'s claude-authored warning. The daemon bounds these
  strings but does not sanitize them (no control-character or terminal-escape
  stripping); the render boundary owing the sanitization is the client's. A
  client sending a `Name` back as ordinary message text is the feature, but
  publishing a name never makes it trusted — nothing in the daemon may treat a
  value from this type as a command vocabulary or hand it to a child as an argv
  element. This is why `eventKind`'s arm for the variant returns the name alone
  (never an entry count, never a string from any entry): the workspace-authored
  provenance makes the #833 keep-values-out-of-logs posture apply *a fortiori*,
  not merely by analogy to `ModelOption`.

- **`Stall` is an internal-only, onset-only empty marker (#638).** It mirrors
  tui-driver's one-shot `stall_detected` signal (no payload, no clearing edge), so
  it carries no fields — no "cleared" state (the phone self-clears on the next
  turn activity) and, like every variant here, no `conversation_id` (the bridge
  injects identity when mapping to the wire). It is a first-class member of the
  same `Event` sum, but the **adapters** decide its fate per-variant: the mobile
  adapter sends it as the wire `stall` event ([protocol-package.md](protocol-package.md)
  § Stall), the future ACP adapter (#600) drops it. This is exactly the
  internal-only asymmetry the package was always designed to host (see *What's
  deliberately NOT in the package* — `Stall` graduated out of that list in #638).
- **`ApiRetry` / `Compacting` are internal-only status peers of `Stall`, but
  unlike `Stall` they are not onset-only (#1074).** Each carries an explicit
  `Active` bool: the rising edge (tui-driver's `*Shown` kind) is `true`, the
  falling edge (`*Hidden`) is `false` — the falling edge is the deliberate "clear
  the indicator" signal a remote head needs (`Stall` instead relies on the phone
  self-clearing on next turn activity, since tui-driver's stall marker has no
  clearing edge). Like `Stall`, neither carries `conversation_id` — the bridge
  injects it at wire-mapping time — and both are dropped by the ACP adapter
  (#600, `acpbridge.MapUpdate`) the same way `Stall` is: no ACP equivalent. The
  mapper copies `Current`/`Total` verbatim on both edges (tui-driver hands the
  last-known counter on `Hidden` "so the final render stays coherent"); a `{0,0}`
  value is a legitimate "retrying, count unknown" state, not an error.

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

## The four enums (`taxonomy.go`)

String-backed (`type ToolKind string`, …): the values **are** the ACP taxonomy
strings, which keeps the model faithful and lets adapters marshal them directly.
Layout mirrors `internal/protocol/codes.go` (grouped, doc-commented const blocks,
named `<Type><Value>`).

| Enum | Exact ACP taxonomy values |
|---|---|
| `ToolKind` | `read, edit, delete, move, search, execute, think, fetch, other` (9) |
| `ToolStatus` | `pending, in_progress, completed, failed` (4) |
| `TurnEndReason` | `end_turn, max_tokens, max_turn_requests, refusal, cancelled` (5) |
| `PermissionOptionKind` (#700) | `allow_once, allow_always, reject_once, reject_always` (4) — the ACP `session/request_permission` kinds |

**Single source of truth → no drift.** For each enum, an **unexported canonical
slice** (`toolKinds`, `toolStatuses`, `turnEndReasons`, `permissionOptionKinds`)
is the one list that both `Valid()` scans and the exactness test asserts against
(deep-equal vs. an independent hardcoded ACP literal + a `len(...) == N` count
guard). A new const without a slice entry — or vice versa — fails the test.
Slice/predicate/const drift is structurally impossible. `PermissionOptionKind`
lives in `taxonomy.go` (not `permission.go`) so all four ACP enums and their SSOT
slices stay visible together and its exactness test sits beside its siblings.

```go
func (k ToolKind) Valid() bool // linear scan of the canonical slice
```

`Valid()` is the **taxonomy-checkability** at the seam (AC#4): the consumer
(#608/#607) calls it to reject an out-of-taxonomy value and map it to its own
wire/sentinel error. Perf is irrelevant — ≤9 elements, runs at seams, not hot
loops. The canonical slices stay **unexported** (no consumer needs to enumerate
yet); #607's wire mapping may add an exported accessor (e.g. `ToolKinds()
[]ToolKind` returning a copy) when it does.

## The permission seam (`permission.go`, #700)

The permission **request/response** pair is the one Phase 3 (#597) internal type
that is genuinely cross-cutting: both the mobile wire adapter (#597) and the
future `pyry acp` adapter (#600) map onto the same daemon-owned shape — ACP's
`session/request_permission` lands on this exact `PermissionRequest` later — so
it is shaped **adapter-neutral**, not mobile-specific. (Reclassifies #606's
tentative grouping, which listed `PermissionRequest` under "internal-only
events"; it follows `Stall`'s #638 precedent and graduates into `Event`.)

**`PermissionRequest`** (outbound `Event`): `RequestID` correlates the request to
its response; `ToolCallID` references the gating tool call by id — the same by-id
reference style `ToolUpdate` uses, *not* re-embedding tool detail — and is empty
when a prompt has no backing tool call; `Title` is the always-present
human-readable context (spanning the tool-triggered and prompt-only cases);
`Options` is an **ordered** `[]PermissionOption` (the consumer renders in order —
a slice, not a map).

**`PermissionOption`**: one selectable answer — `ID` (referenced back by the
response), `Label` (human-readable), `Kind PermissionOptionKind` (the ACP
semantic kind; the field is the enum, so `opt.Kind.Valid()` rejects a fabricated
value at the call site).

**`NewPermissionRequest(requestID, toolCallID, title, options)`**: the
AC-mandated, **non-validating** positional assembler (the three leading strings
are order-sensitive). It is the package's *only* constructor — a 4-field value
assembler, not a long-lived component, so no `Config` struct. It does not
validate; an out-of-taxonomy `Kind` is constructible and caught downstream via
`Valid()`, matching the package convention (see § Why no errors).

**`Inbound` + `PermissionResponse`** (the inbound seam): `PermissionResponse` is
the **first inbound member**, answering a `PermissionRequest` matched by
`RequestID`. `OptionID` names the selected `PermissionOption.ID` — the **option
id, not the kind enum** (the kind is request-side metadata; an easy subtlety to
get wrong in the wire mapping) — and is empty when `Cancelled` is true (the
consumer dismissed the modal without selecting). Exactly one of {`OptionID` set,
`Cancelled`} is meaningful; the package does **not** enforce the exclusivity —
validation is the inbound parser's job, consistent with the package's
construct-then-validate-downstream stance. No `Valid()` on the response (YAGNI
until the inbound parser needs one).

**`Cancel`** (#707, second `Inbound` member): an inbound command to **stop the
current turn** — the neutral form of a remote Esc / ACP `session/cancel` (#600).
**Fieldless** (`type Cancel struct{}`): the daemon has one live turn context, so no
correlation id is needed yet; #600 adds a session/turn id additively if ACP needs
one. The slot was reserved since #700 (the `permission.go:43` comment), and the
marker **stays in `permission.go`** beside its sibling — the relocation-to-own-file
hinted there is optional churn #707 declined. Crucially, `Cancel` is **declared
vocabulary, not a live producer path**: the mobile `interrupt` frame routes to Esc
directly via the `internal/relay` seam (`handleInterrupt`, #707), it does **not**
construct a `Cancel` value — exactly as the mobile `modal_cancel` frame routes to
`ModalResolver.ResolveCancel` rather than building a `PermissionResponse`. The
mobile-wire → neutral-`Cancel` translator is the ACP adapter's job (#600); `Cancel`
exists now so #600 has its target. See [codebase/707.md](../codebase/707.md).

## Why no errors, and no construction-time validation

The package returns **no errors** and does **no construction-time validation**.
Validity is a `bool` predicate (`Valid()`); the *consumer* at the seam rejects an
out-of-taxonomy value and maps it to its own wire/sentinel error. This is the
established project convention — *"refusal-to-wire-code mapping is the consumer's
job, NOT the primitive's"* (PROJECT-MEMORY § Project-level conventions; same
idiom as `internal/protocol`). The types are plain data; constructing an invalid
one is allowed and caught downstream via `Valid()`.

The one constructor — `NewPermissionRequest` (#700, AC-mandated) — is the sole
exception to the package's "no constructors" habit, and it upholds the rule it
lives under: a plain positional value assembler that does **not** validate. Every
other type is still built as a struct literal.

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

- **The remaining inbound commands** — `Prompt`, `DropQueued`. #700 established
  the `Inbound` sealed sum type with `PermissionResponse` as its first member;
  **`Cancel` graduated off this list in #707** (see § The permission seam). `Prompt`
  and `DropQueued` join the set when their tickets land — a purely *additive*
  change. (The relocation of `Inbound` to its own `inbound.go` that #700 hinted is
  optional churn #707 declined; the marker stays in `permission.go`.)
- **Non-turn / internal-only events** — `BusyState`, `QueueState`,
  `ScreenSnapshot`. Out of scope for #606; a later ticket gives them a home.
  (Four types have already **graduated off** this list: `Stall` as an
  internal-only `Event` variant in #638, `PermissionRequest` as an outbound
  `Event` variant in #700, and `ApiRetry` / `Compacting` as `Stall`'s
  PTY-derived status-peer siblings in #1074 — see *The outbound `Event`
  variants* / *The permission seam* above.)
- **Any transport / wire / envelope mapping.** `Events()` draining is #608; v2
  wire types are #607; the ACP `stopReason` return conversion is the #600 adapter.
  Parsing the inbound `PermissionResponse` frame + the gate / nonce / deny-on-
  timeout modal loop are the security-sensitive downstream Phase 3 slices.
- **`Validate()` returning `error`, exported enumeration accessors.** YAGNI until
  a consumer needs them. (The package now has exactly one constructor,
  `NewPermissionRequest`, AC-mandated and non-validating — see § Why no errors.)

## Consumers (deferred — none wired in #606 or #700)

- `internal/turnevent` bridge core (#608) — maps tui-driver `Events()` into this
  model; ranges a `[]Event` / `chan Event` stream; the package #608 is blocked on.
- v2 wire types (#607) — type-switch each `Event` to map it OUT to the mobile wire
  (and map `PermissionRequest` onto the modal wire shape; may add an exported
  `PermissionOptionKind` accessor when it needs to enumerate).
- The inbound parser + gate / nonce / modal control loop (security-sensitive
  Phase 3 slices) — parse the inbound `PermissionResponse` frame and answer behind
  `--allow-remote-permissions` + deny-on-timeout + the one-time nonce.
- `pyry acp` adapter (#600) — near pass-through. Its **outbound `session/update`
  half is now built** ([acpbridge-package.md](acpbridge-package.md), #769): the
  pure `acpbridge.MapUpdate` maps `TextChunk`/`ThoughtChunk`/`ToolStart`/`ToolUpdate`
  out to ACP `session/update` payloads, drops `Stall` (and, since #1074, `ApiRetry`
  / `Compacting` the same way — no ACP equivalent for any of the three status
  peers), and reports "no notification" for `TurnEnd` (whose `stopReason` return
  the consumer re-derives from `TurnEnd.Reason`). Still deferred to later slices:
  the streaming consumer (#750), and mapping ACP's `session/request_permission`
  onto `PermissionRequest`.

## Related

- [ADR 025](../decisions/025-mobile-remote-head-interactive-session.md) — mobile
  remote-head plan; § "The event model" + § "Wire-protocol extension".
- [codebase/606.md](../codebase/606.md) — ticket record (patterns + lessons).
- [codebase/638.md](../codebase/638.md) — the `Stall` internal-only variant +
  its v2 `stall` wire peer (data vocabulary; bridge is #624-B / #608).
- [codebase/1074.md](../codebase/1074.md) — `ApiRetry` / `Compacting`, `Stall`'s
  PTY-derived status-peer siblings, threaded through all five bridge layers
  (mapper, outbound adapter, `cmd/pyry` fan-out) in one ticket; unlike `Stall`
  each carries an explicit `Active` falling edge.
- [codebase/700.md](../codebase/700.md) — the permission request/response seam:
  `PermissionRequest` outbound, the `Inbound` sum type + `PermissionResponse`, and
  the fourth `PermissionOptionKind` enum.
- [codebase/707.md](../codebase/707.md) — `Cancel`, the second `Inbound` member
  (the neutral remote-Esc / ACP `session/cancel` target); declared here, routed to
  Esc via the `internal/relay` `Interrupter` seam (the mobile `interrupt` frame
  does not construct a `Cancel` — the `modal_cancel` precedent).
- [protocol-package.md](protocol-package.md) — the sibling pure-data leaf package
  whose const-block layout, drift-detector test pattern, and consumer-owns-wire-
  codes convention this package mirrors.
- [acpbridge-package.md](acpbridge-package.md) (#769) — the outbound ACP adapter
  that maps this model to ACP `session/update` payloads; the ACP mirror of
  `turnbridge`'s outbound `MapEvent`, and the first concrete piece of the `pyry
  acp` adapter (#600). Its kind/status pass-through is identity precisely because
  these enum values ARE the ACP strings.
- [codebase/595.md](../codebase/595.md) — Phase-1 sibling; the coarse `#589`
  fan-out this typed stream eventually replaces (§ Phase 2).
