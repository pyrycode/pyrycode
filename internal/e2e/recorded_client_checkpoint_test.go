//go:build e2e

package e2e

import (
	"encoding/json"
	"errors"

	"github.com/pyrycode/pyrycode/internal/e2e/internal/claudetape"
)

type recordedCheckpointState struct {
	dir      string
	accepted [][]byte
}

// poll never waits for an action during capture. Real stdout continues normally;
// only replay's player holds its output tail behind the typed observation.
func (s *recordedCheckpointState) poll(tape *claudetape.Tape, player *claudetape.Player, normalize func([]byte) ([]byte, error), emit func([]json.RawMessage) error, totalBytes, frames *int) error {
	required := claudetape.RequiresClientCheckpoint(tape.Scenario)
	if (s.dir != "") != required || (tape.Version == 2) != required {
		return errors.New("client checkpoint profile configuration changed")
	}
	if !required {
		return nil
	}
	raw, exists, err := claudetape.ReadCheckpoint(s.dir, uint64(len(s.accepted)+1))
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}
	if len(s.accepted) != 0 {
		return errors.New("extra client checkpoint")
	}
	normalized, err := normalize(raw)
	if err != nil {
		return err
	}
	if err = claudetape.Safe(normalized); err != nil {
		return err
	}
	if player != nil {
		outputs, err := player.Action(normalized)
		if err != nil {
			return err
		}
		*frames++
		if err = emit(outputs); err != nil {
			return err
		}
	} else {
		*totalBytes += len(normalized)
		if *totalBytes > claudetape.MaxBytes || len(tape.Frames) >= claudetape.MaxFrames {
			return errors.New("capture exceeded bound")
		}
		tape.Frames = append(tape.Frames, claudetape.Frame{Direction: claudetape.ClientCheckpointDirection, Data: normalized})
		*frames++
	}
	s.accepted = append(s.accepted, append([]byte(nil), raw...))
	return claudetape.AcknowledgeCheckpoint(s.dir, raw)
}
func (s *recordedCheckpointState) seal() error {
	if s.dir == "" {
		return nil
	}
	if len(s.accepted) != 1 {
		return errors.New("client checkpoint did not complete")
	}
	_, err := claudetape.SealCheckpointDirectory(s.dir, s.accepted)
	return err
}
