# Historical specs review: batch 02 (#2988)

## Files read

- `docs/specs/README.md` → historical authority boundary, inventory grammar and content-bound approvals.
- `docs/knowledge/features/development-verification.md` § Check searches and citations → absence controls, preserving historical citations and unresolved deferrals.
- `cmd/spec-reference-inventory/main.go` → `run`: unique document/target records and checkout existence checks.
- `cmd/spec-reference-inventory/references.go` → reference extraction contract.
- `cmd/spec-scaffolding-prune/main.go` → `run`, `approval`, `report`: full-byte hashes, heading identities, offline preview and diagnostics.
- `cmd/spec-scaffolding-prune/markdown.go` → `parse`: conservative section boundaries and unsupported-document retention.
- `cmd/spec-scaffolding-prune/evidence.go` → `eligibility`: closed issue plus merged closing-link evidence.
- `cmd/spec-scaffolding-prune/testdata/2929-evidence.json` → reusable evidence; no metadata refresh or live capture.
- The 46 issue-manifest specs → review only the snapshot candidates and their surrounding decisions.
- Owning feature documents and current declarations → verify each disposition before authorizing a notice or removal; detailed evidence map follows in the ledger.

## Context

Historical specs preserve useful reasoning but can advertise retired attach, authentication or persistence designs. Missing references and substantive question bodies are candidates, not proof of errors. Review the exact #2988 manifest at scope commit `bf3129a42d38d257a3085f05dde5079a275c84a6` against reviewed-current commit `f2a41c4b5bf2e72437eb1dc97b47b8178ff86429`.

This is one deliverable: an evidence-backed batch review with documentation authorization. No product behavior, detector or harness changes. No new decision record is anticipated.

## Design

Keep all committed artifacts in this plan, respecting the builder's allowed-file boundary. Add a document ledger, a missing-reference-pair ledger and per-question dispositions, followed by separately extractable JSON approvals and a compact scoped preview. Exact paths and heading ordinals identify findings; repeated headings are distinct identities.

Recover the scope inventory from an archive of the scope tree. Use the shipped inventory and pruning commands on current bytes; retain full corpus outputs only under `/tmp/builder-2988/`. Compare scope/current full bytes and map changed identities if needed. Preserve purely historical references. For misleading prose, give exact notice wording and an insertion heading outside all removal ranges. Retain questions containing useful decisions or unresolved requirements unless those contents are demonstrably duplicated elsewhere or obsolete.

Extract the approvals JSON to scratch, preview with the existing evidence, then filter the generated report to the manifest. Preserve generated checkout/evidence provenance, full-document hashes, actions, reasons and byte ranges. Batch counts must not inherit corpus counts. Nothing invokes apply.

Sizing before commit: four acceptance criteria; zero exported types/interfaces, consumer changes or state-machine reject branches. Estimated total written work below 700 lines including ledger, JSON, validation notes, PR and documentation notices. Only this new plan path will change; fetched branches #2873 and #2882 do not touch it. Review snapshot code remains fixed even if origin/main advances.

## Concurrency model

Read-only offline commands; no added goroutines, runtime state or shutdown paths.

## Error handling

Unsupported/ineligible documents receive no approvals. Stale, duplicate or unmatched approvals fail review validation. Insufficient evidence yields an explicit unresolved disposition identifying the unknown requirement, retained visible questions and an issue comment. No historical citation substitution or inferred resolution from merge alone.

## Testing strategy

Validate exact manifest equality and uniqueness: 46 documents, 33 scope missing document/target pairs across 16 documents, and 46 scoped question occurrences. Compare archived/current hashes and heading identities. Validate cited current declarations, evidence eligibility, approval-to-removal bijection, empty approval diagnostics and scoped preview reproducibility. Run the existing two command packages' race tests; no new tests or production edits. Full-module tests remain the verifier's gate.

## Open questions

- Resolve each candidate through current declarations and owning knowledge; record any remaining unknowns individually in the ledger and on #2988.
- Decide which question bodies contain unique useful decisions or still-deferred requirements and must remain visible.

## Documentation handoff

Pending for the documentation stage: apply only exact notices and approved question removals recorded in the completed ledger at its exact architecture paths/headings. Preserve all historical citations and bodies outside approved ranges, including useful unresolved questions. After notices or intervening edits, re-identify the approved sections and regenerate full-byte hashes, heading ordinals and offline preview before removal. Corpus-report entries outside these 46 documents confer no authority.

For any evidence-backed durable correction, record the exact owning `docs/knowledge/features/` or `docs/knowledge/decisions/` path, heading and replacement wording here and in the PR. If none is found, make no knowledge correction. Never edit `docs/knowledge/codebase/`, `docs/lessons.md` or `docs/PROJECT-MEMORY.md`. Unresolved requirements stay on #2988, with no implementation added.
