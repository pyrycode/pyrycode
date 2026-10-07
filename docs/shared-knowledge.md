# Shared project knowledge

Claude and Codex use the same repository documentation and instructions.
Claude auto memory is disabled for this project. Do not read or write its local
memory directory for routine development. The archived source is historical
evidence for a deliberate audit, not a second set of current instructions.

## Read

Start at [the knowledge index](knowledge/INDEX.md), then read the owning topic.
Use QMD for documentation and source search for the current implementation.
The [catalog](knowledge/CATALOG.md) is a lookup table, not a startup reading list.
The old [project-memory path](PROJECT-MEMORY.md) is a compatibility pointer.

Use `pyrycode-current` for current feature and decision questions; use `pyrycode-docs` for historical ticket reasoning.

With Node installed and `qmd` on `PATH`, configure the current collection from
any working directory on Linux or macOS. Set `PYRYCODE_REPO` to the absolute
checkout path:

```sh
PYRYCODE_REPO=/absolute/path/to/pyrycode
node "$PYRYCODE_REPO/cmd/qmd-current/setup.mjs"
qmd update
qmd embed
```

Setup reconciles QMD's default `index.yml` to search only Markdown files directly
or recursively under this checkout's `docs/knowledge/features/` and
`docs/knowledge/decisions/`. It preserves other collections and all contexts.
Rerunning corrects an existing wrong scope without creating a duplicate collection.
See [the setup overview](knowledge/features/qmd-current-setup.md) for reconciliation
and verification details.

Setup writes configuration only. Run `qmd update` to index new or changed files
and remove stale membership, then `qmd embed` to refresh vectors for the indexed
documents. Embedding alone does not discover file changes. Keep the same
configuration and index environment for both commands; these instructions use
the default index, without QMD's `--index` option.

For isolated verification, use these options before running setup and QMD:

| Option | Effect |
| --- | --- |
| `--repo /absolute/temporary-corpus` on the setup command | Uses a repository-shaped corpus with `docs/knowledge/features/` and `docs/knowledge/decisions/`; otherwise the checkout is inferred from the script's location. |
| `QMD_CONFIG_DIR=/absolute/scratch/config` | Selects the directory containing `index.yml`; otherwise setup uses `$XDG_CONFIG_HOME/qmd`, falling back to `~/.config/qmd`. |
| `INDEX_PATH=/absolute/scratch/index.sqlite` | Selects QMD's SQLite index for subsequent commands; setup itself does not open it. |
| `XDG_CACHE_HOME=/absolute/scratch/cache` | Keeps QMD caches separate during verification. |

Export the environment variables in the shell running all three commands. Set
both `QMD_CONFIG_DIR` and `INDEX_PATH` to isolate configuration and indexed data;
`--repo` alone only changes the corpus.

Shared-host automatic provisioning and role search defaults await adoption by
the agents-repository maintainer. The invocation to adopt in
`pyrycode-agents/container/entrypoint.sh`, `write_qmd_config`, is
`node "$PYRYCODE_REPO/cmd/qmd-current/setup.mjs"`, followed by indexing with the
same environment. This repository-local setup does not provision shared hosts.

## Capture

| Knowledge | Home | Writer |
|---|---|---|
| Product behaviour and engineering lessons | The owning repository topic under `docs/knowledge/` | Documentation stage after a pipeline change; interactive maintainer in a reviewed change |
| Requirements, review findings and unfinished work | The ticket or PR | The role doing that work |
| Agent workflow and review practice | `pyrycode-agents/docs/working-practice.md` | Agent-workflow maintainer |
| Dispatcher behaviour | Dispatcher repository documentation | Dispatcher maintainer |
| Personal preferences, project direction and operating history | The vault | Interactive assistants |

A builder records a durable discovery in its PR's Lessons learned section.
A verifier records one in its review comment. A refiner records one on the issue,
including work that is split, parked or closed without a PR. Link that comment
from any child that continues the work. The documentation stage folds product
lessons into the owning topic, rather than creating a file for every ticket.
Workflow findings stay on the ticket until the agent-workflow maintainer folds
those into the shared practice document. They are never saved only in local memory.

Record the failure that would otherwise recur, its cause, and how to check it.
Do not repeat the implementation summary. Update an existing topic before adding
one. Historical observations need dates and a current-code check before becoming
instructions. The vault links to engineering documentation rather than maintaining
a second active copy. No agent writes the frozen per-ticket history.

## Maintenance

Keep topic documents within the existing size limit. Update the catalog only when
a document is added or removed. Re-index QMD after documentation changes.
Local memory trimming and curation are disabled for this consumer. Other dispatcher
consumers retain their existing policy until migrated explicitly.
