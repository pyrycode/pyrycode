# 1530 — `internal/devices` cross-process locked read-modify-write

## Files to read first

Symbols, not line numbers — resolve each with `codegraph_search` / `codegraph_node`.

- `internal/devices/registry.go` → `Save` — the atomic-write recipe this helper's sibling-lock choice exists to survive: `os.CreateTemp` in `filepath.Dir(path)` → `Chmod 0600` → encode → `Sync` → `Close` → `os.Rename`, plus `os.MkdirAll(dir, 0o700)`. The rename replaces the target's **inode**, which is exactly why the lock must not live on `devices.json`. The `MkdirAll(…, 0o700)` line is also the mode this helper copies for its own parent-directory creation.
- `internal/devices/registry.go` → `Reload` and `reconcileDevices` — disk is authoritative for membership. Extract: why a reload-merge cannot be folded into `Save` (it would make `pyry pair revoke` resurrect the record it just removed), and hence why the entry point serializes a **caller-supplied** mutation.
- `internal/devices/registry.go` → `readDevicesFile` — the `SECURITY:` doc-comment block stating the error wraps `path` only, never the file bytes. Every new error surface in this ticket inherits that rule verbatim.
- `internal/devices/registry_test.go` → `writeDevicesFile`, `mustParseTime` — same-package fixture helpers; reuse them, do not write new ones.
- `internal/devices/registry_test.go` → `TestRegistry_SaveFilePermissions` — the in-repo precedent for asserting an exact `Mode().Perm()` value on a created file. AC 5's mode assertions follow this shape (and inherit its umask assumption).
- `internal/devices/registry_test.go` → `TestRegistry_SaveAtomicRenamePreservesOldFile` — the existing rename-shaped test; AC 3's test performs a real `Save` inside the critical section, so mirror how this one stages a registry on disk.
- `internal/devices/registry_test.go` → `TestReload_ConcurrentReloadValidate` — the package's existing two-goroutine concurrency-test idiom (`sync.WaitGroup`, `t.TempDir()`); the new lock tests match it.
- `internal/conversations/registry.go` → `Update` — ADR 022's callback-under-lock shape. Extract the **doc-comment discipline**: it spells out what `fn` must not do while the lock is held. Mirror that tone. Do not mirror the mechanism — that lock is a `sync.Mutex`, this one is a file lock.
- `cmd/pyry/pair.go` → `runPairDefault`, `runPairRevoke`, `resolveDevicesPath` — the two CLI writers. **Read for shape only; this slice rewires nothing.** Note that `runPairDefault` does `identity.LoadOrCreate` + `keys.LoadOrCreate` + a CSPRNG read between its `devices.Load` and its `Save`; that is the wide side of the race the consumer slice will close.
- `internal/relay/handlers/register_push_token.go` → `RegisterPushToken` — the daemon writer. It calls `Reload` then `Save` on a **long-lived** `*devices.Registry` that already carries un-persisted `LastSeenAt` and push state. This is the single most load-bearing constraint on the API shape below: the entry point must **not** `Load` a fresh registry internally, or the daemon's live state is dropped on the floor.
- `docs/specs/architecture/341-agentrun-trust-helper.md` § "Lock strategy" — the fully-worked design for this mechanism: sibling lock file, `os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)`, never delete the lock file, `defer` unlock-then-close, kernel-releases-on-exit crash safety. Reuse the reasoning. **Do not copy its blocking `LOCK_EX`** — see § "Divergence from #341".
- `internal/agentrun/trust/trust.go` → package doc comment (`Best-effort: no file lock`) — confirms #341's spec shipped without its lock, so there is no in-repo flock precedent to copy. This slice writes the first one.
- `docs/knowledge/decisions/029-devices-registry-reload-at-handshake.md` § "Alternatives considered" and § "Consequences" — the rejected-flock rationale and the "the next reload reconciles it" claim that this ticket overturns. Read for context; **the documentation phase corrects the ADR, not the developer.**

## Context

