# #1838 — let a client send back the bracketed model values claude publishes

Widen `internal/relay`'s inbound model validator so the bracketed variant forms claude
publishes in its `initialize` model menu (`opus[1m]`, `claude-fable-5[1m]`) are accepted
and reach a session, without loosening the argv-injection defense that charset was chosen
for.

## Files to read first

Read these before writing anything. This is the turn-1 data load; the design below assumes
you have it.

**The thing that changes**

- `internal/relay/v2session_settings.go` → `validModel` — the current rule and, more
  importantly, its doc comment: the two properties the closed charset buys (no leading-dash
  flag impersonation; no shell metachar / whitespace / control byte / byte ≥ `0x80`). That
  comment is what has to still be true afterwards, restated for the wider grammar.
- `internal/relay/v2session_settings.go` → `handleSetSessionSettings` — the sole caller.
  Read its numbered "Order is load-bearing" list: step 3 validates **before** any
  persistence, and a rejection replies with the fixed constant `msgSettingsMalformed` and
  echoes no payload byte. Neither property may move.
- `internal/relay/v2session_settings.go` → `validEffort` — the sibling that is **not**
  widened here. Read it so you do not touch it by reflex.

**The two sinks the accepted value reaches**

- `internal/sessions/session.go` → `claudeSettingsArgs` — sink 1. Emits `--model` and the
  value as two separate elements of an argv slice; no shell is involved anywhere on the
  path. `spawnArgs` in the same file is how that suffix is recomposed onto the next spawn.
- `internal/sessions/pool.go` → `deliverSettingsInBand` — sink 2, and **the branch a menu
  click actually travels**. It interpolates `"/model " + value` and writes it to the live
  child's stdin as one line of ordinary turn text (#1581 stopped restarting the child for a
  model change).
- `internal/sessions/pool.go` → `inBandDeliverable` — why sink 2 is the live path: it
  returns true for any present, non-empty model not paired with a YOLO enable, which is
  exactly the shape a menu click produces.

**The live-claude harness AC 4 extends**

- `internal/e2e/realclaude/interactive_stream_inband_model_test.go` → the file header's
  § Evidence, then `TestInteractiveStream_InBandModelChange_LiveChildReportsNewModel`,
  `inbandTapRecorder` / its `consume` method, `inbandSendTurn`, `inbandWaitResults`,
  `inbandPickTarget`, `inbandRunner`. Extract: the tap sits in `streamsup.Config.Stdout`
  **upstream of the parser**, so it sees raw claude stdout lines; A1–A4 read the *first* and
  *last* init-announced model, never a fixed index.
- `internal/streamsup/runner.go` → `RequestInitialize` — writes one `initialize`
  `control_request` onto the live child's held-open stdin, which is how the test makes claude
  emit its own model menu onto the tapped stdout. Also read `Config.RequestInitializeOnSpawn`
  in the same file to see why the daemon asks once per spawn and why this test asks
  explicitly instead.
- `internal/e2e/realclaude/testdata/initialize_control_v2.1.239.json` → the
  `control_responses[0].response.response.models` array. This is the exact nesting the tap
  must decode, and it is also the measurement that changes AC 4's target — see § Context.

**The claims that become false**

