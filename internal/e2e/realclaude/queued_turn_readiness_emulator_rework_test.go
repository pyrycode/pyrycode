//go:build e2e_realclaude

package realclaude

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/history"
)

func testReadinessTargetStates(t *testing.T) ([]readinessPrivateState, []readinessPrivateObservation, time.Time) {
	t.Helper()
	at := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	var states []readinessPrivateState
	for i, e := range []struct{ kind, payload string }{
		{"send_accepted", `{"conversation_id":"conv","message_id":"msg","text":"private prompt","accepted_at":"2026-10-11T12:00:00Z"}`},
		{"message", `{"conversation_id":"conv","message_id":"msg","role":"user","text":"private prompt"}`},
		{"send_delivered", `{"conversation_id":"conv","accepted_entry_id":1,"delivery_entry_id":2}`},
		{"main_turn_opened", `{"conversation_id":"conv","turn_id":"target"}`},
		{"assistant_delta", `{"conversation_id":"conv","turn_id":"target","text":"PiNg"}`},
		{"turn_end", `{"conversation_id":"conv","turn_id":"target"}`},
	} {
		states = append(states, readinessPrivateState{Owner: "private-daemon", Epoch: "epoch", Conversation: "conv", Entry: &history.Entry{ID: uint64(i + 1), Type: e.kind, TS: at.Add(time.Duration(i) * time.Second), Payload: json.RawMessage(e.payload), Session: &history.SessionProvenance{Kind: "claude", SessionID: "session"}}})
	}
	var observations []readinessPrivateObservation
	for _, boundary := range []string{"child_started", "permission_write", "ack_forwarder_line", "ack_stdout_line", "ack_processing", "gate_released"} {
		observations = append(observations, readinessPrivateObservation{At: at.Add(-time.Second), Owner: "private-daemon", Epoch: "epoch", Process: 7, Subject: "session", Child: "child", Boundary: boundary, OK: true})
	}
	return states, observations, at
}

