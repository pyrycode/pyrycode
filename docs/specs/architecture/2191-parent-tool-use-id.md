# #2191 — carry `parent_tool_use_id` on `tool_use` and `tool_result`

A subagent's tool calls reach a client today as ordinary top-level rows interleaved with the
parent's. This ticket declares the line-level key claude already sends, carries it onto the two
tool frames, and publishes it on the wire so a client can nest three parallel subagents as three
collapsible groups instead of thirty unrelated rows.

## Files read

- `internal/streamsup/parser.go` → `consumeLine` — the type switch; its `assistant` and `user`
  arms are the only two places this key may be read, and `consumeToolProgress` sits in a
  structurally disjoint branch, which is what scopes the decode.
- `internal/streamsup/parser.go` → `assistantErrorLine`, `decodeAssistantError` — #2224's
  sidecar. Its docblock argues for SEPARATE decode targets so a hostile `message` cannot blank
  the sidecar and vice versa. That argument decides the assistant side here.
- `internal/streamsup/parser.go` → `userLine`, `emitUser` — #2024/#2087's sidecar. Its docblock
  argues the opposite way, "ONE decode, not two", because a user line routinely carries a whole
  file. That argument decides the user side here. Its `ToolUseResult` is `json.RawMessage`
  precisely so the decode cannot fail — the property this ticket must not break, because
  `IsSynthetic` rides the same struct and a failed decode resurrects the harness-prose rows #2087
  removed.
- `internal/streamsup/parser.go` → `emitAssistant` — takes only the decoded message today, so it
  cannot reach a line-level sibling; `emitUser`'s signature is the in-file precedent for fixing
  that.
- `internal/streamsup/parser.go` → `boundStopField`, `maxTurnEndStopField` — the drop-rather-than-cut
  precedent for a claude-authored string that rides the wire.
- `internal/streamsup/parser.go` → `maxTaskFieldID`, `dropField` — the cap for a machine-generated
  IDENTIFIER, and #2233's `consumePermissionDeniedLine` applies it to a `tool_use_id` on a frame
  that ships. This is the closer precedent of the two.
- `internal/streamsup/parser.go` → `consumeToolProgress` — the overloaded reading of the same key.
  Its census is what proves the committed `tool_progress` capture carries non-null values that
  mean something else entirely.
- `internal/turnevent/event.go` → `ToolStart`, `ToolUpdate` — the two carriers. Every literal of
  both is keyed, so a new field breaks no fixture.
- `internal/turnbridge/outbound.go` → `MapEvent` — the `ToolStart` and `ToolUpdate` arms, and the
  `ToolCallDenied` arm beside them, whose "every string crosses VERBATIM and nothing is re-capped"
  paragraph states this file's standing terms.
- `internal/protocol/interactive.go` → `ToolUsePayload`, `ToolResultPayload` — the wire frames,
  and `ToolResultPayload`'s docblock on the never-droppable `4413` class, which is the reason the
  bound below is not optional.
- `internal/streamsup/capture_test.go` → `capturedLines` — the reader whose docblock forbids by
  name growing it a path parameter; every sibling reader restates its provenance checks rather
  than borrowing them.
- `internal/streamsup/tool_progress_capture_test.go` → `capturedToolProgressLines` — already in
  this package, already reading the fixture whose 12 non-null values are the overload. It is
  reused as-is; nothing new is minted for the negative test.
- `internal/streamsup/compaction_capture_test.go` → `compactionReaderGate` — #2229's four-state
  (fixture, pin) machine with exactly one legal skip. This ticket's reader copies its shape,
  because the fixture cannot exist until after the live gate runs.
- `internal/e2e/realclaude/compaction_capture_test.go` → `TestRealClaude_CompactionCapture`,
  `ccapWriteRecord`, `ccapRecord.fixtureWorthy` — the freshest probe, and the one that reuses the
  most of the shared rig. Its gate comment argues fixture-absence over `PYRY_PROBE_*`.
- `internal/e2e/realclaude/dropped_line_capture_test.go` → `dropcapRecorder`, `dropcapRedactor`,
  `dropcapScanner`, `dropcapMakeEntry`, `dropcapWaitForChild` — the reusable rig. The redaction
  scheme deliberately preserves `tool_use_id` and `parent_tool_use_id` VALUES, which is what keeps
  the join readable in the committed bytes.
- `internal/e2e/realclaude/ask_user_question_capture_test.go` → its argv block — "THERE IS NO
  `--allowed-tools` AT ALL", the note the ticket cites. Reaching the Agent tool needs the
  unrestricted set.
