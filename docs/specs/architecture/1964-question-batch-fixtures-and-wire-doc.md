# #1964 — Pin the question-batch encoding with fixtures and publish it in the mobile-protocol doc

## Files read

- `internal/protocol/questions.go` → `QuestionShownPayload`, `Question`, `QuestionOption`, and the two `MarshalJSON` normalisers — the shape being pinned, and the doc block that already records *documented 12, observed 14*, the bounds' non-enforcement, and the missing truncation report. Its header comment carries the forward reference to this ticket that expires here.
- `internal/protocol/questions_test.go` → `TestQuestionShownPayload_RoundTrip`, `TestQuestionShownPayload_ZeroValue_RoundTrip`, `TestQuestionShownPayload_NilQuestionsNormalises`, `TestQuestion_NilOptionsNormalises` — the four tests #1963 landed. The first two carry the inline goldens this slice moves to disk; its header sentence naming the fixtures as #1964's also expires here.
- `internal/protocol/envelope_test.go` → `readFixture`, `canonical` — the two helpers every fixture-backed test in this package shares.
- `internal/protocol/interactive_test.go` → `roundTripEnvelope`, `TestSlashCommandListPayload_Empty_RoundTrip`, `TestSlashCommandListPayload_ZeroValue_RoundTrip` — the exact shape the new empty test mirrors, and the round-trip helper all three tests already call.
- `internal/protocol/envelope.go` → `Envelope` — `ID`/`Type`/`TS`/`Payload` are the four keys the fixtures carry; `InReplyTo`, `EventID` and `PayloadEncrypted` are `omitempty` and stay absent.
- `internal/protocol/codes.go` → `TypeQuestionShown` — the frame family's argument, the naming trap, and the declare-then-emit sequencing the doc section restates for a client author.
- `internal/e2e/realclaude/testdata/ask_user_question_v2.1.239.json` → the committed capture: one question, two options, `multiSelect:false`, header `"Write strategy"` (14 runes, 14 bytes, pure ASCII). The three arms it cannot settle are why the populated fixture is hand-authored.
- `docs/protocol-mobile.md` → § Application message types, § Interactive events, § `model_list`, § `slash_command_list`, § Modal (v2), § Queue (v2), § Security model, § Changelog — the file the section lands in and the two sections whose structure it copies.
- `docs/knowledge/features/protocol-package-question-batch-payload.md` — #1963's own record; its **Testing** section states what the inline goldens pin, and its closing bullet names this slice as the owner of the documented-12/observed-14 resolution.
- `docs/knowledge/features/protocol-package-slash-command-list-payload.md` — #1718's lessons, three of which bind this slice directly: generate committed fixtures through the package's own marshallers via a `go test -overlay` file mapped to a scratchpad generator; call `readFixture` **once** outside a byte-guard loop so the failure message still has `raw` to print; and treat any "which test reddens this mutant" claim as something to re-run rather than predict, because #1718 shipped that claim wrong in both directions.

## Context

`docs/protocol-mobile.md` is where a client author reads the wire, and it does not contain the string `question_shown` at all. #1963 landed the Go shape and pinned it against **inline** goldens; pyrycode-desktop#849 has to mirror it field for field without reading Go, and every other payload in this package is pinned by committed bytes under `internal/protocol/testdata/`. This slice moves the bytes to disk, adds the one arm #1963 could not reach (a decoded batch carrying `"questions":[]`), and publishes the contract.

No ADR is warranted: this ticket decides nothing new. The design decisions it publishes were all taken in #1962 and #1963 and already have their homes in `TypeQuestionShown`'s and `questions.go`'s doc blocks.

## Design

### Three fixtures under `internal/protocol/testdata/`

Named for the frame type, the family's convention (`slash_command_list.json` / `_empty.json` / `_zero.json`):

| Fixture | Envelope `id` | What it is |
|---|---|---|
| `question_shown.json` | 901 | Populated batch — two questions; the second carries `multi_select: true` and three options. |
| `question_shown_empty.json` | 903 | `"questions":[]` with both ids non-empty. **New coverage.** |
| `question_shown_zero.json` | 902 | Zero value — one all-zero `Question` holding one all-zero `QuestionOption`. |

`question_shown.json` and `question_shown_zero.json` carry **the values #1963's inline goldens already carry**, ids and timestamps included, so the move is auditable as a move rather than a regeneration. Only the byte layout changes: hand-typed indentation becomes the encoder's own compact output. The empty fixture takes the next free id (903) rather than displacing the zero fixture's 902.

