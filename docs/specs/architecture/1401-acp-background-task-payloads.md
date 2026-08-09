# Spec #1401 — ACP `session/update` payload shape for the background-task events

**Ticket:** [#1401](https://github.com/pyrycode/pyrycode/issues/1401) · **Size:** S (PO's `size:s` confirmed, not overridden) · **Labels:** `security-sensitive`

## Files to read first

| Path | What to extract |
|---|---|
| `internal/acpbridge/outbound.go:1-21` | Package doc: the purity contract ("no transport, no I/O, no goroutine, no clock read, no state") and the **two-import discipline** (`encoding/json` + `internal/turnevent` only). Both are load-bearing for this ticket's design and its security posture. |
| `internal/acpbridge/outbound.go:29-108` | The four discriminant constants and the six existing payload/shape structs. **This is the style you match — note every JSON tag is camelCase (`toolCallId`, `rawInput`, `oldText`, `terminalId`), and `omitempty` is used for genuinely optional fields (`locations`, `status`, `content`).** |
| `internal/acpbridge/outbound.go:110-182` | `MapUpdate`. Read it to confirm you add **no arm here** — the three variants keep falling to `default:` (#1402 owns the arms). |
| `internal/acpbridge/outbound_test.go:1-13` | Package + import block, and `TestMapUpdate`'s doc. You add **no rows to `TestMapUpdate`** (see § Out of scope). |
| `internal/acpbridge/outbound_test.go:170-258` | `TestMapUpdate_WireShape` — the `{name, ev, want}` table and the marshal-and-compare idiom at `:242-256`. Your new test copies the idiom but marshals the **payload value directly** (there is no mapper to drive). Note there is no `testdata` dir here; goldens are inline strings. |
| `internal/turnevent/event.go:106-128` | `BackgroundTaskStarted` — the source field list and the `Description` handling constraint (literal command line for `local_bash`). |
| `internal/turnevent/event.go:161-199` | `BackgroundTaskUpdated` — `Patch`'s handling constraint: unparsed blob, plain `string` not `json.RawMessage` because a truncated object no longer parses. |
| `internal/turnevent/event.go:201-289` | `BackgroundTask` (entry type) and `BackgroundTaskRoster`. Key facts: the entry's `Description` repeats the command-line warning; the roster's `Tasks` is **nil for an empty roster and nil when claude omits the key, never an empty non-nil slice**; the roster has **no** top-level `TruncatedFields` — `DroppedTasks` is its only truncation report. |
| `internal/protocol/interactive.go:149-280` | The mobile lane's four types plus `BackgroundTaskRosterPayload.MarshalJSON` (`:274-280`) — the nil-normalisation device to mirror, and the `SECURITY:`-paragraph house style. **Do not copy its snake_case tags and do not copy `ConversationID`.** |
| `internal/turnbridge/outbound.go:159-188` | The mobile roster mapping — the "forward nil, let the type normalise, do not pre-allocate" argument at `:163-169`. Same argument applies to #1402. |
| `internal/protocol/testdata/background_task_*.json` | Realistic distinct field values, especially the deliberately-truncated patch `{"is_backgrounded":tr`. **Values only** — the shape is snake_case and wrong for ACP. |
| `docs/knowledge/decisions/027-acp-mapping.md:33-47` | § "Outbound — daemon → ACP client": the three-column table (`Neutral event \| ACP mapping \| Repo status (verified)`) you add three rows to. |
| `docs/knowledge/decisions/027-acp-mapping.md:61-77` | The six divergences. Append **7** after line 77. The numbering is load-bearing (ADR 026 cites "divergence 6" by number, and `:63` says so) — renumber nothing. |
| `docs/knowledge/decisions/027-acp-mapping.md:79-91` | § "ACP taxonomy reference". Read it to confirm you **do not edit it**: `:81` states its source and capture date, so it is a port of the external spec. A pyry-invented string listed there would read as spec truth. |
| `cmd/pyry/acp_conformance_test.go:614-639` | `TestACPConformance_DialectLock` — the literal-string table you add three rows to, and the comment at `:624-626` you correct. |
| `cmd/pyry/acp_turn_stream.go:11-19` | `sessionUpdateParams` — why no session id belongs in the payload (`SessionID` is the consumer's wrapper field). |
| `internal/streamsup/parser.go:38-177` | The five producer caps (`maxTaskFieldID=256`, `maxTaskDescription=4096`, `maxTaskPatch=4096`, `maxTaskRosterEntries=8`, `maxTaskRosterDescription=512`). `acpbridge` re-caps nothing; these are cited in § Security review as the bound that already exists. |

## Context

`internal/acpbridge` is the pure adapter from the neutral `turnevent` model out to ACP `session/update` payloads. claude's background-task lifecycle already reaches a **mobile** client (`turnbridge/outbound.go:129-188`, `protocol/interactive.go:149-280`, #1393/#1394) but has **no ACP shape at all** — no payload type, no discriminant. Without one a desktop client cannot separate a turn that ended with work still running from a genuine finish, which is #1240's symptom.

This ticket declares the types, picks the discriminant, and records the decision in ADR 027. It adds **no `MapUpdate` arm** — the three variants keep hitting `default:` and returning `ok == false` until #1402, so the new types are unreferenced by production code when this lands. Nothing produces these events on the ACP lane either (#1400). Both are deliberate, with the #1393-then-#1394 precedent one lane over.

## Decision — the open question, resolved

**The discriminant is an extension: three new constants outside ACP's ten-value `sessionUpdate` set.** ADR 027 records this as divergence **7**.

The ticket left this open because a strict `sessionUpdate` enum decoder may reject the whole notification. That risk is real and I could not close it by lookup: the ADR's taxonomy port (`:81`, captured 2026-06-07) says nothing about extension points or unknown-variant tolerance, no ACP schema is vendored in-repo, and a QMD sweep of the design vault turned up no capture of either. So the decision rests on a **dominance argument**, not on a spec fact.

Set out the three candidate framings against the two host postures:

| | Strict host (serde internally-tagged enum / zod discriminated union) | Lenient host (hand-rolled switch, unknown → ignore) |
|---|---|---|
| **A. Extension discriminant**, our own field set | Rejects the notification. Facts dropped. | Falls to the default arm. Facts dropped, nothing corrupted. |
| **B. Reuse one of the four**, our own field set | Rejects the notification just as hard — an ACP `tool_call` body requires `toolCallId`/`title`/`kind`/`status`, and a roster payload has none of them. Facts dropped. | **Believes it.** A phantom tool call appears in the host's tool view, with an empty `toolCallId` that collides across all three variants and may be correlated against a real permission request. **Worse than dropping.** |
| **C. Reuse one of the four**, a *conforming* body | Accepted. | Accepted. |

**B is weakly dominated by A** — identical under a strict host, strictly worse under a lenient one. The failure is not "an unknown string"; it is "a known string over a body that is not that variant", and a strict decoder fails on the missing required fields exactly where it would have failed on the unknown tag.

**C is not available**, and that is what actually forecloses the in-taxonomy branch:

- A conforming body has no destination field for `TaskType`, `TruncatedFields`, `DroppedTasks`, or the per-entry rows — AC 1 requires one for each.
- A conforming body cannot express **an empty roster**, which is the payoff of the whole feature (`turnevent/event.go:274-276`). `tool_call_update` has no shape for "the set is now empty"; synthesising per-task completions would need a stateful `TaskID` → `ToolCallID` join table this package is forbidden to hold (`outbound.go:8-13`). AC 4's `"tasks":[]` requirement presupposes a `tasks` array field, which no ACP variant has.
- Rendering the facts as `agent_message_chunk` text is conforming but injects daemon-authored prose into claude's assistant stream — a lie about provenance — and destroys every field boundary.

So: extension discriminant, with the residual strict-host risk stated honestly in the ADR and carried forward to #1402 (see § Open questions).

### The strings

```go
SessionUpdateBackgroundTaskStarted = "pyry/background_task_started"
SessionUpdateBackgroundTaskUpdated = "pyry/background_task_updated"
SessionUpdateBackgroundTaskRoster  = "pyry/background_task_roster"
```

Declared beside the existing four in `outbound.go:31-36`, in their own `const` block with its own doc comment (the existing block's comment says "These ARE the ACP wire strings (ADR 027 § 'ACP taxonomy reference')" — a claim that must stay true of the four it covers, so the extensions do not join it).

**Why namespaced and not bare `background_task_started`.** The mobile lane uses the bare form (`protocol/codes.go:256-258`) because the mobile wire is pyry's own protocol with no external namespace to share. ACP's discriminant space belongs to the spec. A bare snake_case string is shaped exactly like the ten spec strings, which costs twice: a future spec-added `background_task_*` variant would silently collide with a different body shape, and a reader of a wire log — or of a future re-capture of the taxonomy section — cannot tell ours from spec truth. That is precisely the confusion the ticket's "do not put it in § ACP taxonomy reference" rule exists to prevent, and the prefix enforces it **on the wire**, where the ADR is not available. The `namespace/name` shape matches ACP's own method names (`session/update`, `fs/read_text_file`), so it reads as in-protocol rather than malformed. Cost: zero.

## Design

Four new exported types in `internal/acpbridge/outbound.go`, placed after `ToolCallContent` (`:108`) so the file keeps discriminants-then-payloads order. Type names follow this file's convention of naming the **ACP variant**, not the neutral event; here the two coincide because the ACP variant name is derived from the neutral one. Say so in the doc comment — it is the one place in the file where the naming rule is invisible.

### `BackgroundTaskStarted`

| Field | Type | JSON tag | Note |
|---|---|---|---|
| `SessionUpdate` | `string` | `sessionUpdate` | `SessionUpdateBackgroundTaskStarted` |
| `TaskID` | `string` | `taskId` | claude's opaque task handle; the join key the other two carry |
| `ToolCallID` | `string` | `toolCallId` | the spawning tool call — **the same string the host already saw on a `tool_call`**, so it correlates for free, with no join table |
| `Description` | `string` | `description` | **SECURITY site 1** — see below |
| `TaskType` | `string` | `taskType` | plain string, not an enum (one observation does not earn a closed set) |
| `TruncatedFields` | `[]string` | `truncatedFields,omitempty` | daemon snake_case names, per `turnevent` |

### `BackgroundTaskUpdated`

| Field | Type | JSON tag | Note |
|---|---|---|---|
| `SessionUpdate` | `string` | `sessionUpdate` | `SessionUpdateBackgroundTaskUpdated` |
| `TaskID` | `string` | `taskId` | join key back to the started payload |
| `Patch` | `string` | `patch` | **SECURITY site 3.** Plain `string`, **never `json.RawMessage`** — see § Security review. No `omitempty`: `""` (claude omitted the key) and `"{}"` must stay distinguishable, which is the neutral type's stated property (`event.go:169-170`) |
| `TruncatedFields` | `[]string` | `truncatedFields,omitempty` | |

### `BackgroundTaskRoster`

| Field | Type | JSON tag | Note |
|---|---|---|---|
| `SessionUpdate` | `string` | `sessionUpdate` | `SessionUpdateBackgroundTaskRoster` |
| `Tasks` | `[]BackgroundTask` | `tasks` | **No `omitempty`**, and normalised by `MarshalJSON` below |
| `DroppedTasks` | `int` | `droppedTasks` | **No `omitempty`**: `0` is the positive statement "nothing was dropped", and this is the roster's *only* truncation report (the type has no `TruncatedFields`, deliberately — `event.go:281-288`) |

### `BackgroundTask` (roster entry)

**No `SessionUpdate` field** — it is an element type, not a payload. Fields: `TaskID`/`taskId`, `TaskType`/`taskType`, `Description`/`description` (**SECURITY site 2**), `TruncatedFields`/`truncatedFields,omitempty`.

### The nil-normalisation device — `MarshalJSON` on the roster payload

```go
// MarshalJSON normalises a nil Tasks to an empty array, so an empty roster
// always serialises as "tasks":[] and never as "tasks":null.
func (u BackgroundTaskRoster) MarshalJSON() ([]byte, error)
```

Mirrors `protocol.BackgroundTaskRosterPayload.MarshalJSON` (`interactive.go:274-280`): substitute an empty slice on the copy, then `json.Marshal` a `type alias BackgroundTaskRoster` to avoid recursing back into this method. Three properties are load-bearing and each must be stated in the doc comment:

1. **`omitempty` is not the alternative.** A nil slice marshals to `null` whether or not the tag is set, so dropping `omitempty` from `tasks` only moves the failure from "key absent" to `"tasks":null` — the same "no roster information" reading on the consumer side. The tag is absent *and* the method exists; neither alone is sufficient.
2. **The type is the only place this can live today.** There is no `MapUpdate` arm until #1402. And when #1402 lands, pre-allocating in the mapper would produce identical bytes while hiding the normalisation the type owns — the mobile lane made exactly that call and said so (`turnbridge/outbound.go:163-169`).
3. **Value receiver, not pointer.** `MapUpdate` returns payloads as values into an `any`, and `json.Marshal` on a value boxed in an interface only finds value-receiver methods — a pointer receiver would silently never fire. The copy also means the substitution never touches the caller's slice header.

**`TruncatedFields` is deliberately not normalised**, on any of the four types. nil and `[]` say the identical thing there ("nothing was cut") and no consumer branches on the difference — the call the mobile lane already made (`interactive.go:265-268`). Here `omitempty` carries that: both nil and `[]` are omitted, which is a *stronger* realisation of "they mean the same" than the mobile lane's tag (which ships `null` for nil and `[]` for empty while claiming they are equivalent). It also matches this package's own optional-list convention exactly (`locations,omitempty`, `outbound.go:78`). This divergence from the mobile tag is intentional; note it in the doc comment so a reviewer diffing the two lanes does not read it as a slip.

### The three SECURITY sites (AC 2)

The handling constraint is repeated at **each of the three sites**, not once for the package. Following `internal/protocol`'s house style, each is a `SECURITY:` paragraph in the **type doc comment** naming its specific field:

| Site | Type | Field | Content |
|---|---|---|---|
| 1 | `BackgroundTaskStarted` | `Description` | For claude's `local_bash` task type this is the **literal command line**. Safe to render as inert text; never execute, re-shell, or feed to an HTML sink, an attribute, or a URL. |
| 2 | `BackgroundTask` | `Description` | The same warning, stated again rather than delegated — a roster carries a **list** of command lines, a more tempting shape to feed somewhere structured than a single one (`event.go:217-220`). |
| 3 | `BackgroundTaskUpdated` | `Patch` | An unparsed blob with **no guarantee of being valid JSON** — the producer truncates it and a truncated object no longer parses, which is why it is a plain `string`. A consumer must not assume it parses, must render it as inert text, and must never execute or re-shell it. |

Verification: `grep -c '^// SECURITY:' internal/acpbridge/outbound.go` must be **3**, and each must sit in a different type's doc comment. A single package-level note does not satisfy this AC.

### What is *not* added

- **No `sessionId` / `conversationId` on any payload.** The mobile payloads carry `ConversationID` because the mobile envelope has no session addressing; on ACP the session id is the consumer's (`sessionUpdateParams.SessionID`, `acp_turn_stream.go:16-19`) and every existing `acpbridge` payload omits it.
- **No `toolCallId` on the update or the roster.** Only `BackgroundTaskStarted` carries one because only that neutral event does. Synthesising one for the other two is the join table this package cannot hold.
- **No re-capping.** Every string is already bounded at construction by the producer; `acpbridge` re-decides no maximum, exactly as the mobile arm does not (a second cap is a second place the limit is decided, and the two can disagree silently).
- **No entry in `MapUpdate`, and no new `ContentBlock`/`ToolCallContent` shapes.**

## ADR 027 edits

**Three rows** in § "Outbound — daemon → ACP client" (`:35-47`), inserted after the `Stall` row and before `BusyState`, keeping built-things-first ordering. Three-column shape, Repo status honest about the split:

- Neutral event column: the type plus its field list, matching the existing rows' style.
- ACP mapping column: `session/update` → `pyry/background_task_started` (etc.), each citing **divergence 7**. The roster row also notes `tasks` is always present and never `null`.
- Repo status column: **Payload type built** — `acpbridge.BackgroundTaskStarted` (#1401); the `MapUpdate` arm is **still to come** (#1402), so the event falls to `default:` / `ok == false` today, and no producer emits it on the ACP lane yet (#1400).

**Divergence 7**, appended after divergence 6 (`:77`), renumbering nothing. It must carry:

1. The three strings and why an extension exists at all (no background-task variant among the ten; the facts are real and the empty roster is the payoff).
2. The rejected alternative and the dominance argument (§ Decision's table, compressed).
3. The **residual risk, stated plainly**: a strict `sessionUpdate` decoder may reject the notification, and no capture of ACP's unknown-variant tolerance exists — the taxonomy port (`:81`) says nothing about extension points. The cost is bounded to the dropped background-task facts *if* the host merely rejects the notification; a host that tears the connection down would cost the session, and that is unmeasured.
4. The namespace-prefix mitigation and its two reasons.
5. The forward decision for #1402: whether the arms emit unconditionally or gate on something. Fold this into divergence 7's closing sentence rather than adding an Open item — the Open-items list numbering is surface this ticket has no reason to touch.

**Not edited:** § "ACP taxonomy reference" (`:79-91`). It is a port of the external spec with its source and capture date on its first line.

## `TestACPConformance_DialectLock` edits

`cmd/pyry/acp_conformance_test.go:621-639`. Add three rows to the existing `{got, want}` table against the **literal** ADR strings (`"pyry/background_task_started"`, …) — a constant the lock does not name is unlocked, and this test is what holds the ADR and the code together.

Correct the comment at `:624-626`. Today it reads "the four generic variant discriminants (ADR 027 § 'ACP taxonomy reference'): agent_message_chunk / agent_thought_chunk / tool_call / tool_call_update — not opencode aliases." Both facts change:

- **Count** — seven discriminants now: four generic plus three pyry extensions.
- **Home** — the three extensions are **not** in § "ACP taxonomy reference" (that section is the external-spec port); their home is **divergence 7**.

A blank line and a one-line inline comment separating the four generic rows from the three extension rows is worth the two lines — it puts the home distinction in the code, not only in the block comment. Optional, not mandated.

This comment is deliberately **not** deferred by analogy with the four `ok == false` comment sites (see § Out of scope): its count settles the moment a constant is added, which is here.

## Testing strategy

One new test function in `internal/acpbridge/outbound_test.go` — **not** rows in `TestMapUpdate` or `TestMapUpdate_WireShape`, both of which are driven by `MapUpdate(ev)` and cannot reach a payload with no mapper arm. Name it for what it locks (e.g. `TestBackgroundTaskPayloadWireShape`); table-driven over `{name string, payload any, want string}`, marshalling the payload value directly and comparing the exact string, reusing `TestMapUpdate_WireShape`'s idiom (`:242-256`).

Four rows. Every field in every row is **distinct and non-zero** (except where a row's point is a zero value), and no value repeats across rows, so a field swap or a cross-row copy fails:

1. **`pyry/background_task_started`** — all five fields set to distinct realistic values (e.g. `task_01ABC` / `toolu_01XYZ` / a literal `local_bash` command line / `local_bash` / `["description"]`).
2. **`pyry/background_task_updated`** — a *different* `TaskID` from row 1; `Patch` set to a deliberately **truncated, invalid-JSON** blob (`{"is_backgrounded":tr`, the mobile fixture's value); `TruncatedFields` `["patch"]`. This row is the guard on the `string`-not-`json.RawMessage` typing.
3. **`pyry/background_task_roster`, populated** — two entries with distinct values, at least one carrying a non-empty per-entry `TruncatedFields`, and `DroppedTasks` **non-zero**.
4. **`pyry/background_task_roster`, empty** — the AC 4 row. `Tasks` **left unset (nil)** and `DroppedTasks: 0`; want `{"sessionUpdate":"pyry/background_task_roster","tasks":[],"droppedTasks":0}`.

**Row 4 must not hand-build `[]BackgroundTask{}`.** nil is the only thing the mapper will ever hand this type for an empty roster (`event.go:270-277`: nil both for an empty roster and when claude omits the key, never an empty non-nil slice). A hand-built empty slice marshals to `[]` with no device involved and pins nothing.

### Mutant × row matrix

Each mutation must have a row that is RED for it, and each row must be the sole RED for at least one mutation:

| Mutation | Sole RED row |
|---|---|
| `MarshalJSON` deleted, or given a pointer receiver | 4 |
| `tasks` tag gains `omitempty` (key vanishes on nil) | 4 |
| `droppedTasks` tag gains `omitempty` (key vanishes on 0) | 4 |
| `taskId` and `toolCallId` tags swapped | 1 |
| `truncatedFields` dropped from the roster **entry** type | 3 |
| `Patch` retyped as `json.RawMessage` (marshal fails on the truncated blob) | 2 |
| any discriminant constant's value changed | that type's row(s) — **and** `TestACPConformance_DialectLock` |

Row 4 is the sole RED for three distinct mutations, which is what keeps it from being the vacuous row AC 4 warns about.

Gate: `make check` (the new tests are pure — no goroutines, no I/O, `t.Parallel()` like their neighbours).

## Out of scope — do not touch

- **`MapUpdate` and its `default:` arm.** No new arms. #1402.
- **The four comment sites that misdescribe the `ok == false` path** — `outbound.go:125-128`, `outbound.go:175`, `acp_turn_stream.go:60-62`, `acp_turn_stream.go:97-99`. Their corrected numbers only settle once the arms land; #1402 fixes all four in one pass. The `DialectLock` comment above is **not** one of these.
- **`TestMapUpdate`.** Its doc claims "one case per sealed `turnevent.Event` variant"; that claim is *already* partial. The sealed set is **fourteen** variants (thirteen markers at `turnevent/event.go:447-459` plus `PermissionRequest` at `permission.go:75`), and `MapUpdate` names **six** — `TextChunk`, `ThoughtChunk`, `ToolStart`, `ToolUpdate`, `TurnEnd`, `Stall`. So **eight** have no case today: the three background ones, `ThinkingProgress`, `ApiRetry`, `Compacting`, `Unrecognized`, and `PermissionRequest`. The last of those is absent *by decision*, not pending — permission never reaches the `turnevent` stream at all (ADR 027 divergence 2, refined by #752); the other seven are the pending set. Fixing the doc belongs with the arms, in #1402. Adding `ok == false` rows here would have to be deleted there.
- **`cmd/pyry/acp_turn_stream_test.go:87-90` and `acp_conformance_test.go:556-558`.** Both enumerate discriminants observed on a *driven* session; nothing emits background tasks on the ACP lane yet (#1400), so both are unaffected.
- **`eventKind` (`cmd/pyry/interactive_turn_v2.go:457+`).** It already handles all three variants and already returns the variant name only, content-free.
- **The mobile lane** — `internal/protocol`, `internal/turnbridge`, `docs/protocol-mobile.md`. Untouched.
- **`docs/knowledge/features/acpbridge-package.md`, `docs/knowledge/INDEX.md`, `docs/knowledge/codebase/1401.md`.** The documentation phase owns these; the developer's worktree mutates code, tests, ADR 027, and this spec only.

## Open questions

1. **Does a real ACP host reject or tolerate `pyry/…`?** Unresolved by lookup (no vendored schema, no vault capture, taxonomy port silent). The dominance argument makes the *choice* safe relative to its alternatives, but the absolute risk is unmeasured. The cheapest resolution is an observation against a real host once #1402 emits — recorded in divergence 7, not gated here.
2. **Does #1402 gate emission?** If a host is found to tear the connection down on an unknown discriminant, the arms need a switch (a client-capability probe at `initialize`, or a flag). That is #1402's call with #1400's producer in place; this ticket only fixes what the string is.
3. **Is `_meta` an ACP extension point?** MCP-family protocols conventionally carry one, and if ACP does, a conforming body plus a `_meta` extension would be strict-host-safe. Not built on here: the repo has no evidence the field exists in ACP, and building on an unverified spec field is worse than a plain extension string. Worth checking when the taxonomy section is next re-captured.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No findings — the boundary is upstream and explicit. claude's stdout crosses into trusted-shape/untrusted-content at `internal/streamsup`'s parser, which bounds every string at construction (`maxTaskFieldID=256`, `maxTaskDescription=4096`, `maxTaskPatch=4096`, `maxTaskRosterDescription=512`) and the roster's length (`maxTaskRosterEntries=8`), and scrubs invalid UTF-8. `acpbridge` is downstream of that boundary and re-validates nothing **by design** (`outbound.go:8-13`); the mobile arm makes the identical call for the identical reason. The downstream signal that the *content* is still untrusted is the three `SECURITY:` paragraphs (AC 2) — which is why AC 2 counts sites rather than accepting one package-level note.
- **[Tokens, secrets, credentials]** No findings, but named rather than dismissed: a `local_bash` `Description` is a literal command line and can contain a secret the user passed to a shell (`curl -H "Authorization: …"`). This ticket opens no new exposure — the ACP host is the local desktop client the same user is driving, the same content already crosses the mobile wire (#1394), and the payload adds no identifier the neutral event did not carry. The forward obligation is logging: `acp_turn_stream.go`'s stated rule is that application content is never logged, and `eventKind` (`interactive_turn_v2.go:480-486`) already returns the variant name only for all three variants. **#1402's arms must not log payload fields** — noted in the spec, and code-review's to enforce there.
- **[File operations]** Not applicable by construction — no filesystem access anywhere in the design. The package's stated purity contract forbids I/O and its import discipline (`encoding/json` + `internal/turnevent` only) makes it structural, not advisory.
- **[Subprocess / external command execution]** Not applicable, and pointedly so — this is the category the design exists to keep closed. `Description` **is** a command line; nothing here executes it. The guard is not a grep for `exec.` (which would read clean even if a helper shelled out); it is the package's two-import discipline stated at `outbound.go:16-20`: `os/exec`, `os`, `syscall`, `net`, and `io` are all absent and adding any of them is a visible import-line change in a 200-line file. No symbol in the design shells out, and no field is passed anywhere but `encoding/json`.
- **[Cryptographic primitives]** Not applicable — no randomness, no key material, no comparison against a secret.
- **[Network & I/O]** No findings. The payload is bounded in every dimension by the producer's caps: worst case pre-escape is the roster at 8 × (256 + 256 + 512 + a ≤3-name `truncatedFields`) + tags ≈ **~9 KiB**, and the two scalar payloads at ~5 KiB and ~4.5 KiB. JSON escaping expands these but the same content already fits the mobile v2 65519-byte application-envelope cap (#1393/#1394), and the ACP stdio transport declares no per-notification cap. `acpbridge` adds no cap of its own, deliberately — a second cap is a second place the limit is decided.
- **[Errors, logs, telemetry]** No findings in this ticket (it emits nothing and logs nothing), but one **MUST-KEEP** that the design encodes: `Patch` must stay a plain `string` and never become `json.RawMessage`. The producer truncates it at `maxTaskPatch`, and a cap cut mid-object yields invalid JSON; Go's encoder rejects an invalid `json.RawMessage`, so `json.Marshal` of the whole payload would **fail**, and the consumer's failure path (`acp_turn_stream.go:105-110`) drops the notification. That converts a truncation — something claude's output length alone can trigger — into a silent loss of the whole update. The string typing prevents it; test row 2, with a deliberately truncated blob, is what keeps it prevented, and the mutant matrix names it.
- **[Concurrency]** Not applicable — four value types with no shared state, no lock, no goroutine, no clock read. The one method, `MarshalJSON`, takes a **value receiver**, so the nil substitution lands on a copy and never mutates a caller's slice header; that property is stated in its doc comment and is the third bullet of § "The nil-normalisation device".
- **[Threat model alignment]** The relevant threat is "claude-derived text reaches a renderer that executes or re-shells it", addressed by the three-site repetition (AC 2). `docs/protocol-mobile.md` § Security model governs the mobile lane, not this one. **OUT OF SCOPE, named:** availability against a strict ACP host that rejects — or tears down on — an unknown `sessionUpdate` discriminant. Nothing is emitted until #1402, which owns the gating decision (§ Open questions 1–2) and inherits it via ADR 027 divergence 7.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-08-09
