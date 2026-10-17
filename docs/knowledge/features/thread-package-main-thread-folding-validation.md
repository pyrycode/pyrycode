# `internal/thread` — validation and retention

[Main-thread folding overview](thread-package-main-thread-folding.md).

### Validation and testing

| Document | Topics |
| --- | --- |
| [Checkpoint validation and resume proof](thread-package-main-thread-folding-validation-checkpoints.md#checkpoint-validation-and-resume-proof) | Export/import identity and ownership, persisted read/write round trips, interruption/retry faults and complete suffix proofs. |
| [Continuation retention and accounting](thread-package-main-thread-folding-validation-retention.md#continuation-retention-and-accounting) | Finished main/agent/child markers, reversible snapshots and logical/encoded byte measurements. |
| [Replay, rendering and ingestion validation](thread-package-main-thread-folding-validation-replay.md#replay-rendering-and-ingestion-validation) | Recorded-field validation, source failures, heap boundaries, text ownership and ingestion scaling. |
