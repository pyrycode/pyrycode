# Spec: Make M4's failure self-diagnosing in the stream `new_session` e2e (#1273)

**Scope:** `internal/e2e/relay_v2_stream_new_session_test.go` only. Test-side diagnostics.
No production code. No new files. No new exported symbols. No new helpers.

---

## Files to read first

| Path | What to extract |
|---|---|
| `internal/e2e/relay_v2_stream_new_session_test.go:22-71` | The header comment: the M1–M5 milestone contract and *why* M4 asserts the fresh child's stdin rather than a phone-side delta (the frozen-sink-tag divergence). Do not weaken any of it. |
| `internal/e2e/relay_v2_stream_new_session_test.go:239-282` | The M2 actuation loop. `post` is captured from `readBootstrapIfPresent(regPath)` — **the same file and field** the AC-3 re-read must use. Note the comment already anticipating "the rare double-actuation case". |
| `internal/e2e/relay_v2_stream_new_session_test.go:333-376` | M4: the ack #2 loop, the stdin-log poll loop, and the failure message being rebuilt. The whole diff lands here plus five `t.Logf` prefixes. |
| `internal/e2e/rotation_test.go:143-175` | `readBootstrapIfPresent(regPath) (registryEntry, bool)` — **non-fataling**, one-shot, no polling. This is the AC-3 re-read. Read its doc comment: `ok=false` conflates *missing*, *unparseable*, and *no bootstrap row*. |
| `internal/e2e/rotation_test.go:111-141` | `waitForBootstrapID` / `waitForBootstrapIDChange` / `readBootstrap` — all three **fatal** on timeout. None of them may be used on the failure path. Shown so you recognise them and skip them. |
| `internal/e2e/per_conversation_eviction_test.go:375-395` | `boundSessionID(t, convPath, convID)` — reads `conv.CurrentSessionID`, looks like exactly what AC-3 asks for, and **must not be used**. See § "The helper that must not be used". |
| `internal/e2e/restart_test.go:17-24` | `registryEntry` — the `.ID` field the comparison reads. |
| `internal/e2e/realclaude/prompt_fidelity_test.go:75-85` | The package's existing `"<unresolved home: …>"` sentinel idiom for a diagnostic helper that cannot resolve its value. Mirror this shape rather than emitting `""`. |
| `docs/knowledge/codebase/1137.md` | Milestone structure and the post-rotation drain divergence. Background for why M4's observable is the stdin log at all. |

---

## Context

`TestRelayV2_StreamNewSessionRotatesAndRestartsFresh` failed once during QA's `make check`
on PR #1272. M1–M3 passed; M4's stdin-log poll burned its full 20 s `turnTwoDeadline` and
the test went red. Non-regression was established (additive PR in a different package and
build tag, clean merge-base, 4/4 re-runs green).

The ticket corrects the filed report's central number: the 20.21 s runtime is **not** a
load measurement. M4's poll exits only on finding the needle or on deadline expiry, so a
failure always consumes exactly 20.00 s. Everything from test start through ack #2 took
**0.21 s** — at the *fast* end of the 0.13–1.66 s passing range. The machine was not slow.

This ticket does not fix the flake and does not suppress it. It makes the next occurrence
decidable. The current M4 message names two readings and discriminates neither; at least
four are live:

1. **Genuine slowness** — the machine really was starved for 20 s.
2. **Delivery stalled** — the msgqueue drain's idle wake-up was missed and turn #2 never
   left the queue. This wait *blocks*; there is no periodic retry (#704), so a larger
   deadline catches nothing.
3. **A second rotation stole the turn** — the M2 loop re-sends `new_session` every ~250 ms
   and the file's own comments already anticipate double-actuation. A rotation racing turn
   #2's delivery re-keys the pool under the in-flight write.
4. **The fresh child never spawned**, or spawned without `PYRY_FAKE_CLAUDE_STDIN_LOG`, so
   nothing it received was ever logged.

Reading 3 is the one the current record hides most completely, and it is the one the file
itself predicts.

---

## Design

Four changes, all inside `TestRelayV2_StreamNewSessionRotatesAndRestartsFresh`.

### D1 — Elapsed stamp on every milestone line (AC-1)

Capture the start as the **first statement of the test body**, before `shortHome(t)`, so
the stamp covers pairing and daemon startup — that prefix is precisely the quantity the
ticket's 20.21 − 20.00 = 0.21 s argument turns on.

- `testStart := time.Now()` at the top of the body.
- An `elapsed func() string` closure returning `time.Since(testStart).Round(time.Millisecond).String()`.
- Prefix each of the five existing `t.Logf` milestone lines (currently at `:232`, `:282`,
  `:320`, `:376`, `:394`) with `[t=%s]`, e.g.
  `t.Logf("[t=%s] M1: observed assistant_delta …", elapsed())`.

Keep every existing message body byte-identical; only the prefix is new.

**Why this reaches a CI failure record.** `go test` buffers `t.Logf` output and prints it
when the test *fails*, not only under `-v`. So a future M4 failure automatically carries
M1–M3's stamps, and the reader sees where the 20 s went without re-running anything.
Non-decreasing is guaranteed by the monotonic clock — no ordering logic is needed.

### D2 — The stdin log's size at two moments (AC-2)

Two probes, **using the same instrument at both moments** — `os.ReadFile` + `len`, matching
what the poll loop already does and what `log=%q` already prints. Do not mix `os.Stat` size
into one end and `len(ReadFile)` into the other: the file is being appended to concurrently,
and two different instruments make the delta unattributable.

- Immediately after the ack #2 loop exits, before the poll loop: read the log once and
  record `sizeAtAck2` plus its read error. Also record `ackTwoAt := elapsed()`.
- The poll loop already assigns `logBytes, _ = os.ReadFile(stdinLog)` each iteration.
  **Stop discarding the error** — keep the last iteration's error in a variable so the
  failure message can distinguish *"the file read failed"* from *"the file was empty"*.
  This is the whole point of the ticket: a 0 that silently means "unreadable" would
  reintroduce exactly the ambiguity being closed.
- The failure message reports both sizes, both read errors, and the ack #2 elapsed stamp.

**What the pair decides.** Growth between the two probes falsifies "no bytes arrived at
all" — the log is `O_APPEND` with per-write `Sync`, and both the bootstrap and the fresh
post-rotation child accumulate into one file, so growth means *some* child was alive and
receiving. A non-zero `sizeAtAck2` also weakens reading 4's "spawned without the env" arm:
the tee reached at least one child, and both children inherit the same daemon process env.

**Why the ack #2 stamp is load-bearing, not decoration.** There is no milestone line
between M3 and M4's failure, so the record currently jumps from M3's completion straight to
a 20 s expiry — the up-to-15 s ack #2 wait is invisible inside that gap. Whether ack #2
landed at t=0.21 s or t=14 s *is* the slowness-vs-stall discriminator, and it is the exact
arithmetic the ticket body had to reconstruct by hand from a single total.

### D3 — Registry re-read at failure time (AC-3)

One-shot `readBootstrapIfPresent(regPath)` in the M4 failure branch, reported alongside
`post.ID`.

- `regNow.ID != post.ID` ⟹ **reading 3**: a second rotation moved the binding out from
  under turn #2. Visible as a mismatch instead of indistinguishable from a stall.
- `regNow.ID == post.ID` ⟹ the binding held; reading 3 is excluded and the remaining
  readings are in play.
- `ok == false` ⟹ report a sentinel, e.g. `<no bootstrap entry: registry missing,
  unparseable, or bootstrap row absent>`, mirroring `prompt_fidelity_test.go:81`. It must
  **not** render as `""`, which would read as "the rotation lost the id" — a fabricated
  third finding from a broken instrument.

**Why the sessions registry and not `conversations.json`.** `post.ID` was captured at M2
from `readBootstrapIfPresent(regPath)`. Comparing like against like means a mismatch has
exactly one reading: a later rotation. Comparing `post.ID` against
`conv.CurrentSessionID` from a different file would confound "second rotation" with
"cross-file skew" and hand the next triager a two-reading signal — the defect this ticket
exists to remove. `RotateForNewSession` re-keys, rebinds, and persists in one path, so the
registry bootstrap id tracks the binding; that equivalence is what M2 already relies on.

**The message must name what it read.** Say "registry bootstrap id", not "the conversation's
bound session id". The two are equal in practice via the path above, but the message may
only claim what the instrument actually measured.

**One-shot, not polling** — see AC-4 below.

### D4 — Diagnostic-only (AC-4)

No `time.Second` multiplier in the file changes. No re-run, retry, or repeat wrapper. The
test still fails on the first occurrence.

This is why D3 uses the non-polling `readBootstrapIfPresent` rather than
`waitForBootstrapID`/`waitForBootstrapIDChange`: a polling re-read would introduce a new
duration constant into a file whose ACs forbid exactly that, and would extend the failure
path by its own timeout.

The rationale is in the ticket and is worth carrying in a comment: raising the deadline
cannot fix an unbounded wait (the drain blocks on idle with no periodic retry, #704), and
auto-re-running before reporting red would convert a possible permanent-stall defect into
an invisible one — the false-green shape that already shipped an unverified change once
(#1168 / PR #1169, where a SKIP exited 0 and read as a pass).

---

## The helper that must not be used

`boundSessionID(t, convPath, convID)` (`per_conversation_eviction_test.go:375`) reads
`conv.CurrentSessionID` and is the first thing a search for "the conversation's currently-
bound session id" will surface. **It is wrong here on two independent counts:**

1. **It fatals.** Called while building the M4 `t.Fatalf` arguments, its own
   `t.Fatalf("conversation %s never had a non-empty current_session_id within 2s …")` fires
   first and the M4 diagnostic message — the entire deliverable of this ticket — is never
   printed. A failure-path diagnostic may never itself be able to fail the test.
2. **It polls for 2 s**, adding a deadline to a file whose AC-4 forbids new duration
   constants.

Same rule generalises: nothing on the M4 failure path may call a `t.Fatal*`-ing helper.
That rules out `readBootstrap`, `waitForBootstrapID`, `waitForBootstrapIDChange`, and
`mustReadFile` for the new code. (`mustReadFile` remains fine where it already is — the M2
branch — because that message is itself the terminal one.)

---

## Error handling

Every new read is a diagnostic on a path that is *already failing*. The governing rule:

> **A diagnostic must be able to report its own failure to measure, and must never render
> that failure as a measurement.**

| Probe | Failure mode | Required rendering |
|---|---|---|
| `os.ReadFile(stdinLog)` at ack #2 | read error | report the error; do not print `0` alone |
| `os.ReadFile(stdinLog)` in the poll loop | read error on the last iteration | report the error; without it, `len(nil) == 0` reads as "the file was empty" |
| `readBootstrapIfPresent(regPath)` | `ok == false` | sentinel string, never `""` |

No new error types, no wrapping, no `fmt` import required — build the sentinel with a plain
`if`/`else` before the `Fatalf` and let `%v` render a nil error as `<nil>`.

---

## Testing strategy

The deliverable *is* test code; verification is running it and reading the output.

**AC-1 — on a passing run:**

```
go test -tags e2e -race -run TestRelayV2_StreamNewSessionRotatesAndRestartsFresh -v ./internal/e2e/
```

Expect five `M1:`–`M5:` lines, each carrying a `[t=…]` stamp, values non-decreasing.

**AC-2 / AC-3 — the failure path.** The failure cannot be triggered on demand (that is the
whole premise of this ticket: one occurrence, no repro). Verify by construction, not by
firing it:

- Read the assembled failure message and confirm it names, in order: the missing needle,
  ack #2 received, the size at ack #2 with its elapsed stamp, the size at expiry with its
  read error, `post.ID`, the re-read registry id, and the existing `log=%q`.
- Confirm by inspection that no branch of the new code can `t.Fatal` before that message
  is printed.

**AC-4 — mechanical:**

```
git diff -- internal/e2e/relay_v2_stream_new_session_test.go | grep -E '^[+-].*time\.(Second|Millisecond|Minute)'
```

Expect **no** line that changes a deadline constant. New `time.Since` / `time.Now` calls
are fine and are not multipliers; if the grep surfaces them, confirm each is a stamp, not a
deadline. Also confirm no `for`-retry or repeat wrapper was added around the test body.

**Regression guard:** M1, M2, M3 and M5's assertions are untouched. Only `t.Logf` prefixes
and the M4 failure branch change.

---

## What this record will and will not decide

Stated plainly so the next triager does not over-read it:

- **Decides reading 3.** Registry id ≠ `post.ID` ⟹ a second rotation. Previously invisible.
- **Decides "no bytes at all" vs "bytes, but not turn #2's".** The size pair.
- **Bounds reading 1.** The elapsed stamps show whether the pre-M4 prefix was fast (as in
  the observed failure, 0.21 s) or genuinely starved.
- **Does not separate reading 2 from reading 4** on its own. A frozen log is consistent
  with both a missed drain wake-up and a child that never spawned. A non-zero size at ack
  #2 weakens reading 4's env arm (§ D2) but does not eliminate the "never spawned" arm.

Closing that last gap needs a daemon-log or process-liveness observable, which is a
different ticket. Do not add it here.

---

## Open questions

1. **Should the elapsed stamps also go on the M1/M2/M3 fatal messages?** Not required by
   any AC, and each of those fatals is already terminal and self-describing. Left out
   deliberately — the milestone `t.Logf` lines print on failure anyway, which is what makes
   AC-1 work in the field. If a future M1 or M3 flake appears, revisit then; do not
   pre-build for it now.
2. **Separating reading 2 from reading 4** — deferred as above; a follow-up if the flake
   recurs.
3. **A repro under sustained load** — explicitly out of scope per the ticket. This ticket's
   output is its precondition. File it as a follow-up only if the flake recurs.

---

## Out of scope

- `internal/e2e/realclaude/interactive_stream_new_session_test.go` (#1174) — the real-claude
  sibling covering the same behaviour live. **Do not edit it.**
- Any production code. The msgqueue drain's blocking wake-up (#704) and the frozen sink tag
  (#1133) are both known, tracked, and untouched here.
- `docs/knowledge/codebase/1273.md` — owned by the documentation phase, written after merge.

---

## Sizing note

The change is XS-shaped: one test file, ~30 net lines, ~8 edits, no new helpers or exported
symbols, zero production source files touched. The `size:s` label is **retained
deliberately** rather than downgraded — the verification target is the flaky test itself, so
the developer may need more than one e2e run to land a clean pass for AC-1. The budget is
for the verification loop, not the diff.
