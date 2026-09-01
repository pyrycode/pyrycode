# Firing the ask at spawn time (#1839)
**Firing the ask at spawn time (#1839).** A new `Config.RequestInitializeOnSpawn bool` gates one
call to `RequestInitialize()` inside `spawnAndWait`, between `updateState` and the `onSpawn` seam.
Cardinality is per **spawn**, which is per child by construction — there is no counter and no
per-child bookkeeping, because `spawnAndWait` itself runs exactly once per child. The trap this
design exists to avoid is triggering off the child's own `system`/`init` line instead:
`emitModelAnnounced`'s doc measures that line as firing once per **turn**, so a trigger there would
ask on every turn a child serves and only a multi-turn test would catch it. `mapStreamsupConfig`
sets the field `true`, making the ask the interactive daemon's policy rather than every runner's
behaviour; the zero value leaves every other `streamsup.Config` construction site — the three
`internal/e2e/realclaude` runners included — unchanged with no edit, the same shipped-unwired
property #1206's `OnChildExit` established. The ask's error (only a write-time EPIPE is reachable
here in practice; see the test-writing lesson below) is absorbed into one `Debug` record and never
reaches `waitErr`, so it cannot restart a child or fail a spawn. Because `RequestInitialize` reads
`Stdin()` rather than the rotation-gated `turnTarget`, a `RestartFresh` can leave both the outgoing
child and its successor asked — deliberate, not a gap: gating the ask on the rotation window would
leave a child permanently unasked on the `BeginRotation` abort path, which costs more than one
harmless extra line on a child already being killed.

*Test-writing lesson, from the two multi-step assertions this ticket needed.* The `echo_lines` fake
child gives tests a FIFO barrier, but only for lines the child lives long enough to read: waiting on
`onSpawn`'s signal proves the daemon **wrote** the initialize line, not that the child **read** it,
so a restart test that kills child 1 as soon as child 2's `onSpawn` fires can beat the read and lose
child 1's ask from the transcript — the count reads 1 where 2 was expected, intermittently. The fix
is to wait for the first ask's own echo before restarting. **#1968 hit the same trap over a different
marker** — a test counting `echo_lines`' own startup `READY` write, not an ask — and traced *why* the
race was winnable at all: the package's [teardown reap forks `ps` before SIGTERM](streamsup-package.md#teardown-sigterm--sigkill-grace--descendant-group-reap),
an accidental ~17–22ms grace window nothing asked for, which is what usually (not always) lets a
doomed child finish starting up before its kill lands. `onSpawn` never orders that write either way;
any assertion counting a child's own output needs a barrier on that output specifically. Separately, a single-turn test cannot
distinguish "once per spawn" from "once per turn" — the two cardinalities agree until a second turn
is delivered, so the second turn is the assertion, not padding, for the exact `system`/`init`
mistake this design avoids.

*Test-writing lesson, from code review: an "undeliverable ask is absorbed" test needs the reachable
arm named correctly, or it proves nothing.* A test built around a `crash`-mode child (exits ~20ms
after spawn) intends to exercise the absorbed-error path, but `go test -race -coverprofile` showed
the absorb body at 0 executions against 7 successful asks: the write always lands in a pipe buffer
created microseconds earlier, so only a microsecond-wide EPIPE race is reachable at this call site —
`ErrNoLiveChild` is not, because that arm requires `takeStdin` to have already run, and `takeStdin`'s
one call site sits below `cmd.Wait`, later in the very spawn `RequestInitialize` fires within. There
is no race-free way to force the reachable arm from a child's behaviour, so the honest claim for such
a test is "the flag doesn't perturb the crash/backoff ladder," not "the absorb branch is covered" —
label it as what it measures rather than the AC it was written to satisfy.

*Mutation-testing note, applicable to any future control-request marshaller added this way:* a
byte-exact marshal test pinned against a **fixed literal id** cannot distinguish a structured
`json.Marshal` from a `fmt.Sprintf`-concatenated line, because a plain digit id produces identical
bytes either way. `TestMarshalBypassRevocationEnvelope`'s existing eight-row `request_id`
injection table does not cover this for a new marshaller — a concatenation mutant is per-function,
not inherited across siblings — so `TestMarshalInitializeEnvelope` needed its own single
hostile-id case (an id carrying a raw newline) to catch it; confirmed as the sole detector under
`go test -overlay` review. The revocation table's other seven rows exist to guard `mode`, a second
fixed field `initialize`'s subtype-only inner doesn't have — a new subtype with no such second
field needs the one hostile-id case, not the full table.

