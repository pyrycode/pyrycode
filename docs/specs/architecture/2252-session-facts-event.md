# #2252 — streamsup: report claude's version and permission mode as a bounded event

## Files read

- `internal/streamsup/parser.go` → `systemInitLine` — the one-field decode target and the doc that argues its omissions; `emitModelAnnounced` — the gate, the cap and the undecodable arm this ticket splits; `emitSystemSubtype` — the `init` dispatch seam the Technical Notes point at; `maxModelField` — the per-identifier cap precedent and its "a sibling gets its own constant" paragraph; `emitBackgroundTaskStarted` and `emitRateLimit` — the `bound` closure plus sequential-statements idiom for a multi-field `TruncatedFields`; `truncateField` — cuts and scrubs invalid UTF-8; `controlAckLine` — the only doc in the file that states the rung-disturbance a widened decode target causes.
- `internal/streamsup/effort_init_capture_test.go` → `effortInitPins`, `effortInitCapturedInitKeys`, `effortInitRecord` — the blocker's committed measurement: three init lines, 24 keys each, `claude_code_version` `2.1.259`, `permissionMode` `default`, `effort` absent. Also the in-package reader this ticket's capture test reuses.
- `internal/streamsup/capture_test.go` → `capturedSystemLine`, `capturePath` — the 2.1.220 capture reader; that line's `permissionMode` is `bypassPermissions`, which is what makes a mode assertion non-vacuous against the 2.1.259 lines.
- `internal/streamsup/parser_test.go` → `modelAnnouncedEvent` — fatals unless the parser emits exactly one event, so it is the blast radius of a second event per init line; `modelInitLineFixture` — declares `model` alone, so its callers are unaffected; `TestParser_ModelAnnouncedMapsFromCapture` — the whole-event `DeepEqual` idiom and its stated preference over a per-key sweep.
- `internal/streamsup/envelope.go` → `permissionModeAllowed`, `permissionModeBypass` — the membership check this ticket deliberately does NOT reuse; it bounds what the daemon may ASK FOR, not what claude reports.
- `internal/turnevent/event.go` → `ModelAnnounced` — the variant, its per-turn hazard, its verbatim rule and its own 22-key census to correct; `RateLimited` — the `TruncatedFields []string` sibling; the marker block and the `var _ Event = …` assertions.
- `cmd/pyry/interactive_turn_v2.go` → `interactiveTurnEmitterV2.Handle` — the status-arm shape and the switch's own merge rule; `eventKind` — the name-only log discriminant, and the two variant COUNT claims in its `ModelAnnounced` arm that go stale; `emitMapped` — what an unmapped variant does today.
- `cmd/pyry/stream_turn_busy.go` → `turnMarkFor` — the whitelist whose default already answers this variant.
- `cmd/pyry/stream_turn_busy_test.go` → `turnEventVariants`, `TestTurnMarkFor_TotalOverEveryVariant` — the AST-derived totality guard that reddens the moment a marker method is declared.
- `internal/turnbridge/outbound.go` → `MapEvent` — its `ModelAnnounced` case states why the announcement rides every turn rather than delimiting one; its default is where the new variant lands until #2254.
- `docs/knowledge/features/turnevent-package.md` § "The three sum-type seams" — **the rule that shaped this plan's § cmd/pyry section**: adding an `Event` variant means walking all four production type switches by hand, because only `turnMarkFor` has a marker-derived guard and the other three default silently. Same section records that a prose count of variants goes stale with nothing reddening (#2134) and that one grep for the digits is the reusable check.
- `docs/knowledge/features/streamsup-package-system-maps-per-subtype-since-2026-08-07.md` — the per-subtype mapping seam this ticket extends.

## Context

`emitModelAnnounced` decodes the `system/init` line into `systemInitLine` and emits `turnevent.ModelAnnounced`. That target declares one field. Two of the line's other keys answer the same question `model_announced` answers — the daemon knows what it asked for and only claude knows what it ran — and neither is reported anywhere today: `claude_code_version`, claude's own build, and `permissionMode`, the posture the child is actually running under. An unexpected posture is currently discarded with the rest of the line.

