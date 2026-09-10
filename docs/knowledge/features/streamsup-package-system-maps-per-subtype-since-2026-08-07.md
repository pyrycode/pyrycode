# `system` maps per-subtype since 2026-08-07 (#1380) — the wholesale-drop design's first crack
**`system` maps per-subtype since 2026-08-07 (#1380) — the wholesale-drop design's first crack.** `status`
and any never-seen subtype stay silent exactly as before. This document is a chronicle: `emitSystemSubtype`'s
`case` arms are the one live enumeration of the mapped set (see below), and each dated entry here is the arm
that landed at that point, not a running total — the count below is the *original* five.
`system/task_started` → `turnevent.BackgroundTaskStarted` (`TaskID`, `ToolCallID` — claude's
`tool_use_id`, renamed to match `ToolStart`/`ToolUpdate`'s field name for the same identifier —
`Description`, `TaskType`, `TruncatedFields`); `system/task_updated` → `turnevent.BackgroundTaskUpdated`
(`TaskID`, `Patch`, `TruncatedFields`); `system/background_tasks_changed` → `turnevent.
BackgroundTaskRoster` (`Tasks []BackgroundTask`, `DroppedTasks`) — the aggregate variant, snapshotting
every task claude is tracking at that moment rather than reporting what happened to one;
`system/thinking_tokens` → `turnevent.ThinkingProgress` (`EstimatedTokens`, `EstimatedTokensDelta`,
**no** `TruncatedFields` — two `int`s cannot grow) — the one **rate-bounded** variant, described below;
and `system/init` → `turnevent.ModelAnnounced` (`Model`, `Truncated`) — the one variant naming what
claude is actually running rather than something about a turn or a task, described below (#1600).

**CORRECTED 2026-09-10 (#2252): `system/init` now produces TWO variants, not one — the family's
first line to do that, where every prior "first" here (`task_notification`, above) went the other
way, two subtypes into one event.** `emitSystemSubtype`'s `"init"` case now calls `emitInitLine`,
which decodes the line ONCE into the now-three-field `systemInitLine` and calls two void helpers in
order: `emitModelAnnounced` (unchanged gate, cap and verbatim rule, just no longer owning the
decode) and the new `emitSessionFacts`, mapping `claude_code_version` and `permissionMode` to
`turnevent.SessionFacts`. The undecodable arm moved up into `emitInitLine` and is shared, which is
the whole reason the decode was hoisted rather than duplicated: two decodes of one malformed line
would fire the drop Debug twice for a single bad line. `SessionFacts` gates differently from every
prior single-field arm in this family — it emits when EITHER of its two facts is present and
suppresses only when BOTH are empty, because unlike `ModelAnnounced` (where the field IS the whole
payload) two facts share one event here, and only their joint absence asserts nothing. `effort` was
considered and dropped on evidence, not omitted for lack of trying: #2251's capture set an effort on
both the daemon's launch-argv and in-band paths and no `system/init` line ever carried the key,
machine-pinned by `effortInitPins` in `effort_init_capture_test.go` so a later claude that starts
sending one reddens the pin instead of shipping unnoticed. See
[turnevent-package.md](turnevent-package.md) for `SessionFacts`'s own doc and why it is a new
variant rather than a widened `ModelAnnounced`.

The first three mappings fix #1240's symptom: previously a backgrounded command's lifecycle was
indistinguishable from a genuine turn end (`turn_end`/`end_turn`, state `idle`) because the whole
`system` family was dropped regardless of subtype. `thinking_tokens` fixes a different gap: it is
claude's only mid-turn proof of life on this surface, so mapping it gives a client watching a long turn
something to distinguish "slow" from "wedged." `init` fixes a third: the daemon's own `model` fields mean
the per-session override and read empty in the ordinary case, so nothing previously said what claude was
actually running.

The match (`emitSystemSubtype`) sits **inside** `consumeLine`'s existing `ignoredLineTypes` branch rather
than beside it — `system` stays on the list unchanged, so `emitUnrecognized` (the surfaced tier) stays
structurally unreachable from any `system` line whatever its subtype, and `TestParser_
IgnoredLineTypesIsTheMeasuredSet` above is unaffected. `emitSystemSubtype`'s `case` arms are the single
enumeration of the mapped set; every comment describing the drop rule (this file included) points there
rather than restating it — a fifth captured subtype is a new case arm there, not a new sibling ticket.

**Compaction is now mapped, and only one of its two observed subtypes got an arm (#2227).** A live
capture against claude 2.1.259 (2026-09-08, #2229) settled the question #1074 left open: compaction
arrives as two more subtypes on this same enumeration, not a new top-level type. `system/status`
carries `status:"compacting"` while compaction runs and `status:null` plus `compact_result`/
`compact_error` when it ends; a separate `system/compact_boundary` line carries `compact_metadata`
(`trigger`, `pre_tokens`, `post_tokens`, `duration_ms`). Only `status` got the sixth `case` arm here
(`emitCompactingStatus`, mapping onto `turnevent.Compacting` — declared since #1074, unconstructed from
\#1348's deletion of its only producer until now). `compact_boundary` is a **deliberate** non-mapping,
not a gap: it carries nothing this edge pair needs, its metadata is #2228's payload, and an arm for it
would emit a duplicate edge with nothing to add — it inherits `status`'s former title as the one
measured-and-dropped `system` subtype left standing.

**CORRECTED 2026-09-08 (#2237): `compact_boundary` now has its own arm — the "deliberate non-mapping"
above described #2227's state, not the package's.** `emitSystemSubtype`'s seventh case,
`emitCompactionBoundary`, decodes `compact_metadata` through a target declaring exactly three fields
(`trigger`, `pre_tokens`, `post_tokens`) — the allowlist is structural, so `encoding/json` drops every
other key, including the three operator-transcript uuids the line also carries, without a scrubbing
step to maintain. A nil `compact_metadata` (the line decoded but said nothing to publish) and an
undecodable line both consume the line and emit nothing, the latter on `emitCompactingStatus`'s own
undecodable precedent (a Debug naming the subtype keyword only). `trigger` is bounded at
`maxCompactTrigger` (256 bytes) and **dropped, not cut**, on `maxTurnEndStopField`'s reasoning: it is a
token a client matches against a known set, so a cut token would match nothing while still looking like
one. The two counts cross unclamped, as claude's own numbers. **The arm reads and writes no `Parser`
field at all — not even `p.compacting`** — which is what lets it fire identically whether or not a
compacting edge preceded it (an auto-compaction that announces itself differently is still published)
and is why the line still costs nothing when it falls between a rising and falling edge that never see
it. See [turnevent-package.md](turnevent-package.md) for why the counts need a frame of their own
rather than riding the falling edge's payload, and
[the interactive payload doc](protocol-package-interactive-event-payloads.md) for the wire shape.

The falling edge is wide by design: any `status` other than `"compacting"` closes it, `compact_result`
included, because a missed close (a stuck "compacting" banner) is a worse operator-facing failure than
a spurious one (a banner that closes a beat early). See
[the send-half suppression tiers](streamsup-package-send-half-writeturn.md) for a subtlety this ticket's
own zero-unrecognized criterion exposed: compaction's *consequences* — claude's summary and the harness's
`/compact` echo — arrive as `user` lines, not `system` ones, and needed a separate suppression the
subtype-level seam argument didn't predict. The fixture [#2229's capture wrote](e2e-realclaude-compaction-capture-test-go.md)
had not landed in the tree at the time, so #2227 proved its edges with a live assertion instead of a
fixture replay — see that document for the state of the fixture now.

**`compact_result`/`compact_error` moved from the log to the frame (#2236).** `emitCompactingStatus`
already decoded and bounded (`maxCompactField`, 256 bytes, cut not dropped) both values off the closing
`system/status` line for a Debug record — the only diagnostic a failed compaction had, because the edge
pair itself carries a bare boolean and success looks identical to failure everywhere a client can see.
`turnevent.Compacting` gained `Result`/`ErrorText` fields so the same two already-computed locals reach
the emitted event too; the Debug record is unchanged (same message, same values, computed once and used
twice — `TestParser_CompactingLogsClaudesFailureTextBounded` passes without modification, which is the
evidence the sink changed and the bound didn't). The state machine itself is untouched: `compact_result`
is still not a discriminator, and the falling edge still fires — wide, as above — whether the compaction
it closes succeeded or failed. See
[the interactive payload doc](protocol-package-interactive-event-payloads.md) for the wire shape and
the security posture of putting claude-authored free-form prose on this frame.

**Eighth arm, `permission_denied` (#2232) — and a decode failure the plan never saw coming.**
`emitSystemSubtype`'s eighth case, `emitPermissionDenied`, maps `system/permission_denied` to
`turnevent.ToolCallDenied` (`ToolName`, `ToolCallID`, `Message`, and — when claude sends them —
`DecisionReasonType`/`DecisionReason`). It is unconditional, `emitBackgroundTaskStarted`'s posture
rather than `emitCompactionBoundary`'s: every decodable line emits exactly one event whatever the
fields hold, including all-empty, because the subtype itself is the whole payload here — nothing
else on this surface tells a client the call was denied rather than run-and-failed, so gating on a
field would re-drop the line silently in exactly the case a claude rename causes.

The switch arm alone maps nothing in production, because none of the seven captured real denials
ever reach it. `streamLine.Message` is declared `*streamMessage`, and claude spells `message` on
this line as a plain **string** — so `consumeLine`'s whole-line decode fails before `sl.Type`/
`sl.Subtype` are ever read, and every real denial surfaced as `Unrecognized` forever instead.
`consumePermissionDeniedLine` recovers it inside that decode-failure branch, beside
`dropHarnessProseLine` and disjoint from it by type (a top-level `system`+`permission_denied`
envelope match versus a `user` line's string-content shape) — both entry points stay necessary,
since a denial line that *does* decode cleanly (no `message` key present at all) still takes the
switch arm, and `TestDropcapClassification` carries one row per entry point for exactly that reason.
**The general trap: a single wrongly-typed field on a shared decode target like `streamLine` fails
the ENTIRE top-level decode, not just that field**, so a new subtype's switch arm can be correctly
written and completely unreachable in production if any of claude's actual bytes for that line
don't match the shared target's field types. Check a subtype's fixture against `streamLine`'s
declared field types before writing the switch arm, not after finding the RED run that says the
arm never fired.

**Recovering a denial from the `result` line's own list, because the line's absence is the
daemon's normal case, not a rare drop (#2234).** A fourth, independent decode target off the same
`result` bytes, `resultDenialsLine{ PermissionDenials []resultDenialEntry }`, reads
`permission_denials[].tool_name`/`tool_use_id` — never `tool_input`, which is the tool's full input
and the client already holds it from the matching `tool_use` frame, so decoding it would be the
same leak `session_id`/`uuid` were already refused for on `system/permission_denied` itself.
Measured 2026-09-08 across every committed capture under `internal/e2e/realclaude/testdata`: the
`bypass_approval_argv_v2.1.239_*` arms — launched with `--permission-prompt-tool`, the way
`cmd/pyry/mcp_config.go` launches claude in production — report 9 denials in `result` and **zero**
`permission_denied` lines. In the posture the daemon actually runs, the line this document called
above "the only thing on this surface distinguishing a blocked call" never arrives at all; the
`result` array is the primary channel a real deployment sees, not a hedge against a hypothetical
drop.

`Parser.deniedThisTurn map[string]struct{}` tracks which ids already produced a marker from their
own line this turn, so the same id is never reported twice; it is cleared unconditionally at the
same `result`-arm boundary `thinkingSinceEmit` and `assistantErrorCategory` already reset, rather
than minting a second reset point. Its residual runs the opposite direction from those two
neighbours: a stale id left by a child that dies before its `result` line **suppresses** a later
turn's genuine marker, rather than publishing a wrong one. That is accepted rather than fought,
because a suppressed marker is a second report of a call the client has already seen denied, never
the only report of one, and because claude's `tool_use_id`s are per-call unique — a later turn
colliding with a stale one is not a shape claude produces.

**Test-writing trap: a per-line parser cannot carry state between the line and the `result` that
follows it.** `replayDenialCapture` originally built one fresh `Parser` per captured line; against
the very captures cited above, that reported all seven denials twice, because a `deniedThisTurn` set
starting empty on every call never remembers a marker the previous line already produced. The fix
was to feed a whole capture through one parser instead of one per line. Any capture-replay helper
that constructs a `Parser` more than once per turn cannot exercise this class of cross-line state —
check the helper's parser lifetime before trusting what it reports about a dedup path.

**Ninth arm, `model_refusal_fallback` (#2267) — mapped from documentation, not from a capture, and
that provenance decided the design rather than just caveating it.** `emitSystemSubtype`'s ninth case,
`emitModelRefusalFallback`, maps `system/model_refusal_fallback` to `turnevent.ModelRefusalFallback`
(`Scope`, `OriginalModel`, `FallbackModel`, `RefusalCategory`, `RefusalExplanation`, `Banner`). No
capture of this line exists or can be taken — a refusal cannot be provoked on demand — so the decode
target, `systemModelRefusalFallbackLine`, was built from the Claude Code docs and the Agent SDK's
`sdk.d.ts` against a daemon on claude 2.1.259, six of claude's eleven documented keys declared and
five refused as `systemTaskUpdatedLine`'s kind of control (identity-restating, API-internal, or
naming a message id no daemon surface can join against). `emitPermissionDenied`'s two shapes both
reappear unchanged: unconditional emission (a field-less line is still news, since the subtype is
the whole payload) and an undecodable line producing a subtype-only Debug and no event, with per-field
type tolerance deliberately refused on the same unobserved-failure rule. The four token fields
(`Scope`, both model labels, `RefusalCategory`) drop on overflow and the two prose fields
(`RefusalExplanation`, `Banner`) cut, reusing `maxTaskFieldID`/`maxDenialProse` rather than minting —
the family's established boundary: a client matches or joins against a token, so a truncated one is
worse than an empty one, while a truncated sentence still reads as prose.

**The one new wrinkle documentation-derived provenance adds: a decode target built from field docs is
a prediction about the wire, not a description of it, and that changes what belongs in the code versus
what belongs in the plan.** `permission_denied`'s `message` collision with `streamLine`'s declared
`*streamMessage` forced `consumePermissionDeniedLine`'s recovery path in `consumeLine`'s
decode-failure branch; the documented `model_refusal_fallback` field set carries no `message` key at
all, so no such collision is predicted and no recovery path was built speculatively — a gate written
against a shape nobody has observed can only ever remove a line from the surfaced tier, never add one
back correctly. If the real line does carry a `message` key of any scalar type, this arm is
unreachable in production exactly as `permission_denied`'s was, and the fix is the same shape of
recovery path, not a defensive one guessed in advance. Whoever captures the first real line should
check that before assuming a silent drop elsewhere.

**Acceptance-criteria trap this ticket exposed at refinement, not at implementation: "field absent"
and "field wrong-typed" are different outcomes on this family's shared decode target, and an AC that
conflates them asks the builder to violate the pattern.** A field simply missing from the JSON decodes
fine and reaches the arm as a Go zero value — the ordinary, expected case in a documentation-derived
mapping where nothing is guaranteed present. A field present with the wrong JSON type fails
`encoding/json`'s decode for the *whole* target, per the general trap recorded above for
`permission_denied` — there is no such thing as "that one field failed, the rest decoded." A criterion
asking for "the event still fires, with the bad field empty" describes a per-field tolerance this
family does not build, and can only be satisfied by breaking either the criterion or the pattern.
Write absence and wrong-type as two separate criteria with two separate expected outcomes when a
future subtype's acceptance criteria are drafted from a documentation-derived (rather than
capture-derived) field set.

**`task_progress` was measured and pinned by #2248, then mapped by #2246 — the eleventh `case`
arm.** `internal/streamsup/task_progress_capture_test.go` reads the two verbatim
`system/task_progress` frames `parent_tool_use_v2.1.259.json` already carried — captured for a
different ticket's subagent-join question, kept only because that probe records the whole turn
rather than a filtered quarry — and pins their shape as per-frame equality, not a union:
`taskProgressPinnedKeys` for the top-level fields, `taskProgressPinnedUsageKeys` for the nested
`usage` object. `taskProgressDocumentedKeys`, read off `@anthropic-ai/claude-agent-sdk@0.3.263`'s
`SDKTaskProgressMessage`, is checked only as a set difference against the pin, never copied into
it: `summary` is documented and not observed, because this staging is a local agent without the
progress-summaries option and an MCP task always reports it. `#2246` declared `summary` nowhere
— neither `systemTaskProgressLine` nor `protocol.BackgroundTaskProgressPayload` — on the family's
rule that a documented-but-unobserved field is a fact about the staging, not the subtype; a new
capture with the progress-summaries option is what would change that, not the SDK docs.

Two shared-fixture conventions worth carrying into a later ticket's own reader over a committed
record: a second reader over an already-consumed record
(`internal/streamsup/parent_tool_use_capture_test.go` reads the same file for its
`parentPinnedAgentID`) should mint its own path/version constants rather than reuse the first
reader's, and should not assert the two agree — two readers pinned at two claude releases over two
records is a legitimate steady state an equality check would forbid. And a package-level pin slice
must be declared already sorted, never sorted in place inside the reader: `-race` cannot see that
mutation as a hazard while no parallel test happens to touch the same slice, so a
`sort.StringsAreSorted` assertion (`TestTaskProgressPinsAreDeclaredSorted`) is the only thing
standing behind the rule, not the comment describing it.

**Eleventh arm, `task_progress` (#2246) — a separate frame, and the family's first rate bound
argued from a delta the daemon computes rather than one claude gives.** `emitSystemSubtype`'s
eleventh case, `emitBackgroundTaskProgress`, maps `system/task_progress` to a new
`turnevent.BackgroundTaskProgress` rather than widening `BackgroundTaskUpdated` — decided by the
captured field set, not by economy of frames. `description` on this subtype is the task's
*current activity* ("Reading alpha.txt"), where `description` everywhere else in this family is
its *opening* one; putting both meanings under one name on one task row would have been a
wire-contract trap no later ticket could undo. See
[protocol-package-background-task-event-payloads.md](protocol-package-background-task-event-payloads.md)
for the wire shape.

The rate bound gates on the advance of claude's own `usage.tool_uses` counter,
`minTaskToolCallsPerEvent = 2`, argued from a single observed task rather than
`minThinkingTokensPerEvent`'s four bursts: the ceiling is the observed task's whole advance (any
higher silences the only task ever measured), the floor is that 1 is not a bound at all, and the
two meet — the evidence is too thin to also supply a safety margin, and that thinness is stated at
the constant rather than dressed up. Unlike `thinking_tokens`' `estimated_tokens_delta`, which
claude supplies fresh and already non-negative each line, `tool_uses` is *cumulative per task*, so
the delta here is the daemon's own arithmetic against a value it kept from a previous line. That
moves the overflow hazard: `emitThinkingProgress`'s `d <= 0` guard rules out a bad delta claude
sent, but a `task_progress` counter that fails to advance is a case that *arrives* rather than one
claude rules out, because `prev` is the daemon's own retained state. Left ignored, it leaves a
stale high-water mark no realistic advance climbs out of — silence for that task for the rest of
the turn. `emitBackgroundTaskProgress` answers it by re-baselining down (storing the lower `seen`
without emitting) rather than ignoring the line, so an alternating counter still emits at least
every other line; the discriminating assertion for this class of fix is always the event *after*
the anomaly; one that only checks the first event cannot tell "re-baselined" from "silently
ignored" apart.

The per-task state (`Parser.taskProgressToolUses map[string]int`, capped at
`maxTaskProgressTasks = 8`, follows `deniedThisTurn` in every dimension) has a cardinality-cap
choice worth naming because the obvious alternative inverts the bound rather than merely
relaxing it: treating an *untracked* task past the cap as if its previous value were zero would
restore the 1:1 emit-every-line rate for exactly the tasks a runaway or hostile input arranges to
be past the cap — the cap would still bound memory but would stop bounding the rate for the tasks
that need it most. The arm instead drops a past-cap line with no event, accepting silence for the
ninth-plus concurrent task as the safer failure: it silences one task's progress decoration, never
the turn's own liveness signal, and that task's opening and terminal frames are untouched.

**The dropcap classification pair, and why this arm cannot take `task_notification`'s posture.**
`emitBackgroundTaskNotification` (#2245) gates on nothing — a bare
`{"type":"system","subtype":"task_notification"}` still maps, because absence of a field is
claude's to choose — so its `TestDropcapClassification` row is a single MAPPED line.
`emitBackgroundTaskProgress` cannot follow that precedent: its rate bound reads
`usage.tool_uses`, and a line carrying no counter has nothing to compare against a previous value,
so a bare line is consumed with no event. The correct row is therefore a *pair* — a bare line
pinning the DROP the guard produces, and a second line carrying `usage.tool_uses` at the bound
pinning the MAP — on the same shape `system/init` and `system/status` already use for a gated
arm. Copying the neighbouring `task_notification` row here would have asserted the wrong verdict;
the general check before writing a `TestDropcapClassification` row for a new gated arm is whether
the gate can fire on an empty/absent field, not whether the subtype is "in the mapped family."

**The two prose fields carry a sharper hazard than any prior arm's, because the request being
described was refused.** `RefusalExplanation` and `Banner` are claude's own writing about *why* a
request was declined, so — unlike `ToolCallDenied.Message`, which describes a tool call the daemon
itself made — they can quote or paraphrase the user's own words back out, and `RefusalCategory` is an
open string (`cyber`, `bio`, ...) asserting *what kind* of refusal it was, an accusatory classification
of the user's request that the daemon does not verify and cannot verify — there is nothing on this
surface to check it against. Both hazards are bounded the same way: nothing on the emitting path logs
a decoded field, and nothing in the daemon acts on any of the six fields, so a fabricated or mistaken
line is a misleading label a client renders, never something that drives daemon behavior. `Scope`
carries the one fact a client should act on with care: `session` means claude keeps the *session* on
the fallback model, so a later `ModelAnnounced` reporting a different model has no other explanation on
this wire — a consumer should read this event as the swap's claimed *cause* and `ModelAnnounced` as its
*confirmed result*, never treat the fallback label itself as the daemon's authoritative model state.

**Tenth arm, `task_notification` (#2245) — the family's first shared event, and the first arm named for the
line rather than the event because of it.** `emitSystemSubtype`'s tenth case, `emitBackgroundTaskNotification`,
maps `system/task_notification` onto the *existing* `turnevent.BackgroundTaskUpdated` rather than a new
variant — the obvious-looking alternative, a fourth background-task frame, was rejected because it would add
a frame whose only difference from `task_updated`'s is which fields it fills. Two subtypes now produce one
event, and they fill DISJOINT fields: `task_updated` fills `Patch` and leaves `Status`/`Summary` empty;
`task_notification` fills `Status`/`Summary` and leaves `Patch` empty. A non-empty `Status` is therefore what
tells a consumer a terminal state was reported, and that reading is stated on the event, the wire payload and
`docs/protocol-mobile.md` rather than left to be inferred — no daemon-authored discriminator field names
which line produced the frame, since a field like that would be exactly the invented content `Patch`'s own
contract forbids one field over. `emitBackgroundTaskNotification` is named for the *line*, unlike its three
siblings, which take the variant's name: here two functions produce one event, so a name matching the event
would collide with the peer that already has it. See
[the protocol payload doc](protocol-package-background-task-event-payloads.md) for the wire shape, the
widened SECURITY posture (`Summary` is the family's second field that can carry a literal command line, after
`BackgroundTaskStarted.Description`), and the envelope arithmetic — the arm's own worst case is 4608 bytes
(7.0% of the 65519-byte cap), but the fit-cap test measures the *event type's* ceiling at 8704 bytes (13.3%)
since nothing structural stops a third producing subtype filling all four fields someday. `Status` stays a
plain string, not a closed set: one token (`completed`) has ever been observed, and the capture's own
`limitations` record that `failed`/`stopped` are documented but never staged — the same call this family
already made once for `BackgroundTaskStarted.TaskType`. No patch is synthesized to carry the terminal state:
the captured line carries no `patch` key, and manufacturing one would have falsified both `Patch` doc
comments' promise that the field holds claude's own bytes alone.

Landing the tenth arm meant correcting **four** committed statements that had gone stale, one found only while
reading rather than named by the ticket: `ignoredLineTypes`' "Still dropped in silence" paragraph (which had
named `task_notification` as measured absent — #2247 captured it since), `parser_test.go`'s silence row for
the subtype, `turnevent.BackgroundTaskRoster`'s "no terminal, finish, or completion event exists in this
family" paragraph, and `docs/protocol-mobile.md`'s 2026-08-09 changelog entry making the same claim. Each was
amended with a dated correction in place rather than rewritten, on the #1404 `rate_limit_event` pattern this
same comment already used.

**Test-writing trap: a value sweep for a dropped field is vacuous when the field's one captured value is the
empty string.** `output_file` is excluded from `systemTaskNotificationLine` by design (documented as a path on
the operator's host — "a field that is never declared cannot leak," `systemTaskUpdatedLine`'s guarantee), but
the committed capture's `output_file` happens to be `""`. Sweeping the emitted event's string fields for that
value would match every unset field and pass identically whether or not the field were actually declared and
carried — it proves nothing. The assertion has to move to where the guarantee actually lives: reflection over
`systemTaskNotificationLine`'s declared `json` tags, asserting no field maps to `output_file`. The other three
excluded keys (`tool_use_id`, `uuid`, `session_id`) have non-empty captured values and stay covered by the
ordinary reflection-based value sweep — only the empty-string case needed the structural version. Check
whether a to-be-excluded field's one captured value is empty before writing a drop test as a value sweep; if
it is, the sweep is decoration and the exclusion needs a structural assertion instead.

**Twelfth arm, `informational` (#2319) — the first producer for `turnevent.Banner`, and the gate question
turned on how `claude` composes the line rather than on the shape alone.** `emitSystemSubtype`'s twelfth
case, `emitInformationalBanner`, maps `system/informational` — hook feedback, a `UserPromptSubmit` hook's
block reason, text a slash command prints — onto `turnevent.Banner` (`Level`, `Text`, `Truncated`,
`StopsTurn`), declared unconstructed by #2256. Before this arm, a hook that refused a prompt reached the
operator as nothing at all: the prompt was never answered and nothing said why. `system/notification`
(#2258) is the sibling subtype still outstanding on the same variant. See
[turnevent-package-outbound-event-variants.md](turnevent-package-outbound-event-variants.md) and
[the interactive payload doc](protocol-package-interactive-event-payloads.md) for the wire shape, now
corrected to name this arm rather than the "declared with no producer" claim both carried before it.

The arm gates on `Content` being non-empty — `emitCompactionBoundary`'s answer, not `emitPermissionDenied`'s
unconditional one — because `Text` is the field the variant exists to carry, and a client renders this frame
as a first-class notice: an empty banner is visible chrome saying nothing, worse than the silence it
replaced. **That choice needed checking against the defect it could restore, not just against the shape it
resembles.** A gate that can drop a refusal silently is exactly #2232's hazard, and this one is safe only
because `claude` composes the wrapper prose itself (`"UserPromptSubmit operation blocked by hook:"`, the
hook's path, then the original prompt) — a hook refusing with an empty reason still yields non-empty
`content`. That is a fact about `claude`'s formatting, not about the design, and it is what makes the
precedent's gate applicable here rather than merely similar-looking; a future arm on this switch that picks
the compaction-boundary gate by analogy still owes this check against its own line's composition.

Two new bounds land beside `maxCompactTrigger` and `maxDenialProse`: `maxBannerText` (4 KiB, cuts and
reports through `Truncated`, the contract `turnevent.Banner.Text`'s doc publishes) and `maxBannerLevel`
(256 bytes, drops in silence, since `Level` is a token a client matches). `maxDenialProse` was the tempting
wrong reuse and is deliberately not taken: it is 2 KiB — half the published contract — and it already caps
a `claude`-authored prose field the daemon spells `banner`, `turnevent.ModelRefusalFallback.Banner` (#2267).
The two `banner`s are unrelated variants that happen to share a word; a grep for `Banner` in this package
turns up that other field's hits almost exclusively, and sizing or reusing a constant off a name match
rather than off the variant it actually bounds is the failure mode the new constant's own doc states to
head off. **A name collision between a field and a variant is worth stating at the new constant, because
the next reader greps the word and finds the wrong one first.**

The capture reader, `informational_capture_test.go`, is a fourth reader on `compaction_capture_test.go`'s
terms and deliberately not a generalization of its two nearest siblings (`compaction_capture_test.go`,
`task_notification_capture_test.go`): both of those carry a four-quadrant skip gate built for a fixture that
had not landed in the tree yet. Copying that gate onto an already-committed fixture would add a legal-skip
state this file can never reach, and — because the fixture is committed — a *deleted* fixture would then
read as a skip rather than a failure, silently passing a measurement that proves nothing. This reader fatals
instead. **Check whether a copied sequencing gate answers a question the new reader's own fixture can still
ask, before reusing a nearby reader's shape wholesale; a gate built for "not landed yet" is a trap once the
bytes are committed.** It also pins the line's own key set (`informationalPinnedKeys`), on the family's
`taskNotificationPinnedKeys` shape, so a `claude` release that adds or renames a key on this line reddens
the pin instead of the decode target quietly reading three fields out of a line that changed shape.

**Known gap, left open at review:** `maxBannerText`'s value (4096) is stated in four places —
`turnevent.Banner.Text`'s doc, `protocol.BannerPayload.Text`'s doc, and the constant's own doc, twice over —
and pinned by zero tests. An overlay mutation halving the constant to `2 << 10` still passes both
`internal/streamsup` test files, because each derives its fixture's over-cap length from the symbol itself
(`strings.Repeat("p", maxBannerText+1)`) rather than from the literal the docs publish. The sibling caps in
this family are all symbolic too, but those are measurements the daemon chose; this one is a number a
client was told to expect, and nothing in the diff asserts `maxBannerText == 4096`. A single literal
assertion closes it — worth adding before a later edit to this constant goes unnoticed by every test that
exercises it.
