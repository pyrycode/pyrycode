# #2267 — map claude's `model_refusal_fallback` line to a bounded daemon event

## Files read

- `internal/streamsup/parser.go` → `emitSystemSubtype` — the switch this ticket adds a ninth arm to; its case arms are the ONE enumeration of the mapped set.
- `internal/streamsup/parser.go` → `emitPermissionDenied`, `systemPermissionDeniedLine` — the pattern the ticket points at, two weeks old and on this code path: decode target declaring exactly the carried keys, two local bounding closures, no gate, undecodable → Debug + no event.
- `internal/streamsup/parser.go` → `consumePermissionDeniedLine` — the recovery entry point in `consumeLine`'s decode-failure branch. Read to establish it must NOT be copied here; see Design § "No recovery path".
- `internal/streamsup/parser.go` → `maxTaskFieldID`, `maxDenialProse`, `truncateField` — the two caps this arm reuses and the scrub-and-cut helper behind the `cut` answer.
- `internal/streamsup/parser.go` → `streamLine` — the line-level segmentation struct declaring `type`, `subtype`, `message` only. Its `Message *streamMessage` typing is what made #2232's arm unreachable; the reachability argument below turns on it.
- `internal/turnevent/event.go` → `ToolCallDenied` — the variant shape being mirrored: cut-or-drop per field, `TruncatedFields` + `DroppedFields`, and the "nothing in the daemon acts on any field" trust paragraph.
- `internal/turnevent/event.go` → `ModelAnnounced` — the `Model*` naming family this event joins, and the variant whose value silently changes after a `session`-scoped swap. Its `Model` doc states the verbatim-carry rule the two model labels here inherit.
- `internal/turnevent/event.go` → `isTurnEvent` marker block — declaring the marker is what enrols a variant in the totality guard, which is this change's real blast radius.
- `cmd/pyry/stream_turn_busy_test.go` → `TestTurnMarkFor_TotalOverEveryVariant`, `turnEventVariants` — the marker-derived totality guard that reddens on its own when a variant is added.
- `internal/streamsup/parser_permission_denied_test.go` → `denialLineFixture`, `oneDenial`, `TestParser_PermissionDeniedBoundsEveryClaudeString` — the hermetic test shape, including its statement that hand-built rows may not assert on an invented shape.
- `docs/knowledge/features/streamsup-package-system-maps-per-subtype-since-2026-08-07.md` — the family's own account. Two lessons bind this ticket: the mapped set is enumerated only at `emitSystemSubtype`, and a single wrongly-typed key on `streamLine` fails the WHOLE top-level decode and can leave a correct arm unreachable.
- `docs/specs/architecture/2232-permission-denied-bounded-daemon-event.md` — the analogue's plan, including the Revision recording that its `cmd/` blast radius was missed by a "2 production files" estimate.

## Context

When a turn ends with stop reason `refusal`, claude retries once on a fallback model and announces it with a `system/model_refusal_fallback` line. `emitSystemSubtype` has no arm for that subtype, so the line falls to the `ignoredLineTypes` branch's silent drop. A client then sees the model label change on the next turn's `ModelAnnounced` with nothing explaining it — a silent model swap. This slice maps the line to a daemon turn event. Nothing reaches the wire here; the protocol frame and the `turnbridge` mapping are #2265's.

No ADR is warranted: this is the ninth arm on an enumeration whose design ADR already exists, and it introduces no new mechanism — only a new member of a family `ToolCallDenied` already established.

**The field set is documentation-derived, not capture-derived**, read from the Claude Code headless docs and `@anthropic-ai/claude-agent-sdk@0.3.263`'s `sdk.d.ts` against a daemon on claude 2.1.259. No capture of the line exists and none can be taken without a prompt this repo should not contain. Every consequence of that provenance is stated where it bites, rather than once here.

## Design

### The event — `turnevent.ModelRefusalFallback` (in `event.go`, beside `ToolCallDenied`)

