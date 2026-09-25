# #2651 — capable clients' model_list carries Claude's and Codex's models, tagged

## Files read

- `internal/protocol/interactive.go` → `ModelListPayload`, `ModelOption` (+ its `MarshalJSON`) — the wire row that gains `agent` and `family`.
- `internal/protocol/conversations_read.go` → `AgentClaude`, `AgentCodex` — the wire spellings of the two agents (#2643), reused for the tag.
- `cmd/pyry/session_model_list.go` → `resolveBoundModelList`, `retainedModelVocabulary`, `agentModelVocabulary`, `savedModelVocabulary`, `modelListFor`, `retainedModelLists` — the resolver family; its no-logger rule and "never an empty Models" contract carry over.
- `cmd/pyry/model_vocabulary_store.go` → `modelVocabularyStore.CodexModels` — Codex's families, family as `Value`, no display name, deep copy.
- `internal/turnbridge/outbound.go` → `MapEvent`'s `turnevent.ModelList` arm — the one turnevent→protocol row translation; reused, not forked.
- `internal/relay/v2session_seams.go` → `V2SessionConfig.ModelListFor`, `V2SessionConfig.RetainedModelLists` — the two seams whose types change.
- `internal/relay/v2session_modelrequest.go` → `handleRequestModelList`; `internal/relay/v2session_modelreconcile.go` → `reconcileModelLists` — the two readers, both on Run.
- `internal/relay/v2session.go` → `V2Session.multiAgent`; `internal/relay/v2session_handshake.go` sets it before `reconcileModelLists` runs.
- `cmd/pyry/relay.go` → `relayV2Wiring.modelListFor`, `relayV2Wiring.retainedModelLists` — wiring field types.
- `cmd/pyry/session_model_list_test.go`, `internal/relay/v2session_modelreconcile_test.go`, `internal/relay/v2session_modelrequest_test.go`, `internal/relay/v2session_inlinereply_test.go` — existing seam call sites that take the new argument.

## Context

Slice S4a of Codex support. A `multi_agent` client (#2643) wants one model menu holding both agents' models, each tagged, to build a grouped menu. Old clients keep today's Claude-only list, byte for byte. The capability is per conn, while both seams today answer per conversation with no knowledge of the conn, so the seams gain the conn's decision as an argument. #2646 will read the same merged list for a session's `models` capability, so it is built in one function.

## Design

### Wire (`internal/protocol/interactive.go`)

`ModelOption` gains two fields, both `omitempty`:

- `Agent string \`json:"agent,omitempty"\`` — `protocol.AgentClaude` or `protocol.AgentCodex`.
- `Family string \`json:"family,omitempty"\`` — the Codex family name, or a Claude entry's own `Value`.

`omitempty` is what makes the old shape byte-identical: every producer that does not set them (every path an old client reaches) emits no key. `MarshalJSON` is unchanged.

### Merged list (`cmd/pyry/session_model_list.go`)

- `mergedModelOptions(pool, saved, boundSessionID string) (models []protocol.ModelOption, droppedModels int, ok bool)` — the one place the merged, tagged list is built (#2646's call site). Claude's entries from `retainedModelVocabulary`, in held order, tagged `agent=claude`, `family=Value`; then Codex's from `saved.CodexModels()` (nil `saved` → none), in store order, tagged `agent=codex`, `family=Value`, `display_name=Value`. `droppedModels` is Claude's `DroppedModels` (0 when Claude has none held). `ok` is false exactly when neither agent has an entry — never true with an empty slice.
- Both halves translate rows through `MapEvent` (via a small `mapModelList(turnevent.ModelList, convID) (protocol.ModelListPayload, bool)` helper that `resolveBoundModelList` also uses), so the row mapping is not forked.
- `resolveBoundMergedModelList(convReg, pool, saved, convID) (protocol.ModelListPayload, bool)` — `resolveBoundModelList`'s twin: same hard registry lookup, same reported id from the resolved record, answers from `mergedModelOptions`.
- `resolveBoundModelList` keeps its signature and its answer; only its MapEvent-and-assert tail moves into `mapModelList`.
- Adapters take the conn's decision:
  - `modelListFor(...) func(convID string, multiAgent bool) (protocol.ModelListPayload, bool)`
  - `retainedModelLists(...) func(multiAgent bool) []protocol.ModelListPayload`
  - Both select the resolver with one helper (`modelListResolver(multiAgent)`), so the choice is stated once.

### Seams (`internal/relay/v2session_seams.go`, `cmd/pyry/relay.go`)

- `ModelListFor func(conversationID string, multiAgent bool) (protocol.ModelListPayload, bool)`
- `RetainedModelLists func(multiAgent bool) []protocol.ModelListPayload`
- `relayV2Wiring.modelListFor` / `.retainedModelLists` change type to match; `main.go` is untouched because the constructors return the new types.

### Readers

- `handleRequestModelList` passes `s.multiAgent`; `reconcileModelLists` passes `s.multiAgent`. Both run on Run, where `multiAgent` is Run-owned and set in the handshake before the reconcile. No gate order changes; `withheldFromConn` (#2644) is untouched.

### Behaviour table

| Client | Claude held | Codex held | Answer |
|---|---|---|---|
| capable | yes | yes | Claude rows then Codex rows, tagged |
| capable | yes | no | Claude rows, tagged |
| capable | no | yes | Codex rows, tagged, `dropped_models` 0 |
| capable | no | no | refuse (unavailable / nothing reconciled) |
| old | any | any | today's answer, no tags |

## Concurrency model

Unchanged: synchronous reads on the Run goroutine. `CodexModels` takes the store's leaf mutex once, sequentially after `retainedModelVocabulary`'s locks, never nested. No goroutines.

## Error handling

No new failure modes. Refusal stays the comma-ok's false; nothing logs a conversation id or model value (no logger added). `mapModelList`'s type-system arms refuse as before.

## Testing strategy

- `internal/protocol`: a `ModelOption` with no tags marshals with no `agent`/`family` key; a tagged one carries both.
- `cmd/pyry` (`session_model_list_test.go`): table over the behaviour table on BOTH adapters (`modelListFor(...)(id, multiAgent)` and `retainedModelLists(...)(multiAgent)`): capable + both held (order, tags, Codex display name = family, dropped = Claude's), old client + both held (byte-identical to the Claude-only answer: equal to `resolveBoundModelList`, marshalled bytes carry no `agent`/`family`), capable + no Codex, capable + Codex-only, old + Codex-only (refuse / nothing), neither (refuse / nothing). A `savedVocabularyDouble` variant supplies Codex entries.
- `internal/relay`: the request and reconcile tests assert the seam receives `true` for a conn that negotiated `multi_agent` and `false` otherwise, and the returned payload reaches the wire unchanged.
- Existing call sites take `false` / a `multiAgent` parameter; their assertions are unchanged.

## Size

Six production files (one over the five-file line): `relay.go` and `v2session_seams.go` change only a seam's type. The seam type change also cascades into about 17 existing test call sites (over the 10-site line). The split that would stay under both — seam type change alone — has exactly one consumer, this ticket, so the floor rule merges it back; building as one ticket. Split depth is already capped (parent #2645, grandparent #2589).

## Open questions

- None blocking. Whether #2646 wants the payload or the row slice: `mergedModelOptions` returns rows so it can filter by `Agent`.

## Documentation handoff (pending, documentation stage)

`docs/protocol-mobile.md` § model_list: the merged list for `multi_agent` clients, the `agent` and `family` fields, entry order (Claude's held order then Codex's store order), a Codex entry's `display_name` equals its family, `dropped_models` counts only Claude's cut, and the unchanged shape for other clients.
