# #2608 — translate Codex text, reasoning and turn end into turn events

## Files read

- `internal/codexsup/methods.go` → `serverNotifications` — the 81 methods that reach `OnNotification`; the three classification lists must partition it exactly.
- `internal/codexsup/client.go` → `Config.OnNotification`, `Client.StartTurn`, `Client.call` — the translator's input, and the capture's driver (in-package, so `call` can send `approvalPolicy`/`sandbox`/`summary`).
- `internal/codexsup/serverrequest_test.go` → `jsonSchema.validate`, `loadSchema`, `resolve` — validates each hand-built frame against the committed schema.
- `internal/codexsup/codex_app_server_protocol.schemas.json` → `v2` `TurnCompletedNotification`, `Turn`, `TurnStatus`, `TurnError`, `CodexErrorInfo`, `ThreadTokenUsageUpdatedNotification`, `ThreadTokenUsage`, `TokenUsageBreakdown`, `ModelReroutedNotification`, `ModelVerificationNotification`, `ThreadItem` (19 types), `ServerNotification` — the wire contract.
- `internal/turnevent/event.go` → `TextChunk`, `ThoughtChunk`, `TurnEnd` (token counts, `ModelWindows`, `IsError`, `ErrorCategory`), `ModelWindow`, `RateLimited`, `Compacting`, `Banner`, `Unrecognized`, `UnrecognizedSite` — the target vocabulary.
- `internal/streamsup/parser.go` → `ignoredLineTypes`, `emitUnrecognized`, `truncateRaw`, `truncateField`, `boundStopField`, `maxUnrecognizedRaw`, `maxBannerText`, `maxTurnEndStopField`, `maxModelWindowID` — the precedent for the ignore list and every construction-time bound this translator mirrors.
- `internal/protocol/interactive.go` → the `Site` field comment — calls the site set closed at four; comment-only edit.
- `docs/knowledge/features/codexsup-package.md` — schema-literal lesson: validate hand-built JSON against the schema in a test.

## Context

`codexsup` hands every server notification to the caller raw. This ticket adds the Codex twin of streamsup's parser: a `Translator` that turns those notifications into `turnevent.Event` values, so a Codex turn renders like a Claude turn. Not wired into the daemon (#2585 does that). Tool items (`commandExecution`, `fileChange`) are #2609; here they land on the item-type Unrecognized lane. It also captures live Codex 0.156.1 frames for the tests (and for #2609).

The decision to classify every notification method and every item type on exactly one of three named lists (mapped / ignored / Unrecognized) may deserve an ADR alongside streamsup's known-ignored-list rule; the documentation phase decides.

## Design

New file `internal/codexsup/translate.go`:

- `type Translator struct` — per-connection state: pending usage per turn id, the current model, reroute target per turn id, and whether compaction is active.
- `func NewTranslator(model string) *Translator` — `model` is the model the turns run on (the runner passes `TurnInput.Model`); `SetModel(model string)` updates it between turns.
- `func (t *Translator) Translate(method string, params json.RawMessage) []turnevent.Event` — pure mapping plus the per-turn state above. Called from `OnNotification`, i.e. one goroutine; not safe for concurrent use (documented).

Three package-level lists, each a `map[string]...` or `[]string`:

- `mappedMethods` — `item/agentMessage/delta`, `item/reasoning/summaryTextDelta`, `item/reasoning/textDelta`, `turn/completed`, `thread/tokenUsage/updated`, `item/started`, `item/completed`, `model/rerouted`.
- `ignoredMethods` — no turn meaning (`thread/started`, `turn/started`, `serverRequest/resolved`, `account/rateLimits/updated` with a comment naming a later rate-limit ticket, `item/reasoning/summaryPartAdded`, `thread/status/changed`, …).
- `unrecognizedMethods` — everything else, including `model/verification`, `error`, `warning`, tool output deltas; each becomes `Unrecognized{Site: UnrecognizedCodexMethod, Kind: method}`.

Same for items: `ignoredItemTypes` (`userMessage`, `agentMessage`, `reasoning`), mapped `contextCompaction`; every other `ThreadItem` type → `Unrecognized{Site: UnrecognizedCodexItem, Kind: type}` on `item/started` only (so one row per item, not two).

Mapping:

