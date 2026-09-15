# 2474 — Does a Read outside the workspace prompt under the in-band `default` posture?

## Files read

- `internal/e2e/realclaude/interactive_stream_modal_resolution_test.go` → `startObservedPermissionHarness` — the harness this probe inherits. It seeds no stored bypass, pairs the phone with `AllowRemotePermissions: true`, and takes a `configure` hook that swaps the claude binary for the observer shim. The posture under test is inherited, not constructed.
- `internal/e2e/realclaude/permission_context_test.go` → `permissionObservation`, `runPermissionObserver`, `startPermissionObserver`, `TestInteractiveStreamStdioModalResolution` — the observer that wraps claude's stdout, and the **Write** half of this question. Its outside-directory arm is the same-posture data point that points toward "gated".
- `internal/e2e/realclaude/interactive_stream_attachment_read_test.go` → `TestInteractiveStreamAttachmentRead`, `mintAttachmentToken`, `writeTokenFile`, `drainTurnText` — the **Read** half, and the AC-3 witness pattern. Also the wrong-posture twin: it spawns with `--dangerously-skip-permissions`, so its "no modal" says nothing about `default`.
- `internal/e2e/realclaude/interactive_stream_permission_deny_test.go` → `denyModalsUntilIdle` — the answer-modals-until-idle loop shape, with the retry cap this probe copies in the opposite verb.
- `internal/streamsup/runner.go` → `SetPermissionMode`, `SpawnPermissionMode` — the in-band posture write every spawn issues before a user turn reaches the child.
- `internal/streamsup/parser.go` → `noteControlAck`, `emitModelList` — tells the three `control_response` shapes apart: a `set_permission_mode` success carries `mode` alone, a NAK carries `error` and no inner response, an interrupt ack no inner response at all. `emitModelList`'s undecodable arm is the argument for not declaring the NAK's `error` field.
- `internal/sessions/session.go` → `claudeSettingsArgs`, `permissionModeDefault` — why the launch argv is not the running posture (#2065).
- `internal/sessions/handoff.go` → `handoffNotePathFor` — the directory shape the probe's file must occupy: `<data dir>/handoff-notes/<conversation id>.txt`.
- `internal/e2e/realclaude/stdio_permission_prompt_test.go` → `stdioPermissionSafeLabel` — the 64-byte alphanumeric allowlist every claude-authored label goes through before it reaches a log.
- `internal/e2e/realclaude/permission_offer_diagnostic_test.go` → `TestPermissionObservation_PreservesAlwaysAllowSource` — one of the two round-trips of `permissionObservation` through `json.Marshal` that constrain how the type may be widened; `TestPermissionObservation_PreservesReasonPresence` in `permission_context_test.go` is the other. `writePermissionOfferDiagnostic` is in the same file and is **not** one of them — it marshals `permissionOfferDiagnostic`, a separate record built from selected fields.
- `docs/knowledge/features/e2e-realclaude.md` § "Test infrastructure" — the #2416 no-retry rule, and the rule that a live gate's budget constant states its margin against the production timeout it proves early.

## Context

Juhana decided on 2026-09-06 that a conversation reset runs a wrap-up turn whose reply becomes a handoff note for the successor. #2467 shipped the store: the note lives at `<data dir>/handoff-notes/<conversation id>.txt`, which is not inside any session's workspace. #2475 wants to hand the successor that path and let it decide whether to read — which only works if the read is ungated under the posture sessions actually run in. If it prompts, #2475 becomes the inline fallback instead.

The question is live, not rhetorical, and the two nearest measurements disagree:

- `TestInteractiveStreamAttachmentRead` (#2039) saw a live claude read an absolute path outside its cwd with **no modal** — but under `--dangerously-skip-permissions`. That measures a bypassed child. It is the most likely source of a wrong assumption here, because the finding is written down in terms that read like a general answer.
- `TestInteractiveStreamStdioModalResolution`'s outside-directory arm saw 2.1.259 raise a modal with `reason_type: workingDir` for a **Write** — under a harness whose session posture *is* the in-band default.

Different posture, different permission class. Whether a **Read** is gated the way that Write was is what no run has measured.

This slice observes. It changes no production code and decides nothing; #2475 owns the design decision the finding licenses.

**No ADR.** This records a measurement about upstream claude behaviour at one version, not a choice about our own boundaries. Its home is the package overview, which the documentation stage owns.

## Design

One new file, `internal/e2e/realclaude/interactive_stream_default_posture_read_test.go`, plus a strictly additive widening of the shared observer.

### The measurement

`TestInteractiveStreamDefaultPostureOutsideWorkspaceRead` drives one live turn:

1. Stand up `startObservedPermissionHarness(t, permissionDaemonModel, true, configure)` — the same harness whose Write arm raises a modal, so the posture is inherited rather than asserted into being.
2. Mint a 96-bit token from `crypto/rand` and write it to `<home>/.pyry/test/handoff-notes/<convID>.txt` — the `handoffNotePathFor` shape, under the daemon data dir, outside the workspace (`<home>/work`).
3. Witness the posture ack (below) before reading the turn's frames.
4. Send one message naming that absolute path and asking for its exact contents.
5. Drain to terminal idle, **answering any permission modal `allow_once`** and recording that it was raised.
6. Assert the token is in the accumulated reply; log the finding.

The branch structure is the whole point: the probe passes on **either** answer and reports which. A modal raised is answered so the turn terminates; a modal absent lets the turn complete on its own. Answering `allow_once` rather than denying is what keeps AC-3's witness available on both branches — a denied read produces no token, and the run could then not tell a gated read from a read that never happened.

### The posture witness (AC 2)

The success `control_response` to the spawn's `set_permission_mode` is the observable. Its inner response carries `mode` alone, per `emitModelList`'s shape analysis, so `subtype == "success" && mode == "default"` is a sound discriminant against the initialize ack (`models`/`commands`) and the interrupt ack (no inner response).

`runPermissionObserver` forwards only `can_use_tool` requests and `result` lines today. It gains a third arm, **gated on a new environment variable that only this probe sets**, forwarding `control_response`. `permissionObservation` gains one field:

```go
Response *struct {
    Subtype  string `json:"subtype"`
    Response struct {
        Mode string `json:"mode"`
    } `json:"response"`
} `json:"response,omitempty"`
```

A **pointer with `omitempty`** is load-bearing rather than stylistic. `runPermissionObserver` re-encodes the whole observation over its socket, and both `TestPermissionObservation_PreservesAlwaysAllowSource` and `TestPermissionObservation_PreservesReasonPresence` round-trip the type through `json.Marshal` and assert presence semantics over the result; a nil pointer is omitted, so every one of those paths' bytes is unchanged. A value struct would add a `"response":{...}` key to all three.

The env gate is what keeps the widening inert for the two existing observer callers. The observations channel is capacity-16 and its accept loop **returns permanently when the buffer fills** — so broadening what crosses it unconditionally could silently end observation for `TestInteractiveStreamStdioModalResolution` and `TestInteractiveStreamStdioAlwaysAllowIsSessionScoped`. Gated, only this run carries the extra traffic, and this run drains the ack before the turn drain.

The NAK's `error` field is **not declared**, deliberately. `subtype != "success"` already identifies a NAK, and the string is claude prose that would otherwise reach a test log — the channel `noteControlAck` refuses to open for exactly this reason. A field never declared cannot reach a log.

Ordering: the ack is drained after the message is sealed but before the phone's turn frames. The two transports are independent, so nothing is lost either way, and this ordering is correct whether the bootstrap child spawns eagerly at daemon start or lazily on the first turn. The claim that the read happened *under* `default` rests on the runner's own contract — the posture write precedes any user turn reaching the child — which the ack confirms the child accepted.

### Contracts introduced (all unexported, all in the new file)

- `awaitDefaultPostureAck(t, observations) ` — scans observations until a success `control_response` naming `default` arrives, or fails within the budget. Tolerates and discards sibling acks.
- `driveOutsideWorkspaceRead(t, h, convID, path, startReqID) (reply string, tools []string, modal *postureProbeModal)` — seals the prompt, drains to terminal idle, answers each permission modal `allow_once` up to a cap, returns the accumulated text and whichever modal was raised first.
- `writeHandoffShapedNote(t, home, convID, token) string` — creates `handoff-notes/` at `0700` and the note at `0600`, returns the absolute path.
- `mintPostureProbeToken(t) string` — 12 bytes of `crypto/rand`, hex.

The frame loop inside `driveOutsideWorkspaceRead` is the package's standing one (decrypt every `noise_msg` in arrival order; skip a non-`noise_msg` control frame without decrypting). It is a local copy rather than a widening of `drainTurnText`, which hard-fails on a modal by design and has its own caller.

## Concurrency model

No new goroutines. The observer's listener goroutine is `startPermissionObserver`'s, unchanged and already cleaned up via `t.Cleanup`. The phone stays the run's **only** reader — every `noise_msg` decrypted exactly once in arrival order, or the sequential receive nonce desyncs into a decrypt failure that reads like a daemon bug. Observations are read from the test goroutine only, on a different transport, so interleaving the two drains is safe. Shutdown is the harness's existing `t.Cleanup` chain (daemon stop, phone close, relay close, listener close).

## Error handling

Every failure names the milestone that was missed rather than reporting a bare timeout:

- No posture ack within budget → the child never acknowledged `default`; a "no modal" result from this run would be meaningless. This is AC 2's fail-closed branch and it covers the NAK case too (a NAK arrives with a non-success subtype and never satisfies the discriminant).
- No non-empty `assistant_delta` → the message never produced a turn.
- Delta but no terminal `turn_state{idle}` → the turn never closed; the message names how many modals were answered.
- More modals than the cap → claude is reissuing past the cap; fail rather than widen (`denyModalsUntilIdle`'s stance, same reasoning).
- `TypeError` envelope → fail naming the code.
- Token absent from the reply → the read did not reach the file. Reported with the reply truncated at `questionTextLogCap` and the tool names observed, so a refusal is distinguishable from a silent no-op.

Every claude-authored string reaching a log is bounded: labels through `stdioPermissionSafeLabel`, prose through `truncateString`.

### Budget

One dedicated constant, `outsideReadTurnBudget = 120 * time.Second`, and its size is part of the assertion (#2416). It must stay well under `mcpApprovalTimeout` — ten minutes unless `PYRY_APPROVAL_TIMEOUT` overrides it — because an unanswered modal parks the turn until that window elapses. At 120s the margin is 5×, so a park fails as a budget miss with a named milestone rather than as a ten-minute wall clock. The prompt also carries the #2416 no-retry clause, so a denied or failed call cannot raise a second modal this probe would have to answer.

## Testing strategy

The probe **is** the test; it is a standing live gate, not a deterministic RED/GREEN oracle. It compiles only under `e2e_realclaude` and `make check` never sees it.

Non-vacuity rests on three independent legs, each failing differently:

1. **The token.** 96 bits from `crypto/rand`, existing only inside a file outside the workspace that no ordinary listing of the workspace reaches. The probe asserts its own prompt text does not contain the token before sending, so the claim survives a later edit of the prompt. This separates "no modal because ungated" from "no modal because the read never happened" (AC 3).
2. **The posture ack.** Without it the run cannot claim `default`, and fails (AC 2).
3. **The modal branch is recorded, not asserted.** Neither answer is a failure — which is why legs 1 and 2 have to carry the weight.

Offline verification for this slice is `go vet ./...`, `go build ./cmd/pyry`, and a tagged **compile** of the package (`go test -tags e2e_realclaude -run '^$' ./internal/e2e/realclaude/`) — `make check` cannot see this package, so a compile check is the only offline signal that it builds at all. The live run is the dispatcher's `needs-real-claude` gate after verification; the finding is recorded from that run's output.

## Open questions

1. **Does the bootstrap child spawn eagerly at daemon start or lazily on the first turn?** Resolved by design rather than by measurement: draining the posture ack after the message is sealed is correct either way, so the probe does not depend on the answer.
2. **What does the run actually measure?** Unresolvable before the live gate. The test file carries a `MEASURED` block to be filled from the gate's output with the question, the answer, the claude version and the date (AC 4), and the PR body restates it. Until that run, the block states it is pending and names the run that fills it.

## Documentation handoff

Pending for the documentation stage; not done here.

1. **Record the finding** in `docs/knowledge/features/e2e-realclaude.md`, under `## Test infrastructure`, beside the existing permission-protocol paragraphs — the ones contrasting the 2.1.143 spike with 2.1.259's `can_use_tool` behaviour and recording the outside-directory Write's `workingDir` reason. State the question, the answer, and the claude version it holds for. The document is ~24KB at `95603a47`, well under the 50000-byte cap, so this is a paragraph, not a split.
2. **Correct the #2039 finding in the same pass.** `docs/knowledge/codebase/e2e-realclaude-interactive-stream-attachment-read-test-go.md` records "a live claude opens an absolute path outside its own cwd without hesitation, when told to read it" as a lesson that outlives its ticket, noting the bypass spawn only parenthetically. Scope that sentence to the bypass posture so the next reader cannot take it for a `default` observation. #2475's design turns on this distinction.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** The design adds exactly one new crossing: claude's stdout → `permissionObservation.Response` → the probe's posture assertion. It is subprocess-stdout data and therefore untrusted. Two fields cross and no more; the NAK's `error` string is deliberately not declared, because `subtype != "success"` already identifies a NAK and the string is unbounded claude prose that would reach a test log. The existing boundary in `runPermissionObserver` stays the single place any of this is parsed.
- **[Trust boundaries]** MUST FIX (addressed in this plan before commit): both forwarded strings reach failure messages, so both go through `stdioPermissionSafeLabel` — 64-byte cap, `[A-Za-z0-9_.-]` allowlist — before any `t.Fatalf`/`t.Logf`. Without it a hostile `subtype` forges log lines. Same obligation on `ModalShownPayload.Title` and `ReasonType`, and on the reply text via `truncateString`. Recorded in § Error handling.
- **[Tokens]** The witness is 96 bits from `crypto/rand`, never `time.Now().UnixNano()` — #2039's argument holds unchanged: a nanosecond timestamp is a value a model could plausibly produce, which would dissolve AC 3. It **authorises nothing**: it is a witness, not a credential, so rotation, revocation and expiry do not apply and it is deliberately fine for it to appear in a failure log. Lifecycle is one run; it dies with the temp HOME.
- **[File operations]** The note path is `filepath.Join` over the harness home and a compile-time-constant canonical conversation id — no untrusted component, so no traversal and no canonicalisation gap. No check-then-use on any caller-controlled path, so no TOCTOU. Modes stated explicitly: `0700` for `handoff-notes/`, `0600` for the note, matching `seedBootstrapRegistry` and what `attachments.Store` writes the daemon-side copy at. No symlink following, no atomic-write requirement — it is a fixture in a disposable HOME, not durable state.
- **[File operations]** SHOULD FIX — the probe writes into the daemon's **live** data dir while the daemon is running. Today nothing enumerates `handoff-notes/`; #2467's store addresses `<id>.txt` by exact path. The write happens after the daemon is up and uses this run's own conversation id, so it cannot collide. If a later daemon enumerates that directory at startup, this fixture becomes load-bearing in a way it is not today — the verifier should re-check the assumption if #2475 changes the store's read path.
- **[Subprocess]** No new process is spawned. The shim's `exec.CommandContext(ctx, realBin, os.Args[1:]...)` is untouched, argv-form (never `sh -c`), and the new value is read from the environment, never passed as an argument. Env inheritance is the existing harness's.
- **[Cryptographic primitives]** `crypto/rand` for the only randomness. No new primitives, no key or nonce reuse. `strings.Contains` is the right comparison for the token and `crypto/subtle.ConstantTimeCompare` would be **wrong** here — the token is a synthetic witness, not a secret, and the operands differ in length. Stated so a later "hardening" does not introduce it.
- **[Network & I/O]** No new socket or listener. The observer's loopback connection keeps its existing `io.LimitReader(conn, 2<<20)` cap and 5s deadlines; the new fields decode inside them. The real hazard is the capacity-16 observations channel whose accept loop returns permanently when full — the env gate confines the extra traffic to this run, and this run drains the ack before the turn drain. Recorded in § Design.
- **[Error messages, logs, telemetry]** Covered above: labels allowlisted, prose truncated. The absolute note path reaches the prompt and failure messages and carries the temp HOME — the same exposure `TestInteractiveStreamStdioModalResolution` already accepts for its own `target`, in a disposable directory. Considered and declined. No telemetry. **Superseded in part by the 2026-09-16 revision:** the probe now *does* publish an artifact, so the rules that apply to one apply here — see § Revisions for the record's field discipline and its deny-scan.
- **[Concurrency]** No goroutines are spawned, so none can leak. The phone stays the run's only reader — the receive-nonce rule — and observations are read from the test goroutine on a separate transport. No locks, so no ordering to document. Shutdown is the harness's `t.Cleanup` chain.
- **[Threat model alignment]** This slice measures a permission boundary and moves none: zero production files change, so it cannot weaken what it observes. The security-relevant output is the finding itself. OUT OF SCOPE for #2475: if the read proves ungated, then any text reaching claude's prompt can name an absolute path it will open, so #2475 must establish that the composed path is daemon-derived and cannot be influenced by user- or claude-controlled input. Named here so that ticket inherits the question rather than rediscovering it.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-16

## Revisions

### 2026-09-16 — as-built notes

**Open question 1 (eager or lazy bootstrap spawn) stands resolved by design, unchanged.** The ack is drained after the message is sealed and before the phone's turn frames, which is correct either way, so the probe never had to learn the answer. No design change.

**Open question 2 (what the run measures) remains open by construction** and is this ticket's live-artifact handoff — see below. The `MEASURED` block in `interactive_stream_default_posture_read_test.go` says PENDING and names the log line that fills it.

**The branch came in at 808 lines added against the 800-line one-ticket ceiling — 1% over, stated rather than rounded away.** The § A4 self-check measured the written plan at ~530 and held; the overage is entirely doc-comment prose in the probe itself (the posture argument, the two-disagreeing-measurements framing, and the non-vacuity reasoning), which is the part of this file that makes a green run mean something.

It is not split, and the reason is the floor rule rather than an appeal to the edits being easy. The ticket has **one** deliverable — one live measurement — so there is no seam to cut on: any child would produce a slice whose only consumer is its sibling, which § A1's floor says is part of that sibling, and the floor wins over the ceiling when they disagree. The ticket's own `Estimate:` line predicted this range from the right analogue: #2039, the nearest read-shaped probe, spent 617 lines on its test file, and this one spent 632. Zero production files change, zero call sites need updating, and the fan-out check is inapplicable. Flagged for the verifier as a measured overage, not a silent one.

### 2026-09-16 — rework: the finding is a committed capture, not a log line

Driven by the verifier's MUST FIX on PR #2476. The design above put the probe's entire deliverable in a `t.Logf`, and **both branches pass by design**, so under `make e2e-realclaude` the answer would have been discarded by the run that spent the tokens to get it — the #1763 class `CLAUDE.md` § Testing records.

**The verifier offered two remedies and I measured that only one of them works.** It suggested either a durable `testdata/` capture or "make the finding reach stdout on a non-verbose passing run". The second does not exist. `make e2e-realclaude` is `go test -tags e2e_realclaude -count=1 ./internal/e2e/realclaude/...` — **package-list mode** — and in package-list mode `go test` buffers a passing package's entire output and drops it, direct `os.Stdout` writes included. Measured 2026-09-16 with a throwaway package writing one `t.Logf` and one `os.Stdout` line:

| invocation | what survived |
|---|---|
| `go test -count=1 ./...` (the Makefile's shape) | neither line — only `ok  stdoutcheck  0.153s` |
| `go test -count=1 -v ./...` | both lines |
| `go test -count=1` (local-directory mode) | the `os.Stdout` line only |
| `./...` with a failing test | both lines — failure flushes the buffer |

The `os.Stdout` escape hatch is real but only outside package-list mode, which is the distinction that makes it a trap worth writing down. So on a passing run under this gate **the only thing that survives is a file on disk**, and the capture is not the better option, it is the only one.

The design change: `defaultPostureReadFinding` is marshalled to `testdata/default_posture_outside_read_v<version>.json` by `writeDefaultPostureReadFinding`, **before** the run's own assertions — `TestRealClaude_InBandModeSwitch_Probe`'s discipline, so the evidence lands either way and a run that measured the wrong thing says so on disk via `read_witnessed: false` rather than vanishing. The version is in the name because the answer is version-scoped; a new claude mints a file beside the old rather than over it. `TestDefaultPostureReadCaptureName` pins the name out of `fixtureGlob`, `dropcapFixtureGlob` and `setModeFamilyGlob`, each row carrying a control so a glob that stopped matching anything cannot pass as green.

**A committed artifact changes the security posture**, which is why § Security review's "publishes no artifact" line is superseded. The record carries the answer and never the bytes it was read out of: no reply text (byte count and a `read_witnessed` bool instead), no absolute path (`filepath.Base` only), and every claude-authored string stored already through `stdioPermissionSafeLabel` rather than merely passed through it at a log call. `writeDefaultPostureReadFinding` then deny-scans the marshalled bytes with `newDropcapScanner` — the same scanner `writePermissionOfferDiagnostic` uses — and fails the run writing nothing on a hit. Nothing is expected to trip it; that is the state in which the scanner is worth running, since it checks that the field choices stayed true rather than redacting for them. `TestDefaultPostureReadFindingCarriesNoProse` pins the same property offline, where a field added later would otherwise sail past a scanner looking only for credentials and homes.

Also in this revision, from the same review:

- **SHOULD FIX — the modal is now tied to this read.** `postureProbeModal` records `ToolLabel` from `ModalShownPayload.Title`. Without it, a modal raised anywhere in the turn — claude reaching for `Bash cat` instead of `Read` — filed as "the read was gated" and the `MEASURED` block would have been written from that headline. `BlockedPath` is reduced to `BlockedPathPresent` in the same pass: its presence is the finding and the path itself is claude-authored text the record has no use for.
- **SHOULD FIX — the miscitation is corrected in three places** (the field's doc comment, the offline test, and § Design above). What constrains the pointer-plus-`omitempty` is `runPermissionObserver` re-encoding the whole observation over its socket plus the two `TestPermissionObservation_Preserves*` round-trips — **not** `writePermissionOfferDiagnostic`, which marshals a separate record. The decision was right and only the reason was wrong, which on a shared type in a `security-sensitive` change would have pointed a future exposure audit at bytes that are never written.
- **NIT — the budget claim is now true rather than downgraded.** `outsideReadTurnBudget` was armed twice in sequence, making the real worst case 240s against a comment claiming a 5× margin. Both helpers now share one wall-clock deadline computed once, so 120s bounds the whole measurement and the margin is the 5× the constant says.

## Documentation handoff — live-artifact dependency

AC 4 requires the finding — the question, the answer, the claude version and the date — to be **recorded in the test file**, and only the live gate can produce it. This ticket therefore carries `needs-live-artifacts` alongside `needs-real-claude`:

- **Capture source:** `internal/e2e/realclaude/testdata/default_posture_outside_read_v<version>.json`, written by the run itself. The `#2474 finding:` line the probe logs carries the same content, but is visible only under `-v` — see the revision above. **The capture file is the artifact to recover and `git add`;** a pipeline run happens in a worktree that is removed when the run ends, so an uncommitted capture goes out with it.
- **Commit target:** the capture under `testdata/`, plus the `# MEASURED` block in `internal/e2e/realclaude/interactive_stream_default_posture_read_test.go`, filled from it in place of `PENDING THE LIVE GATE`.
- **Coupled changes:** none. No reader, schema or fixture depends on the capture; `TestDefaultPostureReadCaptureName` already pins it out of every sibling family's sweep, so no existing suite starts reading it.
- **Offline checks to re-run after the capture lands:** `go vet ./...`, `go build ./cmd/pyry`, `make cite-guard`, and the tagged compile plus offline tests (`go test -tags e2e_realclaude -race -run 'TestDefaultPostureAckDiscriminant|TestDefaultPostureReadCaptureName|TestDefaultPostureReadFindingCarriesNoProse|TestPermissionObservation' ./internal/e2e/realclaude/`).
- **Arming:** the probe fails rather than measuring when `claudeVersion()` is empty or collapses to `<empty>`/`<invalid>`, because a finding that cannot name the version it holds for is not a usable record. The writer fails loudly the same way on a deny-scan hit or an unwritable `testdata/`, so a run that cannot land its capture reds instead of exiting green. None of this asserts that the `MEASURED` block is already filled, which would make the gate that fills it unable to run.
