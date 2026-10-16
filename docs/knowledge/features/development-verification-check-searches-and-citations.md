# Check searches and citations

An empty search is not proof of absence. Run a known-present control with the same
flags and paths. Use shell arrays for path lists. Use fixed-string matching for
literal text and check the regular-expression dialect before relying on boundaries.
Search a short fragment when a quoted comment may wrap across lines, then read the
named declaration. Check that cited symbols still exist and still support the claim.
Keep historical citations in historical documents unchanged.

Use the [source reference inventory](../../specs/README.md#source-reference-inventory)
after source moves or deletions to locate historical architecture-spec paths
absent from the current checkout. A missing path identifies a review candidate,
not a false current-state claim; an existing path alone does not validate the
historical claim either. Check current code and knowledge docs before reuse.

Use [scaffolding maintenance](../../specs/README.md#scaffolding-maintenance) only
with verified merged-PR closing links and an unambiguous ticket identity. A closed
issue can have no merged implementation, and a merged implementation can leave
explicitly deferred questions. Require an entire trivial resolution body or an
individual approval bound to the document hash and heading ordinal; a leading
"None" followed by prose and repeated headings cannot safely be classified by
title alone. Preserve historical designs and citations.

Markdown deletion needs block boundaries, not just heading-shaped lines.
Setext titles can span paragraph lines; thematic breaks, containers and reference
definitions change those bounds. `parse` in `cmd/spec-scaffolding-prune` retains
unsupported documents rather than guessing, and `run` prevents removal ranges
from overlapping retained questions. Structural blanks and fence endings accept
only spaces/tabs: Go's `TrimSpace` treats Unicode content as blank and can hide a
boundary or close a fence early. `markdownBlank` separates those checks from
title/resolution trimming; `markdownLines` preserves LF, CRLF and lone CR offsets.
`TestCommonMarkStructuralWhitespace`, `TestCommonMarkWhitespaceAmbiguity` and
`TestCommonMarkLineEndings` exercise preview, exact surviving bytes and repeated
apply. A parser regression can otherwise erase durable design while ordinary
LF/ASCII fixtures stay green.

Reusable GitHub evidence needs completeness and consistency across the corpus.
`gh api graphql --paginate` can stop at a nested connection while exiting zero;
verify final outer pages and nested `totalCount` values against returned nodes.
Validate PR identity facts across every issue association, since one PR can close
several issues: per-issue checks alone miss contradictory merge timestamps.
`snapshot.validate` and `TestCrossIssuePRIdentity` pin that shared-evidence boundary.

Reference extraction must validate whole tokens: splitting quotes inside a path
can turn a quoted placeholder into a root-directory citation, while stripping
periods after a slash can turn traversal or ellipsis placeholders into accepted
directories. Link extraction and tokenization must share boundaries; protecting
pipes only during tokenization still lets link-shaped glob components yield
false interior paths. In `cmd/spec-reference-inventory`, `separateLinks` and
`referenceBoundaries` share delimiter rules, including `markdownEscaped`'s
odd/even backslash parity. Balancing escaped brackets as syntax can omit both
link references and table citations on later lines. Keep accepted-context
regressions alongside whole-reference exclusions: `TestReferences`,
`TestInventoryMarkdownBoundaries` and `TestInventoryMarkdownEscapes` cover nested
labels, adjacent code citations, Unicode whitespace and escaped delimiters.

When correcting a behavioural claim, search prose as well as symbols. Counts, key
lists, test headers and comments can encode the same claim without sharing its words.
Run dependent tests and compile relevant build-tagged packages. A green assertion
can retain a false rationale after the behaviour it described has changed.

[Development verification overview](development-verification.md).
