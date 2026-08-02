# Spec: surface unrecognized claude messages over the v2 relay stream

**Size:** M · **Repo:** `pyrycode` (daemon) + `pyrycode-desktop` (client)

## Context

The interactive daemon has run the stream-json runner in production since
2026-07-24. Its line parser recognises three top-level message types from claude
— `assistant`, `user`, `result` — and drops everything else. The drop writes a
`Debug` log carrying the type name and no content, and the production daemon
runs at info level, so **in practice the drop leaves no trace anywhere and no
client is told**.

The same silence applies one level down. An assistant content block that is not
`text`, `thinking`, or `tool_use` is dropped. A user block that is not
`tool_result` is dropped. A line that fails to decode is dropped without even
recording its type.

That is fine for the message types we ignore on purpose. It is not fine for a
type we have never seen. If a future claude version moves something meaningful
into a new message type or a new content block, every client shows nothing and
nothing anywhere says why. The agent pipeline does not have this problem,
because it forwards claude's output byte for byte.

**Outcome:** a timeline row in the desktop app reading "Unrecognized message",
collapsed to one line, expanding in place to show the raw object. Genuinely
unknown output becomes visible the moment it arrives, without turning anything
on.

## The one real risk, and the measurement that settled it

`system` init fires **once per turn**, not once per session
(`docs/specs/architecture/1088-streamsup-turn-io.md:180`). If every dropped line
became a row, the timeline would gain at least one "Unrecognized message" per
turn and the feature would be worthless noise.

So the parser needs **two tiers**, not one:

- **Known and deliberately ignored** — silent, exactly as before.
- **Genuinely unrecognized** — surfaces.

The known-ignored list is **measured, not guessed**. claude was driven directly
on the bare stream-json surface — the exact fixed prefix `buildArgs` emits — in a
scratch directory on **2026-07-27**, three turns on each of two models, one turn
per run calling tools.

### Measured inventory

Top-level `type` values, across both runs:

| Type | Subtypes seen | Frequency | Disposition |
|---|---|---|---|
| `assistant` | — | per message | mapped |
| `user` | — | per tool result | mapped |
| `result` | `success` | 1 per turn | mapped (turn boundary) |
| `system` | `init`, `thinking_tokens` | **`init` 1 per turn**; `thinking_tokens` ~10 per turn | **ignored** |
| `rate_limit_event` | — | ~1 per run | **ignored** |

`system/status` is not in this sample but is documented from the #1088 spike and
is covered, because `system` is ignored wholesale.

Content-block `type` values, by message role:

| Role | Blocks seen | Disposition |
|---|---|---|
| `assistant` | `text`, `thinking`, `tool_use` | all mapped |
| `user` | `tool_result` only | mapped |

### The open question the measurement answered

Whether claude echoes the delivered prompt back as a `user` message holding a
`text` block. It does on the agent-run surface, captured in tui-driver
`Lessons`. If it did so here too, that would be a second per-turn row unless
`user`/`text` joined the ignored list.

**It does not.** Every `user` line across both runs carried `tool_result` blocks
only, and there were zero `user`/`text` lines. So `user`/`text` needs no ignore
entry, and a `user`/`text` block appearing in future is a real change worth
surfacing.

