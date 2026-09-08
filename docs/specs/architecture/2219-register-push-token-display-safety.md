# #2219 — `register_push_token`'s client-authored fields get a display-safety gate

## Files read

- `internal/relay/handlers/register_push_token.go` → `RegisterPushToken`, `msgMalformed`,
  `replyError` — the handler this ticket gates; its existing four reject branches, the
  dedupe comparison the new checks must precede, and the `SECURITY` block whose claim
  about `Device.Name` this ticket changes.
- `internal/relay/handlers/rename_workspace.go` → `RenameWorkspace`,
  `msgRenameWorkspaceBlankLabel`, `msgRenameWorkspaceLabelTooLong` — **the shape this
  ticket copies**: value guards in the handler, one static message per reject branch,
  the malformed branch logging neither the payload nor the decode error, and validation
  placed ahead of the lookup it protects.
- `internal/protocol/workspace.go` → `RenameWorkspacePayload`, `MaxWorkspaceLabelBytes` —
  **decides where the gate goes.** Its type block states the package rule: a decode-time
  check is "not a general licence", justified only for a field with no downstream
  validator, and "keeping every malformed branch together in the handler is what lets
  each carry its own static message."
- `internal/protocol/pairing.go` → `MaxDeviceNameBytes`, `MintPairingPayload`,
  `MintPairingPayload.UnmarshalJSON` — the byte bound this ticket reuses for the same
  label, the refuse-never-truncate posture, and the "a length ceiling is not a safety
  property" warning that is exactly why a second check is needed.
- `internal/protocol/push.go` → `RegisterPushTokenPayload` — the payload gaining a gate;
  bounds nothing and validates nothing today. It stays a pure DTO.
- `internal/relay/v2session_mint_pairing.go` → `mintLabelIsDisplaySafe` — the refused
  character set this ticket must match exactly, and the doc block whose reasoning
  (why C0 including LF/CR/ESC, why DEL, why C1, why UTF-8 validity is not checked, why
  the empty string passes) transfers unchanged.
- `internal/sessions/systemprompt.go` → `admissibleClientField` — the second existing
  copy. It refuses `"` and invalid UTF-8 on top of the shared set; **this ticket must
  not copy those two**, which is why the accept table pins a `"` as admissible.
- `internal/devices/registry.go` → `Registry.UpdatePushRegistration` — the write site.
  `RegisterPushToken` is its only production caller, which is what makes one gate in the
  handler cover every sink.
- `cmd/pyry/pairing_mint_v2.go` → `pairingMinterV2.MintPairing` — the seam comment whose
  "the REQUESTING name has no such gate" / "PRE-EXISTING AND TRACKED IN #2219, NOT CLOSED
  HERE" paragraphs become wrong when this lands, and must be corrected in the same PR.
- `docs/knowledge/features/devices-registry.md` § "Phase 3 foundation (#250)" — records
  that `Device.Name` is remote-authored with no shape check on this path, and that the
  fix is one gate at the write site rather than one per consumer.
- `docs/knowledge/features/relay-package-handlers.md` § log table — the existing five
  `register_push_token.*` log records, so the two new ones join a documented set.
- `docs/protocol-mobile.md` § "Application message types" and § "Minting a pairing from a
  paired client" → the empty `register_push_token` row, and the `device_name` contract
  prose the new row's language mirrors.

## Context

`devices.Device.Name` is remote-authored on one path and has been since #250: a paired
client's `register_push_token` carries a `device_name`, the handler passes it to
`devices.Registry.UpdatePushRegistration`, and that overwrite is deliberate — the phone
is the source of truth for its own name. Nothing between the wire and the sinks checks
its shape, so a device that names itself `"kitchen\nfake log line"` can forge a line in
the daemon's line-oriented log, an entry in the forensic audit sink
(`audit.Entry.DeviceLabel`, filled from `Device.Name` by `auditQuestion`, both
`modalResolverV2` sites and `pairingMinterV2.auditMint`), and a row of `pyry pair list`
on an operator's terminal.

The frame's `platform` carries the same hazard on a narrower path: client-authored, its
own type block defers validation to a dispatcher that performs none, and it is logged
verbatim in the write branch's record. `token` needs no check — the handler's `SECURITY`
block establishes it is never logged as a field value, and its only other sink is a JSON
string inside `devices.json`, which `encoding/json` escapes.

#2127 gated the *other* door — the label a client asks the daemon to mint — at the trust
boundary, in `mintLabelIsDisplaySafe`. It could not close this one, and its own seam
comment says so and names this ticket.

