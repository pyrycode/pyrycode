# #2261 — carry per-turn token counts on `turn_end`

## Files read

- `internal/streamsup/parser.go` → `streamLine`, `resultLine`, `resultStopLine`,
  `resultDenialsLine`, `resultTurnTotalsLine`, `decodeTurnTotals`, and the `result` arm of
  `Parser.consumeLine`. These establish segmentation-only decoding, independent result-line
  targets, signed numeric forwarding, and the single construction point for `turnevent.TurnEnd`.
- `internal/streamsup/result_turn_totals_test.go` → `totalsFragments`,
  `capturedResultLines`, `TestParser_ResultTurnTotals_IsolatedFromItsSiblings`, and the three
  capture pins. The merged #2260 harness already reads both committed capture layouts and is the
  nearest pattern for synthetic isolation plus replay evidence.
- `internal/streamsup/parser_test.go` → `TestStreamLine_StaysSegmentationOnly`. The new nested
  shape must remain outside the line-level segmentation target.
- `internal/turnevent/event.go` → `TurnEnd`. This is the neutral event contract shared by the
  parser and outbound adapter, including the provenance and no-actuation posture for existing
  result-line numbers.
- `internal/turnbridge/outbound.go` → the `turnevent.TurnEnd` arm of `MapEvent`. It constructs
  `protocol.TurnEndPayload` field by field and is therefore the only mapping site to extend.
- `internal/turnbridge/outbound_test.go` → the `TurnEnd` rows in `TestMapEvent`. Distinct values
  there prove that the adapter neither crosses nor derives fields.
- `internal/protocol/interactive.go` → `TurnEndPayload`. It defines the stable wire names and the
  rule that optional numeric keys remain plain, always-emitted values without `omitempty`.
- `internal/protocol/interactive_test.go` → `TestTurnEndPayload_RoundTrip` and
  `TestTurnEndPayload_TurnTotalsAreOptional`; `internal/protocol/testdata/turn_end.json` → the
  golden envelope. Together they pin both exact output and backward-compatible zero decoding.
- `docs/knowledge/features/streamsup-package.md` and its system-map child → the result-line
  isolation rule and the warning that a hostile field type fails an entire decode target.
- `docs/knowledge/features/protocol-package.md` and its interactive-payload child → the payload
  boundary and client-facing provenance rules.
- `docs/knowledge/features/development-verification.md` → capture replay and protocol-fixture
  proof requirements.
- `docs/specs/architecture/2260-turn-end-turn-totals.md` → the nearest shipped design, including
  the reusable capture readers, explicit overage treatment, and security analysis for numeric
  fields sourced from `claude`.
- `docs/protocol-mobile.md` § `turn_end` → the documentation-stage destination for the token-side
  interpretation required by this ticket.

## Context

Every `claude` result line carries a nested `usage` object with four counts for the turn:
uncached input, output, cache-read input, and cache-creation input. The daemon already treats the
line as the turn boundary but drops those counts, forcing clients to inspect transcript content to
show a terminal-style per-turn token reading.

The four values form one contract. Three are input-side and one is output-side; publishing only a
subset would invite the specifically incorrect `input_tokens + output_tokens` total and leave the
sole consumer contract incomplete. This is therefore one deliverable despite four plumbing hops.

The one-ticket boundary holds on five dimensions: 4 production files, no new exported type or
interface, no changed call-site contract, 4 acceptance criteria, and one nested-decode failure
path. Total written work may exceed the nominal 800-line ceiling, as the refiner forecasts about
980 lines. Splitting cannot create an independently useful child: a parser-only event field has no
consumer outside its sibling, and a wire-only field has no producer. Under the pipeline floor rule,
the one-consumer slices remain together. The implementation will keep the fixed cost below #2260
by reusing its capture readers and extending existing protocol and bridge tables.

No ADR is warranted. This applies the established result-line isolation and additive wire-field
rules without changing either decision.

## Design

### Independent nested decode

Add a fifth result-line decode target beside `resultLine`, `resultStopLine`,
`resultDenialsLine`, and `resultTurnTotalsLine`:

```go
type resultTurnUsageLine struct {
    Usage resultTurnUsage `json:"usage"`
}

type resultTurnUsage struct {
    InputTokens         int `json:"input_tokens"`
    OutputTokens        int `json:"output_tokens"`
    CacheReadTokens     int `json:"cache_read_input_tokens"`
    CacheCreationTokens int `json:"cache_creation_input_tokens"`
}

func decodeTurnUsage(line []byte) (input, output, cacheRead, cacheCreation int)
```

The wrapper keeps `usage` out of `streamLine`, preserving segmentation. Its own unmarshal means a
hostile usage container or wrong-typed count zeros only this four-count group; model windows, stop
shape, recovered denials, turn totals, and the turn boundary continue through their independent
targets. Within `usage`, one wrong-typed count fails the target as a unit and returns four zeros.
An absent or null `usage` produces the nested struct's zero value. A single absent or null member
does not fail decoding, so that member is zero while valid siblings survive.

All fields use signed `int`, matching the existing `claude`-number posture. The daemon performs no
sum, conversion, clamp, range validation, or ordering check. Keys outside the four-field nested
allowlist are ignored.

### Event and wire propagation

`turnevent.TurnEnd` gains `InputTokens`, `OutputTokens`, `CacheReadTokens`, and
`CacheCreationTokens`. Their shared documentation attributes the readings to `claude`, states that
they describe one turn, identifies the three input-side values, and makes clear that
`InputTokens` is uncached input only. These are observations only: no daemon routing, budgeting,
retry, or lifecycle decision reads them.

The result arm calls `decodeTurnUsage` alongside the four existing independent decodes, then copies
the values onto the one `TurnEnd` emission. Ordering and parser resets do not move.

