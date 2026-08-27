# #1689 — Write an `initialize` control request onto the live child's held-open stdin

**Ticket:** https://github.com/pyrycode/pyrycode/issues/1689
**Size:** s (2 production files, 0 consumer call sites, 3 error branches)
**Split from:** #1646. Blockers #1763 / #1764 / #1692 all closed.

---

## Files to read first

Read these before writing anything. This is the turn-1 data load; everything the
design needs is in it.

| Path | Symbols | What to extract |
|---|---|---|
| `internal/streamsup/envelope.go` | `marshalBypassRevocationEnvelope`, `WriteBypassRevocation` | **The shape to mirror field for field.** Nil-check-first ordering, the `fmt.Errorf("streamsup: <verb> <name>: %w", err)` wrapping, exactly one `Write`, never close `w`. |
| `internal/streamsup/envelope.go` | `controlRequest`, `controlRequestInner` | The two structs the new marshaller reuses **unchanged**. Read the `omitempty` paragraph on `controlRequestInner` — it explains why the `initialize` line comes out clean with no new field. |
| `internal/streamsup/runner.go` | `RevokeBypass`, `Interrupt`, `nextControlID`, `Stdin` | The three-line method body, the shared atomic counter, and the fact that `Stdin` releases `r.mu` **before** returning (so the blocking write never holds it) and returns nil between spawns. |
| `internal/streamsup/envelope_test.go` | `TestMarshalBypassRevocationEnvelope`, `decodedControlRequest`, `errWriter` | The byte-exact test pattern: `const want` with a fixed id, newline count, terminator check, round-trip decode. `decodedControlRequest` and `errWriter` are **reused, not re-declared.** |
| `internal/streamsup/envelope_test.go` | `TestWriteBypassRevocation_NilRefusal`, `TestWriteBypassRevocation_WritesEnvelope`, `TestWriteBypassRevocation_WriteError` | The three writer tests to mirror one-for-one. |
| `internal/streamsup/interface_test.go` | `TestRunner_RevokeBypass_NoLiveChild`, `TestRunner_RevokeBypass_LiveChildDelivers` | The live-child harness (`helperRunCfg` with the `echo_lines` child, `runInBackground`, `waitForContains`, `safeBuffer`, `findEchoedLines`) **and the distinct-`request_id` assertion** — that assertion is load-bearing here, see § Testing strategy M2. |
| `internal/streamsup/envelope_test.go` | `TestMarshalInterruptEnvelope` | AC 4's first guard. Read it so you know what must **not** change. |
| `docs/knowledge/features/streamsup-package.md` | § "Bypass revocation send primitive (#1603)" | The interface-placement rule (`Interrupt` off `sessions.Runner`, `RevokeBypass` on it, and *why*) and the one-shared-counter rationale. This is where the prior slice's reasoning lives. |
| `internal/e2e/realclaude/testdata/initialize_control_v2.1.239_before_first_turn.json` | the `control_request_sent` object | The recorded capture. **Read it once, transcribe the literal into a comment, and never reference this path from a `streamsup` test** — see § Design, "The capture is copied, not read". |

---

## Context

`internal/streamsup` writes two `control_request` subtypes onto the live child's
held-open stdin today: `interrupt` (#1120) and `set_permission_mode` (#1604).
Both are marshalled structured — never string-concatenated — so the envelope is
one physical line by construction, and both are pinned byte for byte against a
line a real claude accepted.

This slice adds a third subtype, `initialize`. That request is what makes claude
report the session's model list (concrete identifiers, display names, supported
reasoning-effort levels); the same response carries the slash-command list
(#1683).

**This slice writes the line and stops.** Reading the answer is a separate
concern, exactly as `WriteBypassRevocation` writes without reading its ack. There
is no caller in the daemon and none is added here — the trigger lands with the
publishing slice.

**No ADR is warranted.** Every design decision here is an application of a rule
the package already documents (structured encoding, one shared correlation
counter, interface placement by consumer location). Nothing new is being decided.

### What measurement already settled

Three arm captures committed by #1763 (re-captured 2026-08-25 against an
authenticated child, **claude 2.1.239**) each record the request under
`control_request_sent`, and all three agree field for field:

