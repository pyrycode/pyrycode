# #2169 — register `pyry_files` on the interactive spawn and carry the calling session's identity

The third of three slices split from #2165. #2164 shipped the `attachment.file` control verb and its
destination (`fileAttacher`); #2168 shipped `pyry mcp-files`, the stdio MCP server exposing `send_file`,
which forwards to that verb and reads the session id it forwards from its own environment. Nothing
spawns that server. This slice registers it on the interactive stream spawn and puts the right session
id on each child, which is what makes the tool reachable by a real claude.

## Files read

Production surface:

- `cmd/pyry/mcp_config.go` → `renderMCPApproveConfig`, `writeMCPApproveConfig`, `mcpApproveConfig`,
  `mcpServerSpec`, `permissionArgs`, `approveToolRef` — the document that gains a second entry, its
  two fail-closed guards, and the derive-the-reference-from-the-subcommand's-own-constants pattern
  AC-1's drift guard copies.
- `cmd/pyry/mcp_files.go` → `mcpFilesServerName`, `sendFileToolName`, `envSessionID`, `runMCPFiles`,
  `filesServer` — the three constants #2168 minted for this ticket to write against, and the flag
  contract (`-pyry-name`/`-pyry-socket`, no positionals) the argv must match. `filesServer.toolsCall`'s
  empty-id guard is what makes an absent environment variable a refusal rather than a misfile.
- `cmd/pyry/streamsup_runner.go` → `mapStreamsupConfig`, `newStreamRunnerFactory`, `withApprovalArgs` —
  the mapper that gains `Env`, its stated dividing line (a plain value off one `RunnerConfig` field
  belongs in the mapper; a runtime object belongs one layer up), and the bypass arm AC-3 pins.
- `internal/streamsup/runner.go` → `Config.Env`, `Runner.spawnAndWait` — `Env` is appended to
  `os.Environ()` for the claude child, and only when non-nil. Its doc's "production leaves it nil"
  sentence stops being true here.
- `cmd/pyry/main.go` → `runSupervisor`'s startup block around `writeMCPApproveConfig` and
  `selectInteractiveRunner` — the one production call site of the renamed writer, and the daemon-global
  once-at-startup lifecycle that is exactly why the session identity cannot ride the document's argv.
- `cmd/pyry/attach_file.go` → `fileAttacher`, `conversationForCurrentSession` — the daemon-side rule
  the live test's evidence rests on: the destination is derived from the CALLING session's current
  conversation, so an attachment appearing under that conversation's directory proves the id travelled
  and resolved.
- `internal/attachments/storage.go` → `EnsureDir` — the on-disk layout the live test reads:
  `<instanceDir>/conversations/<conversation-id>/attachments/<attachment-id>/<file>`.
- `cmd/pyry/modal_resolve_v2.go` → `streamApprovalBridge.Surface`, and `internal/modalbridge/modal.go`
  → `PermissionRequestForClass`, `buildPayload` — `Surface` passes `permbridge.Request.ToolName` as the
  screen text, and `buildPayload` puts it in `ModalShownPayload.Prompt`. That is what makes "the modal
  names `mcp__pyry_files__send_file`" an assertable observable rather than an inference.

Tests and harness:

- `cmd/pyry/mcp_config_test.go` → `TestRenderMCPApproveConfig_Content`, `TestApproveToolRef_DriftGuard`
  and the fail-closed table — the shape AC-1's tests extend, including `nextValueEquals` and the
  explicit bare-argv pin.
- `cmd/pyry/streamsup_runner_test.go` → `TestMapStreamsupConfig_Bootstrap`, `TestWithApprovalArgs` —
  the first currently asserts `got.Env == nil` and is this ticket's natural RED; the second is the
  table AC-3's boundary case joins.
- `internal/e2e/realclaude/interactive_stream_modal_resolution_test.go` →
  `startStreamModalResolutionHarness`, `TestInteractiveStreamModalResolution` — the worked example of
  raising a real modal on the stream path and answering it; the live test reuses the harness verbatim.
- `internal/e2e/realclaude/harness_modal_test.go` → `raiseRealPermissionModal`, `drainForControlEvent`,
  `writeFileTrigger` — the trigger/drain scaffold. `raiseRealPermissionModal` asserts only
  `Class == "permission"`, so the live test needs a local sibling that also asserts the tool reference.
