# Knowledge Base Index

Start here in every development session. Read the topic for the task, then search
for details. Do not load the complete catalog into every run.

## Architecture

- [System overview](architecture/system-overview.md): package boundaries and data flow.
- [Coding style](../../CODING-STYLE.md): Go and persistence conventions.
- [Shared knowledge workflow](../shared-knowledge.md): where to read and record lessons.

## Features

- [Protocol](features/protocol-package.md): wire types and validation boundaries.
- [Sessions](features/sessions-package.md): session registry and runner lifecycle.
- [Stream supervision](features/streamsup-package.md): child events and turn lifecycle.
- [Message queue](features/msgqueue-package.md): queue state and delivery.
- [Development verification](features/development-verification.md): testing, captures and source-reading practices.
- [Fake integration harness](features/e2e-harness.md): offline integration tests.
- [Live Claude tests](features/e2e-realclaude.md): authenticated integration tests.
- [Release tooling](../release-tooling.md): test tiers and installation gates.

## Decisions

[The decision catalog](CATALOG.md#decisions) links the recorded architectural choices.
Read the relevant decision when changing that boundary.

## Finding a document

[The full catalog](CATALOG.md) retains the detailed document inventory.
Search it or use QMD with the `pyrycode-docs` collection.
Historical ticket notes under `codebase/` and old plans under `../specs/` describe
past trees. Check current code before reusing their conclusions.

## Recording knowledge

Pipeline builders and verifiers record discoveries on their ticket or PR.
The documentation stage is the sole pipeline writer of `docs/knowledge/`.
It updates the owning topic and the catalog when a new document is needed.
Change this index only when the top-level topic map changes.
Interactive maintenance may update those documents directly in a reviewed change.
Claude's local memory is disabled and is not a knowledge source for development.
