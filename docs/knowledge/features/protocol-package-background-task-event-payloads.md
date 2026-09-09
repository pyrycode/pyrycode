# Background-task event payloads (#1393; mapping wired #1394; terminal state #2245; progress #2246)

The v2 wire shape for the three **background-task events** — work claude starts that outlives the turn that started it (a `local_bash` command backgrounded on timeout). The first frames in the vocabulary whose subject is turn-independent: #1240 found `turn_state{idle}` emitted, and `turn_end.stop_reason == "end_turn"`, while a backgrounded command was still alive, with nothing on the wire to tell a client the two cases apart. `internal/streamsup/parser.go` produces the source `turnevent.BackgroundTask{Started,Updated,Roster}` variants (#1380/#1381/#1382); this ticket (#1393) gave them the wire shape below, 1:1 against `TypeBackgroundTaskStarted` / `TypeBackgroundTaskUpdated` / `TypeBackgroundTaskRoster` (their own const block in `codes.go`, next to `TypeUnrecognizedMessage`). #1394 wired `internal/turnbridge/outbound.go`'s `MapEvent` to actually emit them and wrote `docs/protocol-mobile.md` § `background_task_started` / `_updated` / `_roster` — see [codebase/1394.md](../codebase/1394.md). #2245 widened `BackgroundTaskUpdatedPayload` with `status`/`summary` so a client can finally close a row `background_task_started` opened — see below.

```go
type BackgroundTaskStartedPayload struct {
    ConversationID  string   `json:"conversation_id"`
    TaskID          string   `json:"task_id"`
    ToolCallID      string   `json:"tool_call_id"`
    Description     string   `json:"description"`
    TaskType        string   `json:"task_type"`
    TruncatedFields []string `json:"truncated_fields"`
}

type BackgroundTaskUpdatedPayload struct {
    ConversationID  string   `json:"conversation_id"`
    TaskID          string   `json:"task_id"`
    Patch           string   `json:"patch"` // claude's patch object, WHOLE + unparsed — not json.RawMessage; truncation can leave it invalid JSON
    Status          string   `json:"status"`  // #2245: the terminal state claude reported — claude's REPORT, not the daemon's detection
    Summary         string   `json:"summary"` // #2245: claude's account of what the task did
    TruncatedFields []string `json:"truncated_fields"`
}

type BackgroundTaskRosterPayload struct { // custom MarshalJSON — see below
    ConversationID string           `json:"conversation_id"`
    Tasks          []BackgroundTask `json:"tasks"`
    DroppedTasks   int              `json:"dropped_tasks"`
}

type BackgroundTask struct { // roster row — no tool_call_id, no patch: the roster line carries neither
    TaskID          string   `json:"task_id"`
    TaskType        string   `json:"task_type"`
    Description     string   `json:"description"`
    TruncatedFields []string `json:"truncated_fields"`
}
```

