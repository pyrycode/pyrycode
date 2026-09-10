# #2253 — `protocol`: declare the `session_facts` v2 wire type, payload and doc section

Declare-ahead-of-producer work, the fifth time this repo has done it: `rate_limited`
(#1405 → #1410), `model_announced` (#1616 → #1638), `model_list` (#1704 → #1848/#1849),
`slash_command_list` (#1726 → #2003). Nothing maps or emits the frame here; that is
#2254. pyrycode-desktop#1241 needs a frozen shape to decode against and can be built
in parallel once it is.

## Files read

Production surface the design copies or must satisfy:

- `internal/protocol/codes.go` → `TypeModelAnnounced`, `TypeModelList`,
  `TypeSlashCommandList` — the three declare-ahead blocks whose doc-comment shape
  this one follows: grouping rationale, the daemon-owns-the-name paragraph with its
  discriminating-word analysis, the `inboundAppTypeSet` prohibition, and the
  declaring-vs-emitting ticket sentence.
- `internal/protocol/interactive.go` → `ModelAnnouncedPayload` — the payload this
  ticket otherwise copies, including its `SECURITY` paragraph and its delegation of
  field semantics to the `turnevent` variant. Also `RateLimitedPayload` and
  `BackgroundTaskProgressPayload` for the `TruncatedFields []string` form, and
  `UnrecognizedMessagePayload` for the one comment recording what `nil` means
  against `[]`.
- `internal/turnevent/event.go` → `SessionFacts` and its three fields — the single
  source of truth for the semantics this payload must not restate. Its `NO effort
  FIELD` paragraph is the measurement AC 4 has to carry onto the wire, and its
  `TruncatedFields` comment fixes the daemon-name rule for the entry strings.
- `internal/streamsup/parser.go` → `maxClaudeVersionField`, `maxPermissionModeField`
  — the producer's caps, so the doc section can say the bound is decided at
  construction and this struct re-decides no maximum.
- `cmd/pyry/interactive_turn_v2.go` → `eventKind` — already returns
  `"session_facts"` for the variant (#2252), which is where the wire spelling is
  already visible in the tree and why the name is frozen rather than chosen here.

Guards the new constant must satisfy:

- `cmd/pyry/relay_guard_test.go` → `excludedTypes`, `TestEveryInboundV2TypeHasHandler`
  — AST-walks every `Type*` constant out of `codes.go`, so an unclassified constant
  reddens the build. **This is the only detector that actually goes red**; see the
  package-overview lesson below.
- `internal/protocol/compat_test.go` → `v2OnlyTypes`, `TestTypeConstants_V1V2Partition`,
  `TestIsKnownAppType` — hand-maintained literals. The size equality
  `len(inboundAppTypeSet) + len(v2OnlyTypes) == len(all)` forces the `all` slice and
  the `v2OnlyTypes` map to move together.
- `internal/protocol/interactive_test.go` → `roundTripEnvelope`, `readFixture`,
  `TestModelAnnouncedPayload_RoundTrip`, `TestModelAnnouncedPayload_ZeroValue_RoundTrip`,
  `TestModelAnnouncedType_IsNotClaudesSubtype` — the three-test shape to mirror.

Documentation surface:

- `docs/protocol-mobile.md` → § Application message types (the row), § Interactive
  events (the group opener and its count), § `model_announced` (the section to copy
  and cross-link), § `session_transition` / § `model_list` / § `slash_command_list`
  (the three other counts), § Changelog.

Package overviews carrying lessons that change how this is built:

- `docs/knowledge/features/protocol-package-drift-detectors.md` — three lessons
  load-bearing here. **(a)** `TestTypeConstants_V1V2Partition` cannot redden on an
  unpartitioned constant: `all` and `v2OnlyTypes` are hand-written, so a constant
  left off both moves the size assertion nowhere. Only the relay guard's Assertion #3
  reddens. **(b)** No registry entry can catch a wire-*string* typo — all three key
  on the Go symbol — so the committed fixture round trip is the only thing pinning
  the literal `"session_facts"`. **(c)** The `env`-round-trip family does not always
  exercise payload struct tags; `roundTripEnvelope` does, because it marshals the
  bare payload before re-assembling the envelope, which is what makes the
  no-`omitempty` claim checkable.
- `docs/knowledge/features/protocol-package-constants-codes-go-envelope-types.md`
  § the naming-pin lesson — the discriminating word for a naming pin must not be a
  substring of the correct name. Directly binding here, see Design below.
- `docs/knowledge/features/turnevent-package.md` — adding a variant means checking
  every production type switch, and prose counts go stale silently. This ticket adds
  no variant and no switch arm, but it does add a prose count (nineteen), which is
  why AC 3 enumerates its sites rather than trusting a sweep.

## Context

`turnevent.SessionFacts` shipped in #2252 carrying claude's own build version and the
permission mode claude reports the child is running under. Both stop at the daemon
boundary today: `turnbridge.MapEvent` has no arm for the variant, so no client can
see either fact. This ticket gives the variant its wire form and its documentation
so a client author can decode it before #2254 makes it arrive.

No ADR is warranted. The declare-ahead-of-producer sequencing is already the
package's established pattern with four precedents, and the naming rule (the wire
follows the `turnevent` variant) is already stated in `codes.go`.

## Design

### The constant — `internal/protocol/codes.go`

`TypeSessionFacts = "session_facts"`, in its own `const` block after
`TypeSlashCommandList`, with the doc-comment structure the four sibling blocks share:

1. What the frame is and the gap it closes.
2. Why it groups alone. It is an identity report like `model_announced`, but about
   the **child run** rather than about the turn's model: what claude's build IS and
   what posture it says it is under. Not a capability inventory (`model_list`,
   `slash_command_list`), not a condition report (`rate_limited`).
3. The name is the daemon's, following `turnevent.SessionFacts`.
4. The `inboundAppTypeSet` prohibition, naming both drift detectors and stating that
   classification is mandatory from the moment the constant exists.
5. No inbound request verb is declared, and that is not an omission — the sibling
   blocks' paragraph, unchanged in substance.
6. Declaring ticket #2253, producer #2254.

**The naming pin's discriminating words invert the obvious choice, and this is the
one genuinely new piece of analysis in the ticket.** `session` is unusable as a
discriminator: it is a substring of the correct name `session_facts`, so a
`strings.Contains` check on it is red against the correct name — the trap
`TypeModelAnnounced`'s block records for `model` and `TypeSlashCommandList`'s records
three words wide for `command` / `slash_command` / `slash`. It is also claude's word,
via the `session_id` key on the same `system/init` line. The usable discriminators
are claude's subtype `init` and claude's two keys `claude_code_version` and
`permissionMode`, none of which is a substring of `session_facts`.

A second pin is owed that the siblings did not need: three shipped constants already
begin `session_` — `TypeSessionTransition`, `TypeSessionSettings`, `TypeSessionError`.
Distinguishing from them has to be **equality against the sibling constant**, not
containment on the shared word, which is exactly the shape `TypeQuestionDismissed`'s
pin needed against `TypeModalDismissed`.

### The payload — `internal/protocol/interactive.go`

```go
type SessionFactsPayload struct {
    ConversationID    string   `json:"conversation_id"`
    ClaudeCodeVersion string   `json:"claude_code_version"`
    PermissionMode    string   `json:"permission_mode"`
    TruncatedFields   []string `json:"truncated_fields"`
}
```

No `omitempty` on any field, per the stream's stated rule. No `MarshalJSON`: `nil`
`TruncatedFields` serialises as `null`, which is what the six sibling
`truncated_fields` payloads already do and what the document's existing rows
describe (`array or null`). The `[]`-normalising `MarshalJSON` that `ModelListPayload`
carries is for a list that is the frame's *subject*; a truncation report's absence is
genuinely an absence.

Doc comment, following `ModelAnnouncedPayload`'s shape:

- What it is the wire form of, and the declared-ahead-of-producer sentence carrying
  **the "nothing emits it yet" claim exactly once in the whole ticket** (Technical
  Notes; #1616 spread it across twelve places and #1639 had to repair all twelve).
- Conversation-scoped, no `turn_id`, opens and closes no turn; the bridge supplies
  `ConversationID` because the internal event carries none.
- Semantics of the two claude-derived values are **not restated** — delegated to
  `turnevent.SessionFacts`'s field comments, the way `ModelAnnouncedPayload`
  delegates to `ModelAnnounced.Model`.
- `TruncatedFields` is the siblings' `[]string` and not `ModelAnnounced`'s
  `Truncated bool`, because the report has to say **which** of two fields was cut;
  entries are the daemon's wire names (`claude_code_version`, `permission_mode`),
  never claude's `permissionMode`.
- The four omissions and their reason (`cwd`, `session_id`, `memory_paths`,
  `messaging_socket_path`), the measured absence of `effort`, and MCP server status
  belonging to #2275's frame.
- `SECURITY`, adapted from `ModelAnnouncedPayload`'s: both strings crossed the
  subprocess trust boundary, are bounded but not sanitized, and `PermissionMode` is
  additionally **a claim and not a guarantee** — a client must not read it as an
  authorization decision.

### Guard registrations

Three entries, all mandatory from the moment the constant exists:

- `cmd/pyry/relay_guard_test.go` → `excludedTypes["TypeSessionFacts"] = "push"`.
- `internal/protocol/compat_test.go` → `v2OnlyTypes[TypeSessionFacts] = true`, the
  `all` slice entry, and a `TestIsKnownAppType` table row asserting rejection
  (`ErrUnknownType`) so the type stays off the inbound path.

### `docs/protocol-mobile.md`

- A `session_facts` row in the application-message-type table, beside
  `model_announced`'s.
- A `#### session_facts` section placed after `#### model_announced`, before
  `#### session_transition`: field table, the binary → phone / `interactive`-gated /
  `event_id` sentence, the single `**Declared by #2253; nothing emits it yet.**`
  attribution, what it does not carry and why, and the security paragraph. **That
  paragraph MUST carry the claim-not-guarantee statement for `permission_mode` in
  the doc, not only in the Go comment** — see the security review's trust-boundary
  finding. A client author building against this frame reads
  `docs/protocol-mobile.md`, never `interactive.go`, so a warning that lives only in
  the struct comment does not reach the reader who can act on it.
- Cross-links both ways: the new section points at `model_announced` for the
  per-turn model, and `model_announced`'s section gains one sentence pointing here.
- Four `eighteen` → `nineteen` edits: the group opener, and the sentences in
  `session_transition`, `model_list` and `slash_command_list`.
- **`unrecognized_message`'s "count of five `system` subtypes" is deliberately not
  touched** — already marked stale on purpose by #2246, and the ticket says so.
- A Changelog entry.

## Concurrency model

None. This ticket adds a constant, a struct, test registrations and prose. No
goroutines, no shared state, no locks.

## Error handling

No new failure modes. The payload has no constructor, no validation and no error
path; decoding is `encoding/json`'s. The one behavioural assertion is negative:
`IsKnownAppType` must **reject** `session_facts`, which is what keeps a phone from
pushing the frame into `dispatch.Route`.

## Testing strategy

In `internal/protocol/interactive_test.go`, mirroring the `model_announced` trio:

- `TestSessionFactsPayload_RoundTrip` — reads `testdata/session_facts.json`, asserts
  the envelope type and all four fields, then `roundTripEnvelope`. Values are
  **measured**: `2.1.259` for the version (#2251's effort capture, all three init
  lines) and `default` for the posture (same capture); `bypassPermissions` is the
  other measured value, from the 2.1.220 capture. The two field values differ from
  each other, so a struct wiring them to the wrong keys reddens. `truncated_fields`
  carries one entry naming the **second** field, which discriminates a list that is
  a bool in disguise and pins the daemon-name rule against claude's `permissionMode`.
  The pairing of a 7-byte version with a truncation report is not itself a capture,
  and the comment will say so, following `model_announced.json`'s precedent.
- `TestSessionFactsPayload_ZeroValue_RoundTrip` — reads
  `testdata/session_facts_zero.json`; `bytes.Contains` guards on each key's explicit
  zero (`""`, `""`, `null`) before the round trip, because without them an
  `omitempty` would elide the key from both sides and the round trip alone would stay
  green. This is the AC's "including at its zero value".
- `TestSessionFactsType_IsNotClaudesVocabulary` — the exact-value pin plus the
  negative pins derived above: not `init`, contains neither `init` nor
  `claude_code_version` nor `permissionMode`, and `!= TypeSessionTransition`. Plus a
  payload-bytes regression sweep for the excluded init-line keys (`cwd`,
  `session_id`, `memory_paths`, `messaging_socket_path`, `mcp_servers`, `effort`),
  which is where the "what it does not carry" AC becomes machine-checked rather than
  prose-only.

Two fixtures: `internal/protocol/testdata/session_facts.json` and
`session_facts_zero.json`.

Verification per § B2: `go test -race ./internal/protocol/... ./cmd/pyry/...`,
`go vet ./...`, `go build ./cmd/pyry`. Budget permitting, one `go test -overlay`
mutant against the round trip to confirm the fixture is non-vacuous — the drift-detector
overview records that only a fixture catches a wire-string typo, and overlay mutation
works for this package (it does **not** work for the relay guard's AST family).

## Open questions

1. **Does `session_facts` count among the turn-stream events?** Resolved during
   planning: yes. The eighteen are exactly the `####` frames under § Interactive
   events, and every one is a `turnevent` variant reaching the wire. `session_facts`
   is a `turnevent` variant on the same lane, so the count moves to nineteen — unlike
   `model_list` and `slash_command_list`, which say in their own sections that they
   are *not* `turnevent` variants and are therefore excluded from the count they cite.

2. **Fixture `truncated_fields` value — one entry or two?** Settled at one, naming
   `permission_mode`. Two entries would prove the list holds more than one element
   but would make the zero-vs-nonzero contrast the only discriminator for *which*
   field is named. One entry naming the second field is the stronger pin. If the
   round-trip mutant run shows this is vacuous, the choice moves and lands in
   `## Revisions`.

3. **Does the `model_announced` section's new cross-link disturb its own count
   sentences?** It carries none of the four `eighteen` sentences, so no.

## File-overlap check (§ A2)

`git fetch origin --prune` then a sweep of every `origin/feature/<n>` branch against
the six files above found one hit: `origin/feature/449` touches
`internal/protocol/codes.go`. **Not a blocker, and no `blockedBy` was set.** Issue
#449 closed 2026-05-17, it has no open PR, and its `codes.go` change
(`TypeRekeyRequest`) is already in `main` — the branch is a stale leftover that will
never be merged, so it cannot conflict at integration time. The check exists for
in-flight work; this is not that.

## Sizing (§ A1 / § A4)

| Limit | Boundary | This ticket |
|---|---|---|
| Production source files | ≤ 5 | 2 (`codes.go`, `interactive.go`) |
| Total written work | ≤ 800 | ~560 (60 + 70 production, ~150 tests, ~25 guards, 2 fixtures, ~90 docs, this plan) |
| New exported types | ≤ 5 | 1 (`SessionFactsPayload`) plus 1 constant |
| Consumer call sites | ≤ 10 | 0 — nothing consumes the type; the 3 guard entries are additive |
| Acceptance criteria | ≤ 5 | 5 |
| Reject branches | ≤ 10 | 0 |

Within boundary on every line. Nearest analogue #1616 landed 366 insertions across 9
files for the same work on a single-string payload; the extra here is one more field,
a `[]string` in place of a bool, and four prose counts.

## Security review

**Verdict:** PASS (second pass; the first returned FAIL on one MUST FIX, revised
inline before this section was written).

**Findings:**

- **[Trust boundaries] MUST FIX — resolved inline before the plan was committed.**
  `permission_mode` is a security-posture **claim**, and it is the one field in this
  family a client is most likely to branch on: a UI suppressing a warning banner
  because the posture reads `default`, or a client rendering "safe mode". A buggy or
  compromised claude can report `default` while running under any posture at all;
  what the field proves is what claude *said*. `turnevent.SessionFacts.PermissionMode`
  states this, but the first draft of this plan required the statement only in the
  Go struct comment and left the doc section's security paragraph unspecified. The
  reader who can act on it — pyrycode-desktop#1241's author — reads
  `docs/protocol-mobile.md` and never opens `interactive.go`, so the warning would
  not have reached them. The Design section now requires it in the doc section
  explicitly.
- **[Trust boundaries] No further findings.** The untrusted → trusted crossing is
  `internal/streamsup`'s parser, a single explicit decode point that this ticket does
  not touch and does not duplicate. The Go type gives downstream callers no signal
  that these two strings are model-influenced — `string` is `string` — which is the
  family's existing property (`ModelAnnouncedPayload.Model` shares it). The doc
  comment therefore *is* the mitigation rather than decoration, and removing it
  would be a security regression rather than a style change.
- **[Trust boundaries] The concrete exploit the guard entries prevent is inbound
  injection**, and it is worth naming rather than treating the registrations as
  bookkeeping. A `session_facts` constant leaking into `inboundAppTypeSet` would let
  a phone push a `session_facts` frame into `dispatch.Route`. Two independent
  mitigations land here: `cmd/pyry/relay_guard_test.go`'s Assertion #3 reddens on an
  unclassified constant (the only detector that actually goes red on a *new* one —
  the `compat_test.go` trio is hand-maintained and cannot), plus an explicit
  `TestIsKnownAppType` row asserting `ErrUnknownType`.
- **[Tokens, secrets, credentials] No findings — nothing credential-bearing is
  carried, and the omissions are the guarantee.** The fields that would leak are the
  ones deliberately absent: `cwd`, `memory_paths` and `messaging_socket_path` are the
  operator's local filesystem, and `session_id` is claude's own session identity.
  None is declared on the producer's decode target (`streamsup`'s `systemInitLine`),
  which is stronger than a reflection sweep — a field never decoded cannot leak
  whatever a later check forgets to look at. The Testing strategy pins this as a
  payload-bytes sweep rather than leaving it as prose.
- **[File operations] Not applicable by design.** No production code path in this
  ticket constructs, opens, reads or writes a filesystem path. The two `testdata`
  fixtures are read by tests through the existing `readFixture` helper against a
  constant relative path with no caller-supplied component.
- **[Subprocess / external command execution] No findings, and one invariant #2254
  inherits.** This ticket executes nothing. The values *originate* from a subprocess,
  and the standing rule is that `PermissionMode` is never fed back into any spawn or
  control request — in particular it must not become an input to `streamsup`'s
  `permissionModeAllowed`, which bounds what the **daemon may ask for** and is a
  deliberately different rule from reporting what claude says it got. Nothing
  consumes the payload in this ticket, so the invariant cannot be broken here; it is
  named so the producer ticket does not break it.
- **[Cryptographic primitives] Not applicable by design.** No randomness, no key
  material, no comparison of an attacker-controlled value against a secret. Nothing
  in the design calls for any of the three.
- **[Network & I/O] No findings for this ticket; one bound deferred.** The two
  claude-derived strings are capped at 256 bytes each by the producer
  (`maxClaudeVersionField`, `maxPermissionModeField`) **at construction**, and
  `truncated_fields` holds at most two daemon-authored names, so a worst-case frame
  is ~549 bytes of payload text against the v2 application-envelope cap of 65519 —
  the arithmetic `maxPermissionModeField`'s own comment already carries. This struct
  deliberately re-decides no maximum: a second cap here would be a second place the
  limit is decided and the two could disagree silently. No rate bound is owed —
  `system/init` fires once per turn and yields at most one of these. **OUT OF SCOPE:**
  that the mapping actually routes through the capped construction path rather than
  filling the struct from a raw line is #2254's to hold, since #2254 owns the only
  producer.
- **[Error messages, logs, telemetry] No findings.** The ticket adds no logging
  statement, no error string and no error path — the payload has no constructor and
  no validation. Both values are bounded before they could reach a log at all, which
  `maxClaudeVersionField` states as its own property.
- **[Concurrency] Not applicable by design.** No goroutine, no lock, no shared
  mutable state. A constant, a struct, test registrations and prose.
- **[Threat model alignment] Threat #1, prompt injection (`severity: high`,
  `mitigation: partial`), is the applicable one** and the frame widens the
  model-influenced text reaching a client by two strings. The posture is the family's
  unchanged: bounded but **not sanitized** — nothing on this path strips control
  characters or terminal escape sequences — safe to render as inert text, never to be
  fed to an HTML sink, an attribute or a URL, with the render boundary owing the
  sanitization being the **client's**. Threats #2 through #8 concern transport,
  pairing, key custody and relay identity; a payload declaration touches none of them.
- **[Fixture as a client-facing artifact] No findings after a deliberate choice.** A
  committed fixture is copied by client authors as if it were observed traffic, so a
  fabricated version string could seed a client-side comparison against a value
  claude never emits. Both fixture values are measured from committed captures
  (`2.1.259` and `default`, from #2251's effort capture); the one unmeasured element
  is the pairing of a short version with a truncation report, which the test comment
  will label as chosen-to-discriminate rather than captured, following
  `model_announced.json`'s precedent.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-10

## Revisions

### 2026-09-10 — implementation

The design landed as planned; nothing in the shape, the guard registrations or the
documentation plan changed. Three things are worth recording.

**The count is nineteen, confirmed by enumeration rather than by trusting the
prose.** Open question 1 resolved as expected: the `####` frames under § Interactive
events run `turn_state` through `session_facts` and number nineteen exactly, with
`session_transition`, `model_list` and `slash_command_list` sitting outside the count
they cite. All four sentences moved.

**Three overlay mutants were run, and one falsified a comment I had written.**

| Mutant | Result |
|---|---|
| Swap the `claude_code_version` / `permission_mode` struct tags | Both round trips red — confirms the fixture's two values differ *usefully*, not decoratively |
| `TypeSessionFacts` value → `"session_fact"` | Round trip red — confirms the committed fixture is the thing pinning the wire literal, per the drift-detector overview's lesson that no registry can |
| `omitempty` on `TruncatedFields` | Zero-value round trip red |

The third one corrected me. The zero-value test's comment had borrowed the sibling's
sentence claiming the round trip alone would "go on passing" without the
`bytes.Contains` guards. Against the **committed** fixture that is false — the key
vanishes from the re-marshalled bytes while the fixture still carries it, so the byte
comparison reddens on its own. The guards' real value is surviving a fixture
**regeneration**, which is the property the drift-detector overview records for
key-set pins. The comment now says what the mutant showed rather than what the
sibling's comment says.

**Open question 2 is settled, with a caveat worth naming.** The single
`truncated_fields` entry naming `permission_mode` stands. It is not detectable by a
mutant at this layer — there is no producer to mis-name the entry, so the assertion
can only pin the fixture against itself — which means it is carrying its weight as a
**client-facing artifact** rather than as a guard: it is what a client author copies
when learning that entries use the daemon's `permission_mode` and never claude's
`permissionMode`. The machine-checked half of that rule arrives with #2254's producer.
