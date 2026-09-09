# `tool_progress` is consumed by matching, not added to `ignoredLineTypes` (#2089)

A long-running `Bash` call makes claude emit a `tool_progress` heartbeat every
~30s; before this ticket `consumeLine` had no arm for that top-level type, so
every heartbeat fell to `emitUnrecognized` and put an `Unrecognized message`
row in the operator's chat for the life of the call.

`consumeToolProgress` is the third arm built on the posture `rate_limit_event`
(#1404) and `control_response` (#1500) already state in-file: the type does
**not** join `ignoredLineTypes`, because that list hides a top-level type
wholesale and this type's fourth variety (bash/powershell progress) has no
positive marker — a marker-set matcher lets exactly that variety fall through
to the unrecognized lane on purpose, so a claude release that starts sending it
here raises the alarm instead of going silent. The matched varieties —
`heartbeat: true`, a present `subagent_type`, a present `repl_call` — are
decoded from the raw line into `toolProgressMarkers`, the same second-decode
shape `userToolResultLine` uses for the same reason (`streamLine` stays a pure
segmentation struct).

## Wire facts pinned by a live capture, not the vendor's declared schema alone

Reverse-engineering the 2.1.259 binary got the marker set right, but two facts
needed a real capture (`internal/e2e/realclaude/testdata/tool_progress_v2.1.259.json`,
read by `TestParser_ToolProgressCapturedFramesAreSilent`) to confirm rather than
guess:

- **The heartbeat is a 30s `setInterval`.** `elapsed_time_seconds` steps
  30, 60, 90 … on every captured frame. A foreground tool call that leaves the
  foreground before t+30s produces no heartbeat regardless of how the marker
  set is written — a sizing fact for any probe or staged call in this area,
  not just this one.
- **The `CLAUDE_CODE_REMOTE`/`CLAUDE_CODE_CONTAINER_ID` guard covers only the
  residual bash/powershell-progress emitter**, in both of claude's two
  `tool_progress` emit sites. The heartbeat, subagent-retry and repl-call
  varieties are unguarded — confirmed against the binary, not inferred from
  the capture (the capture couldn't have shown a *false* negative here either
  way).

The capture could not, however, separate whether the residual variety reaches
this surface at all: 0 of 12 frames were marker-less, but the two candidate
emit sites are byte-identical on the wire for the heartbeat case (the
`yield-twin` wrapper appends `session_id`/`uuid` to match the guarded emitter
exactly), and the staged `cat <fifo>` call produces no incremental output for
the output-chunked variant to key on. `consumeToolProgress`'s dated docblock
records this as narrowed, not closed — a future capture would need either
`CLAUDE_CODE_CONTAINER_ID` set or a staged command with incremental stdout.

## `parent_tool_use_id` means something different here, and that scoped #2191's decode

The committed `tool_progress_v2.1.259.json` is the fixture that proves it: 12 of its
frames carry a non-null `parent_tool_use_id`, and none of them name a subagent spawn.
On an `assistant`/`user` line the key means "the Agent/Task call that spawned the
subagent producing this line"; on a `tool_progress` line it means "the tool call this
heartbeat belongs to" — each heartbeat's own `tool_use_id` is a synthetic
`…-heartbeat-N`, and its `parent_tool_use_id` is the real call's id, the Bash call
these frames are progress for, not a spawning Agent call. A decoder that generalised
the spawned-by reading to this line type would nest an ordinary Bash heartbeat under
its own Bash row.

This is `userLine`'s `tool_use_result`/`toolUseResult` hazard arriving from the other
direction: there, two *spellings* collide; here, one spelling carries two *meanings*
depending which line type it rides. #2191 scopes its decode to exactly the `assistant`
and `user` arms of `consumeLine` for this reason — `consumeToolProgress` sits in a
structurally disjoint branch and is never touched — and proves the negative by driving
every frame of this fixture through the parser and asserting no `ToolStart`/`ToolUpdate`
carries a non-empty `ParentToolCallID` at all. See
[the decode-target family doc](streamsup-package-result-stop-shape-second-decode-target-and-dr.md)
for where the two new targets live and why one widens `userLine` while the other gets
its own struct.

## Related

- [`system` maps per-subtype since 2026-08-07](streamsup-package-system-maps-per-subtype-since-2026-08-07.md) — the sibling design for a top-level type's *inner* varieties (system subtypes hidden inside `ignoredLineTypes`), vs. this type's varieties matched at the top level instead.
- `internal/e2e/realclaude`'s live capture for this type, and the FIFO-staging lesson it produced, are in [tool_progress_capture_test.go](e2e-realclaude-tool-progress-capture-test-go.md).
