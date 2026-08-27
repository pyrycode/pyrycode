# #1772 — sanitise a client-supplied attachment filename into one safe path component

One new exported pure function in `internal/attachments`, one new test file. No
existing file changes. No production caller lands with this slice — #1773 is the
first, and #1744 wires the dispatch site above it.

## Files to read first

Read these in order; the first four are the whole design surface.

- `cmd/pyry/main.go` → `sanitizeName` — **the shape to follow.** The rune-wise
  allowlist, the `_` replacement, the empty-result fallback, and above all the
  comment explaining why the `.`/`..` check runs on the *built result* rather
  than the input. Not importable (unexported, package `main`); copy the shape,
  not the code.
- `cmd/pyry/args_test.go` → `TestSanitizeName` — the table shape this ticket's
  table mirrors, including how it names subtests off the input.
- `internal/protocol/attachments.go` → `MaxAttachmentFilenameBytes` — the bound,
  and its doc comment's reasoning (POSIX `NAME_MAX`, one path component). Read
  the block comment at the top of the `const` group too: it states that all four
  bounds are **producer-side contracts with no validator**, which is why this
  function truncates rather than rejects.
- `internal/protocol/attachments.go` → `AttachmentChunkPayload` — the SECURITY
  block. Extract three things: `Filename` is "a display string and a sanitiser
  input, never a path"; `Filename` is under the same **never-log** rule as
  `Data`; and `AttachmentID` gets a canonical-shape **check**, not this
  treatment.
- `internal/attachments/accumulator.go` → the package comment at the top of the
  file — "In-memory only… the package makes zero log calls." This slice must not
  break either clause. **Do not add a second package comment and do not add a
  `doc.go`**; see § Non-goals.
