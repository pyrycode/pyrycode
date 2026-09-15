# 2451 — pre-mark CLAUDE.md external includes approved on the bootstrap workdir entry

## Files read

- `internal/agentrun/trust/trust.go` → `markWorkdirTrustedIn` — the read-modify-write that owns the `projects[realpath]` entry; the one line `entry["hasTrustDialogAccepted"] = true` is where the two new keys go. Also its package doc-comment and `MarkWorkdirTrusted`'s doc, both of which state the written contract as a single key.
- `internal/agentrun/trust/trust_test.go` → `TestMarkWorkdirTrusted_IdempotentPreservesExtraEntryFields` — asserts byte-identity of `~/.claude.json` across a repeat call against a fixture entry that holds only `hasTrustDialogAccepted` + `mcpServers`. Adding keys makes the second write differ from the fixture, so this test goes red unless its fixture carries the new keys. This is the one existing test the change breaks.
- `internal/agentrun/trust/trust_test.go` → `TestMarkWorkdirTrusted_PreservesSiblingProjects`, `writeJSON`, `readJSON` — the fixture/assertion helpers and the sibling-entry pattern the new test mirrors.
- `internal/e2e/workdir_trust_test.go` → `TestE2E_Supervisor_PreMarksWorkdirTrusted` — decodes the entry into a struct carrying only `HasTrustDialogAccepted`. `encoding/json` ignores unknown keys, so this test is unaffected; checked because it is the other place that reads what this helper writes.
- `cmd/pyry/agent_run.go` → `trustMark` — the shared test seam. **Corrected 2026-09-15 rework: the `agent-run` verb does not call it.** The production callers are `runSupervisor` and the ACP lane in `cmd/pyry/main.go`, plus `selfcheck`. See § Revisions — the original claim here is what put the live fixture in the wrong shape.
- `internal/e2e/realclaude/fixtures.go` → `WithWorktreeAuthenticated`, `RunPyryAgentRun`, `resolveAndOpenJSONL` — the live-tier primitives: a temp `$HOME` seeded with the operator's `~/.claude.json` and re-pinned credentials, a real `pyry agent-run` spawn, and `tuidriver.SessionJSONLPath(home, workdir, sessionID)` for locating the child's transcript.
- `docs/knowledge/features/agentrun-trust-subpackage.md` § "Public API", § "Testing", § "Consumers" — states the written contract as the single trust key and enumerates the test matrix. Both go stale with this change; recorded under Documentation handoff, not edited here.

## Context

Claude Code expands an `@` import that resolves outside the session's working directory only when `hasClaudeMdExternalIncludesApproved` is true on the `~/.claude.json` entry of the folder that **owns the CLAUDE.md** — not the folder the child was spawned in. The ticket's first comment carries the measurement: with the workspace root entry holding `false`, a child spawned in the `default/` subfolder received the bare root CLAUDE.md with all eight `@` lines unexpanded, and marking the `default/` entry changed nothing in either folder. Setting the flag on the root entry expanded all eight. The miss is silent — Claude Code's debug log says nothing about skipped imports — and a headless child cannot answer the approval dialog, so nothing recovers it at runtime.

`MarkWorkdirTrusted` already owns exactly this entry and already runs on the bootstrap workdir from `runSupervisor`. Writing the two include keys alongside the trust key is a one-line-shaped change at the single point that already establishes this entry.

No ADR is warranted: this widens an existing helper's written contract within its stated purpose (pre-answer the gates a headless child cannot answer), it adds no boundary and no new consumer.

## Design

### Production change — `markWorkdirTrustedIn`

The entry-mutation step becomes three assignments instead of one:

```go
entry["hasTrustDialogAccepted"] = true
entry["hasClaudeMdExternalIncludesApproved"] = true
entry["hasClaudeMdExternalIncludesWarningShown"] = true
```

Unconditional assignment, matching the existing key's treatment — an existing `false` is overwritten, which is the production state the ticket measured. Everything else about the helper is untouched: same read-modify-write through `map[string]any` with `UseNumber`, same pass-through preservation of sibling projects and sibling keys on the target entry, same atomic tempfile-then-rename, same error taxonomy, same silence in the logs.

`hasClaudeMdExternalIncludesWarningShown` is set for the same reason the approval is: it is the other half of the dialog's state, and leaving it false invites Claude Code to re-raise a warning no headless child can answer.

The package doc-comment and `MarkWorkdirTrusted`'s doc-comment both state the written contract as the single trust key; both are widened to name all three, since that comment is the contract a reader checks against.

**Why no confinement change.** The `$HOME` bound that gates this helper's daemon consumer lives at the `runSupervisor` call site, not in the helper, because the helper is shared with an unconfined `agent-run` path. That split is unchanged here — this ticket widens what is written to an entry the caller already decided to trust, not which entries may be written.

### Live arm — `internal/e2e/realclaude/claude_md_external_includes_test.go`

