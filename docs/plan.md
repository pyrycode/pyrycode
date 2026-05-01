# Pyrycode Plan

This is a repo-local copy of the project plan. The authoritative working doc lives in the Obsidian vault at `📋 Projects/2026-04-10 - Pyrycode/Pyrycode.md`.

## Phase 0 — Minimum viable supervisor

The smallest thing that can replace `tmux` + the bash restart loop and host Pyry in production. **Done; pyrybox is on systemd pyry as of 2026-05-01.**

- [x] Project scaffold: go.mod, directory layout, LICENSE, README, .gitignore
- [x] PTY spawn of `claude` via `creack/pty`
- [x] Transparent stdin/stdout bridging with raw mode on the controlling terminal
- [x] SIGWINCH forwarding so terminal resizes propagate to the child
- [x] Crash detection + exponential backoff restart
- [x] Session continuity across crashes: subsequent runs after the first pass `--continue` to claude, resuming the most recent session for the cwd
- [x] Structured logging via `log/slog`
- [x] Graceful shutdown on SIGINT / SIGTERM
- [x] systemd user unit template
- [x] launchd plist for macOS (cross-platform: Linux + macOS targeted; Windows out of scope)
- [x] Cross-compile verified for darwin/amd64 and darwin/arm64
- [x] Unix control socket — `pyry status`, `pyry stop`, `pyry logs`, `pyry attach` all live
- [x] Tests (unit + integration) for supervisor, bridge, and control plane
- [x] CLI transparency — pyry forwards unknown args to claude verbatim; pyry's own flags use `-pyry-*` prefix
- [x] Named instances — `~/.pyry/<name>.sock` socket layout, `-pyry-name` flag, `PYRY_NAME` env var
- [x] **Phase 0.5 — pyrybox migration:** `pyry` replaces the tmux+bash setup for Pyry itself (2026-05-01)
- [x] **Public release tooling:** goreleaser, install.sh, Homebrew tap (2026-05-01, v0.5.0)
- [x] **`pyry install-service` subcommand:** writes systemd / launchd unit files with auto-PATH inheritance (v0.5.1, v0.5.2)
- [x] **Concurrent-pyry detection:** second `pyry` on the same socket fails with `ErrInstanceRunning` instead of silently hijacking (v0.5.3)
- [ ] Backoff-loop cooldown: if crashes happen N times in T seconds, bail out (deferred — current loop retries forever, which is the right behaviour for the always-on service)

## Phase 1 — Multi-session pool

Lift the supervisor from one-claude to N-claudes, addressed by session UUID. **Design locked, implementation pending — see [`multi-session.md`](multi-session.md) for full design notes.**

| Sub-phase | Scope | Tag |
|---|---|---|
| **1.0** | Pool refactor — internal restructure. Single-session externally; Pool always has exactly one entry. | `v0.6.0` |
| **1.1** | `pyry sessions new/list/rm/rename` + `pyry attach <id>`. Multi-session works from a terminal. | `v0.7.0` |
| **1.2** | `sessions.json` persistence + idle eviction + lazy respawn on next message. | `v0.8.0` |

Locked decisions from the 2026-05-01 design pass:

- **Session ID = UUID** (slug labels later as opt-in aliases)
- **Per-pyry namespace** (registry at `~/.pyry/<pyry-name>/sessions.json`; not discoverable across pyrys)
- **Each session = `claude --session-id <uuid>`** in the same workspace; project memory and hooks shared, session JSONLs distinct
- **Idle eviction:** session JSONL on disk is the identity; running claude is a transient executor. Evicted sessions cost ~zero RAM and respawn lazily.
- **Don't pre-build a generic Router interface** — Phase 2 has one consumer (channels), keep it concrete. Abstraction emerges when the mobile router arrives.

## Phase 2 — Channels integration (pyrycode-owned)

Pyrycode owns the Discord and Telegram clients directly. The current `--channels plugin:discord@…` plugin is structurally one-claude-only (one bot token can't be shared across N processes), so multi-session over Discord requires pyrycode taking over the bot connection.

| Sub-phase | Scope | Tag |
|---|---|---|
| **2.0** | pyrycode-owned Discord client (one bot, gateway connection in pyry) + first-message lazy bind: message in unbound channel → spawn session, register channel→UUID, persist | `v0.9.0` |
| **2.1** | pyrycode-owned Telegram client (same shape, parallel impl) | `v0.10.0` |

Mapping: **Discord channel = session** (one channel binds to one session UUID). Same for Telegram chats. Lazy bind on first message — zero config to start a new conversation in a new channel.

Side benefit: closes the Pyry-main-session MCP-failure issue tracked in `OPEN-QUESTIONS.md` (the official plugins are intermittently flaky; owning the integration end-to-end removes that class of bug).

## Phase 3 — Mobile API

Parallel transport pointing at the same Pool. Stacks on top of Phase 1; doesn't depend on Phase 2. Targets the future mobile client (provisional: KitchenClaw or Pyrycode-native Android).

- WebSocket or HTTP transport
- Per-request session ID header (or a thread-id mapped to session UUID)
- Auth via per-user tokens (transport-level concern, not pyry's responsibility)
- One pyry, N sessions, mobile UI lets user create / rename / archive

| Sub-phase | Scope | Tag |
|---|---|---|
| **3.0** | Protocol design + minimal HTTP/WS server in pyry | `v1.0.0` |
| **3.x** | Per-channel UX features (notifications, attachments, typing indicators) | post-`v1.0.0` |

## Phase 4 — Cross-cutting services in-process

- **Knowledge capture** — observes the session stream directly, runs on boundaries or on a schedule. Replaces `crons/knowledge-capture/run.sh`.
- **memsearch** — exposed as a tool or command channel.
- **Scheduled jobs** — supervised cron runner replaces the bash `crons/` scripts.

## Phase 5 — Remote access

- Small self-hosted relay on pyrybox (fork Happy's server or reimplement in Go)
- E2E crypto — start with TLS + shared secret, upgrade to Noise Protocol (`Noise_IK`) for public deployment
- QR code pairing
- WebSocket transport
- Mobile and desktop clients (TBD — Expo, Tauri, PWA)

## Phase 6 — Voice chat

- WebRTC peer-to-peer audio via `pion/webrtc`, signaled via the relay
- STT pipeline feeding Claude Code stdin
- TTS reading Claude Code stdout aloud
- Integration with KitchenClaw or a Pyrycode-native Android client

## Phase 7 — Distribution polish

Most of the original Phase 6 scope shipped during Phase 0.5 (2026-05-01): Homebrew tap, install.sh, goreleaser releases on tag. Remaining items:

- [ ] AUR package
- [ ] Nix flake
- [ ] Docker image for the relay server (when relay exists)
- [x] Homebrew tap (v0.5.0)
- [x] One-line install script (v0.5.0, hardened in v0.5.2)
- [x] Cross-platform binary releases (Linux/macOS × amd64/arm64)
