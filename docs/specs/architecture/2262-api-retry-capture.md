# #2262 — capture claude's `system/api_retry` line, or record why it did not appear

One new live capture probe under `internal/e2e/realclaude/`, its offline self-checks, and the
record it writes. No production file changes; `streamLine` is not touched.

## Files read

- `internal/e2e/realclaude/dropped_line_capture_test.go` → `dropcapRecorder`, `dropcapRedactor`,
  `dropcapScanner`, `dropcapMakeEntry`, `dropcapWaitForChild`, `newDropcapArgvHandler`,
  `dropcapCaptured`, `dropcapEntry` — every reusable part of the capture family. This probe adds
  no redaction or scanning machinery of its own; it composes these.
- `internal/e2e/realclaude/compaction_capture_test.go` → `TestRealClaude_CompactionCapture`'s
  gate comment, `ccapRecord.fixtureWorthy`, `ccapRecord.stagingVerdict`, `ccapWriteRecord`,
  `ccapAwaitCompactTurn` — the fixture-absence arming, the published did-not-fire, the
  artifact-dir write that survives worktree removal, and the three-arm turn wait.
- `internal/e2e/realclaude/tool_progress_capture_test.go` → `fixtureWorthy`, `tpcapFixturePath` —
  the in-repo promotion gate this copies.
- `internal/e2e/realclaude/fixtures.go` → `WithWorktreeAuthenticated`, `realHome` — the credential
  re-pin that must precede `newDropcapScanner`, and the operator-home value the deny-scan arms on.
- `internal/e2e/realclaude/harness_streamparse_test.go` → `parseOne` — the shipped parser's verdict
  on one line, used here as the second, different-fabric decode witness.
- `internal/streamsup/parser.go` → `streamLine`, `streamMessage`, `Parser.consumeLine`,
  `Parser.emitSystemSubtype`, `Parser.consumePermissionDeniedLine` — the decode target AC 3 asks
  about, the silent drop `api_retry` takes today, and the precedent subtype whose `message` shape
  forced a separate gate.
- `internal/streamsup/runner.go` → `Config.Env`, `Config.Stdout`, `spawnEnv`, `buildArgs`, and the
  `cmd.Env = append(os.Environ(), env...)` composition — the one-entry additive env seam this
  probe's whole staging rides on.
- `docs/knowledge/features/e2e-realclaude-compaction-capture-test-go.md` — the load-bearing lesson
  for AC 5, restated under "Context" below: a capture that fires only inside the dispatcher's
  gate-only worktree loses its in-repo fixture, and only the out-of-worktree artifact record
  survives.

## Context

`Parser.emitSystemSubtype` maps eight `system` subtypes and has no `api_retry` arm; its `default`
returns false and `consumeLine`'s ignored-type branch drops the line in silence, so nothing today
would report one arriving. Nothing in this repo has ever seen the line — the committed per-subtype
census in `testdata/dropped_lines_v2.1.220.json` lists no `system/api_retry`, and the only
`api_retry` bytes in the tree are a hand-built wire envelope in `internal/protocol/testdata/`,
which is evidence about the protocol package and none at all about claude.

The mapping ticket downstream needs the verbatim field set, because this family's rule is that a
subtype's declared keys are exactly what a committed capture shows and nothing invented — the rule
`systemTaskStartedLine`'s doc states. So this ticket produces bytes, or it produces a recorded
absence that says which staging failed to provoke them.

**An API retry cannot be prompted for.** It needs claude's upstream call to fail, so the staging
redirects `ANTHROPIC_BASE_URL` at a loopback listener this rig owns and answers every request with
a retryable overload status. The turn is expected to fail; its `result` line is part of the
evidence.

**No ADR is warranted.** This is one probe inside an established family, and every design choice
below is an application of an existing pattern rather than a new one.

