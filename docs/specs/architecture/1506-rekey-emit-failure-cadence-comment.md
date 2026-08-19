# #1506 — Correct `emitRekeyRequest`'s documented recovery contract on seal/marshal failure

**Size:** XS — comment-only, one file, one contiguous doc-comment block. No production behaviour change, no new symbols, no consumer call sites.

## Files to read first

| Where | Symbol | What to extract |
|---|---|---|
| `internal/relay/v2session_rekey.go` | `emitRekeyRequest` | The doc comment being corrected, and the four failure branches it describes (the two `json.Marshal` guards, the `s.send.Encrypt` guard, the `marshalInnerFrameV2` guard). Note that `s.awaitingRekeyReply = true` and the `armRekeyReplyTimer` call sit *after* all four returns. |
| `internal/relay/v2session_rekey.go` | `handleManualRekey` | The `s.rekeyTimer.Stop()` + `s.rekeyTimer = nil` block that runs immediately **before** the `emitRekeyRequest` call. This is the manual-path hole. |
| `internal/relay/v2session_rekey.go` | `rekeyComplete` | The only success-path re-armer: clears `awaitingRekeyReply`, stops/nils `rekeyReplyTimer`, replaces `rekeyTimer` with a fresh `armRekeyTimer`. |
| `internal/relay/v2session_rekey.go` | `armRekeyTimer`, `armRekeyRetryTimer` | The 1-hour cadence vs the #912 short retry cadence — two different helpers, armed at different sites. |
| `internal/relay/v2session.go` | `handleWake` | The `wakeRekeyEmit` arm: the `transportDown()` gate returns *before* the emit and is the sole `armRekeyRetryTimer` site, so the seal-failure path has already passed it. The `wakeRekeyReplyTimeout` arm is what bounds the `awaitingRekeyReply` early skip (it closes the conn). |
| `internal/relay/v2session.go` | `sealError` | The referent of the retained "same posture as `sealError`" clause — confirm it is still a live symbol before keeping the phrase. |
| `internal/relay/v2session_handshake.go` | `handleNoiseInit` | The initial `s.rekeyTimer = m.armRekeyTimer(...)` in the success tail — the third and last production arm site. |
| `internal/noise/noise.go` | `CipherState.Encrypt` | The thin wrapper: it forwards flynn's error verbatim under a `noise: encrypt: %w` wrap. No error of its own. |
| `docs/protocol-mobile.md` | § Re-key | The 1-hour rule the false clause claims to uphold. Cite by section, never by line. |
| `docs/protocol-mobile.md` | § "Out of scope (v2)" | The "Per-message-counter rotation" bullet — the project decision that the 2⁶⁴ counter is not a practical limit. |
| `docs/knowledge/codebase/912.md` | — | The stop-and-nil ordering rationale, in #912's own words. Confirms the manual-path hazard is a known, still-open shape. |
| `cmd/cite-guard/main.go` | package doc | The citation rule the new comment must satisfy. Read before writing, not after `make check` fails. |

## Context

`emitRekeyRequest`'s doc comment closes with a recovery contract for the seal/marshal failure branches. The scheduled half of it is false, and the manual half is misleading:

- **Scheduled clause** — *"the next 1-hour cadence will attempt another emit."* There is no next cadence. All four failure branches `return` before `s.awaitingRekeyReply = true`, so no reply window ticks; and none of them arms anything. `s.rekeyTimer` still holds the **spent one-shot** that delivered this wake — fired, inert, and non-nil.
- **Manual clause** — *"the operator can re-run `pyry rekey` to retry."* True as a statement about the operator, but it sits in a sentence pairing it with the (false) scheduled fallback, which reads as "the cadence covers you either way." It does not: `handleManualRekey` stops **and nils** `s.rekeyTimer` before calling the emit, so a failed manual emit leaves `s.rekeyTimer == nil` and no cadence at all.

Only three production sites arm `s.rekeyTimer` — verified by sweeping both arm helpers across non-test code:

1. `handleNoiseInit`'s success tail (initial arm),
2. `rekeyComplete` (success-path re-arm, responder swap only),
3. `handleWake`'s `wakeRekeyEmit` transport-down branch (#912 retry) — which the seal-failure path has **already passed**, because reaching `emitRekeyRequest` means `transportDown()` was false.

None of the three is reachable from a failed emit. Net effect: the session stays `V2StateOpen` with no scheduled rekey ever again.

**This is claim/code drift, not a live bug**, and the resolution is to correct the comment. The trigger is unreachable: `s.send.Encrypt` wraps flynn/noise's `Encrypt`, whose only two error returns are `ErrMaxNonce` (nonce exhaustion) and `ErrCipherSuiteCopied` (set only by `(*CipherState).Cipher()`, which this repo never calls). `docs/protocol-mobile.md` already rules the 2⁶⁴ counter out as a practical limit. The three marshal branches encode closed structs and cannot fail. The ticket's Technical Notes establish why the code-fix direction is not XS (signature change across both call sites, plus a nonce-setter seam on a crypto boundary to test dead code); that analysis is accepted here — see § Design decision.

### Verification notes for the developer

