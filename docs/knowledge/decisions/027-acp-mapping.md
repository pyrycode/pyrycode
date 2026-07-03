# ADR 027 — internal turn-event ↔ ACP mapping

## Status

**Accepted** for the mapping, sibling to [ADR 025](025-mobile-remote-head-interactive-session.md). This ADR is doc-only — no code ships with it. It ports the canonical internal-turn-event → ACP mapping table, the six divergences, and the ACP taxonomy reference out of the personal design vault and into the repo, so every child of epic [#600](https://github.com/pyrycode/pyrycode/issues/600) (`pyry acp`, decomposing into T2–T10) builds to one authoritative table versioned next to the code that implements it.

Where this ADR and the vault design docs disagree in future, **this ADR wins for implementers.** The vault docs (`Structured-Event Bridge — internal model and ACP mapping`, `Drop-In Contract`) are retained as design history.

## Context

### Why this ADR now

The internal-event → ACP mapping and its divergences lived only in the personal vault. Epic #600 makes `pyry acp` a real adapter — a JSON-RPC stdio transport ([#756](https://github.com/pyrycode/pyrycode/issues/756)) that drives interactive claude sessions and translates their neutral turn events into the Agent Client Protocol (an open, Zed-stewarded standard). Every ticket from T2 through T10 must implement one slice of that translation. Without an in-repo contract, each implementer would re-derive the mapping from vault prose the developer run cannot open. [ADR 026](026-embedded-acp-pool-exact-one-claude.md) already lands and already cites "divergence 6" by number, as if a canonical numbered list exists in-repo. It does not — until this ADR. **ADR 027 is that list's home**, so 026's references resolve.

### The hard cost invariant (inherited from ADR 025, restated by ADR 026)

`pyry acp` MUST drive a **real interactive `claude` session** — the kind billed under the interactive Max subscription. It MUST NOT use the non-interactive `claude -p` (headless `stream-json`) path, and MUST NOT use the metered Agent SDK path. This is the load-bearing cost decision: the headless path is expected to flip from subscription-covered to pay-per-token metering around June 2026, and an always-on personal session on a metered path is not economical. Every ACP adapter concern below sits on top of that one running interactive claude per ACP session.

### Where the neutral model already lives

The neutral turn-event model is `internal/turnevent` (built for Phase 2 structured streaming, epic #596). Its package doc (`internal/turnevent/event.go:1-19`) already frames the adapter relationship this ADR records: the model is shaped *~90% like ACP* but is *owned by us*, "so churn in the external ACP spec stays inside the ACP adapter and never reaches the daemon core or the mobile wire — same containment logic the tui-driver substrate seal applies to claude's screen." The mobile wire and `pyry acp` are two thin adapters over this one model. Not every conceptual event named in the vault is built yet; the mapping tables below flag built-vs-planned per row so this ADR never reads as if a not-yet-built type already exists.

## Decision

ACP is a **thin adapter over the neutral, daemon-owned turn-event model** — NOT the daemon's native event type. The daemon core emits `turnevent.Event` values; the ACP adapter type-switches over them and produces ACP `session/update` notifications, the `session/prompt` return, and `session/request_permission` calls. Inbound ACP methods are translated the other way, into `turnevent.Inbound` commands.

**Rationale.** ACP is a spec Zed evolves on its own cadence. Keeping it in an adapter contains that external churn: a change to the ACP wire shape is absorbed in the adapter and never reaches the daemon core or the mobile wire. It also keeps the mobile-only concerns — the thinking spinner, the queued backlog, the stall signal, the screen snapshot — out of the ACP surface entirely, because ACP has no home for them (see Divergences 3–5). This is the same containment discipline the tui-driver substrate seal (`cmd/substrate-guard`) applies to claude's screen: one owner, one contained blast radius for external change.

## The mapping

Column order: *Neutral event · ACP mapping · Repo status*. The **Repo status** column is the honesty requirement: `turnevent` type names are cited where the event is already built; rows for concepts not yet built as neutral events are flagged **Planned / deferred** or **Other surface**. Field/enum names are the **code** shape (`permission.go`, `event.go`, `taxonomy.go`, `content.go`) where code and vault differ — the code wins.

### Outbound — daemon → ACP client

| Neutral event | ACP mapping | Repo status (verified) |
|---|---|---|
| `TextChunk` — incremental assistant text (`MessageID, Text`) | `session/update` → `agent_message_chunk` | **Built** — `turnevent.TextChunk` (`event.go:30-34`) |
| `ThoughtChunk` — thinking text (`MessageID, Text`) | `session/update` → `agent_thought_chunk` | **Built** — `turnevent.ThoughtChunk` (`event.go:36-40`) |
| `ToolStart` — tool invocation (`ToolCallID, Title, Kind, RawInput, Locations`) | `session/update` → `tool_call` | **Built** — `turnevent.ToolStart` (`event.go:47-53`) |
| `ToolUpdate` — changed tool-call fields (`ToolCallID, Status, Content`) | `session/update` → `tool_call_update` | **Built** — `turnevent.ToolUpdate` (`event.go:57-61`) |
| `TurnEnd` — end of turn (`Reason`) | the `stopReason` **return** of `session/prompt` (see divergence 1) | **Built** — `turnevent.TurnEnd` (`event.go:63-70`) |
| `PermissionRequest` — permission modal (`RequestID, ToolCallID, Title, Options`) | `session/request_permission` — blocking agent→client call (see divergence 2) | **Built** — `turnevent.PermissionRequest` (`permission.go:12-17`) |
| resolution of a `PermissionRequest` | the **response** to `session/request_permission` | **Built** — resolved via `turnevent.PermissionResponse` (inbound, `permission.go:54-58`) |
| `Stall` — internal-only quiet-but-not-idle onset | **dropped** by ACP; surfaced on **stderr** | **Built** — `turnevent.Stall` (internal-only Event, `event.go:72-78`) |
| `BusyState` — internal-only thinking spinner | **dropped** (see divergence 3) | **Planned / deferred** — no `turnevent` type yet (`event.go:16` "out of scope … a home in a later ticket") |
| `QueueState` — internal-only queued backlog | **dropped** (see divergence 4) | **Other surface** — mobile-wire only (`protocol.QueueStatePayload` / `TypeQueueState`); not a `turnevent.Event` |
| `ScreenSnapshot` — internal-only rendered screen text | **dropped** | **Other surface** — mobile-wire only (`protocol.ScreenSnapshotPayload` / `TypeScreenSnapshot`); not a `turnevent.Event` |

### Inbound — ACP client → daemon

| Neutral command | ACP mapping | Repo status (verified) |
|---|---|---|
| `Prompt` — send a message (content blocks) | `session/prompt` (client→agent) | **Planned / deferred** — not yet a `turnevent.Inbound` type |
| `PermissionResponse` — select (`RequestID, OptionID`) | `session/request_permission` response, `outcome: "selected"` + `optionId` | **Built** — `turnevent.PermissionResponse` (`permission.go:54-58`) |
| `PermissionResponse` — cancel (`Cancelled: true`) | the same response with `outcome: "cancelled"` | **Built** — same type, `Cancelled` field |
| `Cancel` — interrupt the running turn | `session/cancel` **notification** | **Built** — `turnevent.Cancel` (inbound, `permission.go:60-71`) |
| `DropQueued` — drop a queued message | **no ACP method** | **Planned / deferred** — not yet a `turnevent.Inbound` type |

**Field-name authority.** Where code and vault differ, the ADR follows the code. In particular `PermissionRequest` in code is `RequestID, ToolCallID, Title, Options` — a by-id `ToolCallID` reference plus `Title` — not the vault's embedded `toolCall` object; the consumer already saw the matching `ToolStart` in the stream (`permission.go:8-11`). The four taxonomy enums (`taxonomy.go`) are string-backed so their values *are* the ACP strings.

## Divergences

The numbering is load-bearing — ADR 026 cites "divergence 6" by number.

1. **End of turn is a return value in ACP, an event in the neutral stream.** This is the one real structural mismatch. ACP runs a turn as a single `session/prompt` request that streams `session/update` notifications and then *returns* a `stopReason`. The neutral model is a pure event stream with a `TurnEnd` event carrying only the reason. The ACP adapter **holds the in-flight `session/prompt` call open** and resolves it with the mapped `stopReason` when `TurnEnd` arrives. The neutral type already records this as the adapter's job, not the model's (`event.go:63-70`: "converting TurnEnd back into that RPC return is the ACP adapter's job, not this model's").

2. **Permission is a blocking agent→client request in ACP, a request-plus-awaited-response internally.** ACP makes the agent *call* the client and block until it answers. Internally, permission is modeled as a `PermissionRequest` correlated to a `PermissionResponse` by `RequestID`. The ACP adapter satisfies the internal `PermissionRequest` by making the blocking `session/request_permission` call and feeding the client's answer back as the correlated `PermissionResponse`. The neutral type already states ACP `session/request_permission` maps onto its shape (`permission.go:3-11`), so it is adapter-neutral, not mobile-specific.

3. **The thinking spinner (`BusyState`) has no ACP home.** ACP hosts show their own spinner while a `session/prompt` call is in flight, so the adapter drops `BusyState`. **Reliability split (stated deliberately):** `ThoughtChunk` comes from claude's JSONL log and is robust, while `BusyState` is screen-scraped and brittle (it depends on spinner-glyph anchors claude can change on a UI update). This is why the safe-degrade net targets the spinner, not the streamed text: losing `BusyState` costs an explicit spinner, not the reasoning content. (Whether claude reliably streams thinking into the JSONL log is tracked as open item 3.)

4. **The queued backlog is mobile-only.** ACP gives turn pacing to the host, so there is no queue concept in the protocol at all. `QueueState` and the inbound `DropQueued` are both dropped by the ACP adapter.

5. **pyry does not use the host's filesystem or terminal.** claude runs on the daemon's own machine, with its own disk and its own terminal, so `pyry acp` does the work locally. It never calls the ACP `fs/*` or `terminal/*` agent→client methods, and it declares **minimal client-capability requirements** in `initialize`. The only agent→client call it needs is `session/request_permission`.

6. **Session boundaries differ.** ACP creates and resumes sessions with `session/new` and `session/load`. The daemon maps these onto **one running interactive claude session per ACP session** — the invariant [ADR 026](026-embedded-acp-pool-exact-one-claude.md) enforces (`session/new` allocates exactly one supervised interactive claude, "no more, no fewer") and cites as "divergence 6".

## ACP taxonomy reference

Ported for self-containment. Source: `agentclientprotocol.com` (JSON-RPC 2.0, line-delimited over stdio), captured in the vault 2026-06-07. The four enums map directly onto `internal/turnevent` types (`taxonomy.go`).

- **`session/update` variants** (the `sessionUpdate` discriminant): `user_message_chunk`, `agent_message_chunk`, `agent_thought_chunk`, `tool_call`, `tool_call_update`, `plan`, `available_commands_update`, `current_mode_update`, `usage_update`, `config_option_update`.
- **Tool kinds** (== `turnevent.ToolKind`): `read`, `edit`, `delete`, `move`, `search`, `execute`, `think`, `fetch`, `other`.
- **Tool statuses** (== `turnevent.ToolStatus`): `pending`, `in_progress`, `completed`, `failed`.
- **Tool content shapes** (== `turnevent` `content.go`): regular content blocks (text / image / resource) → `TextContent`; diffs (`path, oldText, newText`) → `DiffContent`; terminals (a live `terminalId`) → `TerminalContent`.
- **Client→agent methods:** `initialize`, `authenticate`, `session/new`, `session/load`, `session/prompt`, `session/cancel` (notification), `session/set_mode`, `session/set_config_option`.
- **`stopReason` values** (== `turnevent.TurnEndReason`): `end_turn`, `max_tokens`, `max_turn_requests`, `refusal`, `cancelled`.
- **Agent→client methods:** `session/request_permission`, `fs/read_text_file`, `fs/write_text_file`, `terminal/create`, `terminal/output`, `terminal/release`, `terminal/wait_for_exit`, `terminal/kill`. Of these, `pyry acp` uses only `session/request_permission` (divergence 5).
- **Permission-option kinds** (== `turnevent.PermissionOptionKind`): `allow_once`, `allow_always`, `reject_once`, `reject_always`. The client responds `outcome: "selected"` + `optionId`, or `outcome: "cancelled"`.

## Open items

Recorded as decisions the epic must make, **not blockers**.

1. **Mode exposure.** Whether `pyry acp` exposes `session/set_mode` (plan vs edit) or **pins one mode**. Mobile does not surface mode switching today.
2. **Sourceless `session/update` variants.** How the three ACP `session/update` variants with no internal source — `plan`, `available_commands_update`, `usage_update` — are handled **per variant**: add an internal source, let the adapter synthesize from data the daemon already holds, or omit. Decide per variant.
3. **`ThoughtChunk` robustness.** Whether claude reliably streams its thinking into the JSONL log under the current version — i.e. whether `ThoughtChunk` is robust or degrades to the screen-scraped `BusyState` only. This is the reliability question divergence 3 raises. **Consciously scoped** to the Phase-2 bridge epic ([#596](https://github.com/pyrycode/pyrycode/issues/596)), where the JSONL-vs-screen sourcing is verified against live claude; not dropped.

## Alternatives considered

### A. Make ACP the daemon's native event type

Emit ACP `session/update` shapes directly from the daemon core, no neutral model in between. Rejected: it couples the core to a spec Zed evolves externally, so every ACP change would ripple into the daemon and into the mobile wire. It also has no shape for the mobile-only concerns (`BusyState`, `QueueState`, `Stall`, `ScreenSnapshot`), which would then need a parallel side-channel anyway. The neutral model owns the vocabulary; ACP is one adapter over it.

### B. A second neutral model just for ACP

A separate ACP-shaped internal model distinct from the mobile one. Rejected: two models over one claude session doubles the surface that must stay in sync with tui-driver's `Events()`, for no gain — the mobile and ACP surfaces already agree on ~90% of the shape (`event.go:1-19`), and the divergences are handled by dropping or holding events in the adapter, not by a different core vocabulary.

## Consequences

- **T2–T10 implementers cite this ADR, not the vault.** The mapping table, the six numbered divergences, and the taxonomy reference are the in-repo implementation contract from here on. The vault docs are design history.
- **ACP-spec churn is contained to the adapter.** A change in the external ACP wire shape is absorbed where the type-switch lives and never reaches the daemon core or the mobile wire.
- **The mobile wire and `pyry acp` stay independent adapters over one model.** Neither surface constrains the other; each drops or synthesizes the concerns the other needs.
- **`turnevent` gaps are explicit.** `BusyState`, `Prompt`, and `DropQueued` are named in this contract but not yet built; the built/planned column keeps the ADR honest about what an implementer will and will not find in `internal/turnevent` today.
- **ADR 026's "divergence 6" reference now resolves in-repo.** 026 links back to this ADR as the home of the canonical numbered list.

## Related

- **ADR 025** — [`025-mobile-remote-head-interactive-session.md`](025-mobile-remote-head-interactive-session.md) — this ADR's sibling; the interactive-drive and cost decisions it inherits.
- **ADR 026** — [`026-embedded-acp-pool-exact-one-claude.md`](026-embedded-acp-pool-exact-one-claude.md) — downstream consumer; enforces divergence 6 and the hard cost invariant, and cites "divergence 6" whose canonical list is this ADR.
- **Substrate seal** — `cmd/substrate-guard`; tui-driver v1.0.1 sealed surface.
- **Epic** — [#600](https://github.com/pyrycode/pyrycode/issues/600) `pyry acp`; the neutral model lives in `internal/turnevent`.
- **Design history (vault):** `Structured-Event Bridge — internal model and ACP mapping`; `Drop-In Contract`. Superseded as the implementation contract by this ADR.
