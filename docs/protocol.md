# Control-socket protocol

Wire-format reference for the Unix domain socket that pyry exposes. You only need this if you're writing an alternative client or scripting against pyry from outside Go.

## Transport

- **Socket**: Unix domain stream socket
- **Default path**: `~/.pyry/<name>.sock` where `<name>` defaults to `pyry`, overridden by `-pyry-name` or `$PYRY_NAME`
- **Permissions**: `0600` — owner-only access is the entire authentication boundary
- **Encoding**: line-delimited JSON for the handshake; raw bytes for the streaming portion of `attach`

A connection lifecycle is: dial → write one JSON `Request` → read one JSON `Response` → either close (one-shot verbs) or continue with raw bytes (`attach`).

The handshake has a 5-second deadline applied to the connection. Clients that don't write a complete JSON request within that window get disconnected.

## Message types

These are the Go types in [`internal/control/protocol.go`](../internal/control/protocol.go); the wire format is their JSON encoding.

### `Request`

```json
{
  "verb": "<verb>",
  "attach": { "cols": 200, "rows": 50 }
}
```

| Field | Type | Required | Notes |
|---|---|---|---|
| `verb` | string | yes | One of `status`, `stop`, `logs`, `attach` |
| `attach` | object | optional | Only meaningful for `verb=attach` (see below) |

Unknown verbs return `{"error":"unknown verb: \"…\""}`.

### `AttachPayload` (the `attach` field of `Request`)

```json
{ "cols": 200, "rows": 50 }
```

| Field | Type | Notes |
|---|---|---|
| `cols` | int | Client terminal column count |
| `rows` | int | Client terminal row count |

**Phase 0 caveat**: the server accepts these values but does not currently propagate them to the PTY. Send them anyway for forward compatibility — when geometry plumbing lands, no protocol change will be needed.

### `Response`

Exactly one of `status`, `logs`, `ok`, or `error` is meaningfully populated per response:

```json
{
  "status": { "phase": "running", "child_pid": 12345, ... },
  "logs":   { "lines": ["..."], "capacity": 200 },
  "ok":     true,
  "error":  "..."
}
```

| Field | Type | Set when |
|---|---|---|
| `status` | StatusPayload | Successful `status` response |
| `logs` | LogsPayload | Successful `logs` response |
| `ok` | bool | Successful response for verbs without a typed payload (`stop`, `attach`) |
| `error` | string | Server rejected the request (any verb) |

### `StatusPayload`

```json
{
  "phase": "running",
  "child_pid": 12345,
  "started_at": "2026-04-29T07:18:36Z",
  "uptime": "1m23s",
  "restart_count": 0,
  "last_uptime": "5.158s",
  "next_backoff": "500ms"
}
```

| Field | Type | Notes |
|---|---|---|
| `phase` | string | `starting`, `running`, `backoff`, or `stopped` |
| `child_pid` | int | 0 when no child is running |
| `started_at` | string | RFC 3339 timestamp of when the supervisor entered `Run` |
| `uptime` | string | Go duration since `started_at` |
| `restart_count` | int | Number of times the child has exited |
| `last_uptime` | string | Go duration of the most recent child's lifetime; omitted on first run |
| `next_backoff` | string | Go duration scheduled before next spawn; omitted when running |

Durations use Go's format: `"1m23s"`, `"310ms"`, `"1.5s"`. JSON-friendly without losing precision the way nanosecond integers do when piped through tools like `jq`.

### `LogsPayload`

```json
{
  "lines": [
    "time=2026-04-29T07:17:13.241+03:00 level=INFO msg=\"pyrycode starting\" version=dev",
    "time=2026-04-29T07:17:13.242+03:00 level=INFO msg=\"spawning claude\" args=[]",
    "..."
  ],
  "capacity": 200
}
```

| Field | Type | Notes |
|---|---|---|
| `lines` | string array | Recent supervisor log lines, oldest first |
| `capacity` | int | Configured ring-buffer size (200 by default); useful for clients that want to know whether they got the full history |

Each line is the same text format `slog.NewTextHandler` writes to stderr — newline-trimmed.

## Verbs

### `status`

Snapshot of supervisor state.

**Request**: `{"verb": "status"}`

