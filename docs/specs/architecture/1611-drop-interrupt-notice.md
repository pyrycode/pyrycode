# #1611 — drop claude's `[Request interrupted by user` notice in `emitUser`

## Files read

- `internal/streamsup/parser.go` → `emitUser` — the drop site. Its existing guard is
  `block.Type == "text" && (ul.IsSynthetic || block.Text == harnessNoOutputNudge)`,
  followed by the content-free `p.log.Debug` and a `continue`; the `block.Type != "tool_result"`
  arm below it is the unrecognized lane this ticket is removing one specimen from.
- `internal/streamsup/parser.go` → `harnessNoOutputNudge` — the census docblock AC5 amends, and
  the precedent for how a suppression states its provenance, its match tolerance and its failure
  direction. Its `AMENDED 2026-09-06 (#2087)` entry is the format to copy.
- `internal/streamsup/parser.go` → `ignoredLineTypes` — the other census docblock; confirms the
  suppression tiers are *top-level types* there and *block level* at `harnessNoOutputNudge`. This
  ticket adds to the block-level tier, so the amendment goes on `harnessNoOutputNudge`.
- `internal/streamsup/parser.go` → `dropHarnessProseLine` — the third (line-level) tier, matched on
  `isSynthetic`/`isReplay` for string-content lines. Confirms the interrupt notice cannot be handled
  there: its content is a proper block array, so it decodes and reaches `emitUser`.
- `internal/streamsup/parser.go` → `userLine` — the top-level sidecar/flag decode; nothing new is
  read off the line for this change.
- `internal/streamsup/parser_test.go` → `syntheticUserLine`, `textBlock`, `toolResultBlock`,
  `collectEvents`, `harnessNudgeFixture`, `harnessNudgeDropMsg`, `logRecorder.withMessage` — the
  fixtures the new rows build on. `harnessNudgeFixture`'s doc states the rule the new fixtures must
  follow: a fixture is a **literal**, never built from the production constant it validates.
