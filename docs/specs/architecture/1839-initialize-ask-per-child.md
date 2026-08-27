# #1839 — Ask each live claude child for its initialize reply, once per child

## Files to read first

Production surface:

- `internal/streamsup/runner.go` → `Config` — the struct the new field joins; read `OnChildExit`'s doc for the
  house contract a spawn-time hook inherits (must not block, must not panic, no `recover`), and `onSpawn`'s
  doc for the unexported test seam this design fires beside.
- `internal/streamsup/runner.go` → `spawnAndWait` — **the one call site**. Read the ordering
  `cmd.Start` → `setStdin` → `updateState` → `onSpawn` → `cmd.Wait`, and the `takeStdin` + best-effort close
  below `cmd.Wait`. The `(started bool, waitErr error)` return is what the ask's error must never become.
- `internal/streamsup/runner.go` → `RequestInitialize` — the method to call; its doc argues both placements
  this spec chooses between, and states that it reads `Stdin()` rather than the rotation-gated `turnTarget`.
- `internal/streamsup/runner.go` → `setStdin` — why nothing is logged inside it, and its explicit note that a
  spawn-path diagnostic belongs **above** the acquisition, in `spawnAndWait`.
- `internal/streamsup/envelope.go` → `WriteInitialize` — the error contract already pinned:
  `ErrNoLiveChild` on a nil writer (nothing written), a wrapped error on marshal/write failure, never
  mis-reported as `ErrNoLiveChild`. This spec adds **no** new error semantics; it only decides who absorbs them.
- `cmd/pyry/streamsup_runner.go` → `mapStreamsupConfig` — the pure mapper that sets every plain-value
  `streamsup.Config` field, and `newStreamRunnerFactory` above it, which installs the runtime objects
  (`Stdout`, `OnChildExit`). Read the mapper's doc paragraph on what does and does not cross that line.
