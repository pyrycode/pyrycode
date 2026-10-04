package sessions

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"unicode/utf8"

	"github.com/pyrycode/pyrycode/internal/conversations"
)

const defaultDaemonInstructions = `Keep the main thread free. The operator cannot send you a message while a turn runs. Their messages wait until the turn ends.
- Hand any work longer than a few tool calls to a background subagent. Reply at once with what started, and report the result when it arrives.
- Delegate builds, test runs, device testing, waiting on CI, investigations across several files, ticket filing, design work, log digging and knowledge capture.
- Keep inline only quick work: an answer from context, one lookup, one file read, one small edit.
- In this conversation, responsiveness matters more than token usage.
- Use a cheaper model for routine delegated work.
- Give each subagent a short brief, not the conversation history.
- Check a subagent's claim cheaply before acting on it.
- Never run two subagents on one shared resource, such as a connected device, an emulator or one working tree.`

var (
	ErrDaemonInstructionsTooLong     = errors.New("sessions: daemon instructions exceed the maximum byte length")
	ErrDaemonInstructionsInvalidUTF8 = errors.New("sessions: daemon instructions are not valid UTF-8")
)

// daemonInstructionsStore uses base64-encoded bytes so JSON decoding cannot
// silently repair invalid UTF-8. A pointer distinguishes missing/null from empty.
type daemonInstructionsStore struct {
	Instructions *[]byte `json:"instructions"`
}

func validateDaemonInstructions(text string) error {
	if len(text) > conversations.MaxSystemPromptBytes {
		return ErrDaemonInstructionsTooLong
	}
	if !utf8.ValidString(text) {
		return ErrDaemonInstructionsInvalidUTF8
	}
	return nil
}

// DaemonInstructions returns the current instructions shared by conversations.
func (p *Pool) DaemonInstructions() string {
	p.instructionsMu.RLock()
	defer p.instructionsMu.RUnlock()
	return p.daemonInstructions
}

// DefaultDaemonInstructions returns the exact text a caller can write to reset.
func (p *Pool) DefaultDaemonInstructions() string { return defaultDaemonInstructions }

// SetDaemonInstructions preserves valid text verbatim, including an empty clear.
// Persistence completes before memory publication. It never touches a session or
// its prompt file; edits apply at the next composition/start. It takes no p.mu.
func (p *Pool) SetDaemonInstructions(text string) error {
	if err := validateDaemonInstructions(text); err != nil {
		return err
	}
	p.instructionsMu.Lock()
	defer p.instructionsMu.Unlock()
	if err := p.persistDaemonInstructions(text); err != nil {
		return err
	}
	p.daemonInstructions = text
	return nil
}

func (p *Pool) persistDaemonInstructions(text string) error {
	dir := p.dataDir()
	if dir == "" {
		return nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("sessions: create instructions directory: %w", err)
	}
	raw := []byte(text)
	data, err := json.Marshal(daemonInstructionsStore{Instructions: &raw})
	if err != nil {
		return errors.New("sessions: encode daemon instructions")
	}
	// Reuse the private same-directory tempfile/sync/close/rename recipe. This
	// durable file is outside both transient prompt directories and their cleanup.
	if _, err := writeSystemPromptFile(filepath.Join(dir, "daemon-instructions.json"), string(data)); err != nil {
		return fmt.Errorf("sessions: persist daemon instructions: %w", err)
	}
	return nil
}

// loadDaemonInstructions runs only during New, before the pool is published.
// Only absence seeds a default; an existing invalid or unreadable store is fatal.
func (p *Pool) loadDaemonInstructions() error {
	dir := p.dataDir()
	if dir == "" {
		p.daemonInstructions = defaultDaemonInstructions
		return nil
	}
	path := filepath.Join(dir, "daemon-instructions.json")
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return p.SetDaemonInstructions(defaultDaemonInstructions)
	}
	if err != nil {
		return fmt.Errorf("sessions: inspect daemon instructions: %w", err)
	}
	if !info.Mode().IsRegular() {
		return errors.New("sessions: daemon instructions store is not a regular file")
	}
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("sessions: open daemon instructions: %w", err)
	}
	defer func() { _ = f.Close() }() // read-only descriptor; best-effort cleanup
	// Base64 adds one third; allow envelope overhead while bounding disk reads.
	const maxStoreBytes = 2*conversations.MaxSystemPromptBytes + 128
	data, err := io.ReadAll(io.LimitReader(f, maxStoreBytes+1))
	if err != nil {
		return fmt.Errorf("sessions: read daemon instructions: %w", err)
	}
	if len(data) > maxStoreBytes {
		return ErrDaemonInstructionsTooLong
	}
	var store daemonInstructionsStore
	if !utf8.Valid(data) || json.Unmarshal(data, &store) != nil || store.Instructions == nil {
		// Decoder errors can echo malformed caller bytes; return no parser details.
		return errors.New("sessions: malformed daemon instructions store")
	}
	text := string(*store.Instructions)
	if err := validateDaemonInstructions(text); err != nil {
		return err
	}
	p.daemonInstructions = text
	return nil
}
