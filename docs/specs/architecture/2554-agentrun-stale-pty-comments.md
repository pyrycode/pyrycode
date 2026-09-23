# #2554 — agentrun: trust, reap and stream-runner comments that reason from the deleted PTY spawn

Comment-only change. Short plan.

## Files read

- `internal/agentrun/trust/trust.go` → package doc — names `HasTrustModal` as a live safety net and calls the trust modal a PTY-drive artifact.
- `internal/agentrun/reap.go` → the guard comment inside `ReapDescendantGroups` — claims rootPid is a group leader and cites `sess.Close`.
- `internal/agentrun/reap_test.go` → `TestReapDescendantGroups` subtests `ReapsDescendantGroupSparesCaller`, `ExcludesRootOwnGroup`, `ReapsGrandchildGroupSparesRoot` — repeat the "left for sess.Close" rationale.
- `internal/agentrun/streamrunner/reap.go` → `reapDescendantGroupsFn` doc — "alongside ptyrunner", "Mirrors ptyrunner/reap.go's seam".
- `internal/streamsup/runner.go` → package header dependency-direction note (lists ptyrunner) and the `cmd.Cancel` comment in the spawn path ("proven streamrunner/ptyrunner behaviour").
- `internal/agentrun/streamrunner/args.go` → `BuildClaudeArgs` — agent-run / self-check shape uses `--permission-mode dontAsk`, not the skip flag.
- `cmd/pyry/agent_run.go`, `cmd/pyry/main.go`, `internal/agentrun/selfcheck/selfcheck.go` → the three `trustMark` call sites (daemon serve path, `resolveSpawnDir`, `SelfCheckDenyDefault`); `pyry agent-run` itself never pre-marks.
- `docs/knowledge/features/streamrunner-package.md` § "Why a separate primitive" — 2026-05-14 probe: stream-json + `--dangerously-skip-permissions` ran without a trust dialog.
- `docs/knowledge/features/agentrun-trust-subpackage.md` § "The external-includes gate is keyed on the git root…" and `internal/e2e/realclaude/claude_md_external_includes_test.go` — live evidence that an unapproved include entry leaves a stream-json child's `@` imports silently unexpanded.
- `docs/knowledge/codebase/670.md` — the only trust-modal wedge on record was the pre-#1348 daemon PTY path; ptyrunner detected and aborted, it never dismissed.
- Spawn topology: `grep Setsid|Setpgid` over `internal/agentrun/streamrunner` and `internal/streamsup` production files returns nothing, so claude inherits pyry's process group on both paths.

## Change

Rewrite five comments so they describe today's topology:

1. **`trust.go` package doc.** Drop the PTY-drive framing and the `HasTrustModal` safety net. State that there is no runtime net (every surviving spawn is headless stream-json and nothing watches for or answers a startup dialog), then what a lost race costs per gate: workspace trust — the 2026-05-14 probe covers the skip-permissions shape the daemon uses; there is no captured evidence for the `dontAsk` shape (self-check), and `pyry agent-run` spawns that shape without pre-marking at all. External includes — proven live (#2451) to apply to stream-json children: a lost race leaves `@` imports unexpanded, with nothing logged.
2. **`ReapDescendantGroups` guard comment.** Neither spawn path sets `Setsid`/`Setpgid`, so claude shares pyry's group and the `pgid == self` guard is what spares it; the rootPid guard stays as a defence for a future spawn that makes claude a group leader. No `sess.Close`.
3. **`reap_test.go` comments** in the three subtests above: same rationale — the helper topology stands in for a future group-leader claude; no `sess.Close`.
4. **`streamrunner/reap.go`** seam doc: drop the ptyrunner sibling/mirror wording.
5. **`streamsup/runner.go`**: header lists `streamrunner, …` without ptyrunner; the `cmd.Cancel` comment cites streamrunner's behaviour only.

No code, no test assertion, no identifier changes.

## Testing strategy

No new logic, so no new proof. `go vet`, `go build`, and `go test -race` on `internal/agentrun/...` and `internal/streamsup/...` confirm nothing but comments moved; `make cite-guard` covers citation form.

## Documentation handoff (pending — documentation stage)

`docs/knowledge/features/agentrun-trust-subpackage.md`: the intro paragraph, § "No lock", the "No cross-process serialisation" bullet under "What this helper deliberately does NOT do", and the `ptyrunner-package.md` entry under "Related" still name `HasTrustModal` as the runtime safety net. Bring them in line with the rewritten `trust.go` package doc and its evidence sources listed above.
