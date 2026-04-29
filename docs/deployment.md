# Deploying pyry as a service

This walkthrough covers running pyry as a long-lived daemon under systemd (Linux) or launchd (macOS). For the conceptual overview see [`guide.md`](guide.md#service-mode-production); this document is the operational checklist.

The steps are nearly identical between the two service managers — the differences are unit-file format and the start/stop commands. Both deploy the same `pyry` binary into the same role: a single per-user daemon listening on a Unix socket at `~/.pyry/pyry.sock`.

## Prerequisites

- Pyry binary built or cross-compiled for the target machine. From the repo: `make linux` produces `dist/pyry-linux-amd64`; `make build` produces `./pyry` for the current machine.
- A working `claude` binary on the target machine, and `~/.claude/` already initialised (run `claude` once interactively before installing pyry as a service).
- Knowledge of which claude flags you want pyry to forward: typically the same ones you'd run claude with manually (`--dangerously-skip-permissions`, `--channels …`, etc.).

## Linux — systemd user unit

### Install the binary

```bash
mkdir -p ~/.local/bin
cp pyry-linux-amd64 ~/.local/bin/pyry
chmod +x ~/.local/bin/pyry
```

Confirm `~/.local/bin` is on your `$PATH` and `pyry version` prints something sensible.

### Install the unit file

The repo ships a template at [`systemd/pyry.service`](../systemd/pyry.service). Copy it to your user-systemd directory:

```bash
mkdir -p ~/.config/systemd/user
cp systemd/pyry.service ~/.config/systemd/user/
```

Edit `~/.config/systemd/user/pyry.service`:

- Confirm `WorkingDirectory=%h/pyry-workspace` matches where you want claude's session storage to live (and any project-specific files like hooks).
- Edit the `ExecStart=` line to add any claude flags you need:

```ini
ExecStart=%h/.local/bin/pyry --dangerously-skip-permissions \
  --channels plugin:discord@claude-plugins-official \
  --channels plugin:telegram@claude-plugins-official
```

- Confirm `Environment="PATH=…"` covers the directories `claude` needs. If you use `nvm`, `pyenv`, or any other shimmed tooling, those paths must be in `PATH` here — service-manager processes don't inherit your interactive shell environment.

### Stop any prior `tmux + bash` setup

If you were previously running claude under tmux with a restart loop:

```bash
# Whatever your old start script was; e.g.
tmux kill-session -t claude
```

Two pyrys (or pyry + a tmux'd claude) running in the same `WorkingDirectory` will fight over `~/.claude/projects/<cwd>/` session files. Make sure only one is active.

### Enable and start

```bash
systemctl --user daemon-reload
systemctl --user enable --now pyry
```

`--now` starts the service immediately in addition to enabling it on boot. Verify:

```bash
systemctl --user status pyry          # should show "active (running)"
pyry status                            # should show Phase: running with a Child PID
```

### Watching the lifecycle

```bash
journalctl --user -u pyry -f            # tail the supervisor's structured logs
pyry logs                               # last 200 supervisor events from in-memory ring
```

For claude's actual output, attach: `pyry attach` (Ctrl-B d to detach).

### Boot persistence

By default user-systemd services start when the user logs in. If you want pyry to start at boot before you log in:

```bash
sudo loginctl enable-linger $USER
```

This keeps the user's systemd instance alive across logout, so `pyry.service` runs whenever the machine is up.

### Updating the binary

```bash
cp dist/pyry-linux-amd64 ~/.local/bin/pyry
systemctl --user restart pyry
```

Pyry's `--continue` resume means the supervised claude reconnects to its previous session after the restart — no conversation history is lost.

### Stopping

```bash
systemctl --user stop pyry              # stops the service (but it'll come back on reboot)
systemctl --user disable pyry           # also unregisters from boot
pyry stop                                # equivalent — same shutdown path via the control socket
```

## macOS — launchd

### Install the binary

```bash
mkdir -p ~/.local/bin
cp pyry ~/.local/bin/
chmod +x ~/.local/bin/pyry
```

(Or wherever your `$PATH` points — `/usr/local/bin` is also fine if you have write access.)

### Install the launchd plist

The repo ships a template at [`launchd/dev.pyrycode.pyry.plist`](../launchd/dev.pyrycode.pyry.plist). Copy it to your launch agents directory:

```bash
install -d ~/Library/LaunchAgents
cp launchd/dev.pyrycode.pyry.plist ~/Library/LaunchAgents/
```

Edit `~/Library/LaunchAgents/dev.pyrycode.pyry.plist`:

- Set `ProgramArguments` to the path of your installed pyry binary plus any claude flags. Each flag is a separate `<string>` element:

```xml
<key>ProgramArguments</key>
<array>
    <string>/Users/YOU/.local/bin/pyry</string>
    <string>--dangerously-skip-permissions</string>
    <string>--channels</string>
    <string>plugin:discord@claude-plugins-official</string>
</array>
```

- Set `WorkingDirectory` to the directory you want claude to run in (typically a project root or a dedicated workspace).
- Confirm `EnvironmentVariables.PATH` covers everywhere `claude` lives.

### Load and start

```bash
launchctl load ~/Library/LaunchAgents/dev.pyrycode.pyry.plist
```

`load` registers the service. With `RunAtLoad=true` in the plist (the default in the template), it starts immediately. Verify:

```bash
launchctl list | grep pyrycode          # should show the service with a PID
pyry status                              # should show Phase: running
```

### Watching the lifecycle

The example plist captures stdout/stderr to `/tmp/pyry.{out,err}.log`. Adjust to your preference:

```bash
tail -f /tmp/pyry.out.log /tmp/pyry.err.log
pyry logs
```

### Boot persistence

LaunchAgents (under `~/Library/LaunchAgents/`) start when you log in. They do **not** start before login. For pre-login persistence on macOS you'd need a LaunchDaemon (under `/Library/LaunchDaemons/`) running as root, which is a different threat model — pyry's per-user `0600` socket and `~/.claude/` config don't fit that profile naturally. The standard recommendation is to leave it as a LaunchAgent and accept that pyry comes up when you log in.

### Updating the binary

```bash
cp pyry ~/.local/bin/
launchctl kickstart -k gui/$UID/dev.pyrycode.pyry
```

`kickstart -k` kills and restarts the service, picking up the new binary. As with systemd, `--continue` preserves the claude session.

### Stopping

```bash
launchctl unload ~/Library/LaunchAgents/dev.pyrycode.pyry.plist
pyry stop                                # equivalent control-socket path
```

`unload` also unregisters the service from auto-start at login. `pyry stop` only stops the current run; the service starts again next login. Use whichever matches your intent.

## Common pitfalls

**Service starts but immediately enters a backoff loop.** Almost always means `claude` is failing to launch. Check `pyry logs` for the exit reason. Common causes:

- `claude` not on the configured `PATH`
- `~/.claude/` not initialised (run `claude` interactively at least once first)
- A flag you passed via `ExecStart` is wrong — claude rejects it and exits

**`pyry status` from a different shell can't find the daemon.** Default socket is `~/.pyry/pyry.sock`. If your service-mode pyry is under a non-default name (`-pyry-name foo`) the socket is `~/.pyry/foo.sock` and `pyry status` needs the same name (or `PYRY_NAME=foo` exported in your shell).

**`pyry attach` shows nothing for several seconds.** Normal — claude is waking up, possibly finishing a slow operation, or in mid-restart backoff. `pyry status` from another shell will tell you which phase the supervisor is in.

**Two pyrys racing for the same socket.** One starts, the other's `Listen` either fails (if the first is genuinely listening) or silently replaces the first's socket file (if the first crashed leaving a stale file). Always `systemctl --user status pyry` / `launchctl list | grep pyrycode` before manually starting another instance — and if you want a second instance deliberately, use `-pyry-name`.

**Channel hooks not firing under pyry.** Pyry doesn't intercept claude's hook execution — hooks fire from inside the claude child. If `~/pyry-workspace/.claude/hooks/*.sh` worked under tmux+bash but not under pyry, the most likely cause is the systemd / launchd `PATH` not including the directories your hook scripts call out to (`gh`, `curl`, etc.). Add them to `Environment=` / `EnvironmentVariables`.

**Pyry logs say "spawning claude" but `pyry attach` produces nothing.** You're attaching to the right pyry, but claude has buffered output and isn't sending it until something changes. Type something — anything from your end of the attach — and claude will respond. This is normal terminal-buffering behavior, not a pyry bug.

## See also

- [`guide.md`](guide.md) — full user guide
- [`architecture.md`](architecture.md) — design overview
- [`protocol.md`](protocol.md) — control-socket wire reference
