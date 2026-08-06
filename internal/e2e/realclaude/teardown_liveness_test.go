//go:build e2e_realclaude

package realclaude

// The teardown-liveness instrument #1251 measures with: a classifier over the
// reaper's own log line, a real-bytes proof of the per-pid liveness read's
// fail-safe premise, and the record that carries all three readings.
//
// This file reaches no verdict about pyry and takes no measurement. It is
// depended on as CODE, not as evidence. Everything here runs offline: no live
// claude, no credentials, no daemon, no env gate, no t.Skip.
//
//	go test -tags e2e_realclaude -run '^TestTdn' -v ./internal/e2e/realclaude/
//
// # Reused, not rebuilt
//
// The per-pid liveness read is #1235's (pinReadState / pinClassifyState /
// pinScanArgv) and the FIFO reader-presence read is #1239's (fifoLiveRead).
// Both carry full design treatments in their own file headers — the four-valued
// read, redaction rule 1, and "an instrument failure is a datum, not an abort"
// are argued there and deliberately not restated here. This file CALLS them and
// edits neither.
//
// # The reaper line, and the two ways a matcher over it inverts
//
// reap.go:65 is the only line classified here. Measured 2026-07-30 (Darwin
// 25.5), both renderings on one machine:
//
//	time=… level=INFO msg="agentrun: reaped claude descendant process groups" count=2 pgids="[4242 77]"
//	2026/07/30 23:18:29 INFO agentrun: reaped claude descendant process groups count=2 pgids="[4242 77]"
//
// The first is slog.NewTextHandler (cmd/pyry/main.go:743). The second is
// slog.Default(), and it is the one that matters: runAgentRunPty
// (cmd/pyry/agent_run.go:299) sets no Logger on ptyrunner.Config, so
// ptyrunner.Run falls back to slog.Default() (runner.go:289-292), and every
// probe in this package spawns `pyry agent-run` and captures its stderr. A
// matcher anchored on `msg="agentrun: reaped…"` finds nothing on the live path
// and answers "no line" — read as "the reaper never fired" — with nothing going
// red. So the anchor is the BARE message text, a string literal in this file,
// never a level token, a timestamp, or a reference to what reap.go defines.
//
// Membership is over PARSED INTEGERS, never a substring, because the matcher
// inverts in both directions. A `pgids=[<held>]` substring probe is correct for
// the single-group line and fails the moment a second group is reaped, since
// slog quotes the value as soon as it contains a space — reporting "the reaper
// ran and did not kill our group", which is precisely the regression #1251
// exists to catch. Its dual is worse and is a false POSITIVE: held pgid 77
// matches the text of `pgids=[7788]`, and that is the arm a consumer reads as
// "no leak".
//
// # The premise the whole liveness read fails safe on
//
// pinClassifyState's branch 5 — non-zero exit, empty stdout AND empty stderr —
// is the only input that yields pinStateNoSuchProcess, the one verdict #1251
// reports as its result. Branch 1 keeps every broken invocation away from it,
// which is entirely load-bearing on real `ps` writing to stderr — and
// TestPinClassifyState builds its errors with pinExit1 / pinSignaled, so
// nothing asserted it. TestTdnRealPSMisinvocationsFailSafe executes real
// mis-invocations and asserts that premise on the bytes the classifier actually
// consumes.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

// --- the reaper-log classifier -----------------------------------------------

// tdnTicket is this instrument's provenance, carried into every record.
const tdnTicket = "1250"

// tdnReapMessage is reap.go:65's message text as a STRING LITERAL, deliberately
// not a reference to anything reap.go defines: a renamed message must break the
// self-checks below rather than silently follow the rename into a live run
// where "no line" reads as "the reaper never fired".
//
// The anchor is the bare text. It carries no `msg="`, no level token and no
// timestamp, because the two handlers render all three differently and the one
// the live path uses is slog.Default() (file header).
const tdnReapMessage = "agentrun: reaped claude descendant process groups"

// The reap-line verdicts. Three answers, plus the package-standard
// instrument-failed arm, which is never an answer: without it a line whose
// pgids= attribute cannot be parsed collapses into reap-line-without-held-pgid,
// which a consumer reads as "the reaper ran and did not kill our group" — a
// leak finding manufactured out of the instrument's own breakage.
const (
	// tdnReapHeldPGIDKilled: the reaper reported killing the held pgid.
	tdnReapHeldPGIDKilled = "held-pgid-in-reap-line"
	// tdnReapHeldPGIDAbsent: the reaper emitted its line and the held pgid was
	// not among the groups it reported killing.
	tdnReapHeldPGIDAbsent = "reap-line-without-held-pgid"
	// tdnReapNoLine: no such line. AMBIGUOUS by construction — reap.go:64
	// guards the emit on len(reaped) > 0, so silence means the reaper ran and
	// reaped nothing OR it never fired. The name says "no line", never "no
	// reap", and the ambiguity is spelled out in the Detail.
	tdnReapNoLine = "no-reap-line"
	// tdnReapInstrumentFailed: the line could not be read. Never a statement
	// about the reaper.
	tdnReapInstrumentFailed = "instrument-failed"
)

// tdnReapOutcome is one classification. Consumers record it verbatim into
// published evidence, so every field is self-describing: PGIDs carries the
// parsed set membership was decided against, and Count carries reap.go's own
// count= attribute so a reader sees the two frames agreeing.
type tdnReapOutcome struct {
	Verdict  string `json:"verdict"`
	Detail   string `json:"detail"`
	HeldPGID int    `json:"held_pgid"`
	PGIDs    []int  `json:"pgids_in_line,omitempty"`
	// Count is the SUM of the count= attributes across every anchored line. It
	// can legitimately differ from len(PGIDs) when two lines report the same
	// group, so a disagreement is recorded in Detail and never escalated.
	Count     int    `json:"count_attr,omitempty"`
	LineCount int    `json:"reap_lines_seen"`
	Line      string `json:"reap_line,omitempty"`
}

