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

**#2224 reuses this shape's cap for a field decoded off a different line.**
`assistantErrorLine{ Error string `json:"error"` }` is a third sibling
target, read off the wrapper level of an `assistant` line rather than the
`result` line the two fields above share, and bounded by the same
`boundStopField`/`maxTurnEndStopField` pair rather than a new constant — the
constant's own doc states the reason: these are one shape, "short open-set
tokens off a single line, matched by a consumer against a known list," and
that a value comes off a different line does not split the shape. Because the
value must survive from the `assistant` line to the `result` line that
publishes it, this is the first field in the family that needs the parser to
remember something claude said rather than decode-and-publish within one
line — see [turnevent-package.md](turnevent-package.md) § `TurnEnd` for the
cross-line latch design and its residual/fail-closed argument, which is a
parser-state question rather than a decode-isolation one and belongs there
instead.

**#2191 adds two more targets and, for the first time in the family, bounds
one of them by `maxTaskFieldID` instead of `maxTurnEndStopField`.** The key
is `parent_tool_use_id`, read off `assistant` and `user` lines to publish
`turnevent.ToolStart`/`ToolUpdate`'s `ParentToolCallID`. `maxTurnEndStopField`
covers "short open-set tokens matched against a known list" — this value is
the opposite shape, a machine-minted identifier a client *joins* on, which is
`maxTaskFieldID`'s class (the constant #2233 already applies to a
`tool_use_id` on a shipping frame). Getting this wrong in either direction
would be silent: a token-class cap here would just happen to be the same 256
bytes, so nothing would fail — the constant to reach for is a question the
*value's* semantics answer, not something the cap's number can tell you.

**Which target widens and which gets its own struct split down the middle of
the family, one target each way, and both docblocks are right about their own
line.** `assistantErrorLine`'s independence argument governs the assistant
side — a hostile `parent_tool_use_id` shape must not blank the error
category and vice versa — so the assistant line gets a new sibling target,
`assistantParentLine`, not a fourth field bolted onto `assistantErrorLine`.
`userLine`'s "one decode, not two" argument governs the user side the
opposite way: the key is folded into the existing `userLine` struct rather
than given a fourth target, because a user line routinely carries a whole
file and a second full pass over it is not free. Read the argument as
belonging to the *line*, not to the family — a decode-isolation ruling from
one sibling does not generalize to the other just because the same key is at
stake.

**On the `userLine` side, the field's Go type is load-bearing, not
stylistic.** Every existing `userLine` field is chosen so the decode *cannot
fail* — `ToolUseResult` is `json.RawMessage` for exactly that reason, and the
struct's failure mode is documented as "the block surfaces unmodified." A
plain `string` field for `parent_tool_use_id` would break that property: a
value of `7` or `{}` — both valid JSON, both something claude could emit —
would fail the whole `userLine` unmarshal and zero `IsSynthetic` along with
it, resurrecting the harness-prose rows #2087 removed (an 87 KB skill body,
by that ticket's own measurement) as a disclosure regression reachable by a
value claude controls. `ParentToolUseID json.RawMessage`, decoded through the
shared `parentToolUseID` converter both lines call, keeps the "cannot fail"
property while still landing one converted value. The general form: adding a
field to a struct whose existing fields were typed to make the decode
infallible has to preserve that property with the same care a first design
would, not just match the JSON shape.
