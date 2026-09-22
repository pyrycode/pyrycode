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