- `internal/e2e/realclaude/interactive_stream_attachment_read_test.go` → `requireStoredAttachment` —
  the on-disk attachment assertion to mirror, including its "name the directory, never the leaf"
  obligation.

Package overviews:

- `docs/knowledge/features/pyry-mcp-files-command.md` § "Out of scope (deferred to #2169)" — names the
  shell-path bypass (`pyry mcp-files` run directly with a hand-picked `PYRY_SESSION_ID`) as this
  ticket's inheritance. § "Session id: constructor parameter" records why the id is a constructor
  parameter and never read in the constructor.
- `docs/knowledge/features/pyry-mcp-approve-command.md` — the sibling subcommand whose registration
  shape this document copies.
- `docs/knowledge/features/streamsup-package-constructing-a-streamrunner-newstreamrunnerfacto.md` —
  the mapper-vs-factory dividing line, stated as a package rule rather than a per-ticket choice.

## Context

The operator asks claude for a file; claude makes it on the host and the operator cannot reach it. The
`send_file` tool closes that gap, and it is unreachable until something registers its server on the
spawn claude actually runs. Two facts decide the whole design:

1. **The `--mcp-config` document is daemon-global.** `runSupervisor` writes it once at startup and
   removes it at shutdown; it is byte-identical for every session. So the server's argv cannot name a
   session, and the identity has to travel on something per-spawn.
2. **`streamsup.Config.Env` is per-spawn and inherited.** It is appended to `os.Environ()` for the
   claude child, and the MCP server claude forks inherits it. That is the only per-spawn channel
   between the daemon and a process it never execs itself.

