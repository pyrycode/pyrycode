# `set_permission_mode` in-band probe (#1595)

## TL;DR

**An in-band bypass change is available to pyry's invocation in ONE direction only.** A
`set_permission_mode` control request on a live child's stdin **drops** the bypass posture
(`bypassPermissions` → `default`) on the running session with no respawn. It **cannot add** it:
claude refuses `bypassPermissions` on a session not launched with
`--dangerously-skip-permissions`. The probe **did** discriminate here, so both verdicts are
behavioural, not echoes.

## claude version measured

**2.1.220**, 2026-08-19, macOS, `-race`. Test
`internal/e2e/realclaude/set_permission_mode_probe_test.go`; fixtures
`internal/e2e/realclaude/testdata/set_permission_mode_v2.1.220_<arm>.json`.

## Argv per arm, and the probe prompts

```
claude --input-format stream-json --output-format stream-json --verbose \
       --model claude-haiku-4-5 --max-turns 8 [--dangerously-skip-permissions]
```

No `--permission-prompt-tool` / `--allowed-tools` / `--permission-mode` — pyry's stream path
carries none of them. The bypass posture uses the flag `claudeSettingsArgs` emits.

| arm | launch flag | control request | init modes, arrival order |
|---|---|---|---|
| `revoke` | `--dangerously-skip-permissions` | `mode: "default"` | `[bypassPermissions, default]` — **flipped** |
| `enable` | *(none)* | `mode: "bypassPermissions"` | `[default, default]` — unchanged |
| `control_default` | *(none)* | *(none)* | `[default, default]` |
| `control_bypass` | `--dangerously-skip-permissions` | *(none)* | `[bypassPermissions, bypassPermissions]` |

Prompts, identical across all arms — turn 1 ``Use the Bash tool to run `ls -la /` and report the
first line of output.``, turn 2 the same with `` `ls -la /tmp` ``. Every arm drives both turns and
the controls omit only the control request, so a measurement arm's post-change read and its
control are both a turn 2.

## The `control_request` line sent

```json
{"type":"control_request","request_id":"set-permission-mode-revoke","request":{"subtype":"set_permission_mode","mode":"default"}}
```

## The `control_response` received, verbatim

```
revoke: {"type":"control_response","response":{"subtype":"success","request_id":"set-permission-mode-revoke","response":{"mode":"default"}}}
enable: {"type":"control_response","response":{"subtype":"error","request_id":"set-permission-mode-enable","error":"Cannot set permission mode to bypassPermissions because the session was not launched with --dangerously-skip-permissions"}}
```

Both correlated by `request_id`. Neither known error string appeared — in particular
**`onSetPermissionMode callback not registered` never fired**, so the handler *is* wired under
stream-json in / out. `enable`'s message is a third error string, not read from the binary before.

## `init.permissionMode` after the change

In the table above — `init` is emitted per turn, not at spawn, so the second is the post-change
read.

## Behavioural verdict per direction

The controls separate cleanly at turn 2 — `control_default` is gated (`permission_denials: 1`,
`tool_result.is_error: true`) and `control_bypass` is not (`0`, `false`) — so AC 3's "does not
discriminate" branch did not fire.

- **`revoke`: REVOCATION APPLIED**, judged against `control_default` (matched exactly).
- **`enable`: ESCALATION FAILED**, judged against `control_default` (matched exactly); it
  did not match `control_bypass`.

#383's finding #2 — a `default`-launched child auto-approved Bash anyway — **does not carry over
to this argv**: that spike ran with `--permission-prompt-tool stdio`, which it found
short-circuits enforcement. Without it, a `default` child is genuinely gated.

## The escalation finding

**A bypass-posture escalation is NOT reachable over the daemon's stdin channel.** claude gates it
on the launch argv, not on the control request, and refuses in words. Two caveats: it is a
claude-version fact, not a guarantee; and it does not make the reverse direction safe — `revoke`
succeeding means anything that can write to a child's stdin can *drop* that child's bypass posture
mid-session.

## What #1596 can rely on

1. YOLO **true → false** can be delivered in-band, no respawn. Verified three ways:
   `success` response, `init` echo flipped, behaviour matched the `default` control.
2. YOLO **false → true** cannot. It must keep the `sup.Restart(newArgs)` path.
3. Splitting `inBandDeliverable` on the *direction* of the change is supported here;
   routing both directions in-band is not.
4. No production writer for the subtype exists yet — #1595 deliberately added none.

## How to reproduce

```bash
export CLAUDE_CODE_OAUTH_TOKEN=...   # or ANTHROPIC_API_KEY
go test -tags e2e_realclaude -race -v -run TestRealClaude_SetPermissionMode ./internal/e2e/realclaude/
```

~40 s, four children, roughly $0.05. The test passes on every recorded outcome; read the verdict
out of the log and the evidence out of the fixtures. Run `go vet -tags e2e_realclaude` manually —
`make check` never compiles this package. The whole-value verdict is not perfectly stable run to
run: one earlier run classified `enable` INCONCLUSIVE because claude retried the denied tool, and
the test file's header comment and per-field breakdown explain how to read that case.
