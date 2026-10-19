package claudetape

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

func checkpointFileError() error { return errors.New("client checkpoint file protocol failed") }
func checkpointName(sequence uint64, kind string) string {
	return fmt.Sprintf("%06d.%s", sequence, kind)
}
func checkpointDirectory(dir string) error {
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		return checkpointFileError()
	}
	return nil
}
func NewCheckpointDirectory(dir string) error {
	if err := os.Mkdir(dir, 0700); err != nil {
		return checkpointFileError()
	}
	if err := os.Chmod(dir, 0700); err != nil {
		return checkpointFileError()
	}
	return nil
}

// publishClosedFile never exposes a partial ready file or overwrites evidence.
// The parent directory must already exist; a seal must never be undone by mkdir.
func publishClosedFile(dir, name string, raw []byte) error {
	if checkpointDirectory(dir) != nil {
		return checkpointFileError()
	}
	pending := filepath.Join(dir, name+".pending")
	f, err := os.OpenFile(pending, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return checkpointFileError()
	}
	if err = f.Chmod(0600); err != nil {
		_ = f.Close()
		return checkpointFileError()
	}
	n, writeErr := f.Write(raw)
	closeErr := f.Close()
	if writeErr != nil || n != len(raw) || closeErr != nil {
		return checkpointFileError()
	}
	if os.Link(pending, filepath.Join(dir, name)) != nil {
		return checkpointFileError()
	}
	if os.Remove(pending) != nil {
		return checkpointFileError()
	}
	return nil
}
func readCheckpointFile(path string, limit int) ([]byte, bool, error) {
	before, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil || !before.Mode().IsRegular() || before.Mode().Perm() != 0600 || before.Size() > int64(limit) {
		return nil, false, checkpointFileError()
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, false, checkpointFileError()
	}
	defer f.Close()
	after, err := f.Stat()
	if err != nil || !os.SameFile(before, after) || !after.Mode().IsRegular() || after.Mode().Perm() != 0600 || after.Size() > int64(limit) {
		return nil, false, checkpointFileError()
	}
	raw, err := io.ReadAll(io.LimitReader(f, int64(limit+1)))
	if err != nil || len(raw) > limit {
		return nil, false, checkpointFileError()
	}
	return raw, true, nil
}
func checkpointAck(raw []byte, c ClientCheckpoint) []byte {
	sum := sha256.Sum256(raw)
	ack, _ := json.Marshal(struct {
		Type     string `json:"type"`
		Sequence uint64 `json:"sequence"`
		SHA256   string `json:"sha256"`
	}{"client_checkpoint_ack", c.Sequence, hex.EncodeToString(sum[:])})
	return ack
}

// PublishCheckpoint succeeds only after complete publication, pending removal,
// and the exact helper acknowledgement. It cannot recreate a sealed directory.
func PublishCheckpoint(dir string, c ClientCheckpoint, budget time.Duration) error {
	raw, err := json.Marshal(c)
	if err != nil {
		return checkpointFileError()
	}
	if _, err = DecodeClientCheckpoint(raw); err != nil {
		return err
	}
	if err = publishClosedFile(dir, checkpointName(c.Sequence, "ready"), raw); err != nil {
		return err
	}
	want := checkpointAck(raw, c)
	deadline := time.Now().Add(budget)
	for time.Now().Before(deadline) {
		if checkpointDirectory(dir) != nil {
			return checkpointFileError()
		}
		got, exists, err := readCheckpointFile(filepath.Join(dir, checkpointName(c.Sequence, "ack")), 512)
		if err != nil {
			return err
		}
		if exists {
			if !bytes.Equal(got, want) {
				return checkpointFileError()
			}
			return nil
		}
		time.Sleep(time.Millisecond)
	}
	return checkpointFileError()
}

func ReadCheckpoint(dir string, sequence uint64) ([]byte, bool, error) {
	if sequence == 0 || sequence > MaxFrames {
		return nil, false, checkpointFileError()
	}
	if checkpointDirectory(dir) != nil {
		return nil, false, checkpointFileError()
	}
	raw, exists, err := readCheckpointFile(filepath.Join(dir, checkpointName(sequence, "ready")), MaxClientCheckpointBytes)
	if err != nil || !exists {
		return nil, exists, err
	}
	c, err := DecodeClientCheckpoint(raw)
	if err != nil || c.Sequence != sequence {
		return nil, false, checkpointFileError()
	}
	canonical, _ := json.Marshal(c)
	if !bytes.Equal(raw, canonical) {
		return nil, false, checkpointFileError()
	}
	return raw, true, nil
}

// AcknowledgeCheckpoint is called only after the recorder has accepted/appended
// the action and finished forwarding any newly released replay outputs.
func AcknowledgeCheckpoint(dir string, raw []byte) error {
	c, err := DecodeClientCheckpoint(raw)
	if err != nil {
		return err
	}
	return publishClosedFile(dir, checkpointName(c.Sequence, "ack"), checkpointAck(raw, c))
}

// SealCheckpointDirectory atomically revokes every publisher's old pathname,
// then audits the frozen exact inventory. An in-flight pending file fails seal;
// later path-only publishers cannot create a ready entry in the moved directory.
func SealCheckpointDirectory(dir string, accepted [][]byte) (string, error) {
	if len(accepted) == 0 || len(accepted) > MaxFrames {
		return "", checkpointFileError()
	}
	if checkpointDirectory(dir) != nil {
		return "", checkpointFileError()
	}
	sealedParent, err := os.MkdirTemp(filepath.Dir(dir), "sealed-client-checkpoints-")
	if err != nil {
		return "", checkpointFileError()
	}
	sealed := filepath.Join(sealedParent, "actions") // New private parent, no destination to replace.
	if _, err = os.Lstat(sealed); !os.IsNotExist(err) {
		return "", checkpointFileError()
	}
	if os.Rename(dir, sealed) != nil {
		return "", checkpointFileError()
	}
	entries, err := os.ReadDir(sealed)
	if err != nil || len(entries) != 2*len(accepted) {
		return sealed, checkpointFileError()
	}
	expected := map[string][]byte{}
	for i, raw := range accepted {
		c, err := DecodeClientCheckpoint(raw)
		if err != nil || c.Sequence != uint64(i+1) {
			return sealed, checkpointFileError()
		}
		expected[checkpointName(c.Sequence, "ready")] = raw
		expected[checkpointName(c.Sequence, "ack")] = checkpointAck(raw, c)
	}
	for _, entry := range entries {
		want, ok := expected[entry.Name()]
		if !ok {
			return sealed, checkpointFileError()
		}
		got, exists, err := readCheckpointFile(filepath.Join(sealed, entry.Name()), MaxClientCheckpointBytes)
		if err != nil || !exists || !bytes.Equal(got, want) {
			return sealed, checkpointFileError()
		}
	}
	return sealed, nil
}
