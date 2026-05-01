# Architecture spec — #19 Document recovery for stale service PATH

**Ticket:** [#19](https://github.com/pyrycode/pyrycode/issues/19)
**Status:** Draft for development
**Size:** **XS** (single doc section, no code, no tests)

## Context

`pyry install-service` snapshots the user's `$PATH` at install time and bakes
it into the systemd `Environment="PATH=…"` line or the launchd
`EnvironmentVariables.PATH` entry. Service-manager processes do not inherit
the user's interactive shell environment, so any directory added to `$PATH`
*after* the unit is written (a new `nvm` Node version, a freshly-installed
`pyenv` Python, a new Linuxbrew package) is invisible to the supervised
`claude` and to claude's hook scripts. The failure mode is silent: the hook
or claude itself calls the missing binary, exits 127, and from the service
side nothing visible happens.

A workaround already exists today — re-run `pyry install-service --force --
<original claude flags>` and reload/restart — but it is not documented. This
ticket writes it down.

The ticket body lists three deliberately-deferred richer fixes (a `--refresh`
flag, a drop-in fragment + `pyry path update` subcommand, a `pyry config`
subcommand). All are out of scope here. The acceptance criteria require zero
code changes, zero behavior changes, and zero new CLI flags.

## Design

This is a documentation change in a single file: `docs/deployment.md`. No Go
code, no package additions, no interfaces, no concurrency, no tests beyond
reading the rendered Markdown.

### Where the new section lives

Add a new H2 section **between** the macOS half and the existing
"Common pitfalls" H2. Concretely, insert the new section immediately before
the line `## Common pitfalls` (currently line 232).

**Heading:** `## Updating PATH after installing new tools`

Rationale for placement:

- Single section, not split between Linux and macOS halves. The recovery
  procedure's *concept* is identical on both platforms (re-run
  `pyry install-service --force` with the original flags, then ask the
  service manager to reload). Splitting it would duplicate the trigger and
  cause explanation, and the two service-manager command pairs sit happily
  next to each other under one heading.
- Placing it after both platform sections lets the prose refer back to the
  install commands the reader has just seen, instead of forward-referencing.
- Placing it before "Common pitfalls" keeps the troubleshooting bullet
  cross-link short (the pitfall sits a few lines below the new section).

### Content of the new section

The section must satisfy every acceptance-criterion bullet. Write it as
prose-with-code-blocks, not a bullet list — same register as the rest of the
file. Suggested structure (the developer is free to tighten the wording, but
must hit each numbered beat):

1. **Trigger paragraph.** Open with the trigger and the symptom in one or
   two sentences:

   > After enabling pyry as a service, installing a new shimmed tool
   > (a fresh `nvm` Node version, a new `pyenv` Python, a new Linuxbrew
   > package, etc.) does not propagate into the running service. The
   > symptom is a silent `exit 127` from a claude hook or from claude
   > itself when it tries to invoke the new binary — nothing visible
   > surfaces from the pyry side.

2. **Cause paragraph.** State the underlying cause explicitly: `PATH` is
   captured at `install-service` time and baked into the unit/plist; the
   service does not inherit the user's interactive shell environment, so
   newly-installed shim directories are invisible until the unit is
   rewritten.

3. **systemd recovery block.** Copy-pasteable, in the order the user runs
   them:

   ```bash
   pyry install-service --force -- \
     --dangerously-skip-permissions \
     --channels plugin:discord@claude-plugins-official
   systemctl --user daemon-reload
   systemctl --user restart pyry
   ```

   Annotate that the flags after `--` must match the original
   `install-service` invocation (because `--force` re-renders `ExecStart=`
   wholesale; anything not on the new command line is dropped). The example
   flags above are illustrative — the section should make clear the user
   substitutes their own.

4. **launchd recovery block.** Same shape:

   ```bash
   pyry install-service --launchd --force -- \
     --dangerously-skip-permissions \
     --channels plugin:discord@claude-plugins-official
   launchctl unload ~/Library/LaunchAgents/dev.pyrycode.pyry.plist
   launchctl load   ~/Library/LaunchAgents/dev.pyrycode.pyry.plist
   ```

   Same annotation about reusing the original flags.

