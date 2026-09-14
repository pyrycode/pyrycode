# #2423 — resolve a conversation's transcript folder from its own working directory

## Files read

- `cmd/pyry/snapshot_usage.go` → `snapshotUsageFor`, `bootstrapSnapshotUsage` — the
  reader whose fixed `dir` is the defect, and the build-time nil rule AC 5 protects.
- `cmd/pyry/session_model_window_lookup.go` → `sessionModelWindows` — the resolver
  shape this ticket mirrors: `Pool.Lookup` plus a type assertion off `Session.Runner`,
  logger-free, single refusal spelling.
- `cmd/pyry/streamsup_runner.go` → `mapStreamsupConfig`, `streamClaudeSessionsDir`,
  `newStreamRunnerFactory`, `streamRunner` — where the per-runner folder is already
  computed (`streamsup.Config.ClaudeSessionsDir`) and where the adapter can hand it back.
- `cmd/pyry/relay.go` → `relayWiring`, `startRelayV2`, `runConfigFor` — the two wiring
  points and the field table the new seam joins.
- `cmd/pyry/main.go` → `resolveClaudeSessionsDir`, `resolveSpawnDir`, the
  `startRelayV2` composition literal — the only place holding both the pool and the
  daemon's own sessions directory.
- `internal/sessions/pool.go` → `Pool.buildSession` — proves the runner (and therefore
  its folder) exists from mint time, before any child spawns, and that `spawnDir`
  overrides `tpl.WorkDir`.
- `internal/sessions/reconcile.go` → `DefaultClaudeSessionsDir`, and
  `internal/agentrun/workdir.go` → `ResolveWorkdir`, `canonicalCase` — the two
  derivations that differ, which is what AC 2's case/symlink clause is about.
- `internal/e2e/relay_v2_stream_model_window_test.go` →
  `TestRelayV2_StreamSessionSettingsReportsTheObservedWindow` — the daemon-level
  template: plant a transcript at the path the daemon resolves by session id, read
  `session_settings`.
- `internal/e2e/registry_read_helpers_test.go` → `claudeSessionsDir`, `encodeWorkdir` —
  the harness-side derivation the new arm extends to a non-home workdir.
- `docs/knowledge/features/contextwindow-package.md` — the topic that owns the reading;
  records that `Usage` carries no model and no path out, which this change leans on.

## Context

`snapshotUsageFor` is handed one folder for the whole daemon —
`relayWiring.claudeSessionsDir`, derived once at start-up from the daemon's own
working directory. A conversation created with a `cwd` spawns claude somewhere else
(`Pool.buildSession` prefers `spawnDir` over `tpl.WorkDir`), so claude writes that
session's transcript under the projects folder named for *that* path. The stat misses,
the reader collapses to the fresh-session report, and `session_settings` says 0 used
tokens for a conversation that has burned 43% of its window.

The primitive is right; the wiring is wrong. The fix hands `snapshotUsageFor` a folder
*resolver* keyed by session id, and the resolver reaches each session's own runner for
the value that runner already computed for its spawn probe.

No ADR is warranted: this is the third instance of an established pattern
(`sessionModelWindows`, `resolveBoundModelList`), not a new boundary.

## Design

### The resolver — `cmd/pyry/session_transcript_dir.go` (new)

```go
func sessionTranscriptDir(pool *sessions.Pool, daemonSessionsDir string) func(sessionID string) string
```

`sessionModelWindows` with one substitution: `Pool.Lookup(SessionID(id))`, then a type
assertion off `Session.Runner()` for `interface{ ClaudeSessionsDir() string }`, then the
method. `""` is the only refusal — an unknown id, a runner without the method, and a
runner that could not derive a folder all answer it, and `snapshotUsageFor` reads `""`
as "do not stat".

`daemonSessionsDir` is a **gate, never a value**: when it is `""` the builder returns
`nil` before any closure exists, which is what keeps AC 5's unwired shape. It is read
and not used because the two are one question — both bottom out in
`sessions.DefaultClaudeSessionsDir`, which needs `$HOME`, so a daemon that cannot name
its own projects root cannot name a session's either. Deciding it at build time, on the
resolver's *presence* rather than on what it answers, is the same structural rule
`bootstrapSnapshotUsage` and `runConfigFor` already state.

