# #2665 — create_conversation accepts a model and effort

## Files read

- `internal/relay/handlers/create_conversation.go` → `CreateConversation`, `SessionCreator`, `resolveCreateAgent`, `msgCreateConversationMalformed` — the handler this extends; refusal-before-mint precedent from #2647.
- `internal/protocol/conversations_write.go` → `CreateConversationPayload` — the `Agent *string ,omitempty` pattern the two new fields copy.
- `internal/relay/v2session_settings.go` → `handleSetSessionSettings`, `validModel`, `validEffort`, `msgSettingsModelNotOffered` — the checks and reply table create_conversation must match.
- `internal/relay/v2session_modelrequest.go` → `msgModelListUnavailable` — the vocabulary-unavailable reply message.
- `internal/relay/v2session_seams.go` → `ErrModelNotOffered`, `ErrEffortNotOffered`, `ErrModelVocabularyUnavailable` — the sentinels the membership checks return.
- `cmd/pyry/main.go` → `sessionMinter.Create`, `settingsUpdaterAdapter.UpdateSettings`, `validateModelVocabulary`, `validateEffortVocabulary`, `effortLevelsFor` — the membership checks to reuse and the minter to extend.
- `cmd/pyry/session_model_list.go` → `agentModelVocabulary`, `retainedModelVocabulary` — agent-keyed vocabulary; `boundSessionID == ""` skips the bound hold, which is exactly "no session yet".
- `internal/sessions/pool.go` → `Pool.MintAs`, `Pool.mintSettings`, `Pool.DefaultSettings` — where the mint's settings come from.
- `cmd/pyry/codex_create_conversation_test.go`, `cmd/pyry/settings_agent_vocabulary_test.go` → `startCodexPool`, `newDormantWritePool`, `agentVocabularyDouble`, `claudeEntries`, `codexEntries` — fixtures the new cmd/pyry tests reuse.

Overlapping in-flight branches: none at plan time.

## Context

A Codex conversation's first turn runs on the account default because `create_conversation` carries no model/effort and `Pool.MintAs` gives non-Claude sessions zero settings. A follow-up `set_session_settings` is a second round trip with a race against the first message. This ticket lets the create carry both.

**Deliberate departure from the ticket's technical note.** The note suggests a separate validation seam called in the handler before `conversations.NewID`, then a mint. This plan instead validates *inside* the one `SessionCreator.Create` call, before `resolveSpawnDir` and before the mint, and the handler only maps the returned sentinel. Reasons:

1. The effort check needs "the model the session will start on", which for Claude is the operator default. A separate seam reads that default once to validate and the mint reads it again; an operator change in between validates effort against one model and mints on another. One call composes the settings once and both validates and mints that same value.
2. `SessionCreator` has three implementations and one production call; `CreateConversation` has 15 call sites. Growing `Create`'s signature is the smallest fan-out; a new handler parameter would exceed the ten-site boundary.
3. The refusal still precedes every side effect: the shape checks run in the handler before `NewID`; membership runs in `Create` before `resolveSpawnDir` (which trust-marks a directory) and before `MintWith`. `NewID` itself is a pure rng draw with no side effect, so running it before the membership check creates nothing.

No ADR needed; ADR 039 (resolve the agent, then check within its entries) is followed as is.

## Design

### Wire — `protocol.CreateConversationPayload`

Two new fields after `Agent`: `Model *string \`json:"model,omitempty"\``, `Effort *string \`json:"effort,omitempty"\``. Nil = absent = today's behaviour; omitempty keeps a request without them byte-identical (the #2647 reason). Reply shape unchanged.

### relay exports (one consumer: the handler)

- `relay.ValidModel(m string) bool`, `relay.ValidEffort(e string) bool` — exported wrappers over `validModel` / `validEffort`, so the 16 existing test call sites do not move.
- `relay.MsgSettingsModelNotOffered`, `relay.MsgModelListUnavailable` — the two unexported constants renamed to exported (few sites), so both verbs reply with the same text by construction.

### Handler seam — `handlers.SessionCreator`

```go
// CreateSettings is a create_conversation's requested model and effort; nil = absent.
type CreateSettings struct{ Model, Effort *string }

type SessionCreator interface {
    Create(ctx context.Context, label, spawnDir, agent string, settings CreateSettings) (sessionID, dir string, err error)
}
```

