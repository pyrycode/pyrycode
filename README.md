# Pyrycode

A process supervisor and runtime for [Claude Code](https://claude.com/claude-code). Run `pyry` instead of `claude` to get a long-lived, self-healing, extensible host for your AI assistant.

**Status:** Phase 0 — early scaffolding. Not ready for general use. See [the project plan](docs/plan.md).

## What it does (Phase 0)

- Spawns `claude` inside a pseudo-terminal and bridges stdin/stdout transparently
- Restarts the child on exit with exponential backoff
- Resumes the most recent Claude Code session after a crash so conversation history survives
- Forwards `SIGWINCH` so terminal resizes propagate to the child
- Exposes a local Unix domain socket so `pyry status` can query the running daemon

## What it will do (later phases)

1. **Phase 1 — Multi-session.** Spawn N Claude Code processes, route inbound events to the right one.
2. **Phase 2 — Channels integration.** Replace the current hook-based Discord/Telegram plumbing.
3. **Phase 3 — In-process services.** Knowledge capture, memsearch, scheduled jobs as first-class components.
4. **Phase 4 — Remote access.** Self-hosted relay, E2E encryption via Noise Protocol, QR-code pairing, mobile and desktop clients.
5. **Phase 5 — Voice chat.** WebRTC peer-to-peer audio, STT/TTS pipeline, realtime conversation.
6. **Phase 6 — Distribution.** Homebrew tap, AUR, Nix flake, Docker images.

## Platform support

Pyrycode targets **Linux** and **macOS**. Windows is out of scope — it would require a separate PTY backend (ConPTY) and different signal handling.

## Install

Pyrycode is not yet published. For development:

```bash
git clone https://github.com/pyrycode/pyrycode
cd pyrycode
go build -o pyry ./cmd/pyry
./pyry version
```

Requires Go 1.23 or later and a working `claude` binary on `PATH`.

Cross-compile:

```bash
GOOS=linux  GOARCH=amd64 go build -o dist/pyry-linux-amd64  ./cmd/pyry
GOOS=darwin GOARCH=arm64 go build -o dist/pyry-darwin-arm64 ./cmd/pyry
GOOS=darwin GOARCH=amd64 go build -o dist/pyry-darwin-amd64 ./cmd/pyry
```

## Usage

`pyry` is a near-drop-in replacement for `claude`. Anything pyry doesn't recognize is forwarded to `claude` verbatim. Pyry's own configuration uses an explicit `-pyry-*` prefix so it never collides with claude's namespace.

```bash
pyry                                # run claude under supervision
pyry "summarize foo.md"             # initial prompt forwarded to claude
pyry --model sonnet -p "..."        # any claude flag passes through
pyry -pyry-verbose                  # debug-level pyry logs
pyry -pyry-verbose -- --resume      # use -- if claude args collide with -pyry-*
pyry version
pyry help
```

### Pyry-specific flags

These configure pyry itself and must come **before** any claude args (or after a `--` separator):

| Flag | Default | Purpose |
|---|---|---|
| `-pyry-claude` | `claude` | Path to the claude binary |
| `-pyry-workdir` | current dir | Working directory for the supervised child |
| `-pyry-resume` | `true` | Pass `--continue` to claude on restart so the session survives crashes |
| `-pyry-verbose` | `false` | Debug-level pyry logging |
| `-pyry-name` | `pyry` (or `$PYRY_NAME`) | Instance name; socket is `~/.pyry/<name>.sock` |
| `-pyry-socket` | (unset) | Explicit socket path; overrides `-pyry-name` |

### Querying a running daemon

While `pyry` is running, query its state from another shell:

```bash
$ pyry status
Phase:         running
Child PID:     29059
Restart count: 0
Started at:    2026-04-28T14:58:36Z
Uptime:        1m23s
```

The default socket is `~/.pyry/pyry.sock`. Permissions are `0600`. To run multiple pyrys side by side, give each one a name:

```bash
pyry &                         # default — ~/.pyry/pyry.sock
pyry status                    # talks to the default
pyry stop                      # stops the default

pyry -pyry-name elli &         # second instance — ~/.pyry/elli.sock
pyry status -pyry-name elli    # talks to elli
```

Or set `PYRY_NAME` in the environment so a whole shell session is implicitly scoped to one instance — this works for the supervisor invocation **and** for every control verb:

```bash
PYRY_NAME=elli pyry &
PYRY_NAME=elli pyry status

# Permanent alias is one line:
alias pyry-elli='PYRY_NAME=elli pyry'
pyry-elli                       # supervisor
pyry-elli status                # control
pyry-elli attach                # interactive bridge
```

Convention follows `tmux -L`, `screen -S`, `pm2 --name` — names are how you reason about multiple daemon instances, no path or cwd bookkeeping required.

### Stopping a running daemon

```bash
pyry stop
```

Sends a shutdown request over the control socket. Pyry kills the supervised claude child, removes the socket, and exits — same code path as SIGINT / SIGTERM / `launchctl unload`.

### Attaching to a daemon (service mode)

When pyry runs as a service (launchd / systemd / no controlling terminal), the supervised claude session has no terminal of its own. Connect to it on demand:

```bash
pyry attach
# → "pyry: attached. Press Ctrl-B d to detach."
# Your terminal is now claude's terminal. Type away.
# Press Ctrl-B then d to detach — pyry and claude stay running.
```

Detach leaves the daemon untouched. Reattach later (different shell, after a laptop sleep, over SSH from your phone) and you're back in the same session. To actually shut the daemon down, use `pyry stop`.

Only one client can attach at a time. A second `pyry attach` while another is connected gets a clean error.

Pyry switches between **foreground mode** (running `pyry` from a terminal — bridges PTY directly to your terminal, today's behavior) and **service mode** (no controlling terminal — output buffered, accessible via `pyry attach`) automatically based on whether stdin is a TTY.

### Run as a service (Linux, systemd)

Use the unit file in [`systemd/pyry.service`](systemd/pyry.service):

```bash
mkdir -p ~/.config/systemd/user
cp systemd/pyry.service ~/.config/systemd/user/
systemctl --user daemon-reload
systemctl --user enable --now pyry
journalctl --user -u pyry -f
```

### Run as a service (macOS, launchd)

Edit [`launchd/dev.pyrycode.pyry.plist`](launchd/dev.pyrycode.pyry.plist) to set your binary path and working directory, then:

```bash
install -d ~/Library/LaunchAgents
cp launchd/dev.pyrycode.pyry.plist ~/Library/LaunchAgents/
launchctl load ~/Library/LaunchAgents/dev.pyrycode.pyry.plist
tail -f /tmp/pyry.out.log /tmp/pyry.err.log
```

To stop and unload:

```bash
launchctl unload ~/Library/LaunchAgents/dev.pyrycode.pyry.plist
```

## Design

See [`docs/plan.md`](docs/plan.md) for the full roadmap, and [`docs/architecture.md`](docs/architecture.md) for the deeper technical background (relay vs P2P, Noise Protocol, comparison with Anthropic Remote Control and Happy).

## License

MIT — see [LICENSE](LICENSE).