// tdnClassifyReapLog answers "did the reaper report killing heldPGID?" over
// pyry's captured stderr.
//
// Pure over its bytes: no exec, no file read, no clock. That is what lets every
// arm be driven with no live turn and no credentials, and it is the property
// the live consumer depends on.
//
// It takes no *testing.T and never fails a test: an instrument failure observed
// mid-turn is a datum to publish, not a reason to abort the turn — the same
// contract as fifoLiveRead and pinReadState.
//
// EVERY anchored line is scanned and their pgids unioned, rather than the first
// one winning. This is #1235's own anti-first-match discipline (pinScan.Matches
// is a slice precisely because nothing here resolves to "the" one) applied to
// lines: a second reap line is a datum in the record, not a failure.
func tdnClassifyReapLog(stderr []byte, heldPGID int) tdnReapOutcome {
	out := tdnReapOutcome{HeldPGID: heldPGID}

	if heldPGID <= 1 {
		// reap.go:52 skips pgid <= 1 before it kills anything, so no line can
		// ever carry one. Answering "absent" here would publish a leak finding
		// manufactured out of a consumer that failed to capture its pgid — the
		// same shape as pinReadState's pid <= 0 guard, and the same reason.
		out.Verdict = tdnReapInstrumentFailed
		out.Detail = tdnDetail("held pgid %d is not one the reaper can ever report: reap.go:52 "+
			"skips pgid <= 1 before it kills anything, so no answer about it could be read "+
			"out of the line", heldPGID)
		return out
	}

	seen := make(map[int]bool)
	for _, line := range strings.Split(string(stderr), "\n") {
		if !strings.Contains(line, tdnReapMessage) {
			continue
		}
		out.LineCount++
		if out.Line == "" {
			out.Line = reachCapCommand(strings.TrimSpace(line))
		}

		pgids, err := tdnParsePGIDs(line)
		if err != nil {
			out.Verdict = tdnReapInstrumentFailed
			out.Line = reachCapCommand(strings.TrimSpace(line))
			out.Detail = tdnDetail("the reaped-groups line could not be read: %v. Recorded as "+
				"%s rather than as an answer, because a line whose list cannot be parsed would "+
				"otherwise read as %q — a leak finding produced by this instrument's own "+
				"breakage", err, tdnReapInstrumentFailed, tdnReapHeldPGIDAbsent)
			return out
		}
		for _, pgid := range pgids {
			if seen[pgid] {
				continue
			}
			seen[pgid] = true
			out.PGIDs = append(out.PGIDs, pgid)
		}
		if n, ok := tdnCountAttr(line); ok {
			out.Count += n
		}
	}

	if out.LineCount == 0 {
		out.Verdict = tdnReapNoLine
		out.Detail = tdnDetail("no line carrying %q appears in %d bytes of stderr. This is "+
			"AMBIGUOUS and collapsing it into either reading is a defect: reap.go:64 guards the "+
			"emit on len(reaped) > 0, so silence means the reaper ran and reaped nothing, or "+
			"that it never fired at all", tdnReapMessage, len(stderr))
		return out
	}

	crossCheck := ""
	if out.Count != len(out.PGIDs) {
		crossCheck = fmt.Sprintf(". reap.go's own count= totals %d across %d line(s) while %d "+
			"distinct pgid(s) parsed out; the two frames disagree, which is recorded rather "+
			"than escalated because a union across lines can legitimately differ from any "+
			"single count=", out.Count, out.LineCount, len(out.PGIDs))
	}

	if seen[heldPGID] {
		out.Verdict = tdnReapHeldPGIDKilled
		out.Detail = tdnDetail("the reaper reported killing pgid %d: it is a member of %v, read "+
			"off %d anchored line(s)%s", heldPGID, out.PGIDs, out.LineCount, crossCheck)
		return out
	}
	out.Verdict = tdnReapHeldPGIDAbsent
	out.Detail = tdnDetail("the reaper emitted its line but pgid %d is not among the %d group(s) "+
		"it reported killing (%v), read off %d anchored line(s)%s",
		heldPGID, len(out.PGIDs), out.PGIDs, out.LineCount, crossCheck)
	return out
}

// tdnParsePGIDs extracts the reaped pgid set from one anchored line.
//
// Membership is decided over PARSED INTEGERS, never a substring of the bracket
// text, because a substring matcher inverts in both directions (file header).
// The value is bounded by its terminator — the closing ], then the closing
// quote when quoted — rather than by end-of-line: pgids is last in reap.go's
// call today, but a handler's WithAttrs could append more.
//
// Any parse failure is an error and therefore instrument-failed: an unreadable
// list is never reported as a list the held pgid was absent from.
func tdnParsePGIDs(line string) ([]int, error) {
	at := tdnAttrIndex(line, "pgids=")
	if at < 0 {
		return nil, errors.New("the line carries no pgids= attribute")
	}
	value := line[at+len("pgids="):]

	quoted := strings.HasPrefix(value, `"`)
	if quoted {
		value = value[1:]
	}
	if !strings.HasPrefix(value, "[") {
		return nil, fmt.Errorf("the pgids= value does not open with a bracket: %q",
			reachCapCommand(value))
	}
	end := strings.IndexByte(value, ']')
	if end < 0 {
		return nil, fmt.Errorf("the pgids= list is unterminated, with no closing bracket in %q",
			reachCapCommand(value))
	}
	if quoted && !strings.HasPrefix(value[end+1:], `"`) {
		return nil, fmt.Errorf("the quoted pgids= value is unterminated, with no closing quote "+
			"after the bracket in %q", reachCapCommand(value))
	}

	var pgids []int
	for _, field := range strings.Fields(value[1:end]) {
		pgid, err := strconv.Atoi(field)
		if err != nil {
			return nil, fmt.Errorf("the pgids= list holds a non-integer element %q", field)
		}
		pgids = append(pgids, pgid)
	}
	return pgids, nil
}

// tdnCountAttr reads reap.go's own count= attribute for cross-check. Its
// absence is not a failure — the answer is decided by the parsed pgid list, and
// count= only lets a reader see the two frames agreeing.
func tdnCountAttr(line string) (int, bool) {
	at := tdnAttrIndex(line, "count=")
	if at < 0 {
		return 0, false
	}
	value := line[at+len("count="):]
	if end := strings.IndexByte(value, ' '); end >= 0 {
		value = value[:end]
	}
	count, err := strconv.Atoi(strings.Trim(value, `"`))
	if err != nil {
		return 0, false
	}
	return count, true
}

// tdnAttrIndex finds an slog attribute key at a position where it genuinely
// starts an attribute — the beginning of the line, or after a space.
//
// Without that bound, a key is matched anywhere it appears as a tail: a
// handler's WithAttrs adding `reap_pgids=` would otherwise be read as this
// line's `pgids=`. Anchoring on the message first and then on the attribute
// boundary is also what keeps reap.go:59's Warn — whose attribute is `pgid=`,
// singular — out of the answer entirely.
func tdnAttrIndex(line, attr string) int {
	for i := 0; i+len(attr) <= len(line); {
		next := strings.Index(line[i:], attr)
		if next < 0 {
			return -1
		}
		at := i + next
		if at == 0 || line[at-1] == ' ' {
			return at
		}
		i = at + 1
	}
	return -1
}

// tdnDetail formats a Detail line and caps it with #1230's existing helper. The
// cap is not cosmetic: these strings land in an artifact an operator pastes
// into a public issue, and a reap line can carry an unbounded pgid list.
func tdnDetail(format string, args ...any) string {
	return reachCapCommand(fmt.Sprintf(format, args...))
}

// --- the record and its redaction-safe writer --------------------------------

