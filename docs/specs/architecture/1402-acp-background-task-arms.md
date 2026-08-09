# #1402 — Map the three background-task events in `acpbridge.MapUpdate`

**Size:** S · **Label:** `security-sensitive` (security review at the end of this spec: **PASS**)
**Blocker:** #1401 (landed, PR #1403) · **Blocks:** #1400

---

## Files to read first

Read these before editing. Each line says what to extract; nothing else in these files needs reading.

| Path | What to extract |
|---|---|
| `internal/acpbridge/outbound.go:315-387` | `MapUpdate`'s doc comment + the whole switch. The two comment sites (`:331-333`, `:380-385`), the arm shape to copy, and the `return X{...}, "", true` convention for non-chunk variants. |
| `internal/acpbridge/outbound.go:142-313` | The four payload types #1401 shipped — `BackgroundTaskStarted`, `BackgroundTaskUpdated`, `BackgroundTaskRoster` (+ its `MarshalJSON` at `:280-286`), `BackgroundTask`. **Field names and types only** — do not restate their doc comments at the arms. |
| `internal/acpbridge/outbound.go:38-68` | The extension-discriminant `const` block. Comment site 3 is its closing sentence at `:61-63`. |
| `internal/acpbridge/outbound.go:394-418` | `mapToolContent` / `mapLocations` — the shape `mapBackgroundTasks` mirrors, in particular `mapLocations`' `if len(x)==0 { return nil }` prologue. |
| `internal/turnbridge/outbound.go:129-188` | The mobile arms for the same three events. **Which facts matter**, and the stated reason a nil `Tasks` is forwarded rather than pre-allocated. The ACP framing differs (no `ConversationID`) — do not copy wholesale. |
| `internal/turnevent/event.go:106-289` | The three neutral source types + `turnevent.BackgroundTask`. Exact field names for the arms. |
| `internal/turnevent/permission.go:75-83` | `func (PermissionRequest) isTurnEvent()` and `var _ Event = PermissionRequest{}` — the reason the counts in § "The six comment sites" differ from the ticket body's. |
| `internal/acpbridge/outbound_test.go:11-168` | `TestMapUpdate` — the exhaustiveness table and its loop (note `reflect.DeepEqual` on `update`). AC 1's rows go here. |
| `internal/acpbridge/outbound_test.go:170-258` | `TestMapUpdate_WireShape` — drives `MapUpdate` then marshals its return (`:245-255`). AC 2's byte rung goes here. |
| `internal/acpbridge/outbound_test.go:260-356` | `TestBackgroundTaskPayloadWireShape` — #1401's golden over hand-built values. **Only its doc comment (`:260-269`) changes.** Also: its fixture values, so the new rows use different ones. |
| `cmd/pyry/acp_turn_stream.go:57-104` | `Handle` — comment sites 4 and 5. Note it pre-switches `TurnEnd`/`Stall` only, then calls `MapUpdate` at `:95`. |
| `internal/turnbridge/mapper.go:21-54` | `mapEvent` — the eight `turnevent` kinds this lane's producer emits. Proves `ApiRetry`/`Compacting` reach `MapUpdate` and return `ok == false`, which is why site 5's "unreachable" claim is false. |
| `docs/knowledge/decisions/027-acp-mapping.md` § divergence 7 | Already updated by this architect run — the gating decision (AC 4) is recorded. Read it; do not re-litigate it. The three outbound rows at `:45-47` are **not** yours (see § Work allocation). |

