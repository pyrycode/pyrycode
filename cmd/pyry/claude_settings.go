package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unicode/utf8"

	"github.com/pyrycode/pyrycode/internal/streamsup"
)

// An instance's daemon-wide Claude settings file comes from the
// -pyry-claude-settings flag, then $PYRY_CLAUDE_SETTINGS, then the instance's
// own ~/.pyry/<name>/claude-settings.json when that file exists. The first one
// set wins, the same order as the Claude account source (#2824).
const (
	claudeSettingsFlagName = "pyry-claude-settings"
	claudeSettingsEnv      = "PYRY_CLAUDE_SETTINGS"
	claudeSettingsFileName = "claude-settings.json"
	// claudeSettingsComposedDir holds the per-session merges, one file per
	// session settings file, rewritten on every launch attempt.
	claudeSettingsComposedDir = "claude-settings-composed"
	// maxClaudeSettingsBytes caps one settings file. Hook wiring is a few hundred
	// bytes; anything near this size is refused rather than read in full.
	maxClaudeSettingsBytes = 1 << 20
)

// claudeSettings applies one operator-chosen settings file to every Claude
// session the daemon starts, whatever folder the conversation runs in.
//
// Claude honours only the LAST --settings it is given, and every pool session
// already carries its own --settings file (the MCP enablement booleans, #943).
// A second flag would silently drop one of the two, so the operator file is
// merged into the session's file instead: the top-level keys of both, the
// session's winning on a clash, written to a daemon-owned file that the
// launch's argv then names. A launch with no --settings gets the operator file
// itself.
//
// The file is re-read on every launch attempt, so an edit applies to the next
// Claude start with no daemon restart, and a file that has gone missing or
// stopped parsing refuses the launch rather than starting Claude without it.
// Where the setting comes from is fixed until restart.
type claudeSettings struct {
	path   string
	origin string
	outDir string
	logger *slog.Logger
}

// newClaudeSettings resolves this instance's settings file. Nothing configured
// returns nil, and a nil *claudeSettings changes no launch. A flag or variable
// value that is not one absolute path is a startup error, since it is a
// configuration mistake rather than a file that may yet be fixed. Any composed
// files left by an earlier run are removed: no Claude child of this daemon
// exists yet, so none can be reading one.
func newClaudeSettings(flagValue, envValue, instanceDir string, logger *slog.Logger) (*claudeSettings, error) {
	outDir := filepath.Join(instanceDir, claudeSettingsComposedDir)
	if err := os.RemoveAll(outDir); err != nil {
		return nil, fmt.Errorf("claude settings: remove stale composed settings: %w", err)
	}
	path, origin := flagValue, "flag -"+claudeSettingsFlagName
	if path == "" {
		path, origin = envValue, "env "+claudeSettingsEnv
	}
	if path == "" {
		candidate := filepath.Join(instanceDir, claudeSettingsFileName)
		if _, err := os.Lstat(candidate); errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		path, origin = candidate, "file "+candidate
	}
	if !filepath.IsAbs(path) || hasControlByte(path) {
		return nil, fmt.Errorf("claude settings from %s: want one absolute path to a settings JSON file", origin)
	}
	return &claudeSettings{path: filepath.Clean(path), origin: origin, outDir: outDir, logger: logger}, nil
}

// composer is what the Claude runner factory installs: nil when no settings
// file is configured, so launches keep their argv as built.
func (s *claudeSettings) composer() streamsup.SettingsComposer {
	if s == nil {
		return nil
	}
	return s.compose
}

// check reads the file once at startup so a broken one is reported before the
// first conversation needs Claude. A failure is logged, never fatal: each
// launch re-reads the file and is refused while it stays broken.
func (s *claudeSettings) check() {
	if s == nil {
		return
	}
	if _, err := readSettingsObject(s.path); err != nil {
		s.logger.Warn("claude settings: file unusable; claude launches are refused until it is fixed", "origin", s.origin, "err", err)
		return
	}
	s.logger.Info("claude settings: applied to every claude session", "path", s.path, "origin", s.origin)
}

