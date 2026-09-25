# 039. A capability belongs to the agent and the model together

## Status

Accepted (agreed 2026-09-10, applied #2629)

## Context

Before Codex support, every session ran Claude, so a single closed
vocabulary — one model menu, one fixed effort-level set — could describe
what any session was allowed to run. #2627 gave the daemon a second,
independent source of published models: Codex's families, stored beside
Claude's retained menu rather than merged into it
([`modelVocabularyStore`](../features/streamsup-package-retaining-the-decoded-model-list-for-the-session.md#holding-codexs-model-families-beside-claudes-2627)),
specifically rejecting a per-entry `agent` tag in favor of the entry's
storage location being the only place its agent is recorded.

#2629 is where that separation first had to be read back out for a
decision, not just for storage. `set_session_settings`'s wire validation
checked a `model` value against Claude's retained menu unconditionally, and
an `effort` value against a fixed five-level enum unconditionally — both
written when there was only one agent to check against. Once a session's
agent could be Codex, both checks were wrong for it: a Codex family alias
was refused as not-offered against Claude's menu, and Codex's `ultra` was
refused by the fixed enum before any per-model check could even run,
regardless of whether the target model actually offered it.

## Decision

**A capability — which models exist, and what each one supports — is
scoped to the pair (agent, model), never to the model alone and never to a
single project-wide vocabulary.** A check against "what's offered" must
first resolve which agent the target session runs, then check only that
agent's own entries. Effort-level membership is checked per model within
that agent's list, not against a shared enum: an entry's own
`EffortLevels` is the authority for what that model accepts, and a model
with no entry for its agent falls back to a conservative set scoped to
that agent — Claude's fixed five, or, since #2666, Codex's own held-family
intersection — rather than being treated as offering everything or
nothing.

Concretely, in #2629: `(*sessions.Pool).HarnessFor(id)` resolves a
session's agent (live-then-dormant, canonical, construction-fixed) before
any vocabulary read runs. The adapter then selects that agent's own list —
Claude's retained menu or Codex's `CodexModels()` — and checks `model`
against it, and `effort` against the specific model-row's advertised
levels within that same list. When the target model has no entry at all,
`effortLevelsFor` falls back by agent, not to one shared set: Claude's
fixed `{low, medium, high, xhigh, max}`, or, since #2666, Codex's
`codexCommonEffortLevels` — the levels every *held* family advertises, in
the first family's order, none when no family is held. See
[`effortLevelsFor`](../features/v2-session-manager-state-machine-inbound-set-session-settings-settingsupd.md).

## Rationale

The alternative — one shared vocabulary, or a per-entry tag a caller
merges before checking — was already rejected once, at the storage layer
in #2627, for a durability reason (an older daemon must ignore an unknown
key rather than misread a field it doesn't understand). This decision
extends the same separation to the read side for a correctness reason: a
value valid for one agent is not evidence about another, so merging the
two lists before checking would either accept a Claude alias on a Codex
session (wrong) or refuse a Codex level a Claude session never needed
(also wrong, and the actual bug #2629 fixed). Keeping the check keyed on
(agent, model) is what lets each agent's vocabulary evolve independently —
Codex adding `ultra` needs no change to what a Claude session accepts, and
a future third agent needs no change to either.

The fallback set for an unmatched model was originally one shared, fixed
set for both agents, because a model with no entry has told the daemon
nothing about what *that model* supports — the fallback was meant as a
floor for "we don't know," not a per-agent default.

**#2666 supersedes that for Codex.** The shared five includes `max`, which
not every Codex family advertises; a Codex session on an unlisted or empty
model reported `max` in its capabilities and accepted it, and
`codexTurnOverrides` forwarded it unfiltered on the next `turn/start` —
the shared floor was conservative for Claude, the agent it was measured
against, but not for the second agent it was later applied to unchanged.
Codex's fallback is now `codexCommonEffortLevels`: the levels every
*held* family advertises, in the first family's order, none when no
family is held. This is still not a guess — it reads the vocabulary the
daemon already holds for the session's own agent, the same evidence
`effortLevelsFor`'s matched-entry path uses, intersected across families
instead of keyed to one model row. Claude keeps the original shared
fallback unchanged; nothing about this failure mode applies to it, since
Claude's own five is exactly what Claude's models advertise.

## Consequences

- Any future settings field whose valid values depend on what a model
  supports (not just the model's identity) must follow the same shape:
  resolve the session's agent first, then check within that agent's own
  entries — never a merged or Claude-only list, however small the second
  agent's vocabulary looks today.
- Resolving "which agent" is now a first-class, narrow pool read
  (`HarnessFor`), not folded into an existing settings read — see
  [`Pool.HarnessFor`](../features/sessions-package-key-types-pool-settingsfor.md#poolharnessfor-2629).
  #2589 was flagged at write time as needing the same read; a second
  caller confirms it belongs on the pool rather than inlined once more at
  the call site.
- A well-shaped value that is a real, published entry for the *other*
  agent is refused identically to one that names nothing at all — the
  wire error does not distinguish "not offered by any agent" from "offered,
  but by an agent this session doesn't run." Distinguishing them would leak
  which agent a session runs to a client that guessed wrong, which is not
  information the settings-write path needs to disclose.
- A truncated `effort_levels` (or `value`) row is inconclusive, not proof
  of absence, and falls through to the safer answer (fallback set, or
  `model_list.unavailable`) rather than refusing — the same principle
  #2281 already established for `model`, now stated as applying to any
  per-model capability read, not just that one field.

## Related

- [features/streamsup-package-retaining-the-decoded-model-list-for-the-session.md](../features/streamsup-package-retaining-the-decoded-model-list-for-the-session.md) — the #2627 storage-layer separation this decision extends to the read side.
- [features/v2-session-manager-state-machine-inbound-set-session-settings-settingsupd.md](../features/v2-session-manager-state-machine-inbound-set-session-settings-settingsupd.md) — the #2629 application: `validateEffortVocabulary`, `HarnessFor`, the fallback set.
- [features/sessions-package-key-types-pool-settingsfor.md](../features/sessions-package-key-types-pool-settingsfor.md#poolharnessfor-2629) — `Pool.HarnessFor`.
- `docs/protocol-mobile.md` § `set_session_settings` — the wire-facing statement of this rule for `model` and `effort`.
