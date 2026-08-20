# #1543 — Delete the orphaned turnbridge producer + mapper, re-home the package doc onto `outbound.go`

Ships as `size:s`. No split — see § Size check.

## Files to read first

| Path | Symbol | What to extract |
|---|---|---|
| `internal/turnbridge/outbound.go` | the file-header comment block above the `import` group (the *"outbound.go is the mirror of mapper.go"* paragraph) | The block you replace. Note it sits **below** `package turnbridge`, so it is *not* a doc comment today. |
| `internal/turnbridge/outbound.go` | `MapEvent` | **Read to confirm you leave it alone.** Its arms carry eight `producer` / four `streamsup` attributions to the live path. Not one byte changes. |
| `internal/turnbridge/outbound.go` | `inputSummary` | Doc comment ends *"(mirrors rawInput's posture in mapper.go)"* — the parenthetical you delete. |
| `internal/turnbridge/outbound.go` | `resultSummary` | Doc comment opens *"The current inbound producer (mapper.go)…"* — the attribution you re-ground. |
| `internal/turnbridge/producer.go` | the `// Package turnbridge …` doc comment above `package turnbridge` | The doc that dies with the file. Read it to see what the new one must **stop** saying. |
| `internal/streamsup/parser.go` | `toolResultContent` | The live inbound producer of `ToolContent`. Confirms it emits `TextContent` or `nil` only — the claim `resultSummary` re-grounds onto. |
| `internal/streamsup/parser.go` | `rawInput` | Read to see why `inputSummary`'s cross-reference is **dropped**, not re-grounded — this one has no failure branch to mirror. |
| `internal/turnbridge/mapper.go` | `rawInput` | The posture `inputSummary` used to mirror (empty→nil, marshal error→nil). Contrast with the streamsup twin above. |
| `cmd/pyry/interactive_turn_v2.go` | `interactiveTurnEmitterV2.emitMapped`, `interactiveTurnEmitterV2.emit` | The sole consumer. `emit` is what wraps a payload into an Envelope — the live replacement for the header's dead `cmd/pyry/assistant_turn_v2.go` reference (§ Correction 3). |
| `CODING-STYLE.md` | § "Comments — Citing Other Code" | You are writing new comment lines. Name symbols; never write a `:NNN`. `cite-guard` is in `make check`. |
| `docs/knowledge/features/turnbridge-package.md` | § "Files", § "The mapper (`mapEvent`)", § "The follow-active subscriber" | **Read-only context.** Documents the producer at length. Bringing it to the surviving shape is the *documentation phase's* job, not yours — see § Out of scope. |

## Context

#1348 deleted both terminal-driving claude paths. `internal/turnbridge` was left holding a live half (`outbound.go` — the `turnevent.Event` → v2 wire-payload adapter, consumed by `cmd/pyry`) and a dead half (`producer.go` + `mapper.go` — the tui-driver drain/re-subscribe lifecycle and its `tuidriver.Event` → `turnevent.Event` mapper).

The `git rm` is the trivial part. The work is what the deletion strands inside `outbound.go`: the package's **only** doc comment lives on `producer.go` and describes exactly the role being removed, and three comments in `outbound.go` name `mapper.go`.

Both dead files go together, not just `producer.go`. Deleting `producer.go` alone leaves `mapper.go` reachable only from `mapper_test.go`, and `staticcheck`'s `unused` counts test usage as usage — `make check` would stay green over the residue instead of flagging it. (Same shape as the half-deletion trap recorded on #1522.) The `tui-driver` dependency is also imported by both files, so dropping it needs both.

No ADR is warranted: this removes a dead path, it does not decide anything new.

### Deletion proof — re-run at HEAD `4918bb2`

The ticket's proof was taken at `2e33ffe`. Re-run here via `go` overlay (`Replace` mapping each of the four files to `""`, no worktree writes), all green:

- `go build ./...` — OK
- `go vet ./internal/turnbridge/ ./cmd/pyry/` — OK
- `go test -race ./internal/turnbridge/` — `ok … 1.392s` (this is the proof that `outbound_test.go` uses no helper defined in either deleted test file)
- `go vet -tags e2e_realclaude ./internal/e2e/realclaude/` — OK
- `go list -f '{{.Imports}} || {{.TestImports}}'` post-deletion names `tuidriver` in **neither** list; at HEAD it names it in **both**. The control discriminates.

`cmd/pyry` is the sole importer (`go list` confirms; `internal/protocol`, `internal/streamsup`, `internal/turnevent` and `internal/e2e/internal/fakeclaude` mention the path in **comments only**). There is zero out-of-package reference to any `producer.go` or `mapper.go` symbol.

## Corrections to the ticket's measured facts

