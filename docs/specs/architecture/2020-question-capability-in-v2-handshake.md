# #2020 — advertise `question` in the v2 handshake capability set

## Files read

- `internal/protocol/handshake.go` → `CapabilityInteractive`, `HelloClientPayload.Capabilities`, `HelloAckPayload.Capabilities` — the wire vocabulary layer the new constant joins, and the doc-comment shape the new constant must mirror.
- `internal/relay/v2session_handshake.go` → `supportedV2Capabilities`, `negotiateCapabilities`, `handleNoiseInit` — the daemon's authoritative supported set, the intersection, and the single site where the negotiated slice is both echoed in the ack and reduced to `s.interactive`.
- `internal/relay/v2session_handshake.go` → `decodeInnerFrameV2`, `maxNoisePayloadBytes` — the 65535-byte bound on decoded inner-frame `data`, which is what bounds the size of a phone's advertised capability slice. Load-bearing for the security pass's DoS answer.
- `internal/relay/v2session_test.go` → `TestNegotiateCapabilities`, `TestV2Session_Handshake_CapabilityNegotiation`, `driveToOpenCaps`, `decodeHelloAck`, `activeConnFor` — the two tables this ticket extends and the three helpers the new rows reuse verbatim.
- `internal/relay/v2session_questionreconcile.go` → `reconcileQuestions` — the non-goal. Its gate reads `!s.interactive`, and it stays that way; the new string grants nothing.
- `docs/protocol-mobile.md` § Capability negotiation — the ADR-025 superseded-as-a-requirement amendment, the "Defined capability strings" table, and the intersection paragraph. All three are edited or must be read before editing.
- `docs/protocol-mobile.md` § Security model → Threats 3 and 7 — the declared dispositions the security pass aligns against (metadata observability, DoS).
- `docs/knowledge/features/v2-session-manager-state-machine-capability-negotiation-on-the-handshake.md` — #626's overview. **Carries the lesson that governs this ticket:** its closing sentence reads *"The single-`Interactive`-bool shape is the right shape while `supportedV2Capabilities` has one member (YAGNI); a second capability is a deliberate, separately-reviewed change."* This ticket is that change, and the `security-sensitive` label is why it gets the review below.
- `docs/knowledge/features/protocol-package-handshake-control-payloads.md` — #607's overview; confirms `Capabilities` is advertisement-only at the wire-type layer with no enforcement, so no protocol-side validation belongs in this diff.
- `docs/knowledge/features/protocol-package-drift-detectors.md` — checked to confirm the drift detectors cover `Type*` constants and `Code*` constants only. **No detector enumerates capability constants**, so this ticket adds no registry entry. Matches the ticket's grep-confirmed "no registry or guard to update".

## Context

The v2 handshake negotiates capabilities, but `supportedV2Capabilities` has held exactly one string since #626. The whole inbound-question feature (#1979 and siblings) shipped without adding to it, so a client has no way to ask a daemon whether it understands questions — `pyry --version` returns an unordered commit sha.

That gap has already cost a day. pyrycode-desktop#928 drives the question panel against a live daemon; the Mac's installed daemon predated the question work, and the harness could only gate on a `pyry` binary existing. Its skip message states the requirement in prose — "built from a #820+#854-inclusive tree" — which nothing checks.

**Negotiation is an intersection, not a broadcast.** `negotiateCapabilities` iterates the daemon's supported set and keeps only entries the client also advertised, so the daemon never echoes a string the client did not ask for. A consumer detects question support by advertising `question` in its own `hello` and reading it back out of the `hello_ack`; a daemon built before this lands drops the string, which is precisely the stale-daemon signal #928 needs. An unconditional append to the ack would break the documented trust property ("never a blind mirror of the phone's claims") and is not what this ticket does.

**The convention this establishes.** A cross-repo wire feature adds its capability string, or clients cannot tell which daemon they are talking to. One string per user-facing wire feature beats an orderable version number: a string survives cherry-picks and backports and says what is supported rather than when it was built.

