# Spec #717 — gated remote `modal_answer` resolution

> **Status:** ready for development. Sized **S**. `security-sensitive` (security review at the end of this doc; verdict **PASS**).
>
> This is the security-critical leg of the daemon-side modal bridge (EPIC #597 Phase 3,
> [ADR 025](../../knowledge/decisions/025-mobile-remote-head-interactive-session.md) § Security model):
> it routes an internet-sourced `modal_answer` into claude's permission prompt **only** when the
> answering device is gated. The two prerequisite seams — the safe-answer keystroke verbs (#726) and
> the interception + `ModalResolver` seam + `modal_dismissed` broadcast (#727) — are **already merged**.
> This ticket fills the answer arm of #727's `ModalResolver`: it replaces the deferred-no-op
> `ResolveAnswer` body and extends one interface. **Nothing else.**

## Files to read first

| Path | What to extract |
|---|---|
| `cmd/pyry/modal_resolve_v2.go:11-103` | **The only production file you edit.** `modalKeystroker` interface (line 16 — extend with `Answer`/`AcceptTrust`); `modalResolverV2` struct + `newModalResolverV2`; `ResolveCancel:54-91` — **mirror its shape exactly** (consume → best-effort keystroke → audit → return dismissal); `ResolveAnswer:98-103` — **replace this body**. |
| `cmd/pyry/modal_resolve_v2_test.go` | The test harness you extend: `fakeKeystroker:24-32` (extend to record `Answer`/`AcceptTrust`), `recordPermissionModal:36-47`, `auditLogger`/`auditRecords:51-74`, `testDevice:76-79`, and `TestModalResolverV2_Answer_NoOp:235-263` (**replace** — answer is no longer a no-op). The cancel tests are the template for every assertion you write. |
| `internal/modalbridge/modal.go:75-186` | `Outstanding{Class, Options, DefaultOptionID}`; `Lookup` (read, no consume) and `Resolve` (one-shot consume = the idempotency gate); the option-id consts `optProceed`/`optExit:42-44` and class consts `classPermission`/`classTrust:33-36`. |
| `internal/devices/auth.go:48-91` | `MayAnswerRemotePermission()` (nil-safe, fail-closed eligibility gate); `RemotePermissionOutcome` + `OutcomeAllow`/`OutcomeDeny:69-75`; `AuthorizeRemotePermission(d, outcome):89-91` (the grant decision — `MayAnswer && outcome==Allow`). |
| `internal/audit/audit.go:27-81` | `Entry`; `OutcomeAllowed`/`OutcomeDenied`/`OutcomeDeniedUnauthorized:43-49`; `SourceRemote:57-61`; `Log` (emits a fixed non-secret attribute set — never a token/body). |
| `internal/supervisor/modal.go:52-66` | `AcceptTrust()` / `Answer(choice)` / `SendEsc()` — the verbs the keystroker routes to; each returns wrapped `ErrNoLiveSession` and **writes nothing** when no child is live. |
| `internal/protocol/messaging.go:88-162` | `ModalOption{ID,Label}`; `ModalAnswerPayload{ModalID,OptionID,AnswerToken}`; **`ModalDismissedPayload.Outcome` doc (line 153): "the selected `ModalOption.ID` when answered"** — the wire Outcome is the *option_id*, not "allowed". |
| `internal/relay/v2session.go:366-393` | `ModalDismissal{Outcome,Source}` (line 370: "#717 uses the answered option_id") + the `ModalResolver` interface contract your `ResolveAnswer` fulfils. |
| `internal/relay/v2session.go:1495-1518` | `handleModalAnswer` — already broadcasts `modal_dismissed` **iff** `ResolveAnswer` returns `ok=true`. **No manager change is needed**; you change only the resolver impl. |
| `internal/turnevent/taxonomy.go:50-53` | `PermissionOptionKind` consts — these strings (`allow_once`/`allow_always`/`reject_once`/`reject_always`) are exactly the permission `option_id`s on the wire. |
| `docs/knowledge/codebase/727.md` § "Out of scope (siblings)" (lines 124-130) | The hand-off note: replace `ResolveAnswer` body, extend `modalKeystroker` with `Answer`/`AcceptTrust`, the relay manager needs no change, `Lookup`/`Resolve` are the registry seams. |

## Context

A phone surfaces a permission/trust modal (`modal_shown`, #716) and the human taps an option. The phone
sends an inbound `modal_answer{modal_id, option_id, answer_token}` v2 control frame. #727 already
intercepts it at `dispatchAppFrame`, decodes the payload, and calls `ResolveAnswer(...)` on the
composition-root resolver — but that method is a deliberate **deferred no-op** (returns `(zero, false)`,
routes nothing) so the escalating ALLOW path stayed dead until the per-device gate existed. This ticket
is that gate.

The cancel arm (`ResolveCancel`, #727) is the complete template: cancel is the fail-safe terminal
decision (dismiss = deny) and is intentionally **ungated**. Answer is the dangerous twin — it can
*grant* a permission — so it adds three things cancel doesn't: a fail-closed per-device gate, an
`option_id → keystroke` mapping, and a three-way audit matrix (`allowed`/`denied`/`denied_unauthorized`).

**Production rollout note (not a gap):** #708 (live producer wiring) is still open, so nothing
`Record`s into the daemon-singleton `modalReg` in production yet — every production `modal_answer`
takes the unknown-`modal_id` no-op path until #708 lands. This ticket is unit-tested against a real
`modalbridge.Registry` with `Record`ed modals, so the gated path is fully exercised in tests. This is
the intended seam-first phased rollout; do not wire #708 here.

## Design

### Surface touched

- **`cmd/pyry/modal_resolve_v2.go`** — extend `modalKeystroker`; replace `ResolveAnswer`; add one
  unexported pure mapping helper. **No new file, no new exported type, no new sentinel.**
- **No change** to `internal/relay` (the manager already broadcasts on `ok=true`),
  `internal/supervisor` (`Answer`/`AcceptTrust`/`SendEsc` already exist, #726),
  `internal/devices`/`internal/audit`/`internal/modalbridge` (all consumed as-is), or `cmd/pyry/relay.go`
  (`sup` already satisfies the extended interface — see below).

### Interface extension (no consumer cascade)

`modalKeystroker` currently declares only `SendEsc() error`. Extend it to the full safe-answer surface:

```go
type modalKeystroker interface {
	SendEsc() error
	Answer(choice string) error
	AcceptTrust() error
}
```

`*supervisor.Supervisor` already implements all three (`internal/supervisor/modal.go:52-66`), so the
existing production wiring `newModalResolverV2(modalReg, sup, logger)` (`cmd/pyry/relay.go:329`) keeps
compiling unchanged. The only other implementer is the test `fakeKeystroker`. **Zero production call-site
cascade.**

### `option_id → (outcome, keystroke)` mapping

This is the net-new translation. The phone speaks abstract `option_id`s; the safe-answer seam wants
claude's literal keystroke. The **single source of truth** linking the two is the order of
`Outstanding.Options`, which the producer (#716) builds in claude's display/selection order. So a
permission option's keystroke is its **1-based position** in `Outstanding.Options`; trust uses the two
dedicated verbs. Define one pure helper:

```go
// classifyAnswer locates optionID within o.Options and maps it to the grant
// outcome + the keystroke verb to actuate. ok=false if optionID is not a
// locatable option of THIS modal (forged / wrong-class / unknown id) — caller
// rejects with no keystroke, no consume, no audit.
func classifyAnswer(o modalbridge.Outstanding, optionID string) (outcome devices.RemotePermissionOutcome, verb answerVerb, choice string, ok bool)
```

| `option_id` | modal class | `outcome` | keystroke verb | `choice` |
|---|---|---|---|---|
| `allow_once` | permission | `OutcomeAllow` | `Answer` | `strconv.Itoa(idx+1)` |
| `allow_always` | permission | `OutcomeAllow` | `Answer` | `strconv.Itoa(idx+1)` |
| `reject_once` | permission | `OutcomeDeny` | `Answer` | `strconv.Itoa(idx+1)` |
| `reject_always` | permission | `OutcomeDeny` | `Answer` | `strconv.Itoa(idx+1)` |
| `proceed` | trust | `OutcomeAllow` | `AcceptTrust` | — |
| `exit` | trust | `OutcomeDeny` | `SendEsc` | — |
| anything else, or id not in `o.Options` | — | — | — | `ok=false` |

`idx` is the option's index in `o.Options` (`slices.IndexFunc` on `ModalOption.ID`). **Membership is
validated for every id** (including `proceed`/`exit`): an `allow_once` sent for a trust modal, or a
`proceed` sent for a permission modal, fails the `o.Options` lookup and is rejected. `answerVerb` is a
small unexported enum (`verbAnswer`/`verbAcceptTrust`/`verbEsc`) mirroring supervisor's `modalKey`;
the resolver switches on it to call the matching `r.kb` method (the same switch shape as
`supervisor.sendModalKeystroke:99-113`).

> **Why positional, not a hardcoded `allow_once→"1"` table:** hardcoding would duplicate the
> producer's option-ordering invariant (`modalbridge.go:119-130`) in a second place that silently
> breaks if the producer reorders. Deriving the keystroke from the surfaced `o.Options` keeps one
> source of truth *and* gives free membership validation. The consumer trusts the producer's contract
> that `o.Options[i]` is selectable by pressing key `i+1` (see Open Questions).

### `ResolveAnswer` decision matrix

`ResolveAnswer(modalID, optionID, answerToken string, dev *devices.Device) (relay.ModalDismissal, bool)`.
All steps run on the manager's single `Run` dispatch goroutine (serialized — see Concurrency).
**The hard invariant: nothing but a fully-authorized, valid answer may consume (`Resolve`) the modal or
route a keystroke.** Order matters — gate before consume.

| # | Condition (first match wins) | `Resolve` (consume)? | keystroke | audit | return |
|---|---|---|---|---|---|
| 1 | `Lookup(modalID)` misses (stale / unknown / **already-resolved replay**) | no | none | none | `(zero, false)` |
| 2 | `!dev.MayAnswerRemotePermission()` (nil / unauth / opt-in off) | **no** | none | `denied_unauthorized` | `(zero, false)` |
| 3 | `classifyAnswer` `ok=false` (forged / wrong-class option_id) | no | none | none (`Warn` log) | `(zero, false)` |
| 4 | eligible + `AuthorizeRemotePermission(dev, outcome)==true` (allow) | yes | allow keystroke | `allowed` | `({optionID, remote}, true)` |
| 5 | eligible + outcome is deny | yes | deny keystroke | `denied` | `({optionID, remote}, true)` |

Steps in order: `Lookup` → eligibility gate → `classifyAnswer` → `Resolve` (consume) → route keystroke
(best-effort) → `audit.Log` → return. Notes:

- **Step 1 is the idempotency mechanism.** `Resolve` (rows 4/5) removes the modal, so any later
  `modal_answer` for the same `modal_id` — a network replay, a reorder, or a second human tap — lands
  in row 1: no second keystroke, no second dismissal, no second audit. This *is* AC2's idempotency and
  first-answer-wins (ADR 025 §4). See "answer_token" below.
- **Step 2 never mutates the modal** (`Lookup` only, no `Resolve`), so an ungated/forged answer leaves
  the modal outstanding for the legitimate local answer or the #725 deny-on-timeout. `o.Class` (from
  the step-1 `Lookup`) supplies the audit `modal_class`. `dev` may be nil → empty `device_hash`/`label`
  (the `ResolveCancel:71-75` pattern).
- **Steps 4/5 use both #702 primitives:** the eligibility gate (`MayAnswerRemotePermission`, step 2)
  distinguishes `denied_unauthorized` from a real decision; `AuthorizeRemotePermission(dev, outcome)`
  then splits `allowed` (true) from `denied` (false). For an eligible device this reduces to
  `outcome==OutcomeAllow`, but calling the primitive keeps the fail-closed conjunction in #702's single
  unit-tested place (defense in depth — it re-checks eligibility).
- **Keystroke is best-effort, exactly like `ResolveCancel:60-69`.** Consume *first* (commit
  idempotency), then route; a keystroke error (no live session / teardown) is `Warn`-logged with the
  supervisor sentinel and tolerated — the modal is already consumed and moot, so the dismissal must
  still broadcast and the audit must still be written. Aborting would orphan a consumed modal.
- **Two distinct "outcome" vocabularies — do not conflate.** The **wire** `ModalDismissal.Outcome` is
  the **`option_id`** (e.g. `"allow_once"`), per `ModalDismissedPayload` (line 153) and the
  `ModalDismissal` doc (line 370). The **audit** `Entry.Outcome` is the classification
  (`allowed`/`denied`/`denied_unauthorized`). Source is `remote` on every row.

### `answer_token` — decoded, not used server-side (by design)

`answer_token` is the **client's** idempotency key (uniqueness matters, secrecy does not — it is *not*
authorization). The daemon's dedup is the `modal_id` one-shot `Resolve`, which already collapses a
replay to row 1 — a silent no-op with **no second dismissal**, which is exactly what AC2 demands
("no second dismissal"). A server-side `answer_token` store that *re-broadcast* the prior result on
replay would *violate* "no second dismissal" and add unbounded state — so it is deliberately not built.
The token is decoded (it arrives as a param) and otherwise unused; document this inline so a reviewer
doesn't read its absence as a bug. **Never log the token** (low value; honour the no-secrets-in-logs
posture even though it is not secret).

## Concurrency model

No new goroutines, no new locks. `ResolveAnswer` runs synchronously on the V2 manager's single `Run`
dispatch goroutine (`dispatchAppFrame → handleModalAnswer → ResolveAnswer`), so all modal resolutions
are serialized — there is no `Lookup`/`Resolve` TOCTOU between two answers. The only other writer of the
registry is the producer goroutine (`Record`, #716), which **only adds** fresh random `modal_id`s under
the registry's own leaf mutex; it can neither delete nor collide with an in-flight resolution. The
defensive `Resolve`-returns-`ok=false` branch (modal vanished between `Lookup` and `Resolve`) is
therefore unreachable in practice but should be handled as a row-1 no-op rather than asserted. The
keystroke `pty.Write` happens without any lock held (the supervisor seam captures `sessMu` then releases
before writing, #726).

## Error handling

- **No new sentinel.** "Nothing to do" is the `(zero, false)` tuple (the `ResolveCancel` /
  `ScreenSnapshot` convention), not an error.
- **Keystroke errors are tolerated** on the committed path (rows 4/5): `Warn`-log
  (`event: "modal_answer.keystroke_err"`, `modal_id`, `err`) and continue to audit + dismissal. The
  supervisor wraps `ErrNoLiveSession`/PTY errors — never a secret.
- **Decode failures never reach here**: `handleModalAnswer:1510-1511` tolerates a bad payload into an
  empty `ModalAnswerPayload`, which becomes an empty `modal_id` → row 1.
- **`classifyAnswer` miss** (row 3) is `Warn`-logged (`event: "modal_answer.invalid_option"`,
  `modal_id`, `option_id`). The `option_id` is attacker-controlled but slog JSON-escapes it (no
  log-injection) and the whole payload is already bounded by the transport AEAD frame cap (#446);
  **truncate the logged `option_id` to a small bound (e.g. 64 bytes)** so a hostile gated device can't
  pad it to bloat the log. The modal **body** is never logged. Not audited (no security decision was
  made; it is a malformed client frame).

## Testing strategy

Bulk of the work is `cmd/pyry/modal_resolve_v2_test.go` (table/named tests, `t.Parallel()`, the cancel
tests are the template). The relay seam (surface→answer→`modal_dismissed` over the real `Frames`/`Run`
loop) is already proven by #727; one focused relay test re-asserts it for the *answer* arm.

**Harness extensions (`cmd/pyry/modal_resolve_v2_test.go`):**
- Extend `fakeKeystroker` to record `Answer` choices (`[]string`) and `AcceptTrust` count, keeping
  `escCalls` and the injectable `err`.
- Add `recordTrustModal` (mirror `recordPermissionModal` with `tuidriver.ModalClassTrustFolder`).
- Add an `eligibleDevice` helper (`testDevice` + `AllowRemotePermissions: true`); `testDevice` stays the
  ineligible baseline.

**`ResolveAnswer` scenarios** (each asserts: keystroke verb+arg, consume/no-consume via a follow-up
`reg.Lookup`/`Resolve`, audit record count + fields, returned dismissal, and `ok`):
- **Allow / permission (AC1):** eligible device, `allow_once` → `Answer("1")`, modal consumed, one audit
  `allowed`, dismissal `{allow_once, remote}`, `ok=true`.
- **Deny / permission (AC1):** eligible, `reject_once` → `Answer("3")`, consumed, audit `denied`,
  dismissal `{reject_once, remote}`, `ok=true`.
- **Allow / trust:** eligible, `proceed` on a trust modal → `AcceptTrust()` (no `Answer`, no `Esc`),
  consumed, audit `allowed`, dismissal `{proceed, remote}`.
- **Deny / trust:** eligible, `exit` on a trust modal → `SendEsc()`, consumed, audit `denied`,
  dismissal `{exit, remote}`.
- **Ungated device (AC2):** ineligible device, `allow_once` → **no keystroke**, modal **NOT consumed**
  (still `Lookup`-able), audit `denied_unauthorized`, `ok=false`, zero dismissal.
- **Nil device (AC2 fail-closed):** `dev=nil` → behaves as ungated; audit `denied_unauthorized` with
  empty `device_hash`/`device_label`; no keystroke; no consume.
- **Stale modal_id (AC2):** unknown id → no keystroke, no audit, `ok=false`.
- **Replayed answer (AC2 — the idempotency assertion):** eligible `allow_once` twice → first `ok=true`
  (1 keystroke, 1 audit, consumed); second `ok=false` (no 2nd keystroke, no 2nd audit, zero dismissal).
- **Forged / wrong-class option (AC2 defense):** eligible device sends `option_id` not in `o.Options`
  (e.g. `"bogus"`, or `proceed` for a permission modal) → no keystroke, no consume, no audit,
  `Warn`-logged, `ok=false`.
- **Keystroke error tolerated:** eligible `allow_once`, keystroker `Answer` returns
  `supervisor.ErrNoLiveSession` → modal still consumed, audit `allowed` still written, `Warn` logged,
  `ok=true` with dismissal (mirrors `TestModalResolverV2_Cancel_KeystrokeError`).
- **SECURITY — no body leak:** across the allow, deny, and `denied_unauthorized` paths, assert
  `secretModalBody` (and the title `"Permission required"`) never appear in any log line.

Replace `TestModalResolverV2_Answer_NoOp` (answer is no longer a no-op).

**Relay seam (one test, `internal/relay/v2session_modal_test.go`):** drive a `modal_answer` through the
real `Frames`/`Run` loop with a `fakeModalResolver` whose `ResolveAnswer` returns
`({option_id, remote}, true)`; assert a `modal_dismissed{outcome: <option_id>, source: remote}` is
decrypted at every interactive head and **none** at a non-interactive head (mirror
`TestV2Session_ModalCancel_FanOut`). The existing `TestV2Session_ModalAnswer_NoOp` (fake returns
`false` → no broadcast) stays. The relay test cannot assert keystroke/audit (cross-package) — those are
cmd/pyry-side; together they cover AC3 end-to-end.

## Open questions

- **Positional keystroke vs claude's actual rendered options.** The design trusts the producer's
  contract that `Outstanding.Options[i]` is selectable by pressing key `i+1`. If a future producer
  change surfaces an option set that doesn't match claude's on-screen numbering (e.g. 4 abstract
  options where claude renders 3), the positional keystroke would mis-select. This is the producer's
  (#716/#708) invariant to keep, not this consumer's to defend; the `classifyAnswer` membership check
  bounds the blast radius (an id not in `o.Options` is rejected, never mis-keyed). Flag for the #708
  live-wiring run to verify against real claude. **Not a blocker for this ticket.**
- **`exit` → `SendEsc` vs `Answer("2")`.** Both dismiss claude's trust modal; the ticket and the #726
  seam prescribe `SendEsc` (the dedicated dismiss verb, and `exit` is the fail-safe decline). Kept as
  specified; the trust-modal claude interaction semantics are ADR 025 / producer territory.

## Security review

**Verdict:** PASS

This is the security-critical leg of the modal bridge, so the pass was run adversarially against the
spec above, assuming holes. The load-bearing property — *an internet-sourced answer can route an ALLOW
keystroke only from a gated device* — was attack-traced through the `ResolveAnswer` decision matrix and
holds because the eligibility gate (step 2) runs **before** option classification and **before** the
registry consume.

**Findings:**

- **[Trust boundaries] No MUST FIX.** The untrusted inputs are `modal_id`, `option_id`, `answer_token`
  (attacker-controlled strings); the trusted input is the authenticated per-conn `*devices.Device`
  (`s.device`, resolved by the first-frame auth gate upstream). The boundary is single-point: `modal_id`
  is used *only* as a registry map key (forged → `Lookup` miss → no-op; unguessable 122-bit
  `crypto/rand` nonce from #716 prevents guessing an outstanding id); `option_id` is validated for
  membership in the *trusted* `Outstanding.Options` before any use. **Critically, no attacker byte
  reaches claude's PTY**: the routed keystroke is `strconv.Itoa(idx+1)` (a digit derived from the
  trusted option *index*) or a fixed verb (`AcceptTrust`/`SendEsc`) — the raw `option_id` is never
  passed to `Answer`. The attacker selects among surfaced options; it cannot inject a keystroke.
- **[Trust boundaries] No MUST FIX — echoed `option_id`.** Rows 4/5 echo `option_id` back to all
  interactive phones as `ModalDismissal.Outcome`. By then `option_id` has passed row-3 membership, so
  it is always one of the 6 producer-emitted enum labels — never arbitrary attacker text.
- **[Tokens/secrets] No MUST FIX.** `answer_token` is a non-secret client idempotency key — decoded,
  unused server-side (the `modal_id` one-shot `Resolve` *is* the dedup), never logged. The audit entry
  carries `DeviceHash` (SHA-256) + `DeviceLabel` only; the plain token never reaches this code (the
  `ResolveCancel` contract). This ticket's security depends on #716's `crypto/rand` `modal_id` nonce —
  a dependency, not this ticket's code.
- **[File ops] N/A.** `ResolveAnswer` does no filesystem I/O.
- **[Subprocess/exec] N/A — but the PTY-injection angle is covered.** No `exec.Command`/shell. The only
  path to claude's PTY is the safe-answer seam, and it receives a computed digit / fixed verb, never raw
  attacker bytes (see Trust boundaries).
- **[Crypto] N/A.** No crypto in this ticket (the nonce, AEAD, and handshake are upstream).
- **[Network & I/O] No MUST FIX.** Inbound fields are not stored and feed a map lookup + membership
  check, so no unbounded allocation. An ungated device can flood `modal_answer` to write one
  `denied_unauthorized` audit line each — bounded by the single Run goroutine's serialized frame
  processing (no new amplification beyond the transport layer the attacker already drives) and
  *intentional* forensics (recording unauthorized probing). 
- **[Errors/logs] SHOULD FIX (folded into the spec).** The row-3 `Warn` logs the attacker-controlled
  `option_id`. slog JSON-escapes it (no log-injection) and the payload is bounded by the #446 AEAD
  frame cap, so this is not exploitable, but the spec now directs the developer to **truncate the logged
  `option_id` to ~64 bytes** as belt-and-suspenders. No modal body/prompt/title or token is ever logged
  (asserted by a test). Not a gate.
- **[Concurrency] No MUST FIX.** All resolutions run on the manager's single Run goroutine (serialized,
  no `Lookup`/`Resolve` TOCTOU); the registry mutex is a leaf lock; the producer only adds. No new
  goroutine. Crash mid-resolution loses only in-memory ephemeral state — no on-disk partial-write
  corruption; the modal re-surfaces on the next producer detection.
- **[Threat model — ADR 025 § Security model] Addressed.** Silent-approval (fail-closed gate before
  consume), replay/reorder (`modal_id` one-shot consume = first-answer-wins, ADR 025 §4), forged
  `modal_id` (unguessable nonce + membership), keystroke injection (index-not-bytes), audit
  completeness (exactly one non-secret entry per terminal decision). A non-`Allow` outcome that somehow
  reached `AuthorizeRemotePermission` fails safe to deny (the row-5 `else` branch). **Out of scope
  (named):** live producer wiring (#708), deny-on-timeout (#725), local-TTY answer arm + cross-source
  first-answer-wins (#706), and verifying the positional keystroke against real claude rendering (the
  #708 live-wiring run).

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-06-23
