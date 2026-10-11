//go:build e2e_realclaude

package realclaude

import (
	"bytes"
	"context"
	"debug/buildinfo"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

type readinessEmulatorInput struct {
	RunID, SourceRevision, Binary, Observations, XML, Resources string
	Homes                                                       []string
}

type readinessPrivateObservation struct {
	At                                                   time.Time `json:"at"`
	Boundary, Subject, Child, Phase, Kind, Device, Owner string
	Value, Process                                       int
	Request                                              uint64 `json:"request"`
	ReplyTo                                              uint64 `json:"reply_to"`
	OK                                                   bool
}

type readinessXMLCase struct {
	Name    string    `xml:"name,attr"`
	Class   string    `xml:"classname,attr"`
	Time    string    `xml:"time,attr"`
	Failure *struct{} `xml:"failure,omitempty"`
	Error   *struct{} `xml:"error,omitempty"`
	Skipped *struct{} `xml:"skipped,omitempty"`
}

type readinessXMLSuite struct {
	XMLName  xml.Name           `xml:"testsuite"`
	Name     string             `xml:"name,attr"`
	Tests    int                `xml:"tests,attr"`
	Failures int                `xml:"failures,attr"`
	Skipped  int                `xml:"skipped,attr"`
	Cases    []readinessXMLCase `xml:"testcase"`
}

func readinessExportEmulator(input readinessEmulatorInput, dir string) error {
	if filepath.Base(input.RunID) != input.RunID || input.RunID == "." || len(input.SourceRevision) != 40 {
		return fmt.Errorf("invalid capture identity")
	}
	raw, err := os.ReadFile(input.Observations)
	if err != nil || len(raw) > 64<<20 {
		return fmt.Errorf("host observations unavailable")
	}
	x, err := os.ReadFile(input.XML)
	if err != nil || len(x) > 16<<20 {
		return fmt.Errorf("Android named results unavailable")
	}
	var suite readinessXMLSuite
	if xml.Unmarshal(x, &suite) != nil || len(suite.Cases) == 0 {
		return fmt.Errorf("Android named results unreadable")
	}
	r := &readinessCapture{DaemonCommit: input.SourceRevision, Arm: "emulator", Setup: map[string]any{"capture_kind": "actual-client diagnostic", "client_send_boundary": "unknown; host_received is after decrypted dispatch", "phone_receipt_projection": "unknown", "kernel_arrival": "unknown", "binary_revision_verified": true}, Counts: map[string]int{"executed": 0, "passed": 0, "failed": 0, "skipped": 0}, Conclusion: "actual-client diagnostic; localize before selecting a repair"}
	if input.Binary != "" {
		binary, e := os.ReadFile(input.Binary)
		if e != nil {
			return fmt.Errorf("tested binary unavailable")
		}
		r.Setup["instrumented_binary_sha256"] = readinessHash(binary)
		info, e := buildinfo.ReadFile(input.Binary)
		if e != nil {
			return fmt.Errorf("tested binary build metadata unavailable")
		}
		revision, tags, modified := "", "", false
		for _, s := range info.Settings {
			switch s.Key {
			case "vcs.revision":
				revision = s.Value
			case "-tags":
				tags = s.Value
			case "vcs.modified":
				modified = s.Value == "true"
			}
		}
		if revision != input.SourceRevision || modified || !strings.Contains(tags, "e2e_realclaude") {
			return fmt.Errorf("tested binary is not clean pinned instrumented source")
		}
	} else {
		return fmt.Errorf("tested binary missing")
	}
	denied := append(append([]string{}, input.Homes...), input.Observations, input.XML, os.Getenv("CLAUDE_CODE_OAUTH_TOKEN"), os.Getenv("ANTHROPIC_API_KEY"))
	conversations, children, devices, owners := map[string]int{}, map[string]int{}, map[string]int{}, map[string]int{}
	processEpoch := map[int]int{}
	ownerProcesses := map[string]map[int]int{}
	requestOrdinals := map[string]int{}
	for _, line := range bytes.Split(raw, []byte{'\n'}) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var o readinessPrivateObservation
		if json.Unmarshal(line, &o) != nil || o.At.IsZero() {
			return fmt.Errorf("host observation malformed")
		}
		if o.Owner == "" {
			o.Owner = "unknown"
		}
		if owners[o.Owner] == 0 {
			owners[o.Owner] = len(owners) + 1
			ownerProcesses[o.Owner] = map[int]int{}
		}
		processes := ownerProcesses[o.Owner]
		if _, ok := processes[o.Process]; !ok {
			processes[o.Process] = len(processes)
		}
		processEpoch[o.Process] = processes[o.Process]
		key := o.Owner + "/" + o.Subject
		if o.Subject != "" && conversations[key] == 0 {
			conversations[key] = len(conversations) + 1
		}
		if o.Child != "" && children[o.Child] == 0 {
			children[o.Child] = len(children) + 1
		}
		denied = append(denied, o.Subject, o.Child, o.Device)
		if o.Boundary != "" {
			r.Witnesses = append(r.Witnesses, readinessWitness{At: o.At, Boundary: o.Boundary, Ordinal: conversations[key], ChildOrdinal: children[o.Child], RestartEpoch: processEpoch[o.Process], OK: o.OK})
			continue
		}
		if o.Phase != "host_received" && o.Phase != "outbound_queued" {
			return fmt.Errorf("unknown request observation phase")
		}
		if devices[o.Device] == 0 {
			devices[o.Device] = len(devices) + 1
		}
		requestKey := o.Owner + "/" + o.Device + "/" + strconv.FormatUint(o.Request, 10)
		ordinal := 0
		if o.Phase == "host_received" {
			if requestOrdinals[requestKey] == 0 {
				requestOrdinals[requestKey] = len(requestOrdinals) + 1
			}
			ordinal = requestOrdinals[requestKey]
		}
		reply := requestOrdinals[o.Owner+"/"+o.Device+"/"+strconv.FormatUint(o.ReplyTo, 10)]
		r.Requests = append(r.Requests, readinessRequestEvent{At: o.At, Phase: o.Phase, Type: o.Kind, Device: fmt.Sprintf("connection-%d", devices[o.Device]), Method: "unattributed; XML does not establish request-method timing", RequestOrdinal: ordinal, ReplyToOrdinal: reply, ConversationOrdinal: conversations[key], RestartEpoch: processEpoch[o.Process]})
	}
	if len(r.Witnesses) == 0 || len(r.Requests) == 0 {
		return fmt.Errorf("host boundaries or request witnesses missing")
	}
	suite.Failures, suite.Skipped = 0, 0
	for i := range suite.Cases {
		c := &suite.Cases[i]
		if !strings.HasPrefix(c.Name, "interactiveTurn_") || strings.ContainsAny(c.Name, " /\\\n\r") || c.Class != "de.pyryco.mobile.e2e.InteractiveStreamE2ETest" {
			return fmt.Errorf("unexpected Android test identity")
		}
		status, reason := "pass", ""
		r.Counts["executed"]++
		if c.Failure != nil || c.Error != nil {
			status, reason = "fail", "assertion failure; raw message excluded"
			r.Counts["failed"]++
			suite.Failures++
		} else if c.Skipped != nil {
			status, reason = "skip", "Android reported skipped; raw message excluded"
			r.Counts["executed"]--
			r.Counts["skipped"]++
			suite.Skipped++
		} else {
			r.Counts["passed"]++
		}
		r.Results = append(r.Results, readinessNamedResult{Name: c.Name, Status: status, Reason: reason})
		c.Time = "" // Retain only separately witnessed experiment times.
	}
	suite.Name = "queued-turn-readiness"
	suite.Tests = len(suite.Cases)
	portable, e := xml.Marshal(suite)
	if e != nil {
		return e
	}
	r.Setup["complete_65_method_result_set"] = len(suite.Cases) == 65
	if input.Resources != "" {
		b, e := os.ReadFile(input.Resources)
		if e != nil {
			return fmt.Errorf("resource samples missing")
		}
		for _, line := range bytes.Split(b, []byte{'\n'}) {
			if len(line) == 0 {
				continue
			}
			var sample map[string]any
			if json.Unmarshal(line, &sample) != nil {
				return fmt.Errorf("resource sample invalid")
			}
			r.Resources = append(r.Resources, sample)
		}
	}
	if e := readinessExportRuntime(dir, input.RunID, r, denied); e != nil {
		return e
	}
	// The normalized XML retains every named outcome, excluding stdout, stack
	// traces, failure messages, properties and device-private metadata.
	for _, secret := range denied {
		if secret != "" && bytes.Contains(portable, []byte(secret)) {
			return fmt.Errorf("XML privacy scan failed")
		}
	}
	if e := os.WriteFile(filepath.Join(dir, "emulator-results.xml"), append(portable, '\n'), 0600); e != nil {
		return e
	}
	b, e := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if e != nil {
		return e
	}
	var m readinessRuntimeManifest
	if json.Unmarshal(b, &m) != nil {
		return fmt.Errorf("manifest unavailable")
	}
	m.SHA256["emulator-results.xml"] = readinessHash(append(portable, '\n'))
	b, e = json.Marshal(m)
	if e != nil {
		return e
	}
	if e = os.WriteFile(filepath.Join(dir, "manifest.json"), append(b, '\n'), 0600); e != nil {
		return e
	}
	return readinessValidateRuntime(dir)
}

