//go:build e2e_realclaude

package realclaude

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

type readinessStartupEvent struct {
	At           time.Time `json:"at"`
	ChildKey     string    `json:"-"`
	ChildOrdinal int       `json:"child_ordinal"`
	RestartEpoch int       `json:"restart_epoch"`
	Role         string    `json:"role"`
	Phase        string    `json:"phase"`
}

type readinessRequestEvent struct {
	At                                                                time.Time `json:"at"`
	Phase, Type, Device, Method                                       string
	RequestOrdinal, ReplyToOrdinal, ConversationOrdinal, RestartEpoch int
	OwnerOrdinal, SessionOrdinal                                      int    `json:",omitempty"`
	Outcome                                                           string `json:",omitempty"`
}

type readinessNamedResult struct {
	Name, Status, Reason string
	Started, Ended       time.Time `json:",omitzero"`
	DurationSeconds      *float64  `json:",omitempty"`
	Clock                string    `json:",omitempty"`
}

type readinessRuntimeManifest struct {
	RunID, SourceRevision, Checkpoint, Conclusion string
	Dirty                                         bool
	SHA256                                        map[string]string
	SourceSHA256                                  map[string]string
	Counts                                        map[string]int
}

func TestReadinessRuntimeManifest(t *testing.T) {
	r := &readinessCapture{Arm: "immediate-fresh", DaemonCommit: strings.Repeat("a", 40), Setup: map[string]any{}, Counts: map[string]int{"executed": 1, "passed": 1, "failed": 0, "skipped": 0}, Results: []readinessNamedResult{{Name: "target", Status: "pass"}}}
	dir := filepath.Join(t.TempDir(), "run")
	if err := readinessExportRuntime(dir, "test-run", r, nil); err != nil {
		t.Fatal(err)
	}
	if err := readinessValidateRuntime(dir); err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []string{"changed", "missing", "unpinned"} {
		t.Run(scenario, func(t *testing.T) {
			path := filepath.Join(dir, "request-events.jsonl")
			original, _ := os.ReadFile(path)
			defer os.WriteFile(path, original, 0600)
			switch scenario {
			case "changed":
				os.WriteFile(path, []byte("{}\n"), 0600)
			case "missing":
				os.Remove(path)
			case "unpinned":
				extra := filepath.Join(dir, "extra.json")
				os.WriteFile(extra, []byte("{}"), 0600)
				defer os.Remove(extra)
			}
			if readinessValidateRuntime(dir) == nil {
				t.Fatal("invalid runtime accepted")
			}
		})
	}
}

func TestReadinessRuntimeDenyScan(t *testing.T) {
	r := &readinessCapture{Setup: map[string]any{"unexpected": "private-payload"}}
	if readinessExportRuntime(filepath.Join(t.TempDir(), "run"), "test-run", r, []string{"private-payload"}) == nil {
		t.Fatal("private value exported")
	}
}

func readinessResults(r *readinessCapture) {
	// Only terminally executed leaves contribute to counts. Planned but unreached
	// methods remain incomplete, rather than invented skips or replacement retries.
	r.Results = nil
	if len(r.Replay) > 0 && r.Setup["preceding_methods"] != 0 {
		for _, row := range r.Replay {
			status, reason := "incomplete", "method not reached"
			if row.Counts["executed"] > 0 {
				status, reason = "fail", "capture or experiment failure"
				if row.Counts["passed"] > 0 {
					status, reason = "pass", ""
				}
			}
			r.Results = append(r.Results, readinessNamedResult{Name: r.Test + "/" + row.Method, Status: status, Reason: reason})
		}
		if r.TargetCounts["executed"] > 0 {
			status := "fail"
			if r.TargetCounts["passed"] > 0 {
				status = "pass"
			}
			r.Results = append(r.Results, readinessNamedResult{Name: r.Test + "/target", Status: status})
		} else {
			r.Results = append(r.Results, readinessNamedResult{Name: r.Test + "/target", Status: "incomplete", Reason: "arm terminated before target"})
		}
	} else {
		status := "fail"
		if r.Counts["passed"] > 0 {
			status = "pass"
		}
		if r.Counts["skipped"] > 0 {
			status = "skip"
		}
		r.Results = append(r.Results, readinessNamedResult{Name: r.Test, Status: status, Reason: r.FailureReason})
	}
}