5. **Cost paragraph.** Explicitly call out the wart: the user has to
   remember (or shell-history-search) their original claude flags, because
   pyry does not maintain pyry-side state for them — claude flags live in
   the unit/plist and are re-rendered from scratch on every `--force`.
   One short sentence is enough; do not editorialise. Suggested phrasing:

   > Note that you have to supply the original claude flags again — pyry
   > does not remember them between runs. If you are not sure what you
   > used last time, search your shell history (`history | grep
   > install-service`) or read the existing `ExecStart=` /
   > `ProgramArguments` line out of the unit file before overwriting it.

6. **Optional verification line.** A single closing sentence pointing the
   user at how to confirm the fix landed:

   > `pyry status` should show `Phase: running` again; if a hook was
   > failing, re-trigger it and confirm it no longer exits 127.

The section should not exceed ~40 lines of rendered Markdown. No tables, no
sub-headings — flat prose with two fenced code blocks.

### Cross-link from the existing troubleshooting bullet

The "Channel hooks not firing under pyry." bullet at the end of the
"Common pitfalls" section (currently around line 246) ends with:

> …Add them to `Environment=` / `EnvironmentVariables`.

Append one short clause that points at the new section. Suggested edit:

> …Add them to `Environment=` / `EnvironmentVariables` — or, if you have
> just installed a new shimmed tool after enabling the service, see
> [Updating PATH after installing new tools](#updating-path-after-installing-new-tools)
> for the refresh procedure.

The anchor `#updating-path-after-installing-new-tools` is what GitHub-flavored
Markdown will generate from the H2 above (lowercase, hyphenated). The
developer should verify by previewing the rendered file.

### What not to change

- No edits to the Linux or macOS install sections themselves. The existing
  prose ("inherited from your current shell's `$PATH`") is correct and does
  not need a forward-reference; the new section is reachable from the
  pitfall and from the table of contents the renderer generates.
- No edits to `cmd/pyry`, `internal/`, or anywhere else in the repo.
- No new files. Specifically, do not add a `docs/knowledge/features/*.md`
  page for this — the recovery procedure is operational documentation, it
  belongs in `deployment.md` next to the install commands it pairs with.
- No changes to `docs/PROJECT-MEMORY.md` or `docs/lessons.md`. This is
  documenting an existing-but-unwritten workaround, not capturing a new
  lesson learned.

## Concurrency model

N/A — documentation only.

## Error handling

N/A — documentation only.

## Testing strategy

- Render `docs/deployment.md` (GitHub preview, or any Markdown viewer) and
  visually confirm:
  - The new H2 appears between the macOS section and "Common pitfalls".
  - Both fenced code blocks render correctly.
  - The cross-link from the hooks pitfall resolves to the new section
    (click it; it should jump).
- `qmd update && qmd embed` after the edit so the docs collection picks up
  the new section. (Per CLAUDE.md: `embed` alone does not detect content
  changes inside an existing file, but this is a content change to an
  already-indexed file, so `embed` will re-vectorise. Running `update` first
  is harmless and matches the documented convention.)
- No Go tests, no `go vet`, no `staticcheck` runs needed — this PR touches
  zero `.go` files.

## Open questions

None. Every acceptance-criterion bullet has a single obvious resolution
above. The developer should not need to make architectural judgement calls;
this spec is a content checklist.

## Out of scope (and why)

- **`pyry install-service --refresh`** (the ticket's "Option B"). Deferred
  pending evidence that re-typing the original claude flags is repeatedly
  painful for real users. This spec deliberately leaves that wart visible
  in the Cost paragraph so the pain is legible if it accumulates.
- **Drop-in fragment + `pyry path update` subcommand** (the ticket's
  "Option C"). Deferred — also breaks systemd/launchd parity since launchd
  has no drop-in equivalent.
- **`pyry config` subcommand to remember claude flags.** Explicitly out of
  scope per the ticket: pyry's design keeps state out of pyry, claude flags
  live in the unit file. Don't sneak this in via documentation either —
  e.g. don't suggest the user maintain a sidecar shell alias or env file
  for their original flags. Shell history is sufficient.