**#1638 (`ea2efa2`, "map claude's announced model onto the v2 wire") landed on `outbound.go` after the ticket's measurement.** It added a `turnevent.ModelAnnounced` arm to `MapEvent` carrying one more `streamsup` attribution and two more `producer` mentions. Three of the ticket's numbers moved, and taking them literally would false-FAIL a correct implementation. Re-measured at `4918bb2`:

| Ticket says | Actually, at HEAD | Consequence |
|---|---|---|
| `outbound.go` says "producer" **nine** times; **six** are streamsup attributions | **eleven** times; **eight** are streamsup attributions (the two new ones are in the `turnevent.ModelAnnounced` arm) | More to preserve, nothing more to change |
| AC 4: `grep -c 'streamsup' outbound.go` is still **3** | **4** at HEAD, and a correct implementation takes it to **5** (§ Design step 3c adds one) | AC 4's grep is wrong in both directions. Use the § Verification check instead. |
| `outbound_test.go` has **three** "producer" mentions | **five** | Cosmetic — the AC only requires that file unmodified, which still holds. |

Line numbers throughout the ticket body are likewise shifted by ~38 lines from `resultSummary` onward (`:276` → `inputSummary`, `:289`/`:292`/`:294` → `resultSummary`). This spec names symbols instead; resolve by symbol, not by the ticket's line numbers.

**Correction 3 — a stranded reference the ticket does not mention.** The header block you are replacing ends *"See cmd/pyry/assistant_turn_v2.go for the consumer shape that wraps a payload into an Envelope."* **`cmd/pyry/assistant_turn_v2.go` does not exist.** The live consumer is `interactiveTurnEmitterV2.emit` in `cmd/pyry/interactive_turn_v2.go`. Do not carry the dead path forward into the new package doc.

**AC 3's `MapEvent` control is safe as written, but only because of a design choice made here.** At HEAD `grep -c '\bMapEvent\b' outbound.go` is 4, one of which lives in the header block being deleted. The new package doc is specified to name `MapEvent` and `BuildTurnState` (§ Design step 2), which holds the count at 4 — and is better package-doc practice anyway. `grep -c '\bmapEvent\b'` goes 1 → **0**.

## Design

Four deletions and three comment edits in one file. No code changes, no signature changes, no new symbols.

### Step 1 — delete four files

`git rm internal/turnbridge/{producer.go,producer_test.go,mapper.go,mapper_test.go}`

`internal/turnbridge` is then exactly `outbound.go` + `outbound_test.go`.

### Step 2 — write the package doc onto `outbound.go`

Today `outbound.go` line 1 is a bare `package turnbridge`, and the descriptive block sits *below* the `import` group — so it is a free-floating comment, not a doc comment. Merge the two: the new doc goes **directly above `package turnbridge`**, and the old free-floating block is removed entirely. One comment, not two.

It must carry forward the surviving substance of the old block — the "pure value-to-value adapter, none of I/O · state · envelope-ID minting · clock read · sealing" posture and why that seam exists — while replacing the *"mirror of mapper.go"* opener with a statement that stands on its own.

A draft; adopt or improve, but every invariant below must hold:

```go
// Package turnbridge adapts the neutral internal turn-event model
// (internal/turnevent, #606) OUT to the v2 interactive wire payloads (#607):
// MapEvent shapes one turnevent.Event into a typed payload, BuildTurnState
// shapes the turn_state payload the lifecycle machine drives. It is a pure
// value-to-value adapter — no I/O, no state, no envelope-ID minting, no clock
// read, no sealing. Every one of those belongs to the consumer (the
// turn-lifecycle integration slice); keeping them out is what makes this
// table-testable and isolates it from the lifecycle state machine. See
// cmd/pyry's interactiveTurnEmitterV2.emit for the consumer shape that wraps a
// payload into an Envelope.
package turnbridge
```

Invariants:

- Starts `// Package turnbridge` and sits immediately above `package turnbridge` in `outbound.go`, with no blank line between.
- Describes the **outbound** adapter. Says nothing about draining a tui-driver `Events()` stream, a producer half, or a consumer half that attaches `OnEvent`.
- Names `mapper.go`, `producer.go` and `mapEvent` **nowhere**.
- Names both exported entry points (`MapEvent`, `BuildTurnState`).
- Points at a consumer symbol that **exists** — not `assistant_turn_v2.go`.
- No `:NNN` citation of any form (`cite-guard` runs in `make check`).

### Step 3 — the three `mapper.go` sites in `outbound.go`

**3a. The header block** — removed by step 2. Nothing separate to do.

**3b. `inputSummary`'s doc comment — delete the cross-reference, do not re-ground it.**

