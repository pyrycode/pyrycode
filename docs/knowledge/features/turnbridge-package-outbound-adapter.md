# The outbound adapter (`MapEvent` / `BuildTurnState`)

`MapEvent` (#627) is a pure type-switch over the sealed `turnevent.Event`,
shaping one event + an explicit `TurnContext` into the matching v2 interactive
wire payload (#607). Every field is carried verbatim from `tc` + the event:

| `ev` concrete type | `typ` | `payload` | `ok` |
|---|---|---|---|
| `TextChunk` | `TypeAssistantDelta` | `AssistantDeltaPayload{tc.ConversationID, tc.TurnID, tc.Seq, ParentToolUseID: ev.ParentToolCallID, ev.Text}` (#2329; straight-through, no re-cap — bounded at construction; empty remains main-thread text). `interactiveTurnEmitterV2` supplies main- or child-lane addressing before this call (#2330); the mapper neither derives nor checks that relationship | true |
| `ToolStart` | `TypeToolUse` | `ToolUsePayload{…, ToolUseID: ev.ToolCallID, ParentToolUseID: ev.ParentToolCallID, Name: ev.Title, InputSummary: inputSummary(ev.RawInput), Input: inputFields(ev.RawInput)}` (#1678; `ParentToolCallID` #2191, straight-through, no re-cap — bounded at construction) | true |
| `ToolUpdate` | `TypeToolResult` | `ToolResultPayload{…, ToolUseID: ev.ToolCallID, ParentToolUseID: ev.ParentToolCallID, IsError: ev.Status == ToolStatusFailed, ResultSummary: resultSummary(ev.Content), ResultDetail: ev.ResultDetail}` (#2024, extended #2025, straight-through, no cap here — see below; `ParentToolCallID` #2191, same rule) | true |
| `ToolProgress` (#2324) | `TypeToolProgress` | `ToolProgressPayload{tc.ConversationID, tc.TurnID, ToolUseID: ev.ToolCallID, ElapsedSeconds: ev.ElapsedSeconds}` — turn-scoped but lifecycle-neutral: it joins the row `ToolStart` already opened, while `ToolUpdate` remains the close. The signed reading crosses verbatim with no clock read, clamp, cadence check, lookup, retention, or deduplication; the producer already bounded the join id, and an independently dropped heartbeat carries no lifecycle meaning | true |
| `TurnEnd` | `TypeTurnEnd` | `TurnEndPayload{…, StopReason: string(ev.Reason), Outcome: ev.Outcome, IsError: ev.IsError, TerminalReason: ev.TerminalReason, ErrorCategory: ev.ErrorCategory, DurationMS: ev.DurationMS, DurationAPIMS: ev.DurationAPIMS, NumTurns: ev.NumTurns, CostUSDTotal: ev.CostUSDTotal, InputTokens: ev.InputTokens, OutputTokens: ev.OutputTokens, CacheReadTokens: ev.CacheReadTokens, CacheCreationTokens: ev.CacheCreationTokens}` (#2223/#2224/#2260/#2261, straight-through, no cap here — see below) | true |
| `Stall` (#639) | `TypeStall` | `StallPayload{tc.ConversationID}` (`tc.TurnID`/`tc.Seq` ignored — not turn-scoped, not a delta) | true |
| `ApiRetry` (#1074) | `TypeApiRetry` | `ApiRetryPayload{tc.ConversationID, ev.Active, ev.Current, ev.Total}` (`tc.TurnID`/`tc.Seq` ignored) | true |
| `Compacting` (#1074) | `TypeCompacting` | `CompactingPayload{tc.ConversationID, ev.Active}` (`tc.TurnID`/`tc.Seq` ignored) | true |
| `Unrecognized` | `TypeUnrecognizedMessage` | `UnrecognizedMessagePayload{tc.ConversationID, ev.Site, ev.Kind, ev.Raw, ev.Truncated}` (`tc.TurnID`/`tc.Seq` ignored — an unrecognized message has no turn we can honestly attribute it to) | true |
| `BackgroundTaskStarted` (#1394) | `TypeBackgroundTaskStarted` | `BackgroundTaskStartedPayload{tc.ConversationID, ev.TaskID, ev.ToolCallID, ev.Description, ev.TaskType, ev.TruncatedFields}` (`tc.TurnID`/`tc.Seq` ignored — a background task outlives the turn that spawned it) | true |
| `BackgroundTaskUpdated` (#1394; `Status`/`Summary` #2245) | `TypeBackgroundTaskUpdated` | `BackgroundTaskUpdatedPayload{tc.ConversationID, ev.TaskID, ev.Patch, ev.Status, ev.Summary, ev.TruncatedFields}` (`tc.TurnID`/`tc.Seq` ignored). Two of claude's subtypes fill this event and they fill DISJOINT fields (`task_updated` → `Patch`, `task_notification` → `Status`/`Summary`); the arm does not branch on which — it copies all four whatever their state, so an empty one crosses as empty and a non-empty `Status` is what tells a client a terminal state arrived | true |
| `BackgroundTaskRoster` (#1394) | `TypeBackgroundTaskRoster` | `BackgroundTaskRosterPayload{tc.ConversationID, tasks, ev.DroppedTasks}` — `ev.Tasks` looped into `[]protocol.BackgroundTask`, nil left nil (the payload's own `MarshalJSON` owns nil→`[]`) | true |
| `ThinkingProgress` (#1386) | `TypeThinkingProgress` | `ThinkingProgressPayload{tc.ConversationID, ev.EstimatedTokens, ev.EstimatedTokensDelta}` (`tc.TurnID`/`tc.Seq` ignored — a periodic reading of an inference request in flight, not a turn-scoped fact) | true |
| `RateLimited` (#1410, `Utilization` #2249) | `TypeRateLimited` | `RateLimitedPayload{tc.ConversationID, ev.Status, ev.LimitType, ev.ResetsAt, ev.Utilization, ev.TruncatedFields}` (`tc.TurnID`/`tc.Seq` ignored — a usage-limit window is a condition of the account, orthogonal to whichever turn observed it). Nil `TruncatedFields` left nil, and here that nil is what reaches the wire as `null`: unlike `BackgroundTaskRosterPayload` two rows up, `RateLimitedPayload` deliberately has **no** `MarshalJSON`, because nothing-was-cut is an absence. `ResetsAt` crosses unclamped and unvalidated in both directions; neither string is re-capped (the producer bounded both at construction). `Utilization *float64` (#2249) crosses **as the same pointer, not deep-copied** — `CompactionBoundary`'s stated reason applies unchanged (`encoding/json` allocates a fresh `float64` per line, nothing downstream mutates a payload) — and, like `ResetsAt`, is unvalidated and unclamped: a client scaling a progress bar by it is assuming a 0–1 domain the daemon never checked. Nil (claude reported nothing) and a pointer to `0` (claude reported a fresh window) are different facts and must stay distinguishable across this arm, which is why the row is a straight pointer copy rather than a dereference-and-rebuild | true |
| `ModelAnnounced` (#1638) | `TypeModelAnnounced` | `ModelAnnouncedPayload{tc.ConversationID, ev.Model, ev.Truncated}` (`tc.TurnID`/`tc.Seq` ignored — an announced model is a property of the turn's configuration, not a turn boundary: claude emits its `init` line once per turn, and `TestTurnMarkFor_TotalOverEveryVariant` pins the lifecycle answer as `turnMarkNone`). `Model` crosses byte-for-byte — no lowercasing, no re-cap, no charset check: the producer already bounds it at `streamsup`'s `maxModelField`, and `internal/relay`'s `validModel` is a deliberately different rule (it bounds a phone-supplied override, not a claude-supplied report). No suppression branch — a zero-value `ModelAnnounced` maps rather than dropping, the same posture `ThinkingProgress` and `RateLimited` both state. `ModelAnnouncedPayload` has no slice field and, unlike `RateLimitedPayload` one row up, no `MarshalJSON`, so the nil-vs-`[]` hazard does not arise here | true |
| `ModelRefusalFallback` (#2265) | `TypeModelRefusalFallback` | `ModelRefusalFallbackPayload{tc.ConversationID, ev.OriginalModel, ev.FallbackModel, ev.Scope, ev.RefusalCategory, ev.Banner, modelRefusalFallbackReportKeys(ev.TruncatedFields), modelRefusalFallbackReportKeys(ev.DroppedFields)}` (`tc.TurnID`/`tc.Seq` ignored — the event has no request or claude-message identity that can join it to an assistant delta). The five published source values cross verbatim and are not re-capped; `RefusalExplanation` is deliberately excluded because `Banner` is the wire's display string. The report helper preserves input order, copies retained tokens, and keeps only keys the payload publishes; nil or excluded-only input remains nil so the wire emits `null`, not `[]`. The frame explains a retry and does not replace `ModelAnnounced` as active-model authority | true |
| `ModelRefusalNoFallback` (#2266) | `TypeModelRefusalNoFallback` | `ModelRefusalNoFallbackPayload{tc.ConversationID, ev.OriginalModel, ev.RefusalCategory, ev.Banner, modelRefusalNoFallbackReportKeys(ev.TruncatedFields), modelRefusalNoFallbackReportKeys(ev.DroppedFields)}` (`tc.TurnID`/`tc.Seq` ignored for the same absent-identity reason as its fallback sibling). The three published source values cross verbatim; request/message identities and `RefusalExplanation` stay excluded, with `Banner` the sole display prose. The report helper preserves daemon order, copies retained tokens, and admits only `original_model`, `refusal_category`, and `banner`; nil or excluded-only input remains nil so the wire emits `null`. The emitter flushes pending text before publishing but does not open, transition, or close a turn, and the frame does not retry or replace `ModelAnnounced` as model authority | true |
| `SessionFacts` (#2252; mapping #2254) | `TypeSessionFacts` | `SessionFactsPayload{tc.ConversationID, ev.ClaudeCodeVersion, ev.PermissionMode, ev.TruncatedFields}` (`tc.TurnID`/`tc.Seq` ignored — a build and a posture are properties of the child run, not of a turn, the same non-turn-scoped shape as `ModelAnnounced` one row up). Both strings cross byte-for-byte — no re-cap (`streamsup`'s `maxClaudeVersionField`/`maxPermissionModeField` already bound them at construction) and no charset check (`internal/relay`'s `validModel` bounds a phone-supplied override, a deliberately different rule). Nil `TruncatedFields` left nil, and — like `RateLimited` two rows up rather than `BackgroundTaskRoster`/`ModelList`/`SlashCommandList` below — `SessionFactsPayload` owns **no** `MarshalJSON`, so that nil reaches the wire as `null`: an allocated empty slice would tell a phone claude's cut text is complete when nothing was. No suppression branch — a zero-value `SessionFacts` maps, `ModelAnnounced`'s posture unchanged. **This is the one row whose consumer `Handle` case shipped a full ticket (#2252) before this arm did (#2254)** — every other split-ticket row here landed its `Handle` case and its `MapEvent` arm together. For the #2252-to-#2254 window, grepping for the `Handle` case alone to answer "is this frame emitted?" would have given a false positive: `Handle` reached `emitMapped`, but `emitMapped` took the unmapped branch and logged a content-free Debug once per turn instead. The check that actually discriminates for a declare-then-emit split is whether this table has a row for the variant, not whether the consumer has a case for it | true |
| `MCPStatus` (#2375) | `TypeMCPStatus` | `MCPStatusPayload{tc.ConversationID, servers, ev.DroppedServers}`. Lifecycle-neutral; a fresh slice preserves row order, all five strings and the drop count. Eligibility and bounds stay upstream; `MarshalJSON` owns nil→`[]` | true |
| `ModelList` (#1848) | `TypeModelList` | `ModelListPayload{tc.ConversationID, models, ev.DroppedModels}` (`tc.TurnID`/`tc.Seq` ignored — one `initialize` exchange per child, not even per-turn, opens and closes no turn; `turnMarkFor` answers `turnMarkNone` by construction). `ev.Models` looped into `[]protocol.ModelOption`, nil left nil (`ModelListPayload.MarshalJSON` owns nil→`[]`, the `ToolStart`/`BackgroundTaskRoster` rule); each row's six fields cross verbatim, including `EffortLevels` (nil→`[]` via `ModelOption.MarshalJSON`) and `TruncatedFields` (nil stays nil — that field is deliberately exempt from normalisation, so an absence reaches the wire as `null`, the opposite polarity from `EffortLevels` one field over). `DroppedModels` is carried from the decode, never recomputed from `len(models)` and never a constant. No re-cap, re-order or charset check of any field — the producer already bounds all three dimensions (`streamsup`'s `maxModelListEntries`/`maxModelResolved`/`maxModelEffortLevel*`), and `internal/relay`'s `validModel`/`validEffort` bound a phone-supplied *inbound* value, not this outbound report. No suppression branch — a zero-value `ModelList` maps, `ModelAnnounced`'s posture unchanged | true |
| `SlashCommandList` (#2001) | `TypeSlashCommandList` | `SlashCommandListPayload{tc.ConversationID, commands, ev.DroppedCommands}` (`tc.TurnID`/`tc.Seq` ignored — the same `initialize`-exchange, not-even-per-turn addressing as `ModelList`; `turnMarkFor` answers `turnMarkNone`). `ev.Commands` looped into `[]protocol.SlashCommand`, nil left nil (`SlashCommandListPayload.MarshalJSON` owns nil→`[]`, the `ModelList`/`BackgroundTaskRoster` rule); each row's five fields cross verbatim, including `Aliases` (nil→`[]` via `SlashCommand.MarshalJSON`) and `TruncatedFields` (nil stays nil — deliberately exempt from normalisation, so an absence reaches the wire as `null`, the opposite polarity from `Aliases` one field over). `DroppedCommands` is the decode's count **plus** whatever the frame cut below drops, never recomputed from `len(commands)` alone and never a constant. No re-cap, re-order or charset check of any dimension the producer already bounds (`streamsup`'s `maxSlashCommandName`/`maxSlashCommandDescription`/`maxSlashCommandArgumentHint`/`maxSlashCommandAlias`/`maxSlashCommandAliasCount`/`maxSlashCommandListEntries`), and `Name` is not an identifier (`__remote-workflow` is in the committed capture) so no charset assumption belongs here either. No suppression branch — a zero-value `SlashCommandList` maps, `ModelList`'s posture unchanged. **Frame-size bound (#2002):** the producer's content caps alone allow a payload several times the 65519-byte v2 envelope, and no *count* cap can close that — the only entry count whose worst case fits (51) is the committed capture's own size, so it would fire on claude's ordinary output. The arm instead walks `e.Commands`, marshals each row as the wire `protocol.SlashCommand` (not the turnevent one — its `MarshalJSON` normalisations are part of what actually crosses), and takes the tail-cut prefix that fits under `maxSlashCommandListBytes` (64000 B), `break`ing rather than skipping on the first row that would not, so a client sees a shortened menu rather than one with holes | true |
| `ContextUsage` (#2371) | `TypeContextUsage` | `ContextUsagePayload{tc.ConversationID, ev.Model, ev.TotalTokens, ev.MaxTokens, ev.Percentage, categories, ev.DroppedCategories, mcpTools, ev.DroppedMCPTools, memoryFiles, ev.DroppedMemoryFiles}` (`tc.TurnID`/`tc.Seq` ignored — the reading arrives *after* the turn it describes has closed, solicited by `cmd/pyry`'s `turnEndContextUsageRequester` on `TurnEnd` (#2289), so an arm that opened a turn here would mint one nothing will ever end; `turnMarkFor` answers `turnMarkNone` by construction, pinned by `TestTurnMarkFor_TotalOverEveryVariant`). The three inventories are each rebuilt as a fresh outer slice by a read-only loop, nil left nil (`ContextUsagePayload.MarshalJSON` owns nil→`[]`, the `ModelList`/`SlashCommandList` rule) — unlike those two neighbours there is no `TruncatedFields` asymmetry here: all three lists share the same nil-forwarding polarity. The three dropped counts are carried independently, never recomputed from a retained list's length and never cross-read with one another — measured against the fake-Claude capture they land at **0/1/1**, not the three mutually distinct values an earlier draft of this ticket's Technical Notes assumed; the unit fixture instead uses mutually distinct counts (3/5/7) so a cross-wired pair still reddens. The four scalars (`Model`, `TotalTokens`, `MaxTokens`, `Percentage`) are claude's own arithmetic and are never derived, defaulted or cross-checked against each other — the mapper does not verify `Percentage` against the two token totals, nor sum `Categories` against `TotalTokens`. **`MemoryFile.Path` is never normalised** — no `filepath.Clean`/`Join`/`Abs`/`Stat`/`Match` — normalising would imply the frame names a real file the daemon acts on, which it does not; a traversal-shaped path in the mapper's fixture crosses byte-for-byte to pin this by test rather than by comment. **`MCPTool.ServerName` is inert** here and collides in name only with `MCPReconnectPayload.ServerName`, which *is* an actuation target; no helper is shared between the two arms, deliberately. Carry-never-mutate holds by construction rather than by discipline: all three row types are flat `string`/`int` structs with no slice field, so the element-wise copy shares no mutable backing array — there is no `sessionModelHold`-style retention of a `ContextUsage` the way `ModelList` has, so that arm's cross-goroutine hazard does not arise here. **Frame byte budget (#2428):** the producer's content caps alone (`maxContextUsageEntries`/`maxContextUsageStringBytes`) allow 45568 B raw / 251648 B HTML-escaped against the 65519 B v2 envelope cap — a count cap can't close that gap. The arm round-robins the three lists against one shared `maxContextUsageListBytes` (60000 B): round *n* offers every still-open list its entry at index *n*, order `categories`→`mcp_tools`→`memory_files`; a list closes (never skips) on its first row that doesn't fit; the walk ends the first round nothing is admitted. Each kept list is a prefix of the producer's order, and each dropped count is the producer's base plus this cut's own removal — never cross-read. Equal turns, not equal bytes, keeps one fat list from starving its neighbours; round 0's worst case sits well under budget, so the one-entry-per-list floor holds by arithmetic. Escaped worst case: 61240 B envelope (93.5% of cap), 8/8/7 of 32 per list kept — short, not roomy. Pinned by `TestMapEventContextUsageWorstCaseAgainstV2EnvelopeCap`; lower the constant if it fails, never raise it. Every string here is claude- or workspace-authored and none reaches a log record — the arm itself writes no log line, and `cmd/pyry`'s `eventKind` case for this variant returns the bare variant name only. No suppression branch, not even on three empty inventories — a zero-value `ContextUsage` maps, `ModelList`'s posture unchanged | true |
| `CompactionBoundary` (#2237) | `TypeCompactionBoundary` | `CompactionBoundaryPayload{tc.ConversationID, ev.Trigger, ev.PreTokens, ev.PostTokens}` (`tc.TurnID`/`tc.Seq` ignored — a compaction boundary is a fact about the conversation's context, not about the turn that happened to contain it). Nothing is re-capped here: the producer bounded `Trigger` at construction. The two count pointers cross **verbatim, not deep-copied** — the producer allocates a fresh `int` per line and retains neither, and nothing downstream mutates a payload, so the aliasing is observable to no one | true |
| `ToolCallDenied` (#2233) | `TypeToolDenied` | `ToolDeniedPayload{tc.ConversationID, tc.TurnID, ToolUseID: ev.ToolCallID, ToolName: ev.ToolName, DecisionReasonType: ev.DecisionReasonType, DecisionReason: ev.DecisionReason, Message: ev.Message, TruncatedFields: deniedReportKeys(ev.TruncatedFields), DroppedFields: deniedReportKeys(ev.DroppedFields)}` — **turn-scoped**, taking `ToolStart`/`ToolUpdate`'s shape rather than the status peers' (`tc.Seq` still ignored): the denial names one call the client already has a `tool_use` for and must land in the same turn. Every string crosses verbatim and nothing is re-capped — the producer bounded all five at construction (`streamsup`'s `maxTaskFieldID`/`maxDenialProse`) | true |
| `Banner` (#2256) | `TypeBanner` | `BannerPayload{tc.ConversationID, ev.Level, ev.Text, ev.Truncated, ev.StopsTurn}` (`tc.TurnID`/`tc.Seq` ignored — a banner rides no turn the daemon could honestly attribute it to). All four claude-side values cross verbatim; the arm bounds, drops, cuts, defaults or recomputes none of them — in particular it never recomputes `Truncated` from `len(ev.Text)`, on `ToolDeniedPayload`'s one-cap-site rule: the 4 KiB bound on `Text` belongs to the producer (#2257) alone | true |
| `ThoughtChunk` | `""` | `nil` | **false** (drop) |
| nil / unknown | `""` | `nil` | false (drop) |

- `payload` is `any` because the payload structs share no marker interface; the
  consumer `json.Marshal`s it directly (same path as `MessagePayload`). It is always
  one of the concrete `protocol.*Payload` value structs, or `nil` when `!ok`.
- **Zero-value-safe.** A nil `ev` falls to the default → drop. Because #607's
  payloads carry no `omitempty`, boundary zero-values (`seq:0`, `is_error:false`)
  are always serialized — they reach the wire rather than vanishing.
- **Internal-only fields are not forwarded.** `ToolStart.Kind`/`Locations` and
  `*.MessageID` have no #607 wire home and are correctly dropped.
- **`is_error = (Status == ToolStatusFailed)`** — `completed`/`pending`/`in_progress`
  all map to `false`. Round-trips with the inbound `toolStatus` (failed↔error,
  completed↔success).
- **`TurnEnd`'s stop-shape fields cross straight through, deliberately uncapped
  at this arm — three from #2223, a fourth, `ErrorCategory`, from #2224** —
  `ToolUpdate`'s `ResultDetail` row above is the standing argument for that
  shape: every string is bounded at *construction* by their producer
  (`streamsup`'s `maxTurnEndStopField`), so a second bound here would be a
  number to keep in step with one that already holds, not a second line of
  defence. `ErrorCategory` is the one field in the set read off an `assistant`
  line rather than the `result` line that ends the turn — this arm still maps
  it straight through unconditionally, because by the time an event reaches
  `MapEvent` the parser has already decided what, if anything, that turn's
  category is; the arm has no visibility into which line produced a field and
  needs none. **#2260's four numbers (`DurationMS`, `DurationAPIMS`, `NumTurns`,
  `CostUSDTotal`) cross the same way, but for a different reason than the
  strings beside them: there is no producer-side cap to stay in step with at
  all** — `streamsup`'s `decodeTurnTotals` applies no clamp, range check or
  ordering check to any of the four, so this arm's uncapped pass-through is
  the *only* posture available, not a second line of defence declined in
  favour of a first one. `DurationAPIMS`/`CostUSDTotal` are session running
  totals the arm does not difference into a per-turn figure; nothing here
  reads a prior event or holds state across turns to do that with anyway,
  which is a structural argument, not merely an unimplemented one. **#2261's
  four token counts cross by the same direct-copy rule but are all per turn;
  the arm neither sums them nor treats uncached `InputTokens` as total input.
  The two cache fields are input-side despite their shortened wire names. The
  mutually distinct mapping-test values are load-bearing because they catch a
  crossed field or a derived total that a round-trip alone would preserve. The
  window pair on the same variant (`ModelWindows`,
  `DroppedModelWindows`) is still not forwarded** — this arm builds the payload
  field by field, so what reaches the wire is exactly what is named in the arm and
  nothing else. This is the first ticket where `TurnEnd` carries both a published
  and an unpublished field side by side, so **a row asserting the window pair
  stays off the payload is now load-bearing** in a way it was not before #2223:
  before this ticket "nothing on `TurnEnd` reaches the wire but `Reason`" was true
  of the whole struct, and any test embedding the struct wholesale would have
  failed to compile against `protocol.TurnEndPayload`'s narrower shape; now that
  `TurnEnd` has some published and some unpublished fields, a future arm could
  embed the struct, compile, and silently leak the window pair, so only an
  explicit "still absent" assertion — not the type system — protects it. The
  general rule for the next event to grow a mixed published/unpublished field
  set: the moment the first field on a variant is published, the *rest* need a
  test they never needed while none of them were.
- **`ThoughtChunk` drops (ADR 025).** #607 defines no thought-text envelope and
  ADR 025 classes thinking as screen-sourced; so the thought *text is not forwarded*.
  The thinking **state** surfaces via `BuildTurnState(convID, StateThinking)`, which
  the **consumer's** lifecycle machine calls when it observes a `ThoughtChunk` —
  deciding "a ThoughtChunk means we are thinking" is a lifecycle decision, kept out of
  the pure mapper. The mapper supplies the *builder*; the consumer owns the *decision
  to call it*.
- **A new row's sentinel has to falsify what the row claims, not just differ from
  `tc`.** `RateLimited`'s rows use short, all-lowercase sentinels — fine for its own
  hazards, but silent on "no lowercasing": an all-lowercase `Model` sentinel survives a
  `strings.ToLower` mapper. `ModelAnnounced`'s rows (#1638) needed a mixed-case
  sentinel to kill that mutant, and a >256-rune one to beat both the producer's
  `maxModelField` (256) and this file's own `maxSummaryLen` (200) for a re-cap mutant
  at either bound to go red.
- **A "value never reaches a log" test needs a positive control that the value
  traversed the path at all**, or a consumer `Handle` arm that silently drops the
  event passes the test for the wrong reason. `ModelAnnounced`'s extension (#1638) to
  `cmd/pyry`'s `TestInteractiveTurnEmitterV2_NoAppOutputLogLeak` pairs log-absence
  with a decoded-payload presence assertion on the recorded push; the payload half is
  what actually caught the arm dropping the event silently — the log-absence half
  alone stayed green throughout.
- **A hand-enumerated leak sweep does not widen when the struct it guards does.**
  `TestInteractiveTurnEmitterV2_BackgroundTasksNoLogLeak` lists one marker string
  per claude-derived field, by hand, across all three background-task variants.
  Adding `Status`/`Summary` to `BackgroundTaskUpdated` (#2245) would have left both
  silently unswept — no red, no signal — without someone remembering to add their
  markers, and `Summary` is the field on this family with the most to leak
  (unbounded model prose). The fix also needed a **second** event of the variant,
  because `task_updated` and `task_notification` fill disjoint fields and one
  fixture leaves half the variant's fields permanently unset and so permanently
  unswept. Check this test whenever a variant already in its list grows a field.
- **Pinning "entry order is preserved" needs a non-monotonic fixture, not just two
  distinguishable entries.** `ModelList`'s outbound row (#1848) uses two entries
  whose `ResolvedModel`/`Value`/`DisplayName` all happen to sort ascending; the
  row's own comment claims a reversal *or a re-sort* is caught, but an ascending
  canonicalising sort is a no-op against an already-ascending fixture — only the
  reversal is. Code review found this by running the sort mutant, not by reading
  the comment. Two entries can only ever be monotonic or reversed; three
  non-monotonic entries (e.g. Beta, Alpha, Gamma) are the minimum that catches a
  sort in either direction. Unfixed as of #1848 (a single SHOULD FIX, below the
  review's three-finding action threshold) — the fixture and its overclaiming
  comment still stand; correct both the next time this row is touched.
- **A row pinning a mutate-through mapper needs its `ev` and `wantPayload` built
  from two separate slice literals, not one shared between them.** If the
  expected payload is built by re-slicing the same backing array as the input
  event, an in-place sort or dedupe inside the mapper's loop mutates both sides
  together and `reflect.DeepEqual` stays green. `ModelList`'s rows (#1848) spell
  the same effort-level and truncated-field slices out twice on purpose. This is
  more than a test nicety here: `sessionModelHold` (#1840) retains the same
  `turnevent.ModelList` without copying and is read on a relay-leg goroutine, so a
  mapper that mutated through would be a data race in production, not merely a
  wrong test result. The same asymmetric-nil hazard applies to a byte-level wire
  test: `ModelOption.EffortLevels` (nil→`[]`) and `TruncatedFields` (nil stays
  `null`) sit on adjacent fields with opposite rules, and copying either row's
  `want`/`notWant` pair onto the other passes against exactly the allocating
  mapper the test exists to catch.
- **A "fresh outer slice" pin cannot be written by aliasing through the mapped
  element, when the source and destination element types differ.** `SlashCommandList`'s
  arm (#2001) considered a row that writes a whole element through
  `payload.Commands[0]` and asserts the event's row is unchanged — the shape
  `TestMapEventSlashCommandListDoesNotMutateTheEvent` was drafted around. It can
  never fail: `turnevent.SlashCommand` and `protocol.SlashCommand` are distinct
  types, so nothing lets `payload.Commands[0]` alias `ev.Commands[0]`'s memory in
  the first place, and a check that is green against every possible
  implementation is not a pin. Cut before merge; the read-only half (assert the
  event is `reflect.DeepEqual` to a pre-call snapshot after `MapEvent` runs) is
  the one that actually reddens on a mutant, since the loop reads `e.Commands`
  by value into a fresh local rather than writing back through it. Before
  drafting a mutate-through assertion for a new arm, check whether the source and
  destination element types could even alias — if they can't, the assertion
  belongs on the source side, not the destination.
- **A tail-cut's `break`-vs-`continue` choice needs a fixture with rows of
  different sizes, not just more rows than the budget allows.** `SlashCommandList`'s
  frame-size cut (#2002) walks `e.Commands` and must stop at the first row that
  would overflow `maxSlashCommandListBytes` rather than skip it and try smaller
  rows behind it — skipping would hole-punch the menu and make the retained set
  order-dependent. An over-budget fixture built from uniformly worst-case-sized
  rows cannot catch a `break`→`continue` mutant: dropping *any one* row frees the
  same number of bytes, so the kept count and total size are identical either
  way, and `go test -overlay` proved the whole package green against that mutant.
  The fixture that catches it is decorrelated — several worst-case rows followed
  by a few tiny ones — so a skipping implementation admits a small row behind the
  cut and a prefix assertion (kept rows are a strict prefix of the input, in
  order) names it. Order the assertions so the prefix check runs before any count
  precondition: a skipping implementation keeps *more* rows, and a count guard
  checked first reports that as a stale fixture rather than as the bug.
- **A wire-size budget's reserve margin should track what is actually left
  unbounded outside it, not mimic a sibling constant's margin by default.**
  `maxDeltaTextBytes` and `maxInputTotalRunes` both reserve against an
  essentially unbounded input field. `maxSlashCommandListBytes` (#2002) can
  afford a tighter reserve (~2.7x its measured worst-case slack, against
  sibling margins several times that) because everything else in the payload
  and envelope is already bounded elsewhere — the one exception is
  `conversation_id`, which nothing in this package caps. Pick the margin from
  what is genuinely still unbounded, not from the neighbouring constant's
  number.
- **Measure a mapped list's wire cost against the *wire* type, not the
  internal one, when the two have different `MarshalJSON` behaviour.** The
  frame-size cut (#2002) marshals each row as `protocol.SlashCommand`, not
  `turnevent.SlashCommand` — the wire type's nil-`Aliases`→`[]` normalisation
  and JSON key names are both inside the bytes that actually cross, and a
  measurement taken against the internal type would under-count every row
  with a nil `Aliases`.
- **A frame with more than one independently bounded list should apportion a
  shared byte budget round-robin, not copy a sibling's single-list tail-cut
  or split the budget into fixed per-list shares.** `ContextUsage`'s three
  inventories got their budget decision in #2428, after #2371 deferred it:
  round *n* offers every still-open list its entry at index *n*, closing a
  list rather than skipping past a row that doesn't fit. Fixed thirds waste
  the envelope — one full lane and two empty ones still gets cut a third of
  the way through with the rest unspent. A sequential walk with a reserved
  floor is comparable code with a worse outcome: it can still land the
  lopsided "32 of one, 1 of each other" shape the floor exists to avoid.
  Round-robin gives every lane equal turns rather than equal bytes, so a fat
  lane's rows cost it turns rather than starving a cheaper-rowed lane.
- **A per-item cut sharing a loop with a global "stop when nothing was
  admitted this round" termination needs a fixture with a *second* list
  still open, or the per-item close flag is never exercised.** The
  round-robin walk (#2428) ends the round the moment every open lane
  rejects, so with only one lane populated a rejection ends the walk whether
  or not that lane is separately marked closed. #2002's "decorrelate row
  sizes so `break` differs from `continue`" lesson, carried into the plan,
  was not sufficient alone here; the fixture also needed a second,
  cheaper-rowed list still open past the first list's cut before a mutation
  deleting the close flags reddened.
- **Equal-length lists collapse a cross-wired dropped count onto the correct
  answer.** Three 32-entry lists cut 24/24/25 mean a mapper adding one
  list's cut onto a *different* list's counter lands on the number the
  correct mapper produces anyway — distinct bases (3/5/7) alone didn't make
  it observable; the lengths also had to differ (32/20/12, cutting 24/12/5).
- **The first arm that rewrites a report slice's *contents* changes what "pure"
  costs, and a `reflect.DeepEqual` snapshot does not catch the aliasing on its
  own.** Every arm before `ToolCallDenied` (#2233) either reads a slice or
  re-slices/loops into a fresh one; `deniedReportKeys` (below) is the first to
  rewrite one element of the producer's own slice — renaming
  `turnevent.ToolCallDenied`'s `tool_call_id` report token to this frame's
  `tool_use_id` wire key — and the same event is also read by `cmd/pyry`'s
  history-append path and by `eventKind`'s other call sites. A rename that
  wrote back through the producer's array, or returned it with one element
  substituted, would make this arm's output visible to those other readers.
  `deniedReportKeys` copies unconditionally, even when no token changes, and
  the test proves it by writing a sentinel *through the payload's slice* after
  the call and asserting the event's own slice is unchanged — comparing the
  event to a pre-call `DeepEqual` snapshot alone would pass a rename that
  happened to write back the *same* value.
- **A payload field's name can diverge from the event field it maps, and when
  it does, a report slice naming that field has to be translated at the same
  seam.** `ToolDeniedPayload.ToolUseID` is `ToolCallDenied.ToolCallID` renamed
  (`ToolUsePayload`/`ToolResultPayload`'s `tool_use_id` precedent), but
  `ToolCallDenied.TruncatedFields`/`DroppedFields` still name that field
  `tool_call_id` — their own producer's field name. Nothing in the type system
  catches a missed rename here: both sides are plain `[]string`, and a
  round-trip fixture is byte-equal whichever token is inside it. The failure
  only surfaces when a client tries to look the reported token up against the
  frame's own keys and finds none. See
  [protocol-package-interactive-event-payloads.md](protocol-package-interactive-event-payloads.md)
  for the payload-side shape, the three-valued reading the two slices make
  decidable, and a doc-comment ordering claim about these same slices that
  shipped false.
- **Filtering a report token for an unpublished field must preserve the report's
  nil semantics as well as its vocabulary.** Both refusal frames (#2265/#2266) omit
  `RefusalExplanation`, so their report helpers remove that field's token. Returning
  an allocated empty slice when it was the only token would encode `[]` and falsely
  distinguish the result from "no published field was reported"; starting with a nil
  output preserves `null`. Each helper also copies retained tokens even though their
  spelling is unchanged, because returning a subslice would let payload mutation
  rewrite the daemon event observed by history and logging.
- **A pure pass-through arm's own test needs a row that can fail in the
  pass-through direction, not merely a row that exercises the field.**
  `Banner`'s arm (#2256) copies `Truncated` verbatim rather than recomputing it
  from `len(ev.Text)`, and every *short*-text row in
  `TestMapEvent_BannerCrossesVerbatim` is equally green against either
  implementation — a length-based mutant only dies on a `Text` past the
  producer's 4 KiB bound carrying `Truncated: false` (the producer chose not to
  flag it), paired with a short `Text` the producer did flag `true`. A single
  row in either direction alone is satisfied by the inverted rule. The same
  shape as the `ModelAnnounced` mixed-case-sentinel lesson above, generalized
  from "does the value survive a transform" to "does the flag disagree with a
  plausible recomputation the arm must NOT perform."

## `BuildTurnState` — the lifecycle payload builder

Returns `TypeTurnState` + `TurnStatePayload{conversationID, string(state)}`. Concrete
return type (not `any`) because it is monomorphic — no consumer type assertion. The
consumer's lifecycle machine decides *which* state applies (thinking / responding /
idle) and calls this; the adapter only shapes the payload.

## Summary derivation (`inputSummary` / `resultSummary` / `truncate`)

The wire envelopes carry a human-readable **précis** (not the raw input/output). Three
pure helpers derive a bounded, single-line summary:

- **`inputSummary(json.RawMessage)`** — `json.Compact` (whitespace → one line) then
  `truncate`. Empty/nil **and** invalid-JSON both yield `""` — `RawInput` is
  best-effort/opaque (#606), so a malformed blob is a précis-less `tool_use`, not an
  error.
- **`resultSummary(turnevent.ToolContent)`** — **exhaustive** over the sealed
  `ToolContent` sum type so a future producer variant cannot silently vanish:
  `nil`→`""` (the legal status-only `ToolUpdate`), `TextContent`→its text,
  `DiffContent`→`Path`, `TerminalContent`→`"terminal <id>"` — each truncated at
  `maxResultSummaryRunes` (10000, #1680), not `maxSummaryLen`. The cap is inert
  for the Diff/Terminal arms (neither approaches it) and applies to all three
  anyway — one rule is cheaper to read and test than a per-arm exception. `is_error`
  does not change the bound: the flag is derived at the `MapEvent` call site and
  never reaches `resultSummary`, so a failed tool's result is truncated exactly as
  a successful one's is. The live inbound producer (`internal/streamsup`'s
  `toolResultContent`) only ever emits `TextContent` or `nil`; the Diff/Terminal
  arms are unreachable today but handled (kept deliberately minimal) until a
  producer (the ACP adapter #600, or a refinement) emits them.
- **`truncate(s, max)`** — returns `s` unchanged at ≤ `max` runes; otherwise cuts at
  `max` runes (`[]rune`, not bytes) and appends `"…"`. Rune-aware so multibyte text
  never splits mid-rune.

`const maxSummaryLen = 200` bounds the **input** précis to one line of ≤ 200
runes — a **phone-display** bound, not a wire constraint (the envelope cap is far
larger); tunable if the mobile view wants a different cap. It stopped bounding the
result précis in #1680: results are an order of magnitude bigger than inputs
(11379 measured local tool results, mean 2959 characters, median 721; only 22%
survived the 200-rune cap whole) and the producer applies no cap of its own, so
the result side needed its own **wire** constraint rather than a display one.

`const maxResultSummaryRunes = 10000` is that constraint — deliberately
`maxDeltaTextBytes`'s number (`cmd/pyry`, the other free-text field on a v2
envelope), so the two don't drift apart for no reason. Its doc comment carries
the full six-bytes-per-rune arithmetic (`encoding/json`'s `SetEscapeHTML`
default makes `'<'`/`'>'`/`'&'`/control bytes cost 6 wire bytes each, a multibyte
rune is emitted raw so it never gets worse) and a never-raise rule with two
reasons: the measured worst case (a `'<'`-filled 64525-rune result with hostile
64-rune identity fields) is 61363 B against the 65519-byte envelope cap — 93.7%,
~4.2 KB of headroom — and `tool_result` is control-class in `internal/eventring`,
preferentially retained up to `MaxEventsPerConversation` (1024) per conversation
holding the marshalled bytes, so raising the rune cap also multiplies the ring's
worst-case per-conversation footprint (~1.4 MiB → ~59 MiB at 10000). 16000 was
measured and rejected at 96309 B, 47% over the cap — exceeding it doesn't
truncate the frame, it **loses** it, and `tool_result` is never-droppable, so the
operator would see an empty row rather than a shortened one.

**A worst-case envelope measurement has to pick the worst case of its `bool`
fields too.** `TestToolResultPayload_FitV2EnvelopeCap` (`internal/turnbridge`,
not `internal/protocol` — see the `maxInputFields` lesson below on why a test
that can't call the unexported helper doesn't stand on the constant) drives the
real `resultSummary` and logs 61363 B, one byte over the spec's hand-computed
61362: `"is_error":false` costs one more wire byte than `true`, and the field is
never `omitempty`. The test's two mutants (`maxResultSummaryRunes` → 16000; the
`TextContent` arm returns `v.Text` uncapped) die at different rungs — the
uncapped arm is caught by the test's rune-count precondition before anything is
marshalled, while only the raised-cap mutant reaches the `< 65519` byte
assertion. That split means the precondition is load-bearing coverage, not a
courtesy guard: deleting it as "redundant" would leave the uncapped-arm mutant to
die on a byte-count failure instead of a legible message naming the arm.

`docs/protocol-mobile.md` § `tool_result` documents the wire shape (the
10000-rune cap, the `…` marker, that `is_error` does not change the bound, and
the six-bytes-per-rune arithmetic) and states the same **display string, not a
capability** hazard § `tool_use` states for `Input` — a tool result is raw
command output or file contents, so a `'<'`-dense result is the ordinary case,
not the contrived one.

**The measured worst case moved with #2024's `ResultDetail` field, and stayed put when #2025
extended it to five shapes** (see
[protocol-package-interactive-event-payloads.md](protocol-package-interactive-event-payloads.md)):
`TestToolResultPayload_FitV2EnvelopeCap` carries it at its own 48-byte
constructed worst case alongside the capped `ResultSummary`, measuring
**61430 B, 93.8%** of the 65519-byte cap — up from 61363 B / 93.7%, exactly
the field's 48 bytes plus 19 B of key and punctuation. The 48-byte bound is
re-derived across all five of #2025's forms (edit 43, write 36, shell/search
25) and the read shape stays the longest, so this measurement did not move a
second time. `ResultDetail` needed no rune cap of its own (see the protocol
doc) because every count formats through `strconv.FormatInt` plus fixed
literals — never claude's text carried through — even though two of those
literals (U+2212, U+00B7, #2025) are non-ASCII; `encoding/json` escapes
neither, so the wire-cost arithmetic survives unchanged. It still eats
headroom on this payload, **~4.1 KB**. The next field added here inherits the
smaller number.

## Per-field input extraction (`inputFields` / `inputValue`, #1678)

`inputSummary` flattens the whole tool input to one 200-rune line, which buries
the field an operator actually wants (an `Edit`'s `file_path`) inside the bulk
text it precedes. `inputFields(ev.RawInput)` sends the input's own top-level
fields instead, each bounded independently, and populates `ToolUsePayload.Input`
alongside — not instead of — the unchanged `inputSummary`. `RawInput` is read
twice on the same `ToolStart` arm and the two readings are deliberately
independent: a summary derived *from* the capped fields would silently change
`inputSummary`'s value, which this ticket must not do.

Behaviour: `len(raw) == 0`, a non-object (array/number/string/malformed), or an
empty object all yield `nil` — `inputSummary`'s existing "malformed blob is a
field-less tool_use, not an error" posture, extended to the map. Every value is
the input's own value **verbatim**: a JSON string is *decoded* (so an embedded
newline is a newline, not a re-quoted JSON literal), any other JSON type is its
compact JSON form. Entries are admitted **shortest value first**, ties broken by
key, against `maxInputTotalRunes`; an entry that does not fit whole is dropped,
never shortened, and the walk stops there. Shortest-first is what actually fixes
the reported bug — for a `Write{content, file_path}` or an `Edit{file_path,
new_string, old_string}`, the short identifying field is admitted before the
bulk text can spend the budget. **Sorted-key order silently reproduces the
original complaint**: `content` sorts before `file_path` and the budget is spent
before the identifying field is ever considered — this is a real trap, not a
hypothetical one, and any future rework of the admission order needs a
regression case shaped like it (`TestInputFields`'s `Write`-shaped row pins it).

**A `json.Unmarshal` trap that shipped correct only because a test happened to
catch it:** deciding "is this value a JSON string" by *trying* `json.Unmarshal(v,
&s)` and checking the error is wrong — a JSON `null` unmarshals into a string
successfully and leaves `""`, silently rendering `null` as an empty value instead
of the literal `"null"` its non-string sibling values get. `inputValue`
discriminates on the value's **leading `"` byte** instead, which is the only form
immune to this. The bug was caught in code review, not by design — the one table
row in `TestInputFields` covering non-string values happened to include a `null`
alongside a number, bool, array and nested object. A future edit to that row that
drops the `null` case would let a regression back in unnoticed.

Four new constants beside `maxSummaryLen`, each carrying its escaped-byte
arithmetic in its own doc comment in `maxDeltaTextBytes`'s form (`cmd/pyry`):
`maxInputValueRunes` (4000, per-value), `maxInputKeyRunes` (128, drops rather
than truncates an over-long key — a cut key would misname the field, where a
cut value is honestly marked with `…`), `maxInputFields` (16, an entry-count
cap the rune budget alone cannot substitute for, since per-entry JSON
structure costs bytes a content budget cannot see), and `maxInputTotalRunes`
(8500, keys and values summed, measured against the 65519-byte envelope cap by
`protocol.TestToolUsePayload_FitV2EnvelopeCap`). **`maxInputFields` shipped
correct but with no test standing on it**: code review found that deleting its
guard leaves both `internal/turnbridge` and `internal/protocol` fully green —
`TestToolUsePayload_FitV2EnvelopeCap` lives in `protocol`, builds its map by
hand, and cannot call the unexported `inputFields` at all, so it measures the
*envelope*, not the *producer's* entry-count guard. Not fixed as part of #1678
(non-blocking); a future touch of `inputFields` should add the missing
`TestInputFields` row (an input with more than `maxInputFields` entries,
asserting exactly `maxInputFields` survive) rather than assume the envelope test
already covers it.

`docs/protocol-mobile.md` § `tool_use` documents the wire shape (per-value cap,
total bound, `…` marker, empty-map polarity, and that `Input` values are
**display strings, not capabilities** — a `file_path` is model-authored text the
daemon neither resolved nor validated, and a client must not open or execute one
on its own).

## What the outbound adapter does NOT do (the seam)

`MapEvent`/`BuildTurnState` produce only the typed payload + discriminant. The
consumer (integration slice, building on #616's fan-out) owns the envelope `ID` mint,
`TS` clock read, `json.Marshal`, AEAD seal, `Push`, the drop-log for un-mappable
events, **and** every lifecycle decision (which conversation/turn/seq/state applies,
turn-id assignment, seq advancement, coalescing). See
`cmd/pyry/session_transition_v2.go` for the existing shape that wraps a payload into
an `Envelope`; the structurally-identical v2 coarse emitter `assistant_turn_v2.go`
was removed in [#699](../codebase/699.md), and the v1 coarse bridge
`cmd/pyry/assistant_turn.go` this paragraph originally also pointed at was removed in
[#913](../codebase/913.md). This is why the adapter is pure: every clock read,
counter, and I/O lives in the consumer.

`interactiveTurnEmitterV2` therefore owns the parent-keyed assistant-lane state
(#2330). The empty parent uses the existing outer-turn id and sequence; each
non-empty parent lazily receives a distinct stable id and independent counter for
that outer turn. Its coalescer intentionally remains **one active buffer keyed by
both parent id and message id**. A buffer per lane looks natural but can delay an
earlier child until after later main or sibling-child prose, destroying global
arrival order; the single buffer flushes on either key change and still lets
same-lane, same-message text coalesce. Turn end and conversation switch flush before
discarding all lane state. Every flushed chunk then returns through `MapEvent` and
the ordinary emitter path, so attributed prose does not acquire a second mapping,
splitting, replay-ring, history, droppable-classification, or fan-out path.
This isolation does not enable the subprocess's subagent-text forwarding flag;
that production switch and its live-Claude proof belong to #2331.

A lifecycle-neutral emitter test that begins with an open turn proves only that the
event does not alter that turn. It stays green if the handler accidentally opens a
turn from idle. Passive event variants therefore need a separate idle-state assertion
that checks both lifecycle state and the absence of synthetic `turn_state` frames.