- `internal/streamsup/parser.go` → `emitModelList` — the reply half. Read its four rungs and
  `logControlResponse` beneath it (the five fixed attributes, and why no string of claude's is admitted).
  This spec starts the first production traffic through it but changes not a byte of it.
- `internal/streamsup/parser.go` → `maxModelListEntries` — the entry cap and its written-out derivation;
  needed for the security section's amplification argument, not for the implementation.
- `cmd/pyry/interactive_turn_v2.go` → `Handle`, `eventKind` — `turnevent.ModelList` has no case arm, so it
  lands on `Handle`'s `default:` and produces one Debug record and no frame. This is the structural proof of
  AC 4; read it rather than re-deriving it.
- `internal/turnbridge/outbound.go` → `MapEvent` — the `default:` arm returning `("", nil, false)`.

Test surface:

- `internal/streamsup/interface_test.go` → `TestRunner_RequestInitialize_LiveChildDelivers` — **the pattern to
  copy verbatim**: `helperRunCfg(t, "echo_lines", …)`, an `onSpawn` signal channel, `runInBackground`, and the
  `ECHO:<line>` round-trip that proves the exact envelope reached the child. The new tests are this test plus
  a count.
- `internal/streamsup/runner_test.go` → `safeBuffer`, `helperRunCfg`, `runInBackground`, `waitForContains` —
  where those helpers actually live (same package, different file). `helperRunCfg` takes `extraEnv ...string`.
- `internal/streamsup/helper_test.go` → `helperChild` — read its mode list. `echo_lines` (echo every stdin
  line as `ECHO:<line>`, never self-exit) and `crash` (exit 1 after a short delay, forcing a respawn) are the
  two modes this spec's tests need. The mode is selected by env, not argv.
- `internal/streamsup/runner.go` → `Restart`, `RestartFresh` — both kill the live child and let the loop
  relaunch. `Restart` swaps the base argv **verbatim**, so a test must re-install the same `Config.Args`
  rather than pass nil, which would clear them.
- `cmd/pyry/streamsup_runner_test.go` → `TestMapStreamsupConfig_Bootstrap`, `TestMapStreamsupConfig_PerSession`
  — the existing field-by-field mapper assertions the one-line wiring extends.
- `docs/knowledge/features/streamsup-package.md` § "Per-child-exit seam (#1206)" — the nearest shipped
  analogue in this exact file. Read the cardinality-is-per-iteration paragraph and the "ships unwired, with
  zero `cmd/pyry` diff" note: **every `streamsup.Config` literal tree-wide is named-field**, which is what
  makes a new field default-off everywhere without editing a single other construction site.
- `docs/knowledge/features/fakeclaude-binary.md` § "Initialize control request answer (#1692)" — the fake
  answers an `initialize` request **unconditionally, in both stream modes, under no env flag**. Nothing needs
  adding to the fake for this ticket.

## Context

`(*streamsup.Runner).RequestInitialize` writes the request; `(*streamsup.Parser).emitModelList` decodes the
reply. Neither has a production consumer, so the round trip exists as two disconnected halves. This slice
joins them and stops there: it does not retain the model list (#1840) and does not decode `commands` (#1720).
Both of those ride this one round trip rather than adding a second, which is why the join is its own ticket.

The whole design question is **where the trigger fires from**, because that choice — not the ask itself —
drives the file count, the test tier, and whether the interface has to widen. Everything else follows.

No ADR is warranted. The placement rule this spec applies (interface method only when the consumer sits
inside `internal/sessions`) is already written down in `RequestInitialize`'s and `RevokeBypass`'s own docs,
and this ticket confirms rather than extends it.

## Design

### The placement decision

Three candidates, not two. The ticket names (a) and (b); (c) is their synthesis and is what this spec
prescribes.

| | Where the ask fires | Cost |
|---|---|---|
| (a) | New exported `OnChildSpawn func()` on `Config`, installed in `newStreamRunnerFactory` | The callback needs the runner that the same `streamsup.New` call returns, so the closure must capture a pre-declared variable assigned *after* `New` — a publication ordering that is correct only because `Run` starts on a later goroutine, and is a footgun to document. Adds a general seam with exactly one consumer that does exactly one thing. |
| (b) | Unconditionally inside `spawnAndWait` | Makes the ask **every** runner's behaviour. `streamsup.New` has one production caller and **three `internal/e2e/realclaude` callers**, which `make check` cannot compile (the `e2e_realclaude` build tag). Perturbing `dropped_line_capture_test.go`'s line census and the two `interactive_stream_inband_*` suites is a change no hermetic gate can detect. |
| **(c)** | **Inside `spawnAndWait`, gated on a new `Config` field** | **A new exported field. Nothing else.** |

**(c) is the design.** It takes (b)'s placement — no new seam, no back-reference, the ask fires exactly where
the child is born — and pays (a)'s price for it, which is one `Config` field rather than one `Config`
callback. The field makes the ask the interactive daemon's *policy*, which is precisely what (b) gives up,
and it makes the three realclaude runners byte-identical by construction: every `streamsup.Config` literal
tree-wide is named-field, so the zero value reaches them with no edit. That is the same property #1206
relied on when `OnChildExit` shipped unwired.

The general callback of (a) is the abstraction; the ask is the thing. Nothing has asked for a per-spawn hook,
so per *Simplicity first*, the field ships and the hook does not. If a second per-spawn consumer ever
appears, (a) is a mechanical follow-up from here.

### Contract — `internal/streamsup`

One new field on `Config`:

```go
// RequestInitializeOnSpawn asks each spawned child, exactly once, to report what
// the session knows about itself. Optional; false (the zero value) keeps a runner
// byte-identical to pre-#1839 behaviour. Set only on the interactive daemon's
// production path.
RequestInitializeOnSpawn bool
```

The field's doc must state, because none of it is inferable from the name:

- **Cardinality is per SPAWN**, and that is the whole of how "exactly once per child" is enforced — there is
  no counter, no "have I asked this child" bookkeeping, and none is wanted. `spawnAndWait` runs once per
  child, so firing there is once per child by construction. It therefore does **not** grow with turns; the
  contrast with the child's `system`/`init` line (which `emitModelAnnounced`'s doc measures as firing once
  per *turn*) is the trap this field exists to avoid and belongs in the doc.
- Unlike `OnChildExit` it fires only when a child **actually launched** — it sits below `cmd.Start`, so a
  pre-launch setup failure never reaches it. This is the opposite cardinality to `OnChildExit`'s
  per-supervision-iteration one, and the two fields sitting near each other makes saying so necessary.
- The ask is **best-effort and its error is absorbed**: it never becomes `spawnAndWait`'s `waitErr`, so it
  cannot enter the backoff ladder, restart a child, or fail a spawn.
- It reads `Stdin()`, **not** the rotation-gated `turnTarget`, so an ask is not refused while a `new_session`
  rotation is armed. See "Rotation" below for why that is the wanted behaviour rather than a tolerated one.

One new call site, in `spawnAndWait`, between the `updateState` call and the `onSpawn` fire:

- Guarded on `r.cfg.RequestInitializeOnSpawn`.
- Calls `r.RequestInitialize()` — the existing method, not a fresh `WriteInitialize` on the local `stdin`
  handle. That reuses the shared `nextControlID` counter (request ids must be unique across subtypes on one
  stream) and keeps one code path to the writer.
- On a non-nil error: one `Debug` record and continue. Nothing else.

**Placed above `onSpawn`, deliberately.** `onSpawn` is nil in production, so the ordering is a test-only
concern — but it is the one that makes the tests deterministic: when `onSpawn` fires, the initialize line is
already in the pipe. `onSpawn`'s doc comment gains a clause saying so.

### Contract — `cmd/pyry`

`mapStreamsupConfig` gains `RequestInitializeOnSpawn: true`. Not `newStreamRunnerFactory`: the mapper is
where every plain-value field is set, and its doc's own dividing line is runtime objects (`Stdout`,
`OnChildExit`) versus plain values derived from the config. A bool constant is the latter, and putting it
there makes it directly assertable by the existing pure-mapper tests with no new scaffolding.