- **`conversation_id` is present and unfilled by this ticket.** All nine pre-existing v2 interactive payloads carry it first; no `turnevent` variant carries one at all — the bridge (#1394) supplies it at mapping time, the same seam `StallPayload`/`ApiRetryPayload` use. **No `session_id` on any of the three** — claude's session identity is not the daemon's conversation identity.
- **`ToolCallID` names claude's `tool_use_id` in the daemon's own vocabulary** (matching `ToolUsePayload.ToolUseID`'s wire role), not claude's subtype name — the whole point of translating rather than passing claude's vocabulary straight through (one claude rename would otherwise break every client at once).
- **`TruncatedFields` (per record) and `DroppedTasks` (roster-level count) are load-bearing, not decoration.** A payload that dropped them would present claude's cut text as complete. Each rides where its dimension is decided: a text cut is a property of one entry, `DroppedTasks` is a property of the roster as a whole (`len(Tasks) + DroppedTasks` is the roster's true size) — so the roster payload carries no top-level `truncated_fields` of its own.
- **`Patch` is a plain `string`, never `json.RawMessage`** — `UnrecognizedMessagePayload.Raw`'s precedent. The producer truncates it at construction (`maxTaskPatch`), and a truncated JSON object is no longer valid JSON; typing it as raw JSON would lie to consumers and break marshalling. Pinned by a fixture carrying a deliberately unparseable fragment.
- **`BackgroundTaskRosterPayload.MarshalJSON` normalises a nil `Tasks` to `[]BackgroundTask{}`** so an empty roster always serialises `"tasks":[]`, never `"tasks":null` — the file's only custom marshaller. `omitempty` was out (AC #3 forbids eliding the key entirely); an empty roster is a *positive* signal ("nothing is alive," exactly #1240's missing case), and `[]` is the better client contract than `null` (no branch on a non-optional array type). `truncated_fields` stays un-normalised on purpose — nil and `[]` mean the same thing there, so there's no signal to protect.
- **No new truncation.** Every string is already byte-capped by the producer at construction (`internal/streamsup/parser.go`'s `maxTaskFieldID`/`maxTaskDescription`/`maxTaskPatch`/`maxTaskSummary`/`maxTaskRosterDescription`, entry count `maxTaskRosterEntries`); a payload-side cap here would be dead code and would risk disagreeing silently with the producer's.
- **Per-field caps don't compose into an envelope guarantee — measured separately (AC #4).** `TestBackgroundTaskPayloads_FitV2EnvelopeCap` fills every field to its producer cap with `<` (not `a` — `encoding/json`'s default `SetEscapeHTML` turns `<`/`>`/`&` into 6-byte escapes, and an ASCII fill under-reports by ~5×) inside a populated `Envelope`, and asserts `< 65519` (a local literal, commented to `docs/protocol-mobile.md` § Application-envelope size cap — no exported production constant, since nothing in this package enforces the cap). Measured (2026-09-10, post-#2245): **`updated` is now the binding case**, 52 868 B / 80.7% — it overtook the roster's 8-entry cap (50 557 B / 77.2%) because the row fills all four claude-derived fields (`task_id`, `patch`, `status`, `summary`) at once, deliberately more than any single frame can carry: `patch` and `status`/`summary` come from disjoint producing subtypes and are never populated together in production, so the row measures the *event type's* ceiling, not either arm's. `started` stays at 45.6%. ~12.5 KB of headroom on the worst case, down from ~15 KB before the widening.
- **`Status`/`Summary` are claude's SECOND producer of this frame, and they fill fields disjoint from `Patch`.** `system/task_updated` fills `Patch` and leaves `Status`/`Summary` empty; `system/task_notification` (#2245) fills `Status`/`Summary` and leaves `Patch` empty. A non-empty `Status` is what tells a consumer a terminal state was reported — no daemon-authored discriminator field names which line produced the frame, since that would be exactly the invented content `Patch`'s own contract forbids one field over. `Status` is documented as claude's *report*, never the daemon's own detection — the daemon does not verify a task reporting `completed` has actually stopped, and only one token (`completed`) has ever been observed; `failed`/`stopped` are documented but unstaged, so `Status` stays a plain string rather than a closed set (`BackgroundTaskStartedPayload.TaskType`'s precedent). The SECURITY block, previously scoped to `Patch` alone, now also covers `Summary` — model-authored free text a client actually renders (unlike `Patch`, told to a client as an opaque blob), and in the committed capture its value *is* the task's literal command line, the same hazard `BackgroundTaskStartedPayload.Description` already carries: render as inert text, never execute, re-shell, or feed to an HTML sink. `output_file`, the one path-shaped key on claude's `task_notification` line, is declared nowhere on this payload or its `turnevent` source — absence from the decode target, not redaction, is the guarantee, and it holds because no code path in the daemon decodes the key at all.
- **A round-trip fixture compared byte-exactly turns a struct widening into a fixture edit, even when the widening plan says it won't.** `roundTripEnvelope` compares marshalled bytes, and neither `Status` nor `Summary` is `omitempty` — the family's convention, matching `Patch` — so `background_task_updated.json` stopped round-tripping the moment the fields were added, despite the #2245 plan stating that fixture would stay untouched. It gained `"status":""`/`"summary":""` and nothing else, keeping its role as the patch-only shape's pin; `background_task_updated_terminal.json` is the new sibling fixture pinning the other shape (`patch` empty, `status`/`summary` populated). Keeping the two shapes as separate fixtures rather than one fixture carrying both is deliberate: a single fixture with all fields populated would describe a frame the daemon can never actually emit, and would let a regression that merges the two producers' field sets pass unnoticed. Expect any additive, non-`omitempty` field on a payload with a byte-exact round-trip fixture to require a fixture edit, however unrelated to protocol logic the change reads.
- **Pure DTOs except the one marshaller above: no constructors, no `Validate()`.** Identical posture to every prior slice; accepting/rejecting a malformed frame is the v2 session manager's job, not this package's.

**`BackgroundTaskProgressPayload` (#2246) is a sibling struct, not a fourth field on
`BackgroundTaskUpdatedPayload`** — `TypeBackgroundTaskProgress` joins this family's own const
block (subject — turn-independent work — is the block's grouping criterion, not shape, so a
periodic reading sits beside three lifecycle frames). It was kept out of `Updated` for the same
`description`-collision reason recorded above for `Status`/`Summary`: on a progress line
`description` is the task's *current* activity, where every other frame in this family uses
`description` for the task's *opening* one, and one field name carrying two meanings on one task
row is a trap no later ticket could undo. `SubagentType`/`LastToolName` describe the agent doing
the work rather than what happened to the task, which is likewise outside what this payload
family reports. Three of claude's ten keys are absent from the decode target for the same reason
`session_id`/`uuid`/`tool_use_id` are absent above. It is also the family's first frame with a
producer-side **rate bound** — most `task_progress` lines are consumed and emit nothing — so,
unlike its three siblings, a client cannot assume one wire frame per claude line; see
[streamsup-package-system-maps-per-subtype-since-2026-08-07.md](streamsup-package-system-maps-per-subtype-since-2026-08-07.md)
§ "Eleventh arm" for the bound's reasoning.

Five golden round-trip tests in `interactive_test.go` follow the `TestApiRetryPayload_RoundTrip` template, over `background_task_started.json` (all six fields, `description` containing `<`/`>`/`&` so the escaped form is visible in the fixture bytes), `background_task_updated.json` (`patch` a deliberately-truncated, unparseable fragment; `status`/`summary` empty — the `task_updated`-produced shape), `background_task_updated_terminal.json` (`patch` empty; `status`/`summary` populated — the `task_notification`-produced shape, #2245), `background_task_roster.json` (2+ entries, mixed `truncated_fields` — one populated, one `null`, `dropped_tasks` non-zero), and `background_task_roster_empty.json` (`"tasks":[]`, `dropped_tasks: 0` — the AC's key case). `TestBackgroundTaskRosterPayload_NilTasksNormalises` closes the gap a fixture structurally can't: unmarshalling `[]` always yields a non-nil slice, so the nil-`Tasks` path — the one #1394's bridge will actually take, since `turnevent.BackgroundTaskRoster.Tasks` is nil both for an empty roster and an omitted key — is only reachable by constructing the payload directly. See [codebase/1393.md](../codebase/1393.md) for the full implementation note, including one unfixed SHOULD FIX (the roster row's `SECURITY:` doc comment claims to repeat a warning it doesn't actually restate).
