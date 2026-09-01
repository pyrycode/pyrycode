# 1933 — Rewrite the deny-on-deadline claims left standing outside the approval path

**Size:** XS · **Not security-sensitive** (comment-only; the behaviour described was reviewed on #1931 and #1932).

## Files to read first

Read these before editing. Every entry is symbol-anchored — resolve with `codegraph_search` / `codegraph_node`, not line numbers.

| Path | Symbol | What to extract |
|---|---|---|
| `cmd/pyry/main.go` | `mcpApprovalTimeout`, `envApprovalTimeout`, `approvalTimeout` | The three docs being rewritten. Note which paragraphs of `mcpApprovalTimeout` stay (see § Design). |
| `internal/relay/v2session_modal.go` | `reconcileModals` | The fourth doc being rewritten. Read its whole doc block — only the first paragraph changes. |
| `internal/permbridge/permbridge.go` | `Register`, `expire`, `SetAnswerable` | **The authoritative mechanism.** `Register`'s doc already states "timeout is a re-check interval rather than a hard deadline whenever an `AnswerableFunc` is installed"; `expire` is the ask-then-re-arm loop. Copy this vocabulary — do not invent a second phrasing for the same behaviour. |
| `cmd/pyry/modal_resolve_v2.go` | `streamApprovalBridge.ApprovalAnswerable` | What the report actually reads: the approval is answerable iff (a) the bridge still holds a modal correlation for it AND (b) `ActiveConns` has at least one **interactive** conn. Note that (b) is *any* interactive conn — **not** "a conn that has seen this modal". That asymmetry is the whole point of AC3. |
| `cmd/pyry/relay.go` | `startRelayV2` — the `w.approvals.SetAnswerable(bridge.ApprovalAnswerable)` block | Already current (#1932 wrote it). It is the wording model for the rewrite: "asks this on every expiry and re-arms the SAME window while somebody can still answer … denies within one window of the last answerer going away." Do **not** edit this file. |
| `internal/e2e/relay_v2_stream_modal_test.go` | `TestRelayV2_StreamModalPermissionRoundTrip` — its file-head doc and the `cases` table | The source for AC2. Read the `approvalTimeout` / `answerDelay` / `loseAnswerer` field comments and the `extended` + `timeout` rows. The head doc's "THE WINDOW IS A RE-CHECK INTERVAL, NOT A DEADLINE (#1932)" paragraph states exactly what the suite pins today. |
| `docs/knowledge/features/permbridge-package.md` | § "Conditional bound: `expire` and `AnswerableFunc`" and § the production-bound note | Background. Contains one nuance the rewrite must not contradict: since #1932 the *production* bound is `idleTimeout + window`, because a vanished phone stays in the daemon's active set until `internal/relay`'s 15-minute idle sweep. **Read-only — the documentation phase owns this file.** |

## Context

The approval window stopped being an unconditional bound. #1931 taught `internal/permbridge`'s `Registry` to route its timer through `expire`, which consults an injected `AnswerableFunc` and re-arms the *same* window while the answer is yes; #1932 wired `streamApprovalBridge.ApprovalAnswerable` in as that report, so it is live in production rather than shipped-dormant. Both slices rewrote the claims in the files they touched.

Four doc comments across two files outside the approval path still describe the pre-#1931 world, and all four are load-bearing rather than passing mentions:

- `mcpApprovalTimeout` says the registry's timer "denies the request and deletes the entry" after the window elapses with no decision — now conditional on nobody being able to answer.
- `approvalTimeout` says "permbridge's deterministic deny-on-deadline logic (the fail-closed core) is untouched" — the deny-on-deadline logic is precisely what #1931 changed.
- `envApprovalTimeout` says the #1139 e2e proves "the daemon's fail-closed timer denies a no-answer approval within a bounded deadline" — #1932 rebuilt that suite; a no-answer approval with an answerer attached is now the case that must *not* deny.
- `reconcileModals` says an unseen prompt "silently rides unseen on the daemon's 10-minute deny-on-timeout". This one **understates** the stakes rather than merely being stale, which is why it is worth its own AC. See § Design AC3.

No ADR is warranted — #1931 and #1932 already carry the decision record, and `permbridge-package.md` § "Conditional bound" is its evergreen home.

**One out-of-scope stale claim, flagged for the documentation phase, not for this diff.** `docs/knowledge/features/v2-session-manager-state-machine-connect-time-modal-reconcile-outstanding.md` carries the same "rides unseen on the daemon's 10-minute deny-on-timeout (#1909)" sentence that `reconcileModals` does. It is a documentation-phase file; the developer must not touch it, and AC4 forbids doing so. Recorded here so the documentation phase folds AC3's new framing into it after code review.

## Design

No runtime change. Four doc comments are rewritten in place, in the two files AC4 names. Nothing is renumbered, nothing is deleted that is still doing work.

**#1909's rewrite of `mcpApprovalTimeout` is the pattern to copy** — rewrite each claim where it stands.

### AC1a — `mcpApprovalTimeout`, first paragraph

Replace the "after it elapses with no resolver decision, the registry's own timer denies the request and deletes the entry" clause. The replacement must carry three facts:

1. The window is re-checked at every expiry and re-armed for the same duration while the approval is still answerable.
2. It denies once the report says nobody can answer — i.e. within one window of the daemon **observing** the last answerer go away.
3. The "it is a default, not the value" sentence about `envApprovalTimeout` / `approvalTimeout` stays as-is.

On (2): say *observing*, not merely *of the last answerer going away*. The daemon learns a phone is gone from `internal/relay`'s idle sweep, not a WS close, so the two differ by up to the sweep interval. State the distinction in a clause; do **not** restate the idle-sweep arithmetic — `permbridge-package.md` owns that and a constant's doc should point rather than duplicate.

**Both later paragraphs stay stated, unchanged in substance.** "Why ten and not two" (waiting is not the unsafe state — the tool does not run while the approval is outstanding) is still true and now *more* load-bearing, since parking is the common case rather than the deadline. "Why ten and not more" is also still true: #1911 removed the message abandonment that used to pin the number against `streamTurnHoldTimeout`.

### AC1b — `approvalTimeout`

Drop the "Only the timer's DURATION is tunable — permbridge's deterministic deny-on-deadline logic (the fail-closed core) is untouched" sentence. What replaces it must keep the true half and correct the false half:

- **True, keep:** the duration is the only knob. `Register`'s own doc says it precisely — "an extension re-arms the duration `Register` was handed, never a different one" — so this value is what every re-arm uses, not just the first arming.
- **False, cut:** that permbridge's deny logic is untouched by this. The fail-closed core is now conditional; if a claim about it is worth making here, it is that the value flows into a re-check interval, not a deadline.

The "unset or unparseable value falls back to the default, so production behaviour is byte-identical when the env is absent" sentence is still true — keep it.

**Out of scope, do not add:** a note that `approvalTimeout` passes the value through unclamped and that permbridge never extends a non-positive window. True, and pinned by `TestRegistry_NonPositiveWindowIsNeverExtended`, but no AC asks for it and adding it widens a comment-only slice.

### AC2 — `envApprovalTimeout`

Re-derive from the current suite; do not merely soften the sentence, and do not delete the #1139 reference. What `TestRelayV2_StreamModalPermissionRoundTrip` pins today is two-sided, and the shrunk ~2s window is what makes both halves cheap:

- The `extended` row: an answerer stays connected and answers past **more than one** window. Nothing may deny in the meantime, so the late allow still lands.
- The `timeout` row (`loseAnswerer`): the answering phone's session is **ended** first, so nobody is left able to answer, and only then does the window deny.

The doc should describe that pair — a deny once nobody is left able to answer, and explicitly no deny while somebody is — rather than "denies a no-answer approval within a bounded deadline". Attribute as the #1139 e2e, rebuilt under #1932. Two or three sentences; this is a constant's doc, not a test summary.

### AC3 — `reconcileModals`, first paragraph

The mechanical fact (a phone connecting after the raise never saw the raise-time `broadcastInteractive` fan-out, because `EventID == nil` keeps it out of the turn-event replay ring) is unchanged — keep it.

The consequence clause changes, and it goes **up** in stakes, not down. The precise shape, from `ApprovalAnswerable`'s two halves:

> The approval is reported answerable while *any* interactive conn is open — not while a conn that has actually seen this modal is open.

So an interactive phone that reconnects and never gets the modal re-sent is counted as an answerer it structurally cannot be: it never received a `modal_shown`, so it can never produce a `modal_answer`, yet its presence re-arms the window at every expiry. The prompt then parks unseen for as long as that phone stays connected, instead of being bounded by a ten-minute deny. Reconciling is what turns that counted answerer into a real one — which makes this function *more* load-bearing than the old comment claimed, not less. The rewritten paragraph must say that.

**Keep the `modalDenyTimeout` disambiguation verbatim in substance** — that the window meant here is `cmd/pyry`'s `mcpApprovalTimeout`, the window permbridge parks the approval for, and **not** this file's `modalDenyTimeout`, which nothing in production arms (`ArmModalTimeout` has test callers only). Still true, still worth stating so a reader does not mistake the dormant lane for a competing deadline. The "10-minute" figure attached to it is what goes; the disambiguation itself stays.

The rest of `reconcileModals`' doc — the `broadcastModalDismissed` sibling paragraph, the Run-goroutine paragraph, the SECURITY paragraph — is untouched.

### Do not widen the sweep

`deny-on-timeout` appears elsewhere. These mentions are **correct as written** and must not be edited:

- `cmd/pyry/relay.go` — the `modalResolver` construction note and the `ModalResolver` field note. They identify *which* mechanism performs the fail-closed deny now that there is no terminal keystroke; they do not describe what bounds the window. `startRelayV2`'s liveness-report block is already current (#1932 wrote it).
- `cmd/pyry/modal_resolve_v2.go` — `noopKeystroker`'s doc. Same reason.
- `internal/relay/v2session_modal.go` — `modalDenyTimeout` and `ArmModalTimeout`. That lane genuinely *is* an unconditional two-minute bound; it is dormant in production, not stale.
- Anything under `docs/knowledge/` — owned by the documentation phase (see § Context).

## Concurrency model

Not applicable. The diff changes no statement that executes, adds no goroutine, and touches no shared state. The concurrency facts the rewritten comments assert are read from `expire` (ask with `mu` released, re-arm under `mu`, at most one `expire` in flight per entry) and from `ApprovalAnswerable` (never callable from the relay `Run` goroutine) — assert nothing beyond what those two docs already state.

## Error handling

Not applicable — no failure mode changes. The only correctness risk is a comment that asserts something the code does not do, which § Testing strategy covers.

## Testing strategy

No new tests. Comment-only diffs get no test coverage, and adding one would be scope creep.

- `make check` passes (the AC4 gate). It runs `make cite-guard`, so **name symbols, never line numbers** — no `file.go:NNN`, no ranges, no bare `:NNN`. Every reference in the new comments is a symbol name, exactly as the existing text in these blocks does it.
- Verify the diff is comment-only deterministically before committing — every added and removed line must be a `//` comment line:

  ```
  git diff -U0 origin/main -- cmd/pyry/main.go internal/relay/v2session_modal.go \
    | grep -E '^[+-]' | grep -vE '^(\+\+\+|---)' | grep -vE '^[+-][[:space:]]*//'
  ```

  Empty output ⇒ AC4's "changes no statement that executes" holds.
- Verify the file list: `git diff --name-only origin/main` must list exactly `cmd/pyry/main.go`, `internal/relay/v2session_modal.go`, and this spec file.
- Re-read each rewritten paragraph against the symbol it describes (`expire` for the re-arm loop, `ApprovalAnswerable` for the two halves, the e2e case table for AC2). A comment that overstates is the failure mode here, and the only detector is that read-back.

## Open questions

- **How much of the `idleTimeout + window` nuance belongs in `mcpApprovalTimeout`'s doc?** The spec's answer is: one clause saying the daemon must *observe* the answerer go away, and no arithmetic — `permbridge-package.md` holds the full treatment. If the developer finds the clause reads as a riddle without the reason, one further short sentence naming the idle sweep as the observation mechanism is acceptable; reproducing the bound is not.
- **Whether `envApprovalTimeout`'s doc should name the `extended` / `timeout` rows explicitly.** Naming them makes the doc precise but couples it to the case table's spelling. Either is fine; describing the two behaviours without the row names is the safer default.
