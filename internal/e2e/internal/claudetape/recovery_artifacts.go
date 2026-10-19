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
)

type RecoveryEndpoint struct{ Daemon, Socket string }

func recoveryRoleValid(role string) bool { return role == "retained" || role == "dropped" }
func auditRecoveryRole(root, role string) (string, RecoveryRole, error) {
	fail := errors.New("recovery role artifact inventory changed")
	var result RecoveryRole
	if !recoveryRoleValid(role) {
		return "", result, fail
	}
	meta := filepath.Join(root, RecoveryMetadataDirectory)
	info, err := os.Lstat(meta)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		return "", result, fail
	}
	if _, err := os.Lstat(filepath.Join(meta, "unexpected")); !errors.Is(err, os.ErrNotExist) {
		return "", result, fail
	}
	hashes := map[string]string{}
	values := map[string][]byte{}
	for _, suffix := range []string{".claim", ".finish", ".json", ".receipt", ".endpoint", ".transcript"} {
		limit := MaxBytes
		if suffix == ".claim" || suffix == ".finish" {
			limit = 0
		}
		if suffix == ".receipt" || suffix == ".endpoint" {
			limit = 16384
		}
		raw, err := runnerPrivateRead(filepath.Join(meta, role+suffix), limit)
		if err != nil {
			return "", result, err
		}
		values[suffix] = raw
		sum := sha256.Sum256(raw)
		hashes[suffix] = hex.EncodeToString(sum[:])
	}
	decode := func(raw []byte, target any) bool {
		d := json.NewDecoder(bytes.NewReader(raw))
		d.DisallowUnknownFields()
		return uniqueSuiteKeys(raw) && d.Decode(target) == nil && d.Decode(new(any)) == io.EOF
	}
	var endpoint RecoveryEndpoint
	if !decode(values[".endpoint"], &endpoint) || !filepath.IsAbs(endpoint.Daemon) || !filepath.IsAbs(endpoint.Socket) {
		return "", result, fail
	}
	tape, err := Decode(bytes.NewReader(values[".json"]))
	if err != nil || tape.Scenario != RecoveryScenario+"-"+role {
		return "", result, fail
	}
	var cwd string
	if json.Unmarshal(tape.Files["$cwd"], &cwd) != nil || cwd != "work" {
		return "", result, fail
	}
	var receipt struct {
		Success bool   `json:"success"`
		Frames  int    `json:"frames"`
		Error   string `json:"error,omitempty"`
	}
	if !decode(values[".receipt"], &receipt) || !receipt.Success || receipt.Error != "" || receipt.Frames != len(tape.Frames) {
		return "", result, fail
	}
	if !decode(values[".transcript"], &result.Transcript) || result.Transcript.Validate() != nil {
		return "", result, fail
	}
	result.Tape = tape
	raw, err := json.Marshal(hashes)
	if err != nil {
		return "", result, fail
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), result, nil
}
func AuditRecoveryRole(root, role string) (string, error) {
	digest, _, err := auditRecoveryRole(root, role)
	return digest, err
}
func AuditRecoverySuiteArtifacts(root string) (map[string]RecoveryRole, error) {
	fail := errors.New("recovery final artifact inventory changed")
	meta := filepath.Join(root, RecoveryMetadataDirectory)
	entries, err := os.ReadDir(meta)
	if err != nil || len(entries) != 16 {
		return nil, fail
	}
	expected := map[string]bool{"config.json": true, "claude": true}
	for _, role := range RecoveryRoles() {
		for _, suffix := range []string{".claim", ".finish", ".json", ".receipt", ".endpoint", ".transcript", ".seal"} {
			expected[role+suffix] = true
		}
	}
	for _, entry := range entries {
		if !expected[entry.Name()] {
			return nil, fail
		}
	}
	roles := map[string]RecoveryRole{}
	for _, role := range RecoveryRoles() {
		digest, value, err := auditRecoveryRole(root, role)
		if err != nil {
			return nil, err
		}
		seal, err := runnerPrivateRead(filepath.Join(meta, role+".seal"), 128)
		if err != nil || string(seal) != digest {
			return nil, fail
		}
		roles[role] = value
	}
	return roles, nil
}
