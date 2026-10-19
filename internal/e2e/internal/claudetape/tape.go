// Package claudetape validates and replays the Claude side of a test daemon's
// stream-json pipes. It is test infrastructure, never a production transport.
package claudetape

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"regexp"
	"strings"
	"time"
)

const MaxBytes = 8 << 20
const MaxFrames = 10000

type Producer struct {
	Revision      string `json:"revision"`
	BinarySHA256  string `json:"binary_sha256"`
	ClaudeVersion string `json:"claude_version"`
	CapturedAt    string `json:"captured_at"`
}

type Frame struct {
	Direction string          `json:"direction"`
	Data      json.RawMessage `json:"data"`
}

type Tape struct {
	Version  int                        `json:"version"`
	Scenario string                     `json:"scenario"`
	Producer Producer                   `json:"producer"`
	Args     []string                   `json:"args"`
	Frames   []Frame                    `json:"frames"`
	Files    map[string]json.RawMessage `json:"files"`
}

func Decode(r io.Reader) (*Tape, error) {
	raw, err := io.ReadAll(io.LimitReader(r, MaxBytes+1))
	if err != nil || len(raw) > MaxBytes {
		return nil, errors.New("tape read failed or exceeded limit")
	}
	var t Tape
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&t); err != nil {
		return nil, errors.New("invalid tape JSON")
	}
	if d.Decode(new(any)) != io.EOF {
		return nil, errors.New("trailing tape data")
	}
	if err := t.Validate(); err != nil {
		return nil, err
	}
	return &t, nil
}

func (t *Tape) Validate() error {
	if (t.Version != 1 && t.Version != 2) || t.Scenario == "" || len(t.Producer.Revision) != 40 || len(t.Producer.BinarySHA256) != 64 || t.Producer.ClaudeVersion == "" || t.Producer.CapturedAt == "" || len(t.Args) == 0 || len(t.Frames) == 0 || len(t.Frames) > MaxFrames {
		return errors.New("incomplete tape provenance or inventory")
	}
	if raw, err := hex.DecodeString(t.Producer.Revision); err != nil || len(raw) != 20 {
		return errors.New("invalid source revision")
	}
	if raw, err := hex.DecodeString(t.Producer.BinarySHA256); err != nil || len(raw) != 32 {
		return errors.New("invalid producer checksum")
	}
	if _, err := time.Parse(time.RFC3339Nano, t.Producer.CapturedAt); err != nil {
		return errors.New("invalid capture timestamp")
	}
	if (t.Version == 2) != RequiresClientCheckpoint(t.Scenario) {
		return errors.New("checkpoint tape profile is not supported")
	}
	inputs, outputs, actions := 0, 0, 0
	for _, f := range t.Frames {
		if f.Direction == "stdin" {
			inputs++
		} else if f.Direction == "stdout" {
			outputs++
		} else if f.Direction == ClientCheckpointDirection && t.Version == 2 {
			action, err := DecodeClientCheckpoint(f.Data)
			actions++
			if err != nil || action.Sequence != uint64(actions) {
				return errors.New("invalid or nonsequential client checkpoint")
			}
			if t.Scenario == "ordinary-queue-placement" && action.Action != "queue_admitted" {
				return errors.New("wrong checkpoint action for queue profile")
			}
			if t.Scenario == "interrupt-running" && (action.Action != "interrupt_phase" || action.Phase != "responding") {
				return errors.New("wrong running interrupt checkpoint")
			}
			if t.Scenario == "subagent-text" && action.Action != "main_stream_observed" {
				return errors.New("subagent checkpoint action changed")
			}
			if t.Scenario == "interrupt-thinking" && (action.Action != "interrupt_phase" || action.Phase != "thinking") {
				return errors.New("wrong thinking interrupt checkpoint")
			}

		} else {
			return errors.New("unknown tape direction")
		}
		v, err := object(f.Data)
		if err != nil || v["type"] == nil {
			return errors.New("invalid stream-json frame")
		}
	}
	if (t.Version == 2 && actions != 1) || inputs == 0 || outputs == 0 {
		return errors.New("tape has no complete exchange")
	}
	return nil
}

// Save creates a candidate only after the scenario succeeds. Existing reviewed
// fixtures are never overwritten. Failures remove only the new partial file.
func (t *Tape) Save(path string) error {
	if err := t.Validate(); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(t, "", "  ")
	if err != nil || len(raw) > MaxBytes {
		return errors.New("tape serialization exceeded limit")
	}
	if err := Safe(raw); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return errors.New("candidate already exists or cannot be created")
	}
	success := false
	defer func() {
		_ = f.Close()
		if !success {
			_ = os.Remove(path)
		}
	}()
	if _, err = f.Write(append(raw, '\n')); err != nil {
		return errors.New("candidate write failed")
	}
	if err = f.Sync(); err != nil {
		return errors.New("candidate sync failed")
	}
	if err = f.Close(); err != nil {
		return errors.New("candidate close failed")
	}
	success = true
	return nil
}

