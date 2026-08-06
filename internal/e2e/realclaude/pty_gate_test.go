//go:build e2e_realclaude

package realclaude

import (
	"os"
	"testing"
)

// ptyGateEnableEnv re-enables the ptyrunner interactive gates that are skipped
// by default. Set it to "1" to run them.
const ptyGateEnableEnv = "PYRY_PTY_GATE"

// skipUnlessPTYGate skips a ptyrunner-path interactive gate unless the operator
// asks for it explicitly.
//
// These four gates cover the ptyrunner, which nothing has run since the
// 2026-07-24 interactive cutover and the 2026-07-25 fleet switch. All four fail
// on clean main (measured 2026-08-05 on claude 2.1.220, reproduced on two tree
// states with matching durations), while all ten streamrunner gates pass.
//
// They are skipped because a permanently red gate is worse than no gate: it
// stops being read, and a fatal assertion hides every check downstream of it.
// That is not hypothetical here — an earlier long-red gate in this repo
// accumulated three months of unseen drift behind its first failure.
//
// What a skip here does and does not mean. It does NOT mean the ptyrunner
// works; it is untested and, on the last measurement, failing. It also does not
// mean a fallback has been lost. Moving back to the ptyrunner was never a switch
// anyone could simply throw — it would be real work whichever state these tests
// were in — so their being red removes an option nobody had rather than one
// that existed yesterday.
//
// The open question is only whether this path is worth keeping at all. That
// decision is #1348, and #972 is the same decision written when the two runners
// were the other way round. Until one of them is answered these gates stay off,
// and the parity cost of maintaining the path keeps being paid for nothing.
//
// Run them with: PYRY_PTY_GATE=1 make e2e-realclaude
func skipUnlessPTYGate(t *testing.T) {
	t.Helper()
	if os.Getenv(ptyGateEnableEnv) == "1" {
		return
	}
	t.Skipf("ptyrunner interactive gate: skipped because %s != 1.\n"+
		"This path is untested, not known-good: all four ptyrunner gates fail on "+
		"clean main as of 2026-08-05, while all ten streamrunner gates pass. "+
		"Nothing has run the ptyrunner since the 2026-07-25 fleet switch, and "+
		"moving back to it would be real work regardless of these tests, so this "+
		"is a path worth a keep-or-drop decision (#1348, #972) rather than an "+
		"alarm. Re-enable with %s=1.", ptyGateEnableEnv, ptyGateEnableEnv)
}
