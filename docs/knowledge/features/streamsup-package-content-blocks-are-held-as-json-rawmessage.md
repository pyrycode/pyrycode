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

**Permission-mode send primitive (#1603, generalised #2042).** `(*Runner).SetPermissionMode(mode
string) error` writes a single structured `control_request` line —
`{"type":"control_request","request_id":"<id>","request":{"subtype":"set_permission_mode","mode":"<mode>"}}`
— onto the live child's held-open stdin, switching a running child's permission posture with **no
respawn** (#1595 measured the `default` line live against claude 2.1.220; #2041 measured
`acceptEdits`/`dontAsk`/`plan`/`auto` the same way against 2.1.239 — all five acked `success` with
the next turn's `init.permissionMode` echoing the new mode). `WritePermissionMode(w io.Writer,
requestID, mode string) error` (`envelope.go`) is the free-function marshal+write half, mirroring
`WriteInterrupt` field-for-field. `controlRequestInner` — previously carrying only `Subtype` —
gained a second field, `Mode` (tagged `mode,omitempty`), declared **after** `Subtype` so the wire
order matches the measured line; `omitempty` keeps every interrupt line byte-identical to before
(`TestMarshalInterruptEnvelope`'s `want` is unmodified and is the sole detector if that tag is ever
dropped).

`WritePermissionMode` replaced #1603's `WriteBypassRevocation`, which took no mode at all. That
writer's actual safety property was never "one literal" but "no escalation reachable from this
surface", and #2042 keeps it under a wider parameter via `permissionModeAllowed`, a **closed
allow-list** (`default`, `acceptEdits`, `plan`, `auto`, `dontAsk`) refused by **non-membership**
rather than a deny-list entry — `bypassPermissions` never appears in production source under
`internal/streamsup`, and every unanticipated spelling (wrong case, a trailing space, a homoglyph)
is refused for free along with it. Three lessons from that widening, each a security-review finding
rather than a style choice:

- **The allow-list is a `switch`, not a package-level slice or map.** A mutable list holding a
  security allow-list can be `append`ed to by anything sharing the package — a test included —
  dissolving the carve-out globally, and a test that did so ahead of its parallel siblings would
  not reliably trip `-race`. Control flow cannot be appended to.
- **Refusal order is a contract, not an accident.** `WritePermissionMode` checks the allow-list
  *before* the nil-writer check. Both orders write zero bytes, but reversed, a caller naming an
  escalation against an unbound runner would get back the *retryable* `ErrNoLiveChild` and could
  reasonably retry forever a request that can never succeed. `ErrUnsupportedPermissionMode` is
  permanent, `errors.Is`-distinguishable from `ErrNoLiveChild`, and returned as a **bare
  sentinel** — never wrapped with the rejected mode — because `Pool.deliverSettingsInBand` logs it
  verbatim and #833 keeps settings values out of the daemon log. That precedence must also hold at
  any runner wrapper before it constructs the writer call: Go evaluates `r.Stdin()` and
  `r.nextControlID()` before entering the delegated function. #2352 caught this in
  `RequestContextUsage`; relying only on `WriteContextUsage`'s validation returned the right
  sentinel but still inspected child state and consumed a shared request ID. Keep the closed
  predicate at the exported writer boundary, and repeat its preflight in a wrapper whenever the
  ordering itself is part of the contract.
- **Vocabulary check, not an authorisation check.** The allow-list answers "will claude parse this
  mode?", never "may this caller change this session's posture?" — three of its five members
  (`acceptEdits`, `auto`, `dontAsk`) genuinely *loosen* a child launched in `default` behind the
  daemon's approval flags, and claude accepts each of them in-band (#2041) precisely because the
  daemon asked, not because claude vetted them. A future caller taking a mode from a wire frame
  (#1687) owns the authorisation decision itself — reading this gate as "refuses unsafe modes"
  would be the wrong takeaway to carry into that ticket.

The allow-list also bounds the emitted line's length: the longest member (`acceptEdits`) keeps the
envelope near 110 bytes, well under `PIPE_BUF`, preserving the single-`write(2)`-per-line
atomicity `Interrupt` already relies on to avoid tearing against a concurrent `WriteTurn` on the
same fd — an unbounded caller-supplied mode would have reopened that hazard.

`request_id` still comes from `nextControlID`, the shared counter (`interruptSeq` → `controlSeq`
at #1603) — one sequence, not one per subtype. `RevokeBypass() error` is **kept**, re-expressed as
`SetPermissionMode(permissionModeDefault)` rather than subsumed: the slice that introduces the
general form is deliberately not the slice that migrates the consumer
(`Pool.deliverSettingsInBand`, left to #2043), so the revocation's wire bytes stay provably
unchanged and the delegation mints exactly one id per call, not two. `sessions.Runner` gained
`SetPermissionMode(mode string) error` beside `RevokeBypass` for the placement reason `RevokeBypass`
was already on it (#1604): both methods' consumers are inside `internal/sessions`, where a
structural type assertion would fail *open* rather than a build failure. **CORRECTED 2026-09-03
(#2064):** the `control_response` ack is no longer unread. `SetPermissionMode` now re-targets a
closed `PostureGate` onto its own freshly-minted id after a successful write, so claude's
per-model refusal of `auto` (#2041) — or any other NAK for a request this method sent — is
recorded as a refusal and surfaces as a turn-gate Warn if it lands while the gate is closed; an
**open** gate is never touched by this call, so a later `/effort` delivery is
unaffected. Model delivery uses its own control request and never participates in
the posture gate. See [Posture gate](streamsup-package-posture-gate-spawn-permission-mode-ack.md).
See [codebase/1603.md](../codebase/1603.md),
[set-permission-mode-inband-probe.md](set-permission-mode-inband-probe.md), and
[permission-mode-switch-inband-probe.md](permission-mode-switch-inband-probe.md).

**Model-control send primitive (#2280).** `(*Runner).SetModel(model string) error`
writes one newline-terminated control request to the held-open child stdin:
`{"type":"control_request","request_id":"<id>","request":{"subtype":"set_model","model":"<model>"}}`.
`WriteModel(w io.Writer, requestID, model string) error` is the marshal-and-write
half: nil stdin returns `ErrNoLiveChild`, JSON encoding keeps the model a string,
and the complete line is passed to one `Write`. `controlRequestInner.Model` is
tagged `omitempty`, so interrupt, initialize and permission-mode requests retain
their prior byte shapes. The id comes from the same `nextControlID` sequence as
every other control subtype.

This request creates no user turn. Its matching success response identifies the
request but does not echo the requested alias; the resolved model is reported by
the next application turn's `system/init`. `SetModel` deliberately does not close
or retarget `PostureGate`: that gate protects spawn and permission-posture
confirmation, while a missing or refused model acknowledgement must not turn an
otherwise healthy session into a refusal window. Model servability and
client-visible refusal reporting therefore remain outside this writer; the
caller-owned validation boundary still constrains network-originated values.

**MCP-control send primitives (#2273).** `WriteMCPStatus`, `WriteMCPReconnect`,
and `WriteMCPToggle` are write-only free functions for the `mcp_status`,
`mcp_reconnect`, and `mcp_toggle` control requests. They take a caller-supplied
request id, make one newline-terminated structured-JSON write to the held-open
child stdin, and neither read the reply nor close the writer. Reconnect always
carries `serverName`; toggle always carries both `serverName` and `enabled`.
This layer deliberately accepts any server-name string: JSON encoding keeps
quotes, backslashes, newlines, and control bytes as string data on one physical
line, while server membership and caller authorisation remain the routing
layer's responsibility.

The shared `controlRequestInner` must distinguish a field that is inapplicable
to one subtype from a legitimate zero value on another. Plain `string` or
`bool` fields with `omitempty` collapse those states: `enabled:false` would
vanish, and the original non-pointer `ServerName` design would also silently
drop an explicitly supplied empty name even though the writer does no name
validation. `ServerName *string` and `Enabled *bool` preserve those explicit
values while nil keeps every unrelated request byte-identical. Tests need a
false toggle and an empty server name to detect both regressions; non-zero
examples alone stay green under the broken scalar design.

The spellings are schema-backed from Claude 2.1.259, not live-compatibility
evidence. The existing [MCP status capture probe](e2e-realclaude-mcp-status-capture-test-go.md)
had not produced a committed fixture when these writers landed; reply decoding
and the complete routed control path still require live evidence.

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

**Inbound `can_use_tool` decode and its `control_response` answer complete the family (#2282).**
`control_request` had been outbound-only through the three primitives above; the parser's
`case "control_request"` arm now also reads one in. `controlRequestSubtypeLine` decodes just the nested
subtype so an unrecognized one (or an undecodable subtype) falls to `emitUnrecognized` exactly as before;
only a matched `can_use_tool` is re-decoded, off the same line bytes, into `canUseToolLine`'s
`CanUseToolRequest`, then handed to a handler installed via `Parser.SetCanUseToolHandler` — dropped
silently with no handler installed, which is safe only because no spawn yet passes
`--permission-prompt-tool stdio`; #2284 spawns under the flag and installs the answerer. The write half,
`WriteCanUseToolAllow`/`WriteCanUseToolDeny`, mirrors `WriteInterrupt` down to the nil-writer
`ErrNoLiveChild` check, and marshals a `controlResponse` whose allow/deny `PermissionResult` payload
nests one level deeper than the ack envelopes above, per the verbatim capture in
[set-permission-mode-inband-probe.md](set-permission-mode-inband-probe.md).

Two lessons from decoding a shape whose only authority is an SDK type definition, never a captured line:

- **A field the ticket doesn't pin to a scalar is typed `json.RawMessage`, not guessed.** `encoding/json`
  fails the *whole* decode on one wrong scalar guess, and this decode's cost of being wrong is a lost
  permission ask claude is blocking on — the field-typing rule the Interrupt/PermissionMode primitives
  don't need, since every one of their fields is pinned by a measured line. `CanUseToolRequest`'s
  `DecisionReason` and `MatchedAskRule` are exactly the fields this caught: both read like plain strings
  and are sent as objects in the plausible shape.
- **`json.RawMessage` on the write path is not the raw-newline injection surface it looks like.**
  `WriteCanUseToolAllow`'s `updatedInput`/`updatedPermissions` and the deny `message` string are the only
  claude- or caller-supplied bytes this family writes back onto the child's stdin. `encoding/json`
  validates and compacts a `Marshaler`'s output: malformed bytes fail the marshal before anything is
  written, and well-formed bytes lose only insignificant whitespace, never gain a raw newline. The
  appended `'\n'` stays the only one in the line by construction, not by scanning the value first.

The subtype-then-payload split is `controlResponseLine`/`controlAckLine`'s idiom applied to a third
shape, for the reason it was first applied: a field added to one target must not change the other's
decode outcome, and reporting an undecodable `can_use_tool` payload as `UnrecognizedLineType` — the same
frame an unrelated subtype gets — is deliberate, not an oversight: an ask this codec cannot read is still
news the daemon should see.

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
