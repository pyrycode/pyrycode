//go:build e2e_realclaude

package realclaude

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const readinessEmulatorRun = "3071-emulator-20261011"

type readinessEmulatorContract struct {
	RunID         string            `json:"run_id"`
	Status        string            `json:"status"`
	Source        string            `json:"daemon_revision"`
	Selection     []string          `json:"method_selection"`
	MobileSource  string            `json:"mobile_source"`
	MobileMerge   string            `json:"mobile_merge"`
	MobileTree    string            `json:"mobile_merged_tree"`
	TestHash      string            `json:"mobile_merged_test_sha256"`
	WrapperHash   string            `json:"wrapper_sha256"`
	AdmissionHash string            `json:"admission_implementation_sha256"`
	Support       map[string]string `json:"support_sha256"`
}

type readinessMethodInterval struct {
	Name, Outcome, Clock string
	Started, Ended       time.Time
}

// These fields are readbacks, not defaults inferred from the historical fixture.
// Nil values mean unknown. Configuration roles never include paths or contents.
type readinessEffectiveSettings struct {
	SessionOrdinal                           int
	OwnerOrdinal, RestartEpoch               int
	Model, PermissionMode, Provenance        string
	OperatorBypass, MCP, SkipDangerous       *bool
	IdleSeconds, SweepSeconds, RetentionDays *float64
	ActiveCap                                *int
	WorkspaceRole, MCPRole, HookRole         string
}

type readinessEmulatorProvenance struct {
	ClockComparison                                                                            string
	RunID, Source, MobileSource, MobileMerge, MobileTree, TestHash, WrapperHash, AdmissionHash string
	Clock                                                                                      string
	DeviceClockOffsetMillis                                                                    *float64
	ClockComparedAt                                                                            time.Time
	Methods                                                                                    []readinessMethodInterval
	Settings                                                                                   []readinessEffectiveSettings
	Targets                                                                                    []readinessEmulatorTarget
	// Independent categories prevent a fixture correction becoming host-cause proof.
	CaptureConclusion, FixtureConclusion, HostConclusion string
	TargetAcquisition                                    string
}

type readinessEmulatorTarget struct {
	OwnerOrdinal, RestartEpoch, ConversationOrdinal, SessionOrdinal, ChildOrdinal int
	EnqueueRestartEpoch, EnqueueSessionOrdinal, EnqueueChildOrdinal               int
	Enqueued, Delivered, Opened, Confirmed                                        time.Time
	PromptMatches, ReplyMatches, ExactlyOnce, CurrentChild                        *bool
	Within90Seconds                                                               *bool
}

func readinessReadEmulatorContract() (readinessEmulatorContract, error) {
	var c readinessEmulatorContract
	b, err := os.ReadFile(filepath.Join("testdata", "queued_turn_readiness", "emulator-capture.json"))
	if err != nil || json.Unmarshal(b, &c) != nil || c.RunID != readinessEmulatorRun || len(c.Selection) != 65 {
		return c, fmt.Errorf("emulator contract unavailable")
	}
	seen := map[string]bool{}
	for _, name := range c.Selection {
		if seen[name] {
			return c, fmt.Errorf("duplicate selected method")
		}
		seen[name] = true
	}
	return c, nil
}

