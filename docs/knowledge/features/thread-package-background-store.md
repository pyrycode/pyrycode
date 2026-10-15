# `internal/thread` — background store

## Background store

`Store` owns history replay, tailing and immutable publications for loaded
conversations. The sections below describe readiness, queries, lifecycle and
verification; [the package overview](thread-package.md) covers daemon wiring
and recovery certificates.

| Topic | Contents |
| --- | --- |
| [Replay and publications](thread-package-background-store-replay.md) | Loading and replay readiness, snapshots, source resolution and fresh retry. |
| [Bounded queries](thread-package-background-store-queries.md) | History pages, newest windows, catch-up coverage and immutable query indexes. |
| [Isolation and lifecycle](thread-package-background-store-lifecycle.md) | Worker ownership, retained changes, joined unload/shutdown and producer draining. |
| [Store verification](thread-package-background-store-verification.md) | Source failure/retry fixtures, measured fixture costs, publication/query proofs and lifecycle checks. |
