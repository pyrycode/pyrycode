# #2172 — advertise `model_list` in the v2 handshake capability set

## Files read

- `internal/protocol/handshake.go` → `CapabilityInteractive`, `CapabilityQuestion` — the two existing vocabulary
  constants and the doc-comment shape the new one mirrors (advertisement-only framing, "detection only, grants no
  access", "the trust decision lives in the consumer").
- `internal/protocol/handshake.go` → `HelloClientPayload.Capabilities`, `HelloAckPayload.Capabilities` — the wire
  fields the string travels in; `omitempty` on both is what makes a no-grant negotiation drop the key.
- `internal/protocol/handshake_test.go` → `TestCapability_Constants_MatchSpec` — the drift detector. Its count
  assertion is `len(got) != len(want)` over two maps declared inside the test, so there is no literal to bump; it
  fires only when a row lands in one map and not the other. **A constant missing from both maps sails through** —
  the shape has no literal to keep honest, which is exactly why the row must go into both maps by hand.
- `internal/relay/v2session_handshake.go` → `supportedV2Capabilities` — the daemon's authoritative set, the one
  production edit outside `protocol`. Its doc comment states the property this ticket depends on: membership grants
  nothing, because every gate reads `s.interactive`.
- `internal/relay/v2session_handshake.go` → `negotiateCapabilities` — iterates the **supported** set and filters by
  the advertised one, so output order is supported-set order and a spoofed advertisement is never a candidate.
- `internal/relay/v2session_handshake.go` → `handleNoiseInit` — derives `s.interactive` with
  `slices.Contains(negotiated, protocol.CapabilityInteractive)`, a value-specific reduction rather than
  `len(negotiated) > 0`. This is the whole reason a third supported member cannot widen access, and what AC#2 pins.
- `internal/relay/v2session.go` → `V2Session.interactive` field doc, `handleActiveConns` — the negotiated flag's
  single write site and the `ActiveConns` read AC#2 asserts against.
- `internal/relay/v2session_test.go` → `TestNegotiateCapabilities`, `TestV2Session_Handshake_CapabilityNegotiation`
  — the two tables gaining rows. Both compare with `slices.Equal`, which is order-sensitive.
- `internal/relay/v2session_modelrequest_test.go` → `TestV2Session_RequestModelList_AnswersWithTheConversationsMenu`,
  `TestV2Session_RequestModelList_NonInteractiveIsInert` — AC#3's pins. Both drive the verb over a conn advertising
  `[]string{protocol.CapabilityInteractive}` only; staying green over an unchanged file **is** AC#3.
- `internal/protocol/codes.go` → `TypeModelList`, `TypeRequestModelList` — the frame and verb this string detects,
  `"model_list"` and `"request_model_list"`; the source of the settled snake_case wire value.
- `docs/protocol-mobile.md` § Capability negotiation (v2) — the "Defined capability strings" table and the
  build-detection paragraph (#2020) the new row extends.
- `docs/knowledge/decisions/037-capability-strings-not-version-numbers.md` § Consequences — the "one string per
  user-facing wire feature, added as part of shipping it" rule this ticket pays late, and the O(k·n) note that comes
  due at k=3.
- `docs/knowledge/features/protocol-package-handshake-control-payloads.md` — narrates the constant set as
  two-valued and explains why `TestCapability_Constants_MatchSpec` exists (symbolic passing on both sides of every
  relay test makes a fat-fingered value self-consistent and green everywhere). Goes stale with this change; folding
  that in is the documentation phase's, not this ticket's.
- `internal/e2e/handshake_interactive_helpers_test.go`, `internal/e2e/realclaude/harness_daemon_test.go` — both
  advertise `interactive` alone and assert with `slices.Contains`, so a third supported entry is unreachable from
  either. No edit needed; checked rather than assumed.

## Context

#2124 (the daemon-wide model-list fallback) and #2125 (the `request_model_list` verb) shipped the on-demand model
list with no capability string. ADR 037, accepted three days earlier under #2020, makes adding the string part of
shipping a cross-repo wire feature rather than a follow-up. This ticket is that omission.

The cost is already banked. pyrycode-desktop#1169 is halted under `error:rework-loop` at `rework-count:3`: the Mac's
installed daemon predates both #2124 and #2125 (verified symbol-by-symbol against the binary in the ticket body), so
no `model_list` frame can reach a childless FAB-created chat, and from the client a stale daemon and a broken feature
present identically as "this chat has no published levels". Two of that spec's four reds were this. The consumer half
is already built: pyrycode-desktop#933's `daemonCapabilityGate.ts` turns a missing string into a **skip** naming the
stale daemon, which parks the run in Inbox for the operator instead of failing it back to an agent who cannot rebuild
a Go binary.

One string covers both #2124 and #2125: they are not separately detectable and not separately useful — the fallback
with no verb gives a client no way to ask, the verb with no fallback has nothing to answer for a childless
conversation.

**No ADR is warranted.** ADR 037 already decided the policy this ticket applies; recording a second ADR for an
instance of an accepted decision would be noise. The k-growth note below is a data point for 037, not a new decision.

## Design

Two production edits, no new types, no new control flow.

**1. `internal/protocol/handshake.go`** — add `CapabilityModelList = "model_list"` immediately after
`CapabilityQuestion`, with a doc comment in its neighbours' shape: what the client is claiming it understands, the
daemon-echoes-the-intersection detection mechanism, the explicit "detection only — grants no access" paragraph, and
the pointer that the trust decision lives in `internal/relay` rather than here. It names #2124 and #2125 as the pair
it detects, since a reader of the constant alone cannot otherwise know what "supports the model list" means.

The wire value is settled by refinement as snake_case `model_list`, matching `TypeModelList` and
`TypeRequestModelList`. Not reopened here.

**2. `internal/relay/v2session_handshake.go`** — append `protocol.CapabilityModelList` (the constant, never a bare
literal) to `supportedV2Capabilities`, **after** `CapabilityQuestion`.

The append position is load-bearing rather than cosmetic. `negotiateCapabilities` emits in supported-set order, and
both relay tables assert with `slices.Equal`, which is order-sensitive; appending at the end makes the all-three want
`[interactive, question, model_list]` and leaves every existing row's want untouched. Inserting anywhere else would
silently rewrite what every future row has to expect.

Nothing else in this repo reads the new constant. `supportedV2Capabilities` has exactly one production reader
(`negotiateCapabilities`) plus two doc-comment mentions; the sweep is in the reading list above.

### Why this cannot widen access

The property that makes a third supported member safe is that `handleNoiseInit` derives the session's authority with
`slices.Contains(negotiated, protocol.CapabilityInteractive)` — a value-specific reduction, not `len(negotiated) > 0`.
Every gate in `internal/relay` then reads `s.interactive`. So a client advertising `model_list` alone negotiates to a
non-empty capability set and a **false** interactive flag, and receives none of the interactive stream. That is not a
new property; #2020 made it load-bearing with `CapabilityQuestion`, the first member grantable to a non-interactive
client. This ticket adds the second such member and re-pins the property with its own row rather than relying on
`question`'s.

### Deliberate non-goals

- **No gate keyed on the new string.** `handleRequestModelList` and `modelListFor` stay reachable exactly as today.
  The verb is already gated on `interactive` at its dispatch arm, pinned by
  `TestV2Session_RequestModelList_NonInteractiveIsInert` (which also asserts the gate precedes both the decode and the
  membership check). That gate is unchanged and no second one is added: ADR 037 makes gating a per-feature decision
  rather than a default, and gating here would cut off pyrycode-mobile, which advertises `interactive` only.
- **No registry, guard or fixture edit.** A capability string is a vocabulary entry in an existing `[]string` field,
  not a frame type, so `cmd/pyry/relay_guard_test.go` and the envelope type-set registries are untouched — same as
  #2020. The `hello`/`hello_ack` JSON fixtures are client-side payloads and unaffected.
- **No `docs/knowledge/features/` edit.** Two overviews there narrate the supported set as two-valued and go stale
  with this change; folding that in belongs to the documentation phase, which runs after verification.
- **Not closing `TestCapability_Constants_MatchSpec`'s known gap.** The count check cannot detect a `Capability*`
  constant missing from *both* maps. That gap is pre-existing, closing it needs reflection or AST walking, and the
  ticket puts it out of scope.

### ADR 037's k-growth note, answered

ADR 037 § Consequences calls the third capability "a reasonable point to reconsider a set lookup" for
`negotiateCapabilities`, which is O(k·n). At k=3 the linear scan is still right: a `map[string]struct{}` would cost a
package-level allocation and an init-order dependency to save three string comparisons on a once-per-connection path,
and it would lose the ordering guarantee that both `slices.Equal` tables depend on (the emit order **is** the
supported-set order). Recording that the question was asked and answered; no change.

## Concurrency model

No new goroutines, no new shared state, no locking change.

`supportedV2Capabilities` is read-only after package init and read on the manager's `Run` goroutine; adding an
element to its literal does not change that. Its doc comment already states the invariant ("nothing may assign to it
or to its backing array at runtime") and the append keeps it true. `negotiateCapabilities` remains pure — it
allocates a fresh `out` slice per call and never aliases the package var's backing array, so a caller cannot mutate
the supported set through its result.

`s.interactive` keeps its single write site in `handleNoiseInit`'s token-OK path, set before `s.state` advances to
`V2StateOpen`, preserved across re-key by not being touched. Unchanged.

## Error handling

No new failure modes. There is no parse, no I/O and no allocation that can fail on this path.

The one behaviour worth naming is the fail-closed default, which is unchanged: an unknown or spoofed advertisement is
never a candidate for the output because the loop iterates the supported set, and an advertise-nothing set yields
`nil`, which `omitempty` drops from the ack and which leaves `s.interactive` at its zero value `false`.

A daemon built before this lands drops the string in the intersection. That absence is the feature — the positive,
checkable stale-daemon signal — not an error path.

## Testing strategy

Five criteria; two ride existing tests unchanged and three get new table rows. No new test function.

**AC#1 — supported set carries it, ack is still advertised ∩ supported in both directions.**
- Forward half: a new `TestV2Session_Handshake_CapabilityNegotiation` row advertising all three, wanting
  `[interactive, question, model_list]` with the interactive flag true. Plus a `TestNegotiateCapabilities` row at the
  pure-function level for the same set, and a reverse-order sibling pinning that the output is ordered by the
  supported set rather than by the client's advertisement.
- Reverse half: the **pre-existing** `"advertise interactive"` row. Its `slices.Equal` against a one-element want
  already dies against an unconditional-append implementation. Verified by running the test unchanged, exactly how
  #2020 discharged its own equivalent criterion; the test header says so.

**AC#2 — `model_list` alone grants no interactive access.** A new row in each relay table advertising the string
alone: the negotiation returns `[model_list]`, the ack echoes `[model_list]`, and `ActiveConns` reports
`Interactive: false`. This is the row that would redden if anyone ever reduced the flag derivation to a non-emptiness
test, and it is the direct analogue of #2020's "question alone grants no interactive" row.

**AC#3 — the verb is unchanged for a connection that never advertised the string.** Discharged by running
`TestV2Session_RequestModelList_AnswersWithTheConversationsMenu` unchanged. Its two rows already drive the verb over
a conn advertising `[]string{protocol.CapabilityInteractive}` and cover both the bound arm and #2124's daemon-wide
fallback arm. An implementation that added a second gate keyed on the new string would redden it. **The file is not
edited** — the evidence is that it stays green over an untouched file, so any diff to it would weaken the claim.

**AC#4 — constant pinned to the literal.** One row on each side of `TestCapability_Constants_MatchSpec`'s map pair:
`"CapabilityModelList": CapabilityModelList` in `got`, `"CapabilityModelList": "model_list"` in `want`. The
`len(got) != len(want)` check is left exactly as it is — hardcoding a `3` would replace a self-maintaining assertion
with one that has to be edited forever.

**AC#5 — the doc row.** A `model_list` row in `docs/protocol-mobile.md` § Capability negotiation's "Defined
capability strings" table, giving the meaning (the client understands `model_list` frames and can ask for one with
`request_model_list`) and carrying the detection-only note in the same shape as the `question` row.

**Gate:** `go test -race ./internal/protocol/... ./internal/relay/...`, `go vet ./...`, `go build ./cmd/pyry`. Not
`needs-real-claude`: every criterion is hermetic table tests plus one doc row. The motivating live gate is
pyrycode-desktop#1169's, in another repo.

### Mutation check

The three new rows are only evidence if they can fail. Before committing, the append to `supportedV2Capabilities` is
reverted in a throwaway overlay run to confirm each new row reddens for its own reason, and the append is moved ahead
of `CapabilityQuestion` to confirm the `slices.Equal` order sensitivity is real rather than assumed. Neither mutation
is committed.

## Open questions

1. **Does anything outside the two tables assert on the supported set's length or exact contents?** — Resolved
   during Phase A: `supportedV2Capabilities` has one production reader and two doc-comment mentions, and both e2e
   harnesses advertise `interactive` alone and assert with `slices.Contains`, so a third entry cannot reach them.
   Nothing further to update.
2. **Is `feature/449`'s overlap on `internal/relay/v2session_test.go` a real blocker?** — Resolved: no. See the
   overlap note below.

## File-overlap check (§ A2)

Ran against every `origin/feature/<n>` branch, not just those with an open PR. One hit:
`origin/feature/449` touches `internal/relay/v2session_test.go`.

Not a blocker, and deliberately not raised as one. That branch's tip is from 2026-05-17, issue #449 closed the same
day, it has never had a PR, and it sits 3129 commits behind main with 4 commits of its own. It is an abandoned
original whose work reached main by another route — not in-flight work that could merge and conflict. Filing
`addBlockedBy(#2172, #449)` against a closed issue would resolve immediately and cost a pointless round-trip through
Backlog. No other branch touches any file in this ticket's set.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No findings, and this is the category the whole ticket lives in. The boundary is a single
  named function, `negotiateCapabilities`, which crosses untrusted (`hello.payload.capabilities`, attacker-chosen)
  to trusted (`hello_ack.payload.capabilities` + `s.interactive`). It is a whitelist by construction, not a filter:
  it iterates `supportedV2Capabilities` and asks whether each entry appears in the advertised set, so the untrusted
  slice is only ever a membership oracle and never a source of output values. The adversarial question is therefore
  not "can a client inject an arbitrary string" (structurally impossible — the output elements are the package var's
  own strings) but **"does adding an element to the whitelist grant anything?"** It does not: `handleNoiseInit`
  reduces to authority with `slices.Contains(negotiated, protocol.CapabilityInteractive)`, a value-specific test,
  and every gate in the package reads the resulting `s.interactive`. AC#2's new row is the assertion that keeps that
  true, and it is the second row in the repo (after #2020's) that would redden under a `len(negotiated) > 0`
  refactor. Downstream code never holds the negotiated slice as authority — it holds a `bool` — so there is no
  "trusted vs untrusted" ambiguity to signal.
- **[Tokens, secrets, credentials]** No findings — the design touches no credential. The handshake's token lives in
  `HelloClientPayload.Token`, is validated on a separate branch of `handleNoiseInit`, and this change neither reads
  nor logs it. Worth stating adversarially: the new string is negotiated in the same `handleNoiseInit` that performs
  the token check, so a "does capability negotiation happen before authentication?" question is fair. It does not
  matter here even if it did — the negotiated set is echoed only inside the AEAD-sealed channel to a peer that
  completed IK, and the set contains no secret. The ordering itself is unchanged by this ticket.
- **[File operations]** Not applicable, and by a design decision rather than by luck: a capability is a compile-time
  string constant in a package-level literal. Nothing in this change reads, writes, or derives a path, so there is no
  traversal, TOCTOU, mode, symlink or atomic-write surface to audit.
- **[Subprocess / external command execution]** Not applicable — no `exec.Command` on this path and no value from
  this change reaches one. Specifically checked because a model *name* elsewhere in the model-list feature can reach
  a child process's arguments: this ticket adds the detection string only and touches neither `modelListFor` nor
  `handleRequestModelList`, so it introduces no new value that could travel there.
- **[Cryptographic primitives]** No findings — no RNG, key, nonce or comparison-against-a-secret is introduced. The
  string comparisons in `negotiateCapabilities` are `slices.Contains` on public wire vocabulary, so constant-time
  comparison is not merely unnecessary but would be misleading: `model_list` is a published constant in
  `docs/protocol-mobile.md`, not a secret, and nothing about the timing of its comparison discloses anything an
  attacker cannot read in the spec.
- **[Network & I/O]** No findings, one bound checked rather than assumed. The advertised set is attacker-controlled
  in both length and content, and `negotiateCapabilities` is O(k·n) over it — so an adversary could pass a very
  large `capabilities` array. The work is bounded upstream and not by this change: the whole inner frame is size-
  capped at `maxNoisePayloadBytes` before decode (the cap enforced in the inner-frame decoder alongside
  `InnerFrameV2Decoded`), so n is bounded by the frame cap, and this ticket moves k from 2 to 3 — a 1.5× constant on
  an already-bounded, once-per-connection computation. No new timeout, deadline or connection-count surface.
- **[Error messages, logs, telemetry]** No findings — the change adds no log call and no error string. The
  negotiated set is already carried in existing handshake logging as public vocabulary; the new string is a published
  constant, so its appearance in a log discloses nothing. Explicitly checked that the change does not cause the
  *advertised* (attacker-controlled) set to be logged anywhere it was not before: it does not, because nothing about
  the logging call sites changes.
- **[Concurrency]** No findings, and the invariant is named rather than assumed. `supportedV2Capabilities` is written
  once at package init and read thereafter on the manager's single `Run` goroutine, so the append is a change to an
  init-time literal, not a runtime mutation — its doc comment already forbids assigning to the var or its backing
  array and the append keeps that true. `negotiateCapabilities` allocates a fresh output slice and never returns an
  alias into the package var's backing array, so a caller cannot mutate the supported set through its result — the
  one way a "read-only after init" claim could quietly become false. No lock is taken, so there is no ordering to
  document; no goroutine is spawned, so there is nothing to leak.
- **[Threat model alignment]** No findings. The relevant threat in `docs/protocol-mobile.md` § Capability
  negotiation is a phone claiming capabilities it was not granted, and the spec's stated mitigation is that the ack
  MUST be the intersection and never a blind mirror. This design preserves that exactly — the ticket names an
  unconditional append to the ack as the wrong implementation, and AC#1's reverse half fails it. The adjacent threat,
  a client escalating privilege by advertising a *new* string, is addressed by the value-specific `s.interactive`
  derivation under [Trust boundaries]. Out of scope and named as such: whether the model-list verb should have a
  capability gate of its own is ADR 037's per-feature decision, answered "no" for this feature by the ticket, because
  gating would cut off pyrycode-mobile, which advertises `interactive` only.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-06