**Reading the `initialize` ack's committed captures, in-package (#1810).** `capturedInitialize`/
`capturedInitializePayload` (`initialize_capture_test.go`) read the four committed
`internal/e2e/realclaude/testdata/initialize_control_*.json` captures of what claude actually sent
back for the request above, selecting a capture by an **arm** (a closed set of identifiers), never a
path — the reader mints the path from package constants and rejects any other string. This is a
second, narrower struct over the same record `internal/e2e/realclaude`'s `initControlFixtureRecord`
already decodes (that type sits behind the `e2e_realclaude` build tag and cannot be imported), so it
inherits none of that package's live-run scaffolding — the whole point is that this proof now runs
inside `make check` instead of behind the opt-in gate that exits 0 with zero tests run when there is
no claude login. `capturedInitializePayload` fatals on the one arm that recorded no response
(`control_no_request`); the wide reader hands that case back as a nil payload instead, so an
absent-payload capture is a distinguishable *fact*, not a read failure — #1811/#1812/#1719/#1809 are
the decodes meant to call the wrapper.

Two things worth keeping in mind for any reader built the same way: first, a reader must not itself
check the invariant its own test exists to pin — `capturedInitialize` deliberately never compares the
decoded payload's model-entry count against the record's own `models_count` summary, precisely because
`TestCapturedInitialize_ModelCountsMatchEachRecordsSummary` checks exactly that; had the reader also
enforced it, that assertion would be satisfied by construction and a mutant substituting the outer
wrapper for the inner payload would stay green. Second, a helper added only because future tickets will
need it (`capturedInitializePayload` had no callers at the time — #1811/#1812/#1719/#1809 hadn't landed
yet) should get a real call site in its own test rather than ship unexercised on the promise of a later
caller: nothing in the build flagged the gap, and giving it one also pinned that the wide and narrow
readers agree byte for byte.

**Decoding the initialize ack into `turnevent.ModelList` (#1811).** `emitModelList` reaches the
capture's `models` array through a **shape discriminant**, not through correlating
`Runner.nextControlID`'s minted `request_id`: the writer discards its id inline
(`WriteInitialize(r.Stdin(), r.nextControlID())`, exactly as `Interrupt`/`RevokeBypass` discard theirs
above), and the parser holds no link to it, so correlation would cost new cross-object state between
the writer and the parser that today share none. The gate is conjunctive —
`response.subtype == controlResponseSuccess` AND a non-empty decoded `models` array — and its own doc
names the limit this buys: it recognises a **shape**, not a **correlated reply**. A future claude
putting a `models` array inside some other successful control response would have that response read
as an inventory too. The consequence is bounded rather than a defect to fix here — the value is still
claude's own claim about itself, bounded by the same three caps, retained for the session's life since #1840 (below), and published on the live interactive turn lane since #1849 (below). **The trade was
revisited at that point (#1862) and re-taken unchanged**: correlating `Runner.nextControlID`'s minted
`request_id` would prove **which reply** the bytes answered, not make the **content** any more
trustworthy, since the same subprocess authors every control response including the one this arm
reads — a correlated inventory is claude's own claim about itself exactly as an uncorrelated one is.

Each `turnevent.ModelOption` entry keeps five of claude's payload keys —
`ResolvedModel`/`Value`/`DisplayName`, `SupportsAutoMode` (#1819) and, since #1827, `EffortLevels` —
mirroring `protocol.ModelOption`'s wire row (#1704) field-for-field, with nothing missing and nothing
invented, taken verbatim per #1600's rule (no lowercasing, alias expansion, date-stamping, family
mapping, or reordering). **`description`, `supportsEffort`, `supportsAdaptiveThinking` and
`supportsFastMode` are decoded nowhere**, on purpose: a field decoded here that nothing publishes is
untrusted prose bounded, retained and carried for nothing, and absence from the decode target is a
stronger guarantee than any test sweep — `systemInitLine`'s argument (#1600) carried over unchanged.
Each string is bounded by its own named cap (`maxModelResolved`/`maxModelValue`/`maxModelDisplayName`,
all 256; `maxModelEffortLevel`, 32, capping one level string). The list itself is bounded too, since #1821, by `maxModelEffortLevelCount` (8) — see "The per-entry byte budget" below; the bool needs no cap and
is never named in `TruncatedFields`, since it carries none of claude's bytes. An empty or absent
`models` array is the safe-failure direction (rung 3, no event) rather than an empty `ModelList` —
`emitRateLimit`'s rung 3 is the precedent: a list naming no model can't serve the purpose the variant
exists for.