`protocol.TurnEndPayload` gains the same Go field names with wire keys `input_tokens`,
`output_tokens`, `cache_read_tokens`, and `cache_creation_tokens`. The shortened cache wire names
are deliberate; only the decode target uses `claude`'s longer `*_input_tokens` keys. The fields
have no `omitempty` and no pointer representation, so old frames decode to zeros and new frames
always emit all four keys.

`MapEvent` copies the four values directly from the neutral event to the payload. It derives and
normalizes nothing.

## Concurrency model

No concurrency changes. `decodeTurnUsage` is a pure function over one byte slice. It runs in the
parser's existing single-reader flow, adds no state, goroutine, channel, lock, or shutdown path,
and feeds the existing synchronous event emission.

## Error handling

`decodeTurnUsage` returns four zeros when `json.Unmarshal` rejects the wrapper or any declared
nested value. It does not log or return the decode error because `encoding/json` error text can
quote hostile subprocess bytes. Absent and null containers, and absent or null individual counts,
decode as zero without an error. The independent targets ensure no token failure affects any
existing result-line field or segmentation.

JSON numeric overflow is another nested-decode failure and receives the same all-zero result.
Valid negative and platform-range values pass through unchanged. Since JSON cannot represent NaN
or infinity, accepted values remain marshalable on the outbound wire.

## Testing strategy

- Add `internal/streamsup/result_turn_usage_test.go` with four distinct synthetic counts to prove
  key spelling and field identity, plus signed negative values to prove the no-clamp posture.
- Table absent, null, hostile container types, and one wrong-typed count. Assert all four zero on a
  target failure; table one absent or null member and assert its valid siblings survive.
- On every hostile row, assert the existing stop shape, totals, model windows or recovered denial
  that shares the line still survives. Add reverse-isolation rows where a hostile sibling shape
  leaves good usage intact. Keep `TestStreamLine_StaysSegmentationOnly` unchanged and green.
- Reuse `capturedResultLines` for
  `bypass_reescalation_v2.1.239_control_bypass.json`: assert its three exact usage tuples and the
  observed non-monotonic output/cache-creation sequence. This distinguishes per-turn readings from
  `total_cost_usd`'s running-total behavior. Reuse the `frames` reader shape from
  `TestParser_ResultTurnTotals_CapturePinZeros` for `compaction_v2.1.259.json` and assert its real
  all-zero usage object. Pin the `error_max_turns` line from
  `permission_mode_switch_v2.1.239_plan.json` so the mapping is not evidenced only by `success`.
- Extend the `TurnEnd` mapping row in `internal/turnbridge/outbound_test.go` with mutually distinct
  values and expected payload fields.
- Extend `TestTurnEndPayload_RoundTrip` and the golden `turn_end.json` with capture-derived values.
  Add the four fields to the legacy-frame zero assertion, proving backward-compatible decoding,
  and assert marshaling a zero-value payload still emits the keys.
- Run `go test -race ./internal/streamsup/... ./internal/turnevent/... ./internal/turnbridge/...
  ./internal/protocol/...`, then `go vet ./...`, then `go build ./cmd/pyry`.

## Open questions

None. The committed captures settle key names, numeric types, zero behavior, and per-turn semantics;
the ticket explicitly leaves subagent inclusion unspecified.

## Documentation handoff

Pending for the documentation stage: update `docs/protocol-mobile.md` § `turn_end` to add
`input_tokens`, `output_tokens`, `cache_read_tokens`, and `cache_creation_tokens`; state that all
four are per turn, that the input, cache-read, and cache-creation counts are input-side, and that
`input_tokens` counts only uncached input, so adding it to `output_tokens` is not the turn total.
Preserve the shortened cache wire names. Do not claim whether subagent usage is included or excluded
unless attributing that statement to `claude`'s documentation rather than daemon measurement.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] No findings. `decodeTurnUsage` is the single explicit crossing from hostile
  subprocess stdout into typed values; the downstream `TurnEnd` and `TurnEndPayload` docs retain
  provenance by attributing every count to `claude`. No value becomes a daemon assertion.
- [Tokens, secrets, credentials] Not applicable. These are numeric token *counts*, not bearer
  tokens, credentials, keys, or secret material. The change neither reads nor stores credentials.
- [File operations] Not applicable. Production code opens no file and constructs no path. Tests
  only replay repository-named committed captures through the existing `capturedResultLines`
  pattern.
- [Subprocess / external command execution] No findings. Values flow out of the already-running
  `claude` child and are never used in an argument, environment variable, command, signal, or spawn
  decision.
- [Cryptographic primitives] Not applicable. No randomness, hashing, key material, nonce, secret
  comparison, or cryptographic operation changes.
- [Network & I/O] No findings. Four `int` values add bounded wire growth independent of hostile
  input length; each renders within the platform integer width. No socket read, connection,
  timeout, TLS setting, or envelope size policy changes.
- [Error messages, logs, telemetry] No findings. `decodeTurnUsage` discards decode errors and logs
  no subprocess bytes. The counts join the existing paired-client `turn_end` payload and no new
  telemetry destination is introduced.
- [Concurrency] No findings. The decode is stateless and synchronous in `Parser.consumeLine`; it
  adds no shared state, lock, goroutine, or shutdown behavior.
- [Threat model alignment] No findings. A hostile `usage` can at worst produce signed numeric
  display data or the documented zero fallback. No daemon behavior, especially budget or spend
  enforcement, acts on these counts. Misattribution is addressed by keeping `claude` provenance
  explicit, and numbers cannot carry terminal escapes, markup, URLs, or control characters that
  would create an additional client sanitization obligation.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-10

## Revisions

None.
