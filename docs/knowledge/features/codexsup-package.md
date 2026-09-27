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
client change: `item/started` is one `ToolStart` whose `Title` is always the
literal tool name — `"shell"` for a command, `"apply_patch"` for a file
change — never the command text itself. turnbridge copies `Title` straight
to `tool_use.name` and `RawInput` to `tool_use.input`/`input_summary`; a
title built from the command (#2668 found this the hard way) put the same
command in both fields, so a client showed it twice with no subject of its
own. Codex reports no tool name for either item, so the translator supplies
one for both, the same way it would for a Claude `Edit`. `RawInput` is
`{command, cwd}` for a command and `{paths}` — every changed path joined by
`", "` — for a file change; a file change also keeps one `Location` per
changed path for clients that read Locations instead of input.
`item/completed` is exactly **one** `ToolUpdate` — never a stream of them.
That constraint is why `item/commandExecution/outputDelta` and
`item/fileChange/outputDelta` moved to `ignoredMethods` rather than
`mappedMethods`: turnbridge turns every
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

The runner's `codexApprovals.handle` parks command and file-change approvals
on the daemon-wide `permbridge.Registry` and surfaces them through the
permission modal. It also adapts eligible `item/tool/requestUserInput`
requests to the question bridge. Invalid question batches and the remaining
unsupported server requests retain the default decline. See
[production wiring](codexsup-package-production-wiring.md) for both paths and
[ADR 038](../decisions/038-codex-daemon-owned-home-read-only-default-decline.md)
for the daemon-owned home and the decline-by-default baseline.

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

## Production wiring

See [Codex production wiring](codexsup-package-production-wiring.md) for the
runner lifecycle, posture, approvals and question bridge.

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
