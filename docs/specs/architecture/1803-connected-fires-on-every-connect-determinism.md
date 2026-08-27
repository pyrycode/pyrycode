# #1803 — Make `TestConnected_FiresOnEveryConnect` deterministic

**Size:** `s` (PO's label kept — the *diff* is `xs`-shaped: one test file, no
production change, ~20 written lines. The cost that earns `s` is the acceptance
work, and § Testing strategy below prices it at ~40 seconds.)
**Security-sensitive:** no (label absent; diff is test-only, § 3 pass skipped).

## Files to read first

| Where | Symbol | What to extract |
|---|---|---|
| `internal/transport/wssclient_test.go` | `TestConnected_FiresOnEveryConnect` | The test being fixed. Note it gates on `<-c.Connected()` and then calls `relay.ForceClose()` with nothing ordering the relay side. |
| `internal/transport/wssclient_test.go` | `newTestRelay` | The handler appends to `relayCtrl.conns` and signals `relayCtrl.connectedCh` *after* `websocket.Accept` returns. This is the whole bug. |
| `internal/transport/wssclient_test.go` | `relayCtrl.ForceClose` | Iterates `relayCtrl.conns`. On an empty slice it is a silent no-op — no error, no panic, nothing dropped. |
| `internal/transport/wssclient_test.go` | `TestConnected_DropsWhenObserverSlow` | The next test in the file, already written correctly: it waits on `relay.connectedCh` before every `ForceClose()`. Copy its ordering, not its loop. |
| `internal/transport/wssclient_test.go` | `TestBackoff_ResetAfterStableConnection` | #1802's rewrite. Its comment above the `relay.connectedCh` wait states the rule outright; the fix here is the same rule applied to a second site. |
| `internal/transport/wssclient.go` | `Client.serve` | Where the connect signal is emitted: `setConn(conn)` then a buffer-1 drop-on-full push to `connectedCh`. This is the mutation point for AC 2. |
| `internal/transport/wssclient.go` | `Client.Connected` | The contract the test's name claims — "emits a value on every successful underlying conn", single observer, drop-on-full. |
| `internal/transport/wssclient.go` | `Client.Connect` | Confirms the post-`serve` path redials **immediately**; the backoff sleep sits only on the dial-failure path. So a 2s deadline on the reconnect is generous, and lengthening it was never the fix. |
| `docs/knowledge/features/transport-package.md` | § "Test surface", the `newTestRelay` bullet | The registration-ordering rule is **already written down** there. This ticket is that documented rule being violated at one remaining call site. |

## Context

`TestConnected_FiresOnEveryConnect` reddens ~2 runs in 40 under 4-way-concurrent
whole-package load, and never in isolation. The ticket carried a hypothesis; it is
now confirmed, not inferred.

**The race.** `websocket.Accept` writes the 101 and returns; only *then* does the
handler in `newTestRelay` append the conn to `relayCtrl.conns`. The client's dial
unblocks on *reading* that 101, so `Client.serve` can run `setConn` and push the
connect signal while the relay's handler goroutine is still descheduled before the
append. The test wakes on `<-c.Connected()` and calls `relay.ForceClose()`, which
walks an empty slice, drops nothing, and returns cleanly. Conn #1 stays up, no
second connect is ever produced, and the 2s deadline expires with
`Connected did not fire after reconnect`. Load widens the descheduling window,
which is exactly why it only reproduces under contention.

**Evidence (measured 2026-08-27, this worktree, via `go test -overlay` — no
worktree writes).** Six runs of `go test -race -count=1 -run
'TestConnected_FiresOnEveryConnect$'`:

| # | Tree under test | Result |
|---|---|---|
| 1 | unmodified | PASS |
| 2 | today's test + a 20ms sleep between `websocket.Accept` and the `relayCtrl.conns` append | **FAIL** — `Connected did not fire after reconnect`, verbatim the observed message |
| 3 | proposed fix + that same 20ms sleep | PASS |
| 4 | today's test + first-connect-only mutant on `Client.serve` | **FAIL** |
| 5 | proposed fix + first-connect-only mutant | **FAIL** |
| 6 | proposed fix, unmodified production tree | PASS |

Row 2 is the diagnosis: widening the registration window turns the intermittent
red into a deterministic one carrying the same message. Row 3 shows the fix
survives a window 20ms wide, i.e. orders of magnitude past anything scheduler
pressure produces.

**Rows 4 and 5 settle the ticket's open "possible false green" question: the
assertion is not vacuous.** Today's test already reddens under the AC-2 mutant,
and so does the fixed one. So this is a false *red* only — the every-connect
property was genuinely pinned all along, and the fix must not weaken it. That is
the constraint the design below is written against: nothing is relaxed, one
ordering gate is added, and one gate is added purely to make a future red
readable.

No ADR is warranted. This is a second application of a rule the package overview
already states.

