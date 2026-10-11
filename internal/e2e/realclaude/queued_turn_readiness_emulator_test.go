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
	"slices"
	"sort"
	"strings"
	"testing"
	"time"
)

type readinessEmulatorInput struct {
	RunID, SourceRevision, Binary, Observations, XML, Resources  string
	Evidence, Persistence, Readbacks, RunnerStatus, TargetPrompt string
	Homes                                                        []string
}

type readinessPrivateObservation struct {
	At                                                   time.Time `json:"at"`
	Boundary, Subject, Child, Phase, Kind, Device, Owner string
	Epoch, Outcome                                       string
	ProcessStart                                         string `json:"process_start"`
	ChildStart                                           string `json:"child_start"`
	OwnerHome                                            string `json:"owner_home"`
	Instance                                             string
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
	if _, e := os.Stat(filepath.Join(dir, "manifest.json")); e == nil {
		return fmt.Errorf("emulator run already exported; preserve executed evidence")
	} else if !os.IsNotExist(e) {
		return fmt.Errorf("emulator destination unavailable")
	}
	if filepath.Base(input.RunID) != input.RunID || input.RunID == "." || len(input.SourceRevision) != 40 {
		return fmt.Errorf("invalid capture identity")
	}
	raw, err := os.ReadFile(input.Observations)
	if err != nil || len(raw) > 64<<20 {
		return fmt.Errorf("host observations unavailable")
	}
	x, err := os.ReadFile(input.XML)
	if err != nil && !os.IsNotExist(err) || len(x) > 16<<20 {
		return fmt.Errorf("Android named results unavailable")
	}
	var suite readinessXMLSuite
	if len(x) > 0 && xml.Unmarshal(x, &suite) != nil {
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
		if revision != input.SourceRevision || modified || !slices.Contains(strings.FieldsFunc(tags, func(r rune) bool { return r == ',' || r == ' ' }), "e2e_realclaude") {
			return fmt.Errorf("tested binary is not clean pinned instrumented source")
		}
	} else {
		return fmt.Errorf("tested binary missing")
	}
	denied := append(append([]string{}, input.Homes...), input.Observations, input.XML, os.Getenv("CLAUDE_CODE_OAUTH_TOKEN"), os.Getenv("ANTHROPIC_API_KEY"))
	contract, e := readinessReadEmulatorContract()
	if e != nil || input.RunID != contract.RunID || input.SourceRevision != contract.Source {
		return fmt.Errorf("capture differs from admitted contract")
	}
	evidence, e := os.ReadFile(input.Evidence)
	if e != nil || len(evidence) > 16<<20 {
		return fmt.Errorf("effective setup and runner intervals unavailable")
	}
	var provenance readinessEmulatorProvenance
	if json.Unmarshal(evidence, &provenance) != nil {
		return fmt.Errorf("capture provenance unreadable")
	}
	status, e := os.ReadFile(input.RunnerStatus)
	if e != nil || len(status) > 64<<20 {
		return fmt.Errorf("witnessed runner status unavailable")
	}
	provenance.Methods, e = readinessRunnerIntervals(status, contract.Selection)
	if e != nil {
		return e
	}
	persisted, e := os.ReadFile(input.Persistence)
	if e != nil || len(persisted) > 128<<20 {
		return fmt.Errorf("persisted lifecycle snapshots unavailable")
	}
	states, e := readinessPrivateRows[readinessPrivateState](persisted)
	if e != nil {
		return e
	}
	readbacks, e := os.ReadFile(input.Readbacks)
	if e != nil || len(readbacks) > 16<<20 {
		return fmt.Errorf("resolved configuration readbacks unavailable")
	}
	resolved, e := readinessPrivateRows[readinessPrivateState](readbacks)
	if e != nil {
		return e
	}
	states = append(states, resolved...)
	observations, e := readinessPrivateRows[readinessPrivateObservation](raw)
	if e != nil {
		return e
	}
	sort.SliceStable(observations, func(a, b int) bool { return observations[a].At.Before(observations[b].At) })
	joins, e := readinessNewJoins(observations, states)
	if e != nil {
		return e
	}
	if e = joins.fold(r); e != nil {
		return e
	}
	provenance.Settings = nil
	for _, state := range states {
		denied = append(denied, state.Owner, state.Epoch, state.Conversation, state.Session)
		if state.Entry != nil {
			if state.Entry.Session != nil {
				denied = append(denied, state.Entry.Session.SessionID)
			}
			var private struct {
				Text      string `json:"text"`
				MessageID string `json:"message_id"`
				DeviceID  string `json:"device_id"`
			}
			if json.Unmarshal(state.Entry.Payload, &private) == nil {
				denied = append(denied, private.Text, private.MessageID, private.DeviceID)
			}
		}
		if state.Settings != nil {
			settings := *state.Settings
			settings.OwnerOrdinal = joins.owners[state.Owner]
			settings.RestartEpoch = joins.epochs[state.Owner+"/"+state.Epoch]
			settings.SessionOrdinal = joins.sessions[state.Owner+"/"+state.Session]
			provenance.Settings = append(provenance.Settings, settings)
		}
	}
	for _, o := range observations {
		denied = append(denied, o.Subject, o.Child, o.Device, o.Owner, o.Epoch)
	}
	// Tiny protocol message IDs are not secrets: scanning their substrings would
	// reject all timestamps. Private authored payloads and identity joins remain denied.
	safeDenied := denied[:0]
	for _, value := range denied {
		if len(value) >= 8 {
			safeDenied = append(safeDenied, value)
		}
	}
	denied = append(safeDenied, input.TargetPrompt, input.Evidence, input.Persistence, input.Readbacks, input.RunnerStatus)
	provenance.Targets, e = joins.targets(states, input.TargetPrompt, provenance.Methods)
	if e != nil {
		return e
	}
	r.Results, r.Counts, e = readinessEmulatorResults(&suite, provenance.Methods, contract.Selection)
	if e != nil {
		return e
	}
	provenance.TargetAcquisition = "not reached"
	for _, method := range provenance.Methods {
		if method.Name == "interactiveTurn_pingPrompt_streamsPingReplyIntoThread" {
			provenance.TargetAcquisition = "enqueue unwitnessed"
		}
	}
	if len(provenance.Targets) > 0 {
		provenance.TargetAcquisition = "persisted target"
	}
	for _, target := range provenance.Targets {
		for boundary, at := range map[string]time.Time{"target_enqueued": target.Enqueued, "durable_target_delivery": target.Delivered, "target_main_turn_opened": target.Opened} {
			if !at.IsZero() {
				epoch, session, child := target.RestartEpoch, target.SessionOrdinal, target.ChildOrdinal
				if boundary == "target_enqueued" {
					epoch, session, child = target.EnqueueRestartEpoch, target.EnqueueSessionOrdinal, target.EnqueueChildOrdinal
				}
				r.Witnesses = append(r.Witnesses, readinessWitness{At: at, Boundary: boundary, OwnerOrdinal: target.OwnerOrdinal, RestartEpoch: epoch, Ordinal: target.ConversationOrdinal, SessionOrdinal: session, ChildOrdinal: child, OK: true})
			}
		}
	}
	r.Setup["xml_available"] = len(x) > 0
	r.Setup["emulator_evidence"] = provenance
	r.Setup["complete_65_method_result_set"] = r.Counts["incomplete"] == 0
	suite.Name = "queued-turn-readiness"
	suite.Tests, suite.Failures, suite.Skipped = len(suite.Cases), r.Counts["failed"], r.Counts["skipped"]
	portable, e := xml.Marshal(suite)
	if e != nil {
		return e
	}
	r.emulatorXML = append(portable, '\n')
	if input.Resources != "" {
		b, e := os.ReadFile(input.Resources)
		if e != nil {
			return fmt.Errorf("resource samples missing")
		}
		resources, e := readinessPrivateRows[readinessPrivateResource](b)
		if e != nil {
			return e
		}
		for _, resource := range resources {
			if joins.owners[resource.Owner] == 0 {
				return fmt.Errorf("unowned resource interval")
			}
			epoch, ok := joins.epochs[resource.Owner+"/"+resource.Epoch]
			if !ok {
				return fmt.Errorf("resource epoch unknown")
			}
			sample, e := readinessSanitizeResource(resource.Sample, joins, resource.Owner, resource.Epoch)
			if e != nil {
				return e
			}
			sample["owner_ordinal"], sample["restart_epoch"] = joins.owners[resource.Owner], epoch
			r.Resources = append(r.Resources, sample)
		}
	}
	if e := readinessExportRuntime(dir, input.RunID, r, denied); e != nil {
		return e
	}
	return nil
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
	if e := readinessSampleEmulator(ctx, input, inputPath, f); e != nil {
		t.Fatal(e)
	}
}

func TestReadinessEmulatorRequestJoins(t *testing.T) {
	var o readinessPrivateObservation
	if json.Unmarshal([]byte(`{"request":4611686018427387921,"reply_to":4611686018427387921,"owner":"private-daemon"}`), &o) != nil || o.Request != 4611686018427387921 || o.ReplyTo != o.Request || o.Owner != "private-daemon" {
		t.Fatal("full-width request/reply ownership join lost")
	}
}
