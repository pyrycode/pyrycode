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
	"strings"

	"github.com/pyrycode/tui-driver/pkg/tuidriver"
)

type LifecycleSnapshotStage struct {
	Version, OwnerPID, BrokerPID, UserSequence               int
	Role, Kind, Revision, SourceBinarySHA256, SnapshotSHA256 string
}

func lifecycleUserInputs(t *Tape) int {
	n := 0
	for _, frame := range t.Frames {
		if frame.Direction == "stdin" {
			v, e := object(frame.Data)
			if e == nil && v["type"] == "user" {
				n++
			}
		}
	}
	return n
}
func LifecycleSnapshotRoles(scenario string) []string {
	switch scenario {
	case "session-rotation":
		return []string{"bootstrap", "rotated"}
	case "session-resume":
		return []string{"bootstrap"}
	}
	return nil
}

func LifecycleNativePath(home, session string) string {
	return tuidriver.SessionJSONLPath(home, filepath.Join(home, "work"), session)
}
func DecodeSessionSnapshot(r io.Reader) (SessionSnapshot, error) {
	fail := errors.New("invalid native session snapshot carrier")
	raw, e := io.ReadAll(io.LimitReader(r, MaxBytes+1))
	if e != nil || len(raw) > MaxBytes || !uniqueSuiteKeys(raw) {
		return SessionSnapshot{}, fail
	}
	var s SessionSnapshot
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&s) != nil || d.Decode(new(any)) != io.EOF {
		return s, fail
	}
	if _, e = s.Bytes(LifecycleRotationSession, "/tmp/owned-snapshot-validation"); e != nil {
		return s, fail
	}
	return s, nil
}
func (s SessionSnapshot) Save(path, home string) error {
	if _, e := s.Bytes(LifecycleRotationSession, "/tmp/owned-snapshot-validation"); e != nil {
		return e
	}
	raw, e := json.Marshal(s)
	if e != nil || len(raw) > MaxBytes {
		return errors.New("native session snapshot exceeded carrier limit")
	}
	return publishSnapshotAt(path, home, raw, func(w io.Writer, p []byte) (int, error) { return w.Write(p) })
}

