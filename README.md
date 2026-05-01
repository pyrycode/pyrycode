# Pyrycode

A process supervisor and runtime for [Claude Code](https://claude.com/claude-code). Run `pyry` instead of `claude` to get a long-lived, self-healing host for your AI assistant — with a control socket for status, logs, graceful shutdown, and detach/reattach from any shell.

## Status

**Phase 0 complete and dogfooded.** Foreground mode is a drop-in `claude` wrapper with auto-restart. Service mode runs `pyry` under launchd or systemd and exposes a Unix-socket control plane. As of `v0.5.2` pyry is daily-driver-grade on Linux: pyrybox now runs claude under systemd via the public install path described below, replacing the prior `tmux` + bash restart-loop setup.

Production hardening — multi-session routing, Channels integration, in-process knowledge capture, remote access, voice — is on the roadmap (see [`docs/plan.md`](docs/plan.md)).

## What it does

- **Supervises `claude`** in a pseudo-terminal with crash recovery and exponential backoff.
- **Resumes the previous session** on every restart so conversation history survives crashes.
- **Two modes from one binary.** Run from a TTY for interactive development; run under a service manager (no TTY) for production. The same binary auto-detects and adapts.
- **Control plane on a Unix socket.** Query state, stream logs, request shutdown, attach a terminal — all from any shell, with single-user filesystem permissions as the security boundary.
- **CLI transparency.** Anything pyry doesn't recognise is forwarded to `claude` verbatim. Pyry's own flags use a `-pyry-*` prefix so they never collide.

## Platforms

Linux and macOS, including Apple Silicon (arm64) — prebuilt `darwin_arm64` binaries ship with every release. Windows is out of scope (different PTY backend, different signal model).

## Install

**Universal one-liner** (Linux / macOS, amd64 / arm64):

```bash
curl -fsSL https://raw.githubusercontent.com/pyrycode/pyrycode/main/install.sh | bash
```

Drops `pyry` in `~/.local/bin/`. Set `PYRY_VERSION=v0.5.2` to pin a release; set `PYRY_INSTALL_DIR=/usr/local/bin` (run with `sudo bash`) for a system-wide install.

**Homebrew** (macOS, Linuxbrew):

```bash
brew install pyrycode/tap/pyry
```

**Go-native** (any platform Go supports):

```bash
go install github.com/pyrycode/pyrycode/cmd/pyry@latest
```

**From source:**

```bash
git clone https://github.com/pyrycode/pyrycode
cd pyrycode
make build           # ./pyry — current platform
make linux           # cross-compile dist/pyry-linux-amd64
make dist            # adds darwin × {amd64, arm64}
```

Requires a working `claude` binary on `PATH` to actually do anything useful. Building from source needs Go 1.26 or later.

## Quickstart

**Foreground (development).** Run `pyry` instead of `claude`:

```bash
pyry "summarize foo.md"           # forwarded as claude's initial prompt
pyry --model sonnet -p "..."      # any claude flag passes through
```

If `claude` exits, pyry restarts it with `--continue` so you keep your session.

**Service mode (production).** Two commands. `pyry install-service` writes a systemd unit (Linux) or launchd plist (macOS), inheriting your shell's `$PATH` so nvm / pyenv / brew tools come along automatically:

```bash
pyry install-service -- \
  --dangerously-skip-permissions \
  --channels plugin:discord@claude-plugins-official

systemctl --user daemon-reload
systemctl --user enable --now pyry
```

(macOS: `launchctl load ~/Library/LaunchAgents/dev.pyrycode.pyry.plist` in place of the systemctl lines.)

The supervised `claude` has no terminal of its own; connect to it on demand:

```bash
pyry attach    # your terminal becomes claude's terminal
               # press Ctrl-B d to detach — pyry stays running
```

`pyry status`, `pyry logs`, and `pyry stop` work from any shell, talking to the daemon over its Unix socket at `~/.pyry/pyry.sock`.

For the full walkthrough — multi-instance, troubleshooting, hooks under service-mode `PATH`, boot persistence — see [**`docs/guide.md`**](docs/guide.md) and [**`docs/deployment.md`**](docs/deployment.md).

## Documentation

- [**`docs/guide.md`**](docs/guide.md) — user guide and walkthrough (start here)
- [`docs/architecture.md`](docs/architecture.md) — design overview (PTY bridging, control plane, lifecycle)
- [`docs/deployment.md`](docs/deployment.md) — service-mode setup under systemd and launchd
- [`docs/protocol.md`](docs/protocol.md) — control-socket wire format reference
- [`docs/plan.md`](docs/plan.md) — phase roadmap

## Development

```bash
make check           # vet + race-enabled tests + staticcheck
make build           # ./pyry
make linux           # cross-compile for Linux
```

[`CODING-STYLE.md`](CODING-STYLE.md) covers Go conventions used in the repo.

## License

MIT — see [LICENSE](LICENSE).
