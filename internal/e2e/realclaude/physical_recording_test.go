//go:build e2e_realclaude

package realclaude

import (
	"github.com/pyrycode/pyrycode/internal/e2e/internal/claudetape"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Declaration follows the original constructor and precedes the real phone
// source send. No captured input or witness is restored into this rig.
func declarePhysicalRecording(t *testing.T, home, kind string, sequence int, user string) {
	t.Helper()
	c, ok := lifecycleRecordingCurrent(t)
	if !ok {
		return
	}
	if c.Process.Home != home || claudetape.PhysicalFamily(c.Process.Scenario) == "" || strings.Contains(user, "${CHILD_HOME}") {
		t.Fatal("unowned physical declaration")
	}
	spec, err := claudetape.InspectPhysicalOperation(home, kind)
	if err != nil {
		t.Fatal(err)
	}
	binding, err := claudetape.ReadLifecycleRole(home, c.Process.Scenario, "bootstrap")
	if err != nil {
		t.Fatal(err)
	}
	prefix := filepath.Join(home, "recording-children", "bootstrap")
	// Phone handshake can finish before the bootstrap wrapper enters its
	// broker. The immutable broker PID is published only after the outer owner
	// has validated the complete job and kernel peer. Wait for that causal
	// publication, not for a provider response or a rig result witness.
	deadline := time.Now().Add(15 * time.Second)
	for {
		_, err := os.Lstat(prefix + ".broker-pid")
		if err == nil {
			break
		}
		if !os.IsNotExist(err) || !time.Now().Before(deadline) {
			t.Fatal("physical producer readiness timed out")
		}
		time.Sleep(10 * time.Millisecond)
	}
	var broker int
	if claudetape.ReadLifecycleControl(prefix+".broker-pid", 16384, &broker) != nil {
		t.Fatal("physical broker readiness is malformed")
	}
	job, err := readLifecycleJob(prefix + ".job")
	if err != nil {
		t.Fatal(err)
	}

	record := claudetape.PhysicalDeclarationRecord{Version: 1, Revision: poolRecordedRevision, SourceBinarySHA256: c.SourceBinarySHA256, OwnerPID: job.Config.OwnerPID, BrokerPID: broker, Declaration: claudetape.PhysicalOperationDeclaration{Version: 1, UserSequence: sequence, Owner: binding, Spec: spec, UserText: user}}
	if claudetape.ValidatePhysicalDeclarationRecord(record, binding, kind, poolRecordedRevision, c.SourceBinarySHA256, job.Config.OwnerPID, broker, sequence) != nil || claudetape.PublishLifecycleControl(claudetape.PhysicalDeclarationPath(prefix, sequence), record) != nil {
		t.Fatal("physical declaration changed or repeated")
	}
}
