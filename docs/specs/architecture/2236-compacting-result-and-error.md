# 2236 — put a failed compaction's result and error on the compacting frame

## Files read

- `internal/streamsup/parser.go` → `emitCompactingStatus` — the whole state machine, its four transitions, and the two values it already decodes and caps; `systemStatusLine` — the decode target that declares `CompactResult`/`CompactError` as plain strings and says why; `maxCompactField` — the 256-byte cap and its "the only cap in this file that bounds a LOG rather than an event" claim, which this ticket falsifies; `truncateField` — cuts rather than drops, and runs `strings.ToValidUTF8` on the cut; `emitUnrecognized` — the package's "never the content itself" rule and its #2227 exception paragraph, which this ticket falsifies in the other direction; `consumeLine`'s `result` arm — the SECOND producer of `Compacting{Active:false}`, which has no line to read fields off.
- `internal/turnevent/event.go` → `Compacting` — one bool today, and a doc whose #2227 correction is the paragraph the new fields hang off; `TurnEnd.ErrorCategory` — #2224's SECURITY paragraph, the precedent this ticket argues from and departs from.
- `internal/turnbridge/outbound.go` → `MapEvent`'s `turnevent.Compacting` arm (two fields today) and its `turnevent.TurnEnd` arm — the comment shape for "bounded at construction, not re-capped here".
- `internal/protocol/interactive.go` → `CompactingPayload` — the "banner-only (tui-driver streams no compaction progress)" claim, flagged stale by #2227's verifier and left; `TurnEndPayload` — the no-`omitempty` rule ("always emitting the key keeps the testdata fixture pinning the full shape") and `ErrorCategory`'s wire-spelling argument.
- `internal/protocol/interactive_test.go` → `roundTripEnvelope` — compares **canonical bytes** both ways, so a widened payload with no `omitempty` forces the existing rising-edge fixture to gain the new keys; `TestCompactingPayload_RoundTrip` — the test the new fixture's sibling mirrors.
- `internal/protocol/testdata/compacting.json` — the committed fixture is a **rising** edge, so the falling edge needs one of its own.
- `internal/streamsup/parser_compacting_test.go` → `compactingRun`, `compactingTrace`, `compactingEvents`, `compactingStartLine`/`compactingEndLine`/`compactingFailLine`, `TestParser_CompactingLogsClaudesFailureTextBounded` — the log record's existing bound assertion, which must keep passing **unchanged**: it is the evidence that this ticket changed which sink the values reach and not what bounds them.
- `internal/streamsup/compaction_capture_test.go` → `compactionPinnedShapes` (empty), `compactionReaderGate`, `TestRealClaudeCompactionCaptureShapesArePinned`, `TestCompactionFixtureReplayReachesBothEdges` — the two tests the opportunistic capture clause would arm, and the four-quadrant gate that decides whether they skip or fatal.
- `internal/turnbridge/outbound_test.go` → the `Compacting cleared` row of `TestMapEventOutbound` — the arm's existing coverage.
- `docs/protocol-mobile.md` § `compacting` — the PTY-derived, banner-only prose; § `turn_end` — how #2224 documented an open-set claude-authored string, its SECURITY paragraph and its changelog entry.
- `docs/knowledge/features/protocol-package-interactive-event-payloads.md`, `docs/knowledge/features/streamsup-package-system-maps-per-subtype-since-2026-08-07.md` — the package overviews for the two layers this ticket touches.

## Context

`protocol.CompactingPayload` carries `conversation_id` and `active` and nothing else. #2227 gave the frame a live producer — `streamsup.emitCompactingStatus` maps claude's `system/status` lines onto a rising/falling edge pair — and that producer **already decodes and caps** `compact_result` and `compact_error` off the closing line, then sends them to a Debug record the production daemon does not print. `emitUnrecognized`'s doc calls that record "the only diagnostic there is for a compaction that FAILED", because the event carries a bare boolean and a failed compaction is indistinguishable from a successful one everywhere a client can see.

This ticket moves those two already-decoded, already-bounded values from the log onto the frame. pyrycode-desktop#1240 renders "Compaction failed" in the error colour off exactly this.

Split from #2228. The trigger and the token counts arrive on a **different** claude line (`system/compact_boundary`), after this edge has already fallen; they are #2237's.

**No ADR is warranted.** The design decision this ticket makes — a claude-authored free-form string on an outbound interactive frame — is #2224's decision applied a second time, and the departure from it (§ Design, "The cut, not the drop") is a two-sentence consequence of the value's shape rather than a new position. The place it belongs is the declaration's own doc comment and § `compacting` of `docs/protocol-mobile.md`, both of which this ticket writes.

