# #1704 — declare the model-list wire type, payload and guard classification

**Size:** s (verified against the six boundaries — see § Sizing)
**Scope:** vocabulary only. No producer, no emit, no handler, no relay wiring, no fixtures, no `docs/protocol-mobile.md`.

## Files to read first

Read these before writing anything. Every entry names the symbol to read and what to take from it.

| File | Symbol | What to extract |
|---|---|---|
| `internal/protocol/codes.go` | the `TypeModelAnnounced` const block | The doc-block form to copy: grouping rationale, the "NAME is the daemon's, not claude's" paragraph, and the `MUST NOT be added to inboundAppTypeSet` paragraph. This block is the template for the new one. |
| `internal/protocol/interactive.go` | `ModelAnnouncedPayload` | The SECURITY paragraph (claude-authored text is a REPORT, never a control input; caps are the producer's), and the delegate-vs-restate convention. |
| `internal/protocol/interactive.go` | `BackgroundTaskRosterPayload` and its `MarshalJSON` | The whole precedent: per-field paragraphs in the type doc, why `[]` beats `null`, why `truncated_fields` is deliberately *not* normalised, why a value receiver, and the `type alias` recursion guard. Read the doc comment in full — it argues both sides. |
| `internal/protocol/interactive.go` | `BackgroundTask` | The entry-type shape: a row type with its own `TruncatedFields` and its own SECURITY paragraph. |
| `internal/protocol/interactive_test.go` | `TestBackgroundTaskRosterPayload_NilTasksNormalises` | The exact shape both nil-path tests take, including the value/pointer subtests and the receiver-not-mutated check. |
| `internal/protocol/interactive_test.go` | `TestModelAnnouncedType_IsNotClaudesSubtype` | The naming-pin shape: negative `strings.Contains` checks, the positive exact-equality pin, and the payload-bytes regression pin over claude's excluded keys. Its doc comment documents the same contains-trap this ticket has. |
| `internal/protocol/compat_test.go` | `TestIsKnownAppType`, `v2OnlyTypes`, `TestTypeConstants_V1V2Partition` | The three lists that take the new constant, and their comment style. |
| `internal/protocol/compat_test.go` | `TestInboundAppTypeSet_CoversAllExportedTypeConstants` | Read it to confirm you must **not** touch it: its `all` literal enumerates 23 v1 types and it asserts both `len(all) == 23` and `len(inboundAppTypeSet) == len(all)`. Adding the new constant here turns it red. |
| `cmd/pyry/relay_guard_test.go` | `excludedTypes`, `TestEveryInboundV2TypeHasHandler` | Where the `"push"` entry goes, and Assertion #3 (totality) — the reason an unclassified constant is red on its own. |
| `internal/relay/v2session_settings.go` | `validModel`, `validEffort` | The inbound rules AC 2 makes you document. Read `validModel`'s doc comment for the argv-injection rationale you must **not** weaken. |
| `docs/knowledge/features/protocol-package.md` | § "Background-task event payloads (#1393…)" | The lessons from the nearest structural analogue, including the recorded SHOULD FIX: the roster row's `SECURITY:` comment *claims* to repeat a warning it does not actually restate. Do not repeat that. |

## Context

The daemon can already ask claude for its model list — a `control_request` with subtype `initialize` on the child's held-open stdin returns a `models` array. Nothing in the tree gives that list a wire shape, so [pyrycode-desktop#561](https://github.com/pyrycode/pyrycode-desktop/issues/561) has been blocked since 2026-08-19: it needs the identifiers for its model menu and the per-model effort levels for its effort segments, because the effort control currently offers combinations that cannot work. [pyrycode-desktop#682](https://github.com/pyrycode/pyrycode-desktop/issues/682) is the same defect for permission modes and needs `supportsAutoMode`.

This is the declare-then-emit sequencing the repo has used twice for this family: #1405 → #1410 (`rate_limited`) and #1616 → #1638 (`model_announced`). The shape lands first so a client can be written against it; the producer follows in #1693 and the fixtures plus the `docs/protocol-mobile.md` contract ride #1705.

**No ADR is warranted.** Every decision here is an application of an existing precedent (`BackgroundTaskRosterPayload`) rather than a new one. The one genuinely new thing — a field a client is meant to send *back*, in a family whose every prior field was a one-way report — is recorded in the struct's own doc and in § Security review below, and its resolution belongs to #1693 where a client first sends one.

## Design

### 1. The wire constant — `internal/protocol/codes.go`

Append a new const block after the `TypeModelAnnounced` block, so a reader greps "model" and finds the two adjacent:

```go
const (
	TypeModelList = "model_list" // binary → phone, outbound v2 model-list report
)
```

The doc block above it follows the `TypeModelAnnounced` block's form and must carry these paragraphs:

- **What the frame is** — the set of models claude will accept for this conversation, sourced from the `initialize` control reply. An inventory, not an event: it does not open or close a turn and is not turn-scoped.
- **Why it is grouped alone** — it is not a turn sub-state, not turn-independent work, not a periodic reading, not a condition report. It is a CAPABILITY report: what claude says it *can be*, where `model_announced` reports what it *is* for one turn.
- **The NAME is the daemon's, not claude's** — required by AC 1. claude's words on this path are `initialize` (the subtype) and `models` (the array key); the wire type names what the frame *is* to a client, so a claude rename lands in one place instead of breaking every client at once. State the trap explicitly: the discriminating words are `init` and `models`, and a `strings.Contains(…, "model")` check would be **red against the correct name**, because `model` is this frame's subject noun (the same trap `TypeModelAnnounced`'s block documents for its own name).
- **`MUST NOT be added to inboundAppTypeSet`** — copy the sibling paragraph's reasoning: this is an outbound binary → phone frame an old phone never receives, and a leak into that set would let a phone send a `model_list` frame into `dispatch.Route`. Name both drift detectors (`compat_test.go`'s partition and `relay_guard_test.go`'s `excludedTypes`).
- **No inbound request verb is declared here, and why** — `TestEveryInboundV2TypeHasHandler`'s Assertion #1 requires an inbound type to be wired into `relay.go`'s `Handlers` map or `internal/relay/v2session.go`'s `dispatchAppFrame` switch, and this ticket ships no handler, so a verb declared here would be red by construction. Filing it under `excludedTypes` to dodge that would be a lie to the guard. If #1693 picks request/reply it declares the verb *with* its handler and moves this constant from `push` to `reply`; a client's decode path is the same frame either way.
- **The declaring-ticket note** — #1704 is vocabulary only; #1693 produces and emits; #1705 adds the fixtures and the `docs/protocol-mobile.md` § `model_list` section. Same sequencing as #1405→#1410 and #1616→#1638.

### 2. The payload and entry types — `internal/protocol/interactive.go`

Append at the end of the file, after `ModelAnnouncedPayload`, in the roster's ordering: payload, its marshaller, entry, its marshaller.

```go
type ModelListPayload struct {
	ConversationID string        `json:"conversation_id"`
	Models         []ModelOption `json:"models"`
	DroppedModels  int           `json:"dropped_models"`
}

type ModelOption struct {
	ResolvedModel    string   `json:"resolved_model"`
	Value            string   `json:"value"`
	DisplayName      string   `json:"display_name"`
	EffortLevels     []string `json:"effort_levels"`
	SupportsAutoMode bool     `json:"supports_auto_mode"`
	TruncatedFields  []string `json:"truncated_fields"`
}
```

The field set is resolved — do not re-derive it. `description` and `supportsFastMode` are deliberately not carried (no named consumer), and `supportsEffort` is subsumed by `EffortLevels` once the empty encoding is decided below. Every entry multiplies against the 65519-byte v2 application-envelope cap.

`ModelListPayload` carries **no** top-level `truncated_fields`, for `BackgroundTaskRosterPayload`'s stated reason: truncation has two dimensions and each rides where it is decided. A text cut is a property of one entry, so `TruncatedFields` rides `ModelOption`. A dropped entry is a property of the frame, so `DroppedModels` rides the payload — reporting *how many*, which a name-only list cannot.

**Type doc paragraphs required on `ModelListPayload`:**

- **What it is and its direction** — binary → phone, the wire form of claude's `initialize` model inventory. A SNAPSHOT of what claude will accept, not a delta.
- **`ConversationID`** — present and unfilled by this ticket; the producer supplies it at mapping time, the seam every v2 interactive payload uses. No `session_id`: claude's session identity is not the daemon's conversation identity.
- **`Models`** — claude's own order, truncated from the tail by the producer. Always present, never `null` — see `MarshalJSON`.
- **`DroppedModels`** — how many entries the producer cut beyond its entry cap; 0 when nothing was dropped, so the true size is `len(Models) + DroppedModels`. **State plainly that nothing counts it yet**: this field is declared ahead of any producer, and it is only honest once something upstream counts. A wire with nowhere to put a drop discards it silently, and a permanent 0 reads as "nothing dropped", which is a lie rather than a gap — that is why it is declared now. #1690 owns making the decode record it; #1693 is where the two meet.
- **A lookup can miss, and that is ordinary** — #1601 established that claude announces an identifier at least as specific as the one it was given, so a client resolving a `model_announced` identifier against this list may find nothing. This shape does not assume every announced identifier appears here. `DisplayName` is the intended join: the announcement names a concrete dated identifier while a client's rows are alias families.

**Type doc paragraphs required on `ModelOption`:**

- **`ResolvedModel`** — what `Value` resolves to right now; the concrete identifier. It closes the question desktop#561 raised and could not: that ticket's shape is "send the family name, it resolves to the newest in the family", with the operator moved to a new model without choosing to. This publishes the resolution *before* the first turn instead of leaving it to be inferred from an announcement after it.
- **`Value`** — the argument you pass. **Not a dated identifier**: an alias (`sonnet`), a bracketed variant (`opus[1m]`), or `default`. A client cannot derive a family by splitting it on `-`. This paragraph must then carry AC 2's inbound-rule sentence:

  > The only inbound path that accepts a model is `set_session_settings`, gated by `internal/relay`'s `validModel`: `""`, or 1..64 bytes whose first byte is alphanumeric and whose every byte is in `[A-Za-z0-9._-]`. Measured against the five values claude returned on 2026-08-21, that rule accepts `default`, `sonnet` and `haiku` and **rejects `opus[1m]` and `claude-fable-5[1m]`** — the bracket is not in the charset. A client must not assume every published `Value` round-trips today.

  Add a short inline comment on the struct field pointing at that paragraph, so a reader skimming the fields sees the hazard.

  **Do not widen `validModel`, here or as a drive-by anywhere.** Its charset is #845's argv-injection defense; admitting `[` and `]` is a security decision about an untrusted phone-supplied string, not a typo fix. It belongs to whichever slice first makes a client send one (#1693), with its own review. Say so in this paragraph.
- **`DisplayName`** — claude's human label, carried because it is the cleanest way to match a per-turn announcement's concrete identifier to a client's alias-family row without a mapping table.
- **`EffortLevels`** — the reasoning-effort levels this model supports. Always present, never `null` — see the entry's `MarshalJSON`, and read its rationale, which is **not** the payload's. Add the one sentence from § Security review: measured 2026-08-22, all five levels claude returns (`low`, `medium`, `high`, `xhigh`, `max`) are accepted by `internal/relay`'s `validEffort`, whose enum is closed — so a level claude adds in future would be published here and refused inbound, the same direction hazard `Value` carries today.
- **`SupportsAutoMode`** — whether claude accepts `auto` permission mode for this model; a client's permission-mode menu greys the option out when false (desktop#682). Note that it collides with nothing in the daemon's own vocabulary: `set_permission_mode` carries `default` / `acceptEdits` / `bypassPermissions` / `plan`, and `auto` is claude's mode name which the daemon does not currently send. Absent in claude's reply (Haiku) decodes to `false`, which is the correct reading.
- **`TruncatedFields`** — names THIS row's cut fields, `null` when nothing was cut. Deliberately not normalised; see § 3.
- **SECURITY** — `ResolvedModel`, `Value`, `DisplayName` and every string in `EffortLevels` are claude-authored strings that crossed the subprocess trust boundary. **Restate the warning in full here; do not delegate it to `ModelAnnouncedPayload`.** The package overview records an unfixed SHOULD FIX on `BackgroundTask` for a `SECURITY:` comment that claims to repeat a warning it does not actually restate — the sentences must literally be present. The warning: safe to RENDER as inert text, never to be fed to an HTML sink, an attribute, or a URL; the daemon bounds these strings but does not sanitize them, so they stay untrusted, model-influenced text all the way to the client, and the render boundary owing the sanitization is the CLIENT's. Add the constraint this type makes newly load-bearing: `Value` is the first field in the family a client is meant to send BACK, and that does not make it trusted — it is claude's text on an inbound path, and the daemon re-validates it at `validModel` rather than trusting that it came from a list the daemon itself published.
- **Caps are the producer's** — this struct re-decides no maximum and declares no charset check. A second cap here would be a second place the limit is decided, and the two could disagree silently.

### 3. The three list fields encode differently, for two different reasons

| Field | Nil encodes as | Reason |
|---|---|---|
| `ModelListPayload.Models` | `[]` | The roster's reasoning transfers whole: an empty list is a positive statement, `[]` reads as an empty list where `null` reads as absent, and a client decoding into a non-optional array type never has to branch. |
| `ModelOption.EffortLevels` | `[]` | **Not** the roster's reason. An empty effort list is not a positive statement here, it is a collapse — Haiku's entry omits `supportedEffortLevels` entirely, and a client's behaviour is identical for absent and empty (no effort control). The wire therefore states one position, and `[]` is the one that spares every row an optional-array branch. #1690 decides whether the daemon-internal value keeps the absent/empty distinction; the wire's position is stated here either way, because an undeclared position is one #1693 would have to invent. |
| `ModelOption.TruncatedFields` | `null` | The roster's own carve-out, unchanged: nil and `[]` say the identical thing ("nothing was cut") and no consumer branches on the difference. |

This means **two** `MarshalJSON` methods, both with **value receivers**, each using the `type alias` indirection that keeps `json.Marshal` from recursing:

- `func (p ModelListPayload) MarshalJSON() ([]byte, error)` — normalises a nil `Models` to `[]ModelOption{}`.
- `func (o ModelOption) MarshalJSON() ([]byte, error)` — normalises a nil `EffortLevels` to `[]string{}`, and leaves `TruncatedFields` alone.

**The entry-level marshaller cannot be folded into the payload's**, and the doc comment must say why: a payload marshaller normalising entries in place would mutate the caller's backing array unless the slice were copied first, and it would not fire at all when a `ModelOption` is marshalled on its own. Assigning a fresh slice to the payload copy's own `Models` field is safe — that is a field on the copy — but reaching *through* it into `p.Models[i]` is not.

The value receiver is load-bearing on both: a pointer-receiver marshaller silently misses the value path, which is the path a round-trip and a bridge both take.

Each `MarshalJSON` doc comment must state which of the two reasons above it implements, so the asymmetry between `Models` and `EffortLevels` is not read as an accident, and must state why `TruncatedFields` is exempt.

### 4. Guard classification

Five edits, four of them one line. The new constant is a **push**.

1. `internal/protocol/compat_test.go`, `TestIsKnownAppType`'s cases — add `{"model_list-rejected", TypeModelList, false, ErrUnknownType}` with the siblings' two-sentence comment: an outbound binary → phone report an old phone never receives, and rejection is also what keeps the type off the inbound path.
2. `internal/protocol/compat_test.go`, the `v2OnlyTypes` literal — `TypeModelList: true,` under a `// v2 model-list report.` comment.
3. `internal/protocol/compat_test.go`, `TestTypeConstants_V1V2Partition`'s `all` — add `TypeModelList` under the same comment. No numeric edit: that test computes `len(all)` rather than hardcoding it.
4. `cmd/pyry/relay_guard_test.go`, `excludedTypes` — `"TypeModelList": "push",` grouped with the outbound pushes, next to `"TypeModelAnnounced"`.
5. **`TestInboundAppTypeSet_CoversAllExportedTypeConstants` — do not touch.** Its `all` is the v1-only set, fixed at 23, and it asserts both `len(all) == 23` and `len(inboundAppTypeSet) == len(all)`. Adding a v2 constant there turns it red. Inbound-ness is not what selects that set: `TypeRequestSnapshot`, `TypeRequestSessionSettings` and `TypeRequestDebugBundle` are all inbound and all live in `v2OnlyTypes`.

Items 1–4 are mandatory from the moment the constant exists, not from the moment #1693 emits it — #1405 and #1616 both registered in the same commit as the constant, and #1074's developer had to discover the second detector at build time because its spec named only the `compat_test.go` trio.

## Concurrency model

None. `internal/protocol` is a pure-data package: no goroutines, no locks, no shared mutable state. The two `MarshalJSON` methods take value receivers and mutate only their own copy, so they are safe to call concurrently on the same logical value. Nothing here is initialised at package init.

## Error handling

No failure modes are introduced. These are pure DTOs — no constructors, no `Validate()`, no truncation, no charset checks. Accepting or rejecting a malformed frame is the v2 session manager's job, and bounding claude's strings is the producer's; both are stated in the type docs so a later reader does not add a second copy of either.

`json.Marshal` on a closed struct of strings, ints, bools and string slices cannot fail in practice; both marshallers propagate its error unchanged rather than swallowing it.

## Testing strategy

Three new tests in `internal/protocol/interactive_test.go`. **No fixtures and no round-trip tests** — `internal/protocol/testdata/` is #1705's, and the round-trips that exercise it ride there too. `bytes`, `strings` and `encoding/json` are already imported.

**`TestModelListPayload_NilModelsNormalises`** — `TestBackgroundTaskRosterPayload_NilTasksNormalises`'s shape:

- Construct `ModelListPayload{ConversationID: "c1"}`; fail fast if `Models` is not nil (precondition).
- Subtests `value` and `pointer` over the same payload, each marshalling and asserting `"models":null` is absent and `"models":[]` is present. The pointer subtest is what catches a pointer-receiver marshaller.
- After the subtests, assert the caller's `Models` is still nil — the normalisation must not write back through the receiver.
- Doc comment must say what the test covers that no fixture could: decoding `[]` always yields a non-nil slice, so the nil branch is reachable only by constructing the value directly. Here that is the *only* path there is, since this slice ships no fixture at all.

**`TestModelOption_NilSliceEncodings`** — the same shape, covering the asymmetry:

- Construct `ModelOption{Value: "sonnet"}`; fail fast unless both `EffortLevels` and `TruncatedFields` are nil.
- Subtests `value` and `pointer`, each asserting all three of: `"effort_levels":null` absent, `"effort_levels":[]` present, and `"truncated_fields":null` **present**.
- A third subtest `nested in payload`: marshal a `ModelListPayload` holding one such entry, assert the same three conditions hold in the payload's bytes (proving the entry marshaller fires through the payload), then assert `p.Models[0].EffortLevels` is **still nil** afterwards. That last assertion is the one that pins the hazard § 3 names — a payload marshaller that normalised entries in place would mutate the caller's backing array and this assertion is the only thing that would catch it.
- Doc comment must record why `truncated_fields` is pinned at all: it ships no fixture, and an unpinned `null` is one that a later `omitempty` or a third normaliser silently turns into something else.

**`TestModelListType_IsNotClaudesVocabulary`** — `TestModelAnnouncedType_IsNotClaudesSubtype`'s shape, with this frame's two claude words:

- `TypeModelList != "initialize"` — the subtype, exact.
- `!strings.Contains(TypeModelList, "init")` — live and discriminating against `model_list`.
- `!strings.Contains(TypeModelList, "models")` — live and discriminating: `model_list` contains `model` but not `models`.
- `TypeModelList == "model_list"` — the positive exact pin. **This is the half that fails a wrong name** rather than merely a claude-derived one; the negative checks alone leave every other wrong name green.
- A payload-bytes regression pin, over the bytes of a `ModelListPayload` holding one populated `ModelOption` (not an `Envelope` — its id/ts would dilute the check): assert none of claude's own entry-key spellings appears — `resolvedModel`, `displayName`, `supportsEffort`, `supportedEffortLevels`, `supportsAutoMode`, `supportsFastMode` — nor `description`, which is deliberately not carried. Non-discriminating today by construction; the job is to go red the day someone wires claude's camelCase keys back in or adds the dropped fields.
- **Two traps the doc comment must record.** First: a `strings.Contains(TypeModelList, "model")` check would be **red against the correct name** — `model` is this frame's subject noun and the daemon's own word, exactly as `TestModelAnnouncedType_IsNotClaudesSubtype` records for its own name. Second: `models` must **not** appear in the excluded-key list of the bytes pin — it is this payload's own wire key, and checking for it would be red against the correct shape.
- Choose field values that cannot collide with the excluded keys: e.g. `ResolvedModel: "claude-opus-4-5-20251101"`, `Value: "opus[1m]"`, `DisplayName: "Opus (1M context)"`, `EffortLevels: {"low","medium","high","xhigh","max"}`. Using `opus[1m]` is deliberate — it is one of the two values `validModel` rejects, so the test carries the hazard the doc describes.

Gate: `make check` covers all of this (`internal/protocol` and `cmd/pyry` are both hermetic unit-test packages). No live-claude or relay suite is involved.

## Sizing

Re-counted against this written spec, not the sketch:

| Limit | Boundary | This spec |
|---|---|---|
| Production source files created or modified | ≤ 3 | **2** — `internal/protocol/codes.go`, `internal/protocol/interactive.go` |
| Total written work | ≤ 400 | **~350** — codes.go ~40, interactive.go ~155, interactive_test.go ~130, compat_test.go ~10, relay_guard_test.go ~3 |
| New exported types or interfaces | ≤ 5 | **2** — `ModelListPayload`, `ModelOption` (plus one constant, which is not a type) |
| Consumer call sites needing simultaneous update | ≤ 10 | **0** — purely additive; nothing constructs or consumes these types yet |
| Acceptance criteria | ≤ 5 | **5** |
| Distinct error/reject branches | ≤ 10 | **0** — no validation, no state machine |

Calibrated against the three nearest analogue commits: #1616 (`model_announced`, 366 insertions of which 111 were `docs/protocol-mobile.md` and 11 a realclaude test), #1405 (`rate_limited`, 340 of which 72 docs), and the closest structural match #1393 (background-task family, 550 insertions — three payload types, one entry type, one marshaller, 330 test lines, four fixtures, and docs deferred to #1394). This ticket is smaller than #1393 on every axis except the marshaller count: two types instead of four, no fixtures, no round-trip tests, no docs file.

**The total holds only if the fixture-and-docs discipline holds.** Every prior commit in this family except #1393 wrote `docs/protocol-mobile.md` in the declaring commit, and every one of them shipped fixtures. Pattern-matching those would both blow this budget and take work that belongs to #1705.

### Out of scope — do not touch

- `docs/protocol-mobile.md` — #1705's. Nothing in the tree gates on a section existing for a new type; verified, no test reads that file.
- `internal/protocol/testdata/` — no new fixture. Verified: no test enumerates the directory, so an absent fixture breaks nothing.
- `internal/relay/v2session_settings.go` — `validModel` is **not** widened here (see § Security review).
- `internal/turnbridge`, `cmd/pyry/interactive_turn_v2.go`, `internal/streamsup` — the producer and the emit path are #1693's.
- `docs/knowledge/features/protocol-package.md` — the documentation phase owns it.

## Open questions

1. **Does #1693 keep this as a push, or turn it into a request/reply?** Undecided by design, and cheap either way: a client's decode path is the same frame, so only this constant's `excludedTypes` classification moves from `push` to `reply`. Freezing the frame now is what lets desktop#561 start.
2. **Does the daemon-internal value keep the absent/empty distinction for effort levels?** #1690's call. The wire's position is stated here regardless, so #1693 does not have to invent one.
3. **Who counts `DroppedModels`?** Nothing does yet — #1690's AC 2 bounds the entry count and reports cut *fields* without recording how many entries were dropped. Commented on #1690; #1693 is where the field and a counter meet.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No MUST FIX. The boundary is explicit and unmoved: every string in `ModelOption` is claude-authored text that crossed the subprocess trust boundary, and this package declares shape only — it performs no validation and no sanitization, by design. The spec requires that to be stated in full on `ModelOption` rather than delegated, because the package overview records an unfixed instance of exactly that delegation failing (`BackgroundTask`'s `SECURITY:` comment claims a restatement it does not make). Downstream callers know what they hold because the type doc says so; there is no type-system signal, which matches every sibling payload in the file.

- **[Trust boundaries — direction]** SHOULD FIX, addressed in the spec. `Value` is the first field in this payload family a client is meant to send **back**; every prior field was a one-way report, and the family's convention sentence is "it is a REPORT, never a control input". Precedent-matching would therefore carry that sentence over unchanged and hide the fact that this field's direction is new. Verified against `internal/relay`'s `validModel` rather than assumed: of the five values measured on 2026-08-21 it accepts `default`, `sonnet`, `haiku` and rejects `opus[1m]` and `claude-fable-5[1m]`, because `[` is outside its `[A-Za-z0-9._-]` charset. The spec requires that measured statement in `Value`'s own doc (AC 2), and requires the SECURITY paragraph to say that publication does not make the value trusted — the daemon re-validates at `validModel` rather than trusting that a value came from a list it published itself.

- **[Subprocess / external command execution]** No MUST FIX, and one explicit non-change. `validModel`'s charset is #845's argv-injection defense: the first-byte-alphanumeric rule bars a leading-dash value from posing as a claude flag, and the closed charset admits no shell metachar, whitespace, or control byte. The obvious "fix" for the finding above — widen the charset so the bracketed values round-trip — is a security decision about an untrusted phone-supplied string, not a typo fix, and this spec forbids it here and as a drive-by. **OUT OF SCOPE, deferred to #1693**, the slice that first makes a client send one; it gets its own review there. Nothing in this ticket reaches `exec.Command`.

- **[Network & I/O — resource exhaustion]** No MUST FIX. `ModelListPayload` is the first payload in this family carrying an unbounded list of multi-string records, and the v2 application envelope caps at 65519 bytes. The spec deliberately declares **no** cap here and says why: caps are the producer's, decided at construction, and a second cap in this struct would be a second place the limit is decided with the two free to disagree silently. That is the same posture `BackgroundTaskRosterPayload` and `ModelAnnouncedPayload` take. The residual risk is that #1693 ships a producer with no entry cap and no field caps; `DroppedModels` and `TruncatedFields` exist precisely so a capped producer has somewhere to report, and #1690's AC 2 already requires the decode to bound the entry count. **SHOULD FIX for #1693, not for this ticket** — a cap declared here would be dead code today and the wrong place tomorrow.

- **[Error messages, logs, telemetry]** No findings. This ticket adds no error paths, no log calls and no telemetry. The two `MarshalJSON` methods propagate `json.Marshal`'s error unchanged rather than wrapping it with any payload byte — relevant because `encoding/json` quotes offending input into its error strings, which is the reason `handleSetSessionSettings` never echoes a decode error.

- **[Concurrency]** No findings, with one design consequence worth naming. `internal/protocol` is a pure-data package. Both marshallers take value receivers and assign only to their own copy's fields; neither reaches through `p.Models[i]`. That is not merely a style choice — a payload marshaller normalising entries in place would mutate the caller's backing array, which for a shared payload is a data race as well as a correctness bug. The spec pins it with an assertion (`p.Models[0].EffortLevels` still nil after marshalling) rather than a comment, because a comment is the thing no test covers.

- **[Tokens, secrets, credentials]** Not applicable, and not by omission. This path carries no credential: the model list comes from claude over the control channel the daemon already writes to, not from an API endpoint and not from an API key. `ANTHROPIC_API_KEY` is a different credential on a different path and is not read here.

- **[File operations]** Not applicable. No path is constructed, no file is opened, no fixture is written — `internal/protocol/testdata/` is untouched by this ticket.

- **[Cryptographic primitives]** Not applicable. No randomness, no comparison against a secret, no key material. The frame rides the existing Noise_IK v2 session like every other v2 application envelope; this ticket changes nothing about that transport.

- **[Threat model alignment]** Addressed. `docs/protocol-mobile.md` § Security model's relevant threat here is a hostile or compromised phone sending a frame it should not be able to send. Two independent guards enforce that for this constant and the spec requires both: `IsKnownAppType` must reject `model_list` (so an old phone can never route one into `dispatch.Route`), and `TestEveryInboundV2TypeHasHandler`'s Assertion #3 forces the constant to be classified. The spec also forbids the one shortcut that would defeat the second guard — declaring an inbound request verb and filing it under `excludedTypes` to dodge the missing-handler failure, which is the #949 failure class the guard exists to catch.

- **[Concurrency — goroutine lifecycle]** Not applicable. No goroutine is spawned.

**Reviewer:** architect (self-review per the security-review checklist)
**Date:** 2026-08-22
