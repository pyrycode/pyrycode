package claudetape

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// AuditDaemonSuiteArtifacts is called before the immutable seal receipt and
// again after the complete original test process/cleanup exits. Its digest pins
// every role binding and sealed child artifact across that interval.
func AuditDaemonSuiteArtifacts(home, scenario string) (string, error) {
	fail := errors.New("daemon suite artifact inventory changed")
	profile, ok := DaemonSuiteProfiles()[scenario]
	if !ok {
		return "", fail
	}
	if _, err := os.Lstat(filepath.Join(home, "recording-unexpected")); !errors.Is(err, os.ErrNotExist) {
		return "", fail
	}
	hashes := map[string]string{}
	read := func(path string, limit int) ([]byte, error) {
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > int64(limit) {
			return nil, fail
		}
		f, err := os.Open(path)
		if err != nil {
			return nil, fail
		}
		opened, statErr := f.Stat()
		raw, readErr := io.ReadAll(io.LimitReader(f, int64(limit)+1))
		closeErr := f.Close()
		if statErr != nil || !os.SameFile(info, opened) || readErr != nil || closeErr != nil || len(raw) > limit {
			return nil, fail
		}
		relative, err := filepath.Rel(home, path)
		if err != nil {
			return nil, fail
		}
		sum := sha256.Sum256(raw)
		hashes[relative] = hex.EncodeToString(sum[:])
		return raw, nil
	}
	decode := func(raw []byte, target any) bool {
		d := json.NewDecoder(bytes.NewReader(raw))
		d.DisallowUnknownFields()
		return uniqueSuiteKeys(raw) && d.Decode(target) == nil && d.Decode(new(any)) == io.EOF
	}
	type roleBinding struct{ Role, ConversationID, SessionID, WorkDir string }
	workdirs := map[string]string{"bootstrap": filepath.Join(home, "work")}
	sessions, conversations := map[string]bool{}, map[string]bool{}
	for _, part := range []string{"recording-roles", "recording-children"} {
		info, err := os.Lstat(filepath.Join(home, part))
		if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
			return "", fail
		}
	}
	entries, err := os.ReadDir(filepath.Join(home, "recording-roles"))
	if err != nil || len(entries) != len(profile.Roles)-1 {
		return "", fail
	}
	expectedRoles := map[string]bool{}
	for _, role := range profile.Roles[1:] {
		expectedRoles[role+".json"] = true
	}
	for _, entry := range entries {
		if !expectedRoles[entry.Name()] {
			return "", fail
		}
		raw, err := read(filepath.Join(home, "recording-roles", entry.Name()), 16384)
		if err != nil {
			return "", err
		}
		var role roleBinding
		if !decode(raw, &role) || role.Role+".json" != entry.Name() || !poolUUID.MatchString(role.SessionID) || role.SessionID == "88888888-8888-4888-8888-888888888888" || role.ConversationID == "" || sessions[role.SessionID] || conversations[role.ConversationID] {
			return "", fail
		}
		rel, err := filepath.Rel(home, role.WorkDir)
		if !filepath.IsAbs(role.WorkDir) || err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return "", fail
		}
		sessions[role.SessionID], conversations[role.ConversationID] = true, true
		workdirs[role.Role] = role.WorkDir
	}
	raw, err := read(filepath.Join(home, "recording-endpoint.json"), 16384)
	if err != nil {
		return "", err
	}
	var endpoint struct{ Daemon, Socket string }
	if !decode(raw, &endpoint) || !filepath.IsAbs(endpoint.Daemon) || !filepath.IsAbs(endpoint.Socket) {
		return "", fail
	}
	children, err := os.ReadDir(filepath.Join(home, "recording-children"))
	if err != nil || len(children) != 3*len(profile.Roles) {
		return "", fail
	}
	expectedChildren := map[string]bool{}
	for _, role := range profile.Roles {
		for _, suffix := range []string{".json", ".claim", ".receipt"} {
			expectedChildren[role+suffix] = true
		}
	}
	for _, child := range children {
		if !expectedChildren[child.Name()] {
			return "", fail
		}
	}
	for _, role := range profile.Roles {
		prefix := filepath.Join(home, "recording-children", role)
		claim, err := read(prefix+".claim", 0)
		if err != nil || len(claim) != 0 {
			return "", fail
		}
		raw, err := read(prefix+".json", MaxBytes)
		if err != nil {
			return "", err
		}
		tape, err := Decode(bytes.NewReader(raw))
		if err != nil || tape.Scenario != scenario+"-"+role {
			return "", fail
		}
		var cwd string
		if json.Unmarshal(tape.Files["$cwd"], &cwd) != nil || filepath.Join(home, cwd) != workdirs[role] {
			return "", fail
		}
		raw, err = read(prefix+".receipt", 16384)
		if err != nil {
			return "", err
		}
		var receipt struct {
			Success bool   `json:"success"`
			Frames  int    `json:"frames"`
			Error   string `json:"error,omitempty"`
		}
		if !decode(raw, &receipt) || !receipt.Success || receipt.Error != "" || receipt.Frames != len(tape.Frames) {
			return "", fail
		}
	}
	raw, err = json.Marshal(hashes)
	if err != nil {
		return "", fail
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}