Two things I checked that change how you should read the ticket:

- **The ticket's `internal/relay/v2session.go:553` and `:564` citations are stale.** Those lines are now `m.drainOnce(runCtx)` and a closing brace in the `Reconnect` arm. The transport-down gate is at `handleWake`'s `wakeRekeyEmit` arm and the retry arm is the `armRekeyRetryTimer` assignment inside it. The ticket's *substance* is correct; only the coordinates rotted — in the ~3 days between filing and now. That is precisely the failure mode `cite-guard` exists to prevent, and it is why the corrected comment must name symbols. Do not copy any `*.go:NNN` coordinate out of the ticket body into the file.
- **Every `v2session_rekey.go` citation in the ticket is still accurate**, as its footer claims. The staleness is confined to the cross-file ones.

## Design decision

**Correct the comment; do not change the code.** This follows the pipeline's Evidence-Based Fix Selection principle: the failure mode has not been observed, cannot be reached without a nonce-exhausted `CipherState`, and the spec declares the state cannot arise. Shipping a re-arm would mean changing `emitRekeyRequest`'s signature and both call sites, or branching the primitive on which caller it serves — and #912 deliberately chose the opposite placement to keep this primitive a pure emit. There is also no way to test the re-arm: `internal/noise.CipherState` wraps its flynn state in an unexported field with no setter, and adding one to test dead code is a genuine footgun (a backwards set is nonce reuse).

The residual risk is documented rather than defended, which is the correct posture for a state the protocol spec rules out.

## What the corrected comment must assert

The deliverable is one contiguous replacement for the final paragraph of `emitRekeyRequest`'s doc comment (the one currently beginning "AEAD-seal failure is realistically unreachable"). Everything above it — the `PRECONDITION (#912)` paragraph, the envelope-ID paragraph, the call-site summary — stays byte-identical.

Each row below is normative content, not prescribed wording. Write it in the file's existing voice.

| # | Must assert | AC |
|---|---|---|
| 1 | Seal failure stays "realistically unreachable", and names `ErrMaxNonce` (nonce exhaustion) as the only reachable trigger. The `sealError` posture reference may be retained. | 4 |
| 2 | On failure the frame is dropped, a WARN line is emitted, the conn is **not** closed, and the session stays `V2StateOpen`. (Unchanged from today — carry it forward.) | — |
| 3 | **Scheduled path:** no further scheduled emit is armed. `s.rekeyTimer` still holds the spent one-shot that delivered the wake — **non-nil, fired, and inert**. | 1 |
| 4 | **Manual path:** `handleManualRekey` already stopped and nil'd `s.rekeyTimer` before calling here, so a failed manual emit likewise leaves no scheduled cadence. The operator's `pyry rekey` re-run is a *recovery step*, not a fallback the cadence provides. | 2 |
| 5 | **Recovery for both:** a phone-initiated re-key (`handleRekeyInit` → `rekeyComplete`) re-arms the cadence; the idle sweep is untouched and still bounds the session. | 3 |
| 6 | The paragraph is scoped to the four failure branches. The `awaitingRekeyReply` early skip is **not** a cadence-death path — it returns while a `rekeyReplyTimer` is in flight, which either closes the conn or is cleared by `rekeyComplete`. | 4 |

### Precision constraints — the ways this comment can be got wrong

These are the specific traps. Each one is a claim that would be false, or would rot, if written the obvious way.

- **Do not write "nil-equivalent" or any wording implying `s.rekeyTimer` is nil on the scheduled path.** It is a non-nil pointer to a fired timer. This is load-bearing: a future re-arm written behind `if s.rekeyTimer == nil` would never fire on the scheduled path (and *would* fire on the manual one). Row 3 must make the non-nil-but-inert distinction explicit enough that the guard-shape mistake is visible.
- **Do not overclaim the idle sweep (row 5).** `idleTimer` re-arms on every inbound frame, so it bounds a session that *goes quiet* — it does **not** bound key lifetime on an actively-used session, which keeps its un-rotated keys until a phone-initiated re-key or teardown. AC 3's "either way" means "in both the scheduled and the manual failure case" (the idle timer is untouched by either), not "regardless of session activity". Say what it actually bounds; do not write a sentence that reads as "so key lifetime is bounded anyway." An accurate limiting clause here is *within* AC 3, not beyond it.
- **Do not restate the nonce arithmetic.** Naming `ErrMaxNonce` and "nonce exhaustion" satisfies row 1. flynn's constant is `MaxNonce = math.MaxUint64 - 1` with the check `n > MaxNonce`, so any restated power-of-two figure is off-by-something and no test pins it. Name the error, not the number.
- **No line-number citations.** `make cite-guard` fails the build on a `*.go:NNN` citation in a `//` comment that resolves to a declaration — which every symbol in this file does. Bare `:NNN` is not flagged by the guard but is banned by convention (it silently inherits the last-named file in the comment). Name symbols. Cite `docs/protocol-mobile.md` by `§` section, matching the two existing references in this same file.
- **Keep it proportionate.** Rows 1–6 fit comfortably in the 15–20 line range. The surrounding paragraphs are dense prose with a bulleted list where structure helps; matching that is fine. This is a doc comment, not a design note.