// tdnArtifactName is the ONE file this writer emits.
const tdnArtifactName = "teardown.json"

// The two points a teardown measurement is read at, carried in the snapshot
// itself. A snapshot that cannot say WHICH point it was taken at is exactly the
// ambiguity #1251's flip gate exists to remove, so this is a member and never a
// position in a slice.
const (
	tdnAtBefore = "before-teardown"
	tdnAtAfter  = "after-teardown"
)

// The dispositions a live consumer reaches, as a POSITIVE ALLOWLIST: exactly
// one of them is a finding about pyry and everything ambiguous lands on
// tdnDispositionSkipped rather than on a verdict.
//
// Two rules the allowlist encodes, neither of which may be softened:
//
//   - A BROKEN INSTRUMENT NEVER OPENS A LEAK TICKET. Either classifier
//     reporting instrument-failed records and skips. Without that arm an
//     unparseable pgids= collapses into reap-line-without-held-pgid, which a
//     consumer reads as "the reaper ran and did not kill our group" — a leak
//     finding manufactured out of the instrument's own breakage.
//   - A TRIGGER MISS IS NOT A PYRY REGRESSION. Preconditions unmet is a
//     record-and-skip, not a red test.
const (
	// tdnDispositionReaperKilled: the command is dead, it could not have
	// finished on its own (the FIFO write end was held across the teardown),
	// and the reaper reported killing its group — so kill(2) succeeded, because
	// reap.go:56-62 skips ESRCH BEFORE the append. The full-strength reading.
	tdnDispositionReaperKilled = "dead-by-reaper"
	// tdnDispositionDeadUnattributed: the command is dead and could not have
	// finished on its own, but the reaper's hand is not established.
	tdnDispositionDeadUnattributed = "dead-not-attributed-to-the-reaper"
	// tdnDispositionLeaked: the ONLY red arm. The pinned process is still
	// running after pyry exited and the content re-match still identifies it as
	// this run's command.
	tdnDispositionLeaked = "leaked"
	// tdnDispositionSkipped: everything else. Never a statement about pyry.
	tdnDispositionSkipped = "skipped"
)

// tdnSnapshot is one point-in-time reading of the pinned command: the content
// match, one per-pid liveness read for every pid pinned at the before-snapshot,
// and the FIFO's own reader state.
//
// ScanErr and Liveness are SEPARATE VALUES WITH NO CROSS-ASSIGNMENT — #1235's
// gate-placement obligation. A failed scan must neither relabel a genuine
// liveness verdict nor be relabelled by one; the consumer's gate belongs after
// its own outcome is decided.
type tdnSnapshot struct {
	At string `json:"at"`
	// ArgvScan is the ONLY member of this record that can carry a command
	// string, and that is the design rather than an accident: `command` reaches
	// the record from the content-first argv scan that already publishes
	// matched rows, never from the narrow per-pid read whose column set
	// TestPinStateColumns_ReadsNoEnvironment pins.
	ArgvScan pinScan `json:"argv_scan"`
	ScanErr  string  `json:"argv_scan_error,omitempty"`
	// Liveness is INDEX-ALIGNED with tdnRecord.HeldPIDs: entry i is about
	// HeldPIDs[i]. That is an invariant, not a convention — every
	// pinStateOutcome carries its own PID, so the artifact is self-checking and
	// the alignment is asserted rather than trusted.
	Liveness []pinStateOutcome `json:"liveness"`
	FIFO     fifoLiveOutcome   `json:"fifo"`
}

// tdnRecord composes the readings a teardown measurement rests on. It
// COMPOSES; it does not conclude — no verdict is synthesised across the
// members, and no pid/content join is performed here, because both live
// consumers own their own join at the rig level (#1236 AC2, #1251 AC3).
// Disposition is a field the rig SETS through decide; nothing inside this type
// derives it.
//
// No new liveness type: the per-pid and FIFO readings are #1235's and #1239's
// outcomes verbatim.
//
// The before/after PAIR is the shape, not a single reading with a Notes
// sentence beside it: the instrument has to be shown flipping inside the very
// run whose verdict it reports, and a flip recorded as prose is one keystroke
// from the hand-typed record #1251 exists to rule out.
type tdnRecord struct {
	Ticket string   `json:"ticket"`
	Notes  []string `json:"notes,omitempty"`

	// Provenance. RunnerFromEnv is DOCUMENTATION and RunnerFromArgv is the
	// EVIDENCE — reachRunnerPathFromEnv reads an environment the operator's
	// shell may already have set (it had, on 2026-07-25, silently invalidating
	// a #1223 gate), while the argv read names the runner from the process
	// table this run actually produced.
	ClaudeVersion  string `json:"claude_version,omitempty"`
	TeardownPath   string `json:"teardown_path,omitempty"`
	RunnerFromEnv  string `json:"runner_from_env,omitempty"`
	RunnerFromArgv string `json:"runner_from_argv,omitempty"`

	// FIFOPath is the run-unique needle the content match is decided against,
	// carried so the after-read's pid-reuse join is decidable from the record
	// alone.
	FIFOPath string `json:"fifo_path,omitempty"`
	PyryPID  int    `json:"pyry_pid,omitempty"`
	HeldPGID int    `json:"held_pgid"`
	// HeldPIDs is a SLICE, not a pid: #1230's live run matched two rows on the
	// FIFO needle — the `zsh -c` wrapper claude runs Bash through, whose argv
	// carries the whole command string, and the `cat` itself — so resolving
	// "the" held pid is the first-match defect this family already flags.
	HeldPIDs []int `json:"held_pids,omitempty"`

	Before *tdnSnapshot `json:"before,omitempty"`
	After  *tdnSnapshot `json:"after,omitempty"`

	Reap tdnReapOutcome `json:"reap"`

	Disposition       string `json:"disposition"`
	DispositionDetail string `json:"disposition_detail"`
}

func (r *tdnRecord) note(format string, args ...any) {
	r.Notes = append(r.Notes, fmt.Sprintf(format, args...))
}

// decide sets the disposition and the sentence that earns it, mirroring
// reachRecord.decide (background_reach_probe_test.go:261). The detail is capped
// like every other string in this record: it lands in an artifact an operator
// pastes into a public issue and can quote a classifier Detail that already
// carries `ps` stderr.
func (r *tdnRecord) decide(verdict, format string, args ...any) {
	r.Disposition = verdict
	r.DispositionDetail = tdnDetail(format, args...)
}

