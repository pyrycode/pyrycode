# 2307 — make the mcp_status capture report on the path where it fails

## Files read

- `internal/e2e/realclaude/mcp_status_capture_test.go` → `TestRealClaude_MCPStatusCapture`,
  `mcapAwaitInit`, `mcapPersist`, `mcapWriteRecord`, `mcapRecord`, `mcapCollect`, `mcapCensus`,
  `mcapStatusVerdict` — the instrument this ticket fixes. Every terminating path above the
  snapshot block returns with the record's line count, census and frames still at their zero
  values, which is the reading failure the ticket describes.
- `internal/e2e/realclaude/dropped_line_capture_test.go` → `dropcapRecorder`, `snapshot`,
  `dropcapCaptured`, `dropcapCaps`, `dropcapWaitForChild`, `dropcapSpawnWait` — the recorder mcap
  is wired to. `snapshot` returns kept lines plus five counters, and the counters are how a
  recorder that received bytes but kept no line can still be told from one that received nothing.
- `internal/e2e/realclaude/context_usage_capture_test.go` → `cucapRecord`, `cucapScanRecord`,
  `cucapRedactRecord` — the `stderr_capture` field name, tag and capping shape AC 2 names. Its
  header states the rule this ticket obeys: a string-bearing field added to a record must be
  visited by the redaction pass, and a field re-marshalled after the deny-scan ships unscanned.
- `internal/e2e/realclaude/inband_bypass_revoke_fixture_test.go` → `capFixtureCapture`,
  `truncateString` — the rune-safe byte cap, reused whole.
- `internal/e2e/realclaude/permission_protocol_spike_test.go` → `stderrFixtureCap` — 8 KiB.
- `internal/e2e/realclaude/background_trigger_probe_test.go` → `probeSyncBuffer` — the package's
  goroutine-safe buffer, written by `os/exec`'s copier goroutine and read from the test
  goroutine. Exactly the synchronizing the ticket's technical note asks for.
- `internal/streamsup/runner.go` → `Config.Stderr` — reaches `cmd.Stderr` at each spawn, so one
  buffer accumulates across `streamsup`'s respawns.
- `internal/streamsup/mcp_status_capture_test.go` → `mcpStatusReaderGate`,
  `mcpStatusPinnedServerKeys` — the reading half. Fixture absent plus empty pin is its one legal
  skip, which is why this ticket lands no fixture.
- `internal/e2e/realclaude/harness_streamparse_test.go` → `parseOne` — used by `mcapCollect`. Its
  `t.Fatalf` arm is unreachable: `streamsup.Parser.Write` always returns a nil error.
- `docs/knowledge/features/e2e-realclaude-mcp-status-capture-test-go.md` § "A record field can
  ship unscanned if it's re-marshalled after the deny-scan" and § "`t.Fatalf` inside a
  `t.Cleanup` skips every cleanup registered earlier" — both constrain this design directly.

## Context

Seven gate runs on 2026-09-09 ended `instrument-broken`, detail "no system/init line within
2m0s", and none of them can say why. The records read `lines_captured: 0`, `frames: []`,
`line_type_census: null`, but those are the zero values the record is constructed with:
`TestRealClaude_MCPStatusCapture` returns as soon as `mcapAwaitInit` fails, and the snapshot sits
below that return. "claude printed nothing" and "claude printed lines, none of them
`system/init`" fit the evidence equally and have opposite fixes. The child's stderr is discarded
outright — `streamsup.Config`'s `Stderr` is left nil — so a claude that refused the three-server
`--mcp-config` document wrote its complaint nowhere.

This ticket makes the instrument report. The remedy it enables — a promotable `fired` record or a
recorded-absence fixture — is deliberately out of scope and is filed from the record the next
gate run writes.

No ADR. This changes one probe's own reporting, not a system contract.

## Design

Three coupled edits to `internal/e2e/realclaude/mcp_status_capture_test.go`, no production
source file touched.

### 1. One fill site, on the path everything already goes through

