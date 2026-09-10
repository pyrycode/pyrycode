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
