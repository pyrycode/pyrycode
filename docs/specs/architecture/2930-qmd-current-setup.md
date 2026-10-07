# Evergreen QMD collection setup (#2930)

## Files read

- `docs/knowledge/INDEX.md` → Finding a document: current broad-search guidance.
- `docs/shared-knowledge.md` → Read and Capture: documentation-stage ownership.
- `docs/knowledge/features/development-verification.md` → Check searches and citations: excluded searches need known-present controls.
- `docs/knowledge/features/memorysearch-package.md` → Effective evidence: runtime QMD detection is separate from maintainer collection setup and needs no change.
- `CODING-STYLE.md` → Testing and Persistent data conventions: behavioral assertions and atomic configuration replacement.
- Installed QMD 2.1.0 `collections.js` → configuration paths and `loadConfig`: `QMD_CONFIG_DIR`, XDG fallback and YAML `index.yml` contract.
- Installed QMD 2.1.0 `store.js` → `syncConfigToDb`, `reindexCollection`: CLI imports YAML settings; update deactivates stale indexed paths after scope correction.
- Installed QMD README → Collections and Context: root, pattern, global and path context fields.

## Context

The broad `pyrycode-docs` collection mixes current explanations with historical
specs. Supply one repository-local setup command for a separate current collection,
without copying documents or changing daemon/runtime search access. Documentation
and shared-host provisioning remain later-stage/maintainer work. No decision record
is needed. No feature branches overlap the planned paths.

## Design

`node /absolute/checkout/cmd/qmd-current/setup.mjs [--repo /absolute/corpus]`
reconciles the default QMD index configuration. Infer the checkout from the script's
location, independent of the caller's working directory; `--repo` permits an isolated
repository-shaped corpus. Require installed QMD and its Node runtime. Resolve QMD's
installed executable to reuse its YAML parser dependency, with no new dependencies
or imports of private QMD APIs.

Edit only `collections.pyrycode-current.path` and `.pattern` in supported YAML
configuration, using canonical `docs/knowledge` and `{features,decisions}/**/*.md`.
Retain other collections, settings and every global, collection and path context,
including those attached to the current collection. Use a YAML document edit and
atomic same-directory replacement; an unchanged rerun performs no write.

Honor `QMD_CONFIG_DIR` (otherwise `XDG_CONFIG_HOME/qmd`, then `~/.config/qmd`).
`INDEX_PATH` is consumed by subsequent QMD invocations. Setup does not index or embed:
the caller runs `qmd update` and then `qmd embed` against the same configuration/index.

## Concurrency model

Synchronous maintainer command; no goroutines or background jobs. Run setup while
other configuration writers are idle, as with QMD's own configuration commands.

## Error handling

Missing prerequisites, invalid arguments, missing corpus directories and malformed
configuration return nonzero with contextual diagnostics. Validate before writing;
failed replacement leaves the original intact and cleans up its temporary file.

## Testing strategy

Go tests under `cmd/qmd-current` invoke the actual Node command and installed QMD,
with all config/index/cache paths under `t.TempDir`, from an unrelated working
directory. Fresh setup, byte-stable rerun, wrong root and wrong pattern are separate
scenarios. Compare `qmd ls` membership with four direct/nested feature/decision files;
search each included sentinel and five excluded Markdown/non-Markdown sentinels,
using `pyrycode-docs` positive controls for all exclusions. Compare complete parsed
configuration outside the two edited fields and CLI context listings. Index the
incorrect collection before correction to prove stale membership disappears.
Also verify default checkout resolution without indexing the real corpus and failure
without corrupting malformed config. Report QMD version and membership in verbose
test output and the PR. Missing QMD skips the integration check explicitly.

Run scoped race tests, `go vet ./...`, and `go build ./cmd/pyry`. The verifier owns
the full-module gate. No embeddings, benchmark or live-Claude execution is needed.

## Open questions

None. The sketch and plan fit one deliverable, approximately 400 written lines,
zero exported types, zero consumer updates, three acceptance criteria and fewer
than ten command failure branches.

## Documentation handoff

Pending for documentation stage:
- `docs/shared-knowledge.md`, Read: document the shipped setup invocation and `--repo`, `QMD_CONFIG_DIR` and `INDEX_PATH` isolation options. State: “Use `pyrycode-current` for current feature and decision questions; use `pyrycode-docs` for historical ticket reasoning.” Explain running `qmd update` followed by `qmd embed` against the configured index. State that shared-host automatic provisioning and role defaults await adoption by the agents-repository maintainer.
- `docs/knowledge/INDEX.md`, Finding a document: link the above guidance.
- Agents-repository maintainer handoff: adopt `node "$PYRYCODE_REPO/cmd/qmd-current/setup.mjs"` in automatic provisioning; this branch does not edit that repository or its live configuration.

## Revisions

### 2026-10-07 — verifier findings 1 and 2

The original two-field edit retained `ignore` exclusions, so even the correct root
and pattern could omit feature Markdown. Reconciliation now sets the canonical
root and pattern and removes the current collection's `ignore` field. Other
settings, collections and all contexts remain unchanged. The no-write rerun also
requires the absence of `ignore`.

Editing an anchored YAML mapping also changed collections that aliased it. Resolve
the configuration through the installed YAML parser and copy the collection registry
and current collection before editing, then serialize the resulting document.
This detaches the edited mappings while preserving all other resolved values;
an alias used as the current collection is supported too. Configuration formatting
may change on reconciliation; an unchanged rerun remains byte-stable. Atomic
replacement and validation-before-write remain the same.

Installed-QMD regressions cover exclusions with both incorrect and already-correct
scope, a current mapping anchored for the broad collection, and a current collection
that aliases the broad mapping. Index before and after correction, compare every
other collection's membership, and retain the full configuration/context and search
assertions. The four new scenarios first failed against the prior implementation
with missing feature documents, lost broad history, or a rejected collection alias.
The revision remains one deliverable under 800 written lines, with zero exported
types and consumer updates, three acceptance criteria and fewer than ten failure
branches. No concurrent feature branch touches these files.