// ReadLifecycleSnapshots validates the causal user-delivery witness, complete
// point-in-time native JSONL prefix, and the exact private snapshot inventory.
// Later native appends remain live activity; no partial tail enters the fixture.
func ReadLifecycleSnapshots(home, scenario, revision, sourceSHA string) (map[string]SessionSnapshot, map[string]string, error) {
	fail := errors.New("invalid causal native snapshot inventory")
	snapshots, hashes := map[string]SessionSnapshot{}, map[string]string{}
	snapshotRoles := LifecycleSnapshotRoles(scenario)
	if len(snapshotRoles) == 0 {
		return snapshots, hashes, nil
	}
	profile := LifecycleProfiles()[scenario]
	dir := filepath.Join(home, "recording-snapshots")
	info, e := os.Lstat(dir)
	if e != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		return nil, nil, fail
	}
	entries, e := os.ReadDir(dir)
	if e != nil || len(entries) != 2*len(snapshotRoles) {
		return nil, nil, fail
	}
	expected := map[string]bool{}
	for _, role := range snapshotRoles {
		binding, e := ReadLifecycleRole(home, scenario, role)
		if e != nil {
			return nil, nil, fail
		}
		prefix := filepath.Join(home, "recording-children", role)
		var job LifecycleJob
		var broker int
		var delivery LifecycleDelivery
		if ReadLifecycleControl(prefix+".job", MaxBytes, &job) != nil || ReadLifecycleControl(prefix+".broker-pid", 16384, &broker) != nil || ReadLifecycleControl(prefix+".user-000001", 16384, &delivery) != nil || delivery.Version != 1 || delivery.Kind != "user-input-forwarded" || delivery.Sequence != 1 || delivery.OwnerPID != job.Config.OwnerPID || delivery.BrokerPID != broker {
			return nil, nil, fail
		}
		var stage LifecycleSnapshotStage
		values := map[string][]byte{}
		for _, suffix := range []string{".json", ".stage"} {
			expected[role+suffix] = true
			raw, e := runnerPrivateRead(filepath.Join(dir, role+suffix), MaxBytes)
			if e != nil {
				return nil, nil, fail
			}
			values[suffix] = raw
			sum := sha256.Sum256(raw)
			hashes["recording-snapshots/"+role+suffix] = hex.EncodeToString(sum[:])
		}
		decoder := json.NewDecoder(bytes.NewReader(values[".stage"]))
		decoder.DisallowUnknownFields()
		if len(values[".stage"]) > 16384 || !uniqueSuiteKeys(values[".stage"]) || decoder.Decode(&stage) != nil || decoder.Decode(new(any)) != io.EOF {
			return nil, nil, fail
		}
		s, e := DecodeSessionSnapshot(bytes.NewReader(values[".json"]))
		if e != nil {
			return nil, nil, fail
		}
		sum := sha256.Sum256(values[".json"])
		expectedKind := "native-jsonl-after-user"
		if scenario == "session-resume" {
			expectedKind = "native-jsonl-after-owned-eviction"
			var terminal LifecycleReceipt
			if ReadLifecycleControl(prefix+".receipt", 16384, &terminal) != nil || !terminal.Success || terminal.OwnerPID != job.Config.OwnerPID || terminal.BrokerPID != broker {
				return nil, nil, fail
			}
		}
		if stage.Version != 1 || stage.Role != role || stage.Kind != expectedKind || stage.OwnerPID != delivery.OwnerPID || stage.BrokerPID != broker || stage.UserSequence != 1 || stage.Revision != revision || stage.SourceBinarySHA256 != sourceSHA || stage.SnapshotSHA256 != hex.EncodeToString(sum[:]) {
			return nil, nil, fail
		}
		want, e := s.Bytes(binding.SessionID, home)
		if e != nil {
			return nil, nil, fail
		}
		current, e := readStableSessionFile(LifecycleNativePath(home, binding.SessionID), home, io.ReadAll)
		if e != nil || !bytes.HasPrefix(current, want) {
			return nil, nil, fail
		}
		snapshots[role] = s
	}
	for _, entry := range entries {
		if !expected[entry.Name()] {
			return nil, nil, fail
		}
	}
	inputs := filepath.Join(home, "recording-snapshot-inputs")
	inputInfo, e := os.Lstat(inputs)
	if e != nil || !inputInfo.IsDir() || inputInfo.Mode().Perm() != 0700 {
		return nil, nil, fail
	}
	inputEntries, e := os.ReadDir(inputs)
	if e != nil {
		return nil, nil, fail
	}
	var mode string
	for _, role := range profile.Roles {
		if _, err := ReadLifecycleRole(home, scenario, role); err != nil {
			return nil, nil, fail
		}
		var job LifecycleJob
		if ReadLifecycleControl(filepath.Join(home, "recording-children", role+".job"), MaxBytes, &job) != nil {
			return nil, nil, fail
		}
		if mode != "" && mode != job.Config.Mode {
			return nil, nil, fail
		}
		mode = job.Config.Mode
	}
	if mode != "capture" && mode != "replay" {
		return nil, nil, fail
	}
	if mode == "capture" && len(inputEntries) != 0 || mode == "replay" && len(inputEntries) != len(snapshotRoles) {
		return nil, nil, fail
	}
	for _, entry := range inputEntries {
		role := strings.TrimSuffix(entry.Name(), ".json")
		if _, ok := snapshots[role]; !ok || entry.Name() != role+".json" {
			return nil, nil, fail
		}
		raw, e := runnerPrivateRead(filepath.Join(inputs, entry.Name()), MaxBytes)
		if e != nil {
			return nil, nil, fail
		}
		saved, e := DecodeSessionSnapshot(bytes.NewReader(raw))
		if e != nil {
			return nil, nil, fail
		}
		a, _ := json.Marshal(saved)
		b, _ := json.Marshal(snapshots[role])
		if !bytes.Equal(a, b) {
			return nil, nil, fail
		}
		sum := sha256.Sum256(raw)
		hashes[fmt.Sprintf("recording-snapshot-inputs/%s", entry.Name())] = hex.EncodeToString(sum[:])
	}
	return snapshots, hashes, nil
}
