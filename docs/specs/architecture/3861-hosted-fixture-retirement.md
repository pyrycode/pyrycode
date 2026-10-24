# Hosted fixture retirement

## Files read

- `internal/e2e/hosted_apps_runtime_test.go`: `TestHostedAppProcess`, `hostedNodeEnv`, `hostedGone`; the fake service and its sleep descendant have no fixture-owned fallback.
- `internal/e2e/harness.go`: `spawnWith`, `reapSpawnedGroup`, `Harness.teardown`; forced daemon exit only retires its own group.
- `internal/e2e/harness_test.go`: `TestHarness_ReapsOwnedChildren`; existing unrelated-fixture isolation proof.
- `internal/e2e/hosted_apps_test.go`: seeded committed releases and lifecycle assertions remain in force.
- `internal/apps/owner.go`: `Owner.launch`, `Owner.cleanup`, `Owner.Shutdown`; hosted generations use separate process groups and normal shutdown joins them.
- `internal/apps/publication_process.go`: publication generations also use separate groups; no demonstrated publication defect.
- `scripts/test-admission.py`, `scripts/test_test_admission.py`: inherited descriptors retain serialization; the outer admission group cannot collect separate service groups.
- `docs/knowledge/features/e2e-harness.md` § Teardown Sequence: daemon group cleanup does not cover hosted groups.
- `docs/knowledge/features/apps-package.md`, `apps-package-lifecycle.md`: normal owner shutdown must retain joined generation retirement.

## Context

One invariant: fixture completion retires all its fake hosted generations, even after daemon shutdown is forcibly interrupted. The historical #3854 receipt is referenced by the issue but unavailable at its stated local path; it proves abandoned processes and lock retention, not the initiating branch. Reproduce forced fixture teardown without claiming it explains that historical branch. No product ownership change or decision record is warranted.

## Design

Give `hostedNodeEnv` a private Unix-socket lifetime registry, embedded in its shim's argv rather than the product's private service environment. Each fake helper registers its own PID and sleep PID before publishing readiness. The fixture captures process handles while the helper waits for acknowledgement. Keep connections and handles for all generations, including replaced services. After daemon cleanup, stop accepting, join the accept loop, and retire registered helpers and descendants. Linux process handles pin identities through pidfds; use those handles rather than numeric group signals. Confirm retirement before fixture files disappear. Keep the helper's sleep cleanup deferred on every return path.

The controlled Linux regression runs a nested owner through normal admission with an isolated lock. It deliberately stops the daemon so fixture cleanup reaches its existing SIGKILL escalation, then ends normally or through `t.Fatal`. A retained process receipt identifies the daemon, generation, child, descendant, groups and lock inode. Explicitly pass the isolated held descriptor into the helper and sleep to model the observed retention, independently of normal Go descriptor filtering. An emergency cleanup owned by the outer regression prevents its red baseline from poisoning later checks. A fresh admission command has no inherited held FD and must acquire the same inode. Outer host admission remains untouched.

## Concurrency model

One fixture accept goroutine records helpers under a mutex and is joined after listener closure. Registration completes before service readiness. Fixture cleanup runs after harness cleanup and before fixture directories disappear. Child cleanup kills and waits its direct sleep child. No product goroutines change.

## State transitions and identity reuse

| Event | Race test |
| --- | --- |
| Graceful owner stop and fixture fallback | Existing hosted lifecycle tests |
| Daemon frozen, then killed by fixture cleanup | `TestHostedAppsFixtureCleanup` |
| Fatal owner exit still runs fixture cleanup | `TestHostedAppsFixtureCleanup` |
| Multiple helper generations register, exited handles cannot target reused PIDs | Fixture registry retains Linux process handles; lifecycle restart checks |

## Error handling

Registration failure prevents service readiness. Cleanup reports signal/retirement failures and remains bounded by existing grace constants. Never unlink or replace an admission lock, enumerate unrelated workers, or signal a numeric PID read from disk. Regression fallback uses its own captured process handles.

## Testing strategy

Write the controlled regression first and retain its red output before implementing the registry. Run new cleanup and existing hosted lifecycle checks with `-race -tags=e2e` under `scripts/test-admission.py` on Linux, plus vet and binary build. Full integration coverage and subsequent host-admission acceptance belong to the dispatcher. Estimate ~400 written lines, zero exported types, no changed consumer signatures, three acceptance criteria and fewer than ten reject paths; one repair with its regression. Rechecked before plan commit. No overlapping remote feature branch touched the selected existing file.

## Open questions

Resolve with the regression whether forced daemon teardown reproduces the separate-group escape. Do not infer a publication replacement bug without evidence.

## Documentation handoff

Pending documentation stage: in `docs/knowledge/features/e2e-harness.md`, “Teardown Sequence”, describe the implemented ownership and bounded retirement guarantee for hosted children in separate groups, its failure behavior, and the distinction between process retirement and admission-descriptor release.

## Security review

**Verdict:** PASS

- Trust boundaries/files: registry endpoint lives in a fixture-private 0700 directory; only its generated shim uses it. Registration is test-controlled, with live handles captured before acknowledgement. No product/network data controls a signal target.
- Subprocesses: MUST FIX resolved in design: numeric PID cleanup can target reused identities; Linux cleanup uses captured `os.Process` pidfd handles. Descendants are registered and retired too. No broad process census or group kill is added.
- Tokens/cryptography: no credentials, tokens or cryptographic operations are added.
- Network/I/O: local registry accepts fixed-size identity records with deadlines; cleanup closes the listener and joins its worker. No public listener changes.
- Errors/logs: report fixture identities and synthetic lock metadata only; retain failed output outside the tree.
- Concurrency: close admission before draining registered handles; cleanup is joined before fixture directory removal. Each new worker has listener closure or a deadline as its exit path.
- Threat model: test-only same-user fixture ownership; product relay trust and credential behavior are unchanged.

**Reviewer:** builder (self-review per security-review checklist)
**Date:** 2026-10-24
