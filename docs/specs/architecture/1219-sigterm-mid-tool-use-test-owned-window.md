# #1219 — Test-owned open window for `TestRealClaude_SigtermMidToolUse`

**Size:** S (PO's `size:s` confirmed, not overridden). Test-only, one file.
**Scope:** `internal/e2e/realclaude/sigterm_mid_tool_use_test.go`. Zero production
source files. No new exported identifiers.

---

## Files to read first

| Path | What to extract |
|---|---|
| `internal/e2e/realclaude/sigterm_mid_tool_use_test.go:1-56` | The header block you are rewriting (AC5). It already records defeat #1 (`sleep 30` → #563) and the branch-B terminal-shape reasoning — both must survive the rewrite. |
| `internal/e2e/realclaude/sigterm_mid_tool_use_test.go:71-88` | The three fixture constants (`sigtermSystemPrompt`, `sigtermProcessName`, `sigtermPrompt`). Two of the three change. |
| `internal/e2e/realclaude/sigterm_mid_tool_use_test.go:116-292` | The test body. Note the exact ordering of the four invariants — AC3 forbids relaxing 1/2/3, so the only edits inside this range are the new rendezvous step and invariant 4b's message. |
| `internal/e2e/realclaude/sigterm_mid_tool_use_test.go:350-372` | `waitForBashSubprocess` — the process-tree walk that returns the Bash **pgid**. It keeps its job (pgid capture) but loses its job as the "command is genuinely running" proof. |
| `internal/e2e/realclaude/sigterm_mid_tool_use_test.go:398-425` | `leafCommandName` / `pgidOf`. `leafCommandName` returns `filepath.Base(argv[0])` — for `/bin/cat …` that is `"cat"`. Unchanged. |
| `internal/e2e/realclaude/sigterm_mid_tool_use_test.go:475-512` | `waitForBashToolUseOnDisk` + `findBashToolUse`. `findBashToolUse` returns `(id, index)`; the index is what lets the new failure message quote claude's verbatim `tool_use` envelope. |
| `internal/e2e/realclaude/tool_loop_test.go:152-183` | `contentBlock` + `parseContentBlocks`. **There is no `Input` field.** That is why the new failure message quotes `JSONLEntry.Raw` rather than decoding `input.timeout` — quoting the raw line needs no shared-struct edit and stays inside this ticket's one-file scope. |
| `internal/e2e/realclaude/fixtures.go:56-64` | `WithWorktree` = `t.TempDir()` + `t.Setenv("HOME", dir)`. The returned `workdir` is a plain temp dir (not a git worktree) and already holds `prompt.txt` / `system.txt` — it is the right home for the FIFO. Path contains no spaces. |
| `internal/e2e/realclaude/per_agent_test.go:138-144` | `truncate(b []byte) string` — 1 KiB cap, file-local to that file but package-visible. Reuse it for the new evidence dumps. |
| `Makefile:55-57` | `make e2e-realclaude` = `go test -tags e2e_realclaude ./internal/e2e/realclaude/...`. The new mechanism self-test runs under this tag **without** credentials. |
| `docs/knowledge/codebase/422.md` | The contract this test guards, and the "file-local helper until a second test needs it" precedent the ticket's Technical Notes reaffirm. |

---

## Context

`TestRealClaude_SigtermMidToolUse` is the only live coverage of the
shutdown-while-a-tool-is-running contract (#422). It is RED on `main` — not
from a production regression, but because invariant 4 (*the signal genuinely
landed mid-`tool_use`*) can no longer be staged. claude 2.1.220 attaches its
own timeout to the Bash call and, on expiry, **backgrounds the command and
returns a `tool_result` immediately**. The fixture command `tail -f /dev/null`
therefore reports itself finished before the test can signal.

This is the fixture's second defeat. The first (`sleep 30`, blocked by claude
2.1.158's standalone-sleep guard) was fixed by swapping the command (#563).
A third command swap is a treadmill: each guess survives only until claude
learns to bound it, and the protected invariants go dark in between.

**Root cause, stated precisely.** In the 2026-07-27 probe the `timeout:5000`
field sat inside the `tool_use` **params**, with a matching `description`
("…with 5 second timeout"). That means *the model* chose the bound, not a fixed
client policy — it saw a command whose shape (`tail -f`) advertises "blocks
forever" and defensively capped it. So the lever is not "ask claude for a
longer timeout" (steering the model is the treadmill by another name); the
lever is **who owns the thing that blocks**.

---

## Design

### The inversion

Today, blocking is a property of the **command** claude picked. New shape:
blocking is a property of an **artifact the test created and holds**.

```
test:   mkfifo <workdir>/sigterm-hold
test:   goroutine → open(fifo, O_WRONLY)      [blocks: no reader yet]
claude: Bash → cat <workdir>/sigterm-hold     [blocks: no writer yet]
        ↓ both opens complete at the same instant — a rendezvous
test:   holds the write end, never writes     → cat blocks in read() forever
test:   Close() (t.Cleanup only)              → cat sees EOF and exits
```

Three properties fall out, and each maps to an AC:

1. **The window's end is the test's** (AC2). `cat` cannot complete while the
   test holds the write end open. The only release is `Close()`, which the
   test owns.
2. **The window's start is race-free** (supports AC1). The blocking `open()`
   returns *at the instant* `cat` starts — no 100 ms poll, no process-tree
   walk lag. Today's detection lag is precisely what lost the race against a
   5 s model-chosen timeout; removing it is the largest single improvement in
   the odds even if claude still bounds the call.
3. **The command carries no blocking cue.** `cat <path>` reads as an
   instantaneous file read. The model has no `tail -f`-shaped signal that
   would prompt it to attach a defensive timeout.

**Load-bearing vs. not — state this explicitly in the header.** (1) and (2) are
load-bearing and are pure test-side mechanics. (3) is *not* load-bearing: it is
cue reduction, and if claude bounds every Bash call regardless, (3) buys
nothing while (1) and (2) still hold. The FIFO name is chosen neutrally
(`sigterm-hold`, no `.fifo` suffix) for the same non-load-bearing reason. A
future contributor must not read (3) as "the fix is picking a command claude
won't bound" — that is the treadmill this ticket exists to end.

### New file-local helper

```go
// holdFIFO creates a FIFO at path, then blocks a goroutine on open(O_WRONLY).
// The returned channel receives exactly once, at the instant a reader opens
// the FIFO — i.e. the instant claude's `cat` starts executing.
func holdFIFO(t *testing.T, path string) <-chan struct{}
```

Behaviour contract:

- Creates the FIFO with `syscall.Mkfifo(path, 0o600)` (present on both darwin
  and linux in stdlib `syscall`; the file already imports it for `Kill`).
  `t.Fatalf` on failure.
- Spawns one goroutine doing the blocking `os.OpenFile(path, os.O_WRONLY, 0)`.
  On success it closes the signal channel and parks the `*os.File` where the
  cleanup can reach it. The file is **never** exposed to the caller — the
  caller must not be able to close it early (see hazard below).
- Registers a single `t.Cleanup` that (a) opens the read end with
  `os.O_RDONLY|syscall.O_NONBLOCK` and closes it — POSIX guarantees this
  never blocks and it releases the goroutine if claude never ran the command —
  then (b) closes the write end if the rendezvous happened.
- Returns the receive-only signal channel.

**Hazard — record it in the doc comment.** Closing the write end before
invariant 1 has been asserted makes `cat` exit on its own, and invariant 1
("pyry reaped the Bash process group") would then pass **vacuously**. The
release lives in `t.Cleanup`, which by construction runs after the test body,
so the assertion order is safe by structure rather than by discipline. Do not
add a caller-facing `release()`.

### Changes to the test body

Only two edits inside `TestRealClaude_SigtermMidToolUse`:

1. **Before writing `prompt.txt`**: build `fifoPath = filepath.Join(workdir,
   "sigterm-hold")`, call `holdFIFO`, and build the prompt from the path.
2. **Between `waitForDirectChild` and `waitForBashSubprocess`**: the rendezvous
   gate — `select` on the `holdFIFO` channel vs. `time.After(25 * time.Second)`,
   matching today's budget for "claude got as far as the Bash call". The
   timeout arm follows the established shape in this file: SIGKILL pyry's group,
   then `t.Fatalf` naming the FIFO path and dumping `truncate(stderr.Bytes())`.

Everything downstream of that is untouched **except** invariant 4b's message.
`waitForBashSubprocess` keeps its 25 s budget and its role of capturing
`bashPGID`; it now runs against a process already proven alive, so a timeout
there means "could not read the pgid", not "claude never ran the command" —
worth one sentence in its call-site comment.

### Constant changes

| Constant | Now | Becomes |
|---|---|---|
| `sigtermProcessName` | `"tail"` | `"cat"` |
| `sigtermPrompt` | const string | a `fmt.Sprintf` over a const format, interpolating `fifoPath` |
| `sigtermSystemPrompt` | — | unchanged |

Keep the prompt minimal — ``Use the Bash tool to run `cat <path>`. Do nothing
else.`` **Do not** add "do not set a timeout" or similar. A nudge would not be
load-bearing, and its presence in the prompt would teach the next contributor
that steering is the mechanism.

Accepted risk on `sigtermProcessName = "cat"`: a stray `cat` among pyry's
descendants would be matched. The walk only sees pyry's own subtree and the
prompt runs exactly one command, so this is not worth defending against with
a full-command-line match — and matching the command line would find the
`zsh -c` wrapper first (same pgid, but it exists before `cat` does).

---

## Concurrency model

One new goroutine, owned by `holdFIFO`, for the lifetime of the test:

- **Blocks** in `open(O_WRONLY)` until a reader arrives.
- **Communicates** by closing a `chan struct{}` — closure, not a send, so the
  `select` in the body cannot miss it and no buffering question arises.
- **Shutdown**: the `t.Cleanup` non-blocking read-open guarantees the goroutine
  is released even on the path where claude never runs the command, so the
  test binary never carries a permanently parked OS thread into later tests.

No other concurrency changes. The existing `cmd.Wait` goroutine, `syncBuffer`
mutex, and the two SIGKILL cleanups are untouched.

---

## Error handling

### Invariant 4b's failure message (AC4)

The discrimination is **structural, not textual**. Do not branch on claude's
prose (`"moved to the background"`), because that string is exactly the kind of
thing that goes stale — matching on it would rebuild the treadmill inside the
failure path. The structural fact is stronger and permanent:

> The fixture command blocks on a FIFO this test created and still holds open
> (the write end is closed only in `t.Cleanup`, which runs after this
> assertion). It therefore **cannot** complete on its own.

So any `tool_result` at all means claude ended the call. The message states
that, then orders the checks:

1. **claude's verbatim `tool_use` envelope** (`truncate(events[bashIdx].Raw)`).
   An `input.timeout` field means claude bounded the call itself → fixture
   defeat #3, not a production regression.
2. **claude's verbatim `tool_result` envelope** (`truncate(e.Raw)`). Prose like
   *"did not complete within its Ns timeout and was moved to the background"*
   is the claude 2.1.220 shape this ticket fixed for (#1219); a refusal
   (*"Blocked: …"*) is the #563 shape.
3. **Only if neither**: the FIFO write end was released early — a test bug in
   `holdFIFO`'s lifetime — or, last, pyry's SIGTERM path regressed.

Point the reader at the file header's fragility history and at the
`needs-rework:po` escape hatch in #1219's Technical Notes.

### Other failure paths

- **Rendezvous timeout** — claude never opened the FIFO. Message names the FIFO
  path, says claude did not reach the Bash call (or ran a different command),
  and dumps stderr. Follows the existing SIGKILL-then-`t.Fatalf` shape.
- **`syscall.Mkfifo` failure** — `t.Fatalf` naming the path; a pre-existing
  file at that path is the realistic cause.
- Invariants 1/2/3 and 4a keep their current messages verbatim (AC3).

---

## Testing strategy

**Live gate (AC1).** `make e2e-realclaude` against claude 2.1.220 or later,
with `TestRealClaude_SigtermMidToolUse` PASSING and not skipped. One haiku
call, ~$0.005. This is the acceptance run — a SKIP is not acceptance, and the
ticket carries `needs-real-claude` for exactly this reason.

**Mechanism self-test (proves AC2 without burning a call).** Add
`TestHoldFIFO_RendezvousAndRelease` in the same file. It needs the
`e2e_realclaude` build tag but **no credentials and no claude**, so it runs on
every `make e2e-realclaude` regardless of auth. Scenarios, as bullets:

- **Rendezvous fires on a reader.** `holdFIFO` over a `t.TempDir()` path; assert
  the channel has *not* fired before any reader exists; start `exec.Command
  ("cat", fifoPath)`; assert the channel fires within a few seconds.
- **The command stays blocked while the test holds the end.** After the
  rendezvous, assert the `cat` process is still alive after a short settle
  window — this is the AC2 property, mechanically checked.
- **Release ends it.** Trigger the cleanup path (run the two bullets above
  inside a subtest so its `t.Cleanup` fires) and assert the spawned `cat`
  exits — i.e. the test, and only the test, closed the window.
- **No reader ⇒ no leak.** `holdFIFO` with nothing ever opening the FIFO;
  assert the subtest returns cleanly (the cleanup's non-blocking read-open
  released the parked goroutine rather than hanging the suite).

Reap the spawned `cat` in each scenario so the package leaves no strays.

**Not covered, deliberately.** Whether claude auto-backgrounding changes
production behaviour for `pyry agent-run` is #1221.

---

## Open questions

1. **Does claude bound a plain `cat <path>` anyway?** The design's odds rest on
   (1) and (2) above, not on the answer, but the answer decides whether the run
   goes green on the first try. If the live run shows an `input.timeout` on the
   `cat` call *and* the timeout beats the `waitForBashToolUseOnDisk` flush, the
   ticket's **bounded escape hatch** applies: stop, comment on #1219 with the
   probe transcript, add `needs-rework:po`. Do **not** grow into the filer's
   fallback (restructuring the suite's coverage model) — explicitly out of scope.
2. **Cheap hardening to try before the escape hatch.** `BASH_DEFAULT_TIMEOUT_MS`
   via `t.Setenv` (it flows through — `spawnPyryAgentRun` does
   `cmd.Env = os.Environ()`). Expected to help only when the model omits the
   `timeout` param, since the env var sets a *default*, not a floor;
   `BASH_MAX_TIMEOUT_MS` caps upward and cannot raise a model-chosen 5 s. Worth
   one probe, not worth designing around. This is deterministic client config,
   not prompt steering — the distinction matters for AC2.
3. **Is the `waitForBashToolUseOnDisk` gate still needed?** pyry emits the
   assistant `tool_use` on its own stdout stream-json before the JSONL flush,
   so a stdout-based gate would be earlier. The header records the flush
   genuinely lagging, and AC3 forbids weakening the disk-backed assertion, so
   the disk gate stays. If the live run shows the flush is the binding
   constraint, note the measurement in the header for whoever faces defeat #3
   — do not change the gate under this ticket.

---

## Header rewrite (AC5)

The rewritten header must carry, in this order:

1. The four invariants (unchanged prose).
2. **Fragility history**, as a dated list: `sleep 30` → defeated by claude
   2.1.158's standalone-sleep guard (#563, fixed by a command swap);
   `tail -f /dev/null` → defeated by claude 2.1.220's background-on-timeout,
   with the observed `timeout:5000` sitting in the model-chosen `tool_use`
   params (#1219).
3. **Why the new shape is structurally different**: the blocking artifact is
   created and held by the test, so the window's end is test-owned; the
   rendezvous makes the window's start race-free. Name (3) — the neutral
   command and file name — as explicitly *not* load-bearing, so nobody reads
   this as a third command guess.
4. **What to do on defeat #3**: this is a claude-side policy change, not a
   pyry regression; check the `tool_use` envelope for `input.timeout` first;
   the escape hatch is `needs-rework:po`, not a fourth command.
5. The existing event-driven-timing and branch-B paragraphs, updated for the
   rendezvous but otherwise intact.
