# The result line's stop shape needs its own decode target, and drops rather than truncates (#2223)

`streamLine` already carried `Subtype` for the `result` line's segmentation
role, so publishing `outcome` needed no new decode of that value — only
`is_error` and `terminal_reason` had to be read off the line at all. That
made **carrying `Subtype` out** the ticket's actual security question, not
decoding it: `Subtype` had needed no length bound for its entire life not
because it was a safe value, but because its only two readers,
`resultTurnEndReason` and `emitSystemSubtype`, only ever compare it against
literals in a `switch` — it never crossed a trust boundary in either
direction. Publishing it is what creates the bound's reason. `boundStopField`
is applied to a **copy** at the publish site (`Outcome: boundStopField(sl.Subtype)`),
and `resultTurnEndReason` is deliberately called on the *unbounded* value
first, so the bound cannot move `Reason` — the daemon's own classification —
for any input length. **The general lesson: an internal field whose only
readers are a `switch` against literals carries no bound not because the
value is trusted, but because nothing has carried it anywhere yet — widening
an existing field's *readership* needs the same bound review a brand-new
decode does, not just a new decode.**

`is_error` and `terminal_reason` are read into `resultStopLine`, a decode
target **sibling to `resultLine`** (the struct `decodeModelWindows` reads
`modelUsage` off, #2101) rather than two more fields on it:

```go
type resultStopLine struct {
    IsError        bool   `json:"is_error"`
    TerminalReason string `json:"terminal_reason"`
}
```

**A separate target rather than a widened one, and the reason is failure
isolation, not tidiness.** `resultLine` exists so a hostile `modelUsage`
shape — a number, an array, an object of numbers — fails *that* unmarshal
without disturbing anything else that line carries. Folding the stop-shape
keys into the same struct would let a hostile `modelUsage` also blank
`terminal_reason`: one field claude controls would silently erase a
different one it does not otherwise touch. Two independent targets fail
independently, which is the same property `decodeModelWindows` states for
`Reason` (read from the already-decoded `streamLine`) versus the window
pair — applied a second time, on the same line, for a second, unrelated
pair of fields. `IsError` is a plain `bool`, not `*bool`: absent, `null` and
`false` are one reading and nothing acts differently on the three —
`systemThinkingTokensLine` makes the identical argument for `int` over
`*int`.

Both strings share one cap, `maxTurnEndStopField` (256 bytes — the family's
one-constant-per-shape precedent from `maxTaskFieldID`, and roughly 7x the
longest subtype claude has shipped, `error_max_structured_output_retries` at
35 bytes), applied by `boundStopField` at construction. **An over-long value
is dropped to `""`, not truncated** — `maxModelWindowID`'s drop-not-truncate
choice, generalized to a second reason. `ModelWindow.ModelID` drops because a
future consumer *joins* on it, and a cut id names no model. `outcome` and
`terminal_reason` drop for a related but distinct reason: both are open-set
tokens a client **matches** against a known list, never free text it
*displays*. A 256-byte cut token matches no known token — indistinguishable
from a token claude has not shipped yet, a state the client already has to
handle because the set is open — while carrying `""` says exactly that and
invents nothing a truncated value would (a truncated *display* string, by
contrast, still says something real, which is why `truncateField` truncates
rather than drops everywhere else in this package). No truncation report is
owed here for the same reason `ModelAnnounced.Truncated` exists at all:
that field is displayed, so a mangled value is worth flagging; a dropped
scalar here is already, directly observable as the empty value the wire
documents — there is no `TruncatedFields` and no counter on this shape.

Neither function logs anything on any path, including the decode failure,
which is discarded rather than wrapped — `decodeModelWindows`'s stated
reason applies unchanged: `encoding/json` quotes the offending input bytes
into its error text, so `"err", err` would route claude's own bytes into the
daemon log through a channel no per-attribute check can see. The house idiom
(wrap errors with context) points the other way on purpose here.

See [turnevent-package.md](turnevent-package.md) § `TurnEnd` for the
published fields this decode feeds, and
[turnbridge-package.md](turnbridge-package.md) for the still-unpublished
`ModelWindows`/`DroppedModelWindows` pair this ticket's design deliberately
left alone on the same variant.
