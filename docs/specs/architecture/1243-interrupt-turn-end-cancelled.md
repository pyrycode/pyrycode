# Spec #1243 — An interrupted PTY turn reports `turn_end` with reason `cancelled`

**Ticket:** [#1243](https://github.com/pyrycode/pyrycode/issues/1243) · **Size:** S · **Labels:** `bug`, `security-sensitive`, `needs-real-claude`

## Files to read first

| Path | What to extract |
| --- | --- |
| `internal/turnbridge/mapper.go:56-91` | `mapEntry` — `case "user"` is the **single production edit site**. Note the branch order: `ParseToolResult` matches first, which is what keeps AC2 true for free. |
| `internal/turnbridge/mapper.go:21-31` | `mapEvent`'s `EventKindJsonlEndOfTurn` → `TurnEnd{end_turn}` — today's only turn-end source. **Untouched** by this change; that is AC4. |
| `internal/turnbridge/mapper.go:103-122` | `thinkingText` — the exact shape the new text extractor mirrors (nil-`Message` guard, block loop, `c.Raw["…"].(string)` read). Do **not** refactor it; see § Declined alternatives. |
| `internal/turnbridge/mapper_test.go:12-47` | `entry()` — marshals only `{type, message}` and leaves `Raw` nil. It **cannot** express a top-level sibling; a second builder is needed, not a change to this one. |
| `internal/turnbridge/mapper_test.go:66-219` | `TestMapEvent` table — where the new rows go. Row at `:195-197` ("drop user text (no tool_result)") stays as-is and still drops. |
| `internal/turnevent/taxonomy.go:37-43` | `TurnEndReasonCancelled` already exists. **No taxonomy change.** |
| `internal/turnevent/taxonomy_test.go:87-95` | The exactness assertion (`len == 5`, `reflect.DeepEqual`) that a new reason would break. |
| `internal/turnevent/event.go:65-72` | `TurnEnd` carries `Reason` and nothing else — no marker text reaches the wire. |
| `internal/turnbridge/outbound.go:87-91` | `StopReason: string(e.Reason)` — verbatim pass-through, no allowlist. Nothing downstream needs changing for `cancelled`. |
| `internal/streamsup/parser.go:281-292` | `resultTurnEndReason` — the peer mapping this must agree with (`error_during_execution` → `cancelled`). |
| `cmd/pyry/interactive_turn_v2.go:207-216` | The `inTurn` guard that absorbs an out-of-turn `TurnEnd` (`interactive_turn.turn_end_no_turn`). Bounds the blast radius of a false positive fired while idle. |
| `internal/agentrun/jsonl/testdata/no_end_turn.jsonl:53` | **The only real interruption marker line in the repo** (547 bytes). AC5 arm (a) source. |
| `internal/agentrun/jsonl/testdata/no_end_turn.jsonl:50` | Real `tool_result` user entry (1280 bytes; `is_error:false`, `toolUseResult.interrupted:false`). Base for the AC2 and the tool-output-forge fixtures. |
| `internal/agentrun/jsonl/testdata/no_end_turn.jsonl:3` | Real prompt entry — **34,400 bytes; do not embed it whole.** Only its top-level envelope (incl. `permissionMode`) is needed, for the AC3 forge. |
| `internal/agentrun/ptyrunner/runner_test.go:81-86` | Precedent for a verbatim raw-JSONL line as a test `const` backtick string. |
| tui-driver `pkg/tuidriver/jsonl.go:426-466` | `parseEntry` / `parseMessage` — the population contract the new test builder must mirror: `Raw` **and** `RawLine` always set; `Message.Content` populated **only** when `message.content` is a JSON array (a string content yields nil `Content`). |
| tui-driver `pkg/tuidriver/jsonl.go:78-115` | `JSONLEntry` doc: *"Consumers requiring presence-vs-absence semantics check `_, ok := e.Raw["message"]` directly."* — the blessed idiom for the authorship gate. |
| `docs/specs/architecture/1191-minted-perconv-interrupt-oracle.md` | Prior art: documents that `EventKindJsonlEndOfTurn` can only ever say `end_turn`, so #1191's oracle deliberately could not assert `cancelled`. |
| `docs/protocol-mobile.md:1023-1030` | Threat #1 (prompt injection) — the frame for AC3 and § Security review. |

## Context

On the PTY/JSONL path an interrupt genuinely stops the turn (claude cancels in ~65 ms and records it) but nothing turns that into a `turn_end`. The client's Stop affordance stays mounted until its own 120 s timeout. `turn_end` on this path has exactly one source — `mapEvent`'s `EventKindJsonlEndOfTurn` case — and that event only fires on `assistant` + `stop_reason=="end_turn"` + non-empty text, none of which an interrupted turn produces. The stream-json runner already reports the same user action as `cancelled` (`internal/streamsup/parser.go:281-292`); the two surfaces disagree.

Not production-affecting today (production runs `interactive_runner: stream-json` since the 2026-07-24 cutover) — this restores the rollback guarantee.

The entry the fix keys on **does** reach the mapper: `mapEntry`'s `case "user"` sees it and drops it because `ParseToolResult` doesn't match a text block. This is a mapping gap, not a delivery gap.

## Design

### The design question

The ticket names it correctly: *what is the discriminator?* Two things must be decided independently, and conflating them is what makes this hard:

1. **"Did an interruption happen?"** — the only signal claude emits is the marker prose. There is no structural field for it (see § Declined alternatives).
2. **"Who wrote this entry?"** — claude, or a client whose prompt text happens to quote the marker. This half **is** structural.

The design is the conjunction: **a `user`-type entry ends the turn as `cancelled` iff it is claude-authored AND its text begins with the interruption-marker sentinel.** The prose answers (1); a structural authorship gate answers (2). AC1 rests on the prose half; AC3 rests on the structural half. Neither half alone is sufficient, and it matters which half carries which AC — see the failure-mode table.

### Evidence for the authorship gate

The ticket calls `permissionMode` *"a single-observation contract, not a discriminator"*. That was measured against one transcript. The repo tracks **three** full session transcripts, and a live scan adds 68 more observations. Re-counted at spec time:

**In-repo (`git ls-files '*.jsonl'`, 3 full transcripts, 2 claude versions, 3 sessions, 2 entrypoints):**

| Entry class | Count | `permissionMode` |
| --- | --- | --- |
| Genuine prompt (`clean.jsonl:3`, `double_end_turn.jsonl:3`, `no_end_turn.jsonl:3`) | 3 | **present** (`"default"`), 3/3 |
| `tool_result` user entries | 33 | absent, 33/33 |
| Interruption marker (`no_end_turn.jsonl:53`) | 1 | absent |

The four remaining tracked user entries (`internal/contextwindow/testdata/*.jsonl`, `internal/agentrun/streamjson/testdata/captured_run.jsonl`) are trimmed to `{type, message}` and carry no top-level siblings — they neither support nor refute, exactly as the ticket said.

**Live scan (design-confidence measurement, not a citable fixture — 60 most-recently-modified `~/.claude/projects/**/*.jsonl` on this machine, 2026-07-29):**

| Entry class | Count | `permissionMode` |
| --- | --- | --- |
| Genuine prompt, `content` a **string** | 30 | present, 30/30 |
| Genuine prompt, `content` a **text-block array** | 30 | present, 30/30 |
| claude-authored `isMeta:true` skill injections (`sourceToolUseID` present) | 8 | absent, 8/8 |

Zero counterexamples in either direction across 105 observations.

Two consequences drive the design:

- **`permissionMode` is a usable authorship gate.** 63 genuine prompts carry it; 42 claude-authored entries don't. It is not a single observation.
- **Content shape is NOT a discriminator, and a test that leans on it is asserting its own fixture.** 30 of 60 real prompts arrive as text-block arrays — the same shape as the marker. This is precisely the trap AC3 warns about: if the forge fixture used a plain-string content, it would be rejected for the wrong reason and the real forge (a prompt with an attachment, quoting the marker) would still work. § Testing pins the forge fixture to block content for exactly this reason.

Reproduce the live scan (it is a measurement, so it should be re-runnable, not trusted):

```bash
python3 - <<'PY'
import json,glob,os,collections
files=sorted(glob.glob(os.path.expanduser('~/.claude/projects/*/*.jsonl')),key=os.path.getmtime,reverse=True)[:60]
c=collections.Counter()
for f in files:
    for l in open(f,errors='replace'):
        if '"type":"user"' not in l: continue
        try: o=json.loads(l)
        except Exception: continue
        if o.get('type')!='user': continue
        body=(o.get('message') or {}).get('content')
        blocks=isinstance(body,list) and all(isinstance(b,dict) and b.get('type')=='text' for b in body)
        if not (isinstance(body,str) or blocks): continue
        c[('permissionMode' in o, 'blocks' if blocks else 'str', o.get('isMeta',False))]+=1
print(c)
PY
```

### Contract

One new branch in `mapEntry`'s `case "user"`, **after** the existing `ParseToolResult` branch:

```go
// mapper.go, case "user", after the ParseToolResult branch:
if isInterruptMarker(e) {
    return turnevent.TurnEnd{Reason: turnevent.TurnEndReasonCancelled}, true
}
```

Three unexported helpers and one constant, all in `mapper.go`:

- `const interruptMarkerSentinel = "[Request interrupted by user"` — a **prefix**, not an exact string. Covers both observed variants (`…user]` on 2.1.128, `…user for tool use]` on 2.1.220) and any future suffix without a code change. The whole treadmill surface is this one line.
- `func isInterruptMarker(e tuidriver.JSONLEntry) bool` — true iff `!userAuthored(e)` **and** `strings.HasPrefix(strings.TrimSpace(userText(e)), interruptMarkerSentinel)`. **Evaluate the gate first**: it is a map lookup, and short-circuiting means a 34 KB prompt body is never concatenated. Asserted by the AC3 forge row and the AC1 marker rows.
- `func userAuthored(e tuidriver.JSONLEntry) bool` — `_, ok := e.Raw["permissionMode"]; return ok`. Presence-only; the value is never read. Doc comment must state the invariant it rests on (claude writes `permissionMode` on user-authored prompt entries and on nothing else) and the census above, so a future claude upgrade that drops the field is diagnosable rather than mysterious.
- `func userText(e tuidriver.JSONLEntry) string` — concatenation of the `"text"` field of every `type=="text"` block on `e.Message.Content`; `""` on a nil `Message`. Same shape as `thinkingText` (`mapper.go:109-122`).

**Why `e.Raw` and not a re-parse of `e.RawLine`:** `Raw` is populated for every tailed entry by the same `parseEntry` call that fills `RawLine` (tui-driver `jsonl.go:436`), the `JSONLEntry` doc names `Raw` as *the* idiom for presence-vs-absence, and it costs no second parse of a line that can be 34 KB. A caller-constructed entry may have a nil `Raw` (the struct contract, not an invariant) — a nil map lookup returns `ok == false`, so such an entry reads as claude-authored and is then held out by the prose gate alone. State this posture in the doc comment; it is the behaviour every existing synthetic row in `mapper_test.go` already has.

### Branch order is load-bearing

```
mapEntry, e.Type == "user"
  ├─ ParseToolResult(e.RawLine) != nil  → ToolUpdate{completed|failed}   ← AC2, and the tool-output forge defence
  ├─ isInterruptMarker(e)               → TurnEnd{cancelled}             ← AC1
  └─ otherwise                          → (nil, false)                   ← AC3, and today's behaviour for everything else
```

The first branch is why AC2 needs no new logic: an `is_error:true` `tool_result` is still a `tool_result`, matched and returned before the marker check runs. It is also the reason attacker-controlled *tool output* can never reach the prose matcher — a `cat` of a file containing the marker string is a `tool_result` entry, not a text entry. Both are proven by rows, not by argument (§ Testing rows 3 and 6).

### Declined alternatives

- **`toolUseResult.interrupted`** — reachable via `e.Raw`, but all 33 tracked occurrences are `false`, so nothing confirms it ever goes true. Decisive objection: it lives on the *`tool_result`* entry, so it cannot see the no-tool-in-flight shape (`[Request interrupted by user]`) at all — it covers at most half of AC1.
- **Absence of `permissionMode` alone** — non-starter: all 33 tracked `tool_result` entries also lack it, so this would end the turn on every tool result. The gate is only meaningful in conjunction.
- **Content shape (block array vs string)** — refuted by measurement: 30 of 60 live prompts use block arrays. It looks structural and isn't.
- **Positional / adjacency rules ("the entry after the tool_result")** — refuted in the one real transcript: lines 50→53 run `tool_result` → `last-prompt` → `ai-title` → marker, and neither middle type reaches `mapEntry`, so "the previous entry" means two different things depending on which stream you read.
- **Extracting a shared `blockText(e, blockType, field)` helper across `thinkingText`/`userText`** — declined on scope discipline. It touches working code for a 6-line saving and widens the diff a security-sensitive review has to cover. Note the near-duplication in `userText`'s doc comment; leave the refactor to whoever adds a third caller.
- **Adding a new `TurnEndReason`** — forbidden: `cancelled` already exists and `taxonomy_test.go:87` asserts the set exactly.

## Concurrency model

None. `mapEvent`/`mapEntry` and all three new helpers are pure functions over a value, called on the producer goroutine at `producer.go:157`. No new goroutines, no shared state, no locks, no ordering constraints, nothing to shut down. The turn-lifecycle mutation happens where it already does, in `cmd/pyry/interactive_turn_v2.go`'s `case turnevent.TurnEnd`.

## Error handling / failure modes

| Failure | Behaviour | Direction |
| --- | --- | --- |
| claude renames the marker prose (`[Request cancelled by user]`, …) | `isInterruptMarker` stops matching; no `turn_end` fires | **Fail-safe** — degrades exactly to today's bug, never to a spurious turn end. One `const` to update. |
| claude stops writing `permissionMode` on prompts | Authorship gate opens; a prompt quoting the marker verbatim can end its own turn | **Fail-open — the only degrading direction.** Named in § Security review; the doc comment on `userAuthored` must say so. |
| Marker arrives while no turn is live | `TurnEnd` dropped + debug-logged at `cmd/pyry/interactive_turn_v2.go:209-213` (`interactive_turn.turn_end_no_turn`) | Harmless |
| Two markers for one interrupt | First ends the turn; second hits the same `inTurn` guard | Harmless |
| Marker fires mid-turn on a turn that continues | Stop retracts early; later output lands outside a turn | The damaging case. Bounded by the conjunction: it needs a claude-authored `user` text entry whose text starts with the sentinel. The only observed claude-authored `user` text class is `isMeta` skill injections (8 observed), held out by the prose gate — row 5 proves it. |
| `e.Raw` nil (caller-constructed entry) | Reads as claude-authored; prose gate is then the sole gate | Matches every existing synthetic test row; documented, not silent |

No new error values, no wrapping, no logging added — `mapEvent` is pure and takes no logger, and the emission is already observable at the `turn_end` wire event.

## Testing strategy

### Fixture provenance (AC5)

Every fixture is a `const` backtick string in `mapper_test.go` (precedent: `internal/agentrun/ptyrunner/runner_test.go:81-86`), each with a comment stating its arm. **There is no third arm** — a constructed line presented as captured is what AC5 exists to prevent.

| Fixture | Arm | Provenance |
| --- | --- | --- |
| `markerLine` | **(a) verbatim** | `internal/agentrun/jsonl/testdata/no_end_turn.jsonl:53`, byte-for-byte (547 bytes). |
| `markerLineToolUse` | **(b) derived** | Base `no_end_turn.jsonl:53`; `message.content[0].text` substituted with `[Request interrupted by user for tool use]`, sourced from the live 2.1.220 daemon-log evidence quoted in issue #1243 (21:33:54.939, run 1). No raw line exists in-repo — the live rig deletes its temp `HOME`. |
| `forgedPromptLine` | **(b) derived** | Base `no_end_turn.jsonl:3` (the real prompt); its `message` replaced verbatim by `no_end_turn.jsonl:53`'s `message` (line 3's own 34 KB body is dropped, not quoted; both entries carry `"role":"user"`, so this is exactly AC3's "only `message.content` swapped"). **Equivalently: `markerLine` plus `"permissionMode":"default"`** — the value quoted from line 3. Differs from `markerLine` only in that one key, plus `uuid`/`timestamp`/`parentUuid` values no rule reads. |
| `toolResultErrorLine` | **(b) derived** | Base `no_end_turn.jsonl:50`; `message.content[0].is_error` flipped `false`→`true`. `toolUseResult.interrupted` left `false` — this is an ordinary failing command, not an interrupted one. Cannot occur naturally in a tracked fixture. |
| `toolResultMarkerLine` | **(b) derived** | Base `no_end_turn.jsonl:50`; `message.content[0].content` substituted with the marker text quoted from `no_end_turn.jsonl:53`. Models attacker-controlled tool output quoting the marker. |
| `metaInjectionLine` | **(b) derived** | Base `no_end_turn.jsonl:53`; `permissionMode` still absent, `"isMeta":true` and `"sourceToolUseID"` added and `message.content[0].text` substituted with ordinary skill text — shape and values sourced from the live-scan class recorded in § Evidence (8 occurrences, reproduce command above). |

**The `forgedPromptLine` construction is the AC3 test's whole point.** It must differ from `markerLine` in `permissionMode` and nothing else that any rule reads. If a developer builds it with a plain-string content instead, the row passes because `userText` finds no blocks — the test then asserts its own construction while the real forge (an attachment-bearing prompt, block content, quoting the marker) still works.

### New builder

`func entryFromLine(t *testing.T, line string) tuidriver.JSONLEntry` — mirrors tui-driver's `parseEntry`/`parseMessage` (`jsonl.go:426-466`): unmarshal into `Raw`, set `Type` from `raw["type"]`, populate the typed `Message` (`ID`/`StopReason`/`Content` blocks with per-block `Raw`) when `raw["message"]` is an object, set `RawLine` to the raw bytes, `t.Fatalf` on invalid JSON. **Do not change `entry()`** — its 13 existing call sites stay untouched; it simply cannot express a top-level sibling, which is why this builder exists.

### Rows (added to `TestMapEvent`)

| # | Scenario | Input | Expect | On `main` |
| --- | --- | --- | --- | --- |
| 1 | Interrupt, no tool in flight | `entryFromLine(markerLine)` | `TurnEnd{Cancelled}`, ok | **RED** (drops) |
| 2 | Interrupt with a tool in flight (2.1.220 prose) | `entryFromLine(markerLineToolUse)` | `TurnEnd{Cancelled}`, ok | **RED** (drops) |
| 3 | Ordinary tool failure keeps the turn open (AC2) | `entryFromLine(toolResultErrorLine)` | `ToolUpdate{ToolCallID:"toolu_01LnKvozACwLtXeHyFGfuQvq", Status: failed, Content: TextContent{…}}`, ok | green (guard) |
| 4 | A prompt cannot forge a turn end (AC3) | `entryFromLine(forgedPromptLine)` | `(nil, false)` | green (guard) |
| 5 | claude-authored non-marker `user` text still drops | `entryFromLine(metaInjectionLine)` | `(nil, false)` | green (guard) |
| 6 | Tool output quoting the marker cannot end the turn | `entryFromLine(toolResultMarkerLine)` | `ToolUpdate{…, Status: completed}`, ok | green (guard) |

Rows 3–6 are green on both sides by design. Their value is directional: each goes red the moment the discriminator is built wrong (keyed on the error flag → 3 fails; prose-only → 4 fails; authorship-only → 3 and 5 fail; marker check placed before `ParseToolResult` → 6 fails). Say this in a comment above the block so a reviewer isn't left guessing why a "new" row passes on `main`. The table as a whole is red on `main` via rows 1–2, satisfying AC5.

The existing rows stay unchanged — in particular `:138-142` (`end of turn -> TurnEnd{end_turn}`, AC4) and `:195-197` (`drop user text`, whose `"a cancel marker"` text still drops).

`go test -race ./internal/turnbridge/... ./internal/turnevent/...` must pass; `taxonomy_test.go` must be untouched and still green (proof no reason was added).

### Live gate (operator-run, cross-repo)

In `pyrycode-desktop`, `e2e/real-claude-interrupt.spec.ts` with `test.use({ interactiveRunner: 'pty' })` and `PYRY_E2E_DAEMON_LOG` set must pass, where it currently fails at `expect(interruptButton).toHaveCount(0)` after 120 s (twice out of two). This is the `needs-real-claude` half of AC5 and cannot run in CI or in the developer's worktree. The developer reports it as **not run** rather than assumed; the operator runs it against a build of this branch. The spec pins `pty` only for the gate — do not land a change to the spec file's default runner.

## Open questions

1. **`permissionMode` upgrade tripwire.** Nothing fails loudly if a future claude stops emitting the field on prompts — the forge silently becomes possible again. A cheap tripwire (a hermetic assertion over a checked-in prompt line, or an assertion in the live gate) is worth its own ticket; deliberately not built here, because it would need a fixture-refresh mechanism this repo doesn't have.
2. **Does an interrupt without any marker exist?** The two live runs both produced one. If a shape exists where claude cancels and writes nothing, no mapping fix can see it and the answer is a timeout at a higher layer. No evidence for it; not designed against.
3. **#1244** (fake-tier PTY oracle, natively blocked by this ticket) locks the behaviour in against regression. Nothing here needs to anticipate it beyond emitting the reason string `cancelled` that it will assert.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No MUST FIX. The boundary is the session transcript: claude's JSONL interleaves claude-authored records with verbatim echoes of untrusted, client-authored prompt text, and this change makes the daemon read a *control signal* out of that stream for the first time. The boundary is explicit and single — `isInterruptMarker` in `mapper.go` is the only place that decides "claude said this", and `userAuthored` is the only place that decides authorship. Downstream holds a `turnevent.TurnEnd` carrying a `Reason` and no transcript-derived bytes (`event.go:70-72`), so nothing untrusted crosses further.
- **[Threat model alignment]** No MUST FIX. `docs/protocol-mobile.md:1025` threat #1 (prompt injection, *severity: high, mitigation: partial*) is the parent class: phone text becomes user-role input to claude. This ticket adds a *reflected* variant — phone text round-trips into the transcript and is re-read by the daemon — and closes it with the authorship gate rather than leaving it to the prose. AC3 is the regression test for exactly this threat. The parent threat (malicious prompts being malicious) is unchanged and remains partially mitigated as documented.
- **[Trust boundaries — attacker-controlled tool output]** No MUST FIX, covered by design. Tool output is more attacker-controllable than prompt text (a fetched page or a `cat`ted file can contain anything). It cannot reach the prose matcher because `ParseToolResult` matches first and returns. This is load-bearing branch order, not incidental — pinned by test row 6, and the doc comment on the new branch must say why it sits *after* the `tool_result` branch.
- **[Trust boundaries — residual, accepted]** SHOULD FIX (accepted risk, no code change). A claude-authored `user` text entry whose text begins with the sentinel ends the turn. Beyond the marker itself, the only such class observed in 105 entries is `isMeta` skill injection (row 5). A local slash-command whose stdout began with `[Request interrupted by user` would trip it — self-inflicted, requires the user's own command, and the worst outcome is a Stop button retracting early on the operator's own machine. Not defended against; named here so a future observation escalates it rather than rediscovers it.
- **[Trust boundaries — fail-open direction]** SHOULD FIX (documentation, not code). The gate rests on claude continuing to write `permissionMode` on prompt entries. If that stops, the design fails **open** (forgery possible) rather than closed. 63/63 observations support it today. Mitigation is the doc comment on `userAuthored` naming the invariant + Open question 1's tripwire ticket. Not a MUST FIX: no alternative structural authorship signal exists in the transcript (censused above), and the fallback — prose-only — is strictly worse.
- **[Error messages, logs, telemetry]** No findings. No logging is added; `mapEvent` is pure and takes no logger. Marker text is never logged or forwarded — `TurnEnd` carries only the reason. MUST-NOT-log going forward: the entry text, which can contain arbitrary prompt bytes.
- **[Network & I/O]** No findings on limits. No socket reads, no new caps needed: the entry is already fully in memory, parsed by tui-driver, before `mapEntry` sees it. The one allocation concern — `userText` concatenating a 34 KB prompt body on every user entry — is removed by evaluating the authorship gate (a map lookup) before the text extraction, which is specified in § Contract, not left to the developer.
- **[Concurrency]** No findings. Pure functions, no goroutines, no shared state, no locks, no shutdown path. Nothing to order, nothing to leak.
- **[Tokens, secrets, credentials]** Not applicable — no credential, token, or secret is read, written, compared, or logged anywhere in this change.
- **[File operations]** Not applicable — no path is constructed, opened, stat'd, or written. The transcript is already tailed by tui-driver's existing reader; this change adds no file I/O.
- **[Subprocess / external command execution]** Not applicable — no `exec.Command`, no environment manipulation, no signal handling. The interrupt dispatch itself (`send_esc`) is pre-existing and untouched.
- **[Cryptographic primitives]** Not applicable — no randomness and no comparison against a secret. `strings.HasPrefix` compares against a public, compile-time constant, so constant-time comparison is not indicated.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-07-29