`~/.pyry/<name>/devices.json` has three writer paths across two binaries that run as separate OS processes: `runPairDefault` (`Load` → `Add` → `Save`), `runPairRevoke` (`Load` → `Remove` → `Save`), and `RegisterPushToken` (`Reload` → `Save`). `Registry.Save` rewrites the whole file via temp-file + `os.Rename`, so the loser of any interleaving is silently erased. `Registry.mu` serializes goroutines inside one process and does nothing across processes; a `syscall.Flock` sweep over the repo returns zero hits against a working positive control (15 `os.Rename` hits).

The daemon narrows its own window to the microseconds between `Reload` and `Save` (#782 / ADR 029). Nothing narrows the `pyry pair` side, whose window spans `identity.LoadOrCreate`, `keys.LoadOrCreate` (which mints and persists a keypair on cold start), and a CSPRNG read. ADR 029 claims the loss self-heals — *"A `Save` can still race a `pyry pair` add; the next reload reconciles it."* It does not: `Reload` reconciles memory to disk with **disk authoritative for membership**, so once a daemon `Save` has written a snapshot omitting the new device, the record is gone from disk and every later reload reads the file that no longer contains it.

**This slice ships the primitive only.** No caller is rewired. The cross-process race stays open until the two consumer slices land. That is intentional; nothing below should be read as closing it.

### What the open race actually costs — a note for the consumer slices

The race is usually described as losing an *add*. It also loses a *revoke*, and that direction is the security-relevant one. Ordering, entirely within the daemon's own narrow window: `RegisterPushToken` calls `Reload` and reads `[X]` from disk → `runPairRevoke` writes `[]` → the daemon's `Save` writes `[X]` back. Disk once again lists the revoked device, and the daemon's in-memory set never dropped it, so **X keeps authenticating**. Revocation is silently undone by a concurrent push-token registration.

That does not change this slice's design — the primitive is the same either way — but it does set the priority and the proof obligation for the daemon consumer slice, whose end-to-end test should cover the revoke direction and not only the add direction.

## Design

### Package boundary

No new package. One new file, `internal/devices/lock.go`, plus `internal/devices/lock_test.go`. Stdlib only: `errors`, `fmt`, `os`, `path/filepath`, `syscall`, `time`. `golang.org/x/sys` stays `// indirect` — `syscall.Flock` and `syscall.LOCK_EX` compile on `darwin/{amd64,arm64}` and `linux/{amd64,arm64}` and fail only on Windows, which `CLAUDE.md` puts out of scope. **No build tag.**

### Public API

Three exported symbols. No new types.

```go
// DefaultLockWait is the bounded acquisition wait for callers with no reason
// to choose their own.
const DefaultLockWait = 5 * time.Second

// ErrLockBusy reports that the lock was not acquired within the caller's wait.
var ErrLockBusy = errors.New("devices: lock busy")

// WithLock runs fn while holding an exclusive lock, derived from devicesPath,
// that excludes other OS processes.
func WithLock(devicesPath string, wait time.Duration, fn func() error) error
```

**Why `func() error` and not `func(*Registry) error`.** A callback taking a freshly-`Load`ed registry would read well for the two CLI writers and would be wrong for the daemon: `RegisterPushToken` mutates a long-lived registry that holds `LastSeenAt` bumps and in-flight push registration which are never on disk, then reconciles disk into *that* registry. Handing it a fresh one discards state the daemon is the sole owner of. The bare-thunk shape serves all three writers because each already knows which read-modify-write it wants — `Load`/`Add`/`Save`, `Load`/`Remove`/`Save`, or `Reload`/`Save`. This is the ticket's "serialize a caller-supplied mutation rather than hard-code one," and it is the same reason a reload-merge cannot be folded into `Save`.

**Why an explicit `wait` rather than a package constant or a `context.Context`.** AC 4 needs a bounded wait that a test can drive at millisecond scale; a hard-coded `DefaultLockWait` would make each contention test cost seconds. `context.Context` was considered and rejected: `runPairDefault` and `runPairRevoke` have no ambient context and would have to manufacture one purely to feed this call. If a future caller needs cancellation rather than a deadline, add a context-taking sibling; do not change this signature.

`ErrLockBusy` exists so the daemon consumer slice can `errors.Is` and map to a wire code at the handler, per PROJECT-MEMORY's "refusal-to-wire-code mapping is the consumer's job, not the primitive's." The primitive returns the Go sentinel and nothing else.

### `WithLock` — behaviour contract

Sequence, each step one or two lines:

1. `lockPath := devicesPath + ".lock"`.
2. `os.MkdirAll(filepath.Dir(lockPath), 0o700)` — mirrors `Save`'s mkdir. Required, not defensive: AC 1 puts acquisition *before* the on-disk read, and on a first-ever `pyry pair` the instance directory does not yet exist at that moment (it is created later by `identity.LoadOrCreate` and by `Save`'s own mkdir). Without this the very first pair fails `ENOENT`.
3. `os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)`. Register `defer f.Close()` immediately, so it covers the failed-acquire path too.
4. Acquire: `syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)` in a retry loop against `deadline := time.Now().Add(wait)`. The first attempt is unconditional, so an uncontended acquire costs no sleep and `wait <= 0` still gets one try. On `EWOULDBLOCK`, sleep `lockPollInterval` and retry while the deadline has not passed. Any other errno is terminal and returns wrapped.
5. Deadline exhausted → return `fmt.Errorf("devices: acquire lock %s: %w", lockPath, ErrLockBusy)`. `fn` is never invoked.
6. Acquired → register `defer syscall.Flock(fd, syscall.LOCK_UN)`. Defers are LIFO, so unlock runs before close; close alone would release the lock, but the explicit unlock documents the intent.
7. Run `fn()` and **return its error unchanged**. Callers wrap at their own call site (`fmt.Errorf("pair: %w", err)`), and an unchanged return keeps their `errors.Is` working against their own sentinels.

