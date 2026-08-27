# #1853 — decode the initialize response's `commands` array and report its size

**Size: s.** One production file (`internal/streamsup/parser.go`), one test file
(`internal/streamsup/parser_test.go`). No new exported symbol, no event field, no wire field, no
new constant, no cap.

## Files to read first

Read these before writing anything. Every entry names a **symbol**, not a line — resolve each with
`codegraph_node` / `codegraph_search` and read the declaration.

| Where | Symbol | What to extract |
|---|---|---|
| `internal/streamsup/parser.go` | `controlResponseLine` | The double-nested decode target you widen. Its doc's **"other thirteen top-level keys"** sentence — that number goes to **twelve** when `commands` is declared. |
| `internal/streamsup/parser.go` | `modelOptionLine` | The element-struct this slice's new type mirrors: the JSON-null carve-out paragraph, the "the decode is all-or-nothing at the LINE" paragraph, and the house doc register. Copy the *arguments*, not the prose. |
| `internal/streamsup/parser.go` | `emitModelList` | The four-rung classification you leave alone, and the exact statement order the count is inserted into (after the success gate, before the ack early return). |
| `internal/streamsup/parser.go` | `logControlResponse` | The record you widen. **Two** statements say the set is five — "Five attributes and NOTHING else" and "The attribute set is FIXED at five on every rung". Both are current and both need the edit. Read the `dropped` and `levels_dropped` justification paragraphs; the new attribute needs the same shape of argument. |
| `internal/streamsup/parser.go` | `systemInitLine` | The struct you must **not** touch. Its "twenty-one deliberately absent keys" doc is why `slash_commands` is not the shortcut. |
| `internal/streamsup/parser_test.go` | `TestParser_InitializeControlResponseRejectBranches` | The table the new reject rows join, and one of the five `wantAttrs` maps. |
| `internal/streamsup/parser_test.go` | `TestParser_ControlResponseAckIsConsumedSilently` | The second non-model-list `wantAttrs` map. It sits hundreds of lines from the model-list block — the #1812 miss the package overview records. |
| `internal/streamsup/parser_test.go` | `TestParser_ModelListEntryCountIsBounded`, `TestParser_ModelListEffortLevelCountIsBounded`, `TestParser_ModelListIsLoggedContentFree` | The other three `wantAttrs` maps. All five compare with `reflect.DeepEqual`; all five go red without the new key. |
| `internal/streamsup/parser_test.go` | `modelListLineFixture`, `modelEntryFixture`, `modelEntryWithFixture` | The fixture idiom. `modelListLineFixture` hard-codes an inner response of exactly `{"models": …}` — that is the one thing this slice has to generalise. |
| `internal/streamsup/parser_test.go` | `TestParser_InitializeControlResponseDecodesTheCapturedModels`, `capturedModelEntries`, `capturedModelString` | The capture-pin idiom you copy for `commands`: decode the expectation with **literal key strings**, never through the production target, and guard the capture's own shape with a `Fatalf` before using it as an expectation. |
| `internal/streamsup/parser_test.go` | `capturedInitializeLine` | How a captured `control_response` is replayed through `Parser.Write`. |
| `internal/streamsup/initialize_capture_test.go` | `capturedInitializePayload`, `initCaptureArms`, `initCaptureArmNoRequest` | The arm vocabulary and the payload-required wrapper. `initCaptureArmNoRequest` recorded no `control_response` and must be skipped, exactly as the models test skips it. |
| `docs/knowledge/features/streamsup-package.md` § "Decoding the initialize ack into `turnevent.ModelList` (#1811)" | — | The two lessons that bind this slice: **(a)** any new attribute on `logControlResponse` needs *every* `wantAttrs` map in the file, not only the ones naming the feature that grew it — #1812 learned this at its first green run; **(b)** a capture reader must not itself check the invariant its own test exists to pin. |
| `internal/e2e/realclaude/testdata/initialize_control_v2.1.239.json` | — | The bytes. Three responding arms, byte-identical 51-entry `commands`, four-key vocabulary, nine entries carrying `aliases`. |

## Context