// writeTdnArtifacts persists the record as ONE JSON file, mode 0600, in dir.
//
// It follows writeReachArtifacts (background_reach_probe_test.go:823) and
// diverges in exactly one way: that writer emits a second file holding a
// verbatim three-integer ps snapshot, and this one emits nothing besides the
// record. This ticket takes no wide integer snapshot, and its one wide read
// (pinScanArgv → reachScanArgv) never lets its raw table out of that frame — so
// "the writer writes exactly one file" is a checkable statement of "no verbatim
// ps output is persisted", and TestTdnRecordWriter asserts it by reading the
// directory.
//
// t.Errorf rather than t.Fatalf: the evidence is the deliverable, so a lost
// artifact is loud, but it must not abort the caller's remaining cleanups.
func writeTdnArtifacts(t *testing.T, dir string, rec *tdnRecord) {
	t.Helper()
	blob, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		t.Errorf("marshal teardown record: %v", err)
		return
	}
	path := filepath.Join(dir, tdnArtifactName)
	if err := os.WriteFile(path, append(blob, '\n'), 0o600); err != nil {
		t.Errorf("write teardown artifact %s: %v", path, err)
	}
}

// --- self-checks: the reaper-log classifier ----------------------------------

// The fixture renderings, measured 2026-07-30 on Darwin 25.5 against Go's
// log/slog. The message text is a STRING LITERAL in every one of them and is
// never read back out of reap.go: a renamed message must break this test rather
// than silently follow it.
const (
	tdnFixtureTextOne = `time=2026-07-30T23:18:29.220+03:00 level=INFO ` +
		`msg="agentrun: reaped claude descendant process groups" count=1 pgids=[89355]`

	tdnFixtureTextTwo = `time=2026-07-30T23:18:29.220+03:00 level=INFO ` +
		`msg="agentrun: reaped claude descendant process groups" count=2 pgids="[89355 4242]"`

	tdnFixtureDefaultOne = `2026/07/30 23:18:29 INFO ` +
		`agentrun: reaped claude descendant process groups count=1 pgids=[89355]`

	tdnFixtureDefaultTwo = `2026/07/30 23:18:29 INFO ` +
		`agentrun: reaped claude descendant process groups count=2 pgids="[89355 4242]"`

	// tdnFixtureOtherLines carries reap.go:59's Warn, whose attribute is `pgid=`
	// (SINGULAR) and whose pgid is the held one. Nothing in it is the Info line,
	// so the held pgid appearing in the bytes must not produce an answer.
	tdnFixtureOtherLines = `2026/07/30 23:18:28 INFO pyry: agent-run starting workdir=/tmp/wd
2026/07/30 23:18:29 WARN agentrun: descendant reap: kill group failed pgid=89355 err="operation not permitted"
2026/07/30 23:18:31 INFO pyry: claude exited status=0`
)

// tdnFixtureHeldPGID is the pgid the fixtures above report as reaped.
const tdnFixtureHeldPGID = 89355

