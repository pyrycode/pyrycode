# #1537 — delete the orphaned GetOrCreator seam and ErrAttachUnavailable, sweep handleAttach comments

Short plan: deletions and comment rewording only. No new type, state or failure mode.

## Files read

- `internal/control/server.go` → `GetOrCreator` (deleted), `Sessioner` (drop the embedding and the "Phase 1.1e" sentence), `Session.Activate` and `SessionResolver.ResolveID` docs, `NewServer` doc (foreground `Attach` paragraph), `handleSessionsNew` (two "mirrors handleAttach" asides), the approve handler's clear-deadline comment.
- `internal/control/protocol.go` → `SessionsPayload` doc ("Phase 1.1e (attach) will add further omitempty fields").
- `internal/control/sessions_new_test.go` → `fakeSessioner.GetOrCreate`, `getOrCreateCalls`, `getOrCreateOverride`, `getOrCreateCall`; the "Mirrors …" cites on `TestProtocol_SessionsRoundTripBackCompat` and `TestSessionsNew_PassesLabelOnWire`.
- `internal/sessions/session.go` → `ErrAttachUnavailable` (deleted).
- `internal/sessions/get_or_create.go` → `Pool.GetOrCreate` doc and its take-path comment (two `handleAttach` mentions). `Pool.GetOrCreate` / `GetOrCreateIn` stay; they are live elsewhere.

## Change

Delete `control.GetOrCreator` and its embedding in `Sessioner`; nothing in `internal/control` calls it, and removing a method from an interface only loosens what implementers must provide, so `*sessions.Pool` and every fake still satisfy `Sessioner`. Delete the test double's `GetOrCreate` method and its scaffolding. Delete `sessions.ErrAttachUnavailable`, which nothing produces or consumes.

Comments: where the point survives without the deleted handler, reword it; otherwise delete it.
- `Session.Activate`: drop the "handleAttach calls this before Attach" sentence; do not name a new caller.
- `SessionResolver.ResolveID`: keep "errors are returned verbatim", drop the handler attribution.
- `NewServer`: delete the foreground-`Attach` paragraph.
- `Sessioner`: drop the "Phase 1.1e … will continue this pattern" clause, keep the one-sub-interface-per-verb rationale.
- `handleSessionsNew` and the approve handler: drop the "mirrors handleAttach" parentheticals, keep the deadline reasoning.
- `SessionsPayload`: drop the Phase 1.1e sentence.
- `sessions_new_test.go`: drop the three dead "Mirrors" test cites, keep the description of the technique.
- `get_or_create.go`: say activation is the caller's responsibility without naming a handler.

**Deviation from the ticket:** the ticket says `ErrAttachUnavailable` is `session.go`'s only use of `errors`. At current `main` it is not — `ErrUnsupportedPermissionMode` and `ErrPermissionModeConflict` also use `errors.New` — so the import stays. The acceptance criterion names the import as "unused"; it is not, and removing it would break the build.

## Testing strategy

No new logic, so no new test. Proof is: the AC's `rg -nw` sweep returns zero; `go build ./cmd/pyry`, `go vet ./...`, `go vet -tags e2e_realclaude ./internal/e2e/realclaude/`, and `go test -race ./internal/control/... ./internal/sessions/...` pass. The existing `sessions.*` verb tests cover that the trimmed `Sessioner` still dispatches.

## Documentation handoff

None. The ticket carries no documentation requirement; ADRs 003/008 and the feature docs are out of scope per the ticket.
