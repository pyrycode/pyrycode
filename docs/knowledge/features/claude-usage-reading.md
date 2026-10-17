# Claude usage allowance reading — latest 5-hour/weekly windows, answered on request (#3575)

The daemon keeps the latest 5-hour and weekly Claude usage allowance reading
it has seen on any Claude session and answers it on request, so a client can
show both windows at any time instead of only when a warning fires. Wire
contract: [`docs/protocol-mobile.md` § Claude usage allowance
reading](../../protocol-mobile.md#claude-usage-allowance-reading). Sibling
\#3576 (pushing each new reading to open clients) is blocked on this ticket
and not yet built.

## Shape

- `internal/streamsup/parser_usage_reading.go`'s `UsageWindow{Utilization
  *float64; ResetsAt *int64}` is one of claude's windows, verbatim. Both
  fields are pointers so a key claude's line omitted stays distinguishable
  from a reported `0`.
- `(*Parser).SetUsageWindowsHandler(h func(fiveHour, sevenDay *UsageWindow))`
  installs the destination for every reading the parser decodes; a seam in
  the shape of `SetCanUseToolHandler`, installed before the parser is handed
  to a runner. `nil` (the default) discards readings.
- `(*Parser).readUsageWindows` decodes `rate_limit_info.unifiedWindows` off
  every `rate_limit_event` line through its own decode target
  (`usageWindowsLine`), separate from `rateLimitEventLine`'s whole-line
  decode. It calls the handler only when decoding succeeds and at least one
  window is present; a line with no `unifiedWindows`, a `null`/empty one, or
  one that fails to decode leaves the handler uncalled, so whatever reading
  the consumer already holds stands. A decode failure logs one fixed,
  content-free Debug line (`usageWindowsDropMsg`) and never the line itself.
- `cmd/pyry/claude_usage.go`'s `claudeUsageStore` is the daemon-wide store:
  one mutex guarding the latest `fiveHour`/`sevenDay` pair plus
  `observedAt` (daemon wall clock, UTC, stamped in `record`). The zero value
  is "no reading" — a daemon starts with none. `record` replaces the stored
  reading whole, on every call; a `nil` window argument means that line
  omitted that window, and the new reading has no value there either, not
  the old one carried forward. `ClaudeUsage() protocol.ClaudeUsagePayload`
  satisfies `handlers.ClaudeUsageSource` and reports `state: "none"` before
  the first `record`.
- `internal/relay/handlers/claude_usage.go`'s `RequestClaudeUsage(src,
  logger) dispatch.Handler` is a copy of `RequestClaudeAccount`'s shape:
  decode `{}` (malformed → non-retryable `protocol.malformed`), reply once
  with `TypeClaudeUsage` via `c.Reply`. A `nil` source answers `state:
  "none"`, the same as a daemon with no reading.
- Wiring: `streamApprovalConfig` (`cmd/pyry/streamsup_runner.go`) gained a
  `usage *claudeUsageStore` field; `newStreamRunnerFactory` installs
  `parser.SetUsageWindowsHandler(approval.usage.record)` when it is
  non-nil. `runSupervisor` (`cmd/pyry/main.go`) mints one `claudeUsageStore`,
  passes it into the approval config's `usage:` field and into
  `relayWiring`'s `claudeUsage:` field; `internal/relay/handlers.ClaudeUsageSource`
  is what `startRelayV2` reads to register the `request_claude_usage`
  handler (`cmd/pyry/relay.go`). Only the Claude runner factory installs the
  handler — Codex sessions use a different factory and never update the
  store.
- Wire types live in `internal/protocol/claude_usage.go`: see [Claude usage
  payloads](protocol-package-types-claude-usage-payloads.md) for the field
  shapes, `omitempty` behaviour and the capability string.

## Why the parser needs a second, independent decode

`emitRateLimit`'s existing decode target, `rateLimitEventLine`, is a
whole-line decode: a field that fails to parse fails the whole line and
takes the "undecodable" arm, which suppresses `rate_limited` for that line.
Declaring `unifiedWindows` on that same target would mean a malformed
windows object — bytes `rate_limited` itself never looks at — could silently
suppress a `rate_limited` emission that fires today. `readUsageWindows` is
therefore a wholly separate `json.Unmarshal` against its own target
(`usageWindowsLine`), called first inside `emitRateLimit` but unable to
affect anything downstream of it: a malformed `unifiedWindows` on a
non-benign line still emits `rate_limited` exactly as before, and this is
what `TestParser_UsageWindowsMalformedLeavesRateLimitedAlone` pins. The
general rule: when a new read must not be allowed to change an existing
read's success/failure outcome, decode it separately, even against the same
line, rather than widening the existing target.

## A `_windows.go` file suffix is read as a GOOS build constraint

The plan named the new streamsup file `parser_usage_windows.go`. Go's build
system treats a `_GOOS.go` filename suffix as an implicit platform
constraint — `_windows.go` means "Windows only" — so that file would have
compiled out of every build on Linux and macOS, the project's only two
supported platforms (Windows is out of scope). The file actually landed as
`internal/streamsup/parser_usage_reading.go`. No naming convention in this
codebase flags this ahead of time; the way it surfaces is the new code
silently never building, with no compiler error naming the cause. Any new
file whose last underscore-separated token would read as a GOOS or GOARCH
value (`windows`, `linux`, `darwin`, `amd64`, `arm64`, …) needs a different
name, not a build tag override.

## The capability constant lives beside the frames it detects, not in `handshake.go`

`CapabilityClaudeUsage` is declared in `internal/protocol/claude_usage.go`,
next to `TypeRequestClaudeUsage`/`TypeClaudeUsage`, rather than in
`internal/protocol/handshake.go` where `CapabilityStopBackgroundTask` sits.
`CapabilityThreadCompact` set the precedent this ticket followed: a
detection-only capability string is wire vocabulary for the feature it
detects, so it belongs in that feature's own file. `handshake.go` holds the
negotiation machinery (`supportedV2Capabilities`, `negotiateCapabilities`),
not every capability string that machinery echoes.

## Concurrency: the reply copies values out from under the lock, it does not hand out the stored pointers

The security review's one finding: `claudeUsageWindow`, as first written,
copied the store's `*float64`/`*int64` pointers straight into the payload it
returned. A caller holding that payload outside `claudeUsageStore`'s mutex —
for example a future push consumer like #3576 — could write through those
pointers and mutate the stored reading without ever taking the lock, racing
`record`. The fix copies each value into a freshly allocated pointer
(`copyOf[T any](v *T) *T`) so the returned `ClaudeUsagePayload` shares no
memory with what `claudeUsageStore` holds.
`TestClaudeUsageStore_ReplySharesNothing` mutates through a returned
reply's pointers and asserts a second read is unaffected. Otherwise this is
an ordinary leaf lock: one `sync.Mutex` guards the windows and `observedAt`
together so a reply never pairs one line's windows with a different line's
time, no I/O runs under it, and it takes no other lock. The handler is
installed before `streamsup.New`, before any forwarder goroutine starts, so
no goroutine is added by this feature.

## Production wiring needs its own test, not just the store's unit tests

`TestClaudeUsageStore` exercises `claudeUsageStore` directly by installing
`store.record` on a bare parser; it says nothing about whether
`newStreamRunnerFactory` actually wires that installation into a real
runner, or whether `runSupervisor`/`startRelayV2` actually connect the one
store instance to both the parser-handler side and the relay-handler side.
Two tests close that gap and are written to fail if either wiring line is
deleted: `TestStreamRunnerFactory_RecordsClaudeUsage` runs a real runner
built by `newStreamRunnerFactory(..., streamApprovalConfig{usage: store})`
against a fake child that prints the committed `allowed` line and asserts
the store observes it; `TestClaudeUsageWiredToTheCompositionRoot`
(`relay_guard_test.go`) pins `runSupervisor`'s single store mint together
with its `usage:` and `claudeUsage:` assignments and `startRelayV2`'s
handler reading `w.claudeUsage`. See [Development verification § test
execution](development-verification-test-execution-and-artifact-survival.md)
for why a unit test of an injected dependency is not evidence the daemon's
own composition root actually injects it.

## Related

- [Claude usage payloads](protocol-package-types-claude-usage-payloads.md) —
  the `request_claude_usage` / `claude_usage` wire pair and
  `CapabilityClaudeUsage`.
- [`docs/protocol-mobile.md` § Claude usage allowance
  reading](../../protocol-mobile.md#claude-usage-allowance-reading) — the
  published client-facing contract.
- [streamsup-package.md § Usage allowance windows](streamsup-package.md#usage-allowance-windows-3575) —
  the parser-side seam this store is installed on.
- [Rate-limited event payload § the independent decode](protocol-package-rate-limited-event-payload.md) —
  why `readUsageWindows` cannot share `rateLimitEventLine`'s decode target.
- [claude-account-source.md](claude-account-source.md) — the nearest
  precedent for an account-wide, per-daemon-instance reading answered by
  authenticated paired-client map dispatch with no conversation lookup and
  no `interactive` gate.
- `docs/specs/architecture/3575-claude-usage-reading.md` — the architect
  plan, including the full security review and its one SHOULD FIX (the
  copy-not-share fix above).