This is the **only** consumer edit. `streamRunner` does not grow a method, `sessions.Runner` does not widen,
and no fake runner under `internal/sessions` enters the diff — the cascade `RequestInitialize`'s own doc names
as the cost of interface placement is avoided by not taking that placement.

### Data flow

```
Run → spawnAndWait
        cmd.Start
        setStdin(stdin, freshSeq)        publishes the handle
        updateState(PhaseRunning, pid)
        ── if RequestInitializeOnSpawn ──▶ RequestInitialize
                                             nextControlID → WriteInitialize(Stdin(), id)
                                             one line onto the child's held-open stdin
        onSpawn (nil in production)
        cmd.Wait  ← blocks here for the child's whole life

child stdout → Parser.Write → control_response arm → emitModelList
        → one bounded turnevent.ModelList onto the ordinary event stream
        → streamTurnSink → interactiveTurnEmitterV2.Handle → default: → one Debug record
        → no MapEvent call, no frame, no eventring entry
```

The reply half is entirely existing code and this ticket adds nothing to it.

### Rotation — a rotation's successor counts, and so does the outgoing child

`RequestInitialize` reads `Stdin()`, so the ask is not gated by `BeginRotation`'s armed window. Two children
can therefore be asked around one `RestartFresh`: the outgoing one (whose spawn predates the arm, so
`setStdin` leaves the gate standing) and the successor. Both asks are correct under AC 2, which requires that
a replacement child be asked in its own right, and the extra ask on the doomed child is harmless in exactly
the way the ticket describes — it commits nothing, no queue head rides on it, and it is one bounded line.

Do **not** add a gate check here. Refusing the ask while rotating would trade a free, harmless line for a
real failure mode: a child that is *not* killed after all (the `BeginRotation` abort path) would be left
permanently unasked, which is the state AC 2 exists to prevent.

## Concurrency model

No new goroutines. No new locks. No change to shutdown.

- The ask runs on the **Run goroutine**, inside `spawnAndWait`, holding no Runner lock (`RequestInitialize`
  takes and releases `r.mu` inside `Stdin()` before writing). It inherits `OnChildExit`'s stated constraint —
  the supervision loop is stalled until it returns — and satisfies it: one `json.Marshal` of two fixed
  literals plus one ~90-byte `Write` onto a pipe whose 64 KiB buffer was created microseconds earlier by
  `cmd.StdinPipe` and is necessarily empty. It cannot block on a full buffer, and it cannot panic
  (`WriteInitialize` nil-checks its writer first).
- **No new happens-before requirement.** `spawnAndWait` is called from `Run`'s single goroutine and is the
  only writer of the stdin handle, via `setStdin` above and `takeStdin` below; no second spawn can interleave.
- **Concurrent writers to the same pipe are pre-existing, not introduced.** `WriteUserTurn`, `Interrupt` and
  `RevokeBypass` already write onto the handle from other goroutines the instant `setStdin` publishes it. The
  ask joins that set at one specific moment and adds no new class of interleaving; its own line is far below
  `PIPE_BUF`, so it is atomic against them. Nothing in this slice depends on ordering against a turn.

## Error handling

Exactly one new failure branch. The three failure modes below all arrive as a non-nil error from one call and
are answered identically — deliberately, because none of them is actionable in a slice that consumes no reply.