- `item/agentMessage/delta` → `TextChunk{MessageID: itemId, Text: delta}`; both reasoning deltas → `ThoughtChunk{MessageID: itemId, Text: delta}`.
- `thread/tokenUsage/updated` → held per `turnId`, emits nothing.
- `turn/completed` → exactly one `TurnEnd`: `completed`→`end_turn`, `interrupted`→`cancelled`, `failed`→`end_turn` unless `contextWindowExceeded`→`max_tokens`; `failed` sets `IsError` and `ErrorCategory` = the `codexErrorInfo` string, or the single key of an object variant. `usageLimitExceeded` first emits `RateLimited{Status: "rejected"}`. Token counts from the held usage: `InputTokens = inputTokens − cachedInputTokens − cacheWriteInputTokens` if Codex's input includes them (resolved from the capture), `CacheReadTokens = cachedInputTokens`, `CacheCreationTokens = cacheWriteInputTokens`, `OutputTokens = outputTokens`; `ModelWindows = [{model, modelContextWindow}]` when the window is > 0 and the model id is non-empty and within `maxModelWindowID`. Pending usage for the turn is dropped after.
- `model/rerouted` → `Banner{Level: "warning", Text: "Codex rerouted this turn from <from> to <to> (<reason>)"}` bounded like streamsup's banner; the turn's `ModelWindows` key becomes `toModel`.
- `item/started` of `contextCompaction` → `Compacting{Active: true}`; `item/completed` → `Compacting{Active: false}`.
- `turnevent` gains `UnrecognizedCodexMethod = "codex_method"` and `UnrecognizedCodexItem = "codex_item"`.
- Undecodable params of a mapped method → `Unrecognized{Site: UnrecognizedUndecodable}`.

Bounds, mirroring streamsup: `Unrecognized.Raw` cut at 16 KiB with `Truncated`; banner text cut at 4 KiB; `ErrorCategory` dropped over 256 bytes; model ids over 256 dropped from `ModelWindows`. Text is never interpreted.

Capture: `internal/codexsup/capture_test.go`, `TestCaptureLive`, skipped unless `PYRY_CODEX_CAPTURE_BIN` and `PYRY_CODEX_CAPTURE_HOME` are set. It starts one client, one thread per scenario (read-only sandbox, granular approval with `sandbox_approval`), records every notification and server request to `internal/codexsup/testdata/capture/<scenario>.jsonl`, scrubs account email/ids and local paths. Scenarios: plain, reasoning, command accepted, command declined, file edit, interrupted.

State bounds (from the security review): pending usage and reroute targets are keyed by the peer's `turnId`, so each map holds at most `maxPendingTurns` (8) entries; on overflow the map is cleared before the new entry is stored. Codex runs one turn per thread at a time, so a legitimate peer never has more than one or two pending.

