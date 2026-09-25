# #2643 — `multi_agent` capability and the per-conversation `agent` field

## Files read

- `internal/protocol/handshake.go` → `CapabilityInteractive`, `CapabilityContextUsage` — the vocabulary block the new constant joins and the "detection only" doc shape to mirror.
- `internal/protocol/conversations_read.go` → `ConversationSummary` — the row that gains `agent`; its omitempty discipline decides byte-identity for old clients.
- `internal/relay/v2session_handshake.go` → `supportedV2Capabilities`, `negotiateCapabilities`, `handleNoiseInit` — where the negotiated set is computed and `s.interactive` is recorded; new members are APPENDED.
- `internal/relay/v2session.go` → `V2Session.interactive`, `routeAppFrame` — the session field to sit beside, and the one production site that builds a `*dispatch.Conn` per frame.
- `internal/dispatch/dispatch.go` → `Conn`, `NewConn`, `NewTestConn`, `Conn.Auth` — the per-frame handler surface; `NewTestConn` has 17 call sites and must keep its signature.
- `internal/relay/handlers/list_conversations.go` → `ListConversations`, `ConversationLister` — the reply producer. `ListConversations(` has 13 call sites (12 in tests), so its signature stays too.
- `internal/sessions/pool.go` → `Pool.HarnessFor` — live + dormant harness read; `ErrSessionNotFound` for anything else. Canonical, so an empty persisted harness already reads `claude`.
- `cmd/pyry/relay.go` → `relayWiring`, the `Handlers` map in `startRelayV2` — where the handler is wired; `startRelayV2` holds no pool, so the harness read arrives as a closure like `modelWindows`.
- `cmd/pyry/main.go` → the `relayWiring{...}` literal, `sessionModelWindows` — the composition root where pool-backed closures are built.
- `cmd/pyry/dormant_settings_write_test.go` → `newDormantWritePool` — real pool warm-started with a dormant entry whose fields a test splices in (`"harness":"codex",`); reused for the daemon-level test.
- `internal/relay/v2session_test.go` → `TestNegotiateCapabilities`, `TestV2Session_Handshake_CapabilityNegotiation`, `TestV2Session_OpenState_HandlerAuthDevice`, `TestV2Session_RekeyResponder_HappyPath_RoundTripUnderNewKeys` — tables to extend and the propagation/re-key patterns to mirror.

## Context

Slice S4a of Codex support. A client that understands several agents advertises `multi_agent` and gets an `agent` on every `conversations` row; every other client gets today's list minus Codex conversations, so an app that only knows Claude never meets a conversation it cannot drive. The negotiated decision must be readable by #2644 (pushed frames, from the session) and #2645 (`model_list`, a handler / manager verb), so it lives both on `V2Session` and on `dispatch.Conn`.

Size: 8 production files against the ≤5 line (the refiner estimated 7, stated the overage and kept one ticket because the constant, the session field and the Conn accessor are each one-consumer slices of this deliverable — the floor rule). The eighth is `cmd/pyry/main.go`, because the pool-backed closure is built at the composition root. Total written work is estimated ~450 lines, one deliverable, 4 ACs, no new exported types beyond one func type — within every other line.

## Design

### Protocol

- `protocol.CapabilityMultiAgent = "multi_agent"` in `handshake.go`, doc'd like its neighbours: it grants no interactive access; it gates the `agent` field and the Codex-row filter on `conversations` (and, later, #2644/#2645).
- `protocol.AgentClaude = "claude"`, `protocol.AgentCodex = "codex"` in `conversations_read.go` — the closed wire vocabulary of the field.
- `ConversationSummary.Agent string \`json:"agent,omitempty"\``. Empty for a client without the capability → key absent → row byte-identical to today's. A capable client always gets a non-empty value.

### Relay

- `supportedV2Capabilities` gains `protocol.CapabilityMultiAgent`, APPENDED after `CapabilityContextUsage`.
- `V2Session.multiAgent bool`, set in `handleNoiseInit` beside `s.interactive` with the same value-specific `slices.Contains(negotiated, protocol.CapabilityMultiAgent)`. Re-key never touches it (as with `interactive`), so it survives.
- `routeAppFrame` calls `conn.SetMultiAgent(s.multiAgent)` after `dispatch.NewConn`, before `Route` starts.
- No `interactive` gate changes.

### Dispatch

- `Conn.multiAgent bool`, `func (c *Conn) MultiAgent() bool`, `func (c *Conn) SetMultiAgent(bool)`. The setter must be called before the Conn is handed to `Route` (the goroutine start is the happens-before edge); written once, so no lock. Constructors unchanged — tests opt in with the setter.