### Adjacent, out of scope

`Rekey`'s doc comment forwards to "emitRekeyRequest's documented posture" and notes a manual caller receives `nil` even when the emit failed. That stays accurate under this change. Extending it with a matching cadence clause is **permitted but not required** — take it only if it reads cleanly in one added clause. Do not restructure that comment.

## Concurrency model

Unchanged. No goroutine, channel, timer, or lock is added, removed, or re-ordered. Everything the comment describes runs on the manager's single dispatch goroutine (`Run`), under the package's existing no-mutex / no-atomic invariant. The timer callbacks the comment names (`armRekeyTimer`, `armRekeyReplyTimer`, `armRekeyRetryTimer`) each spawn a `time.AfterFunc` goroutine that selects on `m.wake` vs `ctx.Done()`; none of that is touched.

The one concurrency *fact* the comment now records — that a fired one-shot `*time.Timer` remains a non-nil, inert pointer — is a `time` package property, not a new invariant.

## Error handling

No change to any error path. The four `return` sites keep their existing WARN lines and their existing event names (`v2.rekey.emit.marshal_failed`, `v2.rekey.emit.seal_failed`). No new close code, no new sentinel, no signature change.

## Testing strategy

**No new tests.** There is nothing to assert: no behaviour changes, and the branches the comment describes are unreachable by construction (§ Design decision explains why forcing them would require a nonce setter on the AEAD wrapper).

Verification is the diff-hygiene gate in AC 5, which the developer runs directly:

- Every added and removed line in `git diff origin/main -- '*.go'`, after trimming leading whitespace, starts with `//`. Run it and paste the result; a single non-comment line means the change escaped its scope.
- `make check` stays green. This includes `cite-guard`, which is the gate that catches a line-number citation in the new text — expect it to be the one that fires if anything does.

`make check` also runs the existing `internal/relay` rekey tests unchanged; they neither gain nor lose coverage here.

## Open questions

None blocking. One judgement call is delegated: whether to extend `Rekey`'s doc comment with a cadence clause (§ Adjacent). Take it only if it lands in one clause; otherwise leave it.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No findings. The change adds and removes only `//` comment lines; no data crosses any boundary differently. The trust boundary in this file is `handleRekeyInit`'s peer-static continuity check (`bytes.Equal(resp.PeerStatic(), s.peerStatic)`), which this ticket does not touch and whose doc comment is unmodified.
- **[Tokens, secrets, credentials]** No findings. No token, key, or credential handling is described, added, or re-documented. The corrected text names timers and control-flow paths only.
- **[File operations]** Not applicable — no filesystem path, no file is opened, created, or removed by this change.
- **[Subprocess execution]** Not applicable — no `exec.Command`, no shell invocation anywhere in the affected code.
- **[Cryptographic primitives]** No findings, and one deliberate improvement. The comment currently attributes seal failure vaguely to "correct flynn/noise"; the corrected text names `ErrMaxNonce` as the sole reachable trigger, which is *more* precise about the crypto failure surface, not less. The change adds no primitive, no RNG use, and no comparison. Critically, it does **not** add a nonce setter to `internal/noise.CipherState` — that was considered as a testing seam and rejected in § Design decision, because a backwards nonce set is nonce reuse under a live key. Rejecting that seam is the security-positive outcome of this ticket.
- **[Network & I/O]** No findings. No socket read, no size cap, no timeout value changes. The `rekeyReplyTimeout` window and the idle sweep the comment references keep their current values.
- **[Error messages, logs, telemetry]** No findings. All four WARN lines keep their existing event names and field sets; no new field, and nothing logged that was not logged before. Worth noting for the reviewer: the existing lines carry `conn_id` only — no payload, no key material, no device name — and this change preserves that.
- **[Concurrency]** No findings. No goroutine, lock, channel, or timer is added or re-ordered (§ Concurrency model). The single-owner-goroutine invariant on `s.send` / `s.state` / `s.rekeyTimer` is unchanged.
- **[Threat model alignment]** One finding, resolved as accepted-and-now-documented rather than fixed. `docs/protocol-mobile.md` § Re-key mandates a 1-hour key rotation. On the four failure branches that rotation silently stops for the life of the session, and because `idleTimer` re-arms on every inbound frame, an *actively-used* session would then run with unbounded key lifetime — a real weakening of the forward-secrecy posture, if it were reachable. It is not: the sole trigger is nonce exhaustion, which the same document rules out as impractical in § "Out of scope (v2)". The correct treatment is therefore to document the residual precisely (which is this ticket) rather than to defend an unreachable state with an untestable re-arm. **This is the finding that motivated the `security-sensitive` label, and the spec's answer to it is row 5 plus the idle-sweep precision constraint** — the comment must not let a future reader believe the idle sweep bounds key lifetime on an active session, because that belief is what would make this residual invisible if the trigger ever became reachable (e.g. a future caller that seals far more frames per session, or a `Cipher()` call appearing in-repo). Classified **SHOULD FIX, addressed in-spec**; no MUST FIX remains.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-08-19