```
{"type":"control_request","request_id":"initialize-control-1","request":{"subtype":"initialize"}}
```

(Verified in this spec run against all three committed arm fixtures. The fixture
files are indented, so the recorded *field order* — `type`, `request_id`,
`request.subtype` — is the byte claim, and it matches `controlRequest`'s
declaration order exactly.)

The consequence that drives the whole design: **the accepted line carries no
subtype-specific field.** `request` holds `subtype` and nothing else. The
`omitempty` question the ticket's older Technical Notes flagged as open is closed
by measurement — `controlRequestInner` needs no new field at all.

---

## Design

### Package structure

No new package, no new file. Two production files change:

- `internal/streamsup/envelope.go` — the marshaller + the writer
- `internal/streamsup/runner.go` — the `*Runner` method

### New symbols

```go
// envelope.go
func marshalInitializeEnvelope(requestID string) ([]byte, error)
func WriteInitialize(w io.Writer, requestID string) error

// runner.go
func (r *Runner) RequestInitialize() error
```

That is the entire new surface. **Zero new types**, zero new interfaces, zero new
fields on existing types.

#### `marshalInitializeEnvelope`

Returns the single newline-terminated `initialize` control line for `requestID`.
It builds a `controlRequest` whose `Request` is `controlRequestInner{Subtype:
"initialize"}` — `Mode` left at its zero value, which `omitempty` drops — marshals
it, and appends `'\n'`.

Its doc comment must record the provenance: the line #1763 captured live against
claude **2.1.239**, agreeing across all three arms, with the locally-minted id
substituted for the harness's `initialize-control-1`. Cite the capture by
subtype-and-ticket in prose, not by file path.

> **Do not "fix" the 2.1.220 references elsewhere in this file.**
> `marshalBypassRevocationEnvelope`'s doc says 2.1.220 because that is the version
> #1595 measured *that* line against. Two different measurements, two correct
> version tokens. Leave the existing one alone.

#### `WriteInitialize`

Mirrors `WriteBypassRevocation` exactly:

1. `if w == nil { return ErrNoLiveChild }` — **first**, before anything else, so
   there is no panic and no partial write.
2. `marshalInitializeEnvelope(requestID)`; on error wrap as
   `"streamsup: marshal initialize: %w"` (not reachable with fixed literals;
   defensive, same as its two siblings).
