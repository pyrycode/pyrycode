# #2647 — a capable client can create a Codex conversation

## Files read

- `internal/relay/handlers/create_conversation.go` → `CreateConversation`, `SessionCreator`, `ErrSpawnDirRejected` — the handler that gains the `agent` field, its validation, and the reply's `agent`.
- `internal/protocol/conversations_write.go` → `CreateConversationPayload`, `ConversationCreatedPayload` — request and reply payloads that each gain one field.
- `internal/protocol/conversations_read.go` → `ConversationSummary.Agent`, `AgentClaude`, `AgentCodex` — the wire vocabulary and the `omitempty` precedent the reply's field mirrors.
- `internal/relay/handlers/list_conversations.go` → `AgentOf`, `ListConversationsWithAgents` — how a list row's agent is gated on `c.MultiAgent()`; the reply here follows the same rule.
- `internal/dispatch/dispatch.go` → `Conn.MultiAgent`, `Conn.SetMultiAgent` — the capability read and the test hook.
- `internal/protocol/codes.go` → `CodeProtocolUnsupported`, `CodeProtocolMalformed` — the existing codes available for the refusals.
- `internal/sessions/pool.go` → `Pool.Mint`, `Pool.mintSettings`, `Pool.buildSession`, `Pool.buildSessionAs`, `Pool.HarnessFor`, `Pool.SettingsFor` — the mint path, the harness-aware builder only revive reaches today, and the accessors the tests read.
- `internal/sessions/runnerstate.go` → `HarnessClaude`, `RunnerConfig.Harness` — the pool's harness vocabulary ("claude"; any other value is carried unvalidated to the factory).
- `internal/sessions/registry.go` → `canonicalHarness`, `harnessForDisk` — a non-claude harness is persisted on the entry, which is what makes the agent survive a restart.
- `cmd/pyry/main.go` → `sessionMinter`, `selectInteractiveRunner` (the harness switch), `sessionHarness` — the production adapter and the factory that routes `"codex"`.
- `cmd/pyry/codex_runner.go` → `harnessCodex`, `newCodexRunnerFactory`, `codexTurnSettings` — the Codex runner reads model/effort from the argv, so a zero-settings session reaches Codex with none.
- `cmd/pyry/codex_thread_registry_test.go` → `startCodexPool`, `awaitEnvelope`, `isText` — the real-pool-plus-fake-Codex fixture the end-to-end test reuses.
- `internal/sessions/pool_mint_settings_test.go` → `helperPoolMintBootstrap` — the fixture that gives the bootstrap an operator model/effort, used to prove a Codex mint does not inherit them.
- `internal/relay/handlers/create_conversation_test.go` → `stubSessionCreator`, `newCreateConvConn` — the handler test doubles.

In-flight overlap check: no remote `feature/*` branch touches any of the files above.

## Context

`create_conversation` always mints a Claude session: `sessionMinter.Create` → `Pool.Mint` → `buildSession`, which hard-codes `HarnessClaude`. The pool can already build a Codex session (`buildSessionAs`) and the production factory already routes `"codex"`, but only reviving a dormant entry reaches it. This ticket opens the creation path to a `multi_agent` client, keeping an older client's experience byte-identical.

## Design

### Wire

- `CreateConversationPayload` gains `Agent *string \`json:"agent,omitempty"\``. Pointer so absent/null (nil) is distinct from `""`; `omitempty` so the existing round-trip test's encoding of a request without it stays byte-identical.
- `ConversationCreatedPayload` gains `Agent string \`json:"agent,omitempty"\``, set only for a `multi_agent` conn (the `ConversationSummary.Agent` rule). Empty for an older client → key absent → byte-identical reply.

### Handler (`CreateConversation`)

After the payload decodes and **before** `conversations.NewID` / `creator.Create`, resolve the agent:

