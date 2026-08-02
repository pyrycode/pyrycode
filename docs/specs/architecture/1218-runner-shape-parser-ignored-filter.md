# 1218 — Compare the two runners over the alphabet the parser actually maps

Ticket: [#1218](https://github.com/pyrycode/pyrycode/issues/1218) · size `s` · one file, **no production code**

## Files to read first

| Path | What to extract |
|---|---|
| `internal/e2e/realclaude/ptyrunner_byte_equivalence_test.go:56-108` | `additiveDriftAllowlist` type + the two one-sided tables. The per-entry trailing-comment-with-citation convention the new table must follow. |
| `…:160-197` | `shapeFilterTypes` (the union) and its only caller `extractShapes`. **The single edit point for the filter.** |
| `…:258-337` | `additiveDriftViolations` — confirm it reads only `expectedStreamRunnerOnly.Events` / `expectedPtyRunnerOnly.Events`. AC2 is "keep it that way". |
| `…:532-578` | `compareShapes` — the four guards AC3 falsifies (`:543`, `:546`, `:549`, `:552`) and the three that survive untouched (last-is-result ×2, last-subtype-agreement). |
| `…:588-642` | `checkInit` — read-only. Proves AC4 needs no edit: it walks **raw bytes**, never shapes. |
| `…:704-808` | `TestAdditiveDriftAssertion_SelfCheck` — the `intersectionBaseline` fixture and the `inject` helper the new sub-tests reuse; the assert-shape (`len(vs)`, `strings.Contains` on identifier + table name) to copy. |
| `internal/streamsup/parser.go:38-73` | `ignoredLineTypes` and the 2026-07-27 measurement comment. **The set being mirrored** — `{system, rate_limit_event}`. |
| `internal/streamsup/parser.go:203-241` | `consumeLine` — the ignored arm returns before emitting. This is what the parity test exercises. |
| `internal/streamsup/parser.go:105-124` | `NewParser(sink func(turnevent.Event), logger *slog.Logger) *Parser` + `Write` — line-buffered, so fixture lines need a trailing `\n`. `logger` nil is fine (Debug only). |
| `internal/agentrun/ptyrunner/runner_test.go:148-168` | `lines[0]` of ptyrunner's raw stdout is asserted `(system,init)`. Evidence for § "The positional claim is not lost". |
| `cmd/pyry/agent_run_test.go:660-694` | `system/init` asserted to precede every `assistant`. Same evidence, verb level. |
| `internal/e2e/realclaude/fixtures.go:373-390` | `parseInitSessionID` scans for the **first matching** init line — position-independent. No consumer indexes init. |
| `internal/e2e/realclaude/interactive_stream_liveness_test.go:229-258` | The existing pre-ship alarm for `ignoredLineTypes` going *too narrow*. Explains why the new parity test only needs to guard the *too wide* direction. |

## Context

`TestPtyRunnerVsStreamRunner_StructuralEquivalence` compares the two runners' stdout as `(type, subtype)` sequences. It is RED because the streamrunner side carries ~10 `system/thinking_tokens` lines per turn and the ptyrunner side carries none. Everything else matches: same init, same two assistant messages, same `result/success`.

The shipped parser (`internal/streamsup`) ignores the whole `system` family **by design**, on a 2026-07-27 measurement: it is claude's catch-all namespace and its highest-rate emitter. So the test fails on exactly the lines production deliberately drops. The equivalence being asserted is real; the alphabet is stale.

The fix is to filter the parser-ignored family out of the SEQUENCE comparison. The tension is that the existing filter mechanism cannot express it.

## Design

### The problem with the existing mechanism

`shapeFilterTypes()` (`:166`) is defined as the union of `expectedStreamRunnerOnly.Events` and `expectedPtyRunnerOnly.Events` — tables documented as "types one runner emits but the other does not". `system` is **not** one-sided: both runners emit `system/init`. Adding it to either table would be a factually false entry, and because `additiveDriftViolations` reads the same tables, the entry would silently pre-authorise a future genuinely one-sided `system` divergence — the exact regression AC2 forbids.

Two concepts are sharing one source of truth:

- **"one runner emits it, the other doesn't, and that's fine"** → governs the SET check *and* the sequence filter.
- **"the shipped parser maps nothing from this type"** → should govern the sequence filter *only*.

### The change: a third table, consulted by the filter only

Add a package-level table alongside the two allowlists. It is the test's mirror of `streamsup.ignoredLineTypes`:

```go
// parserIgnoredTypes mirrors internal/streamsup.ignoredLineTypes: the top-level
// stream-json types the shipped parser drops in silence. Distinct from the two
// additiveDriftAllowlist tables on purpose — those answer "is this one-sided
// emission tolerated?" (SET check + filter); this one answers "does the shipped
// parser map anything from this type?" (filter ONLY). A member here is dropped
// from the SEQUENCE comparison and stays fully policed by additiveDriftViolations.
var parserIgnoredTypes = map[string]struct{}{
	"system":           {}, // #1218: claude's catch-all namespace and highest-rate emitter (~10 thinking_tokens/turn vs 1 init/turn, measured 2026-07-27). Parser ignores the family wholesale; see streamsup/parser.go ignoredLineTypes.
	"rate_limit_event": {}, // #1218: parser-ignored too. Also in expectedStreamRunnerOnly.Events — independently true (streamrunner-only) and that entry still governs the SET check.
}
```

Then `shapeFilterTypes()` unions **three** tables instead of two. One added loop; its only caller (`extractShapes`) is unchanged. `additiveDriftViolations` is **not** touched — that is what makes AC2 hold.

Two decisions worth stating:

- **Mirror the parser's full set, not just the `system` delta.** `rate_limit_event` appearing in two tables is not duplication-by-accident: "streamrunner emits it and ptyrunner doesn't" and "the parser maps nothing from it" are independently true, and each table is consulted for a different question. Carrying the full set means a reader can diff `parserIgnoredTypes` against `parser.go` mechanically, which is what AC1's "matches the parser's `ignoredLineTypes` set" asks for. Set union is idempotent, so the filter is unaffected either way.
- **`map[string]struct{}`, not `map[string]bool`.** Matches the surrounding file, which is the convention a reader of *this* file expects. The value-type divergence from `parser.go` is keys-only and is called out in the comment.

### Data flow after the change

```
raw stdout ──> extractShapes ──> filter = one-sided ∪ one-sided ∪ parser-ignored ──> SEQUENCE ──> compareShapes
           └──> extractEventTypeSet ──> (no filter) ──> additiveDriftViolations ──> SET check
                                                        consults ONLY the two one-sided tables
```

The SET check keeps seeing every `system` line on both sides. If one runner stops emitting `system/init` while the other still does, `system` lands in one side's set and not the other's, is absent from both one-sided tables, and a violation fires naming the type and the table to edit. Filtering the family out of the sequence buys no silence there.

### AC3 — the four falsified guards

Post-filter, the observed intersection is 3 shapes per side: `assistant`, `assistant`, `result/success`. Rewrite as:

| Line | Now | Becomes | Why it is still true and load-bearing |
|---|---|---|---|
| `:543` | `len(stream) < 4` — "want >= 4 (init+user+assistant+result)" | `len(stream) < 2` — "want >= 2 (assistant+result)" | Two empty slices are `reflect.DeepEqual`. Without a floor, a turn where **both** runners produced nothing passes vacuously. It also guards the index accesses below. |
| `:546` | same, pty side | same, pty side | ditto |
| `:549` | `stream[0] == (system,init)` | `stream[0].Type != "assistant"` | The intersection sequence opens with model output. Catches a turn whose first meaningful envelope is a bare `result` (no model output at all) with a sharper message than the floor guard gives. |
| `:552` | same, pty side | same, pty side | ditto |

`>= 2` rather than `>= 3`, deliberately: the two-assistant split is claude-version-dependent (a thinking message plus a text message — see the `maxTurns` comment at `:396-405`), so `>= 3` would be pinning a version artifact. `assistant + result` is the durable floor.

Guards that do **not** change: last-is-`result` on both sides (`:557`, `:560`) and the last-subtype agreement (`:563`). All three remain true post-filter.

### The positional claim is not lost

The ticket flags "init arrives first" as the real cost of the wholesale drop. It is already pinned in four cheaper places, none of which needs a live claude:

1. `internal/agentrun/ptyrunner/runner_test.go:148-168` asserts `lines[0]` of ptyrunner's **raw** stdout decodes to `(system,init)` — strictly stronger than an index-0 check on a post-filter sequence, and driven by a fake helper child.
2. `cmd/pyry/agent_run_test.go:671-688` asserts `system/init` precedes every `assistant` on the verb's stdout.
3. `internal/agentrun/streamjson/emitter.go:110-146` — `New` writes the init envelope **before** returning the `*Emitter`, so nothing can structurally precede it.
4. On the streamrunner side the claim was never pyrycode's to make: streamrunner is a byte-exact passthrough of claude's stdout (`internal/agentrun/streamrunner/watchdog_test.go:162-166`), so init position there is claude's behaviour.

And no consumer depends on the position: `parseInitSessionID` (`fixtures.go:373-390`) scans line-by-line for the first init envelope with a non-empty `session_id`; it never indexes. `checkInit` likewise walks raw bytes and returns on first match.

So dropping the index-0 init guards costs nothing that is not covered deterministically elsewhere, which is why the wholesale drop is the right route.

## Error handling

No new failure modes. `extractShapes` keeps its existing contract — malformed JSON on any line still returns an error and the caller still `t.Fatalf`s. Filtering happens after a successful decode, exactly as today.

## Testing strategy

AC5 requires a live-claude pass, but the mechanism itself is pinned by fixture so a regression does not need an API key to surface. Three additions, all in the same file, all under the existing `e2e_realclaude` build tag.

**1. Filter behaviour — new `TestExtractShapes_FiltersParserIgnoredTypes` (no claude, no network).**

- Fixture: an init line, two `system/thinking_tokens` lines, an assistant line, a `rate_limit_event` line, a `result/success` line.
- Assert the returned sequence has no shape whose `Type` is `system` or `rate_limit_event`.
- Assert the surviving shapes are `[assistant, result/success]` in that order — proves the filter drops only what it should and preserves stream order.
- Negative arm: a shape whose type is in neither the one-sided tables nor `parserIgnoredTypes` (e.g. `assistant`) survives.

**2. SET check unaffected — new sub-tests inside `TestAdditiveDriftAssertion_SelfCheck`.**

Reuse `intersectionBaseline` and the `inject` helper. Two arms, matching the existing sub-test pairing:

- `system_one_sided_streamrunner`: remove the `system/init` line from the **pty** fixture only. Expect exactly 1 violation, naming `system` and `expectedStreamRunnerOnly.Events`.
- `system_one_sided_ptyrunner`: remove it from the **stream** fixture only. Expect exactly 1 violation, naming `system` and `expectedPtyRunnerOnly.Events`.

This is the AC2 pin, and it is the coverage the ticket correctly notes the existing self-check does **not** provide (its baseline carries `system/init` on both sides, so it stays green whichever table a fix touches).

**3. Parity with the shipped parser — new `TestParserIgnoredTypesMatchesStreamsupParser` (no claude, no network).**

Guards the dangerous direction: `parserIgnoredTypes` listing a type the parser actually **maps**, which would silently drop a meaningful envelope from the comparison.

- Table-driven over the keys of `parserIgnoredTypes`, each mapped to a representative line (`{"type":"system","subtype":"thinking_tokens"}`, `{"type":"rate_limit_event"}`). A key with no fixture fails the test, so adding a member forces adding a fixture.
- For each: construct a real `streamsup.NewParser(sink, nil)` where `sink` counts events, `Write` the line plus a trailing `\n`, assert **zero** events. A type the parser maps emits at least one; a type it no longer ignores emits `turnevent.Unrecognized`.
- Adds imports of `internal/streamsup` and `internal/turnevent` to the file. No cycle — `internal/e2e/realclaude` is a leaf test package.

The opposite direction (`ignoredLineTypes` growing past this mirror) already fails loudly in the same pre-ship gate: `interactive_stream_liveness_test.go:229-258` fatals on any `unrecognized_message` during a normal turn, and this very gate is how #1218 itself was found. It needs no new coverage.

**4. Live gate (AC5).** `make e2e-realclaude` against a live claude; `TestPtyRunnerVsStreamRunner_StructuralEquivalence` must **PASS** — a SKIP does not satisfy the AC (`WithWorktreeAuthenticated` skips without `ANTHROPIC_API_KEY`). Paste both observed shape sequences into the PR. Expect 3 shapes per side.

## Out of scope

- No production code changes. The ticket's "Do not" holds: neither runner's output is altered.
- `expectedStreamRunnerOnly` / `expectedPtyRunnerOnly` membership is unchanged. `rate_limit_event` stays where it is.
- `checkInit` and its two call sites (`:499`, `:500`) are untouched (AC4).
- No `docs/knowledge/codebase/1218.md` — the documentation phase owns that after merge.

## Open questions

- **Whether `assistant`-first is the right successor to `init`-first at `:549`/`:552`.** A tool-using turn still opens with an `assistant` envelope (a `tool_use` block lives inside one) and `user` is filtered on both sides, so the claim should hold. The live run under AC5 confirms it against the actual sequence; if it does not hold, drop the two guards rather than weakening them — the length floor and the last-is-`result` guards already carry the structural sanity, and § "The positional claim is not lost" shows nothing unique is lost.
