# `cmd/qmd-current` — current documentation search setup

The [shared knowledge guide](../../shared-knowledge.md#read) owns the setup
invocation, index refresh order and maintainer adoption handoff. Collection setup
is separate from [agent search-access detection](memorysearch-package.md#effective-evidence).

## Configuration reconciliation

In `setup`, the canonical collection root is the resolved checkout's
`docs/knowledge`, with pattern `{features,decisions}/**/*.md`. Checking those
two fields alone is insufficient: a retained `ignore` rule can still hide required
feature documents. Reconciliation removes the current collection's `ignore`,
including when root and pattern already match. An unchanged rerun requires all
three conditions and leaves the configuration bytes untouched.

YAML anchors and aliases can make collections share a mapping. Editing that
mapping directly can silently narrow the broad collection and lose historical
search results. `setup` resolves YAML values and copies both the collection
registry and the current collection before editing. Other collections, settings,
and global, collection and path contexts retain their resolved values. YAML
formatting can change during reconciliation; preserving values does not mean
preserving the original anchor syntax or comments.

Configuration replacement is atomic after validation. Run setup while other
configuration writers are idle; atomic replacement does not merge concurrent
edits.

## Verification

`TestQMDCurrentSetup` invokes the shipped command and installed QMD against a
temporary repository-shaped corpus, from an unrelated working directory.
`newQMDFixture` isolates configuration, SQLite storage and caches before any QMD
invocation. Run the repeatable verification with installed QMD and Node:

```sh
go test -race -count=1 -v ./cmd/qmd-current/...
```

Compare `qmd ls` membership with the exact direct and nested feature/decision
Markdown paths after `qmd update`. A successful setup exit or an empty excluded
search cannot prove scope: use included searches and broad-collection positive
controls for the excluded sentinels, as described in
[search verification](development-verification.md#check-searches-and-citations).
Index an incorrect collection before correcting it to prove stale members vanish.

Exercise `ignore` with both wrong and already-correct root/pattern values. Use
real YAML anchors and aliases in both directions; independent JSON mappings
cannot expose shared-mapping corruption. Compare every other collection's indexed
membership, complete resolved configuration outside the reconciled fields, and
QMD's context listing. These checks detect historical documents disappearing
even when current-collection searches pass.

`TestQMDCurrentDefaultAndInvalidConfig` checks checkout inference without indexing
the real corpus and verifies malformed configuration remains intact on failure.
Missing QMD skips the integration tests explicitly; inspect executed tests and
skip reasons before claiming verification. Collection membership and keyword
search checks need no embeddings or live-Claude execution.