New `mcapFillCapture(t, recorder, stderr, red, rec)` becomes the only place the record learns
what the instrument held. It **assigns**, never appends: `LinesCaptured`, the five cap counters,
`LineTypeCensus`, `UndecodedLines`, `Frames` and the new `StderrCapture`. It is therefore
idempotent re-derivation from the recorder and the buffer, which are the sources of truth.

`mcapPersist` calls it before `mcapWriteRecord`. Persist already runs from the `t.Cleanup`
registered before anything below it can fail, so every terminating path reaches it — the
init-wait return, the `streamsup.New` failure, the no-live-child return, the two mid-loop
returns, and a `t.Fatalf` anywhere. That is one guarantee at one site rather than a fill
statement per return, and it is the difference between a rule and a habit.

Two consequences the implementation must honour:

- The recorder and the stderr buffer have to exist **before** that cleanup is registered, so
  both move above it. Today the recorder is constructed two statements after.
- The happy path keeps an inline `mcapFillCapture` call, because the server extraction reads
  `rec.Frames`. Persist re-runs it and overwrites with a snapshot that may hold later lines.
  That is why the fill is assignment-only, and why more frames is more evidence rather than a
  disagreement: `Requests[].ReplyIndices` are recorder indices and are stable.

The fill lives in `mcapPersist`, not in `mcapWriteRecord`. `mcapCollect` needs a `*testing.T`
for `parseOne`, and `mcapWriteRecord` is deliberately `*testing.T`-free so AC 4's existing test
can hand it a hostile record and read the directory back.

A filled-but-empty record is distinguishable from an unfilled one in the written bytes:
`mcapCensus` returns a non-nil empty map and `mcapCollect` a non-nil empty slice, so `{}`/`[]`
say the pass ran and `null` says it never did. Neither tag carries `omitempty`. That
distinction is precisely what the seven existing records lacked, so it is asserted rather than
left to be noticed.

### 2. The child's stderr

- `probeSyncBuffer` reused whole, wired into `streamsup.Config`'s `Stderr`. One buffer
  accumulating across `streamsup`'s respawns is itself the evidence for a crash loop.
- New field on `mcapRecord`, matching `cucapRecord`'s spelling:
  `StderrCapture string \`json:"stderr_capture"\``. No `omitempty`.
- **Redact first, cap second.** `red.str` runs over the whole string so every run-local path
  matches its substitution rule; `capFixtureCapture` then bounds it at `stderrFixtureCap`. The
  reverse order can cut a `/var/folders/...` path mid-way, leaving a prefix the redactor no
  longer matches while `dropcapFixedNeedles`' fixed `/var/folders/` needle still does — which
  refuses the entire write over a truncation artifact. This ordering has its own test.
- Capped at fill time, not in a local copy at write time. `mcapWriteRecord` marshals `rec`
  directly, so capping there would leave the in-memory record and the file disagreeing about a
  field a reader is told the record carries.
- The field is filled before `mcapWriteRecord` marshals, so it goes through the **first**
  marshal and the deny-scan sees it. The package overview's re-marshal lesson is what makes
  that ordering worth stating.
- `mcapRedactionRationale` gains a **fourth** clause naming the stderr capture. That constant is
  what a human reads before deciding whether to paste this record into a public issue, and it
  currently enumerates three things the record can carry. A new field of pure free-form child
  output that the rationale does not mention leaves that enumeration silently wrong, which is
  the exact failure the rationale exists to prevent. The clause says what stderr is (claude's
  own diagnostics, the place an auth failure prints), why it is kept (a child that refused the
  document complains only here), and what defends it (the substitution table, the deny-scan
  ahead of every filesystem call, and the byte cap — naming the cap as a bound on reviewability,
  never as a redaction).

### 3. A bounded silence window

`mcapAwaitInit(recorder, budget, silence, poll) string` returns one of three named outcomes
instead of a bool:

| Outcome | Meaning |
|---|---|
| `mcapInitSeen` | the `system/init` line arrived |
| `mcapInitSilent` | at the silence deadline the recorder had received nothing at all |
| `mcapInitAbsent` | the full budget elapsed with output arriving and no `system/init` in it |

