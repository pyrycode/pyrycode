# #1678 — send the tool input as capped fields on `tool_use`

## Files to read first

| Path | Symbol | What to extract |
|---|---|---|
| `internal/turnbridge/outbound.go` | `MapEvent` (the `turnevent.ToolStart` arm) | The one production edit site. Note the per-arm comment style — every arm explains what crosses verbatim and why. |
| `internal/turnbridge/outbound.go` | `inputSummary`, `truncate`, `maxSummaryLen` | `inputSummary` is the sibling the new helper sits beside and must not disturb; `truncate` is the rune-safe cut AC 2 mandates; `maxSummaryLen`'s doc comment is the "phone-display bound, not a wire constraint" framing the new constants deliberately invert. |
| `internal/turnbridge/outbound.go` | the package doc comment on `package turnbridge` | "pure value-to-value adapter — no I/O". This is the hard constraint behind the no-logging rule below. |
| `internal/protocol/interactive.go` | `ToolUsePayload` | The struct that gains one field. Also read the file-level comment above `TurnStatePayload` — the no-`omitempty` rule for this whole stream. |
| `internal/protocol/interactive.go` | `BackgroundTaskRosterPayload.MarshalJSON` | The exact precedent for the nil→empty normalisation AC 4 needs, including why a doc comment would not have been enough. Copy its shape (value receiver, `type alias`). |
| `internal/protocol/interactive_test.go` | `TestBackgroundTaskPayloads_FitV2EnvelopeCap` | The shape AC 3 names: `'<'` fill, mirrored producer caps as test-local constants, worst-case envelope, `t.Logf` of the percentage. |
| `internal/protocol/interactive_test.go` | `TestToolUsePayload_RoundTrip`, `roundTripEnvelope`, `readFixture` | The golden round-trip that must be extended, and the byte-comparison harness that forces `testdata/tool_use.json` to gain the new key. |
| `internal/protocol/interactive_test.go` | `TestBackgroundTaskRosterPayload_NilTasksNormalises` | The direct-marshal polarity test AC 4's protocol-side rung mirrors. |
| `internal/turnbridge/outbound_test.go` | `TestMapEventOutbound`, `TestInputSummary` | The first has one `ToolStart` row to update and compares with `reflect.DeepEqual` (so a map field is safe — do not "fix" it to `==`). The second must pass **unmodified** (AC 1). |
| `internal/turnbridge/outbound_test.go` | `TestMapEventBackgroundTaskRosterEmptyTasksOnTheWire` | The bridge-side bytes assertion AC 4's turnbridge rung mirrors, including why it asserts on what `MapEvent` **returned** rather than on a test-built payload. |
| `cmd/pyry/interactive_turn_v2.go` | `maxDeltaTextBytes` | The house form for a wire-cap constant: escaped arithmetic in the doc comment, "if that ever fails, LOWER this constant — never raise it", belt-and-suspenders framing. The new constants inherit all three. |
| `internal/streamsup/parser.go` | the `"tool_use"` case inside `emitAssistant`'s block loop, and `rawInput` | Proof of what the producer does and does not bound: `Title` and `ToolCallID` cross verbatim with **no cap**, and `rawInput` only nils an empty blob. This is why the cap test fills those fields hostilely and why the § Open questions entry exists. |
| `internal/protocol/testdata/tool_use.json` | — | One-line fixture; gains `"input":{…}`. |
| `docs/protocol-mobile.md` | § `tool_use` (and § Application-envelope size cap) | The table AC 5 extends, and the 65519-byte number every piece of arithmetic below refers to. |
| `docs/knowledge/features/turnbridge-package.md` | § "Summary derivation" and § "What the outbound adapter does NOT do (the seam)" | Where the existing helpers are described and where the seam is drawn. Read-only — the documentation phase owns this file. |
| `docs/knowledge/features/protocol-package.md` | the "Per-field caps don't compose into an envelope guarantee" bullet | The measured percentages of the existing cap tests, for calibrating the new one. Read-only. |

## Context

A tool row in a client shows the tool name plus `input_summary` — the whole tool input compacted to one line and cut at 200 runes. For an `Edit` that is mostly replaced text with the file path buried inside it, so the one thing an operator wants to see is the one thing that gets cut. The fix is to send the input's own fields, each capped on its own, and let the client pick what to show collapsed and what to list expanded.

