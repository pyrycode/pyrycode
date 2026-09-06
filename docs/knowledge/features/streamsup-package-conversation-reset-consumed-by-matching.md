# `conversation_reset` is consumed by matching, and declines its neighbours' stronger guarantee (#2134)

claude announces a `/clear`, a plan-mode exit, or a fresh-session flow on its
own stdout as a top-level `conversation_reset` line naming the id it mounted
the fresh transcript under. Desktop's **Reset session** produces it too — it
sends the literal text `/clear`, which claude runs as a slash command rather
than passing to the model. Before this ticket `consumeLine` had no arm for the
type, so every reset fell to `emitUnrecognized` and put an `Unrecognized
message` row in the operator's chat for something the operator explicitly
asked for.

The arm copies `consumeToolProgress`'s **shape** — a marker-set matcher that
returns on consume and otherwise falls through to `emitUnrecognized`, written
out rather than reached by `fallthrough` — but not its **reason**.
[`tool_progress` falls through](streamsup-package-tool-progress-consumed-by-matching.md)
because one *variety* of the type is unmeasured; `conversation_reset` falls
through because one *payload* is unusable: a `new_conversation_id` that is
absent, empty, or not a canonical lowercase UUID stem is a reset the daemon
cannot act on. Copying a shape without carrying its reason is what turns two
different decisions into one rule a later reader can't re-derive — worth
saying explicitly here because this is the second arm built on the same
shape, for a different reason than the first.

**This arm deliberately does not take `rate_limit_event`/`control_response`/
`tool_progress`'s stronger guarantee.** Those three each document that
`emitUnrecognized` is unreachable for their type "by matching rather than by
list membership." `conversation_reset` declines it on purpose: an id the
daemon can't act on is routed to the unrecognized lane *because* an
announcement the parser silently swallows is the exact defect this ticket
fixes. A later reader tidying the matcher into an unconditional consume would
look like a cleanup while quietly restoring the original bug.

The id is gated with `transcript.ValidStem` **before** construction
(`emitConversationReset`), which is what makes
`turnevent.ConversationReset.NewConversationID` canonical by construction for
every downstream consumer instead of each one re-deriving the question —
`internal/turnevent` can't hold the check itself, since
`TestImportBoundary_StdlibOnly` keeps that package stdlib-only. The gate
proves the id is *well-formed*, not that claude was *entitled* to name it; a
buggy or compromised claude can announce any well-formed stem, including
another session's. #2135, which re-keys the session registry on this value,
owns the authorization question — the gate here only
closes off traversal and newline injection into the eventual
`<dir>/<id>.jsonl` resolution it feeds.

## Testing: a hostile row can pass by failing at the wrong layer

A validator-rejection test row (`../../etc/passwd`, a stem with an embedded
newline) is one careless fixture away from proving nothing: written as a
malformed JSON value, it fails at `json.Unmarshal` and lands on the *same*
decline path a `ValidStem` rejection would, passing green without ever
reaching the predicate it was meant to exercise. The fix is mechanical rather
than careful — each row is written as the well-formed JSON string the decode
must accept, and the test re-decodes it and fails if it doesn't come back
byte-identical, so a row that quietly stopped being valid JSON can't hide
behind the decode failure. The one row exempted on purpose is a non-string id,
which is *supposed* to fail the decode. A guard that instead selects which
rows get this treatment by matching the row's *name string* is a rename away
from silently dropping the check it exists to run — correct as written, but
worth flagging as a table-test idiom to avoid the next time one is built.

Same trap, one layer down, for the regexp anchor behind the traversal
argument: `$` matching before a trailing newline would let a canonical stem
with `"\n../../etc/passwd"` appended through whole. Verified rather than
assumed — Go's `$` is `\z` semantics, so it doesn't — but that is a Go-specific
fact, not a general regexp one; a Perl-family port of this predicate would
need the check re-run rather than trusted by analogy.

## Related

- [`tool_progress` is consumed by matching](streamsup-package-tool-progress-consumed-by-matching.md) — the shape precedent, built for a different reason.
- [`turnevent-package.md`](turnevent-package.md) — `ConversationReset`'s field-level doc: canonical-by-construction, no `Truncated` field, and carrying claude's own identity on purpose; also the prose-drift lesson this ticket reprised at `eventKind`.
