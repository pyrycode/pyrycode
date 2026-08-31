# Test surface (`internal/acp/acp_test.go`)

Same-package, stdlib `testing` only, `t.Parallel()`, `-race`-clean. **Inbound**
tests use the single-shot `run`/`runErr` harness (`strings.NewReader` fed whole,
`Serve` runs to EOF, output parsed). **Outbound** tests (#757) use a separate
**live-Serve harness** (`liveTransport`) — see below.

- `TestTransport_Request_SingleResponseMatchingID` / `_NullIDIsARequest` —
  request → exactly one framed response, echoed id; the null-id-is-a-request edge.
- `TestTransport_Notification_NoResponse` — notification runs, `w` empty.
- `TestTransport_UnknownMethod_MethodNotFound` — `-32601`, echoed id.
- `TestTransport_ParseError` — non-JSON → `-32700`, id `null`.
- `TestTransport_InvalidRequest` — non-object JSON and a valid non-frame object
  → `-32600`, id `null`.
- `TestTransport_ResponseFrameTolerated` — response-shaped line → nothing written.
- `TestTransport_BatchRejected` — top-level array → single `-32600`, id `null`.
- `TestTransport_HandlerError_RPCvsPlain` / `_ErrorData` — `*Error` → verbatim
  code on the wire; plain error → `-32603` with detail in the diagnostics sink,
  **not** on `w`; handler `*Error` `Data` reaches the wire.
- `TestTransport_DiagnosticsIsolation` — every line on `w` is a `"jsonrpc":"2.0"`
  frame; error detail appears only in the diagnostics sink.
- `TestTransport_NoPanic` — adversarial inputs (empty/whitespace-only, truncated
  JSON, deeply-nested, wrong field types) → `Serve` returns without panicking.
- `TestTransport_BlankLinesSkipped_EOFReturnsNil` / `_NoTrailingNewline` /
  `_OverlongLine_ReturnsWrappedError` / `_ContextCancelledBetweenFrames` —
  framing edges.
- `TestTransport_RegisterGuards` / `TestNew_NilArgs` — programmer-error posture.

**Outbound `Call` tests (#757)** run against `liveTransport`: `Serve` on its own
goroutine over paired `io.Pipe`s; the test reads request frames off the writer to
learn the generated id, then feeds a matching response on the reader. `Call` is
issued from a spawned goroutine and its outcome collected over a channel, so
`t.Fatalf` only ever runs on the test goroutine. The diagnostics sink is a
mutex-guarded `syncBuffer` (a bare `bytes.Buffer` would `-race` against the Serve
goroutine — the `docs/lessons.md` gotcha).

- `TestTransport_Call_ResultPath` — request has a numeric id + method + params;
  the matching-id `result` response resolves `Call` to that result, nil error.
- `TestTransport_Call_ErrorPath` — nil params is omitted from the wire; a
  matching-id `error` response maps to an `*Error` with the code/message/`Data`
  (`errors.As`).
- `TestTransport_Call_ConcurrentDistinctIDs` — 32 concurrent `Call`s get distinct
  ids and each resolves to its *own* response (each echoes its own params). Under
  `-race` — the AC-2 race-safety proof.
- `TestTransport_Call_UnknownIDDropped` — a response with no outstanding call is
  dropped + logged ("no waiter" in the diag sink); a subsequent real call still
  resolves (Serve stayed live, registry uncorrupted).
- `TestTransport_Call_ContextCancelledReclaims` — cancel before feeding a
  response → `context.Canceled`; a late response for the reclaimed id is dropped;
  a fresh call with a new id still resolves (no waiter lingers).
- `TestTransport_Call_MalformedErrorObject` — an `error` that is not an object →
  a synthesized `*Error{CodeInternalError}`, `Call` does not hang.
- `TestTransport_Call_MarshalParamsError` — an unmarshallable param (a channel) →
  `Call` returns a marshal error before any id is burned or frame written.

`make check` (vet, race, staticcheck, substrate-guard) is green. Substrate-guard
is trivially green — `internal/acp` names no claude-TUI substrate literals (it
drives no claude), so it needs no allowlist entry in `cmd/substrate-guard`.