func readinessEmulatorResults(suite *readinessXMLSuite, intervals []readinessMethodInterval, selection []string) ([]readinessNamedResult, map[string]int, error) {
	selected, cases, witnessed := map[string]bool{}, map[string]readinessXMLCase{}, map[string]readinessMethodInterval{}
	for _, name := range selection {
		if selected[name] {
			return nil, nil, fmt.Errorf("duplicate selected method")
		}
		selected[name] = true
	}
	for _, c := range suite.Cases {
		if !selected[c.Name] || c.Class != "de.pyryco.mobile.e2e.InteractiveStreamE2ETest" {
			return nil, nil, fmt.Errorf("unexpected Android method")
		}
		if _, ok := cases[c.Name]; ok {
			return nil, nil, fmt.Errorf("duplicate Android result")
		}
		cases[c.Name] = c
	}
	for _, m := range intervals {
		if !selected[m.Name] {
			return nil, nil, fmt.Errorf("unexpected runner method")
		}
		if _, ok := witnessed[m.Name]; ok {
			return nil, nil, fmt.Errorf("duplicate runner interval")
		}
		if m.Started.IsZero() || (!m.Ended.IsZero() && m.Ended.Before(m.Started)) || m.Clock != "runner-status-host-utc" {
			return nil, nil, fmt.Errorf("invalid runner interval clock or chronology")
		}
		witnessed[m.Name] = m
	}
	counts := map[string]int{"executed": 0, "passed": 0, "failed": 0, "skipped": 0, "incomplete": 0}
	var results []readinessNamedResult
	for _, name := range selection {
		r := readinessNamedResult{Name: name, Status: "incomplete", Reason: "selected method not completed"}
		if c, ok := cases[name]; ok {
			r.Status, r.Reason = "pass", ""
			if c.Failure != nil || c.Error != nil {
				r.Status, r.Reason = "fail", "assertion failure; raw message excluded"
			} else if c.Skipped != nil {
				r.Status, r.Reason = "skip", "runner reported skipped; raw message excluded"
			}
			if c.Time != "" {
				v, e := strconv.ParseFloat(c.Time, 64)
				if e != nil || v < 0 || math.IsInf(v, 0) || math.IsNaN(v) {
					return nil, nil, fmt.Errorf("invalid XML duration")
				}
				r.DurationSeconds = &v
			}
		}
		if m, ok := witnessed[name]; ok {
			r.Started, r.Ended, r.Clock = m.Started, m.Ended, m.Clock
			if _, available := cases[name]; !available && m.Outcome != "incomplete" {
				r.Status, r.Reason = m.Outcome, "runner terminal status; XML unavailable"
				c := readinessXMLCase{Name: name, Class: "de.pyryco.mobile.e2e.InteractiveStreamE2ETest"}
				switch m.Outcome {
				case "pass":
				case "fail":
					c.Failure = &struct{}{}
				case "skip":
					c.Skipped = &struct{}{}
				default:
					return nil, nil, fmt.Errorf("unknown runner outcome")
				}
				suite.Cases = append(suite.Cases, c)
				r.Reason = ""
				if r.Status == "fail" {
					r.Reason = "assertion failure; raw message excluded"
				}
				if r.Status == "skip" {
					r.Reason = "runner reported skipped; raw message excluded"
				}
			}
			if m.Outcome != "incomplete" && m.Outcome != r.Status {
				return nil, nil, fmt.Errorf("runner/XML outcomes disagree")
			}
			if m.Outcome == "incomplete" && r.Status != "incomplete" {
				return nil, nil, fmt.Errorf("terminal XML without witnessed runner completion")
			}
			if r.Status != "incomplete" && m.Ended.IsZero() {
				return nil, nil, fmt.Errorf("runner completion missing")
			}
		} else if r.Status != "incomplete" {
			return nil, nil, fmt.Errorf("executed method lacks runner interval")
		}
		switch r.Status {
		case "pass":
			counts["passed"]++
			counts["executed"]++
		case "fail":
			counts["failed"]++
			counts["executed"]++
		case "skip":
			counts["skipped"]++
		case "incomplete":
			counts["incomplete"]++
		}
		results = append(results, r)
	}
	return results, counts, nil
}

