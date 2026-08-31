# Classification decision table (the core mechanism)

`handleLine([]byte)` classifies each line, after trimming JSON's four
insignificant whitespace bytes (space/tab/CR/LF). An **empty / whitespace-only
line is skipped** — no output, no dispatch.

| Condition (checked in order) | Outcome | Response `id` |
|---|---|---|
| `!json.Valid(line)` | parse error | `-32700`, `null` |
| first non-ws byte is `[` | batch — **unsupported** | `-32600`, `null` |
| first non-ws byte is not `{` (non-object JSON value: `42`, `"x"`, `true`) | invalid request | `-32600`, `null` |
| `json.Unmarshal` into `rpcMessage` fails (valid object, wrong field types, e.g. `{"method":5}`) | invalid request | `-32600`, `null` |
| `method` present **and** `id` present | **request** → dispatch | one response, echoed id |
| `method` present, `id` absent | **notification** → dispatch | none written |
| `method` absent, `id` present, (`result` or `error` present) | **response frame** → **routed** to the `Call` awaiting this id (#757); unknown / reclaimed id → logged debug + dropped | none written |
| object, none of the above | invalid request | `-32600`, `null` |

**Handler mapping for a request:**
- `(result, nil)` → success response with `result` (a nil result marshals to
  `null`).
- `(nil, *Error)` → error response with that code/message/data.
- `(nil, plainErr)` → `-32603` internal error, generic message; the real error
  is logged to stderr, **never leaked on the wire**.
- **no** handler registered → `-32601` method not found, echoed id.

**Handler mapping for a notification:** dispatch if registered; **never write a
response** regardless of return. A non-nil error (or an unregistered
notification) is logged at debug/warn; nothing reaches the writer.

### Absent-vs-present-`null` is the whole classifier's input

The single decode-by-shape `rpcMessage` detects field presence by **nil-ness**:
an absent JSON key leaves the `*string` / `json.RawMessage` at nil; a present
key — **even a literal `null`** — is non-nil. This is exactly what separates a
null-id *request* (`{"jsonrpc":"2.0","method":"m","id":null}` — dispatched,
response id `null`) from a *notification* (no `id` key — no response). A one-type
decoder (over separate request/notification/response structs) holds the exported
surface to three types.

### The response-frame case (filled by #757)

A well-formed JSON-RPC **response** frame (an `id` plus a `result`/`error`, no
`method`) is **routed to its waiter** — `handleLine` calls `t.routeResponse(&msg)`
(see [Outbound-request primitive](#outbound-request-primitive-transportcall-757)).
Either way it produces **no** writer output. #755 originally left this as a
tolerated log-and-drop (a documented scope boundary), pinned by a test so #757
changed the behaviour against a known baseline; #757 replaced the drop with
response routing. An **unknown or already-reclaimed id** keeps the log-and-drop
behaviour, now *inside* `routeResponse`.

### Batch rejection (recorded in the package doc comment)

A top-level JSON array (a JSON-RPC batch) is rejected with a **single** Invalid
Request (`-32600`, id `null`); batching is **not supported**. ACP does not use
JSON-RPC batching, so this deviation from the spec's per-element batch handling
is inert in practice and keeps the transport a strict one-frame-per-line reader.
