# Spec: docs/e2e-realclaude — document Max-Mac CLAUDE_CODE_OAUTH_TOKEN extraction workflow (#492)

## Files to read first

- `docs/knowledge/features/e2e-realclaude.md` (entire file, ~115 lines) — the doc being extended. Note specifically:
  - **L30-32** — `WithWorktreeAuthenticated`'s implementation paragraph already contains the verbatim `security find-generic-password -s 'Claude Code-credentials' -w | jq -r '.claudeAiOauth.accessToken'` extraction recipe and the `HOME`-pinning rationale. The new operator-facing section MUST be consistent with this prose (same shell command, same env var name, same negative controls). The implementation paragraph stays as-is.
  - **L105 / L114-115** — `Related` ticket index entries for #409, #487, #491. The new section's tail-end "see also" can reference these without rewriting them.
  - **L52-66** (Test infrastructure / Make target) — context for *where* the new section slots. Existing top-level structure is: lead → "Why a sibling" → "Build tag" → "What's there today" → "Test infrastructure" → "Make target" → "CI cadence" → "Verifying tag exclusion" → "Related". The operator-auth section is the natural insertion point right after **Build tag**, before **What's there today**, so an operator hitting the doc sees auth setup BEFORE the implementation deep-dive.
- `docs/knowledge/INDEX.md` — confirms the existing one-line entry for `e2e-realclaude.md`; the AC explicitly says this file is NOT modified by the developer. Only consult it to be sure the existing summary line still makes sense after the new section lands (it does — the file is still about the realclaude suite; one operator-auth section doesn't shift the doc's headline scope).
- Ticket [#489](https://github.com/pyrycode/pyrycode/issues/489) body (Root cause + Fix shape Part A) — the recon source. The Keychain layout (`Claude Code-credentials` service, account = macOS user, 1025-byte JSON blob), the four negative controls (copying `~/.claude.json` alone → `apiKeySource:"none"`; copying full `~/.claude/` → same; HOME-pinning non-negotiable; Keychain is not file-backed in any portable sense), and the empirical verification one-liner are all in #489. **Do not paraphrase** — quote the shell commands verbatim so the documented workflow is the same one that was empirically verified on 2026-05-20.
- Ticket [#406](https://github.com/pyrycode/pyrycode/issues/406) — the CI-secret-injection bug the new section's "out of scope" sub-section must cross-link. Do NOT summarise or duplicate #406's contents; one bullet referencing the issue number is sufficient.

No codegraph queries are productive for a doc-only ticket — there is no Go symbol surface this change interacts with. `mcp__codegraph__codegraph_search WithWorktreeAuthenticated` would surface the existing fixture, but the fixture is already covered by L30-32 of the existing doc, which is the source of truth this spec defers to.

## Context

`make e2e-realclaude` is structurally unrunnable on a Max-only operator Mac without the right env var: claude on macOS stores Max OAuth credentials in Keychain (service `Claude Code-credentials`), and the binary accepts the extracted access token via `CLAUDE_CODE_OAUTH_TOKEN` (the bare-token sibling of `ANTHROPIC_API_KEY`). This was reconstructed on 2026-05-20 during recon for #489 and empirically verified end-to-end. Sibling #490 widened the `WithWorktreeAuthenticated` fixture to recognise the env var; #491 flipped 5 more test files onto that fixture so the 9 previously-no-auth tests now skip-or-pass under Max-only auth. Both have landed (CLOSED on `main`).

What's missing is operator-facing prose: the only place the Keychain recipe lives today is inside `WithWorktreeAuthenticated`'s implementation paragraph (L30-32 of the doc) and in the body of the now-closed #489 issue. An operator landing on `docs/knowledge/features/e2e-realclaude.md` reading top-down will not see the workflow until they're already several screens into a fixture-implementation paragraph. The next operator to set up a fresh Max-only Mac would otherwise have to redo the same Keychain reconnaissance.

This ticket adds a focused, copy-pasteable operator-auth section that names both auth paths (`ANTHROPIC_API_KEY` and `CLAUDE_CODE_OAUTH_TOKEN`) side-by-side, captures the extraction recipe with the Keychain-confirmation caveat, documents the token-rotation trap with two concrete patterns, names the negative controls so they aren't re-recon'd, and explicitly fences what is NOT in scope (in-suite refresh, CI keychain, default agent-run path) with a pointer to #406 for the CI bug.

## Design

### Where the new section lives

Insert a new H2 section titled **`## Operator authentication`** between the existing **`## Build tag`** section (ends ~L23) and **`## What's there today`** (starts ~L25). Rationale:

- An operator opening the doc reads top-down: "Why a sibling" → "Build tag" → ... — operator auth setup belongs in that prefix, BEFORE the implementation deep-dive.
- It does not disrupt the existing flow ("Build tag" → "What's there today" → ...) because the new section is operator-facing prose, naturally distinct from the fixture-implementation paragraphs that follow.
- The implementation paragraph at L30-32 stays as-is — it documents fixture internals (skip message, `t.Setenv` semantics, `HOME` re-pinning rules), which is a developer concern, not an operator concern. Operator section is the front door; fixture paragraph is the back door for someone reading code.

### Section structure

The new section has the following sub-structure (H3s under the new H2). Each bullet below is the content sketch for one H3; the developer writes the prose in the project's existing markdown idiom (lower-case verbs in headings where the existing doc uses them, fenced code blocks with the same language tags, etc.).

**`### Two auth paths`** — single short paragraph naming both env vars and the consumer:

- `ANTHROPIC_API_KEY` — Anthropic API-key path (the "console" path: API users with billing on `console.anthropic.com`).
- `CLAUDE_CODE_OAUTH_TOKEN` — Max-plan OAuth path (the "subscription" path: paid `claude.ai` Max plan, no API key issued by default).
- The fixture (`WithWorktreeAuthenticated`) treats either as sufficient; tests skip only when **both** are unset. Cross-reference to L30-32 for the fixture's exact behaviour.

**`### Max-plan operator setup (macOS)`** — the meat of the section. Contains:

- **Where the credential lives**: Keychain service `Claude Code-credentials`, account = macOS user; the entry is a 1025-byte JSON blob whose `claudeAiOauth.accessToken` is the bearer token the suite needs.
- **The extraction one-liner**, as a fenced shell block, verbatim from the existing L32 paragraph and the #489 body:
  ```bash
  security find-generic-password -s 'Claude Code-credentials' -w | jq -r '.claudeAiOauth.accessToken'
  ```
- **First-invocation caveat** as a callout (a short paragraph, not a fenced code block): the first run of `security find-generic-password` against this keychain item triggers a macOS Keychain confirmation dialog ("`security` wants to use your confidential information stored in `Claude Code-credentials` in your keychain"). Click *Always Allow* to suppress on subsequent runs in the same shell session; click *Allow* if you prefer to be prompted each time.

**`### Two patterns for sourcing the token`** — title is illustrative; the developer can pick prose that fits the doc's voice. This sub-section captures the rotation trap and shows BOTH patterns the AC mandates:

- One-paragraph rotation note: claude refreshes the access token via `refreshToken` automatically during interactive use, so an `accessToken` extracted once and bound to a shell env var will eventually become stale (typical TTL: hours, not days; not pinned here because Anthropic owns the value). Operators have two patterns; pick by trade-off.
- **Pattern A — re-source on demand** (fenced bash block, `~/.zshenv`-style, copy-pasteable):
  ```bash
  # ~/.zshenv (or ~/.zprofile, ~/.bashrc)
  export CLAUDE_CODE_OAUTH_TOKEN="$(security find-generic-password -s 'Claude Code-credentials' -w \
    | jq -r '.claudeAiOauth.accessToken')"
  ```
  Trade-off named in one sentence: token is captured at shell startup; new shells get the current value; long-lived shells go stale. Operator runs `exec zsh -l` (or opens a fresh terminal) to refresh.
- **Pattern B — just-in-time shell function** (fenced bash block, copy-pasteable):
  ```bash
  # ~/.zshrc — call as `claude-token` or use as $(claude-token) in commands
  claude-token() {
    security find-generic-password -s 'Claude Code-credentials' -w \
      | jq -r '.claudeAiOauth.accessToken'
  }

  # Invoke per-command:
  CLAUDE_CODE_OAUTH_TOKEN="$(claude-token)" make e2e-realclaude
  ```
  Trade-off named in one sentence: every invocation reads current Keychain state (so refresh is always picked up), but every invocation also pays the Keychain access cost (~tens of ms) and re-triggers the prompt if the operator clicked *Allow* rather than *Always Allow*.
- **Naming and target env var**: both patterns export to `CLAUDE_CODE_OAUTH_TOKEN`. Do NOT rename. The fixture (`WithWorktreeAuthenticated`) and the upstream `claude` binary both spell it this way; aliasing it to a shorter name and re-exporting just doubles the surface area to keep in sync.

**`### Negative controls — what does NOT work`** — three bullets, mirroring the #489 recon's negative controls. The point of writing them down is so the next operator doesn't re-run them:

- Copying `~/.claude.json` into a fresh `$HOME` does NOT authenticate. The subprocess reports `apiKeySource:"none"` and emits "Not logged in".
- Copying the full `~/.claude/` directory (often >1 GB) into a fresh `$HOME` ALSO does NOT authenticate. The directory does not contain the OAuth credential — Keychain is the only source.
- Lifting the suite's `HOME`-pinning (so the subprocess inherits the operator's real `~/.claude/`) is NOT a workaround. The suite pins `$HOME` for per-test JSONL namespace isolation under `~/.claude/projects/<encoded-cwd>/<sid>.jsonl`; lifting it re-introduces a cross-test JSONL race. The env-var path is the only correct fix that keeps the isolation. (One-line cross-reference to L32's "Why `HOME` stays pinned" paragraph — do not duplicate that paragraph here.)

**`### Out of scope for this section`** — three bullets, each naming a known follow-up explicitly so an operator doesn't expect to find it here:

- **In-suite token refresh**: the fixture does NOT re-read Keychain mid-run. A token extracted at process start that expires mid-run will surface as a subprocess auth failure. Deferred until observed; not on the roadmap.
- **CI keychain access**: GitHub Actions runners have no macOS Keychain. The Max-OAuth path is operator-machine only. See #406 for the related CI-secret-injection bug; the realclaude suite intentionally has no CI workflow (see the "CI cadence" section below in this same doc).
- **Defaulting `pyry agent-run` to OAuth**: not needed on the operator Mac. The dispatcher runs in the operator's real shell where Keychain works normally — the operator's pre-existing claude installation handles auth without pyry's help. The env-var path is specifically for the realclaude test suite, not for production agent runs.

### Cross-references

The new section does NOT introduce new cross-references beyond what is already in the doc. Specifically:

- Do NOT add new entries to the `## Related` section at the bottom of the file. The ticket numbers it references (#489, #406) are not deliverable links; #489 is the parent issue whose recon this section preserves, #406 is a stale cross-link inside the "Out of scope" bullet only.
- Do NOT add a new entry to `docs/knowledge/INDEX.md`. The existing one-line summary still describes the file accurately (it is the realclaude suite's feature doc; one operator-auth section does not shift its headline scope).

### What does not change

- L1-23 (lead, "Why a sibling", "Build tag") — unchanged.
- L25-50 ("What's there today" — fixture implementation paragraphs, including `WithWorktreeAuthenticated` at L30-32) — unchanged.
- L52-115 (everything from "Test infrastructure" onward, including the "Related" link section) — unchanged.
- All Markdown link targets, ticket numbers, and code-fence languages already in the file — unchanged.

The new section is purely additive between L23 and L25.

## Concurrency model

N/A. Doc-only change.

## Error handling

N/A. Doc-only change.

## Testing strategy

No automated tests. Verification is operator-driven:

- **Rendering**: open the file in the project's preferred Markdown previewer (or read the raw `.md`). Visually verify the new H2/H3 hierarchy renders cleanly, code fences close, no broken inline backticks.
- **Recipe**: an operator with a real Max plan on a fresh Mac runs the Pattern A or Pattern B block out of the doc and verifies that `make e2e-realclaude` runs at least one previously-skipped test (e.g. `TestRealClaude_PromptFidelity`) to completion. This is the same check that closed #489.
- **No code touches anything**: `git diff --stat` should report exactly one changed file (`docs/knowledge/features/e2e-realclaude.md`); no `*.go`, no `*.md` outside that file. The developer self-checks this before commit.
- **QMD re-embed**: after the doc lands, the developer runs `qmd update && qmd embed` per `CLAUDE.md`'s "Always `qmd update && qmd embed` after: adding or modifying docs" rule. `qmd embed` alone does not detect new sections, but this is a section ADD not a file ADD, so `update` is the relevant step.

## Open questions

None. The AC + the existing doc + the #489 recon fully determine the section's contents. Three points where the developer has minor copy-editing freedom (not blocking):

1. **Heading wording**. The spec uses `## Operator authentication` / `### Two auth paths` / `### Max-plan operator setup (macOS)` / `### Two patterns for sourcing the token` / `### Negative controls — what does NOT work` / `### Out of scope for this section`. Synonyms are fine if they fit the doc's existing voice better; the structure (one H2, six H3s, in this order) is the contract.
2. **Pattern A's target file**. `~/.zshenv` is the AC's example. `~/.zprofile` or `~/.bashrc` work equivalently — the developer may swap if zsh isn't the default on the relevant operator setup. The fenced block should name whichever target it picks.
3. **`exec zsh -l` vs. "open a fresh terminal"**. Pattern A's "how to refresh" sentence can name either; both work. Use whichever feels more natural.
