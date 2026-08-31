# #1909 — Raise the default human-approval window to ten minutes

**Size:** XS (confirmed; PO sized XS). One production literal, one production doc rewrite, one production comment sentence, two test files, one guide section.

**Labels:** `enhancement`, `size:xs`, `security-sensitive`, `needs-real-claude`.

## Files to read first

Symbol-anchored. Resolve each name with `codegraph_search` / `codegraph_node`; do not go looking for line numbers.

| File | Symbol | What to extract |
|---|---|---|
| `cmd/pyry/main.go` | `mcpApprovalTimeout` | The literal to change **and** the stale doc block to rewrite — it is the whole production edit |
| `cmd/pyry/main.go` | `envApprovalTimeout`, `approvalTimeout` | The override contract that stays byte-identical: `os.Getenv` → `time.ParseDuration` → fall back to the constant. Read it to confirm nothing here moves |
| `cmd/pyry/main.go` | `streamTurnHoldTimeout` | The 15-minute delivery hold the new value must stay clear of. **Read-only — do not edit** |
| `cmd/pyry/mcp_approve.go` | `mcpApproveClientMargin`, `newMCPApproveServer` | The derivation `approvalTimeout() + mcpApproveClientMargin`. Both docs state the *relationship*, never a number — confirm that, then leave this file untouched |
| `cmd/pyry/mcp_approve_test.go` | `TestMCPApproveServer_ClientDeadline` | The only thing in the tree that goes **red**: two `want` literals, one row that goes vacuous, and a doc naming `2m30s` twice |
| `cmd/pyry/approval_timeout_test.go` | `TestApprovalTimeout` | Header prose names "the 2-minute `mcpApprovalTimeout`" while the table asserts symbolically — green while reading false |
| `internal/relay/v2session_modal.go` | `reconcileModals` | The doc sentence "the prompt silently rides the daemon's 2-minute deny-on-timeout unseen" — this is the fourth Go site |
| `internal/relay/v2session_modal.go` | `modalDenyTimeout` | A **different** constant that is also two minutes. Read it once so you can tell the two apart, then leave it exactly as it is |
| `internal/e2e/relay_v2_stream_modal_test.go` | the harness env line that appends `"PYRY_APPROVAL_TIMEOUT="+tc.approvalTimeout` | Confirms the fake tier always sets the window explicitly, so it is default-independent |
| `internal/e2e/realclaude/interactive_stream_permission_deny_test.go` | the `dis.Source != "remote"` guards (both the first-modal check and the retry-loop check) | Why a longer default makes this test *stricter*, not looser |
| `docs/guide.md` | § Environment knobs | The user-facing half: table `Default` cell, the reasoning paragraph, the example, and the ceiling paragraph's issue link |
| `docs/knowledge/features/control-plane-approve-mcp-approve-verb-forward-to-permbridge.md` | § on `mcpApprovalTimeout` | The package overview for this window. **Read-only** — it also carries a "Known gap" about the client margin that #1507 already closed; both are the documentation phase's to fix, not yours |

## Context

`mcpApprovalTimeout` is the fail-closed window `cmd/pyry` hands `permbridge.Register` for every approval. When it elapses with no decision, `permbridge`'s own `time.AfterFunc` resolves the entry to a deny and deletes it. It is two minutes, chosen when nothing in production could reach it.

The chain has since landed — #1080 wired the modal resolver, `pyry mcp-approve` wired `--permission-prompt-tool` — so the number is now a live human deadline. Waiting is not the unsafe state: the tool does not execute while an approval is outstanding, so denying at two minutes prevents nothing that remaining pending was not already preventing.

**How far it can move is bounded, and that bound is the whole reason the value is ten and not larger.** `streamTurnHoldTimeout` (15 minutes) bounds the delivery hold, and a give-up there abandons whatever message was queued behind the waiting turn. Ten minutes buys real human time while staying clear of it. That abandonment is #1911's ticket; this slice only moves the number.

**Ordering note, for the record.** #1911's body states that it "lands **before** the approval deadline moves." No `blockedBy` relationship is recorded between the two issues (verified 2026-08-31 via the `blockedBy` connection on #1909: empty), #1902 is closed, and this ticket's own body accepts ten minutes precisely because it stays clear of the hold. So this spec proceeds. The residual exposure — a queued message now has 5 minutes of slack above the approval window instead of 13 — is stated as an accepted finding in § Security review and is #1911's to close. Flagging, not blocking.

