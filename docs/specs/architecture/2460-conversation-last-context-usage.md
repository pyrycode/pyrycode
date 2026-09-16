# 2460 — remember each conversation's last context reading in the registry

## Files read

- `internal/conversations/conversation.go` → `Conversation`, its `SystemPrompt` field doc — the
  nil-pointer-plus-`omitempty` precedent AC-1 names, including the "a registry whose rows all hold
  nil is byte-identical to its pre-ticket form" wording this field has to reproduce.
- `internal/conversations/registry.go` → `Registry.Save`, `Registry.Update`, `SetArchived`,
  `SetSystemPrompt`, `SetWorkspaceLabel` — the setter shape (one field, hit/miss or sentinel,
  never persists), the copy-the-pointee-before-storing idiom, and the `saveMu → mu` lock order.
- `cmd/pyry/interactive_turn_v2.go` → `interactiveTurnEmitterV2`, its `hist` field doc,
  `interactiveTurnEmitterV2.Handle` and its `turnevent.ContextUsage` arm — the post-turn producer,
  and the "assigned after construction because the constructor has 86 test call sites" precedent
  the ticket points at.
- `cmd/pyry/conversation_history.go` → `appendConversationHistory` — the best-effort side-effect
  helper the emitter already calls: nil store ⇒ inert, failure ⇒ one `Warn` naming the conversation
  id and a reason, never a payload.
- `cmd/pyry/relay_context_usage.go` → `contextUsageResolver`, `contextUsageResolver.Get`,
  `contextUsageResolver.fly`, `contextUsageFlight` — the on-demand producer, the settle barrier
  (`done` is the happens-before edge), and `fly`'s "NOTHING HERE LOGS" block the ticket requires
  amending rather than silently breaking.
- `cmd/pyry/relay.go` → `startRelayV2` — the one scope holding `w.convReg`, `w.instanceName` and
  both producers' construction sites (`newContextUsageResolver` and the `emitter.hist` assignment).
- `cmd/pyry/main.go` → `resolveConversationsRegistryPath` — `~/.pyry/<name>/conversations.json`,
  the path every other registry-writing handler in this binary passes.
- `cmd/pyry/channel.go` → the `channel.new` persist — the best-effort `reg.Save(registryPath)`
  shape AC-2 names.
- `internal/streamsup/parser.go` → `decodeContextUsage` — bounds `Model` at
  `maxContextUsageStringBytes` (256) and `strings.Clone`s it; its own comment records that both
  lanes share this decoder. This is why no second bound lands here.
- `internal/turnevent/event.go` → `ContextUsage` — the ten-field event; five of them are stored.
- `cmd/pyry/interactive_turn_v2_test.go` → `emitterContextUsageFixture`, `contextUsageNeedles` —
  the distinctive-needle fixture #2371 built for its log-leak sweep. AC-4's disk-side proof reuses
  it against the saved registry bytes.
- `cmd/pyry/relay_context_usage_test.go` → `ctxUsageResolverFor`, `fakeContextUsageQuerier`,
  `fakeClock` — the resolver harness, which stays untouched because the new dependency is assigned
  after construction.
- `internal/conversations/registry_test.go` → `TestRegistry_SetSystemPrompt_RoundTrip`,
  `TestRegistry_SetSystemPrompt_DoesNotPersist`, `TestRegistry_Save_ActiveOmitsArchivedKey` — the
  three test shapes this field's tests mirror.
- `docs/knowledge/features/conversations-registry.md` § Schema — the documented key list the
  documentation stage extends; states the `omitempty`-placement contract this field joins.

## Context