**The one lesson that shapes the deliverable.** Per the compaction capture's package overview, that
probe fired clean inside the dispatcher's own real-claude gate, wrote its fixture in-repo, printed
`Commit it` — and lost the bytes, because the gate runs in a detached merge-only worktree it then
discards and never runs `git add`. Four sibling captures lost fixtures the same way. This probe
therefore writes the record **twice**: to an `os.MkdirTemp` artifact directory outside any worktree
(the copy that survives, and the one #2236 recovered a sibling's bytes from) and, when the
promotion gate admits it, to the in-repo fixture path. The PR and the closing comment name the
artifact path as the recovery source rather than assuming the in-repo write survives.

## Design

One new file, `internal/e2e/realclaude/api_retry_capture_test.go`, every file-local identifier
prefixed `arcap` per this package's same-package-collision rule. Nothing else in the tree changes.

### The staged upstream

A loopback HTTP listener bound by the rig before claude spawns, and torn down in a `t.Cleanup`
registered before the runner's.

- `arcapListener` — `net.Listen("tcp", "127.0.0.1:0")`, so the kernel picks the port and the socket
  is reachable from nothing off-box. The literal host is load-bearing rather than tidy: a `":0"`
  typo binds every interface and puts the operator's live OAuth bearer token on the LAN, and
  nothing else in this design would notice. `TestArcapStageBindsLoopbackOnly` is that guard.
  Wrapped in an `http.Server` carrying explicit `ReadHeaderTimeout` / `ReadTimeout` /
  `WriteTimeout` / `IdleTimeout` and a `MaxHeaderBytes` (a bare `http.ListenAndServe` is the
  `gosec G114` shape), and an `ErrorLog` pinned to `io.Discard`. That last one is not decoration:
  `http.Server`'s nil `ErrorLog` falls back to the standard logger, which under `go test` writes
  to the gate's captured output, and what it logs on a malformed request is request-derived bytes
  from a connection whose headers carry the token.
- Its handler answers **every** request with HTTP 529 and a fixed rig-authored body in Anthropic's
  error envelope naming `overloaded_error`. 529 rather than 401/403: the ticket this was split
  from records that on claude 2.1.246+ the first two retries after an auth failure are quiet, so a
  401 listener is the shape most likely to produce a silent zero. Uniform for every path, so the
  record never has to explain why two requests got different answers.
- The handler **reads nothing**. It never touches `r.Header` and never reads `r.Body`. It records
  the method and the URL path with the query string discarded, a monotonically counted
  `requests_seen`, and the first/last request timestamps. The census is capped at
  `arcapMaxCensusKeys` distinct method+path keys with the overflow counted, so a pathological path
  space cannot grow the record without bound. Contract:

  ```go
  // arcapStage owns the listener. observe() returns a snapshot of what it saw.
  func arcapNewStage() (*arcapStage, error)   // binds loopback, starts serving
  func (s *arcapStage) baseURL() string       // "http://127.0.0.1:<port>"
  func (s *arcapStage) observe() arcapStaging // request count, census, timestamps
  func (s *arcapStage) close(context.Context) // http.Server.Shutdown
  ```

- The env seam is `streamsup.Config.Env`, one entry: `ANTHROPIC_BASE_URL=<baseURL>`. `spawnAndWait`
  composes `append(os.Environ(), env...)` and os/exec's last-duplicate-wins rule makes that
  override exactly one variable, leaving the operator's credentials and `HOME` intact —
  `spawnEnv`'s doc states the same precedence from the other end. The value is **rig-composed from
  the rig's own listener port**, never harvested; `os.Environ()` is not called in this file, and
  `env_delta` in the record carries that one entry.

### The turn

One child, one prompt, no tools. `arcapAwaitTurn(recorder, quiet, budget) string` returns
`result` / `quiescence` / `budget`, the same three arms `ccapAwaitCompactTurn` has and for the
same reason — a turn whose upstream is broken may never close. It is written here rather than
reused because the quiet window has to be **longer than claude's retry backoff**: a 30 s window
would call the stream quiet in the gap between two retries and cut the capture short of the very
lines it exists to record. `arcapQuiet` is 90 s under a 6-minute budget.

### Classification — the quarry, and the two confusables

`arcapIsAPIRetry(typ, subtype string) bool` matches **exactly** `type == "system" &&
subtype == "api_retry"`. It never matches on the string appearing anywhere in a line, because two
different things in this tree and its dependencies would answer to that:

- `agent_api_retry`, a boolean field on `tool_progress` frames already read by
  `consumeToolProgress` — a subagent's retry flag, present in this tree.
- `system/control_request_progress` with `status: "api_retry"` — a retry inside a control request,
  absent from this tree.

Both are counted into their own `confusable_census` so the record can say it saw them and that
they are not the quarry, and `arcapConfusables(raw []byte) []string` is proved offline against
both shapes.

### AC 3 — the decode verdict, measured twice with different fabric

Per captured line the record carries:

- `decodes_into_stream_line` — from `arcapStreamLineMirror`, a six-field local mirror of
  `streamLine` + `streamMessage` (both unexported in another package, so a mirror is the only way
  to run the same decode). Its doc says it is a mirror and names what keeps it honest.
- `parser_undecodable` — whether `parseOne` emitted `turnevent.Unrecognized` with
  `turnevent.UnrecognizedUndecodable`, which is the **shipped** parser's own verdict on the same
  bytes.
- `message_json_type` — one of `absent` / `null` / `string` / `number` / `bool` / `object` /
  `array`, from a `map[string]json.RawMessage` decode of the top level.

The two verdicts are deterministic code and a shipped-parser observation, not two copies of the
same judgement, so the offline drift alarm compares them on the family where the shipped verdict is
unambiguous — every line except `user` and `system/permission_denied`, the two shapes
`consumeLine`'s failure branch absorbs before `emitUnrecognized`. `system/api_retry` is in that
family, which is what makes the mirror's answer about the quarry checkable.

`message_json_type` is the field AC 3 exists for: `system/permission_denied` carries `message` as a
string where `streamLine` declares an object, `encoding/json` fails the whole line, and that subtype
needed `consumePermissionDeniedLine` to be mapped at all.

### The record

`arcapRecord`, marshalled indented, written through `dropcapRedactor` and `dropcapScanner` by an
`arcapWriteRecord` copied in shape from `ccapWriteRecord` — including the base64-payload decode
before the scan, and the fail-closed rule that a hit writes **nothing** and names only the class.

Provenance and staging fields: `ticket`, `claude_version`, `captured_at`, `is_capture`, `model`,
`spawn_shape`, `spawn_shape_delta`, `env_delta`, `workdir`, `prompt`, and an `arcapStaging` block
carrying `env_var`, `base_url`, `listen_addr`, `loopback_only`, `answered_status`,
`answered_body`, `forwards_requests` (false), `records_headers_or_bodies` (false), `requests_seen`,
`request_census`, `first_request_at`, `last_request_at`, `listen_error`.

Evidence fields: `lines_captured`, `frames` (every line of the turn, `arcapFrame`),
`line_type_census`, `api_retry_line_count`, `message_json_type_census`, `confusable_census`,
`stream_line_decode_failures`, `undecoded_lines`, plus the recorder's cap accounting.

AC 4's "reports that both ran" is two counters that cannot be satisfied by an empty result:
`redaction_rules_installed` (`len(red.rules)`) beside the `redaction` substitution list, and
`credential_scan_needles` (`len(scanner.needles)`) beside `credential_scan_applied` and
`credential_scan_skipped`. A redactor that was never built and one whose table matched nothing both
produce an empty `redaction` array; only the counter tells them apart.

### Outcome and the staging verdict

Vocabulary from the dropcap header: `fired` / `did-not-fire` / `instrument-broken`, with
`absence_claim_valid` its own boolean. The departure from dropcap, stated at the field: there,
only `fired` licenses an absence claim; here the **absence is the publishable result**, so
`absence_claim_valid` is true whenever the staged listener saw at least one request — which is
exactly AC 2's demand and exactly what `fixtureWorthy` gates on.

`arcapRecord.stagingVerdict()` names which reading a zero-`api_retry` run is, and the request count
is what separates them:

| requests seen | reading |
|---|---|
| 0 | claude never reached the staged listener — `ANTHROPIC_BASE_URL` was not honoured on this login shape, or claude failed before its first call. **Instrument-broken**, no absence claim. |
| 1 | the upstream call failed once and claude did not retry at all, so an absent line says the retry path never ran, not that it runs silently. A **qualified** absence. |
| ≥2 | claude retried a failing upstream N times and put no `system/api_retry` on the stream-json surface. The **finding**, and the strongest form the absence can take. |

The one-request row is the case a naive design folds into the last one; keeping it separate is what
stops a weak absence being published as a strong one.

`fixtureWorthy() (string, bool)` promotes both `fired` and `did-not-fire`, and refuses:
`instrument-broken`; `requests_seen == 0` (the non-vacuity rule this family needs, which is **not**
`ccapRecord.fixtureWorthy`'s — a zero-frame record is publishable here, a zero-request one is not);
a `claude_version` whose leading token does not parse as a version; and a fired record whose
`api_retry` frame is not `dropcapEncodingJSONString`, since a base64 frame carries no readable
payload for the mapping to replay.

### Arming, and the fixture name — AC 5

The gate is the **fixture's absence**, checked with `filepath.Glob("testdata/api_retry_v*.json")`.
`PYRY_PROBE_API_RETRY_CAPTURE=1` forces a re-capture over an existing fixture and can never arm the
run: `make e2e-realclaude` sets no custom `PYRY_PROBE_*` variable, so an env-armed probe skips on
the env check *before* the credential check and greens the live gate having captured nothing.

The glob, rather than compaction's compile-time version pin, is what keeps a version bump from
turning this ticket's deliverable into nothing: `ccapRecord.fixtureWorthy` refuses to promote under
a mismatched name, which is right when the fixture is a reader's pinned input and wrong here, where
the record is the deliverable. The filename is composed from the **observed** version instead —
`arcapFixturePath(version) (string, error)` takes the leading token of `claude --version` and
refuses any token that is not `^[0-9][0-9A-Za-z.-]*$`, so an unreadable version cannot compose a
path and a version string can never inject a path separator.

## Concurrency model

Four goroutines beyond the test's own, each with a named exit:

- `http.Server.Serve`, exited by `Shutdown` in a `t.Cleanup` registered **before** the runner's, so
  LIFO tears the listener down after the child is gone rather than under it.
- `runner.Run(ctx)` on its own goroutine, exited by `cancel()` in a cleanup that then waits on
  `runDone` with `arcapRunExitWait` and `t.Errorf`s if it overruns — `ccap`'s shape verbatim.
- os/exec's stdout copier, which drives `dropcapRecorder.Write`; the recorder is mutex-guarded for
  exactly this reason and the test goroutine only ever reads through `snapshot()`.
- The `http.Server`'s per-connection goroutines, bounded by the server's own timeouts.

The stage's counters live behind a `sync.Mutex` and are read only through `observe()`, never as a
direct field read from the test goroutine. `observe()` returns a **deep copy** of the census map:
returning the handler's own map would hand the test goroutine a map that live connections still
write to, and `json.Marshal` reading it during the record write is a race under `-race` and a
corrupt record without one.

## Error handling

Nothing about claude's behaviour is fatal. Fatal only on a broken instrument or a redaction
failure, which is the dropcap header's rule:

- listener bind failure, `streamsup.New` failure, no live child within `dropcapSpawnWait`,
  `WriteTurn` failure, zero lines captured, and `requests_seen == 0` → `instrument-broken`, the
  record still written, then `t.Fatalf`. The zero-request message says explicitly that a re-run
  will not fix it and that the routing decision is a human's, so nobody spends a repair leg on it.
- a deny-scan hit → `t.Fatalf` naming the class only; **nothing** is written, not the record and
  not the fixture.
- `did-not-fire` → not a failure. The record is written, promoted, and the log names the verdict.

The record write is registered in a `t.Cleanup` before anything below it can fail, so a structural
`t.Fatalf` still leaves the evidence on disk.

## Testing strategy

`make check` never compiles this file (it is behind `e2e_realclaude`), so every offline check below
ships inside it and runs under `make e2e-realclaude`, plus a `go vet -tags e2e_realclaude` and a
`-run` over the offline half locally, which is the cheapest compile-and-execute coverage the
standard gate cannot give.

- `TestArcapClassifierRejectsBothConfusables` — table-driven. The load-bearing guard: a
  `tool_progress` frame carrying `agent_api_retry: true`, a `system/control_request_progress` line
  whose `status` is `"api_retry"`, an assistant line with the words in prose, and the real
  `system/api_retry` envelope. Only the last is the quarry; the first two land in
  `confusable_census`.
- `TestArcapDecodeVerdictAgreesWithTheShippedParser` — the drift alarm on the `streamLine` mirror,
  over lines from the family where the shipped verdict is unambiguous, including the
  `permission_denied`-shaped `message`-as-string case as a row that must read *undecodable* on both
  sides.
- `TestArcapMessageJSONTypeNamesEveryShape` — all seven values, absent included.
- `TestArcapFixtureWorthyRefusesEveryBadCapture` — each row a capture that looks green from
  outside: instrument-broken, zero requests, unparseable version, a base64 `api_retry` frame. Two
  rows must be **accepted**: a fired record and a did-not-fire record with requests seen, because
  refusing the absence would defeat AC 1.
- `TestArcapFixturePathRefusesAnUnusableVersion` — an empty version, an `<unavailable: …>` string,
  and a token carrying a path separator.
- `TestArcapStagingVerdictSeparatesEveryReading` — the four rows of the table above, asserting the
  one-request row does not read as the finding.
- `TestArcapAwaitTurnDoesNotDependOnAResult` — the three exits against a real `dropcapRecorder` fed
  by hand, in milliseconds.
- `TestArcapStageAnswersRetryableAndReadsNothing` — the listener end to end over loopback with a
  real `http.Client`: the status and body it answers, that `requests_seen` counts, that the census
  key drops a query string, that the census is capped, and that the whole marshalled observation
  carries no header or body value from a request deliberately sent with both.
- `TestArcapStageBindsLoopbackOnly` — the guard on the one typo that would publish the operator's
  token to the LAN. Asserts `Addr()` parses to an IP that `IsLoopback()`, and that `baseURL` names
  that same address.

RED first: the classifier, the decode verdict and the stage tests are written and watched to fail
before the probe body exists.

## Open questions

1. **Does claude honour `ANTHROPIC_BASE_URL` on a subscription OAuth login?** The variable appears
   nowhere in this tree. Unanswerable from the repo — it is what the live run measures, and a zero
   in `requests_seen` is the answer, published as instrument-broken rather than as an absence.
2. **Is 529 in claude's retryable set?** Chosen from the documented `overloaded` error class. If
   the run shows `requests_seen == 1`, the answer is no or the retry is not attempted here, and the
   staging verdict says so in its own row rather than being folded into the finding.
3. **Does the 90 s quiet window outlast the retry backoff?** Unmeasured. `terminated_on` records
   which arm ended the turn, so a `quiescence` exit alongside a rising request count is the visible
   symptom if it does not.

Each is resolved by the live run, not by implementation, and any that changes the design gets a
`## Revisions` entry here.

## Size — one boundary exceeded, deliberately

Production source files: 0. New exported types: 0. Consumer call sites: 0. Acceptance criteria: 5.
Reject branches: 6. Total written work: ~1200 lines estimated, **1820 actual** (1795 probe + spec),
either way **over the 800-line ceiling**. The estimate ran about 50% light, and the excess is
almost entirely the offline half: eight self-checks, seven of them table-driven, against four
analogue probes that carry three or four each.

It stays one ticket on the floor rule. There is one deliverable — a committed record — and every
available cut leaves a child with a single consumer: a listener nothing but this run calls, or a
rig whose only output is the record it was cut from. Neither half is observable on its own. The
refiner reached the same conclusion on its own measurement against `501e12b2`'s tool-result sidecar
(1382 lines) and `6299ff46`'s compaction capture (1380), and I concur after reading both.

## Security review

**Verdict:** PASS — after three MUST FIX findings were folded into the design above. The first pass
over the pre-revision plan was FAIL.

**Findings:**

- [Trust boundaries] The new boundary is claude's HTTP request → this process, and it is not the
  handler: Go's `http.Server` parses request headers into `r.Header` **before** calling any
  handler, so the operator's OAuth bearer token is in this process's memory whether or not the
  handler reads it. "Reads nothing" therefore constrains what can be *recorded*, not what arrives.
  The boundary that actually holds is the record's fixed field set plus `dropcapScanner`, which is
  fail-closed over the whole marshalled record and arms on the token's own value as a needle. The
  second boundary, claude's stdout → `dropcapRecorder`, is the family's existing one and unchanged.
- [Trust boundaries] **MUST FIX — fixed in the design.** `http.Server.ErrorLog` left nil falls back
  to the standard logger. Under `go test` that writes into the gate's captured output, and what
  net/http logs on a malformed request is derived from a connection whose headers carry the token.
  The plan now pins `ErrorLog` to `io.Discard`.
- [Tokens] The operator's live OAuth token is sent, in plaintext, to a listener this rig owns. That
  is a genuine posture change from the TLS connection to api.anthropic.com it replaces, and it is
  irreducible: TLS here would need a certificate claude would reject, and a closed port that never
  receives the token also never yields `requests_seen`, which is AC 2's only discriminator between
  "the upstream failed" and "claude never arrived". The mitigations are that the traffic never
  leaves the loopback interface, the handler reads nothing, and the deny-scan is fail-closed. The
  record's redaction rationale states this so the operator deciding whether to publish it is told
  rather than left to infer it.
- [Tokens] No token is minted, stored, rotated or revoked by this ticket; there is no key material
  and no lifecycle to address.
- [File operations] `arcapFixturePath` composes a repo path from `claude --version` output. The
  `^[0-9][0-9A-Za-z.-]*$` shape admits no `/`, no `\` and no leading dot, so `..` and every
  absolute form are rejected, and an `<unavailable: …>` string fails at the first character.
  SHOULD FIX: also cap the token's length, so a pathological version string cannot compose an
  absurd filename. Both writes are `0600`, matching the family.
- [File operations] The `filepath.Glob` → write gap is a check-then-use, and benign: the worst
  outcome of losing that race is a re-capture overwriting a fixture the same run just wrote. No
  path in the gap is caller-controlled.
- [Subprocess execution] No `exec.Command` in this file and no `sh -c`; claude is spawned by
  `streamsup` from constant argv. The one new value crossing into the child is
  `ANTHROPIC_BASE_URL`, composed by the rig from its own `net.Listener.Addr()` and never from
  input. The child inherits the operator's environment additively, which is the family's existing
  posture and what keeps the credential working. The YOLO spawn shape's blast radius is unchanged
  and structurally smaller here: with a 529-only upstream claude can never receive a model
  response, so it can never be told to run a tool.
- [Cryptographic primitives] None used. The nonce is a timestamp used as a redaction class value
  and a prompt marker, not as a secret; it is passed non-zero, which `newDropcapRedactor`'s own
  doc flags as a live footgun at zero.
- [Network & I/O] **MUST FIX — fixed in the design.** The bind address had no guard. A `":0"` typo
  binds every interface and publishes the token to the LAN, and no other check in the design would
  catch it. `TestArcapStageBindsLoopbackOnly` now asserts the bound address `IsLoopback()`.
- [Network & I/O] Explicit `ReadHeaderTimeout`, `ReadTimeout`, `WriteTimeout`, `IdleTimeout` and
  `MaxHeaderBytes` on the server (the `gosec G114` shape is what a bare `ListenAndServe` would
  be). The handler performs no read at all, so there is no unbounded body read to cap. Connection
  count is unbounded but loopback-only and practically bounded by the one child; accepted rather
  than capped, and the request census is capped instead — an unbounded map keyed by a path the
  peer chooses was the one growth path in the record.
- [Error messages, logs] The handler logs nothing per request, `listen_error` goes through the
  redactor, and the deny-scan covers the staging block along with the rest of the record. The
  instrument-broken fatals name counts, verdicts and the env var — never a header, a body or a
  matched value, which is the dropcap rule this copies verbatim.
- [Concurrency] **MUST FIX — fixed in the design.** `observe()` returning the handler's own census
  map would hand the test goroutine a map live connections still write to; `json.Marshal` reading
  it during the record write is a race under `-race`. It now returns a deep copy. Lock order is
  trivial: the stage's mutex and the recorder's mutex are never held together. Every goroutine's
  exit is named under "Concurrency model"; the server's `Shutdown` cleanup is registered before the
  runner's so LIFO tears the listener down after the child, not under it.
- [Threat model alignment] No relay and no protocol surface, so `docs/protocol-mobile.md`'s model
  does not apply. The threat this ticket raises for itself — a public artefact produced by a
  staging that touches a credential — is addressed by the three mechanisms above and by AC 4's
  reporting requirement. Nothing is deferred to a future ticket.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-09

## Revisions

### 2026-09-09 — a request count alone cannot mean "claude retried"

The plan's verdict table read the request count as a retry count: two or more requests was the
finding. That is wrong, and wrong in the direction that publishes a false claim. `streamsup`
restarts a crashed child, and a broken upstream is exactly the condition that crashes one, so N
requests can be N children each making a single attempt — a respawn ladder reading as a retry
ladder.

The design now observes the spawn count from the runner's own log record and reads the two numbers
together. `arcapSpawnLog` replaces `newDropcapArgvHandler` at this probe's `Config.Logger` slot; it
keeps the first argv exactly as that one does and additionally counts `spawning claude` records.
`spawns_observed` ships in the record beside `requests_seen`, and only `requests_seen` **above**
`spawns_observed` licenses the finding, since that is what proves by pigeonhole that some single
child made more than one upstream call.

The verdict table's third row widens accordingly: it now covers every run where no child
demonstrably called twice, whether that is one request or several spread across as many spawns.
`TestArcapStagingVerdictSeparatesEveryReading` gains the row a request count alone would get wrong
— three requests across three spawns — and asserts it does not read as the finding.

Nothing else in the design moves; no acceptance criterion changes; the security review's findings
are unaffected, since the new handler writes nothing and reads only the runner's own log records.

### 2026-09-09 — the capture fired, and the record is the answer to all three open questions

The live gate ran the probe at claude 2.1.259 and it **fired**. The record is committed as
`internal/e2e/realclaude/testdata/api_retry_v2.1.259.json`, recovered from the artifact directory
exactly as this plan's Context section said it would have to be — the in-repo write died with the
gate's worktree, and the out-of-worktree copy is what survived.

**What the bytes say.** Ten `system/api_retry` lines across a thirteen-line turn, after thirteen
upstream requests answered 529, all from one spawn. The payload carries `attempt` (1 through 10),
`max_retries`, `retry_delay_ms` (585 ms rising to 37.7 s), `error_status`, `error` (`overloaded`),
`session_id` and `uuid`. The last two are in no docs page, which is the whole reason this family
declares field sets from captures rather than from documentation. `no_response` never appeared.

**AC 3's answer, and it is the easy one.** Every `api_retry` line decodes into the `streamLine`
mirror, the shipped parser finds none of them undecodable, and `message` is **absent** — so the
mapping needs no `consumePermissionDeniedLine`-style gate and can read the line in
`emitSystemSubtype` like the other eight.

**One finding the mapping ticket needs and nobody asked for.** A turn that exhausts its retries
closes as `result` with subtype **`success`**, carrying `terminal_reason: "api_error"` and a
`<synthetic>`-model assistant message holding the error prose. Subtype alone would read that turn
as having succeeded.

The three open questions are answered by the run: claude **does** honour `ANTHROPIC_BASE_URL` on a
subscription OAuth login; 529 **is** in its retryable set; and the 90 s quiet window was never
tested, because the turn ended on `result` after 181 s.

### 2026-09-09 — two fixes the gate and the review forced

**The turn was sized against itself, not against the invocation.** The gate ran the package under
`-timeout 20m`, this capture spent 182 s of it, and a sibling capture was still running when the
binary's timeout fired — a failure this branch introduced. The design now sizes the turn from
`t.Deadline()`: `arcapTurnBudgetWithin` reserves `arcapDeadlineReserve` for teardown and the record
write, shortens the turn to what is left, and skips outright below `arcapMinTurnBudget` rather than
starting a turn whose evidence a `-timeout` kill would discard along with every cleanup.
`arcapBudgetFor` is the arithmetic half, split out because a test cannot set its own deadline, and
`TestArcapTurnBudgetRespectsTheBinaryDeadline` proves the four readings offline.

To be plain about which fix closes the gate failure: **the committed fixture does.** The probe's
gate is the fixture's absence, so with the record landed the capture skips and costs no turn at
all. The deadline guard defends the only path that still spends one, a forced re-capture, where the
hazard is not a starved sibling but this probe's own evidence being killed mid-turn.

**The strongest verdict overclaimed.** Requests above the spawn count proves some child made more
than one upstream *call*, not that those calls were retries of one another — one child asking a
token-counting endpoint and then the messages endpoint would have read as a retry ladder. The
verdict now counts within a single census key: `arcapRecord.busiestEndpoint` names the busiest
endpoint and its repeats, and only repeats **above** `spawnFloor()` license the finding. The
committed capture satisfies it on twelve POSTs to one endpoint beside a single HEAD probe, and
`TestArcapStagingVerdictSeparatesEveryReading` gains the row that would otherwise slip through —
two requests, one spawn, two different endpoints — asserting it does not read as the finding.

Two review nits are also closed: `arcapCollect` parses each line once instead of twice, and
`arcapAwaitTurn`'s `sentAt` now receives the pre-turn line count its doc always described, so a
`system/init` line that arrived before the turn can no longer satisfy the quiescence arm alone.

No acceptance criterion changes, and the security review is unaffected: no new input is read, the
listener is untouched, and the new code reads only the record's own counters and the test binary's
deadline.
