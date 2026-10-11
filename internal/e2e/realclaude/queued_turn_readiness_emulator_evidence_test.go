//go:build e2e_realclaude

package realclaude

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"github.com/pyrycode/pyrycode/internal/history"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A repinned contradiction must still fail: hashes certify bytes, not meaning.
func TestReadinessEmulatorEvidence(t *testing.T) {
	for _, scenario := range []string{"missing emulator evidence", "substituted Go run", "duplicate selected names", "zero executed"} {
		t.Run(scenario, func(t *testing.T) {
			r := &readinessCapture{Arm: "emulator", DaemonCommit: "8a076f1b6b65be51b91bce5435f42dd5719fd312", Setup: map[string]any{"capture_kind": "actual-client diagnostic"}, Counts: map[string]int{"executed": 1, "passed": 1, "failed": 0, "skipped": 0}, Results: []readinessNamedResult{{Name: "interactiveTurn_pingPrompt_streamsPingReplyIntoThread", Status: "pass"}}}
			if scenario == "substituted Go run" {
				r.Arm = "immediate-fresh"
			}
			if scenario == "duplicate selected names" {
				r.Results = append(r.Results, r.Results[0])
			}
			if scenario == "zero executed" {
				r.Counts["executed"] = 0
				r.Counts["passed"] = 0
			}
			if readinessExportRuntime(filepath.Join(t.TempDir(), "run"), "3071-emulator-20261011", r, nil) == nil {
				t.Fatal("incomplete or contradictory actual-client evidence accepted")
			}
		})
	}
}

func TestReadinessEmulatorResults(t *testing.T) {
	r := &readinessCapture{Arm: "emulator", DaemonCommit: "8a076f1b6b65be51b91bce5435f42dd5719fd312", Setup: map[string]any{}, Counts: map[string]int{"executed": 1, "passed": 1}, Results: []readinessNamedResult{{Name: "interactiveTurn_notSelected", Status: "pass"}}}
	dir := filepath.Join(t.TempDir(), "run")
	if err := readinessExportRuntime(dir, "3071-emulator-20261011", r, nil); err != nil {
		return
	}
	b, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m readinessRuntimeManifest
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if err := readinessValidateRuntime(dir); err == nil {
		t.Fatal("unexpected method accepted with valid hashes")
	}
}

func TestReadinessEmulatorOwnership(t *testing.T) {
	at := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	var obs []readinessPrivateObservation
	var states []readinessPrivateState
	for _, owner := range []string{"private-a", "private-b"} {
		for _, epoch := range []string{"first", "replacement"} {
			obs = append(obs, readinessPrivateObservation{At: at, Owner: owner, Epoch: epoch, Process: 7, Subject: "same-session", Child: "reused-child", Boundary: "gate_released", OK: true}, readinessPrivateObservation{At: at, Owner: owner, Epoch: epoch, Process: 7, Subject: "same-conversation", Device: "same-device", Phase: "host_received", Request: 4611686018427387921, Kind: "send_message", Outcome: "unknown"}, readinessPrivateObservation{At: at.Add(time.Second), Owner: owner, Epoch: epoch, Process: 7, Device: "same-device", Phase: "outbound_queued", ReplyTo: 4611686018427387921, Kind: "error", Outcome: "refused"})
			states = append(states, readinessPrivateState{Owner: owner, Epoch: epoch, Conversation: "same-conversation", Session: "same-session"})
		}
	}
	j, err := readinessNewJoins(obs, states)
	if err != nil {
		t.Fatal(err)
	}
	r := &readinessCapture{}
	if err = j.fold(r); err != nil {
		t.Fatal(err)
	}
	if r.Witnesses[0].OwnerOrdinal == r.Witnesses[2].OwnerOrdinal || r.Witnesses[0].ChildOrdinal == r.Witnesses[1].ChildOrdinal || r.Witnesses[0].Ordinal == r.Witnesses[2].Ordinal {
		t.Fatal("daemon/lifetime identity collapsed")
	}
	for i := 0; i < len(r.Requests); i += 2 {
		if r.Requests[i].RequestOrdinal == 0 || r.Requests[i+1].ReplyToOrdinal != r.Requests[i].RequestOrdinal {
			t.Fatal("full-width request/reply join lost")
		}
	}
	// An unknown session is never treated as a conversation ID of the same spelling.
	obs = append(obs, readinessPrivateObservation{At: at, Owner: "private-a", Epoch: "first", Process: 7, Subject: "same-conversation", Boundary: "permission_write"})
	j, err = readinessNewJoins(obs, states)
	if err != nil {
		t.Fatal(err)
	}
	r = &readinessCapture{}
	if err = j.fold(r); err != nil {
		t.Fatal(err)
	}
	if r.Witnesses[len(r.Witnesses)-1].Ordinal != 0 {
		t.Fatal("session/conversation namespaces conflated")
	}
}

