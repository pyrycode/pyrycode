# #2248 — pin claude's `system/task_progress` line from the committed capture

## Files read

- `internal/e2e/realclaude/testdata/parent_tool_use_v2.1.259.json` — **the finding this
  whole plan turns on.** A whole-turn record (17 frames, indices 0–16, matching its own
  `lines_captured`) that carries TWO verbatim `system/task_progress` payloads plus the turn's
  `line_type_census`. Landed on main by `c17cbf23`, after the refiner's stated re-check.
- `internal/e2e/realclaude/parent_tool_use_capture_test.go` — the probe that produced those
  bytes: `sonnet` rather than the family's usual `haiku`, `--allowed-tools` omitted so the
  Agent/Task tool is reachable, and a prompt asking for the delegation explicitly. This is
  the agent-shaped staging the ticket's Technical Notes name as "the nearest thing"; it
  turns out to be the thing itself.
- `internal/streamsup/task_notification_capture_test.go` — `taskNotificationReaderGate`,
  `taskNotificationPinnedKeys` and
  `TestRealClaudeTaskNotificationCaptureKeysArePinned`. The newest reader in the family and
  the shape AC5 is copied from: four quadrants, exactly one legal skip, every provenance
  check written out rather than imported.
- `internal/streamsup/parent_tool_use_capture_test.go` — `parentCapturePath`,
  `parentCaptureVersion`, `parentPinnedAgentID`. A second reader over the SAME record,
  answering a different question (the subagent-to-agent join). It reads none of the
  `task_progress` frames, which is why this ticket has work to do.
- `internal/streamsup/capture_test.go` — `capturedLines`, and its docblock's standing rule
  that a capture reader takes no path parameter and restates its own provenance checks.
- `docs/knowledge/features/e2e-realclaude-task-notification-capture-test-go.md` — the
  one-day-old lessons page. Its three lessons (a deny-scan-describing field fails the
  deny-scan, a background task outlives its turn, re-scan after every marshal) are all
  properties of a *writing* probe. This ticket writes no record, so none of them bind here,
  and saying which lessons do not apply is part of reading them.
- `CODING-STYLE.md`, `docs/knowledge/architecture/system-overview.md` — package layout and
  the streamsup/e2e split.

## Context

The ticket's premise is one commit stale, and the difference is the whole design.

The body states, re-checked 2026-09-09 at `915f8691`: "the string appears nowhere in this
repo — never captured, never counted, entirely unmeasured." That was true at that commit.
`c17cbf23` then landed five recovered captures on main, one of which is
`parent_tool_use_v2.1.259.json`, and it carries the quarry verbatim: two
`system/task_progress` lines at claude 2.1.259, taken under an agent-shaped staging, with
`subagent_type` `general-purpose` and a populated `usage` object on each. Its census counts
`"system/task_progress": 2` and its frames array holds exactly those two.

So the evidence AC1 asks for is already committed, already redacted through
`dropcapRedactor`, already deny-scanned by `dropcapScanner`, and already version-named. What
does NOT exist is any consumer: `internal/streamsup` contains no occurrence of the string
`task_progress` at all. The bytes are committed and unread, which is the state the family's
own gates exist to make impossible.

That reframes the ticket from "spend a live turn capturing bytes" to "make the bytes that
exist load-bearing inside `make check`". The user story is served either way — the mapping
downstream gets a committed line to declare from — and only one of the two costs a live
turn and real tokens to re-measure what is measured.

**What this means for each acceptance criterion**, stated plainly rather than left to be
diffed:

- **AC1** is satisfied by `parent_tool_use_v2.1.259.json` as it stands: committed under
  `internal/e2e/realclaude/testdata/`, named with the claude version, holding the verbatim
  payloads together with the whole turn's line-type census.
- **AC2** is moot in the branch it governs. It specifies what a did-not-fire record must
  say; the subtype fired, twice, so there is no absence to record. The record names its
  staging regardless (its prompt, its model, its spawn-shape delta).
- **AC3** is this ticket's to assert and is asserted below.
- **AC4**'s mechanism is present and has already fired.
  `TestRealClaude_ParentToolUseCapture` arms on its fixture's absence, disarms once it
  exists, and stages what it wrote — which is how these bytes reached main rather than
  going out with a worktree. A second probe re-capturing the same subtype at the same
  claude version would spend a live turn to produce a second copy of a measurement already
  held.
- **AC5** is the real remaining deliverable and is the whole of Phase B.

