//go:build e2e_realclaude

package realclaude

import (
	"reflect"
	"testing"
)

// The committed mobile record is admissible diagnostic evidence, while cause
// admission remains unsatisfied by this non-rejecting execution.
func TestToolThenTextMobileRetainedDiagnostic(t *testing.T) {
	t.Parallel()
	rec, err := tttReadMobileFixture("testdata")
	if err != nil {
		t.Fatal(err)
	}
	if rec.DaemonRevision != "7d1a4a6371aa72c072d20407bb85750c88a8e95c" ||
		rec.MobileRevision != "83a277afe045ddc734d513f47aad6aa6e2aeeb63" ||
		rec.ClaudeVersion != "2.1.280" || len(rec.Rows) != 48 {
		t.Fatal("retained mobile diagnostic provenance changed")
	}
	_, rejected := tttReplay(t, rec.Rows)
	if !reflect.DeepEqual(rejected, rec.RejectedRows) || len(rejected) != 0 {
		t.Fatal("retained mobile replay differs from its zero-rejection census")
	}
	if tttMobileProofComplete(rec) {
		t.Fatal("non-rejecting mobile evidence completed the cause requirement")
	}
}