**On an ADR.** The convention above is a genuine cross-repo design commitment, and I would put it in a decision record. This phase does not write under `docs/knowledge/`, so I am naming it here: **the documentation phase should consider an ADR for "capability strings, not version numbers, are how clients detect daemon features"**, with #2020 and pyrycode-desktop#928 as the motivating pair.

## Design

Two production edits, both additive; no branch is rewired and no consumer call site moves.

**1. `internal/protocol/handshake.go` — the vocabulary constant.**

```go
const CapabilityQuestion = "question"
```

Declared beside `CapabilityInteractive` with a doc comment in the same shape as its neighbour: what a client advertises it for, that the daemon echoes it when supported, the spec cross-reference, and — the one clause that differs — that this string is **detection only** and grants no access, with the interactive flag named as the thing it does not confer. Pure vocabulary, no methods, no validation, matching the layer's stated contract.

Naming note: `cmd/pyry/question_resolve_v2.go` already declares an unexported `classQuestion = "question"`. Different package, different purpose (a classification label, not wire vocabulary) — no collision, and the new constant must not be wired to it.

**2. `internal/relay/v2session_handshake.go` — the supported set.**

```go
var supportedV2Capabilities = []string{protocol.CapabilityInteractive, protocol.CapabilityQuestion}
```

The constant, never a bare literal. Everything downstream follows from the existing shape with no further edit:

- `negotiateCapabilities` iterates the supported set, so `question` is echoed **only** when the client advertised it. Supported-set order puts `interactive` first, which fixes the expected slice order for the two-capability test row.
- The ack literal in `handleNoiseInit` already carries `Capabilities: negotiated`, so the echo needs no change.
- `s.interactive` is `slices.Contains(negotiated, protocol.CapabilityInteractive)` — a **value-specific** membership test, not an emptiness test. A question-only advertisement therefore leaves the flag `false` by construction. This is the property AC-3 exists to pin, and it is the one line a future "simplification" could break; see the security review's trust-boundary finding.

**Data flow, unchanged in shape:** untrusted `hello.payload.capabilities` → `negotiateCapabilities` → one `negotiated` slice → both the sealed `hello_ack` echo and the `s.interactive` reduction. Single source of truth; ack and flag cannot disagree.

**Non-goal — no gating change.** `question_shown`, `question_resolved` and the connect-time question reconcile stay gated on the negotiated `interactive` flag alone (`reconcileQuestions`). Putting any of them behind the new string would cut off pyrycode-mobile, which advertises only `interactive`. The existing suite already pins this without a new test: every case in `internal/relay/v2session_questionreconcile_test.go` opens its conn with `[]string{protocol.CapabilityInteractive}` and asserts the batch still arrives, so a gating change turns that file red.

**Nothing else reads the new constant** after this lands. The consumer side is pyrycode-desktop#928's work.

**3. `docs/protocol-mobile.md` § Capability negotiation.** Add a `question` row to the "Defined capability strings" table stating that the client understands inbound question batches and that the string is a feature-detection signal granting no access. Add a sentence establishing the second, still-live use of the field so the section no longer reads as ruling this out: the ADR-025 amendment retires *old-phone interop* as a requirement, and that stands — but a client asking a daemon of unknown build which wire features it implements is a different use, and the intersection is what makes it work.

## Concurrency model

No new goroutines, channels, locks, or timers; nothing in this diff is spawned or awaited.

`supportedV2Capabilities` is a package-level `var` (a slice cannot be `const`), initialised at package init and read-only thereafter. `negotiateCapabilities` only reads it, and its sole production caller `handleNoiseInit` runs on the manager's Run goroutine. Adding a second element does not change that: the invariant to preserve is that **nothing ever writes the slice or its backing array after init**, which no code does today and this diff does not add.

`s.interactive` keeps its set-once / single-owner-goroutine discipline — written once in `handleNoiseInit`'s token-OK tail before `s.state = V2StateOpen`, read afterwards through the `ActiveConns` snapshot funnel. Re-key preserves it by never touching it. Unchanged.

## Error handling

No new failure mode is introduced, and no new error path is added.

