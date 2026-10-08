# #2929 — Prune expired merged-ticket scaffolding

## Files read

- `cmd/spec-reference-inventory/main.go` → `main`, `run`: standalone maintenance command and deterministic discovery precedent.
- `cmd/spec-reference-inventory/main_test.go` → `TestInventory`, `TestOutputFailure`: temporary-tree and output-failure patterns.
- `docs/specs/README.md` → point-in-time artifacts: preserve historical designs and citations.
- `docs/knowledge/features/development-verification.md` → Check searches and citations: absent or outdated references are review candidates, not deletion authority.
- `docs/specs/architecture/707-interrupt-wire-type-esc-routing.md` → Open questions: deferred multi-phone and ACP work must survive.
- `CODING-STYLE.md`: stdlib tests, error propagation, context cancellation and persistent writes.

## Context

Merged specs retain reading lists and resolved question scaffolding that dilute search. Only exact scaffolding sections with positive GitHub merge evidence may be pruned. One standalone deliverable, no exported types or consumer migrations. No decision record needed. #2928 is merged; feature branches #2873 and #2882 have no overlapping target files.

Sizing: four acceptance criteria; approximately 730 manually written production/test/plan lines, zero exported types, zero changed consumers, fewer than ten eligibility/reject outcomes. Generated evidence and preview records are corpus artifacts, not manually authored logic. Recount before plan commit confirms these limits.

## Design

Add `cmd/spec-scaffolding-prune/` with `main.go` (CLI, preview and apply), `markdown.go` (identity and byte ranges), and `evidence.go` (snapshot and optional acquisition). Default invocation from repository root: `go run ./cmd/spec-scaffolding-prune -evidence SNAPSHOT.json`; `-apply` explicitly enables writes. `-approvals FILE.json` supplies optional content-bound approvals. `-collect-evidence FILE.json` runs a separate network-only acquisition, without scanning or pruning.

Acquisition paginates repository issues and merged PRs once across the corpus using `gh api graphql`. PR closing-issue associations are positive evidence; nested association pagination must be complete. Snapshot records repository, retrieval time, provenance, issue state and merged PR number/time/repository/association. Offline eligibility fails closed on absent, duplicate or contradictory issue records and invalid PR evidence. No inference from mentions, titles or branches.

Discovery reads only directly contained regular `.md` files, preserving bytes. Anchored numeric filename prefix and optional positive integer `ticket:` in leading frontmatter must agree; malformed/conflicting identities remain unchanged. Parse ATX and Setext headings outside frontmatter and backtick/tilde fences. Exact case-insensitive titles select sections ending at the next equal/shallower heading or EOF. Slice original bytes; never render Markdown again.

Each matching section has a document path, SHA-256 of full document, heading ordinal (among all parsed headings), title, and byte start/end. Empty or exact whole-body resolution literals authorize question removal; all other questions require one unique approval matching path, hash and ordinal. Duplicate, stale and unmatched approvals are reported and authorize nothing. Eligibility always applies first.

JSON preview includes checkout commit, evidence hash/provenance/time, all document eligibility reasons, every matching section action/reason and document/section counts with explicit units. Fully discover and emit preview successfully before any write. Apply rechecks document contents, uses temporary-file sync/rename with original permissions, and preserves all bytes outside removal ranges. No transactional promise across multiple documents; failures are explicit and a repeat safely resumes.

## Concurrency model

Sequential scan and writes; no new goroutines. CLI signal context cancels GitHub subprocesses. Acquisition is separate from hermetic pruning and verification.

## Error handling

Bad flags, invalid snapshot envelope/JSON, discovery/read/output/subprocess/write failures return contextual errors and nonzero exit. Unknown ticket/evidence states and rejected approvals are reported findings, never permission to remove. Symlinks/nonregular Markdown entries are diagnosed before output or writes.

## Testing strategy

Write temporary-tree tests first, observe missing implementation failure, then implement. Cover issue states and merge provenance, duplicate evidence, frontmatter agreement/conflict/malformed identity, exact titles, Setext/ATX boundaries, both fences, CRLF and final newline preservation, EOF, #707-style deferred questions, repeated/stale/duplicate/unmatched approvals, eligibility precedence, preview non-mutation, apply idempotence and input/output failures. Acquisition uses injected fake GraphQL pages; no API in tests. Run scoped race tests, module vet and binary build.

Commit a reproducible corpus preview plus its reusable evidence under `cmd/spec-scaffolding-prune/testdata/`. Generated artifacts identify the scanned checkout; PR links them and states counts. No corpus application in this stage.