- `internal/streamsup/parser_test.go` → `TestParser_SyntheticUserLineDropsTextBlocks`,
  `TestParser_HarnessNudgeDropIsLoggedContentFree` — the two tests that independently prove the
  flag arm and the nudge constant (AC2's "unchanged and still independently proven"). Neither is
  edited.
- `internal/e2e/realclaude/interactive_stream_interrupt_test.go` → `interruptNoticePrefix`,
  `drainForCancelledTurnEnd` — the carve-out AC3 deletes. `strings` is imported for exactly one
  expression inside the carve-out arm, so the import goes with it.
- `internal/e2e/internal/fakeclaude/main.go` → `interruptMarkerLine` — the fake's own copy of the
  short wording. Checked and **not affected**: `appendTurnEnd` writes it to the *transcript file*
  for turnbridge's `mapEntry`, never to stdout, so no hermetic e2e observes this notice on the
  stream-json surface this ticket changes.
- `docs/knowledge/codebase/1500.md`, `docs/knowledge/codebase/1243.md` — frozen history. #1500
  recorded the carve-out with this ticket as its re-enable step; #1243 recorded the same prefix
  matched in the now-deleted tui-driver mapper, whose `strings.TrimSpace` does not carry over
  (it concatenated a whole entry's text blocks; this match is per block).

## Context

Interrupting a real claude turn makes the harness inject a `user` message holding one `text` block
carrying its own cancellation notice. `emitUser` drops user/text blocks on exactly two triggers —
the byte-exact `harnessNoOutputNudge` (#1247) and the line-level `isSynthetic` flag (#2087) — and
the interrupt notice trips neither, so it falls to `emitUnrecognized(turnevent.UnrecognizedUserBlock, …)`
and the desktop draws an "Unrecognized message" row on every interrupt. Re-observed by the operator
on 2026-09-14.

The ticket's own re-measurement at `0049b0a1` (6684 local transcript `.jsonl` files) found 31 user/text
interrupt notices, 26 short-wording and 5 tool-use-wording, **31 of 31 carrying no `isMeta`, `isSynthetic`,
`isReplay` or `isCompactSummary` key at all**. So the #2087 flag arm structurally cannot cover this, and
the match must be on the text.

It must be a **prefix**, not a literal: two wordings were measured on the same claude version
(2.1.220) on the same day, differing only in a trailing clause naming what claude happened to
interrupt. An exact pin on either goes red on the other.

This is a suppression rather than a mapping, on the same criterion as #1247 and #2087: the author is
neither the person nor the model, the notice carries nothing a client reader can act on, and the
client already receives the cancelled `turn_end` for the same event.

**No ADR is warranted.** This adds a third trigger to an existing, documented suppression tier; it
does not move a boundary. The durable record is the amended census docblock plus the documentation
stage's fold into the streamsup package overview (see § Documentation handoff).

## Design

### The guard — a third OR'd trigger, not a table

`emitUser`'s existing guard gains one disjunct:

```go
if block.Type == "text" && (ul.IsSynthetic ||
    block.Text == harnessNoOutputNudge ||
    strings.HasPrefix(block.Text, harnessInterruptNoticePrefix)) {
```

The Technical Notes leave open whether the two string triggers should become a measured table of
harness prefixes. **They should not**, and the reason is a property of the two matchers rather than
of their count: they have *different match shapes*. `harnessNoOutputNudge` is byte-exact, and its
docblock earns that tolerance explicitly ("no trim, no fold, no prefix, no substring — because that
is the tolerance one observation earns"). A uniform table of prefixes would silently widen the nudge
from equality to prefix, i.e. would make `nudge + anything` droppable — a regression in the one
property #1247 argued for at length. A table with a per-entry match *mode* is strictly more machinery
than two disjuncts for two entries. Simplicity first: extend the OR, and record the decision in the
constant's docblock so the next reader does not re-open it.

Everything the existing arm already does is preserved unchanged and is load-bearing here too:

- `block.Type == "text"` — keeps the trigger unreachable from tool output (a `tool_result`'s payload
  decodes into `Content`, never `Text`), and keeps a genuinely new *block type* surfacing.
- `continue`, not `return` — the suppression is scoped to this block, so a `tool_result` sharing the
  message still maps with its sidecar detail intact (AC1).
- one content-free `p.log.Debug` carrying `site` and `type` only, with **no third attribute naming
  which trigger fired** — the existing docblock rules that out on the merits, and it is doubly right
  here because the drop path must never be one careless attr away from logging block content.

### The constant

`harnessInterruptNoticePrefix = "[Request interrupted by user"` — placed immediately after
`harnessNoOutputNudge`, with its own docblock stating: the two measured wordings and their runs, the
31-of-31 flag-absence count, why the match is a prefix rather than a literal, why it is **not** on the
flag, why there is no trim/fold/substring, and why the table was rejected. Name carries the match
shape (`…Prefix`) so a future reader cannot mistake it for an exact pin like its neighbour.

Match shape, restated because each half was decided against an alternative:

| choice | rejected alternative | why |
|---|---|---|
| `strings.HasPrefix` | `strings.Contains` | a substring match would drop any block *mentioning* the notice; also O(len(prefix)) vs O(len(block)) — matters when a sibling trigger's observed payload ran to 87244 chars |
| no `TrimSpace` | #1243's `TrimSpace` | that helper concatenated a transcript entry's blocks into one string, making leading whitespace reachable. Per-block here: all 31 corpus occurrences are the bare bracketed string with nothing before it |
| no `HasSuffix("]")` anchor | anchoring both ends | both measured wordings end `]`, but nothing measures that as stable; an anchor buys nothing against an observed failure and goes red on a trailing period or space |

### Census amendment (AC5)

A new `AMENDED 2026-09-14 (#1611)` entry at the end of `harnessNoOutputNudge`'s docblock, in the
existing style, naming: date, model, claude version, both wordings, the 31-of-31 flag-absence count,
and the table of other harness user-text kinds from the ticket's Context (nudge, skill body,
compaction summary, slash-command stdout echo, interrupt notice, `<task-notification>`, `[Image: …]`,
`<command-name>` echo) with how each is handled today. Earlier entries are left **unedited** — they
are dated measurements, accurate as records of what shipped.

## Concurrency model

No change. `emitUser` is called synchronously from `consumeLine` on the single `Parser.Write`
goroutine; the new disjunct reads only the already-decoded `block.Text`, adds no state, no lock and
no goroutine.

## Error handling

No new failure mode. The added expression is a total function on a `string` — it cannot error, cannot
panic, and cannot be reached with a nil receiver (`decodeBlock` has already succeeded). The existing
`_ = json.Unmarshal(line, &ul)` error-drop is untouched.

The behavioural failure direction, stated so the verifier can check it: a notice **reworded past the
prefix** falls back to the unrecognized lane and a human sees the row — the same safe direction
`harnessNoOutputNudge` chose. The unsafe direction (a genuinely new payload swallowed in silence) is
bounded by the prefix being 28 bytes of bracketed harness phrasing rather than a loose word match.

## Testing strategy

All new rows live in `internal/streamsup/parser_test.go` and build on the existing
`syntheticUserLine` / `textBlock` / `toolResultBlock` / `collectEvents` fixtures. Two new fixture
constants, both **literals** per `harnessNudgeFixture`'s rule (never built from the production
constant, so editing the constant goes red):

- `interruptNoticeShortFixture` = the short wording
- `interruptNoticeToolUseFixture` = the tool-use wording

New table test `TestParser_InterruptNoticeIsDropped`, rows chosen so each fails alone under a
specific wrong implementation:

- both wordings on an unflagged line → zero events *(a literal pin reddens one of the two)*
- the notice sharing a message with a `tool_result` → the `ToolUpdate` still lands *(a `return`
  instead of `continue`, or a message-level guard, reddens this)*
- an unflagged text block sharing no prefix → `Unrecognized{user_block, "text"}` *(a widened match
  reddens this; this is AC2's half)*
- a text block with a leading space before the notice → `Unrecognized` *(a `TrimSpace` reddens this)*
- a text block merely *containing* the notice mid-string → `Unrecognized` *(a `Contains` reddens this)*
- an unknown block type sharing a message with the notice → still `Unrecognized{…, "image"}` *(a
  guard widened past `text` reddens this)*
- the notice on an **assistant** line → `TextChunk` *(scope is `emitUser`; a guard placed in
  `decodeBlock` or `emitAssistant` reddens this)*

New `TestParser_InterruptNoticeDropIsLoggedContentFree`, mirroring
`TestParser_HarnessNudgeDropIsLoggedContentFree`: exactly one record with `harnessNudgeDropMsg`,
attrs exactly `{site, type}`, and a sweep asserting no captured attr value or message contains the
notice text. The third assertion is the load-bearing one for the same reason it is there.

AC2's other half — `harnessNoOutputNudge` and the `isSynthetic` arm unchanged and still independently
proven — is satisfied by *not editing* `TestParser_SyntheticUserLineDropsTextBlocks` (whose
"unflagged nudge is still dropped by its own constant" and "unflagged non-nudge text still surfaces"
rows are exactly that proof) or `TestParser_CapturedSyntheticUserLinePinsTheWireSpelling`.

AC3's deletion in `internal/e2e/realclaude/interactive_stream_interrupt_test.go`: the
`interruptNoticePrefix` constant and its docblock, the skip arm inside `drainForCancelledTurnEnd`,
the `— with ONE exception since #1500` clause and the `#1611` sentence in that helper's docblock, the
"now carved out below and filed as #1611" clause in the in-arm reading-2 commentary, and the final
two sentences of the `t.Fatalf` text that point at the carve-out. The `strings` import goes with
them — it is used for exactly that one expression.

Gate (per § B2, touched scope only): `go test -race ./internal/streamsup/...`, `go vet ./...`,
`go build ./cmd/pyry`, plus `go vet -tags e2e_realclaude ./internal/e2e/realclaude/` so the edited
build-tagged file is proven to compile without running the live suite.

AC4 — a live green of `TestInteractiveStreamInterruptStopsRunningTurn` with the carve-out deleted —
is the dispatcher's `needs-real-claude` stage, read by the count of `=== RUN` lines rather than the
exit code (CLAUDE.md § Testing). Not run here.

## Open questions

1. **Table of harness prefixes, or a second disjunct?** Resolved in § Design before implementation:
   a second disjunct, because the two matchers have different shapes and a uniform prefix table
   would widen the nudge's byte-exact tolerance. Recorded in the constant's docblock.
2. **Does deleting the e2e carve-out orphan its `strings` import?** Resolved: yes, it is the only
   use in the file; the import is removed in the same edit.
3. **Does any hermetic test observe this notice on the stream-json surface?** Resolved: no. The
   fake's `interruptMarkerLine` is written to the transcript file for turnbridge, never to stdout.

## Documentation handoff

Owned by the documentation stage, not this builder. **Pending.**

- Fold into `docs/knowledge/features/streamsup-package.md` and its child covering the parser's
  suppression tiers: the parser now has a third block-level drop trigger, it is a **prefix** rather
  than a literal or a flag, and the measurement that forced that shape — two wordings within one
  claude version, and 31 of 31 corpus notices carrying no harness flag. Parent stays a map; watch
  its 50000-byte cap.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No MUST FIX. The boundary is explicit and single: `Parser.Write` →
  `consumeLine` → `emitUser`, with `decodeBlock` producing the typed block. The adversarial
  direction for this change is *invisibility*, not disclosure — it makes a class of block
  unrenderable. Enumerating who can actually reach the widened arm: the harness (intended author);
  **not** the model, whose speech arrives on `assistant` lines and is untouched (pinned by the
  assistant-line row in § Testing strategy); **not** a tool, since a `tool_result`'s payload decodes
  into `Content` and never `Text`, so tripping the arm requires control of the block's *type*, not
  just of a string; **not** the operator, whose mid-turn messages park in msgqueue (#1199) and are
  never echoed back as user/text on this surface (2026-07-27 census). An actor who can author a
  `user` line on claude's stdout already controls every byte the parser reads, so the marginal
  capability granted here is nil. Downstream callers are unaffected: dropping a block removes an
  event rather than changing any consumer's type.
- **[Trust boundaries]** OUT OF SCOPE — a future harness payload shaped
  `[Request interrupted by user] <text the operator needs>` would be dropped whole. Not observed:
  all 31 corpus occurrences are the bare bracketed notice and nothing else. Per evidence-based fix
  selection no defence is shipped; the trigger for revisiting is one measured occurrence of a notice
  carrying a trailing payload, which would become a follow-up ticket narrowing the matcher. Recorded
  here so the failed prediction has a home, matching how `harnessNoOutputNudge`'s docblock handles
  the unshipped skill-prefix fallback.
- **[Tokens, secrets, credentials]** No findings — nothing is generated, stored, compared, rotated
  or expired. The only adjacent surface is `emitUnrecognized`, which puts the offending line's bytes
  on the wire (capped at `maxUnrecognizedRaw`); this change *removes* one recurring payload from
  that path, so the net effect on disclosure is a reduction.
- **[File operations]** Not applicable by design — `emitUser` touches no filesystem, and the parser
  deliberately opens, watches and resolves no transcript file (its only input is the `Write` bytes,
  stated in `Parser`'s own docblock). No path, mode, symlink or TOCTOU surface exists to audit.
- **[Subprocess / external command execution]** Not applicable — no `exec.Command`, no environment
  change, no signal handling. The parser *reads* a subprocess's stdout, which is the category-1
  boundary above and is audited there rather than twice.
- **[Cryptographic primitives]** Not applicable, and stated rather than skipped because the change
  adds a string comparison, which superficially pattern-matches this category. Both compared values
  are public harness phrasing, not secrets; `crypto/subtle.ConstantTimeCompare` is therefore not
  applicable and introducing it would be wrong — it would signal a secret where there is none. No
  RNG is involved.
- **[Network & I/O]** No findings. No new read, no new size cap needed:
  `strings.HasPrefix(block.Text, harnessInterruptNoticePrefix)` is O(28 bytes) regardless of block
  size, so a hostile multi-megabyte text block costs 28 byte-compares — a second, independent reason
  to reject the `strings.Contains` shape beyond the correctness reason in § Design. Everything still
  reaching the unrecognized lane remains governed by the existing `maxUnrecognizedRaw` truncation,
  unchanged.
- **[Error messages, logs, telemetry]** No MUST FIX, and this is the category with real content. The
  MUST-NOT-log field is the block's text; the MUST-log fields are `site` and `type`. The design
  reuses the existing drop site verbatim and adds **no third attribute naming which trigger fired** —
  already ruled out in `harnessNoOutputNudge`'s docblock on disclosure grounds, and independently
  unavailable on the merits there because the nudge is itself flagged. Consequence accepted
  deliberately: a debug log cannot distinguish an interrupt drop from a nudge or skill-body drop.
  `TestParser_InterruptNoticeDropIsLoggedContentFree` pins attrs to exactly `{site, type}` and sweeps
  every captured record for the notice text, which is what catches the realistic future regression
  (someone appending `"text", block.Text` at the drop site).
- **[Concurrency]** No findings. `emitUser` runs synchronously on the single `Parser.Write`
  goroutine; the change adds no state, no lock, no goroutine and no shutdown path. The parser's two
  pieces of cross-line state (`thinkingSinceEmit`, `assistantErrorCategory`) are neither read nor
  written here, and `continue` rather than `return` leaves the `result` arm that resets them
  reachable exactly as before. Downstream turn accounting is also unaffected: `cmd/pyry`'s
  `turnMarkFor` already classifies `Unrecognized` as `turnMarkNone`, i.e. droppable at the fan-in, so
  removing one cannot change a turn's bookkeeping.
- **[Threat model alignment]** No findings against `docs/protocol-mobile.md` § Security model. The
  change adds no frame type, no new field and no new wire path — it removes occurrences of an
  existing one. The threat it touches ("harness-internal prose must not enter the operator's own
  message history") is the same one #1247 and #2087 addressed; this closes the remaining measured
  hole in it rather than opening a boundary. No threat is deferred.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-14
