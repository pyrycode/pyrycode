# #2357 — decode solicited context usage into an event

## Files read

- `internal/streamsup/runner.go` → `Runner`, `RequestContextUsage`, `nextControlID` — owns request-ID minting and the write whose result decides whether a response may emit.
- `internal/streamsup/parser.go` → `Parser`, `NewParser`, `consumeLine`, `noteControlAck`, `emitModelList`, `logControlResponse` — owns the stdout trust boundary, the existing control-response classification, and the sole content-free log record.
- `internal/streamsup/context_usage_bound.go` → `boundContextUsageEntries`, `maxContextUsageEntries`, `maxContextUsageStringBytes` — supplies the shared ranking, byte rejection, ownership, and dropped-count contract from #2356.
- `internal/streamsup/context_usage_capture_test.go` → `contextUsageRead`, `contextUsageCapture` — provides the provenance-checked reader for Claude 2.1.259's two captured response arms.
- `internal/e2e/realclaude/testdata/context_usage_v2.1.259.json` → `arms[].control_responses` — is the byte-level authority for the nested response, scalar totals, model, and category values.
- `internal/turnevent/event.go` → `Event`, `ModelList` — defines the sealed daemon event contract and its bounded-list documentation pattern.
- `cmd/pyry/streamsup_runner.go` → `newStreamRunnerFactory` — confirms production installs the concrete `*streamsup.Parser` directly as `streamsup.Config.Stdout`, allowing runner/parser correlation without another composition-root seam.
- `docs/knowledge/features/streamsup-package.md` → “Turn I/O — envelope write + stdout parser” — fixes `internal/streamsup` as the decode and construction boundary.
- `docs/knowledge/features/development-verification.md` → “Prove that tests distinguish the change”, “Protocol boundaries”, “Captures and live evidence” — requires capture-derived expectations, explicit values, and branch-discriminating correlation tests.
- `CODING-STYLE.md` → “Concurrency”, “Testing”, “Comments — Citing Other Code” — requires mutex-protected shared state, race tests, table-driven coverage, and symbol citations.

## Context

`RequestContextUsage` already sends Claude's `get_context_usage` control request, and the committed 2.1.259 capture proves both supported detail values return the same response shape. The parser currently consumes those replies as ordinary successful acknowledgements. This change admits the payload only when the reply proves ownership by matching an ID that this runner registered for a context-usage request, then publishes Claude's own arithmetic in a bounded neutral event.

This is one deliverable. It modifies three production files, adds two exported event value types and no interface, changes no public signature or consumer call site, has four acceptance criteria, and has fewer than ten reject branches. Estimated written work is 600–700 lines including this plan and tests, within the refiner's estimate and the one-ticket boundary.

## Design

Add `turnevent.ContextUsage` and `turnevent.ContextUsageCategory`. The event carries `Model`, `TotalTokens`, `MaxTokens`, `Percentage`, `Categories`, and `DroppedCategories`; a category carries `Name` and `Tokens`. Integers are copied exactly. Categories are constructed through `boundContextUsageEntries`, using tokens as weight and name as the designated string, so retained values are independent, descending by token count, limited to 32, and every string rejection or count omission contributes once to `DroppedCategories`.

The scalar model is rejected above `maxContextUsageStringBytes`. This makes the complete event bounded rather than leaving one Claude-authored string unlimited. Other scalar values receive no invented arithmetic validation: the capture establishes their JSON integer types, and the contract is to forward Claude's arithmetic rather than replace `contextwindow.Read`.

Give `Parser` a mutex-protected pending context-usage registry. `New` detects the concrete parser already installed as `Config.Stdout` and binds the runner to that registry; non-parser writers remain valid but cannot establish the provenance required to emit this event. `RequestContextUsage` validates detail, mints an ID, registers it, writes the request, and resolves the registration with the write outcome. A failed write removes the exact registration.

Each registration has a write-completion signal. This closes the small concurrency window in which the child can answer after the operating-system write but before `WriteContextUsage` returns: the parser removes an exact matching ID before reading subtype or payload, waits for the write result, and proceeds only after success. A failed write therefore cannot emit even if a pathological writer delivers a response before returning its error. A successful response is one-shot because its ID has already been removed.

