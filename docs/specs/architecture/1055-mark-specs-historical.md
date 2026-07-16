# Spec — #1055: mark the `docs/specs` write-once tree as historical + banner the nine superseded stream-json specs

**Size:** XS (docs-only; `size:s` label is a safe upper bound). 0 production source files. 1 new file (`docs/specs/architecture/README.md`), 10 additive edits (2 READMEs + 9 identical banner prepends). Edit fan-out: none — every edit is an independent, verification-free markdown prepend/append with no compile step and no interdependency.

**Security-sensitive:** no (label absent). Pure documentation annotation; no design surface, no untrusted-input path, no code change.

## Files to read first

Codegraph is not useful here — it parses code, not markdown — so the reading list is the doc files themselves plus the ticket.

- `docs/specs/README.md` (whole file, 8 lines) — existing content the AC1 statement **appends to**, not rewrites. Current text: "Per-ticket build artifacts. Created during the development pipeline." + the `architecture/` / `code-reviews/` subdir list.
- `docs/specs/architecture/392-agentrun-delete-pty-drive-code.md:1-6` — the sharpest hazard; its top line instructs deleting the PTY-drive code that is now the default runner. Confirms *why* the banner is needed and shows the current top-of-file shape (a `# Ticket #392 — …` H1) the banner must sit **above**.
- `docs/specs/architecture/337-agent-run-scaffold.md:1-8` — a spec whose first line is an `# …` H1 immediately followed by `## Files to read first`; the banner goes above that H1, so the file will no longer start with the H1 (valid markdown — a blockquote-first file is fine).
- The GitHub issue #1055 body — the **exact banner text** and the strictly-additive constraint (both reproduced below so the developer needn't leave the spec).

## Context

The 2026-07-15 docs review found the tree's biggest hazard is unmarked historical layers sitting beside maintained docs with equal apparent authority. Per-ticket specs under `docs/specs/` are write-once build artifacts — never current-state authority — but nothing in the tree says so, so an assistant or pipeline agent searching the docs surfaces a stale spec ("delete the current default path") with the same authority as shipped truth.

The nine stream-json-era specs are the worst offenders: they describe a runner (streamrunner / stream-json PTY-drive path) that was cut over to `ptyrunner` in **#470** (streamrunner kept only behind the `PYRY_USE_STREAMJSON` env-var fallback) and confirmed as the default path in **#473**. `392-agentrun-delete-pty-drive-code.md` is the sharpest hazard — it instructs deleting the PTY-drive code that is now the default runner.

This ticket is a labelling pass: annotate, do not rewrite. No spec body is edited to match current behaviour — the banners mark historical status only.

## Design

Three parts, all strictly additive. Nothing is deleted or rewritten.

### Part A — the pinned banner (nine files)

The banner text is supplied verbatim by the reviewer and must appear **byte-for-byte**, including the em-dash (U+2014, bytes `e2 80 94`), not an ASCII hyphen:

```
Superseded by #470/#473 — ptyrunner is the default runner.
```

(60 bytes: `… #470/#473␠<U+2014>␠ptyrunner …`. An editor that autocorrects the em-dash to `-` or `--` fails the AC — verify the codepoint after saving.)

**Contract for each of the nine files:** prepend a banner block — the banner line plus a single blank separator line — at the very top of the file, above the existing first line. Touch no other byte. The nine files (all under `docs/specs/architecture/`):

`332-agent-run-spawn-drive.md`, `337-agent-run-scaffold.md`, `353-jsonl-surface-all-kinds-raw-usage.md`, `354-agent-run-streamjson-emitter.md`, `373-realclaude-runpyryagentrun.md`, `375-agentrun-selfcheck-stream-json.md`, `390-streamrunner-primitive.md`, `391-agent-run-wire-streamrunner.md`, `392-agentrun-delete-pty-drive-code.md`.

