# Mobile live e2e runbook — daemon-side setup for real-transcript resolution

The minimal daemon-side setup so the v2 structured turn bridge resolves a **real**
claude session transcript during the live emulator e2e (rung 3 of the mobile e2e
ladder: a real `pyry` daemon with `PYRY_MOBILE_V2=1`, the Noise handshake, and a
phone emulator driving an interactive structured turn stream end to end).

This is the **manual operator runbook** for the live stack — emulator + a real
`claude` + the relay. That stack runs **only on an operator machine, never under
CI** (same constraint as the realclaude suite's [CI cadence](e2e-realclaude.md#ci-cadence-code-review-phase-no-nightly-workflow)).
It is the manual sibling of the automated suites documented in
[`e2e-harness.md`](e2e-harness.md) (the fakeclaude `internal/e2e` harness) and
[`e2e-realclaude.md`](e2e-realclaude.md) (the real-`claude` trust-boundary suite);
the deterministic stand-in for the structured path is [#642](#cross-references)'s
two-phone test, which proves the same resolution machinery against a real-claude-
*format* transcript. The only delta the live rung adds is **who writes the
transcript**: a real `claude` process instead of a scripted append.

For the v2 wire types and the structured-streaming design this runbook sits under,
see [ADR 025](../decisions/025-mobile-remote-head-interactive-session.md) § Phase 2
and [`turnbridge-package.md`](turnbridge-package.md) — this doc is a setup recipe,
not a tour of the stack.

## The resolution machinery (single source of truth)

Transcript resolution rides two existing daemon functions; the live rung changes
neither. They are the SSOT a future contributor should read before touching this
setup:

- `resolveClaudeSessionsDir(workdir)` (`cmd/pyry/main.go:114-131`) — computes
  `<HOME>/.claude/projects/encode(workdir)` from `-pyry-workdir` (an empty workdir
  resolves to the process cwd, matching claude). **Computed once at startup** and
  threaded to the relay.
- `resolveLatestSessionJSONL(dir)` (`cmd/pyry/interactive_turn_stream_v2.go:98-155`)
  — scans `dir` for `<uuid>.jsonl` files and returns the **most-recently-modified**
  one plus its size as the tail start offset. Returns `no session jsonl found in
  <dir>` when the dir holds no matching file. The resolver is **conversation-
  agnostic**: it picks the newest transcript in the one sessions dir, independent of
  any conversation id (there is one supervised claude, one workdir, one sessions
  dir).
- `encodeWorkdir(workdir)` (`internal/sessions/reconcile.go:20-34`) — the encoding
  `resolveClaudeSessionsDir` uses: it replaces **both** `/` **and** `.` with `-`
  (verified empirically against claude). So `/tmp` encodes to `-tmp`, and a dotted
  dir doubles the dash (`/foo/.bar` → `-foo--bar`).

In normal service-mode operation the relay wires the producer behind the gate
`if bridge != nil && claudeSessionsDir != ""` (`cmd/pyry/relay.go:339`); an
unresolvable dir disables the stream (logged `interactive_turn_stream.no_sessions_dir`).
So a real workdir plus a real claude is sufficient — no special harness path.

## Why `-pyry-workdir=/tmp` with no seed spins forever

This is the failure observed on the first live emulator run (2026-06-17). The
harness started the daemon with a throwaway `-pyry-workdir=/tmp` and no real
workspace or conversation:

1. `resolveClaudeSessionsDir("/tmp")` computes `~/.claude/projects/` + `encodeWorkdir("/tmp")`
   = `~/.claude/projects/-tmp`.
2. No real claude session ever ran in `/tmp`, so that dir is **empty**.
3. `resolveLatestSessionJSONL` returns `no session jsonl found in ~/.claude/projects/-tmp`.
4. The producer Warn-logs and retries every `subscribeRetryDelay`
   (`internal/turnbridge/producer.go:182`) instead of opening its events stream:

   ```
   WARN turnbridge: resolve session jsonl, retrying  error="no session jsonl found in ~/.claude/projects/-tmp"
   ```

Because nothing ever writes a transcript into `-tmp`, the retry never succeeds —
the bridge spins indefinitely and the phone's structured stream never sources.

## The minimal setup

Each step names the existing feature it leans on.

1. **Pick a real workspace dir `W`.** A real project dir, not a throwaway — this is
   what `-pyry-workdir` points at. `resolveClaudeSessionsDir(W)` then resolves to
   `~/.claude/projects/encode(W)`, the dir the producer tails.

2. **Ensure a real claude session has run in `W`** so at least one `<uuid>.jsonl`
   transcript exists under `~/.claude/projects/encode(W)/` **before** the daemon's
   producer first resolves. Concretely, run `claude` once interactively in `W`, let
   it process a turn, and exit — the same "run `claude` once first" convention as
   [`deployment.md`](../../deployment.md) § Prerequisites and the realclaude
   [Max-plan operator setup](e2e-realclaude.md#max-plan-operator-setup-macos).
   Pre-creating the transcript is the fix for the **cold-start producer-subscribe
   race**: the producer captures its tail offset at the **first successful resolve**,
   so a resolve that lands before any transcript exists Warn-retries and a later
   retry can capture an EOF offset *past* the events the phone expects. (The daemon's
   own supervised claude also writes a transcript once it processes a turn, but a
   pre-existing one removes the startup retry window.) This is documented at length
   in [`codebase/642.md`](../codebase/642.md) "Lessons learned" — that is the SSOT;
   do not re-derive it.

3. **Seed the conversation registry.** Add a conversation entry to
   `~/.pyry/<instance-name>/conversations.json` whose `id` is the conversation the
   phone will drive and whose `cwd` is `W` (on-disk shape per
   `internal/e2e/relay_assistant_turn_test.go:51-57`). This satisfies the
   `ValidateConversation` gate in `supervisor.WriteUserTurn` (#312) so the phone's
   turn is accepted and the cursor stamps. The id maps the phone's turn to the
   supervised claude — it does **not** select a transcript (transcript selection is
   newest-wins and conversation-agnostic, per the resolver above).

4. **Start the daemon** with `-pyry-workdir=W` and `PYRY_MOBILE_V2=1` (plus the
   operator's normal relay flags). The producer then computes
   `claudeSessionsDir = ~/.claude/projects/encode(W)`, the gate
   `bridge != nil && claudeSessionsDir != ""` fires (`cmd/pyry/relay.go:339`), and
   the producer tails the resolved transcript.

## How the setup removes the retry loop — and how to confirm it

With `W` non-empty by construction (a real workdir holding a real claude
transcript), step 3 of the failure walk no longer fails: `resolveLatestSessionJSONL`
returns the newest `<uuid>.jsonl`, `WaitForSessionJSONL` succeeds, and the producer
opens its events stream (`sess.Events(...)`, `internal/turnbridge/producer.go:188-201`)
instead of looping.

Following the setup, an operator confirms resolution on the **live stack** by
watching the daemon logs for two observables:

- the `turnbridge: resolve session jsonl, retrying` warning **stops recurring**, and
- the producer **opens its events stream on the resolved transcript** — a real,
  non-scripted `<uuid>.jsonl` under `~/.claude/projects/encode(W)/`.

This is **operator-verified via this runbook on the live stack, not a CI gate** —
the emulator + real-claude + relay stack runs only on an operator machine. Record
the result on the ticket.

A note on reading the logs: a *single* transient `resolve session jsonl, retrying`
during startup, before the pre-created transcript is visible, is **benign** — the
producer resolves on the next retry and proceeds. The failure mode is the
**permanent** loop against an empty sessions dir (a throwaway `/tmp` with no real
claude). The setup makes the dir non-empty by construction, so the loop never
becomes permanent.

## Cross-references

- [`codebase/642.md`](../codebase/642.md) — **the SSOT for the seeding pattern.**
  The wire-level capstone: it seeds `conversations.json`, aligns the sessions dir,
  and proves `resolveLatestSessionJSONL` tails a real-claude-*format* transcript,
  and it documents the cold-start producer-subscribe race this runbook's step 2
  guards against.
- [`e2e-harness.md`](e2e-harness.md) and [`e2e-realclaude.md`](e2e-realclaude.md) —
  the automated Go suites this manual runbook is the sibling of (fakeclaude harness
  and the real-`claude` trust-boundary suite, respectively).
- `resolveClaudeSessionsDir` (`cmd/pyry/main.go:114-131`) and
  `resolveLatestSessionJSONL` (`cmd/pyry/interactive_turn_stream_v2.go:98-155`) —
  the two resolution functions; the live rung resolves a real transcript on the
  **same** machinery #642 aligns against, changing only the transcript's author.
- [ADR 025](../decisions/025-mobile-remote-head-interactive-session.md) § Phase 2
  and [`turnbridge-package.md`](turnbridge-package.md) — the v2 structured-streaming
  design this setup serves.

Automated coverage of real-transcript resolution is **deferred**: #642's
deterministic two-phone proof plus this runbook are the current coverage. A future
slice could lift #642's `PYRY_FAKE_CLAUDE_JSONL_TRIGGER` + sessions-dir-alignment +
pre-create recipe into a real-claude + relay live e2e, inheriting the same
harness-gap and operator-auth constraints noted in `codebase/642.md` and
`e2e-realclaude.md`.