`emitModelList` already decodes claude's initialize payload out of a `control_response` line and
reads exactly one thing out of it: `models`. Everything else in the payload — fourteen top-level
keys in the committed capture — is undeclared and therefore silently discarded.

That makes `commands` invisible in two directions at once. A `commands` that arrives as a number, a
string or an object is *ignored* rather than *refused*, because an undeclared key cannot fail a
decode; and a `commands` that stops decoding is a change nobody can observe, because the one Debug
record `logControlResponse` writes reports nothing about it. This slice closes both, and nothing
else: the array is declared so a shape change fails the whole-line decode onto the undecodable rung,
and its entry count joins the record so the decode has an observable.

The arm is live, not latent — `RequestInitializeOnSpawn` fires the ask once per spawned child and
`mapStreamsupConfig` sets it true, so every interactive spawn solicits this payload from a real
workspace. Every string in that array is **workspace-authored**: a slash command defined in a
repository was written by whoever wrote that repository, and the daemon reads it in whatever
directory the operator points a session at.

**No ADR.** This slice takes no decision the sibling decodes have not already taken — the
declare-only-what-you-read rule is `systemInitLine`'s, the all-or-nothing-at-the-line rule is
`modelOptionLine`'s, and the daemon-computed-integers-only rule is `logControlResponse`'s. It
inherits all three; it establishes none.

## Design

### 1. The new element type

Add one unexported struct beside `modelOptionLine`, declaring **one field**:

```go
type commandEntryLine struct {
	Name string `json:"name"`
}
```

`commandEntryLine`, not `commandOptionLine` — "option" is `modelOptionLine`'s word for a menu
choice, and a slash command is not one. Verified free of collision repo-wide.

**Why a named struct and not `[]json.RawMessage` or `[]string`.** AC 1 requires element-level
failure: an element that is a bare string, and an element whose `name` is a number, must both fail
the *whole-line* decode. `json.RawMessage` accepts every one of those shapes, so it would restore
exactly the silent-ignore this slice exists to remove. `[]string` would fail on the real capture,
whose elements are objects.

**Why only `name`.** The other three keys claude sends — `argumentHint`, `description`, `aliases` —
are deliberately absent, and `systemInitLine`'s doc is the standing argument: *absent from the decode
target is a stronger guarantee than a test sweep, because a field that is never declared cannot
leak.* It is also the whole memory story here. The capture's `commands` array is **14,277 bytes**
compact; the 51 `name` strings inside it total **494**. Declaring one field is what keeps 96 % of a
workspace-authored payload from ever becoming a Go string. Say that in the doc — it is the reason a
later reader must not "complete" the struct.

**The JSON-null carve-out is inherited verbatim, at both positions.** `encoding/json` documents
unmarshalling `null` into a non-pointer Go value as a no-op producing no error, so a null `commands`
lands as a nil slice and a null `name` lands as `""` — neither is a failure, exactly as
`modelOptionLine`'s doc already states for its own strings. AC 1 names both positions; the doc must
name both too.

**No validation of `name`.** Empty, non-ASCII, or outside `[a-z0-9-]` — all decode. The capture
carries `__remote-workflow`, which is the committed proof that no charset may be assumed. The field
is decoded and counted; it is never read, retained, bounded or logged, so there is nothing for a
validation rule to protect.

**That justification is "no sink", not "safe bytes", and the doc must say which.** A workspace-authored
`name` can be `../../etc/passwd`, can carry a `;` or a `[`, and can carry a sub-0x20 byte — none of
which the capture happens to contain, and none of which matters here because the string reaches no
sink at all: no `exec.Command` argument, no `filepath.Join`, no `filepath.Match`, no `regexp`, no log
attribute, no event field, no wire frame. Only `len` escapes the function. Write the reason down as
*no sink*, because the next slice that actually reads `Name` inherits this field with the validation
question looking already settled, and it will not be — the sinks appear with the first reader, and
several of them (`filepath.Match`, `regexp`) treat bytes as syntax with no shell anywhere in sight.

### 2. Widening the decode target

`controlResponseLine`'s innermost anonymous struct gains one field beside `Models`:

```go
Commands []commandEntryLine `json:"commands"`
```