**Hand-authored values, encoder-produced bytes.** The strings stay hand-written — the capture is one question, two options, explicit `multiSelect:false`, so it cannot reach any of AC 2's three arms — but the committed bytes come from marshalling `Envelope` + `QuestionShownPayload` through this package, so escaping is the encoder's own. Method: a throwaway generator mapped over `go test -overlay` onto a path that does not exist in the package, writing the three files. #1718's record of that technique is the reason it is not re-derived here. Nothing of the generator is committed and nothing is left in the worktree.

### Reachability — all nine wire keys, checked rather than copied

Nine keys across three levels: `conversation_id`, `question_batch_id`, `questions` (payload); `question`, `header`, `options`, `multi_select` (`Question`); `label`, `description` (`QuestionOption`).

- The **populated** fixture reaches all nine at non-zero values (`multi_select` at both positions).
- The **empty** fixture reaches the payload's three, and is the only committed bytes carrying `"questions":[]`.
- The **zero** fixture reaches all nine at their zero values — one entry holding one option is the only route to the two deepest levels, since a batch with no questions reaches neither.

`"options":[]` is reached by **no** fixture, and that is a property of this shape rather than an omission: the documented bounds put it out of contract, decoding `[]` always yields a non-nil slice, and the only value that produces those bytes is a constructed nil — which is exactly what `TestQuestion_NilOptionsNormalises` already pins. The two-level analogues (`model_list_zero.json`, `slash_command_list_zero.json`) reach their empty-array key from the same all-zero entry; one nesting level deeper, this shape does not, so their arithmetic is not copied.

### Test changes in `internal/protocol/questions_test.go`

- `TestQuestionShownPayload_RoundTrip` and `TestQuestionShownPayload_ZeroValue_RoundTrip`: replace the inline `raw := []byte(...)` with `readFixture(t, ...)`. **Every field-by-field assertion carries over unchanged**, the zero-value test's seven-key explicit-presence loop included — that loop is what an `omitempty` on `header` or `multi_select` trips, and it must keep running *before* the round trip for the reason its own comment gives. `readFixture` is called once, outside the loop, so a failure still prints `raw`.
- New `TestQuestionShownPayload_Empty_RoundTrip`: mirrors `TestSlashCommandListPayload_Empty_RoundTrip` — guard `"questions":[]` in the canonical bytes, decode the envelope, assert the type, decode the payload, assert both ids and a zero-length `Questions`, then `roundTripEnvelope`. This is the decode-side pin AC 1 names; #1963 proved nil→`[]` on a constructed value only.
- The two nil-normalisation tests are untouched.
- The file header's *"the testdata fixtures are #1964's"* sentence is retracted; the surviving half of its rationale (the capture sits behind `e2e_realclaude`, which `make check` never compiles) still explains why the fixture values are hand-authored.

### `internal/protocol/questions.go` — comment only

The header block names #1964 as the owner of the fixtures and the doc section. That reference expires with this commit and is retracted in it, as #1718 retracted its own three. No code changes.

### `docs/protocol-mobile.md`

1. **New `### Question (v2)` section with a `#### question_shown` subsection**, immediately after § Modal (v2) and before § Queue (v2). Contents:
   - Direction, `v1TypeSet` exclusion, capability gating, and the no-`omitempty` discipline — § Modal's opening shape.
   - **Three field tables**, one per nesting level, each row stating the field's **provenance**: `conversation_id` and `question_batch_id` are daemon-asserted and never filled from claude's tool input; the four strings are claude-authored. A client mirroring the shape field for field must be able to read the trust boundary off the tables, not infer it from the SECURITY paragraph alone.
   - **`question_batch_id`'s nonce obligations**, § Modal's for `modal_id`: one-time, opaque, **unguessable**, minted per surfaced batch, resolved server-side. The minting is #1927's and is bound to `crypto/rand` by `questions.go`. The fixtures' ids are placeholders and the section says so — neither their length nor their shape is a contract.
   - **No inbound verb is declared**, § `slash_command_list`'s statement: nothing accepts an answer or a dismissal today, and whether a dismissal is its own type or reuses `modal_dismissed` is #1927's call. `QuestionOption` carries no id — claude's answer protocol selects by **label** — so whatever inbound frame #1927 designs will carry a claude-authored string back, and publishing that string here does not make it trusted on the way in.
   - **Contract bounds**: 1–4 questions, 2–4 options per question, the header cap, and the **per-field length bound on the four strings** — which is not a number anywhere, because none is published or enforced (below).
   - **The header cap as documented 12, observed 14**, naming the capture and its `"Write strategy"` header, and saying the cap is a **rune** count — with the measured caveat that the observed header is pure ASCII, so 14 runes and 14 bytes coincide there and nothing committed separates the two units. A client sizes for the observed case.
   - **Which component enforces each bound**, re-checked against the tree below.
   - **Nothing emits this frame yet**, with the declare-then-emit chain: type #1962, shape #1963, fixtures and section #1964, parse #1965, producer #1927, consumer pyrycode-desktop#849.
   - **SECURITY** paragraph: four claude-authored strings across the subprocess trust boundary at `model_list`'s trust tier — [§ Security model](../../protocol-mobile.md)'s threat 1 (prompt injection, `severity: high`, `mitigation: partial`) is the live one here, since claude's own words become text a remote client renders — inert text only, client owns sanitization, report never a control input, plus the consequence unique to this shape: it carries no truncation report, so a cut cannot be reported and must not be made silently.
