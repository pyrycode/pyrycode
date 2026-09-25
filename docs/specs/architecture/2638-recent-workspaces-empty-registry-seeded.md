# #2638 — Recent-workspaces empty-registry subtest: start on a seeded registry

## Files read

- `internal/e2e/relay_v2_recent_workspaces_test.go` → `testV2DaemonRecentWorkspacesEmptyRegistry` — the subtest that writes `{"conversations":[]}` before daemon start; the only file touched.
- `internal/e2e/harness.go` → `premarkWorkspaceSeeded` — writes `{"conversations":[],"seeded":true}` but returns early when the file already exists, which is why the subtest's own write leaves the daemon unmarked.
- `cmd/pyry/workspace_seed.go` → `seedDefaultWorkspace` — seeds a General channel in `<home>/pyry-workspace/default` on an unmarked empty registry (since #2569), racing the phone's `recent_workspaces` request.

## Change

The subtest's registry literal changes from `{"conversations":[]}` to `{"conversations":[],"seeded":true}`, matching what `premarkWorkspaceSeeded` writes, and its comment says why the marker is there. The daemon then treats the host as already seeded and never creates the default workspace, so the empty reply is deterministic. Test-only; no production code moves, and the assertions (success reply, non-nil `Workspaces`, length 0) are unchanged. No in-flight branch touches the file.

## Testing strategy

The existing assertions cover it. Proof is `go test -tags e2e -race -count=15 -run 'TestRelayV2_RecentWorkspaces$' ./internal/e2e/` passing all 15 runs.

## Documentation handoff

None.
