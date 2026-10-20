//go:build e2e_realclaude

package realclaude

import (
	"encoding/json"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/e2e/internal/claudetape"
)

// Observations supply no authority and contain no native bytes or arbitrary names.
func observeResumeSeal(t *testing.T, c lifecycleRecordingConfig, stage string, start time.Time) {
	t.Helper()
	type process struct {
		Role                    string
		OwnerPID, BrokerPID     int
		OwnerState, BrokerState string
		Prefix, Receipt         string
	}
	type artifact struct {
		Name, State string
	}
	state := func(pid int) string {
		if pid <= 1 {
			return "unknown"
		}
		switch syscall.Kill(pid, 0) {
		case nil:
			return "present"
		case syscall.ESRCH:
			return "absent"
		default:
			return "unknown"
		}
	}
	ps := []process{}
	expected := map[string]bool{}
	for _, role := range []string{"bootstrap", "resumed"} {
		prefix := filepath.Join(c.Process.Home, "recording-children", role)
		job, _ := readLifecycleJob(prefix + ".job")
		var broker int
		_ = claudetape.ReadLifecycleControl(prefix+".broker-pid", 16384, &broker)
		p := process{Role: role, OwnerPID: job.Config.OwnerPID, BrokerPID: broker, OwnerState: state(job.Config.OwnerPID), BrokerState: state(broker)}
		for _, suffix := range []string{".prefix", ".receipt"} {
			var receipt claudetape.LifecycleReceipt
			value := "unknown"
			if _, err := os.Lstat(prefix + suffix); os.IsNotExist(err) {
				value = "absent"
			} else if claudetape.ReadLifecycleControl(prefix+suffix, 16384, &receipt) == nil {
				value = "failed"
				if receipt.Success && receipt.OwnerPID == p.OwnerPID && receipt.BrokerPID == p.BrokerPID {
					value = "success"
				}
			}
			if suffix == ".prefix" {
				p.Prefix = value
			} else {
				p.Receipt = value
			}
		}
		ps = append(ps, p)
		suffixes := []string{".claim", ".json", ".job", ".broker-pid", ".user-000001"}
		if role == "bootstrap" {
			suffixes = append(suffixes, ".receipt", ".stopped", ".action", ".action.ready")
		} else {
			suffixes = append(suffixes, ".prefix")
		}
		for _, suffix := range suffixes {
			expected[role+suffix] = true
		}
	}
	artifacts := []artifact{}
	entries, err := os.ReadDir(filepath.Join(c.Process.Home, "recording-children"))
	unknown := 0
	if err != nil {
		unknown = -1
	}
	for i, entry := range entries {
		if i >= 64 {
			unknown++
			continue
		}
		name := entry.Name()
		// Recognize terminal and pending states without treating them as allowed.
		known := expected[name] || name == "resumed.receipt"
		for base := range expected {
			known = known || name == base+".pending"
		}
		if !known {
			unknown++
			continue
		}
		info, e := entry.Info()
		s := "unknown"
		if e == nil {
			s = info.Mode().String()
		}
		artifacts = append(artifacts, artifact{name, s})
	}
	raw, _ := json.Marshal(struct {
		Stage        string
		ElapsedNS    int64
		Processes    []process
		Expected     map[string]bool
		Observed     []artifact
		UnknownNames int
	}{stage, time.Since(start).Nanoseconds(), ps, expected, artifacts, unknown})
	t.Logf("resume-seal observation=%s", raw)
}
