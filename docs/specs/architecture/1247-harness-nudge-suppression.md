# Spec #1247 — suppress claude's no-visible-output self-nudge at the user-block drop site

**Size:** S · **Package:** `internal/streamsup` · **Ticket:** #1247 · **Label:** `security-sensitive`

## Files to read first

| Path | Extract |
|---|---|
| `internal/streamsup/parser.go:340-363` | `emitUser` — the exact loop the new guard slots into, and the `block.Type != "tool_result"` arm it must precede |
| `internal/streamsup/parser.go:38-73` | `ignoredLineTypes` + its comment. **Lines 62-66 are the paragraph AC5 makes wrong and requires corrected in place.** |
| `internal/streamsup/parser.go:227-234` | the existing known-ignored drop: the log shape (`Debug`, message + `type` attr, no content) the new drop mirrors |
| `internal/streamsup/parser.go:178-194` | `streamBlock` — `Text` is populated from a block's own `"text"` key; a `tool_result`'s payload lands in `Content`, never in `Text`. This is why the guard cannot be tripped from tool output. |
| `internal/streamsup/parser_test.go:154-166` | the `"unknown user block type surfaces"` case — the anti-widening guard. **Stays green, unmodified.** |
| `internal/streamsup/parser_test.go:187-194` | `"known block with unknown extra fields still maps"` — the package's standing position on unknown sibling fields, which the residual in § Security is argued against |
| `internal/streamsup/parser_test.go:20-28, 220-229` | `collectEvents` + the table-runner shape every new mapping case reuses |
| `internal/streamsup/parser_test.go:433-444` | `TestParser_IgnoredLineTypesIsTheMeasuredSet` — the "pin the knob so growth is deliberate" idiom this ticket deliberately does **not** copy (§ Design, "Constant, not a set") |
| `internal/streamsup/runner_test.go:399-445` | `spawnArgsRecorder` — the capturing `slog.Handler` shape AC4's assertion mirrors (4 methods, mutex-guarded, `Enabled` → true) |
| `internal/turnevent/event.go:99-140` | `UnrecognizedSite` constants; `UnrecognizedUserBlock == "user_block"` is the site string the new log reuses |
| `docs/specs/architecture/unrecognized-claude-messages.md:47-88, 111-118` | the 2026-07-27 measurement and the four-drop-site table. The `user_block` row's description is now incomplete — see § Out of scope. |
| `internal/e2e/realclaude/interactive_stream_liveness_test.go:229` | the shared drain's zero-unrecognized `t.Fatalf` — why this is a latent trip-wire, not a live-test deliverable |

## Context

`emitUser` surfaces every non-`tool_result` block in a `user` message as
`Unrecognized{Site: user_block}`. That rule rests on the 2026-07-27 measurement,
which found `user` messages carry `tool_result` blocks and nothing else.

The measurement never drove a backgrounded command. Backgrounding produces a turn
with no visible model output, and claude's harness then injects a prod:

```
site=user_block  message_type=text
{"type":"text","text":"[Your previous response had no visible output. Please continue and produce a user-visible response.]"}
```

Reproduced 3 of 3 on the #1240 probe, claude 2.1.220. It is a real exception to a
correct rule, not a defect: the rule surfaced it, which is the rule working.

Today it reaches clients as an `unrecognized_message` frame carrying nothing
actionable, and — because the zero-unrecognized assertion lives in the **shared**
`drainForCompletedTurn` — the first real-claude spec that ever backgrounds a
command goes red on a known-benign payload.

**Direction: ignore.** Fixed by the design's own criterion — a payload recurring
per-occurrence of a common action is noise, which is why `system` is ignored
wholesale. Backgrounding is common; the nudge carries nothing a reader wants.
Surfacing stays reachable by the fail-safe direction below. If you disagree with
the direction, route back with `needs-rework:po` rather than implementing the
other one.

### Provenance is thin, and it is binding on the design