The one thing genuinely unmeasured after this ticket is the `summary` field, and it is
recorded as such rather than left silent — see Open questions.

No ADR is warranted. This adds a reader in an established family and changes no decision.

## Design

One new file, `internal/streamsup/task_progress_capture_test.go`. No production file is
touched; `streamLine` and the drop behaviour are left alone, as the ticket instructs.

### Constants — its own, deliberately not shared

The reader mints `taskProgressCapturePath` and `taskProgressCaptureVersion` as its own
package constants, spliced the way every sibling splices them, even though
`parentCapturePath` already names the same file. Borrowing that constant would couple this
pin to another ticket's repin: a later capture at 2.1.260 would move `parentCapturePath` to
a new file and silently carry this reader along to bytes it never measured. Independent
constants make the same event a loud fatal in quadrant 2 instead — the pin names keys and
the file it names is gone.

Deliberately NOT added: an assertion that the two constants are equal. Two readers pinned
at two claude versions over two committed records is a legitimate steady state, and an
equality check would forbid it.

### What gets pinned

Three package-level slices, each a measurement read off claude's own bytes:

- `taskProgressPinnedKeys` — the top-level key set, sorted, that EVERY captured
  `system/task_progress` line arrived with. Per-frame equality rather than a union across
  frames: the family's rule is that the field set is exactly what the capture shows, and a
  union would let one line carrying a subset hide inside another line's keys.
- `taskProgressPinnedUsageKeys` — the sub-keys of the nested `usage` object. The mapping
  downstream has to declare a nested shape, and a top-level pin says nothing about it.
- `taskProgressDocumentedKeys` — what `SDKTaskProgressMessage` describes in
  `@anthropic-ai/claude-agent-sdk@0.3.263`. **A thing to check the capture against, never a
  field set to declare from.** It reaches assertions only as the two set differences, so a
  key the SDK describes and claude does not send is a recorded measurement rather than a
  field declared on the strength of a docs page.

The two differences are themselves pinned, because both carry information the mapping
needs: `summary` is documented and not observed under this staging, and the envelope keys
(`type`, `subtype`, `uuid`, `session_id`) are observed and not documented, which is the
expected shape for this family rather than a finding.

### The gate — AC5's one legal skip

`taskProgressReaderGate(fixtureExists, pinFilled bool) (action, reason string)`, a pure
function over the same four quadrants `taskNotificationReaderGate` establishes, with the
same three actions. Skip only when the fixture is absent AND nothing is pinned; both
mixed states fatal; both present runs.

The difference from the sibling is which quadrant this ticket lands in. The sibling shipped
in the skip quadrant awaiting a live gate. This one ships in the run quadrant, because the
bytes are already committed — so the reader asserts from its first `make check` and the
skip quadrant is proven by the pure-function test alone rather than by the reader's own
leg. That is the stronger of the two states, and it is why the gate's four quadrants must
be proven offline: three of them are otherwise unreachable here.

### The assertions

`TestRealClaudeTaskProgressCaptureKeysArePinned` — provenance first (`is_capture`, the
version arm on the leading token of `claude_version`), then, over the frames whose type is
`system` and subtype is `task_progress`:

- `payload_encoding` must be `json-string`; anything else means a line that was not valid
  UTF-8 and carries no readable key set.
- The frame's envelope labels are checked against its payload's own bytes, so a frame
  labelled one subtype and holding another fails rather than being counted.
- Keys are re-derived by decoding into `map[string]json.RawMessage`, never a typed struct —
  a struct silently drops every key it does not declare, which is the exact failure the
  family's declare-only-what-you-captured rule exists to prevent.
- Zero frames fatals. A reader that passes over an empty loop proves nothing.

`TestRealClaudeTaskProgressCaptureIsNotToolProgress` — AC3, and the trap the ticket names.
Three checks, each refusing a different way the two could be conflated:

- The census must carry the key `system/task_progress` and must NOT carry a bare
  `task_progress` key. The census keys system subtypes as `system/<subtype>` and top-level
  types by their bare name, so `tool_progress` (a top-level type, already measured
  elsewhere) and `task_progress` (a system subtype) occupy distinct keys by construction —
  and a bare `task_progress` key would mean something counted a system subtype as a
  top-level type.
- The census count for `system/task_progress` must equal the number of frames this reader
  matched, so the census and the payloads cannot disagree about how many there were.
- No matched frame's payload may declare `"type"` anything but `system`, which is what
  stops a `tool_progress` line being read under this pin.