func readinessValidateEmulator(dir string, m readinessRuntimeManifest) error {
	c, err := readinessReadEmulatorContract()
	if err != nil {
		return err
	}
	if m.RunID != c.RunID || m.SourceRevision != c.Source || m.Dirty || len(m.SHA256) != 7 || m.Counts["executed"] == 0 {
		return fmt.Errorf("emulator source, selection or execution missing")
	}
	for name, hash := range c.Support {
		if m.SourceSHA256[name] != hash {
			return fmt.Errorf("exporter/reader source differs from contract")
		}
	}
	var setup struct {
		Arm           string `json:"arm"`
		Configuration struct {
			Evidence   readinessEmulatorProvenance `json:"emulator_evidence"`
			BinaryHash string                      `json:"instrumented_binary_sha256"`
		} `json:"configuration"`
	}
	b, _ := os.ReadFile(filepath.Join(dir, "setup.json"))
	if json.Unmarshal(b, &setup) != nil || setup.Arm != "emulator" || len(setup.Configuration.BinaryHash) != 64 {
		return fmt.Errorf("emulator setup missing")
	}
	p := setup.Configuration.Evidence
	if p.RunID != c.RunID || p.Source != c.Source || p.MobileSource != c.MobileSource || p.MobileMerge != c.MobileMerge || p.MobileTree != c.MobileTree || p.TestHash != c.TestHash || p.WrapperHash != c.WrapperHash || p.AdmissionHash != c.AdmissionHash || p.Clock != "runner-status-host-utc" || p.ClockComparedAt.IsZero() {
		return fmt.Errorf("emulator acquisition provenance differs from contract")
	}
	if p.ClockComparison != "host-bracketed-adb-date" && p.ClockComparison != "device-clock-unavailable" {
		return fmt.Errorf("clock comparison provenance unavailable")
	}
	if p.ClockComparison == "host-bracketed-adb-date" && p.DeviceClockOffsetMillis == nil {
		return fmt.Errorf("clock comparison offset missing")
	}
	if len(p.Settings) == 0 {
		return fmt.Errorf("effective setup or persisted target unavailable")
	}
	for _, s := range p.Settings {
		if s.OwnerOrdinal <= 0 || s.RestartEpoch < 0 || (s.Provenance != "runtime-and-persistence-readback" && s.Provenance != "persistence-only") {
			return fmt.Errorf("settings ownership/readback missing")
		}
		if !readinessRole(s.WorkspaceRole) || !readinessRole(s.MCPRole) || !readinessRole(s.HookRole) {
			return fmt.Errorf("configuration role invalid")
		}
		if s.Model != "" && !regexp.MustCompile(`^(sonnet|opus|haiku|claude-(sonnet|opus|haiku)-[a-z0-9.-]+)(\[1m\])?$`).MatchString(s.Model) {
			return fmt.Errorf("unsafe model label")
		}
		switch s.PermissionMode {
		case "", "default", "acceptEdits", "plan", "dontAsk", "bypassPermissions":
		default:
			return fmt.Errorf("unsafe posture label")
		}
	}
	if p.CaptureConclusion != "usable" || p.FixtureConclusion != "unknown" && p.FixtureConclusion != "expected waiting" || p.HostConclusion != "unknown" && p.HostConclusion != "non-reproduction" && p.HostConclusion != "demonstrated defect" {
		return fmt.Errorf("diagnostic conclusions invalid")
	}
	var results []readinessNamedResult
	b, _ = os.ReadFile(filepath.Join(dir, "named-results.json"))
	if json.Unmarshal(b, &results) != nil {
		return fmt.Errorf("named results unreadable")
	}
	var suite readinessXMLSuite
	b, _ = os.ReadFile(filepath.Join(dir, "emulator-results.xml"))
	if xml.Unmarshal(b, &suite) != nil {
		return fmt.Errorf("emulator XML unreadable")
	}
	expected, counts, err := readinessEmulatorResults(&suite, p.Methods, c.Selection)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(results, expected) || !reflect.DeepEqual(counts, m.Counts) {
		return fmt.Errorf("named inventory/count contradiction")
	}
	var requests []readinessRequestEvent
	var boundaries []readinessWitness
	if err := readinessStream(dir, "request-events.jsonl", &requests); err != nil {
		return err
	}
	if err := readinessStream(dir, "boundary-events.jsonl", &boundaries); err != nil {
		return err
	}
	requestTimes := map[int]readinessRequestEvent{}
	for _, r := range requests {
		if r.OwnerOrdinal <= 0 || r.At.IsZero() || r.RestartEpoch < 0 {
			return fmt.Errorf("request ownership unknown")
		}
		if r.Phase == "host_received" && r.RequestOrdinal > 0 {
			requestTimes[r.RequestOrdinal] = r
		}
		if r.Phase == "outbound_queued" && r.ReplyToOrdinal > 0 {
			q, ok := requestTimes[r.ReplyToOrdinal]
			if !ok || r.At.Before(q.At) || q.OwnerOrdinal != r.OwnerOrdinal || q.RestartEpoch != r.RestartEpoch || q.Device != r.Device {
				return fmt.Errorf("reply ownership/chronology contradiction")
			}
		}
	}
	for _, w := range boundaries {
		if w.OwnerOrdinal <= 0 || w.At.IsZero() {
			return fmt.Errorf("boundary ownership missing")
		}
	}
	if p.TargetAcquisition != "persisted target" && p.TargetAcquisition != "not reached" && p.TargetAcquisition != "enqueue unwitnessed" {
		return fmt.Errorf("target acquisition state missing")
	}
	if (p.TargetAcquisition == "persisted target") != (len(p.Targets) > 0) {
		return fmt.Errorf("target acquisition contradiction")
	}
	var resources []struct {
		At           time.Time `json:"at"`
		OwnerOrdinal int       `json:"owner_ordinal"`
		RestartEpoch int       `json:"restart_epoch"`
		BinarySHA256 string    `json:"binary_sha256"`
	}
	if err := readinessStream(dir, "resource-samples.jsonl", &resources); err != nil {
		return err
	}
	if len(resources) == 0 {
		return fmt.Errorf("contemporaneous owned resource samples missing")
	}
	for _, sample := range resources {
		if sample.OwnerOrdinal <= 0 || sample.At.IsZero() || sample.RestartEpoch < 0 || sample.BinarySHA256 != setup.Configuration.BinaryHash {
			return fmt.Errorf("resource/binary ownership invalid")
		}
	}
	for _, t := range p.Targets {
		sampled := false
		for _, sample := range resources {
			if sample.OwnerOrdinal == t.OwnerOrdinal && sample.RestartEpoch == t.EnqueueRestartEpoch && sample.At.Sub(t.Enqueued) >= -5*time.Second && sample.At.Sub(t.Enqueued) <= 5*time.Second {
				sampled = true
			}
		}
		if !sampled {
			return fmt.Errorf("target contemporaneous resources missing")
		}
		effective := false
		for _, s := range p.Settings {
			if s.OwnerOrdinal == t.OwnerOrdinal && s.RestartEpoch == t.RestartEpoch && s.SessionOrdinal == t.SessionOrdinal && s.Provenance == "runtime-and-persistence-readback" && s.Model != "" && s.PermissionMode != "" && s.OperatorBypass != nil && s.MCP != nil && s.SkipDangerous != nil && s.IdleSeconds != nil && s.SweepSeconds != nil && s.RetentionDays != nil && s.ActiveCap != nil {
				effective = true
			}
		}
		if !effective {
			return fmt.Errorf("target effective configuration readback incomplete")
		}
		if t.OwnerOrdinal <= 0 || t.ConversationOrdinal <= 0 || t.Enqueued.IsZero() {
			return fmt.Errorf("target persisted enqueue missing")
		}
		if !t.Delivered.IsZero() && t.Delivered.Before(t.Enqueued) || !t.Opened.IsZero() && (t.Delivered.IsZero() || t.Opened.Before(t.Delivered)) {
			return fmt.Errorf("target persistence chronology contradiction")
		}
		for boundary, at := range map[string]time.Time{"target_enqueued": t.Enqueued, "durable_target_delivery": t.Delivered, "target_main_turn_opened": t.Opened} {
			if at.IsZero() {
				continue
			}
			found := false
			epoch, session, child := t.RestartEpoch, t.SessionOrdinal, t.ChildOrdinal
			if boundary == "target_enqueued" {
				epoch, session, child = t.EnqueueRestartEpoch, t.EnqueueSessionOrdinal, t.EnqueueChildOrdinal
			}
			for _, w := range boundaries {
				if w.OwnerOrdinal == t.OwnerOrdinal && w.RestartEpoch == epoch && w.SessionOrdinal == session && w.ChildOrdinal == child && w.Ordinal == t.ConversationOrdinal && w.Boundary == boundary && w.At.Equal(at) && w.OK {
					found = true
				}
			}
			if !found {
				return fmt.Errorf("target persisted boundary missing or contradictory")
			}
		}
		if t.CurrentChild != nil && *t.CurrentChild {
			if t.SessionOrdinal <= 0 || t.ChildOrdinal <= 0 || t.Confirmed.IsZero() || t.Delivered.IsZero() || t.Confirmed.After(t.Delivered) {
				return fmt.Errorf("current-child confirmation missing")
			}
			witnessed := false
			for _, w := range boundaries {
				if w.OwnerOrdinal == t.OwnerOrdinal && w.RestartEpoch == t.RestartEpoch && w.SessionOrdinal == t.SessionOrdinal && w.ChildOrdinal == t.ChildOrdinal && w.Boundary == "gate_released" && w.OK && w.At.Equal(t.Confirmed) {
					witnessed = true
				}
			}
			if !witnessed {
				return fmt.Errorf("target confirmation belongs to another child")
			}
			var started time.Time
			for _, w := range boundaries {
				if w.OwnerOrdinal == t.OwnerOrdinal && w.RestartEpoch == t.RestartEpoch && w.SessionOrdinal == t.SessionOrdinal && w.ChildOrdinal == t.ChildOrdinal && w.Boundary == "child_started" && w.OK && !w.At.After(t.Confirmed) && (started.IsZero() || w.At.After(started)) {
					started = w.At
				}
			}
			for _, w := range boundaries {
				if w.OwnerOrdinal != t.OwnerOrdinal || w.RestartEpoch != t.RestartEpoch || w.SessionOrdinal != t.SessionOrdinal || w.At.After(t.Delivered) || w.At.Before(started) {
					continue
				}
				if w.Boundary == "child_started" && w.OK && w.ChildOrdinal != t.ChildOrdinal || w.Boundary == "child_wait_complete" && w.ChildOrdinal == t.ChildOrdinal {
					return fmt.Errorf("confirmed child retired before delivery")
				}
			}
			chain := map[string]time.Time{}
			for _, w := range boundaries {
				if w.OwnerOrdinal == t.OwnerOrdinal && w.RestartEpoch == t.RestartEpoch && w.SessionOrdinal == t.SessionOrdinal && w.ChildOrdinal == t.ChildOrdinal && w.OK && !w.At.After(t.Delivered) && !w.At.Before(started) {
					chain[w.Boundary] = w.At
				}
			}
			for _, boundary := range []string{"child_started", "permission_write", "ack_forwarder_line", "ack_stdout_line", "ack_processing", "gate_released"} {
				if chain[boundary].IsZero() {
					return fmt.Errorf("current-child admission observation missing")
				}
			}
			if !chain["gate_released"].Equal(t.Confirmed) {
				return fmt.Errorf("target confirmation chain mismatch")
			}
			// Stdin completion and stdout forwarding use independent goroutines. An
			// acknowledgment can be observed before the permission writer returns.
			previous := chain["child_started"]
			for _, boundary := range []string{"ack_forwarder_line", "ack_stdout_line", "ack_processing", "gate_released"} {
				if chain[boundary].Before(previous) {
					return fmt.Errorf("stdout acknowledgment chronology contradiction")
				}
				previous = chain[boundary]
			}
			if chain["permission_write"].Before(chain["child_started"]) {
				return fmt.Errorf("permission write precedes successful Start")
			}

		}
		if t.Within90Seconds != nil {
			if t.Delivered.IsZero() || t.Opened.IsZero() || *t.Within90Seconds != (t.Delivered.Sub(t.Enqueued) <= 90*time.Second && t.Opened.Sub(t.Enqueued) <= 90*time.Second) {
				return fmt.Errorf("target deadline comparison contradiction")
			}
		}
	}
	return nil
}

