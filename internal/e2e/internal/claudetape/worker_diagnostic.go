package claudetape

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

type lifecycleWorkerDiagnostic struct {
	Version           int                                `json:"version"`
	Scenario          string                             `json:"scenario"`
	InvalidUnpromoted bool                               `json:"invalid_unpromoted"`
	Roles             map[string]*LifecycleTape          `json:"roles"`
	Attempts          []lifecycleWorkerDiagnosticAttempt `json:"attempts"`
	Observations      []FallbackWorkerObservation        `json:"observations,omitempty"`
}
type lifecycleWorkerDiagnosticAttempt struct {
	Index                         int `json:"index"`
	OwnerPID, GroupPID, DaemonPID int
	JobPathBound                  bool                   `json:"job_path_bound"`
	Tape                          *FallbackWorkerTape    `json:"tape,omitempty"`
	Receipt                       *FallbackWorkerReceipt `json:"receipt,omitempty"`
}

// SaveLifecycleWorkerDiagnostic retains only already validated carriers and
// closed numeric ownership observations after a failed final inventory. It never
// copies environment, arbitrary paths, raw logs or an invalid provider carrier.
// This is explicitly invalid/unpromoted evidence, not a suite or coverage proof.
func SaveLifecycleWorkerDiagnostic(home, scenario, destination string, roles map[string]*LifecycleTape) error {
	parent, err := os.Lstat(filepath.Dir(destination))
	if err != nil || !parent.IsDir() || parent.Mode().Perm() != 0700 {
		return os.ErrInvalid
	}
	profile, ok := LifecycleProfiles()[scenario]
	if !ok || LifecycleWorkerLimit(scenario) == 0 || len(roles) != len(profile.Roles) {
		return os.ErrInvalid
	}
	for _, role := range profile.Roles {
		tape := roles[role]
		if tape == nil || tape.Validate() != nil || tape.Tape.Scenario != scenario+"-"+role {
			return os.ErrInvalid
		}
	}
	diagnostic := lifecycleWorkerDiagnostic{Version: 1, Scenario: scenario, InvalidUnpromoted: true, Roles: roles, Attempts: []lifecycleWorkerDiagnosticAttempt{}}
	// Unknown fields or malformed values are never copied as raw source bytes.
	_ = ReadLifecycleControl(filepath.Join(home, "recording-worker-observations.json"), 16384, &diagnostic.Observations)
	for i := 1; i <= LifecycleWorkerLimit(scenario); i++ {
		path := FallbackWorkerJobPath(home, i)
		if _, err := os.Lstat(path); os.IsNotExist(err) {
			continue
		}
		attempt := lifecycleWorkerDiagnosticAttempt{Index: i}
		var job FallbackWorkerJob
		if ReadLifecycleControl(path, 16384, &job) == nil && job.Home == home && job.Scenario == scenario && job.Version == 1 && job.Path == strings.TrimSuffix(path, ".job")+".json" {
			attempt.JobPathBound = true
			attempt.OwnerPID = job.OwnerPID
			attempt.GroupPID = job.GroupPID
			attempt.DaemonPID = job.DaemonPID
		}
		// Derived closed paths only; a job cannot redirect this reader to another file.
		if raw, err := runnerPrivateRead(strings.TrimSuffix(path, ".job")+".json", MaxBytes); err == nil {
			if tape, err := DecodeFallbackWorker(bytes.NewReader(raw)); err == nil {
				attempt.Tape = tape
			}
		}
		var receipt FallbackWorkerReceipt
		if ReadLifecycleControl(strings.TrimSuffix(path, ".job")+".receipt", 16384, &receipt) == nil {
			// Do not copy an unverified receipt string into diagnostic output.
			receipt.TapeSHA256 = ""
			attempt.Receipt = &receipt
		}
		diagnostic.Attempts = append(diagnostic.Attempts, attempt)
	}
	raw, err := json.MarshalIndent(diagnostic, "", "  ")
	if err != nil || len(raw) > MaxBytes || Safe(raw) != nil {
		return os.ErrInvalid
	}
	f, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, writeErr := f.Write(append(raw, '\n'))
	closeErr := f.Close()
	if writeErr != nil {
		return writeErr
	}
	return closeErr
}
