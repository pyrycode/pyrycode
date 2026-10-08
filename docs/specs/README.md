# Specs

Per-ticket build artifacts. Created during the development pipeline.

Subdirectories are created as needed:
- `architecture/` — architect output
- `code-reviews/` — code review reports

## These are point-in-time artifacts, not current-state authority

Each spec here is written once, when its ticket is refined, and never updated
afterward. A spec records what was *designed* for that ticket at that moment —
not how the system behaves now. Treat every file in this tree as historical:
it is never the authority on current behaviour, and some specs are explicitly
banner-stamped as superseded. For how the system works today, consult
`docs/knowledge/`, not the specs.

## Source reference inventory

Run `make spec-reference-inventory` from the repository root after source moves or
deletions, or before relying on historical source references. The command scans
only directly contained `docs/specs/architecture/*.md` files against the current
checkout. It changes no repository files and requires no network. Findings do
not fail `make check`; the inventory runs separately from that gate.

Stdout contains headerless TSV with three fields: repository-relative spec path,
referenced path, and the literal status `existing` or `missing`. Each distinct
(spec, path) pair appears once, sorted by spec then path; the same path cited by
different specs retains separate records. Directory paths keep their trailing
slash. Repeated runs on the same tree produce byte-identical output; target
existence is checked afresh, so deleting a target changes its next-run status to
`missing`.

A successful scan exits zero even when targets are missing. Execution errors in
spec discovery, reading/scanning, target inspection or output writing produce
diagnostics on stderr and a nonzero exit. A nonexistent target is a finding;
other target-inspection failures are execution errors. Discovery, read and
inspection failures occur before any inventory records are emitted.

The reference grammar accepts complete repository-rooted paths starting with
`cmd/` or `internal/` and ending in `.go` or `/`. Slash-separated components
contain only ASCII letters, digits, underscores, hyphens or dots. References can
appear in prose, inline or fenced code, tables, Markdown link labels and rooted
local link destinations. Surrounding Markdown punctuation, colon-prefixed decimal
line numbers with optional hyphenated end numbers (such as `:12` or `:12-34`),
and fragments (such as `#entry`) are omitted from the reported path.

Whole URLs, globs, absolute paths, traversal-bearing paths (`.` or `..`
components), placeholders, basename-only references and longer invalid paths are
excluded. For example, `internal/*/file.go`, `internal/<pkg>/file.go`,
`internal/.../file.go`, `main.go` and `internal/pkg/file.go.bak` produce no
records. No qualifying prefix or interior substring is extracted from an
excluded reference. Relative link destinations are not resolved, and symbols
are not inferred.

A missing historical path is a review candidate, not proof of a false current-state claim; preserve historical citations and check current code and knowledge docs.
