package claudetape

import (
	"bytes"
	"encoding/json"
	"errors"
)

// The resume file is a complete native output of the preceding owned role.
// It remains literal in fixtures and in every replay launch. Only cross-fresh
// comparison may replace its bytes with the closed predecessor reference.
func validateLifecycleResumeInput(tape *Tape, source SessionSnapshot) error {
	fail := errors.New("resume input is not its complete owned predecessor output")
	if tape == nil || tape.Validate() != nil {
		return fail
	}
	ids := 0
	for i, arg := range tape.Args {
		if arg == "--session-id" {
			return fail
		}
		if arg == "--resume" {
			ids++
			if i+1 >= len(tape.Args) || tape.Args[i+1] != daemonSession {
				return fail
			}
		}
	}
	if ids != 1 {
		return fail
	}
	snapshot, err := DecodeSessionSnapshot(bytes.NewReader(tape.Files["$resume-input"]))
	if err != nil || snapshot != source {
		return fail
	}
	return nil
}
func lifecycleResumeContract(tape *Tape, source SessionSnapshot) (*Tape, error) {
	if err := validateLifecycleResumeInput(tape, source); err != nil {
		return nil, err
	}
	copyTape := *tape
	copyTape.Files = map[string]json.RawMessage{}
	for name, value := range tape.Files {
		copyTape.Files[name] = append(json.RawMessage(nil), value...)
	}
	copyTape.Files["$resume-input"] = json.RawMessage(`{"source_role":"bootstrap","kind":"complete-native-session-jsonl","mode":384}`)
	if err := copyTape.Validate(); err != nil {
		return nil, err
	}
	return &copyTape, nil
}
