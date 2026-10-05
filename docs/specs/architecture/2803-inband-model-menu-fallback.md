# #2803 — keep the second live in-band model transition on bracket-free menus

## Files read

- `internal/e2e/realclaude/interactive_stream_inband_model_test.go` →
  `TestInteractiveStream_InBandModelChange_LiveChildReportsNewModel`,
  `inbandPickBracketedValue`, `inbandMenuRow`: phase boundaries, request
  correlation and the obsolete bracketed-only prerequisite.
- `internal/e2e/realclaude/interactive_stream_inband_menu_drift_test.go` →
  `inbandCompareMenu`: retain chosen-row diagnostics on B1/B2 failures.
- `internal/sessions/modelfamily.go` → `familyAlias`, and
  `internal/sessions/pool.go` → `deliverSettingsInBand`: full IDs can be
  rewritten; plain alphabetic aliases retain their published resolution contract.
- `internal/relay/v2session_settings_test.go` → `TestValidModel` and
  `TestValidModel_ByteSetIsClosed`: existing hermetic bracket validation stays intact.
- `docs/knowledge/features/e2e-realclaude.md` and
  `docs/knowledge/features/e2e-realclaude-interactive-stream-inband-model-test-go.md`:
  live evidence must observe the same child, and upstream menus can drift within
  one binary version; retain menu logging and exact-resolution comparisons.
- `docs/knowledge/features/development-verification.md` → verify inherited
  premises and distinguish each selection condition independently.
- `CODING-STYLE.md`: table-driven stdlib tests, gofmt and goroutine cleanup.

## Context

Both #2796 and main pass the first transition but fail before phase 2 because
Claude no longer publishes a bracketed row. This ticket repairs that test
prerequisite without changing production delivery or weakening either transition.
No decision record is needed. No other fetched feature branch touches either
test file. One deliverable; estimate about 230 written lines including this plan,
zero exported types, one consumer, four acceptance criteria, fewer than ten
selection/rejection branches; all sizing limits hold.

## Design

Replace the fatal bracketed-only picker with a pure
`inbandPickPhaseTwoTarget(menu, baseline) (inbandMenuRow, bool)` selection helper.
First select a nonempty bracketed value with a nonempty resolution differing
from the baseline. Otherwise select a published plain alphabetic alias meeting
those same resolution conditions. Never use a full ID as the fallback: production
may rewrite it to a family alias whose resolution differs from the full-ID row.
Preserve menu order within each preference. The live caller logs the full menu
and chosen value, exact expected resolution and bracketed coverage availability.
No target is a fatal failure with the menu, never a skip or a retired phase.
An available bracketed target is sent once; neither delivery failure nor B1/B2
failure triggers alias fallback. Preserve A1–A4, B1–B3, request IDs 1 and 3,
and chosen-row drift diagnostics. Check one spawn across the entire test as well
as the final PID. Update phase rationale and messages for conditional coverage.

## Concurrency model

Selection is synchronous pure logic. Existing recorder locking, child goroutine,
cancellation and bounded cleanup remain unchanged; no new goroutines.

## Error handling

Empty values/resolutions and baseline-equivalent rows are unusable. Missing
targets fail before sending settings; delivery, correlated acknowledgement and
model/PID/spawn failures retain their existing failure paths and diagnostics.

## Testing strategy

Write credential-free table-driven selection tests first and observe failure.
Cover bracket preference even after an alias, deterministic first candidate,
the reported bracket-free menu, empty fields, baseline-equivalent rows, full-ID
exclusion, and no usable target. Run these and existing drift tests with
`-tags e2e_realclaude -race`; retain and run hermetic relay bracket validation.
Run `go vet ./...`, tagged package vet and `go build ./cmd/pyry` with output outside
the worktree. The verifier owns `make check`; the dispatcher owns the named live
test with `e2e_realclaude` and `-race`. Pending live acceptance must record executed,
pass and skip counts plus the selected value; a skip does not satisfy acceptance.

## Open questions

None. Missing live credentials are handled by the dispatcher-owned gate.

## Documentation handoff

Pending for the documentation stage: in
`docs/knowledge/features/e2e-realclaude-interactive-stream-inband-model-test-go.md`,
under the `interactive_stream_inband_model_test.go` entry, replace the claim that
absence of a bracketed row is fatal: phase 2 prefers a bracketed target and
otherwise uses a published plain alias, always proving a second exact model
transition in the same child. State that a bracket-free live pass proves in-band
model delivery but cannot prove live bracketed-value support; hermetic bracketed
validation remains covered.