Drop the trailing parenthetical `(mirrors rawInput's posture in mapper.go)`. The sentence it hangs off already states the posture in full: *"RawInput is best-effort/opaque (#606), so a malformed blob is a précis-less tool_use, not an error."*

Why deletion rather than re-grounding onto `internal/streamsup`'s same-named `rawInput`: the mirror was true of the **deleted** `rawInput`, which had a `marshal error → nil` branch matching `inputSummary`'s `compact error → ""`. The streamsup twin carries claude's bytes through opaquely and **has no failure branch at all**, so "mirrors its posture" would be false of precisely the malformed-input claim the parenthetical is attached to. Re-grounding here would trade a dead reference for a wrong one.

**3c. `resultSummary`'s doc comment — re-ground, do not blank.**

The claim survives the deletion in substance; only its attribution dies. Verified at HEAD: `internal/streamsup`'s `toolResultContent` returns `turnevent.TextContent` or `nil` and nothing else, and `DiffContent` / `TerminalContent` have zero constructors anywhere outside `internal/turnevent`'s own compile-time assertions and tests plus `outbound_test.go`'s `resultSummary` table. So the sentence stays true — swap `(mapper.go)` for the live producer:

- *"The current inbound producer (mapper.go)"* → *"The live inbound producer (`internal/streamsup`'s `toolResultContent`)"*, or equivalent.
- Everything after the semicolon is untouched text, including *"until a producer (e.g. the ACP adapter #600)"*.

Naming another package's unexported symbol in a comment is established house style here — `MapEvent`'s arms already name `streamsup`'s `maxTaskFieldID`, `minThinkingTokensPerEvent`, `emitRateLimit`, `maxRateLimitField` and `maxModelField`.

The edit reflows the enclosing sentence, so the *"until a producer (e.g. the ACP adapter #600)"* clause may land on a different line than it does today. AC 4's "survive unedited" is about the **text**, not the line — the clause is part of the same semicolon-joined sentence as the stale attribution, so a reflow is unavoidable and is not a violation.

### What must not change

- **`func MapEvent`** — not one byte, from `func MapEvent(` through its closing brace. All eight `producer` attributions and all four `streamsup` attributions live inside it and document live-path invariants of `internal/streamsup`. This is the single highest-value thing to get right: the ticket's stale "sweep the word producer" trap would destroy accurate documentation.
- **`BuildTurnState`, `truncate`, `TurnContext`, `TurnState`, the `State*` constants, `maxSummaryLen`** — untouched.
- **`outbound_test.go`** — must not appear in `git diff --name-only main`. It names no deleted symbol, so the deletion strands nothing in it.
- **Everything outside `internal/turnbridge`.** Five other packages carry comments naming the deleted symbols (`cmd/pyry/interactive_turn_v2.go`, `cmd/pyry/relay.go`, `internal/streamsup/parser.go` ×4, `internal/turnevent/event.go`, `internal/e2e/realclaude/interactive_per_conversation_liveness_test.go`). **#1544 owns every one of them** — it is natively blocked by this ticket. Fixing one here creates the merge conflict the split exists to avoid.

## Concurrency model

None. The surviving package is a pure synchronous value-to-value adapter with no goroutines, no channels and no shutdown sequence. Deleting `Producer` removes the only concurrent machinery the package ever had — this ticket *reduces* the concurrency surface to zero.

## Error handling

Unchanged. `MapEvent`'s `(typ, payload, ok)` contract, `inputSummary`/`resultSummary`'s best-effort `""` returns, and the `default: return "", nil, false` arm all survive byte-identical. No new failure mode is introduced or removed.

## Testing strategy

**No new tests, and no test edits.** The change is four deletions plus comments in one file; there is no behaviour to assert that `outbound_test.go` does not already cover. Adding a test here would be pinning a comment.

The deletion's safety is proven by existing gates, in this order:

1. **`go test -race ./internal/turnbridge/` is green** — the load-bearing check. `mapper_test.go` and `producer_test.go` between them held ~1100 lines of test code; CLAUDE.md's rule is that deleting a test file takes its shared helpers with it. A green race run over `outbound_test.go` alone is the direct proof that no surviving test depended on a helper that just vanished. (Already confirmed by overlay at HEAD; re-confirm on the real tree.)
2. **`go list -f '{{.Imports}} {{.TestImports}}' ./internal/turnbridge` names `github.com/pyrycode/tui-driver/pkg/tuidriver` nowhere.** Control: at `main` it appears in *both* lists, so an absence here is a real absence rather than a vacuous one.
3. **`make check`** — includes `vet`, race tests, `staticcheck`, `substrate-guard`, **`cite-guard`**, and the fake-claude e2e suite. `staticcheck`'s `unused` is what would have flagged a `producer.go`-only deletion; deleting both files means there is no residue for it to miss. `cite-guard` is diff-scoped and will reject any `:NNN` in the comments you write.
4. **`go vet -tags e2e_realclaude ./internal/e2e/realclaude/`** — run explicitly. `make check` never compiles that package, so its green says nothing about it; it can fail to build while every `make check` passes honestly.