- `docs/knowledge/features/streamsup-package.md`, `.../turnevent-package.md`,
  `.../protocol-package-interactive-event-payloads.md` — the package overviews for the three
  layers this crosses.
- `docs/protocol-mobile.md` § `tool_use`, § `tool_result` — the two tables that gain a row, and
  § Error codes `4413`, which states that a frame over the envelope cap is lost rather than
  truncated.

## Context

Every `assistant` and `user` line claude emits carries `parent_tool_use_id`: null on the main
conversation, or the `tool_use_id` of the Agent/Task call that spawned the subagent producing the
line. Nested subagents carry the id of the *inner* Agent call, so a client rebuilds the whole tree
by following ids. The daemon declares the field nowhere; the only mention in Go source is
`userLine`'s docblock, which names it as a key the decoder deliberately does not read.

**The key is overloaded across line types, and that is the finding the design turns on.** On a
`tool_progress` line the same key means *the tool call this progress frame belongs to*: each
heartbeat's own `tool_use_id` is a synthetic `…-heartbeat-N` and its `parent_tool_use_id` is the
real call's id. A decoder that generalised the spawned-by reading to `tool_progress` would nest an
ordinary Bash heartbeat underneath its own Bash row. This is `userLine`'s
`tool_use_result`/`toolUseResult` hazard arriving from the other direction — same key, two
meanings — so the decode is scoped to the two line types measured, and to nothing else.

