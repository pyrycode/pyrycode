package claudetape

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"reflect"
	"strings"
	"time"
	"unicode/utf8"
)

type CLIFrame struct {
	Stream string `json:"stream"`
	Data   string `json:"data"`
}
type CLISnapshot struct {
	Exists bool   `json:"exists"`
	Mode   uint32 `json:"mode"`
	Data   string `json:"data"`
}

// CLITape captures one native CLI invocation including its one input followed
// by EOF, both output streams, actual process status and actual session JSONL.
// Transcript bytes retain their encoding; only declared filesystem paths bind.
type CLITape struct {
	Version      int                        `json:"version"`
	Scenario     string                     `json:"scenario"`
	Producer     Producer                   `json:"producer"`
	DaemonSHA256 string                     `json:"daemon_sha256"`
	Args         []string                   `json:"args"`
	Files        map[string]json.RawMessage `json:"files"`
	Input        string                     `json:"input"`
	InputEOF     bool                       `json:"input_eof"`
	Frames       []CLIFrame                 `json:"frames"`
	ExitCode     int                        `json:"exit_code"`
	SessionID    string                     `json:"session_id"`
	Snapshot     CLISnapshot                `json:"snapshot"`
}

func CLIProfiles() map[string]string {
	return map[string]string{
		"cli-prompt-ascii":       "TestRealClaude_PromptFidelity",
		"cli-prompt-unicode":     "TestRealClaude_PromptFidelity_Unicode",
		"cli-doctor":             "TestRealClaude_DoctorPoisoningRegression",
		"cli-bash-nonzero":       "TestRealClaude_BashTool_NonZeroExit",
		"cli-max-turns":          "TestRealClaude_MaxTurnsHonored",
		"cli-large-prompt":       "TestRealClaude_LargePromptNearContextWindow",
		"cli-long-session":       "TestRealClaude_LongSessionJSONLIntegrity",
		"cli-tool-loop":          "TestRealClaude_ToolLoopIntegrity",
		"cli-role-po":            "TestRealClaude_PO_RoleLoop",
		"cli-role-architect":     "TestRealClaude_Architect_RoleLoop",
		"cli-role-developer":     "TestRealClaude_Developer_RoleLoop",
		"cli-role-review":        "TestRealClaude_CodeReview_RoleLoop",
		"cli-role-documentation": "TestRealClaude_Documentation_RoleLoop",
	}
}
func (t *CLITape) Validate() error {
	fail := errors.New("incomplete CLI recording contract")
	if t.Version != 1 || CLIProfiles()[t.Scenario] == "" || len(t.Args) == 0 || len(t.Files) == 0 || !t.InputEOF || t.Input == "" || !utf8.ValidString(t.Input) || len(t.Frames) == 0 || len(t.Frames) > MaxFrames || t.ExitCode < 0 || t.ExitCode > 255 {
		return fail
	}
	if validateCLISavedArgs(t.Args) != nil {
		return fail
	}
	for _, v := range []struct {
		value  string
		length int
	}{{t.Producer.Revision, 20}, {t.Producer.BinarySHA256, 32}, {t.DaemonSHA256, 32}} {
		raw, err := hex.DecodeString(v.value)
		if err != nil || len(raw) != v.length {
			return fail
		}
	}
	if t.Producer.ClaudeVersion == "" {
		return fail
	}
	if _, err := time.Parse(time.RFC3339Nano, t.Producer.CapturedAt); err != nil {
		return fail
	}
	for _, flag := range []string{"--settings", "--append-system-prompt-file", "$cwd"} {
		if len(t.Files[flag]) == 0 {
			return fail
		}
	}
	v, err := object([]byte(t.Input))
	if err != nil || v["type"] != "user" {
		return fail
	}
	initCount := 0
	session := ""
	for _, frame := range t.Frames {
		if frame.Data == "" || !utf8.ValidString(frame.Data) {
			return fail
		}
		switch frame.Stream {
		case "stdout":
			v, err := object([]byte(strings.TrimSpace(frame.Data)))
			if err != nil {
				return fail
			}
			if v["type"] == "system" && v["subtype"] == "init" {
				initCount++
				id, ok := v["session_id"].(string)
				if !ok || !poolUUID.MatchString(id) {
					return fail
				}
				session = id
			}
		case "stderr":
		default:
			return fail
		}
	}
	if initCount > 1 || session != t.SessionID {
		return fail
	}
	if t.Scenario != "cli-large-prompt" && (initCount != 1 || !t.Snapshot.Exists) {
		return fail
	}
	if t.Snapshot.Exists {
		if !poolUUID.MatchString(t.SessionID) || t.Snapshot.Mode == 0 || t.Snapshot.Mode > 0777 || t.Snapshot.Data == "" || !utf8.ValidString(t.Snapshot.Data) {
			return fail
		}
		for _, line := range strings.Split(strings.TrimSuffix(t.Snapshot.Data, "\n"), "\n") {
			if !json.Valid([]byte(line)) {
				return fail
			}
		}
	} else if t.Snapshot.Mode != 0 || t.Snapshot.Data != "" {
		return fail
	}
	raw, err := json.Marshal(t)
	if err != nil || len(raw) > MaxBytes {
		return fail
	}
	return t.safeFields()
}
func DecodeCLI(r io.Reader) (*CLITape, error) {
	raw, err := io.ReadAll(io.LimitReader(r, MaxBytes+1))
	if err != nil || len(raw) > MaxBytes {
		return nil, errors.New("CLI recording exceeds limit")
	}
	var t CLITape
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if !uniqueSuiteKeys(raw) || d.Decode(&t) != nil || d.Decode(new(any)) != io.EOF {
		return nil, errors.New("invalid CLI recording JSON")
	}
	if err = t.Validate(); err != nil {
		return nil, err
	}
	return &t, nil
}
func (t *CLITape) Save(path string) error {
	if err := t.Validate(); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(t, "", "  ")
	if err != nil || len(raw) > MaxBytes {
		return errors.New("CLI candidate exceeds limit")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return errors.New("CLI candidate exists or cannot be created")
	}
	ok := false
	defer func() {
		_ = f.Close()
		if !ok {
			_ = os.Remove(path)
		}
	}()
	if _, err = f.Write(append(raw, '\n')); err != nil {
		return err
	}
	if f.Sync() != nil || f.Close() != nil {
		return errors.New("CLI candidate close failed")
	}
	ok = true
	return nil
}
func SameCLIContract(a, b *CLITape) error {
	fail := errors.New("CLI outgoing input contract changed")
	if a.Validate() != nil || b.Validate() != nil || a.Scenario != b.Scenario || !reflect.DeepEqual(a.Args, b.Args) || (&Tape{Files: a.Files}).CheckFiles(b.Files) != nil || a.InputEOF != b.InputEOF {
		return fail
	}
	x, err := object([]byte(a.Input))
	if err != nil {
		return fail
	}
	y, err := object([]byte(b.Input))
	if err != nil || !reflect.DeepEqual(x, y) {
		return fail
	}
	return nil
}