**No ADR.** This adds no new architectural seam; it applies a rule two neighbouring
verbs already publish (`rename_workspace`'s handler-side value guards,
`mint_pairing`'s display-safety predicate) to a third. The documentation phase folds the
lesson into the package overviews.

## Design

### One gate, in the handler, before the dedupe

`RegisterPushToken` is the only production caller of
`devices.Registry.UpdatePushRegistration`, so a check placed in the handler *is* the
check at the write site — the "one gate where the value enters the registry" the ticket
asks for. Three guards run after the JSON decode and **before** the dedupe comparison:

1. `len(p.DeviceName) > protocol.MaxDeviceNameBytes` → refuse.
2. `!pushFieldIsDisplaySafe(p.DeviceName)` → refuse.
3. `!pushFieldIsDisplaySafe(p.Platform)` → refuse.

Each answers `protocol.CodeProtocolMalformed` with its own static message and
`Retryable: false`, via the existing `replyError`. Each returns before
`UpdatePushRegistration`, before `Reload` and before `Save`, so a refused frame mutates
no registry row and writes no `devices.json`.

**The ordering is load-bearing, not stylistic.** The dedupe branch acks without writing
when the payload triple equals the stored one. A device whose name was stored before this
gate existed could repeat that unsafe name forever and be acked, never reaching a guard,
if the guards sat after the comparison. Running them first makes "an unsafe value is
refused" true of every frame rather than of every *changed* frame.

### Not a decode hook

`RegisterPushTokenPayload` stays a pure DTO with no `UnmarshalJSON`.
`RenameWorkspacePayload`'s block states the package rule this follows:
`MintPairingPayload`'s decode-time check "is a deliberate departure from this package's
pure-DTO posture… not a general licence", justified by that field having no downstream
validator. This field has one — its handler — and keeping every malformed branch in the
handler is what lets each carry its own static message.

### The predicate: restated, not shared

```go
func pushFieldIsDisplaySafe(v string) bool  // refuses C0, DEL, C1; empty passes
```

Third copy of the same loop. Sharing was weighed and rejected on both available routes:
importing `internal/relay` from `internal/relay/handlers` puts a child package's
dependency on its parent in place for a six-line loop, and exporting it from
`internal/protocol` contradicts `RenameWorkspacePayload`'s stated reason for keeping
validation out of the DTO package — and would mean rewriting #2127's just-landed security
gate as a side effect of this ticket. The refused set must match `mintLabelIsDisplaySafe`'s
exactly, so the doc block names the other two copies and states that the set moves in all
three or in none.

It must **not** inherit `admissibleClientField`'s two extra rules: that function refuses
`"` (for a system-prompt rendering it owns) and invalid UTF-8 (which `encoding/json`
already makes unreachable). A `"` in a device name is admissible here, and the accept
table pins it.

### Refusal messages

Three new constants beside `msgMalformed`, one per branch, mirroring
`rename_workspace`'s four. Naming the offending field is **not an oracle**: the client
authored both values, so a refusal tells it nothing it did not already hold — the
reasoning `pairing.not_permitted` publishes for its own refusal. No message names the
value, its length, or the bound.

### Logging

The three new reject branches log `event` and `conn_id` and nothing else —
`rename_workspace`'s strict posture rather than this handler's existing one. The stored
`dev.Name` is deliberately *not* logged there: on a device paired before this gate it is
the pre-gate residual this ticket does not clean up, and a new line is not the place to
surface it.

The existing malformed branch **drops its `"err", err` field** (AC-3). `encoding/json`
quotes offending input into its error text, and a type error midway through a well-formed
object returns an error with fields already populated from supplied bytes.

### The seam comment correction must name what is left, not just what closed

`pairingMinterV2.MintPairing`'s block currently says the requesting name "has no such
gate" and that the residual is "TRACKED IN #2219, NOT CLOSED HERE". Both sentences become
wrong. The replacement must not merely flip them to "handled" — that is the exact
false-confidence shape the original paragraph argued against. It states instead that the
`register_push_token` door is now gated, and that `Device.Name` is **still not
display-safe as a type invariant**, for two reasons this ticket does not close:

- a name stored in `devices.json` **before** this gate existed is read back unchecked
  (explicitly out of scope), and
- `pyry pair --name` is operator-authored and deliberately ungated —
  `mintLabelIsDisplaySafe`'s own block states why the check does not live in the shared
  mint step.

So a consumer still owes its own escaping. The correction says that, or it replaces one
wrong claim with another.

### `platform` gets no byte bound

AC-1 asks for the character classes on `platform`; AC-2 bounds only `device_name`.
Inventing a `MaxPlatformBytes` would publish a constant this ticket cannot anchor. The
residual ceiling is the ~65519-byte application-envelope cap — the same posture
`HelloClientPayload` is documented as taking at `maxRetainedClientNameBytes`.

## Concurrency model

No goroutines, no new shared state. The handler stays stateless beyond its closure
capture; the guards are pure functions of the decoded payload. The reject branches return
before `reg`'s mutex is ever taken, so a refusal is strictly less concurrent work than
today's dedupe path.

## Error handling

| Condition | Code | Retryable | Registry | Disk |
|---|---|---|---|---|
| Payload undecodable | `protocol.malformed` | no | untouched | unwritten |
| `device_name` over `MaxDeviceNameBytes` | `protocol.malformed` | no | untouched | unwritten |
| `device_name` carries C0/DEL/C1 | `protocol.malformed` | no | untouched | unwritten |
| `platform` carries C0/DEL/C1 | `protocol.malformed` | no | untouched | unwritten |

Refused, never truncated or repaired: a shortened or scrubbed name is a name the client
did not send, on a frame whose whole subject is which label a device is filed under.

**The refusal is permanent for that device** until it renames itself — the phone sends
this frame on every WS connect, and the reject is non-retryable, as the existing
malformed branch already is. That is the fail-closed direction and is accepted.

## Testing strategy

New tests in `internal/relay/handlers/register_push_token_test.go`, table-driven, stdlib
only, matching the file's existing fixture (`newTestConn`, `makeRequest`,
`assertEnvelopeShape`):