A plain slice, not a pointer to one: absent, null and empty are answered identically (count 0), so a
pointer would buy a distinction nothing acts on — `modelOptionLine`'s own argument for `[]modelOptionLine`.

**Doc edit on this struct.** Its closing paragraph reads *"The initialize payload's other **thirteen**
top-level keys are absent for that same reason"*. The capture's payload has fourteen top-level keys;
with `commands` declared alongside `models`, twelve remain. Change the numeral, and keep the
`account` sentence that follows it — `account` is still undeclared and that is still the point.

### 3. Where the count is taken

Inside `emitModelList`, between the success gate and the ack rung's early return:

- rung 1 (undecodable) returns **before** the count exists → reports 0.
- rung 2 (nak) returns **before** the count is taken → reports 0. This placement *is* AC 2's "the
  success gate refuses to read a payload out of a response reporting failure", and it is the one
  ordering decision in this slice. Taking the count above the subtype comparison would report a
  count off a response that announced failure.
- rung 3 (ack) reports the count.
- rung 4 (model list) reports the count.

One local `int` read from `len(cr.Response.Response.Commands)`, threaded to all four
`logControlResponse` calls. No other statement in `emitModelList` moves; the classification, the
caps, the loop and the emit are untouched.

**The decode's input stays `line` — the top-level bytes — and that is load-bearing, not incidental.**
`streamLine`'s doc states the property it preserves: control shapes are read from the top level only
and nested content is never re-scanned, which is what stops a tool result whose text is literally
`{"type":"result"}` from forging a turn boundary. The same rule now covers `commands`: decoding this
array from anywhere but the top-level line would let claude's own **tool output** announce a command
inventory the daemon never solicited. Nothing in this slice moves that input, and the developer must
not "helpfully" reach for a nested field to get at the array.

### 4. The record

`logControlResponse` takes one more `int` and writes one more attribute, named `commands`.

**Parameter position: last.** The existing three ints are one dimension — `models` with `dropped` and
`levels_dropped` qualifying it — and inserting a fourth between them would split a trio that reads as
a unit. Appending keeps the parameter order and the attribute order identical, so a reader checks one
order and not two.

**Four adjacent ints is a swap hazard, and it is pinned rather than designed away.** A swap between
`commands` and any of the model trio is detectable on exactly one rung — the ack rung, where the trio
is all-zero and `commands` is not — and § Testing's commands-without-models scenario is that pin. A
struct parameter would remove the hazard structurally but is a refactor this slice does not carry.

**What the number means, and how it differs from `models`.** `models` is the **emitted** count, after
`maxModelListEntries` cut. `commands` is the **decoded** count, because nothing caps, retains or
emits it — the scope boundary excludes a per-field cap and there is no event to carry a
`DroppedCommands` alongside. Two counts with different meanings on one record needs one sentence in
the doc, or the next reader infers a cap that does not exist.

**Admissibility.** A daemon-computed integer derived from a slice length, carrying none of claude's
bytes — the same footing that admits `models`, `dropped` and `levels_dropped` where no string from
the payload is admitted. No name, no `argumentHint`, no `description`, no alias reaches this record
on any rung. AC 4's "no log line carries decoded content" needs no assertion of its own: the five
`wantAttrs` maps are exact map equalities, so any attribute beyond the six turns them red.

**Doc edits on this function:** both "five" statements go to **six**, and the new attribute gets a
short justification paragraph in the register of the `dropped` one — *without* that paragraph's
operator-versus-client argument, which does not transfer: `dropped` completes a client-facing wire
field, and `commands` has no wire field to complete. Its argument is simpler and stronger — this
record is the **only** observable the decode has, on every rung.

### 5. Why no cap is owed, and where the bound actually lives

The scope boundary excludes a per-field cap. That is a scope statement, and on a
subprocess-supplied array it needs a reason behind it, so the developer should record one on the new
type rather than leave the absence bare.

The bound is `defaultMaxParseBuf` — **4 MiB on the whole line, applied before the decoder sees it**.
That constant is already the whole of what bounds the `models` array's transient spike, measured in
`controlResponseLine`'s neighbouring paragraph, and it bounds this one by the same mechanism. The
arithmetic favours this array on both sides:

- `commandEntryLine` is **one** field — a 16-byte string header — where `modelOptionLine` is five,
  including a slice header. So per densest-legal element (`{}`), the commands array's worst-case
  transient is a **fraction** of the already-accepted models one, and a single 4 MiB line cannot
  maximise both.
- It is **transient, never retained**. The slice lives from `json.Unmarshal` until `emitModelList`
  returns and is garbage from that point; nothing downstream is handed it, because nothing but an
  `int` leaves the function. That is a stronger position than `models`, whose *capped* result is
  retained for the session's life by `cmd/pyry`'s `sessionModelHold` and per conversation by the
  eventring.

So this slice adds a constant-factor transient to an input that was already bounded, retains nothing,
and introduces no new unbounded surface. A cap would be a defence against a failure mode that has not
been observed and that the existing 4 MiB line bound already covers. Naming the bound is the
deliverable; adding one is not.

### 6. Explicitly not done

- **`systemInitLine` is not widened.** claude's `system/init` carries a `slash_commands` array with
  these same 51 names in the same order, as bare strings, and the fourth capture carries it even
  though that arm recorded no `control_response` at all — so it is a live temptation, not a
  hypothetical one. That struct declares one field on purpose; adding a field there is a security
  decision about twenty-one deliberately-omitted keys, not a shortcut around this decode.
- No daemon-internal value, no retention, no per-field cap, no entry-count cap, no
  `turnevent` change, no `internal/protocol` change, no wire frame, no client.
- **`internal/e2e/realclaude`'s `inbandTapRecorder.consume` is not edited.** Its "other thirteen
  top-level keys, the ~14 KB commands array included, are dropped here" comment describes *that
  test's own* anonymous decode struct, which still declares only `models`. It stays true. Named here
  so the numeral sweep this slice does perform does not sweep it too.

## Concurrency model

None. `emitModelList` runs synchronously on the parser's single `consumeLine` goroutine, holds no
lock, and this slice adds one local `int` and one struct field. No goroutine, no channel, no shared
state, no shutdown sequence.

## Error handling

The four rungs are unchanged in number, in order, and in what each answers with. What changes is
which inputs reach rung 1:

| Input | Rung | `commands` attr |
|---|---|---|
| `commands` is a number, a string, or an object | undecodable | 0 |
| `commands` is an array whose element is not an object (a number, a bare string) | undecodable | 0 |
| an entry's `name` is a number, an object or an array | undecodable | 0 |
| `commands` is `null`, or an entry's `name` is `null` | **not** a failure — nil slice / `""` | 0 / N |
| `commands` absent, null, or `[]` | whichever rung `models` selects | 0 |
| subtype is not `success`, whatever `commands` carries | nak | 0 |
| success, non-empty `commands`, no `models` | ack | N |
| success, non-empty `models` and non-empty `commands` | model_list | N |

Nothing panics, nothing emits a `turnevent.Unrecognized`, nothing fails the stream. The unmarshal
error is still not logged, for the reason `emitModelList`'s undecodable arm already argues at
length — `encoding/json` quotes the offending input bytes into its error text, and with `commands`
declared those bytes are now workspace-authored command names. That rule was already load-bearing;
this slice makes it more so, and the doc should say so in one clause.

## Testing strategy

Scenarios, not code. Table rows go in the existing tables; new behaviour gets its own function.

**Fixture support (one new helper).** `modelListLineFixture` builds an inner response of exactly
`{"models": …}` and cannot express a `commands` payload. Add a sibling that takes the whole inner
object — `initializeLineFixture(t *testing.T, subtype string, inner map[string]any) string` — and
re-express `modelListLineFixture` as a one-line delegation to it, so the wrapper shape stays defined
in one place. `inner` values stay `any` for the same reason `models` is: a row must be able to put a
number where an array belongs.

**Rows added to `TestParser_InitializeControlResponseRejectBranches`** (all assert 0 events, exactly
one record, and the exact six-key `wantAttrs`):

- `commands` is a number → undecodable
- `commands` is a string → undecodable
- `commands` is an object → undecodable
- `commands` is an array whose element is a number → undecodable *(the element-level mismatch: no
  partial list survives)*