- `negotiateCapabilities` is total: it cannot fail, allocates only on a match, and returns `nil` for advertise-nothing / only-unsupported. Adding a supported entry does not change any of that.
- A client advertising an unknown string still has it silently dropped — the pre-existing, correct behaviour for a vocabulary field, and the reason a pre-#2020 daemon is detectable at all.
- The ack payload grows by roughly a dozen bytes when `question` is granted. `json.Marshal` of `HelloAckPayload` has no realistic new failure, and the outbound `noise_resp` early-data slot is not bounded by `maxNoisePayloadBytes` (that cap guards *inbound* decode in `decodeInnerFrameV2`). The existing marshal-failure branch already closes at `StatusHandshakeFailure` and needs no change.
- Deliberately **not** added: any log line echoing the client's advertised set. That would put attacker-controlled, unbounded strings into the log channel for no operator benefit. See the security review.

## Testing strategy

Both tables live in `internal/relay/v2session_test.go`. No existing row changes meaning: every current row advertises only `interactive` and/or an unsupported string, so its expected intersection is unchanged. RED is established by adding the rows before the production edits — each new row fails on the old supported set for the right reason (`question` absent from the negotiated slice), which is the RED I watch before writing either production line.

**`TestNegotiateCapabilities` — new rows on the pure intersection:**

- `question` alone → `[question]`.
- both advertised → `[interactive, question]` in **supported-set order**, which pins the ordering contract rather than merely set membership.
- reversed advertisement order (`[question, interactive]`) → still `[interactive, question]`, so the row above cannot pass by accidentally mirroring the client's order.
- `interactive` alone → `[interactive]`, unchanged — the existing row already covers the "does not leak an unadvertised supported string" direction, which is the failure mode an unconditional append would have.

**`TestV2Session_Handshake_CapabilityNegotiation` — new rows through a real handshake**, driven by the existing `driveToOpenCaps` (advertised set in, sealed ack out), `decodeHelloAck`, and `activeConnFor`. One row per acceptance criterion:

- **AC-1** advertise `[interactive, question]` → reaches open; ack carries both literal wire strings; `Interactive` flag `true`.
- **AC-2** advertise `[interactive]` → ack carries `interactive` and **not** `question`; flag `true`. This is the row an unconditional-append implementation fails.
- **AC-3** advertise `[question]` → reaches open; ack carries `question`; `activeConnFor(...).Interactive` is **`false`**. First state in which the ack carries a `capabilities` key on a non-interactive conn.

The table's existing assertion (b) — the byte check that a no-grant ack drops the `capabilities` key entirely — is guarded by `len(tc.wantAck) == 0`, so it is skipped for all three new rows and keeps its meaning for the existing no-grant ones. No helper changes.

Note on AC-3's value: it is the only row that distinguishes the shipped `slices.Contains(negotiated, CapabilityInteractive)` from a `len(negotiated) > 0` reduction. Against that mutant, every other row in both tables stays green.

**Gate (§ B2):** `go test -race ./internal/relay/... ./internal/protocol/...`, `go vet ./...`, `go build ./cmd/pyry`. The full-module race suite is the verifier's gate, not mine.

## Open questions