Contract addition: `Create` checks a present non-empty model/effort against `agent`'s vocabulary before any side effect and returns `relay.ErrModelNotOffered`, `relay.ErrEffortNotOffered` or `relay.ErrModelVocabularyUnavailable` (wrapped or bare) with nothing created.

### Handler flow

decode → `resolveCreateAgent` → **shape**: `p.Model != nil && !relay.ValidModel(*p.Model)` or the same for effort → `protocol.malformed`, `msgCreateConversationMalformed`, not retryable → `NewID` → `creator.Create(..., CreateSettings{p.Model, p.Effort})` → error mapping, first match wins:

| error | code | message | retryable |
|---|---|---|---|
| `ErrSpawnDirRejected` | malformed | cwd rejected (unchanged) | no |
| `relay.ErrModelNotOffered` | malformed | `relay.MsgSettingsModelNotOffered` | no |
| `relay.ErrEffortNotOffered` | malformed | `msgCreateConversationMalformed` | no |
| `relay.ErrModelVocabularyUnavailable` | `model_list.unavailable` | `relay.MsgModelListUnavailable` | yes |
| other | binary_offline (unchanged) | | yes |

Each refusal logs one event (`create_conversation.settings_refused` with a static `reason` field) carrying conn id only — never the model or effort.

### Pool — `Pool.MintWith`

`MintWith(label, spawnDir, harness string, settings SessionSettings) (SessionID, error)` is today's `MintAs` body with `settings` used verbatim in place of the harness-dependent `mintSettings()` read. `MintAs` becomes `MintWith(label, spawnDir, harness, p.MintDefaults(harness))`.

`MintDefaults(harness string) SessionSettings` (exported) answers what a mint of `harness` starts on: `mintSettings()` under RLock for Claude, zero otherwise. It is the one definition of "what the mint would give", read by both `MintAs` and the cmd-side composer, so the two cannot drift.

### cmd/pyry — `sessionMinter`

Gains `saved savedModelVocabulary` (wired with `modelVocabulary` in `main`; nil in tests is two sources, as for the adapter). `Create`:

1. `base := p.MintDefaults(agent)`; `start := sessions.SessionSettings{Model: base.Model, Effort: base.Effort}`, then each present field replaces its counterpart (an explicit `""` replaces too — it means "agent default", as on `set_session_settings`). Posture is structurally absent: only Model and Effort are ever copied.
2. If a present value is non-empty: `list, have := agentModelVocabulary(p, saved, agent, "")`; `validateModelVocabulary` for a model; `validateEffortVocabulary(agent, list, have, start.Model, effort)` for an effort — `start.Model` is the requested model, else the mint default. Same helpers, same order, same sentinels as `UpdateSettings`.
3. `resolveSpawnDir`, then `MintWith(label, resolved, agent, start)`.

A request with neither field reads `MintDefaults` and mints exactly what `MintAs` does today.

## Concurrency model

No new goroutines or locks. `MintDefaults` takes `p.mu.RLock` and releases it before `MintWith` builds (which must stay off `p.mu`), the same benign window `MintAs` documents today: a concurrent bootstrap settings change alters what a new session inherits, never an existing one. The composed `start` is a single snapshot used for both the effort check and the mint.

## Error handling

All refusals return before `resolveSpawnDir` and `MintWith`: no trust mark, no pool entry, no registry save, no conversation row. Error mapping is by `errors.Is` at the handler (consumer-side mapping convention). Messages are static constants; no requested value is echoed or logged.

## Testing strategy

- **protocol:** a request with neither field marshals byte-identically to today's fixture; both fields round-trip when set.
- **handlers (stub creator):** table — shape-invalid model; shape-invalid effort → malformed, create message, not retryable, creator not called, registry empty; creator returns each of the three sentinels → the code/message/retryable above, registry empty; accepted → creator received the exact pointers; neither field → creator received nil/nil and reply bytes carry no new keys.
- **sessions:** `MintWith` stores the given settings (`SettingsFor`) for a Codex harness; `MintDefaults` is the bootstrap's model/effort for Claude and zero for Codex.
- **cmd/pyry (real pool):** Codex create with a held family model + advertised effort → runner config carries both; Codex model with no families → `ErrModelVocabularyUnavailable` and the pool gained no session; Claude effort only → checked against the operator default model (refused when that model advertises none) and, when accepted, the default model is kept; Claude model only → default effort kept.