`input_summary` is untouched: same meaning, same value, same cap. Mobile parses it today; deprecating it is a later decision.

Two things were considered and rejected upstream — do not re-propose them: a `kind` discriminant (the client can run the identical switch on `name`, which is already on the payload) and a pre-chosen `subject` field (that would make the daemon own a display decision the client is better placed to make).

**No ADR.** This is one additive field on an existing payload, following two conventions this repo has already decided (the `…` truncation marker, and the measured-envelope-cap pattern). It establishes nothing new to record.

## Design

### The wire field

`ToolUsePayload` gains one field:

```go
Input map[string]string `json:"input"`
```

No `omitempty` — this stream's file-level rule is that every field is always present so the fixtures pin the full shape.

`map[string]string`, not a slice of pairs: `encoding/json` sorts map keys on marshal, so the bytes are deterministic without the developer writing a sort. The input's *original* field order is lost, which is fine — ordering a tool row is a display decision and the client owns it (the same argument that rejected `subject`).

Adding a map makes `ToolUsePayload` non-comparable with `==`. Every existing comparison already goes through `reflect.DeepEqual` or byte equality, so nothing breaks; the risk is a future `==` compiling against `any` and panicking at runtime. Worth one sentence in the type's doc comment.

### Empty polarity: `{}`, decided in `protocol`, not stumbled into

AC 4 collapses three distinct inputs — absent, empty, not-an-object — into the same answer: no fields. Since the payload deliberately does not distinguish them, there is nothing for `null` to mean that `{}` does not, and `{}` is the better client contract (iterable without a branch, in every client language). This is the roster's argument, restated for a map.

The normalisation lives on `ToolUsePayload`:

```go
func (p ToolUsePayload) MarshalJSON() ([]byte, error)  // nil Input -> empty map, then marshal via a type alias
```

Copy `BackgroundTaskRosterPayload.MarshalJSON` exactly — value receiver, `type alias` indirection to stop the recursion. Its doc comment explains why a doc comment alone would not have been enough; the same reasoning applies here and the new comment should say so rather than restate it.

The bridge correspondingly returns a **nil** map for the no-fields cases and does **not** pre-allocate an empty one. Pre-allocating would produce identical bytes while hiding which layer owns the decision — the roster arm's stated reason, and the trap a prior ticket hit when a normaliser silently satisfied an assertion the producer was supposed to satisfy.

### The extraction

One new unexported helper in `internal/turnbridge/outbound.go`, beside `inputSummary`:

```go
// inputFields extracts the tool input's top-level object as a bounded
// name-to-string map; nil when there is nothing to send.
func inputFields(raw json.RawMessage) map[string]string
```

Behaviour, in order:

1. `len(raw) == 0` → nil.
2. Unmarshal into `map[string]json.RawMessage`. An error → nil. This is what makes "not a JSON object" free: a JSON array, number, string or bool all fail to unmarshal into a map, and so does a malformed blob. A literal `null` succeeds and leaves the map nil.
3. Empty map → nil.
4. Per entry, derive the value: a JSON string value becomes its **decoded** string (so `"a\nb"` arrives as two lines, and a path is a path — not a re-quoted JSON literal); any other JSON type becomes its `json.Compact` form. No path rewriting, no workspace-relative form, no other normalisation (AC 1).
5. Drop any entry whose key exceeds `maxInputKeyRunes`. Keys are **never truncated** — a cut key is a false claim about the input's field name, where a cut value is honestly marked with `…`.
6. Cap each value with `truncate(v, maxInputValueRunes)` (AC 2).
7. Admit entries **shortest value first**, tie-broken by key ascending for determinism, spending from `maxInputTotalRunes`. An entry costs `runeLen(key) + runeLen(value)`. An entry that does not fit whole is dropped and the walk stops. Stop also at `maxInputFields` entries.
8. Nothing admitted → nil.

**Why shortest-first.** It is the policy AC 3 leaves to this spec, and it is what actually fixes the reported bug. For `Write{content, file_path}` and `Edit{file_path, new_string, old_string}`, the short identifying field is admitted before the bulk text can spend the budget. Sorted-key order would put `content` before `file_path` and reproduce the original complaint.