func TestTdnClassifyReapLog(t *testing.T) {
	tests := []struct {
		name         string
		stderr       string
		held         int
		wantVerdict  string
		wantPGIDs    []int
		wantLines    int
		wantCount    int
		wantDetailIn []string
	}{
		{
			name:        "TextHandler, one pgid, held present",
			stderr:      tdnFixtureTextOne,
			held:        tdnFixtureHeldPGID,
			wantVerdict: tdnReapHeldPGIDKilled,
			wantPGIDs:   []int{89355},
			wantLines:   1,
			wantCount:   1,
		},
		{
			// The case a single-pgid matcher gets wrong. slog quotes the value
			// the moment it contains a space, so `pgids=[89355]` becomes
			// `pgids="[89355 4242]"` and a substring probe stops matching —
			// inverting the reading on exactly the multi-group regression #1251
			// exists to catch.
			name:        "TextHandler, slog's quoted multi-pgid rendering, held present",
			stderr:      tdnFixtureTextTwo,
			held:        tdnFixtureHeldPGID,
			wantVerdict: tdnReapHeldPGIDKilled,
			wantPGIDs:   []int{89355, 4242},
			wantLines:   1,
			wantCount:   2,
		},
		{
			// The rendering the LIVE path produces: `pyry agent-run` passes no
			// Logger, so ptyrunner falls back to slog.Default().
			name:        "slog.Default rendering, one pgid, held present",
			stderr:      tdnFixtureDefaultOne,
			held:        tdnFixtureHeldPGID,
			wantVerdict: tdnReapHeldPGIDKilled,
			wantPGIDs:   []int{89355},
			wantLines:   1,
			wantCount:   1,
		},
		{
			name:        "slog.Default rendering, quoted multi-pgid, held present",
			stderr:      tdnFixtureDefaultTwo,
			held:        tdnFixtureHeldPGID,
			wantVerdict: tdnReapHeldPGIDKilled,
			wantPGIDs:   []int{89355, 4242},
			wantLines:   1,
			wantCount:   2,
		},
		{
			name:         "a reaped line that does not carry the held pgid",
			stderr:       tdnFixtureTextTwo,
			held:         777,
			wantVerdict:  tdnReapHeldPGIDAbsent,
			wantPGIDs:    []int{89355, 4242},
			wantLines:    1,
			wantCount:    2,
			wantDetailIn: []string{"777"},
		},
		{
			// The false POSITIVE a substring matcher produces: 77 is inside the
			// text of 7788, and this is the arm a consumer reads as "no leak".
			name: "a held pgid that is a substring of a reaped one is not a member",
			stderr: `2026/07/30 23:18:29 INFO ` +
				`agentrun: reaped claude descendant process groups count=1 pgids=[7788]`,
			held:        77,
			wantVerdict: tdnReapHeldPGIDAbsent,
			wantPGIDs:   []int{7788},
			wantLines:   1,
			wantCount:   1,
		},
		{
			// reap.go:59's Warn carries `pgid=` (singular) and the held pgid, so
			// bytes alone are not an answer: the anchor is the message.
			name:        "other pyry lines, including the singular-pgid Warn, are not the reap line",
			stderr:      tdnFixtureOtherLines,
			held:        tdnFixtureHeldPGID,
			wantVerdict: tdnReapNoLine,
			wantLines:   0,
			// reap.go:64 guards the emit on len(reaped) > 0, so silence has TWO
			// readings and collapsing them into either one is the defect.
			wantDetailIn: []string{"reaped nothing", "never fired"},
		},
		{
			name:         "empty input",
			stderr:       "",
			held:         tdnFixtureHeldPGID,
			wantVerdict:  tdnReapNoLine,
			wantLines:    0,
			wantDetailIn: []string{"reaped nothing", "never fired"},
		},
		{
			name: "an anchored line with no pgids attribute is a broken instrument",
			stderr: `2026/07/30 23:18:29 INFO ` +
				`agentrun: reaped claude descendant process groups count=1`,
			held:         tdnFixtureHeldPGID,
			wantVerdict:  tdnReapInstrumentFailed,
			wantLines:    1,
			wantDetailIn: []string{"pgids="},
		},
		{
			name: "an anchored line with an unterminated pgid list is a broken instrument",
			stderr: `2026/07/30 23:18:29 INFO ` +
				`agentrun: reaped claude descendant process groups count=2 pgids="[89355 4242`,
			held:         tdnFixtureHeldPGID,
			wantVerdict:  tdnReapInstrumentFailed,
			wantLines:    1,
			wantDetailIn: []string{"unterminated"},
		},
		{
			name: "an anchored line whose list holds a non-integer is a broken instrument",
			stderr: `2026/07/30 23:18:29 INFO ` +
				`agentrun: reaped claude descendant process groups count=2 pgids="[89355 abc]"`,
			held:         tdnFixtureHeldPGID,
			wantVerdict:  tdnReapInstrumentFailed,
			wantLines:    1,
			wantDetailIn: []string{"abc"},
		},
		{
			// The union across lines, not the first match. #1235's own
			// anti-first-match discipline (pinScan.Matches is a slice precisely
			// because nothing here resolves to "the" one) applied to lines.
			//
			// The ORDER of the two fixtures is the whole point and is not
			// interchangeable — #1250 shipped it the other way round and code
			// review caught that the row did not discriminate the mutation its
			// name claims to guard (#1250 PR #1252, SHOULD FIX, repaired here).
			// tdnFixtureDefaultTwo renders `pgids="[89355 4242]"`, so putting it
			// FIRST puts the held pgid in line 1 and a first-line-only union still
			// answers held-pgid-in-reap-line: only the LineCount/Count bookkeeping
			// caught the truncation, never the verdict a consumer reads.
			//
			// This way round, line 1 contributes {89355} alone and 4242 appears
			// solely in line 2, so a first-line-only union yields
			// tdnReapHeldPGIDAbsent and the row fails on the VERDICT. AC4's
			// multi-group requirement rests on this arm.
			name:        "two reap lines, the held pgid only in the second",
			stderr:      tdnFixtureTextOne + "\n" + tdnFixtureDefaultTwo,
			held:        4242,
			wantVerdict: tdnReapHeldPGIDKilled,
			// 89355 from line 1, 4242 new in line 2; line 2's duplicate 89355 is
			// deduped by `seen`. count= totals 1 + 2 = 3 across the two lines.
			wantPGIDs: []int{89355, 4242},
			wantLines: 2,
			wantCount: 3,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := tdnClassifyReapLog([]byte(tc.stderr), tc.held)

			if got.Verdict != tc.wantVerdict {
				t.Fatalf("verdict: got %q (%s), want %q", got.Verdict, got.Detail, tc.wantVerdict)
			}
			if !tdnIsReapVerdict(got.Verdict) {
				t.Errorf("verdict %q is not one of the recorded values", got.Verdict)
			}
			if got.HeldPGID != tc.held {
				t.Errorf("held pgid: got %d, want %d — the outcome names the pgid it was asked "+
					"about", got.HeldPGID, tc.held)
			}
			if !tdnEqualInts(got.PGIDs, tc.wantPGIDs) {
				t.Errorf("pgids: got %v, want %v — the record shows what membership was decided "+
					"against, not merely the verdict", got.PGIDs, tc.wantPGIDs)
			}
			if got.LineCount != tc.wantLines {
				t.Errorf("reap lines seen: got %d, want %d", got.LineCount, tc.wantLines)
			}
			if got.Count != tc.wantCount {
				t.Errorf("count attr: got %d, want %d — reap.go's own count= is carried for "+
					"cross-check against the parsed list", got.Count, tc.wantCount)
			}
			if got.Detail == "" {
				t.Errorf("empty detail: an outcome that cannot say which arm fired and why is " +
					"indistinguishable from a reading")
			}
			for _, want := range tc.wantDetailIn {
				if !strings.Contains(got.Detail, want) {
					t.Errorf("detail: got %q, want it to contain %q", got.Detail, want)
				}
			}
		})
	}

	t.Run("a pgid the reaper could never report is rejected rather than answered", func(t *testing.T) {
		// reap.go:52 skips pgid <= 1 before it kills anything, so no line can
		// ever carry one. Answering "absent" for such a caller would manufacture
		// a leak finding out of a consumer that failed to capture its pgid —
		// the exact fail-safe rule this instrument is built on.
		for _, held := range []int{0, -1, 1} {
			got := tdnClassifyReapLog([]byte(tdnFixtureDefaultOne), held)
			if got.Verdict != tdnReapInstrumentFailed {
				t.Errorf("tdnClassifyReapLog(held=%d) = %q (%s); want %q", held, got.Verdict,
					got.Detail, tdnReapInstrumentFailed)
			}
		}
	})
}

// --- self-checks: the liveness read's fail-safe premise, against real ps -----