var credentialSourceLabel = regexp.MustCompile(`"(?:tokenSource|apiKeySource)"\s*:\s*"(?:CLAUDE_CODE_OAUTH_TOKEN|ANTHROPIC_API_KEY)"`)

// Safe rejects credentials and unnormalized operator paths. Error messages never
// include source bytes. Captures come from controlled disposable workspaces only.
func Safe(raw []byte) error {
	// These exact values identify the credential source, never its value. Keep
	// the original bytes in the tape; omit only the known label from this scan.
	scan := credentialSourceLabel.ReplaceAll(raw, []byte(`"credentialSourceLabel":"known-source"`))
	lower := strings.ToLower(string(scan))
	for _, needle := range []string{"sk-ant-", "bearer ", "access_token", "refresh_token", "api_key", "oauth_token", "password", "/users/", "/home/"} {
		if strings.Contains(lower, needle) {
			return fmt.Errorf("candidate contains private data (%s)", needle)
		}
	}
	for _, key := range []string{"CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_API_KEY"} {
		if value := os.Getenv(key); len(value) >= 8 && bytes.Contains(raw, []byte(value)) {
			return errors.New("candidate contains configured credential")
		}
	}
	return nil
}

// Player releases only the output prefix following an exactly matched input.
// There is no query reordering, optional frame or fallback to a live producer.
type Player struct {
	tape     *Tape
	next     int
	bindings map[string]string
	used     map[string]string
	err      error
}

func New(t *Tape, args []string) (*Player, error) {
	if err := t.Validate(); err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(t.Args, args) {
		return nil, errors.New("claude spawn arguments changed")
	}
	return &Player{tape: t, bindings: map[string]string{}, used: map[string]string{}}, nil
}

func (p *Player) Prefix() []json.RawMessage {
	if p.err != nil {
		return nil
	}
	var out []json.RawMessage
	for p.next < len(p.tape.Frames) && p.tape.Frames[p.next].Direction == "stdout" {
		f := p.tape.Frames[p.next]
		var v any
		d := json.NewDecoder(bytes.NewReader(f.Data))
		d.UseNumber()
		if err := d.Decode(&v); err != nil {
			p.err = errors.New("invalid recorded output")
			return nil
		}
		v = rewrite(v, p.bindings)
		raw, err := json.Marshal(v)
		if err != nil {
			p.err = errors.New("invalid bound output")
			return nil
		}
		out = append(out, raw)
		p.next++
	}
	return out
}

func (p *Player) Input(raw []byte) ([]json.RawMessage, error) {
	if p.err != nil {
		return nil, p.err
	}
	fail := func() ([]json.RawMessage, error) {
		// Name only protocol labels, never payload bytes: a reordering is then
		// readable from a release log without the tape or the daemon's stdin.
		want := "end of tape"
		if p.next < len(p.tape.Frames) {
			want = frameLabel(p.tape.Frames[p.next].Direction, p.tape.Frames[p.next].Data)
		}
		p.err = fmt.Errorf("claude input contract changed at frame %d: want %s, got %s", p.next, want, frameLabel("stdin", raw))
		return nil, p.err
	}
	if p.next >= len(p.tape.Frames) || p.tape.Frames[p.next].Direction != "stdin" {
		return fail()
	}
	want, err := object(p.tape.Frames[p.next].Data)
	if err != nil {
		return fail()
	}
	got, err := object(raw)
	if err != nil {
		return fail()
	}
	// Only daemon-minted control request IDs may vary. Claude-minted permission
	// IDs are replayed literally and their response correlation stays exact.
	if want["type"] == "control_request" && got["type"] == "control_request" {
		expected, a := want["request_id"].(string)
		actual, b := got["request_id"].(string)
		if !a || !b || expected == "" || actual == "" {
			return fail()
		}
		if bound, ok := p.bindings[expected]; ok && bound != actual {
			return fail()
		}
		if owner, ok := p.used[actual]; ok && owner != expected {
			return fail()
		}
		p.bindings[expected] = actual
		p.used[actual] = expected
	}
	want = rewrite(want, p.bindings).(map[string]any)
	if !reflect.DeepEqual(want, got) {
		return fail()
	}
	p.next++
	out := p.Prefix()
	return out, p.err
}

// AwaitsTurn reports whether every frame before the next daemon user turn has
// been replayed. The recorded order of the daemon's startup requests and its
// first turn is a timing outcome of the capture, not a daemon guarantee: turns
// may flow once claude acknowledges the permission posture, while the MCP status
// ask follows the initialize reply. A driver that sends its first message only
// after this point reproduces the recorded order on any machine.
func (p *Player) AwaitsTurn() bool {
	if p.err != nil || p.next >= len(p.tape.Frames) || p.tape.Frames[p.next].Direction != "stdin" {
		return false
	}
	v, err := object(p.tape.Frames[p.next].Data)
	return err == nil && v["type"] == "user"
}

