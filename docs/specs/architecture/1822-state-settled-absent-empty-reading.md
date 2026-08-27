# #1822 — State the settled absent/empty reading at the four comments that still defer it to the closed #1690

**Size:** XS (comment-only; 2 production source files + 1 test file, ~60 lines of prose rewritten, 0 behaviour change)
**Security-sensitive:** yes — see § Security review.

## Files to read first

Read in this order. The two `turnevent` field docs are the *source of the wording*; everything else is a site or a fact those sites must state correctly.

| Path | Symbol | What to extract |
| --- | --- | --- |
| `internal/turnevent/event.go` | `ModelOption.EffortLevels` | **The canonical effort-levels wording.** The paragraph opening "AN ABSENT KEY, A JSON null AND A PUBLISHED EMPTY ARRAY ARE ONE READING, AND IT IS SPELLED nil (#1828)". Re-use this phrasing; do not re-derive the argument. |
| `internal/turnevent/event.go` | `ModelOption.SupportsAutoMode` | **The canonical bool wording.** "AN ABSENT KEY, A JSON null AND AN EXPLICIT false ARE ONE READING — false" (#1819), plus *why*: the field is a permission GRANT, the safe direction is asymmetric, and the unsafe inverse is granting on silence. This paragraph is the security check for site 3 and site 4. |
| `internal/protocol/interactive.go` | `ModelOption.MarshalJSON` | **Site 1.** The doc's third paragraph ends with the `#1690` deferral. Note the trailing `#1693` clause — #1693 is OPEN and stays. |
| `internal/protocol/interactive.go` | `SlashCommand.MarshalJSON` | **Site 2.** The doc's second paragraph ends with the `#1719`/`#1690` clause. Note the trailing `#1720` clause — #1720 is OPEN and stays. |
| `internal/protocol/interactive.go` | `ModelOption` (the struct doc) | The wire type's own `SupportsAutoMode` sentence — "Absent in claude's reply (Haiku's entry omits it) decodes to false, which is the correct reading." This is the house phrasing site 1 sits next to. Its `supportsEffort … once the empty encoding below is decided` clause is **out of scope** (§ Open questions). |
| `internal/streamsup/parser.go` | `modelOptionLine` | Where each collapse actually happens. For the bool: `encoding/json`'s absent/null no-op — no daemon code implements it. For the list: absent and null land as nil for free, and `[]` is normalised separately. |
| `internal/streamsup/parser.go` | `emitModelList` | The `boundEach` closure's zero-length arm — the one place the `[]` → `nil` normalisation is performed, and the only normalisation anything below the decode does. Site 3's new reason names this. |
| `internal/e2e/internal/fakeclaude/main.go` | `initializeModels` | **Site 3** — the doc comment. The `var` block beneath it (sonnet's eight keys, haiku's four) **must not change**. |
| `internal/e2e/internal/fakeclaude/initialize_control_test.go` | `TestRunStreamJSON_InitializeControlAnswer`, subtest `"covers both the present and the absent arm"` | **Site 4** — the `t.Error` message in the `minimal == 0` branch. Also read the `hasLevels`/`hasAuto` reads above it: presence and value are read *separately*, and that is the input-shape fact the new message restates. |
| `docs/knowledge/features/fakeclaude-binary.md` | § the model-list / `initializeModels` section | **READ ONLY, do not edit.** It carries the same stale claim ("leaving the daemon-internal reading … for #1690", "a fake that emitted `[]`/`false` … would settle #1690's question"). The documentation phase repairs it; it is not a deliverable here. |
| `CODING-STYLE.md` | § "Comments — Citing Other Code" | The rule `make check` enforces via `cmd/cite-guard`: name the symbol, never `file.go:NNN`, no range exemption, no bare `:NNN`. |

## Context

Four shipped comments hand the absent-vs-empty (and absent-vs-false) reading of claude's model capability keys to **#1690, which is CLOSED as NOT_PLANNED**. #1690 was split; the slices that inherited the question have decided it and shipped:

| key | settled reading | decided by | canonical wording |
| --- | --- | --- | --- |
| `supportedEffortLevels` | absent key, JSON `null` and published `[]` are ONE reading, spelled `nil` | #1828 (CLOSED/COMPLETED) | `turnevent.ModelOption.EffortLevels` |
| `supportsAutoMode` | absent key, JSON `null` and explicit `false` are ONE reading — `false` | #1819 (CLOSED/COMPLETED) | `turnevent.ModelOption.SupportsAutoMode` |

Until the tree says so, a reader chasing why a nil effort list or a false auto-mode flag means what it means lands on a closed issue.

**This slice states an answer; it does not choose one.** No behaviour changes, no test is added or removed, no constant is introduced. The design work is entirely in *what each of the four comments must now assert* and — for the fake — *which of its two reasons survived the decision*.

Issue states verified at `e9b04d73`: #1690 CLOSED/NOT_PLANNED, #1719 CLOSED/NOT_PLANNED, #1704 CLOSED/COMPLETED, #1812 CLOSED/COMPLETED, #1819 CLOSED/COMPLETED, #1827 CLOSED/COMPLETED, #1828 CLOSED/COMPLETED; **#1693, #1720 and #1825 OPEN**.

No ADR is warranted — #1828 and #1819 already carry the decisions and their own specs (`docs/specs/architecture/1828-absent-versus-empty-effort-levels.md`, `1819-model-auto-mode-decode.md`).

## Design

No types, no interfaces, no data flow. Four comment contracts.

### Site 1 — `protocol.ModelOption.MarshalJSON` doc

**What must go:** the clause deferring the daemon-internal reading to #1690.

**What must be asserted instead:**

1. The daemon-internal value does **not** keep the distinction: `turnevent.ModelOption.EffortLevels` reads an absent key, a JSON `null` and a published empty array as ONE reading, spelled `nil` (#1828).
2. The wire's position is still stated *here* independently of the daemon-internal spelling, because an undeclared position is one **#1693** would have to invent. **This clause stays — #1693 is open.** The existing "either way" phrasing referred to a live fork; reword it so it reads as independence from the daemon-internal spelling rather than as an undecided question.

**Preferred addition (true, and it closes the paragraph cleanly):** post-#1828 the daemon-internal reading and the wire's collapse *agree*, so #1693's mapping has no fork to bridge. This is the same fact `turnevent.ModelOption.EffortLevels` states from the other side ("a kept distinction would have a lifetime of one call … its first consumer maps into protocol.ModelOption, whose MarshalJSON already normalises nil to []").

**What must NOT change:** the `[]` -is-a-COLLAPSE-not-a-positive-statement argument, the Haiku observation, the `TruncatedFields` carve-out, the cannot-be-folded-into-the-payload's paragraph, the value-receiver note. The method body is untouched.

### Site 2 — `protocol.SlashCommand.MarshalJSON` doc

This site carries **two** closed pointers in one clause and both must move. Re-pointing only the `#1690` half would leave the sentence deferring to a closed ticket — the exact defect this ticket removes.

**What must be asserted instead:**

1. **Model-list half (was `#1690`):** the model list's own question is settled — `turnevent.ModelOption.EffortLevels` reads absent, null and `[]` as one reading, spelled `nil` (#1828).
2. **Slash-command half (was `#1719`):** whether the daemon-internal *alias* value keeps the absent/empty distinction is **#1825**'s call. #1719 closed NOT_PLANNED on 2026-08-26 and split into #1824 → #1825 → #1826; #1825 is open and its AC 2 reads **this comment** to decide.
3. **The precedent is offered, not inherited.** The model list's settled reading is available to #1825 as a precedent to weigh, not a conclusion to adopt. This is the house rule, stated at `turnevent.ModelOption.SupportsAutoMode`: `EffortLevels` "RE-DERIVED its answer rather than inheriting this one … which is why the coincidence must not be read as this paragraph having set a precedent." A sentence that reads as *therefore the aliases collapse too* would pre-empt #1825 and is wrong.
4. **The trailing `#1720` clause is unchanged** — #1720 is open, and an undeclared wire position is still one its producer would have to invent.

**What must NOT change:** the measured 51-entry alias frequency argument, the "frequency INVERTED" comparison to `ModelOption.MarshalJSON`, the `TruncatedFields` carve-out, the fold/receiver paragraphs. The method body is untouched. The separate SECURITY paragraph on `SlashCommand` that names `(#1719/#1720)` as the producer's bound is **out of scope** — it routes to #1826 (see § Out of scope).

### Site 3 — `fakeclaude.initializeModels` doc

The load-bearing site. It currently gives **two** reasons for the map-per-entry / key-omitting shape, and exactly one of them expires with the decision.

**Reason that survives, unchanged in substance:** a `map[string]any` per entry rather than a struct with `omitempty`, because ABSENCE is the load-bearing property of the fake's *output* and a map makes it literal. `omitempty` would conflate `false` with absent for `supportsAutoMode` — that is a fact about the fake's own encoder and is still true.

**Reason that expires:** *"A fake emitting `"supportedEffortLevels":[]` instead of omitting the key would settle it first."* Post-decision there is nothing left to settle.

**Reason that replaces it — the decode-side one.** The comment must say the distinction is now *read* a particular way, **never that it stopped mattering**:

- Both readings are settled and each has one canonical home: `turnevent.ModelOption.EffortLevels` (absent / `null` / `[]` → `nil`, #1828) and `turnevent.ModelOption.SupportsAutoMode` (absent / `null` / `false` → `false`, #1819).
- **The collapse is the DECODE's to perform, and the two collapse in different places.** For the list it needs code: `emitModelList`'s `boundEach` zero-length arm is where `[]` lands on `nil`, and it is the only normalisation anything below the decode does. For the bool it needs none: `encoding/json`'s absent/null no-op already lands all three on `false`, and the field's *shape* — a plain `bool` on `modelOptionLine` — is the decision.
- **Therefore the minimal entry's job is to feed the decode the shape claude actually sends.** A fake emitting `[]` / `false` would hand the decode an already-collapsed input, so the absent-key path would never be traversed — and the absent key is the *only* shape observed: the committed capture's two four-key entries omit the entire capability block, and claude has never published `false` or `[]` at all (`turnevent.ModelOption.SupportsAutoMode` states the never-sent-false measurement). Canning `[]`/`false` would test a shape claude has never produced while leaving the one it does produce unexercised end to end.

**Phrasing constraint:** the minimal entry no longer exists to "carry a distinction" in the daemon's *reading*. It exists to supply the absent *input shape*. Reword the "precisely the distinction the minimal entry exists to carry" clause accordingly.

**What must NOT change:** the two-entry canned list itself — sonnet's eight keys, haiku's four — byte for byte. The capture-provenance paragraph, the deliberately-not-canned opus note, and the READ-ONLY paragraph are untouched.

### Site 4 — the `minimal == 0` assertion message in `initialize_control_test.go`

Inside `TestRunStreamJSON_InitializeControlAnswer`, subtest `"covers both the present and the absent arm"`. **The message changes; the condition does not.** No test is added, removed, renamed, or re-conditioned.

**What must be asserted instead:** present-and-empty or present-and-false is a different *input* from absent, and emitting one would hand the decode an already-collapsed input, leaving the absent-key arm — the shape claude actually sends — unexercised. It must **not** say the readings are undecided, and must **not** say the distinction no longer matters.

Keep the message a single failure sentence in the file's existing idiom (what was observed, then why it is a failure). It is a Go string literal rather than a `//` comment, so `cite-guard` does not scan it — but name symbols there anyway, per `CODING-STYLE.md`.

## Citations — the rule that gates the build

`make check` runs `cite-guard`, which is diff-scoped and fails on any `file.go:NNN` inside a `//` comment that resolves to a declaration, at any depth. **No depth exemption, no range exemption, and never a bare `:NNN`.**

Every reference these four comments need is already a nameable symbol: `turnevent.ModelOption.EffortLevels`, `turnevent.ModelOption.SupportsAutoMode`, `protocol.ModelOption.MarshalJSON`, `emitModelList`'s `boundEach` closure, `modelOptionLine`. Cite the **deciding** tickets (#1828, #1819, #1825) as the existing comments cite theirs; do not cite #1822 — this slice decides nothing.

## Concurrency model

None. No goroutine, channel, lock or shutdown path is touched. `initializeModels`' READ-ONLY paragraph — never appended to, never reassigned, marshalled from `runStreamJSON`'s single goroutine and from `t.Parallel()` subtests — remains accurate because the `var` block is unchanged.

## Error handling

None. No error path, no new failure mode. The only runtime-visible text that changes is one `t.Error` message, which is reached only when the fake's canned list has already regressed.

## Testing strategy

No test is written. Verification is the three ACs, each mechanically checkable:

- `git grep -n 1690 -- internal/protocol internal/e2e/internal/fakeclaude` → **zero hits** (today: exactly the four sites).
- `git grep -n "1719's call" -- internal/protocol/interactive.go` → **zero hits** (today: one).
- `git diff` shows changes only inside comment bodies and one string literal. The `initializeModels` `var` block, both `MarshalJSON` bodies, and every test condition are byte-identical.
- `make check` green — it runs `cite-guard`, so a `file.go:NNN` slipped into any of the four sites fails the build.
- Sanity sweep that the replacements landed rather than merely deleting the pointers: `git grep -n 1828 -- internal/protocol internal/e2e/internal/fakeclaude` and `git grep -n 1825 -- internal/protocol/interactive.go` each return hits.

`internal/e2e` is behind the `e2e` build tag; a bare `go test ./internal/e2e/...` runs zero tests. Use the Makefile target — `make check` covers the fake's package.

## Out of scope — do not touch

Three separate cleanups, each a different defect class with a different owner. The developer's scope discipline applies: if one looks broken, it stays broken here.

- `internal/e2e/realclaude/` (`initialize_control_probe_test.go`, `initialize_control_writer_test.go`, `initialize_control_record_test.go`) — eight further `#1690` mentions. Stale *attribution* ("#1690's decoder"), not a live question deferred to a closed ticket. This ticket's sweep deliberately excludes that path.
- `docs/protocol-mobile.md` § `dropped_models` — "Nothing counts it. … #1690 owns making the decode record it". A stale **claim** that #1812 falsified; belongs to the drop-counter line (#1812 / #1693).
- `internal/protocol/interactive.go`'s `SlashCommandListPayload.DroppedCommands` paragraph, the `SlashCommand` per-entry-string SECURITY paragraph's `(#1719/#1720)`, and the matching `interactive_test.go` round-trip doc — same defect class as this ticket's four, different question and different owner. These route to **#1826**.
- `internal/protocol/interactive.go`'s `ModelOption` struct doc, which still says `supportsEffort` "is subsumed by EffortLevels **once the empty encoding below is decided**" — a live conditional on a question #1828 has since answered. Not named in this ticket's ACs and not caught by either sweep. Leave it; it belongs with the wire-doc cleanup above.

A fifth site named in #1809's body was **already repaired** by #1812 (`ModelListPayload`'s `DroppedModels` paragraph now reads "The decode now COUNTS IT (#1812)"). Do not re-open it.

## Open questions

- **`docs/knowledge/features/fakeclaude-binary.md` carries the same stale claim** in its model-list section — "leaving the daemon-internal reading of that distinction for #1690", "a fake that emitted `[]`/`false` for the minimal entry would settle #1690's question before it got there", and "#1690 is what teaches the daemon to read it". That file is owned by the documentation phase and is **not** a developer deliverable. Flagged here so the documentation phase folds the settled readings and the fake's surviving reason into it after code review.
- Whether site 1's paragraph should additionally state that #1693's mapping now has no fork to bridge is a judgement call left to the developer. It is true and it closes the paragraph; omitting it is not a defect.

## Security review

**Verdict:** PASS

The change is comment-only, but one of the two readings it restates is a **permission-grant default whose entire argument is that the safe direction is asymmetric**. A comment that restates it in the wrong direction would document an inverted default that a later slice could implement. That is the whole reason this ticket carries the label, and it is a real category-7-adjacent hazard (documentation as an attack surface on a future implementer) rather than a formality.

**Findings:**

- **[Trust boundaries] MUST-GET-RIGHT, addressed in the Design.** The boundary these comments describe is claude's subprocess stdout → daemon state, crossed at `streamsup`'s `modelOptionLine` decode and bounded in `emitModelList`. All four sites sit on the *untrusted* side's description. The spec's constraint that the fake's comment must say the distinction is now *read* a particular way — never that it stopped mattering — is what keeps the boundary described as a boundary. The data itself remains untrusted, model-influenced text whose render-boundary sanitization is the CLIENT's; no site may be reworded to suggest the collapse launders it. No wording in this spec does.
- **[Permission-grant default — the label's reason] MUST-GET-RIGHT, addressed.** `supportsAutoMode` is restated at two sites (3 and 4). The settled reading is **absent / `null` / explicit `false` → `false`**, i.e. *grey the option out on silence*. The **unsafe inverse is granting on silence**, which a claude that merely stopped sending the key would walk into. Concrete failure the spec forecloses: a fake comment reading "absence no longer means anything, the daemon defaults auto on" would be a shipped, plausible-looking statement of the inverted default, sitting in the one file a future fake-shape change is read against. The spec pins the wording to `turnevent.ModelOption.SupportsAutoMode`'s own paragraph and forbids re-derivation — reviewer check: **the word `false` must appear as the reading at both sites, and no site may state or imply auto is granted, enabled, or defaulted-on when the key is absent.**
- **[Pre-empting an open security decision] SHOULD FIX, addressed in Design § Site 2.** #1825 is OPEN and its AC 2 reads site 2 to decide the slash-command aliases' own reading. A site-2 rewrite that presented the model list's collapse as binding precedent would pre-decide an open question from a comment — the inverse of the defect this ticket removes. The Design requires the precedent be offered, not inherited, matching the house rule at `turnevent.ModelOption.SupportsAutoMode` ("RE-DERIVED its answer rather than inheriting this one"). Code review should check that site 2 names #1825 as the decider rather than announcing the answer.
- **[Subprocess / external command execution] No findings — not applicable.** Nothing in this change reaches `exec.Command`. `fakeclaude`'s `initializeModels` is a canned response payload, not argv, and the spec forbids changing it. `protocol.ModelOption.Value`'s argv-injection defense (`internal/relay`'s `validModel`, #845's charset) is described at site 1's struct doc and is **not** touched — no site may be reworded to suggest widening it.
- **[Network & I/O — resource bounds] No findings.** The per-element cap (`maxModelEffortLevel`) and the per-count cap (`maxModelEffortLevelCount`, #1821) are stated at `turnevent.ModelOption.EffortLevels` and remain untouched. Site 3's new reason names `boundEach`'s zero-length arm only, which is the normalisation, not the bound — a rewording that conflated the two would misdescribe where the envelope-budget defense lives. The spec keeps them distinct.
- **[Error messages, logs, telemetry] No findings.** The one message that changes is a `t.Error` in a test, reached only on a canned-list regression; it carries no claude-authored bytes. `emitModelList`'s "NOTHING FROM THE PAYLOAD IS LOGGED, on any path" property is unaffected.
- **[Tokens, secrets, credentials] Not applicable** — no credential, token or key material is named, stored, compared or logged on any path this ticket touches.
- **[File operations] Not applicable** — no path is constructed, opened, created or removed. The only files written are the three source files and this spec.
- **[Cryptographic primitives] Not applicable** — no RNG, hash, KDF or comparison is involved.
- **[Concurrency] No findings.** `initializeModels`' READ-ONLY invariant (never appended to, never reassigned; marshalled from `runStreamJSON`'s single goroutine and from `t.Parallel()` subtests, where a mutation would race in a way `-race` catches only on overlap) holds *because* the `var` block is unchanged. The spec makes that an explicit non-deliverable at two places.
- **[Threat model alignment] No findings.** `docs/protocol-mobile.md` § Security model's client-side-render-boundary rule is the one relevant threat; the spec preserves every SECURITY paragraph verbatim and adds no claim about sanitization.

**Reviewer:** architect (self-review per the security-review checklist)
**Date:** 2026-08-27
