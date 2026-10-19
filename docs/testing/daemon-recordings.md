# Captured Claude daemon tests

The recorded suite runs a real isolated daemon, a loopback relay and an authenticated test
client. Only the supervised Claude executable is replaced. Production parsing,
supervision, routing, permission handling, turn emission and delivery still run.
The test-only wrapper and recorder are not imported by the shipped daemon.

Eight scenarios run the same assertions in live capture and offline replay:

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
replay and strict outgoing request comparison. Comparison binds observed control
IDs by producer direction and ordinal; it retains complete ordered requests.
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
| Send-now queued placement | `send-now-placement`, same queue/action/order driver | Actual foreground Bash execution, current Claude interpretation and elapsed/settle time |
| Permission allow/deny | `permission-allow` and `permission-deny`, same resolution/history drivers | Actual file creation/absence, current approval transport and settings-respawn behavior |

The recorded question trigger supplies a complete fixed AskUserQuestion input so
fresh daemon requests remain exactly comparable, including question keys and
option labels. The action still selects from the surfaced batch. Existing live
question triggers name no option, preserving their stronger interpretation test.
The compatibility comparator does not scrub provider prose or loosen request
comparison for these cases. Dismissal and terminal idle may arrive in either order;
both are required after the matching continuation.

Next candidates are ordinary queued placement, further model-control messages and thread
folding. Each must preserve its
original assertions and account for all external inputs before promotion.
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
replay and strict request comparison, then runs `make check-common` **once for
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
checkpoint work and live-only proof. Everything else remains explicitly
unreviewed. Counts of recorded scenarios, shared live entrypoints, static test
declarations and remaining eligibility reviews are separate; the total eligible
migration count stays unknown until reviewed. No live requirement is retired.

Review fingerprints include reachable package function bodies plus conservative
package declaration/method context and the imported shared assertion source.
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
Ordinary queue admission does not itself write Claude stdin, so its separate
client-action checkpoint requirement remains unresolved and it is not migrated.