| Mode | Reaches us as | Answer |
|---|---|---|
| Child died between `cmd.Start` and the ask (teardown race, ctx cancel) | `ErrNoLiveChild` — `takeStdin` already ran | Log, continue |
| Pipe write failed (EPIPE on a child that exited instantly) | wrapped `write initialize` error | Log, continue |
| Marshal failure | wrapped `marshal initialize` error (unreachable — fixed literals) | Log, continue |

Requirements on the branch:

- The error **must not** be assigned to `waitErr` or affect `started`. AC 3's "no child is restarted because
  of it, no session reports an error" is a statement about this exact line.
- **One `Debug` record, not `Warn`.** Two reasons. The failure is not operationally actionable while nothing
  consumes the reply, and the common cause is a benign shutdown race that would produce a `Warn` on every
  daemon stop. `Debug` also matches `logControlResponse`, which logs the *reply* half at `Debug`, keeping both
  ends of one round trip at one level. Follow `logControlResponse`'s attribute discipline: a fixed, small
  attribute set, the `err` admitted (it is daemon-authored — `WriteInitialize` wraps only its own literals and
  an `*os.File` write error, never claude's bytes), and **no** request id, no payload, no model value.
- No `recover`, matching the rest of the spawn path.

## Testing strategy

Hermetic and entirely inside `make check`. The proof belongs at the **streamsup level**, which follows from
the placement: the behaviour is streamsup's, gated by a flag only production sets. A fake-daemon e2e would
re-prove through four more layers what the `echo_lines` round trip proves directly, and the placement is what
makes that unnecessary. `PYRY_FAKE_CLAUDE_STDIN_LOG` is therefore **not** needed and no e2e is prescribed.

Scenarios — bullet points, not bodies. All four reuse `helperRunCfg` / `runInBackground` / `safeBuffer` and
copy the shape of `TestRunner_RequestInitialize_LiveChildDelivers`. Add them to
`internal/streamsup/interface_test.go`, beside the existing `RequestInitialize` band.

A single unexported helper counts `ECHO:` lines in the captured stdout whose payload decodes as a
`control_request` with `request.subtype == "initialize"`, returning the count and the request ids seen. Every
scenario below is that count plus a condition.

**1. Once per child, and not per turn — table-driven over the flag** (AC 1, and the guard for every existing
runner):

- Flag `true`, `echo_lines` child, wait for `READY`, then deliver two user turns (each echoes back, giving a
  FIFO barrier proving the ask line preceded them) → count is exactly **1**, and the id is non-empty.
- Flag `false` (zero value) → count is exactly **0**. This row is the one that pins the three
  `internal/e2e/realclaude` runners and every existing streamsup test as unchanged; it is not redundant with
  the row above.
- The two-turn arm is load-bearing: with a per-turn trigger (the `system`/`init` mistake the ticket warns
  about) a one-turn test stays green and this one reddens.

**2. A replacement child is asked in its own right — table-driven over the restart shape** (AC 2):

- Rows: `Restart` (re-installing the same `Config.Args` — it swaps the argv verbatim, so nil would clear
  them) and `RestartFresh(<new id>)`. Both kill a live `echo_lines` child, which never self-exits, so the
  kill is attributable.
- Wait for the second spawn via `onSpawn` (count the fires), then for the second `READY`.
- Assert count is exactly **2** and the two request ids **differ** — the distinct-id assertion is the sole
  detector for a design that mints or caches one id per runner rather than per ask.
- The `RestartFresh` row is what pins the rotation decision above; without it that decision is prose only.

**3. An undeliverable ask is absorbed** (AC 3):

- Flag `true`, `crash` mode — the child exits ~immediately, so the ask races a dying child and may take
  either the `ErrNoLiveChild` or the EPIPE arm; the test must not depend on which.
- Assert the supervision loop is unharmed: `onSpawn` fires at least twice (the ladder respawned), `Run` has
  not returned, and cancelling the context still joins cleanly.
- Do **not** try to force one specific error arm. `WriteInitialize`'s own error contract is already pinned by
  `TestWriteInitialize_NilRefusal` and `TestWriteInitialize_WriteError` in `envelope_test.go`; what is new
  here, and all that is new here, is that the *call site* swallows it.

**4. The wiring** (`cmd/pyry/streamsup_runner_test.go`):

- Extend the existing `TestMapStreamsupConfig_Bootstrap` and `TestMapStreamsupConfig_PerSession` assertions
  with `RequestInitializeOnSpawn == true`. One assertion each, no new test function needed.

AC 4 needs no test. It is satisfied structurally by `Handle`'s `default:` arm, which reaches neither
`MapEvent` nor `emit` — there is no frame to assert the absence of, and a test asserting "no frame appeared"
would pass equally against a design that never asked at all.

## Open questions

- **Correlation is not needed here, and the reason is worth recording.** At most one `initialize` is in flight
  per stream at any time: the ask is written once per spawn, on the Run goroutine, and the child is torn down
  before the next one. So `RequestInitialize` keeps its write-only id and this slice adds no ack correlation.
  #1840, which retains the reply, is where "which request is this the answer to" first has a possible second
  answer.
- **Observation for the reviewer, not a defect to fix.** Once the daemon starts asking, `logControlResponse`
  emits one `Debug` record per child in every fake-daemon e2e. `relay_v2_stream_new_session_test.go`'s
  `#1318` instrument guard asserts only that *some* `level=DEBUG` record exists anywhere in the capture, so it
  keeps proving what it claims (the `-pyry-verbose` flip works) but will now be satisfied by a record other
  than the one its "WHY A DEBUG RECORD IS DETERMINISTIC HERE" paragraph reasons about. No behaviour changes and
  no test goes red. Whoever next touches that guard should re-derive its paragraph; it is not this ticket's
  edit.
- **`emitMapped`'s doc says `ok==false` is "unreachable for every variant `Handle` routes here"** and that is
  still true — `turnevent.ModelList` lands on `Handle`'s `default:` and never reaches `emitMapped`. Confirmed,
  not a stale claim, and named here so the next reader does not re-check it.

## Scope re-check against the size table

Re-applied to the written spec, not the sketch.

| Limit | Boundary | This spec |
|---|---|---|
| Production source files created or modified | ≤ 3 | **2** — `internal/streamsup/runner.go`, `cmd/pyry/streamsup_runner.go` |
| Total written work | ≤ 400 | **~330** (see below) |
| New exported types or interfaces | ≤ 5 | **0** — one field on an existing struct |
| Consumer call sites needing simultaneous update | ≤ 10 | **1** — `mapStreamsupConfig` |
| Acceptance criteria | ≤ 5 | **4** |
| Distinct error/reject branches in a state machine | ≤ 10 | **1** |

Line estimate, grounded in the nearest analogue rather than asserted. #1206 added `Config.OnChildExit` to
this same file: `internal/streamsup/runner.go` +45, `internal/streamsup/runner_test.go` +230 = **275 lines**,
one production file (re-derived from `870be1cb`). This ticket is that shape plus one line of `cmd/pyry`
wiring and two mapper assertions: ~45 production in streamsup, ~7 in `cmd/pyry`, ~260 of tests (three
table-driven functions plus one counting helper), ~20 of mapper-test edits. **~330.**

The ticket's own analogue, #1604 at 363 lines across 4 production files, is the *interface* placement's cost
and does not apply: this design widens no interface, so the four fake-runner test files that dominated that
diff are absent here.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No findings. The boundary this ticket puts first production traffic across is
  subprocess stdout → daemon state, and it is a single explicit function: `emitModelList`, which is the sole
  reader of a `control_response` payload and the sole constructor of a `turnevent.ModelList`. This spec adds
  no second decode site and changes not a byte of that one. The outbound half crosses no boundary at all:
  `WriteInitialize` marshals two daemon-authored literals and a daemon-minted counter value, so no
  external input reaches the child's stdin on this path. What is genuinely new is that a boundary previously
  exercised only by tests is now exercised in production — which is the ticket's stated reason for the label,
  and is answered by the amplification analysis below rather than by a code change.
- **[Tokens, secrets, credentials]** Not applicable, and specifically so: the ticket's premise is that this
  round trip needs no credential and opens no new trust boundary. The `request_id` is not a secret — it is a
  monotonic counter from `nextControlID`, is not compared against anything, authorises nothing, and is
  deliberately not logged by either `logControlResponse` or this spec's new Debug record. Using
  `crypto/rand` for it would imply a secrecy property it does not have.
- **[File operations]** Not applicable. This design opens, creates, stats and renames no file. The one path
  value anywhere near it, `Config.ClaudeSessionsDir`, is untouched.
- **[Subprocess / external command execution]** No findings. `spawnAndWait`'s `exec.CommandContext`
  invocation, its argv, its `cmd.Dir`, its `cmd.Env`, its `cmd.Cancel` reap-then-SIGTERM teardown and its
  `WaitDelay` are all unchanged. Nothing user-controlled is added to the argv, no shell is introduced, and
  the ask is a write onto an already-open pipe rather than any new process interaction. The design
  deliberately places the ask **below** `cmd.Start` and **above** `cmd.Wait`, inside the existing lifecycle,
  so it adds no new process the teardown must reap.
- **[Cryptographic primitives]** Not applicable. No randomness, no comparison against a secret, no key
  material, no hashing on this path.
- **[Network & I/O — the amplification audit the label asks for]** No findings; this is the category the
  ticket names, so the reasoning is written out rather than asserted.
  - **Per ask, outbound:** exactly one `Write` of a fixed-shape envelope of roughly 90 bytes, whose only
    variable component is a decimal counter. There is no growth term.
  - **Per ask, inbound:** at most one `control_response` line, decoded once by `emitModelList` and bounded at
    construction — `maxModelListEntries` caps the entry count at 10 and reports the overflow rather than
    hiding it, and each retained entry is capped per-field, for a documented 10 KiB retained ceiling. What is
    *transiently* materialised before the cap applies is `json.Unmarshal`'s cost on one line, which is
    `maxTaskRosterEntries`' accepted and already-argued trade, not a new exposure this ticket opens.
  - **Client-driven rate:** a remote `new_session` frame drives `RestartFresh`, so a client does control the
    respawn rate and thereby the ask rate. The amplification factor is what matters, and it is far below 1:
    each respawn already costs a full `claude` process spawn — fork/exec, a new pipe pair, claude's own
    startup — and the ask adds one small pipe write plus one bounded decode on top of that. A client that
    wanted to burn daemon resources would drive the respawn and ignore the ask. **No new rate limit is
    warranted**, and adding one would be a defence against a failure mode that has not been observed and that
    the existing spawn cost already dominates.
  - **Retention:** at most one `turnevent.ModelList` per child, and it is dropped at `Handle`'s `default:`
    before reaching `emit` — so it enters no wire frame and no per-conversation event ring. There is no
    per-conversation multiplier here, which is the shape a bounded per-frame value can otherwise take on.
  - **Timeouts / slow-loris:** not applicable — no socket, no server, no read deadline in this design. The
    reply arrives on the stdout the parser already reads for every other line, on the same terms.
- **[Error messages, logs, telemetry]** No findings, and one explicit requirement carried into § Error
  handling. The reply half is already compliant with #833's "model / effort / YOLO values are NEVER logged at
  any level" posture: `logControlResponse` admits five attributes — a constant type, a keyword from a closed
  daemon-authored set, and three daemon-computed integers — and no value, display name, effort level,
  request id, error string or line byte. This spec's **one new** record, on the ask's failure path, is held to
  the same rule: it may carry the wrapped error (daemon-authored — `WriteInitialize` wraps only its own
  literals and an `*os.File` write error, never claude's bytes) and must carry no request id and no payload.
  Nothing decoded from the reply is logged anywhere in this slice, because nothing in this slice decodes it.
- **[Concurrency]** No findings. No lock is added, so no lock ordering is created. The ask runs on the Run
  goroutine with no Runner lock held (`Stdin()` releases `r.mu` before returning), which is the same discipline
  `Interrupt` and `RevokeBypass` follow. No goroutine is spawned, so none can leak. On shutdown mid-ask the
  write either lands in a pipe nobody will read or fails with EPIPE, and the failure is absorbed — no partial
  state is written anywhere, because the ask writes to no durable store. The TOCTOU shape that would matter
  here — publish the handle, then have it swapped before the write — cannot occur: `spawnAndWait` runs on one
  goroutine and is the only writer of the handle, and the worst case (`takeStdin` ran first) yields
  `ErrNoLiveChild`, which is checked before any byte is written.
- **[Threat model alignment]** No findings. The ticket is daemon-internal: no wire frame is added, removed or
  reshaped, so `docs/protocol-mobile.md` § Security model has no relevant threat to re-answer. The one
  client-reachable lever, respawn-driven ask rate, is audited under Network & I/O above and is out-scaled by
  the spawn it rides on. Retaining and publishing the model list is where a client-visible surface first
  appears; that is #1840 and #1720, and each carries its own review.

**Reviewer:** architect (self-review per the security-review checklist)
**Date:** 2026-08-27
