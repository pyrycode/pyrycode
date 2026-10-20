# Captured Claude daemon tests

The recorded suite runs a real isolated daemon, a loopback relay and an authenticated test
client. Only the supervised Claude executable is replaced. Production parsing,
supervision, routing, permission handling, turn emission and delivery still run.
The test-only wrapper and recorder are not imported by the shipped daemon.

Each manifest scenario runs the same assertions in live capture and offline replay.
Use `python3 scripts/daemon-recordings.py inventory` for current scenario counts and
original live-root associations.
The legacy `question-answer` and four bypass-question recordings retain their
original assertions and fixtures without owning current live roots. Five new
witness profiles share the stronger proof introduced by PR #3622: a fresh
answer-only named-cache witness, exact durable question/answer values, final
owned idle state and a controlled post-model turn. The two primary question roots
use non-bypass stdio permissions. All five new profiles use the original explicit
Sonnet model; the parked question changes to Haiku before the follow-up turn.
Live runs generate unpredictable witnesses and check physical Bash effects;
recordings use fixed witnesses for exact request comparison. The original
negative controls for transport, history, witness leakage, model ownership and
parked-question preservation remain enabled and unchanged.

- `two-turn`: two separately accepted user messages each produce nonempty text
  followed by terminal idle in the correct conversation. Wording is unrestricted.
  This checks sequential delivery, not semantic recall or response chunk counts.
- `permission-deny`: a real Write permission request surfaces to the client;
  its tool, target path and conversation are checked before explicit rejection
  dismisses that modal with remote attribution and the conversation completes.
  The shared live driver also rejects bounded retries and requires matching
  remote dismissals for every decision and exactly one durable deny fact with
  the asking session.
  Live capture also checks that the denied file does not exist. Replay cannot prove that Claude obeys a denial or performs no IO.

- `reply`: a cold reply passes the same completed-turn drain used by
  `TestInteractiveStreamLiveness`: nonempty attributed text then idle, with no
  wording or chunk-count requirement. Parser gaps and unknown rate-limit statuses
  remain global alarms.
- `question-answer`: a nonempty answerable batch parks while the model changes;
  the update reply retains request/session correlation, every question gets its
  last surfaced option, and the original batch is remotely dismissed as answered.
  Continuation must name that chosen option first among the offered labels and
  reach idle. Raw durable history must contain exactly one attributed, visible
  answer fact with bounded context and the asking session. A new application turn
  must announce the selected Haiku model and complete.

- `permission-allow`: the matching remote allow dismissal, nonempty continuation
  then terminal idle, and exactly one durable allow fact with the asking session.
  The lower capture boundary verifies the actual Write tool and target path;
  live capture additionally reads back the exact file bytes. Replay cannot prove
  that a tool actually wrote the file.
- `question-refuse`: a surfaced batch is explicitly refused, dismissed as
  `{refused, remote}`, reaches terminal idle and persists exactly one refusal
  fact with the asking session. The shared driver refuses at most two re-asks
  and allows at most four post-refusal permissions, requiring matching remote
  dismissal of every decision. This preserves the live refusal test's independent
  absence witness: permissions must not silently block forbidden work for it.

- `permission-cancel`: the original live Write trigger surfaces an owned
  permission, then `modal_cancel` must produce a matching `{cancelled, remote}`
  dismissal and an owned `turn_end`. The shared drain preserves the live
  30-second dismissal budget and 60-second warm completion budget starting at
  dismissal, and retains completion if its independent broadcast arrived first.
  An idle frame or another conversation's turn cannot substitute for completion.
  Exactly one durable cancel fact must retain the asking session. Live capture
  and the original live gate also require the denied file to be absent. Replay
  cannot establish actual filesystem effects or elapsed provider time.

- `send-now-placement`: the original foreground Bash trigger starts a busy turn;
  the client queues the marker text, takes its surfaced queued identity and sends
  it now. The single arrival log must contain one turn end and one exact user
  push after the matching Bash result and before final assistant text containing
  the marker, no parser gaps, and an empty final queue. The three-second late-frame
  window also rejects delayed duplicate messages or extra turn ends. Both live
  and recorded callers use this same driver. Real Bash execution, current Claude
  interpretation and elapsed provider time remain live proof.

- `model-announced`: one text turn must expose the exact resolved Haiku model,
  distinct from the spawn alias, in an owned announcement; the observed value
  must be absent from the verbose daemon log. Nonempty reply and terminal idle
  are both required. Current provider resolution remains live proof.
- `model-rejection`: read a complete current menu with no dropped rows or truncated
  values, derive an absent model, and reject the correlated settings update as
  non-retryable `protocol.malformed`, including its co-submitted effort/posture.
  A fresh turn must still announce the original model and reach terminal idle.
- `tool-turn-clean`: the original Write/Read trigger and completed-turn drain keep
  parser-gap and unknown-rate-status alarms. Owned matching Write and Read
  use/result pairs make the sample non-vacuous in live capture and replay.
  Real filesystem effects and newly introduced provider shapes remain live.

- `family-alias`: the complete menu has one row per family; two completed turns
  must announce a nonempty baseline and then the selected family's exact model
  after a model-only pinned-ID request. The family rule is checked independently
  of production rewriting. Current provider menu/resolution stays live.