`lockPollInterval` is an unexported `const` of `10 * time.Millisecond`. Nothing in the design is sensitive to the exact value; it is small enough that a millisecond-scale test wait gets several attempts and coarse enough that a 5-second wait is a few hundred cheap syscalls.

**`EWOULDBLOCK` detection.** `syscall.Flock` returns a `syscall.Errno`; test it with `errors.Is(err, syscall.EWOULDBLOCK)`. On both darwin and linux `EWOULDBLOCK == EAGAIN` (same numeric value), so one check covers both spellings — do not write two.

### Lock placement — the sidecar is mandatory

The lock lives on `devices.json.lock`, a sibling file that no code path ever renames over, and **never** on `devices.json` itself.

`flock(2)` locks attach to the open file description, i.e. to the inode. `Save` commits by renaming a temp file over the target, which installs a **new inode** at that path. A second acquirer opening the path after that rename gets the new inode and acquires its own independent lock while the first holder still believes it is excluding everyone. Mutual exclusion is broken by the exact operation the lock exists to protect, and a naive "flock the registry file" implementation looks correct and is vacuously wrong precisely in the scenario this ticket is about. AC 3's test pins this: it performs a real `Save` inside the critical section and asserts a concurrent acquirer is *still* blocked afterwards.

The lock file is created on demand and **never deleted** — deletion races acquisition (a holder can be unlinked out from under a waiter, after which both proceed). A leftover zero-byte `devices.json.lock` is the intended steady state, not litter.

**The lock fd is never written to.** The file's only role is to own a stable inode; its contents are always zero bytes. The doc comment must say so, because the obvious future "improvement" — stashing the holder's PID or a timestamp for debugging — is wrong twice over: it races (a waiter that has just acquired would truncate under a reader) and it turns a content-free file into an information surface next to a credential store. `O_RDWR` is present only because that is the conventional flock open mode; nothing in this design needs write access.

Note `filepath.Dir(lockPath) == filepath.Dir(devicesPath)` — the sidecar sits in the instance directory, so step 2's mkdir targets exactly the directory `Save` already creates, with the same mode. The helper's filesystem reach is byte-identical to `Save`'s.

Deriving the path from `devicesPath` also gives AC 5's isolation for free: `~/.pyry/a/devices.json.lock` and `~/.pyry/b/devices.json.lock` are different files, so two `-pyry-name` instances never contend.

### Exactly one layer acquires

