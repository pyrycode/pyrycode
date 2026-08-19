# #1608 — Key the context-window usage reader by session id

**Size:** XS · **Split from:** #1587 · **Labels:** `security-sensitive`

## Files to read first

| File | Symbol | What to extract |
|---|---|---|
| `cmd/pyry/snapshot_usage.go` | `snapshotUsageFor` | The whole function. Its doc comment carries the #1214 / #989 history and must survive the edit intact — you are changing the signature, not the reasoning. |
| `cmd/pyry/snapshot_usage_test.go` | `TestSnapshotUsageFor_ReadsTheBoundTranscript`, `TestSnapshotUsageFor_SiblingTranscriptIsNeverRead`, `TestSnapshotUsageFor_UnresolvableReportsFreshSession`, `TestSnapshotUsageFor_UnwiredReturnsNilSeam`, `writeUsageTranscript` | All four tests plus the fixture helper. Two move mechanically, one strengthens, one loses half its body to the new wiring test. |
| `cmd/pyry/relay.go` | `startRelayV2` | The sole production call site: the `snapshotUsage := …` line and the `SnapshotUsage:` field it feeds. Read the surrounding comment block — it describes the nil-seam contract you are preserving. |
| `cmd/pyry/relay.go` | `boundSessionIDForActive` | The in-file precedent for a named, unit-testable resolver extracted out of untestable wiring. Mirror its doc-comment density and fail-closed framing. |
| `cmd/pyry/bound_session_active_test.go` | `TestBoundSessionIDForActive` | The test shape for that precedent — subtests named after the property, not the input. |
| `internal/transcript/transcript.go` | `StatByID`, `ValidStem` | The validate-before-join contract. `ValidStem` is an anchored canonical-UUIDv4 regexp; this is why a caller-supplied id is safe. Do not add a second layer. |
| `internal/contextwindow/usage.go` | `Read` | The two "nothing to report" inputs vs the error path. Note that on error it returns a **zero** `Usage` — including `WindowTokens: 0`. That fact is what makes AC #2's new row discriminating. |
| `internal/relay/v2session_seams.go` | `V2SessionConfig.SnapshotUsage` | The documented `nil ⇒ handler reports zeros` contract. This spec preserves it exactly; the field's type does not change. |
| `cmd/pyry/main.go` | the `relayWiring` literal in `runSupervisor` | `bootstrapIDFn: func() string { return string(pool.BootstrapID()) }` — the single producer, always non-nil. |
| `internal/e2e/relay_v2_stream_run_config_test.go` | `TestRelayV2_StreamRequestSessionSettings` | Read only to confirm you have not modified it. It must stay green **unmodified**; see AC #4 below for why it is the no-regression half and not the discriminating half. |

## Context

`snapshotUsageFor` builds the relay's `SnapshotUsage` seam — the reader behind `used_tokens` / `window_tokens` on both the `screen_snapshot` and the `session_settings` replies. It already resolves a transcript **by session id**; it merely *closes over* a bootstrap-id function, so the id is fixed when the reader is built and no caller can ask about a different session.

Moving the id from a construction-time closure to a call argument is the prerequisite for reporting a conversation's own occupancy instead of the bootstrap's. **No reported value moves and the wire stays byte-identical** — the one production call site binds the same bootstrap-id source at the wiring point instead of inside the reader.

Collapsing the three run-configuration seams is #1609's job. Not here.

## Design

### Two functions where there is one

**`snapshotUsageFor(dir string) func(id string) (usedTokens, windowTokens int)`** — the by-id reader.

- Returns `nil` iff `dir == ""`.
- The returned reader takes the session id **per call**: `transcript.StatByID(dir, id)` → `res.Path` on success, `""` on any error → `contextwindow.Read(path)` → on error, recover with `contextwindow.Read("")`.
- Behaviour of the returned closure is otherwise byte-for-byte today's, with `bootstrapID()` replaced by the `id` parameter.

**`bootstrapSnapshotUsage(dir string, bootstrapID func() string) func() (usedTokens, windowTokens int)`** — the wiring-point seam builder.

