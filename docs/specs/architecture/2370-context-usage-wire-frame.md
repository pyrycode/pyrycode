# #2370 — declare the `context_usage` v2 frame contract

## Files read

- `internal/turnevent/event.go` → `ContextUsage`, `ContextUsageCategory`, `ContextUsageMCPTool`,
  `ContextUsageMemoryFile` — the daemon-internal contract this frame projects. Eleven fields,
  three independently-bounded lists, three independent dropped counts. Its doc block states the
  producer bounds every Claude-authored string and orders each list by descending token count,
  so a count cut keeps the heaviest entries.
- `internal/protocol/interactive.go` → `MCPStatusPayload`, `MCPServerStatus`, `MCPStatusRequestPayload`
  — the nearest shipped analogue (#2373). Settles both encoding questions: `MarshalJSON` with a
  value receiver normalising a nil slice to `[]`, and a doc comment forbidding this layer from
  inferring loss from a retained list's length.
- `internal/protocol/codes.go` → `TypeMCPStatus`, `TypeBanner` — the const-block shape for an
  outbound v2 report: grouped block, doc comment naming direction, the `inboundAppTypeSet`
  prohibition, and which guard surfaces classify it.
- `internal/protocol/envelope.go` → `inboundAppTypeSet`, `IsKnownAppType` — the v1 predicate the
  new type must be rejected by. Membership is the whole mechanism; absence is the requirement.
- `internal/protocol/compat_test.go` → `v2OnlyTypes`, `TestTypeConstants_V1V2Partition`,
  `TestIsKnownAppType` — the drift detector. Every `Type*` constant must appear in exactly one
  side of the partition, and the partition test's `all` list must name it.
- `cmd/pyry/relay_guard_test.go` → `excludedTypes`, `inboundTypes`,
  `TestEveryInboundV2TypeHasHandler` — Assertion #3 fails an unclassified constant. `TypeMCPStatus`
  is filed `"push+reply"` and landed that way in its own declaration commit, ahead of both producers.
- `internal/protocol/interactive_test.go` → `roundTripEnvelope`, `TestMCPStatusPayload_RoundTrip`,
  `TestMCPStatusPayload_ZeroValueEncoding` — the test shape to mirror.
- `internal/protocol/history_test.go` → `assertWireKeys` — asserts the key set exactly, rejecting
  both missing and unexpected keys. This is what pins AC #1 and AC #2.
- `docs/knowledge/features/protocol-package.md` § "Security posture", § "Consumers (deferred)" —
  the trust boundary is `json.Unmarshal` at the dispatcher; committed fixtures under `testdata/`
  double as the cross-language schema reference for clients with no Go binding. That is the reason
  the zero-value shape is committed as a fixture here rather than asserted only in Go.

## Context

The daemon-internal `turnevent.ContextUsage` reading is complete after #2357 and #2291, and nothing
carries it to a client. This slice declares the outbound wire vocabulary and the payload shape — one
type constant and four payload types — and nothing else. It maps no event and emits no frame.

One outbound shape serves every later consumer. #2371 maps the event and publishes the frame after a
turn; #2293 answers an on-demand request with this same frame, correlated by the envelope's reply id.
#2293 mints no second outbound shape, so no request verb and no correlation field belongs on this
payload — correlation rides `Envelope.InReplyTo`, exactly as it does for `MCPStatusRequestPayload`.
pyrycode-desktop#1254 and #2293 are the waiting consumers.

Declaring a contract ahead of its producers is this file's established sequencing (#2052→#2054,
#1983→#1984, and #2373 for the closest sibling). It lets client slices start against a published
shape.

No ADR is warranted: this adds a frame to an existing, already-decided family and changes no
boundary.

## Design

Two production files, both additive; no existing symbol changes, so there is no consumer cascade.

### `internal/protocol/codes.go`

One new grouped const block at the end of the file, following `TypeBanner`'s shape:

- `TypeContextUsage = "context_usage"` — binary → phone, outbound v2 context-window reading.

Its doc comment states the direction and conversation scope, that the single outbound shape serves
both #2371's post-turn publication and #2293's correlated reply (so no request verb is declared
here), that it MUST NOT be added to `inboundAppTypeSet`, and which guard surfaces classify it.