// Scan decoded carriers rather than the outer JSON string encoding. Native
// credential-source labels remain exact source labels; escaping a native JSON
// line inside a frame must not turn that name into a false credential finding.
// Every textual field and file key/value is checked; unknown fields are closed
// by DecodeCLI before this typed audit.

func (t *CLITape) safeFields() error {
	metadata, _ := json.Marshal(struct {
		Scenario     string
		Producer     Producer
		DaemonSHA256 string
		Args         []string
		SessionID    string
	}{t.Scenario, t.Producer, t.DaemonSHA256, t.Args, t.SessionID})
	if err := safeCLIJSON(metadata); err != nil {
		return err
	}
	files, _ := json.Marshal(t.Files)
	if err := safeCLIJSON(files); err != nil {
		return err
	}
	if err := safeCLIJSON([]byte(t.Input)); err != nil {
		return err
	}
	for _, frame := range t.Frames {
		if frame.Stream == "stdout" {
			if err := safeCLIJSON([]byte(frame.Data)); err != nil {
				return err
			}
		} else if err := Safe([]byte(frame.Data)); err != nil {
			return err
		}
	}
	for _, line := range strings.Split(strings.TrimSuffix(t.Snapshot.Data, "\n"), "\n") {
		if line != "" {
			if err := safeCLIJSON([]byte(line)); err != nil {
				return err
			}
		}
	}
	return nil
}
func safeCLIJSON(raw []byte) error {
	// A map decoder keeps only the last repeated key. Reject ambiguity before
	// decoding so an earlier escaped credential cannot disappear from the audit
	// while its original bytes remain in a recorded stream or transcript.
	if !uniqueSuiteKeys(raw) {
		return errors.New("duplicate or deeply nested CLI privacy carrier")
	}
	var value any
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if d.Decode(&value) != nil || d.Decode(new(any)) != io.EOF {
		return errors.New("invalid CLI privacy carrier")
	}
	var visit func(any, int) error
	visit = func(v any, depth int) error {
		if depth > 64 {
			return errors.New("CLI privacy nesting exceeded")
		}
		switch x := v.(type) {
		case map[string]any:
			for key, value := range x {
				if err := Safe([]byte(key)); err != nil {
					return err
				}
				if (key == "tokenSource" || key == "apiKeySource") && (value == "CLAUDE_CODE_OAUTH_TOKEN" || value == "ANTHROPIC_API_KEY") {
					continue
				}
				if err := visit(value, depth+1); err != nil {
					return err
				}
			}
		case []any:
			for _, item := range x {
				if err := visit(item, depth+1); err != nil {
					return err
				}
			}
		case string:
			if err := Safe([]byte(x)); err != nil {
				return err
			}
			if json.Valid([]byte(x)) {
				if !uniqueSuiteKeys([]byte(x)) {
					return errors.New("duplicate nested CLI privacy carrier")
				}
				var nested any
				decoder := json.NewDecoder(strings.NewReader(x))
				decoder.UseNumber()
				if decoder.Decode(&nested) == nil {
					if err := visit(nested, depth+1); err != nil {
						return err
					}
				}
			}
		}
		return nil
	}
	return visit(value, 0)
}
