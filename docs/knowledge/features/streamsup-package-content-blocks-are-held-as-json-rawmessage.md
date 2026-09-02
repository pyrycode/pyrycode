# Content blocks are held as `[]json.RawMessage`, decoded per block
**Content blocks are held as `[]json.RawMessage`, decoded per block.** This is load-bearing rather than
tidying: `streamBlock` declares only the fields the mapping reads, so decoding straight into it would
discard exactly the unknown fields an unrecognized block exists to show, and re-marshalling afterwards
would lose them. It also turns a block that fails to decode into a surfaced event rather than a silent
skip. `Unrecognized.Raw` is truncated to `maxUnrecognizedRaw` (16 KiB) **at construction**, so an
oversized payload never enters the event stream or any log; it is a `string` rather than
`json.RawMessage` because a truncated blob is no longer valid JSON.

Mapping logic mirrors (not imports — `mapper.go`'s helpers are unexported and keyed on tui-driver types)
[`turnbridge/mapper.go`](turnbridge-package.md). Two deliberate divergences: (1) a stream-json
`assistant` event carries a *whole message* that may hold several content blocks (`mapper.go` sees one
block per JSONL line), so the parser iterates `message.content` and emits one event per block,
preserving order; (2) `tool_use` input is carried through as claude's already-decoded
`json.RawMessage` verbatim (`ToolStart.RawInput`) rather than `mapper.go`'s map-re-marshal — one fewer
parse, and it preserves the original key order for what is an opaque pass-through field.

**Turn-stateless in everything that describes claude's output; one token counter as of #1385.** The
parser holds no turn counter, no `awaiting` flag, no transcript, and remembers nothing any line *said* —
every mapping but one is a pure function of the line it reads. The one exception is
`thinkingSinceEmit` (above), a plain accumulated-delta counter, not a memory of content. The *only* turn
boundary is a `result` line: it is where `thinkingSinceEmit` resets, unconditionally, and no other line
type can create, reset, or leak state across one — this is what keeps zero cross-turn bleed structural
rather than tracked, proven by a headline round-trip test driving 3 turns with distinct per-turn markers
over one held-open `Stdin()` handle. Per the T1 spike (#1075): `system/init` fires **once per turn**, not
once per session, and the session id is constant across turns of one process — `init` is still correctly
left out of the reset logic; giving one accumulator two reset boundaries to keep agreeing would cost more
than the single `result` boundary already buys. A child that dies without a `result` leaves a residual
`< minThinkingTokensPerEvent` behind on a long-lived parser (one per session, not per turn) — bounded and
named, not a second reset path.

**Concurrency — no mutex.** `os/exec` drives a non-`*os.File` `Config.Stdout` through exactly one
internal `io.Copy` goroutine, so `Parser.Write` is only ever invoked serially from that goroutine.
Unlike `streamrunner`'s `streamParser` (which locks because a separate watchdog goroutine reads its
state), this parser has no second reader. `sink` runs on that same forwarder goroutine; the consumer
owns any synchronization it needs beyond "called serially, in stream order." This slice spawns no
goroutines of its own.

**No transcript tailing on this path (AC4).** `envelope.go`/`parser.go` open, watch, or resolve no
filesystem path — the parser's only input is the bytes handed to `Write`. Reinforced by the same
`go list -deps` import-boundary invariant #1087 pins (no fsnotify, no `internal/supervisor`).

**Interrupt send primitive (#1120).** `Runner.Interrupt() error` writes a single structured
`control_request` line — `{"type":"control_request","request_id":"<id>","request":{"subtype":"interrupt"}}`
— onto the live child's held-open stdin, ending the running turn (claude acks in ~40ms and closes out the
turn with a `result` whose `subtype` is `error_during_execution`, which the receive-side table above maps
to `TurnEndReasonCancelled` — spike T1, #1075, verified live 2026-07-19). `WriteInterrupt(w io.Writer,
requestID string) error` (`envelope.go`) is the free-function marshal+write half, mirroring `WriteTurn`
minus the turncommit gate — an interrupt is not a queued turn, so there is nothing to claim or drop.
`request_id` is locally minted by a per-`Runner` `atomic.Uint64` (`nextControlID`, stringified,
monotonic from 1; renamed from `nextInterruptID`/`interruptSeq` by #1603 — see below), never
caller-supplied; this ticket writes the id but never reads the
`control_response` ack, so uniqueness-within-the-runner's-lifetime is sufficient — no `crypto/rand`/UUID
dependency. `w == nil` (no live child) is checked first and returns `ErrNoLiveChild` with zero bytes
written — the same safe-no-op contract `WriteTurn` holds — so `Interrupt()` can't panic or partial-write
when called against an idle runner. Small enough (`<PIPE_BUF`) that one `write(2)` can't interleave with
a concurrent `WriteTurn` line on the same fd — the package's existing single-writer-per-syscall
discipline, not a new one. `Interrupt` is a **concrete method on `*Runner`, deliberately not added to
`sessions.Runner`** (kept un-widened per #1077) — mirrors how `*supervisor.Supervisor` encapsulates
`SendEsc` (#726) off the interface; the interrupt *routing* sibling (#1121) reaches it via its own
narrow interface or a type assertion. See [codebase/1120.md](../codebase/1120.md).

**Bypass revocation send primitive (#1603).** `(*Runner).RevokeBypass() error` writes a single
structured `control_request` line —
`{"type":"control_request","request_id":"<id>","request":{"subtype":"set_permission_mode","mode":"default"}}`
— onto the live child's held-open stdin, dropping a running child's bypass posture with **no
respawn** (#1595 measured this live against claude 2.1.220: `success` ack, next `init` reporting
`permissionMode: default`, and turn-2 behaviour matching a `default`-launched control child
exactly). `WriteBypassRevocation(w io.Writer, requestID string) error` (`envelope.go`) is the
free-function marshal+write half, mirroring `WriteInterrupt` field-for-field.
`controlRequestInner` — previously carrying only `Subtype` — gained a second field, `Mode`
(tagged `mode,omitempty`), declared **after** `Subtype` so the wire order matches the measured
line; `omitempty` keeps every interrupt line byte-identical to before (`TestMarshalInterruptEnvelope`'s
`want` is unmodified and is the sole detector if that tag is ever dropped). **Neither
`WriteBypassRevocation` nor `RevokeBypass` takes a mode** — no parameter, field, or option
anywhere on the surface selects one. This is deliberate: the opposite direction (granting bypass
over this channel) would be a privilege escalation reachable over the daemon's own stdin, and
claude refuses it anyway on the launch argv (#1595). Re-granting bypass stays on the
`Restart(newArgs)` respawn path. `request_id` now comes from `nextControlID`, the renamed,
**shared** counter (`interruptSeq` → `controlSeq`) — one sequence, not one per subtype, because
`request_id` must be unique across all in-flight control requests on the stream, not merely within
one subtype. Unlike `Interrupt`, `RevokeBypass` **is** on `sessions.Runner` (#1604 widened the
interface) — the contrast is the rule, not an exception: `Interrupt`'s dispatch lives in `cmd/pyry`,
which can type-assert, whereas `RevokeBypass`'s consumer is `Pool.UpdateSettings` inside
`internal/sessions`, with no such dispatch site. A structural assertion there would fail *open* —
its unmatched arm is a silent no-op, leaving the posture un-revoked while the update reports
success — so the interface method makes a runner that cannot revoke a build failure instead. No
production caller existed at #1603; #1604 is that caller, routing a bypass *revoke* through the
in-band branch (an *enable* still takes `Restart`). The
`control_response` ack (no reader needed — see the event-catalog row above, #1500) is unaffected.
See [codebase/1603.md](../codebase/1603.md) and
[set-permission-mode-inband-probe.md](set-permission-mode-inband-probe.md).

**Initialize send primitive (#1689).** `(*Runner).RequestInitialize() error` writes a single
structured `control_request` line —
`{"type":"control_request","request_id":"<id>","request":{"subtype":"initialize"}}` — onto the live
child's held-open stdin, the third subtype alongside `interrupt` and `set_permission_mode` above.
`WriteInitialize(w io.Writer, requestID string) error` (`envelope.go`) mirrors
`WriteBypassRevocation` field-for-field. Unlike `set_permission_mode`, the accepted line carries
**no subtype-specific field** — three arm captures (#1763, re-captured 2026-08-25 against an
authenticated child, claude 2.1.239) agree byte for byte that `request` holds only `subtype` — so
`controlRequestInner` gained no new field and `TestMarshalInterruptEnvelope` /
`TestMarshalBypassRevocationEnvelope` pass with their `want` literals unmodified. `request_id`
comes from the same shared `nextControlID` counter as `Interrupt` and `RevokeBypass`. **Not added
to `sessions.Runner`**, unlike `RevokeBypass`: the interface-placement rule is by consumer
location, and #1839 (below) put the consumer inside this same file rather than inside
`internal/sessions` — so the rule still argues against an interface method, now for the ordinary
reason (in-package caller, no cross-package dispatch to satisfy) rather than for having no caller
at all. This slice writes the line and stops; nothing reads the ack.

**Tool-result sidecar decode — fail-closed is a confinement control, not just AC hygiene (#2024, extended
to all five shapes by #2025).** `consumeLine`'s `user` arm hands `emitUser` the raw line bytes (the
`emitRateLimit(line)` shape), which decodes the line's `tool_use_result` sidecar a second time into
`json.RawMessage`-typed fields, the `systemTaskUpdatedLine.Patch` precedent. #2024 shipped only the read
shape and named #2025 as the ticket that would add the rest, predicting a dispatch with arms; #2025 found
that prediction wrong — `readLineCount` was a single-shape composer, not a dispatch — and restructured it
into `toolResultDetail`, five composers tried in a fixed order (read → shell → edit → write → search, each
returning `""` on non-match, `""` at the end when none matched). That fixed order is what makes the
fail-closed default hold for a shape recognised only in part: a `structuredPatch` sidecar matching neither
the edit nor the write arm, or a write whose `type` is neither `create` nor `update`, both run off the end
with no count. The reason the default matters this much: `emitUnrecognized` puts the offending line's
bytes on the wire (truncated to `maxUnrecognizedRaw`) for the phone to render, and an undeclared field can
be **the entire contents of a file claude read or edited**, an absolute path, or raw grep output — so no
sidecar path may ever emit `Unrecognized`, structurally, not just by test coverage.

**Identify by presence of keys, never by exact key set — and presence-only decoding is what lets an
unbounded confinement rule scale to five shapes.** #1794's census found 288 of 4883 observed shell
sidecars (5.9%) carry a sixth key (`gitOperation`, `persistedOutputPath`, …) and 90 edit/write sidecars
carry an extra one; an arm keyed on "exactly these keys" would silently lose all of them. `toolResultSidecar`
is widened into a **shape discriminator**: safe scalars by value, everything else — `structuredPatch`,
`oldString`/`newString`, `content`, `mode` — decoded only for *presence*, via a `*jsonKey` field whose
`UnmarshalJSON` retains no byte of the value. That is what lets `structuredPatch: []` (**every observed
create, 240 of 240**) be recognised as present without decoding a single hunk — extending `sidecarFile`'s
pointer-so-absent-differs-from-present-zero rule from a scalar to a whole sub-document.

**Two shapes spelling a key the same are not the same field.** `content` is a write sidecar's full file
text *and* a search sidecar's whole grep output; a shared decode field between them would put the search
arm's grep output on the write arm's target (or vice versa) the moment either shape carried the other's
key. Every arm that needs an unbounded claude-supplied value for its count — shell's `stdout`, the write's
`content`, the edit's hunk `lines` — gets its **own narrow stage-2 target**, re-decoded from the same
`json.RawMessage` and never returned from its composer: every composer returns a `string`, never a decoded
value, so a stage-2 struct can never escape into a caller that might hold claude's bytes.

**The plausible-wrong edit-arm implementation was caught only by mutation, not by review.** A hunk's
`oldLines`/`newLines` are its *spans*, context lines included; differencing them looks like it should
yield `+10 −3` and does not — the count has to come from the `+`/`-` prefixes of the hunk's `lines`
instead. A fixture whose context lines don't outnumber its changed ones agrees with either implementation;
only forcing the span-differencing mutant under `go test -overlay` proved the fixture (and the acceptance
criterion built around it) non-vacuous.

**The two separator glyphs are the first non-ASCII bytes this field has ever carried, and the doc-comment
sweep for it missed one place that said so.** `+10 −3` uses U+2212 MINUS SIGN (not a hyphen);
`created · 54 lines` uses U+00B7 MIDDLE DOT. #2025 named and corrected four doc comments asserting
"digits, spaces and ASCII letters" or equivalent — `maxResultDetailBytes` here, plus one each in
`internal/turnevent/event.go`, `internal/turnbridge/outbound.go` and `internal/protocol/interactive.go` —
but a fifth carried the identical claim and wasn't on that list:
`internal/turnbridge/outbound_test.go`'s `TestToolResultPayload_FitV2EnvelopeCap`, whose 48-byte ASCII
fill was justified with "that IS the producer's alphabet." Only #2025's own security review caught it,
by asking the general question ("what else claims this field is ASCII?") rather than re-checking the
ticket's own enumerated list. **The load-bearing half of every one of those claims survives unchanged: no
claude-authored byte reaches the field.** Only the alphabet, the composer count and the function name
moved — grepping for the four places a fact was *stated* is not the same audit as grepping for every place
it was *relied on*.

Confirmed non-vacuous by mutation: forcing the envelope key from `tool_use_result` back to the
transcript's `toolUseResult` — precisely the dead-code decoder [#2023's stdout capture](e2e-realclaude-tool-result-sidecar-probe-test-go.md)
exists to have prevented — turns 6 subtests red under `go test -overlay`, so the fixtures pin the observed
spelling rather than merely agreeing with it.
