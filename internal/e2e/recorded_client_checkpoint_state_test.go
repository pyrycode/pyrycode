//go:build e2e

package e2e

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/e2e/internal/claudetape"
)

func TestRecordedCheckpointState(t *testing.T) {
	for _, name := range []string{"capture", "replay", "replay delivery error", "capture bound", "changed seal evidence"} {
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "actions")
			if err := claudetape.NewCheckpointDirectory(dir); err != nil {
				t.Fatal(err)
			}
			action := claudetape.ClientCheckpoint{Type: claudetape.ClientCheckpointDirection, Sequence: 1, Action: "interrupt_phase", ConversationID: "conv", Phase: "thinking"}
			raw, _ := json.Marshal(action)
			tape := &claudetape.Tape{Version: 2, Scenario: "interrupt-thinking"}
			var player *claudetape.Player
			if strings.HasPrefix(name, "replay") {
				tape.Producer = claudetape.Producer{Revision: strings.Repeat("a", 40), BinarySHA256: strings.Repeat("b", 64), ClaudeVersion: "synthetic unit rig", CapturedAt: "2026-10-19T00:00:00Z"}
				tape.Args = []string{"--input-format", "stream-json"}
				tape.Frames = []claudetape.Frame{{Direction: "stdin", Data: json.RawMessage(`{"type":"user"}`)}, {Direction: claudetape.ClientCheckpointDirection, Data: raw}, {Direction: "stdout", Data: json.RawMessage(`{"type":"result","subtype":"error_during_execution"}`)}}
				var err error
				player, err = claudetape.New(tape, tape.Args)
				if err != nil {
					t.Fatal(err)
				}
				if out, err := player.Input(tape.Frames[0].Data); err != nil || len(out) != 0 {
					t.Fatal("replay did not hold at client action")
				}
			}
			budget := 2 * time.Second
			if name == "replay delivery error" || name == "capture bound" {
				budget = 150 * time.Millisecond
			}
			done := make(chan error, 1)
			go func() { done <- claudetape.PublishCheckpoint(dir, action, budget) }()
			state := recordedCheckpointState{dir: dir}
			frames, total, released := 0, 0, 0
			if name == "capture bound" {
				total = claudetape.MaxBytes
			}
			emit := func(out []json.RawMessage) error {
				select {
				case <-done:
					t.Fatal("publisher acknowledged before released output handling")
				default:
				}
				if name == "replay delivery error" {
					return errors.New("synthetic output delivery failure")
				}
				released += len(out)
				frames += len(out)
				return nil
			}
			var pollErr error
			deadline := time.Now().Add(2 * time.Second)
			for len(state.accepted) == 0 && pollErr == nil && time.Now().Before(deadline) {
				pollErr = state.poll(tape, player, func(raw []byte) ([]byte, error) { return append([]byte(nil), raw...), nil }, emit, &total, &frames)
				if pollErr == nil && len(state.accepted) == 0 {
					time.Sleep(time.Millisecond)
				}
			}
			failed := name == "replay delivery error" || name == "capture bound"
			if (pollErr != nil) != failed {
				t.Fatalf("checkpoint dispatch acceptance err=%v", pollErr)
			}
			if err := <-done; (err != nil) != failed {
				t.Fatal("publisher acknowledgement acceptance changed")
			}
			if failed {
				if state.seal() == nil {
					t.Fatal("failed action dispatch passed seal")
				}
				return
			}
			if len(state.accepted) != 1 {
				t.Fatal("action was not dispatched")
			}
			if player == nil {
				if frames != 1 || len(tape.Frames) != 1 || tape.Frames[0].Direction != claudetape.ClientCheckpointDirection || released != 0 {
					t.Fatal("capture did not record only the actual action")
				}
			} else if released != 1 || frames != 2 || player.Complete() != nil {
				t.Fatal("replay action failed output/completion milestones")
			}
			if name == "changed seal evidence" {
				if err := os.WriteFile(filepath.Join(dir, "000001.ready"), []byte(`{}`), 0600); err != nil {
					t.Fatal(err)
				}
			}
			sealErr := state.seal()
			if (sealErr != nil) != (name == "changed seal evidence") {
				t.Fatal("state seal did not validate exact file evidence")
			}
			if err := state.poll(tape, player, func(raw []byte) ([]byte, error) { return raw, nil }, emit, &total, &frames); err == nil {
				t.Fatal("post-seal dispatcher accepted old path")
			}
		})
	}
}
func TestRecordedCheckpointProfileIsExplicit(t *testing.T) {
	frames, total := 0, 0
	normalize := func(raw []byte) ([]byte, error) { return raw, nil }
	emit := func([]json.RawMessage) error { return nil }
	for _, row := range []struct {
		scenario string
		version  int
		dir      string
		valid    bool
	}{
		{"reply", 1, "", true}, {"reply", 1, "unexpected", false}, {"interrupt-running", 2, "", false}, {"interrupt-running", 1, "", false},
	} {
		state := recordedCheckpointState{dir: row.dir}
		err := state.poll(&claudetape.Tape{Version: row.version, Scenario: row.scenario}, nil, normalize, emit, &total, &frames)
		if (err == nil) != row.valid {
			t.Fatal("implicit checkpoint profile accepted")
		}
	}
}
