# #2839 — Report the Claude account source and its read state to paired clients

## Files read

- `cmd/pyry/claude_account.go` → `claudeAccount`, `newClaudeAccount`, `resolveClaudeAccountSource`, `readClaudeAccountField`, `read`, `status`, `hasControlByte`: the accessor this ticket extends with a label, read ordering and a wire mapping.
- `cmd/pyry/claude_account_test.go`, `cmd/pyry/claude_account_op_test.go` → `writeAccountFile`, `writeOwnerToken`, `writeFakeOp`, `plantedToken`, `plantedRef`, `testLogger`/`safeLog`: test helpers the new tests reuse.
- `internal/relay/handlers/host_system_prompt.go` → `RequestHostSystemPrompt`, `replyError`: the #2768 read-handler shape to copy.
- `internal/relay/handlers/host_system_prompt_test.go` → `dispatch.NewTestConn` reply capture pattern.
- `internal/protocol/claude_account.go` → `ClaudeAccountPayload`, kind/state constants; package comment says wiring is pending #2839.
- `cmd/pyry/relay.go` → `relayWiring.hostSystemPrompt` and the v2 handler map entries `TypeRequestHostSystemPrompt`: where the new field and entry go.
- `cmd/pyry/main.go` → `runSupervisor`: `account.prime(ctx)` runs before `startRelay`, so the first request already sees a boot-time failure.
- `cmd/pyry/relay_guard_test.go` → `"TypeRequestClaudeAccount": "pending handler (#2839)"` moves to `"map-dispatched"`.
- `docs/knowledge/features/claude-account-source.md` § "Concurrency": the last-finisher caveat this ticket removes by giving each read a sequence number.

Overlap: `origin/feature/2831` adds a different `relayWiring` field and relay.go block; edits here stay additive.

## Context

`claudeAccount.status()` has no reader. #2838 declared the `request_claude_account` / `claude_account` pair. This ticket answers it over the relay, adds an operator `label`, and makes the recorded state independent of which concurrent read finished last, because a client now sees it.

## Design

### Label (`cmd/pyry/claude_account.go`)

- `claudeAccount` gains `label string`; `claudeAccountStatus` gains `Label`.
- In `newClaudeAccount`, when the flag and env values are both empty and the resolved source is nonempty (so the file supplied it), read `label` with `readClaudeAccountField`. A non-string or null label is already refused there with origin-only wording.
- `validAccountLabel(s) bool`: `len(s) <= 64`, `utf8.ValidString`, no rune with `unicode.IsControl` (covers C0, DEL and C1). On failure: `claude account label from file <path>: must be at most 64 bytes of printable UTF-8` — never the value.
- A flag or env source reports `""` and never opens the file for the label.

### Ordering (`claudeAccount.read`)

- Two counters under `mu`: `started uint64`, incremented at the start of each read to give that read its sequence `seq`, and `recorded uint64`, the sequence whose outcome `state`/`reason` hold.
- On completion, the outcome is recorded only when `seq > recorded`. An older read finishing late changes nothing. The caller still gets its own result, and the failure warning is still logged (that launch is still refused). The "recovered" info line fires only when the success is recorded and the previously recorded state was `failed`.

### Wire mapping (`cmd/pyry/claude_account.go`)

- `(*claudeAccount).ClaudeAccount() protocol.ClaudeAccountPayload` maps the snapshot: kind `""` → `machine_login`, `file` → `file`, `1password` → `1password`; state `not-configured` → `not_configured`, `ready`/`failed` unchanged; label and reason copied. The mapping stays in `cmd/pyry`; `internal/protocol` stays pure data.

### Handler (`internal/relay/handlers/claude_account.go`)

- `type ClaudeAccountSource interface { ClaudeAccount() protocol.ClaudeAccountPayload }`.
- `RequestClaudeAccount(src ClaudeAccountSource, logger *slog.Logger) dispatch.Handler`: unmarshal into `protocol.RequestClaudeAccountPayload`; on error log static metadata (`event=request_claude_account.malformed`, `conn_id`) and reply `protocol.CodeProtocolMalformed`, non-retryable. Otherwise one `c.Reply` of `TypeClaudeAccount`. No capability gate, no broadcast.

### Wiring

- `relayWiring.claudeAccount handlers.ClaudeAccountSource`; `runSupervisor` passes `account`.
- Handler map: `protocol.TypeRequestClaudeAccount: handlers.RequestClaudeAccount(w.claudeAccount, logger)`.
- Guard table entry becomes `"map-dispatched"`.
- `internal/protocol/claude_account.go` package comment: wiring is done, in `handlers.RequestClaudeAccount`.

## Concurrency model

No new goroutines. `read` is called concurrently by runner spawn paths; the mutex is taken twice per read (sequence assignment, outcome record) and never across the reader. The handler reads `status()` under the same mutex.

## Error handling

- Malformed request → `protocol.malformed`, non-retryable, nothing echoed from the payload.
- A failed read is in-band: `state: failed`, `reason` from the accessor's fixed vocabulary.
- An invalid label is a startup error naming only the file origin.

## Testing strategy

