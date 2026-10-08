# #2605 — every paired device may answer permission prompts and questions

## Files read

- `internal/devices/auth.go` → `Device.MayAnswerRemotePermission`, `AuthorizeRemotePermission`, `RemotePermissionOutcome` — the two predicates the four gated verbs share today; nil-receiver safety is what makes fail-closed structural.
- `internal/devices/device.go` → `Device.AllowRemotePermissions` — field doc claims the bit gates answering.
- `cmd/pyry/modal_resolve_v2.go` → `modalResolverV2.ResolveAnswerWithAlwaysAllow` — step-2 eligibility gate and the `allow` conjunction feeding stream verdict + audit.
- `cmd/pyry/question_resolve_v2.go` → `questionResolverV2.admit`, `ResolveAnswer`, `ResolveRefusal` — shared gate and the answer-arm allow conjunction; method docs narrate the bit.
- `cmd/pyry/pairing_mint_v2.go` → `pairingMinterV2.MintPairing` — privileged verb, stays on `MayAnswerRemotePermission`; its step-2 doc says a stolen pairing "can never mint one that approves", now false.
- `cmd/pyry/mcp_actuate_v2.go` → `mcpActuatorV2.actuate` — privileged verb, keeps `MayAnswerRemotePermission` + `AuthorizeRemotePermission`; not edited.
- `cmd/pyry/pair.go` → `parsePairArgs` flag help for `--allow-remote-permissions`, `registryGrants` (mint re-check, unchanged).
- `internal/protocol/pairing.go` → `MintPairingPayload` doc ("the one gate ADR 025 places on answering…").
- `internal/relay/v2session_seams.go` → `PairingMinter` contract ("must never be able to mint one that approves").
- Tests: `internal/devices/auth_test.go`, `cmd/pyry/modal_resolve_v2_test.go` (`testDevice`, `eligibleDevice`, `TestModalResolverV2_Answer_UngatedDevice`, `TestModalResolverV2_Answer_NoBodyLeak`), `cmd/pyry/stream_approval_test.go` (`TestModalResolverV2_Answer_StreamGateDeniesBeforePermbridge`, `TestModalResolverV2_Answer_StreamAllow`), `cmd/pyry/question_resolve_v2_test.go` (`TestQuestionResolverV2_GateDeniesBeforeConsume`, `TestQuestionResolverV2_AuditRecordsAreDistinguishableAndContentFree`, `questionArms`).

## Context

