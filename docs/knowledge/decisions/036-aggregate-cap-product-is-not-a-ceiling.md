# 036. An aggregate cardinality cap's product is a result, not an inherited ceiling

## Status

Accepted (#1821)

## Context

`maxTaskRosterEntries` (#1381) derived its cardinality cap — 8 entries — from an
8x multiple over an observation of one roster entry, and its doc *noticed*, as a
side observation rather than a design goal, that the resulting product
(8 × 1024 = 8192 bytes) landed on exactly half of `maxUnrecognizedRaw`'s 16 KiB
whole-line budget for an UNKNOWN control line.

`maxModelListEntries` (#1812) inherited that noticed number as if it were a fixed
ceiling every future aggregate variant's product had to fit under, deriving its
own entry cap partly by solving for "stays under 8192" against a 768-byte
per-entry multiplicand.

When #1827/#1828 added a third size dimension to `ModelOption` — the effort-level
list's own cardinality — closing it (#1821) raised the honest per-entry
multiplicand to `768 + 32N`. Every count cap consistent with the family's own
doctrine floors (a level cap that doesn't fire on claude's five ordinary levels,
`N ≥ 6`; an entry cap that doesn't fire on claude's six ordinary entries,
`E ≥ 10`) produces a minimum product of `10 × (768 + 6×32) = 9600` — greater than
8192. No pair of caps satisfying the family's own doctrine could keep the
inherited number; the ceiling had become the binding constraint by accident.

## Decision

8192 is demoted from a constraint back to what it always was: `maxTaskRosterEntries`'
own product, which happened to land on half of `maxUnrecognizedRaw`. The rule the
roster actually stated was never "a later variant's product must stay under 8192";
it was "a whole KNOWN event must not approach the cap reserved for an entire
UNKNOWN line" — i.e., a fraction of `maxUnrecognizedRaw` (16 KiB) small enough to
leave that ordering intact.

Each aggregate variant now re-derives its own fraction of `maxUnrecognizedRaw`
from its own doctrine floors, rather than inheriting a prior variant's noticed
number. The roster's fraction is stated as 1/2; the model list's, after #1821, is
5/8 (10240/16384). Both are written out explicitly in the constant's own doc
rather than left implicit in an unlabeled byte count.

## Rationale

A derived quantity that happens to also satisfy a second, unrelated property —
here, landing on a clean fraction of a larger budget — is a coincidence worth
*noting*, not a constraint worth *inheriting*. Treating it as a ceiling silently
couples two independent doctrines (the per-entry cardinality/text-cap doctrine
that sizes one variant, and the whole-line KNOWN-vs-UNKNOWN ordering rule that
sizes the package's outer budget) through a number that was never chosen to serve
the second purpose. The coupling stays invisible until a later variant's own
doctrine floors conflict with it — which is exactly what happened here — at which
point the only ways out are to violate the ordering rule the ceiling was never
really protecting, or to weaken the family's own cardinality/text-cap doctrine to
fit a number nobody chose for that reason. Re-deriving the fraction each time
keeps the two doctrines from silently merging into one.

## Consequences

- The next aggregate variant added to this family re-derives its own fraction of
  `maxUnrecognizedRaw` from its own doctrine floors; it does not reuse 5/8 or 1/2
  as a starting constraint.
- A "the product happens to land on a clean fraction/round number" observation in
  a cardinality cap's doc is read as descriptive from here on, never prescriptive,
  for every cap in this family.
- The 1024-byte per-entry unit `maxTaskRosterEntries` and `maxModelListEntries`
  now happen to share (despite different field sets — 256+256+512 vs. 4×256) is
  documented as a number both landed on, not a rule the next variant must satisfy
  — the same demotion this decision performs on 8192, applied pre-emptively so it
  is not inherited as a constraint next time.

## Related

- [features/streamsup-package.md](../features/streamsup-package.md) § "The entry
  count is capped too" and § "The per-entry byte budget" — the constants this
  decision governs (`maxTaskRosterEntries`, `maxModelListEntries`,
  `maxModelEffortLevelCount`, `maxUnrecognizedRaw`).
- `docs/specs/architecture/1821-*.md` — the architect spec that first named this
  as ADR-worthy and supplied the impossibility proof (`9600 > 8192`) this
  decision is built on.
