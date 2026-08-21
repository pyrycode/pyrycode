# #1560 — system-overview's Restart Cycle / Backoff / Fast-crash self-heal sections

Docs-only. One file changes: `docs/knowledge/architecture/system-overview.md`, sections
**Restart Cycle**, **Backoff Strategy**, **Fast-crash self-heal (#1165)**.

No Go file is created or modified. No test is added. `make check` is unaffected and is
still the gate the PR must show green.

---

## Files to read first

Read in this order. Everything below was verified on this branch's tip on 2026-08-20; the
"what to extract" column is the reason to open it, not a summary you can substitute for it.

| Path | Symbol / section | What to extract |
| --- | --- | --- |
| `docs/knowledge/architecture/system-overview.md` | §§ *Restart Cycle*, *Backoff Strategy*, *Fast-crash self-heal (#1165)* | The three sections you are rewriting. Find them by heading text, not by line number. |
| `internal/sessions/runnerstate.go` | `RunnerConfig` doc comment | **The authoritative record of what #1348 moved vs. dropped.** Its two-bullet list (`SelfHeal`, `ValidateConversation`) and the sentence "Neither has a stream-path equivalent today" are the framing the Fast-crash section should adopt. |
| `internal/streamsup/runner.go` | `Runner.Run` (doc comment + loop body) | The real restart loop: derived `iterCtx`, parent-ctx shutdown detection, `drainRestart` skipping backoff, the three-way backoff `select`. Its doc comment ends "Mirrors supervisor.Run" — that is the *shape* claim, still true. |
| `internal/streamsup/runner.go` | `useCreateForm`, `buildArgs` | **The spawn-flag decision as it actually runs.** Read both doc comments in full before writing any sentence about `--session-id`/`--resume` — see § "The AC-1 trap" below. |
| `internal/streamsup/runner.go` | `Runner.Restart`, `Runner.RestartFresh`, `Runner.drainRestart` | Which names survived into streamsup and what each means now. `RestartFresh`'s doc comment names `new_session` rotation as its purpose — no self-heal anywhere in it. |
| `internal/streamsup/backoff.go` | `backoffTimer`, `newBackoffTimer`, `backoffTimer.next` | The ladder itself, plus the file header recording it as a verbatim copy of the deleted `internal/supervisor/backoff.go` and why. |
| `internal/streamsup/runner.go` | `defaultBackoffInitial`, `defaultBackoffMax`, `defaultBackoffReset`, and the `New` arm that applies them | The three numbers and the fact they land on `Config.BackoffInitial` / `BackoffMax` / `BackoffReset` when zero. Re-read the constants at build time — AC 4 requires it. |
| `internal/sessions/pool.go` | `Pool.RotateBootstrapForSelfHeal` | Confirm it is a live *primitive* with no non-test caller. This is the thing that is easy to misread as evidence self-heal survived. |
| `cmd/pyry/streamsup_runner.go` | `mapStreamsupConfig`, `streamClaudeSessionsDir` | Proof the by-id transcript probe is **armed on every production path** (`ClaudeSessionsDir` is derived from `WorkDir`, not left empty). This is what makes the AC-1 trap live rather than theoretical. |
| `docs/knowledge/features/streamsup-package.md` | § "Supervise loop (`Run`)", § "`buildArgs` — the id-flag inversion…", § "`useCreateForm`…" | The pointer target AC 1 offers, and the evergreen prose you should link to rather than re-derive inline. |

**Cite by symbol name, never by line number.** `make cite-guard` fails a `//`-comment
citation that resolves into a declaration, and the ticket body itself demonstrates why:
its `runner.go:57-59` / `:281-288` cites for the backoff defaults were already off by four
and by eighty lines respectively when this spec was written, days after filing. Markdown
prose is not gated by `cite-guard`, but the same rot applies — write `` `useCreateForm` ``,
not a file:line.

---

## Context

`docs/knowledge/architecture/system-overview.md` is the architecture authority: repo
`CLAUDE.md` tells every agent to trust it over the module list in `CLAUDE.md` itself. Three
consecutive sections of it describe the lifecycle of `internal/supervisor`, a package #1348
deleted. The Restart Cycle diagram roots itself in `supervisor.Run()` and `runOnce`; the
Fast-crash self-heal section describes a recovery mechanism as **live production wiring**
when nothing on the production path implements it.

The self-heal section is the consequential one. It ends:

> Production wiring sets `SelfHeal` only on the daemon's bootstrap `Config`, via
> `Pool.RotateBootstrapForSelfHeal`

Verified on this tip: `git grep -n "SelfHeal" -- '*.go' | grep -v _test` returns **four**
lines, all in `internal/sessions` — the `Pool.RotateBootstrapForSelfHeal` declaration and
three comment lines (two in its own doc comment, one in `RunnerConfig`'s). No closure is
installed anywhere. `git grep -in "fastcrash\|crashStreak" -- internal/streamsup/` returns
zero. `RotateBootstrapForSelfHeal` has no non-test caller — only `selfheal_test.go`.

So the doc promises a daemon that heals itself out of a deterministic crash-loop, and the
daemon retries forever instead. An agent reading this doc will assume a recovery path that
does not exist.

**No ADR is warranted.** This is a doc correcting itself to match a deletion (#1348) that
already has its own record; there is no new decision. The documentation phase should not
open one for this ticket.

---

## The AC-1 trap: the ticket's own spawn-flag derivation is stale

Read this before writing a single sentence about `--session-id` or `--resume`.

AC 1 says any surviving spawn-flag prose must be "re-derived from `internal/streamsup/buildArgs`
— first spawn `--session-id`, respawn `--resume`". **That parenthetical was true when #1499
was split and is not true now.** #1630 introduced `useCreateForm` and #1631 armed it on the
production path; both are merged on `main` as of this spec.

What actually decides the id flag today:

- `beginSpawn` calls `useCreateForm(sessionsDir, id, latchCreate)` and feeds *its* answer to
  `buildArgs`'s `create` parameter. The `firstRun`/`forceFirst` latch is no longer passed
  straight through.
- When `Config.ClaudeSessionsDir` is empty, `useCreateForm` returns `latchCreate` verbatim —
  the old first-spawn/respawn behaviour.
- When it is **set — every production path since #1631** — the probe decides *outright* and
  `latchCreate` is **ignored**, including on the first spawn and including after a
  `RestartFresh` rotation re-armed `forceFirst`. A confirmed by-id hit via
  `transcript.StatByID` is the only answer yielding `--resume`; every non-hit yields
  `--session-id`.
- `mapStreamsupConfig` sets `ClaudeSessionsDir: streamClaudeSessionsDir(cfg.WorkDir)`, so the
  probe is armed in production. Empty only where the sessions dir cannot be derived (empty or
  unresolvable workdir, unresolvable `$HOME`).

**Consequence for this ticket.** Writing AC 1's parenthetical verbatim into
`system-overview.md` would replace one stale claim with a newer one — exactly the failure this
ticket exists to fix.

**Recommended resolution: keep no spawn-flag prose in the Restart Cycle section at all.**
Point at `docs/knowledge/features/streamsup-package.md` § "`buildArgs` — the id-flag inversion
that keeps the on-disk session stable" and its `useCreateForm` subsection. This satisfies AC 1
in the strongest way available — a section that keeps zero spawn-flag behaviour trivially
keeps none that is wrongly derived — and it obeys the ticket's own Technical Note that content
which moved "should point at `docs/knowledge/features/streamsup-package.md` rather than being
re-documented inline here."

If a spawn-flag sentence is kept anyway, it must say the flag is chosen **per spawn by a by-id
transcript-existence probe**, not by a first-run latch. Do not write "first spawn `--session-id`,
respawn `--resume`" as an unqualified statement of current behaviour.

**Do not take the ticket's first optional Technical Note** (the `firstRun`-advances-only-on-
successful-`cmd.Start` detail). The gate still exists and still works, but on every production
path the probe overrides the latch it protects, so quoting it inline reintroduces the
latch-decides framing this section is trying to shed. The `firstRun` gate is already documented
where it belongs, in `streamsup-package.md` § "`firstRun` gate".

---

## Design

### Verified facts table

Every row was checked on this branch's tip. Cite these by symbol; re-verify the backoff
numbers at build time per AC 4.

| Claim | Status | Where it lives now |
| --- | --- | --- |
| The supervise loop runs each iteration on a derived `iterCtx` | **true** | `Runner.Run` |
| Shutdown detected from the **parent** ctx, not the child-exit error | **true** | `Runner.Run` |
| A deliberate restart skips backoff | **true** | `Runner.drainRestart`, consumed in `Runner.Run` |
| `runOnce` is the spawn-and-wait step | **false in streamsup** | `Runner.spawnAndWait`. `runOnce` survives only in `internal/sessions` / `internal/turnbridge`, unrelated. |
| `supervisor.Run()` / `Supervisor.Restart` own the loop | **false** | `Runner.Run` / `Runner.Restart` |
| Respawn uses `--continue` when `ResumeLast` is true | **false** | streamsup has no `--continue` path. `ResumeLast` is present on `sessions.RunnerConfig` but deliberately **not mapped** into streamsup. |
| `Config.ResolveSessionID` resolves the id per spawn | **false** | Same: present in `internal/sessions`, deliberately not mapped. streamsup reads its own `r.sessionID`. |
| Initial 500ms, doubles, caps at 30s, resets after 60s uptime | **true** | ladder in `backoffTimer.next`; numbers in `defaultBackoffInitial` / `defaultBackoffMax` / `defaultBackoffReset`, applied by `streamsup.New` onto the zero-valued `Config` fields |
| Ctx cancellation breaks the backoff wait | **true but incomplete** | `Runner.Run`'s `select` has a **third** exit: `<-r.restartCh` relaunches immediately when a restart arrives while the child is already down. |
| A `fastCrashes` streak triggers `Config.SelfHeal()` | **dead** | `fastCrashes`, `FastCrashWindow`, `FastCrashThreshold` return **zero hits repo-wide**. |
| Production wiring sets `SelfHeal` via `Pool.RotateBootstrapForSelfHeal` | **false** | The primitive exists and is exercised only by `selfheal_test.go`. No closure is installed. |
| `Runner.RestartFresh` rotates an id | **true, but not for self-heal** | Its production callers are the `new_session` rotation path in `cmd/pyry/main.go` and the `streamRunner` adapter — no crash-streak trigger anywhere. |

### § Restart Cycle

Re-root the section on `internal/streamsup`. Two acceptable shapes; **prefer (a)**.

**(a) Replace with a pointer.** A short paragraph saying the interactive supervise loop lives
in `internal/streamsup` — `Runner.Run` — and linking
`docs/knowledge/features/streamsup-package.md` § "Supervise loop (`Run`)" for the loop
pseudocode, the `beginSpawn` single-acquisition rule, and the spawn-flag decision. This is the
cheapest correct outcome and carries no re-derivation risk. Keep the section heading so
inbound references to "Restart Cycle" still land.

**(b) Rewrite inline.** If a diagram is kept, it must be rooted at `Runner.Run` and its
branches must be the four the loop actually has, in order:

```
Runner.Run  (internal/streamsup)
    │
    ├── spawnAndWait(iterCtx, args) ──> spawn claude on the held-open stdin seam,
    │                                    wait for child exit
    ├── parent ctx cancelled? ──> graceful shutdown, return ctx.Err()
    ├── deliberate restart pending (drainRestart)? ──> skip backoff, respawn now
    └── crashed? ──> apply backoff, then respawn
```

Prose that may be kept, **only** attributed to `internal/streamsup`: the derived-`iterCtx`
point, and the parent-ctx-not-child-error shutdown rule (a `Runner.Restart` kill cancels only
`iterCtx`, so the loop falls through and relaunches). Both are still exactly true — `Runner.Run`'s
own doc comment states them.

Prose that must **not** survive in any form: `supervisor.Run()`, `runOnce`, `Supervisor.Restart`,
`--continue`, `ResumeLast`, `ResolveSessionID`, and the whole trailing "respawn with a resolved
`--session-id` … or `--resume` … every spawn including the first" clause. Under shape (b) that
clause is dropped rather than rewritten — see § "The AC-1 trap".

### § Backoff Strategy

The **content is correct and stays**; only attribution is added. Verified this tip: the numbers
still match. The section today cites no file and no package at all, so this is an *addition*,
not a correction.

- Attribute the ladder (double → cap at max → reset when uptime exceeds the reset window) to
  `internal/streamsup/backoff.go` — symbol `backoffTimer`, whose file header records it as a
  verbatim copy of the deleted `internal/supervisor/backoff.go`, duplicated deliberately to
  keep streamsup free of a `internal/supervisor` import.
- Attribute the three defaults to `streamsup.Config` (`BackoffInitial` / `BackoffMax` /
  `BackoffReset`), applied by `streamsup.New` when the caller leaves them zero.
- **Re-verify the numbers before shipping** (AC 4 makes this conditional, not assumed):
  `git grep -n "defaultBackoff" -- internal/streamsup/`. Expected 500ms / 30s / 60s. If any
  has moved, write the current value.
- Recommended (cheap, verified, not AC-required): the current bullet "Context cancellation …
  breaks out of the backoff wait" is incomplete. The `select` in `Runner.Run` has three exits —
  the delay elapsing, `ctx.Done()`, and `restartCh`. A restart arriving while the child is
  already down cuts the wait short and relaunches immediately. Since the section is being
  touched anyway, add it.

### § Fast-crash self-heal (#1165)

The mechanism is gone with `internal/supervisor`, and the ticket's Technical Note is explicit:
"Content that is genuinely dead is better deleted than softened."

**Recommended: replace the whole section with a short record that the stream path has no
equivalent**, in the framing `internal/sessions/runnerstate.go`'s `RunnerConfig` doc comment
already uses — the fast-crash recovery is one of two dropped behaviours recorded there rather
than quietly lost, and re-adding it would be a feature on the stream runner, not a config field.
Keep the heading (it carries the #1165 reference that inbound links use) and state plainly that
a deterministic fast-crash loop is retried forever on the current path.

Whichever shape is chosen, all three of these must hold:

1. **No surviving sentence says production wiring sets `SelfHeal`** (AC 2). Not "used to set",
   not "sets it only on the bootstrap `Config`" — the claim in the present tense must be gone.
2. **Neither `Pool.RotateBootstrapForSelfHeal` nor `Runner.RestartFresh` is described as
   reachable from a live self-heal path** (AC 3). Both exist, both rotate an id, and that is
   precisely why each is easy to misread as evidence self-heal survived. If either is named at
   all, name it as an *uncalled primitive* (`RotateBootstrapForSelfHeal`) or as the
   `new_session` rotation seam (`RestartFresh`) — never as a crash-streak consequence.
3. `fastCrashes`, `FastCrashWindow`, `FastCrashThreshold` are dead repo-wide and must not
   appear as current behaviour.

**Link hygiene — read this before keeping the existing links.** The section currently links
`features/sessions-package.md` § `Pool.RotateBootstrapForSelfHeal`, ADR 033, and
`codebase/1165.md`. The ADR and the codebase note are historical records and are fine to keep
*framed as history*. The `sessions-package.md` link is different: **that document itself still
asserts the stale claim**, saying `supervisor.Run` calls the primitive via the `Config.SelfHeal`
closure. Do not link it as evidence of current behaviour. Either drop the link or frame it
explicitly as the #1165-era record. Fixing `sessions-package.md` is **out of scope** — it is a
`features/` doc the documentation phase owns, and this ticket's scope boundary is three sections
of `system-overview.md`. Note it in the PR body as sibling-ticket material for PO.

### Explicitly out of scope

Leave untouched: the module tree, § Interactive Session, **§ Key Types → `supervisor.Config`
(which also mentions `SelfHeal` — the ticket says leave it)**, `supervisor.Bridge`,
`supervisor.Supervisor`, and the Dependencies table. Sibling splits are #1558 and #1559. Do not
edit `docs/knowledge/INDEX.md` — the documentation phase is its sole writer.

---

## Concurrency model

Not applicable — no code changes, no goroutines, no shutdown sequence. The *described*
concurrency (one `Run` goroutine; `cmd.Wait` blocking it; os/exec's internal ctx-watcher firing
`cmd.Cancel` off-loop on either a parent-ctx or an `iterCtx` cancel) is already documented in
`streamsup-package.md` § "Supervise loop (`Run`)" and should be linked, not restated in the
architecture overview.

## Error handling

Not applicable — no failure modes introduced. The one recovery behaviour under discussion is
the absence of one: on the stream path a deterministic fast-crash loop retries forever under
backoff, with no self-heal escape. Saying that plainly is the deliverable.

---

## Verification strategy

There is no test to write. The verification is the scoped symbol sweep AC 5 mandates, and it
must go **in the PR body** with controls in both directions.

### Why a bare-name sweep is worthless here

Six of the nine symbols these sections cite still resolve *somewhere* in the repo — in unrelated
packages, or as survivors that now mean something different. A sweep asking "does this name
exist?" passes the current, wrong doc. Scope the sweep to the package the surrounding sentence
attributes the symbol to.

### Recipe

Run from the repo root, in `bash` (see hazards below):

```bash
for s in runOnce iterCtx drainRestart backoffTimer ResolveSessionID ResumeLast \
         fastCrashes FastCrashWindow FastCrashThreshold spawnAndWait buildArgs; do
  n=$(git grep -c "$s" -- internal/streamsup/ 2>/dev/null | awk -F: '{t+=$2} END{print t+0}')
  r=$(git grep -c "$s" -- '*.go'              2>/dev/null | awk -F: '{t+=$2} END{print t+0}')
  printf "%-20s streamsup=%-4s repo=%s\n" "$s" "$n" "$r"
done
```

Expected on this tip (re-run it; do not paste these numbers without running):

```
runOnce              streamsup=0    repo=3      <- POSITIVE control for "resolves repo-wide, absent in target"
iterCtx              streamsup=8    repo=8
drainRestart         streamsup=6    repo=6      <- POSITIVE control for "resolves in target"
backoffTimer         streamsup=7    repo=7
ResolveSessionID     streamsup=0    repo=7
ResumeLast           streamsup=0    repo=9
fastCrashes          streamsup=0    repo=0      <- NEGATIVE control: dead repo-wide
FastCrashWindow      streamsup=0    repo=0
FastCrashThreshold   streamsup=0    repo=0
spawnAndWait         streamsup=11   repo=15
buildArgs            streamsup=19   repo=46
```

**Both controls are load-bearing and both must appear in the PR body.** `drainRestart` at
`streamsup=6` proves the sweep can report presence; `runOnce` at `streamsup=0, repo=3` proves it
can report absence *while the bare-name check would have passed it*. A sweep that reports
presence unconditionally is as useless as one reporting absence unconditionally — and the
`runOnce` row is the single clearest demonstration in the set, because its two numbers disagree.

Plus the two self-heal-specific checks:

```bash
git grep -n "SelfHeal" -- '*.go' | grep -v _test        # expect 4 lines, all internal/sessions, all decl-or-comment
git grep -in "fastcrash\|crashStreak" -- internal/streamsup/   # expect 0
git grep -n "RotateBootstrapForSelfHeal" -- '*.go'      # expect: decl + doc comment + selfheal_test.go only
```

### Shell hazards that make a sweep silently lie

- **`git grep -E '\bfoo\b'` matches nothing.** POSIX ERE has no `\b`, and git reports the empty
  result as a clean exit — indistinguishable from real absence. Use `-F`, `-P`, or the Grep tool.
- **Under `zsh`, `git grep -- $FILES` does not word-split** an unquoted variable holding a
  path list; it passes one bogus pathspec, yielding 0 hits and exit 0. Run the loop above in
  `bash`, or quote each pathspec explicitly.
- `git grep -c` prints `path:count` per file, so a multi-file symbol needs the `awk` sum above;
  reading only the first line undercounts.

### Final check before pushing

Re-read the three rewritten sections and confirm every Go symbol named in them resolves **in
the package the surrounding sentence attributes it to**. That, not the sweep script, is what
AC 5 is actually asking for — the script is the evidence.

---

## Open questions

1. **Shape of the Restart Cycle section — pointer (a) or inline rewrite (b)?** The spec
   recommends (a). (b) is acceptable if the developer judges the overview loses too much
   standalone value, but (b) carries the AC-1 staleness risk and costs more turns. Developer's
   call; no rework if (b) is chosen and the § "AC-1 trap" constraints are honoured.
2. **`docs/knowledge/features/sessions-package.md` still asserts `supervisor.Run` calls
   `RotateBootstrapForSelfHeal` via `Config.SelfHeal`.** Same stale claim, different file, out
   of scope here. Note it in the PR body for PO to file as a sibling to #1558/#1559 — the
   `features/` docs are the documentation phase's to write.
3. **The backoff numbers are asserted true as of 2026-08-20.** AC 4 makes keeping them
   conditional on their still matching at build time; the developer re-runs the `defaultBackoff`
   grep rather than trusting this spec's table.