No ADR is warranted: both halves are applications of settled patterns (#1106's config shape, the
mapper's own stated dividing line) rather than new decisions.

## Design

### 1. A second entry in the same document, under the same guards

`renderMCPApproveConfig` gains one map entry:

```go
mcpFilesServerName: {Command: pyryBin, Args: []string{"mcp-files", "-pyry-socket", socketPath}},
```

Both guards already fail the whole render on an empty `pyryBin` or an empty `socketPath`, so they
extend to the new entry for free — there is no arrangement in which `pyry_files` is emitted with a
bare argv that resolves to the default-named instance while `pyry_approve` is not. That is the point
of putting it in the same function rather than composing a second document.

`permissionArgs`' two-branch contract, the `pyry_approve` entry, and the
`--strict-mcp-config` / `--permission-mode default` set are untouched. `--strict-mcp-config` means
claude loads this document and nothing else, so a second entry here is a second server there.

**Renames.** The three writer names become accurate now that the document carries two servers:

| Was | Becomes |
|---|---|
| `mcpApproveConfig` | `mcpServersConfig` |
| `renderMCPApproveConfig` | `renderMCPServersConfig` |
| `writeMCPApproveConfig` | `writeMCPServersConfig` |

Consumer sites outside the defining file: one production (`runSupervisor`) and nine in
`cmd/pyry/mcp_config_test.go`. Three prose comments in `internal/e2e/realclaude` cite the old names and
are updated; they are cross-package and call nothing, so this is cite hygiene, not a compile break.

**Deliberately NOT renamed**, each for a reason worth stating rather than leaving as an inconsistency
a later reader has to re-derive:

- The temp-file prefix `pyry-mcp-approve-*.json`. It is an observable in a shared `$TMPDIR` that
  `interactive_background_idle_probe_test.go`'s recorded attribution reasons about; changing it
  invalidates a recorded finding for no gain here.
- The `mcp-approve config:` error prefix. Grep-ability was the stated reason for choosing it and no
  acceptance criterion touches it; renaming it is churn with a (small) chance of breaking a text match
  somewhere the compiler cannot see.

**No `filesToolRef` production var.** `approveToolRef` exists because `--permission-prompt-tool` needs
the string; nothing in production needs `mcp__pyry_files__send_file`. Only the live test does, and it
lives in another package and must transcribe the literal. A test-side drift guard composes the
reference from `mcpFilesServerName` + `sendFileToolName` and pins it against that same literal, so a
rename of either constant reddens in `cmd/pyry` rather than silently in the live gate.

### 2. The identity on the spawn's environment

`mapStreamsupConfig` gains one field:

```go
Env: []string{envSessionID + "=" + cfg.SessionID},
```

Set in the mapper, not in `newStreamRunnerFactory`, on the mapper's own stated dividing line: it is a
plain string read off one `RunnerConfig` field, exactly as `ClaudeSessionsDir`, `RequestInitializeOnSpawn`,
`SpawnPermissionMode` and `OperatorBypass` already are. Doing it here is also what makes AC-2 assertable
against a returned struct with no new scaffolding.

Set **unconditionally**, including when `cfg.SessionID` is empty. #1108 guarantees non-empty at both
pool sites, and an empty value is exactly as safe as an absent variable: `os.Getenv` returns `""` for
both and `filesServer.toolsCall` refuses `""` before it dials. A conditional would buy nothing and cost
the mapper its totality.

Two docs stop being true and change with it: `streamsup.Config.Env`'s "production leaves it nil", and
`mapStreamsupConfig`'s "Stderr/Env have no supervisor.Config analogue and stay nil".

### 3. The bypass boundary, pinned rather than incidental

`withApprovalArgs` is unchanged. A child that keeps the bypass it launches with — the stored escalation,
or the operator's pass-through — gets its args back untouched, so it carries no `--mcp-config` and sees
no MCP server at all, `pyry_files` included. Registering the server for those children means appending
`--mcp-config` inside that arm, and that arm's doc is an essay on how presence-of-flag reasoning
inverted both of the daemon's permission fail-safes at #2065. Out of scope by the ticket's own decision;
the boundary gets a named test so it cannot change silently.

The boundary is about the **args**. Whether such a child also carries `PYRY_SESSION_ID` is unconstrained
and deliberately unasserted: with no server registered, nothing forks the process that would read it.

### Data flow

```
runSupervisor ──> writeMCPServersConfig(pyryBin, socket) ──> /tmp/pyry-mcp-approve-*.json
                     {"mcpServers":{"pyry_approve":…,"pyry_files":["mcp-files","-pyry-socket",sock]}}
                                     │  (daemon-global, once, identical for every session)
                                     ▼
sessions.RunnerConfig ─> mapStreamsupConfig ─> streamsup.Config{Env:["PYRY_SESSION_ID=<this session>"]}
                                     │                        Args + withApprovalArgs(--mcp-config …)
                                     ▼
                          spawnAndWait: os.Environ() + Env  ──> claude child
                                                                  │ forks per --mcp-config entry
                                                                  ▼
                                                     pyry mcp-files -pyry-socket <sock>
                                                     reads PYRY_SESSION_ID from its inherited env
                                                                  │ attachment.file
                                                                  ▼
                                                     fileAttacher → the CALLER's conversation
```

## Concurrency model

None added. `renderMCPServersConfig` is pure; `writeMCPServersConfig` is one synchronous
`CreateTemp`/write/close with no shared state; `mapStreamsupConfig` mutates nothing and constructs no
runtime object. `Env` is a freshly allocated slice per mapper call, so no two runners share a backing
array. No goroutine is spawned, so none can leak.

The document is written once before any runner is constructed and removed after `runSupervisor`
returns, which is the lifecycle #1168 already established; a second entry does not change it.

## Error handling

| Failure | Behaviour |
|---|---|
| `pyryBin == ""` or `socketPath == ""` | `renderMCPServersConfig` returns an error and nil bytes — neither server is emitted. Startup already fails fail-closed on this; unchanged. |
| Config write fails | Daemon startup aborts, as today. No session ever spawns with an empty `--mcp-config`. |
| `cfg.SessionID == ""` reaches the mapper | `PYRY_SESSION_ID=` is set; `filesServer.toolsCall` refuses every call with `reasonSendFileNoSession` before dialling. Fail-closed, and unreachable at both pool sites per #1108. |
| Child stays in bypass | No `--mcp-config`, so no `pyry_files`. `send_file` is simply absent from that session. Stated boundary, pinned by a test. |
| claude forges or invents a session id | Bounded, not defended. The daemon validates the id against its own registries and derives the conversation from it, so at worst it names another of the operator's own live sessions — a misfile, not an escape. `TestFileAttacher_DestinationIsCallerSession` already pins the daemon-side rule. |
| `send_file` called and refused by the operator at the modal | claude reads a denial; nothing is stored. Every call raises a modal by design — the interactive spawn settings carry no `permissions` object, and no allow-list entry is added. |

## Testing strategy

Unit (`cmd/pyry`, `go test -race`, table-driven, stdlib only):

- **AC-1 content** — extend the render-content test: the document names exactly two servers,
  `mcpServerName` and `mcpFilesServerName`; the files entry's command is the pyry binary and its args
  are `["mcp-files","-pyry-socket",<socket>]`; `nextValueEquals` pins the socket value; and neither
  entry equals its bare-subcommand argv (the wrong-daemon hazard).
- **AC-1 fail-closed** — the existing empty-binary / empty-socket table asserts an error and nil bytes;
  extend its failure message to name both servers, since one render now gates two registrations.
- **AC-1 drift guard** — `"mcp__" + mcpFilesServerName + "__" + sendFileToolName` equals
  `"mcp__pyry_files__send_file"`, the literal the live gate transcribes.
- **AC-2 identity** — map two `sessions.RunnerConfig` values differing only in `SessionID` and assert
  each returned `streamsup.Config.Env` carries exactly `PYRY_SESSION_ID=<its own id>` and does not
  carry the other's. Two sessions, not one, because a single-session assertion passes for a mapper that
  hardcodes a constant.
- **AC-2 regression** — `TestMapStreamsupConfig_Bootstrap`'s `Env == nil` assertion becomes an assertion
  on the composed value. This is the RED: it fails on the current tree before the mapper changes.
- **AC-3 boundary** — a subtest in `TestWithApprovalArgs`: for both staying-in-bypass rows, the returned
  args carry no `--mcp-config`, so no document and therefore no `pyry_files` server reaches that child.
  Asserted by flag absence rather than by slice equality, so the claim is about the boundary rather
  than about an unrelated argv shape.

Live claude (`internal/e2e/realclaude`, build tag `e2e_realclaude`, run by `make e2e-realclaude`; the
ticket carries `needs-real-claude`):

- **AC-4** — reuse `startStreamModalResolutionHarness` unchanged. Pre-create a file with known bytes in
  the conversation's recorded workspace (`h.workdir`) so the turn raises exactly one modal — the
  `send_file` one — instead of interleaving a `Write` modal ahead of it. Prompt claude to hand that file
  over. Drain to `modal_shown` and assert `Class == "permission"` **and**
  `Prompt == "mcp__pyry_files__send_file"`; answer `allow_once`; drain to a completed turn; then assert
  the daemon stored the file at
  `<home>/.pyry/test/conversations/<convID>/attachments/<uuid>/<filename>` with the expected bytes.

  Non-vacuity, both ends. A tool absent from the document raises no modal, so on `main` the drain
  deadlines (or, if claude reaches for `Write`/`Read` instead, the `Prompt` assertion fails naming the
  tool it actually asked for — a sharper red than a timeout). The stored attachment is the proof a
  valid session id travelled and resolved to the **driving** conversation: an absent or unresolvable id
  refuses instead of minting, and the directory is keyed by the conversation `fileAttacher` derived from
  the calling session, not by anything the test supplied.

  This gate is not runnable from a builder session — no agent session on this machine can sign claude
  in, so a local probe skips at the credential check and exits 0. The RED-on-`main` confirmation and the
  green are the dispatcher's gate to produce, judged by the count of tests that executed rather than by
  an exit code. `make check` never compiles this package, so its green says nothing about it.

Not run here: the full-module race suite and `make check` (the verifier's gate). Touched-scope
verification is `go test -race ./cmd/pyry/... ./internal/streamsup/...`, `go vet ./...`,
`go build ./cmd/pyry`, plus `go vet -tags e2e_realclaude ./internal/e2e/realclaude/` so the tagged
package is proven to compile — `make check` cannot see it, and a package that fails to build there runs
zero tests and still exits 0.

## Open questions

1. **Does the permission modal's `Prompt` carry the full MCP tool reference?** Resolved during Phase A
   by reading the path rather than guessing: `streamApprovalBridge.Surface` passes
   `permbridge.Request.ToolName` as `PermissionRequestForClass`'s screen text, and `buildPayload` puts
   that trimmed string in `ModalShownPayload.Prompt`. So the assertion is `Prompt` equality, not a
   substring search of a rendered sentence.
2. **Does registering a second server perturb the approve bridge?** Expected not — the entries are
   independent stdio servers and `permissionArgs` is untouched — but the live gate is what actually
   answers it, since `TestInteractiveStreamModalResolution` runs in the same suite and would redden if
   a second entry disturbed the permission round trip. Recorded here so a green there is read as
   evidence rather than as luck.
3. **Is the leaf filename safe to assert on in the live test?** Yes, and only because the test authors
   it: `requireStoredAttachment`'s "name the directory, never the leaf" obligation is about
   client-supplied names, and this name is a fixture constant.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries] OUT OF SCOPE — the shell-path bypass, inherited from #2168 and settled in this
  ticket's body.** This slice creates one new boundary: the daemon writes `PYRY_SESSION_ID` onto a
  process claude controls, and claude can both read it and invent a different one. Nothing stops claude
  from running `pyry mcp-files` directly with a hand-picked id, or from passing one that names another
  live session. It is bounded rather than defended, and the bound is structural, not conventional:
  `fileAttacher` refuses `""`, requires the id to name a **live** session (`sessions.Pool.Lookup`) and
  then requires it to be some conversation's `CurrentSessionID` (`conversationForCurrentSession`, which
  reads `CurrentSessionID` only, so a retired id cannot file into the conversation that moved on from
  it). Every id that survives all three names one of the operator's own live sessions, so the worst
  outcome is a misfile inside the operator's own data — never an escape to another party's. The rule is
  already pinned daemon-side by `TestFileAttacher_DestinationIsCallerSession`, including that the
  destination is the caller's conversation and not the follow-active cursor's. Closing the bypass means
  authenticating the forked child to the daemon, which is a design this family has not taken on; it is
  named in `pyry-mcp-files-command.md` § "Out of scope" and remains a human call.