// TestReadinessEmulatorExport is a dispatcher recovery entry point. It does not
// start an Android suite; the mobile admission wrapper owns that execution.
func TestReadinessEmulatorExport(t *testing.T) {
	inputPath := os.Getenv("PYRY_E2E_READINESS_EMULATOR_INPUT")
	if inputPath == "" {
		t.Skip("dispatcher Android capture export is not configured")
	}
	b, e := os.ReadFile(inputPath)
	if e != nil {
		t.Fatal("private capture input missing")
	}
	var input readinessEmulatorInput
	if json.Unmarshal(b, &input) != nil {
		t.Fatal("private capture input invalid")
	}
	for _, dir := range []string{filepath.Join("testdata", "queued_turn_readiness", "runtime", input.RunID), filepath.Join("/tmp/pyry-3071-live-records", input.RunID)} {
		if e := readinessExportEmulator(input, dir); e != nil {
			t.Fatal(e)
		}
	}
}

func TestReadinessEmulatorExportPrivacy(t *testing.T) {
	raw := []byte(`<testsuite name="private-path"><testcase classname="de.pyryco.mobile.e2e.InteractiveStreamE2ETest" name="interactiveTurn_pingPrompt_streamsPingReplyIntoThread"><failure message="private-payload">private-token</failure><system-out>private-content</system-out></testcase></testsuite>`)
	var suite readinessXMLSuite
	if xml.Unmarshal(raw, &suite) != nil {
		t.Fatal("fixture XML invalid")
	}
	b, e := xml.Marshal(suite)
	if e != nil || bytes.Contains(b, []byte("private-payload")) || bytes.Contains(b, []byte("private-token")) || bytes.Contains(b, []byte("private-content")) {
		t.Fatal("private Android failure exported")
	}
	if len(suite.Cases) != 1 || suite.Cases[0].Failure == nil {
		t.Fatal("failed named outcome lost")
	}
}

