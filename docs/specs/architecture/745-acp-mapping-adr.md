# Spec #745 — ADR: internal-turn-event ↔ ACP mapping (sibling to ADR 025)

**Ticket:** [#745](https://github.com/pyrycode/pyrycode/issues/745) — `docs(adr): ACP mapping ADR sibling to ADR 025`
**Size:** XS (doc-only — one new Markdown file; no `.go`, no tests).
**Not security-sensitive** — no implementation surface, no trust boundary, no wire/crypto change. Nothing to review.

## What the developer delivers

Exactly one new file: **`docs/knowledge/decisions/027-acp-mapping.md`** (see § ADR number). It ports the canonical internal-turn-event → ACP mapping, the six divergences, the ACP taxonomy reference, the hard cost invariant, and the open items into an in-repo ADR in ADR 025's house style.

**Do NOT touch any other file.** In particular do **not** edit `docs/knowledge/INDEX.md` — the documentation phase is its sole writer and will index ADR 027 after this PR merges. (The branch-overlap scan at spec time found #58 / #747 / #765 all editing `INDEX.md`; keeping our developer out of it avoids a merge collision.) The developer's worktree mutates only the new ADR file (this spec is already committed by the architect). No `qmd update`/`embed` in the developer run — that is a post-merge maintenance step, not an AC.

## ADR number — 027, not 026 (binding correction)

The ticket body and its AC say `026-acp-mapping.md`, with the explicit hedge *"next free number after 025 — confirmed free at refinement; re-confirm at author time."* **Re-confirmed at spec time: 026 is now taken.** `docs/knowledge/decisions/026-embedded-acp-pool-exact-one-claude.md` was created by ticket #761 (Accepted 2026-07-03, the same epic #600). The next free number is **027**.

The developer MUST:
- Name the file `docs/knowledge/decisions/027-acp-mapping.md`.
- Title it `# ADR 027 — <title>`.
- Note the number change in the PR description so no reviewer expects `026`.

This is not a judgment call — `ls docs/knowledge/decisions/` shows 026 present and 026 is the highest existing number; re-run that `ls` before writing to confirm 027 is still free.

**Cross-link that makes 027 load-bearing:** ADR 026 already *cites* "divergence 6" and "divergence-6 violation" by number as if a canonical numbered list exists. It does not yet exist in-repo — it lives only in the vault. ADR 027 IS that list's new home. So 027 must define the canonical six (numbered exactly as below) so 026's references resolve, and 027's Related section links 026 as a downstream consumer.

## Files to read first

- `docs/knowledge/decisions/025-mobile-remote-head-interactive-session.md` — **the house-style template.** Copy its section skeleton: `Status` (accepted/phased split), `Context` (with sub-headings), `Decision` (numbered list), `Alternatives considered`, `Consequences`, `Related`. Mirror its Related-section style, including the substrate-seal entry (`Substrate seal: cmd/substrate-guard; tui-driver v1.0.1 sealed surface`).
- `docs/knowledge/decisions/026-embedded-acp-pool-exact-one-claude.md` — the sibling ADR that already states the **hard cost invariant** (interactive claude, never `claude -p`, never the metered Agent SDK) and **divergence 6** (one ACP session ↔ one interactive claude). 027 must be consistent with it and link it. Note its heading style (`# ADR 026: <title>`, a `**Status:**`/`**Phase:**`/`**Refines:**` block) — 027 may follow either 025's or 026's exact heading form; prefer 025's since the ticket names 025 as the template, but stay internally consistent.
- `internal/turnevent/event.go` — the built outbound `Event` sum type. Read the **package doc comment (lines 1–19)**: it already states the adapter framing ("~90% like ACP … owned by us … churn in the external ACP spec stays inside the ACP adapter") — lift that framing into the ADR's Decision/rationale. Also the `TurnEnd` doc (63–70 → divergence 1) and `Stall` doc (72–78 → ACP-dropped).
- `internal/turnevent/permission.go` — `PermissionRequest` (outbound `Event`; its doc at lines 3–11 states ACP `session/request_permission` maps onto its shape → divergence 2), plus the `Inbound` sum type: `PermissionResponse` and `Cancel` (Cancel doc at 60–71 → the neutral form of ACP `session/cancel`).
- `internal/turnevent/taxonomy.go` — the four ACP-valued enums: `ToolKind` (9 values), `ToolStatus` (4), `TurnEndReason` (5 stopReasons), `PermissionOptionKind` (4). These ARE the ACP strings; cite the const names.
- `internal/turnevent/content.go` — the `ToolContent` shapes: `TextContent`, `DiffContent`, `TerminalContent`.
- `internal/protocol/codes.go:118,124,225` + `internal/protocol/messaging.go:197` + `internal/protocol/snapshot.go:36-46` — proof that `QueueState` / `ScreenSnapshot` exist **only as mobile-wire payloads** (`protocol.QueueStatePayload`/`TypeQueueState`, `protocol.ScreenSnapshotPayload`/`TypeScreenSnapshot`), NOT as `turnevent.Event` types. This grounds the "planned/other-surface, not built as a neutral event" column.
- **Vault source (already extracted into this spec below — the developer does not need vault access):** `Structured-Event Bridge — internal model and ACP mapping` and `Drop-In Contract`. This spec quotes everything needed. Keep the vault docs as design history; ADR 027 is the implementation contract from here on.

## The porting contract

The ADR is a *port*: the mapping table, the six divergences, and the ACP taxonomy must match the vault source (quoted below) **and** cite real `internal/turnevent` names where the event is already built. The one honesty requirement the ticket is strict about: **present the full target mapping — it is the design contract T2–T10 build to — but flag built-vs-planned per row so the ADR never reads as if a not-yet-built type already exists.**

### Section skeleton to produce

1. `## Status` — **Accepted** for the mapping; sibling to ADR 025; doc-only, no code ships.
2. `## Context` — why this ADR now (mapping lived only in the vault; epic #600 decomposes into T2–T10, each of which must build to one authoritative table). State the **hard cost invariant** here verbatim-in-spirit: `pyry acp` MUST drive a real interactive `claude` (Max-subscription-billed), MUST NOT use `claude -p` or the metered Agent SDK — inherited from ADR 025 / restated in ADR 026.
3. `## Decision` — ACP is a **thin adapter over the neutral daemon-owned turn-event model**, NOT the daemon's native event type. Rationale (from `event.go` package doc + Drop-In Contract): ACP is a spec Zed evolves on its own cadence; keeping it in an adapter contains external spec churn and keeps the mobile-only concerns (busy spinner, queued backlog, stall, screen snapshot) out of the ACP surface — the same containment the tui-driver substrate seal applies to claude's screen.
4. `## The mapping` — the outbound + inbound tables (below), with the built/planned status column.
5. `## Divergences` — the six, each with its explanation (below).
6. `## ACP taxonomy reference` — the lists (below), so the ADR is self-contained.
7. `## Open items` — recorded as decisions the epic must make, **not blockers** (below).
8. `## Alternatives considered` — at minimum: "make ACP the daemon's native event type" (rejected: couples the core to an externally-evolving spec; ACP has no shape for the mobile-only concerns). Mirror 025's list style.
9. `## Consequences` — e.g. T2–T10 cite this ADR not the vault; ACP-spec churn is contained to the adapter; the mobile wire and ACP stay independent adapters over one model.
10. `## Related` — links (below), mirroring 025's Related style.

> **Style guard (do not over-produce):** write ADR prose, not code. The tables and taxonomy lists below are the *contract* and may be reproduced near-verbatim (exact names matter). The divergence explanations below are given as a thesis + source; expand each into a short ADR paragraph faithful to the vault wording — do not merely paste this spec. No Go code blocks. Target a doc comparable in density to ADR 025, not longer.

### The mapping — outbound (daemon → ACP client)

Reproduce as a table. Column order suggestion: *Neutral event · ACP · Repo status*. The **Repo status** column is the honesty requirement — verified against live code at spec time.

| Neutral event | ACP mapping | Repo status (verified) |
|---|---|---|
| `TextChunk` (incremental assistant text; fields `MessageID, Text`) | `session/update` → `agent_message_chunk` | **Built** — `turnevent.TextChunk` (`event.go`) |
| `ThoughtChunk` (thinking text; `MessageID, Text`) | `session/update` → `agent_thought_chunk` | **Built** — `turnevent.ThoughtChunk` |
| `ToolStart` (`ToolCallID, Title, Kind, RawInput, Locations`) | `session/update` → `tool_call` | **Built** — `turnevent.ToolStart` |
| `ToolUpdate` (`ToolCallID, Status, Content`) | `session/update` → `tool_call_update` | **Built** — `turnevent.ToolUpdate` |
| `TurnEnd` (`Reason`) | the `stopReason` **return** of `session/prompt` (see divergence 1) | **Built** — `turnevent.TurnEnd` |
| `PermissionRequest` (`RequestID, ToolCallID, Title, Options`) | `session/request_permission` (blocking agent→client call, see divergence 2) | **Built** — `turnevent.PermissionRequest` |
| resolution of a `PermissionRequest` | the **response** to `session/request_permission` | **Built** — resolved via `turnevent.PermissionResponse` (inbound) |
| `Stall` (internal-only; quiet-but-not-idle onset) | **dropped** by ACP; surfaced on **stderr** | **Built** — `turnevent.Stall` (internal-only Event) |
| `BusyState` (internal-only; thinking spinner) | **dropped** (see divergence 3) | **Planned / deferred** — no `turnevent` type yet (`event.go:16` "out of scope … a home in a later ticket") |
| `QueueState` (internal-only; queued backlog) | **dropped** (see divergence 4) | **Other surface** — mobile-wire only (`protocol.QueueStatePayload`/`TypeQueueState`); not a `turnevent.Event` |
| `ScreenSnapshot` (internal-only; rendered screen text) | **dropped** | **Other surface** — mobile-wire only (`protocol.ScreenSnapshotPayload`/`TypeScreenSnapshot`); not a `turnevent.Event` |

### The mapping — inbound (ACP client → daemon)

| Neutral command | ACP mapping | Repo status (verified) |
|---|---|---|
| `Prompt` (send a message; content blocks) | `session/prompt` (client→agent) | **Planned / deferred** — not yet a `turnevent.Inbound` type |
| `PermissionResponse` — select (`RequestID, OptionID`) | the `session/request_permission` response, `outcome: "selected"` + `optionId` | **Built** — `turnevent.PermissionResponse` |
| `PermissionResponse` — cancel (`Cancelled: true`) | the response with `outcome: "cancelled"` | **Built** — same type, `Cancelled` field |
| `Cancel` (interrupt the running turn) | `session/cancel` **notification** | **Built** — `turnevent.Cancel` (inbound) |
| `DropQueued` (drop a queued message) | **no ACP method** | **Planned / deferred** — not yet a `turnevent.Inbound` type |

**Field-name authority:** where the code and the vault differ, the **code** shape wins for the ADR. Notably `PermissionRequest` in code is `RequestID, ToolCallID, Title, Options` (a by-id `ToolCallID` reference + `Title`), not the vault's embedded `toolCall`. Cite `permission.go:12-17`, `event.go:47-70`, `content.go`, `taxonomy.go` for the authoritative field/enum names.

### The six divergences (each: thesis → expand into a paragraph faithful to the vault)

Number them exactly 1–6 (ADR 026 cites "divergence 6" — the numbering is load-bearing):

1. **End of turn is a return value in ACP, an event in the neutral stream.** The one real structural mismatch. ACP runs a turn as one `session/prompt` request that streams `session/update` notifications and then *returns* a `stopReason`. The neutral model is a pure event stream with a `TurnEnd` event. The ACP adapter **holds the in-flight `session/prompt` call open** and resolves it with the `stopReason` when `TurnEnd` arrives. (Corroborate with `event.go:63-70` — the code already states this is the adapter's job, not the model's.)
2. **Permission is a blocking agent→client request in ACP, a request+awaited-response internally.** ACP makes the agent call the client and block. Internally it is a `PermissionRequest` correlated to a `PermissionResponse` by `RequestID`. The ACP adapter satisfies the internal `PermissionRequest` with the blocking `session/request_permission` call. (Corroborate with `permission.go:3-11`.)
3. **The thinking spinner (`BusyState`) has no ACP home.** ACP hosts show their own spinner while a prompt call is in flight, so the adapter drops `BusyState`. **Reliability split (state it):** `ThoughtChunk` comes from claude's JSONL log and is robust; `BusyState` is screen-scraped and brittle — which is why the safe-degrade net targets the spinner, not the text.
4. **The queued backlog is mobile-only.** ACP gives turn pacing to the host, so there is no queue concept; `QueueState` and `DropQueued` are dropped by the ACP adapter.
5. **pyry does not use the host's filesystem or terminal.** claude runs on the daemon's own machine with its own disk and terminal, so `pyry acp` does the work locally, never calls `fs/*` or `terminal/*`, and declares **minimal client-capability requirements** in `initialize`. The only agent→client call it needs is `session/request_permission`.
6. **Session boundaries differ.** ACP creates/resumes sessions with `session/new` and `session/load`; the daemon maps these onto **one running interactive claude session per ACP session** (the invariant ADR 026 enforces and cites as "divergence 6").

### ACP taxonomy reference (port verbatim; self-contained)

Source: `agentclientprotocol.com` (JSON-RPC 2.0, line-delimited over stdio), captured in the vault 2026-06-07. Include all of:

- **`session/update` variants** (the `sessionUpdate` discriminant): `user_message_chunk`, `agent_message_chunk`, `agent_thought_chunk`, `tool_call`, `tool_call_update`, `plan`, `available_commands_update`, `current_mode_update`, `usage_update`, `config_option_update`.
- **Tool kinds** (== `turnevent.ToolKind`): read, edit, delete, move, search, execute, think, fetch, other.
- **Tool statuses** (== `turnevent.ToolStatus`): pending, in_progress, completed, failed.
- **Tool content shapes** (== `turnevent` content.go): regular content blocks (text / image / resource), diffs (`path, oldText, newText`), terminals (a live `terminalId`).
- **Client→agent methods:** `initialize`, `authenticate`, `session/new`, `session/load`, `session/prompt`, `session/cancel` (notification), `session/set_mode`, `session/set_config_option`.
- **`stopReason` values** (== `turnevent.TurnEndReason`): `end_turn`, `max_tokens`, `max_turn_requests`, `refusal`, `cancelled`.
- **Agent→client methods:** `session/request_permission`, `fs/read_text_file`, `fs/write_text_file`, `terminal/create`, `terminal/output`, `terminal/release`, `terminal/wait_for_exit`, `terminal/kill`.
- **Permission-option kinds** (== `turnevent.PermissionOptionKind`): allow_once, allow_always, reject_once, reject_always. The client responds `outcome: "selected"` + `optionId`, or `outcome: "cancelled"`.

### Open items (record as epic decisions, NOT blockers)

1. Whether `pyry acp` exposes `session/set_mode` (plan vs edit) or **pins one mode**. Mobile does not surface mode switching today.
2. How the three ACP `session/update` variants with **no internal source** — `plan`, `available_commands_update`, `usage_update` — are handled **per variant**: add an internal source, let the adapter synthesize from data the daemon already has, or omit.
3. **Do not silently drop the vault's third Phase-2 open item:** whether claude streams its thinking into the JSONL log under the current version — i.e. whether `ThoughtChunk` is robust or degrades to the screen-scraped `BusyState` only. The divergence-3 reliability note already touches this; either **cross-reference it there** or **consciously scope it to the Phase-2 bridge (#596)**. State the choice explicitly; do not drop it.

### Related (mirror ADR 025's Related style)

- **ADR 025** — [`025-mobile-remote-head-interactive-session.md`](025-mobile-remote-head-interactive-session.md) — this ADR's sibling; the interactive-drive and cost decisions it inherits.
- **ADR 026** — [`026-embedded-acp-pool-exact-one-claude.md`](026-embedded-acp-pool-exact-one-claude.md) — downstream consumer; enforces divergence 6 and the hard cost invariant. (026 cites "divergence 6"; 027 is that list's home.)
- **Substrate seal** — `cmd/substrate-guard`; tui-driver v1.0.1 sealed surface (mirror 025's entry).
- **Epic** — [#600](https://github.com/pyrycode/pyrycode/issues/600) `pyry acp`; the neutral model lives in `internal/turnevent`.
- **Design history (vault):** `Structured-Event Bridge — internal model and ACP mapping`; `Drop-In Contract`. Superseded as the implementation contract by this ADR.

## Acceptance criteria (restate for the developer)

- [ ] New file `docs/knowledge/decisions/027-acp-mapping.md` (**027**, not 026 — see § ADR number; re-confirm free with `ls` before writing), following ADR 025's house-style sections plus `## The mapping` and `## Divergences`, linking ADR 025 (sibling), ADR 026 (consumer), and the substrate seal.
- [ ] Full outbound + inbound mapping tables, faithful to the vault model, citing real `internal/turnevent` type names where built and flagging built / planned-deferred / other-surface per row (never implying a not-yet-built event is a `turnevent.Event`).
- [ ] All six divergences documented with their explanations, numbered 1–6, faithful to the vault.
- [ ] The hard cost invariant stated (interactive `claude`, never `claude -p` / Agent SDK); the ACP taxonomy reference present and self-contained; the open items recorded as epic decisions (including the third, ThoughtChunk-robustness one — cross-referenced or consciously scoped, not dropped).
- [ ] No code changes; `INDEX.md` untouched (documentation phase owns it). `make check` stays green trivially (only a Markdown file added).

## Open questions

- **Heading form (025 vs 026 style):** both siblings coexist with slightly different Status blocks. Spec recommends 025's form since the ticket names 025 as the template; the developer may match 026's `**Status:**`/`**Refines:**` block instead if it reads cleaner beside its immediate neighbour. Either is acceptable — internal consistency is the only requirement.
- **How much of the ACP taxonomy to inline vs. link:** the spec ports it fully inline for self-containment (the ticket asks for a self-contained ADR). If the developer judges a subsection redundant with the mapping table, condensing is fine as long as every listed item (session/update variants, tool kinds/statuses/content, client→agent + agent→client methods, stopReasons, permission-option kinds) still appears somewhere in the ADR.
