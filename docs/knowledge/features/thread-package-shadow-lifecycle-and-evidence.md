# `internal/thread` — shadow lifecycle and evidence

[Thread package overview](thread-package.md).

## Shadow lifecycle and evidence

| Document | Topics |
| --- | --- |
| [Source retirement](thread-package-shadow-lifecycle-and-evidence-retirement.md) | Retained source/publication boundaries, lifecycle regressions, counted Linux acceptance, [integrated recovery](v2-session-manager-state-machine-reconnect-replay-hello-last-event-id-rin.md#integrated-acceptance) and source/live ordering. |
| [Daemon publication](thread-package-shadow-lifecycle-and-evidence-publication.md) | Discovery, provider wiring, live/query ordering, continuity retirement and bounded coalescing. |
| [Coordination and evidence](thread-package-shadow-lifecycle-and-evidence-coordination.md) | Idle unload, query demand, summary recovery, shutdown and retained capture evidence. |

### Reusable fold regression corpus

[Generated, shadow and recorded corpus proofs](thread-package-reusable-fold-regression-corpus.md#reusable-fold-regression-corpus).

### Full-feed benchmark

`BenchmarkFoldCorpus30000` measures fresh fold construction and complete
`Fold.Feed` for 30,000 generated entries per iteration. Generation, snapshots
and digests are outside measured time/allocation. Use a single measured iteration:

```bash
go test ./internal/thread -run '^$' -bench '^BenchmarkFoldCorpus30000$' -benchtime=1x -count=1 -timeout 12m
```

The [retained pre-optimization baseline](https://github.com/pyrycode/pyrycode/pull/3374)
measured **19.271536560 s**, **4,499,987,800 B/op** and
**14,158,715 allocs/op**, producing **4,200 items from 30,000 entries**.
It used Go **1.26.9** in a **Linux/amd64 container**, an **Intel Core i7-3770
@ 3.40 GHz** and **8 logical CPUs**. Production source was
`a920b90cc6bfd2f1fb8461e9f85ce6f19934afa6` (including the agent-only parent,
accepted-send handle and targeted child-repair fixes); the committed test
harness was `92c58fe5a596c5ed056c8216caf2d7baa8412565`. This was one measured
iteration with other builder checks running concurrently on the same machine;
retain that load qualification when comparing later measurements.
