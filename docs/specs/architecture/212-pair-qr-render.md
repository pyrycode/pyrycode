# #212 — `internal/pair`: `Render` (QR + paste-fallback output)

**Size:** S (PO-confirmed; architect concurs). One new file `render.go`,
one new test file `render_test.go`, one new module dependency
(`github.com/mdp/qrterminal/v3`). Production code is ~50 lines: one
exported function (`Render`), one tiny unexported helper (an error-
tracking `io.Writer` wrapper). 1 new exported name. Zero consumers
wired in this slice — `pyry pair` glue lands in a later phase-3 ticket.
Within all S red lines (≤3 new files, ≤150 prod LOC, ≤5 new exported
names, no consumer cascade — no existing call sites are modified).

**Status:** ready for development.

**Depends on:** #211 (lands `Payload` + `Encode` + `Decode` in the same
package; this ticket consumes both `Payload` and `Encode`). Already on
main as of 2026-05-09 (commit `bc87a7c`). Imports stdlib +
`github.com/mdp/qrterminal/v3` only.

## Files to read first

The developer's turn-1 data load. Each entry is paged in deliberately —
don't grep for them.

- `internal/pair/payload.go` (whole file, 113 lines) — the package this
  spec extends. Read for: package doc-comment style, the `Payload`
  struct shape, the `Encode(p Payload) string` signature this spec
  consumes, and the **token-secrecy contract** documented on the
  package doc and on `Payload.Token`. Render's output contains the
  plaintext token; the secrecy contract from #211 propagates here.
- `internal/pair/payload_test.go` (whole file, ~120 lines) — table-
  driven test patterns to mirror (`t.Parallel()`, sub-tests via
  `t.Run(tt.name, …)`, the `testServerID`/`testRelay`/`testToken`
  constants you can reuse for `render_test.go`).
- `docs/protocol-mobile.md:560-610` — security model items
  `#1 Prompt injection` and `#4 Token leak via phone`. Two specific
  rules bind this ticket: "Paste-fallback is one-time-only" (line 608)
  and "Per-device tokens can leak via … QR screenshots auto-uploaded
  to cloud backup" (line 603). Render is the one-time display surface.
- `docs/protocol-mobile.md:705-714` — appendix "first pairing"
  example. The CLI lines visible there (`==> Server-id: …`,
  `==> Scan QR or paste this:`, …) are illustrative of the eventual
  `pyry pair` command — NOT this ticket's contract. This ticket owns
  only the QR + payload-string + one-line instruction. Surrounding
  framing (server-id summary, relay summary, banner) is the future
  CLI ticket's concern; do NOT add them here.
- `CODING-STYLE.md` § "Error Handling" (`fmt.Errorf("X: %w", err)`,
  sentinel-via-`errors.Is`), § "Testing" (table-driven, stdlib
  `testing` only, `t.Parallel()`, no testify), § "Stdlib over
  dependencies" (justification for the new dep is in this spec).
