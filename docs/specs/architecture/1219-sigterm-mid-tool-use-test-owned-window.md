# #1219 — Test-owned open window for `TestRealClaude_SigtermMidToolUse`

**Size:** S (PO's `size:s` confirmed, not overridden). Test-only, one file.
**Scope:** `internal/e2e/realclaude/sigterm_mid_tool_use_test.go`. Zero production
source files. No new exported identifiers.

---

## Status — re-specified 2026-07-28

**Part I of this design shipped and is green.** Commit `6ca5b37` on `feature/1219`
delivered the FIFO ownership inversion, `holdFIFO`, the rendezvous gate,
`TestHoldFIFO_RendezvousAndRelease`, and the header rewrite. Two live runs against
claude 2.1.220 confirmed invariants 1, 2, 3 and 4a all pass, and that claude
attached **no `timeout` field** to the `cat <fifo>` call — the escape hatch's
trigger was *falsified*, not met.

**Part II is the remaining work.** The live runs falsified invariant 4b's
*operationalisation* — "no matching `tool_result`" — while leaving its *semantics*
intact. claude writes a synthetic `tool_result` into the session JSONL during its
own teardown, marked `interruptedByShutdown: true`. That marker is claude recording
the very fact invariant 4 exists to prove, but it lands on the surface the old
assertion forbade.

**Superseded by Part II** (do not implement these as written):

| Superseded | Where | Replaced by |
|---|---|---|
| "no matching `tool_result` was written" as invariant 4b | Part I § *Error handling* → *Invariant 4b's failure message* | Part II § *The discriminator* |
| "any `tool_result` at all means claude ended the call" | same | Part II § *The discriminator* — true only of a `tool_result` **without** the interruption marker |
| The header's branch-B paragraph and its `subtype != success` flip instruction | shipped at `sigterm_mid_tool_use_test.go:99-108` | Part II § *Header changes (AC5)* |

Everything else in Part I stands and **must not regress** (AC3). The pre-rework
prose is preserved verbatim in git at `93c6d83` if the original reasoning is needed.

---

## Files to read first

Line numbers are against `feature/1219` at `6ca5b37`. Your own edits shift them —
re-derive before citing them anywhere else.

| Path | What to extract |
|---|---|
| `internal/e2e/realclaude/sigterm_mid_tool_use_test.go:20-21` | The invariant-4 statement, **falsified**. "no matching tool_result was written" is the sentence AC5 makes you rewrite. |
| `internal/e2e/realclaude/sigterm_mid_tool_use_test.go:23-41` | The fragility-history block. Defeats #1 and #2 are recorded; you append #3 (the 2026-07-28 discovery). Keep the existing two entries verbatim. |
| `internal/e2e/realclaude/sigterm_mid_tool_use_test.go:72-79` | The "On defeat #3" paragraph. Renumbered and reordered by Part II § *Error handling*. |
| `internal/e2e/realclaude/sigterm_mid_tool_use_test.go:99-108` | The branch-B paragraph, **superseded**. Branch A is now observed. Its `subtype != success` instruction names a field that does not exist on this surface — see the `tool_loop_test.go` row below. |
| `internal/e2e/realclaude/sigterm_mid_tool_use_test.go:258-279` | The `waitForBashToolUseOnDisk` → `SIGTERM` seam. The pre-signal snapshot (Part II § *Second call site*) goes between these two, and nowhere else. |
| `internal/e2e/realclaude/sigterm_mid_tool_use_test.go:342-397` | Invariants 4a and 4b. 4a is unchanged; 4b's inline scan is what the classifier replaces. |
| `internal/e2e/realclaude/sigterm_mid_tool_use_test.go:400-477` | `holdFIFO` — **unchanged** (AC3). Its HAZARD comment is why the release stays in `t.Cleanup`; the classifier's correctness depends on that ordering holding. |
| `internal/e2e/realclaude/sigterm_mid_tool_use_test.go:479-564` | `TestHoldFIFO_RendezvousAndRelease` — **unchanged**. It is AC4's oracle for "holdFIFO lifetime bug"; the new failure message points at it by name. |
| `internal/e2e/realclaude/sigterm_mid_tool_use_test.go:766-784` | `findBashToolUse` returns `(id, index)`. The credential-free test calls it too, so the fixture exercises the same entry path as the live test. |
| `internal/e2e/realclaude/tool_loop_test.go:160-183` | `contentBlock` + `parseContentBlocks`. `IsError bool` at `:167` is `omitempty`-tagged, so **absent and `false` are indistinguishable** after decode. There is no `Input` field and no `Subtype` field — `Subtype` lives on `resultTrailer` (`:194-203`), a different surface. Read, do not modify: it is shared across the package. |
| `internal/e2e/realclaude/resilience_test.go:105-129` | `TestRealClaude_BashTool_NonZeroExit` asserts `is_error == true` on a Bash `tool_result` for a command that **ran to completion** and exited non-zero. This single fact decides the discriminator — see Part II § *Why not `is_error`*. |
| `internal/e2e/realclaude/allowed_tools_enforcement_test.go:166-192` | `structuredDenialHit` reads a top-level `is_error` as a **permission-denial** signal. Third distinct meaning for the same flag in this one package. |
| `internal/e2e/realclaude/fixtures.go:145-167` | `ReadJSONL` — the live parse path. Note `jsonl.NewReader(src io.Reader, jsonl.Config{})`: the zero-value `Config` is proven safe, and the source is any `io.Reader`, which is what lets the credential-free test parse a string. |
| `internal/agentrun/jsonl/reader.go:45-83` | `Event`. `Raw` (`:71`) is the verbatim line bytes; `Kind` (`:77`) is **derived from the line's `type` field**, not caller-supplied. Feed the fixture envelopes through `NewReader` so `Kind` is derived — hand-setting it would let the fixture pass while the live path skips the line. |
| `internal/e2e/realclaude/per_agent_test.go:138-144` | `truncate(b []byte) string`, 1 KiB cap. Reuse for the envelope dumps. |
| `Makefile:55-57` | `make e2e-realclaude` = `go test -tags e2e_realclaude ./internal/e2e/realclaude/...`. The credential-free test runs under this tag with no auth. |
| `docs/knowledge/codebase/422.md:10,16` | Records the old "tool_result-absent" invariant and the same wrong `subtype != success` flip instruction. **Not yours to edit** — see Part II § *Out of scope*. |

---

# Part I — delivered (`6ca5b37`), do not regress

## Context

`TestRealClaude_SigtermMidToolUse` is the only live coverage of the
shutdown-while-a-tool-is-running contract (#422). It went RED because invariant 4
— *the signal genuinely landed mid-`tool_use`* — could no longer be staged.
claude 2.1.220 attaches its own timeout to a Bash call and, on expiry, backgrounds
the command and returns a `tool_result` immediately, so `tail -f /dev/null`
reported itself finished before the test could signal.

In the 2026-07-27 probe the `timeout:5000` sat inside the `tool_use` **params**
with a matching `description`. The *model* chose the bound, on a command whose
shape advertises "blocks forever". So the lever was never "ask claude for a longer
timeout" — steering the model is the treadmill by another name. The lever is **who
owns the thing that blocks**.

## The inversion

Blocking became a property of an artifact the test creates and holds:

```
test:   mkfifo <workdir>/sigterm-hold
test:   goroutine → open(fifo, O_WRONLY)   [blocks: no reader yet]
claude: Bash → cat <workdir>/sigterm-hold  [blocks: no writer yet]
        ↓ both opens complete at the same instant — a rendezvous
test:   holds the write end, never writes  → cat blocks in read() forever
test:   Close() (t.Cleanup only)           → cat sees EOF and exits
```

Three properties, two of them load-bearing:

1. **The window's end is the test's.** `cat` cannot complete while the test holds
   the write end; the only release is `holdFIFO`'s `t.Cleanup`, which by
   construction runs after the test body. Invariant 1 therefore cannot pass
   vacuously. `holdFIFO` never hands the `*os.File` to the caller — that is what
   makes the ordering structural rather than a matter of discipline.
2. **The window's start is race-free.** The blocking `open(O_WRONLY)` returns at
   the instant `cat` starts — no 100 ms poll lag. That lag is what lost the race
   against a 5 s model-chosen timeout.
3. **Not load-bearing:** the neutral command and FIFO name only reduce the cue
   that might prompt a defensive timeout. If claude bounds every Bash call
   regardless, (3) buys nothing while (1) and (2) still hold.

## What shipped

- `holdFIFO(t, path) <-chan struct{}` — file-local, with the vacuous-pass hazard
  documented on the function.
- The rendezvous gate between `waitForDirectChild` and `waitForBashSubprocess`.
- `sigtermProcessName = "cat"`; `sigtermPromptFormat` interpolating the FIFO path.
- `TestHoldFIFO_RendezvousAndRelease` — credential-free, four scenarios.
- The header rewrite with defeats #1 and #2.

**Confirmed by two live runs.** claude's `tool_use` input was
`{"command":"cat …/sigterm-hold"}` with no `timeout` field. Invariants 1, 2, 3 and
4a pass. AC3 pins all of this: it must still hold when Part II lands.

---

# Part II — re-spec: invariant 4's operationalisation

## Context

claude writes this into the session JSONL during its own teardown, after the
signal:

```json
{"type":"tool_result","content":"The user doesn't want to proceed with this tool
 use. The tool use was rejected...","is_error":true,"tool_use_id":"toolu_01WHR…"}
… "toolDenialKind":"user-rejected","interruptedByShutdown":true,"version":"2.1.220"
```

The content block sits inside a `user`-kinded line; `toolDenialKind`,
`interruptedByShutdown` and `version` are **top-level fields of that line**,
siblings of `type` and `message` — not fields of the content block.

`interruptedByShutdown: true` is claude recording *that the signal landed
mid-`tool_use`*. The semantics of invariant 4 are not merely intact, they are now
provable more strongly than before: the rendezvous fired and the test still held
the write end, so `cat` could not have exited on its own. Only the old
operationalisation — inherited from #422's branch-B assumption — is falsified.

**Dropping the on-disk check is the trap.** It looks elegant — the rendezvous plus
a still-held write end reads as stronger structural proof than any file
inspection. It is vacuous under the exact defeat this ticket exists to fix: if
claude backgrounds the command, `cat` keeps running (backgrounded, not killed),
the FIFO stays held, the process group still exists, and pyry still reaps it on
SIGTERM. The rendezvous fires, invariants 1, 2 and 3 all pass — with the scenario
unstaged. The on-disk check is the only thing separating "claude ended the call
while the process lingers" from "the signal landed mid-call".

## The one job

The rendezvous is now a hard precondition. Anything reaching invariant 4b has
already had `cat` open the FIFO, which screens out the #563 refusal shape entirely
(a refused command never runs, so the rendezvous never fires). Two survivors:

- **(A) claude bounded the call and backgrounded it** → fixture defeat → FAIL.
- **(B) claude tore down on SIGTERM and wrote an interruption marker** → staged
  correctly → PASS.

A `holdFIFO` lifetime bug is the third outcome and must also fail.

## The discriminator

**Accept only on positive evidence of shutdown interruption.** Stated as the one
invariant the whole design rests on:

> Every unknown resolves to REJECT. Absence of evidence is never acceptance.

This is what makes a vacuous pass structurally impossible rather than merely
unlikely, and it is why the choice of field is settled by *which way it fails*
as much as by what it means.

Discriminator: the envelope-level **`interruptedByShutdown`** flag on the line
carrying the matching `tool_result`.

### Why not `is_error`

The ticket ranks `is_error` first for durability, and correctly — public wire
vocabulary, already on `contentBlock`, zero struct change. It still loses, on a
fact stronger than the worst-case rule:

`resilience_test.go:126-129` asserts `is_error == true` on a Bash `tool_result`
for a command that **ran to completion** and exited non-zero. Accepting on
`is_error == true` would therefore accept a completed command as proof the signal
landed mid-call — precisely the outcome invariant 4 exists to reject. The flag
means "this tool call failed", and this package already reads it with three
different meanings across three surfaces (non-zero exit at `resilience_test.go`,
permission denial at `allowed_tools_enforcement_test.go:190`, error results on the
trailer). It carries no information about *why* a call ended.

AC2's worst-case rule reaches the same verdict independently: the 2026-07-27
transcript retained the backgrounding envelope's `content` but not its `is_error`,
so the rule forces `is_error: true`, and envelope (B) is also `is_error: true` —
zero separation. Two independent arguments, same answer. **No re-probe is
required**; the `resilience_test.go` fact settles it regardless of what a re-probe
would show.

A further mechanical objection: `contentBlock.IsError` is `omitempty`-tagged, so
after decode "absent" and "`false`" are indistinguishable. Even a favourable
re-probe could not be encoded faithfully without a shared-struct change.

### Why `interruptedByShutdown`, and the asymmetry that makes it honest

It names the exact fact. Its cost is real and must be recorded in the header
(AC5): claude-internal, undocumented, **zero occurrences repo-wide** before this
ticket (verified 2026-07-28).

The worst-case rule is applied asymmetrically — `is_error: true` assumed on
envelope (A), `interruptedByShutdown` assumed absent. That is principled, not
convenient:

- `is_error` is **generic**. Its worst case is not a guess; it is the field's
  *observed* value on a non-shutdown termination in this same package.
- `interruptedByShutdown` is **specific**. For envelope (A) to carry it as `true`,
  claude would have to assert that a call it backgrounded during normal operation
  was interrupted by a shutdown that had not happened. That is a self-contradiction,
  not a plausible value.
- The failure directions differ, and this is decisive. If claude ever drops or
  renames `interruptedByShutdown`, the check **rejects** → the test goes RED →
  someone looks. Fail-closed comes free from Go's zero value: a `bool` field
  decoded from a line that lacks it is `false`, which is the reject side. No extra
  code enforces it.

`toolDenialKind: "user-rejected"` is excluded: semantically mislabelled (no user
rejected anything), which makes it exactly the kind of field a later release
corrects. claude prose is excluded by AC4.

## Design

One new seam, a pure function, called from two places with two different accept
sets. Extracting it is what makes AC2's credential-free test possible at all — a
fixture test that reimplemented the scan would prove only that the fixture agrees
with itself.

```go
// toolResultKind classifies a matching Bash tool_result for invariant 4.
type toolResultKind int

const (
    toolResultAbsent      toolResultKind = iota // no matching tool_result
    toolResultInterrupted                       // matching, interruptedByShutdown: true
    toolResultEnded                             // matching, no interruption marker
)

// classifyBashToolResult scans events after index `from` for a tool_result
// whose tool_use_id is toolUseID and classifies it. The second return is the
// matching entry (zero value when absent) so callers can quote it verbatim.
func classifyBashToolResult(events []JSONLEntry, toolUseID string, from int) (toolResultKind, JSONLEntry)

// interruptedByShutdown reports whether a JSONL line carries claude's
// envelope-level interruptedByShutdown flag set to true. Absent → false.
func interruptedByShutdown(raw json.RawMessage) bool
```

Both file-local, per #422's "promote to `fixtures.go` only when a second test
needs it" precedent. `interruptedByShutdown` decodes into a local anonymous
struct — **do not add the field to `contentBlock`**: it is envelope-level, not a
content block, and `contentBlock` is shared across the package. Zero shared-struct
edits keeps this ticket's blast radius at one file.

A `String()` method on `toolResultKind` is optional; add it only if the failure
messages read better for it.

### First call site — invariant 4b (post-exit, primary)

Replaces the inline scan at `:365-397`. Accepts `toolResultAbsent` (claude was
torn down before writing anything — still valid evidence) and
`toolResultInterrupted`. Fails on `toolResultEnded`.

### Second call site — pre-signal snapshot (temporal, field-independent)

Between `waitForBashToolUseOnDisk` succeeding and `cmd.Process.Signal`, re-read
the JSONL and assert `classifyBashToolResult(...) == toolResultAbsent`. **Any**
matching `tool_result` on disk before the signal means the call ended before the
test signalled — the scenario was never staged.

This is deliberately belt-and-suspenders with *different fabric*: the primary
check reads a claude-internal field, this one reads a timing fact and depends on
no claude field at all. (A) happens before SIGTERM by construction — that is what
defeats the fixture — and (B) happens during teardown, after it. The ticket is
right that it is not sufficient alone (a `tool_result` written just before the
signal but flushed just after is missed), which is why the post-exit check stays
primary. Its second contribution is diagnostic: it fails at the moment of truth
with an unambiguous message instead of surfacing 40 s later as an ambiguous 4b.

Two details: if `findBashToolUse` returns `""` on the pre-signal read, **skip**
the check rather than fail — `waitForBashToolUseOnDisk` just returned true, so a
miss is a transient read and post-exit 4a will catch a genuine absence. And a
concurrent partial write cannot cause a false positive: `jsonl.NewReader` silently
retains trailing partial bytes and never surfaces them.

### Considered and rejected — asserting on `input.timeout`

The `tool_use` envelope carries claude's chosen bound and is on disk *before* the
command runs, so it survives teardown. Asserting "no `input.timeout`" would be a
disk-durable, tool_result-independent defeat detector.

Rejected: a *generous* bound (say 10 minutes) does not defeat anything, so the
assertion would fail a run that staged correctly. The timeout's presence is
**diagnostic** — it tells you which defeat you are in — not **dispositive**. The
failure message dumps the verbatim `tool_use` envelope and orders it first for
exactly that reason. Recorded here so it is not re-litigated.

### Accepted risk — the residual hole

If claude backgrounds the command *and* the `tool_result` never reaches disk
before SIGTERM, both checks see nothing → `toolResultAbsent` → pass, vacuously.
The window is narrow (the test signals only after a 100 ms-polled disk read
succeeds) and has never been observed. Nothing already in the test closes it:
under backgrounding `cat` keeps running, so invariant 1 still passes.

Not closed, per evidence-based fix selection — the two checks cover every observed
shape. If defeat #4 ever lands here, the closer is in *Open questions* below.

## Error handling

### Invariant 4b's failure message (AC4)

Three outcomes, each settled by an artifact already present in the run. No prose
matching anywhere — a string that goes stale every release would rebuild the
treadmill inside the failure path.

Lead with the structural fact, unchanged from Part I and still true: the fixture
command blocks on a FIFO this test created and still held open when the assertion
ran (`holdFIFO` releases only in `t.Cleanup`, after the test body), so it cannot
have completed on its own. Then, in order:

1. **Was `TestHoldFIFO_RendezvousAndRelease` (this file) also RED in this run?**
   Yes → `holdFIFO` lifetime bug; the window mechanism is broken, fix that first
   and treat this failure as downstream noise. No → the write end held; continue.
2. **Does the dumped `tool_use` envelope carry an `input.timeout` field?**
   Yes → claude bounded the call itself. Fixture defeat #4, a claude-side policy
   change, **not** a pyry regression. No → claude ended the call without bounding
   it, or the interruption marker changed shape: compare the dumped `tool_result`
   envelope against the 2026-07-28 shape in this file's header. The discriminator
   is the envelope-level `interruptedByShutdown` flag; if claude renamed or
   dropped it, that is defeat #4 landing on the *field* rather than on the
   command.
3. **Only if neither:** pyry's SIGTERM path regressed — it let claude finish the
   tool call instead of tearing it down.

Both escape-hatch cases route to `needs-rework:po` on #1219 with the probe
transcript, never to a fifth command guess. Dump both envelopes verbatim via
`truncate`.

### Pre-signal snapshot failure

Short and unambiguous: a matching `tool_result` was already on disk when SIGTERM
was sent, so claude ended the Bash call before the test signalled and the scenario
was never staged. Dump the `tool_use` and `tool_result` envelopes; point at the
header's fragility history. This is a fixture defeat by definition — it cannot be
a production regression, because pyry has not been signalled yet.

### Unchanged (AC3)

Invariants 1, 2, 3 and 4a keep their current messages verbatim. `holdFIFO`, the
rendezvous gate, the constants, and `TestHoldFIFO_RendezvousAndRelease` are
untouched.

## Testing strategy

### Credential-free two-envelope test (AC2)

`TestClassifyBashToolResult_ProbeEnvelopes`, same file, same build tag, **no
credentials and no claude** — it runs on every `make e2e-realclaude` regardless of
auth.

Build each case as a JSONL **string** (an assistant `tool_use` line plus, where
applicable, a `user` `tool_result` line), parse it with
`jsonl.NewReader(strings.NewReader(s), jsonl.Config{})` into `[]JSONLEntry`, then
run `findBashToolUse` → `classifyBashToolResult`. Parsing rather than hand-building
entries is the point: `Event.Kind` is derived from the line's `type` field, so a
hand-set `Kind` could make the fixture agree with a live path that skips the line.
This also exercises the same two entry points the live test uses.

**Provenance is part of the deliverable.** Comment each fixture with which bytes
are verbatim from the probe and which are reconstructed. Envelope (B)'s content
block and its three envelope-level fields are verbatim from the 2026-07-27/28
transcripts; the `user` line wrapper around them, and envelope (A)'s
`tool_use_id`, background ID and log path, are reconstructed. A future reader must
not mistake a reconstruction for a capture.

Table rows:

| Case | Envelope | Expected |
|---|---|---|
| 1 | (A) 2026-07-27 backgrounding — verbatim `content`, `is_error: true` per AC2's worst-case rule, no `interruptedByShutdown`; `tool_use` input carries `"timeout":5000` | `toolResultEnded` |
| 2 | (A) with `is_error` **absent** | `toolResultEnded` |
| 3 | (B) 2026-07-28 shutdown interruption — verbatim, `interruptedByShutdown: true` | `toolResultInterrupted` |
| 4 | `tool_use` only, no `tool_result` | `toolResultAbsent` |
| 5 | `tool_result` with `interruptedByShutdown: true` but a **different** `tool_use_id` | `toolResultAbsent` |
| 6 | `tool_result` with `interruptedByShutdown: false` written explicitly | `toolResultEnded` |

Rows 1+2 together are AC2's worst-case rule made mechanical: the verdict on (A) is
*invariant to `is_error`*, so no re-probe can change it. Row 5 stops an unrelated
interruption marker from laundering a defeat. Row 6 pins fail-closed on the
explicit-`false` shape, not only the absent one.

### Live gate (AC1)

`make e2e-realclaude` against claude 2.1.220 or later with
`TestRealClaude_SigtermMidToolUse` **passing and not skipped**. One haiku call,
~$0.005. A SKIP is not acceptance — the ticket carries `needs-real-claude` for
exactly this reason. Expect `toolResultInterrupted`; `toolResultAbsent` also
passes and is not a problem (it is the pre-2.1.220 shape).

## Header changes (AC5)

Every falsified sentence, so none is missed. Line numbers are pre-edit and shift
as you go.

1. **`:20-21`** — restate invariant 4: a Bash `tool_use` envelope is on disk, and
   any matching `tool_result` is a shutdown-interruption artifact, never a
   completion and never claude bounding the call.
2. **`:23-41`** — append defeat #3 to the fragility history: 2026-07-28, claude
   2.1.220 writes a synthetic `tool_result` during teardown carrying
   `interruptedByShutdown: true` (plus `toolDenialKind: "user-rejected"`, which is
   mislabelled — no user rejected anything). Keep defeats #1 and #2 verbatim.
   Record that this is *not* a fixture defeat: it is claude corroborating
   invariant 4 on a surface the old assertion forbade.
3. **`:72-79`** — renumber to defeat #4 and reorder to the three-way check in
   § *Error handling*.
4. **`:99-108`** — replace the branch-B paragraph. Branch A is now **observed**,
   not hypothetical. Record that the old flip instruction named `subtype`, which
   is a field of `resultTrailer` (`tool_loop_test.go:194-203`), not of
   `contentBlock` — the instruction's *direction* was right, its field name was
   not. Do not leave a live pointer at `subtype`.
5. **New paragraph — where defeat #4 lands.** Name `interruptedByShutdown` as the
   field the discriminator depends on; state that it is claude-internal and
   undocumented with zero repo-wide occurrences before this ticket; state that it
   fails closed, so a rename or removal turns the test RED rather than silently
   green. State why `is_error` was rejected in one line (it is `true` on a Bash
   command that ran to completion and exited non-zero — `resilience_test.go:126`),
   so nobody re-litigates it.
6. **`:144-145`** (`sigtermPromptFormat`'s doc comment) — "The command stays in
   flight — blocked in read(), no tool_result — until SIGTERM lands" is now
   wrong: a `tool_result` *is* written, during teardown. Fix the clause; the rest
   of the comment stands.
7. **`:354`** — "Invariant 4b: matching tool_result absent (branch B)" is
   superseded.

## Out of scope

- `docs/knowledge/codebase/422.md:10,16` records the old tool_result-absent
  invariant and the same wrong `subtype != success` instruction. **Documentation
  phase owns it, after the PR merges** — not a developer deliverable, and not an
  AC. Same for any `docs/knowledge/codebase/1219.md`.
- Whether claude's auto-backgrounding changes production behaviour for
  `pyry agent-run` — split to #1221.
- Restructuring the suite's coverage model to drive the scenario without claude in
  the loop — the filer's fallback, deliberately out of scope. The bounded escape
  hatch (`needs-rework:po`) is the exit, not this.
- No production source file is touched. If you find yourself editing outside
  `internal/e2e/realclaude/sigterm_mid_tool_use_test.go`, stop.

## Open questions

1. **Closing the residual hole, if it ever bites.** The closer is not "assert no
   `input.timeout`" (rejected above) but "assert `input.timeout`, *if present*,
   exceeds the test's worst-case time-to-SIGTERM" — ~75 s given the current
   25+25+10+15 budgets. No false-failure risk, since a generous bound passes.
   Deferred because the hole has never been observed and the check costs a magic
   number. Recorded so the next contributor does not have to re-derive it.
2. **Does `interruptedByShutdown` survive a claude upgrade?** Unknown, and
   unknowable without the next release. The design's answer is the failure
   direction, not a prediction: absence rejects, so the cost of being wrong is a
   RED test with a message that names the field.
3. **Is the `waitForBashToolUseOnDisk` gate still the binding constraint?** Carried
   forward from Part I, unresolved. pyry emits the assistant `tool_use` on its own
   stdout stream-json before the JSONL flush, so a stdout-based gate would be
   earlier. AC3 forbids weakening the disk-backed assertion, so the gate stays. If
   the live run shows the flush is binding, note the measurement in the header for
   whoever faces defeat #4 — do not change the gate under this ticket.
