# #1226 — Probe: what does pyry do with the output file a backgrounded Bash command writes?

**Size:** XS. **Deliverable:** one GitHub comment on #1226, plus a follow-up issue only if a
consumer is found. **Zero production code, zero test code, zero file changes in the worktree.**

---

## Files to read first

Read these before searching. The first two are the keystone for AC1 — they are what turns
"outside the session workdir" from a one-sample coincidence into a structural claim.

| Path | What to extract |
|---|---|
| [#1223 comment 5109558101](https://github.com/pyrycode/pyrycode/issues/1223#issuecomment-5109558101) | **Primary source.** The verbatim `tool_result` envelope (output path, `cwd`, `sessionId`, `backgroundTaskId`), the workdir listing, the session-JSONL path, and the "workdir == HOME under `WithWorktreeAuthenticated`" statement. Every AC1 value comes from here. Do not re-derive. |
| `internal/sessions/reconcile.go:14-33` | `encodeWorkdir` — replaces **every** non-alphanumeric with `-`, empirically verified against claude. This is the AC1 keystone; see § AC1. |
| `internal/sessions/reconcile.go:49-62` | `DefaultClaudeSessionsDir` — `EvalSymlinks` **before** encode. Confirms the encoder's input is the *resolved* cwd (`/private/var/...`, not `/var/...`). |
| `internal/transcript/transcript.go:97-124` | `GuardProbedPath` + its doc comment. AC3's mandated known-positive control. Note the "confidentiality boundary" framing and that it is stat-free. |
| `internal/transcript/transcript.go:55-63` | `sessionFileStem` — the `.jsonl`-suffix + `ValidStem` clause. The **second, independent** reason the guard rejects a `tasks/<id>.output` path. |
| `internal/transcript/transcript.go:145-175` | `Newest` — `os.ReadDir` over a directory claude populates, with name validation. An AC2/AC3 near-miss worth recording explicitly. |
| `internal/transcript/transcript.go:190-215` | `Probed` — the one untrusted→trusted path crossing pyry declares. AC3. |
| `internal/debugbundle/bundle.go:128-150` | `newestRecording` — AC2's mandated known-positive control. Globs `*.cast`. |
| `internal/debugbundle/bundle.go:53-64` | `DefaultRecordingsDir` — `$HOME`-rooted (`~/.local/share/pyry-recordings`). Confirms the *previous* ticket body's "walks the session workdir" claim was wrong. |
| `internal/agentrun/ptyrunner/runner.go:685-710` | `pruneOldRecordings` — globs then `os.Remove`s by age. **Use as the known-positive control for AC4's pyry-removal search** (see § AC4). |
| `internal/agentrun/selfcheck/selfcheck.go:68-77` | The `SentinelPath` discipline: "MUST remain a path this package constructed — never file contents or captured claude output". The stance AC3 is testing for violations of. |
| `internal/agentrun/trust/trust.go:11-13` | "MUST NOT log file contents at any layer." Constrains what the AC5 comment may quote. |
| `internal/agentrun/workdir.go:1-30` | `ResolveWorkdir` — a canonicaliser keyed into `~/.claude.json`. Enumerates nothing, owns no directory. Confirms the body's third bullet. |
| `cmd/pyry/interactive_turn_stream_v2.go:178-200` | `resolveLatestSessionJSONL` — newest-by-mtime over a dir **pyry computed**, explicitly not a second cwd-encoder. An AC3 near-miss: record why it is not a hit. |
| `internal/sessions/rotation/probe_linux.go:20-35` | `/proc/<pid>/fd` enumeration — paths the OS reports *about* claude. An AC3 candidate class distinct from "claude's own bytes". |

---

## Context

When claude's Bash timeout expires it moves the command to the background and writes its output
to a file, reporting the path in the `tool_result`. That file holds arbitrary command output. Two
questions follow: does any pyry component consume it (a trust question), and does anything remove
it (an accumulation question).

#1223 already measured the location, which reshapes both halves:

```
/private/tmp/claude-501/<encoded-cwd>/<session-id>/tasks/<backgroundTaskId>.output
```

The accumulation half is mostly answered — the file is not in the session workdir. The trust half
survives, but the search has to move: nothing will be found by looking for workdir walkers.

**This is a probe. The output is evidence plus either a follow-up ticket or an explicit
no-impact note.** Per the project's evidence-based-fix rule, do not propose or write a
defensive change for a consumer that does not exist.

**Ignore the "#1227" attribution in #1223's comment.** It is an off-by-one; the output-file
question is this ticket. #1227 is the interactive-idle-reporting probe. Do not chase it.

---

## Design

There is no code to design. The design is the *method*: one derivation argument and three
searches, each with a known-positive control that must fire.

The governing discipline, applied uniformly below: **a search that returns nothing at all is a
broken search, not a negative finding.** Every negative in this record is earned by showing a
control hit in the same result set.

### AC1 — the derivation argument

Decompose the measured path into four components and settle each against #1223's recorded values.
The AC asks which of `$HOME`, the session workdir, or `$TMPDIR` the **base directory** derives
from. The honest answer is not a flat "none of them" — it is per-component:

| Component | Derived from | Evidence |
|---|---|---|
| `/private/tmp/claude-501` | **None of the three.** A hardcoded `/tmp` plus a uid key. | See the ruling-out argument below. |
| `<encoded-cwd>` | **The session workdir** — its symlink-resolved form. | Byte-exact match to pyry's own encoder; see below. |
| `<session-id>` | The session UUID | Equals `sessionId` in the same envelope and the JSONL filename in #1223's record. |
| `tasks/<id>.output` | `toolUseResult.backgroundTaskId` | Same envelope. |

**Ruling out all three candidates.** #1223's run is a maximally discriminating sample: `$HOME`,
the session workdir, and `$TMPDIR` all lived in **one** subtree, and the file landed in a
**different** one.

- `$HOME` == the session workdir (#1223 states this explicitly: "`workdir == HOME` under
  `WithWorktreeAuthenticated`") == `/private/var/folders/k0/…/001`.
- `$TMPDIR` == `/var/folders/k0/gc07w9ws319b07n0plnw6y8r0000gn/T/` — evidenced by the `probe-hold`
  path in the recorded `tool_use` input, which is a `t.TempDir()` path, and `t.TempDir()` roots at
  `os.TempDir()` == `$TMPDIR`.
- The output file is under `/private/tmp/…`, which is under none of them — nor under the
  `/private`-resolved form of `$TMPDIR`.

**State the sample's limit rather than overclaiming.** Because the three candidates coincide in
this run, the sample rules them out *as the base* but cannot distinguish *among* them had the file
landed inside. It did not, so the claim stands — but say so, so the next reader knows what the
sample can and cannot carry.

`501` is almost certainly the uid. This is checkable offline: run `id -u` on the machine that
produced #1223's run and state whether it matches. If it does, record the base as `/tmp/claude-<uid>`
and note that `/private/tmp` is macOS's symlink-resolved spelling of `/tmp`.

**The keystone — pyry already reproduces the middle component byte-for-byte.** `encodeWorkdir`
(`internal/sessions/reconcile.go:28`) maps every non-alphanumeric to `-`. Applied to #1223's
recorded resolved cwd it yields exactly the observed slug — verified during this spec's authoring,
including the `_` → `-` mapping that a `/`-and-`.`-only encoder would have missed:

```
encodeWorkdir("/private/var/folders/k0/gc07w9ws319b07n0plnw6y8r0000gn/T/TestRealClaude_BackgroundHandleTriggerdefault-onlyrep12436024847/001")
  == "-private-var-folders-k0-gc07w9ws319b07n0plnw6y8r0000gn-T-TestRealClaude-BackgroundHandleTriggerdefault-onlyrep12436024847-001"
  == the observed tasks/ slug            (byte-identical)
```

Re-verify this yourself rather than transcribing it — it is one regex application over two strings
already recorded in #1223. Then record the consequence, because it is what sets the cost of the
*next* decision:

> pyry does not merely lack this path — it is **two facts short of it**: the `/tmp/claude-<uid>`
> base and the `tasks/<id>.output` leaf. It already owns a byte-exact encoder for the hard middle
> component. "pyry does not know this file exists" is therefore a claim about what pyry *does*,
> not about what it would cost pyry to do. A follow-up that wanted to read the file would need no
> new path machinery.

### AC2 — who enumerates or reads *that location*

Sweep four axes over `internal/` and `cmd/`. Record the exact expressions used in the comment.

1. **The base, literally:** `os.TempDir`, `TMPDIR`, `/tmp/claude`, `claude-` adjacent to a uid or
   `Getuid`.
2. **The leaf vocabulary:** `tasks/`, `.output`, `backgroundTaskId`, `background`.
3. **Enumeration verbs:** `os.ReadDir`, `filepath.Glob`, `filepath.Walk`, `filepath.WalkDir`,
   `Readdirnames`, `Readdir(`.
4. **Read verbs pointed at a claude-owned subtree:** `os.ReadFile`, `os.Open`.

**Control: `debugbundle.newestRecording` (`internal/debugbundle/bundle.go:135`) must appear in
axis 3's results.** If it does not, the search is broken — fix the expression and re-run before
recording anything.

A starting inventory of production-code enumerators, from this spec's own sweep. **Verify each
cite; do not transcribe.** Line numbers drift, and a transcribed cite that has moved reads as a
search that was never run:

- `internal/debugbundle/bundle.go:135` — globs `*.cast` under `$HOME`-rooted recordings dir (the control)
- `internal/agentrun/ptyrunner/runner.go:693` — globs `*.cast` in the recordings dir
- `internal/transcript/transcript.go:152` — `os.ReadDir` over the claude sessions dir
- `internal/agentrun/workdir.go:63` — `os.ReadDir` during path canonicalisation
- `internal/sessions/rotation/probe_linux.go:24` — `os.ReadDir` over `/proc/<pid>/fd`
- `cmd/substrate-guard/main.go:86` — `filepath.WalkDir(".")`

Whether that inventory is complete is this AC's job, not this spec's.

**Report production and test hits separately; the verdict rests on production code.** A test that
touches the location is worth one line in the record but does not make pyry a consumer.

### AC3 — who derives a filesystem path from claude-produced bytes

Three distinguishable source classes. Keep them distinct in the record — collapsing them is how a
real hit gets filed as a near-miss:

- **claude's own bytes** — session-JSONL fields, `tool_result` `content`, claude stdout/stream-json.
  Search the field names that carry paths: `cwd`, `session_id`/`sessionId`, `file_path`, `filePath`,
  `path`, and any `filepath.Join` whose argument came from a decoded claude message.
- **filenames in a directory claude writes** — `transcript.Newest` takes a base name from claude's
  sessions dir and validates it via `sessionFileStem`. Record it as a hit-with-a-guard, not a miss.
- **the OS reporting on claude** — `/proc/<pid>/fd` (`probe_linux.go`), which feeds
  `transcript.Probed`. Distinct from claude authoring the bytes.

**Control: `transcript.GuardProbedPath` (`internal/transcript/transcript.go:111`) must appear.**

The AC then requires stating whether that guard would accept or reject a `tasks/<id>.output` path.
**Cite both rejecting clauses, not one** — a single-clause answer is fragile, and the guard's own
doc comment frames the pair as defence in depth:

1. `filepath.Dir(resolved) != canonicalDir` (`transcript.go:116`) — the resolved parent is
   `…/tasks`, never the sessions dir.
2. `sessionFileStem` (`transcript.go:55`) — `bjh1j6tle.output` has no `.jsonl` suffix, so it fails
   before `ValidStem` is even reached.

Note also that `EvalSymlinks` falls back to `filepath.Clean` on error, so the rejection holds
whether or not the file exists on the machine running the check. The guard is stat-free.

### AC4 — what removes the file, in three arms

Label each arm `observed`, `documented`, or `undetermined`. Not-observed does not license
"nothing removes it".

**pyry — decidable by code search.** Search removal verbs (`os.Remove`, `os.RemoveAll`,
`os.Truncate`, rename-away) across `internal/` and `cmd/`, then intersect with AC2's location
axes. **Control: `pruneOldRecordings` (`internal/agentrun/ptyrunner/runner.go:685-710`) must appear
— it is the codebase's clearest glob-then-`os.Remove` housekeeping loop.** With the control firing,
a negative here is `observed` (the code is the artifact and the sweep was complete over the stated
axes) — say which axes, so the scope of the negative is legible.

**OS — cite policy for `/tmp`, not for `$TMPDIR`.** These are different directories with different
policies, and AC1 establishes the file is in the former.

> ⚠️ **The most likely place this probe overturns its own premise.** The ticket body asserts the
> file lands "in a per-uid `/tmp` base … that the OS sweeps". On macOS, the periodic cleaner
> `/etc/periodic/daily/110.clean-tmps` is gated by `daily_clean_tmps_enable`, which historically
> **defaults to `NO`** in `/etc/defaults/periodic.conf`. If that holds on the machine, `/tmp` is
> *not* periodically swept and the honest arm is closer to "cleared at boot only" — which weakens
> the body's accumulation conclusion. **Read `/etc/defaults/periodic.conf` and quote the actual
> line; do not assert either way from memory.** If the body's premise is wrong, say so plainly —
> that is a finding, not a failure.

For Linux, cite `systemd-tmpfiles` and `/usr/lib/tmpfiles.d/tmp.conf` (and note that many distros
mount `/tmp` as tmpfs, making it boot-cleared regardless). If no Linux host is available, label the
Linux sub-arm `documented`, cite `tmpfiles.d(5)` / `systemd-tmpfiles-clean.timer`, and state
explicitly that it was not observed on a live host.

**claude — `undetermined-offline`.** Name what a live run would have to observe:

- whether the `.output` file disappears at task completion, at session exit, or neither;
- whether the whole `<session-id>/` directory is removed or only the file;
- what happens on abnormal exit (a `SIGKILL`ed claude cannot clean up, so that path leaves it).

Name the discriminator too: an OS sweep is days-scale or boot-scale, so a disappearance observed
within the same session is attributable to claude, not to the OS. Without that, a live run would
buy an ambiguous observation.

### AC5 — the record

One comment on #1226, structured as the four arms above plus a verdict. It must contain the search
expressions verbatim (AC2/AC3 both require it) and the control hits that make each negative earned.

Decision rule for the follow-up: **file one only if a component actually consumes the file.** If
none does, the comment says so explicitly and names what was searched, so the next person does not
re-ask. A "no consumer" outcome is a complete, successful result for this ticket — not a reason to
go looking harder or to propose a speculative guard.

---

## Concurrency model

None. No goroutines, no processes, no code.

## Error handling

No runtime errors to handle. The analogous discipline is the earned-negative rule: an empty result
set is a defect in the search expression, not evidence. Each of AC2, AC3, and AC4's pyry arm names
a control that must fire; if a control does not appear, fix the expression and re-run before
recording. Report a search you could not make fire as exactly that — do not convert it into a
negative finding.

## Testing strategy

**No tests. No test files. No live claude run.**

The verification mechanism is the known-positive controls, which is why each search has one. That
is the whole of it.

This constraint is the ticket's stated reason for its XS size, and it is the one that will decide
whether this ticket costs 10 turns or 70. Every sibling in this family (#1223, #1224, #1225, #1230,
#1233) shipped 1400–2100 additions because each built an e2e rig. This ticket carries no
`needs-real-claude` label precisely because it must not. Concretely:

- Do not create or modify any `*.go` file, test or production.
- Do not extend `internal/e2e/realclaude/background_trigger_probe_test.go`.
- Do not write `docs/knowledge/codebase/1226.md` — the documentation phase owns it.
- **The worktree should end with zero changed files.** The deliverable is a GitHub comment.

If you conclude an AC genuinely cannot be settled without a live run, **route back via
`needs-rework:po` with the reason. Do not add a test.** The ticket body makes this a scope
decision, not a developer call.

## Security review

Ticket carries `security-sensitive`. Pass run; verdict **PASS**. See § Security review pass below.

## Open questions

1. **Is `claude-501` uid-keyed?** Settleable offline via `id -u`. If it does not match, the base
   derivation is something else and AC1's claim needs re-wording.
2. **Directory mode of `/tmp/claude-<uid>`.** `/tmp` is world-writable and sticky, so the mode of
   the per-uid directory decides whether another local user can read backgrounded command output.
   **This is outside this ticket's ACs.** If the mode is visible for free while confirming the OS
   arm, record one line. Do not run a permission experiment, do not add an AC, and do not expand
   the probe — file it as a follow-up observation if it looks wrong.
3. **Does the encoder match hold across claude versions?** #1223 is one sample on 2.1.220. The
   match is strong evidence that claude uses one cwd-encoding rule for both `~/.claude/projects/`
   and the tmp tasks tree, but a single observation cannot prove they are the same code path.
   Record it as "identical on the measured sample", not as "the same encoder".

---

## Security review pass

*The referenced `security-review.md` file was not present in the repo or the agent directory at
authoring time; the pass below was run from the general adversarial framing rather than that
file's checklist. Flagged so a reader knows which checklist was applied.*

**Trust boundaries.** This spec introduces no code and therefore no new boundary. It *examines*
one: the untrusted→trusted crossing pyry already declares at `transcript.GuardProbedPath`
(`internal/transcript/transcript.go:111`), and the stance at
`internal/agentrun/selfcheck/selfcheck.go:68-77` that `SentinelPath` must remain a
package-constructed path. AC3 is precisely a test of whether those stances hold everywhere. The
spec strengthens rather than weakens the check by requiring **both** of the guard's rejecting
clauses to be cited, so the record survives a later relaxation of either one.

**Does the probe itself create exposure?** The one real risk is what the AC5 comment publishes.
Constraints, all carried into the spec body:

- The comment must not paste claude's environment. #1223 deliberately redacted its `ps -E` read to
  three variable names because claude's environment carries the OAuth token. Any local inspection
  the developer runs is subject to the same rule.
- `internal/agentrun/trust/trust.go:11-13` forbids logging file contents; the same applies to a
  public issue comment. Quote paths and errnos, never `.output` file contents or `~/.claude.json`
  fields.
- #1223's recorded paths are tmpdir-based and carry no username. A path illustrated from the
  developer's own machine would carry one (`/Users/<name>/…`). Prefer the recorded values.

**Does the spec widen anything?** No. It prescribes read-only searches (`rg`, `Read`) plus at most
`id -u` and reading `/etc/defaults/periodic.conf`. No process spawning, no `ps` over the full
table, no environment reads — the capability that made siblings #1230/#1233 security-sensitive is
not needed here and is not granted. That is deliberate: this ticket needs no environment read, so
the spec does not authorise one.

**Adversarial check on the finding itself.** The failure mode with real security cost is a
*false* "no consumer" verdict — it would close a trust question that is actually open, and the
next person would not re-ask (AC5 says so in as many words). The three known-positive controls
exist to make that specific failure detectable, and the spec states that an unfireable control
must be reported as such rather than converted into a negative. The AC1 "two facts short" framing
is the second guard: it stops the record from reading as "pyry structurally cannot reach this
file", which would be a stronger claim than the evidence supports.

**Noted and deferred:** the `/tmp/claude-<uid>` directory mode (Open Question 2). Real, adjacent,
and outside these ACs. Recorded so it is not lost, fenced so it does not grow the ticket.

**Verdict: PASS.** No finding requires revising the design.