`effort` is not declared. The blocker measured its absence under a spawn shape with an effort actually set, and `effortInitPins` machine-enforces that measurement; declaring a field claude does not send is what this ticket no longer does.

No ADR is warranted. The design adds one variant to an established family and reuses that family's cap, gate and reporting conventions without introducing a new rule.

**Sizing, recorded rather than resolved.** The refiner's estimate is ~900 lines of total written work, about 100 over the one-ticket ceiling, and the body records why it stays one ticket. I re-derived the boundary against this plan and agree. Files: 3 production sources. New exported types: 1. Consumer call sites needing simultaneous update: 5 (two `cmd/pyry` arms, one totality row, two parser tests). Acceptance criteria: 5. Reject branches: 2. Only the line count is exceeded, and both available cuts produce a slice the sizing floor forbids — one field per ticket widens a shipped payload a leg later, and splitting the decode target from the variant leaves a slice that emits nothing and whose only consumer is its sibling. The floor wins over the ceiling.

## Design

### 1. `turnevent.SessionFacts` — the new variant

Three fields: `ClaudeCodeVersion string`, `PermissionMode string`, `TruncatedFields []string`. Plus a value-receiver `isTurnEvent()` marker and its `var _ Event = SessionFacts{}` assertion, beside the siblings.

The NAME is taken from #2253, which has already frozen the wire type as `session_facts`, and the naming rule in `internal/protocol`'s codes doc runs that way round: the wire follows the turnevent variant. The doc states the tension the name carries — a variant called `SessionFacts` declares no session identity at all, because claude's `session_id` is claude's session and not the daemon's conversation.

`TruncatedFields []string` rather than `ModelAnnounced`'s `Truncated bool`, and the sibling's own doc says why: a bool is right when the payload is a single string, and a named list is right when the report has to say WHICH of several was cut.

### 2. `systemInitLine` widened to three fields