func TestReadinessEmulatorIntervals(t *testing.T) {
	old := map[int]readinessCPUCounter{7: {100, "first"}, 8: {90, "exited"}}
	for _, tc := range []struct {
		name    string
		current map[int]readinessCPUCounter
		ticks   uint64
		known   bool
	}{
		{"same child", map[int]readinessCPUCounter{7: {120, "first"}}, 20, true},
		{"PID reused", map[int]readinessCPUCounter{7: {130, "replacement"}}, 0, false},
		{"other daemon", map[int]readinessCPUCounter{9: {200, "other"}}, 0, false},
		{"all descendants exited", map[int]readinessCPUCounter{}, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ticks, known := readinessInterval(old, tc.current)
			if ticks != tc.ticks || known != tc.known {
				t.Fatalf("interval=%d known=%t", ticks, known)
			}
		})
	}
}

func TestReadinessEmulatorSelectedResults(t *testing.T) {
	c, err := readinessReadEmulatorContract()
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	suite := readinessXMLSuite{Cases: []readinessXMLCase{{Name: c.Selection[0], Class: "de.pyryco.mobile.e2e.InteractiveStreamE2ETest", Time: "2.125"}}}
	intervals := []readinessMethodInterval{{Name: c.Selection[0], Started: at, Ended: at.Add(3 * time.Second), Outcome: "pass", Clock: "runner-status-host-utc"}}
	results, counts, err := readinessEmulatorResults(&suite, intervals, c.Selection)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 65 || counts["incomplete"] != 64 || counts["executed"] != 1 || results[0].DurationSeconds == nil || *results[0].DurationSeconds != 2.125 || !results[0].Started.Equal(at) {
		t.Fatal("partial inventory or witnessed durations lost")
	}
	for _, scenario := range []string{"duplicate", "unexpected", "wrong outcome", "backward", "missing interval"} {
		t.Run(scenario, func(t *testing.T) {
			s := suite
			m := append([]readinessMethodInterval{}, intervals...)
			switch scenario {
			case "duplicate":
				s.Cases = append(append([]readinessXMLCase{}, s.Cases...), s.Cases[0])
			case "unexpected":
				s.Cases = []readinessXMLCase{{Name: "interactiveTurn_unselected", Class: s.Cases[0].Class}}
			case "wrong outcome":
				m[0].Outcome = "fail"
			case "backward":
				m[0].Ended = at.Add(-time.Second)
			case "missing interval":
				m = nil
			}
			if _, _, err := readinessEmulatorResults(&s, m, c.Selection); err == nil {
				t.Fatal("contradictory results accepted")
			}
		})
	}
}

func TestReadinessEmulatorRunnerStatus(t *testing.T) {
	name := "interactiveTurn_pingPrompt_streamsPingReplyIntoThread"
	at := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	var raw []byte
	for _, code := range []string{"1", "-2"} {
		for _, line := range []string{"INSTRUMENTATION_STATUS: class=de.pyryco.mobile.e2e.InteractiveStreamE2ETest", "INSTRUMENTATION_STATUS: test=" + name, "INSTRUMENTATION_STATUS_CODE: " + code} {
			b, err := json.Marshal(map[string]any{"at": at, "line": line})
			if err != nil {
				t.Fatal(err)
			}
			raw = append(raw, append(b, '\n')...)
		}
		at = at.Add(2 * time.Second)
	}
	methods, err := readinessRunnerIntervals(raw, []string{name})
	if err != nil {
		t.Fatal(err)
	}
	if len(methods) != 1 || methods[0].Outcome != "fail" || methods[0].Ended.Sub(methods[0].Started) != 2*time.Second {
		t.Fatal("runner timing/outcome lost")
	}
	if _, err := readinessRunnerIntervals(append(raw, raw...), []string{name}); err == nil {
		t.Fatal("repeated runner method accepted")
	}
}

