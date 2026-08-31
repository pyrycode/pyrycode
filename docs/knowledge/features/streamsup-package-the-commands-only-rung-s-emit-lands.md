# The commands-only rung's emit lands (#1891)
**The commands-only rung's emit lands (#1891).** Rung 4 now calls the same
`emitSlashCommandList` rung 5 does, with the identical expression
(`cr.Response.Response.Commands`), below its own `logControlResponse` and inside the
empty-models branch — no second decode, loop or cap, and no keyword added or moved.
`THE DISCRIMINANT` is no longer a conjunction (`subtype == success AND models
non-empty`): `controlResponseSuccess` is now the shared precondition of both emits, and
each array is decided independently below it. One rung (5) can emit a `ModelList`; two
rungs (4 and 5) can emit a `SlashCommandList`.

*Lesson: an enumerated sweep vocabulary is a floor, not a ceiling — a class-word claim
can rot through a synonym the list never named.* #1890 taught this family to sweep for
count words (`non-emitting`, `emits nothing`, `zero events`...); #1891's own sweep, run
against that list, still missed a definite singular — `"the emitting rung's naive
reading"` on rung 3 — because a singular article names no count at all and so matches
none of those words. Code review then found a third shape neither list covers:
`controlResponseSuccess`'s doc called itself "half of emitModelList's conjunctive gate,"
true only while there was one array to conjoin with, and falsified by the very
re-authoring the ticket required two screens away. "Conjunctive" is a structural word
describing the *shape* of the gate, not a count or an article, and no finite grep
vocabulary will anticipate every synonym a doc uses for "there used to be one path and
now there are two." The durable defense isn't a longer word list — it's reading every
doc block that touches the changed function whole, the way #1877's lesson above
(`eventKind`'s "anything else the production producer emits") already established for
tree-wide invariants.

*Lesson: when a ticket's own doc premise, not just a row's `want` field, is what gets
overturned, the test needs surgery, not an added row.* The liveness proof lived in a
table whose single `len(events) != 0` assertion covered every row — adding a
`wantEvents` row wasn't available piecemeal, because the shared assertion had to be
replaced everywhere at once. The tell was in the test's own doc, not its rows: its
stated premise ("a commands-carrying payload with no models emits nothing") was the
exact claim the ticket falsified, which meant the table's shared assertion was built on
that premise and had to go with it.

**The commands-only rung's cap gets its own row (#1885).** #1891 gave rung 4 a call
into the same `emitSlashCommandList` rung 5 uses, but every row in
`TestParser_SlashCommandNamesAreCapped`'s cap matrix still rode rung 5 — so rung 4's
per-name cut was inherited by construction, not pinned, and relocating the bound out of
`emitSlashCommandList` onto rung 5's call site left the whole package green while
workspace-authored command names rode uncut into a retained `turnevent.SlashCommandList`.
The matrix gained one `commandsOnly` bool from which the `models` key, the expected
event count and the `SlashCommandList` index all derive, and one row that is its
neighbour's twin in every variable but the rung.

*Lesson: a `//`-claim sweep has to sort hits by what a sentence asserts, not by whether
it contains a flagged phrase.* Two comments in this file describe the same "one emitter,
one cap" structure this ticket measures for the first time — `emitModelList`'s rung-4
doc and `TestParser_SlashCommandListIsSuppressed`'s "cannot reach the emitter with an
empty array by construction" — and both survive untouched. They read like the claims
the ticket falsified and are not: they assert *behaviour* (what the code does), where
the falsified claims asserted *proof* (that construction alone was sufficient evidence).
Sorting the sweep's hits into those two buckets is what separates a live claim from a
stale one; a phrase grep alone cannot.

*Lesson: when a cut and its report live in the same loop, a relocation mutant and a
pre-cut mutant test different things, and only one matches the risk.* The candidate
mutant was to pre-truncate the entries rung 5 passes into `emitSlashCommandList` and
leave the loop a plain copy — but that also destroys rung 5's report, since the emitter
now sees an already-short name and computes `truncated == false`, reddening the existing
matrix on the *report* rather than isolating the rung. The mutant that actually matches
what a future edit to that loop could do is a relocation: give `emitSlashCommandList` a
`nameLimit` parameter, pass `maxSlashCommandName` from rung 5 and an unbounded limit from
rung 4. Under that one, the new row is the sole failing subtest in the package.

**`Description` joins the per-entry shape (#1904).** `commandEntryLine` gains a second field,
`Description string`, decoded verbatim per #1600's rule — no lowercasing, trimming, charset
filtering, or newline stripping; one captured description carries two raw `0x0a` bytes and they
are not stripped. `turnevent.SlashCommand` gains `Description` in its **mirrored position**,
between `Name` and `TruncatedFields`, matching `protocol.SlashCommand`'s declaration order and
leaving the gap `ArgumentHint` will fill (#1830) — the first evidence for the type's
fixed-order-before-the-second-field-exists promise. A new `maxSlashCommandDescription` (256 bytes)
cuts it at construction; the per-entry term is now the **sum of caps**,
`maxSlashCommandName + maxSlashCommandDescription` = 512 bytes — a worst case, not a description of
one capture, the same derivation shape `maxModelListEntries` uses for its own product. The
ceiling argument follows that doc's corrected framing: state the fraction of `maxUnrecognizedRaw`'s
16 KiB (1/2 or 5/8), never the retired 8192 landmark. This slice does not pick the fraction or the
entry-count factor — both are #1826's — it hands forward only the 512-byte term and the two
arithmetics it implies (16 entries at 1/2, 20 at 5/8). `emitSlashCommandList`'s construction loop
adopts `emitModelList`'s `bound` closure, declared inside the per-entry loop, so the two sequential
`truncateField` calls (`Name` then `Description`) report cuts in call order — the property that
makes `TruncatedFields`'s `["name", "description"]` ordering a consequence of the call sequence
rather than a sort.

*Lesson: a committed-capture pin can assert a narrower claim than its assertion's scope covers.*
`TestParser_InitializeControlResponseCountsTheCapturedCommands` asserted `TruncatedFields == nil`
across the whole 51-entry capture under the message "no captured name approaches the cap" — a claim
about `Name` specifically, checked over the entry's *entire* `TruncatedFields` slice. Declaring
`Description` (whose capture values legitimately exceed 256 on 10 of 51 entries) turned that
assertion red against a correct producer, because the whole-slice check silently absorbed a second
field's cap it was never written to reason about. Narrowed to
`slices.Contains(got.TruncatedFields, "name")`. Worth checking before any future field lands on a
type a capture test asserts whole-struct absence on: a message naming one field and an assertion
covering the whole struct are two different claims, and only the narrower one survives the next
field landing on that struct.

*Lesson: a ten-item `//`-correction list costs a paragraph each, not a sentence each.* The spec
estimated ~265–350 lines against its own ≤400 boundary; the change landed at ~500, almost entirely
in doc prose — the emitter's own code change was ~15 lines. Two of the ten corrections were
re-arguments (rewriting a memory-share-of-payload paragraph, redrawing an unreachable-vs-unwritten
distinction) rather than sentence edits. Worth costing in before sizing #1830 and #1825, which
correct a comparable number of paragraphs in these same functions.

**Fresh-restart under a new id (#1124).** `RestartFresh(newID string)` rotates the runner into a fresh
session: the *next* spawn uses `--session-id <newID>` (a new transcript, no fork) instead of `--resume`,
and a later crash-respawn then `--resume`s `newID` — never the pre-rotation id. It reuses the live-restart
seam above, but rotates the **session id**, not the argv: a new `restartMu`-guarded pair,
`sessionID` (the mutable analogue of the construction-time, immutable `cfg.SessionID`; seeded from it in
`New`) and `rotatePending` (a one-shot flag), sit alongside `args`/`iterCancel` in the same field group.
`Run`'s spawn loop reads them via `beginSpawn()` — the `restartMu`-guarded accessor that snapshots
`(sessionID, rotatePending, args)`, clears `rotatePending`, and publishes `iterCancel`, all in ONE
acquisition, then (since #1630) decides and assembles the argv *below* the unlock via
`useCreateForm`+`buildArgs` — instead of `r.cfg.SessionID` directly; when `rotatePending` is true the
returned `forceFirst` re-arms the Run-goroutine-private `firstRun` local to `true`, and that local feeds
`useCreateForm`'s `latchCreate` argument as `firstRun || forceFirst`. With `Config.ClaudeSessionsDir`
unset (every production path today) the latch still decides outright, exactly as before #1630; set, the
probe overrides it — a rotated id with no transcript still creates, but a rotated id that happened to
collide with an existing transcript would resume, contrary to `forceFirst`'s own intent. That case is
unreachable from `sessions.NewID`'s minting and never observed, so it is deliberately left as a
consequence of `useCreateForm`'s one rule rather than special-cased (see `useCreateForm` above). The one
acquisition is the #1481 fix: splitting the id read from the `iterCancel` publish — as this loop did from #1124 until #1481 — leaves a gap where a racing `RestartFresh` sets `sessionID`/`rotatePending`, reads a `nil`
cancel, cancels nothing, and the spawn launches under the pre-rotation id anyway (see the supervise
loop above). The existing `started`-gated `firstRun`
flip (see the gate above) then does the rest for free: a successful fresh spawn flips `firstRun` back to
`false` so the next respawn `--resume`s the rotated id (with `ClaudeSessionsDir` unset — set, the probe
decides regardless of the flip); a setup failure on the fresh spawn leaves
`firstRun` `true` so the retry keeps trying `--session-id <newID>` rather than `--resume`-ing a session
that was never established. `RestartFresh` mirrors `Restart`'s hint-before-cancel ordering and
newest-wins coalescing exactly, but **leaves `r.args` untouched** — `new_session` rotates identity, not
flags, so args stay `Restart`/`UpdateSettings`'s concern. An empty `newID` is a `Warn`-logged no-op (the
runner never spawns `--session-id ""`); this is a deterministic last-resort guard, not the primary
validation — that's the pool/routing layer's job (#1125), per the "caller-supplied id validation at the
primitive boundary" convention. Concrete method, off `sessions.Runner` (#1077), same discipline as
`Interrupt` above — the routing sibling (#1125) reaches it via a narrow interface or type assertion. See
[codebase/1124.md](../codebase/1124.md).

Still deferred (needs richer context than this slice): `max_tokens`/`refusal` `TurnEnd` reason
classification (`resultTurnEndReason`'s `default` branch is the safe placeholder until one is observed);
routing an inbound remote interrupt frame to the correct per-conversation runner (#1121, blocked-by #1120); routing an inbound `new_session` frame to the correct per-conversation runner and the pool-side
`Pool.RotateID` (#1125, blocked-by #1124); whether the parser's line buffer needs resetting across a
crash-restart (see [codebase/1088.md](../codebase/1088.md) — code review flagged a stale-partial edge
case, non-blocking for this unwired slice).