- **[Trust boundaries] No further findings — the environment value is not new disclosure.** Verified,
  not assumed: `streamsup.Runner.buildArgs` puts the same UUID in every spawn's argv as
  `--session-id <id>` (first run) or `--resume <id>` (respawn), and `spawnAndWait` already logs it —
  `r.log.Info("spawning claude", "args", args, …)`. So claude could read its own session id off
  `/proc/self/cmdline` (or its own argv) before this ticket, and the operator's log already carried it.
  `PYRY_SESSION_ID` moves a value claude already holds to a place its forked child can reach; it does
  not widen who knows it.

- **[Tokens, secrets, credentials] No findings.** The session id is authority-bearing at the
  `attachment.file` seam, so it is treated as one: `cfg.Env` is never logged anywhere in
  `internal/streamsup` (checked — `spawnAndWait` is its only reader), `mapStreamsupConfig` has no error
  return and so no message that could interpolate it, and `filesServer.refuse` deliberately logs only a
  fixed stage word for exactly this reason. No new token is minted, stored, or rotated here. The
  attachment id the tool returns is minted daemon-side from `crypto/rand` via `conversations.NewID` and
  is documented safe to surface, since retrieval re-validates it against the conversation binding.

- **[File operations] No findings — no file operation is added.** The `--mcp-config` document keeps its
  writer, its `os.CreateTemp` recipe, its `0600` mode, its remove-on-error path, and its
  written-once-at-startup / removed-at-shutdown lifecycle. It gains one map entry whose two values are
  the same `pyryBin` and `socketPath` the existing entry already carries, so it discloses nothing new to
  a reader of the shared `$TMPDIR`. No path in this slice is built from caller input; the confinement,
  TOCTOU and symlink defences all live daemon-side in `confineFile` / `readChecked`, and this ticket
  adds no second copy of them.