`ErrorCategory` from an object `codexErrorInfo` is its key only when the object has exactly one key (the schema's `additionalProperties: false` shape); anything else leaves it empty. Model ids interpolated into the reroute banner are cut at `maxModelWindowID` each before the whole text is bounded.

## Concurrency model

None added. `Translate` runs on the caller's goroutine (the read loop). The capture test interrupts from the test goroutine, never from `OnNotification`.

## Error handling

Undecodable params → `Unrecognized` (undecodable lane), never a panic or a silent drop. A `turn/completed` without held usage → `TurnEnd` with zero counts.

## Testing strategy

- `TestMethodListsPartitionServerNotifications` — every `serverNotifications` entry on exactly one list; fails on none or two.
- `TestItemTypesClassified` — every `ThreadItem` type from the schema on exactly one item list.
- Table-driven `TestTranslateFixtures` over committed frames: plain text, reasoning, accepted command (usage), interrupted, and hand-built failed/usageLimit/contextWindow/httpConnectionFailed/reroute/compaction/unknown item.
- `TestHandBuiltFramesMatchSchema` — each hand-built frame validated against its notification's params definition via `jsonSchema.validate`.
- Bounds: oversized raw truncates with `Truncated`.

## Open questions

1. Does `thread/tokenUsage/updated` arrive before or after `turn/completed`? (capture)
2. Does `last` cover the whole multi-call turn, or must counts come from the change in `total`? (capture)
3. Does Codex's `inputTokens` include `cachedInputTokens`? (capture)
4. Can the translator learn the turn's model from the wire (e.g. `thread/settings/updated`) instead of the caller? (capture)

## Documentation handoff (pending, documentation stage)

- `docs/protocol-mobile.md`, `Unrecognized` frame `site` row: add `codex_method`, `codex_item`.
- `docs/knowledge/features/turnevent-package-outbound-event-variants.md`, `Unrecognized` row: same.
- `docs/knowledge/features/codexsup-package.md`: the translator, its three method lists and the observed usage order.

## Security review

**Verdict:** PASS (after one revision: the per-turn maps were unbounded in the first draft; now capped at `maxPendingTurns`)

**Findings:**

- [Trust boundaries] No findings — the single boundary is `Translator.Translate`; everything it reads is untrusted peer JSON decoded into typed structs, and it emits only bounded `turnevent` values. Text deltas are carried verbatim and never interpreted; they are bounded only by `acp`'s `maxLineBytes` (16 MiB), the same posture as streamsup's text path. Model and server strings reaching a client (Banner text, `ErrorCategory`, `Unrecognized.Raw`) carry the client-side sanitization obligation streamsup's variants already document.
- [Trust boundaries] MUST FIX (addressed in Design) — pending usage and reroute state keyed by peer-chosen `turnId` could grow without bound; capped at `maxPendingTurns`.
- [Trust boundaries] SHOULD FIX — an object `codexErrorInfo` with several keys must not smuggle an arbitrary key choice; take the key only when there is exactly one, drop over 256 bytes (`boundStopField`'s rule).
- [Tokens] SHOULD FIX — the capture copies the sign-in into an isolated `CODEX_HOME` outside the repo, removed afterwards. Fixtures are scrubbed of account email and ids and of local paths, then grepped for token shapes (`eyJ`, email `@`, account ids) before `git add`.
- [File operations] No findings — the capture writes fixed names under `testdata/capture/`; the thread's cwd is `t.TempDir()`. The translator touches no files.
- [Subprocess] SHOULD FIX — the capture runs a real Codex agent. It runs in the `read-only` sandbox, and the capture's approval handler accepts only a command containing the exact expected `touch` and a file change whose paths sit under the temp cwd; anything else is declined. The capture is skipped unless two env vars are set, so `make check` never runs Codex.
- [Crypto] Not applicable — no randomness, keys or comparisons against secrets.
- [Network & I/O] No findings — input is bounded by `acp`'s line cap; `Unrecognized.Raw` is cut at 16 KiB with `Truncated`, banner text at 4 KiB.
- [Logs] No findings — the translator logs nothing; params are never logged (package rule in `client.go`'s package doc).
- [Concurrency] No findings — no goroutines; `Translate` runs on the read loop and is documented as not safe for concurrent use. The capture calls `Interrupt` from the test goroutine, never from `OnNotification`.
- [Threat model] OUT OF SCOPE — whether mobile clients accept the new `site` values is #2585's check; nothing from this ticket reaches the wire. A silent reroute is the threat this ticket closes: `model/rerouted` always yields a Banner.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-24

## Revisions

### 2026-09-24 — open questions resolved by the live capture

1. **Usage order.** `thread/tokenUsage/updated` arrives *before* `turn/completed`, once per model call (two on the accepted-command turn), each followed by `account/rateLimits/updated`.
2. **`last` vs `total`.** `last` covers only the latest model call (accepted-command turn: `last.outputTokens` 5, turn total 67). The turn's counts are the change in `total`. The base is taken at the turn's first update as `total − last`, which equals the thread's total before the turn on a fresh or a resumed thread, so no per-thread state is needed.
3. **Cached input.** `inputTokens` includes `cachedInputTokens` (cached ≤ input on every update), so `InputTokens = inputTokens − cachedInputTokens`. `cacheWriteInputTokens` was 0 throughout, so whether input includes it is unmeasured; it is not subtracted.
4. **Model source.** No turn-scoped notification carries the model (the observed `thread.model` on `thread/started` is outside the v2 `Thread` schema). Kept `NewTranslator(model)` + `SetModel`.

### 2026-09-24 — capture deviations

- **Approval policy.** The granular policy is refused (`askForApproval.granular requires experimentalApi capability`), and `codexsup`'s handshake does not declare that capability. The capture uses `untrusted` with the `read-only` sandbox, which still yields command approval requests.
- **Not produced live:** Luna at effort `low` emitted no reasoning item, and the file-edit turn ran a `pwd && ls -la` pre-check, which the capture declined, and then gave up without a `fileChange`. Reasoning deltas are hand-built (schema-validated). The file-edit capture is committed as observed; #2609 needs a recapture for a `fileChange` item.
- **Unknown item type.** A type outside the schema cannot validate, so the hand-built frame uses the schema's unmapped `webSearch`; a type absent from the schema is covered inline in `TestTranslateUnrecognizedLanes`.
- **Method lists** are slices in `translate.go` (`mappedMethods`, `ignoredMethods`, `unrecognizedMethods`) plus item-type slices; there is no separate `turnevent` edit beyond the two sites.
