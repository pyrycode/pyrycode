# #1260 — Capture, verbatim, every stream-json line the daemon drops on the interactive surface

**Size:** S · **Production code:** none · **New files:** 1 Go file + 1 committed fixture · **Modified files:** 0

---

## Files to read first

Read these before writing anything. This is the turn-1 data load; the design below assumes you have it.

| Path | What to extract |
|---|---|
| `internal/streamsup/parser.go:38-115` | `ignoredLineTypes` (the measured drop set + the 2026-07-27 provenance) and `harnessNoOutputNudge` (the block-level suppression, its byte-exact-match rule, and the sentence naming what would promote it to a set with a pin test — this ticket delivers that second observation). |
| `internal/streamsup/parser.go:245-283` | `consumeLine`: the `default:` arm returns at `:277` for an ignored type **before** `emitUnrecognized`. This is why "zero events emitted" is the correct definition of "dropped". |
| `internal/streamsup/parser.go:117-136` | The `Parser` doc: **turn-stateless**, no cross-line accumulator except the partial-line buffer. This is the licence to classify each line with a *fresh* parser. Also the single-writer invariant you must respect in the recorder. |
| `internal/streamsup/parser.go:304-316` | `truncateRaw`: 16 KiB cap **and** `strings.ToValidUTF8(…, "")`. Proof that the `Unrecognized` lane is not a verbatim route. Do not use it. |
| `internal/streamsup/runner.go:62-152` | `streamsup.Config` — every field you set. `Stdout` (`:87-91`) is the parser's own seam and this probe's tap point. `Env` (`:96-98`) — leave nil so the child inherits the test process env verbatim. |
| `internal/streamsup/runner.go:216-254` | `New` — required fields (`ClaudeBin`, `WorkDir`, `SessionID`) and the defaults it fills. |
| `internal/streamsup/runner.go:541-611` | `spawnAndWait`: `cmd.Stdout = r.cfg.Stdout` (`:553`), `cmd.Env` nil ⇒ inherit (`:555-557`), and `cmd.Cancel` reaping descendant groups on ctx cancel (`:566-569`). The reap is why teardown order is load-bearing. |
| `internal/streamsup/runner.go:630-652` | `buildArgs` — the fixed `--input-format/--output-format/--verbose` prefix, `--session-id` on first spawn, and the doc line "Never emits -p/--print". This *is* the interactive spawn shape AC1 names. |
| `internal/streamsup/envelope.go:126-149` | `WriteTurn(ctx, w, prompt)` contract: nil `w` ⇒ `ErrNoLiveChild` (the pre-spawn window you must poll through), writes exactly once, never closes. |
| `cmd/pyry/streamsup_runner.go:113-124` | Production installs `streamsup.NewParser(...)` as `scfg.Stdout`. Your recorder goes in the same slot — that is what makes "upstream of the parser" structural rather than argued. Note the two flags production adds that this probe does not (`withApprovalArgs`). |
| `internal/e2e/realclaude/background_trigger_probe_test.go:150-172` | `probeLeverVars` (the never-dump-the-environment rule) and `probePrompt`. |
| `internal/e2e/realclaude/background_trigger_probe_test.go:647-716` | `holdProbeFIFO` — call it, do **not** edit the file. Read the "DO NOT RELEASE THE FIFO BEFORE BOTH READS" reasoning in the #1240 header too. |
| `internal/e2e/realclaude/background_trigger_probe_test.go:606-614` | `probeClaudeVersion(claudeBin) string` — reuse verbatim for provenance. |
| `internal/e2e/realclaude/fifo_reader_liveness_test.go:80-160` | `fifoLiveRead(path) fifoLiveOutcome` and the three verdicts. Takes no `*testing.T`, never fails a test. |
| `internal/e2e/realclaude/interactive_background_idle_probe_test.go:140-197`, `:326-459` | The idioms to copy: env gate + skip message, non-`t.TempDir()` artifact dir, `t.Cleanup` LIFO ordering, the buffered(1) rendezvous-stamp goroutine, `bgIdlePrompt` (call it — same package), the did-not-fire-is-a-result posture. **`bgIdleRecordTurn` (`:487`) is the wrong recorder for this ticket** — it reads decrypted phone frames, downstream of the parser. |
| `internal/e2e/realclaude/interactive_background_idle_probe_test.go:824-846` | `bgIdleRedact` — the precedent you must *exceed*, and the reason why (§ Redaction). |
| `internal/e2e/realclaude/ptyrunner_byte_equivalence_test.go:1035-1046` | `parseOne(t, line) []turnevent.Event` — call it. Existing precedent for classifying a line by running the shipped parser. |
| `internal/e2e/realclaude/fixtures.go:96-143` | `WithWorktreeAuthenticated(t) string` — temp `$HOME`, re-pinned credentials, seeded `.claude.json`. Note the credential lives in the **process environment**, which is why the deny-scan can look for its value. |
| `internal/turnevent/event.go:99-162` | `UnrecognizedSite` constants and the `Unrecognized` struct. `UnrecognizedUserBlock` is the nudge-drift discriminator. |
| `internal/e2e/realclaude/testdata/` | Fixture naming convention: `<subject>_v<claude-version>[_<variant>].json`. |
| `internal/e2e/realclaude/resilience_test.go:282` | `resolveClaudeBin(t) string`. |