"Nothing at all" is the pure predicate `mcapSawOutput(lines, caps) bool`: true when any of
`len(lines) > 0`, `caps.BlankLines`, `caps.LinesOverCap`, `caps.PartialsDropped` or
`caps.UnterminatedPartial` is non-zero. Counting only kept lines would call a child that printed
a blank line, or one 5 MB line with no newline yet, silent — and both are a child that is
talking.

`mcapInitSilenceBudget = 30 * time.Second`, a quarter of `mcapInitBudget`. The seven observed
runs held the full 120s and never produced the line, so a claude that was going to speak had
four times this window; the sibling `oslcapAwaitInit` gives the same line 60s in total. The
number is a starting point, not a measurement, and the new outcome names which arm fired so the
next run retunes it from the record rather than from another guess.

The record gains `InitWait string \`json:"init_wait"\`` holding that outcome. `InitSeen` stays
— `mcapStatusVerdict` reads it and the streamsup reader's contract is unaffected — and both are
assigned from the same statement pair at one site so they cannot disagree.

The trade-off this accepts, stated rather than discovered later: claude's first stdout line in
stream-json mode is normally `system/init` itself, so a startup slow enough to produce nothing
for 30s reads as silence and is cut. The record now says which arm fired, which is what makes
that recoverable.

## Concurrency model

No new goroutines. `probeSyncBuffer` is written by `os/exec`'s copier goroutine at each spawn and
read from the test goroutine inside `mcapPersist`'s cleanup; its mutex is the whole discipline,
and `Bytes` returns a copy. `dropcapRecorder.snapshot` is already mutex-guarded the same way.

The mutex is load-bearing rather than belt-and-braces, and the reason is the cleanup order.
Cleanups run LIFO, so the run's cancel-and-join executes before `mcapPersist`; on the ordinary
path `streamsup` has reaped the child and `os/exec` has joined its stderr copier by the time the
fill reads the buffer. But that join is itself bounded by `mcapRunExitWait`, and on the timeout
branch it reports a `t.Errorf` and lets persist run anyway — with a copier goroutine possibly
still appending. A plain `bytes.Buffer` would be a race under `-race` on exactly that branch.
`mcapFillCapture` runs on the test goroutine in both call sites — `parseOne` reaches
`*testing.T`, so it must never be called from a goroutine.

`mcapPersist` keeps `t.Errorf` rather than `t.Fatalf`: a fatal inside a cleanup exits that
goroutine and skips every cleanup registered earlier, which here is the stub listener's
close-and-join. The fill it now performs adds no new fatal — `parseOne`'s is unreachable, since
`streamsup.Parser.Write` always returns nil.

## Error handling

- The init wait's two failure outcomes each set `mcapInstrumentBroken` with their own
  `outcome_detail` naming which reading fired, and both now leave a filled record behind.
- An `instrument-broken` run stays a **pass**. `mcapPersist` logs and does not fail, and the
  probe's `t.Fatalf` is still reached only after the init wait succeeds. Turning the broken path
  into a failure would redden the live gate and park this ticket under an error label instead of
  producing the record it exists to produce.
- No fixture is landed and `mcapFixturePath` stays absent, so `mcpStatusReaderGate` keeps its one
  legal skip and `make check` is untouched for every unrelated ticket.
- A stderr capture carrying a credential still refuses the whole write through the existing
  deny-scan, before any filesystem call. `newDropcapScanner` is built after
  `WithWorktreeAuthenticated` re-pins both credential variables, so both are live needles; the
  fixed `sk-ant-` needles catch the shape even when neither variable is set.
- **The stderr text never reaches a log line, a `t.Logf` or a `t.Fatalf`.** Every other consumer
  in this file prints counts, class names and rig-authored constants only. A message quoting
  child output bypasses the deny-scan entirely and lands in a run log this pipeline salvages,
  which is the one way this change could leak what it exists to capture. `mcapPersist`'s log
  line keeps its existing shape and gains no stderr; the init-wait `outcome_detail` strings
  carry the outcome name and the two durations, all rig-authored.