**The name is the daemon's.** It joins the `Model*` family — `ModelAnnounced`, `ModelList`, `ModelWindow` — because what the event reports is a change of the model claude is running, which is that family's subject. It names both halves of the fact, a refusal and a fallback, and deliberately does not shorten to `ModelSwapped`: a `local`-scoped fallback is a one-turn retry, not a swap, so the shorter name would over-claim on half the observed set. It restates claude's subtype closely, and that is fine here in a way it would not be for a field: the subtype IS this event's identity, which is exactly why `trigger` and `direction` are not carried.

| Field | Claude's key | Overflow | Bound |
|---|---|---|---|
| `Scope` | `scope` | **drop** | `maxTaskFieldID` |
| `OriginalModel` | `original_model` | **drop** | `maxTaskFieldID` |
| `FallbackModel` | `fallback_model` | **drop** | `maxTaskFieldID` |
| `RefusalCategory` | `api_refusal_category` | **drop** | `maxTaskFieldID` |
| `RefusalExplanation` | `api_refusal_explanation` | **cut** | `maxDenialProse` |
| `Banner` | `content` | **cut** | `maxDenialProse` |
| `TruncatedFields []string` | — | — | daemon names of the fields CUT, declaration order, nil when none |
| `DroppedFields []string` | — | — | daemon names of the fields DROPPED, declaration order, nil when none |

The reports name the DAEMON's fields, `ToolCallDenied.DroppedFields`' stated rule: `refusal_category`, not claude's `api_refusal_category`; `banner`, not `content`. The `api_` prefix is API-side vocabulary the daemon does not adopt.

**Five keys are deliberately not fields, and the control is their absence from the decode target rather than a scrub** — `systemTaskUpdatedLine`'s rule, which `systemPermissionDeniedLine` already applies to `session_id` and `uuid`. `trigger` and `direction` are constants restating the subtype. `request_id` is an API-side identifier nothing in the daemon reads. `retracted_message_uuids` and `refused_user_message_uuid` name claude's message identity, which no daemon surface can join against — `assistant_delta` carries `turn_id` and `seq`. Retraction of the refused partial therefore cannot be honoured on this wire, and that is a stated limit carried on the type, not something to solve here.

**Cut-or-drop is decided per field on the family's existing boundary.** `Scope` and `RefusalCategory` are tokens a client MATCHES against a set, and the two model labels are tokens a client matches or joins against a `ModelAnnounced` it already holds — a cut token matches nothing while still looking like one, which is `maxTurnEndStopField`'s and `ModelWindow.ModelID`'s answer. The two prose fields are `maxCompactField`'s case: a cut sentence still reads as prose.

**`Scope` is the field that carries the consequence**, and its doc says so: `session` means claude keeps the session on the fallback, so a later `ModelAnnounced` will report a different model with no other explanation; `local` means the retry was for one turn. It is carried verbatim as an open set on `TurnEnd.Outcome`'s rule, because a documented two-value set is still claude's to extend.

The two model labels are carried VERBATIM on `ModelAnnounced.Model`'s stated rule — no lowercasing, no alias expansion, no date-stamping, no lookup against a published list — and that doc's caveat is inherited too: bounded and UTF-8-valid is all they are.

### The parser arm — `emitModelRefusalFallback`

Signature `(line []byte) bool`, reporting that it consumed the line either way, reached from a new `case "model_refusal_fallback"` in `emitSystemSubtype`. Decode target `systemModelRefusalFallbackLine`, six string fields and nothing else declared.

**It gates on nothing**, `emitPermissionDenied`'s posture and on its ground: the SUBTYPE is the payload, since nothing else on this surface explains a model change, so a field-less line is still news; and a gate would re-drop the line silently the first time claude renames a key, restoring the very defect the ticket exists to end. AC 2 is that decision.

**Two consuming paths**, `emitPermissionDenied`'s verbatim:

