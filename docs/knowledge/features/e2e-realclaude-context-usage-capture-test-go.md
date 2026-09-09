# context_usage_capture_test.go

The live `get_context_usage` control-request capture (#2287): one child, one completed
turn, then a `control_request` at each `detail` value (`summary`, `full`) on the same
held-open stdin, correlated by `request_id`. `internal/streamsup/context_usage_capture_test.go`
holds the hermetic pin (`contextUsagePinnedShapes`), re-derived from each arm's own response
bytes rather than trusted off the record's labels — the same four-quadrant fixture/pin gate
as [compaction_capture_test.go](e2e-realclaude-compaction-capture-test-go.md). Fixture
committed at `internal/e2e/realclaude/testdata/context_usage_v2.1.259.json`.

**The finding #2288 (the production writer) needs: `detail:"summary"` and `detail:"full"`
returned the identical payload against claude 2.1.259, differing only in round-trip time**
(102ms vs 710ms — a sevenfold cost). Both arms answered `subtype:"success"` with 386 numeric
leaves and the same `totalTokens` (21929). Nothing observed in this capture justifies ever
requesting `detail:"full"`; a producer should default to `"summary"` unless a future claude
version is shown to diverge.

**A lexical-only tie-break over dotted JSON leaf paths silently picks the wrong candidate
when a shallow field and a deep field share a suffix.** The response nests a whole-context
`percentage` beside a `gridRows` matrix whose every cell repeats its own category's
`percentage`; sorting matched suffixes lexically picks `gridRows[0][0].percentage` over
`response.response.percentage` because `g` < `p`. `cucapSelect`
(`internal/e2e/realclaude/context_usage_capture_test.go:551`) now breaks ties on the
shallowest matching path, ties among equal depths broken lexically only as the last resort.
The bug is generalizable: any summariser selecting one candidate out of several JSON paths
that share a leaf name must rank by depth before it ranks by string order, or a nested
duplicate silently outranks the field the caller meant.

**The daemon's own reading and claude's agree.** `contextwindow.Read` on the same session
reported 21978 tokens against a 200000 window (10.99%) beside claude's 21929 and its own
top-level 11% — the two readings agree to within the token count the transcript gained
between the control reply and the daemon's later read. No divergence to design around.

**A redaction pass over a log line can erase the one path a human needs to recover a lost
fixture.** The probe's own log line names the artifact-directory copy to promote after a
dispatcher gate run discards the in-repo write (the [promotion
path](e2e-realclaude-compaction-capture-test-go.md) `api_retry_capture_test.go` established) —
but the redactor rewrites that directory to `$ARTIFACT_DIR` in the same line, so the log
names the file to recover without naming where it is. A temp directory the run itself
created under `os.MkdirTemp` is not an operator secret; a future capture in this family
should route the artifact path around the redaction pass in its log output, even though the
committed record's own bytes must still go through it.

**The dispatcher's gate reported an unrelated test as introduced-by-this-branch.**
`TestRealClaude_ToolLoopIntegrity` failed only because it was the test running when the
package's 20-minute `go test` budget expired — on `main` alone it passes in under 6s. Not a
defect in this capture; tracked by #2305 against the shared suite's timing margin, not this
file.