No logger, matching `sessionModelWindows`: the only thing a diagnostic here could carry
is a session id or a filesystem path.

### The runner's answer — `streamRunner`

`mapStreamsupConfig` already sets `streamsup.Config.ClaudeSessionsDir` from
`streamClaudeSessionsDir(cfg.WorkDir)`. `newStreamRunnerFactory` stores *that same
string* on the adapter and a new `ClaudeSessionsDir() string` method hands it back —
the EIGHTH concrete method off the un-widened `sessions.Runner`, off for
`ModelWindows`' stated reason (the consumer sits in `cmd/pyry`).

This is AC 2 discharged by construction: one derivation, at one call, with the reader
and the spawn probe reading one field of one struct. A second
`streamClaudeSessionsDir(workdir)` call from the resolver would agree today and is the
shape that can drift — `ResolveWorkdir` applies `canonicalCase` and symlink resolution,
so re-deriving from a differently-spelled workdir names a folder claude never writes.

### The reader — `snapshotUsageFor`

```go
func snapshotUsageFor(dirFor func(sessionID string) string, windows func(sessionID string) map[string]int) func(id string) (usedTokens, windowTokens int)
```

`nil` iff `dirFor == nil`. Per call: `dir := dirFor(id)`; only a non-empty `dir` reaches
`transcript.StatByID`, so the empty-dir guard the old signature enforced once at build
time is enforced per answer — an empty folder must never let a relative `<id>.jsonl`
join against the process directory. Everything downstream (`windows`, the
`contextwindow.Read` join, the discard-the-error arm) is untouched.

`fixedTranscriptDir(dir string) func(string) string` is the adapter for callers that
genuinely have one folder: `nil` for `""`, a constant answerer otherwise. It is what
keeps `bootstrapSnapshotUsage(dir, …)`'s signature and behaviour byte-identical (AC 3)
and what makes the existing reader tests one-token edits.

### Wiring

- `relayWiring` gains `sessionTranscriptDir func(sessionID string) string`; nil in
  foreground/v1 and on a daemon with no sessions directory, which is the
  either-half-unwired shape, not `modelWindows`' degrade-one-integer shape.
- `startRelayV2`: `runConfigFor(w.runSettings, snapshotUsageFor(w.sessionTranscriptDir, w.modelWindows))`.
- `startRelayV2`: `bootstrapSnapshotUsage(w.claudeSessionsDir, …)` unchanged — the
  bootstrap runner's working directory *is* the trusted workdir, and AC 3 pins its
  reading. The bootstrap seam is therefore the one place where reader and probe may
  still name different folders (a symlinked `-pyry-workdir`); AC 3 chooses that over
  changing a shipped reading, and `screen_snapshot` is the only consumer.
- `main.go`: `sessionTranscriptDir: sessionTranscriptDir(pool, claudeSessionsDir),`
  beside `modelWindows: sessionModelWindows(pool)`. The composition root is the only
  place holding both the pool and the daemon's directory; `startRelayV2` holds no pool.

## Concurrency model

No new goroutine, no new lock, one new field written once before its runner is published
and read-only thereafter. The publication edge, the lock ordering and the benign
`Lookup` → `ClaudeSessionsDir` TOCTOU are argued under **[Concurrency]** in the security
review below rather than twice.

## Error handling

| Failure | Answer |
|---|---|
| Daemon has no sessions directory | Builder returns `nil` → both usage seams nil → handlers report zeros (AC 5) |
| Session id the pool does not hold | `""` → no stat → `contextwindow.Read("", nil)` fresh report (AC 4) |
| Runner without the method (bootstrap fakes, non-stream doubles) | `""` → same |
| Runner that could not derive a folder (`ResolveWorkdir` failed, no `$HOME`) | `""` → same |
| Transcript genuinely absent under a resolved folder | `StatByID` errors, path stays `""` → same |

