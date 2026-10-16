# Development verification

Practices shared by Claude, Codex and the pipeline. These consolidate recurring
review findings. Historical ticket examples remain in the vault archive; current
source and the owning package documentation decide current behaviour.

## Establish the change surface

Count construction sites and consumers, not only type declarations. A payload field
can require several builders and narrow interfaces to change. Include test doubles
when widening a function type. A named configuration field may avoid breaking every
function literal, but check whether that shape suits the contract.

A matching method name does not prove two interfaces are the same dependency.
Trace the field's declared type and its assigned implementation before counting hits.
Codegraph has previously missed cross-package callers and selected the wrong symbol
when names collide. Confirm the package identity and supplement its edges with a
source search for qualified calls. The canonical index cannot see pending edits.

A new event variant has consumers beyond exhaustive test tables. Search both its
marker interface and the type switches. Default branches can silently ignore a new
variant even after the build passes. Check logging names, payload mapping, lifecycle
handling and internal busy-state tracking separately.

## Verify inherited premises

A merged dependency may have implemented more or less than the ticket predicted.
Find the production installation and the actual call site. Read existing tests before
rebuilding the same proof. Search specifications for explicitly deferred vocabulary,
missing producers and unwired callers before estimating the remaining work.

Read the validator's complete rejection behaviour before copying it. A membership
check and a live-session routing check serve different requests. Inspect the sibling
that deliberately avoids validation; its explanation often states the boundary of
an exception more clearly than the validating example.

Before copying a sibling's late guard, trace the target operation to its first side
effect. An operation that already returns without changing state may not need that
guard. A shared request shape does not establish identical side effects.

## Sections

| Topic | Contents |
| --- | --- |
| [Check searches and citations](development-verification-check-searches-and-citations.md) | Source searches, citations and evidence inventories. |
| [Prove that tests distinguish the change](development-verification-prove-that-tests-distinguish-the-change.md) | Controlled regressions, ordering, cancellation and mutation checks. |
| [Protocol boundaries](development-verification-protocol-boundaries.md) | Wire validation, round trips and compatibility assertions. |
| [Captures and live evidence](development-verification-captures-and-live-evidence.md) | Capture provenance, live inputs and protocol artifacts. |
| [Test execution and artifact survival](development-verification-test-execution-and-artifact-survival.md) | Gate counts, runtime diagnosis and durable test inputs. |
