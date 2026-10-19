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
	"reflect"
)

// AuditLifecycleArtifacts pins all prefix evidence before original cleanup and
// again afterwards. A seal-ended child gains only its final EOF receipt during
// cleanup; its tape and every other launch/action artifact remain immutable.
func AuditLifecycleArtifacts(home, scenario, revision, sourceSHA, daemonSHA string, complete bool) (string, map[string]*LifecycleTape, error) {
	fail := errors.New("lifecycle artifact inventory changed")
	profile, ok := LifecycleProfiles()[scenario]
	if !ok {
		return "", nil, fail
	}
	if _, e := os.Lstat(filepath.Join(home, "recording-unexpected")); !errors.Is(e, os.ErrNotExist) {
		return "", nil, fail
	}
	hashes := map[string]string{}
	read := func(path string, limit int, pin bool) ([]byte, error) {
		raw, e := runnerPrivateRead(path, limit)
		if e != nil {
			return nil, e
		}
		if pin {
			rel, e := filepath.Rel(home, path)
			if e != nil {
				return nil, e
			}
			sum := sha256.Sum256(raw)
			hashes[rel] = hex.EncodeToString(sum[:])
		}
		return raw, nil
	}
	decode := func(raw []byte, v any) bool {
		d := json.NewDecoder(bytes.NewReader(raw))
		d.DisallowUnknownFields()
		return uniqueSuiteKeys(raw) && d.Decode(v) == nil && d.Decode(new(any)) == io.EOF
	}
	for _, dir := range []string{"recording-roles", "recording-children"} {
		info, e := os.Lstat(filepath.Join(home, dir))
		if e != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
			return "", nil, fail
		}
	}
	finishRaw, e := read(filepath.Join(home, "finish"), 16384, true)
	if e != nil {
		return "", nil, e
	}
	var finish struct{}
	if !decode(finishRaw, &finish) {
		return "", nil, fail
	}
	endpointRaw, e := read(filepath.Join(home, "recording-endpoint.json"), 16384, true)
	if e != nil {
		return "", nil, e
	}
	var endpoint struct{ Daemon, Socket string }
	if !decode(endpointRaw, &endpoint) || !filepath.IsAbs(endpoint.Daemon) || !filepath.IsAbs(endpoint.Socket) {
		return "", nil, fail
	}
	type roleBinding struct{ Role, ConversationID, SessionID, WorkDir string }
	entries, e := os.ReadDir(filepath.Join(home, "recording-roles"))
	if e != nil || len(entries) != 1 || entries[0].Name() != "created-1.json" {
		return "", nil, fail
	}
	raw, e := read(filepath.Join(home, "recording-roles", "created-1.json"), 16384, true)
	if e != nil {
		return "", nil, e
	}
	var created roleBinding
	if !decode(raw, &created) || created.Role != "created-1" || !poolUUID.MatchString(created.SessionID) || created.SessionID == LifecycleBootstrapSession || !poolUUID.MatchString(created.ConversationID) || created.WorkDir != filepath.Join(home, "work") {
		return "", nil, fail
	}
	expectedFiles := map[string]bool{}
	tapes := map[string]*LifecycleTape{}
	pids := map[int]bool{}
	for _, role := range profile.Roles {
		prefix := filepath.Join(home, "recording-children", role)
		end := LifecycleEnd(scenario, role)
		suffixes := []string{".claim", ".json", ".job", ".broker-pid"}
		if end == "seal" {
			suffixes = append(suffixes, ".prefix")
			if complete {
				suffixes = append(suffixes, ".receipt")
			}
		} else {
			suffixes = append(suffixes, ".action", ".action.ready", ".receipt", ".stopped")
		}
		values := map[string][]byte{}
		for _, suffix := range suffixes {
			expectedFiles[role+suffix] = true
			limit := 16384
			if suffix == ".json" || suffix == ".job" {
				limit = MaxBytes
			}
			v, e := read(prefix+suffix, limit, !(end == "seal" && suffix == ".receipt"))
			if e != nil {
				return "", nil, e
			}
			values[suffix] = v
		}
		if len(values[".claim"]) != 0 {
			return "", nil, fail
		}
		tape, e := DecodeLifecycle(bytes.NewReader(values[".json"]))
		if e != nil || tape.End != end || tape.Tape.Scenario != scenario+"-"+role {
			return "", nil, fail
		}
		var job LifecycleJob
		var brokerPID int
		if !decode(values[".job"], &job) || !decode(values[".broker-pid"], &brokerPID) || job.Config.OwnerPID <= 1 || brokerPID <= 1 || brokerPID == job.Config.OwnerPID || pids[brokerPID] || pids[job.Config.OwnerPID] {
			return "", nil, fail
		}
		pids[brokerPID], pids[job.Config.OwnerPID] = true, true
		session, conversation := LifecycleBootstrapSession, ""
		if role == "created-1" {
			session, conversation = created.SessionID, created.ConversationID
		}
		c := job.Config
		if job.Role != role || job.SessionID != session || job.ConversationID != conversation || job.WorkDir != filepath.Join(home, "work") || job.SourceBinarySHA256 != sourceSHA || job.Daemon != endpoint.Daemon || job.Socket != endpoint.Socket || c.Scenario != scenario+"-"+role || c.Home != home || c.WorkDir != job.WorkDir || c.Path != prefix+".json" || c.Claim != prefix+".claim" || c.Finish != filepath.Join(home, "finish") || c.Kill != prefix+".action" || c.Abort != prefix+".abort" || c.End != end || !c.Deadline.IsZero() || c.Producer.Revision != revision || (c.Mode != "capture" && c.Mode != "replay") {
			return "", nil, fail
		}
		if c.Mode == "capture" && (!reflect.DeepEqual(c.Producer, tape.Tape.Producer) || c.Producer.BinarySHA256 != sourceSHA) {
			return "", nil, fail
		}
		b, e := NewDaemonBinding(job.Args, home, job.WorkDir, job.Daemon, job.Socket)
		if e != nil || b.session != session || !reflect.DeepEqual(b.Args(), tape.Tape.Args) {
			return "", nil, fail
		}
		var cwd string
		if json.Unmarshal(tape.Tape.Files["$cwd"], &cwd) != nil || cwd != "work" {
			return "", nil, fail
		}
		receipts := []string{".receipt"}
		if end == "seal" {
			receipts = []string{".prefix"}
			if complete {
				receipts = append(receipts, ".receipt")
			}
		}
		for _, suffix := range receipts {
			var receipt LifecycleReceipt
			if !decode(values[suffix], &receipt) || !receipt.Success || receipt.Error != "" || receipt.Frames != len(tape.Tape.Frames) || receipt.OwnerPID != c.OwnerPID || receipt.BrokerPID != brokerPID {
				return "", nil, fail
			}
		}
		if end != "seal" {
			var action LifecycleKill
			if !decode(values[".action"], &action) || action.PID != c.OwnerPID || action.Signal != end {
				return "", nil, fail
			}
			var ack LifecycleKill
			if !decode(values[".action.ready"], &ack) || ack != action {
				return "", nil, fail
			}
			var stopped LifecycleStopObservation
			if !decode(values[".stopped"], &stopped) || stopped.PID != c.OwnerPID || stopped.Kind != "not-running" {
				return "", nil, fail
			}

		}
		tapes[role] = tape
	}
	children, e := os.ReadDir(filepath.Join(home, "recording-children"))
	if e != nil || len(children) != len(expectedFiles) {
		return "", nil, fail
	}
	for _, entry := range children {
		if !expectedFiles[entry.Name()] {
			return "", nil, fail
		}
	}
	// The source-bound daemon checksum is a parent-owned constant. Bind it to
	// this digest even though no executable bytes are embedded in the fixture.
	hashes["$daemon-sha256"] = daemonSHA
	raw, e = json.Marshal(hashes)
	if e != nil {
		return "", nil, fail
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), tapes, nil
}

func ReadLifecycleControl(path string, limit int, target any) error {
	raw, err := runnerPrivateRead(path, limit)
	if err != nil {
		return err
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if !uniqueSuiteKeys(raw) || d.Decode(target) != nil || d.Decode(new(any)) != io.EOF {
		return errors.New("invalid lifecycle control artifact")
	}
	return nil
}
