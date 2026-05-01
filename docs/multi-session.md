# Multi-session design

Phase 1 of pyrycode replaces the single-session supervisor with a session pool: one pyry process supervising N claude children, each addressed by a session UUID. This document records the design decisions, the architecture, and the phasing.

The motivating use case is the future mobile / Discord experience where a single user (or single bot) holds multiple parallel conversations against the same workspace — different chats, shared hooks, shared MCP, shared project memory, distinct session histories.

## Why a pool

Today's `pyry` is hard-coded to one claude child via `internal/supervisor/Supervisor`. The supervisor owns a single PTY, a single backoff timer, a single `--continue`-resumed session. This is exactly right for "replace tmux" (Phase 0); it's wrong for "host N parallel chats."

The structural change for Phase 1 is to lift that single supervisor into a pool. Externally, pyry still answers on `~/.pyry/<name>.sock`. Internally, requests are routed to one of N supervisors keyed by session UUID.

## Architecture

```
                          ┌──────────────────────────────────┐
                          │           pyry process           │
                          │                                  │
   external transport ───▶│   Router (channel→session,       │
   (Discord, Telegram,    │           mobile→session)        │
    mobile API,           │             │                    │
    `pyry attach <id>`)   │             ▼                    │
                          │   ┌──────────────────────────┐   │
                          │   │ Pool                     │   │
                          │   │   map[UUID]*Supervisor   │   │
                          │   │   sessions.json (state)  │   │
                          │   │   idle-evict / respawn   │   │
                          │   └────────────┬─────────────┘   │
                          │                │                 │
                          │     ┌──────────┼──────────┐      │
                          │     ▼          ▼          ▼      │
                          │  Supervisor  Supervisor  Sup..   │
                          │  └─claude    └─claude    └─claude│
                          │   --session    --session   --se..│
                          │   -id ...      -id ...     ..    │
                          └──────────────────────────────────┘

                       All children share:
                         - WorkingDirectory (one workspace)
                         - .claude/hooks, settings, MCP config
                         - project memory at
                           ~/.claude/projects/<encoded-cwd>/
                       Each child has its own:
                         - <uuid>.jsonl  (in the encoded-cwd dir,
                                          no `sessions/` subdir)
                         - PTY, stdin/stdout
                         - lifecycle state (running, evicted, ...)
```

The single supervisor today becomes a `Pool` containing supervisors. Each supervisor invokes `claude --session-id <uuid>` instead of `--continue`. Sessions persist as files on disk; the running claude process is just a transient executor that reads / writes the session JSONL.

This last insight matters: **a session's identity lives in the JSONL on disk, not in the running process.** Idle eviction (kill claude, keep JSONL) feels seamless because the next message respawns claude pointing at the same JSONL — claude resumes the conversation. Evicted sessions cost ~zero RAM. Active sessions cost one claude process.

## Locked decisions

These are decided as of 2026-05-01 and don't need revisiting unless the design surfaces a problem:

### Session ID format: UUID

`crypto/rand`-generated UUIDs (36-character canonical form). Opaque, no naming conflicts, trivially unique. Slug labels (`tools-discussion`, `vault-help`) can land later as opt-in human-friendly aliases that map to UUIDs; the UUID stays the canonical identity.

### Namespace: per-pyry-name

The session registry lives at `~/.pyry/<pyry-name>/sessions.json`. Two pyrys with different `-pyry-name` (e.g. `pyry` and `elli`) have fully isolated registries. Same pattern as the existing `~/.pyry/<name>.sock` socket layout.

Sessions are *not* discoverable across pyrys. If you need cross-pyry visibility, you're outside pyrycode's scope — that's something a higher layer (relay, mobile API) builds on top.

### Channels mapping: Discord channel = session

One Discord channel binds to one session UUID. The channels router is a `map[discord_channel_id]session_uuid` lookup, persisted in `sessions.json`.

**First-message lazy bind:** when a message arrives in an unbound channel, the Pool spawns a fresh session, the registry binds `channel_id → new_uuid`, and the message is routed to the new claude. Zero config — sessions appear automatically as users start using channels. The same UX shape will apply to mobile threads when that lands.

The same model holds for Telegram (bind by chat ID).

### Mobile is parallel work, not blocking

Phase 1 multi-session is built and shipped without taking the mobile API into account. Mobile becomes another router pointing at the same Pool, added as a parallel implementation when its protocol is decided. The Router shape is left concrete (channels-only) for now; abstraction emerges when the second consumer arrives, not before.

## Phasing

| Sub-phase | Scope | Tag | User-visible |
|---|---|---|---|
| **1.0** | Pool refactor — internal restructure. Single-session externally; Pool always has exactly one entry. | `v0.6.0` | None — pure refactor |
| **1.1** | `pyry sessions new/list/rm/rename` + `pyry attach <id>`. Multi-session works from a terminal. | `v0.7.0` | CLI multi-session |
| **1.2** | `sessions.json` persistence + idle eviction + lazy respawn | `v0.8.0` | Sessions survive pyry restart |
| **2.0** | pyrycode-owned Discord client + first-message lazy bind | `v0.9.0` | One bot, N channels = N sessions |
| **2.1** | pyrycode-owned Telegram client (parallel impl) | `v0.10.0` | Same UX over Telegram |
| **3.0** | Mobile API protocol — separate transport pointing at the Pool | `v1.0.0` | Mobile multi-chat |

