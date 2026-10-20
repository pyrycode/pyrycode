package claudetape

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestConversationCompletionPreservesOutputSchedule(t *testing.T) {
	for _, mode := range []string{"capture", "replay"} {
		t.Run(mode, func(t *testing.T) {
			home := t.TempDir()
			if err := os.Chmod(home, 0700); err != nil {
				t.Fatal(err)
			}
			tape := fallbackExchangeControl()
			frames := []Frame{tape.Frames[0]}
			for range 15 {
				frames = append(frames, Frame{Direction: "stdout", Data: json.RawMessage(`{"type":"rate_limit_event"}`)})
			}
			tape.Frames = append(frames, tape.Frames[1:]...)
			c := LifecycleBrokerConfig{PoolProcessConfig: PoolProcessConfig{Mode: mode, Scenario: "conversation-system-prompt-bare", Path: filepath.Join(home, "bare.json"), Producer: tape.Producer}, OwnerPID: os.Getpid() + 100000}
			origin := time.Unix(0, 0)
			current := origin
			derivations := 0
			observe := newConversationCompletionObserver(c, tape, func(observed *Tape) (FallbackExchange, error) {
				derivations++
				current = current.Add(35 * time.Millisecond)
				return completedConversationExchange(observed)
			})
			ordinal := 0
			for _, frame := range tape.Frames {
				if frame.Direction == "stdout" {
					ordinal++
					if err := forwardLifecycleOutput(origin, int64(time.Duration(ordinal)*20*time.Millisecond), func() time.Time { return current }, func(d time.Duration) { current = current.Add(d) }, func() error { return nil }); err != nil {
						t.Fatalf("completion work delayed output %d: %v", ordinal, err)
					}
				}
				if err := observe(frame.Direction, frame.Data); err != nil {
					t.Fatal(err)
				}
			}
			var receipt ConversationCompletion
			if derivations != 1 || ReadLifecycleControl(filepath.Join(home, "bare.completed"), 16384, &receipt) != nil || ValidateConversationCompletion(receipt, "bare", tape.Producer.Revision, c.OwnerPID, os.Getpid(), tape) != nil {
				t.Fatalf("scheduled outputs lost their actual complete source; derivations=%d", derivations)
			}
		})
	}
}

func TestConversationCompletionRequiresActuallyObservedSuccessfulResult(t *testing.T) {
	for _, mode := range []string{"capture", "replay"} {
		t.Run(mode, func(t *testing.T) {
			home := t.TempDir()
			if os.Chmod(home, 0700) != nil {
				t.Fatal("private home")
			}
			tape := fallbackExchangeControl()
			c := LifecycleBrokerConfig{PoolProcessConfig: PoolProcessConfig{Mode: mode, Scenario: "conversation-system-prompt-bare", Home: home, Path: filepath.Join(home, "bare.json"), Producer: tape.Producer}, OwnerPID: os.Getpid() + 100000}
			observe := NewConversationCompletionObserver(c, tape)
			path := filepath.Join(home, "bare.completed")
			for i, frame := range tape.Frames {
				if err := observe(frame.Direction, frame.Data); err != nil {
					t.Fatal(err)
				}
				if i < len(tape.Frames)-1 {
					if _, err := os.Lstat(path); !os.IsNotExist(err) {
						t.Fatal("receipt authorized by an incomplete turn")
					}
				}
			}
			var receipt ConversationCompletion
			if ReadLifecycleControl(path, 16384, &receipt) != nil || ValidateConversationCompletion(receipt, "bare", tape.Producer.Revision, c.OwnerPID, os.Getpid(), tape) != nil {
				t.Fatal("successful forwarded source not proved")
			}
			if ValidateConversationBrokerOwnership(home, "conversation-system-prompt", map[string]int{"bare": os.Getpid() + 1}) == nil {
				t.Fatal("self-claimed broker accepted")
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := observe("stdout", json.RawMessage(`{"type":"rate_limit_event"}`)); err != nil {
				t.Fatal(err)
			}
			after, _ := os.ReadFile(path)
			if string(before) != string(after) {
				t.Fatal("completion receipt changed after publication")
			}
			for _, kind := range []string{"role", "revision", "owner", "broker", "frame-prefix", "frame-overrun", "digest", "changed-final-source", "native-suggestion", "second-user", "failed-result"} {
				t.Run(kind, func(t *testing.T) {
					bad := receipt
					raw, _ := json.Marshal(tape)
					var changed Tape
					_ = json.Unmarshal(raw, &changed)
					switch kind {
					case "role":
						bad.Role = "prompted"
					case "revision":
						bad.Revision = strings.Repeat("f", 40)
					case "owner":
						bad.OwnerPID++
					case "broker":
						bad.BrokerPID++
					case "frame-prefix":
						bad.Frames--
					case "frame-overrun":
						bad.Frames++
					case "digest":
						bad.ExchangeSHA256 = strings.Repeat("a", 64)
					case "changed-final-source":
						changed.Frames[3].Data = json.RawMessage(strings.ReplaceAll(string(changed.Frames[3].Data), "Would you like a Go test?", "A changed complete answer."))
					case "native-suggestion":
						changed.Frames = append(changed.Frames, Frame{Direction: "stdout", Data: json.RawMessage(`{"type":"prompt_suggestion","suggestion":"Different winner"}`)})
					case "second-user":
						changed.Frames = append(changed.Frames, changed.Frames[0])
					case "failed-result":
						changed.Frames[4].Data = json.RawMessage(`{"type":"result","subtype":"error_during_execution","is_error":true}`)
					}
					if ValidateConversationCompletion(bad, "bare", tape.Producer.Revision, c.OwnerPID, os.Getpid(), &changed) == nil {
						t.Fatal("foreign or partial source completed")
					}
				})
			}
		})
	}
}
func TestConversationCompletionNeverInventsMissingOrFailedTurns(t *testing.T) {
	for _, kind := range []string{"incomplete", "error", "native-suggestion", "foreign-role"} {
		t.Run(kind, func(t *testing.T) {
			home := t.TempDir()
			if os.Chmod(home, 0700) != nil {
				t.Fatal("private home")
			}
			tape := fallbackExchangeControl()
			c := LifecycleBrokerConfig{PoolProcessConfig: PoolProcessConfig{Mode: "capture", Scenario: "conversation-system-prompt-bare", Home: home, Path: filepath.Join(home, "bare.json"), Producer: tape.Producer}, OwnerPID: os.Getpid() + 100000}
			switch kind {
			case "incomplete":
				tape.Frames = tape.Frames[:len(tape.Frames)-1]
			case "error":
				tape.Frames[len(tape.Frames)-1].Data = json.RawMessage(`{"type":"result","subtype":"error_during_execution","is_error":true}`)
			case "native-suggestion":
				tape.Frames = append(tape.Frames[:1], append([]Frame{{Direction: "stdout", Data: json.RawMessage(`{"type":"prompt_suggestion","suggestion":"Native winner"}`)}}, tape.Frames[1:]...)...)
			case "foreign-role":
				c.Scenario = "conversation-system-prompt-bootstrap"
			}
			observe := NewConversationCompletionObserver(c, tape)
			for _, frame := range tape.Frames {
				if err := observe(frame.Direction, frame.Data); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := os.Lstat(filepath.Join(home, "bare.completed")); !os.IsNotExist(err) {
				t.Fatal("invented completed source")
			}
			if CheckConversationSettlement(home, "conversation-system-prompt", tape.Producer.Revision, tape.Producer.BinarySHA256, os.Getpid(), nil) == nil {
				t.Fatal("missing actual workers settled")
			}
		})
	}
}