AC-2 asks for a real child's injected instructions block, so the check belongs in the live tier. One top-level test, two subtest arms sharing a temp `$HOME` from `WithWorktreeAuthenticated`.

Each arm gets its own workspace root under that home, shaped like production: `<root>/CLAUDE.md` holding an `@./brain.md` import line, `<root>/brain.md` holding a unique sentinel, and an empty `<root>/default/` subfolder to spawn in. The import resolves to `<root>/brain.md`, which is outside the spawn cwd `<root>/default` — the condition the flag gates.

**The sentinel lives only in the imported file, never in CLAUDE.md.** That is what makes a raw-transcript containment check sound rather than a schema guess: an unexpanded import puts the literal `@./brain.md` line into the instructions block and leaves the sentinel nowhere, so the sentinel's presence is evidence of expansion and nothing else. The prompt is a single-word reply request, so the model has no reason to emit the sentinel itself.

- **Marked arm.** Calls `trust.MarkWorkdirTrusted(<root>)` — the unit under test — then spawns via `RunPyryAgentRun` with `Workdir: <root>/default`. Asserts the child's transcript contains the sentinel.
- **Control arm.** Hand-writes its root's entry with `hasTrustDialogAccepted: true` and `hasClaudeMdExternalIncludesApproved: false` — the exact pre-fix production state — merging into the seeded `~/.claude.json` rather than replacing it, so the operator's onboarding state survives. Spawns the same way. Asserts the transcript does **not** contain its sentinel.

The control arm is what makes the marked arm's pass mean something: without it a green marked arm is also consistent with imports expanding unconditionally.

**Corrected 2026-09-15 rework.** The sentence that stood here claimed the control arm also pins the ticket's second measurement, because `agent-run` marks the spawn folder's entry on its way in. It does not — the verb carries no `trustMark` call — and that false premise is what shaped the fixture wrongly. The arm does not cover the spawn-folder measurement at all; what governs the child is the entry keyed by its cwd's git root. See § Revisions.

Distinct sentinels per arm so cross-contamination between the two transcripts is visible rather than silently passing.

## Concurrency model

Unchanged. The helper spawns no goroutines and is sequential within an invocation; it holds no lock, by the existing deliberate choice. The live test's two arms run sequentially (non-parallel subtests — the shared `$HOME` comes from `t.Setenv`, which forbids parallel ancestors).

## Error handling

No new failure modes. The three assignments are infallible map writes on an entry the helper has already type-asserted to `map[string]any`; every error path (workdir missing, unparseable JSON, `projects` not an object, entry not an object, and each I/O step) is reached before or after this step and keeps its current wrapping and its current leave-the-file-untouched guarantee.

## Testing strategy

**Offline (AC-1), `internal/agentrun/trust`:**

- New `TestMarkWorkdirTrusted_SetsExternalIncludeFlagsOverExistingFalse` — fixture has the target entry with all three flags explicitly `false` plus an `mcpServers` sibling key, and a second project entry with all three `false`. After the call: the target carries all three flags true with `mcpServers` intact; the other project entry is untouched with all three still false. One test covers every clause of AC-1 — flags set, existing `false` overwritten, sibling keys preserved.
- `TestMarkWorkdirTrusted_IdempotentPreservesExtraEntryFields` — fixture entry gains the two new keys so the byte-identity assertion pins idempotency of the widened write. This is a fixture update, not a weakened assertion; the test keeps asserting exact byte equality.
- The remaining eleven tests are unchanged and must stay green — they pin the preservation, error, mode and realpath behaviour this change must not disturb.

**Live (AC-2):** the two-arm differential test above, run by the dispatcher's `make e2e-realclaude` gate. Not run in this session — the builder role does not obtain Claude credentials or run the live tier. The ticket gets `needs-real-claude` so the gate runs it.

**Builder gate:** `go test -race ./internal/agentrun/trust/...`, `go vet ./...`, `go build ./cmd/pyry`, plus `go vet -tags e2e_realclaude ./internal/e2e/realclaude/` so the live file's compilation is proven offline — `make check` never compiles that package, and a package that fails to build reports zero tests run through a green exit.

## Documentation handoff

Pending for the documentation stage — not edited in this ticket.

- `docs/knowledge/features/agentrun-trust-subpackage.md` § "Public API" — the quoted contract names only `hasTrustDialogAccepted`; widen to all three keys.
- Same file, § "Testing" — add the new test to the enumerated matrix and note the idempotency fixture now carries the include keys.
- Same file, § "Error handling" clause 1 — the fresh-file skeleton shown as `{"projects": {<key>: {"hasTrustDialogAccepted": true}}}` now carries three keys.
- Same file, opening paragraph and § "Consumers" — the helper's purpose widens from "skip the workspace-trust modal" to "pre-answer the startup gates a headless child cannot answer", of which the external-includes approval is the second; the `runSupervisor` consumer entry is the one that makes subfolder conversations inherit the root CLAUDE.md's imports.

## Open questions