- The ticket body itself (#212) — six AC bullets. Five map directly to
  test cases / behavior in this slice; the sixth (manual QR scan) is
  PR-description prose, not code.

That's the read budget. The whole package addition is ~50 production
lines.

## Context

Phase 3 needs `pyry pair` to print a QR symbol AND a paste-fallback
string in one shot, so the user can pair via either path: scanning
with the phone camera (fast, but sometimes fails on small terminal
fonts or wide phones), or pasting the string into the mobile app's
pairing screen (always works, slower).

`Encode` from #211 produces the string. This ticket wraps that string
in a QR symbol rendered as UTF-8 half-blocks and prints it together
with the paste-fallback line and a one-line user instruction, all to
one `io.Writer`.

Out of scope, named:

- The `pyry pair` command itself — minting the token, building the
  `Payload`, choosing the `io.Writer` (will be `os.Stdout`), printing
  the surrounding banner ("Server-id: …", warning copy about pairing
  granting code-execution, etc.). Later phase-3 ticket.
- Token generation / hashing / persistence (#208).
- Anything mobile-side: QR alphabet selection, error-correction
  trade-offs, app-side decoding. Owned by the mobile app and the
  protocol doc, not by this Go module.

Pure side-effect: writes to a writer. No goroutines, no shared state,
no global vars.

## Design

### Package placement

Same package: `internal/pair`. Per CODING-STYLE "one package per
concern" — pairing is the concern, encode/decode (#211) and render
(#212) are siblings under the same concern. Mirrors the `Payload`
doc-comment plan from #211 ("future siblings in the same package land
naturally — QR/paste rendering (#212), the `pyry pair` command, …").

One new file:

```
internal/pair/
  payload.go        (#211, on main)
  payload_test.go   (#211, on main)
  render.go         NEW — Render
  render_test.go    NEW — same-package tests
```

Splitting `Render` into a separate file (rather than appending to
`payload.go`) keeps the pure-function payload codec readable and lets
the QR-library import sit alone where the dependency justification
applies. Same pattern as `internal/sessions/pool.go` vs
`internal/sessions/rotation/watcher.go` — concerns split by file when
they have distinct dependency footprints.

### Exported surface

```go
package pair

import (
    "fmt"
    "io"

    "github.com/mdp/qrterminal/v3"
)

// Render writes a paired-device-friendly representation of p to w:
//
//   1. A QR symbol of Encode(p), drawn with UTF-8 half-block
//      characters (the densest terminal form that scans reliably).
//   2. A blank line.
//   3. The output of Encode(p) on its own line.
//   4. A one-line instruction telling the user to either scan the
//      QR with the Pyrycode mobile app or paste the string above
//      into the app's pairing screen.
//
// Render returns the first error encountered while writing to w; on
// error, w may have received a prefix of the intended output but the
// error is propagated rather than swallowed. Render does not retry.
//
// Render does not log, persist, or otherwise duplicate the rendered
// bytes — its output contains the plaintext device-token and is a
// one-time display surface (see docs/protocol-mobile.md § "Token
// leak via phone"). Callers MUST NOT log the writer's destination,
// MUST NOT capture this output into any context that is persisted
// (CI logs, telemetry, error reports), and MUST treat the calling
// goroutine's stdout as the only intended sink.
func Render(p Payload, w io.Writer) error
```

That's the only new exported name in the package.

### Rendering pipeline

Render does four ordered writes through a single error-tracking
wrapper:

1. **QR symbol.** Call
   `qrterminal.GenerateHalfBlock(Encode(p), qrterminal.M, &tw)` where
   `&tw` is the error-tracking wrapper around `w`. `GenerateHalfBlock`
   draws the QR symbol using the half-block code points (`▀`, `▄`,
   `█`, space). `qrterminal/v3`'s `Generate*` functions return no
   error — they write to the supplied writer and discard write errors
   internally. The wrapper captures any error from the underlying
   writer so we can surface it after the call returns.
2. **Blank line.** `fmt.Fprintln(&tw)`.
3. **Encoded payload.** `fmt.Fprintln(&tw, Encode(p))`. Calling
   `Encode(p)` a second time is fine — Encode is deterministic and
   pure; #211's `TestEncode_StableField Order` pins this.
4. **Instruction line.** `fmt.Fprintln(&tw, "Scan the QR with the
   Pyrycode mobile app, or paste the string above into the app's
   pairing screen.")` — one line, ASCII, no trailing emoji or color
   codes. Concrete copy is fixed by this spec; the developer should
   not paraphrase.

After all four steps, `return tw.err` — `nil` on success, the first
captured write error otherwise.

### Why `GenerateHalfBlock` and not `Generate`

`qrterminal/v3` exposes two QR drawers:

- `Generate(text, level, w)` — one terminal cell per QR module, using
  `█` and space. Symbol becomes very tall (each row taller than wide
  in typical terminal fonts) but works in narrow terminals.
- `GenerateHalfBlock(text, level, w)` — packs two QR rows per
  terminal row using `▀`/`▄`/`█`/space. Symbol comes out roughly
  square at typical 2:1 terminal cell aspect ratios; scans more
  reliably with phone cameras precisely because the printed shape
  matches QR's expected aspect ratio.

The AC explicitly mentions "the half-block variants emitted by
`qrterminal/v3`," and protocol-mobile.md §3 of the security model
notes the user-experience cost of unscannable QR codes. Pick
`GenerateHalfBlock`.

### Error-correction level: M

`qrterminal.M` (medium, ~15% recovery) is the library default for
`Generate*` and a good fit for the payload size (~120-140 base64
characters → version 5–6 QR symbol, well within terminal width on
80-column-wide displays). `L` shrinks the symbol slightly but is
fragile to terminal font glitches; `Q`/`H` enlarge the symbol and
risk wrapping in 80-column terminals. Locking `M` keeps the symbol
predictable across phone-camera + terminal-font combinations and
matches the protocol doc's advisory framing.

The level constant lives at the top of `Render`'s body as a literal
`qrterminal.M` — no package-level var, no config knob, no exposing
the choice in the function signature. If the trade-off ever needs
revisiting, change it here; not configurable from outside.

### Error-tracking writer wrapper

`qrterminal.GenerateHalfBlock` has signature
`func(text string, l Level, w io.Writer)` — no error return. Internally
it calls `w.Write` and ignores errors. For Render to propagate writer
failures (AC #3), Render passes a small wrapper that captures the
first non-nil error and short-circuits all subsequent `Write` calls:

```go
type errTrackingWriter struct {
    w   io.Writer
    err error
}

func (t *errTrackingWriter) Write(p []byte) (int, error) {
    if t.err != nil {
        return 0, t.err
    }
    n, err := t.w.Write(p)
    if err != nil {
        t.err = err
    }
    return n, err
}
```

Unexported. Lives in `render.go`. Used only by `Render`. Returning
`(0, t.err)` on a sticky error means once the underlying writer
errors, `qrterminal`'s subsequent writes to the wrapper are no-ops
(but qrterminal itself ignores the error anyway — the sticky-error
state is what matters), and any later `fmt.Fprintln` short-writes
into the trap rather than calling the broken writer again.

This is the "natural error-tracking adapter" pattern the stdlib uses
internally (cf. `bufio.Writer.flush` short-circuit on `b.err`).

### What Render does NOT do

- Does **not** call `Decode` on the encoded string before writing.
  Round-trip is #211's invariant (proven by `TestEncode_DecodeRoundTrip`);
  re-running it here would couple this ticket to that test for no
  protocol benefit.
- Does **not** validate `p`. Same posture as `Encode`: the encoder's
  contract is "marshal what you're given," and the renderer's contract
  is "draw what the encoder produced." A zero `Payload` will produce
  a string Decode (and the phone) reject; the failure surfaces on
  scan, not on render. The package doc on `Payload` already steers
  callers to validated inputs.
- Does **not** print a banner, server-id summary, relay summary, or
  warning copy. Those belong to the future `pyry pair` command. AC #2
  pins exactly four output sections in fixed order; do not add a fifth.
- Does **not** add ANSI color codes, emoji, or terminal control
  sequences. The output must be plain ASCII + the half-block code
  points the QR drawer emits. Paste-fallback users in narrow / non-
  TTY contexts (logs piped to a file) need ANSI-free output.
- Does **not** size to terminal width. `qrterminal/v3` produces a
  symbol whose width is fixed by the QR version (encoded length).
  Terminals narrower than the symbol wrap; that is the user's
  concern. Sizing detection is later-ticket scope at best.
- Does **not** persist anything. No file I/O, no logging, no
  copy-to-anywhere. Pure in-memory transform fed to one writer.

### Dependency justification

`github.com/mdp/qrterminal/v3` — MIT, single-call API
(`Generate*(text, level, w)`), built on `rsc.io/qr` (also MIT, by
Russ Cox / the Go team — well-maintained QR encoder). Pulled in
transitively. Approximate dependency footprint:

- `github.com/mdp/qrterminal/v3` — ~200 LOC, 0 transitive deps of its
  own beyond `rsc.io/qr`.
- `rsc.io/qr` — ~1500 LOC, no further transitive deps.

Both are pure-Go, no cgo, stdlib-only otherwise. CODING-STYLE.md
"Stdlib over dependencies" allows external deps when they "provide
significant value (like `creack/pty`)." A QR encoder is not in the
stdlib and writing one is a multi-hundred-LOC undertaking that
duplicates a battle-tested, widely-used library; the dependency
choice is justified by the same criterion as `creack/pty`.

PO already named the suggested library in the ticket; architect
concurs. Add to `go.mod` via `go get github.com/mdp/qrterminal/v3`
during development; the new entries land in `go.mod` and `go.sum`.
The `go.mod`/`go.sum` updates count toward the change diff but are
mechanical, not human-reviewed line-by-line.

### Concurrency model

None. `Render` is a synchronous function; no goroutines, no shared
state. Multiple concurrent `Render` calls are trivially safe given
distinct `io.Writer` arguments (caller's responsibility).

`qrterminal/v3`'s `Generate*` functions are stateless w.r.t. package
globals (verified at library version selection — not relying on this
across major version bumps; pin to `/v3`).

### Error handling

| Operation | Failure mode | Return |
|---|---|---|
| QR draw (qrterminal.GenerateHalfBlock) | Underlying writer errors during one of the many small Write calls qrterminal makes. | First error captured by `errTrackingWriter`; surfaced from `Render` after the function returns. |
| Blank line write | Writer errors. | First-error-wins via wrapper. |
| Encoded-payload line write | Writer errors. | First-error-wins. |
| Instruction line write | Writer errors. | First-error-wins. |

Render's return value is **the first underlying writer error**, raw —
not wrapped. The protocol AC ("returns an error if the writer fails")
does not demand wrapping with a sentinel, and there is no sentinel
worth introducing: callers will typically not branch on the kind of
writer error (it's "stdout is closed" / "pipe broken" / similar) —
they propagate up to `pyry pair`'s top-level error handler.

There is one concession to the AC's "does not partially-render-and-
swallow" rule: Render does not attempt to wrap the error with
context. A wrapped error like `fmt.Errorf("rendering qr: %w", err)`
would be acceptable, but the bare-error form is simpler and matches
the stdlib idiom for "you handed me a writer; here's what your
writer told me." The AC says "returns an error if the writer fails,"
not "returns an error annotated with context." Pick the simpler
form.

## Testing strategy

`render_test.go`, same package, stdlib `testing` only, table-driven
where helpful.

| Test | What it pins |
|---|---|
| `TestRender_Format_Happy` | AC #4 (the buffer-content assertions). Build a `Payload` from #211's test constants (`identity.ServerID(testServerID)`, `testRelay`, `testToken`), call `Render(p, &buf)`, assert `err == nil`, assert `buf.Len() > 0`, assert the buffer contains at least one QR block code point (test against the union `{"█", "▀", "▄"}` — pass if any one is present), assert the buffer contains `Encode(p)` as a substring on its own line, assert the instruction string is present. |
| `TestRender_FieldOrder` | AC #2 (output ordering). Split the rendered buffer at the `Encode(p)` substring; assert at least one QR block code point appears in the prefix (QR is BEFORE the encoded payload) and the instruction line appears in the suffix (instruction is AFTER the encoded payload). Also assert there is at least one blank line between the last QR row and the encoded-payload line. |
| `TestRender_Deterministic` | Render the same `Payload` twice into two buffers; assert `bytes.Equal`. Pins QR-symbol determinism for the same payload (a phone re-scanning shouldn't see a different symbol) and depends on #211's `TestEncode_StableField Order` for the encoded-string layer. |
| `TestRender_WriterError` | AC #5 (writer-error propagation). Implement an `io.Writer` whose `Write` returns `io.ErrShortWrite` on the first call (sub-cases for first-call-fails and Nth-call-fails are not necessary; the AC pins "first call"). Assert `Render` returns a non-nil error AND `errors.Is(err, io.ErrShortWrite)` (the bare error is propagated, so the equality check is exact). |
| `TestRender_DoesNotPanicOnBrokenWriter` | Belt-and-suspenders for AC #3's "does not panic on writer failure." Variant of `TestRender_WriterError` whose writer panics if called more than once after the first error — pins the `errTrackingWriter` short-circuit. (If the developer's wrapper short-circuits correctly, `qrterminal` and the subsequent `Fprintln` calls never reach the panicking writer.) |

No PTY, no fixtures, no `t.TempDir`. Pure in-memory tests run under
`go test -race ./...` in milliseconds.

**Logging discipline in tests.** No `t.Logf("rendered: %s", buf.String())`
or `t.Logf("encoded: %s", Encode(p))` anywhere. Test failures should
report only:

- "did not contain a QR block code point" (no buffer dump)
- "Encode(p) substring not found in rendered output" (no Encode dump)
- "expected error %v, got %v" where the values are themselves error
  strings, not payload values.

The `TestDecode_Errors` discipline from #211 (error messages contain
no input characters) extends here: test failures must not echo the
`testToken` or any rendered output. The `testToken` constant is a
fixed test-file value (not a real device token), but writing the
discipline into tests exercises the muscle for when this code path
runs against real tokens in the future `pyry pair` integration test.

## Open questions

None. AC, dependency choice, level/half-block selection, and output
copy are all pinned by this spec.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No new external trust boundary. `Render`'s
  inputs come from in-process callers (`pyry pair` will mint the
  Payload; this ticket's tests construct it directly). Output goes to
  a caller-supplied writer; the caller chooses the destination
  (stdout in production, `bytes.Buffer` in tests). The package doc
  comment on `Render` documents the destination-discipline
  expectation: stdout only, no logging, no telemetry capture.

- **[Tokens, secrets, credentials]** SHOULD FIX (addressed in spec).
  The rendered output contains the plaintext device-token (carried
  by `Payload.Token` and embedded in `Encode(p)`'s output, which
  appears both inside the QR symbol and verbatim on the paste-
  fallback line). Three concrete spec decisions defend the
  visibility-once-only protocol rule
  (`docs/protocol-mobile.md:608` "Paste-fallback is one-time-only";
  `:603` "QR screenshots auto-uploaded to cloud backup"):
  1. `Render` does not log, persist, or duplicate its output.
     Pure write-to-supplied-writer; no `slog` calls, no error
     wrapping that would echo bytes, no telemetry. Documented in
     the function doc-comment.
  2. The doc-comment instructs callers explicitly that the output
     destination must be stdout only and must not be captured into
     persisted contexts (CI logs, error reports, etc.). The future
     `pyry pair` ticket will pass `os.Stdout`; if a test ever
     captures output for assertions, the captured bytes must be
     discarded with the test scope (the `bytes.Buffer` goes out of
     scope and is GC'd; this is the standard discipline).
  3. Tests must not `t.Logf` rendered output or Encode results. The
     "logging discipline in tests" subsection above pins this. The
     test-file `testToken` constant is a fixed dummy value, but the
     discipline exists to harden the muscle for the real-token path
     in the future integration test.

  Generation, hashing, and on-disk storage of the token remain out
  of scope (#208 hashes; future ticket mints).

- **[File operations]** Not applicable. `Render` performs no file
  I/O. The output writer is opaque from this package's POV.

- **[Subprocess / external command execution]** Not applicable. No
  subprocess invocation.

- **[Cryptographic primitives]** Not applicable. The QR encoder is
  a transport-layer formatter, not a cryptographic primitive. No
  randomness is generated by this package; the symbol is a
  deterministic function of `Encode(p)`.

- **[Network & I/O]** No network. The only I/O is the caller-supplied
  `io.Writer`. Failure modes (broken pipe, closed file) propagate
  via the `errTrackingWriter` wrapper.

- **[Error messages, logs, telemetry]** No findings. `Render`
  returns the underlying writer's error verbatim, which from any
  realistic writer (`os.Stdout`, `bytes.Buffer`, a `*os.File`) does
  not contain payload bytes — these errors describe *the writer's*
  failure mode, not the data being written. Worst-case theoretical
  custom-writer-that-echoes-bytes-in-its-error is not a realistic
  concern here; the production caller is `os.Stdout`. The
  test-failure logging discipline (above) prevents the test code
  path from leaking the dummy token.

- **[Concurrency]** Not applicable. Synchronous function, no
  goroutines, no shared state.

- **[Threat model alignment]** The relevant
  `docs/protocol-mobile.md` § Security model items for this slice:
  - **#4 Token leak via phone** (lines 600-610): the rendered output
    is the visibility-once display surface for the token. Defenses
    above (no logging, no persistence, no capture) align with the
    protocol's "one-time-only" rule. The QR-screenshot exposure
    surface is the user's risk, mitigated by the mobile app's
    encrypted-storage requirement (out of scope here).
  - **#1 Prompt injection** (lines 560-580): not applicable. No
    user-controlled bytes from the phone flow through Render — the
    payload is server-generated.
  - **#2 Server-id race**, **#3 Relay operator MITM**: not
    applicable. Render is a local display; no network exposure.
  - **#5 Implementation bugs** (memory safety, weak randomness):
    pure-Go, no cgo (`mdp/qrterminal/v3` and `rsc.io/qr` are
    pure-Go); no randomness generated here. Standard Go memory
    safety applies.

  The dependency footprint adds two third-party modules
  (`mdp/qrterminal/v3` ~200 LOC, `rsc.io/qr` ~1500 LOC). Both are
  small, pure-Go, MIT-licensed, widely used. No supply-chain
  defenses beyond `go.sum` checksum verification (which Go provides
  by default). Pinning to specific versions in `go.mod` is the
  standard practice and is what `go get` produces.

**Reviewer:** architect (self-review per `agents/architect/security-review.md`)
**Date:** 2026-05-09