Phase 1.0–1.2 builds the infrastructure. Phase 2.0–2.1 is what makes it useful for the existing channels-driven UX. Phase 3.0 stacks parallel.

### Why the channels work moves out of Phase 1

Phase 1's "channels router" needs the Discord client to exist before it can do anything. The current `claude --channels plugin:discord@…` setup is structurally one-claude-only — one Discord bot token can't be shared across N processes. For multi-session over Discord, pyrycode has to own the Discord layer (bwmarrin/discordgo or similar): one bot, one gateway connection, route by channel ID.

That's substantively Phase 2 work in the original plan. Re-sequencing puts it where it belongs: Phase 1 is the Pool, Phase 2 is the transports that address sessions in the Pool.

## What replaces what (Phase 2 specifics)

Today's pyrybox runs:
```
claude --dangerously-skip-permissions \
  --channels plugin:discord@claude-plugins-official \
  --channels plugin:telegram@claude-plugins-official
```

After Phase 2.0–2.1, that becomes:
```
pyry  # (pyry's own Discord/Telegram clients spawn from inside)
```

with pyry-side configuration of bot tokens and channel filters. Claude no longer talks to Discord / Telegram directly; pyry does, and routes per session.

This also fixes the "Pyry main session MCP failures (2026-04-22)" issue tracked in OPEN-QUESTIONS — the official Discord/Telegram plugins have intermittent reliability problems that the curl+webhook crons currently work around. Owning the integration end-to-end removes that class of bug.

## Open design questions

Things to settle before the relevant sub-phase starts coding:

- **Pool eviction policy.** LRU when over a memory budget, or fixed idle-timeout, or both? (Phase 1.2)
- **Concurrent active cap.** Hard limit on running claudes? Per-pyry config? (Phase 1.2)
- **Plan files (`.claude/plans/`).** Issue [anthropics/claude-code#27311](https://github.com/anthropics/claude-code/issues/27311) — claude shares plan files across same-CWD sessions. Decide: live with the conflict (chat sessions probably don't use plans heavily), patch claude, or pyry-side coordination layer? (Phase 1.0 design check, Phase 1.2 if mitigation is needed)
- **Discord bot permissions model.** Allowlist channels via config, or "any channel the bot is invited to"? (Phase 2.0)
- **Outbound message ergonomics.** Pyry's claude emits stdout; the Discord client wraps each "claude turn" into one Discord message. Long replies might need chunking. How? (Phase 2.0)
- **Telegram-vs-Discord parity.** Both want first-message lazy bind, but Telegram has bot-vs-userbot distinctions; user-bots aren't really first-class. Worth confirming Telegram's bot API is enough for the routing model. (Phase 2.1)
- **Resource attribution.** If pyry owns Discord and a user's flood of messages triggers excessive claude spawning, who throttles? Discord's rate limit, pyry's session cap, or claude's per-process budget? (Phase 2.0)

These are real design problems. None block Phase 1.0–1.1. A few block Phase 1.2 (eviction). The Discord ones block Phase 2.0.

## Out of scope (explicitly)

- **Cross-machine session migration.** A session lives on one pyry; that pyry owns its JSONL. No "follow the user across hosts."
- **Multi-tenant pyry.** One pyry serves one OS user. Multi-tenant deployments (containers, shared hosts) need a different architecture. Not Phase 1, not Phase 2.
- **Real-time collaboration.** Two humans driving one session simultaneously isn't a goal. Sessions are 1:1 conversations even when accessed from multiple devices (the second device sees the first's input/output, but they're not collaborative).
- **Session forking.** Branching a session into two from a common point — interesting but not now. Sessions are linear conversation logs. If forking matters, build it on top of the JSONL after the fact.

## Implementation notes

Some scattered items worth pinning before implementation starts:

- Pool refactor (1.0) is a low-risk pure-internal refactor: external behaviour is unchanged, just the data structures lift to multi-capable. CI must remain green throughout. No new tests for "multi" in 1.0 — those land with 1.1.
- Per-session attach exclusivity should match today's single-attach exclusion. One terminal per session at a time; second `pyry attach <id>` while a first is active gets a clean error. Concurrent attaches across *different* sessions are fine (different PTYs).
- `sessions.json` should be human-readable JSON, atomic-rename writes (write-temp-then-rename), idempotent on reload.
- The Discord client (Phase 2.0) is a long-lived goroutine inside pyry. Reconnection logic, backoff on disconnect, log-on-restart pattern matches the existing supervisor logic — possibly extractable into a small reusable backoff helper.
- Don't generalise `Router` until there are two implementations. Phase 2.0 has a single concrete `ChannelsRouter`; abstraction lands when the mobile router shows up.