func readinessExportRuntime(dir, runID string, r *readinessCapture, denied []string) error {
	childMap, epochMap := map[string]int{}, map[string]int{}
	for _, w := range r.Witnesses {
		if w.ChildKey != "" && w.ChildOrdinal > 0 {
			childMap[w.ChildKey], epochMap[w.ChildKey] = w.ChildOrdinal, w.RestartEpoch
		}
	}
	for i := range r.Startup {
		e := &r.Startup[i]
		e.ChildOrdinal, e.RestartEpoch = childMap[e.ChildKey], epochMap[e.ChildKey]
	}
	sort.SliceStable(r.Startup, func(i, j int) bool { return r.Startup[i].At.Before(r.Startup[j].At) })
	sort.SliceStable(r.Requests, func(i, j int) bool { return r.Requests[i].At.Before(r.Requests[j].At) })
	sort.SliceStable(r.Witnesses, func(i, j int) bool { return r.Witnesses[i].At.Before(r.Witnesses[j].At) })
	setup := map[string]any{"configuration": r.Setup, "arm": r.Arm, "claude_version": r.ClaudeVersion, "target_turns": r.Turns, "replay_methods": r.Replay, "history": r.PrefixHistory, "kernel_arrival": "unknown"}
	files := map[string][]byte{}
	if r.emulatorXML != nil {
		files["emulator-results.xml"] = r.emulatorXML
	}
	encode := func(name string, value any) error {
		b, e := json.Marshal(value)
		files[name] = append(b, '\n')
		return e
	}
	if err := encode("setup.json", setup); err != nil {
		return err
	}
	if err := encode("named-results.json", r.Results); err != nil {
		return err
	}
	streams := map[string]any{"request-events.jsonl": r.Requests, "startup-events.jsonl": r.Startup, "boundary-events.jsonl": r.Witnesses, "resource-samples.jsonl": r.Resources}
	for name, value := range streams {
		// Marshal each element independently to preserve an append-stream contract.
		b, e := json.Marshal(value)
		if e != nil {
			return e
		}
		var rows []json.RawMessage
		if e = json.Unmarshal(b, &rows); e != nil {
			return e
		}
		var out bytes.Buffer
		for _, row := range rows {
			out.Write(row)
			out.WriteByte('\n')
		}
		files[name] = out.Bytes()
	}
	manifest := readinessRuntimeManifest{RunID: runID, SourceRevision: r.DaemonCommit, Checkpoint: "255e86ae643c13f7bd36affb99b4ae786ad5ee95", Conclusion: r.Conclusion, Dirty: r.Dirty, SHA256: map[string]string{}, SourceSHA256: map[string]string{}, Counts: r.Counts}
	for _, name := range []string{"interactive_stream_queued_turn_readiness_test.go", "queued_turn_readiness_prefix_test.go", "queued_turn_readiness_effects_test.go", "queued_turn_readiness_dependency_test.go", "queued_turn_readiness_runtime_test.go", "queued_turn_readiness_emulator_test.go", "../../streamsup/readiness_observation_live.go", "../../dispatch/dispatch.go", "queued_turn_readiness_emulator_join_test.go", "queued_turn_readiness_emulator_semantics_test.go", "queued_turn_readiness_emulator_sampling_test.go", "queued_turn_readiness_emulator_contract_test.go"} {
		if b, e := os.ReadFile(name); e == nil {
			manifest.SourceSHA256[name] = fmt.Sprintf("%x", sha256.Sum256(b))
		}
	}
	for name, b := range files {
		manifest.SHA256[name] = fmt.Sprintf("%x", sha256.Sum256(b))
	}
	if err := encode("manifest.json", manifest); err != nil {
		return err
	}
	for _, b := range files {
		for _, value := range denied {
			if value != "" && bytes.Contains(b, []byte(value)) {
				return fmt.Errorf("runtime privacy scan failed")
			}
		}
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("runtime directory: %w", err)
	}
	for name, b := range files {
		path := filepath.Join(dir, name)
		if e := os.WriteFile(path+".tmp", b, 0600); e != nil {
			return e
		}
		if e := os.Rename(path+".tmp", path); e != nil {
			return e
		}
	}
	return readinessValidateRuntime(dir)
}

