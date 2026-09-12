# #2374 — Source MCP status only from strict-config children

## Files read

- `internal/streamsup/runner.go` → `Config`, `New`, `beginSpawn`, `spawnAndWait`, `RequestInitialize`, `nextControlID` — owns the immutable daemon-config path, the per-spawn argv snapshot, child-start ordering, and the shared control-request ID sequence.
- `internal/streamsup/parser.go` → `Parser`, `NewParser`, `consumeLine`, `emitModelList`, `decodeMCPStatus`, `emit` — owns inventory recognition, MCP status decoding, and the last boundary before the shared event sink.
- `internal/streamsup/envelope.go` → `WriteMCPStatus` — existing content-free request writer that the runner method will reuse.
- `internal/streamsup/interface_test.go` → `TestRunner_RequestInitializeOnSpawn_ReplacementChild`, `helperRunCfg`, `initializeAsks` — per-child replacement and control-request test patterns.
- `internal/streamsup/helper_test.go` → `helperChild` — child-process lifecycle modes used to prove spawn snapshots without a real Claude process.
- `internal/streamsup/mcp_status_event_test.go` → `TestParser_MCPStatusShapeGate`, `mcpStatusLineFixture`, `collectMCPStatuses` — existing decoder contract that standalone parsers must retain.
- `cmd/pyry/streamsup_runner.go` → `newStreamRunnerFactory`, `withApprovalArgs`, `mapStreamsupConfig` — the factory knows the daemon MCP-config path and constructs the final base argv before `streamsup.New`.
- `cmd/pyry/streamsup_runner_test.go` → `TestNewStreamRunnerFactory`, `TestWithApprovalArgs` — factory and strict-config argv proof patterns.
- `docs/knowledge/features/streamsup-package.md` → “Turn I/O — envelope write + stdout parser” — current parser lifetime, request-correlation, and MCP status decoder boundaries.
- `docs/knowledge/features/development-verification.md` → “Establish the change surface” and “Prove that tests distinguish the change” — requires construction-site tracing and independent proof of combined predicates.
- `docs/specs/architecture/1839-initialize-ask-per-child.md` → `RequestInitializeOnSpawn` design — nearest per-child request analogue and the reason inventory must not be inferred from `system/init`.

## Context

The parser can currently turn any successful, shape-matching MCP status reply into a bounded `turnevent.MCPStatus`, while the interactive daemon asks every child for its initialize inventory. A strict daemon-configured child can reveal only the daemon’s own MCP servers; a stored- or operator-supplied bypass child can inherit private user and project servers. The automatic status request and its event therefore need one per-spawn provenance decision shared across both halves of the round trip.

This does not warrant an ADR. It applies the existing rule that spawn behavior is derived from the argv snapshot in `beginSpawn`, while event admission belongs before the shared sink in `Parser.emit`.

## Design

### Per-spawn eligibility

`Config` gains `MCPStatusConfigPath string`. Empty means no child is eligible. The interactive factory sets it to the same daemon-global path passed to `withApprovalArgs`; other runner construction sites retain the zero-value behavior.

`mcpStatusEligible` examines the complete argv passed to `exec.CommandContext`, not `Config.Args`, the current permission label, or future spawn settings. It returns true only when:

- the configured daemon path is non-empty;
- the argv contains exact `--strict-mcp-config`; and
- exactly one `--mcp-config` occurrence is present and its separate or joined value equals the configured daemon path.

Requiring the sole config occurrence to match fails closed if duplicate operator flags could make the child load anything beyond the daemon document. Dangling flags, another path, missing strict mode, and empty configured paths are ineligible.

`spawnAndWait` installs the eligibility snapshot on the concrete parser before `cmd.Start`. This is the last point with the exact child argv and the only ordering that prevents fast child output from being classified under stale state. A failed start may leave a prepared snapshot, but emits no bytes; the next attempt overwrites it before its own start.

### Parser policy