2. **One row** in § Application message types, in the outbound cluster.
3. **One changelog entry**, dated `2026-09-01`, recording what the fixtures pin, the enforcement statement, and that **no live count moved** — the section lands after § Modal, so every sentence reading *fifteen* stays checkable by counting `#### ` headings under § Interactive events (counted 2026-09-01: `turn_state` through `model_announced`, fifteen).

### Enforcement — as the tree stands 2026-09-01, verified, not assumed

| Bound | Enforcer |
|---|---|
| 1–4 questions per batch | **Nowhere.** |
| 2–4 options per question | **Nowhere.** |
| Header ≤ 12 | **Nowhere, and expected to stay that way.** |
| Per-field length of the four strings | **Nowhere, and no number is published either** — the bound is #1965's and the producer's. |

`internal/protocol` enforces none of these by design — `questions.go` says so in as many words. The fail-closed bounded parse is #1965, which is **OPEN and unmerged** (verified 2026-09-01), and nothing else in `cmd/` or `internal/` decodes an `AskUserQuestion` tool input. The header cap is the one bound whose future enforcer is already constrained *not* to hold it: #1965's own acceptance pins it against the same capture, so a 12-rune fail-closed reject would make the two unsatisfiable together. The per-field length bound gets **no number**, for § Attachments' reason (#1752): publishing a figure ahead of the code that enforces it is what that slice existed to prevent, and a client learns the bound by being rejected. § `slash_command_list`'s *"Nothing counts it yet"* is the honesty pattern; § `model_list`'s 2026-08-27 changelog entry is the cost of publishing an enforcement claim that was not true.

## Concurrency model

None. Three JSON files, test-file edits, one comment retraction, and a documentation section. No goroutines, no shared state, no shutdown path. The one concurrency fact in the area is already pinned by `TestQuestion_NilOptionsNormalises`'s nested subtest — a payload marshaller normalising entries in place would reach through the caller's backing array — and this slice does not touch either marshaller.

## Error handling

No production error paths change. Test-side failure modes:

- A missing or unreadable fixture → `readFixture` `t.Fatalf`s naming the file.
- A fixture whose bytes do not round-trip → `roundTripEnvelope` prints both sides.
- A malformed generated fixture → caught at the first `go test` after generation, before the commit.

## Testing strategy

1. **RED before GREEN.** Add the three fixtures and switch the two existing tests to `readFixture`, plus the new empty test. Prove RED honestly rather than by construction: run the empty test before its fixture exists (`readFixture` fatals), and run the two converted tests against a deliberately wrong byte in the generated fixture, watching `roundTripEnvelope` fail on the diff.
2. **The `omitempty` sweep is run, not predicted.** Add `,omitempty` to each of the nine wire keys in turn over a `go test -overlay` scratch copy — never an in-worktree edit — and record which tests redden. #1718's lesson is explicit that a sole-redness claim written from the prediction column ships wrong; anything the changelog entry says about which fixture catches which mutant comes from the run's output. The mutation-run script is invoked through `bash -c`, not pasted into this session's `zsh` (also #1718's).
3. **Gate:** `go test -race ./internal/protocol/...`, `go vet ./...`, `go build ./cmd/pyry`, `make cite-guard`, `make docs-guard` (the doc edit is why the second one is in the list). The full-module race suite is the verifier's.

## Open questions

1. **Does the empty fixture add a mutant nothing else catches?** An `omitempty` on `questions` also reddens `TestQuestionShownPayload_NilQuestionsNormalises`, so the empty fixture is probably not sole-red for it — its value is the decode-side arm, not a unique mutant. Resolve from the sweep's output and state it accurately in the changelog entry; do not claim sole redness.
2. **Does `make docs-guard` bind `docs/protocol-mobile.md`?** The 50000-byte cap is written for `docs/knowledge/features/`; this file is much larger and already ships. Check what the guard actually covers before assuming the section is free, and if the guard does bind it, that is a finding for the ticket rather than something to work around silently.