- **[Subprocess / external command execution] No findings, but two properties worth stating because they
  look like changes and are not.** (1) The new entry's `command` and `args` are entirely daemon-derived
  — `resolveExecutable()` and the daemon's own `socketPath` — with no caller-controlled value anywhere
  in the argv, and `renderMCPServersConfig`'s two guards fail the whole render before an empty one can
  be emitted. No `sh -c`. (2) Flipping `Config.Env` from nil to non-nil switches `cmd.Env` from implicit
  inheritance to `append(os.Environ(), …)` — the same set of variables, since an `exec.Cmd` with a nil
  `Env` already inherits the parent's environment. Nothing is newly inherited and nothing that used to
  be scrubbed stops being scrubbed. A daemon environment that already carried `PYRY_SESSION_ID` is
  overridden by ours rather than the reverse, per `exec.Cmd.Env`'s documented last-duplicate-wins rule
  — the fail-safe direction, since the daemon's value is the correct one.

- **[Subprocess] No findings — the composed variable cannot be split into two.** `envSessionID + "=" +
  cfg.SessionID` is one slice element and therefore one `NAME=value` entry however the value is spelled;
  an environment entry is not delimiter-parsed after the first `=`. Independently, every `SessionID`
  reaching the mapper is a 36-char canonical UUIDv4: minted by `sessions.NewID` from `crypto/rand`, or,
  on the one caller-supplied path (`sessions.GetOrCreate`), gated through `sessions.ValidID`, which
  checks length, lowercase hex, dash positions, version nibble and variant. So no NUL, newline or `=`
  can reach the value in the first place, and a NUL would be refused by `os/exec` regardless.