## Documentation handoff (pending — documentation stage)

- `docs/protocol-mobile.md` § create_conversation (message-type table row): document `model` and `effort` — optional, `omitempty`, checked against the chosen agent's vocabulary, the refusal codes above, nothing created on refusal — and replace the sentence saying a new Codex conversation always starts with no model or effort set.
- Changelog entry requested; no changelog file exists — record in the project's release notes, do not create a new file.

## Size

6 production files (`conversations_write.go`, `create_conversation.go`, `v2session_settings.go`, `v2session_modelrequest.go`, `pool.go`, `main.go`) — one over the five-file line, as the refiner's estimate already recorded; the relay exports have exactly one consumer (this handler), so by the floor rule they stay in this ticket. ~130 production lines, ~350 test lines. Exported types: `CreateSettings` (1). Consumer sites: 3 `SessionCreator` implementations, 1 production `Create` call. Reject branches: 5.

## Open questions

- None blocking. If `MintDefaults` duplicating `DefaultSettings` reads awkwardly in review, it stays: `DefaultSettings` carries posture, and the mint must never.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] No findings — `model` and `effort` are phone-supplied and untrusted. They pass two explicit gates before reaching any state: the shape checks `relay.ValidModel` / `relay.ValidEffort` in `CreateConversation` (length caps 64 / 32, first byte alphanumeric, word bytes only), then membership in `sessionMinter.Create` against the resolved agent's own vocabulary via `validateModelVocabulary` / `validateEffortVocabulary`. These are the same functions `set_session_settings` runs, so create cannot store a value set cannot. The agent is resolved first (`resolveCreateAgent`), so a Codex value is never checked against Claude's list or the reverse (ADR 039).
- [Tokens] No findings — no secret is introduced; session and conversation ids stay server-minted (`sessions.NewID`, `conversations.NewID`, crypto/rand).
- [File operations] No findings — every refusal returns before `resolveSpawnDir` (which trust-marks a directory) and before `Pool.MintWith` (which writes the session's settings file and the registry), so a refused request touches no file. An accepted request writes through the existing `buildSessionAs` / `saveLocked` path, unchanged.
- [Subprocess] No findings — a stored model reaches claude as the separate argv element after `--model` (`Session` arg builder, exec without a shell); the shape check's alphanumeric first byte makes a value starting with `-` unrepresentable, so it cannot be read as a flag. Codex receives it inside its JSON-RPC request, not argv.
- [Posture] No findings — the composed settings copy only Model and Effort (`sessionMinter.Create` builds `sessions.SessionSettings{Model, Effort}` field by field), and `MintDefaults` is `mintSettings`, which excludes posture structurally. No create request can mint a session with YOLO or a permission mode. `MintWith` itself uses its argument verbatim; its doc states callers pass no posture, and its only callers are `MintAs` and `sessionMinter.Create`.
- [Crypto] No findings — no primitive used.
- [Network & I/O] No findings — the payload is bounded by the existing frame cap; the new fields are further capped by the shape checks before any lookup.
- [Logs / errors] No findings — reply messages are static constants (`msgCreateConversationMalformed`, `relay.MsgSettingsModelNotOffered`, `relay.MsgModelListUnavailable`); the refusal log carries conn id and a static reason only, never the requested value or vocabulary. SHOULD FIX guard for Phase B: the handler test asserts the requested value does not appear in the captured log.
- [Oracle] No findings — distinguishing "not offered" from "vocabulary unavailable" reveals whether a model is on the menu, which `request_model_list` already publishes to the same authenticated client; `set_session_settings` exposes the identical distinction.
- [Concurrency] No findings — one settings snapshot is validated and minted; no lock held across `MintWith`'s file writes.
- [Threat model] No findings — the client is a paired, Noise-authenticated device already holding `set_session_settings`; this adds no capability, only an earlier moment to exercise it.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-25