### `internal/protocol/interactive.go`

Four new exported types appended at the end of the file, projecting `turnevent.ContextUsage`:

- `ContextUsagePayload` — the body of an `Envelope` whose `Type == TypeContextUsage`. Exactly eleven
  keys, all unconditional (no `omitempty` anywhere): `conversation_id`, `model`, `total_tokens`,
  `max_tokens`, `percentage`, `categories`, `dropped_categories`, `mcp_tools`, `dropped_mcp_tools`,
  `memory_files`, `dropped_memory_files`. `ConversationID` has no counterpart on the internal event —
  it is the frame's conversation scope, supplied by the later mapper from the daemon's own record,
  not from Claude.
- `ContextUsageCategory` — `name`, `tokens`. Two keys, no more.
- `ContextUsageMCPTool` — `name`, `server_name`, `tokens`. Three keys.
- `ContextUsageMemoryFile` — `path`, `type`, `tokens`. Three keys.

`func (p ContextUsagePayload) MarshalJSON() ([]byte, error)` normalises all three nil slices to empty
slices and marshals through a local type alias. Value receiver, so the caller's value is never
mutated — `MCPStatusPayload.MarshalJSON`'s reason, restated by reference rather than re-argued.

Three lists share one normaliser rather than each row type carrying its own: the nil-to-`[]` question
belongs to the payload that owns the keys, and `MCPServerStatus` likewise has no `MarshalJSON` of its
own. The row types stay plain structs whose zero values encode as all-present keys without help.

The doc comments carry three load-bearing statements:

1. **Dropped counts are not inferable.** Each of `DroppedCategories`, `DroppedMCPTools` and
   `DroppedMemoryFiles` is copied verbatim from its `turnevent.ContextUsage` counterpart by a later
   mapper. This layer must not infer loss from a retained list's length; the original size stays
   recoverable as `len(list) + dropped`. Three independent pairs, never cross-read.
2. **Every string is untrusted descriptive text.** `Model`, category `Name`, MCP-tool `Name` and
   `ServerName`, and memory-file `Path` and `Type` are Claude- or workspace-authored. This layer
   neither validates nor sanitises them. A client MUST render each as inert text. `Path` in
   particular is descriptive: nothing on this path opens it, and a client must not treat it as a
   file handle or a link target. `ServerName` is not an authorization or actuation input — it names
   a contributor to the reading and nothing more.
3. **The reading is informational.** It mirrors `turnevent.ContextUsage`'s own standing constraint:
   consumers may display it, but it does not replace the daemon-owned `contextwindow.Read` value
   used for control decisions. The integers are Claude's own; the daemon neither recomputes nor
   normalises them, so a client must not assume `Percentage` is derivable from `TotalTokens` and
   `MaxTokens`, nor that the categories sum to the total.

Bounds are not re-decided here. Every string and list count is already capped by `internal/streamsup`
at construction; a second cap in this package would be a second place the limit is decided and the
two could disagree silently — `SessionFactsPayload`'s stated reason.

### Classification (AC #4)

Three surfaces, all test-side except the deliberate absence:

- `internal/protocol/envelope.go` — `inboundAppTypeSet` is **not** touched. Absence is the
  requirement: `IsKnownAppType` therefore returns `ErrUnknownType` for the type, which is the v1
  rejection AC #4 asks for.
- `internal/protocol/compat_test.go` — add `TypeContextUsage: true` to `v2OnlyTypes`, name it in
  `TestTypeConstants_V1V2Partition`'s `all` list, and add a rejection row to `TestIsKnownAppType`'s
  table mirroring the `mcp_status-rejected` row.
- `cmd/pyry/relay_guard_test.go` — add `"TypeContextUsage": "push+reply"` to `excludedTypes`, with a
  comment naming both later producers. Filed at declaration time and ahead of both, exactly as
  `TypeMCPStatus` was: Assertion #3 reports an *unclassified* constant, not an unemitted one.

## Concurrency model