## Design

Four layers, one field pair, in the order #2224 threaded its own:

### 1. `internal/turnevent` — `Compacting` gains two fields

```go
type Compacting struct {
    Active    bool
    Result    string
    ErrorText string
}
```

**Names.** The Go names drop the `compact` prefix the wire keys keep, and the asymmetry is deliberate rather than sloppy: inside a type already named `Compacting` the prefix stutters, while on the wire a bare `error` beside `active` reads as *this frame is an error object* — the exact spelling `TurnEndPayload.ErrorCategory` refused, for the same reason. `ErrorText` rather than `Error` because a struct field named `Error` invites confusion with the `error` interface at every call site that touches it.

**Zero values are honest, and there are two ways to reach them.** `Active:true` carries neither field, by construction: the rising edge is emitted from an arm that has read only `Status`. And `consumeLine`'s turn-boundary reset — the second producer of a falling edge — emits both empty because it has no claude line to read them off; the daemon closed the edge itself and claude reported no outcome. Empty therefore means *absent, empty, or the daemon's own reset*, and no consumer needs to distinguish the three.

### 2. `internal/streamsup` — `emitCompactingStatus` fills them from values it already holds

The falling-edge arm already computes `result` and `detail` through `truncateField` at `maxCompactField` before logging them. The change is that the same two locals also reach the emit. The Debug record is **byte-identical** to today's: same message, same three attributes, same values, computed once and used twice. `TestParser_CompactingLogsClaudesFailureTextBounded` passes unchanged, which is the evidence that this ticket changed the sink and not the bound.

The state machine is untouched — four transitions, `compact_result` still not a discriminator, the falling edge still wide. A failed compaction closes the edge exactly as a successful one does; what changes is only that the frame now says which it was.

**The cut, not the drop.** `truncateField` cuts at 256 and reports that it cut; #2224's `maxTurnEndStopField` drops past its bound, so an over-long `error_category` arrives as `""`. This ticket keeps the cut, and the reason is the value's shape rather than inertia. `error_category` is a token set where a cut token would match no known value while looking like one, so a drop is the honest answer. `compact_error` is free-form prose, where a cut sentence still reads as what it is; and `compact_result` is a short token in every observed line, thirty times inside the cap, so it cannot realistically be cut at all. Keeping the cut also keeps AC 3 literally true — the bound stays the one the package already applies to these two values, unchanged in constant, call and semantics. **The cut is not reported on the wire**: a client cannot distinguish a cut value from a short one and needs no such distinction for prose. `truncateField` runs `strings.ToValidUTF8` on the cut, so a slice landing mid-rune cannot produce an invalid UTF-8 sequence on a JSON string field.

### 3. `internal/turnbridge` — `MapEvent`'s existing arm copies them across

Two more field copies in the `turnevent.Compacting` arm, uncapped here for the reason the `TurnEnd` arm states beside it: both strings are bounded at construction by their producer, and a second bound here would be a number to keep in step with one that already holds.

### 4. `internal/protocol` — `CompactingPayload` publishes them

```go
type CompactingPayload struct {
    ConversationID string `json:"conversation_id"`
    Active         bool   `json:"active"`
    Result         string `json:"compact_result"`
    ErrorText      string `json:"compact_error"`
}
```

**No `omitempty`**, per this file's standing rule as stated at `ToolResultPayload` and restated at `TurnEndPayload`: absence and the zero value mean the same thing, always emitting the key keeps the testdata fixture pinning the full shape, and a client built before this landed ignores the unknown keys. That last clause is AC 4, and it is a property of JSON decoding rather than of anything this ticket writes — but it is asserted anyway, because "obviously true" is how a wire compatibility claim goes false unwatched.

**Consequence for the existing fixture.** `roundTripEnvelope` compares canonical bytes in both directions, so the committed rising-edge `compacting.json` gains `"compact_result":""` and `"compact_error":""`. That is the same edit `turn_end.json` took at #2224 and it is not a wire break: an old decoder reads the two keys it knows and ignores the rest.

### The five written claims that go false

None of them fails a build, so each is corrected in the same commit as the code:

