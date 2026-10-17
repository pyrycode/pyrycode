# #3575 — keep claude's latest 5-hour and weekly allowance reading and answer it on request

## Files read

- `internal/streamsup/parser_rate_limit.go` → `emitRateLimit`, `rateLimitEventLine`, `rateLimitInfo`: the warning-gated `rate_limited` path that must stay unchanged; its whole-line decode is why the windows need a decode of their own.
- `internal/streamsup/parser.go` → `Parser`, `NewParser`: the parser is per session; seams are installed after construction (`SetCanUseToolHandler` in `parser_control.go` is the pattern).
- `cmd/pyry/streamsup_runner.go` → `newStreamRunnerFactory`, `streamApprovalConfig`: the Claude-only runner factory where per-daemon objects reach each session's parser. Codex sessions never pass through it.
- `cmd/pyry/main.go` → `runSupervisor`'s `streamApprovalConfig{...}` literal and the relay config's `claudeAccount:` field: composition root.
- `cmd/pyry/relay.go` → relay config struct field `claudeAccount`, the `Handlers` map entry for `TypeRequestClaudeAccount`.
- `internal/relay/handlers/claude_account.go` → `RequestClaudeAccount`, `ClaudeAccountSource`: the request/reply precedent (map dispatch, no conversation lookup, no interactive gate, malformed payload → `protocol.malformed`).
- `internal/protocol/claude_account.go`, `internal/protocol/codes.go` (`TypeRequestClaudeAccount`), `internal/protocol/envelope.go` → `inboundAppTypeSet`: wire vocabulary and the v1/v2 partition.
- `internal/protocol/compat_test.go`, `cmd/pyry/relay_guard_test.go`: closed lists of every type constant that must classify each new type.
- `internal/relay/v2session_handshake.go` → `supportedV2Capabilities`, `negotiateCapabilities`; `internal/protocol/handshake.go` → `CapabilityStopBackgroundTask` (detection-only precedent) and `handshake_test.go`'s constant table.
- `internal/e2e/realclaude/testdata/subagent_prompt_v2.1.280.json` → `frames[].payload` of type `rate_limit_event`: the committed `allowed` line with five_hour 0.44 / seven_day 0.64.

Overlaps: `feature/2939` touches `cmd/pyry/streamsup_runner.go` and `feature/3240` touches `internal/relay/v2session_handshake.go`; edits there are additive and local.

## Context

Every claude ≥ 2.1.259 `rate_limit_event` carries `rate_limit_info.unifiedWindows.{five_hour,seven_day}.{utilization,resetsAt}`, on benign lines too. The daemon discards it. Clients want "how much of each allowance is left" on demand, labelled with when the daemon observed it. The reading is account-wide and each daemon instance runs one account, so one in-memory reading per daemon is the right granularity. A sibling ticket pushes new readings; this one answers a request.

## Design

### streamsup: a second, independent decode (`internal/streamsup/parser_usage_windows.go`)

New exported type (1) and method:

- `UsageWindow{Utilization *float64; ResetsAt *int64}` — one window, verbatim. Pointers keep absence distinct from `0`.
- `(*Parser).SetUsageWindowsHandler(h func(fiveHour, sevenDay *UsageWindow))` — one reading per call; a nil window was absent on the line. A seam like `SetCanUseToolHandler`: installed before the parser is handed to a runner, read on the forwarder goroutine.

`emitRateLimit` first calls a new unexported `p.readUsageWindows(line)`, then continues exactly as today. `readUsageWindows` decodes into a target that declares only `rate_limit_info.unifiedWindows` (a pointer). It calls the handler only when the decode succeeds and at least one window is present. No `unifiedWindows`, `null`, `{}` (neither window) and a decode failure all leave the handler uncalled, so the previous reading stands. A decode failure logs one content-free Debug line. Because `rateLimitEventLine` is untouched, a malformed `unifiedWindows` cannot change what the line does to `rate_limited`.

A reading replaces the stored one whole: a line carrying only `five_hour` stores a reading with no `seven_day`. That is what claude reported on that line.

### cmd/pyry: the daemon-wide store (`cmd/pyry/claude_usage.go`)

`claudeUsageStore` — mutex-guarded latest pair of `*streamsup.UsageWindow` plus `observedAt time.Time` (daemon wall clock, UTC, taken in `record`). Zero value is "no reading". Methods:

- `record(fiveHour, sevenDay *streamsup.UsageWindow)` — stores the reading and `now()`.
- `ClaudeUsage() protocol.ClaudeUsagePayload` — satisfies `handlers.ClaudeUsageSource`.

Wiring: `streamApprovalConfig` gains `usage *claudeUsageStore`; `newStreamRunnerFactory` installs `parser.SetUsageWindowsHandler(approval.usage.record)` when non-nil. `runSupervisor` builds one store and passes it to the approval config and to the relay config's new `claudeUsage handlers.ClaudeUsageSource` field. Codex sessions use a different factory, so they never update it.

