# #2652 — a pushed model_list reaches a capable client with Codex's models merged in

## Files read

- `internal/relay/v2session.go` → `forwardEnvelope` — the per-conn seal point every pushed frame (push drain, reconnect replay, resync) reaches; already calls `withheldFromConn` and the replay dedup. The merge hooks in here.
- `internal/relay/v2session_agentgate.go` → `withheldFromConn`, `pushedConversationID` — #2644's per-conn agent gate; the new rewrite mirrors its guard shape (not edited).
- `internal/relay/v2session_seams.go` → `V2SessionConfig.CodexConversation`, `ModelListFor`, `RetainedModelLists` — seam shape and nil ⇒ inert convention.
- `internal/relay/v2session_agentgate_test.go` → `openGateConn`, `gateFramesUntil`, `TestV2Session_CodexReplay_WithheldFromOldConn` — the harness the new tests reuse (capable + old conn on one manager; ring replay via `SetReplaySource`).
- `cmd/pyry/session_model_list.go` → `mergedModelOptions`, `mapModelList`, `modelListFor`, `savedModelVocabulary` — #2651's tagging, which this ticket factors so push and reply share it.
- `cmd/pyry/relay.go` → `relayV2Wiring` fields `modelListFor` / `retainedModelLists`, `codexConversation`, the `V2SessionConfig` literal in `startRelayV2` — where the new seam is carried and assigned.
- `cmd/pyry/main.go` → the wiring literal that builds `modelListFor(convReg, pool, modelVocabulary)` — the new builder is filled beside it.
- `cmd/pyry/model_vocabulary_store.go` → `CodexModels` — nil-receiver-safe, returns a deep copy.
- `internal/protocol/interactive.go` → `ModelListPayload`, `ModelOption` — the wire shape; `Agent`/`Family` are `omitempty`, so an untagged frame is byte-identical to pre-#2651.

In-flight overlap: `origin/feature/449` touches `internal/relay/v2session.go` (stale, unrelated block); built through, edits additive.

## Context

`model_list` is a snapshot the client replaces its menu with. #2651 merged Codex's entries into the two reply paths for a `multi_agent` conn; the pushed path (a Claude child's `initialize` reply → `turnbridge.MapEvent` → one envelope shared by every conn and the replay ring) is still Claude-only, so a capable client's grouped menu collapses at the next push. This closes that path at the same per-conn choke point #2644 gated.

## Design

### Daemon side (`cmd/pyry/session_model_list.go`)

Factor #2651's two tagging loops out of `mergedModelOptions` into:

- `claudeModelOptions(models []protocol.ModelOption) []protocol.ModelOption` — a fresh slice, each entry tagged `Agent = claude`, `Family = Value`.
- `codexModelOptions(saved savedModelVocabulary) []protocol.ModelOption` — the store's Codex families through `mapModelList`, tagged `Agent = codex`, `Family = DisplayName = Value`; nil for a nil store or none held.

`mergedModelOptions` becomes: Claude's held list through `mapModelList` → `claudeModelOptions`, then `codexModelOptions(saved)`. Behaviour unchanged (its existing tests pin it).

New builder `pushedModelOptions(saved savedModelVocabulary) func(claude []protocol.ModelOption) []protocol.ModelOption`: returns `claudeModelOptions(claude)` followed by `codexModelOptions(saved)`. Same two helpers as the reply, so push and reply cannot disagree. No logger (#833 rule inherited from `resolveBoundModelList`).

### Relay seam (`internal/relay/v2session_seams.go`)

`V2SessionConfig.MergedModelOptions func(claude []protocol.ModelOption) []protocol.ModelOption` — given a pushed frame's Claude entries, the list a `multi_agent` conn is offered. Called on the Run goroutine per pushed `model_list` to a capable conn. **Optional: nil ⇒ the pushed frame is delivered unchanged to every conn** (no Codex entries; the seam also owns tagging, so an unwired seam leaves the frame as today's). Must return a slice it owns (does not alias the argument).

### Per-conn rewrite (`internal/relay/v2session.go`, beside `forwardEnvelope`)

Placed in `v2session.go` rather than `v2session_agentgate.go` to hold the production-file count at five (the call site in `forwardEnvelope` must change either way).

`func (m *V2SessionManager) mergedForConn(s *V2Session, env protocol.Envelope) protocol.Envelope` — returns `env` unchanged unless all hold: `s.multiAgent`, seam non-nil, `env.Type == TypeModelList`, `env.InReplyTo == nil` (replies already merged by #2651). Then decodes `env.Payload` into a fresh `protocol.ModelListPayload`, replaces `Models` with `seam(p.Models)`, re-marshals, and returns a copy of `env` whose `Payload` is the new bytes. `conversation_id`, `dropped_models`, `ID`, `EventID`, `TS` carry through untouched. A decode or marshal failure returns `env` unchanged (defensive; no log — payload is claude-authored).

`forwardEnvelope` calls it after the withheld gate and the replay dedup, immediately before `json.Marshal(env)`. `env` is a by-value parameter and the rewrite assigns a new `Payload` slice, never writing through the old one, so the shared push/ring copy is not mutated.

### Wiring

`relayV2Wiring.pushedModelOptions` field (`cmd/pyry/relay.go`) assigned straight into `V2SessionConfig.MergedModelOptions` (no wrapper, preserving nil ⇒ inert); `main.go` fills it with `pushedModelOptions(modelVocabulary)` beside `modelListFor`.

## Concurrency model

No new goroutines. `mergedForConn` runs on the Run goroutine (reads `s.multiAgent`, Run-owned). The seam takes the vocabulary store's leaf mutex once per capable-conn push (`CodexModels`), never nested.

## Error handling

Undecodable payload or marshal failure → deliver unchanged. Seam returns whatever it builds; relay does not second-guess it.

## Testing strategy

Relay (`v2session_agentgate_test.go`, reusing the gate harness; seam stub tags Claude and appends one Codex row):
- One push of a `model_list` (plus a `model_list` reply) to a capable and an old conn: capable gets the pushed Claude entries tagged then the Codex entry, same `conversation_id`/`dropped_models`; the reply is unchanged; the old conn's payload bytes equal the pushed bytes exactly.
- Replay: a ring `model_list` replayed to a reconnecting capable conn arrives merged; to an old conn byte-identical (pins the stored copy is not mutated and dedup/replay still apply).
- Unwired seam: capable conn gets the pushed bytes unchanged.

cmd/pyry (`session_model_list_test.go` or a new `_test` beside it):
- `pushedModelOptions` with Codex entries held: Claude rows tagged as `mergedModelOptions` tags them, then Codex rows (`DisplayName = Family = Value`).
- No Codex entries held, and a nil store: only the tagged Claude entries.
- Result does not alias the input slice.

## Documentation handoff

Pending for the documentation stage: `docs/protocol-mobile.md` § model_list — a pushed `model_list` to a `multi_agent` client carries the merged, tagged list too.

## Open questions

- None blocking. Whether a Codex child can ever push `model_list` (it cannot today; only a Claude `initialize` reply maps to one) — if one did, its entries would be tagged Claude. Out of scope.