1. `emitCompactingStatus`'s "NOTHING FROM THE LINE REACHES THE EVENT" paragraph — now exactly two bounded strings do, and the wire-facing blast radius sentence is rewritten rather than deleted.
2. `maxCompactField`'s "THE ONLY CAP IN THIS FILE THAT BOUNDS A LOG RATHER THAN AN EVENT" — the subject dies; the no-rate-bound derivation below it survives verbatim and is kept, since the falling-edge-only argument is unchanged.
3. `emitUnrecognized`'s #2227 correction — "there the content does NOT cross the wire, so a log is the only place a failed compaction is visible at all" is now false in both halves. The package rule ("never the content itself") is restored intact: the log still carries a bounded copy, but it is no longer an exception justified by the wire's silence.
4. `systemStatusLine`'s "CompactResult and CompactError are declared for the LOG, not for the machine" — the *machine* half stays true and is load-bearing (nothing branches on either, which is what makes the falling edge a function of `Status` alone); the *log* half becomes "for the log and for the event".
5. `protocol.CompactingPayload`'s "banner-only (tui-driver streams no compaction progress)", and the same PTY-derived claim in § `compacting` of `docs/protocol-mobile.md`. Both were already stale — #1348 deleted the driver they rest on — and both are rewritten to the stream-json seam rather than patched.

## Concurrency model

No goroutines, no locks, no channels, no shutdown path. `Parser` is single-writer by construction: `emitCompactingStatus` runs on the parse goroutine that owns `p.compacting`, and the two new fields are locals on that goroutine copied into a value struct. `MapEvent` is pure. Nothing this ticket adds is shared.

## Error handling

No new failure mode and no new branch. The three inputs the AC names all reach the same arm:

- **absent** — `encoding/json` leaves the string zero; the edge fires.
- **empty** — decoded as `""`; the edge fires.
- **oversized** — `truncateField` cuts to 256 and the edge fires.
- **wrong JSON type** (e.g. `compact_error` as an object) — fails the whole line decode and takes the existing undecodable path, which consumes the line and touches nothing. Unchanged, and deliberately: a status this parser cannot read is not evidence that compaction ended.

## Testing strategy

Hermetic throughout. Rows go beside #2227's, whose line literals are transcribed rather than captured.

**`internal/streamsup/parser_compacting_test.go`** — the existing `compactingTrace` projection collapses a `Compacting` to one token, so the field assertions use `compactingEvents`, which returns the events themselves. Scenarios:

- The falling edge carries claude's `compact_result` and `compact_error` (AC 1) — fed `compactingFailLine`, both values assert exactly.
- The rising edge carries neither (AC 2) — and **non-vacuously**: the rising line fed carries `compact_result`/`compact_error` keys of its own, so a rising arm that started copying them fails here. A row against `compactingStartLine`, which has no such keys, could not fail.
- Absent, empty and oversized each still fire the falling edge and land the expected value (AC 3), as a table.
- The turn-boundary reset's falling edge carries both empty — the second producer, whose zero values are a claim about a line that does not exist.
- `TestParser_CompactingLogsClaudesFailureTextBounded` and `TestParser_CompactingRisingEdgeLogsNothingFromTheLine` are **not** modified. Their passing unchanged is the evidence for AC 3's "the byte cap the package already applies to them".

**`internal/turnbridge/outbound_test.go`** — one row for a falling edge carrying both fields, asserting they reach the payload; the existing `Compacting cleared` row keeps its event and gains an accurate name.

**`internal/protocol`** — a new falling-edge fixture `compacting_ended.json`, a round-trip test mirroring `TestCompactingPayload_RoundTrip`, and AC 4's own assertion: the same fixture decoded into a local two-field struct that knows only `conversation_id` and `active`, asserting both read exactly as they read from the old fixture. The rising-edge `compacting.json` gains the two empty keys.

**Not run here:** `make check` and the full-module race suite are the verifier's gate. This ticket's own gate is `go test -race` over `internal/streamsup`, `internal/turnevent`, `internal/turnbridge`, `internal/protocol` and `cmd/pyry`, plus `go vet ./...` and a `cmd/pyry` build.

**No `needs-real-claude`.** The closing line's bytes are observed verbatim and the arm that reads them runs on them today; what changes is which sink two already-decoded strings reach. Nothing makes a compaction fail on demand, so the failure arm cannot be provoked live at all and is proved against a mutated line.

## Open questions

1. **Is `compact_error` the same security class as #2224's `error_category`?** Resolved in § Security review below: same class of *provenance*, worse class of *shape*, and the difference is documented rather than mitigated differently.
2. **Does the opportunistic capture commit land?** Deliberately not an acceptance criterion. #2229's probe records survive at `$TMPDIR/pyry-2229-capture-*/ccap-record.json` (three, ~17 KB each, verified present 2026-09-08). Committing one as `internal/e2e/realclaude/testdata/compaction_v2.1.259.json` and filling `compactionPinnedShapes` from the record's own `compaction_shapes` would arm two tests that skip today. It is attempted only after the four ACs are green and committed, it is gated on the credential scan in § Security review, and **if arming either test reddens for any reason beyond the empty pin, the fixture is dropped from the commit and said so in a ticket comment**. It must not spend this ticket's budget.