The reading claude reports for itself arrives once per turn end (#2371) and on demand (#2431), and
nothing holds it. `contextUsageResolver`'s settled flight lives for `contextUsageCollapseWindow`
only — a collapse aid, not a memory. A client that reconnects, restarts, or opens the conversation
from a second device has only the transcript-derived pair on `session_settings`, which reads 0% in
a workspace and guesses the window until a turn ends.

The conversation registry is the daemon's per-conversation memory on disk. This ticket adds the
last reading's **summary** there — `Model`, `TotalTokens`, `MaxTokens`, `Percentage` and the time
it was taken — and deliberately not the three inventories (`Categories`, `MCPTools`,
`MemoryFiles`): they carry workspace memory-file paths and MCP server names the frame's no-log rule
keeps out of records, and a reconnecting client needs the headline numbers rather than a breakdown
taken some turns ago.

Both producers write it. One memory with two producers, not two deliverables: a value written from
only one arm goes stale the moment a client uses the other.

No ADR is warranted — this is a field on an existing schema following an existing precedent, not a
new boundary.

### Sizing

Over the 800-line line of the one-ticket table (~850 estimated, against the refiner's ~950), and
deliberately one ticket. The floor rule decides it: the storage half has exactly one consumer in
this family, so cutting it out would produce a child nothing outside the family calls, and the only
other available cut — post-turn write versus on-demand write — splits one memory across two
tickets rather than splitting two deliverables. Every other line of the table holds: 5 production
files, 1 new exported type, 0 consumer call sites needing simultaneous update, 4 acceptance
criteria, 2 reject branches. Overage stated, building.

## Design

### `internal/conversations` — the stored shape and the door

One new exported type beside `Conversation`:

```go
type ContextUsageReading struct {
    Model       string    `json:"model"`
    TotalTokens int       `json:"total_tokens"`
    MaxTokens   int       `json:"max_tokens"`
    Percentage  int       `json:"percentage"`
    AsOf        time.Time `json:"as_of"`
}
```

All five keys always emit. The reading is a complete record of one measurement; a zero
`total_tokens` is a fact about a fresh session, not an absent value, so `omitempty` on the inner
fields would destroy information the outer pointer already expresses.

`Conversation` gains the last field in declaration order:

```go
LastContextUsage *ContextUsageReading `json:"last_context_usage,omitempty"`
```

Pointer plus `omitempty` for `SystemPrompt`'s reason, with one difference worth stating in the
field doc: `SystemPrompt` needs a pointer to split "absent" from "explicitly empty", while this one
needs it because a zero-valued reading is a *false* reading (0 tokens, 0%, the zero time) that a
client would render as fact. nil means "claude has never reported for this conversation"; there is
no explicitly-empty state and nothing mints one. A registry whose rows all hold nil is byte-
identical to its pre-#2460 form.

The write door is a named setter beside `SetSystemPrompt`:

```go
func (r *Registry) SetLastContextUsage(id ConversationID, reading ContextUsageReading) bool
```

- Hit → stores the five values, returns true. Miss → returns false, no record modified.
- Takes a **value**, not a `*ContextUsageReading`: unlike `SetSystemPrompt`'s tri-state door there
  is no clear operation in this family, and a value argument makes "clear it back to nil"
  structurally unreachable rather than merely unused.
- Normalises `AsOf` to UTC inside the setter, so AC-1's RFC 3339 UTC contract is enforced by the
  package that owns the file rather than by each caller. `time.Time` marshals with whatever offset
  it carries, so a caller-side `.UTC()` would be one forgettable step per producer.
- Stores the address of a fresh local copy, so nothing aliases a caller-held value — the idiom
  `Promote` uses for `Name` and `SetSystemPrompt` for its prompt. `Get`/`List` keep sharing the
  stored pointer exactly as they already do for those two.
- No value validation and no second byte bound. `Model` is bounded upstream at
  `maxContextUsageStringBytes` by `decodeContextUsage`, which both lanes share; the three integers
  are JSON-decoded ints. No UTF-8 sentinel either, for `SetWorkspaceLabel`'s stated reason: the
  string arrives through `encoding/json`, which has already substituted U+FFFD, so the
  round-trip-fidelity hazard `SetSystemPrompt` refuses cannot reach this door.
- Does **not** call `Save` — the convention every other setter in the package keeps.

### `cmd/pyry` — one recorder, two producers

A small helper type in `relay_context_usage.go` (no sixth file), holding everything the write
needs:

```go
type contextUsageRecorder struct {
    reg    *conversations.Registry
    path   string
    logger *slog.Logger
    now    func() time.Time   // nil ⇒ time.Now
}

func (r *contextUsageRecorder) record(id conversations.ConversationID, u turnevent.ContextUsage)
```

`record` projects the five values out of the event, calls `SetLastContextUsage`, and on a hit saves
the registry best-effort. A nil receiver or nil registry is inert — the posture every optional seam
in this binary keeps, and the reason every existing emitter and resolver test stays green with no
edit. A miss writes nothing **and saves nothing**: the `Save` sits behind the setter's bool, which
is what makes AC-2's "a conversation id with no registry row writes nothing and saves nothing"
deterministic rather than incidental.

The projection is where the inventories are dropped, and it drops them by *never naming them*: the
composite literal lists exactly the five stored fields, so `Categories`, `MCPTools` and
`MemoryFiles` have no path to disk at all.

Reached from each producer by a field assigned after construction, both wired in `startRelayV2`
where one recorder value is built:

- `interactiveTurnEmitterV2` gains `usageRec *contextUsageRecorder`, assigned beside the existing
  `emitter.hist` assignment, for that field's stated reason: `newInteractiveTurnEmitterV2` has
  ~86 call sites across the test files and a positional parameter is not separable from them in Go.
- `contextUsageResolver` gains `rec *contextUsageRecorder`, assigned on the value
  `newContextUsageResolver` returns before its `Get` method value is taken. Assigned rather than
  passed for a narrower reason than the emitter's — six call sites, not eighty-six — but the same
  one applies to the test helper `ctxUsageResolverFor`, and a fifth positional parameter on a
  four-parameter constructor is worse than a named field either way.

### Data flow

```
post-turn (#2371)                         on-demand (#2431)
turnEndContextUsageRequester              relay request_context_usage
        │                                         │
  turnevent.ContextUsage                   contextUsageResolver.Get
        │                                         │
 emitter.Handle → ContextUsage arm          fly: QueryContextUsage → MapEvent
        │  flushDelta, emitMapped (wire)           │  settle f, close(f.done)
        └──────────────┬──────────────────────────┘
                 contextUsageRecorder.record
                       │
        Registry.SetLastContextUsage (hit?) → Registry.Save(path)
```

### Call-site placement

- Emitter arm: **after** `e.emitMapped`, so the wire frame is never delayed behind an fsync. The
  arm keeps its lifecycle neutrality untouched — no `startTurnIfNeeded`, no `transitionTo`, no
  `endTurn` — and the arm's comment gains a paragraph saying the reading now has one non-wire
  consumer and which five fields it takes.
- `fly`: inside the existing settle `defer`, **after** `close(f.done)` and gated on `ok`, so
  joiners are released before the registry write rather than behind it. The reading is hoisted into
  the `var` block beside `payload`/`ok` so the deferred closure can see it; nothing else in that
  function moves. `fly`'s "NOTHING HERE LOGS" block gains the boundary the ticket asks for: the
  prohibition is about the reading's own strings, which never reach a record; a save-failure record
  carries the conversation id and the registry error and nothing else.

## Concurrency model

No new goroutine. Both call sites run on goroutines that already exist: the emitter's single
`Handle` goroutine and the flight's own `fly` goroutine.

Locking is entirely inside `internal/conversations` and unchanged: `SetLastContextUsage` takes
`r.mu` and releases it before returning; `Save` takes `saveMu` then briefly `mu`. The recorder holds
no lock of its own and holds nothing across the two calls, so it cannot invert the documented
`saveMu → mu` order.

Two producers can settle seconds apart and both call `Save`. `saveMu` serializes the whole
snapshot→rename sequence, so the later acquirer renames later: **last write wins, and that is the
intended behaviour**. Both readings are valid "last reading" values. No `AsOf` comparison, no
ordering machinery, no background saver — one `Save` per settle, landing in the idle gap after the
turn it describes has closed.

`fly`'s termination argument is unchanged in kind: the added work is a local temp-file write and
fsync under the instance directory, bounded by the filesystem rather than by a context — the same
bound every other `Save` caller in this binary accepts.

## Error handling

| Failure | Behaviour |
|---|---|
| Nil recorder / nil registry (foreground, PTY, every unwired test) | Inert. No write, no log. |
| Unknown conversation id | `SetLastContextUsage` returns false. Nothing written, `Save` not called, nothing logged. |
| `Save` fails (disk full, unwritable dir, rename refused) | One `Warn` carrying the event name, the conversation id and the registry error. The in-memory row keeps the new reading; the next settle retries the write. |
| Mapping/query refusal on the on-demand path | `ok` stays false, so `record` is never reached — AC-3's "settles with `ok`". |

The save-failure record is the only new log line in the change, and its three fields are the whole
of it. The conversation id is a registry-canonical UUID; the registry error names the registry path
and the OS error. Neither can carry a category name, an MCP server name or a memory-file path,
because `record` never holds those in a variable that reaches the logger and the registry never
sees them at all.

## Testing strategy

RED first on the two producer arms — each new test fails against today's tree because the field,
the setter and the recorder do not exist.

`internal/conversations`:

- Round-trip: a `Conversation` carrying a reading survives marshal → unmarshal `DeepEqual`, and
  survives `Save` → `Load` through a real temp file unchanged (AC-1). `time.Date` fixtures, not
  `time.Now`, so no monotonic component survives to break `DeepEqual`.
- Omission: a row with nil emits no `last_context_usage` key, and a saved registry whose rows all
  hold nil contains the key nowhere in its bytes (AC-1's byte-identical claim, in the shape
  `TestRegistry_Save_ActiveOmitsArchivedKey` states its own).
- Setter, table-driven: hit stores exactly the five values and leaves every other field of the row
  and every other row untouched; miss returns false and modifies nothing; a non-UTC `AsOf` is
  stored as UTC and marshals with a `Z` offset; the setter does not persist (mirroring
  `TestRegistry_SetSystemPrompt_DoesNotPersist`).

`cmd/pyry`:

- Recorder over a real temp registry: after `record`, `Load` reports the five values with `as_of`
  from an injected clock — and the saved **file bytes** contain none of `contextUsageNeedles`
  (AC-4's disk side, reusing #2371's distinctive-needle fixture so a cross-wired field reddens).
- Recorder miss: an id with no row leaves no registry file on disk at all — the strongest available
  form of "writes nothing and saves nothing".
- Recorder save failure: a path whose parent is a regular file forces `Save` to fail; the captured
  record carries the conversation id and an error and no needle (AC-4's log side).
- Emitter arm (AC-2): an emitter with a recorder wired handles `emitterContextUsageFixture` and the
  row gains the reading; the wire types the arm emits are unchanged from the existing arm tests, so
  no lifecycle or ordering regression hides behind the new write.
- Resolver (AC-3): a settled `ok` flight records the reading; a refused flight records nothing and
  leaves no registry file.

Existing coverage that must stay green without edits, and is itself part of the proof that the new
dependency is optional: every `newInteractiveTurnEmitterV2` and `ctxUsageResolverFor` call site
constructs without a recorder.

Not attempted here: an end-to-end proof through `internal/e2e`. The two producer arms are pinned at
the daemon-composition layer where both wirings land, and the harness legs
(`relay_v2_stream_context_usage_test.go`, `relay_v2_request_context_usage_test.go`) prove the event
delivery this ticket does not change.

## Open questions

1. **Named setter or bare `Registry.Update`?** Resolved at plan time in favour of the named setter:
   it makes "sets exactly one field" structurally true rather than caller-enforced, and it gives
   the UTC normalisation one home instead of two. Recorded here because it is the only fork the
   ticket left open, and the documentation handoff branches on it.
2. **Does the recorder belong on the emitter at all, or should the post-turn write hang off
   `turnEndContextUsageRequester`?** Resolved in favour of the emitter arm: the ticket names that
   arm, and the requester does not see the parsed reading — only the emitter and `fly` do.

## Documentation handoff

Owned by the documentation stage, not this builder. Pending:

- `docs/knowledge/features/conversations-registry.md` § Schema gains the `last_context_usage` key:
  the five values it holds, the omitted-when-nil contract, and the fact that the three inventories
  (`Categories`, `MCPTools`, `MemoryFiles`) are deliberately absent.
- `docs/knowledge/features/conversations-registry-crud.md` gains a `SetLastContextUsage` entry
  beside `SetSystemPrompt`'s — the named setter did land, so this item is live.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries] No findings, and one verified assumption.** The reading crosses from
  child-authored JSON into daemon state at exactly one place, `decodeContextUsage` in
  `internal/streamsup`, which bounds `Model` at `maxContextUsageStringBytes` (256) and
  `strings.Clone`s it off the parse buffer. The ticket asserts both lanes share it; that was
  checked rather than trusted — the decoder has exactly two callers, `emitContextUsage` (the
  post-turn lane) and `claimContextUsageQuery`'s `pending.complete` (the on-demand lane), and its
  own doc block states that a second decoder for the second lane is the hazard it exists to
  prevent. Had the on-demand lane carried its own decode, this design would put an unbounded
  child-authored string on disk; it does not. `record` is the second, narrower boundary — the one
  place that decides which fields become durable — and it is a single composite literal in a single
  function, not a decision scattered across two producers.
- **[Trust boundaries] No finding — attribution is inherited, not re-decided.** A registry row
  written under the wrong conversation would be a cross-conversation disclosure (#678). This change
  introduces no second attribution decision: the emitter arm files the reading under the same
  follow-active cursor it already stamps the wire frame with, and `fly` files it under the
  `canonicalID` `resolveBoundRunner` returned, which is where the #678 `CurrentSessionID == ""`
  guard already lives. A mis-attributed row would require an already-mis-attributed frame.
- **[Tokens, secrets, credentials] Not applicable — no secret is handled, and the category's real
  content is what is *excluded*.** `Categories`, `MCPTools` and `MemoryFiles` are the sensitive
  half of a reading: memory-file paths disclose the operator's filesystem layout and MCP server
  names disclose their configuration. The design drops them by never naming them in the projection,
  so there is no field to forget to strip and no ordering in which they reach disk.
- **[File operations] No findings.** No path is built from client input: the target is
  `resolveConversationsRegistryPath(w.instanceName)`, whose only variable is a daemon flag passed
  through `sanitizeName`. No new path handling, no `Stat`-then-`Open`, no symlink decision — the
  write is `Registry.Save`'s existing temp-file → `chmod 0600` → fsync → rename recipe, whose
  rename commit point leaves the pre-existing file untouched on interruption.
- **[File operations] No finding — write amplification is bounded, and by a pre-existing bound.**
  The concrete scenario: a paired device tapping a refresh control drives one whole-file rewrite
  plus fsync per settle. The rate ceiling is `contextUsageCollapseWindow` (2s per conversation),
  because a collapsed ask is answered from the settled flight and never reaches `fly`. That same
  window already bounds a strictly more expensive consequence — each fresh flight asks claude at
  `full` detail, which costs real tokens — so an actor who can drive registry fsyncs can already
  drive spend, and the existing bound is the one that matters. The post-turn lane is bounded by
  claude's own turn cadence, which is not network-reachable.
- **[Subprocess / external command execution] Not applicable.** Nothing in this change execs,
  builds argv, or touches child stdin; `record`'s entire reach is one in-memory mutation and one
  local file write.
- **[Cryptographic primitives] Not applicable.** No randomness is consumed — unlike the emitter's
  neighbouring arms, this path mints no id and calls no `conversations.NewID`.
- **[Network & I/O] No findings — per-row growth is bounded.** The only variable-length value that
  becomes durable is `Model`, capped at 256 bytes upstream; the other four are a fixed-width
  timestamp and three JSON-decoded ints. Row count is bounded by hosted conversations, so the file
  cannot be grown without bound by request volume. Nothing new is read from a socket and nothing
  new is sent on one — answering `request_context_usage` from this field is explicitly the
  follow-up ticket, not this one.
- **[Error messages, logs, telemetry] No findings, and the one new log line was audited field by
  field.** `record` adds exactly one record, on `Save` failure, carrying an event name, the
  conversation id, and the registry error. Each is checked rather than assumed: the **conversation
  id** is structurally registry-canonical, because the log sits inside the branch the setter's
  `true` opened — an id with no row never reaches a `Save` and therefore never reaches a log, which
  makes log injection through a client-chosen conversation string unreachable rather than merely
  unlikely. The **registry error** can name the registry path and an OS error and nothing else;
  in particular its `encode` arm cannot carry the reading, because this struct holds no floats, no
  channels and no custom marshaller, and invalid UTF-8 is substituted by `encoding/json` rather
  than reported — so `json.UnsupportedValueError` has no reachable producer here. The **reading's
  own strings** are never held in a variable the logger can see. No metric or telemetry is added.
- **[Concurrency] No findings.** No goroutine is created; both call sites run on goroutines that
  already exist. The recorder holds no lock and holds nothing across the setter and the `Save`, so
  it cannot invert the package's documented `saveMu → mu` order. The setter's scan-and-mutate is one
  `mu` acquisition, so there is no find-then-mutate window — `SetArchived`'s property, inherited by
  construction. Two producers racing is the ticket's intended last-write-wins, and it converges
  correctly rather than merely safely: `Save` snapshots in-memory state at save time, so whichever
  `Save` renames last writes the newest in-memory row, not the row its own caller set. No
  interleaving leaves the file holding an older reading than memory.
- **[Threat model alignment] No findings.** The relevant `docs/protocol-mobile.md` § Security model
  threats are the no-log rule for reading strings (AC-4, discharged above), resource use driven by
  a paired device (bounded by the collapse window, above), and disclosure to a client (no wire
  change in this ticket). Out of scope and named: serving `request_context_usage` from this stored
  field when no fresh reading can be taken — the ticket's own stated follow-up.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-16