3. One `w.Write(env)`; on error wrap as `"streamsup: write initialize: %w"`.
4. Return nil. **Never close `w`.** The `io.Writer` parameter type is what forbids
   a half-close/EOF forgery structurally — this is held by the type, not by a
   test, and the doc comment should say so (as `WriteInterrupt`'s already does) so
   the absent assertion is not read as a missing one.

`requestID` must be a locally-minted id; `Runner.nextControlID` is the only source
that satisfies it, and `RequestInitialize` is the only in-repo caller. State this
in the doc comment as `WriteBypassRevocation` does.

The `control_response` ack is **not** read here. Nothing correlates the
`request_id` yet, and the parser already consumes control responses content-free
(#1500), so the reply is handled without being interpreted. Say this in the doc
comment.

#### `(*Runner).RequestInitialize`

```go
func (r *Runner) RequestInitialize() error {
	return WriteInitialize(r.Stdin(), r.nextControlID())
}
```

Placement in `runner.go`: immediately after `RevokeBypass`, before
`nextControlID` — keeping the three control-request methods contiguous above the
counter they share.

**Naming.** `RequestInitialize`, not `Initialize`. On a type that already has
`New`, a bare `Initialize()` reads as "initialize the runner", which is the
opposite of what it does — it asks the *child* to initialize. `RequestInitialize`
names the act (send the initialize control request) and pairs with the
`control_request` wire type. It returns `error` only, mirroring `RevokeBypass`;
the minted id is deliberately not returned (see § Open questions).

**Not on `sessions.Runner`.** Do not widen that interface. The placement rule the
package documents is *by consumer location*, and this subtype has no consumer at
all yet, so an interface method would be a seam with nothing on the far side of
it — and it would pull every fake runner under `internal/sessions` into the diff.
`RevokeBypass` is on the interface only because its consumer is
`Pool.UpdateSettings` *inside* `internal/sessions`, where a structural type
assertion would fail open. Neither condition holds here. Record the reasoning in
the method's doc comment, contrasting with both siblings.

### Amendments to existing comments (required, small)

These are not optional polish — each one becomes *wrong* or misleadingly
incomplete the moment the third subtype exists.

1. `controlRequestInner`'s `Subtype` field comment currently enumerates
   `"interrupt" | "set_permission_mode"`. Add `| "initialize"`. An enumeration
   that silently omits a live member is the stale-producer-doc failure mode.
2. `controlRequestInner`'s doc paragraph explaining `omitempty` says "every
   interrupt line would grow a `"mode":""` field". Extend it to name the
   `initialize` line as the second no-mode subtype, and name
   `TestMarshalInitializeEnvelope` alongside `TestMarshalInterruptEnvelope` as
   what holds the tag.
3. `nextControlID`'s doc says "shared by `Interrupt` and `RevokeBypass`". Add
   `RequestInitialize`.
4. In `envelope_test.go`, `decodedControlRequest`'s comment says "an interrupt
   line omits the key entirely". Make it "an interrupt or initialize line".

### The capture is copied, not read

Follow the convention `TestMarshalInterruptEnvelope` and
`TestMarshalBypassRevocationEnvelope` already set: a `const want` string literal
with a fixed id, and a doc comment citing the capture in prose.

**Do not read `internal/e2e/realclaude/testdata/initialize_control_v*.json` from a
`streamsup` test.** That family is rewritten in place by a credentialled
`make e2e-realclaude` and nothing gitignores it (#1764), so a cross-package read
would put a hermetic `make check` at the mercy of whatever the operator's last
live run wrote.

The `want` cannot match the capture's `request_id` either: the capture's id is the
harness's `initialize-control-1`, while `nextControlID` mints decimal counter
values. Byte-exactness means the fields, their order, and their values **with the
id substituted** — the same caveat `TestMarshalBypassRevocationEnvelope`'s doc
already states.

### No refactor of the two existing marshallers

Three near-identical marshal+write pairs will exist after this change. Extracting
a shared `marshalControlEnvelope(requestID, inner)` helper is **out of scope** —
it would rewrite two working, byte-pinned code paths for a third that costs
twenty lines to add, widening the diff and putting AC 4's untouched-literals
guarantee at risk for no behavioural gain. Mirror the pattern; do not consolidate
it. (Flagging it here so the duplication reads as a decision rather than an
oversight at review.)

### Data flow

```
   (no production caller — the trigger lands with the publishing slice)
                    │
                    ▼
      (*Runner).RequestInitialize()
                    │
        ┌───────────┴────────────┐
        │                        │
   r.Stdin()               r.nextControlID()
   (nil between spawns;     (atomic controlSeq,
    releases r.mu before     shared with Interrupt
    returning)               and RevokeBypass)
        │                        │
        └───────────┬────────────┘
                    ▼
        WriteInitialize(w, id)
                    │
         w == nil ──┴──► ErrNoLiveChild   (0 bytes written, no panic)
                    │
                    ▼
      marshalInitializeEnvelope(id)
                    │
         marshal err ┴──► "streamsup: marshal initialize: %w"
                    │
                    ▼
       one physical line + '\n'
                    │
                    ▼
              w.Write(env)          ── never Close ──
                    │
         write err ─┴──► "streamsup: write initialize: %w"
                    │
                    ▼
      child's held-open stdin (FIFO pipe)
                    │
                    ▼
   claude replies control_response — NOT read by this slice
   (parser consumes it content-free, #1500)
```

---

## Concurrency model

**No new goroutines, no new state, no change to the shutdown sequence.**

- `RequestInitialize` is safe from any goroutine, exactly as `Interrupt` and
  `RevokeBypass` are.
- `Stdin()` acquires and **releases** `r.mu` before returning the handle, so the
  potentially-blocking `Write` never holds the mutex. Do not restructure this into
  a call that holds the lock across the write.
- `controlSeq` is an atomic counter; `nextControlID` needs no lock.
- **One counter, not one per subtype.** `request_id` must be unique across all
  in-flight control requests on the stream, not merely within one subtype: two
  per-subtype counters would both start at `"1"` and a future ack-correlator keyed
  on `request_id` could not tell the two acks apart. This is the invariant M2 in
  § Testing strategy pins.
- The teardown race is unchanged and already handled: `Stdin()` captures the
  handle, teardown closes it, then the write happens — that surfaces as a wrapped
  write error (EPIPE), never a panic and never a false ack.

---

## Error handling

Three branches, all in `WriteInitialize`. No new sentinel is introduced.

| Failure mode | Detection | Behaviour |
|---|---|---|
| No live child (before first spawn, between spawns, mid-restart) | `w == nil`, checked first | Return `ErrNoLiveChild`. Zero bytes written — structurally, since the check precedes marshal and write. Retryable; the caller may try again once a child is live. |
| Marshal failure | `json.Marshal` error | Wrapped `"streamsup: marshal initialize: %w"`. Not reachable with fixed literals; defensive, matching both siblings. |
| Write failure (e.g. EPIPE on a pipe closed mid-teardown) | `w.Write` error | Wrapped `"streamsup: write initialize: %w"`. **Must not be mis-reported as `ErrNoLiveChild`** — that sentinel is the retryable one and a genuine pipe failure is not. |

`RequestInitialize` adds no branch of its own; it propagates whatever
`WriteInitialize` returns.

---

## Testing strategy

All of it lands under `make check` — no live claude run is needed to accept this
slice, and the ticket deliberately carries no `needs-real-claude` label. Existing
helpers are reused, not re-declared: `decodedControlRequest`, `errWriter`,
`helperRunCfg`, `runInBackground`, `waitForContains`, `safeBuffer`,
`findEchoedLines`.

### `internal/streamsup/envelope_test.go`

- **`TestMarshalInitializeEnvelope`** *(AC 1)* — mirrors
  `TestMarshalBypassRevocationEnvelope`:
  - `const want` = the recorded line with `"fixed-id"` substituted for the
    capture's `initialize-control-1`, plus `"\n"`.
  - Output equals `want` byte for byte.
  - `bytes.Count(out, []byte{'\n'}) == 1` — exactly one raw newline.
  - `out[len(out)-1] == '\n'` — and it is the trailing terminator.
  - Round-trip: decoding `out[:len(out)-1]` into `decodedControlRequest` recovers
    `type: control_request`, `request.subtype: initialize`, `request_id: fixed-id`.
  - **One hostile-id case** (required — added by the § Security review pass; see
    finding 1). Marshal once more with an id that carries a raw newline and a
    forged follow-on object — e.g. `"1\n{\"type\":\"result\",\"subtype\":\"success\"}"`
    — and assert: still exactly one raw newline, still `type: control_request`,
    still `request.subtype: initialize`, and the id round-trips to the input
    verbatim. Two or three lines inside the same test function; do **not** build a
    full table.
  - Doc comment cites the #1763 capture, claude 2.1.239, three arms agreeing, and
    the id-substitution caveat.

- **`TestWriteInitialize_NilRefusal`** *(AC 3, writer half)* — `WriteInitialize(nil,
  "id")` returns an error satisfying `errors.Is(err, ErrNoLiveChild)`. The
  "zero bytes" clause is inherent rather than separately assertable: the condition
  *is* `w == nil`, so there is no sink to observe. What makes the assertion real is
  that the nil check precedes marshal and write — a mutant that reorders it panics
  here (M4).

- **`TestWriteInitialize_WritesEnvelope`** — writes into a `bytes.Buffer` and
  compares against `marshalInitializeEnvelope("id")` with `bytes.Equal`. Catches a
  double write.

- **`TestWriteInitialize_WriteError`** — `errWriter{}` sink; asserts non-nil, that
  it is **not** `ErrNoLiveChild`, and that the message contains `"write initialize"`.

### `internal/streamsup/interface_test.go`

- **`TestRunner_RequestInitialize_NoLiveChild`** *(AC 3, runner half)* — a runner
  built with `helperRunCfg` and never run: `Stdin()` is nil, so
  `RequestInitialize()` returns `ErrNoLiveChild` without writing and without
  panicking.

- **`TestRunner_RequestInitialize_LiveChildDelivers`** *(AC 2)* — mirrors
  `TestRunner_RevokeBypass_LiveChildDelivers` structurally:
  - Spawn the `echo_lines` helper child, wait for `READY`.
  - Call `RequestInitialize()`, then `Interrupt()`.
  - Wait for the interrupt echo (`"subtype":"interrupt"`) — stdin is a FIFO pipe,
    so once the second line comes back the first already has. This is the barrier;
    do not add a sleep.
  - Collect `findEchoedLines`, decode each into `decodedControlRequest`, index by
    subtype.
  - Assert the `initialize` line is present, `type == "control_request"`, and
    `request_id != ""`.
  - **Assert the initialize and interrupt `request_id`s differ.** This is not
    decoration — see M2.

### Mutants each assertion is the sole detector for

State these in the test doc comments so the assertions read as load-bearing:

- **M1 — drop `omitempty` from `controlRequestInner.Mode`.** Reddens
  `TestMarshalInterruptEnvelope`, `TestMarshalBypassRevocationEnvelope`, **and**
  `TestMarshalInitializeEnvelope`, since all three lines would grow `"mode":""`.
  This is AC 4's mechanism.
- **M2 — mint the initialize id from a fresh per-subtype counter** (e.g. a new
  `initializeSeq` starting at 1). Every other assertion stays green: the id is
  still non-empty, the byte-exact test uses a fixed literal id, and the nil-refusal
  path never mints. **Only the distinct-`request_id` assertion in
  `TestRunner_RequestInitialize_LiveChildDelivers` catches it.** Without that
  second control write, AC 2's "from the same local source" clause is satisfied by
  construction and pinned by nothing.
- **M3 — reorder `RequestID` after `Request` in `controlRequest`.** Reddens all
  three byte-exact marshal tests.
- **M4 — move the `w == nil` check after the marshal.** `TestWriteInitialize_NilRefusal`
  panics on the nil-interface `Write`.
- **M5 — write the envelope twice.** `TestWriteInitialize_WritesEnvelope` reddens
  on the `bytes.Equal` compare.
- **M6 — build the line by string concatenation** (`fmt.Sprintf` of the literal
  with `%s` for the id) instead of marshalling `controlRequest`. This mutant is
  green against *every* other assertion in this spec — the fixed-id byte compare,
  the newline count, the terminator, the round-trip, the live-child delivery, the
  nil refusal — because a fixed digit id produces identical bytes either way. Only
  the hostile-id case catches it. That is why it is required on this ticket.

### What must stay green, unmodified *(AC 4)*

`TestMarshalInterruptEnvelope` and `TestMarshalBypassRevocationEnvelope` must pass
with their `const want` literals **byte-identical to what is on `main` today**.

AC 4 asks for **no new test** — it is a guard, and the correct outcome is that
neither literal is touched. Since the measured `initialize` line carries no
subtype-specific field, nothing needs adding to `controlRequestInner`, so nothing
grows those two lines. **If you find yourself editing either `want` literal, that
edit is the defect, not the fix** — back out whatever change to
`controlRequestInner` caused it.

### Explicitly not written

- **No full injection-resistance *table* test for the initialize `request_id`** —
  but the single hostile-id case above is required, and the distinction matters.
  `TestMarshalBypassRevocationEnvelope_RequestIDInjectionResistance` runs eight
  rows because the revocation line carries a *second* fixed field (`mode`) that a
  hostile id might try to rewrite; the `initialize` inner is subtype-only, so seven
  of those rows would assert the same property twice. What the eighth carries —
  that this marshaller is structured and not concatenated — is **not** inherited
  from the revocation's table, because a concatenation mutant is per-function
  (M6). One case, inside the byte-exact test, is the minimum that closes it.
- **No fakeclaude change and no e2e-suite change.** fakeclaude already answers this
  subtype unconditionally — `subtypeInitialize` and `controlRequestID`, dispatched
  in `runStreamJSON` (#1692) — and its own comment notes that nothing sends the line
  today. Adding no caller here is exactly what keeps every existing fake-daemon
  suite's bytes unchanged.
- **No `sessions.Runner` change, no fake-runner change** anywhere under
  `internal/sessions` or `cmd/pyry`. If your diff touches either, the interface got
  widened by accident.

---

## Open questions

1. **Should `RequestInitialize` eventually return the minted `request_id`?** The
   reader slice will need it to correlate the `control_response`. This slice
   returns `error` only, mirroring `RevokeBypass` — building the correlation seam
   now would be a return value with no reader, and changing a signature with one
   caller later is cheap. **Decision for this slice: `error` only.** Left here so
   the publishing slice inherits the question rather than re-deriving it.

2. **Where the trigger goes** (before the first turn vs. after a completed turn) is
   settled but out of scope: #1763 recorded `control_response_within_wait: true`,
   a `success` subtype, a matched `request_id`, and a 6-entry model list at *both*
   send points, and #1764 found `control_responses` to be the only per-turn field
   that ever differs from the no-request control arm — asking does not perturb the
   session. Recorded so the slice that places the trigger inherits it.

---

## Security review

**Verdict:** PASS *(first pass returned one MUST FIX; revised inline and re-walked)*

The asset under review is **the daemon's held-open pipe to its own claude child**.
That pipe is privileged: a second physical line on it forges a `result` (a fake
turn-end), a `control_request` (an interrupt or a permission-mode change), or a
permission approval. Every finding below is measured against "can this slice put a
second line, or a wrong line, on that pipe."

**Findings:**

- **[Trust boundaries] MUST FIX — addressed inline before commit.** The design has
  exactly one value crossing into the envelope: `requestID`. Every other field is a
  fixed Go string literal, and the only in-repo source of `requestID` is
  `nextControlID`, which mints decimal digits from an atomic counter — no untrusted
  data reaches this line today. The hole was in the *pin*, not the design: as first
  written, the spec's test set was green against a `marshalInitializeEnvelope`
  implemented by string concatenation (M6 in § Testing strategy), because a
  fixed-digit id produces byte-identical output either way. Concatenation is what
  the package's named invariant — "structured encoding, never string
  concatenation" — exists to forbid, and `WriteInitialize` is **exported**, so a
  later caller passing an attacker-influenced id would make it live. The
  revocation's existing table does not cover it: a concatenation mutant is
  per-function. Fixed by requiring one hostile-id case inside
  `TestMarshalInitializeEnvelope` (newline-bearing id → still one raw newline,
  `type` and `subtype` intact, id round-trips verbatim). Re-walked after the
  revision: no remaining MUST FIX.

- **[Tokens, secrets, credentials] No findings.** No token, key, or credential is
  created, stored, transmitted, or compared. `request_id` is a correlation id, not
  a capability: it authorises nothing, it is deliberately guessable (a monotonic
  counter), and the channel's authority comes from being the daemon's own pipe to
  its own child rather than from anything in the line. `crypto/rand` is therefore
  correctly absent — see [Cryptographic primitives].
  **OUT OF SCOPE:** the `control_response` this request elicits carries the
  session's model list and slash-command list. Whether any of that is loggable is
  the reader slice's question, not this one — this slice performs no read.
  #1764's redaction work on the captured artifacts is the precedent that slice
  should follow.

- **[File operations] No findings.** No path is constructed, no file opened, no
  mode chosen. The one file-adjacent decision is a negative one and it is a
  test-integrity control, stated in § Design: a `streamsup` test must not read
  `internal/e2e/realclaude/testdata/initialize_control_v*.json`. That family is
  rewritten in place by a credentialled `make e2e-realclaude` and nothing
  gitignores it (#1764), so a cross-package read would make the hermetic
  `make check` gate depend on whatever the operator's last live run happened to
  write. The capture is transcribed into a `const want` instead.

- **[Subprocess / external command execution] No findings.** No `exec.Command`, no
  argv, no environment. The child already exists and its launch is untouched. The
  relevant subprocess-adjacent hazard is a **half-close**: writing EOF to a
  stream-json child's stdin means "no more input" and would take the live session
  down without killing it — an availability attack reachable from inside the
  process. The design forbids it *structurally*, not by convention: the parameter
  is `io.Writer`, which has no `Close`. This is deliberate and must not be widened
  to `io.WriteCloser` or `*os.File` for convenience.

- **[Cryptographic primitives] Not applicable — by design, not by omission.**
  Nothing here is security-bearing randomness. `nextControlID` uses an atomic
  counter rather than `crypto/rand` precisely because `request_id` is a correlation
  handle, not an unguessable nonce; making it random would imply an
  authorisation property the channel does not have and does not need. No hashing,
  no key material, no comparison against a secret, so no
  `crypto/subtle.ConstantTimeCompare` site exists.

- **[Network & I/O] No findings.** No socket, no listener, no timeout surface. The
  slice performs exactly one bounded write of a fixed-shape line (~98 bytes plus
  the id) and zero reads, so there is no input-size cap to specify and no
  slow-loris surface. Resource exhaustion: nothing rate-limits
  `RequestInitialize`, and a caller could in principle spam it. Two things contain
  that — the write blocks only the calling goroutine, because `Stdin()` releases
  `r.mu` *before* returning the handle, so a full pipe never stalls the runner; and
  there is **no caller at all** in this slice. Adding a limiter now would be a
  defence for an unobserved failure mode against a caller that does not exist.
  **OUT OF SCOPE:** send-rate policy belongs to the publishing slice that places
  the trigger.

- **[Error messages, logs, telemetry] No findings.** Both wrapped errors —
  `"streamsup: marshal initialize: %w"` and `"streamsup: write initialize: %w"` —
  are fixed strings; neither interpolates `requestID`, the envelope, or any
  payload. A pipe-write failure surfaces the stdlib error, which is the exposure
  the two existing writers already have and contains no secret. The slice adds no
  `slog` call, and that absence is deliberate rather than an oversight: a
  fire-and-forget write that returns its error has nothing to log that the caller
  cannot log with more context. No telemetry, no metrics, no user-identifiable
  data.

- **[Concurrency] OUT OF SCOPE — named, with an owner.** There is one real hazard
  on this pipe and it is pre-existing: `WriteTurn`, `WriteInterrupt`,
  `WriteBypassRevocation` and now `WriteInitialize` all write to the same stdin
  handle with **no mutex serialising them** — `Stdin()` deliberately releases
  `r.mu` before returning. A pipe write at or below `PIPE_BUF` is atomic on both
  supported platforms, and the initialize line is far below it, so the initialize
  line itself can never be split. It can, however, be interleaved *into* a larger
  non-atomic `WriteTurn` (an attachment-bearing prompt easily exceeds `PIPE_BUF`),
  corrupting the turn envelope. The consequence is a malformed line claude's parser
  drops — an availability bug, not a forgery, since neither writer controls the
  other's bytes. This slice adds no caller, so it cannot trigger the interleave;
  the publishing slice that places the trigger is where the question becomes live
  and is the correct owner.
  The check-then-use gap in `RequestInitialize` (capture the handle via `Stdin()`,
  teardown closes it, then write) is **benign and stays benign**: `os.File.Close`
  marks the file closed in the runtime's poll layer, so a subsequent write returns
  an error rather than reaching a numerically-reused descriptor. The captured
  handle cannot write into a successor child — the failure mode is a dropped
  request surfaced as a wrapped error, never a write to the wrong child, never a
  panic, never a false ack.
  Lock ordering is not a question: exactly one lock is touched, inside `Stdin`, and
  it is released before the write. No goroutine is spawned, so nothing can leak.

- **[Threat model alignment] No findings.** `docs/threat-model.md` does not exist
  and `docs/protocol-mobile.md` § Security model is relay-scoped — no wire, no
  phone, no paired device is involved here. The governing threat is the one
  `internal/streamsup` names for itself and which `marshalTurnEnvelope`'s doc
  states: an untrusted prompt forging a second stream-json line on claude's stdin.
  This slice does not widen that surface — the `initialize` line carries **no
  free-text field at all**, so it has no injection surface of its own, and the
  measurement that closed the `omitempty` question (§ Context) is what guarantees
  no such field gets added. The one residual — that the *pin* on the marshaller's
  structuredness was missing — is finding 1, fixed.

**Reviewer:** architect (self-review per the security-review checklist)
**Date:** 2026-08-25
