# `internal/codexsup` — Codex app-server connection and thread lifecycle

Standalone client for `codex app-server` (#2591, target Codex **0.156.1**), the
first slice of Codex support (#2583 family). It spawns the process, drives the
`initialize`/`initialized` handshake over stdio, and starts, resumes and
interrupts turns on one Codex thread. `cmd/pyry`'s Codex runner (#2620, below)
is its only production consumer: it supervises one `Client` at a time and
supplies the crash backoff, respawn and pool wiring this package itself does
not provide.

`translate.go`'s `Translator` (#2608) is the Codex twin of
[`streamsup`'s parser](streamsup-package.md): it maps server notifications
into [`turnevent.Event`](turnevent-package.md) values so a Codex turn renders
like a Claude turn with no client change. It reaches production through the
same #2620 runner, one `Translator` per spawn.

## The translator's three-list partition

`Translate(method, params)` runs on the caller's goroutine (`OnNotification`),
holds per-turn state (pending usage, reroute target), and is not safe for
concurrent use. Every method in `methods.go`'s `serverNotifications`, and
every schema `ThreadItem` type, sits on exactly one of three package-level
lists — `mappedMethods`, `ignoredMethods`, `unrecognizedMethods` (and the item
twins `mappedItemTypes`/`ignoredItemTypes`/`unrecognizedItemTypes`) — never on
none or on two. `TestMethodListsPartitionServerNotifications` and
`TestItemTypesClassified` fail otherwise, so a schema bump that adds a method
or item type forces a decision instead of dropping it silently. An unmapped
method becomes `turnevent.Unrecognized{Site: UnrecognizedCodexMethod}`; an
unmapped item type becomes `Unrecognized{Site: UnrecognizedCodexItem}`, once
per item on `item/started` only (its `item/completed` stays silent — one row
per item, not two). `model/rerouted` and `model/verification` are on this
partition too, deliberately, so a model reroute is never silent: see below.

Text and reasoning deltas (`item/agentMessage/delta`,
`item/reasoning/summaryTextDelta`, `item/reasoning/textDelta`) become
`TextChunk`/`ThoughtChunk` keyed by the Codex item id. A `contextCompaction`
item's `item/started`/`item/completed` become `Compacting{Active: true/false}`.
`turn/completed` becomes exactly one `TurnEnd`; a `usageLimitExceeded` failure
emits `RateLimited{Status: "rejected"}` first. `model/rerouted` becomes a
warning `Banner` and keys that turn's `ModelWindows` by the model it moved
to — a silent downgrade would otherwise pass for the requested model.

`commandExecution` and `fileChange` items (#2609) become tool events instead
of `Unrecognized`, so a Codex tool row renders like a Claude one with no
client change: `item/started` is one `ToolStart` (`Kind` execute or edit,
`RawInput` `{command, cwd}` for a command, one `Location` per changed path
and the literal title `"apply_patch"` for a file change — Codex reports no
tool name for a file change, so the translator names the tool that produces
the item, the same way it would for a Claude `Edit`), and `item/completed` is
exactly **one** `ToolUpdate` — never a stream of them. That constraint is why
`item/commandExecution/outputDelta` and `item/fileChange/outputDelta` moved
to `ignoredMethods` rather than `mappedMethods`: turnbridge turns every
`ToolUpdate` into a `tool_result` frame, so forwarding the deltas too would
give one tool row several results. `ResultDetail` is built only from the
translator's own literals and the decoded exit code — `"declined"`, or
`"exit "` followed by `strconv` digits with U+2212 (not the ASCII hyphen) for
a negative code — never from the peer's status string, so no Codex byte
reaches that field. A file change's `ToolUpdate` content is each changed
path followed by that change's diff exactly as Codex sent it, joined by
blank lines; nothing is read from disk to reconstruct it.

**Why `thread/tokenUsage/updated` is on the ignored-for-emission-but-still-read
list.** It carries a `turnId` and is held (not emitted) until that turn's
`turn/completed`, at which point the turn's own counts and window ride the
`TurnEnd` in the same fields Claude's `result` line fills.

## Per-turn usage: `last` is a trap, and the arrival order matters

Live-captured against Codex 0.156.1 (Luna, effort low):

- `thread/tokenUsage/updated` arrives **before** `turn/completed`, once per
  model call — the accepted-command turn made two calls and two updates.
- `ThreadTokenUsage.last` covers only the **latest model call**, not the
  turn: on that two-call turn, `last.outputTokens` read 5 against 67 for the
  whole turn. A test that only exercises a single-call turn cannot catch
  this — `last` and "the turn's total" agree exactly when there is only one
  call. The turn's counts instead come from the **change in `total`**
  (the thread's running total), with the base taken as `total − last` at the
  turn's first update — which equals the thread's total before the turn
  started, on both a fresh and a resumed thread, so the translator needs no
  separate per-thread baseline.
- `inputTokens` **includes** `cachedInputTokens` (cached ≤ input on every
  observed update), so `TurnEnd.InputTokens` subtracts it:
  `inputTokens − cachedInputTokens`. `cacheWriteInputTokens` was 0
  throughout the capture, so whether `inputTokens` also includes it is
  unmeasured and it is *not* subtracted — a discrepancy to watch for if a
  future capture shows a nonzero cache write.
- Because the counts are differences of peer-supplied totals, they are
  clamped at zero before reaching `TurnEnd` (`tokenBreakdown.clamped`):
  malformed or out-of-order usage from the peer must not produce a negative
  count on the wire.
- Per-turn state (pending usage, reroute target) is keyed by the peer-chosen
  `turnId` and capped at `maxPendingTurns` (8), clearing the map on overflow
  rather than growing unbounded — Codex runs one turn per thread at a time,
  so a legitimate peer never approaches the cap.

**No live-reachable notification carries the turn's model.** `NewTranslator`
takes the model as a constructor argument (`TurnInput.Model`, the caller's
own knowledge) plus `SetModel` between turns. `thread/settings/updated`'s
`ThreadSettings.model` is thread-scoped and schema-required, but was not
observed live and sits on `ignoredMethods`; #2585 may prefer reading it over
threading the model through the caller.

## The capture's live gaps, and what they mean for a recapture

`TestCaptureLive` (env-gated: `PYRY_CODEX_CAPTURE_BIN` +
`PYRY_CODEX_CAPTURE_HOME`, so `make check` never runs Codex) captures plain
text, an accepted command, a declined command, an interrupted turn and a
file-edit attempt against real Codex 0.156.1. Two frame types could not be
produced on demand and are hand-built instead, each schema-validated by the
package's `jsonSchema.validate`:

- **Reasoning deltas** — Luna at effort `low` emitted no reasoning item in
  the capture.
- **A `fileChange` item** — the file-edit turn ran a `pwd && ls -la`
  pre-check first, which the capture's approval predicate declined (it
  accepts only the exact command it expects), and Codex gave up without
  ever proposing the edit. #2609 mapped `fileChange` to tool events anyway,
  proven only against the schema with hand-built frames
  (`testdata/handbuilt/file_change.jsonl`) rather than a real capture — this
  package's fixtures still don't have one. The mapping assumes `item/started`
  already carries the item's `changes` (the source of `ToolStart`'s
  `Locations`), since the schema requires the field on every `ThreadItem` but
  no capture confirms Codex populates it that early. **Still open**: a real
  `fileChange` capture would confirm or revise that assumption.
- The granular approval policy needs the `experimentalApi` capability at
  `initialize`, which this package's handshake does not declare
  (`askForApproval.granular requires experimentalApi capability`); the
  capture uses `untrusted` with the `read-only` sandbox instead, which still
  yields command approval requests.

**Fixture scrub is enforced in code, not just by a manual grep.** The first
cut of `writeCapture` replaced `cwd` and `os.UserHomeDir()` but not the
isolated `CODEX_HOME` path itself (nor its symlink-resolved form), which
leaked the capturing machine's scratchpad path and OS username through
`thread.path`, and it didn't redact `remoteControl/status/changed`'s
`serverName` (hostname) / `installationId`. Both slipped past a "scrubbed of
local paths" claim in the plan and PR body that no test checked — a grep for
`/Users/` alone misses the dash-encoded worktree-path form. Fixed by passing
the `CODEX_HOME` path into `writeCapture` for replacement alongside `cwd`,
adding `serverName`/`installationId` to `scrub`'s redacted keys, and adding
`TestCaptureFixturesScrubbed`, which fails the build on any `/private/`,
`/Users/`, `-Users-`, `/home/`, `/var/folders/`, unredacted email, or
unredacted machine-key fragment under `testdata/capture/`. Any future capture
addition (#2609's included) inherits this check for free; a new leak shape
would still need a new pattern added to the test.

It reuses [`internal/acp`](acp-package.md)'s `Transport` unchanged for framing
and dispatch, and is tested against
[the fake app-server](fakecodex-binary.md) plus an in-memory peer, with no
Codex account. The wire contract is the committed
`internal/codexsup/codex_app_server_protocol.schemas.json` (`internal/codexsup/SCHEMA.md`
covers regeneration).

## Deny-by-default is the central property

No default path — the one taken when `Config.OnServerRequest` is nil — ever
answers a server request with an acceptance. `serverrequest.go`'s
`declineFor` is a closed switch over the five approval methods with a
schema-shaped decline/denied/empty-grant result; every other server request
(the other five methods at 0.156.1: `item/tool/requestUserInput`,
`mcpServer/elicitation/request`, `item/tool/call`,
`account/chatgptAuthTokens/refresh`, `attestation/generate`) gets a JSON-RPC
`-32601` error. `cancel`/`abort` are deliberately not used as a decline —
both also interrupt the running turn, which a passive default should not do.

Because every method in `methods.go`'s `serverRequests` is registered on the
transport before `Serve` starts, an unlisted future server request cannot
reach this table at all: `acp.Transport` answers an unregistered request
method-not-found on its own. The decline table can only get narrower or wider
deliberately, at a version bump, never by omission.

**Who answers approvals was open through #2620; #2587 answers it for the two
approval methods.** The #2620 runner wires `OnServerRequest` to
`codexApprovals.handle` (§ Approvals reach the permission modal, below),
which parks `item/commandExecution/requestApproval` and
`item/fileChange/requestApproval` on the same daemon-wide `permbridge.Registry`
and permission modal a Claude session uses. The other eight server-request
methods at 0.156.1 — including the legacy `applyPatchApproval`/
`execCommandApproval` and the experimental `item/tool/requestUserInput` — are
still out of scope and keep this package's default decline. See
[ADR 038](../decisions/038-codex-daemon-owned-home-read-only-default-decline.md)
for why the daemon-owned home and the decline-by-default baseline were the
right posture to ship before an operator path existed, and its "Superseded in
part (#2587)" note for what changed.

### The ticket's own literal for the legacy approvals was schema-invalid

`applyPatchApproval` and `execCommandApproval` predate the newer
`item/*/requestApproval` methods and take a `ReviewDecision`, not a
`{"decision":"decline"}` shape. The ticket and the first design both wrote
their default as `{"decision":"denied"}` — a plausible guess that turned out
not to exist in the 0.156.1 schema: `ReviewDecision`'s string arms are
`approved`, `approved_for_session`, `approved_mcp_policy_amendment`,
`timed_out` and `abort`, and a denial is the object arm
`DeniedReviewDecision` (`additionalProperties: false`, `rejection`
required). The verifier caught this on PR #2606 as a MUST FIX; the fix
(`b00fe20e`) is `{"decision":{"denied":{"rejection":"…"}}}`.

The lesson that outlives the ticket: a hand-written literal for a schema
response, even one that reads plausibly and even one written into the
architecture doc, is not evidence it matches the schema. `serverrequest_test.go`'s
`TestDefaultDeclinesMatchSchema` now resolves each server-request method's
response definition from the committed schema (`XParams` → `XResponse` via
the `ServerRequest` arm) and validates the literal wire value against it with
a small draft-07-subset validator (`$ref`, `allOf`/`anyOf`/`oneOf`, `enum`,
`type`, `properties`, `required`, `additionalProperties:false`, `items`);
`TestSchemaValidatorRejectsBareDenied` is the negative control proving the
validator would have caught the original shape. Any package hand-assembling
a JSON-RPC response against a committed schema should validate the literal
against the schema in a test, not just against a human reading of it.

## `acp.Transport.Call` does not fail when `Serve` returns

`Transport.Call` blocks only on its own `ctx` — it has no idea the
connection died, so a call in flight when the app-server process exits (or
the stream breaks) would hang forever without help from this package. The
fix lives entirely in `client.go`, not in `acp`: `Client` keeps its own
`exitCtx`, cancelled once the lifecycle goroutine (`run`) has drained
`Serve` and recorded the exit. Every call wraps its caller `ctx` with
`context.AfterFunc(exitCtx, cancel)` before handing it to `Transport.Call`,
so an in-flight request is cancelled the moment the process is known gone,
and a call issued after exit is cancelled immediately (`AfterFunc` on an
already-done context fires at once). The returned error always wraps
`ErrExited` plus the process's exit error, distinguishing "the process
died" from an ordinary call failure.

One consequence documented on `call` rather than hidden: a response that
lands on the wire just as the process exit is being observed can still be
reported as `ErrExited`, because `Transport.Call`'s `select` may pick the
cancelled context over the delivered result. A caller that retries a failed
call against a respawned process (#2585's job) has to tolerate the
possibility that the original request actually ran.

## Stdout goes through an `io.Pipe`, not straight to the transport

`cmd.Stdout` is a pipe writer; the transport reads the pipe reader. A
separate wait goroutine calls `cmd.Wait()` and only then closes the pipe
writer, so the transport reaches EOF strictly after every byte the child
wrote has been delivered — no trailing frame (e.g. the last `turn/completed`
before an expected exit) is lost to a race between `Wait` returning and the
last read. `kill()` (used both on handshake failure and on a broken stream)
closes the pipe reader with `ErrExited` so `os/exec`'s stdout copier can't
block forever if the transport has already stopped reading; `cmd.WaitDelay`
(5s) bounds the SIGTERM-to-SIGKILL escalation the same way, in case a
descendant process is still holding stdout open.

## Production wiring — the `cmd/pyry` Codex runner (#2620)

`cmd/pyry/codex_runner.go`'s `codexRunner` implements `sessions.Runner` and
supervises one `Client` at a time, the way `streamsup_runner.go`'s
`streamRunner` supervises one Claude child. `harnessRunnerFactory` routes a
session whose harness is `codex` to `newCodexRunnerFactory`; every other
factory case is unchanged. Turn events and the app-server's exit reach the
same `streamTurnSink` a Claude session feeds, through `sinkForTag`/
`exitForTag` on one live tag, so a Codex turn appears on a client exactly
like a Claude one.

**The thread id has to live on the runner, not on `Run`'s stack.** Idle
eviction cancels `Run`'s context and returns; a later activation calls `Run`
again on that **same** `codexRunner` instance (`Session.runActive`). Anything
that must survive an eviction — here, the Codex thread id `runOnce` resumes
— has to be state on the runner (`threadID`, guarded by `mu`), not a local
in `Run`'s or `runOnce`'s frame. The same runner instance also survives a
crash and a `Restart`, so `threadID` carries across all three without a
special case for any of them; only `RestartFresh` clears it and starts a new
thread.

**The thread now survives a daemon restart too, not just an eviction
(#2622).** `registryEntry.ThreadID` (see
[sessions-registry.md](sessions-registry.md)) carries the id from a dormant
entry through `materialise` into `RunnerConfig.ThreadID`, which seeds
`codexRunner.threadID` the same way reactivation after an eviction does. The
runner reports every thread it *starts* back to the pool through
`RunnerConfig.RecordThread`, on the same no-captured-identity shape as
`RunnerConfig.AdoptAnnouncedReset`: the caller supplies the live pool id, so
the report lands correctly no matter how many rotations happened first. A
resumed thread reports nothing, so a rebuild that only resumes never
rewrites the registry.

The first draft called that report with `freshSeq` and the session tag
rotated as two separate steps in `RestartFresh`, outside a shared lock. A
report for the thread the *old* iteration had just started could land in the
gap and pair the new tag id with the old thread — a bug no test caught,
because a test drives the restart and the report in a fixed order and never
interleaves them. The fix moves `r.cfg.Tag.Rotate(newID)` inside the same
`r.mu` section as `freshSeq++`, so `(seq, tag id)` update as one pair: a
stale report either still carries the old tag id, which the pool has already
re-keyed away (`ErrSessionNotFound`, dropped), or fails the seq check and is
never sent. Any report-back closure built on this pattern needs its identity
read at call time, under the same lock as whatever it is racing against —
not captured earlier or read outside the lock.

**The running turn id comes from `turn/started`, never from `StartTurn`'s
return.** `notify` records `turnID` when the `turn/started` notification
arrives and clears it on `turn/completed`; `WriteUserTurn` discards the id
`StartTurn` itself returns. `codexsup`'s read loop delivers notifications in
order, so a turn that completes fast can have its `turn/completed` processed
before `StartTurn`'s call returns — seeding `turnID` from the return value
would then have `turn/completed`'s clear race against it. Reading only the
notification sidesteps that race, at the cost of a narrow window where
`Interrupt` arrives after `WriteUserTurn` returns but before the read loop
has processed `turn/started`: `turnID` is still empty, and `Interrupt`
returns nil without interrupting anything. The window is sub-millisecond
against human reaction time, so it wasn't worth closing here; revisit if
interrupt or approval work touches this path (PR #2623 review).

**A turn that completes before Codex handles `turn/interrupt` also makes
`Interrupt` return nil (#2636).** `declineAll` inside `Interrupt` resolves
the parked approval before the interrupt call goes out; the decline's
`await` goroutine can let the turn finish and clear `turnID` first, so
Codex answers the interrupt with a refusal for a turn it no longer runs.
`Interrupt` treats that refusal as success when `r.turnID` no longer
matches the id it interrupted — but only when the error is a routed
`*acp.Error` (Codex actually answered). A transport failure (timeout, dead
client) is always returned even if `turnID` has since moved on, because the
wire-order argument — a `turn/completed` ahead of the response on the wire
is dispatched inline before that response reaches the caller, see the
paragraph above — only holds for a response Codex sent. This relies on the
fake's `turn.run` removing a completing turn and sending `turn/completed`
as one step under `s.mu` (see [fakecodex-binary.md's note on the same
fix](fakecodex-binary.md#a-completing-turns-removal-and-its-turncompleted-are-one-step-2636));
before that the fake could answer `turnInterrupt`'s refusal ahead of the
notification, which is what made `TestCodexApproval_InterruptDeclines` flake.

**The app-server's `CODEX_HOME` is a daemon-owned directory the daemon keeps
rewriting, not one it writes once.** `codexHomePath` is `<instance
dir>/codex-home`; `prepareCodexHome` runs on every `codexRunner`
construction (crash respawn included, since `newCodexRunnerFactory` is
called once per session, not once per spawn) and unconditionally overwrites
`config.toml` — `approval_policy = "on-request"`, `sandbox_mode =
"read-only"`, `approvals_reviewer = "user"` — through a temp file and
rename, after chmod'ing the directory to `0700`. It touches no other file:
`auth.json`, from an out-of-band `CODEX_HOME=<dir> codex login`, is never
read, copied or moved. Nothing in this directory is the operator's personal
`~/.codex`, which the spike found loads a different default model at the
highest effort plus every personal MCP server, plugin and notify hook. See
[ADR 038](../decisions/038-codex-daemon-owned-home-read-only-default-decline.md).

**The version and sign-in refusal has to come out of the factory, not
`Run` (#2621).** `newCodexRunnerFactory` spends one short-lived `codex
app-server` probe — start against the daemon home, handshake, `checkCodexVersion`
on `Client.Version()`, then `Client.SignedIn` — before it ever builds a
runner, and stops that process (`stopCodexClient`, deferred) on every exit
path without opening a thread. The check cannot live inside `Run`: `Run`'s
crash-backoff loop retries any start failure forever, so a version or
sign-in check placed there would just retry the same failure silently
instead of refusing the session. The version check runs before the sign-in
call, so a Codex too old to implement `account/read` at all is reported as
too old rather than as a failed request. The compare treats a prerelease of
the pinned version as older than the release itself
(`checkCodexVersion`/`parseCodexVersion`: `0.156.1-alpha.1` is refused
against the `0.156.1` pin) — comparing only the three numeric components
would have let a prerelease with the right core numbers through.
`Client.SignedIn` decodes only `account` as `json.RawMessage`, to test for
null/absence, and the `requiresOpenaiAuth` bool; the account's own fields,
including an email address, are never decoded into a typed value, so they
cannot reach a log or an error even by accident.

**Unconfirmed, flagged for the first live run (#2621/#2622).** A thread that
was started and then evicted before any turn ran might not have a rollout
for `thread/resume` to find, if Codex creates it lazily on first turn rather
than on `thread/start`. The fake always resumes successfully, so this has
not been observed; a permanent `thread/resume` failure would retry under
backoff forever, refusing every turn on that session as `ErrNoLiveChild`
until a `RestartFresh`. No defense is built for it without a live
observation (PR #2623 review).

Model and effort reach a turn as `TurnInput.Model`/`Effort`, read from
`RunnerConfig.ClaudeArgs`'s trailing `--model`/`--effort` pair
(`codexTurnSettings`) and refreshed by `SetSpawnArgs`/`Restart`/`SetModel`. That
stored model is a family, not a version, and `WriteUserTurn` resolves it to a
version on every turn — see [§ Resolving the family to a version on every
turn](#resolving-the-family-to-a-version-on-every-turn-2628) below.

**Every turn also asserts the session's full posture (#2586), not just
model and effort.** `codexTurnOverrides` (`cmd/pyry/codex_settings.go`) is
`claudeSettingsArgs`' Codex counterpart: it turns the stored
`PermissionMode`/`YOLO` into the turn's `ApprovalPolicy`, `Sandbox` and
`ApprovalsReviewer` overrides.

| Stored posture | Approval policy | Sandbox | Reviewer |
|---|---|---|---|
| `default`, `acceptEdits` | granular | workspaceWrite | user |
| `plan` | granular | readOnly | user |
| `auto` | on-request | workspaceWrite | auto_review |
| `dontAsk` | never | workspaceWrite | user |
| `bypassPermissions` / YOLO | never | dangerFullAccess | user |
| anything else, including empty | granular | readOnly | user |

**The last row is unreachable through the pool.** `internal/sessions`'
`canonicalSettings` runs at both session-construction sites
(`Pool.mintSettings` and `Pool.revivedSettings`) and turns an empty
`PermissionMode` into `default` before a Codex runner ever sees it — a
pool-held session's mode is never `""`. So a session that never had its
mode explicitly set gets the `default`/`acceptEdits` row (`workspaceWrite`,
no modal for an in-workspace write), not the read-only row this table's last
line describes; that row only fires for a mode hand-constructed outside
`canonicalSettings`. #2660's daemon-level live test assumed the opposite —
that leaving a conversation's mode unset yields read-only — and found this
by reading `canonicalPermissionMode` rather than the table alone.

The YOLO bit is read before the mode, mirroring `claudeSettingsArgs`. The
posture is never empty: Codex keeps a `turn/start` override for the
thread's *later* turns too, so an omitted field would let a previous,
possibly looser, override survive past the turn that set it — asserting
all three every turn is what makes a tightening actually stick. A
tightening still only lands on the *next* turn: a turn already running
keeps the sandbox it started with, and `Interrupt` is the verb for ending
one early.

`SetPermissionMode` and `SetSpawnPermissionMode` now store the mode on the
runner (guarded by `mu`, read as one snapshot in `WriteUserTurn` alongside
model and effort) instead of erroring or doing nothing. A live posture,
model or effort change therefore sends nothing to Codex and respawns
nothing — it reaches Codex on the session's next turn. The pool's in-band
effort delivery hands a runner implementing `SetEffort` the value directly,
in place of the `/effort <level>` command turn Claude's runner still needs
(Codex would run that text as a prompt, not a command); see [`effortSetter`
in `Pool.UpdateSettings`](sessions-package-key-types-pool-updatesettings.md).

This supersedes [ADR 038](../decisions/038-codex-daemon-owned-home-read-only-default-decline.md)'s
fixed read-only posture — see that ADR's "Superseded in part" note for what
still stands (the daemon-owned home, the default decline, and
`config.toml` as the baseline before the first turn).

The `approvalPolicy`/`sandboxPolicy`/`approvalsReviewer` field spellings
came from the committed `codex_app_server_protocol.schemas.json`'s
`v2.TurnStartParams` — which the bundle already had at the time (#2586), a
correction of an assumption that it held only server-request types. Check
the committed bundle before assuming a `v2` request type is missing from
it and needs regenerating.

### Approvals reach the permission modal (#2587)

`codex_runner.go`'s `codexApprovals` wires `codexsup.Config.OnServerRequest`
to the same daemon-wide `permbridge.Registry` and permission modal a Claude
stdio session uses (`stdioPermissionHandler` in `streamsup_runner.go`), for
the two approval methods: `item/commandExecution/requestApproval` and
`item/fileChange/requestApproval`. `handle` runs on the read loop and only
parks — `Register` under a fresh `"codex-"`-prefixed random id (32 hex
chars from `crypto/rand`, so it cannot collide with a Claude `toolu_…` id) —
then hands the wait to its own `await` goroutine, the same shape
`stdioPermissionHandler.await` uses. Anything `handle` cannot park (an
unparseable request, an id-mint failure, a full registry) takes
`ServerRequest.Decline()`, this package's existing default.

**`ServerRequest.ID` (the JSON-RPC id, not a modal correlation id) is what
this ticket added to the type**, set in `serverRequestHandler` from
`acp.ResponderFrom(ctx).ID()` (`internal/acp`'s `Responder.ID()`, itself new).
It exists for exactly one purpose: correlating an unsolicited
`serverRequest/resolved` notification — which names the withdrawn request by
`requestId`, not by anything `codexApprovalRequest` derives — back to a
parked approval. Nothing else in this package or `cmd/pyry` reads it.

**Codex numbers server requests per process, not globally, so the
correlation has to be scoped to the runner that sent them.**
`codexApprovals.observe` matches `serverRequest/resolved`'s `requestId`
against every entry in *this adapter's own* `live` map, never a daemon-wide
one — a fresh Codex spawn restarts its request ids at 0, so a daemon-wide
match could retire a different runner's still-live approval that happens to
share an id. A match sets `withdrawn` before resolving the registry entry
with `Deny(reasonCodexWithdrawn)`, so the parked `await` goroutine (which
runs after `Resolve` delivers) always observes the flag and writes nothing
back — Codex has already moved on. [`fakecodex-binary.md`](fakecodex-binary.md)'s
`[fakecodex:withdraw]` marker is what a consumer test needs to exercise
this: the existing `[fakecodex:approval]` marker always resolves *after* the
client answers, which cannot reach the "Codex gave up first" path at all.

**A request whose real grant the modal cannot show in full is declined,
never parked with a partial or misleading display — this was a review
finding, not the first design.** The first pass of `codexApprovalRequest`
checked only how Codex's strings were *escaped* on display (see
`codexDisplay` below), not which optional params change what an `accept`
actually *grants*. Four fields do, and the first modal design showed none of
them: command `kind: writeStdin` (input to a running terminal, not a
command — the modal still read `command: …`), a non-null
`networkApprovalContext` (a managed-network host grant, host never shown),
an absent/empty `command` (an empty scope, still offered allow), and
file-change `grantRoot` (session-wide writes under a root, only the item's
own paths shown). `codexApprovalRequest` now returns `ok=false` for each —
the caller declines — rather than displaying the field or accepting the
narrower risk. The general lesson: before parking any approval on a human,
enumerate every optional field the schema defines for that request and ask
"does the modal show this?" — not just "is this string escaped?" A field
that changes the grant and isn't shown must decline, not display-with-caveat.

**A byte cap on an approval's displayed text needs a visible truncation
marker, or silent truncation itself becomes the misleading display.** The
first cut of the 4096-byte `codexMaxDescription` cap just cut the string —
for a command request, whose `Description` is `command: …\ncwd: …` with the
command first, a command longer than the cap showed its head and silently
dropped both its tail and the `cwd:` line with no sign anything was cut. The
fix: a command request whose full description doesn't fit the cap is
**declined**, never truncated, so a command is always shown whole with its
cwd — truncation is reserved for the file-change path list, which
`truncateDisplay` cuts on a UTF-8 rune boundary and always ends with a
literal `…[truncated N bytes]` marker inside the cap, never past it.

**Every Codex-supplied string that reaches the modal passes through
`codexDisplay`** — command, cwd, each changed path, the `reason`, and the
`SessionGrant` label — which escapes every non-printable rune (newline,
control, bidi-format) with its `strconv.QuoteRune` form. `permbridge.Request`
itself does not escape anything (see
[permbridge-package.md § Domain types](permbridge-package.md#domain-types-wire-contract-envelope-deferred-to-1104)) —
this is the producer's job, and `codexApprovalRequest` is the single pure
function where it happens, so a command containing a newline cannot forge a
second `cwd:` line or extend the path list.

`AlwaysAllow` reaches the modal only when Codex's own `availableDecisions`
for that request lists the string `acceptForSession` — on every 0.156.1
capture this was present for command requests and absent for file-change
ones — and only when `permbridge.SessionGrant`'s label (the command, or the
joined path list) fits its 1024-byte display cap; past that, the request is
still parked, just with no always-allow offered. `codexDecision` maps the
registry's `Verdict` back to Codex's vocabulary: `ForSession` allow →
`acceptForSession`, plain allow → `accept`, anything else → `decline`.
`cancel` is never produced — it also interrupts the turn, out of scope here.

`declineAll(reason)` is called from `runOnce` after the client is unbound
(`reasonCodexExit`), from `Interrupt` before `turn/interrupt`
(`reasonCodexInterrupt`), and from `BeginTeardown`
(`reasonCodexTeardown`) — every path the operator did not choose, on top of
the registry's own window timeout, funnels through the one-shot
`registry.Resolve`, so a race between one of these and an operator's answer
resolves exactly once.

**Known coverage gap, flagged on review rather than fixed (#2587's PR
verifier, SHOULD FIX, non-blocking):** no test drives `observe`'s
`item/started`/`item/completed` path-tracking and `handle`'s later read of
it end-to-end for a file-change approval — the table test for
`codexApprovalRequest` passes `paths` in directly. `fakecodex` has no
file-change approval marker and no capture has produced a real
`item/fileChange/requestApproval` (see § The capture's live gaps, above,
which flags the same absence for the translator). A regression in that
correlation would silently decline every Codex file edit with no test
catching it.

`TestCodexApprovalLive` (`cmd/pyry/codex_approval_test.go`) is the live
counterpart, on `gpt-6-luna` at effort `low`: a declined `touch` does not
run, an accepted one does. It is not reached by any dispatcher gate —
`make e2e-realclaude` never starts Codex — so it has to be run by hand with
`PYRY_CODEX_CAPTURE_BIN`/`PYRY_CODEX_CAPTURE_HOME` set, the same env pair
`TestCaptureLive` (above) uses.

### Reading the newest model per family on every spawn (#2627)

`Client.LatestModels` (`internal/codexsup/models.go`) reads `model/list` page by
page and folds each page into a table of at most `maxModelFamilies` before
asking for the next, so a family seen only on a later page is never dropped by
a page-cap failure elsewhere in the read. Grouping is on `id`, shaped
`gpt-<version>-<family>`; `slices.Compare` on the dot-separated numeric
components picks the newest (`gpt-6-sol` over `gpt-5.6-sol`), and an id with no
family, such as the reserve model, is left out. `codexRunner.readModels`
(`cmd/pyry/codex_runner.go`) calls it once per spawn, after the client is
bound so a turn is never delayed by it, and hands a successful read to
`modelVocabularyStore.RetainCodex` — see
[the store's Codex section](streamsup-package-retaining-the-decoded-model-list-for-the-session.md#holding-codexs-model-families-beside-claudes-2627)
for what happens to the result. A failed read is not retried or surfaced to
the caller; the runner logs and keeps whatever the store already holds.

**`parseFamilyID` takes everything after the version's first `-` as the
family, so a suffixed id takes its own slot.** `gpt-6-sol-2026-09-01` or
`gpt-6-sol-mini` would each consume a `maxModelFamilies` slot next to `sol`
rather than being recognised as a version of it. Nothing in today's catalog is
shaped this way, and code review flagged it as a non-blocking NIT rather than
a defect — the parse matches the id shape the ticket and plan both specify.
Revisit only if Codex starts shipping suffixed ids, which #2589 (the wire
exposure) is positioned to notice first.

**`readModels`'s own read timeout is the one failure its Warn cannot report,
and code review's SHOULD FIX for it shipped uncorrected.** The function
shadows its parameter — `ctx, cancel := context.WithTimeout(ctx, ...)` — so
`if ctx.Err() == nil` before the Warn tests the *read's* deadline, not the
runner's. That guard exists to suppress the Warn on an ordinary shutdown (the
runner context cancelled from outside); shadowing means a Codex server that
simply stalls `model/list` past `codexStartTimeout` is silently dropped with
no log line, contradicting the plan's own "timeout → error returned, runner
logs." The fix is to give the timeout context its own name and keep testing
the outer one. Non-blocking because the outcome — entries not refreshed this
spawn, previous ones kept — is unaffected either way; only the operator's
visibility into *why* is lost. Anyone debugging a Codex install whose model
menu never updates should check this before assuming the read never ran.

### Resolving the family to a version on every turn (#2628)

A session's stored Codex model is a family (`luna`), not a version. `cmd/pyry/codex_settings.go`'s
`resolveCodexModel(model string, families []turnevent.ModelOption) string` is
the pure lookup: it returns the first held entry's `ResolvedModel` whose
`Value` exactly equals `model`, provided that `ResolvedModel` is non-empty;
otherwise it returns `model` unchanged. An empty model returns at once. So an
unlisted version, a family with no entries held (nil or empty store), and an
entry whose `ResolvedModel` is itself empty all pass through untouched — a
model is never replaced by anything but its own family's resolution.

`codexRunnerConfig` carries this as `Families func() []turnevent.ModelOption`,
wired to `h.vocab.CodexModels` (nil-receiver-safe, the same pattern as
`Models: h.vocab.RetainCodex` above). `WriteUserTurn` calls
`resolveCodexModel(in.Model, r.cfg.Families())` on every turn, not once at
construction — resolving per call, rather than caching the resolution on the
runner, is what lets a `RetainCodex` that moves the family mid-session reach
the *next* turn with no respawn. The resolved value is what both `StartTurn`
sends and the translator's `SetModel` records, so the turn always reports the
model it actually ran on. `r.model` — the stored session setting — is never
written by this path, so the session keeps the family, not the version it
last resolved to.

**The fake Codex could not observe a reported model at all until this ticket
gave it a reason to send usage.** Before #2628, no fake turn sent
`thread/tokenUsage/updated`, so no turn's `TurnEnd` ever carried
`ModelWindows` (`Translator.holdUsage`/`turnCompleted` only populate that
field when a usage notification arrived for the turn — see § Per-turn usage
above). A test asserting "the turn reported model X" had no signal to read,
which is what this ticket needed to prove resolution actually happened. The
fix is the `[fakecodex:usage]` marker (documented in the fake's own header
comment): opt-in, so it sends one token-usage update before an existing
turn's normal completion and changes no existing `TurnEnd` assertion. Any
future test that needs to observe a turn's reported model needs this marker
on that turn.

## Testing

`client_test.go` builds `fakecodex` by import path, the same `TestMain`
pattern `fakecodex` itself uses to build against its own dependencies (see
[fakecodex-binary.md](fakecodex-binary.md)). Two harnesses:

- Against the fake binary: handshake version, thread start/resume,
  interrupt (needs `[fakecodex:hold]` — see fakecodex's own note on why an
  unmarked turn can't exercise interrupt), approval with no handler and with
  a deferred handler, and killing the child externally to prove `Done`/`Err`
  surface a real exit.
- Against an in-memory peer (`io.Pipe` pairs, no process): every frame the
  client sends is checked against the schema's `ClientRequest`/
  `ClientNotification` groups (the wire half of the method-name guarantee —
  `TestMethodNamesInSchema` is the static half, mirroring `fakecodex`'s own
  test of the same name), default declines for all ten `ServerRequest`
  methods table-driven, and a pending call across a peer close proving it
  returns an `ErrExited`-wrapped error instead of hanging.

**Method names checked against the schema said nothing about the params
inside them (#2631).** A golden string for `turn/start` or `thread/resume`
pins what the encoder emits, not what Codex accepts — a regenerated schema
that drops a `SandboxPolicy` arm or adds a required field to it would still
pass every golden-string test unchanged. `schema_params_test.go`'s
`TestRequestParamsMatchSchema` closes that gap: it drives every
`clientRequests` method through the real encoders (`turn/start` once per
combination of the `Approval*`/`Sandbox*`/`Reviewer*` constants, covering
every posture `codexTurnOverrides` can emit) and validates the captured
`params` bytes against that method's definition in the committed schema,
found through the same `ClientRequest.oneOf` walk `TestMethodNamesInSchema`
uses. The validator (`schemaCheck`) treats an undeclared `additionalProperties`
as `false` rather than JSON Schema's default of "anything goes" — deliberately
stricter, so a renamed field fails instead of passing silently — and fails
naming any keyword outside its handled subset (`$ref`, `type`, `enum`,
`properties`/`required`, `items`, `anyOf`/`oneOf`/`allOf`, `minimum`,
`minLength`) rather than ignoring it.

**`peer.read` forwarding a frame twice stalls the client with no clue why.**
Making the handshake's `initialize` params observable meant having `peer.read`
also send that frame to `p.frames` — but the existing `initialize` branch
already fell through to the function's own unconditional `p.frames <- f` at
the bottom via `continue`. Adding a second, explicit send instead of removing
that `continue` delivered every frame twice: `peer.next` then answers each
request twice, and the *next* call's response goes to the wrong reader,
surfacing as a deadline error far from the actual double-send. Any change to
a shared frame-dispatch loop like this one needs a check for an existing
fallthrough path before adding a parallel one.

No test in this file makes a live Codex call — the whole point of building
against the committed schema and the fake is that `client_test.go` runs with
no Codex account. `capture_test.go`'s `TestCaptureLive` (above) is the one
exception in the package, and it is skipped unless both capture env vars are
set, so `make check` still never touches Codex.

## Related

- [acp-package.md](acp-package.md) — the reused `Transport`; its classifier,
  `Responder`/`ErrDeferred` deferred-answer mechanism, and the "`Call` must
  not run on `Serve`'s goroutine" rule all apply here unchanged.
- [fakecodex-binary.md](fakecodex-binary.md) — the test double this
  package's tests run against, including the `json.RawMessage` framing
  pitfall for anything assembling a JSON-RPC frame by hand.
- `internal/codexsup/SCHEMA.md` — how the pinned 0.156.1 schema bundle is
  regenerated.
- [sessions-package.md](sessions-package.md) — `sessions.Runner`, the
  interface `cmd/pyry/codex_runner.go`'s `codexRunner` satisfies, and the
  eviction/activation cycle (`Session.runActive`) behind the thread-id lesson
  above.
- [streamsup-package-retaining-the-decoded-model-list-for-the-session.md § Holding
  Codex's model families beside Claude's (#2627)](streamsup-package-retaining-the-decoded-model-list-for-the-session.md#holding-codexs-model-families-beside-claudes-2627) —
  where `LatestModels`'s result is held daemon-wide, beside Claude's list.
- [sessions-registry.md](sessions-registry.md) — the `thread_id` schema field
  (#2622) this runner writes and reads on rebuild, and the rest of the
  on-disk registry format.
- [streamsup-package.md](streamsup-package.md) § Production wiring — the
  Claude analog (`streamsup_runner.go`'s `streamRunner`) the Codex runner's
  shape mirrors: one child at a time, teardown/rotation write gates, one
  shared turn sink.
- [ADR 038](../decisions/038-codex-daemon-owned-home-read-only-default-decline.md)
  — why `CODEX_HOME` is daemon-owned, and its "Superseded in part (#2587)"
  note for how the default-decline baseline changed once command and
  file-change approvals gained a real operator path.
- [permbridge-package.md](permbridge-package.md) — the registry and modal
  `codexApprovals` parks approvals in, `SessionGrant`, and why `Request`
  fields reach the modal unescaped (the producer's job, `codexDisplay` here).
