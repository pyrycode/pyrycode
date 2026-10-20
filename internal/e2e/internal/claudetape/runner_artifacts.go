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
	"syscall"
)

const RunnerMetadataDirectory = ".pyry-runner-recording"

func runnerPrivateRead(path string, limit int) ([]byte, error) {
	return runnerPrivateReadWithOpen(path, limit, os.OpenFile)
}

func runnerPrivateReadWithOpen(path string, limit int, openFile func(string, int, os.FileMode) (*os.File, error)) ([]byte, error) {
	fail := errors.New("runner artifact changed")
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode() != 0600 || info.Size() > int64(limit) {
		return nil, fail
	}
	// Reject replaced symlinks and avoid waiting for a replaced FIFO's writer.
	f, err := openFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, fail
	}
	opened, statErr := f.Stat()
	if statErr != nil || !opened.Mode().IsRegular() || opened.Mode() != 0600 || opened.Size() > int64(limit) || !os.SameFile(info, opened) {
		_ = f.Close() // The artifact is already rejected; retain the generic error.
		return nil, fail
	}
	raw, readErr := io.ReadAll(io.LimitReader(f, int64(limit)+1))
	closeErr := f.Close()
	if readErr != nil || closeErr != nil || len(raw) > limit {
		return nil, fail
	}
	return raw, nil
}
func AuditRunnerRole(home, scenario, role string) (string, error) {
	digest, _, err := auditRunnerRole(home, scenario, role)
	return digest, err
}
func auditRunnerRole(home, scenario, role string) (string, *Tape, error) {
	fail := errors.New("runner role artifact inventory changed")
	profile, ok := RunnerProfiles()[scenario]
	if !ok {
		return "", nil, fail
	}
	owned, ok := profile.Roles[role]
	if !ok {
		return "", nil, fail
	}
	dir := filepath.Join(home, RunnerMetadataDirectory)
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		return "", nil, fail
	}
	if _, err := os.Lstat(filepath.Join(dir, "unexpected")); !errors.Is(err, os.ErrNotExist) {
		return "", nil, fail
	}
	values := map[string][]byte{}
	hashes := map[string]string{}
	for _, suffix := range []string{".claim", ".finish", ".json", ".receipt"} {
		limit := MaxBytes
		if suffix != ".json" {
			limit = 16384
		}
		raw, err := runnerPrivateRead(filepath.Join(dir, role+suffix), limit)
		if err != nil {
			return "", nil, err
		}
		values[suffix] = raw
		sum := sha256.Sum256(raw)
		hashes[suffix] = hex.EncodeToString(sum[:])
	}
	if len(values[".claim"]) != 0 || len(values[".finish"]) != 0 {
		return "", nil, fail
	}
	tape, err := Decode(bytes.NewReader(values[".json"]))
	if err != nil || tape.Scenario != scenario+"-"+role {
		return "", nil, fail
	}
	var cwd string
	if json.Unmarshal(tape.Files["$cwd"], &cwd) != nil || cwd != owned.Workdir {
		return "", nil, fail
	}
	concrete := append([]string(nil), tape.Args...)
	for i, arg := range concrete {
		if arg == "--session-id" && i+1 < len(concrete) {
			if concrete[i+1] != "${RUNNER_SESSION}" {
				return "", nil, fail
			}
			concrete[i+1] = owned.SessionID
		}
	}
	binding, err := NewRunnerBinding(scenario, role, concrete, home, filepath.Join(home, owned.Workdir))
	if err != nil {
		return "", nil, fail
	}
	argsRaw, _ := json.Marshal(binding.Args())
	savedRaw, _ := json.Marshal(tape.Args)
	if !bytes.Equal(argsRaw, savedRaw) {
		return "", nil, fail
	}
	var receipt struct {
		Success bool   `json:"success"`
		Frames  int    `json:"frames"`
		Error   string `json:"error,omitempty"`
	}
	d := json.NewDecoder(bytes.NewReader(values[".receipt"]))
	d.DisallowUnknownFields()
	if !uniqueSuiteKeys(values[".receipt"]) || d.Decode(&receipt) != nil || d.Decode(new(any)) != io.EOF || !receipt.Success || receipt.Error != "" || receipt.Frames != len(tape.Frames) {
		return "", nil, fail
	}
	raw, err := json.Marshal(hashes)
	if err != nil {
		return "", nil, fail
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), tape, nil
}

// AuditRunnerSuiteArtifacts runs after all original root cleanup. Each role's
// immutable seal pins its four artifacts across that cleanup interval.
func AuditRunnerSuiteArtifacts(home, scenario string) (map[string]*Tape, error) {
	fail := errors.New("runner final artifact inventory changed")
	profile, ok := RunnerProfiles()[scenario]
	if !ok {
		return nil, fail
	}
	dir := filepath.Join(home, RunnerMetadataDirectory)
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 2+5*len(profile.Roles) {
		return nil, fail
	}
	expected := map[string]bool{"config.json": true, "claude": true}
	for role := range profile.Roles {
		for _, suffix := range []string{".claim", ".finish", ".json", ".receipt", ".seal"} {
			expected[role+suffix] = true
		}
	}
	for _, entry := range entries {
		if !expected[entry.Name()] {
			return nil, fail
		}
	}
	tapes := map[string]*Tape{}
	for role := range profile.Roles {
		digest, tape, err := auditRunnerRole(home, scenario, role)
		if err != nil {
			return nil, err
		}
		raw, err := runnerPrivateRead(filepath.Join(dir, role+".seal"), 128)
		if err != nil || string(raw) != digest {
			return nil, fail
		}
		tapes[role] = tape
	}
	return tapes, nil
}