### Handler

- New exported func type `handlers.SessionHarnessFunc func(sessionID string) (harness string, ok bool)`.
- New constructor `ListConversationsWithAgents(reg ConversationLister, harnessFor SessionHarnessFunc) dispatch.Handler` holding today's body plus:
  - per row, `agent := agentOf(conv, harnessFor)`: `AgentClaude` when `CurrentSessionID == ""`, when `harnessFor` is nil, or when it reports `ok == false`; `AgentCodex` exactly when the harness equals `"codex"`; `AgentClaude` otherwise (closed mapping — the wire only ever carries the two values).
  - `c.MultiAgent()` true → set `Agent` on the row. False → skip rows whose agent is `AgentCodex`; leave `Agent` empty.
- `ListConversations(reg)` stays and returns `ListConversationsWithAgents(reg, nil)` — the agent-blind form every existing test uses; with no harness read every row is Claude, which is exactly today's reply.

### Wiring

- `relayWiring.sessionHarness func(sessionID string) (string, bool)` (nil in foreground/v1 → agent-blind).
- `startRelayV2` wires `handlers.ListConversationsWithAgents(w.convReg, w.sessionHarness)`.
- `main.go`: `sessionHarness(pool *sessions.Pool) func(string) (string, bool)` wrapping `pool.HarnessFor`, `err == nil` as ok; set on the `relayWiring` literal.

## Concurrency model

No new goroutines. `s.multiAgent` follows `s.interactive`'s single-writer regime (written on Run before `V2StateOpen`, read on Run by `routeAppFrame`). `Conn.multiAgent` is written before the Route goroutine starts and only read after. `Pool.HarnessFor` takes the pool's RLock; the handler holds no lock across it.

## Error handling