`assistant_delta` is out of scope (#2192 owns it): without `--forward-subagent-text` a subagent's
text never arrives, so the field would be empty on every frame the daemon can produce today.

**An ADR is not warranted.** Nothing here is a new architectural direction — it is the fifth
claude-authored field to walk the same sidecar → turnevent → protocol → docs path (#2024, #2087,
#2223, #2224). The one genuinely new judgement, the bound, belongs beside its constant.

**Observed and deliberately not fixed:** `docs/protocol-mobile.md` § `tool_result`'s field table
has no `result_detail` row, though `ToolResultPayload` has carried the field since #2024. That is
a pre-existing gap in a file this ticket edits; adding the row is not what this ticket was asked
for, and widening the diff to tidy it is the scope creep the pipeline flags. Worth its own
one-line ticket.

## Design

### The value's path

```
claude stdout line  ─┬─ type "assistant" ──> consumeLine ──> emitAssistant(msg, line) ──> ToolStart.ParentToolCallID
                     ├─ type "user"      ──> consumeLine ──> emitUser(msg, line)      ──> ToolUpdate.ParentToolCallID
                     └─ type "tool_progress" ──> consumeToolProgress   (NEVER reads the key)
                                                    │
        turnbridge.MapEvent ──> ToolUsePayload.ParentToolUseID  / ToolResultPayload.ParentToolUseID
                                                    │
                                        wire: "parent_tool_use_id"
```

### Decode targets — one each way, and each doc's own argument decides which

The ticket asks whether to widen `assistantErrorLine`, widen `userLine`, or add a third target.
The two docblocks argue opposite ways on purpose, and the answer is that **both are right about
their own line**:

- **The `user` line widens `userLine`.** Its stated rule is "ONE decode, not two", because such a
  line routinely carries a whole file's contents and a second full pass to fetch one short string
  would double that scan for nothing. `emitUser` already decodes it once.
- **The `assistant` line gets a separate target**, `assistantParentLine`. `assistantErrorLine`'s
  stated rule is that the two targets must fail independently, so a hostile `error` cannot blank
  the sidecar and a hostile `message` cannot blank the category. Folding this key into that struct
  would put a third value behind the same single point of failure, which is what that doc exists
  to refuse. The second pass is affordable here for the reason it is not affordable on the user
  side: an assistant line carries the model's own blocks, not a file's contents.

**Both new fields are `json.RawMessage`, and on the user side that is load-bearing rather than
stylistic.** `userLine`'s existing fields are chosen so the decode *cannot fail*: `ToolUseResult`
is a RawMessage that accepts any valid JSON value, and the whole struct's failure mode is
documented as "ul stays zero, the block surfaces". A `string` field would break that — a
`parent_tool_use_id` of `7` or `{}` would fail the whole `userLine` decode, zeroing `IsSynthetic`
along with it and resurrecting exactly the harness-prose rows #2087 removed, including the 87 KB
skill body that ticket names. That is a disclosure regression reachable by a value claude
controls. A RawMessage target cannot fail, so the property holds unchanged.

The assistant target takes the same type for a smaller reason: it lets **one** converter decide
what a valid value is, so the two arms cannot drift into disagreeing about the same key.

### The converter and the bound — one function, one decision point

`parentToolUseID(raw json.RawMessage) string` is the whole of the semantics: a JSON string yields
its decoded value; every other JSON value, and an absent key, yields `""`. It also applies the
bound, so the cap has exactly one site.

**The bound is `maxTaskFieldID` (256), and it DROPS rather than cuts.** The ticket names this an
open decision and asks for a choice with a reason:

- *Which cap.* `maxTaskFieldID` is the constant for a machine-generated identifier, which is what
  this value is; observed values are `toolu_`-prefixed and around 30 bytes, so 256 is roughly 8×
  the observation — the multiple-of-observation form that constant already uses. A new constant
  would be a second number to keep correct against a value of the same class. `ToolUseID`'s
  verbatim pass-through on these same frames is the *older* precedent, and it is the one not
  followed: #2233 is the newer one, and it caps a `tool_use_id` at `maxTaskFieldID` on a frame
  that ships.
- *Why bound at all.* `tool_result` is never-droppable control class (`4413`). A frame over the
  65519-byte application-envelope cap is **lost, not truncated** — the operator sees no row rather
  than a shortened one — and sustained oversize frames are what reaches the per-session
  push-queue byte ceiling that tears the session down. Leaving a claude-authored string bounded
  only by `defaultMaxParseBuf`'s 4 MiB puts that whole budget on a never-droppable frame.
- *Why drop, not cut.* This is a **join key**, not prose. A cut id matches no `tool_use_id` while
  still looking like one, and a client joining on a prefix could attach a row to the wrong parent.
  Dropping degrades to `""`, which means "main thread" — exactly the pre-#2191 rendering, and a
  strictly safer failure than a wrong parent. This is `maxTurnEndStopField`'s judgement applied to
  a value whose token-ness makes it sharper.
- *No dropped-fields report is owed*, on `maxTurnEndStopField`'s stated rule: a dropped scalar
  reads as an absent one, and that is the intended reading here. #2233 needed its report arrays
  because three of its fields are emptied and empty had two meanings; here empty has one meaning,
  "main thread", and an over-cap id degrades into it honestly.

### No branch on nesting depth

The converter reads the value off the line verbatim. There is no depth parameter, no ancestry
walk, and no comparison against a previously seen id — so a line naming an inner Agent call
carries the inner id, and the client reassembles the tree by following ids. AC 3 is satisfied by
the *absence* of code, which is why it is proven against a synthesized nested line rather than
against a depth claude would have to be talked into producing.

### turnevent and protocol

`ToolStart` and `ToolUpdate` each gain `ParentToolCallID string` — named for `ToolCallID` beside
it, which is that package's own spelling. Every existing literal of both types is keyed, so
nothing breaks.

`ToolUsePayload` and `ToolResultPayload` each gain `ParentToolUseID string` with wire key
`parent_tool_use_id` — the wire's spelling, matching `tool_use_id` beside it, the same
name-at-the-boundary transformation `ToolCallDenied`'s arm already performs. No `omitempty`, per
that file's rule: absence and `""` mean the same thing, and always emitting the key keeps the
testdata fixtures pinning the full shape.

`MapEvent`'s two arms pass the value through verbatim and re-cap nothing — this file's standing
terms, since the producer bounded it at construction.

### Concurrency model

None. Every symbol added is a pure function of one line's bytes or a field on a value type. No
goroutine, no lock, no parser state: unlike #2224's `assistantErrorCategory`, this value is
per-line and reaches its emit inside the same call, so there is nothing to latch and nothing that
can leak from one turn into the next.

### Error handling

One reject branch: a value that is not a JSON string, or is over `maxTaskFieldID`, yields `""`.
Nothing is logged on that path — `decodeModelWindows`' rule, because `encoding/json` quotes the
offending input bytes into its error text, and this input is claude-controlled.

## Testing strategy

Hermetic, inside `make check`, unless marked otherwise:

- `parentToolUseID` table: a plain string; an absent key; `null`; a number; an object; an array; a
  string of exactly `maxTaskFieldID` bytes (carried, matching `boundStopField`'s `<=` boundary);
  one byte over (dropped).
- An `assistant` line with a `tool_use` block and a non-null `parent_tool_use_id` emits a
  `ToolStart` carrying it; the same line with `null` emits one carrying `""`.
- The same pair for a `user` line's `tool_result` block and `ToolUpdate`.
- **Independence, both directions, on the assistant line**: a line whose `error` is a hostile
  shape still carries the parent id, and a line whose `parent_tool_use_id` is a hostile shape still
  reports its error category. This is what proves the separate target earned its second pass.
- **The `userLine` no-new-failure-mode property**: a `user` line whose `parent_tool_use_id` is a
  number still suppresses its synthetic text block and still carries its `ResultDetail`. This is
  the regression the RawMessage choice exists to prevent, and a `string` field would redden it.
- **The `tool_progress` overload does not feed the field**: drive every frame from
  `capturedToolProgressLines` — the committed `tool_progress_v2.1.259.json`, whose 12 non-null
  values are the *other* meaning — through a parser and assert no `ToolStart` or `ToolUpdate` is
  emitted at all. Non-vacuous by that reader's own zero-frames fatal.
- **Nesting depth, synthesized** (AC 3): an assistant line naming an inner Agent call's id yields
  that id, not an outer one. Synthesized deliberately — a depth claude chooses is not provokable
  on demand, and a live test for it would skip while reporting success.
- `internal/protocol/testdata/tool_use.json` and `tool_result.json` gain the key with an empty
  value, pinning that a main-thread frame is unchanged apart from it (AC 4).
- `MapEvent` round-trip assertions for both arms.
- **Live** (`make e2e-realclaude`, the dispatcher's gate): `TestRealClaude_ParentToolUseCapture`
  prompts a turn that calls the Agent tool with a subagent running at least two tools, records
  every line, and writes the fixture in-repo.
- **Replay** of that fixture from `internal/streamsup`, behind a reader gate.

### The capture probe

Reuses the shared rig entire — `dropcapRecorder`, `dropcapRedactor`, `dropcapScanner`,
`dropcapMakeEntry`, `dropcapWaitForChild` — and inherits `TestRealClaude_CompactionCapture`'s
shape. What is deliberately not inherited is `tpcapHoldFIFO`: nothing here holds a call open, and
that machinery cost #2089 two repair legs.

- **The gate is the fixture's absence**, not a `PYRY_PROBE_*` variable. `make e2e-realclaude`
  never sets a custom variable, so an env gate skips on the env check *before* the credential
  check and the live gate passes vacuously — CLAUDE.md § Testing's #1763 failure exactly. A
  variable exists only to FORCE a re-capture over an existing fixture.
- **argv omits `--allowed-tools` entirely**, so the Agent tool is reachable; otherwise the
  interactive YOLO shape the sibling probes use.
- **The record is written outside the worktree**, into a `MkdirTemp` artifact directory, from a
  `t.Cleanup` registered before anything can fail. This is what let #2229's evidence survive its
  gate's worktree removal after the fixture itself did not.
- **A vacuous capture fails loudly.** Zero subagent-attributed frames, or zero tools run by the
  subagent, is `t.Fatalf` with the census — committing a capture that records none would hand the
  reader a fixture proving nothing.
- **The run `git add`s the fixture** (AC 1) and says so in its terminal log with the exact path.

### The reader gate

The fixture cannot exist until the live gate has run, which happens after verification, so a
reader asserting against bytes any earlier would redden `make check` for every unrelated ticket.
`compactionReaderGate`'s four-state machine is copied rather than imported (its package carries a
build tag this one does not): `(fixture absent, pin empty)` is the **only** legal skip;
`(present, empty)` and `(absent, filled)` both fatal; `(present, filled)` runs. Filling the pin is
the commit that lands the fixture, so the end state is reached by construction rather than by
remembering.

## Open questions

1. **Does the live turn produce a nested spawn?** Unknown and not provokable. Resolved by
   construction: AC 3's proof is the synthesized line, and a nested spawn in the capture is a
   bonus the record notes rather than something any assertion depends on.
2. **Will the dispatcher's gate lap actually land the fixture?** #2229's did not — that lap
   verifies from a detached worktree and never runs `git add`, so its fixture went out with the
   worktree and had to be recovered from the artifact directory in a follow-up (#2236). This
   ticket cannot change how the gate laps commit; it makes the probe write in-repo, print the
   exact `git add`, and leave the record outside the worktree so recovery is possible either way.
   Recorded here so the state is expected rather than discovered.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** The boundary is explicit and singular: `parentToolUseID` is the one
  function that turns claude's bytes into a Go string for this key, and it is the only site that
  applies the bound. Downstream holders — `ToolStart.ParentToolCallID`, `ToolUpdate.ParentToolCallID`,
  and both payload fields — are plain strings that no daemon code branches on, which is the
  property that keeps this a *report* rather than a capability. A concrete scenario the design
  does address: a subagent that persuades claude to emit a `parent_tool_use_id` naming an
  unrelated call's id causes a row to render under the wrong group, and nothing more — no daemon
  behaviour keys on the value, so there is no state to corrupt. Documented at both payload fields.
- **[Trust boundaries]** SHOULD FIX — the wire documentation must say the value is a **display
  and join hint, not a capability**, in the same terms § `tool_use`'s input values already use.
  Without it a client author could reasonably read a `tool_use_id`-shaped field as something to
  look up or dereference. Phase B writes it into both `docs/protocol-mobile.md` rows and both
  payload docblocks.
- **[Tokens, secrets, credentials]** Not applicable, and the reason is worth stating rather than
  waving through: the value is an identifier claude mints for its own bookkeeping, carries no
  entropy the daemon relies on, authenticates nothing, and grants nothing. It is not compared
  against any secret, so `crypto/subtle` has no role. The probe's `parentSessionID` is a fixed
  literal in a per-test temp `$HOME`, matching every sibling probe — not a secret, and distinct
  from theirs so records cannot be confused.
- **[File operations]** The only file this ticket writes is the capture fixture, and only from the
  live probe. It inherits `ccapWriteRecord`'s shape: a path composed from package constants and a
  `MkdirTemp` directory, never from claude's output, so no claude-controlled byte reaches a path.
  The `os.Stat` on the fixture path in the probe's gate is check-then-use, and the gap is not
  exploitable: both sides are the operator's own repo, the consequence of losing the race is at
  worst one re-capture, and the alternative — no gate — is the vacuous-run failure this ticket
  exists to avoid.
- **[Subprocess / external command execution]** No value from this ticket reaches an `exec.Command`
  argument. The probe's argv is a fixed slice of literals; the prompt text is a constant and is
  written to the child's *stdin*, not to argv. `--allowed-tools` is omitted so the Agent tool is
  reachable, which widens what the subagent may run — bounded by `--dangerously-skip-permissions`
  in a fresh empty per-test workdir under a temp `$HOME`, the same posture the sibling capture
  probes already run under, and the prompt asks only for tools that read what the probe itself
  wrote.
- **[Cryptographic primitives]** Not applicable — no randomness, no comparison against a secret,
  no key material. The probe's nonce is `time.Now().UnixNano()`, matching `ccapPrimePrompt`, and is
  a cache-buster rather than a security value.
- **[Network & I/O]** The finding this category exists to catch, and the reason the bound is not
  optional. `tool_result` is never-droppable control class (`4413`); a frame over the 65519-byte
  application-envelope cap is lost rather than truncated, and sustained oversize frames reach the
  per-session push-queue byte ceiling that tears the session down. An unbounded pass-through would
  put `defaultMaxParseBuf`'s whole 4 MiB on that frame — a claude-authored availability lever on a
  frame the client cannot afford to lose. `maxTaskFieldID`'s 256-byte drop closes it, and the
  input is already bounded by the 4 MiB whole-line cap before the decoder sees it, so the check is
  O(1) against a bounded input. No socket read, no header, no timeout, and no connection accounting
  is added by this ticket.
- **[Error messages, logs, telemetry]** Nothing is logged on any path this ticket adds — not the
  value, not the decode error. That is deliberate on `decodeModelWindows`' stated rule:
  `encoding/json` quotes the offending input bytes into its error text, so logging the error would
  write claude-controlled content into the daemon's logs. The over-cap drop is likewise silent.
  The probe's own failure messages carry counts, censuses and frame indices only, never payload
  bytes — the rule `TestRealClaude_CompactionCapture`'s AC 5 block states — and the record is
  redacted by `dropcapRedactor` and deny-scanned by `dropcapScanner` before it is written.
- **[Concurrency]** Not applicable, and structurally so: every symbol added is a pure function of
  one line's bytes or a field on a value type. No lock is taken, no shared state is read or
  written, and no goroutine is spawned. In particular this value is deliberately *not* latched on
  the `Parser` the way #2224's error category is — it is consumed inside the same `consumeLine`
  call, so there is no cross-line residual that a turn boundary would have to reset.
- **[Threat model alignment]** § Security model's relevant threat is a hostile or compromised
  claude subprocess emitting content that reaches an internet-exposed frame. The design answers it
  three ways: the value is bounded and dropped rather than cut, nothing in the daemon branches on
  it, and the documentation tells the client it is inert. The threat this ticket explicitly does
  NOT address is a subagent's *text* reaching the wire mis-attributed — that arrives only with
  `--forward-subagent-text`, `flushDelta` in `cmd/pyry/interactive_turn_v2.go` buffers on message
  id alone and would concatenate it into the parent's bubble, and **#2192 owns it** and already
  carries the criterion ("its own `turn_id`/`seq` lane, never concatenated into the parent's
  bubble"). Publishing this field on `assistant_delta` today would hide that bug rather than
  expose it, which is why the ticket moved it out.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-08