func TestReadinessEmulatorTargetBoundaries(t *testing.T) {
	at := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	prompt := "private exact prompt"
	for _, scenario := range []string{"confirmed", "replacement", "write without receipt", "late", "wrong receipt"} {
		t.Run(scenario, func(t *testing.T) {
			entry := func(id uint64, kind string, payload any, offset time.Duration) readinessPrivateState {
				b, err := json.Marshal(payload)
				if err != nil {
					t.Fatal(err)
				}
				return readinessPrivateState{Owner: "private-daemon", Epoch: "epoch", Conversation: "conv", Entry: &history.Entry{ID: id, Type: kind, TS: at.Add(offset), Payload: b, Session: &history.SessionProvenance{Kind: "claude", SessionID: "session"}}}
			}
			states := []readinessPrivateState{entry(1, "send_accepted", map[string]any{"conversation_id": "conv", "message_id": "msg", "text": prompt, "accepted_at": at}, 0), entry(2, "message", map[string]any{"conversation_id": "conv", "message_id": "msg", "role": "user", "text": prompt}, time.Second), entry(3, "send_delivered", map[string]any{"conversation_id": "conv", "accepted_entry_id": 1, "delivery_entry_id": 2}, 2*time.Second), entry(4, "main_turn_opened", map[string]any{"conversation_id": "conv", "turn_id": "target"}, 3*time.Second), entry(5, "assistant_delta", map[string]any{"conversation_id": "conv", "text": "PiNg"}, 4*time.Second)}
			observations := []readinessPrivateObservation{{At: at.Add(-time.Second), Owner: "private-daemon", Epoch: "epoch", Process: 7, Subject: "session", Child: "first", Boundary: "child_started", OK: true}, {At: at.Add(time.Second), Owner: "private-daemon", Epoch: "epoch", Process: 7, Subject: "session", Child: "first", Boundary: "gate_released", OK: true}}
			switch scenario {
			case "replacement":
				observations = append(observations, readinessPrivateObservation{At: at.Add(1500 * time.Millisecond), Owner: "private-daemon", Epoch: "epoch", Process: 7, Subject: "session", Child: "second", Boundary: "child_started", OK: true})
			case "write without receipt":
				states = states[:2]
			case "late":
				states[2].Entry.TS = at.Add(91 * time.Second)
				states[3].Entry.TS = at.Add(92 * time.Second)
			case "wrong receipt":
				states[2].Entry.Payload = json.RawMessage(`{"accepted_entry_id":99,"delivery_entry_id":2}`)
			}
			joins, err := readinessNewJoins(observations, states)
			if err != nil {
				t.Fatal(err)
			}
			targets, err := joins.targets(states, prompt, []readinessMethodInterval{{Name: "interactiveTurn_pingPrompt_streamsPingReplyIntoThread", Started: at.Add(-time.Second), Ended: at.Add(100 * time.Second)}})
			if err != nil {
				t.Fatal(err)
			}
			target := targets[0]
			switch scenario {
			case "confirmed":
				if target.CurrentChild == nil || !*target.CurrentChild || target.ExactlyOnce == nil || !*target.ExactlyOnce {
					t.Fatal("matching current child lost")
				}
			case "replacement":
				if target.CurrentChild != nil && *target.CurrentChild {
					t.Fatal("predecessor confirmation inherited")
				}
			case "write without receipt", "wrong receipt":
				if !target.Delivered.IsZero() || target.Within90Seconds != nil {
					t.Fatal("turn write manufactured durable delivery")
				}
			case "late":
				if target.Within90Seconds == nil || *target.Within90Seconds {
					t.Fatal("late target passed deadline")
				}
			}
		})
	}
}