The exact string exists in no tracked file, and the local `~/.claude/projects`
corpus corroborates nothing (14 phrase-bearing lines, zero genuine injections —
all prose about this ticket, plus the issue's own body delivered as a prompt).
One version, one probe session, three occurrences.

Two consequences the developer must honour:

1. **The issue capture is the only record of the wording.** The fixture is
   transcribed byte-exact from it. Not captured afresh, not reconstructed.
2. **Cross-version stability is unmeasured.** Write the matcher as though the
   string were attested once — because it was.

## Design

### One constant, one guard, one log line

Add to `internal/streamsup/parser.go`, near `ignoredLineTypes`:

```go
// harnessNoOutputNudge is the ONE user/text block the parser drops in silence.
const harnessNoOutputNudge = "[Your previous response had no visible output. Please continue and produce a user-visible response.]"
```

100 bytes, ASCII only, single-spaced, ASCII hyphen in `user-visible`, no smart
quotes — verified by `od -c` against the issue capture. Its doc comment carries
the provenance AC5 demands: the capture's origin (#1240 probe, 3 of 3), the
claude version (2.1.220), that the author is claude's harness rather than the
user or the model, and that this is a **suppression, not a mapping** — the text
must never be rendered as something the person typed.

The guard goes in `emitUser`, **before** the `block.Type != "tool_result"` arm:

```go
if block.Type == "text" && block.Text == harnessNoOutputNudge {
    p.log.Debug("streamsup: dropping known harness user block",
        "site", string(turnevent.UnrecognizedUserBlock),
        "type", block.Type)
    continue
}
```

Three properties are load-bearing and each has a test behind it:

- **`continue`, not `return`.** The suppression is scoped to the matching block;
  sibling blocks in the same message keep their mapping (AC3).
- **`block.Type == "text"` is required, not implied.** A future block type
  carrying identical text is not this payload and must still surface. It is also
  what makes the guard unreachable from tool output (§ Security).
- **Byte-exact `==`.** No trim, no fold, no normalise.

Attrs are `site` and `type` only. `block.Type` is passed rather than the literal
`"text"` so the log stays honest if the guard is ever widened.

### Why exact equality, and not the alternatives

The two candidate errors are not symmetric, and the design picks the one that
fails loudly:

| Matcher | Wording drifts | A future harness payload, or a prompt echo, arrives |
|---|---|---|
| **exact string (chosen)** | no match → row returns → someone looks — **fail-safe** | no match → surfaces — **fail-safe** |
| prefix / substring | still matches → drift invisible | swallowed silently — **fail-open** |
| structural (`[…]` bracket form) | matches | swallows every bracketed harness payload, forever — **fail-open** |

The bracketed `[…]` form is a visible feature of harness-injected prose, and the
Technical Notes flag it as a candidate signal. It is rejected: one observation on
one version is not enough to key a permanent silence on, and its failure
direction is the wrong one. The thin provenance argues for **narrow**, not for
tolerant — a matcher's tolerance should be proportional to how well-attested the
string is, and this one is attested once.

### Constant, not a set

`ignoredLineTypes` is a documented `map[string]bool` with a pin test, because it
is a list expected to be re-measured and grown. This is not that. A one-element
set keyed on 100 bytes of prose invites the read "here is a list to add to,"
which is the opposite of what the provenance supports. A **second** confirmed
harness payload is what should promote the constant to a set — with a measurement
behind it, and with the pin test that idiom carries. Not before.

### This is a new tier, and it says so

`ignoredLineTypes` is top-level only. `emitAssistant`'s comment states the
block-level position explicitly: *"No known-ignored list at block level … Any
fourth is news."* This is the first block-level suppression in the parser. The
constant's doc comment names it as a new tier — one payload, one site, one
version — rather than letting it read as an entry on an existing list.

Scope is `emitUser` only. Assistant `text` blocks map to `TextChunk` and are
untouched; the nudge has never been observed there, and widening to that surface
would silence model speech.

### What does not change

- **`emitAssistant`** — untouched.
- **`ignoredLineTypes`** — no new entry; this is not a top-level type. Its
  **comment** changes (AC5), its contents do not, so
  `TestParser_IgnoredLineTypesIsTheMeasuredSet` stays green untouched.
- **`turnevent`, `turnbridge`, `protocol`, `cmd/pyry`** — no wire change. One
  frame fewer is emitted; no frame changes shape.
- **`parser_test.go:159` `"unknown user block type surfaces"`** — stays green
  **unmodified**. It is the anti-widening guard. If a change to it is needed to
  make the build pass, the guard is too wide and the design is wrong.

### Concurrency model

Unchanged. The guard is straight-line code inside `emitUser`, on the single
os/exec forwarder goroutine that the parser's single-writer invariant already
covers (`parser.go:88-94`). No new state, no new goroutine, no lock. The parser
stays turn-stateless: the suppression reads one block and remembers nothing.

### Error handling and failure direction

There is no new error path — the guard has one branch and it drops. The failure
modes are the two in the matcher table:

- **Under-match** (drift, a new harness wording): the `Unrecognized` row returns,
  a human reads it, and the fix is a deliberate edit with a fresh capture behind
  it. This is the designed-for failure.
- **Over-match**: structurally bounded to one exact 100-byte string in one block
  type at one site. See § Security for the residual.

A block that fails to decode never reaches the guard — `decodeBlock` already
surfaces it as `UnrecognizedUndecodable` and reports `ok == false`.

## Testing strategy

All offline. No live claude, no `needs-real-claude`, no credential fixture. The
developer must finish with `go test -race -run 'TestParser' -v ./internal/streamsup/`
showing **PASS with zero SKIP** — a skipped test is not a passing one.

**Fixture rule, and it is the point of the whole test.** Every test fixture
carries the nudge as a **string literal**, never as `harnessNoOutputNudge`. A
test built from the constant asserts nothing about the bytes: edit the constant
and the test follows it green. The literal is what makes an edit to the constant
go red.

### New table cases in `TestParser_LineMapping`

Each is one row; the existing runner and `collectEvents` are reused as-is.

- **the nudge alone is silent** — a `user` message whose only content block is
  `{"type":"text","text":"<nudge literal>"}` → `want: nil`. (AC1)
- **the nudge alongside a tool_result still emits the tool_result** — the nudge
  block **first**, a `tool_result` block second → exactly one `ToolUpdate`, no
  `Unrecognized`. Nudge-first is deliberate: it proves the loop continued past
  the suppressed block rather than returning. (AC3)
- **the nudge embedded in longer text still surfaces** — text = some prefix +
  the nudge literal → `Unrecognized{Site: user_block, Kind: "text"}`. Kills
  substring matching.
- **drifted wording still surfaces** — the nudge literal with one word altered
  near the end → `Unrecognized{…}`. Kills prefix matching, and is the fail-safe
  direction as an executable assertion.
- **a tool_result whose content is the nudge still maps** — a `tool_result`
  block whose `content` is the nudge literal → the normal `ToolUpdate` carrying
  that text. This pins the trust boundary in § Security: attacker-influenced
  material arrives in `Content`, never in `Text`, so it cannot reach the guard.

The existing `"unknown user block type surfaces"` case is **not** in this list
because it is not edited.

### The log assertion (AC4)

A new test with a capturing `slog.Handler` in `parser_test.go`, mirroring
`spawnArgsRecorder`'s four-method shape (`Enabled` → true so Debug records
arrive; `Handle` filters on the record message; `WithAttrs`/`WithGroup` return
the receiver; mutex-guarded). It records message plus attr key/value pairs — no
new test infrastructure beyond that struct.

