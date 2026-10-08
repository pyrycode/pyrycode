# #2987 — Historical specs review, batch 01 of 22

## Files read

- `docs/specs/README.md` → “These are point-in-time artifacts, not current-state authority” and “Scaffolding maintenance”: preserve designs and citations; removals require byte-bound approval and eligibility.
- `docs/knowledge/features/development-verification.md` → “Check searches and citations”: existence and closed state do not establish current truth.
- The 45 exact manifest paths in issue #2987 → historical bodies, cited targets and question decisions; enumerate each once in the completed ledger below.
- `cmd/spec-reference-inventory/main.go` → `run`: distinct document/target inventory at each pinned tree.
- `cmd/spec-scaffolding-prune/main.go`, `markdown.go`, `evidence.go` → `run`, `parse`, `eligibility`: hashes, all-heading ordinals, unsupported Markdown and merge-evidence boundaries.
- `cmd/spec-scaffolding-prune/testdata/2929-evidence.json` → reusable verified closing-link snapshot; no new evidence acquisition.
- `docs/knowledge/features/sessions-package.md`, `sessions-package-key-types-runner-interface-runnerfactory.md`, `control-plane.md`, `cli-verb-dispatch.md` → current session/control seams and terminal retirement.
- `docs/knowledge/features/jsonl-reconciliation.md`, `rotation-watcher.md` → startup adoption retired by #839; watcher retired by #2137.
- `docs/knowledge/features/e2e-harness.md` → harness history and present test boundaries.
- `internal/sessions/pool.go`, `pool_identity.go`, `reconcile.go`, `runner.go` → `List`, `ResolveID`, `Rename`, `RotateID`, `DefaultClaudeSessionsDir`, runner contract.
- `cmd/pyry/main.go`, `internal/e2e/harness.go`, `cli_verbs_test.go`, `cap_test.go` → `runArgs`, active-cap flag, harness constructors and retained binary-boundary proofs.
- `CODING-STYLE.md` and shared working practice → review scope, commits, GitHub helpers and source evidence.

## Context

Review only issue #2987's exhaustive 45-document manifest at scope commit
`bf3129a42d38d257a3085f05dde5079a275c84a6` against reviewed-current commit
`f2a41c4b5bf2e72437eb1dc97b47b8178ff86429`. Every selected document is byte-identical
between these trees; no candidate disappeared. The scope has 37 missing-reference
pairs in 16 documents and 43 exact-title question occurrences in 43 documents.
The categories overlap. No new architectural decision record is required.

## Design

One deliverable: an evidence-backed review and precise documentation handoff.
Only this plan is committed. Append the disposition ledger and machine-readable
JSON approvals and batch-only preview excerpt here, extracting blocks to scratch
files for existing tool input. Full corpus output and validation scripts stay in
`/tmp/builder-2987/`. No detector, harness, production or corpus edits.

Classify documents and their individual findings as historical and still useful,
superseded/misleading, or unresolved. Missing paths stay intact. Notices give exact
wording, owning-document links and insertion headings outside removal ranges.
Retain questions carrying useful rationale or deferred requirements. Approve only
eligible resolved/obsolete sections with no unique useful decision to lose.
Unsupported #88 and merge-ineligible #27 receive no approval. Reuse current section
identities, never the pre-backfill report. Validate manifest coverage independently
from the ledger, pair uniqueness and section identities against both tree outputs.

Sizing: fewer than 700 written lines planned, zero exported types/interfaces,
zero consumer updates, four acceptance criteria, zero state-machine reject branches.
Remote overlap check found feature/2873 and feature/2882; neither touches this plan
or the selected historical documents. No unmerged dependency is needed.

## Concurrency model

Offline, sequential review and preview. No new goroutines or runtime lifecycle.

## Error handling

Unknown evidence remains explicitly unresolved. Stale, duplicate, unmatched or
ineligible approvals are rejected; diagnostics must be empty. A diagnostic section
range in unsupported Markdown does not authorize removal. Never apply the preview.

## Testing strategy

Reconstruct the scope inventory and preview from the pinned scope tree. Compare
selected document bytes, hashes, pairs and all-heading ordinals with the reviewed
tree. Validate all 45/37/43 entries once, notice headings outside approved ranges,
and one exact removal per approval. Reproduce the selected offline preview using
the committed approval block. Run race tests for both existing review commands,
`go vet ./...`, `go build ./cmd/pyry` and `git diff --check`; the verifier owns the
full-module gate. No live-Claude test is required.

## Open questions

Review outcomes and precise unresolved requirements will be recorded below and on
issue #2987. They add no implementation scope to this ticket.

## Documentation handoff

Pending for the documentation stage: apply only the completed ledger's exact
notices and approvals at its exact paths/headings. Preserve historical citations,
bodies outside approved removals and useful unresolved questions. After notice
insertion or any intervening edit, re-identify the same approved sections and
regenerate full-document hashes, all-heading ordinals and offline preview before
removal. Never authorize other documents from the temporary corpus report.
Any durable correction must name its exact owning knowledge path, heading and
replacement wording below. Frozen archives remain untouched.