- undecodable (a numeric `scope`, say) → `Debug` naming the subtype keyword only, no event, line still reported consumed. One wrong-typed value fails the whole decode target and that is the deliberate answer, not per-field tolerance: the failure has not been observed, and a capture is what would justify building against it. AC 3.
- decodable → exactly one event, whatever the six fields hold, including all-empty.

**Nothing is logged on the emitting path.** `Banner` and `RefusalExplanation` are claude-authored prose about a refused request and may quote the user's own words back; they are the fields a drop site would be most tempted to explain itself with, and they must never reach a log.

**No recovery path in `consumeLine`'s decode-failure branch, and that is the ticket's instruction rather than an omission.** `streamLine` declares `type`, `subtype` and `message`, and the documented field set carries no `message` key at all, so the line decodes cleanly and reaches the subtype dispatch. That is the opposite of `permission_denied`, whose string `message` collided with `streamLine.Message *streamMessage` and forced `consumePermissionDeniedLine`. **Because the field set is documentation-derived, this is a prediction and not a measurement:** should the real line carry a `message` key of any scalar type, the whole line fails `consumeLine`'s decode and this mapping is unreachable in production. Building the recovery gate speculatively is refused anyway — it can only take a line away from the surfaced tier, so its safety is entirely in how little it matches, and a gate matching a shape nobody has seen is unbounded in exactly the wrong direction. The prediction is recorded at the arm so that whoever sees the first real line knows which half broke.

### The bounds — no new constant

`maxTaskFieldID` caps the four token fields; `maxDenialProse` caps the two prose ones. Both are reused rather than minted, per the ticket: they cap the same two shapes for the same reasons, and a second constant of the same value bounding the same shape would be a number to keep in step for nothing. Each gains an `AMENDED` paragraph naming this event, because both docs enumerate the fields they cap and both go stale on the reuse.

`maxDenialProse`'s doc carries the family's envelope arithmetic and claims ONE worst-case number a reader can hold. This event breaks that by one field: 4×256 + 2×2048 = 5120 bytes against `ToolCallDenied`'s 4864, or 7.8% of the 65519-byte v2 application-envelope cap. The amendment states the new number rather than leaving the old sentence to be read as covering it. Nothing reaches the wire in this slice, so it is the budget #2265 inherits.

## Concurrency model

None. The arm reads and writes no `Parser` field — not `p.compacting`, not `p.deniedThisTurn`, not the accumulator — so it is `emitCompactionBoundary`'s stateless shape and no turn-boundary reset has anything of its to reset. No goroutine, no channel, no lock. `p.emit` is the existing single-threaded sink.

## Error handling

One failure mode: the line will not decode into the shape. It consumes the line with a content-free `Debug` and emits nothing, never an `Unrecognized` — keeping `system` whole on `ignoredLineTypes` is what makes "no system line reaches the unrecognized lane" structural, and that outranks surfacing a malformed line of a known subtype. The subtype string logged is a message-name keyword this package owns, not payload.

## Testing strategy

**Hermetic parser tests** — new `internal/streamsup/parser_model_refusal_fallback_test.go`, mirroring `parser_permission_denied_test.go`'s shape with its own fixture builder and single-event helper:

- **Every hand-built line is labelled documentation-derived at the top of the file and at the fixture builder**, naming the doc sources and the fact that no capture exists. #1419 had to repair two shipped fixtures whose hand-built shape had quietly become a claim about observed bytes; this file must not become a third. The label states what the rows may assert — the arm's behaviour given a shape — and what they may not: anything about what claude actually sends.
- Table-driven over-cap rows, one per claude-derived string, asserting the four token fields land empty and named in `DroppedFields` while the two prose fields land at the cap and named in `TruncatedFields`; an all-six-over row pinning both reports' declaration order; and an at-cap row requiring both reports nil, which is what stops the table passing on a `>=` where the arm has `>` (AC 4).
- A full line carrying all six keys, proving each reaches its field under the daemon's name (AC 1).
- Absence rows: a line missing some keys and a line carrying NONE of the six still emit, with each absent field empty and named in neither report — the distinction between "claude sent nothing" and "the daemon emptied it", which is the only thing two report slices buy (AC 2).
- An undecodable line emits nothing and is still consumed (AC 3).
- A leak row: a line also carrying `request_id`, `refused_user_message_uuid` and `retracted_message_uuids` yields an event no field of which contains any of those values — structural, since none is declared on the decode target.

