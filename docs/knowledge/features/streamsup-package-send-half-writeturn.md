# Send half — `WriteTurn`
**Send half — `WriteTurn`.** Mirrors `streamrunner`'s `userTurn`/`userTurnMessage`/
`userTurnContentText` envelope shape verbatim
(`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"…"}]}}`). The prompt is
carried as a JSON string value and `json.Marshal`-escaped, so every embedded metacharacter — critically
every newline — is escaped: the marshalled envelope is always exactly one physical line, and the
trailing `'\n'` `WriteTurn` appends is the only raw newline. This is the injection-resistance property
the ticket called out: a prompt from an untrusted party (mobile client, over the relay) cannot forge a
second stream-json control line (a fake `result`, a `control_request` interrupt, or a permission
approval) on claude's stdin — enforced by structured encoding, not string concatenation, and pinned by
a table-driven test asserting exactly-one-newline + byte-exact round-trip across forged-`result`,
`\r\n`, and control-byte payloads. `WriteTurn(nil, …)` (the shape `Stdin()` returns between spawns)
returns `ErrNoLiveChild` and writes nothing; a write failure (e.g. `EPIPE` mid-teardown) is wrapped and
returned, never panics. `w`'s `io.Writer` type makes a half-close/EOF forgery structurally impossible.
The caller writes turn N+1 by calling `WriteTurn` again on the *same* `Stdin()` handle — no re-open, no
per-turn stdin lifecycle.

