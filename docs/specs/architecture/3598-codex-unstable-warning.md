# Suppress the pyry-enabled Codex feature warning (#3598)

## Files read

- `cmd/pyry/codex_home.go` → `codexHomeConfig`, `prepareCodexHome`: fixed TOML baseline and isolated, atomic config rewrite.
- `cmd/pyry/codex_runner_test.go` → `TestPrepareCodexHome`: posture, permissions, auth preservation and directory contents.
- `docs/knowledge/features/codexsup-package.md` → The translator's three-list partition: warnings remain visible through the translator.
- `docs/knowledge/decisions/038-codex-daemon-owned-home-read-only-default-decline.md`: daemon-owned home and restrictive baseline remain required.
- `CODING-STYLE.md` and `docs/knowledge/features/development-verification.md`: focused regression proof and contract comments.

## Change

Add top-level `suppress_unstable_features_warning = true` before `[features]` in `codexHomeConfig`. Keep `default_mode_request_user_input` enabled and the existing approval, sandbox and reviewer defaults. Explain in the config's Go comment that leaving the workspace untrusted deliberately prevents loading workspace-local Codex configuration. This is one observable behavior: suppress the feature warning pyry causes. Trust-warning deduplication remains #3599.

The [official configuration reference](https://learn.chatgpt.com/docs/config-file/config-reference) identifies this boolean setting. Local `codex --version` reports `codex-cli 0.159.2`; its Linux binary contains both the `ConfigToml` field and warning text prescribing `suppress_unstable_features_warning = true`. Binary SHA-256: `1748767b230ebfc3d4ab7e4e254920d0c0ad9691fd8c11f190e7d44511a4a92e`. The pinned app-server protocol schema does not contain this TOML setting.

No concurrent remote feature branch touches either target file. Estimated total written work: about 60 lines including this plan; no new exports, consumer updates, state or reject branches; two acceptance criteria. The config writer's file operations and lifecycle remain unchanged.

## Testing strategy

Extend `TestPrepareCodexHome` before implementation: require the exact true assignment in the top-level region before any TOML table and reject project trust entries. Retain existing feature, posture, permissions and auth checks. Observe failure without the flag, then pass with it using `python3 scripts/test-admission.py -- go test -timeout=30m -race -count=1 -run '^TestPrepareCodexHome$' ./cmd/pyry/...` on Linux. Run `go vet ./...` and build `./cmd/pyry` with output outside the worktree. Complete acceptance gates belong to the dispatcher; macOS is outside this ticket's verification scope under the host-OS policy.

## Documentation handoff

Pending for the documentation stage: in `docs/knowledge/features/codexsup-package.md`, under **The translator's three-list partition**, state that config suppresses the unstable-feature warning while the workspace deliberately stays untrusted.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] No findings. `codexHomeConfig` remains a constant without caller-controlled data. The regression rejects project/trust entries so suppression cannot enable workspace-local configuration.
- [Tokens] `prepareCodexHome` never reads or moves credentials; the existing `auth.json` preservation assertion remains.
- [File operations] Keep directory mode `0700`, config mode `0600`, and temporary-file rename that replaces a destination symlink; no path handling changes.
- [Subprocesses] No argv, environment, process creation or shutdown change; the daemon-owned home stays isolated from personal configuration.
- [Cryptography] No crypto operations or secret comparisons in the changed constant or assertions.
- [Network and I/O] No network reads or new inputs; only fixed bytes written by the existing config writer.
- [Errors, logs, telemetry] Suppress only the upstream unstable-feature warning via its config key; no translator filtering, logs or error contracts change.
- [Concurrency] No goroutines, locks or lifecycle state added; the existing atomic rewrite remains intact.
- [Threat model] Preserve ADR 038's isolated home and restrictive baseline. Trust-warning deduplication is outside this ticket and belongs to #3599.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-17
