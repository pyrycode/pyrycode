# Background-task event payloads (#1393; mapping wired #1394)

The v2 wire shape for the three **background-task events** — work claude starts that outlives the turn that started it (a `local_bash` command backgrounded on timeout). The first frames in the vocabulary whose subject is turn-independent: #1240 found `turn_state{idle}` emitted, and `turn_end.stop_reason == "end_turn"`, while a backgrounded command was still alive, with nothing on the wire to tell a client the two cases apart. `internal/streamsup/parser.go` produces the source `turnevent.BackgroundTask{Started,Updated,Roster}` variants (#1380/#1381/#1382); this ticket (#1393) gave them the wire shape below, 1:1 against `TypeBackgroundTaskStarted` / `TypeBackgroundTaskUpdated` / `TypeBackgroundTaskRoster` (their own const block in `codes.go`, next to `TypeUnrecognizedMessage`). #1394 wired `internal/turnbridge/outbound.go`'s `MapEvent` to actually emit them and wrote `docs/protocol-mobile.md` § `background_task_started` / `_updated` / `_roster` — see [codebase/1394.md](../codebase/1394.md).

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
- **No new truncation.** Every string is already byte-capped by the producer at construction (`internal/streamsup/parser.go`'s `maxTaskFieldID`/`maxTaskDescription`/`maxTaskPatch`/`maxTaskRosterDescription`, entry count `maxTaskRosterEntries`); a payload-side cap here would be dead code and would risk disagreeing silently with the producer's.
- **Per-field caps don't compose into an envelope guarantee — measured separately (AC #4).** `TestBackgroundTaskPayloads_FitV2EnvelopeCap` fills every field to its producer cap with `<` (not `a` — `encoding/json`'s default `SetEscapeHTML` turns `<`/`>`/`&` into 6-byte escapes, and an ASCII fill under-reports by ~5×) inside a populated `Envelope`, and asserts `< 65519` (a local literal, commented to `docs/protocol-mobile.md` § Application-envelope size cap — no exported production constant, since nothing in this package enforces the cap). Measured: roster (binding case, 8-entry cap) 50 557 B / 77.2%; started 45.6%; updated 40.8% — ~15 KB of headroom on the worst case.
- **Pure DTOs except the one marshaller above: no constructors, no `Validate()`.** Identical posture to every prior slice; accepting/rejecting a malformed frame is the v2 session manager's job, not this package's.

Four golden round-trip tests in `interactive_test.go` follow the `TestApiRetryPayload_RoundTrip` template, over `background_task_started.json` (all six fields, `description` containing `<`/`>`/`&` so the escaped form is visible in the fixture bytes), `background_task_updated.json` (`patch` a deliberately-truncated, unparseable fragment), `background_task_roster.json` (2+ entries, mixed `truncated_fields` — one populated, one `null`, `dropped_tasks` non-zero), and `background_task_roster_empty.json` (`"tasks":[]`, `dropped_tasks: 0` — the AC's key case). `TestBackgroundTaskRosterPayload_NilTasksNormalises` closes the gap a fixture structurally can't: unmarshalling `[]` always yields a non-nil slice, so the nil-`Tasks` path — the one #1394's bridge will actually take, since `turnevent.BackgroundTaskRoster.Tasks` is nil both for an empty roster and an omitted key — is only reachable by constructing the payload directly. See [codebase/1393.md](../codebase/1393.md) for the full implementation note, including one unfixed SHOULD FIX (the roster row's `SECURITY:` doc comment claims to repeat a warning it doesn't actually restate).