## Testing strategy

All offline, in the `TestMcap` family, no claude and no credentials:

- **`TestMcapFillCaptureDescribesWhatTheRecorderHeld`** — table over a recorder fed decodable,
  undecodable and blank lines. Asserts the record's fields at their zero values **before** the
  call and the derived values after, including the `null` versus `{}`/`[]` distinction on an
  empty recorder. The before-side assertion is what stops the test passing against a record that
  was already full.
- **`TestMcapPersistFillsARecordThatNeverReachedTheHappyPath`** — the load-bearing one. A record
  set to `instrument-broken` and never filled, a recorder holding three lines, a stderr buffer;
  `mcapPersist` into a temp dir, then the written JSON read back and asserted on `lines_captured`,
  `line_type_census`, `frames` and `stderr_capture`. Removing the fill from persist reddens this.
  Uses `dropcapScanner{needles: dropcapFixedNeedles()}`, never `newDropcapScanner`, so the row is
  not green or red depending on whose machine runs it, and the record is not fixture-worthy so no
  row can promote a fixture as a side effect.
- **`TestMcapStderrIsRedactedBeforeItIsCapped`** — a run-local path positioned so a cap-first
  implementation would leave a partial `/var/folders/` prefix behind. Asserts the placeholder is
  present (so the row is not vacuous), the raw path and the fixed needle are absent, and the
  result is within `stderrFixtureCap`.
- **`TestMcapAwaitInitStopsEarlyOnSilenceAndHoldsTheBudgetOnOutput`** — four rows in
  milliseconds: an init line ends it seen; total silence returns `mcapInitSilent` in well under
  the budget, asserted on **elapsed time**, which is the acceptance criterion's actual claim;
  non-init lines return `mcapInitAbsent` only after the full budget elapsed; a blank line alone
  counts as output, so it is `mcapInitAbsent` rather than `mcapInitSilent`.
- **`TestMcapSawOutputCountsEveryShapeOfOutput`** — the discriminator's own table, one row per
  counter.

Gate: `go vet -tags e2e_realclaude ./internal/e2e/realclaude/` plus
`go test -tags e2e_realclaude -race -count=1 -run TestMcap ./internal/e2e/realclaude/`.
`make check` never compiles this package, so vet under the tag is what proves the edit builds.

## Open questions

1. Is 30s the right silence window? Unmeasured. Resolved by construction rather than by
   evidence: the record now names which arm fired, so the next gate run supplies the number.
2. Should a silent run also record whether the child had exited? Deferred. `streamsup` respawns,
   so an exit is not terminal, and the accumulating stderr buffer is the better crash-loop
   signal. Revisit only if the next record shows silence with an empty stderr.

## Security review

**Verdict:** PASS (one MUST FIX found on the first pass, fixed in the plan above, re-walked)

**Findings:**

- [Trust boundaries] No findings. This change adds exactly one new untrusted-to-trusted
  crossing — the child's stderr — and routes it through the two defences the existing stdout
  boundary already uses: `red.str` at the single fill site in `mcapFillCapture`, and
  `dropcapScanner.scan` over the marshalled record in `mcapWriteRecord`, which runs ahead of
  every filesystem call. The boundary is one function and one field, not scattered. The
  redactor's substitution table, including every `addPathClass` call for `mcapClassRunLocal`, is
  fully built before `rec` is constructed and before the cleanup that calls the fill is
  registered, so no fill can run against a half-armed table.
- [Tokens, secrets, credentials] **MUST FIX, now fixed in the plan.**
  `mcapRedactionRationale` enumerates what this record can carry and is what a human reads
  before publishing it; a new field of pure free-form child output that the enumeration omits
  makes the record's own stated exposure surface wrong. The plan's Design § 2 now adds a fourth
  clause naming the stderr capture, its defences, and the fact that the byte cap bounds
  reviewability rather than redacting anything. Separately, no token is generated, stored,
  compared or rotated here; `nonce` is `time.Now().UnixNano()` and feeds a substitution rule,
  not a secret. An auth failure is the likeliest thing claude prints to stderr, and the
  ordering that defends it is pre-existing and preserved: `newDropcapScanner` is built after
  `WithWorktreeAuthenticated`, so both credential variables are live needles rather than the
  empty ones that would be silently skipped.