- **Refused `device_name` shapes** — NUL, TAB, LF, CR, ESC, U+001F, DEL, U+0085, U+009F,
  each embedded mid-string. Each asserts `protocol.malformed`, `Retryable == false`, the
  registry row's `Name`/`Platform`/`PushToken` unchanged, and `devices.json` absent.
- **Refused `platform` shapes** — the same assertions on the narrower field.
- **Boundary table** — `MaxDeviceNameBytes` bytes accepted and stored; one byte more
  refused. Pins the bound rather than asserting a number twice.
- **Character-set edges** — U+001F refused / U+0020 accepted, U+007E accepted / U+007F
  refused, U+009F refused / U+00A0 accepted, `"` accepted. These are what redden if the
  predicate is copied from `admissibleClientField` or if a range edge slips by one.
- **Unsafe value equal to the stored one is refused, not deduped** — the AC-3 ordering
  test. The fixture device carries a pre-gate unsafe `Name`; the payload repeats it
  verbatim. A guard placed after the dedupe comparison acks; a guard placed before
  refuses. This is the one test the ordering exists for.
- **Accepted payload stored verbatim** — a name with spaces, a quote and a multi-byte
  rune round-trips byte-for-byte into `Device.Name`, untruncated and unescaped.

Existing behaviour is covered by the eight tests already in the file (first register,
dedupe, changed re-register, reload-before-save, gone-mid-conn, save failure, unauth,
malformed) — all must stay green unmodified, which is the AC-4 proof that nothing else
moved.

Gate: `go test -race ./internal/relay/handlers/... ./internal/protocol/... ./cmd/pyry/...`,
`go vet ./...`, `go build ./cmd/pyry`.

## Documentation

`docs/protocol-mobile.md`'s `register_push_token` row is an empty cell; it gains the
frame's contract — what the three fields are, the 128-byte `device_name` bound, the
refused character classes, `protocol.malformed` as the answer, and that a refusal is a
rejected frame rather than a repaired value. Language mirrors § "Minting a pairing from a
paired client"'s `device_name` row, which publishes the same bound for the same label.

## Open questions

- **Does the error-codes table's `protocol.malformed` row enumerate producing frames?**
  If it does, this frame's new refusals belong in it. Resolve by reading the row in
  Phase B; record the answer under `## Revisions` if it changes what ships.
- **Does the doc carry a per-ticket changelog entry as a convention, or only for wire
  shape changes?** This is a contract publication on an existing frame, not a new type.
  Resolve by reading the changelog's recent entries in Phase B.

## Security review