- **[Cryptographic primitives] N/A — this slice adds no randomness, no comparison against a secret, and
  no key material.** The only random value in the flow is the attachment id, minted daemon-side by
  `conversations.NewID` (`crypto/rand`, UUIDv4) and unchanged here.

- **[Network & I/O] No findings — no listener, dial, deadline or size cap is added or changed.** The
  forked server reaches the daemon through the same `control.AttachFile`, bounded at `control.DialTimeout`
  by an undeadlined ctx, with the same `maxAttachFileBytes` ceiling enforced daemon-side. The 5s-client-
  under-35s-daemon residual is #2168's, recorded in its package overview, and this ticket neither widens
  nor narrows it.

- **[Error messages, logs, telemetry] No findings.** Nothing added logs a path, a filename, an argument
  byte or a session id. The one success log on the daemon side (`fileAttacher`) carries a conversation id
  and an attachment id and nothing else, which is `docs/protocol-mobile.md` § Attachments' filename ban
  satisfied by construction. The live test's failure messages name its own fixture path and fixture
  filename — values the test itself authored, not client-supplied ones, which is the distinction
  `requireStoredAttachment`'s own "name the directory, never the leaf" rule turns on.

- **[Concurrency] No findings.** `Env` is a freshly allocated one-element slice per `mapStreamsupConfig`
  call, so two runners never share a backing array and no append can be observed by another spawn. No
  goroutine, lock or shared mutable state is introduced, so there is no lock order to document, no
  check-then-mutate window, and nothing to leak. The document is written once before any runner exists.

- **[Threat model alignment] One deliberate acceptance, stated rather than defended.** A bypass child
  carries `PYRY_SESSION_ID` on its environment while its args register no server — the mapper sets the
  variable unconditionally, and AC-3's boundary is about the args. The marginal risk is nil rather than
  small: a child in bypass already runs arbitrary commands with no permission modal, so it can read the
  daemon's own `sessions.json` for any id it wants; a variable it could have read anyway grants it
  nothing. Making the mapper conditional would cost totality and directly assertable behaviour to remove
  a capability the same child obtains one command later.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-06

## Revisions

### 2026-09-06 — the identity is composed per spawn, not per construction

**Driven by:** the verifier's MUST FIX on PR #2171 — the id put on the child's environment went stale
on a session-id rotation, and after that `send_file` refused for the rest of that session's life.

