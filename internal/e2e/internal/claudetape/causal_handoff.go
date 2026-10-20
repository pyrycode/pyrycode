package claudetape

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"

	"github.com/pyrycode/pyrycode/internal/sessions"
	"github.com/pyrycode/pyrycode/internal/streamsup"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// lifecycleHandoffText derives the successful wrap-up text through the actual
// production stream parser. Its sole eligible turn is the first user request
// after the original typed rotation checkpoint. No other output may bind an
// outgoing request. Long/truncated and failed notes are unsupported and fail.
func lifecycleHandoffText(tape *Tape) (string, error) {
	fail := errors.New("unproved native handoff dependency")
	if tape == nil || tape.Validate() != nil {
		return "", fail
	}
	checkpoint, armed, ended, bad := false, false, false, false
	users, checkpoints := 0, 0
	var note strings.Builder
	parser := streamsup.NewParser(func(event turnevent.Event) {
		if !armed {
			return
		}
		switch ev := event.(type) {
		case turnevent.TextChunk:
			if len(ev.Text) >= sessions.MaxHandoffNoteBytes-note.Len() {
				bad = true
				return
			}
			note.WriteString(ev.Text)
		case turnevent.TurnEnd:
			armed = false
			ended = true
			if ev.IsError || ev.Reason == turnevent.TurnEndReasonCancelled || ev.Reason == turnevent.TurnEndReasonRefusal {
				bad = true
			}
		}
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	for _, frame := range tape.Frames {
		switch frame.Direction {
		case ClientCheckpointDirection:
			action, err := DecodeClientCheckpoint(frame.Data)
			if err != nil || action.Action != "lifecycle_rotate" {
				return "", fail
			}
			checkpoints++
			checkpoint = true
		case "stdin":
			env, err := object(frame.Data)
			if err != nil {
				return "", fail
			}
			if checkpoint && env["type"] == "user" {
				users++
				if users != 1 || ended {
					return "", fail
				}
				armed = true
			}
		case "stdout":
			// Fixture carriers are pretty-printed. Reconstitute one complete
			// native JSON object per line for the production NDJSON parser;
			// structural presentation whitespace is not a provider event boundary.
			var compact bytes.Buffer
			if json.Compact(&compact, frame.Data) != nil {
				return "", fail
			}
			raw := append(compact.Bytes(), '\n')
			if _, err := parser.Write(raw); err != nil {
				return "", fail
			}
		}
	}
	if checkpoints != 1 || users != 1 || !ended || armed || bad {
		return "", fail
	}
	text := note.String()
	if _, ok := sessions.FencedHandoffNote(text); !ok {
		return "", fail
	}
	return text, nil
}

// lifecycleHandoffContract operates only on a comparison copy. Stored fixtures
// and replay still check the complete literal prompt. The only fresh-capture
// binding is the exact fenced note independently derived from that capture's
// predecessor outputs. Every surrounding byte and the file mode stay strict.
func lifecycleHandoffContract(successor, predecessor *Tape) (*Tape, error) {
	fail := errors.New("native handoff did not reach complete successor prompt")
	if successor == nil || successor.Validate() != nil {
		return nil, fail
	}
	note, err := lifecycleHandoffText(predecessor)
	if err != nil {
		return nil, err
	}
	fenced, ok := sessions.FencedHandoffNote(note)
	if !ok {
		return nil, fail
	}
	const marker = "${CAUSAL_HANDOFF_FROM_BOUND}"
	var file struct {
		Content string `json:"content"`
		Mode    uint32 `json:"mode"`
	}
	raw := successor.Files["--append-system-prompt-file"]
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if !uniqueSuiteKeys(raw) || decoder.Decode(&file) != nil || decoder.Decode(new(any)) != io.EOF || strings.Contains(file.Content, marker) || strings.Contains(note, marker) || strings.Count(file.Content, fenced) != 1 {
		return nil, fail
	}
	normalized, _ := sessions.FencedHandoffNote(marker)
	file.Content = strings.Replace(file.Content, fenced, normalized, 1)
	encoded, err := json.Marshal(file)
	if err != nil {
		return nil, fail
	}
	copyTape := *successor
	copyTape.Files = map[string]json.RawMessage{}
	for key, value := range successor.Files {
		copyTape.Files[key] = append(json.RawMessage(nil), value...)
	}
	copyTape.Files["--append-system-prompt-file"] = encoded
	if copyTape.Validate() != nil {
		return nil, fail
	}
	return &copyTape, nil
}