Operator decision 2026-09-24 (reaffirming pyrycode-mobile#440): view-only clients are not a wanted use case, yet every device paired without `--allow-remote-permissions` (and every minted device) is view-only today, with refused answers and no signal. This drops the per-device bit from the two answering paths (modal answer, question answer/refusal). Minting a pairing and MCP actuation stay gated, so the bit now means "may pair other devices and control tool servers".

ADR 025 needs a dated amendment (see Documentation handoff); the documentation stage writes it.

## Design

New predicates in `internal/devices/auth.go`, beside the existing ones:

- `func (d *Device) MayAnswerPrompt() bool` — true for any non-nil (authenticated) device. Nil-receiver-safe, so the unauthenticated fail-closed stays structural.
- `func AuthorizePromptAnswer(d *Device, outcome RemotePermissionOutcome) bool` — `d.MayAnswerPrompt() && outcome == OutcomeAllow`. The answer paths' own allow conjunction.

Existing `MayAnswerRemotePermission` / `AuthorizeRemotePermission` keep their bodies; their doc comments are rewritten to say they now gate only the privileged verbs (mint, MCP actuation). **Not renamed**: a rename touches `pairing_mint_v2.go` and `mcp_actuate_v2.go` call sites plus e2e comments, pushing the file count further past the boundary for no behaviour change. The doc comment carries the new meaning; a rename can follow separately if wanted.

Call-site switches:

- `modalResolverV2.ResolveAnswerWithAlwaysAllow`: step-2 gate → `dev.MayAnswerPrompt()`; `allow` → `devices.AuthorizePromptAnswer(dev, outcome)`. Ordering (Lookup → gate → classify → consume → verdict → audit) unchanged.
- `questionResolverV2.admit`: gate → `dev.MayAnswerPrompt()`. `ResolveAnswer`'s step-4 conjunction → `devices.AuthorizePromptAnswer(dev, devices.OutcomeAllow)`. Doc blocks updated.

Comment-only edits: `Device.AllowRemotePermissions` doc; `--allow-remote-permissions` help text in `parsePairArgs` ("authorize this device to pair other devices and control MCP servers (default OFF)"); `MintPairingPayload` doc; `PairingMinter` contract; `pairingMinterV2.MintPairing` step-2 sentence (same false claim as the seam, one line).

## Concurrency model

Unchanged. Pure predicates, no new goroutines or state.

## Error handling

Unchanged shapes: nil device → `denied_unauthorized` audit, modal/batch left outstanding, zero/false return. An authenticated device now takes the same path a privileged device takes today.

## Testing strategy

- `auth_test.go`: new tables `TestDevice_MayAnswerPrompt` (bit set → true, bit off → true, nil → false) and `TestAuthorizePromptAnswer` (bit off + allow → true, bit set + allow → true, every non-allow outcome → false, nil + allow → false). Existing `MayAnswerRemotePermission`/`AuthorizeRemotePermission` tables unchanged — they still pin the privileged gate.
- `modal_resolve_v2_test.go`: `TestModalResolverV2_Answer_UngatedDevice` flips to `TestModalResolverV2_Answer_UnprivilegedDevice` — bit-off device's allow and reject answers consume the modal, audit `allowed`/`denied` with identity, return the `{option_id, remote}` dismissal. `NoBodyLeak`'s `denied_unauthorized` row uses a nil device. `testDevice`/`eligibleDevice` comments reworded (bit-off = unprivileged, still answers).
- `stream_approval_test.go`: `StreamGateDeniesBeforePermbridge` keeps only the nil arm; new small test: a bit-off device's allow resolves the parked completer to allow and audits `allowed`.
- `question_resolve_v2_test.go`: `GateDeniesBeforeConsume` keeps only the nil row; new table over `questionArms`: a bit-off device answers/refuses → true, batch consumed, one `allowed`/`denied` record. The three-record audit test's unauthorized case uses a nil device (identity expectation empty for that record).
- Mint and MCP refusal tests, including `internal/e2e/relay_v2_mcp_actuation_test.go`, run unedited.

## Documentation handoff (pending — documentation stage)

- `docs/knowledge/decisions/025-mobile-remote-head-interactive-session.md` § "Security model — remote permission granting (default-safe)": dated amendment (2026-09-24, #2605): any authenticated device may answer permission/trust/destructive modals and question batches; the per-device bit gates only minting pairings and MCP actuation; a stolen unprivileged pairing can approve a tool call, accepted because view-only clients are not wanted. Alternative "C. Auto-grant on timeout (or no per-device gate)" gains a pointer to the amendment.
- `docs/protocol-mobile.md` and package overviews under `docs/knowledge/features/` (at least `devices-package.md`, `pyry-pair-command.md`, `control-plane.md`, the mint-pairing seam doc): state the new meaning wherever answering is described as requiring `--allow-remote-permissions`.

## Size

Production files: `auth.go`, `device.go`, `modal_resolve_v2.go`, `question_resolve_v2.go`, `pair.go`, `pairing_mint_v2.go`, `pairing.go`, `v2session_seams.go` = 8, of which 4 are comment-only. Over the 5-file line, as the refiner's estimate already stated (7) plus one more comment-only fix (`pairing_mint_v2.go`) carrying the same now-false claim as the seam. ~250 lines total. Every other line of the boundary holds.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] No findings — the device identity remains the per-conn authenticated `*devices.Device` bound at handshake; no wire field names a device or the bit. The answer paths now trust any authenticated device, which is the operator's stated intent; the nil (unauthenticated) arm stays fail-closed through the nil-receiver in `Device.MayAnswerPrompt`, pinned by the kept nil-device tests on both paths.
- [Tokens / credentials] No findings — the privilege-escalation-by-minting property is unchanged: `pairingMinterV2.MintPairing` still gates on `MayAnswerRemotePermission` and mints with a literal-false bit; `registryGrants` re-check unchanged. Accepted cost (ticket): a stolen unprivileged pairing can now approve a tool call; revocation via `pyry pair revoke` remains the remedy (connection-scoped, as before).
- [MCP actuation] No findings — `mcpActuatorV2.actuate` is not edited and keeps both `MayAnswerRemotePermission` and `AuthorizeRemotePermission`; those predicates' bodies are unchanged, so the MCP gate cannot silently open. The risk would be a future edit swapping the MCP path onto the new predicate; the distinct names plus doc comments are the guard, and the existing MCP refusal tests pin it.
- [Fail-closed conjunction] No findings — `AuthorizePromptAnswer` keeps the "only OutcomeAllow allows" shape, so an unmapped outcome still denies; table-tested.
- [File ops / subprocess / crypto / network] Not applicable — no file, exec, crypto or I/O change; comments and predicates only.
- [Logs / audit] No findings — audit calls unchanged; identity fields remain the non-secret hash + label.
- [Concurrency] Not applicable — pure predicates.
- [Threat model] OUT OF SCOPE — updating ADR 025 / `docs/protocol-mobile.md` § Security model to record the accepted cost is the documentation stage's (handoff above).

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-24