The `needs-real-claude` label puts `make preship` (and with it the live suite) on this ticket, per CLAUDE.md's test-deletion rule. That is the pipeline's gate, not a developer deliverable. If you do read a live-suite result, read the count of `=== RUN` lines — a healthy full run is in the 700s and reads zero when the build is broken; the exit code is 0 in both the healthy and the broken case.

### Verification checks — use these, not AC 4's greps

AC 4's `grep -c 'streamsup' … is still 3` is stale in both directions (§ Corrections). Substitute these, which are deterministic and do not depend on a count that #1638 already moved once:

- **`git diff main -- internal/turnbridge/outbound.go` contains exactly three hunk regions:** the top of the file (doc added above `package turnbridge`, free-floating block removed), `inputSummary`'s doc comment, and `resultSummary`'s doc comment. **No hunk falls inside `func MapEvent`.** This is the real form of AC 4 — it pins the eight `producer` and four `streamsup` attributions byte-identical without counting anything.
- `git diff --name-only main` lists `internal/turnbridge/outbound.go` and the four deletions, and **not** `internal/turnbridge/outbound_test.go`.
- `grep -c '\bmapEvent\b' internal/turnbridge/outbound.go` → **0** (was 1). Case-sensitively; a case-insensitive grep false-FAILs on the exported `MapEvent`.
- `grep -c '\bMapEvent\b' internal/turnbridge/outbound.go` → **4** (unchanged — the doc comment in step 2 names it, replacing the header block's mention).
- `grep -n 'mapper\.go\|producer\.go' internal/turnbridge/outbound.go` → no output.
- `grep -o 'producer' internal/turnbridge/outbound.go | wc -l` → **11**, unchanged. Nothing is swept: the one stale site is re-grounded, and "The live inbound producer …" still contains the word.
- `grep -c 'streamsup' internal/turnbridge/outbound.go` → **5** (4 at HEAD, +1 from step 3c). Do **not** assert 3.
- `ls internal/turnbridge/` → exactly `outbound.go`, `outbound_test.go`.

Use plain `grep`, not `git grep -E` — POSIX ERE has no `\b`, so `git grep -E '\bMapEvent\b'` matches nothing and reads as a clean absence.

## Out of scope

- **`docs/knowledge/features/turnbridge-package.md`** documents the producer at length (file map, `NewTargetSubscriber` section, API listing). Bringing it to the surviving shape is the **documentation phase's** job. It is not a developer deliverable and must not be treated as one.
- **The five other packages' comments** naming deleted symbols — **#1544**, natively blocked by this ticket.
- **`turnevent.Stall`'s producer count.** `mapEvent`'s `EventKindStallDetected` arm is the only producer of `turnevent.Stall` in the repo; once it is gone, `outbound.go`'s `case turnevent.Stall:` arm, `cmd/pyry`'s two `case turnevent.Stall:` arms and `internal/protocol`'s stall payload are all unreachable. **Leave every one of them.** `internal/protocol` is a mobile wire surface and retiring an event family is a separate decision; #1544 records the fact where `internal/turnevent/event.go` currently asserts the opposite.

## Size check

Re-counted against this written spec, not the sketch:

| Boundary | Limit | This spec |
|---|---|---|
| Production source files created or modified | ≤ 3 | **3** — `outbound.go` modified, `producer.go` + `mapper.go` deleted |
| Total written work | ≤ 400 | **~25** — a ~10-line doc comment, one parenthetical deleted, one clause re-grounded, plus this spec |
| New exported types or interfaces | ≤ 5 | **0** |
| Consumer call sites needing simultaneous update | ≤ 10 | **0** — zero out-of-package references to any deleted symbol; `cmd/pyry` uses only `outbound.go` symbols |
| Acceptance criteria | ≤ 5 | **5** |
| Distinct error/reject branches | ≤ 10 | **0** |

Not refactor-shaped: nothing is renamed, no signature changes, no type is replaced, no import flips anywhere but inside the deleted files. Ships as one `size:s` ticket.

## Open questions

- **None blocking.** The one judgment call — delete vs. re-ground `inputSummary`'s cross-reference — is decided in § Design step 3b with its reason, because re-grounding onto the streamsup twin would assert something false of it.
- **For code review:** three of the ticket body's numbers are stale (§ Corrections). AC 4's `grep -c 'streamsup' … is still 3` will not hold on a correct implementation; the § Verification block above is the substitute. A comment recording this is posted on the issue.