Feed the nudge line to a `Parser` built with that logger, then assert:

- exactly **one** record with message `"streamsup: dropping known harness user block"`
- its attr set is exactly `{site: "user_block", type: "text"}` — two attrs, no
  more
- **no captured attr value contains the nudge text.** This is the assertion that
  encodes "never the text"; the previous two only describe what is present.

The third assertion is what would catch a later edit adding `"text", block.Text`
to the log line, which is the realistic way the content-free rule gets broken.

### Documentation (AC5)

`ignoredLineTypes`' comment at `parser.go:62-66` currently reads that user/text
"needs no ignore entry, and a user/text block appearing in future is a real
change worth surfacing." That is now wrong for exactly one string. Correct it in
place — beside the measurement it amends, not in a new paragraph elsewhere —
recording: the capture's provenance and claude version, that the author is
claude's harness rather than the user or the model, why this is a suppression
rather than a mapping, and that every **other** user/text block still surfaces.

## Security review

**Verdict:** PASS

Run because the ticket carries `security-sensitive`, per
`pyrycode-agents/architect/security-review.md`. Two residuals are named and
accepted; neither is MUST FIX.

**Findings:**

- **[1. Trust boundaries] SHOULD FIX — accepted as designed, with a test as the
  enforcement.** The boundary is subprocess stdout → parent state, and it is
  explicit and single-point: `Parser.consumeLine` is the only place claude's
  bytes become typed events. Within that stream the two sides are not equally
  trusted. **Structure** (top-level `type`, message `role`, block `type`) is
  authored by claude's harness; **strings inside blocks** are routinely
  attacker-influenceable, because a `tool_result`'s content is whatever a file
  read, a command, or a web fetch returned. This suppression keys on a *string* —
  the influenceable side. That is the shape that made #1229 dangerous (a control
  frame keyed on prose the untrusted party also authors), so it is checked, not
  waved past.
  **What bounds it, structurally:** attacker-influenced material does not arrive
  as `block.Text`. It lands inside a `tool_result` block, whose payload
  `streamBlock` decodes into `Content` (`parser.go:178-194`). A tool result whose
  body contains — or *is* — the nudge does not trip the guard, because
  `block.Type == "text"` fails. Reaching the guard needs control over message
  **structure**, not just over a string. Pinned by the fifth test scenario in
  § Testing rather than left as an argument. Downstream callers are handed no new
  type and no new data; they receive strictly fewer events.
