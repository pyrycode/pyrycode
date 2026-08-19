# #1616 — Declare the announced-model v2 wire type and payload

**Size:** S (confirmed against PO's `size:s`; see § Size check).
**Shape:** wire vocabulary only. Nothing emits the frame — that is #1617.

## Files to read first

Read these before writing anything. Every entry names a symbol; resolve it with
`codegraph_search` / `codegraph_node` rather than opening at a line.

| File | Symbol | What to extract |
|---|---|---|
| `internal/turnevent/event.go` | `ModelAnnounced` | The semantics this payload mirrors. Its field comments are the **single source of truth** — the payload points at them, it does not restate them. |
| `internal/protocol/interactive.go` | `RateLimitedPayload` | The nearest analogue's doc-comment shape: what it states, what it delegates, where its SECURITY paragraph draws the line. |
| `internal/protocol/interactive.go` | `UnrecognizedMessagePayload` | The `truncated bool` precedent — why a single bounded string gets a bool, not `truncated_fields`. |
| `internal/protocol/interactive.go` | `ThinkingProgressPayload` | The "points at `turnevent`'s field comments rather than restating them" manner the Technical Notes ask for. |
| `internal/protocol/codes.go` | `TypeRateLimited` | The const-block form: one grouped block, a rationale comment above it, the `MUST NOT be added to inboundAppTypeSet` paragraph. |
| `internal/protocol/compat_test.go` | `TestIsKnownAppType`, `v2OnlyTypes`, `TestTypeConstants_V1V2Partition` | Three of the four registration sites. Each takes one entry. |
| `cmd/pyry/relay_guard_test.go` | `excludedTypes`, `TestEveryInboundV2TypeHasHandler` | The fourth site. Assertion #3 enumerates every `Type*` constant in `codes.go`, so the entry is mandatory the moment the constant exists. |
| `internal/protocol/interactive_test.go` | `TestRateLimitedPayload_RoundTrip`, `TestRateLimitedPayload_ZeroValue_RoundTrip`, `TestRateLimitedType_IsNotClaudesVocabulary` | The three-test template this ticket copies, including why the zero-value fixture exists for the encoding rather than for a scenario. |
| `internal/protocol/interactive_test.go` | `roundTripEnvelope` | Reuse; do not write a new round-trip helper. |
| `internal/protocol/envelope_test.go` | `readFixture`, `canonical` | Reuse for fixture loading and for the `null` / explicit-zero byte assertions. |
| `internal/streamsup/parser.go` | `maxModelField` | The producer's 256-byte cap **and its already-written justification + envelope arithmetic**. Reuse the reasoning by reference; do not re-derive it. |
| `internal/turnbridge/outbound.go` | `MapEvent` | Read the `default` arm only, to confirm it stays untouched. This ticket adds no case here. |
| `docs/protocol-mobile.md` | § `rate_limited` | The section template: field table, "Like every frame in this section…", the declared-vs-emitted paragraph, the SECURITY paragraph. |

## Context

Since #1600 the daemon parses claude's `system` / `init` line into
`turnevent.ModelAnnounced{Model, Truncated}`. The value stops at the daemon
boundary: `turnbridge.MapEvent`'s `default` drops the variant, so no client can
see what claude actually announced for the turn.

This ticket declares the wire shape and stops. The declare-then-emit split is the
house sequencing — `rate_limited` was declared by #1405 and emitted by #1410; the
three background-task frames were declared by #1393 ahead of #1394.

The problem the shape has to solve is a **naming collision with a real semantic
difference**. Three v2 payloads already carry a wire field named `model`
(`ScreenSnapshotPayload`, `SessionSettingsPayload`, `SetSessionSettingsPayload`);
all three mean the **per-session override**, where `""` means "inherited default".
The new value means **what claude announced for this turn**. In the ordinary case
the two disagree — override `""` against a concrete identifier — and a client that
cannot tell them apart shows the wrong one.

## Design

### The type constant

`TypeModelAnnounced = "model_announced"`, in its own `const` block in
`internal/protocol/codes.go`, placed after `TypeRateLimited`'s block.

The name is **the daemon's variant name, not claude's subtype**. claude's subtype
is `init`; the wire follows `internal/turnevent`'s variant (`ModelAnnounced`) so a
claude rename lands in one place. This is the rule `TypeThinkingProgress` and
`TypeRateLimited` already follow, and each pinned it with a test.

The block's rationale comment must carry the `MUST NOT be added to
inboundAppTypeSet` paragraph its five siblings carry — this is an outbound
binary → phone event an old phone never receives, and it lives in `v2OnlyTypes`.

### The payload

In `internal/protocol/interactive.go`, after `RateLimitedPayload`:

```go
type ModelAnnouncedPayload struct {
	ConversationID string `json:"conversation_id"`
	Model          string `json:"model"`
	Truncated      bool   `json:"truncated"`
}
```

Three fields, and each is a decision:

- **`conversation_id`** — bridge-supplied, as for every frame in this family; the
  internal event carries no conversation identity.
- **`model`** — claude's announced identifier, verbatim. Wire-key decision below.
- **`truncated`** — a **bool**, following `UnrecognizedMessagePayload` rather than
  the siblings' `truncated_fields []string`. The payload bounds a single string, so
  a name list would be permanently either `nil` or `["model"]`: a variable-length
  container carrying one bit, plus a name the reader must check against the only
  field there is. The slice exists on the background-task and rate-limit payloads
  because they bound two to four fields and the report has to say *which*.

Nothing else from the init line rides along. The captured line carries 22 keys;
this payload carries the substance of one. `cwd` and `session_id` are not even
declared on the producer's decode target (`streamsup`'s `systemInitLine`), which is
a stronger guarantee than a test sweep — a field never declared cannot leak.

**No `MarshalJSON`.** The nil-normalising guard on `BackgroundTaskRosterPayload`
does not generalise; there is no slice field here and no absent-vs-empty
distinction to protect.

### Decision: the wire key stays `model`

The alternative considered was renaming the wire key to `announced_model` so the
homograph disappears structurally. **Rejected**, for three reasons:

1. The stated house convention is that a wire field name is the `turnevent` field
   name in snake_case — spelled out in `RateLimitedPayload`'s doc comment, whose
   purpose is insulation from *claude's* vocabulary, not from the daemon's own.
   `turnevent.ModelAnnounced.Model` gives `model`.
2. Every field on this wire is scoped by its envelope type. `conversation_id`,
   `truncated` and `status` all rely on exactly that scoping; `model_announced.model`
   is unambiguous in the only context a decoder ever reads it.
3. A rename does nothing for the client author this ticket is actually trying to
   reach — someone reading `screen_snapshot`'s row and never seeing the new frame at
   all. Only the doc cross-references (AC #2) reach them.

The residual risk is a client that flat-merges frames into one session-state object
and collapses the two keys. That failure mode is **hypothesized, not observed**, and
the disagreement it produces is loud (`""` against a concrete identifier) rather
than silent. Recorded here so a future ticket can revisit it cheaply if a client
actually hits it.

**Consequence for #1617:** `MapEvent` maps `ModelAnnounced.Model` → `model` and
`.Truncated` → `truncated`, a straight field copy with no renaming.

### The doc comment

Point at `turnevent.ModelAnnounced`'s field comments rather than restating them —
the manner `ThinkingProgressPayload` uses for its two consumer hazards. In
particular do **not** re-derive: the at-least-as-specific echo rule, the
not-reliably-dated consequence, the never-empty guarantee, or the bounded-and-
UTF-8-valid-is-all-it-is paragraph. Name them and delegate.

State here, because they are properties of *this type* rather than of the event:

- Conversation-scoped, not turn-scoped: no `turn_id`, and receiving one neither
  opens nor closes a turn. A per-turn announcement is not a turn boundary.
- The bound is the producer's, decided at construction (`streamsup`'s
  `maxModelField`), so this struct re-decides no maximum — a second cap would be a
  second place the limit is decided and the two could disagree silently.
- The three-way `model` collision, naming the three existing payload types.
- SECURITY, in the class `RateLimitedPayload` already establishes: claude-authored
  text that crossed the subprocess trust boundary, safe to render as inert text,
  never to an HTML sink / attribute / URL. A **report, never a control input**.

## Registration — four sites, all mandatory

`make check` goes red on any one missed. These are enumeration tables, one entry each.

| # | File | Symbol | Entry |
|---|---|---|---|
| 1 | `internal/protocol/compat_test.go` | `TestIsKnownAppType`'s case table | `{"model_announced-rejected", TypeModelAnnounced, false, ErrUnknownType}` — an old phone never receives it, and rejection is also what keeps a phone from sending one into `dispatch.Route`. |
| 2 | `internal/protocol/compat_test.go` | `v2OnlyTypes` | `TypeModelAnnounced: true` |
| 3 | `internal/protocol/compat_test.go` | `TestTypeConstants_V1V2Partition`'s `all` slice | `TypeModelAnnounced`, under a `// v2 announced-model report.` comment |
| 4 | `cmd/pyry/relay_guard_test.go` | `excludedTypes` | `"TypeModelAnnounced": "push"` |

Site 4 is not optional and not deferrable to #1617.
`TestEveryInboundV2TypeHasHandler`'s Assertion #3 enumerates every `Type*` constant
in `codes.go` and fails on an unclassified one — so the constant **cannot exist** in
a commit that omits its classification entry. Say so in the comment above it, as
`TypeRateLimited`'s and `TypeThinkingProgress`'s entries do.

Site 3 also has a self-checking tie: `TestTypeConstants_V1V2Partition` asserts
`len(inboundAppTypeSet) + len(v2OnlyTypes) == len(all)`, so sites 2 and 3 must land
together or the size check fails.

## Concurrency model

None. This ticket adds two exported identifiers to a pure data package and four
entries to test tables. No goroutines, no channels, no shared state, no shutdown
sequence. `internal/protocol` holds no runtime state.

## Error handling

No new error paths. The type constant participates in `IsKnownAppType`'s existing
`ErrUnknownType` rejection (site 1) and adds no branch of its own. The payload has
no constructor and no validation — the producer's gate and cap are upstream, and
re-deciding either here would create a second place the rule is decided.

## Testing strategy

Two goldens under `internal/protocol/testdata/`, one line each, ids **712** and
**713** (710/711 are `rate_limited`'s; the next free pair).

- `model_announced.json` — populated.
- `model_announced_zero.json` — every field at its zero value.

Three tests in `internal/protocol/interactive_test.go`, reusing `readFixture`,
`canonical` and `roundTripEnvelope`. Scenarios, not code:

**`TestModelAnnouncedPayload_RoundTrip`**
- Decode the populated fixture; assert `env.Type == TypeModelAnnounced`.
- Assert each of the three payload fields against the fixture's values, then
  `roundTripEnvelope`.
- Fixture `model` value: use a **measured** identifier, and prefer
  `claude-haiku-4-5-20251001` — the dated echo of the bare `haiku` alias, captured in
  `internal/e2e/realclaude/testdata/dropped_lines_v2.1.220.json`. Do **not** invent a
  plausible-looking identifier; a client author will copy the fixture as if it were
  measured. (Contrast `rate_limited.json`'s deliberately-unmeasurable
  `<unmeasured>`, which exists because no capture of its field's non-benign value set
  exists. Here captures do exist, so the fixture uses one.)
- `truncated: true` in this fixture, so the populated case and the zero case
  disagree on it and a struct wiring the key to the wrong field cannot pass.

**`TestModelAnnouncedPayload_ZeroValue_RoundTrip`**
- The no-omitempty pin: adding `omitempty` to any one of the three fields turns this
  round trip red, and no realistic fixture can make that claim for all three.
- Assert the fixture carries `"truncated":false` and `"model":""` explicitly — not
  elided — via `canonical`, then decode, assert the three zero values, round-trip.
- Document in the test comment, as `TestRateLimitedPayload_ZeroValue_RoundTrip` does,
  that this frame is one the bridge will never emit: `Model` is never empty (the
  producer's gate does not emit on an empty model) and the bridge always supplies a
  conversation id. **It exists for the encoding, not for the scenario.**

**`TestModelAnnouncedType_IsNotClaudesSubtype`**
- Assert `TypeModelAnnounced != "init"`, that it does not contain `"init"`, and that
  it equals `"model_announced"` exactly.
- **The trap, and it is the same shape #1405 hit:** claude's *key* for the value is
  `model`, so a `strings.Contains(TypeModelAnnounced, "model")` check would be **red
  against the correct name**. Do not write it. `model` here is the subject noun and
  the daemon's own field name — the discriminating word is claude's subtype `init`,
  which names claude's *line*, where ours names what the daemon reports.
- Marshal a populated `ModelAnnouncedPayload` and assert its bytes carry none of
  claude's excluded init-line keys: `cwd`, `session_id`, `tools`, `mcp_servers`,
  `permissionMode`, `slash_commands`. Non-discriminating today by construction —
  these are regression pins that go red the day someone adds them back.

**Negative pin — nothing emits (AC #3).** `internal/turnbridge`'s existing
`MapEvent` tests already cover the `default` arm. Add no case to `MapEvent` and no
test asserting emission. The correct evidence that nothing emits is that
`internal/turnbridge` is untouched by this commit.

## Documentation — `docs/protocol-mobile.md`

Five correction sites plus a new section plus three cross-reference rows. All are
in this one file; all land in this commit.

### New section

`#### model_announced`, placed **after `rate_limited` and before
`session_transition`** — inside § Interactive events, alongside its sibling frames.
Follow `rate_limited`'s section shape. It must record:

- The field table (three rows).
- The "binary → phone only, `interactive` capability-gated, carries an
  envelope-level `event_id`" paragraph every frame in the section carries.
- **Declared, not emitted.** Say it in the form § `rate_limited` uses for the
  inverse ("**Emitted since #1410.** The shape was declared by #1405 so a client
  could be written against it"): the shape is declared by #1616 so a client can be
  written against it, and **nothing emits it** — `turnbridge.MapEvent` has no case
  for `turnevent.ModelAnnounced` — until #1617.
- **The collision, explicitly (AC #2).** Name all three existing `model` fields —
  `screen_snapshot.model`, `session_settings.model`, `set_session_settings.model` —
  say that all three mean the **per-session override** (`""` = inherited default, no
  override), that this one means **what claude announced for this turn**, and that in
  the ordinary case they disagree because the override is `""` while claude has named
  a concrete model.
- **A lookup miss is ordinary.** claude echoes an identifier *at least as specific*
  as the one it was given: it dates a bare family alias (`haiku` →
  `claude-haiku-4-5-20251001`) and passes through anything already fully formed
  (`claude-haiku-4-5`, `claude-sonnet-5`). So the value is **not reliably dated** and
  **need not appear in any published model list** — `claude-haiku-4-5` does not. A
  client must treat a lookup miss as ordinary rather than as an error.
- **Never the empty string**, and the daemon does not repair the identifier.
- **Once per turn, not once per session** — a consumer that latches the first
  announcement shows a stale value; one that renders the latest has no problem.
  Conversation-scoped: no `turn_id`, opens and closes no turn.
- `truncated` says the daemon cut the value to fit its cap; a client ignoring it
  presents claude's cut text as complete.
- **SECURITY**, in the file's existing house posture for claude-derived strings:
  bounded but **not sanitized** — no control-character or terminal-escape stripping
  on this path — so it stays untrusted, model-influenced text all the way to the
  client. Safe to render as inert text, never to an HTML sink, an attribute, or a
  URL. A **report, never a control input**. Say plainly that the render boundary
  owing the sanitization is the **client's**, not the daemon's. Note that only a
  phone-supplied override is charset-validated (`internal/relay`'s `validModel`, a
  deliberately different bound); a `--model` flag or a config default never is.

### The five correction sites

| Site | Change |
|---|---|
| § Interactive events, the "These **fourteen** envelope types form the structured live-session stream" sentence | → **fifteen**. (Verified: the section documents exactly fourteen `####` frames today, `session_transition` excluded by its own text.) |
| § `session_transition`, "distinct from the **fourteen** turn-stream events above" | → **fifteen**. Same count, second site. |
| § `unrecognized_message`, the "known and deliberately ignored" bullet | Three statements, moving **differently** — see below. |
| § Application message types registry, after the `rate_limited` row | Add a `model_announced` row. Every documented frame has one; #1405 added `rate_limited`'s in its **declare** commit. Follow that row's shape, including its declared-vs-emitted clause. |
| Changelog (newest-first, top of the list) | New `2026-08-19` entry. Record the frame, the declared-ahead-of-producer sequencing (#1617), the `model` collision and how a client tells the two apart, the lookup-miss-is-ordinary property, and — as #1394 / #1386 / #1405 each did — **the count corrections this ticket makes**, including that the `system`-subtype count moved four → five because #1600's `init` arm was never reflected here. |

### The `unrecognized_message` bullet — three statements, three different moves

This is the site most likely to be got wrong. It is **not** a uniform count bump.
The paragraph currently makes three separate claims:

1. *"As of #1380–#1385 the daemon parser maps **four** `system` subtypes internally
   (`task_started`, `task_updated`, `background_tasks_changed`, `thinking_tokens`)"*
   → **five**, adding `init`. The attribution must also gain **#1600**, because
   #1380–#1385 did not add the `init` arm. **This sentence is already false on
   `main`** — #1600 added the arm and did not touch this file — so fixing it is a
   correction, not a consequence of the new frame.

2. *"All **four** now reach this wire under their own daemon-owned names, and all
   **four** are documented below"* → **stays four**. `init` is declared here and
   emitted in #1617, so it does not reach the wire on this ticket. A blanket bump
   would make this sentence false in the opposite direction. Reword so the sentence
   distinguishes the four that reach the wire from `init`, which is documented below
   as `model_announced` but does not yet reach it.

3. The #1404 clause's back-reference *"so the count of four above is unaffected"* →
   re-anchor to **five**. `rate_limit_event` remains a top-level line type and not a
   `system` subtype, so the claim itself is unchanged; only the number it points at
   moved.

Also check, and **leave alone**: the surrounding prose using `system/init` as its
example of a high-rate subtype whose surfacing would be noise. That framing survives
— `init` really does fire once per turn, and it still does not surface as
`unrecognized_message`. It can no longer be read as an example of a *silently
dropped* subtype, which statement 1's fix already handles. The closing "Every other
`system` subtype is still silently dropped exactly as before" stays correct once
statement 1 reads five.

### The three cross-reference rows (AC #2)

Each of the three existing `model` rows gains a pointer to the new frame, so that
reading **any one of the four** is enough to learn the other meaning exists:

| Section | Row |
|---|---|
| § Screen snapshot → `screen_snapshot` payload table | `model` row |
| § Session settings → `set_session_settings` payload table | `model` row |
| § Session settings → `session_settings` payload table | `model` row |

Keep each addition to one clause. Each already states the override meaning; append a
pointer along the lines of "this is the per-session **override**, not what claude
announced for the turn — see [`model_announced`](#model_announced)". Do not restate
the new frame's semantics in three places.

## The stale source comment (AC #5)

`internal/e2e/realclaude/interactive_stream_inband_model_test.go`'s header block
closes with *"It still reaches no CLIENT either — `turnbridge.MapEvent`'s `default`
drops the variant, so no wire frame exists for it yet."*

The first two clauses stay true. The third stops being true in the sense the repo
uses everywhere else — § `rate_limited` draws the declared/emitted line sharply — so
correct **the last clause only**: a wire frame is now declared (#1616), and what
remains true is that nothing emits it (#1617).

Use the repo's `CORRECTED (#N)` form. #1600 already corrected this same comment block
in that style, so the form is settled and the cost is one clause. Do not rewrite the
surrounding paragraphs; the `#833` log-posture half is untouched by this ticket.

This file is behind the `e2e_realclaude` build tag, so `make check` never compiles
it. Comment-only edit, no assertion changes — but do not let a typo in the block
ride: the package is `make preship`'s to compile, not `make check`'s.

## Out of scope — do not do these

- **No `turnbridge.MapEvent` case.** That is #1617, and AC #3 is that the `default`
  still drops the variant.
- **No knowledge-base doc.** `docs/knowledge/features/streamsup-package.md` (whose
  "adds the protocol type and the `MapEvent` case **together**" sentence this split
  invalidates) and `docs/knowledge/features/e2e-realclaude.md` (which carries the same
  "no wire frame exists for it yet" clause) both go stale on this ticket. Both belong
  to the **documentation phase**, not the developer — the same call #1600 and #1405
  made. Do not touch `docs/knowledge/**`.
- **No rewriting of dated build artifacts.** `docs/specs/architecture/1600-*.md`,
  `docs/specs/architecture/1582-*.md`, `docs/knowledge/codebase/1600.md`,
  `docs/knowledge/codebase/1385.md` describe the pre-emit world and are correct as of
  their own tickets.
- **No second cap, no validation, no normalisation** of the identifier in
  `internal/protocol`. The bound is `maxModelField`'s.
- **No charset check.** `internal/relay`'s `validModel` bounds a *phone-supplied
  override* and is deliberately a different rule; applying it here would reject
  identifiers claude legitimately announces.

## Size check

Applied to the design, not to PO's body alone.

| Red line | Limit | This ticket |
|---|---|---|
| New files | > 3 | **0** production files (2 one-line JSON goldens) |
| Total written LOC | > ~600 | **~380–420** projected |
| New exported types | > 5 | **2** (`TypeModelAnnounced`, `ModelAnnouncedPayload`) |
| Consumer call sites updated simultaneously | > 10 | **0** — additive; nothing consumes the constant because nothing emits it. The four registration entries are one-line table additions, not call sites. |
| Acceptance criteria | > 5 | **5** |
| Distinct reject branches | ≥ 10 | **0** — no state machine, no new error path |

Production source files touched (non-test `.go`): `internal/protocol/codes.go`,
`internal/protocol/interactive.go` — **2**, against the ≥ 5 commit gate.

The LOC projection is measured against the nearest analogue rather than estimated:
**#1405's declare commit `772b822` is 340 insertions across 8 files** — `codes.go`
+37, `interactive.go` +61, `interactive_test.go` +156, `compat_test.go` +9,
`relay_guard_test.go` +6, two one-line goldens, `protocol-mobile.md` +72. This
ticket is the same shape with more documentation work: five correction sites and
three cross-reference rows on top of the new section, plus a one-clause source-comment
fix, which is roughly +40–80 over #1405's total. No red line is within reach.

**Refactor-shape check:** not refactor-shaped. Verified with
`git grep -n 'TypeRateLimited' -- '*.go'` and the same for `RateLimitedPayload` /
`ThinkingProgressPayload`: every consumer site of an analogue constant outside
`internal/protocol` belongs to the **emit** half (`internal/turnbridge`,
`cmd/pyry/interactive_turn_v2_test.go`, `internal/e2e/**`), which this ticket does
not touch. The new constant has no consumers by construction.

**Not further splittable.** `TestEveryInboundV2TypeHasHandler`'s Assertion #3 means
the type constant cannot exist in a commit that omits its classification entry, so
"add the constant" and "register it" are not independently shippable.

**File-overlap check:** clean. `git fetch origin --prune` then a per-branch
`git diff --name-only origin/main...<branch>` over every `origin/feature/<N>` branch
found one hit — `origin/feature/449` touching `internal/protocol/codes.go` — and
#449 is **CLOSED** (2026-05-17), so that branch is stale rather than in flight. No
`addBlockedBy` needed.

## Open questions

- **Golden envelope ids 712/713** are the next free pair below the `812` outlier;
  if a concurrent branch claims them, any unused pair works — nothing asserts the id.
- **Whether the `model` wire key should have been `announced_model`** is settled for
  this ticket (see § Decision) but is the one call worth revisiting if a real client
  reports collapsing the two values. Cheap to revisit only *before* a client ships
  against the shape, which is exactly the window the declare-then-emit split opens.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No MUST FIX. The boundary is claude's subprocess stdout →
  daemon, and it is crossed **upstream of this ticket**: `streamsup`'s parser bounds
  the value at construction via `maxModelField` (256 bytes) and scrubs invalid UTF-8,
  so an oversized or non-UTF-8 value never reaches `ModelAnnouncedPayload`. This
  ticket adds a pure data type with no parsing, no validation and no constructor, so
  it introduces no new boundary. The design **explicitly forbids** re-deciding the cap
  here (§ Out of scope), which is the failure this category would otherwise produce:
  two caps in two places disagreeing silently. The spec requires the doc comment and
  the wire doc to state that the value stays untrusted, model-influenced text and that
  the **client's render boundary** owes the sanitization — the daemon strips no control
  characters and no terminal escape sequences on this path, and the spec says so in
  both places rather than leaving it implied.
- **[Tokens, secrets, credentials]** Not applicable, and the design decision that makes
  it so is stated rather than assumed: the payload carries exactly three fields, and
  the spec enumerates what is deliberately excluded from claude's 22-key init line —
  `cwd` (the operator's local filesystem path), `session_id` (claude's session
  identity, not the daemon's), `tools`, `mcp_servers`, `permissionMode`,
  `slash_commands`. `cwd` and `session_id` are not declared on the producer's decode
  target (`streamsup`'s `systemInitLine`) at all, which is a stronger guarantee than a
  test sweep. The spec additionally requires a marshal-and-assert regression pin on
  those keys so a future "helpful" addition goes red.
- **[File operations]** Not applicable. The only files created are two one-line JSON
  test goldens under `internal/protocol/testdata/`, at fixed paths with no
  caller-controlled component. No runtime file I/O is added.
- **[Subprocess / external command execution]** Not applicable in the acting
  direction, and this is the category worth stating positively rather than skipping.
  The value **originates** from a subprocess but is never passed back to one: the frame
  is declared outbound-only (`excludedTypes: "push"`, `v2OnlyTypes`, and rejected by
  `IsKnownAppType`), so a phone cannot send a `model_announced` frame into
  `dispatch.Route`. The spec makes the report-never-a-control-input constraint explicit
  in both the Go doc comment and the wire doc, carrying `turnevent.ModelAnnounced`'s
  posture onto the wire.
- **[Cryptographic primitives]** Not applicable. No randomness, no keys, no
  comparisons against secrets. The frame rides the existing Noise-protected v2
  envelope path unchanged.
- **[Network & I/O]** No MUST FIX. Size discipline is inherited and quantified rather
  than assumed: 256 bytes is 0.4% of the 65519-byte v2 application-envelope cap — the
  smallest contribution in the frame family, half `RateLimited`'s — and
  `maxModelField`'s own comment already carries that arithmetic plus the
  amplification analysis (`systemInitLine` holds one scalar and no array, so a 4 MiB
  input line yields at most 256 retained bytes plus one bool). **Rate:** `init` fires
  once per turn, below the gate `ThinkingProgress` already accepts, and the event is
  not in the droppable set (#610), so it holds a queue slot under existing
  backpressure. No new bound is owed and the spec forbids inventing one.
- **[Error messages, logs, telemetry]** No MUST FIX. This ticket adds no logging. The
  #833 log posture — the announced value reaches a daemon *event* and no daemon *log*
  at any level — is preserved because nothing in `internal/protocol` logs, and the spec
  requires the `interactive_stream_inband_model_test.go` correction to touch **only**
  the last clause, explicitly leaving that log-posture paragraph intact.
- **[Concurrency]** Not applicable, stated as a design decision: `internal/protocol` is
  a pure data package holding no runtime state. This ticket adds no goroutines, no
  locks and no shared mutable state, so there is no lock ordering, no check-then-mutate
  window and no goroutine lifecycle to account for.
- **[Threat model alignment]** No MUST FIX. The relevant threat in
  `docs/protocol-mobile.md` § Security model is model-influenced text reaching a client
  renderer. The design aligns with the file's existing house posture for claude-derived
  strings verbatim — bounded, not sanitized, a report and never a control input — and
  the spec requires the new section to carry that posture rather than invent a weaker
  one. One threat is named **out of scope**: nothing in this ticket puts the value in
  front of a renderer, because nothing emits the frame; the client-side render
  boundary's sanitization is the client's, and the frame first reaches a client in
  **#1617**.
- **[Correctness of the distinguishability claim]** SHOULD FIX, noted not gated. The
  `model` homograph is resolved by documentation cross-reference rather than by a
  distinct wire key (§ Decision). This is a **soft** control: a client author who reads
  none of the four documented sites can still merge the two values. Accepted because
  the failure is loud rather than silent (`""` against a concrete identifier), because
  the alternative reaches none of the client authors reading only an existing row, and
  because the declare-then-emit split keeps the shape cheap to change until #1617
  ships. Code-review should confirm all four doc sites landed — that is the whole
  control.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-08-19
