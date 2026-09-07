# Rotation Watcher e2e Pattern (retired, #2137)

**Retired 2026-09-07.** `TestE2E_RotationWatcher_DetectsClear` (`internal/e2e/rotation_test.go`, #120)
drove a real `pyry` daemon through one `/clear`-shaped JSONL rotation and asserted the registry's
tracked id followed via the fsnotify watcher + real platform probe. It was deleted with
[the rotation watcher](rotation-watcher.md) it exercised — the announcement path that replaced the
watcher is already covered end-to-end, and more thoroughly (registry follow *and* client delimiter
*and* gauge), by `TestRelayV2_StreamAnnouncedResetFollowsClaude`
([`streamsup-package-announced-reset-follower.md`](streamsup-package-announced-reset-follower.md)).

The test's file-scope helpers were load-bearing for four sibling tests in `package e2e` and were not
deleted with it: `claudeSessionsDir`, `encodeWorkdir`, `uuidStemPattern`, `waitForBootstrapID`, and
`readBootstrapIfPresent` moved to `internal/e2e/registry_read_helpers_test.go`. `readBootstrap` and
`waitForBootstrapIDChange` had no surviving caller and were deleted with the test.

**Lesson: `git grep -l <name>` counts substring hits, and a load-bearing-helper table built from it
can be wrong in both directions at once.** The retirement ticket's helper table (built by grep) said
`encodeWorkdir` had no other caller — it does, `claudeSessionsDir` (listed in the same table as
load-bearing) calls it — and that `readBootstrap` was used by a sibling test — it isn't; the sibling
calls `readBootstrapIfPresent`, of which `readBootstrap` is a prefix, and the one bare match left was
inside a comment. Deleting per the table as written would have broken the build; keeping per the
table would have left an orphan. `git grep -c -w` (exact word) plus `staticcheck -tags e2e` caught
both; a substring sweep caught neither. Worth remembering anywhere a "used by" table is assembled
from `grep -l` over short, prefix-sharing identifiers.

**Lesson: `make check` runs `staticcheck ./...` untagged, so it never analyses a build-tagged
package.** The retirement ticket's plan assumed leaving an orphaned helper in `package e2e` (build
tag `e2e`) would "redden `make check`" via staticcheck's U1000 — it would not: `internal/e2e` sat
with two pre-existing U1000s at the time, both green through the standard gate. Dead code behind a
build tag has to be found deliberately (`staticcheck -tags e2e ./internal/e2e/...`), the same way
`internal/e2e/realclaude` needs its own `go vet -tags e2e_realclaude` (`make check` never compiles
that package either, since it sits behind the `e2e_realclaude` build tag).

## References

- Retirement ticket: [#2137](https://github.com/pyrycode/pyrycode/issues/2137)
- Surviving helpers: `internal/e2e/registry_read_helpers_test.go`
- Replacement coverage: [`streamsup-package-announced-reset-follower.md`](streamsup-package-announced-reset-follower.md)
- Sibling primitive (unaffected — does not touch the watcher): [`e2e-harness-rotation-primitive-startrotation-fakeclaude-test.md`](e2e-harness-rotation-primitive-startrotation-fakeclaude-test.md)