**AMENDED 2026-07-30 (#1247):** that measurement never drove a *backgrounded*
command. Backgrounding produces a turn with no visible model output, and
claude's harness then injects a `user`/`text` message prodding the model to
speak — one exact 100-byte string, reproduced 3 of 3 on the #1240 probe,
claude 2.1.220. That one payload is now dropped in silence (byte-exact match,
block-level — the parser's first suppression below the top-level
`ignoredLineTypes` tier). Every *other* `user`/`text` block is still a real
change and still surfaces. See `internal/streamsup/parser.go`'s
`harnessNoOutputNudge` and [`codebase/1247.md`](../../knowledge/codebase/1247.md).

### Why `system` is ignored wholesale

Keyed on the top-level `type`, not on `type/subtype`. `system` is claude's
catch-all namespace and its highest-rate emitter, so subtype-grained matching
would turn every new subtype into a per-turn noise row — the exact failure the
two-tier design exists to prevent. A genuinely new **message type** is the alarm
worth raising, and the top-level key is what catches it.

## Daemon changes

### `internal/streamsup/parser.go` — the producer

Three changes.

1. **The known-ignored set** as a named `ignoredLineTypes` map, seeded from the
   measurement above and documented with it. `consumeLine`'s default arm splits:
   an ignored type keeps today's silent debug drop, anything else builds the new
   event.
2. **Content blocks become raw bytes.** `streamMessage.Content` changes from
   `[]streamBlock` to `[]json.RawMessage`, decoded per block inside
   `emitAssistant` / `emitUser`. This is load-bearing, not tidying:
   `streamBlock` declares only the fields the mapping reads, so decoding
   straight into it would **discard exactly the unknown fields an unrecognized
   block exists to show**, and re-marshalling afterwards would lose them. The
   raw bytes are now available per block at no extra cost, and a block that
   fails to decode becomes an unrecognized event rather than a silent skip.
3. **Truncation at construction**, so an oversized payload never enters the
   event stream or any log. `maxUnrecognizedRaw = 16 << 10` sits next to
   `defaultMaxParseBuf`.

The four drop sites now map as:

| Site | Was | Is |
|---|---|---|
| unknown top-level type | silent debug drop | `Unrecognized{Site: line_type}` — unless on the ignored list |
| unknown assistant block | silent debug drop | `Unrecognized{Site: assistant_block}` |
| unknown user block | silent debug drop | `Unrecognized{Site: user_block}` — except one exact harness payload (#1247), dropped in silence |
| undecodable line or block | silent drop, no type recorded | `Unrecognized{Site: undecodable, Kind: ""}` |

`truncateRaw` byte-slices then scrubs invalid UTF-8: the cut can land mid-rune,
and the value rides a JSON string field where an invalid sequence would be
silently replaced downstream anyway.

### `internal/turnevent/event.go` — the sealed variant

`Unrecognized` carries the drop site (`UnrecognizedSite`, a closed string-backed
enum), the message or block type as a string (empty for an undecodable line),
the raw JSON, and a truncated flag. Marker method plus compile-time assertion,
per the existing pattern.

`Raw` is a **`string`, not `json.RawMessage`**, because the producer truncates
it: a truncated blob is no longer valid JSON, so typing it as raw JSON would be
a lie.

### `internal/turnbridge/outbound.go` — the wire mapping

A new arm in `MapEvent` before the default. Carries **conversation identity
only**, no turn id and no seq, matching the `Stall` and `Compacting` arms. This
one is not merely "not turn-scoped": an unrecognized message has **no turn we
can honestly attribute it to**, because we could not parse it well enough to
know what it belongs to.

### `internal/protocol` — the wire vocabulary

`TypeUnrecognizedMessage = "unrecognized_message"` in `codes.go`, grouped alone
rather than with `api_retry`/`compacting` because it is not a claude sub-state:
it reports a gap in **our** mapping.

`UnrecognizedMessagePayload` in `interactive.go`, following the house rule that
no field carries `omitempty`. Byte fixture at
`internal/protocol/testdata/unrecognized_message.json`.

### `cmd/pyry/interactive_turn_v2.go` — the consumer

An arm in `Handle` and in `eventKind`. `eventKind` is load-bearing on two paths,
so it must be updated or the drain's drop diagnostics log an empty kind.

The `Handle` arm takes the status-peer shape: flush any pending delta so
buffered text keeps its wire position, emit, and mutate **no** turn lifecycle.

### `cmd/pyry/stream_turn_busy.go` — no code change, and that is correct

The opener set is a **whitelist**, so the new variant falls to the default and
is a no-op on turn state. That is right: we do not know what the message is, so
it must neither open nor close a turn. **Opening one would wedge the
conversation**, because no turn end follows a message we could not understand.
Only the comment enumerating the variants changes.

The unit test's comment claiming the stream sink emits five variants none of
which can reach the tracker was corrected: `Unrecognized` **is** reachable, and
it reached the tracker safely without one line of change — which is the
whitelist earning its keep.

### ACP — no arm

`internal/acpbridge/outbound.go` deliberately drops `ApiRetry` and `Compacting`
the same way. This is a desktop diagnostic.

### Two guards fail the build if skipped

That is a feature, not an obstacle: `internal/protocol/compat_test.go` (the
v2-only list, the partition list, and the inbound-type rejection row) and
`cmd/pyry/relay_guard_test.go` (the push classification map).

### Logging discipline stays intact

The package rule is that content never reaches a log. The new payload crosses
the wire, not the log. Log sites record site, type, and byte count only.

## Backpressure and size

- **Size cap against the envelope cap.** The binding limit is the **v2
  application-envelope cap of 65519 bytes**, not v1's 1 MiB — v2 superseded that
  (`docs/protocol-mobile.md` § Application-envelope size cap), and the original
  plan for this work carried the stale figure. 16 KiB of raw is roughly a
  quarter of it, leaving room for the envelope's other fields plus JSON
  escaping. Escaping is mild in practice because the payload is already JSON
  text: control characters arrive pre-escaped as printable pairs, so growth is
  quotes and backslashes, not a six-fold expansion of every byte.
- **Backpressure.** Only assistant deltas are droppable under push-queue
  pressure, so this type is never dropped. A burst of unknown lines holds queue
  slots. The size cap bounds bytes but not count. **Accepted:** a burst means
  something is genuinely wrong and you want to see it.

## Testing

Unit tests per layer, in the existing table-test style. The protocol byte
fixture pins the wire shape. `TestParser_IgnoredLineTypesIsTheMeasuredSet` pins
the ignore list itself, so growing it must be a deliberate edit with a
measurement behind it rather than a drive-by.

**Fake-side end to end** (`internal/e2e/relay_v2_stream_unrecognized_test.go`):
a `PYRY_FAKE_CLAUDE_STREAM_BOGUS` rider makes fakeclaude emit one bogus
top-level type and one bogus assistant block ahead of its normal reply, and the
spec asserts both surface as frames to a paired phone **and** that the normal
reply still arrives. One more knob in the harness's existing
environment-variable shape, not a new mechanism.

### The real-claude test is a negative one, and it is the most valuable assertion here

`drainForCompletedTurn` — shared by every stream real-claude spec — now fails on
**any** unrecognized frame. So a normal turn against live claude must produce
**zero**, and every existing stream spec becomes a sentinel for free.
`interactive_stream_unrecognized_test.go` adds the widest single sample: a
tool-calling turn, which carries `thinking`, `tool_use`, `tool_result` and
`text` blocks plus the per-turn `system/init` and the `system/thinking_tokens`
stream.

**That is the regression test on the known-ignored list.** It goes red the day
claude adds a message type, which is exactly the alarm this whole feature exists
to provide, arriving in the pre-ship gate before a binary swap rather than
after. Red does **not** mean something broke; it means claude's output grew a
shape we do not map, and somebody must decide whether it deserves a mapping or
an ignore-list entry.

## Notes and limits

- **Mobile is unaffected and will not crash.** Its decoder ends in a no-op arm
  for unknown types. It also never got `api_retry` or `compacting`, so it is
  already behind on this family.
- **The cap value and the known-ignored list** are the two knobs that decide
  whether this is useful or annoying. Both came out of the measurement.
- **Re-run the measurement before changing the ignore list.** The procedure is
  in this document, and the real-claude assertion is what will tell you it is
  time.