**Turncommit gate on send (#1093).** `WriteTurn` claims the [`internal/turncommit`](../../internal/turncommit)
gate carried on `ctx`, mirroring `supervisor.deliverViaSession` on the PTY path: after the `w == nil`
check, before `marshalTurnEnvelope`. A false claim — the queued head was dropped during the wait for
claude to go ready — returns `turncommit.ErrDropped` bare (unwrapped, so the queue can key drop-handling
on `errors.Is`) and writes **zero bytes**; a nil gate (the non-queue paths, e.g. a direct single-turn
send) delivers unconditionally. The `w == nil` check stays first and consumes no claim, so a
no-live-child send keeps the retryable `ErrNoLiveChild` outcome rather than permanently burning the
claim as a false drop. See [codebase/1093.md](../codebase/1093.md).

**Receive half — `Parser`.** An `io.Writer` wired as `Config.Stdout`. Buffers bytes, splits on `'\n'`
(mirroring `streamrunner/watchdog.go`'s `streamParser.feed` mechanics, but as the terminal sink, not a
tee — `Write` always reports `(len(b), nil)`), drops an oversized unterminated partial past `maxBuf`
(4 MiB). Each complete line is decoded into a minimal local shape and switched on the line's **top-level
`type` only** — nested content (assistant text, tool-result content) is opaque data and is never
re-scanned for control types, so a tool result whose text literally contains `{"type":"result"}` cannot
forge a turn boundary:

| Line `type` | Emits |
|---|---|
| `assistant` | one event per content block, in order: `text`→`TextChunk`, `thinking`→`ThoughtChunk`, `tool_use`→`ToolStart` |
| `user` | one `ToolUpdate` per `tool_result` block (status from `is_error`, content from the string/array union); every other block surfaces as `Unrecognized{Site: user_block}` **except one exact 100-byte payload** (#1247, below), dropped in silence |
| `result` | exactly one `TurnEnd` — **the turn boundary**; `Reason` is `resultTurnEndReason(subtype)` (#1120): `error_during_execution` → `TurnEndReasonCancelled`, everything else (including no/unknown `subtype`) → `TurnEndReasonEndTurn` |
| `system` (unmapped subtypes) | nothing — the **known-ignored** tier, Debug-logged by type only, never content |
| `system/task_started` | one `BackgroundTaskStarted` (#1380, below) |
| `system/task_updated` | one `BackgroundTaskUpdated` (#1382, below) |
| `system/background_tasks_changed` | one `BackgroundTaskRoster` (#1381, below) — the family's one **aggregate** variant |
| `system/thinking_tokens` | **at most one** `ThinkingProgress` per `minThinkingTokensPerEvent` (64) tokens of accumulated `estimated_tokens_delta` (#1385, below) — the family's one **rate-bounded** variant; most lines emit nothing |
| `system/init` | one `ModelAnnounced` **unless** `model` is absent, empty, or undecodable (#1600, below) — the family's only variant naming what claude is actually running, once per **turn** |
| `rate_limit_event` | one `turnevent.RateLimited` **unless** `rate_limit_info.status` is the one measured-benign value or the line carries no decodable `rate_limit_info` (#1404, below) — the family's **first non-`system` mapping**, and the one whose gate suppresses the common case rather than the rare one |
| `control_response` | nothing, for every shape but one — consumed **content-free**, matched on the top-level `type` ALONE so any `subtype` is consumed (#1500) — **except** a `success`-subtype response whose `response.response.models` decodes to a non-empty array, which emits one `turnevent.ModelList` (#1811, below). This is still the ack the daemon **solicits for itself**: interrupt on this path is a stdin `control_request` and claude answers ~40 ms later on the same stdout, so without the arm every interrupt fired a false `unrecognized_message`. Shape authority for the two content-free sibling shapes is the verbatim capture in [`set-permission-mode-inband-probe.md`](set-permission-mode-inband-probe.md#the-control_response-received-verbatim) — `subtype` and `request_id` nest **under `response`**, inverting the request side, so `streamLine.Subtype` decodes empty. CORRECTED 2026-08-27 (#1811): this used to say a `subtype:"error"` NAK is consumed indistinguishably from a success, deliberately, because discriminating it would cost a decode target for the nested object. #1811 built that decode target for an unrelated reason (publishing the model list) and the NAK gap closed as a side effect — the arm's one Debug record now carries a `reason` that names `nak` distinctly from `ack`/`commands_only`/`model_list`/`undecodable` — the fifth keyword, `commands_only`, split off `ack` by #1890 (below) |
| any other type, and any line/block that fails to decode | one `Unrecognized` — the **surfaced** tier (see below) |

**Two tiers, and the split is the whole design.** Before this, everything outside the three mapped
types was dropped with a `Debug` log. The production daemon runs at info level, so that drop left **no
trace anywhere and no client was told** — fine for the types we ignore on purpose, useless for a type we
have never seen. Now the parser distinguishes *known and deliberately ignored* (silent, as before) from
*genuinely unrecognized* (surfaced as `turnevent.Unrecognized`, which reaches desktop clients as an
`unrecognized_message` frame and renders as an expandable timeline row).

`ignoredLineTypes` holds the first tier. It is **measured, not guessed** — claude driven directly on the
bare stream-json surface on 2026-07-27, three turns each on two models, one calling tools:

- `system` is claude's catch-all namespace and its highest-rate emitter: `system/init` fires **once per
  turn** and `system/thinking_tokens` roughly ten times per turn, so subtype-grained matching risks
  turning every new subtype into a per-turn noise row — exactly the failure the two tiers exist to
  prevent. Until 2026-08-07 that argument was implemented by ignoring `system` **wholesale**; #1380
  refines it (below) rather than reversing it — `system` stays on `ignoredLineTypes` unchanged, and one
  measured subtype is now mapped inside that same ignored branch.
- `rate_limit_event` fires ~1 per run. **MAPPED since 2026-08-09 (#1404)** — it is no longer a member of
  `ignoredLineTypes` (the map is down to `{"system": true}`); it has its own arm in `consumeLine`'s main
  switch, gated on `status`, below.
- The measurement also settled a standing question: claude does **not** echo the delivered prompt back
  as a `user`/`text` message on this surface, though it does on the agent-run surface. So `user`/`text`
  needs no ignore entry, and one appearing in future is a real change that surfaces.

**AMENDED 2026-07-30 (#1247).** That measurement never drove a *backgrounded* command. Backgrounding
produces a turn with no visible model output, and claude's harness then injects a `user`/`text` message
prodding the model to speak — reproduced 3 of 3 on the #1240 probe, claude 2.1.220. Exactly one such
string, `harnessNoOutputNudge`, is now dropped in silence by byte-exact equality, guarded on block type
`text` so a `tool_result` (whose payload decodes into `Content`, never `Text`) can't reach it — this is
the parser's **first block-level suppression**, a new tier sitting below `ignoredLineTypes` rather than
an entry on it (that map stays top-level types only, and its own comment now carries this amendment
in place). `continue`, not `return`, scopes the drop to the one block, so a sibling `tool_result` in the
same message still maps. Every *other* `user`/`text` block is still a real change and still surfaces —
matched by exact string, not prefix or substring, because the wording is attested on one claude version
and drift must bring the row back rather than stay silently swallowed. See
[codebase/1247.md](../codebase/1247.md).

**Second observation, 2026-08-02 (#1260).** A separate capture session (independent of the one #1247's
constant was transcribed from) reproduced the same block byte-exact
(`harness_nudge.matches_shipped_constant: true` in `testdata/dropped_lines_v2.1.220.json`) — the second
confirmed payload `harnessNoOutputNudge`'s own doc comment names as the trigger for promoting the
constant from a lone string to a set with a pin test. That promotion has not been done; it is deferred
as a follow-up rather than bundled into #1260, which was scoped to capture and record only. See
[codebase/1260.md](../codebase/1260.md).

`TestParser_IgnoredLineTypesIsTheMeasuredSet` pins the list, so growing it is a deliberate edit with a
measurement behind it. The real-claude suite's shared `drainForCompletedTurn` fails on **any**
unrecognized frame, so every stream spec is a sentinel: it goes red the day claude adds a message type,
in the pre-ship gate rather than in front of a user.
