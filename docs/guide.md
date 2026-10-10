> **Historical — Phase-0 user guide.** This guide covers the original
> single-session Phase-0 surface. The sessions, pairing, mobile/desktop
> remote, and agent-run surfaces post-date it, and its resize and
> single-session statements are superseded. For current state see the
> [README](../README.md) and [`knowledge/`](knowledge/).

# Pyrycode user guide

This guide walks through using `pyry` from first install to running it as a long-lived service. If you just want to try it locally and see what it does, the **Foreground mode** section is enough; everything after that adds production deployment, multi-instance, and operations detail.

## Contents

- [Mental model](#mental-model)
- [Installing](#installing)
- [Updating pyry](#updating-pyry)
- [Foreground mode](#foreground-mode-development)
- [Service mode](#service-mode-production)
- [Environment knobs](#environment-knobs)
- [Control verbs](#control-verbs)
- [Multiple instances](#multiple-instances)
- [Memory credentials](#memory-credentials)
- [Memory configuration](#memory-configuration)
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
| **Service** | Pyry was started without a TTY (launchd, systemd, `nohup`, `< /dev/null`, …) | claude runs headless on the stream-json surface. Its output reaches paired clients (desktop, mobile) over the relay; there is no terminal to borrow. |

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

- Go 1.26.2 or later
- A `claude` binary on `PATH` (or supplied via `-pyry-claude`)
- Linux or macOS (Windows is out of scope)

Install the binary somewhere stable:

```bash
mkdir -p ~/.local/bin
cp pyry ~/.local/bin/
# Make sure ~/.local/bin is on $PATH.
```

**Re-installing over a running daemon (macOS):** do not `cp` a new build over `~/.local/bin/pyry` while the launchd daemon is running from it. The copy succeeds, but the kernel then refuses to execute the file — `launchctl print gui/$(id -u)/dev.pyrycode.pyry` shows `last exit reason = OS_REASON_CODESIGNING`, and even `pyry --version` dies with signal 9 — although `codesign -vv` still reports it valid. The daemon stays down until the file is replaced properly. Write straight to the path instead, which lands on a fresh file:

```bash
go build -o ~/.local/bin/pyry ./cmd/pyry
launchctl kickstart -k gui/$(id -u)/dev.pyrycode.pyry
```

or remove the old file before copying (`rm ~/.local/bin/pyry && cp pyry ~/.local/bin/`). `install.sh`, `pyry update`, `go install` and Homebrew are all unaffected: each replaces the file rather than overwriting it in place.

For a remote target, cross-compile:

```bash
make linux                                          # dist/pyry-linux-amd64
GOOS=linux  GOARCH=arm64 go build -o /tmp/pyry-arm ./cmd/pyry
scp dist/pyry-linux-amd64 server:~/.local/bin/pyry
```

## Updating pyry

Run `pyry update` to download and install the latest GitHub release in place:

```bash
pyry update
```

The command fetches the release manifest, verifies the tarball's SHA-256 against the published `checksums.txt`, and atomically replaces the binary at the path returned by `os.Executable()`. After it returns, restart the daemon if pyry is running as a service:

- macOS (launchd): `launchctl kickstart -k gui/$(id -u)/dev.pyrycode.pyry`
- Linux (systemd): `systemctl --user restart pyry`

Use `pyry update --check` to print the current and latest versions without downloading. Use `pyry update --version <tag>` to install a specific release (including downgrades). If pyry was installed via Homebrew, prefer `brew upgrade pyry` to keep the cellar consistent — `pyry update` will print a hint to that effect but still proceed if you ask it to.

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
| `-pyry-claude-account-source <source>` | (unset) | This instance's Claude account token source — an absolute path to an owner-only token file, an `op://vault/item/field` 1Password reference, or `keychain:<name>` for the macOS Keychain or Linux Secret Service. See [Claude account source](#claude-account-source) below. |
| `-pyry-claude-account-op-cli <name-or-path>` | `op` | The 1Password CLI an `op://` account source runs. One bare executable name on `PATH`, or one absolute path. See [Claude account source](#claude-account-source) below. |

If a claude flag happens to start with `-pyry-`, separate the two with `--`:

```bash
pyry -pyry-verbose -- --pyry-resume   # claude gets --pyry-resume verbatim
```

In practice this never bites because `claude` doesn't have any `-pyry-*` flags.

## Service mode (production)

Service mode is the load-bearing deployment: pyry running as a long-lived daemon, supervised claude detached from any specific terminal, accessible from any shell. This is how you'd run pyry on a server, on a Linux home box, or under launchd on a Mac.

The mode toggle is automatic — when pyry starts without a controlling terminal it switches to service mode. See [`deployment.md`](deployment.md) for systemd and launchd setup walkthroughs.

### Reaching a running session

There is no terminal to attach to. claude runs on the stream-json surface, so its
output is a structured event stream rather than screen bytes, and the daemon fans
that stream to paired clients over the relay. Use the desktop or mobile client to
watch or steer a live session.

`pyry attach` and its `Ctrl-B d` detach existed until #1348 (2026-08-16) and were
removed with the terminal-driving path they read from. They had been non-functional
since the 2026-07-24 cutover, because the bridge they read was only ever fed by the
terminal copy loop.

`pyry status`, `pyry logs` and `pyry stop` are unaffected and work from any shell.

## Environment knobs

A few runtime settings are read from the environment rather than from flags, so they apply in both foreground and service mode.

| Variable | Default | Purpose |
|---|---|---|
| `PYRY_NAME` | `pyry` | Instance name, same as `-pyry-name`; the flag wins when both are set |
| `PYRY_APPROVAL_TIMEOUT` | `10m` | How long an approval waits for a human before the daemon denies it |
| `PYRY_CLAUDE_ACCOUNT_SOURCE` | (unset) | Same as `-pyry-claude-account-source`; the flag wins when both are set. See [Claude account source](#claude-account-source) below. |
| `PYRY_CLAUDE_ACCOUNT_OP_CLI` | (unset) | Same as `-pyry-claude-account-op-cli`; the flag wins when both are set. See [Claude account source](#claude-account-source) below. |

`PYRY_APPROVAL_TIMEOUT` is the one worth setting deliberately. When claude asks permission to use a tool, the request is parked until a client answers it, and this is how long the daemon waits before answering "no" on your behalf. Waiting is not the risky state, because the tool does not run while the request is outstanding, so the value is about how long you might reasonably take to reach your phone rather than about safety. The default is sized for answering from a phone. Shorten it if you sit at the machine and want a faster fail-closed:

```bash
PYRY_APPROVAL_TIMEOUT=2m pyry
```

Lengthening it is fine too, within the ceiling below.

Do not raise it much past fifteen minutes without reading [issue #1911](https://github.com/pyrycode/pyrycode/issues/1911) first. A message you send while an approval is still pending is held behind it, and that hold gives up after fifteen minutes and discards the message. Setting the approval window longer than the hold trades a prompt that gives up too early for a message that disappears. The default is already set clear of that hold, but only by five minutes — so a prompt answered near the end of the window leaves a queued message a thin margin, and raising the knob thins it further. That issue covers lifting the ceiling properly.

The value takes any Go duration, such as `90s`, `10m` or `1h`. An unset or unparseable value falls back to the default.

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
time=2026-04-29T07:17:18.401+03:00 level=WARN msg="claude exited" session=98fcb6d2-1e4a-4b1e-9c3d-7a6f9e2b5c10 err="exit status 1" uptime=5.158s
time=2026-04-29T07:17:18.402+03:00 level=INFO msg="restarting after backoff" delay=500ms
time=2026-04-29T07:17:18.903+03:00 level=INFO msg="spawning claude" args=[--continue] workdir=""
```

The buffer covers supervisor-level events (spawns, exits, restarts, attach/detach, shutdown) — not claude's own output. Under launchd or systemd, claude's stdout is captured by the service manager (`/tmp/pyry.out.log` for the example launchd plist; `journalctl --user -u pyry` for systemd).

When claude exits on its own — not a restart or shutdown pyry asked for — the daemon's own log output also carries the last few lines claude printed to stderr before it died, capped at 1024 bytes. That tail deliberately never reaches this ring, so it never reaches `pyry logs` or the debug bundle either: it can hold a file path or a credential from whatever claude was doing when it failed. See the next section for where to actually read it.

### `pyry stop`

Asks the daemon to shut down gracefully:

```
$ pyry stop
pyry: stop requested
```

Internally: the server acks `OK`, then triggers the same shutdown path as SIGINT/SIGTERM. The supervised child gets SIGKILL via `exec.CommandContext`, the backoff sleep is interrupted, the listener closes, the socket file is removed, and pyry exits with code 0.

Under a service manager, `pyry stop` does the same job as `systemctl --user stop pyry` or `launchctl unload`. Either is fine.

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
pyry logs                       # tails elli
```

Or alias it for convenience:

```bash
alias pyry-elli='PYRY_NAME=elli pyry'
pyry-elli &                     # supervises elli
pyry-elli attach                # attaches to elli
```

For unusual setups (Docker mounts, shared sockets, paths outside `$HOME`), `-pyry-socket /any/path.sock` overrides the name-derived default entirely.

### Claude account source

By default a pyry instance's claude child inherits whatever `CLAUDE_CODE_OAUTH_TOKEN` (or other Claude authentication) is already in the daemon's environment — the same account you'd get running `claude` interactively. When you run more than one instance, or want daemon usage to draw on a separate account from your interactive one, point an instance at its own token file, 1Password reference, or OS secret-store item.

The source is resolved once per instance, in this order, and the first nonempty value wins:

1. `-pyry-claude-account-source <source>`
2. `$PYRY_CLAUDE_ACCOUNT_SOURCE`
3. the `source` field in `~/.pyry/<name>/claude-account.json`

The JSON file is read only when both the flag and the environment variable are empty. A missing file, or a file without a `source` key, means no source is configured and launches keep whatever credentials they inherit — nothing changes. Example `~/.pyry/elli/claude-account.json` with a token file:

```json
{
  "source": "/home/op/.config/pyry/elli-claude-token"
}
```

...or with a 1Password reference as the master copy instead:

```json
{
  "source": "op://vault/elli-claude-token/credential",
  "op_cli": "op"
}
```

The file can also carry an optional `label` — an operator-chosen name for this account, shown to paired clients so an operator juggling several instances can tell accounts apart at a glance:

```json
{
  "source": "/home/op/.config/pyry/elli-claude-token",
  "label": "Work account"
}
```

`label` is read only when `claude-account.json` itself supplies the `source` — an instance pointed at its source by the flag or `$PYRY_CLAUDE_ACCOUNT_SOURCE` always reports an empty label, even if the file also sets one. It must be at most 64 bytes of valid UTF-8 with no control character; anything else is a startup error naming the file, never the label value (it could be a token pasted into the wrong field). Keys other than `source`, `op_cli` and `label` are ignored. The file itself must be owned by the user running pyry and must not be group- or other-writable — it is refused otherwise, since it picks which owner-only file is read, or which executable is run, to produce the token handed to a claude child. This ownership check, and the UTF-8 validity check above, apply to every key the file supplies: an invalid-UTF-8 `source` or `op_cli` is also a startup error now, rather than a value that silently turned into a mangled path that could only fail later at read time.

There are three source kinds: an absolute path to a token file, an `op://vault/item/field` reference read through the 1Password CLI, or `keychain:<name>` read from the macOS Keychain or Linux Secret Service. A relative path, any other URI scheme, or anything else nonempty is refused and stops startup.

**The token file.** One raw token, optionally followed by a single LF or CRLF — not a `source`d shell assignment and not an `EnvironmentFile` line, just the bytes of the token itself:

```bash
printf '%s\n' "sk-ant-oat01-..." > ~/.config/pyry/elli-claude-token
chmod 600 ~/.config/pyry/elli-claude-token
```

The file must be a regular file, owned by the user running pyry, with no group or other permission bits set — `0400` or `0600` are both accepted. It is refused if it's missing, unreadable, not a regular file (a directory or FIFO included), owned by someone else, loosely permissioned, empty, over 4096 bytes, or holds anything other than printable non-space ASCII once the trailing newline is stripped.

**The 1Password reference.** When the source starts with `op://`, the daemon runs `<cli> read <reference>` — the reference as one argument, no shell, no stdin, stderr discarded — and takes the capped stdout (same 4096-byte limit and printable-ASCII shape as the token file) as the token. Nothing is ever cached: this runs on the startup read and again on every claude launch attempt, exactly like the token file.

The CLI itself is resolved independently of where the source came from, in this order, defaulting to `op`:

1. `-pyry-claude-account-op-cli <name-or-path>`
2. `$PYRY_CLAUDE_ACCOUNT_OP_CLI`
3. the `op_cli` field in `~/.pyry/<name>/claude-account.json`
4. `op`

`op_cli` is only ever looked at for an `op://` source — file and keychain sources ignore it, so an odd value left in `claude-account.json` can't break those instances. The value must be either one bare executable name (letters, digits, `.`, `_`, `+`, `-`, other than `.` or `..`) looked up on `PATH`, or one absolute path, which may contain spaces — never a shell command (`op read`, `./op`, and similar are refused). Anything else is a startup error naming only where the `op_cli` setting came from.

On Windows binaries reached through WSL, point `op_cli` at the Windows CLI's absolute path, e.g.:

```bash
pyry -pyry-claude-account-op-cli "/mnt/c/Program Files/1Password CLI/op.exe" -pyry-claude-account-source "op://vault/elli-claude-token/credential"
```

There is no WSL autodetection — the operator must already have WSL interop, the Windows 1Password CLI, and [1Password's desktop-app integration](https://developer.1password.com/docs/cli/app-integration/) set up and unlocked before the daemon starts. See the [`op read` reference](https://developer.1password.com/docs/cli/reference/commands/read/) for what a reference looks like and what the CLI itself requires.

**The OS secret-store item.** Use `keychain:<name>`, where `<name>` is a dedicated item's service name. For example, these flag and environment settings select the same item:

```bash
pyry -pyry-name elli -pyry-claude-account-source "keychain:pyry-elli-claude-token"
PYRY_CLAUDE_ACCOUNT_SOURCE="keychain:pyry-elli-claude-token" pyry -pyry-name elli
```

Or set `~/.pyry/elli/claude-account.json`:

```json
{
  "source": "keychain:pyry-elli-claude-token",
  "label": "Work account"
}
```

On macOS, create a generic-password item in your login Keychain:

```bash
security add-generic-password -s "pyry-elli-claude-token" -a "$USER" -w
```

Enter the raw account token at the password prompt. **Do not reuse `Claude Code-credentials`: it holds Claude Code's interactive login.** Create a separate item for the daemon's account; the daemon does not reject that reserved name itself.

On Linux, install `libsecret-tools` (the package providing `secret-tool` on Debian/Ubuntu), then create a Secret Service item with the matching `service` attribute:

```bash
secret-tool store --label="Pyry elli Claude token" service "pyry-elli-claude-token"
```

Enter the raw token at the password prompt in a logged-in desktop session. The label describes the item; lookup uses the `service` attribute. See the [secret-tool manual](https://manpages.ubuntu.com/manpages/noble/man1/secret-tool.1.html) for its store and lookup commands.

On every read, macOS runs `security find-generic-password -s <name> -w`; Linux runs `secret-tool lookup service <name>`. The tool is resolved through the daemon's `PATH`, and the name is one argument with no shell or stdin. Stdout must satisfy the same token format and 4096-byte limit as the token file; stderr is discarded. An empty name, a control byte (including DEL), or a name starting with `-` is a startup error naming only the setting's origin. This source is supported only on macOS and Linux. See [deployment guidance](deployment.md#claude-account-source) for access prompts, login services and the headless Linux limit.

**When it's read.** The daemon makes one read at startup to initialize the account, and after that re-reads the source fresh on every claude launch attempt — first launches, crash-loop restarts, and respawns alike. A rotated token file (write a new one and `mv` it into place), 1Password item, or OS secret-store item takes effect on the very next launch; there's no caching to invalidate or restart to trigger.

**Failure and recovery.** An unusable *source* — a bad flag/env value, an unreadable or malformed `claude-account.json`, or an invalid `op_cli` — is a startup error: the daemon refuses to start, the same posture as a bad `interactive_runner` or `debug_capture` setting. A *read* that fails at startup or once the daemon is running (the token file got deleted or permissions loosened; a tool is missing; the vault or keyring is locked; an item is missing or access denied; the tool times out, is cancelled, or prints something unusable) refuses that claude launch, with no fallback to a previously read token or the inherited login; the daemon, its control socket and the relay keep running. The next successful read clears the failure and launches resume normally. Neither case ever logs or echoes the token, the configured path, the 1Password reference, the keychain item name, or the tool's own stdout/stderr — failures name only where the setting came from (flag, env, or file) or report one of a small fixed set of reasons ("1Password CLI unavailable", "1Password read timed out", "1Password read failed", and similar). For keychain reads the reasons are "keychain tool unavailable", "keychain read failed", "keychain output empty", "keychain output invalid", "keychain read timed out" and "keychain read cancelled".

The read is bounded to ten seconds at startup, matching the per-attempt deadline the runner itself applies. For the token file that bound covers reads that complete, not reads that hang — an open or read against a wedged FUSE or network mount is not interrupted, so don't rely on this as a hard ceiling on startup time for a token file on unusual storage; a plain local file returns essentially immediately. For 1Password and keychain reads, the caller returns when the bound expires and the tool's process group is killed; a hung tool or access prompt never holds a claude launch past the deadline.

**What a paired client can see.** Any paired client can ask the daemon which account source it uses by sending `request_claude_account` over the relay; the daemon answers with the source's kind (`machine_login`, `file`, `1password` or `os_keychain`), its `label`, and whether its latest read is `ready` or `failed` with a short reason. This is read-only and always reflects a read the daemon already made on its own — asking never triggers one. It never exposes the token, the configured file path, the 1Password reference, or the keychain item name. See [`protocol-mobile.md` § Claude account source](protocol-mobile.md#claude-account-source) for the wire format.

**What stays shared.** This only changes which account token a claude child receives. Settings, plugins, skills, memory and everything else under `CLAUDE_CONFIG_DIR` stay shared across every instance; a Claude account source is not a sandboxed configuration directory.

**A successful read doesn't prove the token works.** It means the daemon handed claude a syntactically acceptable token, not that Claude has accepted it — `claude` itself still decides. Other, higher-priority Claude authentication settings (an API key, a different credential helper) can still override the subscription OAuth token this feature sets. See [Claude Code's authentication documentation](https://code.claude.com/docs/en/authentication) for how Claude resolves credentials.

Only the claude child's environment is affected. The daemon's own environment, any Codex child, and other subprocesses never see this token.

## Memory credentials

Manage an OpenAI memory credential locally on the daemon host, as the user who
runs the service. These commands work without a running daemon and do not prompt:

```bash
pyry memory credential set openai < /path/to/protected/openai-token
pyry memory credential status openai
```

`set` reads the secret only from stdin, through end-of-file. You can also pipe
the output of a secret manager into it. Keep token values out of command-line
arguments. Input is capped at **4096 bytes including any trailing newline**;
oversize input is rejected rather than truncated. One trailing LF or CRLF is
removed, then the token must be nonempty printable non-space ASCII. Thus a token
with a trailing LF can contain at most 4095 bytes, or 4094 with CRLF. Validation
is local, requires no particular key prefix, and makes no OpenAI request.

A successful set prints only JSON containing a non-secret reference, shaped like
`{"reference":"memory:openai:<opaque identifier>"}`. Retain that reference: it
stays the same across successful replacements and resolves internally to the
current token, including in a fresh process. There is no command to print the
secret. Extra arguments and unsupported verbs or providers are rejected without
echoing their values.

`status` prints only `{"configured":false}` when no selection has been committed,
or `{"configured":true}` after reading and validating the selected credential
afresh. Configured means the stored token passes local validation; it does not
prove OpenAI accepts it. Unsafe storage, malformed selection, or a selected token
that is missing, inaccessible or invalid causes a sanitized error and nonzero
exit, rather than an unconfigured result or reuse of a cached token.

Set, status and internal resolution have a ten-second operation deadline,
including waiting for set's stdin or the storage lock. They honor cancellation
and any shorter caller deadline; the CLI also honors SIGINT and SIGTERM. Rejected
input, unsafe storage, cancellation, timeout or a failed save leaves the prior
committed credential and reference intact. Once the failure clears, the same
reference resolves to the old token; a successful replacement selects the new
token. A cancelled or timed-out save cannot publish later.

Secrets and protected selection metadata live in
`~/.pyry/memory/credentials/`, outside vaults and ordinary settings. Storage
directories are service-user-owned with mode 0700, and regular files have mode
0600 from creation. Existing unsafe storage is refused without being repaired or
overwritten. Memory credentials are separate from the daemon's Claude account
and the operator's interactive login. See [deployment requirements](deployment.md#memory-credentials).
This lifecycle currently uses protected files; OS-store access and preference
are deferred to [#3113](https://github.com/pyrycode/pyrycode/issues/3113).

## Memory configuration

Save and inspect managed memory settings locally on the daemon host, as the user
who runs the service. Both commands are noninteractive and work without a running
daemon. For example, substitute your chosen model names in:

```bash
pyry memory configure --vault default \
  --embedding-provider local --embedding-model EMBEDDING_MODEL \
  --capture-agent codex --capture-model CAPTURE_MODEL
pyry memory status
```

All five main choices are required on every configure call:

| Flag | Accepted value |
|------|----------------|
| `--vault` | `default` or an absolute path to an existing local directory |
| `--embedding-provider` | `local` or `openai` |
| `--embedding-model` | Nonblank embedding model name |
| `--capture-agent` | `claude` or `codex` |
| `--capture-model` | Nonblank capture model name |

Optionally repeat `--knowledge-folder /absolute/folder` to add read-only knowledge
roots. For OpenAI embeddings, also supply `--credential-reference REF`, using the
current reference returned by [memory credential set](#memory-credentials).
Options accept both `--flag value` and `--flag=value`. Only `--knowledge-folder`
may repeat; unsupported flags, positional arguments and missing values are errors.
Embedding and capture choices are independent: either embedding provider can pair
with either capture agent. Model names are checked for nonblank values, without
checking model availability or contacting a provider.

Each configure call **replaces all known memory settings** in
`~/.pyry/config.json`. Omitting knowledge-folder flags clears previous additional
roots. Switching to `default` clears the saved vault path; switching to `local`
clears a previous credential reference and rejects a supplied reference, even an
empty one. Unrelated settings and unknown JSON values are preserved, although JSON
formatting may change. An unavailable home is an error; there is no config fallback
relative to the current directory. A successful save prints exactly
`{"configured":true}` followed by a newline. Failed validation or saving exits
nonzero, emits no success JSON and preserves the previous config bytes.

### Vaults and knowledge roots

There are exactly two vault modes. `--vault default` saves only
`{"mode":"default"}`: it records no path or configure-process working directory.
Effective resolution later uses the daemon's resolved startup workspace base,
independently of the seeded `default` channel or any hosted session's working
directory. The concrete default path and its usability checks are deferred until
that resolution.

`--vault /absolute/folder` saves mode `separate` with its canonical path. The
directory must already exist and be writable and traversable by the invoking
service user. Additional knowledge folders must already exist and be readable
and traversable; they are read-only inputs, not capture destinations. Relative
paths, files, unavailable directories and unresolved symlinks are rejected.
Paths are cleaned and symlinks resolved before validation and persistence.
Duplicate or nested additional roots collapse to their parent by path components;
siblings such as `notes` and `notes-old` remain distinct.

Transcript storage is automatic at `~/.pyry/memory/recent-transcripts`, with
existing ancestors and symlinks resolved. It is daemon-owned and cannot be chosen
with a flag. It may not exist yet; configuring or inspecting settings does not
create it. Effective search roots combine the vault, additional roots and
transcripts, indexing overlapping subtrees once while retaining the vault write
destination and transcript ownership separately.

The vault cannot equal, contain or sit inside either transcript storage or
`~/.pyry/memory/credentials`. Additional roots cannot equal, contain or sit inside
credential storage. Transcript and credential storage must also be disjoint,
including through symlink aliases. Missing reserved directories are checked
through existing ancestors; broken symlinks and file ancestors are errors. These
checks apply to separate vaults immediately and to the default vault at effective
resolution. Configuration neither creates nor seeds vaults, and leaves existing
vault files and instructions unchanged.

### Status and credentials

`pyry memory status` takes no arguments and prints JSON followed by a newline.
Missing config, legacy config without memory, or `"memory":null` returns only
`{"configured":false}`. A configured local/default example, with an illustrative
service-user home, is:

```json
{
  "configured": true,
  "vault": {"mode": "default"},
  "additional_roots": [],
  "embedding": {
    "provider": "local",
    "model": "EMBEDDING_MODEL",
    "credential_configured": false
  },
  "capture": {"agent": "codex", "model": "CAPTURE_MODEL"},
  "transcript_path": "/home/service/.pyry/memory/recent-transcripts"
}
```

The command emits compact JSON. A separate vault instead includes its saved path,
for example `"vault":{"mode":"separate","path":"/home/service/vault"}`.
`additional_roots` is always an array, empty when absent. Status validates saved
choices and paths but reports those saved choices; the effective default vault
path stays unresolved and is omitted.

OpenAI requires a current lifecycle-issued reference that resolves successfully
on configure and freshly on every status call. Settings store only the validated
reference, never the token. Status replaces `credential_reference` with
`credential_configured:true` after successful resolution. Local embeddings report
`credential_configured:false` without requiring a stored credential. Missing,
invalid or unresolvable OpenAI references, unsafe or unavailable credential
storage, malformed config and invalid saved choices cause sanitized errors and
nonzero exit with no success JSON. Configure/status output and diagnostics expose
no tokens, references or credential backend/source metadata.

Saving settings alone does not install or start memory. Future daemon application
will occur after restart when runtime support is added. Configured status makes
no installation, indexing or capture-readiness claim. These operations do not
launch, modify or take over manual memsearch; existing client search availability
continues to depend on its own evidence.

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

See [`deployment.md`](deployment.md). Short version: install the binary, drop the systemd unit or launchd plist into the right place, edit `ExecStart` to add any claude flags you need, enable the service. Pair a client to talk to it.

### Multiple project sessions

Use `-pyry-name` per project. Claude's session storage is keyed to working directory, so each pyry should run in its own project root:

```bash
cd ~/Projects/foo && pyry -pyry-name foo &
cd ~/Projects/bar && pyry -pyry-name bar &
pyry status -pyry-name foo
```

### Migrating from `tmux + claude`

If you currently run claude under tmux for resilience, pyry is a near-drop-in replacement. The pattern:

| You used to | You now |
|---|---|
| `tmux new -s claude` then `claude --some-flags` inside | `pyry --some-flags` under launchd / systemd |
| `tmux attach -t claude` | the desktop or mobile client (there is no terminal to attach to) |
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

### `make check` fails on staticcheck

Install staticcheck once: `go install honnef.co/go/tools/cmd/staticcheck@latest`. The Makefile auto-installs if missing.

### Socket file permission denied

Pyry chmods the socket to `0600` (owner-only). If you're trying to connect as a different user, that's the boundary — pyry's threat model assumes single-user. The `-pyry-socket` flag can target a custom path with different permissions if you need that, but it's a deliberate departure from the default.

### Restart loop with `claude exited err="exit status 1"` repeating fast

Your `claude` binary is failing to start. Common causes:

- Wrong path: check `pyry logs` for `claude=...` and confirm the path is right (or set `-pyry-claude /actual/path`).
- Missing config: `~/.claude/` not initialised. Run `claude` directly once first to set up auth.
- Bad flags: pyry forwards your flags verbatim. If they're rejected by claude, the child exits immediately. Try without them.

`pyry logs` tells you *that* claude exited, not *why* — the reason lives in claude's own stderr, which the ring buffer never holds. Read the daemon's own log output instead: `journalctl --user -u pyry` under systemd, or the launchd plist's stderr path (`/tmp/pyry.err.log` in the example plist) under launchd. The `claude exited` line there carries the session id and the last few lines claude printed to stderr before exiting, which is usually enough to diagnose a bad flag or missing config without re-running claude by hand over SSH.

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