Both are resolved in Phase B and recorded under `## Revisions` if either changes the design.

## Security review

**Verdict:** PASS (second pass — the first FAILED on three MUST FIX findings, all revised into the Design section above before this was written)

The deliverable is a **published contract**, so the exploitable surface is not code: it is what a client author is told, and what they will build having read only this section. Every finding below is about the section's content for that reason.

**Findings:**

- [Trust boundaries] **MUST FIX — fixed.** The first draft put provenance only in the SECURITY paragraph. Two fields sit on the *daemon-asserted* side of the boundary (`conversation_id`, `question_batch_id` — never filled from claude's tool input) and four on the *claude-authored* side (`question`, `header`, `label`, `description`), and a client mirroring the tables field for field would have had to infer which was which. Rendering a `header` as trusted chrome is the concrete bad outcome. The field tables now state provenance per row.
- [Tokens] **MUST FIX — fixed.** The first draft published `question_batch_id` as a plain string row. It is `modal_id`'s role exactly — one-time, opaque, **unguessable**, minted per batch, resolved server-side — and the wire doc is what both #1927 and pyrycode-desktop#849 read. Left as drafted, a reader could implement it as a sequence counter, which makes an inbound answer forgeable the moment #1927 lands one. The section now publishes the four properties and names `crypto/rand` as the minting obligation. Related and folded into the same sentence: the fixtures' `qb-7f3a`-style ids are **placeholders**, and the section says neither their length nor their shape is a contract, so nobody sizes a real nonce from a fixture.
- [Tokens / inbound surface] **MUST FIX — fixed.** No inbound verb exists for this family, and `QuestionOption` carries no id because claude's answer protocol selects by **label**. A section that published the outbound shape without saying so invites a client author to design an answer frame that echoes a claude-authored string, treating it as an identifier the daemon issued. The section now states that no inbound verb is declared, that #1927 owns whatever answer or dismissal it designs, and that publishing a label does not make it trusted coming back.
- [Network & I/O] **MUST FIX — fixed.** The four strings have **no published length bound and no enforcer**, and the first draft's enforcement table listed only three bounds — so a reader would have concluded the header cap was the *only* gap and that a `description` was bounded somewhere. It is not. The table gains a fourth row, and the bound is deliberately given **no number** (§ Attachments' #1752 reason: a figure published ahead of its enforcer is worse than none). No socket read, cap, or timeout is otherwise in this slice's scope.
- [File operations] No findings — the generator writes exactly three hardcoded repo-relative paths under `internal/protocol/testdata/`, concatenates no input, and creates no secret-bearing file, so no mode, canonicalisation, symlink or atomicity question arises. The overlay JSON and the generator body live in the scratchpad and are never committed; Phase B checks `git status` before committing so nothing else rides along.
- [Subprocess execution] No findings — the only child processes are `go test` and a `bash -c` mutation script whose arguments this plan authors in full. No value from the ticket, the capture, or any fixture reaches an argv element, and no `sh -c` over a constructed string is used.
- [Cryptographic primitives] No findings in the slice itself — it mints nothing and compares nothing. Its one crypto-adjacent obligation is not to publish a nonce format that would constrain #1927 away from `crypto/rand`; that is the Tokens finding above, and the section publishes the property rather than a format.
- [Error messages, logs, telemetry] No findings — no production error path changes. Test failures print fixture bytes, which are hand-authored placeholder strings about cache design and carry nothing sensitive. The section describes no logging obligation, and deliberately does not invite a client to log the batch id.
- [Concurrency] No findings — no goroutine, no lock, no shared state. The area's one real hazard, a payload marshaller normalising entries in place through the caller's backing array, is pinned by `TestQuestion_NilOptionsNormalises`'s nested subtest; this slice touches neither `QuestionShownPayload.MarshalJSON` nor `Question.MarshalJSON`, so that protection is unchanged.
- [Threat model alignment] `docs/protocol-mobile.md` § Security model threat 1 (prompt injection, `severity: high`, `mitigation: partial`) is the live threat: claude's own words become text rendered on a remote surface, and this frame carries four such strings. The section names it. Threats 2–8 are untouched — the frame rides the existing Noise-sealed lane, adds no route, no key, no credential and no inbound path, so none of their mitigations is loosened.
- **Out of scope, named:** the fail-closed bounded parse and every reject branch (#1965); the producer, the nonce minting, the per-device answer gate's extension and the unanswered-batch fail-safe (#1927); the client-side render sanitization, which the section assigns to pyrycode-desktop#849 as the render boundary that owes it.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-01