`TestTaskProgressReaderGateHasExactlyOneLegalSkip` — the four quadrants, table-driven, pure,
running on every leg including ones where the reader itself could assert nothing.

`TestTaskProgressDocumentedKeysAreOnlyEverAComparison` — the net under the family's rule.
It asserts that the pinned key set is not merely a copy of the documented one: the two
differences must be non-empty in both directions, so a future editor who "fixes" the pin by
pasting the SDK's field list reddens instead of passing. Without it the documented list is
decoration.

## Concurrency model

None, and stating that is the point rather than a formality. Every test in the new file is
a pure read of a committed file plus in-memory decoding. No goroutine is spawned, no
subprocess started, no channel used, so there is no shutdown sequence to design and nothing
to leak. `t.Parallel()` on the pure-function tests only; the reader tests do file I/O on an
immutable committed path and gain nothing from it.

## Error handling

The family's split, restated rather than imported:

- **Skip** — exactly one state, the gate's first quadrant. Anything else that skips would
  be a green run proving nothing, which is CLAUDE.md § Testing's stated failure.
- **Fatal** — every other failure. The capture is committed, so a missing, undecodable,
  wrong-version or zero-frame record is a broken premise rather than an unavailable
  resource. `t.Fatalf` rather than `t.Errorf` wherever a later assertion would otherwise
  run against a zero value and report a second, misleading failure.
- Every fatal message names what to do next: re-capture and repin both ends together,
  restore the fixture, or empty the pin in the same commit.

The one failure mode worth calling out because it is not obvious: a forced re-capture at
the SAME claude version (`PYRY_PROBE_PARENT_TOOL_USE_CAPTURE=1`, or whatever that probe's
force variable is named) overwrites the record in place. If that turn's claude inlines the
reads instead of delegating — the exact flake `parent_tool_use_capture_test.go` exists to
fight — the new record carries no `task_progress` frames and this reader fatals with "zero
frames". That is the correct, loud outcome, and the reader's doc says so, so the next
person reads it as a re-capture that lost a measurement rather than as a broken test.

## Testing strategy

`go test -race ./internal/streamsup/...` is the gate, and it is the whole gate: the new file
carries no build tag, so it runs inside `make check` on every unrelated ticket from now on.
That is the property AC5 buys and the reason the reader lives in `internal/streamsup` rather
than beside the bytes.

RED before GREEN is available here in a way it usually is not for a reader over committed
bytes, because the pin is a value I derive from the record rather than a behaviour I
implement: the first run is written with the gate and the assertions in place and the pin
deliberately holding a wrong set, so the failure message is read once as a real reader would
read it, and only then filled from the record. A pin that has never been seen to fail is a
pin nobody has proven reads anything.

No new fixture is created and no directory is added; in particular nothing is written under
`internal/streamsup/testdata/`, which does not exist and whose creation would fork the
convention.

## Open questions

1. **Should a dedicated `task_progress` probe still be built, for a SECOND staging shape?**
   Resolved for this ticket as no, and the reasoning is in Context. What a second probe
   would genuinely add is the one field this capture cannot show: `summary` is documented
   as present for a local agent only with the progress-summaries option, and always for an
   MCP task. This staging is a local agent without that option, so `summary`'s absence here
   is a measurement about this staging and not about the subtype. This ticket pins that as
   a documented-not-observed difference rather than leaving it silent, which is what stops
   the mapping inventing the field. Whether to spend a live turn provoking the
   progress-summaries or MCP shape is a scoping decision for a human, and the closing
   comment routes it as one.
2. **Does the mapping ticket downstream need anything else from these bytes?** The record
   holds the full payloads, so anything it needs is re-derivable without a new capture.
   Resolved: pin the top-level keys and the `usage` sub-keys, which are the two shapes a
   decode target has to declare.

Both are resolved above; a `## Revisions` entry records anything Phase B changes.

## Security review

**Verdict:** PASS

The category that shapes this review is the one the ticket's own security note raises: the
committed record is a public artefact carrying claude's stdout verbatim. The decisive fact
for every category below is that **this ticket writes no record and adds no bytes to one.**
It reads an artefact that already passed `dropcapRedactor`'s declared substitution table and
`dropcapScanner`'s fail-closed deny-scan. Both mechanisms are inherited unchanged and
neither is weakened, bypassed, or given a new caller.

**Findings:**

