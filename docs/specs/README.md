# Specs

Per-ticket build artifacts. Created during the development pipeline.

Subdirectories are created as needed:
- `architecture/` — architect output
- `code-reviews/` — code review reports

## These are point-in-time artifacts, not current-state authority

Each spec here records the design when its ticket is refined. Mechanical
maintenance may later remove expired scaffolding under the rules below; retain
point-in-time designs and historical citations, with Git history as the archive
of removed scaffolding. A spec records what was *designed* at that moment —
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

## Scaffolding maintenance

From the repository root, preview using a previously verified GitHub snapshot,
then apply explicitly after reviewing the JSON report:

```sh
./scripts/agent-tool.sh spec-scaffolding-prune -evidence ../pyrycode-agents/tools/pyrycode/cmd/spec-scaffolding-prune/testdata/2929-evidence.json > /tmp/scaffolding-preview.json
./scripts/agent-tool.sh spec-scaffolding-prune -evidence ../pyrycode-agents/tools/pyrycode/cmd/spec-scaffolding-prune/testdata/2929-evidence.json -apply > /tmp/scaffolding-apply.json
```

The shipped command is offline and scans only directly contained regular
`docs/specs/architecture/*.md` files. Preview is the default and changes no files.
Apply emits the same report before writing; compare it with the reviewed preview.
Regenerate and verify the preview if the checkout, document bytes, evidence or
approvals change. The report records the checkout commit, evidence provenance,
retrieval time and hash, document hashes, one-based heading ordinals, byte ranges,
actions and reasons. Count fields state their units: scanned/eligible documents
and proposed-removal/deferred-question sections. Discovery, input, output and
write failures produce stderr diagnostics and a nonzero exit. Writes recheck
document bytes and use temporary-file sync/rename with original permissions;
there is no transaction across documents, so inspect failures before resuming.

### Acquire and reuse evidence

Metadata acquisition is separate from the command and hermetic verification.
Reuse the committed snapshot for its recorded scope, or follow the two batched,
paginated GraphQL reads in the
[plan's Revisions](architecture/2929-spec-scaffolding-prune.md#revisions): repository
issues with number/state and merged PRs with number/merge timestamp and
`closingIssuesReferences`. Verify no API errors, complete final outer pages,
unique issue numbers, and each nested link's `totalCount` against its returned
node count before flattening the responses. Record repository, RFC3339 retrieval
time, queries, page counts and response hashes in the reusable snapshot's
provenance. A successful API exit alone does not prove pagination completeness.

The snapshot JSON has `repository`, `retrieved_at`, `provenance` and `issues`.
Each issue has `number`, `state` and `prs`; each associated PR has `number`,
`repository`, `merged_at` and `association: "closingIssuesReferences"`. Append PR
records only through those closing links to issues in `pyrycode/pyrycode`.
The shipped reader accepts this association provenance; it does not collect
metadata or verify it against GitHub during pruning.

Eligibility requires an unambiguous positive ticket identity, closed issue state
and positive merged-PR closing-link evidence in this repository. A numeric
filename prefix or positive integer `ticket:` in leading frontmatter identifies
the issue; both must agree when present. Mentions, titles, branches and closed
state alone do not authorize pruning. Missing, malformed or conflicting
identities, open issues, closed issues without positive merge evidence, unknown
states and missing/contradictory evidence remain unchanged with reasons. PR
identity contradictions are checked across the snapshot, including one PR
closing several issues; future merge timestamps relative to retrieval are invalid.

### Section selection and approvals

Titles match only exact `Files to read first` or `Open questions`, ignoring case,
heading syntax and surrounding whitespace. Numbered, suffixed and similar
titles stay unchanged. ATX and Setext sections end before the next equal or
shallower heading, or at EOF; frontmatter and fenced examples are excluded.
Removal slices original bytes, preserving frontmatter, neighboring headings,
line endings (LF, CRLF or lone CR) and final-newline state outside those ranges.

An entire trimmed question body must be empty or equal, ignoring case, to
`None`, `None.`, `No open questions.` or `No outstanding questions.` for automatic
removal. Merge, a "resolved" title, or a leading "None" followed by prose is
insufficient. Nontrivial questions stay unless individually approved; removal
of a containing reading list is also withheld if it overlaps a retained question.

For a reviewed nontrivial question, pass `-approvals /path/to/approvals.json`
to both preview and apply. The approval file is a JSON array, with each entry
identifying the repository-relative document, SHA-256 of its full reviewed bytes,
and one-based ordinal among all parsed headings:

```json
[{"document":"docs/specs/architecture/707-interrupt-wire-type-esc-routing.md","sha256":"<full reviewed document SHA-256>","heading":15}]
```

Repeated headings need separate entries. Stale, duplicate/ambiguous or unmatched
approvals authorize no removal and appear in `approval_diagnostics`; approvals
never bypass document eligibility. Use fresh report identities after any edit.
The initial backfill uses no approvals. All unapproved nontrivial questions,
including #707's multi-phone and ACP deferrals, remain for [#2931](https://github.com/pyrycode/pyrycode/issues/2931).

### Unsupported Markdown

The conservative parser retains an entire document with an
`unsupported Markdown: <construct> at line N` reason when it cannot establish
safe bounds. This includes fences, HTML blocks, headings, Setext underlines or
reference definitions inside containers; tabs in container indentation;
indented/contained headings or indented HTML; fences indented under an open
container; and nonblank lines less indented than their open fence. It also
rejects ambiguous Setext paragraphs (indented, starting with `[` or potentially
lazy container continuations), container continuations and type-7 HTML whose
paragraph/definition context it cannot model. Recognized matching sections may
still appear as retained diagnostic records; their ranges are not removal
authority. Inspect the reported reason before planning any later manual work.

The [first backfill preview](2929-backfill-preview.json) is reproducible from
input checkout `6155317389a788a8ced84452ba1b41e12e5e5a97` with the verified
snapshot retrieved on 2026-10-08. It scans 1,207 documents, finds 1,189 eligible
documents and proposes 722 section removals in 720 documents (707 reading lists
and 15 trivial question sections), retaining 1,081 question sections and all five
unsupported documents. The initial application removes those exact 722 sections
from 720 documents; no nontrivial questions are approved. Repeated apply on the
pruned checkout proposes zero removals and retains 1,081 question sections.