## Open questions

None. Historical PR closing links may be missing; affected documents remain unchanged with evidence reasons.

## Documentation handoff

- Pending documentation stage: apply and commit the first safe backfill only to exact `docs/specs/architecture/` paths/sections in the verified preview; use no nontrivial-question approvals. Regenerate/verify preview if checkout or evidence changes. Report changed-document, removed-section and deferred-question-section counts; preserve unapproved nontrivial questions including #707 for #2931.
- Pending documentation stage: `docs/specs/README.md` → new “Scaffolding maintenance” section: preview/apply invocations, metadata acquisition/reuse, positive merge evidence, reported unknown states, conservative heading/resolution matching and content-bound individual section approvals.
- Pending documentation stage: `docs/specs/README.md` → “These are point-in-time artifacts, not current-state authority”: amend “written once … never updated afterward” to allow mechanical expired-scaffolding removal while retaining point-in-time designs and historical citations.

## Security review

**Verdict:** PASS

- [Trust boundaries] Snapshot envelope validated; per-issue contradictory evidence fails closed. Only GitHub closing links from the fixed repository grant eligibility. JSON approvals bind whole-document hashes and individual ordinals.
- [File operations] SHOULD FIX: reject symlink/nonregular specs, recheck contents before apply, and atomically replace with preserved permissions. Paths originate from direct directory entries, never approval input.
- [Subprocess/network] Fixed `gh api graphql` arguments, no shell or authentication changes; context cancellation. API access confined to optional acquisition.
- [Tokens/cryptography] No credentials read or printed; SHA-256 binds reviewed document bytes, not secrets.
- [Errors/logs] Diagnostics contain public paths and metadata only. Output failure prevents writes.
- [Concurrency] Sequential execution; no goroutine lifecycle or lock-order changes.
- [Threat model] Local repository maintenance; relay/mobile threat model does not apply. Malicious concurrent filesystem mutation beyond content recheck is outside this trusted-checkout tool's scope.

**Reviewer:** builder (self-review)
**Date:** 2026-10-08

## Revisions

2026-10-08: The completed sketch's acquisition CLI pushed written work beyond the ceiling. Replace the optional `-collect-evidence` CLI and acquisition-test portions above with the ticket's verified-snapshot route. The shipped command only previews/applies offline snapshots. Batched acquisition occurs separately; the durable snapshot records repository, retrieval time, positive PR associations and exact query provenance. All pruning contracts stay the same. Scanning unversioned temporary trees leaves checkout identity empty; the corpus artifact requires and records a real checkout commit.

Acquire reusable metadata with these two paginated reads, then flatten issue nodes into snapshot issue records and append merged PR records only through closing links in this repository. Verify both final outer pages have `hasNextPage: false`, no API errors, no duplicate issue numbers, and every nested link `totalCount` equals its returned node count; reject an incomplete acquisition. Snapshot provenance records the queries, page counts and source-response hashes.

```sh
gh api graphql --paginate -f query='query($endCursor:String){repository(owner:"pyrycode",name:"pyrycode"){issues(first:100,after:$endCursor){nodes{number state} pageInfo{hasNextPage endCursor}}}}' > /tmp/issues-pages.json
gh api graphql --paginate -f query='query($endCursor:String){repository(owner:"pyrycode",name:"pyrycode"){pullRequests(first:100,states:MERGED,after:$endCursor){nodes{number mergedAt closingIssuesReferences(first:100){totalCount nodes{number repository{nameWithOwner}}}} pageInfo{hasNextPage endCursor}}}}' > /tmp/pr-pages.json
```

The nested closing-link connection uses `totalCount`, because `gh --paginate` can stop at a nested `pageInfo` instead of the outer PR cursor. Another conservative clarification: no removal range may overlap a retained question section, including containing scaffolding. Future merge timestamps relative to snapshot retrieval and conflicting duplicate PR records are contradictory evidence.

Reproduce preview from repository root with `go run ./cmd/spec-scaffolding-prune -evidence cmd/spec-scaffolding-prune/testdata/2929-evidence.json`. Approval format is a JSON array of `{ "document": "docs/specs/architecture/707-example.md", "sha256": "full document SHA-256", "heading": 2 }`; heading ordinals are one-based among all parsed headings, and only exact question sections can match. Initial backfill uses no approvals. Generated corpus artifacts are reviewed data, not hermetic golden fixtures.