**What was wrong.** § 2 above composed the variable in `mapStreamsupConfig` as
`Env: []string{envSessionID + "=" + cfg.SessionID}`. `cfg.SessionID` is construction-fixed — the
field's own construction-site comment in `internal/sessions` → `Pool.New` says it "does NOT mirror a
/clear rotation" — and a process's environment is fixed at exec. So every child spawned after a
rotation carried the retired id while its own argv carried the live one. `Pool.rekeyLocked` deletes the
old key, so `fileAttacher`'s liveness adapter returns `ErrSessionNotFound` and every later call is
refused. Fail-closed, never a misfile, but the ticket's central claim — *carry the calling session's
identity* — held only until the first rotation. The plan never stated the condition, and the live gate
could not see it: `TestInteractiveStreamSendFile` issues no `/clear`.

**The new contract.** The variable's NAME crosses the mapper seam; the binding is composed at the
spawn that knows the live id.

- `streamsup.Config` gains `SessionIDEnvVar string` (the name; empty binds nothing) and
  `Config.Env` goes back to what it was — a caller-fixed slice production leaves nil.
- `beginSpawn` composes the child environment below its unlock, from the SAME snapshotted id
  `buildArgs` reads, via the new pure `spawnEnv(base, name, sessionID)`. `spawnAndWait` takes that
  composed `env` instead of reading `r.cfg.Env`. Both are one more value through signatures that
  already carry `args`, `freshSeq` and `spawnMode` from the same snapshot, so the
  one-acquisition-per-spawn-setup charter (#1481) is untouched — and reading a live id at spawn time
  instead would have been a second acquisition that could land on the far side of a racing rotation
  and skew the argv and the environment apart.
- `mapStreamsupConfig` sets `SessionIDEnvVar: envSessionID` and leaves `Env` nil.

The argv and the environment now name one session by construction rather than by agreement.

**What this does NOT close, and cannot.** A `/clear` rotation re-keys the pool **without respawning**
(`Pool.onRotate` → `RotateID`; no `RestartFresh`), so the live child keeps the environment it was
exec'd with. `send_file` from that child refuses — fail-closed, the same shape as before — until that
runner's next spawn. No environment-based design can close it: an exec'd process's environment is not
writable from outside. Closing it means giving the forked server a way to ask the daemon "which session
is this child?" rather than telling it, which is the same authentication design
`docs/knowledge/features/pyry-mcp-files-command.md` § "Out of scope" already names as a human call, and
is out of scope here. Two consequences worth stating plainly:

- The claim this ticket ships is **the spawn's identity**, not the session's identity for all time.
- The runner's `r.sessionID` is not updated by a `/clear` either, so a post-`/clear` crash-respawn
  re-emits the retired id in both places. That is pre-existing behaviour of the rotation seam (the pool
  observes the rotation; nothing pushes it into the runner), not something this ticket introduces, and
  it stays fail-closed.

**Tests.** AC-2's proof moves one layer down, to where the value is now composed, and gets stronger for
it — it asserts what the child actually receives rather than what a struct field says:

- `internal/streamsup` → `TestRunner_BeginSpawn_EnvCarriesOwnLiveSessionID` — two runners, each
  spawn's environment carries its own id and not the other's (AC-2).
- `internal/streamsup` → `TestRunner_BeginSpawn_EnvTracksRotatedSessionID` — after `RestartFresh`, the
  environment and the argv both name the rotated id. This is the regression pin for the finding, and
  it was RED on the wiring before the fix.
- `internal/streamsup` → `TestSpawnEnv` — the composer's edges: an unset name binds nothing, and the
  caller's `Config.Env` is never appended into (spare capacity supplied, so the aliasing is
  observable).
- `cmd/pyry` → `TestMapStreamsupConfig_CarriesOwnSessionIdentity` keeps its two-session shape on
  `SessionID`, pins `SessionIDEnvVar`, and now asserts `Env == nil` — the direct regression pin against
  composing a construction-time identity here again.

**Also in this revision** (both verifier NITs): `mcpApprovePath` is renamed `mcpServersPath` in
`runSupervisor`, `selectInteractiveRunner`, `newStreamRunnerFactory` and `withApprovalArgs`, joining
§ 1's three renames — it names a document carrying two servers. And the transcribed mcp-config comment
in `ask_user_question_capture_test.go` and `bypass_approval_argv_probe_test.go` now says the probe
registers the approve server alone, deliberately, so a reader arriving from the renamed function is not
left asking where `pyry_files` went.
