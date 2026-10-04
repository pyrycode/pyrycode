# #2760 — daemon-supplied workspace base in hello_ack

## Files read

- `internal/relay/v2session_seams.go` → `V2SessionConfig`: additive optional fields preserve existing config literals.
- `internal/relay/v2session_handshake.go` → `WorkspaceRoot`, `handleNoiseInit`: select metadata only after token, version and atomic static-key binding admission; seal it in Noise response early data.
- `internal/relay/v2session.go` → `NewV2SessionManager`: config is retained by the manager; existing Run lifecycle remains unchanged.
- `internal/relay/v2session_test.go` → `TestV2Session_HelloAckWorkspaceRoot`, `driveToOpenCaps`: real encrypted handshake, raw omission, filesystem and privacy assertions.
- `internal/relay/v2session_min_version_test.go` → `runHelloFrom`, `assertVersionRejected`: reusable real handshake and encrypted error/close observation.
- `internal/relay/v2session_static_key_test.go` → `installKey`, `TestV2Session_StaticKey_OtherInstallRefusedLikeUnknownToken`: distinguish an authenticated Noise key from an admitted paired install.
- `docs/knowledge/features/relay-package.md` → logging discipline: host paths must remain absent from routing metadata and logs.
- `docs/knowledge/features/v2-session-manager.md` and `v2-session-manager-state-machine-noise-init-happy-and-failure-path.md` → admission lesson: rejected peers can decrypt response early data, so encryption alone does not authorize path disclosure.
- `docs/knowledge/features/development-verification.md` → protocol boundaries: inspect raw keys as well as decoded zero values.
- `docs/protocol-mobile.md` → “hello_ack (v2-specific note)” and “Security model”: advertisement only, token authorization and relay blindness.
- `docs/specs/architecture/2378-report-host-workspace-base.md` → prior lexical-only root advertisement contract.

## Context

The daemon will resolve a workspace base in the dependent slice of #2752. This ticket provides the relay seam to advertise that value, preserving legacy callers. No decision record is needed.

## Design

Add `WorkspaceBase *string` to `V2SessionConfig`. Nil means unsupplied and uses `WorkspaceRoot()`. A non-nil absolute string is advertised verbatim, without cleaning or resolving symlinks. A non-nil empty or relative string omits the key without fallback. The caller owns resolution and must not mutate the pointed-to value while the manager runs.

Select the value inside `handleNoiseInit`'s existing accepted-token/no-version-reject guard, after `BindStaticKey` has refined admission. Preserve the existing encrypted ack, error and close flow. No new filesystem operation, log attribute, or unencrypted field is added.

Sizing: one deliverable, two acceptance criteria, two production files, zero new exported types/interfaces, zero required consumer updates, and zero new reject branches. About 230 total written lines including tests and this plan, below all five limits. The #2378 analogue supplies the harness; broader rejection coverage accounts for the estimate increase. The remote feature-branch scan found no overlaps with the planned files.

## Concurrency model

Selection runs synchronously on the manager's existing Run goroutine. No goroutine, channel or lock is added. The supplied config value remains immutable during use.

## Error handling

Explicit empty/relative values degrade optional metadata to omission and do not fail admission. Unsupplied values retain `WorkspaceRoot()`'s unknown/non-absolute HOME omission. Existing token, binding, version, JSON and Noise failures retain their current error/close contracts.

## Testing strategy

Extend `TestV2Session_HelloAckWorkspaceRoot` for supplied absolute values (including an unclean spelling), explicit empty/relative values with an absolute HOME, and supplied absolute values with unavailable HOME. Retain default/unknown/relative HOME cases. Check decoded values, raw-key omission, non-creation, captured logs and routing envelopes.

Reuse `runHelloFrom` to test supplied-base omission for unknown/expired tokens, a static-key mismatch and a version refusal. Retain raw decrypted ack bytes in the helper's outcome; assert omission and unchanged encrypted error/close shape plus path privacy. Run the focused tests red before production edits, then green; run `go test -race ./internal/relay/...`, `go vet ./...`, and `go build -o /tmp/builder-2760/pyry ./cmd/pyry`. The verifier owns the full-module gate.

## Open questions

None. Daemon resolution and HOME confinement belong to the dependent slice.

## Documentation handoff

Pending for the documentation stage: in `docs/protocol-mobile.md`, “hello_ack (v2-specific note)”, document the optional supplied base, legacy default only when unsupplied, and omission for explicit empty/relative values; retain encryption/admission/privacy guarantees.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] The base comes from `V2SessionConfig`, never a client hello. MUST FIX avoided by retaining the admission guard after `BindStaticKey`; encryption alone would disclose metadata to rejected peers.
- [Tokens, secrets, credentials] No credential handling changes. Unknown/expired tokens and mismatched installs receive no workspace metadata.
- [File operations] Lexical absolute-path classification only; no stat, open, mkdir, symlink resolution or path-based access. OUT OF SCOPE: daemon resolution/HOME confinement remains in the dependent slice of #2752.
- [Subprocesses] The supplied string never reaches process argv or environment; no subprocess is added.
- [Cryptography] Existing Noise `WriteResp` seals the entire ack; no key, nonce or random source changes.
- [Network and I/O] No inbound shape or cap changes. Only the caller-supplied string enters encrypted early data; outer routing envelopes gain no path.
- [Errors, logs, telemetry] Invalid supplied values omit metadata silently; the base enters no error or log field. Tests use a distinctive supplied path to detect disclosure.
- [Concurrency] No new goroutines or locks; caller immutability prevents mutation races on the optional pointer.
- [Threat model] Relay MITM cannot read the path, and a leaked bound token cannot disclose it from another install. Advertisement grants no filesystem capability; prompt processing, key lifecycle and existing denial-of-service policy are unchanged.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-04
