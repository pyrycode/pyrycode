# A cardinality cut's pre-allocation must match the cap, not the input (#2101)

`decodeModelWindows`'s survivor slice was first built with
`make([]turnevent.ModelWindow, 0, len(rl.ModelUsage))` — sized to the
just-decoded, still-untrusted map, *before* the per-entry content filters
(`ContextWindow <= 0`, over-long id) remove most of it. A `make([]T, 0, N)`
backing array is `N` elements regardless of how many are later appended, and a
slice returned from the function keeps that whole backing array reachable
through its `cap`, not just its `len`. On a line carrying 100 000 unusable
`{}` entries and one usable one, the returned `ModelWindows` slice had `len ==
1` but `cap == 100001` — retaining roughly the full decoded map's worth of
memory (~2.3 MB at that size, ~7 MB at `defaultMaxParseBuf`'s full 4 MiB line)
for the life of the `TurnEnd` event, which is exactly the claim
`maxModelWindowEntries`'s own derivation makes false: "what is retained is
only the capped result."

This is a *different* leak from the one the over-cap path already guards
against (a `windows[:cap]` reslice pinning the decoder's backing array when
more than `maxModelWindowEntries` entries survive filtering — closed with
`slices.Clone` on that branch only). The clone only helps once the survivor
count exceeds the cap; this one fires whenever the *input* map is oversized
and filtering rejects most of it, however few entries survive — no over-cap
map is needed to trigger it. Both had to be fixed independently: the over-cap
branch clones, and the pre-allocation itself needs
`min(len(rl.ModelUsage), maxModelWindowEntries)` as its capacity, never the
raw decoded-map length.

**The general rule for this cap family:** a cardinality cut's pre-allocation
capacity is a security-relevant number in its own right, not an
implementation detail free to size off whatever's convenient. Sizing it from
an untrusted count instead of the cap re-opens the exact unbounded-retention
hole the cap exists to close — invisibly to every ordinary test, because
every behavioural assertion (`len`, contents, drop count) stays correct while
the backing array balloons. It surfaced only by measuring retained bytes
through the production `NewParser`+`Write` path (`unsafe.Sizeof` times
`cap()`, not `len()`), not by calling the decode function directly or reading
sizes off a table-driven unit test — the ordinary test tier for this family
cannot see it. The next per-entry cap built on this pattern (`boundEach`,
`emitModelList`, `emitBackgroundTaskRoster`) that pre-allocates against a
decoded collection's length before filtering carries the same hazard.

Slice ownership and string ownership need separate proofs. Replacing fields in
the source entry after a bound returns proves that the result owns its structs,
but a shallow struct copy still passes while its strings share the source's
backing bytes. Tests for `boundContextUsageEntries` therefore build designated
strings over mutable byte buffers, mutate those buffers after the call, and
assert that the retained values stay unchanged. Without that mutation, removing
the per-field `strings.Clone` would leave an ownership test green while allowing
a short retained string to pin a much larger untrusted parse buffer.