`Model`, `ClaudeCodeVersion` (`claude_code_version`) and `PermissionMode` (`permissionMode`, claude's camelCase). Nothing else is declared. `cwd`, `session_id`, `memory_paths` and `messaging_socket_path` stay out, and the type's doc keeps arguing the omissions — a field never declared cannot leak.

The census in that doc, and the matching one on `ModelAnnounced`, both read 22 keys and are wrong: the committed capture pins 24, with `memory_paths` and `messaging_socket_path` among the newcomers. Corrected where touched, per the Technical Notes, rather than copied forward.

**The rung-disturbance this accepts, stated rather than discovered.** `controlAckLine`'s doc names the hazard exactly: a new field on a shared decode target makes a non-string value in THAT field fail the WHOLE-line decode, so a line that emits `ModelAnnounced` today would newly emit nothing. One target is chosen anyway, for three reasons. AC4 requires it, and one target is what makes the declared-field-set pin a single assertion rather than two that can drift. Both new keys are strings on all four committed init lines across two releases. And a second target would decode the same line twice and fire the undecodable Debug twice for one malformed line. The acceptance is written into the type's doc so a later reader meets a decision rather than an oversight.

### 3. `emitInitLine` — one decode, two emits

The `init` arm of `emitSystemSubtype` calls `emitInitLine`, which owns the decode and the undecodable arm and then calls two void helpers in order:

- `emitModelAnnounced(il systemInitLine)` — signature changes from `(line []byte) bool`; the gate on an empty model, the cap and the verbatim rule are untouched. One production call site, and no test calls the method directly.
- `emitSessionFacts(il systemInitLine)` — new.

Naming departs from the family's `emit<Variant>` convention on purpose: a function that emits two variants cannot be named after one, so this one is named after the LINE it maps.

The undecodable Debug moves up into `emitInitLine` and its paragraph moves with it — the `err` is never logged because `encoding/json` quotes the offending input bytes into its error text. Sharing that arm is what keeps one malformed line producing exactly one record. `emitInitLine` reports the line CONSUMED on every path, as its predecessor did.

### 4. The gate

`emitSessionFacts` emits nothing when BOTH decoded values are empty, and exactly one event when at least one is non-empty, carrying the other empty.

It gates on the DECODED values, before the cap, because the criterion is a statement about the LINE. One consequence is worth naming: `truncateField` DELETES invalid UTF-8, so a value made only of invalid bytes passes the gate and lands empty on the event. That residue already exists behind `ModelAnnounced.Model`'s "never empty" claim and is not repaired here.

The gate DIVERGES from `emitModelAnnounced`'s, and the divergence is the point. There the model IS the whole payload, so an event naming none asserts something the line did not say. Here two facts share one event: one present fact is still news, and the other's absence is claude's own choice — `emitBackgroundTaskStarted`'s rule. Both-empty is the only case that asserts nothing, and that is what is dropped. Silently, for the arm above's stated reason: init fires once per TURN, so a Debug here would reinstate a per-turn noise row.

### 5. Caps

Two new constants at 256, `maxClaudeVersionField` and `maxPermissionModeField`. Separate constants even at the family's shared value, for `maxRateLimitField`'s stated reason — they bound different fields for different reasons and folding them would make a future change to one silently move the other.

Applied through the `bound` closure and sequential statements, so `TruncatedFields`'s ORDER rests on something a reader sees rather than on the left-to-right operand rule. The names in the list are the DAEMON's — `claude_code_version` and `permission_mode`, the latter NOT claude's `permissionMode`, exactly as `limit_type` is not claude's `rateLimitType` — and they match the payload keys #2253 has frozen.

Measured, not chosen: the observed values are `2.1.220` and `2.1.259` at 7 bytes, and `bypassPermissions` and `default` at 17 and 7. 256 is about 15x the longest observation, wider than `maxModelField`'s 10x and for the same reason — room for a scheme claude has not shipped, and a hard cut on anything that has stopped being an identifier. Envelope arithmetic in `maxUnrecognizedRaw`'s style: worst case 512 bytes of claude-derived text plus about 37 bytes of daemon-authored names, roughly 0.8% of the 65519-byte application-envelope cap, matching `maxCompactField`'s pair. No rate bound is owed: init fires once per turn and the event is not a droppable delta, so it holds a queue slot under the same backpressure its five sibling variants do.

Deliberately NOT `permissionModeAllowed`'s membership check, and the distinction is the design rather than an oversight. That validator bounds what the DAEMON may ask for on a control request, where refusing an unknown value is correct. This field reports what claude SAYS it is running, which the daemon neither controls nor may reject. A membership check here would drop a real posture report the first time claude ships a new mode — the one case an operator most needs to see — and AC1 forbids it in as many words.

### 6. The four production type switches, walked by hand

The turnevent package overview's rule: only `turnMarkFor` has a marker-derived guard, and the other three default silently.

- **`interactiveTurnEmitterV2.Handle`** — new status arm, `flushDelta` then `emitMapped`, with NO turn-lifecycle mutation. Kept separate from the `ModelAnnounced` arm despite the identical body, per that switch's own merge rule that arms merge on a shared REASON: an announced model is a property of the turn's CONFIGURATION, while these are properties of the child's BUILD and POSTURE, which do not vary per turn at all.
- **`eventKind`** — name-only arm returning `session_facts`. NEITHER value is returned. Both are claude-authored strings on the path `maxModelField` already bounds, so the #833 posture covers them unchanged. The two variant COUNT claims in this switch's `ModelAnnounced` arm go stale on the marker declaration; the live one is corrected in place with a dated note, in the house style that leaves the falsified sentence legible.
- **`turnMarkFor`** — UNCHANGED. Its whitelist default already answers `turnMarkNone`, which is correct: claude's build and posture say nothing about whether a turn is open, and the event arrives in every conversation on every turn, so opening a mark would wedge all of them. The AST walk enrolls the variant the moment its marker is declared, so a row in `TestTurnMarkFor_TotalOverEveryVariant` is required; it asserts the existing answer rather than a new arm.
- **`turnbridge.MapEvent`** — UNCHANGED, default stays armless. #2253 declares the wire type and #2254 wires the mapper. Until then `emitMapped` logs one content-free `interactive_turn.unmapped` Debug per turn naming only the kind, which is exactly why the `eventKind` arm above is what stops it reading `unknown`.

## Concurrency model

Nothing is added. The parser decodes on the single stdout-reading goroutine and retains nothing across lines; the event is a pure value type. No goroutine, no lock, no channel, no `context` in the touched path. The `TruncatedFields` slice is built by `append` on a nil local per event, so no slice header is shared between two events — the aliasing hazard the `ModelList` arm names does not arise here.

## Error handling

- **Undecodable line** — one Debug naming the subtype only, no event, line consumed. The `err` is never logged. Shared by both variants, which keeps one malformed line to one record.
- **Both facts absent or empty** — no event, no log, line consumed.
- **A value over its cap** — cut at the constant, scrubbed to valid UTF-8, the field named in `TruncatedFields`, event still emitted.
- **No panic path.** Every operation is a decode, a length comparison and a slice append.

## Testing strategy

`internal/streamsup`:

- The blocker's capture drives the central assertion: each of the three `system/init` lines in the committed 2.1.259 capture produces one `SessionFacts` carrying the version and mode the line itself shows, derived from its bytes rather than pinned, read through the existing in-package record reader. A whole-event `reflect.DeepEqual` against a struct literal carries "and nothing else from the line", per the sibling test's stated preference over a per-key sweep.
- The 2.1.220 capture is the second witness and the one that makes the mode assertion non-vacuous: its `permissionMode` is `bypassPermissions`, so a mapping that hardcoded, lowercased or allow-listed `default` maps the 2.1.259 lines to themselves and passes without it.
- Verbatim rows the captures cannot prove, synthesized and inventing no field structure: mixed case, a mode in no published list, an unfamiliar version scheme.
- The gate table: both keys absent; both present-but-empty; one absent and one present; one empty and one present. The first two emit nothing; the rest emit exactly one, with the other field empty.
- Cap rows per field, including a cut landing mid-rune so the UTF-8 scrub is proved, and the `TruncatedFields` ORDER when both are cut.
- The declared-field-set pin over `systemInitLine` by reflection: exactly three fields with exactly the three JSON tags, so a later widening has to be deliberate. The four named keys are asserted PRESENT on the captured line first, so the pin is a statement about dropping them rather than about a line that never carried them.
- One init line produces exactly TWO events in the order `ModelAnnounced` then `SessionFacts`; an undecodable one produces zero events and exactly ONE Debug record.
- A content-free log assertion over the captured line: neither value reaches the log at any level, following the sibling's own test.
- Two existing tests move off `modelAnnouncedEvent`, whose helper fatals unless the parser emits exactly one event, because both feed the 2.1.220 capture and that line carries both new keys. Every other caller feeds the model-only fixture and its event count is unchanged.

`cmd/pyry`: the totality row asserting `turnMarkNone`; a `Handle` arm test proving no turn-lifecycle mutation, following the `ModelAnnounced` arm's own; an `eventKind` assertion that the name is returned and neither value is.

`internal/turnevent`: the variant's field round-trip beside its siblings.

Gate: `go test -race` on the three touched packages, `go vet ./...`, `go build ./cmd/pyry`.

## Open questions

1. **Does `ModelAnnounced`'s doc need its 22-key census corrected as well as `systemInitLine`'s?** Both carry it and the Technical Notes say to correct the count where touched. Expected answer yes, both to 24 with the two newcomers named. No design impact.
2. **Is the `Handle` arm right before `MapEvent` has one?** Expected answer yes: AC5 requires it, the Debug it produces until #2254 is content-free and one per turn, and the arm needs no revisit when the mapper lands.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No findings. The boundary is explicit and single: `emitInitLine`'s `json.Unmarshal` into `systemInitLine` is the only place these bytes become typed values, and the decode input is the TOP-LEVEL line bytes rather than any nested field. That is what stops a tool result whose text is literally an init line from announcing a posture the daemon never ran — the forgery argument `emitModelAnnounced` already rests on, inherited unchanged because the decode site does not move. Downstream holds `turnevent.SessionFacts`, a pure value type whose fields are documented as claude-authored and bounded-only. The WIDENING is the one thing that touches this boundary, and its worst case is a REFUSAL rather than a leak: a non-string `permissionMode` fails the whole-line decode and suppresses both events. Named in `systemInitLine`'s doc, and the reason a second decode target was considered and rejected is recorded in § Design 2 rather than left implicit.
- **[Trust boundaries — the omissions]** No findings, and this is where the category actually bites. `cwd` is the operator's filesystem path and `session_id` is claude's session identity; neither is declared, and the plan pins the declared field set by reflection so a later widening reddens a test rather than passing quietly. Absent from the decode target is stronger than a downstream sweep, because a field never declared cannot reach an event or a log by any path.
- **[Tokens, secrets, credentials]** No findings, by design decision rather than absence of thought. Neither field is a credential, and `apiKeySource` — the one key on the captured line that names a credential's PROVENANCE — is deliberately left undeclared and is covered by the field-set pin above. Nothing is generated, stored, rotated or revoked by this change.
- **[File operations]** Not applicable, and the design decision that makes it so: this path reads a decoded line and writes an in-memory event. No path is constructed, opened, stat-ed or written. The two capture files the tests read are committed fixtures at package-relative constant paths with no caller-supplied component.
- **[Subprocess / external command execution]** Not applicable. Nothing here reaches `exec.Command`, and no value on this path flows to an argv. The inverse direction is worth stating because it is the plausible-looking hole: `permissionMode` is REPORTED here, never consumed as an input to a spawn or a control request. The daemon's own spawn posture is decided by `permissionModeAllowed` and `permissionModeSpawnWritable` on a different path, and § Design 5 records that this ticket deliberately does not reuse or feed those. A future consumer that treated this reported value as an authorization decision would be trusting claude's claim about its own posture; the variant's doc says so at the field.
- **[Cryptographic primitives]** Not applicable. No randomness, no comparison against a secret, no key material on this path.
- **[Network & I/O]** No findings. Input size is capped twice over: the line itself by `defaultMaxParseBuf` before the decode, and each retained value by its own 256-byte constant at construction, so an oversized value never enters the event stream, a queue or a log. Amplification from input to retained bytes is near zero — the target holds three scalars and no array, so a 4 MiB line yields at most 768 retained bytes. The envelope arithmetic is in § Design 5. No socket, listener, timeout or TLS configuration is touched.
- **[Error messages, logs, telemetry]** No findings, and this is the category the design spends the most on. Neither value is logged at any level, which is the #833 posture applied unchanged. Three specific sinks were checked. The undecodable arm logs the subtype keyword only and never the `err`, because `encoding/json` quotes the offending input bytes into its error text — a channel no per-path attribute check can see. `eventKind` returns the variant NAME only. And `emitMapped`'s unmapped-drop Debug, which this variant will hit on every turn until #2254 lands, logs only `kind` from `eventKind` and no payload. The both-empty gate is silent rather than logging a reason, so no per-turn record is added on any path.
- **[Concurrency]** No findings. No lock is taken, so no ordering exists to get wrong. No shared state is read-then-mutated: the parser retains nothing across lines and the event's one slice is allocated per event on a nil local, so no header is shared between two events. No goroutine is spawned, so none can leak. A signal mid-line loses at most the line being decoded, which is the existing behaviour of every sibling arm and leaves no partial state on disk.
- **[Threat model alignment]** No findings. The relevant threat in the protocol spec's security model is a compromised or buggy claude asserting something the daemon then presents as fact. It is addressed the way the family addresses it — bound the value, carry it verbatim, and state at the field that the daemon is REPORTING a claim rather than vouching for it. What is explicitly OUT OF SCOPE and named: the frame's client-facing render sanitization, which is the client's at its own render boundary because nothing on this path strips control characters or terminal escape sequences; the wire form itself, which is #2253's; and the mapping that carries these values to a phone, which is #2254's.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-10
