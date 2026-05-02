# 004 — `fsnotify` for Live `/clear` Rotation Detection

**Status:** Accepted and shipped (Phase 1.2b-B #39)
**Date:** 2026-05-02

## Context

Phase 1.2b-B (#39) needs to detect that claude has rotated its session UUID (a `/clear` event) within ~1 second of it happening, while pyry is running. The detection target is a CREATE on `~/.claude/projects/<encoded-cwd>/<new-uuid>.jsonl`. Three options were on the table:

1. **Poll the dir on a timer.** Strictly worse on both axes the AC cares about: latency (a 1s poll gives 0–1s detection delay; a 250ms poll burns CPU on every supervised process for an event that fires once a session) and CPU (constant `os.ReadDir` syscalls on a dir that mutates rarely).
2. **Raw `inotify` (Linux) + `kqueue` (Darwin).** Native, zero deps, but ~150 lines of duplicated platform code (CGO-free `inotify` via `golang.org/x/sys/unix`; `kqueue` similar). Two stacks to maintain forever, one of which we'd rarely smoke on the dev machine.
3. **`github.com/fsnotify/fsnotify`.** Mature (v1.10.0), BSD-3-licensed, ~5k LOC, used by helm/k8s/etcd/many others. One API across Linux + macOS + (Windows, BSDs — irrelevant to us). The transitive dependency is `golang.org/x/sys`, which `creack/pty` and `golang.org/x/term` already pull in.

Pyry's working principle is "stdlib over dependencies, add external deps only when they provide significant value (like `creack/pty`)." The bar is therefore: does fsnotify clear that bar, or do we duplicate the platform code ourselves?

## Decision

**Adopt `github.com/fsnotify/fsnotify` v1.10.0 as the second non-stdlib runtime dependency** (after `creack/pty`). The watcher in `internal/sessions/rotation/watcher.go` uses it to watch `cfg.ClaudeSessionsDir` for CREATE events.

## Rationale

- **Latency requirement is hard.** AC mandates ~1 second. Polling can hit it but only at a CPU cost that's wasteful for an event that fires once per `/clear`. fsnotify is push-based and effectively zero-cost when idle.
- **Cross-platform code we don't want to own.** The `inotify`/`kqueue` divergence is exactly the kind of platform code where a well-maintained library beats hand-rolled. Bugs in `kqueue` event coalescing or `inotify` watch-fd exhaustion would surface on user machines, not our dev boxes.
- **Dependency surface is minimal.** fsnotify pulls in `golang.org/x/sys` only (verified in `go.sum`), which is already transitive. No `cgo`, no init-time global state.
- **Trust signal.** k8s, helm, etcd, prometheus, hashicorp/vault, and others use it in production. The license (BSD-3) is compatible with pyry's MIT.

## Consequences

- **Two external runtime deps now** (`creack/pty`, `fsnotify`) plus three `golang.org/x` extensions (`sys`, `term`, `sync`). The bar for adding the next one stays high — re-justify against the same alternatives.
- **`golang.org/x/sync/errgroup`** lands as part of the same ticket for the bootstrap+watcher fan-out in `Pool.Run`. It's a semi-official extension (effectively stdlib), used here because Phase 1.1's N-session fan-out will reuse the same pattern.
- **Failure-mode contract.** If fsnotify's `NewWatcher` or `Add(dir)` fails, pyry logs a warning and proceeds without rotation detection. The watcher is never load-bearing for startup; missing the rotation just means lazy respawn (1.2c) sees a stale UUID until the next `pyry` restart triggers the startup-side reconciler.
- **Linux watch-fd cap.** `inotify` watches consume an fd from `fs.inotify.max_user_watches`. Pyry uses one watch (the per-workdir claude dir), well under any reasonable cap. Phase 1.1's N-session fan-out will need one watch per distinct workdir, still negligible.

## References

- Ticket: [#39](https://github.com/pyrycode/pyrycode/issues/39)
- Feature doc: [`features/rotation-watcher.md`](../features/rotation-watcher.md)
- Companion startup half: [`features/jsonl-reconciliation.md`](../features/jsonl-reconciliation.md) (#38)
- fsnotify upstream: <https://github.com/fsnotify/fsnotify>