- [File operations] No findings. No path in this change is built from any input: the record path
  stays `filepath.Join(dir, mcapRecordName)` over two rig-owned constants at mode `0o600`, and
  no new `os.WriteFile` is introduced. No fixture is landed, so `mcpStatusReaderGate` keeps its
  one legal skip. No stat-then-open, no symlink traversal, no atomic-write requirement changes.
- [Subprocess / external command execution] SHOULD FIX, accepted with the reason stated.
  Wiring `streamsup.Config`'s `Stderr` changes the child's stderr from discarded to drained into
  an unbounded in-memory `probeSyncBuffer`, so a claude in a crash loop accumulates without
  limit for the run's duration. Bounded in time by the existing budgets, not in bytes. Accepted
  rather than fixed: the three existing stderr consumers in this package
  (`background_trigger_probe_test.go`, `finding_live_run_test.go`,
  `context_usage_capture_test.go`) hold the same posture against the same class of child, this
  failure has never been observed here, and `capFixtureCapture` bounds the half that reaches
  disk. Named so it is a decision rather than an oversight. `mcapArgs`, `mcapConfigDocument` and
  the environment the child inherits are all unchanged.
- [Cryptographic primitives] Not applicable, and the reason is structural: nothing in this
  change generates, derives, stores or compares a secret, so there is no primitive to pick
  badly. The only comparisons added are string equality on rig-authored outcome constants.
- [Network & I/O] No findings. No network surface; the stub Unix listener in the live test is
  untouched. The stdout size limits that matter — `dropcapMaxPartial` and
  `dropcapMaxCaptureBytes` in `dropcapRecorder` — are unchanged, and `mcapSawOutput` reads their
  counters rather than relaxing them. Stderr's write-side bound is `stderrFixtureCap`; its
  read-side bound is the finding above.
- [Error messages, logs, telemetry] No findings, on one explicit constraint now carried by the
  plan's Error handling section: the stderr text reaches no `t.Logf` and no `t.Fatalf`. That is
  the one route by which this change could leak what it exists to capture, because a message
  bypasses the deny-scan and lands in a salvaged run log — the mistake `cucapScrubbed` exists to
  catch in the sibling capture, which does print stderr into a fatal. `mcapPersist`'s log line
  keeps its counts-and-names shape, `mcapWriteRecord`'s refusal still names the class and never
  the value, and the new `outcome_detail` strings carry an outcome name and two durations.
- [Concurrency] No findings. No goroutine is added, so none can leak. One mutex, never held
  across another, so there is no lock order to get wrong. The buffer read is a copy
  (`probeSyncBuffer.Bytes`) and the recorder read is a copy (`dropcapRecorder.snapshot`), so
  `mcapSawOutput` and the fill cannot check-then-mutate shared state. The synchronizing is
  load-bearing on one specific branch rather than decorative: cleanups run LIFO so the run's
  cancel-and-join normally precedes `mcapPersist` and `os/exec` has joined the stderr copier by
  then, but that join is bounded by `mcapRunExitWait` and its timeout branch lets persist run
  with the copier possibly still appending.
- [Threat model alignment] No findings. No relay and no CLI surface, so
  `docs/protocol-mobile.md` § Security model does not apply. The threat that does apply is this
  package's standing one — a record produced on an operator's machine and committed or pasted
  into a public issue — and it is addressed by the redaction table, the fail-closed deny-scan,
  and the rationale clause the MUST FIX above adds. The out-of-scope item is named by the
  ticket itself: the remedy, a promotable `fired` record or a recorded-absence fixture plus its
  pin, is filed from the record the next gate run writes.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-10