`WithLock` is the **sole** acquirer. `Load`, `Save`, and `Reload` do not acquire and are not changed by this ticket. This must be stated in `WithLock`'s doc comment, because the alternative — `Save` acquiring too — is subtly broken: a nested acquisition opens a *second* file description, which genuinely contends with the outer one (flock is per-description, not per-process), so the inner call would block against its own caller. With the bounded wait it does not hang forever the way a blocking `LOCK_EX` would; it burns `wait` and returns `ErrLockBusy`. That is a better failure mode, not a licence to nest.

### Divergence from #341

#341's spec chose a **blocking** `syscall.Flock(fd, LOCK_EX)` on the grounds that *"pyrycode has no nonblocking-acquire requirement."* This slice has one. AC 4 requires a bounded wait because `RegisterPushToken` runs on the daemon's request path and must not wedge forever behind a stuck `pyry pair` process holding the lock. Hence `LOCK_EX|LOCK_NB` against a deadline. Every other element of #341's lock design — sibling file, `O_CREATE|O_RDWR` at `0600`, never delete, defer unlock-then-close, kernel-releases-on-exit — is adopted unchanged.

## Concurrency model

No goroutines spawned. `WithLock` is sequential within one invocation: mkdir → open → acquire → `fn` → unlock → close.

Two axes of exclusion, both by `flock(2)`:

- **Across processes** — the reason this ticket exists. `pyry pair` and the daemon serialize on the shared sidecar inode.
- **Within one process** — each `WithLock` call opens its own file description, and flock contends per description, so two goroutines in one process genuinely exclude each other. This is what makes the same-process tests below a real proof rather than a simulation, and it is specific to `flock(2)`: `fcntl` byte-range locks are per-process and would *not* contend, so the mechanism choice and the test shape are coupled. Do not switch to `fcntl`.

Lock ordering: `WithLock` takes exactly one lock and never nests. `fn` may take `Registry.mu` (every `Registry` method does), so the order is always file-lock-then-mutex, never the reverse. No `Registry` method acquires the file lock, so the reverse order is unreachable by construction.

Crash safety: the kernel releases a flock when the fd closes, including on process exit or `SIGKILL`. There is no stale-lock cleanup path and no lock-file TTL. A process killed mid-region leaves `devices.json` either pre- or post-rename, never partial, because `Save` is already atomic.

No `context.Context`. The deadline is the cancellation story.

## Error handling

Four terminal classes, all returned wrapped, none of which run `fn`:

1. **mkdir failure** — `fmt.Errorf("devices: mkdir %s: %w", dir, err)`.
2. **open failure** — `fmt.Errorf("devices: open lock %s: %w", lockPath, err)`.
3. **non-`EWOULDBLOCK` flock errno** — `fmt.Errorf("devices: flock %s: %w", lockPath, err)`. Terminal; no retry.
4. **wait exhausted** — `fmt.Errorf("devices: acquire lock %s: %w", lockPath, ErrLockBusy)`.

A fifth, non-terminal-for-the-lock class: `fn` returned an error. The lock is still released; `fn`'s error is returned unchanged.

No retries beyond the acquisition poll loop. No logging inside the helper — `internal/devices` has no logger and gains none here; consumers log at their call sites.

**SECURITY — inherited from `readDevicesFile`.** Every error surface above names the **lock path** and nothing else. It must never carry `devices.json`'s bytes, nor any decoded device field. This helper never reads or decodes the registry, so the leak is structurally out of reach — but the rule is stated in the doc comment anyway, because the consumer slices will add error paths that *do* sit next to decoded data.

## Testing strategy

`internal/devices/lock_test.go`, same package (`package devices`), stdlib `testing`, no testify, table-driven where rows help. Every test uses `t.TempDir()`; the real `~/.pyry` is never touched. All tests must pass under `-race`.

**Recorder discipline (applies to every test that records ordering).** Guard the recording slice with its own `sync.Mutex`. Without the file lock, both goroutines append concurrently, and an unguarded slice makes the mutation check below fail as a `-race` data-race report instead of as the assertion failure it is meant to be. Guard it, and the mutant fails cleanly on the assertion.

### AC 2 — mutual exclusion, non-interleaving

