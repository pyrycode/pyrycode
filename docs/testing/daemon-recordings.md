# Captured Claude daemon tests

The pilot runs a real isolated daemon, a loopback relay and an authenticated test
client. Only the supervised Claude executable is replaced. Production parsing,
supervision, routing, permission handling, turn emission and delivery still run.
The test-only wrapper and recorder are not imported by the shipped daemon.

Two scenarios share exactly the same driver in live capture and offline replay:

- `two-turn`: two separately accepted user messages each produce nonempty text
  followed by terminal idle in the correct conversation. Wording is unrestricted.
  This checks sequential delivery, not semantic recall or response chunk counts.
- `permission-deny`: a real Write permission request surfaces to the client;
  its explicit rejection dismisses that modal with remote attribution and the
  conversation completes. Live capture also checks that the denied file does
  not exist. Replay cannot prove that Claude obeys a denial or performs no IO.

`make e2e` includes these cases in the ordinary offline integration selection.
`make e2e-recorded-claude` runs just the pilot. Missing fixtures fail; nothing
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
It preserves every observed frame and its direction in order, with an 8 MiB /
10000-frame bound. Paths inside the disposable HOME are replaced with the single
`${PILOT_HOME}` binding, including embedded prompt/argument paths. There is no
arbitrary prose scrubber or field allowlist. Known secret patterns, configured
credentials and unnormalized operator home paths are rejected without echoing
the payload. Raw transcripts are never written. Candidate files are private.
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
stage must execute both named leaves with no failures or skips. A failing stage
cannot produce a successful compatibility summary, even if the others passed.
Verification never replaces the reviewed baseline.

## Migration inventory and remaining live proof

`inventory` lists static test declarations under `internal/e2e/realclaude`,
separating the live-tagged files from offline evidence validation. It includes
opt-in probes, so those declaration counts are not executed acceptance counts.
The pilot is additive. It does not retire existing fake or live tests, change
preship, or disable automatic dispatcher routing.

Next candidates are ordinary reply delivery, permission allow/deny, questions,
queue actions, model-control messages and thread folding. Each must preserve its
original assertions and account for all external inputs before promotion.
Crash/restart, session files, real tool effects, signal handling, transport
recovery, provider compatibility and measured timing retain explicit live or
other dedicated deterministic integration proof. They cannot be represented by
playing stdout alone. Desktop and Android daemon-message recordings cannot
substitute for this lower boundary: doing so would bypass daemon behaviour.