- Returns `nil` if `bootstrapID == nil`, or if `snapshotUsageFor(dir)` returned `nil`.
- Otherwise returns a closure that calls the reader with `bootstrapID()` on each invocation.
- The nil check happens at **build** time, before any closure exists, so no path can invoke a nil id source.

Both live in `cmd/pyry/snapshot_usage.go`. The ticket left the file placement open and named `boundSessionIDForActive` as the *shape* precedent, not the location. Co-locating keeps both halves of the nil contract in one file and lets the relocated assertion sit next to its surviving sibling in `snapshot_usage_test.go` — one test file touched instead of two.

### Why the nil-dir guard stays inside the reader

The ticket makes this the architect's call. It stays inside `snapshotUsageFor`, because it is not merely a seam-shaping concern — it protects the reader itself.

With `dir == ""`, `StatByID` would `filepath.Join("", id+".jsonl")`, yielding the **relative** path `<id>.jsonl` resolved against the daemon's working directory. A stray file of that name in cwd would be read and reported as a session's occupancy. The guard is the thing that prevents that, so it belongs where the join happens, not at the wiring point.

That also keeps AC #1's subject coherent: "one reader instance, built over a single wired sessions directory" describes a builder that still has exactly one legitimate unwired condition to detect.

### Wiring

`startRelayV2`'s `snapshotUsage := snapshotUsageFor(w.claudeSessionsDir, w.bootstrapIDFn)` becomes `snapshotUsage := bootstrapSnapshotUsage(w.claudeSessionsDir, w.bootstrapIDFn)`. Nothing else in the config literal moves; the `SnapshotUsage:` field, its comment, and `V2SessionConfig.SnapshotUsage`'s type are untouched. Update the `snapshotUsageFor, built above over …` phrasing in the `SnapshotUsage:` comment to name the new builder.

### Data flow

```
relay handler
  └─ SnapshotUsage()                        (func() (int, int) — unchanged type)
      └─ bootstrapSnapshotUsage's closure
          ├─ bootstrapID()  →  pool.BootstrapID()
          └─ reader(id)                     (func(string) (int, int) — the new shape)
              ├─ transcript.StatByID(dir, id)   validate stem → join → stat
              └─ contextwindow.Read(path)       ""/err → fresh-session report
```

### What must not change