**Totality guard** — one row for `turnevent.ModelRefusalFallback` in `TestTurnMarkFor_TotalOverEveryVariant`'s table in `cmd/pyry/stream_turn_busy_test.go`, answering `turnMarkNone`, with the reasoning recorded at the row. No production change: `turnMarkFor`'s opener set is a whitelist and its `default` already answers `turnMarkNone`. Declaring `isTurnEvent` is what enrols the variant, so the guard goes one row short on its own — #2232's plan missed exactly this and paid for it in a RED run.

**No live proof, and none is attempted.** A refusal cannot be provoked, so the live suite can never reach this arm; the ticket deliberately carries no `needs-real-claude`. Nothing under `internal/e2e/realclaude` is edited, so no `e2e_realclaude` compile is owed — but `go vet -tags e2e_realclaude ./internal/e2e/realclaude/` is run anyway, because it is free and the standard gate never compiles that package.

**No comment sweep.** #2232 removed the arm COUNTS from the prose blocks in `internal/e2e/realclaude` and `internal/streamsup` for precisely this reason, so what is left describes the set without a number to correct. Verified by re-reading those blocks rather than assumed.

## Open questions

1. Whether the real line's keys match the documented set at all — chiefly whether it carries a `message` key, which would make this arm unreachable in production exactly as #2232's was. Unanswerable without a capture that cannot be taken. Resolved by recording the prediction at the arm rather than by hedging the code.
2. Whether `scope` is genuinely a closed two-value set. Not chased: the decode and the carry are tolerant either way, so an answer would change documentation and no code.
3. Whether the two local bounding closures should be lifted into one shared helper now that a second arm needs both answers. Resolved in favour of keeping them local: `emitBackgroundTaskStarted`'s `bound` and `emitPermissionDenied`'s pair are already this file's established shape, each closure carries its own doc explaining which of the two answers it is, and extracting would edit a shipped arm for no behavioural gain. Revisit on a third.
4. The per-session model the pool holds in its settings goes stale after a `session`-scoped swap. Explicitly out of scope per the ticket; a separate deliverable, and nothing here touches it.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** Finding, and it is NOT `ToolCallDenied`'s restated. The boundary itself is structural and clean: claude's stdout → daemon event at `emitModelRefusalFallback`, which decodes the TOP-LEVEL line bytes only, reached from `emitSystemSubtype` inside `consumeLine`'s branch that already gated on the top-level `type`, so a tool result whose text is literally a `model_refusal_fallback` line cannot forge one — `streamLine`'s doc states that control shapes are read from the top level and nested content is never re-scanned. What is new is the CLAIM the event carries. This is an announcement about which model claude will run NEXT, and `ModelAnnounced` is the daemon's authoritative statement of what it IS running. A consumer that updated its model state from `FallbackModel` rather than waiting for the following `ModelAnnounced` would be trusting a prediction as an observation, and a fabricated or simply mistaken line could then park a client on a model label claude never adopted. The daemon does not verify the labels against anything and deliberately will not — there is nothing on this surface to verify them against. What bounds the damage is that NOTHING IN THE DAEMON ACTS ON ANY FIELD: no routing, no retry, no model selection and no session setting is keyed on them, so a fabricated value is a misleading label rather than an actuator. **Phase B (MUST STATE):** carry that sentence on the type, and say explicitly that this event does not replace `ModelAnnounced` — a consumer reads the swap's CAUSE here and its RESULT there.
- **[Tokens, secrets, credentials]** Finding, sharper than the analogue's and bounded by the same three decisions. No secret is generated, stored or compared here. But `api_refusal_explanation` and `content` are claude's prose about a request that was REFUSED, so unlike `ToolCallDenied.Message` — which describes a tool call the daemon made — they can quote or paraphrase the user's own words back out, and `retracted_message_uuids` is claude telling us it withdrew a partial response about the same subject. Three things bound it: both values are capped at construction (`maxDenialProse`), neither is EVER logged (the arm logs nothing on the emitting path), and nothing reaches the wire in this slice. The content also flows back to the principal who authored it, not across a trust boundary. **Phase B (MUST):** no `slog` call in this arm may take a decoded field as a value.
- **[Trust boundaries, second — misattribution]** Finding, stated because the category strings are accusatory in a way no sibling variant's fields are. `RefusalCategory` is an open string carrying values like `cyber` or `bio`, and the event asserts that the user's request drew that classification. A fabricated or mistaken line therefore attributes a refusal category to a user who triggered none. This is `ToolCallDenied`'s "renders a successful call as blocked" one degree worse, and it has the same answer — nothing in the daemon acts on it, the rendering is the client's — but it is not covered by that variant's doc, so it needs its own. **Phase B (MUST STATE):** say at the field that the classification is claude's assertion about the request, never the daemon's finding.
- **[File operations]** No finding, and by a stronger absence than #2232's. There is no production file operation, and — unlike the analogue — no capture reader either, because no capture of this line exists. Nothing in this change opens, stats or names a path, so there is no traversal surface to canonicalise, no check-then-open gap and no file mode to specify.
- **[Subprocess / external command execution]** Finding, and it is the consumer's to honour. Nothing here executes anything, but a `cyber`-category refusal's explanation or banner can quote the command line or code the model refused to produce, so a consumer that re-shells either executes exactly what claude declined. `ToolCallDenied.Message` and `BackgroundTaskStarted.Description` carry the identical hazard and state the rule at the field. **Phase B (MUST STATE):** carry the same sentence on both prose fields — safe to RENDER as text, never to execute or re-shell.
- **[Cryptographic primitives]** Not applicable, by a design decision rather than by absence: no value on this path is a secret, no comparison is made against one, and no identifier is minted — every string is claude's, carried or emptied. There is no randomness in the arm.
- **[Network & I/O]** No finding in this slice, and one question named for the next. The line is capped at 4 MiB by `defaultMaxParseBuf` before the decoder sees it, and every claude-derived string is capped again at construction, for a 5120-byte worst-case event. No RATE bound and none is owed here: one O(1) length test per field against an already-capped line, reaching a synchronous `emit` with no queue in this package, and a `local`-scoped fallback fires at most once per refused turn while a `session`-scoped one stops re-announcing by definition — both are turn-paced, not model-paced. The fan-out consequence belongs to #2265, which puts the event on the wire; named here so that ticket inherits the question rather than rediscovering it.
- **[Error messages, logs, telemetry]** No finding. MUST-NOT-log is every decoded field, the two prose ones sharpest per the second finding above; MUST-log is nothing. The one log is the undecodable-path `Debug`, carrying the literal subtype keyword this package owns — the same class as the `sl.Type` the drop branch already logs — so no byte of claude's line is recorded anywhere. No telemetry.
- **[Concurrency]** No finding, structurally. The arm reads and writes no `Parser` field, takes no lock, spawns no goroutine, and reaches the existing synchronous sink. There is nothing to order, nothing to leak and no check-then-mutate. The variant gets no arm in `cmd/pyry`'s interactive `Handle`, so nothing in this slice renders it either.
- **[Threat model alignment]** OUT OF SCOPE — `docs/protocol-mobile.md` § Security model does not bind this slice, because nothing here reaches the wire. #2265 owns the frame, the fan-out and the envelope arithmetic against the live cap, and inherits the four consumer-facing doc obligations above. One caveat it must carry rather than re-derive: every string here is bounded and UTF-8-valid and that is ALL it is — nothing on this path strips control characters or terminal escape sequences, which is `ModelAnnounced.Model`'s stated caveat and applies to all six fields.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-09