// TestTdnRealPSMisinvocationsFailSafe executes real `ps` mis-invocations and
// proves, on the bytes the classifier actually consumes, that a broken
// instrument can never publish an absence.
//
// The load-bearing assertion in every arm is that STDERR IS NON-EMPTY. That is
// what keeps pinClassifyState's branch 5 — the only input that yields
// pinStateNoSuchProcess — unreachable from a broken instrument. A verdict-only
// assertion would still pass on a platform that had gone silent on stderr,
// which is precisely the scenario in which the instrument would publish "the
// process is gone" out of its own breakage.
//
// # Which of these shapes can occur through pinReadState in production
//
//   - A non-numeric pid: UNREACHABLE. pinReadState takes an int.
//   - A non-positive pid: UNREACHABLE past the guard at :276, and already
//     covered by TestPinClassifyState's final subtest (:919). Not duplicated.
//   - Arms A, B and C: UNREACHABLE without an edit to pinStateColumns or
//     pinStateArgs, which TestPinStateColumns_ReadsNoEnvironment (:950) already
//     catches. They are proven here as PLATFORM facts, not as reachable paths.
//   - Arm D: structurally REACHABLE. pinReadState bounds the pid below and
//     never above, so any caller holding a garbage-but-positive pid reaches it.
//     No current caller does — pids come from pinScanArgv matches and
//     cmd.Process.Pid — but this is the one arm whose premise protects a live
//     path rather than an edit-only one.
func TestTdnRealPSMisinvocationsFailSafe(t *testing.T) {
	arms := []struct {
		name string
		// args is built by construction, never by pinStateArgs: deviating from
		// the production argument list is what makes these mis-invocations.
		args  []string
		pid   int
		extra func(t *testing.T, stdout []byte, err error)
	}{
		{
			// The strongest fixture here, and deliberately FOUR columns rather
			// than the three in the ticket body. `ps` drops the unknown column
			// and prints the rest, so a four-column request comes back as a
			// three-field row that pinStateRow parses SUCCESSFULLY — see extra.
			name:  "A. a bad column carrying the = suffix",
			args:  []string{"-p", "1", "-o", "pid=,nosuchcolumn=,ppid=,stat="},
			pid:   1,
			extra: tdnAssertPartialRowIsStillNotAReading,
		},
		{
			name: "B. a bare bad column with no = suffix",
			args: []string{"-p", "1", "-o", "nosuchcolumn"},
			pid:  1,
		},
		{
			name: "C. an illegal option",
			args: []string{"-Q", "-p", "1"},
			pid:  1,
		},
	}

	for _, arm := range arms {
		t.Run(arm.name, func(t *testing.T) {
			stdout, err := tdnRunPS(arm.args)
			tdnAssertFailsSafe(t, arm.args, arm.pid, stdout, err)
			if arm.extra != nil {
				arm.extra(t, stdout, err)
			}
		})
	}

	t.Run("D. an out-of-range pid, confirmed rejected rather than assumed", func(t *testing.T) {
		// The threshold is PLATFORM-DEPENDENT and a hard-coded constant is a
		// portability trap: macOS caps pids at 99999 (measured — `ps -p 99999`
		// is a clean empty/empty absence and 100000 is the first rejection),
		// while Linux's default pid_max is 4194304, where 100000 is an ordinary
		// unused pid and this arm would silently become an absence test —
		// asserting instrument-failed against real bytes that correctly say
		// no-such-process. So the ladder escalates until ps actually rejects an
		// operand, and the rejection is confirmed rather than assumed.
		var tried []string
		for _, candidate := range tdnOutOfRangeLadder {
			args, classifyPID := tdnCandidateArgs(candidate)
			stdout, err := tdnRunPS(args)
			exitErr, ok := tdnExitError(t, args, err)
			if !ok {
				// Exit 0: the operand names a live process. Keep escalating.
				tried = append(tried, fmt.Sprintf("%s: exit 0, %d stdout bytes (a live pid)",
					candidate.operand, len(stdout)))
				continue
			}
			tried = append(tried, fmt.Sprintf("%s: exit %d, %d stdout bytes, %d stderr bytes",
				candidate.operand, exitErr.ExitCode(), len(stdout), len(exitErr.Stderr)))
			if len(exitErr.Stderr) == 0 {
				// Accepted as an ordinary unused pid: exit 1 with empty stdout
				// and empty stderr is a LEGITIMATE no-such-process on this
				// platform, not a rejection. Keep escalating.
				continue
			}
			t.Logf("#1250 arm D fired on candidate %s: %s", candidate.operand, tried[len(tried)-1])
			tdnAssertFailsSafe(t, args, classifyPID, stdout, err)
			return
		}
		t.Fatalf("no candidate in the ladder was rejected by ps, so this platform's "+
			"out-of-range arm was never exercised; every candidate reported:\n  %s",
			strings.Join(tried, "\n  "))
	})
}

// tdnAssertPartialRowIsStillNotAReading is arm A's addition: on a platform that
// prints the surviving columns, the stdout of a BROKEN ps parses as a
// well-formed row about pid 1 that pinIsZombie reads as running. Branch order
// alone stands between that and a fabricated `running` verdict.
//
// The parse is CONDITIONAL because the shape is Darwin-specific — Linux procps
// rejects an unknown -o specifier with empty stdout — so the portable
// assertions are the invariants in tdnAssertFailsSafe and this one records what
// the platform produced either way.
func tdnAssertPartialRowIsStillNotAReading(t *testing.T, stdout []byte, err error) {
	t.Helper()
	pid, ppid, state, ok := pinStateRow(stdout)
	if !ok {
		t.Logf("#1250 arm A: this platform printed %d bytes on stdout that parse to no "+
			"well-formed row; the partial-row shape is Darwin-specific", len(stdout))
		return
	}
	t.Logf("#1250 arm A: a BROKEN ps printed a row that parses as pid=%d ppid=%d state=%q "+
		"(zombie=%t) — a live-looking reading about pid 1, produced entirely by the "+
		"instrument's own breakage", pid, ppid, state, pinIsZombie(state))
	got := pinClassifyState(1, stdout, err)
	if got.Verdict != pinStateInstrumentFailed {
		t.Fatalf("a parseable row on the stdout of a failed ps classified as %q (%s); want %q — "+
			"stdout alongside an error is never parsed as process rows",
			got.Verdict, got.Detail, pinStateInstrumentFailed)
	}
	if got.StateColumn != "" {
		t.Errorf("instrument-failed outcome recorded state column %q; nothing was read",
			got.StateColumn)
	}
}

// tdnAssertFailsSafe asserts one arm's observed triple and then its verdict.
func tdnAssertFailsSafe(t *testing.T, args []string, pid int, stdout []byte, err error) {
	t.Helper()
	exitErr, ok := tdnExitError(t, args, err)
	if !ok {
		t.Fatalf("ps %s exited 0 with %d bytes on stdout; a mis-invocation must fail",
			strings.Join(args, " "), len(stdout))
	}
	if exitErr.ExitCode() <= 0 {
		t.Fatalf("ps %s reported exit code %d; want a normal non-zero exit (a negative code is a "+
			"signal, which is a different arm of the classifier)",
			strings.Join(args, " "), exitErr.ExitCode())
	}

	// THE load-bearing assertion. pinClassifyState's branch 1 is what keeps
	// branch 5 — the only producer of no-such-process — unreachable from a
	// broken instrument, and branch 1 fires on stderr and nothing else.
	if len(exitErr.Stderr) == 0 {
		t.Fatalf("ps %s exited %d with SILENT STDERR. Every branch that keeps a broken "+
			"instrument away from %q depends on ps writing there, so on this platform a "+
			"mis-invoked ps is byte-identical to a dead pid and the liveness read would "+
			"publish an absence out of its own breakage",
			strings.Join(args, " "), exitErr.ExitCode(), pinStateNoSuchProcess)
	}
	t.Logf("#1250 ps %s -> exit %d, %d stdout bytes, %d stderr bytes: %s",
		strings.Join(args, " "), exitErr.ExitCode(), len(stdout), len(exitErr.Stderr),
		reachCapCommand(strings.TrimSpace(string(exitErr.Stderr))))

	got := pinClassifyState(pid, stdout, err)
	if got.Verdict != pinStateInstrumentFailed {
		t.Fatalf("real bytes from `ps %s` classified as %q (%s); want %q, and NEVER %q or %q — "+
			"a half-run instrument must not publish a statement about the process",
			strings.Join(args, " "), got.Verdict, got.Detail, pinStateInstrumentFailed,
			pinStateNoSuchProcess, pinStateRunning)
	}
	if got.ToolStderr == "" {
		t.Errorf("outcome recorded no tool stderr although ps wrote %d bytes there; the "+
			"classifier reads a different channel from the one asserted above",
			len(exitErr.Stderr))
	}
	if !strings.Contains(got.Detail, "stderr") {
		t.Errorf("detail %q does not name stderr; branch 1 is the arm that must fire here, "+
			"because it is the one that precedes every parse", got.Detail)
	}
}

