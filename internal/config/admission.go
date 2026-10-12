package config

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
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

func validateAdmission(root map[string]json.RawMessage, file, parent os.FileInfo) error {
	raw, present := root["relay_admission_tokens"]
	if !present {
		return nil
	}
	var tokens map[string]string
	if err := json.Unmarshal(raw, &tokens); err != nil || tokens == nil {
		return errors.New("config: relay admission tokens must be an object of credentials")
	}
	for _, token := range tokens {
		if len(token) != 64 {
			return errors.New("config: relay admission tokens must be 64 lowercase hexadecimal characters")
		}
		for _, c := range []byte(token) {
			if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
				return errors.New("config: relay admission tokens must be 64 lowercase hexadecimal characters")
			}
		}
	}
	if !privateAdmissionStorage(file, 0o600) || !privateAdmissionStorage(parent, os.ModeDir|0o700) {
		return errors.New("config: relay admission requires an effective-user-owned 0600 file and 0700 parent directory")
	}
	return nil
}

func privateAdmissionStorage(info os.FileInfo, mode os.FileMode) bool {
	if info == nil {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == uint32(os.Geteuid()) && info.Mode() == mode
}