func TestReadinessEmulatorPendingTarget(t *testing.T) {
	for _, scenario := range []string{"failed", "incomplete", "binding without provenance", "spawn after enqueue"} {
		t.Run(scenario, func(t *testing.T) {
			states, observations, at := testReadinessTargetStates(t)
			states = states[:1]
			if scenario == "spawn after enqueue" {
				for i := range observations {
					observations[i].At = at.Add(time.Second)
				}
			}
			if scenario == "binding without provenance" {
				states[0].Entry.Session = nil
				states = append(states, readinessPrivateState{At: at.Add(-time.Second), Owner: "private-daemon", Epoch: "epoch", Conversation: "conv", Session: "session", Kind: "binding"})
			}
			j, err := readinessNewJoins(observations, states)
			if err != nil {
				t.Fatal(err)
			}
			methods := []readinessMethodInterval{{Name: "interactiveTurn_pingPrompt_streamsPingReplyIntoThread", Started: at.Add(-time.Second), Ended: at.Add(90 * time.Second), Outcome: "fail", Clock: "runner-status-host-utc"}}
			if scenario == "incomplete" {
				methods[0].Ended, methods[0].Outcome = time.Time{}, "incomplete"
			}
			targets, err := j.targets(states, "private prompt", methods)
			if err != nil || len(targets) != 1 {
				t.Fatalf("pending target unavailable: %v", err)
			}
			target := targets[0]
			if target.SessionOrdinal != 1 || target.ChildOrdinal != 1 || !target.Enqueued.Equal(at) {
				t.Fatal("accepted-only session/child/enqueue lost")
			}
			if !target.Delivered.IsZero() || !target.Opened.IsZero() || target.PromptMatches != nil || target.ReplyMatches != nil || target.ExactlyOnce != nil || target.CurrentChild != nil || target.Within90Seconds != nil {
				t.Fatal("absent target boundaries manufactured")
			}
			c, r := testReadinessEmulatorCapture(t)
			p := r.Setup["emulator_evidence"].(readinessEmulatorProvenance)
			p.Methods[0].Name = c.Selection[1]
			p.Targets, p.Methods = targets, append(p.Methods, methods...)
			suite := readinessXMLSuite{Cases: []readinessXMLCase{{Name: c.Selection[1], Class: "de.pyryco.mobile.e2e.InteractiveStreamE2ETest"}}}
			if scenario != "incomplete" {
				suite.Cases = append(suite.Cases, readinessXMLCase{Name: methods[0].Name, Class: "de.pyryco.mobile.e2e.InteractiveStreamE2ETest", Failure: &struct{}{}})
			}
			r.Results, r.Counts, err = readinessEmulatorResults(&suite, p.Methods, c.Selection)
			if err != nil {
				t.Fatal(err)
			}
			r.emulatorXML, err = xml.Marshal(suite)
			if err != nil {
				t.Fatal(err)
			}
			r.Setup["emulator_evidence"] = p
			r.Witnesses = nil
			if err := j.fold(r); err != nil {
				t.Fatal(err)
			}
			r.Witnesses = append(r.Witnesses, readinessWitness{At: target.Enqueued, Boundary: "target_enqueued", OwnerOrdinal: 1, Ordinal: 1, SessionOrdinal: target.EnqueueSessionOrdinal, ChildOrdinal: target.EnqueueChildOrdinal, OK: true})
			if err := readinessExportRuntime(filepath.Join(t.TempDir(), "run"), c.RunID, r, nil); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestReadinessEmulatorTurnIsolation(t *testing.T) {
	for _, scenario := range []string{"target", "foreign session", "unknown session", "unknown turn", "foreign delta", "unknown delta", "unknown delta turn", "foreign completion", "later independent turn", "intervening user message"} {
		t.Run(scenario, func(t *testing.T) {
			states, observations, at := testReadinessTargetStates(t)
			switch scenario {
			case "foreign session", "unknown session":
				for i := 3; i < len(states); i++ {
					states[i].Entry.Session = &history.SessionProvenance{Kind: "claude", SessionID: "foreign"}
					if scenario == "unknown session" {
						states[i].Entry.Session = nil
					}
				}
			case "unknown turn":
				states[3].Entry.Payload = json.RawMessage(`{"conversation_id":"conv"}`)
			case "foreign delta":
				states[4].Entry.Session = &history.SessionProvenance{Kind: "claude", SessionID: "foreign"}
			case "unknown delta":
				states[4].Entry.Session = nil
			case "unknown delta turn":
				states[4].Entry.Payload = json.RawMessage(`{"conversation_id":"conv","text":"PiNg"}`)
			case "foreign completion":
				states[5].Entry.Payload = json.RawMessage(`{"conversation_id":"conv","turn_id":"other"}`)
			case "later independent turn":
				for i := 3; i < 6; i++ {
					e := *states[i].Entry
					e.ID += 3
					e.TS = e.TS.Add(3 * time.Second)
					e.Payload = bytes.ReplaceAll(e.Payload, []byte("target"), []byte("other"))
					states = append(states, readinessPrivateState{Owner: "private-daemon", Epoch: "epoch", Conversation: "conv", Entry: &e})
				}
			case "intervening user message":
				states[3].Entry.Type, states[3].Entry.Payload = "message", json.RawMessage(`{"conversation_id":"conv","message_id":"other","role":"user"}`)
				states[4].Entry.Type = "main_turn_opened"
			}
			j, err := readinessNewJoins(observations, states)
			if err != nil {
				t.Fatal(err)
			}
			targets, err := j.targets(states, "private prompt", []readinessMethodInterval{{Name: "interactiveTurn_pingPrompt_streamsPingReplyIntoThread", Started: at.Add(-time.Second)}})
			if err != nil {
				t.Fatal(err)
			}
			target := targets[0]
			if scenario == "target" || scenario == "later independent turn" {
				if target.ExactlyOnce == nil || !*target.ExactlyOnce || target.ReplyMatches == nil || !*target.ReplyMatches {
					t.Fatal("target turn comparison lost")
				}
			} else if scenario == "foreign delta" {
				if target.ReplyMatches != nil && *target.ReplyMatches {
					t.Fatal("foreign delta satisfied target reply")
				}
			} else if scenario == "foreign completion" || scenario == "unknown delta" || scenario == "unknown delta turn" {
				if target.ReplyMatches != nil {
					t.Fatal("foreign completion finalized target")
				}
			} else if !target.Opened.IsZero() || target.ReplyMatches != nil || target.ExactlyOnce != nil {
				t.Fatal("unattributed turn claimed as target")
			}
		})
	}
}

func TestReadinessResourceHelperProcess(t *testing.T) {
	if os.Getenv("PYRY_READINESS_RESOURCE_HELPER") != "1" {
		return
	}
	_, _ = io.Copy(io.Discard, os.Stdin)
}

func TestReadinessEmulatorChildResources(t *testing.T) {
	if readinessProcIdentity(os.Getpid()) == "" {
		t.Skip("kernel process start identity unavailable")
	}
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	child := exec.CommandContext(ctx, bin, "-test.run=^TestReadinessResourceHelperProcess$")
	child.Env = append(os.Environ(), "PYRY_READINESS_RESOURCE_HELPER=1")
	stdin, err := child.StdinPipe()
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	if err := child.Start(); err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stdin.Close(); cancel(); _ = child.Wait() })
	for _, scenario := range []string{"live", "completed", "reused PID", "missing start identity", "replacement", "other daemon"} {
		t.Run(scenario, func(t *testing.T) {
			dir := t.TempDir()
			input := readinessEmulatorInput{Binary: bin, Observations: filepath.Join(dir, "observations.jsonl"), Persistence: filepath.Join(dir, "persistence.jsonl"), Homes: []string{dir}}
			at := time.Now().UTC()
			o := map[string]any{"at": at, "owner": "private-daemon", "epoch": "epoch", "owner_home": dir, "instance": "test", "process": os.Getpid(), "process_start": readinessProcIdentity(os.Getpid()), "subject": "session", "child": "old", "boundary": "child_started", "ok": true, "value": child.Process.Pid, "child_start": readinessProcIdentity(child.Process.Pid)}
			if scenario == "reused PID" || scenario == "replacement" {
				o["child_start"] = "retired-lifetime"
			}
			if scenario == "missing start identity" {
				delete(o, "child_start")
			}
			if scenario == "other daemon" {
				o["process"] = child.Process.Pid
				o["process_start"] = readinessProcIdentity(child.Process.Pid)
			}
			var raw bytes.Buffer
			if err := json.NewEncoder(&raw).Encode(o); err != nil {
				t.Fatal(err)
			}
			if scenario == "completed" || scenario == "replacement" {
				o["boundary"], o["at"] = "child_wait_complete", at.Add(time.Nanosecond)
				if err := json.NewEncoder(&raw).Encode(o); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "replacement" {
				o["boundary"], o["child"], o["child_start"], o["at"] = "child_started", "new", readinessProcIdentity(child.Process.Pid), at.Add(2*time.Nanosecond)
				if err := json.NewEncoder(&raw).Encode(o); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(input.Observations, raw.Bytes(), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "capture-complete"), nil, 0600); err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			if err := readinessSampleEmulator(ctx, input, filepath.Join(dir, "input.json"), &out); err != nil {
				t.Fatal(err)
			}
			resources, err := readinessPrivateRows[readinessPrivateResource](out.Bytes())
			if err != nil || len(resources) != 1 {
				t.Fatalf("one-sample output unavailable: %v", err)
			}
			want := 0
			if scenario == "live" || scenario == "replacement" {
				want = 1
			}
			sample := resources[0].Sample
			children, _ := sample["children"].([]any)
			if len(children) != want || sample["live_claude_children"] != float64(want) {
				t.Fatalf("child attribution: samples=%d census=%v want=%d", len(children), sample["live_claude_children"], want)
			}
			if want == 1 {
				key := "old"
				if scenario == "replacement" {
					key = "new"
				}
				if children[0].(map[string]any)["private_child"] != key {
					t.Fatal("resources assigned to predecessor")
				}
			}
		})
	}
}
