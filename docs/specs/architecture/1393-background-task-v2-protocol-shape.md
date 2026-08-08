# #1393 — Give the background-task events their v2 protocol shape

**Size:** S (confirmed, not overridden). **Scope:** wire vocabulary only — three frame types, four payload structs, four fixtures, and the registrations `make check` demands. Nothing emits these frames when this ticket lands; that is #1394.

---

## Files to read first

| Path | What to extract |
|---|---|
| `internal/protocol/interactive.go:1-14` | The file's standing doctrine: "wire vocabulary only: pure structs and their (de)serialization", and **"No field carries omitempty: every field is always present on the wire"**. Both constrain this ticket. |
| `internal/protocol/interactive.go:117-145` | `UnrecognizedMessagePayload` — the closest shape precedent: bridge-supplied `conversation_id` first, a producer-truncated string field, a `SECURITY:` doc block explaining why the field is a plain `string` and not `json.RawMessage`. Model your `patch` doc on this. |
| `internal/protocol/interactive.go:91-115` | `ApiRetryPayload` / `CompactingPayload` — the analogue commit's payloads. Doc-comment density and the "The bridge (#608) supplies ConversationID because the internal marker carries none" sentence to mirror. |
| `internal/protocol/codes.go:168-187` | The v2 interactive const block. Note its hand-counted trailer **"these six live in the latter"** — do **not** add to this block, or that word needs bumping. |
| `internal/protocol/codes.go:189-203` | The `TypeApiRetry`/`TypeCompacting` block — a *separate* const block with its own "these two live in the latter" trailer. **This is the pattern to copy**: a new block, its own count word, no edit to a neighbour. |
| `internal/protocol/codes.go:215-223` | The `TypeUnrecognizedMessage` single-const block — shows the trailing `// binary → phone, outbound v2 …` inline comment style each const carries. |
| `internal/turnevent/event.go:105-128` | `BackgroundTaskStarted` — authoritative field docs + the daemon-vs-claude naming rule (`tool_call_id`, **not** claude's `tool_use_id`). |
| `internal/turnevent/event.go:160-198` | `BackgroundTaskUpdated` — why `Patch` is a plain `string` and not `json.RawMessage` (truncation leaves it invalid JSON); the UTF-8 scrub caveat (`TruncatedFields` reports the **cap cut only**, never scrub removals). |
| `internal/turnevent/event.go:200-231` | `BackgroundTask` — the roster element. Field order is `TaskID, TaskType, Description, TruncatedFields`; **no** `tool_call_id`, **no** `patch`. |
| `internal/turnevent/event.go:268-290` | `BackgroundTaskRoster` — why `DroppedTasks` is a count rather than a `TruncatedFields` entry, and why this variant has no top-level `TruncatedFields`. |
| `internal/streamsup/parser.go:47,66,106,155,177` | The five producer caps: `maxTaskFieldID` 256, `maxTaskDescription` 4096, `maxTaskPatch` 4096, `maxTaskRosterEntries` 8, `maxTaskRosterDescription` 512. The envelope-cap test mirrors these as **local** constants (see § Testing). |
| `internal/streamsup/parser.go:802-807` | `truncateField` — caps are **byte** caps (`len(s) <= limit`, `s[:limit]`), not rune caps. This is what makes 6-bytes-out-per-input-byte the true ceiling. |
| `internal/protocol/envelope_test.go:11-18` | `canonical` is `json.Compact` — a **byte-level** comparison. Fixture bytes must match Go's marshalling exactly, including struct field order and `null` vs `[]`. This is what makes the fixtures real pins. |
| `internal/protocol/interactive_test.go:14-28, 210-240` | `roundTripEnvelope` and `TestApiRetryPayload_RoundTrip` — the exact test shape to replicate four times. |
| `internal/protocol/compat_test.go:51, 157, 218` | The three registration sites, per type: rejection table, `v2OnlyTypes` map, `all` list in `TestTypeConstants_V1V2Partition`. |
| `cmd/pyry/relay_guard_test.go:117-130` | `excludedTypes`. `TestEveryInboundV2TypeHasHandler` enumerates every `Type*` constant; an unregistered new type turns `make check` red. |
| `internal/protocol/conversations_read.go:10-25` | `ConversationsPayload` + `ConversationSummary` — the naming precedent for a payload and its **row** type (row carries no `Payload` suffix), and the package's willingness to be deliberate about `null` on the wire. |

Not owed an edit, verified: `internal/turnbridge/mapper.go` (PTY path, not this one), `internal/turnbridge/outbound.go` (#1394), `internal/protocol/envelope.go`'s `inboundAppTypeSet` (**must not** gain these), `docs/protocol-mobile.md` (#1394).

---

## Context

`internal/streamsup/parser.go` already translates claude's `system/task_started`, `system/task_updated` and `system/background_tasks_changed` lines into `turnevent.BackgroundTaskStarted` / `BackgroundTaskUpdated` / `BackgroundTaskRoster`. `internal/turnbridge/outbound.go`'s `MapEvent` returns `ok == false` for all three, so they reach nothing on the wire.

This ticket defines the wire shape they will map *to*. It deliberately stops short of the mapping so that #1394 never has to reopen `internal/protocol/interactive.go`.

**The translation rule.** Wire names follow the daemon's variant names, not claude's subtypes. The daemon is the single place a claude rename lands; if clients read claude's vocabulary, one claude release breaks all of them at once.

---

## Design

### Frame-type constants — `internal/protocol/codes.go`

A **new const block** appended after the `TypeUnrecognizedMessage` block, carrying its own doc comment ending in the standard "…the drift detector in `internal/protocol/compat_test.go` partitions `Type*` constants between `inboundAppTypeSet` and `v2OnlyTypes`; **these three** live in the latter" trailer.

```go
const (
	TypeBackgroundTaskStarted = "background_task_started" // binary → phone, outbound v2 background-task open
	TypeBackgroundTaskUpdated = "background_task_updated" // binary → phone, outbound v2 background-task change
	TypeBackgroundTaskRoster  = "background_task_roster"  // binary → phone, outbound v2 background-task snapshot
)
```

**Do not extend the existing `TypeTurnState`…`TypeStall` block** — its doc trailer hand-counts "these six", and a fourth block costs nothing while an edit there costs a count bump plus the risk of getting it wrong.

The doc block must state *why* these are grouped alone: they are the first frames whose subject is work that outlives the turn that started it, which is the whole point of #1240's symptom (a turn reports `end_turn` while a command it started is still alive).

### Payload structs — `internal/protocol/interactive.go`

Four new exported types appended to the file. Field order below **is** the wire order (`json.Compact` byte-compares, so order is load-bearing for the fixtures).

| Type | Fields (wire names, in order) |
|---|---|
| `BackgroundTaskStartedPayload` | `conversation_id`, `task_id`, `tool_call_id`, `description`, `task_type`, `truncated_fields` |
| `BackgroundTaskUpdatedPayload` | `conversation_id`, `task_id`, `patch`, `truncated_fields` |
| `BackgroundTaskRosterPayload` | `conversation_id`, `tasks`, `dropped_tasks` |
| `BackgroundTask` (row) | `task_id`, `task_type`, `description`, `truncated_fields` |

Types: `ConversationID string`, `DroppedTasks int`, `Tasks []BackgroundTask`, `TruncatedFields []string`, everything else `string`. No `omitempty` anywhere — the file's doctrine.

Naming notes, both deliberate:

- The row type is `BackgroundTask`, **not** `BackgroundTaskPayload`. Precedent: `ConversationSummary`, `RecentWorkspace`, `ModalOption`, `QueuedItem` — rows carry no `Payload` suffix. The package prefix disambiguates `protocol.BackgroundTask` from `turnevent.BackgroundTask`.
- `tool_call_id`, not claude's `tool_use_id`, per `turnevent.BackgroundTaskStarted`'s doc. The name matches the value `TruncatedFields` would report for it.

**`conversation_id` is present and unfilled.** All nine existing v2 interactive payloads declare it first; no `turnevent` variant carries one. Declaring it here is what lets #1394 be a `turnbridge` change only. This ticket never sets it to anything but a fixture literal.

**`session_id` appears on none of them.** claude's session identity is not the daemon's conversation identity, and no existing v2 payload carries it.

### The one behavioural decision: `tasks` normalises to `[]`

`BackgroundTaskRosterPayload` gets a value-receiver `MarshalJSON` that substitutes an empty slice for a nil `Tasks` before marshalling, via the standard `type alias …` indirection to avoid recursion. ~9 lines. It is the only custom marshaller in the file, and that deviation needs its reason on the record:

- `omitempty` is out — AC #3 forbids eliding the key.
- Between `null` and `[]`, `[]` is the better client contract: an empty roster is a *positive* statement ("nothing is alive" — precisely #1240's signal), and `[]` reads as an empty list where `null` reads as absent/unknown. A client decoding into a non-optional array type never has to branch.
- Protocol shapes are expensive to change once a client ships against them, so the better shape is worth 9 lines now.

**Why a doc comment would not have been enough.** The fixture test constructs the payload directly and byte-compares — it pins *the type's* marshalling, not *the bridge's* construction. Without `MarshalJSON`, #1394 mapping a nil slice would ship `null` to a phone while the empty-roster fixture kept asserting `[]`, and nothing in `make check` would notice. The method is what makes AC #3 a guarantee rather than a decoration.

**`truncated_fields` is deliberately *not* normalised** — it stays a plain nil-able slice and marshals to `null` when nothing was cut. The asymmetry is intentional: `tasks` is the frame's subject and its empty value is the signal, whereas `truncated_fields` is metadata about another field where nil and `[]` say the identical thing ("nothing cut") and no consumer branches on the difference. `turnevent`'s own doc anticipates this ("nil … so a consumer can emit it as absent rather than `[]`"). Fixtures pin both forms: at least one fixture with `"truncated_fields":null` and at least one with a populated array.

### Truncation reports are carried, not dropped

`TruncatedFields` rides each record and `DroppedTasks` rides the roster, mirroring where each dimension is decided upstream. A payload that dropped them would present claude's cut text to a phone as complete. `DroppedTasks` is a count, not a name in a list, because the roster's true size is `len(Tasks) + DroppedTasks` and a name-only report loses that.

### Data flow (unchanged by this ticket, shown for the seam)

```
claude stdout
  → internal/streamsup/parser.go   caps + TruncatedFields/DroppedTasks decided HERE
  → turnevent.BackgroundTask{Started,Updated,Roster}
  → internal/turnbridge MapEvent   ← still returns ok=false. #1394 fills this in.
  → protocol.BackgroundTask*Payload + Type* constant   ← THIS TICKET defines these
  → Envelope → phone
```

### Concurrency model

None. `internal/protocol` is a stdlib-only leaf data package: pure structs, no goroutines, no shared state, no locks. `MarshalJSON` is a value receiver over a copy and introduces no mutation.

### Error handling

Two paths only, both stdlib:

- **Marshal** — `MarshalJSON` returns `json.Marshal`'s error unchanged; no wrapping, since there is no context to add that the caller lacks.
- **Unmarshal** — a type mismatch on the wire (e.g. `tasks` as an object) yields the stdlib `*json.UnmarshalTypeError`. This package does not validate: it is vocabulary, and rejecting malformed frames is the v2 session manager's job.

No new failure mode is introduced. In particular this ticket adds **no truncation** — every string is already capped at construction by the producer, so a payload-side cap would be dead code and a test asserting a field is under its cap would pass without this ticket existing.

---

## Testing strategy

All tests go in the three existing test files. No new test file.

### Round-trip fixtures — `internal/protocol/interactive_test.go` + `testdata/`

Four new fixtures and four tests, each replicating `TestApiRetryPayload_RoundTrip` (`:210-240`): read fixture → unmarshal `Envelope` → assert `env.Type` → unmarshal payload → assert each field → `roundTripEnvelope(t, env, payload, raw)`.

| Fixture | What it must pin |
|---|---|
| `background_task_started.json` | All six fields populated. `task_type` `"local_bash"`, `description` a realistic command line containing `<`/`>`/`&` so the fixture itself shows the escaped `<` form. `truncated_fields` populated (at least `["description"]`). |
| `background_task_updated.json` | `patch` carrying a **deliberately unparseable** fragment — a truncated object such as `{"is_backgrounded":tr` — to pin in the fixture itself that `patch` is text, not JSON. `truncated_fields` `["patch"]`. |
| `background_task_roster.json` | Two or more entries; at least one with `truncated_fields` populated and one with it `null`; `dropped_tasks` non-zero, so the count dimension is pinned as distinct from the per-entry text dimension. |
| `background_task_roster_empty.json` | `"tasks":[]` present in the serialised bytes, `dropped_tasks` 0. The AC's key case. |

Plus one non-fixture test asserting the normalisation directly: marshal a `BackgroundTaskRosterPayload` whose `Tasks` is **nil** and assert the output contains `"tasks":[]` and not `"tasks":null`. The empty fixture alone would not catch a regression, because unmarshalling `[]` yields a non-nil empty slice — the nil path is only reachable by constructing it.

**All of the above was verified against the real `protocol.Envelope` before this spec was committed** (`go test -overlay`, no worktree writes), so the developer should hit no surprises:

- nil `Tasks` marshals to `"tasks":[]` both by value and through a pointer, and through `any` — which is the path `roundTripEnvelope(t, env, payload, …)` takes.
- A populated roster is byte-stable across marshal → unmarshal → marshal, so the fixture pin holds.
- A mixed roster naturally emits `"truncated_fields":["description"]` on one entry and `"truncated_fields":null` on another, which is exactly the `background_task_roster.json` shape specified above.
- A `description` of `grep -rn 'a<b&c' .` serialises with each `<` and `&` replaced by its six-byte lowercase-hex `\uXXXX` form, so the escaping is visible in the fixture bytes themselves rather than only in the cap test.
- The unparseable `patch` fragment `{"is_backgrounded":tr` marshals to the string `"{\"is_backgrounded\":tr"` and `json.Compact` handles the containing envelope cleanly — the fixture is safe because the fragment is the *contents of a JSON string*, correctly escaped.

### Envelope-cap test — AC #4

One test, `TestBackgroundTaskPayloads_FitV2EnvelopeCap`, table-driven over all three payloads:

- A local `const maxV2AppEnvelope = 65519`, commented with `docs/protocol-mobile.md` § Application-envelope size cap as its source. Do **not** export a production constant — nothing in this package enforces the cap, and inventing an exported one implies an enforcement that lives elsewhere.
- Local constants mirroring the five producer caps, each commented with `internal/streamsup/parser.go`'s constant name. `internal/protocol` is a stdlib-only leaf and must not import `internal/streamsup`, so this mirroring is unavoidable; the comment is what a future cap change greps for. **Flagged as a real drift risk** — see Open questions.
- Fill every string field to exactly its cap with `<`. Not `a`: measured, an `a`-filled roster envelope is 9576 B and a `<`-filled one is 50536 B, so an ASCII fill under-reports by **5.3×** and proves nothing. `<` is also realistic — `Description` is the literal command line for the `local_bash` task type.
- Roster at the full 8-entry cap, every entry's `truncated_fields` listing all three names.
- Wrap each payload in a populated `Envelope` (non-nil `EventID`), marshal, assert `len(out) < maxV2AppEnvelope`, and `t.Logf` the measured size and percentage so the headroom is visible in `-v` output rather than only in the assertion.

**Measured on this design** (real `protocol.Envelope`, hostile 64-byte `conversation_id`, max-uint64 envelope ID, via `go test -overlay`):

| payload | `a` fill | `<` fill | % of 65519 |
|---|---|---|---|
| roster (8 entries) | 9 576 B | **50 536 B** | **77.1 %** |
| started | 5 519 B | 29 839 B | 45.5 % |
| updated | 4 935 B | 26 695 B | 40.7 % |

The roster is the binding case, and it fits with ~15 KB spare. Two secondary results worth keeping in the test's comment: a NUL fill measures **identical** to `<` (both are 6-byte escapes, so "a character that costs six bytes to escape" admits either), and a 4-byte emoji fill measures identical to `a` (Go emits multi-byte runes raw) — confirming multi-byte runes are *not* the worst case.

These numbers are the expected outcome, not the assertion: the test asserts `< 65519` and constructs the payload itself, per AC #4's "proven by a test that constructs one rather than by argument".

### Registrations — required for `make check`

- `internal/protocol/compat_test.go`, three sites per type: a `{…, false, ErrUnknownType}` rejection row (`:51`), `v2OnlyTypes` (`:157`), and the `all` list in `TestTypeConstants_V1V2Partition` (`:218`). The union-count assertion rebalances automatically.
- `cmd/pyry/relay_guard_test.go` `excludedTypes` (`:121`), three entries, each `"push"`.

Verified **not** owed an edit: `internal/eventring/ring_test.go:233`, `internal/relay/v2session_test.go:2671`, `internal/e2e/relay_two_phone_structured_test.go:74` all enumerate `Type*` constants, but each is a hand-picked list rather than an exhaustive drift detector, so none goes red on a new type.

`cmd/substrate-guard` is not a risk: it scans `.go` files only (fixtures are `.json`) and bans claude-TUI screen literals and CSI escapes, none of which this ticket's content resembles.

---

## Scope check

| Red line | Limit | This ticket |
|---|---|---|
| New production files | ≤3 | **0** (2 modified: `codes.go`, `interactive.go`) |
| Total written lines | ≤~600 | **~330** projected (≈22 `codes.go` + ≈95 `interactive.go` + ≈195 tests + 4 fixtures) |
| New exported types | ≤5 | **4** |
| Consumer call sites | ≤10 | **4** registration edits; purely additive, no cascade |
| Acceptance criteria | ≤5 | **5** |
| State-machine reject branches | ≤10 | **0** — no state machine |

Nothing rationalized down. Two counts to state plainly rather than bury:

- **Four new files exist** — all one-line JSON fixtures in an existing `testdata/` directory with 40+ siblings. §4's commit-time self-check defines the countable set as *production source files*, excluding tests and data, giving 0 new / 2 modified. Four `Write` calls is the whole turn cost, and AC #3 names the empty-roster fixture explicitly, so three is not an option.
- **AC count is 5, at the boundary, not over.**

Anchor commit `a5f7bdb` (#1074), the same file set for two scalar payloads, was 110 insertions. This ticket is three payloads (one with a nested row type), four fixtures instead of two, and one extra test, so ~3× that is the expectation and lands inside the cap.

## File-overlap check

`git fetch origin --prune` then a sweep of every `origin/feature/<N>` branch's diff against main, over all five files this ticket touches. One hit: `origin/feature/449` touches `internal/protocol/codes.go`. **Not a blocker** — issue #449 is CLOSED (2026-05-17), the branch has no PR (open or merged), and it sits 1768 commits behind main with 4 ahead. It is an abandoned branch that will never merge, not concurrent in-flight work. No `addBlockedBy` set. No other branch touches any of the five files.

---

## Open questions

1. **Cap-mirroring drift.** The envelope-cap test hard-codes 256/4096/512/8 because `internal/protocol` may not import `internal/streamsup`. If a producer cap is raised later, this test keeps passing against the stale number. Mitigation in scope: name the source constant in a comment on each local constant. A structural fix (a shared caps package, or moving the test to a package that may import both) is a larger refactor and is **out of scope** — worth its own ticket if the caps ever move.
2. **Whether `dropped_tasks` should also appear per-entry.** It should not, and the spec does not add it — `turnevent.BackgroundTaskRoster`'s doc argues the count belongs at the roster level. Recorded here only so a reviewer sees it was considered rather than missed.

---

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No finding, and the reason is structural rather than incidental. The untrusted→trusted boundary for all three events is `internal/streamsup/parser.go`, upstream of this ticket: every claude-derived string is byte-capped by `truncateField` (`:802-807`) and scrubbed of invalid UTF-8 *before* a `turnevent` value exists. These payload structs sit strictly downstream of that boundary and re-decide nothing. The spec's "adds **no** truncation" rule is what keeps the boundary single rather than scattered — a second cap here would create a second place where the maximum is decided, and the two could disagree silently.
- **[Trust boundaries — downstream signal]** SHOULD FIX, addressed in the spec. `Description` (a literal shell command line for `local_bash`) and `Patch` cross to a phone as plain text with nothing in the type system marking them hostile. The spec requires each field's doc comment to carry `turnevent`'s "safe to RENDER as text, never to execute or re-shell" warning, and requires the `patch` doc to carry `UnrecognizedMessagePayload`'s render-as-inert-text / never-feed-an-HTML-sink language. Code-review should check these comments landed; a Go type cannot enforce it, and inventing a `type UntrustedText string` for three fields would be a package-wide taxonomy change well outside this ticket.
- **[Trust boundaries — type honesty]** No finding. `Patch` is a plain `string`, not `json.RawMessage`. This is the load-bearing choice: the producer truncates mid-object, so a truncated patch is *not* valid JSON, and typing it as raw JSON would both lie to consumers and break marshalling. The spec pins this with a fixture containing a deliberately unparseable fragment, so the property is asserted rather than asserted-about.
- **[Network & I/O — resource exhaustion]** No finding, and this is the ticket's genuine security question. Per-field caps do not compose into an envelope guarantee, so the aggregate was measured rather than argued: worst case is the roster at 50 536 B against the 65 519-byte v2 application-envelope cap (77.1 %), with `<`/NUL fill establishing the true 6-bytes-per-input-byte ceiling. Confirmed that multi-byte runes are *not* worse (Go emits them raw). AC #4's test pins this so a future cap rise that would overflow the envelope fails `make check` rather than producing an undeliverable frame at runtime. The residual risk is the cap-mirroring drift in Open question 1 — a raised producer cap could silently invalidate the measurement; noted, not exploitable without a separate code change that code-review would see.
- **[Error messages, logs, telemetry]** No finding. This ticket adds no logging. The producer's standing rule — log the byte count and the field name, never the content — is upstream and untouched. `MarshalJSON` returns `json.Marshal`'s error unwrapped, so no payload content can reach an error string through it.
- **[Concurrency]** Not applicable, by design rather than by omission: `internal/protocol` is a stdlib-only leaf data package. No goroutine is spawned, no lock taken, no shared state mutated. `MarshalJSON` has a value receiver and operates on a copy.
- **[Tokens, secrets, credentials]** Not applicable. No credential material is generated, stored, compared, or transported. `conversation_id` is the daemon's own routing identifier, not a secret; the spec explicitly excludes claude's `session_id` from all three payloads.
- **[File operations]** Not applicable. The only files added are static `testdata/*.json` fixtures read by `readFixture` with a test-controlled constant name (`internal/protocol/envelope_test.go:20-23`). No path is built from any runtime input.
- **[Subprocess / external command execution]** Not applicable, and worth naming because the data is command-shaped: `Description` *is* a shell command line for `local_bash`. This ticket introduces no `exec` call anywhere, and the field is carried as opaque text. The exposure is entirely in what a *client* does with it, which is why the render-never-execute warning is required on the field doc.
- **[Cryptographic primitives]** Not applicable. No randomness, hashing, key material, or comparison of attacker-controlled values against secrets. Frames are encrypted by the Noise transport layer downstream, untouched here.
- **[Threat model alignment]** `docs/protocol-mobile.md` § Application-envelope size cap is the one threat-model clause this ticket engages, and AC #4's test is its discharge. The capability-gating question (which phones may receive these frames) and the protocol-document section are `docs/protocol-mobile.md`'s and #1394's, correctly out of scope here — this ticket ships types that nothing emits, so no frame reaches any phone until #1394 makes the gating decision explicitly.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-08-09