1. Does `hasClaudeMdExternalIncludesWarningShown` need to be true for the approval to take effect, or is it independent bookkeeping? The ticket specifies both, and the manual stopgap set both, so both are written either way; resolving this changes nothing about the design. Recorded because the live arm cannot distinguish them.
2. Does the seeded `~/.claude.json` in the live tier carry operator-side state that interacts with the control arm's hand-written `false`? Expected no — the evidence is explicit that the flag is read per-entry, and both arms' roots are fresh temp paths with no pre-existing entry. Confirm when the live gate runs; if the control arm expands its import anyway, the per-entry premise is wrong and the finding is worth more than the test.

## Revisions

### 2026-09-15 — implementation

The design landed as planned; no finding forced a change to it. Recording the two Open Questions' state and one detail the plan did not name.

- **Open question 1 (does `WarningShown` gate the approval?) stays open, by design.** It is not answerable offline and it does not branch the implementation — the ticket specifies both keys and the manual stopgap set both, so both are written on either answer. The live gate cannot separate them either, since no arm writes one without the other. Deliberately left as bookkeeping rather than turned into a second control arm: an arm that isolated `WarningShown` would spend a live child to answer a question whose answer changes nothing here.
- **Open question 2 (seeded `~/.claude.json` interfering with the control arm) stays open pending the live gate**, which is where it was always going to be decided. The control arm's failure message is written to make that outcome legible: if it reddens, the per-entry premise is wrong and the marked arm proves nothing, which is the finding to report rather than a test to relax.
- **The control arm resolves its root through `agentrun.ResolveWorkdir` before hand-writing the entry.** The plan said "hand-writes its root's entry" without naming how the key is spelled. It must go through the same realpath rule the helper uses — macOS resolves `/var` to `/private/var` and folds to the on-disk case — or the control arm would write a key claude never reads, quietly degrading into a no-flag arm that passes for the wrong reason. This adds an `internal/agentrun` import to the live test file.
- **Verification added beyond the plan's gate:** the `e2e`-tagged fake-daemon supervisor trust tests, which are the other consumer of what this helper writes. Green — they decode the entry into a struct carrying only the trust key, so the two added keys pass through unread, as the reading list predicted.

### 2026-09-15 — rework after the live gate's marked arm went red

The live gate ran the suite (1415 executed, 1 failed) and reddened exactly one
test: the marked arm. The control arm passed. **The production change is
unaffected and stays as designed** — the defect was in the live fixture, and the
control arm's green was vacuous.

**The mechanism, read out of the claude 2.1.259 binary rather than inferred.**
The approval is a per-project key, and the project it belongs to is not the one
the plan assumed:

- Both include flags sit in claude's *project* config defaults alongside
  `hasTrustDialogAccepted`, so the ticket's premise that they live on a
  `projects[...]` entry is right.
- The gate on an import is "resolves outside the child's cwd", and it is lifted
  by `hasClaudeMdExternalIncludesApproved` read from the current project config.
- That config is keyed by **the canonical git root of the cwd, falling back to
  the cwd itself when no repository encloses it.**

That single rule reconciles both measurements. In production the workspace root
is a repository, so a child in `<root>/default` is governed by `<root>`'s entry —
which is why the operator saw marking the root work and marking `default` do
nothing, and why marking the bootstrap workdir is the correct fix. In the live
fixture the workspace was a bare `MkdirAll` tree with no repository, so the key
degraded to `<root>/default` — an entry the test never marks and that
`agent-run` does not mark either, since the verb carries no `trustMark` call.
Neither arm ever had an approved entry: the marked arm went red and the control
arm went green because *nothing* reached either child.

**Two changes, both in the live test.**

1. `makeGitRoot` makes each workspace root a real repository, restoring the
   production topology the ticket describes. Plain `git init`, not a linked
   worktree — claude skips a main-repo ancestor's CLAUDE.md only when cwd sits
   in a *linked* worktree, which a plain repo is not.
2. A second sentinel per arm, living only in CLAUDE.md and required present by
   **both** arms. This is the vacuity guard the first cut lacked: an arm
   asserting the imported sentinel is *absent* passes whenever nothing reached
   the child. With it, the broken fixture would have reported itself on the
   first live run instead of spending a second one.

**Both Open Questions are now closed.** Question 1: `WarningShown` does not gate
the approval — the include decision reads `hasClaudeMdExternalIncludesApproved`
alone, and `WarningShown` only suppresses the dialog. Writing both remains
correct, since a headless child cannot answer a warning either. Question 2: the
seeded `~/.claude.json` does not interact with the control arm — the lookup is a
single keyed entry, so the operator's unrelated entries cannot reach it.

The import spelling was checked while confirming the above and is fine: the
extractor accepts a `./`-prefixed target, resolves it against the CLAUDE.md's own
directory, and skips code spans, so `@./brain.md` in a paragraph is a real
import. Ruling it out mattered because `git init` alone would not have helped had
the syntax been wrong.

**Still not run in this session:** the live tier itself. The builder role does
not obtain Claude credentials, so AC-2 remains the dispatcher gate's to decide.