- `commands` is an array whose element is a bare string → undecodable *(the shape a future claude
  most plausibly sends, since `system/init` already spells these as bare strings)*
- an entry's `name` is a number → undecodable
- an entry's `name` is `null`, alongside a well-formed sibling entry → **ack**, `commands` = 2. The
  carve-out row: a null decodes to `""` and is counted, rather than failing the line.
- `commands` is `null` → ack, `commands` = 0
- `commands` is `[]` → ack, `commands` = 0
- **THE SUCCESS GATE, commands half: a `subtype:"error"` NAK carrying a well-formed non-empty
  `commands` array and no `models`** → nak, `commands` = **0**. This row is the sole detector for the
  count being taken above the subtype comparison instead of below it. Without it that placement is
  unpinned — every other nak row carries an empty or absent `commands` and reads 0 either way.
  Comment it as the load-bearing row, in the register the existing `THE SUBTYPE HALF` row uses.

**New test — the ack rung reports a count (AC 2's second half, AC 4's hand-built pin).** The capture
cannot supply this case: all three responding arms carry both arrays. Hand-build an inner response
carrying `commands` with **at least three** distinct entries and **no** `models` key, replay it, and
assert: 0 events; one record; attrs exactly `{type: control_response, reason: ack, models: 0,
dropped: 0, levels_dropped: 0, commands: 3}`. Three, not one — a mutant reporting a bare
present/absent bool, or `1`, or `len(models)`, all read wrong against 3 and right against 1. Add the
paired sub-case with neither array present → same map with `commands: 0`, which is the criterion's
"now distinguishable in the log from one carrying neither array" stated as an assertion rather than
as prose.