- **No logger.** The reader takes none today and must not gain one (AC #2). `cmd/pyry/snapshot_usage.go` imports no logging package; keep it that way. This is enforced structurally by the signature and the import block, not by a test — a test cannot observe the absence of a logger the function has no way to reach.
- **No second validation layer.** `StatByID` validates the stem before any join. Do not add a shape check, a length check, or a `filepath.Clean` on top of it (§ Security review, Trust boundaries).
- **No error return.** Every failure still collapses to the fresh-session report.
- **No new fields on `relayWiring`, no signature change to `startRelayV2`.**

## Concurrency model

No goroutines are created or destroyed. The reader is invoked from relay handler goroutines, as today.

The change strictly *reduces* captured state: the reader's closure now captures only `dir`, an immutable string, instead of also capturing a function that reads pool state. `contextwindow.Read` is documented safe for concurrent use — each call opens its own file and reader, neither shared.

`bootstrapSnapshotUsage`'s closure calls `bootstrapID()` per invocation, which reaches `Pool.BootstrapID` under the pool's `RLock` — identical to today's timing. No new locks, no new lock ordering, no new shared mutable state.

## Error handling

Unchanged contract: three failure classes, one outcome.

| Class | Where it fails | Path value |
|---|---|---|
| Empty, malformed, or hostile id | `ValidStem` rejects before any `filepath.Join` — **no filesystem access at all** | `""` |
| Transcript not yet written | `os.Stat` returns `ENOENT` | `""` |
| Resolved path stats clean but cannot be read | `contextwindow.Read` on a real path | non-empty → recovery branch |

All three report zero used against the default window — exactly what a brand-new session looks like, which is the truthful answer in each case. No error reaches the caller; no id and no path reaches a log line.

## Testing strategy

All in `cmd/pyry/snapshot_usage_test.go`. `writeUsageTranscript` and `defaultWindow` are reused unchanged.

### `TestSnapshotUsageFor_ReadsTheBoundTranscript` — mechanical

Build one reader over `dir`, call it with the id. Same assertions.

### `TestSnapshotUsageFor_SiblingTranscriptIsNeverRead` — strengthens (AC #1)

Keep the fixture (bound written first, sibling second so the sibling is also newest by mtime). Build **one** reader and ask it for **both** ids:

- `read(boundID)` → `1000`
- `read(siblingID)` → `150000`

Each assertion is the sole red for a distinct mutant, which is the reason both belong here:

| Mutant | Symptom | Killed by |
|---|---|---|
| Reader ignores its argument and uses a captured id | both calls return `1000` | the `siblingID` assertion |
| Reader scans the directory for the newest file | both calls return `150000` | the `boundID` assertion |

Two ids with distinct figures also settle "neither transcript is ever read for the other's id" in both directions: any cross-read that reaches the result changes a number.

### `TestSnapshotUsageFor_UnresolvableReportsFreshSession` — one new row (AC #2)

One reader over `dir`; the table gains a per-row setup hook (an optional `prepare func(*testing.T, string)`, nil for the existing rows). Every row asserts `used == 0 && window == defaultWindow`.

Existing rows, unchanged in substance: no transcript written yet · empty id · path traversal attempt · not an id at all.

New row — **"resolved path stats clean but cannot be read"**: `prepare` creates a **directory** at `<dir>/<id>.jsonl` for an otherwise-valid uuid.

This is the deterministic stand-in for the unlink race the ticket calls undrivable. Measured on this tree (darwin, 2026-08-19):

- `StatByID` **succeeds** — a directory stats clean, so `path` is non-empty and the row genuinely reaches the recovery branch;
- `contextwindow.Read(path)` fails: `contextwindow: scan transcript: jsonl: read at offset 0: … is a directory`;
- on that error `Read` returns a **zero** `Usage`, i.e. `WindowTokens: 0`.

So without the recovery branch the row reports `(0, 0)`, and the `window == defaultWindow` assertion is the **sole red** for the branch the ticket measured at count 0. `read(2)` on a directory fd is `EISDIR` on both Linux and macOS, so the row is portable; no chmod, so it does not silently pass as root.

### `TestSnapshotUsageFor_UnwiredReturnsNilSeam` — loses half (AC #3)

Keeps only `snapshotUsageFor("") == nil`. Update the doc comment: the nil-id half now lives in the wiring test.

### `TestBootstrapSnapshotUsage` — new (AC #3 + AC #4)

Three subtests:

- **no sessions dir ⇒ nil seam** — `bootstrapSnapshotUsage("", <non-nil id source>)` is nil.
- **no id source ⇒ nil seam** — `bootstrapSnapshotUsage(t.TempDir(), nil)` is nil. This is the relocated assertion; its comment should record *why* it survives the move: `bootstrapIDFn` is a func field whose zero value is nil and which `relayWiring` documents as legitimately nil, so this pins a documented optional-field contract. It is not averting a live panic — `relayWiring`'s single producer always sets it, and no test constructs one.
- **wired ⇒ reports the bootstrap session's occupancy** — the discriminating half of AC #4. Seed the temp dir with the bootstrap's transcript **and a sibling's** with a different figure; the id source returns the bootstrap id. Assert the seam is non-nil and reports the bootstrap's figure — *not* zero (which would mean the id never reached the reader) and *not* the sibling's (which would mean the wrong id did).

### AC #4's second half

`internal/e2e.TestRelayV2_StreamRequestSessionSettings` must stay green **unmodified** — run it, do not touch it. Its `used_tokens == 0` assertion is zero against zero: stream-mode fakeclaude binds no sessions dir and writes no transcript, so every id yields 0 there. It is the no-regression half of AC #4, never the discriminating half. **Do not seed a transcript into the e2e daemon** to make it discriminate — that is scope widening the ticket explicitly forbids.

The relay-level tests (`internal/relay`'s `SnapshotUsage` doubles) are insensitive to this change by construction. Their staying green is not evidence either way.

### Gate

`make check`. The touched package is `cmd/pyry`; `go test -race ./cmd/pyry/ ./internal/e2e/` covers both halves of AC #4 directly.

## Open questions

None blocking. One deliberate non-decision: whether a future caller should be able to ask for a session id the pool does not know about. Today the reader answers for **any** stem-valid id whose transcript exists in the wired directory, which is the pre-existing behaviour and is unchanged here. Whether the *caller* should authorise the id before asking is #1609's problem, when a resolved (non-bootstrap) session id first reaches this seam. It is named in the security review below rather than silently deferred.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** MUST-NOT-REGRESS, addressed. This change moves a value across a boundary: the session id goes from daemon-internal (`pool.BootstrapID()`, server-minted) to caller-supplied-by-parameter. The boundary is explicit and singular — `transcript.StatByID` calls `ValidStem`, an **anchored** (`^…$`) canonical-lowercase-UUIDv4 regexp, before any `filepath.Join`, so an invalid stem is rejected with zero filesystem access. The design forbids a second validation layer precisely so this stays the one place the decision is made. Note the boundary has not actually widened *in production*: the sole call site still supplies the bootstrap id. The trust question genuinely opens in #1609, when a resolved session id first reaches the parameter — recorded in Open questions rather than deferred silently.
- **[File operations — path traversal]** No finding. `ValidStem`'s 36-char anchored alphabet admits no `/`, no `.`, and no `..`, so `../../../etc/passwd` and every relative-escape shape are rejected pre-join. This is pinned by an existing table row that survives the edit. The `dir == ""` guard (§ Design) closes the adjacent hazard: a relative join against the daemon's cwd.
- **[File operations — TOCTOU]** SHOULD FIX → already satisfied by the design. The reader is a `Stat`-then-`Open` on a path derived from an id, which is the classic check-then-use shape. Two things make it non-exploitable: the id cannot name a path outside `dir` (above), and the stat result is used **only** for its path — never for a size, mode, or authorisation decision — so a swap during the gap can at worst substitute another file in an already-trusted directory, and the failure collapses to the fresh-session report. AC #2's new row exercises exactly that recovery branch, which is why it is worth adding rather than assuming. No `O_NOFOLLOW` is warranted: the directory is daemon-owned and this is the pre-existing posture, not a new one.
- **[File operations — permissions / atomic writes]** N/A by design decision. This is a read-only path; the change creates and mutates no files. The only writes are test fixtures at `0600` / `0700`.
- **[Error messages, logs, telemetry]** No finding, and it is load-bearing here. The reader takes no logger and returns no error, so neither the caller-supplied id nor the resolved path can reach a log line or an error string — the exact property AC #2 pins. Under this change the id becomes *more* caller-influenced, which makes the no-logger constraint more valuable than before, not less; § Design states it as a hard constraint on the diff. Note `contextwindow.Read`'s error **does** wrap the full path — the recovery branch discards that error rather than propagating it, which is what keeps the path off every surface.
- **[Concurrency]** No finding. No goroutines, no locks, no shared mutable state introduced; the reader's captured state shrinks from `(dir, func)` to `(dir)`. `contextwindow.Read` is documented safe for concurrent use. `Pool.BootstrapID`'s `RLock` is reached at the same points and the same frequency as today.
- **[Tokens, secrets, credentials]** N/A. The session id is a routing key, not a secret — it already crosses the wire in both directions via `BootstrapSessionID`. No credential is read, derived, stored, or compared.
- **[Cryptographic primitives]** N/A. No randomness, no comparison against a secret, no key material on this path.
- **[Subprocess execution]** N/A. Nothing is executed; the id never reaches an `exec.Command` argument.
- **[Network & I/O]** No finding. No socket is read and no request is parsed here. The transcript is read by `contextwindow.Read`, whose scan behaviour and any size limits are unchanged by this ticket.
- **[Threat model alignment]** The relevant threat is cross-session confidentiality — a client being shown another session's context figure. The design strengthens the pin on it: `TestSnapshotUsageFor_SiblingTranscriptIsNeverRead` today only proves no directory scan happens; under AC #1 it proves one reader answers two ids *differently*, which is the property that actually forbids a cross-read. Wire-visible output is two aggregate integers; transcript content never crosses the wire, unchanged from #857.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-08-19