Nothing logs and no path reaches any surface on any of these, which is load-bearing
rather than incidental here: the folder now derives from a directory a paired client
chose (`resolveSpawnDir`'s confined, symlink-resolved output).

## Testing strategy

- `cmd/pyry/session_transcript_dir_test.go` — over a real `*sessions.Pool` with a
  folder-answering runner factory, mirroring `newModelWindowsTestPool`: the session's
  own folder; **isolation**, two pool sessions built in two directories each answering
  their own; the four refusals above; and `sessionTranscriptDir(pool, "")` returning a
  nil resolver without touching the pool.
- `cmd/pyry/snapshot_usage_test.go` — the eight existing call sites become
  `fixedTranscriptDir(dir)`; new rows for a nil resolver (nil seam), a resolver
  answering `""` (fresh report, and no file of that name in the process directory is
  read), and **one reader answering two ids out of two different folders** — the unit
  half of AC 1.
- `cmd/pyry/streamsup_runner_test.go` — a factory-built runner's `ClaudeSessionsDir()`
  equals `streamClaudeSessionsDir(WorkDir)`, i.e. `mapStreamsupConfig`'s own value, for
  a workdir that is not the process directory. This is the assertion that would redden
  if the adapter ever derived its own.
- `internal/e2e/relay_v2_stream_workspace_usage_test.go` — the daemon-level arm of AC 1,
  built on `TestRelayV2_StreamSessionSettingsReportsTheObservedWindow`'s shape: a real
  daemon whose workdir is `$HOME`, two `create_conversation` frames carrying two
  distinct `cwd`s under `$HOME`, a transcript planted under **each workspace's** own
  projects folder with a distinct used count, and `session_settings` for each
  conversation asserted to report its own count — not zero (today's reading, which is
  the sole red) and not the other's. No turn is driven: the used count comes off the
  planted file and the window is the default, so the join `#2107` needed is not in
  scope here.

## Open questions

- Whether the existing `TestRelayV2_StreamSessionSettingsReportsTheObservedWindow`
  keeps passing once its (cwd-less) conversation resolves through the runner's
  `canonicalCase`d folder rather than the daemon's `filepath.Abs`ed one. Expected yes
  on the harness's all-lowercase temp home; resolve by running it.

## Documentation handoff

Pending for the documentation stage — not done here, per the builder's file
restrictions:

- `docs/knowledge/features/contextwindow-package.md` (the topic that owns the
  context-window reading): record that the transcript folder is resolved **per
  session** from that session's own working directory, and that a conversation spawned
  in a workspace therefore reports a real `used_tokens` where it previously reported 0.
- `docs/protocol-mobile.md`, the `session_settings` field table, rows `used_tokens` and
  `window_tokens`: they currently leave the figure's scope unstated while telling a
  client that `0` used against a non-zero window is a genuine fresh session. That claim
  becomes true for workspace conversations with this change; the rows should say which
  directory the figure is resolved against.

## Security review

**Verdict:** PASS

The change's whole security content is that a **filesystem path a paired device can
influence** now decides which folder the daemon stats a transcript in. Everything below
is measured against that one sentence.

**Findings:**

- **[Trust boundaries] No findings — the boundary is `resolveSpawnDir`, unchanged, and
  the new code never holds a caller-supplied string.** The resolver takes a *session
  id* and returns a path the daemon composed. The id comes only from
  `resolveBoundRunSettings`, which refuses a conversation whose `CurrentSessionID` is
  empty, so `runConfigFor` consults the reader exclusively with an id the daemon's own
  registry produced; `bootstrapSnapshotUsage` supplies the pool's own bootstrap id. The
  *directory* half crossed the boundary long before this ticket:
  `create_conversation`'s `cwd` is expanded, confined to `$HOME` (symlinks resolved),
  and trust-marked by `resolveSpawnDir` before `Pool.CreateIn` ever sees it, and
  `streamClaudeSessionsDir` re-resolves it through `ResolveWorkdir`. What is new is only
  that `cmd/pyry` reads back a value it already computed and already handed to
  `streamsup`.

- **[File operations] No findings — the join is structurally non-traversable, and two
  independent validators say so.** `sessions.encodeWorkdir` replaces both `/` and `.`
  with `-`, so the encoded workdir is a single path component that can contain neither
  a separator nor `..`, whatever the requested `cwd` spelled; and `transcript.StatByID`
  rejects any id failing `ValidStem` **before** `filepath.Join`, so the stem half cannot
  traverse either. The resolver depends on the first of those: do not "simplify"
  `encodeWorkdir` to a `/`-only replacement. Empty-folder handling is the one guard this
  ticket moves — from build time to per answer — and it is load-bearing rather than
  tidy: an empty `dir` must not reach `StatByID`, which would otherwise join a
  *relative* `<id>.jsonl` against the daemon's process directory. The read is the only
  filesystem operation; this change creates, writes, and removes nothing, so file modes
  and atomic-write questions do not arise.

- **[File operations / symlinks] SHOULD FIX — none at the code level; recorded as the
  containment argument the verifier should check the implementation still honours.**
  The daemon follows whatever `~/.claude/projects/<encoded>/<id>.jsonl` resolves to.
  A hostile paired device cannot plant that symlink through any verb in this path
  (`create_conversation` creates a directory, not a link), and even granting it, the
  *only* thing that leaves `contextwindow.Read` is two integers about a file whose name
  must equal a UUID the daemon itself just minted. No content, no path, no existence
  signal for any pre-existing file. `O_NOFOLLOW` would buy nothing against that
  disclosure and would break the legitimate case, since `$HOME` itself is a symlink on
  macOS. The obligation on Phase B is negative: do not add a path, a folder, or an id
  to any error, log line, or wire field.

- **[Subprocess] No findings — the value is not new capability and must not become
  argv.** `ClaudeSessionsDir` has crossed into `streamsup.Config` since #1631, where it
  drives a by-id *existence probe*, not a flag; `mapStreamsupConfig`'s doc states that
  no resolver crosses and neither side scans. The new method is a pure read of that same
  field. It must never be used to build a claude argument: a directory derived from a
  device-chosen `cwd` reaching `exec.Command` would be a different design with a
  different review.

- **[Errors, logs, telemetry] No findings, and the posture is enforced by having no
  place to put a diagnostic.** `sessionTranscriptDir` takes no logger and returns no
  error; `Pool.Lookup`'s error is discarded rather than wrapped, for
  `resolveBoundRunSettings`' stated reason — returning it bare is what keeps a hostile
  or malformed id out of a log line or a wire frame. `snapshotUsageFor` keeps discarding
  `contextwindow.Read`'s error, which wraps the full path. MUST NOT grow a logger: the
  only fields a "why did this session read nothing" line could carry are a session id
  and a filesystem path, which is exactly the channel #833's posture closes.

- **[Concurrency] No findings.** One new field, written once inside
  `newStreamRunnerFactory`'s single-goroutine window before the runner is published to
  the pool, read-only afterwards — the same publication edge `scfg.Stdout`,
  `OnChildExit` and `PostureGate` already rely on. No new lock; the pool's lock and the
  field read are sequential and never nested, so the daemon's lock order is unchanged.
  The `Lookup` → `ClaudeSessionsDir` TOCTOU is benign: a `/clear` rotation inside the
  window keeps the session's working directory, so either reading names the same folder.

- **[Network & I/O] OUT OF SCOPE — no new limit, and the cost change is the ticket's
  point.** A workspace conversation's `request_session_settings` used to *miss* the stat
  and read nothing; it will now open and scan that session's transcript (487KB, 124 rows
  in the measured case) per request. That is the same cost the daemon's own conversation
  has paid since #857, it is bounded by `internal/agentrun/jsonl`'s 16 MiB per-line cap
  and one read per request, and the request arrives only over an established Noise
  session from an already-paired device. No amplification has been observed; a cache or
  a rate limit here would be a defence for a failure mode with no evidence behind it,
  and belongs to a future ticket if one appears.

- **[Tokens, secrets, credentials]** Not applicable: this path mints, stores, compares
  and transports nothing secret — the values that leave are two integers.

- **[Cryptographic primitives]** Not applicable: no randomness and no comparison against
  a secret. The one comparison in the design is `dir == ""`, on a daemon-composed string.

- **[Threat model alignment]** `docs/protocol-mobile.md` § Security model's relevant
  actor is a **paired-but-hostile device**. Its net new lever after this change: it can
  steer the daemon to stat `~/.claude/projects/<encoded(D)>/<its own session id>.jsonl`
  for any directory `D` under `$HOME` it can name through `create_conversation`'s `cwd`.
  Because the stem is a UUID the daemon minted for that device's own conversation, no
  pre-existing file can be named, and the answer it learns is the usage of its own
  transcript — which is precisely what the verb exists to tell it. Unpaired actors reach
  none of this. No threat is deferred.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-14
