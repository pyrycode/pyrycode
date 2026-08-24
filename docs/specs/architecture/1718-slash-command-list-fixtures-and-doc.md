# #1718 — Pin the slash-command-list encoding with fixtures and publish it in the mobile-protocol doc

**Size:** `s` (confirmed against the written spec — see § Size re-check).
**Shape to mirror:** #1705 (`e2e046d`, *"test(protocol): pin the model-list encoding with fixtures and publish it"*, 5 files, 284 insertions / 3 deletions). This slice is the same changeset shape for the command list.

## Files to read first

Symbols, not line numbers — resolve each with `codegraph_node` / `codegraph_search`.

- `internal/protocol/interactive.go` → `SlashCommandListPayload`, `SlashCommand` and **both** their `MarshalJSON` methods — the struct doc comments already carry every measurement this ticket publishes (51 entries, 42/9 key-set split, 33 empty hints, 11 aliases, the `reset` case, the newline case). Read them before writing prose; the doc section is those comments turned outward, not a new derivation.
- `internal/protocol/interactive.go` → `ModelListPayload`, `ModelOption` and their `MarshalJSON` — the sibling whose three-way list-encoding split this shape repeats.
- `internal/protocol/interactive_test.go` → `TestModelListPayload_RoundTrip`, `TestModelListPayload_Empty_RoundTrip`, `TestModelListPayload_ZeroValue_RoundTrip` — **the three tests to mirror**, including the byte-guard device and the "chosen rather than captured" comment register. Copy the scaffolding; the content differs.
- `internal/protocol/interactive_test.go` → `TestSlashCommandListPayload_NilCommandsNormalises`, `TestSlashCommand_NilSliceEncodings`, `TestSlashCommandListType_IsNotClaudesVocabulary` — #1727's three tests. Two of their doc comments go stale the moment fixtures exist; see § Deliverable 5.
- `internal/protocol/envelope_test.go` → `readFixture`, `canonical`, and `internal/protocol/interactive_test.go` → `roundTripEnvelope` — the shared helpers. Note that `canonical` is `json.Compact` only: it does **not** re-encode through the struct, which is why an `omitempty` mutant reddens the round-trip byte comparison as well as the byte guards.
- `internal/protocol/testdata/model_list.json`, `model_list_empty.json`, `model_list_zero.json` — the three files to imitate byte-for-byte in structure: one line, compact, envelope-wrapped.
- `internal/e2e/realclaude/testdata/initialize_control_v2.1.239.json` → the array at `control_responses[0].response.response.commands` — **the authority for every fixture string**. Extract programmatically; do not retype.
- `docs/protocol-mobile.md` → § `model_list` (the section to mirror and to cross-reference), § `model_announced` (how #1705 wired a bidirectional cross-reference), § Application message types (the registry table), § Changelog (the `2026-08-22` entry, which explains why every choice in § `model_list` was made — read it before writing the new one).
- `docs/knowledge/features/protocol-package.md` § *Slash-command-list payload* — #1727's recorded lessons, including the two mutation-testing gotchas this ticket's § Deliverable 3 procedure is built to defeat.

## Context

`TypeSlashCommandList` landed in #1726; `SlashCommandListPayload` / `SlashCommand` and their two `MarshalJSON` normalisers landed in #1727. The shape is therefore fully declared and **completely unpinned by bytes**: the package has no `slash_command_list*` fixture and `docs/protocol-mobile.md` has no § `slash_command_list`. A client author writing a decoder today reads Go structs, which is exactly the gap #1704 left and #1705 closed for `model_list`.

Two consumers are blocked on this slice, not on the producer: [pyrycode-desktop#681](https://github.com/pyrycode/pyrycode-desktop/issues/681) (Actions-menu grey-out, matches menu entries against the list) and [pyrycode-desktop#694](https://github.com/pyrycode/pyrycode-desktop/issues/694) (slash-command type-ahead, renders name + argument hint + description per row).

Scope is fixtures and prose. **No producer, no emit, no handler, no relay wiring, and no change to either struct's fields.** If writing the fixtures shows the declared shape is wrong, that is rework of #1727 (route `needs-rework:po`), not a quiet edit here.

**No ADR is warranted.** Every decision this slice records is a fixture value or a prose statement about an already-declared shape; the design decisions themselves were made in #1726/#1727.

## Design

Five deliverables. Nothing under `internal/` outside `internal/protocol/` is touched, and no `*.go` production file changes except the one doc comment in § Deliverable 5.

### Deliverable 1 — three committed fixtures under `internal/protocol/testdata/`

Filenames follow the sibling exactly: `slash_command_list.json`, `slash_command_list_empty.json`, `slash_command_list_zero.json`. Each is **one compact line**, envelope-wrapped, `"type":"slash_command_list"`. Envelope `id`s `717`, `718`, `719` (next free after `model_list_zero.json`'s `716`); `ts` any distinct RFC3339 instant.

**Produce the bytes by marshalling in Go and committing the output — never by hand-writing escapes.** Go's encoder escapes `<`, `>` and `&` as `<` / `>` / `&` and passes all other non-ASCII through as raw UTF-8. Both behaviours appear in these fixtures (see the row table), and hand-typing either is how a fixture ends up pinning a decoder that does not exist.

#### `slash_command_list.json` — the populated frame

`conversation_id: "c1"`, `dropped_commands: 2` (**chosen**, non-zero for `model_list.json`'s reason: the fixture must pin the value rather than the zero encoding). Five rows, **in claude's own capture order**, drawn from the 51-entry array named above:

| # | capture `name` | `aliases` | `argument_hint` | `description` | `truncated_fields` | what this row is for |
|---|---|---|---|---|---|---|
| 1 | `claude-api` | *none* → `[]` | `""` | measured (1078 B / 1068 runes, **2 embedded newlines**, non-ASCII em dashes) | `["description"]` | the 42-of-51 key set, an empty hint, **the only measured control character on this path**, raw-UTF-8 pass-through, and the cut report |
| 2 | `clear` | `["reset","new"]` | measured (`[name]`) | measured (95 B) | `null` | **the load-bearing row**: `reset` is the desktop Actions menu's own entry and is an alias, not a name. Multi-element alias array. |
| 3 | `config` | `["settings"]` | measured (`key=value`) | measured (20 B) | `null` | a **single**-element alias array beside row 2's two, and a plain non-empty hint |
| 4 | `model` | *none* → `[]` | measured (`<model>`) | measured (32 B) | `null` | a second 42-of-51 row, and the `<`/`>` **HTML-escape** pin — this row is why `<` appears in the committed bytes |
| 5 | `usage` | `["cost","stats"]` | `""` | measured (69 B — the capture's **median** description) | `null` | aliases **and** an empty hint on one row: the row a name-only matcher misses *and* that renders with nothing after it |

Rows are in ascending capture order (`claude-api` 18th, `clear` 24th, `config` 27th, `model` 35th, `usage` 44th of 51). Two values are **chosen rather than captured**, and the test comment must say so in `TestModelListPayload_RoundTrip`'s register:

- **`"aliases":[]` on rows 1 and 4.** claude never sends an empty alias array — 0 of 51 entries carry one, 42 omit the key. `[]` is the position `SlashCommand.MarshalJSON` decided for both absent and empty, so it is what the wire states and what a fixture must show.
- **`truncated_fields: ["description"]` on row 1.** Nothing cut it; the fixture carries that description at its full measured length. The flag is chosen to discriminate a struct that drops or mis-wires the field, and `claude-api`'s description is the sharpest pairing available — 1078 bytes against a median of 69, in the 10-of-51 population a per-field bound cuts first. This is exactly the register `model_list.json` uses for its own `truncated_fields: ["value"]` on a row carrying its full measured value.

Row 1's description is too long for a table literal, and it is the only one that is. Assert it by **measured property** — byte length, rune length, `strings.Count(d, "\n") == 2`, and a prefix — in its own `t.Run` rather than as an exact string in the row table. The other four rows carry exact strings, none longer than 95 bytes.

#### `slash_command_list_empty.json` — the frame carrying no commands

`conversation_id: "c1"`, `commands: []`, `dropped_commands: 0`. The list key is **present as `[]`, never elided** — an empty list is a positive statement that claude offered nothing.

#### `slash_command_list_zero.json` — the zero-value payload

`conversation_id: ""`, `dropped_commands: 0`, and `commands` carrying **exactly one all-zero entry**: `{"name":"","argument_hint":"","description":"","aliases":[],"truncated_fields":null}`. One entry, because a frame carrying no entries cannot reach the per-entry keys at all. The `aliases: []` beside `truncated_fields: null` inside that entry is the two marshallers' decided asymmetry, not a typo — `SlashCommand.MarshalJSON`'s doc says why.

This fixture is the only route to five of the eight wire keys at their zero value, and those five are precisely the five § Deliverable 3 says reach no assertion in the tree today.

### Deliverable 2 — three tests in `internal/protocol/interactive_test.go`

`TestSlashCommandListPayload_RoundTrip`, `TestSlashCommandListPayload_Empty_RoundTrip`, `TestSlashCommandListPayload_ZeroValue_RoundTrip` — the sibling trio's shape, same helpers (`readFixture`, `canonical`, `roundTripEnvelope`), same table-driven per-row subtests, same closing `roundTripEnvelope` call.

Scenarios, as bullets rather than pre-written bodies:

- **Populated.** Envelope `Type == TypeSlashCommandList`; `ConversationID == "c1"`; exactly 5 entries; a per-row subtest keyed on the command name asserting `Name`, `ArgumentHint`, `Description`, joined `Aliases`, and both forms of `TruncatedFields` (populated on row 1, `nil` on the other four — the `strings.Join` idiom the sibling uses). Row 1's description asserted by property: byte length, rune length, `strings.Count(d, "\n") == 2`, and a prefix. `DroppedCommands == 2`.
- **Empty.** A byte guard that the fixture carries `"commands":[]` — load-bearing exactly as the sibling's is: without it, an `omitempty` on `Commands` that also regenerated the fixture would elide the key from both sides and the round trip would still pass. Then `Type`, `ConversationID == "c1"`, `len(Commands) == 0`, `DroppedCommands == 0`.
- **Zero value.** Byte guards on the five keys whose zero value an `omitempty` would elide: `"conversation_id":""`, `"dropped_commands":0`, `"name":""`, `"argument_hint":""`, `"description":""`. Then decode and assert each field's zero value, `len(Commands) == 1`, and the entry's two list fields on the decode side — `Aliases` non-nil and empty (decoding `[]`), `TruncatedFields` nil (decoding `null`). The three list keys are additionally covered by #1727's constructed-value tests, the only route that reaches a nil slice at all.

State in the populated test's doc comment that the five rows are a **sample** of the capture's 51, chosen to cover both key sets and the four client-facing properties — not the whole array, and not evidence of any cap.

### Deliverable 3 — the `omitempty` mutation run (AC 2)

Eight wire keys: `conversation_id`, `commands`, `dropped_commands` on the payload; `name`, `argument_hint`, `description`, `aliases`, `truncated_fields` on the entry. **Each is run, one at a time, and what it actually reddens is recorded** — the run is the evidence, not the table below.

**The scoping trap is real and #1727's code review already recorded it.** `json:"conversation_id"` appears 17 times in `interactive.go`, `truncated_fields` 6, `description` 3, `name` 2. An unscoped substitution reddens a dozen unrelated tests and credits this ticket's mutant with a red no fixture here produced. Scope every mutant to the struct under test, assert the substitution count, and `diff` before trusting the verdict. Run it via `go test -overlay` so nothing is written into the worktree:

```bash
# One mutant. SRC = worktree root, SCRATCH = scratchpad dir. Repeat eight times.
python3 - "$SRC/internal/protocol/interactive.go" "$SCRATCH/interactive.go" SlashCommand argument_hint <<'PY'
import re, sys
src, dst, struct_name, key = sys.argv[1:5]
text  = open(src).read()
start = text.index(f"type {struct_name} struct {{")
end   = text.index("\n}\n", start)
span, n = re.subn(f'json:"{key}"', f'json:"{key},omitempty"', text[start:end])
assert n == 1, f"{n} substitutions inside {struct_name} — refusing"
open(dst, "w").write(text[:start] + span + text[end:])
PY
diff "$SRC/internal/protocol/interactive.go" "$SCRATCH/interactive.go"   # MUST be exactly one changed line
printf '{"Replace":{"%s":"%s"}}' "$SRC/internal/protocol/interactive.go" "$SCRATCH/interactive.go" > "$SCRATCH/overlay.json"
cd "$SRC" && go test -overlay="$SCRATCH/overlay.json" ./internal/protocol/
```

`type SlashCommand struct {` does not match `type SlashCommandListPayload struct {`, so the struct name selects the intended span unambiguously.

**Predicted matrix — verify it, do not copy it.** Any disagreement between prediction and run is a finding to report, not something to reconcile silently.

| wire key | predicted red | mechanism |
|---|---|---|
| `conversation_id` | zero | byte guard + round trip |
| `commands` | empty | byte guard + round trip (already red under #1727) |
| `dropped_commands` | empty, zero | round trip; byte guard on zero |
| `name` | zero | byte guard + round trip |
| `argument_hint` | zero, **populated** | byte guard on zero; round trip on populated, via rows 1 and 5's empty hints |
| `description` | zero | byte guard + round trip |
| `aliases` | zero, populated | round trip (already red under #1727) |
| `truncated_fields` | populated, zero | round trip (already red under #1727) |

The five AC 2 names — `conversation_id`, `dropped_commands`, `name`, `argument_hint`, `description` — must each be reddened by a test **this slice commits**, and the zero-value fixture is what reaches all five. Record the result in the populated/zero tests' doc comments the way #1705's changelog entry records its nine.

### Deliverable 4 — `docs/protocol-mobile.md`

Four edits. **Placement is load-bearing:** the new `#### slash_command_list` lands **immediately after § `model_list` and before `#### Reconnect replay & resync (consumer, #647)`**, which is what keeps every turn-stream count checkable by counting `####` headings.

1. **Registry row** in § Application message types, immediately after the `model_list` row, in that row's exact form: direction `binary → phone`, v1 `no`, and a description naming the source (`initialize` control reply's `commands` array), the two consumers, that the shape was declared by #1727 with the type by #1726 and the fixtures/section by #1718, that **nothing emits it yet (#1720)**, and a `[Interactive events](#interactive-events-v2-capability-gated)` pointer. Cross-link `[`model_list`](#model_list)` as the sibling from the same reply.
2. **`#### slash_command_list`**, mirroring § `model_list`'s structure wholesale — the content differs, the scaffolding does not:
   - Direction paragraph, including its own *"conversation-scoped menu, distinct from the fifteen turn-stream events above"* sentence, that it arrives on a `control_response` rather than the turn stream, that receiving one **neither opens nor closes a turn**, and that it is a **snapshot**, not a delta.
   - **Payload field table** — `conversation_id`, `commands` (always present, never `null`), `dropped_commands` (with § `model_list`'s honest qualification copied: nothing counts it, so `len(commands) + dropped_commands` is not the menu's true size and no cap is enforced).
   - **Per-entry field table** — `name`, `argument_hint`, `description`, `aliases`, `truncated_fields`.
   - **"Nothing emits this frame yet"** paragraph naming the producer slice **#1720**, the shape ticket #1727, the type ticket #1726, and the #1405→#1410 / #1616→#1638 / #1704→#1693 sequencing this is the fourth instance of.
   - **Four numbered client-facing properties**, in § `model_list`'s "things a client will otherwise get wrong" form:
     1. **A name-only match misses aliases, and the cheaper source cannot repair it.** 11 aliases across 9 of 51 entries; name them. `reset` is an alias of `clear` and is the desktop Actions menu's own entry, so a name-only matcher greys out a command that works. The same capture's `system`/`init` line carries a names-only `slash_commands` twin — identical 51 names, identical order, **not one of the 11 aliases** — which is the measured reason a client cannot be told to read the init line instead. (`terminal_slash_commands`, 2 entries, is a third array again and is neither.) Close with the COLLAPSE's one blind spot: because absent and empty are both `[]`, `truncated_fields` naming `aliases` is the **only** thing distinguishing "cut to nothing" from "none", and a client must read that as *unknown*, not as *no aliases*.
     2. **The count is workspace- and version-dependent.** 51 against claude 2.1.239 here; an earlier hand count against 2.1.220 in a different working directory reported 74. A client may not cache a count, assume a floor, or treat a small list as an error.
     3. **The strings are workspace-authored, bounded but not sanitized.** A command defined in a repository was written by whoever wrote that repository. `claude-api`'s description carries embedded newlines, and `0x0a` is the **only** sub-`0x20` byte anywhere across the 51 entries' four string fields — so newlines are *the* control character on this path rather than one class among several, and a type-ahead row assuming one line per description will not get one. 14 of 51 descriptions carry non-ASCII; one name is `__remote-workflow`, outside any obvious identifier charset. Render as inert text; never feed to an HTML sink, an attribute, or a URL.
     4. **A cut is reportable and must be read.** State the size with its **unit named**: the 51 entries serialise to **14,277 bytes of compact UTF-8**, and the same array with its non-ASCII `\u`-escaped is 14,371 — the two disagree precisely because 14 of the 51 descriptions carry non-ASCII. Say which the wire is: Go's encoder escapes `<`, `>` and `&` (and U+2028/U+2029, of which the capture contains none) and passes every other non-ASCII rune through as raw UTF-8, so a real frame is the UTF-8 form plus 6 bytes per escaped `<`/`>`/`&` — **neither of the two numbers exactly**, which is why the unit has to be named rather than implied. The committed fixture shows both behaviours side by side: `model`'s `<model>` hint beside `claude-api`'s raw em dashes. Longest description **1,145 bytes / 1,135 runes**, longest hint 121 B / 115 runes, longest name 24 B; mean description 207 B, median 69, and **10 of 51 exceed 256** in both units. So a per-field bound will cut real rows, and a client ignoring the report presents cut text as complete.
   - **SECURITY paragraph**, strengthening § `model_list`'s claude-authored warning rather than restating it: workspace-authored is a **lower-trust origin** than claude. Report, never a control input, with the one amendment `SlashCommand`'s doc comment already states — a client is meant to send a `Name` **back**, as ordinary message text; publishing a name does not make it trusted, it reaches no argv element, and this frame declares no inbound verb.
3. **Bidirectional cross-reference.** The new section links to `[`model_list`](#model_list)`; § `model_list` gains **one sentence** with a `[`slash_command_list`](#slash_command_list)` link, so a reader arriving at either learns that two frames publish a per-conversation menu from the same `initialize` reply and which is which (`model_list` inventories *identities*, `slash_command_list` inventories *verbs*). Use the bare-anchor form the neighbouring links already use (`#model_announced`, `#set_session_settings`).
4. **Changelog entry** at the top of § Changelog, in the `2026-08-22` entry's register: what landed, why, the consumers, that nothing emits it, the three fixtures and what each pins, that all eight `omitempty` mutants were **run**, the four properties, and the explicit no-count-moved statement.

**AC 5 has a deterministic check.** Apart from the single sentence added inside § `model_list`, the entire `docs/protocol-mobile.md` diff must be **additive**: `git diff -- docs/protocol-mobile.md` shows exactly one modified line and no other deletions. That mechanically proves the three "fifteen" sentences (§ Interactive events, § `session_transition`, § `model_list`) and § `unrecognized_message`'s "five" `system` subtypes are untouched. Verify it before committing; do not re-count by eye.

### Deliverable 5 — retract the deferral sentences this slice makes false

Three sentences in the tree defer to #1718 by name and expire the moment it lands. The ticket that lands owes the retraction; leaving them is a stale claim a later reader trusts.

- `internal/protocol/interactive.go` → `SlashCommandListPayload` doc comment: *"docs/protocol-mobile.md § slash_command_list — that section lands with the fixtures in #1718"*. The section now exists; drop the parenthetical. **This is a comment-only edit; no field changes.**
- `internal/protocol/interactive_test.go` → `TestSlashCommandListPayload_NilCommandsNormalises` doc comment: *"this slice (#1727) ships no fixture at all (internal/protocol/testdata/ is #1718's)"*. Re-point at the fixtures that now exist; the test's own reason — decoding `[]` always yields a non-nil slice, so only a constructed value reaches nil — is unchanged and stays.
- `internal/protocol/interactive_test.go` → `TestSlashCommand_NilSliceEncodings` doc comment: *"truncated_fields is pinned at all precisely BECAUSE it ships no fixture"* is now **false** — the populated and zero fixtures both pin `"truncated_fields":null`. Restate why the constructed test survives: it reaches the value / pointer / nested-in-payload forms and the caller's-backing-array assertion, which no fixture can.

Do **not** touch `docs/knowledge/features/protocol-package.md`, which carries its own #1718 deferrals — that file belongs to the documentation phase.

## Concurrency model

None. `internal/protocol` is a pure-data package: no goroutines, no locks, no shared mutable state. The only concurrency-adjacent property in scope is already pinned by #1727 and re-asserted by this slice's nested subtest — neither `MarshalJSON` may write back through its receiver into a caller's backing array, because a payload shared between an emitter goroutine and a per-connection fan-out would make that a data race as well as a correctness bug.

## Error handling

No new failure modes: no constructor, no validator, no branch. The three tests fail loudly (`t.Fatalf`) on unreadable fixtures or malformed JSON — `readFixture`'s and the sibling tests' existing behaviour. `make check` is the gate; `internal/protocol` is in the hermetic tier, so nothing here needs `make preship`.

## Testing strategy

1. `cd "$SRC" && go test -race ./internal/protocol/` green with the three new tests and the three fixtures.
2. Eight `omitempty` mutants run per § Deliverable 3, each with its `diff` verified as a single changed line, each result recorded. Five of the eight must be red on a test this slice commits.
3. The additive-diff check on `docs/protocol-mobile.md` per § Deliverable 4.
4. `make check` green.

## Open questions

- **`TestSlashCommandListType_IsNotClaudesVocabulary`'s constructed `clear` row does not match the capture, and this slice deliberately leaves it alone.** That test builds `clear` with `ArgumentHint: ""` and the description *"Clear conversation history and free up context"*; the capture's `clear` carries `[name]` and *"Start a new session with empty context; previous session stays on disk (resumable with /resume)"*, and its comment reads as attributing the empty hint to `clear` rather than to the row it constructs. The new fixture is capture-faithful, so the two will differ inside one file. **This is not a contradiction to reconcile** — that test pins wire *spellings*, not measurements, and its row needs no capture provenance. Do not edit it to match, and do not edit the fixture to match it. Worth one line from the documentation phase; not this ticket's edit.
- The producer's per-field bound and the entry cap behind `dropped_commands` are **#1719/#1720's** to decide. This slice states in prose that no cap is enforced today and pins the report's encoding; it decides no maximum.

## Size re-check (§ 4 of the architect checklist)

Counted against this written spec, not the sketch.

| Boundary | Limit | This spec |
|---|---|---|
| Production source files created or modified | ≤ 3 | **1** (`internal/protocol/interactive.go`, one doc comment; fixtures are `.json`, tests are `_test.go`, the protocol doc is `.md`) |
| Total written work | ≤ 400 | **≈ 345** — 3 fixture lines, ≈ 270 test lines (#1705's analogue: 235), ≈ 60 doc lines (#1705: 49), ≈ 12 retraction lines |
| New exported types or interfaces | ≤ 5 | **0** |
| Consumer call sites needing simultaneous update | ≤ 10 | **0** — nothing constructs either type |
| Acceptance criteria | ≤ 5 | **5** |
| Distinct error/reject branches in a state machine | ≤ 10 | **0** |

Nearest analogue re-derived with `git show --stat e2e046d`: **5 files changed, 284 insertions(+), 3 deletions(-)** — insertions plus deletions, 287. This slice adds a richer four-property section and the § Deliverable 5 retractions on top of that shape, landing under the boundary with room. Size **`s`** holds.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries] No findings, and stating why is the point of this slice.** The boundary is claude's subprocess stdout → the daemon, and it was crossed long before this frame: `SlashCommand`'s four strings are **workspace-authored**, a strictly lower-trust origin than the claude-authored strings `ModelOption` warns about, because a command defined in a repository was written by whoever wrote that repository. Nothing in this slice moves data across a boundary — no producer, no decode of live claude output, no handler. What it does is make the boundary *legible*: § Deliverable 4's SECURITY paragraph and property 3 are the client-facing statement of where the sanitization obligation sits, and it sits at the **client's render boundary**. The enforcing symbols are `SlashCommandListPayload.MarshalJSON` and `SlashCommand.MarshalJSON`, neither of which validates or rewrites content — by design, since a second bound here would be a second place the limit is decided and the two could disagree silently.
- **[Injection sinks — the finding that actually matters here] SHOULD FIX, and it is discharged inside the spec.** The realistic exploit against this frame is not against the daemon; it is a hostile repository planting a slash-command description that a careless client renders into an HTML sink, an attribute, or a URL. Two properties make this sharper than the `model_list` precedent: `claude-api`'s description carries **embedded newlines** (`0x0a` is the only sub-`0x20` byte across all 51 entries' four string fields), so a client splitting on lines or assuming a single-line row gets something it did not design for; and one name is `__remote-workflow`, outside any obvious identifier charset, so a client validating names against a charset guess will mis-handle a real entry. The spec addresses both by making them **committed bytes** rather than prose alone — the populated fixture's row 1 is the newline case, and § Deliverable 4 property 3 states the charset case. A client author copying the fixture meets the hazard on the first frame they decode. Code review should check the fixture actually carries the newlines rather than a flattened description.
- **[Escaping / encoder behaviour] No findings.** Go's `json.Marshal` escapes `<`, `>`, `&` and U+2028/U+2029, and leaves every other non-ASCII rune as raw UTF-8; the capture contains no U+2028/U+2029, and `0x0a` is its only sub-`0x20` byte. The spec requires the fixtures be **generated by marshalling in Go**, never hand-written, precisely so the committed bytes cannot pin an escaping the encoder does not produce. The `<>`-bearing row (`model`) makes that behaviour visible in the committed file rather than assumed. Note that the escape is HTML-*safe* output from Go's encoder and is **not** a sanitizer: it defends the JSON transport, not the client's DOM, and property 3 says so.
- **[Truncation as a security property] No findings.** `truncated_fields` is load-bearing rather than decoration: a client ignoring it presents cut text as complete. The COLLAPSE on `Aliases` creates one blind spot — absent and empty are the same `[]` position — so a report naming `aliases` is the only signal separating "cut to nothing" from "none". § Deliverable 4 property 1 requires the section to state that a client must read that as *unknown*, not as *no aliases*; a grey-out consumer that read it as *none* would enable a menu entry it cannot match. This is the one place where mis-reading the frame has a consumer-visible correctness consequence, which is why it is spelled out rather than left implicit.
- **[Inbound surface] No findings — none exists.** `TypeSlashCommandList` is outbound-only, classified `"push"` in `cmd/pyry/relay_guard_test.go`'s `excludedTypes` and held out of `inboundAppTypeSet` (`TestTypeConstants_V1V2Partition` and the `slash_command_list-rejected` case in `compat_test.go` are the deterministic rails). This slice declares no inbound verb and adds no handler, so there is no phone-supplied value reaching any code path. The one echo-back path — a client sending a `Name` back — arrives as **ordinary message text** on a path that does not consult this list and reaches no argv element, which is why this frame needs no `ModelOption.Value`-style inbound amendment. Unlike `model_list`, no field here is re-validated by `set_session_settings`, because none is ever sent to it.
- **[Tokens, secrets, credentials] Not applicable — and the absence is verifiable.** Every fixture string is drawn from a committed public capture of claude's own command list; no credential, token, path, hostname or user-identifying value is introduced. Code review can confirm by diffing each fixture string against `internal/e2e/realclaude/testdata/initialize_control_v2.1.239.json`, which is the spec's stated provenance for all of them.
- **[File operations] Not applicable.** Three new files under `internal/protocol/testdata/`, read at test time by `readFixture` with a constant name. No caller-supplied path, no traversal surface, no TOCTOU, no mode-sensitive write. The mutation procedure in § Deliverable 3 writes **only** into the scratchpad and reaches the build via `go test -overlay`, so no mutant can be committed by accident — which is itself the mitigation for the "mutant left in the tree" failure mode.
- **[Subprocess execution] Not applicable.** No `exec.Command`, no `sh -c`. The `python3` heredoc in § Deliverable 3 is a developer-run procedure over repo-local paths with a hard `assert` on the substitution count; it is not shipped code and executes nothing derived from untrusted input.
- **[Cryptographic primitives] Not applicable.** No randomness, no keys, no comparisons against secrets. The frame is sealed by the existing Noise session at the transport layer; this slice touches no transport code.
- **[Network & I/O — resource exhaustion] SHOULD FIX, deferred by design and named.** The measured array is **14,277 bytes** and the per-conversation event ring retains a large number of control-class events, so a menu frame is a real per-conversation memory multiplier rather than a one-off — and the count is workspace-dependent (51 here, 74 in an earlier count), so a hostile or merely large workspace inflates it. This slice **cannot** fix that: it emits nothing and enforces no cap. What it does is refuse to state a cap it does not have (§ Deliverable 4's `dropped_commands` qualification copies § `model_list`'s honest phrasing: nothing counts it, no cap is enforced, `len(commands) + dropped_commands` is not the true size), and § Open questions names **#1719/#1720** as the owner of the per-field bound and the entry cap. Deciding a bound here would be a second place the limit is decided.
- **[Error messages, logs, telemetry] Not applicable.** No log call, no error string, no metric. The three tests print fixture bytes on failure, which are committed public data.
- **[Concurrency] No findings.** Pure-data package. The one hazard in the family — a marshaller reaching through `p.Commands[i]` and mutating a caller's backing array shared between an emitter goroutine and a per-connection fan-out — is pinned by #1727's `TestSlashCommand_NilSliceEncodings` nested subtest, and § Deliverable 5 requires that test's comment be corrected without weakening the assertion.
- **[Threat model alignment] Addressed.** § Security model threat 1 (*prompt injection*, `severity: high`, `mitigation: partial`) is the live one: workspace-authored strings crossing to a phone are the injection surface, and this slice's mitigation is documentation plus committed bytes that make the hazard concrete. Threats 2–8 concern transport, pairing, key handling and relay operation, none of which this slice touches. No threat is silently dropped.

**Reviewer:** architect (self-review per the security-review checklist)
**Date:** 2026-08-24