- `attachment-read`: upload one declared, hashed chunk before routing any turn,
  require its correlated stored receipt and the actual on-host directory/file,
  then send the attachment ID with a prompt that withholds its contents. Owned
  accumulated text must contain the file-only token before terminal idle; errors
  and modals fail. The live caller mints a 96-bit random token and the recorded
  caller uses a fixed fixture token. Unpredictable real file-opening proof stays
  live; the daemon upload and storage checks also run during replay.
- Four `bypass-questions-*` cases cross stored/operator bypass with the existing
  stdio-permission configuration off/on. Every case answers one single-select
  and one multi-select batch, including custom free text, checks continuation
  choice ordering and exact answer history, runs the ordinary Bash witness turn,
  and confirms the child still reports bypass. Both original live observation
  and capture/replay independently require init to offer AskUserQuestion in
  bypass, exactly two uniquely identified asks, at least three completed results
  and no host approval for ordinary tools. Physical Bash effects and unpredictable
  labels/free text remain live proof.

The `workspace-protocol` batch adds nine cases:

- `workspace-change` checks correlated workspace changes, canonical returned and
  persisted directories, and delivery of a second turn in the same conversation.
- `skill-silence` requires the skill's file-only reply token, the known harness
  drop witness, and absence of the skill body marker from daemon logs.
- `confirmed-permission-settings` checks initial bypass, correlated confirmation
  of default mode without a child restart, and a real Read permission round trip.
- `default-outside-read` exercises the default-mode acknowledgement before the
  first user input and the shared outside-workspace Read drain. The live test
  retains its version-specific finding and unpredictable file-opening witness.
- `mcp-reconnect` requires both production MCP servers to become healthy, a
  deliberately broken named server with an error, and a correlated reconnect
  after repairing its command. Replay checks the daemon protocol; actual MCP
  process startup and command repair remain live proof.
- `permission-context-absent` and `permission-context-outside` compare optional
  context fields against native request bytes, including absent versus null,
  reconnect the phone and require the identical modal, then deny it and correlate
  the native denial result. The actual target remains absent in live runs.
- `native-suggestions` requires source-native suggestions, a nonempty owned set
  with a positive revision, and a higher-revision explicit-null clear on the next
  user message. Continuous reads preserve ordinary no-suggestion windows. The
  final typed user input must reach Claude stdin before sealing. The native
  wrapper refuses non-stream invocations before producer admission, matching the
  original live source-isolation rig, so fallback attempts cannot produce output.

- `hook-banner` retains the five-second no-read refusal window, exact next-send
  ACK barrier, subsequent text/idle milestones and all six durable banner joins.
  Native source order must place the refused result before the liveness input.
  Installed user settings, hook script bytes and modes are exact inputs. Actual
  hook execution and original conversation creation remain live proof.

Subagent text retains its live main/child identity and sequence assertions. Its
first-client-delta checkpoint lets the production coalescer flush a main delta
before receiving the remaining text during replay.

These profiles retain the working directory as an exact input. The skill's full
`SKILL.md` and native non-stream rejection policy and enable setting are captured
and compared. Permission-context and native-suggestion checks observe selected
native source objects through a bounded loopback channel in memory; this does
not replace the daemon's parsing or client delivery. Old profiles keep their
previous input inventories and fixtures.

`make e2e` includes these cases in the ordinary offline integration selection.
`make e2e-recorded-claude` runs just the recorded suite. Missing fixtures fail; nothing
falls back to Claude. Replay removes provider credentials and uses disposable
HOME and test pairing state. The daemon is expected to execute during replay.

## Commands

```sh
python3 scripts/daemon-recordings.py inventory
python3 scripts/daemon-recordings.py replay --output /absolute/new/replay-reports
python3 scripts/daemon-recordings.py capture --output /absolute/new/capture
python3 scripts/daemon-recordings.py verify --output /absolute/new/compatibility
```

Capture and verification require explicit `CLAUDE_CODE_OAUTH_TOKEN` or
`ANTHROPIC_API_KEY`, a Claude executable, clean committed source and a daemon
binary stamped with that exact revision. Use the configured dedicated test login;
do not put credentials into arguments or fixtures. Output must be new and outside
the checkout. Failed attempts remain separate evidence. Capture never overwrites
a fixture and replay never creates one. Inspect successful candidates before
copying them into `internal/e2e/testdata/claude-tapes/` and committing them.

The wrapper records complete JSONL objects on both pipes before forwarding them.
Every forwarded frame is recorded first, in order, with an 8 MiB / 10000-frame
bound. The successful driver seals the captured prefix before teardown; later
process activity is outside this pilot. The helper drains all readable daemon
stdin before sealing and rejects a pending partial line; the final nonblocking
drain defines the captured prefix. Paths inside the disposable HOME bind
to `${PILOT_HOME}`. The test daemon executable, its control socket and generated
MCP configuration filename have their own explicit path bindings. Complete
settings, appended system prompt and MCP configuration file contents are also
retained and compared. There is no arbitrary prose scrubber or field allowlist.
Known secret patterns, configured credentials and unnormalized operator home
paths are rejected without echoing the payload. Exact credential-source labels
in `tokenSource` and `apiKeySource` metadata are safe names; the scanner accepts
those two known labels without altering captured bytes or permitting values. Raw transcripts are never written. Candidate files are private.
A successful scenario explicitly seals its stream before teardown; incomplete
or failed scenarios cannot publish a candidate. The pilot allows one Claude
process per scenario and does not model respawns or resume files.