**Markdown form is the developer's call** (blockquote, bold line, etc. — Technical Notes explicitly say only the text is pinned). Recommended form, visually distinct and grep-clean:

```
> **Superseded by #470/#473 — ptyrunner is the default runner.**
```

followed by one blank line, then the original H1 and the rest of the file unchanged. The wrapping `>` and `**` sit outside the pinned literal, so a `grep -F` on the exact 60-byte string still matches (see Testing). Use the **same** banner for all nine.

### Part B — `docs/specs/README.md` (AC1)

**Append** a short paragraph stating that the per-ticket specs are point-in-time pipeline artifacts, never current-state authority. Do not delete or reword the two existing lines. One or two sentences suffice; suggested substance (exact wording is the developer's): *the specs here are point-in-time build artifacts captured when each ticket was refined — they record what was designed then, not how the system behaves now, and are never current-state authority; some may be explicitly banner-stamped as superseded.*

### Part C — `docs/specs/architecture/README.md` (AC2, new file)

Create this file (it does not exist). It carries the **same** point-in-time / non-authority statement, scoped to architect-output specs: the files in this directory are architect specifications produced per ticket, point-in-time and never current-state authority. A pointer to `docs/knowledge/` as the maintained current-state source is a nice-to-have, not required. Because the file is new, the additive constraint is trivially met.

## Concurrency model

N/A — documentation edits, no runtime.

## Error handling / failure modes to avoid

- **Em-dash corruption.** The single easiest way to fail AC3. Confirm `e2 80 94` survives in each of the nine after saving (Testing gives the grep).
- **Non-additive drift.** Any reflow, trailing-whitespace normalization, or "while I'm here" edit to an existing line breaks AC4. Prepend only; leave everything below the banner byte-identical. For the two READMEs, append/create only — never rewrite existing lines.
- **Banner below the H1.** AC3 says *above the original content*. Placing it under the H1 fails the AC even though it may read better. Above the first line.

## Testing strategy

No `go test` — this is docs-only. Verification is deterministic and mechanical; the developer runs these and pastes the output into the PR body:

- **Banner present and exact (all nine):**
  `grep -Fl 'Superseded by #470/#473 — ptyrunner is the default runner.' docs/specs/architecture/{332-…,337-…,…,392-…}.md` must list all nine files (i.e. 9 matches). Use `grep -F` (fixed string) so the em-dash and `#`/`/` are matched literally.
- **Banner is at the top (all nine):** for each file, `head -1` (or the first non-empty line) is the banner line. The original H1 must still be present immediately below the blank separator.
- **Strictly additive (the crisp check):** `git diff --numstat -- docs/specs/architecture/<file>.md` for each of the nine must show **0 in the deleted column** (`<added>\t0\t<file>`). Zero deletions ⇒ no original line was touched. Same check for `docs/specs/README.md` (append ⇒ 0 deleted). The new `docs/specs/architecture/README.md` has no prior content, so additivity is trivially satisfied.
- **Byte-for-byte body preservation (spot-check the sharpest file):** `diff <(git show origin/main:docs/specs/architecture/392-agentrun-delete-pty-drive-code.md) <(tail -n +3 docs/specs/architecture/392-agentrun-delete-pty-drive-code.md)` (adjust `+3` to the number of prepended lines) should report no differences — the body below the banner equals `main`'s content exactly.

## Open questions

None blocking. Two developer-discretion points, both already resolved in this spec:
- Banner markdown wrapping (blockquote+bold recommended; only the text is pinned).
- README wording (substance pinned by AC1/AC2; phrasing is the developer's).

## Scope guard

Docs-only. The developer's worktree mutates exactly: the nine `docs/specs/architecture/*.md` spec files, `docs/specs/README.md`, the new `docs/specs/architecture/README.md`, and this spec file. **No code, no tests, no `docs/knowledge/` edits** — the knowledge-base note (if any) is documentation-phase's job, not a developer AC.
