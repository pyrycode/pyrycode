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

## Revisions

- 2026-10-17, build: the streamsup file is `internal/streamsup/parser_usage_reading.go`, not `parser_usage_windows.go`. Go reads a `_windows.go` suffix as a GOOS build constraint, so the planned name compiled out on Linux and macOS. No contract changed.
- 2026-10-17, build: `CapabilityClaudeUsage` is declared in `internal/protocol/claude_usage.go` beside the frames it detects, as `CapabilityThreadCompact` sits in `thread_compact.go`, rather than in `handshake.go`. Same string and contract.
- 2026-10-17, rework (verifier finding 1, MUST FIX): the issue carries `security-sensitive`, and the plan was committed without the pass. Added `## Security review` below. Its one SHOULD FIX changes `claudeUsageStore.ClaudeUsage`'s contract: `claudeUsageWindow` copies each value into a fresh pointer instead of handing out the stored ones, so a payload held outside the lock shares nothing with the stored reading. `TestClaudeUsageStore_ReplySharesNothing` covers it.
- 2026-10-17, rework (verifier findings 2 and 3, SHOULD FIX): the production wiring had no test. `TestStreamRunnerFactory_RecordsClaudeUsage` runs a real runner from `newStreamRunnerFactory(..., streamApprovalConfig{usage: store})` against a fake child that prints the committed `allowed` line, and `TestClaudeUsageWiredToTheCompositionRoot` in `relay_guard_test.go` pins `runSupervisor`'s single store mint, its `usage:` and `claudeUsage:` assignments, and `startRelayV2`'s handler reading `w.claudeUsage`. Deleting any one of those four lines fails one of the two tests. `TestClaudeUsageStore`'s comment no longer claims factory wiring it does not exercise.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] No findings. One boundary: claude's stdout reaches daemon state only through `readUsageWindows`, which decodes into `usageWindowsLine` and hands on `*UsageWindow` values whose only fields are `*float64` and `*int64`. No claude-authored string crosses it, and nothing downstream (`claudeUsageStore`, `protocol.ClaudeUsageWindow`) has a field that could hold one. The decode is separate from `rateLimitEventLine`, so this boundary cannot change what the `rate_limited` path trusts or emits. Only Claude sessions cross it: `newStreamRunnerFactory` is the sole installer of `SetUsageWindowsHandler`, and Codex runners never go through it.
- [Trust boundaries, input bounds] No findings. `encoding/json` bounds every stored value: a number outside float64 range for `utilization`, a fraction or out-of-range value for `resetsAt`, or a wrong type anywhere under `unifiedWindows` makes `json.Unmarshal` return an error, and `readUsageWindows` returns before the handler on any error, so a partly decoded window is never stored. JSON has no NaN or Inf, so every stored float is finite and `json.Marshal` of the reply cannot fail on it. Negative, past and above-1 values pass through by design (AC 3); clients are warned in the documentation handoff. The stored reading is fixed-size whatever the line's size, and the line itself is already held to the parser's `defaultMaxParseBuf` partial-line cap. `encoding/json` matches keys case-insensitively and keeps the last of duplicate keys, the same as the existing `rateLimitEventLine` decode, so a key spelled `Five_Hour` would be read; that changes which of claude's own numbers is kept, not what kind of data crosses.
- [Tokens, secrets, credentials] No findings. The reading holds no credential. The handler reads only `ClaudeUsageSource` and never touches the account token provider, and nothing in the change logs a payload.
- [File operations] No findings. The store is memory only: `claudeUsageStore` has no path and nothing persists it, so a daemon starts with no reading (AC 2).
- [Subprocesses] No findings. No argv, environment or child lifecycle changes. The reading comes off the existing claude child's stdout.
- [Cryptography] No findings. The reply travels over the existing Noise_IK session through `c.Reply`. No key, nonce or comparison is added.
- [Network and I/O] No findings. No new socket read. The request payload reaches `RequestClaudeUsage` only after the v2 application-envelope cap (65519 bytes) and decryption; it decodes into an empty struct, and anything that does not decode as a JSON object or null gets the non-retryable `protocol.malformed` reply. Each request costs one mutex hold and a fixed-size marshal, with no model call and no I/O under the lock, so a paired client repeating it costs no more than any other map-dispatched request. Request rate limiting is threat 7 in `docs/protocol-mobile.md` § Security model, deferred project-wide.
- [Errors, logs, telemetry] No findings. `readUsageWindows`' one log line is Debug with the fixed `usageWindowsDropMsg` and the fixed reason `undecodable`; it deliberately does not log `err`, because `encoding/json`'s `UnmarshalTypeError` quotes the offending number. The malformed-request Warn in `RequestClaudeUsage` logs only the fixed event name and `conn_id`, and the client gets the fixed message "malformed request_claude_usage payload".
- [Concurrency] SHOULD FIX: `claudeUsageWindow` as first built copied the store's `*float64` and `*int64` into the payload, so a caller holding the payload outside the lock, such as #3576's push, could write through them into the stored reading and race `record`. Copy each value into a fresh pointer; `TestClaudeUsageStore_ReplySharesNothing` checks it. Otherwise one `sync.Mutex` guards the windows and `observedAt` together, so a reply never pairs one line's windows with another line's time. It is a leaf lock with no I/O under it and no other lock taken inside it. Concurrent `record` calls are last-writer-wins by lock order, and each stored reading keeps its own observation time. The handler is installed before `streamsup.New`, before any forwarder goroutine starts. No goroutine is added.
- [Threat model] No findings. Authorization is the same as `request_claude_account`: any paired client that completed the hello in `handleNoiseInit` may ask; an unpaired token is closed with `StatusUnauthorized` before any app frame is dispatched. There is no conversation lookup and no `interactive` gate, because the reading belongs to the account, not to a conversation. A paired device learns the account's allowance use and, through `observed_at`, when the daemon last saw a model call on any Claude session. Paired devices can already list every conversation and its activity, so this adds no exposure class, and per-device revocation (threat 4) removes it. A malicious relay sees only ciphertext and frame size (threat 3); the reply's size varies only with whether each window is present.

**Reviewer:** builder (self-review per the security-review checklist, added at rework after verifier finding 1)
**Date:** 2026-10-17
