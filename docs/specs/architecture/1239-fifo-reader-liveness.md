# #1239 — FIFO reader-presence liveness read, with its offline self-check

**Size:** S (confirmed — see § Size check).
**Security review:** not applicable — `security-sensitive` is not on the ticket, deliberately (PO's reasoning: no wire payload, no untrusted input, no credential path, nothing published).
**Production changes:** none. One new test file, `internal/e2e/realclaude/fifo_reader_liveness_test.go`.

---

## Files to read first

| Path | What to extract |
|---|---|
| `internal/e2e/realclaude/background_trigger_probe_test.go:663-715` | `holdProbeFIFO` — full body. It **creates** the FIFO (`syscall.Mkfifo(path, 0o600)`), holds the write end in a goroutine, and returns a rendezvous channel closed the instant a reader opens. Release is its own registered `t.Cleanup`; the write end never leaves the helper. Note the cleanup's `default:` arm (no reader ever arrived) — it opens the read end non-blockingly to unpark the goroutine. |
| `internal/e2e/realclaude/background_trigger_probe_test.go:1113-1173` | `TestProbeFIFOHold_HoldsReaderUntilCleanupRelease` — the offline shape to mirror for AC4: reader-exit assertion registered **before** `holdProbeFIFO` so `t.Cleanup`'s LIFO puts it after the release; real `cat`; rendezvous wait; still-blocked assertion. Also read its doc comment — it names why a kill-the-reader safety net after `holdProbeFIFO` would make the check vacuous. |
| `internal/e2e/realclaude/background_trigger_probe_test.go:104-148` | The three const blocks. Per-file distinct consts is the package convention (`probeFIFOName`, `probeHeldCommandName`, `probeFIFOReleaseDeadline`, `probeEnableEnv`). Your file gets its own, none reused except `probeFIFOReleaseDeadline` is **not** yours to reuse — declare your own deadline const. |
| `internal/e2e/realclaude/background_trigger_probe_test.go:266-290` | The `probeFiredBackground` / `probeDidNotFire` / `probeCommandNeverRan` const block and `probeProc` — the package's outcome idiom: bare untyped `string` consts in kebab-case, plus a struct with JSON tags for evidence. Match it. |
| `internal/e2e/realclaude/background_trigger_probe_test.go:1-60` | The file-header comment convention for this package: mechanism diagram, what is being probed, what is not load-bearing. Your file needs one in the same register. |
| `internal/e2e/realclaude/fixtures.go:96-130` | `WithWorktreeAuthenticated` — `t.Skipf`s without `ANTHROPIC_API_KEY` / `CLAUDE_CODE_OAUTH_TOKEN`. **Do not call it.** Read it only to confirm it is the skip source AC5 forbids. |
| `internal/e2e/realclaude/fixtures_test.go:348-355` | `TestMain` — branches only on `GO_TEST_HELPER_PROCESS`, then `m.Run()`. It does **not** gate credentials, which is what makes AC5's zero-SKIP run achievable. |
| `CODING-STYLE.md` § Error Handling, § Testing | `errors.Is` / `errors.As` for matching (never string compare); table-driven tests; `t.Helper()` on shared assertions; stdlib `testing` only. |

Everything else in the package is out of scope for this ticket.

---

## Context

Several probes in `internal/e2e/realclaude` stage a live claude turn around a command the test holds open through a FIFO. Holding the write end proves the command could not have **finished**; it does not prove the command is **alive**. A killed command leaves the same held write end and records identically.

The family has shipped an instrument whose failure mode is indistinguishable from the result it claims twice already (#1230's MUST FIX; re-derived on #1235). The FIFO read has such a mode **on the success arm**, where errno discrimination cannot reach it: on a **regular file** or a **character device**, `open(path, O_WRONLY|O_NONBLOCK)` succeeds with no reader anywhere, and a bare `open` reports that as "the command is alive."

It has a **dual** form that is the one that actually breaks the consumer: an instrument that can only ever answer "alive". Two differently-constructed FIFOs cannot prove the read flips — only one FIFO across one reader's lifetime can.

This is the only piece of the #1227 family provable with no live claude: a deterministic RED/GREEN gate rather than a probe. That is why it lands first and alone. #1240 consumes it at the instant it records `turn_state{idle}` and is natively blocked on this ticket.

---

## The measurement

Run during this design pass on macOS 25.5.0 / uid 501, Go 1.26.2. Reproduced with the exact gate this spec prescribes (`Lstat` → `Mode().Type() != os.ModeNamedPipe` → `open(O_WRONLY|O_NONBLOCK)`), write end held throughout by a `holdProbeFIFO`-shaped goroutine:

```
fresh fifo, no reader ever:                  NO-READER (ENXIO)
write end held + cat blocked on read:        READER-PRESENT / READER-PRESENT / READER-PRESENT
reader still blocked after 3 reads:          yes (ProcessState nil)
after Kill(), before Wait() [sample 0]:      READER-PRESENT
after Kill(), before Wait() [sample 1]:      READER-PRESENT
after Kill(), before Wait() [sample 2]:      READER-PRESENT
after Kill(), before Wait() [sample 3]:      READER-PRESENT
after Kill(), before Wait() [sample 4]:      READER-PRESENT
after Kill()+Wait() (reaped), write end held: NO-READER (ENXIO)
regular file:                                INSTRUMENT-FAIL not-a-fifo mode=-rw-------
char device /dev/null:                       INSTRUMENT-FAIL not-a-fifo mode=Dcrw-rw-rw-
directory:                                   INSTRUMENT-FAIL not-a-fifo mode=drwx------
missing path:                                INSTRUMENT-FAIL lstat: ... no such file or directory
bare open(/dev/null, O_WRONLY|O_NONBLOCK):   SUCCEEDS
symlink -> fifo (Lstat, not Stat):           INSTRUMENT-FAIL not-a-fifo mode=Lrwxr-xr-x
```

Separately, on a `mkfifo(path, 0o000)` FIFO as uid 501:

```
mode=p--------- type-is-fifo=true
open err=... permission denied  errors.As=true errno=13  is-EACCES=true  is-ENXIO=false
syscall.ENXIO.Error() == "device not configured"     (Darwin; Linux says "no such device or address")
```

**Three design consequences, each load-bearing:**

1. **`Kill()` alone does not flip the read — `Wait()` is the synchronisation point.** Five consecutive samples taken after `Kill()` and before `Wait()` all read READER-PRESENT. A flip test that kills and reads without reaping does not fail intermittently; it fails **every time**, and looks like the read is broken. This is the single most expensive thing to rediscover in the developer loop.
2. **The mode gate must be positive and must run before the open.** `/dev/null` is the case that discriminates a `ModeNamedPipe` allowlist from a regular-file blacklist — a blacklist admits it, the bare open succeeds, and the verdict is a false "reader present". `Lstat` also rejects a symlink-to-FIFO (`Lrwxr-xr-x`); that is the correct conservative reading, see § Error handling.
3. **The errno *name* must come from a lookup table, not from `err.Error()`.** `syscall.Errno.Error()` returns the platform's message text, and ENXIO's differs between Darwin and Linux. Published evidence needs a stable name.

---

## Design

One new file. No production change, no `go.mod` change, no edit to any existing file.

```
internal/e2e/realclaude/fifo_reader_liveness_test.go     //go:build e2e_realclaude
```

### Symbol prefix

Every new symbol takes the `fifoLive` prefix; every new test takes the `TestFIFOReaderLiveness_` prefix so AC5's single `-run '^TestFIFOReaderLiveness'` covers them all. Both prefixes were verified free on `main` and on every in-flight sibling branch that touches this package (`feature/1174`, `feature/1219`, `feature/1230`) — a branch-overlap check does not catch same-package identifier collisions, so this was checked by name.

### Verdicts

Three untyped `string` consts in the package's kebab-case outcome idiom (mirror `probeFiredBackground` et al. at `:266-290`):

```go
fifoLiveReaderPresent    = "reader-present"
fifoLiveNoReader         = "no-reader"
fifoLiveInstrumentFailed = "instrument-failed"
```

Never a bare boolean, at any layer.

### The outcome type

```go
// fifoLiveOutcome is one liveness read. Recorded verbatim by consumers
// (#1240), so every field is JSON-tagged and self-describing.
type fifoLiveOutcome struct {
    Verdict   string // one of the three consts above
    Detail    string // why this arm fired, in prose
    Path      string
    Mode      string // os.FileMode.String() as observed at Lstat; "" if Lstat failed
    Errno     int    // 0 when no errno was involved
    ErrnoName string // "ENXIO", "EACCES", …; "" when Errno == 0
}
```

`Detail` is what a consumer publishes when it must say "which arm fired, and why". Every arm sets it; none is left empty. `Mode` carries the gate's own input so a reader of the evidence can see what was admitted or rejected, not just the verdict.

### The read

```go
// fifoLiveRead answers "is some process currently holding this FIFO's read
// end?" with no pid and no ps. Never fails the test — it returns the outcome,
// including instrument failure, so a consumer can record it mid-turn.
func fifoLiveRead(path string) fifoLiveOutcome
```

Takes no `*testing.T` and never calls `t.Fatal`. #1240 calls it at the instant it records `turn_state{idle}`; an instrument failure there is a datum to publish, not a reason to abort the turn.

Sequence, in this order (the order is the contract, not an implementation detail):

1. `os.Lstat(path)`. Error → `instrument-failed`, errno recorded, `Mode` empty. This is where `ENOENT` surfaces.
2. `info.Mode().Type() != os.ModeNamedPipe` → `instrument-failed`, `Mode` recorded. A positive equality against the single admitted type — not a blacklist, not a bitmask test that some other type could slip through.
3. `os.OpenFile(path, os.O_WRONLY|syscall.O_NONBLOCK, 0)`.
   - `err == nil` → close the fd immediately, then `reader-present`. **The close is load-bearing**: it is the transient second write end retiring, and it is what makes repeated reads non-perturbing (§ Concurrency).
   - `err != nil` → hand to the classifier below.

`Mode` is recorded from step 1's `Lstat`, so the outcome always states what the gate actually saw.

### The open classifier

```go
// fifoLiveClassifyOpenErr maps a non-nil error from the O_WRONLY|O_NONBLOCK
// open onto a verdict. ENXIO is the ONLY input that yields no-reader.
func fifoLiveClassifyOpenErr(err error) fifoLiveOutcome
```

Split out as a pure function over `error` for one reason: it is the only way to cover `EACCES` and `EINTR` without root-dependence or a skip. A real `EACCES` case needs a `mkfifo(…, 0o000)` FIFO, which **succeeds** as root — so a container CI running as uid 0 would flip that case to `reader-present` and fail. `EINTR` cannot be produced from a real open on demand at all. Driving the classifier with synthetic `syscall.Errno` values is deterministic, root-independent, and lets AC3's strongest clause ("no error path anywhere yields no-reader") be asserted exhaustively rather than sampled.

Extraction: `errors.Is(err, syscall.ENXIO)` for the one verdict-bearing case; `errors.As(err, &errno)` for the numeric value (verified above to work through `*os.PathError`). An error that carries no `syscall.Errno` is still `instrument-failed`, with `Errno` 0 and the error text in `Detail` — never silently reclassified.

### The errno name table

```go
// fifoLiveErrnoName returns a stable, platform-independent name for the
// errnos this instrument can record. syscall.Errno.Error() is the platform's
// message text, which differs across Darwin and Linux for the same errno.
func fifoLiveErrnoName(errno syscall.Errno) string
```

A small `map[syscall.Errno]string` covering the arms the tables exercise (`ENXIO`, `EACCES`, `EINTR`, `EPERM`, `ENOENT`, `ENOTDIR`, `EMFILE`, `ENFILE`, `ELOOP`) with a `fmt.Sprintf("errno(%d)", int(errno))` fallback. The number is always present in `Errno` regardless; the fallback never drops it.

**Do not reach for `golang.org/x/sys/unix.ErrnoName`.** `golang.org/x/sys` is an **indirect** dependency (`go.mod:18`); using it promotes it to direct, which is a `go.mod` edit — a production change this ticket forbids.

---

## Concurrency model

The read itself is synchronous and owns no goroutine. The only goroutines in play are `holdProbeFIFO`'s (one, holding the write end) and each test's `cmd.Wait()` waiter where the `:1123` shape calls for one.

**Non-perturbation, and why it depends on `holdProbeFIFO`.** Each read opens a *second* write end and closes it. POSIX delivers EOF to a FIFO reader only when the **last** writer closes, so while `holdProbeFIFO`'s write end is held the transient one is invisible to the reader. This is not incidental: if the read were taken without a persistent write end held, its own `Close()` would be the last writer closing, would EOF the reader, and would kill the very thing it claims to observe. AC4's test must therefore use `holdProbeFIFO` and not a hand-rolled writer.

**Cleanup ordering (AC4 only).** Register the reader-exit assertion **before** `holdProbeFIFO` so `t.Cleanup`'s LIFO puts it after the release. Registering it later runs it before the release and it fails spuriously; adding a kill-the-reader net after `holdProbeFIFO` makes it pass vacuously. Mirror `:1123` exactly.

**AC1 and AC4 want opposite things from their reader — two tests, not one.** AC1's flip test kills its reader mid-test; a killed reader makes the cleanup-ordering assertion vacuous, so the flip test registers **no** reader-exit cleanup: by the time the body finishes, the reader is already reaped. AC4's test needs a reader that survives to cleanup. They share `holdProbeFIFO`; they do not share a test.

---

## Error handling

Every arm, and the one input that produces each verdict:

| Input | Verdict | `Mode` | `Errno` / `ErrnoName` |
|---|---|---|---|
| `Lstat` fails (`ENOENT`, `ENOTDIR`, `ELOOP`, …) | `instrument-failed` | `""` | recorded |
| FIFO exists, some process holds the read end | `reader-present` | `p---------` | 0 / `""` |
| FIFO exists, no process holds the read end (`ENXIO`) | `no-reader` | `p---------` | 6 / `ENXIO` |
| Regular file at the path | `instrument-failed` | `-rw-------` | 0 / `""` |
| Character device (`/dev/null`) | `instrument-failed` | `Dcrw-rw-rw-` | 0 / `""` |
| Directory | `instrument-failed` | `drwx------` | 0 / `""` |
| Symlink → FIFO | `instrument-failed` | `Lrwxr-xr-x` | 0 / `""` |
| FIFO, open fails `EACCES` / `EINTR` / any other errno | `instrument-failed` | `p---------` | recorded |
| Open fails with a non-`syscall.Errno` error | `instrument-failed` | `p---------` | 0, text in `Detail` |

**`ENXIO` is the only input in the table that yields `no-reader`.** No error path anywhere else reaches that verdict — that is the invariant AC3 exists to pin, and the classifier table asserts it row by row.

**`Lstat`, not `Stat`, is deliberate.** A symlink at the path is an instrument anomaly: the caller creates the path itself, so a symlink there means something else wrote it, and `Stat` would follow it to a target the gate never inspected. Rejecting it as instrument failure is the conservative arm. Record it in the file header so a future reader does not "fix" it to `Stat`.

**TOCTOU.** The mode is observed at `Lstat` and the open happens after; a path that changed in between would be reported with the mode the gate actually admitted on. The outcome carries that mode precisely so the evidence shows what was gated, not what was assumed. No locking, no re-stat — the test owns the path.

---

## Testing strategy

Four tests, all offline, all under the `TestFIFOReaderLiveness_` prefix. Scenarios, not code — write them in the package's idiom.

### 1. `TestFIFOReaderLiveness_FlipsAcrossOneReaderLifetime` (AC1)

One FIFO, one reader's lifetime. Two differently-constructed FIFOs do not satisfy this AC.

- `holdProbeFIFO` creates the FIFO and holds the write end for the whole test.
- Start `cat <path>` via `exec.Command` (no shell). Wait on the rendezvous channel — that fires the instant `cat` opens the read end, with no poll lag.
- Read → assert `reader-present`, and assert `Mode` is the FIFO mode.
- `cmd.Process.Kill()`, then `cmd.Wait()`. **Both.** Assert nothing about `Wait`'s error — a killed process returns `*exec.ExitError` and that is the expected outcome, not a failure.
- Read the **same path** again → assert `no-reader`, with the write end still held. Assert `ErrnoName == "ENXIO"`.
- Register no reader-exit cleanup: the reader is already reaped, so such an assertion would be vacuous.
- Failure message should name the consequence: a read that does not flip means every killed command reads as alive, and the consumer's liveness claim is unfalsifiable.

**The `Wait()` is not optional and not a tidiness step.** See § The measurement — without it the second read returns `reader-present` on every sample, deterministically.

### 2. `TestFIFOReaderLiveness_NonPipePathsAreInstrumentFailures` (AC2, plus AC3's filesystem arms)

Table-driven over paths the test constructs itself — `holdProbeFIFO` creates its own FIFO, so these cases cannot use it.

- Regular file (`os.WriteFile` into `t.TempDir()`) → `instrument-failed`, `Mode` recorded and non-empty.
- Character device `/dev/null` → `instrument-failed`, `Mode` recorded. **This is the discriminating case**: a bare open on `/dev/null` succeeds with no reader anywhere, so a regular-file blacklist passes this test only if the gate is a positive `ModeNamedPipe` check.
- Directory (`t.TempDir()` itself) → `instrument-failed`.
- Symlink → FIFO → `instrument-failed`, mode `L…`.
- Missing path → `instrument-failed`, with the detail naming `lstat` — proving the mode gate ran first and the miss did not surface at the open.
- Every row additionally asserts the verdict is **not** `reader-present` **and not** `no-reader`. Both halves matter: the first catches the inverting failure, the second catches a gate that silently reports non-FIFOs as absent readers.
- Add a comment stating the test fails if the gate is removed or inverted into a blacklist — that is the property, not the row list.

### 3. `TestFIFOReaderLiveness_OpenErrnoArms` (AC3)

Table-driven over `fifoLiveClassifyOpenErr` with synthetic errors — `&os.PathError{Op: "open", Path: …, Err: syscall.EACCES}` and so on. Root-independent by construction.

- `ENXIO` → `no-reader`, `ErrnoName == "ENXIO"`, `Errno` numeric.
- `EACCES`, `EINTR`, `EPERM`, `EMFILE`, `ENFILE`, `ELOOP` → `instrument-failed`, each with name and number recorded.
- An unmapped errno → `instrument-failed`, number recorded, name falling back to the `errno(N)` form (never empty, never dropped).
- A non-`syscall.Errno` error (e.g. `errors.New("synthetic")`) → `instrument-failed`, `Errno == 0`, text in `Detail`.
- One assertion applied to every row except the `ENXIO` one: the verdict is not `no-reader`. Spell that out as the invariant under test.

### 4. `TestFIFOReaderLiveness_RepeatedReadsDoNotPerturbReader` (AC4)

`:1123`'s shape plus the read. Demonstrates non-perturbation rather than citing POSIX.

- Register the reader-exit assertion **first** (so LIFO runs it after `holdProbeFIFO`'s release), exactly as `:1123` does — including its deadline and its failure message register.
- `holdProbeFIFO`, start `cat`, wait on the rendezvous.
- Take the liveness read **at least three times** in succession; assert `reader-present` each time.
- Assert the reader is still blocked afterwards (the `select` on the exited channel with a short settle window, as `:1123` does at 500 ms) — this is the assertion that would fail if each read's `Close()` EOF'd the reader.
- Let cleanup release the write end; the pre-registered assertion then proves the reader exits only on the release.

### AC5 — the offline, positively-readable run

```
go test -tags e2e_realclaude -run '^TestFIFOReaderLiveness' -v ./internal/e2e/realclaude/
```

Must PASS with a `--- PASS` line for each of the four tests and **zero `--- SKIP` lines**, on a machine with no `claude` on PATH and no credentials. Paste the output into the PR as the evidence — a green exit code is not the deliverable, because a per-test skip also exits 0.

To keep it that way the file must not: call `WithWorktreeAuthenticated`, require `claude`, sit behind `PYRY_PROBE_BACKGROUND_TRIGGER` or any env gate, or start a daemon, phone, or harness.

Also run `go vet ./...`, `staticcheck ./...`, and `go test -race -tags e2e_realclaude -run '^TestFIFOReaderLiveness' ./internal/e2e/realclaude/`.

---

## Must NOT build

- **Any pid matcher, process-tree walk, or `ps` invocation.** The package already carries `probeDescendantsFromPS` (`:891`) for #1223's rig and #1235 is building another for the `agent-run` probes. This must not become a third. There is no pid in this ticket.
- **Any live claude, daemon, phone, or harness.** Nothing here needs one and it would destroy AC5.
- **Any edit to `fixtures.go` or `background_trigger_probe_test.go`.** Calling `holdProbeFIFO` from a new file in the same package needs no edit and no export. Concurrent branches (`feature/1174`, `feature/1219`, `feature/1230`) plus siblings #1234/#1235 land in this package; a new file is the only conflict-free shape.
- **Any `go.mod` change** — including the one `golang.org/x/sys/unix.ErrnoName` would force.
- **Any production change.**
- Any `t.Skip` in the new file, for any reason.
- Any `*testing.T` parameter on `fifoLiveRead` — #1240 calls it mid-turn and must be free to record an instrument failure rather than abort.

---

## Size check

| Red line | Limit | This design |
|---|---|---|
| New files | ≤ 3 | **1** |
| Total written LOC (prod + tests + helpers + branch logging) | ≤ ~600 | **~420** projected in this package's comment-dense style |
| New exported types / interfaces | ≤ 5 | **0** (all symbols unexported, test-only) |
| Consumer call sites needing simultaneous update | ≤ 10 | **0** (new file; #1240 lands later and is blocked on this) |
| Acceptance criteria | ≤ 5 | **5** |
| Distinct error / reject branches | ≤ ~10 | **4** verdict-producing arms in `fifoLiveRead` |
| Production source files touched (commit self-check) | < 5 | **0** |

No red line tripped. PO's `size:s` stands — I am not overriding it downward despite zero production code; the four-test matrix and the measurement work are real, and PO already corrected `xs` → `s` for exactly that reason.

**File-overlap check (branch-based, run 2026-07-29):** `git fetch origin --prune` then a sweep of every `origin/feature/<N>` branch's diff against `main`. Branches touching `internal/e2e/realclaude/`: `feature/1174` (`interactive_stream_new_session_test.go`), `feature/1219` (`sigterm_mid_tool_use_test.go`), `feature/1230` (`background_reach_probe_test.go`). None touches `fifo_reader_liveness_test.go`, `fixtures.go`, `background_trigger_probe_test.go`, or `go.mod`. No overlap; no `addBlockedBy` needed. Identifier prefixes `fifoLive` and `TestFIFOReaderLiveness` were additionally grepped on `main` and in those three branches' blobs — free everywhere, since branch overlap does not catch same-package identifier collisions.

---

## Open questions

1. **Does `#1240` want the outcome flattened or nested in its record?** `fifoLiveOutcome` is JSON-tagged and self-contained either way. Resolve in #1240; nothing here blocks on it.
2. **Should `fifoLiveRead` grow a "which errnos are expected on this platform" note?** ENXIO is 6 on both Darwin and Linux and EACCES is 13 on both, so the numeric values in the evidence happen to agree today. The name map makes this moot; no action unless a third platform ever appears (Windows is out of scope).