- `p.Agent == nil` → `protocol.AgentClaude`.
- `*p.Agent == AgentClaude` → claude (any client).
- `*p.Agent == AgentCodex` → codex if `c.MultiAgent()`; otherwise refuse.
- anything else (including `""`) → refuse.

Both refusals reply `protocol.CodeProtocolUnsupported`, non-retryable, with a static message each (`msgCreateConversationAgentUnsupported` for the capability refusal, `msgCreateConversationAgentUnknown` for an unknown value). The requested value is never echoed on the wire or logged verbatim (it is client-controlled); the log line carries `conn_id` and the event only. Because the check precedes the mint, a refusal creates nothing — no pool session, no registry entry, no row.

`protocol.unsupported` over `protocol.malformed`: the frame is well-formed; it asks for something this daemon will not do for this client. One code for both refusals keeps the client's handling to one branch; the messages distinguish them for a human.

`SessionCreator.Create` gains a fourth parameter: `Create(ctx, label, spawnDir, agent string) (sessionID, dir string, err error)`. `agent` is the resolved wire value (`AgentClaude` / `AgentCodex`). Two implementers: `stubSessionCreator` (tests) and `sessionMinter`.

The reply's `Agent` is the resolved agent when `c.MultiAgent()`, else `""`.

### Pool (`internal/sessions`)

New entry point `Pool.MintAs(label, spawnDir, harness string) (SessionID, error)`; `Mint` becomes `return p.MintAs(label, spawnDir, HarnessClaude)` so its ~25 test call sites are untouched. `MintAs` is today's `Mint` body with two differences:

- settings: `mintSettings()` for a canonical-claude harness; the zero `SessionSettings` otherwise — a Codex session starts at Codex's own defaults, never the operator's Claude model/effort (the argv then carries no `--model`/`--effort`, so `codexTurnSettings` yields none).
- build: `buildSessionAs(id, label, spawnDir, settings, harness, "")` instead of `buildSession`.

The harness is carried onto the `Session` by `buildSessionAs` and persisted by `saveLocked` via `harnessForDisk`, so it survives a restart as the dormant entry's `harness`. No harness validation in the pool: the factory remains the one place that decides which harnesses have a runner (`RunnerConfig.Harness` contract).

### cmd/pyry (`sessionMinter.Create`)

Forwards `agent` as the harness: `m.p.MintAs(label, resolved, agent)`. The wire vocabulary and the harness vocabulary are the same strings (`protocol.AgentCodex == harnessCodex == "codex"`, `AgentClaude == sessions.HarnessClaude`), the equality `AgentOf` already relies on. Only the handler-validated values reach here.

### Data flow

```
client create_conversation{agent}
  → CreateConversation: decode → resolve agent (refuse → error reply, stop)
  → NewID → creator.Create(ctx, convID, spawnDir, agent)
       → sessionMinter: resolveSpawnDir → Pool.MintAs(label, dir, agent)
            → buildSessionAs(harness=agent, settings=zero for codex) → register → save → supervise
  → reg.Create(row) → Save → reply conversation_created{..., agent if multi_agent}
first message → drain Activate → factory routes "codex" → Codex runner
```

## Concurrency model

Unchanged. `MintAs` keeps `Mint`'s locking exactly (RLock around `mintSettings`, only taken on the claude arm; build off-lock; Lock for register + save). No new goroutines.

## Error handling

- Refusals: `protocol.unsupported`, `retryable: false`, before any side effect.
- A Codex host with no usable Codex (binary missing, too old, not signed in): `newCodexRunnerFactory` refuses at build, inside `MintAs`, so the create fails as today's mint failures do — retryable `server.binary_offline`, no row, nothing registered (`buildSessionAs` removes its files). Not a new path.

## Testing strategy

