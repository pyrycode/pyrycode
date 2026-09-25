# #2629 — check a session's model and effort against its own agent's advertised models and levels

## Files read

- `internal/relay/v2session_settings.go` → `handleSetSessionSettings` (wire boundary, reply mapping), `validModel` (the grammar-plus-bound shape the new effort check mirrors), `validEffort` (the closed set being replaced).
- `internal/relay/v2session_seams.go` → `ErrSessionUnknown`, `ErrModelNotOffered`, `ErrModelVocabularyUnavailable` — the sentinel family the new effort sentinel joins.
- `cmd/pyry/main.go` → `settingsUpdaterAdapter.UpdateSettings`, `requireKnownSession`, `validateModelVocabulary` — the gate this ticket widens.
- `cmd/pyry/session_model_list.go` → `savedModelVocabulary`, `retainedModelVocabulary`, `savedModelList` — Claude's three sources; the store is the Codex half's only source.
- `cmd/pyry/model_vocabulary_store.go` → `modelVocabularyStore.CodexModels` (deep copy, nil-receiver-safe) — Codex's families with `EffortLevels`.
- `cmd/pyry/codex_runner.go` → `harnessCodex` — the harness name a Codex session carries.
- `internal/sessions/pool.go` → `SettingsFor`, `DormantSettingsFor`, `Lookup`, `buildSessionAs` — the live/dormant partition; `Session.harness` is set once at construction.
- `internal/sessions/registry.go` → `registryEntry.Harness`, `canonicalHarness` — a dormant entry's harness, empty meaning claude.
- `internal/streamsup/parser.go` → `maxModelEffortLevel` (32), `maxModelEffortLevelCount` — a Claude entry's levels can be cut, reported as `"effort_levels"` in `TruncatedFields`.
- `internal/sessions/session.go` → `claudeSettingsArgs` (`--effort` as its own argv element); `internal/sessions/pool.go` → `deliverSettingsInBand` (`/effort <level>` user turn). The two Claude sinks; neither changes.
- `cmd/pyry/agent_run.go` → `validEfforts` — the dispatcher's closed set; untouched.

No in-flight `feature/*` branch touches these files.

## Context

A phone's `set_session_settings` passes relay's `validEffort` (closed {low, medium, high, xhigh, max}) and then `settingsUpdaterAdapter`, which checks a model against Claude's entries only and never checks effort. Codex's `ultra` is refused at the wire and a Codex family (`luna`) is refused as not offered. #2627 stores each agent's entries with their advertised effort levels; a capability belongs to the agent and the model together.

## Design

### Relay (`internal/relay`)

**`validEffort` becomes a grammar**, the shape check at the untrusted boundary:

```
effort    := "" | first rest{0,31}
first     := [a-z0-9]
rest      := [a-z0-9_-]
```

So `ultra` passes; a leading `-` or `_`, uppercase, whitespace, control bytes, bytes ≥ 0x80 and anything over 32 bytes are refused with today's `protocol.malformed` reply. 32 is the Codex level alphabet's bound and streamsup's `maxModelEffortLevel`. Whether a well-formed level is *offered* is the adapter's question, not relay's.

**New sentinel `ErrEffortNotOffered`** in `v2session_seams.go`, beside `ErrModelNotOffered`. `handleSetSessionSettings` maps it to `CodeProtocolMalformed` + `msgSettingsMalformed`, not retryable — byte-identical to the closed set's refusal. No log line (the closed-set refusal had none; the value is never logged).

### Sessions (`internal/sessions`)

**`(*Pool).HarnessFor(id SessionID) (string, error)`** — the canonical harness of the live session `id`, else of its dormant entry (`canonicalHarness`), else `ErrSessionNotFound`. One `RLock`, two map reads, live first. The empty id misses both maps naturally (it is not a key; unlike `Lookup`, no bootstrap fallback). No logging, error returned bare — `DormantSettingsFor`'s posture.

### Adapter (`cmd/pyry`)