- **[1. Trust boundaries] No finding — blast radius is diagnostic-only.** The
  suppressed frame is `Unrecognized`. `cmd/pyry/stream_turn_busy.go`'s opener set
  is a **whitelist**, so `Unrecognized` neither opens nor closes a turn
  (`unrecognized-claude-messages.md` § `stream_turn_busy.go`). A successful
  forgery cannot wedge a conversation, forge a turn boundary, or move lifecycle
  state — it hides one diagnostic row from a human. No capability is granted and
  none is removed. The inverse direction (making the guard *not* fire) costs one
  byte and the consequence is that the row returns: the designed fail-safe.
- **[2. Tokens, secrets, credentials] Not applicable — no secret exists in this
  design.** It introduces no token, no credential, and no persisted state; the
  only new datum is a compile-time constant of harness boilerplate published in
  the issue. The adjacent question worth answering: could the suppression ever
  hide a payload carrying a secret an operator needs to see? No — it is keyed on
  one exact known-benign string, and any other content surfaces unchanged.
- **[3. File operations] Not applicable — the design performs no filesystem
  access.** The parser opens, watches, and resolves no file at all; its only
  input is the `Write` bytes (`parser.go:79-81`). No path is built, so path
  traversal, TOCTOU, file modes, symlinks, and atomic writes have no surface
  here.
- **[4. Subprocess execution] Not applicable — no process is spawned, and none
  can be influenced.** The design reads the stdout of a child spawned elsewhere
  (`runner.go` `buildArgs`); it alters no argv, no environment, and no signal
  path. It cannot influence whether a child is spawned or killed, because the
  parser is turn-stateless and `Unrecognized` is not a turn opener.
- **[5. Cryptographic primitives] No finding, and the constant-time question is
  answered rather than skipped.** No RNG, no keys, no hashing. The category's one
  live sub-item is that an attacker-influenceable string is compared to a
  constant with `==`, which is variable-time and leaks a common-prefix timing
  signal. `crypto/subtle.ConstantTimeCompare` is **not** warranted: the compared
  value is not a secret — it is published verbatim in issue #1247 — so timing
  reveals nothing an adversary does not already have. Using `subtle` here would
  misrepresent the constant as sensitive.
- **[6. Network & I/O] No finding — every existing cap is untouched and none is
  weakened.** The comparison runs on an already-decoded `block.Text`, whose size
  is bounded by the unchanged `defaultMaxParseBuf` (4 MiB). `maxUnrecognizedRaw`
  (16 KiB) still applies to every block that surfaces. Note the interaction
  explicitly: an oversized user/text block used to be truncated at `Unrecognized`
  construction and now could in principle be dropped earlier — but a match
  requires exactly 100 bytes, so no oversized payload can ever match, and every
  non-matching one follows the unchanged truncation path. Push-queue and
  backpressure behaviour are unchanged; one frame fewer is emitted.
- **[7. Error messages, logs, telemetry] No finding — MUST-NOT-log is enforced by
  an assertion, not by convention.** The guard has no error path and produces no
  error message. MUST-log: `site`, `type`. MUST-NOT-log: the block text. The
  payload is not itself sensitive, but the rule is about the site: the same line
  would carry an arbitrary user/text block's content the day someone appends
  `"text", block.Text`. AC4's third assertion — no captured attr value contains
  the nudge — is what catches that edit. Telemetry: nothing user-identifiable is
  aggregated; the frame that stops reaching clients carried no user data. The
  design does reduce what an operator can see, deliberately; the compensating
  control is the fail-safe direction (drift → the row returns).