- **two acquirers do not interleave** — two goroutines, each calling `WithLock` on the same devices path with an ample wait. Each callback records `<id>:enter`, sleeps ~30ms, records `<id>:exit`. After both return, assert the recorded sequence is one region fully closed before the other opens — i.e. entries 1 and 2 share an id, entries 3 and 4 share the other id. Either winner is acceptable; interleaving is not.
- **mutation check (required by the AC).** Deleting the `syscall.Flock` acquisition must redden the test above. This is deterministic, not probabilistic: the 30ms in-region sleep guarantees both goroutines are inside simultaneously when the lock is gone. Verify with `go test -overlay=<abs-path json>` so no worktree write is needed, and record the result in the ticket's knowledge doc.

### AC 3 — a real `Save` inside the region does not release the lock

- **rename inside the critical section still excludes** — goroutine A enters `WithLock` on `devicesPath`, and inside the region calls `(*Registry).Save(devicesPath)` on a registry staged with one device (this is the temp-file + rename that replaces the target inode). A then signals *renamed* and blocks on a *checked* channel. The main goroutine, on *renamed*, calls `WithLock(devicesPath, ~50ms, …)` and asserts it returns an error satisfying `errors.Is(err, ErrLockBusy)` and that its `fn` never ran. Then it signals *checked*, A returns, and the test asserts a subsequent acquire now succeeds.
- This is the test that discriminates the sidecar design from the naive one: implement the lock on `devices.json` itself and the middle acquire **succeeds**, turning the assertion red.

### AC 4 — bounded wait, no write, file byte-unchanged

- **timed-out acquirer errors and writes nothing** — pre-stage a real `devices.json` via `writeDevicesFile` and capture its bytes with `os.ReadFile`. Goroutine A holds the lock, parked on a channel the test controls. The test calls `WithLock(devicesPath, ~50ms, …)` with a callback that flips a `bool` (declared, never expected to flip) and returns nil. Assert: the returned error satisfies `errors.Is(err, ErrLockBusy)`; the error string contains the lock path; the flag is still false; and re-reading `devices.json` gives bytes equal to the capture. Then release A.
- **the wait is bounded, not infinite** — the same shape, asserting the call returned within a generous multiple of the requested wait (e.g. under 2s for a 50ms wait). This is the AC's "does not block forever on a peer that never releases"; keep the assertion loose enough not to flake on a loaded CI runner.

### AC 5 — path derivation, cold start, modes