// tdnExitError extracts the *exec.ExitError from one ps result, failing the
// test on an error that is not one (ps missing, or never started).
func tdnExitError(t *testing.T, args []string, err error) (*exec.ExitError, bool) {
	t.Helper()
	if err == nil {
		return nil, false
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("ps %s returned %T (%v), not an *exec.ExitError; ps never ran, so no premise "+
			"about its output could be tested", strings.Join(args, " "), err, err)
	}
	return exitErr, true
}

// tdnPIDCandidate is one rung of the out-of-range ladder. pid is 0 when the
// operand does not fit an int64, and the operand is carried as a string so a
// value past every integer type can still be handed to ps.
type tdnPIDCandidate struct {
	operand string
	pid     int64
}

// tdnOutOfRangeLadder escalates past both platforms' pid ceilings. Measured
// rejections on Darwin 25.5: 100000 (the first), 4194305, 2147483647,
// 4294967296 — all `ps: process id too large`. 99999 is NOT here: it is a clean
// empty/empty absence on macOS, which is a legitimate no-such-process.
var tdnOutOfRangeLadder = []tdnPIDCandidate{
	{operand: "100000", pid: 100000},
	{operand: "4194305", pid: 4194305},
	{operand: "2147483648", pid: 2147483648},
	{operand: "4294967296", pid: 4294967296},
	{operand: "9223372036854775807", pid: 9223372036854775807},
	{operand: "99999999999999999999"},
}

// tdnCandidateArgs builds one rung's argument list, using the PRODUCTION list
// wherever the candidate fits an int so this arm also exercises pinStateArgs
// verbatim. The `int64(int(pid)) == pid` round-trip keeps the ladder portable
// to a 32-bit int, where the upper rungs would otherwise not be representable.
func tdnCandidateArgs(c tdnPIDCandidate) (args []string, classifyPID int) {
	if c.pid > 0 && int64(int(c.pid)) == c.pid {
		return pinStateArgs(int(c.pid)), int(c.pid)
	}
	return []string{"-p", c.operand, "-o", pinStateColumns}, 0
}

// tdnRunPS execs one ps and returns exactly what pinReadState's own call
// returns (process_pin_liveness_test.go:293).
//
// .Output() and nothing else. It populates *exec.ExitError.Stderr ONLY because
// it owns cmd.Stderr; a helper that set cmd.Stderr = &buf to "capture stderr
// for the assertion" would leave ExitError.Stderr empty, and the classifier
// would then see exit 1 with empty stdout and empty stderr — branch 5, the very
// arm this test exists to prove unreachable. The premise has to be asserted on
// the bytes the consumer consumes, or the assertion is about a different
// channel.
func tdnRunPS(args []string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), reachPSTimeout)
	defer cancel()
	return exec.CommandContext(ctx, "ps", args...).Output()
}

// --- self-checks: the record and its redaction-safe writer -------------------

func TestTdnRecordWriter(t *testing.T) {
	rec := tdnFixtureRecord()
	blob, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		t.Fatalf("marshal record: %v", err)
	}

	t.Run("the marshalled record carries no environment read", func(t *testing.T) {
		// Asserted over the MARSHALLED RECORD, never over this file's source:
		// arms B and C above legitimately contain the strings `nosuchcolumn` and
		// `-Q`, so a source-level grep tripwire would be checking the wrong
		// artifact.
		for _, forbidden := range []string{"environ", "ps -E", "-eww", "eww"} {
			if strings.Contains(string(blob), forbidden) {
				t.Errorf("the record carries %q; `ps -E` / `ps -e <env column>` / BSD `ps eww` "+
					"print each process's full ENVIRONMENT, which on an operator machine means "+
					"CLAUDE_CODE_OAUTH_TOKEN / ANTHROPIC_API_KEY, into an artifact destined for "+
					"a public issue", forbidden)
			}
		}
	})

	t.Run("the disposition is one of the recorded values", func(t *testing.T) {
		// The record composes; it does not conclude. Disposition is a field the
		// rig SETS, so the only property this file can assert about it is that
		// it is a member of the allowlist — an unrecognised string in a
		// published artifact is a verdict nobody can look up.
		if !tdnIsDisposition(rec.Disposition) {
			t.Errorf("disposition %q is not one of the recorded values", rec.Disposition)
		}
		if rec.DispositionDetail == "" {
			t.Error("empty disposition detail: a disposition that cannot say what earned it " +
				"is indistinguishable from an unset field")
		}
	})

	t.Run("a command string reaches the record only from the argv scan", func(t *testing.T) {
		// The positive half first: the allowed source genuinely publishes one,
		// so the negative assertions below are not vacuous.
		scan, err := json.Marshal(rec.Before.ArgvScan)
		if err != nil {
			t.Fatalf("marshal argv scan: %v", err)
		}
		if !strings.Contains(string(scan), `"command"`) {
			t.Fatalf("the argv scan published no command at all, so the checks below prove "+
				"nothing: %s", scan)
		}

		// The narrow per-pid read's column set is pinned by
		// TestPinStateColumns_ReadsNoEnvironment (:950) and is not restated
		// here. This is the record-level tripwire against a FUTURE field: it
		// holds structurally today because none of these types has one. Both
		// snapshots are walked, not just one: #1251 widened the record to carry
		// a before/after pair, and a tripwire that only watched one half would
		// let a future field through on the other.
		for _, part := range []struct {
			name string
			v    any
		}{
			{name: "before liveness (the narrow per-pid read)", v: rec.Before.Liveness},
			{name: "before fifo", v: rec.Before.FIFO},
			{name: "after liveness (the narrow per-pid read)", v: rec.After.Liveness},
			{name: "after fifo", v: rec.After.FIFO},
			{name: "reap", v: rec.Reap},
		} {
			b, err := json.Marshal(part.v)
			if err != nil {
				t.Fatalf("marshal %s: %v", part.name, err)
			}
			if strings.Contains(string(b), `"command"`) {
				t.Errorf("%s carries a command key: %s\ncommand may reach the record only from "+
					"the argv scan that already publishes matched rows, never from the narrow "+
					"per-pid read", part.name, b)
			}
		}
	})

	t.Run("the writer emits exactly one file, mode 0600", func(t *testing.T) {
		dir := t.TempDir()
		writeTdnArtifacts(t, dir, rec)

		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("read artifact dir: %v", err)
		}
		// "Exactly one file" IS the redaction assertion. writeReachArtifacts
		// writes a second file holding a verbatim three-integer ps snapshot;
		// this ticket takes no wide integer snapshot at all, and its one wide
		// read never lets its raw table out of reachScanArgv's frame, so no
		// verbatim ps output is persisted anywhere.
		if len(entries) != 1 {
			t.Fatalf("artifact dir holds %d entries (%v); want exactly one — no verbatim ps "+
				"output is persisted by this writer", len(entries), entries)
		}
		if entries[0].Name() != tdnArtifactName {
			t.Errorf("artifact name: got %q, want %q", entries[0].Name(), tdnArtifactName)
		}

		info, err := os.Stat(filepath.Join(dir, entries[0].Name()))
		if err != nil {
			t.Fatalf("stat artifact: %v", err)
		}
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Errorf("artifact mode: got %04o, want 0600", perm)
		}

		written, err := os.ReadFile(filepath.Join(dir, entries[0].Name()))
		if err != nil {
			t.Fatalf("read artifact: %v", err)
		}
		var round tdnRecord
		if err := json.Unmarshal(written, &round); err != nil {
			t.Fatalf("the artifact is not valid JSON: %v\n%s", err, written)
		}
		if round.Reap.Verdict != rec.Reap.Verdict {
			t.Errorf("round-tripped record lost its reap verdict: %q, want %q",
				round.Reap.Verdict, rec.Reap.Verdict)
		}
		// The FLIP is the property #1251 depends on, so the round-trip asserts
		// the pair rather than a single verdict: a record that marshalled only
		// one of the two snapshots would still round-trip a verdict cleanly.
		if round.Before == nil || round.After == nil {
			t.Fatalf("round-tripped record lost a snapshot: before=%v after=%v",
				round.Before, round.After)
		}
		if round.Before.Liveness[0].Verdict != rec.Before.Liveness[0].Verdict ||
			round.After.Liveness[0].Verdict != rec.After.Liveness[0].Verdict {
			t.Errorf("round-tripped record lost its liveness verdicts: before %q/%q after %q/%q",
				round.Before.Liveness[0].Verdict, rec.Before.Liveness[0].Verdict,
				round.After.Liveness[0].Verdict, rec.After.Liveness[0].Verdict)
		}
		if round.Before.At != tdnAtBefore || round.After.At != tdnAtAfter {
			t.Errorf("round-tripped snapshots lost the point they were taken at: %q / %q",
				round.Before.At, round.After.At)
		}
	})
}