func testReadinessEmulatorCapture(t *testing.T) (readinessEmulatorContract, *readinessCapture) {
	t.Helper()
	c, err := readinessReadEmulatorContract()
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	one, zero := 1, 0
	idle, sweep, retention := 0.0, 3600.0, 30.0
	suite := readinessXMLSuite{Cases: []readinessXMLCase{{Name: c.Selection[0], Class: "de.pyryco.mobile.e2e.InteractiveStreamE2ETest", Time: "3"}}}
	p := readinessEmulatorProvenance{RunID: c.RunID, Source: c.Source, MobileSource: c.MobileSource, MobileMerge: c.MobileMerge, MobileTree: c.MobileTree, TestHash: c.TestHash, WrapperHash: c.WrapperHash, AdmissionHash: c.AdmissionHash, Clock: "runner-status-host-utc", ClockComparison: "device-clock-unavailable", ClockComparedAt: at.Add(-time.Minute), CaptureConclusion: "usable", FixtureConclusion: "unknown", HostConclusion: "unknown", TargetAcquisition: "persisted target", Methods: []readinessMethodInterval{{Name: c.Selection[0], Started: at, Ended: at.Add(3 * time.Second), Outcome: "pass", Clock: "runner-status-host-utc"}}, Settings: []readinessEffectiveSettings{{OwnerOrdinal: 1, RestartEpoch: 0, SessionOrdinal: 1, Model: "sonnet", PermissionMode: "default", Provenance: "runtime-and-persistence-readback", OperatorBypass: readinessBool(false), MCP: readinessBool(true), SkipDangerous: readinessBool(true), IdleSeconds: &idle, SweepSeconds: &sweep, RetentionDays: &retention, ActiveCap: &zero, WorkspaceRole: "test-owned-substitute", MCPRole: "test-owned-empty", HookRole: "test-owned-empty"}}, Targets: []readinessEmulatorTarget{{OwnerOrdinal: 1, ConversationOrdinal: 1, SessionOrdinal: 1, ChildOrdinal: 1, EnqueueSessionOrdinal: 1, EnqueueChildOrdinal: 1, Enqueued: at, Delivered: at.Add(time.Second), Opened: at.Add(2 * time.Second), Confirmed: at, CurrentChild: readinessBool(true), Within90Seconds: readinessBool(true)}}}
	results, counts, err := readinessEmulatorResults(&suite, p.Methods, c.Selection)
	if err != nil {
		t.Fatal(err)
	}
	portable, err := xml.Marshal(suite)
	if err != nil {
		t.Fatal(err)
	}
	r := &readinessCapture{Arm: "emulator", DaemonCommit: c.Source, Setup: map[string]any{"emulator_evidence": p, "instrumented_binary_sha256": strings.Repeat("a", 64)}, Counts: counts, Results: results, emulatorXML: portable, Witnesses: []readinessWitness{{At: at, Boundary: "gate_released", OwnerOrdinal: one, SessionOrdinal: one, ChildOrdinal: one, Ordinal: one, OK: true}}, Requests: []readinessRequestEvent{{At: at, OwnerOrdinal: 1, Device: "connection-1", Phase: "host_received", RequestOrdinal: 1}, {At: at.Add(time.Second), OwnerOrdinal: 1, Device: "connection-1", Phase: "outbound_queued", ReplyToOrdinal: 1}}, Resources: []map[string]any{{"at": at, "owner_ordinal": 1, "restart_epoch": 0, "binary_sha256": strings.Repeat("a", 64)}}}
	for _, boundary := range []string{"child_started", "permission_write", "ack_forwarder_line", "ack_stdout_line", "ack_processing"} {
		r.Witnesses = append(r.Witnesses, readinessWitness{At: at, Boundary: boundary, OwnerOrdinal: 1, Ordinal: 1, SessionOrdinal: 1, ChildOrdinal: 1, OK: true})
	}
	for boundary, when := range map[string]time.Time{"target_enqueued": at, "durable_target_delivery": at.Add(time.Second), "target_main_turn_opened": at.Add(2 * time.Second)} {
		r.Witnesses = append(r.Witnesses, readinessWitness{At: when, Boundary: boundary, OwnerOrdinal: 1, Ordinal: 1, SessionOrdinal: 1, ChildOrdinal: 1, OK: true})
	}
	return c, r
}

