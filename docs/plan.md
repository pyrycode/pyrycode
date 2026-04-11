# Pyrycode Plan

This is a repo-local copy of the project plan. The authoritative working doc lives in the Obsidian vault at `📋 Projects/2026-04-10 - Pyrycode/Pyrycode.md`.

## Phase 0 — Minimum viable supervisor

The smallest thing that can replace `tmux` + the bash restart loop and host Pyry in production.

- [x] Project scaffold: go.mod, directory layout, LICENSE, README, .gitignore
- [x] PTY spawn of `claude` via `creack/pty`
- [x] Transparent stdin/stdout bridging with raw mode on the controlling terminal
- [x] SIGWINCH forwarding so terminal resizes propagate to the child
- [x] Crash detection + exponential backoff restart
- [x] `--resume` flag: subsequent runs after the first resume the most recent session
- [x] Structured logging via `log/slog`
- [x] Graceful shutdown on SIGINT / SIGTERM
- [x] systemd user unit template
- [x] launchd plist for macOS (cross-platform: Linux + macOS targeted; Windows out of scope)
- [x] Cross-compile verified for darwin/amd64 and darwin/arm64
- [ ] Real test on pyrybox: `pyry` replaces the tmux setup for Pyry itself
- [ ] Unix control socket (`pyry status`, `pyry logs`, `pyry attach`)
- [ ] Session ID tracking with explicit persistence across restarts (beyond `--resume` heuristics)
- [ ] Backoff-loop cooldown: if crashes happen N times in T seconds, bail out
- [ ] Tests (unit + a small integration test with a fake child)

## Phase 1 — Multi-session

- Spawn N Claude Code processes, each with its own working directory, session ID, and tag
- Session registry keyed by logical name: `default`, `project:elli`, `ephemeral:task-xyz`
- Routing API — "deliver this event to session X"
- Default session + on-demand project sessions + ephemeral sessions
- Session lifecycle: create, attach, detach, terminate, auto-expire

## Phase 2 — Channels integration

- Replace the current hook-based Discord/Telegram plumbing (see `pyry-workspace/.claude/hooks/*`)
- Inbound channel messages become events routed to the appropriate session
- Outbound replies flow through the daemon
- Supports Channel access policy natively (allowlists, pairing)

## Phase 3 — Cross-cutting services in-process

- **Knowledge capture** — observes the session stream directly, runs on boundaries or on a schedule. Replaces `crons/knowledge-capture/run.sh`.
- **memsearch** — exposed as a tool or command channel.
- **Scheduled jobs** — supervised cron runner replaces the bash `crons/` scripts.

## Phase 4 — Remote access

- Small self-hosted relay on pyrybox (fork Happy's server or reimplement in Go)
- E2E crypto — start with TLS + shared secret, upgrade to Noise Protocol (`Noise_IK`) for public deployment
- QR code pairing
- WebSocket transport
- Mobile and desktop clients (TBD — Expo, Tauri, PWA)

## Phase 5 — Voice chat

- WebRTC peer-to-peer audio via `pion/webrtc`, signaled via the relay
- STT pipeline feeding Claude Code stdin
- TTS reading Claude Code stdout aloud
- Integration with KitchenClaw or a Pyrycode-native Android client

## Phase 6 — Distribution

- Homebrew tap, AUR package, Nix flake
- Docker image for the relay server
- One-line install script