// TestReadinessEmulatorResources runs beside the dispatcher-admitted mobile gate
// until its test-owned completion sentinel or context deadline is observed.
func TestReadinessEmulatorResources(t *testing.T) {
	inputPath := os.Getenv("PYRY_E2E_READINESS_EMULATOR_INPUT")
	if inputPath == "" {
		t.Skip("dispatcher Android resource sampler is not configured")
	}
	b, e := os.ReadFile(inputPath)
	if e != nil {
		t.Fatal("private capture input missing")
	}
	var input readinessEmulatorInput
	if json.Unmarshal(b, &input) != nil || input.Resources == "" {
		t.Fatal("resource sampler input invalid")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 70*time.Minute)
	defer cancel()
	f, e := os.OpenFile(input.Resources, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if e != nil {
		t.Fatal("resource output unavailable")
	}
	defer f.Close()
	previous := map[int]uint64{}
	last := time.Now()
	for {
		if _, e := os.Stat(filepath.Join(filepath.Dir(inputPath), "capture-complete")); e == nil {
			return
		}
		raw, _ := os.ReadFile(input.Observations)
		processes := map[int]bool{}
		for _, line := range bytes.Split(raw, []byte{'\n'}) {
			var o readinessPrivateObservation
			if json.Unmarshal(line, &o) == nil && o.Boundary == "child_started" {
				processes[o.Process] = true
			}
		}
		now := time.Now()
		var ticks uint64
		intervalKnown := false
		for pid := range processes {
			sample := readinessResources(ctx, pid, input.Observations)
			if values, ok := sample["private_cpu_ticks"].(map[int]uint64); ok {
				for p, v := range values {
					if old, known := previous[p]; known && v >= old {
						ticks += v - old
						intervalKnown = true
					}
					previous[p] = v
				}
			}
			delete(sample, "private_cpu_ticks")
			sample["interval_cpu_known"], sample["interval_cpu_ticks"], sample["interval_seconds"] = intervalKnown, ticks, now.Sub(last).Seconds()
			if json.NewEncoder(f).Encode(sample) != nil {
				t.Fatal("resource write failed")
			}
		}
		last = now
		select {
		case <-ctx.Done():
			t.Fatal("resource sampler deadline before completion")
		case <-time.After(2 * time.Second):
		}
	}
}

func TestReadinessEmulatorRequestJoins(t *testing.T) {
	var o readinessPrivateObservation
	if json.Unmarshal([]byte(`{"request":4611686018427387921,"reply_to":4611686018427387921,"owner":"private-daemon"}`), &o) != nil || o.Request != 4611686018427387921 || o.ReplyTo != o.Request || o.Owner != "private-daemon" {
		t.Fatal("full-width request/reply ownership join lost")
	}
}