**Response (success)**:
```json
{ "status": { ...StatusPayload... } }
```

**Response (server error)**: should not occur for `status`; the server panics at construction time if `state` is nil.

### `logs`

Recent supervisor log lines from the in-memory ring buffer.

**Request**: `{"verb": "logs"}`

**Response (success)**:
```json
{ "logs": { ...LogsPayload... } }
```

**Response (no log provider configured)**:
```json
{ "error": "logs: no log provider configured" }
```

This shouldn't happen with the standard pyry binary — it's only possible if a custom embedder constructs the server without a `LogProvider`.

### `stop`

Asks the daemon to shut down gracefully. Server acknowledges first, then triggers shutdown.

**Request**: `{"verb": "stop"}`

**Response (success)**:
```json
{ "ok": true }
```

**Response (no shutdown handler)**:
```json
{ "error": "stop: no shutdown handler configured" }
```

After receiving the `OK`, the client should expect the connection to close shortly. The shutdown propagates through the supervisor: child gets SIGKILL via `exec.CommandContext`, the listener closes, the socket file is removed, pyry exits with code 0.

### `attach`

Upgrades the connection to raw bytes flowing between the client's terminal and the supervised PTY.

**Request**:
```json
{
  "verb": "attach",
  "attach": { "cols": 200, "rows": 50 }
}
```

**Response (success)**:
```json
{ "ok": true }
```

**Response (no attach provider — daemon is in foreground mode)**:
```json
{ "error": "attach: no attach provider configured (daemon may be in foreground mode)" }
```

**Response (another client is already attached)**:
```json
{ "error": "attach: supervisor: bridge already has an attached client" }
```

After a successful `OK`:

1. The connection's deadline is cleared by the server (no implicit timeout).
2. Bytes the client writes flow as input to the supervised claude process.
3. Bytes claude writes to its stdout/stderr flow back to the client.
4. The connection ends when the client closes it (clean detach) or when the daemon shuts down.

Clients are expected to handle a detach signal locally. The pyry CLI uses Ctrl-B (0x02) followed by `d` (0x64) — same as `tmux`. This is *purely* a client-side convention; the server doesn't see or care about the escape sequence. Any byte-stream client can implement a different detach UX.

When the user wants to detach, the client closes the connection. The server's input pump (`io.Copy(pipeW, conn)`) returns on EOF, the bridge clears its attached state, and the connection is fully torn down.

## Examples

### Bash + jq

Quick status check:

```bash
echo '{"verb":"status"}' | nc -U ~/.pyry/pyry.sock | jq .
```

Or read just the phase:

```bash
echo '{"verb":"status"}' | nc -U ~/.pyry/pyry.sock | jq -r .status.phase
```

### Python

```python
import json
import socket

def pyry_status(socket_path="~/.pyry/pyry.sock"):
    import os
    path = os.path.expanduser(socket_path)
    with socket.socket(socket.AF_UNIX, socket.SOCK_STREAM) as s:
        s.connect(path)
        s.sendall(json.dumps({"verb": "status"}).encode() + b"\n")
        # Read until we have a complete JSON value.
        buf = b""
        while True:
            chunk = s.recv(4096)
            if not chunk:
                break
            buf += chunk
            try:
                return json.loads(buf)
            except json.JSONDecodeError:
                continue

print(pyry_status())
```

### Go (using the public `internal/control` types directly)

If you're embedding pyry-as-a-library, the [`internal/control`](../internal/control) package's `Status`, `Logs`, `Stop`, and `Attach` functions are the supported way. They handle the dial, deadline, and handshake correctly:

```go
import "github.com/pyrycode/pyrycode/internal/control"

resp, err := control.Status(ctx, "/Users/me/.pyry/pyry.sock")
if err != nil {
    log.Fatal(err)
}
fmt.Println(resp.Phase)
```

## Stability and versioning

The protocol is **unversioned** in Phase 0. As long as we're under 1.0, any field may be added (with `omitempty` so older clients still parse), removed (with breakage), or renamed. Once pyry stabilises we'll either freeze the current shape or add an explicit version negotiation in the handshake.

If you're writing a serious external client, pin to a specific commit and watch the release notes.
