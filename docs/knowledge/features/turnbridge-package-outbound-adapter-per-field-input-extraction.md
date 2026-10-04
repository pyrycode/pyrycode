# Per-field input extraction (`inputFields` / `inputValue`, #1678)

`inputSummary` flattens the whole tool input to one 200-rune line, which buries
the field an operator actually wants (an `Edit`'s `file_path`) inside the bulk
text it precedes. `inputFields(ev.RawInput)` sends the input's own top-level
fields instead, each bounded independently, and populates `ToolUsePayload.Input`
alongside — not instead of — the unchanged `inputSummary`. `RawInput` is read
twice on the same `ToolStart` arm and the two readings are deliberately
independent: a summary derived *from* the capped fields would silently change
`inputSummary`'s value, which this ticket must not do.

Behaviour: `len(raw) == 0`, a non-object (array/number/string/malformed), or an
empty object all yield `nil` — `inputSummary`'s existing "malformed blob is a
field-less tool_use, not an error" posture, extended to the map. Every value is
the input's own value **verbatim**: a JSON string is *decoded* (so an embedded
newline is a newline, not a re-quoted JSON literal), any other JSON type is its
compact JSON form. Entries are admitted **shortest value first**, ties broken by
key, against `maxInputTotalRunes`; an entry that does not fit whole is dropped,
never shortened, and the walk stops there. Shortest-first is what actually fixes
the reported bug — for a `Write{content, file_path}` or an `Edit{file_path,
new_string, old_string}`, the short identifying field is admitted before the
bulk text can spend the budget. **Sorted-key order silently reproduces the
original complaint**: `content` sorts before `file_path` and the budget is spent
before the identifying field is ever considered — this is a real trap, not a
hypothetical one, and any future rework of the admission order needs a
regression case shaped like it (`TestInputFields`'s `Write`-shaped row pins it).

**A `json.Unmarshal` trap that shipped correct only because a test happened to
catch it:** deciding "is this value a JSON string" by *trying* `json.Unmarshal(v,
&s)` and checking the error is wrong — a JSON `null` unmarshals into a string
successfully and leaves `""`, silently rendering `null` as an empty value instead
of the literal `"null"` its non-string sibling values get. `inputValue`
discriminates on the value's **leading `"` byte** instead, which is the only form
immune to this. The bug was caught in code review, not by design — the one table
row in `TestInputFields` covering non-string values happened to include a `null`
alongside a number, bool, array and nested object. A future edit to that row that
drops the `null` case would let a regression back in unnoticed.

Four new constants beside `maxSummaryLen`, each carrying its escaped-byte
arithmetic in its own doc comment in `maxDeltaTextBytes`'s form (`cmd/pyry`):
`maxInputValueRunes` (4000, per-value), `maxInputKeyRunes` (128, drops rather
than truncates an over-long key — a cut key would misname the field, where a
cut value is honestly marked with `…`), `maxInputFields` (16, an entry-count
cap the rune budget alone cannot substitute for, since per-entry JSON
structure costs bytes a content budget cannot see), and `maxInputTotalRunes`
(8500, keys and values summed, measured against the 65519-byte envelope cap by
`protocol.TestToolUsePayload_FitV2EnvelopeCap`). **`maxInputFields` shipped
correct but with no test standing on it**: code review found that deleting its
guard leaves both `internal/turnbridge` and `internal/protocol` fully green —
`TestToolUsePayload_FitV2EnvelopeCap` lives in `protocol`, builds its map by
hand, and cannot call the unexported `inputFields` at all, so it measures the
*envelope*, not the *producer's* entry-count guard. Not fixed as part of #1678
(non-blocking); a future touch of `inputFields` should add the missing
`TestInputFields` row (an input with more than `maxInputFields` entries,
asserting exactly `maxInputFields` survive) rather than assume the envelope test
already covers it.

`docs/protocol-mobile.md` § `tool_use` documents the wire shape (per-value cap,
total bound, `…` marker, empty-map polarity, and that `Input` values are
**display strings, not capabilities** — a `file_path` is model-authored text the
daemon neither resolved nor validated, and a client must not open or execute one
on its own).
