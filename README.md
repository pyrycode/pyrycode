# Pyrycode

A process supervisor and runtime for [Claude Code](https://claude.com/claude-code). Run `pyry` instead of `claude` to get a long-lived, self-healing host for your AI assistant — with a control socket for status, logs, graceful shutdown, and detach/reattach from any shell.

## Status

**Phase 0 complete and exercised.** Foreground mode is a drop-in `claude` wrapper with auto-restart. Service mode runs `pyry` under launchd or systemd and exposes a Unix-socket control plane. The next milestone (Phase 0.5) is using pyry as the daily-driver supervisor on a real Linux box, replacing the prior `tmux` + bash restart-loop setup.

Production hardening — multi-session routing, Channels integration, in-process knowledge capture, remote access, voice — is on the roadmap (see [`docs/plan.md`](docs/plan.md)).

## What it does

- **Supervises `claude`** in a pseudo-terminal with crash recovery and exponential backoff.
- **Resumes the previous session** on every restart so conversation history survives crashes.
- **Two modes from one binary.** Run from a TTY for interactive development; run under a service manager (no TTY) for production. The same binary auto-detects and adapts.
- **Control plane on a Unix socket.** Query state, stream logs, request shutdown, attach a terminal — all from any shell, with single-user filesystem permissions as the security boundary.
- **CLI transparency.** Anything pyry doesn't recognise is forwarded to `claude` verbatim. Pyry's own flags use a `-pyry-*` prefix so they never collide.

## Platforms

Linux and macOS. Windows is out of scope (different PTY backend, different signal model).

## Install

Pyrycode is not yet published as a binary release. Build from source:

```bash
git clone https://github.com/pyrycode/pyrycode
cd pyrycode
make build           # ./pyry
./pyry version
```

Requires Go 1.23 or later and a working `claude` binary on `PATH`.

Cross-compile for a remote machine:

```bash
make linux           # dist/pyry-linux-amd64
make dist            # adds darwin/arm64 and darwin/amd64
```

## Quickstart

**Foreground (development).** Run `pyry` instead of `claude`:

```bash
pyry "summarize foo.md"           # forwarded as claude's initial prompt
pyry --model sonnet -p "..."      # any claude flag passes through
```

If `claude` exits, pyry restarts it with `--continue` so you keep your session.

**Service mode (production).** Run `pyry` under launchd or systemd. The supervised `claude` has no terminal of its own; you connect to it on demand:

```bash
pyry attach    # your terminal becomes claude's terminal
               # press Ctrl-B d to detach — pyry stays running
```

`pyry status`, `pyry logs`, and `pyry stop` work from any shell.

For the full walkthrough — including multi-instance, deployment under systemd / launchd, and troubleshooting — see [**`docs/guide.md`**](docs/guide.md).

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