**Verdict:** PASS (after one MUST FIX revised into § "The seam comment correction must
name what is left, not just what closed" above, then re-walked).

**Findings:**

- **[Trust boundaries] MUST FIX — resolved in the plan before commit.** The design closes
  the `register_push_token` door, but `Device.Name` remains un-invariant afterwards: a
  name written to `devices.json` before this gate is read back unchecked, and
  `pyry pair --name` stays deliberately ungated. Rewriting
  `pairingMinterV2.MintPairing`'s comment to say the hazard is handled would manufacture
  exactly the false confidence its original paragraph argued against. The Design section
  now prescribes what the correction must state. No code change follows from this — it is
  a claim-accuracy fix, and it is the one thing in this ticket a reviewer cannot recover
  from the diff.

- **[Trust boundaries] The ticket's sink enumeration is incomplete, and the gate covers
  the gap for free.** Sweeping every non-test read of `Device.Name` turns up
  `internal/relay` → `V2Session`'s handshake success log (`device_name`, `device.Name`),
  which the issue body does not list alongside the rekey log, the four `audit.Entry`
  fill sites, `pairingMinterV2.MintPairing`'s two records and `pyry pair list`. It needs
  no separate work: one gate at the write site covers a sink nobody enumerated, which is
  the argument for the placement rather than a hole in it. Recorded so a later reader
  does not mistake the issue's list for the complete one.

- **[Tokens, secrets, credentials] No findings — the out-of-scope call on `token` was
  verified rather than borrowed.** `Device.PushToken` has exactly one non-test read
  outside the registry's own assignment: the dedupe comparison in `RegisterPushToken`. It
  is never a log field value, never rendered, and `internal/debugbundle` collects no
  `devices.json`. Its only other sink is a JSON string value inside that file, which
  `encoding/json` escapes. The handler's `SECURITY` block is accurate as written.

- **[Subprocess / external command execution] No findings — checked, not assumed.**
  `Device.Name` reaches no `exec.Command` argument anywhere. The similar-looking system
  prompt path consumes `sessions.ClientIdentity.Name`, which is the `hello` frame's
  self-reported name gated by `admissibleClientField` — a different field on a different
  frame, and unaffected by this ticket.

- **[File operations] No findings.** The reject branches return before `Reload` and
  `Save`, so a refused frame performs zero filesystem operations — strictly fewer than
  today's accept path. No client byte reaches `registryPath`, which is daemon-derived,
  and `device_name` never becomes a path component: `resolveDevicesPath` sanitises the
  *instance* name, never a device name.

- **[Network & I/O] SHOULD FIX, deliberately deferred.** `platform` gains a character-set
  check but no byte bound, so its ceiling stays the ~65519-byte application-envelope cap.
  AC-2 bounds only `device_name`, and a `MaxPlatformBytes` invented here would publish a
  constant with no anchor behind it. The exposure shrinks rather than grows: today an
  oversize `platform` is persisted into `devices.json` on every connect, and after this
  change a control-bearing one is refused before any write.

- **[Error messages, logs, telemetry] No findings.** Three static messages, none naming
  the value, its length or the bound. Distinguishing which field failed is **not an
  oracle**: the client authored both, so a refusal tells it nothing it did not already
  hold — `pairing.not_permitted`'s published reasoning, and frames are AEAD-authenticated
  under the paired session so no third party can probe with one. The three new branches
  log `event` and `conn_id` only, and the existing malformed branch loses its decode
  error.

- **[Concurrency] No findings.** No goroutines, no new locks, no check-then-mutate gap:
  the guards read the same local decoded payload the write site is handed, with no re-read
  between them. Both reject branches return before `reg`'s mutex is taken.

- **[Cryptographic primitives] Not applicable — no randomness, key material, or
  comparison against a secret is introduced or touched. The one credential in the frame's
  neighbourhood, `Device.TokenHash`, is read from the already-authenticated
  `dispatch.Conn` and never from the payload.

- **[Threat model alignment] Addressed.** This is `docs/protocol-mobile.md` §
  Attachments' log-injection shape, applied to a second field family. It does not widen
  threat 4 (*token leak via phone*): a compromised client's authority is unchanged, and
  what it loses is the ability to forge a log line. The availability cost is named and
  accepted — the reject is non-retryable and the phone sends this frame on every connect,
  so a device that names itself unsafely has no push registration until it renames.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-08

## Revisions

### 2026-09-08 — the ESC rows carry a bare ESC (verifier finding: `substrate-guard` red)

No design change: the refused set, the three guards, their ordering and the published
contract are unchanged. Only the two ESC **test values** are respelled.

They originally read `kitchen\x1b[31mred` and `fcm\x1b[31m`. `cmd/substrate-guard` bans
the ESC-escape-then-open-bracket source sequence in every `.go` file outside a two-path
allowlist, with no per-line exemption, so both rows reddened the merge gate. The gate
under test refuses ESC as a C0 control on its own — the CSI tail exercised no additional
branch — so the values became `kitchen\x1bpad` and `fcm\x1bpad`, which keep the
mid-string embedding the rest of the table uses. This matches how the sibling test for
the same character set spells its ESC case against `mintLabelIsDisplaySafe`. The `\u`
spelling of ESC would also clear the scan and was rejected: it evades a merge gate rather
than satisfying it.
