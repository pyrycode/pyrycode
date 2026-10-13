package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// readConfig pins the parent and file so admission checks describe the same
// filesystem targets as the bytes read, including any followed symlink targets.
func readConfig(path string) ([]byte, os.FileInfo, os.FileInfo, error) {
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		if errors.Is(err, os.ErrPermission) {
			// Legacy configs can live in searchable, unreadable directories.
			// Missing handle metadata prevents admission maps using this path.
			data, readErr := os.ReadFile(path)
			return data, nil, nil, readErr
		}
		return nil, nil, nil, err
	}
	defer func() { _ = dir.Close() }()
	fd, err := unix.Openat(int(dir.Fd()), filepath.Base(path), unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, nil, nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	defer func() { _ = f.Close() }()
	file, err := f.Stat()
	if err != nil {
		return nil, nil, nil, err
	}
	parent, err := dir.Stat()
	if err != nil {
		return nil, nil, nil, err
	}
	data, err := io.ReadAll(f)
	return data, file, parent, err
}

// decodeObject rejects duplicate keys for uniqueField, or all keys when it is
// empty. Other fields keep their existing JSON decoding compatibility.
func decodeObject(data []byte, uniqueField string) (map[string]json.RawMessage, error) {
	var object map[string]json.RawMessage
	invalid := errors.New("config: parse: invalid JSON configuration object")
	if err := json.Unmarshal(data, &object); err != nil {
		return nil, invalid
	}
	if object == nil {
		return nil, nil
	}
	// Validate the complete document before walking its original key occurrences.
	dec := json.NewDecoder(bytes.NewReader(data))
	if _, err := dec.Token(); err != nil {
		return nil, invalid
	}
	seen := make(map[string]bool)
	for dec.More() {
		name, err := dec.Token()
		key, ok := name.(string)
		if err != nil || !ok {
			return nil, invalid
		}
		if (uniqueField == "" || key == uniqueField) && seen[key] {
			return nil, errors.New("config: duplicate relay admission field or identity")
		}
		seen[key] = true
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			return nil, invalid
		}
	}
	return object, nil
}

func validateAdmission(root map[string]json.RawMessage, file, parent os.FileInfo) (map[string]string, error) {
	for key := range root {
		if key != "relay_admission_tokens" && strings.EqualFold(key, "relay_admission_tokens") {
			return nil, errors.New("config: relay admission field must use relay_admission_tokens spelling")
		}
	}
	raw, present := root["relay_admission_tokens"]
	if !present {
		return nil, nil
	}
	entries, err := decodeObject(raw, "")
	if err != nil {
		return nil, err
	}
	if entries == nil {
		return nil, errors.New("config: relay admission tokens must be an object of credentials")
	}
	tokens := make(map[string]string, len(entries))
	for identity, value := range entries {
		var token string
		if err := json.Unmarshal(value, &token); err != nil {
			return nil, errors.New("config: relay admission tokens must be an object of credentials")
		}
		if len(token) != 64 {
			return nil, errors.New("config: relay admission tokens must be 64 lowercase hexadecimal characters")
		}
		for _, c := range []byte(token) {
			if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
				return nil, errors.New("config: relay admission tokens must be 64 lowercase hexadecimal characters")
			}
		}
		tokens[identity] = token
	}
	if !privateAdmissionStorage(file, 0o600) || !privateAdmissionStorage(parent, os.ModeDir|0o700) {
		return nil, errors.New("config: relay admission requires an effective-user-owned 0600 file and 0700 parent directory")
	}
	return tokens, nil
}

func privateAdmissionStorage(info os.FileInfo, mode os.FileMode) bool {
	if info == nil {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == uint32(os.Geteuid()) && info.Mode() == mode
}