**New test — the capture pin (AC 4's first half).** For each arm in `initCaptureArms` except
`initCaptureArmNoRequest`:

- Read the arm's `commands` array with **literal key strings**, in a helper mirroring
  `capturedModelEntries` (`[]map[string]any` decoded off `capturedInitializePayload`). Not through
  `commandEntryLine` — an expectation read through the production target follows a wrong json tag
  green.
- Guard the capture's own shape first, with `Fatalf`s that name the arm, before using it as an
  expectation — the idiom `TestParser_InitializeControlResponseDecodesTheCapturedModels` already
  uses for its six entries and three key-counts: exactly **51** entries; every entry carries a
  **string** `name`; at least one entry carries `aliases` (the committed proof that an undeclared
  fourth key is tolerated rather than a decode failure — 9 of 51 do); at least one `name` falls
  outside `[a-z0-9-]` (`__remote-workflow` — the committed proof that no charset is assumed). A
  re-capture that changes any of these fails here, naming the arm, rather than silently narrowing
  what the assertion below proves.
- Replay `capturedInitializeLine(t, arm)` and assert the record's attrs are exactly
  `{type: control_response, reason: model_list, models: 6, dropped: 0, levels_dropped: 0,
  commands: 51}` — with `51` taken from the literal-key length, not transcribed. `models: 6` beside
  `commands: 51` is what kills an argument swap between the two.
- The helper must **not** cross-check the count against anything the record supplies; this test is
  the pin, and a reader that enforced the invariant would satisfy it by construction (the
  package overview's `capturedInitialize` lesson).

**The five `wantAttrs` maps.** Every one of them gains `"commands": "0"`, including the two that name
no model list: `TestParser_ControlResponseAckIsConsumedSilently` and
`TestParser_InitializeControlResponseRejectBranches`. The other three are
`TestParser_ModelListEntryCountIsBounded`, `TestParser_ModelListEffortLevelCountIsBounded` and
`TestParser_ModelListIsLoggedContentFree`. All five compare with `reflect.DeepEqual` and all five go
red without the key — the failure #1812 hit only at its first attempt at green, recorded in the
package overview precisely so the next attribute would not repeat it.

**Existing model-list behaviour is unchanged on every rung (AC 3)** and needs no new assertion: the
whole model-list block runs unamended except for the added map key.

**Gate:** `make check`. This slice adds no live-claude test and compiles nothing behind the
`e2e_realclaude` tag, so `make preship` is not required by the change itself.

## Citation rule

Every comment this slice adds or edits names a **symbol** and no line number. `make cite-guard` runs
inside `make check` and fails a line-number citation added to a `//` comment — no depth exemption, no
range exemption, and a bare `:NNN` slips the gate while being the least readable form. This family's
cites went stale twice in three days as siblings merged, which is why the rule is absolute here.

## The numeral sweep this slice owes

Three live claims go false and must move in the same commit:

| Symbol | Claim | Becomes |
|---|---|---|
| `controlResponseLine` | "the initialize payload's other **thirteen** top-level keys are absent" | twelve |
| `logControlResponse` | "**Five** attributes and NOTHING else" | Six |
| `logControlResponse` | "The attribute set is **FIXED at five** on every rung" | six |

Swept repo-wide; nothing else in `cmd/` or `internal/` states either count about these two symbols.
`parser_test.go`'s two "fourteen-key payload" comments describe the **capture**, which does not
change, and stay. `internal/e2e/realclaude`'s `inbandTapRecorder.consume` comment describes its own
struct and stays — see § 6.

Note the numeral sweep is a **word** sweep and a **numeral** sweep: run both `five`/`Five` and `5`
against these two doc regions before calling it done. A `\b`-anchored `git grep -E` finds nothing —
POSIX ERE has no `\b` — so use `-F`, `-P`, or the Grep tool.

## Open questions

- **Attribute name.** `commands`, matching `models`. If the developer finds a slog key collision in
  this record's consumers, the fallback is `command_count` — but `models` sets the precedent and
  neither name carries content, so `commands` should stand.
- **`docs/specs/architecture/1839-initialize-ask-per-child.md`** carries its own "admits five
  attributes" sentence. It is a frozen per-ticket build artifact, not a live doc, and it is outside
  the developer's writable surface. Left alone deliberately; noted so a reviewer sweeping the same
  words does not read it as a miss.

## Size accounting

Re-counted against this written spec, not against the sketch:

| Boundary | Limit | Here |
|---|---|---|
| Production source files created or modified | ≤ 3 | **1** — `internal/streamsup/parser.go` |
| New exported types or interfaces | ≤ 5 | **0** — `commandEntryLine` is unexported; `logControlResponse` is a method on an unexported receiver path |
| Consumer call sites needing simultaneous update | ≤ 10 | **9** — 4 `logControlResponse` calls, 5 `wantAttrs` maps |
| Acceptance criteria | ≤ 5 | **4** |
| Distinct reject branches in the state machine | ≤ 10 | **4** rungs, unchanged in number and order |
| Total written work | ≤ 400 | **~355** — parser.go ≈ 145 (new struct + doc ~30, the § 1 no-sink and § 5 bound clauses ~16, `controlResponseLine` field + numeral ~10, `emitModelList` count + doc + top-level-only clause ~32, `logControlResponse` signature + attribute + two numerals + justification paragraph ~45, 4 call sites ~10); parser_test.go ≈ 210 (fixture helper ~20, 10 reject rows ~55, ack-count test ~50, capture pin ~70, 5 map keys ~5, misc ~10) |

Nearest measured analogues, `internal/streamsup` only: **#1812** 328 insertions, **#1827** 518,
**#1821** 607. All three also widened `internal/turnevent/event.go` and #1812 widened
`internal/protocol` as well; this slice adds no event field, no wire field, no constant and no cap,
which is where the difference sits.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** SHOULD FIX — *addressed inline in § 3.* The boundary is subprocess stdout →
  parent state, and it is explicit and singular: `consumeLine` decodes the top-level line into
  `streamLine`, and `emitModelList` decodes the same **top-level bytes** into `controlResponseLine`.
  The first draft did not restate that the decode's input stays `line`, and a developer reaching for
  a nested field to get at the array would let claude's own **tool output** announce a command
  inventory the daemon never solicited — the exact forgery `streamLine`'s doc exists to prevent for
  `{"type":"result"}`. Now stated as a requirement in § 3. Downstream holds nothing: only an `int`
  leaves `emitModelList`, so no caller ever holds untrusted bytes from this array and no
  trusted/untrusted type signal is owed.
- **[Tokens, secrets, credentials]** No findings. No token, secret or credential exists on this path.
  The one credential-adjacent key in the payload is `account`, and it is **not declared** on
  `controlResponseLine` — never decoded, never bounded, never retained, never logged. The spec
  requires keeping that sentence when the neighbouring numeral is corrected, so the guarantee does
  not get edited away by the sweep. A workspace author could put a secret in a *command name*; the
  design's answer is that no string from this array reaches a log, an event, retention, or the wire.
- **[File operations]** No findings. No filesystem path is constructed from, or influenced by, any
  decoded value — `Name` reaches no `filepath` call at all. The only file this slice touches is the
  committed capture, read by `capturedInitializePayload`, which mints its path from package constants
  and rejects any arm outside `initCaptureArms`; that reader is test-only and already path-safe by
  construction. No file is created, so no mode, symlink or atomic-write question arises.
- **[Subprocess / external command execution]** No findings. Nothing here execs anything or assembles
  argv; `buildArgs` is untouched. Worth stating explicitly because the noun invites the error: a
  slash-command `name` is a **claude-side** command, and the daemon decodes and counts it — it never
  executes it, never passes it to `exec.Command`, and never shells out. No `sh -c` anywhere on the
  path.
- **[Cryptographic primitives]** No findings, and none applicable. No randomness, no key, no hash. The
  only comparison in the touched function is `cr.Response.Subtype != controlResponseSuccess`, which
  is pre-existing and compares against a package constant, not a secret — no constant-time
  requirement.
- **[Network & I/O — input size limits]** SHOULD FIX — *addressed inline in § 5.* The first draft
  left "no per-field cap" as a bare scope statement with no reasoning, which on a subprocess-supplied
  array is exactly the shape that reads as an oversight. The bound is `defaultMaxParseBuf`, 4 MiB on
  the whole line before the decoder sees it — already the whole of what bounds the `models` array's
  transient spike. Two facts make no new cap owed and both are now in the spec:
  `commandEntryLine` is **one** field where `modelOptionLine` is five, so per densest-legal element
  the worst-case transient is a fraction of the already-accepted one and a single line cannot
  maximise both; and the slice is **transient, never retained**, where `models`' capped result *is*
  retained by `sessionModelHold` and the eventring. Constant-factor amplification of an
  already-bounded input, no new unbounded surface, no observed failure — the evidence does not
  support paying for a cap here.
- **[Error messages, logs, telemetry]** No findings, and this is the category the design is built
  around. Six attributes, all `type`/`reason` constants or daemon-computed integers derived from
  slice lengths; no `name`, no `argumentHint`, no `description`, no alias, no `request_id`, no
  `error` string, no line bytes, on any of the four rungs. The unmarshal `err` stays unlogged for the
  reason `emitModelList`'s undecodable arm already argues — `encoding/json` **quotes the offending
  input bytes** into its error text — and this slice makes that rule strictly more load-bearing,
  since those quoted bytes are now workspace-authored command names. § 4 requires the clause saying
  so. Enforcement is deterministic rather than advisory: the five `wantAttrs` maps are exact
  `reflect.DeepEqual` map equalities, so a seventh attribute of any kind turns every one of them red.
  No telemetry, no metrics.
- **[Concurrency]** No findings. `emitModelList` runs synchronously on the parser's single
  `consumeLine` goroutine. This slice adds one function-local `int` and one struct field, spawns no
  goroutine, takes no lock, and touches no shared state — so there is no lock order to document, no
  check-then-mutate window, and no goroutine whose exit condition needs stating.
- **[Threat model alignment]** No findings; the relevant threat is named and out of scope by
  construction. `docs/protocol-mobile.md` § Security model governs the relay and phone legs, and
  nothing in this slice reaches the wire — no frame, no payload, no client. The subprocess-trust
  threat that *does* apply is claude's own claim about itself, and for the published model list it is
  answered by a **client-side render boundary** (`protocol.ModelOption`'s SECURITY paragraph). This
  slice publishes nothing, so no render boundary is owed by it — and the moment a later slice puts a
  command `name` in front of a client, one is, on that precedent. Named here so the next ticket
  inherits the obligation rather than the silence.

**Reviewer:** architect (self-review per the security-review checklist)
**Date:** 2026-08-27