- `docs/knowledge/features/attachments-package.md` § "Mutation-testing lessons
  (measured across #1769 and #1770)" — this package's testing bar. In
  particular the first lesson: *a row aimed at a property has to use the input
  that is indistinguishable from the property's absence*, or the row looks like
  coverage while leaving the mutant alive. § "What a successful `Assemble` does
  not mean" names this ticket by number and says what a green `Assemble` does
  **not** license.
- `docs/protocol-mobile.md` § Attachments, the `filename` row of the frame table
  and the "**Trust and content hygiene**" paragraph — the published contract
  this function implements one half of.

## Context

`AttachmentChunkPayload.Filename` arrives attacker-chosen on the upload leg. The
published contract calls it "a display string and a **sanitiser input, never a
path**", and this slice is that sanitiser: the single place a client's string
becomes a host-side path component.

It is a **pure function**. No filesystem, no wire dispatch, no logger, no
conversation and no attachment id. It returns one `string` and no error — every
input has a safe answer, so there is nothing to refuse.

The sanitised component **does not replace the client's name**. § Attachments
records that `filename` is stored and echoed back *verbatim* on the retrieval
leg, so the raw string survives for display and the host-side name is a separate
artefact. That is what makes an allowlist narrower than "every printable rune" a
decision this slice may take rather than data loss — the information is not
destroyed, it is just not the thing that touches a path.

No ADR is warranted. The design is `sanitizeName`'s established shape with two
documented divergences (§ Design), and the reasoning fits in the function's own
doc comment.

## Design

One file, `internal/attachments/filename.go`, carrying one exported function,
one unexported constant, and one unexported helper.

```go
// SanitizeFilename turns a client-supplied filename into one safe path
// component. Total: every input has an answer, so there is no error return.
func SanitizeFilename(name string) string

// fallbackFilename is the component returned when nothing in the input
// survives the allowlist. Fixed, so the answer is the same every time.
const fallbackFilename = "attachment"

// truncateToBytes returns s cut to at most maxBytes bytes, never splitting a
// rune. May return "" if maxBytes is smaller than s's first rune.
func truncateToBytes(s string, maxBytes int) string
```

Spell it `Sanitize`, with a **z**. The prose in the ticket and in
`docs/protocol-mobile.md` uses British "sanitise", but the repo's Go identifiers
are American — `sanitizeName` is the precedent and there is no existing
exported `Sanitize*`/`Sanitise*` symbol anywhere in `internal/` or `cmd/` to
contradict it.

### The pipeline

Four steps, in this order. The order is load-bearing and § Testing strategy
pins it.

1. **Map, rune-wise.** Walk `name` with `for _, r := range name`. Write `r`
   when it is in the allowlist `a-z A-Z 0-9 _ . -` — byte-for-byte
   `sanitizeName`'s allowlist — and write `'_'` otherwise. Track a `bool`
   recording whether any rune was written unchanged.
2. **Fallback.** If no rune survived the allowlist, return `fallbackFilename`.
3. **Leading dot.** If the built result begins with `'.'`, prefix it with
   `'_'`.
4. **Bound.** `truncateToBytes(out, protocol.MaxAttachmentFilenameBytes)`.

Use `strings.Builder` for step 1, as `sanitizeName` does.

### Why each step is where it is

**Step 1 discharges the separator, control-character and encoding hazards at
once, because the allowlist is positive.** A `/` is not on it, so it becomes
`_`. Neither is NUL — worth naming on its own, because `report.txt\x00.exe` is
the classic filename attack and AC 2 only says "control characters". Neither is
`\`, DEL, U+202E, a shell metacharacter, or a space. Nothing has to be
enumerated and nothing can be forgotten, which is the whole argument for an
allowlist over a denylist here.

It also fixes the encoding. `for _, r := range name` yields `utf8.RuneError`
for each byte of invalid UTF-8, and `utf8.RuneError` is not on the allowlist, so
invalid input decays to `_`. Every byte written is therefore ASCII, so the
output is valid UTF-8 by construction rather than by a check.

**Step 2's condition is derived from the input, and that is deliberate — it is
the one place this design does not follow `sanitizeName`'s
check-the-built-result rule.** `sanitizeName` asks `b.Len() == 0`, which under a
replace-never-strip transform is true only for the empty input. AC 3 asks for
more: the empty string *or* "a name whose every character is replaced" must
answer the fixed component. Reading it the weaker way makes AC 3's second arm
dead — `"///"` would answer `"___"`, which is not "a fixed non-empty component,
the same one every time" — so the AC as written requires the stronger reading.

The output alone cannot support that reading. `"___"` is the answer both to
`"///"` (nothing survived) and to the literal client name `"___"` (everything
survived), and AC 5 requires the second to come back byte-identical, since
`"___"` is already one safe component within the bound. A
`strings.Trim(out, "_") == ""` check on the result would collapse the two and
break AC 5. The `bool` distinguishes them for one byte of state.

The flag is stated relative to the allowlist — "some rune passed" — not relative
to any particular allowlist, so widening the allowlist later does not drift its
meaning. That is the property `sanitizeName`'s comment is protecting, reached by
a different route.

One condition covers both of AC 3's arms: the empty input writes no runes, so
the flag is false for the same reason `"///"`'s is.

**Step 3 prefixes rather than appends, and that is the second divergence from
`sanitizeName`,** which answers `"."` with `"._"` and `".."` with `".._"`. A
suffix satisfies AC 1 but not AC 2 — `"._"` still begins with a dot, so the
stored file is still hidden. The prefix discharges **both criteria with one
rule**: after it, the result cannot begin with `'.'`, and a string that does not
begin with `'.'` is neither `"."` nor `".."`. Checked on the built result, per
`sanitizeName`'s rule, so it keeps holding if the allowlist changes.

`.` stays on the allowlist. Dropping it would satisfy AC 1 and AC 2 trivially
and break AC 5, which requires `report-2026.pdf` back byte-identical.

**Step 4 is last, and the earlier steps survive it.** Cutting a suffix cannot
introduce a `/` or a control character, cannot change the first byte, and
cannot empty the string: the loop only runs while the length exceeds the bound
and removes at most 4 bytes per turn, so it exits with at least 252 bytes in
hand. Steps 1–3's postconditions therefore all still hold on the returned value,
which is what AC 1–3 are stated on.

The order matters in the other direction too. A 255-byte name beginning with
`.` grows to 256 at step 3; truncating **before** the prefix would return 256
bytes and blow AC 4. § Testing strategy pins this with a dedicated row.

### `truncateToBytes`

Cut at a rune boundary, never mid-rune. The straightforward form drops one rune
at a time off the end:

```go
for len(s) > maxBytes {
    _, size := utf8.DecodeLastRuneInString(s)
    s = s[:len(s)-size]
}
return s
```

`DecodeLastRuneInString` returns `size == 1` for an invalid trailing byte, so
the loop always makes progress and always terminates. Each turn is O(1) and
drops at least one byte, so the whole call is O(len(s)) — **do not write a
condition that recomputes `utf8.RuneCountInString` each turn**, which would make
it quadratic on an input this function does not bound (see § Security review,
Network & I/O).

**Be honest about what this buys today.** Because step 1 leaves the string
ASCII, a plain `s[:maxBytes]` would give the same answer for every input that
can reach step 4, and no test driven through `SanitizeFilename` can tell the two
apart. The rune-safe form is here to remove the coupling between the allowlist
and the cut — widening the allowlist by one non-ASCII rune would otherwise
silently start writing invalid UTF-8 to disk — and it earns its keep by being
tested **directly**, on multi-byte input, rather than through the public
surface. That direct test is what makes AC 4's "no multi-byte rune is split"
clause non-vacuous.

### Contract to state in the doc comment

Not prose to copy — the properties the comment has to carry:

- What is returned: exactly one path component, never empty, never `.` or
  `..`, never containing `/`, never containing a control character, never
  beginning with `.`, valid UTF-8, and at most
  `protocol.MaxAttachmentFilenameBytes` **bytes** measured by `len` (say
  "bytes", not "characters" — the note in
  `docs/knowledge/features/attachments-package.md` about `ErrDigestMismatch`'s
  message is about exactly this slip).
- What is **not** returned: a unique name, and not an identifier. Distinct
  client names collide — `a/b` and `a_b` both answer `a_b`, every unusable name
  answers `fallbackFilename`, and a case-insensitive host folds `Report.pdf`
  into `report.pdf`. A caller that stores files by this component alone lets one
  upload overwrite another. #1773 keys storage by `attachment_id`.
- What it must **not** be called on: `AttachmentID`. Sanitising an id changes
  which attachment is addressed. The id's contract is a canonical-shape check
  that *rejects*, on `conversations.ValidID`'s precedent — see
  `AttachmentChunkPayload`'s SECURITY block.
- That this is not the enforcement point for the wire bound. An over-long
  filename is truncated here, never refused; if the daemon is to reject the
  frame, that check is #1767's admission pass.
- That the never-log rule still binds. Sanitising removes the log-injection
  shape, but § Attachments bans logging a filename for a *second, independent*
  reason — a filename is often private in itself — so the output is no more
  loggable than the input.

### Non-goals

- **No package doc comment and no `doc.go`.** `internal/attachments` already
  carries its package comment at the top of `accumulator.go` (#1769, merged
  2026-08-25 as `811f67f`). A second one is a duplicate, not a merge conflict.
  Leave that comment's opening sentence alone as well: it describes the
  accumulator rather than claiming exclusivity, and every invariant it asserts
  about the package — in-memory only, no filesystem, no wire code, zero log
  calls — this slice keeps. It is one clause short of complete after this
  ticket; that is the documentation phase's to fold in, not a code change here.
- No path construction, no per-conversation directory layout, no containment
  check, no write — #1773.
- No `conversation_id` or `attachment_id` validation — #1767 / #1773.
- No knowledge-base doc. The developer's deliverables end at the two files
  above.

## Concurrency model

None, and that is the design. `SanitizeFilename` and `truncateToBytes` hold no
state, take no lock, spawn no goroutine, and touch no package-level variable, so
they are safe to call from any goroutine concurrently. Say so in the doc
comment, because the neighbouring type in this package carries the opposite
constraint: `Accumulator` deliberately has no lock and is documented as fed
serially by one session's `appFrameWorker` goroutine. A reader who arrives via
`Accumulator` should not inherit that assumption here.

Mark the tests `t.Parallel()` at both levels, per `CODING-STYLE.md` and
`TestSanitizeName`.

## Error handling

There is none, and it is worth stating why rather than leaving it as an
absence. The function is **total**: every string has a safe component, so a
refusal would have no better answer to offer than the fallback already gives.
Returning `(string, error)` would push a branch into every call site that could
only ever log or ignore it — and logging it is precisely what the never-log rule
forbids.

The consequence for the rest of the family: because there is no error, there is
no error string, so there is no place in this file for a raw filename to leak
into #1744's line-oriented log. That is a structural property, not a discipline
one — unlike `Accumulator`, which had to keep the declared `sha256` out of
`ErrDigestMismatch`'s message by hand.

An input that violates the published 255-byte bound is truncated, not refused
(§ Design, step 4).

## Testing strategy

One new file, `internal/attachments/filename_test.go`. Three tests. The main one
is table-driven in the house shape — a `[]struct{ in, want string }` looped with
`t.Run`, exact expected strings rather than property assertions, following
`TestSanitizeName`. Rows whose input is long are built with `strings.Repeat`
inside the composite literal; the shape does not need a second table.

**Assert the universal postconditions inside the loop, on every row, in
addition to the row's own `want`.** All five criteria are stated on the
*returned value*, so they hold for every input rather than only for the row
aimed at them, and asserting them once in the loop body is both cheaper and
stronger than scattering them across rows. For each `got`, check: non-empty;
`len(got) <= protocol.MaxAttachmentFilenameBytes`; `utf8.ValidString(got)`; no
`/`; no rune below `0x20` and no `0x7f`; does not begin with `.`; is neither
`.` nor `..`. This is what makes each of the ~26 rows below a fixture for all
five criteria at once — and it is why the AC 4 rows may assert `len`, and must
not assert `utf8.RuneCountInString`.

### `TestSanitizeFilename` — exact input/output rows

AC 1 — separators and parent references:

- `"../etc/passwd"` → `"_.._etc_passwd"` (both separators replaced; the result
  began with `.`, so step 3 prefixed).
- `"a/../b"` → `"a_.._b"` (no prefix — the result does not begin with `.`).
- `".."` → `"_.."`, and `"."` → `"_."`. These two also pin the divergence from
  `sanitizeName`, which answers `".._"` and `"._"`.
- `"a\\b"` (a backslash) → `"a_b"`.

AC 2 — control characters and hidden files:

- `"a\x00b"` → `"a_b"`. Call the NUL row out by name; it is the classic
  filename attack, not merely one control character among several.
- `"a\nb"` → `"a_b"`, `"a\rb"` → `"a_b"`, `"a\r\nb"` → `"a__b"` (two runes in,
  two underscores out — pins that the map is per-rune and does not collapse
  runs).
- `".bashrc"` → `"_.bashrc"`, `".ssh"` → `"_.ssh"`.
- `"a.b.c"` → `"a.b.c"` — a non-leading dot is untouched, so the prefix rule is
  positional rather than a blanket dot rule.

AC 3 — the fixed fallback. Every row here answers `fallbackFilename`:

- `""` — the empty input.
- `"///"` — **the row that makes AC 3's second arm live.** A weaker reading of
  AC 3 answers `"___"` here; this row is the sole red for it.
- `"報告書"` — a legitimate non-ASCII name where nothing survives the allowlist.
  Include it beside `"///"`: it is the case that shows the stronger reading is
  the *useful* one and not just the literal one.
- `"\x00\n"` — control characters only.

AC 4 — the byte bound. Assert `len(got)`, never
`utf8.RuneCountInString(got)`; a rune-count assertion passes on a multi-byte
fixture while the byte bound is blown, which is the failure this criterion
exists to catch. Also assert `utf8.ValidString(got)` on each.

- 300 `'a'` → exactly 255 `'a'`.
- `strings.Repeat("aé", 200)` — 400 runes, 600 input bytes, mapping to 400
  output bytes and cut to exactly 255. Pins that the bound is measured on the
  **output** in bytes and that a multi-byte input does not confuse the count.
- Exactly `protocol.MaxAttachmentFilenameBytes` `'a'` → returned unchanged.
  This is the off-by-one pin: it reddens `>` written as `>=` in the truncation
  condition.
- **The ordering row.** A 255-byte name beginning with `'.'` — e.g. `"."`
  followed by 254 `'a'`. Expect exactly 255 bytes, first byte `'_'`, last byte
  dropped relative to the input. An implementation that truncates before
  prefixing returns 256 bytes and this row is its sole red.

AC 5 — identity for an already-safe name. Assert byte equality with the input:

- `"report-2026.pdf"` — the ticket's own example.
- `"a_b-c.tar.gz"`, `"README"`, `"2026"`.
- `"___"` and `"_"` — **the rows that pin step 2's `bool` against the
  `strings.Trim` shortcut.** Under a result-derived emptiness check these
  answer `fallbackFilename`; under the design here they come back unchanged.
  Per the package's first mutation-testing lesson, a row aimed at "the
  distinction comes from the input, not the output" has to use the value whose
  *output* is indistinguishable from the property's absence, and `"___"` is
  exactly that value.

### `TestSanitizeFilename_FallbackIsAFixpoint`

One assertion: `SanitizeFilename(fallbackFilename) == fallbackFilename`. It
pins the constant's own safety without machinery — any future edit that gives
the fallback a leading dot, a separator, or an over-long value turns it red.
Cheaper than a second copy of the postcondition list, and it is the reason step
2 may return the constant directly rather than routing it through steps 3–4.

### `TestTruncateToBytes`

Direct rows on the helper, with genuinely multi-byte input. This is the test
that makes AC 4's "no multi-byte rune is split" clause non-vacuous — see
§ Design, `truncateToBytes`.

- `("abc", 10)` → `"abc"` and `("abc", 3)` → `"abc"` — no-ops at and under the
  bound.
- `("abcd", 3)` → `"abc"`.
- `("héllo", 2)` → `"h"` — cutting at 2 lands inside `é`, so the rune goes.
- `("héllo", 3)` → `"hé"` — cutting at 3 lands exactly on the boundary, so it
  stays. This pair is the sole red for a plain `s[:maxBytes]`.
- `("日本語", 4)` → `"日"` — two boundaries to back over, not one.
- `("é", 1)` → `""` — the documented degenerate case. `SanitizeFilename` never
  reaches it, since it passes 255.
- Assert `utf8.ValidString` on every returned value.

### Verification

`make check` is the gate: `go vet`, race-enabled unit tests, staticcheck. No e2e
tier is involved — nothing here reaches a socket, a subprocess, or the
filesystem, and no `internal/e2e` test changes.

The claims above about which row is the sole red for which mutant are stated as
design intent, not as measurement. If the developer or code review wants them
measured, the package's established method is `go test -overlay` with an
absolute-path JSON manifest and no worktree write — and per the third
mutation-testing lesson in
`docs/knowledge/features/attachments-package.md`, grep the run's output for
`build failed` before trusting any verdict, because a mutant that deletes the
only use of an import fails the build and scores as green from the exit code
alone.

## Open questions

- **The fallback's value.** `"attachment"` is descriptive, extensionless, and
  honest about carrying no information from the client. `"_"` would match
  `sanitizeName`'s precedent more closely but reads as noise on disk. Either
  satisfies every criterion; take `"attachment"` unless #1773's directory
  layout gives a reason to prefer otherwise.
- **Extension loss on truncation.** A 300-byte `…….pdf` loses its extension at
  step 4. Declined rather than open: `mime_type` rides the same frame, the raw
  name is echoed verbatim on retrieval, and extension-preserving truncation is
  machinery for a case no observed client produces. Noted here so #1773 does
  not rediscover it as a bug.
- **A named type for the sanitised value** — see § Security review, trust
  boundaries. Declined for this slice; #1773 is where the question becomes real.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** SHOULD FIX, discharged in the doc comment.
  `SanitizeFilename` is the boundary — a single exported function, the whole
  crossing from client-chosen string to host-side path component, with nothing
  scattered elsewhere. The weakness is that it returns a plain `string`,
  indistinguishable from the raw `AttachmentChunkPayload.Filename` it was
  derived from, so nothing structural stops #1773 from building a path out of
  the raw field. A named `type Component string` would make the boundary
  type-visible; declined here because the ticket fences path construction to
  #1773, `CODING-STYLE.md` says not to define abstractions preemptively, and
  `sanitizeName` returns a plain string. The mitigation that costs nothing is
  the doc comment's imperative plus a hand-off: **#1773's spec should require
  this call to be the only place the storage path reads `Filename`.** Note also
  that "sanitised" is host-side only — it does not make the value safe to
  *render*, since § Attachments puts that obligation on the client and the raw
  name is echoed back verbatim regardless.
- **[Tokens, secrets, credentials]** Not applicable — no token, no key, no
  randomness. One adjacent property does apply: § Attachments treats a filename
  as private in itself, which is why the never-log rule survives sanitisation
  (see Errors/logs below). Disk permissions for the eventual stored file are
  #1773's.
- **[File operations]** No findings in this slice — it opens nothing, stats
  nothing, and constructs no path. Two hand-offs to #1773, both real:
  - **Traversal is discharged, and discharged on the built result.** No `/`
    (not on the allowlist), and neither `.` nor `..` (step 3's prefix). The
    component is also never empty, so `filepath.Join(dir, component)` can never
    collapse to `dir` itself. NUL is worth naming separately: `foo.txt\x00.exe`
    is the classic filename attack and it dies at step 1 with everything else
    outside the allowlist.
  - **The component is not unique, and that is an overwrite hazard one layer
    up.** `a/b` and `a_b` both answer `a_b`; every unusable name answers
    `"attachment"`, which makes the fallback maximally collision-prone; and
    APFS is case-insensitive by default, so `Report.pdf` and `report.pdf` are
    one file on macOS. If #1773 names a stored file by this component alone, a
    second upload silently overwrites a first within the same conversation.
    #1773 must key storage by the (canonical-shape-checked) `attachment_id` and
    treat the sanitised filename as display material at most. Stated in the doc
    comment so the obligation travels with the function.
  - `PATH_MAX` (1024 on macOS, 4096 on Linux) bounds the whole path, where
    `NAME_MAX` bounds this component. A 255-byte component under a deep
    conversation directory is #1773's arithmetic, not this function's.
- **[Subprocess / external command execution]** OUT OF SCOPE, named rather than
  dismissed. `-` is on the allowlist, so `-rf` survives as `-rf`, and a
  component passed as an argv element would parse as a flag. No current or
  planned path does that — the component is only ever joined behind a directory
  in #1773, where it is never argv-leading — and adding a leading-`-` rule now
  would be a defence for an unobserved failure mode that also mangles the
  legitimate name `-report.pdf`. If #1746 or a later slice ever hands an
  attachment path to `exec.Command`, that slice owns the check. Nothing here
  reaches `sh -c` or touches the environment.
- **[Cryptographic primitives]** Not applicable — no RNG, no hash, no
  comparison against a secret. One deliberate decision worth recording: the
  fallback is a fixed constant and is **not** randomised or hashed. AC 3
  requires the same answer every time, and a random fallback would trade a
  documented collision for a non-deterministic filename that no test could pin.
- **[Network & I/O]** One finding, no fix needed here.
  `MaxAttachmentFilenameBytes` is a producer-side contract with **no
  validator** — the `const` block's own comment says so — so nothing bounds
  this function's input. A hostile client can send a filename as large as the
  ~65 KB application envelope allows, and it rides *every* chunk of a transfer.
  The cost is bounded and linear: one `strings.Builder` of at most `len(name)`
  bytes, then a truncation loop that drops at least one byte per O(1) turn.
  Total work is O(bytes received), with no amplification, so it is not a DoS
  vector — provided the truncation loop is written as § Design specifies and
  not with a per-turn `utf8.RuneCountInString`, which would make a 65 KB
  filename quadratic. That is the one implementation detail in this spec with a
  security reason behind it. Rejecting an over-long filename outright, if the
  daemon ever wants to, belongs to #1767's admission pass.
- **[Errors, logs, telemetry]** MUST-NOT-LOG, and the reason is easy to get
  wrong. Sanitising removes the *log-injection* half of the hazard — the output
  carries no newline and no control character, so it cannot forge a log line.
  It does **not** remove the *privacy* half: § Attachments bans logging a
  filename for two independent reasons, and "a filename is often private in
  itself" is untouched by any transform. So the sanitised component is no more
  loggable than the raw one, and #1744 must not read "sanitised" as "safe to
  log". Structurally, this file has no error strings at all (§ Error handling),
  so there is no second surface for the name to leak through — unlike
  `Accumulator`, which had to keep an attacker-chosen digest out of
  `ErrDigestMismatch`'s message by hand.
- **[Concurrency]** No findings, and no lock needed: both functions are pure,
  hold no state, and touch no package-level variable. The finding worth
  recording is the *documentation* risk — `Accumulator`, in the same package, is
  explicitly fed serially by one goroutine and carries no lock for that reason.
  A reader arriving from it could assume the same constraint applies here.
  § Concurrency model requires the doc comment to say it does not.
- **[Threat model alignment]** `docs/protocol-mobile.md` § Attachments, "Trust
  and content hygiene", assigns three obligations. This slice discharges the
  `filename` half of "the daemon validates each field before use", and keeps
  "never log a raw filename" true by making zero log calls. The third — "a
  client MUST sanitise `filename` before rendering" — is **not** dischargeable
  here and is not a gap: the raw name is echoed back verbatim by design, so the
  render-side obligation stays with the client exactly as published.
  `AttachmentChunkPayload`'s SECURITY block additionally warns that
  `AttachmentID` needs a canonical-shape check rather than this treatment; the
  doc comment states that this function must not be called on the id, because
  sanitising an id changes which attachment is addressed and a mangled id would
  pass silently where a check would reject.

**Reviewer:** architect (self-review per the security-review checklist)
**Date:** 2026-08-25