`Parser` gains per-child policy state: whether a runner installed a child policy, whether that child is eligible, whether its one request attempt has fired, and the request callback. Standalone parsers have no installed child policy: they continue exposing decoded MCP status for decoder tests and non-runner consumers.

`beginMCPStatusChild` replaces all four values before each child starts. `Parser.emit` then applies two rules:

1. A decoded `turnevent.MCPStatus` is dropped when a child policy is installed and that child is ineligible. The check occurs before `p.sink`; all non-status event values are forwarded unchanged.
2. The first `turnevent.ModelList` or `turnevent.SlashCommandList` from an eligible child marks the request attempted and calls the request callback. Marking precedes the call, so a reply containing both inventories still triggers once and a failed delivery is not retried by later inventory events. The next child resets the latch.

The existing emitters already supply the correct trigger semantics: they emit inventory only from a successfully decoded initialize-shaped reply with a non-empty model or command array. Empty, absent, rejected, and undecodable inventories emit no trigger event and need no new initialize-complete type.

### Request path and wiring

`Runner.RequestMCPStatus` mints from `nextControlID` and delegates to `WriteMCPStatus(r.Stdin(), id)`. It remains off `sessions.Runner`; its only consumer is the parser bound to this concrete runner.

`New` retains the concrete parser already discovered from `Config.Stdout` for context-usage correlation. The field is renamed from the context-specific name because it now owns both parser integrations. `spawnAndWait` passes `r.RequestMCPStatus` to that parser’s per-child reset.

`newStreamRunnerFactory` assigns `scfg.MCPStatusConfigPath = mcpServersPath` beside the final `withApprovalArgs` result. This keeps the path and the argv composer in one construction window and does not ask `mapStreamsupConfig` to carry a factory-only runtime value.

```text
beginSpawn snapshots args
  -> spawnAndWait classifies args against MCPStatusConfigPath
  -> Parser begins child state before cmd.Start
  -> successful initialize reply emits first inventory
  -> Parser marks attempted and calls Runner.RequestMCPStatus once
  -> child reply decodes to MCPStatus
  -> Parser drops it if ineligible, otherwise forwards it unchanged
```

## Concurrency model

No goroutines or locks are added. `beginMCPStatusChild` runs on the supervisor’s Run goroutine before `cmd.Start`; parser writes begin only from the `os/exec` stdout forwarder created after that call. `cmd.Wait` does not return until the forwarder finishes, so the next spawn’s reset cannot race the prior child’s parser writes. These start/wait edges preserve the parser’s existing single-writer model across replacements.

The request callback runs on the stdout forwarder while no parser or runner lock is held. It performs one small stdin write. The attempted latch is set first, so callback failure and any later inventory cannot duplicate the request.

Shutdown is unchanged. A request racing child exit may return `ErrNoLiveChild` or a wrapped pipe error; neither reaches `spawnAndWait`, its `waitErr`, or the backoff decision.

## Error handling

- Unsupported or ambiguous argv is ineligible and produces no diagnostic; it is an expected privacy decision.
- Failure to deliver the automatic status request writes one fixed Debug message with a daemon-authored event name only. The error, request ID, request bytes, response bytes, server names, and status values are not logged.
- A nil request callback is treated as an unavailable automatic request and does not panic; production installs it before `Run` can start.
- Existing MCP response decode failures remain silent except for `logControlResponse`’s fixed, content-free classification.

## Testing strategy

Tests are hermetic and stay in `internal/streamsup` and `cmd/pyry`.