---

## Context

Four downstream tickets (#1261–#1264) need to map claude's `system/*` and `rate_limit_event` messages into pyry's client surface. Nobody has ever read one. The record is type names and counts, taken on the **headless** surface; the surface that matters is the **interactive** one, and this repo already holds a counter-example to assuming they match (#1218: `ptyrunner` emits zero `system/thinking_tokens`, `streamrunner` ~10/turn).

This ticket changes no drop behaviour. It reads what is already on the wire and writes it down.

The one thing that has to be right is the **tap point**. #1240's rig stages exactly the turn we need but records decrypted phone frames, which sit *downstream* of the parser — every dropped line is absent from that recorder by construction, and the resulting zero would read as a measured absence.

---

## Design

### The tap: drive `streamsup.Runner` in process, tee `Config.Stdout`

The ticket left this to the architect. **Decision: in-process runner.** The probe constructs a real `streamsup.Runner` and installs its own recorder where production installs the parser.

```
test goroutine ──> streamsup.New(Config{Stdout: recorder}) ──> Run(ctx)
                                                               │
                                              exec claude ─────┘   argv == buildArgs(base, true, id)
                                                               │
       recorder <── cmd.Stdout <── claude stdout ──────────────┘
          │
          ├─ append raw line bytes (mutex-guarded)
          └─ classify: parseOne(line) ⇒ len(events) == 0 ⇒ DROPPED
```

Why this seam and not a hand-rolled `exec.Cmd` on the argv prefix:

- **The capture point is production's own parser slot.** `cmd/pyry/streamsup_runner.go:117` assigns `streamsup.NewParser(...)` to `scfg.Stdout`; the recorder takes that exact slot. "Upstream of the parser" is then structural, not an argument.
- **The argv is computed by production code, not transcribed.** `buildArgs` emits the prefix; a hand-rolled `exec.Cmd` would copy it and could drift silently from the shape it claims to measure.
- **One hop for the lever.** `t.Setenv(BASH_DEFAULT_TIMEOUT_MS, "5000")` → `os.Environ()` → `Config.Env == nil` → `cmd.Env == nil` → the child inherits verbatim (`runner.go:555-557`). #1240 needed two hops through the daemon.
- **No pyry binary, no relay, no phone, no noise handshake, no conversation seeding.** Every one of those is a failure mode between the lever and the bytes, and none of them affects what claude writes to stdout.

**The known delta from a production interactive session, which the record must state verbatim (AC5).** Production adds, via `withApprovalArgs` and `claudeSettingsArgs`, a `--permission-prompt-tool` / `--mcp-config` pair (non-yolo only) and a per-session `--settings`. This probe passes neither. It uses `--dangerously-skip-permissions`, which is a *real* production shape (the YOLO session bit, `internal/sessions/session.go:106-107`) and is precisely the arm on which `withApprovalArgs` injects nothing. The consequence to state: the `--mcp-config` server would appear in `system/init`'s `mcp_servers` / `tools`, so this capture's `system/init` is the yolo variant. Record the argv verbatim and name the absent flags; do not paper over it.

### Package, file, naming

- One new file: `internal/e2e/realclaude/dropped_line_capture_test.go`, build tag `//go:build e2e_realclaude`.
- **Every file-local identifier takes the `dropcap` prefix.** Siblings add files to this package concurrently and a branch-overlap check does not catch a same-package identifier collision (it surfaces only once both are on main). #1240's header states this; obey it.
- Call, never copy, never edit: `holdProbeFIFO`, `fifoLiveRead`, `probeClaudeVersion`, `bgIdlePrompt`, `parseOne`, `resolveClaudeBin`, `WithWorktreeAuthenticated`.

### The recorder

```go
type dropcapRecorder struct { /* mu, partial-line buf, lines []dropcapLine, bytesDropped int, resultSeen chan struct{} */ }
var _ io.Writer = (*dropcapRecorder)(nil)
```

Contract: `Write` accumulates and splits on `'\n'`; each **complete** line is appended (raw bytes, before any redaction) and classified; the trailing partial is carried. On the first line whose top-level `type` is `result`, close `resultSeen` once (`sync.Once`) — that is the turn boundary the test waits on.

- **Mutex-guarded.** os/exec drives `Stdout` from one internal copier goroutine while the test goroutine reads the accumulated lines. That is a race under `-race`; `probeSyncBuffer` (`background_trigger_probe_test.go:722`) is the local precedent for the shape.
- **The partial-line buffer is capped at 4 MiB** (`dropcapMaxPartial`), the same value and the same reason as `defaultMaxParseBuf` (`parser.go:13-19`): a child that emits megabytes with no newline would otherwise grow this buffer without bound. Past the cap, drop the partial, count it into `PartialsDropped`, and resume at the next `'\n'`. This is claude's stdout — untrusted input read into the parent's memory — and it is the only unbounded accumulator in the design.
- **Total-capture cap, never silent.** Stop appending past `dropcapMaxCaptureBytes` (8 MiB), count into `LinesDropped`/`BytesDropped`, and **keep consuming** — an unconsumed stdout would block claude. A line that would cross the budget is recorded **whole or not at all**: individual payloads are never truncated (AC3), so the cap drops entire lines and says how many.
- Nothing else. The recorder does not forward to a live parser; the turn does not need one.

### Classification — decided by the shipped parser, not by a mirror

For each captured line, `parseOne(t, line)` (fresh parser per line; licensed by the documented turn-statelessness at `parser.go:124-128`):

| Events emitted | Meaning | Recorded `reason` |
|---|---|---|
| 0, and top-level type ∉ {assistant, user, result} | in `ignoredLineTypes` | `ignored-line-type` |
| 0, and type == `user` | every block suppressed — the harness nudge | `user-block-suppressed` |
| 0, and type == `assistant` | message nil or zero mappable blocks | `empty-message` |
| ≥ 1 | mapped, or surfaced as `Unrecognized` — **not dropped** | *(not recorded as dropped)* |

This never reads `streamsup`'s unexported table; it asks the shipped parser what it does. `emitUnrecognized` is reached only *after* the ignored-type `return` at `:277`, so "zero events" and "dropped" are the same predicate by construction.

**Block-level pass, for AC2's suppressed `user`/`text` block.** Independently of the line classification, decode every `user` line's `message.content[]` and record each block with `type == "text"` verbatim. Whether it byte-matched the shipped `harnessNoOutputNudge` is decided by the same parser, not by a copy of the constant: feed the isolated line to `parseOne` and look for a `turnevent.Unrecognized` with `Site == turnevent.UnrecognizedUserBlock` and `Kind == "text"`.

- such an event present ⇒ the block **did not** match ⇒ drift; record `matches_shipped_constant: false`
- absent ⇒ it matched ⇒ record `true`

A `true` here is the second confirmed observation `harnessNoOutputNudge`'s own doc comment (`parser.go:112-114`) names as the thing that promotes the constant from a lone string to a set with a pin test.

### The staged turn

Reuse #1223's proven lever and #1240's interactive prompt:

1. `WithWorktreeAuthenticated(t)` → temp `$HOME` (skips cleanly with no credentials).
2. `workdir` = a fresh empty dir under that `$HOME`. **Not a git repo** — see § Redaction.
3. `t.Setenv(BASH_DEFAULT_TIMEOUT_MS, "5000")` **before** constructing the runner.
4. `holdProbeFIFO(t, filepath.Join(workdir, dropcapFIFOName))` → rendezvous channel; stamp its firing into a **buffered(1)** channel from its own goroutine (never a direct field write — that races the test goroutine).
5. Control read: `fifoLiveRead(fifoPath)` **before** the send. The only correct answer is `no-reader`; anything else means the instrument does not discriminate in this rig ⇒ outcome `instrument-broken`, no claims.
6. `streamsup.New(Config{ClaudeBin, WorkDir: workdir, SessionID: dropcapSessionID, Args: dropcapArgs, Stdout: rec, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})`, then `go runner.Run(ctx)`. Pass a **discard handler explicitly** — `Logger: nil` falls back to `slog.Default()` (`runner.go:233-235`), which sends the runner's lifecycle lines to CI output.
7. Poll `runner.Stdin()` until non-nil (or `WriteTurn` until it stops returning `ErrNoLiveChild`), then `streamsup.WriteTurn(ctx, runner.Stdin(), []byte(bgIdlePrompt(fifoPath, nonce)))`.
8. Wait on `resultSeen` or the turn budget.
9. At that instant, take the second `fifoLiveRead`.
10. Classify, redact, write the record.

`dropcapArgs` = `{"--model", "haiku", "--dangerously-skip-permissions"}`. Nothing more; `buildArgs` supplies the rest.

**Teardown order is load-bearing.** `t.Cleanup` runs **LIFO**, so *registered first* means *runs last*. Register the record-writing cleanup **first** in the test body — exactly as #1240 does at `:374` — so the execution order is *runner ctx cancelled → FIFO released → record written*, and a structural `t.Fatalf` anywhere below still leaves the evidence on disk.

Both liveness reads happen in the test body (steps 5 and 9), before any cleanup runs, so no read is ever taken after the FIFO's write end is released — a read taken after that would be measuring a `cat` the release itself killed.

`cmd.Cancel` reaps the child's descendant process groups (`runner.go:566-569`), which is what kills the backgrounded `cat` on the normal path. `holdProbeFIFO`'s release is the backstop: if the reap misses (claude already exited, so os/exec never fires `Cancel`), closing the last write end sends `cat` EOF and it exits on its own. Neither path leaves an orphan.

### Outcomes — three, and only one licenses an absence claim

| Outcome | Condition | Absence claims |
|---|---|---|
| `fired` | pre-read `no-reader` **and** rendezvous fired **and** the read at turn end is `reader-present` | **valid** |
| `did-not-fire` | pre-read `no-reader`, but the rendezvous never fired or the turn ended without the command alive | none |
| `instrument-broken` | pre-read not `no-reader`, the runner never spawned, or zero lines captured | none — no claim of any kind |

`fired` is exactly AC1's "a Bash command is left running in the background": the probe holds the FIFO's only write end for the whole window, so `cat` cannot have finished; `reader-present` at turn end excludes its having been killed; the pre-read control is what makes `reader-present` non-vacuous.

The record carries `absence_claim_valid` as its own boolean, tied to `outcome == "fired"`. AC1's did-not-fire arm is a legitimate published result, not a failed test.

### The record, and the fixture

**One artefact, two homes.** The probe writes `dropcap-record.json` into a non-`t.TempDir()` artifact dir (`os.MkdirTemp("", "pyry-1260-capture-*")`, logged redacted). The developer copies that file **verbatim** (`cp`, byte-preserving) to `internal/e2e/realclaude/testdata/dropped_lines_v<claude-version>.json` and commits it. No hand-transcription step exists, so the invent-a-plausible-fixture failure mode (#1243, #1247) is structurally unavailable.

Record shape (JSON, one object):

- **Provenance** — `ticket`, `claude_version` (from `probeClaudeVersion`), `captured_at` (RFC3339), `spawn_shape` (the argv **verbatim**, redacted), `spawn_shape_delta` (fixed prose naming `--mcp-config` / `--permission-prompt-tool` / `--settings` as absent and why), `is_capture: true`, `model`, `env_delta` (a **fixed literal** `["BASH_DEFAULT_TIMEOUT_MS=5000"]` — never harvested from `os.Environ()`).
- **Outcome** — `outcome`, `outcome_detail`, `absence_claim_valid`, `pre_rendezvous_read`, `turn_end_read` (both `fifoLiveOutcome` verbatim), `rendezvous_at`.
- **Dropped lines** — an array, in stream order. Per entry: `index`, `type`, `subtype`, `reason`, `payload_len_bytes_captured`, `payload_len_bytes` (post-redaction), `payload_encoding`, and the payload itself.
- **Census** — `census` (map `"<type>/<subtype>"` → count), `expected_absent` (which of `task_started` / `task_updated` / `background_tasks_changed` / `task_notification` had count 0), `rate_limit_event_observed` (bool), `harness_nudge` (`observed`, `matches_shipped_constant`, and the block payload).
- **Redaction** — `redaction` (§ Redaction), `redaction_rationale` (fixed prose).
- **Limitations** — a fixed constant stating AC5 in full: one turn, one spawn shape, one claude version, one model; cross-version and cross-surface stability unmeasured; the census is a census of *this* turn.
- **Capture cap** — `lines_dropped_over_cap`, `bytes_dropped_over_cap` (0 in the normal case; never silent).

**Payload encoding — the AC3 trap, and the fix.** A payload stored as a JSON string field is re-encoded by the container. That round-trip is lossless *for valid UTF-8* (container-level `<`/`>`/`&` escaping decodes back identically), but Go's encoder replaces invalid UTF-8 with U+FFFD — the same silent mutation the ticket calls out in `truncateRaw`. So, per entry:

- valid UTF-8 ⇒ field `payload`, `payload_encoding: "json-string"`
- otherwise ⇒ field `payload_b64` (`base64.StdEncoding`), `payload_encoding: "base64"`

`payload_encoding` is written on **every** entry, and `payload_len_bytes` vs `payload_len_bytes_captured` makes any length change from redaction visible at the entry where it happened. No payload is ever truncated, and nothing is ever stripped of invalid bytes.

### Redaction (AC4)

The primary defence is **by construction**, and the record says so:

- the workdir is a fresh empty directory with no git repo ⇒ no branch names, no file contents;
- the prompt is rig-authored (`bgIdlePrompt`) ⇒ no operator prose;
- the Bash command is `cat <fifo>` ⇒ it produces no output before it is backgrounded;
- `os.Environ()` is never read into the record — `env_delta` is a fixed literal (the #1223 rule, `background_trigger_probe_test.go:150-158`).

On top of that, two mechanisms — and note carefully **what each one covers**, because the payloads are not the only exposure.

**(a) A declared substitution table, applied to every string that enters the record — not only to payloads.**

`dropcapRedact(b []byte) ([]byte, []dropcapSubstitution)`, applied longest-value-first so a shorter path cannot shadow a longer one. Classes: temp `$HOME`, the operator's real `HOME` (`fixtures.go:34` — `WithWorktreeAuthenticated` copies the operator's `~/.claude.json` into the temp home, so real-home strings can reach a payload), `os.TempDir()`, the artifact dir, the resolved workdir, the FIFO path, the session UUID, the prompt nonce.

For every path class, the table carries **both the raw path and its `filepath.EvalSymlinks` form**. On macOS the temp root resolves `/var/…` → `/private/var/…` and `agentrun.ResolveWorkdir` performs the same mapping (`runner.go:69-73`), so a payload can carry the resolved spelling of a path the table only knows unresolved. A class whose two forms are equal contributes one entry, not two.

Each applied substitution is recorded as `{class, replacement, count}`. **`class` is a name (`"operator_home"`), never the value** — writing the matched value into the record would put the very string being redacted into the fixture.

The fields that must go through it are **not** just the payloads. `fifoLiveOutcome` carries the FIFO path in both `Path` and `Detail` (`fifo_reader_liveness_test.go:136-156`), so `pre_rendezvous_read` and `turn_end_read` leak an absolute path under the temp `$HOME` without any payload being involved. The same goes for `spawn_shape`, `outcome_detail`, `fifo_path`, `workdir`, and **every `t.Logf` in the file**, including the artifact-dir line. Redact at the point each field is assigned, so the declaration (`count`, and the entry's `payload_len_bytes` vs `payload_len_bytes_captured`) sits with the value it describes — AC3's "the file saying so at the point where it happened".

**(b) A fail-closed deny-scan over the whole serialized record.**

`dropcapDenyScan(b []byte) []string` runs on the **final marshalled record**, after every substitution, and returns the *names* of any denied class still present:

- the values of `CLAUDE_CODE_OAUTH_TOKEN` and `ANTHROPIC_API_KEY`, when set;
- the literal `sk-ant-`;
- the temp `$HOME`, the operator `HOME`, the artifact dir, and the workdir literals (raw **and** `EvalSymlinks` forms);
- the prefixes `/Users/`, `/home/`, `/private/var/folders/`, `/var/folders/`.

Two rules that make this instrument work rather than invert:

- **Minimum needle length.** `bytes.Contains(x, []byte(""))` is **always true**, and a 2-character credential value matches nearly every payload. Skip any needle shorter than 16 bytes and record `credential_scan_applied: false` with the class name — an arm that silently did not run must never read as an arm that ran and found nothing. `bgIdleRedact`'s own empty-`realHome` guard (`interactive_background_idle_probe_test.go:821-823`) is the same lesson on the substitution side, and it is documented there as load-bearing rather than defensive noise.
- **Escaped forms.** Check each literal raw **and** in its JSON-string-escaped form (marshal to a JSON string, strip the surrounding quotes). The record embeds payloads inside JSON strings, where the escaping differs from the raw bytes.

Credential values are read via `os.Getenv` **solely** as needles. They are never stored in a struct field, never logged, and never written. This is the one deliberate exception to the rule below.

**On a hit: `t.Fatalf`, and no file is written — not the record, not the fixture.** The message names the **class** and the offending entry index only. It must never print the payload, the matched value, or the surrounding bytes; "let me include the raw payload to help debug it" is exactly how the token reaches CI output and inverts the whole control (#1240 states the same rule for its decrypt failures at `:483-486`). An operator's fix is to extend the table with the named class and re-run — one live turn, which is the correct price for not publishing a credential.

Substitution is best-effort and declared; the scan is the deterministic net behind it. Different fabric: a substitution table that misses a shape is caught by a scan that does not depend on the table being right.

**`os.Environ()` is never called in this file.** `env_delta` is a fixed literal, matching #1223's `probeLeverVars` rule (`background_trigger_probe_test.go:150-158`) — claude's environment holds `CLAUDE_CODE_OAUTH_TOKEN` and `ANTHROPIC_API_KEY`, and this artefact is pasted into a public issue. The two `os.Getenv` calls in the deny-scan are the named exception, and they read values *to search for*, never to record. The prohibition is greppable on purpose.

**File modes.** The artifact dir is `os.MkdirTemp` (0700); the record is written `0600`, matching `bgIdleWriteRecord` (`:813`). Both matter on a shared box where `$TMPDIR` is world-traversable.

**Log ordering.** Test output gets substitution but cannot be un-written, so the deny-scan cannot protect it after the fact. Emit the outcome/summary `t.Logf` **after** the scan has passed, so anything that reaches CI output has already cleared the net. The early artifact-dir line is the one exception; it prints a redacted path and nothing else.

**Why `bgIdleRedact` is not sufficient, stated in the record.** It substitutes one value (the operator's home) into a *summary* string that `turnbridge/outbound.go` had already capped at 200 runes — the exposure was structurally bounded before redaction ever ran. Here the input is an uncapped raw payload that can carry cwd, tool output, file contents, branch names and prompt text, and it passes through unsummarised. A single home substitution would leave the temp-`$HOME` path, the workdir, the session UUID and any tool output untouched.

**What redaction deliberately keeps**, and why removing it would defeat the ticket: top-level types and subtypes, claude's own structural fields, tool names, model names, token counts, timestamps, the task-lifecycle payload structure and its human-readable strings, and the harness nudge text. Those are the mapping's raw material.

---

## Concurrency model

Three goroutines, no shared mutable state outside two guarded seams.

| Goroutine | Owns | Synchronisation |
|---|---|---|
| test | the record, the reads, teardown | reads recorder state only through its mutex-guarded accessor |
| `runner.Run` (+ os/exec's stdout copier) | `recorder.Write` | `recorder.mu` guards the line buffer and the partial; `sync.Once` guards `resultSeen` |
| `holdProbeFIFO`'s writer, and the rendezvous stamper | the FIFO write end | rendezvous is a **closed** channel; the stamp lands in a **buffered(1)** channel so a never-firing rendezvous leaks no goroutine |

Shutdown: `cancel()` the runner ctx after the record is captured; `Run` returns `ctx.Err()`; `cmd.Cancel` reaps descendant groups then SIGTERMs. Wait for `Run` to return (a `done` channel) before the test body ends so `-race` sees no in-flight `Write`.

---

## Error handling

| Failure | Handling |
|---|---|
| No credentials / no `claude` on PATH | `t.Skip` — `WithWorktreeAuthenticated` already does this |
| Env gate unset | `t.Skipf` with the exact re-run command, #1240's message shape. A skip is the normal `make e2e-realclaude` outcome and carries no signal |
| `streamsup.New` / `Run` fails to spawn | `outcome = instrument-broken`, record written, no claims |
| Pre-read not `no-reader` | `outcome = instrument-broken`, record written, no claims |
| Rendezvous never fires | `outcome = did-not-fire`, record written, **no absence claim** |
| Turn budget expires before `result` | record `terminated_on: budget`; outcome per the table |
| A captured line is not valid JSON | Record it with `type: ""` and `reason: undecodable-line`. It is data about claude, not a broken instrument |
| Deny-scan hit | `t.Fatalf`, **no fixture written**. Fail closed |
| Capture cap reached | Counted into the record, drain continues. Never silent |

Nothing about claude's behaviour is fatal. Only a broken *instrument* or a redaction failure is.

---

## Testing strategy

One live probe plus four offline checks, all in the new file. Offline means "runs under `make e2e-realclaude` with no claude and no network" — the existing pattern (`ptyrunner_byte_equivalence_test.go:1002`).

**Live (env-gated, `PYRY_PROBE_DROPPED_LINE_CAPTURE=1`):**
- `TestRealClaude_DroppedLineCapture` — the probe above. Asserts nothing about claude; it records and classifies.

**Offline (always run):**
- *Recorder split* — feed a synthetic byte stream through `Write` in awkward chunkings (split mid-line, mid-UTF-8-rune, a lone `\n`, a final line with no trailing newline, an empty line) and assert the reassembled lines are byte-identical to the input lines and in order. The instrument that mis-splits produces a wrong absence census; this is the check that stops an inverting failure.
- *Classification* — table over synthetic lines: `system/init`, `system/thinking_tokens`, `rate_limit_event` ⇒ dropped with `ignored-line-type`; `{"type":"result","subtype":"success"}` and an `assistant`/`text` line ⇒ **not** dropped (the control arm that keeps the zero-event assertions from passing vacuously); a `user` line whose only block is a `text` block ⇒ dropped with `user-block-suppressed`.
- *Redaction + deny-scan* — a synthetic record embedding a fake home path, a workdir, a `sk-ant-`-prefixed literal and a UUID. Assert: every substitution is declared with a count; no declared `class` contains its own matched value; the deny-scan reports the surviving `sk-ant-` literal when redaction is deliberately handed an empty table (the arm that proves the net is not decorative); the JSON-escaped form of a literal is caught as well as the raw; and — the inverting-failure arm — **a needle shorter than 16 bytes, including `""`, is skipped and reported as not-applied rather than matching everything**. Add one arm feeding a synthetic `fifoLiveOutcome` through the record path, asserting its `Path` and `Detail` come out substituted: that is the field pair that leaks an absolute path with no payload involved.
- *Fixture validation* — read `testdata/dropped_lines_v*.json` (glob; `t.Fatalf` naming the probe command if **no** file matches, because a committed ticket with no capture is exactly the failure this ticket exists to prevent) and assert: the provenance keys are present and non-empty; `is_capture` is true; every entry's `payload_encoding` is one of the two declared values; every `json-string` payload is valid JSON with a `type` field; the census entry count matches the dropped-line array length; the deny-scan finds nothing in any committed payload. Then, **if** `harness_nudge.observed`, feed its payload line to `parseOne` and assert it emits no `Unrecognized{Site: UnrecognizedUserBlock}` — the pin test `harnessNoOutputNudge`'s doc comment asks for.

The fixture-validation test's deny-scan arm is the one that keeps running forever: it turns "a future re-capture must not commit an operator's home path" from a rule someone remembers into a check that fails.

---

## Scope

Total projected: **~480–580 lines**, one new Go file, one committed fixture, **zero production files**, **zero modified files**, **zero consumer call sites** (nothing existing changes, so there is no edit cascade), three outcome branches. Every helper named above already exists and is called, not rebuilt.

Budget guidance: this package's header comments run long. Keep this file's header to **≤ 60 lines** — the design reasoning lives in this spec, and the header's job is to say what the probe stages, where it taps, and that a skip carries no signal.

---

## Open questions

1. **Does `--dangerously-skip-permissions` alter the dropped-line set?** It certainly alters `system/init` (`permissionMode`, and the absent `--mcp-config` server in `mcp_servers`/`tools`). Whether it alters the `system/task_*` family is unmeasured. Resolution: record the argv verbatim and state the delta; a follow-up capture on the non-yolo daemon shape can compare. Do not silently claim parity.
2. **Model dependence.** `--model haiku` matches #1240. #1218 already proved one subtype's rate is surface-dependent; it may also be model-dependent. Stated as a limitation, not measured here.
3. **Fixture filename when the capture does not fire.** Commit it under the same name — a did-not-fire capture is still a capture of `system/init`, `system/thinking_tokens` and `rate_limit_event`. The record's `outcome` is what tells a reader which claims it supports. The offline validation must therefore **not** require any `task_*` entry.
4. **Second observation of the nudge.** If it appears and matches, `harnessNoOutputNudge` earns the promotion its doc comment describes. That promotion is a change to `internal/streamsup/parser.go` and is **out of scope here** — this ticket captures the observation and pins it; a follow-up ticket makes the constant a set.

---

## Security review

**Verdict:** PASS (after one FAIL round — four MUST FIX findings were found and the spec was revised before this section was written)

**Findings:**

- **[Trust boundaries] MUST FIX — fixed.** The boundary is claude's stdout → record → committed fixture → *public issue comment*, and the first draft applied redaction only to **payloads**. `fifoLiveOutcome` carries the FIFO path in both `Path` and `Detail` (`fifo_reader_liveness_test.go:136-156`), so `pre_rendezvous_read` / `turn_end_read` published an absolute path under the temp `$HOME` with no payload involved — as did `spawn_shape`, `outcome_detail`, `workdir`, and the artifact-dir `t.Logf`. Now: substitution at every field assignment, plus a deny-scan over the **whole marshalled record** as the net, fail-closed. Boundary is two named functions, not scattered.
- **[Trust boundaries] No further findings.** The committed fixture is re-scanned by the offline validation test, so the boundary is enforced again at the point a future re-capture would cross it.
- **[Tokens, secrets, credentials] MUST FIX ×2 — both fixed.** (i) A deny-scan needle taken from `os.Getenv` inverts when the value is empty or short: `bytes.Contains(x, []byte(""))` is always true, and a 2-byte value matches nearly every payload. Now guarded at 16 bytes minimum with an explicit `credential_scan_applied: false` record — an arm that did not run must not read as an arm that found nothing. `bgIdleRedact`'s empty-`realHome` guard (`:821-823`) is the same lesson on the substitution side. (ii) The failure path printed the offending content, which would put the token straight into CI output and invert the control it exists to enforce. Now the message names the class and the entry index only. Creation/rotation/revocation/expiry are **not applicable**: this ticket mints no tokens and reads two existing values solely as search needles, never storing or logging them.
- **[File operations] SHOULD FIX — addressed inline.** Record written `0600`, artifact dir `os.MkdirTemp` `0700` (`bgIdleWriteRecord:813` is the precedent); both matter where `$TMPDIR` is world-traversable. **Path traversal not applicable** — every path is a rig constant joined under a per-test temp `$HOME`; no external input reaches a path. **Symlinks handled** — `fifoLiveRead` gates on `os.Lstat` plus a positive `os.ModeNamedPipe` allowlist (`:145-157`), which is why `/dev/null` cannot answer the reader-presence question. **TOCTOU not applicable** — the Lstat-then-open pair is on a rig-controlled path inside a per-test temp dir with no second writer. **Atomic write not applicable** — a one-shot test artefact; a partial write is caught by the offline JSON-validity check before anything trusts it. One genuine gap found and closed: macOS resolves `/var/…` → `/private/var/…` and `agentrun.ResolveWorkdir` does the same (`runner.go:69-73`), so the substitution table now carries the `filepath.EvalSymlinks` form of every path class alongside the raw one.
- **[Subprocess / external command execution] No findings.** `exec.Command` is called by production code (`runner.go:551`), not by the probe; argv is `buildArgs` plus two rig constants. The prompt reaches claude through `marshalTurnEnvelope`'s structured encoding, which forbids a second physical line by construction (`envelope.go:43-51`), so a prompt cannot forge a `result`, a `control_request`, or an approval. `fifoPath` is a temp path plus a fixed basename with no shell metacharacters (`bgIdlePrompt:450-454`). No `sh -c` in the probe. Environment inheritance is deliberate (`Config.Env` nil ⇒ verbatim inherit, `runner.go:555-557`) and is the reason `os.Environ()` is prohibited in the file. Descendant reap on ctx cancel (`:566-569`) plus the FIFO-EOF backstop covers the double-fork escape: `cat` is orphaned on neither path.
- **[Cryptographic primitives] Not applicable, with the reason.** No crypto, no key material, no comparison against a secret. The `nonce` is `time.Now().UnixNano()` and must **stay** wall-clock — `bgIdlePrompt:451-454` explicitly warns against "upgrading" it to `crypto/rand`, because it is a cache-buster and reading it as a token is the mistake. The session UUID is a fixed literal in a per-test temp `$HOME`, not a secret.
- **[Network & I/O] MUST FIX — fixed.** No sockets, but claude's stdout is untrusted input read into the parent's memory and the first draft capped only the *total* capture: the partial-line accumulator was unbounded, so a child emitting megabytes without a newline would grow it without limit — the exact hazard `defaultMaxParseBuf` closes in the real parser (`parser.go:13-19`). Now capped at 4 MiB with a non-silent drop counter. Total capture capped at 8 MiB, dropping whole lines only, so no payload is ever silently truncated.
- **[Error messages, logs, telemetry] No further findings after the token fix.** Explicit discard handler rather than `Logger: nil`, which would fall back to `slog.Default()` (`runner.go:233-235`). Every `t.Logf` is substituted, and the summary log is emitted only after the deny-scan passes, since output cannot be un-written. No telemetry.
- **[Concurrency] MUST FIX (spec-text) — fixed.** The teardown paragraph stated the `t.Cleanup` LIFO arrows backwards, which would have led a developer to register the record write last and lose the evidence on a `t.Fatalf`. Corrected, with the read-before-release invariant made explicit: releasing the FIFO kills `cat`, so a liveness read after it measures a command the release itself ended. Otherwise: one mutex (no ordering question), `sync.Once` on `resultSeen`, and every goroutine's exit condition enumerated — `cmd.Wait` inside `spawnAndWait` means no `Write` can land after `Run` returns, and the mutex holds regardless.
- **[Threat model alignment] Named, not skipped.** `docs/protocol-mobile.md` § Security model is **not applicable**: the design deliberately does not use the relay/phone rig, so no mobile wire, no noise session, no device identity is involved. The governing threat for this ticket is the one #1223 and #1240 established in code rather than in a document — *published evidence leaks operator data* — and it is what §Redaction is written against. Out of scope and named: this ticket changes no drop behaviour, so it adds no client-facing surface; promoting `harnessNoOutputNudge` to a set is deferred (Open question 4).

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-08-02
