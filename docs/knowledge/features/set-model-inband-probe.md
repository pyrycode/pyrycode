# `set_model` in-band probe (#2279)

## TL;DR

**claude 2.1.259 accepts a `set_model` control request, and validates nothing when it does.**
Every arm — a real model switch, an unservable model string, and all three reset spellings —
draws `{"subtype":"success"}` from the control layer. An unservable model is echoed back as the
session's model on the next `system/init` line and only fails one turn later, as a turn-level
error (`result.is_error: true`) that still reports `result.subtype: "success"`. There is no
`control_response` of subtype `error` for this subtype, unlike `set_permission_mode`
([`set-permission-mode-inband-probe.md`](set-permission-mode-inband-probe.md)), so a caller
cannot read a rejected model change off the ack or off the response subtype.

## claude version measured

**2.1.259**, 2026-09-09, macOS, `-race`. Test
`internal/e2e/realclaude/set_model_probe_test.go` (`TestRealClaude_SetModelProbe`); fixtures
`internal/e2e/realclaude/testdata/set_model_v2.1.259_<arm>.json`.

## Argv, and the probe prompts

```
claude --input-format stream-json --output-format stream-json --verbose \
       --model claude-haiku-4-5 --max-turns 4
```

Launched pinned on `claude-haiku-4-5` in every arm, so a change away from the launch model is
observable rather than confounded — a request naming the model already in use would report the
same value whether it was applied or ignored. Two tool-free turns, control request between them;
the second turn's `system/init` line is the post-change read.

## The five arms, wire shape and read

| arm | `model` sent | `control_response` | init model, turn 2 | turn 2 result |
|---|---|---|---|---|
| `accept` | `"sonnet"` | `success` | `claude-sonnet-5` | `success`, not `is_error` |
| `refuse` | `"claude-no-such-model-2279"` | `success` | `claude-no-such-model-2279` | `subtype:"success"`, **`is_error:true`** |
| `reset_omitted` | key absent | `success` | `claude-sonnet-5` | `success`, not `is_error` |
| `reset_null` | `null` | `success` | `claude-sonnet-5` | `success`, not `is_error` |
| `reset_default` | `"default"` | `success` | `claude-sonnet-5` | `success`, not `is_error` |

All five requests correlate by `request_id`, all five draw `control_response.subtype: "success"`.

## The accept arm — alias in, resolved id out

Sent `{"type":"control_request","request_id":"set-permission-mode-accept","request":{"subtype":"set_model","model":"sonnet"}}`.
Turn 2's `system/init.model` reads `claude-sonnet-5`, not the alias `sonnet`. Same rule #1595
already established for the launch `--model` flag: the init line reports a resolved id, never
the string a caller sent. A capture (or a future encoder) needs both strings, not a boolean
saying they matched.

## The three reset spellings — all three work, and reset overrides the launch flag

`omitempty` on a `json.RawMessage` field is what let one struct mint all three shapes: nil omits
the `model` key, `[]byte("null")` mints an explicit `"model":null`, and `json.Marshal("default")`
mints the string. All three drew `success` and all three moved the init line from
`claude-haiku-4-5` to `claude-sonnet-5` — the **account default**, not back to the pinned launch
flag. A plain `Model string` field could not have minted the null case at all: it would have sent
the four-character string `"null"`, a different request, and the null-spelling measurement would
have silently recorded the omitted-spelling's answer under the wrong name.

## The refuse arm — the finding that changes a sibling's premise

Sent `{"type":"control_request","request_id":"set-permission-mode-refuse","request":{"subtype":"set_model","model":"claude-no-such-model-2279"}}`,
a string no published model row carries. The control layer:

```
{"type":"control_response","response":{"subtype":"success","request_id":"set-permission-mode-refuse"}}
```

Turn 2's `system/init.model` then reports `claude-no-such-model-2279` **verbatim** — claude
accepted an unservable string as the session's model with no validation at the control layer at
all. The rejection surfaces one turn later, in the turn itself:

```
result.subtype:  "success"          <- stays success
result.is_error: true               <- the only field that flips
result.result:   "There's an issue with the selected model (claude-no-such-model-2279).
                  It may not exist or you may not have access to it. Run --model to pick
                  a different model."
exit_code:       1
stderr:          [claude-code:unrecognized_model] {"model":"claude-no-such-model-2279","query_source":"sdk"}
```

So a refusal **does** carry usable text, in two places — the turn result and stderr — but **not**
in the shape #1595's `enable` arm set as precedent (a genuine `control_response` of subtype
`error`). A consumer keying on `control_response.subtype` reads this as accepted. A consumer
keying on `result.subtype` reads the failed turn as accepted too — `is_error` is the only signal
that flips. Both traps are real; the second was found in code review after an earlier draft of
this finding described only the first.

## What #2280 and #2281 can rely on

1. The subtype is real. `set_model` is accepted by the control layer at 2.1.259, so the
   `Pool.deliverSettingsInBand` replacement the family exists to build has a target.
2. **The encoder needs no pointer and no custom marshaller for reset.** All three spellings
   behave identically, so emitting the plain string `"default"` is sufficient — the `omitempty`
   string field `controlRequestInner` already uses elsewhere works unchanged for this subtype.
3. **Reset returns to the account default, not to the session's launch `--model` flag.** A
   daemon that wants "go back to what this session started with" cannot use the reset spelling
   for that; it has to resend the launch model explicitly.
4. **#2281 cannot report a rejected model change from the control ack.** There is no
   `control_response` of subtype `error` for this subtype. It has to read the *following* turn's
   result (`is_error: true`, with `result.subtype` staying `"success"`) or stderr's
   `unrecognized_model` line — or validate the model string against the published list
   (`initialize`'s `models` array, [`initialize_control_v2.1.239.json`](../../../internal/e2e/realclaude/testdata/initialize_control_v2.1.239.json))
   before sending, so the daemon never has to relay a mid-turn failure back to a client at all.
5. Two turns are enough to observe every finding above; no arm needed a third.

## How to reproduce

```bash
export CLAUDE_CODE_OAUTH_TOKEN=...   # or ANTHROPIC_API_KEY
go test -tags e2e_realclaude -race -v -run TestRealClaude_SetModelProbe ./internal/e2e/realclaude/
```

~24s, five children. The probe gates on the fixture family's absence (`os.Stat` on the five
`testdata/set_model_v2.1.259_*.json` paths, `PYRY_PROBE_SET_MODEL_RECAPTURE=1` forces a
re-capture) — with the family committed it skips and spends no live time. `go vet -tags
e2e_realclaude` separately; `make check` never compiles this package.

## See also

[`set-permission-mode-inband-probe.md`](set-permission-mode-inband-probe.md) — #1595, the
precedent this probe's rig is ridden from (`runSetModeChild` et al., generalised by an additive
`controlLine` minter) and the one whose `error`-subtype refusal shape does **not** carry over to
`set_model`.

[`sessions-package-key-types-pool-updatesettings.md`](sessions-package-key-types-pool-updatesettings.md)
— `Pool.deliverSettingsInBand`, the production `/model <name>` writer this family's control-request
replacement targets.
