# Pyrycode user guide

This guide walks through using `pyry` from first install to running it as a long-lived service. If you just want to try it locally and see what it does, the **Foreground mode** section is enough; everything after that adds production deployment, multi-instance, and operations detail.

## Contents

- [Mental model](#mental-model)
- [Installing](#installing)
- [Foreground mode](#foreground-mode-development)
- [Service mode](#service-mode-production)
- [Control verbs](#control-verbs)
- [Multiple instances](#multiple-instances)
- [CLI transparency](#cli-transparency)
- [Common workflows](#common-workflows)
- [Troubleshooting](#troubleshooting)
- [FAQ](#faq)

## Mental model

Pyry is a thin process supervisor for Claude Code. It does three things:

1. **Spawns `claude` in a pseudo-terminal (PTY)** so claude renders its TUI normally — colors, prompts, line editing.
2. **Restarts the child** whenever it exits, with exponential backoff. Restarts pass `--continue` so claude resumes the most recent session for the working directory.
3. **Exposes a control socket** so other shells can ask the daemon questions (`status`, `logs`), shut it down (`stop`), or take over the terminal (`attach`).

The same `pyry` binary runs in two modes, auto-detected from whether stdin is a controlling terminal:

| Mode | Trigger | What happens |
|---|---|---|
| **Foreground** | You ran `pyry` from a real terminal | PTY is bridged directly to your stdin/stdout. Same UX as running `claude`, plus auto-restart. |
| **Service** | Pyry was started without a TTY (launchd, systemd, `nohup`, `< /dev/null`, …) | PTY master lives in the supervisor with no local bridge. Output is discarded until a client attaches. `pyry attach` from a separate shell takes over interactively. |

Foreground is for development and experimentation. Service is for the real deployment — pyry running as a daemon you connect to from any shell, surviving laptop sleep, SSH disconnects, and accidental `/exit`.

## Installing

Build from source:

```bash
git clone https://github.com/pyrycode/pyrycode
cd pyrycode
make build           # produces ./pyry
./pyry version
```

Requirements:

- Go 1.23 or later
- A `claude` binary on `PATH` (or supplied via `-pyry-claude`)
- Linux or macOS (Windows is out of scope)

Install the binary somewhere stable:

```bash
mkdir -p ~/.local/bin
cp pyry ~/.local/bin/
# Make sure ~/.local/bin is on $PATH.
```

For a remote target, cross-compile:

```bash
make linux                                          # dist/pyry-linux-amd64
GOOS=linux  GOARCH=arm64 go build -o /tmp/pyry-arm ./cmd/pyry
scp dist/pyry-linux-amd64 server:~/.local/bin/pyry
```

## Foreground mode (development)

This is the simplest way to try pyry. Run it from a terminal exactly the way you'd run `claude`:

```bash
pyry                              # interactive claude session, supervised
pyry "summarize foo.md"           # initial prompt, claude takes over interactively
pyry --model sonnet -p "..."      # one-shot non-interactive (claude --print mode)
```

Anything pyry doesn't recognise — flags, positional args, the `--print` short form — passes through to `claude` unchanged. **There is no need to learn a new CLI.**

When `claude` exits (crash, `/exit`, you typed Ctrl-C and it propagated), pyry restarts it with `--continue` so the conversation history survives. Backoff is 500 ms, doubling on each restart up to 30 s, resetting once the child has stayed up for 60 s.

To stop pyry from foreground mode: open a second shell and run `pyry stop`, or press Ctrl-C until pyry's signal handler fires (the foreground PTY-bridge eats your first few Ctrl-Cs as raw bytes for claude). `Ctrl-C` is *not* the friendly exit — `pyry stop` is.

### Pyry-specific flags

These configure pyry itself and must come **before** any claude args (or after a `--` separator). Use `pyry help` for the up-to-date list.

| Flag | Default | Purpose |
|---|---|---|
| `-pyry-claude <path>` | `claude` | Path to the claude binary |
| `-pyry-workdir <dir>` | current dir | Working directory for the supervised child |
| `-pyry-resume` | `true` | Pass `--continue` to claude on restart so the session survives crashes |
| `-pyry-verbose` | `false` | Debug-level pyry logging on stderr |
| `-pyry-name <name>` | `pyry` (or `$PYRY_NAME`) | Instance name; socket is `~/.pyry/<name>.sock` |
| `-pyry-socket <path>` | (unset) | Explicit socket path; overrides `-pyry-name` |

If a claude flag happens to start with `-pyry-`, separate the two with `--`:

```bash
pyry -pyry-verbose -- --pyry-resume   # claude gets --pyry-resume verbatim
```

In practice this never bites because `claude` doesn't have any `-pyry-*` flags.

## Service mode (production)

Service mode is the load-bearing deployment: pyry running as a long-lived daemon, supervised claude detached from any specific terminal, accessible from any shell. This is how you'd run pyry on a server, on a Linux home box, or under launchd on a Mac.

The mode toggle is automatic — when pyry starts without a controlling terminal it switches to service mode and exposes the `pyry attach` verb. See [`deployment.md`](deployment.md) for systemd and launchd setup walkthroughs.

### Attaching

Once pyry is running as a service, your day-to-day access pattern is:

```bash
pyry attach       # your terminal becomes claude's terminal
                  # interact normally
                  # press Ctrl-B then d to detach
                  # pyry and claude keep running
```

The escape sequence is `Ctrl-B` then `d` — the same convention as `tmux`. Anything else after `Ctrl-B` (including a typo like `Ctrl-B s`) is forwarded normally; only the literal `Ctrl-B d` triggers detach. False positives are unlikely because `Ctrl-B` is rarely typed in normal claude interaction.

Detach **does not** stop pyry. To actually shut the daemon down, use `pyry stop` or `systemctl --user stop pyry` / `launchctl unload`.

Only one client can attach at a time. A second `pyry attach` while another is connected gets a clean `attach: bridge already has an attached client` error.

### Window size

Pyry sends your terminal's columns and rows in the attach handshake. The current Phase 0 implementation accepts these values but does not yet propagate them to the PTY — claude renders at whatever default size the supervisor's PTY was allocated with. Live SIGWINCH propagation while attached is on the roadmap.

In practice: if claude's rendering looks wrong after attach (wrapped lines, wrong column count), detach and reattach to refresh. This will go away when the geometry plumbing lands.

## Control verbs

All four verbs accept the same socket-selection flags (`-pyry-name`, `-pyry-socket`) and the `PYRY_NAME` environment variable. They share the `parseClientFlags` resolver, so what works for one works for all.

### `pyry status`

Prints a snapshot of the daemon's state.

```
$ pyry status
Phase:         running
Child PID:     29059
Restart count: 0
Last uptime:   1m23s
Started at:    2026-04-29T07:18:36Z
Uptime:        1m23s
```

Phases:

- `starting` — supervisor is up, no child has spawned yet (very brief)
- `running` — a child is alive (`Child PID` is set)
- `backoff` — child exited, supervisor is waiting before respawning (`Next backoff` shows the delay)
- `stopped` — supervisor has returned (you'll usually see this only as the last log line, not via `status`)

Use `status` to confirm pyry is up, watch the restart count drift to spot crash loops, or grab the child PID for `kill -0 $PID` style checks.

### `pyry logs`

Prints the last 200 lifecycle log lines from the supervisor's in-memory ring buffer:

```
$ pyry logs
time=2026-04-29T07:17:13.241+03:00 level=INFO msg="pyrycode starting" version=dev name=pyry claude=claude socket=/Users/me/.pyry/pyry.sock
time=2026-04-29T07:17:13.242+03:00 level=INFO msg="spawning claude" args=[] workdir=""
time=2026-04-29T07:17:18.401+03:00 level=WARN msg="claude exited" err="exit status 1" uptime=5.158s
time=2026-04-29T07:17:18.402+03:00 level=INFO msg="restarting after backoff" delay=500ms
time=2026-04-29T07:17:18.903+03:00 level=INFO msg="spawning claude" args=[--continue] workdir=""
```

The buffer covers supervisor-level events (spawns, exits, restarts, attach/detach, shutdown) — not claude's own output. Under launchd or systemd, claude's stdout is captured by the service manager (`/tmp/pyry.out.log` for the example launchd plist; `journalctl --user -u pyry` for systemd).

### `pyry stop`

Asks the daemon to shut down gracefully:

```
$ pyry stop
pyry: stop requested
```

Internally: the server acks `OK`, then triggers the same shutdown path as SIGINT/SIGTERM. The supervised child gets SIGKILL via `exec.CommandContext`, the backoff sleep is interrupted, the listener closes, the socket file is removed, and pyry exits with code 0.

Under a service manager, `pyry stop` does the same job as `systemctl --user stop pyry` or `launchctl unload`. Either is fine.

### `pyry attach`

Covered above under [Service mode](#service-mode-production). Three rules of thumb:

- Press `Ctrl-B d` to detach, not `Ctrl-C`.
- Detach leaves the daemon running. Use `pyry stop` to actually stop.
- Only one attacher at a time.

## Multiple instances

Sometimes you want more than one pyry running. Common reasons:

- A separate session per project (claude session storage is keyed to working directory).
- A second claude identity on the same machine.
- A test instance alongside the real one.

Pyry models this the way `tmux -L` and `screen -S` do: each instance has a name, and the name maps to a socket path under `~/.pyry/`.

```bash
pyry &                          # default — ~/.pyry/pyry.sock
pyry status                     # talks to the default

pyry -pyry-name elli &          # second instance — ~/.pyry/elli.sock
pyry status -pyry-name elli     # talks to elli

pyry status                     # still talks to the default — flag wins per-invocation
```

For shells that work primarily with one named instance, set the environment variable once and let every command pick it up:

```bash
export PYRY_NAME=elli
pyry &                          # supervises elli
pyry status                     # queries elli
pyry attach                     # attaches to elli
```

Or alias it for convenience:

```bash
alias pyry-elli='PYRY_NAME=elli pyry'
pyry-elli &                     # supervises elli
pyry-elli attach                # attaches to elli
```

For unusual setups (Docker mounts, shared sockets, paths outside `$HOME`), `-pyry-socket /any/path.sock` overrides the name-derived default entirely.

## CLI transparency

Pyry is designed to be invisible. Anything it doesn't recognise as one of its own flags or verbs is forwarded to `claude` verbatim:

```bash
pyry "summarize foo.md"           # → claude "summarize foo.md"
pyry --model sonnet -p "hello"    # → claude --model sonnet -p "hello"
pyry -pyry-verbose --resume       # pyry-verbose to pyry, --resume to claude
```

The split happens by walking the argument list left to right:

1. `--` is an explicit separator: everything before goes to pyry, everything after to claude.
2. Args matching a known `-pyry-*` flag (with or without a value, with or without `=`) are pyry's. Boolean flags consume only themselves; value flags also consume the next arg.
3. The first arg that isn't a recognised pyry flag tips the rest of the list into claude territory.

Rule 3 is the same convention used by `sudo`, `time`, and `xargs`: pyry flags must come before claude args, or after a `--`. The reserved verb names (`status`, `stop`, `logs`, `attach`, `version`, `help`) are checked separately as the first argument and only if no other claude args follow.

## Common workflows

### Replacing a foreground claude session

You're used to running `claude` in your terminal. Just run `pyry` instead. Everything else stays the same.

### Running pyry as a background service

See [`deployment.md`](deployment.md). Short version: install the binary, drop the systemd unit or launchd plist into the right place, edit `ExecStart` to add any claude flags you need, enable the service. Use `pyry attach` to talk to it.

### Multiple project sessions

Use `-pyry-name` per project. Claude's session storage is keyed to working directory, so each pyry should run in its own project root:

```bash
cd ~/Projects/foo && pyry -pyry-name foo &
cd ~/Projects/bar && pyry -pyry-name bar &
pyry attach -pyry-name foo
```

### Migrating from `tmux + claude`

If you currently run claude under tmux for resilience, pyry is a near-drop-in replacement. The pattern:

| You used to | You now |
|---|---|
| `tmux new -s claude` then `claude --some-flags` inside | `pyry --some-flags` under launchd / systemd |
| `tmux attach -t claude` | `pyry attach` |
| `tmux send-keys 'C-b d'` (detach) | `Ctrl-B d` (same key, but routed through pyry's escape detector) |
| `tmux kill-session -t claude` | `pyry stop` |

Pyry adds: auto-restart with backoff, session resume on every restart via `--continue`, structured supervisor logs queryable via `pyry logs`, and a stable Unix-socket control plane.

### Watching the lifecycle in real time

```bash
# In one shell, run pyry as a foreground process or a service.
# In another:
journalctl --user -u pyry -f                    # systemd
tail -f /tmp/pyry.out.log /tmp/pyry.err.log     # launchd (paths from the example plist)
watch -n 1 pyry status                          # snapshot every second
```

## Troubleshooting

### `pyry: status: dial /Users/me/.pyry/pyry.sock: ... no such file or directory`

The daemon isn't running, or it's running under a different name. Check:

- Is pyry actually up? `ls ~/.pyry/` to see which socket files exist.
- Is your `PYRY_NAME` set in this shell? `echo $PYRY_NAME`.
- Did you set `-pyry-name` at startup but not on the client side?

### Pyry restarts forever after I `/exit`

That's the design. Pyry treats *any* child exit as a crash and restarts it. To actually stop the daemon: `pyry stop` from another shell, `systemctl --user stop pyry`, or send SIGTERM to the pyry process directly.

This behavior is deliberate: in production (service mode over SSH), if `/exit` killed pyry you couldn't get back to claude until someone manually started it again. Auto-restart is the always-on contract.

### `attach: bridge already has an attached client`

Someone else is currently attached. Phase 0 enforces single-attacher. Either find them and ask them to detach, or wait. (Long term: `pyry status` could surface the attached client's PID; not yet implemented.)

### `attach: no attach provider configured (daemon may be in foreground mode)`

You ran `pyry attach` against a daemon that started in foreground mode. Foreground pyry has the PTY bridged to its own terminal — there's nothing to attach to. Restart pyry without a TTY (e.g., under launchd / systemd, or `nohup pyry < /dev/null > pyry.log 2>&1 &`).

### `make check` fails on staticcheck

Install staticcheck once: `go install honnef.co/go/tools/cmd/staticcheck@latest`. The Makefile auto-installs if missing.

### Socket file permission denied

Pyry chmods the socket to `0600` (owner-only). If you're trying to connect as a different user, that's the boundary — pyry's threat model assumes single-user. The `-pyry-socket` flag can target a custom path with different permissions if you need that, but it's a deliberate departure from the default.

### Restart loop with `claude exited err="exit status 1"` repeating fast

Your `claude` binary is failing to start. Common causes:

- Wrong path: check `pyry logs` for `claude=...` and confirm the path is right (or set `-pyry-claude /actual/path`).
- Missing config: `~/.claude/` not initialised. Run `claude` directly once first to set up auth.
- Bad flags: pyry forwards your flags verbatim. If they're rejected by claude, the child exits immediately. Try without them.

The exponential backoff means crash loops slow down (500 ms → 1 s → 2 s → 4 s … → 30 s cap) — pyry stays available for `status` / `stop` queries throughout.

## FAQ

**Q: How is this different from running claude under `tmux`?**

`tmux` is a terminal multiplexer that happens to keep processes alive. Pyry is a process supervisor that happens to host a claude session. The relevant differences for this use case: pyry has typed exponential backoff (tmux doesn't restart at all if the inner shell exits), pyry's `--continue` integration preserves session history across restarts, pyry's control plane lets you query state and shut down cleanly without a separate session-manager command set, and pyry's binary footprint is ~5 MB versus tmux's full-featured implementation.

If you don't need any of that and you already know tmux, tmux is fine. Pyry is the right answer when you want a service that survives reboots and exposes a programmatic interface.

**Q: Why not just `systemd`'s `Restart=always`?**

Pyry adds: PTY allocation (claude needs a terminal to render), session continuity via `--continue`, attach/detach, and the control plane. `Restart=always` covers crash recovery alone, which is the smallest piece.

**Q: Does pyry work with `claude --print` (non-interactive mode)?**

Yes — claude's `-p` is just another flag pyry passes through. But there's no real reason to wrap one-shot `claude -p` runs in pyry; the supervisor's whole point is keeping a long-lived session alive. Use `claude -p` directly for one-offs.

**Q: Can I run pyry inside Docker?**

Yes, but you have to opt into TTY allocation (`docker run -it`) for foreground mode, or run in service mode and expose the socket as a volume mount. Service mode is the natural fit; the `-pyry-socket` flag handles unusual paths.

**Q: What's the security model?**

Single-user, filesystem-permission-based. The control socket is `0600`, so only the user that started pyry can connect. Any process running as that user can `pyry stop` the daemon — pyry isn't designed to defend against same-user adversaries. The threat model targets "developer's laptop" and "single-tenant home server," not multi-tenant production hosts.

**Q: Can two pyrys share a session?**

No. Phase 0 is single-session per pyry instance. Two instances pointing at the same working directory will fight over `~/.claude/projects/<cwd>/` session files via the `--continue` heuristic; they'll cross-contaminate. Use named instances (`-pyry-name foo` / `-pyry-name bar`) and/or different working directories. Phase 1 introduces multi-session within a single pyry, which is the right architecture for sharing.