A tape records source revision, daemon executable SHA-256, observed Claude
version, UTC capture time and complete spawn arguments. This provenance describes
the producer, not a claim of fresh compatibility on subsequent replays.

## Replay and fresh compatibility

Replay compares complete spawn arguments and daemon requests, including unknown
extra fields, exact user text and permission choices. JSON object key order is
irrelevant. Only daemon-minted control request IDs may vary: they bind one-to-one
and the corresponding recorded response IDs are rewritten consistently. IDs
minted by recorded Claude permission requests remain exact. Values in ordinary
text are never rebound. Recorded outputs wait behind their preceding matching
inputs. Changed, missing or extra input, wrong correlation, unconsumed frames,
invalid provenance and loss all fail. Diagnostics contain positions and counts,
not application payloads. Original timing is not replayed.

Verification separately reports fresh live capture, committed replay, fresh
replay and outgoing request comparison under each explicit manifest policy.
`strict_ordered` binds observed control IDs by producer direction and ordinal and
retains complete ordered requests. Only `native-suggestions` declares
`native_coding_prefix`: initialize, one to six exact source-declared coding
prompts each immediately followed by one context query, then the exact clear
input. The policy validates both entire traces independently and compares full
initialize, user and context-query templates, including unknown fields. Only
unique nonempty top-level control IDs and the checked prompt text slot vary;
extra, missing, reordered or malformed requests fail. Reports name this policy
and both coding iteration counts. It does not claim identical request counts.
Replay itself still matches every stored input exactly, and the native source
suggestion and client set/clear assertions remain required.
Different provider wording and accounting do not themselves fail this comparison.
Changed user choices, commands, recovery steps and request payloads do. Every
stage must execute every selected named leaf with no failures or skips; default
selection includes the full manifest. A failing stage
cannot produce a successful compatibility summary, even if the others passed.
Verification never replaces the reviewed baseline.

## Migration inventory and remaining live proof

`inventory` lists static test declarations under `internal/e2e/realclaude`,
separating the live-tagged files from offline evidence validation. It includes
opt-in probes, so those declaration counts are not executed acceptance counts.
The first migration moves reply and question protocol assertions into
`internal/e2e/internal/scenario`. Existing live callers and recorded tests share
those drivers, including history validation. No live test is removed or retagged,
preship remains unchanged, and dispatcher routing is unchanged.

| Existing live contract | Offline owner | Retained live proof |
| --- | --- | --- |
| `TestInteractiveStreamLiveness` | `reply`, same completed-turn driver | Current provider compatibility |
| `TestInteractiveStreamQuestionAnswer` and stdio counterpart | `question-answer`, same question/model/history driver | Unpredictable question labels and current Claude interpretation |
| Three-turn memory continuity | None in this batch | Actual Claude context retention |
| Question refusal | `question-refuse`, same refusal drain and history checks | Unpredictable question labels, blocked Write absence and actual provider refusal behavior |
| Permission cancellation | `permission-cancel`, same cancellation/history checks | Denied-file absence, real parked-completer timing and current stdio behavior |
| Model announcement | `model-announced`, same first-announcement/reply/idle and verbose-log assertions | Current alias resolution and provider behavior |
| Absent-model rejection | `model-rejection`, same complete-menu/rejection/retained-model driver | Current advertised menu and provider interpretation |
| Tool-turn parser sample | `tool-turn-clean`, same completed-turn alarms plus owned Write/Read round trips | Actual filesystem effects and current provider line vocabulary |
| Send-now queued placement | `send-now-placement`, same queue/action/order driver | Actual foreground Bash execution, current Claude interpretation and elapsed/settle time |
| Permission allow/deny | `permission-allow` and `permission-deny`, same resolution/history drivers | Actual file creation/absence, current approval transport and settings-respawn behavior |

The recorded question trigger supplies a complete fixed AskUserQuestion input so
fresh daemon requests remain exactly comparable, including question keys and
option labels. The action still selects from the surfaced batch. Existing live
question triggers name no option, preserving their stronger interpretation test.
The compatibility comparator does not scrub provider prose or loosen request
comparison for these cases. Dismissal and terminal idle may arrive in either order;
both are required after the matching continuation.

The reviewed backlog includes ordinary queued placement, further model-control
messages and thread folding. `reviewed_migration_backlog` keeps candidates,
checkpoint work and harness work visible even when no eligibility review remains.
Classification does not count unsupported scenarios as migrated. Each must
preserve its original assertions and account for all external inputs before promotion.
Crash/restart, session files, real tool effects, signal handling, transport
recovery, provider compatibility and measured timing retain explicit live or
other dedicated deterministic integration proof. They cannot be represented by
playing stdout alone. Desktop and Android daemon-message recordings cannot
substitute for this lower boundary: doing so would bypass daemon behaviour.

The refusal capture uses the same complete question schema as the answer capture,
with a fixed instruction for refusal. It exercises daemon routing and persistence;
the original live refusal trigger still blocks a nonce-named Write behind a real
answer and retains the non-vacuous filesystem absence check. No live requirement
is removed or retagged. The shared permission and refusal drains collect dismissal
and idle in either order, but cannot pass with an unresolved sent decision. Allow
still needs text before idle; deny and refusal retain their original ability to
finish silently. Retry caps remain four permission retries, two re-asked questions
and four post-refusal permission allows.