Already correct, do **not** touch: `cmd/pyry/interactive_turn_v2.go:480-490` (`eventKind` has all three arms, #1394) and `cmd/pyry/acp_conformance_test.go:643-645` (`TestACPConformance_DialectLock` already pins the three strings, #1401).

---

## Context

`acpbridge.MapUpdate` is the pure adapter from the neutral `turnevent.Event` model to ACP `session/update` payloads. #1401 declared the four background-task payload types, picked the three `pyry/`-prefixed extension discriminants, and gave the roster a `MarshalJSON` that normalises a nil `Tasks` to `[]`. Nothing references them: the three neutral variants still fall to `default:` and return `ok == false`, so a desktop client cannot separate a turn that ended with work still running from a genuine finish (#1240's symptom). The mobile lane has carried these facts since #1394.

This ticket wires the three arms, corrects six comment sites the arms falsify, and lands the gating decision divergence 7 delegated here.

**It does not make the events arrive.** On the ACP lane `turnbridge.mapEvent` emits eight `turnevent` kinds and none of them is a background-task variant; the three are produced only by `streamsup`, which is #1400. The work here is unit-testable with no producer.

---

## Design

### The three arms

Three new `case` arms in `MapUpdate`'s switch, placed **after `turnevent.ToolUpdate` and before `turnevent.TurnEnd`** — the `ok == true` arms stay grouped, and the order matches `turnevent`'s own declaration order and ADR 027's outbound table.

Every arm is a flat field copy. Every claude-derived string crosses **verbatim**: the mapper re-caps nothing, re-encodes nothing, re-orders nothing — the producer bounded each one at construction (`streamsup`'s `maxTaskFieldID`, `maxTaskDescription`, `maxTaskPatch`, `maxTaskRosterDescription`, `maxTaskRosterEntries`). This is the same call the mobile arm makes for the same reason.

| `ev` | `update` | `msgID` | `ok` |
|---|---|---|---|
| `BackgroundTaskStarted` | `acpbridge.BackgroundTaskStarted{SessionUpdate: SessionUpdateBackgroundTaskStarted, TaskID, ToolCallID, Description, TaskType, TruncatedFields}` | `""` | `true` |
| `BackgroundTaskUpdated` | `acpbridge.BackgroundTaskUpdated{SessionUpdate: SessionUpdateBackgroundTaskUpdated, TaskID, Patch, TruncatedFields}` | `""` | `true` |
| `BackgroundTaskRoster` | `acpbridge.BackgroundTaskRoster{SessionUpdate: SessionUpdateBackgroundTaskRoster, Tasks: mapBackgroundTasks(e.Tasks), DroppedTasks}` | `""` | `true` |

Three properties are load-bearing and each has a test row that fails without it:

- **`msgID` is `""` on all three.** `MapUpdate`'s doc comment states msgID is "non-empty only for the two chunk variants". That claim must stay true; these are not chunk variants and none of the three neutral events carries a `MessageID` field to return anyway.
- **No session or conversation identifier is added.** The mobile payloads carry `ConversationID` because the mobile envelope has no session addressing. On ACP the session id is the consumer's wrapper field (`sessionUpdateParams.SessionID`, `cmd/pyry/acp_turn_stream.go:16-19`) and every existing `acpbridge` payload omits it. There is no field to set — this is a "do not invent one" note, not a code instruction.
- **`DroppedTasks` rides.** It is the roster's **only** truncation report — `turnevent.BackgroundTaskRoster` has no `TruncatedFields` field at all (`event.go:269-289`), so an arm written by analogy with the two scalar variants silently drops the roster's entire truncation signal.

Arm comments: short. The field-level reasoning (why `Patch` is a plain string, why `Description` is a command line, why `TruncatedFields` is `omitempty`) already lives on the declarations #1401 shipped — do not restate it. What each arm's comment *should* carry is the one fact not visible at the declaration: that every string crosses verbatim because the producer already bounded it (once, on the first arm), and at the roster arm, why `Tasks` is forwarded rather than pre-allocated.

### `mapBackgroundTasks` — the one new helper

```go
// mapBackgroundTasks maps each turnevent.BackgroundTask to its ACP roster-entry
// shape, returning nil for an empty input.
func mapBackgroundTasks(tasks []turnevent.BackgroundTask) []BackgroundTask
```

Mirrors `mapLocations` exactly (`outbound.go:409-418`): `if len(tasks) == 0 { return nil }`, then `make([]BackgroundTask, len(tasks))` and an index copy of the four fields.

**Returning nil for an empty input is the contract, not an implementation detail.** `BackgroundTaskRoster.MarshalJSON` owns the nil → `[]` normalisation, and #1401's comment (`outbound.go:266-268`) states that pre-allocating in the mapper "would produce identical bytes while hiding the normalisation the type owns". A `[]BackgroundTask{}` built here is **byte-indistinguishable** from the correct mapper and passes any wire-shape golden — which is exactly why AC 2 needs a second assertion that reads the value. The helper's doc comment must say this; it is the only place a reader learns why the empty case is not just "convenient".

(`turnevent.BackgroundTaskRoster.Tasks` is documented nil for an empty roster and nil when claude omits the key, never an empty non-nil slice — so the `len == 0` prologue is reached only via nil in practice, and returns nil either way.)

### The six comment sites — **the ticket body's two counts are wrong; use these**

The ticket says `MapUpdate` names 6 of `turnevent`'s **13** sealed variants and that 7 reach `default:`. The sealed set has **14** members. `PermissionRequest` carries the marker (`internal/turnevent/permission.go:75`) and `permission.go:80` asserts `var _ Event = PermissionRequest{}`, but `event.go:33`'s own doc comment enumerates only 13 and omits it — the ticket inherited that stale enumeration. Verified by driving all 14 zero values through `MapUpdate`: today 4 `ok == true`, 10 `ok == false`.

**After this ticket:** 9 variants are named in the switch (7 → `ok == true`, 2 → `ok == false`), and **5** reach `default:`.

So AC 3's two supplied numbers are each one low. Write these instead:

- `ok == false` holds for **seven** variants, not six.
- `default:` carries **five** named existing variants, not four.

| # | Site | What it must say after this ticket |
|---|---|---|
| 1 | `internal/acpbridge/outbound.go:331-333` — `MapUpdate` doc | `ok` is false for **seven** sealed variants: `TurnEnd` (→ the `session/prompt` stopReason return, divergence 1) and `Stall` (internal-only, consumer writes it to stderr), each matched by name for a documented reason, **plus the five that reach `default:`** — `ThinkingProgress`, `ApiRetry`, `Compacting`, `Unrecognized`, `PermissionRequest`. Keep nil-safety as its own clause: a nil `Event` also yields `ok == false`, but nil is not a variant and must not be counted as an eighth. |
| 2 | `internal/acpbridge/outbound.go:380-385` — the `default:` arm | It carries **five** named, existing variants — the list above — not the "nil, or any impossible future variant" it claims today. **None of the five is future and none is impossible.** Four (`ThinkingProgress`, `ApiRetry`, `Compacting`, `Unrecognized`) have producers that put them on an `Event` stream; `PermissionRequest` is a sealed member constructed in `internal/modalbridge` but travelling the **modal** path rather than the `Event` stream (ADR 027 divergence 2 / #752). Keep the existing forward-looking rationale — the arm stays explicit so a *new* producer variant surfaces as a visible drop — but it is no longer the whole story. |
| 3 | `internal/acpbridge/outbound.go:61-63` — const block closing sentence | Replaces "Nothing emits these yet — #1402 owns the `MapUpdate` arms and the gating decision that risk feeds." Both clauses are now false: the arms are below and the decision is made (**emit unconditionally, no gate in `acpbridge`** — ADR 027 divergence 7). What stays true is **narrower and must not be conflated with it**: no ACP-lane *producer* emits the three neutral events until #1400, so divergence 7's residual risk (a strict `sessionUpdate` enum decoder rejecting the whole notification) remains unrealised on this lane. *The mapper having an arm is not the same fact as the lane having a producer* — the replacement has to keep those apart. |
| 4 | `cmd/pyry/acp_turn_stream.go:60-62` | "MapUpdate is therefore only ever reached for the four emit-able variants, where ok is always true" is wrong twice. `MapUpdate` has **seven** `ok == true` arms. On **this lane** the producer emits only four of them — `turnbridge.mapEvent` (`internal/turnbridge/mapper.go:21-54`) has no background-task arm, which is #1400 — so state the mapper's count and this lane's subset as two separate facts, and make the sentence true of whichever set it names. |
| 5 | `cmd/pyry/acp_turn_stream.go:96-99` — the `!ok` branch | It is **not** unreachable and **not** merely defensive against a future variant. `mapEvent` emits `ApiRetry` and `Compacting` (`mapper.go:36-50`); `cmd/pyry/acp_turn_streams.go:140-144` builds this lane's producer as `turnbridge.New{OnEvent: sink.Handle}` over that same `mapEvent`; and `Handle` pre-switches only `TurnEnd` and `Stall`. Both therefore reach `MapUpdate` and return `ok == false` **on every API retry and every compaction**. It is a live drop path. No behaviour change — it already logs and drops. |
| 6 | `internal/acpbridge/outbound_test.go:260-269` — `TestBackgroundTaskPayloadWireShape` doc | Both halves of "have no `MapUpdate` arm until #1402, so there is no mapper to reach them through, which is also why they are absent from `TestMapUpdate_WireShape`" stop being true. The test keeps a job worth stating: it is the only coverage of **`BackgroundTask`, the roster's entry type**, as a directly-marshalled subject — `BackgroundTask` is never a `MapUpdate` return — and it pins the four payload types' wire shape independently of the mapper. |

No behaviour change at any of the six.

---

## Concurrency model

**None, and that is a constraint this ticket must not weaken.** `MapUpdate`, `mapToolContent`, `mapLocations` and the new `mapBackgroundTasks` are pure functions — no goroutine, no channel, no mutex, no clock read, no package state. This is the property AC 4's decision (b) rests on: a gate cannot live in this package without either a signature change across every consumer or state the package doc (`outbound.go:8-13`) forbids. `mapBackgroundTasks` allocates a fresh slice and copies fields; it never retains or mutates the caller's slice, so a caller reusing its `turnevent` value across goroutines is unaffected.

---

## Error handling

Nothing here can fail. The three arms are total field copies over a sealed sum with no parsing, no validation and no rejection path; a zero-value or partially-populated neutral event maps to a zero-value-carrying payload rather than an error. No new reject branch, no new log call.

Two failure modes belong to neighbours and stay there:

- **Marshalling** happens in the consumer. The only way a background payload could fail to marshal is if `Patch` were retyped `json.RawMessage` (a truncated blob is invalid JSON); #1401 pinned it as a plain `string` and `TestBackgroundTaskPayloadWireShape`'s row 2 guards it. Not this ticket's surface.
- **A `!ok` outcome** is the consumer's to handle, and `acpTurnStream.Handle` already logs-and-drops. Site 5 corrects the comment about that branch; the branch itself is unchanged.

---

## Testing strategy

Three test files' worth of change, all in `internal/acpbridge/outbound_test.go`. Every new fixture value is **distinct, non-zero, and different from the values `TestBackgroundTaskPayloadWireShape` already uses**, so a field swap, a dropped field, a zero-value default *and* a cross-test copy each fail a row.

### `TestMapUpdate` — three new rows (AC 1)

Placed with the other `ok == true` cases, before the drop cases.

- **`BackgroundTaskStarted` → `pyry/background_task_started`.** All five neutral fields set to distinct non-zero values (`TaskID`, `ToolCallID`, `Description`, `TaskType`, `TruncatedFields`); `wantUpdate` carries each in its destination field; `wantMsgID: ""`.
- **`BackgroundTaskUpdated` → `pyry/background_task_updated`.** `TaskID`, `Patch`, `TruncatedFields` distinct and non-zero. Use a `Patch` that is **not** valid JSON (a truncated object) — the neutral doc says the producer's cut leaves it unparseable, and a fixture that happens to parse would let a future `json.RawMessage` retyping slip past this row.
- **`BackgroundTaskRoster` → `pyry/background_task_roster`, populated.** `DroppedTasks` non-zero, and **two** entries so per-entry field carriage is provable, each entry's `TaskID` / `TaskType` / `Description` / `TruncatedFields` distinct across entries. One entry carries `TruncatedFields`, the other leaves it nil — the two states are distinguishable and a mapper that hard-codes either fails.

The table's `reflect.DeepEqual` on `update` (`:155`) does the work; no loop change.

### `TestMapUpdate_WireShape` — four new rows

Three drive the variants above and lock the camelCase tags and the `pyry/` discriminants through the mapper's return path. The fourth is **AC 2's byte rung**:

- **`turnevent.BackgroundTaskRoster{}`** (nil `Tasks`, `DroppedTasks` 0) → golden `{"sessionUpdate":"pyry/background_task_roster","tasks":[],"droppedTasks":0}`.

This marshals **the value `MapUpdate` actually returns**, not a payload the test constructs — that is what makes it different from #1401's `TestBackgroundTaskPayloadWireShape` and what proves the type's normaliser fires through the mapper's return path. It catches a mapper that substitutes or drops the roster. It **cannot** catch a mapper that pre-allocates `[]BackgroundTask{}` — hence the next test.

### `TestMapUpdate_EmptyRosterForwardsNilTasks` — AC 2's value rung (new, ~15 lines)

The rung the bytes cannot reach. Drive `MapUpdate(turnevent.BackgroundTaskRoster{})`, assert `ok == true`, type-assert the return to `BackgroundTaskRoster`, and assert **`Tasks` is nil** — forwarded, not pre-allocated.

Its doc comment must name the reason, citing `outbound.go:266-268`: a `[]BackgroundTask{}` built in the arm is byte-indistinguishable from the correct mapper, so no golden can see the difference. Without this assertion the normalisation silently migrates out of the type that owns it and into a mapper branch.

> **Trap to avoid.** Do **not** "fix" a failing row by writing `Tasks: []BackgroundTask{}` into a `wantUpdate` literal. `reflect.DeepEqual` distinguishes nil from an empty slice; if a table row and the arm are corrupted together the suite goes green while the property is gone. That co-corruption is precisely why this rung is a separate, explicitly-named assertion rather than an implication of a table row.

### Gate

`make check` (vet + `-race` + staticcheck + substrate-guard + fake-claude e2e). No new e2e surface, no producer, no live-claude dependency.

---

## Work allocation

`docs/knowledge/` is not the developer's worktree. This ticket's five ACs land in three places:

| AC | Deliverable | Phase |
|---|---|---|
| 1 | The three arms + `mapBackgroundTasks` + `TestMapUpdate` rows | **Developer** |
| 2 | `TestMapUpdate_WireShape` empty-roster row + `TestMapUpdate_EmptyRosterForwardsNilTasks` | **Developer** |
| 3 | The six comment sites (5 in `.go`, 1 in `_test.go`) | **Developer** |
| 4 | ADR 027 divergence 7 — the decision + its reasoning | **Done — this architect run.** Already committed. |
| 4 | ADR 027 outbound rows `:45-47` — flip Repo status "Payload type built … `MapUpdate` arm still to come (#1402)" → **Built**, keeping the separate "no ACP-lane producer yet (#1400)" fact | **Documentation** (describes merged code state) |
| 5 | `docs/knowledge/features/acpbridge-package.md` — § rewrite + § security re-check | **Documentation** |

Same split #1401 used (`8a41bcf` ADR / `92224ea` feature doc + INDEX + codebase note), all on the branch pre-merge, so code-review sees the whole set.

### What documentation must land for AC 5

- **§ "Background-task payload shapes — declared, not yet wired" (`:166`).** The heading and three claims inside are false once the arms land: "still **unreferenced by `MapUpdate`**", "keep falling to `default:` / `ok == false` until #1402 adds the arms", and the `TestBackgroundTaskPayloadWireShape` note that the three are "absent from `TestMapUpdate_WireShape`". The section **stays** — it is where the discriminant reasoning lives — but describes a wired mapper, with "no ACP-lane producer until #1400" as the remaining gap. The `MapUpdate` table at `:120-128` gains the three rows and its `default` row's set changes.
- **§ "Not `security-sensitive`" (`:318-326`).** Re-check, now that the mapper carries claude's literal command line and an unparsed blob. See § Security review below — the conclusion holds, but the doc must say *why it holds in the presence of command text* rather than resting on the older "no untrusted-party input" phrasing alone.

---

## Open questions

1. **Does any real ACP host reject an unknown `sessionUpdate` discriminant?** Unmeasured, and structurally unmeasurable until #1400 gives this lane a producer. ADR 027 divergence 7 states it as residual risk; AC 4's decision explicitly declines to defend against it. **Resolution: an observation against a real host, after #1400 — not in this ticket.**
2. **`event.go:33`'s `Event` doc comment omits `PermissionRequest`** from its enumeration of the sealed set, which is what produced the ticket's off-by-one. Out of scope here (this ticket owns `acpbridge` and `acp_turn_stream.go` comments, not `turnevent`'s). Worth a follow-up ticket against `internal/turnevent`; noted so the next reader of that comment is not misled the same way.

---

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries] No findings — but the boundary moves, and that is the whole reason for this pass.** claude's output is untrusted-*content* (not untrusted-*party*: claude runs as the daemon's own child, and its output is not attacker-supplied under this system's threat model). The trust boundary where that content is bounded is the **producer**, `internal/streamsup/parser.go`, which caps every one of these strings at construction (`maxTaskFieldID`, `maxTaskDescription`, `maxTaskPatch`, `maxTaskRosterDescription`) and caps the roster's length (`maxTaskRosterEntries`). This ticket adds no new boundary: `MapUpdate` is downstream of it and re-caps nothing, by design. **A second cap here would be a second place the limit is decided, and the two could disagree silently** — that is a stated property of #1401's declarations, not an omission of this spec.
- **[Trust boundaries] No findings — the new arms widen the *reach* of two hazardous values, and each already carries its warning at the declaration.** `Description` is claude's **literal command line** for the `local_bash` task type; `Patch` is an **unparsed blob with no guarantee of being valid JSON**. Both become reachable by a third-party ACP host for the first time. Both are safe to **render as inert text** and must never be executed, re-shelled, or fed to an HTML sink, an attribute, or a URL. #1401 put that warning on all three sites that carry it (`BackgroundTaskStarted.Description`, `BackgroundTask.Description`, `BackgroundTaskUpdated.Patch`); this spec deliberately does **not** restate it at the arms, because a second copy in a mapper branch is a second thing to keep in sync. The rendering obligation is the **host's**, and the ADR-027 dialect is where a host integrator meets it.
- **[Subprocess / external command execution] No findings, and this is the category worth stating explicitly rather than skipping.** The mapper contains no `exec.Command`, no `sh -c`, and no call that reaches one — `MapUpdate`, `mapBackgroundTasks`, `mapToolContent` and `mapLocations` are total field copies over value types, and `acpbridge`'s import set is `encoding/json` + `internal/turnevent` only, enforced by the package doc. The reason this category is not vacuous is the inverse direction: this ticket ships a **command line as data** to a third party. Nothing in pyry re-shells it; the risk lives entirely in a host that treats `description` as executable, which is what the `SECURITY:` blocks address.
- **[Error messages, logs, telemetry] No findings.** The arms add no log call. The one log site the change touches is `acp_turn_stream.go:100-103`'s `!ok` drop, which logs `eventKind(ev)` — the **variant name only**. `eventKind`'s background-task arms (`cmd/pyry/interactive_turn_v2.go:480-490`, #1394) already carry an explicit comment that `TaskID`, `ToolCallID`, `Description`, `TaskType`, `Patch` and the roster rows are *not* returned, precisely because they are the most tempting fields in the package to log. This ticket changes that site's **comment** only; correcting "unreachable" to "reached on every API retry and compaction" makes it clearer that the path is hot, which strengthens the no-content-in-logs rule rather than weakening it. No new field, no content, no session id added to any log.
- **[Network & I/O] No findings — no new input, no new size limit needed.** This is an **outbound-only** mapper; it reads nothing from a socket and unmarshals nothing. Every inbound cap is the producer's and already in place, including the one a per-field text cap cannot supply: `maxTaskRosterEntries` bounds the roster's **length**, so an unbounded `Tasks` slice cannot reach `mapBackgroundTasks`. The helper allocates `len(tasks)` entries, so its allocation is bounded by that same cap and is not an amplification vector.
- **[Concurrency] No findings.** Pure functions, no state, no goroutine, no lock, no clock read; nothing to order and nothing to leak. `mapBackgroundTasks` copies into a fresh slice and neither retains nor mutates the input. `go test -race` has nothing to catch — reaffirmed rather than assumed, because AC 4's decision (b) *depends* on this package staying stateless.
- **[Threat model alignment] No findings; one divergence explicitly accepted.** ADR 027 divergence 7's residual risk — a strict `sessionUpdate` decoder rejecting the whole notification, with an unmeasured chance it tears the connection down instead — is **knowingly not defended** by AC 4's decision. That is a documented, reasoned acceptance with a named resolution path (observe against a real host after #1400), not an unexamined gap. Worst case on the failure it does risk: a host drops notifications it cannot decode, i.e. the facts are lost — the same outcome as today's `ok == false`. Availability impact is bounded to one ACP session and requires a host that fails hard on an unknown enum; no data is exposed by the failure.
- **[Tokens/secrets, File operations, Cryptographic primitives] Not applicable, by construction, not by omission.** No credential, no key, no RNG, no filesystem path, and no file handle appears anywhere in this change — `acpbridge` imports `encoding/json` and `internal/turnevent`, and neither the arms nor the helper introduces an import. None of the three payloads carries a session, conversation, or device identifier (§ Design states this as a positive requirement, not an accident).

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-08-09