1. **Does any drift detector or exhaustiveness guard enumerate capability constants?** — *Resolved before commit.* No. The detectors in `compat_test.go` and `cmd/pyry/relay_guard_test.go` partition `Type*` constants and check `Code*` strings; a capability string is a vocabulary entry in an existing `[]string` field, not a frame type. Grep confirms `Capability` appears in no guard or exhaustiveness test. No registry edit.
2. **Does any existing test assert the whole `supportedV2Capabilities` slice, or an exact ack for a client advertising a superset?** — *Resolved before commit.* No. The symbol appears outside its declaration only in a test comment. No client in the repo advertises `question` today, so no existing expectation moves.
3. **Should `internal/protocol/handshake_test.go` gain a round-trip test for the new constant?** — Deferred to Phase B. The existing `Capabilities` round-trip tests pin the *field*, and a constant carrying no new wire shape adds nothing to that. Current intent: no protocol-package test, keeping the diff to the two tables the ticket names. If Phase B finds a per-constant assertion precedent in that file, this is revised with a `## Revisions` entry.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries] No findings, with one named fragility.** The single untrusted→trusted boundary is `handleNoiseInit`'s decode of `helloPayload.Capabilities`, funnelled through `negotiateCapabilities`. The security primitive is the loop shape: it iterates `supportedV2Capabilities` and filters by the advertised set, so the output is a subset of supported **by construction** — a spoofed `"question"`-adjacent string can never be granted, and this diff does not touch that shape. The one thing this ticket genuinely changes is that the negotiated slice can now be non-empty for a client that advertised no `interactive`. Every downstream authority decision must therefore be value-specific, not emptiness-based. It is: `s.interactive` is `slices.Contains(negotiated, protocol.CapabilityInteractive)`, and the five reconcile gates (`reconcileQuestions` and siblings) read `s.interactive`, never `negotiated`. **The fragility is that a future reader could "simplify" that `Contains` to `len(negotiated) > 0`, silently granting interactive access to a question-only client.** Mitigation is deterministic, not advisory: AC-3's table row asserts `ActiveConns(...).Interactive == false` on a question-only handshake and reddens against exactly that mutant. Code property plus a test that fails — different fabric.
- **[Tokens, secrets, credentials] Finding — pre-authorization capability disclosure, accepted (unchanged in kind from #626).** The `hello_ack` is sealed via `WriteResp` and sent **before** `Devices.Validate`, by the pinned "state → `HandshakeComplete` before token validation" invariant. So a peer that completes Noise IK but holds no valid device token still learns the daemon supports `question`. Assessment: the string is public protocol vocabulary of the same class as `interactive`, which is already disclosed on this exact path and which #626's review accepted on the grounds that the echo grants nothing (the session never opens, is never enumerated, is never pushed to) and leaks nothing. The incremental disclosure here is one bit of build-recency to an unauthorized peer — which is the ticket's stated purpose, just reachable slightly earlier than authorization. It keys no vulnerability: nothing in the daemon behaves differently toward a peer that knows the answer, and the honest client obtains the same bit post-authorization anyway. Closing it would mean building the ack after the token check, breaking a pinned invariant for zero gain — the same trade #626 made and recorded. No token, credential, or key material is added to, or read by, this diff.
- **[File operations] Not applicable by design.** The diff is one constant, one slice element, test table rows, and a docs table row. No path is constructed, no file opened, created, stat'd, or written; no mode, symlink, or atomic-rename question arises.
- **[Subprocess / external command execution] Not applicable by design.** No `exec.Command`, no shell, no environment variable is read or set anywhere in the diff.
- **[Cryptographic primitives] No findings.** No primitive, key, or nonce is added, reused, or reconfigured. The ack continues to be sealed by the existing `WriteResp` early-data slot; the only change is ~13 bytes more plaintext, which the Noise framing handles identically. `crypto/subtle` is deliberately **not** used in `negotiateCapabilities`: `slices.Contains` compares public vocabulary strings, not secrets, and a constant-time comparison there would signal a secret where none exists.
- **[Network & I/O] No findings, quantified rather than asserted.** The advertised slice is untrusted and unbounded in element count at the type level, so intersection cost is O(|supported| × |advertised|) — and this ticket **doubles** the per-handshake scan by taking the supported set from 1 to 2. The bound comes from `decodeInnerFrameV2`, which rejects inner-frame `data` over `maxNoisePayloadBytes` (65535) after base64 decode, so a hostile advertised set holds at most ~21k entries; 2 × 21k string comparisons is microseconds, once per handshake, and every existing connection/timeout/idle bound is untouched. Worth recording for the future: this is O(k·n) in the supported-set size k, so a supported set that ever grows large wants a set lookup rather than a scan. At k=2 that would be premature.
- **[Error messages, logs, telemetry] No findings — and one thing deliberately not built.** `negotiated` is not logged today; the accept path logs `conn_id` and `device_name`, the reject paths log static reasons. This diff adds no log call and no telemetry. The tempting addition — logging the client's advertised set to help debug a stale-daemon report — is a **non-goal**, because it would write unbounded attacker-controlled strings into the operator log channel. The `hello_ack` the client already receives is the supported diagnostic.
- **[Concurrency] No findings.** No goroutine, lock, channel, or timer is added, so no lifecycle, ordering, or leak question arises. `supportedV2Capabilities` is a `var` because a slice cannot be `const`, hence technically mutable; it is written only at package init and read from the Run goroutine, and this diff preserves that (the element is added to the literal, not assigned at runtime). `s.interactive` keeps its set-once discipline, still written before the session becomes enumerable.
- **[Threat model alignment] One observation, disposition already declared by the spec.** Threat 3 (relay operator MITM) notes the relay can **observe metadata** including frame sizes. A daemon that echoes `question` emits a `noise_resp` roughly a dozen ciphertext bytes longer than one that does not, so a passive relay can distinguish daemon builds by frame length without decrypting anything. § Security model already states that v2 provides no metadata privacy beyond TLS and that padding and timing obfuscation are out of scope for v2, so this falls inside a declared, accepted residual — and it is a strictly weaker disclosure than the ticket's own goal of telling clients the answer outright. Threat 7 (denial of service, `mitigation: deferred`) is addressed above under Network & I/O: the doubling is bounded and negligible. Threats 1, 2, 4, 5, 6 and 8 are untouched — this diff adds no prompt path, no routing key, no token handling, no nonce or replay surface, and no key material.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-02

## Revisions

### 2026-09-02 — Phase B

Three departures from the plan above. None changes the design; all three are recorded because the verifier diffs the implementation against this document.

1. **AC-2 gets no new test row.** The Testing strategy prescribed three new rows in `TestV2Session_Handshake_CapabilityNegotiation`, one per criterion. Only two were added. AC-2 ("advertises only `interactive` → ack carries `interactive` and not `question`") is already pinned by the table's pre-existing `advertise interactive` row: its assertion is `slices.Equal(ack.Capabilities, tc.wantAck)` against a one-element want, which is exactly the exact-equality check an unconditional-append implementation fails. A third row would have re-run an identical input for no additional discriminating power. The test's doc comment now states that this pre-existing row carries AC-2, so the coverage is findable rather than implied.

2. **`question_resolved` does not exist — corrected to `question_shown` / `question_dismissed`.** The Design section's non-goal paragraph named `question_resolved` as an outbound wire type, taken from the ticket body. Grep found the string in no Go file, fixture or doc: the outbound pair is `TypeQuestionShown` = `question_shown` and `TypeQuestionDismissed` = `question_dismissed`, with `TypeQuestionAnswer` / `TypeQuestionRefused` inbound. The non-goal itself is unaffected — `reconcileQuestions` gates on `s.interactive` and stays that way — but the invented name was not propagated into the constant's doc comment or the spec table, both of which use the real type names.

3. **Open question 3 resolved the other way: a protocol-package test was added.** The plan's intent was to add none. Phase B found the precedent it named as the trigger for revising — `TestErrorCode_Constants_MatchSpec` in `compat_test.go` pins every `Code*` constant to its exact spec string — and, more decisively, a real vacuity: every test in this repo passes `CapabilityQuestion` symbolically on both the advertise and the expect side, so a fat-fingered value would be self-consistent and green in all of them. That matters more here than for an ordinary constant, because the capability set is a cross-repo contract compared against a literal outside this module, and detection is the ticket's entire purpose. `TestCapability_Constants_MatchSpec` in `handshake_test.go` closes it, mirroring the `Code*` shape.

**Also verified, not a departure.** The Testing strategy claimed AC-3's row is the only assertion in either capability table that separates the shipped `slices.Contains(negotiated, protocol.CapabilityInteractive)` reduction from a `len(negotiated) > 0` one. Confirmed by mutation rather than asserted: with that reduction replaced under a `go test -overlay`, exactly one subtest reddens — `question_alone_grants_no_interactive`, on `ActiveConns Interactive = true, want false` — and every other row in both tables stays green.