- **[8. Concurrency] No finding — no new lock, goroutine, or shared state.** The
  guard is straight-line code inside `emitUser`, on the single os/exec forwarder
  goroutine the parser's documented single-writer invariant already covers
  (`parser.go:88-94`). It reads a local `block` decoded from a local `raw`, so
  there is no check-then-mutate on shared state and no lock-ordering question.
  Nothing is spawned, so nothing can leak. Shutdown mid-write is unchanged: the
  parser's only state is the partial-line buffer. On the test side, the capturing
  handler is driven synchronously by `Write` on the test goroutine, so it is not
  actually raced; the mutex specified in § Testing is for consistency with
  `spawnArgsRecorder` and to keep the recorder safe if it is later pointed at a
  live runner.
- **[9. Threat model alignment] No finding — the design reinforces threat #1.**
  `docs/protocol-mobile.md:1025` (§ Security model, *Prompt injection*,
  `severity: high, mitigation: partial`) turns on keeping the origin of text
  distinguishable — its v1 mitigation is a system-prompt prefix marking
  mobile-originated messages. The ticket's anti-widening rule is in the same
  family: mapping user/text blocks generically would render harness-injected
  prose as user-authored history, an origin-confusion bug. Choosing **suppression
  over mapping** preserves that boundary rather than eroding it. Threats #2–#8
  (server-id race, relay MITM, token leak, replay, DoS, static-key compromise)
  have no surface here — this change adds no network path, no key, and no
  persisted state. `docs/threat-model.md` does not exist in this repo.

**Residual 1 — sibling fields on the nudge block are suppressed with it.**
`{"type":"text","text":"<nudge>","injected_by":"…"}` matches and is dropped,
hiding the extra field. **Accepted.** The package already decided unknown sibling
fields do not change a block's disposition (`parser_test.go:187-194`, "known
block with unknown extra fields still maps"), and a key-set check would be a
defence against a failure mode never observed. An adversary able to add sibling
fields to a block inside a `user` message already holds a strictly better target:
a forged `tool_result` block maps to `ToolUpdate`, which does reach the tool
tracker. Seeing a nudge block carrying extra fields is the trigger to revisit.

**Residual 2 — a genuine user message could be swallowed.** Only if claude begins
echoing prompts as `user`/`text` on this surface (measured absent, 2026-07-27)
**and** the prompt is byte-exactly the 100-byte nudge. Under a substring or
prefix matcher this residual would be large — any message containing the phrase.
Under exact equality it is one specific 100-byte string typed on purpose, and
what is lost is a diagnostic row. **Accepted**, and it is the direct payoff of
the narrow matcher.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-07-30

## Out of scope

- **`docs/specs/architecture/unrecognized-claude-messages.md`.** Its four-drop-
  site table's `user_block` row (line 117) and its § "The open question the
  measurement answered" both now need the exception noted. That file belongs to
  the **documentation phase**, which writes it from this spec plus the merged
  diff. The developer must not edit it — AC5 is satisfied entirely inside
  `parser.go`.
- **A real-claude spec that backgrounds a command.** The shared drain's
  zero-unrecognized assertion is a latent trip-wire, and this change disarms it
  for this payload. Proving that live needs a spec that backgrounds a command,
  which does not exist. Not a deliverable here; the offline cases cover the
  parser's behaviour completely.
- **The assistant surface.** See § Design.

## Open questions

- **Does the wording change across claude versions?** Unmeasured — one version.
  The design's answer is the fail-safe direction: drift makes the row reappear.
  No action needed until it does.
- **Is the nudge emitted on any path other than backgrounding?** Backgrounding is
  the one observed trigger; "a turn that produced no visible output" is the
  stated cause and may have other routes. Immaterial to this design — the guard
  keys on the payload, not on why it fired.

## Files touched

| File | Change |
|---|---|
| `internal/streamsup/parser.go` | one constant + doc comment, one 6-line guard in `emitUser`, corrected `ignoredLineTypes` comment |
| `internal/streamsup/parser_test.go` | 5 table rows, 1 log-assertion test + its capturing handler |

One production file. Well inside `s`.
