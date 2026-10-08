# Source reference inventory (#2928)

## Files read

- `cmd/docs-guard/main.go` → `main`, `featuresDir`: existing document guard, with a different scan scope and blocking purpose.
- `cmd/cite-guard/main.go` → `main`, `citeRe`: Go-comment citations are not a parser for historical Markdown references.
- `Makefile` → `spec-reference-inventory`: existing non-gating target invokes the new command without arguments.
- `docs/specs/README.md` § These are point-in-time artifacts, not current-state authority: preserve historical references.
- `docs/knowledge/features/development-verification.md` § Check searches and citations: inventory findings require checking current code; absence does not establish a false claim.
- `CODING-STYLE.md`: stdlib tests, contextual errors and synchronous command conventions.

## Context

Historical architecture specs cite files removed since their tickets shipped.
The inventory makes those references discoverable without changing historical
documents or judging their claims. #2931 owns that subsequent review. No decision
record is needed. QMD and codegraph found no reusable inventory implementation.

## Design

Keep all code in the unexported `cmd/spec-reference-inventory` main package.
`run(root, stdout) error` discovers directly contained Markdown specs, reads each,
deduplicates its accepted references, inspects targets and writes records sorted
by spec then referenced path. Output is headerless TSV: spec, path, and the literal
status `existing` or `missing`. Preserve directory-reference trailing slashes.
Inspect targets afresh on every invocation. No repository files are written.

The reference parser validates complete tokens rather than searching for root
substrings. Recognize whitespace/code/table delimiters, surrounding Markdown
punctuation and link-label/destination boundaries. Remove decimal line suffixes
(including ranges) and fragments, then validate root, component characters and
the `.go` or `/` ending. Reject URLs, globs, absolute or relative paths,
traversal components, placeholders, basenames and longer invalid references
whole. Do not resolve relative links or infer symbols. Read complete documents
without Scanner's default line-size limit.

No other feature branch currently touches these planned paths. Sizing against
the sketch and this plan: approximately 600 written lines, zero exported types,
zero updated consumers, four acceptance criteria, fewer than ten error branches.
The #1467 guard analogue is consistent with this bounded standalone command.

## Concurrency model

All discovery, parsing, inspection and output run synchronously. No goroutines,
child processes, cancellation plumbing or network operations are needed.

## Error handling

Discovery/read and target-inspection failures return contextual errors. Only
filesystem not-exist is a missing finding. Other failures reach stderr with the
command name and exit nonzero; stdout contains inventory records only. Collect
records before emitting them so a filesystem failure does not produce a partial
inventory. Output-write failures also return an error. Reading whole files avoids
a separate bounded-line scanning failure mode.

## Testing strategy

Write hermetic tests before implementation. Table-driven grammar cases cover
prose, inline/fenced code, tables, link labels/rooted destinations, suffixes,
component characters and whole-reference exclusions. Temporary trees prove file
and directory existence/deletion, per-spec deduplication, ordering, repeatability,
direct-only discovery and content/metadata non-mutation. Helper subprocesses
exercise actual stdout/stderr and exit behavior, including discovery, read and
inspection failures (broken spec links and target symlink loops avoid permission
tests that pass incorrectly as root). Check output-writer failure separately.
Run the touched package with race detection, `go vet ./...`, `go build ./cmd/pyry`
with the binary outside the worktree, and the existing make target on the corpus.
Record scanned specs, existing/missing records and checkout commit in the PR.
The verifier owns the full-module gate.

## Open questions

None. Token-boundary details will be pinned by accepted/excluded examples before
implementation; any change to the stated contract requires a Revisions entry.

## Documentation handoff

Pending for the documentation stage: add “Source reference inventory” in
`docs/specs/README.md`, naming `make spec-reference-inventory`, headerless TSV
fields/statuses, stdout/stderr and exit behavior, and the accepted grammar and
all exclusions from this ticket. Include exactly: “A missing historical path is
a review candidate, not proof of a false current-state claim; preserve historical
citations and check current code and knowledge docs.” Explain rerunning from the
repository root after source moves/deletions; findings do not fail `make check`.

## Revisions

- 2026-10-08, verifier findings 1 and 2: replace flat pipe splitting and the
  link regex with shared rune-aware token boundaries and balanced link labels.
  Table pipes delimit cells only outside code spans and bracketed components;
  outer-pipe rows and delimiter-led tables cover both table forms. Keep embedded
  pipes and link shapes in excluded references whole. Recognize commas between
  complete inline code citations and Unicode whitespace before link labels.
  Add parser and temporary-tree regressions for both findings. The inventory
  format, filesystem behavior and documentation handoff remain as designed.