func (p *Player) Complete() error {
	if p.err != nil {
		return p.err
	}
	if p.next != len(p.tape.Frames) {
		return fmt.Errorf("unconsumed Claude frames: %d", len(p.tape.Frames)-p.next)
	}
	return nil
}

// frameLabel names a frame by direction, type and control subtype only.
func frameLabel(direction string, raw []byte) string {
	v, err := object(raw)
	if err != nil {
		return direction + " invalid JSON"
	}
	label := direction
	if kind, ok := v["type"].(string); ok {
		label += " " + kind
	}
	if request, ok := v["request"].(map[string]any); ok {
		if subtype, ok := request["subtype"].(string); ok {
			label += " " + subtype
		}
	}
	return label
}

func object(raw []byte) (map[string]any, error) {
	var v map[string]any
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if err := d.Decode(&v); err != nil || v == nil {
		return nil, errors.New("expected JSON object")
	}
	if d.Decode(new(any)) != io.EOF {
		return nil, errors.New("trailing JSON data")
	}
	return v, nil
}

// rewrite changes only protocol correlation fields. Tool data and other payloads
// can contain request_id keys whose values must remain part of the exact contract.
func rewrite(v any, bindings map[string]string) any {
	envelope, ok := v.(map[string]any)
	if !ok {
		return v
	}
	identity := envelope
	switch envelope["type"] {
	case "control_request":
	case "control_response":
		identity, ok = envelope["response"].(map[string]any)
		if !ok {
			return v
		}
	default:
		return v
	}
	if id, ok := identity["request_id"].(string); ok {
		if bound, exists := bindings[id]; exists {
			identity["request_id"] = bound
		}
	}
	return v
}

// SameContract compares complete daemon requests in order. Only control IDs
// minted by the two producers are rebound by their observed origin and ordinal.
// Different provider prose and accounting do not change this request contract.
func SameContract(a, b *Tape) error {
	contract := func(t *Tape) ([]string, error) {
		if err := t.Validate(); err != nil {
			return nil, err
		}
		ids := map[string]string{}
		counters := map[string]int{}
		var requests []string
		for _, f := range t.Frames {
			v, err := object(f.Data)
			if err != nil {
				return nil, err
			}
			if v["type"] == "control_request" {
				id, ok := v["request_id"].(string)
				if !ok || id == "" {
					return nil, errors.New("control request lacks identity")
				}
				if _, exists := ids[id]; exists {
					return nil, errors.New("control request repeats identity")
				}
				counters[f.Direction]++
				ids[id] = fmt.Sprintf("${%s-control-%d}", f.Direction, counters[f.Direction])
			}
			if f.Direction == ClientCheckpointDirection {
				// Action identifiers never participate in native control bindings.
				raw, err := json.Marshal(v)
				if err != nil {
					return nil, err
				}
				requests = append(requests, ClientCheckpointDirection+":"+string(raw))
			}
			if f.Direction == "stdin" {
				raw, err := json.Marshal(rewrite(v, ids))
				if err != nil {
					return nil, err
				}
				requests = append(requests, string(raw))
			}
		}
		return requests, nil
	}
	if a.Version != b.Version || a.Scenario != b.Scenario || !reflect.DeepEqual(a.Args, b.Args) || a.CheckFiles(b.Files) != nil {
		return errors.New("scenario or Claude arguments changed")
	}
	left, err := contract(a)
	if err != nil {
		return err
	}
	right, err := contract(b)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(left, right) {
		return errors.New("ordered daemon request contract changed")
	}
	return nil
}

// CheckFiles compares parsed input-file JSON, including string-valued system
// prompts. Presentation whitespace cannot change a file's logical contract.
func (t *Tape) CheckFiles(actual map[string]json.RawMessage) error {
	if len(t.Files) != len(actual) {
		return errors.New("claude input file inventory changed")
	}
	canonical := func(raw []byte) ([]byte, error) {
		d := json.NewDecoder(bytes.NewReader(raw))
		d.UseNumber()
		var value any
		if err := d.Decode(&value); err != nil {
			return nil, err
		}
		if d.Decode(new(any)) != io.EOF {
			return nil, errors.New("trailing file JSON")
		}
		return json.Marshal(value)
	}
	for key, want := range t.Files {
		got, ok := actual[key]
		if !ok {
			return errors.New("claude input file missing")
		}
		left, a := canonical(want)
		right, b := canonical(got)
		if a != nil || b != nil || !bytes.Equal(left, right) {
			return errors.New("claude input file contents changed")
		}
	}
	return nil
}