- **missing parent directory succeeds** — devices path under a `t.TempDir()` subdirectory that is deliberately **not** created. `WithLock` must succeed. Assert the parent directory now exists with `Mode().Perm() == 0o700` and the lock file exists with `Mode().Perm() == 0o600`. (Mode assertions assume the developer's/CI umask does not strip these bits — the same assumption `TestRegistry_SaveFilePermissions` already makes for `0600`.)
- **distinct instances do not contend** — two devices paths in two different temp directories, standing in for two `-pyry-name` instances. Hold the lock on the first; assert an acquire on the second succeeds within a short wait rather than timing out.
- **lock file is a sibling, not the registry** — after a successful `WithLock` on a path with no `devices.json` present, assert `devices.json.lock` exists and `devices.json` does **not**. Acquiring the lock must not conjure a registry file.

### AC 1 — the region spans the whole read-modify-write

- **callback observes the lock held for its whole body** — largely covered by AC 2 and AC 3 above; add one row where `fn` performs a full `Load` → `Add` → `Save` against the locked path and returns nil, asserting the resulting `devices.json` contains the added device. This pins that the entry point composes with the real read-modify-write rather than merely with a sleep.
- **`fn`'s error propagates unchanged and the lock still releases** — `fn` returns a sentinel the test declares; assert `errors.Is(err, thatSentinel)` (unwrapped, verbatim), and that a subsequent `WithLock` on the same path acquires immediately.

### What NOT to test in #1530

- Cross-process contention via a subprocess harness. Same-process contention is a genuine proof because flock is per open file description, which the ticket measured directly. A subprocess harness would add cost and prove nothing extra.
- Any behaviour of `runPairDefault`, `runPairRevoke`, or `RegisterPushToken`. This slice rewires no caller; the end-to-end proof that the race is closed belongs to the daemon consumer slice.
- Lock-file cleanup. The file is intentionally permanent; asserting it lingers tests the design, not the behaviour.
- `Load`/`Save`/`Reload` semantics. Unchanged by this ticket and already covered.

## Open questions

- **`EINTR` during a non-blocking flock.** The design treats every non-`EWOULDBLOCK` errno as terminal. A signal arriving mid-syscall could in principle surface `EINTR` and fail an acquire that would have succeeded on retry. Not observed anywhere in this repo, and Go installs most handlers with `SA_RESTART`, so this spec does **not** mandate a retry — per the pipeline's evidence-based-fix rule. If it is ever seen in the wild, the fix is to fold `EINTR` into the same retry branch as `EWOULDBLOCK`.
- **`flock` over NFS.** Linux ≥ 2.6.37 emulates `flock` via `fcntl` on NFS; older kernels make it node-local, and macOS behaviour varies by mount. `~/.pyry` on a network filesystem is exotic and not a supported configuration. Out of scope; note it rather than defend against it.
- **Whether `DefaultLockWait` at 5s is right for the daemon.** The CLI can afford to wait; `RegisterPushToken` runs on a request path and may want a tighter bound. The daemon consumer slice owns that call, so it picks its own value there — that is precisely why `wait` is a parameter. No decision needed here.
- **ADR 029's correction.** Its rejected-alternative rationale and its *"the next reload reconciles it"* consequence are both wrong, as is `docs/knowledge/features/devices-registry.md` § *Two-writer clobber guard*, which restates the same claim. Correcting them is the **documentation phase's** job, not the developer's. Do not add a docs AC to this ticket.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No findings. The helper decodes nothing and holds no registry data — it opens a zero-byte sidecar and runs a caller-supplied thunk, so there is no untrusted→trusted transition inside it. Its one input, `devicesPath`, is operator-supplied (`-pyry-name` → `sanitizeName` → `resolveDevicesPath`), and because `filepath.Dir(devicesPath+".lock") == filepath.Dir(devicesPath)`, the helper's filesystem reach is byte-identical to the `Save` that already writes there. One genuine behaviour change to hand to the consumer slice: `WithLock`'s mkdir runs *earlier* than `Save`'s, so a `pyry pair revoke <name>` against a never-paired instance — which today exits 1 without creating anything, since `Load` returns ENOENT-as-empty and `Remove` misses before `Save` is reached — would leave behind an empty instance directory and a lock file. Cosmetic, not a boundary violation, but it is a real diff in observable behaviour.
- **[Tokens, secrets, credentials]** SHOULD FIX → **addressed inline** (§ "Lock placement"). The helper never opens, reads, or decodes `devices.json`, so no `token_hash` is reachable from any code path it owns. The residual risk is forward-looking and concrete: a later edit that stashes the holder's PID or a timestamp in the lock file for debugging would both race (a waiter that has just acquired truncates under a concurrent reader) and place an information surface immediately beside a credential store. The spec now requires the doc comment to state that the fd is never written and that `O_RDWR` is conventional rather than needed.
- **[File operations]**
  - *Path traversal* — **OUT OF SCOPE**, pre-existing, follow-up ticket warranted. `sanitizeName` permits `.`, so `-pyry-name=..` survives sanitization intact and `resolveDevicesPath` yields `~/.pyry/../devices.json` = `~/devices.json`. Multi-level traversal is blocked (`/` maps to `_`, so `../../etc` becomes `.._.._etc`), leaving exactly a one-level escape. This ticket does not widen it: `Save` already writes and `MkdirAll`s that same resolved path today, so `WithLock`'s reach is unchanged. The input is an operator's own flag, not attacker-controlled — someone who can pass `-pyry-name` already has the shell. Recommend a separate ticket to reject `.` and `..` as whole components in `sanitizeName`; do **not** fold it into #1530.
  - *Symlinks* — `os.OpenFile(lockPath, O_CREATE|O_RDWR, …)` follows symlinks. A planted `devices.json.lock → elsewhere` would misdirect the lock to a foreign inode, degrading exclusion to the status quo ante (no lock) rather than corrupting anything, because the fd is never written. Cross-uid planting is out of reach: the instance directory is created at `0700` here and by `Save`. Same-uid is trusted under the Unix model, consistently with every other file this project owns. `O_NOFOLLOW` is not warranted.
  - *Permissions* — explicit and stated: dir `0700`, lock file `0600`. Umask can only clear bits, never set them, so a permissive umask cannot loosen either — the only consequence is a test-assertion assumption, and it is the same one `TestRegistry_SaveFilePermissions` already makes. `os.MkdirAll` does not re-chmod an existing directory, so a pre-existing `0755` instance dir stays `0755`; identical to `Save`'s behaviour today, not a regression introduced here.
  - *TOCTOU* — this is the ticket. The lock is acquired before the on-disk read and released after the write commits, and the sidecar placement is the specific countermeasure for the inode-swap-via-rename hole that would silently void a lock held on `devices.json` itself. AC 3's test is the assertion that pins it.
  - *Atomic writes* — N/A. The helper writes zero bytes; the atomicity that matters belongs to `Save`, unchanged.
- **[Subprocess / external command execution]** N/A — no `exec`, no shell, no environment read or scrub. The same-process test shape (justified by flock being per open file description) is what keeps a subprocess harness out of this ticket entirely.
- **[Cryptographic primitives]** N/A in this slice — no RNG, no hashing, no comparison of secrets. One forward-looking note for the consumer slice: AC 1 places acquisition before `devices.Load` in `runPairDefault`, so the critical section will span `identity.LoadOrCreate`, `keys.LoadOrCreate` (which mints and persists a keypair on cold start), and the CSPRNG read. X25519 keygen is sub-millisecond and the surrounding file writes dominate; the hold time stays orders of magnitude under `DefaultLockWait`. Named so the daemon slice picks its `wait` with the CLI's worst-case hold in view, not to gate this spec.
- **[Network & I/O]** No network, no sockets, no parsing, so no input caps or timeouts apply. Resource use is bounded by construction: the poll loop is `wait / lockPollInterval` cheap syscalls (≈500 for the 5s default) and the fd count is one per in-flight call. The one consequence worth naming: `RegisterPushToken` runs per-connection, so a stuck CLI holder parks a handler goroutine for up to `wait` per connection. That bound is the caller's to choose — precisely why `wait` is a parameter and `ErrLockBusy` is a sentinel the handler can map — and the daemon slice should choose sub-second rather than inherit `DefaultLockWait`.
- **[Error messages, logs, telemetry]** No findings. All four error classes name the **lock path** and nothing else; the helper has no logger and gains none. The path embeds only the operator's own instance name, which is not a secret, and this matches `readDevicesFile`'s existing `path`-only wrap — the rule the ticket's SECURITY note requires every new surface to inherit. Because the helper never decodes the registry, the `json.Unmarshal`-echoes-a-`token_hash` leak is structurally unreachable here rather than merely avoided by discipline. The doc comment states the rule anyway, since the consumer slices add error paths that *do* sit beside decoded data.
- **[Concurrency]** No findings. One lock, never nested. Ordering is always file-lock-then-`Registry.mu` and the reverse is unreachable by construction, because no `Registry` method acquires the file lock — a property the "exactly one layer acquires" decision exists to preserve. A mistaken nested `WithLock` degrades to a bounded `ErrLockBusy` after `wait`, which is strictly better than the indefinite hang #341's blocking `LOCK_EX` would have produced. Crash or `SIGKILL` mid-region: the kernel releases the flock on fd close, there is no stale-lock cleanup path to get wrong, and `devices.json` is left pre- or post-rename because `Save` is already atomic. No goroutines are spawned, so there is no lifecycle to leak.
- **[Threat model alignment]** The threat here is two cooperating same-uid processes, not an adversary — cross-uid is already handled by the `0700` instance directory. The security-relevant framing, now recorded in § "What the open race actually costs", is that the unclosed race loses a **revoke** and not merely an add: daemon `Reload` reads `[X]` → `runPairRevoke` writes `[]` → daemon `Save` writes `[X]` back, while the daemon's in-memory set never dropped X, so a revoked device keeps authenticating. This slice neither introduces nor closes that hole; it is named here so the daemon consumer slice carries it as a proof obligation covering the revoke direction, not only the add direction.

**MUST FIX:** none.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-08-19
