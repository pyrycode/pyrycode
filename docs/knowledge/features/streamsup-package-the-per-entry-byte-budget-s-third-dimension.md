# The per-entry byte budget's third dimension is bounded too, since #1821
**The per-entry byte budget's third dimension is bounded too, since #1821.** `EffortLevels` is the
family's first field that is a list *inside* a list entry — entries × levels × level length is a third
size dimension. #1827 bounded the third factor (one level string's length, `maxModelEffortLevel`, 32); #1821 closed the second, `maxModelEffortLevelCount` (8), bounding how many levels one entry retains.
The bound sits inside `boundEach`, **after** #1828's zero-length arm and **before** the per-element
loop, and truncates **from the tail** so claude's order survives a cut, mirroring the entry-count cap's
own placement argument one level down. See "The entry count is capped too" below for the resulting
per-entry multiplicand (1024 bytes) and the re-derived `maxModelListEntries` arithmetic. The cut report
still follows #1811's per-entry convention: `boundEach` appends `"effort_levels"` to `TruncatedFields`
**at most once per entry**, whether the cause is an over-long element, an over-long list, or both —
never once per cut element. Before #1848/#1849 published the event to a client, `logControlResponse` carried the only
operational signal either mechanism had: a fifth attribute, `levels_dropped`, the daemon-computed total
of levels dropped by the count bound across the *retained* entries (entries removed by the entry-count
cap are already counted by `dropped`, so the accumulating loop runs over the already-resliced entry
list). See [ADR 036](../decisions/036-aggregate-cap-product-is-not-a-ceiling.md) for why the 8192-byte
figure this replaced was never really a ceiling.

**`SupportsAutoMode` collapses absent, JSON `null` and an explicit `false` into one reading — `false`
— and the reason is that this field describes a withheld permission grant, not a general-purpose
optional bool (#1819).** "claude refused auto for this model" and "claude said nothing about auto for
this model" drive the identical client behaviour (grey the option out), so no distinction was lost by
choosing a plain `bool` over a `*bool`; the unsafe collapse would have been the inverse, granting on
silence, which nothing here does. claude has never sent `false` at all — every capture arm shows four
`true` entries and two carrying no capability key whatsoever — and `protocol.ModelOption` already made
the same collapse (#1704), so a pointer here would have preserved a distinction only long enough for
the mapping (#1848) to discard it. **The next field facing this question had to re-derive its own
answer rather than inherit this one — and, having re-derived it, landed on a collapse too, by a
different argument**: `EffortLevels` (#1827, decided #1828) is an empty-menu question, not a
withheld-grant question. An absent key, a JSON `null` and a published `[]` are ONE reading in this
field as well, spelled `nil`, but the reason is not the bool's asymmetric-safe-direction argument —
it's that both readings leave the daemon holding the same empty hand: its one effort vocabulary
(`internal/relay`'s `validEffort`) is forbidden from feeding this list in either direction, so
"absent" and "published-empty" issue the identical instruction (*this is not a menu you may offer*)
regardless of which of the two claude meant. `boundEach` (`internal/streamsup/parser.go`) normalises
the zero-length shape at construction, to `nil` rather than `[]string{}`, because `nil` is
`TruncatedFields`'s own spelling for "nothing cut" and a struct with two list fields disagreeing on
how to spell empty is worse than the small one-time cost of picking a direction. One trap the decision
exists partly to close: `slices.Equal(nil, []string{})` reports `true`, so a design that tried to
*keep* the distinction would have carried a difference invisible to the comparison idiom every other
assertion on this field already uses.

*Test-writing lesson: a collapse design's test can't stop at asserting the decoded value.* With absent
and present-`false` deliberately indistinguishable downstream, a table test that only checks
`SupportsAutoMode`'s decoded value is satisfied by a fixture builder that quietly drops the `false` key
— proving one wire shape twice and calling it two. The fix is a guard that asserts what the built
fixture's line *actually carries* (read back with literal key strings, `capturedModelEntries`' idiom)
before the decoded value is inspected at all. Also worth knowing before reaching for live capture: the
present-`false` shape exists nowhere in the tree (not the committed capture, not `fakeclaude`'s canned
list) and has to be hand-built — a design that keeps two shapes apart can never be shown to do so by a
fixture transcribed from real bytes when one of the two shapes has never been observed.

**Two more testing lessons, from #1827's list-valued extension of this same table shape.** First, a
claim of *sole* redness in a test's doc comment is itself a measurable claim, not a description, and is
cheapest to check at the moment it's written: two such claims here were plausible and wrong until
mutation testing corrected them — an unconditional-append mutant reddens the all-fit row only *within
that table*, not uniquely across the package (the capture pin and two decode-table rows also redden),
and a scrambled-order row is not sole-red against a mutant that merely *sorts* (sorting also reddens the
already-canonical five-level row); it's sole-red only against a mutant that canonicalises into claude's
own published order, a narrower claim than "sorted." Second, a `check` closure that indexes into a
decoded slice (`models[0].EffortLevels[0]`) turns a mutant's clean FAIL into a process-wide panic once a
mutant (a json-tag typo) leaves that slice empty — it killed the parallel subtests before they could
report, so one mutation run showed four reds where eleven were expected and briefly read as a coverage
gap rather than a harness bug. A length `t.Fatalf` before the index costs two lines and keeps a
mutation run's output honest.
