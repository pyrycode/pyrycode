# #2646 — each session's capability list, reported to capable clients

## Files read

- `internal/relay/v2session_settings.go` → `handleRequestSessionSettings` (the reply this ticket extends; runs on the conn's app-frame worker), `handleSetSessionSettings` (the refusals, unchanged), `validModel`, `validEffort`, `validPermissionMode` (the switch that stays the authority for permission modes; its doc says why no package-level collection may hold the vocabulary).
- `internal/relay/v2session.go` → `appFrameJob`, the `TypeRequestSessionSettings` arm of `dispatchAppFrame` (enqueues on Run), the worker's `appFrameSessionSettingsRequest` arm; `V2Session.multiAgent` (Run-owned, set in the handshake).
- `internal/relay/v2session_seams.go` → `RunConfig`, `V2SessionConfig.RunConfigFor`, `V2SessionConfig.EffectiveEffortFor` — the seam posture the new one copies (resolved id from the daemon's record, comma-ok, optional).
- `internal/protocol/settings.go` → `SessionSettingsPayload` — gains the optional object; must stay comparable (`assertSessionSettingsPayload` compares with `!=`).
- `cmd/pyry/main.go` → `settingsUpdaterAdapter.UpdateSettings`, `validateModelVocabulary`, `fallbackEffort`, `validateEffortVocabulary` — the checks the list must agree with; `relayV2Wiring` construction (`settings: settingsUpdaterAdapter{pool, modelVocabulary}`).
- `cmd/pyry/relay.go` → `relayV2Wiring`, the `V2SessionConfig` literal (`RunConfigFor`, `SettingsUpdater`).
- `cmd/pyry/session_model_list.go` → `agentModelVocabulary` — the per-agent vocabulary both the check and the list read.
- `cmd/pyry/codex_runner.go` → `codexRunner.WriteUserTurn` (starts a NEW turn via `codexsup.Client.StartTurn`; nothing steers a running one), `codexRunner.Interrupt`.
- `cmd/pyry/main.go` → `newInboundDeliver` — agent-agnostic: every queued message waits for the conversation's turn to go idle (`waitIdleForDelivery`) before `WriteUserTurn`. So a message sent mid-turn never reaches the running turn, for either agent.
- `cmd/pyry/settings_agent_vocabulary_test.go` → `agentVocabularyDouble`, `claudeEntries`, `codexEntries`, `newAgentVocabularyAdapter` — fixtures the new cmd tests reuse.
- `internal/relay/v2session_settings_read_test.go` → `readManagerFor`, `readSeams`, `resolveFixtureConv`; `internal/relay/v2session_modelrequest_test.go` → the multi_agent-vs-old table pattern.

## Context

Slice S4a of Codex support, daemon side. A `multi_agent` client builds a session's controls from a per-session capability list; the daemon keeps refusing anything outside it. The refusals already exist (#2629 for model/effort, `validPermissionMode` for posture). This ticket reports them, built from the same code the checks run, so the list and the checks cannot drift. No check changes.

In-flight overlap: `origin/feature/449` touches `internal/relay/v2session.go` but is a stale branch of a closed issue; no dependency.

## Design

### Wire (`internal/protocol/settings.go`)

- New `SessionCapabilities` struct: `Interrupt bool "interrupt"`, `MidTurnInput bool "mid_turn_input"`, `EffortLevels []string "effort_levels"`, `PermissionModes []string "permission_modes"`, `AttachmentTypes []string "attachment_types"`, `Models []string "models"`. No omitempty inside: a present object always carries all six keys, lists as `[]` never `null`.
- `SessionSettingsPayload` gains `Capabilities *SessionCapabilities \`json:"capabilities,omitempty"\``. A pointer keeps the payload comparable and makes absence byte-identical to today's shape.

### Seam (`internal/relay/v2session_seams.go`)

- Relay-local, primitive-typed `AgentCapabilities { Interrupt, MidTurnInput bool; EffortLevels, Models []string }` — the agent-and-model-dependent half.
- `V2SessionConfig.CapabilitiesFor func(sessionID, model string) (AgentCapabilities, bool)`. Keyed by the session id and model from the ACCEPTED `RunConfig` (the daemon's own record), never the caller's conversation id. false = no session with a known agent. Optional: nil ⇒ the object is omitted.

### Relay composition (`internal/relay/v2session_settings.go`, `v2session.go`)

- `appFrameJob` gains `multiAgent bool`, captured from `s.multiAgent` on Run at the `TypeRequestSessionSettings` enqueue; the worker passes `job.multiAgent` to `handleRequestSessionSettings(ctx, s, plaintext, multiAgent)`. The worker never reads Run-owned `s.multiAgent`.
- After `RunConfigFor` accepts, and only when `multiAgent && m.cfg.CapabilitiesFor != nil`, call `CapabilitiesFor(cfg.SessionID, cfg.Model)` once. On true, attach `sessionCapabilities(agent)`:
  - `EffortLevels` = agent levels filtered to non-empty values passing `validEffort`; `Models` = agent models filtered to non-empty values passing `validModel`. The filter is what makes "every listed option passes the wire shape checks" a property of the relay rather than of the producer.
  - `PermissionModes` = `permissionModeOptions()` — a function returning a fresh slice literal of the five, never a package-level collection. `validPermissionMode` stays the authority; a test pins agreement.
  - `AttachmentTypes` = `[]string{"*/*"}`. No MIME allowlist.
- Unresolved conversation, old client, nil seam, or seam false ⇒ no `capabilities` key; every other field as today.

### Producer (`cmd/pyry/main.go`, `cmd/pyry/relay.go`)

- `effortLevelsFor(list turnevent.ModelList, have bool, model string) []string` — the first uncut exact entry's `EffortLevels`, else `fallbackEffortLevels()`. `validateEffortVocabulary` is rewritten as membership in `effortLevelsFor(...)`, so the reported levels ARE the check's set (behaviour identical; `TestValidateEffortVocabulary` pins it). `fallbackEffort` (switch) is replaced by `fallbackEffortLevels()` (fresh slice literal) — its only caller is `validateEffortVocabulary`.
- `settingsUpdaterAdapter.Capabilities(sessionID, model string) (relay.AgentCapabilities, bool)`: "" id ⇒ false; `HarnessFor` error ⇒ false; `agentModelVocabulary(a.p, a.saved, harness, id)` — the exact read `UpdateSettings` does; `Models` = `Value`s of rows whose `value` is not truncated (exactly what `validateModelVocabulary`'s first loop accepts), `[]` when no vocabulary; `EffortLevels` = `effortLevelsFor(list, have, model)`. `Interrupt`/`MidTurnInput` by harness: Claude true/false, Codex true/false; an unknown harness ⇒ false.
- `relayV2Wiring.capabilities` field (relay.go), set in main.go from the same `settingsUpdaterAdapter{pool, modelVocabulary}` value; the config literal wires `CapabilitiesFor: w.capabilities` (nil when unwired — assigned only when non-nil, per `runConfigFor`'s rule about wrappers).

### Mid-turn input, as measured

Both agents report `mid_turn_input: false`. `newInboundDeliver` holds every message until the conversation's turn is idle, and Codex's `WriteUserTurn` then starts a new turn (`turn/start`); nothing sends input into a running turn. Reported only; delivery unchanged.

## Concurrency model

No goroutines. `CapabilitiesFor` runs on the conn's app-frame worker, after `RunConfigFor`, like `EffectiveEffortFor`. It reads the pool and store through their own locks (`HarnessFor`, then `agentModelVocabulary`), sequentially, never nested — the same order `UpdateSettings` takes. `multiAgent` is copied into the job on Run; the channel send orders it.

## Error handling

No new failure modes. A false comma-ok omits the object; nothing logs a model, effort, conversation id or capability value. The reply is still owed on every path exactly as today.

## Testing strategy

- `internal/protocol`: nil `Capabilities` marshals with no `capabilities` key (byte-identical to the pre-change field set); a present one with empty lists marshals all six keys and `[]`.
- `internal/relay`:
  - old client ⇒ no key and `CapabilitiesFor` never called; capable client ⇒ object present, seam called once with the fixture's session id and model (not the conversation id), permission modes and `*/*` filled.
  - capable client, unresolved conversation ⇒ no key, seam not called; seam false ⇒ no key.
  - producer values failing `validModel`/`validEffort` (or "") are dropped.
  - `permissionModeOptions()` agrees with `validPermissionMode`: each listed passes; over a candidate set (claude's six modes, "", case variants) accepted ⇔ listed; mutating a returned slice does not change the next call.
- `cmd/pyry`:
  - `Capabilities` for a Claude session and a Codex session (fixtures from `settings_agent_vocabulary_test.go`): models, effort levels for the stored model, fallback five for an unlisted/empty model, empty models when no vocabulary, false for an unknown id.
  - For each agent: every listed model and every listed effort is accepted by `UpdateSettings`; an unlisted model gets `ErrModelNotOffered`, an unlisted effort `ErrEffortNotOffered`.
  - `TestValidateEffortVocabulary` unchanged and green (behaviour-identical rewrite).

## Size

Six production files (`protocol/settings.go`, `relay/v2session_seams.go`, `relay/v2session.go`, `relay/v2session_settings.go`, `cmd/pyry/main.go`, `cmd/pyry/relay.go`) — one over the five-file line; `v2session.go` is a one-field job change the ticket's "safe on the worker" note requires. Any split (seam+producer vs wire+relay) yields a slice whose only consumer is its sibling, so the floor rule merges it back; building as one. ~600 lines total.

## Open questions

- None blocking.

## Documentation handoff (pending, documentation stage)

`docs/protocol-mobile.md` § session_settings: document the `capabilities` object, each field's meaning (`interrupt`, `mid_turn_input` — false for both agents today, `effort_levels` for the current model incl. the fallback five, `permission_modes` without bypass which stays on `yolo`, `attachment_types` `["*/*"]`, `models` the session's own agent's values, empty when unavailable), when it is absent (client without `multi_agent`, no resolved session), and that the daemon refuses a request outside it whatever the client shows.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] No findings. The one untrusted input is `request_session_settings`'s `conversation_id`, still consumed only by `RunConfigFor` as a registry key. `CapabilitiesFor` never sees it: it is keyed by `RunConfig.SessionID` and `RunConfig.Model`, both from the daemon's own records, and is called only after `RunConfigFor` accepted. The `multi_agent` flag is the handshake's negotiated decision (`V2Session.multiAgent`), copied on Run into `appFrameJob`, not a per-request client claim.
- [Trust boundaries — enforcement] No findings. The list is advisory; enforcement is unchanged and client-independent: `handleSetSessionSettings` runs `validModel`/`validEffort`/`validPermissionMode` and `settingsUpdaterAdapter.UpdateSettings` runs the per-agent membership checks for every conn before anything reaches the pool or runner. The list is derived from those same functions (`effortLevelsFor` is the check's own set; models are exactly the rows the check's exact-match loop accepts; permission modes are pinned against the switch by test), and the relay drops any producer value its shape checks would refuse, so the list can never advertise something the daemon refuses. Nothing in the list widens a check: bypass is not listed and stays reachable only through `yolo`.
- [Trust boundaries — vocabulary integrity] No findings. `permissionModeOptions()` and `fallbackEffortLevels()` return fresh slice literals, so no package-level mutable collection holds a security vocabulary and no caller can append to the next reply's list or to the check's set; `validPermissionMode` stays a switch. Producer slices come from `agentModelVocabulary`, whose sources return deep copies (`cloneModelList`); the relay filter builds new slices.
- [Tokens, secrets, credentials] Not applicable. No token, key or credential is read or carried.
- [File operations] Not applicable. No file is opened; the vocabulary comes from in-memory holds and the already-loaded store.
- [Subprocess] Not applicable. Nothing is spawned and nothing reaches an argv.
- [Cryptographic primitives] Not applicable. The reply rides the existing Noise session.
- [Network & I/O — bounds] No findings. The object is bounded by its sources: Claude's list by `maxModelListEntries` rows with values ≤ 64 bytes after `validModel`, Codex's by `maxModelFamilies` (8) families of ≤ 32 bytes; effort levels by the producer caps and `validEffort`'s 32 bytes; five permission modes; one attachment type. A few KiB at most, well under the Noise frame limit; a too-large frame would fail sealing and not be sent (fail closed).
- [Error messages, logs] No findings. No new log line; the existing debug log carries only `conn_id`. No capability value, model, effort or id is logged or placed in an error string.
- [Concurrency] No findings. `CapabilitiesFor` runs on the worker like `EffectiveEffortFor`; it takes `HarnessFor`'s and the vocabulary's locks sequentially, never nested, in the same order `UpdateSettings` does. The worker does not read Run-owned `s.multiAgent`; the value travels in the job. No goroutine is added.
- [Disclosure] No findings. The object reaches only a conn that negotiated `multi_agent` and asked about a conversation it can already address; it names model values that conn's `model_list` already shows (#2651) and the fixed permission/attachment vocabulary. An old client's reply is byte-identical to today's.
- [Threat model alignment] No findings. Per `docs/protocol-mobile.md` § Security model the relay sees only ciphertext; the reply travels inside the paired, authenticated session like today's `session_settings`.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-25