- `internal/protocol` — round-trip: a request with `"agent":"codex"` decodes to a non-nil pointer; one without it re-encodes without the key. Reply: `Agent` empty → no `agent` key; set → present.
- `internal/relay/handlers` (stub creator records `agent`):
  - capable conn + `"codex"` → `Create` got `"codex"`; reply `agent == "codex"`.
  - capable conn + absent → `Create` got `"claude"`; reply `agent == "claude"`.
  - older conn + absent → `Create` got `"claude"`; reply JSON has no `agent` key.
  - older conn + `"codex"` → `protocol.unsupported`, non-retryable; `Create` never called; no row.
  - capable conn + `"gemini"` (and `""`) → same refusal; nothing created.
- `internal/sessions` — `MintAs(…, "codex")` on a pool whose bootstrap carries opus/high: `HarnessFor` answers `codex`, `SettingsFor` is zero, the persisted entry's `harness` is `codex`; a second pool loaded from that registry answers `HarnessFor == codex` from the dormant entry. `Mint` still inherits (existing `TestPool_CreateIn_InheritsOperatorSettings` family unchanged).
- `cmd/pyry` — through `sessionMinter{pool}.Create(ctx, "conv-1", "", "codex")` on `startCodexPool`: `Activate` builds a `*codexRunner`, a user turn produces a text event from fake Codex (the conversation's first message runs on Codex), and after the pool stops a fresh pool on the same registry answers `HarnessFor == codex`.

## Open questions

- None blocking. Error-code choice resolved above (`protocol.unsupported`).

## Documentation handoff (pending — documentation stage)

`docs/protocol-mobile.md` § create_conversation and § conversation_created: the optional `agent` request field (`"claude"` / `"codex"`, absent = claude); the `agent` reply field, present only for a `multi_agent` client; the two refusals (`"codex"` from a client without `multi_agent`, any unknown value) — both `protocol.unsupported`, non-retryable, nothing created; a new Codex conversation starts at Codex's own default model and effort. Add a changelog entry.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] No findings — the `agent` string is client-controlled and crosses into the daemon at `CreateConversation`, where it is resolved against the closed set {`AgentClaude`, `AgentCodex`} before anything else happens; everything downstream (`SessionCreator.Create`, `Pool.MintAs`, the runner factory's harness switch) receives only one of those two constants. The capability gate is the per-conn `c.MultiAgent()` decision negotiated at handshake, not a payload field, so a client cannot claim it in-band.
- [Tokens] No findings — no tokens involved; session and conversation ids stay server-minted (`sessions.NewID`, `conversations.NewID`).
- [File operations] No findings — no new path handling. `spawnDir` validation is unchanged (`resolveSpawnDir` in `sessionMinter.Create` runs before `MintAs` for both agents). The registry write is the existing `saveLocked` path; `harnessForDisk` adds one string field it already supports.
- [Subprocess] SHOULD FIX (addressed by design) — the harness selects which binary the daemon runs. Only the two validated constants can reach the factory, and the factory's `default` arm refuses anything else. The Codex argv carries no client-supplied values from this frame (no model/effort, and the zero settings keep the posture at the default mode). Phase B must keep the validation ahead of `NewID`/`Create`; the handler tests assert `Create` is never called on a refusal.
- [Privilege] No findings — a Codex mint starts from zero `SessionSettings`, so it inherits neither the operator's model/effort nor any posture; the bypass was already never inherited (#1575).
- [Crypto] N/A by design — no cryptographic operation added or changed.
- [Network & I/O] No findings — the frame size is already capped by the dispatcher; the new field is one short string that is compared, never stored or echoed.
- [Error messages, logs] No findings — refusal messages are static constants; the requested agent value is neither echoed nor logged (it is attacker-chosen text). Log lines carry `event` and `conn_id` only, mirroring the malformed-payload branch.
- [Concurrency] No findings — `MintAs` preserves `Mint`'s lock discipline; the capability read is a construction-fixed bool.
- [Threat model] No findings — a paired but older client cannot create a Codex conversation whose pushed frames it could not render (the #2644 gate's premise); a capable client gains only what its capability already advertises. Switching an existing conversation's agent is OUT OF SCOPE (its own ticket, per the issue).

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-25
