# The outbound `Event` variants (`event.go`, `permission.go`)

Six ACP-shaped outbound turn events plus three internal-only status peers (`Stall`,
`ApiRetry`, `Compacting`). `Stall` and `ApiRetry` are still PTY-derived with no
producer since #1348 deleted the terminal-driving path that built them; `Compacting`
is not — `internal/streamsup`'s `emitCompactingStatus` has mapped it off the
stream-json `system/status` line since #2227, the first of the three to regain one:

| Type | Fields | Notes |
|---|---|---|
| `TextChunk` | `MessageID, Text string` | incremental assistant text, grouped by message |
| `ThoughtChunk` | `MessageID, Text string` | streaming reasoning ("thinking") text |
| `ToolStart` | `ToolCallID, ParentToolCallID, Title string`, `Kind ToolKind`, `RawInput json.RawMessage`, `Locations []Location` | a new tool invocation; `ParentToolCallID` (#2191) is the spawning call's own `ToolCallID`, empty on the main conversation — dropped, not cut, as a join key past `maxTaskFieldID` |
| `ToolUpdate` | `ToolCallID, ParentToolCallID string`, `Status ToolStatus`, `Content ToolContent`, `ResultDetail string` | changed fields of an existing tool call; `Content` may be `nil` (status-only update). `ParentToolCallID` (#2191) is `ToolStart`'s field, read off the `user` line, not latched. `ResultDetail` (#2024, all five sidecar shapes since #2025) is daemon-composed display text derived from claude's stdout sidecar — a read's or shell's line count, an edit's `+10 −3`, a write's `created · 54 lines`, a search's `78 lines`/`5 files` — named generically because it composes across shapes rather than minting a new event field per shape. Its two separator glyphs (U+2212, U+00B7) are the field's first non-ASCII bytes; every byte is still daemon-formatted from claude's counts, never claude's text passed through — see [streamsup-package-content-blocks-are-held-as-json-rawmessage.md](streamsup-package-content-blocks-are-held-as-json-rawmessage.md) |
| `TurnEnd` | `Reason TurnEndReason`, `ModelWindows []ModelWindow`, `DroppedModelWindows int` (#2101), `Outcome string`, `IsError bool`, `TerminalReason string` (#2223), `ErrorCategory string` (#2224), `DurationMS, DurationAPIMS, NumTurns int`, `CostUSDTotal float64` (#2260) | end of a claude turn; `Reason` is the daemon's two-value classification, `ModelWindows`/`DroppedModelWindows` are unreachable on the wire, `Outcome`/`IsError`/`TerminalReason` are claude's own stop shape read off the `result` line, `ErrorCategory` is claude's API-failure category read off an `assistant` line, and `DurationMS`/`DurationAPIMS`/`NumTurns`/`CostUSDTotal` are four more numbers off the `result` line — `DurationMS`/`NumTurns` per turn, `DurationAPIMS`/`CostUSDTotal` running totals the daemon never differences — all claude-authored fields on this variant DO reach the wire — see below |
| `ModelWindow` (#2101, element type — not an `Event`, no marker) | `ModelID string`, `WindowTokens int` | one model's context-window reading off the `result` line's `modelUsage` map, sorted by `ModelID` |
| `Stall` (#638) | *none* (`struct{}`) | **internal-only** onset marker; no ACP equivalent — mobile adapter sends it, the future ACP adapter (#600) drops it; see below |
| `ApiRetry` (#1074) | `Active bool`, `Current, Total int` | **internal-only** status peer of `Stall`: claude's live API-error retry state. `Active` is the rising/falling edge; `Current`/`Total` are the parsed `attempt N/M` counter (`{0,0}` when unparsed) |
| `Compacting` (#1074) | `Active bool` | **internal-only** status peer of `Stall`: claude's auto-compaction banner. Mapped from the stream-json `system/status` line since #2227 (`internal/streamsup`'s `emitCompactingStatus`) — `Active` is the only field because the edge is what lights the banner, not because a deleted driver was silent about the rest. claude's trigger and token counts do **not** extend this struct (see `CompactionBoundary` below) |
| `CompactionBoundary` (#2237) | `Trigger string`, `PreTokens *int`, `PostTokens *int` | claude's finished-compaction line, `system/compact_boundary` — a frame of its own rather than three more fields on `Compacting`, because claude states these values on a line that arrives **after** `Compacting`'s falling edge has already shipped (the committed capture's turn order). Conversation-scoped like `Compacting`, opens and closes no turn. `Trigger` is an open-set token (`manual` observed, `auto` documented), dropped past a 256-byte bound rather than cut — a client matches it against known values, so a cut token would misinform where a dropped one only under-informs. `PreTokens`/`PostTokens` are **pointers, the one place this variant departs from `Compacting`'s shape**: a count claude omitted (`post_tokens` is optional in claude's own shape) and a count of zero are different facts, and collapsing them renders "24k → 0 tokens" for a boundary claude reported without a post count. **Every field here is claude's, including the fact of the boundary itself** — unlike `Compacting.Active`, which the daemon computes from a status comparison, nothing here is daemon-observed, so a client must render the frame as claude's assertion, not the daemon's finding. Unlike `ConversationReset`, it gets its own `Handle` arm (a compaction boundary may legitimately arrive with no turn open) and an `eventKind` arm naming the variant only, never `Trigger`'s bytes; `turnMarkFor` needs no arm since its `default` already answers `turnMarkNone`. The producing arm in `streamsup` reads and writes no `Parser` field, so it fires the same whether or not a compacting edge preceded it |
| `Unrecognized` | `Site UnrecognizedSite`, `Kind string`, `Raw string`, `Truncated bool` | **internal-only** diagnostic, and the one variant that is not a claude sub-state: the stream parser met output it has no mapping for. `Site` is a closed enum (`line_type` / `assistant_block` / `user_block` / `undecodable`); `Kind` is the offending type, empty for `undecodable`; `Raw` is the offending JSON already truncated by the producer, a `string` and not `json.RawMessage` because a truncated blob is no longer valid JSON |
| `PermissionRequest` (#700, `permission.go`) | `RequestID, ToolCallID, Title string`, `Options []PermissionOption` | daemon asks the consumer to answer a permission modal; correlated to its `PermissionResponse` by `RequestID`; see § The permission seam |
| `SlashCommandList` (#1854, produced #1877, published #2003) | `Commands []SlashCommand`, `DroppedCommands int` | claude's slash-command inventory for this session + working directory — the `commands` array of the same `initialize` reply `ModelList` carries `models` from. Constructed and emitted since #1877, entry-count bounded and its drop counted since #1826, reaching an interactive conn's wire since #2003 — see below |
| `SlashCommand` (#1854, element type — not an `Event`, no marker) | `Name, ArgumentHint, Description string`, `Aliases, TruncatedFields []string` | one inventory entry, mirroring `protocol.SlashCommand`'s field order |
| `ConversationReset` (#2134) | `NewConversationID string` | claude's top-level `conversation_reset` announcement — a `/clear`, a plan-mode exit, or a fresh-session flow — naming the id it mounted the fresh transcript under. Opens and closes no turn. **Canonical by construction, and that is why it carries no cap or `Truncated` field** — a first for a claude-derived string here: `streamsup`'s producer runs the value through `transcript.ValidStem` before constructing the event and emits nothing on failure, and `ValidStem`'s fixed-36-length anchored match already *is* the cap a `truncateField` would otherwise give it. **Carries claude's own identity on purpose, inverting the family rule** that omits it (`BackgroundTaskStarted`, `systemTaskStartedLine` — claude's session identity is not the daemon's conversation identity): here the identity *is* the entire payload, since following it to the transcript it just mounted is the reason the event exists. No `Handle` arm and no wire shape — see below |
| `ToolCallDenied` (#2232, recovered from `result` too since #2234) | `ToolName, ToolCallID, Message, DecisionReasonType, DecisionReason string`, `TruncatedFields, DroppedFields []string` | claude's per-call denial, mapped off `system/permission_denied` — the only thing on this surface distinguishing a BLOCKED call from one that ran and failed, since claude also writes the identical rejection text into a `tool_result` carrying `is_error:true`. **In the daemon's own production launch posture the line never arrives at all** (measured 2026-09-08: 9 of 9 `result`-reported denials, 0 lines, across the `--permission-prompt-tool` capture arms) — #2234's `resultDenialsLine` reads the `result` line's own `permission_denials[]` and recovers one marker per id no line already announced, `tool_name`/`tool_use_id` only, no `tool_input` and no prose (`Message`/`DecisionReasonType`/`DecisionReason` empty, none synthesized) — see [streamsup-package-system-maps-per-subtype-since-2026-08-07.md](streamsup-package-system-maps-per-subtype-since-2026-08-07.md) for the recovery mechanics and its cross-line state. Named for the daemon's own vocabulary and deliberately not `PermissionDenied`: that spelling would sit beside `PermissionRequest`/`PermissionResponse`, the MODAL ask/answer pair, and read as that pair's negative answer when nothing asked. Unconditional like `BackgroundTaskStarted`, not gated like `CompactionBoundary` — the subtype itself is the payload, so an all-empty decode still emits rather than re-dropping the line silently in exactly the case a claude rename would cause. `DroppedFields` is new to the family: three fields (`ToolName`, `ToolCallID`, `DecisionReasonType`) are dropped rather than cut on overflow, so an empty value is ambiguous between "claude omitted it" and "the daemon dropped it" — the same absence-versus-value confusion `DecisionReasonType`'s own OBSERVED-ABSENT pin exists to prevent — and the slice resolves it the way `BackgroundTaskRoster.DroppedTasks` resolves it for a roster. `Message`/`DecisionReason` are cut instead, on `maxCompactField`'s reasoning that a cut sentence still reads as prose; `ToolCallID`'s drop is the sharpest case, since it is JOINED against a `ToolStart` the client already saw and a cut id would match nothing while still looking like a real one — this DIVERGES from `BackgroundTaskStarted.ToolCallID`, which cuts and reports the same identifier under the join-key rule's later answer. Every field is claude's, including the fact of the denial (`CompactionBoundary`'s class, not `Compacting`'s); the daemon does not cross-check `ToolCallID` against a call it actually saw, so a fabricated or stale line, or (since #2234) a `result` entry with no corroborating line at all, can mislabel a successful call as blocked, bounded only by nothing in the daemon acting on any field here and the join being the client's to make. Opens and closes no turn — the `ToolStart` for the same call already did that — so `turnMarkFor` needs no arm and its `default` already answers `turnMarkNone` correctly |

- **`SlashCommandList` / `SlashCommand` (#1854) were declared ahead of their
  producer; #1877 shipped the producer, and #2003 gave them a publisher.**
  `internal/streamsup`'s `emitModelList` decodes `commands`, caps each entry's
  `Name` at `maxSlashCommandName` (256 bytes) at construction, and emits one
  `SlashCommandList` beside `ModelList` on the model-list rung — an absent, null
  or empty `commands` array emits nothing (the decode collapses all three onto
  one nil slice, so the producer can't make the positive statement an empty
  emit would be making; see [streamsup-package.md](streamsup-package.md)).
  `turnbridge.MapEvent` stopped being armless for this variant in #2001, which
  gave it an explicit `SlashCommandList` arm (see
  [turnbridge-package.md](turnbridge-package.md)), but that arm stayed
  genuinely unreached in production until #2003 gave `interactiveTurnEmitterV2.Handle`
  a `SlashCommandList` case — before that, the value reached `Handle`'s
  `default` and stopped there, never reaching `MapEvent` at all. Since #2003
  `Handle`'s case forwards the event straight through `emitMapped` to that arm,
  so a decoded inventory now reaches every interactive conn; `Handle`'s
  `default` is no longer a drop site for this variant, and `MapEvent`'s own
  `default` stays armless for it for a new reason — something routes to the
  typed arm now, where before nothing did. The entry count is bounded and its
  drop counted since #1826
  (`maxSlashCommandListEntries`, truncation from the tail, reported on
  `DroppedCommands`) — one ticket after the first emit, exactly the gap
  `ModelList`'s own count bound (`maxModelListEntries`) took after its first
  emit (#1811 → #1812). `turnMarkFor` answers it correctly by construction
  (`turnMarkNone`, same as `ModelList`): the inventory is reported once per
  `initialize` exchange, which opens and closes no turn and is not even
  per-turn.
- **`SlashCommand` now carries all five of `protocol.SlashCommand`'s fields**,
  in that type's own declaration order — `ArgumentHint` (#1957), `Description`
  (#1904) and `Aliases` (#1825) arrived one at a time after `Name`, following
  `ModelOption`'s one-field-at-a-time growth across #1819/#1827/#1828. Fixing
  the order before the second field existed is what let each later field land
  in its mirrored position instead of being appended.
- **`DroppedCommands` arrived with its entry-count bound (#1826), one ticket
  after the wire type declared its own** — the same order `ModelList.DroppedModels`
  arrived in with `maxModelListEntries`. A daemon-internal struct isn't a
  compatibility surface, so this field could wait for the bound that produces
  it rather than being declared ahead of it the way the wire type was; see
  [Producing `turnevent.SlashCommandList`](streamsup-package-producing-turnevent-slashcommandlist.md)
  for the derivation.
- **SECURITY: every string on these two types is workspace-authored** — a
  command defined in a repository was written by whoever wrote that repository,
  a *lower*-trust origin than claude's own strings, which strengthens rather than
  restates `ModelOption`'s claude-authored warning. The daemon bounds these
  strings but does not sanitize them (no control-character or terminal-escape
  stripping); the render boundary owing the sanitization is the client's. A
  client sending a `Name` back as ordinary message text is the feature, but
  publishing a name never makes it trusted — nothing in the daemon may treat a
  value from this type as a command vocabulary or hand it to a child as an argv
  element. This is why `eventKind`'s arm for the variant returns the name alone
  (never an entry count, never a string from any entry): the workspace-authored
  provenance makes the #833 keep-values-out-of-logs posture apply *a fortiori*,
  not merely by analogy to `ModelOption`.

- **`Stall` is an internal-only, onset-only empty marker (#638).** It mirrors
  tui-driver's one-shot `stall_detected` signal (no payload, no clearing edge), so
  it carries no fields — no "cleared" state (the phone self-clears on the next
  turn activity) and, like every variant here, no `conversation_id` (the bridge
  injects identity when mapping to the wire). It is a first-class member of the
  same `Event` sum, but the **adapters** decide its fate per-variant: the mobile
  adapter sends it as the wire `stall` event ([protocol-package.md](protocol-package.md)
  § Stall), the future ACP adapter (#600) drops it. This is exactly the
  internal-only asymmetry the package was always designed to host (see *What's
  deliberately NOT in the package* — `Stall` graduated out of that list in #638).
- **`ApiRetry` / `Compacting` are internal-only status peers of `Stall`, but
  unlike `Stall` they are not onset-only (#1074).** Each carries an explicit
  `Active` bool: the rising edge (tui-driver's `*Shown` kind) is `true`, the
  falling edge (`*Hidden`) is `false` — the falling edge is the deliberate "clear
  the indicator" signal a remote head needs (`Stall` instead relies on the phone
  self-clearing on next turn activity, since tui-driver's stall marker has no
  clearing edge). Like `Stall`, neither carries `conversation_id` — the bridge
  injects it at wire-mapping time — and both are dropped by the ACP adapter
  (#600, `acpbridge.MapUpdate`) the same way `Stall` is: no ACP equivalent. The
  mapper copies `Current`/`Total` verbatim on both edges (tui-driver hands the
  last-known counter on `Hidden` "so the final render stays coherent"); a `{0,0}`
  value is a legitimate "retrying, count unknown" state, not an error. The
  tui-driver framing above is provenance for the shape, not current wiring for
  both fields alike — `ApiRetry` still has no producer, but `Compacting` has had
  a stream-json one since #2227 (see the `Compacting` row above).

- **`RawInput` is opaque.** Typed `json.RawMessage` (undecoded pass-through
  bytes). The package **never inspects, parses, or mutates it** — consumers decode
  it on their own terms. `json.RawMessage` is preferred over `map[string]any`
  precisely because it does not force a parse. `TestToolStart_RawInputOpaque`
  round-trips structured JSON, invalid-JSON bytes, and `nil` unchanged.
- **`TurnEnd.Reason` is what the ACP divergence is about — an ACP divergence.**
  ACP models end-of-turn as the `stopReason` *return value* of
  `session/prompt`, not as an event. Converting `TurnEnd` back into that RPC
  return is the **ACP adapter's** job (design-doc divergence 1), not this
  model's. `ModelWindows`/`DroppedModelWindows` (#2101, below) have no ACP
  equivalent and are not part of that divergence.
- **`TurnEnd` gained `ModelWindows []ModelWindow` / `DroppedModelWindows int`
  (#2101)** — claude's per-model `contextWindow` reading off the `result`
  line's `modelUsage` map. Decoded by a second, independent unmarshal off the
  raw line bytes, so a hostile `modelUsage` shape cannot disturb turn-end
  segmentation; `Reason` still comes from the already-decoded `streamLine`
  regardless of what this decode does (see [streamsup-package.md](streamsup-package.md)).
  Sorted by `ModelID` because Go randomises map iteration — capping a decoded
  map directly would make *which* entries survive nondeterministic between
  runs on identical bytes, so the sort is what makes the cut reproducible.
  Bounded on both dimensions claude's map has (`maxModelWindowID` 256 bytes,
  `maxModelWindowEntries` 16); unlike every truncating cap in this family, an
  over-long id is **dropped, not truncated** — a future consumer joins on
  `ModelID`, and a truncated id names no model, which is strictly worse than
  no entry. `DroppedModelWindows` is one counter for every reason an entry is
  missing (unusable window, over-long id, over-cap):
  `len(ModelWindows) + DroppedModelWindows == len(modelUsage)` is the
  invariant, matching `ModelList.DroppedModels`' "true size is len + dropped".
  **CORRECTED 2026-09-08 (#2223):** this pair is still unreachable on the wire
  by construction — `turnbridge.MapEvent`'s `TurnEnd` arm builds
  `protocol.TurnEndPayload` field by field rather than embedding the event
  ([turnbridge-package.md](turnbridge-package.md)) — but that is no longer true
  of the *variant as a whole*: three sibling fields landed in #2223 (below) and
  do reach the wire, from the same arm, in the same ticket. `ModelID` is
  claude-authored text, bounded but not sanitized (no control-character or
  terminal-escape stripping) — `ModelOption.DisplayName`'s SECURITY posture
  applies unchanged: the render boundary owing sanitization is the client's.
  See [contextwindow-package.md](contextwindow-package.md) for the believed-window
  consumer this is meant to feed (`Usage.WindowTokens`, `defaultWindowTokens`).
- **`TurnEnd` gained `Outcome string`, `IsError bool`, `TerminalReason string`
  (#2223) — claude's own stop shape for the turn, published beside the
  daemon's `Reason` and never reconciled with it.** `Reason` is streamsup's
  `resultTurnEndReason` collapsing every subtype onto `end_turn`/`cancelled`;
  `Outcome` is the subtype itself, carried verbatim, so a turn that hit
  `--max-turns` is `end_turn` *and* `error_max_turns` at once — both readings
  are true, and a consumer that "resolves" the disagreement by preferring one
  undoes the reason the ticket exists (a truncated run reading as a finished
  answer). `IsError` is read from claude, **not derived from `Outcome`**: claude
  sends subtype `success` with `is_error: true` when the turn ended on an API
  error — a context overflow is the documented case — so inferring the flag
  from the subtype silently reclassifies exactly that turn as clean.
  `TerminalReason` is the open-set, finer-grained cause beside the subtype
  (`max_turns`, `budget_exhausted`, `prompt_too_long`, `hook_stopped`,
  `completed`, …) and is what makes a context overflow legible at all, since
  that turn's subtype is plain `success`.
  Decoded off the raw `result` line bytes by a **second unmarshal target**,
  `streamsup`'s `resultStopLine` — not two more fields on the package's
  existing `resultLine` (the struct `decodeModelWindows` reads `modelUsage`
  off). **The reason is failure isolation, not tidiness:** `resultLine` exists
  so a hostile `modelUsage` shape fails *that* unmarshal without disturbing
  anything else; folding the stop-shape keys into the same struct would let a
  hostile `modelUsage` also blank `TerminalReason`, i.e. let one field claude
  controls silently erase a different one. Two independent targets fail
  independently — the same property `decodeModelWindows` states for
  `Reason` versus the window pair, applied a second time for a different pair
  of fields on the same line.
  Both strings are bounded at construction by one shared constant,
  `maxTurnEndStopField` (256 bytes — roughly 7x the longest subtype claude
  has shipped, `error_max_structured_output_retries` at 35 bytes), and an
  over-long value is **dropped, not truncated** — `ModelWindow.ModelID`'s
  drop-not-truncate argument, generalized: both fields are open-set tokens a
  *client matches* against a known list, never free text a client displays,
  so a cut token (matching nothing) and an absent one carry the same meaning,
  while a truncated *display* string would still say something. `Outcome`
  and `TerminalReason` are claude-authored and reach a client unsanitized
  (bounded, not scrubbed) — the first claude-authored strings this variant
  publishes; the render boundary owing sanitization is the client's, per
  `docs/protocol-mobile.md` § `turn_end`.
  **Publishing `Outcome` is what first put a bound on `streamLine.Subtype`.**
  That field had needed no length bound for its entire life not because it
  was safe, but because its only two readers (`resultTurnEndReason`,
  `emitSystemSubtype`) only ever compare it against literals in a `switch` —
  it crossed no trust boundary. `boundStopField` is applied to a **copy** at
  the publish site, and `resultTurnEndReason` is deliberately called on the
  *unbounded* value first, so the bound cannot move `Reason` for any input.
  The general lesson: an internal field with zero downstream readers beyond a
  `switch` carries no bound not because the value is trusted, but because
  nothing has yet carried it anywhere — widening an existing field's
  *readership* needs the same bound review a brand-new decode does.
- **`TurnEnd` gained a fourth stop-shape field, `ErrorCategory string` (#2224) —
  read off a different line than the other three, and the parser's first
  cross-line state that remembers content rather than counts it.**
  `Outcome`/`IsError`/`TerminalReason` all decode from the `result` line that
  ends the turn; `ErrorCategory` is the wrapper-level `error` key on an
  `assistant` line — a sibling of `message`, never inside `message.content` —
  naming why claude's API call failed (`rate_limit`, `overloaded`,
  `account_on_hold`, `authentication_failed`, …) rather than how the turn
  stopped. The two axes are independent: a frame can carry `Outcome: "success"`
  and `ErrorCategory: "rate_limit"` on the same `turn_end`. Because
  `emitAssistant` sees only the decoded message and emits nothing for one with
  no mappable content blocks, the value has nowhere to ride until the next
  `result` line, so `streamsup.Parser` gained a new field,
  `assistantErrorCategory`, written by every `assistant` line (an absent
  `error` key writes `""`) and read-and-cleared by the same `result` arm that
  already resets `thinkingSinceEmit`. That is a new *kind* of state for the
  parser: `thinkingSinceEmit`'s doc argued it was "a token COUNTER — not a
  memory of anything claude said," a defense that does not stretch to a
  remembered category, and the doc was corrected in place rather than left
  standing (`Parser`'s and `emitBackgroundTaskRoster`'s comments, both amended
  again for #2224). The residual case a counter's argument can't answer: a
  child that dies without a `result` line leaves `assistantErrorCategory` set
  on a parser `cmd/pyry` reuses across the respawn, and attributing a stale
  category to a later turn is a **wrong claim**, not an early event like the
  counter's residual. The fix is a latch, not a second reset point: every
  `assistant` line overwrites the field, including to empty, so the residual
  survives only until the next `assistant` line rather than until the next
  `result`. The narrowing is accepted rather than closed — a turn that emits
  no `assistant` line at all before its `result` still reports the dead turn's
  category, pinned by a named test rather than silently possible — chosen as
  the fail-closed direction: a dropped category (an error-bearing line
  followed by a clean one inside one turn) matches today's behaviour, while a
  stale one sends an operator to fix an account that is fine. See
  [streamsup-package-result-stop-shape-second-decode-target-and-dr.md](streamsup-package-result-stop-shape-second-decode-target-and-dr.md)
  for the shared decode/cap mechanics (`assistantErrorLine`,
  `maxTurnEndStopField`, `boundStopField`) this field reuses unchanged.
- **`TurnEnd` gained four more numbers off the same `result` line — `DurationMS`,
  `DurationAPIMS`, `NumTurns int` and `CostUSDTotal float64` (#2260) — two per
  turn and two running totals, and the pair that looks most alike is the pair
  that disagrees.** `DurationMS`/`NumTurns` describe the turn that just ended;
  `DurationAPIMS`/`CostUSDTotal` only grow across the session, and
  `DurationAPIMS` is routinely *larger* than `DurationMS` — a running total, not
  an inner slice of it — so a consumer must not read it as this turn's own API
  time, and differencing consecutive `TurnEnd`s does not rescue that reading
  either. The daemon differences neither total; it publishes both exactly as
  claude sent them. Decoded by a fourth sibling target on the `result` line,
  `resultTurnTotalsLine`, which — unlike `resultStopLine`/`resultDenialsLine` —
  fails as a single unit rather than field-by-field, and is published as plain
  `int`/`float64` with no pointer and no consistency check between the two
  durations (a `DurationAPIMS <= DurationMS` bound would reject the majority of
  observed lines, since the larger one is a total, not an error). See
  [streamsup-package-result-stop-shape-second-decode-target-and-dr.md](streamsup-package-result-stop-shape-second-decode-target-and-dr.md)
  for the decode mechanics, the no-clamp trap and why the family's
  claude-authored-but-unsanitized SECURITY posture only half-transfers to a
  numeric field. `docs/protocol-mobile.md` § `turn_end` is where a client reads
  the full per-turn/running-total distinction and the "zero is claude's, not a
  decode failure" rule.
- **Widening a sealed sum-type variant with a slice breaks `==`, and a grep for
  the variant's type name will not find where it breaks.** `TurnEnd` stopped
  being comparable the moment `ModelWindows` landed, and the site that
  actually broke was `cmd/pyry`'s `TestSessionModelHold_OtherVariantsChangeNothing`,
  which compared two `turnevent.Event` **interface** values with `!=` — a
  comparison that dispatches to the dynamic type's equality and panics once
  that type carries a slice, taking its parallel subtests down with it. A
  sweep for source shaped like `== turnevent.TurnEnd` cannot find this: the
  comparison names no type at all, it reads `seen[0] != tt.ev` on two
  `Event`s. `ModelList` had carried a slice since #1812 and escaped only by
  not appearing in that particular table. What actually finds this is running
  every consumer package's tests (or grepping for interface-value `==`/`!=`
  against `turnevent.Event`, and `map[turnevent.Event]...` keys) — the next
  variant to grow a slice needs that sweep, not a search keyed on the type
  that has already proven it can miss.

## Location (field of `ToolStart`)

```go
type Location struct {
    Path string
    Line int   // 1-based; 0 means unspecified
}
```

A file a tool call touches (ACP tool-call location). `Line int` with
`0 = unspecified` keeps `Location` a clean value type. If a future consumer must
distinguish "line absent" from "line 0" (no valid 1-based line is 0, so unlikely),
switch to `*int` — deferred (YAGNI).
