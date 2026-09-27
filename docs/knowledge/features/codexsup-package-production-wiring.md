# Codex production wiring

[Package overview](codexsup-package.md)

## Production wiring — the `cmd/pyry` Codex runner (#2620)

`cmd/pyry/codex_runner.go`'s `codexRunner` implements `sessions.Runner` and
supervises one `Client` at a time, the way `streamsup_runner.go`'s
`streamRunner` supervises one Claude child. `harnessRunnerFactory` routes a
session whose harness is `codex` to `newCodexRunnerFactory`; every other
factory case is unchanged. Turn events and the app-server's exit reach the
same `streamTurnSink` a Claude session feeds, through `sinkForTag`/
`exitForTag` on one live tag, so a Codex turn appears on a client exactly
like a Claude one.

**`codexRunner.State().ChildPID` now reports the app-server's pid while a
client is bound, not always 0 (#2663).** `codexsup.Client.PID()` returns the
app-server process's pid (0 for the in-memory test peer, since its `cmd` is
nil); `runOnce` sets `r.state.ChildPID` in the same `r.mu` section that binds
`r.client` and sets `PhaseRunning`, and clears it back to 0 in the section
that unbinds the client after `Done`/ctx. This is what lets
`activeSessionStarter.start`'s live-wrap-up gate ([Inbound new_session § The
wrap-up turn](v2-session-manager-state-machine-inbound-new-session-sessionstarter-seam.md#the-wrap-up-turn-and-the-replys-tense-2477))
actually reach Codex: before this ticket the runner always reported 0, so a
Codex reset silently took the synchronous rotation path instead — no
`resetting` frames, no handoff note, and nothing that read as an error.
`newCodexRunnerFactory` chains a `wrapUpCapture` into the Codex sink at the
same position `newStreamRunnerFactory` gives it (parser side of the fan-in
channel — see [the placement
rule](streamsup-package-announced-reset-follower.md#a-second-instance-of-the-placement-rule-wrapupcapture-2477)),
and `codexRunner.BeginWrapUp` arms it, mirroring `streamRunner`.

**A reset test that stubs `ChildPID` cannot catch this class of bug.** The
gap surfaced only in a test that read the pid off a runner built through the
real `newCodexRunnerFactory` against the fake Codex, not a hand-built double
with `ChildPID` set directly — a stubbed value exercises the wrap-up gate
without ever proving the production runner reports one.

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

### Codex questions reach the shared bridge (#2671)

`codexHomeConfig` enables `[features].default_mode_request_user_input` so
Codex can ask in its default collaboration mode. The daemon-owned home still
sets `approval_policy = "on-request"`, `sandbox_mode = "read-only"` and
`approvals_reviewer = "user"`. `Client.handshake` also declares
`experimentalApi`: the configured granular approval policy required that
initialize capability in a live Luna run. It admits experimental methods and
fields to the connection, so `serverRequests`' pinned method list and
`codexApprovals.handle`'s validation remain the admission boundary; a later
schema update must review both before adding a method.

`codexQuestionRequest` accepts only 1–4 questions with distinct Codex IDs and
distinct question texts, `isOther: true`, `isSecret: false`, and 2–4 complete
options each. It preserves question order, headers, text, labels and
descriptions while adapting each question to the bridge's `AskUserQuestion`
shape with `multiSelect: false`. `questionbridge.Parse` applies the shared
16 KiB whole-batch input bound. A malformed or ineligible request emits no
question batch or permission modal and takes the existing default decline.

The bridge validates the device answer against the parked batch and resolves
it by question text. `codexQuestionResponse` uses the parked, ordered
ID/text pairs to return one single-select value under each original Codex ID;
an incomplete or unexpected verdict returns `{"answers":{}}` as a whole.
Distinct texts matter as much as distinct IDs: without both checks, one
bridge answer could collapse two Codex questions or overwrite a response.
No Codex ID enters the mobile wire format.

Refusal, window expiry, interrupt and controlled teardown send empty answers
while the Codex request is writable, clear the batch and dismiss it once. A
spontaneous process exit still retires the batch locally. If
`serverRequest/resolved` arrives first, the runner dismisses the batch and
sends no response to Codex. `codexApprovals.await` remains the sole response
writer; response completion is independent of the initial question broadcast,
which may wait on the relay goroutine handling an interrupt. Terminal drains
close request admission and join writable question responses before interrupt
or input close. Keeping a request discoverable until its response completes
also lets a racing Codex withdrawal suppress a late write. These orderings
prevent a question from staying shown after teardown or losing its empty
answer when the client closes.

Question text, headers, option labels, descriptions and answer values cross a
subprocess trust boundary. They are displayed as untrusted text and never
logged by the production adapter; its retained ID/text correlation exists
only while the request is parked. See [Question in the mobile
protocol](../../protocol-mobile.md#question-v2) for the unchanged client
contract.

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

### Reading model families once at daemon start, before any spawn (#2664)

The Codex model menu used to stay empty until a Codex session actually
spawned. `startCodexModelRead` (`cmd/pyry/codex_runner.go`) now runs once per
daemon start, off the startup path: a goroutine bounded by
`codexStartTimeout` calls `readCodexModelsAtStart`, sharing
`startCheckedCodex` — the start/version/sign-in checks factored out of
`probeCodex` — then reads `model/list` into `RetainCodex`, as a spawn's own
read does (see above). Opens no thread, runs no turn. `runSupervisor` defers
the goroutine's `wait` (cancel + join) ahead of
`defer modelVocabularyStore.Close()`, so the goroutine always joins before the
store closes. A client connected before the read finishes sees the new
entries only on its next `request_model_list` — no push. Any failure leaves
the held entries unchanged and logs one Info line with no account or model
value.

**A `wait` that cancels before it joins can't also be the test's "read
finished" signal.** The first version ran the read inside the `go func`
itself, so a test driving it through `startCodexModelRead` and then calling
`wait` cancelled its own read (`initialize: context canceled`). The fix
splits the synchronous `readCodexModelsAtStart`, which tests call directly,
from the `go func` wrapper, which only adds the timeout and `wait`. Every
hermetic e2e daemon now spawns the configured Codex binary once at start —
a quiet-startup-log assertion must allow for the Info line above.

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

### The composed system prompt reaches Codex threads too (#2662)

Codex used to ignore the pool's composed-prompt file entirely: `Pool.SystemPromptFor`
reported the prompt as applied to a session while the Codex thread never saw it.
`codexRunner.runOnce` now reads that same file (`--append-system-prompt-file`,
recorded on `codexRunnerConfig.PromptFile` at factory time, fixed for the runner's
life) fresh on every spawn and sends its bytes as `developerInstructions` on
`thread/start` and `thread/resume` — the field rides `codexsup.Config`, so the
thread-open call signatures don't change. There is still exactly one compose
path: `Pool.writeComposedPrompt`/`refreshSystemPrompt`/`refreshSystemPromptForRotation`
write the file (operator prompt, attached clients, fenced handoff note); this
runner only reads it, the same rule a Claude child's `--append-system-prompt-file`
follows.

A missing, empty, oversize (> 256 KiB) or non-regular file sends no key rather
than failing the spawn — `readPrompt` opens the file `O_RDONLY|O_NONBLOCK` and
refuses anything `Stat` on the open descriptor doesn't report as a regular file,
so a FIFO planted at the path can't hang the spawn on the `Run` goroutine. An
oversize file is treated as a read failure (nothing sent), not truncated: a cut
could split the fenced handoff-note section and leave a dangling fence in the
instructions. Every failure logs the session id and the error (or a size-only
reason) and never the file's content.

**Telling a Codex thread open's own instructions apart from an unrelated one in
a pool-level test needs a stronger key than the instruction text.** The
pool-plus-factory test for this (`TestCodexRunner_ConversationPromptReachesThread`)
identifies which logged `thread/start`/`thread/resume` belongs to the session
under test by the `cwd` the client sends — read from the runner's own `cfg.Dir`
— rather than by matching on prompt content: filtering by instruction text would
silently hide a thread open that carried the *wrong* instructions instead of
failing the test.