## Design

**Diff is test-only, one function, one file.** No production change. (Rows 4/5
above rule out the ticket's escape hatch — the defect is not in `Client.serve` or
`Client.Connected`, so `needs-rework:po` for a `security-sensitive` relabel does
**not** apply.)

Replace the two-gate body of `TestConnected_FiresOnEveryConnect` with four gates,
in this order. Every gate stays an inline `select` with
`case <-time.After(2 * time.Second)` — the same deadline as today, per AC 4:

| # | Wait on | `t.Fatal` message | Why it exists |
|---|---|---|---|
| 1 | `relay.connectedCh` | `relay never registered the first conn` | **The fix.** Establishes happens-before: the relay has appended conn #1 to `relayCtrl.conns`, so the `ForceClose` at gate 3 provably has something to close. Satisfies AC 3. |
| 2 | `c.Connected()` | `Connected did not fire after first connect` | Unchanged assertion — the signal fires on the first conn. Also drains the buffer-1 channel so gate 4 cannot read a stale token. |
| 3 | — (`relay.ForceClose()`) | — | The induced drop, now strictly after registration. |
| 4a | `relay.connectedCh` | `client never reconnected to the relay` | **Diagnostic split.** A timeout here means the reconnect never reached the relay — a dial/harness fault, not a signal regression. |
| 4b | `c.Connected()` | `Connected did not fire after reconnect` | The property under test. Because 4a passed, a red here can only mean the relay accepted a fresh conn and `Client.serve` failed to signal it. |

Gate 4a is the part that is not strictly required to fix the flake, and it is the
part that makes the ticket's user story true. The user story asks that a red run
*carry signal*. Today's single failure message covers two unrelated causes — a
`ForceClose` that dropped nothing, and a genuinely missing signal — and probes 2
and 4 above produce **the same string** for both. Gate 4a separates them
permanently: after this change, `Connected did not fire after reconnect` is
reachable only when a reconnect provably happened.

**Ordering rationale, to be captured in a short comment above gate 1** (mirror
the wording of the comment `TestBackoff_ResetAfterStableConnection` carries):
`relayCtrl.ForceClose` can only drop conns the relay handler has already
registered, and the handler registers *after* `websocket.Accept` returns — later
than the client's own dial, which unblocks on the 101. Gating on `<-c.Connected()`
alone orders nothing on the relay side. Cite symbols, never line numbers —
`make cite-guard` is diff-scoped and will fail the branch otherwise.

**Deliberately not done** (each of these was considered and rejected; do not
"improve" the fix into one of them):

- **No new deadline, and no longer one.** All four gates keep 2s. The reconnect
  path in `Client.Connect` redials immediately after a healthy `serve` return —
  there is no backoff sleep on it — so a longer wait would only have masked the
  race, never fixed it.
- **No `relayCtrl` helper method and no shared `waitSignal` test helper.** Four
  inline `select`s repeat a shape, but the file's established idiom is the inline
  `select` at every site (`TestConnected_DropsWhenObserverSlow`,
  `TestBackoff_ResetAfterStableConnection`), and the per-gate `t.Fatal` strings in
  the table are the diagnostic value a shared helper would flatten. A helper with
  one caller is also the wrong time to extract one.
- **No switch to `c.DropConn()`.** It would remove the harness race outright
  (`Client.setConn` precedes the connect-signal push, so a live conn is guaranteed
  once `Connected()` fires), but it changes the drop's origin from server-side to
  client-side and stops proving the reconnect reached the relay. The relay-side
  drop is what production does when the relay recycles a conn.
- **No `relay.ConnCount()` assertion.** Gate 4a already proves a second accept.
- **`TestReceive_ReturnsErrDisconnectedOnConnDrop` stays untouched.** It has the
  same unsynchronised shape and the ticket puts it out of scope; no failure has
  been observed there. Leave it. If it ever reddens, it gets its own ticket.

## Concurrency model

No goroutines are added. The change is one happens-before edge in the test.

```
relay handler goroutine          client goroutine (Connect → serve)      test goroutine
  websocket.Accept → 101 ──────► dial returns
  append to relayCtrl.conns        setConn(conn)
  push relayCtrl.connectedCh       push connectedCh (buffer 1, drop-on-full)
          │                                  │
          └──── gate 1 ◄─────────────────────┼──── gate 2
                                             │      ForceClose()  ← now provably after the append
  (conn #1 read error) ◄─────────────────────┘
  ... reconnect: Accept → append → push ─────────── gate 4a
                                   push connectedCh ─ gate 4b
```

Today only the right-hand edge exists, so `ForceClose()` is unordered with respect
to the append. Gate 1 adds the left-hand edge.

Channel-capacity reasoning, which is what makes gate 4b safe:

- `relayCtrl.connectedCh` is buffered 16 with a non-blocking send; exactly two
  conns exist over the test's lifetime, so no token is dropped and gates 1 and 4a
  drain conn #1's and conn #2's tokens in that order.
- `Client.Connected` is buffer 1 with drop-on-full and a single observer. Gate 2
  drains conn #1's token before `ForceClose`, so the channel is empty when
  `Client.serve` pushes conn #2's — gate 4b therefore observes the *second* signal,
  never a re-read of the first. This is precisely why gate 2 must stay ahead of
  gate 3, and why the fix cannot be "wait on the relay instead of the client".

## Error handling

Test-only; there are no new production failure modes. The failure surface is the
four `t.Fatal` strings in the § Design table, and their whole purpose is that each
one names a distinct cause. `newTestRelay` already registers `t.Cleanup(r.Close)`
and the test already registers `cancel()` + `c.Close()`; cleanups run LIFO, so the
client tears down before the relay. Unchanged.

## Testing strategy

Three checks, all cheap. Run them in this order.

**AC 1 — 40-run load sweep, ~40s.** From the worktree:

```bash
for round in $(seq 1 10); do
  for i in 1 2 3 4; do go test -race -count=1 ./internal/transport/ > /tmp/s_${round}_${i}.log 2>&1 & done
  wait
done
grep -h -c '^ok' /tmp/s_*.log | paste -sd+ - | bc   # want 40
```

Measured on the proposed diff in this worktree, 2026-08-27: **40 ok / 40 runs,
37.8s wall clock, zero failures.** Report the count, not the exit code.

**AC 2 — the first-connect-only mutant.** Do this with `go test -overlay` so
nothing is written into the worktree and no mutant can reach a commit. In
`Client.serve`, wrap the `connectedCh` push in a package-level `sync.Once`
(`sync` is already imported), so the signal fires on the first conn and never
again:

```bash
SP=$(mktemp -d)
python3 - "$PWD" "$SP" <<'PY'
import json, pathlib, sys
w, sp = sys.argv[1], sys.argv[2]
p = pathlib.Path(w, "internal/transport/wssclient.go")
src = p.read_text()
old = "\tselect {\n\tcase c.connectedCh <- struct{}{}:\n\tdefault:\n\t}\n"
new = "\tmutantOnce.Do(func() {\n\t\tselect {\n\t\tcase c.connectedCh <- struct{}{}:\n\t\tdefault:\n\t\t}\n\t})\n"
assert src.count(old) == 1
pathlib.Path(sp, "m.go").write_text(src.replace(old, new) + "\nvar mutantOnce sync.Once\n")
pathlib.Path(sp, "ov.json").write_text(json.dumps({"Replace": {str(p): str(pathlib.Path(sp, "m.go"))}}))
PY
go test -race -count=1 -run 'TestConnected_FiresOnEveryConnect$' -overlay="$SP/ov.json" ./internal/transport/
```

**Must FAIL**, with `Connected did not fire after reconnect` — and, critically,
*not* with `client never reconnected to the relay`. Verified on the proposed diff
(row 5 of the evidence table): the reconnect gate passes, the signal gate reddens.
That distinction is the AC. A `-run` filter is required; the mutant's `sync.Once`
is package-level and would poison `TestConnected_DropsWhenObserverSlow` if the
whole package ran under it.

**Regression — the widened-window probe (optional, ~10s, high value).** Same
overlay technique, but against the *test* file: insert
`time.Sleep(20 * time.Millisecond)` in `newTestRelay`'s handler between the
`websocket.Accept` error check and the `relayCtrl.conns` append. The unfixed test
fails deterministically; the fixed one passes. If AC 1's sweep comes back green
but you want proof the fix is what made it green rather than luck, this is the
run that shows it.

**Do not run `make check` as the AC-1 evidence.** A single green package run says
nothing about a 1-in-20 flake. Run the sweep and report the count.

## Open questions

- **Nothing pins the `setConn`-before-signal ordering in `Client.serve`.** The
  #248 contract is that `Connected()` fires *after* `setConn`, so an observer
  waking on it finds `Send`/`Receive` already wired to the live conn. A mutant
  that swapped those two statements would pass every test in the package,
  including the one this ticket fixes. Out of scope here — this ticket must not
  grow a second property. Worth its own ticket if anyone wants it; noting it so
  the observation is not lost.
- **`TestReceive_ReturnsErrDisconnectedOnConnDrop` and `TestPing_FiredAt30s`** are
  carried as out-of-scope by the ticket body and stay that way. The first has the
  same unsynchronised shape (a 50ms sleep narrows the window without closing it);
  the second reddened 1 of 40 once and 0 of 80 twice.
- **The registration-ordering rule now has three call sites obeying it and one
  documented statement of it** (in `transport-package.md` § "Test surface"). If a
  fourth site appears, that is the point at which a `relayCtrl` helper stops being
  premature. Not now.
