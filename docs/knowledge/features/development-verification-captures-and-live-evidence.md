# Captures and live evidence

An acceptance criterion requiring a capture must be reachable under the gate's normal
invocation. An operator-only environment switch cannot be its sole trigger. Missing
fixtures can arm a capture, with an explicit force switch for later recapture. Validate
the evidence before writing it, and keep the observed version consistent with the
reader's path and pin.

A committed-fixture reader must fail when its fixture is missing. Do not copy a skip
state from a probe that was written before evidence could exist. Conversely, a capture
that stops running after its fixture exists can retain stale assertions indefinitely.
When changing the parser's result for its frames, inspect the disarmed probe and compile
the tagged suite even if no new live capture is needed.

Do not equate a non-zero parser event count with reaching an unrecognized lane. That
shortcut becomes false when a previously silent matched frame gains a legitimate event.
State the expected event cardinality for each captured marker or subtype, and keep the
unrecognized census tied to the discriminator that actually selects that lane.

A ticket or plan can misstate which identifier a cited capture fixture actually
carries — paraphrase drifts from the bytes it describes. Verify a fed value's
identifier against the capture file itself before writing the assertion, and assert
the value actually fed, not the prose's claim about it.

Record enough redacted context to diagnose an empty capture. Count other line types,
subtypes, tool outcomes and timing without retaining secret-bearing inputs. On an
assertion failure, retain the relevant redacted response and selected result fields:
a temporary transcript path loses its evidence when fixture cleanup runs. A passing
rerun cannot establish what the failed response said. The
[allowed-tools refusal test](e2e-realclaude-allowed-tools-enforcement-test-go.md#refusal-wording-and-diagnostic-evidence)
needed this distinction to separate a missed explicit refusal from a missing signal.
A witness controlled by the test rig is stronger than a model being asked to wait.
Ensure its release path cannot hang when no reader starts.

Read the evidence and the expression behind a conclusive flag. A boolean can omit the
very observation that changes its interpretation. Reusing a decoder does not validate
an unobserved envelope key; a wrong key can silently produce an empty result.

When a capture's evidence is an explicit zero, assert the raw key and numeric JSON type
before asserting the decoded zero. Missing, null, wrong-typed, and explicit-zero input
can deliberately collapse to the same downstream zero value, so parser output alone
cannot prove which source shape the fixture retained.

A full-stack proof of a rate-bounded delivery path needs two runs on the identical
drive helper, not one. A below-bound feed producing zero client-visible frames does
not by itself show the feed arrived: a wrong env name, a malformed line, or a drop
before the parser all read as the same zero. Pair it with a feed that crosses the
bound by one unit and must produce exactly one frame carrying that crossing line's
own values. The crossing run is what kills the false-pass causes the zero alone
cannot rule out, and together the pair pins the bound from both sides — a lowered
bound reds the silent run, a raised bound or an off-by-one comparison reds the
crossing run. Compute both feeds' expected total from the fed lines themselves
rather than restating it as a literal, so a feed that drifts off the edge of the
bound fails as that, not as a wrong frame count.

[Development verification overview](development-verification.md).