**No ADR.** This changes one tuning constant. ADR 025 § Security model's requirement is "a bounded window ... never auto-grant"; ten minutes is still bounded and still denies. Nothing about the decision record changes.

## Design

There is no new structure. The change is a value and the prose that describes it.

### The production edit — `mcpApprovalTimeout` in `cmd/pyry/main.go`

```go
const mcpApprovalTimeout = 10 * time.Minute
```

The doc above it must be rewritten, not merely renumbered. It currently states three things that are false:

1. *"Until #1080 wires the modal-resolve consumer there is no resolver"* — #1080 landed.
2. *"inert for now because nothing invokes the verb until the `pyry mcp-approve` sibling wires --permission-prompt-tool"* — that sibling landed.
3. *"make it configurable when the full chain lands"* — `envApprovalTimeout` and `approvalTimeout` sit directly beneath it and have done exactly that since #1139.

The replacement doc must say, in the package's own voice:

- What the constant is: the default human-approval window handed to `permbridge.Register` for every `VerbMCPApprove` request; when it elapses with no resolver decision the registry's own timer denies and deletes the entry.
- That it is a **default**, overridable via `envApprovalTimeout` — pointing at `approvalTimeout`, which is the accessor every consumer actually calls.
- Why ten and not more: name `streamTurnHoldTimeout` and say the value is deliberately held clear of it, because a give-up there abandons the message queued behind the waiting turn (#1911). This is the sentence that stops the next reader raising it to twenty.
- Why ten and not two: waiting is not the unsafe state — the tool does not run while the approval is outstanding — so the number is about how long a person may take to reach a phone.
- Keep the honest "a tuning knob, not a contract" framing. Drop the three stale clauses.

**Naming discipline:** `streamTurnHoldTimeout`, `approvalTimeout`, `envApprovalTimeout`, `permbridge.Register` by symbol name. No line numbers anywhere — `make cite-guard` fails the build on a `//`-comment citation that resolves to a declaration at any depth, and there is no range or depth exemption.

### The fourth Go site — `reconcileModals` in `internal/relay/v2session_modal.go`

Its doc says a phone that connects after a raise never sees the prompt, which *"silently rides the daemon's 2-minute deny-on-timeout unseen."* That sentence is about **this** constant, not about `modalDenyTimeout` in the same file. The evidence, already settled on the ticket and re-confirmed here:

- Nothing in production arms `modalDenyTimeout`. Its only arming site is `ArmModalTimeout`, whose only callers are three test files; `v2session.go`'s mention is a comment.
- The path that raises a permbridge-parked approval as `modal_shown` is `streamApprovalBridge.Surface` (`cmd/pyry/modal_resolve_v2.go`), which records into modalbridge and broadcasts without arming any relay-side timer. Its own doc says a dropped surface means claude "then times out to deny via permbridge."

Update the number in that sentence and **name the constant** (`mcpApprovalTimeout`, or `approvalTimeout`'s window) so the next reader does not have to re-derive which of the file's two two-minute constants was meant. That naming is the durable half of this edit; the number is the perishable half.

**`modalDenyTimeout` is not touched.** Different constant, different mechanism, unchanged here.

### `cmd/pyry/mcp_approve.go` — no edit

`mcpApproveClientMargin` and `newMCPApproveServer` both document the *relationship* (`approvalTimeout()` plus the margin, so the daemon's informative deny wins the race against the client's generic one) and neither states a number. #1507 already made the client derive from the env-aware accessor rather than the constant, so raising the default raises both ends of the control socket together with no code change here. Read both docs to confirm; change neither.

### `docs/guide.md` § Environment knobs — the user-facing half

Four things in that section are wrong or become wrong:

1. **Table.** The `PYRY_APPROVAL_TIMEOUT` row's `Default` cell says `2m` → `10m`.
2. **Reasoning paragraph.** It argues *from* two minutes: "Two minutes suits someone sitting at the machine and is short for a remote client. Around ten minutes is a better fit for remote use." Ten minutes is now what you get. Reframe: the default is sized for someone answering from a phone, and the knob is there to shorten it if you are at the machine and want a faster fail-closed, or to lengthen it within the ceiling below. The "waiting is not the risky state, because the tool does not run while the request is outstanding" sentence is still true and should survive.
3. **Example.** `PYRY_APPROVAL_TIMEOUT=10m pyry` now sets the knob to exactly its own default and teaches nothing. Re-point it at a value that is genuinely a change from the default — a shorter one reads best given the paragraph's new direction.
4. **Ceiling paragraph.** The fifteen-minute advice is still true and stays. Two things in it move: the link to [issue #1902](https://github.com/pyrycode/pyrycode/issues/1902) must become #1911 (the split closed #1902; #1911 is where the queued-head abandonment now lives), and the paragraph should note that the default is already set clear of the hold — with the default at `10m` there are only five minutes of headroom, not thirteen, and a reader raising the knob should know that.

The trailing sentence ("The value takes any Go duration, such as `90s`, `10m` or `1h`. An unset or unparseable value falls back to the default.") is a *format* example and stays correct either way; changing `10m` there is optional and not an AC.

`docs/guide.md` is not the documentation phase's file — `docs/knowledge/**` is. This edit is yours.

### Out of scope — name it, do not edit it

- `docs/knowledge/features/control-plane-approve-mcp-approve-verb-forward-to-permbridge.md` states the window as "2 minutes" and carries a stale "Known gap" claiming the client margin still derives from the const (#1507 closed that).
- `docs/knowledge/features/v2-session-manager-state-machine-connect-time-modal-reconcile-outstanding.md` and `...-immediate-flush-on-reconnect-reconnect.md` each mirror the "2-minute deny-on-timeout" sentence.

All three are the documentation phase's to fold in after code review. Do not edit them; do not add them as an AC.

- `docs/specs/architecture/1507-*.md`, `1139-*.md`, `1105-*.md`, `1104-*.md`, `1080-*.md` are other tickets' build artifacts and are frozen history. Do not edit.
- `internal/e2e/internal/fakeclaude/main.go`'s mention reads "PYRY_APPROVAL_TIMEOUT, ~2s in the timeout case" — that is the explicit override, not the default. No edit.
- `internal/attachments/registry_test.go`, `cmd/pyry/inbound_deliver_rotation_test.go`, `internal/e2e/realclaude/initialize_control_probe_test.go`, `internal/msgqueue`'s `defaultGiveUpAfter` all mention two minutes about unrelated mechanisms. No edit.
- `acpPermissionTimeout` appears only in a knowledge doc; no such symbol exists in `cmd/` or `internal/` (verified). Not this window, not yours.

## Concurrency model

Unchanged. Stated so the reviewer can confirm nothing moved:

- `permbridge.Registry` arms one `time.AfterFunc` per `Register`, inside the same `mu` critical section that installs the map entry — so there is no fire-before-install race regardless of the duration.
- `resolve` is the single arbiter: `delete` under `mu` picks exactly one winner, which becomes the sole writer of the buffered(1) channel. A raced answer and a fired timer still produce exactly one verdict.
- Raising the duration changes only how long an entry and its timer live. No goroutine is added, none changes lifetime shape, no lock ordering moves.
- `pyry mcp-approve` reads the env once per short-lived process in `newMCPApproveServer`; the daemon reads it once when it installs the registry. Both continue to.

## Error handling

Unchanged, and this is load-bearing for the ticket:

- Deny-on-deadline is `permbridge`'s, and its logic is untouched. Fail-closed stays fail-closed.
- The `mcpApproveClientMargin` ordering invariant holds at every window because both ends derive from `approvalTimeout()` (#1507): the daemon's informative "approval request timed out" deny fires one margin before the client's generic "approval unavailable". At the new default that is 10m vs 10m30s.
- An unset, empty or unparseable `PYRY_APPROVAL_TIMEOUT` still falls back to the constant. `os.Getenv` returns `""` for both unset and empty, so the two are the same code path by construction.
- No new log line, no new error value, no new branch.

## Testing strategy

Scenarios, not code. Three test-side changes; only the first goes red today.

### `TestMCPApproveServer_ClientDeadline` (`cmd/pyry/mcp_approve_test.go`) — goes red

- The **unset** row and the **unparseable** row both hardcode `2*time.Minute + 30*time.Second`. Both become `10*time.Minute + 30*time.Second`. These two rows are the tree's only *numeric* pin on the new default: combined with the test's existing `s.timeout - approvalTimeout() == mcpApproveClientMargin` assertion, they force `approvalTimeout()` with the env absent to be exactly ten minutes. That is what makes AC-1 non-vacuous.
- The **`2s` row** is unchanged and keeps the override honest.
- The **`generous override 10m` row stops proving anything** once the default is also ten minutes: `approvalTimeout()` returns `10m` whether or not it reads the env, so the row would stay green against a seam that ignored `PYRY_APPROVAL_TIMEOUT` entirely. Re-point it at a window distinct from the new default. **Use `12m` → `12m30s`**, and rename the row to match. `12m` is above the new default (so it also pins that the accessor does not clamp *down* to the default) and below `streamTurnHoldTimeout`, so the fixture does not read as advice contradicting the guide's fifteen-minute ceiling.
- **Add a deterministic guard so no future default change can silently re-vacuum a row.** In the subtest, for any row whose `env` parses as a duration, fail if that parsed value equals `mcpApprovalTimeout`. The unset row (`""`) and the unparseable row (`"not-a-duration"`) fail to parse and are excluded by construction — no special-casing needed. This is the deterministic half of AC-3's "no row can pass by reading the default"; the row comment is the advisory half. ~4 lines.
- The test's doc names `2m30s` twice and says the first two rows "keep 2m30s". Both become the new value. Keep the `#1507` reference and the "serial by construction — `t.Setenv` forbids `t.Parallel`" note.

### `TestApprovalTimeout` (`cmd/pyry/approval_timeout_test.go`)

- The header names "the 2-minute `mcpApprovalTimeout`". Correct the number. Also correct the trailing claim that the e2e relies on "the default being unchanged" — that clause is now the opposite of true; the fake tier sets the window explicitly on every arm, so it is default-independent. Say that instead.
- The four table rows stay as they are. The two fallback rows compare against `mcpApprovalTimeout` symbolically on purpose: they assert the *routing* (unset/empty/unparseable reaches the default), which is orthogonal to the value.
- **Add one assertion after the loop** pinning the value in the file that owns the accessor: with `envApprovalTimeout` set to `""`, `approvalTimeout()` returns exactly `10 * time.Minute`. Comment it with why it exists — the symbolic rows cannot see a value regression, and `docs/guide.md` § Environment knobs publishes this number to users, so the two must not drift. ~5 lines. Without it, the default's only numeric pin lives in a different file about a different subsystem, one refactor away from vanishing silently.
- The `30s` override row carries the same latent shape the `12m` row is being fixed for (it would go vacuous if the default ever became `30s`). Not observed, not fixed here — the guard added above covers the class in the file where AC-3 lives.

### Not changed, and why — state this in the PR rather than editing anything

- `internal/e2e/relay_v2_stream_modal_test.go` sets `PYRY_APPROVAL_TIMEOUT` explicitly on every arm (the harness appends it to the daemon env), including the `2s` timeout arm. Default-independent.
- `internal/e2e/realclaude/interactive_stream_permission_deny_test.go` and its allow sibling `interactive_stream_modal_resolution_test.go` do not set it and run at the default, but neither waits the window out. The deny test asserts `Source == "remote"` and `Outcome == "reject_once"` on the first modal *and* re-asserts `Source == "remote"` on every retry dismissal, precisely so a timeout-deny cannot masquerade as its own reject. A longer window makes that attribution **stricter**: it widens the margin before the daemon's timer could produce a `Source == "timeout"` dismissal the test would then fail on. This is why the ticket carries `needs-real-claude` — it runs a live permission round-trip at a default this ticket edits.

### Gates

`make check` is the gate for the two unit tests. Because this edits a default the live permission suite runs at, `make preship` (or at minimum `make e2e-realclaude`) is the honest gate for the realclaude claim above. **Read the count of `=== RUN` lines, not the exit code** — a missing credential skips everything and a build break runs nothing, and both exit 0.

## Open questions

- **The `12m` fixture value is a judgement call, not a derivation.** The binding constraints are: it must differ from the new default (AC-3), and it should sit below `streamTurnHoldTimeout` so the fixture does not read as advice against the guide. Any value satisfying both is defensible; `12m` is chosen because it is also *above* the default, preserving the original row's "generous" direction and pinning that the accessor does not clamp down.
- **Whether the guide's format-example list should stop using `10m`** now that `10m` is the default. Left to the developer; not an AC either way.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No finding. The boundary this ticket sits on is "an outstanding approval has not granted anything" and it does not move. `permbridge.Register` parks the request and returns a handle the `approve` tool blocks on; the tool does not execute until `resolve` produces a verdict, and `resolve`'s `delete` under `mu` is the single arbiter for both the answer path and the timer path. A longer window extends *waiting*, never *permission*. The spec changes the duration argument to `Register` and nothing else on that path.
- **[Trust boundaries]** No finding — fail-open is structurally impossible to introduce here. The only value that changes is a `time.Duration` passed to `time.AfterFunc`. The safe default (`Deny(reasonTimeout)`) is a literal inside `Register` and is not parameterised by this ticket.
- **[Threat model alignment]** No finding. ADR 025 § Security model requires "an unanswered prompt is answered with the SAFE default (deny / ESC) after a bounded window. Never auto-grant." Ten minutes is bounded and still denies. The ADR specifies no number, so no ADR text is falsified.
- **[Network & I/O — resource exhaustion]** SHOULD FIX, accepted as designed. `permbridge.Registry` has **no cap on outstanding entries**: it is a bare `map[string]*pending` with one `time.AfterFunc` per entry, so a 5× longer window means entries and timers live 5× longer and the steady-state count of outstanding approvals rises accordingly. The mitigating structure is the fan-in, not a cap: entries are minted only by `claude` calling the `approve` tool through `--permission-prompt-tool`, one per non-allowlisted tool use, and claude blocks on each result — so the count is bounded by concurrent supervised tool uses, not by anything a remote party can drive. Each entry is a small struct, a buffered(1) channel and a timer. Not gating; naming it so a future ticket that lets a remote party mint approvals knows a cap becomes required at that moment.
- **[Network & I/O — read deadlines]** No finding. The `pyry mcp-approve` client read deadline rises with the window (10m30s) by design — that is AC-3, and #1507's derivation from `approvalTimeout()` is what keeps the daemon's informative deny ahead of the client's generic one at *every* window rather than only at the old default. The socket held open for that duration is a Unix socket under `~/.pyry/`, reachable only by a local process already running as the user, which has strictly more capability than the socket grants. No listener, deadline or TLS setting changes.
- **[Tokens, secrets, credentials]** SHOULD FIX, accepted. No token handling changes, but the modal answer window widens 5×, so the interval in which a client-minted answer token could be replayed against a still-outstanding prompt widens with it. Two things bound the residual: the answer arrives over the Noise_IK-sealed relay channel, so the actor must already be a paired device; and `permbridge.resolve` is a one-shot — the first winner deletes the entry, so a replay after resolution is a no-op returning `false`. The exposure is "a paired device can answer a prompt for longer", which is the feature. Nothing to change in the spec.
- **[Error messages, logs, telemetry]** No finding. The spec adds no log line and no error value. The one message-selection behaviour that depends on these numbers — which side's deny text the operator sees — is preserved by `mcpApproveClientMargin` and is pinned at every window by the `s.timeout - approvalTimeout() == mcpApproveClientMargin` assertion in `TestMCPApproveServer_ClientDeadline`, which this ticket keeps.
- **[Concurrency]** No finding. `Register` arms the timer inside the same `mu` critical section that installs the map entry, so there is no fire-before-install race at any duration; `mu` is a leaf lock never held across the channel send; `resolve` is idempotent. Raising the duration adds no goroutine and changes no lock ordering. Daemon shutdown with an outstanding approval behaves as it does today — a 5× longer window makes that state 5× more likely to be occupied, but the handling of it is unchanged and out of scope here.
- **[Availability — the reason the number is ten and not larger]** OUT OF SCOPE, owned by **#1911**. `streamTurnHoldTimeout` (15 minutes) bounds the delivery hold and a give-up there *abandons* the message queued behind the waiting turn. At the new default the headroom between the approval window and that hold is five minutes rather than thirteen, so a person who takes most of the window to answer leaves a queued message with a much thinner margin. This is the ticket's own stated bound and the explicit reason it stops at ten. #1911's body asserts it should land first; no `blockedBy` is recorded and this ticket's premise (10m stays clear of 15m) is what PO accepted. The spec's guide edit is required to carry this forward to users: the ceiling paragraph must re-point from the now-closed #1902 to #1911 and must say the default is already set clear of the hold.
- **[File operations]** Not applicable — the design touches no filesystem path, creates no file, and reads no user-controlled path. `docs/guide.md` is edited by a human/developer at build time, not at runtime.
- **[Subprocess / external command execution]** Not applicable — no `exec.Command` and no environment scrubbing changes. `pyry mcp-approve` is spawned by `claude`, whose invocation this ticket does not touch; the only effect is that the short-lived process's own read deadline is longer.
- **[Cryptographic primitives]** Not applicable — no RNG, no key material, no comparison of attacker-controlled values against secrets. The change is one `time.Duration` literal.

**Reviewer:** architect (self-review per the security-review checklist)
**Date:** 2026-08-31