## Revisions

*(none yet — appended in Phase B if the design moves)*

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries] No findings, but a class change worth naming.** The boundary is single and explicit and this ticket does not move it: `emitCompactingStatus`'s `json.Unmarshal` into `systemStatusLine` is where claude's stdout becomes daemon state, and everything downstream (`MapEvent`, `CompactingPayload`) holds parsed, bounded strings it does not re-derive. What *does* change is the payload's composition: `CompactingPayload` today carries a daemon-supplied `conversation_id` and a daemon-computed `active` and **not one byte claude wrote**, and after this ticket it carries two claude-authored strings beside them. `TurnEndPayload` never had to make that jump — it already carried three. The type system gives no untrusted-data signal here and none is proposed (it would be a new convention for one frame); the signal this package uses is the SECURITY paragraph on the declaration, which the plan writes at both `turnevent.Compacting` and `protocol.CompactingPayload`.
- **[Error messages, logs, telemetry] SHOULD FIX — the prose hazard must be written down, not inherited.** #2224's SECURITY paragraph is the right precedent for provenance (claude-authored, bounded by the daemon, **not** sanitized, the render boundary owing control-character and terminal-escape stripping being the client's) and the plan takes it. It is the wrong precedent for shape, and copying it verbatim would be the finding. `error_category` is an open set of short category tokens; `compact_error` is free-form prose of arbitrary content — newlines, ANSI escapes, markdown, a URL, or text impersonating daemon chrome all fit inside 256 bytes. Phase B must therefore state at the declaration and in § `compacting` of `docs/protocol-mobile.md` that (a) it is prose rather than a token, (b) it must be rendered as inert text and never fed to an HTML sink, an attribute or a URL — `UnrecognizedMessagePayload.Raw`'s rule, which is the closer neighbour on shape — and (c) it must be attributed to claude, never shown as the daemon's own finding. `compact_result` takes the open-set rule instead: an unrecognised token is *unknown*, not an error, and a value at exactly the cap may be a cut one.
- **[Threat model alignment] No findings — no new disclosure class.** § Security model's threat 1 (model-authored text reaching a client) lands in the outward direction here exactly as it does for `turn_end`'s three strings and `attachment_offered`'s filename. That the text could quote transcript content is not a new exposure: the same paired, E2E-encrypted client already receives the entire assistant stream over `assistant_delta`, so 256 bytes of claude's compaction error discloses nothing it cannot already read. This is the argument that keeps the finding above at SHOULD FIX rather than MUST FIX, and it is stated because a reader who did not check would have to assume it.
- **[Network & I/O] No findings.** Worst case 512 bytes added to one frame — ~0.8% of the 65519-byte application-envelope cap — and the rate is bounded by the same derivation `maxCompactField` already carries: the falling edge fires at most once per completed compaction, because an edge can only fall from one that rose and the rising edge is idempotent while open. Plus at most one per turn boundary from `consumeLine`'s reset, which carries both fields empty. No new socket read, no new cap needed.
- **[Concurrency] No findings.** Single-writer by construction — see § Concurrency model. No lock, no shared state, no goroutine.
- **[File operations] SHOULD FIX, and it gates the optional work.** The only file this ticket could write outside the four production files and their tests is the opportunistic capture fixture, ~17 KB of **real claude transcript bytes** into a public repository. The record was taken with `ANTHROPIC_API_KEY` unset, so it is **unscanned rather than proven clean** — the ticket says so explicitly. Phase B must, before `git add`, read the record's `redaction` and `credential_scan_*` fields and hand-scan the payloads for credential-shaped strings (`sk-`, `ANTHROPIC_`, bearer tokens, absolute home paths). **If the scan cannot be performed or is not clean, the fixture is dropped** and the pin stays empty; the two tests that would have armed keep skipping on the one quadrant `compactionReaderGate` already permits. Dropping costs nothing this ticket owes.
- **[Tokens, secrets, credentials] Not applicable** — no token is generated, stored, compared or logged. The one credential-adjacent question is the capture scan above, filed under File operations.
- **[Subprocess / external command execution] Not applicable** — no `exec.Command` on any path this ticket touches. The claude child is already running; this code reads its stdout and nothing else.
- **[Cryptographic primitives] Not applicable** — no randomness, no comparison against a secret, no key material. The frame's confidentiality is the Noise_IK session's, unchanged.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-08