None. `internal/protocol` is a pure-data leaf package — no goroutines, no locks, no shared mutable
state, no `context`. The new `MarshalJSON` takes a value receiver and mutates only its own copy, so
concurrent marshals of the same payload value do not race.

## Error handling

No new failure mode and no new error path. The only error this code can return is
`encoding/json`'s own from the aliased marshal, returned unwrapped because wrapping a marshal error
of a struct of strings and ints adds no context a caller could act on — the sibling normalisers do
the same. Decoding is the dispatcher's existing `json.Unmarshal` against the selected payload type;
this slice adds no validation, deliberately, since it has no authority to reject Claude's arithmetic.

## Testing strategy

Two committed fixtures under `internal/protocol/testdata/` — they are the cross-language schema
reference for clients with no Go binding, which is why the empty shape is a file and not only an
in-Go assertion:

- `context_usage.json` — populated: two categories, two MCP tools, two memory files, all three
  dropped counts non-zero and mutually distinct (so a cross-wired count fails), and hostile-looking
  descriptive text in one row of each list (markup metacharacters, an embedded newline, a traversal-
  shaped path) to pin that this layer passes it through verbatim.
- `context_usage_empty.json` — zero-value: every key present, all three lists `[]`, all scalars zero.

Test cases in `internal/protocol/interactive_test.go`:

- `TestContextUsagePayload_RoundTrip` — decodes the populated fixture; asserts the envelope type;
  asserts the exact eleven-key top-level set and the exact key set of every row in all three lists
  via `assertWireKeys`; asserts each decoded field value including all three dropped counts
  separately; re-marshals through `roundTripEnvelope` for byte equality.
- `TestContextUsagePayload_EmptyFixtureRoundTrip` — decodes the zero-value fixture, asserts the three
  lists decode non-nil and empty and the scalars are zero, and round-trips it byte-for-byte.
- `TestContextUsagePayload_ZeroValueEncoding` — marshals `ContextUsagePayload{}` through both a value
  and a pointer; asserts all eleven keys present, the three lists as `[]` rather than `null`, the
  scalars as `0` and `""`; asserts the receiver's slices are still nil afterwards (the normaliser did
  not mutate the caller); and marshals each of the three zero row types to assert their key sets and
  zero encodings.

Classification assertions land in the existing guards named under § Design rather than in new tests.

Gate: `go test -race ./internal/protocol/... ./cmd/pyry/...`, `go vet ./...`, `go build ./cmd/pyry`.

## Open questions

1. **Zero-value fixture, or in-test marshal only?** #2373 asserted its zero shape in Go with no
   fixture. Resolved before implementation in favour of a committed fixture: with three lists and
   three dropped counts the empty shape is not obvious to a client author, and the package overview
   records that `testdata/` fixtures are the schema reference for non-Go clients. The in-Go
   assertions are kept as well — they prove the pointer receiver and the non-mutation property,
   which a fixture cannot.