- `TestNewClaudeAccount_Label` (table): label from the file for a file-supplied source; flag and env sources report `""` even when the file has a label; absent label `""`; exactly 64 bytes accepted; 65 bytes, invalid UTF-8, a control character (C0 and C1), a non-string and null refused naming the file and not echoing the value.
- `TestClaudeAccount_LaterReadWins`: a reader double blocks the first read on a channel; a second read starts after the first has entered, completes; then the first is released. Both orders: fail-after-success leaves `ready`; success-after-fail leaves `failed` with the later read's reason.
- `TestClaudeAccount_WirePayload` (table): status → payload mapping for not configured, file ready, 1password failed, with labels.
- `TestRequestClaudeAccount` in `internal/relay/handlers`: correlated single reply with the source's payload and all four keys; malformed and wrong-type payloads get non-retryable `protocol.malformed` without echoing the payload.
- `TestClaudeAccountFrame_FileSourceNoLeak`: claude-account.json (with label) points at a token file in a distinctive directory; frames from the real handler after `prime` (ready), after making the token file unreadable and reading (failed), and after restoring and reading (ready, reason `""`); none of the serialized frames contains the token, token path, json path. Asserts kind/label/state/reason.
- `TestClaudeAccountFrame_OnePasswordNoLeak`: same over an `op://` reference with distinctive vault and item ids and a fake `op` (chosen by `op_cli` in the json), in ready and failed states.

## Open questions

- Should C1 controls count as control characters? Decided yes (`unicode.IsControl`): the label is rendered by clients.

## Documentation handoff

Pending for the documentation stage:

- `docs/protocol-mobile.md`, "Claude account source" section and the application-message table rows: remove the "pending" wording #2838 added.
- `docs/guide.md`, "Claude account source": document the optional `label` key, its 64-byte limit (UTF-8, no control characters), that it applies only when the file supplies the source, and that paired clients can read the kind, label and read state but never the token or path.
- `docs/knowledge/features/claude-account-source.md`, "Concurrency": replace the last-finisher caveat with the ordering now guaranteed: each read takes a sequence number when it starts, and an outcome is recorded only if no later-started read has already recorded one.

## Revisions

- 2026-10-05, build: `encoding/json` replaces invalid UTF-8 inside a string with U+FFFD instead of failing, so `validAccountLabel` never saw the bad bytes and an invalid-UTF-8 label was accepted. `readClaudeAccountField` now refuses a value whose raw JSON bytes are not valid UTF-8, with its existing `%q must be a string` origin-only wording. This applies to every key it reads (`source`, `op_cli`, `label`); a `source` or `op_cli` with invalid bytes was previously turned into a mangled path that could only fail later, and now stops startup like any other unusable claude-account.json value.
- 2026-10-05, build: `TypeRequestClaudeAccount` moved into the guard's `inboundTypes` table (beside `TypeRequestHostSystemPrompt`), not just relabelled in place; the guard fails a wired inbound type classified anywhere else. Added `TestClaudeAccountFrame_NotConfigured` for the no-source reply through the handler.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] No findings. The only inbound data is the `request_claude_account` payload, decoded once in `RequestClaudeAccount` into an empty struct; nothing from it reaches the accessor. Authentication is the existing paired-client map dispatch. The label crosses file → memory once, in `newClaudeAccount`, under the ownership/mode checks `readClaudeAccountField` already applies.
- [Tokens] No findings by construction: `claudeAccount` never holds the token, and `ClaudeAccount()` builds the payload from `kind`, `label`, `state`, `reason` only. `reason` comes from `accountReadError`'s fixed daemon strings or three fixed fallbacks in `read`, never an OS/exec error. SHOULD FIX: prove it with the two planted-secret frame tests (file and op), covering the json path, which `reason` must never carry; the verifier checks they exist.
- [Tokens] SHOULD FIX: the label is operator text sent to clients; it must not be an accidental paste of a token surfacing verbatim. It cannot be prevented by validation, but the label is read only from the owner-only file's dedicated `label` key, never from `source`, so a token pasted into `source` never becomes a label. The test asserts flag/env sources report `""`.
- [File operations] No findings. No new file access: the label reuses `readClaudeAccountField` (O_NONBLOCK open, fstat type/owner/mode on the read descriptor, 64 KiB cap). The extra open of the same file is a second read of an owner-only file; a swap between reads can only change it to another owner-only file.
- [Subprocesses] No findings. No new subprocess; the handler never triggers a read, so a client cannot make the daemon run `op` on demand.
- [Cryptography] No findings. None used.
- [Network and I/O] No findings. Frame size is capped by the existing relay transport; the reply is bounded (label ≤ 64 bytes, reason from a fixed set).
- [Errors, logs] SHOULD FIX: the malformed-request log line carries only `event` and `conn_id`, never the payload; the label error never echoes the value. Tests assert both.
- [Concurrency] No findings. One mutex, never held across the reader or a send; the sequence check and the state write happen under one lock, so there is no check-then-act gap.
- [Threat model] OUT OF SCOPE: disclosing which source kind and label an instance uses to every paired client is the product decision in #2816/#2838 (paired clients are the operator's devices). Per-client authorisation of this read is not in the protocol's security model and is not picked up by a ticket.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-05
