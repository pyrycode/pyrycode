# #2234 — recover a denial the `result` line reports but no `permission_denied` line announced

## Files read

- `internal/streamsup/parser.go` → `Parser` (the three cross-line-state paragraphs above it), `consumeLine`'s `result` arm, `emitPermissionDenied`, `systemPermissionDeniedLine`, `resultLine`, `resultStopLine`, `assistantErrorLine`, `decodeModelWindows`, `decodeStopShape`, `boundStopField`, `emitBackgroundTaskRoster` — the three independent-unmarshal precedents, the one reset point, the roster's count bound, and the mapping this slice extends.
- `internal/turnevent/event.go` → `ToolCallDenied` — the event already exists; its field docs settle drop-vs-cut per field and state that the id is the client's join key.
- `internal/streamsup/permission_denial_capture_test.go` → `capturedDenialLines`, `replayDenialCapture`, `denialCapturePinnedCounts`, `TestParser_DenialCaptureMapsEveryCapturedLine` — the fifth-reader ban, and the pinned counts that become AC 2's real-bytes proof.
- `internal/streamsup/parser_permission_denied_test.go` → `denialLineFixture`, `oneDenial` — the hand-built-line conventions AC 1 is proven under, and the file's own rule that nothing hand-built may assert on an invented shape.
- `docs/knowledge/features/streamsup-package-result-stop-shape-second-decode-target-and-dr.md` — why a second target rather than a widened one; failure isolation, not tidiness.
- `internal/e2e/realclaude/testdata/bypass_reescalation_v2.1.239_*.json`, `bypass_approval_argv_v2.1.239_*.json` — the measured entry shape and the measured absence of the line.

## Context

`internal/e2e/realclaude/testdata` measurement, re-derived at `6e6f926e`: every `permission_denials` entry in every committed capture carries exactly three keys — `tool_name`, `tool_use_id`, `tool_input` — and the longest observed array is **one entry**. The five `bypass_approval_argv` arms, launched the way `cmd/pyry/mcp_config.go` launches claude in production, report **9 denials in `result` and 0 `permission_denied` lines**. So on the daemon's own posture #2232's mapping reports nothing at all for a blocked call.

`resultLine` declares only `modelUsage`, so `permission_denials` is not decoded today. This slice adds the fourth independent unmarshal off the `result` line and the parser's fourth piece of cross-line state.

No ADR is warranted: this is the fourth application of an argument `resultStopLine` already records.

## Design

### A fourth decode target

`resultDenialsLine` and `resultDenialEntry`, siblings of `resultLine` and `resultStopLine` rather than fields on either. The isolation is AC 3 verbatim and runs three ways: a hostile `modelUsage` cannot suppress the denials, a hostile `permission_denials` cannot erase `terminal_reason` or the windows, and neither can reach segmentation, which comes from the already-decoded `streamLine`.

```go
type resultDenialsLine struct { PermissionDenials []resultDenialEntry `json:"permission_denials"` }
type resultDenialEntry struct { ToolName, ToolUseID string /* json: tool_name, tool_use_id */ }
```

`tool_input` is **not declared**. It is the tool's full input — a shell command in every captured entry — the client already holds it from the `tool_use` frame for the same id, and absence from the decode target is the structural guarantee `systemPermissionDeniedLine` states for `session_id` and `uuid`. Both fields are plain strings, so a non-string value fails the whole decode and takes the nothing-recovered path: fail-closed, `emitPermissionDenied`'s rule.

### The fourth piece of cross-line state

`Parser.deniedThisTurn map[string]struct{}` — the bounded set of `tool_use_id`s that already produced a marker from their own line this turn. `nil` until the first denial, so a session that never denies allocates nothing.