- **[Trust boundaries]** No findings. One boundary, file to memory, and it is explicit:
  `taskProgressCapturePath` is a package constant and the reader takes **no path
  parameter**, which is the standing rule `capturedLines`' docblock states by name. That
  rule is what stops an unchecked file being read behind provenance assertions, and the
  worst case it forecloses is precisely a hand-built payload file swapped in for a genuine
  capture. `is_capture` and the `claude_version` arm are both asserted before any key is
  read, so a file failing provenance fails before it can influence a pin.
- **[Tokens, secrets, credentials]** No findings. Nothing is generated, stored, rotated, or
  compared against a secret. The record's `session_id` is a declared substitution class and
  arrives already replaced in the committed bytes; `task_id`, `tool_use_id` and `uuid` are
  claude's per-message identifiers, kept deliberately in every capture in this directory and
  not secrets. The adjacent leak vector is real but belongs to the next category.
- **[File operations]** No findings, and one deliberate choice worth naming. The gate
  derives existence from a **single `os.ReadFile`**, taking `errors.Is(err, fs.ErrNotExist)`
  as absence, rather than `os.Stat` followed by a read. That is the TOCTOU-free shape: there
  is no gap between check and use for a swap to land in. No file is created, so no mode
  question arises; no path is concatenated from any input, so traversal is unreachable; the
  path is a repo-relative compile-time constant, so symlink following is not attacker-
  influenced. The read is uncapped, which is safe here for a stated reason rather than an
  assumed one: the operand is a reviewed in-repo file of about 30 KB, not a socket.
- **[Subprocess / external command execution]** No findings — nothing is executed. This is
  the sharpest difference from the writing probes in this family: `tncapStageFixture`'s
  whole class of concern, a `git add` whose combined output can carry a repository path into
  a record after the first marshal, does not arise where no record is written and no command
  is run.
- **[Cryptographic primitives]** No findings. No randomness is drawn. The one comparison in
  the design is between two sorted lists of **field names** — claude's vocabulary on one
  side, a pinned literal on the other — so neither operand is a secret and constant-time
  comparison would protect nothing.
- **[Network & I/O]** No findings — no socket, no listener, no HTTP server, so there is no
  size cap, timeout, or slow-loris surface to specify.
- **[Error messages, logs, telemetry]** **SHOULD FIX.** Every fatal must name field names,
  frame indices, counts and the census, and **never print a payload value.** For today's
  committed bytes this is discipline rather than exposure — those bytes are already public
  in the repo — but it is load-bearing forward: the ticket's own security note records that
  a background task's `description` carries a literal operator command line, and
  `description` is one of the keys these frames carry. A fatal that printed values would put
  that into CI output on the first re-capture whose staging differed. Phase B enforces it;
  the verifier can check it by reading the format strings.
  Also recorded here because the asymmetry is easy to get wrong in the other direction: the
  sibling's lesson that rig-authored prose describing the deny-scan **fails** that deny-scan
  does **not** bind on this file. That lesson governs prose entering a written record, where
  `dropcapFixedNeedles` scans it. This file's prose reaches test output only and is never
  marshalled into an artefact, so nothing scans it and no constraint on its wording is
  warranted. Inventing one would be theatre.
- **[Concurrency]** **SHOULD FIX**, and it is the one finding a `-race` run would probably
  not catch on its own. The pins are package-level `var` slices, and the reader has to
  compare a sorted observed set against them. Sorting a pin **in place** mutates shared
  package state that a `t.Parallel()` sibling could read — a race that stays invisible while
  no parallel test happens to touch the same slice, which is exactly today's arrangement, so
  the detector has nothing to trip on and the hazard ships latent. Fix by construction
  rather than by copying: **declare the pins already sorted and never sort them at run
  time.** Only locally-built slices are sorted. Beyond that the design spawns no goroutine,
  takes no lock, and shares no mutable state, so there is no lock ordering to document and
  no shutdown path to design.
- **[Threat model alignment]** No findings, and one adversarial check worth recording
  because it is the non-obvious way a reader can hurt a producer. A new consumer of a
  committed artefact can create pressure to loosen the producer's promotion gate, which is
  the failure `stagingVerdict`'s doc warns about in the sibling. It does not arise here: this
  reader is pinned to the 2.1.259 record, and a future capture refused by
  `parent_tool_use_capture_test.go`'s `fixtureWorthy` leaves those bytes untouched, so a
  refusal never reddens this reader and never creates a reason to weaken the refusal. The
  one coupling that does exist — a forced re-capture overwriting the record in place — is
  named in Error handling and resolves to a loud fatal, not a silent pass.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-09