The `control_response` arm attempts context-usage consumption before `noteControlAck` and `emitModelList`. A minimal request-ID decode locates and retires the pending entry. Only then does a separate target decode subtype and the context payload. A non-success subtype, malformed payload, overlong model, unknown ID, and duplicate response emit nothing. `emitModelList` still runs for every line and remains the sole caller of `logControlResponse`, so every response retains exactly the existing content-free record and classification.

## Concurrency model

`RequestContextUsage` may run on any caller goroutine while `Parser.Write` runs on the child stdout goroutine. One registry mutex protects only map lookup, insertion, and deletion; it is never held across stdin I/O, JSON decoding, logging, or event emission. The per-registration completion channel publishes the write result and lets an unusually early response wait without retaining a second map entry or spawning a goroutine. The parser removes the entry before waiting and before decoding the reply, preserving one-shot semantics. No new goroutine or shutdown path is introduced.

## Error handling

Unsupported detail and request-write failures keep their existing returned errors. The write-failure path also removes its registration before publishing the failed result. Response-side failures are silent event omissions: no parser error is returned and no decode error is logged. In particular, `encoding/json` errors never reach `slog`, because they can quote Claude-authored bytes. The unchanged `logControlResponse` record remains the only record for the line and carries only daemon-authored keywords and counts unrelated to context-usage content.

## Testing strategy

- Replay each successful capture arm after registering its captured request ID through a real `Runner.RequestContextUsage` call, then compare the emitted model, totals, percentage, category names, and category token counts against independently decoded capture bytes. Assert exactly one event.
- Feed a synthetic list containing more than 32 valid categories plus overlong names, and assert the 32 heaviest valid entries in descending token order and the additive dropped count. Cover an overlong scalar model as a non-emitting boundedness case.
- Prove correlation and lifecycle with unknown, duplicate, invalid-first, error-first, and write-failed response cases. Include a writer that exposes the response before returning its write error so registration-before-write and write-result publication are tested under the actual race window.
- Capture all log records for valid, invalid, error, unknown, duplicate, and write-failed cases. Assert exactly one existing `logControlResponse` record per line and search every message and attribute for distinctive model, category, request-ID, decoder-error, and raw-response sentinels.
- Run RED before production changes, then `go test -race ./internal/streamsup/...`, `go test -race ./internal/turnevent/...`, `go vet ./...`, and `go build ./cmd/pyry`.

## Open questions

None. The capture fixes the wire shape, #2356 fixes the list policy, and production's direct parser-as-stdout wiring fixes the runner/parser binding without a new configuration surface.

## Documentation handoff

No documentation requirement was specified in the ticket. The later documentation stage should update the `internal/streamsup` and `internal/turnevent` feature references to include the new solicited `ContextUsage` event and its pending-ID ownership rule.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] No findings — `Parser` receives untrusted child stdout; an exact locally minted pending ID admits the reply, the registry retires it before payload decoding, and `boundContextUsageEntries` plus the scalar model check bound all Claude-authored strings before event construction.
- [Tokens, secrets, credentials] No findings — the monotonically minted request ID is a correlation handle, not an authentication token or secret; it is never persisted or logged, and a matched or failed registration is revoked before any later reply can reuse it.
- [File operations] Not applicable — production performs no file operation; the committed fixture is test input read through the existing provenance-checking `contextUsageRead` helper.
- [Subprocess / external command execution] No findings — the change writes the existing JSON control envelope to the already-running child and adds no command, argument, environment, or signal behavior.
- [Cryptographic primitives] Not applicable — no cryptography or security randomness is introduced; request IDs establish local request ownership on one child stream, not hostile-peer authentication.
- [Network & I/O] No findings — input remains subject to `Parser`'s existing `defaultMaxParseBuf`; output has one model string capped at 256 bytes and at most 32 category names capped at 256 bytes each, with fixed-width integer fields.
- [Error messages, logs, telemetry] No findings — decoder errors and all response fields are omitted from logs; `logControlResponse` remains the only record and its existing fixed daemon-authored attributes remain unchanged.
- [Concurrency] No findings — one mutex covers atomic registry mutation only, a per-request completion signal orders response handling after the write result without holding the mutex across I/O, and no goroutine is added.
- [Threat model alignment] No findings — this is a local subprocess stdout boundary rather than relay or client authentication. Exact pending-ID matching, one-shot retirement, bounded construction, and content-free logging address the applicable forged-response, replay, resource-size, and disclosure threats.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-11

## Revisions

None.