2. **`"push+reply"` or `"push"` in `excludedTypes`?** Resolved in favour of `"push+reply"`: both
   later producers are named in the ticket (#2371 publishes, #2293 replies), and `TypeMCPStatus` set
   the precedent of filing the eventual dual classification in the declaring commit.

Anything that moves during Phase B is recorded under `## Revisions`.

## Documentation handoff

**Pending — owned by the documentation stage. Not done in this ticket.**

In `docs/protocol-mobile.md`, under the application message types, give `context_usage` its own
heading among the interactive-event frames, at the same depth the sibling frame headings use there.
Add a dated entry to that document's `## Changelog`. The section must document:

- direction (binary → phone, outbound) and conversation scope;
- the complete field shapes — all eleven payload keys, and the exact keys of all three row types;
- empty-list semantics (`[]`, never `null`) and dropped-count semantics (explicit, never inferred
  from a retained list's length; original size is `len(list) + dropped`);
- the untrusted-text rendering requirement for memory-file paths, MCP names and every other
  Claude- or workspace-authored string.

It must state that publication is delivered by the dependent ticket (#2371, with #2293 answering
on demand) rather than claiming this declaration emits the frame.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] SHOULD FIX — **the payload is mixed-provenance and the first draft did not say
  so in the code.** `ConversationID` is daemon-authored, supplied by the later mapper from the
  daemon's own registry; every other string on the payload and on all three row types is Claude- or
  workspace-authored. A reader who assumes uniform provenance errs in a harmful direction half the
  time — treating `Model` or `Path` as daemon-authored is exactly the mistake that turns descriptive
  text into a trusted value. Phase B must state the split explicitly in `ContextUsagePayload`'s doc
  comment, not merely list which strings are untrusted.
- [Trust boundaries] No findings beyond the above — this layer promotes nothing to trusted. It
  neither validates nor sanitises, and `turnevent.ContextUsage`'s producer-side bounds are not
  re-decided here, so there is no second cap to disagree with `internal/streamsup`'s.
- [Tokens, secrets, credentials] OUT OF SCOPE — the payload holds no credential, but memory-file
  `Path` values disclose workspace layout (project names, home-directory shape). The standing
  obligation from `docs/knowledge/features/protocol-package.md` § "Security posture" — never log
  `Envelope.Payload` — is what bounds that disclosure, and it binds the publisher, not this
  declaration. #2371 and #2293 own it; neither may log the frame body.
- [File operations] SHOULD FIX — production code here performs no file operation, but `Path` is
  path-*shaped* untrusted text, which is the category's real hazard on this frame. Phase B's doc
  comment must state that nothing joins, cleans, resolves or opens it — including a later mapper:
  normalising the string would imply it names a real file this path uses, which it does not. The
  populated fixture commits a traversal-shaped path so pass-through is pinned by a test rather than
  by the comment alone. Test-side reads use `readFixture` against a constant name, so no
  user-controlled value reaches a filesystem call.
- [Subprocess execution] SHOULD FIX — no field is passed to any process, but `ServerName` collides
  by name with `MCPReconnectPayload.ServerName`, which crosses an actuation seam verbatim and is
  validated by nothing in this package or `internal/relay`. Two identically-named fields in one
  package, one inert and one an actuator input, is a live confusion hazard. Phase B must state in
  `ContextUsageMCPTool`'s doc comment that this one names a contributor to a reading and is never an
  actuation or authorization input.
- [Cryptographic primitives] No findings — the change creates, stores, derives and compares no
  secret or cryptographic material.
- [Network and I/O] No findings — the package adds no reader, writer or socket path. Three bounded
  lists make this frame larger than `MCPStatusPayload`'s one, but every list count and string length
  is already capped by `internal/streamsup` at construction and the relay's frame-size cap sits at
  the WS read boundary upstream; adding a cap here would be the silent-disagreement failure the
  design explicitly avoids.
- [Errors, logs, telemetry] No findings — no log, metric or telemetry is added, and no error message
  carries a field value. The only error returnable is `encoding/json`'s own.
- [Concurrency] No findings — pure-data leaf package. The normaliser's value receiver mutates only
  its copy, which the zero-value test asserts directly, and no goroutine, lock or shared mutable
  state is introduced.
- [Threat model alignment] OUT OF SCOPE for the parts this slice cannot decide — the frame is
  outbound-only, carries no request verb, and confers no authority, so it adds no inbound attack
  surface; `IsKnownAppType` rejects it on the v1 path by the type's deliberate absence from
  `inboundAppTypeSet`. Who may *ask* for this reading on demand is #2293's authorization decision,
  and when it is published is #2371's; neither is settled here.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-14

## Revisions

**2026-09-14 — fixture strengthened beyond the stated testing strategy.**
§ Testing strategy asked only that the three dropped counts be "mutually distinct" so a
cross-wired count fails. Implementation adds a second, stronger property: each count must also
differ from its own list's length, asserted in `TestContextUsagePayload_RoundTrip`. The first
draft of `context_usage.json` used `dropped_categories: 2` beside two category rows, and the new
assertion caught it — a fixture in that shape cannot distinguish an explicit dropped count from
one inferred via `len(list)`, which is the property AC #3 exists to pin. The fixture now carries
3 / 5 / 7 against two rows per list. No design or contract change; the payload shape is exactly
as committed in Phase A.
