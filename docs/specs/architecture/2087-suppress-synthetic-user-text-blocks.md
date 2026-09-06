# 2087 — Suppress harness-synthetic user/text blocks (skill invocations)

## Files read

- `internal/streamsup/parser.go` → `emitUser` — the drop site. Already takes the raw line bytes (#2024), which is why the line-level flag and the message's blocks can meet here without widening `streamLine`.
- `internal/streamsup/parser.go` → `userToolResultLine` — the in-file precedent for "a top-level field belonging to one line type, decoded out of the line a second time". Its docblock carries the #2023 snake-case finding, which the change must preserve and now extend.
- `internal/streamsup/parser.go` → `harnessNoOutputNudge` — the existing block-level suppression whose rationale this ticket generalises ("the same would go for any future harness-injected prose"). Its byte-exact-match argument is what the new arm must NOT weaken.
- `internal/streamsup/parser.go` → `ignoredLineTypes` — the 2026-07-27 census docblock. The `CORRECTED`/`AMENDED` house style for recording a new measurement without editing an old row lives here, and AC5 is an entry in it.
- `internal/streamsup/parser.go` → `streamLine`, `countToolResultBlocks`, `toolResultDetail` — the surrounding decode/segmentation contract the change must leave alone.
- `internal/streamsup/capture_test.go` → `capturedLine`, `capturedLines`, `capturePath` — the committed-capture reader, no build tag, inside `make check`. `capturedLine(t, "user", "")` is AC2's source of bytes; the exactly-one rule and the `is_capture` provenance check both live below it.
- `internal/e2e/realclaude/testdata/dropped_lines_v2.1.220.json` → the one `user` record — claude's verbatim stdout for the nudge line at claude 2.1.220, carrying top-level `"isSynthetic": true` and no `isMeta` key.
- `internal/streamsup/parser_test.go` → `harnessNudgeFixture`, `harnessNudgeDropMsg`, `collectEvents`, `logRecorder`, `TestParser_HarnessNudgeDropIsLoggedContentFree` — the fixtures-as-literals rule (never build a fixture from the constant it validates) and the content-free logging assertion this ticket copies for its own arm.
- `internal/e2e/realclaude/interactive_stream_unrecognized_test.go` → `TestInteractiveStreamNoUnrecognizedOnToolTurn` — the live sibling's spine: pair → seed → spawn → dial → handshake → send → drain.
- `internal/e2e/realclaude/interactive_stream_liveness_test.go` → `drainForCompletedTurn`, `writeStreamInteractiveConfig` — the shared drain that carries the zero-`unrecognized_message` alarm, and the config write that selects the stream-json interactive runner.
- `internal/e2e/realclaude/interactive_stream_model_announced_test.go` → `spawnBootstrapDaemonVerbose`, `announcedSpawnAlias` — `spawnBootstrapDaemon` plus `-pyry-verbose` so the daemon logs at Debug into the captured stderr buffer. `announcedSpawnAlias` is `"haiku"`, identical to the shared spawner's model, so this is reusable verbatim rather than needing a fourth near-copy.
- `internal/e2e/realclaude/harness_daemon_test.go` → `spawnBootstrapDaemon`, `bootstrapDaemon`, `lockedBuffer` — the spawn argv (`--model haiku --dangerously-skip-permissions`) and the stderr buffer the drop-record assertion reads.
- `docs/knowledge/features/streamsup-package-send-half-writeturn.md` § the 2026-07-30 (#1247) and 2026-08-02 (#1260) amendments — the package overview's home for this doctrine. Two things it settles that the parser's own docblocks do not: the block-level suppression is a **tier below `ignoredLineTypes`**, not an entry on it, and #1260's "second confirmed payload" means a second *occurrence of the same string*, which is a different claim from this ticket's second *kind* of payload. The two amendments would read as contradicting each other without that distinction spelled out, so the new docblock entry spells it out.
- `CODING-STYLE.md`, `CLAUDE.md` § Testing — the live-suite rule (read the executed-test count, never the exit code) and the stdlib/table-driven test conventions.

## Context

Invoking a skill puts an "Unrecognized message" row carrying the entire skill body — 87 KB in one observed case, truncated at the daemon's payload cap — into the operator's chat. The cause is that claude's harness injects the skill's instructions on the output stream as a `user` line holding a `text` block, and `emitUser` surfaces every user block that is neither a `tool_result` nor the one known harness nudge.

The 2026-07-27 census that seeded that rule is not wrong; it measured turns that called tools and never invoked a skill. `harnessNoOutputNudge`'s docblock already predicted this category in as many words: a harness-authored user/text block is neither the person's message nor the model's reply, "and the same would go for any future harness-injected prose."

The decision taken on the ticket is **suppress, not map**. A marker row naming the loaded skill is a wire change across two clients and is deliberately out of scope.

No ADR is warranted: this extends an existing, already-documented suppression tier rather than establishing a new one.

### The premise this design rests on, and how far it is measured

The matcher keys on the line-level synthetic flag rather than on the block's body, because an 87 KB body that varies per skill can never be matched without also swallowing a genuinely new user/text block.

What is **pinned by committed bytes**: the *nudge* line on claude's **stdout** surface carries top-level `"isSynthetic": true` and no `isMeta` key (`dropped_lines_v2.1.220.json`, the one `user` record; the 2.1.239 sidecar capture agrees).

What is **inferred**: that a *skill* line on that same stdout surface carries the same flag. The ticket asks for a live run to settle it. **No claude credentials are present in this build environment** (`ANTHROPIC_API_KEY` and `CLAUDE_CODE_OAUTH_TOKEN` both unset), so the live test is written but cannot be executed here; the dispatcher's `needs-real-claude` gate is what runs it.

In place of the live read, the inference was tightened against the operator's local transcript corpus on 2026-09-06 (measured, not assumed):

| Surface | Nudge line | Skill line |
|---|---|---|
| stdout (`--output-format stream-json`) | `isSynthetic: true` — committed capture | *the inference* |
| JSONL transcript (`~/.claude/projects`) | `isMeta: true`, 1 of 1 | `isMeta: true`, 249 of 249 |

So `isMeta` (transcript) ↔ `isSynthetic` (stdout) is established for the nudge, and skill lines are in the same transcript class as the nudge — 249 of 249, zero truthy `isSynthetic` anywhere in the corpus. The ticket's inference is a two-step; this makes both steps measured except the final surface hop.

The failure direction if the hop is wrong is the safe one and costs nothing: the flag arm never fires, and the Unrecognized row for skills stays exactly as it is today. The fallback the ticket names (match the harness-fixed prefix `Base directory for this skill: `) is **not** implemented here — shipping a second matcher against an unfalsified premise is a defence for a failure mode that has not been observed. If the live gate shows the flag absent, the fallback is a follow-up with a measurement behind it.

## Design

One production file: `internal/streamsup/parser.go`.

### 1. The decode target

`userToolResultLine` is renamed **`userLine`** and gains one field:

```go
type userLine struct {
    ToolUseResult json.RawMessage `json:"tool_use_result"`
    IsSynthetic   bool            `json:"isSynthetic"`
}
```

Both fields are top-level siblings of `message` on a `user` line, so one struct is the honest shape and the name follows. `emitUser` already unmarshals the line into this struct, so folding the flag in costs **zero** additional passes over the line — a second `json.Unmarshal` would re-scan a payload that carries whole file contents on every `Read` tool result.

`streamLine` is untouched: it stays the line-level *segmentation* struct at Type/Subtype/Message, for the reason its siblings' docblocks already state.

The rename touches three identifier sites (declaration, one cross-reference in `toolResultDetail`'s neighbourhood, one use in `emitUser`) and no exported surface.

**The docblock gains the casing finding, which is the reason the fold earns its keep.** The #2023 finding — that this envelope spells the sidecar `tool_use_result` where the transcript spells it `toolUseResult` — is preserved verbatim and moves to the field it describes. What is new: the envelope is **mixed**, not uniformly snake_case. The captured line reads `parent_tool_use_id`, `session_id` … and `isSynthetic`. A reader who generalises #2023's finding into "this surface renames keys to snake_case" writes `is_synthetic` and gets dead code — the same failure #2023 bought at the cost of a wasted live run, arriving from the opposite direction. `isMeta` is the other dead-code spelling and is named too.

### 2. The suppression

In `emitUser`'s block loop, the existing nudge guard widens to two independent triggers behind one drop site:

```go
if block.Type == "text" && (ul.IsSynthetic || block.Text == harnessNoOutputNudge) {
    // content-free Debug log: site and type only
    continue
}
```

Four properties, each load-bearing:

- **`block.Type == "text"` gates both arms.** A block of an unknown type still reaches the unrecognized lane whether the line is flagged or not — a genuinely new block shape is the alarm this feature exists to raise, and the flag must not blanket it. It also keeps the guard unreachable from tool output: a `tool_result`'s payload decodes into `Content`, never into `Text`.
- **`continue`, not `return`.** The suppression is scoped to the block, so a `tool_result` sharing a flagged line still maps to `ToolUpdate` with its sidecar detail intact.
- **The two triggers are `||`-independent.** `harnessNoOutputNudge` is not subsumed. A claude version that stops stamping the flag must not resurrect the nudge row, and byte-exact equality remains the tolerance one observation earns.
- **The log stays site + type.** No third attribute, no reason discriminator: the drop site would otherwise be one careless edit away from carrying an 87 KB skill body into the daemon's logs, and that is precisely the payload this ticket exists to keep out of sight.

The Debug message string is unchanged (`streamsup: dropping known harness user block`) — it is accurate for both arms, and the existing content-free test pins it as a literal.

### 3. The census entry (AC5)

A dated `AMENDED 2026-09-06 (#2087)` paragraph is appended to `harnessNoOutputNudge`'s docblock, with a pointer from `ignoredLineTypes`' existing 2026-07-30 amendment. It records: the trigger (skill invocation), the two observations (18681 and 87244 chars, 2026-09-04, Opus 5), what the 2026-07-27 census measured and therefore could not have caught, the transcript-corpus measurement above with its date and counts, and the surface hop that remains an inference. The 2026-07-27 and 2026-07-30 rows are left byte-unchanged.

## Concurrency model

None. `emitUser` runs on the single `os/exec` stdout-forwarder goroutine that owns the `Parser`, the change adds no state, no goroutine, and no lock. The parser stays turn-stateless: the flag is read off the line being processed and retained nowhere.

## Error handling

- The `json.Unmarshal` of the line into `userLine` keeps its existing dropped error and its existing rationale (the line already decoded once in `consumeLine`, and a `json.RawMessage` target cannot fail). Adding a `bool` field does not add a failure mode: a non-boolean `isSynthetic` fails the whole unmarshal, `ul` stays zero, `IsSynthetic` is false, and the block reaches the unrecognized lane — the safe direction.
- An absent key decodes to `false`, which is the wanted default: unflagged means surface.
- No new error is returned or wrapped; no `panic`; no path can drop a `tool_result`.

## Testing strategy

### Hermetic, inside `make check` — `internal/streamsup/parser_test.go`

Fixtures are string **literals**, never built from the production constant, per `harnessNudgeFixture`'s standing rule.

1. **Table: the flag arm's behaviour matrix** (AC1). Synthesised lines through `collectEvents`:
   - flagged + non-nudge `text` → zero events
   - flagged + `tool_result` → one `ToolUpdate`, sidecar detail intact
   - flagged + unknown block type → `Unrecognized`
   - flagged + `text` **and** `tool_result` on one line → the text drops, the `ToolUpdate` survives
   - `"isSynthetic": false` present + non-nudge `text` → `Unrecognized` (the field is read by VALUE, not by presence)
   - flag absent + non-nudge `text` → `Unrecognized`
   - flag absent + byte-exact nudge → zero events (AC3, the independent guard)
2. **Captured-bytes pin** (AC2), driven from `capturedLine(t, "user", "")`:
   - the captured bytes contain `"isSynthetic":true` — the wire-spelling assertion, whose failure message names `isMeta` and `is_synthetic` as the two dead-code spellings
   - the captured line as-is → zero events
   - the captured line with its nudge text replaced by a distinctive non-nudge probe → **still** zero events. This is the assertion that proves the *flag* arm fired rather than the nudge constant; the replacement is asserted to have actually occurred so the case cannot go vacuous.
3. **Content-free logging for the flag arm** (AC1). A flagged line whose text is a distinctive probe token: exactly one drop record, attrs exactly `{site, type}`, and no emitted record — message or attr — contains the probe token.
4. `TestParser_HarnessNudgeDropIsLoggedContentFree` and `TestParser_IgnoredLineTypesIsTheMeasuredSet` stay green, unedited.

### Live, behind `e2e_realclaude` — new `internal/e2e/realclaude/interactive_stream_skill_test.go`

Spine copied from `TestInteractiveStreamNoUnrecognizedOnToolTurn`, with three deltas:

- A skill is written into the test's **own temp HOME** (`WithWorktreeAuthenticated`'s tempdir) as `.claude/skills/<name>/SKILL.md`, so the body is test-authored and nothing is lifted from the operator's machine.
- `spawnBootstrapDaemonVerbose` replaces `spawnBootstrapDaemon` — its only delta is `-pyry-verbose`, and its model alias is already `"haiku"`, so it is reusable verbatim.
- The turn is driven by a prompt naming the skill, then drained with the shared `drainForCompletedTurn`, whose zero-`unrecognized_message` arm is the assertion AC4 asks for.

**Non-vacuity is proved from the daemon's own Debug log, not from the reply text.** The test asserts the captured stderr carries the drop record with `site=user_block type=text` — which is only reachable if a flagged user/text line actually arrived at `emitUser` and the new arm fired. A run in which claude never invoked the skill produces no such record and fails. A token echoed in the reply would have been weaker: claude could obtain it by `Read`ing the skill file, which delivers the body as a `tool_result` and exercises none of this.

The same haystack carries a second assertion, free: the daemon's Debug output must **not** contain the skill body's distinctive marker — the content-free rule proved on the live surface rather than only on a synthesised line.

## Open questions

1. **Does a skill line on the stdout surface carry `isSynthetic`?** Unresolvable in this environment (no credentials). Resolved by the `needs-real-claude` gate. Recorded above with the measurement that narrows it and the safe failure direction. If it comes back absent, the prefix fallback is a follow-up ticket, and the docblock entry gets the failed inference appended — this plan's § Context states that contract in advance so the outcome cannot be quietly re-scoped.
2. **Will haiku reliably invoke a skill on request?** If the live test proves flaky on model compliance rather than on the behaviour under test, the prompt is what tightens, not the assertion. Any change lands as a `## Revisions` entry.
3. **Neighbours #2088 and #2089** edit the same `consumeLine` switch and the same census docblock. No in-flight branch touches these files as of the § A2 scan; whichever lands second rebases. Recorded so the conflict is expected rather than surprising.

## Security review

**Verdict:** PASS

The category that matters here is the first one, and it is not a formality: this change makes the daemon **silently discard** content, and the discriminator it keys on is written by the untrusted side of the boundary. That is the shape of an attacker-controlled suppression primitive, so it was walked as one.

**Findings:**

- **[Trust boundaries] — the load-bearing one. No MUST FIX, and the reason is `block.Type == "text"`, not the flag.** The boundary is `claude`'s stdout crossing into daemon state at `Parser.Write` → `consumeLine` → `emitUser`. Three actors can reach it, and only one is interesting:
  - A **compromised `claude` binary** can stamp any line however it likes — but it is *below* this boundary and already controls the whole stream, so it can hide prose today by simply not emitting it. Not a new capability. Out of scope, and out of scope before this ticket too.
  - **Prompt injection** (protocol-mobile.md § Security model, threat 1: `severity: high`, `mitigation: partial`) makes the *model* emit text. Model speech arrives on `assistant` lines, which this arm never touches — `emitAssistant` is unchanged and its text still maps to `TextChunk`.
  - **Hostile tool output** — a fetched page, a repository file, a command's stdout — is the actor that could plausibly try to smuggle text into the suppressed lane. It cannot: tool output arrives as a `tool_result` block, whose payload decodes into `Content` and never into `Text`, and the arm is gated on `block.Type == "text"`. **That gate is the security boundary of this change, not merely a tidiness choice**, and the plan's hermetic table pins it from both sides (flagged + `tool_result` → `ToolUpdate` survives; flagged + unknown type → still `Unrecognized`).

  Downstream callers are unaffected: suppression emits nothing, so no consumer receives a value it must now treat differently.

- **[Trust boundaries, second question] Does suppressing hide something the operator needs for safety?** Named rather than waved past. Approval and permission flows on this surface do not travel as user/text blocks — they are `control_request` / MCP-approval traffic with their own arms in `consumeLine`, untouched here. What a flagged text block carries is harness self-talk, which is precisely what `harnessNoOutputNudge`'s docblock argues does not belong in the person's message history. A genuinely new *block type* on a flagged line still alarms, so the blind spot is bounded to `text` on a flagged `user` line.

- **[Tokens, secrets, credentials] No secrets handled — and the change REMOVES a disclosure path.** Today a skill body reaches the paired phone verbatim inside `unrecognized_message.Raw` (capped by `maxUnrecognizedRaw`, but 16 KiB of it still goes over the wire) and is available to any client rendering it. Skill bodies are operator-authored instruction files that can carry API details and internal procedure. After this change that content leaves the process nowhere: not on the wire, not in a log. Data minimisation, not just a UI fix.

- **[Errors, logs, telemetry] No finding; three tests hold the line.** The drop site logs `site` and `type` only — no `reason` discriminator was added precisely because the drop path is one careless attribute away from writing an 87 KB skill body into daemon stderr and journald. Asserted hermetically for both arms (attrs pinned by `reflect.DeepEqual`, plus a sweep proving no record carries the probe token) and once more against the live daemon's Debug stderr in the real-claude test.

- **[Network & I/O] No finding — the suppression retains ZERO bytes.** `IsSynthetic` is a `bool`; nothing of claude's payload is copied, sliced, or held. The slicing-retention trap (a capped substring of a large decode pinning the whole allocation) cannot apply to a field that stores no bytes. Existing bounds are untouched and unweakened: `defaultMaxParseBuf` still caps the partial-line accumulator, `maxUnrecognizedRaw` still caps what a surfaced `Unrecognized` carries. Net wire pressure falls.

- **[File operations] SHOULD FIX — the live test writes a file; the plan must say what mode.** The real-claude test writes `SKILL.md` into its temp HOME. Create the directory `0o700` and the file `0o600`, matching `WithWorktreeAuthenticated`'s existing `0o600` for the seeded `.claude.json`. Paths are composed from `t.TempDir()`-derived roots and compile-time constants only — no external input reaches a path component, so traversal, TOCTOU and symlink handling do not arise. The verifier should check the modes landed.

- **[Subprocess / external command execution] SHOULD FIX — the test-authored skill body must be inert.** No production argv changes. The live test reuses `spawnBootstrapDaemonVerbose` unmodified, whose `--dangerously-skip-permissions` is pre-existing across every test in this package and confined to an isolated temp HOME. But this test writes a file that `claude` will *execute as instructions*, so the body must contain no tool directives at all — no writes, no shell, no network. This is also why non-vacuity is proved from the daemon's Debug record rather than from a token the skill tells claude to write somewhere: the assertion that needed a side effect is the one that would have needed a permissive skill.

- **[Cryptographic primitives] Not applicable, with the reason.** No randomness and no comparison against a secret. The byte-exact `harnessNoOutputNudge` comparison is a match against a public, committed constant — timing reveals nothing an attacker does not already have — so `crypto/subtle.ConstantTimeCompare` is not indicated, and using it would misleadingly imply the constant is sensitive.

- **[Concurrency] Not applicable, with the reason.** `emitUser` runs on the single `os/exec` stdout-forwarder goroutine that owns the `Parser`. The change adds no field to `Parser`, no goroutine, no lock, and no cross-call state: the flag is read off the line in hand and retained nowhere, so there is no shared state to order locks around, no check-then-mutate, and nothing to recover after a mid-write signal.

- **[Threat model alignment] Addressed above under trust boundaries.** protocol-mobile.md § Security model threat 1 (prompt injection) is the only relevant entry and its posture is unchanged — the arm cannot be reached by injected content. Threat 5 (implementation bugs) is met the usual way: the whole behaviour matrix is pinned hermetically inside `make check`, and the wire spelling is pinned against committed capture bytes rather than a hand-written line, which is the specific bug class (a decoder keyed on the wrong spelling, silently never firing) that #2023 paid for once already.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-06

## Revisions

### 2026-09-06 — during implementation

Two departures from the plan as committed, neither changing the design:

1. **The live test's witness checks the drop record's MESSAGE, not its attributes.** § Testing strategy said the test asserts the daemon's stderr "carries the drop record with `site=user_block type=text`". As written it asserts only that the record's message string is present. Reason: matching `site=user_block` would couple the test to the daemon's slog handler encoding (a JSON handler renders `"site":"user_block"`), and the message string already identifies the drop site uniquely — nothing else in the daemon emits it — so the attributes add nothing to the witness while adding a way for it to break for an unrelated reason. The sibling live test that reads this same buffer (`TestInteractiveStream_ModelAnnounced`'s AC-4 assertion) searches a bare value for the same reason. The attribute pin is not lost: `TestParser_SyntheticDropIsLoggedContentFree` asserts the exact attribute map hermetically, where the handler is the test's own.

2. **Two decoy-spelling rows were added to the hermetic table.** `"isMeta":true` and `"is_synthetic":true` must each still surface the text block. Not in the plan; added because both spellings are one plausible generalisation away from the right one (the transcript's name, and over-applying #2023's snake_case finding to a mixed-case envelope), and a decoder silently keyed on either is the exact failure mode #2023 paid a live run to discover. Additive test coverage of behaviour the plan already specifies, not a design change.

**Open question 1 (does a skill line carry the flag on the stdout surface?) remains open** and is unchanged from the plan: it is unresolvable without credentials, which this environment does not have. The contract stated in § Context — ship the flag arm alone, record the failed inference and follow up with the prefix fallback if the live gate disproves it — is what the parser's docblock now carries, so the outcome cannot be quietly re-scoped later.

### 2026-09-06 — rework leg 2 (verifier findings on PR #2132)

Two SHOULD FIX and one NIT were carried unaddressed through the previous rework dispatch, which pushed no commits. All three are addressed here. No production behaviour changed; the only production edit is a comment.

1. **The live test's non-vacuity witness was weaker than its docblock claimed, and the docblock was the more expensive half.** The witness was the daemon's Debug drop record alone, which fires on *either* trigger, so a turn in which claude ignored the skill but drew a harness nudge would have read as a pass — the exact reading AC4 forbids. Fixed by asserting **both** legs, and the docblock now states what each one does and does not prove.

   The verifier offered a second route — a content-free `trigger` attribute at the drop site naming which arm fired. **That route is unavailable on the merits, and finding out why is the substantive result of this leg.** The committed capture's one `user` record *is* the nudge, and it carries `"isSynthetic": true`. So on today's claude a nudge trips the **flag** arm and the string comparison beside it is never reached; a `trigger` attribute would report `flag` for a nudge and for a skill body alike and could not discriminate. This also means `harnessNoOutputNudge`'s arm is currently unreachable on the live surface — kept deliberately, since AC3's case is a claude that *stops* stamping the flag, and pinned by a hermetic row rather than by any live run. Both facts are now in the parser's docblock, where the next reader will otherwise get them backwards.

   The witness is therefore a pair, and neither leg suffices alone: `skillReplyToken` in the reply proves claude read the skill body (the prompt withholds both the token and the path); the drop record proves a harness-authored user/text block arrived and was suppressed; and the drain's zero-`unrecognized_message` pass is the third leg — had the skill's own line not been suppressed, its body would have surfaced there. Forging the set would need a no-visible-output response to summon a nudge *and* a visible reply carrying the token, which are contradictory.

2. **`emitUser`'s surviving `block.Type != "tool_result"` comment still described the pre-#2087 guard** ("every one but the harness nudge caught above"). Rewritten to state what reaches that arm now: every block of an unknown type, flagged line or not, plus every user/text block on an unflagged line that is not byte-exactly the nudge.

3. **NIT:** the seeded-identifier comment claimed the values were distinct from every sibling's. Only the Go names are; the UUIDs are deliberately shared with `streamBootstrapUUID`/`streamConvID`, which a third test already reuses. The comment now says so, and why it is harmless (each test seeds its own temp HOME).

**Departure from the plan:** `drainForCompletedTurn` now returns the driving conversation's concatenated reply text, and accumulates every delta rather than decoding only the first. The drain asserts nothing new — a content assertion there would be imposed on all nine callers, and real claude's words are non-deterministic — it just makes the text available to a caller that needs one. Returning a value rather than taking a sink keeps all nine existing call sites compiling unchanged, since a Go call used as a statement may discard results.

### 2026-09-06 — Open question 1: strongly evidenced, not yet closed

The 2026-09-05 23:11 real-claude gate ran `TestInteractiveStreamSkillInvocationIsSilent` against claude 2.1.259 on `--model haiku` and it **passed**: 911 tests executed, and the one failure in that run is `TestInitControlArms_…`, which is #2131's version-collision and not this branch's (see the verifier's second review for the deterministic attribution). The turn produced zero `unrecognized_message` frames, exactly one drop record, and a 5-byte assistant delta — the length of the reply the skill body instructed at the time.

So the arm fires on a live skill turn, and before this change the same turn surfaced the body. What that run could **not** say is which trigger fired, for the reason in item 1 above. The strengthened witness is what makes the next green run read on the skill line specifically. The parser docblock records it at exactly this strength; the contract for the prefix fallback is unchanged and still stated there, so a disproof later cannot be quietly re-scoped.

**This ticket cannot pass its live gate until #2131 lands** — that fixture collision reddens the gate for every ticket carrying `needs-real-claude`, and it bounced this one twice on a failure no builder can fix from here. #2087 is now marked `blockedBy` #2131.