// tdnFixtureRecord builds a record with every member populated, composed from
// the real classifiers rather than from literals: a redaction check over a
// record nobody populated proves nothing.
//
// It is a FIXTURE, not a coherent run: the held pids come from pinArgvFixture
// (rows 300 and 400, the `zsh -c` wrapper and its `cat`, one shared pgid) while
// the held pgid and the reap line come from this file's reap fixtures. Nothing
// here joins the two, and nothing needs to — the assertions it feeds are about
// redaction, disposition membership and the JSON round trip.
func tdnFixtureRecord() *tdnRecord {
	rec := &tdnRecord{
		Ticket:         tdnTicket,
		ClaudeVersion:  "2.1.220 (Claude Code)",
		TeardownPath:   "fixture: no teardown was performed",
		RunnerFromEnv:  reachRunnerPathFromEnv(nil),
		RunnerFromArgv: "fixture: no claude row was pinned",
		FIFOPath:       pinFixtureNeedle,
		PyryPID:        100,
		HeldPGID:       tdnFixtureHeldPGID,
		HeldPIDs:       []int{300, 400},
		Before: &tdnSnapshot{
			At: tdnAtBefore,
			ArgvScan: pinMatchArgvExcluding([]byte(pinArgvFixture), []string{pinFixtureNeedle},
				map[int]string{pinFixtureOwnPID: "the instrument's own test binary"}),
			Liveness: []pinStateOutcome{
				pinClassifyState(300, []byte("300 200 S\n"), nil),
				pinClassifyState(400, []byte("400 300 S\n"), nil),
			},
			FIFO: tdnFixtureReaderPresent(),
		},
		After: &tdnSnapshot{
			At: tdnAtAfter,
			// The needle is GONE from the after table: the kernel replaces a
			// defunct process's argv, and a dead pid has no row at all. That is
			// expected and carries no information, which is exactly why the
			// content re-match is dispositive only for a `running` verdict.
			ArgvScan: pinMatchArgvExcluding([]byte(pinArgvFixture), []string{"/tmp/run-p/gone"}, nil),
			Liveness: []pinStateOutcome{
				pinClassifyState(300, []byte("300 200 Z\n"), nil),
				pinClassifyState(400, []byte("400 300 Z\n"), nil),
			},
			FIFO: tdnFixtureNoReader(),
		},
		Reap: tdnClassifyReapLog([]byte(tdnFixtureDefaultTwo), tdnFixtureHeldPGID),
	}
	rec.decide(tdnDispositionReaperKilled, "fixture disposition, set through the real setter; "+
		"no live turn was taken and no claim is made about pyry")
	rec.note("fixture record for %s's redaction self-check; no live turn was taken", tdnTicket)
	return rec
}

// tdnFixtureNoReader is the after point's FIFO reading, from #1239's own pure
// constructor.
func tdnFixtureNoReader() fifoLiveOutcome {
	out := fifoLiveClassifyOpenErr(syscall.ENXIO)
	out.Path = "/tmp/pyry-1250-fixture/teardown-hold"
	out.Mode = os.ModeNamedPipe.String()
	return out
}

// tdnFixtureReaderPresent is the before point's FIFO reading.
//
// It is the ONE member of this fixture built by literal rather than by calling
// a classifier, and the reason is structural: fifoLiveRead's positive arm is
// reached only by an open(2) that succeeds, so there is no pure constructor for
// it — producing one honestly would mean staging a live FIFO with a live reader,
// which this offline file deliberately does not do. Nothing gates on it: the
// FIFO readings are corroboration in #1251's record and never decide a verdict.
func tdnFixtureReaderPresent() fifoLiveOutcome {
	return fifoLiveOutcome{
		Verdict: fifoLiveReaderPresent,
		Detail: "fixture: open(…, O_WRONLY|O_NONBLOCK) succeeded, so a process held the " +
			"read end at the before point",
		Path: "/tmp/pyry-1250-fixture/teardown-hold",
		Mode: os.ModeNamedPipe.String(),
	}
}

// --- test helpers ------------------------------------------------------------

// tdnIsReapVerdict reports whether v is one of the recorded values.
func tdnIsReapVerdict(v string) bool {
	switch v {
	case tdnReapHeldPGIDKilled, tdnReapHeldPGIDAbsent, tdnReapNoLine, tdnReapInstrumentFailed:
		return true
	}
	return false
}

// tdnIsDisposition reports whether v is one of the recorded dispositions. It
// mirrors tdnIsReapVerdict and exists for the same reason: a disposition string
// nobody can look up is a verdict a reader of the published artifact cannot
// interpret.
func tdnIsDisposition(v string) bool {
	switch v {
	case tdnDispositionReaperKilled, tdnDispositionDeadUnattributed,
		tdnDispositionLeaked, tdnDispositionSkipped:
		return true
	}
	return false
}

func tdnEqualInts(got, want []int) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range want {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
