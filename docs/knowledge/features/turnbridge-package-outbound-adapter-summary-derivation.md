# Summary derivation (`inputSummary` / `resultSummary` / `truncate`)

The wire envelopes carry a human-readable **précis** (not the raw input/output). Three
pure helpers derive a bounded, single-line summary:

- **`inputSummary(json.RawMessage)`** — `json.Compact` (whitespace → one line) then
  `truncate`. Empty/nil **and** invalid-JSON both yield `""` — `RawInput` is
  best-effort/opaque (#606), so a malformed blob is a précis-less `tool_use`, not an
  error.
- **`resultSummary(turnevent.ToolContent)`** — **exhaustive** over the sealed
  `ToolContent` sum type so a future producer variant cannot silently vanish:
  `nil`→`""` (the legal status-only `ToolUpdate`), `TextContent`→its text,
  `DiffContent`→`Path`, `TerminalContent`→`"terminal <id>"` — each truncated at
  `maxResultSummaryRunes` (10000, #1680), not `maxSummaryLen`. The cap is inert
  for the Diff/Terminal arms (neither approaches it) and applies to all three
  anyway — one rule is cheaper to read and test than a per-arm exception. `is_error`
  does not change the bound: the flag is derived at the `MapEvent` call site and
  never reaches `resultSummary`, so a failed tool's result is truncated exactly as
  a successful one's is. The live inbound producer (`internal/streamsup`'s
  `toolResultContent`) only ever emits `TextContent` or `nil`; the Diff/Terminal
  arms are unreachable today but handled (kept deliberately minimal) until a
  producer (the ACP adapter #600, or a refinement) emits them.
- **`truncate(s, max)`** — returns `s` unchanged at ≤ `max` runes; otherwise cuts at
  `max` runes (`[]rune`, not bytes) and appends `"…"`. Rune-aware so multibyte text
  never splits mid-rune.

`const maxSummaryLen = 200` bounds the **input** précis to one line of ≤ 200
runes — a **phone-display** bound, not a wire constraint (the envelope cap is far
larger); tunable if the mobile view wants a different cap. It stopped bounding the
result précis in #1680: results are an order of magnitude bigger than inputs
(11379 measured local tool results, mean 2959 characters, median 721; only 22%
survived the 200-rune cap whole) and the producer applies no cap of its own, so
the result side needed its own **wire** constraint rather than a display one.

`const maxResultSummaryRunes = 10000` is that constraint — deliberately
`maxDeltaTextBytes`'s number (`cmd/pyry`, the other free-text field on a v2
envelope), so the two don't drift apart for no reason. Its doc comment carries
the full six-bytes-per-rune arithmetic (`encoding/json`'s `SetEscapeHTML`
default makes `'<'`/`'>'`/`'&'`/control bytes cost 6 wire bytes each, a multibyte
rune is emitted raw so it never gets worse) and a never-raise rule with two
reasons: the measured worst case (a `'<'`-filled 64525-rune result with hostile
64-rune identity fields) is 61363 B against the 65519-byte envelope cap — 93.7%,
~4.2 KB of headroom — and `tool_result` is control-class in `internal/eventring`,
preferentially retained up to `MaxEventsPerConversation` (1024) per conversation
holding the marshalled bytes, so raising the rune cap also multiplies the ring's
worst-case per-conversation footprint (~1.4 MiB → ~59 MiB at 10000). 16000 was
measured and rejected at 96309 B, 47% over the cap — exceeding it doesn't
truncate the frame, it **loses** it, and `tool_result` is never-droppable, so the
operator would see an empty row rather than a shortened one.

**A worst-case envelope measurement has to pick the worst case of its `bool`
fields too.** `TestToolResultPayload_FitV2EnvelopeCap` (`internal/turnbridge`,
not `internal/protocol` — see the [`maxInputFields` coverage](turnbridge-package-outbound-adapter-per-field-input-extraction.md) on why a test
that can't call the unexported helper doesn't stand on the constant) drives the
real `resultSummary`; its original measurement logged 61363 B, one byte over the spec's hand-computed
61362: `"is_error":false` costs one more wire byte than `true`, and the field is
never `omitempty`. The test's two mutants (`maxResultSummaryRunes` → 16000; the
`TextContent` arm returns `v.Text` uncapped) die at different rungs — the
uncapped arm is caught by the test's rune-count precondition before anything is
marshalled, while only the raised-cap mutant reaches the `< 65519` byte
assertion. That split means the precondition is load-bearing coverage, not a
courtesy guard: deleting it as "redundant" would leave the uncapped-arm mutant to
die on a byte-count failure instead of a legible message naming the arm.

`docs/protocol-mobile.md` § `tool_result` documents the wire shape (the
10000-rune cap, the `…` marker, that `is_error` does not change the bound, and
the six-bytes-per-rune arithmetic) and states the same **display string, not a
capability** hazard § `tool_use` states for `Input` — a tool result is raw
command output or file contents, so a `'<'`-dense result is the ordinary case,
not the contrived one.

**The tool-result envelope test retains a conservative 48-byte `ResultDetail` allowance** (see
[protocol-package-interactive-event-payloads.md](protocol-package-interactive-event-payloads.md)):
`TestToolResultPayload_FitV2EnvelopeCap` fills it with 48 ASCII digits alongside
the capped `ResultSummary`. Since #2745 only Edit and Write compose details,
bounded by 43 and 36 bytes respectively, so the fill exceeds either producer's
current bound. `TestToolResultDetail_BoundedByConstruction` checks both forms
against this allowance. The Read form formerly supplied the longest bound;
removing it leaves the envelope test's allowance unchanged.
Historically, adding the field in #2024 measured **61430 B, 93.8%** of the
65519-byte cap — up from 61363 B / 93.7%, exactly the field's 48 bytes plus
19 B of key and punctuation. #2025's five-form expansion left that measurement
unchanged; it is a historical measurement, not a fresh measurement of today's
complete payload. `ResultDetail` needs no rune cap of its own (see the protocol
doc) because every count formats through `strconv.FormatInt` plus fixed
literals — never claude's text carried through — even though two of those
literals (U+2212, U+00B7, #2025) are non-ASCII; `encoding/json` escapes
neither, so the ASCII fill has the same wire cost per byte. Later fields must
be checked against the current complete envelope; the old **~4.1 KB** headroom
is historical too.