**Why drop rather than shorten when the budget binds.** It buys the invariant *every value on the wire is either the input's value verbatim, or that value cut at exactly `maxInputValueRunes`* — one reject branch fewer and a cleaner client contract. It costs nothing measurable: the peak observed `Edit` (27592 characters) reduces to roughly `file_path` + two 4000-rune values ≈ 8090 runes, inside the 8500 budget, so the total bound does not bind on the measured corpus at all. That margin is the reason `maxInputTotalRunes` is not lower. Do not "improve" this into a shorten-to-fit path.

A dropped field is simply absent. Per the ticket's explicit instruction, this payload gets **no** `truncated_fields` list — the `…` marker is this wire's value-level convention, and `input_summary` remains as the whole-input fallback.

### Constants and the arithmetic

Three new constants in `internal/turnbridge/outbound.go`, separate from `maxSummaryLen` (AC 2), each carrying its arithmetic in its doc comment in the form `maxDeltaTextBytes` uses:

| Constant | Value | Basis |
|---|---|---|
| `maxInputValueRunes` | 4000 | The measurement in #1678: 97% of 11336 calls arrive completely intact; 8000 buys one point and doubles the worst case. |
| `maxInputKeyRunes` | 128 | Real input keys are `file_path`, `command`, `pattern`. Generous; exists only to stop one pathological key eating the whole budget. |
| `maxInputFields` | 16 | Observed maximum is 6. Necessary and not redundant with the rune budget: per-entry JSON structure (`"":"",`) is bytes a *content* budget does not see, so without this a map of many tiny entries could out-cost its own content. |
| `maxInputTotalRunes` | 8500 | Keys and values summed. The worst call in the whole corpus is 8147 characters, which the per-value cap reduces to roughly 8090 before this bound is consulted, so it does not bind on measured traffic. Also a memory knob, not only a wire knob — see § Security review, Network & I/O. |

**The escape arithmetic.** `encoding/json` has `SetEscapeHTML` on by default, so `<`, `>`, `&` and every control byte without a short escape cost six bytes each. The existing constants note that a *byte* cut is what makes six-bytes-per-input-byte the ceiling; here the cut is a **rune** cut, and six bytes per rune is still the ceiling — a 1-byte rune escapes to at most 6, a multi-byte rune emits raw at 4 or fewer, `U+2028`/`U+2029` escape to 6 from 3 input bytes, and an invalid byte becomes `�` (6 bytes) while `utf8.RuneCountInString` counts it as one rune. So `8500 × 6 = 51000` bytes is the map's content ceiling.

The bound is on pre-ellipsis content. Each admitted value may add one `…` beyond its cap, exactly as `inputSummary` does today (`TestInputSummary` pins `maxSummaryLen - 6` plus `…`), so at most `maxInputFields` extra runes — 96 bytes. Say so in the comment rather than pretending the bound is exact.

**Measured worst case: 56570 bytes, 86.3% of the 65519-byte cap**, filling every value with `<`, `conversation_id` / `turn_id` / `tool_use_id` at 64 runes, `name` at **512** runes, and `input_summary` full. That is inside the 91.9% the house already ships for `maxDeltaTextBytes`, with roughly 8.9 KB of headroom. Inherit the rule verbatim: **if the cap test ever fails, LOWER these constants — never raise them.**

The 512-rune `name` is deliberate and is what forced 8500 over a roomier 9000 (which measures 90.9% under the same fill). `name` is unbounded upstream, so budgeting for a fat MCP tool name is the difference between a measured guarantee and a tacit assumption — see § Security review, Network & I/O.

`tool_use` and `tool_result` are separate envelopes. #1680's result cap does not ride this frame and the two budgets do not sum.

### Nothing is logged

`internal/turnbridge` has no logger and its package doc commits to "no I/O". Tool inputs are user content. Do not add a logger, a `slog` call, a `fmt.Printf`, or an error return that would tempt a caller to log the blob. Every rejection path above returns nil silently — that is the design, not an oversight.

### What `docs/protocol-mobile.md` § `tool_use` must say (AC 5)

One table row for `input` plus a short paragraph under the table covering: the per-value cap and that it is counted in **runes**; the total bound across the map; the `…` marker and its known ambiguity with a value that legitimately ends in `…` (the operator accepted this); that absent, empty, and non-object inputs all yield `{}` and never `null`; that a field may be absent because the total bound dropped it, with `input_summary` remaining as the whole-input fallback; and that key order on the wire is alphabetical (a marshalling artefact), so display order is the client's choice.

