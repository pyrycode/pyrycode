# #1624 — stop citing #1605 as the live in-flight-window measurement

## Files read

- `internal/sessions/pool.go` → `Pool.deliverSettingsInBand` doc comment, the second "ordering facts" bullet — the only production file touched.

## Change

The bullet ends "`interrupt` remains the verb for ending a running turn; #1605 measures the in-flight window live." #1605 closed without that scope and its children #1622/#1623 measured only the turn boundary, so the clause claims a measurement nobody ran. Replace it so the bullet keeps naming `interrupt` as the verb for ending a running turn and states plainly that the in-flight window (a revoke arriving with a tool call already dispatched) is not measured live. No ticket is cited as measuring it. Comment-only; no code, type or behaviour moves.

## Testing strategy

No new logic, so no new test. `go vet ./...`, `go build ./cmd/pyry` and `go test -race ./internal/sessions/...` confirm the comment edit breaks nothing; the verifier's `make check` is the full gate.

## Documentation handoff (pending — documentation stage)

- `docs/knowledge/features/sessions-package-key-types-pool-updatesettings.md`, paragraph beginning "#1605 was split, not landed as such": add that the in-flight window (a revoke arriving mid-turn with a tool call already dispatched) is unmeasured, and that the #1604 "handed to #1605" note is superseded.
- `docs/knowledge/codebase/1604.md` is frozen — leave it alone.

## Security review

**Verdict:** PASS

The change edits one clause of a doc comment. No statement, type, signature or runtime path moves, so the review asks whether the comment misleads a reader about security behaviour rather than whether code is exploitable.

**Findings:**

- [Trust boundaries] No findings. No data crosses a boundary differently: `Pool.deliverSettingsInBand` still sends the same control requests in the same order. The edit removes a claim that a revoke arriving while a tool call is dispatched had been measured live. The claim was false, and a reader relying on it could have trusted revocation to cover in-flight work. The comment now says the window is not measured and keeps naming `interrupt` as the verb for ending a running turn, so it narrows the documented guarantee to what #1622/#1623 actually tested.
- [Tokens, secrets, credentials] No findings. The comment names no credential and the edit touches no token path.
- [File operations] No findings. No file is created, read or written.
- [Subprocess execution] No findings. The child's lifecycle and the arguments in `claudeSettingsArgs` are untouched.
- [Cryptographic primitives] No findings. None are involved.
- [Network & I/O] No findings. No read, write or size limit changes.
- [Error messages, logs, telemetry] No findings. No log or error text changes.
- [Concurrency] No findings. The turncommit-gate ordering described in the first bullet is unchanged, and so is the code that implements it.
- [Threat model alignment] OUT OF SCOPE. A permission revoke does not tear down a tool call that was already dispatched, and nothing has measured that window live. The ticket deliberately files no probe because no failure has been observed. #1604's SHOULD FIX finding stays discharged by documenting the behaviour, and this edit makes that documentation accurate. A probe would be filed as a new ticket once a failure is observed.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-22

## Revisions

- 2026-09-22: Added the `## Security review` section after verifier review. The ticket carries `security-sensitive` and the original plan commit lacked the section. The design and the code are unchanged.