## Batch migration tooling

`internal/e2e/testdata/claude-scenarios.json` is the shared source of scenario
names, stdio configuration, explicit bootstrap Claude arguments, original live
owners, retained live obligations and
named batches. Both the Go tests and Python runner read it. A driver without a
manifest entry, an entry without an implemented driver, an empty/unknown/duplicate
selection, or missing/extra/duplicate/skipped execution evidence fails. Default
selection remains every scenario; normal `make e2e` and `make check` retain all
recorded coverage. A selected batch never narrows the full regression gate.

```sh
python3 scripts/daemon-recordings.py inventory
python3 scripts/daemon-recordings.py plan --batch queue
python3 scripts/daemon-recordings.py replay --batch permissions --output /outside/new/replay-reports
python3 scripts/daemon-recordings.py capture --scenario permission-cancel --output /outside/new/capture
python3 scripts/daemon-recordings.py batch --batch all --agents-repo /path/to/pyrycode-agents --output /outside/new/batch
```

`batch` audits the manifest, performs fresh capture, committed replay, fresh
replay and declared-policy request comparison, then runs `make check-common` **once for
the entire selected group**, its mapped live test roots once, the complete
201-cut restoration proof once, and citation/documentation guards once. Live
roots and restoration cuts are counted from Go JSON events: exit zero, skipped
cases or an empty run cannot substitute for evidence. Failed stages stop later
gates and retain their reports and private logs. The summary records source
revision, manifest identity, individual stages, selected cases and retained live
obligations. Every fresh producer must match the pinned clean source revision;
changing HEAD or leaving source changes during gates fails completion. The
existing shared host admission launcher serializes expensive stages and its
inherited descriptor is preserved. Set `AGENTS_REPO_PATH` or use `--agents-repo`
for a checkout where the sibling agents repository is elsewhere. Provider
credentials and `CLAUDE_CONFIG_DIR` are removed for replay/offline gates.

### Inventory counts and review boundaries

The standard-library Go AST reader enumerates declarations and package-function
references, including callbacks passed as function values; Go build expressions
are evaluated rather than inferred from filenames or a line-prefix regex. The
Python analysis reports possible paths to configured provider prerequisites or
literal Claude executable calls, process-helper entries, and entries with no
known provider path. **No known path is not an offline-safety proof.** Methods,
dynamic dispatch and external packages are not type-resolved; findings are
conservative source evidence, not executed acceptance or automatic eligibility.
Diagnostic probe environment gates are names only, never values.

Reviewed manifest dispositions distinguish shared replay assertions, candidates,
checkpoint work, additional harness work, controls and live-only proof. Everything else remains explicitly
unreviewed. Counts of recorded scenarios, shared live entrypoints, static test
declarations and remaining eligibility reviews are separate; the total eligible
migration count stays unknown until reviewed. No live requirement is retired.

Review fingerprints include reachable package function bodies plus conservative
package declaration/method context and the imported `scenario` and `claudetape`
non-test source, including native bypass source audits.
Changing a timeout constant, model table, harness global or shared assertion
invalidates the review as well as changing a function. Inventory reports stale
reviews, and plan/scaffold/batch refuse to proceed. A maintainer reviews the new
boundary and records the new digest from inventory; the tooling never silently
refreshes its own approval metadata. Context is deliberately conservative and
can require re-review after an unrelated declaration change.

### Generate a reviewable proposal

```sh
python3 scripts/daemon-recordings.py scaffold \
  --name send-now-placement \
  --live-test TestRealClaude_SendQueuedNowPlacement \
  --retained-live 'Actual Bash execution and elapsed/settle time remain live' \
  --output /outside/new/proposal
```

This creates a Go registry adapter, manifest fragment with current source
fingerprints, and a review checklist in a new directory outside the checkout.
The adapter calls `t.Fatal` until the shared driver is implemented. It is a
proposal, never counted as a migrated scenario. It refuses unsafe names, missing
live owners/obligations, already-owned or live-only entries, and existing output
directories. Use `--stdio-permission` when the reviewed scenario requires that
configuration. Review the actual spawn/bypass settings and all external-input
checkpoints rather than inferring them from the generated stub.

After implementing the shared driver and merging its manifest entry, commit clean
source, capture the named candidates into a new directory, inspect their privacy,
provenance and complete inputs, then commit the inspected fixtures and run the
batch workflow. Capture never overwrites a baseline and neither scaffolding nor
batching removes or retags a live test. Semantic assertion mapping, physical/time
witnesses and candidate promotion remain explicit code-review work.

### Send-now input checkpoint

The send-now tape is accepted for capture or replay only if its exact queued
text appears once on daemon stdin after the first Bash tool-use start and before
that tool's matching result on stdout. Partial tool-use starts and complete
assistant blocks are correlated by tool-use ID. This is the causal boundary that
holds fast replay while the client queues and accelerates the message; a delay
or recorded timestamp cannot substitute for it. Controls reject early, late,
missing, duplicated, foreign and output-only substitutes for that input.