func readinessRole(s string) bool {
	return s == "unknown" || s == "test-owned-empty" || s == "test-owned-substitute" || s == "witnessed-configured"
}

func readinessStream(dir, name string, out any) error {
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return err
	}
	rows := bytes.Split(bytes.TrimSpace(b), []byte{'\n'})
	if len(bytes.TrimSpace(b)) == 0 {
		rows = nil
	}
	b = append([]byte{'['}, bytes.Join(rows, []byte{','})...)
	b = append(b, ']')
	if json.Unmarshal(b, out) != nil {
		return fmt.Errorf("invalid evidence stream")
	}
	return nil
}

// Instrumentation status witnesses method boundaries; XML order is never timing.
// At is the admitted runner's UTC receipt time, distinct from a device clock.
func readinessRunnerIntervals(raw []byte, selection []string) ([]readinessMethodInterval, error) {
	type statusLine struct {
		At   time.Time `json:"at"`
		Line string    `json:"line"`
	}
	lines, err := readinessPrivateRows[statusLine](raw)
	if err != nil {
		return nil, err
	}
	selected := map[string]bool{}
	for _, s := range selection {
		selected[s] = true
	}
	name, class := "", ""
	active := map[string]int{}
	seen := map[string]bool{}
	var methods []readinessMethodInterval
	var last time.Time
	for _, line := range lines {
		if line.At.IsZero() || !last.IsZero() && line.At.Before(last) {
			return nil, fmt.Errorf("runner status clock moved backward")
		}
		last = line.At
		if value, ok := strings.CutPrefix(line.Line, "INSTRUMENTATION_STATUS: test="); ok {
			name = value
		}
		if value, ok := strings.CutPrefix(line.Line, "INSTRUMENTATION_STATUS: class="); ok {
			class = value
		}
		value, ok := strings.CutPrefix(line.Line, "INSTRUMENTATION_STATUS_CODE: ")
		if !ok {
			continue
		}
		code, e := strconv.Atoi(value)
		if e != nil || !selected[name] || class != "de.pyryco.mobile.e2e.InteractiveStreamE2ETest" {
			return nil, fmt.Errorf("unexpected instrumentation status identity")
		}
		if code == 1 {
			if seen[name] {
				return nil, fmt.Errorf("repeated method execution")
			}
			seen[name] = true
			active[name] = len(methods)
			methods = append(methods, readinessMethodInterval{Name: name, Started: line.At, Outcome: "incomplete", Clock: "runner-status-host-utc"})
		} else {
			index, ok := active[name]
			if !ok {
				return nil, fmt.Errorf("instrumentation completion without start")
			}
			status := ""
			switch code {
			case 0:
				status = "pass"
			case -1, -2:
				status = "fail"
			case -3, -4:
				status = "skip"
			default:
				return nil, fmt.Errorf("unknown instrumentation outcome")
			}
			methods[index].Ended, methods[index].Outcome = line.At, status
			delete(active, name)
		}
		name, class = "", ""
	}
	if len(methods) == 0 {
		return nil, fmt.Errorf("instrumentation executed no selected methods")
	}
	return methods, nil
}