### protocol (`internal/protocol/claude_usage.go`, `codes.go`, `envelope.go`, `handshake.go`)

- `TypeRequestClaudeUsage = "request_claude_usage"` (client → daemon, in `inboundAppTypeSet`, map-dispatched) and `TypeClaudeUsage = "claude_usage"` (daemon → client, outbound reply only).
- `RequestClaudeUsagePayload struct{}`.
- `ClaudeUsagePayload{State string "state"; ObservedAt *time.Time "observed_at,omitempty"; FiveHour, SevenDay *ClaudeUsageWindow "...,omitempty"}`, with `State` one of `ClaudeUsageStateNone = "none"` or `ClaudeUsageStateObserved = "observed"`.
- `ClaudeUsageWindow{Utilization *float64 "utilization,omitempty"; ResetsAt *int64 "resets_at,omitempty"}`. `omitempty` on a pointer omits only nil, so `0` serializes as `0`.
- `CapabilityClaudeUsage = "claude_usage"`, detection-only, appended to `supportedV2Capabilities`.

### relay handler (`internal/relay/handlers/claude_usage.go`)

`ClaudeUsageSource interface{ ClaudeUsage() protocol.ClaudeUsagePayload }` and `RequestClaudeUsage(src, logger) dispatch.Handler`, a copy of `RequestClaudeAccount`'s shape: decode `{}` (malformed → non-retryable `protocol.malformed` reply), reply once with `TypeClaudeUsage` via `c.Reply`. A nil source answers `state: "none"`.

Exported types across the change: `streamsup.UsageWindow`, `protocol.RequestClaudeUsagePayload`, `protocol.ClaudeUsagePayload`, `protocol.ClaudeUsageWindow`, `handlers.ClaudeUsageSource` — five.

## Concurrency model

`record` runs on each Claude session's stdout forwarder goroutine (many concurrently); `ClaudeUsage` runs on relay dispatch goroutines. One `sync.Mutex` guards both fields. No goroutine is added. The handler is installed in the factory before `streamsup.New`, before any forwarder starts.

## State transitions and identity reuse

| Event | Covered by |
|---|---|
| Reading arrives, then a line with no `unifiedWindows` (reading kept) | `TestParser_UsageWindowsKeepsPreviousReading` (streamsup) and `TestClaudeUsageStore` (cmd/pyry, `-race`) |
| Repeated readings from many sessions concurrently (last write wins, no race) | `TestClaudeUsageStore_ConcurrentRecord` under `go test -race` |
| Daemon start: no reading until first line | `TestClaudeUsageStore` "none" row and `TestRequestClaudeUsage` |

Session rotation, respawn and eviction do not touch the store: it is per daemon and keyed by nothing.

## Error handling

Undecodable `unifiedWindows`: Debug log with a fixed reason, no value logged, previous reading kept. Malformed request payload: `protocol.malformed`, not retryable. Marshal failure of the reply is returned as the handler error, as in the precedent.

## Testing strategy

- streamsup: committed `allowed` line from `subagent_prompt_v2.1.280.json` → handler gets 0.44/1790367600 and 0.64/1790575200 and `rate_limited` is not emitted; one window absent; zero utilization stays `0`; utilization > 1 and a past `resetsAt` pass unchanged; no `unifiedWindows` / `null` / `{}` after a reading → handler not called; malformed `unifiedWindows` on a non-benign line → handler not called and `RateLimited` still emitted exactly as before.
- cmd/pyry: store returns `none` before any record; `observed` with `observed_at` between before/after timestamps; absent window absent from the payload; concurrent record/read under `-race`.
- protocol: payload JSON shape — absent window omitted, `0` kept, `none` has only `state`.
- handlers: one requester-correlated reply; malformed payload → error; nil source → `none`.
- handshake: `negotiateCapabilities` echoes `claude_usage` only when advertised.
- Closed lists in `compat_test.go`, `relay_guard_test.go`, `handshake_test.go` classify the new constants.

## Open questions

- Frame names: chosen `request_claude_usage` / `claude_usage` and capability `claude_usage`, mirroring `claude_account`.

## Documentation handoff

Pending for the documentation stage:

- `docs/protocol-mobile.md`: new section next to § Claude account source documenting `request_claude_usage` / `claude_usage`: every field (`state`, `observed_at`, `five_hour`, `seven_day`, each window's `utilization`, `resets_at`); the reading covers the whole account and is held per daemon instance; `observed_at` is when the daemon read claude's line and the reading is never refreshed without a model call; no reading (`state: "none"`) until the first model call after daemon start; the same unvalidated-number and absent-versus-zero warnings as § `rate_limited`, including that `utilization` has been observed as a 0-to-1 fraction.
- `docs/protocol-mobile.md` § Capability negotiation (v2): add `claude_usage` as detection-only.