- Table-test `mcpStatusEligible` with the exact daemon pair, missing strict mode, missing/wrong/dangling config, empty daemon path, joined config syntax, and duplicate config occurrences.
- Drive a parser configured for an eligible child with a successful reply carrying both model and command inventories. Assert no earlier request, exactly one request from that line, no repeat on later inventory, and a fresh request after the replacement-child reset.
- Drive an ineligible child through inventory, MCP status, and ordinary event variants. Assert zero requests, MCP status absent before the sink, and the other event values unchanged. Assert the eligible and standalone-parser status paths still forward.
- Make the request callback fail with a distinctive secret-bearing error. Assert the inventory still forwards, later inventory does not retry, and captured logs contain neither the sentinel nor request/response content.
- Use the helper child plus `onSpawn` to prove `spawnAndWait` installs eligibility from each actual argv: changing next-spawn args does not affect the live child, while a restarted child receives the new ineligible snapshot.
- Extend the factory construction test to assert the daemon path reaches `streamsup.Config`/the runner policy without changing bypass argv composition.

Verification after GREEN:

- `go test -race ./internal/streamsup/... ./cmd/pyry/...`
- `go vet ./...`
- `go build ./cmd/pyry`

## Open questions

- None. The only syntax choice is resolved fail-closed: multiple MCP config occurrences are ineligible even if one names the daemon document.

## Documentation handoff

Pending for the documentation stage: update `docs/knowledge/features/streamsup-package.md`, in “Turn I/O — envelope write + stdout parser”, to document that automatic MCP status requests are triggered once per eligible child by its first initialize inventory event, eligibility comes from that child’s actual strict daemon-config argv, and ineligible `MCPStatus` events are suppressed before the shared sink. No builder edit to the shared knowledge document.

## Scope re-check

- Deliverables: 1 — the per-child MCP status provenance gate covers request and publication as one privacy behavior.
- Production source files: 3 — `internal/streamsup/runner.go`, `internal/streamsup/parser.go`, `cmd/pyry/streamsup_runner.go`.
- Total written work: approximately 520 lines including tests and this plan, within 800.
- New exported types or interfaces: 0.
- Consumer call sites requiring simultaneous update: 1 — `newStreamRunnerFactory`.
- Acceptance criteria: 4.
- Distinct reject/error branches: 4 — missing/ambiguous argv, ineligible status suppression, nil requester, request delivery failure.

The refiner estimated two production files. Reading the current seam adds `parser.go` because the trigger and pre-sink suppression are both parser-owned; the total scope and every quantitative ticket boundary remain within the S ticket.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] No findings — subprocess stdout crosses into bounded event types only through `consumeLine`, `emitModelList`, and `decodeMCPStatus`; the new trust decision is centralized in `mcpStatusEligible` and enforced at `Parser.emit` before the shared sink.
- [Tokens, secrets, credentials] No findings — no credential or authorization token is created or retained. `nextControlID` remains a correlation value, not a secret, and neither it nor the daemon-config path is logged.
- [File operations] No findings — the daemon MCP-config file lifecycle is unchanged. `MCPStatusConfigPath` is compared to argv only; this work does not open, stat, create, follow, or rewrite the path.
- [Subprocess / external command execution] No findings — `exec.CommandContext`, environment inheritance, signals, and teardown are unchanged. Eligibility reads the immutable argv slice already selected for that exact spawn and adds no shell interpretation or argument.
- [Cryptographic primitives] Not applicable — the policy performs no cryptography, randomness, hashing, or secret comparison.
- [Network & I/O] No findings — automatic output is bounded by the existing parser buffer and `decodeMCPStatus` limits. At most one small request attempt is added per eligible child; the attempted latch is set before I/O, including on failure.
- [Error messages, logs, telemetry] No findings — the new failure diagnostic intentionally omits the returned error as well as all request, response, path, server, and status content. It carries only a fixed message and fixed event name.
- [Concurrency] No findings — no goroutine or lock is added. The pre-`cmd.Start` policy write and post-`cmd.Wait` replacement ordering preserve the parser’s single-writer lifecycle, and delivery failure cannot reach supervision state.
- [Threat model alignment] No findings — the paired client remains untrusted, and the policy removes its route to a bypass child’s private MCP inventory before any shared turn sink or mobile mapping. Manual MCP control and later publication behavior remain outside this ticket.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-12