A harness miss (unknown or evicted session) reads `claude`, per AC 2 — it is not an error and does not drop the row for a capable client. For a non-capable client the same miss keeps the row (fail open to today's list). No new error replies.

## Testing strategy

- `internal/relay/v2session_test.go`
  - `TestNegotiateCapabilities`: `multi_agent granted`; all five in reverse → supported order.
  - `TestV2Session_Handshake_CapabilityNegotiation`: `multi agent alone grants no interactive` (ack echoes it, flag false); all five → interactive true.
  - New `TestV2Session_MultiAgent_ReachesHandlerAndSurvivesRekey`: table {advertised multi_agent → true, advertised interactive only → false}; a handler records `c.MultiAgent()` for a frame before and after a re-key from the same initiator static; both readings equal the expectation, and `s.multiAgent` matches after stop.
- `internal/relay/handlers/list_conversations_test.go`: registry with a Claude conversation (bound session → claude), a Codex one (→ codex), one with no bound session, one bound to a session the reader misses.
  - capable conn: four rows, agents claude/codex/claude/claude.
  - non-capable conn: three rows, Codex absent; each row's raw JSON has no `agent` key and equals the bytes `ListConversations(reg)` produces for the same rows on a registry without the Codex conversation.
- `cmd/pyry` daemon-level test: `newDormantWritePool(t, "\"harness\":\"codex\",")`, conversations registry binding one conversation to the live bootstrap and one to the dormant Codex entry, handler from `ListConversationsWithAgents(reg, sessionHarness(pool))`; capable conn sees claude + codex, non-capable sees only the Claude row with no `agent` key.

## Open questions

- None blocking. Whether an unknown harness string should be filtered for old clients is moot today (the pool only writes claude/codex); the closed mapping reads it as claude.

## Documentation handoff (pending — documentation stage)

`docs/protocol-mobile.md`:
- § Capability negotiation (v2): name `multi_agent` and say what it gates (the `agent` field and the Codex-row filter on `conversations`; grants no interactive access).
- The `conversations` row of the message-types table: document `agent` (`"claude"` | `"codex"`, the bound session's agent; no bound session or an unheld one reads `"claude"`), and state that a client without the capability is not sent Codex conversations.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] No findings. The only client-controlled input is the advertised capability list in the handshake hello. It crosses into trusted state at one point: `negotiateCapabilities` intersects it with `supportedV2Capabilities`, and `handleNoiseInit` reduces the result to `s.multiAgent` with a value-specific `slices.Contains`, on the token-OK path, before `V2StateOpen`. A spoofed or unsupported string never reaches the negotiated set. Downstream code holds a bool (`V2Session.multiAgent`, `dispatch.Conn.MultiAgent`), never the raw list.
- [Trust boundaries — false advertisement] No findings. `multi_agent` states what the client can render. It does not authorize anything. A client that claims it falsely is still a paired device that passed Noise_IK authentication and the device-token check. That device may already read every Claude conversation's id, label and cwd through the same verb, so a Codex row gives it nothing its pairing does not already grant. The trust unit in `docs/protocol-mobile.md` § Security model is the paired device, not the capability set. The worst outcome of a false claim is that the client is shown a row it cannot drive, which is a problem for that client only.
- [Trust boundaries — filter is presentation, not access control] OUT OF SCOPE. The Codex-row filter keeps an old client from being *shown* a conversation it cannot drive. It does not stop that client from naming a Codex conversation id in another verb. This is acceptable for this slice. No production path creates a Codex conversation today (#2647 adds creation). An old client learns ids only from this list, and from pushed frames, which #2644 gates on the same `Conn.MultiAgent` / `s.multiAgent` decision. Conversation ids are unguessable registry ids. How a verb other than `list_conversations` handles a Codex conversation named by a non-capable client (reject, or serve as today) belongs to the ticket that makes each such verb Codex-aware: #2644 for pushed frames, #2645 for `model_list`, #2647 for creation.
- [Trust boundaries — fail-open direction] No findings. This is the intended direction, per AC 2. A harness miss (no bound session, or a session `Pool.HarnessFor` answers `ErrSessionNotFound` for) reads `claude` in `agentOf`, so an old client is sent the row. A missed session is not running Codex under this daemon, so the row cannot lead the client into a Codex session. The alternative, hiding rows on a miss, would silently drop Claude conversations whose session was evicted from today's list, which breaks AC 3's "exactly today's list". The mapping in `agentOf` is closed: only an exact `codex` harness yields `AgentCodex`, and the raw harness string never reaches the wire.
- [Separation from `interactive`] No findings. `s.multiAgent` is a separate field from `s.interactive`, derived from a different capability string. No `interactive` gate in `V2SessionManager`'s dispatch switch or in `v2session_modal.go` reads it. `TestV2Session_Handshake_CapabilityNegotiation`'s "multi agent alone grants no interactive" case pins this. A re-key cannot change the decision: `handleNoiseInit` routes a `noise_init` on a `V2StateOpen` session to `handleRekeyInit`, which never writes either flag. A `noise_init` in `V2StateHandshakeComplete` closes the conn. `TestV2Session_MultiAgent_ReachesHandlerAndSurvivesRekey` pins that the flag survives.
- [Tokens, secrets, credentials] No findings. The change creates, stores and compares no secrets. The device token check in `handleNoiseInit` is unchanged, and the flag is set after it.
- [File operations] No findings. No file is read or written. `Pool.HarnessFor` reads in-memory registry state, and the persisted `Harness` field was already written by #2629.
- [Subprocess execution] No findings. No process is spawned. The harness read does not start a dormant session.
- [Cryptographic primitives] No findings. Noise_IK and the re-key path are untouched.
- [Network & I/O] No findings. The reply is at most as large as today's plus one short fixed-vocabulary field per row. A non-capable client's reply is never larger than today's.
- [Error messages, logs, telemetry] No findings. No log line or error reply is added. The handler logs nothing about the harness, and a miss is not an error.
- [Concurrency] No findings. `s.multiAgent` has `s.interactive`'s single-writer regime (written on Run before `V2StateOpen`, read on Run in `routeAppFrame`). `Conn.SetMultiAgent` is called before the Route goroutine starts, so the goroutine start is the happens-before edge, and nothing writes the flag after that. `Pool.HarnessFor` takes the pool's RLock, and the handler holds no lock of its own across the call, so no lock-ordering edge is added.
- [Threat model alignment] No findings. The design affects no threat in `docs/protocol-mobile.md` § Security model: authentication, E2E encryption, replay and pairing are untouched. The only new behaviour is a reply shaped by an already-authenticated client's negotiated capability.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-25

## Revisions

- **2026-09-25 — security review added (verifier finding on PR #2648).** The ticket carries `security-sensitive`, and the plan committed in `a087a005` had no `## Security review` section. The section above records the pass. Verdict PASS: no design change and no code change. It answers the verifier's four questions: a false advertisement exposes nothing beyond the device's pairing, the filter is presentation and per-verb handling is owned by #2644, #2645 and #2647, fail-open on a harness miss is intended, and `multi_agent` is independent of `interactive` and survives a re-key.
