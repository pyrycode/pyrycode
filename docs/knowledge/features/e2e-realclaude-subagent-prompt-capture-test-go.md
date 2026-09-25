## subagent_prompt_capture_test.go (#2658)

The live half of the delegated-prompt drop: stages one foreground `general-purpose` subagent
under `--forward-subagent-text` (inherited automatically from `streamsup.New`'s own argv), records
every line of the turn, and writes `testdata/subagent_prompt_v<claude version>.json` for
[`internal/streamsup`](streamsup-package-send-half-writeturn.md)'s offline replay to run inside
`make check` against. Reuses [`parent_tool_use_capture_test.go`](e2e-realclaude-parent-tool-use-capture-test-go.md)'s
`ptuc*`/`dropcap*` helpers entire — same package, same rig — rather than building a second one; this
is the pattern's second reuse (`tool_progress_capture_test.go` was the first) and confirms the
family's helpers were built general enough for a fixture other than the one that introduced them.

### The open question resolved against the census, not with it

The plan carried one open question into the live gate: does the delegated-prompt line carry a flag
the way every other member of streamsup's harness-text census does? It does not. The capture's only
distinguishing key on that line is `parent_tool_use_id`; none of `isSynthetic`, `isReplay`, `isMeta`,
or `isCompactSummary` is present. That makes the drop's trigger a **sibling field on the line**, not
a match on the block's text — cheaper than a string match and immune to wording drift, but also the
reason this line doesn't belong in the census table itself: every other row there is harness prose
the person never wrote, dropped because it's noise; this one is the subagent's real input, dropped
only because the same text already reaches clients as the spawning Agent call's own `prompt` field.
See [streamsup's writeup](streamsup-package-send-half-writeturn.md) for the full trigger and why it
sits beside the census table rather than inside it.

### `fixtureWorthy` guards attribution, not just occurrence

Beyond the family's usual checks (outcome fired, spawn shape, claude version, frame decodability),
`spcRecord.fixtureWorthy` refuses a record with anything but **exactly one** Agent call and
**exactly one** delegated-prompt line, and refuses that line if its `parent_tool_use_id` doesn't
name the turn's own Agent call. A capture with two subagents, or a delegated-prompt line pointing at
the wrong parent, would let the reader pin a fixture that looks right but proves an ambiguous claim —
`TestSpcFixtureWorthyRefusesEveryBadCapture`'s "two Agent calls" and "prompt under another parent"
rows are the two failure modes a merely-syntactic check (does a delegated-prompt line exist at all)
would have missed.

### A content-free census does not mean a content-free field

`spcRecord.DelegatedPrompts` records each candidate line's frame index, redacted parent id, and its
**top-level key names and block types only** — never the prompt text itself, even though the line's
own bytes are already captured (and already deny-scanned) elsewhere in the record. Recording the
census this way means the offline `TestSpcDelegatedPromptCensus` can assert the classification logic
against synthesized lines without ever handling a real prompt, and a reviewer reading the committed
fixture's census section learns the shape of the finding without needing the deny-scan to have caught
everything downstream of it.

### Related

- [`parent_tool_use_capture_test.go`](e2e-realclaude-parent-tool-use-capture-test-go.md) — the rig this
  probe reuses whole, and the model-choice trap (haiku won't delegate) that also applies here.
- [streamsup's `send-half`/parser doc](streamsup-package-send-half-writeturn.md) — the fourth
  `emitUser` trigger this capture proves, its census placement, and why the fail-closed direction on a
  malformed parent id is unchanged.