func readinessValidateRuntime(dir string) error {
	b, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return fmt.Errorf("runtime manifest missing")
	}
	var m readinessRuntimeManifest
	if json.Unmarshal(b, &m) != nil || m.RunID == "" || len(m.SourceRevision) != 40 || (len(m.SHA256) != 6 && len(m.SHA256) != 7) {
		return fmt.Errorf("runtime provenance incomplete")
	}
	for name := range m.SHA256 {
		if filepath.Base(name) != name || (strings.HasSuffix(name, ".xml") && name != "emulator-results.xml") {
			return fmt.Errorf("invalid runtime pin")
		}
	}
	for _, name := range []string{"setup.json", "request-events.jsonl", "startup-events.jsonl", "boundary-events.jsonl", "resource-samples.jsonl", "named-results.json"} {
		b, e := os.ReadFile(filepath.Join(dir, name))
		if e != nil || m.SHA256[name] != fmt.Sprintf("%x", sha256.Sum256(b)) {
			return fmt.Errorf("runtime file missing or changed: %s", name)
		}
		if strings.HasSuffix(name, ".json") {
			if !json.Valid(b) {
				return fmt.Errorf("runtime JSON invalid")
			}
		} else {
			for _, row := range bytes.Split(bytes.TrimSpace(b), []byte{'\n'}) {
				if len(row) > 0 && !json.Valid(row) {
					return fmt.Errorf("runtime stream invalid")
				}
			}
		}
	}
	if hash, ok := m.SHA256["emulator-results.xml"]; ok {
		b, e := os.ReadFile(filepath.Join(dir, "emulator-results.xml"))
		if e != nil || readinessHash(b) != hash {
			return fmt.Errorf("emulator XML missing or changed")
		}
	}
	for name := range m.SHA256 {
		if _, e := os.Stat(filepath.Join(dir, name)); e != nil {
			return fmt.Errorf("pinned runtime file missing")
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || (entry.Name() != "manifest.json" && m.SHA256[entry.Name()] == "") {
			return fmt.Errorf("unpinned runtime file")
		}
	}
	if m.RunID == readinessEmulatorRun || m.SHA256["emulator-results.xml"] != "" {
		return readinessValidateEmulator(dir, m)
	}
	return nil
}

func TestReadinessRuntimeRetained(t *testing.T) {
	dirs, err := filepath.Glob(filepath.Join("testdata", "queued_turn_readiness", "runtime", "*"))
	if err != nil || len(dirs) == 0 {
		t.Fatal("new runtime evidence missing")
	}
	contract, err := readinessReadEmulatorContract()
	if err != nil {
		t.Fatal(err)
	}
	if os.Getenv("PYRY_E2E_READINESS_REQUIRE_EMULATOR") == "1" || contract.Status == "executed; builder recovery required" || contract.Status == "recovered" {
		if err := readinessValidateRuntime(filepath.Join("testdata", "queued_turn_readiness", "runtime", readinessEmulatorRun)); err != nil {
			t.Fatal(err)
		}
	}
	for _, dir := range dirs {
		if err := readinessValidateRuntime(dir); err != nil {
			t.Fatal(err)
		}
	}
}

func readinessHash(b []byte) string { return fmt.Sprintf("%x", sha256.Sum256(b)) }