The manifest preserves the original live harness's explicit `--model haiku
--dangerously-skip-permissions` bootstrap arguments. This operator bypass is
needed for the foreground Bash call to run without a permission modal. All
previous scenarios retain empty extra arguments. Every supplied settings,
system-prompt and MCP configuration file is captured and compared; the bypass
spawn has settings and system prompt but no approval MCP file. Missing a file
from an existing tape's input inventory still fails replay and fresh comparison.
The recorded receive adapter returns an empty envelope only on the deadline,
preserving the shared driver's late-frame window and transport failures.

The live send-now test still supplies a fresh run nonce. Recorded requests use a
fixed run suffix so their complete stdin contracts remain comparable; the shell
command, marker and all queue/order assertions are unchanged. The ordinary-queue
live test uses the extracted classifier with its original assertions intact.
Ordinary queue admission does not itself write Claude stdin. Its separate
client-action checkpoint profile below records the actual client observation.

### Three-scenario protocol batch

`--batch basic-protocol` groups `model-announced`, `model-rejection` and
`tool-turn-clean`. Implement and check each shared driver with focused controls,
then capture and review the three candidates together. After committing only the
new inspected fixtures, run the batch once: fresh capture and per-case contract
comparisons, one full offline regression, one mapped live selection containing
all three original roots, one 201-cut restoration and one guard invocation.
The full offline gate still exercises every manifest scenario. All eight earlier
fixtures remain unchanged; no live tests are removed or retagged.

These cases need no queue checkpoint or process-lifecycle support. Announcement
and tool-turn spawns retain the live explicit Haiku/operator-bypass arguments;
model rejection keeps Haiku with normal approval configuration. Live prompts keep
fresh nonces, while recorded prompts use fixed run suffixes. Model-menu retry
counts are not provider requests: complete ordered daemon stdin still faces the
existing exact comparison. A changed provider menu that changes a submitted model
or another request must fail compatibility and be reviewed.

Extracting the announcement drain also affects existing family-alias consumers.
The helper retains its first-announcement, owned nonempty text and terminal-idle
milestones. Each new announcement consumer additionally requires the announcement
itself to belong to the driving conversation. Controls reject missing or wrong
models, alias echoes, foreign attribution, log leaks, incomplete menus, wrong
request correlation, retryable/wrong rejection codes, changed post-rejection
models, missing/foreign tool results and parser/rate-limit alarms.

### Six-scenario extended batch

`--batch extended-protocol` selects family selection, attachment reading and all
four bypass-question configurations. These are **six scenarios covering three
original live test roots**; the bypass root itself has four configuration subtests
and two question turns plus an ordinary-tool turn per configuration. Scenario,
root and executed-subtest counts are kept separate. The group shares one complete
regression and one 201-cut restoration, and its live selection executes all three
original roots. Default offline selection includes all seventeen scenarios.

The manifest's explicit `initial_yolo` flag seeds only the disposable test
bootstrap registry, using the original live stored-bypass setup. Operator-bypass
variants instead retain the explicit CLI flag. All eleven prior entries stay
false and preserve their arguments/fixtures. The scaffold defaults the new field
to false; malformed or implicit configuration is rejected by the Python audit.
The stdio configuration labels describe the selected harness configuration;
bypass runtime routing still follows production's stdio ownership rule.

The bypass source audit runs against the original live observer's selected source
frames and the complete recorded stdout. Capture must pass it before publication,
and every replay rechecks it before releasing frames. The ordinary file witness
is always checked in the original live test and fresh capture; replay cannot
claim real Claude Bash execution. Question histories and session settings are
read from the real isolated daemon in every mode. Fixed recorded question inputs,
free text and witness bytes preserve complete ordered request comparison.

The attachment source file lives in a sibling test directory, outside the daemon
HOME/workdir. Its read-back bytes determine the chunk size and SHA, and the shared
driver checks the actual stored directory before sending its first routed turn.
A fixed replay token establishes deterministic protocol/data assertions only;
it does not replace the original live random-token non-guessability claim.
No dynamic session-ID rebinding, multi-process playback, queue checkpoint support,
production changes or live-test retirement are introduced by this batch.


### Client observation checkpoint batch

`--batch client-checkpoints` groups ordinary queue placement, running and initial
thinking interrupts, and subagent text. These four profiles alone use version 2
tapes, with exactly one typed `client_checkpoint` in addition to native stdin and
stdout. Legacy profiles remain version 1. No checkpoint is written to Claude
stdin or allowed to rebind native identifiers. The strict comparison includes
the complete action object in its ordered request sequence.

The driver publishes only after checking the owned queue admission, requested
interrupt phase, or first owned nonempty main text delta with a valid initial
sequence/turn identity. Replay holds its output tail at that action until the
same real daemon/client observation occurs. Capture polls actions without
waiting or suppressing native stdout. Native source audits require queue
admission between Bash use and matching result, interrupt input between phase
observation and the cancelled result, or nonempty native main text on both
sides of the client main-delta observation. Early/late checkpoints fail capture;
a timestamp, sleep, retrospective marker or provider stdout alone cannot replace
the client observation.

Each run uses a new private action directory. Closed canonical files are
published exclusively and acknowledged only after capture append or successful
replay matching and output delivery. Before save or successful receipt, the
helper requires complete input consumption and exactly one accepted action,
atomically revokes the publisher's directory pathname and audits the frozen
exact ready/ack inventory. Missing, extra, partial or changed action evidence
fails. The daemon pipe helper allows thirty minutes, preserving all original
cumulative phase budgets. The selected serial recording and live cohorts allow
180 minutes overall; individual driver phase deadlines remain unchanged.

Physical Bash execution, measured queue duration, actual provider interruption
and latency remain live obligations. Every original live root stays enabled
with its original trigger, budget and assertions; live callbacks are omitted.
Subagent replay preserves the requirement for at least two main lane deltas,
nonempty child text and distinct stable main/child turn identities.


### Direct pool control batch

`--batch pool-controls` covers model changes, stored bypass revocation,
re-escalation and the spawn posture gate. These profiles declare `pool_stdio`
and run the complete original live test bodies in a separately compiled test
binary. Their pool, concrete runner, raw native control/init taps, response
counts, operator provenance, model/menu resolution, unchanged supervised PID and
spawn-count assertions all remain in place. Only the executable/isolated HOME
setup acquires an optional recording adapter. Normal live runs use the original
credential setup. No test is removed or retagged.

The single fresh provider child is captured behind the original runner's pipes.
Capture refuses an external `CLAUDE_CONFIG_DIR` and the closed argv profile
rejects new or unknown flags until their input contracts are reviewed.
Replay uses no provider credentials/configuration and never starts Claude.
The profile refuses resume, continuation or a second child. Every supplied
settings, system-prompt or MCP file is included with its complete content and
mode; settings are mandatory. Complete argv and cwd remain compared. The
manifest's daemon seed/extra-argument fields stay unused and false/empty for
pool profiles: their original test bodies compose the actual spawn configuration,
which is recorded and checked at the lower pipe boundary.

A typed identity binding permits only the exact `--session-id` argument and
matching top-level native `session_id` to use the fresh pool session identity.
System-init cwd metadata binds only to the declared, checked actual working
directory. Nested identities, user/tool text and all other fields stay literal.
File argument paths are explicit file handles with independently compared
contents/modes, never recursive path scrubbing. Unexpected private paths fail
capture. The helper checks the original runner's actual cwd and HOME and inherits
them when starting native Claude; it cannot repair an incorrect environment.

The original root must run exactly once and pass, with no skipped/failing event.
Before runner cleanup, its recording must seal with complete input consumption;
source failures cannot publish successful candidates. Capture provenance names
the compiled original-test binary's checksum and pinned clean source revision.
Error-only version diagnostics use the same capture's provider version. Private
original-root JSON events are retained alongside each stage's reports. The four
serial pool roots receive adequate whole-run limits; bounded native pipe helpers
allow twenty minutes per root. Real provider application of the controls remains
an explicit live obligation.

### Combined migration batch

`--batch remaining-protocol` groups the nine workspace cases, four client
checkpoints, four pool controls and five stronger question witness profiles.
The automation checks all 22 fresh captures, saved and fresh replay, exact
request contracts, original mapped live roots, the full common regression,
201 restoration cuts and documentation guards once per stable source revision.
Fresh candidates require independent privacy, provenance and boundary review
before fixture promotion. A failed run remains separate evidence.

### Dynamic conversation process roles

`--batch dynamic-conversations` runs four original daemon test roots: default
conversation liveness, workspace-folder liveness, owned turn
state and per-session prompt-file argv. Each root still creates its conversations
over the real client protocol and retains its original assertions and budgets.
Live runs keep their random nonce; recording runs use a fixed test nonce for
strict request comparison. No created conversation is replaced by a seeded row.

A closed suite inventory assigns the bootstrap child and its created
child separate tapes. The creation reply must match the persisted conversation
and its independent session before the next user message can launch that child.
Role identities cannot be reused or selected by scheduling/launch order. Missing,
extra, duplicate or rebound roles and unknown invocations fail. Every child
captures complete arguments, settings and appended-prompt input contents and
modes, plus every supplied MCP input (the bypass launch supplies none). Session binding applies only to its exact CLI slot and native top-level
session field; ordinary session-looking text stays literal. Declared HOME,
daemon and control-socket paths have explicit filesystem bindings.

Capture provenance identifies both the compiled original-test binary and the
source-stamped daemon checksum. Child streams seal before daemon cleanup; the
parent publishes the aggregate candidate only after exactly one original root
run/pass and successful cleanup, with no skipped/failing event. Suites reject
duplicate JSON keys, unknown fields, changed role inventory and aggregate byte
or frame limits. An immutable receipt pins all role artifacts; the parent repeats
the inventory/hash audit after original cleanup and rejects late helpers, extra
claims and changed files. A sealed wrapper stays alive for at most thirty seconds
through cleanup to avoid causing a respawn. Activity after the sealed prefix is
outside this recording proof. Replay runs the actual daemon without provider credentials or
Claude; strict comparison checks each role independently. Current Claude startup,
interpretation, physical effects and provider timing remain live obligations.


### CLI process and transcript recordings

`--batch cli-process` proposes thirteen original `pyry agent-run` roots covering
ASCII/Unicode prompt fidelity, doctor contamination, nonzero Bash, turn limits,
large prompts, long sessions, tool loops and five dispatcher role stand-ins.
The original bodies and their transcript readers remain the acceptance tests.
A private HOME and a native CLI executable wrapper are the recording seams.
The actual daemon binary and original test binary have independent source and
checksum provenance. No fixture is accepted merely because its helper completed.

The CLI boundary checks the full native argv, every supplied input file and
mode, and the complete workspace file inventory. Only `.pyry-test-recording`
contains excluded test bookkeeping. It records the one actual JSON request and
its EOF, both native output streams, the process exit status, and the actual
session JSONL bytes. Path bindings preserve Unicode and literal JSON escaping.
Replay waits for EOF, rejects changed requests/files and duplicate invocations,
never starts Claude, and installs the captured session file before returning
its captured exit status. The original tests then use the real transcript reader.
Publication requires exactly one successful original root, no skips/failures,
and a final private artifact/receipt/hash audit after original cleanup. Fresh
comparison checks all outgoing inputs; current provider interpretation, real
execution, context limits, and role behavior remain live obligations.

The active-switch daemon prototype remains pending: the first completed turn
can launch an independent fallback-suggestion print process while the second
conversation runs. The recorder rejects that unowned invocation. Supporting it
requires capturing the print request and its causal connection to the first
reply, including cancellation; it must not be suppressed for recording.


### Direct runner query and parser profiles

`--batch direct-runner` proposes the original two-arm applied-settings query and
compacting-edge collector roots. The production runner/parser and independent
raw `response.applied` tap remain inside the compiled original test binary.
That binary is the runner checksum owner; these roots do not claim to use an
otherwise unused daemon executable. Inherited and explicit settings arms have
separate fixed session roles. The explicit effort still comes from the inherited
arm's actual initialize menu, and explicit-null effort remains distinct from a
missing field. Compaction keeps both original priming turns, the original compact
request, event ordering, complete boundary fields and zero-gap assertions.

Each role has closed argv, no input-file flags, and an empty controlled workdir
whose relative path and mode are checked. Applied-settings retains its original
empty `--setting-sources` value; provider-created HOME state between its two arms
remains a current-provider obligation. A role seals before its original runner
cancellation, and its wrapper waits boundedly through that cancellation. The
original root and cleanup must pass before publication. Exclusive claims, full
role inventories and immutable per-role artifact hashes reject additional,
missing, failed, modified or late invocations. Replay has no Claude credentials
and never launches a provider. Whole-role request comparisons preserve every
control query and complete outgoing JSON field. Provider compaction behavior,
settings defaults and menu accuracy remain covered by the original live roots.


### Same-daemon error recovery

`--batch session-recovery` proposes the original retained/dropped session-error
root. Its deliberately failing executable still closes stdin, advertises
readiness, writes its original failure marker and exits through the original
external gate. Production retry/backoff and the dropped arm's original
three-second give-up window remain unchanged in capture and replay. Only the
final recovered Claude child is recorded, once per independent retained/dropped
HOME. The original root still decrypts all queue/error frames, checks daemon and
session identity, distinguishes held versus fresh delivery and reads its JSONL.

A private parent builds the same bridge-enabled daemon tag as the original root;
its actual source stamp/checksum and the compiled original-test binary identify
the capture. The normal live root still builds its own bridge-enabled daemon and
ignores the ordinary `PYRY_E2E_BIN`. Role selection uses the exact owned HOME,
never retry count or launch order. The original failing script is not replaced
by a tape or counted as a recorded Claude launch.

After the root itself observes completion, correct delivery and an empty queue,
a recording-only hook captures the actual native JSONL before the unchanged
marker checks. Replay restores those exact bytes and mode at the production
session path, preserving Unicode and escaping with only declared path bindings.
Each JSONL object retains its actual session identity. This is a captured file
witness; current Claude startup, interpretation and physical transcript creation
remain obligations of the original live run. Settings, appended prompts and any
supplied MCP file remain complete inputs to the strict native pipe recorder.

Each role seals before original daemon cleanup. A final exact private inventory
pins its endpoint, request tape, transcript, exclusive claim, receipt and seal
across that cleanup. Candidate publication requires exactly one run/pass for the
outer root and both subtests, no skipped/failed event, matching source checksums
and successful post-cleanup audits. Unknown or duplicate children fail closed.
`--batch runner-recovery` groups this root with the two direct-runner profiles.


## Conversation deletion and owned process lifetime

`conversation-lifecycle` runs the original create, rename, archive, unarchive,
liveness, delete reply and on-disk removal assertions. The bootstrap and actually
created conversation keep independent roles. Session IDs bind to the persisted
conversation before launch; the original bootstrap UUID is checked before typed
normalization. The complete supplied files, modes, argv and strict stdin remain
part of the outgoing contract.

Capture execs the real Claude executable at the daemon-owned PID. A separate
broker owns the recording pipes as a child of the outer test process, in its own
process group. A private Unix socket transfers exactly seven distinct pipe ends
with fixed read/write directions and EOF-framed job identity. Kernel peer PID and
UID, the closed current role/job/source, and the broker's actual parent must all
match before readiness. This keeps the observer outside the provider descendant
tree that production legitimately reaps before signalling Claude. Replay runs an owned stand-in and
never executes a provider. Before the unchanged original delete envelope, the
client hook asks the broker to acknowledge a typed `lifecycle_delete` checkpoint.
The checkpoint orders replay with the original client deletion. Every subsequent
provider frame and daemon request through the final prefix seal remains recorded
and strictly matched, including turn-tail and context-usage traffic.

Conversation deletion removes the registry row and releases conversation live
state; it does not evict the pool session. Both owned providers must remain alive
after the original reply and disk assertions. Their prefixes then seal without
closing their pipes. Actual stdout/stderr EOF is required only during the
unchanged original daemon cleanup, bounded by a 30-second absolute deadline.
No provider exit code or signal is inferred for the metadata deletion. Captured
ownership/action PIDs remain in private launch audit files. Native protocol PID
metadata remains literal, opaque replay data and is never used as a process or
signal target.

Each pipe write has both a ten-second bound and the remaining absolute lifecycle
or seal deadline. Original stdin EOF is forwarded, and failure cleanup addresses
only the private jobs belonging to this test. Changed tapes, roles, launch
arguments, owned action/acknowledgment, failed terminal receipts,
public controls, duplicate launches and extra artifacts fail the final audit.
The outer parent closes the FD listener and joins its actual broker processes
after the complete original root and cleanup, then performs the immutable final
audit before publishing any candidate. A timeout kills only actual exec children
owned by that parent. It never signals a provider using a fixture PID. Controls
run the production descendant reaper against an actual detached owned witness,
then signal the provider while holding its stdin open, proving that the external
broker survives both native capture and offline replay teardown. Stderr bytes belong to the saved prefix; their separate diagnostic
stream order is not an asserted deletion property.

## Session rotation and native transcript snapshots

`session-rotation` runs the original new-session root, including its repeated
control sends, persisted fresh identity, settled registry, transition attribution,
second user-send acknowledgement and coexistence of the two physical JSONL files.
A typed `lifecycle_rotate` checkpoint orders the unchanged first rotation send.
The bootstrap journal stays strict through subsequent wrap-up and control traffic
until its actual output EOF. The successor may register only against the current
persisted conversation, after the actual predecessor PID is gone and its owned
broker has published a successful terminal receipt. The successor uses the
original fresh-session argv; resume or extra unknown launches fail the closed
role audit. Its prefix seals alive before unchanged daemon cleanup.

The transcript assertions use complete point-in-time native JSONL snapshots.
Capture reads a stable private file through descriptor-anchored, no-symlink HOME
traversal. It retains raw JSONL bytes, record order, whitespace, Unicode escaping,
numeric formatting and mode. Only top-level session identity tokens and declared
HOME path tokens bind. Nested IDs, message prose and unrelated bytes stay literal.
Duplicate keys, secrets, ambiguous reserved markers, incomplete records, foreign
identities, public files and expansion beyond eight MiB fail closed.

Replay stages the reviewed snapshot carrier privately. It installs each native
file exclusively only after the strict matching original user request has reached
that role's provider boundary. The original physical-file assertions then run.
A private stage binds the exact snapshot digest to the current owner, broker,
source binary, revision and user sequence. Both sides of daemon cleanup validate
the full private snapshot inventory and require the native file still to contain
the complete saved prefix. Later native appends are allowed; rewriting the saved
prefix is not. Fixtures contain captured native outputs, so fresh captures compare
all outgoing requests while independently validating their own transcript witness.
Current provider transcript creation, rotation interpretation and signal behavior
remain covered by the retained original live root.

`clear-wrapup-skipped` adds the original bounded `/clear` root. Its bootstrap,
actually revived bound conversation and fresh successor have three closed roles.
The original seeded UUIDs stay distinct. Both conversation launches must match
the current persisted binding; the successor also requires the actual predecessor
PID to be gone and its external broker to have finished. A typed rotation
checkpoint precedes the unchanged `/clear` send. The production one-millisecond
wrap-up bound, reset-edge assertions, transition counts, eight-second quiet
window and seeded-note byte comparison still run. The recording boundary also
checks the complete literal successor prompt. No
note or file is synthesized for this root. Bootstrap and successor remain alive
until the original daemon cleanup; the bound predecessor journals all traffic
through its actual output EOF.

`clear-reset` runs the original successful `/clear` root with the same three
owned roles and distinct original seed IDs. The original planted build tag uses
the existing deterministic recording nonce hook. All original precondition,
reset-edge, fresh-ID attribution and successor recall assertions remain intact.
Both capture and replay deliver the complete literal appended prompt and check
its mode. The recorded fixture retains every native wrap-up output byte and the
complete successor input file.

Fresh capture comparison handles this one output-to-input dependency explicitly.
The actual production stream parser derives the successful wrap-up text from the
first user turn after the typed predecessor rotation checkpoint. Only its text
chunks before the successful terminal event may contribute; missing, failed,
ambiguous, multiple-user or oversized notes fail closed. The successor's full
captured prompt must contain exactly one complete production-fenced copy of that
same note. Suite decoding validates this causal edge. Comparison then operates
on a copy, binding only that exact fenced note to its fixed predecessor role.
Every surrounding prompt byte, file mode, argv, original user request and control
request stays strict. Stored fixtures are never rewritten by the comparator,
and replay continues checking complete literal prompt files. Different model
notes can therefore compare only when each is the precise predecessor output
that actually reached its own successor. A foreign note, omitted note, duplicate
fence, changed prompt header or changed mode still fails.