One more sentence, which is a security note rather than a shape note: **values are display strings, not capabilities.** A `file_path` in this map is model-authored text that the daemon neither resolved nor validated, and a `command` value is a literal shell command line. A client may render them; it must not treat them as a path to open on its own filesystem or a command to run. See § Security review, File operations.

## Concurrency model

None. `MapEvent` is a pure function on immutable inputs; `inputFields` allocates its own map and returns it. No goroutine, no shared state, no lock, no context. The existing `t.Parallel()` on every test in both packages stays valid.

## Error handling

`inputFields` returns no error. Every failure mode is "this tool_use carries no fields", which is `inputSummary`'s existing posture for the identical reasons: `RawInput` is best-effort and opaque (#606), so a malformed blob is a field-less `tool_use`, not an error. The reject branches are: empty raw, non-object or malformed JSON, empty object, over-long key, budget or field-count exhausted.

## Testing strategy

Scenarios, not test code. Table-driven, stdlib only, `t.Parallel()`.

**`internal/turnbridge/outbound_test.go`**

- `TestInputFields` — one table, deliberately kept to these eleven rows; do not pad it. Four yielding nil (AC 4's three named cases plus a malformed blob): nil `RawMessage`; `{}`; a JSON array; `{not json`. Then: a plain two-field object; a string value with an embedded newline and quote arriving **decoded**, not re-quoted; one row covering non-string values together (number, bool, array, nested object with interior whitespace) arriving as compact JSON; a value over `maxInputValueRunes` cut at exactly that many runes with a trailing `…`; a multibyte value cut on a rune boundary; a key over `maxInputKeyRunes` dropping its entry while its siblings survive; a `Write`-shaped input whose bulk value would exhaust the budget, asserting `file_path` is present and intact — the regression this ticket exists for, and the row that proves shortest-first.

  Deliberately **not** a row: an empty `json.RawMessage{}` (the nil row already covers the `len(raw) == 0` branch) and a bare JSON number (the array row already covers "unmarshals into a map fails"). More than `maxInputFields` entries is likewise skipped here — a 17-entry literal costs more to read than it proves, and the field cap is already exercised by the envelope test, which fills to exactly that many.
- `TestMapEventToolUseEmptyInputOnTheWire` — mirroring `TestMapEventBackgroundTaskRosterEmptyTasksOnTheWire`: for each of absent / empty-object / non-object `RawInput`, `json.Marshal` the payload **`MapEvent` returned** and assert the bytes contain `"input":{}` and never `"input":null`. Asserting on the returned value rather than a test-built payload is the point — a test-built one would only prove `protocol`'s `MarshalJSON` works, not that the bridge reached it with the nil intact.
- `TestMapEventOutbound` — extend the existing `ToolStart` row's expected payload with the populated `Input` map. One row, no new rows; `reflect.DeepEqual` already handles the map.
- `TestInputSummary` — **must not be modified** (AC 1). If it needs a change, the extraction has leaked into the summary path and the change is wrong.

**`internal/protocol/interactive_test.go`**

- `TestToolUsePayload_FitV2EnvelopeCap` (AC 3) — the `TestBackgroundTaskPayloads_FitV2EnvelopeCap` shape. Mirror the four turnbridge caps as test-local constants, each commenting its source (`// internal/turnbridge.maxInputValueRunes` and so on) with the same warning the existing block carries: raising a producer cap without updating these leaves the measurement silently stale. Fill with `'<'`, not `'a'` — an ASCII fill under-reports by over 5× and would prove nothing. Fill the map to `maxInputTotalRunes` of content across `maxInputFields` entries, and fill the unbounded identity fields hostilely too (`conversation_id` / `turn_id` / `tool_use_id` at 64, `name` at **512** — see § Security review, Network & I/O, for why `name` gets four times the paranoia of the others). Wrap in a worst-case `Envelope` (max-uint64 `id`, populated `event_id`), `t.Logf` the byte count and percentage as the precedent does, and assert `< 65519` against a test-local literal commented to `docs/protocol-mobile.md` § Application-envelope size cap.
- `TestToolUsePayload_NilInputNormalises` (AC 4, protocol rung) — direct marshal, no fixture, mirroring `TestBackgroundTaskRosterPayload_NilTasksNormalises`: a nil `Input` marshals to `"input":{}`.
- `TestToolUsePayload_RoundTrip` — extend with an `Input` assertion; `roundTripEnvelope` compares bytes, so `testdata/tool_use.json` must gain the matching `"input":{…}` key. Give the fixture a shape that earns its keep — a `WebSearch` input whose one field is the query, matching the existing `input_summary` value.

**Gate.** `make check` is sufficient; nothing here touches the live-claude suite. Note that `internal/e2e/realclaude`'s background-idle probe decodes `tool_use` into its own local struct carrying only `input_summary`, so an additive field cannot disturb it — but it is behind the `e2e_realclaude` tag and `make check` never compiles it, so do not read a green `make check` as evidence about that package.

## Open questions

- **`name` and `tool_use_id` are unbounded upstream.** The `"tool_use"` block case in `emitAssistant` passes claude's `Name` and `ID` through verbatim with no cap, and `rawInput` only nils an empty blob. So the envelope guarantee AC 3 establishes is conditional on those two fields being sane, exactly as the existing background-task cap test is conditional on `conversation_id` (it picks 64). The cap test should fill them at the stated bounds above and say in its doc comment that these are assumptions, not enforced caps. Capping them upstream is a separate ticket and explicitly **not** in scope here — do not add a cap to `internal/streamsup` under this ticket.
- **`maxInputTotalRunes` is a first calibration.** 8500 clears the whole measured corpus with ~8.9 KB of envelope headroom under a hostile 512-rune tool name. If a future payload field grows (a `tool_result`-style addition to `tool_use`, say), the cap test is what catches it, and the constant comes **down**.
- **The event ring's per-event size assumption changes underneath it.** `eventring.MaxEventsPerConversation` is 1024 and `tool_use` is a retained control-class event, so this ticket multiplies the ring's worst-case per-conversation footprint. The ring's own package doc already flags its bound as "a tunable starting point, not load-tested". Quantified in § Security review; the calibration is a follow-up, not this ticket.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries] No findings, but the exposure widens and the spec should say so.** There is exactly one boundary and it is a single pure function: `inputFields` is where claude's opaque `RawInput` becomes structured wire data. Nothing downstream re-parses it — `MapEvent` hands the map to the consumer, which marshals it. No new *class* of data crosses: `inputSummary` already ships the first 200 runes of this same blob to this same audience over this same channel. What changes is volume — up to ~8500 runes where 200 shipped before. The audience is unchanged and is not "the network": these frames go only to conns that negotiated the `interactive` capability, over a Noise_IK channel to a device the operator paired with `pyry pair`, and the relay is a blind router that sees ciphertext only (`docs/protocol-mobile.md` § Application-envelope size cap describes the envelope as the *decrypted* one). The recipient already receives `assistant_delta` frames carrying up to 10000 bytes of arbitrary assistant text, so a 40× widening of tool-input volume to the same paired device does not grant a capability that channel lacked.

- **[Tokens, secrets, credentials] OUT OF SCOPE, named deliberately.** Tool inputs can contain secrets: `Bash{command}` may carry an inline credential, `Write{content}` may be a `.env` file, `Edit{new_string}` may be a private key. This ticket increases how much of such a value reaches a paired client from 200 runes to 4000. No redaction is specified, and that is a decision, not an omission — AC 1 forbids normalisation, a redaction heuristic over model-authored text is stochastic and would diverge from `input_summary` (which ships the same bytes unredacted today), and the operator is the sole recipient and already sees this content in their own terminal. A general secret-redaction policy for this wire is a product decision spanning `input_summary`, `result_summary` and `assistant_delta`; it does not belong to one additive field. Not filed as a ticket here — flagging it to the operator is the right next step, since they own the call.

- **[File operations] SHOULD FIX — addressed in the spec, doc-only.** `inputFields` performs no file I/O and resolves no path; AC 1's "no path rewritten" means the daemon deliberately does not canonicalise, which is correct precisely because it never opens these values. The risk is at the *client*: a model could emit `{"file_path": "../../etc/passwd"}` and a client rendering it as an actionable link would act on daemon-relayed, model-authored text. Mitigated by the "values are display strings, not capabilities" sentence now required in § What `docs/protocol-mobile.md` § `tool_use` must say. No production code change; the client tickets (pyrycode-desktop#642/#643/#645) are where it binds.

- **[Subprocess / external command execution] No findings.** Nothing in this design execs. Values may contain shell metacharacters — `Bash{command}` *is* a shell command line — but they are never passed to `exec.Command` or `sh -c` by the daemon. The same display-strings-not-capabilities note covers the client side.

- **[Cryptographic primitives] Not applicable, with a reason.** No randomness, no key material, no nonce, and no comparison against a secret. `truncate` is not constant-time and does not need to be: it operates on model-authored display text, never on a credential the daemon holds. The frames themselves inherit the existing Noise_IK seal on the consumer's push path — this ticket adds no crypto and changes none.

- **[Network & I/O] SHOULD FIX ×2 — one addressed here, one deferred with arithmetic.**
  1. *Envelope overflow.* This is the category the whole total-bound design serves, and the reason a per-value cap alone was rejected. Addressed: `maxInputFields` exists specifically because per-entry JSON structure is bytes a content-rune budget cannot see, keys count against the budget and are dropped rather than truncated, and `TestToolUsePayload_FitV2EnvelopeCap` measures the result rather than arguing it. The adversarial finding that changed the design: `name` is **unbounded** in `emitAssistant`, so filling it at the precedent's timid 64–128 would have made the guarantee tacit. The cap test now fills it at 512 hostile runes, and that is what pushed `maxInputTotalRunes` down from 9000 (90.9%) to 8500 (86.3%). Residual risk is a >512-rune `<`-dense tool name, which requires an operator-installed hostile MCP server and whose consequence is one dropped frame, not compromise. Capping `name` upstream stays out of scope.
  2. *Resource exhaustion in the event ring — the finding this pass turned up that the design had not considered.* `internal/eventring` retains up to `MaxEventsPerConversation` = 1024 events per conversation and preferentially retains control-class events, of which `tool_use` is one, evicting `assistant_delta` first. Today a `tool_use` payload is ~250–400 bytes, so a full ring costs ~0.3 MB per conversation. After this change a heavy-`Edit` session stores ~9 KB per `tool_use` and the worst case is ~55 KB, i.e. **~9 MB and up to ~56 MB per conversation**, a 30–100× growth in a daemon designed to run for days; `convs` is not itself capped, so it multiplies by live conversations. The unsealed per-conn outbound queue in `V2SessionManager.Push` is a second, shallower retention point with the same multiplier. Deferred rather than fixed: the ring is purely in-memory and never persisted (its package doc is explicit, and it deliberately does not survive a daemon restart), no OOM has been observed, and its own doc already flags `MaxEventsPerConversation` as "a tunable starting point, not load-tested" with ADR 025 § Roadmap calling for a load test. The contribution here is the number, plus a second reason for the "LOWER these constants, never raise them" rule: `maxInputTotalRunes` is now a memory knob as well as a wire knob. Calibrating the ring is a follow-up ticket, filed against `internal/eventring`, not against this one.

- **[Error messages, logs, telemetry] No findings — and this is the category with the hardest constraint.** Nothing about the extraction reaches a log: `internal/turnbridge` has no logger, its package doc commits to "no I/O", and every reject path in `inputFields` returns nil silently by design. `inputFields` returns no error, so there is no error string for a caller to log a blob through. Checked the persistence surfaces too: `internal/debugbundle` references neither `eventring` nor the interactive frame types, so tool inputs cannot reach a bundle an operator might share; the event ring is memory-only. The developer must not add a logger, an error return, or a `slog` call to this path — that is stated in § Nothing is logged as a hard constraint.

- **[Concurrency] No findings.** `inputFields` is pure and allocates a fresh map per call; `MapEvent` hands it off and retains nothing. No goroutine, no lock, no context, no shared state. The one aliasing question — whether a caller could retain and mutate the returned map — is answered by `eventring.Ring.Append`'s signature, which takes an already-marshalled `json.RawMessage`, not the payload value, so the map is never stored live. Existing `t.Parallel()` coverage in both packages stays valid.

- **[Threat model alignment] No findings.** The relevant threat is the untrusted relay, and it is unchanged: frames are sealed before they leave the daemon and the relay routes ciphertext. This ticket adds no new participant, no new capability, and no new persistence.

**Reviewer:** architect (self-review per the security-review checklist)
**Date:** 2026-08-21