`savedModelVocabulary` gains `CodexModels() []turnevent.ModelOption`; `*modelVocabularyStore` already implements it.

**`agentModelVocabulary(pool, saved, harness, boundSessionID) (turnevent.ModelList, bool)`** in `session_model_list.go`:
- `sessions.HarnessClaude` → `retainedModelVocabulary` (unchanged three sources).
- `harnessCodex` → the store's `CodexModels()` as a `ModelList`, `have` iff non-empty; nil store → none.
- any other harness → none.

**`validateEffortVocabulary(list, have, model, effort) error`** (pure):
- `effort == ""` → nil (clear).
- Find the entry for `model`: exact `Value` match, `have`, `model != ""`, and neither `"value"` nor `"effort_levels"` in its `TruncatedFields` (a cut entry cannot prove what it offers).
- Entry found → nil iff `effort` is in its `EffortLevels` (exact); an entry advertising no levels accepts none → `relay.ErrEffortNotOffered`.
- No entry → today's fallback set {low, medium, high, xhigh, max}, else `relay.ErrEffortNotOffered`.

**`settingsUpdaterAdapter.UpdateSettings`** — when the update carries a non-empty model or a non-empty effort:
1. Empty id → `relay.ErrSessionUnknown` (existing guard).
2. `HarnessFor(id)`; miss → `relay.ErrSessionUnknown` **before** any vocabulary read. This replaces `requireKnownSession` (Lookup + DormantSettingsFor), which answered the same question.
3. `list, have := agentModelVocabulary(...)`.
4. Non-empty model → `validateModelVocabulary(list, have, model)` (unchanged function, now fed the agent's list).
5. Non-empty effort → the model the session will run: the update's own model if `u.Model != nil` (including `""`, which has no entry and falls back), else the stored one via `storedModel(id)` = `SettingsFor`, then `DormantSettingsFor`; both missing → `relay.ErrSessionUnknown` (the revive-between-reads race; same answer the write path gives). Then `validateEffortVocabulary`.
6. The existing live-then-dormant write, unchanged.

An empty effort, an empty model, and posture-only updates skip steps 2–5 exactly as today.

## Concurrency model

No goroutines. `HarnessFor`, `SettingsFor`, `DormantSettingsFor` each take `Pool.mu` read once, called with nothing held. `CodexModels` takes the store's leaf mutex. The reads are advisory by the time the write runs (a revive can move the id dormant→live); the harness is construction-fixed and carried across a revive, so it cannot change under the check. The stored model can change between the check and the write only by another concurrent `set_session_settings` — the same window the model check has today.

## Error handling

| Case | Adapter returns | Wire reply |
|---|---|---|
| unknown / empty id | `ErrSessionUnknown` | `session.not_found` |
| model absent from complete list | `ErrModelNotOffered` | `protocol.malformed` "requested model is not offered" |
| no / incomplete list for a model | `ErrModelVocabularyUnavailable` | `model_list.unavailable`, retryable |
| effort not offered by entry / not in fallback | `ErrEffortNotOffered` | `protocol.malformed` "malformed set_session_settings request" |

Model is checked before effort, so a frame with both an unoffered model and an effort answers the model reply.

## Testing strategy

RED first: relay `ultra` acceptance and the adapter's Codex-family acceptance fail today.

- `internal/relay`: `TestValidEffort` rewritten for the grammar — `ultra`, `xhigh`, `a`, 32-byte value accepted; `-high`, `_x`, `LOW`, `" "`, 33 bytes refused (**intended change**: it pinned `ultra` refused). A byte-closure test walking all 256 bytes at first and later positions. `TestV2Session_SetSessionSettings_MalformedRejected`'s `"invalid effort ultra"` row becomes a leading-dash row (**intended change**). A handler test: a seam returning `ErrEffortNotOffered` gets the malformed, non-retryable reply.
- `internal/sessions`: `HarnessFor` — live claude session, dormant entry with `harness: codex`, dormant entry with no key (→ claude), unknown and empty id → `ErrSessionNotFound`.
- `cmd/pyry`: table test for `validateEffortVocabulary` (entry lists level; entry lacks it; entry with no levels; model with no entry → fallback in/out; `ultra` via fallback refused; truncated levels → fallback; empty effort). Adapter tests through a real pool with a registry seeding one Claude and one Codex dormant entry and a store double holding Claude entries plus Codex families:
  - Codex family on the Codex session accepted, refused (not offered) on the Claude session; Claude alias refused on the Codex session.
  - Codex session, no Codex entries → unavailable.
  - `ultra` on Codex `luna` (advertised) accepted; on a Claude model not advertising it refused; `xhigh`/`max` per entry.
  - effort-only update validates against the stored model; update's own model wins over stored.
  - unknown id with effort → `ErrSessionUnknown` and the store is not read (counting double).
  - stored model with no entry → fallback set.
  - live session: one test against a live (bootstrap) Claude session.
- Existing Claude suites run; any that break because a Claude entry doesn't advertise a level get updated and named in the PR.

## Open questions

- Whether existing adapter tests set efforts against seeded Claude entries whose `EffortLevels` omit them — resolved by running `./cmd/pyry` tests; any update is listed in the PR.

## Documentation handoff

Pending for the documentation stage:
- `docs/protocol-mobile.md` § `set_session_settings`: effort is now a grammar at the wire (`[a-z0-9][a-z0-9_-]{0,31}`) and is accepted only when the session's model advertises it (fallback set when the model has no entry); a model is checked against the session's own agent.
- The relay / cmd-pyry settings sections of the owning package overviews under `docs/knowledge/features/`.
- ADR candidate: "a capability belongs to the agent and the model together" (agreed 2026-09-10) as the validation rule for settings.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] No findings — the phone's effort crosses one boundary, `validEffort` in `internal/relay/v2session_settings.go`, now a closed-alphabet grammar with a mandatory alphanumeric first byte and a 32-byte bound; the adapter's `validateEffortVocabulary` is a second, membership check that only narrows. Every accepted value is also either an exact member of an agent-advertised list or of the fallback five, so the adapter never admits a value relay's grammar alone would not.
- [Trust boundaries] The advertised lists are subprocess-derived (claude `initialize`, codex `model/list`) and so partly untrusted; they are used only as an allowlist to compare against, never as output. A hostile child advertising `-flag` cannot get it past relay's grammar, which runs first and does not consult the list.
- [Subprocess] No findings — sinks unchanged: claude argv gets `--effort` and the value as separate elements (`claudeSettingsArgs`, no shell), in-band `/effort <level>` is a JSON-encoded user turn, Codex's is a JSON field. The grammar excludes `-` first (flag posing), whitespace and newlines (turn/line injection) and any byte ≥ 0x80. SHOULD FIX (implemented as a test): the byte-closure test over all 256 bytes, so the alphabet is machine-checked like `validModel`'s.
- [Tokens / Crypto / File ops] Not applicable — no secret, key, randomness or file path is read or written; `HarnessFor` is a map read.
- [Error messages / logs] No findings — the new refusal reuses the fixed `msgSettingsMalformed` constant; neither the effort, the model nor any list value reaches a reply or a log. `HarnessFor` returns the bare sentinel.
- [Existence oracle] No findings — `HarnessFor`'s miss answers `session.not_found` before any vocabulary read, so an unknown id cannot probe vocabulary completeness or learn which agent a session runs; for a known id on an interactive connection the agent is not secret. Tested with a counting store double.
- [Concurrency] No findings — single-lock reads, no new lock edge; the check-then-write gap is the one the model check already has, and the harness cannot change across it.
- [Network & I/O] No findings — no new reads from sockets; payload size is already bounded by the frame decoder.
- [Threat model] OUT OF SCOPE — `agent-run`'s `validEfforts` (dispatcher path) stays a closed set by the ticket's instruction.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-25
