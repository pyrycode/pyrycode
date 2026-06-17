# Spec #661 — Mobile live e2e: documented daemon-side setup so the turn bridge resolves a real claude transcript

**Size: XS** (downgraded from S). Deliverable is **one new documentation file**; zero production `.go` files, zero new types, zero tests. The end-state criterion is verified by an **operator runbook** on the live emulator stack, not by CI. See [§ Size & the doc-vs-code fork](#size--the-doc-vs-code-fork) for why this is documentation-only and not a daemon code gap.

> **This is a documentation-deliverable ticket.** The developer's worktree mutates exactly one file: the new doc under `docs/knowledge/features/`. The general "developer worktree should only touch code, tests, and the spec" guidance guards against smuggling a *fixed-cost knowledge-doc AC onto a code ticket*; here the doc **is** the work (there is no code), so that rule's rationale does not fire. Do **not** add a `docs/knowledge/codebase/<N>.md` AC — that stays the documentation phase's job. Do **not** touch `docs/knowledge/INDEX.md` — the documentation phase is its sole writer.

## Files to read first

- `cmd/pyry/interactive_turn_stream_v2.go:98-155` — `resolveLatestSessionJSONL(dir)`: the producer's resolver. Scans `dir` for `<uuid>.jsonl`, returns the most-recently-modified one + its size as the tail start offset. Returns `no session jsonl found in <dir>` when the dir has no matching file. **This is the function that emits the warning in the ticket.** Note the doc-comment already states the "rotation-following is load-bearing" and "reuses the daemon's already-computed `claudeSessionsDir`" facts — lift them.
- `cmd/pyry/main.go:114-131` — `resolveClaudeSessionsDir(workdir)`: computes `<HOME>/.claude/projects/encode(workdir)` from `-pyry-workdir` (empty → process cwd). Computed **once at startup** and threaded to the relay. This is the "sessions dir computed from workdir" half of the mapping.
- `internal/sessions/reconcile.go:20-48` — `encodeWorkdir` + `DefaultClaudeSessionsDir`: the exact encoding rule (`/` **and** `.` → `-`, so `/tmp` → `-tmp`, and a dotted dir doubles the dash). This is *why* the warning reads `~/.claude/projects/-tmp`. Cite the empirical-verification comment.
- `cmd/pyry/relay.go:336-344` — the production gate `if bridge != nil && claudeSessionsDir != ""` that wires `startInteractiveTurnStreamV2`, plus the `else if bridge != nil` branch that logs `interactive_turn_stream.no_sessions_dir`. Confirms the stream is wired live in normal daemon operation (service mode), not just in tests — so a real workdir + real claude is sufficient; no special harness path is needed.
- `docs/knowledge/codebase/642.md` (whole) — the wire-level capstone. **The single most important reference.** It proves the resolver tails a real-claude-format transcript at the aligned dir, documents the **cold-start producer-subscribe race** ("Lessons learned" → pre-create the session JSONL before the daemon starts), and the sessions-dir-alignment recipe. The doc must cross-reference this as the SSOT for the seeding pattern.
- `docs/knowledge/features/e2e-realclaude.md:36-54` — Max-plan operator auth + the `~/.claude.json` / `hasCompletedOnboarding` prerequisite, and the "run `claude` once interactively first" recipe. The runbook reuses this auth setup; cross-reference rather than restate.
- `docs/deployment.md:7-12` ("Prerequisites") — establishes the existing convention "run `claude` once interactively before installing pyry … so `~/.claude/` is initialised." The runbook's "real claude session must exist" step is the same convention applied to the e2e workdir; cite it for consistency of voice.
- `internal/e2e/relay_assistant_turn_test.go:37-56` — the canonical `conversations.json` seed shape (`~/.pyry/<name>/conversations.json`, one `{"id": "<uuid>", …}` entry). The runbook's conversation-registry step mirrors this; cite it as the concrete on-disk shape.

## Context

Rung 3 of the mobile e2e ladder is the first **live emulator** run: a real `pyry` daemon (`PYRY_MOBILE_V2=1`), the Noise handshake, and a phone (emulator) driving an interactive structured turn stream end-to-end. On 2026-06-17 the handshake succeeded but the structured turn bridge spun:

```
WARN turnbridge: resolve session jsonl, retrying  error="no session jsonl found in ~/.claude/projects/-tmp"
```

**Root cause.** The harness started the daemon with a throwaway `-pyry-workdir=/tmp` and no real workspace/conversation. The daemon computes its sessions dir once at startup (`resolveClaudeSessionsDir` → `<HOME>/.claude/projects/encode(/tmp)` = `~/.claude/projects/-tmp`) and the producer tails the newest `<uuid>.jsonl` there (`resolveLatestSessionJSONL`). With no real claude session ever having run in `/tmp`, that dir is empty, every resolve fails, and the producer Warn-retries forever instead of opening its events stream.

The wire-level capstone #642 sidesteps this with `fakeclaude` writing a **scripted** transcript to the aligned dir. The emulator rung needs the **real** mapping: a real workdir + a real conversation + a live `claude` whose own transcript the bridge resolves unaided. This ticket documents that minimal setup.

## Size & the doc-vs-code fork

The ticket body (and the PO's refinement) leaves a fork: **document** the setup, or — if a phone-created conversation genuinely cannot be mapped to a real claude session with existing features — **route back for a code split**. Resolved by evidence to **documentation-only**:

1. **The sessions dir is computed purely from the workdir** (`resolveClaudeSessionsDir`, `cmd/pyry/main.go:118-131`) — an existing feature. Point `-pyry-workdir` at a real workspace and the dir resolves correctly.
2. **The resolver is conversation-agnostic** (`resolveLatestSessionJSONL`, `cmd/pyry/interactive_turn_stream_v2.go:114-155`): it returns the **newest** `<uuid>.jsonl` in that one dir, independent of any conversation id. There is *one* supervised claude, *one* workdir, *one* sessions dir. So "a phone-created conversation cannot be mapped to a real transcript" is **not** a code gap — the conversation id never participates in transcript resolution.
3. **The conversation registry is a separate, already-shipped gate.** A `conversations.json` entry exists only to satisfy `supervisor.WriteUserTurn`'s `ValidateConversation` check so the turn is accepted and the cursor stamps (#312). Seeding `conversations.json` is an existing feature, exercised in `internal/e2e/relay_assistant_turn_test.go:37-56`.
4. **#642 already proves the full producer path live** against a real-claude-*format* transcript at the aligned dir. The only delta for the emulator rung is *who writes the transcript* (a real `claude` process instead of a scripted append) — and a real `claude` writes its transcript automatically when run in its workdir (`docs/deployment.md:7-12`).

All four are existing daemon features. There is **no code to add**, so no `needs-rework:po` split. The deliverable is the runbook doc (per `[[po-document-or-support-fork-resolve-by-evidence]]`). The S→XS downgrade is taken: zero production files, one doc.

**§4 production-source self-check:** new/modified `*.go` / `*.kt` / `*.ts` files (excluding tests, `*.md`, and this spec) = **0**. Well under the ≥5 gate. **File-overlap check:** ran `git fetch origin --prune` + branch-overlap scan against all 17 in-flight `feature/*` branches for the new doc path; no overlap (a brand-new ticket-unique filename cannot conflict).

## Design — the deliverable doc

**Create one new file:** `docs/knowledge/features/mobile-live-e2e-runbook.md`.

**Home rationale (architect's call).** A new file, *not* an append to `e2e-harness.md` or `e2e-realclaude.md`. Those two document the **automated Go suites** (`internal/e2e` fakeclaude harness; the `e2e_realclaude` package). This is a **manual operator runbook** for the live emulator + real-claude + relay stack — a distinct, evergreen concern. Keeping it separate keeps both scopes clean; the runbook cross-references both as neighbors. (The documentation phase will add the one-line `INDEX.md` entry post-merge — do not write it here.)

The doc is prose + a numbered runbook + one log-observation block. Required sections (the developer writes the project's house markdown style; these are the *contract* for what must be covered, not a layout to transcribe verbatim):

1. **Title + one-paragraph purpose.** "Minimal daemon-side setup so the v2 structured turn bridge resolves a *real* claude session transcript during the live emulator e2e (rung 3 of the mobile e2e ladder)." State up front: live stack is operator-machine-only (emulator + real claude + relay), never CI.

2. **The resolution machinery (the SSOT pointer).** Two functions, cited with file:line, described by contract only (no code paste):
   - `resolveClaudeSessionsDir(workdir)` → `<HOME>/.claude/projects/encode(workdir)`, computed once at startup from `-pyry-workdir` (`cmd/pyry/main.go:118-131`).
   - `resolveLatestSessionJSONL(dir)` → tails the newest `<uuid>.jsonl` in that dir; errors `no session jsonl found in <dir>` when empty (`cmd/pyry/interactive_turn_stream_v2.go:98-155`).
   - The `encodeWorkdir` rule: `/` **and** `.` → `-` (`internal/sessions/reconcile.go:20-34`) — so `/tmp` → `-tmp`. **This is AC#4's cross-reference requirement.**

3. **Why `-pyry-workdir=/tmp` with no seed spins (AC#2).** Walk the failure: encode(`/tmp`)=`-tmp` → `~/.claude/projects/-tmp` is empty (no real claude session ever ran there) → `resolveLatestSessionJSONL` returns `no session jsonl found in ~/.claude/projects/-tmp` → the producer Warn-retries (`turnbridge: resolve session jsonl, retrying`) instead of opening its stream. Reproduce the exact warning string from the ticket.

4. **The minimal setup (AC#1) — numbered runbook.** Each step names the existing feature it leans on:
   1. **Pick a real workspace dir `W`** (a real project dir, not a throwaway). This is what `-pyry-workdir` points at.
   2. **Ensure a real claude session has run in `W`** so at least one `<uuid>.jsonl` transcript exists under `~/.claude/projects/encode(W)/` *before* the daemon's producer first resolves — pre-creating the transcript is the fix for the **cold-start producer-subscribe race** documented in `codebase/642.md` (Lessons learned). Concretely: run `claude` once interactively in `W` (same "run claude once first" convention as `docs/deployment.md:7-12` and the realclaude auth setup in `e2e-realclaude.md:48-54`), then exit. (The daemon's own supervised claude will also write a transcript once it processes a turn, but pre-existing one removes the startup retry window.)
   3. **Seed the conversation registry.** Add a conversation entry to `~/.pyry/<instance-name>/conversations.json` (on-disk shape per `internal/e2e/relay_assistant_turn_test.go:37-56`) whose id is the conversation the phone will drive, so `WriteUserTurn`'s `ValidateConversation` gate accepts the turn and stamps the cursor (#312). Explain the id maps the phone's turn to the supervised claude — **not** to a specific transcript (transcript selection is newest-wins, conversation-agnostic).
   4. **Start the daemon** with `-pyry-workdir=W` and `PYRY_MOBILE_V2=1` (+ the operator's normal relay flags). The producer then computes `claudeSessionsDir = ~/.claude/projects/encode(W)`, the gate `bridge != nil && claudeSessionsDir != ""` fires (`cmd/pyry/relay.go:336-344`), and the producer tails the resolved transcript.

5. **How the setup removes the retry loop (AC#2, second half) + the observable (AC#3).** State the daemon-log observables the operator checks on the live stack:
   - the `turnbridge: resolve session jsonl, retrying` warning **stops recurring**, and
   - the producer **opens its events stream on the resolved transcript** (a real, non-scripted `<uuid>.jsonl` under `~/.claude/projects/encode(W)/`).
   Mark this explicitly as **operator-verified via the runbook on the live stack, not a CI gate** (`[[po-document-or-support-fork-resolve-by-evidence]]`: operator-only live-stack AC = runbook-verified daemon-side observable). The developer cannot and need not run this stack.

6. **Cross-references (AC#4).** Link, with one-line "what it gives you" each: `codebase/642.md` (the scripted-transcript seeding pattern + cold-start lesson — the SSOT), `e2e-harness.md` + `e2e-realclaude.md` (the automated suites this runbook is the manual sibling of), and the two resolution functions by file:line. The doc must make the **single source of truth** explicit: real transcript resolution rides the *same* `resolveClaudeSessionsDir` / `resolveLatestSessionJSONL` machinery #642 aligns against — the emulator rung changes only the transcript's author.

**Constraints on the doc body:**
- No code blocks longer than a shell-command line or the reproduced warning string. Cite functions by file:line; do not paste their bodies.
- Voice/format: match the existing `docs/knowledge/features/*.md` house style (see `e2e-realclaude.md` for the prose register and the "Operator … setup" framing).
- Keep it minimal — this is a setup recipe, not a tutorial on the whole v2 stack. Defer the v2 wire details to `protocol-mobile.md` and ADR 025 by reference.

## Concurrency model

N/A — documentation only. For accuracy, the doc must correctly describe **one** concurrency-relevant fact: the producer captures its tail offset at the **first successful resolve** (cold-start race), which is why step 4.2 (a pre-existing transcript) matters. Source: `codebase/642.md` "Lessons learned"; do not re-derive, cite.

## Error handling

The "error" being documented is the benign-vs-stuck `resolveLatestSessionJSONL` retry:
- **Benign (transient):** during startup before any transcript exists, the producer Warn-logs and retries; once a transcript appears it resolves and proceeds.
- **Stuck (the bug):** an empty sessions dir that *never* gets a transcript (throwaway `/tmp`, no real claude) → permanent retry loop. The runbook's job is to make the dir non-empty by construction (real workdir + real claude session). The doc explains both states so an operator reading a transient startup warning doesn't mistake it for the failure.

## Testing strategy

- **No automated test.** The live emulator + real-claude + relay stack runs only on an operator machine, never under `-tags e2e` CI (consistent with `e2e-realclaude.md` "CI cadence"). Adding a realclaude+relay e2e is explicit scope-creep and out of bounds for this ticket.
- **The existing deterministic proof stands in for resolution correctness:** #642 already proves `resolveLatestSessionJSONL` resolves and tails a real-claude-format transcript at the aligned dir (3× deterministic, `go test -tags=e2e`). The runbook is the operator confirmation that a *real* claude (not fakeclaude) populates that same dir.
- **AC#3 verification = the runbook itself**, executed by the operator on the live stack: follow the steps, observe the two daemon-log observables in § Design step 5. Record the result on the ticket.

## Open questions

- **Future automated live e2e (out of scope, deferred).** The `PYRY_FAKE_CLAUDE_JSONL_TRIGGER` + sessions-dir-alignment + pre-create recipe from #642 already gives a *deterministic* stand-in. A future slice could build a real-claude+relay live e2e that exercises this exact runbook, but it inherits the same harness-gap deferrals noted in `codebase/642.md` (the #647/#649 forward pointer) and the operator-auth constraints of `e2e-realclaude.md`. The doc should include a one-line "automated coverage is deferred; #642's deterministic proof + this runbook are the current coverage" note, not build it.
- **Doc filename.** `mobile-live-e2e-runbook.md` chosen for discoverability; if the documentation phase prefers `mobile-emulator-e2e-setup.md` at INDEX time, that's a rename, not a content change.