Written at exactly one site (`emitPermissionDenied`, recording the **bounded** id it just published, and only when that id is non-empty) and read-and-cleared at exactly one site (`consumeLine`'s `result` arm), which is the boundary #1385 established and #2224 and #2227 reused. No second boundary is minted.

**The residual fails the opposite way from its three neighbours, and that is argued rather than inherited.** #1385's counter costs an early event; #2224's category costs a wrong claim, answered by a per-line write; #2227's edge costs a banner outliving its compaction. A stale id costs neither — it **suppresses** a later turn's genuine marker. Three things bound it. The set is cleared unconditionally at the one boundary, so only a child that dies without a `result` can leave one. claude's `tool_use_id`s are per-call unique (`toolu_01…` in all sixteen captured entries), so a later turn colliding with a stale id is not a shape claude produces. And a collision would suppress a marker for an id the client has **already seen a denial for**, so the loss is a second report of one call, never the only report of a call. No laundering write exists here and none is invented: #2224's answer does not transfer, because no line writes this field on the way past.

### `maxTurnDenials` — one constant, two dimensions

Caps both how many ids are remembered and how many entries are read, on `maxTaskFieldID`'s one-constant-over-several-fields precedent. Value 16 against a longest observation of one entry per turn and one line per turn — a wider multiple than `maxTaskRosterEntries`' 8, because a turn genuinely may deny several calls and the retention is cheap: 16 ids at `maxTaskFieldID` is 4 KiB of worst-case parser state.

**A FULL SET SUPPRESSES RECOVERY ENTIRELY**, and the direction is deliberate. Past the cap the set no longer proves it holds every id, so recovering would risk a duplicate marker for a call already reported. The case can only arise where lines DO arrive — in the posture that motivates this ticket the set is empty, not full — so what is given up is a recovery that posture never needs, and what is bought is that "one marker per denied call" holds for every input.

### `emitRecoveredDenials(line []byte, denied map[string]struct{})`

Called from the `result` arm, **before** the `TurnEnd` emit (AC 1: a client that closes the turn on that event has already seen them) and below the state resets, so no decode outcome can reorder, duplicate or suppress the boundary — `decodeModelWindows`' rule, held for an arm that emits rather than returns.

Per entry, in claude's array order, truncated from the tail:

1. id empty, or longer than `maxTaskFieldID` → **dropped**, counted. The id is the client's join key (`ToolCallDenied.ToolCallID`) and this marker carries no prose at all, so an unattributable one is strictly worse than none.
2. id present in `denied` → **suppressed**, not counted (AC 2 — this is the correct outcome, not a loss).
3. otherwise → one `ToolCallDenied` with `ToolName` dropped-if-over-cap and reported in `DroppedFields` exactly as `emitPermissionDenied` does, `ToolCallID` the id, and `Message` / `DecisionReasonType` / `DecisionReason` **empty — none synthesized**, `TruncatedFields` nil.

The cut is reported by a Debug naming one integer: entries beyond the cap plus entries with no usable id, logged only when non-zero. `emitBackgroundTaskRoster` refuses to log its entry count, but that refusal is about the **undecodable** path where nothing decoded; its actual cut count is reported on the event. There is no event here to carry one and widening `turnevent` is out of scope, so the remaining channel is the same class as the oversized-partial-line drop's `"bytes", len(rest)` — a length of input the daemon refused, never content of it. No claude-authored byte is logged on any path, and the decode error is discarded rather than logged for `decodeModelWindows`' stated reason.

**A recovered marker and a line-derived one are not structurally distinguishable** — a line-derived marker whose claude prose was absent looks identical. Every captured line carries `message`, so the two are tellable apart in practice, and the ticket accepts that in exchange for not adding a provenance field across `turnevent` and `protocol`. Recorded here so the trade is visible rather than assumed.

## Concurrency model

No goroutines. The new field is covered by `Parser`'s single-writer invariant exactly as the three before it, including across a child respawn; a future concurrent reader guards all four fields, not three.

## Error handling

Every failure returns a value, never an error: an undecodable `permission_denials`, an absent key, `null`, `[]`, and an array of hostile element shapes all recover nothing and disturb nothing. The `result` line's boundary, reason, windows and stop shape are unaffected on every one of those paths.

## Testing strategy

New `internal/streamsup/result_denial_recovery_test.go`, hand-built lines only, under this package's existing division of labour:

- AC 1 — a `result` whose array names an unannounced id yields exactly one marker, carrying the name and id, empty on all three prose fields; and the marker precedes the `TurnEnd` in the emitted order.
- AC 2 — a `permission_denied` line followed by a `result` naming the same id yields exactly one marker (the line's), with claude's prose intact.
- AC 3 — three rows: a hostile `modelUsage` beside a valid denials array still recovers; a hostile denials array still yields the turn's `TurnEnd` with its `terminal_reason`, `is_error` and windows; neither shape changes the count of `TurnEnd` events.
- AC 4 — two turns through one parser: turn one's id is announced and suppressed; turn two names the **same** id with no line and DOES produce a marker, which is the residual's direction made executable.
- Bounds — an over-cap array reports its cut; an entry with an empty or over-cap id is dropped; an over-cap `tool_name` drops and reports; a full set suppresses recovery.

Existing `TestParser_DenialCaptureMapsEveryCapturedLine` is AC 2 against **real bytes** at no cost: its four arms carry both a line and a `result` entry for every denial, and its counts are strict equalities, so a recovery that fired there reddens it. One added assertion in `permission_denial_capture_test.go` names that property instead of leaving it incidental. No fifth capture reader is written — out of scope per the ticket.

## Open questions

- Whether `maxTurnDenials` should differ from `maxTaskRosterEntries`' 8. Resolved in Design above at 16, on the retention cost; revisit only if a capture ever shows a turn denying more.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No MUST FIX. The boundary is explicit and single: claude's stdout bytes enter through `emitRecoveredDenials`' own `json.Unmarshal` into `resultDenialsLine`, and every value published is bounded at construction before it leaves. The decode's input is the **top-level** `line` bytes handed down by `consumeLine`'s `result` arm — never a nested field — so `streamLine`'s stated property holds: a tool result whose text is literally a `result` line cannot forge a denial, because nested content is never re-scanned. Two things this deliberately does not do, both already argued at `ToolCallDenied`: the id is not verified against a tool call the parser saw (that needs state this mapping refuses to hold), and nothing in the daemon acts on any field — no retry, no routing, no teardown — so a fabricated entry stays a misleading label rather than an actuator.
- **[Tokens, secrets, credentials]** Not applicable by design decision: the entry shape declares two fields and neither is a credential. `session_id` and `uuid` are absent from the decode target, as `systemPermissionDeniedLine` already refuses them, and `tool_input` — the one field of the three that could carry an operator's typed command line — is refused for exactly that reason.
- **[File operations]** Not applicable: no path is constructed, opened, or compared on any path added here.
- **[Subprocess execution]** Not applicable: nothing added reaches `exec`. Worth stating rather than skipping, because `tool_input` in every captured entry **is** a shell command; not decoding it is what keeps it out of reach of any future reader.
- **[Cryptographic primitives]** Not applicable: no randomness, no comparison against a secret. The one comparison added is a map lookup of claude's id against ids claude itself sent this turn, neither of which is a secret.
- **[Network & I/O — resource exhaustion]** SHOULD FIX, addressed in the design rather than deferred. An array length claude controls is not a length to trust: without a cap, one `result` line could mint unbounded markers into the event stream and an unbounded id set into parser state. `maxTurnDenials` bounds both dimensions, retention is 4 KiB worst case, and per-value size is bounded by `maxTaskFieldID` before anything is retained or published. Phase B must not publish an id it did not bound first — that is the check the verifier should look for.
- **[Error messages, logs, telemetry]** No MUST FIX. Nothing claude-authored is logged on any path: the decode error is **discarded** rather than logged, because `encoding/json` quotes offending input into its error text, which would route claude's bytes to a log through a channel no per-attribute check sees. The one Debug added carries a single daemon-computed integer and no decoded value. The absence of prose on a recovered marker is a genuine leak-reduction rather than a gap — the recovery reports the fact of the denial and nothing claude said about it.
- **[Concurrency]** No findings. No lock is taken and none is needed: `Parser`'s single-writer invariant covers the new field exactly as it covers the three before it, os/exec drives `Write` from one goroutine, and `cmd.Wait` is the happens-before edge across a respawn. No goroutine is spawned, so none can leak.
- **[Threat model alignment]** The relevant threat is a compromised or buggy `claude` child, which the daemon already treats as untrusted input — `ToolCallDenied`'s doc states the residual: a mistaken entry can name a call that actually ran and succeeded, and a client that trusts it renders a successful call as blocked. This slice **widens** that exposure, because a `result` entry can now produce a marker with no line to corroborate it, and the widening is accepted on the ticket's own ground: the join is the client's, an id matching no call it saw renders unattributed, and nothing in the daemon is keyed on any field. Out of scope and named as such: verifying an id against a tool call the daemon observed, which needs per-call parser state and is nobody's ticket today.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-08