- `internal/protocol/interactive.go` → `ModelOption` — its `Value` paragraph ("Value does
  not round-trip today … Do NOT widen validModel … It belongs to whichever slice first makes
  a client send one (#1693)") and the one-line comment on the `Value` field itself. This
  ticket is that slice.
- `internal/protocol/interactive_test.go` → the marshal test whose fixture-free
  `ModelListPayload` uses `Value: "opus[1m]"`; its comment calls that "one of the two
  measured values internal/relay's validModel rejects".
- `docs/protocol-mobile.md` § `model_list` — three sites: the `value` row of the per-model
  field table, numbered property **3**, and the `2026-08-22` entry in § Changelog.
  Re-derive their positions; do not trust a line number.
- `docs/protocol-mobile.md` § Changelog → the `2026-08-19` entry, which opens
  "**Superseded by the `2026-08-20` entry above**" and then names which of its own claims
  still hold. That is the convention to copy for a dated entry that has become partly false.

**Conventions**

- `internal/protocol/testdata/model_list.json` — #1705's committed fixture, carrying both
  bracketed values. The hermetic tests source their strings from here conceptually; no live
  run is needed to know them.
- `CODING-STYLE.md` § "Comments — Citing Other Code" — cite the symbol, never the line.
  `make cite-guard` is a build gate, diff-scoped, with no depth and no range exemption.
- `docs/knowledge/features/v2-session-manager.md` → the `validModel` bullet under
  § "Inbound set_session_settings". **Read-only.** It states the old charset; the
  documentation phase corrects it after code review. Do not edit it.

## Context

`set_session_settings` is the only inbound path that accepts a model. Its validator
`validModel` admits `""`, or 1..64 bytes whose first byte is alphanumeric and whose every
byte is in `[A-Za-z0-9._-]`. The bracket is outside that set, so the two variant forms
claude publishes in its own model menu are refused before they reach a session. Once the
daemon publishes that menu (#1837), a client builds rows that fail on click — the defect
class pyrycode-desktop#682 reports for permission modes.

The charset is not an accident. It is #845's argv-injection defense against an untrusted
phone-supplied string, and #1704 and #1705 both deliberately left it alone, naming the
widening as belonging to whichever slice first makes a client send one. This is that slice,
so the widening is a decision to make explicitly rather than a typo to fix.

**Measured, and it changes AC 4's target.** The ticket quotes a 2026-08-21 measurement
against claude 2.1.220 in which the two bracketed values were `opus[1m]` (Opus, 1M context)
and `claude-fable-5[1m]` (Fable). The newer capture committed in this repo,
`internal/e2e/realclaude/testdata/initialize_control_v2.1.239.json`, shows a **different
menu**: six rows, and `opus[1m]` is **gone** — the Opus row is now plain `opus` →
`claude-opus-5`. The only bracketed value on 2.1.239 is `claude-fable-5[1m]` →
`claude-fable-5`.

Two consequences, and both are load-bearing for the design below:

1. **The hermetic tests are unaffected.** AC 1 asks that `opus[1m]` and
   `claude-fable-5[1m]` be *accepted by the validator*. That is a property of the grammar,
   not of what claude currently offers, and both strings are committed in
   `internal/protocol/testdata/model_list.json`. Pin both regardless of the menu.
2. **The live test must not hardcode a bracketed value.** AC 4's literal wording names the
   1M-context Opus row, which the installed claude may no longer publish. Hardcoding
   `opus[1m]` would make AC 4's test red for a reason that has nothing to do with this
   change, and hardcoding `claude-fable-5[1m]` would rot the same way on the next version
   bump. The test instead **asks the live child for its own menu** and sends back a value
   the child just published. See § Testing strategy.

   *Stated assumption:* AC 4 is satisfied by proving the round trip for *the* bracketed
   variant the running claude publishes, whichever one that is, rather than for Opus
   specifically. Its measurable claim — "claude's own per-turn announcement names [the
   bracketed] model rather than the one the preceding turn named" — is asserted unchanged.

**No ADR.** This is a narrowing of one existing validator's rule with its rationale already
living in that validator's doc comment. There is no cross-cutting decision to record beyond
what the comment and this spec carry, and `docs/knowledge/features/v2-session-manager.md`
already owns the prose home for the `set_session_settings` cluster. The documentation phase
should fold the new grammar into that overview and into
`docs/knowledge/features/protocol-package.md`, both of which currently state the old charset.

## Design

### The accepted grammar

State the rule as a grammar, not as a diff to a byte set. That is what makes it reviewable
in both directions.

```
model    := "" | base variant?
base     := alnum wordbyte*
variant  := "[" wordbyte+ "]"
wordbyte := alnum | "." | "_" | "-"
alnum    := [A-Za-z0-9]
```

plus the unchanged length bound: `len(model) <= 64` bytes.

Read it as: an optional trailing bracket group, whose contents come from the **same closed
byte class as the base**, appended to a value that is otherwise exactly what was accepted
before.

### What the grammar buys, per property

| Property | Why it holds |
|---|---|
| At most one bracket group | `wordbyte` admits neither `[` nor `]`, so nothing inside the group can open or close a second one. |
| The group is the value's final element | The group's `]` is required to be the value's **last byte**; there is no production for a suffix after it. |
| The group is non-empty | `wordbyte+`, not `wordbyte*`. |
| The group is balanced | A `[` with no final `]` has no parse; a `]` with no `[` is not a `wordbyte` and so cannot appear in `base`. |
| The group is not nested | Same reason as "at most one group". |
| The group does not lead | `base` is non-empty and its first byte is `alnum`. |
| No leading dash | Unchanged: `base`'s first byte is `alnum`. |
| No shell metachar, whitespace, control byte, or byte ≥ `0x80` anywhere | Every byte of an accepted value is `alnum`, `.`, `_`, `-`, `[` or `]`. Nothing else has a production. |

### Implementation shape

Two unexported functions in `internal/relay/v2session_settings.go`. No `regexp` — a 64-byte
shape check is a byte loop, and the package does not import `regexp` today.

- `modelWordByte(c byte) bool` — reports whether `c` is in `[A-Za-z0-9._-]`. The **one**
  place the closed byte class is written down; both `base` and the variant's interior
  consult it, so the two can never drift apart.
- `validModel(m string) bool` — unchanged signature and unchanged contract shape (`""` is
  accepted; the caller treats `false` as malformed).

The decomposition that makes three of the table's properties fall out of a single condition,
and is the part worth getting right: find the **first** `[`. If there is none, the whole
value is `base`. If there is one, then all three of the following must hold — and together
they rule out nesting, a second group, and a trailing suffix without any of the three
needing its own check:

1. the value's **last** byte is `]`,
2. the bytes strictly between them are non-empty and every one is a `modelWordByte`
   (which excludes both `[` and `]`),
3. the prefix before the `[` is a valid `base`.

Worked rejections to sanity-check the decomposition against, all of which AC 3 names:
`opus[1m][2m]` (interior holds `]`), `opus[[1m]]` (interior holds `[`), `opus[1m]x` (last
byte is not `]`), `opus[]` (interior empty), `[1m]` (empty prefix), `opus[1m` (last byte is
not `]`), `opus1m]` (no `[`, so `]` must be a `base` byte and is not).

### The two sinks, and what the widening does to each

Both sinks are stated because a value accepted here reaches both, and the second one is the
branch the user story travels.

**Sink 1 — argv.** `claudeSettingsArgs` appends `--model` and the value as **two separate
elements** of a `[]string` handed to `exec.Command`. There is no `sh -c` and no shell
anywhere on this path, so `[` and `]` are ordinary bytes in an `execve` argument vector:
they are metacharacters only where a shell parses them, and none does. The property that
mattered for argv — that the value cannot pose as a flag — is carried entirely by the
first-byte-`alnum` rule, which the grammar retains unchanged. No argv element can be split
or added by the value, because argv elements are passed as a slice rather than a string.

**Sink 2 — turn text.** `deliverSettingsInBand` builds `"/model " + value` and writes it to
the live child's stdin as one line. The property this depends on is that an accepted value
is a **single whitespace-free token**: it can neither terminate the line (no `\n`, no `\r`)
nor introduce a second word (no space, no `\t`) nor open a second slash command (no `/`).
That property comes from the closed byte class, and admitting `[` and `]` does not weaken it
— neither byte is a line terminator, a separator, or a command sigil. **This is checked, not
assumed:** `TestValidModel_ByteSetIsClosed` (§ Testing strategy) proves the closure
directly by walking all 256 byte values, and names those five bytes in its failure message.

**A third destination, not a sink.** The value is also persisted into the session registry
as a JSON string field (`Pool.UpdateSettings` → the registry entry's `Model`). It is never a
path component: every filesystem path on this path derives from the session id or the
session uuid, never from settings. Verified across `internal/sessions` — `Model` is read at
exactly three places (`claudeSettingsArgs`, `deliverSettingsInBand`, and the registry
entry's construction), and none builds a path.

### What is deliberately *not* changed

- **`validEffort`.** It carries the identical hazard in the same direction — a closed enum
  that happens to accept all five levels claude returns today — but there is nothing to
  widen it for yet. Widening against a hypothetical is not evidence-based. Do not touch it,
  and do not "improve" its doc comment while nearby.
- **The 64-byte length bound.** The longest bracketed value measured is
  `claude-fable-5[1m]` at 18 bytes. Widening the charset must not widen the length.
- **No allowlisting against the published menu.** A tempting alternative is to accept a
  model only if it appeared in a `model_list` this daemon published. Reject it: the menu is
  per-conversation and is not retained at the relay, so this would introduce cross-request
  state and a TOCTOU window, and it would still need a shape check underneath for the case
  where no menu has been published. `ModelOption`'s own SECURITY paragraph already states
  the correct posture — publishing a value does not make it trusted, and the daemon
  re-validates it — and a stateless shape check is the strictly simpler way to keep that
  true.
- **The rejection reply.** It stays the fixed constant `msgSettingsMalformed`, with no
  payload byte and no reason echoed. Telling the client *why* a model was rejected would
  quote attacker bytes into a wire string.

## Concurrency model

Nothing new. `validModel` and `modelWordByte` are pure functions of their arguments with no
package state, so they are callable from any goroutine and introduce no lock.
`handleSetSessionSettings` continues to run on the manager's single `Run` dispatch
goroutine, so the surrounding ordering is untouched.

The live test adds one `RequestInitialize` call from the test goroutine to a runner whose
stdin write path is already serialised internally, and the tap's new field is written under
the same `inbandTapRecorder.mu` that already guards `models` and `results` — required,
because `os/exec` drives `Write` from its own stdout-copier goroutine while the test
goroutine reads, which is a race under `-race`.

## Error handling

No new failure mode and no new branch in the handler. A value that fails the grammar takes
the existing step-3 path: the fixed `msgSettingsMalformed` reply, `CodeProtocolMalformed`,
nothing persisted, nothing logged about the value. `validModel` returns `bool`, so there is
no error to wrap and no per-reject log call to add — and none should be added.

Be precise about why, because the obvious justification is an overclaim. #833's posture is
that the relay path keeps settings values out of its own records: `handleSetSessionSettings`
logs `conn_id` and `session_id` only, and `deliverSettingsInBand` logs the setting *name*
and the error, never the value. An **accepted** value does reach the daemon log further
down, as one element of the composed argv in `streamsup`'s `"spawning claude"` record at
Info — that has always been true of every model value and is not changed here. A
**rejected** value reaches nothing, spawns nothing and is written nowhere, so a
reject-branch log would be the only place in the daemon where a refused, attacker-chosen
string is recorded. Leave it that way.

## Testing strategy

Three tests. Two hermetic (they run under `make check`), one live.

### 1. `TestValidModel` — extend the existing table

Same table-driven shape, same file. Add rows in three groups; keep the existing rows.

*Accept (AC 1 and the grammar's positive cases):*

- `opus[1m]` and `claude-fable-5[1m]` — the two values the ticket names, both committed in
  `internal/protocol/testdata/model_list.json`.
- `a[b]` — the minimal well-formed variant.
- A value of exactly 64 bytes whose tail is a bracket group (e.g. 60 `a`s plus `[1m]`), to
  show the length bound is measured over the whole value including the group.

*Reject — the bracket shapes AC 3 names:*

- leading: `[1m]`, `[1m]opus`
- unbalanced: `opus[1m`, `opus1m]`, `opus]1m[`
- empty: `opus[]`
- nested: `opus[[1m]]`, `opus[a[b]c]`
- second group: `opus[1m][2m]`, `a[b]c[d]`
- not final: `opus[1m]x`, `opus[1m]-beta`
- over the bound: the 64-byte accepted row plus one byte

*Reject — every argv-injection shape AC 2 names, now re-checked with a bracket present so
the widening cannot have opened a side door:*

- leading dash: `-foo[1m]`, and the existing `--dangerously-skip-permissions`
- shell metachar inside the group: `opus[1;rm]`, `opus[$x]`, `` opus[`x`] ``, `opus[a|b]`
- whitespace inside the group: `opus[1 m]`, `opus[1\tm]`
- control byte inside the group: `opus[1\x00m]`
- byte ≥ `0x80` inside the group: `opus[café]`
- separator inside the group: `opus[a/b]`

### 2. `TestValidModel_ByteSetIsClosed` — the property pin (new)

The table above is examples; this one proves the closure, and it is what makes the § Design
claim about sink 2 a check rather than an assumption. Three loops over all 256 byte values,
each asserting an **iff**:

- `validModel(string([]byte{b}))` is true **iff** `b` is alphanumeric — the first-byte rule,
  and the reason a leading dash or a leading `[` cannot pass.
- `validModel(string([]byte{'a', b}))` is true **iff** `b` is in `[A-Za-z0-9._-]` — the
  base's byte class. Note both brackets are correctly rejected here (`a[` is unbalanced,
  `a]` has `]` outside any group), which is the positive statement that the widening did
  **not** just add two bytes to the charset.
- `validModel("a[" + string([]byte{b}) + "]")` is true **iff** `b` is in `[A-Za-z0-9._-]` —
  the group interior's byte class.

Then one derived assertion with its own message, naming the bytes by name rather than by
code: none of `\n`, `\r`, `\t`, `space` or `/` is accepted in any of the three positions, so
`"/model " + value` at `deliverSettingsInBand` is one line carrying one token.

Build the loops with `string([]byte{...})`, never `string(rune(b))` — the latter
UTF-8-encodes any `b >= 0x80` into two bytes and would silently test a different input than
the one named.

*Non-vacuity.* Each loop is the sole red for a distinct mutant: adding `[`/`]` to the flat
byte class reddens loop 2; dropping the first-byte-`alnum` rule reddens loop 1; admitting a
byte ≥ `0x80` in the group reddens loop 3. The empty-group and unbalanced rejections are
**not** covered by these loops (every probe uses a non-empty, balanced group) — they are
covered by the table in test 1, which is why both tests exist.

### 3. AC 4 — the live-claude proof, as a second phase of the existing test

**This is a case, not new infrastructure.** Do **not** write a second test function with its
own pool, factory, tap and run goroutine: that would duplicate the whole live-session wiring
and the two copies would drift. Instead append a second phase to
`TestInteractiveStream_InBandModelChange_LiveChildReportsNewModel`, which already owns a
live child, a tap, and a settings path.

This is safe with respect to #1582's evidence, and the reason is structural rather than
careful: A1–A4 read from a `models` slice **snapshotted before the new phase exists**, so
appending turns after them cannot move what they assert. Keep A1–A4's conditions and their
message text byte-for-byte, keep the function name (the file header documents a `-run`
filter against it), and keep the existing phase first.

Changes, smallest first:

**(a) `inbandTapRecorder.consume` gains one arm.** It already decodes each line into an
anonymous struct and skips a decode failure silently. Add the nested shape for the
`initialize` reply to that same struct — `encoding/json` ignores keys a struct does not
declare, so only `value` and `resolvedModel` are retained and the reply's other thirteen
top-level keys (including the ~14 KB `commands` array) are discarded during decode. The
nesting, from the committed capture, is:

```
{"type":"control_response","response":{"subtype":"success","response":{"models":[
  {"value":"claude-fable-5[1m]","resolvedModel":"claude-fable-5", ...}, ...]}}}
```

Guard the new arm on `type == "control_response"` and a non-empty models array, retain the
rows under the existing mutex, and expose them through a snapshot accessor mirroring
`initModels`.

**(b) A picker, mirroring `inbandPickTarget`'s contract.** Return the first menu row whose
`value` contains `[` **and** whose `resolvedModel` differs from the model the child
announced before the change — the second condition is what stops the phase from asserting a
change that was already true. If no row qualifies, `t.Fatalf` listing every `value` the menu
offered. **Fail, do not skip**: a skip is indistinguishable from the package's
absent-credentials skip in the run count, and a claude that publishes no bracketed value at
all retires this ticket's premise, which somebody should be told about.

**(c) The phase body**, after A1–A4:

- Call `RequestInitialize()` on the captured runner (it is promoted through `inbandRunner`'s
  embedded `*streamsup.Runner`). Do this rather than setting
  `streamsup.Config.RequestInitializeOnSpawn` in the shared factory: the field would change
  the child's behaviour for the first phase too, and that phase carries measured evidence.
- Poll, bounded, until the tap has recorded a non-empty menu; `t.Fatalf` on timeout.
- Snapshot the announced models again and take the last one as this phase's baseline.
- Pick a row per (b), `t.Logf` the whole menu and the chosen row.
- `pool.UpdateSettings(pool.Default().ID(), sessions.SettingsUpdate{Model: &value})` —
  **model only**. A non-nil `YOLO`, even one equal to the stored value, or a
  present-but-empty `Model` routes onto the restart path via `inBandDeliverable` and would
  silently make the phase measure the mechanism it exists to distinguish itself from.
- Tolerate the settle wait exactly as the first phase does, then send a further turn with
  `inbandSendTurn`.

**(d) Assertions.** Three, each with its own diagnosis in the message:

- **B1** — the last announced model differs from this phase's baseline. This is AC 4's own
  wording, and it is the assertion that goes red if claude's `/model` refuses the bracketed
  form. Per the ticket's Technical Notes, a red here is a finding worth routing back on
  rather than papering over: it would mean claude publishes a value as "the argument you
  pass to select this model" and then declines it.
- **B2** — the last announced model equals the chosen row's `resolvedModel`. If B1 passes
  and B2 fails, the message must say so explicitly: the bracketed value applied, but claude's
  announcement disagrees with what its own menu said the row resolves to. That is a
  claude-side inconsistency to record, not a defect in this change.
- **B3** — the child pid is unchanged across the phase. AC 4 says "delivered to a *running*
  child"; without this, a respawn could produce B1's evidence through the recomposed argv
  instead of through the in-band write.

**(e) The file header.** Add a short block, in the file's existing correction style, saying
the function now carries two phases, which ACs each serves, and that the bracketed value is
read from claude's own reply rather than pinned — so a future reader does not go looking for
a stale `inbandModelTargets`-style constant that was deliberately not written.

**What this phase does and does not prove.** It exercises sink 2 (`deliverSettingsInBand`)
via `Pool.UpdateSettings`, which is the seam `handleSetSessionSettings` calls — but it does
**not** run `validModel`, which is unexported and in a package `internal/e2e/realclaude`
cannot reach. That split is deliberate and should be stated in the phase's comment: the
hermetic tests prove *the daemon accepts the value*, the live phase proves *claude applies
it to a running child*, and together they close the user story. Driving the relay handler
end-to-end would require a Noise handshake and a `V2SessionManager` around a live claude,
which is out of all proportion to what it would add.

**Running it.** The package is behind the `e2e_realclaude` build tag, so `make check` never
compiles it and `go vet` does not execute it — a file in this package can fail to build
while every gate stays green. Compile and run this one test explicitly:

```
go test -tags e2e_realclaude -race -v \
  -run TestInteractiveStream_InBandModelChange ./internal/e2e/realclaude/
```

Read the count of `=== RUN` lines, not the exit code: with no claude credentials every test
skips and the package exits 0, and a build failure exits 0 through a shell wrapper too.

## Documentation corrections

AC 5 names `docs/protocol-mobile.md`; the same claim also lives in two Go doc comments,
which are inside the developer's surface and must not be left contradicting the code.

**`docs/protocol-mobile.md` § `model_list` — three sites, positions re-derived:**

1. The `value` row of the per-model field table. It currently reads "not necessarily
   sendable back". The general caution survives — `effort_levels` still carries it — but it
   should no longer point at `value` as the example.
2. Numbered property **3**. Rewrite rather than delete. The half that retires is the model
   half: the two bracketed values are now accepted, and the "charset is not widened" sentence
   is false. The half that survives, and AC 5 requires to survive, is the `effort_levels`
   hazard — the inbound effort enum is closed and accepts all five levels claude returns
   today, so a level claude adds later would be published here and refused inbound. State
   the new model rule as the accepted **grammar** (a value, optionally followed by one
   trailing bracket group drawn from the same closed byte class), not as a byte-set diff,
   and say why the group is bounded that way.
3. The `2026-08-22` entry in § Changelog. It is a dated record, so **correct it in place
   using the file's own convention rather than rewriting history**: the `2026-08-19` entry
   opens "**Superseded by the `2026-08-20` entry above**" and then names which of its own
   claims still hold. Do the same here — open the correction, name the two claims that
   retire (the two values are rejected; the charset is deliberately not widened), and
   confirm that the rest of the entry still holds. Add a new dated entry for this ticket.

Do **not** touch that section's "Nothing emits this frame yet" correction — #1837 owns it
and is editing the same section.

**`internal/protocol/interactive.go` → `ModelOption`:**

- The `Value` paragraph's last three sentences ("Value does not round-trip today …",
  "Do NOT widen validModel …", "It belongs to whichever slice first makes a client send one
  (#1693), with its own review") are now false, and the third one is a standing instruction
  against exactly what this ticket does. Replace with the accepted grammar and a pointer to
  `validModel` by symbol. Keep the surrounding SECURITY posture verbatim — publishing a
  value does not make it trusted, and the daemon re-validates it — because that is *more*
  true now, not less.
- The `Value` **field**'s one-line comment ("Does not round-trip today — internal/relay's
  validModel rejects the bracketed forms") is false in both clauses. Correct or drop it.
- The `EffortLevels` paragraph's direction-hazard sentence says `Value` "carries today" the
  same hazard. That cross-reference breaks: after this change only `EffortLevels` carries it.

**`internal/protocol/interactive_test.go`:** the comment on the fixture-free marshal test
calls its `Value: "opus[1m]"` "one of the two measured values internal/relay's validModel
rejects". The value stays (it is a fine encoding probe); the justification changes.

**Read-only, do not edit:** `docs/knowledge/features/protocol-package.md` and
`docs/knowledge/features/v2-session-manager.md` both state the old charset. The
documentation phase owns them and runs after code review.

## Open questions

1. **Does claude's `/model` accept the bracketed form it publishes?** Unmeasured. The live
   phase measures it, and per the ticket's Technical Notes a red B1 is a finding to route
   back on — it would mean the defect moves from the daemon to claude rather than closing.
2. **Does the init line's `model` field equal the menu row's `resolvedModel` for a bracketed
   value?** One confirming data point exists (`haiku` → `claude-haiku-4-5-20251001`, matching
   the menu), but nothing has measured a bracketed row. B2 is the assertion that answers it,
   with a message that distinguishes this case from a failure of the change.
3. **Whether `opus[1m]` returns.** The 2.1.220 measurement saw it and the 2.1.239 capture
   does not. Nothing depends on the answer: the hermetic tests pin the string regardless of
   the menu, and the live phase reads the menu rather than a constant.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No MUST FIX. The boundary is one named function, `validModel`,
  called from `handleSetSessionSettings` at step 3 of its documented order — before any
  persistence — and the design moves neither the function nor the step. The "only inbound
  path" claim was **verified, not inherited**: `Pool.UpdateSettings` has exactly one
  non-test caller chain, `cmd/pyry`'s `settingsUpdaterAdapter` implementing
  `relay.SettingsUpdater`, reached only from `handleSetSessionSettings`. There is no control
  socket verb and no second remote route. One property worth stating because the spec relies
  on it and a reader will not derive it: the gate is a **write-time** check, not a read-time
  one — the registry load path rebuilds `SessionSettings` from the stored entry without
  re-validating — so a value admitted today survives any later narrowing of the grammar.
  Not exploitable (a stored value is only ever as dangerous as what the gate admitted when
  it was written), but it is the reason the grammar must be right the first time rather than
  tightenable later.
- **[Tokens, secrets, credentials]** N/A by design. No token, key or credential exists on
  this path. The model value is not a secret, is not compared against one, and gates nothing.
- **[File operations]** N/A, verified rather than assumed. The accepted value reaches three
  destinations and no path component is among them: `claudeSettingsArgs` (argv),
  `deliverSettingsInBand` (turn text), and the registry entry's `Model` field (a JSON
  string). Every filesystem path on this path — the registry, the #943 `--settings` file —
  derives from the session id or uuid. `Model` is read at exactly those three sites across
  `internal/sessions`.
- **[Subprocess / external command]** The load-bearing category, and no MUST FIX.
  `claudeSettingsArgs` appends `--model` and the value as **two separate elements** of a
  `[]string` passed to `exec.Command`; no `sh -c` and no shell exists anywhere on the spawn
  path, so `[` and `]` are ordinary bytes to `execve` and cannot be glob-expanded, word-split
  or otherwise reinterpreted. **Checked and clean:** nothing joins a claude argv into a
  string anywhere in `internal/sessions`, `internal/streamsup` or `cmd/pyry` — the only
  `strings.Join` calls in reach are over log text and over CLI error messages, neither on
  this path. The flag-impersonation defense is carried entirely by the first-byte-`alnum`
  rule, which the grammar retains, and the § Design table derives that from the productions
  rather than asserting it. The second sink is the one the user story travels and gets the
  same treatment: `"/model " + value` stays one line carrying one token because the closed
  byte class admits no `\n`, `\r`, `\t`, space or `/`, and `TestValidModel_ByteSetIsClosed`
  proves that by walking all 256 byte values rather than by enumerating examples.
- **[Cryptographic primitives]** N/A. No primitive is touched; the reply still seals through
  `forwardEnvelope` unchanged, and nothing here compares an attacker-controlled value against
  a secret.
- **[Network & I/O]** No findings, with one deliberate non-change worth naming: the 64-byte
  length bound stays exactly where it is. Widening the *charset* must not widen the *length*,
  and the longest measured bracketed value (`claude-fable-5[1m]`) is 18 bytes, so there is no
  pressure to move it and no amplification into the registry, the argv or the wire. The
  validator is also specified as a byte loop with `regexp` explicitly ruled out — "widen a
  validator with a regex" is the classic ReDoS shape, and a linear scan over at most 64 bytes
  has no backtracking behaviour to exploit.
- **[Error messages, logs, telemetry]** No MUST FIX, and one **SHOULD FIX** the spec already
  states as a constraint: the rejection reply must stay the fixed constant
  `msgSettingsMalformed`, with no reason and no payload byte echoed. Telling a client *why*
  its model was refused is the natural next feature request and would quote attacker bytes
  into a wire string; code review should treat any such addition as a regression. Two things
  the adversarial pass corrected in the spec itself: an accepted value **does** reach the
  daemon log, as one element of the composed argv in `streamsup`'s `"spawning claude"` record
  at Info — true of every model value since long before this ticket, so not a new leak, but
  the spec's original "never logged at any level" wording was an overclaim and has been
  fixed. The follow-on question that raises is answered by the same closure: because the
  charset admits no newline, no quote and no control byte, an accepted value **cannot forge
  or split a structured log record** in that argv field. Log injection is closed by the same
  property that closes turn-text injection, which is an argument for keeping the two sinks'
  reasoning unified rather than special-casing either.
- **[Concurrency]** No findings. `validModel` and `modelWordByte` are pure functions of their
  arguments with no package state, so they add no lock and no ordering constraint;
  `handleSetSessionSettings` still runs on the manager's single `Run` dispatch goroutine. In
  the live test, the tap's new menu field is written under the same `inbandTapRecorder.mu`
  that already guards `models` and `results` — required, since `os/exec` drives `Write` from
  its own stdout-copier goroutine while the test goroutine reads, which is a race under
  `-race`.
- **[Threat model alignment]** The relevant threat in `docs/protocol-mobile.md` § Security
  model is a paired-but-hostile or compromised client sending a crafted
  `set_session_settings`. The authorization boundary — pairing plus the negotiated
  `interactive` capability — is untouched; only the value grammar widens. #845's original
  argv-injection threat stays addressed by the retained leading-dash bar and the closed byte
  class. The turn-text-injection threat, which became real when #1581 moved delivery in-band
  and had not been written down anywhere, is **newly stated and newly machine-checked** here
  — that is the one place this ticket strengthens the posture rather than merely preserving
  it. Explicitly rejected as an alternative, and worth recording as a threat-model decision:
  validating an inbound model against a `model_list` this daemon previously published would
  introduce cross-request state and a TOCTOU window while still needing a shape check
  underneath, so the stateless shape check remains the design.
- **[Out of scope]** `validEffort` carries the identical hazard in the same direction and is
  deliberately not widened — its closed enum accepts all five levels claude returns today, so
  there is no observed failure to widen against, and doing so on a hypothetical would be the
  opposite of evidence-based. It has no successor ticket yet; whichever slice first needs a
  level claude has added should take it, exactly as this slice took the model half from
  #1704/#1705. The client-side half — a menu that greys out a row it cannot send — is
  pyrycode-desktop#682's class and belongs to the client.

**Reviewer:** architect (self-review per the security-review checklist)
**Date:** 2026-08-27