// compose is the streamsup.SettingsComposer. current is the launch's own
// --settings value: "" when it has none, otherwise the session's settings file
// path or, in principle, an inline JSON object.
func (s *claudeSettings) compose(current string) (string, error) {
	operator, err := readSettingsObject(s.path)
	if err != nil {
		return "", err
	}
	if current == "" {
		return s.path, nil
	}
	var session map[string]json.RawMessage
	if strings.HasPrefix(strings.TrimSpace(current), "{") {
		session, err = parseSettingsObject([]byte(current))
		if err != nil {
			return "", fmt.Errorf("session settings: %w", err)
		}
	} else {
		session, err = readSettingsObject(current)
		if err != nil {
			return "", fmt.Errorf("session settings: %w", err)
		}
	}
	merged := make(map[string]json.RawMessage, len(operator)+len(session))
	for k, v := range operator {
		merged[k] = v
	}
	// The session's keys win: they are what keeps the child from stopping on a
	// startup modal no client can answer.
	for k, v := range session {
		merged[k] = v
	}
	data, err := json.Marshal(merged)
	if err != nil {
		return "", fmt.Errorf("encode merged settings: %w", err)
	}
	sum := sha256.Sum256([]byte(current))
	out := filepath.Join(s.outDir, hex.EncodeToString(sum[:8])+".json")
	if err := writeFileAtomic(s.outDir, out, data); err != nil {
		return "", fmt.Errorf("write merged settings: %w", err)
	}
	return out, nil
}

// readSettingsObject reads one settings file and returns its top-level keys. A
// settings file can define hooks, which run commands as the daemon user, so it
// must be a regular file owned by this user that no group or other user can
// write, the rule claude-account.json follows. Errors name the path and a
// fixed reason.
func readSettingsObject(path string) (map[string]json.RawMessage, error) {
	// O_NONBLOCK keeps a FIFO at this path from blocking the launch before the
	// type check below refuses it.
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("settings file %s: missing", path)
	}
	if err != nil {
		return nil, fmt.Errorf("settings file %s: unreadable", path)
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("settings file %s: unreadable", path)
	}
	switch {
	case !info.Mode().IsRegular():
		return nil, fmt.Errorf("settings file %s: not a regular file", path)
	case !ownedByEUID(info):
		return nil, fmt.Errorf("settings file %s: owned by another user", path)
	case info.Mode().Perm()&0o022 != 0:
		return nil, fmt.Errorf("settings file %s: group or other writable", path)
	}
	data, err := io.ReadAll(io.LimitReader(f, maxClaudeSettingsBytes+1))
	if err != nil {
		return nil, fmt.Errorf("settings file %s: unreadable", path)
	}
	if len(data) > maxClaudeSettingsBytes {
		return nil, fmt.Errorf("settings file %s: larger than %d bytes", path, maxClaudeSettingsBytes)
	}
	obj, err := parseSettingsObject(data)
	if err != nil {
		return nil, fmt.Errorf("settings file %s: %w", path, err)
	}
	return obj, nil
}

// parseSettingsObject accepts exactly one JSON object in valid UTF-8.
func parseSettingsObject(data []byte) (map[string]json.RawMessage, error) {
	var obj map[string]json.RawMessage
	if !utf8.Valid(data) || json.Unmarshal(data, &obj) != nil || obj == nil {
		return nil, errors.New("not a JSON object")
	}
	return obj, nil
}

// writeFileAtomic writes data to path through a scratch file in dir, created
// owner-only, so a child reading path sees either the old complete file or the
// new one, never a prefix.
func writeFileAtomic(dir, path string, data []byte) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".composed-*.json.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(name)
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		_ = os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(name)
		return err
	}
	if err := os.Rename(name, path); err != nil {
		_ = os.Remove(name)
		return err
	}
	return nil
}