func TestReadinessEmulatorSemanticValidation(t *testing.T) {
	c, r := testReadinessEmulatorCapture(t)
	at := r.Witnesses[0].At
	dir := filepath.Join(t.TempDir(), "run")
	if err := readinessExportRuntime(dir, c.RunID, r, nil); err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []string{"stale confirmation", "wrong reply owner", "wrong count", "missing selected result", "late comparison", "resource owner", "replacement child before delivery", "completed child before delivery", "persistence epoch", "persistence session", "persistence child"} {
		t.Run(scenario, func(t *testing.T) {
			path := filepath.Join(dir, "setup.json")
			b, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "stale confirmation":
				b = bytes.ReplaceAll(b, []byte(`"ChildOrdinal":1`), []byte(`"ChildOrdinal":2`))
			case "wrong reply owner":
				path = filepath.Join(dir, "request-events.jsonl")
				b, _ = os.ReadFile(path)
				var requests []readinessRequestEvent
				if err := readinessStream(dir, "request-events.jsonl", &requests); err != nil {
					t.Fatal(err)
				}
				requests[1].OwnerOrdinal = 2
				b = nil
				for _, request := range requests {
					row, _ := json.Marshal(request)
					b = append(b, append(row, '\n')...)
				}
			case "missing selected result":
				path = filepath.Join(dir, "named-results.json")
				b = []byte(`[]`)
			case "late comparison":
				b = bytes.ReplaceAll(b, []byte(at.Add(time.Second).Format(time.RFC3339Nano)), []byte(at.Add(91*time.Second).Format(time.RFC3339Nano)))
			case "replacement child before delivery", "completed child before delivery", "persistence epoch", "persistence session", "persistence child":
				path = filepath.Join(dir, "boundary-events.jsonl")
				var witnesses []readinessWitness
				if err := readinessStream(dir, "boundary-events.jsonl", &witnesses); err != nil {
					t.Fatal(err)
				}
				if strings.HasPrefix(scenario, "persistence") {
					for i := range witnesses {
						if witnesses[i].Boundary != "durable_target_delivery" {
							continue
						}
						switch scenario {
						case "persistence epoch":
							witnesses[i].RestartEpoch++
						case "persistence session":
							witnesses[i].SessionOrdinal++
						case "persistence child":
							witnesses[i].ChildOrdinal++
						}
					}
				} else {
					w := readinessWitness{At: at.Add(500 * time.Millisecond), Boundary: "child_started", OwnerOrdinal: 1, Ordinal: 1, SessionOrdinal: 1, ChildOrdinal: 2, OK: true}
					if scenario == "completed child before delivery" {
						w.Boundary, w.ChildOrdinal = "child_wait_complete", 1
					}
					witnesses = append(witnesses, w)
				}
				b = nil
				for _, witness := range witnesses {
					row, _ := json.Marshal(witness)
					b = append(b, append(row, '\n')...)
				}
			case "resource owner":
				path = filepath.Join(dir, "resource-samples.jsonl")
				b, _ = os.ReadFile(path)
				b = bytes.ReplaceAll(b, []byte(`"owner_ordinal":1`), []byte(`"owner_ordinal":2`))
			}
			original, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			defer os.WriteFile(path, original, 0600)
			manifestPath := filepath.Join(dir, "manifest.json")
			manifestBytes, err := os.ReadFile(manifestPath)
			if err != nil {
				t.Fatal(err)
			}
			defer os.WriteFile(manifestPath, manifestBytes, 0600)
			var m readinessRuntimeManifest
			if json.Unmarshal(manifestBytes, &m) != nil {
				t.Fatal("manifest malformed")
			}
			if scenario == "wrong count" {
				m.Counts["executed"]++
			} else {
				if err := os.WriteFile(path, b, 0600); err != nil {
					t.Fatal(err)
				}
				m.SHA256[filepath.Base(path)] = readinessHash(b)
			}
			b, err = json.Marshal(m)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(manifestPath, b, 0600); err != nil {
				t.Fatal(err)
			}
			if err := readinessValidateRuntime(dir); err == nil {
				t.Fatal("repinned semantic contradiction accepted")
			}
		})
	}
}
